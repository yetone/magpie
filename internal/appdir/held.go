package appdir

// What keeps magpie's folders from moving on Windows (#7812): there a
// folder can't be renamed while any process has a file in it open, and
// one magpie doesn't run often does. An agent keeps the MCP servers it
// started from magpie's mcp-npm folder for its whole session, whether
// magpie quits or not. So a move first checks that the folder can be
// moved at all, before a byte is copied, and names what holds it: the
// user quits those and moves again.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// A Proc is a running process as a listing of every one saw it.
type Proc struct {
	PID, PPID int
	Args      string // its command line
}

// HeldBy lists every running process, where a folder can't be renamed
// while one has a file in it open (Windows); nil elsewhere, where a
// rename never waits on them. The cli sets it (appdir can't import proc).
var HeldBy func() ([]Proc, error)

// A Holder is a process whose command line names one of magpie's folders.
type Holder struct {
	PID  int
	Name string // what it runs: its package, else its program
	By   string // the program that started it, "" when not known
	Ours bool   // it runs under this magpie, and ends with it
}

func (h Holder) String() string {
	s := h.Name
	if h.By != "" {
		s += " (started by " + h.By + ")"
	}
	return fmt.Sprintf("%s, pid %d", s, h.PID)
}

// HeldError is why magpie's folder can't move now: what has files in it
// open, or the system's refusal when none was found.
type HeldError struct {
	Dir     string
	Holders []Holder
	Err     error
}

func (e *HeldError) Error() string {
	if len(e.Holders) == 0 {
		return fmt.Sprintf("%s can't be moved while something has a file in it open (%v): quit the agents that use magpie's MCP servers, then move again", e.Dir, e.Err)
	}
	names := make([]string, len(e.Holders))
	for i, h := range e.Holders {
		names[i] = h.String()
	}
	return fmt.Sprintf("%s can't be moved while these run from it: %s. Quit them, or the agents that started them, then move again", e.Dir, strings.Join(names, "; "))
}

func (e *HeldError) Unwrap() error { return e.Err }

// Holding is the processes in ps, but self and what it runs under, whose
// command line names a file in one of dirs. Case and slashes aside, as
// Windows reads a path.
func Holding(ps []Proc, dirs []string, self int) []Holder {
	parent := map[int]int{}
	args := map[int]string{}
	for _, p := range ps {
		args[p.PID] = p.Args
		if p.PID != p.PPID {
			parent[p.PID] = p.PPID
		}
	}
	above := map[int]bool{}
	for p, n := self, 0; p > 0 && n <= len(ps); p, n = parent[p], n+1 {
		above[p] = true
	}
	ours := func(pid int) bool {
		for p, n := parent[pid], 0; p > 0 && n <= len(ps); p, n = parent[p], n+1 {
			if p == self {
				return true
			}
		}
		return false
	}
	var keys []string
	for _, d := range dirs {
		if d = strings.TrimRight(winPath(d), `\`); d != "" {
			keys = append(keys, d)
		}
	}
	var hs []Holder
	for _, p := range ps {
		if above[p.PID] || p.Args == "" {
			continue
		}
		a := winPath(p.Args)
		in := false
		for _, k := range keys {
			if i := strings.Index(a, k); i >= 0 {
				if rest := a[i+len(k):]; rest == "" || strings.ContainsAny(rest[:1], `\" `) {
					in = true
					break
				}
			}
		}
		if !in {
			continue
		}
		h := Holder{PID: p.PID, Name: runs(p.Args), Ours: ours(p.PID)}
		if pa, ok := args[parent[p.PID]]; ok && pa != "" {
			h.By = program(pa)
		}
		hs = append(hs, h)
	}
	return hs
}

func winPath(s string) string { return strings.ToLower(strings.ReplaceAll(s, "/", `\`)) }

// runs names what a command line runs: the package a node_modules path
// in it names (figma-developer-mcp, @scope/name), else its program.
func runs(args string) string {
	a := strings.ReplaceAll(args, `\`, "/")
	if _, rest, ok := strings.Cut(a, "node_modules/"); ok {
		parts := strings.SplitN(rest, "/", 3)
		name := parts[0]
		if strings.HasPrefix(name, "@") && len(parts) > 1 {
			name += "/" + parts[1]
		}
		if name = strings.Trim(name, `" `); name != "" {
			return name
		}
	}
	return program(args)
}

// program is the base name of a command line's program, quoted or not.
func program(args string) string {
	a := strings.TrimSpace(args)
	var exe string
	if strings.HasPrefix(a, `"`) {
		exe, _, _ = strings.Cut(a[1:], `"`)
	} else {
		exe, _, _ = strings.Cut(a, " ")
	}
	exe = strings.ReplaceAll(exe, `\`, "/")
	return filepath.Base(exe)
}

// holders lists what holds magpie's folders (where they are reached and
// where they are now), and whether it could tell: false without HeldBy
// or when the system couldn't be asked.
func holders() ([]Holder, bool) {
	if HeldBy == nil {
		return nil, false
	}
	ps, err := HeldBy()
	if err != nil {
		return nil, false
	}
	dirs := []string{Config(), Cache()}
	for _, d := range []string{Config(), Cache()} {
		if t, ok := linkOf(d); ok {
			dirs = append(dirs, t)
		}
	}
	return Holding(ps, dirs, os.Getpid()), true
}

// notOurs is the holders that won't end when this magpie quits.
func notOurs(hs []Holder) []Holder {
	var out []Holder
	for _, h := range hs {
		if !h.Ours {
			out = append(out, h)
		}
	}
	return out
}

// heldWait is how long a move waits for what holds the folders to let
// go: the magpie it replaces has just quit, and what ran under it may
// still be ending.
var heldWait = 10 * time.Second

// waitFree waits, up to heldWait, until nothing holds magpie's folders,
// and is what still does after it.
func waitFree() []Holder {
	until := time.Now().Add(heldWait)
	for {
		hs, ok := holders()
		if !ok || len(hs) == 0 || time.Now().After(until) {
			return hs
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// probeRename is the rename that checks a folder can be moved; a var for
// tests, which have it refused as Windows refuses a folder held open.
var probeRename = os.Rename

// checkSuffix names a folder while movable checks it; Unreachable, at
// every start, puts one a magpie that died in the check left back.
const checkSuffix = ".moving-check"

// movable checks that dir can be renamed, as moving it needs (the copy
// across disks too: the folder is put aside before the link takes its
// place), by renaming it and back. Nothing is copied before this passes.
func movable(dir string) error {
	aside := dir + checkSuffix
	if err := probeRename(dir, aside); err != nil {
		hs, _ := holders()
		return &HeldError{Dir: dir, Holders: hs, Err: err}
	}
	var err error
	for i := 0; i < 5; i++ {
		if err = os.Rename(aside, dir); err == nil {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("magpie's folder is at %s: it was renamed to check it can move and couldn't be renamed back (%w); rename it to %s", aside, err, dir)
}
