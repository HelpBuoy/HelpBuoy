//go:build !windows && !darwin

package sysproxy

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// System reads the GNOME proxy settings with gsettings, when it is there.
func System() Settings {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	get := func(schema, key string) string {
		out, err := exec.CommandContext(ctx, "gsettings", "get", schema, key).Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	mode := get("org.gnome.system.proxy", "mode")
	if strings.Trim(mode, "'") != "manual" {
		return Settings{}
	}
	return ParseGsettings(mode,
		get("org.gnome.system.proxy.https", "host"),
		get("org.gnome.system.proxy.https", "port"),
		get("org.gnome.system.proxy", "ignore-hosts"))
}
