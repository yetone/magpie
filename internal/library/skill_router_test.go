package library

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/yetone/magpie/internal/agent"
)

// routerView is the page's view, failing the test on an error.
func routerView(t *testing.T) *View {
	t.Helper()
	v, err := Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func agentView(t *testing.T, v *View, id string) AgentView {
	t.Helper()
	for _, a := range v.Agents {
		if a.ID == id {
			return a
		}
	}
	t.Fatalf("no agent %s", id)
	return AgentView{}
}

// With the skill router on, Claude Code's skills folder has one skill,
// magpie-skills, in place of a link of each skill ticked for it: its
// SKILL.md lists each with its description and a path, through the
// agent's own folder, that leads to the skill's SKILL.md. A skill kept
// direct, or one only for the user to type, keeps its link; off, every
// link is back and the router's entry and folder are gone.
func TestSkillRouter(t *testing.T) {
	h := sandbox(t)
	src := filepath.Join(h, "src")
	skill(t, filepath.Join(src, "pdf"), "pdf", "Read PDFs. Use for any .pdf file")
	skill(t, filepath.Join(src, "xlsx"), "xlsx", "Edit spreadsheets")
	write(t, filepath.Join(src, "deploy/SKILL.md"), "---\nname: deploy\ndescription: Ship it\ndisable-model-invocation: true\n---\n")
	ok(t)(InstallSkills(src, []string{"pdf", "xlsx", "deploy"}, []string{"claude"}))
	dir := filepath.Join(h, ".claude", "skills")
	entry := filepath.Join(dir, RouterSkill)
	for _, n := range []string{"pdf", "xlsx", "deploy"} {
		if !ours(filepath.Join(dir, n), n) {
			t.Fatalf("router off: claude has no link of %s", n)
		}
	}
	if _, err := os.Lstat(entry); err == nil {
		t.Fatal("router off, yet magpie-skills is there")
	}

	settingsAt := filepath.Join(h, ".claude", "settings.json")
	mineRule := "Read(//etc/hosts)"
	write(t, settingsAt, `{"model":"opus","permissions":{"allow":["`+mineRule+`"]}}`)
	ok(t)(SetSkillRouter(true))
	// Claude Code reads a listed skill without asking: a rule for the
	// entry, and one for the folder each link leads to (here the folders
	// the skills were installed from)
	allow := func() []string {
		var doc struct {
			Model       string
			Permissions *struct{ Allow []string }
		}
		if err := json.Unmarshal([]byte(read(t, settingsAt)), &doc); err != nil {
			t.Fatal(err)
		}
		if doc.Model != "opus" {
			t.Fatalf("the user's model went: %s", read(t, settingsAt))
		}
		if doc.Permissions == nil {
			return nil
		}
		return doc.Permissions.Allow
	}
	wantRules := []string{
		mineRule,
		"Read(/" + claudePath(filepath.Join(realDir(dir), RouterSkill)) + "/**)",
		"Read(/" + claudePath(realDir(filepath.Join(src, "pdf"))) + "/**)",
		"Read(/" + claudePath(realDir(filepath.Join(src, "xlsx"))) + "/**)",
	}
	if got := allow(); !slices.Equal(got, wantRules) {
		t.Fatalf("permissions.allow: %q, want %q", got, wantRules)
	}
	for _, n := range []string{"pdf", "xlsx"} {
		if _, err := os.Lstat(filepath.Join(dir, n)); err == nil {
			t.Fatalf("router on: claude still has its own %s", n)
		}
	}
	if !ours(filepath.Join(dir, "deploy"), "deploy") {
		t.Fatal("a skill only for the user to type lost its link")
	}
	if !routerOurs(entry) {
		t.Fatal("no magpie-skills entry of magpie's")
	}
	md := read(t, filepath.Join(entry, "SKILL.md"))
	m := parseMeta([]byte(md))
	if m.Name != RouterSkill || m.Description == "" || len(m.Description) > routerDescMax {
		t.Fatalf("front matter: %+v", m)
	}
	if strings.Contains(md, "## deploy") || !strings.Contains(md, "## pdf") || !strings.Contains(md, "## xlsx") {
		t.Fatalf("listed:\n%s", md)
	}
	// the path given leads to the skill, through the agent's own folder
	at := filepath.Join(realDir(dir), RouterSkill, ".skills", "pdf", "SKILL.md")
	if !strings.Contains(md, "`"+at+"`") {
		t.Fatalf("no path %s in:\n%s", at, md)
	}
	if got := read(t, at); !strings.Contains(got, "Read PDFs") {
		t.Fatalf("the path doesn't lead to pdf's SKILL.md: %q", got)
	}
	if !strings.Contains(md, "Read PDFs. Use for any .pdf file") {
		t.Fatalf("pdf's description isn't given whole in the body:\n%s", md)
	}
	if a := loaded(t).Applied["claude"]; a == nil || !a.Router || slices.Contains(a.Skills, "pdf") {
		t.Fatalf("applied: %+v", a)
	}
	v := routerView(t)
	if !v.SkillRouter || agentView(t, v, "claude").SkillRouter != "on" {
		t.Fatalf("view: %v %q", v.SkillRouter, agentView(t, v, "claude").SkillRouter)
	}
	// magpie's own entry isn't a skill of the agent's to bring in
	if slices.ContainsFunc(v.FoundSkills, func(f FoundSkill) bool { return f.Name == RouterSkill }) {
		t.Fatal("magpie-skills is offered as a skill found in an agent")
	}
	// a sync that changes nothing writes nothing
	if r := ok(t)(Sync()); slices.Contains(r.Changed, "claude") {
		t.Fatalf("a second sync changed claude: %+v", r.Changed)
	}

	// xlsx kept direct: its link again, and out of the list; carried
	ok(t)(SkillRouterHow("xlsx", true))
	if !ours(filepath.Join(dir, "xlsx"), "xlsx") {
		t.Fatal("direct, xlsx has no link")
	}
	if md := read(t, filepath.Join(entry, "SKILL.md")); strings.Contains(md, "## xlsx") {
		t.Fatalf("direct, xlsx is still listed:\n%s", md)
	}
	if _, err := os.Lstat(filepath.Join(entry, ".skills", "xlsx")); err == nil {
		t.Fatal("direct, xlsx's link in the router stays")
	}
	b := carry(t)
	for _, s := range b.Skills {
		if s.Name == "xlsx" && s.Router != SkillDirect {
			t.Fatalf("a sync doesn't carry xlsx's direct: %+v", s.Router)
		}
	}

	// off: every link back, the entry and its folder gone
	ok(t)(SetSkillRouter(false))
	for _, n := range []string{"pdf", "xlsx", "deploy"} {
		if !ours(filepath.Join(dir, n), n) {
			t.Fatalf("router off again: claude has no link of %s", n)
		}
	}
	if _, err := os.Lstat(entry); err == nil {
		t.Fatal("router off, magpie-skills stays")
	}
	if _, err := os.Stat(routerDir(dir)); err == nil {
		t.Fatal("router off, its folder stays")
	}
	if a := loaded(t).Applied["claude"]; a == nil || a.Router || len(a.RouterRules) != 0 {
		t.Fatalf("applied after off: %+v", a)
	}
	if got := allow(); !slices.Equal(got, []string{mineRule}) {
		t.Fatalf("router off, permissions.allow: %q", got)
	}
}

// A skill the router lists is still in Codex's $ picker (yuushabu_buyinn
// on X: "skill routing means I can't use / to pick a skill"; Claude
// Code's are commands, TestSkillRouterKeepsSlashCommands): its folder
// gets a stub by the skill's name, which Codex offers to type and keeps
// out of the model's prompt (allow_implicit_invocation off; Pi and Droid
// read disable-model-invocation), and which sends it to the skill's own
// SKILL.md. Gemini CLI, which has neither, gets no stub, nor does Claude
// Code's skills folder, which Crush and others read; a skill kept direct,
// or the router off, has its link where the stub was.
func TestSkillRouterSlash(t *testing.T) {
	h := sandbox(t)
	src := filepath.Join(h, "src")
	skill(t, filepath.Join(src, "pdf"), "pdf", "Read PDFs. Use for any .pdf file")
	write(t, filepath.Join(src, "xlsx/SKILL.md"), "---\nname: xlsx\ndescription: Edit spreadsheets\nargument-hint: <file>\n---\n")
	write(t, filepath.Join(src, "lint/SKILL.md"), "---\nname: lint\ndescription: Model only\nuser-invocable: false\n---\n")
	ok(t)(InstallSkills(src, []string{"pdf", "xlsx", "lint"}, []string{"claude", "codex", "gemini"}))
	ok(t)(SetSkillRouter(true))
	folders := map[string]string{
		"claude": filepath.Join(h, ".claude", "skills"),
		"codex":  filepath.Join(h, ".codex", "skills"),
		"gemini": filepath.Join(h, ".gemini", "skills"),
	}
	for _, id := range []string{"codex"} {
		dir := folders[id]
		for _, n := range []string{"pdf", "xlsx"} {
			p := filepath.Join(dir, n)
			if !stubOf(p) {
				t.Fatalf("%s: no stub of %s to type it by", id, n)
			}
			md := read(t, filepath.Join(p, "SKILL.md"))
			m := parseMeta([]byte(md))
			if m.Name != n || !m.Manual || m.Description == "" {
				t.Fatalf("%s: %s's stub front matter: %+v\n%s", id, n, m, md)
			}
			at := filepath.Join(realDir(dir), RouterSkill, ".skills", n, "SKILL.md")
			if !strings.Contains(md, "`"+at+"`") {
				t.Fatalf("%s: %s's stub doesn't send the agent to %s:\n%s", id, n, at, md)
			}
			if got := read(t, at); !strings.Contains(got, "name: "+n) {
				t.Fatalf("%s: the stub's path doesn't lead to %s: %q", id, n, got)
			}
			if y := read(t, filepath.Join(p, "agents", "openai.yaml")); !strings.Contains(y, "allow_implicit_invocation: false") {
				t.Fatalf("%s: %s's stub is in Codex's prompt: %q", id, n, y)
			}
		}
		if m := parseMeta([]byte(read(t, filepath.Join(dir, "pdf", "SKILL.md")))); m.Description != "Read PDFs. Use for any .pdf file" {
			t.Fatalf("%s: pdf's stub description: %q", id, m.Description)
		}
		if m := parseMeta([]byte(read(t, filepath.Join(dir, "xlsx", "SKILL.md")))); m.Hint != "<file>" {
			t.Fatalf("%s: xlsx's argument-hint: %q", id, m.Hint)
		}
		// one only the model calls has nothing to type
		if _, err := os.Lstat(filepath.Join(dir, "lint")); err == nil {
			t.Fatalf("%s: a skill only the model calls has a stub", id)
		}
		if md := read(t, filepath.Join(dir, RouterSkill, "SKILL.md")); !strings.Contains(md, "## pdf") || !strings.Contains(md, "## lint") {
			t.Fatalf("%s: the router no longer lists them:\n%s", id, md)
		}
	}
	for _, id := range []string{"gemini", "claude"} {
		if !routerOurs(filepath.Join(folders[id], RouterSkill)) {
			t.Fatalf("%s isn't given the router", id)
		}
		for _, n := range []string{"pdf", "xlsx"} {
			if _, err := os.Lstat(filepath.Join(folders[id], n)); err == nil {
				t.Fatalf("%s's skills folder, read by an agent that would carry a stub's description in its prompt, has %s", id, n)
			}
		}
	}
	v := routerView(t)
	for _, f := range v.FoundSkills {
		if f.Name == "pdf" || f.Name == "xlsx" {
			t.Fatalf("a stub is offered as a skill found in an agent: %+v", f)
		}
	}
	if p := ok(t)(Sync()); len(p.Changed) != 0 || len(p.Problems) != 0 {
		t.Fatalf("a second sync: changed %v, problems %+v", p.Changed, p.Problems)
	}

	// kept direct: its link where the stub was
	ok(t)(SkillRouterHow("xlsx", true))
	for _, id := range []string{"claude", "codex", "gemini"} {
		if p := filepath.Join(folders[id], "xlsx"); !ours(p, "xlsx") {
			t.Fatalf("%s: direct, xlsx has no link", id)
		}
	}
	if _, err := os.Stat(filepath.Join(stubsDir(folders["claude"]), "xlsx")); err == nil {
		t.Fatal("direct, xlsx's stub stays")
	}

	// off: the links back, no stub left
	ok(t)(SetSkillRouter(false))
	for _, id := range []string{"claude", "codex", "gemini"} {
		for _, n := range []string{"pdf", "xlsx", "lint"} {
			if p := filepath.Join(folders[id], n); !ours(p, n) {
				t.Fatalf("%s: router off, no link of %s", id, n)
			}
		}
		if _, err := os.Stat(stubsDir(folders[id])); err == nil {
			t.Fatalf("%s: router off, the stubs' folder stays", id)
		}
	}
}

// Alma reads Codex's folder too and neither flag, so with Alma here
// Codex's folder gets no stub: it would put each skill's description in
// Alma's prompt, which the router is there to keep out.
func TestSkillRouterSlashNotWhereAlmaReads(t *testing.T) {
	h := sandbox(t)
	// Alma is there once it has its data folder (%APPDATA% on Windows,
	// which sandbox clears)
	t.Setenv("APPDATA", filepath.Join(h, "AppData", "Roaming"))
	cfg, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	almaDir := filepath.Join(cfg, "alma")
	os.MkdirAll(almaDir, 0o755)
	if tg := targetByID("alma"); tg == nil || !slices.Contains(tg.SkillsAlso, "codex") {
		t.Fatalf("alma: %+v", tg)
	}
	src := filepath.Join(h, "src")
	skill(t, filepath.Join(src, "pdf"), "pdf", "Read PDFs")
	ok(t)(InstallSkills(src, []string{"pdf"}, []string{"codex"}))
	ok(t)(SetSkillRouter(true))
	dir := filepath.Join(h, ".codex", "skills")
	if !routerOurs(filepath.Join(dir, RouterSkill)) {
		t.Fatal("codex isn't given the router")
	}
	if _, err := os.Lstat(filepath.Join(dir, "pdf")); err == nil {
		t.Fatal("with Alma reading Codex's folder, codex has a stub of pdf")
	}
	// Alma gone: the stub
	os.RemoveAll(almaDir)
	ok(t)(Sync())
	if !stubOf(filepath.Join(dir, "pdf")) {
		t.Fatal("no Alma: codex has no stub of pdf")
	}
}

// Unticked from every skill the router listed, the agent's entry goes.
func TestSkillRouterLastSkillUnticked(t *testing.T) {
	h := sandbox(t)
	skill(t, filepath.Join(h, "src/pdf"), "pdf", "Read PDFs")
	ok(t)(InstallSkills(filepath.Join(h, "src"), []string{"pdf"}, []string{"claude"}))
	ok(t)(SetSkillRouter(true))
	entry := filepath.Join(h, ".claude", "skills", RouterSkill)
	if !routerOurs(entry) {
		t.Fatal("no entry")
	}
	ok(t)(SkillAgents("pdf", []string{}))
	if _, err := os.Lstat(entry); err == nil {
		t.Fatal("no skill left to list, the entry stays")
	}
	if a := loaded(t).Applied["claude"]; a != nil && a.Router {
		t.Fatalf("applied: %+v", a)
	}
	// the router is on and lists nothing there: the page must not say "on"
	// for an agent whose prompt is as it was (ConatusMoon2222 on X)
	if got := agentView(t, routerView(t), "claude").SkillRouter; got != routerNone {
		t.Fatalf("nothing listed, the view says %q", got)
	}
	ok(t)(SkillAgents("pdf", []string{"claude"}))
	if got := agentView(t, routerView(t), "claude").SkillRouter; got != "on" {
		t.Fatalf("a skill listed, the view says %q", got)
	}
}

// A magpie-skills of the user's own in the agent's folder is left as it
// is, and the agent keeps each skill's link, which the page says.
func TestSkillRouterLeavesTheUsersOwn(t *testing.T) {
	h := sandbox(t)
	skill(t, filepath.Join(h, "src/pdf"), "pdf", "Read PDFs")
	ok(t)(InstallSkills(filepath.Join(h, "src"), []string{"pdf"}, []string{"claude"}))
	own := filepath.Join(h, ".claude", "skills", RouterSkill)
	write(t, filepath.Join(own, "SKILL.md"), "---\nname: magpie-skills\ndescription: mine\n---\n")
	ok(t)(SetSkillRouter(true))
	if got := read(t, filepath.Join(own, "SKILL.md")); !strings.Contains(got, "description: mine") {
		t.Fatalf("the user's own magpie-skills was written over: %q", got)
	}
	if !ours(filepath.Join(h, ".claude", "skills", "pdf"), "pdf") {
		t.Fatal("with a magpie-skills of its own, claude lost pdf's link")
	}
	if why := agentView(t, routerView(t), "claude").SkillRouter; why != routerOwn {
		t.Fatalf("why: %q", why)
	}
	ok(t)(SetSkillRouter(false))
	if _, err := os.Stat(filepath.Join(own, "SKILL.md")); err != nil {
		t.Fatal("router off took the user's own magpie-skills")
	}
}

// The description stays within what Codex takes, however many skills and
// however long theirs, and is cut on a whole character.
func TestRouterDescFits(t *testing.T) {
	var names, descs []string
	for i := range 300 {
		names = append(names, fmt.Sprintf("skill-%03d-with-a-long-name", i))
		descs = append(descs, strings.Repeat("说明文字", 60))
	}
	d := routerDesc(names, func(i int) string { return descs[i] })
	if len(d) > routerDescMax || !utf8.ValidString(d) {
		t.Fatalf("%d bytes, valid %v", len(d), utf8.ValidString(d))
	}
	few := routerDesc([]string{"pdf"}, func(int) string { return "Read PDFs. Use for any .pdf file" })
	if !strings.Contains(few, "pdf (Read PDFs)") {
		t.Fatalf("one skill: %q", few)
	}
}

// A skill in the library's own folder needs no rule of its own: one for
// that folder covers every such skill.
func TestRouterRulesLibraryFolder(t *testing.T) {
	h := sandbox(t)
	for _, n := range []string{"a", "b"} {
		skill(t, skillDir(n), n, "Does "+n)
	}
	tg := &Target{Agent: &agent.Agent{ID: "claude"}, Skills: filepath.Join(h, ".claude", "skills"), Settings: filepath.Join(h, ".claude", "settings.json")}
	got := routerRules(tg, map[string]bool{"a": true, "b": true})
	want := []string{
		"Read(/" + claudePath(filepath.Join(realDir(tg.Skills), RouterSkill)) + "/**)",
		"Read(/" + claudePath(realDir(skillsDir())) + "/**)",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("%q, want %q", got, want)
	}
	tg.Agent.ID = "codex"
	if got := routerRules(tg, map[string]bool{"a": true}); got != nil {
		t.Fatalf("codex: %q", got)
	}
	if got := claudePath(`C:\Users\a\.claude`); got != "/c/Users/a/.claude" && filepath.Separator == '\\' {
		t.Fatalf("windows path: %q", got)
	}
}

// With the router on, a skill it lists is still a /name of Claude Code's
// (RoyShen on X): a slash command in its commands folder that reads the
// skill's SKILL.md, said disable-model-invocation so it stays out of the
// model's prompt. A skill kept direct, or one of the user's own commands
// by that name, gets none; a library command by that name takes its
// place; router off, they go.
func TestSkillRouterKeepsSlashCommands(t *testing.T) {
	h := sandbox(t)
	src := filepath.Join(h, "src")
	skill(t, filepath.Join(src, "pdf"), "pdf", "Read PDFs. Use for any .pdf file")
	skill(t, filepath.Join(src, "xlsx"), "xlsx", "Edit spreadsheets")
	skill(t, filepath.Join(src, "docx"), "docx", "Write documents")
	write(t, filepath.Join(src, "deploy/SKILL.md"), "---\nname: deploy\ndescription: Ship it\ndisable-model-invocation: true\n---\n")
	ok(t)(InstallSkills(src, []string{"pdf", "xlsx", "docx", "deploy"}, []string{"claude", "codex"}))
	cmds := filepath.Join(h, ".claude", "commands")
	mine := filepath.Join(cmds, "docx.md")
	write(t, mine, "my own docx command\n")
	r := ok(t)(SetSkillRouter(true))
	for _, p := range r.Problems {
		if strings.Contains(p.What, "command") {
			t.Fatalf("a problem for a stub: %+v", p)
		}
	}
	stub := filepath.Join(cmds, "pdf.md")
	got := read(t, stub)
	at := filepath.Join(realDir(filepath.Join(h, ".claude", "skills")), RouterSkill, ".skills", "pdf", "SKILL.md")
	for _, w := range []string{"disable-model-invocation: true\n", `description: "Read PDFs. Use for any .pdf file"`, "`" + at + "`", "$ARGUMENTS"} {
		if !strings.Contains(got, w) {
			t.Fatalf("pdf's stub has no %q:\n%s", w, got)
		}
	}
	if !strings.Contains(read(t, at), "Read PDFs") {
		t.Fatal("the stub's path doesn't lead to pdf's SKILL.md")
	}
	if _, err := os.Stat(filepath.Join(cmds, "xlsx.md")); err != nil {
		t.Fatal("xlsx has no stub")
	}
	if _, err := os.Stat(filepath.Join(cmds, "deploy.md")); err == nil {
		t.Fatal("deploy keeps its own link, yet has a stub")
	}
	if read(t, mine) != "my own docx command\n" {
		t.Fatal("the user's own docx command was written over")
	}
	// Codex has no such folder of magpie's: nothing written for it
	if es, _ := os.ReadDir(filepath.Join(h, ".codex", "prompts")); len(es) != 0 {
		t.Fatalf("codex was given stubs: %v", es)
	}
	if r := ok(t)(Sync()); slices.Contains(r.Changed, "claude") {
		t.Fatalf("a second sync changed claude: %+v", r.Changed)
	}
	// the stubs aren't offered as the user's commands to bring in
	p, err := ProbeCommands()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range p.Candidates {
		if c.Name == "pdf" || c.Name == "xlsx" {
			t.Fatalf("a stub is offered to import: %+v", c)
		}
	}

	// kept direct: its link is the /name, and the stub goes
	ok(t)(SkillRouterHow("xlsx", true))
	if _, err := os.Stat(filepath.Join(cmds, "xlsx.md")); err == nil {
		t.Fatal("direct, xlsx's stub stays")
	}

	// a library command called pdf takes the stub's place
	r = ok(t)(SaveCommand("", Command{Name: "pdf", Description: "mine", Prompt: "Do the pdf thing", Agents: []string{"claude"}}))
	for _, p := range r.Problems {
		if strings.Contains(p.What, "pdf") {
			t.Fatalf("a problem: %+v", p)
		}
	}
	if got := read(t, stub); !strings.Contains(got, "Do the pdf thing") {
		t.Fatalf("pdf.md isn't the library's command:\n%s", got)
	}
	a := loaded(t).Applied["claude"]
	if slices.ContainsFunc(a.RouterCommands, func(f SubagentFile) bool { return f.Path == stub }) {
		t.Fatalf("the command is still on record as a stub: %+v", a.RouterCommands)
	}
	ok(t)(RemoveCommands([]string{"pdf"}))
	if got := read(t, stub); !strings.Contains(got, "disable-model-invocation") {
		t.Fatalf("the command gone, pdf has no stub again:\n%s", got)
	}

	// off: every stub goes, the user's own stays
	ok(t)(SetSkillRouter(false))
	if _, err := os.Stat(stub); err == nil {
		t.Fatal("router off, pdf's stub stays")
	}
	if read(t, mine) != "my own docx command\n" {
		t.Fatal("router off took the user's own command")
	}
	if a := loaded(t).Applied["claude"]; a != nil && len(a.RouterCommands) != 0 {
		t.Fatalf("applied after off: %+v", a.RouterCommands)
	}
}
