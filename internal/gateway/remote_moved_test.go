package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A Remote magpie whose computer came back at another address
// (luckymanjc on Discord: A's router gives it a new IP each boot): the
// request that finds no one at the old one is answered from the address
// that lists for the provider's key, and the provider is saved there.
func TestRemoteMagpieMovedIsFound(t *testing.T) {
	fresh(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"c1","object":"chat.completion","model":"sol","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`)
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"sol"}, Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	h := New().Handler()
	remote := httptest.NewServer(h)
	t.Cleanup(remote.Close)
	at := strings.TrimPrefix(remote.URL, "http://")
	id, err := provider.Add(provider.Provider{ID: "office", Name: "Office", Preset: provider.RemoteMagpiePreset, Key: "secret", Chat: at})
	if err != nil {
		t.Fatal(err)
	}
	office, err := provider.Find(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := office.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	// A's old address now answers nobody (nothing listens on port 1)
	office, _ = provider.Find(id)
	office.Chat, office.Responses, office.Anthropic, office.Decide = "127.0.0.1:1", "", "", ""
	if err := provider.Save(*office); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(provider.SeekHere([]string{"127.0.0.1:1", at}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"office/fake/sol","messages":[{"role":"user","content":"hi"}]}`)))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	office, _ = provider.Find(id)
	if office.Chat != remote.URL+"/v1" {
		t.Errorf("saved at %q, want %s/v1", office.Chat, remote.URL)
	}
}
