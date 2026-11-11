package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// clineFreeLimitMsg is Cline's refusal of a free model whose day's
// allowance is used up, as Cline's own clients' tests quote it (cline/cline
// sdk/packages/llms/src/providers/errors.test.ts, apps/vscode ClineError
// tests: "Error 429: Daily free limit reached on model …").
const clineFreeLimitMsg = "Daily free limit reached on model deepseek/deepseek-v4-flash. Try again in 23h 59m"

// clineRestsCleared starts t with nobody resting, and leaves nobody resting after
// it: a Cline model it rested would be held from the next test's request.
func clineRestsCleared(t *testing.T) {
	clear := func() {
		restingUntil.Lock()
		restingUntil.m, restingUntil.note = map[string]time.Time{}, map[string]Rest{}
		restingUntil.Unlock()
	}
	clear()
	t.Cleanup(clear)
}

// Cline's daily limit of one free model rests that model alone, until the
// time it names: the key's other models, free or the plan's, are still
// served (#1398, wuasakura: one free model's 429 sent the whole Cline
// account to the back). The same words from another vendor rest its key
// as before, and so does a Cline 429 that doesn't say it is the model's.
func TestClineFreeLimitRestsTheModel(t *testing.T) {
	old := allowances
	defer func() { allowances = old }()
	allowances = func(string) map[string]provider.Allowance { return nil }
	clineRestsCleared(t)
	s := &Server{}
	cline := provider.Provider{ID: "cline", Preset: "clinepass", Chat: "https://api.cline.bot/api/v1", Key: "clp_key"}
	other := provider.Provider{ID: "relay", Chat: "https://relay.test/v1", Key: "k"}
	at := func(p provider.Provider, model string) candidate {
		return candidate{p: p, model: model, rest: p.ID}
	}
	day := 23*time.Hour + 59*time.Minute
	near := func(d, want time.Duration) bool { return d > want-time.Minute && d <= want }

	for _, body := range []string{
		`{"error":{"message":"` + clineFreeLimitMsg + `"}}`, // as clineUnwrapped hands it on
		clineFreeLimitMsg,
		escaped(clineFreeLimitMsg, "429"),
		// as the Cline plugin passes Cline's envelope on (its failure())
		`{"error":{"message":"usage limit reached: {\"success\":false,\"error\":\"` + clineFreeLimitMsg + `\"}","type":"cline_error","code":429}}`,
	} {
		if f := failure(429, []byte(body)); f != failQuota {
			t.Fatalf("%s: %s, want %s", body, f, failQuota)
		}
		for _, p := range []provider.Provider{cline, {ID: "cline-host", Chat: "https://api.cline.bot/api/v1", Key: "k"}} {
			c := at(p, "cline-free/deepseek-v4.1-flash")
			r := s.restAfter(c, 429, http.Header{}, []byte(body))
			if r.Key != c.restID() || r.Model != c.model {
				t.Fatalf("%s %s: rests %s (model %q), not the model %s", p.ID, body, r.Key, r.Model, c.restID())
			}
			if r.By != "resets" || !near(time.Until(r.Until), day) {
				t.Fatalf("%s %s: rests %v by %s, want %v by resets", p.ID, body, time.Until(r.Until), r.By, day)
			}
			for _, m := range []string{"cline-free/mimo-v2.6-flash", "cline-pass/glm-5.3"} {
				if resting(at(p, m)) {
					t.Fatalf("%s: %s rests beside the free model out of its day", p.ID, m)
				}
			}
			if !resting(c) {
				t.Fatalf("%s: the free model out of its day isn't resting", p.ID)
			}
		}
	}

	// another vendor saying the same names the model, so it rests that
	// model alone (modelNamed), not by Cline's day
	c := at(other, "deepseek-v4-flash")
	if r := s.restAfter(c, 429, http.Header{}, []byte(`{"error":{"message":"`+clineFreeLimitMsg+`"}}`)); r.Key != c.restID() || r.By == "resets" {
		t.Fatalf("another vendor's refusal rests %s by %s, want its model %s", r.Key, r.By, c.restID())
	}
	// a Cline 429 that doesn't name the model's limit rests the key
	cline.ID = "cline2"
	c = at(cline, "cline-free/deepseek-v4.1-flash")
	if r := s.restAfter(c, 429, http.Header{}, []byte(`{"error":{"message":"Rate limit exceeded"}}`)); r.Key != c.restKey() {
		t.Fatalf("a plain Cline rate limit rests %s, want the key %s", r.Key, c.restKey())
	}
}

// Through the gateway, as the reporter's agent asks it: once a free model
// of a Cline key is out of its day, a request for another of its models
// still reaches Cline and is answered, and the one out of its day is
// answered with its refusal without asking Cline again.
func TestClineFreeLimitLeavesOtherModels(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	clineRestsCleared(t)
	cline, err := provider.FromPreset("clinepass")
	if err != nil {
		t.Fatal(err)
	}
	cline.ID, cline.Key = "cline", "clp_key"
	cline.Models = []string{"cline-free/deepseek-v4.1-flash", "cline-free/mimo-v2.6-flash"}
	if err := provider.Save(cline); err != nil {
		t.Fatal(err)
	}
	const completion = `{"id":"gen_1","object":"chat.completion","model":"cline-free/mimo-v2.6-flash","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6}}`
	var mu sync.Mutex
	var asked []string
	s := New()
	s.client = &http.Client{Transport: countTransport(func(r *http.Request) (*http.Response, error) {
		var q struct {
			Model string `json:"model"`
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &q)
		mu.Lock()
		asked = append(asked, q.Model)
		mu.Unlock()
		h := http.Header{"Content-Type": {"application/json; charset=utf-8"}}
		if q.Model == "cline-free/deepseek-v4.1-flash" {
			return &http.Response{StatusCode: 429, Header: h,
				Body: io.NopCloser(strings.NewReader(`{"success":false,"error":"` + clineFreeLimitMsg + `"}`))}, nil
		}
		return &http.Response{StatusCode: 200, Header: h,
			Body: io.NopCloser(strings.NewReader(`{"success":true,"data":` + completion + `}`))}, nil
	})}
	ask := func(model string) (int, string) {
		t.Helper()
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
			strings.NewReader(`{"model":"cline/`+model+`","stream":false,"messages":[{"role":"user","content":"Say pong"}]}`)))
		return rec.Code, rec.Body.String()
	}
	if code, body := ask("cline-free/deepseek-v4.1-flash"); code != 429 || !strings.Contains(body, "Daily free limit") {
		t.Fatalf("free model out of its day: %d %s", code, body)
	}
	if code, body := ask("cline-free/mimo-v2.6-flash"); code != 200 || !strings.Contains(body, "pong") {
		t.Fatalf("another free model of the key: %d %s", code, body)
	}
	if code, body := ask("cline-free/deepseek-v4.1-flash"); code != 429 || !strings.Contains(body, "Daily free limit") {
		t.Fatalf("free model out of its day, again: %d %s", code, body)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(asked, ",") != "cline-free/deepseek-v4.1-flash,cline-free/mimo-v2.6-flash" {
		t.Fatalf("Cline was asked for %v, want deepseek once, then mimo", asked)
	}
}
