// Command recpng writes frames of a recording made with the client's
// --record option as PNG files, to check captures by eye:
//
//	go run ./client-windows/internal/cmd/recpng -frame 0 -out first.png DIR
package main

import (
	"flag"
	"fmt"
	"image/png"
	"log"
	"os"

	"github.com/mevdschee/help-me-win/client-windows/capture"
	"github.com/mevdschee/help-me-win/proto/encode"
)

func main() {
	frame := flag.Int("frame", -1, "frame to write; negative counts from the end")
	out := flag.String("out", "frame.png", "PNG file to write")
	flag.Parse()
	if flag.NArg() != 1 {
		log.Fatal("usage: recpng [-frame N] [-out FILE] DIR")
	}
	rec, err := encode.OpenRecording(flag.Arg(0))
	if err != nil {
		log.Fatal(err)
	}
	n := len(rec.Frames)
	if n == 0 {
		log.Fatal("recording has no frames")
	}
	i := *frame
	if i < 0 {
		i += n
	}
	if i < 0 || i >= n {
		log.Fatalf("frame %d of %d", *frame, n)
	}
	img, err := rec.Load(i, nil)
	if err != nil {
		log.Fatal(err)
	}
	lo, hi := byte(255), byte(0)
	for j := 3; j < len(img.Pix); j += 4 {
		lo, hi = min(lo, img.Pix[j]), max(hi, img.Pix[j])
	}
	fmt.Printf("captured alpha values %d..%d\n", lo, hi)
	if rec.BGRA {
		capture.SwapRB(img.Pix)
	} else {
		for j := 3; j < len(img.Pix); j += 4 {
			img.Pix[j] = 255
		}
	}
	f, err := os.Create(*out)
	if err != nil {
		log.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		log.Fatal(err)
	}
	if err := f.Close(); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("frame %d of %d (%dx%d, t=%v) written to %s\n", i, n, rec.Width, rec.Height, rec.Frames[i].T, *out)
}
