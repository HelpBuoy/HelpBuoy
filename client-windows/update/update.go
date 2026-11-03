// Package update checks GitHub for a newer release of the client and
// downloads its MSI. It has no GUI dependencies; the window asks the user
// and starts msiexec.
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// LatestReleaseURL is the GitHub API endpoint for the newest release.
const LatestReleaseURL = "https://api.github.com/repos/mevdschee/helpbuoy/releases/latest"

// Limits for the release query and the MSI download.
const (
	queryTimeout    = 15 * time.Second
	queryLimit      = 1 << 20
	downloadTimeout = 5 * time.Minute
	downloadLimit   = 200 << 20
)

// Release is the part of a GitHub release the update check needs.
type Release struct {
	Version string // without the leading "v"
	MsiURL  string
	MsiName string
	MsiSize int64
}

// ParseRelease reads a GitHub "latest release" response and finds the first
// MSI served over https. Drafts and prereleases are ignored.
func ParseRelease(data []byte) (Release, bool) {
	var r struct {
		TagName    string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
		Assets     []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
			Size int64  `json:"size"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(data, &r); err != nil || r.Draft || r.Prerelease {
		return Release{}, false
	}
	for _, a := range r.Assets {
		if strings.HasSuffix(strings.ToLower(a.Name), ".msi") && strings.HasPrefix(a.URL, "https://") {
			return Release{
				Version: strings.TrimPrefix(r.TagName, "v"),
				MsiURL:  a.URL, MsiName: a.Name, MsiSize: a.Size,
			}, true
		}
	}
	return Release{}, false
}

// Newer reports whether version a is newer than b. Versions are dotted
// numbers like "0.2.0", optionally with a leading "v"; anything else, such
// as "dev", is never newer and never older.
func Newer(a, b string) bool {
	pa, okA := parseVersion(a)
	pb, okB := parseVersion(b)
	if !okA || !okB {
		return false
	}
	for i := range max(len(pa), len(pb)) {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			return x > y
		}
	}
	return false
}

// IsRelease reports whether version is a release version that takes part
// in update checks. Development builds ("dev") do not.
func IsRelease(version string) bool {
	_, ok := parseVersion(version)
	return ok
}

func parseVersion(s string) ([]int, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if s == "" {
		return nil, false
	}
	var parts []int
	for _, p := range strings.Split(s, ".") {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil, false
		}
		parts = append(parts, n)
	}
	return parts, true
}

// Check asks url (normally LatestReleaseURL) for the latest release and
// returns it if it is newer than version, or nil. A development version
// never checks. Network and HTTP errors are returned; callers that only
// want to try again next time can ignore them.
func Check(ctx context.Context, url, version string) (*Release, error) {
	if !IsRelease(version) {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "helpbuoy/"+version)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, queryLimit))
	if err != nil {
		return nil, err
	}
	r, ok := ParseRelease(data)
	if !ok || !Newer(r.Version, version) {
		return nil, nil
	}
	return &r, nil
}

// Download saves the MSI of r in dir and checks its size against the size
// GitHub reported. It returns the path of the file.
func Download(ctx context.Context, r Release, dir string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", r.MsiURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download failed: %s", resp.Status)
	}
	name := filepath.Base(r.MsiName)
	if name == "." || name == string(filepath.Separator) || !strings.HasSuffix(strings.ToLower(name), ".msi") {
		name = "helpbuoy-update.msi"
	}
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	n, err := io.Copy(f, io.LimitReader(resp.Body, downloadLimit))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && r.MsiSize > 0 && n != r.MsiSize {
		err = fmt.Errorf("download incomplete: %d of %d bytes", n, r.MsiSize)
	}
	if err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}
