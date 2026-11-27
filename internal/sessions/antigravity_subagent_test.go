package sessions

import (
	"bytes"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// Taytay_19891213 on X: a call of an Antigravity subagent's sits in the
// subagent's conversation database and in its parent's, under the same
// response id (step metadata 9.11), and Usage counted it once for each.
// The parent here is #2190's real conversation (four calls, each with its
// own response id); the subagent's database holds the parent's step 3 as
// it was written, and a call of its own: step 3's row with only its
// response id changed, so the same time and tokens. The shared call
// counts once, on Calls and on the Usage page's per-file reads alike; the
// subagent's own call, alike in all but its response id, is a call of its
// own.
func TestAntigravityAppSubagentCallOnce(t *testing.T) {
	claude, _ := setup(t)
	home := filepath.Join(filepath.Dir(claude), "home")
	const parent, sub = "37c3c786-9a11-44be-97df-b8fc1ccebc78", "5d0f3a2e-7c41-4b8e-9a6d-2f1e8c4b7a90"
	parentPath := antigravityFixture(t, home, parent)
	subPath := filepath.Join(filepath.Dir(parentPath), sub+".db")

	pdb, err := sql.Open("sqlite", parentPath)
	if err != nil {
		t.Fatal(err)
	}
	defer pdb.Close()
	var shared, gen []byte
	if err := pdb.QueryRow("SELECT metadata FROM steps WHERE idx = 3").Scan(&shared); err != nil {
		t.Fatal(err)
	}
	if err := pdb.QueryRow("SELECT data FROM gen_metadata WHERE idx = 1").Scan(&gen); err != nil {
		t.Fatal(err)
	}
	const response = "ap7NasizAqfoz7IPiqSy0AE" // step 3's, as written
	if bytes.Count(shared, []byte(response)) != 1 {
		t.Fatalf("step 3's metadata doesn't hold its response id %q once", response)
	}
	own := bytes.Replace(shared, []byte(response), []byte("zz7NasizAqfoz7IPiqSy0AE"), 1)

	sdb, err := sql.Open("sqlite", subPath+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer sdb.Close()
	if _, err := sdb.Exec(antigravitySchema); err != nil {
		t.Fatal(err)
	}
	for idx, b := range [][]byte{shared, own} {
		if _, err := sdb.Exec("INSERT INTO steps (idx, step_type, metadata) VALUES (?, 15, ?)", idx, b); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := sdb.Exec("INSERT INTO gen_metadata (idx, data, size) VALUES (1, ?, ?)", gen, len(gen)); err != nil {
		t.Fatal(err)
	}

	calls := map[string]int{}
	var tokens Tokens
	for _, c := range Calls(time.Time{}) {
		if c.Agent != "antigravity" {
			continue
		}
		calls[c.Session]++
		tokens.add(c.Tokens)
	}
	if n := calls[parent] + calls[sub]; n != 5 {
		t.Errorf("Calls counted %d (by session %v), want 5: the parent's 4 and the subagent's own, the shared one once", n, calls)
	}
	// #2190's four, and the subagent's own (step 3's tokens again)
	want := Tokens{Input: 13317 + 14184 + 15531 + 4254 + 14184, Output: 621 + 164 + 194 + 276 + 164, CacheRead: 18561}
	if tokens != want {
		t.Errorf("Calls' tokens %+v, want %+v", tokens, want)
	}

	// The Usage page reads each file on its own (ReadCallSource) and
	// counts a message id once over them all (usage's visibleLocal).
	ids := map[string]int{}
	files := 0
	for _, s := range CallSources() {
		if s.Agent != "antigravity" {
			continue
		}
		files++
		for _, c := range ReadCallSource(s) {
			ids[c.Msg]++
		}
	}
	if files != 2 {
		t.Fatalf("%d Antigravity call sources, want the parent's and the subagent's", files)
	}
	if len(ids) != 5 {
		t.Errorf("%d distinct call ids over both files, want 5: %v", len(ids), ids)
	}
	twice := 0
	for id, n := range ids {
		if id == "" {
			t.Error("a call with no id to count it once by")
		}
		if n > 1 {
			twice++
		}
	}
	if twice != 1 {
		t.Errorf("%d call ids in both files, want the shared call's alone: %v", twice, ids)
	}
}
