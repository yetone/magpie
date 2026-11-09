package provider

import (
	"context"
	"net/http"
	"net/url"
	"strings"
)

// The ways a provider's Socket is set: SocketAuto tries the Responses
// WebSocket and leaves a host that refuses its handshake to HTTP for a
// while; SocketOn tries it every turn. Off is "".
const (
	SocketAuto = "auto"
	SocketOn   = "on"
)

// ParseSocket is the Socket v sets: auto, on, or "" for off (off, no,
// default or empty); ok is false for anything else.
func ParseSocket(v string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case SocketAuto:
		return SocketAuto, true
	case SocketOn, "yes", "true":
		return SocketOn, true
	case "", "off", "no", "false", "default":
		return "", true
	}
	return "", false
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
	if p.Socket == "" || !p.TakesSocket() {
		return socketAt{}, false
	}
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(p.Responses), "/"))
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return socketAt{}, false
	}
	return socketAt{host: u.Host, path: u.Path + "/responses", shun: p.Socket != SocketOn}, true
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
	if at.host == "" || !strings.EqualFold(req.URL.Host, at.host) || req.URL.Path != at.path {
		return false, false
	}
	return true, at.shun
}
