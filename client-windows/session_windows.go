//go:build windows

package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/mevdschee/help-me-win/client-windows/capture"
	"github.com/mevdschee/help-me-win/client-windows/config"
	"github.com/mevdschee/help-me-win/client-windows/injector"
	"github.com/mevdschee/help-me-win/client-windows/perfmode"
	"github.com/mevdschee/help-me-win/client-windows/session"
	"github.com/mevdschee/help-me-win/proto/appkey"
	"github.com/mevdschee/help-me-win/proto/encode"
	"github.com/mevdschee/help-me-win/proto/screen"
)

// newScreenRunner returns the runner of real sessions: GDI capture, the
// encoder and the SendInput injector.
func newScreenRunner(k *appkey.Key, store config.Store, hello screen.Hello, log *slog.Logger) session.Runner {
	return &session.ScreenRunner{
		HostKey:   k.Signer,
		NewSource: func() (capture.Screen, error) { return capture.New(capture.Options{}) },
		NewEncoder: func() session.FrameEncoder {
			return encode.New(encode.Config{SwapRB: true})
		},
		NewInjector: func(log *slog.Logger, onInjected func()) session.Injector {
			return injector.New(log, onInjected)
		},
		Hello: screen.Hello{Hostname: hello.Hostname, UserMachine: hello.UserMachine, OS: hello.OS},
		PerformanceMode: func() bool {
			c, err := store.Load()
			return err == nil && c.PerformanceMode
		},
		EnablePerformance: perfmode.Enable,
		Logger:            log,
	}
}

// record captures the primary monitor at fps for d into a new recording in
// dir, for the replay benchmark.
func record(dir string, d time.Duration, fps int, log *slog.Logger) error {
	if fps <= 0 || fps > 60 {
		return errors.New("--record-fps must be 1..60")
	}
	scr, err := capture.New(capture.Options{})
	if err != nil {
		return err
	}
	defer scr.Close()
	ms, err := scr.Monitors()
	if err != nil {
		return err
	}
	m := ms[capture.DefaultMonitor(ms, nil)]
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
	log.Info("recording done", "frames", frames)
	fmt.Fprintf(os.Stderr, "recorded %d frames of %v into %s\n", frames, m.Rect.Size(), dir)
	return nil
}
