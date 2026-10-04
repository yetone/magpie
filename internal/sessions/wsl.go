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
	if WSLHomes == nil {
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
		for _, e := range l.Files {
			if want[e.Agent] {
				out = append(out, file{agent: e.Agent, key: e.Key, path: e.Path, main: e.Main, size: e.Size, mod: e.Mod, wsl: n, cold: !l.Running})
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
		if !h.Running {
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
	defer wslSess.Unlock()
	for _, l := range wslSess.lists {
		if !l.Running && under(path, l.Home) {
			return true
		}
	}
	return false
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
	if WSLHomes == nil {
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
