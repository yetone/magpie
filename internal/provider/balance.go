package provider

// What is left on an API key, as the vendor's own balance endpoint tells
// it: DeepSeek, Kimi, OpenRouter, SiliconFlow and AiHubMix are known by their hosts
// (AiHubMix tells the whole account's to its access token, BalanceToken);
// any other provider can name an endpoint and where the amount sits in its
// reply (BalanceURL, BalancePath), the way a relay's own usage query does.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
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
}

// balanceSourceOf is the provider's own endpoint when it named one, else
// the one its host is known to have.
func balanceSourceOf(p Provider) (balanceSource, bool) {
	if p.BalanceURL != "" {
		path := p.BalancePath
		// a token saved beside it (a new-api relay's access token, its
		// /api/user/self telling the account's quota) is asked with
		// instead of the key
		return balanceSource{p.BalanceURL, func(b []byte) (string, error) { return readBalancePath(b, path) }, p.BalanceToken}, true
	}
	hosts := []string{hostOf(p.Chat), hostOf(p.Responses), hostOf(p.Anthropic)}
	for _, h := range hosts {
		switch h {
		case "api.deepseek.com":
			return balanceSource{"https://api.deepseek.com/user/balance", readDeepSeek, ""}, true
		case "api.moonshot.cn":
			return balanceSource{"https://api.moonshot.cn/v1/users/me/balance", readMoonshot("¥"), ""}, true
		case "api.moonshot.ai":
			return balanceSource{"https://api.moonshot.ai/v1/users/me/balance", readMoonshot("$"), ""}, true
		case "openrouter.ai":
			return balanceSource{"https://openrouter.ai/api/v1/credits", readOpenRouter, ""}, true
		case "api.siliconflow.cn":
			return balanceSource{"https://api.siliconflow.cn/v1/user/info", readSiliconFlow("¥"), ""}, true
		case "api.siliconflow.com":
			return balanceSource{"https://api.siliconflow.com/v1/user/info", readSiliconFlow("$"), ""}, true
		case "aihubmix.com":
			if p.BalanceToken != "" {
				return balanceSource{"https://aihubmix.com/api/user/self", readAiHubMixAccount, p.BalanceToken}, true
			}
			return balanceSource{"https://aihubmix.com/dashboard/billing/remain", readAiHubMix, ""}, true
		}
	}
	return balanceSource{}, false
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
	var parts []string
	for _, in := range r.Infos {
		if v, ok := number(in.Total); ok {
			parts = append(parts, money(currencySign(in.Currency), v))
		}
	}
	if len(parts) == 0 {
		return "", errors.New("no balance in the reply")
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
	if !strings.Contains(path, ";") && !strings.Contains(path, ":") {
		return readBalanceOne(b, path)
	}
	var out []string
	for part := range strings.SplitSeq(path, ";") {
		if strings.TrimSpace(part) == "" {
			continue
		}
		label, expr, ok := strings.Cut(part, ":")
		if !ok {
			label, expr = "", part
		}
		label = strings.TrimSpace(label)
		v, err := readBalanceOne(b, expr)
		if err != nil {
			if label != "" {
				err = fmt.Errorf("%s: %w", label, err)
			}
			return "", err
		}
		if label != "" {
			v = label + " " + v
		}
		out = append(out, v)
	}
	if len(out) == 0 {
		return "", errors.New("no balance path: where in the reply the amount is, e.g. data.balance")
	}
	return strings.Join(out, " · "), nil
}

// readBalanceOne is one amount of a balance path.
func readBalanceOne(b []byte, path string) (string, error) {
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
		return "", errors.New("no balance path: where in the reply the amount is, e.g. data.balance")
	}
	var reply any
	if err := json.Unmarshal(b, &reply); err != nil {
		return "", errors.New("the reply is not JSON")
	}
	e := &balanceExpr{src: path, reply: reply}
	if e.lone() {
		// a path alone: its value, a number or not
		v, err := e.at(path)
		if err != nil {
			return "", err
		}
		if n, ok := number(v); ok {
			return balanceAmount(sign, n, percent), nil
		}
		if s, ok := v.(string); ok && s != "" && !percent {
			return sign + s, nil
		}
		return "", fmt.Errorf("%q in the reply is not an amount", path)
	}
	n, err := e.sum()
	if err == nil && e.i < len(e.src) {
		err = fmt.Errorf("the balance path has %q it can't read", e.src[e.i:])
	}
	if err != nil {
		return "", err
	}
	if math.IsInf(n, 0) || math.IsNaN(n) {
		return "", fmt.Errorf("the balance path divides by nothing")
	}
	return balanceAmount(sign, n, percent), nil
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
// false when there is no way to ask it.
func Balance(ctx context.Context, p Provider) (amount string, ok bool, err error) {
	src, ok := balanceSourceOf(p)
	if !ok || p.Account != nil || p.Key == "" {
		return "", false, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src.url, nil)
	if err != nil {
		return "", true, err
	}
	if src.token != "" {
		// a named endpoint still gets the provider's headers, which is
		// where a new-api relay's New-Api-User goes
		if p.BalanceURL != "" {
			for k, v := range p.Headers {
				req.Header.Set(k, v)
			}
		}
		req.Header.Set("Authorization", src.token)
	} else {
		for k, v := range AuthHeaders(p, Chat) {
			req.Header.Set(k, v)
		}
		for k, v := range p.Headers {
			req.Header.Set(k, v)
		}
	}
	req.Header.Set("Accept", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", true, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		// what a JSON reply says, on one line; a page of HTML says nothing
		msg := strings.Join(strings.Fields(string(b)), " ")
		if !strings.HasPrefix(msg, "{") {
			return "", true, errors.New(res.Status)
		}
		if r := []rune(msg); len(r) > 200 {
			msg = string(r[:200]) + "…"
		}
		return "", true, fmt.Errorf("%s: %s", res.Status, msg)
	}
	amount, err = src.read(b)
	return amount, true, err
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
}

// KeyBalances is the balance of every provider magpie can ask one of, the
// key in use and each other key it has on — or its account's, once, when
// it has a token for that — as cards beside the subscriptions' allowances.
// What was asked less than a minute ago is not asked again, unless the
// providers were saved since (ForgetBalances).
func KeyBalances(ctx context.Context) []SubscriptionQuota {
	c := &keyBalanceCache
	c.Lock()
	if c.data != nil && time.Since(c.at) < time.Minute {
		defer c.Unlock()
		return c.data
	}
	c.Unlock()
	type job struct {
		p    Provider
		user string
	}
	var jobs []job
	for _, p := range All() {
		if p.Hidden || p.Account != nil || p.Key == "" {
			continue
		}
		src, ok := balanceSourceOf(p)
		if !ok {
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
	out := make([]SubscriptionQuota, len(jobs))
	var wg sync.WaitGroup
	for i, j := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			q := SubscriptionQuota{Provider: j.p.ID, Name: j.p.Name, Icon: j.p.Icon, User: j.user, Windows: []QuotaWindow{}}
			amount, _, err := Balance(ctx, j.p)
			if err != nil {
				q.Error = err.Error()
			} else {
				q.Balance = amount
			}
			out[i] = q
		}()
	}
	wg.Wait()
	if ctx.Err() == nil {
		c.Lock()
		c.at, c.data = time.Now(), out
		c.Unlock()
	}
	return out
}
