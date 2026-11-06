package views

import (
	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/helpbuoy/helpbuoy/desk/ui"
	"github.com/helpbuoy/helpbuoy/proto/i18n"
)

// RunningWidth and RunningHeight are the window size for AlreadyRunning.
const (
	RunningWidth  = 460
	RunningHeight = 170
)

// AlreadyRunning shows that the desk app is already running for this user
// and returns when the window is closed or OK is clicked. The window must be
// open.
func AlreadyRunning() {
	c := ui.New()
	defer c.Unload()
	rl.SetTargetFPS(30)
	for !rl.WindowShouldClose() {
		c.Begin()
		rl.BeginDrawing()
		rl.ClearBackground(ui.ColBackground)
		w, h := ui.WindowSize()
		const pad = 24
		y := float32(pad)
		c.Text(ui.Bold, ui.SizeHeading, i18n.Trans("HelpBuoy Desk is already running"), pad, y, ui.ColText)
		y += 36
		c.Paragraph(ui.Regular, ui.SizeBody, i18n.Trans("Switch to the open window. Two desk apps with the same key would keep disconnecting each other."), pad, y, w-2*pad, ui.ColMuted)
		okL := i18n.Trans("OK")
		bw := max(100, c.Measure(ui.Bold, ui.SizeBody, okL).X+16)
		done := c.Button("running/ok", rl.NewRectangle(w-pad-bw, h-pad-34, bw, 34), okL, ui.Primary, false)
		done = done || rl.IsKeyPressed(rl.KeyEnter) || rl.IsKeyPressed(rl.KeyEscape)
		c.End()
		rl.EndDrawing()
		if done {
			return
		}
	}
}
