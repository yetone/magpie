package gateway

// GET or POST /v1/magpie/statusline: the text `magpie statusline` prints
// for Claude Code's status line, for a Claude Code on another computer
// than the gateway (steven_4422 on Discord runs magpie in Docker and
// Claude Code on the Linux host: the container can't write the host's
// settings.json, and a command naming a binary inside it runs nowhere
// else). The host needs only curl:
//
//	curl -fsS -m 1 -H 'Authorization: Bearer <key>' --data-binary @- http://<host>:3425/v1/magpie/statusline || true
//
// Claude Code puts its JSON on the command's stdin, and curl sends it as
// the body, whose session_id picks the "→ model" part (?session= does as
// well, for a GET). ?only=claude,a@example.com keeps those accounts, as
// `magpie statusline claude a@example.com` does. The line is made by
// provider.StatuslineText, the CLI's own, from the same kept windows
// (/v1/magpie/quotas?kept=1: no vendor asked) and the session's route.
//
// It answers like the quotas and the route it is made of: this computer,
// and another only with an enabled gateway key (lanGuard asks for it), a
// key held to a budget, models or accounts is told none of it, or only
// what it may use when its owner lets it be told its quotas, and the route
// of its own requests alone. A read-only key may ask it, and is told the
// accounts' use without the model (it may not ask for a route).

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// StatuslinePath is where the status line is told.
const StatuslinePath = "/v1/magpie/statusline"

func (s *Server) statusline(w http.ResponseWriter, r *http.Request) {
	if !local(r) && !sharedWith(r) {
		writeError(w, provider.Chat, http.StatusForbidden, "magpie's status line is told to another machine only when magpie is shared on the local network (Settings → Network and sharing → Share on local network) and the request carries its API key (Authorization: Bearer <key> or x-api-key: <key>)")
		return
	}
	scope, ok := scopeQuotas(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	session := strings.TrimSpace(q.Get("session"))
	if r.Method == http.MethodPost {
		// Claude Code's JSON, as the command's stdin; small, and one that
		// isn't JSON only leaves the session out
		var in struct {
			SessionID string `json:"session_id"`
		}
		if json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in) == nil && session == "" {
			session = strings.TrimSpace(in.SessionID)
		}
	}
	if len(session) > 128 {
		session = session[:128] // as sessionOf keeps it
	}
	model := ""
	who := access.Caller(r.Context())
	// a read-only key is refused /v1/magpie/route (another key's routes), so
	// it is told no model here either
	if session != "" && !who.ReadOnly {
		only := ""
		if heldKey(who) {
			only = who.KeyID
		}
		if _, route := s.trace.sessionLatest(r.Context(), session, only, 0, 0); route != nil {
			sr := sessionRouteOf(*route)
			model = sr.Model
			if model == "" {
				model = sr.Asked
			}
		}
	}
	var names []string
	for _, v := range q["only"] {
		for _, n := range strings.Split(v, ",") {
			if n = strings.TrimSpace(n); n != "" {
				names = append(names, n)
			}
		}
	}
	now := time.Now()
	line := provider.StatuslineText(model, scope.quotas(provider.KeptQuotaReport(now)), names, now)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, line+"\n")
}
