package provider

// PLUGIN-SERVED (see AGENTS.md): Zed ("zed") is a deprecated built-in
// subscription served by its plugin, @magpie-community/opencode-zed-auth,
// once moved onto it (provider.Moved; the default for a new sign-in). A
// moved one's sign-ins, models, requests and usage are all the plugin's,
// never this code's (only the move, in migrate*.go, still reads its
// accounts). A fix here alone doesn't reach those users; fix the plugin
// (github.com/magpie-community/plugins, packages/zed) and raise the mover's
// min in internal/provider/migrate_zed.go.

// A Zed subscription is the account the Zed editor signs in to (Zed Pro, its
// trial, a student or business plan): the models Zed hosts — Anthropic's,
// OpenAI's, Google's and xAI's — asked through cloud.zed.dev with a
// short-lived token the account's sign-in is traded for.
//
// The sign-in is Zed's own, run by magpie (zed_signin.go) and kept in
// logins.json; the models are served by the gateway (gateway/zed.go), which
// writes each request in its model's own provider's API. The protocol lives
// in internal/zed.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/zed"
)

// Where Zed's API and sign-in page are; vars so tests can point them
// elsewhere.
var (
	zedCloud = zed.CloudURL
	zedSite  = zed.SiteURL
)

var zedClient = &http.Client{Timeout: 20 * time.Second}

// ErrZedSignIn is an account Zed no longer takes: it must be signed in again.
var ErrZedSignIn = errors.New("the Zed sign-in has expired — sign in again")

// zedCreds is what a Zed sign-in leaves: the account's id and access token
// (which last until Zed refuses them), the machine id they were issued with,
// the organization its models are asked under, and the model list last read.
type zedCreds struct {
	UserID   string          `json:"userId"`
	Access   string          `json:"accessToken"`
	SystemID string          `json:"systemId"`
	Org      string          `json:"organizationId,omitempty"`
	Login    string          `json:"login,omitempty"`
	Name     string          `json:"name,omitempty"`
	Plan     string          `json:"plan,omitempty"` // plan_v3, e.g. zed_pro
	Models   json.RawMessage `json:"models,omitempty"`
}

func zedSaved(l savedLogin) (zedCreds, bool) {
	var c zedCreds
	if json.Unmarshal(l.Auth, &c) != nil || c.UserID == "" || c.Access == "" {
		return zedCreds{}, false
	}
	return c, true
}

type zedLogin struct {
	Login
	creds zedCreds
}

// zedLogins is every Zed account signed in, the one in use first.
func zedLogins() []zedLogin {
	var out []zedLogin
	for _, l := range sideLogins("zed", "", func(l savedLogin) bool {
		_, ok := zedSaved(l)
		return ok
	}) {
		c, _ := zedSaved(l.saved)
		a := zedLogin{Login: l.Login, creds: c}
		a.Lapsed = l.saved.Lapsed
		out = append(out, a)
	}
	return out
}

func zedLoginList() []Login { return loginsOf(zedSide()) }

func zedSide() []sideLogin {
	var out []sideLogin
	for _, l := range zedLogins() {
		out = append(out, sideLogin{Login: l.Login})
	}
	return out
}

func switchZedLogin(user string) error { return switchSideLogin("zed", user, zedSide()) }

func setZedLoginOn(user string, on bool) error { return setSideLoginOn("zed", user, on, zedSide()) }

func forgetZedLogin(user string) error {
	if err := forgetSideLogin("zed", user, zedSide(), nil); err != nil {
		return err
	}
	zedTokens.forget(user)
	return nil
}

// zedLookup reads one saved account.
func zedLookup(user string) (zedCreds, bool) {
	loginsMu.Lock()
	defer loginsMu.Unlock()
	for _, l := range readLogins() {
		if l.Agent == "zed" && strings.EqualFold(l.User, user) {
			return zedSaved(l)
		}
	}
	return zedCreds{}, false
}

// zedEdit changes one saved account's credentials.
func zedEdit(user string, fn func(c *zedCreds, l *savedLogin)) error {
	return editSideLogin("zed", user, func(ls []savedLogin, i int) ([]savedLogin, error) {
		c, ok := zedSaved(ls[i])
		if !ok {
			return nil, errors.New("Zed: unreadable sign-in")
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

// zedLapse records that Zed refused an account's sign-in.
func zedLapse(user string) error {
	msg := user + "'s Zed sign-in has expired — sign in again"
	_ = editSideLogin("zed", user, func(ls []savedLogin, i int) ([]savedLogin, error) {
		ls[i].Lapsed = msg
		return ls, nil
	})
	return fmt.Errorf("%s: %w", user, ErrZedSignIn)
}

// zedRefused is Zed turning the account's own sign-in away.
func zedRefused(err error) bool {
	var st *zed.HTTPStatusError
	return errors.As(err, &st) && st.StatusCode == http.StatusUnauthorized
}

// ---- the model token ----------------------------------------------------------

// zedTokens keeps each account's LLM token in memory: Zed mints them for an
// hour or so and says when one is stale, so none is written down.
var zedTokens = &zedTokenCache{m: map[string]string{}}

type zedTokenCache struct {
	mu sync.Mutex // held while one is minted, so two requests don't both mint
	m  map[string]string
}

func (t *zedTokenCache) forget(user string) {
	t.mu.Lock()
	delete(t.m, strings.ToLower(user))
	t.mu.Unlock()
}

// ZedToken is a live model token for an account, minted anew when renew is
// set (the last one was refused) or none is kept.
func ZedToken(ctx context.Context, user string, renew bool) (string, error) {
	t := zedTokens
	t.mu.Lock()
	defer t.mu.Unlock()
	key := strings.ToLower(user)
	if tok := t.m[key]; tok != "" && !renew {
		return tok, nil
	}
	c, ok := zedLookup(user)
	if !ok {
		return "", fmt.Errorf("no Zed account %q", user)
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	tok, err := zed.LLMToken(ctx, zedClient, zedCloud, c.UserID, c.Access, c.SystemID, c.Org)
	if err != nil {
		delete(t.m, key)
		if zedRefused(err) {
			return "", zedLapse(user)
		}
		return "", err
	}
	t.m[key] = tok
	return tok, nil
}

// ZedCloud is where the gateway asks an account's models.
func ZedCloud() string { return zedCloud }

// ZedProviderOf is the provider (anthropic, open_ai, google, x_ai) whose API
// a model is asked in, as the account's list said, else guessed from its id.
func ZedProviderOf(user, model string) string {
	if c, ok := zedLookup(user); ok {
		if p := zed.ProviderOf(c.Models, model); p != "" {
			return p
		}
	}
	return zed.GuessProvider(model)
}

// ---- models -----------------------------------------------------------------

func zedFetchModels(ctx context.Context, user string) ([]catalog.Model, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var raw []byte
	for try := 0; ; try++ {
		tok, err := ZedToken(ctx, user, try > 0)
		if err != nil {
			return nil, err
		}
		var h http.Header
		raw, h, err = zed.FetchModels(ctx, zedClient, zedCloud, tok)
		var st *zed.HTTPStatusError
		if err != nil && try == 0 && errors.As(err, &st) && zed.TokenStale(st.StatusCode, h) {
			continue
		}
		if err != nil {
			return nil, err
		}
		break
	}
	ms, err := zed.ParseModels(raw)
	if errors.Is(err, zed.ErrNoModels) {
		return nil, errors.New("Zed lists no models for this account — its plan may not include Zed's hosted models (see zed.dev/account)")
	}
	if err != nil {
		return nil, err
	}
	if err := zedEdit(user, func(c *zedCreds, _ *savedLogin) { c.Models = raw }); err != nil {
		return nil, err
	}
	return ms, catalog.SaveLive("zed", zedCloud, ms)
}

// ---- the provider -----------------------------------------------------------

func zedProvider(l zedLogin) Provider {
	user := l.User
	a := &Account{Agent: "zed", User: user, Plan: l.Plan, Stream: true}
	a.models = func() []catalog.Model {
		c, _ := zedLookup(user)
		ms, _ := zed.ParseModels(c.Models)
		return ms
	}
	a.fetch = func(ctx context.Context) ([]catalog.Model, error) { return zedFetchModels(ctx, user) }
	return Provider{ID: "zed", Name: "Zed", Icon: "zed", Website: "https://zed.dev", Account: a}
}

func zedAccount() (Provider, bool) {
	ls := zedLogins()
	if len(ls) == 0 {
		return Provider{}, false
	}
	return zedProvider(ls[0]), true
}

// zedAlsoOn is the Zed accounts in use behind the first.
func zedAlsoOn() []Provider {
	var out []Provider
	for _, l := range zedLogins() {
		if !l.Active && l.On {
			out = append(out, zedProvider(l))
		}
	}
	return out
}
