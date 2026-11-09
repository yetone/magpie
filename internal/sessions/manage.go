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
// Pi's, omp's, Cursor CLI's (a chat's folder, its store and meta.json),
// DeepSeek Harness's (a session's folder, and its listing's cached row) and
// Droid's (a transcript and its .settings.json, and its subagents'; Droid's
// own session index is a cache it rebuilds from those files, as it does
// when it deletes an empty draft itself; zachzhao1984 on Discord) and
// Antigravity CLI's (agy: a conversation's brain folder, conversations/<id>.db
// and annotations/<id>.pbtxt, what agy's own delete removes; ysm119 on
// Discord). agy's conversation_summaries.db and jetbox_summaries_proto.pb,
// which it keeps open, keep the row, and agy lists no conversation whose
// files are gone, as a restore brings it back to its /resume list.
// Hermes's and Alma's are rows in a database the agent keeps open and
// writes, so they are only listed, and so are OpenCode's and ZCode's, and
// Cline's, whose sessions.db lists a session whose files are gone. Grok
// Build's are folders, but it copies each session's text into
// session_search.sqlite, which a delete would leave behind. Kimi Code's are
// folders, but session_index.jsonl, which Kimi writes, names each one. A
// Claude Desktop (Cowork) session is a Claude Code folder inside a session
// Desktop keeps and lists itself. The agents' indexes are left as they are: Codex's
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
	// Undeletable: why a session of an agent whose sessions can be deleted
	// can't be: "cursor-editor", a chat Cursor's editor keeps in its own
	// database, or "read-only" (rowDeletable). The page says it on the
	// row's delete, never by leaving the control out (ysm119 on Discord).
	Undeletable string `json:"undeletable,omitempty"`
	// Pending: a file of it is still being read for the first time, and
	// what it spent and its messages aren't counted yet (the Sessions page
	// answers before such a read, manage_quick.go)
	Pending bool `json:"pending,omitempty"`
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
	case "claude", "qoder", "qoder-cn", "workbuddy", "workbuddy-ai", "codebuddy", "codex", "pi", "raft", "cindy", "omp", "cursor", "dsh", "droid", "agy":
		return true
	}
	return false
}

// rowDeletable says whether one session of an agent can be moved to the
// trash, and, for one that can't of an agent whose sessions can, why. A
// read-only session can't, except a Cursor editor transcript whose chat
// the editor keeps no longer (fileMore.loose): it is files of its own.
func rowDeletable(agent string, s Session, fs []file) (bool, string) {
	if !Deletable(agent) {
		return false, ""
	}
	if !s.ReadOnly {
		return true, ""
	}
	if agent != "cursor" {
		return false, "read-only"
	}
	for _, f := range fs {
		if f.readOnly && !f.m().loose {
			return false, "cursor-editor"
		}
	}
	return true, ""
}

// ActiveWindow is how recently a session's file may have been written for
// it to count as in use, and not be deleted.
const ActiveWindow = time.Minute

// ErrActive is a session still being written: its agent may be running it.
var ErrActive = errors.New("the session was written to in the last minute; it may still be running")

// Agents are the agents with sessions on this computer, by how many.
func Agents() []AgentCount {
	storeMu.RLock()
	defer storeMu.RUnlock()
	mu.Lock()
	defer mu.Unlock()
	defer useDBs()()
	return agentCounts(allFiles())
}

func agentCounts(all []file) []AgentCount {
	n := map[string]map[string]bool{}
	// as listAgent lists them, from the parses kept: a copy of a
	// conversation is not a session of its own (ccCopies)
	copies := ccCopies(all)
	for _, f := range all {
		if copies[f.key] != "" {
			continue
		}
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
	storeMu.RLock()
	defer storeMu.RUnlock()
	mu.Lock()
	defer mu.Unlock()
	loadCache()
	defer useDBs()()
	return listAgent(agent, allFiles())
}

// NativeIDs are the ids of every session the agents named keep themselves,
// by agent: one nothing was said in too, as ListAgent lists it. It tells a
// gateway session an agent keeps from one only magpie saw. Only the named
// agents' files are looked at, and of those only the ones whose session id
// is inside the file are read when they changed.
func NativeIDs(agents map[string]bool) map[string]map[string]bool {
	storeMu.RLock()
	defer storeMu.RUnlock()
	mu.Lock()
	defer mu.Unlock()
	loadCache()
	defer useDBs()()
	return nativeIDs(agents, allFiles())
}

// ManageView is the session management page, read in one pass over every
// agent's files: the agents with sessions, the ids of the sessions of those
// asked for (as NativeIDs), and the agent shown with its sessions (as
// ListAgent).
type ManageView struct {
	Agents   []AgentCount
	Native   map[string]map[string]bool
	Agent    string
	Sessions []Managed
}

// Manage reads the management page. pick, given the counts and the native
// ids of the agents in native, names the agent to list ("" for none); it
// runs under the sessions lock and must not call back into this package.
// Only native's agents and the one picked are read: a change to another
// agent's sessions waits for a page that shows it (#2776).
func Manage(native map[string]bool, pick func([]AgentCount, map[string]map[string]bool) string) ManageView {
	storeMu.RLock()
	defer storeMu.RUnlock()
	mu.Lock()
	defer mu.Unlock()
	loadCache()
	defer useDBs()()
	all := allFiles()
	v := ManageView{Agents: agentCounts(all), Native: nativeIDsWith(native, all, true)}
	v.Agent = pick(v.Agents, v.Native)
	if v.Agent != "" {
		v.Sessions = listAgentWith(v.Agent, all, true)
	}
	return v
}

// readFor brings want's parses up to date: all of them, or, quick, as
// quickRead does, with pending the paths still being read.
func readFor(want, all []file, quick bool) (pending map[string]bool, done func()) {
	if !quick {
		refresh(want, all)
		return nil, func() {}
	}
	return quickRead(want, all)
}

// idInFile says which agents' session ids are read from inside the
// session's file, not taken from its name (as assemble does). ZCode's are:
// its database rows are the files, a fork's and a subagent's filed under
// the session they ran in (ocRoots), which a request names by their own ids
// (subIDs, zcodeBareID).
func idInFile(agent string) bool {
	switch agent {
	case "codex", "pi", "raft", "cindy", "omp", "dsh", "grok", "droid", "zcode":
		return true
	}
	return false
}

func nativeIDs(agents map[string]bool, all []file) map[string]map[string]bool {
	return nativeIDsWith(agents, all, false)
}

func nativeIDsWith(agents map[string]bool, all []file, quick bool) map[string]map[string]bool {
	// Raft's Pi sessions are Pi's to the gateway (GatewayKeys)
	asked := func(agent string) bool { return agents[agent] || agent == "raft" && agents["pi"] }
	groups := byKey(all, func(f *file) bool { return asked(f.agent) })
	var want []file
	for _, f := range all {
		if !asked(f.agent) {
			continue
		}
		if idInFile(f.agent) {
			want = append(want, f)
		}
	}
	_, done := readFor(want, all, quick)
	defer done()
	out := map[string]map[string]bool{}
	for a := range agents {
		out[a] = map[string]bool{}
	}
	for _, fs := range groups {
		id := sessionID(fs)
		if a := fs[0].agent; agents[a] {
			// by every id a request can name it (requestIDs, as
			// GatewayKeys), its subagents' too: their calls are counted in
			// it (subIDs)
			for _, sid := range append([]string{id}, subIDs(fs, id)...) {
				for _, rid := range requestIDs(a, sid) {
					out[a][rid] = true
				}
			}
		}
		if fs[0].agent == "raft" && agents["pi"] {
			out["pi"][id] = true
		}
	}
	return out
}

func listAgent(agent string, all []file) []Managed {
	return listAgentWith(agent, all, false)
}

func listAgentWith(agent string, all []file, quick bool) []Managed {
	groups := byKey(all, func(f *file) bool { return f.agent == agent })
	var mine []file
	for _, f := range all {
		if f.agent == agent {
			mine = append(mine, f)
		}
	}
	pending, done := readFor(mine, all, quick)
	defer done()
	identities := map[string]CodexIdentity(nil)
	taskTitles := map[codexTaskTitle]string(nil)
	if agent == "codex" {
		identities = codexIdentitySnapshot(mine)
		taskTitles = codexSubagentTaskTitles(mine)
	}
	copies, branches := ccFamily(mine)
	copyIDs := copiesByKeeper(copies, groups)
	price, titles := pricer(), codexTitleOf()
	out := []Managed{}
	for k, fs := range groups {
		if copies[k] != "" {
			continue
		}
		s, _ := assemble(fs, price, titles)
		s.Copies = copyIDs[k]
		setBranch(&s, branches[k], groups)
		setSessionCodexIdentity(&s, identities)
		setSessionCodexTaskTitle(&s, taskTitles)
		listed(&s)
		// a WSL distro's too (TJHHHH): moved out over \\wsl.localhost
		m := Managed{Session: s, Files: len(fs)}
		m.Deletable, m.Undeletable = rowDeletable(agent, s, fs)
		for _, f := range fs {
			m.Size += f.size
			m.Pending = m.Pending || pending[f.path]
			if st := cache[f.path]; st != nil && f.main {
				for _, d := range st.Days.all() {
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

// listed fills in what assemble leaves out of a session nothing was said
// in, as ListAgent lists it and Find finds it: how to resume it, and
// whether its conversation can be read.
func listed(s *Session) {
	if s.Resume == "" && !s.ReadOnly {
		s.Resume = resumeCommand(s.WSL, s.Agent, s.ID, s.Cwd, s.Path)
	}
	if s.Carry == nil {
		s.Carry = carries(*s)
	}
	s.Transcript = readable(*s)
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
	// Gateway: a session seen only through magpie's gateway, whose items
	// are the conversation text magpie recorded of it (gateway_delete.go)
	Gateway bool `json:"gateway,omitempty"`
	// CodexCatalog: a Codex thread's rows in the Codex app's catalog,
	// taken out with it and put back on Restore (codex_catalog.go)
	CodexCatalog []codexCatalogRow `json:"codex_catalog,omitempty"`
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
	storeMu.Lock()
	defer storeMu.Unlock()
	mu.Lock()
	defer mu.Unlock()
	loadCache()
	defer useDBs()()
	all := allFiles()
	groups := byKey(all, func(f *file) bool { return f.agent == agent })
	var mine []file
	for _, f := range all {
		if f.agent == agent {
			mine = append(mine, f)
		}
	}
	refresh(mine, all)
	// the session as the page lists it: by the id assemble gives it
	var fs []file
	var s Session
	key := ""
	price, titles := pricer(), codexTitleOf()
	for k, g := range groups {
		if one, _ := assembleListed(g, price, titles); one.ID == id {
			fs, s, key = g, one, k
			break
		}
	}
	if len(fs) == 0 {
		return Trashed{}, errors.New("no such session")
	}
	if ok, why := rowDeletable(agent, s, fs); !ok {
		if why == "cursor-editor" {
			return Trashed{}, errors.New("Cursor's editor keeps this chat in its own database: delete it in Cursor")
		}
		return Trashed{}, errors.New("this session is read only")
	}
	paths := sessionPaths(agent, id, fs)
	// the copies of its conversation go with it: they were listed as
	// this session, and one would come back as its row after (ccCopies)
	if len(paths) > 0 {
		for k, keeper := range ccCopies(mine) {
			if c := groups[k]; keeper == key && len(c) > 0 && !c[0].readOnly {
				paths = append(paths, sessionPaths(agent, sessionID(c), c)...)
				fs = append(fs[:len(fs):len(fs)], c...)
			}
		}
	}
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
	// agy holds a conversation's presence lock while it has it open, idle
	// for longer than a minute too
	if agent == "agy" && agyHeld(filepath.Join(agyDataDir(fs), "presence", id+".lock")) {
		return Trashed{}, ErrActive
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
		delete(ccUUIDMemo, f.path)
	}
	saveCache()
	wslForget(fs)
	if agent == "codex" {
		// the Codex app's list of it, which it keeps apart from the files
		var homes []string
		for _, p := range paths {
			homes = append(homes, codexHomeOf(p))
		}
		tidyCodexCatalog(homes)
		if b, err := os.ReadFile(filepath.Join(dir, manifest)); err == nil {
			json.Unmarshal(b, &t)
		}
		t.Key = agent + "/" + name
	}
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
			if f.readOnly {
				// an editor transcript, never the folder it is in
				for _, p := range cursorEditorPaths(f, id) {
					add(p)
				}
				continue
			}
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
	if agent == "agy" {
		// what agy's own delete (its /resume picker) removes: the
		// conversation's brain folder, its trajectory database with the
		// journal files SQLite keeps beside it, and its annotations
		dir := agyDataDir(fs)
		if dir == "" {
			return nil
		}
		add(filepath.Join(dir, "brain", id))
		db := filepath.Join(dir, "conversations", id+".db")
		for _, p := range []string{db, db + "-wal", db + "-shm", db + "-journal"} {
			add(p)
		}
		add(filepath.Join(dir, "annotations", id+".pbtxt"))
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
			add(dshCacheRecord(f.m().sid))
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
			if agent != "codex" && agent != "pi" && agent != "raft" && agent != "cindy" {
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
	if agent == "droid" {
		// each transcript's settings beside it: what it spent, its model
		for _, f := range fs {
			if f.m().manifest != "" && !inside(f.m().manifest, out) {
				add(f.m().manifest)
			}
		}
	}
	if agent == "claude" {
		dir := ClaudeDir()
		if fs[0].m().wsl != "" {
			// the distro's own ~/.claude
			if dir = wslHomeOf(fs[0].m().wsl); dir == "" {
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
		// gateway mode lists what the gateway kept, not this computer's
		// agents' sessions (OwnOff)
		if !t.Gateway && ownOff() {
			continue
		}
		dir := filepath.Dir(p)
		t.Key = filepath.Base(filepath.Dir(dir)) + "/" + filepath.Base(dir)
		if t.Gateway {
			// its text expires, or is cleared, while it waits here
			t.Size = sizeOf(filepath.Join(dir, "files"))
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Deleted.After(out[j].Deleted) })
	return out
}

// trashFolder is a trash key's folder, or "" for a key that isn't one.
func trashFolder(key string) string {
	agent, name, ok := strings.Cut(key, "/")
	if !ok || (!Deletable(agent) && agent != gatewayTrash) || name == "" || strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
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
	if !isGatewayKey(key) && ownOff() {
		return ErrNotRead
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
	if !isGatewayKey(key) && ownOff() {
		return Trashed{}, ErrNotRead
	}
	mu.Lock()
	defer mu.Unlock()
	if isGatewayKey(key) {
		// its items can only be this session's text folders in the store
		for _, m := range t.Items {
			if !t.Gateway || m.Name != filepath.Base(m.Name) || m.Name == "" || filepath.Base(m.From) != gatewayIdentity(t.Agent, t.ID) ||
				filepath.Dir(filepath.Dir(m.From)) != gatewayDir() {
				return Trashed{}, errors.New("the trash note is damaged")
			}
		}
		if !t.Gateway {
			return Trashed{}, errors.New("the trash note is damaged")
		}
		if err := restoreGateway(dir, t); err != nil {
			return Trashed{}, err
		}
		os.RemoveAll(dir)
		return t, nil
	}
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
	// the Codex app lists it again: it wouldn't take in a thread that old
	// by itself
	if t.Agent == "codex" && len(t.CodexCatalog) > 0 {
		restoreCodexCatalog(t.CodexCatalog)
	}
	os.RemoveAll(dir)
	wslRelistNow(t.Items)
	return t, nil
}
