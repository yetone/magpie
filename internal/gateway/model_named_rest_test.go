package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// Vendors that count limits per model name the model in the refusal. Each
// body is the vendor's own shape (OpenAI's and Groq's messages as they
// word them, Gemini's QuotaFailure with the model in quotaDimensions).
const (
	openAIModelTPM = `{"error":{"message":"Rate limit reached for gpt-4o in organization org-abc123 on tokens per min (TPM): Limit 30000, Used 29000, Requested 5000. Please try again in 8s. Visit https://platform.openai.com/account/rate-limits to learn more.","type":"tokens","param":null,"code":"rate_limit_exceeded"}}`
	groqModelTPD   = "{\"error\":{\"message\":\"Rate limit reached for model `gpt-4o` in organization `org_01` service tier `on_demand` on tokens per day (TPD): Limit 100000, Used 99800, Requested 1200. Please try again in 17m1.4s. Need more tokens? Upgrade to Dev Tier today at https://console.groq.com/settings/billing\",\"type\":\"tokens\",\"code\":\"rate_limit_exceeded\"}}"
	geminiModelDay = `{"error":{"code":429,"message":"You exceeded your current quota, please check your plan and billing details. For more information on this error, head to: https://ai.google.dev/gemini-api/docs/rate-limits.","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"quotaMetric":"generativelanguage.googleapis.com/generate_content_free_tier_requests","quotaId":"GenerateRequestsPerDayPerProjectPerModel-FreeTier","quotaDimensions":{"location":"global","model":"gpt-4o"},"quotaValue":"50"}]}]}}`
	// no model named: the key's own limit
	anthropicOrgRate = `{"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed the rate limit for your organization (abc) of 30,000 input tokens per minute."}}`
)

// A 429 that names the model it limited rests that model on that key, not
// the key: its other models are asked straight (WJaunita45604 on X: one
// model limited closed the whole provider until it came back). One that
// names no model rests the key, as before.
func TestModelNamedLimitRestsTheModelNotTheKey(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"groq tpd", groqModelTPD, 429},
		{"gemini per-model day quota", geminiModelDay, 429},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("XDG_CACHE_HOME", t.TempDir())
			restingUntil.Lock()
			restingUntil.m, restingUntil.note = map[string]time.Time{}, map[string]Rest{}
			restingUntil.Unlock()
			routed.Lock()
			routed.failures = map[string]int{}
			routed.Unlock()
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Model string `json:"model"`
				}
				json.NewDecoder(r.Body).Decode(&req)
				w.Header().Set("Content-Type", "application/json")
				if req.Model == "gpt-4o" {
					w.WriteHeader(tc.status)
					io.WriteString(w, tc.body)
					return
				}
				io.WriteString(w, `{"id":"ok","choices":[]}`)
			}))
			defer up.Close()
			p := provider.Provider{ID: "relay", Name: "relay", Chat: up.URL + "/v1",
				Models: []string{"gpt-4o", "gpt-4o-mini"}, Key: "k-one"}
			if err := provider.Save(p); err != nil {
				t.Fatal(err)
			}
			saved, err := provider.Find("relay")
			if err != nil {
				t.Fatal(err)
			}
			s := New()
			send := func(model string) int {
				rec := httptest.NewRecorder()
				body := `{"model":"relay/` + model + `","messages":[{"role":"user","content":"hi"}]}`
				s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
				return rec.Code
			}
			if code := send("gpt-4o"); code != 429 {
				t.Fatalf("gpt-4o: %d", code)
			}
			key := saved.RestKey(saved.KeyList()[0])
			_, keyRests := RestOf(key)
			r, modelRests := RestOf(key + "/gpt-4o")
			if keyRests {
				t.Errorf("the key rests whole for one model's limit")
			}
			if !modelRests || r.Model != "gpt-4o" {
				t.Errorf("gpt-4o doesn't rest on its own: %+v", r)
			}
			if code := send("gpt-4o-mini"); code != 200 {
				t.Errorf("gpt-4o-mini, which the vendor didn't limit, answered %d", code)
			}
		})
	}
}

// A limit that names no model is the key's: it rests whole, as before.
func TestLimitNamingNoModelRestsTheKey(t *testing.T) {
	restingUntil.Lock()
	restingUntil.m, restingUntil.note = map[string]time.Time{}, map[string]Rest{}
	restingUntil.Unlock()
	s := &Server{}
	c := candidate{p: provider.Provider{ID: "relay", Key: "k-one"}, model: "gpt-4o", rest: "relay"}
	r := s.restAfter(c, 429, http.Header{}, []byte(anthropicOrgRate))
	if r.Key != c.restKey() || r.Model != "" {
		t.Fatalf("rests %q (model %q); want the key %q", r.Key, r.Model, c.restKey())
	}
	r = s.restAfter(c, 429, http.Header{}, []byte(openAIModelTPM))
	if r.Key != c.restID() || r.Model != "gpt-4o" {
		t.Fatalf("rests %q (model %q); want the model %q", r.Key, r.Model, c.restID())
	}
}

func TestModelNamedReads(t *testing.T) {
	c := func(m string) candidate { return candidate{model: m} }
	for _, tc := range []struct {
		model, why, body string
		want             bool
	}{
		{"gpt-4o", failRate, openAIModelTPM, true},
		{"gpt-4o-mini", failRate, openAIModelTPM, false}, // longer id is another model
		{"gpt-4", failRate, openAIModelTPM, false},
		{"openai/gpt-4o", failRate, openAIModelTPM, true},
		{"gpt-4o", failQuota, geminiModelDay, true},
		{"gpt-4o", failCredit, openAIModelTPM, false}, // a balance is every model's
		{"gpt-4o", failOther, openAIModelTPM, false},
		{"gpt-4o", failRate, anthropicOrgRate, false},
		{"", failRate, openAIModelTPM, false},
		{"gpt-4o", failRate, `{"error":{"message":"Rate limit reached for gpt-4o."}}`, true},
		{"gpt-4o", failRate, `{"error":{"message":"Rate limit reached for models/gpt-4o: slow down"}}`, true},
	} {
		if got := modelNamed(c(tc.model), tc.why, []byte(tc.body)); got != tc.want {
			t.Errorf("%s %s: got %v want %v (%.60s)", tc.model, tc.why, got, tc.want, tc.body)
		}
	}
}
