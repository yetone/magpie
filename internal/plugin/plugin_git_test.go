package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// Lemon on Discord: can a plugin be installed straight from git (GitHub)?
// bun add takes github:owner/repo, owner/repo, a GitHub URL and git+…
// URLs, and installs the package at node_modules/<the name its own
// package.json gives>, not the spec's.
func TestIsGit(t *testing.T) {
	for spec, want := range map[string]bool{
		"github:owner/repo":                        true,
		"github:owner/repo#v1.2.0":                 true,
		"gitlab:owner/repo":                        true,
		"owner/repo":                               true,
		"owner/repo#main":                          true,
		"https://github.com/owner/repo":            true,
		"https://github.com/owner/repo.git":        true,
		"git+https://github.com/owner/repo.git":    true,
		"git+ssh://git@github.com/owner/repo.git":  true,
		"git+file:///tmp/repo.git":                 true,
		"git://github.com/owner/repo.git":          true,
		"opencode-gemini-auth":                     false,
		"opencode-gemini-auth@latest":              false,
		"@magpie-community/opencode-kiro-auth":     false,
		"@magpie-community/opencode-kiro-auth@1.2": false,
		"./my-plugin.js":                           false,
		"/abs/plugin":                              false,
		"file:///abs/plugin":                       false,
		`C:\plugins\x`:                             false,
		"https://example.com/x.tgz":                false,
	} {
		if got := IsGit(spec); got != want {
			t.Errorf("IsGit(%q) = %v, want %v", spec, got, want)
		}
	}
	// an npm spec is named as it was
	for spec, want := range map[string]string{"@scope/x@1.2": "@scope/x", "x@latest": "x", "x": "x"} {
		if got := Name(spec); got != want {
			t.Errorf("Name(%q) = %q, want %q", spec, got, want)
		}
	}
}

// The test binary stands in for bun when MAGPIE_FAKE_BUN is set: add,
// update and remove as bun does them for a git spec, fetching from local
// repositories ($MAGPIE_FAKE_GITHUB/owner/repo.git for GitHub's), and,
// like bun, keeping the commit its lock has when a spec is added again.
func init() {
	if os.Getenv("MAGPIE_FAKE_BUN") == "1" {
		if err := fakeBun(os.Args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "fake bun:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
}

func fakeBun(args []string) error {
	if f, err := os.OpenFile(os.Getenv("MAGPIE_FAKE_BUN_LOG"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		fmt.Fprintln(f, strings.Join(args, " "))
		f.Close()
	}
	if len(args) < 3 || args[1] != "--ignore-scripts" {
		return fmt.Errorf("unexpected %q", args)
	}
	var pj map[string]any
	b, err := os.ReadFile("package.json")
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, &pj); err != nil {
		return err
	}
	deps := map[string]string{}
	if d, ok := pj["dependencies"].(map[string]any); ok {
		for k, v := range d {
			deps[k], _ = v.(string)
		}
	}
	lock := map[string]string{}
	if b, err := os.ReadFile("fake.lock"); err == nil {
		_ = json.Unmarshal(b, &lock)
	}
	switch args[0] {
	case "add":
		spec := args[2]
		for n, s := range deps {
			if s == spec && lock[n] != "" {
				return nil // as bun: the lock's commit stays
			}
		}
		name, commit, err := fakeFetch(spec)
		if err != nil {
			return err
		}
		deps[name], lock[name] = spec, commit
	case "update":
		for _, n := range args[2:] {
			if deps[n] == "" {
				return fmt.Errorf("no dependency %s", n)
			}
			name, commit, err := fakeFetch(deps[n])
			if err != nil {
				return err
			}
			lock[name] = commit
		}
	case "remove":
		for _, n := range args[2:] {
			delete(deps, n)
			delete(lock, n)
			os.RemoveAll(filepath.Join("node_modules", n))
		}
	default:
		return fmt.Errorf("unexpected %q", args)
	}
	pj["dependencies"] = deps
	b, _ = json.MarshalIndent(pj, "", "  ")
	if err := os.WriteFile("package.json", b, 0o644); err != nil {
		return err
	}
	b, _ = json.Marshal(lock)
	return os.WriteFile("fake.lock", b, 0o644)
}

// fakeFetch clones spec's repository and puts the package in it at
// node_modules/<its name>.
func fakeFetch(spec string) (name, commit string, err error) {
	s, ref, _ := strings.Cut(spec, "#")
	var url string
	switch {
	case strings.HasPrefix(s, "git+file://"):
		url = strings.TrimPrefix(s, "git+")
	case strings.HasPrefix(s, "github:"), strings.HasPrefix(s, "https://github.com/"), IsGit(s) && !strings.Contains(s, ":"):
		repo := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(s, "github:"), "https://github.com/"), ".git")
		url = filepath.Join(os.Getenv("MAGPIE_FAKE_GITHUB"), filepath.FromSlash(repo)+".git")
	default:
		return "", "", fmt.Errorf("fetches only git, not %s", spec)
	}
	tmp, err := os.MkdirTemp("", "fakebun")
	if err != nil {
		return "", "", err
	}
	defer os.RemoveAll(tmp)
	clone := []string{"clone", "-q"}
	if ref != "" {
		clone = append(clone, "--branch", ref)
	}
	if out, err := exec.Command("git", append(clone, url, tmp)...).CombinedOutput(); err != nil {
		return "", "", fmt.Errorf("git clone %s: %v: %s", url, err, out)
	}
	out, err := exec.Command("git", "-C", tmp, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", "", err
	}
	var p struct{ Name string }
	b, err := os.ReadFile(filepath.Join(tmp, "package.json"))
	if err != nil {
		return "", "", err
	}
	if err := json.Unmarshal(b, &p); err != nil || p.Name == "" {
		return "", "", fmt.Errorf("%s has no package name", spec)
	}
	dest := filepath.Join("node_modules", filepath.FromSlash(p.Name))
	os.RemoveAll(dest)
	err = filepath.WalkDir(tmp, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(tmp, path)
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dest, rel), 0o755)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dest, rel), b, 0o644)
	})
	return p.Name, strings.TrimSpace(string(out)), err
}

// gitRepo is a plugin's repository: a checkout to commit to and the bare
// repository it pushes to, which is fetched from.
type gitRepo struct {
	t          *testing.T
	work, bare string
}

func (r gitRepo) git(dir string, args ...string) {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		r.t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

// release commits the plugin at version v and pushes it.
func (r gitRepo) release(v string) {
	r.t.Helper()
	pj := fmt.Sprintf(`{"name": "my-oc-plugin", "version": %q, "type": "module", "main": "index.js"}`, v)
	if err := os.WriteFile(filepath.Join(r.work, "package.json"), []byte(pj), 0o644); err != nil {
		r.t.Fatal(err)
	}
	r.git(r.work, "add", "-A")
	r.git(r.work, "commit", "-qm", "v"+v)
	r.git(r.work, "push", "-q", r.bare, "HEAD:main")
}

func newGitRepo(t *testing.T, bare string) gitRepo {
	t.Helper()
	r := gitRepo{t: t, work: filepath.Join(t.TempDir(), "work"), bare: bare}
	if err := os.MkdirAll(r.work, 0o755); err != nil {
		t.Fatal(err)
	}
	r.git(r.work, "init", "-q", "-b", "main")
	r.git(filepath.Dir(r.work), "init", "-q", "--bare", "-b", "main", bare)
	js, err := os.ReadFile("testdata/fake/index.js")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.work, "index.js"), js, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.work, "README.md"), []byte("# my-oc-plugin\n\nFrom git.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.release("1.0.0")
	return r
}

// A plugin added from GitHub (or any git URL) is kept as it was given,
// is known by the package its repository names, and lists, switches off
// and on, updates (fetching the repository again), shows its README and
// is removed like any other; npm is never asked about it.
func TestGitPlugin(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git on PATH")
	}
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	realBun, _ := exec.LookPath("bun")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MAGPIE_BUN", self) // never downloaded
	github := filepath.Join(dir, "github")
	log := filepath.Join(dir, "bun.log")
	orig := bunCommand
	bunCommand = func(ctx context.Context, bun, dir string, args ...string) *exec.Cmd {
		if len(args) > 0 && args[0] == "run" && realBun != "" {
			return orig(ctx, realBun, dir, args...)
		}
		cmd := exec.CommandContext(ctx, self, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "MAGPIE_FAKE_BUN=1", "MAGPIE_FAKE_GITHUB="+github, "MAGPIE_FAKE_BUN_LOG="+log)
		return cmd
	}
	t.Cleanup(func() { bunCommand = orig })
	t.Cleanup(Settle)
	asked := false
	origLatest := latestOf
	latestOf = func(ctx context.Context, names []string) map[string]string {
		asked = asked || slices.Contains(names, "my-oc-plugin")
		return map[string]string{"my-oc-plugin": "9.0.0"}
	}
	t.Cleanup(func() { latestOf = origLatest })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	repo := newGitRepo(t, filepath.Join(github, "owner", "repo.git"))
	const spec = "github:owner/repo"
	e, err := Add(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	if e.Spec != spec {
		t.Fatalf("added as %q, want the spec as given", e.Spec)
	}
	if n, v, p := Name(spec), Installed(spec), PackageName(spec); n != "my-oc-plugin" || v != "1.0.0" || p != "my-oc-plugin" {
		t.Fatalf("Name, Installed, PackageName = %q %q %q", n, v, p)
	}
	if got := Target(spec); got != filepath.Join(Dir(), "node_modules", "my-oc-plugin") {
		t.Fatalf("Target = %s", got)
	}
	if Pinned(spec) {
		t.Fatal("a git plugin is pinned")
	}
	if l := Load().Plugins; len(l) != 1 || l[0].Spec != spec {
		t.Fatalf("plugins = %+v", l)
	}
	if realBun != "" {
		loaded, err := Plugins(ctx)
		if err != nil || len(loaded) != 1 || loaded[0].Error != "" {
			t.Fatalf("Plugins = %+v, %v", loaded, err)
		}
		ps, err := Providers(ctx)
		if err != nil || len(ps) != 1 || ps[0].ID != "fakeco" || ps[0].Spec != spec {
			t.Fatalf("Providers = %+v, %v", ps, err)
		}
	}
	if p, err := Readme(ctx, spec); err != nil || !strings.Contains(p.Readme, "From git.") {
		t.Fatalf("Readme = %+v, %v", p, err)
	}

	// update fetches the repository's commit now, the spec kept
	repo.release("2.0.0")
	if err := Update(ctx); err != nil {
		t.Fatal(err)
	}
	if v := Installed(spec); v != "2.0.0" {
		t.Fatalf("after update: %s", v)
	}
	repo.release("3.0.0")
	if err := Upgrade(ctx, Name(spec)); err != nil {
		t.Fatal(err)
	}
	if v := Installed(spec); v != "3.0.0" || Load().Plugins[0].Spec != spec {
		t.Fatalf("after upgrade: %s %+v", v, Load().Plugins)
	}
	// npm's my-oc-plugin, whatever it is, is never offered over it
	if u, err := CheckUpdates(ctx); err != nil || asked || len(u.Waiting) != 0 || Installed(spec) != "3.0.0" {
		t.Fatalf("CheckUpdates = %+v, %v; npm asked: %v", u, err, asked)
	}

	// off and on, by its package or by the spec
	if err := SetOff("my-oc-plugin", true); err != nil || !Load().Plugins[0].Off {
		t.Fatalf("off: %v %+v", err, Load().Plugins)
	}
	if err := SetOff(spec, false); err != nil || Load().Plugins[0].Off {
		t.Fatalf("on: %v %+v", err, Load().Plugins)
	}

	// the same package from another URL takes the entry's place
	file := "git+file://" + filepath.ToSlash(repo.bare)
	if !strings.HasPrefix(file, "git+file:///") {
		file = "git+file:///" + strings.TrimPrefix(file, "git+file://")
	}
	if _, err := Add(ctx, file); err != nil {
		t.Fatal(err)
	}
	if l := Load().Plugins; len(l) != 1 || l[0].Spec != file || Name(file) != "my-oc-plugin" || Installed(file) != "3.0.0" {
		t.Fatalf("plugins = %+v", l)
	}

	if err := Remove(ctx, "my-oc-plugin"); err != nil {
		t.Fatal(err)
	}
	if l := Load().Plugins; len(l) != 0 {
		t.Fatalf("plugins = %+v", l)
	}
	if _, err := os.Stat(filepath.Join(Dir(), "node_modules", "my-oc-plugin")); !os.IsNotExist(err) {
		t.Fatalf("still installed: %v", err)
	}
	b, _ := os.ReadFile(log)
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if !strings.Contains(l, " --ignore-scripts ") {
			t.Errorf("bun %s: scripts run", l)
		}
	}
}
