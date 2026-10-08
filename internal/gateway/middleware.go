package gateway

import (
	"net/http"

	"github.com/yetone/magpie/internal/middleware"
	"github.com/yetone/magpie/internal/provider"
)

// serveAgent is serve for a request an agent sent: the gateway middleware
// plugins (internal/middleware) see its body first, and its reply on the
// way back, in the agent's own API. magpie's own requests through serve
// (titles, the router's classifier, search and vision stand-ins) go
// without them, as their replies are magpie's to read.
func (s *Server) serveAgent(w http.ResponseWriter, r *http.Request, from provider.Protocol, body []byte) {
	r = withGatewaySession(w, r)
	w, recorded := recordConversation(w, r, from, body)
	defer recorded()
	run := middleware.Begin(middleware.Info{Protocol: string(from), Model: modelOf(body), Stream: streamOf(body), Path: r.URL.Path, Agent: agentOf(r)})
	if run == nil {
		s.serve(w, r, from, body)
		return
	}
	defer run.End()
	body, turned := run.Request(body)
	if turned != nil {
		writeError(w, from, turned.Status, turned.Message)
		return
	}
	w, done := run.Wrap(w)
	defer done()
	s.serve(w, r, from, body)
}
