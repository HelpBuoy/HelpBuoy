// Command macapp writes a zipped macOS .app bundle for the desk app, with
// helpbuoy-relay next to the main executable, on any OS (no Xcode tools needed).
//
//	go run tools/macapp/main.go -app helpbuoy-desk -id helpbuoy.desk \
//	    -version 0.0.1 -exe bin/darwin-arm64/helpbuoy-desk \
//	    -relay bin/darwin-arm64/helpbuoy-relay -icon desk/resources/icon.png -out out.zip
package main

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"image"
	_ "image/png"
	"io/fs"
	"log"
	"os"
	"time"
)

func main() {
	app := flag.String("app", "helpbuoy-desk", "bundle and executable name")
	id := flag.String("id", "helpbuoy.desk", "CFBundleIdentifier")
	version := flag.String("version", "0.0.1", "CFBundleShortVersionString (numeric, like 1.2.3)")
	exe := flag.String("exe", "", "main executable")
	relay := flag.String("relay", "", "helpbuoy-relay executable to put next to it")
	icon := flag.String("icon", "", "PNG icon, 256x256 or 512x512")
	out := flag.String("out", "", "output .zip")
	flag.Parse()
	if *exe == "" || *relay == "" || *icon == "" || *out == "" {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(*app, *id, *version, *exe, *relay, *icon, *out); err != nil {
		log.Fatal(err)
	}
}

func run(app, id, version, exe, relay, icon, out string) error {
	exeData, err := os.ReadFile(exe)
	if err != nil {
		return err
	}
	relayData, err := os.ReadFile(relay)
	if err != nil {
		return err
	}
	pngData, err := os.ReadFile(icon)
	if err != nil {
		return err
	}
	icns, err := makeICNS(pngData)
	if err != nil {
		return err
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	root := app + ".app/Contents/"
	now := time.Now()
	files := []struct {
		name string
		data []byte
		mode fs.FileMode
	}{
		{root + "Info.plist", []byte(infoPlist(app, id, version)), 0o644},
		{root + "MacOS/" + app, exeData, 0o755},
		{root + "MacOS/helpbuoy-relay", relayData, 0o755},
		{root + "Resources/icon.icns", icns, 0o644},
	}
	for _, f := range files {
		h := &zip.FileHeader{Name: f.name, Method: zip.Deflate, Modified: now}
		h.SetMode(f.mode) // keeps the executable bit for Finder and unzip
		w, err := zw.CreateHeader(h)
		if err != nil {
			return err
		}
		if _, err := w.Write(f.data); err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return os.WriteFile(out, buf.Bytes(), 0o644)
}

// makeICNS wraps a PNG in an .icns container. 256x256 goes in as ic08,
// 512x512 as ic09; macOS scales it for the smaller sizes.
func makeICNS(pngData []byte) ([]byte, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(pngData))
	if err != nil {
		return nil, fmt.Errorf("icon: %w", err)
	}
	var typ string
	switch {
	case cfg.Width == 256 && cfg.Height == 256:
		typ = "ic08"
	case cfg.Width == 512 && cfg.Height == 512:
		typ = "ic09"
	default:
		return nil, fmt.Errorf("icon must be 256x256 or 512x512, got %dx%d", cfg.Width, cfg.Height)
	}
	var b bytes.Buffer
	total := uint32(8 + 8 + len(pngData))
	b.WriteString("icns")
	binary.Write(&b, binary.BigEndian, total)
	b.WriteString(typ)
	binary.Write(&b, binary.BigEndian, uint32(8+len(pngData)))
	b.Write(pngData)
	return b.Bytes(), nil
}

func infoPlist(app, id, version string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleExecutable</key><string>` + app + `</string>
	<key>CFBundleIdentifier</key><string>` + id + `</string>
	<key>CFBundleName</key><string>` + app + `</string>
	<key>CFBundleIconFile</key><string>icon</string>
	<key>CFBundlePackageType</key><string>APPL</string>
	<key>CFBundleShortVersionString</key><string>` + version + `</string>
	<key>CFBundleVersion</key><string>` + version + `</string>
	<key>LSMinimumSystemVersion</key><string>11.0</string>
	<key>NSHighResolutionCapable</key><true/>
</dict>
</plist>
`
}
