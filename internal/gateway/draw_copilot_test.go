package gateway

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// A Copilot account's image model (GPT Image 2.5 Flare in Copilot CLI) is
// listed by its /models as an image model, never offered to an agent's chat,
// and draws at {api}/images/generations with the model, prompt, size and
// quality Copilot CLI sends.
func TestCopilotDrawsItsImageModel(t *testing.T) {
	fresh(t)
	cfg := os.Getenv("XDG_CONFIG_HOME")
	os.MkdirAll(filepath.Join(cfg, "github-copilot"), 0o755)
	os.WriteFile(filepath.Join(cfg, "github-copilot", "apps.json"), mustJSON(map[string]any{
		"github.com:Iv1.x": map[string]any{"user": "u", "oauth_token": "gho_img"},
	}), 0o600)
	var mu sync.Mutex
	var paths, bodies []string
	var sess string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/models":
			io.WriteString(w, `{"data":[
			  {"id":"gpt-4.1","name":"GPT-4.1","model_picker_enabled":true,"is_chat_default":true,"policy":{"state":"enabled"},"supported_endpoints":["/chat/completions"],"capabilities":{"type":"chat"}},
			  {"id":"gpt-image-2.5-flare","name":"GPT Image 2.5 Flare","model_picker_enabled":true,"policy":{"state":"enabled"},"capabilities":{"type":"image"}}]}`)
		case "/images/generations":
			mu.Lock()
			paths, bodies, sess = append(paths, r.URL.Path), append(bodies, string(b)), r.Header.Get("Authorization")
			mu.Unlock()
			io.WriteString(w, `{"data":[{"b64_json":"`+base64.StdEncoding.EncodeToString(pngBytes)+`"}],"usage":{"input_tokens":5,"output_tokens":100}}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer api.Close()
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/token") {
			json.NewEncoder(w).Encode(map[string]any{"token": "sess", "expires_at": time.Now().Add(time.Hour).Unix(), "endpoints": map[string]string{"api": api.URL}})
			return
		}
		io.WriteString(w, `{"copilot_plan":"individual"}`)
	}))
	defer gh.Close()
	oldTok, oldUser := provider.CopilotTokenURL, provider.CopilotUserURL
	provider.CopilotTokenURL, provider.CopilotUserURL = gh.URL+"/token", gh.URL+"/user"
	defer func() { provider.CopilotTokenURL, provider.CopilotUserURL = oldTok, oldUser }()

	p, err := provider.Find("copilot")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Fetch(t.Context()); err != nil {
		t.Fatal(err)
	}
	p, _ = provider.Find("copilot")
	var drawers []string
	for _, m := range Drawers(*p) {
		drawers = append(drawers, m.ID)
	}
	if strings.Join(drawers, ",") != "gpt-image-2.5-flare" {
		t.Fatalf("Copilot draws with %v", drawers)
	}
	for _, m := range p.Available() {
		if m.Draws {
			continue
		}
		if strings.Contains(m.ID, "image") {
			t.Fatalf("an image model among chat models: %v", m)
		}
	}

	s := New()
	code, a, raw := postImages(t, s, "/v1/images/generations", "application/json",
		`{"model":"copilot/gpt-image-2.5-flare","prompt":"a magpie","size":"1024x1024","quality":"high","background":"transparent","output_format":"png"}`)
	if code != 200 || len(a.Data) != 1 {
		t.Fatalf("%d %s", code, raw)
	}
	mu.Lock()
	defer mu.Unlock()
	sent := bodies[0]
	if paths[0] != "/images/generations" || !strings.Contains(sent, `"model":"gpt-image-2.5-flare"`) ||
		!strings.Contains(sent, `"size":"1024x1024"`) || !strings.Contains(sent, `"quality":"high"`) ||
		strings.Contains(sent, "background") || strings.Contains(sent, "output_format") {
		t.Fatalf("asked %v %s", paths, sent)
	}
	if sess != "Bearer sess" {
		t.Fatalf("Authorization %q", sess)
	}
}
