package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// kindDecision is the kind of model OpenAI's Decisions API is asked of
// (serveRetrieval's "decisions"): a group of it is the providers that
// can serve the API and list the model (servesKind).
const kindDecision = "decision"

// decisionModels are the models OpenAI's Decisions API takes
// (openai-python's DecisionCreateParams.model: gpt-6-luna alone), served
// by a provider that can serve the API even when its list of models to
// chat with leaves them out.
var decisionModels = []string{"gpt-6-luna"}

// decide serves POST /v1/decisions, OpenAI's Decisions API: a model, an
// input shared by every question (text, images, a conversation's items)
// and up to 400 questions, each answered with a probability, a choice or
// a score. Like embeddings it is no chat, so it is not translated: the
// body goes as the agent sent it to a provider that can serve the API
// (canDecide), at its OpenAI-style base + /decisions (Azure's
// /openai/v1/decisions), and the answer comes back as it said it. The
// API isn't streamed, and bills input tokens only.
func (s *Server) decide(w http.ResponseWriter, r *http.Request) {
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
		writeError(w, provider.Chat, 400, "name the model: \"model\" is a magpie model id, as /v1/models lists them (OpenAI's Decisions API takes gpt-6-luna)")
		return
	}
	s.serveRetrieval(w, r, "/decisions", "decisions", start, asked, &decisionRequest{jsonRetrieval{req: req}})
}

// decisionRequest is a Decisions API body: a JSON object, sent only to
// a provider that can serve the API, and counted by its own usage.
type decisionRequest struct{ jsonRetrieval }

// refuses is why p can't be sent the request, "" when it can.
func (d *decisionRequest) refuses(p provider.Provider) string {
	if canDecide(p) {
		return ""
	}
	return fmt.Sprintf("%s can't serve OpenAI's Decisions API (/v1/decisions): only an OpenAI API key, Azure OpenAI and another magpie serve it", p.Name)
}

// counted is what the answer's usage says: input_tokens has the cached
// and cache-written tokens in it (input_tokens_details), told apart so
// each is priced at its rate; no output is billed.
func (d *decisionRequest) counted(b []byte) Usage {
	var a struct {
		Usage struct {
			Input   int `json:"input_tokens"`
			Details struct {
				Cached int `json:"cached_tokens"`
				Write  int `json:"cache_write_tokens"`
			} `json:"input_tokens_details"`
		} `json:"usage"`
	}
	if json.Unmarshal(b, &a) != nil {
		return Usage{}
	}
	u := a.Usage
	read, write := max(u.Details.Cached, 0), max(u.Details.Write, 0)
	return Usage{Input: max(u.Input-read-write, 0), CacheRead: read, CacheWrite: write}
}

// canDecide is whether p can serve OpenAI's Decisions API: OpenAI's own
// API with a key (a ChatGPT sign-in's backend has no such route), Azure
// OpenAI, which serves it on its v1 API, or another magpie, which routes
// it on. A relay or a plugin, whatever models it lists, is none.
func canDecide(p provider.Provider) bool {
	if p.IsPlugin() {
		return false
	}
	if p.IsRemoteMagpie() || p.IsAzure() {
		return true
	}
	return p.Account == nil && p.Key != "" && provider.HostOf(p.Base(provider.Chat)) == "api.openai.com"
}

// servesDecision is servesKind's for the Decisions API: a provider that
// can serve it with the model on its list, or an OpenAI key asked for a
// model the API takes. Azure's ids are its deployments' and another
// magpie's are its own, so each has the model when its list says so.
func servesDecision(p provider.Provider, model string) bool {
	if !canDecide(p) {
		return false
	}
	if !offChatList(p, model) {
		return true
	}
	return slices.Contains(decisionModels, model) && !p.IsAzure() && !p.IsRemoteMagpie()
}
