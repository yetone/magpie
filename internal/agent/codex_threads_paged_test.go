package agent

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/filelock"
	"github.com/yetone/magpie/internal/settings"
)

// Parry_Moody and Cyrus on Discord: Codex on OpenAI through an API key
// before magpie, and for two days every older conversation in the Codex
// app said "继续聊天未成功: This chat uses openai, but your current
// provider is magpie. Switch to openai to continue". Codex 0.166 on keeps
// a thread paginated (history_mode "paginated"), and reads its provider
// from its row in the state database, not from its files (codex-rs
// thread-store read_thread: "Paginated history may contain only a suffix,
// so its display metadata lives in SQLite"); resume refuses a provider
// other than the one Codex is on. The file moves left every packed thread
// older than 30 days, and the rest of a big history past a sync's budget,
// on OpenAI.
//
// Every paginated thread of OpenAI's now moves to magpie by its row
// alone, of any age, its files neither rewritten nor copied, and back at
// the Disconnect the same way. One a Codex has open stays, as its writer
// would put its provider back, and so does a legacy thread past 30 days
// (its provider is its file's, the Sessions page moves it).
func TestCodexPagedThreadsFollowByRow(t *testing.T) {
	const (
		old    = "019a0000-0000-7000-8000-0000000000d1"
		open   = "019a0000-0000-7000-8000-0000000000d2"
		legacy = "019a0000-0000-7000-8000-0000000000d3"
		theirs = "019a0000-0000-7000-8000-0000000000d4"
	)
	home, _ := codexHome(t, "", "model = \"gpt-5.4\"\n")
	dir := filepath.Join(home, ".codex")
	db, err := sql.Open("sqlite", filepath.Join(dir, "state_5.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// as Codex 0.166's migration 0040 adds it
	if _, err := db.Exec(`CREATE TABLE threads (id TEXT PRIMARY KEY, rollout_path TEXT NOT NULL, model_provider TEXT NOT NULL, model TEXT, updated_at INTEGER NOT NULL DEFAULT 0, history_mode TEXT NOT NULL DEFAULT 'legacy')`); err != nil {
		t.Fatal(err)
	}
	// a history past a chunk, all packed and three months old
	var many []string
	for i := range 250 {
		many = append(many, fmt.Sprintf("019a0000-0000-7000-8000-%012x", 0xe00+i))
	}
	ago := time.Now().Add(-90 * 24 * time.Hour)
	stamp := map[string]time.Time{}
	for _, id := range append(append([]string{old, open, legacy}, many...), theirs) {
		p := "openai"
		if id == theirs {
			p = "custom"
		}
		path := codexThreadOn(t, dir, id, p)
		os.Chtimes(path, ago, ago)
		packThread(t, path)
		fi, _ := os.Stat(path + ".zst")
		stamp[id] = fi.ModTime()
		mode := "paginated"
		if id == legacy {
			mode = "legacy"
		}
		if _, err := db.Exec(`UPDATE threads SET history_mode = ?, updated_at = ? WHERE id = ?`, mode, ago.Unix(), id); err != nil {
			t.Fatal(err)
		}
	}
	// a Codex has open (codex-rs rollout writer_lock.rs)
	locks := filepath.Join(dir, "thread-writer-locks")
	os.MkdirAll(locks, 0o755)
	lf, err := os.OpenFile(filepath.Join(locks, open+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := filelock.TryLock(lf); !got {
		t.Fatal("lock:", err)
	}
	cx := codex(home)
	if err := cx.Fields[0].Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if p, _ := edit.GetTOMLTop(filepath.Join(dir, "config.toml"), "model_provider"); p != "magpie" {
		t.Fatalf("provider %q, want magpie as Codex's provider with an API key", p)
	}
	want := map[string]string{old: "magpie", open: "openai", legacy: "openai", theirs: "custom"}
	for _, id := range many {
		want[id] = "magpie"
	}
	check := func(when string) {
		t.Helper()
		for id, w := range want {
			if p := codexRowOn(t, dir, id); p != w {
				t.Errorf("%s: thread %s on %q, want %q", when, id, p, w)
			}
		}
		for id, at := range stamp {
			z := filepath.Join(dir, "sessions", "2026", "10", "21", "rollout-2026-10-21T10-00-00-"+id+".jsonl.zst")
			fi, err := os.Stat(z)
			if err != nil || !fi.ModTime().Equal(at) {
				t.Fatalf("%s: thread %s's file written: %v", when, id, err)
			}
			on := "openai"
			if id == theirs {
				on = "custom"
			}
			if p := zstMetaProvider(t, z); p != on {
				t.Fatalf("%s: thread %s's file names %q", when, id, p)
			}
		}
		if l, _ := filepath.Glob(filepath.Join(settings.Dir(), "trash", "codex-provider", "*")); len(l) > 0 {
			t.Fatalf("%s: copied to the trash: %v", when, l)
		}
	}
	check("connected")
	// let go, the open one follows at the next sync
	filelock.Unlock(lf)
	lf.Close()
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	want[open] = "magpie"
	check("let go")
	if err := cx.Unwire(); err != nil {
		t.Fatal(err)
	}
	for id, w := range want {
		if w == "magpie" {
			want[id] = "openai"
		}
	}
	check("disconnected")
}
