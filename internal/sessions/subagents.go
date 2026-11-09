package sessions

import (
	"bytes"
	"cmp"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Subagent is a conversation a session's agent started inside it, which its
// agent keeps beside the session's own and counts in it (#2261): a Claude
// Code (or Qoder) subagent's agent-<id>.jsonl, an OpenCode child session, a
// Kimi Code agents/agent-<n>/wire.jsonl. Codex's subagents are sessions of
// their own, which name theirs in ParentSession instead.
type Subagent struct {
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
	// Kind is the agent it ran as: Claude Code's agentType ("Explore"),
	// OpenCode's agent ("general"), Kimi Code's subagentName ("coder")
	Kind string `json:"kind,omitempty"`
	// Parent is the subagent that started it, "" when the session did
	Parent string    `json:"parent,omitempty"`
	Start  time.Time `json:"start,omitzero"`
	Last   time.Time `json:"last,omitzero"`
	// Path is the file its conversation is in (OpenCode's database)
	Path string `json:"-"`
}

// subagentsOf are the subagents among a session's files, oldest first.
func subagentsOf(s Session, fs []file) []Subagent {
	var out []Subagent
	var spawned map[string]kimiSpawn
	for _, f := range fs {
		if f.main {
			continue
		}
		st := cache[f.path]
		if st == nil {
			continue
		}
		sa := Subagent{Start: st.Start, Last: st.Last, Path: f.path}
		switch s.Agent {
		case "claude", "qoder", "qoder-cn":
			sa.ID = SubagentOf(f.path)
			// a compaction's own run (agent-acompact-<id>.jsonl) is no
			// subagent anyone started
			if sa.ID == "" || strings.HasPrefix(sa.ID, "acompact-") {
				continue
			}
			m := ccMeta(f.path)
			sa.Kind, sa.Parent = m.AgentType, m.ParentAgentID
			// the file only when the meta names no task: cmp.Or read
			// every Claude Code subagent's file for a title it threw away
			if sa.Title = m.Description; sa.Title == "" {
				sa.Title = ccTask(f.path)
			}
		case "opencode":
			sa.ID = f.m().sid
			if f.m().parent != s.ID {
				sa.Parent = f.m().parent
			}
			sa.Title, sa.Kind = ocSubagentTitle(st.more().Named)
			sa.Title = cmp.Or(sa.Title, st.Title, st.more().First)
			// OpenCode 2 names the agent in the session's row, and its
			// titles no longer do (#2290)
			if v2, ok := f.m().oc.(ocV2DB); ok {
				sa.Kind = cmp.Or(v2.agent(f.path, f.m().sid), sa.Kind)
			}
		case "kimi":
			sa.ID = filepath.Base(filepath.Dir(f.path))
			if spawned == nil {
				spawned = map[string]kimiSpawn{}
				for _, g := range fs {
					w := kimiWireOf(g.path)
					for id, sp := range w.Spawned {
						spawned[id] = sp
					}
				}
			}
			sp := spawned[sa.ID]
			if sp.ParentAgentID != "main" {
				sa.Parent = sp.ParentAgentID
			}
			sa.Kind = sp.SubagentName
			if sa.Title = title(sp.Description); sa.Title == "" {
				sa.Title = kimiWireOf(f.path).Prompt
			}
		default:
			continue
		}
		out = append(out, sa)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].Start.Equal(out[j].Start) {
			return out[i].Start.Before(out[j].Start)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// ccSubMeta is what Claude Code's agent-<id>.meta.json beside a subagent's
// file says of it.
type ccSubMeta struct {
	AgentType     string `json:"agentType"`
	Description   string `json:"description"`
	ParentAgentID string `json:"parentAgentId"`
}

// ccMeta reads the meta file of the Claude Code subagent whose file p is.
// One read is kept: Claude Code writes it as it starts the subagent. One
// not there yet is read again.
func ccMeta(p string) ccSubMeta {
	meta := strings.TrimSuffix(p, ".jsonl") + ".meta.json"
	if v, ok := ccMetas.Load(meta); ok {
		return v.(ccSubMeta)
	}
	b, err := os.ReadFile(meta)
	if err != nil {
		return ccSubMeta{}
	}
	var m ccSubMeta
	if json.Unmarshal(b, &m) != nil {
		return ccSubMeta{}
	}
	m.AgentType = m.AgentType[:min(len(m.AgentType), 128)]
	m.ParentAgentID = m.ParentAgentID[:min(len(m.ParentAgentID), 128)]
	m.Description = title(m.Description)
	ccMetas.Store(meta, m)
	return m
}

var ccMetas sync.Map

// ccTask is what a Claude Code subagent was asked: its file's first user
// line, which Claude Code writes as it starts it, among its first 16. What
// was read, a task or none, is kept until the file's size or time changes,
// and only its first lines are read again then: every Sessions list asks
// for each subagent's task, and a file naming none was read on every ask.
func ccTask(p string) string {
	fi, err := os.Stat(p)
	if err != nil {
		return ""
	}
	if v, ok := ccTasks.Load(p); ok {
		if t := v.(ccTaskRead); fi.Size() == t.size && fi.ModTime().Equal(t.mod) {
			return t.task
		}
	}
	task, n := "", 0
	_, err = scanAt(p, 0, nil, func(b []byte, _, _ int64) bool {
		if n++; n > 16 {
			return false
		}
		if !bytes.Contains(b, ccUserLine) {
			return true
		}
		var l struct {
			Type    string `json:"type"`
			IsMeta  bool   `json:"isMeta"`
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(b, &l) == nil && l.Type == "user" && !l.IsMeta {
			task = untagged(ccText(l.Message.Content))
		}
		return task == ""
	})
	if err != nil {
		// not readable now: what was read before, if anything
		if v, ok := ccTasks.Load(p); ok {
			return v.(ccTaskRead).task
		}
		return ""
	}
	// the size and time from before the read: a line written while it was
	// read changes them, and the file is read again on the next ask
	ccTasks.Store(p, ccTaskRead{task: task, size: fi.Size(), mod: fi.ModTime()})
	return task
}

// ccTaskRead is what ccTask read of a file, as of its size and time.
type ccTaskRead struct {
	task string
	size int64
	mod  time.Time
}

var ccTasks sync.Map

// ocSubagentTitle splits the title OpenCode gives a session its task tool
// starts, "<description> (@<agent> subagent)", into the two. A title it
// made up itself ("Child session - <time>") is none.
func ocSubagentTitle(named string) (string, string) {
	if named == "" || ocDefaultTitle(named) {
		return "", ""
	}
	if rest, ok := strings.CutSuffix(named, " subagent)"); ok {
		if i := strings.LastIndex(rest, " (@"); i > 0 {
			return strings.TrimSpace(rest[:i]), rest[i+3:]
		}
	}
	return named, ""
}

// agent is the agent OpenCode 2 ran a session as, session_v2.agent
// ("general", "explore"), "" when it names none or the column isn't there.
// One found is kept: a session's agent is set as it starts.
func (s ocV2DB) agent(path, sid string) string {
	if v, ok := ocAgents.Load(path); ok {
		return v.(string)
	}
	var a sql.NullString
	if s.db.QueryRow(`SELECT agent FROM session_v2 WHERE id = ?`, sid).Scan(&a) != nil || a.String == "" {
		return ""
	}
	k := a.String[:min(len(a.String), 128)]
	ocAgents.Store(path, k)
	return k
}

var ocAgents sync.Map

// kimiSpawn is Kimi Code's subagent.spawned record, which the agent that
// started a subagent writes in its own wire.jsonl.
type kimiSpawn struct {
	SubagentID    string `json:"subagentId"`
	SubagentName  string `json:"subagentName"`
	ParentAgentID string `json:"parentAgentId"`
	Description   string `json:"description"`
}

// kimiWireSubs is what a Kimi wire file says of subagents: those its agent
// started, and, of a subagent's own, the task it was given.
type kimiWireSubs struct {
	size    int64
	mod     time.Time
	Spawned map[string]kimiSpawn
	Prompt  string
}

var (
	kimiWires     sync.Map
	kimiSpawned   = []byte(`"subagent.spawned"`)
	kimiPromptRec = []byte(`"turn.prompt"`)
)

// kimiWireOf reads a wire file for its subagents, again only once it
// changes.
func kimiWireOf(p string) kimiWireSubs {
	fi, err := os.Stat(p)
	if err != nil {
		return kimiWireSubs{}
	}
	if v, ok := kimiWires.Load(p); ok {
		if w := v.(kimiWireSubs); w.size == fi.Size() && w.mod.Equal(fi.ModTime()) {
			return w
		}
	}
	w := kimiWireSubs{size: fi.Size(), mod: fi.ModTime()}
	main := filepath.Base(filepath.Dir(p)) == "main"
	scan(p, 0, func(b []byte) {
		switch {
		case bytes.Contains(b, kimiSpawned):
			var r struct {
				Type string `json:"type"`
				kimiSpawn
			}
			if json.Unmarshal(b, &r) == nil && r.Type == "subagent.spawned" && r.SubagentID != "" {
				if w.Spawned == nil {
					w.Spawned = map[string]kimiSpawn{}
				}
				r.SubagentName = r.SubagentName[:min(len(r.SubagentName), 128)]
				w.Spawned[r.SubagentID] = r.kimiSpawn
			}
		case !main && w.Prompt == "" && bytes.Contains(b, kimiPromptRec):
			var r kimiWire
			if json.Unmarshal(b, &r) == nil && r.Type == "turn.prompt" {
				var text []string
				for _, p := range r.Input {
					if p.Type == "text" {
						text = append(text, p.Text)
					}
				}
				w.Prompt = title(strings.Join(text, " "))
			}
		}
	})
	kimiWires.Store(p, w)
	return w
}

// ErrNoSubagent is a subagent the session doesn't have.
var ErrNoSubagent = errors.New("no such subagent in this session")

// SubagentTranscript is the conversation of the session's subagent sub.
func SubagentTranscript(s Session, sub string) (Transcript, error) {
	if !Reads(s.Agent) {
		return Transcript{Parts: []Part{}}, ErrNotRead
	}
	for _, sa := range s.Subagents {
		if sa.ID != sub {
			continue
		}
		c := Session{Agent: s.Agent, ID: sa.ID, Path: sa.Path}
		return TranscriptOf(c)
	}
	return Transcript{Parts: []Part{}}, ErrNoSubagent
}
