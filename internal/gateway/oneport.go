package gateway

import (
	"context"
	"net/http"
	"path"
	"strings"
)

// Front is what the gateway's own listener serves: Handler behind
// lanGuard, built once. `magpie web`'s port hands the gateway's client
// routes (ClientPath) to it too, so a request there is judged exactly as
// one on the gateway's port: the allow list, a key from another machine
// or through a proxy, a rebound page's Host, and corsGuard's Origin rules.
func (s *Server) Front() http.Handler {
	// the goroutine dump ahead of lanGuard too, whose allow list reads
	// settings.json under its lock (dumpFirst)
	// (socketEntry: a socket's turns go through all of it again)
	s.frontOnce.Do(func() { s.front = socketEntry(s.dumpFirst(lanGuard(s.Handler()))) })
	return s.front
}

// clientPrefixes are the gateway's client routes (Handler) that a page
// served on a port of its own passes on to the gateway: everything an
// agent or an SDK calls with the gateway as its base URL.
var clientPrefixes = []string{
	"/v1", "/v1beta",
	"/models", "/chat/completions", "/responses", "/messages",
	"/images", "/embeddings", "/rerank", "/decisions", "/audio", "/videos",
	"/muse-code/models", CodexPath, AlphaSearchPath, "/mcp",
	// the key page (KeyPagePath): a key's holder opens it at the address
	// they give their agents
	KeyPagePath,
	// and the join page (JoinPagePath), at the same address
	JoinPagePath,
}

// ClientPath: p is one of the gateway's client routes, which `magpie web`
// answers on its own port too by handing it to the gateway, so one address
// (one reverse-proxy location) serves the page and the agents (zgcwkj on
// Discord). The page's own routes keep its key: /api/* is the page's, so
// the Ollama and Claude Desktop routes there, and the Claude helper's
// callbacks under /_magpie/, stay on the gateway's port alone. A path that
// isn't clean (a "..", a "//") is none, and stays behind the page's key.
func ClientPath(p string) bool {
	if p == "" || path.Clean(p) != strings.TrimSuffix(p, "/") && p != "/" {
		return false
	}
	for _, pre := range clientPrefixes {
		if p == pre || strings.HasPrefix(p, pre+"/") {
			return true
		}
	}
	return false
}

// PagePortHeader marks a request `magpie web`'s page port handed to the
// gateway (FromPagePort). The page's port is the one put behind a proxy
// or a forward, and a raw TCP forward (frp's tcp, ssh -R, socat) or a
// proxy that adds no forwarding header reaches it from loopback with
// nothing to tell it from an agent here. So no request through it is
// local: each is judged as one from another machine, answered only while
// magpie is shared and with an enabled gateway key, whatever peer, Host
// or browser marks it has, and MAGPIE_TRUST_PROXY doesn't change that
// (reported privately by Coffemon). Agents here call the gateway's own
// port, which needs no key from loopback, as before the page's port
// answered client routes. The header only ever takes a request's
// locality away, so one a client sends itself costs nobody else; lanGuard
// moves it into the request's context, so it never goes on upstream.
const PagePortHeader = "X-Magpie-Page-Port"

// pagePort is the context key of a request the page's port handed over.
type pagePort struct{}

// FromPagePort is r marked as handed over by the page's port: for the
// gateway this process serves (Front), or the header toGateway passes on.
func FromPagePort(r *http.Request) *http.Request {
	r = r.Clone(r.Context())
	r.Header.Set(PagePortHeader, "1")
	return r
}

// viaPagePort: r came through `magpie web`'s page port (FromPagePort).
func viaPagePort(r *http.Request) bool {
	ok, _ := r.Context().Value(pagePort{}).(bool)
	return ok || r.Header.Get(PagePortHeader) != ""
}

// takePagePort moves the page port's mark from r's headers into its
// context, so nothing passes the header on to a vendor.
func takePagePort(r *http.Request) *http.Request {
	if r.Header.Get(PagePortHeader) == "" {
		return r
	}
	r = r.WithContext(context.WithValue(r.Context(), pagePort{}, true))
	r.Header = r.Header.Clone()
	r.Header.Del(PagePortHeader)
	return r
}
