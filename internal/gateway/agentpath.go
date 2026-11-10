package gateway

import (
	"context"
	"net/http"
	"strings"

	"github.com/yetone/magpie/internal/usage"
)

// AgentPath is the prefix of a gateway address that names the agent
// calling it, for an agent that can be given neither a key of its own
// (TokenFor) nor a User-Agent: OpenBot seals the key of an endpoint it
// lists in safeStorage, which magpie doesn't open, and hands the endpoint
// to the OpenCode it runs, whose requests say "opencode/…". So its
// requests were counted and listed as OpenCode's (SSNQT on X). Given
// <gateway>/agent/openbot/v1, the gateway reads /agent/openbot off the
// path and serves the rest as it is.
const AgentPath = "/agent/"

// AgentURL is the gateway at gw, as the agent id reaches it (AgentPath).
func AgentURL(gw, id string) string { return strings.TrimRight(gw, "/") + AgentPath + id }

// agentPathCtx holds the agent a request's path named (takeAgentPath).
type agentPathCtx struct{}

// takeAgentPath is r without the AgentPath its path begins with, the agent
// it names kept for agentOf: /agent/openbot/v1/chat/completions is
// /v1/chat/completions from OpenBot. A path naming no agent magpie knows is
// left as it is.
func takeAgentPath(r *http.Request) *http.Request {
	rest, ok := strings.CutPrefix(r.URL.Path, AgentPath)
	if !ok {
		return r
	}
	id, rest, _ := strings.Cut(rest, "/")
	if id == "" || !usage.IsAgent(id) {
		return r
	}
	r2 := r.Clone(context.WithValue(r.Context(), agentPathCtx{}, usage.AgentOf(id)))
	r2.URL.Path = "/" + rest
	if raw := r2.URL.RawPath; raw != "" {
		if rawRest, ok := strings.CutPrefix(raw, AgentPath+id); ok {
			r2.URL.RawPath = "/" + strings.TrimPrefix(rawRest, "/")
		} else {
			r2.URL.RawPath = ""
		}
	}
	r2.RequestURI = r2.URL.RequestURI()
	return r2
}

// pathAgent is the agent r's path named (takeAgentPath), or "".
func pathAgent(r *http.Request) string {
	id, _ := r.Context().Value(agentPathCtx{}).(string)
	return id
}
