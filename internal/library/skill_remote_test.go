package library

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
)

// An agent on another computer is given, through the MCP router, the
// skills ticked for it and those ticked for no agent (a magpie in Docker
// ticks none), every one at the address any app is given; not one turned
// off here, one taken from another magpie, or any while the router is
// off. A skill's files are its folder's, without what readSkills leaves
// out, the ones that run marked, and its SKILL.md's
// disable-model-invocation read as Manual.
func TestRemoteSkills(t *testing.T) {
	h := sandbox(t)
	src := filepath.Join(h, "src")
	skill(t, filepath.Join(src, "deploy"), "deploy", "Ships the site")
	write(t, filepath.Join(src, "deploy", "scripts", "ship.sh"), "#!/bin/sh\necho shipped\n")
	if err := os.Chmod(filepath.Join(src, "deploy", "scripts", "ship.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(src, "deploy", "node_modules", "x", "index.js"), "module.exports = 1\n")
	skill(t, filepath.Join(src, "everyone"), "everyone", "For any agent")
	skill(t, filepath.Join(src, "theirs"), "theirs", "Codex's")
	skill(t, filepath.Join(src, "resting"), "resting", "Off here")
	skill(t, filepath.Join(src, "lent"), "lent", "From another magpie")
	write(t, filepath.Join(src, "wipe", "SKILL.md"), "---\nname: wipe\ndescription: Wipes the cache\ndisable-model-invocation: true\n---\nWipe it.\n")
	ok(t)(InstallSkills(src, []string{"deploy", "resting", "lent", "wipe"}, []string{"claude"}))
	ok(t)(InstallSkills(src, []string{"theirs"}, []string{"codex"}))
	ok(t)(InstallSkills(src, []string{"everyone"}, []string{}))
	ok(t)(SkillHere("resting", false))
	ok(t)(change(func(l *Library) error {
		if l.Borrowed == nil {
			l.Borrowed = map[string]*Borrowed{}
		}
		l.Borrowed["http://nas:3425"] = &Borrowed{Name: "nas", Skills: map[string]string{"lent": "x"}}
		return nil
	}))

	names := func(ks []gateway.RemoteSkill) []string {
		var out []string
		for _, k := range ks {
			out = append(out, k.Name)
		}
		return out
	}
	// the router's switch is the gateway's to read (hubSkillsFor): the
	// pull script is given the skills with it off
	remote := func(agent string) []gateway.RemoteSkill {
		t.Helper()
		ks, err := theHub{}.RemoteSkills(agent)
		if err != nil {
			t.Fatal(err)
		}
		return ks
	}
	if got := names(remote("claude")); !slices.Equal(got, []string{"deploy", "everyone", "wipe"}) {
		t.Fatalf("claude's: %v", got)
	}
	if got := names(remote("claude" + gateway.HubProjectSep + "p1")); !slices.Equal(got, []string{"deploy", "everyone", "wipe"}) {
		t.Fatalf("claude's in a project: %v", got)
	}
	if got := names(remote("")); !slices.Equal(got, []string{"deploy", "everyone", "theirs", "wipe"}) {
		t.Fatalf("any app's: %v", got)
	}
	all := remote("")
	if i := slices.IndexFunc(all, func(k gateway.RemoteSkill) bool { return k.Name == "wipe" }); !all[i].Manual || all[0].Manual || all[0].About != "Ships the site" {
		t.Fatalf("meta: %+v", all)
	}
	k, fs, found := theHub{}.RemoteSkill("claude", "deploy")
	if !found || k.About != "Ships the site" || string(fs.Files["scripts/ship.sh"]) != "#!/bin/sh\necho shipped\n" || !slices.Equal(fs.Exec, []string{"scripts/ship.sh"}) {
		t.Fatalf("deploy: %+v %+v", k, fs)
	}
	if _, ok := fs.Files["SKILL.md"]; !ok {
		t.Fatalf("no SKILL.md: %v", fs.Files)
	}
	for f := range fs.Files {
		if filepath.Base(filepath.Dir(f)) == "x" {
			t.Fatalf("installed dependencies given: %v", f)
		}
	}
	for _, name := range []string{"theirs", "resting", "lent", "nope"} {
		if _, _, found := (theHub{}).RemoteSkill("claude", name); found {
			t.Errorf("claude given %s", name)
		}
	}
}
