package provider

// PLUGIN-SERVED (see AGENTS.md): Xiaomi MiMo ("mimo-app") is a deprecated
// built-in subscription served by its plugin,
// @magpie-community/opencode-mimo-auth, once moved onto it (provider.Moved;
// the default for a new sign-in). A moved one's sign-ins, models, requests
// and usage are all the plugin's, never this code's (only the move, in
// migrate*.go, still reads its accounts). A fix here alone doesn't reach
// those users; fix the plugin (github.com/magpie-community/plugins,
// packages/mimo) and raise the mover's min in
// internal/provider/migrate_mimo.go.

// A MiMo subscription is the Xiaomi account Xiaomi MiMo (the desktop app,
// mimo-ai.xiaomimimo.com/desktop) signs in to: MiMo Pro and MiMo Flash,
// free for a while to any account and on a plan's weekly allowance after,
// asked through the MiMo server's own route in OpenAI's chat completions.
//
// The app has no token of its own: it signs in to the Xiaomi account in a
// browser window and uses the cookies Xiaomi's sign-on leaves on the MiMo
// server (serviceToken, userId). magpie does the same without a window: the
// sign-in is Xiaomi's QR / long-poll one (mimo_signin.go), whose passToken
// renews that session as the app's own window does — the MiMo server sends
// an unsigned request to account.xiaomi.com, which sends it back signed in.
// Usage is the server's /user/usage and plan (mimo_usage.go).

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// MiMoID is the MiMo subscription's id, as a provider and in logins.json.
const MiMoID = "mimo-app"

// Where MiMo's servers and Xiaomi's sign-on are; vars so tests can point
// them elsewhere. The app picks the server by the account's region
// (/user/xiaomi/me) and starts at Singapore's.
var (
	mimoHosts = map[string]string{
		"SGP": "https://mimo-server-sgp.xiaomimimo.com/api",
		"RU":  "https://mimo-server-ru.xiaomimimo.com/api",
		"IN":  "https://mimo-server-in.xiaomimimo.com/api",
	}
	mimoAccountSite = "https://account.xiaomi.com"
)

const (
	mimoDefaultRegion = "SGP"
	// mimoAppVersion is the Xiaomi MiMo whose requests magpie makes: the
	// app sends it as X-Client-Version on everything it asks its server.
	mimoAppVersion = "26.929.292248"
	// mimoSource is what the app marks its model requests with.
	mimoSource = "mimocode-cli-free"
	// mimoUA is the app's sign-in window's, which its session sends to
	// Xiaomi and to the MiMo server alike.
	mimoUA = "miNative PC/Normal Windows_NT/10.0.26100 SDKV/1.0.0 DEVT/PC DEVS/Windows APP/miaccount_desktop APPV/0.1.0"
	// mimoRenewAfter is how old a session gets before it is renewed: the
	// app takes a sign-in to last 48 hours and looks again at half that.
	mimoRenewAfter = 24 * time.Hour
)

var mimoClient = &http.Client{Timeout: 30 * time.Second}

// ErrMiMoSignIn is an account Xiaomi no longer signs in: it must be signed
// in again.
var ErrMiMoSignIn = errors.New("the Xiaomi MiMo sign-in has expired — sign in again")

// mimoModels are the app's own two, as it lists them for its engine
// (mimo-auto, the app's default, is asked as mimo-pro).
var mimoModels = []catalog.Model{
	{ID: "mimo-pro", Name: "MiMo Pro", Released: "2026-07-01", Images: true, Context: 1_000_000, Output: 128_000},
	{ID: "mimo-flash", Name: "MiMo Flash", Released: "2026-07-01", Images: true, Context: 1_000_000, Output: 128_000},
}

// mimoCreds is what a MiMo sign-in leaves: the Xiaomi account's id and
// passToken (which signs it on again), the device id they were issued to,
// the MiMo server it is served at and the session cookies that server set,
// and when those were set.
type mimoCreds struct {
	UserID    string            `json:"userId"`
	CUserID   string            `json:"cUserId,omitempty"`
	PassToken string            `json:"passToken"`
	DeviceID  string            `json:"deviceId"`
	Region    string            `json:"region,omitempty"`
	Base      string            `json:"base"`
	Cookies   map[string]string `json:"cookies,omitempty"`
	Issued    time.Time         `json:"issued"`
	Name      string            `json:"name,omitempty"`
}

func mimoSaved(l savedLogin) (mimoCreds, bool) {
	var c mimoCreds
	if json.Unmarshal(l.Auth, &c) != nil || c.UserID == "" || c.PassToken == "" || c.Base == "" {
		return mimoCreds{}, false
	}
	return c, true
}

type mimoLogin struct {
	Login
	creds mimoCreds
}

// mimoLogins is every MiMo account signed in, the one in use first.
func mimoLogins() []mimoLogin {
	var out []mimoLogin
	for _, l := range sideLogins(MiMoID, "", func(l savedLogin) bool {
		_, ok := mimoSaved(l)
		return ok
	}) {
		c, _ := mimoSaved(l.saved)
		a := mimoLogin{Login: l.Login, creds: c}
		a.Lapsed = l.saved.Lapsed
		out = append(out, a)
	}
	return out
}

func mimoLoginList() []Login { return loginsOf(mimoSide()) }

func mimoSide() []sideLogin {
	var out []sideLogin
	for _, l := range mimoLogins() {
		out = append(out, sideLogin{Login: l.Login})
	}
	return out
}

func switchMiMoLogin(user string) error { return switchSideLogin(MiMoID, user, mimoSide()) }

func setMiMoLoginOn(user string, on bool) error {
	return setSideLoginOn(MiMoID, user, on, mimoSide())
}

func forgetMiMoLogin(user string) error {
	return forgetSideLogin(MiMoID, user, mimoSide(), nil)
}

// mimoLookup reads one saved account.
func mimoLookup(user string) (mimoCreds, bool) {
	loginsMu.Lock()
	defer loginsMu.Unlock()
	for _, l := range readLogins() {
		if l.Agent == MiMoID && strings.EqualFold(l.User, user) {
			return mimoSaved(l)
		}
	}
	return mimoCreds{}, false
}

// mimoEdit changes one saved account's credentials.
func mimoEdit(user string, fn func(c *mimoCreds, l *savedLogin)) error {
	return editSideLogin(MiMoID, user, func(ls []savedLogin, i int) ([]savedLogin, error) {
		c, ok := mimoSaved(ls[i])
		if !ok {
			return nil, errors.New("Xiaomi MiMo: unreadable sign-in")
		}
		fn(&c, &ls[i])
		b, err := json.Marshal(c)
		if err != nil {
			return nil, err
		}
		ls[i].Auth, ls[i].Seen = b, time.Now().UTC()
		return ls, nil
	})
}

// mimoLapse records that Xiaomi refused to sign an account on again.
func mimoLapse(user string) error {
	msg := user + "'s Xiaomi MiMo sign-in has expired — sign in again"
	_ = editSideLogin(MiMoID, user, func(ls []savedLogin, i int) ([]savedLogin, error) {
		ls[i].Lapsed = msg
		return ls, nil
	})
	return fmt.Errorf("%s: %w", user, ErrMiMoSignIn)
}

func mimoDeviceID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "pc_" + hex.EncodeToString(b)
}

// mimoBaseOf is the server for a region, "" when MiMo has none there.
func mimoBaseOf(region string) string {
	return mimoHosts[strings.ToUpper(strings.TrimSpace(region))]
}

// ---- the session ----------------------------------------------------------------

// mimoMe is the MiMo server's /user/xiaomi/me: who the session is.
type mimoMe struct {
	Code int `json:"code"`
	Data struct {
		UserID   json.Number `json:"userId"`
		Nickname string      `json:"nickname"`
		NickName string      `json:"nickName"`
		Name     string      `json:"name"`
		Display  string      `json:"displayName"`
		UserName string      `json:"userName"`
		Region   string      `json:"region"`
		Country  string      `json:"country"`
	} `json:"data"`
}

func (m mimoMe) name() string {
	d := m.Data
	return firstNonEmpty(d.Nickname, d.NickName, d.Name, d.Display, d.UserName)
}

// mimoRejected are the codes the MiMo server turns an account away with
// (the app's own list): signed in, but not served.
func mimoRejected(status, code int) bool {
	return status == http.StatusForbidden || code == 403 || code == 46109
}

// mimoSession signs an account on at a MiMo server as the app's window
// does: the server's /user/xiaomi/me, asked with no session, sends the
// browser to account.xiaomi.com's serviceLogin, which — holding the
// account's passToken — sends it back through the server's /sts, where
// the session cookies are set, to /user/xiaomi/me again. The jar holds
// what each hop sets; the MiMo server's cookies are the session.
func mimoSession(ctx context.Context, c mimoCreds, base string) (map[string]string, mimoMe, error) {
	jar, _ := cookiejar.New(nil)
	acct, err := url.Parse(mimoAccountSite)
	if err != nil {
		return nil, mimoMe{}, err
	}
	var seed []*http.Cookie
	for _, kv := range [][2]string{{"userId", c.UserID}, {"passToken", c.PassToken}, {"cUserId", c.CUserID},
		{"deviceId", c.DeviceID}, {"pass_ua", "pc"}, {"uLocale", "zh_CN"}} {
		if kv[1] != "" {
			seed = append(seed, &http.Cookie{Name: kv[0], Value: kv[1], Path: "/"})
		}
	}
	jar.SetCookies(acct, seed)
	bu, err := url.Parse(base)
	if err != nil {
		return nil, mimoMe{}, err
	}
	client := &http.Client{Timeout: mimoClient.Timeout, Jar: jar, Transport: mimoClient.Transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("Xiaomi MiMo sign-in: too many redirects")
			}
			// the server's followup is written http://; its session
			// cookies are only sent back over https
			if req.URL.Scheme == "http" && bu.Scheme == "https" && strings.EqualFold(req.URL.Host, bu.Host) {
				req.URL.Scheme = "https"
			}
			return nil
		}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/user/xiaomi/me", nil)
	if err != nil {
		return nil, mimoMe{}, err
	}
	mimoHeaders(req)
	res, err := client.Do(req)
	if err != nil {
		return nil, mimoMe{}, fmt.Errorf("Xiaomi MiMo sign-in: %w", err)
	}
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	res.Body.Close()
	var me mimoMe
	if json.Unmarshal(b, &me) != nil {
		// Xiaomi's sign-in page, not the server's answer: the passToken
		// no longer signs the account on
		return nil, mimoMe{}, ErrMiMoSignIn
	}
	if mimoRejected(res.StatusCode, me.Code) {
		return nil, me, errors.New("Xiaomi MiMo doesn't serve this Xiaomi account (its region isn't served here)")
	}
	if me.Code != 0 || me.Data.UserID.String() == "" {
		return nil, me, ErrMiMoSignIn
	}
	cookies := map[string]string{}
	for _, ck := range jar.Cookies(bu) {
		cookies[ck.Name] = ck.Value
	}
	if len(cookies) == 0 {
		return nil, me, errors.New("Xiaomi MiMo sign-in: the server set no session")
	}
	return cookies, me, nil
}

// mimoHeaders are what the app's session sends its server.
func mimoHeaders(req *http.Request) {
	req.Header.Set("User-Agent", mimoUA)
	req.Header.Set("X-Client-Version", mimoAppVersion)
}

// mimoRenewing keeps two requests from renewing one account at once.
var mimoRenewing sync.Mutex

// mimoFresh is an account's credentials with a live session, renewed when
// it is older than the app lets one get, or when force is set (the server
// turned the last one away).
func mimoFresh(ctx context.Context, user string, force bool) (mimoCreds, error) {
	c, ok := mimoLookup(user)
	if !ok {
		return mimoCreds{}, fmt.Errorf("no Xiaomi MiMo account %q", user)
	}
	if !force && len(c.Cookies) > 0 && time.Since(c.Issued) < mimoRenewAfter {
		return c, nil
	}
	mimoRenewing.Lock()
	defer mimoRenewing.Unlock()
	// renewed by another request while this one waited
	if n, ok := mimoLookup(user); ok && n.Issued.After(c.Issued) && len(n.Cookies) > 0 {
		return n, nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	cookies, _, err := mimoSession(ctx, c, c.Base)
	if errors.Is(err, ErrMiMoSignIn) {
		return mimoCreds{}, mimoLapse(user)
	}
	if err != nil {
		if len(c.Cookies) > 0 && !force {
			return c, nil // a hiccup: the session in hand may still do
		}
		return mimoCreds{}, err
	}
	c.Cookies, c.Issued = cookies, time.Now().UTC()
	if err := mimoEdit(user, func(s *mimoCreds, l *savedLogin) {
		s.Cookies, s.Issued = c.Cookies, c.Issued
		l.Lapsed = ""
	}); err != nil {
		return mimoCreds{}, err
	}
	return c, nil
}

// mimoCookie is a session as a Cookie header, in a steady order.
func mimoCookie(cookies map[string]string) string {
	var parts []string
	for _, k := range []string{"serviceToken", "userId", "cUserId"} {
		if v, ok := cookies[k]; ok {
			parts = append(parts, k+"="+v)
		}
	}
	for k, v := range cookies {
		if k != "serviceToken" && k != "userId" && k != "cUserId" {
			parts = append(parts, k+"="+v)
		}
	}
	return strings.Join(parts, "; ")
}

// mimoSign makes a request the app's session would: its cookies, no
// Authorization (the app strips the engine's placeholder key), its source
// and version.
func mimoSign(req *http.Request, c mimoCreds) {
	req.Header.Del("Authorization")
	req.Header.Set("Cookie", mimoCookie(c.Cookies))
	req.Header.Set("X-Mimo-Source", mimoSource)
	mimoHeaders(req)
}

// mimoGet asks the MiMo server for one of the account's pages, renewing
// the session once when it is turned away, and decodes its data.
func mimoGet(ctx context.Context, user, path string, data any) error {
	for try := 0; ; try++ {
		c, err := mimoFresh(ctx, user, try > 0)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.Base, "/")+path, nil)
		if err != nil {
			return err
		}
		mimoSign(req, c)
		req.Header.Del("X-Mimo-Source")
		res, err := mimoClient.Do(req)
		if err != nil {
			return err
		}
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		var env struct {
			Code int             `json:"code"`
			Msg  string          `json:"msg"`
			Data json.RawMessage `json:"data"`
		}
		bad := json.Unmarshal(b, &env) != nil
		if res.StatusCode == http.StatusUnauthorized || (res.StatusCode == http.StatusOK && bad) {
			// a session gone stale, or a redirect to Xiaomi's sign-in
			if try == 0 {
				continue
			}
			return mimoLapse(user)
		}
		if res.StatusCode != http.StatusOK || bad {
			return fmt.Errorf("Xiaomi MiMo %s: %s", path, APIError(b, res.Status))
		}
		if env.Code != 0 {
			return fmt.Errorf("Xiaomi MiMo %s: code %d %s", path, env.Code, env.Msg)
		}
		if data == nil {
			return nil
		}
		return json.Unmarshal(env.Data, data)
	}
}

// mimoBody asks mimo-auto, the app's default, as the model it stands for.
func mimoBody(body []byte) []byte {
	if !bytes.Contains(body, []byte(`"mimo-auto"`)) {
		return body
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if dec.Decode(&m) != nil || m["model"] != "mimo-auto" {
		return body
	}
	m["model"] = "mimo-pro"
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

// ---- the provider -----------------------------------------------------------

func mimoProvider(l mimoLogin) Provider {
	user := l.User
	a := &Account{Agent: MiMoID, User: user, Plan: l.Plan, Stream: true}
	a.body = mimoBody
	a.sign = func(ctx context.Context, req *http.Request, _ []byte) error {
		c, err := mimoFresh(ctx, user, false)
		if err != nil {
			return err
		}
		mimoSign(req, c)
		return nil
	}
	a.models = func() []catalog.Model { return mimoModels }
	return Provider{ID: MiMoID, Name: "Xiaomi MiMo", Icon: "mimocode", Chat: strings.TrimRight(l.creds.Base, "/") + "/route",
		Website: "https://mimo-ai.xiaomimimo.com", Account: a}
}

func mimoAccount() (Provider, bool) {
	ls := mimoLogins()
	if len(ls) == 0 {
		return Provider{}, false
	}
	return mimoProvider(ls[0]), true
}

// mimoAlsoOn is the MiMo accounts in use behind the first.
func mimoAlsoOn() []Provider {
	var out []Provider
	for _, l := range mimoLogins() {
		if !l.Active && l.On {
			out = append(out, mimoProvider(l))
		}
	}
	return out
}
