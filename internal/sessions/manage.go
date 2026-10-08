package sessions

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// Managing sessions: every session of one agent, to look through and pick
// up again, and to delete — which never deletes. A deleted session's files
// are moved into magpie's own trash folder (<magpie dir>/trash/sessions/),
// with a note of where each came from, and a restore moves them back.
//
// Only the agents whose sessions are files of their own can be deleted:
// Claude Code's (and Qoder's and WorkBuddy's, kept the same way), Codex's,
// Pi's, omp's, Cursor CLI's (a chat's folder, its store and meta.json) and
// DeepSeek Harness's (a session's folder, and its listing's cached row).
// Hermes's and Alma's are rows in a database the agent keeps open and
// writes, so they are only listed, and so are Cline's, whose sessions.db
// lists a session whose files are gone. The agents' indexes are left as they are: Codex's
// session_index.jsonl (names by thread id) and its state database, and
// Claude Code's history.jsonl (the prompts typed, for the up arrow), are
// written by the agent while it runs, and a name or a prompt left for a
// session that is gone does nothing.

// Managed is a session as the management page lists it: what List has, and
// how big its files are, how many messages it has, and whether it can be
// deleted.
type Managed struct {
	Session
	Size      int64 `json:"size"`
	Messages  int   `json:"messages"`
	Files     int   `json:"files"`
	Deletable bool  `json:"deletable"`
	// Codex: the provider the session was made with, and the one its Codex
	// uses now, whose sessions alone its history lists (#887)
	Provider     string `json:"provider,omitempty"`
	UsesProvider string `json:"uses_provider,omitempty"`
}

// AgentCount is an agent with sessions on this computer, and how many.
type AgentCount struct {
	Agent     string `json:"agent"`
	Count     int    `json:"count"`
	Deletable bool   `json:"deletable"`
}

// Deletable says whether magpie can move an agent's sessions to its trash:
// those kept as files of their own, in a layout magpie knows whole.
func Deletable(agent string) bool {
	switch agent {
	case "claude", "qoder", "qoder-cn", "workbuddy", "codex", "pi", "omp", "cursor", "dsh":
		return true
	}
	return false
}

// ActiveWindow is how recently a session's file may have been written for
// it to count as in use, and not be deleted.
const ActiveWindow = time.Minute

// ErrActive is a session still being written: its agent may be running it.
var ErrActive = errors.New("the session was written to in the last minute; it may still be running")

// Agents are the agents with sessions on this computer, by how many.
func Agents() []AgentCount {
	dbReadMu.Lock()
	defer dbReadMu.Unlock()
	mu.Lock()
	defer mu.Unlock()
	defer closeDBs()
	n := map[string]map[string]bool{}
	for _, f := range allFiles() {
		if n[f.agent] == nil {
			n[f.agent] = map[string]bool{}
		}
		n[f.agent][f.key] = true
	}
	out := []AgentCount{}
	for a, keys := range n {
		out = append(out, AgentCount{Agent: a, Count: len(keys), Deletable: Deletable(a)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Agent < out[j].Agent
	})
	return out
}

// ListAgent is every session of one agent, the most recently active first,
// with no limit; one nothing was said in is listed too, to be cleared away.
func ListAgent(agent string) []Managed {
	dbReadMu.Lock()
	defer dbReadMu.Unlock()
	mu.Lock()
	defer mu.Unlock()
	loadCache()
	defer closeDBs()
	all := allFiles()
	groups := map[string][]file{}
	var mine []file
	for _, f := range all {
		if f.agent == agent {
			groups[f.key] = append(groups[f.key], f)
			mine = append(mine, f)
		}
	}
	refresh(mine, all)
	price := pricer()
	out := []Managed{}
	for _, fs := range groups {
		s, _ := assemble(fs, price)
		if s.Resume == "" && !s.ReadOnly {
			s.Resume = resumeCommand(s.WSL, s.Agent, s.ID, s.Cwd)
		}
		if s.Carry == nil {
			s.Carry = carries(s)
		}
		s.Transcript = HasTranscript(s.Agent)
		// a WSL distro's too (TJHHHH): moved out over \\wsl.localhost
		m := Managed{Session: s, Files: len(fs), Deletable: Deletable(agent) && !s.ReadOnly}
		for _, f := range fs {
			m.Size += f.size
			if st := cache[f.path]; st != nil && f.main {
				for _, d := range st.Days {
					m.Messages += d.Prompts + d.Replies
				}
			}
		}
		out = append(out, m)
	}
	if agent == "codex" {
		codexProviders(out, groups)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].Last.Equal(out[j].Last) {
			return out[i].Last.After(out[j].Last)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// TrashDir is where deleted sessions are kept: <magpie dir>/trash/sessions.
func TrashDir() string { return filepath.Join(settings.Dir(), "trash", "sessions") }

// Trashed is a deleted session in magpie's trash.
type Trashed struct {
	Key     string    `json:"key"` // its folder in the trash, <agent>/<name>
	Agent   string    `json:"agent"`
	ID      string    `json:"id"`
	Title   string    `json:"title"`
	Cwd     string    `json:"cwd"`
	Last    time.Time `json:"last"`
	Deleted time.Time `json:"deleted"`
	Size    int64     `json:"size"`
	Items   []moved   `json:"items"`
}

// moved is one file or folder of a session moved to the trash: where it
// was, and its name in the trash folder's files/.
type moved struct {
	From string `json:"from"`
	Name string `json:"name"`
}

// manifest is the note kept beside a trashed session's files.
const manifest = "session.json"

// Delete moves a session's files to magpie's trash: its own file, the
// folder of its subagents and tool results beside it, its other files (a
// Codex session's later segments, its subagents' elsewhere) and, for Claude
// Code, the file history, todos and environment it keeps by the session's
// id. A session any file of which was written in the last minute is not
// touched (ErrActive). One in a WSL distro is moved out of it over
// \\wsl.localhost the same way, its Claude Code files from the distro's
// ~/.claude, and Restore puts it back there.
func Delete(agent, id string) (Trashed, error) {
	if !Deletable(agent) {
		return Trashed{}, fmt.Errorf("magpie can't delete %s sessions", agent)
	}
	if !safeID.MatchString(id) {
		return Trashed{}, errors.New("no such session")
	}
	dbReadMu.Lock()
	defer dbReadMu.Unlock()
	mu.Lock()
	defer mu.Unlock()
	loadCache()
	defer closeDBs()
	all := allFiles()
	groups := map[string][]file{}
	var mine []file
	for _, f := range all {
		if f.agent == agent {
			groups[f.key] = append(groups[f.key], f)
			mine = append(mine, f)
		}
	}
	refresh(mine, all)
	// the session as the page lists it: by the id assemble gives it
	var fs []file
	var s Session
	price := pricer()
	for _, g := range groups {
		if one, _ := assemble(g, price); one.ID == id {
			fs, s = g, one
			break
		}
	}
	if len(fs) == 0 {
		return Trashed{}, errors.New("no such session")
	}
	paths := sessionPaths(agent, id, fs)
	if len(paths) == 0 {
		// a stopped distro's listing, of files since gone
		wslForget(fs)
		return Trashed{}, errors.New("no such session")
	}
	now := time.Now()
	for _, p := range paths {
		if recent(p, now) {
			return Trashed{}, ErrActive
		}
	}
	t := Trashed{Agent: agent, ID: id, Title: s.Title, Cwd: s.Cwd, Last: s.Last, Deleted: now}
	name := now.UTC().Format("20060102T150405.000000000") + "-" + id
	dir := filepath.Join(TrashDir(), agent, name)
	t.Key = agent + "/" + name
	if err := os.MkdirAll(filepath.Join(dir, "files"), 0o700); err != nil {
		return Trashed{}, err
	}
	for i, p := range paths {
		m := moved{From: p, Name: strconv.Itoa(i) + "-" + filepath.Base(p)}
		t.Size += sizeOf(p)
		if err := move(p, filepath.Join(dir, "files", m.Name)); err != nil {
			// what was moved goes back, and the session stays as it was
			for _, back := range t.Items {
				move(filepath.Join(dir, "files", back.Name), back.From)
			}
			os.RemoveAll(dir)
			return Trashed{}, err
		}
		t.Items = append(t.Items, m)
		// the note is kept as it grows, so a crash leaves it naming
		// every file moved so far
		writeManifest(dir, t)
	}
	if err := writeManifest(dir, t); err != nil {
		return Trashed{}, err
	}
	for _, f := range fs {
		delete(cache, f.path)
	}
	saveCache()
	wslForget(fs)
	return t, nil
}

// sessionPaths are the files and folders a session's delete moves, none
// inside another.
func sessionPaths(agent, id string, fs []file) []string {
	var out []string
	add := func(p string) {
		if _, err := os.Lstat(p); err == nil {
			out = append(out, p)
		}
	}
	if agent == "cursor" {
		// a chat is its folder, its subagents' chats theirs, and the
		// transcripts Cursor wrote of it
		cwd := ""
		for _, f := range fs {
			add(filepath.Dir(f.path))
			if st := cache[f.path]; st != nil && f.main {
				cwd = st.Cwd
			}
		}
		for _, p := range cursorTranscripts(cwd, id) {
			add(p)
		}
		return out
	}
	if agent == "dsh" {
		// each session's own folder (the parent's and its subagents'), and
		// the row dsh caches of it for its listing; none at all when one
		// isn't in a folder of its own, rather than half the session
		for _, f := range fs {
			if dshSessionDir(f) == "" {
				return nil
			}
		}
		for _, f := range fs {
			add(dshSessionDir(f))
			add(dshCacheRecord(f.sid))
		}
		return out
	}
	for _, f := range fs {
		if f.main {
			add(f.path)
			// the compressed form a rollout is read past while both are there
			if agent == "codex" {
				add(rolloutTwin(f.path))
			}
			// the folder beside it: a Claude Code session's subagents and
			// tool results, an omp session's artifacts
			if agent != "codex" && agent != "pi" {
				if d := strings.TrimSuffix(f.path, ".jsonl"); d != f.path {
					if fi, err := os.Stat(d); err == nil && fi.IsDir() {
						add(d)
					}
				}
			}
		}
	}
	for _, f := range fs {
		if !f.main && !inside(f.path, out) {
			add(f.path)
		}
	}
	if agent == "claude" {
		dir := ClaudeDir()
		if fs[0].wsl != "" {
			// the distro's own ~/.claude
			if dir = wslHomeOf(fs[0].wsl); dir == "" {
				return out
			}
			dir = filepath.Join(dir, ".claude")
		}
		add(filepath.Join(dir, "file-history", id))
		add(filepath.Join(dir, "session-env", id))
		todos, _ := filepath.Glob(filepath.Join(dir, "todos", id+"-*.json"))
		for _, p := range todos {
			add(p)
		}
	}
	return out
}

func inside(p string, dirs []string) bool {
	for _, d := range dirs {
		if strings.HasPrefix(p, d+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// recent says whether p, or anything in it, was written within ActiveWindow.
func recent(p string, now time.Time) bool {
	hot := false
	filepath.WalkDir(p, func(q string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if fi, err := d.Info(); err == nil && now.Sub(fi.ModTime()) < ActiveWindow {
			hot = true
			return filepath.SkipAll
		}
		return nil
	})
	return hot
}

func sizeOf(p string) int64 {
	var n int64
	filepath.WalkDir(p, func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

func writeManifest(dir string, t Trashed) error {
	b, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, manifest+".tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, manifest))
}

// move renames a file or folder, or copies it and then removes it when the
// two are on different volumes.
func move(from, to string) error {
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	if _, err := os.Lstat(to); err == nil {
		return fmt.Errorf("%s is already there", to)
	}
	if err := os.Rename(from, to); err == nil {
		return nil
	} else if !errors.Is(err, crossDeviceErr) {
		return err
	}
	if err := copyAll(from, to); err != nil {
		os.RemoveAll(to)
		return err
	}
	return os.RemoveAll(from)
}

func copyAll(from, to string) error {
	return filepath.WalkDir(from, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		dst := filepath.Join(to, rel)
		fi, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(dst, fi.Mode().Perm()|0o700)
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return copySymlink(target, dst, fi)
		}
		if !fi.Mode().IsRegular() {
			return nil
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fi.Mode().Perm())
		if err != nil {
			return err
		}
		_, err = io.Copy(out, in)
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err == nil {
			os.Chtimes(dst, fi.ModTime(), fi.ModTime())
		}
		return err
	})
}

// Trash is what is in magpie's session trash, the latest deleted first.
func Trash() []Trashed {
	out := []Trashed{}
	notes, _ := filepath.Glob(filepath.Join(TrashDir(), "*", "*", manifest))
	for _, p := range notes {
		var t Trashed
		b, err := os.ReadFile(p)
		if err != nil || json.Unmarshal(b, &t) != nil {
			continue
		}
		dir := filepath.Dir(p)
		t.Key = filepath.Base(filepath.Dir(dir)) + "/" + filepath.Base(dir)
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Deleted.After(out[j].Deleted) })
	return out
}

// trashFolder is a trash key's folder, or "" for a key that isn't one.
func trashFolder(key string) string {
	agent, name, ok := strings.Cut(key, "/")
	if !ok || !Deletable(agent) || name == "" || strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		return ""
	}
	return filepath.Join(TrashDir(), agent, name)
}

// Purge erases a trashed session for good: its folder in magpie's trash,
// files and note. Only when the reader asks for it (#487); nothing is ever
// erased by itself. A key that isn't a trashed session's, or whose folder
// (or its agent's) is a link that could lead out of the trash, is refused.
func Purge(key string) error {
	dir := trashFolder(key)
	if dir == "" {
		return errors.New("no such deleted session")
	}
	mu.Lock()
	defer mu.Unlock()
	for _, p := range []string{filepath.Dir(dir), dir} {
		if fi, err := os.Lstat(p); err != nil || !fi.IsDir() {
			return errors.New("no such deleted session")
		}
	}
	if fi, err := os.Lstat(filepath.Join(dir, manifest)); err != nil || !fi.Mode().IsRegular() {
		return errors.New("no such deleted session")
	}
	return os.RemoveAll(dir)
}

// Restore moves a trashed session's files back where they were. Nothing is
// moved when any of those places is taken again.
func Restore(key string) (Trashed, error) {
	dir := trashFolder(key)
	if dir == "" {
		return Trashed{}, errors.New("no such deleted session")
	}
	var t Trashed
	b, err := os.ReadFile(filepath.Join(dir, manifest))
	if err != nil || json.Unmarshal(b, &t) != nil {
		return Trashed{}, errors.New("no such deleted session")
	}
	t.Key = key
	mu.Lock()
	defer mu.Unlock()
	for _, m := range t.Items {
		if _, err := os.Lstat(m.From); err == nil {
			return Trashed{}, fmt.Errorf("%s is there again; nothing was restored", m.From)
		}
		if m.Name != filepath.Base(m.Name) || m.Name == "" {
			return Trashed{}, errors.New("the trash note is damaged")
		}
	}
	for i, m := range t.Items {
		if err := move(filepath.Join(dir, "files", m.Name), m.From); err != nil {
			// those put back go to the trash again, so it stays whole
			for _, back := range t.Items[:i] {
				move(back.From, filepath.Join(dir, "files", back.Name))
			}
			return Trashed{}, err
		}
	}
	os.RemoveAll(dir)
	wslRelistNow(t.Items)
	return t, nil
}
