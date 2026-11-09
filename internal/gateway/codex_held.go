package gateway

import (
	"cmp"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// codexHolds are the ChatGPT accounts Codex's turns are relayed on as they
// came (codexUpstreamOn: Codex's own sign-in, or the account a compaction
// goes to) that ChatGPT refused with a 429 naming when they are back. The
// accounts loop rests and holds its own (timedHeld); a relayed turn has no
// candidate to rest, so it is held here: hsjzg58958 on X, an account
// limited for 300 seconds was asked again every time Codex tried again,
// till ChatGPT flagged it. Keyed by the account's user, lower case.
var codexHolds struct {
	sync.Mutex
	m map[string]codexHold
}

// codexHold is a refusal given again without asking until it ends.
type codexHold struct {
	until  time.Time
	status int
	why    string // failRate, failQuota or failCredit
	body   []byte // as ChatGPT said it, for Codex to show as it would
	ctype  string
	codex  http.Header // its x-codex-* windows, which Codex reads beside it
}

// codexHoldKey is who a turn relayed to base on on goes to, for its hold;
// "" for one not held: Codex's API key, which OpenAI's platform limits
// by its own headers Codex reads, or a sign-in magpie can't name.
func codexHoldKey(base string, on *provider.Provider) string {
	if base != provider.CodexBase {
		return ""
	}
	if on != nil {
		if on.Account == nil || on.LimitRestOff {
			return ""
		}
		return strings.ToLower(on.Account.User)
	}
	who, ok := provider.CodexSignedIn()
	if !ok {
		return ""
	}
	return strings.ToLower(who)
}

// noteCodexHold holds who after ChatGPT refused a turn with res's 429,
// when it named for how long (vendorSaidWhen), as rateRest reads it.
func noteCodexHold(who string, status int, header http.Header, body []byte) {
	if who == "" || status != http.StatusTooManyRequests || !vendorSaidWhen(header, body) {
		return
	}
	why := failure(status, body)
	if why != failRate && why != failQuota && why != failCredit {
		return
	}
	now := time.Now()
	d := retryAfter(header, now)
	d = max(d, min(resetsNoted(header, now), longestQuota), min(resetsAt(body, now), longestQuota))
	if d <= 0 {
		return
	}
	codexHolds.Lock()
	defer codexHolds.Unlock()
	if codexHolds.m == nil {
		codexHolds.m = map[string]codexHold{}
	}
	if h, ok := codexHolds.m[who]; ok && h.until.After(now.Add(d)) {
		return // a longer hold stands
	}
	windows := http.Header{}
	for k, vs := range header {
		if strings.HasPrefix(strings.ToLower(k), "x-codex-") {
			windows[k] = slices.Clone(vs)
		}
	}
	codexHolds.m[who] = codexHold{until: now.Add(d), status: status, why: why, body: append([]byte(nil), body...), ctype: header.Get("Content-Type"), codex: windows}
}

// codexHeld is who's hold, while it lasts.
func codexHeld(who string, now time.Time) (codexHold, bool) {
	if who == "" {
		return codexHold{}, false
	}
	codexHolds.Lock()
	defer codexHolds.Unlock()
	h, ok := codexHolds.m[who]
	if ok && !now.Before(h.until) {
		delete(codexHolds.m, who)
		ok = false
	}
	return h, ok
}

// liftCodexHold ends user's hold out of its allowance or credits: a reset
// spent for it, or its window started again (restarted). A rate limit's
// stands: a new window says nothing of it.
func liftCodexHold(user string) {
	codexHolds.Lock()
	defer codexHolds.Unlock()
	who := strings.ToLower(user)
	if h, ok := codexHolds.m[who]; ok && h.why != failRate {
		delete(codexHolds.m, who)
	}
}

// writeCodexHeld answers a turn on a held account as ChatGPT did, with
// when it is back as Retry-After, and says so in the request log.
func (s *Server) writeCodexHeld(w http.ResponseWriter, r *http.Request, h codexHold, base string, on *provider.Provider, model string, start time.Time) {
	wait := time.Until(h.until)
	for k, vs := range h.codex {
		w.Header()[k] = vs
	}
	w.Header().Set("Content-Type", cmp.Or(h.ctype, "application/json"))
	w.Header().Set("Retry-After", strconv.Itoa(int((wait+time.Second-1)/time.Second)))
	w.WriteHeader(h.status)
	w.Write(h.body)
	what := "rate limited"
	if h.why != failRate {
		what = "out of its allowance"
	}
	said := provider.APIError(h.body, http.StatusText(h.status)) + " (" + what + ": not asked again before " + heldClock(h.until, time.Now()) + ")"
	s.record(Call{Time: start, From: provider.Responses, To: provider.Responses, Model: model,
		Provider: "openai", Agent: callerOf(r).agent, Kind: callKind(r.Header), Status: h.status,
		Millis: time.Since(start).Milliseconds(), Error: codexWho(base, on) + ": " + said, headers: headerLogOf(r.Context())})
}
