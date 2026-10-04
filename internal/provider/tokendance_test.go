package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

func TestTokenDancePreset(t *testing.T) {
	p, err := FromPreset("tokendance")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "TokenDance" || p.Icon != "tokendance" || p.Preset != "tokendance" {
		t.Fatalf("identity: %+v", p)
	}
	if p.Chat != "https://tokendance.space/gateway/v1" ||
		p.Responses != "https://tokendance.space/gateway/v1" ||
		p.Anthropic != "https://tokendance.space/gateway" {
		t.Fatalf("endpoints: %q %q %q", p.Chat, p.Responses, p.Anthropic)
	}
	pr := Preset("tokendance")
	if pr.Kind != KindRelay || pr.NoKey || pr.KeysURL != "https://tokendance.space/keys" {
		t.Fatalf("preset: %+v", pr)
	}
	if len(pr.HeaderHints) != 1 || pr.HeaderHints[0] != "X-App-URL" {
		t.Fatalf("header hints: %v", pr.HeaderHints)
	}

	// A custom provider imported at TokenDance's documented base URL is
	// recognized as the preset too.
	im, _ := imported("x", "k", endpoints{chat: p.Chat + "/"}, nil)
	if im.Preset != "tokendance" || im.Icon != "tokendance" {
		t.Fatalf("imported: %+v", im)
	}
}

func TestTokenDanceLiveModelProtocols(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/gateway/v1/models" || r.Header.Get("Authorization") != "Bearer td-key" {
			http.Error(w, "wrong request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[
{"id":"deepseek-v4-pro","name":"DeepSeek V4 Pro","context_length":196608,"supported_protocols":["openai:chat-completions","openai:responses"]},
{"id":"claude-sonnet-5","name":"Claude Sonnet 5","supported_protocols":["anthropic:messages"]},
{"id":"gemini-3.1-pro","name":"Gemini 3.1 Pro","supported_protocols":["google:generate-content"]}
]}`))
	}))
	defer srv.Close()

	ms, at, err := catalog.FetchAt(context.Background(), srv.URL+"/gateway/v1", "td-key", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if at != srv.URL+"/gateway/v1/models" || len(ms) != 2 {
		t.Fatalf("at %q: %+v", at, ms)
	}
	if ms[0].Context != 196608 || !sameStrings(ms[0].APIs, []string{"chat", "responses"}) {
		t.Fatalf("OpenAI protocols: %+v", ms[0])
	}
	if !sameStrings(ms[1].APIs, []string{"anthropic"}) {
		t.Fatalf("Anthropic protocol: %+v", ms[1])
	}
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
