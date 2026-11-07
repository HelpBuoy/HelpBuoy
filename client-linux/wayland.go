package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/helpbuoy/helpbuoy/client-linux/injector"
	"github.com/helpbuoy/helpbuoy/client-linux/portal"
	"github.com/helpbuoy/helpbuoy/client-linux/pwcapture"
	wcapture "github.com/helpbuoy/helpbuoy/client-windows/capture"
	"github.com/helpbuoy/helpbuoy/client-windows/session"
	"github.com/helpbuoy/helpbuoy/proto/appkey"
	"github.com/helpbuoy/helpbuoy/proto/encode"
	"github.com/helpbuoy/helpbuoy/proto/i18n"
	"github.com/helpbuoy/helpbuoy/proto/screen"
)

// restoreTokenFile keeps the portal's restore token in the data directory,
// so the compositor can skip asking which monitor to share next time.
const restoreTokenFile = "wayland-restore-token"

// portalAnswer is how long the user has to answer the compositor's sharing
// dialog.
const portalAnswer = 2 * time.Minute

// wayland reports whether the desktop session is Wayland. XDG_SESSION_TYPE
// is what the login manager started; without it a Wayland socket is the
// sign.
func wayland() bool {
	if t := os.Getenv("XDG_SESSION_TYPE"); t != "" {
		return t == "wayland"
	}
	return os.Getenv("WAYLAND_DISPLAY") != ""
}

// waylandProblem returns why this Wayland desktop can't be shared, for the
// window, or "" when it can: sharing needs the remote desktop portal and
// GStreamer's PipeWire source.
func waylandProblem(log *slog.Logger) string {
	if v, err := portal.Version(); err != nil {
		log.Warn("no remote desktop portal", "err", err)
		return i18n.Trans("This Wayland desktop can't be shared: it offers no remote desktop portal. " +
			"HelpBuoy can share the Wayland desktops of GNOME and KDE Plasma, and any X11 desktop.\n\n" +
			"To get help, log out, choose an X11 session on the login screen " +
			"(on GNOME: the gear button, \"GNOME on Xorg\"; on KDE: \"Plasma (X11)\"), " +
			"log in and start HelpBuoy again.")
	} else {
		log.Info("remote desktop portal", "version", v)
	}
	if err := pwcapture.Check(); err != nil {
		log.Warn("no GStreamer PipeWire source", "err", err)
		return i18n.Trans("To share this Wayland desktop HelpBuoy needs GStreamer with PipeWire support.\n\n" +
			"Install the packages gstreamer1.0-pipewire and gstreamer1.0-tools " +
			"(on Fedora: pipewire-gstreamer and gstreamer1), then start HelpBuoy again.")
	}
	return ""
}

// openWayland asks the compositor to share a monitor and to take input,
// which shows its dialog, and returns the shared screen.
func openWayland(dir string, log *slog.Logger) (*pwcapture.Screen, error) {
	tokenPath := ""
	var opt portal.Options
	if dir != "" {
		tokenPath = filepath.Join(dir, restoreTokenFile)
		if b, err := os.ReadFile(tokenPath); err == nil {
			opt.RestoreToken = strings.TrimSpace(string(b))
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), portalAnswer)
	defer cancel()
	sess, err := portal.Start(ctx, opt)
	if err != nil {
		if errors.Is(err, portal.ErrCancelled) && tokenPath != "" {
			os.Remove(tokenPath) // ask afresh next time
		}
		return nil, err
	}
	log.Info("portal session started", "streams", sess.Streams, "restored", opt.RestoreToken != "")
	if tokenPath != "" && sess.RestoreToken != "" && sess.RestoreToken != opt.RestoreToken {
		if err := os.WriteFile(tokenPath, []byte(sess.RestoreToken+"\n"), 0o600); err != nil {
			log.Warn("restore token not saved", "err", err)
		}
	}
	scr, err := pwcapture.New(sess, log)
	if err != nil {
		sess.Close()
		return nil, err
	}
	return scr, nil
}

// newWaylandRunner returns the runner of real sessions on Wayland: the
// portal's PipeWire stream through GStreamer, the encoder, and input through
// the same portal session.
func newWaylandRunner(k *appkey.Key, hello screen.Hello, dir string, log *slog.Logger) session.Runner {
	// The session opens the source before the injector, and closes the
	// injector first: the injector borrows the source's portal session.
	var (
		mu  sync.Mutex
		cur *pwcapture.Screen
	)
	return &session.ScreenRunner{
		HostKey: k.Signer,
		NewSource: func() (wcapture.Screen, error) {
			scr, err := openWayland(dir, log)
			if err != nil {
				return nil, err
			}
			mu.Lock()
			cur = scr
			mu.Unlock()
			return scr, nil
		},
		NewEncoder: func() session.FrameEncoder {
			return encode.New(encode.Config{SwapRB: true}) // captures are BGRX
		},
		NewInjector: func(log *slog.Logger, onInjected func()) session.Injector {
			mu.Lock()
			scr := cur
			mu.Unlock()
			st := scr.Stream()
			return injector.NewPortal(log, onInjected, scr.Session(), st.Node, st.Rect.Size())
		},
		Hello:  screen.Hello{Hostname: hello.Hostname, UserMachine: hello.UserMachine, OS: hello.OS},
		Logger: log,
	}
}
