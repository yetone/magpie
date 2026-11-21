package library

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/coderun"
	"github.com/yetone/magpie/internal/gateway"
)

// The hub gives an agent the skills it is given that have a code.js, with
// their description and code: one with none, one for another agent and
// one turned off on this computer aren't among them, and the address any
// other app is given has every one with code. The router's SKILL.md says
// which listed skills have code, and how run_code calls them.
func TestHubSkillsHaveCode(t *testing.T) {
	h := sandbox(t)
	src := filepath.Join(h, "src")
	skill(t, filepath.Join(src, "weekly-report"), "weekly-report", "This week's merged PRs, given a repo")
	write(t, filepath.Join(src, "weekly-report", SkillCode), "export default async ({repo}) => repo\n")
	skill(t, filepath.Join(src, "pdf"), "pdf", "Read PDFs")
	skill(t, filepath.Join(src, "theirs"), "theirs", "Codex's")
	write(t, filepath.Join(src, "theirs", SkillCode), "export default async () => 1\n")
	skill(t, filepath.Join(src, "resting"), "resting", "Off here")
	write(t, filepath.Join(src, "resting", SkillCode), "export default async () => 2\n")
	ok(t)(InstallSkills(src, []string{"weekly-report", "pdf", "resting"}, []string{"claude"}))
	ok(t)(InstallSkills(src, []string{"theirs"}, []string{"codex"}))
	ok(t)(SkillHere("resting", false))
	ok(t)(SetMCPHub(true))

	names := func(ks []gateway.HubSkill) []string {
		var out []string
		for _, k := range ks {
			out = append(out, k.Name)
		}
		return out
	}
	got := (theHub{}).Skills("claude")
	if !slices.Equal(names(got), []string{"weekly-report"}) || got[0].About != "This week's merged PRs, given a repo" || !strings.Contains(got[0].Code, "export default") {
		t.Fatalf("claude's: %+v", got)
	}
	if all := names(theHub{}.Skills("")); !slices.Equal(all, []string{"theirs", "weekly-report"}) {
		t.Fatalf("any other app's: %v", all)
	}
	ok(t)(SetMCPHub(false))
	if got := (theHub{}).Skills("claude"); len(got) != 0 {
		t.Fatalf("the hub is off, yet: %+v", got)
	}

	ok(t)(SetSkillRouter(true))
	md := read(t, filepath.Join(h, ".claude", "skills", RouterSkill, "SKILL.md"))
	if !strings.Contains(md, "With code: `skills.weekly_report(args)` in run_code") || strings.Count(md, "With code:") != 1 {
		t.Fatalf("the router's SKILL.md:\n%s", md)
	}
}

// A script saved from run_code is a skill of the user's: its SKILL.md
// with the description given and how to run it, the code as its code.js,
// given to the agent that saved it, and from then on one of the skills
// the hub gives it. A name the library has is refused, and so is code
// with no default export.
func TestHubSaveSkill(t *testing.T) {
	h := sandbox(t)
	ok(t)(SetMCPHub(true))
	code := "// saved\nexport default async (args) => {\nreturn args.n * 2\n}\n"
	if err := (theHub{}).SaveSkill("claude", "double-it", "Doubles args.n", code); err != nil {
		t.Fatal(err)
	}
	dir := skillDir("double-it")
	if got := read(t, filepath.Join(dir, SkillCode)); got != code {
		t.Fatalf("code.js: %q", got)
	}
	md := read(t, filepath.Join(dir, "SKILL.md"))
	if m, _ := readMeta(dir); m.Name != "double-it" || m.Description != "Doubles args.n" || !strings.Contains(md, "skills.double_it(args)") {
		t.Fatalf("SKILL.md:\n%s", md)
	}
	if got := (theHub{}).Skills("claude"); len(got) != 1 || got[0].Name != "double-it" {
		t.Fatalf("claude's: %+v", got)
	}
	if _, err := os.Lstat(filepath.Join(h, ".claude", "skills", "double-it")); err != nil {
		t.Fatalf("not given to claude: %v", err)
	}
	if err := (theHub{}).SaveSkill("claude", "double-it", "again", code); err == nil {
		t.Fatal("saved over a skill the library has")
	}
	if err := (theHub{}).SaveSkill("claude", "no-export", "none", "return 1"); err == nil {
		t.Fatal("saved code with no default export")
	}
	if _, err := os.Lstat(skillDir("no-export")); err == nil {
		t.Fatal("the refused skill's folder is there")
	}
}

// A code.js's default export is what SkillSource's expression is, with
// what is declared before and after it in scope; a file with no default
// export, or with an import, can't be run.
func TestSkillSource(t *testing.T) {
	for _, code := range []string{
		"const k = 2\nexport default async (a) => helper(a) * k\nfunction helper(a) { return a.n }\n",
		"export default async function run(a) { return a.n * 2 };",
		"  export default (a) => a.n * 2",
	} {
		if _, err := coderun.SkillSource(code); err != nil {
			t.Fatalf("%q: %v", code, err)
		}
	}
	for _, code := range []string{"return 1", "import x from 'y'\nexport default () => x", "export default () => 1\nexport const y = 2"} {
		if _, err := coderun.SkillSource(code); err == nil {
			t.Fatalf("%q ran", code)
		}
	}
}
