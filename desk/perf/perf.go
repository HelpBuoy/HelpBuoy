// Package perf is a manual check of the GPU side of the viewer: how long the
// render thread spends per frame uploading dirty rects of a remote screen
// texture (render.Screen.Upload, i.e. rl.UpdateTextureRec) and drawing it.
// It needs a window, so it is not a go test benchmark; the agent runs it
// with the hidden --upload-bench flag and prints a table.
package perf

import (
	"fmt"
	"image"
	"io"
	"math/rand/v2"
	"slices"
	"time"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/mevdschee/helpbuoy/desk/render"
	"github.com/mevdschee/helpbuoy/desk/session"
)

// Frames is the number of measured frames per scenario.
const Frames = 120

type scenario struct {
	name  string
	tiles int  // random 64x64 tiles changed per frame; 0 means the whole screen
	plan  bool // merge with session.PlanUpload, else upload every tile
}

var scenarios = []scenario{
	{"typing: 4 tiles", 4, true},
	{"menus: 40 tiles, planned", 40, true},
	{"busy: 200 tiles, one call each", 200, false},
	{"busy: 200 tiles, planned", 200, true},
	{"full screen", 0, true},
}

// Run measures every scenario at 1920x1080 and 2560x1440 and writes the
// results to w. Call it on the main thread after rl.InitWindow.
func Run(w io.Writer) {
	rl.SetTargetFPS(0)
	fmt.Fprintf(w, "%-11s %-32s %8s %10s %10s %10s\n", "size", "scenario", "calls", "upload ms", "p95 ms", "frame ms")
	for _, sz := range []image.Point{{1920, 1080}, {2560, 1440}} {
		for _, sc := range scenarios {
			calls, up, p95, frame := measure(sz, sc)
			fmt.Fprintf(w, "%-11s %-32s %8.1f %10.2f %10.2f %10.2f\n", fmt.Sprintf("%dx%d", sz.X, sz.Y), sc.name, calls, up, p95, frame)
		}
	}
}

func measure(sz image.Point, sc scenario) (calls, upload, p95, frame float64) {
	img := image.NewRGBA(image.Rect(0, 0, sz.X, sz.Y))
	rng := rand.New(rand.NewPCG(1, 2))
	for i := range img.Pix {
		img.Pix[i] = byte(rng.IntN(256))
	}
	var scr render.Screen
	defer scr.Unload()
	scr.Upload(img, nil, true)
	cols, rows := sz.X/64, sz.Y/64
	ups := make([]float64, 0, Frames)
	var frames, nCalls float64
	for i := range Frames + 10 { // the first frames warm up
		var dirty []image.Rectangle
		if sc.tiles == 0 {
			dirty = []image.Rectangle{img.Rect}
		} else {
			for range sc.tiles {
				x, y := rng.IntN(cols)*64, rng.IntN(rows)*64
				dirty = append(dirty, image.Rect(x, y, x+64, y+64))
			}
		}
		for _, r := range dirty { // change the pixels, so every upload is real
			for y := r.Min.Y; y < r.Max.Y; y++ {
				img.Pix[img.PixOffset(r.Min.X, y)] += 17
			}
		}
		if sc.plan {
			dirty = session.PlanUpload(dirty, img.Rect)
		}
		t0 := time.Now()
		rl.BeginDrawing()
		rl.ClearBackground(rl.Black)
		t1 := time.Now()
		scr.Upload(img, dirty, false)
		t2 := time.Now()
		ww, wh := float32(rl.GetScreenWidth()), float32(rl.GetScreenHeight())
		scr.Draw(scr.Fit(rl.NewRectangle(0, 0, ww, wh)))
		rl.DrawText(fmt.Sprintf("%dx%d  %s", sz.X, sz.Y, sc.name), 10, 10, 20, rl.RayWhite)
		rl.EndDrawing()
		t3 := time.Now()
		if i < 10 {
			continue
		}
		ups = append(ups, ms(t2.Sub(t1)))
		frames += ms(t3.Sub(t0))
		nCalls += float64(len(dirty))
	}
	slices.Sort(ups)
	sum := 0.0
	for _, u := range ups {
		sum += u
	}
	return nCalls / Frames, sum / Frames, ups[len(ups)*95/100], frames / Frames
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
