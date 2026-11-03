//go:build !linux

package capture

import (
	"errors"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/shm"
)

const shmSupported = false

type shmSeg struct {
	id   shm.Seg
	data []byte
}

func newShmSeg(c *xgb.Conn, size int) (*shmSeg, error) {
	return nil, errors.New("MIT-SHM is only used on Linux")
}

func (s *shmSeg) close(c *xgb.Conn) {}
