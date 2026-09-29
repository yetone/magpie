package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Each endpoint is tried with a key made for it, and the Anthropic one
// with a model its key sees even when the exposed one is another's.
func TestTestUsesEachEndpointsKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	seen := map[string]string{} // path → key and model it got
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet { // each key sees its own group's models
			if r.Header.Get("Authorization") == "Bearer sk-anthropic" {
				_, _ = rw.Write([]byte(`{"data":[{"id":"claude-opus-5"}]}`))
			} else {
				_, _ = rw.Write([]byte(`{"data":[{"id":"gpt-6-sol"}]}`))
			}
			return
		}
		var in struct{ Model string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		seen[r.URL.Path] = r.Header.Get("Authorization") + " " + in.Model
		_, _ = rw.Write([]byte(`{}`))
	}))
	defer srv.Close()
	p := Provider{ID: "relay", Name: "Relay", Chat: srv.URL + "/v1", Anthropic: srv.URL,
		Key: "sk-chat", KeyProtocol: Chat, Models: []string{"gpt-6-sol"},
		Keys: []KeyAccount{{Key: "sk-anthropic", Protocol: Anthropic}}}
	for _, r := range p.Test(context.Background()) {
		if !r.OK {
			t.Errorf("%s: %+v", r.Protocol, r)
		}
	}
	if got := seen["/v1/chat/completions"]; got != "Bearer sk-chat gpt-6-sol" {
		t.Errorf("chat got %q", got)
	}
	if got := seen["/v1/messages"]; got != "Bearer sk-anthropic claude-opus-5" {
		t.Errorf("anthropic got %q", got)
	}
	ms := p.Available()
	if len(ms) != 2 {
		t.Fatalf("merged list %+v", ms)
	}
	chat, anth := p.first(), p.Keys[0]
	if !p.Serves(chat, "gpt-6-sol") || p.Serves(chat, "claude-opus-5") || !p.Serves(anth, "claude-opus-5") || p.Serves(anth, "gpt-6-sol") {
		t.Error("each key serves only its own list")
	}
	if !p.Serves(anth, "some-unlisted-model") {
		t.Error("a model no list has is anyone's")
	}
}

// Each model is asked on its own, with a key that sees it: one the vendor
// doesn't serve fails while the others answer.
func TestTestModels(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	var mu sync.Mutex
	seen := map[string]string{} // model → path and key it came with
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			if r.Header.Get("Authorization") == "Bearer sk-anthropic" {
				_, _ = rw.Write([]byte(`{"data":[{"id":"claude-opus-5"}]}`))
			} else {
				_, _ = rw.Write([]byte(`{"data":[{"id":"gpt-6-sol"},{"id":"gone"}]}`))
			}
			return
		}
		var in struct{ Model string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		mu.Lock()
		seen[in.Model] = r.URL.Path + " " + r.Header.Get("Authorization")
		mu.Unlock()
		if in.Model == "gone" {
			rw.WriteHeader(http.StatusNotFound)
			_, _ = rw.Write([]byte(`{"error":{"message":"no channel for model gone"}}`))
			return
		}
		_, _ = rw.Write([]byte(`{}`))
	}))
	defer srv.Close()
	p := Provider{ID: "relay", Name: "Relay", Chat: srv.URL + "/v1", Anthropic: srv.URL,
		Key: "sk-chat", KeyProtocol: Chat,
		Keys: []KeyAccount{{Key: "sk-anthropic", Protocol: Anthropic}}}
	p.Fetch(context.Background())
	rs := p.TestModels(context.Background(), []string{"gpt-6-sol", "claude-opus-5", "gone"})
	if len(rs) != 3 || !rs[0].OK || !rs[1].OK || rs[2].OK || rs[2].Status != 404 || rs[2].Error != "no channel for model gone" {
		t.Fatalf("results %+v", rs)
	}
	if rs[0].Model != "gpt-6-sol" || rs[1].Protocol != Anthropic || rs[2].Model != "gone" {
		t.Fatalf("order %+v", rs)
	}
	if seen["gpt-6-sol"] != "/v1/chat/completions Bearer sk-chat" || seen["claude-opus-5"] != "/v1/messages Bearer sk-anthropic" {
		t.Fatalf("seen %v", seen)
	}
}

// An image model is tested as the gateway draws with it, on the images
// API with one small picture — a chat request it would turn away — while a
// chat model is still asked in chat. The vendor-wide Test picks the chat one.
func TestTestModelsDraws(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	var mu sync.Mutex
	seen := map[string]string{} // model → path it was asked on
	bodies := map[string]map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			rw.WriteHeader(http.StatusNotFound)
			return
		}
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		model, _ := in["model"].(string)
		mu.Lock()
		seen[model] = r.URL.Path
		bodies[model] = in
		mu.Unlock()
		draws := strings.Contains(model, "image") || strings.Contains(model, "flux")
		switch {
		case r.URL.Path == "/v1/images/generations" && draws:
			_, _ = rw.Write([]byte(`{"data":[{"url":"https://img.example/1.png"}]}`))
		case r.URL.Path == "/v1/chat/completions" && !draws:
			_, _ = rw.Write([]byte(`{"choices":[{"message":{"content":"hi"}}]}`))
		default:
			rw.WriteHeader(http.StatusBadRequest)
			_, _ = rw.Write([]byte(`{"error":{"message":"wrong endpoint for ` + model + `"}}`))
		}
	}))
	defer srv.Close()
	p := Provider{ID: "custom", Name: "Custom", Chat: srv.URL + "/v1", Key: "sk-x",
		Models: []string{"gpt-image-1", "flux-kontext-pro", "gpt-6-sol"}}
	rs := p.TestModels(context.Background(), []string{"gpt-image-1", "flux-kontext-pro", "gpt-6-sol"})
	for _, r := range rs {
		if !r.OK {
			t.Fatalf("results %+v", rs)
		}
	}
	if seen["gpt-image-1"] != "/v1/images/generations" || seen["flux-kontext-pro"] != "/v1/images/generations" || seen["gpt-6-sol"] != "/v1/chat/completions" {
		t.Fatalf("seen %v", seen)
	}
	if b := bodies["gpt-image-1"]; b["n"] != float64(1) || b["quality"] != "low" || b["prompt"] == "" {
		t.Fatalf("gpt-image body %v", b)
	}
	if b := bodies["flux-kontext-pro"]; b["quality"] != nil || b["messages"] != nil {
		t.Fatalf("flux body %v", b)
	}
	// the vendor-wide Test asks the chat model, though an image one is first
	mu.Lock()
	clear(seen)
	mu.Unlock()
	if rs := p.Test(context.Background()); len(rs) != 1 || !rs[0].OK || rs[0].Model != "gpt-6-sol" {
		t.Fatalf("Test %+v", rs)
	}
}
