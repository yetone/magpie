// Package sessions lists the agents' recent sessions from their own session
// files — Claude Code's projects/*/<id>.jsonl (and Qoder's, the same kind),
// Codex's rollout files, OpenCode's database (or its older JSON files) and
// ZCode's, Pi's session files and omp's, DeepSeek Harness's, Cline's, Grok
// Build's, WorkBuddy's and CodeBuddy Code's, Droid's, Cursor CLI's and Alma's chat stores — with the tokens each spent, what that cost at the
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
	"errors"
	"io"
	"io/fs"
	"maps"
	"os"
	pathpkg "path"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/klauspost/compress/zstd"
	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/dirent"
	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/proc"
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

// ModelEffort is one model and reasoning effort's share of a session.
type ModelEffort struct {
	Model  string `json:"model"`
	Effort string `json:"effort"`
	Tokens
	Reasoning int     `json:"reasoning"`
	Cost      float64 `json:"cost"`
	Priced    bool    `json:"priced"`
}

// CodexIdentity is a Codex thread's confirmed role and parent chain.
type CodexIdentity struct {
	ID            string `json:"id"`
	Role          string `json:"role"`
	ParentSession string `json:"parent_session,omitempty"`
	RootSession   string `json:"root_session,omitempty"`
}

// Session is one agent session.
type Session struct {
	ReadOnly bool `json:"read_only,omitempty"`
	// Gateway: seen only through magpie's gateway, with no file of the
	// agent's on this computer (external.go)
	Gateway       bool          `json:"gateway,omitempty"`
	Agent         string        `json:"agent"` // magpie agent id: claude, codex, opencode, pi, omp, zcode, dsh, cline, qoder, qoder-cn, grok, workbuddy, workbuddy-ai, codebuddy, droid, cursor, hermes, alma, kimi, agy
	ID            string        `json:"id"`
	Cwd           string        `json:"cwd"`
	Title         string        `json:"title"` // the name it was given, else the agent's own title, else the first prompt
	Nickname      string        `json:"nickname,omitempty"`
	Start         time.Time     `json:"start"`
	Last          time.Time     `json:"last"`
	Role          string        `json:"role,omitempty"`
	ParentSession string        `json:"parent_session,omitempty"`
	RootSession   string        `json:"root_session,omitempty"`
	ModelEffort   []ModelEffort `json:"model_effort,omitempty"`
	Models        []Model       `json:"models"`
	Tokens
	Cost     float64 `json:"cost"`     // USD at the effective price, for the priced models
	Unpriced int     `json:"unpriced"` // models that spent tokens but have no known price
	Resume   string  `json:"resume"`   // the command that picks the session up again
	Path     string  `json:"path"`     // its (main) file
	// App is the agent the session ran in when it isn't the one whose files
	// hold it: claude-desktop for a Claude Code session Claude Desktop's Code
	// tab ran (its entrypoint, desktopEntrypoint). Usage counts the session
	// under App; the key, resume and transcript stay Agent's.
	App string `json:"app,omitempty"`
	// Carry are the other agents that can carry the session on, reading its
	// file as it is (carry.go)
	Carry []Carry `json:"carry,omitempty"`
	// Transcript says its conversation can be read (TranscriptOf)
	Transcript bool `json:"transcript,omitempty"`
	// Subagents are the conversations its agent started inside it, each
	// read with SubagentTranscript (subagents.go)
	Subagents []Subagent `json:"subagents,omitempty"`
	// WSL is the WSL distro the session ran in, its files read through
	// \\wsl.localhost (see wsl.go); "" for this computer's own
	WSL string `json:"wsl,omitempty"`
	// Convs are the ids magpie's gateway keys the session's requests by
	// when they carry no session id of their own (dshConvs)
	Convs []string `json:"-"`
	// ResponseIDs are the ids of the replies the session's own requests
	// got, as the gateway wrote them on its routes and the agent kept them
	// with the reply (dsh's replay state): a fork's inherited replies are
	// its source's, and are not counted here (Route.ResponseID)
	ResponseIDs []string `json:"-"`
	// ForkOf is the session a dsh fork was seeded from (its header's
	// parentSession, isSeeded): the fork's requests open as that one's do,
	// so a reply id that names the fork is taken over the opening only for
	// a fork of the opening's session (gui's ownerOf)
	ForkOf string `json:"-"`
	// SeedConvs are the conversation ids the fork's copied events opened
	// (dshConvs), which its seed's end takes off Convs: the source's
	// opening is what a fork and its source share, and dsh's current
	// forks write no parentSession to say so, so this is how the fork's
	// own replies are known to belong to a fork of that opening
	// (gui's ownerOf)
	SeedConvs []string `json:"-"`
	// TurnConvs are the ids of its newest turn's user messages, one
	// spelling each and the newest dshTurnKept at most: a request still
	// answering has no reply id yet, and the messages it carried — in
	// this session's file before it went out — say which branch it is of
	// (gui's ownerLast)
	TurnConvs []string `json:"-"`
	// Copies are the ids of the sessions that are copies of its
	// conversation, not listed apart from it (ccCopies)
	Copies []string `json:"-"`
	// Subs are the ids its subagents' and advisor's own files name
	// themselves by (omp), each one the session its requests tell the
	// gateway, while their tokens are counted in this session (subIDs)
	Subs []string `json:"-"`
	// BranchOf is the main file of the session a Claude-format session
	// is a branch of (ccFamily): it is listed under that one, and read
	// from that one's start up to where it went its own way
	BranchOf string `json:"-"`
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
	// the agent whose file it is, for its parseVersions
	Agent     string            `json:"agent,omitempty"`
	Codex     *codexUsageState  `json:"-"`
	codexPath string            // the rollout read, for its fork's parent
	Claude    *claudeUsageState `json:"claude,omitempty"`
	Head      digest            `json:"head,omitzero"`
	// int32 beside CdRead: the head is headLen bytes at most, and the 8 bytes
	// it saves take a state from the 448-byte size class to 416 (#2278)
	HeadSize int32 `json:"head_size,omitempty"`
	// Claude Code: parsed by a magpie that reads /cd's relocated lines
	// (ccRelocated), so Cwd is the folder the session was last moved to
	CdRead        bool           `json:"cd_read,omitempty"`
	ContentHash   digest         `json:"content_hash,omitzero"`
	Size          int64          `json:"size"`
	Mod           int64          `json:"mod"` // unix nanoseconds
	Off           int64          `json:"off"` // after the last whole line read
	ID            string         `json:"id,omitempty"`
	CodexIdentity *CodexIdentity `json:"codex_identity,omitempty"`
	Cwd           string         `json:"cwd,omitempty"`
	Title         string         `json:"title,omitempty"`
	Start         time.Time      `json:"start"`
	Last          time.Time      `json:"last"`
	Models        named[Tokens]  `json:"models,omitempty"`
	// Days is Models again, split by the local date each message was
	// written on: a session that runs past midnight counts on both days.
	Days named[*day] `json:"days,omitempty"`
	// Claude Code: the entrypoint its first line naming one says, which
	// tells a session Claude Desktop's Code tab ran (desktopEntrypoint)
	Entry string `json:"entry,omitempty"`
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
	// what few files have, nil for a file with none of it (StateMore)
	*StateMore
}

// StateMore is what only some files' state has. A history's every file is
// kept parsed, and these fields were ~330 of each state's 760 bytes, held
// empty by nearly all of them: ~6 MB of 18k files' states (#2278). It is
// embedded so the cache's JSON keeps its keys; a read through a state goes
// through more() and a write through own(), as a nil one has no fields.
type StateMore struct {
	DBRevision            string            `json:"db_revision,omitempty"`
	CodexIdentityConflict bool              `json:"codex_identity_conflict,omitempty"`
	CodexNickname         string            `json:"codex_nickname,omitempty"`
	CodexSpawnNames       map[string]string `json:"codex_spawn_names,omitempty"`
	CodexSpawnConflicts   map[string]bool   `json:"codex_spawn_conflicts,omitempty"`
	CodexAgentIDs         map[string]string `json:"codex_agent_ids,omitempty"`
	CodexAgentConflicts   map[string]bool   `json:"codex_agent_conflicts,omitempty"`
	Named                 string            `json:"named,omitempty"`  // the agent's own title for it
	Custom                string            `json:"custom,omitempty"` // the name the user gave it
	First                 string            `json:"first,omitempty"`  // the first message, when no prompt looked typed
	Convs                 []string          `json:"convs,omitempty"`  // the gateway's ids for its requests (dshConvs)
	// ResponseIDs are the ids of the replies its own requests got, as the
	// gateway wrote them on its routes and the agent kept them with the
	// reply (dsh's replay state): they name a request after the fact, where
	// a conversation id is only its opening (Route.ResponseID)
	ResponseIDs []string `json:"response_ids,omitempty"`
	// SeedConvs are the conversation ids a fork's copied events opened
	// (dshConvs), kept when its seed's end takes them off Convs: the
	// opening a fork and its source share is the source's, and this is
	// the fork's own record that it came from that opening (ownerSeed)
	SeedConvs []string `json:"seed_convs,omitempty"`
	// TurnConvs are the ids of its newest turn's user messages (the first
	// of dshConvs each, the newest dshTurnKept), reset at the turn's start
	// and not kept from a fork's seed (gui's ownerLast)
	TurnConvs []string `json:"turn_convs,omitempty"`
	// Claude Code: the uuids of the first and the last message of the
	// file's own conversation (ccLink)
	Root string `json:"root,omitempty"`
	Leaf string `json:"leaf,omitempty"`
	// Claude Code: the uuids of the summaries its compactions (/compact,
	// /rewind's summarize) left in the file. A copy made after one starts
	// with that summary, so its Root is one of these (ccCopies).
	Compacts []string `json:"compacts,omitempty"`
	// Pi: in a forked session, the time it was forked; the lines before
	// it are the copy of the session it was forked from
	Since time.Time `json:"since,omitzero"`
	// Grok Build: where its updates were read to, and what the read
	// carries on with
	Grok *grokCarry `json:"grok,omitempty"`
	// Droid: where its transcript was read to, and what the read carries
	// on with
	Droid *droidCarry `json:"droid,omitempty"`
	// Kimi Code: where its wire file was read to, and what the read
	// carries on with
	Kimi *kimiCarry `json:"kimi,omitempty"`
	// agy: where its transcript was read to, and what the read carries on
	// with
	Agy *agyCarry `json:"agy,omitempty"`
	// dsh: where its file was read to, and its header
	Dsh *dshCarry `json:"dsh,omitempty"`
	// Cline: the messages file its counts were read from
	Cline *clineCarry `json:"cline,omitempty"`
}

// noMore is what more gives for a state with none: read, never written.
var noMore StateMore

// more is s's StateMore to read: an empty one when s has none.
func (s *state) more() *StateMore {
	if s.StateMore != nil {
		return s.StateMore
	}
	return &noMore
}

// own is s's StateMore to write, made when s has none.
func (s *state) own() *StateMore {
	if s.StateMore == nil {
		s.StateMore = &StateMore{}
	}
	return s.StateMore
}

// moreOf is m to embed in a state: nil when it holds nothing.
func moreOf(m StateMore) *StateMore {
	if m.zero() {
		return nil
	}
	return &m
}

// zero: m holds nothing a state would keep (TestStateMoreZeroKnowsEveryField).
func (m *StateMore) zero() bool {
	return m == nil || m.DBRevision == "" && !m.CodexIdentityConflict && m.CodexNickname == "" &&
		len(m.CodexSpawnNames) == 0 && len(m.CodexSpawnConflicts) == 0 && len(m.CodexAgentIDs) == 0 &&
		len(m.CodexAgentConflicts) == 0 && m.Named == "" && m.Custom == "" && m.First == "" &&
		len(m.Convs) == 0 && len(m.ResponseIDs) == 0 && len(m.SeedConvs) == 0 && len(m.TurnConvs) == 0 &&
		m.Root == "" && m.Leaf == "" && len(m.Compacts) == 0 && m.Since.IsZero() &&
		m.Grok == nil && m.Droid == nil && m.Kimi == nil && m.Agy == nil && m.Dsh == nil && m.Cline == nil
}

// day is one local date's share of a file: tokens by model, and the time
// the session was at work on it.
type day struct {
	Models named[Tokens] `json:"m,omitempty"`
	// Active is milliseconds of work: the gaps between one line and the
	// next, each counted when shorter than idleGap and put on the later
	// line's day. A longer pause (lunch, the night, a session picked up
	// again next week) counts nothing. A subagent's file counts none, as
	// it runs while its session's own file goes on.
	Active int64 `json:"a,omitempty"`
	// Hours is Active again, by the local hour of the day it was put on
	// (only the hours that have some, or none; 24 numbers in the cache)
	Hours hours `json:"h,omitempty"`
	// Prompts are the messages typed in the session's own file, Replies
	// the agent's messages back (Claude Code's, Codex's, Pi's and omp's)
	Prompts int `json:"u,omitempty"`
	Replies int `json:"r,omitempty"`
	// Tools are the tool calls made, by the tool's name, and Skills the
	// skills called up, by the skill's (subagents' files included)
	Tools  named[int] `json:"t,omitempty"`
	Skills named[int] `json:"k,omitempty"`
}

// routerSkill is the one skill magpie's skill router gives an agent
// (library.RouterSkill). Calling it up only lists the skills behind it,
// and the one the agent then reads is counted (routedSkill), so it isn't
// counted as a skill of its own: every routed use would be counted twice.
const routerSkill = "magpie-skills"

// tool counts a call of a tool, and of a skill when it calls one up; a
// skill called up with no tool (Pi's /skill:name) has no name.
func (s *state) tool(at time.Time, name, skill string) {
	if skill == routerSkill {
		skill = ""
	}
	if name == "" && skill == "" {
		return
	}
	d := s.day(dateOf(at))
	if name != "" {
		*d.Tools.at(name)++
	}
	if skill != "" {
		*d.Skills.at(skill)++
	}
}

// routedSkill is the skill a read of path calls up through magpie's
// skill router (library's skill_router.go): an agent given magpie-skills
// reads a skill's SKILL.md under magpie-skills/.skills/<name>/ with its
// file tool, not its Skill tool. "" for any other path.
func routedSkill(path string) string {
	p := strings.ReplaceAll(path, `\`, "/")
	rest, ok := strings.CutSuffix(p, "/SKILL.md")
	if !ok {
		return ""
	}
	dir, name := pathpkg.Split(rest)
	if name == "" || !strings.HasSuffix(dir, "/"+routerSkill+"/.skills/") {
		return ""
	}
	return name
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
	d := s.Days.at(date)
	if *d == nil {
		*d = &day{}
	}
	return *d
}

// use counts a model's tokens, spent on a date.
func (s *state) use(date, model string, t Tokens) {
	if t.zero() {
		return
	}
	s.Models.at(model).add(t)
	s.day(date).Models.at(model).add(t)
}

// unuse takes back what use counted.
func (s *state) unuse(date, model string, t Tokens) {
	if i, ok := s.Models.find(model); ok {
		s.Models[i].v.sub(t)
	}
	if d := s.Days.of(date); d != nil {
		if i, ok := d.Models.find(model); ok {
			d.Models[i].v.sub(t)
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
			d.Hours.add(t.In(time.Local).Hour(), gap.Milliseconds())
		}
		s.Last = t
	}
}

func (s *state) clone() *state {
	c := *s
	c.Codex = s.Codex.clone()
	c.Claude = s.Claude.clone()
	c.Models = slices.Clone(s.Models)
	if s.Total != nil {
		t := *s.Total
		c.Total = &t
	}
	// its own StateMore: c := *s shares s's
	if m := s.StateMore; m != nil {
		cm := *m
		cm.Compacts = slices.Clone(m.Compacts)
		cm.Convs = slices.Clone(m.Convs)
		cm.ResponseIDs = slices.Clone(m.ResponseIDs)
		cm.SeedConvs = slices.Clone(m.SeedConvs)
		cm.TurnConvs = slices.Clone(m.TurnConvs)
		cm.CodexSpawnNames = maps.Clone(m.CodexSpawnNames)
		cm.CodexSpawnConflicts = maps.Clone(m.CodexSpawnConflicts)
		cm.CodexAgentIDs = maps.Clone(m.CodexAgentIDs)
		cm.CodexAgentConflicts = maps.Clone(m.CodexAgentConflicts)
		c.StateMore = &cm
	}
	c.Days = slices.Clone(s.Days)
	for i, e := range c.Days {
		v := e.v
		d := &day{Active: v.Active, Models: slices.Clone(v.Models), Prompts: v.Prompts, Replies: v.Replies}
		d.Hours = slices.Clone(v.Hours)
		d.Tools, d.Skills = slices.Clone(v.Tools), slices.Clone(v.Skills)
		c.Days[i].v = d
	}
	return &c
}

// file is one session file found on disk. What only some agents' files
// have is in more, so a listing of every rollout copies 96 bytes a file,
// not 232: a Sessions poll copied every agent's list of ~20k files five
// or six times over, ~50 MB a poll.
type file struct {
	agent    string
	key      string // agent:session id — a session may span files
	path     string
	size     int64
	mod      modTime
	main     bool // the session's own file, not a subagent's
	readOnly bool
	// cold: the WSL distro the file is in isn't running, and the file, as
	// last listed, is not to be opened (that would start the distro)
	cold bool
	more *fileMore // nil for a file with none of it; shared by copies
}

// modTime is a file's modification time in unix nanoseconds, 0 for none:
// a time.Time's 24 bytes made a file 96 bytes where it is now 80, and a
// Sessions poll copies every session file's into several slices (#2278).
type modTime int64

func modOf(t time.Time) modTime {
	if t.IsZero() {
		return 0
	}
	return modTime(t.UnixNano())
}

// Time is s as os.Stat's ModTime has it, the zero time for none.
func (s modTime) Time() time.Time {
	if s == 0 {
		return time.Time{}
	}
	return time.Unix(0, int64(s))
}

// UnixNano is Time().UnixNano(): what a parse kept of the time it was read at.
func (s modTime) UnixNano() int64 {
	if s == 0 {
		return time.Time{}.UnixNano()
	}
	return int64(s)
}

func (s modTime) UnixMilli() int64      { return s.Time().UnixMilli() }
func (s modTime) IsZero() bool          { return s == 0 }
func (s modTime) After(t modTime) bool  { return s > t }
func (s modTime) Before(t modTime) bool { return s < t }
func (s modTime) Equal(t modTime) bool  { return s == t }
func (s modTime) Compare(t modTime) int { return cmp.Compare(s, t) }

// fileMore is what a file of some agents has besides.
type fileMore struct {
	// OpenCode and ZCode: the session, and where it is kept; parent is
	// the session that started it, "" for one the user did
	sid    string
	parent string
	oc     ocStore
	// Cline: the session's manifest, beside its messages; Grok Build: its
	// summary.json, beside its updates; Droid: its settings, beside its
	// transcript
	manifest string
	hermes   *hermesDB
	// Alma: its database, and what tells the chat's rows changed
	alma *almaStore
	rev  string
	// agy: what conversation_summaries.db says of the conversation
	agy    *agySummary
	editor *cursorEditorMeta
	// loose: a Cursor editor transcript whose chat Cursor's editor keeps
	// no longer, read from a store that could be read (or none)
	loose bool
	// wsl: the WSL distro whose home it is in
	wsl string
}

// m is what more holds, or none of it. It is a copy: a write goes through w.
func (f file) m() fileMore {
	if f.more == nil {
		return fileMore{}
	}
	return *f.more
}

// w is f's own more to write: copies of f share it, so it is copied first.
func (f *file) w() *fileMore {
	m := f.m()
	f.more = &m
	return f.more
}

// byKey groups the files keep takes by session, each group in the files'
// order. The groups are cut from one copy of the kept files rather than
// grown an append at a time: a file is a few hundred bytes, and a Sessions
// poll grouped every one of ~16k files three times, ~150 MB in eight polls.
// A nil keep takes every file.
func byKey(files []file, keep func(*file) bool) map[string][]file {
	count := map[string]int{}
	n := 0
	for i := range files {
		if keep == nil || keep(&files[i]) {
			count[files[i].key]++
			n++
		}
	}
	kept := make([]file, n)
	groups := make(map[string][]file, len(count))
	at := 0
	for k, c := range count {
		groups[k] = kept[at : at : at+c]
		at += c
	}
	for i := range files {
		if f := &files[i]; keep == nil || keep(f) {
			groups[f.key] = append(groups[f.key], *f)
		}
	}
	return groups
}

// ClaudeDir is Claude Code's folder: $CLAUDE_CONFIG_DIR, else ~/.claude.
func ClaudeDir() string { return appdir.ClaudeDir() }

// ClaudeDirs are every Claude Code folder of the user's, ClaudeDir first:
// one Claude account is one CLAUDE_CONFIG_DIR (appdir.ClaudeHomes).
func ClaudeDirs() []string { return appdir.ClaudeHomes() }

// claudeFiles are the Claude Code session files of every folder
// (ClaudeDirs), a session once: the same id kept in two folders (one
// copied to the other) is counted under the first.
func claudeFiles() []file {
	dirs := ClaudeDirs()
	switch len(dirs) {
	case 0:
		return nil
	case 1:
		return ccFiles("claude", dirs[0])
	}
	// listed first, so the answer is made at its size: grown file by file
	// it was ~1 MB a pass with a few folders, a pass every 10 s while the
	// Sessions page is open
	lists, n := make([][]file, len(dirs)), 0
	for i, d := range dirs {
		lists[i] = ccFiles("claude", d)
		n += len(lists[i])
	}
	// and the answer is the last one when it is the same
	out := sameFiles{last: keptFiles("claude"), hint: n}
	owner := takeClaudeOwners()
	defer putClaudeOwners(owner)
	for i, d := range dirs {
		for _, f := range lists[i] {
			// a subagent's key is its session's: it goes where its session is
			if o, ok := owner[f.key]; ok && o != d {
				continue
			}
			owner[f.key] = d
			out.add(f)
		}
	}
	return sameAsKept("claude", &out)
}

// claudeOwners is claudeFiles' map of each key's folder, kept from pass
// to pass: made new, ~15k keys were ~1 MB of each.
var claudeOwners shelf[map[string]string]

func takeClaudeOwners() map[string]string {
	if m, ok := claudeOwners.take(nil); ok {
		return m
	}
	return map[string]string{}
}

func putClaudeOwners(m map[string]string) {
	clear(m)
	claudeOwners.give(m, nil)
}

// CodexDir is Codex's folder: $CODEX_HOME, else ~/.codex.
func CodexDir() string { return appdir.CodexHome() }

func stat(f *file) bool {
	fi, err := os.Stat(f.path)
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	f.size, f.mod = fi.Size(), modOf(fi.ModTime())
	return true
}

// statFiles fills in each file's size and modification time, as os.Stat
// gives them when follow is set and os.Lstat when it isn't, or leaves the
// file out when it is gone or no regular file. Every Requests page and
// every 15 s Sessions poll walks every session folder again, and stat()ing
// ~21k files one after another was most of that walk (#3493): each folder
// is read in one listing where the system has one (dirent.Stats), and the
// rest are stat()ed on a few goroutines.
func statFiles(files []file, follow bool) []file {
	dirs := map[string]dirent.Listing{}
	var order []string
	for _, f := range files {
		d := filepath.Dir(f.path)
		if _, seen := dirs[d]; !seen {
			dirs[d] = nil
			order = append(order, d)
		}
	}
	listed := make([]dirent.Listing, len(order))
	parallel(len(order), 8, func(i int) {
		if s, ok := dirent.Stats(order[i]); ok {
			listed[i] = s
		}
	})
	for i, d := range order {
		dirs[d] = listed[i]
	}
	ok := make([]bool, len(files))
	parallel(len(files), 256, func(i int) {
		f := &files[i]
		if in := dirs[filepath.Dir(f.path)]; in != nil {
			s, there := in.Get(filepath.Base(f.path))
			if !there || s.Mode != 0 && (s.Mode != fs.ModeSymlink || !follow) {
				return
			}
			if s.Mode == 0 {
				f.size, f.mod, ok[i] = s.Size, modOf(s.ModTime()), true
				return
			}
		}
		fi, err := statOne(f.path, follow)
		switch {
		case err == nil && fi.Mode().IsRegular():
			f.size, f.mod, ok[i] = fi.Size(), modOf(fi.ModTime()), true
			if last, seen := statMemo.Load(f.path); !seen || last != (statGood{f.size, f.mod}) {
				statMemo.Store(f.path, statGood{f.size, f.mod})
			}
		case err != nil && !errors.Is(err, fs.ErrNotExist):
			// A stat that failed for any reason but the file's absence says
			// nothing about it (a busy disk, a share that blinked): keep what
			// it last was, or the session vanishes until the next good poll.
			if last, seen := statMemo.Load(f.path); seen {
				g := last.(statGood)
				f.size, f.mod, ok[i] = g.size, g.mod, true
			}
		default:
			statMemo.Delete(f.path)
		}
	})
	for i, l := range listed {
		l.Done(order[i])
	}
	kept := files[:0]
	for i, f := range files {
		if ok[i] {
			kept = append(kept, f)
		}
	}
	return kept
}

// statGood is a file's size and time as statFiles last read them; statMemo
// holds them by path for a stat that fails and says nothing of the file.
type statGood struct {
	size int64
	mod  modTime
}

var statMemo sync.Map

// statOne is a file statFiles asks after alone: where no listing said, or
// a link it follows.
var statOne = func(path string, follow bool) (os.FileInfo, error) {
	if follow {
		return os.Stat(path)
	}
	return os.Lstat(path)
}

// parallel runs one(i) for each i below n, on a few goroutines when there
// are more than per for each.
func parallel(n, per int, one func(i int)) {
	workers := min(runtime.GOMAXPROCS(0), 8, n/per)
	if workers < 2 {
		for i := range n {
			one(i)
		}
		return
	}
	var next atomic.Int64
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for {
				i := int(next.Add(64)) - 64
				if i >= n {
					return
				}
				for j := i; j < min(i+64, n); j++ {
					one(j)
				}
			}
		})
	}
	wg.Wait()
}

// ccFiles are the session files of an agent that keeps them as Claude Code
// does, under its folder's projects/: a session's own <id>.jsonl in its
// project's folder, its subagents' in <id>/subagents/, and the agents of
// its workflows (ultracode) in <id>/subagents/workflows/<run>/agent-*.jsonl
// — beside each run's journal.jsonl, which isn't a transcript.
func ccFiles(agent, dir string) []file {
	if !Reads(agent) {
		return nil
	}
	return heldFiles("cc\x00"+agent+"\x00"+dir, func() []file { return listCCFiles(agent, dir) })
}

// ccListed is each folder's last listCCFiles' paths and keys, in its
// order: its listing grown from none, a file at a time, was ~35 MB of a
// minute of the page's polls over 13 GB of Claude Code history (#2278),
// and making each file's path and key again was ~10 MB of every poll
// over ~15k sessions, while the listing is the same but for a file or
// two.
var ccListed sync.Map // dir → []ccName

type ccName struct{ path, key string }

// ccNames gives a file's path and key, the last listing's strings when
// they are the same, as they are for all but a file or two.
type ccNames struct {
	last []ccName
	at   int // where the last listing is read up to
	now  []ccName
	made bool // now is made: until a name isn't the last's at its place, now is last[:at]
}

// of is filepath.Join(dir, name) and agent+":"+id, as last listed.
func (c *ccNames) of(dir, name, agent, id string) (string, string) {
	// one file new or gone since is stepped over
	for i := c.at; i < min(c.at+2, len(c.last)); i++ {
		if l := c.last[i]; joinedIs(l.path, dir, name) && joinedIs(l.key, agent, id, ':') {
			c.keep(i, l)
			return l.path, l.key
		}
	}
	n := ccName{filepath.Join(dir, name), agent + ":" + id}
	c.keep(-1, n)
	return n.path, n.key
}

// keep has n, the last listing's ith name or a new one (-1), next in now.
func (c *ccNames) keep(i int, n ccName) {
	if !c.made && i == c.at {
		c.at++
		return
	}
	if !c.made {
		c.made = true
		c.now = append(make([]ccName, 0, len(c.last)+len(c.last)/8), c.last[:c.at]...)
	}
	if i >= 0 {
		c.at = i + 1
	}
	c.now = append(c.now, n)
}

// names are the names listed: the last listing's when they are all of it.
func (c *ccNames) names() []ccName {
	switch {
	case c.made:
		return slices.Clip(c.now)
	case c.at == len(c.last):
		return c.last
	}
	return slices.Clone(c.last[:c.at])
}

// joinedIs is whether s is a, sep, b: filepath.Join(a, b) for a clean a
// and a b without a separator, as a folder's entries are.
func joinedIs(s, a, b string, sep ...byte) bool {
	c := byte(filepath.Separator)
	if len(sep) > 0 {
		c = sep[0]
	}
	return len(s) == len(a)+1+len(b) && s[len(a)] == c && s[:len(a)] == a && s[len(a)+1:] == b
}

func listCCFiles(agent, dir string) []file {
	projects := filepath.Join(dir, "projects")
	watch(dir) // statFiles reads only the projects that changed
	last, _ := ccListed.Load(dir)
	names := ccNames{last: func() []ccName { l, _ := last.([]ccName); return l }()}
	// listed into a list of its own kept from the last pass, and given as
	// the last pass's answer when it is the same once stat()ed
	key := "cc\x00" + agent + "\x00" + dir
	var out []file
	if b, ok := ccScratch.LoadAndDelete(key); ok {
		out = b.([]file)[:0]
	} else {
		out = make([]file, 0, len(names.last)+len(names.last)/8)
	}
	defer func() {
		// the last listing's names are kept already
		if names.made || names.at != len(names.last) {
			ccListed.Store(dir, names.names())
		}
	}()
	for _, project := range readDirectory(projects) {
		if !dirent.IsFolder(projects, project) {
			continue
		}
		root := filepath.Join(projects, project.Name())
		for _, e := range readDirectory(root) {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
				path, key := names.of(root, e.Name(), agent, strings.TrimSuffix(e.Name(), ".jsonl"))
				f := file{agent: agent, key: key, path: path, main: true}
				out = append(out, f)
			} else if dirent.IsFolder(root, e) {
				path := filepath.Join(root, e.Name())
				subs := filepath.Join(path, "subagents")
				for _, sub := range readDirectory(subs) {
					if sub.IsDir() || !strings.HasSuffix(sub.Name(), ".jsonl") {
						continue
					}
					path, key := names.of(subs, sub.Name(), agent, e.Name())
					f := file{agent: agent, key: key, path: path}
					out = append(out, f)
				}
				for _, run := range readDirectory(filepath.Join(subs, "workflows")) {
					if !dirent.IsFolder(filepath.Join(subs, "workflows"), run) {
						continue
					}
					dir := filepath.Join(subs, "workflows", run.Name())
					for _, sub := range readDirectory(dir) {
						if sub.IsDir() || !strings.HasPrefix(sub.Name(), "agent-") || !strings.HasSuffix(sub.Name(), ".jsonl") {
							continue
						}
						path, key := names.of(dir, sub.Name(), agent, e.Name())
						f := file{agent: agent, key: key, path: path}
						out = append(out, f)
					}
				}
			}
		}
	}
	same := sameFiles{last: keptFiles(key)}
	for _, f := range statFiles(out, true) {
		same.add(f)
	}
	ccScratch.Store(key, out[:0])
	return sameAsKept(key, &same)
}

// ccScratch is each folder's list listCCFiles lists into, taken while it
// does: two listing one folder at once each have their own.
var ccScratch sync.Map // lister key → []file

// allFiles are every agent's session files.
// filesHook, when a test sets it, runs on each pass over every agent's
// files.
var filesHook func()

func allFiles() []file { return allFilesWith(&allKept[0], zcodeFiles) }

// allFilesForTitles is allFiles with ZCode's sessions listed from their own
// rows alone: a name poll needs titles, not every message counted
// (zcodeFiles joins them for a session's size and last activity).
func allFilesForTitles() []file { return allFilesWith(&allKept[1], zcodeTitleFiles) }

func allFilesWith(kept *filesKept, zcode func() []file) []file {
	if filesHook != nil {
		filesHook()
	}
	lists := callFileLists(codexFiles())
	lists = append(lists, openCodeFiles(), piFiles(), raftFiles(), cindyFiles(), wslFiles("pi", "raft", "omp", "kimi"), zcode())
	lists = append(lists, dshFiles(), clineFiles(), ccFiles("qoder", QoderDir("qoder")), ccFiles("qoder-cn", QoderDir("qoder-cn")),
		grokFiles(), workbuddyFiles(), workbuddyAIFiles(), codebuddyFiles(), droidFiles(), ompFiles(), cursorFiles(), hermesFiles(), almaFiles(), kimiFiles(), agyFiles(),
		antigravityFiles()) // #2190
	// each lister asked Reads already; a file listed under another
	// agent's name is held to that agent too
	return allowed(kept.distinct(lists...))
}

// distinctOf is lists one after another with each path kept once, the
// first time it is listed (distinctPaths), in one slice and one set made
// at their size: a file is ~200 bytes, and the call files were copied and
// a set of them made once, then copied and a set made again with the
// other agents' (allFiles, callSources), ~10 MB on each of a poll's
// several passes over ~34k sessions (#2278).
func distinctOf(lists ...[]file) []file {
	n := 0
	for _, fs := range lists {
		n += len(fs)
	}
	return distinctInto(make([]file, 0, n), lists...)
}

// distinctInto is distinctOf appended to out.
func distinctInto(out []file, lists ...[]file) []file {
	seen := pathSet()
	defer putPathSet(seen)
	for _, fs := range lists {
		for _, f := range fs {
			if _, ok := seen[f.path]; !ok {
				seen[f.path] = struct{}{}
				out = append(out, f)
			}
		}
	}
	return out
}

// pathSets are sets of paths made for one pass over a listing and given
// back after it: a set of every session file's path, made new on each of
// a poll's several passes (distinctOf, refresh, pruneCalls), was ~100 MB
// of what a minute of polls allocated over ~34k files (#2278). A set
// keeps its room when cleared. They are kept on a shelf: a poll collects
// garbage about twice a second, which emptied a sync.Pool of them before
// the next pass.
var pathSets shelf[map[string]struct{}]

func pathSet() map[string]struct{} {
	if m, ok := pathSets.take(nil); ok {
		return m
	}
	return map[string]struct{}{}
}

func putPathSet(m map[string]struct{}) {
	clear(m)
	pathSets.give(m, nil)
}

// distinctPaths is fs with each path kept once, the first time it is
// listed. A path is one file to read and count: a session a database
// holds twice under one id (a TEXT and a BLOB row, #2932), or a folder
// two agents are told to share, would otherwise be counted twice.
// fs is given back as it is when every path in it is once: it may be a
// list kept and read elsewhere, so it is never written.
func distinctPaths(fs []file) []file {
	seen := pathSet()
	defer putPathSet(seen)
	var out []file // made at the first path listed again
	for i, f := range fs {
		if _, ok := seen[f.path]; ok {
			if out == nil {
				out = append(make([]file, 0, len(fs)-1), fs[:i]...)
			}
			continue
		}
		seen[f.path] = struct{}{}
		if out != nil {
			out = append(out, f)
		}
	}
	if out == nil {
		return fs
	}
	return out
}

// Dirs are the folders the sessions are read from: Claude Code's and
// Codex's, and the other agents' where they keep sessions on this computer.
func Dirs() []string {
	// gateway mode reads no folder here (OwnOff)
	if ownOff() {
		return nil
	}
	out := ClaudeDirs()
	out = append(out, CodexDirs()...)
	var named []os.FileInfo
	for _, d := range []struct{ dir, sessions string }{
		{OpenCodeDir(), OpenCodeDir()},
		{PiDir(), PiDir()},
		{SlockDir(), filepath.Join(SlockDir(), "agents")},
		{ZCodeDir(), zcodeDB()},
		{DshDir(), filepath.Join(DshDir(), "sessions")},
		{ClineSessionDir(), ClineSessionDir()},
		{QoderDir("qoder"), filepath.Join(QoderDir("qoder"), "projects")},
		{QoderDir("qoder-cn"), filepath.Join(QoderDir("qoder-cn"), "projects")},
		{GrokDir(), filepath.Join(GrokDir(), "sessions")},
		{WorkBuddyDir(), filepath.Join(WorkBuddyDir(), "projects")},
		{WorkBuddyAIDir(), filepath.Join(WorkBuddyAIDir(), "projects")},
		{CodeBuddyDir(), filepath.Join(CodeBuddyDir(), "projects")},
		{OmpDir(), filepath.Join(OmpDir(), "sessions")},
		{FactoryDir(), filepath.Join(FactoryDir(), "sessions")},
		{CursorDir(), filepath.Join(CursorDir(), "chats")},
		{cursorDataDir(), filepath.Join(cursorDataDir(), "projects")},
		{filepath.Dir(cursorEditorDB()), cursorEditorDB()},
		{AlmaDir(), almaDB()},
		{MiniMaxDir(), minimaxSessions()},
		{KimiCodeDir(), kimiSessions()},
		{AntigravityAppDir(), antigravityConversations()},
		{AntigravityInsidersDir(), filepath.Join(AntigravityInsidersDir(), "conversations")},
		{AgyDir(), filepath.Join(AgyDir(), "brain")},
	} {
		if _, err := os.Stat(d.sessions); err != nil {
			continue
		}
		// a folder two agents share is named once
		// (CODEBUDDY_CONFIG_DIR set to ~/.workbuddy, #2932)
		if fi, err := os.Stat(d.dir); err == nil {
			if slices.ContainsFunc(named, func(o os.FileInfo) bool { return os.SameFile(o, fi) }) {
				continue
			}
			named = append(named, fi)
		}
		out = append(out, d.dir)
	}
	// Cindy's Pi sessions, by their own folder: Cindy's is the app's
	// whole data
	for _, d := range cindySessionDirs() {
		if _, err := os.Stat(d); err == nil {
			out = append(out, d)
		}
	}
	out = append(out, HermesDirs()...)
	out = append(out, wslDirs()...)
	return out
}

// rollout-2026-09-20T15-48-28-<thread id>[_<segment>].jsonl, and .jsonl.zst
// once the Codex app's "compress local chat history" has packed an older one
var rolloutName = lazyre.New(`^rollout-\d{4}-\d{2}-\d{2}T\d{2}-\d{2}-\d{2}-([0-9A-Za-z-]+?)(?:_[0-9A-Za-z-]+)?\.jsonl(?:\.zst)?$`)

// zstSuffix ends a rollout the Codex app has compressed.
const zstSuffix = ".zst"

// codexFiles are the rollouts in every Codex folder of the user's
// (appdir.CodexHomes): one ChatGPT account is one CODEX_HOME, and a
// session is written into the folder of the account it ran under, so
// reading CodexDir alone left out every other account's sessions and
// calls (phantom3792 on X).
func codexFiles() []file {
	if !Reads("codex") {
		return nil
	}
	if !quiet() {
		return heldFiles("codex", listCodexFiles)
	}
	warmListing.Lock()
	defer warmListing.Unlock()
	if warmListing.files != nil && time.Since(warmListing.at) < warmListingFor {
		return warmListing.files // only read, as heldFiles' are
	}
	files := slices.Clip(listCodexFiles())
	warmListing.at, warmListing.files = time.Now(), files
	return files
}

// warmListing is codexFiles' listing kept through the warm-up: its reads
// (the stats of every day and of 30, the Requests page) each listed every
// rollout again within a few seconds, ~12k lstat()s and ~45 MB a time
// (#3493). It is kept only while nobody but the warm-up reads (quiet) and
// for warmListingFor at most, the age statsFor's memo already answers
// with; Warm lets it go when it ends.
var warmListing struct {
	sync.Mutex
	at    time.Time
	files []file
}

const warmListingFor = 10 * time.Second

func forgetWarmListing() {
	warmListing.Lock()
	warmListing.files = nil
	warmListing.Unlock()
}

// listCodexFiles is codexFiles listed now.
func listCodexFiles() []file {
	dirs := CodexDirs()
	lists := make([][]file, len(dirs))
	for i, dir := range dirs {
		lists[i] = codexFilesIn(dir)
	}
	// one folder's listing is the whole of it: copied, it was ~65 MB of
	// what eight Sessions polls allocated
	return distinctPaths(concatKept("codex", lists...))
}

// CodexDirs are every Codex folder of the user's, CodexDir first.
func CodexDirs() []string { return appdir.CodexHomes() }

// walkLinked is filepath.WalkDir over root, which can be a link to the
// folder: the Codex CLI keeps .codex on another drive by making
// ~/.codex/sessions a junction to it, and a symlink does the same
// elsewhere. WalkDir lstat()s the root it is handed, where a link is no
// folder at all, so it walked nothing behind it (#2233). It walks where
// the link leads (proc.RealPath, since filepath.EvalSymlinks leaves a
// junction as it is) and hands fn each path under root, not under the
// link's target: an agent's folder is read back out of a session's path
// (Codex's config.toml and state databases), and the target holds none
// of it. A root that is no link is walked as it is.
func walkLinked(root string, fn fs.WalkDirFunc) error {
	walk := root
	if fi, err := os.Lstat(root); err == nil && !fi.IsDir() {
		if real, err := proc.RealPath(root); err == nil {
			walk = real
		}
	}
	return filepath.WalkDir(walk, func(p string, d fs.DirEntry, err error) error {
		if walk != root {
			if rel, err := filepath.Rel(walk, p); err == nil {
				p = filepath.Join(root, rel)
			}
		}
		return fn(p, d, err)
	})
}

// codexFilesIn are the rollouts in a Codex folder.
func codexFilesIn(dir string) []file {
	if !Reads("codex") {
		return nil
	}
	// its unchanged day folders aren't read again; a change in dir itself
	// (history.jsonl, at each prompt) has them all read
	watch(dir)
	var lists [2][]file
	for i, folder := range []string{"sessions", "archived_sessions"} {
		root := filepath.Join(dir, folder)
		if listed, ok := listedRollouts(root); ok {
			lists[i] = listed
			continue
		}
		var found []file
		walkLinked(root, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			m := rolloutName.FindStringSubmatch(d.Name())
			if m == nil {
				return nil
			}
			found = append(found, file{agent: "codex", key: "codex:" + m[1], path: p, main: true})
			return nil
		})
		lists[i] = statFiles(found, false)
	}
	out := concatKept("codex\x00"+dir, lists[:]...)
	// During compression, prefer the readable plain twin in the same
	// directory: the new compressed file may not have its trailer yet.
	// (a set kept from poll to poll: made new, it was ~0.4 MB of each, #2278)
	plain := pathSet()
	defer putPathSet(plain)
	for _, f := range out {
		if !strings.HasSuffix(f.path, zstSuffix) {
			plain[f.path] = struct{}{}
		}
	}
	twin := func(f file) bool {
		q, ok := strings.CutSuffix(f.path, zstSuffix)
		_, plainToo := plain[q]
		return ok && plainToo
	}
	// out may be a list kept and read elsewhere: a twin left out is left
	// out of a copy
	if slices.ContainsFunc(out, twin) {
		out = slices.DeleteFunc(slices.Clone(out), twin)
	}
	return distinctCodexFiles(out)
}

// rolloutsListed is how many rollouts each root's last listing found.
var rolloutsListed sync.Map

// rolloutsSeen are each folder's rollouts as its last listing found them,
// in name order: their paths and keys, made again for each of ~20k
// rollouts on every listing, were ~50 MB of a minute of the page's polls
// (#2278).
var rolloutsSeen sync.Map // shown folder → []file

// listedRollouts are the rollouts under root, in walkLinked's order and
// with their sizes and times, read with one listing a folder
// (dirent.Stats) rather than a listing and an lstat() a file: Codex's day
// folders were listed twice and every rollout lstat()ed on each 15 s
// Sessions poll. ok is false when a folder couldn't be read so, and
// walkLinked and statFiles read root instead.
func listedRollouts(root string) ([]file, bool) {
	// made against the last listing of root, and that listing itself when
	// nothing in it changed
	key := "rollouts\x00" + root
	out := sameFiles{last: keptFiles(key)}
	if n, ok := rolloutsListed.Load(root); ok {
		out.hint = n.(int) + n.(int)/16
	}
	walk := root
	if fi, err := os.Lstat(root); err != nil {
		return nil, false
	} else if !fi.IsDir() {
		real, err := proc.RealPath(root)
		if err != nil {
			return nil, false
		}
		walk = real
	}
	type span struct {
		shown      string
		start, end int
	}
	var leaves []span
	var in func(real, shown string) bool
	in = func(real, shown string) bool {
		entries, ok := dirent.Stats(real)
		if !ok {
			return false
		}
		defer entries.Done(real)
		prev, _ := rolloutsSeen.Load(shown)
		seen, _ := prev.([]file)
		start, leaf := out.n(), true
		for _, e := range entries {
			name := e.Name
			switch e.Mode {
			case fs.ModeDir:
				leaf = false
				if !in(filepath.Join(real, name), filepath.Join(shown, name)) {
					return false
				}
			case 0:
				// both in name order: a rollout listed before keeps its
				// path and key, and only its size and time are new
				for len(seen) > 0 && filepath.Base(seen[0].path) < name {
					seen = seen[1:]
				}
				if len(seen) > 0 && filepath.Base(seen[0].path) == name {
					f := seen[0]
					f.size, f.mod = e.Size, modOf(e.ModTime())
					out.add(f)
					continue
				}
				if m := rolloutName.FindStringSubmatch(name); m != nil {
					out.add(file{agent: "codex", key: "codex:" + m[1], path: filepath.Join(shown, name), main: true, size: e.Size, mod: modOf(e.ModTime())})
				}
			}
		}
		// a day folder's only: in a folder with folders in it, out has
		// their rollouts among its own
		if leaf {
			leaves = append(leaves, span{shown, start, out.n()})
		}
		return true
	}
	if !in(walk, root) {
		return nil, false
	}
	l, changed := out.list()
	rolloutsListed.Store(root, len(l))
	if changed {
		listsKept.Store(key, l)
		// each day folder's rollouts are its part of the list: a copy of
		// each was the list again (#2278)
		for _, sp := range leaves {
			rolloutsSeen.Store(sp.shown, l[sp.start:sp.end:sp.end])
		}
	}
	return l, true
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
	// storeMu keeps the reads of the sessions apart from the changes made
	// to their files (Delete, a provider move, a project relocation): a
	// read takes it shared, so reads never wait on one another, and a
	// change alone. The store handles they share live as long as their last
	// user (useDBs), not as long as the read that opened them.
	storeMu sync.RWMutex
	mu      sync.Mutex
	cache   map[string]*state // path → parse
	// parsing holds the files a refresh is reading now, each closed when
	// its parse is in the cache: another reader serves the parse kept of
	// such a file rather than reading it a second time, and waits only for
	// one it has no parse of yet (#2776: the start's warm-up held every
	// page for its whole read)
	parsing         = map[string]chan struct{}{}
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
// 20: Codex's source and direct parent thread identity.
// 21: Codex subagent nicknames and parent spawn task names.
// 22: dsh's gateway conversation ids (Convs).
// 23: Claude Code's first and last message uuids (Root, Leaf).
// 24: Claude Code's compact summaries' uuids (Compacts).
// 25: the skills Codex calls up by reading their SKILL.md.
// 26: Grok Build's tool calls and skills.
// 27: Factory Droid's skills (its Skill tool and /<name> prompts).
// 28: OpenCode 2's, Cline's and dsh's tool calls and skills.
// 29: Claude Code's entrypoint (Entry), Desktop's Code tab apart.
//
// A change to how one agent's files are read raises that agent's
// parseVersions instead: a raise of cacheVersion reads every file of every
// agent again, minutes of CPU for a long history after each update (#2278).
const cacheVersion = 29

// parseVersions is cacheVersion for one agent's parses, 0 when not listed:
// raised, the parses of that agent's files kept by an older magpie are read
// again, and the rest stand.
//
// codex 1, zcode 1: a fork's copies of its parent's usage are the
// parent's (codex_fork.go, zcDB.copied).
//
// codex 2: only a fork's copies are; its own calls after them are its own,
// whatever their numbers.
//
// dsh 1: the ids of the replies its own requests got (ResponseIDs), for
// the routes that carry no session id and an opening a fork copies.
//
// dsh 2: the openings a fork's seed opened (SeedConvs), which its end
// takes off Convs: a fork dsh writes today names no parentSession, so its
// own replies are told from its source's by them.
//
// antigravity 1: a Gemini deployment's name with any one letter after it
// (gemini-4-argon-d) is its model's (GeminiDeployment), not -n alone.
// antigravity 2: a conversation's folder is its summary row's workspace
// (a parse kept from before has none, under the same row revision).
// dsh 3: the ids of its newest turn's messages (TurnConvs), which a
// request still answering carried and has no reply id for yet.
var parseVersions = map[string]int{"codex": 3, "zcode": 1, "dsh": 3, "antigravity": 2}

type cacheFile struct {
	Version int    `json:"version"`
	Zone    string `json:"zone"` // the time zone its days are dates in
	// Parsers is parseVersions as the parses were made
	Parsers map[string]int    `json:"parsers,omitempty"`
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
	cacheUsed()
	z := zone()
	if loaded && z == cacheZone {
		return
	}
	loaded, cacheZone = true, z
	cacheGeneration++
	cache = map[string]*state{}
	saved() // the kept file as the last save left it
	if b, done, err := readCacheFile(CachePath()); err == nil {
		files, ok := decodeCache(b, z)
		done()
		if ok {
			cache = files
		}
	}
}

// saveCache keeps the parses on disk. Writing the whole index (tens of MB
// for a long history) takes a second or more, so it is done behind the
// request: the parses in the cache are never changed once put there (a new
// parse starts from a clone), so a copy of the map is a snapshot; saves in a
// row are folded into the latest, and one is written saveGap after the
// last at the soonest.
func saveCache() { saveCacheWithin(saveGap) }

// saveCacheWithin is saveCache written gap after the last save at the
// soonest; saves folded into one wait the shortest gap any of them asked.
func saveCacheWithin(gap time.Duration) {
	snap := save{cacheFile{Version: cacheVersion, Zone: zone(), Parsers: parseVersions, Files: savable()}, CachePath()}
	saving.Lock()
	defer saving.Unlock()
	if saving.next == nil || gap < saving.gap {
		saving.gap = gap
	}
	saving.next = &snap
	if saving.running {
		if gap < saveGap {
			select { // a save waiting out a longer gap looks again
			case saveNow <- struct{}{}:
			default:
			}
		}
		return
	}
	saving.running = true
	go func() {
		for {
			saving.Lock()
			wait := saving.gap - time.Since(saving.wrote)
			if saving.hurry {
				wait = 0
			}
			saving.Unlock()
			if wait > 0 {
				select {
				case <-time.After(wait):
				case <-saveNow:
					continue // hurried, or a shorter gap asked: look again
				}
			}
			saving.Lock()
			f := saving.next
			saving.next = nil
			if f == nil {
				saving.running, saving.hurry = false, false
				select { // a Saved's wake-up, unused, cuts no later gap
				case <-saveNow:
				default:
				}
				saving.Broadcast()
				saving.Unlock()
				return
			}
			saving.Unlock()
			writeCache(f)
			saving.Lock()
			saving.wrote = time.Now()
			saving.Unlock()
		}
	}()
}

// saveGap is how long a save waits after the last one written. A Sessions
// or Usage page polls every 15 s, and a live session changes its file
// between any two, so each poll wrote the whole index again: tens of MB
// for a long history, ~20 MB allocated to encode it each time. The index
// only spares the next start a read; what a save not yet written misses is
// read again then. Saved does not wait it out.
var saveGap = time.Minute

// A refresh that reads scanFiles files or more is a scan (a first start, a
// lost or outdated index), not a poll catching up with a live session. Its
// checkpoints wait scanSaveGap, not saveGap, and its last save waits for
// nothing: under saveGap a scan's later checkpoints and its end waited a
// minute after its first checkpoint, and a magpie quit or updated within
// it kept a few hundred of 17,000 files, so each start read 22 GB of
// rollouts again (15 to 28 s before Sessions showed, 70 to 94 s of CPU).
var (
	scanFiles   = 32
	scanSaveGap = 15 * time.Second
)

// saveNow wakes a save waiting out saveGap once saving.hurry is set.
var saveNow = make(chan struct{}, 1)

var saving struct {
	sync.Mutex
	*sync.Cond
	running bool
	next    *save
	gap     time.Duration // the shortest gap the saves folded into next asked
	wrote   time.Time     // when the last save was written
	hurry   bool          // Saved is waiting: no save waits out saveGap
	// failed says the last write didn't reach the disk: the parses in
	// memory are then the only ones, and stay (dropIdleCache)
	failed bool
}

type save struct {
	cacheFile
	path string
}

func init() { saving.Cond = sync.NewCond(&saving.Mutex) }

// Saved waits for the index being saved, so a process about to end leaves
// it on disk.
func Saved() { saved() }

// saved waits for the saves under way, written without their saveGap.
func saved() {
	saving.Lock()
	if saving.running {
		saving.hurry = true
		select {
		case saveNow <- struct{}{}:
		default:
		}
	}
	for saving.running {
		saving.Wait()
	}
	saving.Unlock()
	ccFamilyWrites.Wait()
	ccRestWrites.Wait()
}

// saveHook, when a test sets it, runs before each save is written.
var saveHook func()

func writeCache(c *save) {
	if saveHook != nil {
		saveHook()
	}
	ok := false
	defer func() {
		saving.Lock()
		saving.failed = !ok
		saving.Unlock()
	}()
	dir := filepath.Dir(c.path)
	if os.MkdirAll(dir, 0o755) != nil {
		return
	}
	tmp, err := os.CreateTemp(dir, "sessions-*.json")
	if err != nil {
		return
	}
	w := bufio.NewWriterSize(tmp, 1<<20)
	if err = encodeCache(w, c.cacheFile); err == nil {
		err = w.Flush()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil || os.Rename(tmp.Name(), c.path) != nil {
		os.Remove(tmp.Name())
		return
	}
	ok = true
}

// refresh parses those of want that changed since their parse was kept, a
// few files at a time, and keeps the parses of every file still on disk
// (all of them), read by List or by Stats. With no all-set it leaves other
// cached agents alone, for a Codex-only identity refresh.
func refresh(want, all []file) {
	defer func() { trimSummaryRevisions(time.Now()) }()
	var todo, waits []file
	named := false
	for _, f := range want {
		if f.cold {
			continue // in a stopped WSL distro: what was read of it stands
		}
		if s := parsed(f.path); s == nil || s.Size != f.size || s.Mod != f.mod.UnixNano() || ccStale(f, s) || (f.agent == "hermes" && (f.m().hermes == nil || f.m().rev == "" || s.more().DBRevision != f.m().rev)) || ((f.agent == "alma" || f.agent == "cursor" || f.agent == "agy" || f.agent == "antigravity") && s.more().DBRevision != f.m().rev) {
			if parsing[f.path] != nil {
				// a refresh is reading it, another or this one (a path
				// listed twice): its last parse stands until that one is
				// in, and one never read is waited for
				if s == nil {
					waits = append(waits, f)
				}
				continue
			}
			// marked as it is taken, so a second listing of the path is
			// not a second job: two jobs shared one entry, and the second
			// publish closed a nil channel (#2932)
			parsing[f.path] = make(chan struct{})
			todo = append(todo, f)
		} else if (s.Agent != f.agent || f.agent == "claude" && !s.CdRead) && parsing[f.path] == nil {
			// kept by a magpie that didn't name the agent: named now, so
			// a raise of another agent's parseVersions keeps it. A Claude
			// Code parse whose folder is the file's (not ccStale) is as a
			// read of the relocated lines would make it, and is marked so,
			// so the folder isn't worked out again on every refresh
			n := *s
			n.Agent = f.agent
			n.CdRead = n.CdRead || f.agent == "claude"
			cache[f.path] = &n
			named = true
		}
	}
	if len(waits) > 0 {
		defer waitParsing(waits)
	}
	gone := false
	if len(cache) > 0 && all != nil {
		on := pathSet()
		defer putPathSet(on)
		for _, f := range all {
			on[f.path] = struct{}{}
		}
		for p := range cache {
			if _, ok := on[p]; !ok {
				delete(cache, p)
				gone = true
			}
		}
	}
	trimSummaryRevisions(time.Now())
	if len(todo) == 0 {
		if gone || named {
			saveCache()
		}
		return
	}
	// Limit cold-scan workers to the windows that can survive publication.
	// Otherwise a batch of historical files could retain every window until
	// the whole scan finishes.
	windows := summaryRevisionWindows(todo, time.Now())
	// the biggest first, so no long file is left to run on alone at the
	// end; with half the cores (scanReaders)
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
		jobs[i] = job{f: f, old: parsed(f.path)}
	}
	generation := cacheGeneration
	mu.Unlock()
	ch := make(chan int)
	finished := make(chan int, len(jobs))
	for i := 0; i < min(scanReaders(), len(todo)); i++ {
		go func() {
			for i := range ch {
				j := &jobs[i]
				// its file's calls lock is taken before a read slot, as
				// readCalls takes them
				calls := callsAlong(j.f, j.old)
				release := readSlot()
				end := readBegin()
				if parseHook != nil {
					parseHook()
				}
				j.parsed = parseWith(j.f, j.old, calls)
				end()
				release()
				if j.parsed.Agent != j.f.agent {
					n := *j.parsed // a parser may hand back old, which readers hold
					n.Agent = j.f.agent
					j.parsed = &n
				}
				if (j.parsed.Claude != nil && !windows[j.f.path]) || (j.parsed.Claude == nil && !recentRevision(j.parsed.Mod, j.parsed.revisionWeight(), time.Now())) {
					j.parsed = j.parsed.withoutRevisions()
				}
				progress.files.Add(1)
				progress.read.Add(left[j.f.path])
				finished <- i
			}
		}()
	}
	go func() {
		for i := range jobs {
			ch <- i
		}
		close(ch)
	}()
	// publish puts a finished parse in the cache at once, for a reader
	// waiting on it, and lets the file be read again.
	publish := func(i int) {
		j := &jobs[i]
		// A concurrent read may already have published a newer parse.
		if generation == cacheGeneration && parsed(j.f.path) == j.old {
			cache[j.f.path] = j.parsed
		}
		close(parsing[j.f.path])
		delete(parsing, j.f.path)
	}
	// What is read is kept on disk every checkpointEvery, not only at the
	// end: a first read of a long history on a slow disk runs for minutes,
	// and a magpie quit before the end (its CPU full, #1665) kept nothing
	// of it, so the next start read it all again.
	checkpointGap, endGap := saveGap, saveGap
	if len(jobs) >= scanFiles {
		checkpointGap, endGap = min(scanSaveGap, saveGap), 0
	}
	tick := time.NewTicker(checkpointEvery)
	defer tick.Stop()
	unsaved := false
	for n := 0; n < len(jobs); {
		select {
		case i := <-finished:
			mu.Lock()
			publish(i)
			mu.Unlock()
			unsaved = true
			n++
		case <-tick.C:
			if !unsaved {
				continue
			}
			mu.Lock()
			if generation == cacheGeneration {
				saveCacheWithin(checkpointGap)
			}
			mu.Unlock()
			unsaved = false
		}
	}
	mu.Lock()
	progress.finish()
	if generation == cacheGeneration {
		saveCacheWithin(endGap)
	}
}

// waitParsing waits, with mu let go, for the files other refreshes are
// reading, and reads itself any of them that came to nothing (a Reset
// dropped its parse).
func waitParsing(fs []file) {
	for {
		var chans []chan struct{}
		for _, f := range fs {
			if ch := parsing[f.path]; ch != nil {
				chans = append(chans, ch)
			}
		}
		if len(chans) == 0 {
			break
		}
		mu.Unlock()
		for _, ch := range chans {
			<-ch
		}
		mu.Lock()
	}
	var missing []file
	for _, f := range fs {
		if cache[f.path] == nil {
			missing = append(missing, f)
		}
	}
	if len(missing) > 0 {
		refresh(missing, nil)
	}
}

// checkpointEvery is how often a long read of changed files keeps what it
// has read so far on disk.
var checkpointEvery = 5 * time.Second

// parseHook, when a test sets it, runs before each changed file is parsed.
var parseHook func()

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
		// held: a model's price looks its provider up among every
		// provider, and with nothing held each look built them all again
		// (~40 MB in 4 minutes of the Sessions page open, a check of every
		// kept price on each 10 s refresh); held, a pass builds them once
		defer provider.Hold()()
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
	desktopTitles.Lock()
	desktopTitles.names, desktopTitles.last = nil, nil
	desktopTitles.Unlock()
	directoryCache.Lock()
	directoryCache.entries = map[string]directoryEntry{}
	directoryCache.Unlock()
	resetCalls()
	droidCallers.reset()
	grokKinds.reset()
	wslReset()
	forgetHermesPrints()
	forgetAgySummaries()
	forgetBehind()
	mu.Lock()
	defer mu.Unlock()
	saved()
	ccFamiliesForget()
	cache, loaded = nil, false
	cacheGeneration++
	forgetKeptStats()
	forgetKeptList()
}

// List reads the latest sessions of every agent, the most recently active
// first, at most limit of them (Limit when 0).
func List(limit int) []Session {
	out, _, _ := ListUnless(limit, -1)
	return out
}

// ListUnless is List, and a number for its listing that changes whenever
// the listing is made again: at least once a listing has stood
// listKeptFor. When that number is made, the listing is still the one
// the caller had it with, and no copy of it is given (same). The Sessions
// page asks for every session each 15 s, and an unchanged list was
// copied, rowed, encoded and hashed again, only to answer X-Magpie-Same.
func ListUnless(limit, made int) (out []Session, now int, same bool) {
	// read before the locks: it reads settings.json
	alias := provider.UsageAlias()
	out, now, same = listing(limit, made)
	if alias != nil {
		for i := range out {
			aliasSession(&out[i], alias)
		}
	}
	return out, now, same
}

func listSessions(limit int) []Session {
	out, _, _ := listing(limit, -1)
	return out
}

// listing is the listing at limit, kept or made now, and keptList.made
// for it: none when the kept one is made, the number the caller had.
func listing(limit, made int) ([]Session, int, bool) {
	storeMu.RLock()
	defer storeMu.RUnlock()
	if limit <= 0 {
		limit = Limit
	}
	mu.Lock()
	defer mu.Unlock()
	loadCache()

	defer useDBs()()
	files := allFiles()
	// every changed file, not just the latest sessions': the first read
	// indexes them all in one run the page can show, and the stats read
	// after it has nothing left to do
	refresh(files, files)
	price, titles := pricer(), codexTitleOf()
	if out, ok := keptListing(files, limit, price, titles); ok {
		if keptList.made == made {
			return nil, made, true
		}
		return slices.Clone(out), keptList.made, false
	}
	price, titles = keepListing(files, limit, price, titles)
	groups := byKey(files, nil)
	latest := make(map[string]time.Time, len(groups))
	for _, f := range files {
		if f.mod.After(modOf(latest[f.key])) {
			latest[f.key] = f.mod.Time()
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
	identities := codexIdentitySnapshot(files)
	taskTitles := codexSubagentTaskTitles(files)

	copies, branches := ccFamily(files)
	copyIDs := copiesByKeeper(copies, groups)

	// made for the page at once, not grown an append at a time (#2278)
	out := make([]Session, 0, min(limit, len(keys)))
	for _, k := range keys {
		if copies[k] != "" {
			continue
		}
		if s, ok := assemble(groups[k], price, titles); ok {
			s.Copies = copyIDs[k]
			setBranch(&s, branches[k], groups)
			setSessionCodexIdentity(&s, identities)
			setSessionCodexTaskTitle(&s, taskTitles)
			out = append(out, s)
			if len(out) == limit {
				break
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Last.After(out[j].Last) })
	keepList(out)
	return out, keptList.made, false
}

// sortSessionFiles puts a session's own file first; for Codex the earliest
// segment.
func sortSessionFiles(fs []file) {
	sort.SliceStable(fs, func(i, j int) bool {
		if fs[i].main != fs[j].main {
			return fs[i].main
		}
		return filepath.Base(fs[i].path) < filepath.Base(fs[j].path)
	})
}

// sessionID is a session's id: its key's, or for an agent that writes it
// inside the file (idInFile), what its own file's parse read.
func sessionID(fs []file) string {
	sortSessionFiles(fs)
	id := strings.TrimPrefix(fs[0].key, fs[0].agent+":")
	if idInFile(fs[0].agent) && fs[0].main {
		if st := cache[fs[0].path]; st != nil && st.ID != "" {
			id = st.ID
		}
	}
	return id
}

// subIDs are the ids a session's other files (an omp subagent's or
// advisor's) give in their own header, other than its own id. Each of
// those files is an agent of its own to its vendor: omp sends its id as
// the request's session (x-session-affinity), so the gateway records the
// subagent's calls under that id, while this session counts the same
// calls from the file. GatewayKeys keeps the gateway from counting them
// a second time.
func subIDs(fs []file, id string) []string {
	var out []string
	for _, f := range fs {
		if f.main || !idInFile(f.agent) {
			continue
		}
		if st := cache[f.path]; st != nil && st.ID != "" && st.ID != id && !slices.Contains(out, st.ID) {
			out = append(out, st.ID)
		}
	}
	return out
}

// assemble puts a session together from its files' parses.
func assemble(fs []file, price func(string) *catalog.Price, codexTitle func(string) string) (Session, bool) {
	s, ok := assembleListed(fs, price, codexTitle)
	if !ok {
		return s, false
	}
	if !s.ReadOnly {
		s.Resume = resumeFor(s.WSL, s.Agent, s.ID, s.Cwd, s.Path)
	}
	s.Carry, s.Transcript = carries(s), readable(s)
	if s.Transcript {
		s.Subagents = subagentsOf(s, fs)
	}
	return s, true
}

// assembleListed is assemble without what only a listed session's row shows (how
// to resume it, its subagents): whether List lists the session, and its
// agent, id, title and usage. The stats ask it of every session on each
// poll, and building every session's subagents there read each Claude Code
// subagent's file again (3.5 GB of a live magpie's 6.3 GB of scans).
func assembleListed(fs []file, price func(string) *catalog.Price, codexTitle func(string) string) (Session, bool) {
	sortSessionFiles(fs)
	s := Session{Agent: fs[0].agent, Path: fs[0].path, Models: []Model{}, WSL: fs[0].m().wsl}
	for _, f := range fs {
		s.ReadOnly = s.ReadOnly || f.readOnly
	}
	s.ID = sessionID(fs)
	s.Subs = subIDs(fs, s.ID)
	var custom, named, first string
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
			if st.more().Named != "" {
				named = st.more().Named
			}
			if st.more().Custom != "" {
				custom = st.more().Custom
			}
			if first == "" {
				first = st.more().First
			}
			if s.Agent == "claude" && s.App == "" && desktopEntrypoint(st.Entry) {
				s.App = "claude-desktop"
			}
			s.Convs = append(s.Convs, st.more().Convs...)
			s.ResponseIDs = append(s.ResponseIDs, st.more().ResponseIDs...)
			s.SeedConvs = append(s.SeedConvs, st.more().SeedConvs...)
			s.TurnConvs = append(s.TurnConvs, st.more().TurnConvs...)
			if d := st.more().Dsh; d != nil && d.Head.IsSeeded && d.Head.DelegationDepth == 0 {
				s.ForkOf = d.Head.ParentSession
			}
		}
		if s.Agent == "codex" && st.more().CodexNickname != "" {
			s.Nickname = st.more().CodexNickname
		}
		if !st.Start.IsZero() && (s.Start.IsZero() || st.Start.Before(s.Start)) {
			s.Start = st.Start
		}
		if st.Last.After(s.Last) {
			s.Last = st.Last
		}
		for name, t := range st.Models.all() {
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
		custom = codexTitle(s.ID)
	}
	if s.Agent == "claude-desktop" && s.WSL == "" {
		custom = cmp.Or(DesktopTitles([]string{s.ID})[s.ID], custom)
	}
	s.Title = cmp.Or(custom, named, s.Title, first)
	// in the models' order: summed in the map's, the cost came out a last
	// digit apart from read to read, and the session's row read as changed
	for _, name := range slices.Sorted(maps.Keys(models)) {
		m := models[name]
		if p := price(m.Model); p != nil {
			// a sum of the session's calls of the model, at its base price:
			// which call went over a long-context tier isn't known here
			m.Cost, m.Priced = p.At(0).CostSplit(m.Input, m.Output, m.CacheRead, m.CacheWrite, m.CacheWrite1h), true
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
		s.Last = fs[0].mod.Time()
	}
	if s.Title == "" && s.Tokens.zero() {
		return s, false // nothing was said in it
	}
	return s, true
}

// parserFor is the source of truth for both parsing and window admission.
// A default line reader is Claude-compatible, including both Qoder variants.
type sessionParser struct {
	whole func(file) *state
	// grow reads a file on from where the kept parse of it left off, when
	// the file has only grown since (tail.go), else whole
	grow   func(file, *state) *state
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
		return sessionParser{grow: parseDsh}
	case "cline":
		return sessionParser{grow: parseCline}
	case "grok":
		return sessionParser{grow: parseGrok}
	case "kimi":
		return sessionParser{grow: parseKimi}
	case "droid":
		return sessionParser{grow: parseDroid}
	case "agy":
		return sessionParser{grow: parseAgy}
	case "antigravity":
		return sessionParser{whole: parseAntigravity} // #2190
	case "cursor":
		return sessionParser{whole: parseCursor}
	case "codex":
		return sessionParser{line: codexBody}
	case "pi", "raft", "cindy", "omp":
		return sessionParser{line: piParse}
	case "workbuddy", "workbuddy-ai", "codebuddy":
		return sessionParser{line: workbuddyLine}
	default:
		return sessionParser{line: claudeLine, claude: true}
	}
}

// parse reads a file on from where old left it, or from the start.
func parse(f file, old *state) *state { return parseWith(f, old, nil) }

// parseWith is parse with calls, when not nil, reading the file's calls on
// the same pass.
func parseWith(f file, old *state, calls *callsRide) *state {
	s := parseRead(f, old, calls)
	if s != nil && s.StateMore.zero() {
		s.StateMore = nil // only empty values were written through own()
	}
	return s
}

func parseRead(f file, old *state, calls *callsRide) *state {
	defer func() {
		if calls != nil {
			calls.release()
		}
	}()
	// Summary caches contain aggregates only. After a restart, a changed
	// Codex file rebuilds its transient response index from the source.
	if f.agent == "codex" && old != nil && old.Codex == nil {
		old = nil
	}
	parser := parserFor(f.agent)
	if parser.whole != nil {
		return parser.whole(f)
	}
	if parser.grow != nil {
		return parser.grow(f, old)
	}
	// Every source using claudeLine must rebuild a missing transient window.
	if parser.claude && old != nil && old.Claude == nil {
		old = nil
	}
	if ccStale(f, old) {
		old = nil
	}

	headBytes := headOf(f.path)
	var s *state
	if old != nil && !packed(f.path) && f.size >= old.Size && old.Off <= f.size && sameHead(headBytes, old.Head, int(old.HeadSize)) && old.ContentHash != (digest{}) && prefixHash(f.path, old.Size) == old.ContentHash {
		s = old.clone()
	} else {
		s = &state{}
	}
	s.Size, s.Mod = f.size, f.mod.UnixNano()
	s.Head, s.HeadSize = headDigest(headBytes), int32(len(headBytes))
	s.CdRead = parser.claude
	s.ContentHash = prefixHash(f.path, f.size)
	var head func([]byte) bool
	if f.agent == "codex" {
		s.codexPath = f.path
		head = func(b []byte) bool { return codexHead(s, b, f.main) }
	}
	line := func(b []byte, _, _ int64) bool {
		parser.line(s, b, f.main)
		return true
	}
	if calls != nil && (s.Off != 0 || calls.st.Off != 0) {
		calls.release() // a read goes on from where it was
		calls = nil
	}
	if calls != nil {
		head, line = calls.ride(head, line)
	}
	var thin func([]byte) (thinner, bool)
	switch {
	case parser.claude:
		thin = thinClaude
	case f.agent == "codex":
		thin = thinCodex
	}
	off, err := scanThin(f.path, s.Off, head, thin, line)
	if err == nil {
		s.Off = off
	}
	if calls != nil {
		calls.finish(off, err)
	}
	if isBuddy(f.agent) && s.Cwd == "" {
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
// scanHook, when a test sets it, is told where each scan of a file starts.
var scanHook func(path string, off int64)

func scanHead(path string, off int64, head func([]byte) bool, fn func([]byte)) (int64, error) {
	return scanAt(path, off, head, func(b []byte, _, _ int64) bool { fn(b); return true })
}

// scanAt is scanHead telling fn where each line starts and ends in the file,
// and stopping when fn says so; what it returns is where the last line it
// handled ended.
// A compressed file's places are those of its lines decompressed; it is read
// from its start, what comes before off passed over.
func scanAt(path string, off int64, head func([]byte) bool, fn func(b []byte, start, end int64) bool) (int64, error) {
	return scanThin(path, off, head, nil, fn)
}

// scanThin is scanAt handing fn a long line thin says yes to, from its
// first chunk, with its strings cut to thinKeep bytes (thinner):
// what the line holds besides long strings is kept as it was, and the
// line is never gathered whole. A nil thin thins nothing.
func scanThin(path string, off int64, head func([]byte) bool, thin func([]byte) (thinner, bool), fn func(b []byte, start, end int64) bool) (int64, error) {
	if scanHook != nil {
		scanHook(path, off)
	}
	f, err := openLines(path)
	if err != nil {
		return off, err
	}
	defer f.Close()
	if s, ok := f.(io.Seeker); ok {
		if _, err := s.Seek(off, io.SeekStart); err != nil {
			return off, err
		}
	}
	r, release := lineReader(f, scanBuffer(f, off))
	defer release()
	if _, ok := f.(io.Seeker); !ok && off > 0 {
		// passed over through the reader the lines are read with, not a
		// copy's buffer of its own
		if _, err := r.Discard(int(off)); err != nil {
			if errors.Is(err, io.EOF) {
				return off, nil
			}
			return off, err
		}
	}
	pf, plain := f.(*os.File)
	var long []byte
	var kept *[]byte // long's buffer, from longLines
	defer func() { giveLong(kept, long) }()
	var parts blocks // a packed file's long line so far
	defer parts.release()
	var n int64 // bytes of the line so far
	skip, seen := false, false
	var th thinner // the line's state while it is thinned
	thinning := false
	for {
		chunk, err := r.ReadSlice('\n')
		first := n == 0
		n += int64(len(chunk))
		if errors.Is(err, bufio.ErrBufferFull) {
			if !seen && head != nil {
				seen = true
				skip = skip || !head(chunk)
			}
			if first && !skip && thin != nil {
				th, thinning = thin(chunk)
			}
			if thinning {
				if n > maxLine {
					skip, thinning, long = true, false, long[:0]
				} else if !skip {
					long = th.add(long, chunk)
				}
				continue
			}
			if plain && !skip && len(long) == 0 {
				// gathered in one buffer of the line's own size, not grown a
				// chunk at a time (~5 times the line allocated, #2515)
				if l := lineLen(pf, off, maxLine+r.Size()); l > maxLine+r.Size() {
					skip = true
				} else if l > cap(long) {
					kept, long = takeLong(l)
				}
			}
			switch {
			case skip || len(long)+parts.n+len(chunk) > maxLine:
				skip, long = true, long[:0]
				parts.release()
			case plain:
				long = append(long, chunk...)
			default:
				// a packed file's line has no length to read ahead: it is
				// kept in pooled blocks until its end, then gathered once
				// at its own size. Grown by append it allocated ~3.8 times
				// the line, most of what a cold read of packed rollouts
				// with pasted images allocated.
				parts.add(chunk)
			}
			continue
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return off, nil
			}
			return off, err
		}
		if parts.n > 0 {
			if l := parts.n + len(chunk); l > cap(long) {
				kept, long = takeLong(l)
			}
			long = parts.appendTo(long[:0])
			parts.release()
		}
		b := chunk
		if thinning {
			long = th.add(long, chunk)
			b = long
		} else if len(long) > 0 {
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
		n, skip, seen, thinning, long = 0, false, false, false, long[:0]
	}
}

// longLines keep the buffers scanAt gathers long lines in between scans:
// a line handed out from one is only good until the scan ends, as one
// from its read buffer is.
var longLines sync.Pool

// takeLong is an empty buffer of at least l bytes, from the pool when it
// has one that large (the one a scan had, too small, is let go).
func takeLong(l int) (*[]byte, []byte) {
	if p, ok := longShelf.take(func(p *[]byte) bool { return cap(*p) >= l }); ok {
		return p, (*p)[:0]
	}
	if p, _ := longLines.Get().(*[]byte); p != nil {
		if cap(*p) >= l {
			return p, (*p)[:0]
		}
		longLines.Put(p)
	}
	b := make([]byte, 0, l)
	return &b, b
}

// giveLong gives the buffer long was gathered in back to the pool.
func giveLong(p *[]byte, long []byte) {
	if p == nil {
		return
	}
	*p = long[:cap(long)]
	if poisonReleased {
		for i := range *p {
			(*p)[i] = 0xFF
		}
	}
	*p = (*p)[:0]
	if cap(*p) <= longShelved {
		// kept in place of a smaller one when the shelf is full
		out, kept, swapped := longShelf.give(p, func(h, p *[]byte) bool { return cap(*h) < cap(*p) })
		if !kept {
			longLines.Put(p)
		} else if swapped {
			longLines.Put(out)
		}
		return
	}
	longLines.Put(p)
}

// longShelf keeps long-line buffers up to longShelved bytes across garbage
// collections; a longer one, rare, is left to longLines.
var longShelf shelf[*[]byte]

const longShelved = 4 << 20

// blocks hold a long line read from a packed file until its end is found:
// copied into 1 MiB blocks kept between scans, so the line is allocated
// only once, at its own length.
type blocks struct {
	b []*[]byte
	n int // bytes held
}

var gatherBlocks = sync.Pool{New: func() any { b := make([]byte, 0, 1<<20); return &b }}

func (g *blocks) add(p []byte) {
	g.n += len(p)
	for len(p) > 0 {
		if len(g.b) == 0 || len(*g.b[len(g.b)-1]) == cap(*g.b[len(g.b)-1]) {
			g.b = append(g.b, gatherBlocks.Get().(*[]byte))
		}
		last := g.b[len(g.b)-1]
		k := min(len(p), cap(*last)-len(*last))
		*last = append(*last, p[:k]...)
		p = p[k:]
	}
}

func (g *blocks) appendTo(dst []byte) []byte {
	for _, b := range g.b {
		dst = append(dst, *b...)
	}
	return dst
}

func (g *blocks) release() {
	for _, b := range g.b {
		*b = (*b)[:0]
		gatherBlocks.Put(b)
	}
	g.b, g.n = g.b[:0], 0
}

// lineLen is the length, its newline included, of the line at off in f,
// read ahead without moving f: 0 when it has no end yet, and past most
// (but no further) for a longer one.
func lineLen(f *os.File, off int64, most int) int {
	buf := make([]byte, 64<<10)
	n := 0
	for n <= most {
		k, err := f.ReadAt(buf, off+int64(n))
		if i := bytes.IndexByte(buf[:k], '\n'); i >= 0 {
			return n + i + 1
		}
		n += k
		if err != nil {
			return 0
		}
	}
	return n
}

// scanBuffer is the read buffer a reader of f needs for what is left of it
// from off: 1 MiB at most, and no more than that rest of a plain file.
// Every line reader of a session file sizes its buffer by it. Every scan
// took 1 MiB, and a start's read of ~4,600 mostly small session files
// (each read again from where it was left) allocated ~9 GB of them, which
// the GC's heap target followed up (#2776). A line longer than the buffer
// (a file grown since) is still read whole, through scanAt's long line.
func scanBuffer(f io.Reader, off int64) int {
	const most, least = 1 << 20, 16 << 10
	if pf, ok := f.(*os.File); ok {
		if info, err := pf.Stat(); err == nil {
			return int(min(most, max(least, info.Size()-off+1)))
		}
	}
	return most
}

// lineReaders keep scanAt's read buffers between scans, one pool per
// power-of-two size from scanBuffer's 16 KiB to its 1 MiB. Every scan
// allocated its own: a live magpie's start read ~3,200 Claude Code subagent
// files for their first lines (ccTask), most over 1 MiB, and with the
// session and call reads scanAt allocated 6.3 GB in 48 minutes, 3.8 GB of
// it these buffers, while ~157 MB was live. A pool keeps about one buffer
// per reader running at once, and lets idle ones go at the next GCs.
var lineReaders [7]sync.Pool

// lineShelves keep a few of each first, across garbage collections.
var lineShelves [7]shelf[*bufio.Reader]

// poisonReleased, set by the tests, fills a buffer given back with 0xFF, so
// a reader that kept a line past its scan reads garbage and fails.
var poisonReleased = false

// lineReader is a reader of f with a buffer of at least size bytes (at
// most 1 MiB); release gives the buffer back. A line handed out from it is
// only good until release: one kept longer is copied.
func lineReader(f io.Reader, size int) (*bufio.Reader, func()) {
	class := 0
	for class < len(lineReaders)-1 && 16<<10<<class < size {
		class++
	}
	r, _ := lineShelves[class].take(nil)
	if r == nil {
		r, _ = lineReaders[class].Get().(*bufio.Reader)
	}
	if r == nil {
		r = bufio.NewReaderSize(nil, 16<<10<<class)
	}
	r.Reset(f)
	return r, func() {
		if poisonReleased {
			r.Reset(poison{})
			r.Peek(r.Size())
		}
		r.Reset(nil)
		if _, kept, _ := lineShelves[class].give(r, nil); !kept {
			lineReaders[class].Put(r)
		}
	}
}

type poison struct{}

func (poison) Read(b []byte) (int, error) {
	for i := range b {
		b[i] = 0xFF
	}
	return len(b), nil
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
	d, ok := streamShelf.take(nil)
	if !ok {
		d = streams.Get().(*zstd.Decoder)
	}
	if err := d.Reset(f); err != nil {
		f.Close()
		return nil, err
	}
	return &zstdFile{d: d, f: f}, nil
}

// streams holds decoders between packed reads. A decoder made for each
// read allocated its window (a packed rollout's is 2 MiB, held in 3 MiB at
// low memory) and its block buffers every time: 75 GB of a live magpie's
// first read of 11,591 rollouts. One kept here keeps them, and is let go
// with the pool's other idle ones by the garbage collector. streamShelf
// keeps a few first, across collections.
var streamShelf shelf[*zstd.Decoder]

var streams = sync.Pool{New: func() any {
	d, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderLowmem(true))
	if err != nil {
		panic(err) // only for options it doesn't know
	}
	return d
}}

// zstdFile is a packed file's lines, closed with the file. Its decoder goes
// back to the pool at Close, which a read after can't reach.
type zstdFile struct {
	d *zstd.Decoder
	f *os.File
}

func (z *zstdFile) Read(p []byte) (int, error) {
	if z.d == nil {
		return 0, os.ErrClosed
	}
	return z.d.Read(p)
}

func (z *zstdFile) Close() error {
	if z.d == nil {
		return os.ErrClosed
	}
	z.d.Reset(nil)
	if _, kept, _ := streamShelf.give(z.d, nil); !kept {
		streams.Put(z.d)
	}
	z.d = nil
	return z.f.Close()
}

// lastIndex is bytes.LastIndex(b, key), found going forward. LastIndex
// goes back a byte at a time (Rabin-Karp), and over Claude Code's lines,
// one holding a tool's whole output, it was ~8 s of a first start's CPU
// on a 14 GB history (#2278); Index skips ahead with the CPU's vector
// instructions. The line's tail is looked at first, where Claude Code
// writes the timestamp.
func lastIndex(b, key []byte) int {
	const tail = 1 << 10
	if len(key) == 0 {
		return len(b)
	}
	if len(b) > tail {
		if i := forwardLast(b[len(b)-tail:], key); i >= 0 {
			return len(b) - tail + i
		}
	}
	return forwardLast(b, key)
}

// forwardLast is the last key in b, found by Index from each one before;
// even a 1 KB tail took LastIndex ~1 µs.
func forwardLast(b, key []byte) int {
	last := -1
	for off := 0; off < len(b); {
		i := bytes.Index(b[off:], key)
		if i < 0 {
			break
		}
		last = off + i
		off = last + 1
	}
	return last
}

// tsAt reads the time of a line from its first (or last) "timestamp" key,
// without decoding the rest of it.
func tsAt(b []byte, last bool) time.Time {
	key := []byte(`"timestamp":"`)
	// the first is looked for only when it is the one asked: found before
	// each last one too, it was ~2 s of a first start's CPU (#2278)
	var i int
	if last {
		i = lastIndex(b, key)
	} else {
		i = bytes.Index(b, key)
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

var tag = lazyre.New(`</?[A-Za-z][\w-]*[^<>]*>`)

// untagged is a message the agent or a harness wrapped in tags, as words.
func untagged(s string) string { return title(tag.ReplaceAllString(s, " ")) }

// title is a prompt made one short line: each run of ASCII white space (as
// regexp's \s) one space, trimmed, and cut at 160 characters. It reads only
// as far as it keeps, and one past: a pasted file as a prompt went through
// a regexp and into runes whole, 0.45 GB of a cold start's first 2 minutes.
func title(s string) string {
	t, _ := titleCut(s)
	return t
}

// titleCut is title(s), and whether words were left past what it kept.
func titleCut(s string) (string, bool) {
	const most = 160
	s = strings.TrimLeftFunc(s, unicode.IsSpace)
	b := make([]byte, 0, min(len(s), 4*most))
	n, in, i := 0, false, 0 // runes kept; in a run of \s
	for ; i < len(s) && n < most; i++ {
		switch c := s[i]; c {
		case '\t', '\n', '\f', '\r', ' ':
			if !in {
				b, n, in = append(b, ' '), n+1, true
			}
		default:
			_, w := utf8.DecodeRuneInString(s[i:])
			b, n, in = append(b, s[i:i+w]...), n+1, false
			i += w - 1
		}
	}
	if strings.IndexFunc(s[i:], func(r rune) bool { return !unicode.IsSpace(r) }) < 0 {
		return strings.TrimRightFunc(string(b), unicode.IsSpace), false
	}
	// as []rune made it: each byte not UTF-8 one U+FFFD
	return strings.TrimSpace(string([]rune(string(b)))) + "…", true
}

// ---- pricing ----------------------------------------------------------------

var dated = lazyre.New(`-\d{8}$`)

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
		// without a rate window's rate (#2463): a session sums its calls by
		// model, not by time, so the window of the hour it is read in
		// would price all of them at it, and the same session read 3× at
		// the peak and 1× off it
		if pr, ok, _ := provider.BasePriceIn(s, pid, rest); ok {
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
	// Grok Build's fast variant (grok-4.7-build-fast), which only the Grok
	// CLI names, at twice its model's price, as a Grok account's call on it
	// is (TradingLife on Discord)
	if base, ok := provider.GrokBuildFast(bare); ok {
		if pr, ok := catalog.PricedBy(makers(base), base); ok {
			return pr.Times(provider.GrokFastTimes), true
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

var safeID = lazyre.New(`^[0-9A-Za-z_-]+$`)
var safeOmpProfile = lazyre.New(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// ResumeCommand is the shell line that picks a session up again in its
// folder, or "" for an id that isn't plain.
func ResumeCommand(agent, id, cwd string) string { return resumeCommand("", agent, id, cwd, "") }

// resumeCommand is ResumeCommand for a session in a WSL distro too, when
// distro is set: see wslResume.
// resumeFor is resumeCommand for a listed row: none for an agent whose
// command is gone from this computer, which the line would fail to start.
// A WSL distro's agent is looked for there, not here.
func resumeFor(distro, agent, id, cwd, path string) string {
	if distro == "" && cliGone(agent) {
		return ""
	}
	return resumeCommand(distro, agent, id, cwd, path)
}

func resumeCommand(distro, agent, id, cwd, path string) string {
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
	case "codebuddy":
		// its CLI's -r, --resume [sessionId], one bin of the package's
		// three (codebuddy, cbc, codebuddy-code)
		run = "codebuddy --resume " + id
	case "omp":
		run = "omp --resume " + id
		if profile := ompProfile(path); profile != "" {
			if !safeOmpProfile.MatchString(profile) {
				return ""
			}
			run = "omp --profile " + profile + " --resume " + id
		}
	case "droid":
		run = "droid --resume " + id
	case "kimi":
		run = "kimi --session " + id
	case "agy":
		// --conversation picks it up from any folder; one started on
		// magpie's gateway needs the variables and --model it was
		// started with
		run = "agy --conversation " + id
		if AgyStart != nil && distro == "" {
			if start := AgyStart(); start != "" {
				run = start + " --conversation " + id
			}
		}
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
// under agent:id. One nothing was said in is found too, as ListAgent lists
// it: the Sessions page shows it, and its conversation, Markdown and
// Terminal open from it as from any other (#2273).
func Find(agent, id string) (Session, bool) {
	alias := provider.UsageAlias()
	s, ok := findListed(agent, id)
	if ok && alias != nil {
		aliasSession(&s, alias)
	}
	return s, ok
}

func findListed(agent, id string) (Session, bool) {
	for _, s := range listSessions(0) {
		if s.Agent == agent && s.ID == id {
			return s, true
		}
	}
	if s, ok, found := get(agent + ":" + id); found && s.ID == id {
		if !ok {
			listed(&s)
		}
		return s, true
	}
	return Session{}, false
}

// Titles is the title of each session named by key (agent:id, as Get takes
// it) that has its files on this computer: the name it was given, else the
// agent's own title, else its first prompt. A key with no files, or a
// session with no title, is left out. Desktop's local metadata can name a
// session without its CLI transcript. The Routing page uses this too (#1293).
// A ZCode request names its session without the sess_ prefix its database
// gives it, so that id names the session too.
func Titles(keys []string) map[string]string {
	out := map[string]string{}
	if len(keys) == 0 {
		return out
	}
	// A ZCode session the CLI database has no row for (the app keeps its
	// tasks in an index of its own) is still named as the app names it.
	defer func() {
		for _, k := range keys {
			if out[k] != "" {
				continue
			}
			if agent, id, ok := strings.Cut(k, ":"); ok && agent == "zcode" {
				if t, ok := zcodeTaskOf("sess_" + zcodeBareID(id)); ok && title(t.title) != "" && !ocDefaultTitle(title(t.title)) {
					out[k] = t.title
				}
			}
		}
	}()
	want := make(map[string]bool, len(keys))
	// alias maps the key a file is grouped under back to the key asked for:
	// ZCode's request id, without the prefix its database gives it.
	alias := map[string]string{}
	var desktopIDs, piDesktopIDs []string
	for _, k := range keys {
		want[k] = true
		if agent, id, ok := strings.Cut(k, ":"); ok && agent == "zcode" && !strings.HasPrefix(id, "sess_") {
			full := agent + ":sess_" + id
			want[full] = true
			alias[full] = k
		}
		if id, ok := strings.CutPrefix(k, "claude-desktop:"); ok {
			desktopIDs = append(desktopIDs, id)
			continue
		}
		// PI-Desktop names its chats in pi.sqlite, which none of their files
		// holds (#1967); a request it sent without a User-Agent of its own
		// was counted as Pi's, so both of its names are looked up
		if agent, id, ok := strings.Cut(k, ":"); ok && isPIDesktop(agent) {
			piDesktopIDs = append(piDesktopIDs, id)
		}
	}
	for id, name := range DesktopTitles(desktopIDs) {
		out["claude-desktop:"+id] = name
	}
	if len(piDesktopIDs) > 0 {
		names := PIDesktopTitles(piDesktopIDs)
		for _, k := range keys {
			if agent, id, ok := strings.Cut(k, ":"); ok && isPIDesktop(agent) {
				if name := names[id]; name != "" {
					out[k] = name
				}
			}
		}
	}
	mu.Lock()
	defer mu.Unlock()
	loadCache()
	defer useDBs()()
	files := allFilesForTitles()
	// ZCode files a subagent as sess_subagent_agent_<id> and a fork as
	// sess_<id> with the parent's id in parent_id; ocRoots files both under
	// the parent session's key. A request names either by its own id, so its
	// title is read from its own file when no group carries the key asked
	// for.
	// A top-level session is named by its own group, so only child
	// sessions are indexed.
	subFiles := map[string]*file{} // a ZCode child session's own id → its file
	for i := range files {
		f := &files[i]
		if f.agent != "zcode" || f.m().sid == "" || f.m().parent == "" {
			continue
		}
		subFiles[f.m().sid] = f
		subFiles[zcodeBareID(f.m().sid)] = f
	}
	subAsked := map[string]string{} // the key asked for → the child's file
	for _, k := range keys {
		agent, id, ok := strings.Cut(k, ":")
		if !ok || agent != "zcode" {
			continue
		}
		if f := subFiles[zcodeBareID(id)]; f != nil {
			want[f.key] = true
			subAsked[k] = f.path
		}
	}
	files = zcodeTitleStamps(files, want)
	groups := byKey(files, func(f *file) bool { return want[f.key] })
	var fs []file
	for _, f := range files {
		if want[f.key] {
			fs = append(fs, f)
		}
	}
	if len(fs) == 0 {
		return out
	}
	refresh(fs, files)
	parents := codexTaskParentFiles(fs, files)
	if len(parents) > 0 {
		refresh(parents, files)
	}
	titleFiles := append(append([]file(nil), fs...), parents...)
	taskTitles := codexSubagentTaskTitles(titleFiles)
	identities := codexIdentitySnapshot(fs)
	price, titles := pricer(), codexTitleOf()
	for k, g := range groups {
		if s, ok := assembleListed(g, price, titles); ok {
			setSessionCodexIdentity(&s, identities)
			setSessionCodexTaskTitle(&s, taskTitles)
			if s.Title == "" {
				continue
			}
			out[k] = s.Title
			if asked := alias[k]; asked != "" {
				out[asked] = s.Title
			}
		}
	}
	for k, path := range subAsked {
		if out[k] != "" {
			continue
		}
		if st := cache[path]; st != nil {
			if name := cmp.Or(st.more().Named, st.Title, st.more().First); name != "" {
				out[k] = name
			}
		}
	}
	return out
}

// zcodeBareID is a ZCode session id without the prefix its database gives
// it: a subagent's sess_subagent_agent_, any other session's sess_. A
// request names a session by the bare id.
func zcodeBareID(id string) string {
	if bare, ok := strings.CutPrefix(id, "sess_subagent_agent_"); ok {
		return bare
	}
	return strings.TrimPrefix(id, "sess_")
}

// Get is the session whose files are grouped under key (a Summary's Key),
// however long ago it was at work.
func Get(key string) (Session, bool) {
	alias := provider.UsageAlias()
	s, ok, _ := get(key)
	if ok && alias != nil {
		aliasSession(&s, alias)
	}
	return s, ok
}

// get is Get, and whether key has files at all: ok is false for a session
// nothing was said in, which found still says is there.
func get(key string) (s Session, ok, found bool) {
	mu.Lock()
	defer mu.Unlock()
	loadCache()
	defer useDBs()()
	files := allFiles()
	var fs []file
	for _, f := range files {
		if f.key == key {
			fs = append(fs, f)
		}
	}
	if len(fs) == 0 {
		return Session{}, false, false
	}
	var identities map[string]CodexIdentity
	var taskTitles map[codexTaskTitle]string
	if strings.HasPrefix(key, "codex:") {
		var codexFiles []file
		for _, f := range files {
			if f.agent == "codex" {
				codexFiles = append(codexFiles, f)
			}
		}
		refresh(codexFiles, files)
		identities = codexIdentitySnapshot(codexFiles)
		taskTitles = codexSubagentTaskTitles(codexFiles)
	} else {
		refresh(fs, files)
	}
	s, ok = assemble(fs, pricer(), codexTitleOf())
	setSessionCodexIdentity(&s, identities)
	setSessionCodexTaskTitle(&s, taskTitles)
	if parserFor(fs[0].agent).claude {
		// judged with the files of its agent cache has read, as the
		// listing judges them
		var mine []file
		groups := map[string][]file{}
		for _, f := range files {
			if f.agent == fs[0].agent {
				mine = append(mine, f)
				groups[f.key] = append(groups[f.key], f)
			}
		}
		_, branches := ccFamily(mine)
		setBranch(&s, branches[key], groups)
	}
	return s, ok, true
}

// setBranch marks s a branch of the session by the key head (ccFamily):
// nested under it, and read from its start (TranscriptOf). "" leaves s.
func setBranch(s *Session, head string, groups map[string][]file) {
	g := groups[head]
	if head == "" || len(g) == 0 {
		return
	}
	s.Role, s.ParentSession = "branch", sessionID(g)
	for _, f := range g {
		if f.main {
			s.BranchOf = f.path
			break
		}
	}
}
