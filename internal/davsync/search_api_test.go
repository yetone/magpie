package davsync

import (
	"testing"

	"github.com/yetone/magpie/internal/backup"
	"github.com/yetone/magpie/internal/provider"
)

// The search APIs (#419) sync with the providers: sent without keys, the
// server's keys stay; a change to them is a change to the providers; with
// none, the providers' hash is what it was before them.
func TestSyncSearchAPIs(t *testing.T) {
	server := backup.Bundle{Keys: true, Searches: &[]provider.SearchAPI{{Vendor: "tavily", Key: "tvly-k"}}}
	sent := backup.Bundle{Searches: &[]provider.SearchAPI{{Vendor: "tavily"}, {Vendor: "brave"}}}
	take(&server, sent, "providers")
	if s := *server.Searches; len(s) != 2 || s[0].Key != "tvly-k" || s[1].Key != "" {
		t.Fatalf("taken %+v", s)
	}

	before := backup.Bundle{Providers: []provider.Provider{{ID: "a"}}}
	none := before
	none.Searches = &[]provider.SearchAPI{}
	if hashes(before)["providers"] != hashes(none)["providers"] {
		t.Error("no search API changed the providers' hash")
	}
	one := before
	one.Searches = &[]provider.SearchAPI{{Vendor: "exa", Key: "k"}}
	if hashes(before)["providers"] == hashes(one)["providers"] {
		t.Error("a search API isn't a change")
	}

	// brought from a magpie before them, the ones here stay
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := provider.SetSearchAPI(provider.SearchAPI{Vendor: "exa", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	if err := bring(backup.Bundle{}, "providers"); err != nil {
		t.Fatal(err)
	}
	if len(provider.StoredSearchAPIs()) != 1 {
		t.Fatal("an older magpie's providers took them away")
	}
	if err := bring(backup.Bundle{Searches: &[]provider.SearchAPI{}}, "providers"); err != nil {
		t.Fatal(err)
	}
	if len(provider.StoredSearchAPIs()) != 0 {
		t.Fatal("removed elsewhere, kept here")
	}
}
