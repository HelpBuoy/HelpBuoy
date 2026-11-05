package appkey

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/helpbuoy/helpbuoy/proto/identity"
)

type fileState struct {
	data  []byte
	mode  fs.FileMode
	mtime time.Time
}

func snapshot(t *testing.T, dir string) map[string]fileState {
	t.Helper()
	m := map[string]fileState{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(p)
		m[e.Name()] = fileState{data, fi.Mode(), fi.ModTime()}
	}
	return m
}

func sameFiles(t *testing.T, before, after map[string]fileState) {
	t.Helper()
	if len(before) != len(after) {
		t.Fatalf("file set changed: %d -> %d files", len(before), len(after))
	}
	for name, b := range before {
		a, ok := after[name]
		if !ok {
			t.Fatalf("%s disappeared", name)
		}
		if !bytes.Equal(a.data, b.data) || a.mode != b.mode || !a.mtime.Equal(b.mtime) {
			t.Fatalf("%s changed", name)
		}
	}
}

// age moves the mtime of the files in dir into the past, so that loading
// does not wait for a possibly concurrent writer.
func age(t *testing.T, dir string) {
	t.Helper()
	old := time.Now().Add(-time.Hour)
	for _, name := range []string{PrivateFile, PublicFile} {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			if err := os.Chtimes(p, old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func checkKey(t *testing.T, k *Key, dir, comment string) {
	t.Helper()
	if k.Public.Type() != ssh.KeyAlgoED25519 {
		t.Fatalf("type %s", k.Public.Type())
	}
	if !bytes.Equal(k.Signer.PublicKey().Marshal(), k.Public.Marshal()) {
		t.Fatal("signer and public key differ")
	}
	if k.Comment != comment || k.Line != identity.Line(k.Public, comment) {
		t.Fatalf("comment %q line %q", k.Comment, k.Line)
	}
	if k.Fingerprint != ssh.FingerprintSHA256(k.Public) || k.Dir != dir {
		t.Fatalf("fingerprint %q dir %q", k.Fingerprint, k.Dir)
	}
	data := make([]byte, 64)
	rand.Read(data)
	sig, err := k.Signer.Sign(rand.Reader, data)
	if err != nil {
		t.Fatal(err)
	}
	if err := k.Public.Verify(data, sig); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestCreateOnceThenLoad(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sub", "helpbuoy")
	k, created, err := LoadOrCreate(dir, "anna@PC")
	if err != nil || !created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	checkKey(t, k, dir, "anna@PC")

	if runtime.GOOS != "windows" {
		fi, err := os.Stat(dir)
		if err != nil || fi.Mode().Perm() != 0o700 {
			t.Fatalf("dir mode %v %v", fi.Mode(), err)
		}
		fi, err = os.Stat(filepath.Join(dir, PrivateFile))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("private mode %v %v", fi.Mode(), err)
		}
	}
	priv, _ := os.ReadFile(filepath.Join(dir, PrivateFile))
	if !bytes.HasPrefix(priv, []byte("-----BEGIN OPENSSH PRIVATE KEY-----\n")) ||
		!bytes.HasSuffix(priv, []byte("-----END OPENSSH PRIVATE KEY-----\n")) {
		t.Fatalf("private file format:\n%s", priv)
	}
	pub, _ := os.ReadFile(filepath.Join(dir, PublicFile))
	if string(pub) != k.Line+"\n" {
		t.Fatalf("public file %q, want %q", pub, k.Line+"\n")
	}

	age(t, dir)
	before := snapshot(t, dir)
	k2, created, err := LoadOrCreate(dir, "other@name")
	if err != nil || created {
		t.Fatalf("second call: created=%v err=%v", created, err)
	}
	if k2.Line != k.Line {
		t.Fatalf("different key loaded: %q vs %q", k2.Line, k.Line)
	}
	k3, err := Load(dir)
	if err != nil || k3.Line != k.Line {
		t.Fatalf("Load: %v", err)
	}
	sameFiles(t, before, snapshot(t, dir))
}

func TestLoadMissing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nothing")
	if _, err := Load(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("Load created the directory")
	}
	empty := t.TempDir()
	if _, err := Load(empty); !errors.Is(err, fs.ErrNotExist) || errors.Is(err, ErrDamaged) {
		t.Fatalf("err = %v", err)
	}
}

func TestDamagedPrivateKey(t *testing.T) {
	for name, content := range map[string][]byte{
		"empty":     {},
		"garbage":   []byte("hello\n"),
		"truncated": nil, // filled below
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if name == "truncated" {
				if _, _, err := LoadOrCreate(dir, "x@y"); err != nil {
					t.Fatal(err)
				}
				p := filepath.Join(dir, PrivateFile)
				data, _ := os.ReadFile(p)
				os.Remove(p)
				content = data[:len(data)/2]
			}
			p := filepath.Join(dir, PrivateFile)
			if err := os.WriteFile(p, content, 0o600); err != nil {
				t.Fatal(err)
			}
			age(t, dir)
			before := snapshot(t, dir)
			_, _, err := LoadOrCreate(dir, "x@y")
			if !errors.Is(err, ErrDamaged) || !strings.Contains(err.Error(), p) {
				t.Fatalf("err = %v", err)
			}
			if _, err := Load(dir); !errors.Is(err, ErrDamaged) {
				t.Fatalf("Load err = %v", err)
			}
			sameFiles(t, before, snapshot(t, dir))
		})
	}
}

func TestWrongKeyType(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not found")
	}
	cases := map[string][]string{
		"rsa":       {"-t", "rsa", "-b", "2048", "-N", ""},
		"encrypted": {"-t", "ed25519", "-N", "secret"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, PrivateFile)
			args := append([]string{"-q", "-f", p, "-C", "a@b"}, args...)
			if out, err := exec.Command("ssh-keygen", args...).CombinedOutput(); err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			age(t, dir)
			before := snapshot(t, dir)
			if _, _, err := LoadOrCreate(dir, "a@b"); !errors.Is(err, ErrDamaged) {
				t.Fatalf("err = %v", err)
			}
			sameFiles(t, before, snapshot(t, dir))
		})
	}
}

func TestMismatchedPublicKey(t *testing.T) {
	dir := t.TempDir()
	other := t.TempDir()
	if _, _, err := LoadOrCreate(dir, "a@b"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadOrCreate(other, "a@b"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(other, PublicFile))
	p := filepath.Join(dir, PublicFile)
	os.Remove(p)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	age(t, dir)
	before := snapshot(t, dir)
	if _, _, err := LoadOrCreate(dir, "a@b"); !errors.Is(err, ErrDamaged) || !strings.Contains(err.Error(), p) {
		t.Fatalf("err = %v", err)
	}
	sameFiles(t, before, snapshot(t, dir))

	// A garbage public file is damaged too.
	os.Remove(p)
	os.WriteFile(p, []byte("nonsense\n"), 0o644)
	age(t, dir)
	if _, err := Load(dir); !errors.Is(err, ErrDamaged) {
		t.Fatalf("err = %v", err)
	}
}

func TestReadOnlyKeyLoads(t *testing.T) {
	if runtime.GOOS != "windows" && os.Getuid() == 0 {
		t.Skip("root ignores permissions")
	}
	dir := t.TempDir()
	k, _, err := LoadOrCreate(dir, "a@b")
	if err != nil {
		t.Fatal(err)
	}
	age(t, dir)
	for _, name := range []string{PrivateFile, PublicFile} {
		p := filepath.Join(dir, name)
		if err := os.Chmod(p, 0o400); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(p, 0o600) })
	}
	if runtime.GOOS != "windows" {
		os.Chmod(dir, 0o500)
		t.Cleanup(func() { os.Chmod(dir, 0o700) })
	}
	before := snapshot(t, dir)
	k2, created, err := LoadOrCreate(dir, "a@b")
	if err != nil || created || k2.Line != k.Line {
		t.Fatalf("created=%v err=%v", created, err)
	}
	sameFiles(t, before, snapshot(t, dir))
}

func TestUnreadablePrivateKey(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("needs Unix permissions as non-root")
	}
	dir := t.TempDir()
	if _, _, err := LoadOrCreate(dir, "a@b"); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, PrivateFile)
	os.Chmod(p, 0)
	age(t, dir)
	before := snapshot(t, dir)
	if _, _, err := LoadOrCreate(dir, "a@b"); !errors.Is(err, ErrDamaged) || !strings.Contains(err.Error(), p) {
		t.Fatalf("err = %v", err)
	}
	sameFiles(t, before, snapshot(t, dir))
}

func TestMissingPublicRecreated(t *testing.T) {
	dir := t.TempDir()
	k, _, err := LoadOrCreate(dir, "anna@PC")
	if err != nil {
		t.Fatal(err)
	}
	privPath := filepath.Join(dir, PrivateFile)
	pubPath := filepath.Join(dir, PublicFile)
	os.Remove(pubPath)
	age(t, dir)
	privBefore, _ := os.Stat(privPath)
	privData, _ := os.ReadFile(privPath)

	// Load never writes.
	lk, err := Load(dir)
	if err != nil || lk.Comment != "" || !bytes.Equal(lk.Public.Marshal(), k.Public.Marshal()) {
		t.Fatalf("Load: %v %+v", err, lk)
	}
	if _, err := os.Stat(pubPath); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("Load wrote the public file")
	}

	k2, created, err := LoadOrCreate(dir, "new@name")
	if err != nil || created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	if !bytes.Equal(k2.Public.Marshal(), k.Public.Marshal()) || k2.Comment != "new@name" {
		t.Fatalf("got %q", k2.Line)
	}
	pub, _ := os.ReadFile(pubPath)
	if string(pub) != k2.Line+"\n" {
		t.Fatalf("pub file %q", pub)
	}
	privAfter, _ := os.Stat(privPath)
	privData2, _ := os.ReadFile(privPath)
	if !bytes.Equal(privData, privData2) || !privAfter.ModTime().Equal(privBefore.ModTime()) || privAfter.Mode() != privBefore.Mode() {
		t.Fatal("private key file changed")
	}
}

func TestPublicOnlyIsDamaged(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := LoadOrCreate(dir, "a@b"); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(dir, PrivateFile))
	age(t, dir)
	before := snapshot(t, dir)
	if _, _, err := LoadOrCreate(dir, "a@b"); !errors.Is(err, ErrDamaged) {
		t.Fatalf("err = %v", err)
	}
	if _, err := Load(dir); !errors.Is(err, ErrDamaged) {
		t.Fatalf("Load err = %v", err)
	}
	sameFiles(t, before, snapshot(t, dir))
}

func TestRemove(t *testing.T) {
	dir := t.TempDir()
	k, _, err := LoadOrCreate(dir, "a@b")
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "config.json")
	os.WriteFile(other, []byte("{}"), 0o600)
	os.WriteFile(filepath.Join(dir, "id_ed25519.bak"), []byte("x"), 0o600)
	if err := Remove(dir); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if strings.Join(names, ",") != "config.json,id_ed25519.bak" {
		t.Fatalf("left %v", names)
	}
	if err := Remove(dir); err != nil {
		t.Fatalf("second Remove: %v", err)
	}
	if err := Remove(filepath.Join(dir, "missing")); err != nil {
		t.Fatalf("Remove missing dir: %v", err)
	}
	k2, created, err := LoadOrCreate(dir, "a@b")
	if err != nil || !created || k2.Line == k.Line {
		t.Fatalf("created=%v err=%v", created, err)
	}
}

func TestConcurrentLoadOrCreate(t *testing.T) {
	for round := 0; round < 5; round++ {
		dir := filepath.Join(t.TempDir(), "d")
		const n = 32
		var wg sync.WaitGroup
		lines := make([]string, n)
		errs := make([]error, n)
		createdCount := make([]bool, n)
		start := make(chan struct{})
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				k, created, err := LoadOrCreate(dir, "a@b")
				errs[i], createdCount[i] = err, created
				if err == nil {
					lines[i] = k.Line
				}
			}()
		}
		close(start)
		wg.Wait()
		created := 0
		for i := range n {
			if errs[i] != nil {
				t.Fatalf("goroutine %d: %v", i, errs[i])
			}
			if lines[i] != lines[0] {
				t.Fatalf("different keys: %q vs %q", lines[i], lines[0])
			}
			if createdCount[i] {
				created++
			}
		}
		if created != 1 {
			t.Fatalf("%d goroutines report created", created)
		}
		k, err := Load(dir)
		if err != nil || k.Line != lines[0] {
			t.Fatalf("Load: %v", err)
		}
	}
}

func TestSSHKeygenCompatible(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not found")
	}
	dir := t.TempDir()
	k, _, err := LoadOrCreate(dir, "anna@PC")
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("ssh-keygen", "-y", "-f", filepath.Join(dir, PrivateFile)).CombinedOutput()
	if err != nil {
		t.Fatalf("ssh-keygen -y: %v\n%s", err, out)
	}
	got := strings.TrimSpace(string(out))
	want := identity.Line(k.Public, "")
	// ssh-keygen -y prints the comment stored in the private key, if any.
	if got != want && got != want+" anna@PC" {
		t.Fatalf("ssh-keygen -y = %q, want %q", got, want)
	}

	// And the other way around: a key written by ssh-keygen loads.
	dir2 := t.TempDir()
	p := filepath.Join(dir2, PrivateFile)
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "bob@PC2", "-f", p).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	pubData, _ := os.ReadFile(p + ".pub")
	k2, created, err := LoadOrCreate(dir2, "ignored@x")
	if err != nil || created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	if k2.Line != strings.TrimSpace(string(pubData)) || k2.Comment != "bob@PC2" {
		t.Fatalf("line %q, want %q", k2.Line, pubData)
	}
	checkKey(t, k2, dir2, "bob@PC2")
}

func TestReadOnlyDirMissingPublic(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("needs Unix permissions as non-root")
	}
	dir := t.TempDir()
	k, _, err := LoadOrCreate(dir, "a@b")
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(dir, PublicFile))
	os.Chmod(dir, 0o500)
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	age(t, dir)
	k2, created, err := LoadOrCreate(dir, "a@b")
	if err != nil || created || !bytes.Equal(k2.Public.Marshal(), k.Public.Marshal()) {
		t.Fatalf("created=%v err=%v", created, err)
	}
	if _, err := os.Stat(filepath.Join(dir, PublicFile)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("public file appeared")
	}
}

// TestConcurrentProcesses runs LoadOrCreate in several copies of the test
// binary at once.
func TestConcurrentProcesses(t *testing.T) {
	if dir := os.Getenv("APPKEY_TEST_DIR"); dir != "" {
		k, _, err := LoadOrCreate(dir, "a@b")
		if err != nil {
			os.Stdout.WriteString("ERROR " + err.Error() + "\n")
			os.Exit(1)
		}
		os.Stdout.WriteString("LINE " + k.Line + "\n")
		os.Exit(0)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Skip(err)
	}
	dir := filepath.Join(t.TempDir(), "d")
	const n = 8
	cmds := make([]*exec.Cmd, n)
	outs := make([]*bytes.Buffer, n)
	for i := range n {
		cmds[i] = exec.Command(exe, "-test.run=^TestConcurrentProcesses$")
		cmds[i].Env = append(os.Environ(), "APPKEY_TEST_DIR="+dir)
		outs[i] = new(bytes.Buffer)
		cmds[i].Stdout = outs[i]
		if err := cmds[i].Start(); err != nil {
			t.Fatal(err)
		}
	}
	var first string
	for i := range n {
		if err := cmds[i].Wait(); err != nil {
			t.Fatalf("process %d: %v\n%s", i, err, outs[i])
		}
		_, line, ok := strings.Cut(strings.TrimSpace(outs[i].String()), "LINE ")
		if !ok {
			t.Fatalf("process %d: %s", i, outs[i])
		}
		if first == "" {
			first = line
		} else if line != first {
			t.Fatalf("different keys %q vs %q", line, first)
		}
	}
	k, err := Load(dir)
	if err != nil || k.Line != first {
		t.Fatalf("Load: %v", err)
	}
}
