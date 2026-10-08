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
//
// Who answered is kept on disk as well (affinity.json, next to the
// providers), so a restart — an update's included — doesn't hand a
// conversation to whoever routing puts first while the vendor still has
// it cached at the one before (vincentzhang on Discord).

import (
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/steady"
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
	// sticksKept is how many conversations, the latest, are kept on disk.
	sticksKept = 512
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
	effort    string // the effort the group's member it answered as is fixed at
	turn      int
	at        time.Time
	cacheRead int
}

var sticks = struct {
	sync.Mutex
	m map[string]stick // scope|conversation → who answered it last
	// the file as last read or written: another magpie's writes (the one
	// handing over to this one) are read again when a conversation is missed
	from string
	mod  time.Time
}{m: map[string]stick{}}

// sticksSaving keeps one write of the file at a time, the latest last.
var sticksSaving sync.Mutex

// savedStick is a stick as the file has it.
type savedStick struct {
	Rest      string    `json:"rest"`
	Who       string    `json:"who"`
	Model     string    `json:"model,omitempty"`
	Effort    string    `json:"effort,omitempty"`
	Turn      int       `json:"turn,omitempty"`
	At        time.Time `json:"at"`
	CacheRead int       `json:"cacheRead,omitempty"`
}

func sticksPath() string { return filepath.Join(filepath.Dir(provider.Path()), "affinity.json") }

// stickOf is who answered key last, read from the file again when it isn't
// known here and the file changed since: after a restart, or written by
// the magpie that handed over. Called with sticks held.
func stickOf(key string) (stick, bool) {
	if st, ok := sticks.m[key]; ok {
		return st, true
	}
	path := sticksPath()
	fi, err := os.Stat(path)
	if err != nil || path == sticks.from && fi.ModTime().Equal(sticks.mod) {
		return stick{}, false
	}
	sticks.from, sticks.mod = path, fi.ModTime()
	b, err := os.ReadFile(path)
	if err != nil {
		return stick{}, false
	}
	var saved map[string]savedStick
	if json.Unmarshal(b, &saved) != nil {
		return stick{}, false
	}
	for k, v := range saved {
		if time.Since(v.At) > stickKeep || v.Who == "" {
			continue
		}
		if st, ok := sticks.m[k]; ok && !st.at.Before(v.At) {
			continue
		}
		sticks.m[k] = stick{rest: v.Rest, who: v.Who, model: v.Model, effort: v.Effort, turn: v.Turn, at: v.At, cacheRead: v.CacheRead}
	}
	st, ok := sticks.m[key]
	return st, ok
}

// saveSticks writes the latest conversations' answerers to the file, only
// for this user to read.
func saveSticks() {
	sticksSaving.Lock()
	defer sticksSaving.Unlock()
	now := time.Now()
	type kept struct {
		k string
		v savedStick
	}
	var ks []kept
	sticks.Lock()
	for k, st := range sticks.m {
		if now.Sub(st.at) <= stickKeep {
			ks = append(ks, kept{k, savedStick{st.rest, st.who, st.model, st.effort, st.turn, st.at, st.cacheRead}})
		}
	}
	sticks.Unlock()
	slices.SortFunc(ks, func(a, b kept) int { return b.v.At.Compare(a.v.At) })
	ks = ks[:min(len(ks), sticksKept)]
	out := make(map[string]savedStick, len(ks))
	for _, k := range ks {
		out[k.k] = k.v
	}
	b, err := json.Marshal(out)
	if err != nil {
		return
	}
	path := sticksPath()
	if os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return
	}
	path, err = edit.Target(path) // a symlink stays, its target written
	if err != nil {
		return
	}
	tmp := path + ".magpie-tmp"
	if os.WriteFile(tmp, b, 0o600) != nil {
		return
	}
	if steady.Rename(tmp, path) != nil {
		os.Remove(tmp)
		return
	}
	if fi, err := os.Stat(path); err == nil {
		sticks.Lock()
		// what this magpie wrote isn't read back as another's
		sticks.from, sticks.mod = sticksPath(), fi.ModTime()
		sticks.Unlock()
	}
}

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
			case Text, Image, File:
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
	st, had := stickOf(key)
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
		func(c candidate) bool { return c.who() == st.who && c.model == st.model && c.effort == st.effort },
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
	case at > 0 && pl.order[at].Known && pl.order[at].Used >= provider.SpentShareOf(cs[at].p.Routing):
		// all but used up, and put behind another: first still — the
		// one key or account it has (#1016), or every one used up — it
		// doesn't move
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
			weightedKept(cs[0], cs[at])
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
	case "first":
		if rotate {
			cs, pl = leastHeld(scope, cs, pl)
		}
	}
	return cs, pl, a, key
}

// heldFor is how long after its last answer a conversation still counts as
// holding the one that answered it: a session whose user is reading or
// typing is still going.
const heldFor = 30 * time.Minute

// leastHeld puts first, for a conversation nobody has answered yet, the
// one in turn that the fewest other conversations in scope are held on
// (#946): in turn counts requests, not conversations, so a second session
// started after the first had sent an even number of them went to the
// model the first was kept on. Of those held on as few, routing's order
// stands.
func leastHeld(scope string, cs []candidate, pl planned) ([]candidate, planned) {
	if len(cs) < 2 || len(pl.order) < len(cs) {
		return cs, pl
	}
	if strings.HasPrefix(scope, provider.GroupPrefix) {
		if _, ms, ok := provider.FindGroup(scope); ok {
			for _, m := range ms {
				if len(m.Path) > 1 {
					return cs, pl // a group in the group: its own routing orders its models (#576)
				}
			}
		}
	}
	now := time.Now()
	held := map[string]int{}
	sticks.Lock()
	for k, st := range sticks.m {
		if strings.HasPrefix(k, scope+"|") && now.Sub(st.at) <= heldFor {
			held[st.who+"/"+provider.WithMemberEffort(st.model, st.effort)]++
		}
	}
	sticks.Unlock()
	if len(held) == 0 {
		return cs, pl
	}
	seat := func(c candidate) string { return c.who() + "/" + provider.WithMemberEffort(c.model, c.effort) }
	best := -1
	for i, c := range cs {
		if pl.order[i].Rest != nil || pl.order[i].Aside {
			continue
		}
		if best < 0 || held[seat(c)] < held[seat(cs[best])] {
			best = i
		}
	}
	if best <= 0 {
		return cs, pl
	}
	cs = append(append([]candidate{cs[best]}, cs[:best]...), cs[best+1:]...)
	pl.order = append(append([]Weighed{pl.order[best]}, pl.order[:best]...), pl.order[best+1:]...)
	for j := range pl.order {
		pl.order[j].Turn = j == 0
	}
	return cs, pl
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
	sticks.m[key] = stick{rest: c.rest, who: c.who(), model: c.model, effort: c.effort, turn: turn, at: now, cacheRead: cacheRead}
	if len(sticks.m) > 4096 {
		for k, st := range sticks.m {
			if now.Sub(st.at) > stickKeep {
				delete(sticks.m, k)
			}
		}
	}
	sticks.Unlock()
	saveSticks()
}

// unanswered forgets that c answered a conversation, when its reply to it
// broke off (#733): the agent's retry goes by routing again, not back to
// the one that just failed it. The account alone is enough: affine's
// widest match keeps a conversation on an account whose answerer's model
// has since left the group, so a stick that only matched by its model
// would leave the next request kept on the account that just broke off.
func unanswered(key string, c candidate) {
	sticks.Lock()
	st, ok := stickOf(key)
	ok = ok && st.who == c.who()
	if ok {
		delete(sticks.m, key)
	}
	sticks.Unlock()
	if ok {
		saveSticks()
	}
}

// foreignReasoning is how a vendor refuses reasoning another account (or
// organization, or vendor) sealed: OpenAI's "The encrypted content for
// item rs_… could not be verified", invalid_encrypted_content; xAI's
// (Grok's API and a SuperGrok account alike) "Could not decrypt the
// provided encrypted_content. Ensure the value is the unmodified
// encrypted_content from a previous response."
var foreignReasoning = regexp.MustCompile(`(?i)invalid_encrypted_content|encrypted[ _]content.{0,80}could not be (verified|decrypted)|could not (decrypt|verify).{0,40}encrypted[ _]content`)

// refusedSeals remembers, per conversation and the key or account that
// refused it, the sealed reasoning another one wrote. Codex hands every
// earlier turn's reasoning back on each request, so once a conversation
// has moved, what was sealed before it moved is left out of its later
// requests there up front, rather than refused and asked again each turn.
// Reasoning the one it moved to sealed itself still goes along, for its
// cache and its train of thought.
var refusedSeals = struct {
	sync.Mutex
	m map[string]sealsRefused // scope|conversation|who → what it refused
}{m: map[string]sealsRefused{}}

type sealsRefused struct {
	at    time.Time
	seals map[[sha256.Size]byte]bool
}

// sealedItem is what's read of an input item to tell sealed reasoning.
type sealedItem struct {
	Type string `json:"type"`
	Enc  string `json:"encrypted_content"`
}

// sealedKinds are the input items a vendor seals for itself: its
// reasoning, and the compaction a conversation's earlier turns were
// folded into (OpenAI's, when Codex compacts on its own models; magpie's
// own is text by the time a request is sent).
var sealedKinds = map[string]bool{"reasoning": true, "compaction": true, "compaction_summary": true}

var compactionKinds = []string{"compaction", "compaction_summary"}

// seals are the sealed reasoning and compactions (encrypted_content) in a
// Responses request's input, of these types.
func seals(body []byte, kinds ...string) [][sha256.Size]byte {
	var q struct {
		Input []sealedItem `json:"input"`
	}
	if json.Unmarshal(body, &q) != nil {
		return nil
	}
	var out [][sha256.Size]byte
	for _, it := range q.Input {
		if slices.Contains(kinds, it.Type) && it.Enc != "" {
			out = append(out, sha256.Sum256([]byte(it.Enc)))
		}
	}
	return out
}

// refused notes that who turned away the sealed items of these types in
// body, in the conversation key names.
func refused(key, who string, body []byte, kinds ...string) {
	ss := seals(body, kinds...)
	if len(ss) == 0 {
		return
	}
	now := time.Now()
	refusedSeals.Lock()
	defer refusedSeals.Unlock()
	k := key + "|" + who
	r := refusedSeals.m[k]
	if r.seals == nil {
		r.seals = map[[sha256.Size]byte]bool{}
	}
	for _, s := range ss {
		r.seals[s] = true
	}
	r.at = now
	refusedSeals.m[k] = r
	if len(refusedSeals.m) > 4096 {
		for k, r := range refusedSeals.m {
			if now.Sub(r.at) > stickKeep {
				delete(refusedSeals.m, k)
			}
		}
	}
}

// withoutRefused takes out of a Responses request the sealed reasoning
// and compactions who already refused in this conversation; the rest
// stays as it is.
func withoutRefused(key, who string, body []byte) ([]byte, bool) {
	k := key + "|" + who
	refusedSeals.Lock()
	r, ok := refusedSeals.m[k]
	if ok && time.Since(r.at) > stickKeep {
		delete(refusedSeals.m, k)
		ok = false
	}
	refusedSeals.Unlock()
	if !ok {
		return nil, false
	}
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
		var t sealedItem
		if json.Unmarshal(it, &t) == nil && sealedKinds[t.Type] && t.Enc != "" && r.seals[sha256.Sum256([]byte(t.Enc))] {
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

// withoutReasoning takes the sealed reasoning out of a Responses request's
// input — what another account wrote and this one can't read. What was
// said and done stays; only the model's private notes to itself go.
func withoutReasoning(body []byte) ([]byte, bool) {
	return withoutKinds(body, "reasoning")
}

// withoutCompaction takes out of a Responses request's input the sealed
// compaction of its earlier turns — OpenAI's, which no other vendor or
// account can read (waroy: Grok's "Could not decrypt the provided
// encrypted_content" with the reasoning already gone). What the turns
// since said and did stays; the summary of those before goes.
func withoutCompaction(body []byte) ([]byte, bool) {
	var q struct {
		Input []sealedItem `json:"input"`
	}
	if json.Unmarshal(body, &q) != nil {
		return nil, false
	}
	for _, it := range q.Input {
		if slices.Contains(compactionKinds, it.Type) && it.Enc != "" {
			return withoutKinds(body, compactionKinds...)
		}
	}
	return nil, false
}

// withoutKinds is a Responses request without its input items of these
// types.
func withoutKinds(body []byte, kinds ...string) ([]byte, bool) {
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
		if json.Unmarshal(it, &t) == nil && slices.Contains(kinds, t.Type) {
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
