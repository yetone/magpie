package gateway

import (
	"net/http"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

func (server *Server) drawingModel(request *http.Request, id string) (provider.Provider, string, bool) {
	if agentOf(request) != "codex" || strings.Contains(id, "/") {
		return resolveDrawing(id)
	}
	if preferred := server.codexDrawingProvider(request); preferred != "" {
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

func (server *Server) codexDrawingProvider(request *http.Request) string {
	rawTurn := strings.TrimSpace(request.Header.Get("x-codex-image-turn-id"))
	turn := drawingTurnID(rawTurn)
	session := sessionOf(request.Header)
	if rawTurn != "" && turn == "" || turn == "" && session == "" {
		return ""
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
		if route.Done && (route.Status >= 400 || route.Error != "") {
			return ""
		}
		return route.imageProvider
	}
	return ""
}
