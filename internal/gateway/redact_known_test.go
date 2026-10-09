package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// The vendor answers about a placeholder in words of its own, the agent
// reads the value there, and the next turn's history has it in words no
// masking rule knows: it goes as the same placeholder again, on every
// route a request can take.
func TestEchoedValueMaskedNextTurn(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	const secret, email = "Mv4kestrelPass28", "tomas.ng@quietfield.dev"
	placeholder := regexp.MustCompile(`\{\{[A-Z_]+_[a-z2-7]{8}\}\}`)
	var mu sync.Mutex
	var sent []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		sent = append(sent, string(b))
		mu.Unlock()
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(400)
			io.WriteString(w, `{"error":{"message":"not here","type":"invalid_request_error"}}`)
			return
		}
		// what it was given, in its own words: no DB_PASSWORD= before the
		// one, a full stop after the other
		var secretP, emailP string
		for _, p := range placeholder.FindAllString(string(b), -1) {
			switch {
			case strings.HasPrefix(p, "{{SECRET_"):
				secretP = p
			case strings.HasPrefix(p, "{{EMAIL_"):
				emailP = p
			}
		}
		reply := "Noted: your database password is " + secretP + ", and I will write to " + emailP + "."
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id": "c1", "object": "chat.completion", "model": "m1",
			"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": reply}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 3, "completion_tokens": 1},
		})
	}))
	defer up.Close()
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}, Chat: up.URL + "/v1", Anthropic: up.URL}); err != nil {
		t.Fatal(err)
	}
	if err := settings.Save(settings.Settings{Redact: true, RedactPersonal: true}); err != nil {
		t.Fatal(err)
	}
	leaked := func(b string) bool { return strings.Contains(b, secret) || strings.Contains(b, email) }

	user := `{"role":"user","content":"DB_PASSWORD=` + secret + ` and I am ` + email + `"}`
	code, body := post(t, "/v1/chat/completions", `{"model":"fake/m1","messages":[`+user+`]}`)
	if code != 200 || len(sent) != 1 || leaked(sent[0]) {
		t.Fatalf("first turn: %d %s\nsent: %v", code, body, sent)
	}
	var first struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	json.Unmarshal([]byte(body), &first)
	if len(first.Choices) == 0 || !strings.Contains(first.Choices[0].Message.Content, secret) || !strings.Contains(first.Choices[0].Message.Content, email) {
		t.Fatalf("the agent should read its values back: %s", body)
	}
	echo := first.Choices[0].Message.Content
	asst, _ := json.Marshal(map[string]any{"role": "assistant", "content": echo})
	code, body = post(t, "/v1/chat/completions", `{"model":"fake/m1","messages":[`+user+`,`+string(asst)+`,{"role":"user","content":"go on"}]}`)
	if code != 200 || len(sent) != 2 {
		t.Fatalf("second turn: %d %s", code, body)
	}
	if leaked(sent[1]) {
		t.Fatalf("the second turn sent a value to the vendor: %s", sent[1])
	}
	// the same placeholders as the first turn, so what the vendor cached
	// of it holds
	for _, p := range placeholder.FindAllString(sent[0], -1) {
		if strings.Count(sent[1], p) != 2 {
			t.Fatalf("%s not in the second turn twice: %s", p, sent[1])
		}
	}

	// the reply alone, on every other route a request can take
	e, _ := json.Marshal(echo)
	routes := []struct{ name, path, body string }{
		{"responses", "/v1/responses", `{"model":"fake/m1","input":` + string(e) + `}`},
		{"anthropic", "/v1/messages", `{"model":"fake/m1","max_tokens":10,"messages":[{"role":"assistant","content":` + string(e) + `},{"role":"user","content":"go on"}]}`},
		{"count_tokens", "/v1/messages/count_tokens", `{"model":"fake/m1","messages":[{"role":"user","content":` + string(e) + `}]}`},
		{"gemini", "/v1beta/models/fake/m1:generateContent", `{"contents":[{"role":"model","parts":[{"text":` + string(e) + `}]},{"role":"user","parts":[{"text":"go on"}]}]}`},
		{"embeddings", "/v1/embeddings", `{"model":"fake/m1","input":` + string(e) + `}`},
	}
	for _, r := range routes {
		mu.Lock()
		before := len(sent)
		mu.Unlock()
		post(t, r.path, r.body)
		mu.Lock()
		got := append([]string(nil), sent[before:]...)
		mu.Unlock()
		if len(got) == 0 {
			t.Errorf("%s: nothing reached the vendor", r.name)
		}
		for _, b := range got {
			if leaked(b) {
				t.Errorf("%s: a value went to the vendor: %s", r.name, b)
			}
		}
	}

	// and Codex's own backend
	var codex []string
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		codex = append(codex, string(b))
		w.Header()["Content-Type"] = nil
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r1"}}`,
			`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":9,"output_tokens":2}}}`))
	})
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := settings.Save(settings.Settings{Redact: true, RedactPersonal: true}); err != nil {
		t.Fatal(err)
	}
	codexPost(t, `{"model":"gpt-5.5","stream":true,"input":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":`+string(e)+`}]}]}`)
	if len(codex) == 0 {
		t.Fatal("codex: nothing reached the backend")
	}
	for _, b := range codex {
		if leaked(b) {
			t.Errorf("codex: a value went to the backend: %s", b)
		}
	}
}
