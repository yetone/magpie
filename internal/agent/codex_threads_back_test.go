package agent

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/edit"
)

// codexThreadOn writes a thread of Codex's on the provider p, its rollout
// a day old and its row in the state database, as Codex 0.162 keeps them.
func codexThreadOn(t *testing.T, dir, id, p string) string {
	t.Helper()
	sub := filepath.Join(dir, "sessions", "2026", "10", "21")
	os.MkdirAll(sub, 0o755)
	path := filepath.Join(sub, "rollout-2026-10-21T10-00-00-"+id+".jsonl")
	b := `{"timestamp":"2026-10-21T10:00:00.000Z","type":"session_meta","payload":{"id":"` + id + `","timestamp":"2026-10-21T10:00:00.000Z","cwd":"/work/proj","originator":"codex_cli_rs","cli_version":"0.162.1","source":"cli","model_provider":"` + p + `","base_instructions":{"text":"x"}}}` + "\n"
	if err := os.WriteFile(path, []byte(b), 0o644); err != nil {
		t.Fatal(err)
	}
	day := time.Now().Add(-24 * time.Hour)
	os.Chtimes(path, day, day)
	db, err := sql.Open("sqlite", filepath.Join(dir, "state_5.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS threads (id TEXT PRIMARY KEY, rollout_path TEXT NOT NULL, model_provider TEXT NOT NULL, model TEXT, updated_at INTEGER NOT NULL DEFAULT 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO threads (id, rollout_path, model_provider, model, updated_at) VALUES (?, ?, ?, 'fake/m1', ?)`, id, path, p, day.Unix()); err != nil {
		t.Fatal(err)
	}
	return path
}

func codexRowOn(t *testing.T, dir, id string) string {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, "state_5.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var p string
	if err := db.QueryRow(`SELECT model_provider FROM threads WHERE id = ?`, id).Scan(&p); err != nil {
		t.Fatal(err)
	}
	return p
}

// kaizaoji on X: after using magpie with Codex and disconnecting it, Codex
// said a project's earlier context didn't exist. Codex lists only the
// threads of the provider it is on (app-server thread/list, the resume
// picker), and with no ChatGPT sign-in (or an API key, or an account out
// of its allowance) magpie is Codex's provider, so every thread started
// then names "magpie" and was gone once Codex was back on OpenAI. They
// come back to the provider Codex is on: at the Disconnect, and at a sync
// after the provider was taken off by hand. A thread of a provider of the
// user's own (CC Switch's "custom") stays where it is, and none moves
// while Codex is on magpie, at its top or in a profile.
func TestCodexThreadsComeBackOffMagpie(t *testing.T) {
	const (
		mine   = "019a0000-0000-7000-8000-0000000000a1"
		theirs = "019a0000-0000-7000-8000-0000000000a2"
		before = "019a0000-0000-7000-8000-0000000000a3"
	)
	t.Run("disconnect", func(t *testing.T) {
		// run after the TempDir's removal: what it couldn't remove (#3422)
		var home string
		t.Cleanup(func() {
			filepath.Walk(home, func(p string, fi os.FileInfo, err error) error {
				if err == nil && p != home {
					t.Logf("left after the test: %s (%v)", p, fi.ModTime())
				}
				return nil
			})
		})
		home, _ = codexHome(t, "", "model = \"gpt-5.4\"\n")
		dir := filepath.Join(home, ".codex")
		cx := codex(home)
		if err := cx.Fields[0].Set("fake/m1"); err != nil {
			t.Fatal(err)
		}
		if p, _ := edit.GetTOMLTop(filepath.Join(dir, "config.toml"), "model_provider"); p != "magpie" {
			t.Fatalf("provider %q, want magpie as Codex's provider with no sign-in", p)
		}
		minePath := codexThreadOn(t, dir, mine, "magpie")
		codexThreadOn(t, dir, theirs, "custom")
		codexThreadOn(t, dir, before, "openai")
		// connected, magpie's threads are where Codex lists them
		if err := cx.Sync(); err != nil {
			t.Fatal(err)
		}
		if p := codexRowOn(t, dir, mine); p != "magpie" {
			t.Fatalf("moved off magpie while Codex is on it: %q", p)
		}
		if err := cx.Unwire(); err != nil {
			t.Fatal(err)
		}
		for id, want := range map[string]string{mine: "openai", theirs: "custom", before: "openai"} {
			if p := codexRowOn(t, dir, id); p != want {
				t.Errorf("thread %s on %q, want %q", id, p, want)
			}
		}
		if b, _ := os.ReadFile(minePath); !strings.Contains(string(b), `"model_provider":"openai"`) {
			t.Fatalf("rollout:\n%s", b)
		}
	})
	t.Run("by hand", func(t *testing.T) {
		// magpie taken out of config.toml by hand, its table left for its
		// threads, and a profile of the user's on magpie
		table := "\n[model_providers.magpie]\nname = \"magpie\"\nbase_url = \"http://127.0.0.1:3425/v1\"\nwire_api = \"responses\"\n"
		profile := "\n[profiles.mg]\nmodel_provider = \"magpie\"\n"
		home, _ := codexHome(t, "", "model = \"gpt-5.4\"\n"+table+profile)
		dir := filepath.Join(home, ".codex")
		path := filepath.Join(dir, "config.toml")
		codexThreadOn(t, dir, mine, "magpie")
		codexThreadOn(t, dir, theirs, "custom")
		cx := codex(home)
		cx.Sync()
		// its threads are where `codex -p mg` lists them
		if p := codexRowOn(t, dir, mine); p != "magpie" {
			t.Fatalf("moved while a profile is on magpie: %q", p)
		}
		os.WriteFile(path, []byte("model = \"gpt-5.4\"\n"+table), 0o644)
		cx.Sync()
		for id, want := range map[string]string{mine: "openai", theirs: "custom"} {
			if p := codexRowOn(t, dir, id); p != want {
				t.Errorf("thread %s on %q, want %q", id, p, want)
			}
		}
		// CC Switch's provider set by hand: a thread left on magpie goes there
		codexThreadOn(t, dir, before, "magpie")
		os.WriteFile(path, []byte("model = \"gpt-5.4\"\nmodel_provider = \"custom\"\n"+table+"\n[model_providers.custom]\nname = \"custom\"\nbase_url = \"https://relay.example/v1\"\n"), 0o644)
		cx.Sync()
		for id, want := range map[string]string{mine: "openai", theirs: "custom", before: "custom"} {
			if p := codexRowOn(t, dir, id); p != want {
				t.Errorf("thread %s on %q, want %q", id, p, want)
			}
		}
	})
}
