package gateway

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
	"github.com/yetone/magpie/internal/usageapi"
)

// The usage log, as the Usage page reads it (HoGee_xxl on X: an app of
// their own reading magpie's usage over its gateway, as they can't over
// the GUI's /api/usage): GET /v1/magpie/usage is the page's summary of a
// ?period=, the same JSON as /api/usage, and GET /v1/magpie/usage/requests
// its requests a page at a time, with /api/usage/requests' filters. Both
// are read-only, and what was said in a call is not told here.
//
// They name accounts, keys and sessions, so, as /v1/magpie/quotas, they
// answer this machine, and another only with an enabled gateway key while
// magpie is shared. A key held to a budget, to some models or to some
// accounts is someone else's to use: it is told its own calls alone, from
// /v1/magpie/usage/requests, and not the summary of everyone's.

const usageRefused = "magpie's usage is told to another machine only when magpie is shared on the local network (Settings → Network and sharing → Share on local network) and the request carries its API key (Authorization: Bearer <key> or x-api-key: <key>)"

// heldKey: the request's gateway key is held to a budget, models or
// accounts, and so sees only its own usage.
func heldKey(who access.Identity) bool {
	return who.KeyID != "" && (who.Limited() || len(who.Models) > 0 || len(who.Accounts) > 0)
}

// heldRefused is what a held key is told where this magpie's own accounts
// would be: the quotas, their cards and history, a card's refresh and the
// accounts' lanes (321sssrt-bit, #1981: a friend given a key with a
// budget saw every account's card, email and balance on their Remote
// magpie's Usage page). The key's own limit stays at /v1/magpie/limit.
const heldRefused = "this gateway key is held to a budget, models or accounts, so it is told nothing of this magpie's accounts: GET /v1/magpie/limit tells it its own limit"

// heldQuotasOff is what a held key whose owner hasn't turned its quotas on
// is told by the quotas and the status line (scopeQuotas): it names the
// switch that tells it those of what it may use (#2216), as steven_4422
// read the bare refusal as "a held key gets no status line".
const heldQuotasOff = "this gateway key is held to a budget, models or accounts, so it is told nothing of this magpie's accounts until its owner turns on Quotas for it (Gateway → Gateway keys, or magpie gateway-key quotas <id> on); it is then told the accounts it may use of each provider whose every model it may use. GET /v1/magpie/limit tells it its own limit and what holds it"

// refuseHeld answers a held key 403 with msg, marked with
// provider.KeyHeldCode so another magpie shows the key's own card alone
// rather than "isn't shared", and says whether it did.
func refuseHeld(w http.ResponseWriter, r *http.Request, msg string) bool {
	if !heldKey(access.Caller(r.Context())) {
		return false
	}
	writeJSON(w, http.StatusForbidden, map[string]any{"error": map[string]any{"message": msg, "type": "permission_error", "code": provider.KeyHeldCode}})
	return true
}

func (s *Server) usageSummary(w http.ResponseWriter, r *http.Request) {
	if !local(r) && !sharedWith(r) {
		writeError(w, provider.Chat, http.StatusForbidden, usageRefused)
		return
	}
	if refuseHeld(w, r, "this gateway key is held to a budget, models or accounts, so it is told its own calls alone: GET /v1/magpie/usage/requests") {
		return
	}
	out := usageapi.State(usageapi.PeriodOf(r.URL.Query().Get("period")))
	if !local(r) {
		out.Path = "" // where the log is on this machine
	}
	writeJSON(w, 200, out)
}

func (s *Server) usageRequests(w http.ResponseWriter, r *http.Request) {
	if !local(r) && !sharedWith(r) {
		writeError(w, provider.Chat, http.StatusForbidden, usageRefused)
		return
	}
	q := r.URL.Query()
	offset, _ := strconv.Atoi(q.Get("offset"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	f := usageapi.FilterOf(q)
	who := access.Caller(r.Context())
	if _, ok := q["cursor"]; ok {
		s.usageFeed(w, r, q.Get("cursor"), f, limit, who)
		return
	}
	if !heldKey(who) {
		writeJSON(w, 200, withoutClientIPs(usageapi.LedgerPage(usageapi.PeriodOf(q.Get("period")), f, offset, limit)))
		return
	}
	f.CallerKey = who.KeyID
	writeJSON(w, 200, withoutClientIPs(usageapi.OwnPage(usageapi.LedgerPage(usageapi.PeriodOf(q.Get("period")), f, offset, limit), who.KeyID)))
}

// usageFeed answers ?cursor=: the requests as changes after a cursor (#6060,
// rdsaltbo on X), "start" to read them all, "now" for the cursor of this
// moment. A held key is told its own calls and the ids of its own that went.
func (s *Server) usageFeed(w http.ResponseWriter, r *http.Request, cursor string, f usage.Filter, limit int, who access.Identity) {
	key := ""
	if heldKey(who) {
		key = who.KeyID
		f.CallerKey = key
	}
	out, err := usageapi.Feed(cursor, f, limit, key)
	if err != nil {
		var fe *usage.FeedError
		if errors.As(err, &fe) {
			status := http.StatusBadRequest
			if fe.Code != "cursor_invalid" && fe.Code != "cursor_filter_changed" {
				status = http.StatusGone // expired, or of another feed: read again
			}
			writeJSON(w, status, map[string]any{"error": map[string]any{"message": fe.Message, "type": "invalid_request_error", "code": fe.Code}})
			return
		}
		writeError(w, provider.Chat, http.StatusInternalServerError, "magpie's usage feed could not be kept: "+err.Error())
		return
	}
	for i := range out.Rows {
		out.Rows[i].ClientIP = ""
	}
	writeJSON(w, 200, out)
}
