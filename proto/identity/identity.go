// Package identity handles Ed25519 public keys as people paste them: parsing
// key lines mangled by chat apps, fingerprints, and the user@machine name.
package identity

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/user"
	"runtime"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/ssh"
)

// ErrNotEd25519 is returned for valid SSH keys of another type (RSA, ECDSA, ...).
var ErrNotEd25519 = errors.New("not an ssh-ed25519 key")

// ErrNoKey is returned when the text contains no SSH public key at all.
var ErrNoKey = errors.New("no ssh-ed25519 public key found")

// keyTypes are the SSH public key type names ParsePasted looks for in pasted
// text. Only ssh-ed25519 is accepted; the others are recognized so that the
// caller can tell the user that the wrong kind of key was pasted.
var keyTypes = []string{
	ssh.KeyAlgoED25519,
	ssh.KeyAlgoSKED25519,
	ssh.KeyAlgoRSA,
	ssh.KeyAlgoDSA,
	ssh.KeyAlgoECDSA256,
	ssh.KeyAlgoECDSA384,
	ssh.KeyAlgoECDSA521,
	ssh.KeyAlgoSKECDSA256,
}

// blobFields is the number of length-prefixed strings in the wire encoding of
// each public key type, the type name included.
var blobFields = map[string]int{
	ssh.KeyAlgoED25519:    2, // name, key
	ssh.KeyAlgoSKED25519:  3, // name, key, application
	ssh.KeyAlgoRSA:        3, // name, e, n
	ssh.KeyAlgoDSA:        5, // name, p, q, g, y
	ssh.KeyAlgoECDSA256:   3, // name, curve, point
	ssh.KeyAlgoECDSA384:   3,
	ssh.KeyAlgoECDSA521:   3,
	ssh.KeyAlgoSKECDSA256: 4, // name, curve, point, application
}

// ParsePasted finds an "ssh-ed25519 AAAA... [comment]" key line inside text
// as it arrives from WhatsApp, mail or a chat: tolerates line breaks inside the
// base64, leading/trailing spaces, quotes, backticks, non-breaking and
// zero-width spaces, and other words before or after the key. It returns the
// key, its comment (may be empty) and the normalized authorized_keys line
// "ssh-ed25519 <base64>[ <comment>]".
//
// The text is first normalized: zero-width characters (U+200B..U+200D,
// U+2060, U+FEFF, soft hyphen) are dropped, every Unicode space (including
// NBSP) and line break becomes a plain space, and straight, typographic and
// angle quotes as well as backticks become spaces.
//
// A key starts after a key type name ("ssh-ed25519", "ssh-rsa", ...) or at a
// word beginning with "AAAA". From there base64 characters are collected,
// skipping any whitespace in between, and the SSH wire encoding inside them
// determines exactly where the key ends, so a line-wrapped key is put back
// together without swallowing the following word. The blob is validated with
// ssh.ParsePublicKey. The first ssh-ed25519 key wins; if only keys of other
// types are found, the error is ErrNotEd25519, otherwise ErrNoKey.
//
// The comment is the first whitespace-separated word after the key, with
// leading "([{<*~" and trailing ".,;:!?)]}>*~" characters removed. It is used
// only if it looks like a name: it contains an "@" that is neither its first
// nor its last character, and it satisfies ValidName. Anything else (a
// following sentence such as "thanks!", or nothing at all) gives an empty
// comment. Comments containing spaces are not supported.
func ParsePasted(text string) (key ssh.PublicKey, comment, line string, err error) {
	rs := normalize(text)
	foundOther := false
	for _, start := range candidates(rs) {
		k, end, ok := parseBlobAt(rs, start)
		if !ok {
			continue
		}
		if k.Type() != ssh.KeyAlgoED25519 {
			foundOther = true
			continue
		}
		c := commentAfter(rs, end)
		return k, c, Line(k, c), nil
	}
	if foundOther {
		return nil, "", "", ErrNotEd25519
	}
	return nil, "", "", ErrNoKey
}

// normalize maps text to runes as described in ParsePasted. Invalid UTF-8
// becomes a space.
func normalize(text string) []rune {
	rs := make([]rune, 0, len(text))
	for _, r := range text {
		switch {
		case r == utf8.RuneError:
			rs = append(rs, ' ')
		case r == '\u200b', r == '\u200c', r == '\u200d', r == '\u2060',
			r == '\ufeff', r == '\u00ad', r == '\u180e':
			// zero-width characters: drop
		case unicode.IsSpace(r), unicode.IsControl(r):
			rs = append(rs, ' ')
		case strings.ContainsRune("\"'`\u2018\u2019\u201a\u201b\u201c\u201d\u201e\u201f\u00ab\u00bb\u2039\u203a\u2032\u2033", r):
			rs = append(rs, ' ')
		default:
			rs = append(rs, r)
		}
	}
	return rs
}

func isBase64(r rune) bool {
	return r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '+' || r == '/'
}

// candidates returns the rune offsets where a base64 key blob may start, in
// order of appearance.
func candidates(rs []rune) []int {
	var out []int
	seen := map[int]bool{}
	add := func(i int) {
		if !seen[i] {
			seen[i] = true
			out = append(out, i)
		}
	}
	for i := range rs {
		// After a key type name, skipping whitespace.
		for _, t := range keyTypes {
			if hasPrefixAt(rs, i, t) {
				j := i + len(t)
				for j < len(rs) && rs[j] == ' ' {
					j++
				}
				if j < len(rs) {
					add(j)
				}
			}
		}
		// A bare blob: a word starting with "AAAA".
		if (i == 0 || !isBase64(rs[i-1]) && rs[i-1] != '=') && hasPrefixAt(rs, i, "AAAA") {
			add(i)
		}
	}
	return out
}

func hasPrefixAt(rs []rune, i int, s string) bool {
	for _, c := range s {
		if i >= len(rs) || rs[i] != c {
			return false
		}
		i++
	}
	return true
}

// parseBlobAt collects base64 characters from rs[start:], skipping spaces,
// and parses the public key they encode. It returns the key and the rune
// offset just after its last base64 (or padding) character.
func parseBlobAt(rs []rune, start int) (ssh.PublicKey, int, bool) {
	var b64 []byte
	var pos []int // rune offset of each collected character
	for i := start; i < len(rs); i++ {
		r := rs[i]
		if r == ' ' {
			continue
		}
		if !isBase64(r) {
			break
		}
		b64 = append(b64, byte(r))
		pos = append(pos, i)
		// SSH public key blobs are at most a few KB; stop collecting long
		// before anything unreasonable.
		if len(b64) > 16384 {
			break
		}
	}
	if len(b64) < 4 {
		return nil, 0, false
	}
	n := len(b64) / 4 * 4
	raw := make([]byte, base64.StdEncoding.DecodedLen(n))
	m, err := base64.StdEncoding.Decode(raw, b64[:n])
	if err != nil {
		return nil, 0, false
	}
	raw = raw[:m]
	// Bytes of a possible trailing partial group.
	if rest := b64[n:]; len(rest) >= 2 {
		extra, err := base64.RawStdEncoding.DecodeString(string(rest))
		if err == nil {
			raw = append(raw, extra...)
		}
	}

	size, ok := blobSize(raw)
	if !ok {
		return nil, 0, false
	}
	k, err := ssh.ParsePublicKey(raw[:size])
	if err != nil {
		return nil, 0, false
	}
	used := base64.RawStdEncoding.EncodedLen(size)
	if used > len(pos) {
		return nil, 0, false
	}
	end := pos[used-1] + 1
	// Skip padding, possibly separated by spaces.
	for pad := (4 - used%4) % 4; pad > 0; {
		j := end
		for j < len(rs) && rs[j] == ' ' {
			j++
		}
		if j >= len(rs) || rs[j] != '=' {
			break
		}
		end = j + 1
		pad--
	}
	return k, end, true
}

// blobSize reads the key type name at the start of raw and returns the
// length of the whole public key blob according to its number of fields.
func blobSize(raw []byte) (int, bool) {
	name, ok := wireString(raw, 0)
	if !ok {
		return 0, false
	}
	fields, ok := blobFields[string(name)]
	if !ok {
		return 0, false
	}
	off := 0
	for range fields {
		s, ok := wireString(raw, off)
		if !ok {
			return 0, false
		}
		off += 4 + len(s)
	}
	return off, true
}

func wireString(b []byte, off int) ([]byte, bool) {
	if off+4 > len(b) {
		return nil, false
	}
	n := binary.BigEndian.Uint32(b[off:])
	if uint64(off)+4+uint64(n) > uint64(len(b)) {
		return nil, false
	}
	return b[off+4 : off+4+int(n)], true
}

// commentAfter returns the comment following a key that ends at rune offset
// end, following the rule documented on ParsePasted.
func commentAfter(rs []rune, end int) string {
	i := end
	for i < len(rs) && rs[i] == ' ' {
		i++
	}
	j := i
	for j < len(rs) && rs[j] != ' ' {
		j++
	}
	word := string(rs[i:j])
	word = strings.TrimLeft(word, "([{<*~")
	word = strings.TrimRight(word, ".,;:!?)]}>*~")
	at := strings.IndexByte(word, '@')
	if at <= 0 || at == len(word)-1 || !ValidName(word) {
		return ""
	}
	return word
}

// ParseLine parses one well-formed authorized_keys line (as stored by the
// relay or sent in control messages). Only ssh-ed25519 is accepted.
//
// The line is "ssh-ed25519 <base64>" optionally followed by a space and a
// comment; surrounding ASCII whitespace (such as a trailing newline) is
// ignored.
// The comment is the rest of the line with surrounding spaces removed. Lines
// with options, line breaks or control characters are rejected.
func ParseLine(line string) (key ssh.PublicKey, comment string, err error) {
	line = strings.Trim(line, " \t\r\n")
	if line == "" {
		return nil, "", ErrNoKey
	}
	if !utf8.ValidString(line) {
		return nil, "", fmt.Errorf("invalid key line: not valid UTF-8")
	}
	for _, r := range line {
		if unicode.IsControl(r) && r != '\t' {
			return nil, "", fmt.Errorf("invalid key line: control character")
		}
	}
	typ, rest, _ := strings.Cut(line, " ")
	b64, comment, _ := strings.Cut(rest, " ")
	comment = strings.Trim(comment, " \t")
	if _, known := blobFields[typ]; !known {
		return nil, "", ErrNoKey
	}
	blob, err := base64.StdEncoding.Strict().DecodeString(b64)
	if err != nil {
		return nil, "", fmt.Errorf("invalid key line: %v", err)
	}
	k, err := ssh.ParsePublicKey(blob)
	if err != nil {
		return nil, "", fmt.Errorf("invalid key line: %v", err)
	}
	if k.Type() != typ {
		return nil, "", fmt.Errorf("invalid key line: type %q does not match key type %q", typ, k.Type())
	}
	if typ != ssh.KeyAlgoED25519 {
		return nil, "", ErrNotEd25519
	}
	return k, comment, nil
}

// Fingerprint returns "SHA256:<base64 without padding>", like ssh-keygen -l.
func Fingerprint(key ssh.PublicKey) string { return ssh.FingerprintSHA256(key) }

// Line returns the authorized_keys line for key with an optional comment.
func Line(key ssh.PublicKey, comment string) string {
	s := key.Type() + " " + base64.StdEncoding.EncodeToString(key.Marshal())
	if comment != "" {
		s += " " + comment
	}
	return s
}

// UserMachine returns the current "user@machine" name: user lower-cased,
// machine as the OS reports it (%COMPUTERNAME% on Windows, short hostname
// elsewhere), e.g. "anna@ANNA-LAPTOP".
//
// On Windows the user is %USERNAME% (falling back to the account name without
// its DOMAIN\ prefix); elsewhere it is the account name, falling back to $USER
// and $LOGNAME. If a part can't be determined, "user" or "machine" is used.
// Spaces and control characters are replaced with "_" and the result is
// shortened if needed, so that it always satisfies ValidName.
func UserMachine() string {
	u, m := currentUser(), machineName()
	u = sanitize(strings.ToLower(u), "user")
	m = sanitize(m, "machine")
	ur, mr := []rune(u), []rune(m)
	if len(mr) > 32 {
		mr = mr[:32]
	}
	if len(ur)+1+len(mr) > 64 {
		ur = ur[:63-len(mr)]
	}
	return string(ur) + "@" + string(mr)
}

func currentUser() string {
	if runtime.GOOS == "windows" {
		if s := os.Getenv("USERNAME"); s != "" {
			return stripDomain(s)
		}
	}
	if cu, err := user.Current(); err == nil && cu.Username != "" {
		return stripDomain(cu.Username)
	}
	for _, env := range []string{"USER", "LOGNAME", "USERNAME"} {
		if s := os.Getenv(env); s != "" {
			return stripDomain(s)
		}
	}
	return ""
}

// stripDomain removes a Windows "DOMAIN\" prefix.
func stripDomain(s string) string {
	if i := strings.LastIndexByte(s, '\\'); i >= 0 {
		return s[i+1:]
	}
	return s
}

func machineName() string {
	if runtime.GOOS == "windows" {
		if s := os.Getenv("COMPUTERNAME"); s != "" {
			return s
		}
	}
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	if i := strings.IndexByte(h, '.'); i > 0 {
		h = h[:i]
	}
	return h
}

// sanitize replaces characters ValidName rejects with "_" and drops "@", so
// the result can be used as one side of a user@machine name.
func sanitize(s, fallback string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '@':
			return '_'
		case unicode.IsSpace(r) || !unicode.IsPrint(r):
			return '_'
		}
		return r
	}, strings.TrimSpace(s))
	if s == "" {
		return fallback
	}
	return s
}

// ValidName reports whether s is acceptable as a client or helper name:
// 1..64 printable characters, no spaces or control characters.
func ValidName(s string) bool {
	if s == "" || !utf8.ValidString(s) || utf8.RuneCountInString(s) > 64 {
		return false
	}
	for _, r := range s {
		if !unicode.IsPrint(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}
