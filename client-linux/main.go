// Command helpbuoy is the Linux client: the person who needs help runs it,
// and allows a techie to take control when the techie connects. It talks
// the X11 protocol directly (jezek/xgb) and is built without cgo, so one
// static binary runs on any Linux desktop with an X server, or XWayland
// for its own window. Wayland desktops can't be shared: there the window
// only explains how to log in with an X11 session.
//
// Hidden flags for testing: --demo uses a key that only lives in memory and
// a temporary settings folder, so the real key and settings are untouched,
// and streams the test pattern unless --source screen is given; --log FILE
// writes a debug log; --record DIR records the primary monitor for
// --record-for (see encode.Recorder) and exits without any window or
// network.
package main

import (
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"

	"github.com/mevdschee/helpbuoy/client-linux/capture"
	"github.com/mevdschee/helpbuoy/client-linux/injector"
	"github.com/mevdschee/helpbuoy/client-linux/ui"
	"github.com/mevdschee/helpbuoy/client-windows/app"
	wcapture "github.com/mevdschee/helpbuoy/client-windows/capture"
	"github.com/mevdschee/helpbuoy/client-windows/config"
	"github.com/mevdschee/helpbuoy/client-windows/identity"
	winjector "github.com/mevdschee/helpbuoy/client-windows/injector"
	"github.com/mevdschee/helpbuoy/client-windows/session"
	"github.com/mevdschee/helpbuoy/proto/appkey"
	"github.com/mevdschee/helpbuoy/proto/encode"
	"github.com/mevdschee/helpbuoy/proto/i18n"
	pid "github.com/mevdschee/helpbuoy/proto/identity"
	"github.com/mevdschee/helpbuoy/proto/screen"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

var (
	//go:embed resources/icon48.png
	icon48 []byte
	//go:embed resources/icon32.png
	icon32 []byte
	//go:embed resources/icon16.png
	icon16 []byte
)

func main() {
	xgb.Logger.SetOutput(io.Discard) // it logs every connection without an Xauthority file
	demo := flag.Bool("demo", false, "use an in-memory key and temporary settings")
	logPath := flag.String("log", "", "write a debug log to this file")
	source := flag.String("source", "", "what sessions show: screen or pattern (default pattern with --demo, else screen)")
	recDir := flag.String("record", "", "record the primary monitor into this new directory and exit")
	recFor := flag.Duration("record-for", 10*time.Second, "how long --record records")
	recFPS := flag.Int("record-fps", 30, "captures per second for --record")
	flag.Parse()

	log := newLogger(*logPath)
	slog.SetDefault(log)
	if *recDir != "" {
		if wayland() {
			fmt.Fprintln(os.Stderr, "helpbuoy: a Wayland desktop can't be recorded; log in with an X11 session")
			os.Exit(1)
		}
		if err := record(*recDir, *recFor, *recFPS, log); err != nil {
			fmt.Fprintln(os.Stderr, "helpbuoy:", err)
			os.Exit(1)
		}
		return
	}
	if *source == "" {
		*source = "screen"
		if *demo {
			*source = "pattern"
		}
	}
	userMachine := pid.UserMachine()

	var (
		store     config.Store
		loadKey   func() (*appkey.Key, error)
		removeKey func() error
	)
	if *demo {
		dir, err := os.MkdirTemp("", "helpbuoy-demo-")
		if err == nil {
			defer os.RemoveAll(dir)
		}
		store = config.Store{Dir: dir}
		loadKey = func() (*appkey.Key, error) {
			k, err := identity.Ephemeral(userMachine)
			if k != nil {
				k.Dir = dir
			}
			return k, err
		}
		removeKey = store.Reset
	} else {
		dir, dirErr := identity.Dir()
		store = config.Store{Dir: dir}
		loadKey = func() (*appkey.Key, error) {
			if dirErr != nil {
				return nil, dirErr
			}
			k, created, err := identity.LoadOrCreate(dir)
			if created {
				log.Info("created a new app key", "dir", dir)
			}
			return k, err
		}
		removeKey = store.RemoveKey
	}

	hostname, _ := os.Hostname()
	hello := screen.Hello{Hostname: hostname, UserMachine: userMachine, OS: "linux"}

	w := ui.New([][]byte{icon48, icon32, icon16})
	if *source == "screen" && wayland() {
		// Through XWayland the screen reads black except for X11 windows,
		// and input only reaches X11 windows: refuse instead of showing
		// the techie a black screen.
		log.Warn("Wayland session, sharing is not supported")
		w.Unsupported = i18n.Trans("This computer uses a Wayland desktop. HelpBuoy can only share " +
			"and control X11 desktops for now.\n\n" +
			"To get help, log out, choose an X11 session on the login screen " +
			"(on GNOME: the gear button, \"GNOME on Xorg\"; on KDE: \"Plasma (X11)\"), " +
			"log in and start HelpBuoy again.")
	}
	ctrl := app.New(app.Deps{
		UI:        w,
		Store:     store,
		LoadKey:   loadKey,
		RemoveKey: removeKey,
		NewRunner: func(k *appkey.Key) session.Runner {
			if *source == "pattern" {
				// Read while the window's connection is open: an X server
				// without clients resets, which a short connection before
				// the window could run into.
				hello.Width, hello.Height = screenSize()
				return &session.PatternRunner{
					HostKey: k.Signer,
					Sink:    &winjector.Counter{Logger: log},
					Hello:   hello,
					Label:   userMachine,
					Logger:  log,
				}
			}
			return newScreenRunner(k, hello, log)
		},
		EscHook: func(onEsc func()) func() {
			stop, err := injector.InstallEscHook(onEsc)
			if err != nil {
				log.Error("Esc hook not installed", "err", err)
				return func() {}
			}
			return stop
		},
		// Releases are published as a Windows MSI only, so there is no
		// update to offer here yet.
		CheckUpdate: nil,
		AppVersion:  version,
		UserMachine: userMachine,
		Logger:      log,
	})
	if err := w.Run(ctrl); err != nil {
		fmt.Fprintln(os.Stderr, "helpbuoy:", err)
		os.Exit(1)
	}
	ctrl.WaitClosed(time.Second)
}

// wayland reports whether the desktop session is Wayland. XDG_SESSION_TYPE
// is what the login manager started; without it a Wayland socket is the
// sign.
func wayland() bool {
	if t := os.Getenv("XDG_SESSION_TYPE"); t != "" {
		return t == "wayland"
	}
	return os.Getenv("WAYLAND_DISPLAY") != ""
}

// screenSize returns the size of the X screen, or 0x0 without a display.
func screenSize() (int, int) {
	c, err := xgb.NewConn()
	if err != nil {
		return 0, 0
	}
	defer c.Close()
	s := xproto.Setup(c).DefaultScreen(c)
	return int(s.WidthInPixels), int(s.HeightInPixels)
}

// newScreenRunner returns the runner of real sessions: X capture, the
// encoder and the XTEST injector.
func newScreenRunner(k *appkey.Key, hello screen.Hello, log *slog.Logger) session.Runner {
	return &session.ScreenRunner{
		HostKey:   k.Signer,
		NewSource: func() (wcapture.Screen, error) { return capture.New(log) },
		NewEncoder: func() session.FrameEncoder {
			return encode.New(encode.Config{SwapRB: true}) // captures are BGRX
		},
		NewInjector: func(log *slog.Logger, onInjected func()) session.Injector {
			return injector.New(log, onInjected)
		},
		Hello:  screen.Hello{Hostname: hello.Hostname, UserMachine: hello.UserMachine, OS: hello.OS},
		Logger: log,
	}
}

// record captures the primary monitor at fps for d into a new recording in
// dir, for the replay benchmark.
func record(dir string, d time.Duration, fps int, log *slog.Logger) error {
	if fps <= 0 || fps > 60 {
		return errors.New("--record-fps must be 1..60")
	}
	scr, err := capture.New(log)
	if err != nil {
		return err
	}
	defer scr.Close()
	ms, err := scr.Monitors()
	if err != nil {
		return err
	}
	m := ms[wcapture.DefaultMonitor(ms, nil)]
	c, err := scr.Open(m)
	if err != nil {
		return err
	}
	defer c.Close()
	rec, err := encode.NewRecorder(dir, m.Rect.Dx(), m.Rect.Dy(), true)
	if err != nil {
		return err
	}
	log.Info("recording", "dir", dir, "monitor", m.Name, "rect", m.Rect, "for", d, "fps", fps)
	tick := time.NewTicker(time.Second / time.Duration(fps))
	defer tick.Stop()
	start := time.Now()
	frames := 0
	for now := start; now.Sub(start) < d; now = <-tick.C {
		img, err := c.Capture()
		if err != nil {
			log.Warn("capture failed", "err", err)
			continue
		}
		if err := rec.Add(img, now); err != nil {
			rec.Close()
			return err
		}
		frames++
	}
	if err := rec.Close(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "recorded %d frames of %v into %s\n", frames, m.Rect.Size(), dir)
	return nil
}

func newLogger(path string) *slog.Logger {
	var out io.Writer = io.Discard
	level := slog.LevelInfo
	if path != "" {
		if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
			out = f
			level = slog.LevelDebug
		}
	}
	return slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{Level: level}))
}
