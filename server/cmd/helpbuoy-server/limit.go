package main

import (
	"flag"
	"fmt"
	"io"

	"github.com/helpbuoy/helpbuoy/server/store"
)

// limitCmd prints or sets the limits the admin gives a server: for now how
// many sessions it allows at the same time (store.State.MaxSessions). Techies
// can't change them, so a host can sell a helpdesk with a fixed number of
// sessions.
func limitCmd(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("limit", flag.ContinueOnError)
	fs.SetOutput(stderr)
	state := addStateFlags(fs)
	sessions := fs.Int("sessions", 0, "sessions allowed at the same time, 0 for no limit")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return fmt.Errorf("%w: limit [--sessions N]", errUsage)
	}
	st, err := openStore(state)
	if err != nil {
		return err
	}
	set := false
	fs.Visit(func(f *flag.Flag) { set = set || f.Name == "sessions" })
	var idx *store.Index
	if !set {
		idx, _, err = st.Load()
	} else {
		idx, _, err = st.Update(func(s *store.State) error { return s.SetMaxSessions(*sessions) })
	}
	if err != nil {
		return err
	}
	if n := idx.State.MaxSessions; n > 0 {
		fmt.Fprintf(stdout, "sessions: %d at the same time\n", n)
	} else {
		fmt.Fprintln(stdout, "sessions: no limit")
	}
	return nil
}
