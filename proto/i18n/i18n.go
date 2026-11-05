// Package i18n translates the user-facing strings of the helpbuoy apps.
//
// The English text is the key: Trans("Connect") returns the translation for
// the user's language, or the English text when there is none. Arguments
// fill the placeholders {0}, {1}, ... by index, so a translation may reorder
// or repeat them. Catalogs are flat JSON objects in locales/<lang>.json,
// compiled into the binary. Plurals are not supported; phrase counted
// strings so the number needs no agreement ("Computers online: 3").
package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"path"
	"strconv"
	"strings"
	"sync"
)

//go:embed locales/*.json
var locales embed.FS

var (
	once    sync.Once
	catalog map[string]string
	lang    = "en"
)

// load picks the user's language and its catalog on first use.
func load() {
	once.Do(func() {
		for _, tag := range preferred() {
			if l := base(tag); l != "" {
				if c, err := readCatalog(l); err == nil {
					lang, catalog = l, c
					return
				}
				if l == "en" {
					return
				}
			}
		}
	})
}

// base reduces a language tag such as "nl_NL.UTF-8" or "nl-BE" to "nl".
func base(tag string) string {
	tag = strings.ToLower(strings.TrimSpace(tag))
	if i := strings.IndexAny(tag, "_-.@"); i >= 0 {
		tag = tag[:i]
	}
	if tag == "c" || tag == "posix" {
		return "en"
	}
	return tag
}

func readCatalog(l string) (map[string]string, error) {
	data, err := locales.ReadFile(path.Join("locales", l+".json"))
	if err != nil {
		return nil, err
	}
	var c map[string]string
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("i18n: %s.json: %w", l, err)
	}
	return c, nil
}

// Lang returns the language in use, such as "en" or "nl".
func Lang() string {
	load()
	return lang
}

// Trans returns the translation of text with {0}, {1}, ... replaced by
// fmt.Sprint of the argument at that index. A placeholder without an
// argument is left as is.
func Trans(text string, args ...any) string {
	load()
	if t, ok := catalog[text]; ok && t != "" {
		text = t
	}
	return fill(text, args)
}

func fill(s string, args []any) string {
	if len(args) == 0 || !strings.Contains(s, "{") {
		return s
	}
	var b strings.Builder
	for {
		i := strings.IndexByte(s, '{')
		if i < 0 {
			break
		}
		j := strings.IndexByte(s[i:], '}')
		if j < 0 {
			break
		}
		n, err := strconv.Atoi(s[i+1 : i+j])
		if err != nil || n < 0 || n >= len(args) || s[i+1] == '+' {
			b.WriteString(s[:i+1])
			s = s[i+1:]
			continue
		}
		b.WriteString(s[:i])
		fmt.Fprint(&b, args[n])
		s = s[i+j+1:]
	}
	b.WriteString(s)
	return b.String()
}

// Chars returns every character used by the embedded translations, so a
// font atlas can include them.
func Chars() []rune {
	seen := map[rune]bool{}
	var out []rune
	entries, _ := locales.ReadDir("locales")
	for _, e := range entries {
		c, err := readCatalog(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			continue
		}
		for _, t := range c {
			for _, r := range t {
				if !seen[r] {
					seen[r] = true
					out = append(out, r)
				}
			}
		}
	}
	return out
}
