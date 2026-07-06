package updater

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseSemverAndGreater(t *testing.T) {
	cases := []struct {
		a, b string
		want bool // a greater than b
	}{
		{"v0.11.0", "v0.10.9", true},
		{"v0.10.9", "v0.11.0", false},
		{"v1.0.0", "v0.99.99", true},
		{"v0.11.1", "v0.11.0", true},
		{"v0.11.0", "v0.11.0", false},
		{"v0.11.0", "v0.11.0-beta.1", true},       // release beats prerelease
		{"v0.11.0-beta.1", "v0.11.0", false},      // prerelease loses to release
		{"v0.11.0-beta.2", "v0.11.0-beta.1", true}, // later beta
	}
	for _, c := range cases {
		av, aok := parseSemver(c.a)
		bv, bok := parseSemver(c.b)
		if !aok || !bok {
			t.Fatalf("parse failed for %q/%q", c.a, c.b)
		}
		if got := av.greater(bv); got != c.want {
			t.Errorf("%s greater than %s = %v, want %v", c.a, c.b, got, c.want)
		}
	}
	if _, ok := parseSemver("dev"); ok {
		t.Error(`parseSemver("dev") should be !ok`)
	}
}

func newTestServer(t *testing.T, releases []map[string]any) *Checker {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(releases)
	}))
	t.Cleanup(ts.Close)
	c := New()
	c.APIBase = ts.URL
	return c
}

func rel(tag string, prerelease bool, withAsset bool) map[string]any {
	assets := []map[string]any{}
	if withAsset {
		assets = append(assets, map[string]any{
			"name":                 "TimereportingAssistant-Setup-" + tag + ".exe",
			"browser_download_url": "https://example.test/" + tag + "/setup.exe",
		})
	}
	return map[string]any{"tag_name": tag, "draft": false, "prerelease": prerelease, "assets": assets}
}

func TestLatestStableOnly(t *testing.T) {
	c := newTestServer(t, []map[string]any{
		rel("v0.12.0-beta.1", true, true),
		rel("v0.11.0", false, true),
		rel("v0.10.0", false, true),
	})
	got, err := c.Latest("v0.11.0", false)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("expected no stable update over v0.11.0, got %v", got.TagName)
	}
	// From an older version, the newest stable is selected (beta ignored).
	got, err = c.Latest("v0.10.0", false)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.TagName != "v0.11.0" {
		t.Fatalf("expected v0.11.0, got %v", got)
	}
}

func TestLatestIncludePrerelease(t *testing.T) {
	c := newTestServer(t, []map[string]any{
		rel("v0.12.0-beta.1", true, true),
		rel("v0.11.0", false, true),
	})
	got, err := c.Latest("v0.11.0", true)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.TagName != "v0.12.0-beta.1" {
		t.Fatalf("expected v0.12.0-beta.1, got %v", got)
	}
	if got.AssetURL == "" {
		t.Error("expected an installer asset URL")
	}
}

func TestLatestSkipsReleasesWithoutInstaller(t *testing.T) {
	c := newTestServer(t, []map[string]any{
		rel("v0.13.0", false, false), // no installer asset
		rel("v0.11.0", false, true),
	})
	got, err := c.Latest("v0.10.0", false)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.TagName != "v0.11.0" {
		t.Fatalf("expected v0.11.0 (v0.13.0 has no asset), got %v", got)
	}
}
