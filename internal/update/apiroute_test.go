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
