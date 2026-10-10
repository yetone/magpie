package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// A Gemini reply that says nothing — a STOP with no parts, only reasoning,
// or no candidate at all — is a failure, asked again and then told the
// agent as an error in its own protocol, never a turn ended as it should
// (#667: antigravity/gemini-3.8-flash handed pi content [] and zero usage
// as a normal stop, and the agent's run ended mid-task). Antigravity's
// Code Assist and Factory's Gemini route share the decoder and translate;
// Factory's is the one a test can point at a fake upstream.
func TestGeminiEmptyReplyIsAnError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))

	f := &fake{t: t}
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	t.Cleanup(provider.FactoryBaseForTest(up.URL, up.URL+"/eu"))
	auth, _ := json.Marshal(map[string]any{
		"accessToken": "tok", "refreshToken": "r",
		"expiresAt": time.Now().Add(time.Hour).UnixMilli(),
		"orgId":     "org_D", "activeOrganizationId": "fac_D", "email": "d@example.com",
	})
	dir := filepath.Dir(provider.Path())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal([]map[string]any{{
		"agent": "factory", "user": "d@example.com", "plan": "pro", "on": true, "auth": json.RawMessage(auth),
	}})
	if err := os.WriteFile(filepath.Join(dir, "logins.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	const model = "factory/gemini-3.8-flash"
	asks := []struct{ name, path, body, ended string }{
		{"chat", "/v1/chat/completions", `{"model":"` + model + `","stream":true,"messages":[{"role":"user","content":"hi"}]}`, `"finish_reason":"stop"`},
		{"anthropic", "/v1/messages", `{"model":"` + model + `","stream":true,"max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`, `"end_turn"`},
		{"responses", "/v1/responses", `{"model":"` + model + `","stream":true,"input":"hi"}`, `response.completed`},
	}
	empties := map[string]string{
		"STOP and no parts": `data: {"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":0}}` + "\n\n",
		"only reasoning": `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Let me see.","thought":true}]}}]}` + "\n\n" +
			`data: {"candidates":[{"content":{"role":"model","parts":[{"text":""}]},"finishReason":"STOP"}]}` + "\n\n",
		"no candidate": `data: {"usageMetadata":{"promptTokenCount":0}}` + "\n\n",
	}
	for what, reply := range empties {
		f.reply = reply
		for _, a := range asks {
			f.calls = 0
			code, body := post(t, a.path, a.body)
			if code == 200 && (strings.Contains(body, a.ended) || !strings.Contains(body, "an empty reply")) {
				t.Errorf("%s, %s: a normal end, not an error: %d %s", what, a.name, code, body)
			}
			if f.calls < 2 {
				t.Errorf("%s, %s: asked %d times, not again", what, a.name, f.calls)
			}
		}
		code, body := post(t, "/v1/chat/completions", `{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`)
		if code != 502 || !strings.Contains(body, "an empty reply") {
			t.Errorf("%s, not streamed: %d %s", what, code, body)
		}
	}

	// what says something, or ended at its length, still goes as it came
	for what, reply := range map[string]string{
		"text":                    `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]},"finishReason":"STOP"}]}` + "\n\n",
		"reasoning to its length": `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Let me see.","thought":true}]},"finishReason":"MAX_TOKENS"}]}` + "\n\n",
	} {
		f.reply = reply
		code, body := post(t, "/v1/chat/completions", asks[0].body)
		if code != 200 || strings.Contains(body, "an empty reply") || !strings.Contains(body, `"finish_reason"`) {
			t.Errorf("%s: %d %s", what, code, body)
		}
	}
}

// A Gemini reply that says nothing several times running is asked again
// on the same account before the request's own tries run out: on a long
// conversation antigravity/gemini-3.8-flash ended with only its reasoning
// three times in a row, the error stopped Pi's run, and the user had to
// type "continue" (#667, tianshuo886). One that never says anything still
// ends with the error, asked a bounded number of times.
func TestGeminiEmptyRepliesAskedAgain(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))

	calls, emptyFor := 0, 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		if calls <= emptyFor {
			io.WriteString(w, `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Let me see.","thought":true}]}}]}`+"\n\n"+
				`data: {"candidates":[{"content":{"role":"model","parts":[{"text":""}]},"finishReason":"STOP"}]}`+"\n\n")
			return
		}
		io.WriteString(w, `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]},"finishReason":"STOP"}]}`+"\n\n")
	}))
	t.Cleanup(up.Close)
	t.Cleanup(provider.FactoryBaseForTest(up.URL, up.URL+"/eu"))
	auth, _ := json.Marshal(map[string]any{
		"accessToken": "tok", "refreshToken": "r",
		"expiresAt": time.Now().Add(time.Hour).UnixMilli(),
		"orgId":     "org_D", "activeOrganizationId": "fac_D", "email": "d@example.com",
	})
	dir := filepath.Dir(provider.Path())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal([]map[string]any{{
		"agent": "factory", "user": "d@example.com", "plan": "pro", "on": true, "auth": json.RawMessage(auth),
	}})
	if err := os.WriteFile(filepath.Join(dir, "logins.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	const model = "factory/gemini-3.8-flash"
	for _, a := range []struct{ name, path, body string }{
		{"chat", "/v1/chat/completions", `{"model":"` + model + `","stream":true,"messages":[{"role":"user","content":"hi"}]}`},
		{"anthropic", "/v1/messages", `{"model":"` + model + `","stream":true,"max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`},
	} {
		calls, emptyFor = 0, 3
		code, body := post(t, a.path, a.body)
		if code != 200 || !strings.Contains(body, "hello") || strings.Contains(body, "an empty reply") {
			t.Errorf("%s: three empty replies running weren't asked past: %d calls, %d %s", a.name, calls, code, body)
		}
		if a.name == "anthropic" && strings.Count(body, "event: message_start") != 1 {
			t.Errorf("%s: not one message: %s", a.name, body)
		}

		calls, emptyFor = 0, 1<<20
		code, body = post(t, a.path, a.body)
		if !strings.Contains(body, "an empty reply") {
			t.Errorf("%s: a model that never answers isn't told as the error: %d %s", a.name, code, body)
		}
		if calls > 6 {
			t.Errorf("%s: a model that never answers asked %d times", a.name, calls)
		}
	}
}

func TestLateRestsEmptyReply(t *testing.T) {
	if lateRests("Antigravity: an empty reply (the model answered nothing; try again)") {
		t.Errorf("lateRests should be false for empty reply")
	}
	if !lateRests("some generic provider failure") {
		t.Errorf("lateRests should be true for generic provider failure")
	}
}

// When Antigravity accounts answer Gemini with only thinking and an empty STOP,
// the accounts must not be put into rest backoff, and the routing group must
// fall over to another member to answer (#1221).
func TestGeminiEmptyReplyGroupFailover(t *testing.T) {
	for _, stream := range []bool{true, false} {
		t.Run(map[bool]string{true: "stream", false: "nostream"}[stream], func(t *testing.T) {
			s, _ := antigravityGroupFor(t, []string{"u1@example.com", "u2@example.com"}, "gemini-3.8-flash", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, sse(
					`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"thinking...","thought":true}]}}]}}`,
					`data: {"response":{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"thoughtsTokenCount":5}}}`))
			})
			reqBody := fmt.Sprintf(`{"model":"group/g","stream":%t,"max_tokens":100,"messages":[{"role":"user","content":"hello"}]}`, stream)
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(reqBody)))
			if rec.Code != 200 || !strings.Contains(rec.Body.String(), "from the other") {
				t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
			}
			r := lastRoute(s)
			if len(r.Tries) != 2 {
				t.Fatalf("expected exactly 2 tries, got %d: %+v", len(r.Tries), r.Tries)
			}
			if r.Tries[0].ID != "antigravity" || r.Tries[1].ID != "other" {
				t.Fatalf("expected tries [antigravity, other], got [%s, %s]", r.Tries[0].ID, r.Tries[1].ID)
			}
			for i, try := range r.Tries {
				if try.Rest != nil {
					t.Fatalf("try %d unexpectedly rested: %+v", i, try)
				}
			}
			for _, user := range []string{"u1@example.com", "u2@example.com"} {
				if _, ok := restOf("antigravity@" + user); ok {
					t.Fatalf("antigravity@%s unexpectedly placed in rest", user)
				}
			}
		})
	}
}

