package library

// A server run with `npx -y pkg` is given to an agent as node running the
// package's own program, from a folder magpie installs the package in.
// npx stays running as the server's parent for as long as the server
// does: on Windows a cmd.exe, npx's node (76 MB) and a second cmd.exe
// beside a 51 MB server, ~92 MB of wrapper a server for each instance an
// agent starts; Codex starts one a thread, 200 of them in #3885. Until
// the package is installed, and for any npx line not read here, the
// agent is given npx as the library has it; the library itself, and a
// server's check, keep npx.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/settings"
)

// npxRun is an `npx -y <spec> args…` line: the package and its arguments.
type npxRun struct {
	spec, name string
	args       []string
}

// npmSpec is a package from the registry, with a version or tag or none:
// npx's other specs (a path, a git or tarball URL, an alias) are left to
// npx.
var npmSpec = lazyre.New(`^((?:@[a-z0-9][a-z0-9._~-]*/)?[a-z0-9][a-z0-9._~-]*)(?:@([A-Za-z0-9._~^<>=*| -]+))?$`)

// readNpx is s's npx line, ok false for anything else: a flag but -y
// before the package (-p, --package, -c, a registry), or no -y, which
// would have npx ask on the server's own stdin.
func readNpx(s *Server) (npxRun, bool) {
	if s.Remote() || progName(s.Command) != "npx" {
		return npxRun{}, false
	}
	yes := false
	for i, a := range s.Args {
		switch {
		case a == "-y" || a == "--yes":
			yes = true
		case strings.HasPrefix(a, "-"):
			return npxRun{}, false
		default:
			m := npmSpec.FindStringSubmatch(a)
			if !yes || m == nil {
				return npxRun{}, false
			}
			return npxRun{spec: a, name: m[1], args: s.Args[i+1:]}, true
		}
	}
	return npxRun{}, false
}

// pinned says the spec names an exact version, which never changes: one
// with none, or a tag or range, is installed again after a day, as npx
// would fetch the newer one.
func (r npxRun) pinned() bool {
	_, v, ok := strings.Cut(strings.TrimPrefix(r.spec, "@"), "@")
	return ok && exactVersion.MatchString(v)
}

var exactVersion = lazyre.New(`^\d+\.\d+\.\d+([-+].*)?$`)

// npxDir is where magpie installs a spec: a folder of its own each.
func npxDir(spec string) string {
	h := sha256.Sum256([]byte(spec))
	return filepath.Join(settings.Dir(), "mcp-npm", hex.EncodeToString(h[:8]))
}

// nodeBeside is the node to run the package with: node by name for npx
// by name, as the agent finds both on its PATH, or the node in npx's own
// folder; "" when that folder has none.
func nodeBeside(npx string) string {
	if !strings.ContainsAny(npx, `/\`) {
		return "node"
	}
	node := filepath.Join(filepath.Dir(npx), "node")
	if runtime.GOOS == "windows" {
		node += ".exe"
	}
	if st, err := os.Stat(node); err != nil || st.IsDir() {
		return ""
	}
	return node
}

// npxBin is the program npx would run of the package installed in dir:
// its one bin, or the one named as the package; "" when there is none,
// or it isn't a script node runs.
func npxBin(dir, name string) string {
	pkg := filepath.Join(dir, "node_modules", filepath.FromSlash(name))
	b, err := os.ReadFile(filepath.Join(pkg, "package.json"))
	if err != nil {
		return ""
	}
	var m struct {
		Bin json.RawMessage `json:"bin"`
	}
	if json.Unmarshal(b, &m) != nil {
		return ""
	}
	var rel, one string
	var bins map[string]string
	switch {
	case json.Unmarshal(m.Bin, &one) == nil:
		rel = one
	case json.Unmarshal(m.Bin, &bins) == nil:
		if len(bins) == 1 {
			for _, v := range bins {
				rel = v
			}
		} else {
			rel = bins[name[strings.LastIndexByte(name, '/')+1:]]
		}
	}
	if rel == "" {
		return ""
	}
	bin := filepath.Join(pkg, filepath.FromSlash(rel))
	if rel, err := filepath.Rel(pkg, bin); err != nil || strings.HasPrefix(rel, "..") {
		return ""
	}
	head := make([]byte, 128)
	f, err := os.Open(bin)
	if err != nil {
		return ""
	}
	n, _ := f.Read(head)
	f.Close()
	ext := strings.ToLower(filepath.Ext(bin))
	if ext != ".js" && ext != ".cjs" && ext != ".mjs" {
		line, _, _ := strings.Cut(string(head[:n]), "\n")
		if !strings.HasPrefix(line, "#!") || !strings.Contains(line, "node") {
			return ""
		}
	}
	return bin
}

// npxDirect is s as node running the package's program, when magpie has
// the package installed; s when it hasn't, with the install started.
func npxDirect(s *Server) *Server {
	r, ok := readNpx(s)
	if !ok || npxInstall == nil {
		return s
	}
	node := nodeBeside(s.Command)
	if node == "" {
		return s
	}
	dir := npxDir(r.spec)
	bin := npxBin(dir, r.name)
	if bin == "" || !r.pinned() && npxStale(dir) {
		npxStart(r, s.Command, dir, bin == "")
	}
	if bin == "" {
		return s
	}
	c := *s
	c.Command = node
	c.Args = append([]string{bin}, r.args...)
	return &c
}

// npxStale says the install is a day old.
func npxStale(dir string) bool {
	st, err := os.Stat(filepath.Join(dir, ".installed"))
	return err != nil || time.Since(st.ModTime()) > 24*time.Hour
}

var npxQueue struct {
	sync.Mutex
	busy   map[string]bool
	failed map[string]time.Time
}

// npxStart installs the spec in the background, once at a time; new says
// nothing is installed yet, and the agents are synced again once it is.
// A failed install is tried again after an hour.
func npxStart(r npxRun, npx, dir string, new bool) {
	npxQueue.Lock()
	if npxQueue.busy == nil {
		npxQueue.busy, npxQueue.failed = map[string]bool{}, map[string]time.Time{}
	}
	if npxQueue.busy[dir] || time.Since(npxQueue.failed[dir]) < time.Hour {
		npxQueue.Unlock()
		return
	}
	npxQueue.busy[dir] = true
	npxQueue.Unlock()
	go func() {
		err := npxInstall(r.spec, npx, dir)
		npxQueue.Lock()
		delete(npxQueue.busy, dir)
		if err != nil {
			npxQueue.failed[dir] = time.Now()
		}
		npxQueue.Unlock()
		if err != nil {
			log.Printf("library: installing %s for its MCP server: %v", r.spec, err)
			return
		}
		if new && npxBin(dir, r.name) != "" && npxSynced != nil {
			npxSynced()
		}
	}()
}

// npxInstall installs spec in dir with the npm beside npx; nil in every
// test binary (testsLeaveTheMachine), which never reaches the registry.
var npxInstall = realInstall

func realInstall(spec, npx, dir string) error {
	npm := "npm"
	if strings.ContainsAny(npx, `/\`) {
		npm = filepath.Join(filepath.Dir(npx), "npm")
		if runtime.GOOS == "windows" {
			npm += ".cmd"
		}
	} else if _, err := exec.LookPath(npm); err != nil {
		if p := proc.FindTool(npm); p != "" {
			npm = p
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// a package.json of its own, so npm never takes a folder above for
	// the project
	if _, err := os.Stat(filepath.Join(dir, "package.json")); err != nil {
		if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte("{\"private\":true}\n"), 0o644); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cmd := proc.CommandContext(ctx, npm, "install", "--prefix", dir, "--no-save", "--no-package-lock", "--no-audit", "--no-fund", "--loglevel=error", spec)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return &npmError{err, strings.TrimSpace(string(out))}
	}
	return os.WriteFile(filepath.Join(dir, ".installed"), []byte(spec+"\n"), 0o644)
}

type npmError struct {
	err error
	out string
}

func (e *npmError) Error() string {
	if len(e.out) > 400 {
		e.out = e.out[len(e.out)-400:]
	}
	return e.err.Error() + ": " + e.out
}

// npxSynced syncs the agents again, for the servers an install has given
// a program; a var for tests.
var npxSynced func()

// testsLeaveTheMachine keeps every package's tests, not the library's
// alone, from installing a server's package or ending the machine's own
// processes: a test that adds a server run with `npx -y` and takes it out
// again, as internal/cli's TestLibraryMCPHub does, had npm install it
// under its temp HOME after it ended, which failed its cleanup, and ended
// the user's own running `npx -y` server of that name (#4140). A test
// that is about either sets its own.
func testsLeaveTheMachine() {
	if testing.Testing() {
		npxInstall = nil
		ending = func([]*Server) {}
	}
}

func init() {
	testsLeaveTheMachine()
	npxSynced = func() {
		if _, err := Sync(); err != nil {
			log.Printf("library: syncing after an MCP package install: %v", err)
		}
	}
}

// npxDirs are the folders of the packages the library's servers run.
func (l *Library) npxDirs() map[string]bool {
	dirs := map[string]bool{}
	for _, s := range l.MCP {
		if r, ok := readNpx(s); ok {
			dirs[filepath.Base(npxDir(r.spec))] = true
		}
	}
	return dirs
}

// npxPrune takes out the installs no server of the library's runs, a day
// after the last: a server taken out of the library leaves no package
// behind.
func npxPrune(keep map[string]bool) {
	if npxInstall == nil {
		return
	}
	root := filepath.Dir(npxDir(""))
	des, _ := os.ReadDir(root)
	for _, de := range des {
		dir := filepath.Join(root, de.Name())
		if !de.IsDir() || keep[de.Name()] || !npxStale(dir) {
			continue
		}
		npxQueue.Lock()
		busy := npxQueue.busy[dir]
		npxQueue.Unlock()
		if !busy {
			os.RemoveAll(dir)
		}
	}
}
