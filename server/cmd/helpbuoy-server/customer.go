package main

import (
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/helpbuoy/helpbuoy/server/store"
)

// stringList is a flag that can be given more than once.
type stringList []string

func (l *stringList) String() string { return strings.Join(*l, ",") }

func (l *stringList) Set(s string) error {
	*l = append(*l, s)
	return nil
}

// customerCmd manages the customers, the groups of computers on the server
// (PLAN.md 13). Customers are named by name (any case) or id.
func customerCmd(args []string, stdout, stderr io.Writer) error {
	const use = "customer list|add|rename|remove"
	if len(args) == 0 {
		return fmt.Errorf("%w: %s", errUsage, use)
	}
	fs := flag.NewFlagSet("customer "+args[0], flag.ContinueOnError)
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
			return fmt.Errorf("%w: customer list", errUsage)
		}
		idx, _, err := st.Load()
		if err != nil {
			return err
		}
		count := map[string]int{}
		for _, c := range idx.State.Computers {
			count[c.Customer]++
		}
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tID\tCOMPUTERS")
		for _, cu := range idx.State.Customers {
			fmt.Fprintf(tw, "%s\t%s\t%d\n", cu.Name, cu.ID, count[cu.ID])
		}
		if n := count[""]; n > 0 || len(idx.State.Customers) > 0 {
			fmt.Fprintf(tw, "(no customer)\t-\t%d\n", n)
		}
		tw.Flush()
	case "add":
		if len(pos) != 1 {
			return fmt.Errorf("%w: customer add NAME", errUsage)
		}
		var id string
		_, _, err := st.Update(func(s *store.State) error {
			var err error
			id, err = s.AddCustomer(pos[0])
			return err
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "added customer %s (%s)\n", strings.TrimSpace(pos[0]), id)
	case "rename":
		if len(pos) != 2 {
			return fmt.Errorf("%w: customer rename CUSTOMER NAME", errUsage)
		}
		var old string
		_, _, err := st.Update(func(s *store.State) error {
			var err error
			old, err = s.RenameCustomer(pos[0], pos[1])
			return err
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "renamed customer %s to %s\n", old, strings.TrimSpace(pos[1]))
	case "remove":
		if len(pos) != 1 {
			return fmt.Errorf("%w: customer remove CUSTOMER", errUsage)
		}
		var name string
		_, _, err := st.Update(func(s *store.State) error {
			var err error
			name, err = s.RemoveCustomer(pos[0])
			return err
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "removed customer %s; its computers have no customer now\n", name)
	default:
		return fmt.Errorf("%w: %s", errUsage, use)
	}
	return nil
}
