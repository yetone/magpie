package agent

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// codexThreadBy writes a thread of Codex's on the provider p that the
// client originator started on model, its rollout a day old and its row in
// the state database, as Codex 0.169 keeps them (originator and model in
// the threads table).
func codexThreadBy(t *testing.T, dir, id, p, originator, model string) string {
	t.Helper()
	sub := filepath.Join(dir, "sessions", "2026", "11", "16")
	os.MkdirAll(sub, 0o755)
	path := filepath.Join(sub, "rollout-2026-11-16T10-00-00-"+id+".jsonl")
	b := `{"timestamp":"2026-11-16T10:00:00.000Z","type":"session_meta","payload":{"id":"` + id + `","timestamp":"2026-11-16T10:00:00.000Z","cwd":"/work/proj","originator":"` + originator + `","cli_version":"0.169.0","source":"vscode","model_provider":"` + p + `","base_instructions":{"text":"x"}}}` + "\n"
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
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS threads (id TEXT PRIMARY KEY, rollout_path TEXT NOT NULL, model_provider TEXT NOT NULL, model TEXT, originator TEXT, updated_at INTEGER NOT NULL DEFAULT 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO threads (id, rollout_path, model_provider, model, originator, updated_at) VALUES (?, ?, ?, ?, ?, ?)`, id, path, p, model, originator, day.Unix()); err != nil {
		t.Fatal(err)
	}
	return path
}

// xxxxpouqqs on X: "t3code 每次重新会话都出现类似于 The 'codex-2/gpt-6.1-sol'
// model is not supported when using Codex with a ChatGPT account". T3
// Code's magpie · Codex instance runs Codex in ~/.codex on magpie by -c
// model_provider alone, so config.toml names no magpie and its threads
// were moved onto OpenAI at magpie's next Sync (bringBack); Codex resumes
// a thread on the provider it was started on, and T3 sent magpie's model
// to ChatGPT. T3's threads stay on magpie, those an earlier magpie moved
// come back, and the Codex CLI's still move to where Codex lists them.
func TestT3CodexThreadsStayOnMagpie(t *testing.T) {
	const (
		t3Mine   = "019a0000-0000-7000-8000-0000000000b1"
		cliMine  = "019a0000-0000-7000-8000-0000000000b2"
		t3Moved  = "019a0000-0000-7000-8000-0000000000b3"
		t3Theirs = "019a0000-0000-7000-8000-0000000000b4"
	)
	table := "\n[model_providers.magpie]\nname = \"magpie\"\nbase_url = \"http://127.0.0.1:3425/v1\"\nwire_api = \"responses\"\n"
	t3With := func(t *testing.T, home, config string) {
		t.Helper()
		t.Setenv("T3CODE_HOME", "")
		d := filepath.Join(home, ".t3", "userdata")
		os.MkdirAll(d, 0o755)
		if err := os.WriteFile(filepath.Join(d, "settings.json"), []byte(`{"providerInstances":{"magpie-codex":{"driver":"codex","enabled":true,"config":{`+config+`"launchArgs":"-c model_provider=\"magpie\""}}}}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("in this Codex home", func(t *testing.T) {
		home, _ := codexHome(t, "", "model = \"gpt-5.4\"\n"+table)
		dir := filepath.Join(home, ".codex")
		t3With(t, home, "")
		minePath := codexThreadBy(t, dir, t3Mine, "magpie", "T3 Code", "codex-2/gpt-6.1-sol")
		codexThreadBy(t, dir, cliMine, "magpie", "codex_cli_rs", "codex-2/gpt-6.1-sol")
		codexThreadBy(t, dir, t3Moved, "openai", "T3 Code", "codex-2/gpt-6.1-sol")
		codexThreadBy(t, dir, t3Theirs, "openai", "T3 Code", "gpt-5.4")
		codex(home).Sync()
		for id, want := range map[string]string{t3Mine: "magpie", cliMine: "openai", t3Moved: "magpie", t3Theirs: "openai"} {
			if p := codexRowOn(t, dir, id); p != want {
				t.Errorf("thread %s on %q, want %q", id, p, want)
			}
		}
		if b, _ := os.ReadFile(minePath); !strings.Contains(string(b), `"model_provider":"magpie"`) {
			t.Fatalf("T3's rollout moved:\n%s", b)
		}
	})
	t.Run("in another Codex home", func(t *testing.T) {
		// the instance runs Codex in ~/.codex-t3: ~/.codex's threads on
		// magpie are no T3 thread of magpie's, and move as before
		home, _ := codexHome(t, "", "model = \"gpt-5.4\"\n"+table)
		dir := filepath.Join(home, ".codex")
		t3With(t, home, `"homePath":"~/.codex-t3",`)
		codexThreadBy(t, dir, t3Mine, "magpie", "T3 Code", "codex-2/gpt-6.1-sol")
		codex(home).Sync()
		if p := codexRowOn(t, dir, t3Mine); p != "openai" {
			t.Errorf("thread on %q, want openai", p)
		}
	})
}
