package gateway

import (
	"net/http"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// isMemoryKind: a call Codex makes to write or consolidate its memories,
// however it names one (usage.PurposeOf's memory_consolidation).
func isMemoryKind(kind string) bool {
	return usage.PurposeOf(kind) == "kind:memory_consolidation"
}

// codexTurnKey is whose turns codexTurns holds: the agent recorded and the
// gateway key it came with, so a Codex on another computer (its own key,
// #742's Codex-BWG and Codex-R4S) is told apart from this one's.
func codexTurnKey(r *http.Request, who string) string {
	return who + "\x00" + access.Caller(r.Context()).KeyID
}

// rememberCodexTurn keeps the model a turn of Codex's own conversation
// was asked on, one magpie serves, for its memory calls.
func (s *Server) rememberCodexTurn(r *http.Request, who, agent, kind, asked string) {
	if agent != "codex" || kind != "" {
		return
	}
	s.codexTurns.Store(codexTurnKey(r, who), asked)
	// and by its conversation, for a memory call that raced it
	// (codexMemoryStandIn)
	s.codexLeads.saw(r, kind, asked)
}

// isReviewCall: Codex's auto-review of an approval (the guardian), which
// it asks for on codex-auto-review, the reviewer model of OpenAI's, when
// its list has that or names it as a model's auto_review_model_override.
func isReviewCall(kind, asked string) bool {
	return kind == "guardian" || asked == "codex-auto-review"
}

// freeOnChatGPT says whether a call on model by a ChatGPT account on plan
// is free (provider.ChatGPTReviewFree, which the Usage page's prices
// follow too).
func freeOnChatGPT(model, plan string) bool { return provider.ChatGPTReviewFree(model, plan) }

// codexMemoryStandIn is the model a Codex memory call goes to when magpie
// knows none by the name it came with (#742): Codex writes and
// consolidates its memories on models of its own (memories.extract_model
// and consolidation_model; by default gpt-6-luna and gpt-6.1-terra from
// Codex 0.167.0, gpt-5.6-luna and gpt-5.6-terra before) by
// their bare names, whatever it is set to, so with magpie as its provider
// every one was turned away as an unknown model before it was routed. It
// goes to the model that Codex's own turns go to — the last one it had
// answered here, else the last in the ledger (a week back) from the same
// agent with the same key — so it is routed, and in the views, as the
// memory call it is. Its auto-review (isReviewCall) goes there too when
// Codex's config names no model magpie serves (StandIn): Codex reviews on
// the conversation's model when it has no reviewer, and a Codex whose
// config magpie doesn't read as its own — on magpie through another
// provider table or a profile, under a CODEX_HOME of its own, or on
// another computer — was answered 404 "magpie knows no model
// \"codex-auto-review\"" (Adam on Discord). "" for any other call, a model
// magpie serves, or a Codex that has had no turn answered here.
//
// Codex starts its memories with each turn of a conversation, so a memory
// call can come before the turn it was started with, the first after the
// model was switched: it is read after that turn, as the app's titles are
// (codexLeads.sideOf, beside), not on the model switched away from
// (cybercaius on Discord).
//
// why is the move's reason for the trace: movedCodexFollow for a bare
// default magpie serves that went on the conversation's model instead,
// movedCodexTurn for a model magpie doesn't serve.
func (s *Server) codexMemoryStandIn(r *http.Request, who, agent, kind, asked string, body []byte) (to, why string) {
	if agent != "codex" || !isMemoryKind(kind) && !isReviewCall(kind, asked) {
		return "", ""
	}
	if _, _, ok := provider.Resolve(asked); ok {
		// A memory call on one of Codex's own bare defaults that a
		// provider of magpie's happens to serve under the same name:
		// the name is Codex's own, not a pick of that provider's model,
		// and the account it resolves to may refuse the model outright
		// (400, three tries, the memory lost, #4765). With Codex's
		// config naming no model for its memories, the call follows the
		// conversation's, as it does on the ChatGPT backend's path
		// (codexFollowTo). "" keeps it where the name resolves: any
		// other call, the config's own pick, a call whose config magpie
		// can't read, or no conversation of magpie's to follow.
		//
		// A "provider/model" id is never one of Codex's defaults, which
		// are bare: it is the user's pick in memories.extract_model or
		// consolidation_model, and goes as it came, whatever the config
		// here says — a Remote magpie can't read the Codex config on the
		// computer the call came from, and a Codex on a profile or a
		// CODEX_HOME of its own has its config elsewhere too (#5413).
		if !isMemoryKind(kind) || strings.Contains(asked, "/") || CodexSideSet == nil || CodexSideSet("memories") {
			return "", ""
		}
		to := s.codexMemorySession(r, body)
		if to == "" || to == asked {
			return "", ""
		}
		return to, movedCodexFollow
	}
	if isMemoryKind(kind) {
		if m := s.codexLeads.sideOf(r, body, true); m != "" && m != asked {
			if _, _, ok := provider.Resolve(m); ok {
				return m, movedCodexTurn
			}
		}
	}
	key := codexTurnKey(r, who)
	if m, ok := s.codexTurns.Load(key); ok {
		if m, _ := m.(string); m != "" && m != asked {
			if _, _, ok := provider.Resolve(m); ok {
				return m, movedCodexTurn
			}
		}
	}
	keyID := access.Caller(r.Context()).KeyID
	var last usage.Record
	usage.Visit(time.Now().Add(-7*24*time.Hour), func(rec usage.Record) {
		if rec.Agent != who || rec.CallerKeyID != keyID || rec.Kind != "" || rec.Rejected || rec.IsReported() || rec.Failed() || rec.Requested == "" {
			return
		}
		if rec.Time.After(last.Time) {
			last = rec
		}
	})
	m := last.Requested
	if id, ok := provider.GroupFor(m); ok {
		m = id
	}
	if m == "" || m == asked {
		return "", ""
	}
	if _, _, ok := provider.Resolve(m); !ok {
		return "", ""
	}
	return m, movedCodexTurn
}
