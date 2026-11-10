package gateway

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptrace"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/claudeseal"
	"github.com/yetone/magpie/internal/codexcat"
	"github.com/yetone/magpie/internal/codexsummary"
	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

// CodexPath is where Codex's built-in OpenAI provider reaches magpie, as its
// `openai_base_url`. Codex keeps its own sign-in and sends what it would send
// the ChatGPT backend: a request for one of its own models goes on there as
// it came, one for a magpie model (a catalog id, provider/model) is served
// like any other, and the model list is OpenAI's with magpie's added. Ending
// in /backend-api/codex keeps Codex treating it as that backend.
const CodexPath = "/backend-api/codex"

// codexAPIBase is where a Codex signed in with an API key sends its requests;
// its model list still comes from the ChatGPT backend. A var so tests can
// point it elsewhere.
var codexAPIBase = "https://api.openai.com/v1"

// magpieCompaction marks a compaction item magpie made: its summary, which
// only magpie reads back (codexsummary).
const magpieCompaction = codexsummary.Mark

var nativeSealedAgentPayload = lazyre.New(`^gAAAAA[A-Za-z0-9_-]+={0,2}$`)

// codexCompactPrompt and codexSummaryPrefix are Codex's own (Apache-2.0,
// openai/codex, prompts/templates/compact).
const codexCompactPrompt = `You are performing a CONTEXT CHECKPOINT COMPACTION. Create a handoff summary for another LLM that will resume the task.

Include:
- Current progress and key decisions made
- Important context, constraints, or user preferences
- What remains to be done (clear next steps)
- Any critical data, examples, or references needed to continue

Be concise, structured, and focused on helping the next LLM seamlessly continue the work.`

const codexSummaryPrefix = codexsummary.Prefix

func (s *Server) codexBackend(w http.ResponseWriter, r *http.Request) {
	// Responses over a WebSocket, Codex's own way to its provider: each
	// turn is served as its POST is (responses_ws.go), and Codex sends
	// only a turn's new input after the reply it follows (#4058)
	if wantsSocket(r) {
		if strings.TrimPrefix(r.URL.Path, CodexPath) == "/responses" && s.serveResponsesSocket(w, r) {
			return
		}
		http.Error(w, "magpie serves a WebSocket on "+CodexPath+"/responses alone", http.StatusUpgradeRequired)
		return
	}
	// the pool's accounts and their model lists say they are this Codex, or
	// newer: the backend serves a model only to a client that knows it
	provider.SawCodexClient(r.Header)
	rest := strings.TrimPrefix(r.URL.Path, CodexPath)
	// a turn of Codex's own is read to codexBodyLimit, one for a magpie
	// model held to the limit of every other route once it is named
	body, ok := s.readRequestBody(w, r, provider.Responses, s.codexLimitOf())
	if !ok {
		return
	}
	var err error
	switch {
	case r.Method == http.MethodGet && rest == "/models":
		s.codexModels(w, r)
		return
	case r.Method == http.MethodPost && rest == AlphaSearchPath:
		// web.run on a magpie model is searched where the model is
		// served, never relayed on Codex's own sign-in; one of Codex's
		// own models goes through magpie's accounts when its turns do
		// (codexAccounts), else on as it came
		model := modelOf(body)
		if !strings.Contains(model, "/") {
			id, ok := codexAccounts(r, model)
			if !ok {
				break
			}
			body = withModel(body, id)
		}
		s.alphaSearchBody(w, r, body)
		return
	case r.Method == http.MethodPost && (rest == "/responses" || rest == "/responses/compact"):
		var model string
		body, model, err = requestModel(body)
		if err != nil {
			writeError(w, provider.Responses, 400, err.Error())
			return
		}
		// the route that shows it while it is held before routing names
		// it, as handle's and gemini's do (Jason on Discord: a held Codex
		// turn showed as no model, "magpie · 0 on")
		noteAsked(r.Context(), model)
		// the service tier set for Codex, in place of the one it asked
		body = codexTiered(r, body, model)
		// a turn of the conversation's own: the model its side calls may
		// follow (codexFollowTo)
		meta := requestSessionMetadata(r.Header, body)
		kind := requestCallKind(r.Header, meta)
		// a subagent resume_agent brought back on its lead's model goes
		// back on the one it was spawned with (codexResumeTo)
		if to := s.codexResumeTo(r, agentOf(r), kind, model, meta); to != "" {
			body, model = withModel(body, to), to
		}
		s.codexLeads.saw(r, kind, model)
		// a thread's title, where Settings sends it (#705) — the request
		// still on Codex's own model, through its sign-in, when it says
		// nothing of them
		if rest == "/responses" {
			session := func() string { return s.codexSideSession(r, body) }
			if to := codexTitlesTo(r.Header, body, false, session); to != "" {
				s.codexTitle(w, r, body, to)
				return
			}
			if to := codexDescriptionsTo(r.Header, body, false, session); to != "" {
				s.codexDescription(w, r, body, to)
				return
			}
			if to := codexSummariesTo(r.Header, body, false, session); to != "" {
				s.codexSummary(w, r, body, to)
				return
			}
			if to := codexPredictionsTo(r.Header, body, false, session); to != "" {
				s.codexPrediction(w, r, body, to)
				return
			}
			// a subagent on one of Codex's own models, put on the model
			// set for Codex's subagents, a ChatGPT account's in magpie;
			// the request on the model asked for when it can't be, the
			// trace saying why (codexUpstreamOn, serve). With none set,
			// it stays on the model it was named (codexFollowTo). A
			// memory call with nothing set for it goes on the model its
			// conversation is on when that is one of magpie's, not on
			// the ChatGPT sign-in (codexFollowTo). Set to the lead's model,
			// a subagent asking for any model goes on the lead's, one of
			// Codex's own through its sign-in too (codexSubagentPick)
			if !strings.Contains(model, "/") || provider.CodexSubagentModel() == provider.CodexSubagentLead {
				pick := s.codexSubagentPick(r, agentOf(r), kind, model, body, true)
				if to := s.codexFollowTo(r, agentOf(r), kind, model, body); to != "" {
					body, model = withModel(body, to), to
				}
				r = withSubagentPick(r, pick)
				if pick.Moved() {
					body, model = withModel(body, pick.To), pick.To
				}
			}
			// spawn_agent names every model a subagent may be put on,
			// not only Codex's first five (Gearclickist on Discord)
			body = spawnAgentModels(r, body, model, true)
		}
		// one of Codex's own models the user disabled (#1867) is refused
		// here, never relayed to the ChatGPT backend; a magpie model is
		// refused by serve
		if !strings.Contains(model, "/") {
			if msg, off := codexNativeOff(model); off {
				writeError(w, provider.Responses, http.StatusForbidden, msg)
				return
			}
		}
		// The namespace owns the route even if a model is not in the catalog.
		// Unknown providers/groups must fail locally, never fall through to OpenAI.
		if strings.Contains(model, "/") {
			if most := s.bodyLimitOf(); int64(len(body)) > most {
				writeError(w, provider.Responses, http.StatusRequestEntityTooLarge, overLimit(most).Error()+
					"; this limit is for a magpie model, and one of Codex's own models takes up to "+sizeWord(s.codexLimitOf()))
				return
			}
			if rest == "/responses/compact" {
				writeError(w, provider.Responses, 400, "/responses/compact is not supported for Magpie models; use a compaction_trigger on /responses")
				return
			}
			if kind == "guardian" || isMemoryKind(kind) {
				// an auto-review or a memory call, at an effort the
				// model takes (codexSideEffort)
				body = codexSideEffort(body, model)
			}
			// a sealed subagent task goes to a ChatGPT account the model
			// or group has, or is turned away (serve, sealedReader)
			// a provider set to compact natively is asked Codex's own
			// compaction, and its sealed item goes back as it came (#1099)
			body, compact := codexInputAs(body, true, s.nativeCompactionFor(model))
			if compact {
				s.codexCompact(w, r, body)
				return
			}
			s.serveAgent(w, r, provider.Responses, body)
			return
		}
		// the ChatGPT backend checks every item id as OpenAI does
		body = plainItemIDs(boundCallIDs(callItemIDs(body)))
		if rest == "/responses/compact" {
			// Codex compacts a thread before its first turn back on one of
			// its own models; the thread's reasoning from another vendor
			// goes, as it does from a turn: with its text in it, the
			// backend refuses the whole compaction over "input[n].content:
			// array too long", and Codex shows it as the turn's error
			// (jue on Discord). A summary magpie made goes as its text.
			body, _ = codexInput(body, false)
			// a compaction another provider sealed: the sign-in can't
			// read it, and nothing here can compact it but its writer
			if cp, foreign := foreignToSignin(body); foreign {
				writeError(w, provider.Responses, 400, checkpointError(model, cp))
				return
			}
			// a key held to some accounts (#905) may not spend the sign-in
			// compaction still relays on: refused as its turns are (#967),
			// with nothing asked of OpenAI — and refused when the sign-in
			// can't be shown to be the key's, switched off or gone
			on := compactOn(r, model)
			// a seal only its writer reads (#1099, #2271): a compact
			// carrying one magpie recorded the writer of is judged here —
			// the writer on in magpie answers it signed as the writer, a
			// gone writer is refused with the reason — not relayed to be
			// refused there (#2727)
			writer, gone, _ := sealedCompact(r, model, body)
			if writer != nil {
				on = writer
			}
			// the key's accounts are asked of the account the compact
			// goes to: the writer when it answers it, not the sign-in
			if who, held := compactSigninHeld(r, model, on); held {
				msg := compactHeldError(who, model)
				if writer != nil {
					msg = sealedCompactHeldError(who, model, writer)
				}
				writeError(w, provider.Responses, 403, msg)
				return
			}
			if gone != "" {
				writeError(w, provider.Responses, 400, gone)
				return
			}
			if on != nil {
				s.codexUpstreamOn(w, r, rest, body, on)
				return
			}
			break // preserve native compaction's existing passthrough
		}
		id, ok := codexAccounts(r, model)
		if !ok {
			id, ok = codexSealedFor(r, model, body)
		}
		if ok {
			// the codex/ in it is magpie's, not Codex's: Codex asked its
			// own model bare (providerPicked)
			r = providerPicked(r)
			// through magpie's accounts, Codex's compaction is the codex
			// provider's to decide as a magpie model's is: sealed natively
			// only with its native compaction on, as only the account that
			// sealed it can read it; off, magpie's summary, which any of
			// them reads (#2582, #2642: one of Codex's own models compacted
			// natively whatever the setting, and the conversation was held
			// to that account)
			body, compact := codexInputAs(withModel(body, id), true, s.nativeCompactionFor(id))
			if compact {
				s.codexCompact(w, r, body)
				return
			}
			s.serveAgent(w, r, provider.Responses, body)
			return
		}
		// a turn on Codex's own sign-in that masking would change goes
		// to the model the user set for that, as written, as one through
		// magpie's accounts does in serve (maskRouteFor)
		if to := codexMaskRoute(r, rest, body); to != "" {
			body, compact := codexInputAs(withModel(body, to), true, s.nativeCompactionFor(to))
			if compact {
				s.codexCompact(w, r, body)
				return
			}
			s.serveAgent(w, r, provider.Responses, body)
			return
		}
		body, _ = codexInput(body, false)
		// a compaction another provider sealed (Copilot's, native) is
		// never relayed to the sign-in, which can't read it: a call
		// beside the conversation goes without it, a turn is refused
		// with why, as one routed is (#1099; echo_ts: every auto-review
		// on codex-auto-review answered 400 invalid_encrypted_content)
		if cp, foreign := foreignToSignin(body); foreign {
			if !compactionAside(kind, model) {
				writeError(w, provider.Responses, 400, checkpointError(model, cp))
				return
			}
			if b, ok := withoutCompaction(body); ok {
				body = b
			}
		}
		if r.Header.Get(AccountHeader) != "" {
			// relayed as it came, it would go to Codex's own sign-in only
			writeError(w, provider.Responses, 400, AccountHeader+" names one of magpie's Codex accounts, and Codex isn't signed in to ChatGPT here with any on in magpie")
			return
		}
	}
	if r.Method == http.MethodPost && rest == "/responses" {
		withGatewaySession(w, r)
		var recorded, hooked func()
		w, recorded = recordConversation(w, r, provider.Responses, body)
		defer recorded()
		// and the tool loop it may be stuck in spends that sign-in's
		// allowance as it would a magpie model's
		if s.stoppedToolLoop(w, r, provider.Responses, body) {
			return
		}
		// Codex's own sign-in: the request hooks, not the middleware
		w, body, hooked = gatewayHooks(w, r, provider.Responses, body)
		defer hooked()
	}
	s.codexUpstream(w, r, rest, body)
}

// sealedTaskError is what a subagent is told whose task its lead sealed
// when nothing model names can read it. lead is the provider that
// answered the lead, "" when magpie didn't (the lead was one of Codex's
// own models) or has no record of it in the last sealerKeep. A task
// already sealed opens only where it was sealed, so the way on with it
// is said too (#1367). earlier is a provider that answered the lead's
// earlier turns but can't have sealed the task (canSeal): the lead moved
// on to Codex's own model, whose turns magpie keeps no record of, and the
// task was sealed there (plugins#70). Each says what is sealed: the task
// itself, which fork_turns="none" doesn't change (#1855).
func sealedTaskError(model, lead, earlier string) string {
	if earlier != "" {
		return fmt.Sprintf("This subagent's task was sealed by the ChatGPT backend, which answered the agent that sent it on one of Codex's own models (a subagent of this thread put there, or the lead after a switch), and only a ChatGPT account can open it. %s can't. %s answered the lead's earlier turns, but it neither seals a task nor opens one. "+sealedTaskItself+" Set Codex's subagent model on magpie's Agents page to a model of a ChatGPT account added in magpie, or to one that isn't served by the ChatGPT backend, so subagents get tasks they can read. To go on with this task, have it spawned again from an agent that isn't on Codex's own models."+sealedWayOut(), model, earlier)
	}
	if lead != "" {
		return fmt.Sprintf("This subagent's task was sealed by the server that answered its lead (%s), and only that server can open it; %s can't. "+sealedTaskItself+" Give the subagent the lead's model, or a lead model that isn't served by the ChatGPT backend, so its subagents get a task they can read. To go on with this task, use a model on %s for the lead and the subagent, or have the lead spawn the subagent again."+sealedWayOut(), lead, model, lead)
	}
	return fmt.Sprintf("This subagent's task was sealed by the ChatGPT backend that answered its lead, and only that backend can open it: a ChatGPT account, or the Responses API provider that answered the lead. %s is neither: magpie has no record of it answering the lead in the last 30 days. "+sealedTaskItself+" Give the subagent the lead's model, or a lead model that isn't served by the ChatGPT backend, so its subagents get a task they can read. To go on with this task, use the lead's model, or have the lead spawn the subagent again."+sealedWayOut(), model)
}

// sealedWayOut is the way to subagents on any model from a lead on
// Codex's own models (#2245): "Codex subagents on any model", in Codex's
// settings on the Agents page (settings.CodexAgentsV1, #141), has the
// lead hand its subagents their tasks as text. Off, the refusal names
// it; on, a thread still sealing its tasks is on V2 all the same: begun
// before it was on, or held there by Codex's own features.multi_agent_v2.
func sealedWayOut() string {
	if settings.Load().CodexAgentsV1 {
		return ` "Codex subagents on any model" is on in Codex's settings on magpie's Agents page, but this thread runs Codex's multi-agent V2, whose tasks are sealed: it began before the setting was turned on, or Codex's own features.multi_agent_v2 is on in its config. Start a new thread, with features.multi_agent_v2 off.`
	}
	return ` For subagents on any model while the lead is on a ChatGPT model, turn on "Codex subagents on any model" in Codex's settings on magpie's Agents page and start a new thread: the lead then hands its subagents their tasks as text.`
}

// sealedTaskItself says what a sealed task is (#1855): spawn_agent's
// message, which the backend that answered the lead returns sealed (its
// function call's encrypted_function_args), and which Codex hands the
// subagent as it came, as its NEW_TASK (codex-rs multi_agents_v2.rs
// agent_message_from_tool). fork_turns="none" leaves only the lead's
// history out of the subagent's first request; the task is still sealed.
const sealedTaskItself = `What is sealed is the task itself: the message the agent that spawned this subagent wrote in spawn_agent, which Codex hands on as the backend sealed it. fork_turns="none" keeps the lead's earlier turns out of the subagent, but not the seal on its task, so it doesn't help here.`

// canSeal says whether the provider id names could have sealed a
// subagent's task, as sealedReader has who can open one: a ChatGPT
// account, or a provider of no sign-in serving the Responses API (a relay
// of the ChatGPT backend, #1109). One magpie can't find is taken as one
// that could.
func canSeal(id string) bool {
	p, err := provider.Find(id)
	if err != nil || p == nil {
		return true
	}
	if p.Account != nil {
		return p.Account.Agent == "codex"
	}
	return slices.Contains(p.Speaks(), provider.Responses)
}

// sealedReader is who can read a subagent's task its lead sealed: a
// ChatGPT account (#619). The ChatGPT backend seals spawn_agent's message
// for a lead it answers as it came (passthrough, a Codex account a group
// has as well), and only it opens it again. A provider that relays the
// ChatGPT backend's Responses API (Sub2API, #1109) seals its lead's tasks
// as well and opens them again: it reads the task when it answered the
// lead (lead, the provider id), and the task is relayed to it as it came,
// on Responses, never translated. sealers are the providers that answered
// the agents the sealed messages came from: the lead, for a subagent's
// task, and the conversation itself, for a lead its subagent's sealed reply
// comes back to (#1237) — the subagent, sent a task sealed there, had to be
// on that provider too.
func (s *Server) sealedReader(p provider.Provider, model string, sealers []string) bool {
	if p.Account != nil && p.Account.Agent == "codex" {
		return true
	}
	return p.Account == nil && slices.Contains(sealers, p.ID) && slices.Contains(s.usable(p, model), provider.Responses)
}

// sealedReaders keeps of cands those that can read a sealed subagent task,
// pl's order, held, capped and unlisted with them.
func (s *Server) sealedReaders(cands []candidate, pl planned, sealers []string) ([]candidate, planned) {
	var kept []candidate
	var order []Weighed
	for i, c := range cands {
		if s.sealedReader(c.p, c.model, sealers) {
			kept = append(kept, c)
			order = append(order, pl.order[i])
		}
	}
	cands, pl.order = kept, order
	pl.held = slices.DeleteFunc(slices.Clone(pl.held), func(c candidate) bool { return !s.sealedReader(c.p, c.model, sealers) })
	pl.capped = slices.DeleteFunc(slices.Clone(pl.capped), func(c candidate) bool { return !s.sealedReader(c.p, c.model, sealers) })
	// the accounts whose list lacks the model, which a native compaction's
	// writer is taken back from (checkpointReaders), are narrowed alike
	var unlisted []candidate
	var unlistedW []Weighed
	for i, c := range pl.unlisted {
		if s.sealedReader(c.p, c.model, sealers) {
			unlisted = append(unlisted, c)
			unlistedW = append(unlistedW, pl.unlistedW[i])
		}
	}
	pl.unlisted, pl.unlistedW = unlisted, unlistedW
	return cands, pl
}

// leadProvider is the provider that answered a subagent's lead, the
// thread parent names, in any scope magpie routed it in: "" when it
// answered none of the lead's turns, or so long ago it no longer counts.
// Given a lead's own conversation, it is the provider that answered the
// lead.
func leadProvider(scope, parent string) string {
	parent = strings.TrimSpace(parent)
	if parent == "" {
		return ""
	}
	sticks.Lock()
	defer sticks.Unlock()
	st, had := stickOf(scope + "|" + parent) // reads what's kept on disk too
	if !had {
		for k, v := range sticks.m {
			if strings.HasSuffix(k, "|"+parent) && (!had || v.at.After(st.at)) {
				st, had = v, true
			}
		}
	}
	if !had || time.Since(st.at) > stickKeep {
		return ""
	}
	id, _, _ := strings.Cut(st.who, "@") // an account: provider@user
	id, _, _ = strings.Cut(id, "#")      // a key: provider#key
	return id
}

// sealerKeep is how far back the usage log is read for who answered a
// lead whose answerer affinity no longer remembers (#1367).
const sealerKeep = 30 * 24 * time.Hour

// loggedAnswer is who the usage log has answering a conversation: the
// provider, and the key or account as candidate.who names it ("" when the
// record doesn't tell).
type loggedAnswer struct {
	provider, who string
	at            time.Time
}

// loggedAnswers are the answers the usage log has to these conversations
// (a lead's thread, a request's own) in the last sealerKeep, the latest
// first: who could have sealed a message in them when affinity has let it
// go — a day and more since, a restart past the conversations
// affinity.json keeps, or the lead's last reply breaking off
// (unanswered), after which nothing could answer the lead again to be
// remembered (#1367). A call refused or failed answered nothing, a title
// isn't the conversation's own turn, and Codex's own sign-in (the ChatGPT
// backend, relayed as it came) is no provider here: a lead it answered is
// told of as one sealed by the ChatGPT backend. That relay is a row whose
// provider is openai (one written before it named the account, or a call
// sent with an API key) or codex with the API host alone. A row an account
// answered names that account in Host ("host as email", provider.Where)
// and still counts.
func loggedAnswers(convs ...string) []loggedAnswer {
	convs = slices.DeleteFunc(slices.Clone(convs), func(c string) bool { return strings.TrimSpace(c) == "" })
	if len(convs) == 0 {
		return nil
	}
	var all []loggedAnswer
	// a provider renamed since answered by the id it has now, the one
	// its candidates have (cybercaius: a renamed provider was told apart
	// from itself)
	renamed := provider.UsageRenames()
	usage.VisitSessions(time.Now().Add(-sealerKeep), convs, func(r usage.Record) {
		// the provider that answered, not one a deleted provider's
		// calls were merged into on the Usage page (Renames.Own)
		r.Provider = renamed.Own(r.Provider, r.Time)
		if r.Provider == "" || r.Provider == "magpie" || r.IsRejected() || r.IsReported() || r.Status <= 0 || r.Status >= 400 || r.Computer != "" || isTitleKind(r.Kind) {
			return
		}
		// ponytail: host " as " is provider.Where. A relay that starts
		// writing Host that way would be counted as a lead's answerer.
		ownRelay := r.Provider == "openai" || (r.Provider == "codex" && !strings.Contains(r.Host, " as "))
		if ownRelay && strings.HasPrefix(r.Endpoint, CodexPath) {
			return // Codex's own sign-in
		}
		a := loggedAnswer{provider: r.Provider, at: r.Time}
		switch {
		case r.ProviderAccount != "":
			a.who = r.Provider + "@" + strings.ToLower(r.ProviderAccount)
		case r.ProviderKeyID != "":
			a.who = r.Provider + "#" + r.ProviderKeyID
		}
		all = append(all, a)
	})
	slices.SortStableFunc(all, func(a, b loggedAnswer) int { return b.at.Compare(a.at) })
	return all
}

// loggedAnswerers are the providers of loggedAnswers, each once, the
// latest first.
func loggedAnswerers(convs ...string) []string {
	var ids []string
	for _, a := range loggedAnswers(convs...) {
		if !slices.Contains(ids, a.provider) {
			ids = append(ids, a.provider)
		}
	}
	return ids
}

// leadFirst puts first the account that answered the lead, the thread
// parent names, in scope: the one that sealed its subagent's task, which
// another account may not open, as it doesn't another's reasoning.
func leadFirst(scope, parent string, cands []candidate, pl planned) ([]candidate, planned, string) {
	parent = strings.TrimSpace(parent)
	if parent == "" {
		return cands, pl, ""
	}
	sticks.Lock()
	st, had := stickOf(scope + "|" + parent)
	if !had {
		// a group with rules keeps the lead's conversation under its
		// first words too
		for k, v := range sticks.m {
			if strings.HasPrefix(k, scope+"|") && strings.HasSuffix(k, "|"+parent) {
				st, had = v, true
				break
			}
		}
	}
	sticks.Unlock()
	if !had || time.Since(st.at) > stickKeep {
		// affinity let the lead go: the usage log still has which key or
		// account answered it (#1367)
		st = stick{}
		for _, a := range loggedAnswers(parent) {
			if a.who != "" {
				st.who = a.who
				break
			}
		}
		if st.who == "" {
			return cands, pl, ""
		}
	}
	for i, c := range cands {
		if i > 0 && c.sameVendor(st.who, st.model) {
			cands = append(append([]candidate{c}, cands[:i]...), cands[i+1:]...)
			pl.order = append(append([]Weighed{pl.order[i]}, pl.order[:i]...), pl.order[i+1:]...)
			return cands, pl, c.rest
		}
	}
	if len(cands) > 0 && cands[0].sameVendor(st.who, st.model) {
		return cands, pl, cands[0].rest
	}
	return cands, pl, ""
}

// Only native sealed agent tasks need this guidance. Other encrypted_content
// fields (including ordinary model history) keep their existing handling.
func hasSealedAgentMessage(body []byte) bool {
	var request map[string]json.RawMessage
	if decodeJSONView(body, &request) != nil {
		return false
	}
	items, isList := inputList(request["input"])
	if !isList {
		return false
	}
	for _, raw := range items {
		var item struct {
			Type    string          `json:"type"`
			Content json.RawMessage `json:"content"`
		}
		if decodeJSONView(raw, &item) != nil || item.Type != "agent_message" {
			continue
		}
		for _, content := range elementsOf(trimJSON(item.Content)) {
			var part struct {
				Type             string `json:"type"`
				EncryptedContent string `json:"encrypted_content"`
			}
			if decodeJSON(content, &part) == nil && part.Type == "encrypted_content" && nativeSealedAgentPayload.MatchString(part.EncryptedContent) {
				return true
			}
		}
	}
	return false
}

// withoutSealedMessages is a request's input with each sealed agent message
// that is not a task left out, a note in its place; ok says one was. In
// MultiAgentV2 an agent on one of Codex's own models has its send_message
// sealed by the ChatGPT backend, and Codex hands it on as encrypted_content
// under a plain header (codex-rs protocol.rs to_model_input_item: "Message
// Type: MESSAGE" for a message, NEW_TASK for a task, spawn_agent's or
// followup_task's). A lead or subagent served by a model that can't open
// it was turned away whole, though all it lost was that message: the
// sender's final result reaches its lead as text all the same (#1613). A
// task is kept, and still turned away (serve): the subagent would have
// nothing to do. An item whose header says neither is taken as a task.
func withoutSealedMessages(body []byte) ([]byte, bool) {
	var q map[string]json.RawMessage
	if json.Unmarshal(body, &q) != nil {
		return body, false
	}
	var items []json.RawMessage
	if json.Unmarshal(q["input"], &items) != nil {
		return body, false
	}
	changed := false
	for i, raw := range items {
		var item map[string]json.RawMessage
		if json.Unmarshal(raw, &item) != nil || string(item["type"]) != `"agent_message"` {
			continue
		}
		var parts []map[string]any
		if json.Unmarshal(item["content"], &parts) != nil {
			continue
		}
		message, sealed := false, false
		for _, p := range parts {
			switch p["type"] {
			case "input_text":
				t, _ := p["text"].(string)
				message = message || strings.HasPrefix(t, "Message Type: MESSAGE\n")
			case "encrypted_content":
				c, _ := p["encrypted_content"].(string)
				sealed = sealed || nativeSealedAgentPayload.MatchString(c)
			}
		}
		if !message || !sealed {
			continue
		}
		var from string
		json.Unmarshal(item["author"], &from)
		if from == "" {
			from = "another agent"
		}
		note := "[magpie: this message from " + from + " was sealed by the ChatGPT backend, which answered that agent on one of Codex's own models, and only it can open it; this model can't, so it was left out. An agent's final result still reaches the agent that spawned it as text.]"
		for j, p := range parts {
			if c, _ := p["encrypted_content"].(string); p["type"] == "encrypted_content" && nativeSealedAgentPayload.MatchString(c) {
				parts[j] = map[string]any{"type": "input_text", "text": note}
			}
		}
		b, err := marshalPlain(parts)
		if err != nil {
			continue
		}
		item["content"] = b
		if b, err = marshalPlain(item); err != nil {
			continue
		}
		items[i], changed = b, true
	}
	if !changed {
		return body, false
	}
	var err error
	if q["input"], err = marshalPlain(items); err != nil {
		return body, false
	}
	nb, err := marshalPlain(q)
	if err != nil {
		return body, false
	}
	return nb, true
}

// compactSigninHeld is the calling key when its accounts (#905) hold a
// native compaction off the account Codex is signed in to. Compaction
// relays as it came (#876, the encrypted history in it readable only by
// the ChatGPT backend that sealed it), so it spends that sign-in with
// nothing of the key asked — and the gate fails closed: a held key may
// compact only where the sign-in can be shown to be one of the key's
// accounts (AllowsAccount), so a Codex switched off in magpie, or
// signed in to ChatGPT nowhere magpie can resolve, is refused as an
// account the key may not use is, the relay reaching the sign-in all
// the same. Codex signed in with its own API key (auth.json's
// auth_mode) spends no account the key's list governs, and is the one
// allowance. A key held to some models alone (#882) still compacts as
// it always did, its holds asked on /responses where its turns go.
func compactSigninHeld(r *http.Request, model string, on *provider.Provider) (access.Identity, bool) {
	who, held := accountHolds(r)
	if !held || provider.CodexAPIKeySignedIn() {
		return who, false
	}
	if on != nil {
		// compacted on another of Codex's accounts (compactOn): the key
		// holds that one, not the sign-in
		return who, !who.AllowsAccount(on.ID, on.AccountID())
	}
	p, _, ok := provider.Resolve("codex/" + model)
	if !ok || p.Account == nil || p.Account.Agent != "codex" || !who.AllowsAccount(p.ID, p.AccountID()) {
		return who, true
	}
	return who, false
}

// compactHeldError is what a native compaction is refused with on a key
// whose accounts (#905) don't take in the sign-in Codex compacts on:
// native compaction has no other route — the encrypted history in the
// request is bound to that account — so the message says the two ways
// out, not the accounts alone.
func compactHeldError(who access.Identity, model string) string {
	return fmt.Sprintf("Native compaction for %s goes through the account Codex is signed in to, which the gateway key %q may not use; it may use %s. Native compaction has no other route: add the signed-in account to the key in magpie's Gateway page, or use one of magpie's models, whose compaction goes through a compaction_trigger on /responses and the key's accounts.",
		model, who.KeyName, keyAccountNames(who))
}

// sealedCompactHeldError is compactHeldError for a compact sealed by
// writer, another of Codex's accounts, which only it can read (#2727).
func sealedCompactHeldError(who access.Identity, model string, writer *provider.Provider) string {
	return fmt.Sprintf("This conversation's native compaction for %s was sealed by %s, and only that account can read it; the gateway key %q may not use it; it may use %s. Add %s to the key in magpie's Gateway page, or start a new conversation.",
		model, writer.Account.User, who.KeyName, keyAccountNames(who), writer.Account.User)
}

// compactOn is the account a native compaction of one of Codex's own
// models goes to in place of the ChatGPT sign-in it would relay on: one of
// Codex's other accounts on in magpie while the sign-in is paused there
// (#263, the user's own account kept for Codex's remote control, Computer
// Use and the like, another doing the work). Its turns already go to the
// others (codexAccounts, routing passing the paused one over); relayed as
// it came, compaction alone still spent the paused account — and on
// history the others sealed. The first not resting is taken, else the
// first. nil keeps the relay to the sign-in: not paused, or nothing else
// on, or Codex on an API key.
func compactOn(r *http.Request, model string) *provider.Provider {
	if model == "" || strings.Contains(model, "/") || apiKey(r.Header) || r.Header.Get(AccountHeader) != "" {
		return nil
	}
	p, _, ok := provider.Resolve("codex/" + model)
	if !ok || p.Account == nil || p.Account.Agent != "codex" || !p.OwnPaused() {
		return nil
	}
	others := p.AlsoOn()
	// one whose sign-in its vendor revoked is the last asked (#1793)
	revoked := func(o provider.Provider) bool { return provider.SignInMarked(o.Account) }
	for i, o := range others {
		if !resting(candidate{p: o, model: model, rest: o.ID}) && !revoked(o) {
			return &others[i]
		}
	}
	for i, o := range others {
		if !revoked(o) {
			return &others[i]
		}
	}
	if len(others) > 0 {
		return &others[0]
	}
	return nil
}

// codexAccounts is what a request for one of Codex's own models is served
// as when Codex is signed in to ChatGPT and more of its accounts are on in
// magpie: the codex subscription's model (codex/<model>), which goes to the
// account Codex is signed in to and, when that one is out of its allowance
// or rate limited, on to the next — as it can't when relayed as it came.
// A gateway key held to some models or accounts (#882, #905) is served
// through it always, alone no less: the relay would spend Codex's own
// sign-in with nothing of the key asked, its accounts and its models both.
// codexHeldKnown is provider.CodexUsedUpKnown, which tests set.
var codexHeldKnown = provider.CodexUsedUpKnown

func codexAccounts(r *http.Request, model string) (string, bool) {
	h := r.Header
	if model == "" || strings.Contains(model, "/") || apiKey(h) {
		return "", false
	}
	id := "codex/" + model
	p, _, ok := provider.Resolve(id)
	// one account named is found among them however many are on
	pinned := h.Get(AccountHeader) != ""
	// an account with a usage cap, or set not to spend its credits, goes
	// through routing, which holds it there, even alone: relayed as it
	// came, nothing would
	if ok && p.Account != nil && p.Account.Agent == "codex" {
		if provider.HoldCaps(p, "codex", p.Account.User).Holds() {
			return id, true
		}
	}
	// the key's holds are served, not relayed past: a key held to some
	// models or accounts goes through routing, which holds it to them,
	// where the relay as it came asks nothing of the key — an account it
	// may not use spent, a model it may not asked for. Neither list set,
	// the relay is the key's as it always was
	_, keyHeld := keyHolds(r)
	_, accHeld := accountHolds(r)
	if keyHeld || accHeld {
		return id, true
	}
	// Codex's sign-in known to be out (of its allowance, no credits): a
	// Codex started before magpie took its turns over (codex.out) still
	// sends them to its built-in provider, and relayed as they came each
	// one met the same refusal until Codex was restarted, which then sent
	// them to magpie as codex/<model> (unsplashz on Discord). Served as
	// that restarted Codex's are, they go where routing sends them, with
	// no restart. A request signed by an account magpie doesn't have (a
	// Codex in WSL) goes as it came
	if ok && p.Account != nil && p.Account.Agent == "codex" && codexHeldKnown() {
		if acct := h.Get("chatgpt-account-id"); acct == "" || acct == provider.CodexSignedInAccount() {
			return id, true
		}
	}
	if !ok || p.Account == nil || p.Account.Agent != "codex" || len(p.AlsoOn()) == 0 && !pinned {
		return "", false
	}
	return id, true
}

// codexSealedFor is codexAccounts for a request magpie would relay as it
// came to Codex's own sign-in, the one account on, that carries a native
// compaction magpie recorded the writer of: it goes through routing,
// which sends it only to that writer or answers why not
// (checkpointReaders, checkpointError). Relayed, it reached the sign-in
// whoever sealed it, and was refused there (#2642: the account that
// compacted it switched off, the one left was sent it).
func codexSealedFor(r *http.Request, model string, body []byte) (string, bool) {
	if model == "" || strings.Contains(model, "/") || apiKey(r.Header) {
		return "", false
	}
	if _, sealed := checkpointOf(body); !sealed {
		return "", false
	}
	id := "codex/" + model
	if p, _, ok := provider.Resolve(id); !ok || p.Account == nil || p.Account.Agent != "codex" {
		return "", false
	}
	return id, true
}

// sealedCompact judges a legacy compact whose input carries a seal magpie
// recorded the writer of (#2727): the writer on in magpie answers it,
// signed as the writer — a seal only its writer reads (#1099, #2271), and
// relayed as it came the compact reaches the sign-in, which another
// account's seal refuses, its reason unread. A writer gone (signed out,
// lapsed, paused, switched off, kept from the model) is refused here with
// the same words the same request on /responses gets (checkpointError,
// #2642). The sign-in's pause (compactOn) changes the signature, not the
// judgment. judged is false for what goes as it came: a seal a magpie
// key's candidate wrote, a request signing with its own
// chatgpt-account-id (a Codex on WSL or another CODEX_HOME — codexServed's
// caution), a provider or sign-in state magpie cannot read, and the
// sign-in's own seal while the sign-in can still be asked (the backend
// takes its own).
func sealedCompact(r *http.Request, model string, body []byte) (writer *provider.Provider, gone string, judged bool) {
	cp, ok := checkpointOf(body)
	if !ok || strings.Contains(cp.Who, "#") {
		return nil, "", false
	}
	if acct := r.Header.Get("chatgpt-account-id"); acct != "" && acct != provider.CodexSignedInAccount() {
		return nil, "", false
	}
	p, err := provider.Find(cp.Provider)
	if err != nil || p.Account == nil || p.Account.User == "" {
		return nil, "", false
	}
	if len(provider.Logins(func() string {
		if p.Account.Agent != "" {
			return p.Account.Agent
		}
		return cp.Provider
	}())) == 0 {
		// the accounts aren't readable here (discovery off): not judged
		return nil, "", false
	}
	// whether the writer can be asked is checkpointGone's walk: for a
	// writer gone it answers with the reason, for one on and serving the
	// seal's model its answer is checkpointNotRouted — its own words for a
	// healthy writer (native_compaction.go)
	why, _ := checkpointGone(model, "account", cp)
	healthy, _ := checkpointNotRouted(model, "")
	if why != healthy {
		return nil, checkpointError(model, cp), true
	}
	user := strings.TrimPrefix(cp.Who, cp.Provider+"@")
	// the writer is matched by its email, as the record holds the account
	// user: two Team seats of one email read as one here, and a compact
	// sealed by either relays as it came — the old forwarding, not worse
	// (only the sign-in's own tokens go out unchanged)
	if strings.EqualFold(p.Account.User, user) {
		return nil, "", true // the sign-in's own: relayed as it came
	}
	others := p.AlsoOn()
	for i := range others {
		if o := others[i]; o.Account != nil && strings.EqualFold(o.Account.User, user) {
			return &others[i], "", true
		}
	}
	// on and serving, yet not among the accounts on: goes as it came
	return nil, "", false
}

// withModel is a request body asking for another model; a body of null
// goes as it came (it was a panic, writing into no map).
func withModel(body []byte, model string) []byte {
	q, err := rawObject(body)
	if err != nil || q == nil {
		return body
	}
	q["model"], _ = json.Marshal(model)
	b, err := marshalJSON(q)
	if err != nil {
		return body
	}
	return b
}

// marshalJSON is json.Marshal, a request body split at its members or
// items written back without encoding/json's scanner (marshalRaw).
func marshalJSON(v any) ([]byte, error) {
	if b, ok := marshalRaw(v, true); ok {
		return b, nil
	}
	return json.Marshal(v)
}

// codexUpstream relays a request as it came, the sign-in included, to where
// Codex would have sent it.
// relayOnCredits says whether the account a turn is relayed on — on, or
// Codex's own sign-in when on is nil — goes on past its allowance on its
// credits, as its last reading says, and is let spend them (onCredits).
func relayOnCredits(on *provider.Provider) bool {
	if on != nil {
		return candidate{p: *on}.onCredits()
	}
	who, ok := provider.CodexSignedIn()
	if !ok {
		return false
	}
	a := allowances("codex")[who]
	return a.OnCredits() && provider.CodexCredits(who)
}

func (s *Server) codexUpstream(w http.ResponseWriter, r *http.Request, rest string, body []byte) {
	s.codexUpstreamOn(w, r, rest, body, nil)
}

// codexUpstreamOn is codexUpstream signed by the account on instead of
// Codex's own sign-in, when on isn't nil (compactOn).
func (s *Server) codexUpstreamOn(w http.ResponseWriter, r *http.Request, rest string, body []byte, on *provider.Provider) {
	// a gateway key's turn on Codex's own sign-in waits for its key's slot
	// here, outside serve (key_slots.go); its agent gone, nothing is sent
	if _, err := keySlot(r.Context()); err != nil {
		return
	}
	start := time.Now()
	usage.Saw(agentOf(r))
	// Codex's own turns come here from the mux, not through serve: the
	// row written below sums this call's tries in its own context (#4456)
	r = r.WithContext(s.countBytes(r.Context()))
	w0, body0 := w, body // as they came, for the fallbacks (codexFallBack)
	// Claude's blocks magpie gave Codex (claudeseal) are Claude's alone:
	// to OpenAI each item is its summary, as before magpie carried them
	body = withoutClaudeSeals(body)
	base := provider.CodexBase
	if apiKey(r.Header) && rest != "/models" {
		base = codexAPIBase
	}
	// the account ChatGPT limited till a time it named is not asked again
	// before it: Codex's retries are answered here (codexHolds)
	holdKey := ""
	if r.Method == http.MethodPost && (rest == "/responses" || rest == "/responses/compact") {
		holdKey = codexHoldKey(base, on)
		if h, ok := codexHeld(holdKey, start); ok {
			if rest == "/responses" && base == provider.CodexBase && on == nil && s.codexFallsBack(r, body0) {
				// Codex's own sign-in held, and somewhere for the turn to
				// go on to: the fallbacks answer, as they did the refusal
				// that held it, not Codex handed that refusal again while
				// it lasts (riverleaf88 on X: the turn after went on to
				// the group no more, nor did the auto-review)
				s.codexFallBack(w0, r, body0, &keptAlive{proto: provider.Responses, told: start, streams: streamOf(body0)})
				return
			}
			s.writeCodexHeld(w, r, h, base, on, modelOf(body), start)
			return
		}
	}
	var maskedHits maskHits
	// the ChatGPT sign-in the user set to go unmasked, and its fallbacks
	// (codexFallBack masks for its own route), gets the turn as written
	// (isee1_1 on Discord); Codex's own API key is no account of magpie's
	sentWhole := base == provider.CodexBase && codexRouteUnmasked(on)
	if r.Method == http.MethodPost && !sentWhole {
		var unmask func()
		var err error
		to := provider.Provider{ID: "codex"} // the ChatGPT backend
		if on != nil {
			to = *on
		}
		w, body, unmask, maskedHits, err = redactedHits(maskedFor(r.Context(), to, false, nil), w, body)
		defer unmask()
		if err != nil {
			writeError(w, provider.Responses, maskStatus(err), err.Error())
			return
		}
	}
	if r.Method == http.MethodPost && rest == "/responses" && base == provider.CodexBase {
		// a replayed web search the request doesn't declare the tool for
		// is refused by the ChatGPT backend (#1270), as on an account's
		body = provider.DeclareSearch(body, strings.EqualFold(r.Header.Get("X-OpenAI-Internal-Codex-Responses-Lite"), "true"))
	}
	if r.Method == http.MethodPost && base == provider.CodexBase {
		// a call in the history by a name the backend refuses (another
		// provider's model wrote it) turned every turn and compaction
		// away, as on an account's (provider.CodexToolNames, wing3556)
		body = provider.CodexToolNames(body)
		// and a citation without the offsets the backend's schema wants,
		// another vendor's Responses reply's (Volcengine Ark's
		// url_citation) as Codex stored it, turned them away alike
		// (#4921)
		body = withoutOffsetlessCitations(body)
	}
	u := base + rest
	if r.URL.RawQuery != "" {
		u += "?" + r.URL.RawQuery
	}
	// Codex's turn on its own sign-in, the one account it has, with
	// nobody else to ask (codexAccounts): an overload of the backend's —
	// a busy status, or server_is_overloaded on a stream that has said
	// nothing yet (#1418), which Codex doesn't ask again for — is asked
	// again of it, as the accounts loop asks its last account, the same
	// bytes with the same sign-in, and then of the subscription's
	// fallbacks; nobody rests for it (夜松云 on Discord)
	overload := r.Method == http.MethodPost && rest == "/responses" && base == provider.CodexBase && on == nil
	streams := streamOf(body)
	kept := &keptAlive{proto: provider.Responses, told: start, streams: streams}
	busyAgain := 0     // times the turn was asked again for an overload
	var tried []string // what each of those said, for the request log
	// a turn goes in the Routing view's live trace as the others do, to
	// the one it can go to: Codex's own sign-in, or its key
	var tr *Route
	// where the try now going spent its time (TryNet): the request goes
	// out through magpie's own client, as a routed try's does, so the
	// Routing page says whose time a slow turn was here too
	nt := &netTrace{}
	first := firstToken{start: start} // the reply's first tokens (#196), counted as ms are
	served := ""                      // the model the reply says answered
	var uu Usage
	metadata := requestSessionMetadata(r.Header, body)
	kind := requestCallKind(r.Header, metadata)
	var titleReply capturedBody // bounded; ordinary streams incur no copy
	captureTitle := false
	effort := "" // the reasoning Codex asked for, which goes on as it is
	end := func(status int, msg string, tokens, out int) {}
	var tools *toolScan // whether the reply calls a tool, for a turn's end
	var live *liveRow   // the reply counted as it reaches Codex (live.go)
	if rest != "/responses" {
		// no route of its own (a compaction, a memory): recorded alone
		noteMasked(r, "chat", maskedHits, sentOf(maskedHits, body), 0, "openai", modelOf(body))
	}
	if rest == "/responses" {
		imageProvider := ""
		if !apiKey(r.Header) {
			imageProvider = "codex"
		}
		who := "Codex's own sign-in"
		if base == codexAPIBase {
			who = "Codex's API key"
		}
		model := modelOf(body)
		kept.model = model // only a turn is kept alive (overload)
		effort = requestEffort(provider.Responses, body)
		seat := Weighed{ID: "codex", Provider: "openai", Name: "OpenAI", Icon: "openai", Who: who, Kind: "account", Agent: "codex", Model: model}
		link := s.titlePrompts.observe(r, body, metadata, kind, start)
		captureTitle = link != nil && isTitleKind(kind)
		turn, scan := agentTurnOf(r, kind)
		tools = scan
		// the window Codex holds the conversation to, not models.dev's
		// for the id, which is OpenAI's API's (netube on Discord: 922K
		// on Routing for a Codex Plus account's 272K), known as the turn
		// goes out (livePrompt)
		user := ""
		if on != nil && on.Account != nil {
			user = on.Account.User
		}
		window := cmp.Or(provider.CodexWindow(user, model), catalog.ContextOf(model))
		tr = s.trace.beginFor(r, Route{turn: turn, window: window, dirOf: dirOf(provider.Responses, body), tools: tools, imageTurn: drawingTurnID(metadata.Turn), imageCaller: codexTurnKey(r, callerOf(r).agent), imageProvider: imageProvider, TitleLink: link, Time: start, Agent: callerOf(r).agent, Session: sessionOf(r.Header), Conv: convOf(r.Header, body), ParentSession: titleParentSession(r.Header, metadata, kind), Kind: kind, Model: model, Effort: effort, Tier: bodyTier(provider.Responses, body), Provider: "openai", Subagent: subagentPickOf(r),
			Order: []Weighed{seat}, Tries: []Try{{ID: seat.ID, Model: model, Effort: effort, Start: start}}})
		if m := noteMasked(r, "chat", maskedHits, sentOf(maskedHits, body), tr.ID, "openai", model); m != nil {
			s.trace.update(tr, func(t *Route) { t.Masked = m })
		}
		first.onFirst = s.trace.firstContent(tr)
		live = liveBegin(inflightOf(r.Context()), model, provider.Responses)
		live.routed("openai", model)
		promptRead := s.inspectPrompt(tr, provider.Responses, body, body, sentWhole)
		end = func(status int, msg string, tokens, out int) {
			ms := time.Since(start).Milliseconds()
			ttft, text := first.ms()
			replyDigest := ""
			if captureTitle && status < 400 && msg == "" && !titleReply.truncated {
				replyDigest = titleReplyDigest(titleReply.buf.Bytes(), nil)
			}
			prompt := promptRead() // outside the trace's lock, which reading it takes
			s.trace.update(tr, func(t *Route) {
				// what masking took: magpie's own time isn't split here
				t.Masking = maskingMs(r.Context(), 0)
				// the last try: the turn asked again for an overload has one each
				try := &t.Tries[len(t.Tries)-1]
				try.Done, try.Status, try.Millis, try.Error = true, status, time.Since(try.Start).Milliseconds(), msg
				// the try's first token from its own Start, as its timing's
				// first byte is: a turn asked again for an overload began
				// after the request did
				before := try.Start.Sub(start).Milliseconds()
				fromTry := func(ms int64) int64 {
					if ms <= 0 {
						return 0 // none came
					}
					return max(ms-before, 1)
				}
				try.TTFT, try.FirstText = fromTry(ttft), fromTry(text)
				try.Net = nt.timing(try.Start, 0)
				t.ReasoningHidden = first.hidesReasoning(provider.Responses, uu.Reasoning)
				try.ReasoningHidden = t.ReasoningHidden
				t.Done, t.Status, t.Error, t.Millis, t.Tokens = true, status, msg, ms, tokens
				if t.TitleLink != nil && isTitleKind(kind) && status < 400 && msg == "" && !titleReply.truncated {
					link := *t.TitleLink
					link.Reply = replyDigest
					t.TitleLink = &link
				}
				t.Output, t.Reasoning, t.TTFT, t.FirstText = out, uu.Reasoning, ttft, text
				flow := first.flowFor(uu.Reasoning)
				t.Flow, try.Flow = flow, flow
				t.Usage = routeUsage("openai", model, uu)
				if prompt != nil {
					t.Prompt = prompt.calibrated(promptCounted(t.Usage), window)
				}
				try.Served, try.Swapped = served, swapped(model, served)
				t.Served, t.Swapped = try.Served, try.Swapped
			})
		}
	}
	var res *http.Response
	autoReset := false    // a Codex reset looked at, once
	creditsAsked := false // asked again onto the account's credits, once (relayOnCredits)
	resetNote := ""       // what spending it did, for the request log
	resets := 0           // times a large request ChatGPT closed on was asked again (#1628)
	sent := len(body)     // the bytes that went, compressed (provider.CodexCompressed)
	began := start        // when the try now going began
	// failWith tells Codex of a failure before any reply: as the stream's
	// error once it was sent the stream's headers while it waited
	failWith := func(status int, msg string) {
		if kept.sent {
			streamError(w, provider.Responses, kept.model, status, msg)
			return
		}
		writeError(w, provider.Responses, status, msg)
	}
	// handOn notes a try that failed for an overload, which the turn goes
	// on from, after wait: in the trace, and as a row handed on, as the
	// accounts loop notes one
	handOn := func(status int, msg string, wait time.Duration) {
		s.trace.update(tr, func(t *Route) {
			try := &t.Tries[len(t.Tries)-1]
			try.Done, try.Status, try.Error, try.Fail = true, status, msg, failOther
			try.Millis, try.Again = time.Since(try.Start).Milliseconds(), wait.Milliseconds()
			try.Net = nt.timing(try.Start, 0)
		})
		servedProvider, servedAccount := codexServed(base, r.Header, on)
		rec := usage.Record{RouteID: tr.ID, Time: began, Agent: callerOf(r).agent, Provider: servedProvider, Host: provider.HostOf(base),
			ProviderAccount: servedAccount, Model: modelOf(body), Requested: modelOf(body), Effort: effort,
			Millis: time.Since(began).Milliseconds(), Status: status, Session: sessionOf(r.Header), NativeSession: nativeSessionOf(r.Header),
			Kind: kind, Endpoint: r.URL.Path, HandedOn: true}
		failedWith(&rec, status, msg, "")
		appendUsage(r, rec)
		tried = append(tried, fmt.Sprintf("openai: %d %s", status, msg))
	}
	// handOut is handOn for a try the account was out for (codexAccountOut),
	// the trace saying why as a routed try's does
	handOut := func(status int, msg string, body []byte) {
		handOn(status, msg, 0)
		s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1].Fail = failure(status, body) })
	}
	// askAgain pauses wait, Codex kept alive meanwhile, for the turn to be
	// asked again; false when Codex went away in it
	askAgain := func(status int, msg string, wait time.Duration) bool {
		handOn(status, msg, wait)
		busyAgain++
		if !pauseAlive(r.Context(), w, kept, streams, start, wait) {
			s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1].Gone = true })
			end(status, msg, 0, 0)
			return false
		}
		began, nt = time.Now(), &netTrace{}
		s.trace.update(tr, func(t *Route) {
			again := t.Tries[0]
			t.Tries = append(t.Tries, Try{ID: again.ID, Model: again.Model, Effort: again.Effort, Start: began})
		})
		first, titleReply = firstToken{start: start, onFirst: s.trace.firstContent(tr)}, capturedBody{}
		return true
	}
	var sniff *usageSniffer
	var refusal []byte // what OpenAI said, of a request it turned away
	// how the last try's reply ended, for the log of one that ended early
	// (codexEndedEarly): the read of OpenAI's that failed, the write to
	// the client that did, and the bytes it was passed
	var readErr, writeErr error
	var passed int64
	relayedAcct := "" // magpie's ChatGPT account the turn went out on, if any
	if rest == "/responses" && base != codexAPIBase {
		// the account goes on its credits, its week used up: a reset it
		// spends by itself when the week runs out goes first
		// (resetBeforeCredits)
		if who, out, ok := s.resetBeforeCreditsOn(r.Context(), on); ok {
			autoReset = true
			s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1].Reset = &AutoReset{Who: who, Text: out.Text()} })
			resetNote = "openai (" + who + "): used one of its resets by itself before its credits (" + out.Text() + ")"
		}
	}
	for {
		// the try's own, which a stream that failed before its reply ends
		// (holdWriter.stop)
		attempt, cancel := context.WithCancel(r.Context())
		defer cancel()
		for tries := 0; ; tries++ {
			// a request asked again within the try (a reset spent, a
			// dropped one, an item taken out) is its Before
			var asking, asked, wrote atomic.Int64
			traced := httptrace.WithClientTrace(attempt, nt.hooks(&asking, &asked, &wrote))
			ctx := provider.ViaSignedIn(traced, "codex")
			if on != nil {
				ctx = on.Via(traced)
			}
			req, err := http.NewRequestWithContext(ctx, r.Method, u, bytes.NewReader(body))
			if err != nil {
				failWith(502, err.Error())
				end(502, err.Error(), 0, 0)
				return
			}
			copyHeaders(req.Header, r.Header)
			if on != nil {
				// the account's own token and id in place of the sign-in's;
				// what Codex said it takes back stays as it said it
				accept := req.Header.Values("Accept")
				if err := on.Sign(ctx, req, provider.Responses, body); err != nil {
					msg := "OpenAI (" + on.Account.User + "): " + err.Error()
					writeError(w, provider.Responses, 502, msg)
					end(502, msg, 0, 0)
					return
				}
				// a sign-in its vendor revoked isn't sent again (#1793)
				if why, ok := provider.RevokedSignIn(on.Account, req); ok {
					writeError(w, provider.Responses, 401, why)
					end(401, why, 0, 0)
					return
				}
				req.Header.Del("Accept")
				for _, v := range accept {
					req.Header.Add("Accept", v)
				}
			}
			// left to the transport, the reply comes back plain for the usage in it
			req.Header.Del("Accept-Encoding")
			// a turn to ChatGPT goes zstd-compressed again, as Codex sent it
			// before magpie read it (ylorn on X); an API key's goes plain
			sent = provider.CodexCompressed(req, body)
			// counted for the request's ledger row, as forwardOnce's are (#4456)
			sentUp(r.Context(), sent, len(body))
			res, err = s.client.Do(req)
			res = gotDown(r.Context(), res)
			if err != nil {
				msg := "OpenAI: " + err.Error()
				if overload && busyAgain < lastRetries && droppedUnanswered(r.Context(), err) {
					// the connection closed with nothing of a reply (a
					// proxy's or ChatGPT's front's "EOF" on a fresh
					// connection, which Go doesn't send again by itself):
					// asked again after a moment on a new one, as the
					// accounts loop asks its last account again after the
					// same 502 (aetrox333 on Discord: gpt-6-luna failed
					// in 43ms every time, auto/gpt-6-luna never)
					if !askAgain(502, msg, retryPause<<busyAgain) {
						return
					}
					continue
				}
				if rest == "/responses" {
					msg = codexUnreached(modelOf(body), err)
				}
				failWith(502, msg)
				end(502, msg, 0, 0)
				return
			}
			if on != nil {
				// one of magpie's accounts: a revoked sign-in shows on its
				// row, to sign in again (#1793)
				noteSignIn(ctx, on.Account, req, res)
			}
			if !creditsAsked && rest == "/responses" && base != codexAPIBase && res.StatusCode == http.StatusTooManyRequests && relayOnCredits(on) {
				// the account holds credits and was refused as out of its
				// allowance: ChatGPT may turn away the turn that uses the
				// window up before it spends them, and answers the next
				// on them (Zhenzhen on Discord). Asked again at once,
				// before a reset is looked at, rather than the turn ended
				creditsAsked = true
				msg, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
				res.Body.Close()
				res.Body = io.NopCloser(bytes.NewReader(msg))
				if failure(res.StatusCode, msg) == failQuota {
					s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1].Credits = true })
					continue
				}
			}
			if !autoReset && rest == "/responses" && base != codexAPIBase && res.StatusCode == http.StatusTooManyRequests {
				// the account is out of its allowance: one it lets spend its
				// resets by itself, its week used up, spends one and is asked
				// again — nobody else is there to ask
				autoReset = true
				msg, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
				res.Body.Close()
				res.Body = io.NopCloser(bytes.NewReader(msg))
				if failure(res.StatusCode, msg) == failQuota {
					if who, out, ok := s.autoResetSignedIn(r.Context()); ok {
						s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1].Reset = &AutoReset{Who: who, Text: out.Text()} })
						resetNote = "openai (" + who + "): used one of its resets by itself (" + out.Text() + ")"
						continue
					}
				}
			}
			if rest == "/responses" && base != codexAPIBase && edgeMayDrop(res.StatusCode, sent) && resets < lastRetries {
				// ChatGPT closed the connection before answering a large
				// request, which often passes (#1628), or its front couldn't
				// retry one that large (#2034): asked again after a moment,
				// as the accounts loop asks its last one again
				head, _ := io.ReadAll(io.LimitReader(res.Body, 4<<10))
				res.Body.Close()
				res.Body = io.NopCloser(bytes.NewReader(head))
				if edgeSaidDrop(res.StatusCode, head) {
					wait := retryPause << resets
					resets++
					select {
					case <-time.After(wait):
						continue
					case <-r.Context().Done():
					}
				}
			}
			if overload && busyAgain < lastRetries && res.StatusCode >= 400 {
				// the backend too busy for the turn, said as its status
				msg, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
				res.Body.Close()
				res.Body = io.NopCloser(bytes.NewReader(msg))
				if codexBusy(res.StatusCode, msg) {
					if wait, ok := passing(res.StatusCode, res.Header, msg, busyAgain); ok {
						if !askAgain(res.StatusCode, provider.APIError(msg, res.Status), wait) {
							return
						}
						continue
					}
				}
			}
			if rest != "/responses" || tries >= 3 || (res.StatusCode != 400 && res.StatusCode != 404) {
				break
			}
			// an item OpenAI can't take — sealed by another account, or
			// another vendor's that it looks up and doesn't have — is taken
			// out and the rest asked again, rather than the conversation
			// stuck on it for good
			msg, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
			res.Body.Close()
			res.Body = io.NopCloser(bytes.NewReader(msg))
			b, ok := withoutUnreadable(body, msg)
			if !ok {
				break
			}
			body = b
		}
		if rest == "/responses" {
			if acct, ok := codexRelayedAccount(base, r.Header, on); ok {
				// the windows the reply tells of the account it went out on
				// are kept as its usage, and a full one has it read again at
				// once, as a turn served through magpie's accounts has: with
				// Codex's sign-in the only account, every turn comes this way,
				// and the Codex app was moved off a used-up account only at
				// the next look, minutes on, Send greyed until it was
				// restarted (unsplashz on Discord)
				heardCodexLimits(acct, res)
				relayedAcct = acct.Account.User
			}
			// a web page or nothing at all, served 200, is the 502 it stands
			// for (#1012)
			res = notAnAPIReply(res, "OpenAI: ")
			if base != codexAPIBase {
				// still closed on after that: Codex is told what may help,
				// at the 503 it asks again for (#1628)
				res = codexDropped(res, sent)
			}
		}
		defer res.Body.Close()
		if base == codexAPIBase && res.StatusCode == http.StatusUnauthorized {
			// Codex signed in with an API key OpenAI refuses — often one a
			// relay issued, left in ~/.codex/auth.json — and one of Codex's own
			// models was picked, which goes out with Codex's own sign-in
			msg, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
			writeError(w, provider.Responses, res.StatusCode, codexKeyRefused(msg))
			s.record(Call{Route: routeOf(tr), Time: start, From: provider.Responses, To: provider.Responses, Model: modelOf(body),
				Provider: "openai", Agent: callerOf(r).agent, Status: res.StatusCode,
				Millis: time.Since(start).Milliseconds(), Error: res.Status, headers: headerLogOf(r.Context())})
			end(res.StatusCode, res.Status, 0, 0)
			return
		}
		if (rest == "/responses" || rest == "/responses/compact") && res.StatusCode >= 400 {
			msg, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
			res.Body = io.NopCloser(bytes.NewReader(msg))
			// its credits and resets looked at above, a 429 that names
			// when the account is back holds it till then (codexHolds)
			noteCodexHold(holdKey, res.StatusCode, res.Header, msg)
			if overload && codexBusy(res.StatusCode, msg) && s.codexFallsBack(r, body) {
				// still too busy once asked again: the fallbacks answer
				said := provider.APIError(msg, res.Status)
				handOn(res.StatusCode, said, 0)
				end(res.StatusCode, said, 0, 0)
				s.codexFallBack(w0, r, body0, kept)
				return
			}
			if overload && codexAccountOut(res.StatusCode, msg) && s.codexFallsBack(r, body) {
				// the account out of its allowance (its credits and resets
				// looked at above): the fallbacks answer, as they do for a
				// turn through magpie's accounts, rather than Codex handed
				// the refusal (adorable_piglet on Discord)
				said := provider.APIError(msg, res.Status)
				handOut(res.StatusCode, said, msg)
				end(res.StatusCode, said, 0, 0)
				s.codexFallBack(w0, r, body0, kept)
				return
			}
			if said, ok := codexSaidNothing(rest, modelOf(body), codexWho(base, on), res, msg); ok {
				// a failure with nothing said of it, which Codex shows as
				// "Unknown error" alone (#409), or a web page Codex shows
				// as its markup: said where the turn, or the compaction
				// (outrageous_piglet on Discord), went
				failWith(res.StatusCode, said)
				s.record(Call{Route: routeOf(tr), Time: start, From: provider.Responses, To: provider.Responses, Model: modelOf(body),
					Provider: "openai", Agent: callerOf(r).agent, Kind: callKind(r.Header), Status: res.StatusCode,
					Millis: time.Since(start).Milliseconds(), Error: said, headers: headerLogOf(r.Context())})
				end(res.StatusCode, said, 0, 0)
				return
			}
		}
		// a stream that may still be asked again, or whose Codex has the
		// stream's headers already, goes through a holdWriter: the frames
		// before a reply held, its reasoning and text let through as they
		// come, an error before them held for the turn to be asked again
		out := w
		var hw *holdWriter
		if overload && streams && (busyAgain < lastRetries || kept.sent || s.codexFallsBack(r, body)) {
			hw = newHoldWriter(w, true)
			hw.alive, hw.streams, hw.ctx, hw.stop = kept, true, r.Context(), cancel
			// reasoning goes to Codex at once, as it always went
			hw.thinkingShown = true
			out = hw
		}
		for k, vs := range res.Header {
			if !hopHeader(k) {
				out.Header()[k] = vs
			}
		}
		if hw != nil && res.StatusCode < 400 && res.Header.Get("Content-Type") == "" {
			// the ChatGPT backend streams without saying so: held as the
			// stream it is
			out.Header().Set("Content-Type", "text/event-stream")
		}
		modelsEtag(out.Header())
		out.WriteHeader(res.StatusCode)
		stopWatch := func() {}
		if hw != nil {
			stopWatch = hw.watch()
		}
		if rest == "/responses" {
			ct := res.Header.Get("Content-Type")
			if ct == "" && streams { // the ChatGPT backend streams without saying so
				ct = "text/event-stream"
			}
			sniff = newSniffer(provider.Responses, ct)
		}
		f, _ := out.(http.Flusher)
		buf := make([]byte, 32<<10)
		refusal = nil
		readErr, writeErr, passed = nil, nil, 0
		for {
			n, err := res.Body.Read(buf)
			if err != nil && err != io.EOF {
				readErr = err
			}
			if n > 0 {
				if captureTitle {
					titleReply.add(buf[:n])
				}
				if sniff != nil {
					sniff.write(buf[:n])
					first.see(buf[:n])
					tools.add(buf[:n])
				}
				if res.StatusCode >= 400 && len(refusal) < 8<<10 {
					refusal = append(refusal, buf[:n]...)
				}
				m, werr := out.Write(buf[:n])
				passed += int64(m)
				live.observe(buf[:m])
				if werr != nil {
					writeErr = werr
					break
				}
				if f != nil {
					f.Flush()
				}
			}
			if err != nil {
				break
			}
		}
		stopWatch()
		if hw == nil {
			break
		}
		hw.settle()
		if hw.mayAskAgain() && hw.failure != 0 && !hw.refused && codexBusy(hw.failure, hw.errBody()) && r.Context().Err() == nil {
			// the backend too busy for the turn, said on its stream before
			// any of the reply (#1418)
			said := "OpenAI: " + string(hw.errBody())
			if wait, ok := passing(hw.failure, res.Header, hw.errBody(), busyAgain); ok {
				res.Body.Close()
				if !askAgain(hw.failure, said, wait) {
					return
				}
				continue
			}
			if s.codexFallsBack(r, body) {
				res.Body.Close()
				handOn(hw.failure, said, 0)
				end(hw.failure, said, 0, 0)
				s.codexFallBack(w0, r, body0, kept)
				return
			}
		}
		if hw.mayAskAgain() && hw.failure != 0 && !hw.refused && codexAccountOut(hw.failure, hw.errBody()) && r.Context().Err() == nil && s.codexFallsBack(r, body) {
			// the account out of its allowance, said on its stream before
			// any of the reply: the fallbacks answer
			said := "OpenAI: " + string(hw.errBody())
			res.Body.Close()
			handOut(hw.failure, said, hw.errBody())
			end(hw.failure, said, 0, 0)
			s.codexFallBack(w0, r, body0, kept)
			return
		}
		hw.release()
		break
	}
	if sniff == nil {
		return
	}
	call := Call{Time: start, From: provider.Responses, To: provider.Responses, Model: modelOf(body),
		Provider: "openai", Agent: callerOf(r).agent, Kind: kind, Status: res.StatusCode,
		Millis: time.Since(start).Milliseconds(), Fallback: resetNote, headers: headerLogOf(r.Context())}
	if len(tried) > 0 {
		// the overloads it was asked again for, before the one that answered
		call.Fallback = strings.Join(append(tried, call.Fallback), "; ")
		call.Fallback = strings.TrimSuffix(call.Fallback, "; ")
	}
	call.TTFT, call.FirstText = first.ms()
	uu.add(sniff.usage())
	call.ReasoningHidden = first.hidesReasoning(provider.Responses, uu.Reasoning)
	uu.Tier = servedTierAt(provider.HostOf(base), uu.Tier)
	call.Flow = first.flowFor(uu.Reasoning)
	call.Usage, served = uu, uu.Served
	errType := ""
	if res.StatusCode >= 400 {
		// its words, the status in front when it said none: the log and
		// the Routing view show why, not just that
		call.Error = provider.APIError(refusal, res.Status)
		errType = provider.ErrorType(refusal)
		if relayedAcct != "" && failure(res.StatusCode, refusal) == failQuota {
			// the account it went out on refused the turn as out of its
			// allowance: read again now, so magpie takes the turns over
			// (codex.out) and the Codex app is told without a restart
			codexMayBeUsedUp(relayedAcct)
		}
	} else if r.Context().Err() != nil && !sniff.whole() {
		// Codex went away mid-reply (Esc, a closed window): nobody
		// failed, and the row says it was canceled, as a routed turn's
		// does, not a clean 200 (#4866)
		call.Status, call.Error = canceled(r.Context())
	} else if sniff.failed != "" {
		// the backend's response.failed (or error event) under a 200:
		// what it said, as serve tells a routed reply's (#4866)
		call.Error, errType = sniff.failed, uu.ErrType
	} else if said := codexStreamCut(res, sniff, began, r.Context()); said != "" {
		// the stream's bytes stopped mid-reply, no failure said and no
		// last event reached: the client still there hears it as the
		// stream's failure, not a bare EOF to puzzle over, and the row
		// says why instead of a clean 200 with no usage (#3377)
		call.Status, call.Error = http.StatusBadGateway, said
		// after a blank line, so a line the cut left half written can't
		// take the events streamError writes into its data (#2446, #2580)
		w.Write([]byte("\n\n"))
		streamError(w, provider.Responses, call.Model, call.Status, said)
	}
	if line := codexEndedEarly(res, sniff, call.Model, callerOf(r).agent, began, passed, readErr, writeErr, r.Context()); line != "" {
		log.Print(line)
	}
	live.finish(call.Status >= 400, uu.Output)
	end(call.Status, call.Error, uu.Input+uu.Output+uu.CacheRead+uu.CacheWrite, uu.Output)
	call.Route = tr.ID
	call.Masked = s.trace.maskedOf(tr)
	s.record(call)
	servedProvider, servedAccount := codexServed(base, r.Header, on)
	rec := usage.Record{RouteID: tr.ID, Time: start, Agent: call.Agent,
		Provider: servedProvider, Host: provider.HostOf(base), ProviderAccount: servedAccount, Model: call.Model,
		Requested: call.Model, Served: served,
		Input: uu.Input, Output: uu.Output, CacheRead: uu.CacheRead, CacheWrite: uu.CacheWrite,
		Reasoning: uu.Reasoning, Effort: effort, Millis: call.Millis, Masking: maskingMs(r.Context(), 0),
		// Codex's own sign-in: the request goes as Codex sent it
		TierAsked: bodyTier(provider.Responses, body), TierSent: bodyTier(provider.Responses, body), TierServed: uu.Tier, TTFT: call.TTFT, FirstText: call.FirstText, ReasoningHidden: call.ReasoningHidden, Flow: call.Flow, Status: call.Status, Session: sessionOf(r.Header), NativeSession: nativeSessionOf(r.Header), Kind: call.Kind,
		RequestID: requestID(res.Header), ResponseID: uu.ResponseID, Endpoint: r.URL.Path}
	failedWith(&rec, call.Status, call.Error, errType)
	if up := upBytesOf(r.Context()); up != nil {
		rec.UpBytes, rec.PlainBytes, rec.Socket = up.wire, up.plain, up.socket
		rec.DownBytes = up.downOf()
	}
	appendUsage(r, rec)
}

// codexBusy says a failure of the ChatGPT backend's, said as a status or
// on a stream before any reply, is it too busy for the turn (busy): no
// fault of the account's, nor of the request's. Its front closing on the
// request is no overload: a large request's has its own retries
// (edgeSaidDrop, codexDropped), and a small one's goes to Codex as the
// 503 it asks again for (TestCodexResetJudgedByTheBytesSent). A 429 is
// the account's allowance or rate, which isn't outwaited in seconds.
func codexBusy(status int, msg []byte) bool {
	if status == http.StatusTooManyRequests || edgeSaidDrop(status, msg) || codexReset(status, msg) {
		return false
	}
	return busy(status, msg)
}

// codexRouteUnmasked says a turn relayed to the ChatGPT backend on the
// account on, or on Codex's own sign-in (the codex subscription) when on
// is nil, goes there as written: the user set that subscription to go
// unmasked, and every fallback of it too (unredactedRoute).
func codexRouteUnmasked(on *provider.Provider) bool {
	if on != nil {
		return unredactedRoute(*on, false, nil)
	}
	p, err := provider.Find("codex")
	if err != nil || p.Account == nil || p.Account.Agent != "codex" {
		return false
	}
	return unredactedRoute(*p, false, nil)
}

// codexAccountOut says the ChatGPT backend turned away a turn on Codex's
// own sign-in for the account, not the request: out of its allowance
// (usage_limit_reached, a plan without Codex), of its credits, or rate
// limited — what a turn through magpie's accounts goes on past to the
// next. A 401 is never one: Codex renews its own sign-in on it.
func codexAccountOut(status int, body []byte) bool {
	if status == http.StatusUnauthorized {
		return false
	}
	if status == http.StatusForbidden && bytes.Contains(body, []byte("usage_not_included")) {
		return true
	}
	switch failure(status, body) {
	case failQuota, failCredit, failRate:
		return true
	}
	return false
}

// codexFallsBack says Codex's turn on one of its own models has fallbacks
// to go on to (codexFallBack): the Codex subscription's, set in magpie, at
// least one of them found, or a routing group the user built with the
// model in it (provider.GroupsAfter). A turn compacting natively is the backend's to
// answer, and goes nowhere else.
//
// Codex's auto-review on codex-auto-review falls back too where its
// conversation's turns would (codexReviewAfter): no group has that model,
// so with the account out and Codex routed on through a group, every
// review was handed the refusal and failed while the turns went on
// (riverleaf88 on X).
func (s *Server) codexFallsBack(r *http.Request, body []byte) bool {
	model := modelOf(body)
	if model == "" || strings.Contains(model, "/") || compactionTriggerIn(body) {
		return false
	}
	p, _, ok := provider.Resolve("codex/" + model)
	if !ok || p.Account == nil || p.Account.Agent != "codex" {
		return false
	}
	return len(provider.FallbackModels(p)) > 0 || len(provider.GroupsAfter(p.ID+"/"+model)) > 0 ||
		len(s.codexReviewAfter(r, p, model, body)) > 0
}

// codexReviewAfter is where Codex's auto-review on codex-auto-review goes
// on to when the Codex subscription can't take it (the account out of its
// allowance, every account held at its cap), after the subscription's own
// fallbacks: where the turns of the conversation it reviews go on to.
// That conversation on one of magpie's models, the model itself, when
// magpie serves it to this caller (servedSession); on one of Codex's own,
// the routing groups the user built with that model in them
// (provider.GroupsAfter), which a turn on it goes on to. Codex asks its
// reviewer by OpenAI's name whatever the conversation is on (codex-rs
// guardian reviewer_config.rs, approval_review_preferred_model), and only
// the ChatGPT backend serves it. nil for any other request.
func (s *Server) codexReviewAfter(r *http.Request, p provider.Provider, model string, body []byte) []string {
	lead := s.codexReviewLead(r, p, model, body)
	if lead == "" {
		return nil
	}
	if strings.Contains(lead, "/") {
		to := s.servedSession(r, lead)
		if to == "" {
			return nil
		}
		if q, _, ok := provider.Resolve(to); ok && q.ID == p.ID && !provider.IsGroupRef(to) {
			// the subscription itself, under another name: out too
			return nil
		}
		return []string{to}
	}
	return provider.GroupsAfter(p.ID + "/" + lead)
}

// codexReviewLead is the model the conversation Codex's auto-review on
// codex-auto-review through p, the Codex subscription, reviews is on: one
// of Codex's own by its bare name, or one of magpie's ids; "" for any
// other request, or a conversation magpie has seen no turn of.
func (s *Server) codexReviewLead(r *http.Request, p provider.Provider, model string, body []byte) string {
	if p.ID != "codex" || p.Account == nil || p.Account.Agent != "codex" || !strings.EqualFold(strings.TrimSpace(model), provider.CodexAutoReview) {
		return ""
	}
	lead, _, _ := s.codexLeads.lookup(r, body)
	lead = strings.TrimPrefix(lead, "codex/")
	if strings.EqualFold(lead, model) {
		return ""
	}
	return lead
}

// codexFallBack serves Codex's turn on its own sign-in, which the backend
// was too busy for however often it was asked again, from the Codex
// subscription's fallbacks, as serve goes on to them for a turn through
// magpie's accounts; the account itself is not asked again (handedOnOf).
// kept is what Codex was sent while it waited: the stream's headers, once
// it had them, which the fallback's reply follows.
func (s *Server) codexFallBack(w http.ResponseWriter, r *http.Request, body []byte, kept *keptAlive) {
	id := "codex/" + modelOf(body)
	body, _ = codexInputAs(withModel(body, id), true, s.nativeCompactionFor(id))
	s.serve(w, r.WithContext(context.WithValue(r.Context(), handedOnKey{}, kept)), provider.Responses, body)
}

// handedOnKey carries, in a request codexFallBack hands on, what its
// agent was sent while it waited.
type handedOnKey struct{}

// handedOnOf is what the agent of a request codexFallBack handed on was
// sent while it waited, nil for any other request.
// pickedKey marks a request whose model magpie put a provider on
// (codexAccounts' codex/<model>), the client having asked it bare.
type pickedKey struct{}

func providerPicked(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), pickedKey{}, true))
}

// namedProvider is whether the client named the provider of the model it
// asked for ("claude/claude-opus-5-5", a flat or aliased spelling of one),
// rather than a bare model magpie put on a provider. A provider named so
// is the one asked: past its usage cap it goes on to its own fallbacks
// only, never to a routing group that merely has the model in it
// (Anotiaww on X).
func namedProvider(r *http.Request, asked string) bool {
	picked, _ := r.Context().Value(pickedKey{}).(bool)
	return strings.Contains(asked, "/") && !picked
}

func handedOnOf(r *http.Request) *keptAlive {
	k, _ := r.Context().Value(handedOnKey{}).(*keptAlive)
	return k
}

// fallbacksOnly is cands without model's own on provider id, pl with
// them: a request handed on to the fallbacks (codexFallBack) after the
// account was asked as often as it is asked.
func fallbacksOnly(cands []candidate, pl planned, id, model string) ([]candidate, planned) {
	own := func(c candidate) bool { return c.p.ID == id && c.model == model }
	var kept []candidate
	var order []Weighed
	for i, c := range cands {
		if !own(c) {
			kept = append(kept, c)
			order = append(order, pl.order[i])
		}
	}
	cands, pl.order = kept, order
	pl.held = slices.DeleteFunc(slices.Clone(pl.held), own)
	pl.capped = slices.DeleteFunc(slices.Clone(pl.capped), own)
	var unlisted []candidate
	var unlistedW []Weighed
	for i, c := range pl.unlisted {
		if !own(c) {
			unlisted = append(unlisted, c)
			unlistedW = append(unlistedW, pl.unlistedW[i])
		}
	}
	pl.unlisted, pl.unlistedW = unlisted, unlistedW
	return cands, pl
}

// codexServed is who a relay through codexUpstreamOn is recorded as.
// Codex's own ChatGPT sign-in is the codex account, and a compaction
// signed by another of its accounts (compactOn) is that account, so the
// account card's window counts the call. A call sent to OpenAI's API
// with Codex's key stays openai with no account. With no sign-in to
// name, or a request whose chatgpt-account-id isn't the sign-in's, it
// stays openai too, rather than a codex row that names the wrong account.
// Rows already written are not rewritten.
func codexServed(base string, h http.Header, on *provider.Provider) (id, account string) {
	p, ok := codexRelayedAccount(base, h, on)
	if !ok {
		return "openai", ""
	}
	return p.ID, p.Account.User
}

// codexRelayedAccount is the ChatGPT account of magpie's that a relay
// through codexUpstreamOn went out on: the one it was signed with (on),
// or Codex's own sign-in when the request names it; false for a call to
// OpenAI's API, or one signed by an account magpie doesn't have.
func codexRelayedAccount(base string, h http.Header, on *provider.Provider) (provider.Provider, bool) {
	if base != provider.CodexBase || apiKey(h) {
		return provider.Provider{}, false
	}
	if on != nil && on.Account != nil && on.Account.User != "" {
		return *on, true
	}
	// the token went out as Codex sent it: it is the sign-in magpie reads
	// only when it names the same ChatGPT account. A Codex in WSL, or
	// under another CODEX_HOME, signs with its own, which isn't named here
	if acct := h.Get("chatgpt-account-id"); acct != "" && acct != provider.CodexSignedInAccount() {
		return provider.Provider{}, false
	}
	p, err := provider.Find("codex")
	if err != nil || p.Account == nil || p.Account.User == "" {
		return provider.Provider{}, false
	}
	return *p, true
}

// unreadableItem is the item a refusal names: sealed content OpenAI
// can't verify, or an id it doesn't have ("Item with id 'rs_…' not
// found"), which a relay in front of one answers in words of its own
// ("Referenced reasoning item 'rs_…' was not found or has expired",
// #3062).
var unreadableItem = lazyre.New(`(?i)(?:encrypted content for item|item with id|referenced reasoning item) '?([A-Za-z]+_[A-Za-z0-9_-]+)'?`)

// withoutUnreadable is body without what OpenAI's refusal msg says it
// can't read: the item it names, what another account sealed
// (withoutSeals: a web search's too, #3887), or a member of Codex's
// passthrough it doesn't know.
func withoutUnreadable(body, msg []byte) ([]byte, bool) {
	if f, ok := refusedPassthrough(msg); ok {
		// a member of Codex's passthrough the backend doesn't know yet
		// (passthrough_refused.go)
		return withoutPassthrough(body, f)
	}
	if !foreignReasoning.Match(msg) && !bytes.Contains(bytes.ToLower(msg), []byte("not found")) {
		return nil, false
	}
	if m := unreadableItem.FindSubmatch(msg); m != nil {
		if b, ok := withoutItem(body, string(m[1])); ok {
			return b, true
		}
	}
	if foreignReasoning.Match(msg) {
		return withoutSeals(body)
	}
	return nil, false
}

// withoutItem is a Responses request without the input item of this id.
func withoutItem(body []byte, id string) ([]byte, bool) {
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
			ID string `json:"id"`
		}
		if json.Unmarshal(it, &t) == nil && t.ID == id {
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

// codexKeyRefused says why a model of Codex's own failed with 401: what
// OpenAI said, and what to do about it.
func codexKeyRefused(msg []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	said := strings.TrimSpace(string(msg))
	if json.Unmarshal(msg, &e) == nil && e.Error.Message != "" {
		said = e.Error.Message
	}
	return "OpenAI refused the API key Codex is signed in with (" + said + "). " +
		"This is one of Codex's own models, which goes to OpenAI with Codex's own sign-in: " +
		"pick one of magpie's models in Codex (provider/model, or set it in magpie's Agents view), " +
		"or sign Codex in with ChatGPT, or with an OpenAI API key"
}

// droppedUnanswered says whether err, a request's that got no reply, is
// one that may pass when asked again: the connection closed or reset
// before a byte of a reply, or couldn't be made. Not the caller gone, nor
// a wait for the reply that ran out, which a second try would wait out
// again.
func droppedUnanswered(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var ne interface{ Timeout() bool }
	return !errors.As(err, &ne) || !ne.Timeout()
}

// codexUnreached says why a model of Codex's own failed with 502: OpenAI,
// where it goes with Codex's own sign-in, couldn't be reached — often from
// a Codex whose only sign-in is a relay's key, whose own models it still
// lists (#322) — and what to do about it.
func codexUnreached(model string, err error) string {
	return "OpenAI can't be reached (" + err.Error() + "). " + model + " is one of Codex's own models, " +
		"which goes to OpenAI with Codex's own sign-in: pick one of magpie's models in Codex " +
		"(provider/model, or set it in magpie's Agents view), or let Codex reach OpenAI"
}

// codexFailedEmpty says why a model of Codex's own failed when OpenAI gave
// an error status and nothing else: where it went, and what to do — a
// Codex left on its own model while magpie's Agents view picked another
// for it sends the turn to OpenAI, not to that one (#409).
func codexFailedEmpty(model, status string) string {
	return "OpenAI answered " + status + " and said nothing more. " + model + " is one of Codex's own models, " +
		"which goes to OpenAI with Codex's own sign-in, not to a provider in magpie: pick one of magpie's models in Codex " +
		"(provider/model, or set it in magpie's Agents view), or try again later"
}

// codexWho names who a request relayed to OpenAI went to: Codex's own
// sign-in, its API key, or one of magpie's ChatGPT accounts (on).
func codexWho(base string, on *provider.Provider) string {
	switch {
	case on != nil && on.Account != nil && on.Account.User != "":
		return "OpenAI (" + on.Account.User + ")"
	case base == codexAPIBase:
		return "OpenAI's API"
	}
	return "OpenAI"
}

// codexSaidNothing is what Codex is told of a failure OpenAI answered a
// turn or a compaction with that it can't show: an empty body, which
// Codex shows as "unexpected status 502 Bad Gateway: Unknown error"
// (#409, outrageous_piglet on Discord: a compaction), or a web page in
// place of an API error, which it shows as markup. ok is false for an
// error Codex reads as it came.
func codexSaidNothing(rest, model, who string, res *http.Response, msg []byte) (string, bool) {
	t := bytes.TrimSpace(msg)
	page := len(t) > 0 && (htmlStart.Match(t) || strings.HasPrefix(strings.ToLower(res.Header.Get("Content-Type")), "text/html"))
	if len(t) > 0 && !page {
		return "", false
	}
	if rest == "/responses" && len(t) == 0 && who == "OpenAI" {
		return codexFailedEmpty(model, res.Status), true
	}
	what := "this turn"
	if rest == "/responses/compact" {
		what = "this compaction"
	}
	said := " and said nothing more"
	if page {
		said = " with a web page in place of an API error"
		if title := provider.PageTitle(msg); title != "" {
			said += " (“" + title + "”)"
		}
	}
	return who + " answered " + what + " for " + model + " " + res.Status + said +
		". " + model + " is one of Codex's own models, which goes to OpenAI, not to a provider in magpie; try again later, " +
		"or pick one of magpie's models in Codex (provider/model, or set it in magpie's Agents view)", true
}

// codexStreamCut says when a relayed stream was cut mid-reply: an SSE reply
// whose bytes stopped with no failure said and no last event reached, its
// client still there to hear of it. Left alone the reply just ends: Codex
// read a bare EOF, "stream disconnected before completion", and the row
// recorded a clean 200 with nothing of why — every reply the ChatGPT
// backend cut about two minutes in said nothing else for days (#3377).
// The text carries how long the stream had gone, so a cut's fixed shape
// shows in the log.
func codexStreamCut(res *http.Response, sniff *usageSniffer, began time.Time, ctx context.Context) string {
	if res.StatusCode >= 400 || !sniff.sse || sniff.failed != "" || sniff.whole() || ctx.Err() != nil {
		return ""
	}
	return "OpenAI closed the stream " + time.Since(began).Round(time.Second).String() + " into the reply, " +
		"before its last event: response.completed never came, and the usage with it. " +
		"The reply is lost, and asking again sends the turn whole"
}

// codexEndedEarly is the app.log line of a relayed Codex stream that
// ended before its last event, "" for one that came whole or failed with
// a status: which side ended it, and how. Codex says only what its HTTP
// client says of a connection that broke under a reply, "error decoding
// response body" (reqwest's words for any failed read of a body), so the
// log tells a reply OpenAI broke off (its read's error, or a clean end
// before response.completed) from a client that went away or couldn't be
// written to (wjh66 on Discord: "reconnecting 5/5", then that error, and
// nothing in app.log).
func codexEndedEarly(res *http.Response, sniff *usageSniffer, model, agent string, began time.Time, passed int64, readErr, writeErr error, ctx context.Context) string {
	if res == nil || sniff == nil || res.StatusCode >= 400 {
		return ""
	}
	if sniff.sse && sniff.whole() {
		return ""
	}
	// a body not a stream's has no last event to miss: only a read or a
	// write that failed, or a client gone, ended it early
	if !sniff.sse && readErr == nil && writeErr == nil && ctx.Err() == nil {
		return ""
	}
	agent = cmp.Or(agent, "the client")
	upstream := "OpenAI ended the stream"
	if readErr != nil {
		upstream = "reading OpenAI's stream failed (" + readErr.Error() + ")"
	}
	var client string
	switch {
	case writeErr != nil:
		client = "writing to " + agent + " failed (" + writeErr.Error() + ")"
	case ctx.Err() != nil:
		client = agent + " went away (" + context.Cause(ctx).Error() + ")"
	case sniff.failed != "":
		client = agent + " was passed OpenAI's failure"
	default:
		client = agent + " was told it as response.failed"
	}
	before := "before its last event"
	if !sniff.sse {
		before = "before its end"
	}
	return fmt.Sprintf("gateway: codex: the reply of %s ended %s in, %s, %d bytes passed on: %s; %s",
		cmp.Or(model, "(no model)"), time.Since(began).Round(time.Millisecond), before, passed, upstream, client)
}

// routeOf is the id of the route a Codex relay opened, so the call it
// records is the one the Routing page's "Request and response" opens (#2481);
// 0 for a compaction, which opens none.
func routeOf(tr *Route) int64 {
	if tr == nil {
		return 0
	}
	return tr.ID
}

// apiKey reports whether Codex signed in with an API key rather than a
// ChatGPT account.
func apiKey(h http.Header) bool {
	return strings.HasPrefix(strings.TrimPrefix(h.Get("Authorization"), "Bearer "), "sk-")
}

func hopHeader(k string) bool {
	switch http.CanonicalHeaderKey(k) {
	case "Connection", "Keep-Alive", "Proxy-Connection", "Transfer-Encoding", "Upgrade", "Te", "Trailer", "Content-Length":
		return true
	}
	return false
}

// codexHeader is one of the headers Codex tells the ChatGPT backend about
// a request by, which go on with it when a pool account signs it as they
// do when Codex's own sign-in does (codexUpstream): x-openai-subagent (a
// guardian review, a thread's title, memories, a compaction…), the turn's
// x-codex-turn-metadata, its installation, window and parent thread, and
// the session. Never a sign-in: the account's own goes in its place, and
// Codex's device attestation is its own sign-in's. Nor what holds for
// Codex's own account alone: x-openai-codex-luna-reserve, which says its
// plan's allowance is used up (another account's may not be), and
// x-codex-turn-state, the backend's routing of the turn for that account.
func codexHeader(k string) bool {
	k = strings.ToLower(k)
	if strings.Contains(k, "authorization") || k == "x-oai-attestation" ||
		k == "x-openai-codex-luna-reserve" || k == "x-codex-turn-state" {
		return false
	}
	switch k {
	case "session-id", "thread-id", "session_id", "conversation_id", "x-client-request-id", "version":
		return true
	}
	return strings.HasPrefix(k, "x-openai-") || strings.HasPrefix(k, "x-codex-")
}

// callKind is what an agent made a call for when it isn't a turn of the
// conversation, as Codex names it in x-openai-subagent: "guardian" (auto
// review of an approval), "review", "compact", "memory_consolidation",
// "thread_title", "collab_spawn"… A turn Codex sends on Luna Reserve, once
// the plan's own allowance is used up, is "luna_reserve". A call Codex
// makes on a hidden thread of its own goes without x-openai-subagent: its
// x-codex-turn-metadata names the thread's source instead — "thread_title"
// for the title of a new chat (#314), "guardian_review". A web search
// magpie runs for a model that can't search is "web_search". DSH marks its
// compaction with x-deepseek-harness-compact: "compact" too.
func callKind(h http.Header) string {
	v := strings.TrimSpace(h.Get("x-openai-subagent"))
	if v == "" && h.Get("x-openai-memgen-request") != "" {
		v = "memgen"
	}
	if v == "" {
		v = threadSource(h.Get("x-codex-turn-metadata"))
	}
	if v == "" && dshCompacts(h) {
		v = KindCompact // DSH's compaction of its context
	}
	if v == "" && h.Get("User-Agent") == SearchAgent {
		v = "web_search"
	}
	if v == "" && h.Get("User-Agent") == VisionAgent {
		v = "vision"
	}
	if v == "" && h.Get("User-Agent") == RouterAgent {
		v = "routing" // a group's classifier, asked before the turn (#1839)
	}
	if v == "" && h.Get("x-openai-codex-luna-reserve") != "" {
		v = "luna_reserve"
	}
	if len(v) > 40 {
		v = v[:40]
	}
	return v
}

// threadSource is the source Codex's turn metadata gives the thread a call
// was made on, when that isn't the user's conversation or a subagent's
// (which x-openai-subagent names): a feature's own thread, as Codex's
// ThreadSource has it — "thread_title", "guardian_review",
// "memory_consolidation".
func threadSource(meta string) string {
	if !strings.Contains(meta, "thread_source") {
		return ""
	}
	var m struct {
		Source string `json:"thread_source"`
	}
	if json.Unmarshal([]byte(meta), &m) != nil {
		return ""
	}
	switch s := strings.TrimSpace(m.Source); s {
	case "", "user", "subagent":
		return ""
	default:
		return s
	}
}

func copyHeaders(dst, src http.Header) {
	for k, vs := range src {
		// what a proxy in front of magpie says of the caller isn't the
		// ChatGPT backend's (proxyIdentity)
		if !hopHeader(k) && !proxyIdentity(k) && http.CanonicalHeaderKey(k) != "Host" && http.CanonicalHeaderKey(k) != AccountHeader {
			dst[k] = vs
		}
	}
}

// codexModelsWait is how long the ChatGPT backend is given for its model
// list. Codex gives the whole request 5 s (MODELS_REFRESH_TIMEOUT in its
// models endpoint) and then keeps the list it was built with, magpie's
// models nowhere in it; a backend slow to answer, or not reachable at all
// on a network that drops chatgpt.com's packets rather than refusing
// them, held magpie's answer past that (#539). A var so tests can say.
var codexModelsWait = 3 * time.Second

// codexListFor is how long the backend's list is handed out again without
// asking it; past that it is still handed out at once while it is asked
// again (#3734). A var so tests can say.
var codexListFor = time.Minute

// codexListFetch bounds one ask of the backend, which goes on past
// codexModelsWait for the callers after.
const codexListFetch = 30 * time.Second

// codexLists is the backend's last list for each sign-in. Twenty Codex
// app-servers asked it at once, each on its own and each given up at
// codexModelsWait, so a backend slower than that never got a list through
// and Codex fell back to its own metadata, without tool_search for
// magpie's groups, and inlined every Apps tool: ~500K tokens a turn
// (#3734, 1,906 timeouts in a day).
var codexLists = struct {
	sync.Mutex
	at map[string]*codexListed
	// asking is the asks going, for tests to wait on
	asking sync.WaitGroup
}{at: map[string]*codexListed{}}

type codexListed struct {
	body []byte // the list as the backend said it, nil for none yet
	etag string
	got  time.Time
	// deny is a 401 or 403 the last ask got, Codex's to see
	deny     *http.Response
	denyBody []byte
	// asked is closed when the ask going ends; nil when none is
	asked chan struct{}
}

// codexListKey is whose list it is: the backend's, the account's (a
// token renewed is the same account), the version Codex says.
func codexListKey(r *http.Request) string {
	who := r.Header.Get("chatgpt-account-id")
	if who == "" {
		who = r.Header.Get("Authorization")
	}
	return provider.CodexBase + "\x00" + who + "\x00" + r.URL.RawQuery
}

// codexBackendList is the backend's model list for r's sign-in: the last
// one asked when it is fresh, or at once while it is asked again; else
// what an ask brings within codexModelsWait. One ask at a time for each
// sign-in, whoever is waiting on it. body is nil when there is none;
// deny, when the backend refused the sign-in.
func (s *Server) codexBackendList(r *http.Request) (body []byte, etag string, deny *http.Response, denyBody []byte) {
	key := codexListKey(r)
	codexLists.Lock()
	e := codexLists.at[key]
	if e == nil {
		e = &codexListed{}
		codexLists.at[key] = e
	}
	if e.body != nil && time.Since(e.got) < codexListFor {
		defer codexLists.Unlock()
		return e.body, e.etag, nil, nil
	}
	if e.asked == nil {
		// a refusal an ask before had is no answer for this one
		e.asked, e.deny, e.denyBody = make(chan struct{}), nil, nil
		codexLists.asking.Add(1)
		go s.askCodexList(r.Context(), r.Header.Clone(), r.URL.RawQuery, e, e.asked)
	}
	asked, stale := e.asked, e.body != nil
	codexLists.Unlock()
	if !stale {
		t := time.NewTimer(codexModelsWait)
		select {
		case <-asked:
		case <-t.C:
		case <-r.Context().Done():
		}
		t.Stop()
	}
	codexLists.Lock()
	defer codexLists.Unlock()
	return e.body, e.etag, e.deny, e.denyBody
}

// askCodexList asks the backend for its list into e, past the request
// that started it, and closes done.
func (s *Server) askCodexList(rctx context.Context, h http.Header, query string, e *codexListed, done chan struct{}) {
	defer codexLists.asking.Done()
	var body, denyBody []byte
	var etag string
	var deny *http.Response
	defer func() {
		codexLists.Lock()
		if body != nil {
			e.body, e.etag, e.got, e.deny, e.denyBody = body, etag, time.Now(), nil, nil
		} else if deny != nil {
			// a sign-in to renew: the next caller waits on the backend
			// again and sees it, nothing stale handed out meanwhile
			e.body, e.deny, e.denyBody = nil, deny, denyBody
		}
		e.asked = nil
		codexLists.Unlock()
		close(done)
	}()
	u := provider.CodexBase + "/models"
	if query != "" {
		u += "?" + query
	}
	ctx, cancel := context.WithTimeout(provider.ViaSignedIn(context.WithoutCancel(rctx), "codex"), codexListFetch)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return
	}
	copyHeaders(req.Header, h)
	// the whole list, shared by every caller, whatever one of them had
	for _, k := range []string{"Accept-Encoding", "If-None-Match", "If-Modified-Since"} {
		req.Header.Del(k)
	}
	res, err := s.client.Do(req)
	if err != nil {
		return
	}
	b, _ := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	res.Body.Close()
	var list struct {
		Models []any `json:"models"`
	}
	if res.StatusCode < 300 && json.Unmarshal(b, &list) == nil && list.Models != nil {
		body, etag = b, res.Header.Get("ETag")
		// the versions as the backend says them, before any is stamped
		// (see codexcat.V1)
		codexcat.Remember(list.Models)
	} else if res.StatusCode == 401 || res.StatusCode == 403 {
		deny, denyBody = &http.Response{StatusCode: res.StatusCode, Header: res.Header}, b
	}
}

// codexModels is the ChatGPT backend's model list for this sign-in, with
// magpie's models after it. Should the backend not answer, or not in
// time, Codex's last list of its own stands in.
func (s *Server) codexModels(w http.ResponseWriter, r *http.Request) {
	var own []any
	body, etag, deny, denyBody := s.codexBackendList(r)
	// A sign-in the backend calls revoked (401 token_revoked, "Encountered
	// invalidated oauth token", provider.SignInRevoked) is answered as a
	// backend that didn't answer: Codex's last list of its own, magpie's
	// models after it. Passed through, Codex tried one refresh, which
	// fails for good for such a sign-in (openai/codex login/src/auth/
	// manager.rs: refresh_token_expired, _reused, _invalidated), and kept
	// its bundled list, so magpie's models were gone from the picker until
	// the user signed in again (jjt on Discord, #4308). Nothing is kept of
	// it: the next ask goes to the backend again, so a sign-in made again
	// has its list at once, and Codex still learns of the dead sign-in on
	// a turn of its own models, whose 401 goes to it as it came. Any other
	// 401 or 403 ("token expired", for a refresh to mend) is Codex's to see.
	if deny != nil && provider.SignInRevoked(deny.StatusCode, denyBody) {
		deny, body, etag = nil, nil, ""
	}
	if deny != nil {
		// a sign-in to renew is Codex's to see
		w.Header().Set("Content-Type", deny.Header.Get("Content-Type"))
		w.WriteHeader(deny.StatusCode)
		w.Write(denyBody)
		return
	}
	if body != nil {
		// each caller's own copy: what follows writes into the entries
		var list struct {
			Models []any `json:"models"`
		}
		json.Unmarshal(body, &list)
		own = list.Models
	}
	// what follows reads the providers seven times over (the catalog, the
	// codex provider's picks and windows, the list's tag): held, they are
	// built once, where each build read every agent's sign-in, keychain
	// items among them, and together held the answer past Codex's 5 s (#746)
	defer provider.Hold()()
	cached := own == nil
	if cached {
		for _, e := range codexcat.CacheEntries() {
			own = append(own, e)
		}
	}
	all := codexMerged(own, cached)
	// the list is the backend's and magpie's, and so is its ETag
	w.Header().Set("ETag", codexcat.WithTag(etag, codexListTag()))
	writeJSON(w, 200, map[string]any{"models": all})
}

// codexMerged is the list magpie hands a Codex beside its ChatGPT sign-in:
// own, the backend's entries (cached when they are Codex's last list, not
// the backend's answer), as the user narrowed and set them on the codex
// provider, with magpie's models after them. The entries are written into.
func codexMerged(own []any, cached bool) []any {
	defer provider.Hold()()
	// the slugs the sign-in's list has, before the user's narrowing: a
	// ChatGPT account's model it lacks is listed by magpie's id after it
	// (provider.CodexListedBeside)
	backend := make(map[string]bool, len(own))
	for _, m := range own {
		if o, ok := m.(map[string]any); ok {
			if slug, _ := o["slug"].(string); slug != "" {
				backend[slug] = true
			}
		}
	}
	// the windows the user set on the codex provider, as /v1/models says
	// them (#674); a cached entry, which keeps what magpie handed Codex
	// last, takes the account's own list's back when none is set
	window := provider.CodexNativeWindow(cached)
	for _, m := range own {
		if o, ok := m.(map[string]any); ok {
			slug, _ := o["slug"].(string)
			if n, most, ok := window(slug); ok {
				codexcat.Window(o, n, most)
			}
		}
	}
	// The backend lists every model the ChatGPT account can reach. When the
	// user picked among them on the codex provider, keep the list to those:
	// their pick governs Codex's own models, not just magpie's added ones.
	// A hidden one stays: it is never in a picker, magpie's included
	// (parseCodexModels), and Codex uses it for jobs of its own. One of them
	// is codex-auto-review. Without it in the list, Codex's auto-review runs
	// on the conversation's own model at low effort
	// (codex-rs/ext/guardian-reviewer select_review_model), and so it is
	// charged where that model is (#1460).
	if keep, narrowed := provider.CodexNativePicked(); narrowed {
		kept := own[:0]
		for _, m := range own {
			o, _ := m.(map[string]any)
			if v, _ := o["visibility"].(string); v == "hide" {
				kept = append(kept, m)
				continue
			}
			if slug, _ := o["slug"].(string); slug != "" && !keep[slug] {
				continue
			}
			kept = append(kept, m)
		}
		own = kept
	}
	// and the ones taken out of Codex's list on the Agents page
	if off := provider.CodexNativeHidden(); len(off) > 0 {
		kept := own[:0]
		for _, m := range own {
			o, _ := m.(map[string]any)
			if slug, _ := o["slug"].(string); off[slug] {
				continue
			}
			kept = append(kept, m)
		}
		own = kept
	}
	// and all of them with Show Codex's own models off: the picker offers
	// magpie's alone. The model Codex is set to and the ones magpie serves
	// stay; a hidden job (codex-auto-review) stays as it does above
	// (iiiiiimone on X: the switch left Codex's picker as it was)
	if out := provider.CodexOwnLeftOut(codexModelNow()); out != nil {
		kept := own[:0]
		for _, m := range own {
			o, _ := m.(map[string]any)
			slug, _ := o["slug"].(string)
			if v, _ := o["visibility"].(string); v != "hide" && slug != "" && out(slug) {
				continue
			}
			kept = append(kept, m)
		}
		own = kept
	}
	// and the ones the user disabled (#1867), which are refused (codexNativeOff)
	if len(provider.DisabledModels()) > 0 {
		kept := own[:0]
		for _, m := range own {
			o, _ := m.(map[string]any)
			if slug, _ := o["slug"].(string); slug != "" {
				if _, off := codexNativeOff(slug); off {
					continue
				}
			}
			kept = append(kept, m)
		}
		own = kept
	}
	// multi-agent V1 when the user asked for it (#141): the version is all
	// that changes
	if codexcat.V1() {
		for _, m := range own {
			if o, ok := m.(map[string]any); ok {
				codexcat.Stamp(o)
			}
		}
	}
	// and the auto-review model the user picked (#938)
	autoReview := codexcat.AutoReviewModel()
	for _, m := range own {
		if o, ok := m.(map[string]any); ok {
			codexcat.AutoReview(o, autoReview)
		}
	}
	// a Codex here for account failover alone, not connected to magpie,
	// keeps to its own models (#1385: 62 of magpie's were in its list)
	var ms []catalog.Model
	if !codexOwnOnly() {
		ms = provider.CodexListedBeside(backend)
	}
	// Codex's own, as the user left them in its list, and a ChatGPT
	// account's that the sign-in's list lacks
	mine := make(map[string]bool, len(own))
	for _, m := range own {
		if o, ok := m.(map[string]any); ok {
			if slug, _ := o["slug"].(string); slug != "" {
				mine[slug] = true
			}
		}
	}
	for _, m := range ms {
		if codexcat.ChatGPTOwn(m.ID) {
			mine[m.ID] = true
		}
	}
	all := append(own, codexcat.Entries(ms, len(own)+100)...)
	if at, ok := provider.CodexOrder(); ok {
		codexcat.Order(all, at)
	}
	// the Codex app and its VS Code extension read the first 100 alone:
	// hidden ones go last, and Codex's own and the ones the user uses are
	// among the 100, wherever the user's order put them (maiya on Discord)
	codexcat.FitOwn(all, usage.CodexUsed(), func(slug string) bool { return mine[slug] })
	return all
}

// modelsEtag is the X-Models-Etag of a backend reply as Codex should read
// it: with magpie's models in it, as the list's own ETag has them, so a
// change to either has Codex ask for the list again.
func modelsEtag(h http.Header) {
	if v := h.Get("X-Models-Etag"); v != "" {
		h.Set("X-Models-Etag", codexcat.WithTag(v, codexListTag()))
	}
}

// CodexOwnOnly reports whether the Codex asking for its model list at
// CodexPath reaches magpie for account failover alone, not connected to
// it (agent.CodexOwnOnly): it is then handed only its own models (#1385).
// Set by main; nil, every Codex gets magpie's models too.
var CodexOwnOnly func() bool

// CodexModelNow is the model this machine's Codex is set to
// (agent.CodexModelNow); nil, none.
var CodexModelNow func() string

func codexModelNow() string {
	if CodexModelNow == nil {
		return ""
	}
	return CodexModelNow()
}

func codexOwnOnly() bool { return CodexOwnOnly != nil && CodexOwnOnly() }

// codexListTag is the tag of the list codexModels hands out now.
func codexListTag() string {
	if codexOwnOnly() {
		return provider.CodexOwnListTag()
	}
	// with the models Codex used, which codexcat.Fit brings to the top
	tag := provider.CodexListTag()
	if used := usage.CodexUsed(); len(used) > 0 {
		tag += "." + codexcat.UsedTag(used)
	}
	return tag
}

// codexInput restores summaries magpie made. For a magpie model, it also
// replaces Codex's compaction trigger with a request to summarise the input.
// OpenAI's own models keep the trigger for their backend to handle.
func codexInput(body []byte, magpieModel bool) (_ []byte, compact bool) {
	return codexInputAs(body, magpieModel, false)
}

// codexInputAs is codexInput, the trigger kept for a magpie model too
// when native says its provider compacts natively (NativeCompaction,
// #1099): the summaries magpie made before are still restored.
func codexInputAs(body []byte, magpieModel, native bool) (_ []byte, compact bool) {
	if !anyInputItem(body, func(it []byte) bool { return codexInputItemChanges(it, magpieModel, native) }) {
		return body, false
	}
	var q map[string]json.RawMessage
	if json.Unmarshal(body, &q) != nil {
		return body, false
	}
	var items []map[string]any
	if json.Unmarshal(q["input"], &items) != nil {
		return body, false
	}
	changed := false
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		switch it["type"] {
		case "compaction", "compaction_summary":
			enc, _ := it["encrypted_content"].(string)
			msg, ours := codexsummary.Message(enc)
			if !ours {
				out = append(out, it) // OpenAI's own, for OpenAI
				continue
			}
			changed = true
			if msg != nil {
				out = append(out, msg)
			}
		case "compaction_trigger":
			if !magpieModel || native {
				out = append(out, it)
				continue
			}
			changed, compact = true, true
			out = append(out, userMessage(codexCompactPrompt))
		case "reasoning":
			// OpenAI accepts no reasoning content parts in replayed input.
			// Drop the item so its encrypted_content cannot fail there too.
			if !magpieModel {
				if content, present := it["content"]; present && content != nil {
					parts, ok := content.([]any)
					if !ok || len(parts) > 0 {
						changed = true
						continue
					}
				}
				// Nor reasoning with nothing sealed in it — another vendor's,
				// only an id (rs_…): OpenAI, which keeps nothing (store is
				// false), looks the id up and answers 404 "Item … not found".
				// Nor Claude's block magpie carried in one (claudeseal),
				// which OpenAI would refuse as invalid_encrypted_content:
				// before magpie carried it, the item had nothing sealed
				if enc, _ := it["encrypted_content"].(string); enc == "" || claudeseal.Is(enc) {
					changed = true
					continue
				}
			}
			out = append(out, it)
		default:
			out = append(out, it)
		}
	}
	if !changed {
		return body, false
	}
	if compact {
		// Responses Lite offers Codex's tools as an additional_tools input
		// item, not in tools (codex/gpt-6-astra's entry is Codex's own):
		// left in, the summary request still offered every one of them,
		// and the model could call one rather than write the summary
		// (likely WuSix's "the model wrote no summary", 3 times in a row,
		// on Discord)
		kept := out[:0]
		for _, it := range out {
			if it["type"] != additionalToolsItem {
				kept = append(kept, it)
			}
		}
		out = kept
	}
	b, _ := json.Marshal(out)
	q["input"] = b
	if compact {
		// the summary is text; a tool call would be no summary, so it
		// offers none. Otherwise it goes as Codex asks for its own summary
		// from a provider it reaches itself: "tools": [] (not left out,
		// #1511), streamed with its tool_choice — a relay in front of the
		// ChatGPT backend turns away one that isn't ("invalid codex
		// request", #292)
		q["tools"] = json.RawMessage("[]")
		q["tool_choice"] = json.RawMessage(`"auto"`)
		q["parallel_tool_calls"] = json.RawMessage("false")
		if cm, ok := localCompactionMeta(q["client_metadata"]); ok {
			q["client_metadata"] = cm
		}
	}
	nb, err := marshalJSON(q)
	if err != nil {
		return body, false
	}
	return nb, compact
}

// codexInputItemChanges reports whether codexInputAs would put another in
// the place of it, an input item, or leave it out: read as the rewrite
// reads it (a map[string]any), without decoding the conversation.
func codexInputItemChanges(it []byte, magpieModel, native bool) bool {
	typ, _ := stringBytes(memberOrNil(it, "type"))
	switch {
	case string(typ) == "compaction" || string(typ) == "compaction_summary":
		enc, _ := stringBytes(memberOrNil(it, "encrypted_content"))
		return hasPrefix(enc, magpieCompaction)
	case string(typ) == "compaction_trigger":
		return magpieModel && !native
	case string(typ) == "reasoning":
		if magpieModel {
			return false
		}
		if c := memberOrNil(it, "content"); c != nil && string(c) != "null" {
			if c[0] != '[' {
				return true
			}
			empty := true
			eachElement(c, func([]byte) bool { empty = false; return false })
			if !empty {
				return true
			}
		}
		enc, _ := stringBytes(memberOrNil(it, "encrypted_content"))
		return len(enc) == 0 || hasPrefix(enc, claudeseal.Mark)
	}
	return false
}

// turnMetadataHeader is where Codex says what a request is for, as it does
// in client_metadata's x-codex-turn-metadata too.
const turnMetadataHeader = "X-Codex-Turn-Metadata"

// localCompactionTurn is Codex's turn metadata for a compaction magpie
// summarises itself, as Codex says it of the summary it asks a provider
// for: compaction.implementation "responses", not the remote
// "responses_compaction_v2" Codex named when it sent a compaction_trigger
// (Codex 0.162.1 under a provider named "OpenAI" and under another, both
// captured). The trigger is gone from what magpie sends, so the request
// said remote compaction and carried none, a shape Codex itself never
// sends. A relay that only takes Codex's own requests refused magpie's
// compaction with "invalid codex request" (#2352, #1171). Everything
// else stays byte for byte; false when there's nothing to change.
func localCompactionTurn(meta string) (string, bool) {
	if !strings.Contains(meta, `"compaction"`) {
		return meta, false
	}
	var m struct {
		Compaction *struct {
			Implementation *string `json:"implementation"`
		} `json:"compaction"`
	}
	if json.Unmarshal([]byte(meta), &m) != nil || m.Compaction == nil || m.Compaction.Implementation == nil {
		return meta, false
	}
	impl := *m.Compaction.Implementation
	if impl == "responses" {
		return meta, false
	}
	was, _ := json.Marshal(impl)
	old := `"implementation":` + string(was)
	if strings.Count(meta, old) != 1 {
		return meta, false
	}
	return strings.Replace(meta, old, `"implementation":"responses"`, 1), true
}

// previousModelCompaction is whether a compaction is the one Codex runs
// before a turn on another model, on the model the thread was on: a
// switch to a model with a smaller window (compaction.reason
// "model_downshift") or of another compaction hash ("comp_hash_changed"),
// the only places Codex names either (codex-rs core/src/session/turn.rs,
// maybe_run_previous_model_inline_compact). When it fails Codex asks the
// turn's own model instead, but only under its provider named "OpenAI"
// (compact_model_fallback.rs); under magpie's, the provider Codex has
// while its sign-in is used up (codex.out), the failure is the turn's,
// and every turn of the thread asks the old model again.
func previousModelCompaction(r *http.Request, body []byte) bool {
	meta := r.Header.Get(turnMetadataHeader)
	if meta == "" {
		meta = gjson.GetBytes(body, "client_metadata.x-codex-turn-metadata").Str
	}
	if !strings.Contains(meta, `"compaction"`) {
		return false
	}
	switch gjson.Get(meta, "compaction.reason").Str {
	case "model_downshift", "comp_hash_changed":
		return true
	}
	return false
}

// localCompactionMeta is a request's client_metadata with its
// x-codex-turn-metadata made localCompactionTurn's.
func localCompactionMeta(raw json.RawMessage) (json.RawMessage, bool) {
	var cm map[string]json.RawMessage
	var meta string
	if json.Unmarshal(raw, &cm) != nil || json.Unmarshal(cm["x-codex-turn-metadata"], &meta) != nil {
		return raw, false
	}
	turn, ok := localCompactionTurn(meta)
	if !ok {
		return raw, false
	}
	cm["x-codex-turn-metadata"], _ = marshalPlain(turn)
	out, err := marshalPlain(cm)
	if err != nil {
		return raw, false
	}
	return out, true
}

// bareReasoningRefused is how unfit remembers a provider turning away, for
// model, reasoning items with nothing sealed in them (withoutBareReasoning).
func bareReasoningRefused(model string) string { return "bare reasoning\x00" + model }

// refusesInput is a 400 refusing a Responses request over its input: the
// error's param is the input, as OpenAI's own is for an item it can't find
// and a relay in front of it passes on with a message of its own ("bad
// response status code 400", #1044), or the error names an item by id.
func refusesInput(status int, b []byte) bool {
	if !badRequest(status) {
		return false
	}
	var e struct {
		Error struct {
			Param any `json:"param"`
		} `json:"error"`
	}
	if json.Unmarshal(b, &e) == nil {
		if p, _ := e.Error.Param.(string); p == "input" || strings.HasPrefix(p, "input[") || strings.HasPrefix(p, "input.") {
			return true
		}
	}
	return unreadableItem.Match(b)
}

// withoutBareReasoning is a Responses request without the reasoning items
// that have nothing sealed in them, where store isn't true: an id and a
// summary only, as magpie gives Codex in a translated reply (rs_ and
// newID). OpenAI's API and Azure OpenAI's look such an item up among the
// items they stored, and with store false they stored none, so the whole
// request goes back 400 "Item with id 'rs_…' not found" (#1008); its
// summary was the model's notes to itself, which no model reads back.
func withoutBareReasoning(body []byte) []byte {
	if !bytes.Contains(body, []byte(`"reasoning"`)) || !anyInputItem(body, func(it []byte) bool {
		r := reasoningOf(it)
		return r.typeOK && r.encOK && r.reasoning && !r.sealed
	}) {
		return body
	}
	var q map[string]json.RawMessage
	if json.Unmarshal(body, &q) != nil {
		return body
	}
	var store bool
	if json.Unmarshal(q["store"], &store) == nil && store {
		return body
	}
	var items []json.RawMessage
	if json.Unmarshal(q["input"], &items) != nil {
		return body
	}
	kept := items[:0:0]
	for _, it := range items {
		var t struct {
			Type string `json:"type"`
			Enc  string `json:"encrypted_content"`
		}
		if json.Unmarshal(it, &t) == nil && t.Type == "reasoning" && t.Enc == "" {
			continue
		}
		kept = append(kept, it)
	}
	if len(kept) == len(items) {
		return body
	}
	q["input"], _ = marshalJSON(kept)
	b, err := marshalJSON(q)
	if err != nil {
		return body
	}
	return b
}

// reasoningLook is what decoding an input item into the structs the
// reasoning rewrites read it by gives: its type is reasoning, something
// is sealed in it, it has its text; and whether a decode of its type
// beside each other field succeeds.
type reasoningLook struct {
	reasoning, sealed, text  bool
	typeOK, encOK, contentOK bool
}

// reasoningOf is reasoningLook of it, an input item. Its keys are matched
// as encoding/json matches a field's name (bytes.EqualFold), the last one
// standing. A plain item is read where it lies, nothing decoded; one with
// a key escaped or past ASCII, a type that is, or a null where a field
// is, is decoded.
func reasoningOf(it []byte) reasoningLook {
	if len(it) == 0 {
		return reasoningLook{}
	}
	switch it[0] {
	case 'n':
		return reasoningLook{typeOK: true, encOK: true, contentOK: true}
	case '{':
	default:
		return reasoningLook{}
	}
	r := reasoningLook{typeOK: true, encOK: true, contentOK: true}
	slow := false
	eachMember(it, func(k, v []byte) bool {
		name := k[1 : len(k)-1]
		if !plainASCII(name) {
			slow = true
			return false
		}
		field := bytes.EqualFold(name, []byte("type")) || bytes.EqualFold(name, []byte("encrypted_content")) || bytes.EqualFold(name, []byte("content"))
		if field && v[0] == 'n' {
			slow = true
			return false
		}
		switch {
		case bytes.EqualFold(name, []byte("type")):
			if v[0] != '"' {
				r.typeOK = false
				return true
			}
			typ := v[1 : len(v)-1]
			if !plainASCII(typ) {
				slow = true
				return false
			}
			r.reasoning = string(typ) == "reasoning"
		case bytes.EqualFold(name, []byte("encrypted_content")):
			r.encOK = r.encOK && v[0] == '"'
			r.sealed = len(v) > 2
		case bytes.EqualFold(name, []byte("content")):
			r.contentOK = r.contentOK && v[0] == '['
			r.text = false
			eachElement(v, func([]byte) bool { r.text = true; return false })
		}
		return true
	})
	if !slow {
		return r
	}
	var te struct {
		Type string `json:"type"`
		Enc  string `json:"encrypted_content"`
	}
	var tc struct {
		Type    string            `json:"type"`
		Content []json.RawMessage `json:"content"`
	}
	errE, errC := decodeJSON(it, &te), decodeJSON(it, &tc)
	var tt struct {
		Type string `json:"type"`
	}
	errT := decodeJSON(it, &tt)
	return reasoningLook{reasoning: tt.Type == "reasoning", sealed: te.Enc != "", text: len(tc.Content) > 0,
		typeOK: errT == nil, encOK: errE == nil, contentOK: errC == nil}
}

// reasoningTextRefused is how unfit remembers a provider turning away, for
// model, reasoning items with their text in them (withoutReasoningText).
func reasoningTextRefused(model string) string { return "reasoning text\x00" + model }

// reasoningContentRefusal is OpenAI's refusal of a reasoning item with its
// text in it, naming the item: "Invalid 'input[1].content': array too
// long. Expected an array with maximum length 0, but got an array with
// length 1 instead." (the ChatGPT backend's; a relay's may word the rest
// otherwise, #1411)
var reasoningContentRefusal = lazyre.New(`input\[(\d+)\]\.content'?:? array too long`)

// refusesReasoningText is a 400 refusing the reasoning text in a Responses
// request: the item the refusal names is reasoning with text in it.
func refusesReasoningText(status int, b, body []byte) bool {
	if !badRequest(status) {
		return false
	}
	m := reasoningContentRefusal.FindSubmatch(b)
	if m == nil {
		return false
	}
	it := gjson.GetBytes(body, "input."+string(m[1]))
	return it.Get("type").String() == "reasoning" && len(it.Get("content").Array()) > 0
}

// withoutReasoningText is a Responses request without the reasoning items
// that carry their text (content parts), as another vendor's model wrote
// them: DeepSeek's, which a routing group handed Codex with its own
// encrypted_content beside the text. OpenAI's API, the ChatGPT backend and
// a relay in front of them take a reasoning item's content only empty, and
// turn the whole request away over one (#1411); its seal is no more theirs
// than its text, so the item goes, as codexInput leaves it out for one of
// Codex's own models.
func withoutReasoningText(body []byte) []byte {
	if !bytes.Contains(body, []byte(`"reasoning"`)) || !anyInputItem(body, func(it []byte) bool {
		r := reasoningOf(it)
		return r.typeOK && r.contentOK && r.reasoning && r.text
	}) {
		return body
	}
	var q map[string]json.RawMessage
	if json.Unmarshal(body, &q) != nil {
		return body
	}
	var items []json.RawMessage
	if json.Unmarshal(q["input"], &items) != nil {
		return body
	}
	kept := items[:0:0]
	for _, it := range items {
		var t struct {
			Type    string            `json:"type"`
			Content []json.RawMessage `json:"content"`
		}
		if json.Unmarshal(it, &t) == nil && t.Type == "reasoning" && len(t.Content) > 0 {
			continue
		}
		kept = append(kept, it)
	}
	if len(kept) == len(items) {
		return body
	}
	q["input"], _ = marshalJSON(kept)
	b, err := marshalJSON(q)
	if err != nil {
		return body
	}
	return b
}

// citationOffsetRefused is how unfit remembers a provider turning away,
// for model, a citation without the offsets OpenAI's schema wants
// (withoutOffsetlessCitations).
func citationOffsetRefused(model string) string { return "citation offsets\x00" + model }

// citationOffsetRefusal is the ChatGPT backend's refusal of an annotation
// the offsets its schema wants are missing from, as a relay in front of
// it passes it on: "Invalid response.create payload: Missing required
// parameter: 'input[221].content[0].annotations[0].start_index'."
// (#4921)
var citationOffsetRefusal = lazyre.New(`input\[\d+\]\.content\[\d+\]\.annotations\[\d+\]\.(?:start|end)_index`)

// refusesCitationOffsets is a 400 refusing a Responses request over a
// citation that lacks the offsets OpenAI's schema wants.
func refusesCitationOffsets(status int, b []byte) bool {
	return badRequest(status) && citationOffsetRefusal.Match(b)
}

// withoutOffsetlessCitations is a Responses request without the
// output_text annotations that lack the offsets OpenAI's schema wants on
// every one, as another vendor's Responses reply wrote them (Volcengine
// Ark's url_citation, #4921): Codex stored the reply magpie relayed, and
// where that schema holds — the ChatGPT backend, OpenAI's API, a relay in
// front of them — the replay was refused whole, every turn of the thread
// with it. Such an annotation goes; the text, and a citation with its
// offsets, stay as they came.
func withoutOffsetlessCitations(body []byte) []byte {
	if !bytes.Contains(body, []byte(`"annotations"`)) || !anyInputItem(body, itemCitesWithoutOffsets) {
		return body
	}
	q, err := rawObject(body)
	if err != nil {
		return body
	}
	items, err := rawArray(q["input"])
	if err != nil {
		return body
	}
	changed := false
	for i, raw := range items {
		if !itemCitesWithoutOffsets(raw) {
			continue
		}
		it, err := rawObject(raw)
		if err != nil {
			continue
		}
		parts, err := rawArray(it["content"])
		if err != nil {
			continue
		}
		itemChanged := false
		for j, p := range parts {
			part, err := rawObject(p)
			if err != nil || string(part["type"]) != `"output_text"` {
				continue
			}
			notes := elementsOf(part["annotations"])
			if notes == nil {
				continue
			}
			kept := notes[:0]
			dropped := false
			for _, a := range notes {
				if annotationLacksOffsets(a) {
					dropped = true
					continue
				}
				kept = append(kept, a)
			}
			if dropped {
				part["annotations"], _ = marshalPlain(kept)
				parts[j], _ = marshalPlain(part)
				itemChanged = true
			}
		}
		if itemChanged {
			it["content"], _ = marshalPlain(parts)
			items[i], _ = marshalPlain(it)
			changed = true
		}
	}
	if !changed {
		return body
	}
	q["input"], _ = marshalPlain(items)
	nb, err := marshalPlain(q)
	if err != nil {
		return body
	}
	return nb
}

// itemCitesWithoutOffsets is whether it, an input item, carries an
// output_text annotation without the offsets OpenAI's schema wants, read
// as the scan reads it, without decoding the conversation.
func itemCitesWithoutOffsets(it []byte) bool {
	if !bytes.Contains(it, []byte(`"annotations"`)) {
		return false
	}
	for _, part := range elementsOf(memberOrNil(it, "content")) {
		if typ, _ := stringBytes(memberOrNil(part, "type")); string(typ) != "output_text" {
			continue
		}
		for _, a := range elementsOf(memberOrNil(part, "annotations")) {
			if annotationLacksOffsets(a) {
				return true
			}
		}
	}
	return false
}

// openaiItemPrefix is the id prefix OpenAI takes for each kind of call item
// a vendor's reply may have handed Codex with another: magpie, or the
// vendor, gave a tool search's and a custom tool's call a function_call's
// id (fc_…), and Codex hands the item back on every later turn. OpenAI's
// own models and the ChatGPT backend turn the whole request away ("Invalid
// 'input[98].id': 'fc_…'. Expected an ID that begins with 'tsc'", "…with
// 'ctc'"), Codex's compaction with it.
var openaiItemPrefix = map[string]string{
	"tool_search_call": "tsc_",
	"custom_tool_call": "ctc_",
}

// callItemIDs is a Responses request whose call items have ids OpenAI
// takes (openaiItemPrefix): fc_X goes as tsc_X or ctc_X, the call_id the
// call's output names it by as it was, and the rest of the request byte
// for byte.
func callItemIDs(body []byte) []byte {
	if !bytes.Contains(body, []byte(`"tool_search_call"`)) && !bytes.Contains(body, []byte(`"custom_tool_call"`)) {
		return body
	}
	if !anyInputItem(body, func(it []byte) bool {
		typ, _ := stringBytes(memberOrNil(it, "type"))
		prefix := openaiItemPrefix[string(typ)]
		id, _ := stringBytes(memberOrNil(it, "id"))
		return prefix != "" && len(id) > 0 && !hasPrefix(id, prefix)
	}) {
		return body
	}
	var q map[string]json.RawMessage
	if json.Unmarshal(body, &q) != nil {
		return body
	}
	var items []json.RawMessage
	if json.Unmarshal(q["input"], &items) != nil {
		return body
	}
	changed := false
	for i, raw := range items {
		var it map[string]json.RawMessage
		var typ, id string
		if json.Unmarshal(raw, &it) != nil || json.Unmarshal(it["type"], &typ) != nil {
			continue
		}
		prefix := openaiItemPrefix[typ]
		if prefix == "" || json.Unmarshal(it["id"], &id) != nil || id == "" || strings.HasPrefix(id, prefix) {
			continue
		}
		if _, rest, ok := strings.Cut(id, "_"); ok && rest != "" {
			id = prefix + rest
		} else {
			id = prefix + id
		}
		it["id"], _ = json.Marshal(id)
		if b, err := marshalPlain(it); err == nil {
			items[i], changed = b, true
		}
	}
	if !changed {
		return body
	}
	q["input"], _ = marshalPlain(items)
	nb, err := marshalPlain(q)
	if err != nil {
		return body
	}
	return nb
}

// itemIDRefusal is OpenAI's refusal of an input item whose id has a
// character other than [A-Za-z0-9_-], which Copilot passes on as it is
// (echo_ts, Codex moved from Muse Spark on OpenCode to GPT on Copilot):
// "Invalid 'input[1].id': 'rs_6aca2fc7533c979d5f0e4f7a:rs_01a125caa544754e9a3c70080100b009'.
// Expected an ID that contains letters, numbers, underscores, or dashes,
// but this value contained additional characters."
// It is the tail within 1000 characters of the head: the regexp
// .{0,1000}? it was unrolled to 88kB of program, held from start to exit.
func itemIDRefusal(b []byte) bool {
	const tail = "Expected an ID that contains letters, numbers, underscores, or dashes"
	for _, at := range itemIDRefusalHead.FindAllIndex(b, -1) {
		rest, end := b[at[1]:], 0
		for n := 0; n < 1000 && end < len(rest); n++ {
			_, w := utf8.DecodeRune(rest[end:])
			end += w
		}
		if bytes.Contains(rest[:min(len(rest), end+len(tail))], []byte(tail)) {
			return true
		}
	}
	return false
}

var itemIDRefusalHead = lazyre.New(`Invalid 'input\[\d+\]\.id'`)

// itemIDsRefused is how unfit remembers a provider turning away, for
// model, an input item id with another character in it (plainItemIDs).
func itemIDsRefused(model string) string { return "item ids\x00" + model }

// validatesItemIDs reports whether p checks a Responses input item's id
// as OpenAI does, without having to be told by a refusal: OpenAI's API,
// Azure OpenAI's, the ChatGPT backend behind a Codex sign-in, and Copilot's
// /responses in front of them.
func validatesItemIDs(p provider.Provider) bool {
	return strings.HasSuffix(p.VendorHost(), "openai.com") || p.IsAzure() ||
		p.Account != nil && (p.Account.Agent == "codex" || p.Account.Agent == "copilot")
}

// itemIDCap is the most characters an input item's id may have: OpenAI's
// API, the ChatGPT backend, and Copilot's /responses in front of them turn
// a longer one away — "Invalid 'input[1].id': string too long. Expected a
// string with maximum length 64, but got a string with length 420
// instead" (isnine, #4525; ghjkllasdf-fake's 444 alike, #4527: Copilot
// gives its items ids hundreds of characters long, and Codex carries each
// one back on every later turn). A call's call_id is another field's to
// bound, as BoundCallID does.
const itemIDCap = 64

// boundItemID is id, of at most itemIDCap characters, as an upstream that
// checks item ids takes it: one longer goes as its kind's prefix and the
// sha256 of the whole of it, cut to fit. The kind comes from typ
// (itemKindPrefix, openaiItemPrefix's for a call), and only an item of another
// kind takes a prefix from its id: Copilot's own ids can be opaque, + and
// / in them and no prefix at the front, and a bare digest passed the
// length and the characters and still had the whole request turned away
// with an empty "invalid_request_body" (#5266). The same id goes to the
// same shorter one every turn, and no two ids land on one (a plain cut
// could: ids of a vendor can begin alike).
func boundItemID(id, typ string) string {
	sum := sha256.Sum256([]byte(id))
	digest := hex.EncodeToString(sum[:])
	prefix := openaiItemPrefix[typ]
	if prefix == "" {
		prefix = itemKindPrefix[typ]
	}
	if prefix == "" {
		if i := strings.IndexByte(id, '_'); i > 0 && i <= 4 && safeID([]byte(id[:i])) {
			prefix = id[:i+1]
		}
	}
	return prefix + digest[:itemIDCap-len(prefix)]
}

// idOptional are the kinds of input item whose id OpenAI's /responses
// takes left out ("" is a message given with no type), as OpenAI's own SDK
// has them (openai-go v3.42.0's ResponseInputItemUnionParam: the id is
// param.Opt on each of these, required on the rest). One of these with an
// id too long goes without it, which every upstream that checks ids takes:
// Copilot answered a message's and a call's overlong id left out with 200,
// and the same id as a bare digest with an empty 400 "invalid_request_body"
// (isnine on #4525, echo_ts after Codex's compaction). A call and its
// output keep their call_id, which pairs them.
var idOptional = map[string]bool{
	"": true, "message": true,
	"function_call": true, "function_call_output": true,
	"custom_tool_call": true, "custom_tool_call_output": true,
	"tool_search_call": true, "tool_search_output": true,
	"computer_call_output": true,
	"shell_call":           true, "shell_call_output": true,
	"apply_patch_call": true, "apply_patch_call_output": true,
	"mcp_approval_response": true,
	"additional_tools":      true,
	"compaction":            true,
}

// itemKindPrefix is the prefix OpenAI gives the id of each kind of input
// item that can't go without one (openai-go's id "required"), which
// boundItemID binds an overlong id under. Copilot hands these out opaque
// too: a web_search_call's id ~450 characters of Base64 with no ws_ at the
// front, which as a bare digest had the whole request turned away with the
// same empty 400 "invalid_request_body" (echo_ts, native web search on
// Copilot). A kind not here (local_shell_call, item_reference, program)
// keeps the prefix its own id has.
var itemKindPrefix = map[string]string{
	"reasoning":             "rs_",
	"web_search_call":       "ws_",
	"file_search_call":      "fs_",
	"computer_call":         "cu_",
	"image_generation_call": "ig_",
	"code_interpreter_call": "ci_",
	"mcp_call":              "mcp_",
	"mcp_list_tools":        "mcpl_",
	"mcp_approval_request":  "mcpr_",
}

// plainItemIDs is a Responses request whose input items' ids are of
// [A-Za-z0-9_-] only and of itemIDCap characters at most, as OpenAI takes
// them, and the rest of the request byte for byte. Another vendor's reply
// can hand the agent an item whose id has more in it — Muse Spark on
// OpenCode's reasoning id is two joined with a colon ("rs_6aca…:rs_01a1…")
// — and the agent sends it back on every later turn, after a switch too,
// where OpenAI and Copilot turn the whole request away over it (echo_ts).
// Such reasoning goes: an upstream that checks ids never made it, so its
// encrypted_content is another vendor's, which it couldn't read either.
// Reasoning of the characters they take, but longer than they take
// (Copilot's own, #4525), keeps its place, its id bound; any other item (a
// message, a call) keeps what it says, its id left out when it was long and
// may be (idOptional), bound when it was long and may not (boundItemID), or
// with each other character as "_"; a call's call_id, which its output
// names it by, stays as it was.
func plainItemIDs(body []byte) []byte {
	if !bytes.Contains(body, []byte(`"id"`)) {
		return body
	}
	if !anyInputItem(body, func(it []byte) bool {
		id, _ := stringBytes(memberOrNil(it, "id"))
		return len(id) > itemIDCap || len(id) > 0 && !safeID(id)
	}) {
		return body
	}
	var q map[string]json.RawMessage
	if json.Unmarshal(body, &q) != nil {
		return body
	}
	var items []json.RawMessage
	if json.Unmarshal(q["input"], &items) != nil {
		return body
	}
	changed := false
	kept := items[:0:0]
	for _, raw := range items {
		var it map[string]json.RawMessage
		var typ, id string
		if json.Unmarshal(raw, &it) != nil || json.Unmarshal(it["id"], &id) != nil || id == "" ||
			len(id) <= itemIDCap && safeIDChars.MatchString(id) {
			kept = append(kept, raw)
			continue
		}
		changed = true
		// another vendor's reasoning goes; the vendor's own, only too
		// long, stays with its seal
		if json.Unmarshal(it["type"], &typ) == nil && typ == "reasoning" && !safeIDChars.MatchString(id) {
			continue
		}
		switch {
		case len(id) > itemIDCap && idOptional[typ]:
			delete(it, "id")
		case len(id) > itemIDCap:
			it["id"], _ = json.Marshal(boundItemID(id, typ))
		default:
			it["id"], _ = json.Marshal(unsafeToolID.ReplaceAllString(id, "_"))
		}
		b, err := marshalPlain(it)
		if err != nil {
			return body
		}
		kept = append(kept, b)
	}
	if !changed {
		return body
	}
	q["input"], _ = marshalPlain(kept)
	nb, err := marshalPlain(q)
	if err != nil {
		return body
	}
	return nb
}

// longCallID finds a call_id longer than the ChatGPT backend takes.
var longCallID = lazyre.New(`"call_id"\s*:\s*"[^"]{65,}"`)

// boundCallIDs is a Responses request with every call_id longer than 64
// characters as provider.BoundCallID has it, the rest of the request byte
// for byte (#732, congee949): a conversation that had a foreign provider's
// tool calls (two ids joined, 86 or 87 characters) went on Codex's own
// ChatGPT sign-in as it came, and the backend turned it away with 400
// "input[7].call_id … maximum length 64". A call and its output get the
// same id, on every request.
func boundCallIDs(body []byte) []byte {
	if !longCallID.Match(body) {
		return body
	}
	var q map[string]json.RawMessage
	if json.Unmarshal(body, &q) != nil {
		return body
	}
	var items []json.RawMessage
	if json.Unmarshal(q["input"], &items) != nil {
		return body
	}
	changed := false
	for i, raw := range items {
		var it map[string]json.RawMessage
		var id string
		if json.Unmarshal(raw, &it) != nil || json.Unmarshal(it["call_id"], &id) != nil {
			continue
		}
		b := provider.BoundCallID(id)
		if b == id {
			continue
		}
		it["call_id"], _ = json.Marshal(b)
		if nb, err := marshalPlain(it); err == nil {
			items[i], changed = nb, true
		}
	}
	if !changed {
		return body
	}
	q["input"], _ = marshalPlain(items)
	nb, err := marshalPlain(q)
	if err != nil {
		return body
	}
	return nb
}

// openaiOnly are the parts of a Responses request Codex sends only to a
// provider named "OpenAI": its built-in one (signed in, through
// openai_base_url), or CC Switch's table so named for remote compaction.
// Under any other name Codex leaves them out itself (client.rs, !is_openai):
// output token replay's include and the sealed output it carries back on a
// message or call (encrypted_content on any item but reasoning and a
// compaction).
var openaiOnly = [][]byte{[]byte(`"internal_chat_message_metadata_passthrough"`),
	[]byte(`"encrypted_function_args"`), []byte(`"stream_options"`), []byte(`"configuration_update"`),
	[]byte(`"` + outputReplay + `"`), []byte(`"encrypted_content"`)}

// outputReplay is what Codex asks OpenAI for with output token replay on
// (client.rs): each message's and call's own sealed copy. Copilot's
// /responses refuses it ("Invalid value: 'output.encrypted_content'",
// echo_ts), as an API that isn't OpenAI's may.
const outputReplay = "output.encrypted_content"

// keepsSeal are the input items whose encrypted_content Codex sends any
// provider (ResponseItem::clear_encrypted_content leaves only these): a
// reasoning item's, which foreignReasoning takes out once refused, and a
// compaction's.
var keepsSeal = map[string]bool{`"reasoning"`: true, `"compaction"`: true, `"compaction_summary"`: true, `"context_compaction"`: true}

// forVendor is a Responses request as Codex sends it to a provider not
// OpenAI's: without its messages' internal metadata, a call's
// encrypted_function_args, stream_options' reasoning_summary_delivery,
// configuration_update items (the last one's effort at the top instead),
// output.encrypted_content in its include,
// and the sealed output (encrypted_content) on a message or call that an
// OpenAI model wrote earlier in the conversation; and, for a summary ask
// of magpie's own (compaction, codexCompact), with its developer messages
// as the system ones they are (developerItemsAsSystem) — Codex's own turns
// keep the role, which a relay may be checking them by (#350, #1758).
// A relay that checks it is Codex's turned the lot away ("invalid codex
// request", #292), and Copilot the include (echo_ts). OpenAI's API and the
// ChatGPT backend get it as it came; so does anything else, byte for byte,
// when none of them is in it.
func forVendor(p provider.Provider, body []byte, compaction bool) []byte {
	if p.Account != nil && p.Account.Agent == "codex" || strings.HasSuffix(p.VendorHost(), "openai.com") {
		return body
	}
	if compaction {
		body = developerItemsAsSystem(body)
	}
	if !slices.ContainsFunc(openaiOnly, func(k []byte) bool { return bytes.Contains(body, k) }) || !forVendorChanges(body) {
		return body
	}
	q, err := rawObject(body)
	if err != nil {
		return body
	}
	changed := false
	// Codex's stream_options holds reasoning_summary_delivery alone; another
	// client's other options stay
	if so, err := rawObject(q["stream_options"]); err == nil {
		if _, ok := so["reasoning_summary_delivery"]; ok {
			changed = true
			delete(so, "reasoning_summary_delivery")
			if q["stream_options"], _ = marshalPlain(so); len(so) == 0 {
				delete(q, "stream_options")
			}
		}
	}
	var include []string
	if decodeJSON(q["include"], &include) == nil && slices.Contains(include, outputReplay) {
		changed = true
		if include = slices.DeleteFunc(include, func(v string) bool { return v == outputReplay }); len(include) == 0 {
			delete(q, "include")
		} else {
			q["include"], _ = marshalPlain(include)
		}
	}
	if items, err := rawArray(q["input"]); err == nil {
		updated := "" // the effort the agent changed to mid-thread
		out := items[:0]
		for _, raw := range items {
			it, err := rawObject(raw)
			if err != nil {
				out = append(out, raw)
				continue
			}
			if string(it["type"]) == `"configuration_update"` {
				changed = true
				if e := updateEffort(raw); e != "" {
					updated = e
				}
				continue
			}
			_, meta := it["internal_chat_message_metadata_passthrough"]
			_, sealed := it["encrypted_function_args"]
			_, replay := it["encrypted_content"]
			replay = replay && !keepsSeal[string(it["type"])]
			if meta || sealed || replay {
				changed = true
				delete(it, "internal_chat_message_metadata_passthrough")
				delete(it, "encrypted_function_args")
				if replay {
					delete(it, "encrypted_content")
				}
				raw, _ = marshalPlain(it)
			}
			out = append(out, raw)
		}
		if changed {
			q["input"], _ = marshalPlain(out)
		}
		// the items go, the change they made stays: at the top, which is
		// all this vendor reads
		if reasoning, err := rawObject(q["reasoning"]); updated != "" && err == nil && reasoning != nil {
			reasoning["effort"], _ = json.Marshal(updated)
			q["reasoning"], _ = marshalPlain(reasoning)
		}
	}
	if !changed {
		return body
	}
	nb, err := marshalPlain(q)
	if err != nil {
		return body
	}
	return nb
}

// keepsAccessPrograms says a Responses request to p goes with its top-level
// access_programs, the Cyber program a request asks the model under
// (OpenAI's Daybreak, magpie-releases#18): to the ChatGPT backend, to
// OpenAI's API, to another magpie (whose own upstream decides), and to a
// relay that takes Codex's requests as Codex sent them (CodexAsSent). Codex
// itself sends the field to nobody else: only with a ChatGPT sign-in, or an
// API key on its provider named "openai" (codex-rs cyber_access_program.rs,
// for_auth and for_provider), which is how it comes to magpie. OpenCode Go
// answers it, for DeepSeek and GLM, with a 503 "Endpoint is unavailable"
// that says nothing of the field (#3172), and no other vendor has a program
// to read in it.
func keepsAccessPrograms(p provider.Provider, in http.Header) bool {
	return p.Account != nil && p.Account.Agent == "codex" ||
		strings.HasSuffix(p.VendorHost(), "openai.com") ||
		p.IsRemoteMagpie() ||
		p.CodexAsSent && p.Account == nil && fromCodex(in)
}

// withoutAccessPrograms is body without its top-level access_programs, the
// rest byte for byte (withoutTopMember).
func withoutAccessPrograms(body []byte) []byte {
	if !bytes.Contains(body, []byte(`"access_programs"`)) {
		return body
	}
	return withoutTopMember(body, "access_programs")
}

// forVendorChanges reports whether forVendor has anything to leave out of
// body, read as it reads it, without decoding the conversation: Codex
// sends what the rewrite looks for by name in every request, and its
// metadata on every message, so a word in the body said little.
func forVendorChanges(body []byte) bool {
	so, found, ok := topMember(body, "stream_options")
	if !ok {
		return false
	}
	if found && len(so) > 0 && so[0] == '{' {
		if _, has := memberOf(so, "reasoning_summary_delivery"); has {
			return true
		}
	}
	if inc, found, ok := topMember(body, "include"); !ok {
		return false
	} else if found && bytes.Contains(inc, []byte(`"`+outputReplay+`"`)) {
		return true
	}
	return anyInputItem(body, func(it []byte) bool {
		if len(it) == 0 || it[0] != '{' {
			return false
		}
		typ := string(memberOrNil(it, "type"))
		if typ == `"configuration_update"` {
			return true
		}
		_, meta := memberOf(it, "internal_chat_message_metadata_passthrough")
		_, sealed := memberOf(it, "encrypted_function_args")
		_, replay := memberOf(it, "encrypted_content")
		return meta || sealed || replay && !keepsSeal[typ]
	})
}

// developerItemsAsSystem turns the developer messages of a Responses
// request into system ones, where they stand. A provider that isn't
// OpenAI's may not take the role: its Responses answered a Codex
// compaction 400 "developer is not one of ['system', 'assistant', 'user',
// 'tool', 'function']", the chat-only vendor behind it knowing chat roles
// alone (#2955) — every retry Codex made failed the same, while the turns
// around it, carrying no developer message, passed. Zed's cloud turned
// the whole request away over one (ZedBody), and a Chat Completions
// upstream is given the messages as system for the same reason
// (developerAsSystem). A developer message says what a system one says,
// and every Responses backend takes system; the ChatGPT backend, which
// takes developer alone, never comes here. The message stays where it
// stood, so a thread's cached prefix keeps its shape (#502).
func developerItemsAsSystem(body []byte) []byte {
	if !bytes.Contains(body, []byte(`"developer"`)) {
		return body
	}
	var q map[string]json.RawMessage
	if json.Unmarshal(body, &q) != nil {
		return body
	}
	var items []json.RawMessage
	if json.Unmarshal(q["input"], &items) != nil {
		return body
	}
	changed := false
	out := make([]json.RawMessage, 0, len(items))
	for _, raw := range items {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber() // a number goes back as it came, not as a float
		var it map[string]any
		if dec.Decode(&it) == nil && (it["type"] == nil || it["type"] == "message") && it["role"] == "developer" {
			it["role"] = "system"
			if b, err := marshalPlain(it); err == nil {
				raw, changed = b, true
			}
		}
		out = append(out, raw)
	}
	if !changed {
		return body
	}
	q["input"], _ = marshalPlain(out)
	nb, err := marshalPlain(q)
	if err != nil {
		return body
	}
	return nb
}

func userMessage(text string) map[string]any { return codexsummary.UserMessage(text) }

// compactWithin is how long a compaction's summary is waited for before
// magpie's own goes in its place. Codex hears nothing while the summary is
// made, and gives up on a stream that says nothing for 300s
// (stream_idle_timeout_ms; agentQuietMost): a summary later than that is
// never read, and the local one written after it reached nobody, so Codex
// sent the same ~3MB history again, each time waiting 300s (#5275,
// hayden4608: a slow uplink still sending it at 299s). A minute short of
// it, as agentQuietMost.
var compactWithin = 4 * time.Minute

// codexCompact compacts a conversation held by a magpie model: the model
// summarises it, and the summary goes back to Codex as the compaction item
// the backend would have made, marked as magpie's so magpie reads it back
// in later requests.
//
// A provider that answers the summary 404 — a relay that serves the
// conversation turn by turn but not the summary of it, "Upstream request
// failed" (#866) — or 405 or 501 (lacksEndpoint), or 400 for an item of it it doesn't have (#1008),
// or a kind of item it doesn't take (inputKindRefused: Command Code's
// "unsupported_input_item", #5181, #5259) is
// asked once more with the conversation as plain text, none of its items'
// ids or sealed reasoning in it; when that fails too the summary is
// magpie's own, the conversation's user messages and last reply,
// so the compaction still completes and Codex goes on. So is one the
// ChatGPT backend answers 502 "response protection is unavailable" (vs on
// Discord): the same long history failed so on every account and model,
// and as plain text was summarised — and one it breaks off with that
// refusal after the reply began, HTTP 200 and an error event (#1270). Any
// other failure (401, 429, 500) goes back to Codex with its status, said
// as the compaction's, with who answered it (compactFailed).
//
// The summary is asked for whole (askWhole): a reply that breaks off, or
// ends incomplete, is a failure the next account or member answers, as
// one that fails before it begins, and is never taken for the summary.
// When none is left and the last one's stream broke off, the summary is
// magpie's own (#3526): Codex, told a 502, asked the same huge summary
// over until the guard held it, and the conversation could neither compact
// nor go on. One that ended incomplete is still told so. One conversation's
// compaction that keeps failing stops being asked for a while
// (compactFails, #2274).
func (s *Server) codexCompact(w http.ResponseWriter, r *http.Request, body []byte) {
	key, model := compactKey(body), modelOf(body)
	// Codex's compaction on the model a thread is leaving, under magpie's
	// provider: one that fails is compacted locally, as Codex would have
	// the turn's own model do it (previousModelCompaction), never the
	// turn's failure. On CodexPath Codex's provider is its own "OpenAI",
	// which asks the turn's model itself, a summary where magpie's local
	// one is only what was asked and the last reply
	prev := !strings.HasPrefix(r.URL.Path, CodexPath+"/") && previousModelCompaction(r, body)
	if msg, held := s.compactFails.held(key, model); held && !prev {
		log.Printf("codex compaction: %s", msg)
		writeError(w, provider.Responses, http.StatusBadRequest, msg)
		return
	}
	if turn, ok := localCompactionTurn(r.Header.Get(turnMetadataHeader)); ok {
		r = r.Clone(r.Context())
		r.Header.Set(turnMetadataHeader, turn)
	}
	// the summary has compactWithin, Codex hearing nothing meanwhile: past
	// it, the summary is magpie's own, which Codex still takes (#5275)
	client := r.Context()
	budget, stop := context.WithCancelCause(client)
	timer := time.AfterFunc(compactWithin, func() { stop(sideTimedOut{"compaction summary", compactWithin}) })
	defer func() {
		timer.Stop()
		stop(context.Canceled)
	}()
	late := func() bool { _, ok := sideTimeout(budget); return ok }
	lateSummary := func() string {
		why := fmt.Sprintf("no summary came in %s, and Codex stops waiting at 5 minutes", compactWithin)
		log.Printf("codex compaction: %s: %s; compacting locally", model, why)
		return localSummary(body, why)
	}
	r = r.WithContext(budget)
	r = askWhole(r)
	r = askCompaction(r)
	ask := func(r *http.Request, b []byte) (*recorder, compactResult, error) {
		rec := &recorder{header: http.Header{}, status: 200}
		s.serve(rec, r, provider.Responses, b)
		if rec.status >= 400 {
			return rec, compactResult{}, nil
		}
		res, err := compactReply(rec.body.Bytes())
		if t := bytes.TrimSpace(rec.body.Bytes()); err == nil && res.Status == "" && (len(t) == 0 || t[0] != '{') {
			// a stream with no response.completed stopped before its end:
			// what it had said is no summary (#2274)
			err = errEndedShort
		}
		return rec, res, err
	}
	rec, res, err := ask(r, body)
	// A relay of the ChatGPT backend takes developer alone: the summary
	// ask went to it with the developer messages as system ones
	// (forVendor, #2955), and it refused every one, 400 "System messages
	// are not allowed", three of the failures holding the conversation
	// (compactFails, #5067) — while the turns around it, developer as
	// they came, are taken. Asked again with the roles as the client
	// sent them, the swap off, the summary is taken the way the turns
	// are; when that is refused too, the ask goes on as text, as for the
	// shapes below. An older sub2api says only "Upstream request failed"
	// for the same refusal, and one may say it in an error event after a
	// 200 (roleRefused); a summary with no developer message to give back
	// is not asked again the same
	if roleRefused(rec, err) && !late() && !bytes.Equal(developerItemsAsSystem(body), body) {
		who, msg := compactFailure(body, rec)
		if err != nil {
			msg = "broken off: " + err.Error()
		}
		log.Printf("codex compaction: %s answered %d (%s); asking again with the roles as the client sent them", who, rec.status, msg)
		rec, res, err = ask(askAsSent(r), body)
	}
	local := ""
	if late() {
		local = lateSummary()
		rec, res, err = &recorder{header: http.Header{}, status: 200}, compactResult{}, nil
	} else if first := rec.status; lacksEndpoint(first) || itemNotFound(rec) || inputKindRefused(rec, err) || roleRefused(rec, err) || protectionRefused(first, rec.body.Bytes()) ||
		summaryTooLarge(rec) || err != nil && protectionWords.MatchString(err.Error()) {
		who, msg := compactFailure(body, rec)
		if err != nil {
			msg = "broken off: " + err.Error()
		}
		log.Printf("codex compaction: %s answered %d (%s); asking again with the conversation as text", who, first, msg)
		rec, res, err = ask(r, plainCompact(body))
		if late() {
			local = lateSummary()
			rec, res, err = &recorder{header: http.Header{}, status: 200}, compactResult{}, nil
		} else if rec.status >= 400 || err != nil {
			again := ""
			if err != nil {
				again = "broken off: " + err.Error()
			} else {
				_, again = compactFailure(body, rec)
			}
			log.Printf("codex compaction: %s answered %d again (%s); compacting locally", who, rec.status, again)
			local = localSummary(body, fmt.Sprintf("%s answered %d to the summary request: %s", who, first, msg))
			rec, res, err = &recorder{header: http.Header{}, status: 200}, compactResult{}, nil
		}
	}
	if prev && local == "" && (rec.status >= 400 || err != nil) {
		// the thread's old model can't summarise it — Codex's sign-in used
		// up, the model gone — and the turn is on another: compacted
		// locally, the thread goes on on the model it was switched to,
		// where the refusal failed every turn of it (hiwise_aigc on X)
		who, msg := compactFailure(body, rec)
		if err != nil {
			msg = "broken off: " + err.Error()
		}
		log.Printf("codex compaction: %s answered %d (%s) on the model the thread is leaving; compacting locally", who, rec.status, msg)
		local = localSummary(body, fmt.Sprintf("%s answered %d to the summary request: %s", who, rec.status, msg))
		rec, res, err = &recorder{header: http.Header{}, status: 200}, compactResult{}, nil
	}
	if local == "" && (err != nil && strings.HasSuffix(err.Error(), errEndedShort.Error()) || summaryEndedShort(rec)) {
		// nobody left, and the last one's stream broke off before its end
		// (#2274): the words before the cut are no summary, and Codex,
		// told a 502, asked the same huge summary over, three times, until
		// the guard held it and the conversation could neither compact nor
		// go on (#3526). A translated reply that stopped short comes as a
		// 502 saying so (summaryEndedShort), the same cut. The summary is
		// magpie's own, as when no model can be asked at all; the summary
		// as text isn't tried, the cut was in what the model wrote, not in
		// what it was asked
		who, _ := compactFailure(body, rec)
		why := provider.APIError(rec.body.Bytes(), "")
		if err != nil {
			why = err.Error()
		}
		log.Printf("codex compaction: %s: %s; compacting locally", who, why)
		local = localSummary(body, why)
		rec, res, err = &recorder{header: http.Header{}, status: 200}, compactResult{}, nil
	}
	if rec.status >= 400 {
		// said as the compaction's failure, with who answered it: a bare
		// "Fake: 502 Bad Gateway" read as Codex's own, and a body with
		// nothing in it as "Unknown error" (outrageous_piglet on Discord);
		// the vendor's code, param and Retry-After go with it
		who, msg := compactFailure(body, rec)
		s.compactFails.failed(key, rec.status, msg)
		for k, vs := range rec.header {
			if k != "Content-Length" && k != "Content-Type" {
				w.Header()[k] = vs
			}
		}
		writeVendorError(w, provider.Responses, rec.status, compactFailed(who, rec.status, msg), rec.body.Bytes())
		return
	}
	if err == nil && local == "" && res.Status == "incomplete" {
		// cut short — its output limit — it's no summary (#2274)
		why := "unknown"
		var d struct {
			Reason string `json:"reason"`
		}
		if json.Unmarshal(res.IncompleteDetails, &d) == nil && d.Reason != "" {
			why = d.Reason
		}
		err = fmt.Errorf("%s (%s)", incompleteReply, why)
	}
	if err != nil && prev {
		who, _ := compactFailure(body, rec)
		log.Printf("codex compaction: %s: %v on the model the thread is leaving; compacting locally", who, err)
		local, err = localSummary(body, who+": "+err.Error()), nil
	}
	if err != nil {
		who, _ := compactFailure(body, rec)
		msg := "compaction: " + who + ": " + err.Error() + ", so there is no summary"
		s.compactFails.failed(key, http.StatusBadGateway, err.Error())
		writeError(w, provider.Responses, 502, msg)
		return
	}
	if local == "" && strings.TrimSpace(res.summary()) == "" && calledTool(res.Output) {
		// a model deep in a conversation of tool calls goes on calling
		// them, offered none or not: DeepSeek through a group answered the
		// summary request with reasoning and an exec_command call, 3 times
		// in a row (Lucy on Discord, auto-deepseek-v4-1-flash). Whatever
		// let it — an upstream that reads calls out of its text with no
		// tools offered, a history tool declared as a stub that it calls
		// anyway — the history's calls are what it follows. Asked again
		// with the conversation as text, there's no exchange of calls to
		// go on with; when it still writes no summary, magpie compacts
		// without a model, so Codex goes on rather than failing over and
		// over
		who, _ := compactFailure(body, rec)
		called := answeredWith(res.Output)
		log.Printf("codex compaction: %s wrote no summary%s; asking again with the conversation as text", who, called)
		prec, pres, perr := ask(r, plainCompact(body))
		switch {
		case late():
			local = lateSummary()
		case perr == nil && prec.status < 400 && pres.Status != "incomplete" && strings.TrimSpace(pres.summary()) != "":
			rec, res = prec, pres
		default:
			again := "no summary" + answeredWith(pres.Output)
			if perr != nil {
				again = perr.Error()
			} else if prec.status >= 400 {
				_, msg := compactFailure(body, prec)
				again = fmt.Sprintf("%d: %s", prec.status, msg)
			}
			log.Printf("codex compaction: %s asked again as text: %s; compacting locally", who, again)
			local = localSummary(body, who+" wrote no summary"+called+", asked again as text: "+again)
		}
	}
	var sum strings.Builder
	if local != "" {
		sum.WriteString(local)
	} else {
		sum.WriteString(res.summary())
	}
	if strings.TrimSpace(sum.String()) == "" && prev {
		sum.WriteString(localSummary(body, "the model wrote no summary"+answeredWith(res.Output)))
	}
	if strings.TrimSpace(sum.String()) == "" {
		why := "the model wrote no summary" + answeredWith(res.Output)
		s.compactFails.failed(key, http.StatusBadGateway, why)
		writeError(w, provider.Responses, 502, "compaction: "+why)
		return
	}
	if client.Err() != nil {
		// Codex left before it was answered: what was made never reaches
		// it, so the conversation is no shorter and is no success; Codex
		// asks the same huge summary again (#5275)
		why := "Codex left before the summary reached it"
		if local != "" {
			why += " (magpie's own)"
		}
		log.Printf("codex compaction: %s: %s", model, why)
		s.compactFails.failed(key, 499, why)
		return
	}
	s.compactFails.done(key)
	id := res.ID
	if id == "" {
		id = fmt.Sprintf("resp_magpie_%d", time.Now().UnixNano())
	}
	item := map[string]any{"type": "compaction", "id": "cmp_" + strings.TrimPrefix(id, "resp_"),
		"encrypted_content": codexsummary.Encrypted(sum.String())}
	done := map[string]any{"id": id, "object": "response", "status": "completed", "output": []any{item}}
	if len(res.Usage) > 0 {
		done["usage"] = res.Usage
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	w.WriteHeader(200)
	for _, ev := range []map[string]any{
		{"type": "response.created", "response": map[string]any{"id": id, "object": "response", "status": "in_progress", "output": []any{}}},
		{"type": "response.output_item.added", "output_index": 0, "item": item},
		{"type": "response.output_item.done", "output_index": 0, "item": item},
		{"type": "response.completed", "response": done},
	} {
		b, _ := json.Marshal(ev)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev["type"], b)
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// compactFailed is what Codex is told of a summary request that failed:
// that it was the compaction's, who answered it and what they said —
// never nothing, which Codex shows as "Unknown error".
func compactFailed(who string, status int, msg string) string {
	if strings.TrimSpace(msg) == "" {
		msg = "nothing more"
	}
	return fmt.Sprintf("compaction: %s answered the summary request %d %s: %s", who, status, http.StatusText(status), msg)
}

// lacksEndpoint is a status saying the provider has no summary request to
// answer at that address: 404, 405 (a relay that takes GETs there) or 501.
// The summary is asked again as plain text, and made without a model when
// that fails too, as for a relay's 404 (#866).
func lacksEndpoint(status int) bool {
	return status == http.StatusNotFound || status == http.StatusMethodNotAllowed || status == http.StatusNotImplemented
}

// summaryEndedShort is a summary ask answered 502 because the reply
// stopped before its end: a translated one serve held whole and found
// cut (errEndedShort, #3526, Edward4226's third shape).
func summaryEndedShort(rec *recorder) bool {
	return rec.status == http.StatusBadGateway && strings.HasSuffix(provider.APIError(rec.body.Bytes(), ""), errEndedShort.Error())
}

// upstreamFailed is a relay's own word that the provider behind it turned
// the request away, without saying why: "flyapi: Upstream request failed"
// came as a 404 (#866), "apex: Upstream request failed" as a 400
// invalid_request_error (#1511). Codex asking the same relay itself
// compacts, so the summary request is asked again as text, and compacted
// without a model when that fails too.
func upstreamFailed(rec *recorder) bool {
	return rec.status >= 400 && rec.status < 500 && rec.status != http.StatusUnauthorized &&
		rec.status != http.StatusForbidden && rec.status != http.StatusTooManyRequests &&
		upstreamFailedWords.Match(rec.body.Bytes())
}

var upstreamFailedWords = lazyre.New(`(?i)upstream request failed`)

// roleRefused is a summary ask a relay turned away in words that may be
// the role the swap gave it: a relay of the ChatGPT backend takes
// developer alone, and the summary went to it with the developer messages
// as system ones (forVendor, #2955), refused "System messages are not
// allowed" (#5067) as a current sub2api passes it on, or "Upstream request
// failed" as an older one words every refusal (upstreamFailed) — as a 4xx,
// or as an error event in the summary's 200 stream, which had counted
// toward the guard without either retry. The summary is asked again the
// way the turns go (askAsSent), and as text when that is refused too.
func roleRefused(rec *recorder, err error) bool {
	if rec.status >= 400 {
		return upstreamFailed(rec) || rec.status == http.StatusBadRequest && systemRefusedWords.Match(rec.body.Bytes())
	}
	return err != nil && (systemRefusedWords.MatchString(err.Error()) || upstreamFailedWords.MatchString(err.Error()))
}

var systemRefusedWords = lazyre.New(`(?i)system messages are not allowed`)

// summaryTooLarge is a summary request refused for its size: past the
// vendor's byte cap (413; DeepSeek's "Failed to buffer the request body:
// length limit exceeded", celestial_eagle_99230 on Discord) or past the
// model's window. Codex's remote compaction asks nothing smaller after
// either (it trims only by its token estimate against the catalog's
// window, and a 1M window holds more bytes than the cap takes), so told
// of it, Codex could neither compact nor go on. The summary is asked
// again as text, its images left out and long outputs cut
// (plainCompact), and compacted without a model when that is refused
// too.
func summaryTooLarge(rec *recorder) bool {
	return rec.status == http.StatusRequestEntityTooLarge || overflowed(rec.status, rec.body.Bytes())
}

// itemNotFound is a 400 refusing an item of the input by its id, as Azure
// OpenAI's and OpenAI's API answer one they never stored ("Item with id
// 'rs_…' not found", #1008), which a relay in front of them passes on.
func itemNotFound(rec *recorder) bool {
	b := rec.body.Bytes()
	return rec.status == http.StatusBadRequest && unreadableItem.Match(b) && bytes.Contains(bytes.ToLower(b), []byte("not found"))
}

// inputKindRefused is a summary request turned away for a kind of item in
// its input the provider doesn't take, whatever else the conversation
// holds: Command Code's /responses takes messages, function calls, their
// outputs and reasoning, and answers anything else 400
// "unsupported_input_item" with param "input" (#5181, Edward4226; #5259,
// Mmx233, a Codex subagent's history), and Grok's backend an
// agent_message 422 "unknown item type". It comes as that status, as
// another magpie's message in front of it ("Remote magpie: Command Code:
// {…}", a 400 or its 502), or as an error event in the summary's stream.
// The conversation as text is messages alone, which it takes. A refusal
// of the key or account (401, 403, 429) is no such answer.
func inputKindRefused(rec *recorder, err error) bool {
	switch rec.status {
	case http.StatusBadRequest, http.StatusUnprocessableEntity, http.StatusBadGateway:
	default:
		if err == nil || rec.status >= 400 {
			return false
		}
	}
	return inputKindWords.Match(rec.body.Bytes())
}

var inputKindWords = lazyre.New(`(?i)unsupported_input_item|unknown item type`)

// compactFailure names who answered a summary request with a failure — the
// provider, the model and the provider's base URL — and what it said.
func compactFailure(body []byte, rec *recorder) (who, msg string) {
	model := modelOf(body)
	who = cmp.Or(rec.header.Get(providerHeader), strings.SplitN(model, "/", 2)[0]) +
		" (" + cmp.Or(rec.header.Get(modelHeader), model)
	if p, _, ok := provider.Resolve(model); ok {
		if u := cmp.Or(p.Base(provider.Responses), p.Base(provider.Chat), p.Base(provider.Anthropic)); u != "" {
			who += ", " + u
		}
	}
	return who + ")", provider.APIError(rec.body.Bytes(), http.StatusText(rec.status))
}

// compactItems is a summary request and its input's items.
func compactItems(body []byte) (map[string]json.RawMessage, []map[string]any) {
	var q map[string]json.RawMessage
	if json.Unmarshal(body, &q) != nil {
		return nil, nil
	}
	var items []map[string]any
	json.Unmarshal(q["input"], &items)
	return q, items
}

// itemText is what an input item says, as text: a message's text parts, a
// call's name and arguments, a call's output (its middle left out when
// long), reasoning's summary.
func itemText(it map[string]any) (role, text string) {
	str := func(v any) string { s, _ := v.(string); return s }
	parts := func(v any) string {
		if s, ok := v.(string); ok {
			return s
		}
		var ts []string
		ps, _ := v.([]any)
		for _, p := range ps {
			if m, ok := p.(map[string]any); ok && str(m["text"]) != "" {
				ts = append(ts, str(m["text"]))
			}
		}
		return strings.Join(ts, "\n")
	}
	switch str(it["type"]) {
	case "message", "":
		return cmp.Or(str(it["role"]), "user"), parts(it["content"])
	case "agent_message":
		// a Codex subagent's task, or a message between agents: the
		// user's turn for the agent that receives it, as a translated
		// request reads it (#5259)
		return "user", parts(it["content"])
	case "function_call", "custom_tool_call":
		return "tool call", str(it["name"]) + " " + cmp.Or(str(it["arguments"]), str(it["input"]))
	case "function_call_output", "custom_tool_call_output":
		out := parts(it["output"])
		if len(out) > 4000 {
			out = out[:2000] + "\n…\n" + out[len(out)-2000:]
		}
		return "tool output", out
	case "reasoning":
		return "reasoning", parts(it["summary"])
	}
	return "", ""
}

// plainCompact is a summary request with the conversation in it as text, in
// one user message, and Codex's request to summarise it after: no item ids,
// sealed reasoning or calls a provider may look up and not find.
func plainCompact(body []byte) []byte {
	q, items := compactItems(body)
	if q == nil {
		return body
	}
	var b strings.Builder
	for _, it := range items {
		role, text := itemText(it)
		if strings.TrimSpace(text) == "" || text == codexCompactPrompt {
			continue
		}
		fmt.Fprintf(&b, "[%s]\n%s\n\n", role, text)
	}
	t := b.String()
	if len(t) > plainCompactMax {
		t = "…\n" + t[len(t)-plainCompactMax:]
	}
	q["input"], _ = json.Marshal([]map[string]any{
		userMessage("This is the conversation so far:\n\n" + t),
		userMessage(codexCompactPrompt),
	})
	delete(q, "previous_response_id")
	nb, err := marshalJSON(q)
	if err != nil {
		return body
	}
	return nb
}

// plainCompactMax bounds the conversation's text a plain summary request
// carries; the oldest of it is left out.
const plainCompactMax = 400_000

// localSummaryMax bounds the user's messages a local summary keeps, the
// latest of them, as Codex's own compaction does.
const localSummaryMax = 80_000

// localSummary is a summary made without a model: what the user asked (the
// summaries before this one with it), the latest of it kept, and the last
// reply.
func localSummary(body []byte, why string) string {
	_, items := compactItems(body)
	var asked []string
	last := ""
	for _, it := range items {
		role, text := itemText(it)
		text = strings.TrimSpace(text)
		switch {
		case text == "" || text == codexCompactPrompt:
		case role == "user":
			asked = append(asked, text)
		case role == "assistant":
			last = text
		}
	}
	from, n := len(asked), 0
	for from > 0 && n+len(asked[from-1]) <= localSummaryMax {
		from--
		n += len(asked[from])
	}
	var b strings.Builder
	b.WriteString("magpie compacted this conversation without a model (" + why + "), so this is no summary: it is what the user asked")
	if from > 0 {
		fmt.Fprintf(&b, " (the latest %d of %d messages)", len(asked)-from, len(asked))
	}
	b.WriteString(" and the last reply. Look at the workspace for where the work stands before going on.\n\n## The user's messages\n")
	for _, a := range asked[from:] {
		b.WriteString("\n" + a + "\n")
	}
	if last != "" {
		b.WriteString("\n## The last reply\n\n" + last + "\n")
	}
	return b.String()
}

type compactOutput struct {
	Type    string `json:"type"`
	Name    string `json:"name"` // a call's tool
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

type compactResult struct {
	ID     string          `json:"id"`
	Output []compactOutput `json:"output"`
	Usage  json.RawMessage `json:"usage"`
	// Status is how the response ended ("completed", "incomplete"), and
	// IncompleteDetails why when it was cut short: max_output_tokens (#1466)
	Status            string          `json:"status"`
	IncompleteDetails json.RawMessage `json:"incomplete_details"`
	Error             *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// summary is the text of a summary reply's messages.
func (res compactResult) summary() string {
	var b strings.Builder
	for _, o := range res.Output {
		if o.Type != "message" {
			continue
		}
		for _, c := range o.Content {
			b.WriteString(c.Text)
		}
	}
	return b.String()
}

// calledTool is whether a summary reply called a tool: a function_call,
// custom_tool_call, tool_search_call, a hosted tool's call.
func calledTool(out []compactOutput) bool {
	return slices.ContainsFunc(out, func(o compactOutput) bool { return strings.HasSuffix(o.Type, "_call") })
}

// answeredWith says what a summary reply without a summary held instead
// — " (it answered with function_call shell)", " (it answered with
// reasoning)", " (it answered with nothing)" — so the failure says whether
// the model called a tool, only thought, or said nothing.
func answeredWith(out []compactOutput) string {
	if len(out) == 0 {
		return " (it answered with nothing)"
	}
	var kinds []string
	for _, o := range out {
		k := cmp.Or(o.Type, "an untyped item")
		if o.Name != "" {
			k += " " + o.Name
		}
		if !slices.Contains(kinds, k) {
			kinds = append(kinds, k)
		}
	}
	return " (it answered with " + strings.Join(kinds, ", ") + ")"
}

// compactReply reads the summary's reply, a Responses stream as Codex asks
// for it or a response as a whole: the items from output_item.done (the
// ChatGPT backend's completed response lists none), usage from completed.
func compactReply(b []byte) (compactResult, error) {
	var res compactResult
	t := bytes.TrimSpace(b)
	if len(t) > 0 && t[0] == '{' {
		err := json.Unmarshal(t, &res)
		return res, err
	}
	var items []compactOutput
	var failed string
	readSSE(bytes.NewReader(b), func(_, data string) error {
		var ev struct {
			Type     string         `json:"type"`
			Item     compactOutput  `json:"item"`
			Response *compactResult `json:"response"`
			Message  string         `json:"message"` // an error event's
			Error    *struct {
				Message string `json:"message"`
			} `json:"error"` // or nested in its error, as the ChatGPT backend's (#1270)
		}
		if json.Unmarshal([]byte(data), &ev) != nil {
			return nil
		}
		if ev.Response != nil && ev.Response.ID != "" {
			res.ID = ev.Response.ID
		}
		switch ev.Type {
		case "response.output_item.done":
			items = append(items, ev.Item)
		case "response.completed", "response.incomplete":
			if ev.Response != nil {
				res.Output, res.Usage = ev.Response.Output, ev.Response.Usage
				res.Status, res.IncompleteDetails = ev.Response.Status, ev.Response.IncompleteDetails
			}
			if res.Status == "" {
				res.Status = strings.TrimPrefix(ev.Type, "response.")
			}
		case "response.failed", "error":
			failed = ev.Message
			if failed == "" && ev.Error != nil {
				failed = ev.Error.Message
			}
			failed = cmp.Or(failed, "the model failed")
			if ev.Response != nil && ev.Response.Error != nil && ev.Response.Error.Message != "" {
				failed = ev.Response.Error.Message
			}
		}
		return nil
	})
	if failed != "" {
		return res, errors.New(failed)
	}
	if len(items) > 0 {
		res.Output = items
	}
	return res, nil
}

// recorder keeps a reply for a second look.
type recorder struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (r *recorder) Header() http.Header         { return r.header }
func (r *recorder) WriteHeader(status int)      { r.status = status }
func (r *recorder) Write(b []byte) (int, error) { return r.body.Write(b) }
