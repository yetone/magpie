package gateway

import (
	"bytes"
	"cmp"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
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
	body, err := codexBody(r)
	if err != nil {
		writeError(w, provider.Responses, 400, err.Error())
		return
	}
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
		// The namespace owns the route even if a model is not in the catalog.
		// Unknown providers/groups must fail locally, never fall through to OpenAI.
		if strings.Contains(model, "/") {
			if rest == "/responses/compact" {
				writeError(w, provider.Responses, 400, "/responses/compact is not supported for Magpie models; use a compaction_trigger on /responses")
				return
			}
			if hasSealedAgentMessage(body) {
				writeError(w, provider.Responses, 400, "An OpenAI lead sent a sealed subagent task that a Magpie-served model cannot read. Use a Magpie-served model for the lead, or choose an OpenAI subagent.")
				return
			}
			body, compact := codexInput(body, true)
			if compact {
				s.codexCompact(w, r, body)
				return
			}
			s.serve(w, r, provider.Responses, body)
			return
		}
		body = searchCallIDs(body)
		if rest == "/responses/compact" {
			break // preserve native compaction's existing passthrough
		}
		body, _ = codexInput(body, false)
		if id, ok := codexAccounts(r.Header, model); ok {
			s.serve(w, r, provider.Responses, withModel(body, id))
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

// codexBody reads a request's body as it was before Codex compressed it
// (zstd, for the ChatGPT backend), so it can be read and passed on plain.
func codexBody(r *http.Request) ([]byte, error) {
	var rd io.Reader = r.Body
	switch enc := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding"))); enc {
	case "", "identity":
	case "zstd":
		d, err := zstd.NewReader(r.Body)
		if err != nil {
			return nil, err
		}
		defer d.Close()
		rd = d
	case "gzip":
		g, err := gzip.NewReader(r.Body)
		if err != nil {
			return nil, err
		}
		rd = g
	default:
		return nil, fmt.Errorf("magpie can't read a %s body", enc)
	}
	r.Header.Del("Content-Encoding")
	return io.ReadAll(rd)
}

// codexAccounts is what a request for one of Codex's own models is served
// as when Codex is signed in to ChatGPT and more of its accounts are on in
// magpie: the codex subscription's model (codex/<model>), which goes to the
// account Codex is signed in to and, when that one is out of its allowance
// or rate limited, on to the next — as it can't when relayed as it came.
func codexAccounts(h http.Header, model string) (string, bool) {
	if model == "" || strings.Contains(model, "/") || apiKey(h) {
		return "", false
	}
	id := "codex/" + model
	p, _, ok := provider.Resolve(id)
	// one account named is found among them however many are on
	pinned := h.Get(AccountHeader) != ""
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
	end := func(status int, msg string, tokens, out int) {}
	if rest == "/responses" {
		who := "Codex's own sign-in"
		if base == codexAPIBase {
			who = "Codex's API key"
		}
		model := modelOf(body)
		seat := Weighed{ID: "codex", Provider: "openai", Name: "OpenAI", Icon: "openai", Who: who, Kind: "account", Agent: "codex", Model: model}
		tr = s.trace.begin(Route{Time: start, Agent: agentOf(r), Session: sessionOf(r.Header), ParentSession: titleParentSession(r.Header, metadata, kind), Kind: kind, Model: model, Provider: "openai",
			Order: []Weighed{seat}, Tries: []Try{{ID: seat.ID, Model: model, Start: start}}})
		end = func(status int, msg string, tokens, out int) {
			ms := time.Since(start).Milliseconds()
			ttft, text := first.ms()
			s.trace.update(tr, func(t *Route) {
				t.Tries[0].Done, t.Tries[0].Status, t.Tries[0].Millis, t.Tries[0].Error = true, status, ms, msg
				t.Tries[0].TTFT, t.Tries[0].FirstText = ttft, text
				t.Done, t.Status, t.Error, t.Millis, t.Tokens = true, status, msg, ms, tokens
				t.Output, t.TTFT, t.FirstText = out, ttft, text
				t.Usage = routeUsage("openai", model, uu)
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
		Reasoning: uu.Reasoning, Millis: call.Millis, TTFT: call.TTFT, FirstText: call.FirstText, Status: call.Status, Session: sessionOf(r.Header), NativeSession: nativeSessionOf(r.Header), Kind: call.Kind,
		RequestID: requestID(res.Header), Endpoint: r.URL.Path}
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

// codexModels is the ChatGPT backend's model list for this sign-in, with
// magpie's models after it. Should the backend not answer, Codex's last
// list of its own stands in.
func (s *Server) codexModels(w http.ResponseWriter, r *http.Request) {
	var own []any
	etag := ""
	u := provider.CodexBase + "/models"
	if r.URL.RawQuery != "" {
		u += "?" + r.URL.RawQuery
	}
	if req, err := http.NewRequestWithContext(provider.ViaSignedIn(r.Context(), "codex"), http.MethodGet, u, nil); err == nil {
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
			} else if res.StatusCode == 401 || res.StatusCode == 403 {
				// a sign-in to renew is Codex's to see
				w.Header().Set("Content-Type", res.Header.Get("Content-Type"))
				w.WriteHeader(res.StatusCode)
				w.Write(b)
				return
			}
		}
	}
	if own == nil {
		for _, e := range codexcat.CacheEntries() {
			own = append(own, e)
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
	ms := provider.CodexListed()
	// the list is the backend's and magpie's, and so is its ETag
	w.Header().Set("ETag", codexcat.WithTag(etag, provider.CodexListTag()))
	writeJSON(w, 200, map[string]any{"models": append(own, codexcat.Entries(ms, len(own)+100)...)})
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

// searchCallIDs is a Responses request whose tool_search_call items have
// ids OpenAI takes. A vendor's reply that magpie, or the vendor, gave the
// call as a function_call's (fc_…) came back to Codex as Codex's
// tool_search_call with that id, and Codex hands it back on every later
// turn: OpenAI's own models and the ChatGPT backend turn the whole request
// away ("Invalid 'input[98].id': 'fc_…'. Expected an ID that begins with
// 'tsc'"), Codex's compaction with it. Such an id goes as a tsc_ one; the
// rest of the request goes byte for byte.
func searchCallIDs(body []byte) []byte {
	if !bytes.Contains(body, []byte(`"tool_search_call"`)) {
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
		if json.Unmarshal(raw, &it) != nil || string(it["type"]) != `"tool_search_call"` {
			continue
		}
		var id string
		if json.Unmarshal(it["id"], &id) != nil || id == "" || strings.HasPrefix(id, "tsc_") {
			continue
		}
		if _, rest, ok := strings.Cut(id, "_"); ok && rest != "" {
			id = "tsc_" + rest
		} else {
			id = "tsc_" + id
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
func (s *Server) codexCompact(w http.ResponseWriter, r *http.Request, body []byte) {
	rec := &recorder{header: http.Header{}, status: 200}
	s.serve(rec, r, provider.Responses, body)
	if rec.status >= 400 {
		for k, vs := range rec.header {
			w.Header()[k] = vs
		}
		w.WriteHeader(rec.status)
		w.Write(rec.body.Bytes())
		return
	}
	res, err := compactReply(rec.body.Bytes())
	if err != nil {
		writeError(w, provider.Responses, 502, "compaction: "+err.Error())
		return
	}
	var sum strings.Builder
	for _, o := range res.Output {
		if o.Type != "message" {
			continue
		}
		for _, c := range o.Content {
			sum.WriteString(c.Text)
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
			failed = cmp.Or(ev.Message, "the model failed")
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
