package provider

// Alibaba Cloud's Token Plan (Bailian's, at token-plan.<region>.maas.
// aliyuncs.com, sk-sp- keys) has a monthly window and, on some plans, 5-hour
// and weekly ones, which Alibaba tells only to its console's gateway, never
// to the plan's inference key (the key gets BailianGateway.Login.NotLogined
// there, and 400 "model missing" on every path of the model host). The
// console takes a temporary token, which Alibaba's own client (modelstudioai
// /cli, `bl usage token-plan`) makes from the account's AccessKey ID and
// Secret:
//
//  1. POST modelstudio.<region>.aliyuncs.com/modelstudio/cli/
//     generateAccessToken, signed ACS3-HMAC-SHA256 (Action
//     GenerateCLIAccessToken, version 2026-02-10, no query, no body), gives
//     cliAccessToken;
//  2. POST <console>/cli/api.json?action=BroadScopeAspnGateway&product=
//     sfm_bailian&api=zeldaHttp.apikeyMgr./tokenplan/personal/api/v2/usage
//     with Bearer cliAccessToken and the form params={Api,V,Data}&region,
//     gives data.DataV2.data.data with per5HourPercentage, per1WeekPercentage
//     and per1MonthPercentage (each a share used, 0 to 1) and their
//     ResetTime in milliseconds.
//
// The gateway has no public documentation: the fields are read as the
// client does, from whichever per<N><Unit> pairs the reply has. Only the
// personal plan's API is known. The Qwen AI platform's plan is read the
// same way at its own console, with a sign-in to the platform instead of
// an access key (qianwen_plan.go); Qwen Cloud's sits behind a console that
// takes a browser sign-in, so it has no card (#5829).
//
// A RAM user's key is refused by the console (BailianGateway.Team.
// NotAuthorised) until the main account grants what Alibaba's Token Plan
// docs list for RAM users (aliRAMHint); the same API serves the main
// account and RAM users, the bl CLI has no other route for them.

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/lazyre"
)

const (
	aliACSAction  = "GenerateCLIAccessToken"
	aliACSVersion = "2026-02-10"
	aliACSPath    = "/modelstudio/cli/generateAccessToken"
	aliUsageAPI   = "zeldaHttp.apikeyMgr./tokenplan/personal/api/v2/usage"
	aliPlanName   = "Token Plan"
	aliKeyHint    = "check the AccessKey ID and Secret in the provider's editor (Alibaba Cloud console → AccessKey), and that the account may use Model Studio"
	// aliRAMHint is what Alibaba asks of a RAM user's key for the Token
	// Plan (Token Plan → Permissions and usage → RAM user authorization):
	// two system policies the Bailian and ModelStudio ones don't include,
	// and a permission given in Model Studio's own console. Without the
	// last the console answers BailianGateway.Team.NotAuthorised (#6506).
	aliRAMHint = "a RAM user's AccessKey needs, from the main account: the AliyunTokenPlanReadOnlyAccess and AliyunBSSReadOnlyAccess system policies in the RAM console (AliyunBailian* and AliyunModelStudio* policies don't cover the Token Plan), and the Administrator or Subscribe Plan permission for this RAM user on the Account Management page of the Model Studio (Bailian) console. Or use the main account's own AccessKey"
	// aliTokenTTL is how long a console token is kept before a new one is
	// made; one the console refuses is made again at once
	aliTokenTTL = 20 * time.Minute
)

// aliSite is where a Token Plan's region is asked: the host that makes
// the console token, the console gateway and its action.
type aliSite struct {
	region, mint, gateway, action string
}

var aliSites = map[string]aliSite{
	"cn-beijing":     {"cn-beijing", "modelstudio.cn-beijing.aliyuncs.com", "bailian-cs.console.aliyun.com", "BroadScopeAspnGateway"},
	"ap-southeast-1": {"ap-southeast-1", "modelstudio.ap-southeast-1.aliyuncs.com", "bailian-singapore-cs.alibabacloud.com", "IntlBroadScopeAspnGateway"},
}

// aliPlanSite is the region of the Alibaba Cloud Token Plan p's endpoint is
// on (token-plan.cn-beijing.maas.aliyuncs.com, token-plan.ap-southeast-1.
// maas.aliyuncs.com); ok false for another host, a plan of the Qwen AI
// platform or Qwen Cloud, or an account.
func aliPlanSite(p Provider) (aliSite, bool) {
	if p.Account != nil {
		return aliSite{}, false
	}
	for _, base := range []string{p.Chat, p.Anthropic, p.Responses} {
		if !strings.Contains(strings.ToLower(base), "maas.aliyuncs.com") {
			continue
		}
		h := hostOf(base)
		region, ok := strings.CutPrefix(h, "token-plan.")
		if !ok {
			continue
		}
		region, ok = strings.CutSuffix(region, ".maas.aliyuncs.com")
		if !ok {
			continue
		}
		if s, ok := aliSites[region]; ok {
			return s, true
		}
	}
	return aliSite{}, false
}

// TakesAlibabaAccessKey says p is on Alibaba Cloud's Token Plan, whose
// editor then asks for the account's AccessKey ID and Secret.
func TakesAlibabaAccessKey(p Provider) bool {
	_, ok := aliPlanSite(p)
	return ok
}

// aliPlanQuotas is a card for each Alibaba Cloud Token Plan region a
// provider in use is on and has the account's access key for, one for each
// access key and region, named for the first such provider; a region no
// provider on it has the access key for is a card that says so
// (accessKeyCard, #1821).
func aliPlanQuotas(ctx context.Context) []SubscriptionQuota {
	type job struct {
		p    Provider
		site aliSite
	}
	seen := map[string]bool{}
	var jobs, keyless []job
	keyed := map[string]bool{}
	for _, p := range planViews(All()) {
		if p.Hidden || p.Off {
			continue
		}
		site, ok := aliPlanSite(p)
		if !ok {
			continue
		}
		if p.AccessKeyID == "" || p.SecretAccessKey == "" {
			if AsksAccessKey(p) != "" {
				keyless = append(keyless, job{p, site})
			}
			continue
		}
		keyed[site.region] = true
		if seen[p.AccessKeyID+" "+site.region] || !wantsCard(ctx, p.ID, "") {
			continue
		}
		seen[p.AccessKeyID+" "+site.region] = true
		jobs = append(jobs, job{p, site})
	}
	out := make([]SubscriptionQuota, len(jobs))
	var wg sync.WaitGroup
	for i, j := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = aliPlanQuota(j.p.Via(ctx), j.p, j.site)
			if out[i].Error == "" {
				// what routing goes by, read just now (KeyAllowance)
				noteKeyAllowance(j.p, out[i].Windows, time.Now())
			}
			out[i] = keepReading(ctx, out[i], keyTag("alibaba "+j.site.region, j.p.AccessKeyID))
		}()
	}
	wg.Wait()
	for _, j := range keyless {
		if keyed[j.site.region] || seen["none "+j.site.region] || !wantsCard(ctx, j.p.ID, "") {
			continue
		}
		seen["none "+j.site.region] = true
		out = append(out, accessKeyCard(j.p, aliPlanName))
	}
	return out
}

// aliPlanQuota is p's plan as a card, read with its access key.
func aliPlanQuota(ctx context.Context, p Provider, site aliSite) SubscriptionQuota {
	q := SubscriptionQuota{Provider: p.ID, Name: p.Name, Icon: p.Icon, Plan: aliPlanName, Windows: []QuotaWindow{}}
	ws, err := aliReadUsage(ctx, p.AccessKeyID, p.SecretAccessKey, site, time.Now)
	if err != nil {
		q.Error = err.Error()
		return q
	}
	q.Windows = ws
	return q
}

type aliToken struct {
	token string
	at    time.Time
}

var (
	aliTokensMu sync.Mutex
	aliTokens   = map[string]aliToken{}
)

func aliTokenKey(ak string, site aliSite) string { return ak + " " + site.region }

// aliReadUsage reads the plan's windows: the console token kept for the
// access key, made again once when the console says it isn't logged in.
func aliReadUsage(ctx context.Context, ak, sk string, site aliSite, now func() time.Time) ([]QuotaWindow, error) {
	key := aliTokenKey(ak, site)
	for try := 0; ; try++ {
		aliTokensMu.Lock()
		t, ok := aliTokens[key]
		aliTokensMu.Unlock()
		if !ok || try > 0 || now().Sub(t.at) > aliTokenTTL {
			tok, err := aliMintToken(ctx, ak, sk, site, now())
			if err != nil {
				return nil, err
			}
			t = aliToken{tok, now()}
			aliTokensMu.Lock()
			aliTokens[key] = t
			aliTokensMu.Unlock()
		}
		raw, err := aliGateway(ctx, t.token, site)
		if err != nil {
			if try == 0 && ok && err == errAliNotLogged {
				continue
			}
			if err == errAliNotLogged {
				return nil, fmt.Errorf("Alibaba Cloud's console answered BailianGateway.Login.NotLogined for this access key; %s", aliKeyHint)
			}
			return nil, err
		}
		ws := aliReadWindows(raw)
		if len(ws) == 0 {
			return nil, fmt.Errorf("Alibaba Cloud's console told no window for this plan (it may be unlimited, or not a personal Token Plan): %s", volcHead(raw))
		}
		return ws, nil
	}
}

var errAliNotLogged = fmt.Errorf("not logged in")

// aliACSSign is the headers of an ACS3-HMAC-SHA256 request of the
// control plane's RPC action to host at path, with no query and body, at
// now with nonce: as Alibaba's own client (acs.ts signAcsRequest) signs it,
// the signed headers sorted, content-type among them.
func aliACSSign(ak, sk, host, path, action, version string, body []byte, now time.Time, nonce string) map[string]string {
	sum := sha256.Sum256(body)
	hashed := hex.EncodeToString(sum[:])
	h := map[string]string{
		"host":                  host,
		"x-acs-action":          action,
		"x-acs-version":         version,
		"x-acs-date":            now.UTC().Format("2006-01-02T15:04:05Z"),
		"x-acs-signature-nonce": nonce,
		"x-acs-content-sha256":  hashed,
		"content-type":          "application/json",
	}
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var ch strings.Builder
	for _, k := range keys {
		ch.WriteString(k + ":" + h[k] + "\n")
	}
	signed := strings.Join(keys, ";")
	canonical := strings.Join([]string{http.MethodPost, path, "", ch.String(), signed, hashed}, "\n")
	csum := sha256.Sum256([]byte(canonical))
	toSign := "ACS3-HMAC-SHA256\n" + hex.EncodeToString(csum[:])
	m := hmac.New(sha256.New, []byte(sk))
	m.Write([]byte(toSign))
	h["authorization"] = "ACS3-HMAC-SHA256 Credential=" + ak + ",SignedHeaders=" + signed + ",Signature=" + hex.EncodeToString(m.Sum(nil))
	return h
}

func aliNonce() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// aliMintToken makes the console token from the access key. The access
// key is sent nowhere but modelstudio.<region>.aliyuncs.com.
func aliMintToken(ctx context.Context, ak, sk string, site aliSite, now time.Time) (string, error) {
	h := aliACSSign(ak, sk, site.mint, aliACSPath, aliACSAction, aliACSVersion, nil, now, aliNonce())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+site.mint+aliACSPath, nil)
	if err != nil {
		return "", err
	}
	for k, v := range h {
		if k != "host" {
			req.Header.Set(k, v)
		}
	}
	req.Header.Set("Accept", "application/json")
	res, err := Keyed(http.DefaultClient).Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return "", err
	}
	var r struct {
		CliAccessToken string `json:"cliAccessToken"`
		Code           string `json:"Code"`
		Message        string `json:"Message"`
	}
	_ = json.Unmarshal(raw, &r)
	if tok := strings.TrimSpace(r.CliAccessToken); tok != "" && res.StatusCode < 300 {
		return tok, nil
	}
	switch {
	case r.Code != "" || r.Message != "":
		return "", fmt.Errorf("Alibaba Cloud %s: %s %s; %s", aliACSAction, r.Code, r.Message, aliKeyHint)
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		return "", fmt.Errorf("Alibaba Cloud %s: %s; %s", aliACSAction, res.Status, aliKeyHint)
	case res.StatusCode >= 300:
		return "", fmt.Errorf("Alibaba Cloud %s: %s: %s", aliACSAction, res.Status, volcHead(raw))
	}
	return "", fmt.Errorf("Alibaba Cloud %s gave no token: %s", aliACSAction, volcHead(raw))
}

// aliGateway asks the console's usage API with token and gives the
// business data of the reply (data.DataV2.data.data).
func aliGateway(ctx context.Context, token string, site aliSite) ([]byte, error) {
	u := "https://" + site.gateway + "/cli/api.json?action=" + site.action + "&product=sfm_bailian&api=" + url.QueryEscape(aliUsageAPI)
	return aliAskUsage(ctx, token, aliConsole{url: u, region: site.region, site: "BAILIAN_ALIYUN", who: "Alibaba Cloud's console", denied: aliRAMHint})
}

// aliConsole is a console gateway the Token Plan's usage API is asked
// at: Alibaba Cloud's (aliGateway) or the Qwen AI platform's
// (qianwen_plan.go), which answer alike.
type aliConsole struct {
	url, region string
	site        string // cornerstoneParam's consoleSite
	who         string // the console, as an error names it
	denied      string // what to do when the console says NotAuthorised
	ua          string // the User-Agent its own client sends, if any
}

// aliAskUsage posts the usage API's form to the console with token, and
// gives the reply's business data.
func aliAskUsage(ctx context.Context, token string, c aliConsole) ([]byte, error) {
	params, _ := json.Marshal(map[string]any{
		"Api": aliUsageAPI, "V": "1.0",
		"Data": map[string]any{"cornerstoneParam": map[string]any{
			"protocol": "V2", "console": "ONE_CONSOLE", "productCode": "p_efm", "switchUserType": 3, "consoleSite": c.site}},
	})
	form := url.Values{"params": {string(params)}, "region": {c.region}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader([]byte(form.Encode())))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "*/*")
	if c.ua != "" {
		req.Header.Set("User-Agent", c.ua)
	}
	res, err := Keyed(http.DefaultClient).Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 300 {
		return nil, fmt.Errorf("%s: %s: %s", c.who, res.Status, volcHead(raw))
	}
	var env struct {
		Data struct {
			Success   *bool           `json:"success"`
			ErrorCode json.RawMessage `json:"errorCode"`
			ErrorMsg  json.RawMessage `json:"errorMsg"`
			DataV2    json.RawMessage `json:"DataV2"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &env) != nil {
		return nil, fmt.Errorf("%s answered in a form magpie doesn't know: %s", c.who, volcHead(raw))
	}
	if env.Data.Success != nil && !*env.Data.Success && len(env.Data.ErrorCode) > 0 {
		code := strings.Trim(string(env.Data.ErrorCode), `"`)
		switch {
		case strings.Contains(code, "NotLogined"):
			return nil, errAliNotLogged
		case c.denied != "" && (strings.Contains(code, "NotAuthorised") || strings.Contains(code, "NotAuthorized")):
			return nil, fmt.Errorf("%s: %s: %s", c.who, code, c.denied)
		}
		if msg := strings.TrimSpace(strings.Trim(string(env.Data.ErrorMsg), `"`)); msg != "" && msg != "null" {
			return nil, fmt.Errorf("%s: %s: %s", c.who, code, msg)
		}
		return nil, fmt.Errorf("%s: %s", c.who, code)
	}
	// the reply holds it in two envelopes, one less in some replies
	var node json.RawMessage = env.Data.DataV2
	for i := 0; i < 2; i++ {
		var in struct {
			Data json.RawMessage `json:"data"`
		}
		if json.Unmarshal(node, &in) == nil && len(in.Data) > 0 && in.Data[0] == '{' {
			node = in.Data
		}
	}
	return node, nil
}

var aliWindowField = lazyre.New(`^per(\d+)(Hour|Day|Week|Month)(Percentage|ResetTime)$`)

// aliReadWindows reads the per<N><Unit>Percentage and ResetTime pairs of
// the usage data as windows, the shortest first. Percentage is the share
// used, 0 to 1; a window with no percentage is left out (it may be
// unlimited).
func aliReadWindows(raw []byte) []QuotaWindow {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	type win struct {
		n    int
		unit string
		used *float64
		at   any
	}
	wins := map[string]*win{}
	for k, v := range m {
		g := aliWindowField.FindStringSubmatch(k)
		if g == nil {
			continue
		}
		n, _ := strconv.Atoi(g[1])
		key := g[1] + g[2]
		w := wins[key]
		if w == nil {
			w = &win{n: n, unit: g[2]}
			wins[key] = w
		}
		if g[3] == "ResetTime" {
			w.at = v
		} else if f, ok := volcNum(v); ok {
			w.used = &f
		}
	}
	hours := map[string]time.Duration{"Hour": time.Hour, "Day": 24 * time.Hour, "Week": 7 * 24 * time.Hour, "Month": 30 * 24 * time.Hour}
	var all []*win
	for _, w := range wins {
		if w.used != nil {
			all = append(all, w)
		}
	}
	sort.Slice(all, func(i, j int) bool {
		return time.Duration(all[i].n)*hours[all[i].unit] < time.Duration(all[j].n)*hours[all[j].unit]
	})
	var out []QuotaWindow
	for _, w := range all {
		q := QuotaWindow{Used: *w.used * 100, Span: time.Duration(w.n) * hours[w.unit], ResetsAt: volcTime(w.at)}
		switch {
		case w.n == 5 && w.unit == "Hour":
			q.Name = "5 hours"
		case w.n == 1 && w.unit == "Week":
			q.Name = "Weekly"
		case w.n == 1 && w.unit == "Month":
			q.Name = "Month"
		case w.n == 1:
			q.Name = map[string]string{"Hour": "Hour", "Day": "Daily"}[w.unit]
		}
		if q.Name == "" {
			q.Name = fmt.Sprintf("%d %ss", w.n, strings.ToLower(w.unit))
		}
		out = append(out, q)
	}
	return out
}
