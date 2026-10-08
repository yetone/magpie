package provider

// PLUGIN-SERVED (see AGENTS.md): ZCode ("zcode") is a deprecated built-in
// subscription served by its plugin, @magpie-community/opencode-zcode-auth,
// once moved onto it (provider.Moved; the default for a new sign-in). A
// moved one's sign-ins, models, requests and usage are all the plugin's,
// never this code's (only the move, in migrate*.go, still reads its
// accounts). A fix here alone doesn't reach those users; fix the plugin
// (github.com/magpie-community/plugins, packages/zcode) and raise the
// mover's min in internal/provider/migrate_zcode.go.

// ZCode's Start Plan (体验套餐) is the free allowance ZCode gives a Z.ai or
// BigModel account that has no GLM Coding Plan. It is not served where the
// Coding Plan is: ZCode sends its requests to zcode.z.ai itself,
// /api/v1/zcode-plan/anthropic, with its own session token (the
// credential store's zcodejwttoken, the `token` its sign-in's poll hands
// back) as the key, and reads what is left from
// /api/v1/zcode-plan/billing/balance with the same token. The account's
// zcode-api-key on api.z.ai/api/anthropic is billed to the Coding Plan, or
// else to the account's API balance, and refused with 1113 "Insufficient
// balance or no resource package" when there is neither.
//
// So an account goes to the Start Plan when it has ZCode's token and no
// Coding Plan (as /api/biz/subscription/list says), or has no Coding Plan
// key at all; to the Coding Plan otherwise, as before.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/netproxy"
)

// zcodeStartBase is where the Start Plan is served.
func zcodeStartBase() string { return zcodeAPI + "/api/v1/zcode-plan/anthropic" }

// zcodeStartModels are the Start Plan's models before ZCode's config is
// read: its builtinModelIds for account:zai-start-plan.
var zcodeStartModels = func() []catalog.Model {
	var out []catalog.Model
	for _, m := range zcodeModels {
		if m.ID != "GLM-5.3" {
			out = append(out, m)
		}
	}
	return out
}()

// zcodeStartNamed is whether a plan's name, as an account showed it, is
// ZCode's Start Plan (zcodeBalance.active's name for it).
func zcodeStartNamed(plan string) bool {
	p := strings.ToLower(plan)
	return strings.Contains(p, "start plan") || strings.Contains(p, "start-plan") || strings.Contains(plan, "体验")
}

// zcodeStartServes is the Start Plan's models, as ZCode's config on this
// machine lists them for account:zai-start-plan (zcodeStartModels without
// one): whether it serves model. GLM-5.3 is the Coding Plan's alone.
func zcodeStartServes() func(model string) bool {
	ms := zcodeStartModels
	if b, ok := zcodeLocalBuiltin(); ok {
		if l := b.models("account:zai-start-plan"); len(l) > 0 {
			ms = l
		}
	}
	return func(model string) bool {
		return slices.ContainsFunc(ms, func(m catalog.Model) bool { return strings.EqualFold(m.ID, model) })
	}
}

// ZCodeStartBlockedHint is what the Start Plan's "request has been blocked
// due to unusual activity" (HTTP 405, code 3012 "method not allowed")
// means (#425): zcode.z.ai looks at what a request carries and turns away
// one that isn't the ZCode app's own, its system prompt and all, as
// zcode2api found, so magpie sends it as the app does (zcode_client.go).
// One turned away still is Alibaba Cloud's firewall blocking the address
// (BlockedHint), or ZCode checking for something magpie doesn't send.
const ZCodeStartBlockedHint = "ZCode's Start Plan still turned this request away, though magpie sends it as the ZCode app does; it can be a network block of this IP, or ZCode checking for something new. Use an account with a GLM Coding Plan, or add another provider to this group"

// zcodeStartRefused matches that refusal: the block page, or its code.
var zcodeStartRefused = regexp.MustCompile(`(?i)unusual activity|"code"\s*:\s*"?3012\b`)

// zcodeStartExplain gives ZCodeStartBlockedHint for the Start Plan's block.
func zcodeStartExplain(status int, body []byte) string {
	if status >= 400 && (status == http.StatusMethodNotAllowed || EdgeBlocked(body) || zcodeStartRefused.Match(body)) {
		return ZCodeStartBlockedHint
	}
	return ""
}

// ---- which plan -----------------------------------------------------------------

var zcodeRoutes = struct {
	sync.Mutex
	m map[string]zcodeRoute
}{m: map[string]zcodeRoute{}}

type zcodeRoute struct {
	start bool
	at    time.Time
	ttl   time.Duration
}

// zcodeOnStart says whether k's requests go to the Start Plan, asking
// when the last answer is old; with ctx nil it only says what was found
// last (the Coding Plan when nothing was).
func zcodeOnStart(ctx context.Context, k zcodeKey) bool { return zcodeOnStartAs(ctx, k, "") }

// zcodeOnStartAs is zcodeOnStart for an account that showed plan: with
// ctx nil and nothing found yet, the plan's name says it. An account with
// a key and no Coding Plan (the key made, the plan not bought) is on the
// Start Plan, and was shown the Coding Plan's models, GLM-5.3 among them,
// until a request found where it goes.
func zcodeOnStartAs(ctx context.Context, k zcodeKey, plan string) bool {
	if k.JWT == "" || k.team() { // a team's seat is on the team's plan (zcode_team.go)
		return false
	}
	if k.Key == "" {
		return true
	}
	id := k.Key + "\x00" + k.JWT
	zcodeRoutes.Lock()
	r, ok := zcodeRoutes.m[id]
	zcodeRoutes.Unlock()
	if ctx == nil && !ok {
		return zcodeStartNamed(plan)
	}
	if ctx == nil || ok && time.Since(r.at) < r.ttl {
		return r.start
	}
	start, sure := zcodeDecide(ctx, k)
	ttl := 10 * time.Minute
	if !sure {
		ttl = time.Minute
	}
	zcodeRoutes.Lock()
	zcodeRoutes.m[id] = zcodeRoute{start: start, at: time.Now(), ttl: ttl}
	zcodeRoutes.Unlock()
	return start
}

// zcodeDecide asks whether the account has a Coding Plan: with one it is
// used; with none the Start Plan is all the account may have. When that
// can't be told, the Start Plan is used if it is there.
func zcodeDecide(ctx context.Context, k zcodeKey) (start, sure bool) {
	plan, err := zcodePlan(ctx, k)
	switch {
	case err == nil && plan != "":
		return false, true
	case err == nil:
		return true, true
	}
	if b, err := zcodeStartBalance(ctx, k.JWT); err == nil {
		if _, _, ok := b.active(); ok {
			return true, true
		}
	}
	return false, false
}

// zcodeRebase moves a request made for one plan's endpoint to another's.
func zcodeRebase(req *http.Request, from, to string) {
	if from == to || from == "" {
		return
	}
	rest, ok := strings.CutPrefix(req.URL.String(), from)
	if !ok {
		return
	}
	if u, err := url.Parse(to + rest); err == nil {
		req.URL, req.Host = u, u.Host
	}
}

// zcodeSourceHeaders are the headers the plugin the Start Plan serves sends
// with a model request (zcode_client.go): ZCode's app version and SDK,
// the CLI's title, its agent and release, the machine (X-Os-Version being
// "<platform> <release> <arch>", as the environment section has it), the
// language and time zone, a new request and trace id, and no query or
// session id, X-Device-Mid or anthropic-beta.
func zcodeSourceHeaders(req *http.Request) {
	platform := zcodePlatform()
	category := map[string]string{"darwin": "macos", "win32": "windows"}[platform]
	if category == "" {
		category = "linux"
	}
	req.Header.Del("anthropic-beta")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("User-Agent", "ZCode/"+zcodeAppVersion+" ai-sdk/anthropic/3.0.81")
	req.Header.Set("X-ZCode-App-Version", zcodeAppVersion)
	req.Header.Set("X-Title", "Z Code@cli")
	req.Header.Set("X-ZCode-Agent", "glm")
	req.Header.Set("HTTP-Referer", "https://zcode.z.ai") // as the plugin has it, wherever zcodeAPI points
	req.Header.Set("X-Platform", platform+"-"+zcodeArch())
	req.Header.Set("X-Os-Category", category)
	req.Header.Set("X-Os-Version", zcodeOSVersion())
	req.Header.Set("X-Release-Channel", "production")
	req.Header.Set("X-Client-Language", zcodeLanguage())
	req.Header.Set("X-Client-Timezone", zcodeTimezone())
	req.Header.Set("X-Request-Id", randomUUID())
	req.Header.Set("X-ZCode-Session-Type", "main")
	req.Header.Set("X-ZCode-Trace-Id", randomUUID())
}

// zcodeLanguage is the user's locale as Node's Intl gives it (zh-CN,
// en-US): the environment's, else the system's (zcodeSystemLocale), else
// en-US, ICU's own default.
func zcodeLanguage() string {
	for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if l := zcodeBCP47(os.Getenv(k)); l != "" {
			return l
		}
	}
	if l := zcodeBCP47(zcodeSystemLocale()); l != "" {
		return l
	}
	return "en-US"
}

// zcodeBCP47 turns a POSIX locale (zh_CN.UTF-8, en_US@euro) into a BCP 47
// tag (zh-CN), "" for none or C/POSIX.
func zcodeBCP47(v string) string {
	v, _, _ = strings.Cut(v, ".")
	v, _, _ = strings.Cut(v, "@")
	v = strings.TrimSpace(v)
	if v == "" || v == "C" || v == "POSIX" {
		return ""
	}
	return strings.ReplaceAll(v, "_", "-")
}

// zcodeTimezone is the IANA name of the local time zone, as Node's Intl
// gives it: TZ, the zone /etc/localtime links to, else one at the local
// offset (Asia/Shanghai at +8, Etc/GMT∓n otherwise, UTC at 0).
func zcodeTimezone() string {
	if tz := os.Getenv("TZ"); tz != "" && !strings.HasPrefix(tz, ":") {
		return tz
	}
	if l, err := os.Readlink("/etc/localtime"); err == nil {
		if _, name, ok := strings.Cut(l, "zoneinfo/"); ok {
			return name
		}
	}
	if name := time.Local.String(); name != "Local" && name != "" && strings.Contains(name, "/") {
		return name
	}
	_, off := time.Now().Zone()
	switch {
	case off == 8*3600:
		return "Asia/Shanghai"
	case off == 0:
		return "UTC"
	case off%3600 == 0 && off > 0:
		return "Etc/GMT-" + strconv.Itoa(off/3600)
	case off%3600 == 0:
		return "Etc/GMT+" + strconv.Itoa(-off/3600)
	}
	return "UTC"
}

// zcodeClock is the time a Start Plan request's context prefix gives the
// day of (zcodeDateReminder); tests set it.
var zcodeClock = time.Now

// zcodeStartRequest makes req, a request to the Start Plan, ZCode's own:
// its headers, and a model request's body shaped as ZCode's
// (zcodeStartBody) for an account on base.
func zcodeStartRequest(req *http.Request, base string, body []byte) {
	zcodeSourceHeaders(req)
	if len(body) == 0 || !strings.HasSuffix(req.URL.Path, "/v1/messages") {
		return
	}
	nb := zcodeStartBody(body, zcodeStartProvider(base), zcodeClock())
	req.Body = io.NopCloser(bytes.NewReader(nb))
	req.ContentLength = int64(len(nb))
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(nb)), nil }
}

// zcodeStartTransport carries the Start Plan's model requests over
// HTTP/1.1 only. The gateway's client speaks HTTP/2 wherever the server
// offers it, and zcode.z.ai does; every client the Start Plan is known to
// serve speaks HTTP/1.1 to it: ZCode itself (Node's fetch), an OpenCode
// plugin run in magpie's plugin host (Bun's fetch: ARNO's "Freeflow",
// provider zcode-start, served on the same machine and account where
// magpie's own request, its headers and body the same, was turned away
// with 405 / code 3012) and zcode2api-plus (Go, a transport of its own
// with HTTP/2 off). Otherwise as the gateway's: the proxy in force for
// the request (netproxy), and as long a wait for the answer's head.
var zcodeStartTransport = func() *http.Transport {
	var h1 http.Protocols
	h1.SetHTTP1(true)
	return &http.Transport{
		Proxy:                 netproxy.Func,
		ResponseHeaderTimeout: 10 * time.Minute,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       90 * time.Second,
		Protocols:             &h1,
	}
}()

var zcodeStartClient = &http.Client{Transport: netproxy.Dispatch(zcodeStartTransport)}

// zcodeStartClientFor is zcodeStartClient for a request to the Start
// Plan's endpoint (where sign put it), nil for any other.
func zcodeStartClientFor(req *http.Request) *http.Client {
	if strings.HasPrefix(req.URL.String(), zcodeStartBase()+"/") {
		return zcodeStartClient
	}
	return nil
}

// zcodeJWTExpired says whether ZCode's token has run out; ZCode then asks
// to sign in again, and so does magpie.
func zcodeJWTExpired(jwt string) bool {
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		return false
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return false
	}
	var claims struct {
		Exp float64 `json:"exp"`
	}
	if json.Unmarshal(b, &claims) != nil || claims.Exp == 0 {
		return false
	}
	return time.Now().After(time.Unix(int64(claims.Exp), 0))
}

var errZCodeExpired = errors.New("ZCode's sign-in has expired; sign in to ZCode again (or add the account again in magpie)")

// ---- the allowance ----------------------------------------------------------------

type zcodeBalance struct {
	ServerTime any `json:"server_time"`
	Plans      []struct {
		PlanID       string `json:"plan_id"`
		UserPlanID   string `json:"user_plan_id"`
		Name         string `json:"name"`
		Status       string `json:"status"`
		EndsAt       any    `json:"ends_at"`
		Entitlements []struct {
			ID     string `json:"entitlement_id"`
			Period string `json:"period"`
		} `json:"entitlements"`
	} `json:"plans"`
	Balances []struct {
		PlanID       string   `json:"plan_id"`
		UserPlanID   string   `json:"user_plan_id"`
		Entitlement  string   `json:"entitlement_id"`
		ShowName     string   `json:"show_name"`
		Capabilities []string `json:"capabilities"`
		Total        any      `json:"total_units"`
		Used         any      `json:"used_units"`
		Remaining    any      `json:"remaining_units"`
		ExpiresAt    any      `json:"expires_at"`
		PeriodStart  any      `json:"period_start"`
		PeriodEnd    any      `json:"period_end"`
	} `json:"balances"`
}

// zcodeNum reads a number the balance gives as a number or a string.
func zcodeNum(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil
	}
	return 0, false
}

// zcodeStartBalance is the Start Plan's balance, as ZCode reads it: a plan
// still "active" past its end is over, and the buckets of plans that are
// over are left out.
func zcodeStartBalance(ctx context.Context, jwt string) (zcodeBalance, error) {
	var b zcodeBalance
	if jwt == "" {
		return b, errors.New("not signed in to ZCode")
	}
	if zcodeJWTExpired(jwt) {
		return b, errZCodeExpired
	}
	u := zcodeAPI + "/api/v1/zcode-plan/billing/balance?" + url.Values{"app_version": {zcodeAppVersion}}.Encode()
	if err := zcodeCall(ctx, http.MethodGet, u, "Bearer "+jwt, nil, &b); err != nil {
		return b, err
	}
	now := float64(time.Now().Unix())
	if t, ok := zcodeNum(b.ServerTime); ok && t > 0 {
		now = t
	}
	over := map[string]bool{}
	for i, p := range b.Plans {
		if end, ok := zcodeNum(p.EndsAt); ok && end > 0 && end <= now && strings.EqualFold(strings.TrimSpace(p.Status), "active") {
			b.Plans[i].Status = "expired"
		}
		if strings.EqualFold(strings.TrimSpace(b.Plans[i].Status), "expired") {
			over[p.UserPlanID+"\x00"+p.PlanID] = true
		}
	}
	bs := b.Balances[:0]
	for _, x := range b.Balances {
		keep := true
		for _, p := range b.Plans {
			if x.UserPlanID != "" && p.UserPlanID != "" && x.UserPlanID == p.UserPlanID || (x.UserPlanID == "" || p.UserPlanID == "") && x.PlanID == p.PlanID {
				keep = !over[p.UserPlanID+"\x00"+p.PlanID]
				if keep {
					break
				}
			}
		}
		if keep {
			bs = append(bs, x)
		}
	}
	b.Balances = bs
	return b, nil
}

// active is the Start Plan the account has now, as ZCode tells it: an
// active plan whose id or name says start plan (or that has neither).
func (b zcodeBalance) active() (name string, until *time.Time, ok bool) {
	for _, p := range b.Plans {
		if !strings.EqualFold(strings.TrimSpace(p.Status), "active") {
			continue
		}
		id, n := strings.ToLower(strings.TrimSpace(p.PlanID)), strings.ToLower(strings.TrimSpace(p.Name))
		isStart := func(s string) bool { return strings.Contains(s, "start-plan") || strings.Contains(s, "start plan") }
		if id != "" || n != "" {
			if !isStart(id) && !isStart(n) {
				continue
			}
		}
		if end, ok := zcodeNum(p.EndsAt); ok && end > 0 {
			t := time.Unix(int64(end), 0)
			until = &t
		}
		return firstNonEmpty(p.Name, "Start Plan"), until, true
	}
	return "", nil, false
}

// zcodePeriod reads an entitlement's period: "daily", "weekly", "monthly".
func zcodePeriod(p string) time.Duration {
	p = strings.ToLower(p)
	switch {
	case strings.Contains(p, "day") || strings.Contains(p, "daily"):
		return 24 * time.Hour
	case strings.Contains(p, "week"):
		return 7 * 24 * time.Hour
	case strings.Contains(p, "month"):
		return 30 * 24 * time.Hour
	}
	return 0
}

// zcodeStartQuota is the Start Plan's allowance: a window for each of its
// buckets, a model's tokens for the day or for the plan's time, as ZCode
// shows them.
func zcodeStartQuota(ctx context.Context, l Login, jwt string) SubscriptionQuota {
	q := SubscriptionQuota{Provider: "zcode", Name: "ZCode", Icon: "zcode", Plan: l.Plan, User: l.User, Windows: []QuotaWindow{}}
	b, err := zcodeStartBalance(ctx, jwt)
	if err != nil {
		q.Error = err.Error()
		return q
	}
	name, until, ok := b.active()
	if !ok {
		q.Error = "this account has no GLM Coding Plan, and ZCode's Start Plan has ended or was never started"
		return q
	}
	q.Plan = name
	if until != nil {
		q.Until, q.Renew = until, "off"
	}
	var buckets []zcodeBucket
	for _, x := range b.Balances {
		total, hasTotal := zcodeNum(x.Total)
		used, hasUsed := zcodeNum(x.Used)
		left, hasLeft := zcodeNum(x.Remaining)
		if !hasTotal && !hasUsed && !hasLeft {
			continue
		}
		var models []string
		for _, c := range x.Capabilities {
			if m := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(c), "model:")); m != "" {
				models = append(models, m)
			}
		}
		w := QuotaWindow{Name: firstNonEmpty(x.ShowName, strings.Join(models, ", "), "Credits")}
		if !hasUsed && hasTotal && hasLeft {
			used = total - left
		}
		if hasTotal && total > 0 {
			w.Used = 100 * used / total
			w.Display = fmt.Sprintf("%s / %s", compactNumber(used), compactNumber(total))
		}
		if t, ok := zcodeNum(x.ExpiresAt); ok && t > 0 {
			r := time.Unix(int64(t), 0)
			w.ResetsAt = &r
		}
		for _, p := range b.Plans {
			if x.UserPlanID != "" && p.UserPlanID != "" && x.UserPlanID != p.UserPlanID || (x.UserPlanID == "" || p.UserPlanID == "") && x.PlanID != p.PlanID {
				continue
			}
			for _, e := range p.Entitlements {
				if e.ID == x.Entitlement {
					w.Span = zcodePeriod(e.Period)
				}
			}
		}
		if w.Span == 0 {
			s, ok1 := zcodeNum(x.PeriodStart)
			e, ok2 := zcodeNum(x.PeriodEnd)
			if ok1 && ok2 && e > s {
				w.Span = time.Duration(e-s) * time.Second
			}
		}
		q.Windows = append(q.Windows, w)
		buckets = append(buckets, zcodeBucket{models, hasLeft && left <= 0 || hasTotal && total > 0 && used >= total})
	}
	zcodeAlternatives(q.Windows, buckets)
	return q
}

// zcodeBucket is what zcodeAlternatives reads of a window's bucket.
type zcodeBucket struct {
	models []string
	spent  bool
}

// zcodeAlternatives scopes each bucket's window to the models a request
// spends it on. Two plans can give one model (ZCode Trust Build's and the
// Start Plan's GLM-5.3-Flash), and the server picks the bucket — ZCode
// sends no plan with a request — so a model counts on one bucket: a live
// one before a spent one, the least used of those. A bucket that is no
// model's stays on the card, Aside, so a spent one doesn't hold up the
// model, or the account, another still has (ganlerk on Discord).
func zcodeAlternatives(ws []QuotaWindow, bs []zcodeBucket) {
	best := map[string]int{}
	for i, b := range bs {
		for _, m := range b.models {
			k := strings.ToLower(m)
			j, ok := best[k]
			if !ok || bs[j].spent && !b.spent || bs[j].spent == b.spent && ws[i].Used < ws[j].Used {
				best[k] = i
			}
		}
	}
	for i, b := range bs {
		if len(b.models) == 0 {
			continue
		}
		var mine []string
		for _, m := range b.models {
			if best[strings.ToLower(m)] == i {
				mine = append(mine, m)
			}
		}
		if len(mine) == 0 {
			ws[i].Aside = true
			continue
		}
		ws[i].matches = func(model string) bool {
			for _, m := range mine {
				if strings.EqualFold(m, model) {
					return true
				}
			}
			return false
		}
	}
}
