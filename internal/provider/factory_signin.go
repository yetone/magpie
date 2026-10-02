package provider

// PLUGIN-SERVED (see AGENTS.md): Factory ("factory") is a deprecated
// built-in subscription served by its plugin,
// @magpie-community/opencode-factory-auth, once moved onto it
// (provider.Moved; the default for a new sign-in). A moved one's sign-ins,
// models, requests and usage are all the plugin's, never this code's (only
// the move, in migrate*.go, still reads its accounts). A fix here alone
// doesn't reach those users; fix the plugin
// (github.com/magpie-community/plugins, packages/factory) and raise the
// mover's min in internal/provider/migrate_factory.go.

// Signing in to a Factory account from magpie: WorkOS's device flow under
// droid's client, as `droid` runs it. magpie shows the code, the user
// confirms it on the page WorkOS names, and the tokens WorkOS then hands
// over are kept for magpie alone.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func startFactorySignIn(s *signInFlow) error {
	ctx, cancel := context.WithCancel(context.Background())
	dctx, dcancel := context.WithTimeout(ctx, 30*time.Second)
	var dc struct {
		DeviceCode string `json:"device_code"`
		UserCode   string `json:"user_code"`
		URI        string `json:"verification_uri"`
		Complete   string `json:"verification_uri_complete"`
		Interval   int    `json:"interval"`
	}
	err := factoryDevice(dctx, &dc)
	dcancel()
	if err != nil {
		cancel()
		return err
	}
	if dc.DeviceCode == "" || dc.UserCode == "" {
		cancel()
		return errors.New("Factory's sign-in gave no device code")
	}
	s.mu.Lock()
	s.st.URL, s.st.Code = firstNonEmpty(dc.Complete, dc.URI), dc.UserCode
	s.stop = cancel
	s.mu.Unlock()
	interval := time.Duration(max(dc.Interval, 1)) * time.Second
	go func() {
		defer cancel()
		fail := func(msg string) { s.finish(SignInState{State: "failed", Error: msg}) }
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(interval):
			}
			t, err := factoryAuthenticate(ctx, url.Values{
				"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
				"device_code": {dc.DeviceCode}, "client_id": {factoryClientID}})
			switch {
			case ctx.Err() != nil:
				return
			case err != nil:
				continue // a hiccup: ask again
			case t.Error == "authorization_pending":
				continue
			case t.Error == "slow_down":
				interval += time.Second
				continue
			case t.Error == "expired_token":
				fail("the code expired; start again")
				return
			case t.Error == "access_denied":
				fail("the sign-in was declined")
				return
			case t.Error != "" || t.Access == "":
				fail("Factory: " + firstNonEmpty(strings.TrimSpace(t.Error+" "+t.Desc), "no token came back"))
				return
			}
			user, err := factorySignedInWith(ctx, t)
			if err != nil {
				fail(err.Error())
				return
			}
			s.finish(SignInState{State: "done", User: user, Using: strings.EqualFold(activeOf(factorySide()), user)})
			return
		}
	}()
	return nil
}

// factoryDevice asks WorkOS for a device code.
func factoryDevice(ctx context.Context, v any) error {
	b, code, err := factoryPost(ctx, "/authorize/device", url.Values{"client_id": {factoryClientID}})
	if err != nil {
		return err
	}
	if code != http.StatusOK {
		return &factoryStatus{code, "Factory sign-in: " + APIError(b, http.StatusText(code))}
	}
	return json.Unmarshal(b, v)
}

// factorySignedInWith keeps the account WorkOS just signed in, as droid
// does: a token that names no org (the JWT's external_org_id or org_id) is
// put in the first org /api/cli/org lists, and whoami, asked without an org
// header, gives Factory's own id for the org the token is in (the active
// organization droid then sends as X-Factory-Org-Id) and where Factory
// serves it from. droid carries on without either when they fail.
func factorySignedInWith(ctx context.Context, t factoryTokens) (string, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	c := factoryCreds{Access: t.Access, Refresh: t.Refresh, ExpiresAt: factoryExpiry(t.Access),
		Org: t.Org, Email: t.User.Email, UserID: t.User.ID}
	claims := jwtClaims(t.Access)
	c.Org = firstNonEmpty(c.Org, claimString(claims, "org_id"))
	c.Email = firstNonEmpty(c.Email, claimString(claims, "email"))
	c.UserID = firstNonEmpty(c.UserID, claimString(claims, "sub"))
	if c.Org == "" && claimString(claims, "external_org_id") == "" {
		if org, err := factoryFirstOrg(ctx, c); err == nil && org != "" {
			r, err := factoryRenew(ctx, c.Refresh, org)
			if err != nil {
				return "", err
			}
			c.Access, c.ExpiresAt, c.Org = r.Access, factoryExpiry(r.Access), org
			if r.Refresh != "" {
				c.Refresh = r.Refresh
			}
		}
	}
	who, err := factoryWhoami(ctx, c)
	if err != nil && firstNonEmpty(c.Email, c.UserID) == "" {
		return "", err
	}
	if err == nil {
		c.Active, c.Region, c.Prem = who.OrgID, who.Region, who.Prem
		c.Email = firstNonEmpty(c.Email, who.Email)
		c.UserID = firstNonEmpty(c.UserID, who.UserID)
	}
	user := firstNonEmpty(c.Email, c.UserID)
	if user == "" {
		return "", errors.New("signed in, but Factory didn't say whose account it is; try again")
	}
	auth, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	if err := addSideLogin(savedLogin{Agent: "factory", User: user, Auth: auth}, "", func(savedLogin) {}); err != nil {
		return "", err
	}
	// signed in again: whatever WorkOS refused before is over
	_ = editSideLogin("factory", user, func(ls []savedLogin, i int) ([]savedLogin, error) {
		ls[i].Lapsed = ""
		return ls, nil
	})
	forgetAccountCaches()
	return user, nil
}
