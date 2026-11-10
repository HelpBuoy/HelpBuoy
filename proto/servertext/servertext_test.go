package servertext

import "testing"

func TestClean(t *testing.T) {
	for in, want := range map[string]string{
		"Example Inc":        "Example Inc",
		"Microsoft‮ Support": "Microsoft Support",
		"line one\nline two": "line one line two",
		"zero​width":         "zerowidth",
		"  a \t b  ":         "a b",
		"ok\x00\x1b[31m":     "ok[31m",
		"✔ Verified":         "✔ Verified",
	} {
		if got := Clean(in); got != want {
			t.Errorf("Clean(%q) = %q, want %q", in, got, want)
		}
	}
}
