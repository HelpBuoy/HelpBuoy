package encode

import "github.com/helpbuoy/helpbuoy/proto/screen"

// Sizes of the version 2 frame wire format (proto/screen/ops.go).
const (
	frameHeader = 4 + 1 + 4 // Seq, flags, op count
)

// OpSize returns the encoded size of one op in the version 2 wire format.
func OpSize(r screen.Rect) int { return r.EncodedSize() }

// splitParts packs ops into frame parts whose payload stays within
// screen.MaxPart (an op larger than that gets a part of its own). It returns
// the parts and the total payload size.
func splitParts(ops []screen.Rect) ([]screen.Frame, int) {
	var frames []screen.Frame
	start, size, total := 0, frameHeader, 0
	for i, op := range ops {
		n := OpSize(op)
		if i > start && size+n > screen.MaxPart {
			frames = append(frames, screen.Frame{More: true, Rects: ops[start:i:i]})
			total += size
			start, size = i, frameHeader
		}
		size += n
	}
	frames = append(frames, screen.Frame{Rects: ops[start:]})
	return frames, total + size
}
