// Command mkmsix packs a directory into an unsigned MSIX package, like
// "makeappx pack /d DIR /p OUT.msix /nc" on Windows. The directory must hold
// AppxManifest.xml; mkmsix adds AppxBlockMap.xml and [Content_Types].xml.
//
// The files are stored without compression, which keeps the block map
// simple: a block of a stored file has a hash but no compressed size. The
// Microsoft Store signs the package after certification; to install it
// before that, sign it with osslsigncode (see the msix-test target).
//
// Usage: go run ./tools/mkmsix DIR OUT.msix
package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"hash/crc32"
	"io"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	blockSize    = 64 * 1024 // the block map hashes the files in blocks of 64 KiB
	manifestName = "AppxManifest.xml"
	blockMapName = "AppxBlockMap.xml"
	typesName    = "[Content_Types].xml"
)

// The MS-DOS date of every entry, 1980-01-01, so that a package depends only
// on its contents.
const dosDate = 1<<5 | 1

// contentTypes maps the extensions of the payload files to their content
// type in [Content_Types].xml.
var contentTypes = map[string]string{
	"exe":  "application/x-msdownload",
	"dll":  "application/x-msdownload",
	"png":  "image/png",
	"ico":  "image/vnd.microsoft.icon",
	"json": "application/json",
	"txt":  "text/plain",
	"xml":  "application/xml",
}

// Names are kept to characters that need no escaping in a zip part name or
// in XML.
var validName = regexp.MustCompile(`^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*$`)

// countWriter counts the bytes written to the package, to find the size of
// each local file header.
type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

type entry struct {
	name    string // zip name, with slashes
	size    int
	lfhSize int64
	hashes  []string
}

func main() {
	log.SetFlags(0)
	if len(os.Args) != 3 {
		log.Fatal("usage: mkmsix DIR OUT.msix")
	}
	if err := pack(os.Args[1], os.Args[2]); err != nil {
		log.Fatal("mkmsix: ", err)
	}
}

func pack(dir, out string) error {
	names, err := payload(dir)
	if err != nil {
		return err
	}
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer f.Close()
	cw := &countWriter{w: f}
	zw := zip.NewWriter(cw)

	var entries []entry
	exts := map[string]bool{}
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
		if err != nil {
			return err
		}
		lfh, err := store(zw, cw, name, data)
		if err != nil {
			return err
		}
		e := entry{name: name, size: len(data), lfhSize: lfh}
		for off := 0; off < len(data); off += blockSize {
			sum := sha256.Sum256(data[off:min(off+blockSize, len(data))])
			e.hashes = append(e.hashes, base64.StdEncoding.EncodeToString(sum[:]))
		}
		entries = append(entries, e)
		if name != manifestName {
			ext := strings.ToLower(strings.TrimPrefix(path.Ext(name), "."))
			if contentTypes[ext] == "" {
				return fmt.Errorf("%s: no content type for .%s", name, ext)
			}
			exts[ext] = true
		}
	}
	if _, err := store(zw, cw, blockMapName, blockMap(entries)); err != nil {
		return err
	}
	if _, err := store(zw, cw, typesName, types(exts)); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return f.Close()
}

// payload lists the files of dir as zip names, sorted, with the manifest
// last, as makeappx does.
func payload(dir string) ([]string, error) {
	var names []string
	hasManifest := false
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)
		switch {
		case name == manifestName:
			hasManifest = true
			return nil
		case name == blockMapName || name == typesName || name == "AppxSignature.p7x":
			return fmt.Errorf("%s: mkmsix makes this file itself", name)
		case !validName.MatchString(name):
			return fmt.Errorf("%q: unsupported file name", name)
		}
		names = append(names, name)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !hasManifest {
		return nil, fmt.Errorf("%s: no %s", dir, manifestName)
	}
	sort.Strings(names)
	return append(names, manifestName), nil
}

// store adds data uncompressed under name, with the sizes and CRC in the
// local file header (no data descriptor), and returns the size of that
// header, which the block map needs.
func store(zw *zip.Writer, cw *countWriter, name string, data []byte) (int64, error) {
	if err := zw.Flush(); err != nil {
		return 0, err
	}
	start := cw.n
	w, err := zw.CreateRaw(&zip.FileHeader{
		Name:               name,
		Method:             zip.Store,
		ModifiedDate:       dosDate,
		CRC32:              crc32.ChecksumIEEE(data),
		CompressedSize64:   uint64(len(data)),
		UncompressedSize64: uint64(len(data)),
	})
	if err != nil {
		return 0, err
	}
	if err := zw.Flush(); err != nil {
		return 0, err
	}
	lfh := cw.n - start
	if want := int64(30 + len(name)); lfh != want {
		return 0, fmt.Errorf("%s: local file header of %d bytes, want %d", name, lfh, want)
	}
	_, err = w.Write(data)
	return lfh, err
}

// blockMap returns AppxBlockMap.xml for the entries. Its file names use
// backslashes.
func blockMap(entries []entry) []byte {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="no"?>` + "\r\n")
	b.WriteString(`<BlockMap xmlns="http://schemas.microsoft.com/appx/2010/blockmap" HashMethod="http://www.w3.org/2001/04/xmlenc#sha256">`)
	for _, e := range entries {
		fmt.Fprintf(&b, `<File Name="%s" Size="%d" LfhSize="%d">`, strings.ReplaceAll(e.name, "/", `\`), e.size, e.lfhSize)
		for _, h := range e.hashes {
			fmt.Fprintf(&b, `<Block Hash="%s"/>`, h)
		}
		b.WriteString(`</File>`)
	}
	b.WriteString(`</BlockMap>`)
	return b.Bytes()
}

// types returns [Content_Types].xml for the payload extensions.
func types(exts map[string]bool) []byte {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="no"?>` + "\r\n")
	b.WriteString(`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">`)
	var list []string
	for ext := range exts {
		list = append(list, ext)
	}
	sort.Strings(list)
	for _, ext := range list {
		fmt.Fprintf(&b, `<Default Extension="%s" ContentType="%s"/>`, ext, contentTypes[ext])
	}
	b.WriteString(`<Override PartName="/` + manifestName + `" ContentType="application/vnd.ms-appx.manifest+xml"/>`)
	b.WriteString(`<Override PartName="/` + blockMapName + `" ContentType="application/vnd.ms-appx.blockmap+xml"/>`)
	b.WriteString(`</Types>`)
	return b.Bytes()
}
