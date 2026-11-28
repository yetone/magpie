package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// Zanghaihuase6m on X: Ultra picked for gpt-6.2-astra in Cindy, and
// magpie's Requests said medium. Cindy offers the levels magpie's
// /v1/models gives (parseModelsListResponse reads supported_reasoning_levels,
// strings or objects' "effort", none left out) and hands the one picked to
// its Codex (0.166), which doesn't know gpt-6.2-astra (Codex has it from
// 0.171): Cindy's catalog gives such a model no levels
// (nativeCodexFallbackModel), and Codex sends Ultra on an entry with none
// as medium (ModelInfo::resolve_reasoning_effort). Cindy is now offered
// every level but Ultra, each sent as picked, and max goes out at max.
// Another client is still offered Ultra.
func TestCindyOfferedOnlyLevelsItsCodexSends(t *testing.T) {
	efforts := cindyAstra(t)
	p, err := provider.Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}

	// what Cindy's discovery reads of a row: declaredReasoningEfforts
	levelsOf := func(key string) []string {
		req := httptest.NewRequest("GET", "/v1/models", nil)
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		rec := httptest.NewRecorder()
		New().Handler().ServeHTTP(rec, req)
		var out struct {
			Data []struct {
				ID     string            `json:"id"`
				Levels []json.RawMessage `json:"supported_reasoning_levels"`
				Flat   []string          `json:"reasoning_levels"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("%d: %s", rec.Code, rec.Body)
		}
		for _, m := range out.Data {
			if m.ID != "codex/gpt-6.2-astra" {
				continue
			}
			var got []string
			for _, l := range m.Levels {
				var s string
				var o struct {
					Effort string `json:"effort"`
				}
				if json.Unmarshal(l, &s) != nil {
					json.Unmarshal(l, &o)
					s = o.Effort
				}
				if s != "" && s != "none" && !slices.Contains(got, s) {
					got = append(got, s)
				}
			}
			if !slices.Equal(got, slices.DeleteFunc(slices.Clone(m.Flat), func(e string) bool { return e == "none" })) {
				t.Fatalf("supported_reasoning_levels %v, reasoning_levels %v", got, m.Flat)
			}
			return got
		}
		t.Fatalf("codex/gpt-6.2-astra not listed: %s", rec.Body)
		return nil
	}

	// Codex's resolve_reasoning_effort on the entry Cindy's catalog gives
	// a model its Codex doesn't know: no levels, so no multi-agent effort
	// and no max among them
	codexSends := func(picked string) string {
		if picked == "ultra" {
			return "medium"
		}
		return picked
	}
	offered := levelsOf(TokenFor("cindy"))
	if !slices.Contains(offered, "max") {
		t.Fatalf("Cindy offered %v, no max", offered)
	}
	for _, e := range offered {
		if sent := codexSends(e); sent != e {
			t.Errorf("Cindy offered %s, which its Codex sends as %s (offered %v)", e, sent, offered)
		}
	}
	if other := levelsOf(""); !slices.Contains(other, "ultra") {
		t.Errorf("another client offered %v, no ultra", other)
	}

	// the turn Cindy's Codex sends at max, as its proxy leaves it
	// (reconcileResponsesReasoningEffort keeps a level the list has)
	body := `{"model":"codex/gpt-6.2-astra","instructions":"You are Codex, a coding agent based on GPT-5.","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"ping"}]}],"tools":[],"tool_choice":"auto","parallel_tool_calls":true,"reasoning":{"effort":"max","summary":"auto"},"store":false,"stream":true,"include":["reasoning.encrypted_content"],"prompt_cache_key":"019ac9c4-cindy"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+TokenFor("cindy"))
	req.Header.Set("Content-Type", "application/json")
	New().Handler().ServeHTTP(rec, req)
	if got := efforts(); rec.Code != 200 || got != "max" {
		t.Fatalf("max from Cindy: status %d, the backend was sent %q: %s", rec.Code, got, rec.Body)
	}
}

// cindyAstra signs Codex in to a Pro ChatGPT account whose backend lists
// gpt-6.2-astra with the levels Codex's catalog gives it (low to max, and
// ultra). efforts takes the reasoning.effort of each turn the backend was
// sent since it was last called.
func cindyAstra(t *testing.T) (efforts func() string) {
	t.Helper()
	fresh(t)
	home := os.Getenv("HOME")
	claims := func(m map[string]any) string {
		b, _ := json.Marshal(m)
		return "h." + base64.RawURLEncoding.EncodeToString(b) + ".s"
	}
	os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	os.WriteFile(filepath.Join(home, ".codex", "auth.json"), mustJSON(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
		"id_token": claims(map[string]any{"email": "pro@example.com",
			"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "pro"}}),
		"access_token":  claims(map[string]any{"exp": time.Now().Add(time.Hour).Unix(), "who": "acct-pro"}),
		"refresh_token": "r-acct-pro", "account_id": "acct-pro"}}), 0o600)

	var mu sync.Mutex
	var sent []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			var ls []map[string]any
			for _, l := range []string{"low", "medium", "high", "xhigh", "max", "ultra"} {
				ls = append(ls, map[string]any{"effort": l, "description": ""})
			}
			w.Write(mustJSON(map[string]any{"models": []map[string]any{{"slug": "gpt-6.2-astra", "display_name": "GPT-6.2-Astra",
				"visibility": "list", "priority": 1, "supported_reasoning_levels": ls, "default_reasoning_level": "low",
				"input_modalities": []string{"text", "image"}, "context_window": 272000, "supported_in_api": true}}}))
			return
		}
		var q struct {
			Reasoning struct {
				Effort string `json:"effort"`
			} `json:"reasoning"`
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &q)
		mu.Lock()
		sent = append(sent, q.Reasoning.Effort)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r1","model":"gpt-6.2-astra"}}`,
			`data: {"type":"response.output_text.delta","delta":"pong"}`,
			`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":7,"output_tokens":1}}}`))
	}))
	t.Cleanup(up.Close)
	old := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = old })

	return func() string {
		mu.Lock()
		defer mu.Unlock()
		got := strings.Join(sent, ",")
		sent = nil
		return got
	}
}
