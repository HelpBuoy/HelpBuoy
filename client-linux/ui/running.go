package ui

import (
	"encoding/binary"
	"errors"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

// ShowRunning asks the window manager to bring the window of the running
// copy, process pid, to the front. It finds the window by _NET_WM_PID among
// the window manager's _NET_CLIENT_LIST.
func ShowRunning(pid int) error {
	if pid <= 0 {
		return errors.New("no process id in the lock file")
	}
	c, err := xgb.NewConn()
	if err != nil {
		return err
	}
	defer c.Close()
	x := &xwin{c: c, setup: xproto.Setup(c), atoms: map[string]xproto.Atom{}}
	x.scr = x.setup.DefaultScreen(c)
	for _, n := range []string{"_NET_CLIENT_LIST", "_NET_WM_PID", "_NET_ACTIVE_WINDOW"} {
		r, err := xproto.InternAtom(c, false, uint16(len(n)), n).Reply()
		if err != nil {
			return err
		}
		x.atoms[n] = r.Atom
	}
	list, err := xproto.GetProperty(c, false, x.scr.Root, x.atoms["_NET_CLIENT_LIST"],
		xproto.AtomWindow, 0, 1<<16).Reply()
	if err != nil {
		return err
	}
	for _, w := range cardinals(list) {
		r, err := xproto.GetProperty(c, false, xproto.Window(w), x.atoms["_NET_WM_PID"],
			xproto.AtomCardinal, 0, 1).Reply()
		if err != nil {
			continue
		}
		if p := cardinals(r); len(p) == 1 && int(p[0]) == pid {
			x.win = xproto.Window(w)
			// Source 2 (a pager): the user asked for it by starting the
			// app again, so focus stealing prevention should let it through.
			x.clientMessage(x.atoms["_NET_ACTIVE_WINDOW"], 2, xproto.TimeCurrentTime, 0)
			_, err := xproto.GetInputFocus(c).Reply() // flush before closing
			return err
		}
	}
	return errors.New("no window of the running copy found")
}

// cardinals returns the 32-bit values of a property reply.
func cardinals(r *xproto.GetPropertyReply) []uint32 {
	if r == nil || r.Format != 32 {
		return nil
	}
	v := make([]uint32, 0, len(r.Value)/4)
	for i := 0; i+4 <= len(r.Value); i += 4 {
		v = append(v, binary.LittleEndian.Uint32(r.Value[i:]))
	}
	return v
}
