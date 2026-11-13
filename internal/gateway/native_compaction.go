package gateway

// Native compaction (#1099): a provider the user set to compact natively
// (provider.Provider.NativeCompaction) is asked Codex's compaction_trigger
// as Codex sent it, and the compaction item it writes back goes to Codex
// as it came, sealed (encrypted_content), not as magpie's "magpie1:"
// summary. Only the key or account that sealed it can read it again, so
// magpie remembers who wrote each one (compactions.json, beside
// affinity.json), and a later request carrying it goes only there — a
// writer whose own list has since dropped the model included, tried as
// the last resort an account whose list lacks the model is. When that one
// can't be asked — switched off, removed, out of the group — the request
// is refused, saying why: the compaction is never sent to another to be
// refused, dropped (withoutCompaction), or summarised in its place, which
// would lose the conversation's earlier context unasked. A call beside
// the conversation (compactionAside: Codex's auto-review and the like),
// which its writer doesn't serve, goes without it instead.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/steady"
	"github.com/yetone/magpie/internal/usage"
)

const (
	// checkpointKeep is how long who sealed a compaction is remembered: a
	// conversation goes on from its compaction for days.
	checkpointKeep = 30 * 24 * time.Hour
	// checkpointsKept is how many compactions, the latest, are kept.
	checkpointsKept = 1024
)

// compactionTriggerIn says whether a Responses request's input holds
// Codex's compaction_trigger.
func compactionTriggerIn(body []byte) bool {
	if !bodyHas(body, `"compaction_trigger"`) {
		return false
	}
	for _, it := range getIn(body, "input").Array() {
		if it.Get("type").Str == "compaction_trigger" {
			return true
		}
	}
	return false
}

// sealedCompactions are the encrypted_content of the compaction items in
// a Responses request's input that a vendor sealed: magpie's own
// ("magpie1:") are text by the time a request is sent, and aren't.
func sealedCompactions(body []byte) []string {
	if !bodyHas(body, `"compaction`) {
		return nil
	}
	var out []string
	for _, it := range getIn(body, "input").Array() {
		if !slices.Contains(compactionKinds, it.Get("type").Str) {
			continue
		}
		if enc := it.Get("encrypted_content").Str; enc != "" && !strings.HasPrefix(enc, magpieCompaction) {
			out = append(out, enc)
		}
	}
	return out
}

// carriesCompaction is whether c's provider can be asked Codex's own
// compaction for model: its wire carries it, and model is asked on
// Responses there.
func (s *Server) carriesCompaction(p provider.Provider, model string) bool {
	return p.CarriesCompaction() && slices.Contains(s.usable(p, model), provider.Responses)
}

// compactsNatively is whether a Codex compaction for model at p goes to it
// as Codex sent it (provider.Provider.CompactsOn).
func (s *Server) compactsNatively(p provider.Provider, model string) bool {
	return p.CompactsOn(model) && s.carriesCompaction(p, model)
}

// nativeCompactionFor is whether Codex's compaction_trigger for the model
// it asked (a provider's model, or a routing group) goes on as Codex sent
// it: every member of a group compacting natively, as any of them may
// answer it, and a key or account the request would go over able to be
// asked it (plannedCarrier, #2725). A group with one that doesn't keeps
// magpie's summary, which any member can read; so does a provider whose
// vendor lists the model only to keys that speak no Responses there, as
// with native compaction off, where the trigger was refused before.
func (s *Server) nativeCompactionFor(asked string) bool {
	if _, ms, ok := provider.FindGroup(asked); ok {
		if len(ms) == 0 {
			return false
		}
		for _, m := range ms {
			if !s.compactsNatively(m.Provider, m.Model) {
				return false
			}
		}
		return slices.ContainsFunc(ms, func(m provider.Member) bool { return s.plannedCarrier(m.Provider, m.Model) })
	}
	p, model, ok := provider.Resolve(asked)
	if !ok || !s.compactsNatively(p, model) {
		return false
	}
	// the request goes over the provider's keys, then its fallbacks' (plan)
	if s.plannedCarrier(p, model) {
		return true
	}
	for _, m := range provider.FallbackModels(p) {
		if (m.Provider.ID != p.ID || m.Model != model) && s.plannedCarrier(m.Provider, m.Model) {
			return true
		}
	}
	return false
}

// plannedCarrier is whether one of the keys or accounts plan would put in
// a Responses request's candidates for model at p — those it goes over,
// those made for another protocol, and the held and capped ones, not those
// whose own list lacks the model — can be asked Codex's compaction, as
// compactionCarriers keeps them. It reads perKeyBarred alone, not plan:
// a plan weighs the keys, and a provider routed in turn would move its
// turn along for a request that never went (every Codex turn is decided).
func (s *Server) plannedCarrier(p provider.Provider, model string) bool {
	cs, aside, _, barred := perKeyBarred(p, model, provider.Responses)
	for _, l := range [][]candidate{cs, aside, creditsHeld(barred), capHeldOnly(barred)} {
		if slices.ContainsFunc(l, func(c candidate) bool { return s.carriesCompaction(c.p, c.model) }) {
			return true
		}
	}
	return false
}

// keepsCompaction is whether body goes to p as it is for its compaction:
// Codex's trigger, to one that carries it, or a sealed compaction, to one
// set to compact natively or recorded as its writer — a fallback that
// answered a native compaction for another sealed it whatever its own
// setting (#2271). A translation would leave either out, so such a request
// is relayed.
func (s *Server) keepsCompaction(from provider.Protocol, p provider.Provider, model string, body []byte) bool {
	if from != provider.Responses || !s.carriesCompaction(p, model) {
		return false
	}
	if compactionTriggerIn(body) {
		return true
	}
	if len(sealedCompactions(body)) == 0 {
		return false
	}
	if p.CompactsOn(model) {
		return true
	}
	cp, ok := checkpointOf(body)
	return ok && cp.Provider == p.ID
}

// compactionAside is whether a call is one Codex makes beside the
// conversation, on a model of its own choosing, with the conversation's
// history in it: the auto-review of an approval (the guardian, asked on
// codex-auto-review or the model set for it), a memory, a subagent, a
// thread's title, description, summary, the next message's prediction or
// a suggestion, each on the model set for it. A
// compaction another provider sealed reaches it only because the history
// does, and no model but its writer's reads it; such a call goes without
// it rather than be refused, as each review was (echo_ts: Copilot's
// compaction sent to OpenAI's codex-auto-review, 400
// invalid_encrypted_content). What the call is about — the instructions,
// the turns since and their tool calls — stays. The conversation's own
// turns are held to the writer still (#1099).
func compactionAside(kind, model string) bool {
	switch usage.PurposeOf(kind) {
	case "kind:guardian", "kind:memory_consolidation", "kind:collab_spawn",
		"kind:thread_title", "kind:thread_description", "kind:composer_predictions", "kind:ambient_suggestions":
		return true
	}
	if isSummaryKind(kind) {
		return true
	}
	_, bare, _ := strings.Cut(model, "/")
	if bare == "" {
		bare = model
	}
	return bare == provider.CodexAutoReview
}

// foreignToSignin is the writer of a compaction body carries that
// Codex's own ChatGPT sign-in can't read: one magpie recorded as sealed
// by a provider in magpie other than the Codex subscription (Copilot, an
// API key's). A seal of the Codex subscription's, one magpie has no
// record of (OpenAI's own, compacted on the sign-in), and one whose
// provider can't be read now are the sign-in's to judge, as before.
func foreignToSignin(body []byte) (checkpointWriter, bool) {
	cp, ok := checkpointOf(body)
	if !ok || cp.Provider == "codex" {
		return checkpointWriter{}, false
	}
	p, err := provider.Find(cp.Provider)
	if err != nil || p.Account != nil && p.Account.Agent == "codex" {
		return checkpointWriter{}, false
	}
	return cp, true
}

// checkpointWriter is who sealed a compaction: the key or account
// (candidate.who), its provider and the model it answered as.
type checkpointWriter struct {
	Who      string    `json:"who"`
	Provider string    `json:"provider"`
	Model    string    `json:"model,omitempty"`
	At       time.Time `json:"at"`
}

var checkpoints = struct {
	sync.Mutex
	m map[string]checkpointWriter // hex sha256 of encrypted_content → who wrote it
	// the file as last read or written
	from string
	mod  time.Time
}{m: map[string]checkpointWriter{}}

var checkpointsSaving sync.Mutex

func checkpointsPath() string {
	return filepath.Join(filepath.Dir(provider.Path()), "compactions.json")
}

func checkpointKey(enc string) string {
	h := sha256.Sum256([]byte(enc))
	return hex.EncodeToString(h[:])
}

// checkpointFor is who sealed the compaction enc, read from the file
// again when it isn't known here and the file changed since. Called with
// checkpoints held.
func checkpointFor(enc string) (checkpointWriter, bool) {
	path := checkpointsPath()
	if path != checkpoints.from && checkpoints.from != "" {
		// another home (a test's): what was known is of the other's
		checkpoints.m = map[string]checkpointWriter{}
		checkpoints.mod = time.Time{}
	}
	k := checkpointKey(enc)
	if cp, ok := checkpoints.m[k]; ok && time.Since(cp.At) <= checkpointKeep {
		return cp, true
	}
	fi, err := os.Stat(path)
	if err != nil || path == checkpoints.from && fi.ModTime().Equal(checkpoints.mod) {
		checkpoints.from = path
		return checkpointWriter{}, false
	}
	checkpoints.from, checkpoints.mod = path, fi.ModTime()
	b, err := os.ReadFile(path)
	if err != nil {
		return checkpointWriter{}, false
	}
	var saved map[string]checkpointWriter
	if decodeJSON(b, &saved) != nil {
		return checkpointWriter{}, false
	}
	for sk, v := range saved {
		if v.Who == "" || time.Since(v.At) > checkpointKeep {
			continue
		}
		if had, ok := checkpoints.m[sk]; ok && !had.At.Before(v.At) {
			continue
		}
		checkpoints.m[sk] = v
	}
	cp, ok := checkpoints.m[k]
	return cp, ok
}

// checkpointOf is who sealed the latest compaction in a Responses
// request's input that magpie knows the writer of.
func checkpointOf(body []byte) (checkpointWriter, bool) {
	encs := sealedCompactions(body)
	if len(encs) == 0 {
		return checkpointWriter{}, false
	}
	checkpoints.Lock()
	defer checkpoints.Unlock()
	for _, enc := range slices.Backward(encs) {
		if cp, ok := checkpointFor(enc); ok {
			return cp, true
		}
	}
	return checkpointWriter{}, false
}

// wroteCheckpoint remembers that c sealed the compaction enc.
func wroteCheckpoint(enc string, c candidate) {
	now := time.Now()
	checkpoints.Lock()
	checkpointFor(enc) // the file's, read in first so the write keeps them
	checkpoints.m[checkpointKey(enc)] = checkpointWriter{Who: c.who(), Provider: c.p.ID, Model: c.model, At: now}
	checkpoints.Unlock()
	saveCheckpoints()
}

// saveCheckpoints writes the latest compactions' writers to the file,
// only for this user to read.
func saveCheckpoints() {
	checkpointsSaving.Lock()
	defer checkpointsSaving.Unlock()
	type kept struct {
		k string
		v checkpointWriter
	}
	var ks []kept
	checkpoints.Lock()
	for k, v := range checkpoints.m {
		if time.Since(v.At) <= checkpointKeep {
			ks = append(ks, kept{k, v})
		}
	}
	checkpoints.Unlock()
	slices.SortFunc(ks, func(a, b kept) int { return b.v.At.Compare(a.v.At) })
	ks = ks[:min(len(ks), checkpointsKept)]
	out := make(map[string]checkpointWriter, len(ks))
	for _, k := range ks {
		out[k.k] = k.v
	}
	b, err := json.Marshal(out)
	if err != nil {
		return
	}
	path := checkpointsPath()
	if os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return
	}
	target, err := edit.Target(path) // a symlink stays, its target written
	if err != nil {
		return
	}
	tmp := target + ".magpie-tmp"
	if os.WriteFile(tmp, b, 0o600) != nil {
		return
	}
	if steady.Rename(tmp, target) != nil {
		os.Remove(tmp)
		return
	}
	if fi, err := os.Stat(path); err == nil {
		checkpoints.Lock()
		// what this magpie wrote isn't read back as another's
		checkpoints.from, checkpoints.mod = path, fi.ModTime()
		checkpoints.Unlock()
	}
}

// checkpointReaders keeps of cands those of the key or account that
// sealed cp (behind a remote magpie, of the provider there too), pl's
// order with them; left says one is left, if only held or capped. One
// whose own list lacks the model now (planned.unlisted) is kept too: it
// is tried as the last resort an account whose list lacks the model is,
// and none other can read what it sealed. Its row goes from left to the
// order with it, so the Routing page doesn't tell it both tried and
// unlisted, skipped.
func checkpointReaders(cands []candidate, pl planned, cp checkpointWriter) (_ []candidate, _ planned, left bool) {
	// behind a remote magpie, the provider there that sealed it too
	reads := func(c candidate) bool { return c.sameVendor(cp.Who, cp.Model) }
	kept, pl, ok := keepCandidates(cands, pl, reads)
	if ok {
		return kept, pl, true
	}
	var found []candidate
	var ws []Weighed
	for i, c := range pl.unlisted {
		if !reads(c) {
			continue
		}
		found = append(found, c)
		w := pl.unlistedW[i]
		w.Unlisted = false // it is asked, as the conversation's only reader
		ws = append(ws, w)
	}
	if len(found) == 0 {
		return nil, pl, false
	}
	// the rows it goes to order with: they leave left, or the Routing page
	// would have it both tried and unlisted, skipped
	asked := make(map[string]bool, len(ws))
	for _, w := range ws {
		asked[w.ID] = true
	}
	pl.left = slices.DeleteFunc(pl.left, func(w Weighed) bool { return asked[w.ID] })
	pl.order = append(pl.order, ws...)
	return found, pl, true
}

// checkpointHeld is, of held, the key or account that sealed the native
// compaction cp a request carries, when it is held there (in pl, which
// checkpointReaders left only it in); nil when the request carries none.
func checkpointHeld(cp *checkpointWriter, held []Weighed, pl planned) []Weighed {
	if cp == nil {
		return nil
	}
	ids := map[string]bool{}
	for _, c := range append(slices.Clone(pl.capped), pl.held...) {
		ids[c.rest] = true
	}
	var out []Weighed
	for _, w := range held {
		if w.Capped > 0 && ids[w.ID] {
			out = append(out, w)
		}
	}
	return out
}

// compactionCarriers keeps of cands those that can be asked Codex's own
// compaction.
func (s *Server) compactionCarriers(cands []candidate, pl planned) (_ []candidate, _ planned, left bool) {
	return keepCandidates(cands, pl, func(c candidate) bool { return s.carriesCompaction(c.p, c.model) })
}

func keepCandidates(cands []candidate, pl planned, keep func(candidate) bool) ([]candidate, planned, bool) {
	var kept []candidate
	var order []Weighed
	for i, c := range cands {
		if keep(c) {
			kept = append(kept, c)
			if i < len(pl.order) {
				order = append(order, pl.order[i])
			}
		}
	}
	cands, pl.order = kept, order
	pl.held = slices.DeleteFunc(slices.Clone(pl.held), func(c candidate) bool { return !keep(c) })
	pl.capped = slices.DeleteFunc(slices.Clone(pl.capped), func(c candidate) bool { return !keep(c) })
	return cands, pl, len(cands) > 0 || len(pl.held) > 0 || len(pl.capped) > 0
}

// compactionWho is who sealed the native compaction cp, for the trace;
// "" for none.
func compactionWho(cp *checkpointWriter) string {
	if cp == nil {
		return ""
	}
	return cp.Who
}

// checkpointError is what Codex is told when the compaction a request
// carries was sealed by a key or account the request can't go to now:
// why, as magpie's own settings say, and what lets the conversation go on
// (#2642: it said "switched off, removed, or no longer serves" of an
// account that was on, and "switch it on again" changed nothing).
func checkpointError(model string, cp checkpointWriter) string {
	what := "account"
	if strings.Contains(cp.Who, "#") {
		what = "key"
	}
	why, fix := checkpointGone(model, what, cp)
	msg := fmt.Sprintf("This conversation was compacted natively by %s: the compaction is sealed (encrypted) to that %s, and no other key or account can read it. The request for %s can't go to it now: %s. magpie doesn't send the compaction to another, which would refuse it, nor drop it or summarise it in its place, which would lose the conversation's earlier context. To go on with this conversation, %s; or start a new conversation.",
		cp.Who, what, model, why, fix)
	if p, err := provider.Find(cp.Provider); err == nil {
		if p.NativeCompaction {
			msg += fmt.Sprintf(" Native compaction is on for %s, so each conversation it compacts is held to the %s that compacted it; turned off (the provider's Compaction row), magpie summarises instead, which any of them can read.", p.ID, what)
		} else {
			msg += fmt.Sprintf(" Native compaction is off for %s now, so a compaction magpie routes among its %ss is magpie's summary, which any of them can read.", p.ID, what)
		}
	}
	return msg
}

// checkpointGone is why the key or account that sealed cp can't be asked
// for model, and what would let it be: removed, signed out, its sign-in
// lapsed, paused or switched off, the model kept from it, or the model or
// group asked for not going to it. The capped and the held are told
// checkpointCappedError's instead.
func checkpointGone(model, what string, cp checkpointWriter) (why, fix string) {
	p, err := provider.Find(cp.Provider)
	if err != nil {
		return fmt.Sprintf("the provider %s is no longer in magpie", cp.Provider),
			fmt.Sprintf("add %s again with that %s", cp.Provider, what)
	}
	served := cp.Model
	if served == "" {
		served = model
	}
	if _, off := provider.ModelDisabled(p.ID, served); off {
		return fmt.Sprintf("%s/%s is disabled", p.ID, served), fmt.Sprintf("enable %s/%s again", p.ID, served)
	}
	if _, kid, isKey := strings.Cut(cp.Who, "#"); isKey {
		for _, k := range p.KeyList() {
			if k.ID != kid {
				continue
			}
			switch {
			case !k.On:
				return fmt.Sprintf("that key of %s is switched off", p.ID), "switch it on again in the provider's keys"
			case !p.AccountServes(kid, served):
				return fmt.Sprintf("that key is set not to serve %s (Model settings)", served), fmt.Sprintf("let it serve %s again", served)
			}
			return checkpointNotRouted(model, served)
		}
		return fmt.Sprintf("that key is no longer one of %s's", p.ID), fmt.Sprintf("add the same key to %s again", p.ID)
	}
	user := strings.TrimPrefix(cp.Who, cp.Provider+"@")
	agent := cp.Provider
	if p.Account != nil && p.Account.Agent != "" {
		agent = p.Account.Agent
	}
	for _, l := range provider.Logins(agent) {
		if !strings.EqualFold(l.User, user) {
			continue
		}
		switch {
		case l.Lapsed != "":
			return "its sign-in has lapsed", fmt.Sprintf("sign in to %s again in magpie", l.User)
		case l.Paused:
			return "it is paused, passed over while magpie's other accounts are on", fmt.Sprintf("turn %s on again: the others can stay on beside it, and only this conversation is held to it", l.User)
		case !l.On:
			return "it is switched off in magpie", fmt.Sprintf("switch %s on again: the others can stay on beside it, and only this conversation is held to it", l.User)
		case !p.AccountServes(l.User, served):
			return fmt.Sprintf("it is set not to serve %s (Model settings)", served), fmt.Sprintf("let %s serve %s again", l.User, served)
		}
		return checkpointNotRouted(model, served)
	}
	return fmt.Sprintf("%s is no longer signed in to magpie", user), fmt.Sprintf("sign in to %s again in magpie", user)
}

// checkpointNotRouted is checkpointGone for a writer that is on and may
// serve the model: what was asked for doesn't go to it.
func checkpointNotRouted(model, served string) (why, fix string) {
	return fmt.Sprintf("%s doesn't route to it (another model, or a routing group it isn't in)", model), fmt.Sprintf("ask for %s, which it compacted, through a route that has it", served)
}

// carrierError is what Codex is told when its compaction_trigger, asked
// of a provider set to compact natively, has nowhere left that carries it.
func carrierError(model string) string {
	return fmt.Sprintf("Codex asked %s for its own compaction (native compaction), and none of the keys or accounts that serve %s now can be asked it on the Responses API. magpie doesn't turn it into a summary of its own unasked. Turn native compaction off for the provider to have magpie summarise the conversation instead.", model, model)
}

// compactionWatch reads, on the way past, the compaction a native
// compaction request's reply seals, for wroteCheckpoint.
type compactionWatch struct {
	w    http.ResponseWriter
	buf  bytes.Buffer
	over bool
}

// compactionWatchMax is how much of a reply is read for its compaction.
const compactionWatchMax = 64 << 20

func (c *compactionWatch) Header() http.Header         { return c.w.Header() }
func (c *compactionWatch) WriteHeader(code int)        { c.w.WriteHeader(code) }
func (c *compactionWatch) Unwrap() http.ResponseWriter { return c.w }

func (c *compactionWatch) Write(p []byte) (int, error) {
	if !c.over {
		if c.buf.Len()+len(p) > compactionWatchMax {
			c.over = true
			c.buf = bytes.Buffer{}
		} else {
			c.buf.Write(p)
		}
	}
	return c.w.Write(p)
}

func (c *compactionWatch) Flush() {
	if f, ok := c.w.(http.Flusher); ok {
		f.Flush()
	}
}

// sealed is the encrypted_content of the last compaction item the reply
// gave: in a stream's output_item.done or response.completed, or a JSON
// body's output.
func (c *compactionWatch) sealed() string {
	b := bytes.TrimSpace(c.buf.Bytes())
	if len(b) == 0 {
		return ""
	}
	found := ""
	fromItems := func(items []gjson.Result) {
		for _, it := range items {
			if slices.Contains(compactionKinds, it.Get("type").Str) {
				if enc := it.Get("encrypted_content").Str; enc != "" && !strings.HasPrefix(enc, magpieCompaction) {
					found = enc
				}
			}
		}
	}
	if b[0] == '{' {
		fromItems(gjson.GetBytes(b, "output").Array())
		return found
	}
	for line := range bytes.SplitSeq(b, []byte("\n")) {
		data, ok := bytes.CutPrefix(bytes.TrimSpace(line), []byte("data:"))
		if !ok || !bodyHas(data, `"compaction`) {
			continue
		}
		ev := gjson.ParseBytes(bytes.TrimSpace(data))
		if it := ev.Get("item"); it.Exists() {
			fromItems([]gjson.Result{it})
		}
		fromItems(ev.Get("response.output").Array())
	}
	return found
}
