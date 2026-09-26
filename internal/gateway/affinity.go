package gateway

// Affinity: a conversation stays with the key or account that answered it,
// so that what the vendor cached of it — the whole conversation so far, on
// every request — is read again rather than sent afresh to someone else
// and paid for in full. Routing still decides who goes first when nobody
// has answered yet, and whenever the one that did is resting or all but
// used up.
//
// How long it stays is the provider's or group's affinity: for the whole
// session; within a turn only — while the agent sends tool results back,
// until the user speaks again; never; or, by default, worked out from the
// conversation itself: within a turn always, and across turns while what
// the vendor said it read from its cache the last time is worth keeping
// and not yet gone cold.

import (
	"encoding/json"
	"net/http"
	"regexp"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

const (
	// cacheWorth is how many tokens read from the vendor's cache make a
	// conversation worth keeping where it is across turns.
	cacheWorth = 1024
	// cacheCold is how long a vendor keeps a prompt cached without it being
	// read: five minutes at Anthropic and at OpenAI, the shortest there is.
	cacheCold = 5 * time.Minute
	// stickKeep is how long a conversation's last answerer is remembered.
	stickKeep = 24 * time.Hour
)

// Affinity is what the trace tells of a request's conversation and whether
// it stayed with who answered it last.
type Affinity struct {
	Mode      string    `json:"mode"`             // the provider's or group's: "", session, turn, off
	Turn      int       `json:"turn"`             // the user's turns in the conversation so far
	Within    bool      `json:"within,omitempty"` // the agent sending tool results back: mid-turn
	Last      string    `json:"last,omitempty"`   // who answered the conversation last
	LastTurn  int       `json:"lastTurn,omitempty"`
	At        time.Time `json:"at,omitempty"`        // when
	CacheRead int       `json:"cacheRead,omitempty"` // tokens that answer read from the vendor's cache
	Kept      bool      `json:"kept,omitempty"`      // Last was put first for it
	// Why it was kept — "session", "turn", "cache" — or not: "off", "first"
	// (nobody has answered it yet), "new-turn", "no-cache" (the vendor
	// read too little from its cache to keep), "cold" (too long ago),
	// "resting", "spent", "gone" (no longer one to route to).
	Why string `json:"why"`
}

type stick struct {
	rest      string
	who       string // the key or account, however many were on
	model     string // the model it answered as: a group may have several on one account
	turn      int
	at        time.Time
	cacheRead int
}

var sticks = struct {
	sync.Mutex
	m map[string]stick // scope|conversation → who answered it last
}{m: map[string]stick{}}

// turnOf counts the user's turns in a request's conversation, and says
// whether it is the agent handing tool results back within one.
func turnOf(from provider.Protocol, body []byte) (turn int, within bool) {
	req, err := parse(from, body)
	if err != nil {
		return 0, false
	}
	return turnIn(req)
}

// turnIn is turnOf for a request already parsed. The last of the user's
// messages tells whether the turn goes on: an agent may put its own notes
// after it (Claude Code a system message after the tool results).
func turnIn(req *Request) (turn int, within bool) {
	for _, m := range req.Messages {
		if m.Role != "user" {
			continue
		}
		text, result := false, false
		for _, p := range m.Parts {
			switch p.Kind {
			case Text, Image:
				text = true
			case ToolResult:
				result = true
			}
		}
		if text && !result {
			turn++
		}
		within = result
	}
	return turn, within
}

// affine puts first whoever answered the conversation last, while its
// affinity says to keep it there.
func affine(scope, mode string, rotate bool, in http.Header, from provider.Protocol, body []byte, cs []candidate, pl planned) ([]candidate, planned, *Affinity, string) {
	key := scope + "|" + conversationID(in, body)
	a := &Affinity{Mode: mode}
	a.Turn, a.Within = turnOf(from, body)
	sticks.Lock()
	st, had := sticks.m[key]
	sticks.Unlock()
	if had && time.Since(st.at) > stickKeep {
		had = false
	}
	if had {
		a.Last, a.LastTurn, a.At, a.CacheRead = st.rest, st.turn, st.at, st.cacheRead
	}
	// the account and the model that answered: a group with Opus and
	// Sonnet both on one Claude account, a rule sending the turn to Opus,
	// had the turn's next request kept to the account and so to Sonnet,
	// its first member there; the account alone when the model is gone
	at := -1
	for _, same := range []func(candidate) bool{
		func(c candidate) bool { return c.who() == st.who && c.model == st.model },
		func(c candidate) bool { return c.who() == st.who },
	} {
		for i, c := range cs {
			if had && at < 0 && same(c) {
				at, a.Last = i, c.rest // as it goes by now
			}
		}
	}
	switch {
	case mode == provider.AffinityOff:
		a.Why = "off"
	case !had:
		a.Why = "first"
	case at < 0:
		a.Why = "gone"
	case pl.order[at].Rest != nil:
		a.Why = "resting"
	case pl.order[at].Known && pl.order[at].Used >= usedShare:
		a.Why = "spent"
	case mode == provider.AffinitySession:
		a.Why = "session"
	case a.Within:
		a.Why = "turn"
	case mode == provider.AffinityTurn, rotate:
		a.Why = "new-turn"
	case st.cacheRead < cacheWorth:
		a.Why = "no-cache"
	case time.Since(st.at) > cacheCold:
		a.Why = "cold"
	default:
		a.Why = "cache"
	}
	switch a.Why {
	case "session", "turn", "cache":
		a.Kept = true
		if at > 0 {
			cs = append(append([]candidate{cs[at]}, cs[:at]...), cs[at+1:]...)
			order := append(append([]Weighed{pl.order[at]}, pl.order[:at]...), pl.order[at+1:]...)
			pl.order = order
		}
		for j := range pl.order {
			pl.order[j].Turn = false // kept, whoever's turn it was
		}
	case "new-turn":
		if rotate {
			cs, pl = after(cs, pl, at)
		}
	}
	return cs, pl, a, key
}

// after puts first the one after cs[at], round from the end, that isn't
// resting.
func after(cs []candidate, pl planned, at int) ([]candidate, planned) {
	for d := 1; d < len(cs); d++ {
		i := (at + d) % len(cs)
		if pl.order[i].Rest != nil || pl.order[i].Aside {
			continue // a turn goes round the keys routed over only
		}
		if i > 0 {
			cs = append(append([]candidate{cs[i]}, cs[:i]...), cs[i+1:]...)
			pl.order = append(append([]Weighed{pl.order[i]}, pl.order[:i]...), pl.order[i+1:]...)
		}
		for j := range pl.order {
			pl.order[j].Turn = j == 0 // its turn after the last's, not by the count
		}
		break
	}
	return cs, pl
}

// answered remembers who answered a conversation, and what it read from
// the vendor's cache doing so.
func answered(key string, c candidate, turn, cacheRead int) {
	now := time.Now()
	sticks.Lock()
	defer sticks.Unlock()
	sticks.m[key] = stick{rest: c.rest, who: c.who(), model: c.model, turn: turn, at: now, cacheRead: cacheRead}
	if len(sticks.m) > 4096 {
		for k, st := range sticks.m {
			if now.Sub(st.at) > stickKeep {
				delete(sticks.m, k)
			}
		}
	}
}

// foreignReasoning is how OpenAI refuses reasoning another account (or
// organization) sealed: "The encrypted content for item rs_… could not be
// verified", invalid_encrypted_content.
var foreignReasoning = regexp.MustCompile(`(?i)invalid_encrypted_content|encrypted content.{0,80}could not be (verified|decrypted)`)

// withoutReasoning takes the sealed reasoning out of a Responses request's
// input — what another account wrote and this one can't read. What was
// said and done stays; only the model's private notes to itself go.
func withoutReasoning(body []byte) ([]byte, bool) {
	var q map[string]json.RawMessage
	if json.Unmarshal(body, &q) != nil {
		return nil, false
	}
	var items []json.RawMessage
	if json.Unmarshal(q["input"], &items) != nil {
		return nil, false
	}
	kept := items[:0:0]
	for _, it := range items {
		var t struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(it, &t) == nil && t.Type == "reasoning" {
			continue
		}
		kept = append(kept, it)
	}
	if len(kept) == len(items) {
		return nil, false
	}
	q["input"], _ = json.Marshal(kept)
	b, err := json.Marshal(q)
	return b, err == nil
}
