package gateway

import (
	"cmp"
	"net/http"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

func (server *Server) drawingModel(request *http.Request, id string) (provider.Provider, string, bool) {
	if agentOf(request) != "codex" || strings.Contains(id, "/") {
		return resolveDrawing(id)
	}
	preferred, group := server.codexDrawingProvider(request)
	if group != "" && GroupDraws(group) {
		// the turn's routing group has an image model of its own: Codex
		// draws on it, on the provider the user put it there with, not on
		// a chat member's provider or Settings' pick (changqiu4890 on
		// Discord)
		return resolveDrawing(group)
	}
	if preferred != "" {
		if matched, err := provider.Find(preferred); err == nil && matched.On() && !matched.DecideOnly() {
			for _, model := range Drawers(*matched) {
				if model.ID == id {
					return *matched, id, true
				}
			}
			if drawsFamily(*matched, id) {
				return *matched, drawnAs(*matched, id), true
			}
		}
	}
	if selected, ok := drawer(); ok {
		return resolveDrawing(selected)
	}
	return provider.Provider{}, "", false
}

func drawingTurnID(id string) string {
	id = strings.TrimSpace(id)
	if len(id) > 128 || strings.ContainsAny(id, "\r\n\t") {
		return ""
	}
	return id
}

// codexDrawingProvider is the provider that served the Codex turn an
// image tool call is of, and the routing group the turn asked for, if it
// did.
func (server *Server) codexDrawingProvider(request *http.Request) (string, string) {
	rawTurn := strings.TrimSpace(request.Header.Get("x-codex-image-turn-id"))
	turn := drawingTurnID(rawTurn)
	session := sessionOf(request.Header)
	if rawTurn != "" && turn == "" || turn == "" && session == "" {
		return "", ""
	}
	caller := codexTurnKey(request, callerOf(request).agent)
	server.trace.mu.Lock()
	defer server.trace.mu.Unlock()
	for index := len(server.trace.routes) - 1; index >= 0; index-- {
		route := server.trace.routes[index]
		if route.imageCaller != caller || route.Kind != "" && route.Kind != "luna_reserve" {
			continue
		}
		if turn != "" && route.imageTurn != turn || session != "" && route.Session != session {
			continue
		}
		group := ""
		if route.Group != nil {
			group = cmp.Or(route.Group.Ref, provider.GroupPrefix+route.Group.ID)
		}
		if route.Done && (route.Status >= 400 || route.Error != "") {
			// a failed turn served nothing; its group is still the user's
			return "", group
		}
		return route.imageProvider, group
	}
	return "", ""
}
