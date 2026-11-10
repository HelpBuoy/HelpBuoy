// Command helpbuoy-desk is the desk app: it lists connected computers
// and opens remote sessions for their help requests.
//
// It uses raylib through raylib-go built with CGO_ENABLED=0 (purego), so it
// cross-compiles for every target with plain "go build".
//
// Flags for trying the UI without a server:
//
//	--demo           fake computers and requests, no network, nothing stored
//	--page NAME      start on "register", "server" or "session" (demo only)
//	--datadir DIR    use DIR instead of the per-user data directory
//	--screenshot F   save a frame as PNG to F, then exit (with --demo: screenshot.sh)
//	--size WxH       window size, for --screenshot also below the minimum size
package main

import (
	"context"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime"
	"slices"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/helpbuoy/helpbuoy/desk/identity"
	"github.com/helpbuoy/helpbuoy/desk/perf"
	"github.com/helpbuoy/helpbuoy/desk/views"
	"github.com/helpbuoy/helpbuoy/proto/instance"
	"github.com/helpbuoy/helpbuoy/proto/update"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

//go:embed resources/icon.png
var iconPNG []byte

func init() {
	runtime.LockOSThread() // raylib must stay on the main OS thread
}

func main() {
	demo := flag.Bool("demo", os.Getenv("SV_TECHIE_DEMO") != "", "show fake computers; no network, nothing stored")
	page := flag.String("page", os.Getenv("SV_TECHIE_PAGE"), `start page: "register", "server" or "session" (demo)`)
	dataDir := flag.String("datadir", "", "data directory (default: per-user data directory)")
	screenshot := flag.String("screenshot", "", "save a frame as PNG to this file, then exit")
	size := flag.String("size", "1100x700", "window size WxH")
	// --upload-bench is a hidden developer check (desk/perf): it measures
	// texture uploads in a window, prints a table and exits.
	uploadBench := slices.Contains(os.Args[1:], "--upload-bench")
	if uploadBench {
		os.Args = slices.DeleteFunc(os.Args, func(a string) bool { return a == "--upload-bench" })
	}
	flag.Parse()
	var w, h int32
	if _, err := fmt.Sscanf(*size, "%dx%d", &w, &h); err != nil || w <= 0 || h <= 0 {
		fmt.Fprintln(os.Stderr, "--size wants WxH, for example 1100x700")
		os.Exit(2)
	}

	rl.SetTraceLogLevel(rl.LogWarning)
	// One desk app per data directory (so per user): two would share the
	// key and keep pushing each other off the server. Demo mode, screenshots
	// and the bench store nothing and never connect.
	if !*demo && *screenshot == "" && !uploadBench {
		dir := *dataDir
		if dir == "" {
			dir, _ = identity.Dir() // on error the app shows it later
		}
		if dir != "" {
			lock, err := instance.Acquire(dir, "desk.lock")
			switch {
			case errors.Is(err, instance.ErrRunning):
				rl.SetConfigFlags(rl.FlagWindowHighdpi | rl.FlagMsaa4xHint)
				rl.InitWindow(views.RunningWidth, views.RunningHeight, "HelpBuoy Desk")
				if img := rl.LoadImageFromMemory(".png", iconPNG, int32(len(iconPNG))); img != nil && img.Width > 0 {
					rl.SetWindowIcon(*img)
					rl.UnloadImage(img)
				}
				views.AlreadyRunning()
				rl.CloseWindow()
				return
			case err != nil:
				fmt.Fprintln(os.Stderr, "lock:", err) // go on without the lock
			default:
				defer lock.Unlock()
			}
		}
	}
	rl.SetConfigFlags(rl.FlagWindowResizable | rl.FlagWindowHighdpi | rl.FlagMsaa4xHint)
	// Like most apps, the title shows the version: "HelpBuoy Desk v0.4.19".
	title := "HelpBuoy Desk"
	if update.IsRelease(version) {
		title += " " + version
	}
	rl.InitWindow(w, h, title)
	defer rl.CloseWindow()
	if *screenshot == "" {
		rl.SetWindowMinSize(views.MinWidth, views.MinHeight)
	}
	rl.SetExitKey(rl.KeyNull) // Esc belongs to the remote session
	if img := rl.LoadImageFromMemory(".png", iconPNG, int32(len(iconPNG))); img != nil && img.Width > 0 {
		rl.SetWindowIcon(*img)
		rl.UnloadImage(img)
	}
	if uploadBench {
		perf.Run(os.Stdout)
		return
	}

	opts := views.Options{DataDir: *dataDir, Version: version, Demo: *demo, Page: *page, Icon: iconPNG, Screenshot: *screenshot}
	if runtime.GOOS == "windows" {
		// Only Windows updates itself (MSI); the Debian package is updated
		// with the system's packages.
		opts.CheckUpdate = func(ctx context.Context) (*update.Release, error) {
			return update.Check(ctx, update.LatestReleaseURL, version, update.DeskMSI)
		}
	}
	app := views.New(opts)
	for !rl.WindowShouldClose() && !app.Quit() {
		app.Frame()
	}
	app.Shutdown()
	if app.ScreenshotFailed() {
		rl.CloseWindow()
		os.Exit(1)
	}
}
