package library

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// setUpLibrary fills a sandbox's library: two sets, the second on, an
// agent's own additions, a server with a key, a skill kept in the library
// and one linked to a folder of the user's.
func setUpLibrary(t *testing.T, h string) {
	t.Helper()
	first, work := "Use tabs.", "Company rules."
	ok(t)(SaveInstructions(InstructionsChange{Shared: &first, Agents: []string{"claude", "codex"}}))
	ok(t)(SaveInstructions(InstructionsChange{Create: &InstrSet{ID: "work", Name: "Work"}, Texts: map[string]*string{"work": &work}, Activate: "work"}))
	extra := "Answer in French."
	ok(t)(SaveInstructions(InstructionsChange{Extra: map[string]*string{"codex": &extra}}))
	ok(t)(SaveServer("", Server{Name: "fs", Transport: "stdio", Command: "npx", Args: []string{"-y", "@mcp/fs"},
		Env: map[string]string{"GITHUB_TOKEN": "ghp-secret", "MODE": "ro"}, Agents: []string{"claude", "codex"}}))
	skill(t, filepath.Join(h, "src/pdf"), "pdf", "Read PDFs")
	os.Chmod(filepath.Join(h, "src/pdf/scripts/run.sh"), 0o755)
	ok(t)(InstallSkills(filepath.Join(h, "src"), []string{"pdf"}, []string{"claude"}))
}

func TestCarry(t *testing.T) {
	h := sandbox(t)
	if b, err := Collect(); b != nil || err != nil {
		t.Fatalf("no library yet: %+v %v", b, err)
	}
	setUpLibrary(t, h)
	// big files and links stay behind
	write(t, filepath.Join(h, "src/pdf/big.bin"), strings.Repeat("x", maxCarried+1))
	os.Symlink("/etc/hosts", filepath.Join(h, "src/pdf/hosts"))
	b, err := Collect()
	if err != nil {
		t.Fatal(err)
	}
	if b.Texts["default"] != "Use tabs." || b.Texts["work"] != "Company rules." || b.Instructions.Active != "work" || b.Extra["codex"] != "Answer in French." {
		t.Fatalf("instructions: %+v %+v", b.Instructions, b.Texts)
	}
	if len(b.Skills) != 1 || b.Skills[0].Source.Kind != "folder" {
		t.Fatalf("skills: %+v", b.Skills)
	}
	s := b.Skills[0]
	if _, ok := s.Files["SKILL.md"]; !ok || string(s.Files["scripts/run.sh"]) != "echo hi\n" || runtime.GOOS != "windows" && (len(s.Exec) != 1 || s.Exec[0] != "scripts/run.sh") {
		t.Fatalf("pdf: %+v", s)
	}
	if _, ok := s.Files["hosts"]; ok || len(s.Left) != 1 || s.Left[0] != "big.bin" {
		t.Fatalf("pdf carried a link or a big file: %v %v", keysOf(s.Files), s.Left)
	}
	// the same library, the same bundle
	again, _ := Collect()
	if j1, j2 := mustJSON(t, b), mustJSON(t, again); j1 != j2 {
		t.Fatal("two bundles of one library differ")
	}
	b.WithoutSecrets(func(k string) bool { return strings.Contains(k, "TOKEN") })
	if b.MCP[0].Env["GITHUB_TOKEN"] != "" || b.MCP[0].Env["MODE"] != "ro" {
		t.Fatalf("env without secrets: %v", b.MCP[0].Env)
	}
	if again.MCP[0].Env["GITHUB_TOKEN"] != "ghp-secret" {
		t.Fatal("stripping one bundle stripped the other")
	}
	carried := mustJSON(t, again)

	// another computer: what it had of its own in the library goes, the
	// agents' own files stay, and the library is written into them
	h2 := sandbox(t)
	write(t, filepath.Join(h2, ".claude/CLAUDE.md"), "My own notes.\n")
	write(t, filepath.Join(h2, ".claude/skills/mine/SKILL.md"), "---\nname: mine\n---\n")
	skill(t, filepath.Join(h2, "src/old"), "old", "Old")
	ok(t)(InstallSkills(filepath.Join(h2, "src"), []string{"old"}, []string{"claude"}))
	var in Bundle
	json.Unmarshal([]byte(carried), &in)
	ok(t)(Put(&in))
	if s := read(t, filepath.Join(h2, ".claude/CLAUDE.md")); !strings.Contains(s, "My own notes.") || !strings.Contains(s, "Company rules.") {
		t.Errorf("claude's instructions:\n%s", s)
	}
	if s := read(t, filepath.Join(h2, ".codex/AGENTS.md")); !strings.Contains(s, "Answer in French.") {
		t.Errorf("codex's instructions:\n%s", s)
	}
	if s := read(t, filepath.Join(h2, ".codex/config.toml")); !strings.Contains(s, "mcp_servers.fs") || !strings.Contains(s, "ghp-secret") {
		t.Errorf("codex's servers:\n%s", s)
	}
	if s := read(t, filepath.Join(h2, ".claude/skills/pdf/scripts/run.sh")); s != "echo hi\n" {
		t.Errorf("pdf in claude: %q", s)
	}
	if fi, err := os.Stat(filepath.Join(SkillPath("pdf"), "scripts/run.sh")); err != nil || runtime.GOOS != "windows" && fi.Mode().Perm()&0o100 == 0 {
		t.Errorf("run.sh: %v %v", fi, err)
	}
	if _, err := os.Lstat(filepath.Join(h2, ".claude/skills/old")); !os.IsNotExist(err) {
		t.Error("the skill the bundle hasn't is still in claude")
	}
	if _, err := os.Stat(filepath.Join(h2, "src/old/SKILL.md")); err != nil {
		t.Error("the user's folder went with the skill")
	}
	if _, err := os.Stat(filepath.Join(h2, ".claude/skills/mine/SKILL.md")); err != nil {
		t.Error("claude's own skill went")
	}
	iv, _ := ReadInstructions()
	if len(iv.Sets) != 2 || iv.Shared != "Company rules." {
		t.Errorf("sets: %+v", iv.Sets)
	}
	// …and it carries back what it was given; putting it again changes nothing
	back, _ := Collect()
	if mustJSON(t, back) != carried {
		t.Errorf("carried back differs:\n%s\n%s", mustJSON(t, back), carried)
	}
	r := ok(t)(Put(&in))
	if len(r.Changed) != 0 {
		t.Errorf("putting the same again wrote %v", r.Changed)
	}

	// made without keys, it leaves the keys here where they are
	stripped := *back
	stripped.MCP = []*Server{{Name: "fs", Transport: "stdio", Command: "npx", Env: map[string]string{"GITHUB_TOKEN": "", "MODE": "rw"}, Agents: []string{"codex"}}}
	ok(t)(Put(stripped.WithSecrets(back, func(k string) bool { return strings.Contains(k, "TOKEN") })))
	if s := read(t, filepath.Join(h2, ".codex/config.toml")); !strings.Contains(s, "ghp-secret") || !strings.Contains(s, `"rw"`) {
		t.Errorf("codex's servers after a keyless one:\n%s", s)
	}
}

func TestCarryRefuses(t *testing.T) {
	sandbox(t)
	for _, b := range []*Bundle{
		{Skills: []*CarriedSkill{{Name: "x", Files: map[string][]byte{"../../evil": nil}}}},
		{Skills: []*CarriedSkill{{Name: "x", Files: map[string][]byte{"/etc/evil": nil}}}},
		{Skills: []*CarriedSkill{{Name: "x", Files: map[string][]byte{`a\..\..\evil`: nil}}}},
		{Skills: []*CarriedSkill{{Name: "../x", Files: map[string][]byte{"SKILL.md": nil}}}},
		{Extra: map[string]string{"../x": "y"}},
		{Texts: map[string]string{"../../x": "y"}},
		{Instructions: Instructions{Sets: []InstrSet{{ID: "../x"}}}},
	} {
		if _, err := Put(b); err == nil {
			t.Errorf("put %+v", b)
		}
	}
	if _, err := os.Stat(path()); !os.IsNotExist(err) {
		t.Error("a refused bundle changed the library")
	}
}

func keysOf[V any](m map[string]V) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
