package gateway

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/bridgesid"
	"github.com/yetone/magpie/internal/testenv"
)

// An account can wait for its quota longer than the ordinary idle hour.
// Starting another conversation after a restart must not sweep its kept
// transcript away, whether the index is still on disk or already loaded.
func TestClaudeSweepKeepsQuotaSessions(t *testing.T) {
	for _, state := range []string{"disk", "loaded", "resuming", "transferred", "running"} {
		t.Run(state, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			dir := filepath.Join(t.TempDir(), "projects", "work")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			old := time.Now().Add(-2 * idleLongest)
			for _, id := range []string{"kept", "expired"} {
				path := filepath.Join(dir, id+".jsonl")
				if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(path, old, old); err != nil {
					t.Fatal(err)
				}
			}
			entry := shelfEntry{Session: "kept", Sessions: []string{dir}, Key: "conversation", At: old, Until: time.Now().Add(time.Hour)}
			b, err := json.Marshal(shelfFile{Version: shelfFileVersion, Entries: []shelfEntry{entry}})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(shelfPath()), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(shelfPath(), b, 0o600); err != nil {
				t.Fatal(err)
			}
			bridge := newSubscriptionBridge()
			t.Cleanup(bridge.abortAll)
			if state != "disk" {
				bridge.loadShelf()
			}
			if state == "resuming" || state == "transferred" {
				saved := bridge.shelf[entry.Key]
				saved.gone = state == "transferred"
				delete(bridge.shelf, entry.Key)
				bridge.resuming = map[*savedSession]bool{saved: true}
				defer saved.timer.Stop()
			}
			if state == "running" {
				bridge.shelf[entry.Key].timer.Stop()
				delete(bridge.shelf, entry.Key)
				bridge.runs["active"] = &subscriptionRun{sessionID: entry.Session, sessions: entry.Sessions}
				defer func() {
					// Seeing the file disappear does not synchronize with
					// the sweeper's reads of the bridge's maps.
					bridge.mu.Lock()
					delete(bridge.runs, "active")
					bridge.mu.Unlock()
				}()
			}
			bridge.sweepSessions([]string{dir}, false)
			eventually(t, "the expired session wasn't swept", func() bool {
				_, err := os.Stat(filepath.Join(dir, "expired.jsonl"))
				return os.IsNotExist(err)
			})
			if _, err := os.Stat(filepath.Join(dir, "kept.jsonl")); err != nil {
				t.Fatalf("the session kept until the quota resets was swept: %v", err)
			}
		})
	}
}

func TestClaudeSweepUnknownShelfLeavesFiles(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Dir(shelfPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, data := range []string{`{`, `{"version":999}`} {
		if err := os.WriteFile(shelfPath(), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, known := newSubscriptionBridge().keptSessions(); known {
			t.Fatal("unreadable shelf allowed a destructive sweep")
		}
	}
}

// A gateway that stopped leaves its runs' sessions in the callers' folders'
// projects, where the user's own Claude Code lists them in `claude
// --resume` (YZSR on Discord: 12 of a folder's 14 were the runs'). The
// next gateway removes, from every project, the runs' that are older than
// a session is kept, known by their ids, and leaves the user's own,
// however old, a run's still kept, and a newer run's.
func TestClaudeRunSessionsSweptFromEveryProject(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	config := filepath.Join(home, ".claude")
	dir := filepath.Join(config, "projects", "-Users-me-proj")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * idleLongest)
	mine, left, kept, recent := newUUID(), bridgesid.New(), bridgesid.New(), bridgesid.New()
	if bridgesid.Is(mine) || !bridgesid.Is(left) {
		t.Fatal("the runs' ids aren't told from the user's")
	}
	write := func(sid string, at time.Time) string {
		path := filepath.Join(dir, sid+".jsonl")
		if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
		return path
	}
	minePath, leftPath, keptPath, recentPath := write(mine, old), write(left, old), write(kept, old), write(recent, time.Now())
	if err := os.Mkdir(filepath.Join(dir, left), 0o700); err != nil { // its sub-agents
		t.Fatal(err)
	}
	b := newSubscriptionBridge()
	t.Cleanup(b.abortAll)
	b.runs["active"] = &subscriptionRun{sessionID: kept, sessions: []string{dir}}
	b.sweepRunSessions(config)
	for _, p := range []string{leftPath, filepath.Join(dir, left)} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s stayed: %v", p, err)
		}
	}
	for _, p := range []string{minePath, keptPath, recentPath} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
}
