// Package render keeps a session's remote screen in one GPU texture and
// uploads only the changed rectangles each frame (PLAN.md 5.5), with
// rl.UpdateTextureRec (glTexSubImage2D underneath). Main OS thread only.
package render

import (
	"image"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/mevdschee/help-me-win/helper/session"
	"github.com/mevdschee/help-me-win/proto/screen"
)

// Screen is one remote screen texture.
type Screen struct {
	tex  rl.Texture2D
	w, h int
	ok   bool
	buf  []byte
}

// Ready reports whether the texture holds a screen.
func (s *Screen) Ready() bool { return s.ok }

// Size returns the remote screen size in pixels.
func (s *Screen) Size() (w, h int) { return s.w, s.h }

// Texture returns the texture to draw.
func (s *Screen) Texture() rl.Texture2D { return s.tex }

// Upload copies the dirty rects of img into the texture, recreating the
// texture first if the image size changed. It matches the callback of
// session.Session.Update.
func (s *Screen) Upload(img *image.RGBA, dirty []image.Rectangle, resized bool) {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	if resized || !s.ok || w != s.w || h != s.h {
		s.Unload()
		blank := rl.GenImageColor(w, h, rl.Black)
		s.tex = rl.LoadTextureFromImage(blank)
		rl.UnloadImage(blank)
		rl.SetTextureFilter(s.tex, rl.FilterBilinear)
		s.w, s.h, s.ok = w, h, true
		dirty = []image.Rectangle{img.Rect}
	}
	for _, r := range dirty {
		r = r.Intersect(img.Rect)
		if r.Empty() {
			continue
		}
		b := session.SubImageBytes(img, r, s.buf)
		if r.Dx() != w && cap(b) > cap(s.buf) {
			s.buf = b
		}
		rl.UpdateTextureRec(s.tex, rl.NewRectangle(float32(r.Min.X-img.Rect.Min.X), float32(r.Min.Y-img.Rect.Min.Y), float32(r.Dx()), float32(r.Dy())), b)
	}
}

// Fit returns the letterboxed rectangle in area that shows the whole screen
// at its aspect ratio.
func (s *Screen) Fit(area rl.Rectangle) rl.Rectangle {
	return Fit(s.w, s.h, area)
}

// Fit returns the largest rectangle with the aspect ratio of w x h, centered
// in area.
func Fit(w, h int, area rl.Rectangle) rl.Rectangle {
	if w <= 0 || h <= 0 || area.Width <= 0 || area.Height <= 0 {
		return rl.NewRectangle(area.X, area.Y, 0, 0)
	}
	scale := min(area.Width/float32(w), area.Height/float32(h))
	dw, dh := float32(w)*scale, float32(h)*scale
	return rl.NewRectangle(area.X+(area.Width-dw)/2, area.Y+(area.Height-dh)/2, dw, dh)
}

// Draw draws the screen into dest.
func (s *Screen) Draw(dest rl.Rectangle) {
	if !s.ok {
		return
	}
	src := rl.NewRectangle(0, 0, float32(s.w), float32(s.h))
	rl.DrawTexturePro(s.tex, src, dest, rl.NewVector2(0, 0), 0, rl.White)
}

// Unload frees the texture.
func (s *Screen) Unload() {
	if s.ok {
		rl.UnloadTexture(s.tex)
		s.ok = false
	}
}

// maxCursorTextures bounds the cursor textures kept; beyond it all are
// dropped and reloaded on use.
const maxCursorTextures = 64

// Cursors keeps GPU textures of remote cursor shapes, by shape ID.
type Cursors struct {
	tex map[uint32]rl.Texture2D
}

// Draw draws shape with its hot spot at p, scaled by scale.
func (c *Cursors) Draw(shape *screen.CursorShape, p rl.Vector2, scale float32) {
	if shape == nil || shape.W <= 0 || shape.H <= 0 || len(shape.RGBA) != shape.W*shape.H*4 {
		return
	}
	t, ok := c.tex[shape.ID]
	if !ok {
		if len(c.tex) >= maxCursorTextures {
			c.Unload()
		}
		if c.tex == nil {
			c.tex = map[uint32]rl.Texture2D{}
		}
		img := rl.NewImage(shape.RGBA, int32(shape.W), int32(shape.H), 1, rl.UncompressedR8g8b8a8)
		t = rl.LoadTextureFromImage(img)
		rl.SetTextureFilter(t, rl.FilterBilinear)
		c.tex[shape.ID] = t
	}
	src := rl.NewRectangle(0, 0, float32(shape.W), float32(shape.H))
	dst := rl.NewRectangle(p.X-float32(shape.HotX)*scale, p.Y-float32(shape.HotY)*scale, float32(shape.W)*scale, float32(shape.H)*scale)
	rl.DrawTexturePro(t, src, dst, rl.NewVector2(0, 0), 0, rl.White)
}

// Unload frees all cursor textures.
func (c *Cursors) Unload() {
	for _, t := range c.tex {
		rl.UnloadTexture(t)
	}
	c.tex = nil
}
