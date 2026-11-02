package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
)

// ChangQiu on Discord: a provider switched off long ago was still asked.
// A key added to a provider switched off isn't given its own list: the
// vendor is asked nothing.
func TestPrefillAsksNoProviderSwitchedOff(t *testing.T) {
	prefillHome(t)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = rw.Write([]byte(groupAll))
	}))
	t.Cleanup(srv.Close)
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: srv.URL + "/v1", Key: "sk-all", Off: true,
		Keys: []KeyAccount{{Key: "sk-new"}}}); err != nil {
		t.Fatal(err)
	}
	if n := PrefillNewKeys(context.Background(), map[string]map[string]bool{}); n != 0 || hits.Load() != 0 {
		t.Fatalf("switched off: %d keys given a list, the vendor asked %d times", n, hits.Load())
	}
	// the same provider on is asked: the test can tell
	if err := SetOff("relay", false); err != nil {
		t.Fatal(err)
	}
	PrefillNewKeys(context.Background(), map[string]map[string]bool{})
	if hits.Load() == 0 {
		t.Fatal("switched on, the vendor was never asked")
	}
}

// The plugin providers a listing of the plugins asks nothing are those
// switched off in magpie, by the plugin's own id: a plugin's provider is
// magpie's as it is, or with "-plugin" taken off when the id is a
// built-in's. A built-in switched off is no plugin's.
func TestPluginsOffNamesThePluginsOwnIDs(t *testing.T) {
	prefillHome(t)
	// as providers.json keeps them: a plugin's provider has no base of its own
	b, _ := json.Marshal(file{Providers: []Provider{
		{ID: "fakeco", Name: "Fake", Off: true},
		{ID: "openai-plugin", Name: "OpenAI (plugin)", Off: true},
		{ID: "onco", Name: "On"},
		{ID: "deepseek", Name: "DeepSeek", Key: "sk-d", Off: true},
	}})
	if err := os.MkdirAll(filepath.Dir(Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), b, 0o600); err != nil {
		t.Fatal(err)
	}
	got := pluginsOff()
	slices.Sort(got)
	// "openai-plugin" is both: a plugin's own "openai-plugin" is magpie's
	// as it is, as its "openai" is magpie's "openai-plugin"
	if want := []string{"fakeco", "openai", "openai-plugin"}; !slices.Equal(got, want) {
		t.Fatalf("pluginsOff = %v, want %v", got, want)
	}
}

// Refreshing every list (the GUI's Refresh, `magpie sync`) asks no
// provider switched off, and keeps its list; one on is asked.
func TestRefreshListsAsksNoProviderSwitchedOff(t *testing.T) {
	prefillHome(t)
	var off, on atomic.Int32
	vendor := func(n *atomic.Int32) *httptest.Server {
		srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			n.Add(1)
			_, _ = rw.Write([]byte(groupAll))
		}))
		t.Cleanup(srv.Close)
		return srv
	}
	offSrv, onSrv := vendor(&off), vendor(&on)
	for _, p := range []Provider{
		{ID: "gone", Name: "Gone", Chat: offSrv.URL + "/v1", Key: "sk-gone", Off: true},
		{ID: "here", Name: "Here", Chat: onSrv.URL + "/v1", Key: "sk-here"},
	} {
		if err := Save(p); err != nil {
			t.Fatal(err)
		}
	}
	got := RefreshLists(context.Background())
	if off.Load() != 0 {
		t.Fatalf("the provider switched off was asked %d times", off.Load())
	}
	if on.Load() == 0 || !slices.Equal(got, []Listed{{"here", 3}}) {
		t.Fatalf("RefreshLists = %v, the one on asked %d times", got, on.Load())
	}
}
