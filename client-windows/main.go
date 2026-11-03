//go:build windows

// Command helpbuoy is the Windows client: the person who needs help runs
// it, and allows a techie to take control when the techie connects.
//
// Hidden flags for testing: --demo uses a key that only lives in memory and
// a temporary settings folder, so the real key and settings are untouched,
// and streams the test pattern unless --source screen is given; --log FILE
// writes a debug log; --record DIR records the primary monitor for
// --record-for (see encode.Recorder) and exits without any window or
// network.
package main

import (
	"context"
	"flag"
	"io"
	"log/slog"
	"os"
	"runtime"
	"time"

	"github.com/rodrigocfd/windigo/co"
	"github.com/rodrigocfd/windigo/win"

	"github.com/mevdschee/helpbuoy/client-windows/app"
	"github.com/mevdschee/helpbuoy/client-windows/config"
	"github.com/mevdschee/helpbuoy/client-windows/identity"
	"github.com/mevdschee/helpbuoy/client-windows/injector"
	"github.com/mevdschee/helpbuoy/client-windows/session"
	"github.com/mevdschee/helpbuoy/client-windows/ui"
	"github.com/mevdschee/helpbuoy/client-windows/update"
	"github.com/mevdschee/helpbuoy/proto/appkey"
	pid "github.com/mevdschee/helpbuoy/proto/identity"
	"github.com/mevdschee/helpbuoy/proto/screen"
)

// version is set at build time with -ldflags "-X main.version=...". Release
// builds use "v<major.minor.build>", which turns on the update check; "dev"
// never checks.
var version = "dev"

// iconID is the RT_GROUP_ICON resource id from resources/winres.json.
const iconID = 101

func main() {
	runtime.LockOSThread() // the Win32 UI is single-threaded

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
		if err := record(*recDir, *recFor, *recFPS, log); err != nil {
			log.Error("recording failed", "err", err)
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
	hello := screen.Hello{
		Hostname:    hostname,
		UserMachine: userMachine,
		OS:          "windows",
		Width:       int(win.GetSystemMetrics(co.SM_CXSCREEN)),
		Height:      int(win.GetSystemMetrics(co.SM_CYSCREEN)),
	}

	w := ui.New(iconID)
	ctrl := app.New(app.Deps{
		UI:        w,
		Store:     store,
		LoadKey:   loadKey,
		RemoveKey: removeKey,
		NewRunner: func(k *appkey.Key) session.Runner {
			if *source == "pattern" {
				return &session.PatternRunner{
					HostKey: k.Signer,
					Sink:    &injector.Counter{Logger: log},
					Hello:   hello,
					Label:   userMachine,
					Logger:  log,
				}
			}
			return newScreenRunner(k, store, hello, log)
		},
		EscHook: func(onEsc func()) func() {
			stop, err := injector.InstallEscHook(onEsc)
			if err != nil {
				log.Error("Esc hook not installed", "err", err)
				return func() {}
			}
			return stop
		},
		CheckUpdate: func(ctx context.Context) (*update.Release, error) {
			return update.Check(ctx, update.LatestReleaseURL, version)
		},
		AppVersion:  version,
		UserMachine: userMachine,
		Logger:      log,
	})
	w.Run(ctrl)
	ctrl.WaitClosed(time.Second)
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
