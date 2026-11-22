package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"golang.org/x/crypto/ssh"

	"github.com/helpbuoy/helpbuoy/proto/hostprobe"
	"github.com/helpbuoy/helpbuoy/proto/serveraddr"
	"github.com/helpbuoy/helpbuoy/proto/transport"
	"github.com/helpbuoy/helpbuoy/server/store"
)

// probeServer looks up the host key fingerprint of another server; tests
// replace it.
var probeServer = func(addr string) (string, error) {
	key, err := hostprobe.Probe(context.Background(), addr, 0, transport.DialOptions{})
	if err != nil {
		return "", err
	}
	return ssh.FingerprintSHA256(key), nil
}

// otherCmd manages the other servers the computers of this server may use
// (PLAN.md 12.3), like the Other servers part of the desk app's Server tab.
// A server is named by its address.
func otherCmd(args []string, stdout, stderr io.Writer) error {
	const use = "other list|add|rename|remove"
	if len(args) == 0 {
		return fmt.Errorf("%w: %s", errUsage, use)
	}
	fs := flag.NewFlagSet("other "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	state := addStateFlags(fs)
	fingerprint, none := "", false
	switch args[0] {
	case "add":
		fs.StringVar(&fingerprint, "fingerprint", "", "host key fingerprint of the server, instead of looking it up")
	case "rename":
		fs.BoolVar(&none, "none", false, "take the name away")
	}
	pos, err := parse(fs, args[1:])
	if err != nil {
		return err
	}
	st, err := openStore(state)
	if err != nil {
		return err
	}
	// address returns the normalized form of the address the admin typed.
	address := func(s string) (string, error) {
		a, err := serveraddr.Parse(s)
		if err != nil {
			return "", err
		}
		return a.String(), nil
	}
	switch args[0] {
	case "list":
		if len(pos) != 0 {
			return fmt.Errorf("%w: other list", errUsage)
		}
		idx, _, err := st.Load()
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ADDRESS\tNAME\tFINGERPRINT")
		for _, a := range idx.State.Approved {
			name := a.Name
			if name == "" {
				name = "-"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\n", a.Address, name, a.Fingerprint)
		}
		tw.Flush()
	case "add":
		if len(pos) < 1 || len(pos) > 2 {
			return fmt.Errorf("%w: other add ADDRESS [NAME] [--fingerprint SHA256:...]", errUsage)
		}
		addr, err := address(pos[0])
		if err != nil {
			return err
		}
		name := ""
		if len(pos) == 2 {
			name = pos[1]
		}
		fp := fingerprint
		if fp == "" {
			if fp, err = probeServer(addr); err != nil {
				return fmt.Errorf("no HelpBuoy server answers at %s: %w", addr, err)
			}
		}
		if _, _, err := st.Update(func(s *store.State) error {
			_, err := s.AddApproved(addr, fp, name)
			return err
		}); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "added other server %s (%s)\n", addr, strings.TrimSpace(fp))
	case "rename":
		if len(pos) != 2 && !(none && len(pos) == 1) || none && len(pos) == 2 {
			return fmt.Errorf("%w: other rename ADDRESS NAME, or --none instead of the name", errUsage)
		}
		addr, err := address(pos[0])
		if err != nil {
			return err
		}
		name := ""
		if !none {
			if name = strings.TrimSpace(pos[1]); name == "" {
				return fmt.Errorf("%w: the name is empty; use --none to take it away", errUsage)
			}
		}
		if _, _, err := st.Update(func(s *store.State) error {
			_, err := s.RenameApproved(addr, name)
			return err
		}); err != nil {
			return err
		}
		if none {
			fmt.Fprintf(stdout, "other server %s has no name now\n", addr)
		} else {
			fmt.Fprintf(stdout, "renamed other server %s to %s\n", addr, name)
		}
	case "remove":
		if len(pos) != 1 {
			return fmt.Errorf("%w: other remove ADDRESS", errUsage)
		}
		addr, err := address(pos[0])
		if err != nil {
			return err
		}
		if _, _, err := st.Update(func(s *store.State) error { return s.RemoveApproved(addr) }); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "removed other server %s\n", addr)
	default:
		return fmt.Errorf("%w: %s", errUsage, use)
	}
	return nil
}
