package provider

// What is left on an API key, as the vendor's own balance endpoint tells
// it: DeepSeek, Kimi, OpenRouter, SiliconFlow, StepFun, Command Code and AiHubMix are known by their hosts
// (AiHubMix tells the whole account's to its access token, BalanceToken);
// any other provider can name an endpoint and where the amount sits in its
// reply (BalanceURL, BalancePath), the way a relay's own usage query does,
// with {key} where it wants the key in the URL (withBalanceKey).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
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
}

// balanceSourceOf is the provider's own endpoint when it named one, else
// the one its host is known to have.
func balanceSourceOf(p Provider) (balanceSource, bool) {
	if p.BalanceURL != "" {
		path := balancePathOf(p)
		// a token saved beside it (a new-api relay's access token, its
		// /api/user/self telling the account's quota) is asked with
		// instead of the key
		return balanceSource{p.BalanceURL, func(b []byte) (string, error) { return readBalancePath(b, path) }, p.BalanceToken, nil}, true
	}
	hosts := []string{hostOf(p.Chat), hostOf(p.Responses), hostOf(p.Anthropic)}
	for _, h := range hosts {
		switch h {
		case "api.deepseek.com":
			return balanceSource{"https://api.deepseek.com/user/balance", readDeepSeek, "", nil}, true
		case "api.moonshot.cn":
			return balanceSource{"https://api.moonshot.cn/v1/users/me/balance", readMoonshot("¥"), "", nil}, true
		case "api.moonshot.ai":
			return balanceSource{"https://api.moonshot.ai/v1/users/me/balance", readMoonshot("$"), "", nil}, true
		case "openrouter.ai":
			return balanceSource{"https://openrouter.ai/api/v1/credits", readOpenRouter, "", nil}, true
		case "api.siliconflow.cn":
			return balanceSource{"https://api.siliconflow.cn/v1/user/info", readSiliconFlow("¥"), "", siliconFlowGone("https://cloud.siliconflow.cn (余额充值 → 代金券)")}, true
		case "api.siliconflow.com":
			return balanceSource{"https://api.siliconflow.com/v1/user/info", readSiliconFlow("$"), "", siliconFlowGone("https://cloud.siliconflow.com")}, true
		case "api.stepfun.com":
			return balanceSource{"https://api.stepfun.com/v1/accounts", readStepFun("¥"), "", nil}, true
		case "api.stepfun.ai":
			return balanceSource{"https://api.stepfun.ai/v1/accounts", readStepFun("$"), "", nil}, true
		case "api.commandcode.ai":
			return balanceSource{"https://api.commandcode.ai/alpha/billing/credits", readCommandCode, "", nil}, true
		case "aihubmix.com":
			if p.BalanceToken != "" {
				return balanceSource{"https://aihubmix.com/api/user/self", readAiHubMixAccount, p.BalanceToken, nil}, true
			}
			return balanceSource{"https://aihubmix.com/dashboard/billing/remain", readAiHubMix, "", nil}, true
		}
	}
	return balanceSource{}, false
}

// balancePathOf is the balance field of a provider that named its Balance
// URL.
func balancePathOf(p Provider) string {
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
	return fmt.Errorf("the Balance URL %s takes the API key, not the access token: set it to %s (Balance field %s) for the account's balance, or remove the token for the key's own", raw, self, newAPIQuotaPath)
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
	return "add the header New-Api-User = your user ID (shown in the site's personal settings) to this provider's Headers; the relay said: " + msg
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

// readDeepSeek: {"is_available":true,"balance_infos":[{"currency":"CNY","total_balance":"110.00",…}]}
func readDeepSeek(b []byte) (string, error) {
	var r struct {
		Infos []struct {
			Currency string `json:"currency"`
			Total    any    `json:"total_balance"`
		} `json:"balance_infos"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return "", err
	}
	type amount struct {
		code string
		v    float64
	}
	var amts []amount
	for _, in := range r.Infos {
		if v, ok := number(in.Total); ok {
			amts = append(amts, amount{strings.ToUpper(in.Currency), v})
		}
	}
	if len(amts) == 0 {
		return "", errors.New("no balance in the reply")
	}
	// DeepSeek lists an account's currencies (CNY, USD) in an order that
	// changes from one read to the next (gakki on Discord), so they are
	// put in one of magpie's own: a currency with money left first, then
	// by its code. The first is the one the trend follows and an alert
	// reads (balanceSeries, BalanceNumber), so it mustn't swap places
	// between reads either.
	slices.SortStableFunc(amts, func(a, b amount) int {
		if (a.v > 0) != (b.v > 0) {
			if a.v > 0 {
				return -1
			}
			return 1
		}
		return strings.Compare(a.code, b.code)
	})
	parts := make([]string, len(amts))
	for i, a := range amts {
		parts[i] = money(currencySign(a.code), a.v)
	}
	return strings.Join(parts, " · "), nil
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
// it has no API for the balance in its place yet, and the balance given
// before 2025-12 is a voucher now, so both are read on the console. A key
// refused for itself (401 30014) is still said as it came.
func siliconFlowGone(console string) func(int, []byte) error {
	return func(status int, b []byte) error {
		var r struct {
			Code int `json:"code"`
		}
		_ = json.Unmarshal(b, &r)
		if status != http.StatusGone && r.Code != 20092 {
			return nil
		}
		return fmt.Errorf("SiliconFlow retired its balance query (/v1/user/info) and has no API for it in its place yet; the balance and vouchers are on %s", console)
	}
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
// number is shown as it is. Several amounts, each with a label if wanted,
// go apart by ";" and are shown together: "5h: windowLimits.fiveHour.used /
// windowLimits.fiveHour.cap %; week: …; $credits.monthlyCredits" is
// "5h 0% · week 3.4% · $70.00".
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
			if label != "" {
				err = fmt.Errorf("%s: %w", label, err)
			}
			return nil, err
		}
		out = append(out, BalancePart{Label: label, Text: v, Percent: pct})
	}
	if len(out) == 0 {
		return nil, errors.New("no balance path: where in the reply the amount is, e.g. data.balance")
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
	n, err := e.sum()
	if err == nil && e.i < len(e.src) {
		err = fmt.Errorf("the balance path has %q it can't read", e.src[e.i:])
	}
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
// + - * / and brackets.
type balanceExpr struct {
	src   string
	i     int
	reply any
}

func (e *balanceExpr) space() {
	for e.i < len(e.src) && e.src[e.i] == ' ' {
		e.i++
	}
}

// lone is whether the whole is one path.
func (e *balanceExpr) lone() bool {
	return !strings.ContainsAny(e.src, "+-*/() ") && e.src != "" && !isDigit(e.src[0])
}

func (e *balanceExpr) sum() (float64, error) {
	v, err := e.product()
	for err == nil {
		e.space()
		if e.i >= len(e.src) || (e.src[e.i] != '+' && e.src[e.i] != '-') {
			break
		}
		op := e.src[e.i]
		e.i++
		var w float64
		if w, err = e.product(); op == '+' {
			v += w
		} else {
			v -= w
		}
	}
	return v, err
}

func (e *balanceExpr) product() (float64, error) {
	v, err := e.unary()
	for err == nil {
		e.space()
		if e.i >= len(e.src) || (e.src[e.i] != '*' && e.src[e.i] != '/') {
			break
		}
		op := e.src[e.i]
		e.i++
		var w float64
		if w, err = e.unary(); err != nil {
			break
		}
		if op == '*' {
			v *= w
		} else if w == 0 {
			return 0, errors.New("the balance path divides by 0")
		} else {
			v /= w
		}
	}
	return v, err
}

func (e *balanceExpr) unary() (float64, error) {
	e.space()
	if e.i >= len(e.src) {
		return 0, errors.New("the balance path ends where a number or a path should be")
	}
	switch c := e.src[e.i]; {
	case c == '-':
		e.i++
		v, err := e.unary()
		return -v, err
	case c == '(':
		e.i++
		v, err := e.sum()
		if err != nil {
			return 0, err
		}
		e.space()
		if e.i >= len(e.src) || e.src[e.i] != ')' {
			return 0, errors.New("the balance path has a ( without its )")
		}
		e.i++
		return v, nil
	case isDigit(c) || c == '.':
		j := e.i
		for j < len(e.src) && (isDigit(e.src[j]) || e.src[j] == '.') {
			j++
		}
		n, err := strconv.ParseFloat(e.src[e.i:j], 64)
		if err != nil {
			return 0, fmt.Errorf("the balance path has %q, not a number", e.src[e.i:j])
		}
		e.i = j
		return n, nil
	}
	j := e.i
	for j < len(e.src) && !strings.ContainsRune("+-*/() ", rune(e.src[j])) {
		j++
	}
	if j == e.i {
		return 0, fmt.Errorf("the balance path has %q where a number or a path should be", e.src[e.i:])
	}
	path := e.src[e.i:j]
	e.i = j
	v, err := e.at(path)
	if err != nil {
		return 0, err
	}
	n, ok := number(v)
	if !ok {
		return 0, fmt.Errorf("%q in the reply is not a number", path)
	}
	return n, nil
}

// at is what is at a dotted path in the reply.
func (e *balanceExpr) at(path string) (any, error) {
	v := e.reply
	for _, k := range strings.Split(path, ".") {
		switch x := v.(type) {
		case map[string]any:
			v = x[k]
		case []any:
			i, err := strconv.Atoi(k)
			if err != nil || i < 0 || i >= len(x) {
				return nil, fmt.Errorf("nothing at %q in the reply", path)
			}
			v = x[i]
		default:
			v = nil
		}
		if v == nil {
			return nil, fmt.Errorf("nothing at %q in the reply", path)
		}
	}
	return v, nil
}

// Balance asks the vendor what is left on the provider's key in use. ok is
// false when there is no way to ask it. A key given limits and no quota of
// its own has what its fullest window has left: what it can spend now.
func Balance(ctx context.Context, p Provider) (amount string, ok bool, err error) {
	amount, _, ws, ok, err := balanceRead(ctx, p)
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
// the user wrote each apart (nil for a vendor magpie reads itself), and the
// key's own usage windows when its reply tells them (a sub2api key's
// limits, readSub2APIKeyLimits).
func balanceRead(ctx context.Context, p Provider) (amount string, parts []BalancePart, ws []QuotaWindow, ok bool, err error) {
	ctx = p.Via(ctx)
	src, ok := balanceSourceOf(p)
	if !ok || p.Account != nil || p.Key == "" {
		return "", nil, nil, false, nil
	}
	if src.token != "" && p.BalanceURL != "" && balanceURLPath(p.BalanceURL) == newAPIKeyUsage {
		return "", nil, nil, true, errKeyUsageWithToken(p.BalanceURL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, withBalanceKey(src.url, p.Key, true), nil)
	if err != nil {
		return "", nil, nil, true, err
	}
	if src.token != "" {
		// a named endpoint still gets the provider's headers, which is
		// where a new-api relay's New-Api-User goes
		if p.BalanceURL != "" {
			for k, v := range p.Headers {
				req.Header.Set(k, withBalanceKey(v, p.Key, false))
			}
		}
		req.Header.Set("Authorization", balanceAuthorization(src.token))
	} else {
		for k, v := range AuthHeaders(p, Chat) {
			req.Header.Set(k, v)
		}
		for k, v := range p.Headers {
			req.Header.Set(k, withBalanceKey(v, p.Key, false))
		}
	}
	req.Header.Set("Accept", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		if k := url.QueryEscape(p.Key); k != "" && strings.Contains(err.Error(), k) {
			// the URL in the error has the key in it: not shown on a card
			return "", nil, nil, true, errors.New(strings.ReplaceAll(err.Error(), k, Mask(p.Key)))
		}
		return "", nil, nil, true, err
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
		return "", nil, nil, true, errors.New(msg)
	}
	if res.StatusCode >= 300 && src.failed != nil {
		if err := src.failed(res.StatusCode, b); err != nil {
			return "", nil, nil, true, err
		}
	}
	if res.StatusCode >= 300 {
		// what a JSON reply says, on one line; a page of HTML says nothing
		msg := strings.Join(strings.Fields(string(b)), " ")
		if !strings.HasPrefix(msg, "{") {
			return "", nil, nil, true, errors.New(res.Status)
		}
		if r := []rune(msg); len(r) > 200 {
			msg = string(r[:200]) + "…"
		}
		return "", nil, nil, true, fmt.Errorf("%s: %s", res.Status, msg)
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
				return "", nil, ws, true, nil
			}
			return "", nil, nil, true, err
		}
		return joinBalanceParts(parts), parts, ws, true, nil
	}
	amount, err = src.read(b)
	return amount, nil, nil, true, err
}

// sub2APIKeyLimits says the provider's Balance URL is a sub2api panel's
// query for a key, whose reply tells the key's own limits when it has
// some.
func sub2APIKeyLimits(p Provider) bool {
	return p.BalanceURL != "" && balanceURLPath(p.BalanceURL) == sub2APIKeyUsage
}

// balanceKeyNames are what a Balance URL or a header's value names the
// key by, for a vendor that wants it somewhere of its own, in the query
// most often (…/balance?key={key}): each key the provider has on is put in
// its own ask, so each card tells that key's balance.
var balanceKeyNames = []string{"{key}", "{apiKey}", "{api_key}"}

// withBalanceKey is s with the key in place of its names, escaped for a
// URL's query when inURL.
func withBalanceKey(s, key string, inURL bool) string {
	if inURL {
		key = url.QueryEscape(key)
	}
	for _, n := range balanceKeyNames {
		s = strings.ReplaceAll(s, n, key)
	}
	return s
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
	if !again && c.data != nil && time.Since(c.at) < time.Minute {
		defer c.Unlock()
		return c.data
	}
	c.Unlock()
	ctx, seq := quotaReading(ctx)
	type job struct {
		p    Provider
		user string
	}
	var jobs []job
	for _, p := range All() {
		if p.Hidden || p.Off || p.Account != nil || p.Key == "" {
			continue
		}
		src, ok := balanceSourceOf(p)
		if !ok && !clineKeyCard(p) {
			continue
		}
		others := 0
		if src.token != "" {
			// the account's balance is the same whichever key asks
			jobs = append(jobs, job{p, ""})
			continue
		}
		for _, k := range p.Keys {
			if !k.Off && k.Key != "" && k.Key != p.Key {
				others++
			}
		}
		if others == 0 {
			jobs = append(jobs, job{p, ""})
			continue
		}
		first := p.KeyName
		if first == "" {
			first = Mask(p.Key)
		}
		jobs = append(jobs, job{p, first})
		for _, k := range p.Keys {
			if k.Off || k.Key == "" || k.Key == p.Key {
				continue
			}
			q := p
			q.Key = k.Key
			name := k.Name
			if name == "" {
				name = Mask(k.Key)
			}
			jobs = append(jobs, job{q, name})
		}
	}
	jobs = slices.DeleteFunc(jobs, func(j job) bool { return !wantsCard(ctx, j.p.ID, j.user) })
	out := make([]SubscriptionQuota, len(jobs))
	var wg sync.WaitGroup
	for i, j := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			q := SubscriptionQuota{Provider: j.p.ID, Name: j.p.Name, Icon: j.p.Icon, User: j.user, Windows: []QuotaWindow{}}
			if clineKeyCard(j.p) {
				// ClinePass's limits beside the credits (cline_usage.go)
				if ws, amount, err := clineKeyUsage(ctx, j.p); err != nil {
					q.Error = err.Error()
				} else {
					q.Windows = append(q.Windows, ws...)
					q.Balance = amount
					now := time.Now()
					q.ReadAt = &now
				}
				out[i] = keepReading(ctx, q, keyTag("balance", j.p.Key))
				return
			}
			amount, parts, ws, _, err := balanceRead(ctx, j.p)
			if err != nil {
				q.Error = err.Error()
			} else {
				q.Balance = amount
				q.BalanceParts = cardParts(parts)
				q.Windows = append(q.Windows, ws...)
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
	if ctx.Err() == nil {
		// each balance kept, and drawn over time with its runs-out
		noteBalanceHistory(out, time.Now())
		c.Lock()
		c.data = cacheCards(c.data, out, seq, again)
		if !again {
			c.at, out = time.Now(), c.data
		}
		c.Unlock()
	}
	return out
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
