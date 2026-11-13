package gateway

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// @czhhcz on X: a relay's /v1/models lists a Claude model with
// reasoning_levels: [], and `magpie model efforts` offers it the model's
// own five from models.dev. Those are what a request is fitted to: Claude
// Code's max and xhigh reach the relay as asked, with nothing for the user
// to set.
func TestRelayListingNoLevelsStillGetsTheAgentsEffort(t *testing.T) {
	fresh(t)
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	if err := os.WriteFile(catalog.CachePath(), []byte(`{"anthropic":{"id":"anthropic","models":{
	  "claude-opus-5-5":{"id":"claude-opus-5-5","reasoning":true,"reasoning_options":[{"type":"effort","values":["low","medium","high","xhigh","max"]}]}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	up := &adaptiveVendor{}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Anthropic: srv.URL}); err != nil {
		t.Fatal(err)
	}
	// the relay's list as it is read: the model, levels none
	if err := catalog.SaveLive("relay", srv.URL, []catalog.Model{{ID: "claude-opus-5-5"}}); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if es := p.Efforts("claude-opus-5-5"); !slices.Equal(es, []string{"low", "medium", "high", "xhigh", "max"}) || !p.KnownBorrowed("claude-opus-5-5") {
		t.Fatal(es)
	}
	for _, e := range []string{"max", "xhigh", "low"} {
		code, body := post(t, "/v1/messages", `{"model":"relay/claude-opus-5-5","max_tokens":32000,"thinking":{"type":"adaptive"},"output_config":{"effort":"`+e+`"},"messages":[{"role":"user","content":"hi"}]}`)
		if code != 200 {
			t.Fatalf("%s: status %d: %s", e, code, body)
		}
		oc, _ := up.last()["output_config"].(map[string]any)
		if got, _ := oc["effort"].(string); got != e {
			t.Errorf("asked %s, the relay got %v", e, up.last()["output_config"])
		}
	}
}
