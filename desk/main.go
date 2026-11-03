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
package main

import (
	_ "embed"
	"flag"
	"os"
	"runtime"
	"slices"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/mevdschee/helpbuoy/desk/perf"
	"github.com/mevdschee/helpbuoy/desk/views"
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
	// --upload-bench is a hidden developer check (desk/perf): it measures
	// texture uploads in a window, prints a table and exits.
	uploadBench := slices.Contains(os.Args[1:], "--upload-bench")
	if uploadBench {
		os.Args = slices.DeleteFunc(os.Args, func(a string) bool { return a == "--upload-bench" })
	}
	flag.Parse()

	rl.SetTraceLogLevel(rl.LogWarning)
	rl.SetConfigFlags(rl.FlagWindowResizable | rl.FlagWindowHighdpi | rl.FlagMsaa4xHint)
	rl.InitWindow(1100, 700, "HelpBuoy Desk")
	defer rl.CloseWindow()
	rl.SetWindowMinSize(views.MinWidth, views.MinHeight)
	rl.SetExitKey(rl.KeyNull) // Esc belongs to the remote session
	if img := rl.LoadImageFromMemory(".png", iconPNG, int32(len(iconPNG))); img != nil && img.Width > 0 {
		rl.SetWindowIcon(*img)
		rl.UnloadImage(img)
	}
	if uploadBench {
		perf.Run(os.Stdout)
		return
	}

	app := views.New(views.Options{DataDir: *dataDir, Version: version, Demo: *demo, Page: *page, Icon: iconPNG})
	defer app.Shutdown()
	for !rl.WindowShouldClose() && !app.Quit() {
		app.Frame()
	}
}
