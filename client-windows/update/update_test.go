package update

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestNewer(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"0.3.0", "0.2.0", true},
		{"v0.2.1", "0.2.0", true},
		{"0.2.1", "v0.2.0", true},
		{"0.10.0", "0.9.9", true},
		{"1.0", "0.9.9", true},
		{"0.2.0", "0.2.0", false},
		{"0.1.9", "0.2.0", false},
		{"0.2", "0.2.0", false},
		{"0.3.0", "", false},
		{"0.3.0", "dev", false},
		{"0.3.0-beta", "0.2.0", false},
	} {
		if got := Newer(tc.a, tc.b); got != tc.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestIsRelease(t *testing.T) {
	if !IsRelease("v0.1.0") || IsRelease("dev") || IsRelease("") {
		t.Fatal()
	}
}

func TestParseRelease(t *testing.T) {
	data := []byte(`{
		"tag_name": "v0.3.0", "draft": false, "prerelease": false,
		"assets": [
			{"name": "notes.txt", "browser_download_url": "https://example.com/notes.txt", "size": 10},
			{"name": "helpbuoy-desk_0.3.0_amd64.msi", "browser_download_url": "https://github.com/mevdschee/helpbuoy/releases/download/v0.3.0/helpbuoy-desk_0.3.0_amd64.msi", "size": 4509696},
			{"name": "helpbuoy_0.3.0_amd64.msi", "browser_download_url": "https://github.com/mevdschee/helpbuoy/releases/download/v0.3.0/helpbuoy_0.3.0_amd64.msi", "size": 3912192}
		]}`)
	r, ok := ParseRelease(data)
	if !ok {
		t.Fatal("no release found")
	}
	want := Release{Version: "0.3.0", MsiName: "helpbuoy_0.3.0_amd64.msi", MsiSize: 3912192,
		MsiURL: "https://github.com/mevdschee/helpbuoy/releases/download/v0.3.0/helpbuoy_0.3.0_amd64.msi"}
	if r != want {
		t.Errorf("got %+v", r)
	}

	for name, bad := range map[string]string{
		"no msi":     `{"tag_name": "v0.3.0", "assets": [{"name": "a.zip", "browser_download_url": "https://x/a.zip"}]}`,
		"desk only":  `{"tag_name": "v0.3.0", "assets": [{"name": "helpbuoy-desk_0.3.0_amd64.msi", "browser_download_url": "https://x/helpbuoy-desk_0.3.0_amd64.msi"}]}`,
		"prerelease": `{"tag_name": "v0.3.0", "prerelease": true, "assets": [{"name": "helpbuoy_a.msi", "browser_download_url": "https://x/a.msi"}]}`,
		"draft":      `{"tag_name": "v0.3.0", "draft": true, "assets": [{"name": "helpbuoy_a.msi", "browser_download_url": "https://x/a.msi"}]}`,
		"not https":  `{"tag_name": "v0.3.0", "assets": [{"name": "helpbuoy_a.msi", "browser_download_url": "http://x/a.msi"}]}`,
		"invalid":    `not json`,
	} {
		if _, ok := ParseRelease([]byte(bad)); ok {
			t.Errorf("%s: expected no release", name)
		}
	}
}

func TestCheck(t *testing.T) {
	var techie string
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		techie = r.Header.Get("User-Agent")
		w.WriteHeader(status)
		fmt.Fprint(w, `{"tag_name": "v0.3.0", "assets": [{"name": "helpbuoy_a.msi", "browser_download_url": "https://x/a.msi", "size": 5}]}`)
	}))
	defer srv.Close()
	ctx := context.Background()

	r, err := Check(ctx, srv.URL, "v0.2.0")
	if err != nil || r == nil || r.Version != "0.3.0" || techie != "helpbuoy/v0.2.0" {
		t.Fatalf("got %+v, %v, techie %q", r, err, techie)
	}
	if r, err := Check(ctx, srv.URL, "v0.3.0"); r != nil || err != nil {
		t.Fatalf("same version: %+v, %v", r, err)
	}
	techie = ""
	if r, err := Check(ctx, srv.URL, "dev"); r != nil || err != nil || techie != "" {
		t.Fatalf("dev build checked: %+v, %v", r, err)
	}
	status = http.StatusNotFound // a private repository
	if r, err := Check(ctx, srv.URL, "v0.2.0"); r != nil || err == nil {
		t.Fatalf("404: %+v, %v", r, err)
	}
}

func TestDownload(t *testing.T) {
	body := "MSI-DATA"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	}))
	defer srv.Close()
	dir := t.TempDir()
	ctx := context.Background()

	path, err := Download(ctx, Release{MsiURL: srv.URL, MsiName: "x.msi", MsiSize: int64(len(body))}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != body || !strings.HasSuffix(path, "x.msi") {
		t.Fatalf("%s: %q", path, b)
	}
	if _, err := Download(ctx, Release{MsiURL: srv.URL, MsiName: "y.msi", MsiSize: 99}, dir); err == nil {
		t.Fatal("size mismatch accepted")
	}
	if _, err := os.Stat(dir + "/y.msi"); !os.IsNotExist(err) {
		t.Fatal("incomplete download kept")
	}
}
