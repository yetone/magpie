package provider

// What is left on an API key, as the vendor's own balance endpoint tells
// it: DeepSeek, Kimi, OpenRouter, StepFun, Command Code, Ollama Cloud,
// AiHubMix and Zhipu/Z.ai pay-as-you-go keys are known by their hosts
// (AiHubMix tells the whole account's to its access token, BalanceToken;
// SiliconFlow's query is gone, siliconFlowRetired);
// any other provider can name an endpoint and where the amount sits in its
// reply (BalanceURL, BalancePath), the way a relay's own usage query does,
// with {key} where it wants the key in the URL (withBalanceKey).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// balanceSource is where one provider's balance is asked and how the
// reply reads.
type balanceSource struct {
	url  string
	read func(body []byte) (string, error)
	// token, when set, is sent as the whole Authorization header in place
	// of the key's: the balance is the account's, not a key's
	token string
	// failed, when set, words a refusal the vendor is known to give in
	// place of the status and body as they came (nil when it isn't one)
	failed func(status int, body []byte) error
	// split, when set, reads what the balance is made of (granted and
	// topped up), told under it on the card
	split func(body []byte) []BalancePart
}

// balanceSourceOf is the provider's own endpoint when it named one, else
// the one its host is known to have.
func balanceSourceOf(p Provider) (balanceSource, bool) {
	p = keyQueryOf(p)
	if p.BalanceURL != "" {
		path := balancePathOf(p)
		// a token saved beside it (a new-api relay's access token, its
		// /api/user/self telling the account's quota) is asked with
		// instead of the key
		return balanceSource{p.BalanceURL, func(b []byte) (string, error) { return readBalancePath(b, path) }, p.BalanceToken, nil, nil}, true
	}
	if src, ok := zhipuBalanceSource(p); ok {
		return src, true
	}
	hosts := []string{hostOf(p.Chat), hostOf(p.Responses), hostOf(p.Anthropic)}
	for _, h := range hosts {
		switch h {
		case "api.deepseek.com":
			return balanceSource{"https://api.deepseek.com/user/balance", readDeepSeek, "", nil, readDeepSeekSplit}, true
		case "api.moonshot.cn":
			return balanceSource{"https://api.moonshot.cn/v1/users/me/balance", readMoonshot("¥"), "", nil, nil}, true
		case "api.moonshot.ai":
			return balanceSource{"https://api.moonshot.ai/v1/users/me/balance", readMoonshot("$"), "", nil, nil}, true
		case "openrouter.ai":
			return balanceSource{"https://openrouter.ai/api/v1/credits", readOpenRouter, "", nil, nil}, true
		case "api.siliconflow.cn":
			return balanceSource{"https://api.siliconflow.cn/v1/user/info", readSiliconFlow("¥"), "", siliconFlowGone("https://cloud.siliconflow.cn", " (余额充值 → 代金券)"), nil}, true
		case "api.siliconflow.com":
			return balanceSource{"https://api.siliconflow.com/v1/user/info", readSiliconFlow("$"), "", siliconFlowGone("https://cloud.siliconflow.com", ""), nil}, true
		case "api.stepfun.com":
			return balanceSource{"https://api.stepfun.com/v1/accounts", readStepFun("¥"), "", nil, nil}, true
		case "api.stepfun.ai":
			return balanceSource{"https://api.stepfun.ai/v1/accounts", readStepFun("$"), "", nil, nil}, true
		case "api.commandcode.ai":
			return balanceSource{"https://api.commandcode.ai/alpha/billing/credits", readCommandCode, "", nil, nil}, true
		case "api-inference.modelscope.cn":
			return balanceSource{modelScopeBalanceURL, readModelScope, "", nil, nil}, true
		case "ollama.com":
			// the credits bought past the plan's allowance (#1888)
			return balanceSource{ollamaBalanceURL, readOllamaCredits, "", nil, nil}, true
		case "aihubmix.com":
			if p.BalanceToken != "" {
				return balanceSource{"https://aihubmix.com/api/user/self", readAiHubMixAccount, p.BalanceToken, nil, nil}, true
			}
			return balanceSource{aiHubMixKeyRemain, readAiHubMix, "", nil, nil}, true
		}
	}
	return balanceSource{}, false
}

// aiHubMixKeyRemain is AiHubMix's query for what is left on a key
const aiHubMixKeyRemain = "https://aihubmix.com/dashboard/billing/remain"

// balancePathOf is the balance field of a provider that named its Balance
// URL.
func balancePathOf(p Provider) string {
	p = keyQueryOf(p)
	if strings.TrimSpace(p.BalancePath) != "" {
		return p.BalancePath
	}
	// a query whose reply is known, with its field left out (#881)
	switch path := balanceURLPath(p.BalanceURL); {
	case path == newAPIUserSelf:
		// new-api's account query: the quota, in new-api's units, as it
		// reports it
		return newAPIQuotaPath
	case path == newAPIKeyUsage:
		// new-api's (and one-api's) query for a key: what is left on it,
		// in the same units
		return newAPIKeyPath
	case path == sub2APIProfile:
		// a sub2api panel's profile, asked with its login JWT: dollars
		return "$data.balance"
	case path == sub2APIKeyUsage:
		// a sub2api panel's query for a key, asked with the key alone:
		// what is left, in dollars, whether the wallet's, the key's own
		// quota or its plan's
		return "$remaining"
	case strings.HasSuffix(path, creditGrants):
		// OpenAI's old credit query, which relays still answer: dollars
		return "$total_available"
	}
	return p.BalancePath
}

// new-api's two balance queries: /api/usage/token tells a key what is left
// on it and is asked with the key alone (its token check takes no access
// token), /api/user/self tells the account's quota, $1 to 500000 of it, to
// the account's access token with the user's id in New-Api-User.
const (
	newAPIKeyUsage  = "/api/usage/token"
	newAPIUserSelf  = "/api/user/self"
	newAPIQuotaPath = "$data.quota / 500000"
	newAPIKeyPath   = "$data.total_available / 500000"
	// creditGrants is OpenAI's old query for what is left on an account,
	// {"total_granted":…,"total_used":…,"total_available":…} in dollars
	creditGrants = "/dashboard/billing/credit_grants"
	// sub2APIProfile is a sub2api panel's account, told to its login JWT
	sub2APIProfile = "/api/v1/user/profile"
	// sub2APIKeyUsage is a sub2api panel's usage query for a key, which
	// tells what is left on it (remaining, USD) without a login
	sub2APIKeyUsage = "/v1/usage"
)

// tokenQueries are the account queries that only a balance token can
// ask, each with the same panel's query for a key and the field magpie
// reads from the account's reply when none is given.
var tokenQueries = []struct{ account, key, field string }{
	{sub2APIProfile, sub2APIKeyUsage, "$data.balance"},
	{newAPIUserSelf, newAPIKeyUsage, newAPIQuotaPath},
}

// keyQueryOf is p asking its panel's query for a key when its Balance URL
// is an account query left without the token it needs: the token removed
// after the editor's "Use …/api/v1/user/profile" (lx_78654 on Discord),
// where the key could only be refused and the card said "Allowance
// unavailable" for good. The same panel's key query is asked with the key
// instead, its field read as that query's unless the user wrote one of
// their own. The saved URL is left as the user set it; a token pasted
// again asks the account query again. One with an Authorization or Cookie
// header of the user's own still asks it as they set it.
func keyQueryOf(p Provider) Provider {
	p = keyNotToken(p)
	if p.BalanceURL == "" || strings.TrimSpace(p.BalanceToken) != "" {
		return p
	}
	for k, v := range p.Headers {
		if strings.TrimSpace(v) != "" && (strings.EqualFold(k, "Authorization") || strings.EqualFold(k, "Cookie")) {
			return p
		}
	}
	u, err := url.Parse(strings.TrimSpace(p.BalanceURL))
	if err != nil || u.Host == "" {
		return p
	}
	path := strings.TrimRight(u.Path, "/")
	for _, q := range tokenQueries {
		base, ok := strings.CutSuffix(path, q.account)
		if !ok {
			continue
		}
		u.Path, u.RawPath, u.RawQuery, u.Fragment = base+q.key, "", "", ""
		p.BalanceURL = u.String()
		if f := strings.TrimSpace(p.BalancePath); f == q.field {
			p.BalancePath = ""
		}
		return p
	}
	return p
}

// BalanceQuery is the Balance URL and field the provider's balance is
// asked at, which differ from the saved ones for an account query left
// without its token (keyQueryOf).
func BalanceQuery(p Provider) (balanceURL, field string) {
	p = keyQueryOf(p)
	if u := probedBalanceURL(p); u != "" {
		// a relay added by hand, its query found (balance_probe.go)
		p.BalanceURL = u
		return u, balancePathOf(p)
	}
	return p.BalanceURL, p.BalancePath
}

// balanceURLPath is a balance URL's path, without a slash at its end.
func balanceURLPath(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return strings.TrimRight(u.Path, "/")
}

// errKeyUsageWithToken is a balance token beside new-api's query for a key,
// which never takes it: asked, it could only be refused, so what to name in
// its place is said instead.
func errKeyUsageWithToken(raw string) error {
	self := newAPIUserSelf
	if u, err := url.Parse(strings.TrimSpace(raw)); err == nil && u.Host != "" {
		self = u.Scheme + "://" + u.Host + newAPIUserSelf
	}
	return errKeyQueryWithToken(raw, self, newAPIQuotaPath, "access token")
}

// errSub2APIKeyUsageWithToken is a login token beside sub2api's query for a
// key, which takes the key alone as new-api's does: the panel's query for
// the account is named in its place.
func errSub2APIKeyUsageWithToken(raw string) error {
	profile := sub2APIProfile
	if u, err := url.Parse(strings.TrimSpace(raw)); err == nil && u.Host != "" {
		profile = u.Scheme + "://" + u.Host + sub2APIProfile
	}
	return errKeyQueryWithToken(raw, profile, "$data.balance", "login token")
}

// errKeyQueryWithToken is a balance token beside a panel's query for a key
// (new-api's /api/usage/token, sub2api's /v1/usage), which never takes it:
// what to name in its place is said.
func errKeyQueryWithToken(raw, accountURL, accountField, token string) error {
	return fmt.Errorf("the Balance URL %s takes the API key, not the %s: set it to %s (Balance field %s) for the account's balance, or remove the token for the key's own", raw, token, accountURL, accountField)
}

// balanceRefusal is what a reply of {"success":false,"message":…} says, a
// new-api relay's way of turning a request down, with a 401 and a 200
// alike; a reply without the two is not one.
func balanceRefusal(b []byte) (string, bool) {
	var r struct {
		Success *bool  `json:"success"`
		Message string `json:"message"`
	}
	if json.Unmarshal(b, &r) != nil || r.Success == nil || *r.Success || strings.TrimSpace(r.Message) == "" {
		return "", false
	}
	return strings.TrimSpace(r.Message), true
}

// newAPIUserHint puts what to do before a new-api refusal about the
// New-Api-User header, which its /api/user/self wants beside the access
// token ("无权进行此操作，未提供 New-Api-User", "Unauthorized, New-Api-User
// header not provided", or its format error or mismatch); any other
// message is left as it came.
func newAPIUserHint(msg string) string {
	if !strings.Contains(strings.ToLower(msg), "new-api-user") {
		return msg
	}
	return "fill in your user ID (shown in the site's personal settings) under this provider's Account balance, beside its access token; the relay said: " + msg
}

// TakesBalanceToken says the provider's vendor tells the account's balance
// to a token of its own (BalanceToken), which the editor then asks for.
func TakesBalanceToken(p Provider) bool {
	if p.BalanceURL != "" {
		return true
	}
	for _, h := range []string{hostOf(p.Chat), hostOf(p.Responses), hostOf(p.Anthropic)} {
		if h == "aihubmix.com" {
			return true
		}
	}
	return false
}

// money is an amount with its currency's sign in front: "¥12.34".
func money(sign string, v float64) string {
	return sign + strconv.FormatFloat(v, 'f', 2, 64)
}

func currencySign(code string) string {
	switch strings.ToUpper(code) {
	case "CNY", "RMB":
		return "¥"
	case "USD":
		return "$"
	case "":
		return ""
	}
	return strings.ToUpper(code) + " "
}

// number reads an amount given as a JSON number or as a string of one.
func number(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	}
	return 0, false
}

// deepSeekAmount is one currency of a DeepSeek account: its total, and
// the granted (赠送) and topped-up (充值) amounts it is made of, when the
// reply tells them.
type deepSeekAmount struct {
	code                   string
	total, granted, topped float64
	split                  bool
}

// deepSeekAmounts: {"is_available":true,"balance_infos":[{"currency":"CNY","total_balance":"110.00","granted_balance":"10.00","topped_up_balance":"100.00"}]}
func deepSeekAmounts(b []byte) ([]deepSeekAmount, error) {
	var r struct {
		Infos []struct {
			Currency string `json:"currency"`
			Total    any    `json:"total_balance"`
			Granted  any    `json:"granted_balance"`
			Topped   any    `json:"topped_up_balance"`
		} `json:"balance_infos"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	var amts []deepSeekAmount
	for _, in := range r.Infos {
		v, ok := number(in.Total)
		if !ok {
			continue
		}
		a := deepSeekAmount{code: strings.ToUpper(in.Currency), total: v}
		g, gok := number(in.Granted)
		t, tok := number(in.Topped)
		if gok && tok {
			a.granted, a.topped, a.split = g, t, true
		}
		amts = append(amts, a)
	}
	if len(amts) == 0 {
		return nil, errors.New("no balance in the reply")
	}
	// DeepSeek lists an account's currencies (CNY, USD) in an order that
	// changes from one read to the next (gakki on Discord), so they are
	// put in one of magpie's own: a currency with money left first, then
	// by its code. The first is the one the trend follows and an alert
	// reads (balanceSeries, BalanceNumber), so it mustn't swap places
	// between reads either.
	slices.SortStableFunc(amts, func(a, b deepSeekAmount) int {
		if (a.total > 0) != (b.total > 0) {
			if a.total > 0 {
				return -1
			}
			return 1
		}
		return strings.Compare(a.code, b.code)
	})
	return amts, nil
}

func readDeepSeek(b []byte) (string, error) {
	amts, err := deepSeekAmounts(b)
	if err != nil {
		return "", err
	}
	parts := make([]string, len(amts))
	for i, a := range amts {
		parts[i] = money(currencySign(a.code), a.total)
	}
	return strings.Join(parts, " · "), nil
}

// Split labels: what a balance is made of, each a key the GUI words in
// the user's language (the TUI and CLI say it in English).
const (
	SplitGranted = "Granted"
	SplitTopUp   = "Topped up"
)

// SplitWords is a balance's split on one line, in English, for the TUI
// and the CLI: "granted ¥10.00 · topped up ¥100.00".
func SplitWords(split []BalancePart) string {
	out := make([]string, 0, len(split))
	for _, p := range split {
		if p.Expires != nil {
			// through the last day it counts: "¥20.00 until Nov 13"
			out = append(out, p.Text+" until "+p.Expires.Add(-time.Second).Format("Jan 2"))
			continue
		}
		out = append(out, strings.ToLower(p.Label)+" "+p.Text)
	}
	return strings.Join(out, " · ")
}

// readDeepSeekSplit is what each currency's balance is made of: the
// granted credit apart from the money topped up (wwcraft_ on Discord),
// for a currency that has some granted. One with none granted is all
// topped up, its total already that, so it adds nothing.
func readDeepSeekSplit(b []byte) []BalancePart {
	amts, err := deepSeekAmounts(b)
	if err != nil {
		return nil
	}
	var out []BalancePart
	for _, a := range amts {
		if !a.split || a.granted == 0 {
			continue
		}
		sign := currencySign(a.code)
		out = append(out, BalancePart{Label: SplitGranted, Text: money(sign, a.granted)}, BalancePart{Label: SplitTopUp, Text: money(sign, a.topped)})
	}
	return out
}

// readMoonshot: {"code":0,"data":{"available_balance":49.58,…},"status":true}
func readMoonshot(sign string) func([]byte) (string, error) {
	return func(b []byte) (string, error) {
		var r struct {
			Data struct {
				Available any `json:"available_balance"`
			} `json:"data"`
		}
		if err := json.Unmarshal(b, &r); err != nil {
			return "", err
		}
		v, ok := number(r.Data.Available)
		if !ok {
			return "", errors.New("no balance in the reply")
		}
		return money(sign, v), nil
	}
}

// readOpenRouter: {"data":{"total_credits":20,"total_usage":3.5}}, in dollars.
func readOpenRouter(b []byte) (string, error) {
	var r struct {
		Data struct {
			Credits any `json:"total_credits"`
			Usage   any `json:"total_usage"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return "", err
	}
	credits, ok := number(r.Data.Credits)
	if !ok {
		return "", errors.New("no credits in the reply")
	}
	used, _ := number(r.Data.Usage)
	return money("$", credits-used), nil
}

// readSiliconFlow: {"code":20000,"data":{"totalBalance":"88.88",…}}
func readSiliconFlow(sign string) func([]byte) (string, error) {
	return func(b []byte) (string, error) {
		var r struct {
			Data struct {
				Total any `json:"totalBalance"`
			} `json:"data"`
		}
		if err := json.Unmarshal(b, &r); err != nil {
			return "", err
		}
		v, ok := number(r.Data.Total)
		if !ok {
			return "", errors.New("no balance in the reply")
		}
		return money(sign, v), nil
	}
}

// siliconFlowGone words SiliconFlow's answer to /v1/user/info since it
// retired the query on 2026-08-14 (410, {"code":20092,"message":"This
// endpoint is deprecated and is no longer available.","data":null}, #1094):
// it has no API for the balance in its place yet (its release notes of
// 2026-08-11 promise an account-level one "in due course", and none is
// listed up to 2026-11), and the balance given before 2025-12 is a voucher
// now, so both are read on the console, where the card's button opens
// (siliconFlowRetired). A key refused for itself (401 30014) is still
// said as it came.
func siliconFlowGone(console, where string) func(int, []byte) error {
	return func(status int, b []byte) error {
		var r struct {
			Code int `json:"code"`
		}
		_ = json.Unmarshal(b, &r)
		if status != http.StatusGone && r.Code != 20092 {
			return nil
		}
		return siliconFlowRetired{console, where}
	}
}

// siliconFlowRetired is SiliconFlow's retired balance query: not a read
// that failed for now, so its card says where the balance is shown and
// opens that console (SeeAt), and keeps no balance read before the query
// went (nam.haid on Discord: every SiliconFlow card said only
// "Allowance unavailable").
type siliconFlowRetired struct{ console, where string }

func (e siliconFlowRetired) Error() string {
	return "SiliconFlow retired its balance query (/v1/user/info) and has no API for it in its place yet; the balance and vouchers are on " + e.console + e.where
}

// zhipuBalanceSource is Zhipu's (BigModel's) and Z.ai's account report,
// /api/biz/account/query-customer-account-report on the API's own host,
// which their consoles read and which takes the API key as a Bearer: it
// is not in docs.bigmodel.cn, so a reply in another shape is said as
// unread, never as a balance. Asked with a GLM Coding Plan's endpoints it
// isn't: the plan's windows are that key's card (planQuotaSourceOf), and
// its cash balance, ¥0 for most, would be noise beside them. nam.haid on
// Discord: a Zhipu key had no card at all.
func zhipuBalanceSource(p Provider) (balanceSource, bool) {
	host, payg := "", false
	for _, base := range []string{p.Chat, p.Responses, p.Anthropic} {
		h := hostOf(base)
		if !zhipuHost(h) {
			continue
		}
		if strings.Contains(base, "/api/coding/") {
			return balanceSource{}, false
		}
		host = strings.ToLower(h)
		payg = payg || basePath(strings.TrimRight(strings.TrimSpace(base), "/")) == zhipuPaygPath
	}
	if host == "" {
		return balanceSource{}, false
	}
	sign := "¥"
	if host == "api.z.ai" {
		sign = "$"
	}
	u := "https://" + host + zhipuAccountReport
	return balanceSource{u, readZhipuBalance(u, sign, payg), "", nil, nil}, true
}

// zhipuAccountReport is the account report's path on both hosts.
const zhipuAccountReport = "/api/biz/account/query-customer-account-report"

// readZhipuBalance reads the account report, as BigModel and Z.ai answer
// it to a key (asked 2026-11-03):
//
//	{"code":200,"msg":"操作成功","data":{"balance":0E-9,"rechargeAmount":0.000000,
//	  "giveAmount":0.000000,"totalSpendAmount":0E-9,"todaySpendAmount":null,
//	  "availableBalance":0E-9,"frozenBalance":0E-9,"creditBalance":null,
//	  "availableCreditBalance":null,"creditStatus":"NOT_OPEN",…},"success":true}
//
// availableBalance being what is left to spend, the frozen part taken
// out. A refusal comes 200 too, in the platform's envelope ({"code":1000,
// "msg":"身份验证失败。","success":false}): a refused key is said as one
// (zhipuRefused), any other code with its words. Asked at only the
// Anthropic endpoint, which a Coding Plan's key shares, an account that
// was never paid or given anything has no card (errNoBalance); at the
// pay-as-you-go endpoint its ¥0.00 is the balance it has.
func readZhipuBalance(u, sign string, payg bool) func([]byte) (string, error) {
	return func(b []byte) (string, error) {
		if code, msg, ok := zhipuEnvelope(b); ok {
			if ZhipuKeyRefused(code) {
				return "", zhipuRefused(u, zhipuKeyRefusal{msg})
			}
			if msg == "" {
				msg = "refused"
			}
			return "", fmt.Errorf("%s answered the balance read with code %d: %s", zhipuVendor(u), code, msg)
		}
		var r struct {
			Data *struct {
				Available any `json:"availableBalance"`
				Balance   any `json:"balance"`
				Recharged any `json:"rechargeAmount"`
				Given     any `json:"giveAmount"`
			} `json:"data"`
		}
		d := json.NewDecoder(bytes.NewReader(b))
		d.UseNumber()
		if err := d.Decode(&r); err != nil {
			return "", err
		}
		if r.Data == nil {
			return "", fmt.Errorf("%s's account report has no data in it", zhipuVendor(u))
		}
		v, ok := number(r.Data.Available)
		if !ok {
			return "", fmt.Errorf("%s's account report has no availableBalance in it", zhipuVendor(u))
		}
		if !payg {
			bal, _ := number(r.Data.Balance)
			paid, _ := number(r.Data.Recharged)
			given, _ := number(r.Data.Given)
			if v == 0 && bal == 0 && paid == 0 && given == 0 {
				return "", errNoBalance
			}
		}
		return money(sign, v), nil
	}
}

// zhipuVendor names the vendor of a Zhipu or Z.ai URL.
func zhipuVendor(u string) string {
	if hostOf(u) == "api.z.ai" {
		return "Z.ai"
	}
	return "Zhipu"
}

// readStepFun: {"object":"account","type":"prepaid","balance":26.00,
// "total_cash_balance":0.00,"total_voucher_balance":26.00}, balance being
// what is left to spend, vouchers included, and the totals what was ever
// paid in and given. A Step Plan key tells it too: the plan's key is the
// account's, and the account's balance is what it spends past the plan.
func readStepFun(sign string) func([]byte) (string, error) {
	return func(b []byte) (string, error) {
		var r struct {
			Balance any `json:"balance"`
		}
		if err := json.Unmarshal(b, &r); err != nil {
			return "", err
		}
		v, ok := number(r.Balance)
		if !ok {
			return "", errors.New("no balance in the reply")
		}
		return money(sign, v), nil
	}
}

// modelScopeBalanceURL is ModelScope's 魔粒 balance in its OpenAPI
// (modelscope.cn/.well-known/openapi.json, GET /magicubes/balance), asked
// with the same API token API-Inference takes, as a Bearer (jiabubu on
// Discord). The page's daily list (/magicubes/transactions) is not in the
// OpenAPI, so what is spent a day is the card's own trend.
const modelScopeBalanceURL = "https://modelscope.cn/openapi/v1/magicubes/balance"

// readModelScope: {"success":true,"data":{"total_balance":1500,
// "available_balance":1300,"frozen_amount":200},"request_id":"…"}, in 魔粒.
// What can be spent is available_balance: the total holds what running
// tasks have frozen. A refusal ({"success":false,"message":…}) is told
// by balanceRefusal before this.
func readModelScope(b []byte) (string, error) {
	var r struct {
		Data struct {
			Available any `json:"available_balance"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return "", err
	}
	v, ok := number(r.Data.Available)
	if !ok {
		return "", errors.New("no balance in the reply")
	}
	return strconv.FormatFloat(v, 'f', -1, 64) + " 魔粒", nil
}

// readAiHubMix: {"object":"list","total_usage":12.5}, what is left on the
// key in dollars, despite the name. A key without a limit answers -1 of
// AiHubMix's units ($1 is 500000 of them): it has no balance of its own, and
// the account's is told only to the account's access token, not to a key.
func readAiHubMix(b []byte) (string, error) {
	var r struct {
		Remain any `json:"total_usage"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return "", err
	}
	v, ok := number(r.Remain)
	if !ok {
		return "", errors.New("no balance in the reply")
	}
	if v < 0 {
		return "", errors.New("this key has no limit, and AiHubMix tells a key only what is left on it: give magpie the account's access token (AiHubMix → Settings → Generate System Access Token) in this provider's settings to see the account's balance, or give the key a limit in AiHubMix's console")
	}
	return money("$", v), nil
}

// readCommandCode: the dollars left on a Command Code key's account, the
// month's credits and the bought and free ones (see cmdCredits), as the
// CLI's totalRemaining. The plan's 5-hour and weekly windows are a card
// of their own with meters and resets (readCommandCodePlan), not words
// squeezed into this amount.
func readCommandCode(b []byte) (string, error) {
	var c cmdCredits
	if err := json.Unmarshal(b, &c); err != nil {
		return "", err
	}
	_, left, ok := cmdLeft(c)
	if !ok {
		return "", errors.New("no balance in the reply")
	}
	return money("$", left), nil
}

// readAiHubMixAccount: {"success":true,"data":{"quota":2500000,…}}, the
// account's balance in AiHubMix's units, $1 to 500000 of them.
func readAiHubMixAccount(b []byte) (string, error) {
	var r struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
		Data    struct {
			Quota any `json:"quota"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return "", err
	}
	if !r.Success {
		if r.Message == "" {
			r.Message = "AiHubMix didn't take the access token"
		}
		return "", errors.New(r.Message)
	}
	v, ok := number(r.Data.Quota)
	if !ok {
		return "", errors.New("no balance in the reply")
	}
	return money("$", v/500000), nil
}

// readBalancePath picks the amount out of a reply by a dotted path, array
// items by their index: "data.total_available", "balance_infos.0.total_balance".
// It can be sums of paths and numbers, with + - * / and brackets, for a
// relay counting in its own units ("data.total_available / 500000") or
// telling what was used of a plan ("(1 - credits.monthlyCredits / 70) %").
// A "$" or "¥" before it is put in front of the amount; a "%" after it
// shows it as a percent of 1 (0.25 is 25%). A path alone that is not a
// number is shown as it is. A path through a list's items ("a.*.used",
// "a[status=active].used") is several values, which sum(), max(), min()
// or count() make one (balanceExpr). Several amounts, each with a label if
// wanted, go apart by ";" and are shown together: "5h: windowLimits.
// fiveHour.used / windowLimits.fiveHour.cap %; week: …; $credits.
// monthlyCredits" is "5h 0% · week 3.4% · $70.00"; one of them the reply
// hasn't is "—" while the others show.
func readBalancePath(b []byte, path string) (string, error) {
	parts, err := readBalanceParts(b, path)
	if err != nil {
		return "", err
	}
	return joinBalanceParts(parts), nil
}

// BalancePart is one amount of a balance field, for a card to show each
// of several on a line of its own, its label apart from its figure, rather
// than all of them run together: Percent is its share, 0 to 100, when it
// was asked as one ("%" after it), for a meter.
type BalancePart struct {
	Label   string   `json:"label,omitempty"`
	Text    string   `json:"text"`
	Percent *float64 `json:"percent,omitempty"`
	// Error is why the amount couldn't be read, its Text then "—".
	Error string `json:"error,omitempty"`
	// Expires is when the part counts no more, for a balance typed with
	// credit that expires (key_balance_own.go); the GUI says in how many
	// days, its Label then empty
	Expires *time.Time `json:"expires,omitempty"`
}

// joinBalanceParts is the amounts on one line, as Balance tells them:
// "5h 0% · week 3.4% · $70.00".
func joinBalanceParts(parts []BalancePart) string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p.Label != "" {
			out = append(out, p.Label+" "+p.Text)
		} else {
			out = append(out, p.Text)
		}
	}
	return strings.Join(out, " · ")
}

// readBalanceParts is readBalancePath's amounts, each apart.
func readBalanceParts(b []byte, path string) ([]BalancePart, error) {
	if !strings.Contains(path, ";") && !strings.Contains(path, ":") {
		v, pct, err := readBalanceOne(b, path)
		if err != nil {
			return nil, err
		}
		return []BalancePart{{Text: v, Percent: pct}}, nil
	}
	var out []BalancePart
	var first error
	read := false
	for part := range strings.SplitSeq(path, ";") {
		if strings.TrimSpace(part) == "" {
			continue
		}
		label, expr, ok := strings.Cut(part, ":")
		if !ok {
			label, expr = "", part
		}
		label = strings.TrimSpace(label)
		v, pct, err := readBalanceOne(b, expr)
		if err != nil {
			// one amount the reply hasn't (an index past the list's end, a
			// window whose cap is 0) is "—", and the others still show
			// (#1993); the whole fails only when none can be read
			if label != "" {
				err = fmt.Errorf("%s: %w", label, err)
			}
			if first == nil {
				first = err
			}
			out = append(out, BalancePart{Label: label, Text: "—", Error: err.Error()})
			continue
		}
		read = true
		out = append(out, BalancePart{Label: label, Text: v, Percent: pct})
	}
	if len(out) == 0 {
		return nil, errors.New("no balance path: where in the reply the amount is, e.g. data.balance")
	}
	if !read {
		return nil, first
	}
	return out, nil
}

// readBalanceOne is one amount of a balance path, and its share in percent
// when it was asked as one.
func readBalanceOne(b []byte, path string) (string, *float64, error) {
	path = strings.TrimSpace(path)
	sign := ""
	for _, s := range []string{"$", "¥", "€", "£"} {
		if rest, ok := strings.CutPrefix(path, s); ok {
			sign, path = s, strings.TrimSpace(rest)
			break
		}
	}
	percent := false
	if rest, ok := strings.CutSuffix(path, "%"); ok {
		percent, path = true, strings.TrimSpace(rest)
	}
	if path == "" {
		return "", nil, errors.New("no balance path: where in the reply the amount is, e.g. data.balance")
	}
	var reply any
	if err := json.Unmarshal(b, &reply); err != nil {
		return "", nil, errors.New("the reply is not JSON")
	}
	e := &balanceExpr{src: path, reply: reply}
	if e.lone() {
		// a path alone: its value, a number or not
		v, err := e.at(path)
		if err != nil {
			return "", nil, err
		}
		if n, ok := number(v); ok {
			return balanceAmount(sign, n, percent), balanceShare(n, percent), nil
		}
		if s, ok := v.(string); ok && s != "" && !percent {
			return sign + s, nil, nil
		}
		return "", nil, fmt.Errorf("%q in the reply is not an amount", path)
	}
	n, err := e.number()
	if err != nil {
		return "", nil, err
	}
	if math.IsInf(n, 0) || math.IsNaN(n) {
		return "", nil, fmt.Errorf("the balance path divides by nothing")
	}
	return balanceAmount(sign, n, percent), balanceShare(n, percent), nil
}

// balanceShare is an amount asked as a percent, 0.25 as 25; nil for one
// that wasn't.
func balanceShare(n float64, percent bool) *float64 {
	if !percent {
		return nil
	}
	p := n * 100
	return &p
}

func balanceAmount(sign string, n float64, percent bool) string {
	if percent {
		return sign + strings.TrimSuffix(strconv.FormatFloat(n*100, 'f', 1, 64), ".0") + "%"
	}
	return money(sign, n)
}

// balanceExpr reads a balance path's sum: paths into the reply, numbers,
// + - * / and brackets. A path can go through every item of a list ("*",
// "data.subscriptions.*.used") or those of them that match ("[status=
// active]", "[plan.kind!=free]"), and is then several values, which
// sum(), max(), min() or count() make one (#1993): "sum(a.*.used) /
// sum(a.*.cap)", "max(a.*.used / a.*.cap)". Several values left as they
// are, outside those, are an error rather than a guess at which of them
// was meant. Within them, an item without the path, or one divided by 0,
// is left out.
type balanceExpr struct {
	src   string
	i     int
	reply any
}

// balanceVal is a number, or several (many) when a path went through a
// list; NaN stands for an item that has none.
type balanceVal struct {
	n    float64
	ns   []float64
	many bool
	// items are a path's own values, for count() to count those that are
	// there, numbers or not
	items []any
}

// balanceFuncs make a path's several values one.
var balanceFuncs = map[string]func([]float64) (float64, error){
	"sum": func(ns []float64) (float64, error) {
		s := 0.0
		for _, n := range ns {
			if !math.IsNaN(n) {
				s += n
			}
		}
		return s, nil
	},
	"count": func(ns []float64) (float64, error) {
		c := 0.0
		for _, n := range ns {
			if !math.IsNaN(n) {
				c++
			}
		}
		return c, nil
	},
	"max": func(ns []float64) (float64, error) { return balanceExtreme(ns, "max", 1) },
	"min": func(ns []float64) (float64, error) { return balanceExtreme(ns, "min", -1) },
}

func balanceExtreme(ns []float64, name string, sign float64) (float64, error) {
	best, found := 0.0, false
	for _, n := range ns {
		if !math.IsNaN(n) && (!found || (n-best)*sign > 0) {
			best, found = n, true
		}
	}
	if !found {
		return 0, fmt.Errorf("%s() of the balance path has nothing to take it of", name)
	}
	return best, nil
}

func (e *balanceExpr) space() {
	for e.i < len(e.src) && e.src[e.i] == ' ' {
		e.i++
	}
}

// lone is whether the whole is one path, to one value.
func (e *balanceExpr) lone() bool {
	return !strings.ContainsAny(e.src, "+-*/() [") && e.src != "" && !isDigit(e.src[0])
}

// number is the whole's one number.
func (e *balanceExpr) number() (float64, error) {
	v, err := e.sum()
	if err == nil && e.i < len(e.src) {
		err = fmt.Errorf("the balance path has %q it can't read", e.src[e.i:])
	}
	if err == nil && v.many {
		err = errors.New("the balance path has several values where one is wanted: put them in sum(), max(), min() or count()")
	}
	return v.n, err
}

func (e *balanceExpr) sum() (balanceVal, error) {
	v, err := e.product()
	for err == nil {
		e.space()
		if e.i >= len(e.src) || (e.src[e.i] != '+' && e.src[e.i] != '-') {
			break
		}
		op := e.src[e.i]
		e.i++
		var w balanceVal
		if w, err = e.product(); err == nil {
			v, err = balanceOp(v, w, op)
		}
	}
	return v, err
}

func (e *balanceExpr) product() (balanceVal, error) {
	v, err := e.unary()
	for err == nil {
		e.space()
		if e.i >= len(e.src) || (e.src[e.i] != '*' && e.src[e.i] != '/') {
			break
		}
		op := e.src[e.i]
		e.i++
		var w balanceVal
		if w, err = e.unary(); err == nil {
			v, err = balanceOp(v, w, op)
		}
	}
	return v, err
}

// balanceOp is v op w, item by item when either is several values: two
// lists by the same path go together, a number with each of a list's.
func balanceOp(v, w balanceVal, op byte) (balanceVal, error) {
	one := func(a, b float64) float64 {
		switch op {
		case '+':
			return a + b
		case '-':
			return a - b
		case '*':
			return a * b
		}
		if b == 0 {
			return math.NaN()
		}
		return a / b
	}
	if !v.many && !w.many {
		if op == '/' && w.n == 0 {
			return v, errors.New("the balance path divides by 0")
		}
		return balanceVal{n: one(v.n, w.n)}, nil
	}
	if v.many && w.many && len(v.ns) != len(w.ns) {
		return v, fmt.Errorf("the balance path puts %d values with %d", len(v.ns), len(w.ns))
	}
	size := max(len(v.ns), len(w.ns))
	out := make([]float64, size)
	for i := range out {
		a, b := v.n, w.n
		if v.many {
			a = v.ns[i]
		}
		if w.many {
			b = w.ns[i]
		}
		out[i] = one(a, b)
	}
	return balanceVal{ns: out, many: true}, nil
}

func (e *balanceExpr) unary() (balanceVal, error) {
	e.space()
	if e.i >= len(e.src) {
		return balanceVal{}, errors.New("the balance path ends where a number or a path should be")
	}
	switch c := e.src[e.i]; {
	case c == '-':
		e.i++
		v, err := e.unary()
		if err != nil {
			return v, err
		}
		return balanceOp(balanceVal{n: 0}, v, '-')
	case c == '(':
		e.i++
		return e.closed(e.sum())
	case isDigit(c) || c == '.':
		j := e.i
		for j < len(e.src) && (isDigit(e.src[j]) || e.src[j] == '.') {
			j++
		}
		n, err := strconv.ParseFloat(e.src[e.i:j], 64)
		if err != nil {
			return balanceVal{}, fmt.Errorf("the balance path has %q, not a number", e.src[e.i:j])
		}
		e.i = j
		return balanceVal{n: n}, nil
	}
	j := e.i
	for j < len(e.src) {
		c := e.src[j]
		if c == '[' {
			k := strings.IndexByte(e.src[j:], ']')
			if k < 0 {
				return balanceVal{}, errors.New("the balance path has a [ without its ]")
			}
			j += k + 1
			continue
		}
		// "*" right after a dot, or at the start, is every item of a list
		if c == '*' && (j == e.i || e.src[j-1] == '.') {
			j++
			continue
		}
		if strings.IndexByte("+-*/() ", c) >= 0 {
			break
		}
		j++
	}
	if j == e.i {
		return balanceVal{}, fmt.Errorf("the balance path has %q where a number or a path should be", e.src[e.i:])
	}
	path := e.src[e.i:j]
	e.i = j
	if f, ok := balanceFuncs[path]; ok && e.i < len(e.src) && e.src[e.i] == '(' {
		e.i++
		v, err := e.closed(e.sum())
		if err != nil {
			return v, err
		}
		ns := v.ns
		if !v.many {
			ns = []float64{v.n}
		}
		if path == "count" && v.items != nil {
			ns = make([]float64, 0, len(v.items))
			for _, x := range v.items {
				if x != nil {
					ns = append(ns, 1)
				}
			}
		}
		n, err := f(ns)
		return balanceVal{n: n}, err
	}
	vs, many, err := e.all(path)
	if err != nil {
		return balanceVal{}, err
	}
	if !many {
		n, ok := number(vs[0])
		if !ok {
			return balanceVal{}, fmt.Errorf("%q in the reply is not a number", path)
		}
		return balanceVal{n: n}, nil
	}
	ns := make([]float64, len(vs))
	for i, x := range vs {
		ns[i] = math.NaN()
		if n, ok := number(x); ok {
			ns[i] = n
		}
	}
	return balanceVal{ns: ns, many: true, items: vs}, nil
}

// closed is a bracket's inside, with the ) after it.
func (e *balanceExpr) closed(v balanceVal, err error) (balanceVal, error) {
	if err != nil {
		return v, err
	}
	e.space()
	if e.i >= len(e.src) || e.src[e.i] != ')' {
		return v, errors.New("the balance path has a ( without its )")
	}
	e.i++
	return v, nil
}

// at is what is at a dotted path in the reply, the path to one value.
func (e *balanceExpr) at(path string) (any, error) {
	vs, many, err := e.all(path)
	if err != nil {
		return nil, err
	}
	if many {
		return nil, fmt.Errorf("%q is several values: put it in sum(), max(), min() or count()", path)
	}
	return vs[0], nil
}

// all is what is at a dotted path in the reply: one value, or, when the
// path goes through a list by "*" or "[…]", every item's (many), nil for
// an item without the rest of the path.
func (e *balanceExpr) all(path string) ([]any, bool, error) {
	segs, err := balanceSegments(path)
	if err != nil {
		return nil, false, err
	}
	vs, many := []any{e.reply}, false
	for _, s := range segs {
		var next []any
		for _, v := range vs {
			if v == nil {
				next = append(next, nil)
				continue
			}
			next = append(next, s.step(v)...)
		}
		many = many || s.spreads()
		vs = next
		if !many && (len(vs) != 1 || vs[0] == nil) {
			return nil, false, fmt.Errorf("nothing at %q in the reply", path)
		}
	}
	return vs, many, nil
}

// balanceSegment is one step of a path: a key, an index or "*", and the
// tests an item must pass, "[status=active]": a list's items that pass,
// or an object when it does.
type balanceSegment struct {
	key   string
	tests []balanceTest
}

type balanceTest struct {
	path []string
	not  bool
	want string
}

func (s balanceSegment) spreads() bool { return s.key == "*" || len(s.tests) > 0 }

// step is what one value leads to: one value, or a list's items.
func (s balanceSegment) step(v any) []any {
	if s.key == "*" {
		v = balanceItems(v)
	} else if s.key != "" {
		v = balanceChild(v, s.key)
	}
	if len(s.tests) == 0 {
		if s.key == "*" {
			return v.([]any)
		}
		return []any{v}
	}
	// a list's items that pass, or an object itself when it does
	items, ok := v.([]any)
	if !ok {
		items = []any{v}
	}
	var out []any
	for _, x := range items {
		if x != nil && s.passes(x) {
			out = append(out, x)
		}
	}
	return out
}

func (s balanceSegment) passes(x any) bool {
	for _, t := range s.tests {
		v := x
		for _, k := range t.path {
			if v = balanceChild(v, k); v == nil {
				break
			}
		}
		got := ""
		switch y := v.(type) {
		case string:
			got = y
		case float64:
			got = strconv.FormatFloat(y, 'f', -1, 64)
		case bool:
			got = strconv.FormatBool(y)
		}
		if (v != nil && got == t.want) == t.not {
			return false
		}
	}
	return true
}

// balanceChild is a key of an object, or an index of a list.
func balanceChild(v any, k string) any {
	switch x := v.(type) {
	case map[string]any:
		return x[k]
	case []any:
		if i, err := strconv.Atoi(k); err == nil && i >= 0 && i < len(x) {
			return x[i]
		}
	}
	return nil
}

// balanceItems is a list's items, or an object's values by key.
func balanceItems(v any) []any {
	switch x := v.(type) {
	case []any:
		return x
	case map[string]any:
		keys := slices.Sorted(maps.Keys(x))
		out := make([]any, len(keys))
		for i, k := range keys {
			out[i] = x[k]
		}
		return out
	}
	return []any{}
}

// balanceSegments splits a path at its dots, a "[…]" test kept whole.
func balanceSegments(path string) ([]balanceSegment, error) {
	var out []balanceSegment
	for rest := path; ; {
		end := 0
		for end < len(rest) && rest[end] != '.' {
			if rest[end] == '[' {
				k := strings.IndexByte(rest[end:], ']')
				if k < 0 {
					return nil, errors.New("the balance path has a [ without its ]")
				}
				end += k
			}
			end++
		}
		seg, err := balanceSegmentOf(rest[:end], path)
		if err != nil {
			return nil, err
		}
		out = append(out, seg)
		if end == len(rest) {
			return out, nil
		}
		rest = rest[end+1:]
	}
}

func balanceSegmentOf(raw, path string) (balanceSegment, error) {
	key, tests, _ := strings.Cut(raw, "[")
	s := balanceSegment{key: key}
	if key == "" && tests == "" {
		return s, fmt.Errorf("nothing at %q in the reply", path)
	}
	for tests != "" {
		body, after, ok := strings.Cut(tests, "]")
		if !ok {
			return s, errors.New("the balance path has a [ without its ]")
		}
		t := balanceTest{}
		left, want, ok := strings.Cut(body, "=")
		if !ok || strings.TrimSpace(left) == "" {
			return s, fmt.Errorf("the balance path has [%s], not a test like [status=active]", body)
		}
		if l, isNot := strings.CutSuffix(left, "!"); isNot {
			t.not, left = true, l
		}
		t.path = strings.Split(strings.TrimSpace(left), ".")
		t.want = strings.Trim(strings.TrimSpace(want), `'"`)
		s.tests = append(s.tests, t)
		tests = strings.TrimPrefix(after, "[")
	}
	return s, nil
}

// Balance asks the vendor what is left on the provider's key in use. ok is
// false when there is no way to ask it. A key given limits and no quota of
// its own has what its fullest window has left: what it can spend now.
func Balance(ctx context.Context, p Provider) (amount string, ok bool, err error) {
	amount, _, _, ws, _, ok, err := balanceRead(ctx, p)
	if !ok && errors.Is(err, errNoBalance) {
		// a relay added by hand whose balance was found nowhere: no
		// balance to ask, as with no Balance URL
		return "", false, nil
	}
	if err == nil && amount == "" && len(ws) > 0 {
		left := ws[0].Limit - ws[0].Amount
		for _, w := range ws[1:] {
			left = min(left, w.Limit-w.Amount)
		}
		amount = money("$", max(0, left))
	}
	return amount, ok, err
}

// balanceRead is what Balance reads, with the amounts of a balance field
// the user wrote each apart (nil for a vendor magpie reads itself), what a
// vendor's balance is made of when it tells that (split), and the
// key's own usage windows when its reply tells them (a sub2api key's
// limits, readSub2APIKeyLimits), and whether what it tells is the
// account's, the same to each of its keys (wide, balanceAccountWide).
func balanceRead(ctx context.Context, p Provider) (amount string, parts, split []BalancePart, ws []QuotaWindow, wide, ok bool, err error) {
	ctx = p.Via(ctx)
	p = keyQueryOf(p)
	// the editor's check asks with the form's headers as they are, a user
	// id typed as a header's name among them (userIDHeader)
	p.Headers = userIDHeader(p.Headers)
	src, ok := balanceSourceOf(p)
	if !ok && probesBalance(p) {
		// a relay added by hand: its balance where relays tell it, or
		// no card (balance_probe.go)
		u, found := probeBalance(ctx, p)
		if !found {
			return "", nil, nil, nil, false, false, errNoBalance
		}
		p.BalanceURL = u
		src, ok = balanceSourceOf(p)
	}
	if !ok && p.Account == nil && p.Key != "" && openCodeZen(p) {
		// a balance the vendor keeps from the key: said so, not left out
		return "", nil, nil, nil, false, true, errOpenCodeNoBalance
	}
	if !ok || p.Account != nil || p.Key == "" {
		return "", nil, nil, nil, false, false, nil
	}
	if src.token != "" && p.BalanceURL != "" {
		switch balanceURLPath(p.BalanceURL) {
		case newAPIKeyUsage:
			return "", nil, nil, nil, false, true, errKeyUsageWithToken(p.BalanceURL)
		case sub2APIKeyUsage:
			return "", nil, nil, nil, false, true, errSub2APIKeyUsageWithToken(p.BalanceURL)
		}
	}
	method := http.MethodGet
	if p.BalanceURL != "" {
		// the method the user named goes with their own Balance URL only;
		// a vendor magpie knows is asked as it answers
		if method, err = balanceMethodOf(p.BalanceMethod); err != nil {
			return "", nil, nil, nil, false, true, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, withBalanceKey(src.url, p.Key, true), nil)
	if err != nil {
		return "", nil, nil, nil, false, true, err
	}
	if src.token != "" {
		// a named endpoint still gets the provider's headers, which is
		// where a new-api relay's New-Api-User goes
		if p.BalanceURL != "" {
			for k, v := range p.SentHeaders(ctx) {
				req.Header.Set(k, withBalanceKey(v, p.Key, false))
			}
		}
		req.Header.Set("Authorization", balanceAuthorization(src.token))
	} else {
		for k, v := range AuthHeaders(p, Chat) {
			req.Header.Set(k, v)
		}
		for k, v := range p.SentHeaders(ctx) {
			req.Header.Set(k, withBalanceKey(v, p.Key, false))
		}
	}
	req.Header.Set("Accept", "application/json")
	res, err := Keyed(http.DefaultClient).Do(req)
	if err != nil {
		if k := url.QueryEscape(p.Key); k != "" && strings.Contains(err.Error(), k) {
			// the URL in the error has the key in it: not shown on a card
			return "", nil, nil, nil, false, true, errors.New(strings.ReplaceAll(err.Error(), k, Mask(p.Key)))
		}
		return "", nil, nil, nil, false, true, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if msg, refused := balanceRefusal(b); refused {
		// said as the relay says it, whatever the status: not a field
		// missing from a reply that was never the balance
		msg = newAPIUserHint(msg)
		if res.StatusCode >= 300 {
			msg = res.Status + ": " + msg
		}
		return "", nil, nil, nil, false, true, errors.New(msg)
	}
	if res.StatusCode >= 300 && src.failed != nil {
		if err := src.failed(res.StatusCode, b); err != nil {
			return "", nil, nil, nil, false, true, err
		}
	}
	if res.StatusCode >= 300 {
		// what a JSON reply says, on one line; a page of HTML says nothing
		msg := strings.Join(strings.Fields(string(b)), " ")
		if !strings.HasPrefix(msg, "{") {
			return "", nil, nil, nil, false, true, errors.New(res.Status)
		}
		if r := []rune(msg); len(r) > 200 {
			msg = string(r[:200]) + "…"
		}
		return "", nil, nil, nil, false, true, fmt.Errorf("%s: %s", res.Status, msg)
	}
	if p.BalanceURL != "" {
		if sub2APIKeyLimits(p) {
			ws, _ = readSub2APIKeyLimits(b)
		}
		// the field the user wrote: its amounts each apart, as well
		if parts, err = readBalanceParts(b, balancePathOf(p)); err != nil {
			if len(ws) > 0 && strings.TrimSpace(p.BalancePath) == "" {
				// a key given limits and no quota of its own has no
				// "remaining": its windows are what it has left
				return "", nil, nil, ws, false, true, nil
			}
			return "", nil, nil, nil, false, true, err
		}
		return joinBalanceParts(parts), parts, nil, ws, balanceAccountWide(p, src, b), true, nil
	}
	amount, err = src.read(b)
	if err == nil && src.split != nil {
		split = src.split(b)
	}
	return amount, nil, split, nil, balanceAccountWide(p, src, b), true, err
}

// balanceAccountWide says what a balance query told is the account's,
// the same whichever of the account's keys asks, rather than the key's
// own (mimi on Discord: three keys of one site each showed its $8.39).
// It goes by what was asked, never the vendor's name: an account query
// asked with its token; a vendor's own query magpie knows (DeepSeek's
// /user/balance, Moonshot's /users/me/balance, OpenRouter's /credits,
// SiliconFlow's, StepFun's, Command Code's and Ollama's), all but
// AiHubMix's /dashboard/billing/remain, which tells the key's own; and a
// sub2api key's /v1/usage when the key has no quota or limits of its own
// (mode "unrestricted"), which tells the user's wallet or the group's
// subscription. new-api's /api/usage/token and any other query the user
// named are a key's own.
func balanceAccountWide(p Provider, src balanceSource, body []byte) bool {
	if src.token != "" {
		return true
	}
	if p.BalanceURL == "" {
		return src.url != aiHubMixKeyRemain
	}
	if !sub2APIKeyLimits(p) {
		return false
	}
	var r struct {
		Mode string `json:"mode"`
	}
	return json.Unmarshal(body, &r) == nil && r.Mode == "unrestricted"
}

// OpenCodeConsole is where OpenCode shows a workspace's credits: the
// Credits card on the console's Overview, to its owners and admins.
const OpenCodeConsole = "https://opencode.ai/console"

// errOpenCodeNoBalance is a Zen key's balance, which OpenCode tells no
// key: Zen's routes (/zen/v1, the inference API's /inference/v1) have no
// usage or balance query, only Go's /zen/go/v1/usage, which tells Go's
// windows and not the credits (proxyInference's paths in sst/opencode's
// packages/console/app/src/lib/inference-proxy.ts; /zen/v1/usage and
// /inference/v1/usage answer 404), and its docs give the balance only on
// the console's Credits card (console/billing.mdx, "Check your balance").
// The card used to be left out unsaid (ttmrmx on X).
var errOpenCodeNoBalance = errors.New("OpenCode tells no API key its balance: Zen's pay-as-you-go credits are shown only in the OpenCode console (" + OpenCodeConsole + ", Overview → Credits, to the workspace's owners and admins)")

// openCodeZen says p is at OpenCode Zen, whose credits OpenCode keeps from
// the key, and not at Go, whose windows are its card (planQuotaSourceOf).
func openCodeZen(p Provider) bool {
	zen := false
	for _, b := range []string{p.Chat, p.Responses, p.Anthropic} {
		switch openCodeGateway(b) {
		case "go":
			return false
		case "zen":
			zen = true
		}
	}
	return zen
}

// balanceMethodOf is the HTTP method a Balance URL is asked with: GET
// when none is named, else GET or POST in any case. Another is an error,
// never a GET in its place.
func balanceMethodOf(m string) (string, error) {
	switch strings.ToUpper(strings.TrimSpace(m)) {
	case "", http.MethodGet:
		return http.MethodGet, nil
	case http.MethodPost:
		return http.MethodPost, nil
	}
	return "", fmt.Errorf("the balance method %q is neither GET nor POST", m)
}

// sub2APIKeyLimits says the provider's Balance URL is a sub2api panel's
// query for a key, whose reply tells the key's own limits when it has
// some.
func sub2APIKeyLimits(p Provider) bool {
	p = keyQueryOf(p)
	return p.BalanceURL != "" && balanceURLPath(p.BalanceURL) == sub2APIKeyUsage
}

// balanceKeyNames are what a Balance URL or a header's value names the
// key by, for a vendor that wants it somewhere of its own, in the query
// most often (…/balance?key={key}) or a header (Authorization: Bearer
// {token}): each key the provider has on is put in its own ask, so each
// card tells that key's balance. Any case, in one or two braces or as
// ${…}: {key}, {apiKey}, {api_key}, {API_KEY}, {token}, {{key}}, ${API_KEY}.
// {token} is the key too: it is what a vendor's own error names the value
// in "Authorization: Bearer {token}" (lx_78654 on Discord, whose header
// went with "{token}" in it as written, and the vendor refused it).
var balanceKeyNames = regexp.MustCompile(`(?i)\$?\{\{?\s*(?:key|api[_-]?key|token)\s*\}?\}`)

// withBalanceKey is s with the key in place of its names, escaped for a
// URL's query when inURL.
func withBalanceKey(s, key string, inURL bool) string {
	if inURL {
		key = url.QueryEscape(key)
	}
	return balanceKeyNames.ReplaceAllLiteralString(s, key)
}

// keyNotToken is p without a balance token that is only its own key
// pasted there: the query is asked as with no token, the key as a Bearer
// the way a chat request sends it, not the key bare in Authorization as
// a new-api access token goes, which a vendor that wants "Bearer <key>"
// refuses (lx_78654 on Discord).
func keyNotToken(p Provider) Provider {
	t := strings.TrimSpace(p.BalanceToken)
	if len(t) > 7 && strings.EqualFold(t[:7], "bearer ") {
		t = strings.TrimSpace(t[7:])
	}
	if t != "" && t == strings.TrimSpace(p.Key) {
		p.BalanceToken = ""
	}
	return p
}

var keyBalanceCache struct {
	sync.Mutex
	at   time.Time
	data []SubscriptionQuota
}

// ForgetBalances has the next KeyBalances ask again, after a provider's
// key or balance token changed.
func ForgetBalances() {
	keyBalanceCache.Lock()
	keyBalanceCache.data = nil
	keyBalanceCache.Unlock()
	// nor is a key's allowance as last read, which may be another key's
	forgetKeyAllowances()
}

// KeyBalances is the balance of every provider magpie can ask one of, the
// key in use and each other key it has on — or its account's, once, when
// it has a token for that — as cards beside the subscriptions' allowances.
// What was asked less than a minute ago is not asked again, unless the
// providers were saved since (ForgetBalances).
func KeyBalances(ctx context.Context) []SubscriptionQuota {
	c := &keyBalanceCache
	_, again := refreshing(ctx) // one card read again (RefreshUsage)
	c.Lock()
	// with allowances read only when asked, the cards stand as they were
	// last read till the user asks (#1518)
	if !again && c.data != nil && (time.Since(c.at) < time.Minute || heldRead(ctx)) {
		defer c.Unlock()
		return c.data
	}
	kept := c.data
	c.Unlock()
	ctx, seq := quotaReading(ctx)
	type job struct {
		p    Provider
		user string
		// own: the key's balance typed by hand, with nothing to ask
		own bool
	}
	var jobs []job
	for _, p := range All() {
		if p.Hidden || p.Off || p.Account != nil || p.Key == "" {
			continue
		}
		src, ok := balanceSourceOf(p)
		zen := !ok && openCodeZen(p)
		if !ok && !clineKeyCard(p) && !zen && !probesBalance(p) {
			// a key with a balance the user typed: what is left of it
			if p.TakesOwnBalance() {
				for _, k := range ownBalanceKeys(p) {
					jobs = append(jobs, job{k.p, k.user, true})
				}
			}
			continue
		}
		others := 0
		if src.token != "" || zen {
			// the account's balance is the same whichever key asks
			jobs = append(jobs, job{p, "", false})
			continue
		}
		for _, k := range p.Keys {
			if !k.Off && k.Key != "" && k.Key != p.Key {
				others++
			}
		}
		// a key on a plan with no balance query of its own (a GLM Coding
		// Plan's, whose windows are its card) has no balance card; a
		// provider read some other way (ClinePass) keeps its card
		keyCard := func(q Provider) bool { return !ok || hasBalanceSource(q) }
		if others == 0 {
			if keyCard(p.atKeyPlan(p.KeyPlan)) {
				jobs = append(jobs, job{p, "", false})
			}
			continue
		}
		first := p.KeyName
		if first == "" {
			first = Mask(p.Key)
		}
		// each key asked at its own plan's host (KeyAccount.Plan):
		// SiliconFlow's China key and its international one
		if q := p.atKeyPlan(p.KeyPlan); keyCard(q) {
			jobs = append(jobs, job{q, first, false})
		}
		for _, k := range p.Keys {
			if k.Off || k.Key == "" || k.Key == p.Key {
				continue
			}
			q := p.atKeyPlan(k.Plan)
			q.Key = k.Key
			if !keyCard(q) {
				continue
			}
			name := k.Name
			if name == "" {
				name = Mask(k.Key)
			}
			jobs = append(jobs, job{q, name, false})
		}
	}
	jobs = slices.DeleteFunc(jobs, func(j job) bool {
		return !wantsCard(ctx, j.p.ID, j.user) && !sharesAskedCard(ctx, kept, j.p.ID, j.user)
	})
	out := make([]SubscriptionQuota, len(jobs))
	var wg sync.WaitGroup
	var slots keySlots
	for i, j := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer slots.take(ctx, j.p.ID)()
			if j.own {
				out[i], _ = ownBalanceCard(j.p, j.p.Key, j.user)
				return
			}
			q := SubscriptionQuota{Provider: j.p.ID, Name: j.p.Name, Icon: j.p.Icon, User: j.user, Windows: []QuotaWindow{}}
			if clineKeyCard(j.p) {
				// ClinePass's limits beside the credits (cline_usage.go)
				if ws, amount, err := clineKeyUsage(ctx, j.p); err != nil {
					// a balance read beside limits that weren't is kept
					q.Error, q.Balance = err.Error(), amount
				} else {
					q.Windows = append(q.Windows, ws...)
					q.Balance = amount
					now := time.Now()
					q.ReadAt = &now
				}
				out[i] = keepReading(ctx, q, keyTag("balance", j.p.Key))
				return
			}
			amount, parts, split, ws, wide, _, err := balanceRead(ctx, j.p)
			if errors.Is(err, errNoBalance) {
				// a reply with no balance for this key, or a relay where
				// none was found: the balance typed for it, else no card
				if j.p.TakesOwnBalance() {
					out[i], _ = ownBalanceCard(j.p, j.p.Key, j.user)
				}
				return
			}
			if errors.Is(err, errOpenCodeNoBalance) {
				// nothing to read or keep: where the balance is shown
				q.Error, q.SeeAt = err.Error(), OpenCodeConsole
				out[i] = q
				return
			}
			var gone siliconFlowRetired
			if errors.As(err, &gone) {
				// the same: a balance read before the query went is stale
				q.Error, q.SeeAt = err.Error(), gone.console
				out[i] = q
				return
			}
			if err != nil {
				q.Error = err.Error()
			} else {
				q.Balance = amount
				q.BalanceParts = cardParts(parts)
				q.BalanceSplit = split
				q.Windows = append(q.Windows, ws...)
				q.wideBalance = wide
				now := time.Now()
				q.ReadAt = &now
				// what routing goes by, read just now rather than in a minute
				noteKeyAllowance(j.p, ws, now)
			}
			// the vendor failing a while shows the balance last read
			out[i] = keepReading(ctx, q, keyTag("balance", j.p.Key))
		}()
	}
	wg.Wait()
	out = slices.DeleteFunc(out, func(q SubscriptionQuota) bool { return q.Provider == "" })
	if ctx.Err() == nil {
		// each balance kept, and drawn over time with its runs-out, on
		// the cards themselves (an estimate is neither: noteBalanceHistory)
		noteBalanceHistory(out, time.Now())
		// the history of the cards read now and of those kept, a partial
		// read (one card's refresh) keeping the others
		h := sharedBalanceHist(slices.Concat(kept, out))
		c.Lock()
		// the keys that read one account's balance, told apart afresh
		// over every card (balance_shared.go)
		c.data = shareBalances(cacheCards(c.data, out, seq, again), h)
		if !again {
			c.at, out = time.Now(), c.data
		} else {
			for i := range out {
				for _, k := range c.data {
					if k.Provider == out[i].Provider && strings.EqualFold(k.User, out[i].User) {
						out[i].SharedBalance = k.SharedBalance
					}
				}
			}
		}
		c.Unlock()
	}
	return out
}

// hasBalanceSource says magpie knows where p's balance is asked.
func hasBalanceSource(p Provider) bool {
	_, ok := balanceSourceOf(p)
	return ok
}

// cardParts are the amounts a balance's card shows each apart, the
// percent a meter: several, or one asked as a percent; nil for one amount
// alone, the figure as it always was.
func cardParts(parts []BalancePart) []BalancePart {
	if len(parts) > 1 || len(parts) == 1 && parts[0].Percent != nil {
		return parts
	}
	return nil
}

// balanceAuthorization is a balance token as its Authorization header: a
// new-api access token goes as it is, a JWT (a sub2api panel's login token)
// as a bearer, as does anything pasted with its scheme already in front.
func balanceAuthorization(token string) string {
	if strings.Contains(token, " ") {
		return token
	}
	if strings.HasPrefix(token, "eyJ") && strings.Count(token, ".") == 2 {
		return "Bearer " + token
	}
	return token
}
