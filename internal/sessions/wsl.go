package sessions

import (
	"bytes"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/lastgood"
)

// Sessions in WSL. On Windows an agent installed in a WSL distro keeps its
// sessions in the distro's home, which magpie opens through
// \\wsl.localhost\<distro> — the distros internal/agent's wsl.go already
// finds and probes for the agents it sets up (WSLHomes, which that package
// sets; this one can't import it). Claude Code's, Codex's, Pi's and omp's
// session files there are read as this computer's are, each session marked
// with its distro (Session.WSL) and resumed through wsl.exe (wslResume).
// So are agy's, and the Antigravity app's when it is connected to the
// distro: its language server runs there and keeps the conversations in
// the distro's ~/.gemini/antigravity (@guyng35955 on X).
//
// Reading over \\wsl.localhost is slow, and looking at a stopped distro's
// files starts it. So the distros' files are listed behind, at most every
// wslRelist, and the listing is kept on disk (sessions-wsl.json beside the
// parses): a listing reads what was last found at once, a stopped
// distro's files stay listed as they were found and are never opened
// (file.cold), and their parses, kept by path as every file's are, stand.

// WSLHome is a WSL distro magpie found agents in: its name, its user's home
// as magpie opens it (\\wsl.localhost\Ubuntu\home\me), and whether it runs.
type WSLHome struct {
	Distro  string
	Home    string
	Running bool
	// Open maps a path under Home to where it can be read from Windows:
	// a folder that is a link in the distro (~/.codex → /mnt/c/…) can't
	// be followed through \\wsl.localhost (#4502), so what is below it is
	// read where the link leads. nil reads every path as it is.
	Open func(string) string
}

// WSLHomes are the WSL distros to read sessions in; nil (none) unless
// internal/agent sets it, which it does where there is WSL.
var WSLHomes func() []WSLHome

// WSLRunning is whether a distro runs now, as wsl.exe said a few seconds
// ago at most; internal/agent sets it. A listing's Running is as old as
// the listing (up to wslRelist and more): a distro the user stopped since
// (wsl --shutdown, to repair WSL) would be started again by reading its
// files, so a file is read only while this says it runs.
var WSLRunning func(distro string) bool

// WSLOff is whether Settings' Detect agents in WSL is off (#1264); set by
// internal/agent. Off, no distro's sessions are listed, not even those
// listed before.
var WSLOff func() bool

// WSLListed is whether wsl.exe answered the last list of distros, so that
// WSLHomes naming none means "no distro has an agent" (the user took the
// last one away), not "wsl.exe didn't answer"; internal/agent sets it. Nil
// is not known.
var WSLListed func() bool

// wslLooking is whether the distros' sessions are listed.
func wslLooking() bool { return WSLHomes != nil && (WSLOff == nil || !WSLOff()) }

// wslUp is WSLRunning's answer; true when nothing set it (the listing
// alone decides).
func wslUp(distro string) bool { return WSLRunning == nil || WSLRunning(distro) }

// wslAgents are the agents whose sessions are read in a distro, and the
// folder under its home each keeps them in (the default: an agent's own
// variables there can't be read from Windows).
var wslAgents = []struct{ agent, dir string }{
	{"claude", ".claude"},
	{"codex", ".codex"},
	{"pi", filepath.Join(".pi", "agent")},
	{"raft", ".slock"},
	{"omp", filepath.Join(".omp", "agent")},
	{"kimi", ".kimi-code"},
	{"antigravity", filepath.Join(".gemini", "antigravity")},
	{"antigravity", filepath.Join(".gemini", "antigravity-insiders")},
	{"agy", filepath.Join(".gemini", "antigravity-cli")},
}

// wslScan lists the session files under a distro's home.
func wslScan(h WSLHome) []file {
	var out []file
	for _, a := range wslAgents {
		dir := filepath.Join(h.Home, a.dir)
		if h.Open != nil {
			dir = h.Open(dir)
		}
		switch a.agent {
		case "claude":
			out = append(out, ccFiles("claude", dir)...)
		case "codex":
			out = append(out, codexFilesIn(dir)...)
		case "pi":
			out = append(out, piFilesIn(dir, "")...)
		case "raft":
			out = append(out, raftFilesIn(dir)...)
		case "omp":
			out = append(out, ompFilesIn(dir)...)
		case "kimi":
			out = append(out, kimiFilesIn(filepath.Join(dir, "sessions"))...)
		case "antigravity":
			// the rows name the distro's own folders (file:///home/…)
			conv := filepath.Join(dir, "conversations")
			if filepath.Base(a.dir) == "antigravity-insiders" {
				if !sameDir(dir, AntigravityInsidersDir()) {
					out = append(out, antigravityInsiders(conv, "linux")...)
				}
			} else if !sameDir(dir, AntigravityAppDir()) {
				out = append(out, antigravityFilesIn(conv, "linux")...)
			}
		case "agy":
			if !sameDir(dir, AgyDir()) {
				out = append(out, agyFilesIn(dir, "linux")...)
			}
		}
	}
	return out
}

// wslEntry is a file of a distro's listing, as kept on disk.
type wslEntry struct {
	Agent string    `json:"agent"`
	Key   string    `json:"key"`
	Path  string    `json:"path"`
	Main  bool      `json:"main,omitempty"`
	Size  int64     `json:"size"`
	Mod   time.Time `json:"mod"`
	// ReadOnly: the Antigravity app's conversations, which magpie lists
	// and never moves or deletes
	ReadOnly bool `json:"readOnly,omitempty"`
	// Agy and Rev: an agy or Antigravity conversation's summary row and
	// what tells it changed (file.more's agy and rev)
	Agy *agySummary `json:"agy,omitempty"`
	Rev string      `json:"rev,omitempty"`
	// Calls is an agy transcript's size and time as a call source, its
	// conversation's database taken in (agyCallStat); nil for a file that
	// isn't one (a conversation listed from its row alone)
	Calls *wslStat `json:"calls,omitempty"`
}

// wslStat is a file's size and time.
type wslStat struct {
	Size int64     `json:"size"`
	Mod  time.Time `json:"mod"`
}

// wslEntryOf is f as a listing keeps it.
func wslEntryOf(f file) wslEntry {
	e := wslEntry{Agent: f.agent, Key: f.key, Path: f.path, Main: f.main, Size: f.size, Mod: f.mod.Time(), ReadOnly: f.readOnly, Agy: f.m().agy, Rev: f.m().rev}
	if f.agent == "agy" && !agyRowOnly(f.path) {
		c := file{path: f.path, size: f.size, mod: f.mod}
		agyCallStat(&c)
		e.Calls = &wslStat{Size: c.size, Mod: c.mod.Time()}
	}
	return e
}

// wslListing is a distro's files as last listed.
type wslListing struct {
	Home    string     `json:"home"`
	Files   []wslEntry `json:"files"`
	Running bool       `json:"-"` // as the last listing found it; false until one has
}

var wslSess struct {
	sync.Mutex
	gen    uint64 // Reset's: a listing started before one is dropped
	loaded bool
	lists  map[string]*wslListing // by distro
	at     time.Time              // when the last listing started
	done   chan struct{}          // the listing under way, nil when none
	kept   []byte                 // what sessions-wsl.json holds
}

var (
	// wslRelist is how often the distros' files are listed again
	wslRelist = 30 * time.Second
	// wslFirstWait is how long a read waits for the very first listing,
	// when nothing was kept of one; after it the read goes on without
	wslFirstWait = 3 * time.Second
)

func wslListPath() string { return filepath.Join(filepath.Dir(CachePath()), "sessions-wsl.json") }

// wslFiles are the session files of the given agents in the WSL distros,
// as last listed; a listing is started behind when the last is older than
// wslRelist.
func wslFiles(agents ...string) []file { return wslFilesAs(false, agents...) }

// wslCallFiles are wslFiles as call sources: an agy transcript with its
// conversation's database taken into its size and time, as agyCallFiles
// has it, and none of agy's conversations listed from their row alone.
func wslCallFiles(agents ...string) []file { return wslFilesAs(true, agents...) }

func wslFilesAs(calls bool, agents ...string) []file {
	agents = slices.DeleteFunc(slices.Clone(agents), func(a string) bool { return !Reads(a) })
	if len(agents) == 0 || !wslLooking() {
		return nil
	}
	wslSess.Lock()
	if !wslSess.loaded {
		wslSess.loaded = true
		wslSess.lists = map[string]*wslListing{}
		if b, err := os.ReadFile(wslListPath()); err == nil && json.Unmarshal(b, &wslSess.lists) == nil {
			wslSess.kept = b
		}
		for n, l := range wslSess.lists {
			if l == nil {
				delete(wslSess.lists, n)
			}
		}
	}
	if wslSess.done == nil && time.Since(wslSess.at) > wslRelist {
		done := make(chan struct{})
		wslSess.done, wslSess.at = done, time.Now()
		go wslList(wslSess.gen, done)
	}
	wait := wslSess.done
	if len(wslSess.lists) > 0 {
		wait = nil // what was found before is read now; the new listing comes next time
	}
	wslSess.Unlock()
	if wait != nil {
		select {
		case <-wait:
		case <-time.After(wslFirstWait):
		}
	}
	want := map[string]bool{}
	for _, a := range agents {
		want[a] = true
	}
	// which run is asked outside the lock: wsl.exe may take a while
	wslSess.Lock()
	up := map[string]bool{}
	for n := range wslSess.lists {
		up[n] = false
	}
	wslSess.Unlock()
	for n := range up {
		up[n] = wslUp(n)
	}
	wslSess.Lock()
	defer wslSess.Unlock()
	names := make([]string, 0, len(wslSess.lists))
	for n := range wslSess.lists {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []file
	for _, n := range names {
		l := wslSess.lists[n]
		cold := !l.Running || !up[n]
		in := &fileMore{wsl: n} // the distro's files share it
		for _, e := range l.Files {
			if !want[e.Agent] {
				continue
			}
			f := file{agent: e.Agent, key: e.Key, path: e.Path, main: e.Main, size: e.Size, mod: modOf(e.Mod), more: in, cold: cold, readOnly: e.ReadOnly}
			if calls && e.Agent == "agy" {
				if e.Calls == nil {
					continue
				}
				f.size, f.mod = e.Calls.Size, modOf(e.Calls.Mod)
			}
			if e.Agy != nil || e.Rev != "" {
				m := f.w()
				m.agy, m.rev = e.Agy, e.Rev
			}
			out = append(out, f)
		}
	}
	return out
}

// wslList lists the distros' files: a running one's afresh, a stopped
// one's as they were. With no distros said at all (wsl.exe didn't answer)
// every listing is kept, cold. A wsl.exe that answered with no distro an
// agent is in (WSLListed) lets them all go.
func wslList(gen uint64, done chan struct{}) {
	defer close(done)
	homes := WSLHomes()
	wslSess.Lock()
	old := wslSess.lists
	wslSess.Unlock()
	lists := map[string]*wslListing{}
	for _, h := range homes {
		if h.Distro == "" || h.Home == "" {
			continue
		}
		if !h.Running || !wslUp(h.Distro) {
			if o := old[h.Distro]; o != nil && o.Home == h.Home {
				c := *o
				c.Running = false
				lists[h.Distro] = &c
			}
			continue
		}
		l := &wslListing{Home: h.Home, Running: true, Files: []wslEntry{}}
		for _, f := range wslScan(h) {
			l.Files = append(l.Files, wslEntryOf(f))
		}
		lists[h.Distro] = l
	}
	// with no distros said and no answer from wsl.exe every listing is
	// kept; one that answered and found no agent anywhere has none left to
	// list, and the rows of files that are gone with them
	if len(homes) == 0 && (WSLListed == nil || !WSLListed()) {
		for n, o := range old {
			c := *o
			c.Running = false
			lists[n] = &c
		}
	}
	b, _ := json.Marshal(lists)
	wslSess.Lock()
	defer wslSess.Unlock()
	if gen != wslSess.gen {
		return
	}
	wslSess.lists = lists
	if wslSess.done == done {
		wslSess.done = nil
	}
	if b != nil && !bytes.Equal(b, wslSess.kept) {
		p := wslListPath()
		// wslFiles reads a file that doesn't parse as no listing: it is
		// copied aside before this one goes over it, and one that can't be
		// copied isn't written over (#2263)
		if err := lastgood.KeepUnparsed(p, lastgood.Into[map[string]*wslListing]); err != nil {
			log.Printf("sessions-wsl.json wasn't written over: %v", err)
		} else if os.MkdirAll(filepath.Dir(p), 0o755) == nil {
			tmp := p + ".tmp"
			if os.WriteFile(tmp, b, 0o644) == nil && os.Rename(tmp, p) == nil {
				wslSess.kept = b
			} else {
				os.Remove(tmp)
			}
		}
	}
}

// wslCold reports whether path is in a WSL distro's home that isn't
// running (or not yet known to run): such a file is not opened.
func wslCold(path string) bool {
	if WSLHomes == nil {
		return false
	}
	wslSess.Lock()
	var in string
	for n, l := range wslSess.lists {
		if under(path, l.Home) {
			if !l.Running {
				wslSess.Unlock()
				return true
			}
			in = n
		}
	}
	wslSess.Unlock()
	return in != "" && !wslUp(in)
}

// under reports whether path is dir or in it, its case aside (Windows').
func under(path, dir string) bool {
	if len(path) < len(dir) || !strings.EqualFold(path[:len(dir)], dir) {
		return false
	}
	return len(path) == len(dir) || os.IsPathSeparator(path[len(dir)]) || os.IsPathSeparator(dir[len(dir)-1])
}

// wslDirs are the agents' folders in the distros sessions were found in.
func wslDirs() []string {
	if !wslLooking() {
		return nil
	}
	wslSess.Lock()
	defer wslSess.Unlock()
	var out []string
	seen := map[string]bool{}
	for _, l := range wslSess.lists {
		for _, e := range l.Files {
			for _, a := range wslAgents {
				if d := filepath.Join(l.Home, a.dir); a.agent == e.Agent && !seen[d] {
					seen[d] = true
					out = append(out, d)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// wslHomeOf is the home of a listed distro, "" when it isn't.
func wslHomeOf(distro string) string {
	wslSess.Lock()
	defer wslSess.Unlock()
	if l := wslSess.lists[distro]; l != nil {
		return l.Home
	}
	return ""
}

// wslForget drops a deleted session's files from their distro's listing,
// and has the next read list the distros again (which keeps that on disk).
func wslForget(fs []file) {
	gone := map[string]bool{}
	for _, f := range fs {
		if f.m().wsl != "" {
			gone[f.path] = true
		}
	}
	if len(gone) == 0 {
		return
	}
	wslSess.Lock()
	defer wslSess.Unlock()
	for _, l := range wslSess.lists {
		var kept []wslEntry
		for _, e := range l.Files {
			if !gone[e.Path] {
				kept = append(kept, e)
			}
		}
		l.Files = kept
	}
	wslSess.at = time.Time{}
}

// wslRelistNow lists the distros again when a restored session's files
// went back into one, after any listing under way (which may have missed
// them), waiting for it as a first listing is waited for: the session is
// listed when the page asks next.
func wslRelistNow(items []moved) {
	if WSLHomes == nil {
		return
	}
	wslSess.Lock()
	in := false
	for _, l := range wslSess.lists {
		for _, m := range items {
			in = in || under(m.From, l.Home)
		}
	}
	busy := wslSess.done
	wslSess.Unlock()
	if !in {
		return
	}
	if busy != nil {
		<-busy
	}
	done := make(chan struct{})
	wslSess.Lock()
	wslSess.done, wslSess.at = done, time.Now()
	go wslList(wslSess.gen, done)
	wslSess.Unlock()
	select {
	case <-done:
	case <-time.After(wslFirstWait):
	}
}

// wslReset forgets the listings, in memory.
func wslReset() {
	wslSess.Lock()
	defer wslSess.Unlock()
	wslSess.gen++
	wslSess.loaded, wslSess.lists, wslSess.at, wslSess.done, wslSess.kept = false, nil, time.Time{}, nil, nil
}

// wslResume is the PowerShell line that resumes a session in a WSL distro
// (run is the agent's own command): wsl.exe starts the user's own shell
// there as a login and interactive one, so the agent is on the PATH it has
// in a terminal there (~/.local/bin, nvm's), in the session's folder.
// Nothing in it is in double quotes, which Windows PowerShell 5.1 passes
// to wsl.exe mangled.
//
//	wsl.exe -d 'Ubuntu' --cd '/home/me/app' -e sh -lc 'exec ${SHELL:-sh} -lic ''claude --resume <id>'''
func wslResume(distro, cwd, run string) string {
	ps := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	line := "wsl.exe -d " + ps(distro)
	if strings.HasPrefix(cwd, "/") {
		line += " --cd " + ps(cwd)
	}
	return line + " -e sh -lc " + ps("exec ${SHELL:-sh} -lic '"+run+"'")
}
