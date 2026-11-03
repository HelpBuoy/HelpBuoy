// Package auth parses and checks the Ed25519 keys that identify techies and
// computers, and connects the SSH public key callback to the role lookup that
// happens after the handshake.
//
// The server never decides a role inside the SSH callback: the callback only
// accepts any Ed25519 key and stores it in the ssh.Permissions. Those
// permissions become visible on the ssh.ServerConn only after the computer has
// proven possession of the private key, so reading the key from them after
// ssh.NewServerConn returns gives a verified identity.
package auth

import (
	"errors"
	"fmt"
	"strings"
	"unicode"

	"golang.org/x/crypto/ssh"
)

// permKey is the ssh.Permissions extension that carries the authorized key.
const permKey = "helpbuoy-pubkey"

// ErrNotEd25519 is returned for keys of any other type.
var ErrNotEd25519 = errors.New("only ssh-ed25519 keys are accepted")

// ParseKey parses one authorized_keys style line ("ssh-ed25519 AAAA... comment").
// Leading and trailing white space is ignored and options are refused. It
// returns the key and its comment.
func ParseKey(line string) (ssh.PublicKey, string, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil, "", errors.New("empty key")
	}
	k, comment, options, rest, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		return nil, "", fmt.Errorf("parse key: %w", err)
	}
	if len(options) > 0 {
		return nil, "", errors.New("key options are not allowed")
	}
	if len(strings.TrimSpace(string(rest))) > 0 {
		return nil, "", errors.New("more than one key given")
	}
	if k.Type() != ssh.KeyAlgoED25519 {
		return nil, "", ErrNotEd25519
	}
	return k, strings.TrimSpace(comment), nil
}

// KeyLine returns the normalized authorized_keys line for k, with the comment
// appended when it is not empty.
func KeyLine(k ssh.PublicKey, comment string) string {
	s := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k)))
	if comment != "" {
		s += " " + comment
	}
	return s
}

// Fingerprint returns the SHA256 fingerprint of k, as "SHA256:...".
func Fingerprint(k ssh.PublicKey) string { return ssh.FingerprintSHA256(k) }

// PublicKeyCallback is the ssh.ServerConfig.PublicKeyCallback of the server.
// It accepts every Ed25519 key and remembers it in the permissions; the role
// is looked up only once the handshake has completed.
func PublicKeyCallback(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
	if key.Type() != ssh.KeyAlgoED25519 {
		return nil, ErrNotEd25519
	}
	return &ssh.Permissions{Extensions: map[string]string{permKey: string(key.Marshal())}}, nil
}

// VerifiedKey returns the key that authenticated a completed handshake.
func VerifiedKey(p *ssh.Permissions) (ssh.PublicKey, error) {
	if p == nil || p.Extensions[permKey] == "" {
		return nil, errors.New("connection has no verified key")
	}
	return ssh.ParsePublicKey([]byte(p.Extensions[permKey]))
}

// CleanName checks a techie or computer name: 1 to max runes, printable, no
// leading or trailing space. It returns the trimmed name.
func CleanName(s string, max int) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", errors.New("name is empty")
	}
	if len([]rune(s)) > max {
		return "", fmt.Errorf("name is longer than %d characters", max)
	}
	for _, r := range s {
		if !unicode.IsPrint(r) {
			return "", errors.New("name contains non-printable characters")
		}
	}
	return s, nil
}

// Truncate cuts s to at most max bytes on a rune boundary and drops
// non-printable characters. It is used for strings reported by apps.
func Truncate(s string, max int) string {
	var b strings.Builder
	for _, r := range s {
		if !unicode.IsPrint(r) {
			continue
		}
		if b.Len()+len(string(r)) > max {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}
