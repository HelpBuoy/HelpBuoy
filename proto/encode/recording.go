package encode

import (
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"time"

	"github.com/klauspost/compress/zstd"
)

// Recording format, shared by the computer's --record option and the replay
// benchmark: a directory with index.json and one file per frame.
//
//	index.json: {"width": W, "height": H, "bgra": true|false,
//	             "frames": [{"file": "000001.zst", "t_ms": 0}, ...]}
//	NNNNNN.zst: zstd of the full W*H*4 pixel buffer (row-major, no padding)
//
// t_ms is the capture time relative to the first frame.

type indexJSON struct {
	Width  int          `json:"width"`
	Height int          `json:"height"`
	BGRA   bool         `json:"bgra"`
	Frames []indexFrame `json:"frames"`
}

type indexFrame struct {
	File string `json:"file"`
	TMS  int64  `json:"t_ms"`
}

// Recorder writes a recording.
type Recorder struct {
	dir   string
	index indexJSON
	first time.Time
	enc   *zstd.Encoder
	buf   []byte
	err   error
}

// NewRecorder creates dir (must not exist or be empty) for a recording of
// w x h frames; bgra says the pixels are BGRA.
func NewRecorder(dir string, w, h int, bgra bool) (*Recorder, error) {
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("encode: recording size %dx%d", w, h)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	if len(entries) > 0 {
		return nil, fmt.Errorf("encode: recording directory %s is not empty", dir)
	}
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedFastest), zstd.WithEncoderConcurrency(1))
	if err != nil {
		return nil, err
	}
	return &Recorder{dir: dir, index: indexJSON{Width: w, Height: h, BGRA: bgra, Frames: []indexFrame{}}, enc: enc}, nil
}

// Add appends one frame captured at t.
func (r *Recorder) Add(img *image.RGBA, t time.Time) error {
	if r.err != nil {
		return r.err
	}
	w, h := r.index.Width, r.index.Height
	if img.Rect.Dx() != w || img.Rect.Dy() != h {
		return fmt.Errorf("encode: frame of %dx%d in a %dx%d recording", img.Rect.Dx(), img.Rect.Dy(), w, h)
	}
	if len(r.index.Frames) == 0 {
		r.first = t
	}
	r.buf = r.buf[:0]
	for y := range h {
		off := img.PixOffset(img.Rect.Min.X, img.Rect.Min.Y+y)
		r.buf = append(r.buf, img.Pix[off:off+w*4]...)
	}
	name := fmt.Sprintf("%06d.zst", len(r.index.Frames)+1)
	if err := os.WriteFile(filepath.Join(r.dir, name), r.enc.EncodeAll(r.buf, nil), 0o644); err != nil {
		r.err = err
		return err
	}
	r.index.Frames = append(r.index.Frames, indexFrame{File: name, TMS: t.Sub(r.first).Milliseconds()})
	return nil
}

// Close writes index.json.
func (r *Recorder) Close() error {
	if r.enc != nil {
		r.enc.Close()
		r.enc = nil
	}
	b, err := json.MarshalIndent(r.index, "", " ")
	if err != nil {
		return err
	}
	return errors.Join(r.err, os.WriteFile(filepath.Join(r.dir, "index.json"), b, 0o644))
}

// Recording is a loaded recording.
type Recording struct {
	Width, Height int
	BGRA          bool
	Frames        []RecordedFrame

	dir string
	dec *zstd.Decoder
}

// RecordedFrame is one frame of a recording; Load decodes it lazily.
type RecordedFrame struct {
	File string
	T    time.Duration
}

// OpenRecording reads index.json from dir.
func OpenRecording(dir string) (*Recording, error) {
	b, err := os.ReadFile(filepath.Join(dir, "index.json"))
	if err != nil {
		return nil, err
	}
	var idx indexJSON
	if err := json.Unmarshal(b, &idx); err != nil {
		return nil, fmt.Errorf("encode: %s: %w", filepath.Join(dir, "index.json"), err)
	}
	if idx.Width <= 0 || idx.Height <= 0 || idx.Width > 16384 || idx.Height > 16384 {
		return nil, fmt.Errorf("encode: recording size %dx%d", idx.Width, idx.Height)
	}
	rec := &Recording{Width: idx.Width, Height: idx.Height, BGRA: idx.BGRA, dir: dir}
	for _, f := range idx.Frames {
		if f.File == "" || filepath.Base(f.File) != f.File {
			return nil, fmt.Errorf("encode: bad frame file name %q", f.File)
		}
		rec.Frames = append(rec.Frames, RecordedFrame{File: f.File, T: time.Duration(f.TMS) * time.Millisecond})
	}
	return rec, nil
}

// Load decodes frame i into dst (allocated if nil or the wrong size).
func (r *Recording) Load(i int, dst *image.RGBA) (*image.RGBA, error) {
	if i < 0 || i >= len(r.Frames) {
		return nil, fmt.Errorf("encode: frame %d of %d", i, len(r.Frames))
	}
	if r.dec == nil {
		dec, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(uint64(r.Width*r.Height*4)+1<<20))
		if err != nil {
			return nil, err
		}
		r.dec = dec
	}
	b, err := os.ReadFile(filepath.Join(r.dir, r.Frames[i].File))
	if err != nil {
		return nil, err
	}
	n := r.Width * r.Height * 4
	raw, err := r.dec.DecodeAll(b, make([]byte, 0, n))
	if err != nil {
		return nil, fmt.Errorf("encode: %s: %w", r.Frames[i].File, err)
	}
	if len(raw) != n {
		return nil, fmt.Errorf("encode: %s has %d bytes, want %d", r.Frames[i].File, len(raw), n)
	}
	if dst == nil || dst.Rect.Dx() != r.Width || dst.Rect.Dy() != r.Height {
		dst = image.NewRGBA(image.Rect(0, 0, r.Width, r.Height))
	}
	for y := range r.Height {
		off := dst.PixOffset(dst.Rect.Min.X, dst.Rect.Min.Y+y)
		copy(dst.Pix[off:off+r.Width*4], raw[y*r.Width*4:])
	}
	return dst, nil
}
