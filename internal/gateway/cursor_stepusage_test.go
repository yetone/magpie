package gateway

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// A step that calls the caller's tools has no TurnEndedUpdate before the
// Run is closed, so its usage is the dashboard's usage event of the Run's
// conversation (#676): read, cache included, not guessed with none. A
// prompt read whole from the cache is no reason to guess either.
func TestCursorToolStepReadsItsUsageEvent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	wait := cursorStepWait
	cursorStepWait.first, cursorStepWait.every, cursorStepWait.until = 0, 10*time.Millisecond, 2*time.Second
	defer func() { cursorStepWait = wait }()

	var mu sync.Mutex
	var conv string
	var ran, shown time.Time // the first Run's start, its event's time
	asks, closed := 0, false
	text := false // the Run answers with text and its turn's usage
	quit := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/aiserver.v1.DashboardService/GetFilteredUsageEvents" {
			mu.Lock()
			defer mu.Unlock()
			asks++
			var q map[string]any
			json.NewDecoder(r.Body).Decode(&q)
			if r.Header.Get("Authorization") != "Bearer tok" || q["startDate"] == nil {
				http.Error(w, `{"code":"unauthenticated"}`, 401)
				return
			}
			ev := func(c string, at time.Time, u map[string]any) map[string]any {
				return map[string]any{"timestamp": strconv.FormatInt(at.UnixMilli(), 10), "model": "grok-4.7-medium-fast", "conversationId": c, "tokenUsage": u}
			}
			evs := []map[string]any{
				ev("another-conversation", ran.Add(time.Second), map[string]any{"inputTokens": 9, "outputTokens": 9}),
				ev(conv, ran.Add(-30*time.Second), map[string]any{"inputTokens": 8, "outputTokens": 8}), // an earlier Run of it
			}
			if asks >= 2 && closed && shown.IsZero() { // it shows a moment after the Run is closed
				shown = ran.Add(2500 * time.Millisecond)
			}
			if !shown.IsZero() {
				evs = append(evs, ev(conv, shown, map[string]any{"inputTokens": 41, "outputTokens": 133, "cacheReadTokens": 4224, "totalCents": 0.6}))
			}
			json.NewEncoder(w).Encode(map[string]any{"usageEventsDisplay": evs, "totalUsageEventsCount": len(evs)})
			return
		}
		f, err := readConnectFrame(bufio.NewReader(r.Body))
		if err != nil {
			http.Error(w, `{"code":"not_found"}`, 404)
			return
		}
		mu.Lock()
		ran, closed = time.Now(), false
		for _, rf := range pbFields(f.data) {
			if rf.num == 1 {
				conv = pbStr(pbFields(rf.data), 5)
			}
		}
		mu.Unlock()
		cursorDuplex(w)
		w.Header().Set("Content-Type", "application/connect+proto")
		if text {
			w.Write(cursorUpdate(1, pb{}.str(1, "READY")))
			w.Write(cursorUpdate(14, pb{}.varint(1, 900).varint(2, 3).varint(3, 900)))
			w.(http.Flusher).Flush()
			return
		}
		w.Write(cursorUpdate(1, pb{}.str(1, "probing")))
		w.Write(cursorUpdate(27, pb{}.varint(1, 1)))
		w.Write(cursorCallFrame(1, "call_1", "cache_probe", "ok"))
		w.(http.Flusher).Flush()
		// as Cursor does: the Run stays open for the call's result, which
		// never comes; it ends once the client closes it
		gone := make(chan struct{})
		go func() { io.Copy(io.Discard, r.Body); close(gone) }()
		select {
		case <-gone:
		case <-quit:
		}
		mu.Lock()
		closed = true
		mu.Unlock()
	}))
	defer up.Close()
	defer close(quit)
	tok, ver, agent, api := cursorToken, cursorVersion, cursorAgent, cursorAPI
	cursorToken = func() (string, error) { return "tok", nil }
	cursorVersion = func() string { return "cli-test" }
	cursorAgent, cursorAPI = up.URL, up.URL
	defer func() { cursorToken, cursorVersion, cursorAgent, cursorAPI = tok, ver, agent, api }()
	forgetCursorEndpoint(t)

	type chatUsage struct {
		Prompt     int `json:"prompt_tokens"`
		Completion int `json:"completion_tokens"`
		Details    struct {
			Cached int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	}
	ask := func() (string, chatUsage) {
		t.Helper()
		body := `{"model":"x","stream":false,"prompt_cache_key":"thread-676","tools":[{"type":"function","function":{"name":"cache_probe","parameters":{"type":"object"}}}],` +
			`"messages":[{"role":"system","content":"` + strings.Repeat("reference ", 2000) + `"},{"role":"user","content":"CALL"}]}`
		w := httptest.NewRecorder()
		var u Usage
		New().serveCursor(w, httptest.NewRequest("POST", "/", strings.NewReader(body)), provider.Chat, "auto", []byte(body), &u)
		var out struct {
			Choices []struct {
				Finish string `json:"finish_reason"`
			} `json:"choices"`
			Usage chatUsage `json:"usage"`
		}
		if json.Unmarshal(w.Body.Bytes(), &out) != nil || w.Code != 200 || len(out.Choices) != 1 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		return out.Choices[0].Finish, out.Usage
	}

	finish, u := ask()
	if finish != "tool_calls" || u.Prompt != 4265 || u.Details.Cached != 4224 || u.Completion != 133 {
		t.Fatalf("a tool step's usage is %s %+v, want the event's: 4265 in, 4224 of it cached, 133 out", finish, u)
	}
	// counted once: the next step of the conversation doesn't take it again
	cursorStepWait.until = 100 * time.Millisecond
	if _, u = ask(); u.Details.Cached != 0 || u.Prompt == 4265 {
		t.Fatalf("the event was counted twice: %+v", u)
	}

	// a text turn: Cursor's own count, the dashboard never asked; all of
	// the prompt from the cache adds no guess
	text = true
	mu.Lock()
	before := asks
	mu.Unlock()
	if finish, u = ask(); finish != "stop" || u.Prompt != 900 || u.Details.Cached != 900 || asks != before {
		t.Fatalf("a text turn's usage is %s %+v (%d asks), want 900, all cached, no ask", finish, u, asks-before)
	}
}
