package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/codexcat"
)

// nianshou5 on Discord: a CLIProxyAPI (CPA) the user hosts, added as a
// custom provider, offered no reasoning effort in the ChatGPT app. CPA's
// /v1/models gives id, object, created and owned_by alone, and its
// Antigravity and xAI channels name a model for the level or speed they
// serve it at (gemini-3.8-flash-high, claude-opus-5-5-high,
// grok-3-mini-fast), which models.dev lists by no such name. The model's
// own levels are borrowed, as for any custom provider's model, and reach
// every list Codex and the ChatGPT app are handed; one whose base magpie
// doesn't know still takes none, and a vendor's own word stays its own.
func TestCPAVariantModelsBorrowLevels(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalog.CachePath(), []byte(`{
	  "google": {"models": {
	    "gemini-3.8-flash": {"id":"gemini-3.8-flash","reasoning":true,"reasoning_options":[{"type":"effort","values":["low","medium","high"]}]}}},
	  "anthropic": {"models": {
	    "claude-opus-5-5": {"id":"claude-opus-5-5","reasoning":true,"reasoning_options":[{"type":"effort","values":["low","medium","high","xhigh","max"]}]}}},
	  "xai": {"models": {
	    "grok-3-mini": {"id":"grok-3-mini","reasoning":true,"reasoning_options":[{"type":"effort","values":["low","medium","high"]}]}}},
	  "acme": {"models": {
	    "acme-1-high": {"id":"acme-1-high","reasoning":true},
	    "acme-1": {"id":"acme-1","reasoning":true,"reasoning_options":[{"type":"effort","values":["low","high"]}]}}}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)

	// CPA's /v1/models as its OpenAIModels handler writes it (router-for-
	// me/CLIProxyAPI sdk/api/handlers/openai): the four fields, nothing of
	// the thinking levels its registry has
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Write([]byte(`{"data":[` +
			`{"id":"gemini-3.8-flash-high","object":"model","owned_by":"antigravity"},` +
			`{"id":"claude-opus-5-5-high","object":"model","owned_by":"antigravity"},` +
			`{"id":"grok-3-mini-fast","object":"model","created":1740960000,"owned_by":"xai"},` +
			`{"id":"gemini-3.8-flash","object":"model","owned_by":"google"},` +
			`{"id":"grok-4.7-build-fast","object":"model","created":1776211200,"owned_by":"xai"}` +
			`],"object":"list"}`))
	}))
	defer srv.Close()
	live, err := catalog.Fetch(context.Background(), srv.URL+"/v1", "k", false, nil)
	if err != nil || len(live) != 5 {
		t.Fatalf("fetch: %v %v", live, err)
	}
	ids := []string{"gemini-3.8-flash-high", "claude-opus-5-5-high", "grok-3-mini-fast", "gemini-3.8-flash", "grok-4.7-build-fast"}
	if err := Save(Provider{ID: "cpa", Name: "CPA", Chat: srv.URL + "/v1", Key: "k", Models: ids}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("cpa", srv.URL+"/v1", live); err != nil {
		t.Fatal(err)
	}
	// acme lists acme-1-high itself, with a thinking switch alone
	if err := Save(Provider{ID: "acme", Name: "Acme", Catalog: "acme", Chat: "https://acme.test/v1", Key: "k", Models: []string{"acme-1-high"}}); err != nil {
		t.Fatal(err)
	}

	want := map[string]string{
		"cpa/gemini-3.8-flash-high": "low,medium,high",
		"cpa/claude-opus-5-5-high":  "low,medium,high,xhigh,max",
		"cpa/grok-3-mini-fast":      "low,medium,high",
		"cpa/gemini-3.8-flash":      "low,medium,high",
		"cpa/grok-4.7-build-fast":   "",
		"acme/acme-1-high":          "",
	}
	got := map[string]string{}
	for _, e := range Catalog() {
		got[e.ID] = strings.Join(e.Efforts, ",")
	}
	for id, w := range want {
		if g, ok := got[id]; !ok || g != w {
			t.Errorf("%s: efforts %q (listed %v), want %q", id, g, ok, w)
		}
	}
	p, err := Find("cpa")
	if err != nil {
		t.Fatal(err)
	}
	if g := strings.Join(p.Efforts("gemini-3.8-flash-high"), ","); g != "low,medium,high" {
		t.Errorf("the gateway fits gemini-3.8-flash-high to %q", g)
	}

	// what the Codex app and the ChatGPT app beside it are handed
	body, err := json.Marshal(codexcat.Entries(CodexListed(), 0))
	if err != nil {
		t.Fatal(err)
	}
	var entries []struct {
		Slug   string `json:"slug"`
		Levels []struct {
			Effort string `json:"effort"`
		} `json:"supported_reasoning_levels"`
	}
	if err := json.Unmarshal(body, &entries); err != nil {
		t.Fatal(err)
	}
	listed := map[string]string{}
	for _, e := range entries {
		var ls []string
		for _, l := range e.Levels {
			ls = append(ls, l.Effort)
		}
		listed[e.Slug] = strings.Join(ls, ",")
	}
	// Codex's list ends a reasoning model of another vendor with ultra
	for _, id := range []string{"cpa/gemini-3.8-flash-high", "cpa/claude-opus-5-5-high", "cpa/grok-3-mini-fast"} {
		if w := want[id] + ",ultra"; listed[id] != w {
			t.Errorf("Codex list: %s offers %q, want %q (list %v)", id, listed[id], w, listed)
		}
	}
}
