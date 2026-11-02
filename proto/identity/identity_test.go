package identity

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func newEd25519(t testing.TB) ssh.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	k, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func rawLine(k ssh.PublicKey) string {
	return strings.TrimSuffix(string(ssh.MarshalAuthorizedKey(k)), "\n")
}

func wrap(s string, n int, sep string) string {
	var b strings.Builder
	for len(s) > n {
		b.WriteString(s[:n])
		b.WriteString(sep)
		s = s[n:]
	}
	b.WriteString(s)
	return b.String()
}

func TestParsePasted(t *testing.T) {
	k := newEd25519(t)
	line := rawLine(k) // "ssh-ed25519 AAAA..."
	b64 := strings.TrimPrefix(line, "ssh-ed25519 ")

	tests := []struct {
		name    string
		text    string
		comment string
	}{
		{"plain", line + " anna@PC", "anna@PC"},
		{"no comment", line, ""},
		{"trailing newline", line + " anna@PC\n", "anna@PC"},
		{"crlf", "\r\n" + line + " anna@PC\r\n", "anna@PC"},
		{"spaces around", "   \t" + line + " anna@PC   ", "anna@PC"},
		{"wrapped lf", "ssh-ed25519 " + wrap(b64, 30, "\n") + " anna@PC", "anna@PC"},
		{"wrapped crlf", "ssh-ed25519\r\n" + wrap(b64, 17, "\r\n") + "\r\nanna@PC", "anna@PC"},
		{"wrapped spaces", "ssh-ed25519 " + wrap(b64, 10, " ") + " anna@PC", "anna@PC"},
		{"nbsp", "ssh-ed25519\u00a0" + b64 + "\u00a0anna@PC", "anna@PC"},
		{"zero width", "\ufeffssh-ed25519 " + wrap(b64, 20, "\u200b") + "\u200d anna@PC\u200c", "anna@PC"},
		{"zero width before comment", line + "\u200banna@PC", "anna@PC"},
		{"double quotes", `"` + line + ` anna@PC"`, "anna@PC"},
		{"smart quotes", "\u201c" + line + " anna@PC\u201d", "anna@PC"},
		{"single smart quotes", "\u2018" + line + " anna@PC\u2019", "anna@PC"},
		{"backticks", "`" + line + " anna@PC`", "anna@PC"},
		{"code block", "```\n" + line + " anna@PC\n```", "anna@PC"},
		{"sentence before", "here is my key: " + line + " anna@PC", "anna@PC"},
		{"sentence after", "here is my key: " + line + " anna@PC thanks a lot!", "anna@PC"},
		{"sentence after no comment", "my key: " + line + " thanks a lot", ""},
		{"sentence next line no comment", line + "\nthanks", ""},
		{"comment with period", "key " + line + " anna@PC.", "anna@PC"},
		{"comment in parens", line + " (anna@PC)", "anna@PC"},
		{"markdown bold", "*" + line + " anna@PC*", "anna@PC"},
		{"bare base64", "key: " + b64 + " anna@pc", "anna@pc"},
		{"rsa before ed25519", rsaLine(t) + " bob@x\n" + line + " anna@PC", "anna@PC"},
		{"domain style host", line + " anna@pc1.example.com", "anna@pc1.example.com"},
		{"type glued to blob", "ssh-ed25519\u200b" + b64 + " anna@PC", "anna@PC"},
		{"line separator", "ssh-ed25519\u2028" + b64 + "\u2028anna@PC", "anna@PC"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, comment, l, err := ParsePasted(tt.text)
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if !bytes.Equal(got.Marshal(), k.Marshal()) {
				t.Fatalf("wrong key")
			}
			if comment != tt.comment {
				t.Fatalf("comment = %q, want %q", comment, tt.comment)
			}
			want := line
			if tt.comment != "" {
				want += " " + tt.comment
			}
			if l != want {
				t.Fatalf("line = %q, want %q", l, want)
			}
		})
	}
}

func rsaLine(t testing.TB) string {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	k, err := ssh.NewPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return rawLine(k)
}

func ecdsaLine(t testing.TB, c elliptic.Curve) string {
	t.Helper()
	priv, err := ecdsa.GenerateKey(c, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	k, err := ssh.NewPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return rawLine(k)
}

func TestParsePastedOtherTypes(t *testing.T) {
	r := rsaLine(t)
	tests := map[string]string{
		"rsa":          r + " bob@PC",
		"rsa wrapped":  "here: " + wrap(r, 40, "\r\n") + " bob@PC",
		"ecdsa256":     ecdsaLine(t, elliptic.P256()) + " bob@PC",
		"ecdsa384":     "`" + ecdsaLine(t, elliptic.P384()) + "`",
		"ecdsa521":     ecdsaLine(t, elliptic.P521()),
		"rsa bare b64": strings.TrimPrefix(r, "ssh-rsa "),
	}
	for name, text := range tests {
		t.Run(name, func(t *testing.T) {
			_, _, _, err := ParsePasted(text)
			if !errors.Is(err, ErrNotEd25519) {
				t.Fatalf("err = %v, want ErrNotEd25519", err)
			}
		})
	}
}

func TestParsePastedNoKey(t *testing.T) {
	k := newEd25519(t)
	b64 := strings.TrimPrefix(rawLine(k), "ssh-ed25519 ")
	tests := map[string]string{
		"empty":       "",
		"spaces":      " \n\t ",
		"words":       "hello, how are you?",
		"type only":   "ssh-ed25519",
		"truncated":   "ssh-ed25519 " + b64[:40] + " anna@PC",
		"corrupted":   "ssh-ed25519 " + b64[:30] + "!" + b64[31:] + " anna@PC",
		"garbage b64": "ssh-ed25519 AAAAbm90IGEga2V5IGF0IGFsbA== anna@PC",
		"private key": "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW\n-----END OPENSSH PRIVATE KEY-----",
		"aaaa":        "AAAA AAAAAAAAAAAAAAAAAAAAAAAAAA",
	}
	for name, text := range tests {
		t.Run(name, func(t *testing.T) {
			_, _, _, err := ParsePasted(text)
			if !errors.Is(err, ErrNoKey) {
				t.Fatalf("err = %v, want ErrNoKey", err)
			}
		})
	}
}

// TestParsePastedSSHKeygen uses real ssh-keygen output when available.
func TestParsePastedSSHKeygen(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not found")
	}
	dir := t.TempDir()
	gen := func(name string, args ...string) string {
		path := filepath.Join(dir, name)
		args = append([]string{"-q", "-N", "", "-C", "anna@PC", "-f", path}, args...)
		if out, err := exec.Command("ssh-keygen", args...).CombinedOutput(); err != nil {
			t.Fatalf("ssh-keygen %v: %v\n%s", args, err, out)
		}
		data, err := os.ReadFile(path + ".pub")
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	ed := gen("ed", "-t", "ed25519")
	k, comment, line, err := ParsePasted("Hi, my key:\r\n" + wrap(ed, 25, "\r\n") + "\r\nBye")
	if err != nil {
		t.Fatal(err)
	}
	if comment != "anna@PC" || line != strings.TrimSpace(ed) {
		t.Fatalf("comment %q line %q, want %q", comment, line, strings.TrimSpace(ed))
	}
	if k.Type() != ssh.KeyAlgoED25519 {
		t.Fatal(k.Type())
	}
	for _, typ := range [][]string{{"-t", "rsa", "-b", "3072"}, {"-t", "ecdsa"}, {"-t", "rsa", "-b", "1024"}} {
		text := gen(strings.Join(typ, ""), typ...)
		if _, _, _, err := ParsePasted(text); !errors.Is(err, ErrNotEd25519) {
			t.Errorf("%v: err = %v, want ErrNotEd25519", typ, err)
		}
		if _, _, err := ParseLine(text); !errors.Is(err, ErrNotEd25519) {
			t.Errorf("ParseLine %v: err = %v, want ErrNotEd25519", typ, err)
		}
	}
}

func TestParseLine(t *testing.T) {
	k := newEd25519(t)
	line := rawLine(k)
	for _, tt := range []struct{ in, comment string }{
		{line, ""},
		{line + " anna@PC", "anna@PC"},
		{line + " anna@PC\n", "anna@PC"},
		{line + " anna@PC\r\n", "anna@PC"},
		{line + " two words", "two words"},
	} {
		got, c, err := ParseLine(tt.in)
		if err != nil {
			t.Fatalf("%q: %v", tt.in, err)
		}
		if c != tt.comment || !bytes.Equal(got.Marshal(), k.Marshal()) {
			t.Fatalf("%q: got comment %q", tt.in, c)
		}
	}
	for _, bad := range []string{
		"",
		"ssh-ed25519",
		"ssh-ed25519 " + strings.TrimPrefix(line, "ssh-ed25519 ")[:20],
		"ssh-ed25519  " + strings.TrimPrefix(line, "ssh-ed25519 "),
		"ssh-rsa " + strings.TrimPrefix(line, "ssh-ed25519 "),
		"no-pty " + line,
		"ssh-ed25519 " + wrap(strings.TrimPrefix(line, "ssh-ed25519 "), 30, "\n"),
		"\u00a0" + line,
	} {
		if _, _, err := ParseLine(bad); err == nil {
			t.Errorf("ParseLine(%q) succeeded", bad)
		}
	}
	if _, _, err := ParseLine(rsaLine(t)); !errors.Is(err, ErrNotEd25519) {
		t.Errorf("rsa: err = %v", err)
	}
}

func TestLineRoundTrip(t *testing.T) {
	k := newEd25519(t)
	if got, want := Line(k, ""), rawLine(k); got != want {
		t.Fatalf("Line = %q, want %q", got, want)
	}
	l := Line(k, "anna@PC")
	if l != rawLine(k)+" anna@PC" {
		t.Fatal(l)
	}
	got, c, err := ParseLine(l)
	if err != nil || c != "anna@PC" || !bytes.Equal(got.Marshal(), k.Marshal()) {
		t.Fatalf("round trip: %v %q", err, c)
	}
	if fp := Fingerprint(k); !strings.HasPrefix(fp, "SHA256:") || strings.HasSuffix(fp, "=") {
		t.Fatal(fp)
	}
}

func TestValidName(t *testing.T) {
	for s, want := range map[string]bool{
		"anna@PC":                    true,
		"a":                          true,
		"":                           false,
		"anna smith@PC":              false,
		"anna\t@PC":                  false,
		"anna\n":                     false,
		"anna\u00a0@PC":              false,
		"anna\u200b@PC":              false,
		"\x00":                       false,
		strings.Repeat("x", 64):      true,
		strings.Repeat("x", 65):      false,
		strings.Repeat("\u00e9", 64): true,
		"\xff":                       false,
		"j\u00f6rg@b\u00fcro":        true,
	} {
		if got := ValidName(s); got != want {
			t.Errorf("ValidName(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestUserMachine(t *testing.T) {
	um := UserMachine()
	if !ValidName(um) {
		t.Fatalf("UserMachine() = %q is not a valid name", um)
	}
	u, m, ok := strings.Cut(um, "@")
	if !ok || u == "" || m == "" || strings.Contains(m, "@") {
		t.Fatalf("UserMachine() = %q", um)
	}
	if u != strings.ToLower(u) {
		t.Fatalf("user not lower-cased: %q", um)
	}
	if strings.Contains(m, ".") {
		t.Fatalf("machine not short: %q", um)
	}
	if got := stripDomain(`CORP\Anna`); got != "Anna" {
		t.Fatal(got)
	}
	if got := sanitize("Anna Smith", "user"); got != "Anna_Smith" {
		t.Fatal(got)
	}
	if got := sanitize("  ", "user"); got != "user" {
		t.Fatal(got)
	}
}

func FuzzParsePasted(f *testing.F) {
	k := newEd25519(f)
	line := rawLine(k)
	b64 := strings.TrimPrefix(line, "ssh-ed25519 ")
	f.Add(line + " anna@PC")
	f.Add("here is my key: \u201c" + wrap(line, 20, "\r\n") + " anna@PC\u201d thanks")
	f.Add(rsaLine(f))
	f.Add(b64)
	f.Add("ssh-ed25519 AAAA")
	f.Add("AAAAC3NzaC1lZDI1NTE5AAAAI")
	f.Add("")
	f.Fuzz(func(t *testing.T, text string) {
		key, comment, l, err := ParsePasted(text)
		if err != nil {
			if !errors.Is(err, ErrNoKey) && !errors.Is(err, ErrNotEd25519) {
				t.Fatalf("unexpected error %v", err)
			}
			return
		}
		if key.Type() != ssh.KeyAlgoED25519 {
			t.Fatalf("type %s", key.Type())
		}
		if comment != "" && !ValidName(comment) {
			t.Fatalf("invalid comment %q", comment)
		}
		if l != Line(key, comment) {
			t.Fatalf("line %q != Line()", l)
		}
		k2, c2, err := ParseLine(l)
		if err != nil {
			t.Fatalf("ParseLine(%q): %v", l, err)
		}
		if c2 != comment || !bytes.Equal(k2.Marshal(), key.Marshal()) {
			t.Fatalf("round trip mismatch for %q", l)
		}
		// Parsing the normalized line again gives the same result.
		k3, c3, l3, err := ParsePasted(l)
		if err != nil || l3 != l || c3 != comment || !bytes.Equal(k3.Marshal(), key.Marshal()) {
			t.Fatalf("reparse of %q: %v %q", l, err, l3)
		}
	})
}
