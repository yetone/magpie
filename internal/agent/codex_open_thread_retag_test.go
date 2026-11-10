package agent

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/filelock"
)

// hubo31377762 on X: switching Codex's provider between magpie and OpenAI
// left the running Codex session unusable. The Codex app holds the writer
// lock of the chat on screen, so no move touched its row, and Codex 0.167
// answers a thread/resume for a chat of another provider with
// modelProviderMismatch. The app's app-server is ended when the route
// changes, and the chat is resumed by the next one, so the held rows move
// to the provider Codex is on just before that end.
func TestCodexOpenThreadsMoveBeforeAppServerEnds(t *testing.T) {
	for _, c := range []struct {
		name, from, to string
		used           bool
		paged          bool
	}{
		{"paginated openai to magpie", "openai", "magpie", true, true},
		{"paginated magpie to openai", "magpie", "openai", false, true},
		{"legacy openai to magpie", "openai", "magpie", true, false},
		{"legacy magpie to openai", "magpie", "openai", false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			const id = "019a0000-0000-7000-8000-0000000000c1"
			const other = "019a0000-0000-7000-8000-0000000000c2"
			home, read := codexHome(t, `{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`, "")
			dir := filepath.Join(home, ".codex")
			path := codexThreadOn(t, dir, id, c.from)
			if c.paged {
				packThread(t, path) // quiet, and packed by Codex as it loads it
			} else {
				now := time.Now() // a legacy chat written to just now: no sync moves it
				os.Chtimes(path, now, now)
			}
			codexThreadOn(t, dir, other, c.from) // closed: not held
			if c.paged {
				db, err := sql.Open("sqlite", filepath.Join(dir, "state_5.sqlite"))
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				if _, err := db.Exec(`ALTER TABLE threads ADD COLUMN history_mode TEXT NOT NULL DEFAULT 'legacy'`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`UPDATE threads SET history_mode = 'paginated' WHERE id = ?`, id); err != nil {
					t.Fatal(err)
				}
			}
			locks := filepath.Join(dir, "thread-writer-locks")
			os.MkdirAll(locks, 0o755)
			lf, err := os.OpenFile(filepath.Join(locks, id+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			defer lf.Close()
			if got, err := filelock.TryLock(lf); !got {
				t.Fatal("lock:", err)
			}
			defer filelock.Unlock(lf)

			cx := codex(home)
			if err := cx.Fields[0].Set("fake/m1"); err != nil {
				t.Fatal(err)
			}
			watchCodexThreads(t)
			t.Cleanup(func() { codexUsedUp = func() bool { return false } })
			codexUsedUp = func() bool { return c.used }
			if err := cx.Sync(); err != nil {
				t.Fatal(err)
			}
			wantCfg := strings.Contains(read(), `model_provider = "magpie"`)
			if wantCfg != c.used {
				t.Fatalf("config on magpie = %v, want %v", wantCfg, c.used)
			}
			if p := codexRowOn(t, dir, id); p != c.from {
				t.Fatalf("a chat the app holds was moved by the Sync: %q", p)
			}

			otherBefore := codexRowOn(t, dir, other)
			codexRetagOpenThreads(dir)

			if p := codexRowOn(t, dir, id); p != c.to {
				t.Fatalf("open chat on %q after the app-server's end was prepared, want %q", p, c.to)
			}
			if p := codexRowOn(t, dir, other); p != otherBefore {
				t.Fatalf("a chat nothing holds was moved here: %q", p)
			}
			if !c.paged {
				b, _ := os.ReadFile(path)
				if !strings.Contains(string(b), `"model_provider":"`+c.to+`"`) {
					t.Fatalf("legacy rollout still names %s:\n%s", c.from, b)
				}
			}
		})
	}
}
