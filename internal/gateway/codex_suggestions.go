package gateway

import (
	"context"
	"log"
	"net/http"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// movedCodexSuggestions is the reason Codex's prompt suggestions went on
// the model set for them (provider.CodexSuggestionsModel) rather than the
// one they asked for.
const movedCodexSuggestions = "codex-suggestions"

// codexSuggestionsTo is the model one of the Codex app's prompt-suggestion
// calls (thread_source ambient_suggestions, and the safety check of what
// they found, ambient_suggestion_safety) goes on: the one set for them,
// "" to leave it on asked. The app asks for a model of OpenAI's that its
// server names (gpt-6-luna) and, on magpie as its provider, falls back to
// the provider's default model, so the suggestions, a long agentic turn
// that reads the project, ran on whatever "auto" picked (@0xcb198609 on
// X: 76.9k tokens on deepseek-v4-pro). Codex has no setting for their
// model; its own switch, Settings › Configuration › Homepage suggestions,
// turns them off. The model set must be enabled, served and allowed to
// the gateway key; when it isn't, the call goes on as asked and the log
// says why. The cheap checks come first, so an unset model costs a held
// settings read.
func codexSuggestionsTo(r *http.Request, agent, kind, asked string) string {
	if agent != "codex" && !fromCodex(r.Header) || usage.PurposeOf(kind) != "kind:ambient_suggestions" {
		return ""
	}
	to := provider.CodexSuggestionsModel()
	if to == "" || to == asked {
		return ""
	}
	if why := compactRefused(r, to); why != "" {
		log.Printf("gateway: Codex's prompt suggestions stay on %s, not %s: %s", asked, to, why)
		return ""
	}
	return to
}

type suggestionsAskedKey struct{}

// withSuggestionsAsked carries the model a prompt-suggestion call asked
// for, put on the model set for them on the ChatGPT backend's route, to
// serve, so the trace and the ledger keep what it asked.
func withSuggestionsAsked(r *http.Request, asked string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), suggestionsAskedKey{}, asked))
}

func suggestionsAskedOf(r *http.Request) string {
	s, _ := r.Context().Value(suggestionsAskedKey{}).(string)
	return s
}
