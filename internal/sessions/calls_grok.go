package sessions

import (
	"bytes"
	"cmp"
	"encoding/json"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"time"

	toml "github.com/pelletier/go-toml/v2"
	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/filememo"
)

// Grok Build's own calls (#4423, liubing0806: its direct calls to x.ai
// never reached Usage) — read from the same updates.jsonl the Sessions
// page reads (parseGrok), each turn_completed's usage a call per model in
// modelUsage, on that model's own counts (grokUsage.tokens: input without
// what the cache held). An empty modelUsage is the session's model
// (current_model_id, else "grok"). The turn's time is the line's. A
// subagent's turns are in its own folder, a call of its own session. A
// fork counts the lines written from its created_at on: what it copied
// from its source is older, and was counted where it was first written.
//
// A turn asked through magpie's gateway is the gateway's to count, and is
// left out here. Matching it to the gateway's records can't be relied on:
// a turn_completed sums every request of the turn per model (modelCalls 4
// on a real one, 7 and 17 in liubing0806's), the gateway logs each request,
// and the model the sum is keyed by is the one the reply named
// (moonshotai/Kimi-K3 for a turn asked on magpie's group/auto-kimi-k3), so
// gatewayMatches would leave such a turn in Local session beside the
// gateway's own rows. Which model a turn was asked on is in its prompt's
// user_message_chunk (_meta.modelId: "group/auto-kimi-k3" for the
// [model."magpie/group/auto-kimi-k3"] table magpie writes in config.toml,
// "grok-4.7" for Grok's own), so a turn is the gateway's when that model
// is one of magpie's tables (grokGateway). Anything not told apart that
// way still goes through gatewayMatches as the others' calls do.
//
// Grok Build only appends to updates.jsonl, a line an event, so a file
// that grew is read on from where the last read left off
// (lineCallReaders).

// grokCallFiles are the session files Sessions lists for Grok Build
// (grokFiles): one updates.jsonl per session folder, beside its
// summary.json.
func grokCallFiles() []file { return grokFiles() }

// grokCallCarry is what a read of a session's updates carries on with:
// what of its summary the turns are counted under.
type grokCallCarry struct {
	Since time.Time `json:"since,omitzero"`
	Model string    `json:"model,omitempty"`
	// Asked is the model the session's last prompt was asked on (its
	// user_message_chunk's _meta.modelId), "" before one is read
	Asked string `json:"asked,omitempty"`
}

// grokCallsBegin starts a session's updates read from its start, with its
// summary: the model an empty modelUsage falls back to, and, for a fork,
// the time its source's lines were copied before.
func grokCallsBegin(st *callFile, f file) {
	st.Session = filepath.Base(filepath.Dir(f.path))
	c := &grokCallCarry{}
	if m, ok := readGrokSummary(f.m().manifest); ok {
		c.Model = m.CurrentModel
		if m.Parent != "" {
			// what it copied from its source is older than it
			if at, err := time.Parse(time.RFC3339Nano, m.CreatedAt); err == nil {
				c.Since = at.Truncate(time.Second)
			}
		}
	}
	st.Grok = c
}

// grokCallsKept is whether st holds what reading on needs: a disk shard
// written before the carry was kept there does not, and its file is read
// whole again once it changes.
func grokCallsKept(st *callFile) bool { return st.Grok != nil }

// grokCallLine reads one line of a session's updates: a turn_completed
// with usage is a call per model.
func grokCallLine(st *callFile, b []byte) {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || len(b) > maxLine {
		return
	}
	if bytes.Contains(b, grokUser) {
		// the model the turn is asked on, for grokGateway
		if c := st.Grok; c != nil {
			if id := gjson.GetBytes(b, "params.update._meta.modelId").String(); id != "" {
				c.Asked = id
			}
		}
		return
	}
	if !bytes.Contains(b, grokTurn) {
		return
	}
	at := numTS(b)
	if at.IsZero() {
		return
	}
	if c := st.Grok; c != nil && !c.Since.IsZero() && at.Before(c.Since) {
		return
	}
	var l grokLine
	if json.Unmarshal(b, &l) != nil {
		return
	}
	u := l.Params.Update
	if u.SessionUpdate != "turn_completed" || u.Usage == nil {
		return
	}
	if c := st.Grok; c != nil && grokGateway(cmp.Or(c.Asked, c.Model)) {
		return
	}
	add := func(model string, u grokUsage) {
		t := u.tokens()
		if t.zero() {
			return
		}
		st.Calls = append(st.Calls, Call{
			Agent:   "grok",
			Time:    at,
			Session: st.Session,
			Model:   st.str(model),
			Tokens:  t,
		})
	}
	if len(u.Usage.ModelUsage) == 0 {
		var model string
		if c := st.Grok; c != nil {
			model = c.Model
		}
		add(cmp.Or(model, "grok"), u.Usage.grokUsage)
		return
	}
	for _, name := range slices.Sorted(maps.Keys(u.Usage.ModelUsage)) {
		add(name, u.Usage.ModelUsage[name])
	}
}

// grokGateway is whether a turn asked on model went through magpie's
// gateway: the model is one of the [model."magpie/…"] tables magpie writes
// in Grok Build's config.toml (internal/agent's grok.go), by the table's
// name or its model (Grok names the turn's model by the latter), or names
// magpie's prefix itself. A config.toml that can't be read tells nothing
// more than the prefix: magpie writes its tables there, so a Grok Build
// wired to magpie has one to read.
func grokGateway(model string) bool {
	if model == "" {
		return false
	}
	if strings.HasPrefix(model, grokGatewayPrefix) {
		return true
	}
	return grokGatewayModels()[model]
}

const grokGatewayPrefix = "magpie/"

// grokGatewayModels are the models of magpie's tables in Grok Build's
// config.toml, read again only when it changes. What it gives is shared
// and must not be changed.
func grokGatewayModels() map[string]bool {
	m, _ := filememo.Read("sessions grok config", filepath.Join(GrokDir(), "config.toml"), func(b []byte) (map[string]bool, error) {
		var c struct {
			Model map[string]any `toml:"model"`
		}
		if err := toml.Unmarshal(b, &c); err != nil {
			return nil, err
		}
		out := map[string]bool{}
		for name, t := range c.Model {
			id, ok := strings.CutPrefix(name, grokGatewayPrefix)
			if !ok {
				continue
			}
			out[id] = true
			// a table, or (as a hand edit may leave it) an array of them
			tables, _ := t.([]any)
			if m, ok := t.(map[string]any); ok {
				tables = []any{m}
			}
			for _, x := range tables {
				if m, ok := x.(map[string]any); ok {
					if v, _ := m["model"].(string); v != "" {
						out[v] = true
					}
				}
			}
		}
		return out, nil
	})
	return m
}
