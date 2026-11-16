package plugin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// A page whose page talks to a local service of its own ships that
// service as its main (magpie.page.service). The GitHub list read a
// package with a main as a provider, so xiaopeng66/magpie-model-lab
// showed under Providers (plugins#1226). A page that says
// "magpie-page" in its keywords, as every page in the plugins
// repository does, is a page, main or not; the file looked for is still
// its main. A provider with a page of settings beside it says no such
// keyword and stays a provider. The package.json is model-lab's, as it
// is, without its 900KB icon.
func TestTaggedPageWithMain(t *testing.T) {
	var mu sync.Mutex
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		mu.Lock()
		asked = append(asked, q.URL.Path)
		mu.Unlock()
		name, ok := strings.CutPrefix(q.URL.Path, "/raw/")
		f, err := os.ReadFile(filepath.Join("testdata/github/raw", filepath.FromSlash(name)))
		if !ok || err != nil {
			http.Error(w, `"Not Found"`, 404)
			return
		}
		w.Write(f)
	}))
	t.Cleanup(srv.Close)
	api, raw, reg := githubAPI, githubRaw, npmRegistry
	githubAPI, githubRaw, npmRegistry = srv.URL, srv.URL+"/raw", srv.URL+"/npm"
	t.Cleanup(func() { githubAPI, githubRaw, npmRegistry = api, raw, reg })

	const repo = "xiaopeng66/magpie-model-lab"
	got, keep, known, err := readTagged(context.Background(), Tagged{Spec: "github:" + repo, Repo: repo}, "main")
	if !keep || !known || err != nil || got.Kind != "page" || got.Package != "magpie-pelican-bench" {
		t.Fatalf("a page with a main: kind %q, package %q, kept %v, known %v, %v", got.Kind, got.Package, keep, known, err)
	}
	mu.Lock()
	a := slices.Clone(asked)
	mu.Unlock()
	if !slices.Contains(a, "/raw/"+repo+"/main/index.mjs") {
		t.Fatalf("its main wasn't looked for: asked %v", a)
	}

	got, keep, _, err = readTagged(context.Background(), Tagged{Spec: "github:someone/provider-with-page", Repo: "someone/provider-with-page"}, "main")
	if !keep || err != nil || got.Kind == "page" {
		t.Fatalf("a provider with a settings page: kind %q, kept %v, %v", got.Kind, keep, err)
	}

	// a page alone, no main, as before
	got, keep, _, err = readTagged(context.Background(), Tagged{Spec: "github:someone/page-glance", Repo: "someone/page-glance"}, "main")
	if !keep || err != nil || got.Kind != "page" {
		t.Fatalf("a page alone: %+v, kept %v, %v", got, keep, err)
	}
}
