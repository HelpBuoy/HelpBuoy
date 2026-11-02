// Package splice joins two SSH channels into one byte stream in both
// directions (PLAN 3.6). The relay never looks at the bytes.
package splice

import (
	"io"
	"sync"
	"sync/atomic"

	"golang.org/x/crypto/ssh"
)

// BufSize is the copy buffer per direction.
const BufSize = 256 << 10

// Result holds the byte counts of a finished splice.
type Result struct {
	AToB int64
	BToA int64
}

// Splice copies a to b and b to a until both directions are done. EOF on one
// side is passed on as a half-close (CloseWrite) to the other side. When
// either channel is closed (its request channel ends), both are closed.
// Channel requests on either side are refused. Splice returns once both
// channels have been closed; the copy goroutines end as soon as the peers
// confirm the close.
func Splice(a ssh.Channel, aReqs <-chan *ssh.Request, b ssh.Channel, bReqs <-chan *ssh.Request) Result {
	var once sync.Once
	closed := make(chan struct{})
	closeBoth := func() {
		once.Do(func() {
			a.Close()
			b.Close()
			close(closed)
		})
	}
	var ab, ba atomic.Int64
	var wg sync.WaitGroup
	cp := func(dst, src ssh.Channel, n *atomic.Int64) {
		defer wg.Done()
		buf := make([]byte, BufSize)
		for {
			m, rerr := src.Read(buf)
			if m > 0 {
				w, werr := dst.Write(buf[:m])
				n.Add(int64(w))
				if werr != nil {
					closeBoth()
					return
				}
			}
			if rerr != nil {
				if rerr == io.EOF {
					dst.CloseWrite()
				} else {
					closeBoth()
				}
				return
			}
		}
	}
	watch := func(reqs <-chan *ssh.Request) {
		for r := range reqs {
			if r.WantReply {
				r.Reply(false, nil)
			}
		}
		closeBoth()
	}
	wg.Add(2)
	go cp(b, a, &ab)
	go cp(a, b, &ba)
	go watch(aReqs)
	go watch(bReqs)
	go func() {
		wg.Wait()
		closeBoth()
	}()
	<-closed
	return Result{AToB: ab.Load(), BToA: ba.Load()}
}
