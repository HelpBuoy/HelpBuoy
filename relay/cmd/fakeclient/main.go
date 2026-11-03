// Command fakeclient is a headless stand-in for the Windows client, for
// manual end-to-end checks of the relay. It connects with the key in
// --key-dir (created if missing), prints its public key line, waits for
// Registered, optionally files a help request, and on SessionReady opens the
// session channel, writes one line into it and echoes everything it
// receives back.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/mevdschee/help-me-win/proto/control"
	"github.com/mevdschee/help-me-win/relay/fake"
)

func main() {
	relay := flag.String("relay", "localhost:5938", "relay host:port")
	keyDir := flag.String("key-dir", "fakeclient-key", "directory with the client key (created if missing)")
	userMachine := flag.String("user-machine", "fake@fakeclient", "user@machine for the key comment and ClientHello")
	request := flag.Bool("request", false, "file a help request once registered")
	message := flag.String("message", "hello from fakeclient", "line written into the session channel")
	once := flag.Bool("once", false, "exit after the first session ends")
	flag.Parse()
	if err := run(os.Stdout, *relay, *keyDir, *userMachine, *request, *message, *once); err != nil {
		fmt.Fprintln(os.Stderr, "fakeclient:", err)
		os.Exit(1)
	}
}

func run(w io.Writer, relay, keyDir, userMachine string, request bool, message string, once bool) error {
	signer, err := fake.LoadOrCreateKey(keyDir, userMachine)
	if err != nil {
		return err
	}
	fmt.Fprintln(w, "key:", fake.KeyLine(signer.PublicKey(), userMachine))
	fmt.Fprintln(w, "fingerprint:", ssh.FingerprintSHA256(signer.PublicKey()))
	c, err := fake.Dial(relay, signer, fake.Options{})
	if err != nil {
		return err
	}
	defer c.Close()
	fmt.Fprintln(w, "relay:", ssh.FingerprintSHA256(c.HostKey))
	if err := c.Send(&control.ClientHello{AppVersion: "fakeclient", UserMachine: userMachine}); err != nil {
		return err
	}
	requested := false
	ask := func() error {
		if !request || requested {
			return nil
		}
		requested = true
		return c.Send(&control.RequestHelp{})
	}
	for m := range c.Msgs {
		switch m := m.(type) {
		case *control.Hello:
			fmt.Fprintf(w, "hello: role=%s name=%q relay=%s\n", m.Role, m.Name, m.RelayVersion)
			if m.Role == control.RoleClient {
				if err := ask(); err != nil {
					return err
				}
			}
		case *control.Registered:
			fmt.Fprintf(w, "registered: %s\n", m.Name)
			if err := ask(); err != nil {
				return err
			}
		case *control.RequestStateMsg:
			fmt.Fprintf(w, "request: %s %s\n", m.RequestID, m.State)
		case *control.SessionReady:
			fmt.Fprintf(w, "session-ready: %s helper=%s (%s) key=%s\n", m.RequestID, m.HelperName, m.HelperDisplayName, m.HelperKey)
			if err := session(w, c, m, message); err != nil {
				fmt.Fprintln(w, "session error:", err)
			}
			fmt.Fprintln(w, "session: closed")
			if once {
				return nil
			}
		case *control.Error:
			return fmt.Errorf("relay: %s", m.Message)
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
