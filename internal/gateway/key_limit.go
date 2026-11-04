package gateway

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/budget"
	"github.com/yetone/magpie/internal/gwbudget"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// limitClock is the time a gateway key's limit is checked at: the window
// a request counts in and when its 429 says the key resets. A variable for
// the tests.
var limitClock = time.Now

// remoteKeyLimitHeader marks a 429 as the gateway key's own limit, not a
// provider's: a magpie that uses this one as a remote rests its key on it,
// and only its model on any other refusal (#2077).
const remoteKeyLimitHeader = "X-Magpie-Limit-Reset"

// keyLimited holds a gateway key's requests to its limit (#585, see
// package budget for the rules): one that would go over is refused with a
// 429 before any provider is asked, saying the key, the limit and when it
// resets; one let in holds a reservation until it ends. A key with a limit
// while plugins/budget is off is refused with a 503, and nothing is sent. Reads, the
// bridge's tool callbacks and counting tokens cost nothing and pass.
func keyLimited(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		who := access.Caller(r.Context())
		limits := who.Limits()
		if who.Issuer != nil {
			limits = append(slices.Clone(limits), who.Issuer.Limits()...)
		}
		if !who.Limited() || r.Method != http.MethodPost || strings.HasPrefix(r.URL.Path, "/_magpie/") || strings.HasPrefix(r.URL.Path, "/mcp/") || strings.HasSuffix(r.URL.Path, "/count_tokens") || r.URL.Path == KeysPath {
			next.ServeHTTP(w, r)
			return
		}
		size, model := r.ContentLength, ""
		// a compressed body (#1223) is reserved at its decoded size and model
		costCap := slices.ContainsFunc(limits, func(l *access.Limit) bool { return l.Cost > 0 })
		if enc := r.Header.Get("Content-Encoding"); costCap || size < 0 || (enc != "" && !strings.EqualFold(enc, "identity")) {
			// a turn on Codex's own path reads to its own limit (codexBackend)
			var most int64
			if strings.HasPrefix(r.URL.Path, CodexPath+"/") {
				most = codexBodyLimit()
			}
			b, status, err := readBoundedRequestBody(w, r, requestLimits{body: most})
			if err != nil {
				writeError(w, protoOfPath(r.URL.Path), status, err.Error())
				return
			}
			r.Body, r.ContentLength = io.NopCloser(bytes.NewReader(b)), int64(len(b))
			size, model = int64(len(b)), budget.ModelOf(b)
		}
		now := limitClock()
		release, err := reserveFor(who, size, model, now)
		if err != nil {
			status, errType := http.StatusServiceUnavailable, "gateway_key_limit_off"
			var refused gwbudget.Refusal
			if errors.As(err, &refused) {
				status, errType = http.StatusTooManyRequests, "gateway_key_limit"
				w.Header().Set("Retry-After", strconv.Itoa(refused.RetryAfter(now)))
				w.Header().Set(remoteKeyLimitHeader, refused.ResetAt().Format(time.RFC3339))
			}
			// the Anthropic and OpenAI SDKs would otherwise retry a 429 or 503
			w.Header().Set("X-Should-Retry", "false")
			writeError(w, protoOfPath(r.URL.Path), status, err.Error())
			c := callerOf(r)
			appendUsage(r, usage.Record{Time: now, Agent: c.agent, Via: c.via, Requested: model, Status: status, Rejected: true,
				Error: err.Error(), ErrType: errType, Endpoint: r.URL.Path, Session: sessionOf(r.Header), NativeSession: nativeSessionOf(r.Header)})
			return
		}
		defer release()
		next.ServeHTTP(w, r)
	})
}

// reserveFor holds a request of who's to its key's limits through the
// gateway's budget point (gwbudget): its release, or why it isn't let in,
// a gwbudget.Refusal or, for a key held to a limit while no limiter serves
// it, gwbudget.ErrUnlimited. A request without a limit is let in as it is.
func reserveFor(who access.Identity, size int64, model string, now time.Time) (release func(), err error) {
	l, err := gwbudget.Limiting(who)
	if err != nil {
		return nil, err
	}
	if l == nil {
		return func() {}, nil
	}
	release, refused := l.Reserve(who, size, model, now)
	if refused != nil {
		return nil, refused
	}
	return release, nil
}

// protoOfPath is the API a path is of, for an error said its way.
func protoOfPath(path string) provider.Protocol {
	switch {
	case strings.HasSuffix(path, "/messages"):
		return provider.Anthropic
	case strings.HasPrefix(path, "/v1beta/"):
		return provider.Gemini
	case strings.HasSuffix(path, "/responses"):
		return provider.Responses
	}
	return provider.Chat
}

// keyLimit answers GET /v1/magpie/limit: the calling gateway key's limit,
// what it has used and when it resets; {"limited":false} for a key with
// none or a request without one. "limits" is each of a key's limits
// (#2227), and "limit" the one holding it most (budget.Binding), which is
// what a magpie from before them reads.
//
// A key held to a budget, models or accounts is told so as well, so this
// agrees with the quotas and the status line (steven_4422: a key held to
// Claude's models read {"limited":false} here and 403 gateway_key_held
// there): "heldTo" what holds it ("budget", "models", "accounts"),
// "models" the models it may use, and "quotas" whether it is told any
// quota, with "quotaProviders" the providers it is told those of
// (scopeQuotas). An account is not named: a key its owner tells no quota
// is told no account either. A key held to nothing has none of these.
func (s *Server) keyLimit(w http.ResponseWriter, r *http.Request) {
	who := access.Caller(r.Context())
	out := map[string]any{"limited": false, "key": who.KeyName}
	if heldKey(who) {
		var heldTo []string
		if who.Limited() {
			heldTo = append(heldTo, "budget")
		}
		if len(who.Models) > 0 && !who.AccountModels {
			heldTo = append(heldTo, "models")
		}
		if len(who.Accounts) > 0 {
			heldTo = append(heldTo, "accounts")
		}
		out["heldTo"], out["quotas"] = heldTo, who.Quotas
		if len(who.Models) > 0 {
			out["models"] = who.Models
		}
		if who.Quotas {
			ps := []string{}
			for id, every := range newQuotaScope(who, provider.Catalog()).every {
				if every {
					ps = append(ps, id)
				}
			}
			slices.Sort(ps)
			out["quotaProviders"] = ps
		}
	}
	// an issued key is held to the limits of the key it was issued under
	// too, which count every key issued under it: "issuerLimits"
	if who.KeyID != "" && who.Issuer != nil && len(who.Issuer.Limits()) > 0 {
		out["issuerLimits"] = budget.All(access.Key{ID: who.Issuer.KeyID, Name: who.Issuer.KeyName, Limit: who.Issuer.Limit, More: who.Issuer.More}, limitClock())
	}
	if !who.Expires.IsZero() {
		out["expires"] = who.Expires
	}
	if who.KeyID == "" || len(who.Limits()) == 0 {
		writeJSON(w, 200, out)
		return
	}
	all := budget.All(access.Key{ID: who.KeyID, Name: who.KeyName, Limit: who.Limit, More: who.More}, limitClock())
	out["limited"], out["limit"], out["limits"] = true, budget.Binding(all), all
	writeJSON(w, 200, out)
}
