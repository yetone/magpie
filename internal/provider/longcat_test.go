package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// LongCat (hoobnn on Discord) is a vendor preset: Meituan's own models at
// api.longcat.chat, chat completions and Responses under /openai/v1 and
// Anthropic messages under /anthropic, as its API docs and its CC Switch
// page give them, and an entry another app has there is the preset.
func TestLongCatPreset(t *testing.T) {
	p, err := FromPreset("longcat")
	if err != nil {
		t.Fatal(err)
	}
	if p.Chat != "https://api.longcat.chat/openai/v1" || p.Responses != "https://api.longcat.chat/openai/v1" || p.Anthropic != "https://api.longcat.chat/anthropic" {
		t.Fatalf("endpoints: %q %q %q", p.Chat, p.Responses, p.Anthropic)
	}
	if p.Name != "LongCat" || p.Icon != "longcat" || p.Catalog != "longcat" || p.KeysURL != "https://longcat.chat/platform/api_keys" {
		t.Fatalf("preset: %+v", p)
	}
	if pr := Preset("longcat"); pr.Kind != KindVendor || pr.Hosts || pr.NoKey {
		t.Fatalf("kind %q hosts %v noKey %v", pr.Kind, pr.Hosts, pr.NoKey)
	}
	// CC Switch's Claude Code entry (ANTHROPIC_BASE_URL) and its Codex one
	// (base_url), as LongCat's cc-switch page shows them
	for _, e := range []endpoints{{anthropic: "https://api.longcat.chat/anthropic"}, {responses: "https://api.longcat.chat/openai/v1"}} {
		im, why := imported("Longcat", "k", e, nil)
		if why != "" || im.Preset != "longcat" || im.Icon != "longcat" || im.Anthropic != "https://api.longcat.chat/anthropic" {
			t.Fatalf("imported %+v: %q %+v", e, why, im)
		}
	}
	// OpenCode keeps models.dev's provider id, longcat, in auth.json
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(path, []byte(`{"longcat": {"type": "api", "key": "lc-key"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	ims, err := readOpenCodeAuth(path)
	if err != nil || len(ims) != 1 || ims[0].Skip != "" || ims[0].Provider.Preset != "longcat" || ims[0].Provider.Key != "lc-key" {
		t.Fatalf("OpenCode import: %v %+v", err, ims)
	}
}

// Its list, /openai/v1/models with the key as a Bearer, as the docs'
// example answers it.
func TestLongCatList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/openai/v1/models" || r.Header.Get("Authorization") != "Bearer k" {
			rw.WriteHeader(http.StatusUnauthorized)
			rw.Write([]byte(`{"error":{"code":"invalid_api_key","message":"missing_api_key","type":"authentication_error"}}`))
			return
		}
		rw.Write([]byte(`{ "object" : "list" , "data" : [ { "id" : "LongCat-2.5" , "object" : "model" , "owned_by" : "LongCat" } ] }`))
	}))
	defer srv.Close()
	ms, _, err := catalog.FetchAt(context.Background(), srv.URL+"/openai/v1", "k", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 || ms[0].ID != "LongCat-2.5" {
		t.Fatalf("%+v", ms)
	}
}
