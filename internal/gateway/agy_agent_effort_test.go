package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// Antigravity CLI (agy) has no effort magpie can set: its --effort is for
// one session, turned away for magpie's models, and settings.json has no
// key for it (xyenon on Discord). Its requests ask thinkingBudget -1, the
// model's own choice. With an effort picked for it in magpie, its requests
// to a Gemini API go out at that effort in the words the model takes: a
// Gemini 2.5 a budget in its range (it turns a level away), a Gemini 3 a
// level of its own. With none picked they go as agy sent them, and another
// agent's never take its pick.
func TestAgyAgentEffort(t *testing.T) {
	fresh(t)
	var mu sync.Mutex
	var sent []byte
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		sent = b
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":1}}`))
	}))
	t.Cleanup(up.Close)
	const flash, pro = "gemini-2.5-flash", "gemini-3.1-pro-preview"
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Models: []string{flash, pro}, Gemini: up.URL + "/v1beta"}); err != nil {
		t.Fatal(err)
	}
	// their levels, as the Gemini API lists them for a Google key
	if err := catalog.SaveLive("relay", up.URL+"/v1beta", []catalog.Model{{ID: flash, Efforts: []string{"low", "medium", "high"}}, {ID: pro, Efforts: []string{"low", "medium", "high"}}}); err != nil {
		t.Fatal(err)
	}
	if p, _ := provider.Find("relay"); !p.Thinks(flash) || !p.Thinks(pro) {
		t.Fatal("the relay's models don't reason")
	}
	s := New()
	// agy's own request, as captured from agy 1.5.3 against a stub
	body := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"includeThoughts":true,"thinkingBudget":-1}}}`
	// the thinkingConfig the relay was sent
	asked := func(agent, model string) gjson.Result {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1beta/models/relay/"+model+":streamGenerateContent?alt=sse", strings.NewReader(body))
		req.Header.Set("x-goog-api-key", TokenFor(agent))
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "ok") {
			t.Fatalf("%s asking %s: %d %s", agent, model, rec.Code, rec.Body)
		}
		mu.Lock()
		defer mu.Unlock()
		return gjson.GetBytes(sent, "generationConfig.thinkingConfig")
	}
	const asSent = `{"includeThoughts":true,"thinkingBudget":-1}`
	same := func(got gjson.Result, _ string) bool {
		return got.Get("includeThoughts").Raw == "true" && got.Get("thinkingBudget").Raw == "-1" && !got.Get("thinkingLevel").Exists()
	}
	for _, m := range []string{flash, pro} {
		if got := asked("agy", m); !same(got, asSent) {
			t.Fatalf("none picked, %s: sent %s, want %s as agy sent it", m, got.Raw, asSent)
		}
	}
	for _, c := range []struct {
		pick, model string
		budget      int64  // a Gemini 2.5's
		level       string // a Gemini 3's
	}{
		{"high", flash, 24000, ""},
		{"low", flash, 4096, ""},
		{"high", pro, 0, "high"},
		{"low", pro, 0, "low"},
		{"max", pro, 0, "high"}, // no Gemini takes max
	} {
		if err := provider.SetAgentEffort("agy", c.pick); err != nil {
			t.Fatal(err)
		}
		got := asked("agy", c.model)
		if got.Get("includeThoughts").Raw != "true" {
			t.Errorf("picked %s, %s: thoughts no longer asked for: %s", c.pick, c.model, got.Raw)
		}
		if c.level == "" {
			if got.Get("thinkingLevel").Exists() || got.Get("thinkingBudget").Int() != c.budget {
				t.Errorf("picked %s, %s: sent %s, want thinkingBudget %d and no level", c.pick, c.model, got.Raw, c.budget)
			}
		} else if got.Get("thinkingBudget").Exists() || got.Get("thinkingLevel").String() != c.level {
			t.Errorf("picked %s, %s: sent %s, want thinkingLevel %q and no budget", c.pick, c.model, got.Raw, c.level)
		}
		// another agent's request goes as it asks
		if got := asked("opencode", c.model); !same(got, asSent) {
			t.Errorf("picked %s for agy, opencode's to %s went %s, want %s", c.pick, c.model, got.Raw, asSent)
		}
	}
	// agy's own /effort or --effort names a level in its request: that is
	// the user's pick of the moment, over the row's (haohaolee on X)
	if err := provider.SetAgentEffort("agy", "low"); err != nil {
		t.Fatal(err)
	}
	body = strings.Replace(body, `"thinkingBudget":-1`, `"thinkingLevel":"HIGH"`, 1)
	if got := asked("agy", pro); got.Get("thinkingLevel").String() != "HIGH" {
		t.Errorf("agy asked HIGH with the row at low: sent %s", got.Raw)
	}
	body = strings.Replace(body, `"thinkingLevel":"HIGH"`, `"thinkingBudget":-1`, 1)
	if err := provider.SetAgentEffort("agy", ""); err != nil {
		t.Fatal(err)
	}
	if got := asked("agy", pro); !same(got, asSent) {
		t.Errorf("taken off: sent %s, want %s", got.Raw, asSent)
	}
}

// A Gemini that lists a level above high (Gemini 4 Hydrogen's xhigh, as
// Google's model page and models.dev give it) is sent it, and max goes as
// its nearest, xhigh; a Gemini listing high at most (Carbon), or none, is
// still sent high (#6452).
func TestWithGeminiEffortAboveHigh(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	if err := os.WriteFile(catalog.CachePath(), []byte(`{"google-vertex":{"models":{
	  "gemini-4-hydrogen":{"id":"gemini-4-hydrogen","reasoning":true,"reasoning_options":[{"type":"effort","values":["low","medium","high","xhigh"]}]},
	  "gemini-4-carbon":{"id":"gemini-4-carbon","reasoning":true,"reasoning_options":[{"type":"effort","values":["low","medium","high"]}]}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	for _, c := range []struct{ model, effort, want string }{
		{"gemini-4-hydrogen", "xhigh", "xhigh"},
		{"gemini-4-hydrogen", "max", "xhigh"},
		{"gemini-4-hydrogen", "high", "high"},
		{"gemini-4-carbon", "xhigh", "high"},
		{"gemini-4-carbon", "max", "high"},
		{"gemini-3.9-unlisted", "max", "high"},
	} {
		out := withGeminiEffort([]byte(`{"contents":[]}`), c.model, c.effort)
		if got := gjson.GetBytes(out, "generationConfig.thinkingConfig.thinkingLevel").String(); got != c.want {
			t.Errorf("%s at %s: %s, want %s", c.model, c.effort, got, c.want)
		}
	}
}

// withGeminiEffort writes a Gemini request's thinkingConfig as the model
// takes it, and a Gemini before 2.5, which doesn't think, is left alone.
func TestWithGeminiEffort(t *testing.T) {
	const in = `{"contents":[],"generationConfig":{"temperature":1,"thinkingConfig":{"includeThoughts":true,"thinkingLevel":"HIGH"}}}`
	for _, c := range []struct{ model, effort, want string }{
		{"gemini-2.5-pro", "none", `{"thinkingBudget":128}`},
		{"gemini-2.5-flash", "none", `{"thinkingBudget":0}`},
		{"gemini-2.5-flash-lite", "minimal", `{"includeThoughts":true,"thinkingBudget":4096}`},
		{"models/gemini-2.5-pro", "xhigh", `{"includeThoughts":true,"thinkingBudget":32000}`},
		{"gemini-3-flash-preview", "minimal", `{"includeThoughts":true,"thinkingLevel":"minimal"}`},
		{"gemini-3.1-pro-preview", "none", `{"includeThoughts":true,"thinkingLevel":"low"}`},
		{"gemini-3.1-pro-preview", "medium", `{"includeThoughts":true,"thinkingLevel":"medium"}`},
		{"openai/gpt-5.5", "xhigh", `{"includeThoughts":true,"thinkingLevel":"xhigh"}`},
		{"openai/gpt-5.5", "none", `{"thinkingBudget":0}`},
	} {
		out := withGeminiEffort([]byte(in), c.model, c.effort)
		if got := gjson.GetBytes(out, "generationConfig.thinkingConfig").Raw; got != c.want {
			t.Errorf("%s at %s: %s, want %s", c.model, c.effort, got, c.want)
		}
		if gjson.GetBytes(out, "generationConfig.temperature").Raw != "1" {
			t.Errorf("%s at %s: the rest of generationConfig lost: %s", c.model, c.effort, out)
		}
	}
	if out := withGeminiEffort([]byte(in), "gemini-2.0-flash", "high"); string(out) != in {
		t.Errorf("gemini-2.0: %s", out)
	}
	// a request with no generationConfig is given one
	if got := gjson.GetBytes(withGeminiEffort([]byte(`{"contents":[]}`), "gemini-3-flash", "high"), "generationConfig.thinkingConfig").Raw; got != `{"thinkingLevel":"high"}` {
		t.Errorf("no generationConfig: %s", got)
	}
	// withFixedEffort, which has no model but the one the body names
	if got := gjson.GetBytes(withFixedEffort(provider.Gemini, []byte(`{"model":"gemini-2.5-flash","contents":[]}`), "high"), "generationConfig.thinkingConfig").Raw; got != `{"thinkingBudget":24000}` {
		t.Errorf("withFixedEffort: %s", got)
	}
}
