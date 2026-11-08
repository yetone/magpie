package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeGitHub is github.com and api.github.com in one server, as on
// 2026-11-08: the release's download address answers 504 on every try,
// while the API's asset address, asked for the bytes, redirects to them.
type fakeGitHub struct {
	srv                          *httptest.Server
	direct, tags, asset, blob    atomic.Int32
	body                         []byte
	served                       []byte // what the blob sends; body unless a test changes it
	directServes                 bool   // the download address sends the file instead of 504
	sum                          string
	assetWithoutOctetStreamAsked atomic.Int32
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	g := &fakeGitHub{body: []byte(strings.Repeat("magpie release ", 8192))}
	g.served = g.body
	s := sha256.Sum256(g.body)
	g.sum = hex.EncodeToString(s[:])
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/yetone/magpie-releases/releases/download/v0.1.9/magpie.zip":
			g.direct.Add(1)
			if g.directServes {
				w.Write(g.served)
				return
			}
			w.WriteHeader(http.StatusGatewayTimeout)
		case "/repos/yetone/magpie-releases/releases/tags/v0.1.9":
			g.tags.Add(1)
			fmt.Fprintf(w, `{"tag_name":"v0.1.9","assets":[{"url":"%[1]s/repos/yetone/magpie-releases/releases/assets/6","name":"SHA256SUMS"},{"url":"%[1]s/repos/yetone/magpie-releases/releases/assets/7","name":"magpie.zip","uploader":{"url":"%[1]s/users/x"}}]}`, g.srv.URL)
		case "/repos/yetone/magpie-releases/releases/assets/7":
			g.asset.Add(1)
			if r.Header.Get("Accept") != "application/octet-stream" {
				// GitHub describes the file instead
				g.assetWithoutOctetStreamAsked.Add(1)
				fmt.Fprint(w, `{"name":"magpie.zip"}`)
				return
			}
			http.Redirect(w, r, "/blob/magpie.zip?sig=x", http.StatusFound)
		case "/blob/magpie.zip":
			g.blob.Add(1)
			w.Write(g.served)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(g.srv.Close)
	oldWeb, oldAPI := githubWeb, githubAPI
	githubWeb, githubAPI = g.srv.URL, g.srv.URL
	t.Cleanup(func() { githubWeb, githubAPI = oldWeb, oldAPI })
	quickRetries(t)
	return g
}

func (g *fakeGitHub) asset0() Asset {
	return Asset{URL: g.srv.URL + "/yetone/magpie-releases/releases/download/v0.1.9/magpie.zip", SHA256: g.sum}
}

// github.com's download address answers 504 on every try: the download
// goes on through GitHub's API route, looked up from the release's tag,
// and the file is the release's, checked against the feed's hash.
func TestDownloadGoesThroughTheAPIWhenGitHubIsBusy(t *testing.T) {
	g := newFakeGitHub(t)
	path := filepath.Join(t.TempDir(), "magpie.zip")
	if err := download(context.Background(), g.asset0(), path); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != string(g.body) {
		t.Fatalf("file has %d bytes, want the release's %d", len(b), len(g.body))
	}
	// the address that 504s isn't asked again and again first
	if n := g.direct.Load(); n != 1 {
		t.Fatalf("download address asked %d times, want 1", n)
	}
	if g.tags.Load() != 1 || g.asset.Load() != 1 || g.blob.Load() != 1 {
		t.Fatalf("tags %d, asset %d, blob %d; want 1 each", g.tags.Load(), g.asset.Load(), g.blob.Load())
	}
	if n := g.assetWithoutOctetStreamAsked.Load(); n != 0 {
		t.Fatalf("asset asked %d times without Accept: application/octet-stream", n)
	}
}

// The feed's own API address is used as it is: no lookup of the tag,
// which costs one of GitHub's 60 anonymous asks an hour.
func TestDownloadTakesTheFeedsAPIAddress(t *testing.T) {
	g := newFakeGitHub(t)
	a := g.asset0()
	a.API = g.srv.URL + "/repos/yetone/magpie-releases/releases/assets/7"
	path := filepath.Join(t.TempDir(), "magpie.zip")
	if err := download(context.Background(), a, path); err != nil {
		t.Fatal(err)
	}
	if g.tags.Load() != 0 || g.blob.Load() != 1 {
		t.Fatalf("tags %d, blob %d; want 0 and 1", g.tags.Load(), g.blob.Load())
	}
}

// The API route is only another way to the same bytes: a file that comes
// through it changed fails its hash and is not kept.
func TestAPIRouteFileIsCheckedLikeAnyOther(t *testing.T) {
	g := newFakeGitHub(t)
	g.served = []byte(strings.Repeat("not magpie ", 8192))
	path := filepath.Join(t.TempDir(), "magpie.zip")
	err := download(context.Background(), g.asset0(), path)
	if err == nil || !strings.Contains(err.Error(), "does not match its checksum") {
		t.Fatalf("err = %v, want a checksum failure", err)
	}
	if _, serr := os.Stat(path); !os.IsNotExist(serr) {
		t.Fatalf("a file that failed its hash was kept: %v", serr)
	}
	if g.blob.Load() == 0 {
		t.Fatal("the API route was never tried")
	}
}

// A mirror the user gave keeps precedence: the download goes through it
// alone, and GitHub's API is never asked.
func TestMirrorKeepsPrecedenceOverTheAPIRoute(t *testing.T) {
	g := newFakeGitHub(t)
	ctx := WithMirror(context.Background(), "https://mirror.example/")
	err := download(ctx, g.asset0(), filepath.Join(t.TempDir(), "magpie.zip"))
	if err == nil {
		t.Fatal("download went through")
	}
	if g.tags.Load() != 0 || g.asset.Load() != 0 {
		t.Fatalf("GitHub's API asked through a mirror: tags %d, asset %d", g.tags.Load(), g.asset.Load())
	}
	if n := g.direct.Load(); n != int32(len(retryWaits)+1) {
		t.Fatalf("download address asked %d times, want every try (%d)", n, len(retryWaits)+1)
	}
}

// site is usemagpie.ai's /files, the release's copy on R2, answering
// status on every try; it counts the asks.
func site(t *testing.T, status int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

// onR2 is the feed's asset when the site holds the file: the site's
// address, github.com's beside it.
func (g *fakeGitHub) onR2(siteURL string) Asset {
	a := g.asset0()
	a.GitHub, a.URL = a.URL, siteURL+"/files/v0.1.9/magpie.zip"
	return a
}

// The site's copy failing, the download goes on from github.com's own
// address: once, not after every try of the site's.
func TestDownloadGoesToGitHubWhenTheSiteIsBusy(t *testing.T) {
	g := newFakeGitHub(t)
	g.directServes = true
	s, asked := site(t, http.StatusServiceUnavailable)
	path := filepath.Join(t.TempDir(), "magpie.zip")
	if err := download(context.Background(), g.onR2(s.URL), path); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != string(g.body) {
		t.Fatalf("file has %d bytes, want the release's %d", len(b), len(g.body))
	}
	if asked.Load() != 1 || g.direct.Load() != 1 || g.tags.Load() != 0 {
		t.Fatalf("site %d, github.com %d, API %d; want 1, 1, 0", asked.Load(), g.direct.Load(), g.tags.Load())
	}
}

// The site and github.com both busy: GitHub's API route, looked up from
// the github.com address (the site's says nothing of the release).
func TestDownloadGoesOnToTheAPIFromTheSite(t *testing.T) {
	g := newFakeGitHub(t)
	s, asked := site(t, http.StatusGatewayTimeout)
	path := filepath.Join(t.TempDir(), "magpie.zip")
	if err := download(context.Background(), g.onR2(s.URL), path); err != nil {
		t.Fatal(err)
	}
	if asked.Load() != 1 || g.direct.Load() != 1 || g.tags.Load() != 1 || g.blob.Load() != 1 {
		t.Fatalf("site %d, github.com %d, tags %d, blob %d; want 1 each", asked.Load(), g.direct.Load(), g.tags.Load(), g.blob.Load())
	}
}

// Every route failing, the error names each, the site's first.
func TestDownloadSaysEachRouteThatFailed(t *testing.T) {
	g := newFakeGitHub(t)
	g.served = []byte("not magpie")
	s, _ := site(t, http.StatusGatewayTimeout)
	host := strings.TrimPrefix(s.URL, "http://")
	err := download(context.Background(), g.onR2(s.URL), filepath.Join(t.TempDir(), "magpie.zip"))
	if err == nil {
		t.Fatal("download went through")
	}
	msg := err.Error()
	for _, want := range []string{"from " + host, "through api.github.com", "does not match its checksum"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("err = %q, want it to say %q", msg, want)
		}
	}
	if strings.Index(msg, host) > strings.Index(msg, "through api.github.com") {
		t.Fatalf("err = %q: the site's failure isn't first", msg)
	}
}

// A file on the site is not put behind the user's mirror (it is no
// GitHub file); github.com's address after it is, and the API is never
// asked through a mirror.
func TestMirrorTakesTheGitHubAddressAfterTheSite(t *testing.T) {
	g := newFakeGitHub(t)
	s, asked := site(t, http.StatusBadGateway)
	var mirrored atomic.Int32
	m := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mirrored.Add(1)
		w.WriteHeader(http.StatusGatewayTimeout)
	}))
	t.Cleanup(m.Close)
	oldWeb := githubWeb
	t.Cleanup(func() { githubWeb = oldWeb })
	a := g.onR2(s.URL)
	// the github.com address as the feed gives it, for mirrored to know
	a.GitHub = "https://github.com/yetone/magpie-releases/releases/download/v0.1.9/magpie.zip"
	ctx := WithMirror(context.Background(), m.URL+"/")
	if err := download(ctx, a, filepath.Join(t.TempDir(), "magpie.zip")); err == nil {
		t.Fatal("download went through")
	}
	if asked.Load() != 1 {
		t.Fatalf("site asked %d times, want 1", asked.Load())
	}
	if n := mirrored.Load(); n != int32(len(retryWaits)) {
		t.Fatalf("mirror asked %d times, want the tries after the site's (%d)", n, len(retryWaits))
	}
	if g.tags.Load() != 0 || g.asset.Load() != 0 {
		t.Fatalf("GitHub's API asked through a mirror: tags %d, asset %d", g.tags.Load(), g.asset.Load())
	}
}

// A connection to the site that breaks on every try (not a busy answer)
// still leaves it for github.com before the tries run out.
func TestDownloadLeavesABrokenSiteForGitHub(t *testing.T) {
	g := newFakeGitHub(t)
	g.directServes = true
	var asked atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		hj, _ := w.(http.Hijacker)
		c, _, _ := hj.Hijack()
		c.Close()
	}))
	t.Cleanup(s.Close)
	path := filepath.Join(t.TempDir(), "magpie.zip")
	if err := download(context.Background(), g.onR2(s.URL), path); err != nil {
		t.Fatal(err)
	}
	if g.direct.Load() != 1 {
		t.Fatalf("github.com asked %d times, want 1", g.direct.Load())
	}
	// with the API route still left after github.com, the site has all
	// but the last two tries
	if want := int32(len(retryWaits) - 1); asked.Load() != want {
		t.Fatalf("site asked %d times, want %d", asked.Load(), want)
	}
}
