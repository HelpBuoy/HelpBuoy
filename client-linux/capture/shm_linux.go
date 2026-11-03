package capture

import (
	"github.com/jezek/xgb"
	"github.com/jezek/xgb/shm"
	"golang.org/x/sys/unix"
)

const shmSupported = true

// shmSeg is a System V shared memory segment attached to the X server.
type shmSeg struct {
	id   shm.Seg
	data []byte
}

// newShmSeg creates a segment of size bytes and attaches it to the X server.
// The segment is marked for removal as soon as the server attached it, so
// it disappears with the process even if it is killed.
func newShmSeg(c *xgb.Conn, size int) (*shmSeg, error) {
	shmid, err := unix.SysvShmGet(unix.IPC_PRIVATE, size, unix.IPC_CREAT|0o600)
	if err != nil {
		return nil, err
	}
	data, err := unix.SysvShmAttach(shmid, 0, 0)
	if err != nil {
		unix.SysvShmCtl(shmid, unix.IPC_RMID, nil)
		return nil, err
	}
	seg, err := shm.NewSegId(c)
	if err == nil {
		err = shm.AttachChecked(c, seg, uint32(shmid), false).Check()
	}
	unix.SysvShmCtl(shmid, unix.IPC_RMID, nil)
	if err != nil {
		unix.SysvShmDetach(data)
		return nil, err
	}
	return &shmSeg{id: seg, data: data}, nil
}

func (s *shmSeg) close(c *xgb.Conn) {
	shm.Detach(c, s.id)
	unix.SysvShmDetach(s.data)
}
