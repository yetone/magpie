package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/lastgood"
	"github.com/yetone/magpie/internal/lazyre"
)

// A leftover is a dependency of plugins/package.json that no plugin on the
// list is installed as. Bun keeps every dependency there and installs each
// one again at every bun add, update and remove, so one that can't be
// installed fails all of them, whatever plugin they are for
// (CHENGJIAHEGOXIS on X: agency-agents, a repository of subagents with no
// package.json, added from Plugins and never a plugin, failed every
// install after it with "EBUSY: failed copying files from cache to
// destination for package agency-agents" on Windows, a file of its copy
// in node_modules held open). It got there by a bun add that failed: Bun
// writes the dependency into package.json before it installs, and exits 1
// with it there. Nothing listed it, so nothing could take it out.

// Leftover is a dependency of plugins/ no plugin is installed as, as the
// Plugins page lists it with a Remove.
type Leftover struct {
	Name string `json:"name"`
	// Spec is what package.json has it at
	Spec string `json:"spec"`
	// Version is the one in node_modules, "" when none is there (or
	// none that says, as a repository without a package.json)
	Version string `json:"version,omitempty"`
	// Missing is whether node_modules has no copy of it at all
	Missing bool `json:"missing,omitempty"`
}

// adding are the specs an add is installing, not on the list yet: what a
// concurrent install would otherwise take for a leftover.
var (
	addingMu sync.Mutex
	adding   = map[string]int{}
)

// beingAdded marks spec as being installed for the list until done is
// called.
func beingAdded(spec string) (done func()) {
	addingMu.Lock()
	adding[spec]++
	addingMu.Unlock()
	return func() {
		addingMu.Lock()
		if adding[spec]--; adding[spec] <= 0 {
			delete(adding, spec)
		}
		addingMu.Unlock()
	}
}

// pkgDeps is plugins/package.json's dependencies; an error when it can't
// be read or parsed (then nothing is a leftover: unknown, not none).
func pkgDeps() (map[string]string, error) {
	b, err := os.ReadFile(filepath.Join(Dir(), "package.json"))
	if err != nil {
		return nil, err
	}
	var pj struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	if err := json.Unmarshal(b, &pj); err != nil {
		return nil, err
	}
	return pj.Dependencies, nil
}

// needed is which of deps a plugin is installed as: one on the list (on
// or off), one being added, and pi's agent while a pi plugin is listed
// (ensurePi installs it beside them). also are more specs to count. ok is
// false when plugins.json is there and can't be read: every dependency
// may be a plugin's then, and none is taken for a leftover.
func needed(deps map[string]string, also ...string) (need map[string]bool, ok bool) {
	if _, err := lastgood.Read(listPath(), lastgood.JSON); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, false
	}
	specs := slices.Clone(also)
	pi := false
	for _, e := range Load().Plugins {
		if IsPath(e.Spec) {
			pi = pi || IsPi(Target(e.Spec))
			continue
		}
		specs = append(specs, e.Spec)
	}
	addingMu.Lock()
	for s := range adding {
		specs = append(specs, s)
	}
	addingMu.Unlock()
	need = map[string]bool{}
	for _, s := range specs {
		if IsPath(s) {
			continue
		}
		for n, v := range deps {
			if v == s {
				need[n] = true
			}
		}
		if !IsGit(s) {
			need[Name(s)] = true
		}
	}
	for n := range deps {
		if need[n] && !pi && n != piAgent {
			pi = IsPi(filepath.Join(Dir(), "node_modules", filepath.FromSlash(n)))
		}
	}
	if pi {
		need[piAgent] = true
	}
	return need, true
}

// Leftovers are plugins/'s dependencies no plugin is installed as, by
// name.
func Leftovers() []Leftover {
	deps, err := pkgDeps()
	if err != nil || len(deps) == 0 {
		return nil
	}
	need, ok := needed(deps)
	if !ok {
		return nil
	}
	var out []Leftover
	for n, s := range deps {
		if need[n] {
			continue
		}
		l := Leftover{Name: n, Spec: s}
		at := filepath.Join(Dir(), "node_modules", filepath.FromSlash(n))
		if _, err := os.Stat(at); err != nil {
			l.Missing = true
		} else if v, known := installedVersion(n); known {
			l.Version = v
		}
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// RemoveLeftover takes the dependency name out of plugins/, refused
// while a plugin is installed as it: bun remove, and when that fails
// (it installs every other dependency again, and one of those can fail it
// as well) the dependency is taken out of package.json by hand, with its
// folder.
func RemoveLeftover(ctx context.Context, name string) error {
	if !slices.ContainsFunc(Leftovers(), func(l Leftover) bool { return l.Name == name }) {
		return fmt.Errorf("%s isn't left in plugins/ without a plugin", name)
	}
	bun, err := Bun(ctx)
	if err != nil {
		installMu.Lock()
		defer installMu.Unlock()
		return dropDeps(name)
	}
	return removeDep(ctx, bun, name)
}

// removeDep takes the dependency name out of plugins/ with bun remove,
// and by hand when bun fails: a dependency left in package.json is
// installed again at every bun command after.
func removeDep(ctx context.Context, bun, name string) error {
	out, err := runBun(ctx, bun, "remove", "--ignore-scripts", name)
	deps, derr := pkgDeps()
	if err == nil && derr == nil {
		if _, still := deps[name]; !still {
			return nil
		}
	}
	if err != nil {
		log.Printf("plugins: bun remove %s: %v: %s; taken out of package.json instead", name, err, lastLines(string(out), 3))
	}
	installMu.Lock()
	defer installMu.Unlock()
	return dropDeps(name)
}

// dropDeps takes names out of plugins/package.json's dependencies and
// their folders out of node_modules. Bun's lock follows package.json at
// its next command. A folder that can't be removed (a file in it held
// open on Windows) stays; with no dependency naming it Bun never copies
// it again. installMu held.
func dropDeps(names ...string) error {
	pj := filepath.Join(Dir(), "package.json")
	b, err := os.ReadFile(pj)
	if err != nil {
		return err
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(b, &obj); err != nil {
		return err
	}
	var deps map[string]json.RawMessage
	if raw, ok := obj["dependencies"]; ok {
		if err := json.Unmarshal(raw, &deps); err != nil {
			return err
		}
	}
	changed := false
	for _, n := range names {
		if _, ok := deps[n]; ok {
			delete(deps, n)
			changed = true
		}
	}
	if changed {
		raw, err := json.Marshal(deps)
		if err != nil {
			return err
		}
		obj["dependencies"] = raw
		out, err := json.MarshalIndent(obj, "", "  ")
		if err != nil {
			return err
		}
		if err := edit.WriteAtomic(pj, append(out, '\n')); err != nil {
			return err
		}
	}
	var errs []error
	for _, n := range names {
		if n == "" || strings.Contains(n, "..") {
			continue
		}
		if err := os.RemoveAll(filepath.Join(Dir(), "node_modules", filepath.FromSlash(n))); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		log.Printf("plugins: %s out of package.json; its folder stays: %v", strings.Join(names, ", "), errors.Join(errs...))
	}
	return nil
}

// bunFailedPkg is a package Bun says it couldn't install: "EBUSY: failed
// copying files from cache to destination for package <name>", "EACCES:
// Permission denied while installing <name>".
var bunFailedPkg = lazyre.New(`(?m)(?:failed [a-z /]+ for package|while installing) (@?[A-Za-z0-9._~-]+(?:/[A-Za-z0-9._~-]+)?)\s*$`)

var ansi = lazyre.New(`\x1b\[[0-9;]*m|</?[a-z]>`)

// bunFailed is the packages out says Bun couldn't install.
func bunFailed(out string) []string {
	out = ansi.ReplaceAllString(out, "")
	var names []string
	for _, m := range bunFailedPkg.FindAllStringSubmatch(out, -1) {
		if n := strings.TrimRight(m[1], "."); !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	return names
}

// bunRunError is a bun command's failure, with all it said.
type bunRunError struct {
	err error
	out string
}

func (e *bunRunError) Error() string { return e.err.Error() }
func (e *bunRunError) Unwrap() error { return e.err }

// failedOf is the packages err, a bun command's failure, says Bun
// couldn't install.
func failedOf(err error) []string {
	var be *bunRunError
	if errors.As(err, &be) {
		return bunFailed(be.out)
	}
	return nil
}

// depsState is plugins/package.json's dependencies and bun.lock before
// a bun add, to put back what the add wrote when it failed.
type depsState struct {
	deps map[string]string
	ok   bool
	lock []byte
	// hadLock is whether bun.lock was there
	hadLock bool
}

func depsNow() depsState {
	s := depsState{}
	var err error
	s.deps, err = pkgDeps()
	s.ok = err == nil
	if b, err := os.ReadFile(filepath.Join(Dir(), "bun.lock")); err == nil {
		s.lock, s.hadLock = b, true
	}
	return s
}

// undo puts back what a failed bun add wrote: a dependency it added is
// taken out with its folder, one it changed goes back to the spec it had,
// and bun.lock to what it was. One known to be broken (gone) since stays
// gone. installMu held.
func (s depsState) undo(gone []string) {
	if !s.ok {
		return
	}
	now, err := pkgDeps()
	if err != nil {
		return
	}
	var added []string
	back := map[string]string{}
	for n, v := range now {
		was, had := s.deps[n]
		switch {
		case !had:
			added = append(added, n)
		case was != v && !slices.Contains(gone, n):
			back[n] = was
		}
	}
	if len(added) > 0 {
		log.Printf("plugins: a failed install left %s in package.json; taken out", strings.Join(added, ", "))
		_ = dropDeps(added...)
	}
	if len(back) > 0 {
		_ = setDeps(back)
	}
	if s.hadLock {
		_ = edit.WriteAtomic(filepath.Join(Dir(), "bun.lock"), s.lock)
	}
}

// setDeps sets dependencies of plugins/package.json to the specs given.
// installMu held.
func setDeps(to map[string]string) error {
	pj := filepath.Join(Dir(), "package.json")
	b, err := os.ReadFile(pj)
	if err != nil {
		return err
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(b, &obj); err != nil {
		return err
	}
	deps := map[string]json.RawMessage{}
	if raw, ok := obj["dependencies"]; ok {
		if err := json.Unmarshal(raw, &deps); err != nil {
			return err
		}
	}
	for n, v := range to {
		deps[n], _ = json.Marshal(v)
	}
	raw, err := json.Marshal(deps)
	if err != nil {
		return err
	}
	obj["dependencies"] = raw
	out, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		return err
	}
	return edit.WriteAtomic(pj, append(out, '\n'))
}

// dropBroken takes out of plugins/ the dependencies err says Bun couldn't
// install that no plugin is installed as (also: specs being installed
// now), and says which: each would fail every bun command after. installMu
// held.
func dropBroken(err error, also ...string) []string {
	failed := failedOf(err)
	if len(failed) == 0 {
		return nil
	}
	deps, derr := pkgDeps()
	if derr != nil {
		return nil
	}
	need, ok := needed(deps, also...)
	if !ok {
		return nil
	}
	var gone []string
	for _, n := range failed {
		if _, in := deps[n]; in && !need[n] {
			gone = append(gone, n)
		}
	}
	if len(gone) == 0 {
		return nil
	}
	log.Printf("plugins: Bun couldn't install %s, which no plugin is installed as; taken out of plugins/", strings.Join(gone, ", "))
	if dropDeps(gone...) != nil {
		return nil
	}
	return gone
}

// othersFailed is whether err, a failed bun add of spec, is only another
// package's failure, spec's own package installed: the add of it is done,
// and the plugin Bun couldn't install says so in its own row.
func othersFailed(err error, spec string) bool {
	failed := failedOf(err)
	if len(failed) == 0 {
		return false
	}
	name := Name(spec)
	if name == spec && IsGit(spec) || slices.Contains(failed, name) {
		return false
	}
	if deps, derr := pkgDeps(); derr != nil || deps[name] == "" {
		return false
	}
	v, known := installedVersion(name)
	return known && v != ""
}
