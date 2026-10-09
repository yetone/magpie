package agent

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
)

// codexThreads makes Codex's state database with a thread on each of the
// providers, as Codex 0.161's state_5.sqlite keeps them (the columns that
// matter).
func codexThreads(t *testing.T, dir string, providers ...string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, "state_5.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE threads (id TEXT PRIMARY KEY, rollout_path TEXT NOT NULL, model_provider TEXT NOT NULL, model TEXT)`); err != nil {
		t.Fatal(err)
	}
	for i, p := range providers {
		if _, err := db.Exec(`INSERT INTO threads VALUES (?, 'x', ?, 'gpt-5.4')`, string(rune('a'+i)), p); err != nil {
			t.Fatal(err)
		}
	}
}

// #1372: a thread started through CC Switch keeps model_provider "custom",
// and the Codex app resumes it on that provider. CC Switch rewrites
// config.toml whole when it switches, so its table can be gone, and the
// app then loads no config for the thread: "Model provider `custom` not
// found". While magpie is wired, each provider a thread names that has no
// table gets one pointed at magpie, and keeps it when Codex steps off
// magpie; Codex's built-in ones, magpie's own and a table the user has
// stay as they are, one spelled inline too.
func TestCodexWritesTablesThreadsName(t *testing.T) {
	for _, auth := range []string{`{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`, ""} {
		mine := "[model_providers.mine]\nname = \"mine\"\nbase_url = \"https://mine.example/v1\"\n"
		inline := "model_providers.inl = { name = \"inl\", base_url = \"https://inl.example/v1\" }\n"
		home, read := codexHome(t, auth, "model = \"gpt-5.4\"\n"+inline+"\n"+mine)
		dir := filepath.Join(home, ".codex")
		codexThreads(t, dir, "custom", "openai", "magpie", "mine", "ollama", "cc-switch-2", "custom", "inl")
		path := filepath.Join(dir, "config.toml")
		table := func(id string) map[string]string {
			tb, err := edit.GetTOMLTable(path, "model_providers."+id)
			if err != nil {
				t.Fatal(err)
			}
			return tb
		}
		cx := codex(home)
		if err := cx.Fields[0].Set("fake/m1"); err != nil {
			t.Fatal(err)
		}
		v1, key := here(home).v1(), here(home).gwKey()
		for _, id := range []string{"custom", "cc-switch-2"} {
			if c := table(id); c["base_url"] != v1 || c["wire_api"] != "responses" || c["experimental_bearer_token"] != key {
				t.Fatalf("auth %q: %s not written through magpie:\n%s", auth, id, read())
			}
		}
		for _, id := range []string{"openai", "ollama"} {
			if table(id) != nil {
				t.Fatalf("auth %q: a table for built-in %s:\n%s", auth, id, read())
			}
		}
		// one spelled inline is there too, and isn't written twice
		if !strings.Contains(read(), inline) || strings.Contains(read(), "[model_providers.inl]") {
			t.Fatalf("auth %q: inline provider:\n%s", auth, read())
		}
		if !strings.Contains(read(), mine) || strings.Count(read(), "[model_providers.custom]") != 1 {
			t.Fatalf("auth %q: user's table changed or custom twice:\n%s", auth, read())
		}
		if d := cx.Check(); d != "" {
			t.Fatalf("auth %q: check: %s", auth, d)
		}
		// Codex back on its own model, and reset: the threads still open
		for _, v := range []string{"gpt-5.4", ""} {
			if err := cx.Fields[0].Set(v); err != nil {
				t.Fatal(err)
			}
			if table("custom")["base_url"] != v1 {
				t.Fatalf("auth %q: %q took the table:\n%s", auth, v, read())
			}
		}
	}
}

// Not wired, magpie writes nothing for the threads: Codex isn't magpie's
// to change then.
func TestCodexThreadTablesOnlyWired(t *testing.T) {
	home, read := codexHome(t, "", "model = \"gpt-5.4\"\n")
	codexThreads(t, filepath.Join(home, ".codex"), "custom")
	cx := codex(home)
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(read(), "custom") {
		t.Fatalf("\n%s", read())
	}
}
