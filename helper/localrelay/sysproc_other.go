//go:build !linux && !windows

package localrelay

import "os/exec"

func setSysProcAttr(*exec.Cmd) {}
