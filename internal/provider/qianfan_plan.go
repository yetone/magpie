package provider

// Baidu Qianfan's Token Plan Personal (qianfan.baidubce.com/v2/tokenplan/
// personal, bce-v3 keys) tells what is used of its month only to a sign-in
// on Baidu Cloud's console, never to the plan's key: the console's
//
//	GET console.bce.baidu.com/api/qianfan/charge/tokenPlanPersonal/resource
//
// answers a request without the console's session with a 302 to
// login.bce.baidu.com, a Bearer key or not, and qianfan.baidubce.com has no
// usage route (#5860, yuushabu_buyinn on X). The console's own page asks it
// with the session's cookies and X-Requested-With, and reads
//
//	{"success":true,"result":{"items":[{"resourceId","planType":"pro",
//	 "resourceStatus":"Running","billingMode":"credit"|"token",
//	 "usedCredits","totalCredits","usedTokens","totalTokens",
//	 "expiresAt","autoRenew","apiKey"},…]}}
//
// (Baidu's console bundle, useRedeemCodeActivation and caller; the same
// fields as qnnp-me/dsh-qianfan-token-plan reads). An item Released, or
// past its expiresAt, is no plan. The item's apiKey is never kept.
//
// As StepFun's sign-in (stepfun_plan.go), the user brings the session from
// their own browser: a bookmarklet run on the signed-in console copies its
// cookies, or the Cookie header of the console's request is pasted from
// the browser's developer tools, where the HttpOnly ones are too. magpie
// asks the console with it before keeping it, and keeps it in
// qianfan-auth.json beside providers.json, readable by the user alone, one
// per provider: two can be two Baidu accounts. A cookie the console sets
// in a reply that read the plan is kept, as the browser keeps it, so a
// session Baidu renews as it is used stays the one magpie holds. When
// Baidu ends it the card says so, the sign-in is marked ended (Ended), and
// the provider's editor asks for a new one again; a later read the console
// takes clears the mark.
//
// There is no AccessKey/SecretKey way to this: Baidu's OpenAPI (the
// Qianfan API catalog, Finance's billing API, the Token Plan Enterprise's
// seat APIs with an API key) has no read for a Token Plan Personal.
//
// The Enterprise plan (tokenplan/team) is read on the console from its
// seats' admin pages (tokenPlan/seat/admin/overview), not this one; it
// isn't read here.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/lastgood"
	"github.com/yetone/magpie/internal/lazyre"
)

// qianfanConsole is Baidu Cloud's console, a var for tests.
var qianfanConsole = "https://console.bce.baidu.com"

const (
	qianfanResourcePath = "/api/qianfan/charge/tokenPlanPersonal/resource"
	qianfanPlanHost     = "qianfan.baidubce.com"
	// qianfanPaste starts what the bookmarklet copies, the JSON of a session
	qianfanPaste = "magpie-qianfan:"
	// qianfanUA is a browser's: the console is a page's API
	qianfanUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36"
)

// QianfanSignInURL is the console page the plan is on, where the user signs
// in and runs the bookmarklet.
const QianfanSignInURL = "https://console.bce.baidu.com/qianfan/resource/token-plan"

// QianfanPlan says p is on Baidu Qianfan's Token Plan Personal, whose usage
// magpie reads with a sign-in to Baidu Cloud's console.
func QianfanPlan(p Provider) bool {
	if p.Account != nil {
		return false
	}
	for _, base := range []string{p.Chat, p.Anthropic, p.Responses} {
		if hostOf(base) == qianfanPlanHost && strings.Contains(strings.ToLower(base), "/tokenplan/personal") {
			return true
		}
	}
	return false
}

// QianfanBookmarklet is the bookmarklet the user runs on the console while
// signed in: it asks the plan's page as the console does, so a page not
// signed in says so there, and copies the page's cookies to be pasted into
// magpie.
func QianfanBookmarklet() string {
	return `javascript:(async()=>{if(location.host!=="console.bce.baidu.com"){alert("Open it on console.bce.baidu.com / 请在百度智能云控制台的页面上点它");return}` +
		`try{const r=await fetch("` + qianfanResourcePath + `",{credentials:"include",redirect:"manual",headers:{"x-requested-with":"XMLHttpRequest"}});` +
		`const j=r.ok?await r.json():{};if(j.success!==true){alert("Sign in to the Baidu Cloud console first / 请先登录百度智能云控制台");return}` +
		`const t="` + qianfanPaste + `"+JSON.stringify({cookie:document.cookie});` +
		`try{await navigator.clipboard.writeText(t);alert("Copied: paste it into magpie / 已复制，回 magpie 粘贴")}` +
		`catch{prompt("Copy this and paste it into magpie / 复制这段，回 magpie 粘贴",t)}` +
		`}catch(e){alert(String(e))}})()`
}

// qianfanSession is a sign-in on the console: its cookies, as a request's
// Cookie header.
type qianfanSession struct {
	Cookie string `json:"cookie"`
	// Ended is when the console first sent this session to sign in again
	// (RFC 3339), "" while it takes it
	Ended string `json:"ended,omitempty"`
}

var qianfanMu sync.Mutex

func qianfanPath() string { return filepath.Join(filepath.Dir(Path()), "qianfan-auth.json") }

// loadQianfan is every provider's sign-in, by provider id; the caller holds
// qianfanMu. A file that doesn't parse is no sign-in, and is left as it is
// (saveQianfan keeps it aside before a sign-in writes over it).
func loadQianfan() map[string]qianfanSession {
	out := map[string]qianfanSession{}
	b, err := os.ReadFile(qianfanPath())
	if err != nil {
		return out
	}
	if json.Unmarshal(b, &out) != nil {
		return map[string]qianfanSession{}
	}
	return out
}

func saveQianfan(all map[string]qianfanSession) error {
	defer filememo.Forget() // read again, where a request holds it
	b, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(qianfanPath()), 0o700); err != nil {
		return err
	}
	if err := lastgood.KeepUnparsed(qianfanPath(), lastgood.Into[map[string]qianfanSession]); err != nil {
		return err
	}
	return writeFileAtomic(qianfanPath(), b)
}

// qianfanSignIn is the provider's sign-in, the file read once until it
// changes: a provider list asks it on every poll.
func qianfanSignIn(id string) (qianfanSession, bool) {
	all, err := filememo.Read("qianfan-auth", qianfanPath(), func(b []byte) (map[string]qianfanSession, error) {
		m := map[string]qianfanSession{}
		return m, json.Unmarshal(b, &m)
	})
	if err != nil {
		return qianfanSession{}, false
	}
	s, ok := all[id]
	return s, ok && s.Cookie != ""
}

// QianfanSignedIn reports whether magpie has a sign-in for the provider,
// ended or not.
func QianfanSignedIn(id string) bool {
	_, ok := qianfanSignIn(id)
	return ok
}

// QianfanEnded reports whether the console has ended the provider's
// sign-in, so the editor asks for a new one.
func QianfanEnded(id string) bool {
	s, ok := qianfanSignIn(id)
	return ok && s.Ended != ""
}

// noteQianfan records what a read of the console with the provider's
// cookie said: the session ended, or the cookies its reply set. Nothing is
// written when the sign-in has changed since (a new paste) or nothing
// changes.
func noteQianfan(id, used string, ended bool, set []*http.Cookie) {
	qianfanMu.Lock()
	defer qianfanMu.Unlock()
	all := loadQianfan()
	s, ok := all[id]
	if !ok || s.Cookie != used {
		return
	}
	n := s
	if ended {
		if n.Ended == "" {
			n.Ended = time.Now().UTC().Format(time.RFC3339)
		}
	} else {
		n.Ended = ""
		n.Cookie = mergeQianfanCookies(used, set)
	}
	if n == s {
		return
	}
	all[id] = n
	_ = saveQianfan(all)
}

// mergeQianfanCookies is the Cookie header with what a console reply set
// (for the resource's path) put in, as a browser would: a value replaced
// where it stood, a new one added, one deleted or expired taken out.
func mergeQianfanCookies(header string, set []*http.Cookie) string {
	type kv struct{ k, v string }
	var jar []kv
	for _, part := range strings.Split(header, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, _ := strings.Cut(part, "=")
		jar = append(jar, kv{k, v})
	}
	now := time.Now()
	for _, c := range set {
		if c.Name == "" || strings.ContainsAny(c.Name+c.Value, ";\r\n") ||
			c.Path != "" && !strings.HasPrefix(qianfanResourcePath, c.Path) {
			continue
		}
		gone := c.MaxAge < 0 || !c.Expires.IsZero() && !c.Expires.After(now)
		i := slices.IndexFunc(jar, func(e kv) bool { return e.k == c.Name })
		switch {
		case gone && i >= 0:
			jar = slices.Delete(jar, i, i+1)
		case gone:
		case i >= 0:
			jar[i].v = c.Value
		default:
			jar = append(jar, kv{c.Name, c.Value})
		}
	}
	out := make([]string, len(jar))
	for i, e := range jar {
		out[i] = e.k + "=" + e.v
	}
	return strings.Join(out, "; ")
}

// SignOutQianfan forgets the provider's sign-in, and no other's.
func SignOutQianfan(id string) error {
	qianfanMu.Lock()
	all := loadQianfan()
	_, had := all[id]
	delete(all, id)
	var err error
	if had {
		err = saveQianfan(all)
	}
	qianfanMu.Unlock()
	forgetPlanQuotas()
	return err
}

// forgetQianfan drops the provider's sign-in, when it is removed.
func forgetQianfan(id string) error { return SignOutQianfan(id) }

// renameQianfan moves the provider's sign-in with it to its new id.
func renameQianfan(from, to string) error {
	qianfanMu.Lock()
	defer qianfanMu.Unlock()
	all := loadQianfan()
	s, ok := all[from]
	if !ok {
		return nil
	}
	delete(all, from)
	all[to] = s
	return saveQianfan(all)
}

var (
	// a copied cURL command's cookies: -b '…' or -H 'Cookie: …'
	qianfanCurlB      = lazyre.New(`(?:^|\s)(?:-b|--cookie)\s+(?:'([^']*)'|"([^"]*)"|\$'([^']*)')`)
	qianfanCurlHeader = lazyre.New(`(?i)(?:-H|--header)\s+(?:'cookie:\s*([^']*)'|"cookie:\s*([^"]*)"|\$'cookie:\s*([^']*)')`)
)

// qianfanCookie is the Cookie header in what the user pasted: what the
// bookmarklet copied, the header as developer tools show it ("Cookie: …"
// or just its value), or a request copied as cURL. "" when it holds none.
func qianfanCookie(text string) (string, error) {
	text = strings.TrimSpace(text)
	if body, ok := strings.CutPrefix(text, qianfanPaste); ok {
		var in struct{ Cookie string }
		if json.Unmarshal([]byte(body), &in) != nil {
			return "", errors.New("that isn't what the bookmarklet copied: copy it again")
		}
		text = in.Cookie
	} else if strings.HasPrefix(text, "curl ") {
		var found string
		for _, re := range []*lazyre.RE{qianfanCurlHeader, qianfanCurlB} {
			if m := re.FindStringSubmatch(text); m != nil {
				found = m[1] + m[2] + m[3]
				break
			}
		}
		text = found
	} else if len(text) >= 7 && strings.EqualFold(text[:7], "cookie:") {
		text = text[7:]
	}
	text = strings.TrimSpace(text)
	if strings.ContainsAny(text, "\r\n") || !strings.Contains(text, "=") {
		return "", errors.New("paste what the bookmarklet copied, or the Cookie header of the console's request")
	}
	return text, nil
}

// SaveQianfanPaste keeps the console session the user pasted for the
// provider id, once the console has taken it.
func SaveQianfanPaste(ctx context.Context, id, text string) error {
	cookie, err := qianfanCookie(text)
	if err != nil {
		return err
	}
	_, set, err := qianfanResource(ctx, cookie)
	if err != nil {
		if errors.Is(err, errQianfanSession) {
			// the bookmarklet sees only the cookies a page may read
			return errors.New("Baidu's console didn't take that sign-in: copy the Cookie header of the console's tokenPlanPersonal/resource request from your browser's developer tools (Network) and paste it here")
		}
		return fmt.Errorf("Baidu's console didn't take the sign-in: %w", err)
	}
	qianfanMu.Lock()
	all := loadQianfan()
	all[id] = qianfanSession{Cookie: mergeQianfanCookies(cookie, set)}
	err = saveQianfan(all)
	qianfanMu.Unlock()
	forgetPlanQuotas()
	return err
}

// errQianfanSession is a session the console no longer takes.
var errQianfanSession = errors.New("signed out of the Baidu Cloud console: sign in again in the provider's settings")

// qianfanClient asks the console without following its redirect to the
// sign-in page, which is how it says the session has ended.
func qianfanClient() *http.Client {
	c := *http.DefaultClient
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &c
}

// qianfanResource asks the console for the account's Token Plan Personal
// as the session, and reads its result and the cookies a reply that read it
// set.
func qianfanResource(ctx context.Context, cookie string) (json.RawMessage, []*http.Cookie, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, qianfanConsole+qianfanResourcePath, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Accept", "application/json;charset=UTF-8")
	req.Header.Set("Referer", QianfanSignInURL)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("User-Agent", qianfanUA)
	res, err := qianfanClient().Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	switch {
	case res.StatusCode >= 300 && res.StatusCode < 400,
		res.StatusCode == http.StatusUnauthorized, res.StatusCode == http.StatusForbidden:
		return nil, nil, errQianfanSession
	case res.StatusCode >= 400:
		return nil, nil, fmt.Errorf("Baidu's console answered %s", res.Status)
	}
	var r struct {
		Success any             `json:"success"`
		Result  json.RawMessage `json:"result"`
		Message any             `json:"message"`
		Code    any             `json:"code"`
	}
	if json.Unmarshal(b, &r) != nil {
		// a sign-in page served as 200 is a session ended too
		if strings.Contains(strings.ToLower(string(b[:min(len(b), 2048)])), "<html") {
			return nil, nil, errQianfanSession
		}
		return nil, nil, fmt.Errorf("Baidu's console answered what isn't JSON: %s", volcHead(b))
	}
	if r.Success != true {
		msg := strings.TrimSpace(fmt.Sprint(r.Message))
		if m, ok := r.Message.(map[string]any); ok {
			msg = strings.TrimSpace(fmt.Sprint(m["global"]))
		}
		code := strings.ToLower(fmt.Sprint(r.Code))
		if strings.Contains(code, "login") || strings.Contains(code, "session") || strings.Contains(code, "unauth") {
			return nil, nil, errQianfanSession
		}
		if msg == "" || msg == "<nil>" {
			msg = volcHead(b)
		}
		return nil, nil, fmt.Errorf("Baidu's console refused: %s", msg)
	}
	return r.Result, res.Cookies(), nil
}

// qianfanItem is one plan the console lists, its apiKey left out.
type qianfanItem struct {
	PlanType     string `json:"planType"`
	Status       string `json:"resourceStatus"`
	BillingMode  string `json:"billingMode"`
	UsedCredits  any    `json:"usedCredits"`
	TotalCredits any    `json:"totalCredits"`
	UsedTokens   any    `json:"usedTokens"`
	TotalTokens  any    `json:"totalTokens"`
	ExpiresAt    any    `json:"expiresAt"`
	AutoRenew    any    `json:"autoRenew"`
}

// qianfanTime reads the console's time: RFC 3339, its "2006-01-02
// 15:04:05" in Beijing time, or seconds or milliseconds.
func qianfanTime(v any) (time.Time, bool) {
	if s, ok := v.(string); ok {
		s = strings.TrimSpace(s)
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t, true
		}
		for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02T15:04:05", "2006-01-02"} {
			if t, err := time.ParseInLocation(layout, s, time.FixedZone("CST", 8*3600)); err == nil {
				return t, true
			}
		}
	}
	return stepfunTime(v)
}

var qianfanPlanNames = map[string]string{"mini": "Mini", "lite": "Lite", "pro": "Pro", "max": "Max"}

// readQianfanPlan is the console's resource result as a card's plan, the
// time it runs until, whether it renews then, and its month's window. The
// plan is the one Running, the latest to end of several; one Released or
// past its end is none, as the console reads it. A reply with no plan, or
// none whose used and total amounts are told, is an error: unknown, never
// none left.
func readQianfanPlan(raw []byte, now time.Time) (plan string, until *time.Time, renew string, w QuotaWindow, err error) {
	var r struct {
		Items []qianfanItem `json:"items"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return "", nil, "", w, fmt.Errorf("Baidu's console answered a plan magpie can't read: %s", volcHead(raw))
	}
	type held struct {
		it  qianfanItem
		end time.Time
	}
	var live []held
	for _, it := range r.Items {
		end, ok := qianfanTime(it.ExpiresAt)
		if strings.EqualFold(it.Status, "Released") || ok && !end.After(now) {
			continue
		}
		live = append(live, held{it, end})
	}
	if len(live) == 0 {
		return "", nil, "", w, errors.New("this Baidu Cloud account has no Token Plan Personal")
	}
	slices.SortStableFunc(live, func(a, b held) int {
		ra, rb := strings.EqualFold(a.it.Status, "Running"), strings.EqualFold(b.it.Status, "Running")
		if ra != rb {
			if ra {
				return -1
			}
			return 1
		}
		return b.end.Compare(a.end)
	})
	it, end := live[0].it, live[0].end
	used, total, unit := it.UsedTokens, it.TotalTokens, "tokens"
	if strings.EqualFold(it.BillingMode, "credit") {
		used, total, unit = it.UsedCredits, it.TotalCredits, "credits"
	}
	u, uok := number(used)
	l, lok := number(total)
	if !uok || !lok || l <= 0 {
		return "", nil, "", w, errors.New("Baidu's console told no quota for this Token Plan")
	}
	w = QuotaWindow{Name: "Month", Span: 30 * 24 * time.Hour, Amount: u, Limit: l, Unit: unit,
		Used: max(0, min(100, u/l*100))}
	plan = "Token Plan"
	if n, ok := qianfanPlanNames[strings.ToLower(it.PlanType)]; ok {
		plan += " " + n
	}
	if !end.IsZero() {
		until = &end
		// a month's plan: its quota is the month's, and ends with it
		if end.Sub(now) <= 32*24*time.Hour {
			w.ResetsAt = &end
		}
	}
	switch v := it.AutoRenew.(type) {
	case bool:
		renew = map[bool]string{true: "auto", false: "off"}[v]
	case string:
		switch strings.ToLower(v) {
		case "true", "1":
			renew = "auto"
		case "false", "0":
			renew = "off"
		}
	}
	return plan, until, renew, w, nil
}

// qianfanNow is the clock readQianfanPlan is read at, a var for tests.
var qianfanNow = time.Now

// qianfanPlanQuota is the provider's Token Plan Personal as a card, read
// with its sign-in.
func qianfanPlanQuota(ctx context.Context, p Provider) SubscriptionQuota {
	q := SubscriptionQuota{Provider: p.ID, Name: p.Name, Icon: p.Icon, Plan: "Token Plan", Windows: []QuotaWindow{}}
	s, ok := qianfanSignIn(p.ID)
	if !ok {
		q.Error = errQianfanSession.Error()
		return q
	}
	raw, set, err := qianfanResource(ctx, s.Cookie)
	if errors.Is(err, errQianfanSession) {
		noteQianfan(p.ID, s.Cookie, true, nil)
	} else if err == nil && (len(set) > 0 || s.Ended != "") {
		noteQianfan(p.ID, s.Cookie, false, set)
	}
	if err != nil {
		q.Error = err.Error()
		return q
	}
	plan, until, renew, w, err := readQianfanPlan(raw, qianfanNow())
	if err != nil {
		q.Error = err.Error()
		return q
	}
	q.Plan, q.Until, q.Renew = plan, until, renew
	q.Windows = append(q.Windows, w)
	return q
}

// qianfanPlanQuotas is a card for each Token Plan Personal provider in use
// that magpie has a sign-in for: one per provider, as two can be two
// accounts.
func qianfanPlanQuotas(ctx context.Context) []SubscriptionQuota {
	var jobs []Provider
	for _, p := range All() {
		if p.Hidden || p.Off || !QianfanPlan(p) || !wantsCard(ctx, p.ID, "") || !QianfanSignedIn(p.ID) {
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
			out[i] = qianfanPlanQuota(p.Via(ctx), p)
			if out[i].Error == "" {
				// what routing goes by, read just now (KeyAllowance)
				noteKeyAllowance(p, out[i].Windows, time.Now())
			}
			out[i] = keepReading(ctx, out[i], keyTag("qianfan", p.ID))
		}()
	}
	wg.Wait()
	return out
}
