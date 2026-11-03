package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// AlphaSearchPath is where Codex's own web search, its web.run tool, goes:
// "alpha/search" under the model provider's base URL, so /v1/alpha/search
// on magpie's provider and CodexPath+"/alpha/search" when Codex is signed in
// to ChatGPT. Codex offers web.run since 0.143 for a provider that sends
// x-openai-actor-authorization, as magpie's does, on a model whose catalog
// entry says use_responses_lite, as a ChatGPT account's do in magpie
// (codexcat copies Codex's own entry). Unanswered, the 404 ended the turn:
// Codex takes any failure of web.run as fatal (anton on Discord).
const AlphaSearchPath = "/alpha/search"

// alphaSearchRequest is the part of Codex's SearchRequest magpie reads; the
// rest goes on to the ChatGPT backend as it came.
type alphaSearchRequest struct {
	Model    string `json:"model"`
	Commands struct {
		SearchQuery []alphaQuery `json:"search_query"`
		ImageQuery  []alphaQuery `json:"image_query"`
	} `json:"commands"`
	Settings struct {
		// Codex's web_search mode: false "cached", true "live"
		ExternalWebAccess json.RawMessage `json:"external_web_access"`
	} `json:"settings"`
}

type alphaQuery struct {
	Q       string   `json:"q"`
	Domains []string `json:"domains"`
}

// alphaSearchReply is Codex's SearchResponse: output is what the model is
// shown as the tool's result.
type alphaSearchReply struct {
	EncryptedOutput *string `json:"encrypted_output"`
	Output          string  `json:"output"`
}

// alphaSearch answers POST /v1/alpha/search. A ChatGPT account's model
// goes to the ChatGPT backend's search, as Codex sends it there itself,
// on the account's sign-in. Any other model's search_query is searched as
// magpie searches for a model that can't (webSearch). What magpie can't
// do is said in the tool's output with a 200, which the model reads; a
// failure status would end the turn.
func (s *Server) alphaSearch(w http.ResponseWriter, r *http.Request) {
	body, ok := s.readRequestBody(w, r, provider.Responses, 0)
	if !ok {
		return
	}
	s.alphaSearchBody(w, r, body)
}

func (s *Server) alphaSearchBody(w http.ResponseWriter, r *http.Request, body []byte) {
	var q alphaSearchRequest
	if err := json.Unmarshal(body, &q); err != nil {
		writeError(w, provider.Responses, http.StatusBadRequest, "magpie couldn't read the web search request: "+err.Error())
		return
	}
	if q.Model == "" {
		writeError(w, provider.Responses, http.StatusBadRequest, "the web search request names no model")
		return
	}
	note := ""
	if ons, model := chatGPTSearchers(r, q.Model); len(ons) > 0 {
		answered, why := s.alphaSearchOn(w, r, withModel(body, model), model, ons)
		if answered {
			return
		}
		// a failure would end the turn: magpie searches in its place
		log.Printf("web search on ChatGPT for %s: %s", q.Model, why)
		note = "ChatGPT's web search failed (" + why + "); magpie searched in its place.\n\n"
	}
	s.alphaSearchHere(w, r, q, note)
}

// chatGPTSearchers is the ChatGPT accounts a web search for model may go
// to, the one it is served on first and those resting or with a revoked
// sign-in last, with the model as the backend knows it: none when model
// isn't a ChatGPT account's, or the calling key may use none of them.
func chatGPTSearchers(r *http.Request, model string) ([]provider.Provider, string) {
	p, bare, ok := provider.Resolve(model)
	if !ok || p.Account == nil || p.Account.Agent != "codex" {
		return nil, ""
	}
	who, keyHeld := keyHolds(r)
	if keyHeld && !modelAllowed(who, p, bare) {
		return nil, ""
	}
	var all []provider.Provider
	if !p.OwnPaused() {
		all = append(all, p)
	}
	all = append(all, p.AlsoOn()...)
	var ready, later []provider.Provider
	for _, o := range all {
		c := candidate{p: o, model: bare, rest: o.ID}
		if !accountAllowed(who, c) {
			continue
		}
		if resting(c) || provider.SignInMarked(o.Account) {
			later = append(later, o)
			continue
		}
		ready = append(ready, o)
	}
	return append(ready, later...), bare
}

// alphaSearchOn sends the search to the ChatGPT backend on each account in
// turn, the next asked when one is refused, limited or fails, and relays
// the backend's answer as it is. It says whether one answered: when none
// did, nothing is written, and the search is magpie's (alphaSearchHere),
// with why returned.
func (s *Server) alphaSearchOn(w http.ResponseWriter, r *http.Request, body []byte, model string, ons []provider.Provider) (bool, string) {
	start := time.Now()
	// as written only when every account it may be asked on is set to go
	// unmasked (provider.SkipsRedaction, isee1_1 on Discord); magpie's own
	// search in its place masks its query itself (maskedQuery)
	if len(ons) > 0 && !slices.ContainsFunc(ons, func(p provider.Provider) bool { return !p.SkipsRedaction() }) {
		return s.alphaSearchAsked(w, r, body, model, ons, start)
	}
	w, body, unmask, hits, err := redactedHits(r.Context(), w, body)
	defer unmask()
	if err != nil {
		return false, err.Error()
	}
	noteMasked(r, "search", hits, sentOf(hits, body), 0, "openai", model)
	return s.alphaSearchAsked(w, r, body, model, ons, start)
}

// alphaSearchAsked is alphaSearchOn once body is as it goes.
func (s *Server) alphaSearchAsked(w http.ResponseWriter, r *http.Request, body []byte, model string, ons []provider.Provider, start time.Time) (bool, string) {
	var errs []string
	for _, on := range ons {
		who := "OpenAI (" + on.Account.User + "): "
		ctx := on.Via(r.Context())
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.CodexBase+AlphaSearchPath, bytes.NewReader(body))
		if err != nil {
			errs = append(errs, who+err.Error())
			continue
		}
		for k, vs := range r.Header {
			if codexHeader(k) {
				req.Header[k] = vs
			}
		}
		req.Header.Set("Content-Type", "application/json")
		// a nil body: the search is answered in JSON, not streamed
		if err := on.Sign(ctx, req, provider.Responses, nil); err != nil {
			errs = append(errs, who+err.Error())
			continue
		}
		// a sign-in its vendor revoked isn't sent again (#1793)
		if why, revoked := provider.RevokedSignIn(on.Account, req); revoked {
			errs = append(errs, who+why)
			continue
		}
		res, err := s.client.Do(req)
		if err != nil {
			if r.Context().Err() != nil {
				return true, ""
			}
			errs = append(errs, who+err.Error())
			continue
		}
		noteSignIn(ctx, on.Account, req, res)
		c := Call{Time: start, From: provider.Responses, To: provider.Responses, Model: model, Provider: on.ID,
			Agent: callerOf(r).agent, Via: callerOf(r).via, Kind: "web_search", Status: res.StatusCode}
		if res.StatusCode < 200 || res.StatusCode > 299 {
			msg, _ := io.ReadAll(io.LimitReader(res.Body, 4<<10))
			res.Body.Close()
			errs = append(errs, who+res.Status+": "+strings.TrimSpace(string(msg)))
			c.Millis, c.Error = time.Since(start).Milliseconds(), res.Status
			s.record(c)
			continue
		}
		for k, vs := range res.Header {
			if !hopHeader(k) {
				w.Header()[k] = vs
			}
		}
		w.WriteHeader(res.StatusCode)
		io.Copy(w, res.Body)
		res.Body.Close()
		c.Millis = time.Since(start).Milliseconds()
		s.record(c)
		return true, ""
	}
	return false, strings.Join(errs, "; ")
}

// alphaSearchHere answers a search for a model no ChatGPT account serves:
// its search_query and image_query searched as magpie searches for a
// model that can't, the findings the tool's output. Opening, clicking or
// finding in a page is the ChatGPT backend's alone, and said so.
func (s *Server) alphaSearchHere(w http.ResponseWriter, r *http.Request, q alphaSearchRequest, note string) {
	var queries []string
	for _, x := range append(q.Commands.SearchQuery, q.Commands.ImageQuery...) {
		text := strings.TrimSpace(x.Q)
		if text == "" {
			continue
		}
		for _, d := range x.Domains {
			if d = strings.TrimSpace(d); d != "" {
				text += " site:" + d
			}
		}
		queries = append(queries, text)
	}
	if len(queries) == 0 {
		writeJSON(w, http.StatusOK, alphaSearchReply{Output: note + "magpie can only search the web for " + q.Model + ": give web.run a search_query. Opening, clicking or finding in a page goes to the ChatGPT backend, for a ChatGPT account's model in magpie."})
		return
	}
	ctx := withWebAccess(context.WithValue(r.Context(), searchForKey{}, &CallFor{Agent: callerOf(r).agent, Model: q.Model}), q.Settings.ExternalWebAccess)
	var said []string
	for _, query := range queries {
		found, _, err := s.webSearch(ctx, query)
		if err != nil {
			if r.Context().Err() != nil {
				return
			}
			log.Printf("web search for %s: %v", q.Model, err)
			msg := "magpie couldn't search the web for " + query + ": " + err.Error()
			if errors.Is(err, errNoSearcher) {
				msg += ": add a search API in magpie's Settings → Models → Web search (or magpie search add <api> <key>), or a provider that can search"
			}
			said = append(said, msg)
			continue
		}
		if len(queries) > 1 {
			found = "Search: " + query + "\n" + found
		}
		said = append(said, found)
	}
	writeJSON(w, http.StatusOK, alphaSearchReply{Output: note + strings.Join(said, "\n\n")})
}
