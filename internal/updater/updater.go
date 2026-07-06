// Package updater checks GitHub Releases for newer versions of the app and
// downloads the Windows installer so the tray can apply updates.
package updater

import (
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

const (
	defaultOwner   = "kwkgaya"
	defaultRepo    = "timereporting-assistant"
	defaultAPIBase = "https://api.github.com"
)

// Release describes a GitHub release and its installer asset.
type Release struct {
	TagName    string
	Prerelease bool
	AssetName  string // installer asset file name
	AssetURL   string // browser_download_url of the installer asset
}

// Checker queries the GitHub Releases API.
type Checker struct {
	Owner      string
	Repo       string
	APIBase    string
	HTTPClient *http.Client
}

// New returns a Checker pointing at the app's public repository.
func New() *Checker {
	return &Checker{
		Owner:      defaultOwner,
		Repo:       defaultRepo,
		APIBase:    defaultAPIBase,
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// list fetches all releases (newest first) from the GitHub API.
func (c *Checker) list() ([]Release, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/releases?per_page=30", c.APIBase, c.Owner, c.Repo)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("github releases: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var wire []struct {
		TagName    string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
		Assets     []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return nil, err
	}
	out := make([]Release, 0, len(wire))
	for _, w := range wire {
		if w.Draft {
			continue
		}
		name, dl := installerAsset(w.Assets)
		out = append(out, Release{
			TagName:    w.TagName,
			Prerelease: w.Prerelease,
			AssetName:  name,
			AssetURL:   dl,
		})
	}
	return out, nil
}

// installerAsset picks the Windows Setup .exe asset from a release's assets.
func installerAsset(assets []struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}) (string, string) {
	for _, a := range assets {
		lower := strings.ToLower(a.Name)
		if strings.Contains(lower, "setup") && strings.HasSuffix(lower, ".exe") {
			return a.Name, a.URL
		}
	}
	return "", ""
}

// Latest returns the newest release strictly newer than currentVersion, or nil
// when already up to date. includePrerelease controls whether prerelease
// (beta) versions are considered.
func (c *Checker) Latest(currentVersion string, includePrerelease bool) (*Release, error) {
	releases, err := c.list()
	if err != nil {
		return nil, err
	}
	cur, curOK := parseSemver(currentVersion)
	var best *Release
	var bestV semver
	for i := range releases {
		r := releases[i]
		if r.Prerelease && !includePrerelease {
			continue
		}
		if r.AssetURL == "" {
			continue // no installer to apply
		}
		v, ok := parseSemver(r.TagName)
		if !ok {
			continue
		}
		if curOK && !v.greater(cur) {
			continue
		}
		if best == nil || v.greater(bestV) {
			rr := releases[i]
			best = &rr
			bestV = v
		}
	}
	return best, nil
}

// Download fetches the release's installer asset into destDir and returns the
// saved file path.
func (c *Checker) Download(r *Release, destDir string) (string, error) {
	if r == nil || r.AssetURL == "" {
		return "", fmt.Errorf("no installer asset to download")
	}
	if err := os.MkdirAll(destDir, 0o700); err != nil {
		return "", err
	}
	name := r.AssetName
	if name == "" {
		name = "TimereportingAssistant-Setup.exe"
	}
	dest := filepath.Join(destDir, name)

	req, err := http.NewRequest(http.MethodGet, r.AssetURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("download %s: status %d", r.AssetURL, resp.StatusCode)
	}
	f, err := os.Create(dest)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return dest, nil
}

// semver is a minimal semantic version for comparing release tags.
type semver struct {
	major, minor, patch int
	pre                 string // prerelease suffix without the leading '-'
}

// parseSemver parses tags like "v0.11.0" or "v0.11.0-beta.1". Build metadata
// (+...) is ignored. Returns ok=false for unparseable input (e.g. "dev").
func parseSemver(s string) (semver, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if s == "" {
		return semver{}, false
	}
	if i := strings.IndexByte(s, '+'); i >= 0 { // strip build metadata
		s = s[:i]
	}
	var pre string
	if i := strings.IndexByte(s, '-'); i >= 0 {
		pre = s[i+1:]
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	var nums [3]int
	for i := 0; i < 3; i++ {
		if i >= len(parts) {
			break
		}
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			return semver{}, false
		}
		nums[i] = n
	}
	return semver{nums[0], nums[1], nums[2], pre}, true
}

// greater reports whether a is a newer version than b.
func (a semver) greater(b semver) bool {
	if a.major != b.major {
		return a.major > b.major
	}
	if a.minor != b.minor {
		return a.minor > b.minor
	}
	if a.patch != b.patch {
		return a.patch > b.patch
	}
	// Same core version: a release (no prerelease) outranks a prerelease.
	if a.pre == b.pre {
		return false
	}
	if a.pre == "" {
		return true
	}
	if b.pre == "" {
		return false
	}
	return a.pre > b.pre // both prerelease: lexical (beta.2 > beta.1)
}
