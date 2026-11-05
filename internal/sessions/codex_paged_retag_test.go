package sessions

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A paginated Codex thread (history_mode "paginated", Codex 0.166 on) is
// on the provider its row names: Codex lists and resumes it there, never
// reading its files' provider (codex-rs thread-store read_thread).
// RetagPagedCodexThreads moves it by that row alone, of any age, packed
// or not, and an id of any length (a paginated file can't be rewritten
// with a longer one), its files not touched; a legacy thread, whose
// provider is its file's, stays. The Sessions page lists it on the
// provider its row names, so it isn't offered a Move it had.
func TestRetagPagedCodexThreads(t *testing.T) {
	_, codex := manageSetup(t)
	main := filepath.Join(codex, "sessions", "2026", "09", "20", "rollout-2026-09-20T15-48-28-"+mgCX+".jsonl")
	b, err := os.ReadFile(main)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-90 * 24 * time.Hour).Truncate(time.Second)
	os.Chtimes(main, old, old)
	os.WriteFile(filepath.Join(codex, "config.toml"), []byte("model = \"gpt-6\"\nmodel_provider = \"magpie\"\n\n[model_providers.magpie]\nname = \"magpie\"\n"), 0o644)
	p := filepath.Join(codex, "state_5.sqlite")
	db, err := sql.Open("sqlite", p)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE threads (id TEXT PRIMARY KEY, rollout_path TEXT NOT NULL, model_provider TEXT NOT NULL, history_mode TEXT NOT NULL DEFAULT 'legacy');
		INSERT INTO threads VALUES (?, ?, 'openai', 'paginated'), ('legacy-one', 'y', 'openai', 'legacy'), ('relayed', 'z', 'my-relay', 'paginated')`, mgCX, main); err != nil {
		t.Fatal(err)
	}
	row := func(id string) string {
		var s string
		if err := db.QueryRow(`SELECT model_provider FROM threads WHERE id = ?`, id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	if m, _ := findManaged(ListAgent("codex"), mgCX); m.Provider != "openai" || m.UsesProvider != "magpie" {
		t.Fatalf("listed %q, uses %q", m.Provider, m.UsesProvider)
	}
	moved, err := RetagPagedCodexThreads(codex, "openai", "magpie")
	if err != nil || len(moved) != 1 || moved[0] != mgCX {
		t.Fatalf("moved %v, %v", moved, err)
	}
	if row(mgCX) != "magpie" || row("legacy-one") != "openai" || row("relayed") != "my-relay" {
		t.Fatalf("rows: %s %s %s", row(mgCX), row("legacy-one"), row("relayed"))
	}
	if b2, _ := os.ReadFile(main); !bytes.Equal(b, b2) {
		t.Fatal("the file was written")
	}
	if fi, _ := os.Stat(main); !fi.ModTime().Equal(old) {
		t.Fatalf("the file's time: %v", fi.ModTime())
	}
	if m, _ := findManaged(ListAgent("codex"), mgCX); m.Provider != "magpie" {
		t.Fatalf("listed on %q after the move, want its row's magpie", m.Provider)
	}
	// an id of another length, and back
	if moved, err := RetagPagedCodexThreads(codex, "my-relay", "magpie"); err != nil || len(moved) != 1 {
		t.Fatalf("moved %v, %v", moved, err)
	}
	if moved, err := RetagPagedCodexThreadIDs(codex, "magpie", "my-relay", []string{"relayed"}); err != nil || len(moved) != 1 || row("relayed") != "my-relay" || row(mgCX) != "magpie" {
		t.Fatalf("moved %v, %v: %s %s", moved, err, row("relayed"), row(mgCX))
	}
	if moved, err := RetagPagedCodexThreads(codex, "magpie", "openai"); err != nil || len(moved) != 1 || row(mgCX) != "openai" {
		t.Fatalf("back: %v, %v, %s", moved, err, row(mgCX))
	}
	// a Codex from before history_mode: its threads go by their files
	if _, err := db.Exec(`ALTER TABLE threads DROP COLUMN history_mode`); err != nil {
		t.Fatal(err)
	}
	if moved, err := RetagPagedCodexThreads(codex, "openai", "magpie"); err != nil || len(moved) != 0 {
		t.Fatalf("moved %v, %v with no history_mode", moved, err)
	}
}
