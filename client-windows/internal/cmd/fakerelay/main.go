// Command fakerelay runs the client's fake relay for manual checks of the
// windows, for example under Wine:
//
//	go run ./client-windows/internal/cmd/fakerelay -listen 127.0.0.1:5938 -register 10s
//
// With -register every pending key is registered after the delay; with
// -connect a helper with a fresh key connects that long after a registered
// client comes online. Once the user allows it, the helper runs the inner SSH client, decodes and acks the frames like the
// helper app and ends the session after -session. With -png the decoded
// screen, cursor included, is written to a PNG file every 2 s and at the end.
package main

import (
	"flag"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"log"
	"os"
	"os/signal"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mevdschee/help-me-win/client-windows/identity"
	"github.com/mevdschee/help-me-win/client-windows/internal/fakerelay"
	"github.com/mevdschee/help-me-win/proto/inner"
	"github.com/mevdschee/help-me-win/proto/screen"
	"github.com/mevdschee/help-me-win/proto/wire"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:5938", "address to listen on")
	register := flag.Duration("register", 0, "register pending keys after this delay (0 = never)")
	connect := flag.Duration("connect", 0, "connect a helper this long after a registered client comes online (0 = never)")
	var o helperOpts
	flag.DurationVar(&o.length, "session", 10*time.Second, "end sessions after this long")
	flag.StringVar(&o.png, "png", "", "write the helper's view of the screen to this PNG file")
	flag.DurationVar(&o.selectAfter, "select-after", 0, "ask for monitor -select after this long (0 = never)")
	flag.IntVar(&o.selectIndex, "select", 0, "monitor index for -select-after")
	flag.DurationVar(&o.refresh, "refresh-after", 0, "ask for a refresh after this long (0 = never)")
	flag.BoolVar(&o.verbose, "v", false, "print every frame and cursor move")
	flag.Parse()

	helper, err := identity.Ephemeral("alice")
	if err != nil {
		log.Fatal(err)
	}
	r, err := fakerelay.Start(*listen, fakerelay.Options{
		AutoRegister: *register,
		AutoConnect:  *connect,
		HelperName:   "alice",
		HelperKey:    helper.Line,
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("fake relay on", r.Addr(), "host key", identity.KeyLine(r.HostKey(), ""))
	go func() {
		for m := range r.Received() {
			fmt.Printf("%s: %T %+v\n", m.Fingerprint, m.Msg, m.Msg)
		}
	}()
	go func() {
		for s := range r.Sessions() {
			go watch(s, helper.Signer, o)
		}
	}()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	<-sig
	r.Close()
}

// helperOpts are the helper behaviours of a fake session.
type helperOpts struct {
	length      time.Duration
	png         string
	selectAfter time.Duration
	selectIndex int
	refresh     time.Duration
	verbose     bool
}

// watch acts as the helper of one session: it decodes and acks frames
// until the session is d old, then ends it and, with -png, writes the last
// image with the cursor drawn in.
func watch(s fakerelay.Session, signer ssh.Signer, o helperOpts) {
	defer s.Channel.Close()
	cs, err := inner.Dial(s.Channel, signer, s.Key)
	if err != nil {
		fmt.Println("inner dial:", err)
		return
	}
	defer cs.Close()
	time.AfterFunc(o.length, func() { cs.Close() })
	fr := wire.NewReader(cs.Screen())
	fw := wire.NewWriter(cs.Screen())
	var wmu sync.Mutex
	write := func(typ uint8, b []byte) {
		wmu.Lock()
		defer wmu.Unlock()
		fw.WriteFrame(typ, b)
	}
	if o.selectAfter > 0 {
		time.AfterFunc(o.selectAfter, func() {
			fmt.Println("asking for monitor", o.selectIndex)
			write(screen.MsgSelectMonitor, screen.EncodeSelectMonitor(screen.SelectMonitor{Index: o.selectIndex}))
		})
	}
	if o.refresh > 0 {
		time.AfterFunc(o.refresh, func() {
			fmt.Println("asking for a refresh")
			write(screen.MsgRefresh, nil)
		})
	}

	var (
		img     *image.RGBA
		dec     = screen.NewDecoder()
		shapes  = map[uint32]screen.CursorShape{}
		pos     screen.CursorPos
		frames  int
		parts   int
		bytes   int
		poses   int
		start   = time.Now()
		lastPNG time.Time
	)
	save := func() {
		if o.png == "" || img == nil {
			return
		}
		if err := writePNG(o.png, img, shapes[pos.ID], pos); err != nil {
			fmt.Println("png:", err)
		}
	}
	defer func() {
		save()
		el := time.Since(start).Seconds()
		fmt.Printf("session %s: %d frames (%.1f fps), %d parts, %.0f kB (%.0f kbit/s), %d shapes, %d cursor moves\n",
			s.Data.RequestID, frames, float64(frames)/el, parts, float64(bytes)/1000, float64(bytes)*8/1000/el, len(shapes), poses)
		if o.png != "" && img != nil {
			fmt.Println("last image written to", o.png)
		}
	}()
	for {
		typ, b, err := fr.ReadFrame()
		if err != nil {
			fmt.Println("screen stream ended:", err)
			return
		}
		switch typ {
		case screen.MsgHello:
			h, err := screen.DecodeHello(b)
			if err != nil {
				fmt.Println("hello:", err)
				return
			}
			fmt.Printf("screen hello %+v\n", h)
			img = image.NewRGBA(image.Rect(0, 0, h.Width, h.Height))
			dec.Reset()
		case screen.MsgFrame:
			bytes += len(b)
			f, err := screen.DecodeFrame(b)
			if err != nil {
				fmt.Println("frame:", err)
				return
			}
			if img == nil {
				fmt.Println("frame before hello")
				return
			}
			parts++
			_, complete, err := dec.Apply(img, f)
			if err != nil {
				fmt.Println("apply:", err)
				dec.Reset()
				write(screen.MsgRefresh, nil)
				continue
			}
			if !complete {
				continue
			}
			frames++
			if o.verbose {
				fmt.Printf("frame %d: %d ops\n", f.Seq, len(f.Rects))
			}
			write(screen.MsgAck, screen.EncodeAck(screen.Ack{Seq: f.Seq}))
			if time.Since(lastPNG) > 2*time.Second {
				save()
				lastPNG = time.Now()
			}
		case screen.MsgCursor:
			c, err := screen.DecodeCursorShape(b)
			if err != nil {
				fmt.Println("cursor shape:", err)
				return
			}
			shapes[c.ID] = c
			fmt.Printf("cursor shape %08x %dx%d hotspot %d,%d\n", c.ID, c.W, c.H, c.HotX, c.HotY)
		case screen.MsgCursorPos:
			p, err := screen.DecodeCursorPos(b)
			if err != nil {
				fmt.Println("cursor pos:", err)
				return
			}
			if p.ID != pos.ID || p.Visible != pos.Visible || o.verbose {
				fmt.Printf("cursor %+v\n", p)
			}
			pos = p
			poses++
		default:
			fmt.Println("unknown screen message", typ)
		}
	}
}

// writePNG writes img with the cursor shape drawn at p. Screen pixels are
// made opaque, as the helper draws them; a count of pixels that arrived
// with another alpha is printed, since the protocol says A = 255.
func writePNG(path string, img *image.RGBA, c screen.CursorShape, p screen.CursorPos) error {
	out := image.NewRGBA(img.Rect)
	copy(out.Pix, img.Pix)
	notOpaque := 0
	for i := 3; i < len(out.Pix); i += 4 {
		if out.Pix[i] != 255 {
			notOpaque++
			out.Pix[i] = 255
		}
	}
	if notOpaque > 0 {
		fmt.Printf("png: %d of %d pixels arrived with alpha != 255\n", notOpaque, len(out.Pix)/4)
	}
	if p.Visible && c.W > 0 && len(c.RGBA) == c.W*c.H*4 {
		src := &image.NRGBA{Pix: c.RGBA, Stride: c.W * 4, Rect: image.Rect(0, 0, c.W, c.H)}
		at := image.Pt(p.X-c.HotX, p.Y-c.HotY)
		draw.Draw(out, src.Rect.Add(at), src, image.Point{}, draw.Over)
	}
	f, err := os.Create(path + ".tmp")
	if err != nil {
		return err
	}
	if err := png.Encode(f, out); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}
