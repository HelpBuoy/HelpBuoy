package serveraddr

import "testing"

func TestParseValid(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"server.example.com", "server.example.com:5959"},
		{"server.example.com:5959", "server.example.com:5959"},
		{"  Server.Example.COM:443 ", "server.example.com:443"},
		{"server.example.com.:22", "server.example.com.:22"},
		{"localhost", "localhost:5959"},
		{"192.0.2.10", "192.0.2.10:5959"},
		{"192.0.2.10:2200", "192.0.2.10:2200"},
		{"[2001:db8::1]", "[2001:db8::1]:5959"},
		{"[2001:db8::1]:443", "[2001:db8::1]:443"},
		{"2001:db8::1", "[2001:db8::1]:5959"},
		{"::1", "[::1]:5959"},
		{"my-server:65535", "my-server:65535"},
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
		":5959",
		"server.example.com:",
		"server.example.com:0",
		"server.example.com:65536",
		"server.example.com:ssh",
		"ssh://server.example.com",
		"server.example.com/path",
		"user@server.example.com",
		"server example.com",
		"-server.example.com",
		"server..example.com",
		"server_example.com",
		"[192.0.2.1]:5959",
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
	for _, s := range []string{"server.example.com:5959", "[::1]:22", "::1", "a:b:c"} {
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
