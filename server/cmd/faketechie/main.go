// Command faketechie is a headless stand-in for the desk app, for manual
// end-to-end checks of the server. It connects with the key in --key-dir
// (created if missing), prints Presence changes, can register a computer key
// (--register), and can connect to the first online computer (--connect-first).
// Session channels from the server are accepted: it writes one line into
// each, prints what it receives, and with --stdin also forwards standard
// input.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"

	"golang.org/x/crypto/ssh"

	"github.com/helpbuoy/helpbuoy/proto/control"
	"github.com/helpbuoy/helpbuoy/server/fake"
)

type options struct {
	server, keyDir, register, name, message string
	connectFirst, stdin, once               bool
}

func main() {
	var o options
	flag.StringVar(&o.server, "server", "localhost:5959", "server host:port")
	flag.StringVar(&o.keyDir, "key-dir", "faketechie-key", "directory with the techie key (created if missing)")
	flag.StringVar(&o.register, "register", "", "register this computer key line ('ssh-ed25519 ... user@machine')")
	flag.StringVar(&o.name, "name", "", "computer name for --register when the key has no comment")
	flag.BoolVar(&o.connectFirst, "connect-first", false, "connect to the first online computer")
	flag.StringVar(&o.message, "message", "hello from faketechie", "line written into each session channel")
	flag.BoolVar(&o.stdin, "stdin", false, "forward standard input into the session channel")
	flag.BoolVar(&o.once, "once", false, "exit after the first session ends")
	flag.Parse()
	if err := run(os.Stdout, o); err != nil {
		fmt.Fprintln(os.Stderr, "faketechie:", err)
		os.Exit(1)
	}
}

func run(w io.Writer, o options) error {
	signer, err := fake.LoadOrCreateKey(o.keyDir, "faketechie")
	if err != nil {
		return err
	}
	fmt.Fprintln(w, "key:", fake.KeyLine(signer.PublicKey(), "faketechie"))
	fmt.Fprintln(w, "fingerprint:", ssh.FingerprintSHA256(signer.PublicKey()))
	c, err := fake.Dial(o.server, signer, fake.Options{})
	if err != nil {
		return err
	}
	defer c.Close()
	fmt.Fprintln(w, "server:", ssh.FingerprintSHA256(c.HostKey))

	done := make(chan struct{})
	go func() {
		for nch := range c.Sessions {
			go func() {
				session(w, nch, o)
				if o.once {
					close(done)
				}
			}()
		}
	}()

	var seq uint32
	next := func() uint32 { return atomic.AddUint32(&seq, 1) }
	connected := false
	last := ""
	for {
		var m any
		select {
		case <-done:
			return nil
		case mm, ok := <-c.Msgs:
			if !ok {
				return fmt.Errorf("connection closed")
			}
			m = mm
		}
		switch m := m.(type) {
		case *control.Hello:
			fmt.Fprintf(w, "hello: role=%s name=%q server=%s\n", m.Role, m.Name, m.ServerVersion)
			if m.Role != control.RoleTechie {
				return fmt.Errorf("this key is not a techie on the server (role %s)", m.Role)
			}
			if o.register != "" {
				s := next()
				fmt.Fprintf(w, "register: seq=%d\n", s)
				if err := c.Send(&control.RegisterComputer{Seq: s, PublicKey: o.register, Name: o.name}); err != nil {
					return err
				}
			}
		case *control.Presence:
			if s := formatPresence(m); s != last {
				last = s
				fmt.Fprint(w, s)
			}
			if o.connectFirst && !connected {
				for _, ci := range m.Computers {
					if ci.Online && ci.Request == nil {
						connected = true
						s, id := next(), control.NewRequestID()
						fmt.Fprintf(w, "connect: seq=%d request=%s computer=%s\n", s, id, ci.Name)
						if err := c.Send(&control.Connect{Seq: s, Fingerprint: ci.Fingerprint, RequestID: id}); err != nil {
							return err
						}
						break
					}
				}
			}
		case *control.Reply:
			if m.Error != "" {
				fmt.Fprintf(w, "reply: seq=%d error=%q\n", m.Seq, m.Error)
			} else {
				fmt.Fprintf(w, "reply: seq=%d ok\n", m.Seq)
			}
		case *control.Error:
			return fmt.Errorf("server: %s", m.Message)
		default:
			fmt.Fprintf(w, "message: %T %+v\n", m, m)
		}
	}
}

func formatPresence(p *control.Presence) string {
	var b strings.Builder
	fmt.Fprintf(&b, "presence: %d computers, %d techies\n", len(p.Computers), len(p.Techies))
	for _, c := range p.Computers {
		state := "offline"
		if c.Online {
			state = "online"
		}
		if c.Request != nil {
			state += " request=" + c.Request.ID + ":" + string(c.Request.State)
		}
		if c.InSessionWith != "" {
			state += " session=" + c.InSessionWith
		}
		fmt.Fprintf(&b, "  computer %s %s %s\n", c.Name, c.Fingerprint, state)
	}
	for _, h := range p.Techies {
		state := "offline"
		if h.Online {
			state = "online"
		}
		fmt.Fprintf(&b, "  techie %s %s %s\n", h.Name, h.Fingerprint, state)
	}
	return b.String()
}

func session(w io.Writer, nch ssh.NewChannel, o options) {
	d, err := control.UnmarshalTechiesessionData(nch.ExtraData())
	if err != nil {
		nch.Reject(ssh.Prohibited, "bad session data")
		return
	}
	ch, reqs, err := nch.Accept()
	if err != nil {
		return
	}
	defer ch.Close()
	go ssh.DiscardRequests(reqs)
	fmt.Fprintf(w, "session: open request=%s computer=%s user_machine=%s key=%s\n", d.RequestID, d.ComputerName, d.UserMachine, d.ComputerKey)
	io.WriteString(ch, o.message+"\n")
	if o.stdin {
		go func() {
			io.Copy(ch, os.Stdin)
			ch.CloseWrite()
		}()
	}
	buf := make([]byte, 32<<10)
	for {
		n, err := ch.Read(buf)
		if n > 0 {
			fmt.Fprintf(w, "recv: %s\n", strings.TrimRight(string(buf[:n]), "\n"))
		}
		if err != nil {
			break
		}
	}
	fmt.Fprintln(w, "session: closed")
}
