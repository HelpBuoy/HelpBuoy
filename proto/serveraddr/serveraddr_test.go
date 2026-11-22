package serveraddr

import "testing"

func TestParseValid(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"server.example.com", "server.example.com:443"},
		{"server.example.com:5959", "server.example.com:5959"},
		{"  Server.Example.COM:443 ", "server.example.com:443"},
		{"server.example.com.:22", "server.example.com.:22"},
		{"localhost", "localhost:443"},
		{"192.0.2.10", "192.0.2.10:443"},
		{"192.0.2.10:2200", "192.0.2.10:2200"},
		{"[2001:db8::1]", "[2001:db8::1]:443"},
		{"[2001:db8::1]:443", "[2001:db8::1]:443"},
		{"2001:db8::1", "[2001:db8::1]:443"},
		{"::1", "[::1]:443"},
		{"my-server:65535", "my-server:65535"},
		{"relay.example.com/acme", "relay.example.com:443/acme"},
		{" Relay.Example.com:8443/Acme/ ", "relay.example.com:8443/acme"},
		{"relay.example.com/", "relay.example.com:443"},
		{"[2001:db8::1]/acme-2", "[2001:db8::1]:443/acme-2"},
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
		"server.example.com/a/b",
		"server.example.com//acme",
		"server.example.com/acme_1",
		"server.example.com/-acme",
		"server.example.com/acme.",
		"server.example.com/acme.example.com",
		"server.example.com:/acme",
		"/acme",
		"https://server.example.com/acme",
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
	for _, s := range []string{"server.example.com:5959", "[::1]:22", "::1", "a:b:c", "server.example.com/acme"} {
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

func TestSplit(t *testing.T) {
	for in, want := range map[string][2]string{
		"relay.example.com:443":      {"relay.example.com:443", ""},
		"relay.example.com:443/acme": {"relay.example.com:443", "acme"},
		"[2001:db8::1]:8443/acme":    {"[2001:db8::1]:8443", "acme"},
	} {
		if hp, v := Split(in); hp != want[0] || v != want[1] {
			t.Errorf("Split(%q) = %q, %q", in, hp, v)
		}
	}
}
