// Command helpbuoy-server is the helpbuoy SSH server. It runs as a normal user
// process next to the desk app and listens on a single port.
//
// Besides serve, its subcommands administer the state directory directly;
// a running server notices their changes within a second.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/helpbuoy/helpbuoy/proto/control"
	"github.com/helpbuoy/helpbuoy/proto/datadir"
	"github.com/helpbuoy/helpbuoy/server/auth"
	"github.com/helpbuoy/helpbuoy/server/hostkey"
	"github.com/helpbuoy/helpbuoy/server/store"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `usage: helpbuoy-server <command> [flags]

commands:
  serve [--mode MODE]                    run the server in one of three modes (default http):
                                           http   plain HTTP on 127.0.0.1:5959, behind a reverse
                                                  proxy (Apache, nginx, HAProxy) that handles HTTPS
                                           https  the built-in HTTPS server, on :443
                                           ssh    plain SSH without WebSocket and TLS (direct
                                                  connect), on :5959; the apps fall back to it
                                                  when HTTPS fails
    [--listen ADDR]                      address to listen on instead of the mode's default
    [--cert FILE --key FILE]             https: certificate files (reloaded when they change)
    [--acme --acme-host NAME]            https: get certificates from Let's Encrypt; needs port
    [--acme-email ADDR]                  443 reachable from the internet; without --vhosts, list
                                         the host names with --acme-host
                                         https without a certificate: a self-signed one
    [--trusted-proxy LIST]               http and https: proxies whose X-Forwarded-For is used
                                         (default 127.0.0.1,::1)
    [--vhosts DIR]                       http and https: serve every virtual host in DIR (see vhost)
  techie list                            list techies
  techie add NAME 'ssh-ed25519 ...'      add a techie key
  techie remove NAME                     remove a techie
  computer list                          list registered computers
  computer add 'ssh-ed25519 ...' [NAME]  register a computer key (NAME defaults to the key comment)
  computer rename SHA256:... NAME        rename a registered computer
  computer remove SHA256:...             remove a registered computer
  info [--company C] [--phone P]         print or set who runs this server, shown to users waiting
       [--address A] [--description D]   for a techie; the company name and phone number are required
                                         before computers can connect
  hostkey [--new]                        print the server host key and fingerprint; --new makes a
                                         new host key, used from the next server start on
  vhost list                             list the virtual hosts in the --vhosts directory
  vhost add NAME                         add a virtual host, e.g. acme.relay.example.com; a running
                                         server picks it up within a second
  version                                print the version

Every command except version takes --state DIR (default: $HELPBUOY_SERVER_STATE,
else the per-user data directory). serve takes its mode and listen address from
$HELPBUOY_SERVER_MODE and $HELPBUOY_SERVER_LISTEN when the flags are left out. With virtual hosts, the commands take
--vhosts DIR (default: $HELPBUOY_SERVER_VHOSTS) and --vhost NAME instead, to work
on one virtual host. To remove a virtual host, delete its directory; a running
server drops it within a second.
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
	case "info":
		err = infoCmd(args[1:], stdout, stderr)
	case "hostkey":
		err = hostkeyCmd(args[1:], stdout, stderr)
	case "vhost":
		err = vhostCmd(args[1:], stdout, stderr)
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

func openStore(state *stateFlags) (*store.Store, error) {
	dir, err := state.dir()
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
	state := addStateFlags(fs)
	pos, err := parse(fs, args[1:])
	if err != nil {
		return err
	}
	st, err := openStore(state)
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
	state := addStateFlags(fs)
	pos, err := parse(fs, args[1:])
	if err != nil {
		return err
	}
	st, err := openStore(state)
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

// infoCmd prints or sets who runs the server. Only the flags given change;
// a running server picks up the change and sends it to every app.
func infoCmd(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("info", flag.ContinueOnError)
	fs.SetOutput(stderr)
	state := addStateFlags(fs)
	address := fs.String("address", "", "the address computers use, e.g. helpme.example.com (\"\" to clear)")
	company := fs.String("company", "", "company name")
	phone := fs.String("phone", "", "phone number users call for help")
	description := fs.String("description", "", "optional line, for example opening hours (\"\" to clear)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return fmt.Errorf("%w: info [--company C] [--phone P] [--address A] [--description D]", errUsage)
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	st, err := openStore(state)
	if err != nil {
		return err
	}
	var idx *store.Index
	if len(set) == 0 || len(set) == 1 && set["state"] {
		if idx, _, err = st.Load(); err != nil {
			return err
		}
	} else {
		idx, _, err = st.Update(func(s *store.State) error {
			info := s.Info()
			if set["address"] {
				info.Address = *address
			}
			if set["company"] {
				info.Company = *company
			}
			if set["phone"] {
				info.Phone = *phone
			}
			if set["description"] {
				info.Description = *description
			}
			return s.SetInfo(info)
		})
		if err != nil {
			return err
		}
	}
	printInfo(stdout, idx.State.Info())
	return nil
}

func printInfo(w io.Writer, info control.ServerInfo) {
	fmt.Fprintf(w, "company:     %s\n", info.Company)
	fmt.Fprintf(w, "phone:       %s\n", info.Phone)
	fmt.Fprintf(w, "address:     %s\n", info.Address)
	fmt.Fprintf(w, "description: %s\n", info.Description)
	if !info.Configured() {
		fmt.Fprintln(w, "Computers can't connect until the company name and phone number are set.")
	}
}

// stdin and interactive are the terminal serve asks on at setup; tests
// replace them.
var (
	stdin       io.Reader = os.Stdin
	interactive           = func() bool {
		fi, err := os.Stdin.Stat()
		return err == nil && fi.Mode()&os.ModeCharDevice != 0
	}
)

// setup asks for the company name and phone number when the server has
// none and serve runs in a terminal (PLAN.md 12.4). Without a terminal it
// only says how to set them; until then the server refuses computers.
func setup(dir string, stdout io.Writer) error {
	st, err := store.Open(dir)
	if err != nil {
		return err
	}
	idx, _, err := st.Load()
	if err != nil {
		return err
	}
	info := idx.State.Info()
	if info.Configured() {
		return nil
	}
	if !interactive() {
		fmt.Fprintln(stdout, "This server has no company name and phone number yet, so it refuses computers.\n"+
			"Set them with: helpbuoy-server info --company \"Example Inc\" --phone \"+1 234 567 890\"")
		return nil
	}
	fmt.Fprintln(stdout, "Users waiting for a techie see who runs this server and who to call.")
	in := bufio.NewScanner(stdin)
	ask := func(prompt, old string, required bool) (string, error) {
		for {
			if old != "" {
				fmt.Fprintf(stdout, "%s [%s]: ", prompt, old)
			} else {
				fmt.Fprintf(stdout, "%s: ", prompt)
			}
			if !in.Scan() {
				if err := in.Err(); err != nil {
					return "", err
				}
				return "", errors.New("setup: no answer")
			}
			v := strings.TrimSpace(in.Text())
			if v == "" {
				v = old
			}
			if v != "" || !required {
				return v, nil
			}
		}
	}
	for {
		if info.Company, err = ask("Company name", info.Company, true); err != nil {
			return err
		}
		if info.Phone, err = ask("Phone number", info.Phone, true); err != nil {
			return err
		}
		if info.Address, err = ask("Address computers use (optional)", info.Address, false); err != nil {
			return err
		}
		if info.Description, err = ask("Description, for example opening hours (optional)", info.Description, false); err != nil {
			return err
		}
		_, _, err = st.Update(func(s *store.State) error { return s.SetInfo(info) })
		if err == nil {
			return nil
		}
		fmt.Fprintf(stdout, "Not saved: %v\n", err)
	}
}

func hostkeyCmd(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("hostkey", flag.ContinueOnError)
	fs.SetOutput(stderr)
	state := addStateFlags(fs)
	newKey := fs.Bool("new", false, "make a new host key, used from the next server start on")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return fmt.Errorf("%w: hostkey", errUsage)
	}
	dir, err := state.dir()
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
