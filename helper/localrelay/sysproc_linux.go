package localrelay

import (
	"os/exec"
	"syscall"
)

// setSysProcAttr makes the child relay stop if the helper app dies.
func setSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
}
