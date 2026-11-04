package plugin

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A plugin's search hook (testdata/search): the host lists the APIs with an
// id magpie can use and an execute, leaves out the rest, and notes them for
// the provider package, which reads the note without starting the host and
// only for plugins that are on. A search is run with the user's key and
// address, its results read from a list or {results}, a throw with a
// status is a refused key, and one that never answers is ended.
func TestPluginSearch(t *testing.T) {
	sandbox(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("testdata/search")
	if _, err := Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	loaded, err := Plugins(ctx)
	if err != nil || len(loaded) != 1 {
		t.Fatalf("%v %v", loaded, err)
	}
	var ids []string
	for _, d := range loaded[0].Searches {
		ids = append(ids, d.ID)
	}
	if strings.Join(ids, " ") != "fake own" {
		t.Fatalf("searches = %+v", loaded[0].Searches)
	}
	if d := loaded[0].Searches[0]; d.Name != "Fake Search" || d.KeysURL != "https://example.com/keys" || d.KeyOptional || d.NeedURL {
		t.Errorf("fake = %+v", d)
	}
	if d := loaded[0].Searches[1]; !d.NeedURL {
		t.Errorf("own = %+v", d)
	}

	ns, err := NotedSearches()
	if err != nil || len(ns) != 2 || ns[0].VendorID() != ShortName(Name(abs))+":fake" || ns[0].Spec != abs {
		t.Fatalf("noted = %+v, %v", ns, err)
	}

	pages, err := RunSearch(ctx, abs, "fake", "go", 6, "k1", "http://x")
	if err != nil || len(pages) != 2 || pages[0].Title != "T go" || pages[0].URL != "https://example.com/6" ||
		pages[0].Text != "key k1 url http://x" || pages[0].Age != "2026" {
		t.Errorf("pages = %+v, %v", pages, err)
	}
	_, err = RunSearch(ctx, abs, "fake", "go", 6, "spent", "")
	var r SearchRefused
	if !errors.As(err, &r) || r.Status != 429 || r.RetryAfter != "30" || !strings.Contains(r.Msg, "quota spent") {
		t.Errorf("a refused key: %#v", err)
	}
	if _, err := RunSearch(ctx, abs, "fake", "go", 6, "boom", ""); err == nil || !strings.Contains(err.Error(), "it broke") || errors.As(err, &r) {
		t.Errorf("a failure: %v", err)
	}
	if _, err := RunSearch(ctx, abs, "fake", "go", 6, "shape", ""); err == nil || !strings.Contains(err.Error(), "neither a list") {
		t.Errorf("a wrong shape: %v", err)
	}
	if _, err := RunSearch(ctx, abs, "nothing", "go", 6, "", ""); err == nil || !strings.Contains(err.Error(), "no search API nothing") {
		t.Errorf("an unknown API: %v", err)
	}
	short, stop := context.WithTimeout(ctx, 400*time.Millisecond)
	t0 := time.Now()
	_, err = RunSearch(short, abs, "fake", "go", 6, "hang", "")
	stop()
	// the host ends it at 400ms; Go's own backstop 5s later
	if err == nil || time.Since(t0) > 4*time.Second {
		t.Errorf("a search that never answers: %v after %s", err, time.Since(t0))
	}
	if _, err := RunSearch(ctx, abs, "fake", "go", 6, "k1", ""); err != nil {
		t.Errorf("the host after a search ran out of time: %v", err)
	}

	// off: not listed
	if err := SetOff(abs, true); err != nil {
		t.Fatal(err)
	}
	if ns, err := NotedSearches(); err != nil || len(ns) != 0 {
		t.Errorf("with the plugin off: %+v, %v", ns, err)
	}
	// a note that can't be read is an error, never "none, so remove"
	if err := os.WriteFile(searchesPath(), []byte("{half"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NotedSearches(); err == nil {
		t.Error("a corrupt note counted as no searches")
	}
}
