package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// Workers AI's decision models are each tested from their right-click as
// System One's are (ARNO on Discord: cloudflare-jev's models couldn't be):
// Jev is asked with the question as the input of a run of it, a Clef with
// the question as it is, at its own path, and each one's answer is read
// out of Cloudflare's envelope.
func TestCloudflareDecideModelsTested(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	asked := map[string]map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q map[string]any
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &q)
		asked[r.URL.Path] = q
		if r.Header.Get("Authorization") != "Bearer k" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"result":{"answers":{"ok":{"type":"noul","noul":0.9}}},"success":true,"errors":[],"messages":[]}`))
	}))
	defer srv.Close()
	p, err := FromPreset("cloudflare-jev")
	if err != nil {
		t.Fatal(err)
	}
	p.Key, p.Decide = "k", srv.URL+"/client/v4/accounts/acc7/ai/run"
	if p.ModelTest() != "" || !p.AsksDecideModels() {
		t.Fatalf("Workers AI's models can't be tested: %q", p.ModelTest())
	}
	rs := p.TestModels(context.Background(), []string{CloudflareJev, CloudflareClefModel})
	for _, r := range rs {
		if !r.OK {
			t.Fatalf("%s: %s", r.Model, r.Error)
		}
	}
	jev := asked["/client/v4/accounts/acc7/ai/run"]
	if in, _ := jev["input"].(map[string]any); jev["model"] != CloudflareJev || in == nil || in["questions"] == nil || in["model"] != nil {
		t.Errorf("Jev was asked %v", jev)
	}
	clef := asked["/client/v4/accounts/acc7/ai/run/@cf/cloudflare/clef"]
	if clef["model"] != "clef" || clef["questions"] == nil || clef["input"] != nil {
		t.Errorf("Clef was asked %v", clef)
	}

	// a key Cloudflare refuses fails the test
	p.Key = "bad"
	if r := p.TestModels(context.Background(), []string{CloudflareClefModel})[0]; r.OK {
		t.Fatal("a refused key passed")
	}
}
