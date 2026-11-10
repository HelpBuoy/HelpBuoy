package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"

	"github.com/helpbuoy/helpbuoy/proto/serveraddr"
	"github.com/helpbuoy/helpbuoy/proto/transport"
	"github.com/helpbuoy/helpbuoy/server/auth"
	"github.com/helpbuoy/helpbuoy/server/front"
	"github.com/helpbuoy/helpbuoy/server/hostkey"
	"github.com/helpbuoy/helpbuoy/server/logfile"
	"github.com/helpbuoy/helpbuoy/server/server"
	"github.com/helpbuoy/helpbuoy/server/store"
)

// The three ways to run the server (--mode).
const (
	// modeHTTP serves the WebSocket on plain HTTP behind a reverse proxy
	// that handles HTTPS. Only that proxy reaches it.
	modeHTTP = "http"
	// modeHTTPS serves the WebSocket with the built-in HTTPS server.
	modeHTTPS = "https"
	// modeSSH serves plain SSH without WebSocket and TLS (direct connect),
	// the default. The apps try HTTPS first and connect again with plain
	// SSH when the port answers with the server's SSH banner.
	modeSSH = "ssh"
)

// defaultListen is the listen address of each mode.
var defaultListen = map[string]string{
	modeHTTP:  "127.0.0.1:5959",
	modeHTTPS: ":443",
	modeSSH:   ":5959",
}

// tlsDirName holds the self-signed certificate and the Let's Encrypt cache
// next to the virtual hosts. It starts with a dot, so it is never taken for
// a virtual host.
const tlsDirName = ".tls"

type serveConfig struct {
	listen    string
	state     string // without virtual hosts
	vhosts    string // the virtual hosts directory, or ""
	mode      string
	cert, key string
	acme      bool
	acmeHosts []string
	acmeEmail string
	trusted   []netip.Prefix
}

func parseServe(args []string, stderr io.Writer) (serveConfig, error) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	mode := fs.String("mode", envOr("HELPBUOY_SERVER_MODE", modeSSH), "ssh (default): plain SSH, direct connect; http: plain HTTP behind a reverse proxy; https: built-in HTTPS server")
	listen := fs.String("listen", os.Getenv("HELPBUOY_SERVER_LISTEN"), "address to listen on, host:port or :port (default: http 127.0.0.1:5959, https :443, ssh :5959)")
	state := fs.String("state", "", "state directory (default: $HELPBUOY_SERVER_STATE, else the per-user data directory)")
	vhosts := fs.String("vhosts", "", "directory with one state directory per virtual host (default: $HELPBUOY_SERVER_VHOSTS)")
	cert := fs.String("cert", "", "with --mode https: certificate file (PEM, full chain)")
	key := fs.String("key", "", "with --mode https: private key file (PEM)")
	useACME := fs.Bool("acme", false, "with --mode https: get certificates from Let's Encrypt")
	acmeHosts := fs.String("acme-host", "", "with --acme and without --vhosts: comma-separated host names to get certificates for")
	acmeEmail := fs.String("acme-email", "", "with --acme: contact address for Let's Encrypt")
	trusted := fs.String("trusted-proxy", "127.0.0.1,::1", "comma-separated proxy addresses or CIDRs whose X-Forwarded-For is used; empty trusts none")
	if err := fs.Parse(args); err != nil {
		return serveConfig{}, err
	}
	if fs.NArg() > 0 {
		return serveConfig{}, fmt.Errorf("serve: unexpected argument %q", fs.Arg(0))
	}
	cfg := serveConfig{listen: *listen, mode: *mode, cert: *cert, key: *key, acme: *useACME, acmeEmail: *acmeEmail}
	if defaultListen[cfg.mode] == "" {
		return serveConfig{}, fmt.Errorf("%w: --mode is http, https or ssh", errUsage)
	}
	var err error
	if cfg.trusted, err = front.ParsePrefixes(*trusted); err != nil {
		return serveConfig{}, err
	}
	for _, h := range strings.Split(*acmeHosts, ",") {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			cfg.acmeHosts = append(cfg.acmeHosts, h)
		}
	}
	switch {
	case cfg.mode != modeHTTPS && (cfg.cert != "" || cfg.key != "" || cfg.acme):
		return serveConfig{}, fmt.Errorf("%w: --cert, --key and --acme need --mode https", errUsage)
	case (cfg.cert == "") != (cfg.key == ""):
		return serveConfig{}, fmt.Errorf("%w: give both --cert and --key", errUsage)
	case cfg.cert != "" && cfg.acme:
		return serveConfig{}, fmt.Errorf("%w: use either --cert and --key or --acme", errUsage)
	}
	if cfg.listen == "" {
		cfg.listen = defaultListen[cfg.mode]
	}
	cfg.vhosts = *vhosts
	if cfg.vhosts == "" {
		cfg.vhosts = os.Getenv("HELPBUOY_SERVER_VHOSTS")
	}
	if cfg.vhosts != "" {
		if *state != "" {
			return serveConfig{}, fmt.Errorf("%w: use either --state or --vhosts", errUsage)
		}
		if cfg.mode == modeSSH {
			return serveConfig{}, fmt.Errorf("%w: virtual hosts need --mode http or https; plain SSH can't name the host", errUsage)
		}
		if cfg.acme && len(cfg.acmeHosts) > 0 {
			return serveConfig{}, fmt.Errorf("%w: with --vhosts, --acme gets certificates for the virtual hosts; leave out --acme-host", errUsage)
		}
		return cfg, nil
	}
	if cfg.acme && len(cfg.acmeHosts) == 0 {
		return serveConfig{}, fmt.Errorf("%w: --acme needs --acme-host with the server's host name", errUsage)
	}
	cfg.state, err = stateDir(*state)
	return cfg, err
}

func serve(args []string, stdout, stderr io.Writer) error {
	cfg, err := parseServe(args, stderr)
	if err != nil {
		return err
	}
	base := cfg.state
	if cfg.vhosts != "" {
		base = cfg.vhosts
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(stdout, nil))

	ln, err := net.Listen("tcp", cfg.listen)
	if err != nil {
		return err
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)

	if cfg.mode == modeSSH {
		if err := setup(cfg.state, stdout); err != nil {
			ln.Close()
			return err
		}
		srv, lf, err := startServer(cfg.state, log)
		if err != nil {
			ln.Close()
			return err
		}
		defer lf.Close()
		log.Info("listening", "addr", ln.Addr().String(), "mode", cfg.mode)
		go func() {
			s := <-sig
			log.Info("signal, shutting down", "signal", s.String())
			srv.Close()
		}()
		err = srv.Serve(ln)
		srv.Close()
		if errors.Is(err, server.ErrServerClosed) {
			return nil
		}
		return err
	}

	f := front.New(ln.Addr(), cfg.trusted)
	var hosts *vhosts
	if cfg.vhosts == "" {
		if err := setup(cfg.state, stdout); err != nil {
			ln.Close()
			return err
		}
		srv, lf, err := startServer(cfg.state, log)
		if err != nil {
			ln.Close()
			return err
		}
		defer lf.Close()
		defer srv.Close()
		go srv.Serve(f.Listener())
	} else {
		hosts = &vhosts{dir: cfg.vhosts, front: f, log: log, running: map[string]*vhost{}, done: make(chan struct{})}
		hosts.sync()
		defer hosts.closeAll()
		go hosts.watch()
	}

	hs := transport.NewHTTPServer(f)
	if cfg.mode == modeHTTPS {
		hs.TLSConfig, err = tlsConfig(cfg, base, f)
		if err != nil {
			ln.Close()
			return err
		}
		ln = tls.NewListener(ln, hs.TLSConfig)
	}
	log.Info("listening", "addr", ln.Addr().String(), "mode", cfg.mode, "vhosts", cfg.vhosts)

	go func() {
		s := <-sig
		log.Info("signal, shutting down", "signal", s.String())
		if hosts != nil {
			hosts.stop()
		}
		hs.Close()
	}()
	err = hs.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// envOr returns the environment variable name, or def when it is empty.
func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// startServer runs the SSH server for one state directory, logging to
// log and to server.log in that directory.
func startServer(dir string, log *slog.Logger) (*server.Server, io.Closer, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, err
	}
	lf, err := logfile.Open(filepath.Join(dir, "server.log"), logfile.DefaultMaxSize)
	if err != nil {
		return nil, nil, err
	}
	both := slog.New(teeHandler{log.Handler(), slog.NewJSONHandler(lf, nil)})
	srv, err := server.New(server.Config{StateDir: dir, Version: version, Log: both})
	if err != nil {
		lf.Close()
		return nil, nil, err
	}
	return srv, lf, nil
}

// teeHandler writes each record to two handlers.
type teeHandler struct{ a, b slog.Handler }

func (t teeHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return t.a.Enabled(ctx, l) || t.b.Enabled(ctx, l)
}

func (t teeHandler) Handle(ctx context.Context, r slog.Record) error {
	err := t.a.Handle(ctx, r.Clone())
	if err2 := t.b.Handle(ctx, r); err == nil {
		err = err2
	}
	return err
}

func (t teeHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return teeHandler{t.a.WithAttrs(as), t.b.WithAttrs(as)}
}

func (t teeHandler) WithGroup(g string) slog.Handler {
	return teeHandler{t.a.WithGroup(g), t.b.WithGroup(g)}
}

// tlsConfig returns the built-in HTTPS server's TLS configuration: the
// certificate files, Let's Encrypt, or a self-signed certificate kept in
// base (or base/.tls with virtual hosts).
func tlsConfig(cfg serveConfig, base string, f *front.Front) (*tls.Config, error) {
	dir := base
	if cfg.vhosts != "" {
		dir = filepath.Join(base, tlsDirName)
	}
	switch {
	case cfg.cert != "":
		fc := &fileCert{cert: cfg.cert, key: cfg.key}
		if _, err := fc.get(nil); err != nil {
			return nil, err
		}
		return &tls.Config{GetCertificate: fc.get, NextProtos: []string{"http/1.1"}}, nil
	case cfg.acme:
		if err := os.MkdirAll(filepath.Join(dir, "acme"), 0o700); err != nil {
			return nil, err
		}
		m := &autocert.Manager{
			Prompt: autocert.AcceptTOS,
			Cache:  autocert.DirCache(filepath.Join(dir, "acme")),
			Email:  cfg.acmeEmail,
		}
		if cfg.vhosts != "" {
			m.HostPolicy = func(_ context.Context, host string) error {
				if !f.HasHost(host) {
					return fmt.Errorf("no virtual host %s", host)
				}
				return nil
			}
		} else {
			m.HostPolicy = autocert.HostWhitelist(cfg.acmeHosts...)
		}
		tc := m.TLSConfig()
		tc.NextProtos = []string{"http/1.1", acme.ALPNProto}
		return tc, nil
	default:
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		cert, err := transport.LoadOrCreateSelfSigned(dir)
		if err != nil {
			return nil, err
		}
		return &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"http/1.1"}}, nil
	}
}

// fileCert serves a certificate from files and loads them again when they
// change, so a renewal (certbot) needs no restart. It checks at most once a
// minute.
type fileCert struct {
	cert, key string

	mu      sync.Mutex
	loaded  *tls.Certificate
	mod     time.Time
	checked time.Time
}

func (c *fileCert) get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loaded != nil && time.Since(c.checked) < time.Minute {
		return c.loaded, nil
	}
	c.checked = time.Now()
	mod := modTime(c.cert).Add(time.Duration(modTime(c.key).UnixNano()))
	if c.loaded != nil && mod.Equal(c.mod) {
		return c.loaded, nil
	}
	cert, err := tls.LoadX509KeyPair(c.cert, c.key)
	if err != nil {
		if c.loaded != nil {
			return c.loaded, nil // keep serving the old one during a renewal
		}
		return nil, err
	}
	c.loaded, c.mod = &cert, mod
	return c.loaded, nil
}

func modTime(name string) time.Time {
	fi, err := os.Stat(name)
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}

// vhosts keeps one SSH server running per subdirectory of dir.
type vhosts struct {
	dir   string
	front *front.Front
	log   *slog.Logger

	mu      sync.Mutex
	running map[string]*vhost
	done    chan struct{}
	once    sync.Once
}

type vhost struct {
	srv *server.Server
	lf  io.Closer
}

// watch picks up added and removed virtual hosts every second.
func (v *vhosts) watch() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-v.done:
			return
		case <-t.C:
			v.sync()
		}
	}
}

func (v *vhosts) stop() {
	v.once.Do(func() { close(v.done) })
}

// sync starts a server for every new virtual host directory and stops the
// servers whose directory is gone.
func (v *vhosts) sync() {
	names, err := listVhosts(v.dir)
	if err != nil {
		v.log.Error("reading virtual hosts", "dir", v.dir, "err", err.Error())
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	want := map[string]bool{}
	for _, name := range names {
		want[name] = true
		if v.running[name] != nil {
			continue
		}
		log := v.log.With("vhost", name)
		srv, lf, err := startServer(filepath.Join(v.dir, name), log)
		if err != nil {
			log.Error("virtual host not started", "err", err.Error())
			continue
		}
		v.running[name] = &vhost{srv: srv, lf: lf}
		go srv.Serve(v.front.AddHost(name))
		log.Info("virtual host started")
	}
	for name, h := range v.running {
		if !want[name] {
			v.front.RemoveHost(name)
			h.srv.Close()
			h.lf.Close()
			delete(v.running, name)
			v.log.Info("virtual host removed", "vhost", name)
		}
	}
}

func (v *vhosts) closeAll() {
	v.stop()
	v.mu.Lock()
	defer v.mu.Unlock()
	for name, h := range v.running {
		v.front.RemoveHost(name)
		h.srv.Close()
		h.lf.Close()
		delete(v.running, name)
	}
}

// listVhosts returns the virtual host names in dir: the subdirectories
// named after a host name.
func listVhosts(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range ents {
		if e.IsDir() && validVhost(e.Name()) == nil {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// validVhost checks that name is a lower-case host name, as clients send it
// in the Host header.
func validVhost(name string) error {
	if strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".") || net.ParseIP(name) != nil {
		return fmt.Errorf("virtual host %q: use a host name like acme.relay.example.com", name)
	}
	a, err := serveraddr.Parse(name)
	if err != nil || a.Host != name {
		return fmt.Errorf("virtual host %q: use a lower-case host name like acme.relay.example.com", name)
	}
	return nil
}

// stateFlags are the flags that pick the state directory of the admin
// commands: --state, or --vhosts with --vhost.
type stateFlags struct {
	state, vhosts, vhost *string
}

func addStateFlags(fs *flag.FlagSet) *stateFlags {
	return &stateFlags{
		state:  fs.String("state", "", "state directory"),
		vhosts: fs.String("vhosts", "", "virtual hosts directory (default: $HELPBUOY_SERVER_VHOSTS)"),
		vhost:  fs.String("vhost", "", "virtual host to work on, with --vhosts"),
	}
}

func (f *stateFlags) vhostsDir() (string, error) {
	if *f.vhosts != "" {
		return *f.vhosts, nil
	}
	if d := os.Getenv("HELPBUOY_SERVER_VHOSTS"); d != "" {
		return d, nil
	}
	return "", fmt.Errorf("%w: give the virtual hosts directory with --vhosts", errUsage)
}

// dir returns the state directory the command works on.
func (f *stateFlags) dir() (string, error) {
	if *f.vhost == "" {
		if *f.vhosts != "" || (*f.state == "" && os.Getenv("HELPBUOY_SERVER_VHOSTS") != "") {
			return "", fmt.Errorf("%w: this server has virtual hosts, name one with --vhost", errUsage)
		}
		return stateDir(*f.state)
	}
	if *f.state != "" {
		return "", fmt.Errorf("%w: use either --state or --vhost", errUsage)
	}
	base, err := f.vhostsDir()
	if err != nil {
		return "", err
	}
	name := strings.ToLower(*f.vhost)
	if err := validVhost(name); err != nil {
		return "", err
	}
	dir := filepath.Join(base, name)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("no virtual host %s in %s; add it with: helpbuoy-server vhost add %s", name, base, name)
	}
	return dir, nil
}

func vhostCmd(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("%w: vhost list|add", errUsage)
	}
	fs := flag.NewFlagSet("vhost "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	state := addStateFlags(fs)
	pos, err := parse(fs, args[1:])
	if err != nil {
		return err
	}
	base, err := state.vhostsDir()
	if err != nil {
		return err
	}
	switch {
	case args[0] == "list" && len(pos) == 0:
		names, err := listVhosts(base)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "VHOST\tHOST KEY\tCOMPANY\tTECHIES\tCOMPUTERS")
		for _, name := range names {
			dir := filepath.Join(base, name)
			fpr := "-"
			if s, err := hostkey.LoadOrCreate(dir); err == nil {
				fpr = auth.Fingerprint(s.PublicKey())
			}
			company, techies, computers := "-", 0, 0
			if st, err := store.Open(dir); err == nil {
				if idx, _, err := st.Load(); err == nil {
					if c := idx.State.Info().Company; c != "" {
						company = c
					}
					techies, computers = len(idx.State.Techies), len(idx.State.Computers)
				}
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\n", name, fpr, company, techies, computers)
		}
		return tw.Flush()
	case args[0] == "add" && len(pos) == 1:
		name := strings.ToLower(pos[0])
		if err := validVhost(name); err != nil {
			return err
		}
		dir := filepath.Join(base, name)
		if _, err := os.Stat(dir); err == nil {
			return fmt.Errorf("virtual host %s already exists", name)
		}
		if err := os.MkdirAll(base, 0o700); err != nil {
			return err
		}
		// Made in a temporary directory and renamed, so a running server
		// never starts a half-made virtual host.
		tmp, err := os.MkdirTemp(base, "."+name+".")
		if err != nil {
			return err
		}
		s, err := hostkey.LoadOrCreate(tmp)
		if err != nil {
			os.RemoveAll(tmp)
			return err
		}
		if err := os.Rename(tmp, dir); err != nil {
			os.RemoveAll(tmp)
			return err
		}
		fmt.Fprintf(stdout, "added %s, host key %s\n", name, auth.Fingerprint(s.PublicKey()))
		fmt.Fprintf(stdout, "Next: helpbuoy-server info --vhost %s --company \"...\" --phone \"...\"\n", name)
		fmt.Fprintf(stdout, "and:  helpbuoy-server techie add --vhost %s NAME 'ssh-ed25519 ...'\n", name)
		return nil
	}
	return fmt.Errorf("%w: vhost list | vhost add NAME", errUsage)
}
