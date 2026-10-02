package provider

// A ZCode subscription is Z.ai's GLM Coding Plan, which ZCode (Zhipu's
// desktop app) signs in to. The plan is served on an Anthropic-compatible
// endpoint to a plain API key, `<id>.<secret>`, that ZCode mints for the
// account and names zcode-api-key; magpie uses that key as ZCode does.
//
// ZCode's own account is read, never changed, from its credential store,
// ~/.zcode/v2/credentials.json: each value is "enc:v1:" + iv.tag.ciphertext
// (base64url), AES-256-GCM under sha256 of $ZCODE_CREDENTIAL_SECRET, or
// else of "zcode-credential-fallback:<platform>:<home>:<user>". Further
// accounts are signed in by magpie with ZCode's own polling sign-in
// (zcode.z.ai/api/v1/oauth/cli/…), and their key is kept in logins.json.
//
// An account with no Coding Plan uses ZCode's free Start Plan instead,
// with ZCode's own session token (zcode_start.go); a seat on a team's GLM
// Coding Plan uses the team project's own key (zcode_team.go).
//
// The plan's models are ZCode's, as its built-in config lists them for the
// coding plan (zcode_models.go); zcodeModels are them before that is read.

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// Where ZCode's coding plan and its sign-in are; vars so tests can point
// them elsewhere.
var (
	ZCodeZaiBase      = "https://api.z.ai/api/anthropic"
	ZCodeBigModelBase = "https://open.bigmodel.cn/api/anthropic"
	zcodeAPI          = "https://zcode.z.ai"
	zcodeZaiAPI       = "https://api.z.ai"
	zcodeBigModelAPI  = "https://bigmodel.cn" // BigModel's business API, where ZCode asks it
)

// zcodeAppVersion is the ZCode whose sign-in magpie makes.
const zcodeAppVersion = "3.14.3"

var zcodeModels = []catalog.Model{
	{ID: "GLM-5.3", Name: "GLM-5.3", Context: 1_000_000, Efforts: []string{"low", "high", "max"}},
	{ID: "GLM-5.3-Flash", Name: "GLM-5.3-Flash", Context: 1_000_000, Efforts: []string{"low", "high", "max"}},
	{ID: "GLM-5.2", Name: "GLM-5.2", Context: 1_000_000, Efforts: []string{"none", "high", "max"}},
	{ID: "GLM-5-Turbo", Name: "GLM-5-Turbo", Context: 200_000, Efforts: []string{"none", "high"}},
}

// zcodeKey is a coding plan's key and where it is served.
type zcodeKey struct {
	Key  string `json:"apiKey,omitempty"`
	Base string `json:"base"`
	JWT  string `json:"jwt,omitempty"` // ZCode's session token, the Start Plan's key (zcode_start.go)
	// A seat on a team's plan (zcode_team.go): the team project it is in,
	// and the account's business sign-in, as its Authorization, for the
	// plan's term and resets. Key is the project's own key, or "" while it
	// is still to be found (ZCode's own account).
	Token   string `json:"token,omitempty"`
	Org     string `json:"org,omitempty"`
	Project string `json:"project,omitempty"`
	// UID is the account's user id, as ZCode's sign-in names it: with the
	// site, which account this is (zcodeSame).
	UID string `json:"uid,omitempty"`
}

// ---- ZCode's own account ------------------------------------------------------

func zcodeCredentialsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".zcode", "v2", "credentials.json")
}

// zcodeSecret is the key ZCode encrypts its credentials with.
func zcodeSecret() []byte {
	seed := os.Getenv("ZCODE_CREDENTIAL_SECRET")
	if seed == "" {
		home, _ := os.UserHomeDir()
		name := ""
		if u, err := user.Current(); err == nil {
			name = u.Username
			if i := strings.LastIndexByte(name, '\\'); i >= 0 {
				name = name[i+1:] // DOMAIN\user; node's userInfo() has the user alone
			}
		}
		platform := runtime.GOOS
		if platform == "windows" {
			platform = "win32"
		}
		seed = "zcode-credential-fallback:" + platform + ":" + home + ":" + name
	}
	sum := sha256.Sum256([]byte(seed))
	return sum[:]
}

func zcodeDecrypt(secret []byte, v string) (string, bool) {
	rest, ok := strings.CutPrefix(v, "enc:v1:")
	if !ok {
		return "", false
	}
	parts := strings.Split(rest, ".")
	if len(parts) != 3 {
		return "", false
	}
	var raw [3][]byte
	for i, p := range parts {
		b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(p, "="))
		if err != nil {
			return "", false
		}
		raw[i] = b
	}
	block, err := aes.NewCipher(secret)
	if err != nil {
		return "", false
	}
	gcm, err := cipher.NewGCMWithNonceSize(block, len(raw[0]))
	if err != nil {
		return "", false
	}
	out, err := gcm.Open(nil, raw[0], append(raw[2], raw[1]...), nil)
	return string(out), err == nil
}

// zcodeOwn is the account ZCode is signed in to, its coding plan's key and
// ZCode's session token (the Start Plan's key); ok is false when it has
// neither.
func zcodeOwn() (who string, k zcodeKey, ok bool) {
	var store map[string]string
	if !readJSON(zcodeCredentialsPath(), &store) {
		return "", zcodeKey{}, false
	}
	secret := zcodeSecret()
	for name, v := range store {
		// account-provider:coding-plan:account:zai-individual-coding-plan:account:<uuid>:api-key
		if !strings.Contains(name, ":coding-plan:") || !strings.HasSuffix(name, ":api-key") {
			continue
		}
		key, ok := zcodeDecrypt(secret, v)
		if !ok || !strings.Contains(key, ".") {
			continue
		}
		base := ZCodeZaiBase
		if strings.Contains(name, ":bigmodel-") {
			base = ZCodeBigModelBase
		}
		// Z.ai's first, as ZCode lists it
		if k.Key == "" || (base == ZCodeZaiBase && k.Base != ZCodeZaiBase) {
			k = zcodeKey{Key: key, Base: base}
		}
	}
	if v, ok := store["zcodejwttoken"]; ok {
		if s, ok := zcodeDecrypt(secret, v); ok {
			k.JWT = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "Bearer "))
		}
	}
	// ZCode switched to a team's plan: that project's key, found when asked
	if t, ok := zcodeOwnTeam(store, secret); ok {
		t.JWT = k.JWT
		k = t
	}
	if k.Key == "" && k.JWT == "" && !k.team() {
		return "", zcodeKey{}, false
	}
	if k.Base == "" {
		k.Base = ZCodeZaiBase
	}
	var info struct {
		Email string `json:"email"`
		Name  string `json:"name"`
		ID    string `json:"user_id"`
	}
	for _, name := range []string{"oauth:zai:user_info", "oauth:bigmodel:user_info"} {
		if v, ok := store[name]; ok && info.Email+info.Name+info.ID == "" {
			if s, ok := zcodeDecrypt(secret, v); ok {
				_ = json.Unmarshal([]byte(s), &info)
			}
		}
	}
	k.UID = strings.TrimSpace(info.ID)
	return zcodeWho(info.Email, info.Name, info.ID), k, true
}

// zcodeWho names a Z.ai account: its email, or the phone number of one
// signed in by phone (its email is then <phone>@phone.local).
func zcodeWho(email, name, id string) string {
	email = strings.TrimSuffix(email, "@phone.local")
	return firstNonEmpty(email, name, id, "ZCode")
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	return ""
}

// ---- the accounts -------------------------------------------------------------

type zcodeLoginKey struct {
	Login
	key zcodeKey
}

func zcodeSaved(l savedLogin) (zcodeKey, bool) {
	var k zcodeKey
	if json.Unmarshal(l.Auth, &k) != nil || k.Key == "" && k.JWT == "" && !k.team() {
		return zcodeKey{}, false
	}
	if k.Base == "" {
		k.Base = ZCodeZaiBase
	}
	return k, true
}

// zcodeLogins is every ZCode account signed in, the first in use first.
func zcodeLogins() []zcodeLoginKey {
	ownUser, own, hasOwn := zcodeOwn()
	if !hasOwn {
		ownUser = ""
	}
	var out []zcodeLoginKey
	for _, l := range sideLogins("zcode", ownUser, func(l savedLogin) bool {
		_, ok := zcodeSaved(l)
		return ok
	}) {
		k := own
		if !l.saved.own() {
			k, _ = zcodeSaved(l.saved)
		}
		out = append(out, zcodeLoginKey{l.Login, k})
	}
	return out
}

func zcodeSide() []sideLogin {
	var out []sideLogin
	for _, z := range zcodeLogins() {
		out = append(out, sideLogin{Login: z.Login})
	}
	return out
}

func zcodeLoginList() []Login { return loginsOf(zcodeSide()) }

func switchZCodeLogin(user string) error { return switchSideLogin("zcode", user, zcodeSide()) }

func setZCodeLoginOn(user string, on bool) error {
	return setSideLoginOn("zcode", user, on, zcodeSide())
}

func forgetZCodeLogin(user string) error {
	return forgetSideLogin("zcode", user, zcodeSide(), nil)
}

func zcodeAccount() (Provider, bool) {
	ls := zcodeLogins()
	if len(ls) == 0 {
		return Provider{}, false
	}
	return zcodeProvider(ls[0].User, ls[0].Plan, ls[0].key), true
}

// zcodeAlsoOn is the ZCode accounts in use behind the first.
func zcodeAlsoOn() []Provider {
	var out []Provider
	for _, l := range zcodeLogins() {
		if !l.Active && l.On {
			out = append(out, zcodeProvider(l.User, l.Plan, l.key))
		}
	}
	return out
}

func zcodeProvider(who, plan string, k zcodeKey) Provider {
	acct := &Account{Agent: "zcode", User: who, Plan: plan}
	// the plan the account is on: its Coding Plan, or ZCode's Start Plan
	// when it has none (zcode_start.go)
	base := func(start bool) string {
		if start {
			return zcodeStartBase()
		}
		return k.Base
	}
	acct.sign = func(ctx context.Context, req *http.Request, body []byte) error {
		start := zcodeOnStart(ctx, k)
		zcodeRebase(req, base(!start), base(start))
		key := k.Key
		if k.team() && key == "" {
			var err error
			if key, err = zcodeTeamKeyOf(ctx, k); err != nil {
				return err
			}
		}
		if start {
			if zcodeJWTExpired(k.JWT) {
				return errZCodeExpired
			}
			key = k.JWT
			zcodeStartRequest(req, body)
		}
		req.Header.Del("Authorization")
		req.Header.Set("x-api-key", key)
		req.Header.Set("Authorization", "Bearer "+key)
		return nil
	}
	acct.explain = func(status int, body []byte) string {
		if zcodeOnStart(nil, k) {
			return zcodeStartExplain(status, body)
		}
		return ""
	}
	acct.models = func() []catalog.Model {
		if zcodeOnStart(nil, k) {
			return zcodeStartModels
		}
		return zcodeModels
	}
	acct.fetch = func(ctx context.Context) ([]catalog.Model, error) {
		b := base(zcodeOnStart(ctx, k))
		ms, err := zcodeFetchModels(ctx, b)
		if err != nil {
			return nil, err
		}
		return ms, catalog.SaveLive("zcode", b, ms)
	}
	return Provider{ID: "zcode", Name: "ZCode", Icon: "zcode", Anthropic: base(zcodeOnStart(nil, k)), Website: "https://zcode.z.ai", Account: acct}
}

// ---- allowance ----------------------------------------------------------------

// zcodeQuota is a coding plan's allowance: credits per five hours and per
// week, as ZCode shows them.
func zcodeQuota(ctx context.Context, l Login, k zcodeKey) SubscriptionQuota {
	if k.team() { // a seat on a team's plan
		return zcodeTeamQuota(ctx, l, k)
	}
	if zcodeOnStart(ctx, k) { // no Coding Plan: ZCode's Start Plan
		return zcodeStartQuota(ctx, l, k.JWT)
	}
	q := SubscriptionQuota{Provider: "zcode", Name: "ZCode", Icon: "zcode", Plan: l.Plan, User: l.User, Windows: []QuotaWindow{}}
	var data zhipuLimits
	if err := zcodeGet(ctx, zcodeRoot(k.Base)+"/api/monitor/usage/quota/limit", k.Key, &data); err != nil {
		q.Error = err.Error()
		return q
	}
	if data.Level != "" {
		q.Plan = "GLM Coding " + strings.ToUpper(data.Level[:1]) + data.Level[1:]
	}
	q.Windows = data.windows()
	q.Until, q.Renew = zhipuTerm(ctx, zcodeRoot(k.Base), k.Key)
	return q
}

// zhipuLimits is what /api/monitor/usage/quota/limit tells: a plan's
// limits, TOKENS_LIMIT on a person's plan and CREDIT_LIMIT on a team's,
// unit 3 the five hours (a team's come with no number) and 6 the week.
type zhipuLimits struct {
	Limits []struct {
		Type      string   `json:"type"`
		Unit      int      `json:"unit"`
		Number    int      `json:"number"`
		Usage     *float64 `json:"usage"`
		Current   *float64 `json:"currentValue"`
		Remaining *float64 `json:"remaining"`
		Percent   *float64 `json:"percentage"`
		Reset     int64    `json:"nextResetTime"`
	} `json:"limits"`
	Level string `json:"level"`
}

// windows are the limits as windows: what is used of the whole when both
// are told (the whole less what remains, or the current value), else the
// percentage the vendor gives. TIME_LIMIT is the month's MCP tool calls,
// which ZCode shows but never stops the models on, so it is set aside, as
// is a limit whose whole is told as 0: no cap (an older plan's), which
// the vendor may still give as 100% used.
func (d zhipuLimits) windows() []QuotaWindow {
	out := []QuotaWindow{}
	for _, x := range d.Limits {
		span := zcodeSpan(x.Unit, x.Number)
		w := QuotaWindow{Name: zcodeWindowName(span), Span: span}
		if strings.EqualFold(x.Type, "TIME_LIMIT") {
			w.Name, w.Aside = "MCP · Month", true
		}
		if x.Percent != nil {
			w.Used = *x.Percent
		}
		if x.Usage != nil && *x.Usage == 0 {
			w.Used, w.Aside = 0, true
		}
		if x.Usage != nil && *x.Usage > 0 {
			total := *x.Usage
			switch {
			case x.Remaining != nil:
				used := total - *x.Remaining
				w.Used = 100 * used / total
				w.Display = fmt.Sprintf("%s / %s", compactNumber(used), compactNumber(total))
			case x.Current != nil:
				if x.Percent == nil {
					w.Used = 100 * *x.Current / total
				}
				w.Display = fmt.Sprintf("%s / %s", compactNumber(*x.Current), compactNumber(total))
			}
		}
		if x.Reset > 0 {
			t := time.UnixMilli(x.Reset)
			w.ResetsAt = &t
		}
		out = append(out, w)
	}
	return out
}

// zhipuTerm is how long a GLM Coding plan is paid for, from the
// account's subscriptions as ZCode reads them: the valid one's next
// renewal, a charge when it renews itself, else the end of its time — or
// the last date its "valid" span names.
func zhipuTerm(ctx context.Context, root, key string) (*time.Time, string) {
	var subs []zhipuSubscription
	if zcodeGet(ctx, root+"/api/biz/subscription/list", key, &subs) != nil {
		return nil, ""
	}
	return zhipuTermOf(subs)
}

type zhipuSubscription struct {
	Status    string `json:"status"`
	Valid     string `json:"valid"`
	AutoRenew any    `json:"autoRenew"` // true or 1
	NextRenew string `json:"nextRenewTime"`
}

// zhipuDate finds the dates in a span like "2026-09-18 12:00:00-2026-10-18 12:00:00".
var zhipuDate = regexp.MustCompile(`\d{4}-\d{2}-\d{2}(?:[ T]\d{2}:\d{2}:\d{2})?`)

// zhipuTime reads the plan's times, "2026-10-18 12:00:00" in Beijing.
func zhipuTime(s string) *time.Time {
	s = strings.Replace(strings.TrimSpace(s), " ", "T", 1)
	cst := time.FixedZone("CST", 8*3600)
	for _, f := range []string{"2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.ParseInLocation(f, s, cst); err == nil {
			return &t
		}
	}
	return nil
}

func zhipuTermOf(subs []zhipuSubscription) (*time.Time, string) {
	for _, s := range subs {
		if !strings.EqualFold(s.Status, "VALID") {
			continue
		}
		auto := s.AutoRenew == true || s.AutoRenew == float64(1)
		if t := zhipuTime(s.NextRenew); t != nil {
			if auto {
				return t, "auto"
			}
			return t, "off"
		}
		if ds := zhipuDate.FindAllString(s.Valid, -1); len(ds) > 0 && !auto {
			if t := zhipuTime(ds[len(ds)-1]); t != nil {
				return t, "off"
			}
		}
		return nil, ""
	}
	return nil, ""
}

// zcodeSpan reads a limit's window: unit 3 counts hours, 6 weeks (and
// 4, 5 days and months by the same count). A team's five hours come with
// no count.
func zcodeSpan(unit, n int) time.Duration {
	if unit == 3 && n <= 0 {
		n = 5
	}
	n = max(n, 1)
	switch unit {
	case 1:
		return time.Duration(n) * time.Minute
	case 3:
		return time.Duration(n) * time.Hour
	case 4:
		return time.Duration(n) * 24 * time.Hour
	case 5:
		return time.Duration(n) * 30 * 24 * time.Hour
	case 6:
		return time.Duration(n) * 7 * 24 * time.Hour
	}
	return 0
}

func zcodeWindowName(span time.Duration) string {
	switch {
	case span == 0:
		return "Credits"
	case span < 24*time.Hour:
		return fmt.Sprintf("%d hours", int(span.Hours()))
	case span == 7*24*time.Hour:
		return "Weekly"
	case span >= 28*24*time.Hour:
		return "Monthly"
	}
	return fmt.Sprintf("%d days", int(span.Hours()/24))
}

// zcodeRoot is the site a plan's endpoint is on: https://api.z.ai.
func zcodeRoot(base string) string {
	if u, err := url.Parse(base); err == nil && u.Host != "" {
		return u.Scheme + "://" + u.Host
	}
	return zcodeZaiAPI
}

func zcodeLoginQuota(ctx context.Context, l Login) SubscriptionQuota {
	for _, z := range zcodeLogins() {
		if strings.EqualFold(z.User, l.User) {
			return zcodeQuota(ctx, l, z.key)
		}
	}
	return SubscriptionQuota{Provider: "zcode", Plan: l.Plan, Windows: []QuotaWindow{}, Error: "not signed in"}
}

// zcodePlan names the coding plan a key has, "" for none.
func zcodePlan(ctx context.Context, k zcodeKey) (string, error) {
	var subs []struct {
		Product string `json:"productName"`
		Status  string `json:"status"`
	}
	if err := zcodeGet(ctx, zcodeRoot(k.Base)+"/api/biz/subscription/list", k.Key, &subs); err != nil {
		return "", err
	}
	for _, s := range subs {
		if strings.EqualFold(s.Status, "VALID") {
			return s.Product, nil
		}
	}
	return "", nil
}

// ---- Z.ai's API ---------------------------------------------------------------

// zcodeCall asks one of Z.ai's JSON endpoints, which wrap what they say in
// {code, msg, data}: code 0 or 200 is a success.
func zcodeCall(ctx context.Context, method, u, auth string, body, dst any) error {
	return zcodeCallH(ctx, method, u, auth, nil, body, dst)
}

// zcodeCallH is zcodeCall with more headers: a team's organization and
// project.
func zcodeCallH(ctx context.Context, method, u, auth string, hdr map[string]string, body, dst any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "ZCode/"+zcodeAppVersion)
	zcodeDeviceHeader(req)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var env struct {
		Code json.RawMessage `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		if json.Unmarshal(b, &env) == nil && env.Msg != "" {
			if c := strings.Trim(string(env.Code), `"`); c != "" && c != "null" && c != "0" {
				return fmt.Errorf("%s (%d, code %s)", env.Msg, res.StatusCode, c)
			}
			return fmt.Errorf("%s (%d)", env.Msg, res.StatusCode)
		}
		return &accountStatusError{status: res.StatusCode}
	}
	if err := json.Unmarshal(b, &env); err != nil {
		return err
	}
	if c := strings.Trim(string(env.Code), `"`); c != "" && c != "0" && c != "200" && c != "null" {
		return errors.New(firstNonEmpty(env.Msg, "error "+c))
	}
	if dst == nil || len(env.Data) == 0 || string(env.Data) == "null" {
		return nil
	}
	return json.Unmarshal(env.Data, dst)
}

// zcodeGet asks with a coding plan's key, which goes bare in Authorization.
func zcodeGet(ctx context.Context, u, key string, dst any) error {
	return zcodeCall(ctx, http.MethodGet, u, key, nil, dst)
}

// zcodeSite is the site a key's account is on: "bigmodel" or "zai".
func zcodeSite(k zcodeKey) string {
	if k.Base == ZCodeBigModelBase || strings.HasSuffix(hostOf(k.Base), "bigmodel.cn") {
		return "bigmodel"
	}
	return "zai"
}

// zcodeSame says two keys are the same account: on the same site, with the
// same user id; one kept before magpie kept ids is told by its key's id,
// and taken for the same when it has none, as it was before.
func zcodeSame(a, b zcodeKey) bool {
	if zcodeSite(a) != zcodeSite(b) {
		return false
	}
	if a.UID != "" && b.UID != "" {
		return a.UID == b.UID
	}
	ia, _, _ := strings.Cut(a.Key, ".")
	ib, _, _ := strings.Cut(b.Key, ".")
	return ia == "" || ib == "" || ia == ib
}

// zcodeName is the name an account signed in is kept under: who, as ZCode
// names it, unless another account is kept under that name, then who and
// its site, numbered when that is taken too. Accounts are kept by name, so
// a Z.ai and a BigModel account on the same phone number, or two ZCode
// names alike, took each other's place (Bandit on Discord). The same
// account signed in again keeps its name and is updated in place.
func zcodeName(who, site string, k zcodeKey, have []zcodeLoginKey) string {
	if k.UID != "" {
		for _, l := range have {
			if l.key.UID == k.UID && zcodeSite(l.key) == site {
				return l.User
			}
		}
	}
	for n := 1; ; n++ {
		name := who
		if n == 2 {
			name = who + " (" + zcodeSiteName(site) + ")"
		} else if n > 2 {
			name = fmt.Sprintf("%s (%s %d)", who, zcodeSiteName(site), n-1)
		}
		i := slices.IndexFunc(have, func(l zcodeLoginKey) bool { return strings.EqualFold(l.User, name) })
		if i < 0 || zcodeSame(have[i].key, k) {
			return name
		}
	}
}

// ---- signing in ---------------------------------------------------------------

// startZCodeSignIn is ZCode's own polling sign-in: zcode.z.ai opens a flow,
// the browser signs in to Z.ai — or to BigModel (智谱, bigmodel.cn) when
// site is "bigmodel" — and the flow is asked until it is ready. The
// account's coding plan key is then found or made, as ZCode does it.
func startZCodeSignIn(s *signInFlow, site string) error {
	if site != "bigmodel" {
		site = "zai"
	}
	ctx, cancel := context.WithCancel(context.Background())
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	poll := "Bearer " + hex.EncodeToString(b)
	var flow struct {
		ID       string  `json:"flow_id"`
		URL      string  `json:"authorize_url"`
		Expires  float64 `json:"expires_at"`
		Interval float64 `json:"poll_interval_sec"`
	}
	if err := zcodeCall(ctx, http.MethodPost, zcodeAPI+"/api/v1/oauth/cli/init", poll, map[string]string{"provider": site}, &flow); err != nil {
		cancel()
		return fmt.Errorf("ZCode sign-in: %w", err)
	}
	u, err := url.Parse(flow.URL)
	if flow.ID == "" || err != nil || u.Scheme != "https" {
		cancel()
		return errors.New("ZCode gave no sign-in page")
	}
	// where Z.ai or BigModel sends the browser back, as ZCode sets it
	// (BigModel's page names it redirect)
	back := zcodeAPI + "/app/oauth/login?" + url.Values{"redirect": {"zcode://oauth/callback"}, "app_version": {zcodeAppVersion}}.Encode()
	q := u.Query()
	if site == "bigmodel" {
		q.Set("redirect", back)
	} else {
		q.Set("redirect_uri", back)
	}
	u.RawQuery = q.Encode()
	s.mu.Lock()
	s.st.URL = u.String()
	s.stop = cancel
	s.mu.Unlock()
	interval := time.Duration(max(flow.Interval, 1)) * time.Second
	deadline := time.Unix(int64(flow.Expires), 0)
	if flow.Expires == 0 {
		deadline = time.Now().Add(5 * time.Minute)
	}
	go func() {
		defer cancel()
		fail := func(msg string) { s.finish(SignInState{State: "failed", Error: msg}) }
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(interval):
			}
			if time.Now().After(deadline) {
				fail("the sign-in expired; start again")
				return
			}
			var got struct {
				Status string `json:"status"`
				Token  string `json:"token"` // ZCode's own session: the Start Plan's key
				Zai    struct {
					AccessToken string `json:"access_token"`
				} `json:"zai"`
				BigModel struct {
					AccessToken string `json:"access_token"`
					Camel       string `json:"accessToken"`
				} `json:"bigmodel"`
				User struct {
					ID    string `json:"user_id"`
					Email string `json:"email"`
					Name  string `json:"name"`
				} `json:"user"`
			}
			err := zcodeCall(ctx, http.MethodGet, zcodeAPI+"/api/v1/oauth/cli/poll/"+url.PathEscape(flow.ID), poll, nil, &got)
			token := strings.TrimSpace(got.Zai.AccessToken)
			if site == "bigmodel" {
				token = firstNonEmpty(got.BigModel.AccessToken, got.BigModel.Camel)
			}
			switch {
			case ctx.Err() != nil:
				return
			case err != nil:
				var st *accountStatusError
				if errors.As(err, &st) && st.status >= 400 && st.status < 500 && st.status != 408 && st.status != 429 {
					fail("ZCode sign-in: " + err.Error())
					return
				}
				continue // a hiccup: ask again
			case got.Status == "pending" || got.Status == "":
				continue
			case got.Status == "failed":
				fail("the sign-in was declined on " + zcodeSiteName(site))
				return
			case got.Status != "ready" || token == "":
				fail("ZCode sign-in: unexpected answer " + got.Status)
				return
			}
			k, plan, err := zcodeSignedIn(ctx, site, token, strings.TrimSpace(got.Token))
			if err != nil {
				fail(err.Error())
				return
			}
			k.UID = strings.TrimSpace(got.User.ID)
			ownUser, own, ok := zcodeOwn()
			if !ok {
				ownUser = ""
			}
			have := zcodeLogins()
			if ok && !slices.ContainsFunc(have, func(l zcodeLoginKey) bool { return strings.EqualFold(l.User, ownUser) }) {
				have = append(have, zcodeLoginKey{Login{User: ownUser}, own}) // removed in magpie, still ZCode's
			}
			who := zcodeName(zcodeWho(got.User.Email, got.User.Name, got.User.ID), site, k, have)
			auth, _ := json.Marshal(k)
			if err := addSideLogin(savedLogin{Agent: "zcode", User: who, Plan: plan, Auth: auth}, ownUser, func(savedLogin) {}); err != nil {
				fail(err.Error())
				return
			}
			s.finish(SignInState{State: "done", User: who, Plan: plan, Using: ok && strings.EqualFold(ownUser, who)})
			return
		}
	}()
	return nil
}

// zcodeSignedIn is what a sign-in gives magpie: the account's coding plan
// key and plan; or, with none of its own, a seat on a team's plan
// (zcode_team.go); or else ZCode's token for its Start Plan while the
// account has one. site is "zai" or "bigmodel", where it signed in.
func zcodeSignedIn(ctx context.Context, site, token, jwt string) (zcodeKey, string, error) {
	var (
		k    zcodeKey
		plan string
		err  error
		team zcodeTeamRefusal
	)
	root := zcodeSiteAPI(site)
	auth, err := zcodeBizAuth(ctx, site, token)
	if err == nil {
		var info zcodeCustomer
		if info, err = zcodeCustomerInfo(ctx, root, auth, site); err == nil {
			k, plan, err = zcodeMintKey(ctx, site, root, auth, info)
			if err == nil && plan != "" {
				k.JWT = jwt
				return k, plan, nil
			}
			// none of its own: a team's, as ZCode lists them
			t, tplan, why, terr := zcodeTeamSignIn(ctx, site, root, auth, info)
			if terr == nil {
				t.JWT = jwt
				return t, tplan, nil
			}
			team = why
			if why == "" && len(info.teamProjects()) > 0 {
				team = zcodeTeamRefusal(terr.Error())
			}
		}
	}
	// the Start Plan's balance not read is said as it is, not taken for
	// the account having none (#282: a 400 "parameter error" read as that)
	var berr error
	if jwt != "" {
		var b zcodeBalance
		if b, berr = zcodeStartBalance(ctx, jwt); berr == nil {
			if name, _, ok := b.active(); ok {
				if err != nil || k.Key == "" { // no key made: the Start Plan alone
					k = zcodeKey{Base: zcodeSiteBase(site)}
				}
				k.JWT = jwt
				return k, name, nil
			}
		}
	}
	switch {
	case team != "" && berr != nil:
		return zcodeKey{}, "", fmt.Errorf("%s; ZCode's Start Plan: %v", team, berr)
	case team != "":
		return zcodeKey{}, "", errors.New(string(team))
	case err != nil && berr != nil:
		return zcodeKey{}, "", fmt.Errorf("%w; ZCode's Start Plan: %v", err, berr)
	case err != nil:
		return zcodeKey{}, "", err
	case berr != nil:
		return zcodeKey{}, "", fmt.Errorf("this %s account has no GLM Coding Plan, of its own or a team's, and ZCode's Start Plan could not be read: %v", zcodeSiteName(site), berr)
	}
	return zcodeKey{}, "", fmt.Errorf("this %s account has no GLM Coding Plan, of its own or a team's, and ZCode's Start Plan has ended or was never started — subscribe at %s, then add it again", zcodeSiteName(site), zcodeSubscribeAt(site))
}

// zcodeSiteName, zcodeSiteAPI, zcodeSiteBase and zcodeSubscribeAt are a
// sign-in's site: Z.ai, or BigModel (智谱), whose business API ZCode asks
// at bigmodel.cn and whose plan is served at open.bigmodel.cn.
func zcodeSiteName(site string) string {
	if site == "bigmodel" {
		return "BigModel"
	}
	return "Z.ai"
}

func zcodeSiteAPI(site string) string {
	if site == "bigmodel" {
		return zcodeBigModelAPI
	}
	return zcodeZaiAPI
}

func zcodeSiteBase(site string) string {
	if site == "bigmodel" {
		return ZCodeBigModelBase
	}
	return ZCodeZaiBase
}

func zcodeSubscribeAt(site string) string {
	if site == "bigmodel" {
		return "bigmodel.cn/glm-coding"
	}
	return "z.ai/subscribe"
}

// zcodeBizAuth is the Authorization a sign-in gives the business API: for
// Z.ai its token exchanged for a business one, as a Bearer; BigModel's
// token is that already, and goes bare (createBigModelBizHeaders).
func zcodeBizAuth(ctx context.Context, site, token string) (string, error) {
	if site == "bigmodel" {
		return token, nil
	}
	var biz struct {
		Token string `json:"access_token"`
	}
	if err := zcodeCall(ctx, http.MethodPost, zcodeZaiAPI+"/api/auth/z/login", "", map[string]string{"token": token}, &biz); err != nil || biz.Token == "" {
		return "", fmt.Errorf("Z.ai sign-in: %v", firstErr(err, "no token"))
	}
	return "Bearer " + biz.Token, nil
}

// zcodeCustomer is an account's organizations and their projects; a
// project of type 2 is a team's coding plan (isBigModelTeamCodingPlanProject).
type zcodeCustomer struct {
	Orgs []struct {
		ID       string `json:"organizationId"`
		Name     string `json:"organizationName"`
		Projects []struct {
			ID   string `json:"projectId"`
			Name string `json:"projectName"`
			Type any    `json:"projectType"`
		} `json:"projects"`
	} `json:"organizations"`
}

func zcodeCustomerInfo(ctx context.Context, root, auth, site string) (zcodeCustomer, error) {
	var info zcodeCustomer
	if err := zcodeCall(ctx, http.MethodGet, root+"/api/biz/customer/getCustomerInfo", auth, nil, &info); err != nil {
		return info, fmt.Errorf("%s account: %w", zcodeSiteName(site), err)
	}
	return info, nil
}

// zcodeMintKey is the account's own coding plan key: the key named
// zcode-api-key in its default project, made if it isn't there, with its
// secret; and the plan it has, "" for none.
func zcodeMintKey(ctx context.Context, site, root, auth string, info zcodeCustomer) (zcodeKey, string, error) {
	// the default organization and project, as ZCode picks them
	org, proj := "", ""
	for _, o := range info.Orgs {
		var ps []string
		def := ""
		for _, p := range o.Projects {
			if p.ID == "" || fmt.Sprint(p.Type) == "2" {
				continue
			}
			ps = append(ps, p.ID)
			if def == "" && strings.Contains(p.Name, "默认项目") {
				def = p.ID
			}
		}
		if o.ID == "" || len(ps) == 0 {
			continue
		}
		if def == "" {
			def = ps[0]
		}
		if org == "" || strings.Contains(o.Name, "默认机构") {
			org, proj = o.ID, def
			if strings.Contains(o.Name, "默认机构") {
				break
			}
		}
	}
	if org == "" {
		return zcodeKey{}, "", fmt.Errorf("this %s account has no project for an API key", zcodeSiteName(site))
	}
	keys := root + "/api/biz/v1/organization/" + url.PathEscape(org) + "/projects/" + url.PathEscape(proj) + "/api_keys"
	key, err := zcodeProjectKey(ctx, site, keys, auth, nil, map[string]any{"name": "zcode-api-key"})
	if err != nil {
		return zcodeKey{}, "", err
	}
	k := zcodeKey{Key: key, Base: zcodeSiteBase(site)}
	plan, err := zcodePlan(ctx, k)
	if err != nil {
		return zcodeKey{}, "", fmt.Errorf("GLM Coding Plan: %w", err)
	}
	return k, plan, nil // plan "" when it has none: zcodeSignedIn looks further
}

// zcodeProjectKey finds the key a project has under want's name (and
// keyType, when want has one), makes it if it isn't there, and reads its
// secret: `<id>.<secret>`.
func zcodeProjectKey(ctx context.Context, site, keys, auth string, hdr map[string]string, want map[string]any) (string, error) {
	type apiKey struct {
		Name    string `json:"name"`
		APIKey  string `json:"apiKey"`
		KeyType any    `json:"keyType"`
	}
	name := fmt.Sprint(want["name"])
	kind, typed := want["keyType"]
	var list []apiKey
	if err := zcodeCallH(ctx, http.MethodGet, keys, auth, hdr, nil, &list); err != nil {
		return "", fmt.Errorf("%s API keys: %w", zcodeSiteName(site), err)
	}
	id := ""
	for _, k := range list {
		if k.Name == name && (!typed || fmt.Sprint(k.KeyType) == fmt.Sprint(kind)) && strings.TrimSpace(k.APIKey) != "" {
			id = strings.TrimSpace(k.APIKey)
		}
	}
	if id == "" {
		var made apiKey
		if err := zcodeCallH(ctx, http.MethodPost, keys, auth, hdr, want, &made); err != nil {
			return "", fmt.Errorf("%s API key: %w", zcodeSiteName(site), err)
		}
		id = strings.TrimSpace(made.APIKey)
	}
	var secret struct {
		Secret string `json:"secretKey"`
	}
	if id != "" {
		if err := zcodeCallH(ctx, http.MethodGet, keys+"/copy/"+url.PathEscape(id), auth, hdr, nil, &secret); err != nil {
			return "", fmt.Errorf("%s API key: %w", zcodeSiteName(site), err)
		}
	}
	if id != "" && strings.TrimSpace(secret.Secret) == "" && typed {
		return id, nil // a team's key with no secret goes as it is (copyBigModelTeamPlanProjectApiKeySecret)
	}
	if id == "" || strings.TrimSpace(secret.Secret) == "" {
		return "", fmt.Errorf("%s gave no API key", zcodeSiteName(site))
	}
	return id + "." + strings.TrimSpace(secret.Secret), nil
}

func firstErr(err error, otherwise string) string {
	if err != nil {
		return err.Error()
	}
	return otherwise
}
