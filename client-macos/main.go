//go:build darwin

// Command helpbuoy is the macOS client: the person who needs help runs it,
// and allows a techie to take control when the techie connects. It captures
// the screen with ScreenCaptureKit at the displays' size in points and
// injects input with CGEvent. Before it starts it asks for the Screen
// Recording and Accessibility permissions, since macOS blocks both until
// the user allows them in System Settings.
//
// Hidden flags for testing: --demo uses a key that only lives in memory and
// a temporary settings folder, so the real key and settings are untouched,
// and streams the test pattern unless --source screen is given; --log FILE
// writes a debug log; --capture sck or cg picks ScreenCaptureKit or
// CoreGraphics capture (by default CoreGraphics only on Macs without Metal,
// such as virtual machines, where a ScreenCaptureKit stream crashes
// WindowServer).
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

	"github.com/helpbuoy/helpbuoy/client-linux/ui"
	"github.com/helpbuoy/helpbuoy/client-macos/capture"
	"github.com/helpbuoy/helpbuoy/client-macos/injector"
	"github.com/helpbuoy/helpbuoy/client-macos/macwin"
	"github.com/helpbuoy/helpbuoy/client-macos/perm"
	"github.com/helpbuoy/helpbuoy/client-windows/app"
	wcapture "github.com/helpbuoy/helpbuoy/client-windows/capture"
	"github.com/helpbuoy/helpbuoy/client-windows/config"
	"github.com/helpbuoy/helpbuoy/client-windows/identity"
	winjector "github.com/helpbuoy/helpbuoy/client-windows/injector"
	"github.com/helpbuoy/helpbuoy/client-windows/session"
	"github.com/helpbuoy/helpbuoy/proto/appkey"
	"github.com/helpbuoy/helpbuoy/proto/encode"
	"github.com/helpbuoy/helpbuoy/proto/i18n"
	pid "github.com/helpbuoy/helpbuoy/proto/identity"
	"github.com/helpbuoy/helpbuoy/proto/instance"
	"github.com/helpbuoy/helpbuoy/proto/screen"
)

// lockName is the lock file in the data directory (see proto/instance).
const lockName = "helpbuoy.lock"

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

//go:embed resources/icon.png
var icon []byte

func main() {
	demo := flag.Bool("demo", false, "use an in-memory key and temporary settings")
	logPath := flag.String("log", "", "write a debug log to this file")
	source := flag.String("source", "", "what sessions show: screen or pattern (default pattern with --demo, else screen)")
	backend := flag.String("capture", "", "how the screen is captured: sck (ScreenCaptureKit) or cg (CoreGraphics); default sck, or cg without Metal")
	flag.Parse()

	log := newLogger(*logPath)
	slog.SetDefault(log)
	if *backend != "" && *backend != "sck" && *backend != "cg" {
		fmt.Fprintln(os.Stderr, "--capture must be sck or cg")
		os.Exit(2)
	}
	capture.Backend = *backend
	if *source == "" {
		*source = "screen"
		if *demo {
			*source = "pattern"
		}
	}
	macwin.Init(i18n.Trans("Hide HelpBuoy"), i18n.Trans("Quit HelpBuoy"), i18n.Trans("Minimize"), icon)
	go func() {
		if err := run(*demo, *source, log); err != nil {
			fmt.Fprintln(os.Stderr, "helpbuoy:", err)
			log.Error("stopped", "err", err)
		}
		macwin.Quit()
	}()
	macwin.Main() // AppKit on the main thread, until Quit
}

// run is the program after the AppKit setup; it returns when the window
// closed.
func run(demo bool, source string, log *slog.Logger) error {
	userMachine := pid.UserMachine()
	var (
		store     config.Store
		loadKey   func() (*appkey.Key, error)
		removeKey func() error
	)
	if demo {
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
		if dirErr == nil {
			// One copy per user: two would share the key and keep pushing
			// each other off the server. A second start shows the first.
			lock, err := instance.Acquire(dir, lockName)
			switch {
			case errors.Is(err, instance.ErrRunning):
				owner := instance.Owner(dir, lockName)
				log.Info("already running", "pid", owner)
				if err := macwin.ShowRunning(owner); err != nil {
					log.Warn("running copy not shown", "err", err)
				}
				return nil
			case err != nil:
				log.Warn("no instance lock", "err", err) // go on without it
			default:
				defer lock.Unlock()
			}
		}
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
	hello := screen.Hello{Hostname: hostname, UserMachine: userMachine, OS: "macos"}

	w := ui.NewWithHost(&macwin.Host{}, nil)
	if source == "screen" {
		w.Permissions = []ui.Permission{{
			Name:    i18n.Trans("Screen Recording"),
			Text:    i18n.Trans("So the techie can see your screen."),
			Granted: perm.ScreenRecording,
			Open:    perm.OpenScreenRecording,
		}, {
			Name:    i18n.Trans("Accessibility"),
			Text:    i18n.Trans("So the techie can use your mouse and keyboard."),
			Granted: perm.Accessibility,
			Open:    perm.OpenAccessibility,
		}}
		w.PermissionsNote = i18n.Trans("Turn on HelpBuoy in each list. macOS then asks to quit and reopen HelpBuoy: click Quit & Reopen.")
	}
	ctrl := app.New(app.Deps{
		UI:        w,
		Store:     store,
		LoadKey:   loadKey,
		RemoveKey: removeKey,
		NewRunner: func(k *appkey.Key) session.Runner {
			if source == "pattern" {
				ms, _ := mustScreen(log).Monitors()
				if len(ms) > 0 {
					m := ms[wcapture.DefaultMonitor(ms, nil)]
					hello.Width, hello.Height = m.Rect.Dx(), m.Rect.Dy()
				}
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
		CheckUpdate: nil,
		Verify:      app.NewVerifier(version),
		AppVersion:  version,
		UserMachine: userMachine,
		Logger:      log,
	})
	if err := w.Run(ctrl); err != nil {
		return err
	}
	ctrl.WaitClosed(time.Second)
	return nil
}

func mustScreen(log *slog.Logger) *capture.Screen {
	s, _ := capture.New(log) // never fails
	return s
}

// newScreenRunner returns the runner of real sessions: ScreenCaptureKit,
// the encoder and the CGEvent injector.
func newScreenRunner(k *appkey.Key, hello screen.Hello, log *slog.Logger) session.Runner {
	return &session.ScreenRunner{
		HostKey:   k.Signer,
		NewSource: func() (wcapture.Screen, error) { return capture.New(log) },
		NewEncoder: func() session.FrameEncoder {
			return encode.New(encode.Config{SwapRB: true}) // captures are BGRA
		},
		NewInjector: func(log *slog.Logger, onInjected func()) session.Injector {
			return injector.New(log, onInjected)
		},
		Hello:  screen.Hello{Hostname: hello.Hostname, UserMachine: hello.UserMachine, OS: hello.OS},
		Logger: log,
	}
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
