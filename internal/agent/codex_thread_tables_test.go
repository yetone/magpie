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
		// the user's own table a thread was started on goes through magpie
		// too, its other keys as they were (TestCodexTakesThreadTables)
		if m := table("mine"); m["base_url"] != v1 || m["name"] != "mine" || strings.Count(read(), "[model_providers.custom]") != 1 {
			t.Fatalf("auth %q: user's table not through magpie or custom twice:\n%s", auth, read())
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

// A Codex thread started on a provider table of the user's own (or one a
// newer CC Switch names after its card) is reopened on that table. With a
// magpie model on, Codex sent "apikeyfan/gpt-6.1-sol" straight to the
// relay, which answered 404 "not supported by any configured account",
// while a new thread worked (a report relayed by the owner, 10-26). The
// table goes through magpie while a magpie model is on and gets its own
// base URL back with Codex's own model; a table no thread was started on,
// and one a profile names, stay as they are.
func TestCodexTakesThreadTables(t *testing.T) {
	for _, auth := range []string{`{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`, ""} {
		conf := "model = \"gpt-5.4\"\n\n" +
			"[model_providers.apikeyfan]\nname = \"apikeyfan\"\nbase_url = \"https://slb.relay.example\"\nwire_api = \"responses\"\nexperimental_bearer_token = \"sk-relay\"\n\n" +
			"[model_providers.unused]\nname = \"unused\"\nbase_url = \"https://unused.example/v1\"\n\n" +
			"[model_providers.work]\nname = \"work\"\nbase_url = \"https://work.example/v1\"\n\n" +
			"[profiles.w]\nmodel_provider = \"work\"\n"
		home, read := codexHome(t, auth, conf)
		dir := filepath.Join(home, ".codex")
		codexThreads(t, dir, "apikeyfan", "work")
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
		v1 := here(home).v1()
		if a := table("apikeyfan"); a["base_url"] != v1 || a["experimental_bearer_token"] != "sk-relay" || a["wire_api"] != "responses" {
			t.Fatalf("auth %q: thread's table not through magpie:\n%s", auth, read())
		}
		if table("unused")["base_url"] != "https://unused.example/v1" || table("work")["base_url"] != "https://work.example/v1" {
			t.Fatalf("auth %q: other tables changed:\n%s", auth, read())
		}
		if d := cx.Check(); d != "" {
			t.Fatalf("auth %q: check: %s", auth, d)
		}
		// pointed away again: the row says so
		if err := edit.SetTOMLKey(path, "model_providers.apikeyfan", "base_url", "https://slb.relay.example"); err != nil {
			t.Fatal(err)
		}
		if d := cx.Check(); !strings.Contains(d, "[model_providers.apikeyfan]") {
			t.Fatalf("auth %q: check: %q", auth, d)
		}
		if err := cx.Sync(); err != nil {
			t.Fatal(err)
		}
		// Codex's own model: the relay's URL is back
		if err := cx.Fields[0].Set("gpt-5.4"); err != nil {
			t.Fatal(err)
		}
		if table("apikeyfan")["base_url"] != "https://slb.relay.example" {
			t.Fatalf("auth %q: own model:\n%s", auth, read())
		}
	}
}

// JsonParser on X: T3 Code starts Codex with a provider of its own on the
// command line (`-c model_providers.openai_token_sharing={…, auth =
// {command = …}}`), so its threads name openai_token_sharing while
// config.toml never had that table. magpie wrote one for them, pointed at
// itself with experimental_bearer_token, Codex merged it with T3's -c, and
// refused the whole config: "model_providers.openai_token_sharing: provider
// auth cannot be combined with experimental_bearer_token". An id
// config.toml never had is the other app's: magpie writes no table for it,
// and takes out the one an older magpie wrote, wired or not. CC Switch's
// ids, and a table of the user's that config.toml lost, are still written
// (#1372).
func TestCodexLeavesOtherAppsProviders(t *testing.T) {
	for _, auth := range []string{`{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`, ""} {
		conf := "model = \"gpt-5.4\"\n\n" +
			"[model_providers.relay]\nname = \"relay\"\nbase_url = \"https://relay.example/v1\"\nexperimental_bearer_token = \"sk-relay\"\n"
		home, read := codexHome(t, auth, conf)
		dir := filepath.Join(home, ".codex")
		codexThreads(t, dir, "openai_token_sharing", "custom", "relay")
		path := filepath.Join(dir, "config.toml")
		cx := codex(home)
		// seen once, then lost, as CC Switch's rewrite loses a table
		if err := cx.Sync(); err != nil {
			t.Fatal(err)
		}
		if err := edit.DelTOMLTable(path, "model_providers.relay"); err != nil {
			t.Fatal(err)
		}
		if err := cx.Fields[0].Set("fake/m1"); err != nil {
			t.Fatal(err)
		}
		if err := cx.Sync(); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(read(), "openai_token_sharing") {
			t.Fatalf("auth %q: wrote another app's provider:\n%s", auth, read())
		}
		v1 := here(home).v1()
		for _, id := range []string{"custom", "relay"} {
			if tb, _ := edit.GetTOMLTable(path, "model_providers."+id); tb["base_url"] != v1 {
				t.Fatalf("auth %q: %s's table not written back:\n%s", auth, id, read())
			}
		}
	}
}

// The table an older magpie wrote for T3 Code's provider is taken out at
// the next sync or Disconnect, connected or not; one the user made, one with keys of
// theirs added, and one Codex is on stay.
func TestCodexDropsStrayThreadTables(t *testing.T) {
	for _, wire := range []bool{true, false} {
		home, read := codexHome(t, "", "model = \"gpt-5.4\"\n")
		dir := filepath.Join(home, ".codex")
		codexThreads(t, dir, "openai_token_sharing", "other_app", "kept")
		path := filepath.Join(dir, "config.toml")
		cx := codex(home)
		if wire {
			if err := cx.Fields[0].Set("fake/m1"); err != nil {
				t.Fatal(err)
			}
		}
		at := here(home)
		stray := func(id string) string {
			return "\n[model_providers." + id + "]\nname = \"" + id + "\"\nbase_url = \"" + at.v1() + "\"\nwire_api = \"responses\"\nexperimental_bearer_token = \"" + at.gwKey() + "\"\n"
		}
		raw, err := edit.Read(path)
		if err != nil {
			t.Fatal(err)
		}
		conf := string(raw) + stray("openai_token_sharing") + stray("other_app") + "env_key = \"MINE\"\n" + stray("kept") +
			"\n[model_providers.mine]\nname = \"mine\"\nbase_url = \"https://mine.example/v1\"\nwire_api = \"responses\"\nexperimental_bearer_token = \"sk-mine\"\n" +
			"\n[profiles.k]\nmodel_provider = \"kept\"\n"
		if err := edit.WriteAtomic(path, []byte(conf)); err != nil {
			t.Fatal(err)
		}
		// a Disconnect (an uninstall's, a quit's) with no Sync before it
		// takes it out too, wired or not
		if err := cx.Fields[0].Set(""); err != nil {
			t.Fatal(err)
		}
		got := read()
		if strings.Contains(got, "openai_token_sharing") {
			t.Fatalf("wired %v: stray table kept:\n%s", wire, got)
		}
		for _, id := range []string{"other_app", "kept", "mine"} {
			if !strings.Contains(got, "[model_providers."+id+"]") {
				t.Fatalf("wired %v: %s taken out:\n%s", wire, id, got)
			}
		}
	}
}

// The same stray table goes at the next Sync with nothing else to do.
func TestCodexSyncDropsStrayThreadTable(t *testing.T) {
	home, read := codexHome(t, "", "model = \"gpt-5.4\"\n")
	codexThreads(t, filepath.Join(home, ".codex"), "openai_token_sharing")
	at := here(home)
	path := filepath.Join(home, ".codex", "config.toml")
	conf := "model = \"gpt-5.4\"\n\n[model_providers.openai_token_sharing]\nname = \"openai_token_sharing\"\nbase_url = \"" + at.v1() + "\"\nwire_api = \"responses\"\nexperimental_bearer_token = \"" + at.gwKey() + "\"\n"
	if err := edit.WriteAtomic(path, []byte(conf)); err != nil {
		t.Fatal(err)
	}
	if err := codex(home).Sync(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(read(), "openai_token_sharing") {
		t.Fatalf("\n%s", read())
	}
}
