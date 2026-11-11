package plugin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The Discover list is asked of the built file first, so a desktop that
// can't reach GitHub still has it (River_WLan on X): with GitHub's API
// refused, the list still comes, in stars order, with what the file kept
// and nothing it made up.
func TestTaggedReposFromTheBuiltList(t *testing.T) {
	file := func(at time.Time, ps ...Tagged) []byte {
		b, _ := json.Marshal(catalogFile{Version: 1, Generated: at.UTC().Format(time.RFC3339), Plugins: ps})
		return b
	}
	good := []Tagged{
		{Spec: "github:a/few", Repo: "a/few", Package: "few", Stars: 2, Icon: "data:image/svg+xml;base64,AAAA"},
		{Spec: "github:b/many", Repo: "b/many", Package: "many", Stars: 90, Kind: "page"},
		{Spec: "mine", Repo: "c/mine", Package: "mine", Stars: 5},
		{Spec: "https://evil.example/x.tgz", Repo: "d/evil", Package: "evil", Stars: 999},
		{Spec: "github:e/odd", Repo: "e/odd", Package: "odd", Kind: "shell", Stars: 999},
		{Spec: "github:f/bad", Repo: "f/bad", Package: "Not A Name", Stars: 999},
	}
	body := file(time.Now(), good...)
	var asked atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		w.Write(body)
	}))
	defer srv.Close()
	var gh atomic.Int32
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gh.Add(1)
		http.Error(w, "unreachable", 503)
	}))
	defer hub.Close()

	api, raw, urls := githubAPI, githubRaw, catalogURLs
	githubAPI, githubRaw, catalogURLs = hub.URL, hub.URL+"/raw", []string{srv.URL}
	t.Cleanup(func() {
		githubAPI, githubRaw, catalogURLs = api, raw, urls
		taggedMu.Lock()
		taggedL = nil
		taggedMu.Unlock()
		_ = os.Remove(taggedCache())
	})
	forget := func() {
		taggedMu.Lock()
		taggedL = nil
		taggedMu.Unlock()
		_ = os.Remove(taggedCache())
	}

	forget()
	l, err := TaggedRepos(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, x := range l {
		got = append(got, x.Repo)
	}
	if strings.Join(got, " ") != "b/many c/mine a/few" {
		t.Fatalf("listed %v", got)
	}
	if l[2].Icon == "" {
		t.Error("the icon the file kept was dropped")
	}
	if gh.Load() != 0 {
		t.Fatalf("GitHub was asked %d times with the list at hand", gh.Load())
	}

	// the file stopped being built: GitHub is asked after all (and its
	// refusal is what the page says)
	forget()
	body = file(time.Now().Add(-catalogMaxAge-time.Hour), good...)
	if _, err := TaggedRepos(context.Background()); err == nil || gh.Load() == 0 {
		t.Fatalf("a list from days ago stood: err %v, GitHub asked %d", err, gh.Load())
	}

	// the address down: the next one answers
	forget()
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(file(time.Now(), good...)) }))
	defer srv2.Close()
	catalogURLs = []string{"http://127.0.0.1:1", srv2.URL}
	if l, err := TaggedRepos(context.Background()); err != nil || len(l) != 3 {
		t.Fatalf("second address: %d, %v", len(l), err)
	}

	// off keeps to GitHub
	forget()
	t.Setenv("MAGPIE_PLUGIN_CATALOG", "off")
	before := gh.Load()
	if _, err := TaggedRepos(context.Background()); err == nil || gh.Load() == before {
		t.Fatalf("off still read the list: err %v", err)
	}
}
