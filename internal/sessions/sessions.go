// Package sessions lists the agents' recent sessions from their own session
// files — Claude Code's projects/*/<id>.jsonl (and Qoder's, the same kind),
// Codex's rollout files, OpenCode's database (or its older JSON files) and
// ZCode's, Pi's session files and omp's, DeepSeek Harness's, Cline's, Grok
// Build's, WorkBuddy's, Droid's, Cursor CLI's and Alma's chat stores — with the tokens each spent, what that cost at the
// effective price, and the command that resumes it. It only ever reads the
// agents' folders.
//
// The files grow long (hundreds of MB), so each one's parse is kept by path,
// size and time, and a file that only grew is read on from where it was left.
// The parses are kept on disk too, in magpie's cache folder.
package sessions

import (
	"bufio"
	"bytes"
	"cmp"
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

	"github.com/klauspost/compress/zstd"
	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// Tokens is a count of tokens. Input excludes what was read from cache.
type Tokens struct {
	Input      int `json:"input"`
	Output     int `json:"output"`
	CacheRead  int `json:"cache_read"`
	CacheWrite int `json:"cache_write"`
	// CacheWrite1h is how many of CacheWrite were written to be kept for an
	// hour, where the agent says (Claude Code's cache_creation)
	CacheWrite1h int `json:"cache_write_1h,omitempty"`
}

func (t *Tokens) add(u Tokens) {
	t.Input += u.Input
	t.Output += u.Output
	t.CacheRead += u.CacheRead
	t.CacheWrite += u.CacheWrite
	t.CacheWrite1h += u.CacheWrite1h
}

func (t *Tokens) sub(u Tokens) {
	t.Input -= u.Input
	t.Output -= u.Output
	t.CacheRead -= u.CacheRead
	t.CacheWrite -= u.CacheWrite
	t.CacheWrite1h -= u.CacheWrite1h
}

// ccCacheCreation is Claude Code's cache writes split by how long they are
// kept.
type ccCacheCreation struct {
	Ephemeral5m int `json:"ephemeral_5m_input_tokens"`
	Ephemeral1h int `json:"ephemeral_1h_input_tokens"`
}

// ccTokens is a Claude Code message's usage, its 1-hour cache writes apart.
func ccTokens(in, out, read, write int, c *ccCacheCreation) Tokens {
	t := Tokens{Input: in, Output: out, CacheRead: read, CacheWrite: write}
	if c != nil {
		t.CacheWrite = max(t.CacheWrite, c.Ephemeral5m+c.Ephemeral1h)
		t.CacheWrite1h = c.Ephemeral1h
	}
	return t
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
	ReadOnly bool      `json:"read_only,omitempty"`
	Agent    string    `json:"agent"` // magpie agent id: claude, codex, opencode, pi, omp, zcode, dsh, cline, qoder, qoder-cn, grok, workbuddy, droid, cursor, hermes, alma
	ID       string    `json:"id"`
	Cwd      string    `json:"cwd"`
	Title    string    `json:"title"` // the name it was given, else the agent's own title, else the first prompt
	Start    time.Time `json:"start"`
	Last     time.Time `json:"last"`
	Models   []Model   `json:"models"`
	Tokens
	Cost     float64 `json:"cost"`     // USD at the effective price, for the priced models
	Unpriced int     `json:"unpriced"` // models that spent tokens but have no known price
	Resume   string  `json:"resume"`   // the command that picks the session up again
	Path     string  `json:"path"`     // its (main) file
	// Carry are the other agents that can carry the session on, reading its
	// file as it is (carry.go)
	Carry []Carry `json:"carry,omitempty"`
	// Transcript says its conversation can be read (TranscriptOf)
	Transcript bool `json:"transcript,omitempty"`
	// UsageIncomplete means native history cannot establish a complete total.
	UsageIncomplete bool `json:"usage_incomplete,omitempty"`
	// WSL is the WSL distro the session ran in, its files read through
	// \\wsl.localhost (see wsl.go); "" for this computer's own
	WSL string `json:"wsl,omitempty"`
}

// PriceOf is the effective price of a model as a session names it, at the
// settings given. Tests swap it; the settings are the ones the listing read,
// so a swapped one prices against the same copy as the default does.
var PriceOf = priceOf

// Limit is how many sessions, the latest by last activity, List reads when
// the caller asks for no particular number. traceRecentFiles sizes the
// window gateway attribution is judged over by it too, so it is not raised
// to make a listing longer: a caller that wants more asks for more.
const Limit = 200

// All is what a listing that means every session asks List for: the count a
// page or a command shows beside the list is taken from every session, so
// the list has to be as long as the count, and this is long past any history
// a computer has. Limit stays what it is because traceRecentFiles sizes
// gateway attribution by it.
const All = 5000

// state is what one file's parse has come to, enough to read on from Off.
type state struct {
	Codex       *codexUsageState  `json:"-"`
	Claude      *claudeUsageState `json:"-"`
	DBRevision  string            `json:"db_revision,omitempty"`
	Head        string            `json:"head,omitempty"`
	HeadSize    int               `json:"head_size,omitempty"`
	ContentHash string            `json:"content_hash,omitempty"`
	Size        int64             `json:"size"`
	Mod         int64             `json:"mod"` // unix nanoseconds
	Off         int64             `json:"off"` // after the last whole line read
	ID          string            `json:"id,omitempty"`
	Cwd         string            `json:"cwd,omitempty"`
	Title       string            `json:"title,omitempty"`
	Named       string            `json:"named,omitempty"`  // the agent's own title for it
	Custom      string            `json:"custom,omitempty"` // the name the user gave it
	First       string            `json:"first,omitempty"`  // the first message, when no prompt looked typed
	Start       time.Time         `json:"start"`
	Last        time.Time         `json:"last"`
	Models      map[string]Tokens `json:"models,omitempty"`
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
	// Codex: the model_provider its session_meta names (codex_provider.go)
	Provider string `json:"provider,omitempty"`
	// Reasonix's native turn ledger uses a sequence across all turns.
	ReasonixAt      int64  `json:"reasonix_at,omitempty"`
	ReasonixCwd     string `json:"reasonix_cwd,omitempty"`
	ReasonixSeq     uint64 `json:"reasonix_seq,omitempty"`
	UsageIncomplete bool   `json:"usage_incomplete,omitempty"`
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
	// the agent's messages back (Claude Code's, Codex's, Pi's and omp's)
	Prompts int `json:"u,omitempty"`
	Replies int `json:"r,omitempty"`
	// Tools are the tool calls made, by the tool's name, and Skills the
	// skills called up, by the skill's (subagents' files included)
	Tools  map[string]int `json:"t,omitempty"`
	Skills map[string]int `json:"k,omitempty"`
}

// tool counts a call of a tool, and of a skill when it calls one up; a
// skill called up with no tool (Pi's /skill:name) has no name.
func (s *state) tool(at time.Time, name, skill string) {
	if name == "" && skill == "" {
		return
	}
	d := s.day(dateOf(at))
	if name != "" {
		if d.Tools == nil {
			d.Tools = map[string]int{}
		}
		d.Tools[name]++
	}
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
	c.Codex = s.Codex.clone()
	c.Claude = s.Claude.clone()
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
	// summary.json, beside its updates; Droid: its settings, beside its
	// transcript
	manifest string
	hermes   *hermesDB
	// Alma: its database, and what tells the chat's rows changed
	alma     *almaStore
	rev      string
	readOnly bool
	// wsl: the WSL distro whose home it is in; cold: that distro isn't
	// running, and the file, as last listed, is not to be opened (that
	// would start the distro)
	wsl  string
	cold bool
}

// ClaudeDir is Claude Code's folder: $CLAUDE_CONFIG_DIR, else ~/.claude.
func ClaudeDir() string {
	if d := appdir.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}

// CodexDir is Codex's folder: $CODEX_HOME, else ~/.codex.
func CodexDir() string {
	if d := appdir.Getenv("CODEX_HOME"); d != "" {
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

// ccFiles are the session files of an agent that keeps them as Claude Code
// does, under its folder's projects/: a session's own <id>.jsonl in its
// project's folder, its subagents' in <id>/subagents/, and the agents of
// its workflows (ultracode) in <id>/subagents/workflows/<run>/agent-*.jsonl
// — beside each run's journal.jsonl, which isn't a transcript.
func ccFiles(agent, dir string) []file {
	projects := filepath.Join(dir, "projects")
	var out []file
	for _, project := range readDirectory(projects) {
		if !project.IsDir() && project.Type()&os.ModeSymlink == 0 {
			continue
		}
		root := filepath.Join(projects, project.Name())
		for _, e := range readDirectory(root) {
			path := filepath.Join(root, e.Name())
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
				f := file{agent: agent, key: agent + ":" + strings.TrimSuffix(e.Name(), ".jsonl"), path: path, main: true}
				if stat(&f) {
					out = append(out, f)
				}
			} else if e.IsDir() || e.Type()&os.ModeSymlink != 0 {
				subs := filepath.Join(path, "subagents")
				for _, sub := range readDirectory(subs) {
					if sub.IsDir() || !strings.HasSuffix(sub.Name(), ".jsonl") {
						continue
					}
					f := file{agent: agent, key: agent + ":" + e.Name(), path: filepath.Join(subs, sub.Name())}
					if stat(&f) {
						out = append(out, f)
					}
				}
				for _, run := range readDirectory(filepath.Join(subs, "workflows")) {
					if !run.IsDir() {
						continue
					}
					dir := filepath.Join(subs, "workflows", run.Name())
					for _, sub := range readDirectory(dir) {
						if sub.IsDir() || !strings.HasPrefix(sub.Name(), "agent-") || !strings.HasSuffix(sub.Name(), ".jsonl") {
							continue
						}
						f := file{agent: agent, key: agent + ":" + e.Name(), path: filepath.Join(dir, sub.Name())}
						if stat(&f) {
							out = append(out, f)
						}
					}
				}
			}
		}
	}
	return out
}

// allFiles are every agent's session files.
func allFiles() []file {
	var out []file
	for _, fs := range [][]file{callFiles(), openCodeFiles(), piFiles(),
		wslFiles("pi"), zcodeFiles(), dshFiles(), clineFiles(), ccFiles("qoder", QoderDir("qoder")), ccFiles("qoder-cn", QoderDir("qoder-cn")),
		grokFiles(), workbuddyFiles(), droidFiles(), ompFiles(), cursorFiles(), hermesFiles(), almaFiles(), reasonixFiles()} {
		out = append(out, fs...)
	}
	return out
}

// Dirs are the folders the sessions are read from: Claude Code's and
// Codex's, and the other agents' where they keep sessions on this computer.
func Dirs() []string {
	out := []string{ClaudeDir(), CodexDir()}
	if len(reasonixSessionDirs()) != 0 || len(reasonixStoreDirs()) != 0 {
		out = append(out, ReasonixDir())
	}
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
		{OmpDir(), filepath.Join(OmpDir(), "sessions")},
		{FactoryDir(), filepath.Join(FactoryDir(), "sessions")},
		{CursorDir(), filepath.Join(CursorDir(), "chats")},
		{AlmaDir(), almaDB()},
	} {
		if _, err := os.Stat(d.sessions); err == nil {
			out = append(out, d.dir)
		}
	}
	out = append(out, HermesDirs()...)
	out = append(out, wslDirs()...)
	return out
}

// rollout-2026-09-20T15-48-28-<thread id>[_<segment>].jsonl, and .jsonl.zst
// once the Codex app's "compress local chat history" has packed an older one
var rolloutName = regexp.MustCompile(`^rollout-\d{4}-\d{2}-\d{2}T\d{2}-\d{2}-\d{2}-([0-9A-Za-z-]+?)(?:_[0-9A-Za-z-]+)?\.jsonl(?:\.zst)?$`)

// zstSuffix ends a rollout the Codex app has compressed.
const zstSuffix = ".zst"

func codexFiles() []file { return codexFilesIn(CodexDir()) }

// codexFilesIn are the rollouts in a Codex folder.
func codexFilesIn(dir string) []file {
	var out []file
	for _, folder := range []string{"sessions", "archived_sessions"} {
		root := filepath.Join(dir, folder)
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
	}
	// During compression, prefer the readable plain twin in the same
	// directory: the new compressed file may not have its trailer yet.
	plain := map[string]bool{}
	for _, f := range out {
		if !strings.HasSuffix(f.path, zstSuffix) {
			plain[f.path] = true
		}
	}
	kept := out[:0]
	for _, f := range out {
		if q, ok := strings.CutSuffix(f.path, zstSuffix); !ok || !plain[q] {
			kept = append(kept, f)
		}
	}
	return distinctCodexFiles(kept)
}

// rolloutTwin is a Codex rollout's file in its other form: the compressed
// one of a plain one, the plain one of a compressed one.
func rolloutTwin(p string) string {
	if q, ok := strings.CutSuffix(p, zstSuffix); ok {
		return q
	}
	return p + zstSuffix
}

var (
	// DB readers share connection lifetimes; call-file readers need only mu.
	dbReadMu        sync.Mutex
	mu              sync.Mutex
	cache           map[string]*state // path → parse
	loaded          bool
	cacheGeneration uint64
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
// 6: per-call metadata and continuation share the session file scan
// 7: Codex's recorded provider and creator identity
// 8: keep only summaries here; request metadata has per-file shards.
// 9: validate the previous full prefix before treating growth as an append.
// 10: Pi's and omp's prompts, replies, tool calls and skills.
// 11: Codex's input without what it wrote to the cache (#589).
// 16: count Codex response records and compaction usage.
// 17: reconcile recent Claude message revisions.
// 18: Claude Code's 1-hour cache writes apart from its 5-minute ones.
// 19: the names the user gave Claude Code's and WorkBuddy's (custom-title).
const cacheVersion = 19

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
	cacheGeneration++
	cache = map[string]*state{}
	saved() // the kept file as the last save left it
	var c cacheFile
	if b, err := os.ReadFile(CachePath()); err == nil && json.Unmarshal(b, &c) == nil && c.Version == cacheVersion && c.Zone == z && c.Files != nil {
		cache = c.Files
	}
}

// saveCache keeps the parses on disk. Writing the whole index (tens of MB
// for a long history) takes a second or more, so it is done behind the
// request: the parses in the cache are never changed once put there (a new
// parse starts from a clone), so a copy of the map is a snapshot; saves in a
// row are folded into the latest.
func saveCache() {
	snap := save{cacheFile{Version: cacheVersion, Zone: zone(), Files: maps.Clone(cache)}, CachePath()}
	saving.Lock()
	defer saving.Unlock()
	saving.next = &snap
	if saving.running {
		return
	}
	saving.running = true
	go func() {
		for {
			saving.Lock()
			f := saving.next
			saving.next = nil
			if f == nil {
				saving.running = false
				saving.Broadcast()
				saving.Unlock()
				return
			}
			saving.Unlock()
			writeCache(f)
		}
	}()
}

var saving struct {
	sync.Mutex
	*sync.Cond
	running bool
	next    *save
}

type save struct {
	cacheFile
	path string
}

func init() { saving.Cond = sync.NewCond(&saving.Mutex) }

// Saved waits for the index being saved, so a process about to end leaves
// it on disk.
func Saved() { saved() }

// saved waits for the saves under way.
func saved() {
	saving.Lock()
	for saving.running {
		saving.Wait()
	}
	saving.Unlock()
}

// saveHook, when a test sets it, runs before each save is written.
var saveHook func()

func writeCache(c *save) {
	if saveHook != nil {
		saveHook()
	}
	b, err := json.Marshal(c.cacheFile)
	if err != nil {
		return
	}
	dir := filepath.Dir(c.path)
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
	if err != nil || os.Rename(tmp.Name(), c.path) != nil {
		os.Remove(tmp.Name())
	}
}

// refresh parses those of want that changed since their parse was kept, a
// few files at a time, and keeps the parses of every file still on disk
// (all of them), read by List or by Stats.
func refresh(want, all []file) {
	defer func() { trimSummaryRevisions(time.Now()) }()
	var todo []file
	for _, f := range want {
		if f.cold {
			continue // in a stopped WSL distro: what was read of it stands
		}
		if s := cache[f.path]; s == nil || s.Size != f.size || s.Mod != f.mod.UnixNano() || (f.agent == "hermes" && (f.hermes == nil || f.hermes.revision == "" || s.DBRevision != f.hermes.revision)) || ((f.agent == "alma" || f.agent == "reasonix") && s.DBRevision != f.rev) || (f.agent == "reasonix" && s.ID != f.sid) {
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
	trimSummaryRevisions(time.Now())
	if len(todo) == 0 {
		if gone {
			saveCache()
		}
		return
	}
	// Limit cold-scan workers to the windows that can survive publication.
	// Otherwise a batch of historical files could retain every window until
	// the whole scan finishes.
	windows := summaryRevisionWindows(todo, time.Now())
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
	type job struct {
		f      file
		old    *state
		parsed *state
	}
	jobs := make([]job, len(todo))
	for i, f := range todo {
		jobs[i] = job{f: f, old: cache[f.path]}
	}
	generation := cacheGeneration
	mu.Unlock()
	var wg sync.WaitGroup
	ch := make(chan int)
	for i := 0; i < min(max(8, 2*runtime.NumCPU()), 32, len(todo)); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range ch {
				j := &jobs[i]
				j.parsed = parse(j.f, j.old)
				if (j.parsed.Claude != nil && !windows[j.f.path]) || (j.parsed.Claude == nil && !recentRevision(j.parsed.Mod, j.parsed.revisionWeight(), time.Now())) {
					j.parsed = j.parsed.withoutRevisions()
				}
				progress.files.Add(1)
				progress.read.Add(left[j.f.path])
			}
		}()
	}
	for i := range jobs {
		ch <- i
	}
	close(ch)
	wg.Wait()
	mu.Lock()
	for _, j := range jobs {
		// A concurrent read may already have published a newer parse.
		if generation == cacheGeneration && cache[j.f.path] == j.old {
			cache[j.f.path] = j.parsed
		}
	}
	progress.finish()
	if generation == cacheGeneration {
		saveCache()
	}
}

// progress is how far the reading of changed files has got, for the page to
// show while it waits: files and bytes read of those to read.
var progress indexing

type indexing struct {
	guard       sync.Mutex
	active      int
	on          atomic.Bool
	files, read atomic.Int64
	todo, bytes atomic.Int64
}

func (p *indexing) start(n int, total int64) {
	p.guard.Lock()
	defer p.guard.Unlock()
	if p.active == 0 {
		p.files.Store(0)
		p.read.Store(0)
		p.todo.Store(int64(n))
		p.bytes.Store(total)
	} else {
		p.todo.Add(int64(n))
		p.bytes.Add(total)
	}
	p.active++
	p.on.Store(true)
}

func (p *indexing) finish() {
	p.guard.Lock()
	defer p.guard.Unlock()
	p.active--
	p.on.Store(p.active > 0)
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

// pricer looks up the price of each model once, out of one read of the
// settings: a listing names the same handful of models over and over, and
// each of them otherwise read the file and parsed it again. Every model is
// priced against the copy read here, so a listing is one snapshot of the
// prices rather than a reading per row, and a price changed while it is
// read takes effect in the next listing rather than halfway through this
// one.
func pricer() func(string) *catalog.Price {
	prices := map[string]*catalog.Price{}
	s := settings.Load()
	return func(model string) *catalog.Price {
		if p, ok := prices[model]; ok {
			return p
		}
		var pp *catalog.Price
		if p, ok := PriceOf(s, model); ok {
			pp = &p
		}
		prices[model] = pp
		return pp
	}
}

// Reset forgets the kept parses, in memory only.
func Reset() {
	directoryCache.Lock()
	directoryCache.entries = map[string]directoryEntry{}
	directoryCache.Unlock()
	resetCalls()
	wslReset()
	mu.Lock()
	defer mu.Unlock()
	saved()
	cache, loaded = nil, false
	cacheGeneration++
}

// List reads the latest sessions of every agent, the most recently active
// first, at most limit of them (Limit when 0).
func List(limit int) []Session {
	dbReadMu.Lock()
	defer dbReadMu.Unlock()
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
	// the keys are not truncated yet: assemble below drops an empty
	// session, and a nonempty one must take its place rather than the
	// page going short - an empty file is not a session (#1320)
	// every changed file, not just the latest sessions': the first read
	// indexes them all in one run the page can show, and the stats read
	// after it has nothing left to do
	refresh(files, files)

	price := pricer()
	out := []Session{}
	for _, k := range keys {
		if s, ok := assemble(groups[k], price); ok {
			out = append(out, s)
			if len(out) == limit {
				break
			}
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
	s := Session{Agent: fs[0].agent, Path: fs[0].path, Models: []Model{}, WSL: fs[0].wsl}
	for _, f := range fs {
		s.ReadOnly = s.ReadOnly || f.readOnly
	}
	s.ID = strings.TrimPrefix(fs[0].key, s.Agent+":")
	var custom, named, first string
	models := map[string]*Model{}
	for _, f := range fs {
		st := cache[f.path]
		if st == nil {
			continue
		}
		s.UsageIncomplete = s.UsageIncomplete || st.UsageIncomplete
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
			if st.Custom != "" {
				custom = st.Custom
			}
			if first == "" {
				first = st.First
			}
			if (s.Agent == "codex" || s.Agent == "pi" || s.Agent == "omp" || s.Agent == "dsh" || s.Agent == "grok" || s.Agent == "droid") && st.ID != "" && f.path == fs[0].path {
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
	// the name the session goes by in its agent: one the user gave it, else
	// Codex's thread name (session_index.jsonl), else the title the agent
	// made, else the first prompt, else the first message
	if s.Agent == "codex" && s.WSL == "" && custom == "" {
		custom = CodexTitles([]string{s.ID})[s.ID]
	}
	s.Title = cmp.Or(custom, named, s.Title, first)
	for _, m := range models {
		if p := price(m.Model); p != nil {
			// a sum of the session's calls of the model, at its base price:
			// which call went over a long-context tier isn't known here
			m.Cost, m.Priced = p.At(0).CostSplit(m.Input, m.Output, m.CacheRead, m.CacheWrite, m.CacheWrite1h), true
			s.Cost += m.Cost
		} else if !m.Tokens.zero() {
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
	if !s.ReadOnly {
		s.Resume = resumeCommand(s.WSL, s.Agent, s.ID, s.Cwd)
		if s.Agent == "reasonix" && runtime.GOOS != "windows" {
			p := s.Path
			if filepath.Base(p) == "events.frames" || filepath.Base(p) == "events.jsonl" {
				p = s.ID
			}
			s.Resume = "reasonix --resume " + shellQuote(p)
		}
	}
	s.Carry, s.Transcript = carries(s), HasTranscript(s.Agent)
	return s, true
}

// parserFor is the source of truth for both parsing and window admission.
// A default line reader is Claude-compatible, including both Qoder variants.
type sessionParser struct {
	whole  func(file) *state
	line   func(*state, []byte, bool)
	claude bool
}

func parserFor(agent string) sessionParser {
	switch agent {
	case "hermes":
		return sessionParser{whole: parseHermes}
	case "alma":
		return sessionParser{whole: parseAlma}
	case "opencode", "zcode":
		return sessionParser{whole: parseOpenCode}
	case "dsh":
		return sessionParser{whole: parseDsh}
	case "cline":
		return sessionParser{whole: parseCline}
	case "grok":
		return sessionParser{whole: parseGrok}
	case "droid":
		return sessionParser{whole: parseDroid}
	case "cursor":
		return sessionParser{whole: parseCursor}
	case "codex":
		return sessionParser{line: codexBody}
	case "reasonix":
		return sessionParser{line: reasonixLine}
	case "pi", "omp":
		return sessionParser{line: piParse}
	case "workbuddy":
		return sessionParser{line: workbuddyLine}
	default:
		return sessionParser{line: claudeLine, claude: true}
	}
}

// parse reads a file on from where old left it, or from the start.
func parse(f file, old *state) *state {
	if f.agent == "reasonix" && strings.HasPrefix(f.rev, reasonixStoreRevision) {
		return parseReasonixStore(f)
	}
	if f.agent == "reasonix" && strings.HasSuffix(f.path, ".wire.jsonl") {
		return parseReasonixWire(f)
	}
	if f.agent == "reasonix" && old != nil && (old.ID != f.sid || f.main && !strings.HasPrefix(old.DBRevision, reasonixRevision)) {
		old = nil
	}
	// Summary caches contain aggregates only. After a restart, a changed
	// Codex file rebuilds its transient response index from the source.
	if f.agent == "codex" && old != nil && old.Codex == nil {
		old = nil
	}
	parser := parserFor(f.agent)
	if parser.whole != nil {
		return parser.whole(f)
	}
	// Every source using claudeLine must rebuild a missing transient window.
	if parser.claude && old != nil && old.Claude == nil {
		old = nil
	}

	headBytes := headOf(f.path)
	var s *state
	if old != nil && !packed(f.path) && f.size >= old.Size && old.Off <= f.size && sameHead(headBytes, old.Head, old.HeadSize) && old.ContentHash != "" && prefixHash(f.path, old.Size) == old.ContentHash {
		s = old.clone()
	} else {
		s = &state{}
	}
	s.Size, s.Mod = f.size, f.mod.UnixNano()
	s.Head, s.HeadSize = hashHead(headBytes), len(headBytes)
	s.ContentHash = prefixHash(f.path, f.size)
	var head func([]byte) bool
	if f.agent == "codex" {
		head = func(b []byte) bool { return codexHead(s, b, f.main) }
	}
	if f.agent == "reasonix" {
		s.ID = f.sid
		if !f.main {
			head = reasonixUsageHead
		}
	}
	off, err := scanAt(f.path, s.Off, head, func(b []byte, _, _ int64) bool {
		parser.line(s, b, f.main)
		return true
	})
	if err == nil {
		s.Off = off
	}
	if f.agent == "workbuddy" && s.Cwd == "" {
		workbuddyMeta(s, f.path)
	}
	if f.agent == "reasonix" {
		reasonixMetadata(s, f)
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
	return scanAt(path, off, head, func(b []byte, _, _ int64) bool { fn(b); return true })
}

// scanAt is scanHead telling fn where each line starts and ends in the file,
// and stopping when fn says so; what it returns is where the last line it
// handled ended.
// A compressed file's places are those of its lines decompressed; it is read
// from its start, what comes before off passed over.
func scanAt(path string, off int64, head func([]byte) bool, fn func(b []byte, start, end int64) bool) (int64, error) {
	f, err := openLines(path)
	if err != nil {
		return off, err
	}
	defer f.Close()
	if s, ok := f.(io.Seeker); ok {
		if _, err := s.Seek(off, io.SeekStart); err != nil {
			return off, err
		}
	} else if _, err := io.CopyN(io.Discard, f, off); err != nil {
		if errors.Is(err, io.EOF) {
			return off, nil
		}
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
				if !fn(b, off, off+n) {
					return off + n, nil
				}
			}
		}
		off += n
		n, skip, seen, long = 0, false, false, long[:0]
	}
}

// packed says whether a session file is compressed: a Codex rollout the
// Codex app has packed (.zst), a dsh session (.zstd). A packed rollout is
// written whole, so it is read whole again whenever it changes.
func packed(path string) bool {
	return strings.HasSuffix(path, zstSuffix) || strings.HasSuffix(path, ".zstd")
}

// openLines opens a session file to read its lines, a packed one decompressed.
func openLines(path string) (io.ReadCloser, error) {
	f, err := os.Open(path)
	if err != nil || !packed(path) {
		return f, err
	}
	d, err := zstd.NewReader(f, zstd.WithDecoderConcurrency(1), zstd.WithDecoderLowmem(true))
	if err != nil {
		f.Close()
		return nil, err
	}
	return zstdFile{d.IOReadCloser(), f}, nil
}

// zstdFile is a packed file's lines, closed with the file.
type zstdFile struct {
	io.ReadCloser
	f *os.File
}

func (z zstdFile) Close() error {
	z.ReadCloser.Close()
	return z.f.Close()
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
// "<provider>/<model>" at the price the gateway counts it at — what the user
// set for that provider and model, else that provider's own list price, else
// its maker's on models.dev — and a bare id only ever at its maker's, which is
// a different question from what one provider charges.
//
// The settings are the ones passed in rather than read here: a listing reads
// them once and prices every model of it against that same copy, so a
// listing is one snapshot of the prices and reads the file once, not once
// per model.
func priceOf(s settings.Settings, model string) (catalog.Price, bool) {
	m := strings.TrimSpace(model)
	if m == "" {
		return catalog.Price{}, false
	}
	if pid, rest, ok := strings.Cut(m, "/"); ok {
		if pr, ok := provider.EffectivePriceIn(s, pid, rest); ok {
			return pr, true
		}
	}
	bare := strings.ToLower(m[strings.LastIndexByte(m, '/')+1:])
	// what the user said the model costs from any provider (*/model): the
	// only price a bare id, or one models.dev has stopped listing, can take
	// from them
	if p, ok := s.ModelPrices[provider.AnyPriceKey(bare)]; ok {
		if pr, bad := p.Price(); bad == "" {
			catalog.OneHourFor(bare, &pr)
			return pr, true
		}
	}
	// a Grok id at an effort (grok-4.7-high) at its model's price, and a
	// version spelled with a dot (Copilot's claude-opus-4.6) at its maker's
	// claude-opus-4-6
	for _, id := range []string{bare, dated.ReplaceAllString(bare, ""), provider.PricedName(bare)} {
		if pr, ok := catalog.PricedBy(makers(id), id); ok {
			return pr, true
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
func ResumeCommand(agent, id, cwd string) string { return resumeCommand("", agent, id, cwd) }

// resumeCommand is ResumeCommand for a session in a WSL distro too, when
// distro is set: see wslResume.
func resumeCommand(distro, agent, id, cwd string) string {
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
	case "omp":
		run = "omp --resume " + id
	case "droid":
		run = "droid --resume " + id
	case "cursor":
		// its chats are looked up by the folder it runs in
		if cwd == "" {
			return ""
		}
		run = "cursor-agent --resume " + id
	default:
		return ""
	}
	if distro != "" {
		return wslResume(distro, cwd, run)
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

// Titles is the title of each session named by key (agent:id, as Get takes
// it) that has its files on this computer: the name it was given, else the
// agent's own title, else its first prompt. A key with no files, or a
// session with no title, is left out. The Routing page names its sessions
// with it (#1293).
func Titles(keys []string) map[string]string {
	out := map[string]string{}
	if len(keys) == 0 {
		return out
	}
	want := make(map[string]bool, len(keys))
	for _, k := range keys {
		want[k] = true
	}
	mu.Lock()
	defer mu.Unlock()
	loadCache()
	defer closeDBs()
	files := allFiles()
	groups := map[string][]file{}
	var fs []file
	for _, f := range files {
		if want[f.key] {
			groups[f.key] = append(groups[f.key], f)
			fs = append(fs, f)
		}
	}
	if len(fs) == 0 {
		return out
	}
	refresh(fs, files)
	price := pricer()
	for k, g := range groups {
		if s, ok := assemble(g, price); ok && s.Title != "" {
			out[k] = s.Title
		}
	}
	return out
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
