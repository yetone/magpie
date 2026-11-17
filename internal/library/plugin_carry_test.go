package library

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

// inJSON is s as a JSON file holds it, without its quotes.
func inJSON(s string) string { b, _ := json.Marshal(s); return string(b[1 : len(b)-1]) }

// #6685 (JJasonsun on Discord): a plugin installed through the Library
// on one computer never reached the other. Its parts went as ordinary
// skills, hooks and servers naming the first computer's folder, and its
// record didn't go at all. Now the plugin goes whole, from a folder with
// its files, and the second computer installs it in its own folder: its
// hook names that folder, its script is there, and nothing the first
// computer's paths are in goes back.
func TestPluginCarriedWhole(t *testing.T) {
	h := sandbox(t)
	pluginAgents(t, h)
	src := filepath.Join(h, "work", "acme")
	writePlugin(t, src)
	write(t, filepath.Join(src, "scripts/lint.sh"), "#!/bin/sh\necho lint\n")
	os.Chmod(filepath.Join(src, "scripts/lint.sh"), 0o755)
	ok(t)(InstallPlugins(src, nil, []string{"claude"}))
	ok(t)(PluginOn("acme-kit", true))
	first := pluginDir("acme-kit")

	b, err := Collect()
	if err != nil {
		t.Fatal(err)
	}
	carried := mustJSON(t, b)
	if b.Plugins == nil || len(*b.Plugins) != 1 {
		t.Fatalf("plugins carried: %s", carried)
	}
	p := (*b.Plugins)[0]
	if p.Name != "acme-kit" || p.Version != "1.2.0" || p.Source != src || p.Files["hooks/hooks.json"] == nil || p.Files["scripts/lint.sh"] == nil {
		t.Fatalf("the plugin carried: %+v", p)
	}
	if runtime.GOOS != "windows" && (len(p.Exec) != 1 || p.Exec[0] != "scripts/lint.sh") {
		t.Errorf("the script that runs: %v", p.Exec)
	}
	// its parts don't go on their own: each names this computer's folder
	if len(b.Skills) != 0 || len(b.MCP) != 0 || len(*b.Hooks) != 0 || len(*b.Subagents) != 0 {
		t.Errorf("a plugin's parts carried as the library's own: %d skills, %d servers, %d hooks, %d subagents",
			len(b.Skills), len(b.MCP), len(*b.Hooks), len(*b.Subagents))
	}
	if strings.Contains(carried, inJSON(first)) {
		t.Errorf("the bundle names the first computer's plugin folder %s", first)
	}

	// the second computer has no such folder
	h2 := sandbox(t)
	pluginAgents(t, h2)
	second := pluginDir("acme-kit")
	if second == first {
		t.Fatal("one library folder for both computers")
	}
	r := putJSON(t, carried)
	for _, pr := range r.Problems {
		if pr.What != "plugin:acme-kit" || !strings.Contains(pr.Error, "commands") && !strings.Contains(pr.Error, "PreCompact") {
			t.Errorf("a problem: %+v", pr)
		}
	}
	v := View0(t)
	if len(v.Plugins) != 1 || v.Plugins[0].Name != "acme-kit" || v.Plugins[0].Version != "1.2.0" || len(v.Plugins[0].Hooks) != 3 || len(v.Plugins[0].Skills) != 1 {
		t.Fatalf("the plugin on the second computer: %+v", v.Plugins)
	}
	if len(v.Skills) != 1 || v.Skills[0].Bundle != "acme-kit" {
		t.Errorf("its skill: %+v", v.Skills)
	}
	settings := read(t, filepath.Join(h2, ".claude/settings.json"))
	if !strings.Contains(settings, inJSON(second+"/scripts/lint.sh")) {
		t.Errorf("claude's hook doesn't name this computer's folder %s:\n%s", second, settings)
	}
	if strings.Contains(settings, inJSON(first)) {
		t.Errorf("claude's hook names the first computer's folder:\n%s", settings)
	}
	if cj := read(t, filepath.Join(h2, ".claude.json")); !strings.Contains(cj, inJSON(second+"/bin/db")) {
		t.Errorf("claude's server doesn't name this computer's folder:\n%s", cj)
	}
	fi, err := os.Stat(filepath.Join(second, "scripts", "lint.sh"))
	if err != nil {
		t.Fatalf("the hook's script: %v", err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm()&0o111 == 0 {
		t.Errorf("the hook's script doesn't run: %v", fi.Mode())
	}

	// what it carries back is what came: no sync goes back and forth
	b2, err := Collect()
	if err != nil {
		t.Fatal(err)
	}
	if back := mustJSON(t, b2); back != carried {
		t.Errorf("the second computer carries back another library:\n%s\nwas\n%s", back, carried)
	}
	// and the same bundle again installs nothing again
	installed := v.Plugins[0].Installed
	putJSON(t, carried)
	if v := View0(t); v.Plugins[0].Installed != installed {
		t.Errorf("the same plugin installed again: %s, was %s", v.Plugins[0].Installed, installed)
	}

	// turned off on the first: off on the second
	(*b.Plugins)[0].Off = true
	putJSON(t, mustJSON(t, b))
	if s := read(t, filepath.Join(h2, ".claude/settings.json")); strings.Contains(s, "lint.sh") {
		t.Errorf("the hook stays while the plugin is off:\n%s", s)
	}

	// taken out on the first: out of the second, its folder too
	none := []*CarriedPlugin{}
	b.Plugins = &none
	putJSON(t, mustJSON(t, b))
	if v := View0(t); len(v.Plugins) != 0 || len(v.Skills) != 0 || len(v.Hooks) != 0 || len(v.Servers) != 0 || len(v.Subagents) != 0 {
		t.Errorf("the plugin stays: %+v", v)
	}
	if _, err := os.Stat(second); err == nil {
		t.Errorf("its folder stays")
	}
}

// A plugin from a repository goes as its source, without its files, and
// the second computer installs it from there, in its own folder. One
// that can't be fetched there yet is kept as pending, with the problem
// said, and Update fetches it.
func TestPluginCarriedBySource(t *testing.T) {
	files := map[string]string{
		".claude-plugin/plugin.json": `{"name":"repo-kit","version":"2.0.0"}`,
		"hooks/hooks.json":           `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"${CLAUDE_PLUGIN_ROOT}/bin/done"}]}]}}`,
		"skills/a/SKILL.md":          "---\nname: a\ndescription: A\n---\nA\n",
	}
	var down atomic.Bool
	tar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			http.Error(w, "no", http.StatusServiceUnavailable)
			return
		}
		w.Write(tarball(t, files))
	}))
	t.Cleanup(tar.Close)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"sha":"p1","commit":{"message":"x","committer":{"date":"2026-09-01T10:00:00Z"}}}]`))
	}))
	t.Cleanup(api.Close)
	oldT, oldA := tarballURL, githubAPI
	tarballURL = func(repo, ref string) string { return tar.URL + "/tar/" + repo }
	githubAPI = api.URL
	t.Cleanup(func() { tarballURL, githubAPI = oldT, oldA })

	const link = "https://github.com/acme/repo-kit"
	h := sandbox(t)
	pluginAgents(t, h)
	ok(t)(InstallPlugins(link, nil, []string{"claude"}))
	b, err := Collect()
	if err != nil {
		t.Fatal(err)
	}
	if len(*b.Plugins) != 1 || (*b.Plugins)[0].Source != link || (*b.Plugins)[0].Files != nil {
		t.Fatalf("carried: %s", mustJSON(t, b.Plugins))
	}
	carried := mustJSON(t, b)

	h2 := sandbox(t)
	pluginAgents(t, h2)
	putJSON(t, carried)
	second := pluginDir("repo-kit")
	if s := read(t, filepath.Join(h2, ".claude/settings.json")); !strings.Contains(s, inJSON(second+"/bin/done")) {
		t.Errorf("claude's hook doesn't name this computer's folder:\n%s", s)
	}
	if v := View0(t); len(v.Plugins) != 1 || v.Plugins[0].Pending || len(v.Plugins[0].Skills) != 1 || v.Plugins[0].Source != link {
		t.Errorf("installed from its source: %+v", v.Plugins)
	}
	if back := mustJSON(t, must(Collect())); back != carried {
		t.Errorf("the second computer carries back another library:\n%s\nwas\n%s", back, carried)
	}

	// a third computer that can't reach it yet
	h3 := sandbox(t)
	pluginAgents(t, h3)
	down.Store(true)
	r := putJSON(t, carried)
	if len(r.Problems) != 1 || r.Problems[0].What != "plugin:repo-kit" {
		t.Errorf("problems: %+v", r.Problems)
	}
	if v := View0(t); len(v.Plugins) != 1 || !v.Plugins[0].Pending {
		t.Fatalf("not kept as pending: %+v", v.Plugins)
	}
	// carried on as it came, so the others aren't told to change
	if back := mustJSON(t, must(Collect())); back != carried {
		t.Errorf("a pending plugin carried as another:\n%s\nwas\n%s", back, carried)
	}
	down.Store(false)
	ok(t)(UpdatePlugin("repo-kit"))
	if v := View0(t); v.Plugins[0].Pending || len(v.Plugins[0].Hooks) != 1 {
		t.Errorf("after Update: %+v", v.Plugins[0])
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// A bundle from a magpie before plugins were carried has a plugin's parts
// as its own items, naming the other computer's folder, and no plugins:
// this computer's plugin stays, with its own parts.
func TestPluginOldBundleKeepsPlugins(t *testing.T) {
	h := sandbox(t)
	pluginAgents(t, h)
	src := filepath.Join(h, "acme")
	writePlugin(t, src)
	ok(t)(InstallPlugins(src, nil, []string{"claude"}))
	b, err := Collect()
	if err != nil {
		t.Fatal(err)
	}
	b.Plugins = nil
	const there = "/Users/other/.config/magpie/library/plugins/acme-kit"
	b.Skills = append(b.Skills, &CarriedSkill{Name: "tdd", Agents: []string{"claude"},
		Files: map[string][]byte{"SKILL.md": []byte("---\nname: tdd\ndescription: Test first\n---\nRun " + there + "/x\n")}})
	hooks := []*Hook{{Name: "acme-kit-1", Event: "PostToolUse", Matcher: "Bash", Command: there + "/scripts/lint.sh", Agents: []string{"claude"}, Bundle: "acme-kit"}}
	b.Hooks = &hooks
	old := mustJSON(t, b)
	if strings.Contains(old, `"plugins"`) {
		t.Fatalf("not as an older magpie wrote it: %s", old)
	}
	putJSON(t, old)
	v := View0(t)
	if len(v.Plugins) != 1 || len(v.Plugins[0].Missing) != 0 || len(v.Skills) != 1 || len(v.Hooks) != 3 {
		t.Errorf("an old bundle took this computer's plugin: plugins %+v, %d skills, %d hooks", v.Plugins, len(v.Skills), len(v.Hooks))
	}
	if s := read(t, filepath.Join(h, ".claude/settings.json")); strings.Contains(s, there) || !strings.Contains(s, inJSON(pluginDir("acme-kit"))) {
		t.Errorf("claude's hooks after an old bundle:\n%s", s)
	}
	if s := read(t, filepath.Join(skillDir("tdd"), "SKILL.md")); strings.Contains(s, there) {
		t.Errorf("the plugin's skill written over: %s", s)
	}
}

// The parts of a plugin a marketplace names other than its manifest does
// name the folder it is kept in, the marketplace's name's: they named the
// manifest's, a folder that isn't there.
func TestPluginMarketplaceNameIsItsFolder(t *testing.T) {
	h := sandbox(t)
	pluginAgents(t, h)
	mk := filepath.Join(h, "market")
	write(t, filepath.Join(mk, ".claude-plugin/marketplace.json"), `{"name":"m","plugins":[
	  {"name":"one","source":"./plugins/one"},{"name":"two","source":"./plugins/two"}]}`)
	write(t, filepath.Join(mk, "plugins/one/.claude-plugin/plugin.json"), `{"name":"one-by-its-own-name"}`)
	write(t, filepath.Join(mk, "plugins/one/hooks/hooks.json"), `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"${CLAUDE_PLUGIN_ROOT}/run"}]}]}}`)
	write(t, filepath.Join(mk, "plugins/two/skills/b/SKILL.md"), "---\nname: b\ndescription: B\n---\nB\n")
	ok(t)(InstallPlugins(mk, []string{"one"}, []string{"claude"}))
	s := read(t, filepath.Join(h, ".claude/settings.json"))
	if !strings.Contains(s, inJSON(pluginDir("one")+"/run")) {
		t.Errorf("the hook doesn't name the plugin's folder %s:\n%s", pluginDir("one"), s)
	}
	if _, err := os.Stat(pluginDir("one")); err != nil {
		t.Errorf("its folder: %v", err)
	}
}

// Whether a plugin goes with its files is the same on every computer: a
// record synced from a Mac names /Users/…, one from Windows C:\…, and the
// computer carrying it on may be the other one.
func TestPluginFromFilesEitherOS(t *testing.T) {
	for src, want := range map[string]bool{
		"/Users/a/work/kit":                      true,
		`C:\Users\a\kit`:                         true,
		"c:/Users/a/kit":                         true,
		`\\nas\share\kit`:                        true,
		"~/kit":                                  true,
		"":                                       true,
		"https://tok@github.com/acme/kit":        true,
		"https://github.com/acme/kit":            false,
		"acme/kit":                               false,
		"git@github.com:acme/kit.git":            false,
		"ssh://git@git.example.com/acme/kit.git": false,
	} {
		if got := fromFiles(src); got != want {
			t.Errorf("fromFiles(%q) = %v, want %v", src, got, want)
		}
	}
}
