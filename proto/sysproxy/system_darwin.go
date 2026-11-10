package sysproxy

import (
	"context"
	"os/exec"
	"time"
)

// System reads the proxy from the network settings with scutil --proxy.
func System() Settings {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/sbin/scutil", "--proxy").Output()
	if err != nil {
		return Settings{}
	}
	return ParseScutil(string(out))
}
