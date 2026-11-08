package gui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/gateway"
)

// A session's details on the Sessions page show how full its context was,
// request by request (liang_yongxiang on Discord: Routing told it for one
// request, the Sessions page not at all). /api/context/session answers one
// session's line: each of its requests the gateway saw, oldest first, over
// every day from its first to its last and none outside them, however many
// other sessions there were since (the Context tab lists the latest 80 and
// would leave it out), and with neither a subagent's prompt nor another
// agent's session of the same id in it.
func TestContextSessionLine(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	mux := keptMux(t)
	ctxKept.Lock()
	ctxKept.days, ctxKept.key = map[string]ctxDay{}, ""
	ctxKept.Unlock()
	reads := map[string]bool{}
	readingDay = func(day string) { reads[day] = true }
	t.Cleanup(func() { readingDay = nil })

	dir := gateway.HistoryDir()
	os.MkdirAll(dir, 0o700)
	now := time.Now()
	dayOf := func(back int) time.Time {
		d := now.AddDate(0, 0, -back)
		return time.Date(d.Year(), d.Month(), d.Day(), 9, 0, 0, 0, time.Local)
	}
	files := map[string]*bytes.Buffer{}
	id := int64(0)
	add := func(back int, agent, session string, chat int, edit func(*gateway.Route)) time.Time {
		id++
		r := ctxRoute(id, agent, session, 20000, chat, 200000, 0)
		r.Time = dayOf(back).Add(time.Duration(id) * time.Second)
		if edit != nil {
			edit(&r)
		}
		day := r.Time.Format("2006-01-02")
		if files[day] == nil {
			files[day] = &bytes.Buffer{}
		}
		j, _ := json.Marshal(r)
		files[day].Write(append(j, '\n'))
		return r.Time
	}
	// a request of another session four days ago, outside the session's days
	add(4, "claude", "older", 1000, nil)
	first := add(3, "claude", "s1", 10000, nil)
	add(3, "claude", "s1", 30000, func(r *gateway.Route) { r.Sub = "explore" })
	add(2, "claude", "s1", 60000, nil)
	add(2, "codex", "s1", 90000, nil)
	last := add(1, "claude", "s1", 140000, nil)
	// 100 sessions since, on its last day after it
	for i := range 100 {
		add(1, "claude", fmt.Sprint("later-", i), 1000, nil)
	}
	for day, b := range files {
		os.WriteFile(filepath.Join(dir, day+".jsonl"), b.Bytes(), 0o600)
	}

	ask := func(agent, sid string, from, to time.Time) *ctxSession {
		t.Helper()
		var got struct {
			Session *ctxSession `json:"session"`
		}
		q := url.Values{"agent": {agent}, "id": {sid}, "from": {from.UTC().Format(time.RFC3339Nano)}, "to": {to.UTC().Format(time.RFC3339Nano)}}
		getJSON(t, mux, "/api/context/session?"+q.Encode(), &got)
		return got.Session
	}
	s := ask("claude", "s1", first, last)
	if s == nil {
		t.Fatal("no line for a session the gateway saw")
	}
	var tokens []int
	for _, p := range s.Points {
		tokens = append(tokens, p.Tokens)
	}
	if fmt.Sprint(tokens) != "[30000 80000 160000]" {
		t.Fatalf("its requests' prompts: %v, want [30000 80000 160000]", tokens)
	}
	if s.Window != 200000 || s.Peak != 160000 || s.Requests != 3 || s.Agent != "claude" || s.Key != "s1" {
		t.Fatalf("session: %+v", s)
	}
	if reads[dayOf(4).Format("2006-01-02")] || reads[dayOf(0).Format("2006-01-02")] {
		t.Fatalf("read days outside the session's: %v", reads)
	}
	if !reads[dayOf(3).Format("2006-01-02")] || !reads[dayOf(1).Format("2006-01-02")] {
		t.Fatalf("didn't read the session's days: %v", reads)
	}

	// the same id under another agent is that agent's own
	if c := ask("codex", "s1", first, last); c == nil || len(c.Points) != 1 || c.Points[0].Tokens != 110000 {
		t.Fatalf("codex's s1: %+v", c)
	}
	// a session the gateway never saw has no line
	if n := ask("claude", "nobody", first, last); n != nil {
		t.Fatalf("a session never seen: %+v", n)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/context/session?agent=claude", nil))
	if rec.Code != 400 {
		t.Fatalf("no id: %d", rec.Code)
	}
}
