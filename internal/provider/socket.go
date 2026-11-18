package provider

import (
	"context"
	"net/http"
	"net/url"
	"strings"
)

// The ways a provider's Socket is set: SocketAuto tries the Responses
// WebSocket and leaves a host that refuses its handshake to HTTP for a
// while; SocketOn tries it every turn; SocketOff is HTTP alone, where
// the default would be the socket. "" is the default (SocketMode).
const (
	SocketAuto = "auto"
	SocketOn   = "on"
	SocketOff  = "off"
)

// ParseSocket is the Socket v sets: auto, on, off (off, no or false), or
// "" for the default (default or empty); ok is false for anything else.
func ParseSocket(v string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case SocketAuto:
		return SocketAuto, true
	case SocketOn, "yes", "true":
		return SocketOn, true
	case SocketOff, "no", "false":
		return SocketOff, true
	case "", "default":
		return "", true
	}
	return "", false
}

// SocketMode is how p's Responses turns go: SocketAuto, SocketOn, or ""
// for HTTP alone. A Socket the user set is kept; unset, OpenAI's own API
// (an https Responses URL at api.openai.com) is auto, as Codex's built-in
// OpenAI provider speaks its WebSocket mode to it (supports_websockets in
// codex-rs's create_openai_provider). There a conversation's next turn
// goes as its new input after previous_response_id, even with store
// false, where HTTP sends the whole conversation every turn (KawaiiZapic
// on X). Any other address is HTTP alone unless set.
func (p Provider) SocketMode() string {
	switch p.Socket {
	case SocketOff:
		return ""
	case SocketAuto, SocketOn:
		return p.Socket
	}
	if p.SocketByDefault() {
		return SocketAuto
	}
	return ""
}

// SocketByDefault says p's turns go over the Responses WebSocket with
// Socket unset (SocketMode).
func (p Provider) SocketByDefault() bool {
	if !p.TakesSocket() || p.PluginProvider() != "" {
		return false
	}
	u, err := url.Parse(strings.TrimSpace(p.Responses))
	return err == nil && u.Scheme == "https" && strings.EqualFold(u.Hostname(), "api.openai.com")
}

// TakesSocket says p can have its turns go over the Responses WebSocket
// (Socket): a provider of a key with a Responses URL. A signed-in account
// or a plugin's provider has its own way there.
func (p Provider) TakesSocket() bool {
	return p.Account == nil && p.Responses != "" && !p.IsVertex()
}

type socketKey struct{}

// socketAt is where a provider's Responses turns are, and whether a host
// that refuses the socket is left to HTTP for a while; zero for a provider
// whose turns go over HTTP alone.
type socketAt struct {
	host, path string
	shun       bool
	// copilot: a Copilot account's turns, at whatever API host its
	// session names (CopilotSocketTurn)
	copilot bool
}

// withSocket is ctx carrying where p's turns may go over the Responses
// WebSocket; one that has none hides any it carried before, so a
// provider never takes another's.
func withSocket(ctx context.Context, p Provider) context.Context {
	at, ok := p.socketAt()
	if !ok {
		if _, had := ctx.Value(socketKey{}).(socketAt); had {
			return context.WithValue(ctx, socketKey{}, socketAt{})
		}
		return ctx
	}
	return context.WithValue(ctx, socketKey{}, at)
}

func (p Provider) socketAt() (socketAt, bool) {
	if p.copilotSocket() {
		return socketAt{copilot: true, shun: true}, true
	}
	mode := p.SocketMode()
	if mode == "" || !p.TakesSocket() {
		return socketAt{}, false
	}
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(p.Responses), "/"))
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return socketAt{}, false
	}
	return socketAt{host: u.Host, path: u.Path + "/responses", shun: mode != SocketOn}, true
}

// SocketTurn says req is a Responses turn that may go over the Responses
// WebSocket: one to chatgpt.com's Codex backend, or to the Responses URL
// of a provider whose Socket is set (its ctx made by Via). shun says a
// refused handshake leaves the host to HTTP for a while.
func SocketTurn(req *http.Request) (ok, shun bool) {
	if req == nil || req.URL == nil {
		return false, false
	}
	if codexTurnURL(req.URL) {
		return true, true
	}
	at, _ := req.Context().Value(socketKey{}).(socketAt)
	if at.copilot {
		return req.URL.Path == "/responses", true
	}
	if at.host == "" || !strings.EqualFold(req.URL.Host, at.host) || req.URL.Path != at.path {
		return false, false
	}
	return true, at.shun
}

// A Copilot account's Responses turn goes over Copilot's Responses
// WebSocket, at the same /responses address, for a model whose
// supported_endpoints list "ws:/responses", as Copilot Chat sends it
// (microsoft/vscode extensions/copilot chatEndpoint.ts: useWebSocket,
// ModelSupportedEndpoint.WebSocketResponses). There a conversation's next
// turn goes on the same socket as its new input after
// previous_response_id with store false, where over HTTP Copilot answers
// previous_response_id 400 "previous_response_id is not supported" and
// every turn goes whole (yuushabu_buyinn on X: 129 GB up for 31 GB down).
// Socket "off" keeps an account's turns on HTTP.
func (p Provider) copilotSocket() bool {
	return p.Account != nil && p.Account.Agent == "copilot" && p.Socket != SocketOff
}

// CopilotSocketTurn says req is a Copilot account's Responses turn that
// may go over Copilot's WebSocket (SocketTurn), its model aside
// (CopilotSocketModel).
func CopilotSocketTurn(req *http.Request) bool {
	if req == nil || req.URL == nil {
		return false
	}
	at, _ := req.Context().Value(socketKey{}).(socketAt)
	return at.copilot && req.URL.Path == "/responses"
}

// CopilotSocketModel says Copilot's list named model served over its
// Responses WebSocket ("ws:/responses" in its supported_endpoints).
func CopilotSocketModel(model string) bool {
	copilotSeenMu.Lock()
	defer copilotSeenMu.Unlock()
	return copilotSeenWS[model]
}
