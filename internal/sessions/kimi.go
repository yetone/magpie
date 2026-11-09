package sessions

import (
	"bytes"
	"cmp"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Kimi Code's sessions (#1913, infinitr0us; the reader follows ggbdpq's
// notes on the issue). A session is the folder sessions/<workdir slug>/
// <session id>/ of Kimi's home (KimiCodeDir), the same one its calls are
// read from (calls_kimi.go): each of its agents writes
// agents/<agent>/wire.jsonl, and state.json beside agents/ holds what the
// session is called. As Kimi Code's own normalizeSessionMeta
// (packages/agent-core-v2/src/session/sessionMetadata) reads it:
//
//   - title, a name the user gave when isCustomTitle is true (or titleKind
//     is "custom", or an old customTitle is set), else Kimi's own;
//   - cwd, or workDir before protocol 1.5; a state.json with neither (an
//     early 1.4 session, one migrated from the Python kimi-cli) has its
//     folder in session_index.jsonl at the root of Kimi's home (#2103);
//   - createdAt and updatedAt, epoch milliseconds from 1.5 on, RFC 3339
//     strings before;
//   - forkedFrom, the session a fork was made from.
//
// The session's id is its folder's name: 1.4's state.json has no id. A
// session archived in Kimi is listed all the same, as Codex's archived
// rollouts are.
//
// The wire records counted: usage.record (one a model response, the same
// tokens Usage counts, a subagent's in its own file), turn.prompt whose
// origin is the user (a prompt typed; a subagent's are its parent's
// system_trigger), a loop step.end that ends the turn (a reply) and a loop
// tool.call. Every record from 1.4 on carries its time in ms; 1.0's (a
// session migrated from the Python kimi-cli) carry none, and its state.json
// dates it.
//
// A fork copies its source's wire records into its own files unchanged,
// times included, then appends {"type":"forked","agentId":…,"time":…}
// (copyAgentWire / appendForkedMarker in Kimi's sessionLifecycleService):
// only the records after the last such marker are the fork's own. A fork
// written without one (forkedFrom set, no marker) counts from its createdAt,
// which Kimi sets after the copy.
//
// Unlike the call reader, a call made through magpie's own entry (a model
// keyed magpie/<provider>/<model>) counts here, as the session's own spend,
// under <provider>/<model>.
//
// `kimi --session <id>` picks one up again (apps/kimi-code/src/cli).

// kimiFilesIn are the wire files of every session under a Kimi Code home's
// sessions folder, one file an agent; the session's own is its main. A
// session migrated from the Python kimi-cli (a ses_* folder) is read too.
// state.json is part of the main file, so a title changed there reads the
// session again.
func kimiFilesIn(sessions string) []file {
	if !Reads("kimi") {
		return nil
	}
	paths, _ := SessionGlob(filepath.Join(sessions, "*", "*", "agents", "*", "wire.jsonl"))
	var out []file
	for _, p := range paths {
		agentDir := filepath.Dir(p)
		dir := filepath.Dir(filepath.Dir(agentDir))
		f := file{agent: "kimi", key: "kimi:" + filepath.Base(dir), path: p, main: filepath.Base(agentDir) == "main"}
		if !stat(&f) {
			continue
		}
		if f.main {
			m := file{path: filepath.Join(dir, "state.json")}
			if stat(&m) {
				f.size += m.size
				if m.mod.After(f.mod) {
					f.mod = m.mod
				}
			}
		}
		out = append(out, f)
	}
	return out
}

// kimiFiles are Kimi Code's sessions on this computer.
func kimiFiles() []file { return kimiFilesIn(kimiSessions()) }

// kimiState is what state.json says of a session.
type kimiState struct {
	Title         string          `json:"title"`
	TitleKind     string          `json:"titleKind"`
	IsCustomTitle *bool           `json:"isCustomTitle"`
	CustomTitle   string          `json:"customTitle"`
	Cwd           string          `json:"cwd"`
	WorkDir       string          `json:"workDir"`
	CreatedAt     json.RawMessage `json:"createdAt"`
	ForkedFrom    string          `json:"forkedFrom"`
}

// kimiStateOf reads the state.json of the session a main wire file is in.
func kimiStateOf(wire string) (kimiState, bool) {
	var st kimiState
	b, err := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(wire))), "state.json"))
	return st, err == nil && json.Unmarshal(b, &st) == nil
}

// kimiIndex is Kimi Code's session_index.jsonl, read again only when it
// changes. Kimi documents it (docs/en/configuration/data-locations.md): a
// line a session, {"sessionId","sessionDir","workDir"}, at the root of its
// home beside sessions/.
var kimiIndex struct {
	sync.Mutex
	path     string
	size     int64
	modified time.Time
	workDirs map[string]string
}

// kimiIndexedWorkDir is the folder session_index.jsonl gives the session a
// main wire file is in, for a state.json that names none (#2103,
// infinitr0us). "" when the index has no line for it.
func kimiIndexedWorkDir(wire string) string {
	dir := filepath.Dir(filepath.Dir(filepath.Dir(wire)))
	path := filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(dir))), "session_index.jsonl")
	fi, err := os.Stat(path)
	if err != nil {
		return ""
	}
	kimiIndex.Lock()
	defer kimiIndex.Unlock()
	if kimiIndex.path != path || kimiIndex.size != fi.Size() || !kimiIndex.modified.Equal(fi.ModTime()) {
		workDirs := map[string]string{}
		scan(path, 0, func(b []byte) {
			var l struct {
				SessionID string `json:"sessionId"`
				WorkDir   string `json:"workDir"`
			}
			if json.Unmarshal(b, &l) == nil && l.SessionID != "" && l.WorkDir != "" {
				workDirs[l.SessionID] = l.WorkDir
			}
		})
		kimiIndex.path, kimiIndex.size, kimiIndex.modified, kimiIndex.workDirs = path, fi.Size(), fi.ModTime(), workDirs
	}
	return kimiIndex.workDirs[filepath.Base(dir)]
}

// names are the user's name for the session and Kimi's own, as Kimi's
// normalizeSessionTitle tells them apart.
func (st kimiState) names() (custom, named string) {
	switch {
	case st.Title != "" && st.IsCustomTitle != nil && *st.IsCustomTitle:
		return st.Title, ""
	case st.Title != "" && st.TitleKind == "custom" && st.IsCustomTitle == nil:
		return st.Title, ""
	case st.Title != "":
		return "", st.Title
	}
	return st.CustomTitle, ""
}

// kimiTime is a state.json date: epoch ms, or an RFC 3339 string.
func kimiTime(raw json.RawMessage) time.Time {
	var n int64
	if json.Unmarshal(raw, &n) == nil {
		return ms(n)
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		t, _ := time.Parse(time.RFC3339Nano, s)
		return t
	}
	return time.Time{}
}

type kimiWire struct {
	Type  string     `json:"type"`
	Time  int64      `json:"time"`
	Model string     `json:"model"`
	Usage *kimiUsage `json:"usage"`
	Input []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"input"`
	Origin struct {
		Kind string `json:"kind"`
	} `json:"origin"`
	Event struct {
		Type         string `json:"type"`
		Name         string `json:"name"`
		FinishReason string `json:"finishReason"`
	} `json:"event"`
}

var (
	kimiForked = []byte(`"type":"forked"`)
	kimiCounts = [][]byte{kimiUsageRecord, []byte(`"turn.prompt"`), []byte(`"step.end"`), []byte(`"tool.call"`)}
)

// kimiCarry is what a read of a Kimi Code wire file carries on with:
// where it read to, what of state.json its records were counted under,
// and the time before which a fork's records are its source's.
type kimiCarry struct {
	Tail    *tail     `json:"tail,omitempty"`
	Created time.Time `json:"created,omitzero"`
	Forked  bool      `json:"forked,omitempty"`
	Since   time.Time `json:"since,omitzero"`
}

// parseKimi reads one agent's wire file of a session, with the session's
// state.json for its own. Kimi only appends to a wire file, so one read
// before is read on from where that read left off; state.json, rewritten
// as the session goes on, is read again each time. A file cut short or
// replaced, or a state.json that now says another creation or fork, is
// read whole again.
func parseKimi(f file, old *state) *state {
	var c kimiCarry
	var st kimiState
	hasSt := false
	if st, hasSt = kimiStateOf(f.path); hasSt {
		c.Created = kimiTime(st.CreatedAt)
		c.Forked = st.ForkedFrom != ""
		if c.Forked {
			c.Since = c.Created
		}
	}
	fresh := func() *state {
		s := &state{}
		if hasSt && f.main {
			s.saw(c.Created, true)
		}
		return s
	}
	var s *state
	var off int64
	if o := old; o != nil && o.more().Kimi != nil && o.more().Kimi.Created.Equal(c.Created) && o.more().Kimi.Forked == c.Forked && o.more().Kimi.Tail.holds(f.path) {
		s, c, off = o.clone(), *o.more().Kimi, o.more().Kimi.Tail.Off
	} else {
		s = fresh()
	}
	end, err := scanAt(f.path, off, nil, func(b []byte, _, _ int64) bool {
		if c.Forked && bytes.Contains(b, kimiForked) {
			var r kimiWire
			if json.Unmarshal(b, &r) == nil && r.Type == "forked" {
				// a fork's copy of its source ends at its last marker:
				// what was counted before it is its source's
				s, c.Since = fresh(), time.Time{}
				return true
			}
		}
		kimiCount(s, b, c.Since, f.main)
		return true
	})
	c.Tail = nil
	if err == nil {
		c.Tail = markTail(f.path, end, 0, end)
	}
	s.Size, s.Mod = f.size, f.mod.UnixNano()
	if hasSt && f.main {
		s.Cwd = cmp.Or(st.Cwd, st.WorkDir, kimiIndexedWorkDir(f.path))
		s.own().Custom, s.own().Named = st.names()
		s.own().Custom, s.own().Named = title(s.more().Custom), title(s.more().Named)
	}
	s.own().Kimi = &c
	return s
}

// kimiCount counts one record of a wire file, none before since.
func kimiCount(s *state, b []byte, since time.Time, main bool) {
	count := false
	for _, k := range kimiCounts {
		if bytes.Contains(b, k) {
			count = true
			break
		}
	}
	var r kimiWire
	if !count {
		// only its time: a light read of the rest
		var t struct {
			Time int64 `json:"time"`
		}
		if json.Unmarshal(b, &t) == nil {
			if at := ms(t.Time); !at.IsZero() && !at.Before(since) {
				s.saw(at, main)
			}
		}
		return
	}
	if json.Unmarshal(b, &r) != nil {
		return
	}
	at := ms(r.Time)
	if at.IsZero() || at.Before(since) {
		return
	}
	s.saw(at, main)
	switch r.Type {
	case "usage.record":
		if u := r.Usage; u != nil && u.InputOther >= 0 && u.Output >= 0 && u.InputCacheRead >= 0 && u.InputCacheCreation >= 0 {
			model := cmp.Or(strings.TrimPrefix(r.Model, kimiGateway), "kimi")
			s.use(dateOf(at), model, Tokens{Input: u.InputOther, Output: u.Output, CacheRead: u.InputCacheRead, CacheWrite: u.InputCacheCreation})
		}
	case "turn.prompt":
		if !main || r.Origin.Kind != "user" {
			return
		}
		s.day(dateOf(at)).Prompts++
		if s.Title == "" {
			var text []string
			for _, p := range r.Input {
				if p.Type == "text" {
					text = append(text, p.Text)
				}
			}
			s.Title = title(strings.Join(text, " "))
		}
	case "context.append_loop_event":
		switch r.Event.Type {
		case "step.end":
			if main && r.Event.FinishReason == "end_turn" {
				s.day(dateOf(at)).Replies++
			}
		case "tool.call":
			s.tool(at, r.Event.Name, "")
		}
	}
}
