package gateway

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/codexcat"
	"github.com/yetone/magpie/internal/provider"
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
// only magpie reads back.
const magpieCompaction = "magpie1:"

var nativeSealedAgentPayload = regexp.MustCompile(`^gAAAAA[A-Za-z0-9_-]+={0,2}$`)

// codexCompactPrompt and codexSummaryPrefix are Codex's own (Apache-2.0,
// openai/codex, prompts/templates/compact).
const codexCompactPrompt = `You are performing a CONTEXT CHECKPOINT COMPACTION. Create a handoff summary for another LLM that will resume the task.

Include:
- Current progress and key decisions made
- Important context, constraints, or user preferences
- What remains to be done (clear next steps)
- Any critical data, examples, or references needed to continue

Be concise, structured, and focused on helping the next LLM seamlessly continue the work.`

const codexSummaryPrefix = `Another language model started to solve this problem and produced a summary of its thinking process. You also have access to the state of the tools that were used by that language model. Use this to build on the work that has already been done and avoid duplicating work. Here is the summary produced by the other language model, use the information in this summary to assist with your own analysis:`

func (s *Server) codexBackend(w http.ResponseWriter, r *http.Request) {
	// Responses over a WebSocket: 426 sends Codex to plain HTTP at once
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		http.Error(w, "magpie speaks HTTP", http.StatusUpgradeRequired)
		return
	}
	// the pool's accounts and their model lists say they are this Codex, or
	// newer: the backend serves a model only to a client that knows it
	provider.SawCodexClient(r.Header)
	rest := strings.TrimPrefix(r.URL.Path, CodexPath)
	body, ok := s.readRequestBody(w, r, provider.Responses, 0)
	if !ok {
		return
	}
	var err error
	switch {
	case r.Method == http.MethodGet && rest == "/models":
		s.codexModels(w, r)
		return
	case r.Method == http.MethodPost && (rest == "/responses" || rest == "/responses/compact"):
		var model string
		body, model, err = requestModel(body)
		if err != nil {
			writeError(w, provider.Responses, 400, err.Error())
			return
		}
		// a thread's title, where Settings sends it (#705) — the request
		// still on Codex's own model, through its sign-in, when it says
		// nothing of them
		if rest == "/responses" {
			if to := codexTitlesTo(r.Header, body, false); to != "" {
				s.codexTitle(w, r, body, to)
				return
			}
		}
		// The namespace owns the route even if a model is not in the catalog.
		// Unknown providers/groups must fail locally, never fall through to OpenAI.
		if strings.Contains(model, "/") {
			if rest == "/responses/compact" {
				writeError(w, provider.Responses, 400, "/responses/compact is not supported for Magpie models; use a compaction_trigger on /responses")
				return
			}
			// a sealed subagent task goes to a ChatGPT account the model
			// or group has, or is turned away (serve, sealedReader)
			body, compact := codexInput(body, true)
			if compact {
				s.codexCompact(w, r, body)
				return
			}
			s.serveAgent(w, r, provider.Responses, body)
			return
		}
		body = boundCallIDs(callItemIDs(body))
		if rest == "/responses/compact" {
			// a key held to some accounts (#905) may not spend the sign-in
			// compaction still relays on: refused as its turns are (#967),
			// with nothing asked of OpenAI — and refused when the sign-in
			// can't be shown to be the key's, switched off or gone
			if who, held := compactSigninHeld(r, model); held {
				writeError(w, provider.Responses, 403, compactHeldError(who, model))
				return
			}
			break // preserve native compaction's existing passthrough
		}
		body, _ = codexInput(body, false)
		if id, ok := codexAccounts(r, model); ok {
			s.serveAgent(w, r, provider.Responses, withModel(body, id))
			return
		}
		if r.Header.Get(AccountHeader) != "" {
			// relayed as it came, it would go to Codex's own sign-in only
			writeError(w, provider.Responses, 400, AccountHeader+" names one of magpie's Codex accounts, and Codex isn't signed in to ChatGPT here with any on in magpie")
			return
		}
	}
	s.codexUpstream(w, r, rest, body)
}

// sealedTaskError is what a subagent is told whose task its lead sealed
// when nothing model names can read it. lead is the provider that
// answered the lead, "" when magpie didn't (the lead was one of Codex's
// own models) or no longer remembers it.
func sealedTaskError(model, lead string) string {
	if lead != "" {
		return fmt.Sprintf("This subagent's task was sealed by the server that answered its lead (%s), and only that server can open it; %s can't. Give the subagent the lead's model, or a lead model that isn't served by the ChatGPT backend, so its subagents get a task they can read.", lead, model)
	}
	return fmt.Sprintf("This subagent's task was sealed by the ChatGPT backend that answered its lead, and only that backend can open it: a ChatGPT account, or the Responses API provider that answered the lead. %s is neither. Give the subagent the lead's model, or a lead model that isn't served by the ChatGPT backend, so its subagents get a task they can read.", model)
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
// pl's order with them.
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
		return cands, pl, ""
	}
	for i, c := range cands {
		if i > 0 && c.who() == st.who {
			cands = append(append([]candidate{c}, cands[:i]...), cands[i+1:]...)
			pl.order = append(append([]Weighed{pl.order[i]}, pl.order[:i]...), pl.order[i+1:]...)
			return cands, pl, c.rest
		}
	}
	if len(cands) > 0 && cands[0].who() == st.who {
		return cands, pl, cands[0].rest
	}
	return cands, pl, ""
}

// Only native sealed agent tasks need this guidance. Other encrypted_content
// fields (including ordinary model history) keep their existing handling.
func hasSealedAgentMessage(body []byte) bool {
	var request map[string]json.RawMessage
	if json.Unmarshal(body, &request) != nil {
		return false
	}
	var items []json.RawMessage
	if json.Unmarshal(request["input"], &items) != nil {
		return false
	}
	for _, raw := range items {
		var item struct {
			Type    string            `json:"type"`
			Content []json.RawMessage `json:"content"`
		}
		if json.Unmarshal(raw, &item) != nil || item.Type != "agent_message" {
			continue
		}
		for _, content := range item.Content {
			var part struct {
				Type             string `json:"type"`
				EncryptedContent string `json:"encrypted_content"`
			}
			if json.Unmarshal(content, &part) == nil && part.Type == "encrypted_content" && nativeSealedAgentPayload.MatchString(part.EncryptedContent) {
				return true
			}
		}
	}
	return false
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
func compactSigninHeld(r *http.Request, model string) (access.Identity, bool) {
	who, held := accountHolds(r)
	if !held || provider.CodexAPIKeySignedIn() {
		return who, false
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

// codexAccounts is what a request for one of Codex's own models is served
// as when Codex is signed in to ChatGPT and more of its accounts are on in
// magpie: the codex subscription's model (codex/<model>), which goes to the
// account Codex is signed in to and, when that one is out of its allowance
// or rate limited, on to the next — as it can't when relayed as it came.
// A gateway key held to some models or accounts (#882, #905) is served
// through it always, alone no less: the relay would spend Codex's own
// sign-in with nothing of the key asked, its accounts and its models both.
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
		if share, _ := provider.HoldShare(p, "codex", p.Account.User); share > 0 {
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
	if !ok || p.Account == nil || p.Account.Agent != "codex" || len(p.AlsoOn()) == 0 && !pinned {
		return "", false
	}
	return id, true
}

// withModel is a request body asking for another model.
func withModel(body []byte, model string) []byte {
	var q map[string]json.RawMessage
	if json.Unmarshal(body, &q) != nil {
		return body
	}
	q["model"], _ = json.Marshal(model)
	b, err := json.Marshal(q)
	if err != nil {
		return body
	}
	return b
}

// codexUpstream relays a request as it came, the sign-in included, to where
// Codex would have sent it.
func (s *Server) codexUpstream(w http.ResponseWriter, r *http.Request, rest string, body []byte) {
	start := time.Now()
	usage.Saw(agentOf(r))
	if r.Method == http.MethodPost {
		var unmask func()
		w, body, unmask = redacted(w, body)
		defer unmask()
	}
	base := provider.CodexBase
	if apiKey(r.Header) && rest != "/models" {
		base = codexAPIBase
	}
	u := base + rest
	if r.URL.RawQuery != "" {
		u += "?" + r.URL.RawQuery
	}
	// a turn goes in the Routing view's live trace as the others do, to
	// the one it can go to: Codex's own sign-in, or its key
	var tr *Route
	first := firstToken{start: start} // the reply's first tokens (#196), counted as ms are
	served := ""                      // the model the reply says answered
	var uu Usage
	metadata := requestSessionMetadata(r.Header, body)
	kind := requestCallKind(r.Header, metadata)
	var titleReply capturedBody // bounded; ordinary streams incur no copy
	captureTitle := false
	effort := "" // the reasoning Codex asked for, which goes on as it is
	end := func(status int, msg string, tokens, out int) {}
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
		effort = requestEffort(provider.Responses, body)
		seat := Weighed{ID: "codex", Provider: "openai", Name: "OpenAI", Icon: "openai", Who: who, Kind: "account", Agent: "codex", Model: model}
		link := s.titlePrompts.observe(r, body, metadata, kind, start)
		captureTitle = link != nil && isTitleKind(kind)
		tr = s.trace.begin(Route{imageTurn: drawingTurnID(metadata.Turn), imageCaller: codexTurnKey(r, callerOf(r).agent), imageProvider: imageProvider, TitleLink: link, Time: start, Agent: agentOf(r), Session: sessionOf(r.Header), Conv: convOf(r.Header, body), ParentSession: titleParentSession(r.Header, metadata, kind), Kind: kind, Model: model, Effort: effort, Provider: "openai",
			Order: []Weighed{seat}, Tries: []Try{{ID: seat.ID, Model: model, Effort: effort, Start: start}}})
		promptRead := s.inspectPrompt(tr, provider.Responses, body)
		end = func(status int, msg string, tokens, out int) {
			ms := time.Since(start).Milliseconds()
			ttft, text := first.ms()
			replyDigest := ""
			if captureTitle && status < 400 && msg == "" && !titleReply.truncated {
				replyDigest = titleReplyDigest(titleReply.buf.Bytes(), nil)
			}
			prompt := promptRead() // outside the trace's lock, which reading it takes
			s.trace.update(tr, func(t *Route) {
				t.Tries[0].Done, t.Tries[0].Status, t.Tries[0].Millis, t.Tries[0].Error = true, status, ms, msg
				t.Tries[0].TTFT, t.Tries[0].FirstText = ttft, text
				t.Done, t.Status, t.Error, t.Millis, t.Tokens = true, status, msg, ms, tokens
				if t.TitleLink != nil && isTitleKind(kind) && status < 400 && msg == "" && !titleReply.truncated {
					link := *t.TitleLink
					link.Reply = replyDigest
					t.TitleLink = &link
				}
				t.Output, t.Reasoning, t.TTFT, t.FirstText = out, uu.Reasoning, ttft, text
				flow := first.flowFor(uu.Reasoning)
				t.Flow, t.Tries[0].Flow = flow, flow
				t.Usage = routeUsage("openai", model, uu)
				if prompt != nil {
					t.Prompt = prompt.calibrated(promptCounted(t.Usage), catalog.ContextOf(model))
				}
				t.Tries[0].Served, t.Tries[0].Swapped = served, swapped(model, served)
				t.Served, t.Swapped = t.Tries[0].Served, t.Tries[0].Swapped
			})
		}
	}
	var res *http.Response
	autoReset := false // a Codex reset looked at, once
	resetNote := ""    // what spending it did, for the request log
	for tries := 0; ; tries++ {
		req, err := http.NewRequestWithContext(provider.ViaSignedIn(r.Context(), "codex"), r.Method, u, bytes.NewReader(body))
		if err != nil {
			writeError(w, provider.Responses, 502, err.Error())
			end(502, err.Error(), 0, 0)
			return
		}
		copyHeaders(req.Header, r.Header)
		// left to the transport, the reply comes back plain for the usage in it
		req.Header.Del("Accept-Encoding")
		if res, err = s.client.Do(req); err != nil {
			msg := "OpenAI: " + err.Error()
			if rest == "/responses" {
				msg = codexUnreached(modelOf(body), err)
			}
			writeError(w, provider.Responses, 502, msg)
			end(502, msg, 0, 0)
			return
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
					s.trace.update(tr, func(t *Route) { t.Tries[0].Reset = &AutoReset{Who: who, Text: out.Text()} })
					resetNote = "openai (" + who + "): used one of its resets by itself (" + out.Text() + ")"
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
		// a web page or nothing at all, served 200, is the 502 it stands
		// for (#1012)
		res = notAnAPIReply(res, "OpenAI: ")
	}
	defer res.Body.Close()
	if base == codexAPIBase && res.StatusCode == http.StatusUnauthorized {
		// Codex signed in with an API key OpenAI refuses — often one a
		// relay issued, left in ~/.codex/auth.json — and one of Codex's own
		// models was picked, which goes out with Codex's own sign-in
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		writeError(w, provider.Responses, res.StatusCode, codexKeyRefused(msg))
		s.record(Call{Time: start, From: provider.Responses, To: provider.Responses, Model: modelOf(body),
			Provider: "openai", Agent: agentOf(r), Status: res.StatusCode,
			Millis: time.Since(start).Milliseconds(), Error: res.Status})
		end(res.StatusCode, res.Status, 0, 0)
		return
	}
	if rest == "/responses" && res.StatusCode >= 400 {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body = io.NopCloser(bytes.NewReader(msg))
		if len(bytes.TrimSpace(msg)) == 0 {
			// a failure with nothing said of it, which Codex shows as
			// "Unknown error" alone (#409): said where the turn went
			said := codexFailedEmpty(modelOf(body), res.Status)
			writeError(w, provider.Responses, res.StatusCode, said)
			s.record(Call{Time: start, From: provider.Responses, To: provider.Responses, Model: modelOf(body),
				Provider: "openai", Agent: agentOf(r), Kind: callKind(r.Header), Status: res.StatusCode,
				Millis: time.Since(start).Milliseconds(), Error: said})
			end(res.StatusCode, said, 0, 0)
			return
		}
	}
	for k, vs := range res.Header {
		if !hopHeader(k) {
			w.Header()[k] = vs
		}
	}
	modelsEtag(w.Header())
	w.WriteHeader(res.StatusCode)
	var sniff *usageSniffer
	if rest == "/responses" {
		ct := res.Header.Get("Content-Type")
		if ct == "" && streamOf(body) { // the ChatGPT backend streams without saying so
			ct = "text/event-stream"
		}
		sniff = newSniffer(provider.Responses, ct)
	}
	f, _ := w.(http.Flusher)
	buf := make([]byte, 32<<10)
	var refusal []byte // what OpenAI said, of a request it turned away
	for {
		n, err := res.Body.Read(buf)
		if n > 0 {
			if captureTitle {
				titleReply.add(buf[:n])
			}
			if sniff != nil {
				sniff.write(buf[:n])
				first.see(buf[:n])
			}
			if res.StatusCode >= 400 && len(refusal) < 8<<10 {
				refusal = append(refusal, buf[:n]...)
			}
			if _, werr := w.Write(buf[:n]); werr != nil {
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
	if sniff == nil {
		return
	}
	call := Call{Time: start, From: provider.Responses, To: provider.Responses, Model: modelOf(body),
		Provider: "openai", Agent: agentOf(r), Kind: kind, Status: res.StatusCode,
		Millis: time.Since(start).Milliseconds(), Fallback: resetNote}
	call.TTFT, call.FirstText = first.ms()
	uu.add(sniff.usage())
	call.Flow = first.flowFor(uu.Reasoning)
	call.Usage, served = uu, uu.Served
	errType := ""
	if res.StatusCode >= 400 {
		// its words, the status in front when it said none: the log and
		// the Routing view show why, not just that
		call.Error = provider.APIError(refusal, res.Status)
		errType = provider.ErrorType(refusal)
	}
	end(call.Status, call.Error, uu.Input+uu.Output+uu.CacheRead+uu.CacheWrite, uu.Output)
	s.record(call)
	rec := usage.Record{RouteID: tr.ID, Time: start, Agent: call.Agent, Provider: call.Provider, Host: provider.HostOf(base), Model: call.Model,
		Requested: call.Model, Served: served,
		Input: uu.Input, Output: uu.Output, CacheRead: uu.CacheRead, CacheWrite: uu.CacheWrite,
		Reasoning: uu.Reasoning, Effort: effort, Millis: call.Millis, TTFT: call.TTFT, FirstText: call.FirstText, Flow: call.Flow, Status: call.Status, Session: sessionOf(r.Header), NativeSession: nativeSessionOf(r.Header), Kind: call.Kind,
		RequestID: requestID(res.Header), ResponseID: uu.ResponseID, Endpoint: r.URL.Path}
	failedWith(&rec, call.Status, call.Error, errType)
	appendUsage(r, rec)
}

// unreadableItem is the item OpenAI's refusal names: sealed content it
// can't verify, or an id it doesn't have ("Item with id 'rs_…' not found").
var unreadableItem = regexp.MustCompile(`(?i)(?:encrypted content for item|item with id) '?([A-Za-z]+_[A-Za-z0-9_-]+)'?`)

// withoutUnreadable is body without what OpenAI's refusal msg says it
// can't read: the item it names, or the reasoning another account sealed.
func withoutUnreadable(body, msg []byte) ([]byte, bool) {
	if !foreignReasoning.Match(msg) && !bytes.Contains(bytes.ToLower(msg), []byte("not found")) {
		return nil, false
	}
	if m := unreadableItem.FindSubmatch(msg); m != nil {
		if b, ok := withoutItem(body, string(m[1])); ok {
			return b, true
		}
	}
	if foreignReasoning.Match(msg) {
		return withoutReasoning(body)
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
	b, err := json.Marshal(q)
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
	case "session_id", "conversation_id", "x-client-request-id", "version":
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
// magpie runs for a model that can't search is "web_search".
func callKind(h http.Header) string {
	v := strings.TrimSpace(h.Get("x-openai-subagent"))
	if v == "" && h.Get("x-openai-memgen-request") != "" {
		v = "memgen"
	}
	if v == "" {
		v = threadSource(h.Get("x-codex-turn-metadata"))
	}
	if v == "" && h.Get("User-Agent") == SearchAgent {
		v = "web_search"
	}
	if v == "" && h.Get("User-Agent") == VisionAgent {
		v = "vision"
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
		if !hopHeader(k) && http.CanonicalHeaderKey(k) != "Host" && http.CanonicalHeaderKey(k) != AccountHeader {
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

// codexModels is the ChatGPT backend's model list for this sign-in, with
// magpie's models after it. Should the backend not answer, or not in
// time, Codex's last list of its own stands in.
func (s *Server) codexModels(w http.ResponseWriter, r *http.Request) {
	var own []any
	etag := ""
	u := provider.CodexBase + "/models"
	if r.URL.RawQuery != "" {
		u += "?" + r.URL.RawQuery
	}
	ctx, cancel := context.WithTimeout(provider.ViaSignedIn(r.Context(), "codex"), codexModelsWait)
	defer cancel()
	if req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil); err == nil {
		copyHeaders(req.Header, r.Header)
		req.Header.Del("Accept-Encoding")
		if res, err := s.client.Do(req); err == nil {
			var list struct {
				Models []any `json:"models"`
			}
			b, _ := io.ReadAll(io.LimitReader(res.Body, 16<<20))
			res.Body.Close()
			if res.StatusCode < 300 && json.Unmarshal(b, &list) == nil {
				own, etag = list.Models, res.Header.Get("ETag")
				// the versions as the backend says them, before any is
				// stamped below (see codexcat.V1)
				codexcat.Remember(own)
			} else if res.StatusCode == 401 || res.StatusCode == 403 {
				// a sign-in to renew is Codex's to see
				w.Header().Set("Content-Type", res.Header.Get("Content-Type"))
				w.WriteHeader(res.StatusCode)
				w.Write(b)
				return
			}
		}
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
	if keep, narrowed := provider.CodexNativePicked(); narrowed {
		kept := own[:0]
		for _, m := range own {
			o, _ := m.(map[string]any)
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
	for _, m := range own {
		if o, ok := m.(map[string]any); ok {
			codexcat.AutoReview(o)
		}
	}
	ms := provider.CodexListed()
	// the list is the backend's and magpie's, and so is its ETag
	w.Header().Set("ETag", codexcat.WithTag(etag, provider.CodexListTag()))
	all := append(own, codexcat.Entries(ms, len(own)+100)...)
	if at, ok := provider.CodexOrder(); ok {
		codexcat.Order(all, at)
	}
	writeJSON(w, 200, map[string]any{"models": all})
}

// modelsEtag is the X-Models-Etag of a backend reply as Codex should read
// it: with magpie's models in it, as the list's own ETag has them, so a
// change to either has Codex ask for the list again.
func modelsEtag(h http.Header) {
	if v := h.Get("X-Models-Etag"); v != "" {
		h.Set("X-Models-Etag", codexcat.WithTag(v, provider.CodexListTag()))
	}
}

// codexInput restores summaries magpie made. For a magpie model, it also
// replaces Codex's compaction trigger with a request to summarise the input.
// OpenAI's own models keep the trigger for their backend to handle.
func codexInput(body []byte, magpieModel bool) (_ []byte, compact bool) {
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
			sum, ok := strings.CutPrefix(enc, magpieCompaction)
			if !ok {
				out = append(out, it) // OpenAI's own, for OpenAI
				continue
			}
			changed = true
			if b, err := base64.StdEncoding.DecodeString(sum); err == nil {
				out = append(out, userMessage(codexSummaryPrefix+"\n"+string(b)))
			}
		case "compaction_trigger":
			if !magpieModel {
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
				if enc, _ := it["encrypted_content"].(string); enc == "" {
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
	b, _ := json.Marshal(out)
	q["input"] = b
	if compact {
		// the summary is text; a tool call would be no summary. Otherwise
		// it goes as Codex asks for its own summary, streamed with its
		// tool_choice: a relay in front of the ChatGPT backend turns away
		// one that isn't ("invalid codex request", #292)
		delete(q, "tools")
		q["tool_choice"] = json.RawMessage(`"auto"`)
		q["parallel_tool_calls"] = json.RawMessage("false")
	}
	nb, err := json.Marshal(q)
	if err != nil {
		return body, false
	}
	return nb, compact
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
	if !bytes.Contains(body, []byte(`"reasoning"`)) {
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
	q["input"], _ = json.Marshal(kept)
	b, err := json.Marshal(q)
	if err != nil {
		return body
	}
	return b
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

// longCallID finds a call_id longer than the ChatGPT backend takes.
var longCallID = regexp.MustCompile(`"call_id"\s*:\s*"[^"]{65,}"`)

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
// Under any other name Codex leaves them out itself (client.rs, !is_openai).
var openaiOnly = [][]byte{[]byte(`"internal_chat_message_metadata_passthrough"`),
	[]byte(`"encrypted_function_args"`), []byte(`"stream_options"`), []byte(`"configuration_update"`)}

// forVendor is a Responses request as Codex sends it to a provider not
// OpenAI's: without its messages' internal metadata, a call's
// encrypted_function_args, stream_options' reasoning_summary_delivery and
// configuration_update items.
// A relay that checks it is Codex's turned the lot away ("invalid codex
// request", #292). OpenAI's API and the ChatGPT backend get it as it came;
// so does anything else, byte for byte, when none of them is in it.
func forVendor(p provider.Provider, body []byte) []byte {
	if p.Account != nil && p.Account.Agent == "codex" || strings.HasSuffix(p.Host(), "openai.com") {
		return body
	}
	if !slices.ContainsFunc(openaiOnly, func(k []byte) bool { return bytes.Contains(body, k) }) {
		return body
	}
	var q map[string]json.RawMessage
	if json.Unmarshal(body, &q) != nil {
		return body
	}
	changed := false
	// Codex's stream_options holds reasoning_summary_delivery alone; another
	// client's other options stay
	var so map[string]json.RawMessage
	if json.Unmarshal(q["stream_options"], &so) == nil {
		if _, ok := so["reasoning_summary_delivery"]; ok {
			changed = true
			delete(so, "reasoning_summary_delivery")
			if q["stream_options"], _ = marshalPlain(so); len(so) == 0 {
				delete(q, "stream_options")
			}
		}
	}
	var items []json.RawMessage
	if json.Unmarshal(q["input"], &items) == nil {
		out := items[:0]
		for _, raw := range items {
			var it map[string]json.RawMessage
			if json.Unmarshal(raw, &it) != nil {
				out = append(out, raw)
				continue
			}
			if string(it["type"]) == `"configuration_update"` {
				changed = true
				continue
			}
			_, meta := it["internal_chat_message_metadata_passthrough"]
			_, sealed := it["encrypted_function_args"]
			if meta || sealed {
				changed = true
				delete(it, "internal_chat_message_metadata_passthrough")
				delete(it, "encrypted_function_args")
				raw, _ = marshalPlain(it)
			}
			out = append(out, raw)
		}
		if changed {
			q["input"], _ = marshalPlain(out)
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

func userMessage(text string) map[string]any {
	return map[string]any{"type": "message", "role": "user",
		"content": []map[string]any{{"type": "input_text", "text": text}}}
}

// codexCompact compacts a conversation held by a magpie model: the model
// summarises it, and the summary goes back to Codex as the compaction item
// the backend would have made, marked as magpie's so magpie reads it back
// in later requests.
//
// A provider that answers the summary 404 — a relay that serves the
// conversation turn by turn but not the summary of it, "Upstream request
// failed" (#866) — or 400 for an item of it it doesn't have (#1008) is
// asked once more with the conversation as plain text, none of its items'
// ids or sealed reasoning in it; when that fails too the summary is
// magpie's own, the conversation's user messages and last reply,
// so the compaction still completes and Codex goes on. So is one the
// ChatGPT backend answers 502 "response protection is unavailable" (vs on
// Discord): the same long history failed so on every account and model,
// and as plain text was summarised — and one it breaks off with that
// refusal after the reply began, HTTP 200 and an error event (#1270). Any
// other failure (401, 429, 500) goes back to Codex as it came.
func (s *Server) codexCompact(w http.ResponseWriter, r *http.Request, body []byte) {
	ask := func(b []byte) (*recorder, compactResult, error) {
		rec := &recorder{header: http.Header{}, status: 200}
		s.serve(rec, r, provider.Responses, b)
		if rec.status >= 400 {
			return rec, compactResult{}, nil
		}
		res, err := compactReply(rec.body.Bytes())
		return rec, res, err
	}
	rec, res, err := ask(body)
	local := ""
	if first := rec.status; first == http.StatusNotFound || itemNotFound(rec) || protectionRefused(first, rec.body.Bytes()) ||
		err != nil && protectionWords.MatchString(err.Error()) {
		who, msg := compactFailure(body, rec)
		if err != nil {
			msg = "broken off: " + err.Error()
		}
		log.Printf("codex compaction: %s answered %d (%s); asking again with the conversation as text", who, first, msg)
		rec, res, err = ask(plainCompact(body))
		if rec.status >= 400 || err != nil {
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
	if rec.status >= 400 {
		for k, vs := range rec.header {
			w.Header()[k] = vs
		}
		w.WriteHeader(rec.status)
		w.Write(rec.body.Bytes())
		return
	}
	if err != nil {
		writeError(w, provider.Responses, 502, "compaction: "+err.Error())
		return
	}
	var sum strings.Builder
	if local != "" {
		sum.WriteString(local)
	} else {
		for _, o := range res.Output {
			if o.Type != "message" {
				continue
			}
			for _, c := range o.Content {
				sum.WriteString(c.Text)
			}
		}
	}
	if strings.TrimSpace(sum.String()) == "" {
		writeError(w, provider.Responses, 502, "compaction: the model wrote no summary")
		return
	}
	id := res.ID
	if id == "" {
		id = fmt.Sprintf("resp_magpie_%d", time.Now().UnixNano())
	}
	item := map[string]any{"type": "compaction", "id": "cmp_" + strings.TrimPrefix(id, "resp_"),
		"encrypted_content": magpieCompaction + base64.StdEncoding.EncodeToString([]byte(sum.String()))}
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

// itemNotFound is a 400 refusing an item of the input by its id, as Azure
// OpenAI's and OpenAI's API answer one they never stored ("Item with id
// 'rs_…' not found", #1008), which a relay in front of them passes on.
func itemNotFound(rec *recorder) bool {
	b := rec.body.Bytes()
	return rec.status == http.StatusBadRequest && unreadableItem.Match(b) && bytes.Contains(bytes.ToLower(b), []byte("not found"))
}

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
	nb, err := json.Marshal(q)
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
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

type compactResult struct {
	ID     string          `json:"id"`
	Output []compactOutput `json:"output"`
	Usage  json.RawMessage `json:"usage"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
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
