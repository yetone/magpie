package gateway

import (
	"context"
	"net/http"
	"strings"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// SubagentPick is a subagent's request put on the model the user set for
// its agent's subagents, in place of the one its lead asked for: Codex's
// (provider.CodexSubagentModel, willz on Discord) or Claude Desktop's
// (provider.DesktopSubagentModel). Kept says why it went on the model
// asked for instead, "" when it was put on To. Lead says To is the model
// the lead's own turns go on (provider.CodexSubagentLead).
type SubagentPick struct {
	Asked string `json:"asked"`
	To    string `json:"to"`
	Kept  string `json:"kept,omitempty"`
	Lead  bool   `json:"lead,omitempty"`
}

// Moved says the request went on To.
func (p *SubagentPick) Moved() bool { return p != nil && p.Kept == "" }

type subagentPickKey struct{}

func withSubagentPick(r *http.Request, p *SubagentPick) *http.Request {
	if p == nil {
		return r
	}
	return r.WithContext(context.WithValue(r.Context(), subagentPickKey{}, p))
}

func subagentPickOf(r *http.Request) *SubagentPick {
	p, _ := r.Context().Value(subagentPickKey{}).(*SubagentPick)
	return p
}

// codexSubagentPick is what is done with the model of a request: nil when
// it isn't a Codex subagent's or no model is set for them, so the request
// goes on as asked. The model set must still be served, be able to open
// the subagent's task, and be one the gateway key may use; when it isn't,
// the request goes on the model asked for and the pick says why. Any
// model opens a task handed as text (a lead on a chat provider, #1633, or
// Codex's multi-agent V1). One the ChatGPT backend sealed, its lead having
// run there, is put only on a ChatGPT account's model, as it was before
// any other could be set; on any other the subagent stays where its lead
// put it, which serve's sealed gate (sealedReader) handles as it did with
// no model set, so setting one is never worse than leaving it unset. A
// relay that sealed the lead's task (#1109) is reached that way too: the
// lead names it.
//
// Set to the lead's model (provider.CodexSubagentLead), the subagent goes
// on the model its lead's own turns last went on (codexLeads), even when
// the lead named another in spawn_agent (cleverpigo on X: a lead on
// WorkBuddy's DeepSeek named gpt-5.6-sol, and the subagent went to
// ChatGPT). native says the request can go to Codex's own ChatGPT sign-in
// (its backend's route), where a lead on one of Codex's own models is
// followed too; elsewhere such a lead leaves the request as asked.
func (s *Server) codexSubagentPick(r *http.Request, agent, kind, asked string, body []byte, native bool) *SubagentPick {
	// Codex by its token or by its User-Agent (fromCodex: its CLI, exec,
	// the IDE extension, the desktop app)
	if agent != "codex" && !fromCodex(r.Header) || usage.PurposeOf(kind) != "kind:collab_spawn" {
		return nil
	}
	to, lead := provider.CodexSubagentModel(), false
	if to == provider.CodexSubagentLead {
		to, _, _ = s.codexLeads.lookup(r, body)
		lead = true
	}
	if to == "" || to == asked {
		return nil
	}
	pick := &SubagentPick{Asked: asked, To: to, Lead: lead}
	if lead && !strings.Contains(to, "/") {
		// the lead on one of Codex's own models: the subagent goes to
		// the ChatGPT sign-in on it, where a model the user disabled is
		// refused (codexNativeOff)
		if !native {
			return nil
		}
		return pick
	}
	if _, off := idOff(to); off {
		// the user disabled it (#1867): the subagent stays on its lead's
		// model, itself refused when it is disabled too
		pick.Kept = "it is disabled in magpie"
		return pick
	}
	if why := provider.CodexSubagentUnserved(to); why != "" {
		pick.Kept = why
		return pick
	}
	if !provider.CodexAccountModel(to) && sealedTaskIn(body) {
		pick.Kept = sealedKept
		return pick
	}
	if who, held := keyHolds(r); held {
		if p, model, ok := provider.Resolve(to); !ok || !modelAllowed(who, p, model) {
			pick.Kept = "the gateway key may not use it"
		}
	} else if withGatewayKey(r) {
		// a routing group bound to other keys (groupBarred)
		if g, _, ok := provider.FindGroup(to); ok && groupBarred(who, g) {
			pick.Kept = "the gateway key may not use it"
		}
	}
	return pick
}

// sealedKept is why a Codex subagent stayed on the model its lead asked
// for when the one set for its subagents can't open its task.
const sealedKept = "its task was sealed by the ChatGPT backend that answered its lead, and only a ChatGPT account can open it"

// sealedTaskIn says a request carries a sealed agent task, not only sealed
// messages: serve leaves those out with a note (withoutSealedMessages).
func sealedTaskIn(body []byte) bool {
	if !hasSealedAgentMessage(body) {
		return false
	}
	b, _ := withoutSealedMessages(body)
	return hasSealedAgentMessage(b)
}
