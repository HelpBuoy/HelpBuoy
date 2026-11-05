// Package appkey manages the application-specific Ed25519 key of the computer
// and desk apps (PLAN.md 6.1): standard OpenSSH files id_ed25519 and
// id_ed25519.pub in the app's data directory, not encrypted, created exactly
// once, never regenerated or rewritten automatically, removable only by an
// explicit Remove call from the app's UI.
package appkey

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/helpbuoy/helpbuoy/proto/identity"
)

// File names inside the data directory.
const (
	PrivateFile = "id_ed25519"
	PublicFile  = "id_ed25519.pub"
)

// ErrDamaged is returned (wrapped) when a key file exists but can't be read
// or parsed. The app must show it and offer Remove; it must not regenerate.
var ErrDamaged = errors.New("app key is damaged or unreadable")

// Key is a loaded app key.
type Key struct {
	Signer      ssh.Signer
	Public      ssh.PublicKey
	Comment     string // user@machine at creation time
	Line        string // authorized_keys line: "ssh-ed25519 AAAA... comment"
	Fingerprint string // SHA256:...
	Dir         string // directory holding the files
}

// Timing of the retry while another process is still writing the files it
// has just created: a damaged file younger than freshAge is retried until
// retryFor has passed.
const (
	freshAge   = 5 * time.Second
	retryFor   = 2 * time.Second
	retryPause = 10 * time.Millisecond
)

// errNoFiles means that neither key file exists.
var errNoFiles = fmt.Errorf("no app key: %w", fs.ErrNotExist)

// errRetry asks the caller to load again (another process created a file
// first).
var errRetry = errors.New("retry")

// LoadOrCreate loads the key from dir, or creates it if neither file exists.
// Creation: mkdir dir (0700), write both files with O_EXCL / CREATE_NEW, the
// private key in OpenSSH format without passphrase, mode 0600 on Unix and an
// owner+SYSTEM-only ACL with inheritance removed on Windows; comment is used
// for the public line. If only the private key exists, the public file is
// derived and written (create-only); if that write fails, for example in a
// read-only directory, the key is returned anyway. Existing files are never
// modified. created reports whether a new key was generated.
//
// When several processes call LoadOrCreate at the same time, exactly one
// creates the key and the others load it, waiting briefly for the files to be
// written completely.
func LoadOrCreate(dir, comment string) (k *Key, created bool, err error) {
	return loadRetrying(dir, comment, true)
}

// Load loads an existing key and never creates anything. It returns an error
// wrapping fs.ErrNotExist if there is no key, ErrDamaged if it is unusable.
// If the public file is missing, the key is derived from the private file and
// Comment is empty.
func Load(dir string) (*Key, error) {
	k, _, err := loadRetrying(dir, "", false)
	return k, err
}

// Remove deletes exactly PrivateFile and PublicFile from dir (missing files are
// fine). Only called after an explicit user confirmation.
func Remove(dir string) error {
	var errs []error
	for _, name := range []string{PrivateFile, PublicFile} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// loadRetrying loads the key, creating it first if create is set and there is
// none. A damaged file that was modified moments ago may still be in the
// middle of being written by another process, so it is retried for a while.
func loadRetrying(dir, comment string, create bool) (*Key, bool, error) {
	var deadline time.Time
	created, contended := false, false
	for {
		k, err := load(dir, comment, create)
		switch {
		case err == nil:
			return k, created, nil
		case errors.Is(err, errNoFiles) && create:
			err = createKey(dir, comment)
			if err == nil {
				created = true
				continue
			}
			if !errors.Is(err, fs.ErrExist) {
				return nil, false, err
			}
			contended = true // another process created it first
		case errors.Is(err, errRetry):
			contended = true
		case errors.Is(err, ErrDamaged) && (contended || created || recentlyModified(dir)):
		default:
			return nil, false, err
		}
		if deadline.IsZero() {
			deadline = time.Now().Add(retryFor)
		} else if time.Now().After(deadline) {
			if errors.Is(err, errRetry) || errors.Is(err, fs.ErrExist) {
				err = fmt.Errorf("%w: %s: files keep changing while loading", ErrDamaged, dir)
			}
			return nil, false, err
		}
		time.Sleep(retryPause)
	}
}

// recentlyModified reports whether one of the key files was modified within
// freshAge.
func recentlyModified(dir string) bool {
	for _, name := range []string{PrivateFile, PublicFile} {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err == nil && time.Since(fi.ModTime()) < freshAge {
			return true
		}
	}
	return false
}

func damaged(path, format string, args ...any) error {
	return fmt.Errorf("%w: %s: %s", ErrDamaged, path, fmt.Sprintf(format, args...))
}

// load reads the key files. If writePub is set and only the private key
// exists, the public file is derived and created.
func load(dir, comment string, writePub bool) (*Key, error) {
	privPath := filepath.Join(dir, PrivateFile)
	pubPath := filepath.Join(dir, PublicFile)

	privData, privErr := os.ReadFile(privPath)
	pubData, pubErr := os.ReadFile(pubPath)
	privMissing := errors.Is(privErr, fs.ErrNotExist)
	pubMissing := errors.Is(pubErr, fs.ErrNotExist)

	switch {
	case privMissing && pubMissing:
		return nil, fmt.Errorf("%w in %s", errNoFiles, dir)
	case privMissing:
		return nil, damaged(pubPath, "public key file without private key %s", privPath)
	case privErr != nil:
		return nil, damaged(privPath, "%v", privErr)
	case pubErr != nil && !pubMissing:
		return nil, damaged(pubPath, "%v", pubErr)
	}

	signer, err := parsePrivate(privData)
	if err != nil {
		return nil, damaged(privPath, "%v", err)
	}
	pub := signer.PublicKey()

	if pubMissing {
		if writePub {
			if err := writeFileExcl(pubPath, []byte(identity.Line(pub, comment)+"\n"), false); err != nil {
				if errors.Is(err, fs.ErrExist) {
					return nil, errRetry
				}
				// The private key is fine; writing the public file is
				// only a convenience, so a read-only directory does not
				// make the key unusable.
			}
		} else {
			comment = ""
		}
		return newKey(dir, signer, comment), nil
	}

	filePub, fileComment, err := identity.ParseLine(string(pubData))
	if err != nil {
		return nil, damaged(pubPath, "%v", err)
	}
	if !bytes.Equal(filePub.Marshal(), pub.Marshal()) {
		return nil, damaged(pubPath, "public key does not match private key %s", privPath)
	}
	return newKey(dir, signer, fileComment), nil
}

func newKey(dir string, signer ssh.Signer, comment string) *Key {
	pub := signer.PublicKey()
	return &Key{
		Signer:      signer,
		Public:      pub,
		Comment:     comment,
		Line:        identity.Line(pub, comment),
		Fingerprint: identity.Fingerprint(pub),
		Dir:         dir,
	}
}

// parsePrivate parses an unencrypted OpenSSH Ed25519 private key.
func parsePrivate(data []byte) (ssh.Signer, error) {
	if len(data) == 0 {
		return nil, errors.New("file is empty")
	}
	raw, err := ssh.ParseRawPrivateKey(data)
	if err != nil {
		var pm *ssh.PassphraseMissingError
		if errors.As(err, &pm) {
			return nil, errors.New("private key is encrypted")
		}
		return nil, err
	}
	var priv ed25519.PrivateKey
	switch p := raw.(type) {
	case *ed25519.PrivateKey:
		priv = *p
	case ed25519.PrivateKey:
		priv = p
	default:
		return nil, fmt.Errorf("not an ed25519 private key (%T)", raw)
	}
	if len(priv) != ed25519.PrivateKeySize {
		return nil, errors.New("invalid ed25519 private key length")
	}
	// The private key embeds its public half; check that it belongs to the
	// seed so a corrupted file can't produce a mismatching key.
	if !bytes.Equal(ed25519.NewKeyFromSeed(priv.Seed()), priv) {
		return nil, errors.New("ed25519 private key is inconsistent")
	}
	return ssh.NewSignerFromKey(priv)
}

// createKey generates a new key and writes both files create-only. It
// returns an error wrapping fs.ErrExist if the private file already exists.
func createKey(dir, comment string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	block, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return err
	}
	privPath := filepath.Join(dir, PrivateFile)
	if err := writeFileExcl(privPath, encodePEM(block.Type, block.Bytes), true); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return err
		}
		return fmt.Errorf("writing %s: %w", privPath, err)
	}
	pubPath := filepath.Join(dir, PublicFile)
	if err := writeFileExcl(pubPath, []byte(identity.Line(sshPub, comment)+"\n"), false); err != nil {
		// Someone derived the public file in the meantime: load decides
		// whether it matches. Any other error surfaces from load, which
		// will try to write it again.
		if !errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("writing %s: %w", pubPath, err)
		}
	}
	return nil
}

// encodePEM encodes data like ssh-keygen does: base64 lines of 70
// characters between BEGIN and END lines.
func encodePEM(typ string, data []byte) []byte {
	var b bytes.Buffer
	b.WriteString("-----BEGIN " + typ + "-----\n")
	s := base64.StdEncoding.EncodeToString(data)
	for len(s) > 70 {
		b.WriteString(s[:70])
		b.WriteByte('\n')
		s = s[70:]
	}
	if s != "" {
		b.WriteString(s)
		b.WriteByte('\n')
	}
	b.WriteString("-----END " + typ + "-----\n")
	return b.Bytes()
}

// writeFileExcl creates path, which must not exist yet, and writes data to
// it. A private file gets owner-only permissions (see createExcl). If writing
// fails after the file was created, the file this call just created is
// removed again, so that a failed write never leaves a damaged key behind.
func writeFileExcl(path string, data []byte, private bool) error {
	f, err := createExcl(path, private)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(path)
		return err
	}
	return nil
}
