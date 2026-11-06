// Command helpbuoy-server is the helpbuoy SSH server. It runs as a normal user
// process next to the desk app and listens on a single port.
//
// Besides serve, its subcommands administer the state directory directly;
// a running server notices their changes within a second.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/helpbuoy/helpbuoy/proto/control"
	"github.com/helpbuoy/helpbuoy/proto/datadir"
	"github.com/helpbuoy/helpbuoy/proto/serveraddr"
	"github.com/helpbuoy/helpbuoy/proto/verify"
	"github.com/helpbuoy/helpbuoy/server/auth"
	"github.com/helpbuoy/helpbuoy/server/hostkey"
	"github.com/helpbuoy/helpbuoy/server/logfile"
	"github.com/helpbuoy/helpbuoy/server/salt"
	"github.com/helpbuoy/helpbuoy/server/server"
	"github.com/helpbuoy/helpbuoy/server/store"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `usage: helpbuoy-server <command> [flags]

commands:
  serve [--listen :5959] [--state DIR]   run the server
  techie list                            list techies
  techie add NAME 'ssh-ed25519 ...'      add a techie key
  techie remove NAME                     remove a techie
  computer list                          list registered computers
  computer add 'ssh-ed25519 ...' [NAME]  register a computer key (NAME defaults to the key comment)
  computer rename SHA256:... NAME        rename a registered computer
  computer remove SHA256:...             remove a registered computer
  title [TEXT]                           print or set the title the apps show ("" for none)
  instruction [TEXT]                     print or set the instruction line the apps show ("" for the default)
  hostkey [--new]                        print the server host key and fingerprint; --new makes a
                                         new host key, used from the next server start on
  verification [--new]                   print the host key and the id to enter in the HelpBuoy
                                         portal; --new makes a new salt, used from the next
                                         server start on, and prints its id
  version                                print the version

Every command except version takes --state DIR (default: $HELPBUOY_SERVER_STATE,
else the per-user data directory).
`

// errUsage marks errors in the command line.
var errUsage = errors.New("usage")

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	var err error
	switch args[0] {
	case "serve":
		err = serve(args[1:], stdout, stderr)
	case "techie":
		err = techieCmd(args[1:], stdout, stderr)
	case "computer":
		err = computerCmd(args[1:], stdout, stderr)
	case "title", "instruction":
		err = infoCmd(args[0], args[1:], stdout, stderr)
	case "hostkey":
		err = hostkeyCmd(args[1:], stdout, stderr)
	case "verification":
		err = verificationCmd(args[1:], stdout, stderr)
	case "version", "--version", "-v":
		fmt.Fprintf(stdout, "helpbuoy-server %s\n", version)
		return 0
	case "help", "--help", "-h":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "helpbuoy-server: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintf(stderr, "helpbuoy-server: %v\n", err)
		if errors.Is(err, errUsage) {
			return 2
		}
		return 1
	}
	return 0
}

// parse parses flags that may appear before, between or after positional
// arguments and returns the positional ones.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

// stateDir returns the state directory: the --state flag, else the
// HELPBUOY_SERVER_STATE environment variable (set by the Debian package's service
// and helpbuoy-serverctl to /var/lib/helpbuoy-server), else the per-user data directory.
func stateDir(s string) (string, error) {
	if s != "" {
		return s, nil
	}
	if s := os.Getenv("HELPBUOY_SERVER_STATE"); s != "" {
		return s, nil
	}
	return datadir.Dir("helpbuoy-server")
}

type serveConfig struct {
	listen string
	state  string
}

func parseServe(args []string, stderr io.Writer) (serveConfig, error) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	listen := fs.String("listen", fmt.Sprintf(":%d", serveraddr.DefaultPort), "address to listen on, host:port or :port")
	state := fs.String("state", "", "state directory (default: $HELPBUOY_SERVER_STATE, else the per-user data directory)")
	if err := fs.Parse(args); err != nil {
		return serveConfig{}, err
	}
	if fs.NArg() > 0 {
		return serveConfig{}, fmt.Errorf("serve: unexpected argument %q", fs.Arg(0))
	}
	dir, err := stateDir(*state)
	if err != nil {
		return serveConfig{}, err
	}
	return serveConfig{listen: *listen, state: dir}, nil
}

func serve(args []string, stdout, stderr io.Writer) error {
	cfg, err := parseServe(args, stderr)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.state, 0o700); err != nil {
		return err
	}
	lf, err := logfile.Open(filepath.Join(cfg.state, "server.log"), logfile.DefaultMaxSize)
	if err != nil {
		return err
	}
	defer lf.Close()
	log := slog.New(slog.NewJSONHandler(io.MultiWriter(stdout, lf), nil))
	srv, err := server.New(server.Config{StateDir: cfg.state, Version: version, Log: log})
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", cfg.listen)
	if err != nil {
		srv.Close()
		return err
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
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

func openStore(state string) (*store.Store, error) {
	dir, err := stateDir(state)
	if err != nil {
		return nil, err
	}
	return store.Open(dir)
}

func techieCmd(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("%w: techie list|add|remove", errUsage)
	}
	fs := flag.NewFlagSet("techie "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	state := fs.String("state", "", "state directory")
	pos, err := parse(fs, args[1:])
	if err != nil {
		return err
	}
	st, err := openStore(*state)
	if err != nil {
		return err
	}
	switch args[0] {
	case "list":
		if len(pos) != 0 {
			return fmt.Errorf("%w: techie list", errUsage)
		}
		idx, _, err := st.Load()
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tFINGERPRINT")
		for i, h := range idx.State.Techies {
			fmt.Fprintf(tw, "%s\t%s\n", h.Name, idx.TechieFingerprint(i))
		}
		tw.Flush()
	case "add":
		if len(pos) != 2 {
			return fmt.Errorf("%w: techie add NAME 'ssh-ed25519 ...'", errUsage)
		}
		_, _, err := st.Update(func(s *store.State) error { return s.AddTechie(pos[0], pos[1]) })
		if err != nil {
			return err
		}
		k, _, _ := auth.ParseKey(pos[1])
		fmt.Fprintf(stdout, "added techie %s (%s)\n", pos[0], auth.Fingerprint(k))
	case "remove":
		if len(pos) != 1 {
			return fmt.Errorf("%w: techie remove NAME", errUsage)
		}
		if _, _, err := st.Update(func(s *store.State) error { return s.RemoveTechie(pos[0]) }); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "removed techie %s\n", pos[0])
	default:
		return fmt.Errorf("%w: techie list|add|remove", errUsage)
	}
	return nil
}

func computerCmd(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("%w: computer list|add|rename|remove", errUsage)
	}
	fs := flag.NewFlagSet("computer "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	state := fs.String("state", "", "state directory")
	pos, err := parse(fs, args[1:])
	if err != nil {
		return err
	}
	st, err := openStore(*state)
	if err != nil {
		return err
	}
	switch args[0] {
	case "list":
		if len(pos) != 0 {
			return fmt.Errorf("%w: computer list", errUsage)
		}
		idx, _, err := st.Load()
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tFINGERPRINT\tREGISTERED BY\tREGISTERED AT\tLAST SEEN")
		for i, c := range idx.State.Computers {
			seen := "never"
			if !c.LastSeen.IsZero() {
				seen = c.LastSeen.Local().Format(time.DateTime)
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", c.Name, idx.ComputerFingerprint(i), c.RegisteredBy,
				c.RegisteredAt.Local().Format(time.DateTime), seen)
		}
		tw.Flush()
	case "add":
		if len(pos) < 1 || len(pos) > 2 {
			return fmt.Errorf("%w: computer add 'ssh-ed25519 ...' [NAME]", errUsage)
		}
		name := ""
		if len(pos) == 2 {
			name = pos[1]
		}
		var fp string
		_, _, err := st.Update(func(s *store.State) error {
			var err error
			fp, name, err = s.AddComputer(pos[0], name, "admin", time.Now())
			return err
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "added computer %s (%s)\n", name, fp)
	case "rename":
		if len(pos) != 2 {
			return fmt.Errorf("%w: computer rename SHA256:... NAME", errUsage)
		}
		var old string
		_, _, err := st.Update(func(s *store.State) error {
			var err error
			old, err = s.RenameComputer(pos[0], pos[1])
			return err
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "renamed computer %s to %s (%s)\n", old, strings.TrimSpace(pos[1]), pos[0])
	case "remove":
		if len(pos) != 1 {
			return fmt.Errorf("%w: computer remove SHA256:...", errUsage)
		}
		var name string
		_, _, err := st.Update(func(s *store.State) error {
			var err error
			name, err = s.RemoveComputer(pos[0])
			return err
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "removed computer %s (%s)\n", name, pos[0])
	default:
		return fmt.Errorf("%w: computer list|add|rename|remove", errUsage)
	}
	return nil
}

// infoCmd prints or sets the title or the instruction line (what). A
// running server picks up the change and sends it to every app.
func infoCmd(what string, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet(what, flag.ContinueOnError)
	fs.SetOutput(stderr)
	state := fs.String("state", "", "state directory")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 1 {
		return fmt.Errorf("%w: %s [TEXT]", errUsage, what)
	}
	st, err := openStore(*state)
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		idx, _, err := st.Load()
		if err != nil {
			return err
		}
		v := idx.State.Title
		if what == "instruction" {
			v = idx.State.Instruction
			if v == "" {
				v = control.DefaultInstruction + " (default)"
			}
		}
		fmt.Fprintln(stdout, v)
		return nil
	}
	idx, _, err := st.Update(func(s *store.State) error {
		if what == "title" {
			return s.SetInfo(pos[0], s.Instruction)
		}
		return s.SetInfo(s.Title, pos[0])
	})
	if err != nil {
		return err
	}
	v := idx.State.Title
	if what == "instruction" {
		v = idx.State.Instruction
	}
	if v == "" {
		if what == "instruction" {
			fmt.Fprintln(stdout, "cleared the instruction; computers show the default")
		} else {
			fmt.Fprintln(stdout, "cleared the title")
		}
	} else {
		fmt.Fprintf(stdout, "set the %s to %q\n", what, v)
	}
	return nil
}

func hostkeyCmd(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("hostkey", flag.ContinueOnError)
	fs.SetOutput(stderr)
	state := fs.String("state", "", "state directory")
	newKey := fs.Bool("new", false, "make a new host key, used from the next server start on")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return fmt.Errorf("%w: hostkey", errUsage)
	}
	dir, err := stateDir(*state)
	if err != nil {
		return err
	}
	if *newKey {
		s, err := hostkey.New(dir)
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, hostkey.PublicLine(s))
		fmt.Fprintln(stdout, auth.Fingerprint(s.PublicKey()))
		fmt.Fprintln(stdout, "This host key is used from the next server start on. Every registered computer\n"+
			"will then warn that the server identity changed; tell its users first, by your own\n"+
			"channels. Techies and computers are kept.")
		return nil
	}
	s, err := hostkey.LoadOrCreate(dir)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, hostkey.PublicLine(s))
	fmt.Fprintln(stdout, auth.Fingerprint(s.PublicKey()))
	if n, ok, err := hostkey.Pending(dir); err != nil {
		return err
	} else if ok {
		fmt.Fprintf(stdout, "from the next start: %s\n", auth.Fingerprint(n.PublicKey()))
	}
	return nil
}

// verificationCmd prints what a company enters in the HelpBuoy portal to get
// its server verified (PLAN.md 12.7): the host key line and the id. It never
// prints the salt itself.
func verificationCmd(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("verification", flag.ContinueOnError)
	fs.SetOutput(stderr)
	state := fs.String("state", "", "state directory")
	next := fs.Bool("new", false, "make a new salt, used from the next server start on, and print its id")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return fmt.Errorf("%w: verification [--new]", errUsage)
	}
	dir, err := stateDir(*state)
	if err != nil {
		return err
	}
	// The host key and salt the server uses after its next start.
	hk, pendingKey, err := hostkey.Pending(dir)
	if err != nil {
		return err
	}
	if !pendingKey {
		if hk, err = hostkey.LoadOrCreate(dir); err != nil {
			return err
		}
	}
	var s []byte
	pendingSalt := false
	switch {
	case *next:
		s, err = salt.Next(dir)
	default:
		if s, pendingSalt, err = salt.Pending(dir); err == nil && !pendingSalt {
			s, err = salt.LoadOrCreate(dir)
		}
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "host key: %s\n", hostkey.PublicLine(hk))
	fmt.Fprintf(stdout, "id:       %s\n", verify.ID(hk.PublicKey(), s))
	switch {
	case *next:
		fmt.Fprintln(stdout, "This id is used from the next server start on. Enter it in the HelpBuoy portal\n"+
			"and restart the server only once the portal says its file is live; until then\n"+
			"computers would see this server as not verified.")
	default:
		if pendingKey || pendingSalt {
			fmt.Fprintln(stdout, "This is the id from the next server start on; until then the server uses its\n"+
				"current host key and salt.")
		}
	}
	return nil
}
