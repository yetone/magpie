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

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/sessions"
	"github.com/yetone/magpie/internal/settings"
)

func TestAutomaticTitleGroupsAPI(t *testing.T) {
	sandboxHome(t)
	t.Setenv("CODEX_HOME", t.TempDir())
	old := served.Swap(nil)
	t.Cleanup(func() { served.Store(old) })
	titleHistory.Lock()
	titleHistory.days = nil
	titleHistory.Unlock()
	at := time.Now().UTC()
	// The optional gateway export uses this same synthetic test key.
	if err := os.MkdirAll(settings.Dir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(settings.Dir(), "codex-title-key"), []byte(strings.Repeat("synthetic-title-key", 2)[:32]), 0600); err != nil {
		t.Fatal(err)
	}
	first := gateway.TitleLink{Scope: "test-local-installation", Prompt: sessions.CodexPromptDigest("test user prompt")}
	reply := first
	reply.Reply = sessions.CodexTitleDigest("完成标题关联测试")
	rows := []gateway.Route{
		{ID: 1, Time: at, Agent: "codex", Session: "main-test", Model: "fake/m1", Provider: "fake", Done: true, Status: 200, TitleLink: &first},
		{ID: 2, Time: at, Agent: "codex", Session: "hidden-title-test", Kind: "thread_title", Model: "openai/gpt-5.6-luna", Provider: "openai", Done: true, Status: 200, Millis: 1000, TitleLink: &reply},
	}
	if file := os.Getenv("TITLE_LINK_FIXTURE_FILE"); file != "" {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &rows); err != nil {
			t.Fatal(err)
		}
	}
	rows = append(rows, gateway.Route{ID: rows[1].ID + 1, Time: rows[1].Time.Add(time.Second), Agent: "codex", Session: "unrelated-chat", Model: "fake/m1", Provider: "fake", Done: true, Status: 200, Order: []gateway.Weighed{}, Tries: []gateway.Try{}})
	day := rows[0].Time.Local().Format("2006-01-02")
	if err := settings.Save(settings.Settings{ModelPrices: map[string]settings.ModelPrice{
		"fake/m1": {Input: new(float64(2)), Output: new(float64(8)), CacheRead: new(float64(0)), CacheWrite: new(float64(0))}, "openai/gpt-5.6-luna": {Input: new(float64(2)), Output: new(float64(8)), CacheRead: new(float64(0)), CacheWrite: new(float64(0))},
	}}); err != nil {
		t.Fatal(err)
	}
	writeHistory := func(rs []gateway.Route) {
		t.Helper()
		os.MkdirAll(gateway.HistoryDir(), 0700)
		var b strings.Builder
		for _, r := range rs {
			data, _ := json.Marshal(r)
			b.Write(data)
			b.WriteByte('\n')
		}
		if err := os.WriteFile(filepath.Join(gateway.HistoryDir(), day+".jsonl"), []byte(b.String()), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeHistory(rows)
	mux := http.NewServeMux()
	traceRoutes(mux)
	request := func(method, path, body string) []byte {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
		return w.Body.Bytes()
	}
	getHistory := func() []routeJSON {
		t.Helper()
		var out struct {
			Routes []routeJSON `json:"routes"`
		}
		json.Unmarshal(request("GET", "/api/gateway/history?day="+day, ""), &out)
		return out.Routes
	}
	input, _ := json.Marshal(map[string]any{"ids": []string{rows[0].Session, rows[1].Session}, "routeIds": []int64{rows[0].ID, rows[1].ID}, "groups": true, "day": day})
	before := getHistory()
	if before[1].ParentSession != "" {
		t.Fatal("matched before actual title write")
	}
	index := filepath.Join(sessions.CodexDir(), "session_index.jsonl")
	applied := rows[1].Time.Add(time.Duration(rows[1].Millis)*time.Millisecond + time.Millisecond)
	nameLine := func(id string) string {
		return fmt.Sprintf("{\"id\":%q,\"thread_name\":\"完成标题关联测试\",\"updated_at\":%q}\n", id, applied.Format(time.RFC3339Nano))
	}
	if err := os.WriteFile(index, []byte(nameLine(rows[0].Session)), 0600); err != nil {
		t.Fatal(err)
	}
	afterRefresh := request("POST", "/api/gateway/session-titles", string(input))
	var after struct {
		Parents map[int64]string  `json:"parents"`
		Matched map[int64]bool    `json:"matched"`
		Names   map[string]string `json:"names"`
	}
	json.Unmarshal(afterRefresh, &after)
	if after.Parents[rows[1].ID] != rows[0].Session || !after.Matched[rows[1].ID] || after.Names[rows[0].Session] != "完成标题关联测试" {
		t.Fatalf("late title refresh: %s", afterRefresh)
	}
	// Name polling must use cached history evidence, even if the log is gone.
	os.Remove(filepath.Join(gateway.HistoryDir(), day+".jsonl"))
	if got := request("POST", "/api/gateway/session-titles", string(input)); string(got) != string(afterRefresh) {
		t.Fatal("name polling reread routing history")
	}
	writeHistory(rows)
	resolved := getHistory()
	if resolved[1].ParentSession != rows[0].Session || resolved[1].Session != rows[1].Session {
		t.Fatal("restart/history native identity changed")
	}
	// The legacy name-only endpoint keeps its existing shape.
	legacy := request("POST", "/api/gateway/session-titles", fmt.Sprintf("{\"ids\":[%q]}", rows[0].Session))
	var names map[string]string
	if json.Unmarshal(legacy, &names) != nil || names[rows[0].Session] != "完成标题关联测试" {
		t.Fatal("legacy names API changed")
	}
	twin := rows[0]
	twin.ID = rows[2].ID + 1
	twin.Session = "simultaneous-twin"
	writeHistory(append(rows, twin))
	getHistory()
	f, _ := os.OpenFile(index, os.O_APPEND|os.O_WRONLY, 0600)
	f.WriteString(nameLine(twin.Session))
	f.Close()
	conflictRefresh := request("POST", "/api/gateway/session-titles", string(input))
	json.Unmarshal(conflictRefresh, &after)
	if after.Parents[rows[1].ID] != "" || after.Matched[rows[1].ID] {
		t.Fatalf("conflict was not revoked: %s", conflictRefresh)
	}
	// The twin still conflicts even when its request isn't in our history.
	writeHistory(rows)
	getHistory()
	unknown := request("POST", "/api/gateway/session-titles", string(input))
	json.Unmarshal(unknown, &after)
	if after.Parents[rows[1].ID] != "" {
		t.Fatal("ignored an unobserved title recipient")
	}
	if dir := os.Getenv("ARTIFACT_DIR"); dir != "" {
		os.MkdirAll(dir, 0700)
		data, _ := json.MarshalIndent(map[string]any{"before": before, "after": resolved, "afterRefresh": json.RawMessage(afterRefresh), "conflictRefresh": json.RawMessage(conflictRefresh)}, "", "  ")
		if err := os.WriteFile(filepath.Join(dir, "title-association.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
