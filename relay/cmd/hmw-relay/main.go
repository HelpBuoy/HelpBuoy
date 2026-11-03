// Command hmw-relay is the help-me-win SSH relay. It runs as a normal user
// process next to the helper app and listens on a single port.
//
// Besides serve, its subcommands administer the state directory directly;
// a running relay notices their changes within a second.
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

	"github.com/mevdschee/help-me-win/proto/datadir"
	"github.com/mevdschee/help-me-win/proto/relayaddr"
	"github.com/mevdschee/help-me-win/relay/auth"
	"github.com/mevdschee/help-me-win/relay/hostkey"
	"github.com/mevdschee/help-me-win/relay/logfile"
	"github.com/mevdschee/help-me-win/relay/server"
	"github.com/mevdschee/help-me-win/relay/store"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `usage: hmw-relay <command> [flags]

commands:
  serve [--listen :5938] [--state DIR]                   run the relay
  helper add NAME [--display "Name"] 'ssh-ed25519 ...'   add a helper key
  helper remove NAME                                     remove a helper
  helper list                                            list helpers
  client list                                            list registered clients
  client rename SHA256:... NAME                          rename a registered client
  client remove SHA256:...                               remove a registered client
  hostkey                                                print the relay host key and fingerprint
  version                                                print the version

Every command except version takes --state DIR (default: $HMW_RELAY_STATE,
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
	case "helper":
		err = helperCmd(args[1:], stdout, stderr)
	case "client":
		err = clientCmd(args[1:], stdout, stderr)
	case "hostkey":
		err = hostkeyCmd(args[1:], stdout, stderr)
	case "version", "--version", "-v":
		fmt.Fprintf(stdout, "hmw-relay %s\n", version)
		return 0
	case "help", "--help", "-h":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "hmw-relay: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintf(stderr, "hmw-relay: %v\n", err)
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
// HMW_RELAY_STATE environment variable (set by the Debian package's service
// and hmw-relayctl to /var/lib/hmw-relay), else the per-user data directory.
func stateDir(s string) (string, error) {
	if s != "" {
		return s, nil
	}
	if s := os.Getenv("HMW_RELAY_STATE"); s != "" {
		return s, nil
	}
	return datadir.Dir("hmw-relay")
}

type serveConfig struct {
	listen string
	state  string
}

func parseServe(args []string, stderr io.Writer) (serveConfig, error) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	listen := fs.String("listen", fmt.Sprintf(":%d", relayaddr.DefaultPort), "address to listen on, host:port or :port")
	state := fs.String("state", "", "state directory (default: $HMW_RELAY_STATE, else the per-user data directory)")
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
	lf, err := logfile.Open(filepath.Join(cfg.state, "relay.log"), logfile.DefaultMaxSize)
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

func helperCmd(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("%w: helper add|remove|list", errUsage)
	}
	fs := flag.NewFlagSet("helper "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	state := fs.String("state", "", "state directory")
	var display *string
	if args[0] == "add" {
		display = fs.String("display", "", "display name shown to clients (default: NAME)")
	}
	pos, err := parse(fs, args[1:])
	if err != nil {
		return err
	}
	st, err := openStore(*state)
	if err != nil {
		return err
	}
	switch args[0] {
	case "add":
		if len(pos) != 2 {
			return fmt.Errorf("%w: helper add NAME [--display \"Name\"] 'ssh-ed25519 ...'", errUsage)
		}
		_, _, err := st.Update(func(s *store.State) error { return s.AddHelper(pos[0], *display, pos[1]) })
		if err != nil {
			return err
		}
		k, _, _ := auth.ParseKey(pos[1])
		fmt.Fprintf(stdout, "added helper %s (%s)\n", pos[0], auth.Fingerprint(k))
	case "remove":
		if len(pos) != 1 {
			return fmt.Errorf("%w: helper remove NAME", errUsage)
		}
		if _, _, err := st.Update(func(s *store.State) error { return s.RemoveHelper(pos[0]) }); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "removed helper %s\n", pos[0])
	case "list":
		if len(pos) != 0 {
			return fmt.Errorf("%w: helper list", errUsage)
		}
		idx, _, err := st.Load()
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tDISPLAY NAME\tFINGERPRINT")
		for i, h := range idx.State.Helpers {
			fmt.Fprintf(tw, "%s\t%s\t%s\n", h.Name, h.DisplayName, idx.HelperFingerprint(i))
		}
		tw.Flush()
	default:
		return fmt.Errorf("%w: helper add|remove|list", errUsage)
	}
	return nil
}

func clientCmd(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("%w: client list|rename|remove", errUsage)
	}
	fs := flag.NewFlagSet("client "+args[0], flag.ContinueOnError)
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
			return fmt.Errorf("%w: client list", errUsage)
		}
		idx, _, err := st.Load()
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tFINGERPRINT\tREGISTERED BY\tREGISTERED AT\tLAST SEEN")
		for i, c := range idx.State.Clients {
			seen := "never"
			if !c.LastSeen.IsZero() {
				seen = c.LastSeen.Local().Format(time.DateTime)
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", c.Name, idx.ClientFingerprint(i), c.RegisteredBy,
				c.RegisteredAt.Local().Format(time.DateTime), seen)
		}
		tw.Flush()
	case "remove":
		if len(pos) != 1 {
			return fmt.Errorf("%w: client remove SHA256:...", errUsage)
		}
		var name string
		_, _, err := st.Update(func(s *store.State) error {
			var err error
			name, err = s.RemoveClient(pos[0])
			return err
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "removed client %s (%s)\n", name, pos[0])
	case "rename":
		if len(pos) != 2 {
			return fmt.Errorf("%w: client rename SHA256:... NAME", errUsage)
		}
		var old string
		_, _, err := st.Update(func(s *store.State) error {
			var err error
			old, err = s.RenameClient(pos[0], pos[1])
			return err
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "renamed client %s to %s (%s)\n", old, strings.TrimSpace(pos[1]), pos[0])
	default:
		return fmt.Errorf("%w: client list|rename|remove", errUsage)
	}
	return nil
}

func hostkeyCmd(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("hostkey", flag.ContinueOnError)
	fs.SetOutput(stderr)
	state := fs.String("state", "", "state directory")
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
	s, err := hostkey.LoadOrCreate(dir)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, hostkey.PublicLine(s))
	fmt.Fprintln(stdout, auth.Fingerprint(s.PublicKey()))
	return nil
}
