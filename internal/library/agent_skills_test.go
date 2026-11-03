package library

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/appdir"
)

// Every agent on the Agents page that reads skills has a place for them in
// the library (Kimi Code had none, so a skill never reached it): each its
// own folder, but Kimi Code, whose own would hide Claude Code's from it,
// the shared ~/.agents/skills.
func TestAgentsSkillsFolders(t *testing.T) {
	h := sandbox(t)
	// Hermes's own folder: ~/.hermes, but %LOCALAPPDATA%\hermes on Windows
	// (#2275: the test wrote ~/.hermes there, where no Hermes is)
	hermes, err := filepath.Rel(h, appdir.HermesHome())
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{".kimi/config.toml", ".commandcode/settings.json", ".factory/settings.json", ".cline/data/globalState.json",
		filepath.Join(hermes, "config.yaml"), ".grok/config.toml", ".qoder/settings.json", ".workbuddy/models.json", ".fx/settings.json", ".config/devin/config.json"} {
		write(t, filepath.Join(h, f), "")
	}
	// hermes and Cline installed: the CLI beside the folder (the folder
	// alone is gone, #2177, and no target — TestTargetsLeaveGoneAgentsOut)
	holdCLI(t, h, "hermes")
	holdCLI(t, h, "cline")
	// and WorkBuddy's app, which is how a Mac has it
	holdApp(t, h, "WorkBuddy.app", "com.tencent.workbuddy.mac")
	want := map[string]string{
		"kimi":        ".agents/skills",
		"goose":       ".agents/skills",
		"commandcode": ".commandcode/skills",
		"droid":       ".factory/skills",
		"cline":       ".cline/skills",
		"hermes":      filepath.Join(hermes, "skills"),
		"grok":        ".grok/skills",
		"qoder":       ".qoder/skills",
		"workbuddy":   ".workbuddy/skills",
		"fx":          ".fx/skills",
		"devin":       ".config/devin/skills",
	}
	for id, d := range want {
		tg := targetByID(id)
		if tg == nil || tg.Skills != filepath.Join(h, d) {
			t.Errorf("%s: %+v", id, tg)
		}
	}
	// what Kimi Code reads first of the shared ones, when it's there
	os.MkdirAll(filepath.Join(h, ".config/agents/skills"), 0o755)
	if tg := targetByID("kimi"); tg == nil || tg.Skills != filepath.Join(h, ".config/agents/skills") {
		t.Errorf("kimi with ~/.config/agents/skills: %+v", tg)
	}
	// Alma, there once it has its data folder, reads ~/.config/alma/skills
	// on every system, and Claude Code's, Codex's and the shared ones (#824).
	// Its data folder on Windows is under %APPDATA%, which sandbox clears:
	// without one there is no Alma there. Goose's config moves there with
	// it, so Goose is there too
	app := filepath.Join(h, "AppData", "Roaming")
	t.Setenv("APPDATA", app)
	write(t, filepath.Join(app, "Block", "goose", "config", "config.yaml"), "")
	cfg, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(cfg, "alma"), 0o755)
	if tg := targetByID("alma"); tg == nil || tg.Skills != filepath.Join(h, ".config/alma/skills") ||
		!slices.Contains(tg.SkillsAlso, "claude") || !slices.Contains(tg.SkillsAlso, "codex") || !slices.Contains(tg.SkillsAlso, "goose") {
		t.Errorf("alma: %+v", tg)
	}
	src := filepath.Join(h, "src/skills")
	skill(t, filepath.Join(src, "pdf"), "pdf", "Read PDFs")
	ok(t)(InstallSkills(src, []string{"pdf"}, []string{"alma"}))
	if !ours(filepath.Join(h, ".config/alma/skills/pdf"), "pdf") {
		t.Error("a skill given to Alma isn't in ~/.config/alma/skills")
	}
	if ProjectSkillsDir("kimi") != ".agents/skills" {
		t.Error("kimi reads a project's .agents/skills")
	}
}

// A skill given to Kimi Code is one link in ~/.agents/skills, which Goose
// shares; Codex and Gemini CLI, which read that folder as well, get no
// second link in their own, and get one again once it's not there.
func TestSharedSkillsWrittenOnce(t *testing.T) {
	h := sandbox(t)
	write(t, filepath.Join(h, ".kimi/config.toml"), "")
	src := filepath.Join(h, "src/skills")
	skill(t, filepath.Join(src, "pdf"), "pdf", "Read PDFs")
	shared := filepath.Join(h, ".agents/skills/pdf")
	cx, cl := filepath.Join(h, ".codex/skills/pdf"), filepath.Join(h, ".claude/skills/pdf")

	ok(t)(InstallSkills(src, []string{"pdf"}, []string{"claude", "codex", "kimi"}))
	if !ours(shared, "pdf") {
		t.Error("~/.agents/skills/pdf isn't the library's")
	}
	if !ours(cl, "pdf") {
		t.Error("claude hasn't it")
	}
	if _, err := os.Lstat(cx); !os.IsNotExist(err) {
		t.Error("codex has a second link to it")
	}
	v, err := Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	also := map[string][]string{}
	for _, a := range v.Agents {
		also[a.ID] = a.SkillsAlso
	}
	for id, w := range map[string]string{"codex": "kimi", "gemini": "goose", "kimi": "goose", "goose": "kimi"} {
		if !slices.Contains(also[id], w) {
			t.Errorf("%s reads %s's skills too: %v", id, w, also[id])
		}
	}
	if slices.Contains(also["claude"], "kimi") {
		t.Errorf("claude reads no ~/.agents/skills: %v", also["claude"])
	}

	// Kimi Code (and Goose) off: Codex gets its own again, the shared goes
	ok(t)(SkillAgents("pdf", []string{"claude", "codex"}))
	if !ours(cx, "pdf") {
		t.Error("codex hasn't it")
	}
	if _, err := os.Lstat(shared); !os.IsNotExist(err) {
		t.Error("~/.agents/skills still has it")
	}
	// on again: codex's own goes, not the skill
	ok(t)(SkillAgents("pdf", []string{"claude", "codex", "goose"}))
	if _, err := os.Lstat(cx); !os.IsNotExist(err) || !ours(shared, "pdf") {
		t.Error("not once in ~/.agents/skills")
	}
	if _, err := os.Stat(filepath.Join(src, "pdf/SKILL.md")); err != nil {
		t.Error("the user's folder went")
	}
}

// A skill of the user's own in ~/.agents/skills brought into the library
// stays there as it is, and Kimi Code, whose folder it is, has it without
// a word of its "own skill by that name".
func TestImportSharedSkillForKimi(t *testing.T) {
	h := sandbox(t)
	// goose off: its folder and, since the sandbox puts the agents' CLIs
	// beside them (#2177), its CLI too
	os.RemoveAll(filepath.Join(h, ".config/goose"))
	os.Remove(filepath.Join(h, "bin/goose"))
	os.Remove(filepath.Join(h, "bin/goose.exe"))
	write(t, filepath.Join(h, ".kimi/config.toml"), "")
	shared := filepath.Join(h, ".agents/skills/orca")
	skill(t, shared, "orca", "Orca")
	ok(t)(ImportSkill("orca"))
	if fi, err := os.Lstat(shared); err != nil || !fi.IsDir() {
		t.Errorf("the shared folder's skill: %v", err)
	}
	v, _ := Read(nil)
	if len(v.Skills) != 1 || !slices.Equal(v.Skills[0].Agents, []string{"kimi"}) || len(v.Skills[0].Problems) != 0 {
		t.Errorf("skills: %+v", v.Skills)
	}
	ok(t)(Sync())
}

// Droid and Grok Build read a user-wide AGENTS.md in their folders
// (~/.factory, $GROK_HOME else ~/.grok): the library's instructions reach
// them there, after what the user wrote, which stays.
func TestDroidGrokInstructions(t *testing.T) {
	h := sandbox(t)
	write(t, filepath.Join(h, ".factory/settings.json"), "")
	g := filepath.Join(h, "grok-home")
	t.Setenv("GROK_HOME", g)
	write(t, filepath.Join(g, "config.toml"), "")
	files := map[string]string{"droid": filepath.Join(h, ".factory/AGENTS.md"), "grok": filepath.Join(g, "AGENTS.md")}
	for id, f := range files {
		if tg := targetByID(id); tg == nil || tg.Instructions != f {
			t.Fatalf("%s: %+v", id, tg)
		}
		write(t, f, "# Mine\n")
	}
	shared := "Use tabs."
	ok(t)(SaveInstructions(InstructionsChange{Shared: &shared, Agents: []string{"droid", "grok"}}))
	for id, f := range files {
		if s := read(t, f); s != "# Mine\n\n"+blockBegin+"\nUse tabs.\n"+blockEnd+"\n" {
			t.Errorf("%s's AGENTS.md:\n%s", id, s)
		}
	}
}

// Devin CLI reads its global rules from AGENTS.md in its config folder
// (~/.config/devin, %APPDATA%\devin on Windows), else the singular AGENT.md
// there (#3151): the library's instructions reach it, after what the user
// wrote, which stays, and Devin is offered them rather than listed as having
// no user-wide file.
func TestDevinInstructions(t *testing.T) {
	h := sandbox(t)
	write(t, filepath.Join(h, ".config/devin/config.json"), "{}")
	tg := targetByID("devin")
	if tg == nil {
		t.Fatal("Devin isn't a target")
	}
	f := filepath.Join(tg.Agent.Dir, "AGENTS.md")
	if tg.Instructions != f {
		t.Fatalf("Devin's instructions: %q, want %q before the file is there", tg.Instructions, f)
	}
	write(t, f, "# Mine\n")
	shared := "Use tabs."
	ok(t)(SaveInstructions(InstructionsChange{Shared: &shared, Agents: []string{"devin"}}))
	if s := read(t, f); s != "# Mine\n\n"+blockBegin+"\nUse tabs.\n"+blockEnd+"\n" {
		t.Fatalf("Devin's AGENTS.md:\n%s", s)
	}

	// a user who keeps theirs as AGENT.md has it written there instead
	h = sandbox(t)
	write(t, filepath.Join(h, ".config/devin/config.json"), "{}")
	dir := targetByID("devin").Agent.Dir
	write(t, filepath.Join(dir, "AGENT.md"), "# Mine\n")
	if tg := targetByID("devin"); tg.Instructions != filepath.Join(dir, "AGENT.md") {
		t.Fatalf("Devin with AGENT.md: %q", tg.Instructions)
	}
}

// Qwen Code reads QWEN.md in its folder ($QWEN_HOME, else ~/.qwen) before
// every conversation (wongxy on Discord): the library's instructions reach
// it there, after what the user wrote, which stays; its settings'
// context.fileName, a JSONC list whose first non-empty name Qwen Code
// writes itself, names another file.
func TestQwenInstructions(t *testing.T) {
	h := sandbox(t)
	dir := filepath.Join(h, ".qwen")
	write(t, filepath.Join(dir, "settings.json"), "{\n  \"model\": {\"name\": \"qwen3.8-max\"}\n}\n")
	inst := filepath.Join(dir, "QWEN.md")
	write(t, inst, "# Mine\n")
	if tg := targetByID("qwen"); tg == nil || tg.Instructions != inst {
		t.Fatalf("Qwen Code's instructions: %+v", tg)
	}
	if id, err := Takes("qwen-code", "instructions"); err != nil || id != "qwen" {
		t.Fatalf("Takes(qwen-code): %q %v", id, err)
	}
	shared := "Use tabs."
	ok(t)(SaveInstructions(InstructionsChange{Shared: &shared, Agents: []string{"qwen"}}))
	if s := read(t, inst); s != "# Mine\n\n"+blockBegin+"\nUse tabs.\n"+blockEnd+"\n" {
		t.Fatalf("QWEN.md:\n%s", s)
	}
	if cfg := read(t, filepath.Join(dir, "settings.json")); !strings.Contains(cfg, "qwen3.8-max") {
		t.Fatalf("instructions rewrote the settings:\n%s", cfg)
	}

	// $QWEN_HOME moves the folder, and context.fileName renames the file
	q := filepath.Join(h, "qwen-home")
	t.Setenv("QWEN_HOME", q)
	write(t, filepath.Join(q, "settings.json"), "{\n  // mine\n  \"context\": {\"fileName\": [\"  \", \" AGENTS.md \", \"QWEN.md\"]},\n}\n")
	if tg := targetByID("qwen"); tg == nil || tg.Instructions != filepath.Join(q, "AGENTS.md") {
		t.Fatalf("Qwen Code under QWEN_HOME with context.fileName: %+v", tg)
	}
	write(t, filepath.Join(q, "settings.json"), "{\"context\": {\"fileName\": \"MY.md\"}}")
	if tg := targetByID("qwen"); tg == nil || tg.Instructions != filepath.Join(q, "MY.md") {
		t.Fatalf("context.fileName as one name: %+v", tg)
	}
}

// Kimi Code 2.x, fx, Prime Agent, StepCode and jcode each read one
// user-wide instructions file of their own, which the library writes into
// with the user's own text kept; Prime Agent and StepCode read only the
// first of their names there is, and StepCode its AGENTS.override.md
// instead of any.
func TestSiblingInstructions(t *testing.T) {
	h := sandbox(t)
	write(t, filepath.Join(h, ".kimi-code", "config.toml"), "default_model = \"k3\"\n")
	write(t, filepath.Join(h, ".fx", "settings.json"), "{}")
	write(t, filepath.Join(h, ".prime", "agent", "settings.json"), "{}")
	write(t, filepath.Join(h, ".stepcode", "config.toml"), "defaultModel = \"step-3\"\n")
	write(t, filepath.Join(h, ".jcode", "config.toml"), "")
	// a user's own CLAUDE.md is the one Prime Agent reads, not a new AGENTS.md
	write(t, filepath.Join(h, ".prime", "agent", "CLAUDE.md"), "# Mine\n")
	want := map[string]string{
		"kimi":        filepath.Join(h, ".kimi-code", "AGENTS.md"),
		"fx":          filepath.Join(h, ".fx", "AGENTS.md"),
		"prime-agent": filepath.Join(h, ".prime", "agent", "CLAUDE.md"),
		"stepcode":    filepath.Join(h, ".stepcode", "agent", "AGENTS.md"),
		"jcode":       filepath.Join(h, ".jcode", "prompt-overlay.md"),
	}
	for id, inst := range want {
		if tg := targetByID(id); tg == nil || tg.Instructions != inst {
			t.Fatalf("%s's instructions: %+v, want %s", id, tg, inst)
		}
		if got, err := Takes(id, "instructions"); err != nil || got != id {
			t.Fatalf("Takes(%s): %q %v", id, got, err)
		}
	}
	if tg := targetByID("stepcode"); tg.Override != filepath.Join(h, ".stepcode", "agent", "AGENTS.override.md") {
		t.Fatalf("StepCode's override: %q", tg.Override)
	}
	shared := "Use tabs."
	ok(t)(SaveInstructions(InstructionsChange{Shared: &shared, Agents: []string{"kimi", "fx", "prime-agent", "stepcode", "jcode"}}))
	for id, inst := range want {
		pre := ""
		if id == "prime-agent" {
			pre = "# Mine\n\n"
		}
		if s := read(t, inst); s != pre+blockBegin+"\nUse tabs.\n"+blockEnd+"\n" {
			t.Fatalf("%s's %s:\n%s", id, filepath.Base(inst), s)
		}
	}

	// variables move them: $JCODE_HOME, $STEP_CODING_AGENT_DIR
	j := filepath.Join(h, "jhome")
	t.Setenv("JCODE_HOME", j)
	write(t, filepath.Join(j, "config.toml"), "")
	if tg := targetByID("jcode"); tg == nil || tg.Instructions != filepath.Join(j, "prompt-overlay.md") {
		t.Fatalf("jcode under JCODE_HOME: %+v", tg)
	}
	s := filepath.Join(h, "step", "my-agent")
	t.Setenv("STEP_CODING_AGENT_DIR", s)
	write(t, filepath.Join(h, "step", "config.toml"), "")
	if tg := targetByID("stepcode"); tg == nil || tg.Instructions != filepath.Join(s, "AGENTS.md") || tg.Override != filepath.Join(s, "AGENTS.override.md") {
		t.Fatalf("StepCode under STEP_CODING_AGENT_DIR: %+v", tg)
	}

	// the old kimi-cli reads no user-wide instructions
	t.Setenv("KIMI_CODE_HOME", "")
	if err := os.RemoveAll(filepath.Join(h, ".kimi-code")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(h, ".kimi", "config.toml"), "")
	if tg := targetByID("kimi"); tg == nil || tg.Instructions != "" {
		t.Fatalf("kimi-cli's instructions: %+v", tg)
	}
}

// Qoder, Command Code, CodeBuddy Code and Reasonix read a user-wide
// instructions file in their own folders too: Qoder's renamed by its
// settings' context.fileName, CodeBuddy's the first of its two names there
// is, Reasonix's in its state folder wherever that is moved.
func TestSiblingInstructionsMore(t *testing.T) {
	h := sandbox(t)
	write(t, filepath.Join(h, ".qoder", "settings.json"), "{}")
	write(t, filepath.Join(h, ".commandcode", "settings.json"), "{}")
	write(t, filepath.Join(h, ".codebuddy", "settings.json"), "{}")
	holdCLI(t, h, "codebuddy") // the folder alone is gone (#2177)
	write(t, filepath.Join(h, ".codebuddy", "CODEBUDDY.mdc"), "# Mine\n")
	want := map[string]string{
		"qoder":       filepath.Join(h, ".qoder", "AGENTS.md"),
		"commandcode": filepath.Join(h, ".commandcode", "AGENTS.md"),
		"codebuddy":   filepath.Join(h, ".codebuddy", "CODEBUDDY.mdc"),
	}
	for id, inst := range want {
		if tg := targetByID(id); tg == nil || tg.Instructions != inst {
			t.Fatalf("%s's instructions: %+v, want %s", id, tg, inst)
		}
	}
	shared := "Use tabs."
	ok(t)(SaveInstructions(InstructionsChange{Shared: &shared, Agents: []string{"qoder", "commandcode", "codebuddy"}}))
	for id, inst := range want {
		pre := ""
		if id == "codebuddy" {
			pre = "# Mine\n\n"
		}
		if s := read(t, inst); s != pre+blockBegin+"\nUse tabs.\n"+blockEnd+"\n" {
			t.Fatalf("%s's %s:\n%s", id, filepath.Base(inst), s)
		}
	}
	if cfg := read(t, filepath.Join(h, ".qoder", "settings.json")); cfg != "{}" {
		t.Fatalf("instructions rewrote Qoder's settings: %s", cfg)
	}

	// Qoder's context.fileName; Reasonix's [storage] state, then its variable
	write(t, filepath.Join(h, ".qoder", "settings.json"), `{"context": {"fileName": ["QODER.md", "AGENTS.md"]}}`)
	if tg := targetByID("qoder"); tg == nil || tg.Instructions != filepath.Join(h, ".qoder", "QODER.md") {
		t.Fatalf("Qoder with context.fileName: %+v", tg)
	}
	// Reasonix Studio is found by its app alone, so its target is asked
	// of the agent as magpie makes it
	rx := filepath.Join(h, ".reasonix")
	reasonix := &agent.Agent{ID: "reasonix", Dir: rx, Path: filepath.Join(rx, "config.toml")}
	if tg := targetOf(reasonix); tg == nil || tg.Instructions != filepath.Join(rx, "REASONIX.md") {
		t.Fatalf("Reasonix's instructions: %+v", tg)
	}
	write(t, reasonix.Path, "[storage]\nstate = \"~/rx-state\"\n")
	if tg := targetOf(reasonix); tg.Instructions != filepath.Join(h, "rx-state", "REASONIX.md") {
		t.Fatalf("Reasonix with [storage] state: %+v", tg)
	}
	st := filepath.Join(h, "rx-env-state")
	t.Setenv("REASONIX_STATE_HOME", st)
	if tg := targetOf(reasonix); tg.Instructions != filepath.Join(st, "REASONIX.md") {
		t.Fatalf("Reasonix under REASONIX_STATE_HOME: %+v", tg)
	}
}

// Grok App's independent home (Use on a custom provider) reads AGENTS.md
// and skills beside agent-home/config.toml, not Grok Build's ~/.grok.
func TestGrokAppInstructions(t *testing.T) {
	h := sandbox(t)
	root := filepath.Join(h, "grok-app-data")
	t.Setenv("GROK_APP_HOME", root)
	home := filepath.Join(root, "agent-home")
	write(t, filepath.Join(home, "config.toml"), "[models]\ndefault = \"deepseek\"\n")
	inst := filepath.Join(home, "AGENTS.md")
	write(t, inst, "# Mine\n")
	tg := targetByID("grok-app")
	if tg == nil || tg.Instructions != inst || tg.Skills != filepath.Join(home, "skills") || tg.MCP == nil || tg.MCP.Path != filepath.Join(home, "config.toml") {
		t.Fatalf("Grok App library places: %+v", tg)
	}
	if !slices.Contains(readsShared, "grok-app") {
		t.Fatal("Grok App doesn't read ~/.agents/skills")
	}
	shared := "Use tabs."
	ok(t)(SaveInstructions(InstructionsChange{Shared: &shared, Agents: []string{"grok-app"}}))
	if s := read(t, inst); s != "# Mine\n\n"+blockBegin+"\nUse tabs.\n"+blockEnd+"\n" {
		t.Fatalf("AGENTS.md:\n%s", s)
	}
	if cfg := read(t, filepath.Join(home, "config.toml")); !strings.Contains(cfg, `default = "deepseek"`) {
		t.Fatalf("instructions rewrote the route:\n%s", cfg)
	}
}

// Pi and OmO's engine (senpi, a fork of Pi) read AGENTS.override.md in
// their agent folder when it is there, else the first there is of
// AGENTS.md, AGENTS.MD, CLAUDE.md and CLAUDE.MD, so a user's own CLAUDE.md
// is written into rather than hidden by a new AGENTS.md.
func TestPiInstructionsFirstThere(t *testing.T) {
	h := sandbox(t)
	pi := filepath.Join(h, ".pi", "agent")
	omo := filepath.Join(h, ".omo", "agent")
	write(t, filepath.Join(omo, "settings.json"), "{}")
	holdCLI(t, h, "omo") // the folder alone is gone (#2177)
	for _, d := range []string{pi, omo} {
		if tg := targetByID(map[string]string{pi: "pi", omo: "omo"}[d]); tg == nil ||
			tg.Instructions != filepath.Join(d, "AGENTS.md") || tg.Override != filepath.Join(d, "AGENTS.override.md") {
			t.Fatalf("with no file there: %+v", tg)
		}
		write(t, filepath.Join(d, "CLAUDE.md"), "# Mine\n")
	}
	want := map[string]string{"pi": filepath.Join(pi, "CLAUDE.md"), "omo": filepath.Join(omo, "CLAUDE.md")}
	for id, inst := range want {
		if tg := targetByID(id); tg == nil || tg.Instructions != inst {
			t.Fatalf("%s's instructions: %+v, want %s", id, tg, inst)
		}
	}
	shared := "Use tabs."
	ok(t)(SaveInstructions(InstructionsChange{Shared: &shared, Agents: []string{"pi", "omo"}}))
	for id, inst := range want {
		if s := read(t, inst); s != "# Mine\n\n"+blockBegin+"\nUse tabs.\n"+blockEnd+"\n" {
			t.Fatalf("%s's CLAUDE.md:\n%s", id, s)
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(inst), "AGENTS.md")); !os.IsNotExist(err) {
			t.Fatalf("%s got an AGENTS.md that hides CLAUDE.md: %v", id, err)
		}
	}
	// an AGENTS.md there comes first again
	write(t, filepath.Join(pi, "AGENTS.md"), "")
	if tg := targetByID("pi"); tg.Instructions != filepath.Join(pi, "AGENTS.md") {
		t.Fatalf("AGENTS.md comes before CLAUDE.md: %q", tg.Instructions)
	}
}

// A skill kept in ~/.agents/skills reaches ZCode, which reads that folder
// beside its own ~/.zcode/skills: giving it to zcode ticks the chip the
// shared copy already answers, and no second link lands in its own folder
// (#4987).
func TestSharedSkillReachesZCode(t *testing.T) {
	h := sandbox(t)
	write(t, filepath.Join(h, ".zcode/cli/config.json"), "")
	write(t, filepath.Join(h, ".zcode/v2/config.json"), "{}")
	shared := filepath.Join(h, ".agents/skills/orca")
	skill(t, shared, "orca", "Orca")
	ok(t)(ImportSkill("orca"))
	own := filepath.Join(h, ".zcode/skills/orca")

	ok(t)(SkillAgents("orca", []string{"kimi", "zcode"}))
	if _, err := os.Lstat(own); !os.IsNotExist(err) {
		t.Error("zcode has a second link to it in its own folder")
	}
	ok(t)(Sync())
	if _, err := os.Lstat(own); !os.IsNotExist(err) {
		t.Error("zcode's own folder got it on the sync too")
	}
}
