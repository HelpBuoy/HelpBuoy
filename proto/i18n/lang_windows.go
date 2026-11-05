package i18n

import (
	"os"

	"golang.org/x/sys/windows"
)

// preferred returns the user's display languages, such as "nl-NL". LANG
// comes first when set, which Wine and test runs use.
func preferred() []string {
	if s := os.Getenv("LANG"); s != "" {
		return []string{s}
	}
	tags, _ := windows.GetUserPreferredUILanguages(windows.MUI_LANGUAGE_NAME)
	return tags
}
