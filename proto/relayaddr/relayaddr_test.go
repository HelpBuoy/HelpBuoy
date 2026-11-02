package relayaddr

import "testing"

func TestParseValid(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"relay.example.com", "relay.example.com:2222"},
		{"relay.example.com:2222", "relay.example.com:2222"},
		{"  Relay.Example.COM:443 ", "relay.example.com:443"},
		{"relay.example.com.:22", "relay.example.com.:22"},
		{"localhost", "localhost:2222"},
		{"192.0.2.10", "192.0.2.10:2222"},
		{"192.0.2.10:2200", "192.0.2.10:2200"},
		{"[2001:db8::1]", "[2001:db8::1]:2222"},
		{"[2001:db8::1]:443", "[2001:db8::1]:443"},
		{"2001:db8::1", "[2001:db8::1]:2222"},
		{"::1", "[::1]:2222"},
		{"my-relay:65535", "my-relay:65535"},
	}
	for _, tt := range tests {
		got, err := Parse(tt.in)
		if err != nil {
			t.Errorf("Parse(%q) error: %v", tt.in, err)
			continue
		}
		if got.String() != tt.want {
			t.Errorf("Parse(%q) = %q, want %q", tt.in, got.String(), tt.want)
		}
	}
}

func TestParseInvalid(t *testing.T) {
	for _, in := range []string{
		"",
		"   ",
		":2222",
		"relay.example.com:",
		"relay.example.com:0",
		"relay.example.com:65536",
		"relay.example.com:ssh",
		"ssh://relay.example.com",
		"relay.example.com/path",
		"user@relay.example.com",
		"relay example.com",
		"-relay.example.com",
		"relay..example.com",
		"relay_example.com",
		"[192.0.2.1]:2222",
		"[2001:db8::1",
		"[2001:db8::1]x",
		"2001:db8::zz",
	} {
		if got, err := Parse(in); err == nil {
			t.Errorf("Parse(%q) = %q, want error", in, got.String())
		}
	}
}

func FuzzParse(f *testing.F) {
	for _, s := range []string{"relay.example.com:2222", "[::1]:22", "::1", "a:b:c"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		a, err := Parse(s)
		if err != nil {
			return
		}
		// A parsed address must round-trip to itself.
		b, err := Parse(a.String())
		if err != nil {
			t.Fatalf("Parse(%q) ok, but reparse of %q failed: %v", s, a.String(), err)
		}
		if a != b {
			t.Fatalf("round trip changed %+v to %+v", a, b)
		}
	})
}
