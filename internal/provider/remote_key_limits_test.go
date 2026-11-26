package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// A magpie from before #2227 answers with "limit" alone: its card is
// that one limit's windows, as before.
func TestKeyLimitCardOneLimit(t *testing.T) {
	var a remoteKeyAnswer
	if err := json.Unmarshal([]byte(`{"limited":true,"key":"Laptop","limit":{"period":"day","tokens":350,"tokenLimit":1000,"tokensLeft":650,"cost":1.5,"costLimit":5,"costLeft":3.5}}`), &a); err != nil {
		t.Fatal(err)
	}
	g, ws := keyLimitCard(&a)
	if !g.Limited || g.Period != "day" || *g.TokensLeft != 650 || *g.CostLeft != 3.5 || len(ws) != 2 || ws[0].Name != "Tokens today" || ws[1].Name != "Spend today" || ws[1].Amount != 1.5 {
		t.Fatalf("one limit: %+v %+v", g, ws)
	}
}

// RemoteKeyFull is true only when every key a Remote magpie uses is told
// "full" by the magpie it reaches (starballl on X): a key held to
// anything, a magpie too old to say, one that refuses or isn't there,
// and a second key that isn't full are not; nor is a provider that is no
// Remote magpie.
func TestRemoteKeyFull(t *testing.T) {
	isolate(t)
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	answers := map[string]string{
		"Bearer full":  `{"limited":false,"key":"Laptop","full":true}`,
		"Bearer held":  `{"limited":false,"key":"Friend","heldTo":["models"],"models":["plan/m1"]}`,
		"Bearer old":   `{"limited":false,"key":"Laptop"}`,
		"Bearer other": `{"error":{"message":"not shared"}}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != RemoteKeyLimitPath {
			http.NotFound(w, r)
			return
		}
		a, ok := answers[r.Header.Get("Authorization")]
		if !ok || strings.Contains(a, "error") {
			w.WriteHeader(http.StatusForbidden)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, a)
	}))
	t.Cleanup(srv.Close)
	gone := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(gone.Close)
	remote := func(at string, keys ...string) Provider {
		p := Provider{ID: "rem", Name: "Rem", Preset: RemoteMagpiePreset, Key: keys[0], Chat: strings.TrimPrefix(at, "http://")}
		for _, k := range keys[1:] {
			p.Keys = append(p.Keys, KeyAccount{Key: k})
		}
		p.remoteMagpieEndpoints()
		return p
	}
	ctx := context.Background()
	if !RemoteKeyFull(ctx, remote(srv.URL, "full")) {
		t.Fatal("a full key: not full")
	}
	for what, p := range map[string]Provider{
		"a held key":            remote(srv.URL, "held"),
		"a magpie too old":      remote(srv.URL, "old"),
		"a refusal":             remote(srv.URL, "other"),
		"an unknown key":        remote(srv.URL, "nope"),
		"no limit route":        remote(gone.URL, "full"),
		"a second key not full": remote(srv.URL, "full", "held"),
		"not a Remote magpie":   {ID: "x", Name: "X", Key: "full", Chat: srv.URL + "/v1", Anthropic: srv.URL},
	} {
		if RemoteKeyFull(ctx, p) {
			t.Errorf("%s: full", what)
		}
	}
}
