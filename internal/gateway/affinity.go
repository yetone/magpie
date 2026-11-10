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
// the vendor said it read from its cache the last time, or wrote to it, is
// worth keeping and not yet gone cold. Written counts as read: a first
// turn reads nothing and writes the whole prompt, which the next turn
// reads if it stays (vilicvane on Discord: the second turn moved, and the
// cache just built was thrown away). A vendor that keeps a prompt cached
// without reporting it — OpenAI's automatic prefix cache, Gemini — says
// neither of a turn that read nothing, and what its cache holds then is
// the prompt that turn was sent: that counts as written too, so its next
// turn stays rather than building the cache afresh elsewhere.
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
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/steady"
)

const (
	// cacheWorth is how many tokens read from the vendor's cache, or
	// written to it — or, from a vendor that reports neither, sent — make
	// a conversation worth keeping where it is across turns.
	cacheWorth = 1024
	// cacheCold is how long a vendor keeps a prompt cached without it being
	// read: five minutes at Anthropic and at OpenAI, the shortest there is.
	cacheCold = 5 * time.Minute
	// chatGPTCold is how long the ChatGPT backend keeps a Codex
	// conversation's prompt cached without it being read. Measured on a
	// real account (10-27, gpt-5.6-terra), a Codex thread left 7, 12, 25,
	// 45 and 60 minutes read back from cache all it had sent before (79–99%
	// of each request), so it is
	// held for the hour measured, not cacheCold's five minutes.
	// Moving a conversation between two ChatGPT accounts after five
	// minutes sent its whole context to the other uncached, and the
	// reasoning the first had sealed was refused there (leelantao on X:
	// two Plus accounts' allowance spent 90% in a day).
	chatGPTCold = time.Hour
	// deepseekCold is how long DeepSeek's own API keeps a conversation's
	// prefix cached without it being read. Its context caching guide
	// (api-docs.deepseek.com/guides/kv_cache, read 10-31) says an unused
	// cache is cleared "usually within a few hours to a few days", and a
	// real Claude Code conversation on deepseek-v4-pro (10-31) read back
	// 98–99% of its prompt after 7 and 26 minutes idle. Moving it to
	// another DeepSeek key after five minutes sent it all again uncached.
	deepseekCold = time.Hour
	// cacheHour is how long Anthropic keeps what was written with
	// cache_control's ttl "1h" (cache_creation's ephemeral_1h_input_tokens)
	// without it being read, as Claude Code asks of a subscription (#2302)
	cacheHour = time.Hour
	// stickKeep is how long a conversation's last answerer is remembered.
	stickKeep = 24 * time.Hour
	// sticksKept is how many conversations, the latest, are kept on disk.
	sticksKept = 512
)

// coldAfter is how long p's vendor keeps a prompt cached without it being
// read: cacheCold, chatGPTCold for a ChatGPT account, or deepseekCold on
// DeepSeek's own API.
func coldAfter(p provider.Provider) time.Duration {
	if p.Account != nil && p.Account.Agent == "codex" {
		return chatGPTCold
	}
	if p.VendorHost() == deepseekHost {
		return deepseekCold
	}
	return cacheCold
}

// Affinity is what the trace tells of a request's conversation and whether
// it stayed with who answered it last.
type Affinity struct {
	Mode       string    `json:"mode"`             // the provider's or group's: "", session, turn, off
	Turn       int       `json:"turn"`             // the user's turns in the conversation so far
	Within     bool      `json:"within,omitempty"` // the agent sending tool results back: mid-turn
	Last       string    `json:"last,omitempty"`   // who answered the conversation last
	LastTurn   int       `json:"lastTurn,omitempty"`
	At         time.Time `json:"at,omitempty"`         // when
	CacheRead  int       `json:"cacheRead,omitempty"`  // tokens that answer read from the vendor's cache
	CacheWrite int       `json:"cacheWrite,omitempty"` // and wrote to it, or was sent where none is
	Kept       bool      `json:"kept,omitempty"`       // Last was put first for it
	// Why it was kept — "session", "turn", "cache" — or not: "off", "first"
	// (nobody has answered it yet), "new-turn", "no-cache" (the vendor
	// read and wrote too little of its cache to keep, or was sent too
	// little for a cache it keeps without saying so), "cold" (too long
	// ago), "resting", "spent", "gone" (no longer one to route to),
	// "main" (a provider's fallback answered it, and the provider's own
	// model is ready again: backToMain), "order" (a later member of a
	// group routed In order answered it, and a member ahead is ready
	// again at a new turn: backToFirst), "back" (a member of a group
	// routed any other way stood in for one listed ahead of it that
	// couldn't take the conversation, and a member ahead is ready first
	// at a new turn: backFromStandIn), "credits" (a Codex account gone
	// onto its credits answered it, set to spend them only after
	// everything else, and one not on its credits leads: lastBehind),
	// "busy" (its plugin says the model is busier than settings.BusyLoad
	// and one that isn't leads, at a new turn: busyBehind),
	// "strict" (Stays: Strict, kept whatever it is), "lead" (put with the
	// key or account its session's main conversation is on: withSession).
	Why string `json:"why"`
	// Main is, for a conversation kept strictly on a provider's fallback,
	// the provider's own key or account that is ready again: it would have
	// gone back to it ("main") but for Stays: Strict, which the Routing
	// page says.
	Main *Weighed `json:"main,omitempty"`
	// LeadModel is, for Why "lead", the model of the session's main
	// conversation, whose key or account this call went to (withSession).
	LeadModel string `json:"leadModel,omitempty"`
}

type stick struct {
	rest       string
	who        string // the key or account, however many were on
	model      string // the model it answered as: a group may have several on one account
	effort     string // the effort the group's member it answered as is fixed at
	turn       int
	at         time.Time
	cacheRead  int
	cacheWrite int
	hour       hourCache
	// stoodIn: it answered while a member the group lists ahead of its
	// own couldn't take the conversation (standsInFor), or answered it
	// on after one that had: a stand-in, given back at a new turn once a
	// member ahead is ready (backFromStandIn). Kept on disk too
	stoodIn bool
}

// hourCache is the part of a conversation's prompt the vendor said it keeps
// for an hour: Anthropic's ephemeral_1h_input_tokens, with what was read
// before them, since its 1-hour breakpoints come before any 5-minute one.
// It stays warm past cacheCold, until an hour after it was last written or
// read (#2302: Stays: Auto moved a conversation as "cold" 5 minutes after
// a 1-hour write). Vendors that report no 1-hour write never have one.
type hourCache struct {
	tokens int
	at     time.Time
}

// after is what is kept for an hour once a reply with usage u came at now.
// A reply that wrote for an hour keeps all it read and that; one that read
// the whole of what was kept refreshes it; one that read less no longer
// starts with it (moved, compacted), and it is gone.
func (h hourCache) after(u Usage, now time.Time) hourCache {
	switch {
	case u.CacheWrite1h > 0:
		return hourCache{tokens: u.CacheRead + u.CacheWrite1h, at: now}
	case h.tokens > 0 && now.Sub(h.at) <= cacheHour && u.CacheRead >= h.tokens:
		return hourCache{tokens: h.tokens, at: now}
	}
	return hourCache{}
}

// warm is whether enough is still kept at now to stay for.
func (h hourCache) warm(now time.Time) bool {
	return h.tokens >= cacheWorth && now.Sub(h.at) <= cacheHour
}

var sticks = struct {
	sync.Mutex
	m map[string]stick // scope|conversation → who answered it last
	// opening: scope|conversation → who a conversation that nobody has
	// answered yet was sent to, while its first request is out. In turn,
	// leastHeld counts them with those answered, so a burst of new
	// conversations spreads rather than all going to the one holding the
	// fewest a moment before (#1342)
	opening map[string]stick
	// the file as last read or written: another magpie's writes (the one
	// handing over to this one) are read again when a conversation is missed
	from string
	mod  time.Time
}{m: map[string]stick{}, opening: map[string]stick{}}

// sticksSaving keeps one write of the file at a time, the latest last.
var sticksSaving sync.Mutex

// sticksFlushAfter is how long a conversation answered waits before the
// file is written: what is answered meanwhile goes in the same write, so a
// gateway answering many requests a second writes it once in a while, not
// once a request (up to 512 conversations marshalled, written and renamed
// on every answer, each after the last). A crash loses the last moment's;
// flushSticks, as the gateway ends, loses nothing. In a test binary it is
// 0, the file written at once, as it was: a write left waiting would
// outlive the test and write into its temp folder as it is removed.
var sticksFlushAfter = func() time.Duration {
	if testing.Testing() {
		return 0
	}
	return 2 * time.Second
}()

// sticksFlush is the write noteSticks has waiting, for the file at path.
var sticksFlush struct {
	sync.Mutex
	pending bool
	path    string
}

// noteSticks has the file written within sticksFlushAfter: what this
// magpie answered is in memory at once.
func noteSticks() {
	if sticksFlushAfter <= 0 {
		saveSticks()
		return
	}
	path := sticksPath()
	sticksFlush.Lock()
	if sticksFlush.pending && sticksFlush.path == path {
		sticksFlush.Unlock()
		return
	}
	sticksFlush.pending, sticksFlush.path = true, path
	sticksFlush.Unlock()
	time.AfterFunc(sticksFlushAfter, func() { flushSticksAt(path) })
}

// flushSticks writes what noteSticks has waiting: the gateway calls it as
// it ends, so the magpie it hands over to, or the next start, reads it.
func flushSticks() { flushSticksAt(sticksPath()) }

// FlushSticks is flushSticks, for magpie ending on a signal before the
// gateway's own end.
func FlushSticks() { flushSticks() }

// flushSticksAt writes the file at path when a write for it is waiting:
// one waiting for another (a test's HOME before this one's) is dropped.
func flushSticksAt(path string) {
	sticksFlush.Lock()
	if !sticksFlush.pending || sticksFlush.path != path {
		sticksFlush.Unlock()
		return
	}
	sticksFlush.pending = false
	sticksFlush.Unlock()
	if path == sticksPath() {
		saveSticks()
	}
}

// savedStick is a stick as the file has it.
type savedStick struct {
	Rest      string    `json:"rest"`
	Who       string    `json:"who"`
	Model     string    `json:"model,omitempty"`
	Effort    string    `json:"effort,omitempty"`
	Turn      int       `json:"turn,omitempty"`
	At        time.Time `json:"at"`
	CacheRead int       `json:"cacheRead,omitempty"`
	// CacheWrite is absent from a file written before it was kept, and
	// reads as 0: such a stick is judged by what was read, as it was then
	CacheWrite int `json:"cacheWrite,omitempty"`
	// CacheHour and CacheHourAt are its hourCache, absent where none was
	// written for an hour
	CacheHour   int       `json:"cacheHour,omitempty"`
	CacheHourAt time.Time `json:"cacheHourAt,omitzero"`
	// StoodIn is its stoodIn, absent from a file written before it was
	// kept: such a stick is no stand-in, kept by its cache as it was
	StoodIn bool `json:"stoodIn,omitempty"`
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
	if decodeJSON(b, &saved) != nil {
		return stick{}, false
	}
	for k, v := range saved {
		if time.Since(v.At) > stickKeep || v.Who == "" {
			continue
		}
		if st, ok := sticks.m[k]; ok && !st.at.Before(v.At) {
			continue
		}
		sticks.m[k] = stick{rest: v.Rest, who: v.Who, model: v.Model, effort: v.Effort, turn: v.Turn, at: v.At, cacheRead: v.CacheRead, cacheWrite: v.CacheWrite,
			hour: hourCache{tokens: v.CacheHour, at: v.CacheHourAt}, stoodIn: v.StoodIn}
	}
	st, ok := sticks.m[key]
	return st, ok
}

// saveSticks writes the latest conversations' answerers to the file, only
// for this user to read.
func saveSticks() {
	sticksSaving.Lock()
	defer sticksSaving.Unlock()
	sticksFlush.Lock()
	if sticksFlush.path == sticksPath() {
		sticksFlush.pending = false // this write is the one waiting
	}
	sticksFlush.Unlock()
	now := time.Now()
	type kept struct {
		k string
		v savedStick
	}
	var ks []kept
	sticks.Lock()
	for k, st := range sticks.m {
		if now.Sub(st.at) <= stickKeep {
			ks = append(ks, kept{k, savedStick{st.rest, st.who, st.model, st.effort, st.turn, st.at, st.cacheRead, st.cacheWrite, st.hour.tokens, st.hour.at, st.stoodIn}})
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
	f := factsOf(from, body)
	return f.turn, f.within
}

// turnIn is turnOf for a request already parsed. The last of the user's
// messages tells whether the turn goes on: an agent may put its own notes
// after it (Claude Code a system message after the tool results).
//
// A turn begins with what the user said (userSaid) or an image or file of
// theirs. A user message the agent wrote alone — a task notification, a
// todo nudge, an agent list, Codex's context — begins none: after an
// answer it carries the turn on, and right after the user's own message
// it leaves that as it was.
//
// An image beside the turn's tool results is the result's, as the bridge
// tells it (match): a client whose tool messages hold text only sends it
// in a user message after (pi's "Attached image(s) from tool result:").
// It carries the turn on, a turn of the user's own begun none (#5767):
// counted one, the router re-decided the conversation's account at every
// image result, and the bridge let the waiting run go under the other
// owner ("tool results account changed"), a new Claude Code told the
// whole conversation, the prompt cache rewritten each time.
func turnIn(req *Request) (turn int, within bool) {
	answered, results := false, false
	for _, m := range req.Messages {
		if m.Role == "assistant" {
			answered = true
			results = false
			continue
		}
		if m.Role != "user" {
			continue
		}
		said, result := saysAnything(m), false
		for _, p := range m.Parts {
			switch p.Kind {
			case Image, File:
				said = true
			case ToolResult:
				result = true
			}
		}
		switch {
		case result:
			within = true
			results = true
		case results && withImages(m):
			// the words beside the images are their caption, as match
			// reads them; the user's words in a message of no images
			// begin a turn (saidWithResults)
			within = true
		case said:
			turn++
			within = false
		case answered:
			within = turn > 0
		}
		answered = false
	}
	return turn, within
}

// strictKeep is, for a conversation that stays strictly where it is
// (provider.AffinityStrict, #1797), the account that answered it brought
// back when the plan left it out at a usage cap: the cap set on it keeps
// new conversations off it, and one already on it goes on there, up to
// the vendor's own limit. One left out only as it won't spend its credits
// isn't brought back, the user's own setting, and the conversation doesn't
// move either: held is it, for the request to be refused as held. Neither
// for another mode, nor for a conversation nobody has answered yet.
func strictKeep(scope, mode string, in http.Header, body []byte, cs []candidate, pl planned) ([]candidate, planned, []Weighed) {
	if mode != provider.AffinityStrict {
		return cs, pl, nil
	}
	sticks.Lock()
	st, had := stickOf(stickKey(scope, in, body))
	sticks.Unlock()
	if !had || time.Since(st.at) > stickKeep || slices.ContainsFunc(cs, func(c candidate) bool { return c.sameVendor(st.who, st.model) }) {
		return cs, pl, nil
	}
	// the left's line for c, as it was left out
	leftAt := func(c candidate) int {
		return slices.IndexFunc(pl.left, func(w Weighed) bool {
			return w.ID == c.rest && w.Model == c.model && w.Fixed == c.effort && w.Capped > 0
		})
	}
	var back []candidate
	var order []Weighed
	for _, c := range pl.capped {
		if !c.sameVendor(st.who, st.model) {
			continue
		}
		i := leftAt(c)
		if i < 0 {
			continue
		}
		w := pl.left[i]
		w.Unlisted, w.Capped, w.CapBack = false, 0, nil
		w.CapAmount, w.CapCount, w.CapUnit = 0, 0, ""
		pl.left = slices.Delete(slices.Clone(pl.left), i, i+1)
		back, order = append(back, c), append(order, w)
	}
	if len(back) > 0 {
		pl.capped = slices.DeleteFunc(slices.Clone(pl.capped), func(c candidate) bool { return c.sameVendor(st.who, st.model) })
		return append(back, cs...), planned{order: append(order, pl.order...), left: pl.left, held: pl.held, capped: pl.capped}, nil
	}
	var held []Weighed
	for _, c := range pl.held {
		if !c.sameVendor(st.who, st.model) {
			continue
		}
		if i := slices.IndexFunc(pl.left, func(w Weighed) bool { return w.ID == c.rest && w.NoCredits }); i >= 0 {
			held = append(held, pl.left[i])
		}
	}
	if len(held) > 0 {
		// only its own reset, should it spend one by itself, brings it back
		pl.held = slices.DeleteFunc(slices.Clone(pl.held), func(c candidate) bool { return !c.sameVendor(st.who, st.model) })
		return nil, planned{order: nil, left: pl.left, held: pl.held, capped: pl.capped}, held[:1]
	}
	return cs, pl, nil
}

// strictOnly leaves of cs, for a conversation kept strictly, the key or
// account cs[at] alone: first as it answered, then its other models a
// group has on it, which a rule may still pick between. Nobody else is
// asked, nor a reset spent elsewhere: its error is the agent's.
func strictOnly(cs []candidate, pl planned, at int) ([]candidate, planned) {
	who, model := cs[at].who(), cs[at].model
	keep := []int{at}
	for i, c := range cs {
		if i != at && c.sameVendor(who, model) {
			keep = append(keep, i)
		}
	}
	out := make([]candidate, 0, len(keep))
	order := make([]Weighed, 0, len(keep))
	for _, i := range keep {
		out = append(out, cs[i])
		if i < len(pl.order) {
			w := pl.order[i]
			w.Turn = false
			order = append(order, w)
		}
	}
	pl.order = order
	pl.held = slices.DeleteFunc(slices.Clone(pl.held), func(c candidate) bool { return !c.sameVendor(who, model) })
	return out, pl
}

// stickKey is the conversation a stick is kept for: scope's, by the
// conversation's id, and a Claude Code subagent's apart from its lead's.
// Claude Code sends its session's id with every request of the session,
// its subagents' too, which say only by x-claude-code-agent-id that they
// are one (subagentOf). Under the session's key, a subagent's answer took
// the lead's stick over: the lead's next turn was judged by the cache of
// the subagent's conversation, moved to another account as "no-cache" or
// "cold" or to wherever the subagent had gone, and the lead's whole
// conversation written to that account's cache again (slkassinthehood on
// X: two Pro accounts' 5 hours spent in ten minutes). The lead's key is
// the session's as before, so what is kept of it, and Codex's leadFirst,
// which reads a lead's stick by its thread id, are as they were.
func stickKey(scope string, in http.Header, body []byte) string {
	key := scope + "|" + affinityID(in, body)
	if sub := subOf(in); sub != "" {
		key += "|agent:" + sub
	}
	return key
}

// affine puts first whoever answered the conversation last, while its
// affinity says to keep it there. Its turn is counted in written, the body
// as the agent wrote it, which the turn's folder is read from too
// (dirOf): one parse of a held body serves both (factsOf), where counting
// it in the masked body parsed the conversation a second time, ~27 MB
// more for each 4 MB Codex request in flight. inOrder: the request is to
// a routing group, or a provider's keys or accounts, set to In order,
// whose order the user chose (backToFirst).
func affine(scope, mode string, rotate, spread, inOrder bool, in http.Header, from provider.Protocol, body, written []byte, cs []candidate, pl planned) ([]candidate, planned, *Affinity, string) {
	key := stickKey(scope, in, body)
	a := &Affinity{Mode: mode}
	a.Turn, a.Within = turnOf(from, written)
	sticks.Lock()
	st, had := stickOf(key)
	sticks.Unlock()
	if had && time.Since(st.at) > stickKeep {
		had = false
	}
	if had {
		a.Last, a.LastTurn, a.At, a.CacheRead, a.CacheWrite = st.rest, st.turn, st.at, st.cacheRead, st.cacheWrite
	}
	// the account and the model that answered: a group with Opus and
	// Sonnet both on one Claude account, a rule sending the turn to Opus,
	// had the turn's next request kept to the account and so to Sonnet,
	// its first member there; the account alone when the model is gone,
	// and behind a remote magpie the provider there alone (sameVendor)
	at := -1
	for _, same := range []func(candidate) bool{
		func(c candidate) bool { return c.who() == st.who && c.model == st.model && c.effort == st.effort },
		func(c candidate) bool { return c.who() == st.who && c.model == st.model },
		func(c candidate) bool { return c.sameVendor(st.who, st.model) },
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
	case mode == provider.AffinityStrict:
		// whatever it is — resting, all but used up, past its cap
		// (strictKeep) — it is the one asked (#1797)
		a.Why = "strict"
		if !a.Within && backToMain(pl.order, at) {
			// a fallback the user's own Strict keeps the conversation on,
			// the provider ready again: said, not overridden
			m := pl.order[0]
			a.Main = &m
		}
	case at < len(pl.order) && pl.order[at].Reserve > 0:
		// held back, its provider's accounts at once taken by those
		// before it (keepAtOnce): asked when they all failed, it keeps no
		// conversation, which goes back to them whatever it cached
		// (01huadalang on Discord: a third ZCode account in use at once
		// had the address banned). Strict, the user's own, keeps it
		a.Why = "reserve"
	case pl.order[at].Rest != nil:
		a.Why = "resting"
	case lastBehind(pl.order, at):
		// a Codex account gone onto its credits, which the user set to
		// spend only after everything else (creditsLast): whatever it
		// has cached, the conversation goes to one not on its credits,
		// within a turn too, as that is what the credits would pay for
		a.Why = "credits"
	case !a.Within && busyBehind(pl.order, at):
		// its plugin says the model is busy past the user's BusyLoad,
		// and one that isn't leads: the next turn goes there rather than
		// wait in the vendor's queue (axelburks on Discord). Within a
		// turn it stays, on the reasoning it sealed for the tool loop
		a.Why = "busy"
	case !a.Within && backToMain(pl.order, at):
		// a provider's fallback is its emergency path, not a place a
		// conversation stays: once one of the provider's own keys or
		// accounts is ready again (its rest over, its quota back) the
		// user's next turn goes to it, whatever the fallback has cached
		// (01huadalang on Discord). Within a turn it stays where it is:
		// the turn's tool loop runs on the reasoning the fallback sealed
		// for it, which the provider can't read, and the whole prompt is
		// sent afresh to the provider at the next turn all the same
		a.Why = "main"
	case !a.Within && inOrder && mode == "" && backToFirst(cs, pl.order, at, !strings.HasPrefix(scope, provider.GroupPrefix)) && (strings.HasPrefix(scope, provider.GroupPrefix) || cacheHolds(st, cs[at].p)):
		// a group set to In order tries its members in the order the user
		// put them in: a conversation that went to a later member while
		// the first couldn't take it (a 429, a 5xx, a rest) goes back to
		// the first at its next turn once that one is ready, whatever the
		// later one cached, as a provider's fallback gives it back (main).
		// Kept by its cache, a Claude Code session stayed on the later
		// member for good after one failure of the first (yyhhyyyyyy on
		// Discord: an API key first, a Claude subscription second, 8 of 10
		// requests on the subscription). Within a turn it stays, on the
		// reasoning the later member sealed; Stays: Session or Strict,
		// the user's own, keep it where it is. A provider's own keys or
		// accounts routed In order do the same (CryptoSarom on X); only
		// a cache that would keep it is overridden, so one gone cold or
		// never written is still told as that
		a.Why = "order"
	case !a.Within && !inOrder && !rotate && mode == "" && st.stoodIn && backFromStandIn(cs, pl.order, at):
		// a member listed after another, in a group routed Smart or any
		// way but In order (whose order is the list's: backToFirst) or
		// In turn (whose next turn goes round), that took the
		// conversation only as the ones ahead couldn't (a 429, a rest,
		// an allowance used up: standsInFor) gives it back at its next
		// turn once routing puts a member ahead of it first again,
		// whatever it cached: kept by its cache, a Codex session that
		// fell over to the group's backup in an outage stayed on the
		// backup after Codex was back (01huadalang on Discord). One
		// routing put first on its own merits, not standing in, keeps
		// its cache as before; within a turn it stays, and Stays:
		// Session or Strict, the user's own, keep it where it is
		a.Why = "back"
	case at > 0 && pl.order[at].Known && pl.order[at].Used >= provider.SpentShareOf(cs[at].p.Routing) && !pl.order[at].CreditsFirst:
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
	case st.cacheRead+st.cacheWrite < cacheWorth:
		// what it wrote counts with what it read: the cache a first turn
		// built is there for the next to read
		a.Why = "no-cache"
	case time.Since(st.at) > coldAfter(cs[at].p) && !st.hour.warm(time.Now()):
		// past what the vendor keeps unread (five minutes, longer at
		// the ChatGPT backend), kept still where the vendor said it
		// keeps enough for an hour (#2302)
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
	case "strict":
		a.Kept = true
		if at > 0 {
			weightedKept(cs[0], cs[at])
		}
		cs, pl = strictOnly(cs, pl, at)
	case "new-turn":
		if rotate {
			cs, pl = after(cs, pl, at)
		}
	case "first":
		switch {
		case rotate:
			cs, pl = leastHeld(scope, key, cs, pl, false)
		case spread && strings.Contains(key, "|agent:"):
			// a subagent goes with its session's account, not to another
			// as a session of its own
			cs, pl = withLead(key, cs, pl)
		case spread:
			cs, pl = leastHeld(scope, key, cs, pl, true)
		}
	}
	if !rotate {
		cs, pl = withSession(scope, key, sessionNamed(in), a, st, had, cs, pl)
	}
	return cs, pl, a, key
}

// backToMain says whether the one that answered last, order[at], is a
// provider's fallback while the provider's own model stands first, ready:
// restLast puts a resting one behind every ready one, so the first is the
// provider's own only when it can take the request. A routing group's
// members are never fallbacks, so its own affinity is as it was.
func backToMain(order []Weighed, at int) bool {
	return at > 0 && at < len(order) && order[at].Fallback && !order[0].Fallback && order[0].Rest == nil
}

// backToFirst says whether, in a group routed In order, the one ready
// first, order[0], is a member of the group ahead of the member that
// answered last, cs[at]: another member, not another key or account of
// the same one (a provider's keys, a group in the group, keep their own
// affinity). accounts: a provider's own keys or accounts routed In order,
// where the first is another key or account of the provider's own, not a
// fallback. restLast puts a resting one behind every ready one, so the
// first is ready only when it isn't resting.
func backToFirst(cs []candidate, order []Weighed, at int, accounts bool) bool {
	if at <= 0 || at >= len(order) || at >= len(cs) || order[0].Rest != nil || order[0].Aside {
		return false
	}
	if accounts {
		return !order[0].Fallback && !order[at].Fallback && cs[0].who() != cs[at].who()
	}
	member := func(i int) string {
		if len(order[i].Via) > 0 {
			return provider.GroupPrefix + order[i].Via[0]
		}
		return cs[i].p.ID + "/" + provider.WithMemberEffort(cs[i].model, cs[i].effort)
	}
	return member(0) != member(at)
}

// backFromStandIn says whether, in a group, the one ready first,
// order[0], is of a member the group lists ahead of the member of the
// one that answered last, cs[at]: not another key or account of the same
// member, nor of one listed after it. restLast puts a resting one behind
// every ready one, so the first is ready only when it isn't resting.
func backFromStandIn(cs []candidate, order []Weighed, at int) bool {
	if at <= 0 || at >= len(order) || at >= len(cs) || order[0].Rest != nil || order[0].Aside {
		return false
	}
	return cs[0].place > 0 && cs[0].place < cs[at].place
}

// standsInFor says whether c, which answered, did so standing in for a
// member of its group listed ahead of its own: one of the plan's (cs and
// order, as planned for the request) was resting, or all but used up, as
// it answered. A failure that has a try go on to the next rests the one
// that failed (restAfter), so a member ahead that failed this request is
// resting by then. A member routing put first while the ones ahead of it
// were ready and had room is no stand-in, whatever the order.
func standsInFor(c candidate, cs []candidate, order []Weighed) bool {
	if c.place <= 1 {
		return false
	}
	for _, o := range cs {
		if o.place <= 0 || o.place >= c.place {
			continue
		}
		if _, resting := restingOf(o); resting {
			return true
		}
		for _, w := range order {
			if w.ID == o.rest && w.Model == o.model && w.Fixed == o.effort {
				if w.Rest != nil || w.Known && w.Used >= provider.SpentShareOf(o.p.Routing) && !w.CreditsFirst {
					return true
				}
				break
			}
		}
	}
	return false
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
// stands. A conversation whose first request is still out counts where it
// was sent (sticks.opening, under key until opened ends it): many started
// at once all went to the one holding the fewest before any of them was
// answered (#1342: 24 requests at once over 5 accounts). A fallback is
// none of those while the provider's own leads (standsIn): held by nobody,
// it was the one held on fewest.
//
// ready is for a conversation kept for the whole session (spreadsSessions):
// it goes only to one routing would put a new conversation on as well —
// not a fallback while the provider's own model leads, nor one used past
// lowShare, which Smart puts behind for new conversations — and routing's
// order is otherwise as it was, its turn marks too.
func leastHeld(scope, key string, cs []candidate, pl planned, ready bool) ([]candidate, planned) {
	if len(cs) < 2 || len(pl.order) < len(cs) {
		return cs, pl
	}
	if strings.HasPrefix(scope, provider.GroupPrefix) {
		if g, ms, ok := provider.FindGroup(scope); ok {
			for _, m := range ms {
				// a group in the group, or a provider routed its own way:
				// its own routing orders its models or keys (#576)
				if len(m.Path) > 1 || ownRouting(m.Provider, g) {
					return cs, pl
				}
			}
		}
	}
	now := time.Now()
	held := map[string]int{}
	sticks.Lock()
	defer sticks.Unlock()
	for _, m := range []map[string]stick{sticks.m, sticks.opening} {
		for k, st := range m {
			if strings.HasPrefix(k, scope+"|") && now.Sub(st.at) <= heldFor && !(ready && strings.Contains(k, "|agent:")) {
				held[st.who+"/"+provider.WithMemberEffort(st.model, st.effort)]++
			}
		}
	}
	defer func() {
		// counted from now until it is answered or ends
		sticks.opening[key] = stick{rest: cs[0].rest, who: cs[0].who(), model: cs[0].model, effort: cs[0].effort, at: now}
	}()
	if len(held) == 0 {
		return cs, pl
	}
	seat := func(c candidate) string { return c.who() + "/" + provider.WithMemberEffort(c.model, c.effort) }
	best := -1
	for i, c := range cs {
		if pl.order[i].Rest != nil || pl.order[i].Aside || standsIn(pl.order, i) || lastBehind(pl.order, i) || busyBehind(pl.order, i) || ready && i > 0 && !readyForNew(pl.order, i) {
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
	if !ready {
		for j := range pl.order {
			pl.order[j].Turn = j == 0
		}
	}
	return cs, pl
}

// readyForNew says whether order[i], behind order[0], is one routing would
// put a new conversation on: not resting, nor a key for another protocol,
// nor a fallback while the provider's own model leads, nor used past
// lowShare unless it spends its credits first (Weighed.CreditsFirst) or
// is that low on its week alone, which Smart and Use before expiry keep
// with those that have room (Weighed.WeekLow).
func readyForNew(order []Weighed, i int) bool {
	w := order[i]
	return w.Rest == nil && !w.Aside && !standsIn(order, i) && !(w.Known && w.Used >= lowShare && !w.CreditsFirst && !(w.WeekLow && w.Used < usedShare)) && !lastBehind(order, i) && !busyBehind(order, i)
}

// standsIn says whether order[i] is a provider's fallback while one of the
// provider's own keys or accounts leads, ready: restLast puts a resting
// one behind every ready one, so a fallback leads only when none of the
// provider's own can take the request. A fallback is the provider's
// emergency path, never a seat a round or a spread goes to: In turn's
// next turn (after) and its new conversation (leastHeld) took it for one
// more key, and sent every other turn to it, with the provider ready
// (01huadalang on Discord, 10-31).
func standsIn(order []Weighed, i int) bool {
	return i < len(order) && order[i].Fallback && !order[0].Fallback
}

// busyBehind says whether order[i] is a model its plugin says is busy
// (Weighed.Busy) while one that isn't leads: restLast put it behind the
// ready ones, and spreading conversations or taking turns never brings
// it forward past them.
func busyBehind(order []Weighed, i int) bool {
	return i > 0 && i < len(order) && order[i].Busy > 0 && order[0].Busy == 0
}

// lastBehind says whether order[i] is a Codex account on its credits set
// to spend them last (Weighed.CreditsLast) while one that isn't leads:
// restLast put it behind every such one, and spreading a conversation or
// taking a turn never brings it forward past them.
func lastBehind(order []Weighed, i int) bool {
	return i > 0 && i < len(order) && order[i].CreditsLast && !order[0].CreditsLast
}

// withLead puts first, for a subagent's conversation nobody has answered
// yet (a key stickKey gave "|agent:"), the key or account its session's
// lead is on, while that one is readyForNew: under spreadsSessions the
// session keeps its account, subagents included, rather than a subagent
// counted as a session of its own and sent to another.
func withLead(key string, cs []candidate, pl planned) ([]candidate, planned) {
	lead, _, _ := strings.Cut(key, "|agent:")
	sticks.Lock()
	st, ok := stickOf(lead)
	sticks.Unlock()
	if !ok || time.Since(st.at) > heldFor || len(pl.order) < len(cs) {
		return cs, pl
	}
	for i := 1; i < len(cs); i++ {
		if cs[i].who() == st.who && cs[i].model == st.model && cs[i].effort == st.effort && readyForNew(pl.order, i) {
			cs = append(append([]candidate{cs[i]}, cs[:i]...), cs[i+1:]...)
			pl.order = append(append([]Weighed{pl.order[i]}, pl.order[:i]...), pl.order[i+1:]...)
			break
		}
	}
	return cs, pl
}

// withSession puts first, for a conversation of a session whose main
// conversation is with another model, the key or account that main
// conversation is on (#5655, jerrychoi; iamnil on Discord): Claude
// Desktop's own Sonnet calls beside an Opus chat (desktop-beside), Claude
// Code's auto mode classifier, a title. A model's conversation is kept
// apart from the session's other models' (scope), so such a call was
// routed as a session of its own: put by leastHeld, or by Smart's order
// once its cache was cold, on the account the fewest others were on, it
// ran the session on two accounts at once, and the second one's 5 hours
// drained on calls the user never made there. One session is billed to
// one account, as a user who picked one in the menu expects, and as the
// vendor sees one client.
//
// The session's main conversation is its largest under another model's
// scope (what its vendor read and wrote of it), answered within heldFor,
// by the session's own id: a subagent (withLead) and a conversation known
// only by its body's hash have none (sessionNamed), since two unrelated
// conversations that open alike share a hash. A main conversation with
// less than cacheWorth kept has no cache to stay for, and goes where its
// own next turn is sent: it leads nothing. It leads only while larger than this
// one's, so the two never take turns following each other. It is
// followed while this conversation isn't held where it is otherwise:
// Stays: Strict, mid-turn on the reasoning one account sealed, or a
// rest, a reserve, credits a fallback routing put first for its own
// reason; and only to one routing would put a new conversation on
// (readyForNew), as the model routing put first. A conversation it moves
// off its own cache writes that once, where the session goes on reading
// it.
func withSession(scope, key string, named bool, a *Affinity, own stick, had bool, cs []candidate, pl planned) ([]candidate, planned) {
	switch a.Why {
	case "first", "gone", "cold", "no-cache", "cache", "session":
	default:
		return cs, pl
	}
	if !named || a.Within && a.Kept || len(cs) < 2 || len(pl.order) < len(cs) || strings.Contains(key, "|agent:") {
		return cs, pl
	}
	id := strings.TrimPrefix(key, scope+"|")
	if id == key || id == "" {
		return cs, pl
	}
	size := 0
	if had {
		size = own.cacheRead + own.cacheWrite
	}
	now := time.Now()
	sticks.Lock()
	defer sticks.Unlock()
	var lead stick
	found := false
	for k, st := range sticks.m {
		if k == key || !strings.HasSuffix(k, "|"+id) || strings.Contains(k, "|agent:") || now.Sub(st.at) > heldFor {
			continue
		}
		if n := st.cacheRead + st.cacheWrite; n >= cacheWorth && n > size && (!found || n > lead.cacheRead+lead.cacheWrite) {
			lead, found = st, true
		}
	}
	if !found {
		return cs, pl
	}
	// of the model routing put first: in a group, another member on the
	// lead's account is another model, which following never picks
	i := slices.IndexFunc(cs, func(c candidate) bool {
		return c.who() == lead.who && c.model == cs[0].model && c.effort == cs[0].effort
	})
	if i < 0 || i > 0 && !readyForNew(pl.order, i) {
		return cs, pl
	}
	if i > 0 {
		cs = append(append([]candidate{cs[i]}, cs[:i]...), cs[i+1:]...)
		pl.order = append(append([]Weighed{pl.order[i]}, pl.order[:i]...), pl.order[i+1:]...)
		for j := range pl.order {
			pl.order[j].Turn = false
		}
		if _, opening := sticks.opening[key]; opening {
			sticks.opening[key] = stick{rest: cs[0].rest, who: cs[0].who(), model: cs[0].model, effort: cs[0].effort, at: now}
		}
	}
	if a.Kept && i == 0 || !a.Kept && i == 0 && pl.order[0].Rest != nil {
		return cs, pl // where it is already
	}
	a.Kept, a.Why, a.LeadModel = false, "lead", lead.model
	return cs, pl
}

// sessionNamed reports whether the request names its session in a header
// (sessionHeaders or X-Magpie-Session), where affinityID would otherwise
// be the hash of how the conversation opens.
func sessionNamed(in http.Header) bool {
	for _, h := range sessionHeaders {
		if strings.TrimSpace(in.Get(h)) != "" {
			return true
		}
	}
	return strings.TrimSpace(in.Get(SessionHeader)) != ""
}

// spreadsSessions says whether new conversations kept for the whole
// session (Stays: Session or Strict) go to the key, account or member the
// fewest others are on (leastHeld), under a routing that picks by what
// each has left: Smart, Least used first, Use before expiry. Several sessions
// started side by side then each keep an account of their own until its
// allowance runs out, as a user picking the mode with several accounts
// expects (connor_yang on Discord: three sessions on two of three
// accounts). In order, By weight, Free first, Speed first, Cheapest, Manual
// and a plugin's routing pick for their own reasons, and keep their order;
// In turn spreads on its own.
func spreadsSessions(mode, routing string) bool {
	if mode != provider.AffinitySession && mode != provider.AffinityStrict {
		return false
	}
	switch routingOf(routing) {
	case "", provider.LeastUsed, provider.Pace:
		return true
	}
	return false
}

// after puts first the one after cs[at], round from the end, that isn't
// resting, nor a fallback while the provider's own leads (standsIn).
func after(cs []candidate, pl planned, at int) ([]candidate, planned) {
	for d := 1; d < len(cs); d++ {
		i := (at + d) % len(cs)
		if pl.order[i].Rest != nil || pl.order[i].Aside || standsIn(pl.order, i) || lastBehind(pl.order, i) || busyBehind(pl.order, i) {
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
// the vendor's cache and wrote to it doing so — u. A vendor that keeps a
// prompt cached without reporting it — OpenAI's automatic prefix cache,
// Gemini — says neither of a turn that read none: what its cache holds
// then is the prompt that turn was sent, and that stands for the write, so
// the next turn stays to read it rather than moving as no-cache and
// building it afresh elsewhere. A reply that reports no usage at all
// (Input 0) counts as no cache, as it did.
func answered(key string, c candidate, turn int, u Usage) {
	answeredStanding(key, c, turn, u, false)
}

// answeredStanding is answered, with whether c stood in for a member
// listed ahead of its own (standsInFor).
func answeredStanding(key string, c candidate, turn int, u Usage, stoodIn bool) {
	cacheRead, cacheWrite := u.CacheRead, u.CacheWrite
	if cacheRead == 0 && cacheWrite == 0 {
		cacheWrite = u.Input
	}
	now := time.Now()
	sticks.Lock()
	var hour hourCache
	if st, ok := sticks.m[key]; ok && st.who == c.who() && st.model == c.model {
		hour = st.hour // a cache is the account's, of the model
		// a stand-in that answers the conversation on is a stand-in
		// still: the member it stood in for, back and put behind it
		// meanwhile by routing, is given it once routing puts it first
		stoodIn = stoodIn || st.stoodIn
	}
	hour = hour.after(u, now)
	sticks.m[key] = stick{rest: c.rest, who: c.who(), model: c.model, effort: c.effort, turn: turn, at: now, cacheRead: cacheRead, cacheWrite: cacheWrite, hour: hour, stoodIn: stoodIn}
	delete(sticks.opening, key)
	if len(sticks.m) > 4096 {
		for k, st := range sticks.m {
			if now.Sub(st.at) > stickKeep {
				delete(sticks.m, k)
			}
		}
	}
	sticks.Unlock()
	noteSticks()
}

// unanswered forgets that c answered a conversation, when its reply to it
// broke off (#733): the agent's retry goes by routing again, not back to
// the one that just failed it. The account alone is enough: affine's
// widest match keeps a conversation on an account whose answerer's model
// has since left the group, so a stick that only matched by its model
// would leave the next request kept on the account that just broke off.
// opened ends a request's part in leastHeld's count of conversations being
// opened, however it ended.
func opened(key string) {
	sticks.Lock()
	delete(sticks.opening, key)
	sticks.Unlock()
}

func unanswered(key string, c candidate) {
	sticks.Lock()
	st, ok := stickOf(key)
	ok = ok && c.sameVendor(st.who, st.model)
	if ok {
		delete(sticks.m, key)
	}
	sticks.Unlock()
	if ok {
		noteSticks()
	}
}

// foreignReasoning is how a vendor refuses reasoning another account (or
// organization, or vendor) sealed: OpenAI's "The encrypted content for
// item rs_… could not be verified", invalid_encrypted_content, and for a
// web search another ChatGPT account ran "Encrypted content could not be
// decrypted or parsed." (#3887); xAI's
// (Grok's API and a SuperGrok account alike) "Could not decrypt the
// provided encrypted_content. Ensure the value is the unmodified
// encrypted_content from a previous response."; Azure's (through a relay
// in front of a deployment, snowshadow) "invalid encrypted summary",
// which names no item and spells it with spaces; xAI's compaction-blob
// refusal, as a key relay that proxies Grok hands it back for the
// reasoning item Codex writes with "content": null beside its
// encrypted_content (zminds #4412) — "Could not decode the compaction
// blob", which names no item and says nothing of encrypted content,
// where the #3392 shaping (xaiBody's) never runs because the relay's
// host isn't api.x.ai.
var foreignReasoning = lazyre.New(`(?i)invalid_encrypted_content|invalid[ _]encrypted[ _](summary|content)|encrypted[ _]content.{0,80}could not be (verified|decrypted)|could not (decrypt|verify).{0,40}encrypted[ _]content|could not (decode|decrypt).{0,20}compaction[ _]blob`)

// refusedSeals remembers, per conversation and the key or account that
// refused it, what another one sealed: its reasoning, and the sealed copy
// output token replay keeps of each message, call and web search (#3887).
// Codex hands every earlier turn's items back on each request, so once a
// conversation has moved, what was sealed before it moved is left out of
// its later requests there up front, rather than refused and asked again
// each turn. What the one it moved to sealed itself still goes along, for
// its cache and its train of thought.
var refusedSeals = struct {
	sync.Mutex
	m map[string]sealsRefused // scope|conversation|who → what it refused
}{m: map[string]sealsRefused{}}

type sealsRefused struct {
	at    time.Time
	seals map[[sha256.Size]byte]bool
}

// sealedItem is what's read of an input item to tell what it has sealed.
type sealedItem struct {
	Type string `json:"type"`
	Enc  string `json:"encrypted_content"`
}

// sealedKinds are the input items a vendor seals for itself: its
// reasoning, and the compaction a conversation's earlier turns were
// folded into (OpenAI's, when Codex compacts on its own models; magpie's
// own is text by the time a request is sent). Refused, they go whole;
// any other item loses its sealed copy alone (withoutItemSeal).
var sealedKinds = map[string]bool{"reasoning": true, "compaction": true, "compaction_summary": true}

var compactionKinds = []string{"compaction", "compaction_summary"}

// seals are what is sealed (encrypted_content) in a Responses request's
// input, item by item.
func seals(body []byte) [][sha256.Size]byte {
	var q struct {
		Input []sealedItem `json:"input"`
	}
	if decodeJSON(body, &q) != nil {
		return nil
	}
	var out [][sha256.Size]byte
	for _, it := range q.Input {
		if it.Enc != "" {
			out = append(out, sha256.Sum256([]byte(it.Enc)))
		}
	}
	return out
}

// refused notes, in the conversation key names, that who turned away the
// seals taken out of sent, the request it refused, to make left, the one
// asked in its place.
func refused(key, who string, sent, left []byte) {
	kept := map[[sha256.Size]byte]bool{}
	for _, s := range seals(left) {
		kept[s] = true
	}
	noteRefused(key, who, slices.DeleteFunc(seals(sent), func(s [sha256.Size]byte) bool { return kept[s] }))
}

// noteRefused notes that who turned away the sealed items of these hashes
// in the conversation key.
func noteRefused(key, who string, ss [][sha256.Size]byte) {
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

// withoutRefused takes out of a Responses request what who already
// refused in this conversation: sealed reasoning and compactions whole,
// any other item's sealed copy alone; the rest stays as it is.
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
	if decodeJSONView(body, &q) != nil {
		return nil, false
	}
	items, isList := inputList(q["input"])
	if !isList {
		return nil, false
	}
	kept, changed := items[:0:0], false
	for _, it := range items {
		var t sealedItem
		if decodeJSONView(it, &t) == nil && t.Enc != "" && r.seals[sha256.Sum256([]byte(t.Enc))] {
			if sealedKinds[t.Type] {
				changed = true
				continue
			}
			if b, ok := withoutItemSeal(it); ok {
				it, changed = b, true
			}
		}
		kept = append(kept, it)
	}
	if !changed {
		return nil, false
	}
	q["input"], _ = json.Marshal(kept)
	b, err := marshalJSON(q)
	return b, err == nil
}

// withoutSeals takes out of a Responses request's input what is sealed,
// but a compaction, for one that can't read what another account sealed:
// the sealed reasoning, the model's private notes to itself, and the
// sealed copy output token replay keeps of each item answered, a message,
// a call or a web search (#3887, hheei: the reasoning alone went, and the
// web search another ChatGPT account ran was refused again). What was
// said and done stays.
func withoutSeals(body []byte) ([]byte, bool) {
	var q map[string]json.RawMessage
	if decodeJSONView(body, &q) != nil {
		return nil, false
	}
	items, isList := inputList(q["input"])
	if !isList {
		return nil, false
	}
	kept, changed := items[:0:0], false
	for _, it := range items {
		var t struct {
			Type string `json:"type"`
		}
		if decodeJSONView(it, &t) == nil && t.Type == "reasoning" {
			changed = true
			continue
		}
		if b, ok := withoutItemSeal(it); ok {
			it, changed = b, true
		}
		kept = append(kept, it)
	}
	if !changed {
		return nil, false
	}
	q["input"], _ = json.Marshal(kept)
	b, err := marshalJSON(q)
	return b, err == nil
}

// withoutItemSeal is an input item without its sealed copy
// (encrypted_content), every other byte as it came; false for one with
// nothing sealed, and for reasoning and a compaction (keepsSeal), whose
// seal is all there is of them.
func withoutItemSeal(it []byte) ([]byte, bool) {
	if keepsSeal[string(memberOrNil(it, "type"))] {
		return nil, false
	}
	if s, ok := stringBytes(memberOrNil(it, "encrypted_content")); !ok || len(s) == 0 {
		return nil, false
	}
	return withoutTopMember(it, "encrypted_content"), true
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
	if decodeJSON(body, &q) != nil {
		return nil, false
	}
	for _, it := range q.Input {
		if slices.Contains(compactionKinds, it.Type) && it.Enc != "" {
			return withoutKinds(body, compactionKinds...)
		}
	}
	return nil, false
}

// overflowLearned remembers, per conversation and the provider's model
// that refused it, how large the request was when the model answered it
// too long (#3453: a rule sent every request of a conversation to a
// member whose bridge refused it as too long; another member answered,
// and the next request tried the first all over again — twice within one
// turn). An overflow says nothing is wrong with the account — the
// request was too long for the model — so nobody rests, and the memory
// is the conversation's, not the member's: another conversation, or a
// key or account of the model on it, goes to it as before. The next
// request of the same conversation, grown or unchanged, is refused the
// same way, the fallback each time spending the member's answer and the
// agent's wait.
var overflowLearned = struct {
	sync.Mutex
	m map[string]map[string]refusedLong // conversation → provider\x00model → what it refused
}{m: map[string]map[string]refusedLong{}}

// refusedLong is a request a conversation's member refused as too long:
// its own bytes as they came, at the time it was refused. A conversation
// only grows — so what is remembered holds — until its agent compacts
// it, and one smaller than what was refused may hold now — so what is
// remembered yields.
type refusedLong struct {
	at    time.Time
	bytes int
}

// noteOverflow remembers that c's model refused this conversation as too
// long at size bytes.
func noteOverflow(key string, c candidate, size int) {
	now := time.Now()
	overflowLearned.Lock()
	defer overflowLearned.Unlock()
	m := overflowLearned.m[key]
	if m == nil {
		m = map[string]refusedLong{}
		overflowLearned.m[key] = m
	}
	m[c.p.ID+"\x00"+c.model] = refusedLong{at: now, bytes: size}
	if len(overflowLearned.m) > 4096 {
		for k, m := range overflowLearned.m {
			old := true
			for _, r := range m {
				if now.Sub(r.at) <= stickKeep {
					old = false
				}
			}
			if old {
				delete(overflowLearned.m, k)
			}
		}
	}
}

// forgetOverflow drops what was remembered of c's model refusing this
// conversation: it has just answered it, whatever its size — the bridge
// that refused it a moment ago may take it now.
func forgetOverflow(key string, c candidate) {
	overflowLearned.Lock()
	defer overflowLearned.Unlock()
	m := overflowLearned.m[key]
	if m == nil {
		return
	}
	delete(m, c.p.ID+"\x00"+c.model)
	if len(m) == 0 {
		delete(overflowLearned.m, key)
	}
}

// behindOverflow puts, of cs, the members whose models refused this very
// conversation as too long at no more than size — what it is now, or has
// grown to since — behind the others, so the member that answered last
// is asked first instead (#3453). The rule that sends the turn to one of
// them has been heard (ruleOver has run); the member is still asked when
// every other member has failed: it is put behind, not out, and when it
// answers the memory of its refusal is dropped. A request smaller than
// what was refused — the agent has compacted the conversation — leaves
// the member where it was.
func behindOverflow(key string, cs []candidate, pl planned, size int) ([]candidate, planned) {
	if len(cs) < 2 || len(pl.order) != len(cs) {
		return cs, pl
	}
	// the conversation's map is read under the lock: another request of
	// the same conversation may be noting or forgetting in it at once
	overflowLearned.Lock()
	noted := overflowLearned.m[key]
	var behind, ready []candidate
	var behindW, readyW []Weighed
	for i, c := range cs {
		if r, ok := noted[c.p.ID+"\x00"+c.model]; ok && size >= r.bytes {
			behind, behindW = append(behind, c), append(behindW, pl.order[i])
			continue
		}
		ready, readyW = append(ready, c), append(readyW, pl.order[i])
	}
	overflowLearned.Unlock()
	if len(behind) == 0 || len(ready) == 0 {
		return cs, pl
	}
	pl.order = append(readyW, behindW...)
	return append(ready, behind...), pl
}

// withoutKinds is a Responses request without its input items of these
// types.
func withoutKinds(body []byte, kinds ...string) ([]byte, bool) {
	var q map[string]json.RawMessage
	if decodeJSONView(body, &q) != nil {
		return nil, false
	}
	items, isList := inputList(q["input"])
	if !isList {
		return nil, false
	}
	kept := items[:0:0]
	for _, it := range items {
		var t struct {
			Type string `json:"type"`
		}
		if decodeJSONView(it, &t) == nil && slices.Contains(kinds, t.Type) {
			continue
		}
		kept = append(kept, it)
	}
	if len(kept) == len(items) {
		return nil, false
	}
	q["input"], _ = json.Marshal(kept)
	b, err := marshalJSON(q)
	return b, err == nil
}

// keptAgain says whether c, having failed through hw, is asked once more
// after a moment before the request moves on, and how long that moment is:
// c is the key or account (and model) the conversation was kept on for
// what its vendor cached of it (aff.Kept: Stays Auto with a warm cache,
// Session, or within a turn), not asked again so already (asked), and the
// failure is one that may be gone a moment later (busy: overloaded, a 5xx
// that says nothing of quota, timed out) and not a rate limit, which rests
// and moves as before. Moved at once, the next one is sent the whole
// conversation uncached, and within a turn may refuse the reasoning the
// first sealed; a moment's wait costs far less. Strict needs none of it:
// its one is the last left, asked again as the last one is.
func keptAgain(aff *Affinity, c candidate, hw *holdWriter, asked bool) (time.Duration, bool) {
	if asked || aff == nil || !aff.Kept || aff.Why == "strict" || c.rest != aff.Last {
		return 0, false
	}
	if !hw.failed() || hw.turnedAway || hw.refused || hw.code() == http.StatusTooManyRequests || !busy(hw.code(), hw.errBody()) {
		return 0, false
	}
	return passing(hw.code(), hw.header, hw.errBody(), 0)
}

// cacheHolds says whether what the conversation has cached would keep it
// where it is: enough written or read (cacheWorth), and not gone cold
// (coldAfter, or kept for an hour: #2302). The "no-cache" and "cold"
// cases of affine read the same.
func cacheHolds(st stick, p provider.Provider) bool {
	return st.cacheRead+st.cacheWrite >= cacheWorth && (time.Since(st.at) <= coldAfter(p) || st.hour.warm(time.Now()))
}
