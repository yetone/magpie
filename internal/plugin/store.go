// Package plugin runs OpenCode's provider plugins for magpie.
//
// A subscription a vendor may object to being used outside its own app is
// not built into magpie: a plugin signs in to it and makes its requests,
// the way OpenCode's plugins do (opencode-gemini-auth and the like), and
// magpie runs those same plugins. They are npm packages (or local files)
// written against OpenCode's plugin API (@opencode-ai/plugin, v1): their
// auth hook gives the sign-in methods and a loader whose fetch carries the
// requests, their config and provider hooks the models. magpie runs them
// under Bun, downloaded the first time a plugin is added, in one process
// (host.js) it talks to over stdin and stdout.
//
// What magpie keeps: plugins.json, the plugins and the config handed to
// them (OpenCode's, its provider section: a plugin's options); the npm
// packages under plugins/; plugin-auth.json, the sign-ins, in OpenCode's
// auth.json shape (so one can be moved between the two).
package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/steady"
)

// Entry is one plugin the user added.
type Entry struct {
	// Spec is how it was added: an npm package (name, name@version), a git
	// repository (github:owner/repo, git+https://…) or a path to a file
	// or folder.
	Spec string `json:"spec"`
	// Off is set on a plugin the user turned off.
	Off bool `json:"off,omitempty"`
	// Options are handed to the plugin as its second argument, as
	// OpenCode's ["name", {options}] does.
	Options map[string]any `json:"options,omitempty"`
}

// List is plugins.json.
type List struct {
	Plugins []Entry `json:"plugins"`
	// Config is OpenCode's config the plugins are handed: its provider
	// section says what a plugin reads of it
	// (provider.google.options.projectId).
	Config map[string]any `json:"config,omitempty"`
}

var listMu sync.Mutex

// Dir is the folder magpie installs npm plugins into.
func Dir() string { return filepath.Join(settings.Dir(), "plugins") }

// AuthPath is the plugins' sign-ins, OpenCode's auth.json in shape.
func AuthPath() string { return filepath.Join(settings.Dir(), "plugin-auth.json") }

func listPath() string { return filepath.Join(settings.Dir(), "plugins.json") }

// Load reads plugins.json.
func Load() List {
	var l List
	if b, err := steady.ReadFile(listPath()); err == nil {
		_ = json.Unmarshal(b, &l)
	}
	return l
}

// writeWhole writes b to p by a rename, so a magpie or the host reading
// p meanwhile reads the old file or the new one, never one half-written;
// read back with steady.ReadFile, which waits out the rename on Windows.
func writeWhole(p string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(p), filepath.Base(p)+".*")
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = steady.Rename(f.Name(), p)
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}

func save(l List) error {
	b, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(settings.Dir(), 0o700); err != nil {
		return err
	}
	if err := writeWhole(listPath(), append(b, '\n')); err != nil {
		return err
	}
	listSeen.Lock()
	listSeen.stamp, listSeen.set = listStamp(), true
	listSeen.Unlock()
	return nil
}

// listSeen is plugins.json as this magpie last wrote or read it.
var listSeen struct {
	sync.Mutex
	stamp string
	set   bool
}

func listStamp() string {
	fi, err := os.Stat(listPath())
	if err != nil {
		return ""
	}
	return fmt.Sprint(fi.ModTime().UnixNano(), fi.Size())
}

// hostStale is set when another magpie changed the plugins (magpie plugin
// add, remove or move in a terminal while the app runs): the host running
// has the old ones loaded.
var hostStale atomic.Bool

// checkList notices plugins.json changed by another magpie: the host is
// started again with the plugins as they are, and the providers the other
// magpie last saw asked for are the ones known meanwhile.
func checkList() {
	st := listStamp()
	listSeen.Lock()
	moved := listSeen.set && listSeen.stamp != st
	listSeen.stamp, listSeen.set = st, true
	listSeen.Unlock()
	if !moved {
		return
	}
	provMu.Lock()
	provCache = nil
	provMu.Unlock()
	hostStale.Store(true)
	changed()
}

// IsPath is whether spec names a file or folder rather than a package,
// as OpenCode tells them apart.
func IsPath(spec string) bool {
	return strings.HasPrefix(spec, "file://") || strings.HasPrefix(spec, ".") || filepath.IsAbs(spec) || regexp.MustCompile(`^[A-Za-z]:[\\/]`).MatchString(spec)
}

var pkgName = regexp.MustCompile(`^(@[a-z0-9][a-z0-9._~-]*/)?[a-z0-9][a-z0-9._~-]*$`)

// gitSpec is a package bun fetches from a git repository, not npm:
// github:owner/repo[#ref] (gitlab: and bitbucket: alike), a git+https://,
// git+ssh://, git+file:// or git:// URL, a GitHub, GitLab or Bitbucket
// page's URL, or owner/repo, GitHub's shorthand.
var gitSpec = regexp.MustCompile(`^(?:(?:github|gitlab|bitbucket):|git\+[a-z]+://|git://)\S+$|^https?://(?:www\.)?(?:github\.com|gitlab\.com|bitbucket\.org)/\S+$|^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9_.-]+(?:#\S+)?$`)

// IsGit is whether spec names a git repository rather than an npm package
// or a path. The package bun installs from it is named by its own
// package.json, not by spec.
func IsGit(spec string) bool { return !IsPath(spec) && gitSpec.MatchString(spec) }

// gitName is the package bun installed from the git spec: the dependency
// of plugins/package.json bun added it as (it keeps spec as given), ""
// when there is none.
func gitName(spec string) string {
	var pj struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	b, err := os.ReadFile(filepath.Join(Dir(), "package.json"))
	if err != nil || json.Unmarshal(b, &pj) != nil {
		return ""
	}
	for name, s := range pj.Dependencies {
		if s == spec {
			return name
		}
	}
	return ""
}

// Name is the package spec names, without its version: "@scope/x" of
// "@scope/x@1.2", the package installed from a git repository (the spec
// itself until it is), or the path.
func Name(spec string) string {
	if IsPath(spec) {
		return spec
	}
	if IsGit(spec) {
		if n := gitName(spec); n != "" {
			return n
		}
		return spec
	}
	at := strings.LastIndex(spec, "@")
	if at > 0 {
		return spec[:at]
	}
	return spec
}

// Target is where the plugin is to load from: the installed package's
// folder, or the path.
func Target(spec string) string {
	if IsPath(spec) {
		p := strings.TrimPrefix(spec, "file://")
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
		return p
	}
	return filepath.Join(Dir(), "node_modules", filepath.FromSlash(Name(spec)))
}

// Add installs a plugin and adds it to the list, in place of one of the
// same package. A package is installed with its scripts left unrun.
func Add(ctx context.Context, spec string) (Entry, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return Entry{}, errors.New("no plugin given")
	}
	// the package each plugin is before the install, which may take a git
	// one's name over (one repository in place of another, or of npm's)
	was := map[string]string{}
	for _, x := range Load().Plugins {
		was[x.Spec] = Name(x.Spec)
	}
	if IsPath(spec) {
		t := Target(spec)
		if _, err := os.Stat(t); err != nil {
			return Entry{}, err
		}
		spec = t
	} else if IsGit(spec) {
		if err := install(ctx, spec); err != nil {
			return Entry{}, err
		}
		if Name(spec) == spec {
			return Entry{}, fmt.Errorf("bun installed %s but plugins/package.json doesn't list it", spec)
		}
	} else {
		if !pkgName.MatchString(Name(spec)) {
			return Entry{}, fmt.Errorf("%q isn't an npm package name", spec)
		}
		if Name(spec) == spec {
			spec += "@latest"
		}
		if err := install(ctx, spec); err != nil {
			return Entry{}, err
		}
	}
	listMu.Lock()
	defer listMu.Unlock()
	l := Load()
	e := Entry{Spec: spec}
	name := Name(spec)
	if i := slices.IndexFunc(l.Plugins, func(x Entry) bool {
		n, ok := was[x.Spec]
		if !ok {
			n = Name(x.Spec)
		}
		return x.Spec == spec || n == name
	}); i >= 0 {
		e.Options = l.Plugins[i].Options
		l.Plugins[i] = e
	} else {
		l.Plugins = append(l.Plugins, e)
	}
	if err := save(l); err != nil {
		return Entry{}, err
	}
	Restart()
	return e, nil
}

// Update installs the version of each npm plugin its spec says now
// (latest, for the most part), and fetches each git one again.
func Update(ctx context.Context) error {
	var errs []error
	for _, e := range Load().Plugins {
		if IsPath(e.Spec) {
			continue
		}
		if err := reinstall(ctx, e.Spec); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", e.Spec, err))
		}
	}
	Restart()
	return errors.Join(errs...)
}

// Remove takes a plugin off the list (and out of plugins/).
func Remove(ctx context.Context, name string) error {
	listMu.Lock()
	l := Load()
	i := slices.IndexFunc(l.Plugins, func(x Entry) bool { return Name(x.Spec) == name || x.Spec == name })
	if i < 0 {
		listMu.Unlock()
		return fmt.Errorf("no plugin %q", name)
	}
	e := l.Plugins[i]
	l.Plugins = slices.Delete(l.Plugins, i, i+1)
	err := save(l)
	listMu.Unlock()
	if err != nil {
		return err
	}
	if !IsPath(e.Spec) {
		if bun, err := Bun(ctx); err == nil {
			_ = bunCommand(ctx, bun, Dir(), "remove", "--ignore-scripts", Name(e.Spec)).Run()
		}
	}
	Restart()
	return nil
}

// SetOff turns a plugin off or back on.
func SetOff(name string, off bool) error {
	listMu.Lock()
	defer listMu.Unlock()
	l := Load()
	i := slices.IndexFunc(l.Plugins, func(x Entry) bool { return Name(x.Spec) == name || x.Spec == name })
	if i < 0 {
		return fmt.Errorf("no plugin %q", name)
	}
	l.Plugins[i].Off = off
	if err := save(l); err != nil {
		return err
	}
	Restart()
	return nil
}

// SetConfig sets the OpenCode config the plugins are handed.
func SetConfig(cfg map[string]any) error {
	listMu.Lock()
	defer listMu.Unlock()
	l := Load()
	l.Config = cfg
	if err := save(l); err != nil {
		return err
	}
	Restart()
	return nil
}

// install puts an npm package into plugins/ with bun add, its scripts
// left unrun as OpenCode leaves them.
func install(ctx context.Context, spec string) error {
	bun, err := Bun(ctx)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return err
	}
	pj := filepath.Join(Dir(), "package.json")
	if _, err := os.Stat(pj); err != nil {
		if err := os.WriteFile(pj, []byte("{\n  \"name\": \"magpie-plugins\",\n  \"private\": true\n}\n"), 0o644); err != nil {
			return err
		}
	}
	out, err := bunCommand(ctx, bun, Dir(), "add", "--ignore-scripts", spec).CombinedOutput()
	if err != nil {
		return fmt.Errorf("bun add %s: %v: %s", spec, err, lastLines(string(out), 6))
	}
	return nil
}

// reinstall installs spec again: an npm one with bun add, a git one with
// bun update, which fetches its branch's (or tag's) commit now where bun
// add keeps the one bun.lock has.
func reinstall(ctx context.Context, spec string) error {
	name := Name(spec)
	if !IsGit(spec) || name == spec {
		return install(ctx, spec)
	}
	bun, err := Bun(ctx)
	if err != nil {
		return err
	}
	out, err := bunCommand(ctx, bun, Dir(), "update", "--ignore-scripts", name).CombinedOutput()
	if err != nil {
		return fmt.Errorf("bun update %s: %v: %s", name, err, lastLines(string(out), 6))
	}
	return nil
}

func lastLines(s string, n int) string {
	ls := strings.Split(strings.TrimSpace(s), "\n")
	if len(ls) > n {
		ls = ls[len(ls)-n:]
	}
	return strings.Join(ls, "\n")
}

func command(ctx context.Context, name string, args ...string) *exec.Cmd {
	return proc.CommandContext(ctx, name, args...)
}

// env is the plugins' environment: magpie's, with its proxy.
func env() []string { return netproxy.Env(os.Environ()) }

// Version is the version of the package spec installed (or checked out
// at its path), "" when there is none.
func Version(spec string) string {
	dir := Target(spec)
	if fi, err := os.Stat(dir); err == nil && !fi.IsDir() {
		dir = filepath.Dir(dir)
	}
	var pj struct {
		Version string `json:"version"`
	}
	if b, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil && json.Unmarshal(b, &pj) == nil {
		return pj.Version
	}
	return ""
}

// PackageName is the npm package spec is: its name, or, for a plugin added
// from a folder or file (a checkout of it), the name its package.json gives.
func PackageName(spec string) string {
	if !IsPath(spec) {
		return Name(spec)
	}
	dir := Target(spec)
	if fi, err := os.Stat(dir); err == nil && !fi.IsDir() {
		dir = filepath.Dir(dir)
	}
	var pj struct {
		Name string `json:"name"`
	}
	if b, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil && json.Unmarshal(b, &pj) == nil && pj.Name != "" {
		return pj.Name
	}
	return spec
}
