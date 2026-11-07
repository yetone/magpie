package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// retrieveTimeout bounds an embeddings or rerank call: a vendor answers
// one in seconds, a long batch in a minute or two.
const retrieveTimeout = 3 * time.Minute

// retrieve serves the retrieval APIs (#765): POST /v1/embeddings in
// OpenAI's shape and POST /v1/rerank in the shape Cohere, Jina and Voyage
// share. Neither is a chat, so neither is translated: the body goes to the
// model's provider at its OpenAI-style base + path as the agent sent it,
// the model id the provider's own, and the vendor's answer comes back as
// it said it. The call is recorded and its tokens counted like a turn's.
func (s *Server) retrieve(path, operation string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, admitted := s.requestBody(w, r, provider.Chat)
		if !admitted {
			return
		}
		start := time.Now()
		var req map[string]json.RawMessage
		if json.Unmarshal(body, &req) != nil || req == nil {
			writeError(w, provider.Chat, 400, "the body isn't a JSON object")
			return
		}
		var asked string
		json.Unmarshal(req["model"], &asked)
		if asked == "" {
			writeError(w, provider.Chat, 400, "name the model: \"model\" is a magpie model id, as /v1/models lists them")
			return
		}
		s.serveRetrieval(w, r, path, operation, start, asked, &jsonRetrieval{req: req})
	}
}

// retrieval is a retrieval request as read: what goes to a vendor for each
// model it may be asked of.
type retrieval interface {
	// mask masks what goes to the vendors, and gives the writer that puts
	// the masked values back in their answer, what it swapped, and the
	// text it sent
	mask(ctx context.Context, w http.ResponseWriter) (http.ResponseWriter, func(), maskHits, string, error)
	// forModel is the body and its Content-Type for the provider's own id
	// of the model
	forModel(model string) ([]byte, string, error)
	// answerType is the Content-Type the vendor's answer goes back with,
	// given the one it came with
	answerType(got string) string
}

// refusingRetrieval is a retrieval request only some providers can be
// sent: refuses is why p can't, "" when it can (decisionRequest).
type refusingRetrieval interface {
	refuses(p provider.Provider) string
}

// countedRetrieval is a retrieval request whose answer's usage is read
// its own way, cached tokens apart (decisionRequest).
type countedRetrieval interface {
	counted(b []byte) Usage
}

// jsonRetrieval is an embeddings or rerank body: a JSON object.
type jsonRetrieval struct{ req map[string]json.RawMessage }

func (j *jsonRetrieval) mask(ctx context.Context, w http.ResponseWriter) (http.ResponseWriter, func(), maskHits, string, error) {
	// masked once, as the settings say: a document a reranker gives back
	// has its secrets again
	all, _ := json.Marshal(j.req)
	w, all, done, hits, err := redactedHits(ctx, w, all)
	json.Unmarshal(all, &j.req)
	return w, done, hits, string(all), err
}

// answerType is JSON, whatever a vendor labels it.
func (j *jsonRetrieval) answerType(string) string { return "application/json" }

func (j *jsonRetrieval) forModel(model string) ([]byte, string, error) {
	j.req["model"], _ = json.Marshal(model)
	out, err := json.Marshal(j.req)
	return out, "application/json", err
}

// serveRetrieval routes a retrieval request for the model asked to its
// provider, or over a group's members, and passes the answer back.
func (s *Server) serveRetrieval(w http.ResponseWriter, r *http.Request, path, operation string, start time.Time, asked string, req retrieval) {
	who := callerOf(r)
	call := Call{Time: start, From: provider.Chat, Agent: who.agent, Via: who.via, Model: asked, headers: headerLogOf(r.Context())}
	usage.Saw(agentOf(r))
	// a model the user disabled (#1867), or a group of them all
	if msg, off := idOff(asked); off {
		call.Status, call.Error = 403, msg
		writeError(w, provider.Chat, 403, msg)
		s.record(call)
		return
	}
	kind := retrievalKind(operation)
	p, model, ok := resolveRetrievalModel(asked, kind)
	if _, _, isGroup := mediaMembers(asked, kind); ok && !isGroup {
		if msg, off := modelOff(p, model, asked); off {
			call.Status, call.Error = 403, msg
			writeError(w, provider.Chat, 403, msg)
			s.record(call)
			return
		}
	}
	if !ok {
		msg := unknownModel(asked, "")
		if off, isOff := provider.SwitchedOff(asked); isOff {
			msg = switchedOff(off, asked)
		}
		call.Status, call.Error = 404, msg
		writeError(w, provider.Chat, 404, msg)
		s.record(call)
		return
	}
	g, ms, isGroup := mediaMembers(asked, kind)
	if isGroup {
		r = heldByGroup(r, g) // held to the group's accounts, if it is
	}
	keyWho, keyHeld := keyHolds(r)
	if keyHeld && (isGroup && !groupAllowed(keyWho, g, ms) || !isGroup && !modelAllowed(keyWho, p, model)) {
		st, msg := keyModelRefusal(w, keyWho, asked)
		call.Status, call.Error = st, msg
		writeError(w, provider.Chat, st, msg)
		s.record(call)
		return
	}
	if isGroup {
		// a model a pause rule holds for now is left out, as for a chat
		var paused []provider.Paused
		if ms, paused = provider.PausedOut(g.Live(), ms, agentOf(r), "", ruleClock()); len(ms) == 0 && len(paused) > 0 {
			msg := pausedError(asked, paused)
			call.Status, call.Error = 503, msg
			writeError(w, provider.Chat, 503, msg)
			s.record(call)
			return
		} else if len(paused) > 0 {
			p, model = ms[0].Provider, ms[0].Model // not the paused one, when planning finds none
		}
	}
	call.To = provider.Chat
	// a routing group's members are tried as its routing orders them,
	// each account or key of theirs too (#773), the next asked when one
	// fails: a member that serves no such API (404), one out of quota
	// (429) or one whose vendor fails; a model is asked on its own
	tries := []candidate{{p: p, model: model}}
	if isGroup {
		cs, pl := s.planGroup(g.Live(), ms, provider.Chat)
		if len(cs) > 0 {
			tries = cs
		} else if slices.ContainsFunc(pl.left, func(w Weighed) bool { return w.HeldBy != "" }) {
			// every account a group in it may use is none it has
			msg := groupsHeldError(asked, pl.left)
			call.Status, call.Error = 403, msg
			writeError(w, provider.Chat, 403, msg)
			s.record(call)
			return
		}
	} else {
		// a model of one provider goes over its keys or accounts as a
		// chat does, never on one whose own list of models leaves it
		// out (#1657)
		cs, code, msg := s.onKeys(r, p, model, asked)
		if code != 0 {
			call.Status, call.Error = code, msg
			writeError(w, provider.Chat, code, msg)
			s.record(call)
			return
		}
		tries = cs
	}
	// a group's members the key may not go to through it are left out
	// (groupKeeps): those a spent limit of the key's counts
	if keyHeld && isGroup {
		keeps := groupKeeps(keyWho, g, ms)
		tries = slices.DeleteFunc(tries, func(c candidate) bool { return !keeps[c.p.ID+"/"+c.model] })
		if len(tries) == 0 {
			st, msg := keyGroupRefusal(w, keyWho, asked, ms)
			call.Status, call.Error = st, msg
			writeError(w, provider.Chat, st, msg)
			s.record(call)
			return
		}
	}
	// the accounts or keys the calling key may not use are left out of
	// the tries too (#905): a group's members through it no less
	if keyWho, held := accountHolds(r); held {
		tries = slices.DeleteFunc(tries, func(c candidate) bool { return !accountAllowed(keyWho, c) })
		if len(tries) == 0 {
			msg := keyAccountsError(keyWho, asked)
			call.Status, call.Error = 403, msg
			writeError(w, provider.Chat, 403, msg)
			s.record(call)
			return
		}
	}
	tries = probeOne(tries) // of those resting, only the one back soonest (#1799)
	var skipped []string
	went := asked // the model the last try asked for
	// the Routing page lists it as it does a chat, each account, key or
	// member asked a try of its one route (sideRoute)
	var grp *GroupRef
	if isGroup {
		grp = groupRef(g, ms)
	}
	side := s.beginSide(r, start, asked, tries[0].p.ID, grp)
	// unless every try is on this machine or the local network, set
	// to go unmasked (provider.SkipsRedaction)
	if slices.ContainsFunc(tries, func(c candidate) bool { return !c.p.SkipsRedaction() }) {
		var done func()
		var err error
		var hits maskHits
		var sent string
		w, done, hits, sent, err = req.mask(r.Context(), w)
		defer done()
		if err != nil { // fails closed
			call.Status, call.Error = maskStatus(err), err.Error()
			side.end(tries[0].p, tries[0].model, call.Status, err, Usage{})
			writeError(w, provider.Chat, call.Status, err.Error())
			s.record(call)
			return
		}
		what := "embeddings"
		switch req.(type) {
		case *audioForm, *speechRequest:
			what = "audio"
		case *decisionRequest:
			what = "decisions"
		}
		// the record names the try that had it last, not the first
		// planned (noteMaskedUntil)
		masked, noted := noteMaskedUntil(r, what, hits, sent, side.id(), tries[0].p.ID, asked)
		side.masked(masked)
		defer func() { noted(call.Provider, went) }()
	}
	for i, c := range tries {
		last := i == len(tries)-1
		call.Provider, went = c.p.ID, c.model
		base := strings.TrimRight(c.p.OpenAIBase(), "/")
		tried := side.try(c)
		msg := ""
		if base == "" {
			msg = fmt.Sprintf("%s has no OpenAI-style API for %s", c.p.Name, path)
		} else if rr, ok := req.(refusingRetrieval); ok {
			msg = rr.refuses(c.p) // never sent, so never a 404 of the vendor's
		}
		if msg != "" {
			tried(400, errors.New(msg))
			if !last {
				skipped = append(skipped, c.label()+": "+msg)
				continue
			}
			call.Status, call.Error, call.Millis = 400, msg, time.Since(start).Milliseconds()
			call.Fallback = strings.Join(skipped, "; ")
			side.end(c.p, c.model, 400, errors.New(msg), Usage{})
			writeError(w, provider.Chat, 400, msg)
			s.record(call)
			return
		}
		out, ctype, err := req.forModel(c.model)
		if err != nil {
			tried(400, err)
			side.end(c.p, c.model, 400, err, Usage{})
			call.Status, call.Error, call.Millis = 400, err.Error(), time.Since(start).Milliseconds()
			writeError(w, provider.Chat, 400, err.Error())
			s.record(call)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), retrieveTimeout)
		// the headers of the vendor's reply passed on (#4145)
		var heard heardHeaders
		ctx = withReplySink(ctx, c.p.ID, heard.put)
		var b []byte
		var got string
		var code int
		var res *http.Response // a streamed answer, not read yet
		if st, ok := req.(streamedRetrieval); ok && st.streams() {
			res, b, got, code, err = s.retrieveOpen(ctx, c.p, base+path, ctype, "", out)
		} else {
			b, got, code, err = s.retrieveAs(ctx, c.p, base+path, ctype, out)
		}
		if res == nil {
			cancel()
		} else {
			defer cancel() // once the answer is copied
		}
		tried(code, err)
		call.Millis = time.Since(start).Milliseconds()
		if err != nil && !last && r.Context().Err() == nil {
			skipped = append(skipped, c.label()+": "+err.Error())
			continue
		}
		call.Status = code
		in, outTokens := retrievedTokens(b), answeredTokens(b)
		var cached Usage // the cache's share, when the answer tells it
		if cr, ok := req.(countedRetrieval); ok && res == nil && err == nil {
			cached = cr.counted(b)
			in, outTokens = cached.Input, cached.Output
		}
		if res != nil {
			// passed on as it comes: audio, or SSE, is never held whole
			heard.into(w.Header())
			w.Header().Set("Content-Type", req.answerType(got))
			w.WriteHeader(code)
			var cut error
			in, outTokens, cut = copyAnswer(w, res.Body)
			res.Body.Close()
			call.Millis = time.Since(start).Milliseconds()
			if cut != nil {
				call.Error = "the answer was cut off: " + cut.Error()
			}
		}
		call.Usage.Input, call.Usage.Output = in, outTokens
		call.Usage.CacheRead, call.Usage.CacheWrite = cached.CacheRead, cached.CacheWrite
		call.Fallback = strings.Join(skipped, "; ")
		providerKeyID, providerKeyName := "", ""
		if c.p.Account == nil && c.p.Key != "" {
			providerKeyID, providerKeyName = provider.KeyID(c.p.Key), c.p.KeyName
		}
		side.end(c.p, c.model, code, err, Usage{Input: in, Output: outTokens, CacheRead: cached.CacheRead, CacheWrite: cached.CacheWrite})
		appendUsage(r, usage.Record{RouteID: side.id(), Operation: operation, Time: start, Agent: call.Agent, Via: call.Via, Provider: c.p.ID, Host: c.p.Where(), Model: c.model, Requested: asked, ProviderKeyID: providerKeyID, ProviderKeyName: providerKeyName, ProviderAccount: accountOf(c.p),
			Input: in, Output: outTokens, CacheRead: cached.CacheRead, CacheWrite: cached.CacheWrite, Millis: call.Millis, Status: code, Session: sessionOf(r.Header)})
		if err != nil {
			call.Error = err.Error()
			s.record(call)
			msg := err.Error()
			if len(skipped) > 0 {
				msg += " (tried first: " + call.Fallback + ")"
			}
			heard.into(w.Header())
			writeError(w, provider.Chat, code, msg)
			return
		}
		s.record(call)
		if res != nil {
			return
		}
		heard.into(w.Header())
		w.Header().Set("Content-Type", req.answerType(got))
		w.WriteHeader(code)
		w.Write(b)
		return
	}
}

// resolveRetrievalModel also finds a preset plan's embedding or rerank
// model, of kind's, by its bare id. Such models stay out of agents'
// chat-model lists, so the general resolver does not otherwise see them
// there.
func resolveRetrievalModel(id, kind string) (provider.Provider, string, bool) {
	if p, model, ok := mediaGroupFirst(id, kind); ok {
		return p, model, true
	}
	if p, model, ok := provider.Resolve(id); ok {
		return p, model, true
	}
	if strings.Contains(id, "/") {
		return provider.Provider{}, "", false
	}
	for _, p := range provider.All() {
		if !p.On() || p.DecideOnly() {
			continue
		}
		for _, m := range p.PlanRetrieval(kind) {
			if m.ID == id {
				return p, id, true
			}
		}
	}
	return provider.Provider{}, "", false
}

// retrieveFrom posts body to url as the provider signs its requests, and
// reads the answer; a failure's code is the vendor's, with its message.
func (s *Server) retrieveFrom(ctx context.Context, p provider.Provider, url string, body []byte) ([]byte, int, error) {
	b, _, code, err := s.retrieveAs(ctx, p, url, "application/json", body)
	return b, code, err
}

// retrieveAs is retrieveFrom with a body of its own Content-Type, a
// multipart form's with its boundary; it also gives the answer's.
func (s *Server) retrieveAs(ctx context.Context, p provider.Provider, url, ctype string, body []byte) ([]byte, string, int, error) {
	accept := ""
	if ctype == "application/json" {
		accept = "application/json"
	}
	res, b, got, code, err := s.retrieveOpen(ctx, p, url, ctype, accept, body)
	if res == nil {
		return b, got, code, err
	}
	defer res.Body.Close()
	b, err = io.ReadAll(io.LimitReader(res.Body, 256<<20))
	if err != nil {
		return nil, "", 502, err
	}
	return b, got, code, nil
}

// retrieveOpen posts body as retrieveAs does, with an Accept of its own
// (none when empty). A success's answer is left unread in res, for the
// caller to read and close; a failure's is read into b, res nil.
func (s *Server) retrieveOpen(ctx context.Context, p provider.Provider, url, ctype, accept string, body []byte) (*http.Response, []byte, string, int, error) {
	ctx = s.metered(p.Via(ctx), p, "") // counted against its MaxRPM (rpm.go)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, nil, "", 500, err
	}
	req.Header.Set("Content-Type", ctype)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	// magpie's, or the agent's own where the provider passes it on (#1851)
	stampUA(ctx, p, req.Header)
	if p.IsRemoteMagpie() {
		req.Header.Set("User-Agent", "magpie/"+Version)
		passOnCaller(ctx, req)
		passOnUA(req, callerUA(ctx))
	}
	if err := p.Sign(ctx, req, provider.Chat, body); err != nil {
		return nil, nil, "", 502, err
	}
	res, err := p.Do(s.client, req)
	heardReply(ctx, p, res)
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, "", 504, fmt.Errorf("%s didn't answer in %s", p.Name, retrieveTimeout)
		}
		return nil, nil, "", 502, err
	}
	got := res.Header.Get("Content-Type")
	if res.StatusCode < 300 {
		return res, nil, got, res.StatusCode, nil
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 256<<20))
	if err != nil {
		return nil, nil, "", 502, err
	}
	hint := ""
	if res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusMethodNotAllowed {
		hint = " — " + p.Name + " may not serve this API"
	}
	return nil, b, got, res.StatusCode, fmt.Errorf("%s: %d %s%s%s", provider.HostOf(url), res.StatusCode, http.StatusText(res.StatusCode), errorSaid(b), hint)
}

// retrievedTokens is what an answer says it counted: OpenAI's
// prompt_tokens, Jina's and Voyage's total_tokens, or Cohere's billed
// input tokens.
func retrievedTokens(b []byte) int {
	var a struct {
		Usage struct {
			Prompt int `json:"prompt_tokens"`
			Input  int `json:"input_tokens"`
			Total  int `json:"total_tokens"`
		} `json:"usage"`
		Meta struct {
			Billed struct {
				Input int `json:"input_tokens"`
			} `json:"billed_units"`
		} `json:"meta"`
	}
	if json.Unmarshal(b, &a) != nil {
		return 0
	}
	for _, n := range []int{a.Usage.Prompt, a.Usage.Input, a.Usage.Total, a.Meta.Billed.Input} {
		if n > 0 {
			return n
		}
	}
	return 0
}
