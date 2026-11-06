// Package verify is the verified server check (PLAN.md 12): the id of a
// server's file on helpbuoy.com, the file itself, the check the clients make
// for every connection request, and the cleanup of text that comes from a
// server, so it can't imitate the badge.
package verify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// SaltSize is the length of a server's verification salt in bytes.
const SaltSize = 32

// BaseURL is where the files are. The clients use nothing else.
const BaseURL = "https://www.helpbuoy.com/verified/"

const (
	maxFile     = 4 << 10          // a larger file can't be checked
	timeout     = 10 * time.Second // per check
	minInterval = 5 * time.Second  // between the starts of two checks
	maxMonths   = 13               // a file is trusted this long after its verified date
)

// ID returns the id of a server's file: the hex SHA-256 of a fixed prefix,
// the host key in SSH wire format and the salt.
func ID(hostKey ssh.PublicKey, salt []byte) string {
	h := sha256.New()
	h.Write([]byte("helpbuoy verified server v1\n"))
	h.Write(hostKey.Marshal())
	h.Write(salt)
	return hex.EncodeToString(h.Sum(nil))
}

// ParseSalt decodes a salt sent by a server. Anything but exactly SaltSize
// bytes of hex is no salt, so a broken server can never end up with an id a
// scanner could compute.
func ParseSalt(s string) ([]byte, bool) {
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != SaltSize {
		return nil, false
	}
	return b, true
}

// HostName returns the host name of a server address as the files list it:
// without port, in lower case and without a trailing dot. An IP address, or
// an address without a host name, gives "".
func HostName(addr string) string {
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "" || net.ParseIP(strings.Trim(host, "[]")) != nil {
		return ""
	}
	return host
}

// File is a server's file on helpbuoy.com.
type File struct {
	HostKey   string   `json:"hostkey"`
	Addresses []string `json:"addresses"`
	Company   string   `json:"company"`
	Country   string   `json:"country"`
	Verified  string   `json:"verified"` // YYYY-MM-DD
}

// State is the outcome of a check.
type State int

const (
	// Unknown means the check could not be made or did not finish: no
	// answer, an unexpected status, or a file that does not parse. It is
	// never shown as verified.
	Unknown State = iota
	// NotVerified means helpbuoy.com has no valid file for this server.
	NotVerified
	// Verified means the file matches this server's host key and address.
	Verified
)

func (s State) String() string {
	return [...]string{"unknown", "not verified", "verified"}[s]
}

// Result is the outcome of one check.
type Result struct {
	State     State
	Company   string   // Verified only
	Country   string   // ISO 3166 code, Verified only
	Addresses []string // the file's host names, Verified only
	Err       error    // why the check failed or the file was refused, for the log
}

// Checker makes the checks for one app. Its zero value is not usable; use
// NewChecker.
type Checker struct {
	client    *http.Client
	base      string
	userAgent string
	now       func() time.Time
	wait      func(ctx context.Context, d time.Duration) error

	slot chan struct{} // one check in flight
	mu   sync.Mutex
	last time.Time // start of the last check
}

// Options configure a Checker for tests; the zero value is what the apps use.
type Options struct {
	BaseURL string           // default BaseURL
	Client  *http.Client     // default: no redirects, 10 s timeout
	Now     func() time.Time // default time.Now
	Wait    func(context.Context, time.Duration) error
}

// NewChecker returns a checker that sends version in its User-Agent.
func NewChecker(version string, o Options) *Checker {
	c := &Checker{
		client:    o.Client,
		base:      o.BaseURL,
		userAgent: "helpbuoy/" + version,
		now:       o.Now,
		wait:      o.Wait,
		slot:      make(chan struct{}, 1),
	}
	if c.base == "" {
		c.base = BaseURL
	}
	if c.client == nil {
		c.client = &http.Client{
			Timeout: timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	if c.now == nil {
		c.now = time.Now
	}
	if c.wait == nil {
		c.wait = sleep
	}
	return c
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Check looks up the server with hostKey and salt. With addr set, the file
// must list its host name; the desk app checks its own server with addr ""
// (PLAN.md 12.5). Checks run one at a time and start at least five seconds
// apart, so a hostile server can't make its computers flood helpbuoy.com.
func (c *Checker) Check(ctx context.Context, hostKey ssh.PublicKey, salt []byte, addr string, matchAddr bool) Result {
	if hostKey == nil || hostKey.Type() != ssh.KeyAlgoED25519 {
		return Result{State: NotVerified, Err: errors.New("not an ed25519 host key")}
	}
	if len(salt) != SaltSize {
		return Result{State: NotVerified, Err: errors.New("the server sent no valid salt")}
	}
	host := ""
	if matchAddr {
		if host = HostName(addr); host == "" {
			return Result{State: NotVerified, Err: fmt.Errorf("%q has no host name", addr)}
		}
	}
	select {
	case c.slot <- struct{}{}:
	case <-ctx.Done():
		return Result{State: Unknown, Err: ctx.Err()}
	}
	defer func() { <-c.slot }()
	c.mu.Lock()
	d := c.last.Add(minInterval).Sub(c.now())
	c.mu.Unlock()
	if d > 0 {
		if err := c.wait(ctx, d); err != nil {
			return Result{State: Unknown, Err: err}
		}
	}
	c.mu.Lock()
	c.last = c.now()
	c.mu.Unlock()

	f, found, err := c.fetch(ctx, ID(hostKey, salt))
	switch {
	case err != nil:
		return Result{State: Unknown, Err: err}
	case !found:
		return Result{State: NotVerified}
	}
	return c.judge(f, hostKey, host, matchAddr)
}

func (c *Checker) fetch(ctx context.Context, id string) (*File, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+id+".json", nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, false, nil
	default:
		return nil, false, fmt.Errorf("helpbuoy.com answered %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxFile+1))
	if err != nil {
		return nil, false, err
	}
	if len(b) > maxFile {
		return nil, false, errors.New("the file is too large")
	}
	var f File
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, false, fmt.Errorf("the file does not parse: %w", err)
	}
	return &f, true, nil
}

// judge decides about a file that was found.
func (c *Checker) judge(f *File, hostKey ssh.PublicKey, host string, matchAddr bool) Result {
	k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(f.HostKey))
	if err != nil {
		return Result{State: Unknown, Err: fmt.Errorf("the file's host key: %w", err)}
	}
	verified, err := time.Parse(time.DateOnly, f.Verified)
	if err != nil || strings.TrimSpace(f.Company) == "" {
		return Result{State: Unknown, Err: errors.New("the file has no company or verified date")}
	}
	if k.Type() != hostKey.Type() || string(k.Marshal()) != string(hostKey.Marshal()) {
		return Result{State: NotVerified, Err: errors.New("the file is for another host key")}
	}
	var hosts []string
	listed := false
	for _, a := range f.Addresses {
		h := HostName(a)
		if h == "" {
			continue
		}
		hosts = append(hosts, h)
		listed = listed || h == host
	}
	if matchAddr && !listed {
		return Result{State: NotVerified, Err: fmt.Errorf("the file does not list %q", host)}
	}
	if c.now().After(verified.AddDate(0, maxMonths, 0)) {
		return Result{State: NotVerified, Err: fmt.Errorf("the file was verified on %s, too long ago", f.Verified)}
	}
	return Result{
		State:     Verified,
		Company:   oneLine(f.Company),
		Country:   strings.ToUpper(strings.TrimSpace(f.Country)),
		Addresses: hosts,
	}
}
