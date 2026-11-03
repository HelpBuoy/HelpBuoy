// Package identity gives the client its application-specific Ed25519 key
// (PLAN.md 6.1), kept in %LOCALAPPDATA%\helpbuoy through proto/appkey.
package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/mevdschee/helpbuoy/proto/appkey"
	"github.com/mevdschee/helpbuoy/proto/datadir"
	pid "github.com/mevdschee/helpbuoy/proto/identity"
)

// AppName is the data directory name below %LOCALAPPDATA%.
const AppName = "helpbuoy"

// LegacyAppName is the data directory name before the rename to HelpBuoy;
// Dir moves it to AppName on first start.
const LegacyAppName = "help-me-win"

// Dir returns the client's data directory. It does not create it.
func Dir() (string, error) { return datadir.Migrate(AppName, LegacyAppName) }

// Comment returns the comment for a new key: the current user@machine.
func Comment() string { return pid.UserMachine() }

// LoadOrCreate loads the app key from dir, creating it on the very first
// start. A damaged key is returned as an error wrapping appkey.ErrDamaged and
// is never replaced.
func LoadOrCreate(dir string) (k *appkey.Key, created bool, err error) {
	return appkey.LoadOrCreate(dir, Comment())
}

// Ephemeral returns a new key that only lives in memory. It is used by the
// hidden --demo mode and by tests; it is never written to disk.
func Ephemeral(comment string) (*appkey.Key, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, err
	}
	pub := signer.PublicKey()
	return &appkey.Key{
		Signer:      signer,
		Public:      pub,
		Comment:     comment,
		Line:        KeyLine(pub, comment),
		Fingerprint: ssh.FingerprintSHA256(pub),
	}, nil
}

// KeyLine returns the authorized_keys line "type base64[ comment]".
func KeyLine(key ssh.PublicKey, comment string) string {
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
	if comment != "" {
		line += " " + comment
	}
	return line
}
