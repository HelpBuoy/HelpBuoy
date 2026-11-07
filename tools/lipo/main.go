// Command lipo merges thin Mach-O executables into one universal (fat)
// binary, on any OS (no Xcode tools needed). Each slice keeps its own code
// signature.
//
//	go run tools/lipo/main.go -out helpbuoy bin/darwin-amd64/helpbuoy bin/darwin-arm64/helpbuoy
package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
)

// align is the slice alignment, 2^14: the arm64 page size, which Apple's
// lipo also uses for x86_64 nowadays.
const align = 14

func main() {
	out := flag.String("out", "", "output file")
	flag.Parse()
	if *out == "" || flag.NArg() < 1 {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(*out, flag.Args()); err != nil {
		log.Fatal(err)
	}
}

type slice struct {
	cpu, sub uint32
	data     []byte
}

func run(out string, paths []string) error {
	var slices []slice
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if len(b) < 12 || binary.LittleEndian.Uint32(b) != 0xfeedfacf {
			return fmt.Errorf("%s: not a thin 64-bit Mach-O file", p)
		}
		s := slice{cpu: binary.LittleEndian.Uint32(b[4:]), sub: binary.LittleEndian.Uint32(b[8:]), data: b}
		for _, o := range slices {
			if o.cpu == s.cpu {
				return fmt.Errorf("%s: a second slice for CPU type %#x", p, s.cpu)
			}
		}
		slices = append(slices, s)
	}
	if len(slices) == 0 {
		return errors.New("no input")
	}
	var buf bytes.Buffer
	be := func(v uint32) { binary.Write(&buf, binary.BigEndian, v) }
	be(0xcafebabe)
	be(uint32(len(slices)))
	off := uint32(1 << align)
	offsets := make([]uint32, len(slices))
	for i, s := range slices {
		offsets[i] = off
		be(s.cpu)
		be(s.sub &^ 0x80000000) // without the capability bits, as lipo does
		be(off)
		be(uint32(len(s.data)))
		be(align)
		off = (off + uint32(len(s.data)) + 1<<align - 1) &^ (1<<align - 1)
	}
	for i, s := range slices {
		buf.Write(make([]byte, int(offsets[i])-buf.Len()))
		buf.Write(s.data)
	}
	return os.WriteFile(out, buf.Bytes(), 0o755)
}
