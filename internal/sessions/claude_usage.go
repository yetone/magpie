package sessions

import (
	"container/list"
	"encoding/json"
	"time"
)

// claudeUsageState tracks message revisions while a source is live. It is
// kept on disk with the read it belongs to (MarshalJSON, writeCallRevisions);
// a changed source without one reconstructs it from its first line.
type claudeUsageState struct {
	Messages map[string]map[string]*claudeContribution `json:"messages,omitempty"`
	recent   list.List
	entries  int
	// what finish evicted, for message to use again: a history's read
	// made a contribution, its branches and its maps for every message,
	// and threw all but the last few thousand away (~1.5 GB on a first
	// start's read of a 21 GB Claude Code history, #2278)
	free         []*claudeContribution
	freeBranches []map[string]*claudeContribution
}

// claudeFree is how many evicted contributions are kept to use again:
// one is evicted for each one made once the window is full.
const claudeFree = 16

// claudeFreeMap is the most a map of an evicted contribution may hold to
// be used again: clear keeps a map's room, and one long message's would
// stay with a short one's.
const claudeFreeMap = 64

// Each active file keeps a recent window even when the whole source exceeds
// the shared index budget. Older messages fall back to the original reader
// behavior; their complete replay history is not retained.
const claudeRevisionEntries = revisionEntries / revisionFiles

type claudeContribution struct {
	key      string
	node     *list.Element
	weight   int
	Request  string                     `json:"request,omitempty"`
	Usage    *claudeUsageVersion        `json:"usage,omitempty"`
	Updated  time.Time                  `json:"updated"`
	Previous []claudeUsageVersion       `json:"previous,omitempty"`
	ReplyDay string                     `json:"reply_day,omitempty"`
	Replied  bool                       `json:"replied,omitempty"`
	Tools    map[string]claudeToolUsage `json:"tools,omitempty"`
	Call     int                        `json:"call"`
	Began    time.Time                  `json:"began"`
	Blocks   map[string]bool            `json:"blocks,omitempty"`
}

type claudeUsageVersion struct {
	At    time.Time `json:"at"`
	Model string    `json:"model"`
	Tokens
}

type claudeToolUsage struct {
	At          time.Time `json:"at"`
	Updated     time.Time `json:"updated"`
	Name, Skill string
	Previous    []claudeToolVersion `json:"previous,omitempty"`
}

type claudeToolVersion struct{ Name, Skill string }

// claudeWindowEntry is one contribution of a window, oldest first, as its
// eviction order has them.
type claudeWindowEntry struct {
	Key string              `json:"key"`
	M   *claudeContribution `json:"m"`
}

// window is r's contributions in their eviction order, to keep on disk.
func (r *claudeUsageState) window() []claudeWindowEntry {
	var out []claudeWindowEntry
	for e := r.recent.Front(); e != nil; e = e.Next() {
		m := e.Value.(*claudeContribution)
		out = append(out, claudeWindowEntry{m.key, m})
	}
	return out
}

// claudeWindowOf is the window kept as entries.
func claudeWindowOf(entries []claudeWindowEntry) *claudeUsageState {
	w := &claudeUsageState{}
	w.fill(entries)
	return w
}

func (r *claudeUsageState) fill(entries []claudeWindowEntry) {
	r.Messages = map[string]map[string]*claudeContribution{}
	for _, e := range entries {
		m := e.M
		if m == nil {
			continue
		}
		m.key = e.Key
		m.weight = m.entryWeight()
		m.node = r.recent.PushBack(m)
		r.entries += m.weight
		branches := r.Messages[e.Key]
		if branches == nil {
			branches = map[string]*claudeContribution{}
			r.Messages[e.Key] = branches
		}
		branches[m.Request] = m
	}
}

// A summary's window is kept in sessions.json with it, so a session that grew
// while magpie was not running is read on, not again from its first line: a
// live 2 GB one took seconds of the first Sessions list after every update
// (#3493). Only a few bounded windows are kept at once (trimSummaryRevisions).
func (r *claudeUsageState) MarshalJSON() ([]byte, error) { return json.Marshal(r.window()) }

func (r *claudeUsageState) UnmarshalJSON(b []byte) error {
	var entries []claudeWindowEntry
	if err := json.Unmarshal(b, &entries); err != nil {
		return err
	}
	r.fill(entries)
	return nil
}

func ccUsageState(p **claudeUsageState) *claudeUsageState {
	if *p == nil {
		*p = &claudeUsageState{}
	}
	return *p
}

// message only joins identities with compatible request IDs. Missing request
// IDs can adopt the sole known branch; a conflict keeps a separate branch.
// With neither ID there is no evidence that two lines are the same call.
func (r *claudeUsageState) message(id, request string) *claudeContribution {
	if id == "" && request == "" {
		return &claudeContribution{Call: -1}
	}
	// the key is looked up from a buffer and made a string only to be
	// kept: every line of a message looks it up, one keeps it
	var buf [96]byte
	kb := append(buf[:0], "message:"...)
	kb = append(kb, id...)
	if id == "" {
		kb = append(append(buf[:0], "request:"...), request...)
	}
	if r.Messages == nil {
		r.Messages = make(map[string]map[string]*claudeContribution)
	}
	key := ""
	branches := r.Messages[string(kb)]
	if branches == nil {
		key = string(kb)
		if n := len(r.freeBranches); n > 0 {
			branches = r.freeBranches[n-1]
			r.freeBranches[n-1] = nil
			r.freeBranches = r.freeBranches[:n-1]
		} else {
			branches = make(map[string]*claudeContribution)
		}
		r.Messages[key] = branches
	}
	if m := branches[request]; m != nil {
		return m
	}
	if len(branches) == 1 {
		if request == "" {
			for _, m := range branches {
				return m
			}
		} else if m := branches[""]; m != nil {
			delete(branches, "")
			m.Request = request
			branches[request] = m
			return m
		}
	}
	if key == "" {
		for _, m := range branches {
			key = m.key // the same string, made once
			break
		}
		if key == "" {
			key = string(kb)
		}
	}
	var m *claudeContribution
	if n := len(r.free); n > 0 {
		m = r.free[n-1]
		r.free[n-1] = nil
		r.free = r.free[:n-1]
		m.Request, m.Call, m.key = request, -1, key
	} else {
		m = &claudeContribution{Request: request, Call: -1, key: key}
	}
	m.node = r.recent.PushBack(m)
	branches[request] = m
	return m
}

// finish accounts for changes made by this line and evicts the least recently
// used contributions. It never throws away the file's append offset or the
// current message's usage. Weight includes nested revision, block and tool IDs.
func (r *claudeUsageState) finish(m *claudeContribution) {
	if m.node == nil {
		return
	}
	n := m.entryWeight()
	if n > claudeRevisionEntries {
		// One exceptionally long message can fill the window by itself. Keep its
		// latest contribution (the original last-message rule), not its history.
		m.Previous, m.Tools, m.Blocks = nil, nil, nil
		n = m.entryWeight()
	}
	r.entries += n - m.weight
	m.weight = n
	r.recent.MoveToBack(m.node)
	for r.entries > claudeRevisionEntries {
		e := r.recent.Front()
		old := e.Value.(*claudeContribution)
		branches := r.Messages[old.key]
		delete(branches, old.Request)
		if len(branches) == 0 {
			delete(r.Messages, old.key)
			if len(r.freeBranches) < claudeFree {
				r.freeBranches = append(r.freeBranches, branches)
			}
		}
		r.entries -= old.weight
		r.recent.Remove(e)
		old.node = nil
		if len(r.free) < claudeFree {
			r.free = append(r.free, old.reset())
		}
	}
}

// reset is m emptied to be used again, keeping its maps' and list's room
// when they are small. Nothing else holds an evicted contribution: clone
// copies each, and window reads only those still in the window.
func (m *claudeContribution) reset() *claudeContribution {
	tools, blocks, previous := m.Tools, m.Blocks, m.Previous[:0]
	if len(tools) > claudeFreeMap {
		tools = nil
	}
	if len(blocks) > claudeFreeMap {
		blocks = nil
	}
	if cap(previous) > claudeFreeMap {
		previous = nil
	}
	clear(tools)
	clear(blocks)
	*m = claudeContribution{Tools: tools, Blocks: blocks, Previous: previous}
	return m
}

func (m *claudeContribution) entryWeight() int {
	n := 2 + len(m.Previous) + len(m.Tools) + len(m.Blocks)
	for _, tool := range m.Tools {
		n += len(tool.Previous)
	}
	return n
}

// accept replaces a contribution only for an unseen, later revision. Exact
// replays keep their original date, and replayed old snapshots cannot undo a
// later block even when the replay was written with a new timestamp.
func (m *claudeContribution) accept(at time.Time, model string, tokens Tokens) (*claudeUsageVersion, bool) {
	old := m.Usage
	if old != nil {
		if old.Model == model && old.Tokens == tokens {
			// Later blocks often repeat the same usage. Their timestamps
			// still bound the revision order, even though the contribution
			// keeps its original date. A modified replay of one of these
			// blocks must not become a new revision merely by appearing later.
			if at.After(m.Updated) {
				m.Updated = at
			}
			return old, false
		}
		for _, v := range m.Previous {
			if v.Model == model && v.Tokens == tokens {
				return old, false
			}
		}
		if at.IsZero() || !m.Updated.IsZero() && !at.After(m.Updated) {
			return old, false
		}
		m.Previous = append(m.Previous, *old)
	}
	m.Usage = &claudeUsageVersion{At: at, Model: model, Tokens: tokens}
	m.Updated = at
	return old, true
}

func (r *claudeUsageState) clone() *claudeUsageState {
	if r == nil {
		return nil
	}
	c := &claudeUsageState{Messages: make(map[string]map[string]*claudeContribution, len(r.Messages))}
	for key, branches := range r.Messages {
		copied := make(map[string]*claudeContribution, len(branches))
		for request, m := range branches {
			n := *m
			n.node = nil
			if m.Usage != nil {
				v := *m.Usage
				n.Usage = &v
			}
			n.Previous = append([]claudeUsageVersion(nil), m.Previous...)
			if m.Blocks != nil {
				n.Blocks = make(map[string]bool, len(m.Blocks))
				for id, seen := range m.Blocks {
					n.Blocks[id] = seen
				}
			}
			if m.Tools != nil {
				n.Tools = make(map[string]claudeToolUsage, len(m.Tools))
				for id, tool := range m.Tools {
					tool.Previous = append([]claudeToolVersion(nil), tool.Previous...)
					n.Tools[id] = tool
				}
			}
			copied[request] = &n
		}
		c.Messages[key] = copied
	}
	for e := r.recent.Front(); e != nil; e = e.Next() {
		m := e.Value.(*claudeContribution)
		n := c.Messages[m.key][m.Request]
		n.node = c.recent.PushBack(n)
		c.entries += n.weight
	}
	return c
}

func ccCount(s *state, m *claudeContribution, at time.Time, model string, tokens Tokens, main bool) {
	old, changed := m.accept(at, model, tokens)
	if changed {
		if old != nil {
			s.unuse(dateOf(old.At), old.Model, old.Tokens)
		}
		s.use(dateOf(at), model, tokens)
	}
	if main && !m.Replied {
		m.Replied, m.ReplyDay = true, dateOf(at)
		s.day(m.ReplyDay).Replies++
	}
}

// A later block can contain new tools even though its message usage repeats.
func ccTool(s *state, m *claudeContribution, at time.Time, id, name, skill string) {
	if skill == routerSkill {
		skill = ""
	}
	if id == "" {
		s.tool(at, name, skill)
		return
	}
	if m.Tools == nil {
		m.Tools = make(map[string]claudeToolUsage)
	}
	var previous []claudeToolVersion
	if old, ok := m.Tools[id]; ok {
		if old.Name == name && old.Skill == skill {
			if at.After(old.Updated) {
				old.Updated = at
				m.Tools[id] = old
			}
			return
		}
		if at.IsZero() || !old.Updated.IsZero() && !at.After(old.Updated) {
			return
		}
		for _, v := range old.Previous {
			if v.Name == name && v.Skill == skill {
				return
			}
		}
		previous = append(append([]claudeToolVersion(nil), old.Previous...), claudeToolVersion{old.Name, old.Skill})
		d := s.day(dateOf(old.At))
		if old.Name != "" {
			*d.Tools.at(old.Name)--
		}
		if old.Skill != "" {
			*d.Skills.at(old.Skill)--
		}
	}
	m.Tools[id] = claudeToolUsage{At: at, Updated: at, Name: name, Skill: skill, Previous: previous}
	s.tool(at, name, skill)
}
