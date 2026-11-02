package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func edKey(t *testing.T) ssh.PublicKey {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	k, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestParseKey(t *testing.T) {
	k := edKey(t)
	line := KeyLine(k, "anna@PC")
	got, comment, err := ParseKey("  " + line + "\r\n")
	if err != nil || comment != "anna@PC" || Fingerprint(got) != Fingerprint(k) {
		t.Fatalf("ParseKey: %v %q", err, comment)
	}
	if _, c, err := ParseKey(KeyLine(k, "")); err != nil || c != "" {
		t.Errorf("no comment: %v %q", err, c)
	}
	rk, _ := rsa.GenerateKey(rand.Reader, 1024)
	rpub, _ := ssh.NewPublicKey(&rk.PublicKey)
	for _, bad := range []string{
		"",
		"garbage",
		string(ssh.MarshalAuthorizedKey(rpub)),
		`command="ls" ` + line,
		line + "\n" + line,
	} {
		if _, _, err := ParseKey(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if !strings.HasPrefix(Fingerprint(k), "SHA256:") {
		t.Error(Fingerprint(k))
	}
}

func TestCallbackAndVerifiedKey(t *testing.T) {
	k := edKey(t)
	p, err := PublicKeyCallback(nil, k)
	if err != nil {
		t.Fatal(err)
	}
	got, err := VerifiedKey(p)
	if err != nil || Fingerprint(got) != Fingerprint(k) {
		t.Fatalf("VerifiedKey: %v", err)
	}
	if _, err := VerifiedKey(&ssh.Permissions{}); err == nil {
		t.Error("empty permissions accepted")
	}
	rk, _ := rsa.GenerateKey(rand.Reader, 1024)
	rpub, _ := ssh.NewPublicKey(&rk.PublicKey)
	if _, err := PublicKeyCallback(nil, rpub); err == nil {
		t.Error("rsa key accepted")
	}
}

func TestNames(t *testing.T) {
	if n, err := CleanName("  anna@PC ", 64); err != nil || n != "anna@PC" {
		t.Errorf("%q %v", n, err)
	}
	for _, bad := range []string{"", "   ", "a\x00b", strings.Repeat("x", 65)} {
		if _, err := CleanName(bad, 64); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if got := Truncate("ab\x1bcdé", 5); got != "abcd" {
		t.Errorf("Truncate = %q", got)
	}
}
