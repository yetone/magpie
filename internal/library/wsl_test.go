package library

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/agent"
)

// wslSandbox is sandbox with a running WSL distro, Ubuntu-24.04, whose /
// is a temp folder and whose user /home/me has Codex, Claude Code, Pi and
// OmO; it gives back that user's home as magpie opens it.
func wslSandbox(t *testing.T) string {
	t.Helper()
	sandbox(t)
	root := t.TempDir()
	home := filepath.Join(root, "home", "me")
	write(t, filepath.Join(home, ".codex", "config.toml"), "model = \"gpt-5.5\"\n")
	write(t, filepath.Join(home, ".claude", "settings.json"), "{}\n")
	write(t, filepath.Join(home, ".pi", "agent", "settings.json"), "{}\n")
	write(t, filepath.Join(home, ".omo", "agent", "settings.json"), "{}\n")
	t.Cleanup(agent.FakeWSL(map[string]string{
		"Ubuntu-24.04": "home:/home/me\ndir:.codex\ndir:.claude\ndir:.pi\ndir:.omo\n",
	}, map[string]string{"Ubuntu-24.04": root}))
	return home
}

const (
	wslCodex  = "codex@wsl:Ubuntu-24.04"
	wslClaude = "claude@wsl:Ubuntu-24.04"
	wslPi     = "pi@wsl:Ubuntu-24.04"
	wslOmO    = "omo@wsl:Ubuntu-24.04"
)

// The agents magpie found in a WSL distro are the library's to give to
// (#331), each at its own files in the distro's home, not Windows'.
func TestWSLTargets(t *testing.T) {
	h := wslSandbox(t)
	want := map[string][3]string{ // instructions, MCP, skills
		wslCodex:  {".codex/AGENTS.md", ".codex/config.toml", ".codex/skills"},
		wslClaude: {".claude/CLAUDE.md", ".claude.json", ".claude/skills"},
		wslPi:     {".pi/agent/AGENTS.md", ".pi/agent/mcp.json", ".pi/agent/skills"},
		wslOmO:    {".omo/agent/AGENTS.md", ".omo/agent/mcp.json", ".omo/agent/skills"},
	}
	for id, w := range want {
		tg := targetByID(id)
		if tg == nil {
			t.Fatalf("%s isn't a target: %v", id, ids(Targets()))
		}
		mcp := ""
		if tg.MCP != nil {
			mcp = tg.MCP.Path
		}
		at := func(rel string) string {
			if rel == "" {
				return ""
			}
			return filepath.Join(h, rel)
		}
		if tg.Instructions != at(w[0]) || mcp != at(w[1]) || tg.Skills != at(w[2]) || tg.Copy || tg.Store != at(".local/share/magpie/skills") {
			t.Errorf("%s: %+v (mcp %s)", id, tg, mcp)
		}
	}
	for _, kind := range []string{"instructions", "mcp", "skills"} {
		if id, err := Takes("Codex · WSL Ubuntu-24.04", kind); err != nil || id != wslCodex {
			t.Errorf("Takes %s: %s %v", kind, id, err)
		}
	}
	// a stopped distro's is none: opening its files would start it
	if tg := targetOf(&agent.Agent{ID: "codex@wsl:Stopped", WSL: "Stopped"}); tg != nil {
		t.Errorf("stopped distro: %+v", tg)
	}
}

// A skill reaches the agents in WSL as one copy in the distro, magpie's
// (~/.local/share/magpie/skills) — a link to a Windows folder is nothing
// they can follow — and each agent there a link the distro makes to it
// (cutemmuu on X). The copy is made again when the library's changes,
// and taken away once no agent there has it.
func TestWSLSkillsLinked(t *testing.T) {
	h := wslSandbox(t)
	src := filepath.Join(home(), "src", "skills")
	skill(t, filepath.Join(src, "pdf"), "pdf", "Read PDFs")
	ok(t)(InstallSkills(src, []string{"pdf"}, []string{"claude", wslCodex, wslClaude, wslPi}))
	store := filepath.Join(h, ".local/share/magpie/skills/pdf")
	if fi, err := os.Lstat(store); err != nil || !fi.IsDir() || !ours(store, "pdf") || !strings.Contains(read(t, filepath.Join(store, "SKILL.md")), "Read PDFs") {
		t.Fatalf("no copy of magpie's in the distro: %v %v", fi, err)
	}
	for _, d := range []string{".codex/skills/pdf", ".claude/skills/pdf", ".pi/agent/skills/pdf"} {
		p := filepath.Join(h, d)
		to, err := os.Readlink(p)
		if err != nil || to != store {
			t.Fatalf("%s: not a link to the distro's copy: %q %v", d, to, err)
		}
		if !strings.Contains(read(t, filepath.Join(p, "SKILL.md")), "Read PDFs") || read(t, filepath.Join(p, "scripts/run.sh")) != "echo hi\n" || !ours(p, "pdf") {
			t.Errorf("%s isn't the library's skill", d)
		}
	}
	// Windows without the right to make symlinks (Developer Mode off, not
	// elevated) links with junctions (#973); only where neither can be
	// made is the library's a copy of the folder, and this machine's Claude
	// Code a marked copy of that
	links := dirLink(t.TempDir(), filepath.Join(t.TempDir(), "link")) == nil
	if local := filepath.Join(home(), ".claude/skills/pdf"); linked(local) != links || !ours(local, "pdf") {
		t.Errorf("this machine's Claude Code: linked %v, symlinks here %v", linked(local), links)
	}
	// the skill is its folder: an edit there reaches the copies at the next
	// sync; where the library holds a copy, the edit is made in that copy
	edited := filepath.Join(src, "pdf/SKILL.md")
	if !links {
		edited = filepath.Join(skillDir("pdf"), "SKILL.md")
	}
	write(t, edited, "---\nname: pdf\ndescription: Changed\n---\n")
	res := ok(t)(Sync())
	if !slices.Contains(res.Changed, wslCodex) {
		t.Errorf("changed: %v", res.Changed)
	}
	if !strings.Contains(read(t, filepath.Join(h, ".codex/skills/pdf/SKILL.md")), "Changed") {
		t.Error("the copy wasn't made again")
	}
	if res := ok(t)(Sync()); len(res.Changed) != 0 {
		t.Errorf("a sync with nothing new wrote %v", res.Changed)
	}
	ok(t)(SkillAgents("pdf", []string{"claude", wslClaude}))
	if _, err := os.Lstat(filepath.Join(h, ".codex/skills/pdf")); !os.IsNotExist(err) {
		t.Errorf("codex@wsl still has it: %v", err)
	}
	if !ours(filepath.Join(h, ".claude/skills/pdf"), "pdf") || !ours(store, "pdf") {
		t.Error("claude@wsl lost it")
	}
	// no agent in the distro given it: its copy there goes too
	ok(t)(SkillAgents("pdf", []string{"claude"}))
	for _, p := range []string{filepath.Join(h, ".claude/skills/pdf"), store} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("%s is still there: %v", p, err)
		}
	}
}

// MCP servers are written into the distro's files in each agent's own
// format; one that runs a Windows program isn't given to them.
func TestWSLServers(t *testing.T) {
	h := wslSandbox(t)
	ok(t)(SaveServer("", Server{Name: "fetch", Transport: "stdio", Command: "uvx", Args: []string{"mcp-server-fetch"},
		Agents: []string{"codex", wslCodex, wslClaude, wslOmO}}))
	if s := read(t, filepath.Join(h, ".codex/config.toml")); !strings.Contains(s, "[mcp_servers.fetch]") || !strings.Contains(s, `command = "uvx"`) || !strings.Contains(s, `model = "gpt-5.5"`) {
		t.Errorf("codex@wsl config.toml:\n%s", s)
	}
	if s := read(t, filepath.Join(h, ".claude.json")); !strings.Contains(s, `"fetch"`) || !strings.Contains(s, `"uvx"`) {
		t.Errorf("claude@wsl .claude.json:\n%s", s)
	}
	if s := read(t, filepath.Join(h, ".omo/agent/mcp.json")); !strings.Contains(s, `"fetch"`) {
		t.Errorf("omo@wsl mcp.json:\n%s", s)
	}
	if s := read(t, filepath.Join(home(), ".codex/config.toml")); !strings.Contains(s, "[mcp_servers.fetch]") {
		t.Errorf("this machine's codex:\n%s", s)
	}

	res, err := SaveServer("", Server{Name: "win", Transport: "stdio", Command: `C:\Program Files\nodejs\npx.cmd`, Args: []string{"x"},
		Agents: []string{"codex", wslCodex}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Problems) != 1 || res.Problems[0].Agent != wslCodex || res.Problems[0].What != "mcp:win" {
		t.Errorf("problems: %+v", res.Problems)
	}
	if strings.Contains(read(t, filepath.Join(h, ".codex/config.toml")), "mcp_servers.win") {
		t.Error("the Windows program was given to codex@wsl")
	}
	if !strings.Contains(read(t, filepath.Join(home(), ".codex/config.toml")), "mcp_servers.win") {
		t.Error("this machine's codex didn't get it")
	}
}

// The shared instructions, and what one WSL agent gets besides, go into
// its file in the distro; the files magpie keeps of it are named without
// the colon Windows can't have in a name.
func TestWSLInstructions(t *testing.T) {
	h := wslSandbox(t)
	write(t, filepath.Join(h, ".codex/AGENTS.md"), "Mine.\n")
	shared, extra := "Be brief.", "Use apt."
	res := ok(t)(SaveInstructions(InstructionsChange{Shared: &shared, Agents: []string{wslCodex, wslClaude},
		Extra: map[string]*string{wslCodex: &extra}}))
	s := read(t, filepath.Join(h, ".codex/AGENTS.md"))
	if !strings.HasPrefix(s, "Mine.\n") || !strings.Contains(s, "Be brief.\n\nUse apt.") {
		t.Errorf("codex@wsl AGENTS.md:\n%s", s)
	}
	if s := read(t, filepath.Join(h, ".claude/CLAUDE.md")); !strings.Contains(s, "Be brief.") || strings.Contains(s, "apt") {
		t.Errorf("claude@wsl CLAUDE.md:\n%s", s)
	}
	// what it gets besides, and its file kept before the change
	for _, dir := range []string{filepath.Dir(extraPath(wslCodex)), res.Backup} {
		es, _ := os.ReadDir(dir)
		var names []string
		for _, e := range es {
			names = append(names, e.Name())
		}
		if !slices.ContainsFunc(names, func(n string) bool { return strings.HasPrefix(n, "codex@wsl") && !strings.Contains(n, ":") }) ||
			slices.ContainsFunc(names, func(n string) bool { return strings.Contains(n, ":") }) {
			t.Errorf("%s: %v", dir, names)
		}
	}
	// a setup carried elsewhere keeps what it gets besides
	if x := extras(); x[wslCodex] != extra {
		t.Errorf("extras: %v", x)
	}
	iv, err := ReadInstructions()
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(iv.Agents, func(a AgentInstructions) bool { return a.Agent == wslCodex })
	if i < 0 || !iv.Agents[i].On || iv.Agents[i].Extra != extra || iv.Agents[i].Own != 1 {
		t.Errorf("instructions page: %+v", iv.Agents)
	}
}

// magpie's own image server, a Windows program, is given to an agent in
// WSL all the same (#900): by the binary's path in the distro, which WSL's
// interop starts, told the distro, with the variables it is given named in
// WSLENV. A distro whose interop is off is told so.
func TestWSLMagpieImage(t *testing.T) {
	h := wslSandbox(t)
	probed := 0
	answer := "/mnt/d/tools/Magpie/magpie-windows-amd64.exe\ninterop:on\n"
	old := wslProbe
	wslProbe = func(distro, script string) (string, error) {
		probed++
		if distro != "Ubuntu-24.04" || !strings.Contains(script, `wslpath -u 'D:\tools\Magpie\magpie-windows-amd64.exe'`) {
			t.Errorf("probe %s: %s", distro, script)
		}
		return answer, nil
	}
	reset := func() {
		wslExes.Lock()
		wslExes.at, wslExes.out = nil, nil
		wslExes.Unlock()
	}
	reset()
	t.Cleanup(func() { wslProbe = old; reset() })
	// the Windows magpie this computer runs, as it would on Windows
	thisMagpie(t, `D:\tools\Magpie\magpie-windows-amd64.exe`)

	img := Server{Name: "magpie-image", Transport: "stdio", Command: `D:\tools\Magpie\magpie-windows-amd64.exe`, Args: []string{"mcp", "image"},
		Env: map[string]string{"MAGPIE_ADDR": "127.0.0.1:4000"}, Agents: []string{wslCodex, wslClaude}}
	res := ok(t)(SaveServer("", img))
	if len(res.Problems) != 0 {
		t.Fatalf("problems: %+v", res.Problems)
	}
	s := read(t, filepath.Join(h, ".codex/config.toml"))
	for _, w := range []string{"[mcp_servers.magpie-image]", `command = "/mnt/d/tools/Magpie/magpie-windows-amd64.exe"`, `"--wsl"`, `"Ubuntu-24.04"`, `WSLENV = "MAGPIE_ADDR"`} {
		if !strings.Contains(s, w) {
			t.Errorf("codex@wsl config.toml has no %s:\n%s", w, s)
		}
	}
	if s := read(t, filepath.Join(h, ".claude.json")); !strings.Contains(s, `"/mnt/d/tools/Magpie/magpie-windows-amd64.exe"`) || !strings.Contains(s, `"--wsl"`) {
		t.Errorf("claude@wsl .claude.json:\n%s", s)
	}
	// written as it is: the next sync changes nothing, nor asks the distro again
	if res := ok(t)(Sync()); len(res.Changed) != 0 || len(res.Problems) != 0 {
		t.Errorf("a sync with nothing new: %+v", res)
	}
	if probed != 1 {
		t.Errorf("the distro was asked %d times", probed)
	}

	// the hub on: an agent in WSL is given magpie-image itself still, run
	// in its distro, whose paths it saves to; the hub gives it nothing
	ok(t)(SetMCPHub(true))
	if s := read(t, filepath.Join(h, ".codex/config.toml")); !strings.Contains(s, "[mcp_servers.magpie-image]") {
		t.Errorf("codex@wsl lost magpie-image to the hub:\n%s", s)
	}
	if got := hubServers(wslCodex); len(got) != 0 {
		t.Errorf("the hub gives codex@wsl %v", got)
	}
	ok(t)(SetMCPHub(false))

	// drives mounted elsewhere: the server is told where
	reset()
	answer = "/win/d/tools/Magpie/magpie-windows-amd64.exe\ninterop:on\n"
	ok(t)(Sync())
	if s := read(t, filepath.Join(h, ".codex/config.toml")); !strings.Contains(s, `"/win/d/tools/Magpie/magpie-windows-amd64.exe"`) || !strings.Contains(s, `"--mount"`) || !strings.Contains(s, `"/win/"`) {
		t.Errorf("custom mount:\n%s", s)
	}

	// interop off: nothing it could start, and why
	reset()
	answer = "/mnt/d/tools/Magpie/magpie-windows-amd64.exe\ninterop:off\n"
	res, err := Sync()
	if err != nil || len(res.Problems) == 0 || !strings.Contains(res.Problems[0].Error, "interop") {
		t.Errorf("interop off: %+v", res.Problems)
	}
}

// TJHHHH on Discord: magpie kept starting WSL while they repaired it. An
// agent made while its distro ran, which the user then stops (wsl
// --shutdown), is no target: writing the library there would start it.
// Giving it something says the distro isn't running.
func TestWSLStoppedSinceNoTarget(t *testing.T) {
	wslSandbox(t)
	tg := targetByID(wslCodex)
	if tg == nil {
		t.Fatalf("not a target while running: %v", ids(Targets()))
	}
	agent.StopFakeWSL("Ubuntu-24.04")
	if wslTargetOf(tg.Agent) != nil {
		t.Fatal("a target in the stopped distro")
	}
	for _, t2 := range Targets() {
		if t2.Agent.WSL != "" {
			t.Fatalf("%s is a target while its distro is stopped", t2.Agent.ID)
		}
	}
}

// viewOf is the agent id as Read lists it, nil for none.
func viewOf(t *testing.T, id string) *AgentView {
	t.Helper()
	v, err := Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range v.Agents {
		if a.ID == id {
			return &a
		}
	}
	return nil
}

// cybercaius on Discord: with WSL not started, the MCP page didn't list
// the distro's agents at all. A stopped distro's agents are listed by
// what magpie last saw of them there, their ticks as the library has
// them; a tick is kept, and written into the distro once it runs (Woken).
// Nothing of the distro is opened or run while it is stopped (#3896).
func TestWSLStoppedListedAndQueued(t *testing.T) {
	const distro = "Ubuntu-24.04"
	h := wslSandbox(t)
	ok(t)(SaveServer("", Server{Name: "fetch", Transport: "stdio", Command: "uvx", Args: []string{"mcp-server-fetch"}, Agents: []string{wslCodex}}))
	ok(t)(SaveServer("", Server{Name: "events", Transport: "sse", URL: "https://example.invalid/sse"}))
	agent.StopFakeWSL(distro)
	// the distro's files gone from reach: anything opened there fails,
	// anything written there makes its folder again
	root := filepath.Dir(filepath.Dir(h))
	away := root + ".away"
	if err := os.Rename(root, away); err != nil {
		t.Fatal(err)
	}
	runs := agent.FakeWSLRuns(distro)
	cx := viewOf(t, wslCodex)
	if cx == nil {
		t.Fatal("a stopped distro's Codex isn't listed")
	}
	if cx.Asleep != distro || cx.Unseen || cx.MCP != filepath.Join(h, ".codex", "config.toml") || cx.Skills != filepath.Join(h, ".codex", "skills") || !cx.NoSSE || cx.MustCopy || cx.How != HowLink {
		t.Errorf("Codex while stopped: %+v", *cx)
	}
	if cl := viewOf(t, wslClaude); cl == nil || cl.Asleep != distro || cl.MCP != filepath.Join(h, ".claude.json") || cl.NoSSE {
		t.Errorf("Claude Code while stopped: %+v", cl)
	}
	for _, tg := range Targets() {
		if tg.Agent.WSL != "" {
			t.Errorf("%s is a target while its distro is stopped", tg.Agent.ID)
		}
	}
	// ticked while stopped: kept, and given only what it can take (no SSE
	// for OmO, as its chip says)
	ok(t)(ServerAgents("fetch", []string{wslCodex, wslClaude}))
	ok(t)(EveryServerAgents([]string{wslOmO}, true))
	l, err := load()
	if err != nil {
		t.Fatal(err)
	}
	if got := l.server("fetch").Agents; !slices.Equal(got, []string{wslClaude, wslCodex, wslOmO}) {
		t.Errorf("fetch's agents: %v", got)
	}
	if got := l.server("events").Agents; len(got) != 0 {
		t.Errorf("an SSE server given to a stopped agent that takes none: %v", got)
	}
	if id, err := Takes("Claude Code · WSL "+distro, "mcp"); err != nil || id != wslClaude {
		t.Errorf("Takes while stopped: %s %v", id, err)
	}
	if AsleepIn(wslClaude) != distro || AsleepIn("claude") != "" {
		t.Errorf("AsleepIn: %q", AsleepIn(wslClaude))
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Errorf("something was written into the stopped distro: %v", err)
	}
	if n := agent.FakeWSLRuns(distro); n != runs {
		t.Errorf("%d commands ran in the stopped distro", n-runs)
	}
	if res, err := Woken(); res != nil || err != nil {
		t.Errorf("a sync while the distro is stopped: %+v %v", res, err)
	}
	// the user starts it: what was ticked is written there
	if err := os.Rename(away, root); err != nil {
		t.Fatal(err)
	}
	agent.StartFakeWSL(distro)
	res, err := Woken()
	if err != nil || res == nil {
		t.Fatalf("no sync once the distro ran: %+v %v", res, err)
	}
	if s := read(t, filepath.Join(h, ".claude.json")); !strings.Contains(s, `"fetch"`) {
		t.Errorf("claude@wsl .claude.json:\n%s", s)
	}
	if s := read(t, filepath.Join(h, ".omo", "agent", "mcp.json")); !strings.Contains(s, `"fetch"`) {
		t.Errorf("omo@wsl mcp.json:\n%s", s)
	}
	if res, err := Woken(); res != nil || err != nil {
		t.Errorf("a second sync with nothing started: %+v %v", res, err)
	}
	if v := viewOf(t, wslCodex); v == nil || v.Asleep != "" {
		t.Errorf("Codex once running: %+v", v)
	}
}

// An agent the library names in a distro that stopped before magpie ever
// saw its places is listed on its rows as one whose ticks can't be shown
// (Unseen); one it names nothing for isn't listed, and neither can be
// given anything until the distro runs.
func TestWSLStoppedUnseen(t *testing.T) {
	const distro = "Ubuntu-24.04"
	wslSandbox(t)
	agent.All() // the probe, while it runs; no target is made
	agent.StopFakeWSL(distro)
	runs := agent.FakeWSLRuns(distro)
	ok(t)(SaveServer("", Server{Name: "fetch", Transport: "stdio", Command: "uvx", Agents: []string{wslPi}}))
	if v := viewOf(t, wslPi); v == nil || !v.Unseen || v.Asleep != distro || v.MCP != "" {
		t.Errorf("Pi named while stopped: %+v", v)
	}
	if v := viewOf(t, wslCodex); v != nil {
		t.Errorf("Codex named nowhere, never seen: %+v", v)
	}
	if _, err := Takes("Codex · WSL "+distro, "mcp"); err == nil || !strings.Contains(err.Error(), "isn't running") {
		t.Errorf("Takes of an unseen agent: %v", err)
	}
	ok(t)(EveryServerAgents([]string{wslCodex}, true))
	l, _ := load()
	if got := l.server("fetch").Agents; !slices.Equal(got, []string{wslPi}) {
		t.Errorf("fetch's agents: %v", got)
	}
	if n := agent.FakeWSLRuns(distro); n != runs {
		t.Errorf("%d commands ran in the stopped distro", n-runs)
	}
}

// instrRow is the agent id's row on the Instructions page, nil for none.
func instrRow(t *testing.T, id string) *AgentInstructions {
	t.Helper()
	iv, err := ReadInstructions()
	if err != nil {
		t.Fatal(err)
	}
	v, err := Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	// the page's whole read and the one after an instructions change agree
	if len(v.Instructions.Agents) != len(iv.Agents) {
		t.Errorf("Read lists %d instructions rows, ReadInstructions %d", len(v.Instructions.Agents), len(iv.Agents))
	}
	for _, a := range iv.Agents {
		if a.Agent == id {
			return &a
		}
	}
	return nil
}

// #4182: the Instructions page left a stopped distro's agents out, so
// none could be switched on until WSL ran, and a switch on the page sent
// the agents without them. They are listed at the file magpie last saw
// them read, switched as the library has them, and a switch is written
// into the distro once it runs (Woken), on or off. Nothing of the distro
// is opened or run while it is stopped (#3896): not even its file, whose
// own lines aren't counted then.
func TestWSLStoppedInstructions(t *testing.T) {
	const distro = "Ubuntu-24.04"
	h := wslSandbox(t)
	agentsMD := filepath.Join(h, ".codex", "AGENTS.md")
	write(t, agentsMD, "Mine.\n")
	shared := "Be brief."
	// the distro seen running, nothing of the library given to it
	ok(t)(SaveInstructions(InstructionsChange{Shared: &shared, Agents: []string{"claude"}}))
	if r := instrRow(t, wslCodex); r == nil || r.Asleep != "" || r.Own != 1 {
		t.Fatalf("Codex while running: %+v", r)
	}
	root := filepath.Dir(filepath.Dir(h))
	away := root + ".away"
	stop := func() {
		t.Helper()
		agent.StopFakeWSL(distro)
		// the distro's files gone from reach: anything opened there fails,
		// anything written there makes its folder again
		if err := os.Rename(root, away); err != nil {
			t.Fatal(err)
		}
		if res, err := Woken(); res != nil || err != nil {
			t.Errorf("a sync while the distro is stopped: %+v %v", res, err)
		}
	}
	start := func() {
		t.Helper()
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			t.Errorf("something was written into the stopped distro: %v", err)
		}
		if err := os.Rename(away, root); err != nil {
			t.Fatal(err)
		}
		agent.StartFakeWSL(distro)
		if res, err := Woken(); err != nil || res == nil {
			t.Fatalf("no sync once the distro ran: %+v %v", res, err)
		}
	}
	stop()
	runs := agent.FakeWSLRuns(distro)
	cx := instrRow(t, wslCodex)
	if cx == nil {
		t.Fatal("a stopped distro's Codex isn't on the Instructions page")
	}
	if cx.Asleep != distro || cx.Unseen || cx.On || cx.Path != agentsMD || cx.Own != 0 || cx.Edited || cx.Override != "" {
		t.Errorf("Codex while stopped: %+v", *cx)
	}
	if cl := instrRow(t, wslClaude); cl == nil || cl.Asleep != distro || cl.Path != filepath.Join(h, ".claude", "CLAUDE.md") {
		t.Errorf("Claude Code while stopped: %+v", cl)
	}
	if id, err := Takes("Codex · WSL "+distro, "instructions"); err != nil || id != wslCodex {
		t.Errorf("Takes while stopped: %s %v", id, err)
	}
	// switched on while stopped: kept, and on the page as on
	ok(t)(SaveInstructions(InstructionsChange{Agents: []string{"claude", wslCodex}}))
	if l, _ := load(); !slices.Equal(l.Instructions.Agents, []string{"claude", wslCodex}) {
		t.Errorf("the agents: %v", l.Instructions.Agents)
	}
	if r := instrRow(t, wslCodex); r == nil || !r.On {
		t.Errorf("Codex switched on while stopped: %+v", r)
	}
	if n := agent.FakeWSLRuns(distro); n != runs {
		t.Errorf("%d commands ran in the stopped distro", n-runs)
	}
	start()
	if s := read(t, agentsMD); !strings.HasPrefix(s, "Mine.\n") || !strings.Contains(s, "Be brief.") {
		t.Errorf("codex@wsl AGENTS.md once the distro ran:\n%s", s)
	}
	if r := instrRow(t, wslCodex); r == nil || r.Asleep != "" || !r.On || r.Own != 1 {
		t.Errorf("Codex once running: %+v", r)
	}
	// switched off while stopped: the part is taken out once it runs,
	// though no agent of the distro is on any more
	stop()
	ok(t)(SaveInstructions(InstructionsChange{Agents: []string{"claude"}}))
	start()
	if s := read(t, agentsMD); s != "Mine.\n" {
		t.Errorf("codex@wsl AGENTS.md once switched off and run:\n%s", s)
	}
}

// One the library switched on in a distro that stopped before magpie
// saw its file is listed as on, unseen, to be kept on; one never seen and
// not on isn't listed.
func TestWSLStoppedInstructionsUnseen(t *testing.T) {
	const distro = "Ubuntu-24.04"
	wslSandbox(t)
	agent.All() // the probe, while it runs; no target is made
	agent.StopFakeWSL(distro)
	runs := agent.FakeWSLRuns(distro)
	ok(t)(SaveInstructions(InstructionsChange{Agents: []string{wslPi}}))
	if r := instrRow(t, wslPi); r == nil || !r.Unseen || !r.On || r.Asleep != distro || r.Path != "" {
		t.Errorf("Pi switched on while stopped: %+v", r)
	}
	if r := instrRow(t, wslCodex); r != nil {
		t.Errorf("Codex on nowhere, never seen: %+v", r)
	}
	if _, err := Takes("Codex · WSL "+distro, "instructions"); err == nil || !strings.Contains(err.Error(), "isn't running") {
		t.Errorf("Takes of an unseen agent: %v", err)
	}
	if n := agent.FakeWSLRuns(distro); n != runs {
		t.Errorf("%d commands ran in the stopped distro", n-runs)
	}
}

// wslHookAgents are the Hooks page's agents in WSL, by id.
func wslHookAgents(t *testing.T) map[string]HookAgentView {
	t.Helper()
	v, err := Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]HookAgentView{}
	for _, a := range v.HookAgents {
		if strings.Contains(a.ID, "@wsl:") {
			m[a.ID] = a
		}
	}
	return m
}

// #4237: the Hooks page left a stopped distro's agents out, where while
// it runs it lists them greyed, saying magpie gives no agent in WSL hooks
// yet. They are listed the same while it is stopped, by what magpie saw
// there (their shape), and a hook given one says why, as while it runs.
// There is nothing to write once the distro runs. Nothing of the distro
// is opened or run while it is stopped (#3896).
func TestWSLStoppedHooks(t *testing.T) {
	const distro = "Ubuntu-24.04"
	h := wslSandbox(t)
	running := wslHookAgents(t)
	if len(running) != 4 || running[wslCodex].No == "" || !running[wslCodex].WSL {
		t.Fatalf("the WSL agents on the Hooks page while it runs: %+v", running)
	}
	agent.StopFakeWSL(distro)
	root := filepath.Dir(filepath.Dir(h))
	if err := os.Rename(root, root+".away"); err != nil {
		t.Fatal(err)
	}
	runs := agent.FakeWSLRuns(distro)
	if stopped := wslHookAgents(t); !reflect.DeepEqual(stopped, running) {
		t.Errorf("the WSL agents on the Hooks page while it is stopped: %+v, while it ran: %+v", stopped, running)
	}
	l, err := load()
	if err != nil {
		t.Fatal(err)
	}
	l.Hooks = append(l.Hooks, &Hook{Name: "notify", Event: "turn-end", Command: "notify-send done", Agents: []string{wslCodex}})
	if err := l.save(); err != nil {
		t.Fatal(err)
	}
	v, err := Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Hooks) != 1 || v.Hooks[0].Problems[wslCodex] != running[wslCodex].No {
		t.Errorf("a hook given the stopped Codex: %+v", v.Hooks)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Errorf("something was written into the stopped distro: %v", err)
	}
	if n := agent.FakeWSLRuns(distro); n != runs {
		t.Errorf("%d commands ran in the stopped distro", n-runs)
	}
}

// wslSubagentAgents are the Subagents page's agents in WSL, by id.
func wslSubagentAgents(t *testing.T) map[string]SubagentAgentView {
	t.Helper()
	v, err := Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]SubagentAgentView{}
	for _, a := range v.SubagentAgents {
		if strings.Contains(a.ID, "@wsl:") {
			m[a.ID] = a
		}
	}
	return m
}

// #4238: the Subagents page left a stopped distro's agents out, where
// while it runs it lists those that take subagents here (Claude Code and
// Codex) greyed, saying magpie gives no agent in WSL subagents yet. They
// are listed the same while it is stopped, by what magpie saw there
// (their shape), and a subagent given one says why, as while it runs.
// There is nothing to write once the distro runs. Nothing of the distro
// is opened or run while it is stopped (#3896).
func TestWSLStoppedSubagents(t *testing.T) {
	const distro = "Ubuntu-24.04"
	h := wslSandbox(t)
	running := wslSubagentAgents(t)
	if len(running) != 2 || running[wslCodex].No == "" || !running[wslCodex].WSL || running[wslClaude].No == "" {
		t.Fatalf("the WSL agents on the Subagents page while it runs: %+v", running)
	}
	agent.StopFakeWSL(distro)
	root := filepath.Dir(filepath.Dir(h))
	if err := os.Rename(root, root+".away"); err != nil {
		t.Fatal(err)
	}
	runs := agent.FakeWSLRuns(distro)
	if stopped := wslSubagentAgents(t); !reflect.DeepEqual(stopped, running) {
		t.Errorf("the WSL agents on the Subagents page while it is stopped: %+v, while it ran: %+v", stopped, running)
	}
	l, err := load()
	if err != nil {
		t.Fatal(err)
	}
	l.Subagents = append(l.Subagents, &Subagent{Name: "reviewer", Description: "Reviews a diff", Prompt: "Review it.", Agents: []string{wslClaude}})
	if err := l.save(); err != nil {
		t.Fatal(err)
	}
	v, err := Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Subagents) != 1 || v.Subagents[0].Problems[wslClaude] != running[wslClaude].No {
		t.Errorf("a subagent given the stopped Claude Code: %+v", v.Subagents)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Errorf("something was written into the stopped distro: %v", err)
	}
	if n := agent.FakeWSLRuns(distro); n != runs {
		t.Errorf("%d commands ran in the stopped distro", n-runs)
	}
}

func TestLinuxHome(t *testing.T) {
	for in, want := range map[string]string{
		`\\wsl.localhost\Ubuntu\home\me`: "/home/me",
		`\\wsl$\Ubuntu-24.04\root`:       "/root",
		`\\WSL.LOCALHOST\Ubuntu`:         "/",
		`C:\Users\me`:                    "",
		"/tmp/x/home/me":                 "",
	} {
		if got := linuxHome(in); got != want {
			t.Errorf("linuxHome(%s) = %q, want %q", in, got, want)
		}
	}
}
