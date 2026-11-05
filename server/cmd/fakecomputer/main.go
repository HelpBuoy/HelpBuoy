// Command fakecomputer is a headless stand-in for the Windows client, for
// manual end-to-end checks of the server. It connects with the key in
// --key-dir (created if missing), prints its public key line, waits for
// Registered, and on SessionReady (a techie connecting) opens the session
// channel, writes one line into it and echoes everything it receives back.
// With --refuse it refuses every techie instead.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/helpbuoy/helpbuoy/proto/control"
	"github.com/helpbuoy/helpbuoy/server/fake"
)

func main() {
	server := flag.String("server", "localhost:5959", "server host:port")
	keyDir := flag.String("key-dir", "fakecomputer-key", "directory with the computer key (created if missing)")
	userMachine := flag.String("user-machine", "fake@fakecomputer", "user@machine for the key comment and ComputerHello")
	refuse := flag.Bool("refuse", false, "refuse every techie that connects")
	message := flag.String("message", "hello from fakecomputer", "line written into the session channel")
	once := flag.Bool("once", false, "exit after the first session ends")
	flag.Parse()
	if err := run(os.Stdout, *server, *keyDir, *userMachine, *refuse, *message, *once); err != nil {
		fmt.Fprintln(os.Stderr, "fakecomputer:", err)
		os.Exit(1)
	}
}

func run(w io.Writer, server, keyDir, userMachine string, refuse bool, message string, once bool) error {
	signer, err := fake.LoadOrCreateKey(keyDir, userMachine)
	if err != nil {
		return err
	}
	fmt.Fprintln(w, "key:", fake.KeyLine(signer.PublicKey(), userMachine))
	fmt.Fprintln(w, "fingerprint:", ssh.FingerprintSHA256(signer.PublicKey()))
	c, err := fake.Dial(server, signer, fake.Options{})
	if err != nil {
		return err
	}
	defer c.Close()
	fmt.Fprintln(w, "server:", ssh.FingerprintSHA256(c.HostKey))
	if err := c.Send(&control.ComputerHello{AppVersion: "fakecomputer", UserMachine: userMachine}); err != nil {
		return err
	}
	for m := range c.Msgs {
		switch m := m.(type) {
		case *control.Hello:
			fmt.Fprintf(w, "hello: role=%s name=%q server=%s\n", m.Role, m.Name, m.ServerVersion)
		case *control.Registered:
			fmt.Fprintln(w, "registered")
		case *control.RequestStateMsg:
			fmt.Fprintf(w, "request: %s %s\n", m.RequestID, m.State)
		case *control.SessionReady:
			fmt.Fprintf(w, "session-ready: %s techie=%s key=%s\n", m.RequestID, m.TechieName, m.TechieKey)
			if refuse {
				fmt.Fprintln(w, "session: refused")
				if err := c.Send(&control.CancelRequest{RequestID: m.RequestID}); err != nil {
					return err
				}
				continue
			}
			if err := session(w, c, m, message); err != nil {
				fmt.Fprintln(w, "session error:", err)
			}
			fmt.Fprintln(w, "session: closed")
			if once {
				return nil
			}
		case *control.Error:
			return fmt.Errorf("server: %s", m.Message)
		default:
			fmt.Fprintf(w, "message: %T %+v\n", m, m)
		}
	}
	return fmt.Errorf("connection closed")
}

func session(w io.Writer, c *fake.Conn, m *control.SessionReady, message string) error {
	ch, reqs, err := c.Client.OpenChannel(control.ChannelSession,
		control.MarshalSessionChannelData(control.SessionChannelData{RequestID: m.RequestID}))
	if err != nil {
		return err
	}
	defer ch.Close()
	go ssh.DiscardRequests(reqs)
	fmt.Fprintln(w, "session: open")
	if _, err := io.WriteString(ch, message+"\n"); err != nil {
		return err
	}
	buf := make([]byte, 32<<10)
	for {
		n, err := ch.Read(buf)
		if n > 0 {
			fmt.Fprintf(w, "recv: %s\n", strings.TrimRight(string(buf[:n]), "\n"))
			if _, werr := ch.Write(buf[:n]); werr != nil {
				return werr
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
