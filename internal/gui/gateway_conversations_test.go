package gui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/sessions"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

func TestGatewayConversationAPI(t *testing.T) {
	sandboxHome(t)
	sessions.Reset()
	t.Cleanup(sessions.Reset)
	mux := http.NewServeMux()
	sessionRoutes(mux, folderOnly{})
	sessionManageRoutes(mux, folderOnly{})
	request := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		return w
	}
	if w := request("POST", "/api/sessions/recording", `{"on":true}`); w.Code != 200 || !settings.Load().GatewayConversations {
		t.Fatalf("consent: %d %s", w.Code, w.Body)
	}
	usage.Append(usage.Record{Time: time.Now(), Agent: "opencode", Session: "remote-only", Provider: "fake", Model: "m", Status: 200})
	if err := sessions.SaveGatewayTurn(sessions.GatewayTurn{Agent: "opencode", Session: "remote-only", Time: time.Now(), Input: []sessions.Part{{Role: "user", Kind: "text", Text: "remote question"}}}); err != nil {
		t.Fatal(err)
	}
	w := request("GET", "/api/sessions/manage?agent=opencode", "")
	var list struct {
		Sessions  []sessions.Session `json:"sessions"`
		Recording bool               `json:"recording"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if !list.Recording || len(list.Sessions) != 1 || !list.Sessions[0].Transcript || !list.Sessions[0].ReadOnly || list.Sessions[0].Resume != "" {
		t.Fatalf("listing: %s", w.Body)
	}
	w = request("GET", "/api/sessions/transcript?agent=opencode&id=remote-only", "")
	var tr sessions.Transcript
	if err := json.Unmarshal(w.Body.Bytes(), &tr); err != nil {
		t.Fatal(err)
	}
	if tr.Source != "gateway" || len(tr.Parts) != 1 || tr.Parts[0].Text != "remote question" {
		t.Fatalf("transcript: %s", w.Body)
	}
	if w := request("GET", "/api/sessions/transcript?agent=codex&id=remote-only", ""); strings.Contains(w.Body.String(), "remote question") {
		t.Fatal("cross-agent disclosure")
	}
	if w := request("POST", "/api/sessions/recording", `{"clear":true}`); w.Code != 200 {
		t.Fatalf("clear: %s", w.Body)
	}
	w = request("GET", "/api/sessions/transcript?agent=opencode&id=remote-only", "")
	if strings.Contains(w.Body.String(), "remote question") || settings.Load().GatewayConversations {
		t.Fatal("clear retained content or consent")
	}
	if _, ok := usage.GatewaySessionByID("opencode", "remote-only", nil); !ok {
		t.Fatal("clear removed usage")
	}
	guard := webGuard("test-session", "secret", 0, mux)
	w = httptest.NewRecorder()
	guard.ServeHTTP(w, httptest.NewRequest("GET", "/api/sessions/transcript?agent=opencode&id=remote-only", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unguarded transcript: %d", w.Code)
	}
}

func TestGatewaySessionHistoryAndCalendar(t *testing.T) {
	sandboxHome(t)
	sessions.Reset()
	t.Cleanup(sessions.Reset)
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, time.Local)
	for _, offset := range []int{-3, -2, -1} {
		usage.Append(usage.Record{Time: today.AddDate(0, 0, offset), Agent: "opencode", Session: "multi-day", Provider: "fake", Model: "m", Input: 10, Output: 2, Status: 200})
	}
	all := combinedSessionStats(0)
	if all.From != today.AddDate(0, 0, -3).Format(time.DateOnly) || len(all.Sessions) != 1 || all.Sessions[0].Input != 30 {
		t.Fatalf("all-time gateway history missing: %+v", all)
	}
	view := all.Overview("", "", "", 8)
	if len(view.Days) != 4 || view.Days[0] != 1 || view.Days[1] != 1 || view.Days[2] != 1 || view.Days[3] != 0 {
		t.Fatalf("gateway calendar lost active dates: %v", view.Days)
	}
	if len(view.Output) != 4 || view.Output[0] != 2 || view.Output[1] != 2 || view.Output[2] != 2 {
		t.Fatalf("gateway daily output missing: %v", view.Output)
	}
	window := combinedSessionStats(2)
	if len(window.Sessions) != 1 || window.Sessions[0].Input != 10 {
		t.Fatalf("two-day window includes older requests: %+v", window)
	}
}

func TestGatewaySessionsNativeWinsBeforeLimits(t *testing.T) {
	h := sandboxHome(t)
	claude := filepath.Join(h, ".claude")
	t.Setenv("CLAUDE_CONFIG_DIR", claude)
	proj := filepath.Join(claude, "projects", "-work")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for id, offset := range map[string]int{"old-native": -4, "new-native": -1} {
		at := now.AddDate(0, 0, offset).Format(time.RFC3339)
		line := fmt.Sprintf(`{"type":"user","message":{"role":"user","content":"hello"},"uuid":"u","timestamp":%q,"cwd":"/work","sessionId":%q}`+"\n"+
			`{"type":"assistant","message":{"model":"claude-sonnet-5","id":"msg_1","role":"assistant","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":10,"output_tokens":2}},"uuid":"a","timestamp":%q,"cwd":"/work","sessionId":%q}`+"\n", at, id, at, id)
		if err := os.WriteFile(filepath.Join(proj, id+".jsonl"), []byte(line), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sessions.Reset()
	t.Cleanup(sessions.Reset)
	usage.Append(usage.Record{Time: now, Agent: "claude", Session: "old-native", Provider: "fake", Model: "m", Input: 50, Status: 200})
	mux := http.NewServeMux()
	sessionRoutes(mux, folderOnly{})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/sessions?limit=1", nil))
	var out sessionsJSON
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Sessions) != 1 || out.Sessions[0].ID != "new-native" || out.Sessions[0].ReadOnly {
		t.Fatalf("limit exposed a duplicate gateway projection: %s", w.Body)
	}
	st := combinedSessionStats(2)
	if len(st.Sessions) != 1 || st.Sessions[0].ID != "new-native" || st.Sessions[0].Input != 10 {
		t.Fatalf("date window exposed a duplicate gateway projection: %+v", st.Sessions)
	}
}

func TestGatewaySessionsIgnoreLargeGeneratedHistory(t *testing.T) {
	sandboxHome(t)
	sessions.Reset()
	t.Cleanup(sessions.Reset)
	if err := os.MkdirAll(filepath.Dir(usage.Path()), 0700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(usage.Path())
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(f)
	for i := range 150000 {
		if err := enc.Encode(usage.Record{Time: time.Now(), Agent: "claude", Session: fmt.Sprintf("request-%d", i), Model: "m", Input: 1, Status: 200}); err != nil {
			f.Close()
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	sessionRoutes(mux, folderOnly{})
	sessionManageRoutes(mux, folderOnly{})
	for _, path := range []string{"/api/sessions/manage?agent=claude", "/api/sessions"} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		var out struct {
			Sessions []json.RawMessage `json:"sessions"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if w.Code != 200 || len(out.Sessions) != 0 {
			t.Fatalf("generated history leaked into %s: status=%d rows=%d", path, w.Code, len(out.Sessions))
		}
	}
}
