package sessions

import (
	"bytes"
	"cmp"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/appdir"
)

// LiveAgent's engine, K-brain, keeps a session in a folder of its own
// under sessions in its data folder (LiveAgentDir), in a folder per
// project: <root>/sessions/<project>/<id>/session.jsonl (older ones right
// in sessions: <root>/sessions/<id>/session.jsonl). sessions/.runs and
// sessions/.deleted-history are its own, not sessions. Read in K-brain
// ac94873 (internal/session/transcript.go, location.go).
//
// K-brain writes the whole file again on every save, through a temp file
// renamed over it, so a read finds it whole. Each line is
// {"type", "payload"}. The first is session_meta: the id, title (the
// first prompt, cut at 64, until renamed), model and provider, cwd,
// created_at and updated_at, the session it was forked from (forked_from,
// with fork_seq the last message it copied) and, for a subagent's, task_id.
// It holds what the session spent so far: usage_in (prompt tokens, cache
// reads and writes in them), usage_cached, usage_out, usage_cache_write,
// and the same by "<model> @ <provider>" in model_usage; what its
// subagents spent is in sub_usage, by the same key, and in neither total.
// "message" lines follow, by seq: {"seq", "message"}, an OpenAI-shaped
// message with id, role (user, assistant, tool, system), content (a string
// or parts), reasoning, tool_calls, tool_call_id and name, sent_at, and on
// an assistant's reply the call's usage and its "<model> @ <provider>".
// A user message the user typed is authored; one with origin "background"
// is a runtime notice. compaction, task, snapshot and schedule lines come
// after.
//
// A reply's usage counts on the day it came back. What the meta's totals
// hold beyond the replies in the file — a compaction's summary call, a
// reply rewound and dropped, a subagent's (sub_usage) — counts on the day
// of the last message, as Droid's total does. A fork starts with copies of
// its source's messages up to fork_seq, and once it runs its totals start
// from what those copies spent, so neither counts in it. A subagent's own
// session (task-<parent>-<task>, task_id set) is filed under its parent's:
// its spend is the parent's sub_usage already, so only its activity counts.
// The gateway's own record of LiveAgent's calls carries no session, so
// nothing here is counted twice. LiveAgent has no resume command.

// LiveAgentDir is LiveAgent's data folder: $LIVEAGENT_HOME, else
// $K_BRAIN_HOME (each only an absolute path), else the folder
// ~/.liveagent/data-dir.json points to, else ~/.liveagent.
func LiveAgentDir() string {
	home, _ := os.UserHomeDir()
	return LiveAgentDirIn(home)
}

// LiveAgentDirIn is LiveAgentDir for the home folder given. It is the one
// rule agent.liveAgentDir uses too.
func LiveAgentDirIn(home string) string {
	for _, v := range []string{"LIVEAGENT_HOME", "K_BRAIN_HOME"} {
		if d := strings.TrimSpace(appdir.Getenv(v)); d != "" && filepath.IsAbs(d) {
			return d
		}
	}
	def := filepath.Join(home, ".liveagent")
	if b, err := os.ReadFile(filepath.Join(def, "data-dir.json")); err == nil {
		var p struct {
			Path string `json:"path"`
		}
		if json.Unmarshal(b, &p) == nil && filepath.IsAbs(p.Path) && filepath.Clean(p.Path) != filepath.Clean(def) {
			return p.Path
		}
	}
	return def
}

func liveagentSessions() string { return filepath.Join(LiveAgentDir(), "sessions") }

type liveagentUsage struct {
	Prompt     int `json:"prompt_tokens"`
	Completion int `json:"completion_tokens"`
	Details    *struct {
		Cached int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CacheHit   int `json:"prompt_cache_hit_tokens"`
	CacheWrite int `json:"prompt_cache_write_tokens"`
}

// tokens are K-brain's counts as magpie keeps them: input without what
// was read from or written to the cache (ai.Usage's Cached, CacheWrite).
func (u liveagentUsage) tokens() Tokens {
	read := u.CacheHit
	if u.Details != nil && u.Details.Cached > 0 {
		read = u.Details.Cached
	}
	return Tokens{Input: max(0, u.Prompt-read-u.CacheWrite), Output: u.Completion, CacheRead: read, CacheWrite: u.CacheWrite}
}

type liveagentMeta struct {
	ID         string                    `json:"id"`
	Title      string                    `json:"title"`
	Model      string                    `json:"model"`
	Provider   string                    `json:"provider"`
	Cwd        string                    `json:"cwd"`
	ForkedFrom string                    `json:"forked_from"`
	ForkSeq    int                       `json:"fork_seq"`
	TaskID     string                    `json:"task_id"`
	In         int                       `json:"usage_in"`
	Cached     int                       `json:"usage_cached"`
	Out        int                       `json:"usage_out"`
	CacheWrite int                       `json:"usage_cache_write"`
	Models     map[string]liveagentUsage `json:"model_usage"`
	Subs       map[string]liveagentUsage `json:"sub_usage"`
	Created    time.Time                 `json:"created_at"`
	Updated    time.Time                 `json:"updated_at"`
}

type liveagentToolCall struct {
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type liveagentMessage struct {
	Role       string              `json:"role"`
	Content    json.RawMessage     `json:"content"`
	Reasoning  string              `json:"reasoning"`
	ToolCalls  []liveagentToolCall `json:"tool_calls"`
	ToolCallID string              `json:"tool_call_id"`
	Name       string              `json:"name"`
	Authored   bool                `json:"authored"`
	Origin     string              `json:"origin"`
	SentAt     *time.Time          `json:"sent_at"`
	Usage      *liveagentUsage     `json:"usage"`
	Model      string              `json:"model"`
}

type liveagentLine struct {
	Type    string `json:"type"`
	Payload struct {
		liveagentMeta
		Seq     int              `json:"seq"`
		Message liveagentMessage `json:"message"`
	} `json:"payload"`
}

// readLiveAgentMeta is a session's session_meta line.
func readLiveAgentMeta(path string) (liveagentMeta, bool) {
	var l liveagentLine
	ok := false
	scanAt(path, 0, nil, func(b []byte, _, _ int64) bool {
		ok = json.Unmarshal(b, &l) == nil && l.Type == "session_meta"
		return false
	})
	return l.Payload.liveagentMeta, ok
}

// liveagentParents is whose subagent each task-* session is, as its meta
// said when the listing last read it.
var liveagentParents listMemo[string]

func liveagentFiles() []file {
	if !Reads("liveagent") {
		return nil
	}
	root := liveagentSessions()
	a, _ := filepath.Glob(filepath.Join(root, "*", "*", "session.jsonl"))
	b, _ := filepath.Glob(filepath.Join(root, "*", "session.jsonl"))
	parents := liveagentParents.round()
	defer parents.done()
	var out []file
	for _, p := range append(a, b...) {
		dir := filepath.Dir(p)
		id := filepath.Base(dir)
		rel, err := filepath.Rel(root, dir)
		if err != nil || strings.HasPrefix(rel, ".") || !safeID.MatchString(id) {
			continue // .runs, .deleted-history
		}
		f := file{agent: "liveagent", key: "liveagent:" + id, path: p, main: true}
		if !stat(&f) {
			continue
		}
		if strings.HasPrefix(id, "task-") {
			parent, _ := parents.get(p, f.size, f.mod.Time(), func() (string, bool) {
				m, ok := readLiveAgentMeta(p)
				if m.TaskID == "" {
					return "", ok
				}
				return m.ForkedFrom, ok
			})
			if parent != "" && parent != id && safeID.MatchString(parent) {
				f.key, f.main = "liveagent:"+parent, false
			}
		}
		out = append(out, f)
	}
	return out
}

// liveagentModel is the model a "<model> @ <provider>" key names.
func liveagentModel(label string) string {
	if i := strings.LastIndex(label, " @ "); i >= 0 {
		label = label[:i]
	}
	return strings.TrimSpace(label)
}

// parseLiveAgent reads a session's file whole: K-brain writes it whole.
func parseLiveAgent(f file) *state {
	s := &state{Size: f.size, Mod: f.mod.UnixNano()}
	var m liveagentMeta
	started := false
	var said time.Time             // the last message's time
	counted := map[string]Tokens{} // what replies counted, by key
	copied := map[string]Tokens{}  // what a fork's copies spent, by key
	scanAt(f.path, 0, nil, func(b []byte, _, _ int64) bool {
		var l liveagentLine
		if json.Unmarshal(b, &l) != nil {
			return true
		}
		if !started {
			started = true
			if l.Type != "session_meta" {
				return false
			}
			m = l.Payload.liveagentMeta
			if f.main {
				s.ID, s.Cwd, s.own().Named = m.ID, m.Cwd, title(m.Title)
			}
			s.saw(m.Created, f.main)
			return true
		}
		if l.Type != "message" {
			return true
		}
		msg := l.Payload.Message
		var at time.Time
		if msg.SentAt != nil {
			at = *msg.SentAt
		}
		label := msg.Model
		if label == "" {
			label = m.Model + " @ " + m.Provider
		}
		if m.ForkedFrom != "" && m.TaskID == "" && l.Payload.Seq <= m.ForkSeq {
			// a copy of the source's: counted there
			if msg.Usage != nil {
				t := copied[label]
				t.add(msg.Usage.tokens())
				copied[label] = t
			}
			return true
		}
		s.saw(at, f.main)
		if at.After(said) {
			said = at
		}
		date := dateOf(at)
		switch msg.Role {
		case "user":
			text := liveagentText(msg.Content)
			if !f.main || !msg.Authored || msg.Origin == "background" || strings.TrimSpace(text) == "" {
				return true
			}
			s.day(date).Prompts++
			if s.Title == "" {
				s.Title = title(text)
			}
		case "assistant":
			if f.main {
				s.day(date).Replies++
			}
			for _, c := range msg.ToolCalls {
				s.tool(at, c.Function.Name, liveagentSkill(c.Function.Name, c.Function.Arguments))
			}
			if msg.Usage != nil && f.main && date != "" {
				t := msg.Usage.tokens()
				s.use(date, liveagentModel(label), t)
				c := counted[label]
				c.add(t)
				counted[label] = c
			}
		}
		return true
	})
	if !started || !f.main {
		return s
	}
	// what the totals hold beyond the replies counted: on the last day
	totals := map[string]Tokens{}
	for k, u := range m.Models {
		totals[k] = u.tokens()
	}
	if len(m.Models) == 0 && (m.In != 0 || m.Out != 0) {
		u := liveagentUsage{Prompt: m.In, Completion: m.Out, CacheHit: m.Cached, CacheWrite: m.CacheWrite}
		totals[m.Model+" @ "+m.Provider] = u.tokens()
	}
	for k := range totals {
		t := totals[k]
		t.sub(copied[k])
		t.sub(counted[k])
		totals[k] = t
	}
	for k, u := range m.Subs {
		t := totals[k]
		t.add(u.tokens())
		totals[k] = t
	}
	if said.IsZero() {
		s.saw(m.Updated, true)
	}
	when := said
	if when.IsZero() {
		when = s.Last
	}
	if when.IsZero() {
		when = time.Unix(0, s.Mod)
	}
	for k, t := range totals {
		t = Tokens{Input: max(0, t.Input), Output: max(0, t.Output), CacheRead: max(0, t.CacheRead), CacheWrite: max(0, t.CacheWrite)}
		s.use(dateOf(when), liveagentModel(k), t)
	}
	return s
}

// liveagentText is a message's words: its content as a string, or its
// text parts.
func liveagentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	json.Unmarshal(raw, &parts)
	var out []string
	for _, p := range parts {
		if p.Type == "text" && strings.TrimSpace(p.Text) != "" {
			out = append(out, p.Text)
		}
	}
	return strings.Join(out, "\n")
}

// liveagentSkill is the skill a tool call calls up: one whose SKILL.md it
// reads, by Read or SkillsManager's read, at skill://<name>/SKILL.md (or
// <name>/SKILL.md to SkillsManager; K-brain's internal/agent/skills.go),
// or through magpie's skill router.
func liveagentSkill(name, args string) string {
	var a struct {
		Action string `json:"action"`
		Path   string `json:"path"`
	}
	if json.Unmarshal([]byte(args), &a) != nil || a.Path == "" {
		return ""
	}
	switch {
	case name == "SkillsManager" && a.Action == "read":
	case name == "Read" && strings.HasPrefix(a.Path, "skill:"):
	case name == "Read":
		return routedSkill(a.Path)
	default:
		return ""
	}
	p := strings.TrimPrefix(strings.TrimPrefix(a.Path, "skill://"), "skill:")
	rest, ok := strings.CutSuffix(p, "/SKILL.md")
	if !ok {
		return ""
	}
	return filepath.Base(filepath.FromSlash(rest))
}

var liveagentSaid = []byte(`{"type":"message"`)

// liveagentTranscript tells add what a LiveAgent session said, by its
// message lines; a fork's copies of its source's are read too, as the
// conversation it goes on from.
func liveagentTranscript(path string, add func(bool, Part) bool) error {
	names := map[string]string{} // a tool call's name, by its id
	_, err := scanAt(path, 0, nil, func(b []byte, _, _ int64) bool {
		if !bytes.HasPrefix(b, liveagentSaid) {
			return true
		}
		var l liveagentLine
		if json.Unmarshal(b, &l) != nil || l.Type != "message" {
			return true
		}
		for _, p := range liveagentParts(l.Payload.Message, names) {
			if !add(p.Role != "assistant", p) {
				return false
			}
		}
		return true
	})
	return err
}

// liveagentParts are what one message said.
func liveagentParts(m liveagentMessage, names map[string]string) []Part {
	text := liveagentText(m.Content)
	var parts []Part
	switch m.Role {
	case "user":
		if strings.TrimSpace(text) == "" {
			return nil
		}
		if m.Authored && m.Origin != "background" {
			return []Part{textPart("user", text)}
		}
		// what LiveAgent put in itself: a notice, a task's report
		return []Part{{Role: "user", Kind: "context", Text: text}}
	case "assistant":
		model := liveagentModel(m.Model)
		if strings.TrimSpace(m.Reasoning) != "" {
			parts = append(parts, Part{Role: "assistant", Kind: "thinking", Text: m.Reasoning, Model: model})
		}
		if strings.TrimSpace(text) != "" {
			p := textPart("assistant", text)
			p.Model = model
			parts = append(parts, p)
		}
		for _, c := range m.ToolCalls {
			names[c.ID] = c.Function.Name
			parts = append(parts, Part{Role: "assistant", Kind: "tool_use", Name: c.Function.Name, Text: pretty(json.RawMessage(c.Function.Arguments)), Model: model})
		}
	case "tool":
		parts = append(parts, Part{Role: "tool", Kind: "tool_result", Name: cmp.Or(names[m.ToolCallID], m.Name), Text: text})
	}
	return parts
}
