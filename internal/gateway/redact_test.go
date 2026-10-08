package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/redact"
	"github.com/yetone/magpie/internal/settings"
)

// Each mode needs a fresh process: the redaction key is cached after its
// first use, so another test's masking would hide initialization races.
func TestConcurrentRedactedCountTokens(t *testing.T) {
	const modeEnv = "MAGPIE_TEST_REDACT_CONCURRENCY"
	mode := os.Getenv(modeEnv)
	if mode == "" {
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		// a child that hangs prints its stacks before this run's own deadline
		// does, rather than at a fixed 20s a loaded machine can spend on
		// starting it
		timeout := "0"
		if d, ok := t.Deadline(); ok {
			timeout = (time.Until(d) * 9 / 10).Round(time.Second).String()
		}
		for _, mode := range []string{"shared", "new_per_request"} {
			t.Run(mode, func(t *testing.T) {
				t.Parallel() // independent processes, each with a cold key cache
				cmd := exec.Command(exe, "-test.run=^TestConcurrentRedactedCountTokens$", "-test.timeout="+timeout)
				cmd.Env = append(os.Environ(), modeEnv+"="+mode)
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("concurrent first requests: %v\n%s", err, out)
				}
			})
		}
		return
	}

	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	const workers = 8
	const key = "sk-proj-abcdEFGH1234ijklMNOP5678qrst"
	const reply = `{"input_tokens":17}`
	bodies := make(chan string, workers)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read upstream request: %v", err)
			w.WriteHeader(500)
			return
		}
		bodies <- string(body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, reply)
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}, Anthropic: up.URL}); err != nil {
		t.Fatal(err)
	}
	if err := settings.Save(settings.Settings{Redact: true}); err != nil {
		t.Fatal(err)
	}
	var shared http.Handler
	if mode == "shared" {
		shared = New().Handler()
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			h := shared
			if h == nil {
				h = New().Handler()
			}
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/v1/messages/count_tokens", strings.NewReader(
				`{"model":"fake/m1","messages":[{"role":"user","content":"`+key+`"}]}`))
			h.ServeHTTP(rec, req)
			if rec.Code != 200 || rec.Body.String() != reply {
				t.Errorf("count reply: %d %s", rec.Code, rec.Body.String())
			}
		}()
	}
	close(start)
	wg.Wait()
	close(bodies)
	first, calls := "", 0
	for body := range bodies {
		calls++
		if !json.Valid([]byte(body)) || strings.Contains(body, key) || !strings.Contains(body, "{{API_KEY_") {
			t.Errorf("upstream received an unmasked or invalid request: %s", body)
		}
		if first == "" {
			first = body
		} else if body != first {
			t.Errorf("same secret produced different placeholders: %s / %s", first, body)
		}
	}
	if calls != workers {
		t.Fatalf("upstream calls: %d, want %d", calls, workers)
	}
	if key, err := os.ReadFile(filepath.Join(settings.Dir(), "redact.key")); err != nil || len(key) != 32 {
		t.Fatalf("persisted redaction key: %d bytes, error %v", len(key), err)
	}
}

// the vendor sees placeholders; the agent gets its key back, even from a
// stream that splits a placeholder and a protocol it was translated from
func TestRedactedRequest(t *testing.T) {
	const key = "sk-proj-abcdEFGH1234ijklMNOP5678qrst"
	var got []byte
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		p := regexp.MustCompile(`\{\{API_KEY_[a-z2-7]{8}\}\}`).FindString(string(got))
		if p == "" {
			p = "none"
		}
		chunk := func(s string) string {
			b, _ := json.Marshal(map[string]any{"id": "c1", "model": "m1", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": s}}}})
			return "data: " + string(b)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		split := min(9, len(p))
		io.WriteString(w, sse(chunk("it is "+p[:split]), chunk(p[split:]), `data: {"id":"c1","model":"m1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`, "data: [DONE]"))
	}))
	defer up.Close()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}, Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	req := `{"model":"fake/m1","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"my key is ` + key + `"}]}`

	// off: as it was
	if code, body := post(t, "/v1/messages", req); code != 200 || !strings.Contains(string(got), key) || !strings.Contains(body, "it is none") {
		t.Fatalf("off: %d %s, sent: %s", code, body, got)
	}

	if err := settings.Save(settings.Settings{Redact: true}); err != nil {
		t.Fatal(err)
	}
	code, body := post(t, "/v1/messages", req)
	if code != 200 || strings.Contains(string(got), key) || !strings.Contains(string(got), "{{API_KEY_") {
		t.Fatalf("%d, sent: %s", code, got)
	}
	var text strings.Builder
	for _, e := range events(body) {
		if d, ok := e["delta"].(map[string]any); ok {
			s, _ := d["text"].(string)
			text.WriteString(s)
		}
	}
	if text.String() != "it is "+key {
		t.Fatalf("text %q in:\n%s", text.String(), body)
	}
}

func TestCountTokensRedacted(t *testing.T) {
	const key = "sk-proj-abcdEFGH1234ijklMNOP5678qrst"
	cases := []struct {
		name     string
		settings settings.Settings
		value    string
		prefix   string
	}{
		{"secrets", settings.Settings{Redact: true}, key, "{{API_KEY_"},
		{"personal", settings.Settings{RedactPersonal: true}, "13812345678", "{{PHONE_"},
		{"words", settings.Settings{RedactWords: []string{"Nightjar"}}, "Nightjar", "{{TERM_"},
		{"rules", settings.Settings{Redact: true, RedactRules: []redact.Rule{{Kind: "GW_KEY", Prefix: "acme-"}}}, "acme-Zx9ab12cdEF", "{{GW_KEY_"},
		{"rules_off", settings.Settings{RedactRules: []redact.Rule{{Kind: "GW_KEY", Prefix: "acme-"}}}, "acme-Zx9ab12cdEF", ""},
		{"off", settings.Settings{}, key, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fake{ctype: "application/json", reply: `{"input_tokens":17}`}
			setup(t, provider.Anthropic, f)
			if err := settings.Save(tc.settings); err != nil {
				t.Fatal(err)
			}
			req := `{"model":"fake/m1","system":"` + tc.value + `","messages":[{"role":"user","content":"` + tc.value + `"}],` +
				`"tools":[{"name":"lookup","description":"` + tc.value + `","input_schema":{"type":"object"}}]}`
			code, body := post(t, "/v1/messages/count_tokens", req)
			if code != 200 || body != f.reply {
				t.Fatalf("count reply: %d %q, want %q", code, body, f.reply)
			}
			if f.calls != 1 || f.path != "/v1/messages/count_tokens" || !json.Valid(f.got) || modelOf(f.got) != "m1" {
				t.Fatalf("upstream: %d calls to %s, body %s", f.calls, f.path, f.got)
			}
			if tc.prefix == "" {
				if strings.Count(string(f.got), tc.value) != 3 {
					t.Fatalf("redaction off changed content: %s", f.got)
				}
			} else if strings.Contains(string(f.got), tc.value) || strings.Count(string(f.got), tc.prefix) != 3 {
				t.Fatalf("system, message and tool description must be masked: %s", f.got)
			}
		})
	}
}

func TestCountTokensEstimateRedacted(t *testing.T) {
	const contents = `{"contents":[{"role":"user","parts":[{"text":"13812345678"}]}]}`
	cases := []struct {
		name, path, body, field string
		masked                  int
	}{
		{"anthropic_relay", "/v1/messages/count_tokens", `{"model":"fake/m1","messages":[{"role":"user","content":"13812345678"}]}`, "input_tokens", 4},
		{"anthropic_unknown", "/v1/messages/count_tokens", `{"model":"unknown","messages":[{"role":"user","content":"13812345678"}]}`, "input_tokens", 4},
		{"gemini", "/v1beta/models/fake/m1:countTokens", contents, "totalTokens", 2},
		{"gemini_wrapped", "/v1beta/models/fake/m1:countTokens", `{"generateContentRequest":` + contents + `}`, "totalTokens", 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fake{}
			setup(t, provider.Chat, f)
			// Anthropic counts the 18-byte placeholder for this 11-byte
			// phone. Gemini only estimates locally, so keeps the original.
			for _, mode := range []struct {
				name string
				on   bool
				want int
			}{{"off", false, 2}, {"on", true, tc.masked}} {
				t.Run(mode.name, func(t *testing.T) {
					if err := settings.Save(settings.Settings{RedactPersonal: mode.on}); err != nil {
						t.Fatal(err)
					}
					code, body := post(t, tc.path, tc.body)
					var counts map[string]int
					if err := json.Unmarshal([]byte(body), &counts); err != nil || code != 200 || counts[tc.field] != mode.want {
						t.Fatalf("estimate: %d %s, want %s=%d (decode: %v)", code, body, tc.field, mode.want, err)
					}
					if f.calls != 0 {
						t.Fatalf("local estimate made %d upstream calls", f.calls)
					}
				})
			}
		})
	}
}
