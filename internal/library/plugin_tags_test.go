package library

import (
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// #7549 (jinpwng): a plugin installed from a git source pinned to a
// release tag shows the pin and its repository's newer stable tags.
// Update keeps the pin; an upgrade to a newer tag installs that one under
// the same name, its project keeps it, and the project's copy of its
// skill is made again from the new files.
func TestPluginUpgradesToNewerTag(t *testing.T) {
	h := sandbox(t)
	pluginAgents(t, h)
	addr, commit := gitRepo(t, map[string]string{
		".claude-plugin/plugin.json": `{"name":"acme","version":"1.0.0"}`,
		"skills/tidy/SKILL.md":       "---\nname: tidy\ndescription: Tidies\n---\nmarker-v1\n",
	})
	bare := strings.TrimPrefix(addr, "file://")
	tag := func(name string) {
		t.Helper()
		if out, err := exec.Command(realGit, "--git-dir", filepath.FromSlash(bare), "tag", name, "refs/heads/main").CombinedOutput(); err != nil {
			t.Fatalf("git tag %s: %v\n%s", name, err, out)
		}
	}
	tag("v1.0.0")
	pinned := addr + "/-/tree/v1.0.0"
	res := ok(t)(InstallPlugins(pinned, nil, []string{}))
	if !slices.Equal(res.Installed, []string{"acme"}) {
		t.Fatalf("installed: %v", res.Installed)
	}
	proj := filepath.Join(t.TempDir(), "proj")
	gitInit(t, proj)
	ok(t)(AddProject(proj))
	ok(t)(ProjectCopy(proj, true))
	ok(t)(ProjectPlugin(proj, "acme", []string{"claude"}))
	copied := filepath.Join(proj, ".claude", "skills", "tidy", "SKILL.md")
	if !strings.Contains(read(t, copied), "marker-v1") {
		t.Fatalf("the project's copy: %q", read(t, copied))
	}
	write(t, filepath.Join(proj, "keep.txt"), "the project's own\n")

	commit(map[string]string{
		".claude-plugin/plugin.json": `{"name":"acme","version":"1.1.0"}`,
		"skills/tidy/SKILL.md":       "---\nname: tidy\ndescription: Tidies\n---\nmarker-v1.1\n",
	})
	tag("v1.1.0")
	tag("1.0.5")      // newer, written without a v
	tag("v2.0.0-rc1") // no pre-release
	tag("v0.9.0")     // older

	pin, err := PluginTags("acme")
	if err != nil {
		t.Fatal(err)
	}
	if want := (&PluginPin{Name: "acme", Pin: "v1.0.0", Newer: []string{"v1.1.0", "1.0.5"}}); !reflect.DeepEqual(pin, want) {
		t.Errorf("tags = %+v, want %+v", pin, want)
	}

	// an ordinary update keeps the pin
	forgetProbes()
	ok(t)(UpdatePlugin("acme"))
	v := View0(t)
	if v.Plugins[0].Source != pinned || !strings.Contains(read(t, copied), "marker-v1\n") {
		t.Errorf("update moved the pin: %s, %q", v.Plugins[0].Source, read(t, copied))
	}

	// the upgrade
	ok(t)(UpgradePlugin("acme", "v1.1.0"))
	v = View0(t)
	if len(v.Plugins) != 1 || v.Plugins[0].Name != "acme" || v.Plugins[0].Source != addr+"/-/tree/v1.1.0" {
		t.Fatalf("plugins after the upgrade: %+v", v.Plugins)
	}
	if !strings.Contains(read(t, copied), "marker-v1.1") {
		t.Errorf("the project's copy isn't made again: %q", read(t, copied))
	}
	if !slices.Contains(v.Projects[0].Plugins["acme"], "claude") || read(t, filepath.Join(proj, "keep.txt")) != "the project's own\n" {
		t.Errorf("the project lost its plugin or its own file: %+v", v.Projects[0].Plugins)
	}
	if pin, err := PluginTags("acme"); err != nil || pin.Pin != "v1.1.0" || len(pin.Newer) != 0 {
		t.Errorf("tags after: %+v %v", pin, err)
	}
	// not a release tag, or no pin at all
	if _, err := UpgradePlugin("acme", "main"); err == nil {
		t.Error("upgraded to a branch")
	}
}

func TestRetagSource(t *testing.T) {
	for _, c := range [][3]string{
		{"https://github.com/acme/kit/tree/v1.0.0/plugins/a", "v1.2.0", "https://github.com/acme/kit/tree/v1.2.0/plugins/a"},
		{"https://github.com/acme/kit/tree/v1.0.0", "v1.2.0", "https://github.com/acme/kit/tree/v1.2.0"},
		{"https://gitlab.com/g/kit/-/tree/1.0/x", "1.1", "https://gitlab.com/g/kit/-/tree/1.1/x"},
		{"https://gitea.io/g/kit/src/tag/v1/x", "v2", "https://gitea.io/g/kit/src/tag/v2/x"},
		// a folder named like the tag isn't the tag
		{"https://github.com/acme/v1.0.0x/tree/v1.0.0/v1.0.0", "v1.1.0", "https://github.com/acme/v1.0.0x/tree/v1.1.0/v1.0.0"},
	} {
		src, ok := pinnedSource(c[0])
		if !ok {
			t.Errorf("%s: not pinned", c[0])
			continue
		}
		if got, err := retagSource(c[0], src.Ref, c[1]); err != nil || got != c[2] {
			t.Errorf("%s → %s: %s %v, want %s", c[0], c[1], got, err, c[2])
		}
	}
	for _, s := range []string{"https://github.com/acme/kit", "https://github.com/acme/kit/tree/main", "https://github.com/acme/kit/tree/0123abcd"} {
		if _, ok := pinnedSource(s); ok {
			t.Errorf("%s read as pinned", s)
		}
	}
}
