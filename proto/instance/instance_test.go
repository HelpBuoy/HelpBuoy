package instance

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

const name = "test.lock"

func TestSecondAcquireFails(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	l, err := Acquire(dir, name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(dir, name); !errors.Is(err, ErrRunning) {
		t.Fatalf("second Acquire: %v, want ErrRunning", err)
	}
	if got := Owner(dir, name); got != os.Getpid() {
		t.Errorf("Owner = %d, want %d", got, os.Getpid())
	}
	if err := l.Unlock(); err != nil {
		t.Fatal(err)
	}
	l, err = Acquire(dir, name)
	if err != nil {
		t.Fatalf("Acquire after Unlock: %v", err)
	}
	l.Unlock()
}

func TestOtherDirsAreIndependent(t *testing.T) {
	a, err := Acquire(filepath.Join(t.TempDir(), "user1"), name)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Unlock()
	b, err := Acquire(filepath.Join(t.TempDir(), "user2"), name)
	if err != nil {
		t.Fatalf("lock in another data directory: %v", err)
	}
	b.Unlock()
}

func TestOwnerMissing(t *testing.T) {
	if got := Owner(t.TempDir(), name); got != 0 {
		t.Errorf("Owner without a file = %d", got)
	}
}

// TestOtherProcess checks the lock against a second process, which is how
// two copies of an app meet. The child prints the owner it reads.
func TestOtherProcess(t *testing.T) {
	if dir := os.Getenv("INSTANCE_TEST_DIR"); dir != "" {
		_, err := Acquire(dir, name)
		if errors.Is(err, ErrRunning) {
			os.Stdout.WriteString(strconv.Itoa(Owner(dir, name)))
			os.Exit(3)
		}
		if err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	dir := t.TempDir()
	l, err := Acquire(dir, name)
	if err != nil {
		t.Fatal(err)
	}
	run := func() (int, string) {
		cmd := exec.Command(os.Args[0], "-test.run=^TestOtherProcess$")
		cmd.Env = append(os.Environ(), "INSTANCE_TEST_DIR="+dir)
		out, err := cmd.Output()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode(), string(out)
		}
		if err != nil {
			t.Fatal(err)
		}
		return 0, string(out)
	}
	code, out := run()
	if code != 3 {
		t.Fatalf("other process while locked: exit %d, want 3 (ErrRunning)", code)
	}
	if out != strconv.Itoa(os.Getpid()) {
		t.Errorf("other process read owner %q, want %d", out, os.Getpid())
	}
	l.Unlock()
	if code, _ := run(); code != 0 {
		t.Fatalf("other process after Unlock: exit %d, want 0", code)
	}
}
