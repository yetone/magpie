package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// CLIProxyAPI's management API, the part of it a usage reader asks, so
// such a reader shows magpie's pooled limits (#7402): T3 Code's Usage
// providers → Add hub takes a CLIProxyAPI hub's URL and management key,
// lists its accounts (GET /v0/management/auth-files) and reads each
// one's usage through the hub (POST /v0/management/api-call, the hub
// asking the vendor with the account's token), then blocks and resumes
// a thread whose endpoint is the hub's origin by them. Here the hub is
// the gateway, and the key a gateway key; a read-only one is enough.
//
// Nothing here asks a vendor or carries a token. An account is a
// subscription card as last read (provider.CachedCards, what
// /v1/magpie/quotas?kept=1 tells), for the subscriptions T3 Code reads:
// Codex ("codex"), Claude ("claude") and Grok ("xai"). An api-call is
// answered only for its account's own usage read (mgmtUsageURL), GET, as
// the vendor's reply would be made of that card's windows; any other
// URL or method, Codex's reset credits and their consume among them, is
// answered 403 in the call's status_code, and the header the caller sent
// (the hub's $TOKEN$ placeholder) is never read. Every other management
// route, the ones that change CLIProxyAPI's config or its accounts
// (reset-quota, auth-files' writes) and auth-files/models (T3 Code then
// counts an account for every model of its agent) included, is answered
// 404, or 405 for a known route by another method.
//
// It is magpie's quotas told another way, so it is admitted as they are,
// and more: another machine only while magpie is shared with it, and on
// this one only with a gateway key, so a browser page or an app the
// gateway lets in on loopback without a key (Origin null, an app's
// scheme, a *.localhost or rebound page) is answered 401. A key held to
// a budget, models or accounts is told what scopeQuotas lets it be.

const (
	mgmtAuthFilesPattern = "GET /v0/management/auth-files"
	mgmtAPICallPattern   = "POST /v0/management/api-call"
	mgmtPattern          = "/v0/management/"
)

// mgmtCards are the cards an account is read from; a test sets them.
var mgmtCards = provider.CachedCards

// mgmtKinds are the providers told, by magpie's id, as CLIProxyAPI names
// them in an auth file's provider.
var mgmtKinds = map[string]string{"codex": "codex", "claude": "claude", "grok": "xai"}

// mgmtUsageURL is each kind's usage read, the one URL its api-call is
// answered for (T3 Code's cliproxyApi.ts at 0c8e46b0).
var mgmtUsageURL = map[string]string{
	"codex":  "https://chatgpt.com/backend-api/wham/usage",
	"claude": "https://api.anthropic.com/api/oauth/usage",
	"xai":    "https://cli-chat-proxy.grok.com/v1/billing?format=credits",
}

// mgmtAccount is a card as an auth file: id and auth_index stand for the
// account, made of its provider, user and the magpie it is from, and
// tell nothing else of it.
type mgmtAccount struct {
	card      provider.SubscriptionQuota
	kind      string
	id, index string
}

func mgmtError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}

// mgmtAdmit admits a management request as the quotas are, with a key
// asked of this machine too, and gives its quota scope.
func mgmtAdmit(w http.ResponseWriter, r *http.Request) (*quotaScope, bool) {
	if !local(r) && !sharedWith(r) {
		mgmtError(w, http.StatusForbidden, "magpie's quotas are told to another machine only when magpie is shared on the local network and the request carries a gateway key")
		return nil, false
	}
	if access.Caller(r.Context()).KeyID == "" {
		mgmtError(w, http.StatusUnauthorized, "missing management key")
		return nil, false
	}
	return scopeQuotas(w, r)
}

func mgmtAccounts(scope *quotaScope, now time.Time) []mgmtAccount {
	out := []mgmtAccount{}
	seen := map[string]bool{}
	for _, q := range scope.cards(mgmtCards(now)) {
		kind, ok := mgmtKinds[q.Provider]
		if !ok || q.Kind == "plan" || q.Kind == "balance" {
			continue
		}
		key := q.Provider + "\x00" + strings.ToLower(strings.TrimSpace(q.User)) + "\x00" + q.From
		if seen[key] {
			continue
		}
		seen[key] = true
		sum := sha256.Sum256([]byte(key))
		index := hex.EncodeToString(sum[:8])
		out = append(out, mgmtAccount{card: q, kind: kind, id: kind + "-" + index, index: index})
	}
	return out
}

// mgmtAuthFiles lists the accounts as CLIProxyAPI's GET auth-files does.
func (s *Server) mgmtAuthFiles(w http.ResponseWriter, r *http.Request) {
	scope, ok := mgmtAdmit(w, r)
	if !ok {
		return
	}
	now := time.Now()
	files := []map[string]any{}
	for _, a := range mgmtAccounts(scope, now) {
		q := a.card
		label := q.Alias
		if label == "" {
			label = q.User
		}
		f := map[string]any{
			"id": a.id, "auth_index": a.index, "name": a.id,
			"type": a.kind, "provider": a.kind, "label": label,
			"status": "active", "status_message": "", "disabled": false, "unavailable": false,
			"runtime_only": true, "source": "memory", "size": 0,
		}
		if u := strings.TrimSpace(q.User); u != "" {
			f["email"] = u
		}
		if a.kind == "codex" && q.Plan != "" {
			f["id_token"] = map[string]any{"plan_type": q.Plan}
		}
		files = append(files, f)
	}
	writeJSON(w, http.StatusOK, map[string]any{"observed_at": now.UTC(), "files": files})
}

// mgmtCall is CLIProxyAPI's api-call request, its three spellings of
// auth_index included.
type mgmtCall struct {
	AuthIndexSnake  *string           `json:"auth_index"`
	AuthIndexCamel  *string           `json:"authIndex"`
	AuthIndexPascal *string           `json:"AuthIndex"`
	Method          string            `json:"method"`
	URL             string            `json:"url"`
	Header          map[string]string `json:"header"`
	Data            string            `json:"data"`
}

func (c mgmtCall) index() string {
	for _, p := range []*string{c.AuthIndexSnake, c.AuthIndexCamel, c.AuthIndexPascal} {
		if p != nil && strings.TrimSpace(*p) != "" {
			return strings.TrimSpace(*p)
		}
	}
	return ""
}

// mgmtAPICall answers an account's usage read from its card, as
// CLIProxyAPI's POST api-call answers the vendor's reply.
func (s *Server) mgmtAPICall(w http.ResponseWriter, r *http.Request) {
	scope, ok := mgmtAdmit(w, r)
	if !ok {
		return
	}
	var c mgmtCall
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&c); err != nil {
		mgmtError(w, http.StatusBadRequest, "invalid body")
		return
	}
	method := strings.ToUpper(strings.TrimSpace(c.Method))
	if method == "" {
		mgmtError(w, http.StatusBadRequest, "missing method")
		return
	}
	raw := strings.TrimSpace(c.URL)
	if raw == "" {
		mgmtError(w, http.StatusBadRequest, "missing url")
		return
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		mgmtError(w, http.StatusBadRequest, "invalid url")
		return
	}
	now := time.Now()
	var acct *mgmtAccount
	if idx := c.index(); idx != "" {
		for _, a := range mgmtAccounts(scope, now) {
			if a.index == idx {
				acct = &a
				break
			}
		}
	}
	if acct == nil {
		mgmtError(w, http.StatusBadRequest, "auth credential not found for auth_index")
		return
	}
	reply := func(status int, body any) {
		b, _ := json.Marshal(body)
		writeJSON(w, http.StatusOK, map[string]any{"status_code": status, "header": map[string][]string{"Content-Type": {"application/json"}}, "body": string(b)})
	}
	if method != http.MethodGet || !sameURL(u, mgmtUsageURL[acct.kind]) {
		reply(http.StatusForbidden, map[string]any{"error": "this hub answers only the account's usage read (GET " + mgmtUsageURL[acct.kind] + "), from what it last read; it sends nothing on"})
		return
	}
	body, read := mgmtUsage(acct.kind, acct.card, now)
	if !read {
		reply(http.StatusBadGateway, map[string]any{"error": "the account's usage hasn't been read"})
		return
	}
	reply(http.StatusOK, body)
}

// sameURL says u is want: the scheme, host, path and query alike.
func sameURL(u *url.URL, want string) bool {
	w, err := url.Parse(want)
	if err != nil || want == "" {
		return false
	}
	return strings.EqualFold(u.Scheme, w.Scheme) && strings.EqualFold(u.Host, w.Host) &&
		strings.TrimSuffix(u.Path, "/") == strings.TrimSuffix(w.Path, "/") && u.Query().Encode() == w.Query().Encode()
}

// mgmtNotFound answers every other management route: 405 for a known
// one by another method, else 404, as CLIProxyAPI does.
func (s *Server) mgmtNotFound(w http.ResponseWriter, r *http.Request) {
	allow := map[string]string{"/v0/management/auth-files": "GET", "/v0/management/api-call": "POST"}[strings.TrimSuffix(r.URL.Path, "/")]
	if allow != "" {
		w.Header().Set("Allow", allow)
		mgmtError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	mgmtError(w, http.StatusNotFound, "not found")
}

// mgmtWindows are the card's windows that are a share used of an
// allowance: not a text row, a lane, an aside or a wallet.
func mgmtWindows(q provider.SubscriptionQuota) []provider.QuotaWindow {
	var out []provider.QuotaWindow
	for _, w := range q.Windows {
		if w.Text || w.Lane != "" || w.Aside || w.Balance != nil {
			continue
		}
		out = append(out, w)
	}
	return out
}

// mgmtReset is when w resets, as quotaReport reckons it.
func mgmtReset(w provider.QuotaWindow, now time.Time) *time.Time {
	if w.ResetsAt != nil {
		return w.ResetsAt
	}
	if w.ResetSecs > 0 {
		t := now.Add(time.Duration(w.ResetSecs) * time.Second)
		return &t
	}
	return nil
}

func mgmtISO(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}

// mgmtUsage is the vendor's usage reply made of q, and whether q has
// anything read to make it of.
func mgmtUsage(kind string, q provider.SubscriptionQuota, now time.Time) (map[string]any, bool) {
	ws := mgmtWindows(q)
	if len(ws) == 0 && q.Error != "" {
		return nil, false
	}
	switch kind {
	case "codex":
		return codexMgmtUsage(q, ws, now), true
	case "claude":
		return claudeMgmtUsage(ws, now), true
	default:
		return grokMgmtUsage(ws, now), true
	}
}

// codexMgmtUsage is ChatGPT's wham/usage: the account's first two
// windows its primary and secondary, as Codex's reply orders them.
func codexMgmtUsage(q provider.SubscriptionQuota, ws []provider.QuotaWindow, now time.Time) map[string]any {
	var main []map[string]any
	reached := false
	for _, w := range ws {
		if w.Model != "" || len(main) == 2 {
			continue
		}
		cw := map[string]any{"used_percent": w.Used}
		if span := provider.WindowSpan(w); span > 0 {
			cw["limit_window_seconds"] = int64(span / time.Second)
		}
		if t := mgmtReset(w, now); t != nil {
			cw["reset_at"] = t.Unix()
			cw["reset_after_seconds"] = max(0, int64(t.Sub(now)/time.Second))
		}
		reached = reached || w.Used >= 100
		main = append(main, cw)
	}
	out := map[string]any{"rate_limit": nil}
	if q.Plan != "" {
		out["plan_type"] = q.Plan
	}
	if len(main) > 0 {
		rl := map[string]any{"allowed": !q.Held, "limit_reached": reached, "primary_window": main[0], "secondary_window": nil}
		if len(main) > 1 {
			rl["secondary_window"] = main[1]
		}
		out["rate_limit"] = rl
	}
	return out
}

// claudeMgmtUsage is Anthropic's api/oauth/usage: five_hour, seven_day,
// and each model's week as a weekly_scoped limit.
func claudeMgmtUsage(ws []provider.QuotaWindow, now time.Time) map[string]any {
	out := map[string]any{"five_hour": nil, "seven_day": nil, "extra_usage": nil}
	limits := []map[string]any{}
	for _, w := range ws {
		reset := mgmtISO(mgmtReset(w, now))
		if w.Model == "" {
			name := map[time.Duration]string{5 * time.Hour: "five_hour", 7 * 24 * time.Hour: "seven_day"}[provider.WindowSpan(w)]
			if name != "" && out[name] == nil {
				out[name] = map[string]any{"utilization": w.Used, "resets_at": reset}
			}
			continue
		}
		display := w.Model
		if _, after, ok := strings.Cut(w.Name, "·"); ok && strings.TrimSpace(after) != "" {
			display = strings.TrimSpace(after)
		}
		limits = append(limits, map[string]any{"kind": "weekly_scoped", "percent": w.Used, "resets_at": reset,
			"scope": map[string]any{"model": map[string]any{"display_name": display}}})
	}
	out["limits"] = limits
	return out
}

// grokMgmtUsage is Grok's v1/billing?format=credits: the allowance's
// share used and its period.
func grokMgmtUsage(ws []provider.QuotaWindow, now time.Time) map[string]any {
	cfg := map[string]any{}
	for _, w := range ws {
		if w.Model != "" {
			continue
		}
		cfg["creditUsagePercent"] = w.Used
		span := provider.WindowSpan(w)
		period := map[string]any{}
		switch {
		case span >= 28*24*time.Hour:
			period["type"] = "USAGE_PERIOD_TYPE_MONTHLY"
		case span == 7*24*time.Hour:
			period["type"] = "USAGE_PERIOD_TYPE_WEEKLY"
		case span == 24*time.Hour:
			period["type"] = "USAGE_PERIOD_TYPE_DAILY"
		}
		if end := mgmtReset(w, now); end != nil {
			period["end"] = mgmtISO(end)
			if span > 0 {
				start := end.Add(-span)
				if span >= 28*24*time.Hour {
					start = end.AddDate(0, -1, 0)
				}
				period["start"] = mgmtISO(&start)
			}
		}
		if len(period) > 0 {
			cfg["currentPeriod"] = period
		}
		break
	}
	return map[string]any{"config": cfg}
}
