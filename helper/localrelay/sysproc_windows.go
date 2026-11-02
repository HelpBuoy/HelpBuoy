package localrelay

import (
	"os/exec"
	"syscall"
)

const createNoWindow = 0x08000000

// setSysProcAttr keeps hmw-relay.exe from opening a console window, since the
// helper app is a GUI program.
func setSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}
