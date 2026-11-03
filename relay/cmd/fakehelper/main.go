// Command fakehelper is a headless stand-in for the helper app, for manual
// end-to-end checks of the relay. It connects with the key in --key-dir
// (created if missing), prints Presence changes, can register a client key
// (--register), and can connect to the first online client (--connect-first).
// Session channels from the relay are accepted: it writes one line into
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

	"github.com/mevdschee/helpbuoy/proto/control"
	"github.com/mevdschee/helpbuoy/relay/fake"
)

type options struct {
	relay, keyDir, register, name, message string
	connectFirst, stdin, once              bool
}

func main() {
	var o options
	flag.StringVar(&o.relay, "relay", "localhost:5959", "relay host:port")
	flag.StringVar(&o.keyDir, "key-dir", "fakehelper-key", "directory with the helper key (created if missing)")
	flag.StringVar(&o.register, "register", "", "register this client key line ('ssh-ed25519 ... user@machine')")
	flag.StringVar(&o.name, "name", "", "client name for --register when the key has no comment")
	flag.BoolVar(&o.connectFirst, "connect-first", false, "connect to the first online client")
	flag.StringVar(&o.message, "message", "hello from fakehelper", "line written into each session channel")
	flag.BoolVar(&o.stdin, "stdin", false, "forward standard input into the session channel")
	flag.BoolVar(&o.once, "once", false, "exit after the first session ends")
	flag.Parse()
	if err := run(os.Stdout, o); err != nil {
		fmt.Fprintln(os.Stderr, "fakehelper:", err)
		os.Exit(1)
	}
}

func run(w io.Writer, o options) error {
	signer, err := fake.LoadOrCreateKey(o.keyDir, "fakehelper")
	if err != nil {
		return err
	}
	fmt.Fprintln(w, "key:", fake.KeyLine(signer.PublicKey(), "fakehelper"))
	fmt.Fprintln(w, "fingerprint:", ssh.FingerprintSHA256(signer.PublicKey()))
	c, err := fake.Dial(o.relay, signer, fake.Options{})
	if err != nil {
		return err
	}
	defer c.Close()
	fmt.Fprintln(w, "relay:", ssh.FingerprintSHA256(c.HostKey))

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
			fmt.Fprintf(w, "hello: role=%s name=%q relay=%s\n", m.Role, m.Name, m.RelayVersion)
			if m.Role != control.RoleHelper {
				return fmt.Errorf("this key is not a helper on the relay (role %s)", m.Role)
			}
			if o.register != "" {
				s := next()
				fmt.Fprintf(w, "register: seq=%d\n", s)
				if err := c.Send(&control.RegisterClient{Seq: s, PublicKey: o.register, Name: o.name}); err != nil {
					return err
				}
			}
		case *control.Presence:
			if s := formatPresence(m); s != last {
				last = s
				fmt.Fprint(w, s)
			}
			if o.connectFirst && !connected {
				for _, ci := range m.Clients {
					if ci.Online && ci.Request == nil {
						connected = true
						s, id := next(), control.NewRequestID()
						fmt.Fprintf(w, "connect: seq=%d request=%s client=%s\n", s, id, ci.Name)
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
			return fmt.Errorf("relay: %s", m.Message)
		default:
			fmt.Fprintf(w, "message: %T %+v\n", m, m)
		}
	}
}

func formatPresence(p *control.Presence) string {
	var b strings.Builder
	fmt.Fprintf(&b, "presence: %d clients, %d helpers\n", len(p.Clients), len(p.Helpers))
	for _, c := range p.Clients {
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
		fmt.Fprintf(&b, "  client %s %s %s\n", c.Name, c.Fingerprint, state)
	}
	for _, h := range p.Helpers {
		state := "offline"
		if h.Online {
			state = "online"
		}
		fmt.Fprintf(&b, "  helper %s %s %s\n", h.Name, h.Fingerprint, state)
	}
	return b.String()
}

func session(w io.Writer, nch ssh.NewChannel, o options) {
	d, err := control.UnmarshalHelperSessionData(nch.ExtraData())
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
	fmt.Fprintf(w, "session: open request=%s client=%s user_machine=%s key=%s\n", d.RequestID, d.ClientName, d.UserMachine, d.ClientKey)
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
