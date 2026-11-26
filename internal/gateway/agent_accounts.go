package gateway

import (
	"net/http"
	"strings"

	"github.com/yetone/magpie/internal/access"
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
		agent := callerOf(r).agent
		if agent == "" {
			next.ServeHTTP(w, r)
			return
		}
		set := provider.AgentAccounts(agent)
		if len(set) == 0 {
			next.ServeHTTP(w, r)
			return
		}
		who := access.Caller(r.Context()).HeldToAgent(agent, set)
		next.ServeHTTP(w, r.WithContext(access.WithIdentity(r.Context(), who)))
	})
}

// nativeAgentAccount is p on the account or key r's agent is set to use
// on it (agentHeld), p itself when none is set or p no longer has it.
func nativeAgentAccount(r *http.Request, p provider.Provider) provider.Provider {
	agent := callerOf(r).agent
	if agent == "" {
		return p
	}
	ref := ""
	for _, a := range provider.AgentAccounts(agent) {
		if pid, id, ok := strings.Cut(a, "/"); ok && pid == p.ID {
			ref = id
		}
	}
	if ref == "" || strings.EqualFold(p.AccountID(), ref) {
		return p
	}
	for _, k := range p.KeysOn() {
		if strings.EqualFold(provider.KeyID(k.Key), ref) {
			return p.WithKey(k)
		}
	}
	for _, a := range p.AccountsOn() {
		if strings.EqualFold(a.Provider.AccountID(), ref) {
			return a.Provider
		}
	}
	return p
}
