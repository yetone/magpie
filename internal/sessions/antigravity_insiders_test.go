package sessions

import (
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// Insiders keeps the app's SQLite format in ~/.gemini/antigravity-insiders.
// Reuse the real step bytes from #2190 to exercise discovery through both
// Sessions and Usage, with stable absent and then with the same UUID in both.
func TestAntigravityInsidersSessions(t *testing.T) {
	claude, _ := setup(t)
	home := filepath.Join(filepath.Dir(claude), "home")
	stable := filepath.Join(home, ".gemini", "antigravity")
	insiders := filepath.Join(home, ".gemini", "antigravity-insiders")
	const id = "37c3c786-9a11-44be-97df-b8fc1ccebc78"
	const insiderID = "insiders-" + id
	antigravityFixture(t, home, id)
	if err := os.Rename(stable, insiders); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(insiders, "conversation_summaries.db")+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(antigravitySummariesSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO conversation_summaries (conversation_id, title, preview, last_modified_time, workspace_uris, last_user_input_time, app_data_dir) VALUES (?, 'Insiders conversation', '', '2026-11-07 00:12:46+00:00', '[\"file:///work/insiders\"]', '2026-11-07 00:12:43+00:00', 'antigravity-insiders')", id); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(Found(), "antigravity") {
		t.Error("an Insiders-only install is missing from the session reading choice")
	}
	if !slices.Contains(Dirs(), insiders) {
		t.Error("the Insiders session folder is not listed")
	}
	check := func(stableHere bool, title string) {
		t.Helper()
		ss := List(0)
		s := find(t, ss, "antigravity", insiderID)
		if s.Title != title || s.Cwd != filepath.FromSlash("/work/insiders") || !s.ReadOnly || s.Resume != "" {
			t.Errorf("Insiders title/folder/read-only/resume = %q/%q/%v/%q", s.Title, s.Cwd, s.ReadOnly, s.Resume)
		}
		if s.Path != filepath.Join(insiders, "conversations", id+".db") {
			t.Errorf("Insiders path = %q", s.Path)
		}
		want := Tokens{Input: 43032, Output: 979}
		if got := model(s, "gemini-3.8-flash").Tokens; got != want {
			t.Errorf("Insiders model tokens = %+v, want %+v", got, want)
		}
		if got := model(s, "antigravity").Tokens; got != (Tokens{Input: 4254, Output: 276, CacheRead: 18561}) {
			t.Errorf("Insiders call without a model = %+v", got)
		}
		if stableHere {
			st := find(t, ss, "antigravity", id)
			if st.Title != "" || st.Cwd != "" || model(st, "gemini-3.8-flash").Tokens != want {
				t.Error("stable's session picked up Insiders' summary or tokens")
			}
		}
		counts := map[string]int{}
		msgs := map[string]bool{}
		var tokens Tokens
		for _, c := range Calls(time.Time{}) {
			if c.Agent != "antigravity" {
				continue
			}
			counts[c.Session]++
			if c.Session == insiderID {
				tokens.add(c.Tokens)
			}
			if c.Msg == "" || msgs[c.Msg] {
				t.Errorf("stable and Insiders share call id %q", c.Msg)
			}
			msgs[c.Msg] = true
		}
		if counts[insiderID] != 4 || (stableHere && counts[id] != 4) || (!stableHere && counts[id] != 0) {
			t.Errorf("calls by session = %v", counts)
		}
		if tokens != (Tokens{Input: 47286, Output: 1255, CacheRead: 18561}) {
			t.Errorf("Insiders Usage tokens = %+v", tokens)
		}
	}
	check(false, "Insiders conversation")
	antigravityFixture(t, home, id)
	if !slices.Contains(Dirs(), stable) {
		t.Error("the stable app's session folder is not listed")
	}
	check(true, "Insiders conversation")
	if _, err := db.Exec("UPDATE conversation_summaries SET title = 'Renamed Insiders conversation' WHERE conversation_id = ?", id); err != nil {
		t.Fatal(err)
	}
	check(true, "Renamed Insiders conversation")

	if err := SetReads(nil, map[string]bool{"antigravity": false}); err != nil {
		t.Fatal(err)
	}
	if len(antigravityFiles()) != 0 || len(antigravityCallFiles()) != 0 {
		t.Error("turning off Antigravity still discovers app files")
	}
	for _, s := range List(0) {
		if s.Agent == "antigravity" {
			t.Error("turning off Antigravity still lists an app session")
		}
	}
	for _, c := range Calls(time.Time{}) {
		if c.Agent == "antigravity" {
			t.Error("turning off Antigravity still counts an app call")
		}
	}
}
