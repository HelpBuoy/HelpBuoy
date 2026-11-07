package pwcapture

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestArgs(t *testing.T) {
	got := strings.Join(Args(3, 42, 1280, 720), " ")
	for _, want := range []string{"pipewiresrc fd=3 path=42 ", "format=BGRx,width=1280,height=720", "fdsink fd=1"} {
		if !strings.Contains(got, want) {
			t.Errorf("args %q lack %q", got, want)
		}
	}
}

// TestCapturePipeWire streams a solid color into the local PipeWire and
// captures it through a connection of its own, as the portal would hand
// over.
func TestCapturePipeWire(t *testing.T) {
	sock := filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "pipewire-0")
	if _, err := os.Stat(sock); err != nil || Check() != nil {
		t.Skip("no PipeWire or no gst-launch-1.0 with pipewiresrc")
	}
	if _, err := exec.LookPath("pw-dump"); err != nil {
		t.Skip("no pw-dump")
	}
	name := fmt.Sprintf("helpbuoy-test-%d", os.Getpid())
	// foreground-color is ARGB: red.
	prod := exec.Command(gstLaunch, "-q", "videotestsrc", "pattern=solid-color", "foreground-color=0xffff0000",
		"!", "video/x-raw,format=BGRx,width=64,height=48,framerate=30/1",
		"!", "pipewiresink", "mode=provide", "stream-properties=p,node.name="+name)
	if err := prod.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { prod.Process.Kill(); prod.Wait() })
	node := findNode(t, name)

	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	f, err := conn.(*net.UnixConn).File()
	conn.Close()
	if err != nil {
		t.Fatal(err)
	}
	// Scaled up on the way, as a HiDPI stream would be scaled down.
	c, err := Start(f, node, 128, 96, nil)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for range 5 {
		img, err := c.Capture()
		if err != nil {
			t.Fatal(err)
		}
		if img.Rect.Dx() != 128 || img.Rect.Dy() != 96 {
			t.Fatalf("size %v", img.Rect)
		}
		p := img.Pix[(48*128+64)*4:]
		if p[0] > 10 || p[1] > 10 || p[2] < 245 { // BGRX red
			t.Fatalf("pixel %v, want red as BGRX", p[:4])
		}
		time.Sleep(20 * time.Millisecond)
	}
	c.Close()
	if _, err := c.Capture(); err == nil {
		t.Error("Capture after Close worked")
	}
}

// TestCaptureBadNode reports why the stream did not start.
func TestCaptureBadNode(t *testing.T) {
	sock := filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "pipewire-0")
	if _, err := os.Stat(sock); err != nil || Check() != nil {
		t.Skip("no PipeWire or no gst-launch-1.0 with pipewiresrc")
	}
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	f, _ := conn.(*net.UnixConn).File()
	conn.Close()
	c, err := Start(f, 999999, 64, 48, nil)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Capture(); err == nil {
		t.Fatal("capture of a missing node worked")
	} else {
		t.Log(err)
	}
}

func findNode(t *testing.T, name string) uint32 {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		out, err := exec.Command("pw-dump").Output()
		if err == nil {
			var objs []struct {
				ID   uint32 `json:"id"`
				Info struct {
					Props map[string]any `json:"props"`
				} `json:"info"`
			}
			if json.Unmarshal(out, &objs) == nil {
				for _, o := range objs {
					if o.Info.Props["node.name"] == name {
						return o.ID
					}
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("test node did not show up")
	return 0
}
