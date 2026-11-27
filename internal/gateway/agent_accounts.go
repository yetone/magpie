package gateway

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/memdistill"
	"github.com/yetone/magpie/internal/provider"
)

// agentHeld holds each request of an agent set to use one account or key
// of some providers (provider.AgentAccounts, nb5p on Discord: Claude Code
// on one key of a relay, Codex on another, for the relay's dashboard to
// show each one's use) to those, by the rule a routing group's accounts
// hold it (access.Identity.HeldToAgent). Every route that picks an
// account asks the one identity — chat, responses, messages,
// count_tokens, the fallbacks — so each holds it alike: a provider set
// for the agent is served by that account or key alone, and while it
// rests or is off the request goes on to the model's fallbacks, never to
// the provider's other keys; every other provider serves as before. The
// models the agent sees are its usual ones. The native route takes the
// account itself (nativeAgentAccount).
func agentHeld(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, NativePath) {
			next.ServeHTTP(w, r)
			return
		}
		// who the request is before its agent's hold or a group's, for
		// magpie's own asks made while serving it (ownHeld)
		ctx := context.WithValue(r.Context(), beforeAgentKey{}, access.Caller(r.Context()))
		agent := holdAgent(callerOf(r).agent)
		set := provider.AgentAccounts(agent)
		if len(set) == 0 {
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		who := access.Caller(ctx).HeldToAgent(agent, set)
		next.ServeHTTP(w, r.WithContext(access.WithIdentity(ctx, who)))
	})
}

// OwnAgent is the agent magpie's own requests are held as (nb5p on
// Discord: Claude Code and Codex each on a relay key of their own, and
// magpie's own requests, tidying memory among them, on a third): the
// editor's Accounts by agent sets it as "Magpie's own requests", and
// every one of them is held to its accounts by the one rule an agent's
// are (HeldToAgent). They are
//   - Memory reading and tidying conversations (memdistill.Agent's
//     token, through the Handler: holdAgent),
//   - the classifier and decision models a routing group asks (askWaited
//     for those asked at their own API, askChat through serve),
//   - web search, by a model (searchWith) and by a plan's own search
//     service (ownAccountOn: Kimi Code's, ChatGPT's, WorkBuddy's; GLM's
//     keys, glmSearch), the conversation's own provider's too,
//   - image descriptions for a model that can't see (vision),
//   - a request a magpie makes of its own, as "magpie" (usage.AgentOf).
//
// Not theirs: the advisor (the agent named its model, and it is asked as
// the agent), Codex's own titles, summaries and compaction, an image an
// agent's tool draws, a Settings check (it checks the route an agent's call
// takes), and a page's or a tool's own agent (artifacts, billion-context).
const OwnAgent = "magpie"

// holdAgent is the agent whose accounts hold a request of agent: magpie's
// own requests are held as OwnAgent, whichever name they are counted by.
func holdAgent(agent string) string {
	if agent == memdistill.Agent {
		return OwnAgent
	}
	return agent
}

// beforeAgentKey keeps who a request is before agentHeld held it to its
// agent's accounts.
type beforeAgentKey struct{}

// ownAsked says r is one of magpie's own asks made in process while it
// serves an agent's request, which reach serve past the Handler's chain.
func ownAsked(r *http.Request) bool {
	switch r.UserAgent() {
	case RouterAgent, SearchAgent, VisionAgent:
		return true
	}
	return false
}

// ownIdentity is who magpie's own ask made for ctx's request is, and
// whether OwnAgent is set to some accounts: the request's identity
// before its agent's hold (a gateway key's accounts and limits still
// hold it), held to OwnAgent's accounts instead. With none set it is the
// request's identity as it is.
func ownIdentity(ctx context.Context) (access.Identity, bool) {
	set := provider.AgentAccounts(OwnAgent)
	if len(set) == 0 {
		return access.Caller(ctx), false
	}
	return beforeAgent(ctx).HeldToAgent(OwnAgent, set), true
}

// beforeAgent is ctx's identity before agentHeld held it to its agent's
// accounts, its identity when it went through no agentHeld.
func beforeAgent(ctx context.Context) access.Identity {
	if who, ok := ctx.Value(beforeAgentKey{}).(access.Identity); ok {
		return who
	}
	return access.Caller(ctx)
}

// ownHeld is r held as OwnAgent when it is one of magpie's own asks
// (ownAsked) and OwnAgent is set to some accounts; r otherwise.
func ownHeld(r *http.Request) *http.Request {
	if !ownAsked(r) {
		return r
	}
	who, ok := ownIdentity(r.Context())
	if !ok {
		return r
	}
	return r.WithContext(access.WithIdentity(r.Context(), who))
}

// ownAccountOn is p on the account or key OwnAgent is set to use on it,
// for magpie's own ask a plan's own service answers with p's key itself
// (a Kimi Code plan's or a ChatGPT account's search): p itself when none
// is set; false when the one set is off, so the ask goes to no other.
func ownAccountOn(p provider.Provider) (provider.Provider, bool) {
	return agentAccountOn(OwnAgent, p)
}

// nativeAgentAccount is p on the account or key r's agent is set to use
// on it (agentHeld), p itself when none is set or p no longer has it.
func nativeAgentAccount(r *http.Request, p provider.Provider) provider.Provider {
	q, _ := agentAccountOn(holdAgent(callerOf(r).agent), p)
	return q
}

// agentAccountOn is p on the account or key agent is set to use on it,
// and true; p itself and true when none is set or p no longer has it;
// p and false when the one set is off.
func agentAccountOn(agent string, p provider.Provider) (provider.Provider, bool) {
	ref := agentRef(agent, p.ID)
	if ref == "" || strings.EqualFold(p.AccountID(), ref) {
		return p, true
	}
	for _, k := range p.KeysOn() {
		if strings.EqualFold(provider.KeyID(k.Key), ref) {
			return p.WithKey(k), true
		}
	}
	for _, a := range p.AccountsOn() {
		if strings.EqualFold(a.Provider.AccountID(), ref) {
			return a.Provider, true
		}
	}
	return p, false
}

// agentRef is the account's or key's id agent is set to use on provider
// pid, "" for none.
func agentRef(agent, pid string) string {
	if agent == "" {
		return ""
	}
	ref := ""
	for _, a := range provider.AgentAccounts(agent) {
		if id, ok := strings.CutPrefix(a, pid+"/"); ok {
			ref = id
		}
	}
	return ref
}

// errOwnAccountOff is magpie's own ask of a plan's own service while the
// account or key magpie's own requests are set to use on it is off.
func errOwnAccountOff(p provider.Provider) error {
	return cantServe{fmt.Errorf("%s: the account or key magpie's own requests are set to use is off (the provider's settings in magpie, Advanced, Accounts by agent)", p.Name)}
}
