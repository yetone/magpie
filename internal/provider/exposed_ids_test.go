package provider

import "testing"

// The quotas narrowed by the models offered build the lists of their own
// providers only: every other provider's, each account's and plugin's,
// was built on each usage read for nothing.
func TestExposedIDsBuildsOnlyTheProvidersAsked(t *testing.T) {
	isolate(t)
	for _, p := range []Provider{
		{ID: "asked", Name: "Asked", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"gemini-a"}},
		{ID: "other", Name: "Other", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1"}},
	} {
		if err := Save(p); err != nil {
			t.Fatal(err)
		}
	}
	got := exposedIDs("asked", "antigravity")
	if _, ok := got["other"]; ok || len(got) != 1 {
		t.Fatalf("built %v, want the one asked alone", got)
	}
	if !got["asked"]["gemini-a"] {
		t.Fatalf("its offered models: %v", got["asked"])
	}
}

// The tray reads usage every few minutes: the offered models of the
// providers whose quotas are narrowed are built once while nothing changes, so the catalog they
// load can be let go, and again after a change, which reaches the answer.
func TestKeptExposedIDsBuiltOncePerChange(t *testing.T) {
	isolate(t)
	keptExposed.Lock()
	keptExposed.ids = nil
	keptExposed.Unlock()
	if err := Save(Provider{ID: "asked", Name: "Asked", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"gemini-a"}}); err != nil {
		t.Fatal(err)
	}
	first := keptExposedIDs("asked", "antigravity")
	if !first["asked"]["gemini-a"] {
		t.Fatalf("offered: %v", first["asked"])
	}
	builds := exposedBuilds.Load()
	for range 5 {
		keptExposedIDs("asked", "antigravity")
	}
	if n := exposedBuilds.Load() - builds; n != 0 {
		t.Fatalf("built %d more times while nothing changed", n)
	}
	if err := Save(Provider{ID: "asked", Name: "Asked", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"gemini-b"}}); err != nil {
		t.Fatal(err)
	}
	got := keptExposedIDs("asked", "antigravity")
	if !got["asked"]["gemini-b"] || got["asked"]["gemini-a"] {
		t.Fatalf("after the change: %v", got["asked"])
	}
}
