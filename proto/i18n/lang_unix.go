//go:build !windows && !darwin

package i18n

import "os"

// preferred returns the user's languages from the POSIX locale variables.
func preferred() []string {
	var tags []string
	for _, v := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if s := os.Getenv(v); s != "" {
			tags = append(tags, s)
			break
		}
	}
	return tags
}
