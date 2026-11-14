package sessions

import (
	"bytes"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

// pagedPackedSetup is @xMopf7RF's case (X, 11-14): a packed, paginated
// rollout from months ago whose session_meta names a provider shorter
// than magpie, with Codex on magpie now. It gives the rollout's path and
// its bytes, and with row, the state database Codex keeps its row in.
func pagedPackedSetup(t *testing.T, row bool) (path string, b []byte, db *sql.DB) {
	t.Helper()
	_, codex := manageSetup(t)
	src := filepath.Join(codex, "sessions", "2026", "09", "20", "rollout-2026-09-20T15-48-28-"+mgCX+".jsonl")
	plain, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(src)
	plain = bytes.Replace(plain, []byte(`"model_provider":"openai"`), []byte(`"model_provider":"zai","history_mode":"paginated"`), 1)
	enc, _ := zstd.NewWriter(nil)
	b = enc.EncodeAll(plain, nil)
	path = filepath.Join(codex, "sessions", "2026", "03", "09", "rollout-2026-03-09T09-25-41-"+mgCX+".jsonl.zst")
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-200 * 24 * time.Hour).Truncate(time.Second)
	os.Chtimes(path, old, old)
	os.WriteFile(filepath.Join(codex, "config.toml"), []byte("model_provider = \"magpie\"\n\n[model_providers.magpie]\nname = \"magpie\"\n"), 0o644)
	if !row {
		return path, b, nil
	}
	db, err = sql.Open("sqlite", filepath.Join(codex, "state_5.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE threads (id TEXT PRIMARY KEY, rollout_path TEXT NOT NULL, model_provider TEXT NOT NULL, history_mode TEXT NOT NULL DEFAULT 'legacy');
		INSERT INTO threads VALUES (?, ?, 'zai', 'paginated')`, mgCX, path); err != nil {
		t.Fatal(err)
	}
	return path, b, db
}

// A paginated rollout keeps its history by byte, so "magpie" can't be
// written in place of a shorter id in it. The move was refused for it
// ("keeps its history by byte, and "magpie" is longer than the provider
// it names: Codex couldn't open it moved", @xMopf7RF on X), though Codex
// reads such a thread's provider from its row, never from its file: it
// moves by its row, its file as it was, and moves back the same way.
func TestMoveCodexProviderPagedByRow(t *testing.T) {
	path, b, db := pagedPackedSetup(t, true)
	row := func() string {
		var s string
		if err := db.QueryRow(`SELECT model_provider FROM threads WHERE id = ?`, mgCX).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	if m, _ := findManaged(ListAgent("codex"), mgCX); m.Provider != "zai" || m.UsesProvider != "magpie" {
		t.Fatalf("listed %q, uses %q", m.Provider, m.UsesProvider)
	}
	mv, err := MoveCodexProvider(mgCX, "magpie")
	if err != nil {
		t.Fatal(err)
	}
	if row() != "magpie" || len(mv.DB) != 1 || len(mv.Files) != 0 || mv.From != "zai" {
		t.Fatalf("row %q, moved %+v", row(), mv)
	}
	if now, _ := os.ReadFile(path); !bytes.Equal(now, b) {
		t.Fatal("the paginated rollout was written")
	}
	if m, _ := findManaged(ListAgent("codex"), mgCX); m.Provider != "magpie" {
		t.Fatalf("listed on %q after the move", m.Provider)
	}
	if _, err := MoveCodexProvider(mgCX, "zai"); err != nil || row() != "zai" {
		t.Fatalf("back: %v, row %q", err, row())
	}
	if now, _ := os.ReadFile(path); !bytes.Equal(now, b) {
		t.Fatal("moving back wrote the rollout")
	}
}

// With no row in Codex's database to move it by, such a thread can't be
// moved: the error says why and what can be done, and nothing is left
// changed, not even a copy in magpie's trash.
func TestMoveCodexProviderPagedNoRow(t *testing.T) {
	path, b, _ := pagedPackedSetup(t, false)
	_, err := MoveCodexProvider(mgCX, "magpie")
	var stuck *StuckError
	if !errors.As(err, &stuck) || stuck.From != "zai" || stuck.To != "magpie" {
		t.Fatalf("err %v", err)
	}
	for _, want := range []string{"history by byte", "no row", "stays on zai", "Delete"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("%q doesn't say %q", err, want)
		}
	}
	if now, _ := os.ReadFile(path); !bytes.Equal(now, b) {
		t.Fatal("a refused move changed the file")
	}
	if left := providerTrash(t); len(left) != 0 {
		t.Fatalf("trash kept %v", left)
	}
}
