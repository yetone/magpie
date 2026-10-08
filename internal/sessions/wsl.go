package sessions

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Sessions in WSL. On Windows an agent installed in a WSL distro keeps its
// sessions in the distro's home, which magpie opens through
// \\wsl.localhost\<distro> — the distros internal/agent's wsl.go already
// finds and probes for the agents it sets up (WSLHomes, which that package
// sets; this one can't import it). Claude Code's, Codex's and Pi's session
// files there are read as this computer's are, each session marked with its
// distro (Session.WSL) and resumed through wsl.exe (wslResume).
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
}

// wslScan lists the session files under a distro's home.
func wslScan(home string) []file {
	var out []file
	for _, a := range wslAgents {
		dir := filepath.Join(home, a.dir)
		switch a.agent {
		case "claude":
			out = append(out, ccFiles("claude", dir)...)
		case "codex":
			out = append(out, codexFilesIn(dir)...)
		case "pi":
			out = append(out, piFilesIn(dir, "")...)
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
func wslFiles(agents ...string) []file {
	if !wslLooking() {
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
		for _, e := range l.Files {
			if want[e.Agent] {
				out = append(out, file{agent: e.Agent, key: e.Key, path: e.Path, main: e.Main, size: e.Size, mod: e.Mod, wsl: n, cold: cold})
			}
		}
	}
	return out
}

// wslList lists the distros' files: a running one's afresh, a stopped
// one's as they were. With no distros said at all (wsl.exe didn't answer)
// every listing is kept, cold.
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
		for _, f := range wslScan(h.Home) {
			l.Files = append(l.Files, wslEntry{Agent: f.agent, Key: f.key, Path: f.path, Main: f.main, Size: f.size, Mod: f.mod})
		}
		lists[h.Distro] = l
	}
	if len(homes) == 0 {
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
		if os.MkdirAll(filepath.Dir(p), 0o755) == nil {
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
		if f.wsl != "" {
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
