package sessions

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// devinSetup puts testdata/devin's sessions where Devin's CLI keeps them,
// $XDG_DATA_HOME/devin/cli (%APPDATA%\devin\cli on Windows): sessions.db,
// made from sessions.sql, and the folder store's sessions/<id>. Both are a
// real Devin CLI's (3000.11.234), cut to the messages and with the project
// folder renamed: plucky-switch and poised-trust only in the database,
// cyan-skipjack only in the folder store, and chrome-radish in both, as the
// folder store's first run copies it over.
func devinSetup(t *testing.T) (cli string) {
	t.Helper()
	setup(t)
	cli = DevinCLIDirs()[0]
	copyTree(t, "testdata/devin/cli/sessions", filepath.Join(cli, "sessions"))
	q, err := os.ReadFile("testdata/devin/cli/sessions.sql")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(cli, "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(string(q)); err != nil {
		t.Fatal(err)
	}
	return cli
}

// devinCalls are the Devin calls Calls gives, by request.
func devinCalls(t *testing.T) map[string]Call {
	t.Helper()
	out := map[string]Call{}
	for _, c := range Calls(time.Time{}) {
		if c.Agent == "devin" {
			if _, dup := out[c.Msg]; dup {
				t.Fatalf("request %s counted twice", c.Msg)
			}
			out[c.Msg] = c
		}
	}
	return out
}

// Devin CLI's calls reach the usage ledger (aohan0905 on X): each reply's
// tokens, model and effort, from either of its stores, once a request
// though Devin writes each reply twice and a session can be in both.
func TestDevinCalls(t *testing.T) {
	devinSetup(t)
	cs := devinCalls(t)
	if len(cs) != 7 {
		t.Fatalf("want 7 Devin calls, got %d: %+v", len(cs), cs)
	}
	for _, w := range []struct {
		req, session, model, effort string
		tokens                      Tokens
	}{
		{"d07541eb-60f2-46b4-97b4-eb53c04f19a8", "plucky-switch", "swe-2", "high", Tokens{Input: 2143, Output: 44, CacheRead: 10112}},
		{"29bc2ea6-ee76-4e5d-95c1-ce2be7d94327", "poised-trust", "swe-2", "high", Tokens{Input: 10914, Output: 68, CacheRead: 1344}},
		{"cda0f093-57c7-4c4a-b73e-8f55035b52b7", "poised-trust", "swe-2", "high", Tokens{Input: 2261, Output: 42, CacheRead: 10112}},
		{"7f0d1818-1993-4c66-9fec-d5c5f292062a", "chrome-radish", "claude-haiku-5-5", "low", Tokens{Input: 467, Output: 85, CacheRead: 12633, CacheWrite: 6163}},
		{"03288c50-2fbc-412f-8930-e8e6f0ebcc0e", "chrome-radish", "claude-haiku-5-5", "low", Tokens{Input: 2, Output: 19, CacheRead: 18796, CacheWrite: 635}},
		{"282cedf0-804b-4df6-8d1c-74ce51d981ff", "cyan-skipjack", "claude-haiku-5-5", "low", Tokens{Input: 468, Output: 85, CacheRead: 18796}},
		{"3becd065-65e5-4345-bfa7-d5ccd738876b", "cyan-skipjack", "claude-haiku-5-5", "low", Tokens{Input: 2, Output: 19, CacheRead: 18796, CacheWrite: 636}},
	} {
		c, ok := cs["devin:"+w.req]
		if !ok {
			t.Errorf("request %s (%s) not read", w.req, w.session)
			continue
		}
		if c.Session != w.session || c.Model != w.model || c.Effort != w.effort || c.Tokens != w.tokens || c.Cwd != "/work/proj" {
			t.Errorf("request %s: %+v", w.req, c)
		}
	}
	c := cs["devin:7f0d1818-1993-4c66-9fec-d5c5f292062a"]
	if want := time.Date(2026, 11, 13, 4, 10, 5, 299749000, time.UTC); !c.Time.Equal(want) || c.Millis != 1548 || c.TTFT != 0 {
		t.Errorf("chrome-radish's first call: time %v ms %d ttft %d", c.Time, c.Millis, c.TTFT)
	}
	if c := cs["devin:03288c50-2fbc-412f-8930-e8e6f0ebcc0e"]; c.TTFT != 1171 || c.Millis != 1210 {
		t.Errorf("chrome-radish's second call: ttft %d ms %d", c.TTFT, c.Millis)
	}
}

// A session in both stores is one source, and a source counts each
// request once, so a reader of one source at a time (the Requests page)
// counts a call once too.
func TestDevinSessionListedOnce(t *testing.T) {
	devinSetup(t)
	got := map[string]int{}
	n := 0
	for _, f := range devinCallFiles() {
		got[f.key]++
		// a source on its own counts each request once: Devin writes a
		// reply as two messages with one request_id
		st, ok := readDevinCalls(f)
		if !ok {
			t.Fatalf("%s not read", f.path)
		}
		seen := map[string]bool{}
		for _, c := range st.Calls {
			if seen[c.Msg] {
				t.Fatalf("%s: request %s counted twice", f.path, c.Msg)
			}
			seen[c.Msg] = true
		}
		n += len(st.Calls)
	}
	if n != 7 {
		t.Fatalf("the sources have %d calls, want 7", n)
	}
	want := map[string]int{"devin:plucky-switch": 1, "devin:poised-trust": 1, "devin:chrome-radish": 1, "devin:cyan-skipjack": 1}
	if len(got) != len(want) {
		t.Fatalf("sources %v, want %v", got, want)
	}
	for k, n := range want {
		if got[k] != n {
			t.Fatalf("sources %v, want %v", got, want)
		}
	}
}

// A session the database alone has, read again after Devin adds to it.
func TestDevinDBReadAgain(t *testing.T) {
	cli := devinSetup(t)
	if n := len(devinCalls(t)); n != 7 {
		t.Fatalf("want 7 calls, got %d", n)
	}
	db, err := sql.Open("sqlite", filepath.Join(cli, "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO message_nodes (row_id, session_id, node_id, parent_node_id, chat_message, created_at, metadata)
		VALUES (900, 'plucky-switch', 900, 0, '{"role":"assistant","content":"ok","metadata":{"request_id":"r-new","generation_model":"swe-2-high","created_at":"2026-11-13T05:00:00Z","metrics":{"input_tokens":5,"output_tokens":1,"cache_read_tokens":null,"cache_creation_tokens":null}}}', 1794546000, NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE sessions SET last_activity_at = 1794546000 WHERE id = 'plucky-switch'`); err != nil {
		t.Fatal(err)
	}
	cs := devinCalls(t)
	if c, ok := cs["devin:r-new"]; !ok || c.Tokens != (Tokens{Input: 5, Output: 1}) {
		t.Fatalf("the new call: %+v (of %d)", c, len(cs))
	}
}
