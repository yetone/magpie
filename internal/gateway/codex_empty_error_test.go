package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// One of Codex's own models, failed by the ChatGPT backend with an error
// status and nothing in its body, fails saying where it went and what to do:
// relayed as it came, Codex said only "unexpected status 502 Bad Gateway:
// Unknown error" (#409, Codex's app left on GPT-5.6 Terra while magpie's
// Agents view had DeepSeek picked for it).
func TestCodexOwnModelEmptyError(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})
	code, body := codexPost(t, `{"model":"gpt-5.6-terra","stream":true,"input":"今天几号"}`)
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if code != 502 || json.Unmarshal([]byte(body), &e) != nil {
		t.Fatalf("%d %q", code, body)
	}
	for _, want := range []string{"502 Bad Gateway", "gpt-5.6-terra is one of Codex's own models", "pick one of magpie's models"} {
		if !strings.Contains(e.Error.Message, want) {
			t.Errorf("no %q in %q", want, e.Error.Message)
		}
	}
}

// What the ChatGPT backend said of a failure still goes to Codex as it said
// it.
func TestCodexOwnModelErrorBodyKept(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	said := `{"error":{"message":"The model is overloaded","type":"server_error"}}`
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		io.WriteString(w, said)
	})
	if code, body := codexPost(t, `{"model":"gpt-5.6-terra","stream":true,"input":"hi"}`); code != 502 || body != said {
		t.Fatalf("%d %s", code, body)
	}
}

// A magpie model whose vendor fails with an empty body is never relayed
// empty: the agent is told the vendor and its status.
func TestCodexMagpieModelEmptyError(t *testing.T) {
	setup(t, provider.Responses, &fake{t: t, refuse: func([]byte) (int, string) { return http.StatusBadGateway, " " }})
	code, body := codexPost(t, `{"model":"fake/m1","stream":true,"input":"hi"}`)
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if code != 502 || json.Unmarshal([]byte(body), &e) != nil || !strings.Contains(e.Error.Message, "502") {
		t.Fatalf("%d %q", code, body)
	}
}
