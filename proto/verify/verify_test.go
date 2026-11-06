package verify

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func key(t *testing.T, seed byte) ssh.PublicKey {
	t.Helper()
	priv := ed25519.NewKeyFromSeed(bytesOf(seed, ed25519.SeedSize))
	k, err := ssh.NewPublicKey(priv.Public())
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func bytesOf(b byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}

func TestIDFixed(t *testing.T) {
	// A fixed value, so the id never changes by accident: changing it would
	// orphan every published file. It is SHA-256 of the prefix, the host key
	// in wire format and the salt (PLAN.md 12.3).
	k := key(t, 1)
	got := ID(k, bytesOf(2, SaltSize))
	b := append([]byte("helpbuoy verified server v1\n"), k.Marshal()...)
	sum := sha256.Sum256(append(b, bytesOf(2, SaltSize)...))
	if got != hex.EncodeToString(sum[:]) || got != fixedID {
		t.Fatalf("ID = %s, want %s", got, fixedID)
	}
	if ID(k, bytesOf(3, SaltSize)) == got || ID(key(t, 4), bytesOf(2, SaltSize)) == got {
		t.Fatal("the id must depend on both the host key and the salt")
	}
}

func TestParseSalt(t *testing.T) {
	good := strings.Repeat("ab", SaltSize)
	if b, ok := ParseSalt(good); !ok || len(b) != SaltSize {
		t.Fatalf("ParseSalt(good) = %v, %v", b, ok)
	}
	for _, s := range []string{"", "ab", strings.Repeat("ab", SaltSize-1), strings.Repeat("ab", SaltSize+1), strings.Repeat("zz", SaltSize)} {
		if _, ok := ParseSalt(s); ok {
			t.Errorf("ParseSalt(%q) accepted", s)
		}
	}
}

func TestHostName(t *testing.T) {
	for in, want := range map[string]string{
		"helpme.example.com:5959":  "helpme.example.com",
		"HelpMe.Example.COM.:5959": "helpme.example.com",
		"helpme.example.com":       "helpme.example.com",
		"192.0.2.1:5959":           "",
		"[2001:db8::1]:5959":       "",
		"2001:db8::1":              "",
		":5959":                    "",
	} {
		if got := HostName(in); got != want {
			t.Errorf("HostName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestClean(t *testing.T) {
	for in, want := range map[string]string{
		"Example Inc":            "Example Inc",
		"✔ Verified by HelpBuoy": "by HelpBuoy",
		"Microsoft‮ Support":     "Microsoft Support",
		"line one\nline two":     "line one line two",
		"Geverifieerd: Bank":     ": Bank",
		"VÉRIFIÉ ✅ support":      "support",
		"zero​width":             "zerowidth",
		"verververifiedified":    "ver",
		"Alice":                  "Alice",
		"Unverified":             "Un",
		"✔️ Microsoft":           "Microsoft",
		"ok\x00\x1b[31m":         "ok[31m",
	} {
		if got := Clean(in); got != want {
			t.Errorf("Clean(%q) = %q, want %q", in, got, want)
		}
	}
}

// site is a fake helpbuoy.com.
type site struct {
	mu    sync.Mutex
	files map[string]any // id -> File or raw string
	codes map[string]int
	hits  int
	reqs  []*http.Request
}

func (s *site) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hits++
	s.reqs = append(s.reqs, r)
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/verified/"), ".json")
	if code := s.codes[id]; code != 0 {
		if code == http.StatusFound {
			http.Redirect(w, r, "/elsewhere", code)
			return
		}
		w.WriteHeader(code)
		return
	}
	f, ok := s.files[id]
	if !ok {
		http.NotFound(w, r)
		return
	}
	if raw, ok := f.(string); ok {
		w.Write([]byte(raw))
		return
	}
	json.NewEncoder(w).Encode(f)
}

type env struct {
	site *site
	c    *Checker
	now  time.Time
	hk   ssh.PublicKey
	salt []byte
	id   string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	s := &site{files: map[string]any{}, codes: map[string]int{}}
	srv := httptest.NewTLSServer(s)
	t.Cleanup(srv.Close)
	cl := srv.Client()
	cl.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	e := &env{site: s, now: time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC), hk: key(t, 1), salt: bytesOf(2, SaltSize)}
	e.id = ID(e.hk, e.salt)
	e.c = NewChecker("v1.2.3", Options{
		BaseURL: srv.URL + "/verified/",
		Client:  cl,
		Now:     func() time.Time { return e.now },
		Wait:    func(context.Context, time.Duration) error { return nil },
	})
	return e
}

func (e *env) file() File {
	return File{
		HostKey:   strings.TrimSpace(string(ssh.MarshalAuthorizedKey(e.hk))),
		Addresses: []string{"helpme.example.com"},
		Company:   "Example Inc",
		Country:   "nl",
		Verified:  "2026-11-06",
	}
}

func (e *env) check(addr string) Result {
	return e.c.Check(context.Background(), e.hk, e.salt, addr, true)
}

func TestCheckVerified(t *testing.T) {
	e := newEnv(t)
	e.site.files[e.id] = e.file()
	r := e.check("HELPME.example.com.:5959")
	if r.State != Verified || r.Company != "Example Inc" || r.Country != "NL" || len(r.Addresses) != 1 {
		t.Fatalf("got %+v", r)
	}
	req := e.site.reqs[0]
	if req.Header.Get("Cache-Control") != "no-cache" || req.Header.Get("User-Agent") != "helpbuoy/v1.2.3" {
		t.Errorf("headers %v", req.Header)
	}
	if req.URL.RawQuery != "" || req.Header.Get("Cookie") != "" {
		t.Errorf("the request carries more than the id: %v", req.URL)
	}
}

func TestCheckRefusals(t *testing.T) {
	other := key(t, 9)
	for name, tc := range map[string]struct {
		edit func(e *env, f *File)
		addr string
		want State
	}{
		"missing":        {func(e *env, f *File) { delete(e.site.files, e.id) }, "helpme.example.com", NotVerified},
		"other host key": {func(e *env, f *File) { f.HostKey = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(other))) }, "helpme.example.com", NotVerified},
		"other address":  {nil, "scam.example.net", NotVerified},
		"ip address":     {func(e *env, f *File) { f.Addresses = []string{"192.0.2.1"} }, "192.0.2.1:5959", NotVerified},
		"expired":        {func(e *env, f *File) { f.Verified = "2025-10-05" }, "helpme.example.com", NotVerified},
		"no company":     {func(e *env, f *File) { f.Company = " " }, "helpme.example.com", Unknown},
		"bad host key":   {func(e *env, f *File) { f.HostKey = "nonsense" }, "helpme.example.com", Unknown},
		"500":            {func(e *env, f *File) { e.site.codes[e.id] = 500 }, "helpme.example.com", Unknown},
		"redirect":       {func(e *env, f *File) { e.site.codes[e.id] = http.StatusFound }, "helpme.example.com", Unknown},
		"does not parse": {func(e *env, f *File) { e.site.files[e.id] = "{" }, "helpme.example.com", Unknown},
		"too large":      {func(e *env, f *File) { e.site.files[e.id] = `{"company":"` + strings.Repeat("x", 5000) + `"}` }, "helpme.example.com", Unknown},
		"just in time":   {func(e *env, f *File) { f.Verified = "2025-10-07" }, "helpme.example.com", Verified},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			f := e.file()
			e.site.files[e.id] = &f
			if tc.edit != nil {
				tc.edit(e, &f)
			}
			if r := e.check(tc.addr); r.State != tc.want {
				t.Fatalf("got %v (%v), want %v", r.State, r.Err, tc.want)
			}
		})
	}
}

func TestCheckBadInput(t *testing.T) {
	e := newEnv(t)
	e.site.files[e.id] = e.file()
	if r := e.c.Check(context.Background(), e.hk, e.salt[:10], "helpme.example.com", true); r.State != NotVerified {
		t.Errorf("short salt: %v", r.State)
	}
	if r := e.c.Check(context.Background(), e.hk, nil, "helpme.example.com", true); r.State != NotVerified {
		t.Errorf("no salt: %v", r.State)
	}
	rsa := rsaKey(t)
	if r := e.c.Check(context.Background(), rsa, e.salt, "helpme.example.com", true); r.State != NotVerified {
		t.Errorf("rsa host key: %v", r.State)
	}
	if e.site.hits != 0 {
		t.Errorf("helpbuoy.com was asked %d times for input that can't be verified", e.site.hits)
	}
	// The desk app checks its own server without the address.
	if r := e.c.Check(context.Background(), e.hk, e.salt, "localhost:5959", false); r.State != Verified {
		t.Errorf("host key only: %v (%v)", r.State, r.Err)
	}
}

func rsaKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	const line = "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAAAgQC6p0Kq0oCm9V5Dq0Nf3bQd0Cr8pX2G0yq5YbwdP3l1P2Ar4h2X1x1Gm6cVzZ6Wv5hA3P0dS6G9p2VkWQ3mB2y8m0Yp5ZbqB6H5x3V4Wj6Yf0c9mV3sJb3cC1kVfY6dW0b8G3y1qX8r9e5aQ0mN7xY3t0P1oK5vZ2lW3xQ== test"
	k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		t.Skip("no rsa test key:", err)
	}
	return k
}

func TestCheckPacing(t *testing.T) {
	e := newEnv(t)
	e.site.files[e.id] = e.file()
	var waited []time.Duration
	e.c.wait = func(_ context.Context, d time.Duration) error {
		waited = append(waited, d)
		e.now = e.now.Add(d)
		return nil
	}
	for range 3 {
		if r := e.check("helpme.example.com"); r.State != Verified {
			t.Fatal(r.State, r.Err)
		}
		e.now = e.now.Add(time.Second)
	}
	if len(waited) != 2 || waited[0] != 4*time.Second || waited[1] != 4*time.Second {
		t.Fatalf("waited %v, want two waits of 4s", waited)
	}
}

func TestCheckCancelled(t *testing.T) {
	e := newEnv(t)
	e.site.files[e.id] = e.file()
	e.check("helpme.example.com")
	e.c.wait = sleep
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if r := e.c.Check(ctx, e.hk, e.salt, "helpme.example.com", true); r.State != Unknown {
		t.Fatalf("cancelled check: %v", r.State)
	}
}

const fixedID = "28e0c12e9fd12681a67f3d12a1bdfa85d700ea8c482c721a96af048da67368d0"
