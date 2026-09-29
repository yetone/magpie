package gateway

// Fallback: a provider can name models to use when it can't take a request
// — a coding plan out of quota, a rate limit, an overloaded or failing
// vendor. The request goes to the next one only while none of the reply has
// been sent, so an agent sees one clean answer from whoever gave it, never a
// half from each. A provider that just failed that way waits at the back of
// the line for a minute, rather than costing every request a doomed try.
//
// A provider with several keys on is several candidates, one per key, in
// order, and so is a subscription with several accounts on: when one
// account runs out, the next account of the same provider takes the
// request before any fallback model does. A key can be made for one
// protocol only, as some relays hand them out; see perKey.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

const fallbackCooldown = time.Minute

type candidate struct {
	p     provider.Provider
	model string
	rest  string // what rests after a failure: the provider, or one of its keys
	// effort is the reasoning the group's member it is of is fixed at
	// ("provider/model:low"); "" for one that follows the agent or the group
	effort string
}

// label names a candidate in a call's record: the provider, and the key
// when it has several on.
func (c candidate) label() string {
	if c.rest == c.p.ID {
		return c.p.ID
	}
	if c.p.Account != nil {
		return c.p.ID + " (" + c.p.Account.User + ")"
	}
	if c.p.KeyName != "" {
		return c.p.ID + " (" + c.p.KeyName + ")"
	}
	return c.p.ID + " (" + provider.Mask(c.p.Key) + ")"
}

// restKey is what a candidate rests by: an account by its user, whether
// or not the agent is signed in to it — magpie can sign Codex in to the
// next account when the one it is on is out, and the one out stays out.
func (c candidate) restKey() string {
	if a := c.p.Account; a != nil && a.User != "" {
		return c.p.ID + "@" + strings.ToLower(a.User)
	}
	return c.rest
}

// seat is the candidate as one of a group's members has it: its key or
// account, the model, and the effort the member is fixed at.
func (c candidate) seat() string {
	return provider.WithMemberEffort(c.rest+"/"+c.model, c.effort)
}

func (c candidate) isOpenRouterFree() bool {
	return c.p.Preset == "openrouter" && strings.HasSuffix(c.model, ":free")
}

// restID is a free OpenRouter model's own rest key. A rate limit on that
// model is its free-tier limit, while an account-level rest stays on restKey.
func (c candidate) restID() string {
	if c.isOpenRouterFree() {
		return c.restKey() + "/" + c.model
	}
	return c.restKey()
}

// who is the key or account itself, however many the provider has on: a
// provider's one key rests as the provider, and as itself once another
// is added, and a conversation it answered stays with it all the same.
func (c candidate) who() string {
	if c.p.Account == nil && c.p.Key != "" {
		return c.p.ID + "#" + provider.KeyID(c.p.Key)
	}
	return c.rest
}

// perKey is a provider once per key it has on, in order — or, for a
// signed-in agent, once per account it has on, its own first. A key made
// for one protocol only serves on that one's endpoint, and the keys that
// suit the request go first: the Anthropic key for a Claude model, the
// OpenAI one for a GPT model, else the key that speaks what the agent
// spoke, so nothing is translated that needn't be.
func perKey(p provider.Provider, model string, from provider.Protocol) []candidate {
	out, aside, _ := perKeyOf(p, model, from)
	return append(out, aside...)
}

// perKeyOf is perKey, split: the keys routing goes over, those made for
// another protocol, and the accounts or keys it left out as not listing
// the model. Keys made for different protocols are not one pool: routing
// weighs, rotates and keeps conversations over those made for the
// protocol that suits the request best, and the others are tried only
// after them, in the order they suit it.
func perKeyOf(p provider.Provider, model string, from provider.Protocol) (out, aside, left []candidate) {
	if p.Account != nil {
		all := []candidate{{p: p, model: model, rest: p.ID}}
		for _, q := range p.AlsoOn() {
			all = append(all, candidate{p: q, model: model, rest: p.ID + "@" + q.Account.User})
		}
		// an account whose plan lacks the model (a Free one behind a Plus)
		// would only answer 400; it is tried only when none lists it
		for _, c := range all {
			if c.p.Account.Lists(model) {
				out = append(out, c)
			} else {
				left = append(left, c)
			}
		}
		if len(out) == 0 {
			return all, nil, nil
		}
		return out, nil, left
	}
	keys := p.KeysOn()
	var unlisted []candidate
	for _, k := range keys {
		q := p.WithKey(k)
		if len(q.Speaks()) == 0 {
			continue // made for a protocol this provider has no endpoint for
		}
		rest := p.ID
		if len(keys) > 1 {
			rest += "#" + provider.KeyID(k.Key)
		}
		if !p.Serves(k, model) {
			// the vendor lists the model to another key only
			unlisted = append(unlisted, candidate{p: q, model: model, rest: rest})
			continue
		}
		out = append(out, candidate{p: q, model: model, rest: rest})
	}
	if len(out) == 0 {
		out, unlisted = unlisted, nil // no key lists it: try them all the same
	}
	if len(out) == 0 {
		return []candidate{{p: p, model: model, rest: p.ID}}, nil, nil
	}
	sort.SliceStable(out, func(i, j int) bool { return keyFit(out[i].p, model, from) < keyFit(out[j].p, model, from) })
	pool := out[:0:0]
	for _, c := range out {
		if c.p.KeyProtocol == out[0].p.KeyProtocol {
			pool = append(pool, c)
		} else {
			aside = append(aside, c)
		}
	}
	return pool, aside, unlisted
}

// keyFit ranks how well a key suits a request, best first: 0 fits, 1 needs
// the request translated, 2 is made for another vendor's models.
func keyFit(q provider.Provider, model string, from provider.Protocol) int {
	if q.KeyProtocol == "" {
		return 0
	}
	switch modelFamily(model) {
	case provider.Anthropic:
		if q.KeyProtocol == provider.Anthropic {
			return 0
		}
		return 2
	case provider.Chat:
		if q.KeyProtocol != provider.Anthropic {
			return 0
		}
		return 2
	}
	if q.Base(from) != "" {
		return 0
	}
	return 1
}

// modelFamily is the protocol a model is at home in, when its name says:
// Anthropic for Claude, Chat (standing for OpenAI's) for GPT and the o-series.
func modelFamily(model string) provider.Protocol {
	m := strings.ToLower(model)
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	switch {
	case strings.HasPrefix(m, "claude"):
		return provider.Anthropic
	case strings.HasPrefix(m, "gpt-"), strings.Contains(m, "codex"), len(m) > 1 && m[0] == 'o' && m[1] >= '1' && m[1] <= '9':
		return provider.Chat
	}
	return ""
}

// candidates is the primary and then its fallbacks, each provider's keys
// or accounts as its routing orders them, those resting after a recent
// failure moved behind the rest.
func (s *Server) candidates(p provider.Provider, model string, from provider.Protocol) []candidate {
	out, _ := s.plan(p, model, from)
	return out
}

// plan is candidates, and for the routing trace what each stood where it
// did by, and those left out.
func (s *Server) plan(p provider.Provider, model string, from provider.Protocol) ([]candidate, planned) {
	var pl planned
	add := func(q provider.Provider, m string, fallback bool) []candidate {
		cs, aside, left := perKeyOf(q, m, from)
		cs, wg := weigh(q, cs, m, from)
		for i, c := range cs {
			w := weighed(c, q, wg, fallback, from)
			w.Turn = i == 0 && q.Routing == provider.Rotate && len(cs) > 1
			pl.order = append(pl.order, w)
		}
		pl.order = append(pl.order, asideOf(aside, q, fallback, from)...)
		cs = append(cs, aside...)
		for _, c := range left {
			w := weighed(c, q, weighing{}, fallback, from)
			w.Unlisted = true
			pl.left = append(pl.left, w)
		}
		return cs
	}
	out := add(p, model, false)
	seen := map[string]bool{p.ID + "/" + model: true}
	for _, id := range p.Fallback {
		fp, fm, ok := provider.Resolve(id)
		if !ok || seen[fp.ID+"/"+fm] {
			continue
		}
		seen[fp.ID+"/"+fm] = true
		out = append(out, add(fp, fm, true)...)
	}
	return restLast(out, pl)
}

// planGroup is plan for a routing group: every member's keys or accounts
// weighed together as the group's routing says — in order, member by
// member, each as its own provider's routing orders it; else all as one,
// so a subscription whose allowance renews soonest goes first whichever
// provider it is of. A group in the group is planned the same way by its
// own routing, in its place when the group's is in order. A member's
// fallbacks are not the group's.
func (s *Server) planGroup(g provider.Group, ms []provider.Member, from provider.Protocol) ([]candidate, planned) {
	var pl planned
	var asides []candidate
	var wAsides []Weighed
	out := planLevel(g, ms, 0, from, &pl, &asides, &wAsides)
	out, pl.order = append(out, asides...), append(pl.order, wAsides...)
	if len(out) == 0 {
		return nil, pl
	}
	return restLast(out, pl)
}

// planLevel orders the models ms of g, a group depth groups down from the
// one asked for, adding to pl's order as it goes: those set aside and
// those unlisted it gathers for planGroup to put last.
func planLevel(g provider.Group, ms []provider.Member, depth int, from provider.Protocol, pl *planned, asides *[]candidate, wAsides *[]Weighed) []candidate {
	keys := func(m provider.Member) []candidate {
		cs, aside, left := perKeyOf(m.Provider, m.Model, from)
		// the effort the member is fixed at goes with each of its keys:
		// the same model at another effort is another member's
		for _, l := range [][]candidate{cs, aside, left} {
			for i := range l {
				l[i].effort = m.Effort
			}
		}
		*asides = append(*asides, aside...)
		for _, w := range asideOf(aside, m.Provider, false, from) {
			w.Via = m.Groups()
			*wAsides = append(*wAsides, w)
		}
		for _, c := range left {
			w := weighed(c, m.Provider, weighing{}, false, from)
			w.Unlisted, w.Via = true, m.Groups()
			pl.left = append(pl.left, w)
		}
		return cs
	}
	if g.Routing != provider.Ordered {
		of := map[string]provider.Member{} // candidate → its model
		var all []candidate
		for _, m := range ms {
			cs := keys(m)
			for _, c := range cs {
				of[c.seat()] = m
			}
			all = append(all, cs...)
		}
		cs, wg := weigh(provider.Provider{ID: provider.GroupPrefix + g.ID, Routing: g.Routing}, all, "", from)
		for i, c := range cs {
			m := of[c.seat()]
			w := weighed(c, m.Provider, wg, false, from)
			w.Routing, w.Via = g.Routing, m.Groups()
			w.Turn = i == 0 && g.Routing == provider.Rotate && len(cs) > 1
			pl.order = append(pl.order, w)
		}
		return cs
	}
	var out []candidate
	for i := 0; i < len(ms); {
		m := ms[i]
		if len(m.Path) > depth+1 {
			// a group in g: its models, planned by its routing
			j := i + 1
			for j < len(ms) && len(ms[j].Path) > depth+1 && ms[j].Path[depth] == m.Path[depth] {
				j++
			}
			out = append(out, planLevel(m.Via[depth], ms[i:j], depth+1, from, pl, asides, wAsides)...)
			i = j
			continue
		}
		cs, wg := weigh(m.Provider, keys(m), m.Model, from)
		for k, c := range cs {
			w := weighed(c, m.Provider, wg, false, from)
			w.Turn = k == 0 && m.Provider.Routing == provider.Rotate && len(cs) > 1
			w.Via = m.Groups()
			pl.order = append(pl.order, w)
		}
		out = append(out, cs...)
		i++
	}
	return out
}

// asideOf is how the trace tells the keys made for another protocol than
// those routed over: after them, in the order they suit the request.
func asideOf(cs []candidate, q provider.Provider, fallback bool, from provider.Protocol) []Weighed {
	var out []Weighed
	for _, c := range cs {
		w := weighed(c, q, weighing{}, fallback, from)
		w.Aside = true
		out = append(out, w)
	}
	return out
}

// restLast moves those resting after a recent failure behind the rest.
func restLast(out []candidate, pl planned) ([]candidate, planned) {
	if len(out) == 1 {
		if r, ok := restOf(out[0].restKey()); ok {
			pl.order[0].Rest = &r // tried all the same: there is no other
		} else if r, ok := restOf(out[0].restID()); ok {
			pl.order[0].Rest = &r // tried all the same: there is no other
		}
		return out, pl
	}
	var ready, resting []candidate
	var wReady, wResting []Weighed
	for i, c := range out {
		r, ok := restOf(c.restKey())
		if !ok && c.restID() != c.restKey() {
			r, ok = restOf(c.restID())
		}
		if ok {
			pl.order[i].Rest = &r
			resting, wResting = append(resting, c), append(wResting, pl.order[i])
		} else {
			ready, wReady = append(ready, c), append(wReady, pl.order[i])
		}
	}
	pl.order = append(wReady, wResting...)
	return append(ready, resting...), pl
}

var restingUntil = struct {
	sync.Mutex
	m    map[string]time.Time
	note map[string]Rest // why each rests
}{m: map[string]time.Time{}, note: map[string]Rest{}}

func (s *Server) resting(id string) bool {
	_, ok := restOf(id)
	return ok
}

// restOf is why a candidate is resting, while it is.
func restOf(id string) (Rest, bool) {
	restingUntil.Lock()
	defer restingUntil.Unlock()
	until := restingUntil.m[id]
	if !time.Now().Before(until) {
		return Rest{}, false
	}
	r := restingUntil.note[id]
	r.Until = until
	return r, true
}

// quotaWords are how vendors say "out of quota" or "slow down" when their
// status code doesn't: some answer 400 or 403 with it.
var quotaWords = regexp.MustCompile(`(?i)quota|insufficient|balance|credit|billing|exceeded|rate.?limit|usage.?limit|limit.?reached|hit your .*limit|limit.{0,24}resets|too many requests|overloaded|余额|额度|欠费|限流|频率|套餐|用量|上限`)

// unservedWords are how a vendor says the model isn't one it serves this
// key, or this way — words another provider, or key, may not answer with.
var unservedWords = regexp.MustCompile(`(?i)model.{0,80}(not (supported|accessible|available|found|enabled|allowed)|unsupported|does ?n[o']t exist|unknown|invalid)|(no such|unknown|invalid|unsupported) model|model_not_found|模型.{0,12}(不存在|不支持|无权|未开通)`)

// retryable says whether another provider may do better with a request
// that failed this way: the vendor was busy, out of quota or failing, or
// this key or provider can't serve it — not the request itself at fault.
func retryable(status int, body []byte) bool {
	switch {
	case status == 401, status == 402, status == 403, status == 404, status == 408, status == 429, status >= 500:
		return true
	case status == 400, status == 422:
		return quotaWords.Match(body) || unservedWords.Match(body)
	}
	return false
}

const (
	// lastRetries is how many times the last one left is tried again after
	// a failure that passes — a busy vendor, a dropped connection.
	lastRetries = 2
	// longestPause is the longest the vendor's Retry-After is waited for
	// before that; longer, and the agent gets the error.
	longestPause = 8 * time.Second
)

// retryPause is the first pause before the last one left is tried again;
// each pause after is twice the one before.
var retryPause = time.Second

// passing says whether a failure is one that may be gone a moment later,
// and how long to wait before trying the same one again.
func passing(status int, header http.Header, again int) (time.Duration, bool) {
	wait := retryPause << again
	if d := retryAfter(header, time.Now()); d > 0 {
		wait = d
	}
	switch {
	case wait > longestPause:
		return 0, false
	case status == 408, status == 500, status == 502, status == 503, status == 504, status == 529:
		return wait, true
	case status == 429:
		return wait, retryAfter(header, time.Now()) > 0 // a rate limit says when
	}
	return 0, false
}

// holdWriter keeps an error reply back while another provider may still
// answer: headers and body wait until release, or are dropped for the next
// try. Anything else goes straight through — but for a stream, only once
// its first content comes: an error before that is a failure another may
// answer, as an error status is.
type holdWriter struct {
	w       http.ResponseWriter
	hold    bool
	header  http.Header
	status  int
	passing bool
	held    bytes.Buffer

	stream     bool      // a stream held until its first content
	since      time.Time // when it began
	scanned    int       // how much of held has been read as events
	failure    int       // the status the stream's error stands for
	failMsg    string
	sharedPool bool // an OpenRouter upstream pool rejected this attempt

	ended bool   // the stream's last event was written: the reply is whole
	tail  []byte // the end of the last write, for a marker split across two
}

func newHoldWriter(w http.ResponseWriter, hold bool) *holdWriter {
	return &holdWriter{w: w, hold: hold, header: http.Header{}}
}

func (h *holdWriter) Header() http.Header { return h.header }

func (h *holdWriter) WriteHeader(code int) {
	if h.status != 0 {
		return
	}
	h.status = code
	if h.hold && code >= 400 {
		return
	}
	if h.hold && strings.HasPrefix(h.header.Get("Content-Type"), "text/event-stream") {
		h.stream, h.since = true, time.Now()
		return
	}
	h.pass()
}

func (h *holdWriter) pass() {
	dst := h.w.Header()
	for k, v := range h.header {
		if k != resetsHeader { // magpie's own note, for restAfter
			dst[k] = v
		}
	}
	h.w.WriteHeader(h.status)
	h.passing = true
}

func (h *holdWriter) Write(b []byte) (int, error) {
	if h.status == 0 {
		h.WriteHeader(http.StatusOK)
	}
	h.see(b)
	if h.passing {
		return h.w.Write(b)
	}
	n, err := h.held.Write(b)
	if h.stream && h.failure == 0 {
		h.scan()
	}
	return n, err
}

// streamEnds are how each protocol's stream says it is over, as they can
// only appear outside a quoted string: text that says so is escaped.
var streamEnds = [][]byte{
	[]byte(`"type":"response.completed"`), []byte(`"type":"response.incomplete"`), // Responses
	[]byte(`"type":"message_stop"`), []byte("event: message_stop"), // Anthropic
	[]byte("data: [DONE]"), // Chat Completions
}

// see notes a stream's last event going by. An agent may hang up as soon as
// it has that — Codex does — and a reply it had whole is not canceled.
func (h *holdWriter) see(b []byte) {
	if h.ended {
		return
	}
	buf := append(h.tail, b...)
	for _, m := range streamEnds {
		if bytes.Contains(buf, m) {
			h.ended = true
			return
		}
	}
	h.tail = append(h.tail[:0], buf[max(len(buf)-32, 0):]...)
}

// holdLongest is the longest a stream is held waiting for its first
// content, and holdMost the most of it.
const (
	holdLongest = 15 * time.Second
	holdMost    = 1 << 20
)

// scan reads the held stream's events so far: an error before any content
// fails it; content, or waiting too long for it, lets it through.
func (h *holdWriter) scan() {
	for {
		rest := h.held.Bytes()[h.scanned:]
		end := eventEnd(rest)
		if end < 0 {
			break
		}
		h.scanned += end
		switch kind, status, msg := streamEvent(rest[:end]); kind {
		case eventLead:
			continue
		case eventError:
			h.failure, h.failMsg = status, msg
			return
		}
		h.flow()
		return
	}
	if h.held.Len() > holdMost || time.Since(h.since) > holdLongest {
		h.flow()
	}
}

// flow lets a held stream through, and what follows it.
func (h *holdWriter) flow() {
	h.pass()
	h.w.Write(h.held.Bytes())
	h.held.Reset()
	h.Flush()
}

func (h *holdWriter) Flush() {
	if f, ok := h.w.(http.Flusher); ok && h.passing {
		f.Flush()
	}
}

// code is the status a try failed with: the reply's, or its stream's
// error's.
func (h *holdWriter) code() int {
	if h.failure != 0 {
		return h.failure
	}
	return h.status
}

// errBody is what the vendor said of the failure.
func (h *holdWriter) errBody() []byte {
	if h.failure != 0 {
		return []byte(h.failMsg)
	}
	return h.held.Bytes()
}

// failed reports a held error another provider could answer instead.
func (h *holdWriter) failed() bool {
	return !h.passing && h.code() >= 400 && retryable(h.code(), h.errBody())
}

// release sends a held reply after all: nobody else is left to try.
func (h *holdWriter) release() {
	if h.passing || h.status == 0 {
		return
	}
	h.pass()
	h.w.Write(h.held.Bytes())
	h.Flush()
}

// eventEnd is where the first whole event in b ends, or -1.
func eventEnd(b []byte) int {
	for i := 0; i+1 < len(b); i++ {
		if b[i] != '\n' {
			continue
		}
		if b[i+1] == '\n' {
			return i + 2
		}
		if b[i+1] == '\r' && i+2 < len(b) && b[i+2] == '\n' {
			return i + 3
		}
	}
	return -1
}

const (
	eventContent = iota
	eventLead    // what comes before a reply's content: a start, a ping
	eventError
)

// streamEvent says what one server-sent event of a reply is, in any of the
// protocols an agent speaks: the start of a reply, its content, or an
// error — with the status that error stands for.
func streamEvent(ev []byte) (kind, status int, msg string) {
	var name string
	var data []byte
	for _, ln := range bytes.Split(ev, []byte("\n")) {
		ln = bytes.TrimRight(ln, "\r")
		switch {
		case bytes.HasPrefix(ln, []byte("event:")):
			name = strings.TrimSpace(string(ln[6:]))
		case bytes.HasPrefix(ln, []byte("data:")):
			data = append(data, bytes.TrimSpace(ln[5:])...)
		}
	}
	if name == "" && len(data) == 0 {
		return eventLead, 0, "" // a comment, a keep-alive
	}
	var v struct {
		Type     string          `json:"type"`
		Error    json.RawMessage `json:"error"`
		Message  json.RawMessage `json:"message"` // a Responses error's; an Anthropic start's is the message
		Response struct {
			Error json.RawMessage `json:"error"`
		} `json:"response"`
		Choices *[]struct {
			Delta        map[string]any `json:"delta"`
			FinishReason *string        `json:"finish_reason"`
		} `json:"choices"`
	}
	if json.Unmarshal(data, &v) != nil {
		return eventContent, 0, "" // [DONE], or what isn't ours to read
	}
	typ := v.Type
	if typ == "" {
		typ = name
	}
	errOf := func(raw json.RawMessage) (int, int, string) {
		msg := string(raw)
		var m string
		if json.Unmarshal(v.Message, &m) == nil && m != "" {
			msg += " " + m
		}
		return eventError, streamStatus(msg), msg
	}
	switch {
	case typ == "error" || typ == "response.failed":
		if len(v.Response.Error) > 0 && string(v.Response.Error) != "null" {
			return errOf(v.Response.Error)
		}
		return errOf(v.Error)
	case len(v.Error) > 0 && string(v.Error) != "null":
		return errOf(v.Error)
	case typ == "ping", typ == "message_start", typ == "response.created", typ == "response.in_progress", typ == "response.queued":
		return eventLead, 0, ""
	case strings.HasPrefix(typ, "codex."):
		// the ChatGPT backend's word on the account (codex.rate_limits),
		// ahead of the reply: taken for content, it let the stream
		// through, and a refusal after it (response.failed) went to the
		// agent rather than tried again
		return eventLead, 0, ""
	case typ == "" && v.Choices != nil:
		// a Chat chunk: the first says only who speaks
		for _, c := range *v.Choices {
			if c.FinishReason != nil {
				return eventContent, 0, ""
			}
			for k, x := range c.Delta {
				if k != "role" && x != nil && x != "" {
					return eventContent, 0, ""
				}
			}
		}
		return eventLead, 0, ""
	}
	return eventContent, 0, ""
}

// streamStatus is the status an error in a stream stands for, as a vendor
// would have answered it before streaming.
func streamStatus(msg string) int {
	l := strings.ToLower(msg)
	switch {
	case strings.Contains(l, "overloaded"):
		return 529
	case creditWords.MatchString(msg) && !strings.Contains(l, "rate"):
		return 402
	case strings.Contains(l, "rate_limit"), strings.Contains(l, "rate limit"), strings.Contains(l, "too many"), quotaWords.MatchString(msg):
		return 429
	case unservedWords.MatchString(msg):
		return 400
	case strings.Contains(l, "invalid"), strings.Contains(l, "context_length"), strings.Contains(l, "too long"):
		return 400
	}
	return 502
}
