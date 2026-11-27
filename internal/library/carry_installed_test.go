package library

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// #7952: dependencies installed inside a skill must not use the sync's
// file budget or reach another computer as a partial installation
// Lock files stay: a skill can ship one that npm ci needs to rebuild it
func TestCarryLeavesInstalledFilesLocal(t *testing.T) {
	sandbox(t)
	ok(t)(CreateSkill("mine", "my own", []string{}))
	dir := skillDir("mine")
	content := map[string]string{
		"SKILL.md":                  read(t, filepath.Join(dir, "SKILL.md")),
		"package.json":              `{"name":"mine","version":"1.0.0"}`,
		"package-lock.json":         `{"lockfileVersion":3,"packages":{}}`,
		"yarn.lock":                 "# yarn lockfile v1\n",
		"pnpm-lock.yaml":            "lockfileVersion: '9.0'\n",
		"bun.lock":                  `{"lockfileVersion":1}`,
		"bun.lockb":                 "\x00bun\xff",
		"uv.lock":                   "version = 1\n",
		"nested/package-lock.json":  `{"lockfileVersion":3}`,
		"scripts/run.py":            "print('skill')\n",
		"templates/image.png":       "\x89PNG\r\n\x1a\n\x00\xff",
		"references/venv":           "a file, not an environment directory",
		"fixtures.pyc/example.json": `{"compiled":false}`,
	}
	for rel, data := range content {
		write(t, filepath.Join(dir, filepath.FromSlash(rel)), data)
	}
	before := ownHash(dir)
	if before == "" {
		t.Fatal("skill's own files could not be hashed")
	}
	installed := []string{
		"node_modules/pkg/index.js", ".venv/Lib/site-packages/pkg.py", "venv/lib/pkg.py",
		"__pycache__/run.cpython-312.pyc", ".pytest_cache/v/cache/nodeids",
		".mypy_cache/3.12/cache.json", ".ruff_cache/result",
		"nested/node_modules/pkg/index.js", "nested/run.pyc", "nested/run.pyo",
	}
	for _, rel := range installed {
		write(t, filepath.Join(dir, filepath.FromSlash(rel)), "installed on this computer")
	}
	if got := ownHash(dir); got != before {
		t.Fatalf("install changed the skill's own files: %s, was %s", got, before)
	}
	for name, collect := range map[string]func() (*Bundle, error){"backup": Collect, "remote sharing": Shared} {
		t.Run(name, func(t *testing.T) {
			b, err := collect()
			if err != nil {
				t.Fatal(err)
			}
			c := carriedSkill(t, b, "mine")
			if len(c.Files) != len(content) || len(c.Left) != 0 {
				t.Errorf("carried %v, left %v; want only the skill's own files", keysOf(c.Files), c.Left)
			}
			for rel, want := range content {
				if !bytes.Equal(c.Files[rel], []byte(want)) {
					t.Errorf("skill file %s was lost or changed: %q", rel, c.Files[rel])
				}
			}
			for _, rel := range installed {
				if _, ok := c.Files[rel]; ok || slices.Contains(c.Left, rel) || slices.Contains(c.Exec, rel) {
					t.Errorf("installation file %s is in the bundle", rel)
				}
			}
		})
	}
	// An unchanged bundle keeps the receiving computer's own installation
	ok(t)(Put(must(Collect())))
	for _, rel := range installed {
		if got := read(t, filepath.Join(dir, filepath.FromSlash(rel))); got != "installed on this computer" {
			t.Errorf("putting the same skill removed local %s", rel)
		}
	}
	// Another computer gets the scripts, assets and locks, not an install
	b := must(Collect())
	sandbox(t)
	ok(t)(Put(b))
	for rel, want := range content {
		if got := read(t, filepath.Join(skillDir("mine"), filepath.FromSlash(rel))); got != want {
			t.Errorf("the other computer's %s: %q, want %q", rel, got, want)
		}
	}
	for _, rel := range installed {
		if _, err := os.Stat(filepath.Join(skillDir("mine"), filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Errorf("the other computer received installation file %s: %v", rel, err)
		}
	}
}

func TestCarryInstalledFilesTakeNoBudget(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	write(t, filepath.Join(a, "SKILL.md"), "skill a\n")
	write(t, filepath.Join(b, "SKILL.md"), "skill b\n")
	write(t, filepath.Join(a, "node_modules/pkg/tiny.js"), "x")
	write(t, filepath.Join(a, "nested/.venv/large.bin"), "too large for this budget")
	write(t, filepath.Join(a, "run.pyc"), "x")
	for i, c := range readSkillsMax([]string{a, b}, 8, 16) {
		if len(c.Files) != 1 || string(c.Files["SKILL.md"]) != []string{"skill a\n", "skill b\n"}[i] || len(c.Left) != 0 {
			t.Errorf("skill %d: files %v, left %v; installations used the content's budget", i, keysOf(c.Files), c.Left)
		}
	}
}

// Older bundles can record installation files as too large and missing
// They no longer belong in Left, while source files and locks still do
func TestCarryOldMissingInstalledFilesStayLocal(t *testing.T) {
	sandbox(t)
	ok(t)(CreateSkill("mine", "my own", []string{}))
	b := must(Collect())
	c := carriedSkill(t, b, "mine")
	c.Left = []string{"node_modules/pkg/index.js", "nested/.venv/lib/pkg.py", "run.pyc", "nested/run.pyo",
		"templates/large.pptx", "package-lock.json", "fixtures.pyc/example.json", "references/venv"}
	ok(t)(Put(b))
	if _, err := os.Stat(filepath.Join(skillDir("mine"), "templates/large.pptx")); !os.IsNotExist(err) {
		t.Fatalf("missing template unexpectedly exists: %v", err)
	}
	c = carriedSkill(t, must(Collect()), "mine")
	if !slices.Equal(c.Left, []string{"fixtures.pyc/example.json", "package-lock.json", "references/venv", "templates/large.pptx"}) {
		t.Errorf("old missing installation files were carried on: %v", c.Left)
	}
}

// Folder plugins share the same file budget with skills
func TestCarryPluginLeavesInstalledFilesLocal(t *testing.T) {
	h := sandbox(t)
	pluginAgents(t, h)
	src := filepath.Join(h, "work", "acme")
	writePlugin(t, src)
	ok(t)(InstallPlugins(src, nil, []string{"claude"}))
	dir := pluginDir("acme-kit")
	write(t, filepath.Join(dir, "node_modules/pkg/index.js"), "module.exports = 1")
	write(t, filepath.Join(dir, "skills/pdf/__pycache__/run.pyc"), "bytecode")
	write(t, filepath.Join(dir, "package-lock.json"), `{"lockfileVersion":3}`)
	b := must(Collect())
	if b.Plugins == nil || len(*b.Plugins) != 1 {
		t.Fatalf("plugins: %+v", b.Plugins)
	}
	p := (*b.Plugins)[0]
	for _, rel := range []string{"node_modules/pkg/index.js", "skills/pdf/__pycache__/run.pyc"} {
		if _, ok := p.Files[rel]; ok || slices.Contains(p.Left, rel) {
			t.Errorf("plugin installation file %s is in the bundle", rel)
		}
	}
	if string(p.Files["package-lock.json"]) != `{"lockfileVersion":3}` || p.Files["hooks/hooks.json"] == nil {
		t.Errorf("plugin lost its lock or hooks: %v", keysOf(p.Files))
	}
}
