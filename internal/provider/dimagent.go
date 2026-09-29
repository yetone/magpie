package provider

// A DimAgent subscription is the account DimAgent (dimagent.cn) signs in to:
// an OpenAI-compatible chat endpoint, a model listing, and an allowance, all
// served under one OAuth access token.
//
// The sign-in is DimAgent desktop's own — its public OAuth client, its PKCE,
// and the fixed localhost:54321 callback — run by magpie and kept in
// logins.json; the token is renewed as the app renews it, and a renewal the
// upstream refuses is remembered against the account as lapsed.
//
// Because the upstream speaks chat completions, a DimAgent account needs no
// backend of its own in the gateway: its provider carries the base URL and
// the sign, and magpie's ordinary relay and translation do the rest.
// The protocol lives in internal/dimagent.

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
	"github.com/yetone/magpie/internal/dimagent"
)

// Where DimAgent's site is; a var so tests can point it elsewhere.
var dimagentAPI = dimagent.DefaultBaseURL

var (
	// dimagentClient carries every DimAgent round, and dimagentMu serializes
	// checking, rotating and saving tokens: two requests refreshing one
	// account would spend the same refresh token twice.
	dimagentClient = &http.Client{Timeout: 20 * time.Second}
	dimagentMu     sync.Mutex
)

// dimagentRefreshLead is how long before a token lapses it is renewed. The
// upstream's tokens run for days, so a day's head start is the app's own
// interval; magpie only notices at a request, a model list, or a quota.
const dimagentRefreshLead = 24 * time.Hour

// dimagentCreds is an account's tokens, what its token said who it is, and
// the model listing last read from it.
type dimagentCreds struct {
	UID       string `json:"uid"`
	Access    string `json:"accessToken"`
	Refresh   string `json:"refreshToken"`
	ExpiresAt int64  `json:"expiresAt"` // unix ms, when the access token lapses
	Sub       string `json:"sub,omitempty"`
	Nickname  string `json:"nickname,omitempty"`
	// Plan is what the account's token claims said it is subscribed to; the
	// usage reply carries no name for it, so this is where it comes from.
	Plan string `json:"plan,omitempty"`
	// Models is /v1/models?type=dim as the account last answered it, kept so
	// the picker has its names, windows and efforts between fetches.
	Models json.RawMessage `json:"models,omitempty"`
}

// dimagentLogin is one signed-in account with what magpie saved of it.
type dimagentLogin struct {
	Login
	creds dimagentCreds
}

// dimagentWho names an account: the nickname its token carries, its subject,
// its id, or the vendor, so an account is never left nameless.
func dimagentWho(c dimagentCreds) string {
	return firstNonEmpty(strings.TrimSpace(c.Nickname), c.Sub, c.UID, "DimAgent")
}

func dimagentSaved(l savedLogin) (dimagentCreds, bool) {
	var c dimagentCreds
	if json.Unmarshal(l.Auth, &c) != nil || c.Access == "" {
		return dimagentCreds{}, false
	}
	return c, true
}

// dimagentLogins is every DimAgent account signed in, the first in use first.
func dimagentLogins() []dimagentLogin {
	var out []dimagentLogin
	for _, l := range sideLogins("dimagent", "", func(l savedLogin) bool {
		_, ok := dimagentSaved(l)
		return ok
	}) {
		c, _ := dimagentSaved(l.saved)
		a := dimagentLogin{Login: l.Login, creds: c}
		a.Lapsed = l.saved.Lapsed // a refused renewal shows on the account
		out = append(out, a)
	}
	return out
}

func dimagentLoginList() []Login { return loginsOf(dimagentSide()) }

func dimagentSide() []sideLogin {
	var out []sideLogin
	for _, l := range dimagentLogins() {
		out = append(out, sideLogin{Login: l.Login})
	}
	return out
}

func switchDimAgentLogin(user string) error {
	return switchSideLogin("dimagent", user, dimagentSide())
}

func setDimAgentLoginOn(user string, on bool) error {
	return setSideLoginOn("dimagent", user, on, dimagentSide())
}

func forgetDimAgentLogin(user string) error {
	return forgetSideLogin("dimagent", user, dimagentSide(), nil)
}

// ---- keeping the sign-in --------------------------------------------------

// dimagentEdit saves c over the account named user, keeping what it had of
// its model list and identity when the new tokens say less. The caller holds
// dimagentMu, so no second refresh is writing the same account.
func dimagentEdit(user string, c dimagentCreds, renewed bool) error {
	return editSideLogin("dimagent", user, func(ls []savedLogin, i int) ([]savedLogin, error) {
		old, ok := dimagentSaved(ls[i])
		if !ok {
			return nil, errors.New("DimAgent: unreadable sign-in")
		}
		if len(c.Models) == 0 {
			c.Models = old.Models
		}
		for name, got := range map[string]*string{"uid": &c.UID, "sub": &c.Sub, "nickname": &c.Nickname, "plan": &c.Plan} {
			if *got == "" {
				*got = old.field(name)
			}
		}
		b, err := json.Marshal(c)
		if err != nil {
			return nil, err
		}
		ls[i].Auth, ls[i].Seen = b, time.Now().UTC()
		ls[i].Plan = c.Plan // the plan the account's token names, kept in the list too
		if renewed {
			ls[i].Renewed, ls[i].Lapsed = time.Now().UTC(), ""
		}
		return ls, nil
	})
}

// field is one of the identity claims a saved account carries, by name.
func (c dimagentCreds) field(name string) string {
	switch name {
	case "uid":
		return c.UID
	case "sub":
		return c.Sub
	case "nickname":
		return c.Nickname
	case "plan":
		return c.Plan
	}
	return ""
}

// isExpiredRefusal is the upstream saying this sign-in is gone: only a 401
// or 403 ends an account, a hiccup doesn't. The status travels typed on the
// error from the round that read it, as every other subscription carries one,
// so an account isn't ended on a wording that happens to name a code.
func isExpiredRefusal(err error) bool {
	var st *dimagent.HTTPStatusError
	return errors.As(err, &st) &&
		(st.StatusCode == http.StatusUnauthorized || st.StatusCode == http.StatusForbidden)
}

// dimagentLapse records that DimAgent refused an account's refresh token.
func dimagentLapse(user string, err error) error {
	if !isExpiredRefusal(err) {
		return err
	}
	msg := user + "'s DimAgent sign-in has expired — sign in again"
	_ = editSideLogin("dimagent", user, func(ls []savedLogin, i int) ([]savedLogin, error) {
		ls[i].Lapsed = msg
		return ls, nil
	})
	return fmt.Errorf("%s (%w)", msg, err)
}

// dimagentFresh answers with a live token for one account, renewing it when
// it has lapsed or will within the lead time. The renewal runs on a context
// of its own: once the upstream has rotated the pair, its reply must be kept
// even if the request that needed it is gone.
func dimagentFresh(ctx context.Context, user string) (dimagentCreds, error) {
	dimagentMu.Lock()
	defer dimagentMu.Unlock()
	l, ok := dimagentLookup(user)
	if !ok {
		return dimagentCreds{}, fmt.Errorf("no DimAgent account %q", user)
	}
	c, valid := dimagentSaved(l)
	if !valid {
		return dimagentCreds{}, errors.New("DimAgent: unreadable sign-in")
	}
	if c.Access != "" && !dimagentNearExpiry(c.ExpiresAt) {
		return c, nil
	}
	if c.Refresh == "" {
		if c.Access != "" {
			return c, nil // nothing to renew it with; let the request try what there is
		}
		return dimagentCreds{}, errors.New("this DimAgent account is signed out; sign in again")
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	tok, err := dimagent.Refresh(rctx, dimagentClient, dimagentAPI, c.Refresh)
	cancel()
	if err != nil {
		// a hiccup inside the lead time: the token it has still runs, so the
		// request goes on with it and the renewal is tried again next time
		if !isExpiredRefusal(err) && c.Access != "" && c.ExpiresAt > 0 && time.Now().UnixMilli() < c.ExpiresAt {
			return c, nil
		}
		return dimagentCreds{}, dimagentLapse(l.User, err)
	}
	c.Access = tok.AccessToken
	if tok.RefreshToken != "" {
		c.Refresh = tok.RefreshToken
	}
	if p := tok.Plan(); p != "" {
		c.Plan = p // a plan that changed since is read back off the new token
	}
	c.ExpiresAt = tok.Expiry().UnixMilli()
	if err := dimagentEdit(l.User, c, true); err != nil {
		return c, err
	}
	return c, nil
}

// dimagentNearExpiry is true within the lead time of a token's end, or when
// it never said when that is.
func dimagentNearExpiry(expiresAt int64) bool {
	if expiresAt == 0 {
		return true
	}
	return time.Now().UnixMilli() >= expiresAt-dimagentRefreshLead.Milliseconds()
}

// dimagentLookup reads one saved account. loginsMu is held only for the read,
// so a DimAgent renewal never stalls the other subscriptions.
func dimagentLookup(user string) (savedLogin, bool) {
	loginsMu.Lock()
	defer loginsMu.Unlock()
	for _, l := range readLogins() {
		if l.Agent == "dimagent" && strings.EqualFold(l.User, user) {
			return l, true
		}
	}
	return savedLogin{}, false
}

// ---- models ---------------------------------------------------------------

// dimagentFetchModels reads an account's own list and keeps it beside it, so
// the picker shows what that account can call.
func dimagentFetchModels(ctx context.Context, user string) ([]catalog.Model, error) {
	c, err := dimagentFresh(ctx, user)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	raw, err := dimagent.FetchModels(ctx, dimagentClient, dimagentAPI, c.Access)
	if err != nil {
		return nil, err
	}
	ms, err := dimagent.ParseModels(raw)
	if errors.Is(err, dimagent.ErrNoModels) {
		// the upstream answered well and listed nothing: the account's
		// subscription has ended, or was never one that calls models
		return nil, errors.New("DimAgent: this account lists no models — its subscription has ended, or it was never one that calls them (see dimagent.cn)")
	}
	if err != nil {
		return nil, err
	}
	// Only update the listing, using the credentials saved now: a chat or
	// quota request may have rotated the tokens while the model list loaded.
	err = editSideLogin("dimagent", user, func(ls []savedLogin, i int) ([]savedLogin, error) {
		latest, ok := dimagentSaved(ls[i])
		if !ok {
			return nil, errors.New("DimAgent: unreadable sign-in")
		}
		latest.Models = raw
		auth, err := json.Marshal(latest)
		if err != nil {
			return nil, err
		}
		ls[i].Auth, ls[i].Seen = auth, time.Now().UTC()
		return ls, nil
	})
	if err != nil {
		return nil, err
	}
	return ms, catalog.SaveLive("dimagent", dimagentAPI+"/v1", ms)
}

// ---- the provider ---------------------------------------------------------

func dimagentProvider(a dimagentLogin) Provider {
	user := a.User
	acct := &Account{Agent: "dimagent", User: user, Plan: a.Plan, Stream: false}
	acct.sign = func(ctx context.Context, req *http.Request, body []byte) error {
		c, err := dimagentFresh(ctx, user)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.Access)
		// as the app's own chats go out, which is how the upstream knows them
		req.Header.Set("User-Agent", dimagent.ChatUserAgent)
		req.Header.Set("x-title", dimagent.TitleChat)
		req.Header.Set("HTTP-Referer", dimagent.RefererURL)
		return nil
	}
	acct.models = func() []catalog.Model {
		loginsMu.Lock()
		defer loginsMu.Unlock()
		for _, l := range readLogins() {
			if l.Agent == "dimagent" && strings.EqualFold(l.User, user) {
				c, _ := dimagentSaved(l)
				ms, err := dimagent.ParseModels(c.Models)
				if err != nil {
					return nil
				}
				return ms
			}
		}
		return nil
	}
	acct.fetch = func(ctx context.Context) ([]catalog.Model, error) { return dimagentFetchModels(ctx, user) }
	return Provider{ID: "dimagent", Name: "DimAgent", Icon: "dimagent",
		Chat: dimagentAPI + "/v1", Website: "https://dimagent.cn", Account: acct}
}

func dimagentAccount() (Provider, bool) {
	ls := dimagentLogins()
	if len(ls) == 0 {
		return Provider{}, false
	}
	return dimagentProvider(ls[0]), true
}

// dimagentAlsoOn is the DimAgent accounts in use behind the first.
func dimagentAlsoOn() []Provider {
	var out []Provider
	for _, l := range dimagentLogins() {
		if !l.Active && l.On {
			out = append(out, dimagentProvider(l))
		}
	}
	return out
}

// dimagentSignedIn is whether any DimAgent account is kept.
func dimagentSignedIn() bool { return len(dimagentLogins()) > 0 }
