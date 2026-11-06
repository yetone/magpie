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
