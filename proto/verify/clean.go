package verify

import (
	"strings"
	"unicode"
)

// marks are characters that look like a check mark or a badge.
var marks = map[rune]bool{
	'✓': true, '✔': true, '☑': true, '✅': true, '🗸': true, '🗹': true,
	'√': true, '🛡': true, '🏅': true, '🎖': true,
}

// words is "verified" in every language the apps are translated to, in lower
// case. Longer words come first, so "vérifiée" goes before "vérifié".
var words = []string{
	"geverifieerd", "verifiziert", "verificado", "verificada", "verificato",
	"verificata", "vérifiées", "vérifiés", "vérifiée", "vérifié", "verified",
}

// Clean prepares text that comes from a server (its title, instruction line,
// a techie's name) for display, so it can't imitate the verified badge
// (PLAN.md 12.6): one line, no control or format characters (bidi overrides,
// zero-width characters), no check marks, and no word "verified" in any of
// the apps' languages.
func Clean(s string) string {
	s = oneLine(s)
	rs := make([]rune, 0, len(s))
	for _, r := range s {
		if !marks[r] && !unicode.Is(unicode.Variation_Selector, r) {
			rs = append(rs, r)
		}
	}
	for removed := true; removed; {
		removed = false
		for _, w := range words {
			if i := indexFold(rs, []rune(w)); i >= 0 {
				rs = append(rs[:i], rs[i+len([]rune(w)):]...)
				removed = true
			}
		}
	}
	return strings.Join(strings.Fields(string(rs)), " ")
}

// oneLine puts s on one line without control or format characters.
func oneLine(s string) string {
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

// indexFold finds w (lower case) in s, ignoring case.
func indexFold(s, w []rune) int {
outer:
	for i := 0; i+len(w) <= len(s); i++ {
		for j, r := range w {
			if unicode.ToLower(s[i+j]) != r {
				continue outer
			}
		}
		return i
	}
	return -1
}
