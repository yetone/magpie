package gui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/sessions"
	"github.com/yetone/magpie/internal/usage"
)

// lurx98 on X: the Sessions page's tabs said Codex 6 and Pi 8; Codex's
// tab, opened, said 0 and "no sessions yet", and Pi's said 6 from any
// other tab. Deleting a Pi row titled "Task: …" said "no such session".
// The Codex rollouts were subagent threads whose parent thread was gone,
// listed as no row (keptSubagents) but counted. The Pi ones were a
// pi-subagents scout's calls through the gateway, named by the scout's
// own session id: the lead's row counts them (NativeIDs), yet the Pi
// tab listed them again as a gateway session of their own, which delete
// sent to the lead's files, where no session has that id. Each tab's
// count is the rows it lists, whichever tab is open, and every row a
// tab lists can be deleted.
func TestSessionTabCountsAreTheirRows(t *testing.T) {
	home := sandboxHome(t)
	// Pi 1.4.0 with pi-subagents 0.88.1: the lead and its scout's run
	pi := filepath.Join(home, ".pi", "agent")
	copyTree(t, filepath.Join("..", "sessions", "testdata", "pi-subagents"), pi)
	const scout = "01a1d480-2447-77e7-b4d4-1747dc3bd17c"
	// not written to a moment ago, so it can be deleted
	old := time.Now().Add(-2 * time.Hour)
	filepath.WalkDir(pi, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			err = os.Chtimes(p, old, old)
		}
		return err
	})

	// Codex App: a thread with a subagent of its own, and two subagent
	// threads (one of the other) whose parent thread was deleted
	codex := filepath.Join(home, ".codex")
	t.Setenv("CODEX_HOME", codex)
	at := time.Now().Add(-2 * time.Hour).UTC()
	rollout := func(id, parent string, n int) {
		t.Helper()
		source := `"vscode"`
		if parent != "" {
			source = fmt.Sprintf(`{"subagent":{"thread_spawn":{"parent_thread_id":%q,"depth":1}}}`, parent)
		}
		start := at.Add(time.Duration(n) * time.Minute)
		ts := start.Format(time.RFC3339Nano)
		lines := []string{
			fmt.Sprintf(`{"timestamp":%q,"type":"session_meta","payload":{"id":%q,"session_id":%q,"source":%s,"cwd":"/work"}}`, ts, id, id, source),
			fmt.Sprintf(`{"timestamp":%q,"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"check %s"}]}}`, ts, id),
			fmt.Sprintf(`{"timestamp":%q,"type":"token_usage_record","payload":{"response_id":"r-%s","session_id":%q,"thread_id":%q,"turn_id":"t","usage":{"input_tokens":100,"cached_input_tokens":0,"output_tokens":10,"reasoning_output_tokens":0,"total_tokens":110}}}`, ts, id, id, id),
		}
		p := filepath.Join(codex, "sessions", start.Format("2006/01/02"), "rollout-"+start.Format("2006-01-02T15-04-05")+"-"+id+".jsonl")
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		var b bytes.Buffer
		for _, l := range lines {
			b.WriteString(l + "\n")
		}
		if err := os.WriteFile(p, b.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	rollout("01a1e000-0000-7000-8000-000000000001", "", 0)
	rollout("01a1e000-0000-7000-8000-000000000002", "01a1e000-0000-7000-8000-000000000001", 1)
	rollout("01a1e000-0000-7000-8000-000000000003", "01a1e000-0000-7000-8000-0000000000ff", 2)
	rollout("01a1e000-0000-7000-8000-000000000004", "01a1e000-0000-7000-8000-000000000003", 3)

	// the scout's calls through the gateway, and a session of Pi's seen
	// only through it
	if err := os.MkdirAll(filepath.Dir(usage.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(usage.Path())
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(f)
	enc.Encode(usage.Record{Time: at, Agent: "pi", Provider: "fake", Model: "m", Session: scout, Input: 10, Status: 200})
	enc.Encode(usage.Record{Time: at, Agent: "pi", Provider: "fake", Model: "m", Session: "elsewhere", Input: 10, Status: 200})
	f.Close()
	sessions.Reset()
	t.Cleanup(sessions.Reset)

	mux := http.NewServeMux()
	sessionRoutes(mux, folderOnly{})
	sessionManageRoutes(mux, folderOnly{})
	type view struct {
		Agent  string `json:"agent"`
		Agents []struct {
			Agent string `json:"agent"`
			Count int    `json:"count"`
		} `json:"agents"`
		Sessions []sessions.Managed `json:"sessions"`
	}
	get := func(agent string) view {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/sessions/manage?agent="+agent, nil))
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", agent, w.Code, w.Body)
		}
		var v view
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		if v.Agent != agent {
			t.Fatalf("asked for %s, shown %s", agent, v.Agent)
		}
		return v
	}
	// the rows each tab lists
	want := map[string]int{"codex": 2, "pi": 2}
	rows := map[string][]sessions.Managed{}
	for _, a := range []string{"codex", "pi"} {
		v := get(a)
		rows[a] = v.Sessions
		var ids []string
		for _, s := range v.Sessions {
			ids = append(ids, s.ID)
		}
		if len(v.Sessions) != want[a] {
			t.Errorf("%s lists %v, want %d rows", a, ids, want[a])
		}
		for _, s := range v.Sessions {
			if s.ID == scout {
				t.Errorf("%s lists the scout's calls as a row of their own", a)
			}
		}
	}
	// every tab's count, whichever tab is open
	for _, open := range []string{"codex", "pi", "codex"} {
		for _, a := range get(open).Agents {
			if n, ok := want[a.Agent]; ok && a.Count != n {
				t.Errorf("with %s open, the %s tab says %d, its list %d", open, a.Agent, a.Count, n)
			}
		}
	}
	// every Pi row can be deleted
	var ids []string
	for _, s := range rows["pi"] {
		ids = append(ids, s.ID)
	}
	body, _ := json.Marshal(map[string]any{"agent": "pi", "ids": ids})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/sessions/delete", bytes.NewReader(body)))
	if w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	var out struct {
		Deleted []string          `json:"deleted"`
		Refused []json.RawMessage `json:"refused"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Refused) != 0 || len(out.Deleted) != len(ids) {
		t.Errorf("deleting the Pi rows %v: %s", ids, w.Body)
	}
}
