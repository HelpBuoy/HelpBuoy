// Package servertext cleans text that comes from a server (its title,
// instruction line, a techie's name) before the apps show it.
package servertext

import (
	"strings"
	"unicode"
)

// Clean puts s on one line without control or format characters (bidi
// overrides, zero-width characters), with runs of spaces collapsed.
func Clean(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteRune(' ')
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r), r == unicode.ReplacementChar:
		default:
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}
