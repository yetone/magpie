package sessions

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// wslAntigravity puts an Antigravity app conversation, with its summary
// row naming a folder in the distro, and agy's three conversations in the
// distro's home, as the Windows app connected to WSL leaves them: its
// language server runs in the distro, so ~/.gemini is the distro's.
func wslAntigravity(t *testing.T, home string) (id string) {
	t.Helper()
	id = "37c3c786-9a11-44be-97df-b8fc1ccebc78"
	antigravityFixture(t, home, id)
	db, err := sql.Open("sqlite", filepath.Join(home, ".gemini", "antigravity", "conversation_summaries.db")+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(antigravitySummariesSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO conversation_summaries (conversation_id, title, preview, step_count, last_modified_time, workspace_uris, status, project_id, last_user_input_time, app_data_dir) VALUES (?, 'Login redirect fix', 'Login redirect fix', 2, '2026-11-07 00:12:46.60116+00:00', '[\"file:///home/me/site\"]', 'CASCADE_RUN_STATUS_IDLE', 'default-project', '2026-11-07 00:12:43.784481+00:00', 'antigravity')", id); err != nil {
		t.Fatal(err)
	}
	copyTree(t, "testdata/agy-sessions", filepath.Join(home, ".gemini", "antigravity-cli"))
	return id
}

// @guyng35955 on X: the Antigravity app on Windows connected to a WSL
// Ubuntu showed no usage. Its conversations, in the distro's
// ~/.gemini/antigravity, are listed with the distro, read only, under the
// distro's own folder (not a Windows path), and their calls count; so do
// agy's in the distro's ~/.gemini/antigravity-cli.
func TestWSLAntigravityApp(t *testing.T) {
	inZone(t, 0)
	home, _ := wslDistro(t)
	id := wslAntigravity(t, home)

	ss := List(0)
	s := find(t, ss, "antigravity", id)
	if s.WSL != "Ubuntu" || s.Title != "Login redirect fix" || s.Cwd != "/home/me/site" || !s.ReadOnly || s.Resume != "" || s.Tokens.zero() {
		t.Fatalf("the distro's Antigravity conversation: %+v", s)
	}
	if n := count(ss, "agy"); n != 3 {
		t.Fatalf("want agy's 3 conversations in the distro, got %d", n)
	}
	two := find(t, ss, "agy", agyTwoTurns)
	if two.WSL != "Ubuntu" || two.Title != "Hello from the fake." || two.Cwd != "/work/proj" || two.Tokens.zero() {
		t.Fatalf("the distro's agy conversation: %+v", two)
	}
	if want := `wsl.exe -d 'Ubuntu' --cd '/work/proj' -e sh -lc 'exec ${SHELL:-sh} -lic ''agy --conversation ` + agyTwoTurns + `'''`; two.Resume != want {
		t.Fatalf("agy resume\n got %s\nwant %s", two.Resume, want)
	}

	want, err := antigravityRead(filepath.Join(home, ".gemini", "antigravity", "conversations", id+".db"))
	if err != nil || len(want) == 0 {
		t.Fatalf("the fixture's calls: %v %v", want, err)
	}
	calls := map[string]int{}
	for _, c := range Calls(time.Time{}) {
		calls[c.Agent]++
	}
	if calls["antigravity"] != len(want) {
		t.Fatalf("the distro's Antigravity calls: %d, want %d (%v)", calls["antigravity"], len(want), calls)
	}
	if calls["agy"] == 0 {
		t.Fatalf("the distro's agy calls aren't counted: %v", calls)
	}

	// magpie starts again: the listing kept on disk is read first, and
	// carries the conversation's row, so it keeps its name and folder
	wslSettle()
	Saved()
	Reset()
	s = find(t, List(0), "antigravity", id)
	if s.WSL != "Ubuntu" || s.Title != "Login redirect fix" || s.Cwd != "/home/me/site" || !s.ReadOnly {
		t.Fatalf("after a restart: %+v", s)
	}
}

// A distro whose ~/.gemini/antigravity is this computer's own (a link to
// Windows' folder, which distro.open opens there) is read once, as this
// computer's: its calls aren't counted twice.
func TestWSLAntigravityLinkedHomeOnce(t *testing.T) {
	home, _ := wslDistro(t)
	own, _ := os.UserHomeDir()
	id := "37c3c786-9a11-44be-97df-b8fc1ccebc78"
	antigravityFixture(t, own, id)
	WSLHomes = func() []WSLHome {
		return []WSLHome{{Distro: "Ubuntu", Home: home, Running: true, Open: func(p string) string {
			if rel, err := filepath.Rel(filepath.Join(home, ".gemini"), p); err == nil && filepath.IsLocal(rel) {
				return filepath.Join(own, ".gemini", rel)
			}
			return p
		}}}
	}
	Reset()
	n := 0
	for _, s := range List(0) {
		if s.Agent == "antigravity" {
			n++
			if s.WSL != "" {
				t.Fatalf("listed as the distro's: %+v", s)
			}
		}
	}
	if n != 1 {
		t.Fatalf("want the conversation once, got %d", n)
	}
}

// A database in a distro is read through Windows' share of it, where an
// open in place answers SQLITE_BUSY (openAppStore), so it is read as
// OpenUntouched does; this computer's own, and a distro's folder that
// leads to Windows' drive, are read in place.
func TestOnWSLShare(t *testing.T) {
	for p, want := range map[string]bool{
		`\\wsl.localhost\Ubuntu\home\me\.gemini\antigravity\conversation_summaries.db`: true,
		`\\WSL.LOCALHOST\Ubuntu-24.04\home\me\x.db`:                                    true,
		`\\wsl$\Ubuntu\home\me\x.db`:                                                   true,
		`//wsl.localhost/Ubuntu/home/me/x.db`:                                          true,
		`C:\Users\me\.gemini\antigravity\conversation_summaries.db`:                    false,
		`\\server\wsl.localhost\x.db`:                                                  false,
		`/home/me/.gemini/antigravity/conversation_summaries.db`:                       false,
	} {
		if got := onWSLShare(p); got != want {
			t.Errorf("onWSLShare(%s) = %v, want %v", p, got, want)
		}
	}
}
