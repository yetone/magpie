package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCursorEnvRunsInMagpiesHome(t *testing.T) {
	got := strings.Join(cursorEnv([]string{"PATH=/bin", "HOME=/Users/me", "CURSOR_CONFIG_DIR=/x", "CURSOR_API_KEY=k", "KEEP=1"}, "/cache/home"), "\n")
	for _, bad := range []string{"HOME=/Users/me", "CURSOR_CONFIG_DIR=", "CURSOR_API_KEY="} {
		if strings.Contains(got, bad) {
			t.Fatalf("kept %s in %q", bad, got)
		}
	}
	for _, want := range []string{"PATH=/bin", "KEEP=1", "HOME=/cache/home"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %s in %q", want, got)
		}
	}
}

func TestCursorHandsToolCallsOverOneAnswerAtATime(t *testing.T) {
	old := cursorSettle
	cursorSettle = 10 * time.Millisecond
	defer func() { cursorSettle = old }()
	run := &subscriptionRun{pending: map[string]chan mcpToolResult{}}
	c := &cursorTurn{run: run}
	run.resume = c.resumed
	seg := run.attach()
	c.called("call_a", "read", json.RawMessage(`{"path":"a"}`))
	var kinds []EventKind
	for ev := range seg {
		kinds = append(kinds, ev.Kind)
	}
	if len(kinds) != 4 || kinds[0] != KToolStart || kinds[2] != KUsage || kinds[3] != KStop {
		t.Fatalf("first answer = %v", kinds)
	}
	// made while the caller runs the first: it opens the next answer
	c.called("call_b", "read", nil)
	seg = run.attach()
	c.resumed()
	var ids []string
	for ev := range seg {
		if ev.Kind == KToolStart {
			ids = append(ids, ev.ID)
		}
	}
	if strings.Join(ids, ",") != "call_b" {
		t.Fatalf("next answer calls = %v", ids)
	}
}

func TestSlowToolResultIsCollectedWithWait(t *testing.T) {
	b := newSubscriptionBridge()
	run := &subscriptionRun{bridge: b, token: "tok", pending: map[string]chan mcpToolResult{},
		patience: 20 * time.Millisecond, late: map[string]chan mcpToolResult{}}
	b.runs["tok"] = run
	mux := http.NewServeMux()
	mux.HandleFunc("POST /cb/{token}", b.mcpCall)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	call := func(body string) string {
		res, err := http.Post(srv.URL+"/cb/tok", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out mcpToolResult
		_ = json.NewDecoder(res.Body).Decode(&out)
		return out.Content[0]["text"].(string)
	}
	if got := call(`{"tool_call_id":"call_1","name":"bash","arguments":{}}`); !strings.Contains(got, "still running") || !strings.Contains(got, waitTool) {
		t.Fatalf("slow call answered %q", got)
	}
	if _, err := run.continueWith([]Part{{Kind: ToolResult, CallID: "call_1", Text: "done"}}); err != nil {
		t.Fatal(err)
	}
	if got := call(`{"tool_call_id":"call_w","name":"` + waitTool + `","arguments":{"call":"call_1"}}`); got != "done" {
		t.Fatalf("wait answered %q", got)
	}
}
