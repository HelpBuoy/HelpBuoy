package identity

import (
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestEphemeral(t *testing.T) {
	k, err := Ephemeral("anna@PC")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(k.Line, "ssh-ed25519 AAAA") || !strings.HasSuffix(k.Line, " anna@PC") {
		t.Fatalf("line %q", k.Line)
	}
	pub, comment, _, _, err := ssh.ParseAuthorizedKey([]byte(k.Line))
	if err != nil || comment != "anna@PC" {
		t.Fatal(err, comment)
	}
	if ssh.FingerprintSHA256(pub) != k.Fingerprint || k.Dir != "" {
		t.Fatal("fingerprint or dir")
	}
}
