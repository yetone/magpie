package gateway

import (
	"log"
	"net/http"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// A gateway key reaches two surfaces, each chosen on its own
// (ifitscolorful on X: an MCP gateway's key must not be able to spend the
// subscriptions): the models, and the MCP servers. A key held to no model
// (access.NoModels, a key for MCP servers only) is refused every route of
// keyModelRoutes, and one held to no MCP server (access.NoMCP, for models
// only) every route of keyMCPRoutes. Both are judged by the pattern the
// request is routed to, before anything is read, reserved or sent, so no
// spelling of a route is left out; the key's lists hold it inside the
// routes it is let reach as before. TestKeySurfacesEveryRoute fails when a
// route is in neither list nor keySurfaceNeither, so a new one is sorted
// before it ships.

// keyModelRoutes are the routes that use a model or tell of the models
// and the accounts that serve them: the model APIs in every spelling
// (OpenAI's, Anthropic's, Gemini's, Ollama's, Codex's backend, a
// provider's native API), count_tokens, decisions, the chat page, the
// model lists, the quotas and the accounts behind them, and the routing.
var keyModelRoutes = map[string]bool{
	"GET /v1/models": true, "GET /models": true, "GET /v1/models/{id...}": true, "GET /muse-code/models": true,
	"GET /api/hello": true, "GET /api/version": true, "GET /api/tags": true,
	"POST /api/show": true, "POST /api/chat": true, "POST /api/generate": true,
	"GET /v1/magpie/quotas": true, "GET /v1/magpie/quotas/history": true, "GET " + AccountsPath: true,
	mgmtAuthFilesPattern: true, mgmtAPICallPattern: true, mgmtPattern: true,
	"GET " + provider.RemoteCardsPath: true, "POST " + provider.RemoteRefreshPath: true,
	"POST " + provider.RemoteCodexResetPath: true, "GET " + provider.RemoteAgentModelsPath: true,
	"GET /v1/magpie/route": true, "GET " + StatuslinePath: true, "POST " + StatuslinePath: true,
	"GET /v1/magpie/trace": true, "GET " + routeSnapshotPath: true, "GET " + routeEventsPath: true,
	"GET " + ChatPagePath + "/models":                     true,
	"POST " + ChatPagePath + "/anthropic/v1/messages":     true,
	"POST " + ChatPagePath + "/anthropic/v1/files":        true,
	"DELETE " + ChatPagePath + "/anthropic/v1/files/{id}": true,
	"POST " + ChatPagePath + "/v1/tools/asr":              true,
	"POST " + ChatPagePath + "/v1/tools/tts":              true,

	// an artifact page's own calls on a model: sample, speech, dictation,
	// and a comment sent to its agent
	"POST " + ArtifactPagePath + "{ref}/_api/dictation": true,
	"POST " + ArtifactPagePath + "{ref}/_api/tts":       true,
	"POST " + ArtifactPagePath + "{ref}/_api/comments":  true,
	"POST " + ArtifactPagePath + "{ref}/_api/sample":    true,

	"POST /v1/chat/completions": true, "POST /chat/completions": true,
	"POST /v1/responses": true, "POST /responses": true, "GET /v1/responses": true, "GET /responses": true,
	"POST /v1/responses/compact": true, "POST /responses/compact": true,
	"POST /v1/messages": true, "POST /messages": true, "POST /v1/systemone": true,
	"POST /v1" + AlphaSearchPath: true, "POST " + AlphaSearchPath: true, "POST /v1/messages/count_tokens": true,
	"POST /v1/images/generations": true, "POST /images/generations": true, "POST /v1/images/edits": true, "POST /images/edits": true,
	"POST /v1/embeddings": true, "POST /embeddings": true, "POST /v1/rerank": true, "POST /rerank": true,
	"POST /v1/decisions": true, "POST /decisions": true,
	"POST /v1/audio/transcriptions": true, "POST /audio/transcriptions": true,
	"POST /v1/audio/translations": true, "POST /audio/translations": true,
	"POST /v1/audio/speech": true, "POST /audio/speech": true,
	"GET /v1/realtime": true, "GET /realtime": true,
	"POST /v1/videos": true, "POST /videos": true, "GET /v1/videos/{id}": true, "GET /videos/{id}": true,
	"GET /v1/videos/{id}/content": true, "GET /videos/{id}/content": true,
	"POST " + NativePath + "{provider}/{path...}": true, "GET " + NativePath + "{provider}/{path...}": true,
	"DELETE " + NativePath + "{provider}/{path...}": true, "PUT " + NativePath + "{provider}/{path...}": true,
	"PATCH " + NativePath + "{provider}/{path...}": true,
	CodexPath + "/": true, "GET " + CodexCatalogPath: true,
	"GET /v1beta/models": true, "POST /v1beta/models/{call...}": true,
}

// keyMCPRoutes are the routes that reach an MCP server: the hub, the
// Library's servers relayed or run, magpie's own servers and the files
// they made, and the list of magpie's own.
var keyMCPRoutes = map[string]bool{
	"/mcp/{name}": true, "/mcp/run/{name}": true,
	SearchMCPPath: true, VisionMCPPath: true, ImageMCPPath: true, ImageMCPFiles + "{name}": true,
	BrowserMCPPath: true, MemoryMCPPath: true, SessionsMCPPath: true, ArtifactsMCPPath: true,
	ToolsMCPPath: true, ToolsMCPOne + "{plugin}": true,
	HubMCPPath + "{agent}": true, HubMCPAll: true,
	"GET " + OwnMCPPath:                           true,
	"POST " + ArtifactPagePath + "{ref}/_api/mcp": true,
}

// KeyNoModelsCode is the error code a key for MCP servers only is refused
// a model route with; KeyNoMCPCode the one a key for models only is
// refused an MCP route with.
const (
	KeyNoModelsCode = "gateway_key_mcp_only"
	KeyNoMCPCode    = "gateway_key_no_mcp"
)

// keySurfaceGuard refuses a key held to no model every route of
// keyModelRoutes, and one held to no MCP server every route of
// keyMCPRoutes. It sits after readOnlyGuard, where the key is known.
func keySurfaceGuard(mux *routeMux, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		who := access.Caller(r.Context())
		noModels, noMCP := who.ModelsOff(), who.MCPOff()
		if !noModels && !noMCP {
			next.ServeHTTP(w, r)
			return
		}
		// a path that isn't clean is routed to a redirect under the
		// pattern of where it goes, and judged again when it gets there
		_, pattern := mux.Handler(r)
		switch {
		case noModels && keyModelRoutes[pattern]:
			log.Printf("refused %s %s: gateway key %q is for MCP servers only", r.Method, r.URL.Path, who.KeyName)
			writeJSON(w, http.StatusForbidden, map[string]any{"error": map[string]any{
				"message": "the gateway key " + quoteName(who.KeyName) + " is for MCP servers only: it may use no model, nor read the quotas or accounts behind them. Its owner picks its models in Gateway → Gateway keys, or with magpie gateway-key models <id> all",
				"type":    "permission_error", "code": KeyNoModelsCode}})
			return
		case noMCP && keyMCPRoutes[pattern]:
			log.Printf("refused %s %s: gateway key %q may use no MCP server", r.Method, r.URL.Path, who.KeyName)
			writeJSON(w, http.StatusForbidden, map[string]any{"error": map[string]any{
				"message": "the gateway key " + quoteName(who.KeyName) + " may use no MCP server: it is for models only. Its owner picks its MCP servers in Gateway → Gateway keys, or with magpie gateway-key mcp <id> all",
				"type":    "permission_error", "code": KeyNoMCPCode}})
			return
		}
		next.ServeHTTP(w, r)
	})
}
