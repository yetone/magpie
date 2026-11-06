package provider

// StepFun's Step Plan: its 5-hour and weekly windows and the month's
// credits are told only to a sign-in on StepFun's platform (the page at
// platform.stepfun.ai or .com), not to a key, which is refused there ("api
// key not permitted for this method"). The user signs in there in their
// own browser; the session's cookie is HttpOnly and the platform returns
// to no app, so a bookmarklet of magpie's, run on the signed-in page, asks
// the platform to renew it as the page does and copies what it gets, and
// the user pastes that into magpie (SaveStepFunPaste). From then on magpie
// renews the session and asks the platform itself, as the page does:
//
//	POST <platform>/passport/proto.api.passport.v1.PassportService/RefreshToken
//	POST <platform>/api/step.openapi.devcenter.Dashboard/QueryStepPlanRateLimit
//	POST <platform>/api/step.openapi.devcenter.Dashboard/GetStepPlanStatus
//
// each with the session as the one cookie Oasis-Token (the access token,
// three dots and the refresh token) and the page's own headers; the
// international site is app 20700, the Chinese one 10300. The session is
// kept in stepfun-auth.json beside providers.json, readable by the user
// alone, one per provider: two StepFun providers on one site can be two
// accounts, each with its own windows (#2384). The site, which platform
// is asked, is the provider's (StepFunSite), never the session's.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/lastgood"
)

// stepfunSites are StepFun's two platforms, by the API host a provider uses.
var stepfunSites = map[string]struct{ platform, app string }{
	"ai":  {"https://platform.stepfun.ai", "20700"},
	"com": {"https://platform.stepfun.com", "10300"},
}

// StepFunSite is which of StepFun's platforms the provider's key is for,
// "ai" or "com", or "" for another vendor.
func StepFunSite(p Provider) string {
	for _, base := range []string{p.Chat, p.Anthropic, p.Responses} {
		switch hostOf(base) {
		case "api.stepfun.ai":
			return "ai"
		case "api.stepfun.com":
			return "com"
		}
	}
	return ""
}

// StepFunSignInURL is the page the user signs in on, for the site.
func StepFunSignInURL(site string) string {
	s, ok := stepfunSites[site]
	if !ok {
		return ""
	}
	return s.platform + "/step-plan"
}

// stepfunPaste starts what the bookmarklet copies, the JSON of a session.
const stepfunPaste = "magpie-stepfun:"

// StepFunBookmarklet is the bookmarklet the user runs on a StepFun platform
// page they are signed in on: it renews the session as the page does, on
// either site, and copies it to be pasted into magpie.
func StepFunBookmarklet() string {
	sites := map[string][2]string{}
	for id, s := range stepfunSites {
		sites[s.platform] = [2]string{id, s.app}
	}
	cfg, _ := json.Marshal(sites)
	return `javascript:(async()=>{const s=` + string(cfg) + `[location.origin];` +
		`if(!s){alert("Open it on platform.stepfun.ai or platform.stepfun.com / 请在 StepFun 开放平台的页面上点它");return}` +
		`try{let w="";try{w=localStorage.getItem("web_id")||""}catch{}` +
		`const r=await fetch("/passport/proto.api.passport.v1.PassportService/RefreshToken",{method:"POST",credentials:"include",body:"{}",headers:{"content-type":"application/json","connect-protocol-version":"1","oasis-appid":s[1],"oasis-platform":"web","oasis-webid":w}});` +
		`const j=r.ok?await r.json():{},a=j.accessToken&&j.accessToken.raw,f=j.refreshToken&&j.refreshToken.raw;` +
		`const c=a?JSON.parse(atob(a.split(".")[1].replace(/-/g,"+").replace(/_/g,"/"))):{};` +
		`if(!c.activated||!f){alert("Sign in to StepFun first / 请先登录 StepFun");return}` +
		`const t="` + stepfunPaste + `"+JSON.stringify({site:s[0],access:a,refresh:f,webId:w});` +
		`try{await navigator.clipboard.writeText(t);alert("Copied: paste it into magpie / 已复制，回 magpie 粘贴")}` +
		`catch{prompt("Copy this and paste it into magpie / 复制这段，回 magpie 粘贴",t)}` +
		`}catch(e){alert(String(e))}})()`
}

// SaveStepFunPaste keeps the session the user pasted for the provider id,
// whose platform is site: what the bookmarklet copied, or the Oasis-Token
// cookie as the browser's developer tools show it (the access token, three
// dots, the refresh one).
func SaveStepFunPaste(ctx context.Context, id, site, text string) error {
	text = strings.TrimSpace(text)
	if body, ok := strings.CutPrefix(text, stepfunPaste); ok {
		var in struct{ Site, Access, Refresh, WebID string }
		if json.Unmarshal([]byte(body), &in) != nil {
			return errors.New("that isn't what the bookmarklet copied: copy it again")
		}
		if in.Site != site {
			return fmt.Errorf("that is a sign-in on %s, not %s", strings.TrimPrefix(StepFunOrigin(in.Site), "https://"), strings.TrimPrefix(StepFunOrigin(site), "https://"))
		}
		return SaveStepFunSession(ctx, id, site, in.Access, in.Refresh, in.WebID)
	}
	text, _, _ = strings.Cut(strings.TrimPrefix(text, "Oasis-Token="), ";")
	access, refresh, ok := strings.Cut(text, "...")
	if !ok || access == "" || refresh == "" {
		return errors.New("paste what the bookmarklet copied")
	}
	return SaveStepFunSession(ctx, id, site, access, refresh, "")
}

// StepFunOrigin is the site's platform.
func StepFunOrigin(site string) string { return stepfunSites[site].platform }

// stepfunSession is a sign-in on one of the platforms.
type stepfunSession struct {
	Access  string `json:"access"`
	Refresh string `json:"refresh"`
	WebID   string `json:"webId"`
	// Account is the platform's id for who signed in: a renewal that
	// comes back as someone else (StepFun hands out an anonymous token
	// once the refresh token is past its 30 days) is a session ended
	Account string `json:"account"`
}

var stepfunMu sync.Mutex

func stepfunPath() string { return filepath.Join(filepath.Dir(Path()), "stepfun-auth.json") }

// loadStepFun is every provider's session, by provider id; the caller
// holds stepfunMu. A file that doesn't parse is no session, and is left
// as it is (saveStepFun keeps it aside before a sign-in writes over it).
// One written before #2384 kept a session per site ("ai", "com"): it is
// given to the StepFun providers on that site and written back
// (stepfunBySite), so nobody signs in again for the upgrade.
func loadStepFun() map[string]stepfunSession {
	out := map[string]stepfunSession{}
	b, err := os.ReadFile(stepfunPath())
	if err != nil {
		return out
	}
	if json.Unmarshal(b, &out) != nil {
		return map[string]stepfunSession{}
	}
	if moved, ok := stepfunBySite(out); ok {
		_ = saveStepFun(moved) // not written, it is moved again on the next read
		return moved
	}
	return out
}

// stepfunOldSites are the keys stepfun-auth.json had before #2384, when
// a session was kept per site.
var stepfunOldSites = []string{"ai", "com"}

// stepfunBySite is all with each session kept under a site's name given to
// the StepFun providers on that site instead, and whether there was any.
// One provider on the site: it was clearly theirs. Several: each gets it,
// so none loses its windows, and the user brings another account's sign-in
// to the ones that are someone else's (those copies are one sign-in, and
// are renewed together until then: stepfunNow). A provider with a session
// of its own keeps it. With providers.json unreadable, or no StepFun
// provider on the site yet, the session waits where it is.
func stepfunBySite(all map[string]stepfunSession) (map[string]stepfunSession, bool) {
	var old []string
	for _, site := range stepfunOldSites {
		if _, ok := all[site]; ok {
			old = append(old, site)
		}
	}
	if len(old) == 0 {
		return nil, false
	}
	f, err := read()
	if err != nil {
		return nil, false
	}
	out := maps.Clone(all)
	moved := false
	for _, site := range old {
		var to []string
		for _, p := range f.Providers {
			if StepFunSite(normalize(p)) == site {
				to = append(to, p.ID)
			}
		}
		if len(to) == 0 {
			continue
		}
		for _, id := range to {
			if _, own := out[id]; !own {
				out[id] = all[site]
			}
		}
		// a StepFun provider whose id is the site's name has it already
		if !slices.Contains(to, site) {
			delete(out, site)
		}
		moved = true
	}
	return out, moved
}

// forgetStepFun drops the provider's session, when it is removed.
func forgetStepFun(id string) error { return SignOutStepFun(id) }

// renameStepFun moves the provider's session with it to its new id.
func renameStepFun(from, to string) error {
	stepfunMu.Lock()
	defer stepfunMu.Unlock()
	all := loadStepFun()
	s, ok := all[from]
	if !ok {
		return nil
	}
	delete(all, from)
	all[to] = s
	return saveStepFun(all)
}

func saveStepFun(all map[string]stepfunSession) error {
	defer filememo.Forget() // read again, where a request holds it
	b, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(stepfunPath()), 0o700); err != nil {
		return err
	}
	// loadStepFun reads a file that doesn't parse as no session: what it
	// held, every provider's sign-in, is kept aside before it is written over
	if err := lastgood.KeepUnparsed(stepfunPath(), lastgood.Into[map[string]stepfunSession]); err != nil {
		return err
	}
	return writeFileAtomic(stepfunPath(), b)
}

// stepfunClaims is what the access token says of itself.
type stepfunClaims struct {
	Activated bool  `json:"activated"`
	Exp       int64 `json:"exp"`
	OasisID   any   `json:"oasis_id"`
}

func readStepFunToken(raw string) (stepfunClaims, bool) {
	var c stepfunClaims
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return c, false
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil || json.Unmarshal(b, &c) != nil {
		return c, false
	}
	return c, true
}

func (c stepfunClaims) account() string {
	switch v := c.OasisID.(type) {
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case string:
		return v
	}
	return ""
}

// StepFunSignedIn reports whether magpie has a sign-in for the provider.
// StepFunSignedIn reads the file once until it changes: a provider list
// asks it for each StepFun provider on every poll, and each read was a
// file open, most often of a file that isn't there. One with a site's
// session from before #2384 is moved first, under the lock.
func StepFunSignedIn(id string) bool {
	all, err := filememo.Read("stepfun-auth", stepfunPath(), func(b []byte) (map[string]stepfunSession, error) {
		stepfunParses.Add(1)
		m := map[string]stepfunSession{}
		return m, json.Unmarshal(b, &m)
	})
	if err != nil {
		return false // no file, or one that doesn't parse: no session, as loadStepFun
	}
	if slices.ContainsFunc(stepfunOldSites, func(s string) bool { _, ok := all[s]; return ok }) {
		stepfunMu.Lock()
		defer stepfunMu.Unlock()
		return loadStepFun()[id].Access != ""
	}
	return all[id].Access != ""
}

// stepfunParses counts StepFunSignedIn's parses, for a test.
var stepfunParses atomic.Int64

// SaveStepFunSession keeps the session the sign-in page ended in, once
// the platform has taken it: a page that isn't signed in yet has an
// anonymous one, which is refused here. It is the provider id's alone; site
// is the provider's platform.
func SaveStepFunSession(ctx context.Context, id, site, access, refresh, webID string) error {
	if _, ok := stepfunSites[site]; !ok {
		return fmt.Errorf("no StepFun site %q", site)
	}
	c, ok := readStepFunToken(access)
	if !ok || !c.Activated || c.account() == "" {
		return errors.New("not signed in to StepFun yet")
	}
	var dev struct {
		DeviceID any `json:"device_id"`
	}
	if parts := strings.Split(refresh, "."); len(parts) == 3 {
		if b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "=")); err == nil {
			_ = json.Unmarshal(b, &dev)
		}
	}
	// the page's web id is the refresh token's device, which the
	// platform wants them to agree on
	if id := fmt.Sprint(dev.DeviceID); dev.DeviceID != nil && id != "" {
		webID = id
	}
	if _, ok := readStepFunToken(refresh); !ok || webID == "" {
		return errors.New("the sign-in gave no session to keep")
	}
	s := stepfunSession{Access: access, Refresh: refresh, WebID: webID, Account: c.account()}
	if err := stepfunCall(ctx, site, s, "/api/step.openapi.devcenter.Dashboard/QueryStepPlanRateLimit", nil); err != nil {
		return fmt.Errorf("StepFun didn't take the sign-in: %w", err)
	}
	stepfunMu.Lock()
	all := loadStepFun()
	all[id] = s
	err := saveStepFun(all)
	stepfunMu.Unlock()
	forgetPlanQuotas()
	return err
}

// SignOutStepFun forgets the provider's sign-in, and no other's.
func SignOutStepFun(id string) error {
	stepfunMu.Lock()
	all := loadStepFun()
	_, had := all[id]
	delete(all, id)
	var err error
	if had {
		err = saveStepFun(all)
	}
	stepfunMu.Unlock()
	forgetPlanQuotas()
	return err
}

func forgetPlanQuotas() {
	planQuotaCache.Lock()
	planQuotaCache.data = nil
	planQuotaCache.Unlock()
}

// errStepFunSession is a session the platform no longer takes.
var errStepFunSession = errors.New("signed out of StepFun: sign in again in the provider's settings")

// stepfunCall posts {} to the platform as the session, reading the reply
// into dst.
func stepfunCall(ctx context.Context, site string, s stepfunSession, path string, dst any) error {
	st := stepfunSites[site]
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, st.platform+path, strings.NewReader("{}"))
	if err != nil {
		return err
	}
	// the cookie is the session alone: another field beside it and the
	// platform reads the two tokens as one, and refuses it
	req.Header.Set("Cookie", "Oasis-Token="+s.Access+"..."+s.Refresh)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("Oasis-Appid", st.app)
	req.Header.Set("Oasis-Platform", "web")
	req.Header.Set("Oasis-Webid", s.WebID)
	res, err := Keyed(http.DefaultClient).Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode == http.StatusUnauthorized {
		return errStepFunSession
	}
	if res.StatusCode >= 300 {
		var e struct{ Message string }
		if json.Unmarshal(b, &e) == nil && e.Message != "" {
			return fmt.Errorf("%s: %s", res.Status, e.Message)
		}
		return errors.New(res.Status)
	}
	if dst == nil {
		return nil
	}
	return json.Unmarshal(b, dst)
}

// renewStepFun has the platform renew the session, as the page does every
// half hour (two hours abroad).
func renewStepFun(ctx context.Context, site string, s stepfunSession) (stepfunSession, error) {
	var out struct {
		AccessToken  struct{ Raw string } `json:"accessToken"`
		RefreshToken struct{ Raw string } `json:"refreshToken"`
	}
	if err := stepfunCall(ctx, site, s, "/passport/proto.api.passport.v1.PassportService/RefreshToken", &out); err != nil {
		return s, err
	}
	c, ok := readStepFunToken(out.AccessToken.Raw)
	if !ok || !c.Activated || c.account() != s.Account {
		return s, errStepFunSession
	}
	s.Access = out.AccessToken.Raw
	if out.RefreshToken.Raw != "" {
		s.Refresh = out.RefreshToken.Raw
	}
	return s, nil
}

// stepfunNow is the provider's session, renewed on its site first when it
// is about to run out, and kept. Another provider holding the same session
// (one sign-in the upgrade gave to every provider on its site, or the same
// one brought to two) is renewed with it: the platform may end the refresh
// token a renewal replaces, which would sign that one out.
func stepfunNow(ctx context.Context, id, site string, force bool) (stepfunSession, error) {
	stepfunMu.Lock()
	defer stepfunMu.Unlock()
	all := loadStepFun()
	s, ok := all[id]
	if !ok || s.Access == "" {
		return s, errStepFunSession
	}
	if c, _ := readStepFunToken(s.Access); !force && time.Until(time.Unix(c.Exp, 0)) > 5*time.Minute {
		return s, nil
	}
	n, err := renewStepFun(ctx, site, s)
	if err != nil {
		return s, err
	}
	for k, o := range all {
		if k == id || o.Refresh == s.Refresh && o.Account == s.Account {
			all[k] = n
		}
	}
	_ = saveStepFun(all)
	return n, nil
}

// stepfunAsk calls the site's platform as the provider's session, renewing
// it once when the platform says it has run out.
func stepfunAsk(ctx context.Context, id, site, path string, dst any) error {
	s, err := stepfunNow(ctx, id, site, false)
	if err != nil {
		return err
	}
	err = stepfunCall(ctx, site, s, path, dst)
	if !errors.Is(err, errStepFunSession) {
		return err
	}
	if s, err = stepfunNow(ctx, id, site, true); err != nil {
		return err
	}
	return stepfunCall(ctx, site, s, path, dst)
}

// stepfunRate reads a rate as it comes, a number or a string of one,
// a fraction of the whole (0.87) or, should it ever come so, a percent.
func stepfunRate(v any) (float64, bool) {
	f, ok := number(v)
	if !ok {
		return 0, false
	}
	if f > 1 {
		f /= 100
	}
	return max(0, min(1, f)), true
}

// stepfunTime reads a time given in seconds or milliseconds, zero being
// none.
func stepfunTime(v any) (time.Time, bool) {
	f, ok := number(v)
	if !ok || f <= 0 {
		return time.Time{}, false
	}
	if f > 1e12 {
		return time.UnixMilli(int64(f)), true
	}
	return time.Unix(int64(f), 0), true
}

// readStepPlan reads QueryStepPlanRateLimit:
//
//	{"five_hour_usage_left_rate":0.6,"five_hour_usage_reset_time":"1790680000",
//	 "weekly_usage_left_rate":0.9,"weekly_usage_reset_time":"1791200000",
//	 "plan_credit_rate_limit":{"subscription_credit_left_rate":0.87,
//	   "subscription_credit_reset_time":"1792000000","topup_credit_left_rate":0},…}
//
// what is left of each as a fraction. A plan without a window tells it
// as 0 left and 0 for its reset, which is no window rather than one used up.
// Credits that never reset (a monthly plan's, gone when the plan ends) tell
// their reset as 0 too, but come with credit_buckets: those are a window
// with no reset.
func readStepPlan(b []byte) ([]QuotaWindow, error) {
	var r struct {
		FiveLeft    any `json:"five_hour_usage_left_rate"`
		FiveReset   any `json:"five_hour_usage_reset_time"`
		WeekLeft    any `json:"weekly_usage_left_rate"`
		WeekReset   any `json:"weekly_usage_reset_time"`
		CreditLimit struct {
			Left    any               `json:"subscription_credit_left_rate"`
			Reset   any               `json:"subscription_credit_reset_time"`
			Buckets []json.RawMessage `json:"credit_buckets"`
		} `json:"plan_credit_rate_limit"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	var out []QuotaWindow
	add := func(name string, span time.Duration, left, reset any, resetless bool) {
		at, ok := stepfunTime(reset)
		if !ok && !resetless {
			return
		}
		l, lok := stepfunRate(left)
		if !lok {
			return
		}
		w := QuotaWindow{Name: name, Span: span, Used: (1 - l) * 100}
		if ok {
			w.ResetsAt = &at
		}
		out = append(out, w)
	}
	add("5 hours", 5*time.Hour, r.FiveLeft, r.FiveReset, false)
	add("7 days", 7*24*time.Hour, r.WeekLeft, r.WeekReset, false)
	add("Credits", 0, r.CreditLimit.Left, r.CreditLimit.Reset, len(r.CreditLimit.Buckets) > 0)
	return out, nil
}

// readStepPlanStatus reads GetStepPlanStatus's subscription: the plan's
// name, when its paid time ends and whether it renews then.
func readStepPlanStatus(b []byte) (plan string, until *time.Time, renew string) {
	var r struct {
		Subscription struct {
			Name      string `json:"name"`
			PlanType  any    `json:"plan_type"`
			ExpiredAt any    `json:"expired_at"`
			AutoRenew any    `json:"auto_renew"`
		} `json:"subscription"`
	}
	if json.Unmarshal(b, &r) != nil {
		return "", nil, ""
	}
	s := r.Subscription
	plan = s.Name
	if t, ok := stepfunTime(s.ExpiredAt); ok {
		until = &t
	}
	switch v := s.AutoRenew.(type) {
	case bool:
		renew = map[bool]string{true: "auto", false: "off"}[v]
	case string:
		if b, err := strconv.ParseBool(v); err == nil {
			renew = map[bool]string{true: "auto", false: "off"}[b]
		}
	}
	return plan, until, renew
}

// stepPlanQuota is the provider's Step Plan as a card, asked with its own
// sign-in.
func stepPlanQuota(ctx context.Context, p Provider) SubscriptionQuota {
	site := StepFunSite(p)
	q := SubscriptionQuota{Provider: p.ID, Name: p.Name, Icon: p.Icon, Windows: []QuotaWindow{}}
	var raw json.RawMessage
	if err := stepfunAsk(ctx, p.ID, site, "/api/step.openapi.devcenter.Dashboard/QueryStepPlanRateLimit", &raw); err != nil {
		q.Error = err.Error()
		return q
	}
	ws, err := readStepPlan(raw)
	if err != nil {
		q.Error = err.Error()
		return q
	}
	q.Windows = append(q.Windows, ws...)
	var status json.RawMessage
	if stepfunAsk(ctx, p.ID, site, "/api/step.openapi.devcenter.Dashboard/GetStepPlanStatus", &status) == nil {
		q.Plan, q.Until, q.Renew = readStepPlanStatus(status)
	}
	switch {
	case len(q.Windows) == 0 && q.Plan == "":
		q.Error = "this StepFun account has no Step Plan"
	case q.Plan == "":
		q.Plan = "Step Plan"
	}
	return q
}

// stepPlanQuotas is a card for each StepFun provider in use that magpie
// has a sign-in for: one per provider, not per site, as two on one site
// can be two accounts (#2384).
func stepPlanQuotas(ctx context.Context) []SubscriptionQuota {
	var jobs []Provider
	for _, p := range All() {
		if p.Hidden || p.Off {
			continue
		}
		if StepFunSite(p) == "" || !wantsCard(ctx, p.ID, "") || !StepFunSignedIn(p.ID) {
			continue
		}
		jobs = append(jobs, p)
	}
	out := make([]SubscriptionQuota, len(jobs))
	var wg sync.WaitGroup
	for i, p := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = stepPlanQuota(ctx, p)
			if out[i].Error == "" {
				// what routing goes by, read just now (KeyAllowance)
				noteKeyAllowance(p, out[i].Windows, time.Now())
			}
		}()
	}
	wg.Wait()
	return out
}
