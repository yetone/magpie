package claudecode

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/proc"
)

// own is what Own reads of the machine; tests stand in for it.
var own = struct {
	first   func() string       // the one a terminal runs (proc.FindTool)
	all     func() []string     // every one (proc.FindTools)
	read    func(string) string // the version where it is installed, run nothing
	version func(string) string // what --version says (proc.Version)
}{
	first:   func() string { return proc.FindTool("claude") },
	all:     func() []string { return proc.FindTools("claude") },
	read:    installedVersion,
	version: proc.Version,
}

// Own is this machine's own Claude Code, the newest of them when it has
// more than one: a machine can have an old one first on PATH and a newer
// one after it, two npm installs in two Node versions, of which `claude
// update` updated the second while `claude` still ran the first
// (zi_xiang5806 on X: magpie's sign-in ran 2.1.38, which has no
// --claudeai). One is passed over only for one whose version is known and
// newer: a version that can't be read is unknown, not old, and the first
// stays when no other is known to be newer. "" when there is none.
func Own() string {
	first := own.first()
	all := own.all()
	if len(all) == 0 || len(all) == 1 && (first == "" || all[0] == first) {
		return first
	}
	cands := make([]string, 0, len(all)+1)
	if first != "" {
		cands = append(cands, first)
	}
	for _, p := range all {
		if p != first {
			cands = append(cands, p)
		}
	}
	// what each is, read where it is installed; one is asked its version
	// only when another's is known: with none known there is nothing to
	// compare it with, and a program that isn't Claude Code (a test's
	// stand-in, a launcher of the user's) is not run for nothing
	vers := make([]string, len(cands))
	known := false
	for i, p := range cands {
		if vers[i] = semverOf(own.read(p)); vers[i] != "" {
			known = true
		}
	}
	if !known {
		return cands[0]
	}
	var wg sync.WaitGroup
	for i, p := range cands {
		if vers[i] != "" {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			vers[i] = VersionOf(p)
		}()
	}
	wg.Wait()
	best, bv := cands[0], vers[0]
	if bv == "" {
		// the first's version is unknown: it stays
		return best
	}
	for i, v := range vers[1:] {
		if v != "" && newer(v, bv) {
			best, bv = cands[i+1], v
		}
	}
	return best
}

// semver is a version in what `claude --version` says ("2.1.38 (Claude
// Code)").
var semver = lazyre.New(`\b(\d+\.\d+\.\d+)\b`)

func semverOf(s string) string {
	if m := semver.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}

// installedVersion is the version of the Claude Code at path as where it
// is installed says, running nothing: an npm install's package.json
// (proc.PackageVersion), the native installer's
// ~/.local/share/claude/versions/<version>, or Homebrew's
// Caskroom/claude-code/<version>/. "" when it says none.
func installedVersion(path string) string {
	if v := proc.PackageVersion(path); v != "" {
		return v
	}
	real, err := proc.RealPath(path)
	if err != nil {
		return ""
	}
	dir, name := filepath.Dir(real), filepath.Base(real)
	if filepath.Base(dir) == "versions" && versionRe.MatchString(name) {
		return name
	}
	if parts := strings.Split(filepath.ToSlash(real), "/"); len(parts) >= 3 {
		for i := 0; i+2 < len(parts); i++ {
			if parts[i] == "Caskroom" && parts[i+1] == "claude-code" && versionRe.MatchString(parts[i+2]) {
				return parts[i+2]
			}
		}
	}
	return ""
}

// VersionOf is the version of the Claude Code at path, as `claude
// --version` says it (proc.Version, which reads an npm install's
// package.json rather than run it), "" when it can't be read. Own asks it
// only of one whose version isn't read where it is installed. It is kept
// while the binary is the same file, so asking again costs a stat; one
// that couldn't be read is asked again after a minute.
func VersionOf(path string) string {
	if path == "" {
		return ""
	}
	st, err := os.Stat(path)
	if err != nil {
		return ""
	}
	real := path
	if r, err := proc.RealPath(path); err == nil {
		real = r
		if rs, err := os.Stat(r); err == nil {
			st = rs
		}
	}
	stamp := fmt.Sprint(real, st.Size(), st.ModTime().UnixNano())
	versions.Lock()
	e, ok := versions.m[path]
	versions.Unlock()
	if ok && e.stamp == stamp && (e.v != "" || time.Since(e.at) < time.Minute) {
		return e.v
	}
	v := semverOf(own.version(path))
	versions.Lock()
	versions.m[path] = versionEntry{stamp: stamp, v: v, at: time.Now()}
	versions.Unlock()
	return v
}

type versionEntry struct {
	stamp, v string
	at       time.Time
}

var versions = struct {
	sync.Mutex
	m map[string]versionEntry
}{m: map[string]versionEntry{}}
