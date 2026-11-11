package library

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/mcpauth"
)

// A remote server that asks for an OAuth sign-in is signed in to once, in
// magpie, and every agent given it reaches it through the gateway's
// /mcp/<name>, which adds magpie's token (#615): the agents are given that
// address in place of the server's. A server magpie isn't signed in to is
// given as it is, and the agent signs in itself as before.

func init() {
	mcpauth.Changed = func(string) { _, _ = Sync() }
	// an agent beyond loopback is given a relayed server, or the MCP hub,
	// with the LAN key (through, hubServerFor), which is the one there is
	// no longer
	access.LANChanged = func() {
		if relaysAny() || (theHub{}).On() {
			_, _ = Sync()
		}
	}
}

// relaysAny says whether any of the library's servers is given through the
// gateway: one magpie is signed in to.
func relaysAny() bool {
	mu.Lock()
	l, err := load()
	mu.Unlock()
	if err != nil {
		return false
	}
	return slices.ContainsFunc(l.MCP, func(s *Server) bool { return s.Transport == "http" && mcpauth.SignedIn(s.Name, s.URL) })
}

// through is the server as an agent that reaches the gateway at base is
// given it. Only a streamable HTTP server is relayed: an SSE one's stream
// names the address to post to, which would have to be rewritten.
func through(s *Server, base string) *Server {
	s = s.live()
	if s.Plugin != "" {
		// a plugin's tools, which the gateway serves itself
		c := *s
		c.URL = pluginURL(base, s.Plugin)
		if k := agent.KeyAt(base); k != gateway.Token {
			c.Headers = map[string]string{"Authorization": "Bearer " + k}
		}
		return &c
	}
	if s.Transport != "http" || !mcpauth.SignedIn(s.Name, s.URL) {
		return s
	}
	c := *s
	c.URL = strings.TrimRight(base, "/") + "/mcp/" + url.PathEscape(s.Name)
	c.Headers = nil
	for k, v := range s.Headers {
		// magpie's token goes in its place
		if strings.EqualFold(k, "Authorization") {
			continue
		}
		if c.Headers == nil {
			c.Headers = map[string]string{}
		}
		c.Headers[k] = v
	}
	if k := agent.KeyAt(base); k != gateway.Token {
		// from another machine (a WSL distro under NAT) the gateway refuses
		// a call without an enabled key, with a 401 the agent would take
		// for the server asking it to sign in; the relay drops this
		// Authorization for magpie's token
		if c.Headers == nil {
			c.Headers = map[string]string{}
		}
		c.Headers["Authorization"] = "Bearer " + k
	}
	return &c
}

// gatewayOf is the gateway's address as the target's agent reaches it.
func gatewayOf(t *Target) string {
	if t.Agent != nil && t.Agent.Gateway != nil {
		return t.Agent.Gateway()
	}
	return gateway.URL()
}

var errNotHTTP = errors.New("magpie signs in only to a server reached over streamable HTTP")

// SignInServer starts signing magpie in to the library's server called
// name: the State's URL is the page to open in a browser.
func SignInServer(ctx context.Context, name string) (mcpauth.State, error) {
	return signInServer(ctx, name, nil)
}

func signInServer(ctx context.Context, name string, label *string) (mcpauth.State, error) {
	mu.Lock()
	l, err := load()
	mu.Unlock()
	if err != nil {
		return mcpauth.State{}, err
	}
	s := l.server(name)
	if s == nil {
		return mcpauth.State{}, fmt.Errorf("no server called %s", name)
	}
	if s.Transport != "http" {
		return mcpauth.State{}, errNotHTTP
	}
	var app *mcpauth.App
	if s.OAuth != nil {
		app = &mcpauth.App{ClientID: s.OAuth.ClientID, ClientSecret: s.OAuth.ClientSecret, CallbackPort: s.OAuth.CallbackPort}
	}
	if label != nil {
		return mcpauth.StartAccount(ctx, s.Name, s.URL, app, *label)
	}
	return mcpauth.StartWith(ctx, s.Name, s.URL, app)
}

// SignOutServer forgets magpie's sign-in to the server called name, and
// gives the agents the server's own address again.
func SignOutServer(name string) error { return mcpauth.SignOut(name) }
