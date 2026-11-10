package agent

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/gateway"
)

// sameGroup compares two values of a field, one of them read back from the
// agent's own settings where Claude Code's [1m] mark rides on the group's
// id: the marked one is the group magpie set, not another group — the
// gateway serves either (#750's comparison, with the mark on one side).
func TestSameGroupTakesTheOneMMarkOff(t *testing.T) {
	for _, pair := range [][2]string{
		{"group/auto-gpt-5", "group/auto-gpt-5[1m]"},
		{"group/auto-gpt-5[1m]", "group/auto-gpt-5"},
		{"group/auto-gpt-5[1m]", "magpie/group/auto-gpt-5"},
	} {
		if !sameGroup(pair[0], pair[1]) {
			t.Errorf("sameGroup(%q, %q) = false", pair[0], pair[1])
		}
	}
	if sameGroup("group/auto-gpt-5", "group/auto-other") {
		t.Error("two different groups are the same")
	}
	if sameGroup("group/auto-gpt-5", "a/gpt-5") {
		t.Error("a group and a model are the same")
	}
}

// Something else rewrote Codex's config: the base URL gone while the model
// is still magpie's is unwired, and setting it again wires it back.
func TestCodexDriftUnwired(t *testing.T) {
	home, read := codexHome(t, `{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`, "")
	cx := codex(home)
	if err := cx.Apply("model", "fake/m1"); err != nil {
		t.Fatal(err)
	}
	if d := cx.Drift(); d != nil {
		t.Fatalf("drift right after a set: %+v", d)
	}
	os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte("model = \"fake/m1\"\n"), 0o644)
	d := cx.Drift()
	if d == nil || d.Kind != "unwired" || d.Want != "fake/m1" {
		t.Fatalf("unwired: %+v", d)
	}
	if err := cx.Reapply(); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); !strings.Contains(cfg, "openai_base_url") || cx.Drift() != nil {
		t.Fatalf("reapplied:\n%s", cfg)
	}
}

// A magpie model replaced by one of the agent's own is drift; one magpie
// model for another (the agent's own picker) isn't. Keep forgets it.
func TestDriftReplaced(t *testing.T) {
	home, read := codexHome(t, "", "")
	cx := codex(home)
	if err := cx.Apply("model", "fake/m1"); err != nil {
		t.Fatal(err)
	}
	// a switcher put Codex back on OpenAI, wholesale
	os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte("model = \"gpt-5.5\"\nmodel_provider = \"openai\"\n"), 0o644)
	d := cx.Drift()
	if d == nil || d.Kind != "replaced" || d.Want != "fake/m1" || d.Now != "gpt-5.5" {
		t.Fatalf("replaced: %+v", d)
	}
	if err := cx.Reapply(); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); !strings.Contains(cfg, `model = "fake/m1"`) || !strings.Contains(cfg, `model_provider = "magpie"`) || cx.Drift() != nil {
		t.Fatalf("reapplied:\n%s", cfg)
	}
	os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte("model = \"gpt-5.5\"\n"), 0o644)
	cx.Keep()
	if d := cx.Drift(); d != nil {
		t.Fatalf("kept: %+v", d)
	}
}

// A Codex used since magpie set it, none of whose requests reached the
// gateway, runs round magpie: bypassed, until a request arrives or it is
// set again. A use while magpie wasn't up says nothing.
func TestDriftBypassed(t *testing.T) {
	home, _ := codexHome(t, "", "")
	cx := codex(home)
	defer func(s time.Time) { started = s }(started)
	started = time.Now().Add(-time.Hour)
	// a request reaching the gateway, kept to this run: usage.Saw's record
	// lasts the process, so the next run (-count=2) would start with it
	var saw time.Time
	defer func(f func(string) time.Time) { lastSeen = f }(lastSeen)
	lastSeen = func(string) time.Time { return saw }
	if err := cx.Apply("model", "fake/m1"); err != nil {
		t.Fatal(err)
	}
	// Apply stamped now; put it back to before the prompt
	m := appliedLoad()
	a := m["codex"]
	a.At = time.Now().Add(-10 * time.Minute)
	m["codex"] = a
	appliedSave(m)
	hist := filepath.Join(home, ".codex", "history.jsonl")
	prompt := func(at time.Time) {
		os.WriteFile(hist, []byte(fmt.Sprintf(`{"session_id":"s","ts":%d,"text":"hi"}`+"\n", at.Unix())), 0o600)
	}
	prompt(time.Now().Add(-5 * time.Minute))
	if d := cx.Drift(); d == nil || d.Kind != "bypassed" || d.Want != "fake/m1" {
		t.Fatalf("bypassed: %+v", d)
	}
	saw = time.Now()
	if d := cx.Drift(); d != nil {
		t.Fatalf("a request arrived: %+v", d)
	}
	prompt(time.Now().Add(-time.Minute))
	if d := cx.Drift(); d != nil {
		t.Fatalf("used before the last request: %+v", d)
	}
	started = time.Now()
	prompt(time.Now().Add(-5 * time.Minute))
	if d := cx.Drift(); d != nil {
		t.Fatalf("used while magpie was down: %+v", d)
	}
}

// ifccod7391564 on X: the prompts magpie's bridge gives Claude Code, in
// magpie's own work folder, go into its history.jsonl too. They reached
// magpie, so they are no use round it: only the user's own prompts there
// count as Claude Code bypassing magpie.
func TestClaudeBridgePromptsArentBypass(t *testing.T) {
	home, _ := codexHome(t, "", "")
	managed := claudeManaged
	claudeManaged = func() string { return filepath.Join(home, "managed-settings.json") }
	t.Cleanup(func() { claudeManaged = managed })
	defer func(s time.Time) { started = s }(started)
	started = time.Now().Add(-time.Hour)
	defer func(f func(string) time.Time) { lastSeen = f }(lastSeen)
	lastSeen = func(string) time.Time { return time.Time{} }
	a := claude(home)
	if err := a.Apply("model", "fake/m1"); err != nil {
		t.Fatal(err)
	}
	m := appliedLoad()
	r := m["claude"]
	r.At = time.Now().Add(-10 * time.Minute)
	m["claude"] = r
	appliedSave(m)
	hist := filepath.Join(home, ".claude", "history.jsonl")
	at := time.Now().Add(-5 * time.Minute).UnixMilli()
	var lines string
	for _, project := range []string{
		"/private/var/folders/xz/6c5sgg_n3sgdlpc209dpp2n80000gn/T/magpie-claude-work-501", // the reporter's Mac
		"/tmp/magpie-claude-work-1000/",
		`C:\\Users\\me\\AppData\\Local\\Temp\\magpie-claude-work`,
		"C:/Users/me/AppData/Local/Temp/magpie-claude-work",
		"/mnt/c/Users/me/AppData/Local/Temp/magpie-claude-work",
	} {
		lines += fmt.Sprintf(`{"display":"[Pasted text #1 +30 lines]","timestamp":%d,"project":"%s","sessionId":"s"}`+"\n", at, project)
	}
	os.WriteFile(hist, []byte(lines), 0o600)
	if d := a.Drift(); d != nil {
		t.Fatalf("magpie's own prompts counted as bypass: %+v", d)
	}
	// the user's own prompt, in a folder of theirs, still does: even one
	// whose name only starts like magpie's
	os.WriteFile(hist, []byte(lines+fmt.Sprintf(`{"display":"hi","timestamp":%d,"project":"/Users/me/magpie-claude-workshop","sessionId":"u"}`+"\n", at)), 0o600)
	if d := a.Drift(); d == nil || d.Kind != "bypassed" {
		t.Fatalf("the user's own prompt: %+v", d)
	}
}

// A profile that sets its own provider wins over magpie's top level.
func TestCodexDriftProfile(t *testing.T) {
	home, read := codexHome(t, "", "")
	cx := codex(home)
	if err := cx.Apply("model", "fake/m1"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte("profile = \"work\"\n"+read()+"\n[profiles.work]\nmodel_provider = \"openai\"\n"), 0o644)
	if d := cx.Drift(); d == nil || d.Kind != "unwired" || !strings.Contains(d.Detail, "profile work") {
		t.Fatalf("profile: %+v\n%s", d, read())
	}
}

// Every agent magpie wires through the gateway says when its wiring was
// taken out behind magpie's back — the gateway's URL swapped for another —
// and setting it again puts it back.
func TestDriftUnwiredEveryAgent(t *testing.T) {
	home, _ := codexHome(t, "", "")
	managed := claudeManaged
	claudeManaged = func() string { return filepath.Join(home, "managed-settings.json") }
	t.Cleanup(func() { claudeManaged = managed })
	almaApp := startAlma(t) // Alma keeps its providers in the app
	// OpenHanako gives the model to an agent of its own
	os.MkdirAll(filepath.Join(home, ".hanako", "agents", "hana"), 0o755)
	os.WriteFile(filepath.Join(home, ".hanako", "agents", "hana", "config.yaml"), []byte("agent:\n  name: Hana\n"), 0o644)
	// Craft Agents, once it has run and saved its config
	craftFixture(t, home)
	// AstrBot is connected once it has written its settings
	os.MkdirAll(filepath.Join(home, ".astrbot", "data"), 0o755)
	os.WriteFile(filepath.Join(home, ".astrbot", "data", "cmd_config.json"), []byte(astrbotConfig), 0o644)
	// QwenPaw, once it has written its agents' settings
	qwenpawFixture(t, home)
	for _, a := range All() {
		// agy's gateway is in the command that starts it, in no file (its
		// Check is TestAgy's)
		if a.Check == nil || a.Launch != nil || a.Native != nil {
			continue
		}
		// a WSL agent's files live in the distro, beyond the sandbox home:
		// picking a model here writes the user's real configs
		if a.WSL != "" {
			continue
		}
		t.Run(a.ID, func(t *testing.T) {
			// the field that takes one of magpie's models
			var f Field
			want := ""
			for _, g := range a.Fields {
				for _, o := range g.Options(a.Values()) {
					if o.Ref == "fake/m1" && want == "" {
						f, want = g, o.Value
					}
				}
			}
			// or the one that puts it on magpie as a whole (Claude Desktop)
			for _, g := range a.Fields {
				for _, o := range g.Options(a.Values()) {
					if o.Value == magpieID && want == "" {
						f, want = g, o.Value
					}
				}
			}
			if want == "" {
				t.Fatal("no field takes magpie's models")
			}
			if err := a.Apply(f.Key, want); err != nil {
				t.Fatal(err)
			}
			if d := a.Drift(); d != nil {
				t.Fatalf("drift right after a set: %+v", d)
			}
			// something else points the agent at another server
			n := 0
			filepath.WalkDir(home, func(p string, e os.DirEntry, err error) error {
				if err != nil || e.IsDir() {
					return nil
				}
				b, _ := os.ReadFile(p)
				if s := strings.ReplaceAll(string(b), gateway.URL(), "http://127.0.0.1:9"); s != string(b) {
					os.WriteFile(p, []byte(s), 0o644)
					n++
				}
				return nil
			})
			n += almaApp.repoint(gateway.URL(), "http://127.0.0.1:9")
			if n == 0 {
				t.Fatal("no file holds the gateway's URL")
			}
			d := a.Drift()
			if d == nil || d.Kind != "unwired" {
				t.Fatalf("unwired: %+v", d)
			}
			if err := a.Reapply(); err != nil {
				t.Fatal(err)
			}
			if d := a.Drift(); d != nil {
				t.Fatalf("still drifted after Apply again: %+v", d)
			}
		})
	}
}

// Claude Code's managed settings win over the user's: a base URL there is
// drift magpie can only point out.
func TestClaudeDriftManaged(t *testing.T) {
	home, _ := codexHome(t, "", "")
	managed := claudeManaged
	claudeManaged = func() string { return filepath.Join(home, "managed-settings.json") }
	t.Cleanup(func() { claudeManaged = managed })
	cl := claude(home)
	if err := cl.Apply("model", "fake/m1"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(claudeManaged(), []byte(`{"env":{"ANTHROPIC_BASE_URL":"https://corp.example"}}`), 0o644)
	if d := cl.Drift(); d == nil || d.Kind != "unwired" || !strings.Contains(d.Detail, "managed") {
		t.Fatalf("managed: %+v", d)
	}
}

// The Codex app writes no prompt log, but it logs each model request it
// opens: one sent elsewhere, or refused because magpie wasn't running, is
// drift; one that reached magpie isn't.
func TestCodexDriftReached(t *testing.T) {
	home, _ := codexHome(t, `{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`, "")
	cx := codex(home)
	if err := cx.Apply("model", "fake/m1"); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(home, ".codex", "logs_2.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE logs (id INTEGER PRIMARY KEY AUTOINCREMENT, ts INTEGER NOT NULL, ts_nanos INTEGER NOT NULL,
		level TEXT NOT NULL, target TEXT NOT NULL, feedback_log_body TEXT)`); err != nil {
		t.Fatal(err)
	}
	ws := "ws://" + strings.TrimPrefix(gateway.URL(), "http://") + "/backend-api/codex/responses"
	at := time.Now().Add(2 * time.Second).Unix()
	log := func(level, body string) {
		at++
		db.Exec(`INSERT INTO logs (ts, ts_nanos, level, target, feedback_log_body) VALUES (?, 0, ?, 'codex_api::endpoint::responses_websocket', ?)`,
			at, level, "responses_websocket.connect{}: "+body)
		forgetReached()
	}
	log("INFO", "connecting to websocket: "+ws)
	log("ERROR", "failed to connect to websocket: IO error: Connection refused (os error 61), url: "+ws)
	if d := cx.Drift(); d == nil || d.Kind != "bypassed" || !strings.Contains(d.Detail, "wasn't running") {
		t.Fatalf("refused: %+v", d)
	}
	log("INFO", "connecting to websocket: wss://chatgpt.com/backend-api/codex/responses")
	if d := cx.Drift(); d == nil || d.Kind != "bypassed" || !strings.Contains(d.Detail, "chatgpt.com") {
		t.Fatalf("elsewhere: %+v", d)
	}
	log("INFO", "connecting to websocket: "+ws)
	if d := cx.Drift(); d != nil {
		t.Fatalf("reached magpie: %+v", d)
	}
}

// A magpie on a port of its own (magpie-dev) runs beside the installed one:
// an agent that one wired to its gateway is not drift here, while one wired
// to neither still is.
func TestDriftInstalledMagpie(t *testing.T) {
	t.Setenv("MAGPIE_ADDR", "127.0.0.1:3426")
	home, _ := codexHome(t, "", "")
	cx := codex(home)
	if err := cx.Apply("model", "fake/m1"); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(home, ".codex", "config.toml")
	os.WriteFile(cfg, []byte("openai_base_url = \"http://"+gateway.DefaultAddr+gateway.CodexPath+"\"\nmodel = \"fake/m1\"\n"), 0o644)
	if d := cx.Drift(); d != nil {
		t.Fatalf("the installed magpie's wiring taken for drift: %+v", d)
	}
	os.WriteFile(cfg, []byte("openai_base_url = \"http://127.0.0.1:9999"+gateway.CodexPath+"\"\nmodel = \"fake/m1\"\n"), 0o644)
	if d := cx.Drift(); d == nil || d.Kind != "unwired" {
		t.Fatalf("wired to neither: %+v", d)
	}
}

// Whether the installed magpie wired the agent is looked up in the bytes
// already read, not in a copy of the file and two strings of it: each
// field's drift asked, 5 MB a poll of a Codex config.toml of 1.5 MB.
func TestInstalledWiringReadsNoCopy(t *testing.T) {
	t.Setenv("MAGPIE_ADDR", "127.0.0.1:3426")
	home, _ := codexHome(t, "", "")
	cx := codex(home)
	var cfg strings.Builder
	cfg.WriteString("openai_base_url = \"http://" + gateway.DefaultAddr + gateway.CodexPath + "\"\nmodel = \"fake/m1\"\n")
	for i := range 20000 {
		fmt.Fprintf(&cfg, "[projects.\"/Users/someone/work/project-%05d\"]\ntrust_level = \"trusted\"\n", i)
	}
	path := filepath.Join(home, ".codex", "config.toml")
	if err := os.WriteFile(path, []byte(cfg.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	os.Chtimes(path, old, old)
	if !cx.theInstalled() {
		t.Fatal("the installed magpie's wiring not seen")
	}
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	for range 10 {
		cx.theInstalled()
	}
	runtime.ReadMemStats(&m1)
	if n := m1.TotalAlloc - m0.TotalAlloc; n > 1<<20 {
		t.Fatalf("10 looks at an unchanged config allocated %d bytes", n)
	}
}
