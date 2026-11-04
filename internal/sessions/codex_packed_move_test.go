package sessions

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

// 毅 on Discord: Codex 0.166's app refused to reopen a chat from before
// magpie ("This chat uses openai, but your current provider is magpie").
// RelabelCodexThreads moves the plain rollouts only; a packed one (.zst)
// stayed on OpenAI. MovePackedCodexThreads moves the packed ones with a
// turn since the time given, newest first, a copy of each kept, until the
// files moved come to the budget; an older one, a plain one (Relabel's)
// and one of another provider stay.
func TestMovePackedCodexThreads(t *testing.T) {
	_, home := manageSetup(t)
	const (
		newest = "019a0000-0000-7000-8000-0000000000e1"
		newer  = "019a0000-0000-7000-8000-0000000000e2"
		old    = "019a0000-0000-7000-8000-0000000000e3"
		plain  = "019a0000-0000-7000-8000-0000000000e4"
		theirs = "019a0000-0000-7000-8000-0000000000e5"
	)
	week := time.Now().Add(-8 * 24 * time.Hour).Truncate(time.Second)
	paths := map[string]string{}
	for _, id := range []string{newest, newer, old, plain, theirs} {
		p := "openai"
		if id == theirs {
			p = "custom"
		}
		paths[id] = cxRollout(t, home, "sessions", id, p, week)
		if id != plain {
			pack(t, paths[id], false)
		}
	}
	db := filepath.Join(home, "state_5.sqlite")
	d, err := sql.Open("sqlite", db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`CREATE TABLE threads (id TEXT PRIMARY KEY, rollout_path TEXT NOT NULL, model_provider TEXT NOT NULL, title TEXT NOT NULL, archived INTEGER NOT NULL DEFAULT 0, updated_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for id, ago := range map[string]time.Duration{newest: 8, newer: 9, old: 40, plain: 8, theirs: 8} {
		p := "openai"
		if id == theirs {
			p = "custom"
		}
		// the row names the plain path, as Codex keeps it once packed
		if _, err := d.Exec(`INSERT INTO threads VALUES (?, ?, ?, 't', 0, ?)`, id, paths[id], p, now.Add(-ago*24*time.Hour).Unix()); err != nil {
			t.Fatal(err)
		}
	}
	d.Close()
	since := now.Add(-30 * 24 * time.Hour)

	// a budget the first thread's file reaches: that one alone
	moved, err := MovePackedCodexThreads(home, "openai", "magpie", since, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(moved) != 1 || moved[0].ID != newest || moved[0].Backup == "" {
		t.Fatalf("moved %+v, want the newest alone", moved)
	}
	if _, err := os.Stat(filepath.Join(moved[0].Backup, "move.json")); err != nil {
		t.Fatalf("no copy kept: %v", err)
	}
	// the next call goes on with the rest
	if moved, err = MovePackedCodexThreads(home, "openai", "magpie", since, 64<<20); err != nil || len(moved) != 1 || moved[0].ID != newer {
		t.Fatalf("again: %+v %v", moved, err)
	}
	want := map[string]string{newest: "magpie", newer: "magpie", old: "openai", plain: "openai", theirs: "custom"}
	for id, prov := range want {
		if got, _, _ := threadProvider(t, db, id); got != prov {
			t.Errorf("row %s on %q, want %q", id, got, prov)
		}
		f := paths[id]
		if id != plain {
			f += zstSuffix
		}
		if got := codexMetaProvider(f); got != prov {
			t.Errorf("file %s on %q, want %q", filepath.Base(f), got, prov)
		}
		if fi, _ := os.Stat(f); !fi.ModTime().Equal(week) {
			t.Errorf("%s: time %v, want %v kept", filepath.Base(f), fi.ModTime(), week)
		}
	}
	// the messages are there, and the file is one Codex reads (zstd)
	b, _ := os.ReadFile(paths[newest] + zstSuffix)
	z, _ := zstd.NewReader(nil)
	defer z.Close()
	out, err := z.DecodeAll(b, nil)
	if err != nil || !bytes.Contains(out, []byte("remember: the build uses make")) {
		t.Fatalf("packed rollout after the move: %v\n%s", err, out)
	}
	// nothing left to move
	if moved, err = MovePackedCodexThreads(home, "openai", "magpie", since, 64<<20); err != nil || len(moved) != 0 {
		t.Fatalf("a third time: %+v %v", moved, err)
	}
}

// A state database whose threads have no updated_at (a Codex before it)
// moves none of them.
func TestMovePackedCodexThreadsNoUpdatedAt(t *testing.T) {
	_, home := manageSetup(t)
	const id = "019a0000-0000-7000-8000-0000000000e6"
	day := time.Now().Add(-8 * 24 * time.Hour)
	path := cxRollout(t, home, "sessions", id, "openai", day)
	pack(t, path, false)
	db := cxThreadRows(t, home, map[string]string{id: "openai"})
	d, err := sql.Open("sqlite", db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE threads SET rollout_path = ?`, path); err != nil {
		t.Fatal(err)
	}
	d.Close()
	if moved, err := MovePackedCodexThreads(home, "openai", "magpie", time.Time{}, 64<<20); err != nil || len(moved) != 0 {
		t.Fatalf("moved %+v %v", moved, err)
	}
	if got, _, _ := threadProvider(t, db, id); got != "openai" {
		t.Fatalf("row on %q", got)
	}
}

// hxy920830 on X: Codex had been on a provider of the user's own, a
// relay's table, and once magpie was its provider the app listed those
// threads and refused to open one ("This chat uses myrelay, but your
// current provider is magpie"). An id of another length than magpie's
// can't be written in place, so MoveRecentCodexThreads moves the recent
// ones, plain or packed, each rewritten with a copy kept; an older one
// and another provider's stay.
func TestMoveRecentCodexThreads(t *testing.T) {
	_, home := manageSetup(t)
	const (
		plain  = "019a0000-0000-7000-8000-0000000000f1"
		packed = "019a0000-0000-7000-8000-0000000000f2"
		old    = "019a0000-0000-7000-8000-0000000000f3"
		theirs = "019a0000-0000-7000-8000-0000000000f4"
	)
	week := time.Now().Add(-8 * 24 * time.Hour).Truncate(time.Second)
	paths := map[string]string{}
	on := map[string]string{plain: "myrelay", packed: "myrelay", old: "myrelay", theirs: "custom"}
	for id, p := range on {
		paths[id] = cxRollout(t, home, "sessions", id, p, week)
	}
	pack(t, paths[packed], false)
	db := filepath.Join(home, "state_5.sqlite")
	d, err := sql.Open("sqlite", db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`CREATE TABLE threads (id TEXT PRIMARY KEY, rollout_path TEXT NOT NULL, model_provider TEXT NOT NULL, title TEXT NOT NULL, archived INTEGER NOT NULL DEFAULT 0, updated_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for id, ago := range map[string]time.Duration{plain: 8, packed: 9, old: 40, theirs: 8} {
		if _, err := d.Exec(`INSERT INTO threads VALUES (?, ?, ?, 't', 0, ?)`, id, paths[id], on[id], now.Add(-ago*24*time.Hour).Unix()); err != nil {
			t.Fatal(err)
		}
	}
	d.Close()
	since := now.Add(-30 * 24 * time.Hour)
	moved, err := MoveRecentCodexThreads(home, "myrelay", "magpie", since, 64<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(moved) != 2 || moved[0].ID != plain || moved[1].ID != packed {
		t.Fatalf("moved %+v, want the plain then the packed one", moved)
	}
	for _, mv := range moved {
		if _, err := os.Stat(filepath.Join(mv.Backup, "move.json")); err != nil {
			t.Fatalf("%s: no copy kept: %v", mv.ID, err)
		}
	}
	want := map[string]string{plain: "magpie", packed: "magpie", old: "myrelay", theirs: "custom"}
	for id, prov := range want {
		if got, _, _ := threadProvider(t, db, id); got != prov {
			t.Errorf("row %s on %q, want %q", id, got, prov)
		}
		f := paths[id]
		if id == packed {
			f += zstSuffix
		}
		if got := codexMetaProvider(f); got != prov {
			t.Errorf("file %s on %q, want %q", filepath.Base(f), got, prov)
		}
	}
	if b, _ := os.ReadFile(paths[plain]); !bytes.Contains(b, []byte("remember: the build uses make")) {
		t.Fatalf("plain rollout after the move:\n%s", b)
	}
	// the packed-only move of the same leaves a plain thread
	if moved, err = MovePackedCodexThreads(home, "custom", "magpie", since, 64<<20); err != nil || len(moved) != 0 {
		t.Fatalf("packed-only: %+v %v", moved, err)
	}
}
