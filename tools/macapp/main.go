// Command macapp writes a zipped macOS .app bundle for the desk app or the
// client, on any OS (no Xcode tools needed).
//
//	go run tools/macapp/main.go -app helpbuoy-desk -id helpbuoy.desk \
//	    -version 0.0.1 -exe bin/darwin-arm64/helpbuoy-desk \
//	    -icon desk/resources/icon.png -out out.zip
//
// With -rcodesign it signs the bundle ad hoc with rcodesign (apple-codesign)
// before zipping it: the signature seals Info.plist and the resources, and
// covers every slice of a universal executable. Apple silicon needs at
// least an ad hoc signature to run anything, and macOS ties the Screen
// Recording and Accessibility permissions to it.
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
	"os/exec"
	"path/filepath"
	"time"
)

func main() {
	app := flag.String("app", "helpbuoy-desk", "bundle and executable name")
	id := flag.String("id", "helpbuoy.desk", "CFBundleIdentifier")
	version := flag.String("version", "0.0.1", "CFBundleShortVersionString (numeric, like 1.2.3)")
	exe := flag.String("exe", "", "main executable")
	icon := flag.String("icon", "", "PNG icon, 256x256 or 512x512")
	out := flag.String("out", "", "output .zip")
	name := flag.String("name", "", "bundle name shown in Finder and the menu bar (default: -app)")
	minOS := flag.String("min", "11.0", "LSMinimumSystemVersion")
	rcodesign := flag.String("rcodesign", "", "rcodesign executable to sign the bundle ad hoc with")
	flag.Parse()
	if *exe == "" || *icon == "" || *out == "" {
		flag.Usage()
		os.Exit(2)
	}
	if *name == "" {
		*name = *app
	}
	if err := run(bundle{*app, *name, *id, *version, *minOS}, *exe, *icon, *out, *rcodesign); err != nil {
		log.Fatal(err)
	}
}

// bundle is what goes into Info.plist.
type bundle struct {
	exe, name, id, version, minOS string
}

type file struct {
	name string
	data []byte
	mode fs.FileMode
}

func run(b bundle, exe, icon, out, rcodesign string) error {
	exeData, err := os.ReadFile(exe)
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

	root := b.name + ".app/Contents/"
	files := []file{
		{root + "Info.plist", []byte(infoPlist(b)), 0o644},
		{root + "MacOS/" + b.exe, exeData, 0o755},
		{root + "Resources/icon.icns", icns, 0o644},
	}
	if rcodesign != "" {
		if files, err = sign(files, b.name+".app", rcodesign); err != nil {
			return err
		}
	}
	return writeZip(files, out)
}

// sign writes the bundle to a temporary directory, signs it there and
// returns its files, now with the signature.
func sign(files []file, app, rcodesign string) ([]file, error) {
	tmp, err := os.MkdirTemp("", "macapp-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	for _, f := range files {
		p := filepath.Join(tmp, filepath.FromSlash(f.name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(p, f.data, f.mode); err != nil {
			return nil, err
		}
	}
	cmd := exec.Command(rcodesign, "sign", filepath.Join(tmp, app))
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("rcodesign: %w", err)
	}
	var signed []file
	err = filepath.WalkDir(tmp, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(tmp, p)
		signed = append(signed, file{filepath.ToSlash(rel), data, info.Mode().Perm()})
		return nil
	})
	return signed, err
}

func writeZip(files []file, out string) error {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	now := time.Now()
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

func infoPlist(b bundle) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleExecutable</key><string>` + b.exe + `</string>
	<key>CFBundleIdentifier</key><string>` + b.id + `</string>
	<key>CFBundleName</key><string>` + b.name + `</string>
	<key>CFBundleIconFile</key><string>icon</string>
	<key>CFBundlePackageType</key><string>APPL</string>
	<key>CFBundleShortVersionString</key><string>` + b.version + `</string>
	<key>CFBundleVersion</key><string>` + b.version + `</string>
	<key>LSMinimumSystemVersion</key><string>` + b.minOS + `</string>
	<key>NSHighResolutionCapable</key><true/>
</dict>
</plist>
`
}
