package gateway

import (
	"bytes"
	"context"
	"encoding/json"
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
		who := callerOf(r)
		call := Call{Time: start, From: provider.Chat, Agent: who.agent, Via: who.via, Model: asked}
		usage.Saw(agentOf(r))
		p, model, ok := resolveRetrievalModel(asked)
		if !ok {
			msg := fmt.Sprintf("magpie knows no model %q", asked)
			if off, isOff := provider.SwitchedOff(asked); isOff {
				msg = switchedOff(off, asked)
			}
			call.Status, call.Error = 404, msg
			writeError(w, provider.Chat, 404, msg)
			s.record(call)
			return
		}
		g, ms, isGroup := provider.FindGroup(asked)
		if keyWho, held := keyHolds(r); held && (isGroup && !groupAllowed(keyWho, g, ms) || !isGroup && !modelAllowed(keyWho, p, model)) {
			msg := keyModelError(keyWho, asked)
			call.Status, call.Error = 403, msg
			writeError(w, provider.Chat, 403, msg)
			s.record(call)
			return
		}
		call.To = provider.Chat
		// a routing group's members are tried as its routing orders them,
		// each account or key of theirs too (#773), the next asked when one
		// fails: a member that serves no such API (404), one out of quota
		// (429) or one whose vendor fails; a model is asked on its own
		tries := []candidate{{p: p, model: model}}
		if isGroup {
			if cs, _ := s.planGroup(g.Live(), ms, provider.Chat); len(cs) > 0 {
				tries = cs
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
		var skipped []string
		// masked once, as the settings say: a document a reranker gives
		// back has its secrets again
		all, _ := json.Marshal(req)
		// unless every try is on this machine or the local network, set
		// to go unmasked (provider.SkipsRedaction)
		if slices.ContainsFunc(tries, func(c candidate) bool { return !c.p.SkipsRedaction() }) {
			var done func()
			w, all, done = redacted(w, all)
			defer done()
			json.Unmarshal(all, &req)
		}
		for i, c := range tries {
			last := i == len(tries)-1
			call.Provider = c.p.ID
			base := strings.TrimRight(c.p.Base(provider.Chat), "/")
			if base == "" {
				msg := fmt.Sprintf("%s has no OpenAI-style API for %s", c.p.Name, path)
				if !last {
					skipped = append(skipped, c.label()+": "+msg)
					continue
				}
				call.Status, call.Error, call.Millis = 400, msg, time.Since(start).Milliseconds()
				call.Fallback = strings.Join(skipped, "; ")
				writeError(w, provider.Chat, 400, msg)
				s.record(call)
				return
			}
			req["model"], _ = json.Marshal(c.model)
			out, _ := json.Marshal(req)
			ctx, cancel := context.WithTimeout(r.Context(), retrieveTimeout)
			b, code, err := s.retrieveFrom(ctx, c.p, base+path, out)
			cancel()
			call.Millis = time.Since(start).Milliseconds()
			if err != nil && !last && r.Context().Err() == nil {
				skipped = append(skipped, c.label()+": "+err.Error())
				continue
			}
			call.Status = code
			in := retrievedTokens(b)
			call.Usage.Input = in
			call.Fallback = strings.Join(skipped, "; ")
			providerKeyID, providerKeyName := "", ""
			if c.p.Account == nil && c.p.Key != "" {
				providerKeyID, providerKeyName = provider.KeyID(c.p.Key), c.p.KeyName
			}
			appendUsage(r, usage.Record{Operation: operation, Time: start, Agent: call.Agent, Via: call.Via, Provider: c.p.ID, Host: c.p.Where(), Model: c.model, Requested: asked, ProviderKeyID: providerKeyID, ProviderKeyName: providerKeyName, ProviderAccount: accountOf(c.p),
				Input: in, Millis: call.Millis, Status: code, Session: sessionOf(r.Header)})
			if err != nil {
				call.Error = err.Error()
				s.record(call)
				msg := err.Error()
				if len(skipped) > 0 {
					msg += " (tried first: " + call.Fallback + ")"
				}
				writeError(w, provider.Chat, code, msg)
				return
			}
			s.record(call)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			w.Write(b)
			return
		}
	}
}

// resolveRetrievalModel also finds a preset plan's embedding model by its
// bare id. Such models stay out of agents' chat-model lists, so the general
// resolver does not otherwise see them there.
func resolveRetrievalModel(id string) (provider.Provider, string, bool) {
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
		for _, m := range p.PlanEmbeddings() {
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
	ctx = p.Via(ctx)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, 500, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if p.IsRemoteMagpie() {
		passOnCaller(ctx, req)
	}
	if err := p.Sign(ctx, req, provider.Chat, body); err != nil {
		return nil, 502, err
	}
	res, err := p.Do(s.client, req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, 504, fmt.Errorf("%s didn't answer in %s", p.Name, retrieveTimeout)
		}
		return nil, 502, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 256<<20))
	if err != nil {
		return nil, 502, err
	}
	if res.StatusCode >= 300 {
		hint := ""
		if res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusMethodNotAllowed {
			hint = " — " + p.Name + " may not serve this API"
		}
		return b, res.StatusCode, fmt.Errorf("%s: %d %s%s%s", provider.HostOf(url), res.StatusCode, http.StatusText(res.StatusCode), vendorSaid(vendorMessage(b)), hint)
	}
	return b, res.StatusCode, nil
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
