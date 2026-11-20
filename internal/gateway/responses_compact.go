package gateway

import (
	"context"
	"fmt"
	"net/http"

	"github.com/yetone/magpie/internal/provider"
)

// POST /v1/responses/compact (and /responses/compact) is OpenAI's unary
// compaction: the conversation in, one response.compaction object back,
// its compaction item sealed (encrypted_content) by the server that wrote
// it. Pi's Responses compaction extension asks it of a provider set to the
// Responses API (#7281); on the generic route it was the 404 of an unknown
// path, and never reached the upstream.
//
// It is relayed as it came, to /responses/compact under the provider's
// Responses base, only where that is the provider's own API: a key's
// provider whose model is asked on Responses (OpenAI, a relay or a local
// server speaking it, another magpie). Nothing else has the route, and a
// translation can't make the sealed item, so anywhere else — a model on
// Chat or Messages only, a subscription or plugin account — is a 400
// saying so, as CodexPath's is for magpie's models, for the client to fall
// back to compacting on /responses. Who sealed the item is remembered
// (wroteCheckpoint), so a later /responses carrying it goes back there
// (#1099). A ChatGPT account isn't asked it here: its backend's compaction
// is reached on CodexPath, as Codex sends it.

type unaryCompactKey struct{}

// responsesCompact serves POST /v1/responses/compact.
func (s *Server) responsesCompact(w http.ResponseWriter, r *http.Request) {
	body, ok := s.requestBody(w, r, provider.Responses)
	if !ok {
		return
	}
	body, _, err := requestModel(body)
	if err != nil {
		writeError(w, provider.Responses, 400, err.Error())
		return
	}
	r = r.WithContext(context.WithValue(r.Context(), unaryCompactKey{}, true))
	s.serveAgent(w, r, provider.Responses, body)
}

// unaryCompact is whether r is a /responses/compact request.
func unaryCompact(r *http.Request) bool {
	v, _ := r.Context().Value(unaryCompactKey{}).(bool)
	return v
}

// compactsUnary is whether model at p can be asked /responses/compact: a
// key's provider (no account) whose wire carries Responses and asks model
// on it.
func (s *Server) compactsUnary(p provider.Provider, model string) bool {
	return p.Account == nil && s.carriesCompaction(p, model)
}

// unaryCompactCarriers keeps of cands those compactsUnary.
func (s *Server) unaryCompactCarriers(cands []candidate, pl planned) ([]candidate, planned, bool) {
	return keepCandidates(cands, pl, func(c candidate) bool { return s.compactsUnary(c.p, c.model) })
}

// unaryCompactError is what a /responses/compact for model is told when
// nothing that serves it has the route.
func unaryCompactError(model string) string {
	return fmt.Sprintf("/responses/compact is not supported for %s: it is relayed only to a provider whose own API is Responses (an API key's provider asking the model on Responses), and none of those serves %s; compact on /responses instead", model, model)
}
