// Command hmw-helper is the helper app: it lists connected clients
// and opens remote sessions for their help requests.
//
// It uses raylib through raylib-go built with CGO_ENABLED=0 (purego), so it
// cross-compiles for every target with plain "go build".
//
// Flags for trying the UI without a relay:
//
//	--demo           fake clients and requests, no network, nothing stored
//	--page NAME      start on "register", "settings" or "session" (demo only)
//	--datadir DIR    use DIR instead of the per-user data directory
package main

import (
	_ "embed"
	"flag"
	"os"
	"runtime"
	"slices"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/mevdschee/help-me-win/helper/perf"
	"github.com/mevdschee/help-me-win/helper/views"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

//go:embed resources/icon.png
var iconPNG []byte

func init() {
	runtime.LockOSThread() // raylib must stay on the main OS thread
}

func main() {
	demo := flag.Bool("demo", os.Getenv("SV_HELPER_DEMO") != "", "show fake clients and requests; no network, nothing stored")
	page := flag.String("page", os.Getenv("SV_HELPER_PAGE"), `start page: "register", "settings" or "session" (demo)`)
	dataDir := flag.String("datadir", "", "data directory (default: per-user data directory)")
	// --upload-bench is a hidden developer check (helper/perf): it measures
	// texture uploads in a window, prints a table and exits.
	uploadBench := slices.Contains(os.Args[1:], "--upload-bench")
	if uploadBench {
		os.Args = slices.DeleteFunc(os.Args, func(a string) bool { return a == "--upload-bench" })
	}
	flag.Parse()

	rl.SetTraceLogLevel(rl.LogWarning)
	rl.SetConfigFlags(rl.FlagWindowResizable | rl.FlagWindowHighdpi | rl.FlagMsaa4xHint)
	rl.InitWindow(1100, 700, "help-me-win helper")
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

	app := views.New(views.Options{DataDir: *dataDir, Version: version, Demo: *demo, Page: *page})
	defer app.Shutdown()
	for !rl.WindowShouldClose() && !app.Quit() {
		app.Frame()
	}
}
