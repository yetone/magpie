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
