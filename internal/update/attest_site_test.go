package update

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// The fixture is usemagpie.ai/files/v0.1.3986/attestations.jsonl as the
// site served it on 2026-11-13, byte for byte: one bundle whose statement
// names all 17 files of the release.
const (
	siteTag    = "v0.1.3986"
	siteDigest = "55036e99a685f7ffa844124a4f5ded0a2dc8bc93edeb6b3b5449eb520d8ff1ed" // magpie-cli-android-arm64
)

func siteFixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "attest", "attestations-"+siteTag+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// siteAsset is a file the feed names on the site, as the feed gives it.
func siteAsset(digest string) Asset {
	return Asset{URL: Site + "/files/" + siteTag + "/magpie-cli-android-arm64", SHA256: digest, attest: true}
}

// siteServer stands in for usemagpie.ai's /files/<tag>/attestations.jsonl:
// it answers with handler, counts the asks and points the updater at it.
func siteServer(t *testing.T, handler http.HandlerFunc) *int32 {
	t.Helper()
	var asks int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&asks, 1)
		if r.URL.Path != "/files/"+siteTag+"/attestations.jsonl" {
			t.Errorf("asked the site for %s", r.URL.Path)
		}
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	old := attestSite
	attestSite = srv.URL
	t.Cleanup(func() { attestSite = old })
	return &asks
}

func serves(body []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { w.Write(body) }
}

func withSource(t *testing.T, src string) {
	t.Helper()
	before := settings.Load()
	s := before
	s.UpdateSource = src
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { settings.Save(before) })
}

// tamperedLines flips one byte of the signature of every bundle in jsonl.
func tamperedLines(t *testing.T, jsonl []byte) []byte {
	t.Helper()
	var out [][]byte
	for _, line := range bytes.Split(bytes.TrimSpace(jsonl), []byte("\n")) {
		var b map[string]any
		if err := json.Unmarshal(line, &b); err != nil {
			t.Fatal(err)
		}
		sig := b["dsseEnvelope"].(map[string]any)["signatures"].([]any)[0].(map[string]any)
		s := sig["sig"].(string)
		flip := byte('A')
		if s[10] == 'A' {
			flip = 'B'
		}
		sig["sig"] = s[:10] + string(flip) + s[11:]
		l, _ := json.Marshal(b)
		out = append(out, l)
	}
	return append(bytes.Join(out, []byte("\n")), '\n')
}

// A file from the site has its bundles from the site: GitHub's API isn't
// asked, and the check is the same one (root, identity, digest).
func TestAttestationFromTheSite(t *testing.T) {
	gh := attestServer(t, 404, nil)
	site := siteServer(t, serves(siteFixture(t)))
	if err := attestedFile(context.Background(), siteAsset(siteDigest), "magpie-cli-android-arm64"); err != nil {
		t.Fatal(err)
	}
	if n := atomic.LoadInt32(site); n != 1 {
		t.Fatalf("asked the site %d times", n)
	}
	if n := atomic.LoadInt32(gh); n != 0 {
		t.Fatalf("asked GitHub %d times", n)
	}
	// Settings' update source the site alone: attested now, where it was
	// unknown before
	withSource(t, settings.UpdateSourceSite)
	buf := logged(t)
	if err := attestation(context.Background(), siteAsset(siteDigest)); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 || atomic.LoadInt32(gh) != 0 {
		t.Fatalf("log %q, GitHub asked %d", buf.String(), *gh)
	}
}

// The site's bundles attest other bytes: refused, as GitHub's would be.
func TestAttestationFromTheSiteOfAnotherDigestRefused(t *testing.T) {
	attestServer(t, 404, nil)
	siteServer(t, serves(siteFixture(t)))
	if err := attestation(context.Background(), siteAsset(otherDigest)); !errors.Is(err, errUnattested) {
		t.Fatalf("got %v", err)
	}
}

// The site has no bundles for the tag: GitHub's API is asked, as before.
// A tag pruned from R2 is a 302 to GitHub's release, which has no such
// asset; the redirect is not followed.
func TestAttestationSiteMissGoesToGitHub(t *testing.T) {
	var followed int32
	gone := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&followed, 1)
		http.NotFound(w, r)
	}))
	defer gone.Close()
	for name, h := range map[string]http.HandlerFunc{
		"pruned, 302 to GitHub": func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, gone.URL+"/yetone/magpie-releases/releases/download/"+siteTag+"/attestations.jsonl", http.StatusFound)
		},
		"404":          func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) },
		"502":          func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(502) },
		"html":         serves([]byte("<html>captive portal</html>")),
		"empty":        serves(nil),
		"cut mid-line": serves(siteFixture(t)[:5000]),
	} {
		gh := attestServer(t, 200, attestFixture(t))
		site := siteServer(t, h)
		a := siteAsset(attestedSums)
		if err := attestedFile(context.Background(), a, "f"); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if atomic.LoadInt32(site) != 1 || atomic.LoadInt32(gh) != 1 {
			t.Errorf("%s: site asked %d, GitHub %d", name, *site, *gh)
		}
	}
	// nothing listening at the site
	gh := attestServer(t, 200, attestFixture(t))
	srv := httptest.NewServer(http.NotFoundHandler())
	attestSite = srv.URL
	srv.Close()
	if err := attestation(context.Background(), siteAsset(attestedSums)); err != nil || atomic.LoadInt32(gh) != 1 {
		t.Fatalf("site down: %v, GitHub asked %d", err, *gh)
	}
	if n := atomic.LoadInt32(&followed); n != 0 {
		t.Fatalf("the redirect was followed %d times", n)
	}
}

// A miss at the site with Settings' update source the site alone is
// unknown, as it was: GitHub is never asked.
func TestAttestationSiteMissSiteAloneUnknown(t *testing.T) {
	gh := attestServer(t, 200, attestFixture(t))
	siteServer(t, func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	withSource(t, settings.UpdateSourceSite)
	var u errUnknown
	if err := attestation(context.Background(), siteAsset(siteDigest)); !errors.As(err, &u) {
		t.Fatalf("got %v", err)
	}
	if n := atomic.LoadInt32(gh); n != 0 {
		t.Fatalf("asked GitHub %d times", n)
	}
}

// A tampered bundle from the site is never taken. GitHub's API is asked;
// when it can't be read, or is not to be asked, that is a refusal, not
// unknown; when it has a valid bundle, that one holds (the site is only
// transport).
func TestAttestationSiteTamperedRefused(t *testing.T) {
	bad := tamperedLines(t, siteFixture(t))
	for name, c := range map[string]struct {
		code int
		body []byte
		src  string
	}{
		"GitHub rate limited": {403, []byte(`{"message":"API rate limit exceeded"}`), ""},
		"GitHub 503":          {503, nil, ""},
		"GitHub has none":     {404, nil, ""},
		"site alone":          {200, attestFixture(t), settings.UpdateSourceSite},
	} {
		attestServer(t, c.code, c.body)
		siteServer(t, serves(bad))
		withSource(t, c.src)
		err := attestedFile(context.Background(), siteAsset(siteDigest), "magpie-cli-android-arm64")
		if !errors.Is(err, errUnattested) || !strings.Contains(err.Error(), "not installed") {
			t.Errorf("%s: got %v", name, err)
		}
	}
	withSource(t, "")
	// GitHub's own valid bundle for these bytes (v0.1.3925's SHA256SUMS)
	attestServer(t, 200, attestFixture(t))
	siteServer(t, serves(tamperedLines(t, siteFixture(t))))
	buf := logged(t)
	if err := attestation(context.Background(), siteAsset(attestedSums)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "GitHub's held") {
		t.Fatalf("not said: %q", buf.String())
	}
}

// The site's bundles must show the same identity as GitHub's.
func TestAttestationFromTheSiteWrongIdentityRefused(t *testing.T) {
	attestServer(t, 503, nil)
	siteServer(t, serves(siteFixture(t)))
	old := attestID
	t.Cleanup(func() { attestID = old })
	attestID.SAN = `^https://github\.com/someone/else/\.github/workflows/release\.yml@refs/heads/main$`
	if err := attestation(context.Background(), siteAsset(siteDigest)); !errors.Is(err, errUnattested) {
		t.Fatalf("got %v", err)
	}
}

// Only a file from the site has its bundles asked there: Settings' update
// source GitHub downloads from github.com, and a file the feed names on
// GitHub (the feed's fallback) is GitHub's.
func TestAttestationSiteOnlyForTheSitesFiles(t *testing.T) {
	gh := attestServer(t, 200, attestFixture(t))
	site := siteServer(t, serves(siteFixture(t)))
	withSource(t, settings.UpdateSourceGitHub)
	if err := attestation(context.Background(), siteAsset(attestedSums)); err != nil {
		t.Fatal(err)
	}
	withSource(t, "")
	a := Asset{URL: githubWeb + "/" + releaseRepo + "/releases/download/" + siteTag + "/f", SHA256: attestedSums}
	if err := attestation(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(site) != 0 || atomic.LoadInt32(gh) != 2 {
		t.Fatalf("site asked %d, GitHub %d", *site, *gh)
	}
	for u, want := range map[string]string{
		Site + "/files/v0.1.3986/magpie-darwin-arm64.zip": attestSite + "/files/v0.1.3986/attestations.jsonl",
		Site + "/files/v0.1.3986":                         "",
		Site + "/dl/v0.1.3986/f":                          "",
		"https://example.com/files/v0.1.3986/f":           "",
	} {
		if got := siteBundlesURL(Asset{URL: u}); got != want {
			t.Errorf("%s: %q, want %q", u, got, want)
		}
	}
}
