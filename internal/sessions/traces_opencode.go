package sessions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// traceOpenCodeWindow keeps the latest main sessions, newest first.
// A child is not exported (the poll skips it), so it takes no place:
// Limit newer children would fill the window and the parent would not
// be read. Claude's children are exported, which is why traceRecentFiles
// gives them a quota of their own.
func traceOpenCodeWindow(files []file) []file {
	sortTraceOCFiles(files)
	out := make([]file, 0, min(len(files), Limit))
	for _, f := range files {
		if !f.main {
			continue
		}
		out = append(out, f)
		if len(out) == Limit {
			break
		}
	}
	return out
}

func traceOCMessages(f file, budget *int64) [][]byte {
	var out [][]byte
	for _, b := range f.m().oc.messages(f.m().sid) {
		if *budget <= 0 {
			break
		}
		*budget -= int64(len(b))
		out = append(out, b)
	}
	return out
}
func traceOCParts(f file, mid string, budget *int64) [][]byte {
	var out [][]byte
	for _, b := range f.m().oc.parts(mid) {
		if *budget <= 0 {
			break
		}
		*budget -= int64(len(b))
		out = append(out, b)
	}
	return out
}

func (r *TraceReader) pollOpenCode(bodies bool, budget *int64) []TraceSpan {
	// Unchanged SQLite/WAL files need no queries. Opening a new connection for
	// data_version would reset its baseline, so use both files' stat identities.
	dbPath := openCodeDB()
	if fileExists(dbPath) {
		if info, err := os.Stat(dbPath); err == nil {
			if r.ocInfo != nil && !os.SameFile(r.ocInfo, info) {
				for key := range r.snapshots {
					if strings.HasPrefix(key, "opencode:") {
						delete(r.snapshots, key)
					}
				}
				r.ocStamp = [4]int64{}
			}
			r.ocInfo = info
		}
		var stamp [4]int64
		for i, path := range []string{dbPath, dbPath + "-wal"} {
			if info, err := os.Stat(path); err == nil {
				stamp[i*2], stamp[i*2+1] = info.Size(), info.ModTime().UnixNano()
			}
		}
		if r.ocStamp == stamp && !r.ocPending {
			return nil
		}
		out, pending := r.pollOpenCodeDB(dbPath, bodies, budget)
		r.ocStamp, r.ocPending = stamp, pending
		return out
	}
	files := openCodeJSONFiles(filepath.Join(OpenCodeDir(), "storage"))
	for i, f := range files {
		if store, ok := f.m().oc.(ocFiles); ok {
			for _, message := range readDirectory(filepath.Join(store.root, "message", f.m().sid)) {
				mid := strings.TrimSuffix(message.Name(), ".json")
				for _, part := range readDirectory(filepath.Join(store.root, "part", mid)) {
					if info, err := part.Info(); err == nil && info.Mode().IsRegular() {
						files[i].size += info.Size()
						if info.ModTime().After(files[i].mod.Time()) {
							files[i].mod = modOf(info.ModTime())
						}
					}
				}
			}
		}
	}
	files = traceOpenCodeWindow(files)
	seen := map[string]bool{}
	var out []TraceSpan
	for _, f := range files {
		path := "opencode:" + f.path
		seen[path] = true
		if !f.main || f.mod.Before(modOf(r.Since.Add(-2*time.Second))) || *budget <= 0 {
			continue
		}
		s := r.snapshot(path)
		if s.size == f.size && s.mod.Equal(f.mod.Time()) {
			continue
		}
		localBudget := int64(32 << 20)
		docs := traceOCMessages(f, &localBudget)
		// Remember oversized legacy snapshots until they change. SQLite uses
		// resumable row batches instead of this snapshot path.
		if localBudget <= 0 {
			*budget -= 32 << 20
			s.size, s.mod = f.size, f.mod.Time()
			continue
		}
		messages := make([]ocTraceMessage, 0, len(docs))
		for _, b := range docs {
			var m ocTraceMessage
			if json.Unmarshal(b, &m) != nil {
				continue
			}
			if m.Role == "" {
				m.Role = m.Type
			}
			if m.Model.ID != "" {
				m.ModelID = m.Model.ID
				m.ProviderID = m.Model.ProviderID
			}
			if _, ok := f.m().oc.(ocV2DB); !ok {
				for _, p := range traceOCParts(f, m.ID, &localBudget) {
					var part ocTracePart
					if json.Unmarshal(p, &part) == nil {
						m.Content = append(m.Content, part)
					}
				}
			}
			messages = append(messages, m)
		}
		if localBudget <= 0 {
			*budget -= 32 << 20
			s.size, s.mod = f.size, f.mod.Time()
			continue
		}
		*budget -= (32 << 20) - localBudget
		out = append(out, r.observations(s, traceOpenCode(f.m().sid, messages, bodies))...)
		s.size, s.mod = f.size, f.mod.Time()
	}
	for path := range r.snapshots {
		if strings.HasPrefix(path, "opencode:") && !seen[path] {
			delete(r.snapshots, path)
		}
	}
	return out
}

type ocTracePart struct {
	ID, Type, Text, Name, Tool, CallID string
	Synthetic, Ignored                 bool
	Time                               struct{ Created, Ran, Completed int64 }
	State                              struct {
		Status                        string
		Input, Output, Content, Error json.RawMessage
		Time                          struct{ Start, End int64 }
	}
}
type ocTraceMessage struct {
	ID, Type, Role, ParentID, ModelID, ProviderID, Finish, Text string
	Model                                                       struct{ ID, ProviderID string }
	Time                                                        struct{ Created, Completed int64 }
	Tokens                                                      *struct {
		Input, Output, Reasoning int
		Cache                    struct{ Read, Write int }
	}
	Error   json.RawMessage
	Content []ocTracePart
}

func traceOpenCode(sid string, messages []ocTraceMessage, bodies bool) []TraceSpan {
	sort.SliceStable(messages, func(i, j int) bool {
		if messages[i].Time.Created == messages[j].Time.Created {
			return messages[i].ID < messages[j].ID
		}
		return messages[i].Time.Created < messages[j].Time.Created
	})
	c := &traceCursor{agent: "opencode", session: sid, turns: map[string]*traceTurn{}}
	var out []TraceSpan
	var current *traceTurn
	roots := map[string]*traceTurn{}
	for _, m := range messages {
		if m.ID == "" || m.Time.Created <= 0 {
			continue
		}
		if m.Role == "user" {
			current = &traceTurn{id: m.ID, start: ms(m.Time.Created), last: ms(m.Time.Created)}
			if bodies {
				current.input = m.Text
				for _, p := range m.Content {
					if p.Type == "text" && !p.Synthetic && !p.Ignored {
						current.input += p.Text
					}
				}
			}
			roots[m.ID] = current
			continue
		}
		if m.Role != "assistant" {
			continue
		}
		t := roots[m.ParentID]
		if t == nil {
			if m.ParentID != "" {
				continue
			}
			t = current
		}
		if t == nil {
			continue
		}
		end := ms(m.Time.Completed)
		failed := len(m.Error) > 0 && string(m.Error) != "null"
		if !end.IsZero() {
			s := c.span(t.id, m.ID, "model "+m.ModelID, "generation", ms(m.Time.Created), end)
			s.Model, s.Provider, s.Error = m.ModelID, m.ProviderID, failed
			if u := m.Tokens; u != nil {
				s.Tokens = Tokens{Input: u.Input, Output: u.Output + u.Reasoning, CacheRead: u.Cache.Read, CacheWrite: u.Cache.Write}
				s.Reasoning = u.Reasoning
			}
			if bodies {
				s.Input = t.input
				b, _ := json.Marshal(m.Content)
				s.Output = string(b)
				t.output = s.Output
			}
			out = append(out, s)
			if end.After(t.last) {
				t.last = end
			}
			t.failed = t.failed || failed
		}
		for _, p := range m.Content {
			if p.Type != "tool" {
				continue
			}
			if p.State.Status != "completed" && p.State.Status != "error" {
				continue
			}
			start, done := ms(p.Time.Ran), ms(p.Time.Completed)
			if start.IsZero() {
				start = ms(p.Time.Created)
			}
			if start.IsZero() {
				start = ms(p.State.Time.Start)
			}
			if done.IsZero() {
				done = ms(p.State.Time.End)
			}
			name := p.Name
			if name == "" {
				name = p.Tool
			}
			key := p.ID
			if key == "" {
				key = p.CallID
			}
			if key == "" {
				continue
			}
			s := c.span(t.id, key, name, "tool", start, done)
			s.Error = p.State.Status == "error"
			if bodies {
				s.Input = string(p.State.Input)
				s.Output = string(p.State.Output)
				if s.Output == "" {
					s.Output = string(p.State.Content)
				}
				if s.Error {
					s.Output = string(p.State.Error)
				}
			}
			out = append(out, s)
			if done.After(t.last) {
				t.last = done
			}
			t.failed = t.failed || s.Error
		}
	}
	for _, t := range roots {
		out = append(out, c.root(t, t.last, t.failed))
	}
	return out
}
