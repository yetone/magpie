package provider

import (
	"fmt"
	"maps"
	"strings"
)

// An agent can be set to use one account or key of a provider (nb5p on
// Discord: Claude Code on one key of a relay, Codex on another, so the
// relay's dashboard shows each one's use). Provider.AgentAccounts keeps
// it, the agent's id (usage.AgentOf's) to the account's or key's id
// (AccountIDs); the gateway holds the agent's requests to it
// (access.Identity.HeldToAgent). An agent not in it uses the provider as
// every caller does.

// SetAgentAccount sets the account or key of provider id that agent
// uses, ref an account's or key's id or the name it is shown by; "" to
// let the agent use every one again.
func SetAgentAccount(id, agent, ref string) error {
	agent = strings.ToLower(strings.TrimSpace(agent))
	if agent == "" {
		return fmt.Errorf("which agent?")
	}
	p, err := Find(id)
	if err != nil {
		return err
	}
	ref = strings.TrimSpace(ref)
	if ref != "" {
		a, ok := p.agentAccountRef(ref)
		if !ok {
			return fmt.Errorf("%s has no account or key %q", p.Name, ref)
		}
		ref = a
	}
	if p.AgentAccounts[agent] == ref {
		return nil
	}
	m := maps.Clone(p.AgentAccounts)
	if ref == "" {
		delete(m, agent)
	} else {
		if m == nil {
			m = map[string]string{}
		}
		m[agent] = ref
	}
	p.AgentAccounts = m
	return Save(*p)
}

// agentAccountRef is ref, an account's or key's id or name, as p's
// AccountIDs give its id.
func (p Provider) agentAccountRef(ref string) (string, bool) {
	ids := p.AccountIDs()
	for _, a := range ids {
		if strings.EqualFold(a.ID, ref) {
			return a.ID, true
		}
	}
	for _, a := range ids {
		if strings.EqualFold(a.User, ref) || strings.EqualFold(a.Shown(p), ref) {
			return a.ID, true
		}
	}
	return "", false
}

// AgentAccounts is every account or key agent is set to use, one a
// provider, as an account list keeps them ("<provider>/<id>"). One the
// provider no longer has (a key taken out, an account signed out) holds
// nothing: the agent uses that provider as before.
func AgentAccounts(agent string) []string {
	agent = strings.ToLower(strings.TrimSpace(agent))
	if agent == "" || !agentAccountSaved(agent) {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, p := range sharedAll() {
		ref := p.AgentAccounts[agent]
		if ref == "" || seen[p.ID] {
			continue
		}
		seen[p.ID] = true
		if a, ok := p.agentAccountRef(ref); ok {
			out = append(out, p.ID+"/"+a)
		}
	}
	return out
}

// agentAccountSaved is whether any saved provider sets agent to an
// account, read from the parsed file: the gateway asks for every request
// of an agent, a Codex model list and a socket outside a request's hold
// among them, where building every provider (each sign-in read) would be
// a second build for one list (TestCodexModelListBuildsProvidersOnce).
func agentAccountSaved(agent string) bool {
	for _, p := range storedFile().Providers {
		for k, v := range p.AgentAccounts {
			if strings.TrimSpace(v) != "" && strings.EqualFold(strings.TrimSpace(k), agent) {
				return true
			}
		}
	}
	return false
}

// normalAgentAccounts is m as it is kept: agents in lower case, none
// with no account.
func normalAgentAccounts(m map[string]string) map[string]string {
	var out map[string]string
	for k, v := range m {
		k, v = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
		if k == "" || v == "" {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[k] = v
	}
	return out
}
