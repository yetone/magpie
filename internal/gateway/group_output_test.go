package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A group tells agents the longest reply a member of it gives, and each
// member is asked for no more than its own (ARNO on Discord: a group's
// maxTokens was its smallest member's, and replies were truncated).
func TestGroupOutputPerMember(t *testing.T) {
	fresh(t)
	asked := map[string]map[string]any{}
	for _, id := range []string{"a", "b"} {
		id := id
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var q map[string]any
			b, _ := io.ReadAll(r.Body)
			json.Unmarshal(b, &q)
			asked[id] = q
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
		}))
		t.Cleanup(up.Close)
		if err := provider.Save(provider.Provider{ID: id, Name: strings.ToUpper(id), Key: "k", Models: []string{"m"}, Chat: up.URL + "/v1"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SetModelOutput("a/m", 4096); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetModelOutput("b/m", 32000); err != nil {
		t.Fatal(err)
	}
	for _, g := range []provider.Group{{Name: "G", Members: []string{"a/m", "b/m"}}, {Name: "H", Members: []string{"b/m", "a/m"}}} {
		g.Routing = provider.Ordered
		if err := provider.SaveGroup(g); err != nil {
			t.Fatal(err)
		}
	}
	s := New()

	// told the longest
	var told int
	for _, e := range provider.Served() {
		if e.ID == provider.GroupPrefix+"g" {
			told = e.Output
		}
	}
	if told != 32000 {
		t.Fatalf("the group says %d", told)
	}

	// a asked for its own 4096, b for the 20000 the agent asked
	for _, g := range []string{"g", "h"} {
		if code, body := postAs(t, s, "", `{"model":"group/`+g+`","max_tokens":20000,"messages":[{"role":"user","content":"hi"}]}`); code != 200 {
			t.Fatalf("%d %s", code, body)
		}
	}
	if a, b := asked["a"]["max_tokens"], asked["b"]["max_tokens"]; a != float64(4096) || b != float64(20000) {
		t.Fatalf("a asked %v, b asked %v", a, b)
	}

	// a request under a member's limit goes as it is
	postAs(t, s, "", `{"model":"group/g","max_tokens":1000,"messages":[{"role":"user","content":"hi"}]}`)
	if a := asked["a"]["max_tokens"]; a != float64(1000) {
		t.Fatalf("a asked %v", a)
	}
}

func TestWithMaxOutputAnthropicThinking(t *testing.T) {
	out := withMaxOutput(provider.Anthropic, []byte(`{"max_tokens":32000,"thinking":{"type":"enabled","budget_tokens":16000}}`), 8192)
	var v struct {
		MaxTokens int `json:"max_tokens"`
		Thinking  struct {
			Type   string `json:"type"`
			Budget int    `json:"budget_tokens"`
		} `json:"thinking"`
	}
	json.Unmarshal(out, &v)
	if v.MaxTokens != 8192 || v.Thinking.Type != "enabled" || v.Thinking.Budget != 8191 {
		t.Fatalf("%s", out)
	}
	out = withMaxOutput(provider.Anthropic, []byte(`{"max_tokens":32000,"thinking":{"type":"enabled","budget_tokens":16000}}`), 1000)
	json.Unmarshal(out, &v)
	if v.MaxTokens != 1000 || v.Thinking.Type != "disabled" {
		t.Fatalf("%s", out)
	}
	if out := withMaxOutput(provider.Responses, []byte(`{"max_output_tokens":100}`), 8192); string(out) != `{"max_output_tokens":100}` {
		t.Fatalf("%s", out)
	}
}
