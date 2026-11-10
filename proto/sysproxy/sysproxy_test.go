package sysproxy

import "testing"

func TestParseWindows(t *testing.T) {
	tests := []struct {
		enable           uint32
		server, override string
		want             string
	}{
		{1, "proxy.corp:8080", "", "proxy.corp:8080"},
		{1, "http=web.corp:3128;https=secure.corp:8443;ftp=f:21", "", "secure.corp:8443"},
		{1, "http=web.corp:3128", "", "web.corp:3128"},
		{1, "proxy.corp", "", "proxy.corp:80"},
		{0, "proxy.corp:8080", "", ""},
		{1, "", "", ""},
	}
	for _, tt := range tests {
		if got := ParseWindows(tt.enable, tt.server, tt.override).Proxy; got != tt.want {
			t.Errorf("ParseWindows(%d, %q) = %q, want %q", tt.enable, tt.server, got, tt.want)
		}
	}
	s := ParseWindows(1, "proxy.corp:8080", "*.corp.example.com;10.*;<local>")
	for host, direct := range map[string]bool{
		"help.example.com":      false,
		"a.corp.example.com":    true,
		"10.1.2.3":              true,
		"intranet":              true,
		"localhost":             true,
		"127.0.0.1":             true,
		"corp.example.com.evil": false,
	} {
		u, _ := Lookup(s, host)
		if (u == nil) != direct {
			t.Errorf("%s: proxy %v, want direct %v", host, u, direct)
		}
	}
}

const scutilOut = `<dictionary> {
  ExceptionsList : <array> {
    0 : *.local
    1 : 169.254/16
    2 : .corp.example.com
  }
  ExcludeSimpleHostnames : 1
  FTPPassive : 1
  HTTPEnable : 1
  HTTPPort : 3128
  HTTPProxy : web.corp
  HTTPSEnable : 1
  HTTPSPort : 8443
  HTTPSProxy : secure.corp
}
`

func TestParseScutil(t *testing.T) {
	s := ParseScutil(scutilOut)
	if s.Proxy != "secure.corp:8443" || !s.BypassSimple || len(s.Bypass) != 3 {
		t.Fatalf("got %+v", s)
	}
	for host, direct := range map[string]bool{
		"help.example.com":   false,
		"printer.local":      true,
		"corp.example.com":   true,
		"x.corp.example.com": true,
		"intranet":           true,
	} {
		u, _ := Lookup(s, host)
		if (u == nil) != direct {
			t.Errorf("%s: proxy %v, want direct %v", host, u, direct)
		}
	}
	if s := ParseScutil("<dictionary> {\n  HTTPSEnable : 0\n  HTTPSProxy : x\n}\n"); s.Proxy != "" {
		t.Errorf("disabled: %+v", s)
	}
}

func TestParseGsettings(t *testing.T) {
	s := ParseGsettings("'manual'", "'proxy.corp'", "3128", "['localhost', '127.0.0.0/8', '::1', '*.corp']")
	if s.Proxy != "proxy.corp:3128" || len(s.Bypass) != 4 {
		t.Fatalf("got %+v", s)
	}
	if u, _ := Lookup(s, "x.corp"); u != nil {
		t.Error("*.corp not bypassed")
	}
	if u, _ := Lookup(s, "help.example.com"); u == nil || u.Host != "proxy.corp:3128" {
		t.Errorf("help.example.com: %v", u)
	}
	if s := ParseGsettings("'none'", "'proxy.corp'", "3128", "@as []"); s.Proxy != "" {
		t.Errorf("mode none: %+v", s)
	}
}

func TestOff(t *testing.T) {
	if u, err := Off(nil); u != nil || err != nil {
		t.Fatal("Off returned a proxy")
	}
}
