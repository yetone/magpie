// Package sessions lists the agents' recent sessions from their own session
// files — Claude Code's projects/*/<id>.jsonl (and Qoder's, the same kind),
// Codex's rollout files, OpenCode's database (or its older JSON files) and
// ZCode's, Pi's session files, DeepSeek Harness's, Cline's, Grok Build's and
// WorkBuddy's — with the
// tokens each spent, what that cost at list price, and the command that
// resumes it. It only ever reads the agents' folders.
//
// The files grow long (hundreds of MB), so each one's parse is kept by path,
// size and time, and a file that only grew is read on from where it was left.
// The parses are kept on disk too, in magpie's cache folder.
package sessions

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// Tokens is a count of tokens. Input excludes what was read from cache.
type Tokens struct {
	Input      int `json:"input"`
	Output     int `json:"output"`
	CacheRead  int `json:"cache_read"`
	CacheWrite int `json:"cache_write"`
}

func (t *Tokens) add(u Tokens) {
	t.Input += u.Input
	t.Output += u.Output
	t.CacheRead += u.CacheRead
	t.CacheWrite += u.CacheWrite
}

func (t *Tokens) sub(u Tokens) {
	t.Input -= u.Input
	t.Output -= u.Output
	t.CacheRead -= u.CacheRead
	t.CacheWrite -= u.CacheWrite
}

func (t Tokens) zero() bool { return t == Tokens{} }

// Model is one model's share of a session.
type Model struct {
	Model string `json:"model"`
	Tokens
	Cost   float64 `json:"cost"`
	Priced bool    `json:"priced"`
}

// Session is one agent session.
type Session struct {
	Agent  string    `json:"agent"` // magpie agent id: claude, codex, opencode, pi, zcode, dsh, cline, qoder, qoder-cn, grok, workbuddy
	ID     string    `json:"id"`
	Cwd    string    `json:"cwd"`
	Title  string    `json:"title"` // the first prompt, else the agent's own title
	Start  time.Time `json:"start"`
	Last   time.Time `json:"last"`
	Models []Model   `json:"models"`
	Tokens
	Cost     float64 `json:"cost"`     // USD at list price, for the priced models
	Unpriced int     `json:"unpriced"` // models that spent tokens but have no known price
	Resume   string  `json:"resume"`   // the command that picks the session up again
	Path     string  `json:"path"`     // its (main) file
}

// PriceOf is the list price of a model as a session names it. Tests swap it.
var PriceOf = priceOf

// Limit is how many sessions, the latest by last activity, List reads.
const Limit = 200

// state is what one file's parse has come to, enough to read on from Off.
type state struct {
	Size   int64             `json:"size"`
	Mod    int64             `json:"mod"` // unix nanoseconds
	Off    int64             `json:"off"` // after the last whole line read
	ID     string            `json:"id,omitempty"`
	Cwd    string            `json:"cwd,omitempty"`
	Title  string            `json:"title,omitempty"`
	Named  string            `json:"named,omitempty"` // the agent's own title for it
	First  string            `json:"first,omitempty"` // the first message, when no prompt looked typed
	Start  time.Time         `json:"start"`
	Last   time.Time         `json:"last"`
	Models map[string]Tokens `json:"models,omitempty"`
	// Days is Models again, split by the local date each message was
	// written on: a session that runs past midnight counts on both days.
	Days map[string]*day `json:"days,omitempty"`
	// Claude Code: the message last counted, whose later lines repeat it,
	// and the day it was counted on
	Msg      string `json:"msg,omitempty"`
	MsgModel string `json:"msg_model,omitempty"`
	MsgUse   Tokens `json:"msg_use"`
	MsgDay   string `json:"msg_day,omitempty"`
	// Codex: the model in use, and its running total (input with cache) last seen
	Model string  `json:"model,omitempty"`
	Total *Tokens `json:"total,omitempty"`
	// Pi: in a forked session, the time it was forked; the lines before
	// it are the copy of the session it was forked from
	Since time.Time `json:"since,omitzero"`
}

// day is one local date's share of a file: tokens by model, and the time
// the session was at work on it.
type day struct {
	Models map[string]Tokens `json:"m,omitempty"`
	// Active is milliseconds of work: the gaps between one line and the
	// next, each counted when shorter than idleGap and put on the later
	// line's day. A longer pause (lunch, the night, a session picked up
	// again next week) counts nothing. A subagent's file counts none, as
	// it runs while its session's own file goes on.
	Active int64 `json:"a,omitempty"`
	// Hours is Active again, by the local hour of the day it was put on
	// (24 of them, or none)
	Hours []int64 `json:"h,omitempty"`
	// Prompts are the messages typed in the session's own file, Replies
	// the agent's messages back (Claude Code and Codex alone tell them)
	Prompts int `json:"u,omitempty"`
	Replies int `json:"r,omitempty"`
	// Tools are the tool calls made, by the tool's name, and Skills the
	// skills called up, by the skill's (subagents' files included)
	Tools  map[string]int `json:"t,omitempty"`
	Skills map[string]int `json:"k,omitempty"`
}

// tool counts a call of a tool, and of a skill when it calls one up.
func (s *state) tool(at time.Time, name, skill string) {
	if name == "" {
		return
	}
	d := s.day(dateOf(at))
	if d.Tools == nil {
		d.Tools = map[string]int{}
	}
	d.Tools[name]++
	if skill != "" {
		if d.Skills == nil {
			d.Skills = map[string]int{}
		}
		d.Skills[skill]++
	}
}

// idleGap is the longest pause between two lines still counted as work.
const idleGap = 5 * time.Minute

// dateOf is the local date of a time, "" for none.
func dateOf(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.In(time.Local).Format(time.DateOnly)
}

func (s *state) day(date string) *day {
	if s.Days == nil {
		s.Days = map[string]*day{}
	}
	d := s.Days[date]
	if d == nil {
		d = &day{}
		s.Days[date] = d
	}
	return d
}

// use counts a model's tokens, spent on a date.
func (s *state) use(date, model string, t Tokens) {
	if t.zero() {
		return
	}
	if s.Models == nil {
		s.Models = map[string]Tokens{}
	}
	m := s.Models[model]
	m.add(t)
	s.Models[model] = m
	d := s.day(date)
	if d.Models == nil {
		d.Models = map[string]Tokens{}
	}
	m = d.Models[model]
	m.add(t)
	d.Models[model] = m
}

// unuse takes back what use counted.
func (s *state) unuse(date, model string, t Tokens) {
	if m, ok := s.Models[model]; ok {
		m.sub(t)
		s.Models[model] = m
	}
	if d := s.Days[date]; d != nil {
		if m, ok := d.Models[model]; ok {
			m.sub(t)
			d.Models[model] = m
		}
	}
}

// saw notes the time of a line and, in a session's own file, the work
// since the line before.
func (s *state) saw(t time.Time, main bool) {
	if t.IsZero() {
		return
	}
	if s.Start.IsZero() || t.Before(s.Start) {
		s.Start = t
	}
	if t.After(s.Last) {
		if gap := t.Sub(s.Last); main && !s.Last.IsZero() && gap < idleGap {
			d := s.day(dateOf(t))
			d.Active += gap.Milliseconds()
			if d.Hours == nil {
				d.Hours = make([]int64, 24)
			}
			d.Hours[t.In(time.Local).Hour()] += gap.Milliseconds()
		}
		s.Last = t
	}
}

func (s *state) clone() *state {
	c := *s
	c.Models = make(map[string]Tokens, len(s.Models))
	for k, v := range s.Models {
		c.Models[k] = v
	}
	if s.Total != nil {
		t := *s.Total
		c.Total = &t
	}
	c.Days = make(map[string]*day, len(s.Days))
	for k, v := range s.Days {
		d := &day{Active: v.Active, Models: make(map[string]Tokens, len(v.Models)), Prompts: v.Prompts, Replies: v.Replies}
		if v.Hours != nil {
			d.Hours = append([]int64(nil), v.Hours...)
		}
		if v.Tools != nil {
			d.Tools = maps.Clone(v.Tools)
		}
		if v.Skills != nil {
			d.Skills = maps.Clone(v.Skills)
		}
		for m, t := range v.Models {
			d.Models[m] = t
		}
		c.Days[k] = d
	}
	return &c
}

// file is one session file found on disk.
type file struct {
	agent string
	key   string // agent:session id — a session may span files
	path  string
	main  bool // the session's own file, not a subagent's
	size  int64
	mod   time.Time
	// OpenCode and ZCode: the session, and where it is kept
	sid string
	oc  ocStore
	// Cline: the session's manifest, beside its messages; Grok Build: its
	// summary.json, beside its updates
	manifest string
}

// ClaudeDir is Claude Code's folder: $CLAUDE_CONFIG_DIR, else ~/.claude.
func ClaudeDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}

// CodexDir is Codex's folder: $CODEX_HOME, else ~/.codex.
func CodexDir() string {
	if d := os.Getenv("CODEX_HOME"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex")
}

func stat(f *file) bool {
	fi, err := os.Stat(f.path)
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	f.size, f.mod = fi.Size(), fi.ModTime()
	return true
}

func claudeFiles() []file { return ccFiles("claude", ClaudeDir()) }

// ccFiles are the session files of an agent that keeps them as Claude Code
// does, under its folder's projects/: a session's own <id>.jsonl in its
// project's folder, and its subagents' in <id>/subagents/.
func ccFiles(agent, dir string) []file {
	projects := filepath.Join(dir, "projects")
	var out []file
	mains, _ := filepath.Glob(filepath.Join(projects, "*", "*.jsonl"))
	for _, p := range mains {
		f := file{agent: agent, key: agent + ":" + strings.TrimSuffix(filepath.Base(p), ".jsonl"), path: p, main: true}
		if stat(&f) {
			out = append(out, f)
		}
	}
	subs, _ := filepath.Glob(filepath.Join(projects, "*", "*", "subagents", "*.jsonl"))
	for _, p := range subs {
		f := file{agent: agent, key: agent + ":" + filepath.Base(filepath.Dir(filepath.Dir(p))), path: p}
		if stat(&f) {
			out = append(out, f)
		}
	}
	return out
}

// allFiles are every agent's session files.
func allFiles() []file {
	var out []file
	for _, fs := range [][]file{claudeFiles(), codexFiles(), openCodeFiles(), piFiles(),
		zcodeFiles(), dshFiles(), clineFiles(), ccFiles("qoder", QoderDir("qoder")), ccFiles("qoder-cn", QoderDir("qoder-cn")),
		grokFiles(), workbuddyFiles()} {
		out = append(out, fs...)
	}
	return out
}

// Dirs are the folders the sessions are read from: Claude Code's and
// Codex's, and the other agents' where they keep sessions on this computer.
func Dirs() []string {
	out := []string{ClaudeDir(), CodexDir()}
	for _, d := range []struct{ dir, sessions string }{
		{OpenCodeDir(), OpenCodeDir()},
		{PiDir(), PiDir()},
		{ZCodeDir(), zcodeDB()},
		{DshDir(), filepath.Join(DshDir(), "sessions")},
		{ClineSessionDir(), ClineSessionDir()},
		{QoderDir("qoder"), filepath.Join(QoderDir("qoder"), "projects")},
		{QoderDir("qoder-cn"), filepath.Join(QoderDir("qoder-cn"), "projects")},
		{GrokDir(), filepath.Join(GrokDir(), "sessions")},
		{WorkBuddyDir(), filepath.Join(WorkBuddyDir(), "projects")},
	} {
		if _, err := os.Stat(d.sessions); err == nil {
			out = append(out, d.dir)
		}
	}
	return out
}

// rollout-2026-09-20T15-48-28-<thread id>[_<segment>].jsonl
var rolloutName = regexp.MustCompile(`^rollout-\d{4}-\d{2}-\d{2}T\d{2}-\d{2}-\d{2}-([0-9A-Za-z-]+?)(?:_[0-9A-Za-z-]+)?\.jsonl$`)

func codexFiles() []file {
	var out []file
	root := filepath.Join(CodexDir(), "sessions")
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		m := rolloutName.FindStringSubmatch(d.Name())
		if m == nil {
			return nil
		}
		fi, err := d.Info()
		if err != nil || !fi.Mode().IsRegular() {
			return nil
		}
		out = append(out, file{agent: "codex", key: "codex:" + m[1], path: p, main: true, size: fi.Size(), mod: fi.ModTime()})
		return nil
	})
	return out
}

var (
	mu     sync.Mutex
	cache  map[string]*state // path → parse
	loaded bool
	// the zone the parses in memory date their days in
	cacheZone string
)

// CachePath is where the parses are kept between runs.
func CachePath() string { return filepath.Join(filepath.Dir(catalog.CachePath()), "sessions.json") }

// cacheVersion changes when a parse would come out differently, so the
// parses kept by an older magpie are read again.
// 2: each file's usage by day, and its active time
// 3: the active time by hour of the day
// 4: the tool calls and skills a day
// 5: again, for the prompts and replies a day, which an early 4 left out
const cacheVersion = 5

type cacheFile struct {
	Version int               `json:"version"`
	Zone    string            `json:"zone"` // the time zone its days are dates in
	Files   map[string]*state `json:"files"`
}

// zone tells time zones apart well enough for the days kept by date: their
// offsets in winter and in summer. Moved to another, the files are read again.
func zone() string {
	y := time.Now().Year()
	_, w := time.Date(y, 1, 1, 0, 0, 0, 0, time.Local).Zone()
	_, s := time.Date(y, 7, 1, 0, 0, 0, 0, time.Local).Zone()
	return strconv.Itoa(w) + "/" + strconv.Itoa(s)
}

func loadCache() {
	z := zone()
	if loaded && z == cacheZone {
		return
	}
	loaded, cacheZone = true, z
	cache = map[string]*state{}
	var c cacheFile
	if b, err := os.ReadFile(CachePath()); err == nil && json.Unmarshal(b, &c) == nil && c.Version == cacheVersion && c.Zone == z && c.Files != nil {
		cache = c.Files
	}
}

func saveCache() {
	b, err := json.Marshal(cacheFile{Version: cacheVersion, Zone: zone(), Files: cache})
	if err != nil {
		return
	}
	dir := filepath.Dir(CachePath())
	if os.MkdirAll(dir, 0o755) != nil {
		return
	}
	tmp, err := os.CreateTemp(dir, "sessions-*.json")
	if err != nil {
		return
	}
	_, err = tmp.Write(b)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil || os.Rename(tmp.Name(), CachePath()) != nil {
		os.Remove(tmp.Name())
	}
}

// refresh parses those of want that changed since their parse was kept, a
// few files at a time, and keeps the parses of every file still on disk
// (all of them), read by List or by Stats.
func refresh(want, all []file) {
	var todo []file
	for _, f := range want {
		if s := cache[f.path]; s == nil || s.Size != f.size || s.Mod != f.mod.UnixNano() {
			todo = append(todo, f)
		}
	}
	gone := false
	if len(cache) > 0 {
		on := make(map[string]bool, len(all))
		for _, f := range all {
			on[f.path] = true
		}
		for p := range cache {
			if !on[p] {
				delete(cache, p)
				gone = true
			}
		}
	}
	if len(todo) == 0 {
		if gone {
			saveCache()
		}
		return
	}
	// the biggest first, so no long file is left to run on alone at the
	// end; and twice the cores, as the reading waits on the disk
	sort.Slice(todo, func(i, j int) bool { return todo[i].size-offOf(todo[i]) > todo[j].size-offOf(todo[j]) })
	left := make(map[string]int64, len(todo))
	var total int64
	for _, f := range todo {
		left[f.path] = f.size - offOf(f)
		total += left[f.path]
	}
	progress.start(len(todo), total)
	defer progress.on.Store(false)
	var wg sync.WaitGroup
	var put sync.Mutex
	ch := make(chan file)
	for i := 0; i < min(max(8, 2*runtime.NumCPU()), 32, len(todo)); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for f := range ch {
				put.Lock()
				old := cache[f.path]
				put.Unlock()
				s := parse(f, old)
				progress.files.Add(1)
				progress.read.Add(left[f.path])
				put.Lock()
				cache[f.path] = s
				put.Unlock()
			}
		}()
	}
	for _, f := range todo {
		ch <- f
	}
	close(ch)
	wg.Wait()
	saveCache()
}

// progress is how far the reading of changed files has got, for the page to
// show while it waits: files and bytes read of those to read.
var progress indexing

type indexing struct {
	on          atomic.Bool
	files, read atomic.Int64
	todo, bytes atomic.Int64
}

func (p *indexing) start(n int, total int64) {
	p.files.Store(0)
	p.read.Store(0)
	p.todo.Store(int64(n))
	p.bytes.Store(total)
	p.on.Store(n > 0)
}

// Progress is how far the sessions being read have got; Indexing is false
// when none are.
type Progress struct {
	Indexing bool  `json:"indexing"`
	Files    int64 `json:"files"`
	Done     int64 `json:"done"`
	Bytes    int64 `json:"bytes"`
	Read     int64 `json:"read"`
}

// Indexing is the reading's progress.
func Indexing() Progress {
	p := &progress
	return Progress{Indexing: p.on.Load(), Files: p.todo.Load(), Done: p.files.Load(), Bytes: p.bytes.Load(), Read: min(p.read.Load(), p.bytes.Load())}
}

// offOf is where the parse kept of f left off.
func offOf(f file) int64 {
	if s := cache[f.path]; s != nil && s.Off <= f.size {
		return s.Off
	}
	return 0
}

// pricer looks up the price of each model once.
func pricer() func(string) *catalog.Price {
	prices := map[string]*catalog.Price{}
	return func(model string) *catalog.Price {
		if p, ok := prices[model]; ok {
			return p
		}
		var pp *catalog.Price
		if p, ok := PriceOf(model); ok {
			pp = &p
		}
		prices[model] = pp
		return pp
	}
}

// Reset forgets the kept parses, in memory only.
func Reset() {
	mu.Lock()
	defer mu.Unlock()
	cache, loaded = nil, false
}

// List reads the latest sessions of every agent, the most recently active
// first, at most limit of them (Limit when 0).
func List(limit int) []Session {
	if limit <= 0 {
		limit = Limit
	}
	mu.Lock()
	defer mu.Unlock()
	loadCache()

	defer closeDBs()
	files := allFiles()
	groups := map[string][]file{}
	latest := map[string]time.Time{}
	for _, f := range files {
		groups[f.key] = append(groups[f.key], f)
		if f.mod.After(latest[f.key]) {
			latest[f.key] = f.mod
		}
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if !latest[keys[i]].Equal(latest[keys[j]]) {
			return latest[keys[i]].After(latest[keys[j]])
		}
		return keys[i] < keys[j]
	})
	if len(keys) > limit {
		keys = keys[:limit]
	}
	// every changed file, not just the latest sessions': the first read
	// indexes them all in one run the page can show, and the stats read
	// after it has nothing left to do
	refresh(files, files)

	price := pricer()
	out := []Session{}
	for _, k := range keys {
		if s, ok := assemble(groups[k], price); ok {
			out = append(out, s)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Last.After(out[j].Last) })
	return out
}

// assemble puts a session together from its files' parses.
func assemble(fs []file, price func(string) *catalog.Price) (Session, bool) {
	// the session's own file first; for Codex the earliest segment
	sort.SliceStable(fs, func(i, j int) bool {
		if fs[i].main != fs[j].main {
			return fs[i].main
		}
		return filepath.Base(fs[i].path) < filepath.Base(fs[j].path)
	})
	s := Session{Agent: fs[0].agent, Path: fs[0].path, Models: []Model{}}
	s.ID = strings.TrimPrefix(fs[0].key, s.Agent+":")
	var named, first string
	models := map[string]*Model{}
	for _, f := range fs {
		st := cache[f.path]
		if st == nil {
			continue
		}
		if f.main {
			if s.Cwd == "" {
				s.Cwd = st.Cwd
			}
			if s.Title == "" {
				s.Title = st.Title
			}
			if st.Named != "" {
				named = st.Named
			}
			if first == "" {
				first = st.First
			}
			if (s.Agent == "codex" || s.Agent == "pi" || s.Agent == "dsh" || s.Agent == "grok") && st.ID != "" && f.path == fs[0].path {
				s.ID = st.ID
			}
		}
		if !st.Start.IsZero() && (s.Start.IsZero() || st.Start.Before(s.Start)) {
			s.Start = st.Start
		}
		if st.Last.After(s.Last) {
			s.Last = st.Last
		}
		for name, t := range st.Models {
			m := models[name]
			if m == nil {
				m = &Model{Model: name}
				models[name] = m
			}
			m.add(t)
		}
	}
	if s.Title == "" {
		s.Title = named
	}
	if s.Title == "" {
		s.Title = first
	}
	for _, m := range models {
		if p := price(m.Model); p != nil {
			m.Cost, m.Priced = p.Cost(m.Input, m.Output, m.CacheRead, m.CacheWrite), true
			s.Cost += m.Cost
		} else {
			s.Unpriced++
		}
		s.Tokens.add(m.Tokens)
		s.Models = append(s.Models, *m)
	}
	sort.Slice(s.Models, func(i, j int) bool {
		a, b := s.Models[i], s.Models[j]
		if a.Input+a.Output != b.Input+b.Output {
			return a.Input+a.Output > b.Input+b.Output
		}
		return a.Model < b.Model
	})
	if s.Last.IsZero() {
		s.Last = fs[0].mod
	}
	if s.Title == "" && s.Tokens.zero() {
		return s, false // nothing was said in it
	}
	s.Resume = ResumeCommand(s.Agent, s.ID, s.Cwd)
	return s, true
}

// parse reads a file on from where old left it, or from the start.
func parse(f file, old *state) *state {
	switch f.agent {
	case "opencode", "zcode":
		return parseOpenCode(f)
	case "dsh":
		return parseDsh(f)
	case "cline":
		return parseCline(f)
	case "grok":
		return parseGrok(f)
	}
	var s *state
	if old != nil && f.size >= old.Size && old.Off <= f.size {
		s = old.clone()
	} else {
		s = &state{}
	}
	s.Size, s.Mod = f.size, f.mod.UnixNano()
	line := claudeLine
	switch f.agent {
	case "codex":
		line = codexLine
	case "pi":
		line = piParse
	case "workbuddy":
		line = workbuddyLine
	}
	var head func([]byte) bool
	if f.agent == "codex" {
		// a Codex line is read only when its start says it is wanted
		line = codexBody
		head = func(b []byte) bool { return codexHead(s, b, f.main) }
	}
	off, err := scanHead(f.path, s.Off, head, func(b []byte) { line(s, b, f.main) })
	if err == nil {
		s.Off = off
	}
	if f.agent == "workbuddy" && s.Cwd == "" {
		workbuddyMeta(s, f.path)
	}
	return s
}

// maxLine is the longest line looked at; a longer one (an image pasted
// inline) is stepped over.
const maxLine = 32 << 20

// scan calls fn on each whole line of the file from off, and returns the
// offset after the last one. A line still being written is left for later.
func scan(path string, off int64, fn func([]byte)) (int64, error) {
	return scanHead(path, off, nil, fn)
}

// scanHead is scan with a look at each line's start first: head is given
// the line, or its first megabyte when it is longer, and a line it says no
// to is stepped over without being gathered (most of a Codex rollout's
// bytes are lines no one reads: compactions, tool output). A nil head
// wants every line.
func scanHead(path string, off int64, head func([]byte) bool, fn func([]byte)) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return off, err
	}
	defer f.Close()
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return off, err
	}
	r := bufio.NewReaderSize(f, 1<<20)
	var long []byte
	var n int64 // bytes of the line so far
	skip, seen := false, false
	for {
		chunk, err := r.ReadSlice('\n')
		n += int64(len(chunk))
		if errors.Is(err, bufio.ErrBufferFull) {
			if !seen && head != nil {
				seen = true
				skip = skip || !head(chunk)
			}
			if !skip && len(long)+len(chunk) <= maxLine {
				long = append(long, chunk...)
			} else {
				skip, long = true, long[:0]
			}
			continue
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return off, nil
			}
			return off, err
		}
		b := chunk
		if len(long) > 0 {
			long = append(long, chunk...)
			b = long
		}
		if !skip {
			if b = bytes.TrimSpace(b); len(b) > 0 && (seen || head == nil || head(b)) {
				fn(b)
			}
		}
		off += n
		n, skip, seen, long = 0, false, false, long[:0]
	}
}

// tsAt reads the time of a line from its first (or last) "timestamp" key,
// without decoding the rest of it.
func tsAt(b []byte, last bool) time.Time {
	key := []byte(`"timestamp":"`)
	i := bytes.Index(b, key)
	if last {
		i = bytes.LastIndex(b, key)
	}
	if i < 0 {
		return time.Time{}
	}
	rest := b[i+len(key):]
	j := bytes.IndexByte(rest, '"')
	if j < 0 || j > 40 {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339Nano, string(rest[:j]))
	return t
}

var (
	space = regexp.MustCompile(`\s+`)
	tag   = regexp.MustCompile(`</?[A-Za-z][\w-]*[^<>]*>`)
)

// untagged is a message the agent or a harness wrapped in tags, as words.
func untagged(s string) string { return title(tag.ReplaceAllString(s, " ")) }

// title is a prompt made one short line.
func title(s string) string {
	s = strings.TrimSpace(space.ReplaceAllString(s, " "))
	if r := []rune(s); len(r) > 160 {
		s = strings.TrimSpace(string(r[:160])) + "…"
	}
	return s
}

// ---- pricing ----------------------------------------------------------------

var dated = regexp.MustCompile(`-\d{8}$`)

// priceOf prices a model as a session names it: one through magpie as
// "<provider>/<model>" at the price the gateway counts it at, else the bare
// id at its maker's list price on models.dev.
func priceOf(model string) (catalog.Price, bool) {
	m := strings.TrimSpace(model)
	if m == "" {
		return catalog.Price{}, false
	}
	if pid, rest, ok := strings.Cut(m, "/"); ok {
		for _, p := range provider.All() {
			if p.ID == pid {
				for _, c := range p.Catalogs() {
					if pr, ok := catalog.PriceOf(c, rest); ok {
						return pr, true
					}
				}
				break
			}
		}
	}
	bare := strings.ToLower(m[strings.LastIndexByte(m, '/')+1:])
	for _, id := range []string{bare, dated.ReplaceAllString(bare, "")} {
		for _, c := range makers(id) {
			if pr, ok := catalog.PriceOf(c, id); ok {
				return pr, true
			}
		}
	}
	return catalog.Price{}, false
}

// makers are the models.dev providers that make a model of this id.
func makers(id string) []string {
	for _, m := range []struct{ prefix, provider string }{
		{"claude", "anthropic"}, {"gpt", "openai"}, {"o1", "openai"}, {"o3", "openai"}, {"o4", "openai"},
		{"codex", "openai"}, {"gemini", "google"}, {"deepseek", "deepseek"}, {"grok", "xai"},
		{"glm", "zai"}, {"kimi", "moonshotai"}, {"qwen", "alibaba"}, {"mistral", "mistral"},
		{"devstral", "mistral"}, {"minimax", "minimax"},
	} {
		if strings.HasPrefix(id, m.prefix) {
			return []string{m.provider}
		}
	}
	return nil
}

// ---- resuming ---------------------------------------------------------------

var safeID = regexp.MustCompile(`^[0-9A-Za-z_-]+$`)

// ResumeCommand is the shell line that picks a session up again in its
// folder, or "" for an id that isn't plain.
func ResumeCommand(agent, id, cwd string) string {
	if !safeID.MatchString(id) {
		return ""
	}
	var run string
	switch agent {
	case "claude":
		run = "claude --resume " + id
	case "codex":
		run = "codex resume " + id
	case "opencode":
		run = "opencode --session " + id
	case "pi":
		run = "pi --session " + id
	case "cline":
		run = "cline --id " + id
	case "qoder":
		run = "qodercli --resume " + id
	case "qoder-cn":
		run = "qoderclicn --resume " + id
	case "grok":
		run = "grok --resume " + id
	default:
		return ""
	}
	if cwd == "" {
		return run
	}
	if runtime.GOOS == "windows" {
		return "Set-Location -LiteralPath '" + strings.ReplaceAll(cwd, "'", "''") + "'; " + run
	}
	return "cd " + shellQuote(cwd) + " && " + run
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// Find is a session by agent and id: a listed one, else one Get finds
// under agent:id.
func Find(agent, id string) (Session, bool) {
	for _, s := range List(0) {
		if s.Agent == agent && s.ID == id {
			return s, true
		}
	}
	if s, ok := Get(agent + ":" + id); ok && s.ID == id {
		return s, true
	}
	return Session{}, false
}

// Get is the session whose files are grouped under key (a Summary's Key),
// however long ago it was at work.
func Get(key string) (Session, bool) {
	mu.Lock()
	defer mu.Unlock()
	loadCache()
	defer closeDBs()
	files := allFiles()
	var fs []file
	for _, f := range files {
		if f.key == key {
			fs = append(fs, f)
		}
	}
	if len(fs) == 0 {
		return Session{}, false
	}
	refresh(fs, files)
	return assemble(fs, pricer())
}
