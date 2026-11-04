package gateway

import (
	"context"
	"net"
	"net/http"
	"slices"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/gwmw"
	"github.com/yetone/magpie/internal/middleware"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
)

// serveAgent is serve for a request an agent sent: the agent middleware
// of the gateway's middleware point (gwmw.Agents: the JS gateway middleware
// plugins, internal/middleware, while plugins/middleware is on) see its
// body first, and its reply on the way back, in the agent's own API.
// magpie's own requests through serve (titles, the router's classifier,
// search and vision stand-ins) go without them, as their replies are
// magpie's to read.
func (s *Server) serveAgent(w http.ResponseWriter, r *http.Request, from provider.Protocol, body []byte) {
	withGatewaySession(w, r)
	// the agent's own turn, which the user's model for requests masking
	// would change is for (maskRouteFor)
	r = agentTurn(r)
	// a Responses request naming the response it follows is given the
	// conversation whole before anything reads it (prev_response.go)
	w, r, body, kept := previousResponse(w, r, from, body)
	defer kept()
	if id := keptTurnLost(r); id != "" {
		// another magpie's delta after a turn this one doesn't hold:
		// it sends the conversation whole (remote_delta.go)
		writePrevNotFound(w, id)
		return
	}
	w, recorded := recordConversation(w, r, from, body)
	defer recorded()
	// a conversation going round one tool call is answered here, before
	// anything of it is asked of a hook, a middleware or a provider
	if s.stoppedToolLoop(w, r, from, body) {
		return
	}
	w, body, hooked := gatewayHooks(w, r, from, body)
	defer hooked()
	agents := gwmw.AgentsWanted(middlewareWanted)
	if len(agents) == 0 {
		s.serve(w, r, from, body)
		return
	}
	info := gwmw.Info{Protocol: string(from), Model: modelOf(body), Stream: streamOf(body), Path: r.URL.Path, Agent: agentOf(r)}
	var runs mwRuns
	for _, a := range agents {
		// the user's middleware runs before a route is begun: a wait
		// magpie knows of (unrouted.go)
		waited := noteWait(r.Context(), "JS middleware")
		run := a.Begin(info)
		if run == nil {
			waited()
			continue
		}
		defer run.End()
		if t, ok := run.(gwmw.Tried); ok {
			runs.tried = append(runs.tried, t)
		}
		if t, ok := run.(gwmw.Accounts); ok {
			runs.accounts = append(runs.accounts, t)
		}
		var turned *gwmw.Reject
		body, turned = run.Request(body)
		waited()
		if turned != nil {
			writeError(w, from, turned.Status, turned.Message)
			return
		}
		var done func()
		w, done = run.Wrap(w)
		defer done()
	}
	if len(runs.tried) > 0 || len(runs.accounts) > 0 {
		r = r.WithContext(context.WithValue(r.Context(), triedKey{}, runs))
	}
	s.serve(w, r, from, body)
}

type triedKey struct{}

// mwRuns are the middleware of an agent's request that are asked again on
// each try: tried change its body (onProvider), accounts may pass its
// account over (onAccount).
type mwRuns struct {
	tried    []gwmw.Tried
	accounts []gwmw.Accounts
}

// triedOn is body as it goes to model on p, in the agent's API: what each
// middleware of the request that is also gwmw.Tried (the JS chain's
// onProvider) made of it for that provider. Each try starts from the body
// it is given, so a change for one provider never reaches another's.
func triedOn(ctx context.Context, p provider.Provider, model string, body []byte) []byte {
	runs, _ := ctx.Value(triedKey{}).(mwRuns)
	if len(runs.tried) == 0 {
		return body
	}
	to := gwmw.To{ID: p.ID, Name: p.Name, Account: accountAgent(p), Model: model}
	for _, t := range runs.tried {
		body = t.Tried(body, to)
	}
	return body
}

// accountSkipped asks the request's middleware with onAccount whether the
// account p is may be used for this try of model, before anything of the
// try is done: by is the middleware that passed it over, "" when none did.
// A try on a key asks none, and tells them it is no account's.
func accountSkipped(ctx context.Context, p provider.Provider, model string) (by string) {
	runs, _ := ctx.Value(triedKey{}).(mwRuns)
	if len(runs.accounts) == 0 {
		return ""
	}
	a := gwmw.Account{ID: p.ID, Name: p.Name, Model: model}
	if p.Account != nil {
		a.Agent, a.User, a.Plan = p.Account.Agent, p.Account.User, p.Account.Plan
	}
	for _, t := range runs.accounts {
		if skip, who := t.Account(a); skip {
			return who
		}
	}
	return ""
}

// ownAsk is ctx for a request magpie asks of its own gateway while it
// serves an agent's (a web search, an image's description): the agent's
// middleware isn't run on it, as on magpie's other own requests. Its
// runtimes are the agent request's, which one goroutine may use at a time
// (moejs), and Run has no lock to hold callers to it, while these asks go
// beside the request and each other: images described at once ran
// onProvider on one runtime, and the gateway died of "concurrent map read
// and map write" (#3695). Nor does the hooks' ctx describe them: a
// description's body is Chat and a search's Anthropic, whatever
// ctx.protocol names, and ctx.model and ctx.stream are the agent's. Nor
// is it the agent's turn, which a model set for requests masking would
// change takes (maskRouteFor): it is masked as magpie's own asks are.
func ownAsk(ctx context.Context) context.Context {
	return context.WithValue(context.WithValue(ctx, triedKey{}, mwRuns{}), agentTurnKey{}, false)
}

// middlewareWanted: plugins.json has a JS middleware on, so plugins/middleware
// is in use.
func middlewareWanted() bool { return middleware.On() > 0 }

// gatewayHooks runs the asynchronous request hooks (OpenCode plugins'
// gateway entries, in the plugin host) on an agent's request: the body they
// leave is the one served, and they see the reply the agent got once it is
// whole. Its writer wraps the middleware's, so what they see is what the
// agent was given. With no hook it is the request as it came.
func gatewayHooks(w http.ResponseWriter, r *http.Request, from provider.Protocol, body []byte) (http.ResponseWriter, []byte, func()) {
	if !plugin.HasGateway() {
		return w, body, func() {}
	}
	info := plugin.GatewayContext{Origin: gatewayOrigin(r), Protocol: string(from), Model: modelOf(body), Stream: streamOf(body), Path: r.URL.Path, Agent: agentOf(r), Session: sessionOf(r.Header)}
	if req, err := parse(from, body); err == nil {
		info.Cwd = workDir(req)
		info.Query = gatewayQuery(req)
	}
	// the plugin host, started for it if need be: a wait magpie knows of
	// before a route is begun (unrouted.go)
	waited := noteWait(r.Context(), "plugin hooks")
	run := plugin.GatewayBegin(r.Context(), body, info)
	waited()
	if run == nil {
		return w, body, func() {}
	}
	w, done := gatewayResponse(w, r, from, run.Body, run)
	return w, run.Body, func() { done(); run.Close() }
}

// gatewayQuery uses the same user-authored words as routing, excluding
// agent context, tool results and images. Tool-only increments have none.
func gatewayQuery(req *Request) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		m := req.Messages[i]
		if m.Role != "user" {
			continue
		}
		if query := userSaid(m); query != "" {
			return query
		}
	}
	return ""
}

// gatewayOrigin exposes provenance, never credentials or peer addresses.
// Forwarded traffic remains remote even when the administrator trusts its
// proxy for authentication: that trust does not establish conversation ownership.
//
// The sharing key magpie made itself is "magpie-key" only when it comes
// from this computer (ownComputer): that is where magpie writes it into
// the owner's own agents. The same key is listed under Gateway keys with
// a Copy button and offered to connect other computers, so it can be in a
// friend's hands too, and from anywhere else it stays "gateway-key", as
// every other gateway key does — one the owner made for someone else, a
// value a user brought, or one that didn't authenticate. A plugin can then
// serve the owner's own agents without serving a friend's key
// (plugins#526). Plugins that trust only "local" see both as not local.
func gatewayOrigin(r *http.Request) string {
	if who := access.Caller(r.Context()); who.KeyID != "" && who.Default && ownComputer(r) {
		return "magpie-key"
	} else if who.KeyID != "" || managedKey(r) {
		return "gateway-key"
	}
	if local(r) && !forwarded(r) {
		return "local"
	}
	return "remote"
}

func forwarded(r *http.Request) bool {
	return slices.ContainsFunc(forwardHeaders, func(h string) bool { return r.Header.Get(h) != "" })
}

// ownComputer: r's connection was made on this computer, where magpie
// writes the sharing key into agents (agent.keyAt): loopback (a WSL
// distro on the WSL network is mapped to it by lanGuard), one of this
// computer's own addresses (an agent calling MAGPIE_ADDR beyond loopback),
// or a WSL distro's address under NAT calling this computer's network
// address. Never a request with forwarding headers, a trusted proxy's
// included, nor one through magpie web's page port.
func ownComputer(r *http.Request) bool {
	if forwarded(r) || viaPagePort(r) {
		return false
	}
	if local(r) {
		return true
	}
	a, ok := peerAddr(r)
	if !ok {
		return false
	}
	if ownAddr(a) {
		return true
	}
	ip := net.IP(a.AsSlice())
	return slices.ContainsFunc(WSLNets(), func(n *net.IPNet) bool { return n.Contains(ip) })
}
