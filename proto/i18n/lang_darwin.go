package i18n

import (
	"os"
	"os/exec"
	"strings"
)

// preferred returns the user's languages: LC_ALL, LC_MESSAGES or LANG when
// set (a terminal start), else AppleLanguages, since an .app started from
// Finder gets no LANG.
func preferred() []string {
	for _, v := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if s := os.Getenv(v); s != "" {
			return []string{s}
		}
	}
	out, err := exec.Command("defaults", "read", "-g", "AppleLanguages").Output()
	if err != nil {
		return nil
	}
	// The output is a plist array: ( "nl-NL", "en-NL" ).
	var tags []string
	for _, f := range strings.FieldsFunc(string(out), func(r rune) bool {
		return r == '(' || r == ')' || r == ',' || r == '"' || r == ' ' || r == '\n'
	}) {
		tags = append(tags, f)
	}
	return tags
}
