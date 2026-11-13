package gateway

// An agent that changes its reasoning effort mid-thread changes the
// request's reasoning.effort, and the upstream's cached prompt with it
// (#617). OpenAI's Responses API takes the change as an input item instead
// ("Change reasoning mid-conversation", developers.openai.com/api/docs/
// guides/reasoning): the thread's first effort stays at the top, and
//
//	{"type": "configuration_update", "reasoning": {"effort": "high"}}
//
// goes before the next user message, in the history from then on, so the
// prefix the cache keeps stays as it was. The GPT-6 family takes it, but
// reasons less on an update than on the same effort at the top, so only a
// lower effort goes as one: a higher one starts the thread again at it.
//
// The agent's own history has no such items, so magpie keeps, for each
// thread on each account and model, the effort it started at and where it
// put each update, and puts them back on every later request. A history
// that isn't the last one with more after it (compacted, edited, another
// thread under the same key) starts the thread again, at the effort it
// asks — what a request costs without any of this. Lost on a restart, a
// thread's state costs one cache miss, as before.

import (
	"crypto/sha256"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// effortThread is a thread's reasoning as magpie sends it.
type effortThread struct {
	base    string         // the effort at the top, the thread's first
	updates []effortUpdate // in order of where they go
	n       int            // how many items the last request's input had
	sum     [sha256.Size]byte
	at      time.Time
}

// effortUpdate is a configuration_update put before the agent's input
// item at (an index into the agent's own input).
type effortUpdate struct {
	at     int
	effort string
}

// effort is the thread's effort after its updates.
func (t *effortThread) effort() string {
	if len(t.updates) > 0 {
		return t.updates[len(t.updates)-1].effort
	}
	return t.base
}

var effortThreads = struct {
	sync.Mutex
	m map[string]*effortThread
	// no is an account and model whose upstream turned the items away:
	// its requests go as the agent sent them
	no map[string]time.Time
}{m: map[string]*effortThread{}, no: map[string]time.Time{}}

const effortThreadKeep = 24 * time.Hour

// takesEffortUpdates: a GPT-6 model, through a ChatGPT account or
// OpenAI's own API, asked in Responses, with MAGPIE_EFFORT_UPDATES=on — a
// preview until it is seen on the real backends: one that took the item
// and ignored it would leave an effort change doing nothing, unseen.
func takesEffortUpdates(p provider.Provider, model string) bool {
	if os.Getenv("MAGPIE_EFFORT_UPDATES") != "on" || p.Base(provider.Responses) == "" {
		return false
	}
	if !(p.Account != nil && p.Account.Agent == "codex" || p.Account == nil && strings.HasSuffix(p.VendorHost(), "openai.com")) {
		return false
	}
	if i := strings.LastIndexByte(model, '/'); i >= 0 {
		model = model[i+1:]
	}
	return strings.HasPrefix(strings.ToLower(model), "gpt-6")
}

// withEffortUpdates is a Responses request to who's model, asking for
// effort (as fitted to the model), with the thread's effort changes as
// configuration_update items, conv naming the conversation; ok is false
// when it goes as it is.
func withEffortUpdates(conv, who, model, effort string, body []byte) ([]byte, bool) {
	var q map[string]json.RawMessage
	if json.Unmarshal(body, &q) != nil {
		return nil, false
	}
	var reasoning map[string]json.RawMessage
	if json.Unmarshal(q["reasoning"], &reasoning) != nil || reasoning == nil {
		effort = ""
	}
	var items []json.RawMessage
	if json.Unmarshal(q["input"], &items) != nil || len(items) == 0 {
		return nil, false
	}
	var key string
	json.Unmarshal(q["prompt_cache_key"], &key)
	k := conv + "|" + key + "|" + who + "|" + model
	acct := who + "|" + model

	effortThreads.Lock()
	defer effortThreads.Unlock()
	now := time.Now()
	if t, ok := effortThreads.no[acct]; ok && now.Sub(t) < effortThreadKeep {
		return nil, false
	}
	// what the update can't go with: a history the server keeps, the
	// API's automatic truncation or compaction, an agent that sends its
	// own updates, or a request without an effort
	_, prev := q["previous_response_id"]
	_, managed := q["context_management"]
	if prev || managed || string(q["truncation"]) == `"auto"` || effort == "" || effort == "none" ||
		bodyHas(q["input"], `"configuration_update"`) {
		delete(effortThreads.m, k)
		return nil, false
	}

	t := effortThreads.m[k]
	if t != nil && (len(items) < t.n || itemsSum(items[:t.n]) != t.sum || now.Sub(t.at) > effortThreadKeep) {
		t = nil
	}
	if t == nil {
		effortThreads.m[k] = &effortThread{base: effort, n: len(items), sum: itemsSum(items), at: now}
		pruneEffortThreads(now)
		return nil, false
	}
	if slices.Index(effortRank, effort) > slices.Index(effortRank, t.base) {
		// more than the thread started at: the backend gives an update
		// a fraction of the reasoning it asks — gpt-6.1-sol started at
		// low and updated to xhigh reasons as at high, a quarter of
		// xhigh's (Lutra-Fs, #617) — so the thread starts again at it, at
		// the top: the cache is lost once for each level up, as a group's
		// auto effort loses it (keepsEffort), and the reasoning is had
		effortThreads.m[k] = &effortThread{base: effort, n: len(items), sum: itemsSum(items), at: now}
		return nil, false
	}
	if effort != t.effort() {
		// before the first user message this turn added; with none, at
		// its end — between the replies either way
		at := len(items)
		for i := t.n; i < len(items); i++ {
			if isUserItem(items[i]) {
				at = i
				break
			}
		}
		if n := len(t.updates); n > 0 && t.updates[n-1].at == at {
			// the same turn asked again at another effort: one update
			// there, none when it's back to what it was before
			t.updates = t.updates[:n-1]
			if effort != t.effort() {
				t.updates = append(t.updates, effortUpdate{at, effort})
			}
		} else {
			t.updates = append(t.updates, effortUpdate{at, effort})
		}
	}
	t.n, t.sum, t.at = len(items), itemsSum(items), now
	if len(t.updates) == 0 {
		return nil, false // still at the effort it started at
	}

	out := make([]json.RawMessage, 0, len(items)+len(t.updates))
	u := 0
	for i := 0; i <= len(items); i++ {
		for u < len(t.updates) && t.updates[u].at == i {
			e := t.updates[u].effort
			if e == "ultra" {
				e = "max" // Codex CLI's name for it
			}
			raw, _ := marshalPlain(map[string]any{"type": "configuration_update", "reasoning": map[string]any{"effort": e}})
			out = append(out, raw)
			u++
		}
		if i < len(items) {
			out = append(out, items[i])
		}
	}
	reasoning["effort"], _ = json.Marshal(t.base)
	q["reasoning"], _ = marshalPlain(reasoning)
	q["input"], _ = marshalPlain(out)
	b, err := marshalPlain(q)
	if err != nil {
		return nil, false
	}
	return b, true
}

// effortUpdatesRefused notes that who's model turned the items away: its
// requests go as the agent sent them from now on.
func effortUpdatesRefused(who, model string) {
	effortThreads.Lock()
	defer effortThreads.Unlock()
	effortThreads.no[who+"|"+model] = time.Now()
}

func pruneEffortThreads(now time.Time) {
	if len(effortThreads.m) <= 4096 {
		return
	}
	for k, t := range effortThreads.m {
		if now.Sub(t.at) > time.Hour {
			delete(effortThreads.m, k)
		}
	}
}

func itemsSum(items []json.RawMessage) [sha256.Size]byte {
	h := sha256.New()
	for _, it := range items {
		h.Write(it)
		h.Write([]byte{0})
	}
	var s [sha256.Size]byte
	h.Sum(s[:0])
	return s
}

func isUserItem(raw json.RawMessage) bool {
	var it struct {
		Type string `json:"type"`
		Role string `json:"role"`
	}
	return json.Unmarshal(raw, &it) == nil && it.Role == "user" && (it.Type == "" || it.Type == "message")
}

// Codex sends such updates itself with its reasoning_effort_override
// feature on (0.162.1): an effort switched mid-thread (/model, then "s")
// leaves reasoning.effort at the thread's first and goes as a
// configuration_update after the turn's user message. The last one is the
// effort the request is asked at; read from the top alone, Routing showed
// medium for a thread switched to xhigh, and a vendor the items are taken
// out for (forVendor) or a translation was asked at medium (zzzkl3994 on
// Discord).

// effortUpdated is the effort the last configuration_update in a Responses
// request's input asks for: "" with none.
func effortUpdated(body []byte) string {
	if !bodyHas(body, `"configuration_update"`) {
		return ""
	}
	e := ""
	inputItems(body, func(it []byte) bool {
		if string(memberOrNil(it, "type")) == `"configuration_update"` {
			if u := updateEffort(it); u != "" {
				e = u
			}
		}
		return true
	})
	return e
}

// updateEffort is the effort a configuration_update item asks for.
func updateEffort(it []byte) string {
	var u struct {
		Reasoning *struct {
			Effort string `json:"effort"`
		} `json:"reasoning"`
	}
	if decodeJSON(it, &u) != nil || u.Reasoning == nil {
		return ""
	}
	return u.Reasoning.Effort
}

// withEffortUpdated is a Responses request whose last configuration_update
// asks for effort, the rest as it came: magpie's own pick for the request
// goes where the agent's last change of effort went, so the upstream
// that reads the items isn't asked another by them, and the thread's
// prefix stays as it was. The body as it is when it has none.
func withEffortUpdated(body []byte, effort string) []byte {
	if !bodyHas(body, `"configuration_update"`) {
		return body
	}
	q, err := rawObject(body)
	if err != nil || q == nil {
		return body
	}
	items, err := rawArray(q["input"])
	if err != nil {
		return body
	}
	last := -1
	for i, raw := range items {
		if string(memberOrNil(raw, "type")) == `"configuration_update"` && updateEffort(raw) != "" {
			last = i
		}
	}
	if last < 0 {
		return body
	}
	it, err := rawObject(items[last])
	if err != nil || it == nil {
		return body
	}
	reasoning, err := rawObject(it["reasoning"])
	if err != nil || reasoning == nil {
		return body
	}
	reasoning["effort"], _ = json.Marshal(effort)
	it["reasoning"], _ = marshalPlain(reasoning)
	items[last], _ = marshalPlain(it)
	q["input"], _ = marshalPlain(items)
	b, err := marshalPlain(q)
	if err != nil {
		return body
	}
	return b
}
