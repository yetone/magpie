package library

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// #7269 (jinpwng): a Pi package imported from a git repository's file://
// address, given to a project for Pi, is updated in place: the same bundle,
// the same folder, so the project's one entry in .pi/settings.json reaches
// the new version and nothing else in it changes.
func TestPiPackageFromGitUpdatesInPlace(t *testing.T) {
	h := sandbox(t)
	pluginAgents(t, h)
	addr, commit := gitRepo(t, map[string]string{
		"package.json":         `{"name":"acme-pi","version":"1.0.0","pi":{"extensions":["./extension.ts"],"skills":["./skills"]}}`,
		"extension.ts":         "export default function (pi) { pi.registerCommand('acme-v1', {}) }\n",
		"skills/tidy/SKILL.md": "---\nname: tidy\ndescription: Tidies\n---\nv1\n",
	})
	if _, err := ProbePlugins(addr); err != nil {
		t.Fatal(err)
	}
	res := ok(t)(InstallPlugins(addr, nil, []string{}))
	if len(res.Installed) != 1 {
		t.Fatalf("installed: %+v", res.Installed)
	}
	name := res.Installed[0]
	if strings.HasPrefix(name, skillTemp) {
		t.Fatalf("named after its download's temp folder: %s", name)
	}
	proj := filepath.Join(t.TempDir(), "proj")
	gitInit(t, proj)
	ok(t)(AddProject(proj))
	file := filepath.Join(proj, ".pi/settings.json")
	write(t, file, "{\n  \"packages\": [\"npm:pi-own\"]\n}\n")
	ok(t)(ProjectPlugin(proj, name, []string{"pi"}))
	before := read(t, file)
	if !strings.Contains(before, pluginDir(name)) || !strings.Contains(before, "npm:pi-own") {
		t.Fatalf("assigned:\n%s", before)
	}

	commit(map[string]string{
		"package.json": `{"name":"acme-pi","version":"2.0.0","pi":{"extensions":["./extension.ts"],"skills":["./skills"]}}`,
		"extension.ts": "export default function (pi) { pi.registerCommand('acme-v2', {}) }\n",
	})
	forgetProbes()
	ok(t)(UpdatePlugin(name))

	v := View0(t)
	if len(v.Plugins) != 1 || v.Plugins[0].Name != name {
		var names []string
		for _, b := range v.Plugins {
			names = append(names, b.Name)
		}
		t.Fatalf("after the update the library has %v, want only %s", names, name)
	}
	if s := read(t, filepath.Join(pluginDir(name), "extension.ts")); !strings.Contains(s, "acme-v2") {
		t.Fatalf("the bundle's folder has %q", s)
	}
	if after := read(t, file); after != before {
		t.Fatalf("the project's settings changed:\n%s\nwas\n%s", after, before)
	}
	if !slices.Contains(v.Projects[0].Plugins[name], "pi") {
		t.Fatalf("project's plugins: %+v", v.Projects[0].Plugins)
	}
}

// A bundle named before by its download's temp folder (magpie-skill-…)
// keeps that name on its update: it is the one the projects have.
func TestPluginUpdateKeepsItsName(t *testing.T) {
	h := sandbox(t)
	pluginAgents(t, h)
	addr, commit := gitRepo(t, map[string]string{
		"package.json":         `{"name":"acme-pi","version":"1.0.0","pi":{"skills":["./skills"]}}`,
		"skills/tidy/SKILL.md": "---\nname: tidy\ndescription: Tidies\n---\nv1\n",
	})
	res := ok(t)(InstallPlugins(addr, nil, []string{}))
	was := res.Installed[0]
	old := skillTemp + "4102397733"
	ok(t)(change(func(l *Library) error {
		b := l.plugin(was)
		removePluginParts(l, b)
		l.Plugins = slices.DeleteFunc(l.Plugins, func(x *Plugin) bool { return x == b })
		return nil
	}))
	if err := os.Rename(pluginDir(was), pluginDir(old)); err != nil {
		t.Fatal(err)
	}
	ok(t)(change(func(l *Library) error {
		l.Plugins = append(l.Plugins, &Plugin{Name: old, Source: addr, Agents: []string{}})
		return nil
	}))
	proj := filepath.Join(t.TempDir(), "proj")
	gitInit(t, proj)
	ok(t)(AddProject(proj))
	ok(t)(ProjectPlugin(proj, old, []string{"pi"}))
	file := filepath.Join(proj, ".pi/settings.json")
	before := read(t, file)

	commit(map[string]string{"skills/tidy/SKILL.md": "---\nname: tidy\ndescription: Tidies\n---\nv2\n"})
	forgetProbes()
	ok(t)(UpdatePlugin(old))
	v := View0(t)
	if len(v.Plugins) != 1 || v.Plugins[0].Name != old {
		t.Fatalf("plugins: %+v", v.Plugins)
	}
	if s := read(t, filepath.Join(pluginDir(old), "skills/tidy/SKILL.md")); !strings.Contains(s, "v2") {
		t.Fatalf("not updated: %q", s)
	}
	if read(t, file) != before {
		t.Fatalf("project settings changed:\n%s", read(t, file))
	}
}

// The top of a downloaded repository goes by the repository's name, never
// by its download's temp folder; a folder by its own name.
func TestPluginRootName(t *testing.T) {
	tmp := filepath.Join(os.TempDir(), skillTemp+"123")
	for in, want := range map[string]string{
		"file:///srv/git/acme-pi.git":            "acme-pi",
		"file:///Users/me/src/acme-pi":           "acme-pi",
		"file:///Users/me/src/acme-pi/.git":      "acme-pi",
		"https://github.com/acme/pi-kit":         "pi-kit",
		"acme/pi-kit":                            "pi-kit",
		"git@gitlab.example.com:team/pi-kit.git": "pi-kit",
		"https://gitlab.com/g/sub/pi-kit.git":    "pi-kit",
	} {
		if got := rootName(in, tmp, tmp); got != want {
			t.Errorf("%s: %q, want %q", in, got, want)
		}
	}
	if got := rootName("acme/pi-kit", filepath.Join(tmp, "plugins", "kit"), tmp); got != "kit" {
		t.Errorf("a folder in the download: %q", got)
	}
	if got := rootName("/Users/me/acme", "/Users/me/acme", ""); got != "acme" {
		t.Errorf("a folder: %q", got)
	}
}
