package provider

// A Qoder subscription is served through the API the Qoder client talks to
// (api3.qoder.sh's agent_chat_generation SSE), signed with the COSY envelope
// the client uses, the way a Devin one is (devin.go). Unlike Devin, Qoder has
// no CLI whose sign-in magpie can read: magpie runs Qoder's own OAuth device
// flow (qoder.com -> dt- -> jt-) and keeps the tokens itself, in its config
// dir. The protocol lives in internal/qoder; here is who is signed in, the
// models it offers, and the sign-in.
//
// Qoder's requests carry none of Qoder's name — they look like the client's —
// so the gateway tells them apart by their own credential, not by a key it
// hands out.

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/qoder"
	"github.com/yetone/magpie/internal/settings"
)

// qoderStore is magpie's Qoder credential store; one, pointed at its config
// dir. A var so tests can point it at a scratch home.
var (
	qoderStoreOnce sync.Once
	qoderStore     *qoder.Store
)

func qoderCreds() *qoder.Store {
	qoderStoreOnce.Do(func() { qoderStore = qoder.NewStore(settings.Dir()) })
	return qoderStore
}

// QoderSignedIn reports whether a Qoder account is kept in magpie.
func QoderSignedIn() bool {
	c, err := qoderCreds().Load()
	return err == nil && c != nil && c.Token != ""
}

// QoderCredential returns the signed-in account's uid and a live job token,
// refreshing it when near expiry. The gateway asks for it per request; the
// store keeps exactly one refresh token, spent on every rotate, so the fresher
// one is written straight back.
func QoderCredential(ctx context.Context) (uid, token string, err error) {
	c, err := qoderCreds().EnsureFresh(ctx, http.DefaultClient)
	if err != nil {
		return "", "", err
	}
	return c.UID, c.Token, nil
}

// qoderModels is the account's model list: what a fetch or a picker visit last
// asked Qoder for. It never spawns a request here — Available() runs on every
// gateway call.
func qoderModels() []catalog.Model {
	if ms, _, ok := catalog.Live("qoder"); ok {
		return ms
	}
	return nil
}

func qoderFetchModels(ctx context.Context) ([]catalog.Model, error) {
	uid, token, err := QoderCredential(ctx)
	if err != nil {
		return nil, err
	}
	c, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	raw, err := qoder.FetchModels(c, http.DefaultClient, qoder.APIHost, uid, token)
	if err != nil {
		return nil, err
	}
	ms, err := qoder.ParseModels(raw)
	if err != nil {
		return nil, err
	}
	return ms, catalog.SaveLive("qoder", "", ms)
}

// qoderAccount is the signed-in Qoder subscription as a provider.
func qoderAccount() (Provider, bool) {
	if !QoderSignedIn() {
		return Provider{}, false
	}
	c, _ := qoderCreds().Load()
	// Qoder's device flow names an account by its uid and gives no email, so
	// the uid is the identity to show; an email, if one is ever known, reads
	// better and takes precedence. Never leave it empty — the Routing view and
	// the account list label the account by this, and an empty one shows as
	// "undefined".
	user := c.Email
	if user == "" {
		user = c.UID
	}
	if user == "" {
		user = "Qoder"
	}
	acct := &Account{Agent: "qoder", User: user, Plan: "", Stream: true}
	acct.models = qoderModels
	acct.fetch = qoderFetchModels
	return Provider{ID: "qoder", Name: "Qoder", Icon: "qoder", Website: "https://qoder.com", Account: acct}, true
}

// QoderForget drops magpie's Qoder sign-in; the account still stands at Qoder.
func QoderForget() error { return qoderCreds().Forget() }

// ---- the device-flow sign-in -------------------------------------------------

// qoderFlow is a sign-in in progress, between showing the URL and the tokens
// landing. The caller (the window, or `magpie account`) opens the URL and then
// drives QoderCompleteSignIn; only one is waited on at a time.
type qoderFlow struct {
	verifier, nonce string
	client          *qoder.DeviceFlow
}

// QoderAuthURL begins a Qoder sign-in: it returns the qoder.com page to open.
// QoderCompleteSignIn, given the same context, waits for the user to authorize
// there and finishes the sign-in.
func QoderAuthURL() (url string, flow *qoderFlow, err error) {
	f := qoder.NewDeviceFlow(http.DefaultClient)
	url, verifier, nonce, err := f.Authorization()
	if err != nil {
		return "", nil, err
	}
	return url, &qoderFlow{verifier: verifier, nonce: nonce, client: f}, nil
}

// QoderCompleteSignIn waits for the device flow to be authorized, trades the
// device token for the job token, fetches the models, and keeps the account.
// It answers who signed in. It blocks until the user finishes at qoder.com or
// ctx ends.
func QoderCompleteSignIn(ctx context.Context, fl *qoderFlow) (user string, err error) {
	dt, err := fl.client.PollDeviceToken(ctx, fl.nonce, fl.verifier, 2*time.Second)
	if err != nil {
		return "", err
	}
	jt, err := fl.client.JobToken(ctx, dt.Token)
	if err != nil {
		return "", err
	}
	life := jt.Expiry()
	if life <= 0 {
		life = 24 * time.Hour // Qoder advertises a day when it names no expiry
	}
	cred := qoder.Credential{
		UID: dt.UserID, Token: jt.Token, RefreshToken: jt.RefreshToken,
		DeviceToken: dt.Token, DeviceRefresh: dt.RefreshToken,
		ExpiresAt: time.Now().Add(life).UnixMilli(),
	}
	// who signed in: the account endpoint answers the email and name, asked
	// with the device token while it is still fresh. It is best-effort — a
	// sign-in that can't reach it still works, the account just shows by uid.
	if ui, uerr := qoder.FetchUserInfo(ctx, http.DefaultClient, dt.Token); uerr == nil && ui != nil {
		cred.Email, cred.Name = ui.Email, ui.Name
	}
	if err := qoderCreds().Save(cred); err != nil {
		return "", err
	}
	// fill in who it is and what models, best-effort: a sign-in that can't
	// reach the model list still works, the list just comes later
	c, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if raw, ferr := qoder.FetchModels(c, http.DefaultClient, qoder.APIHost, cred.UID, cred.Token); ferr == nil {
		if ms, perr := qoder.ParseModels(raw); perr == nil {
			_ = catalog.SaveLive("qoder", "", ms)
		}
	}
	forgetAccountCaches()
	if cred.Email == "" {
		return cred.UID, nil // no email known; the uid names the account in the list
	}
	return cred.Email, nil
}

// startQoderSignIn drives Qoder's device flow for the sign-in screen: it shows
// the qoder.com page, waits in the background for the user to authorize, then
// trades the tokens and reports the account — the shape a self-run sign-in
// takes (see startWorkBuddySignIn).
func startQoderSignIn(s *signInFlow) error {
	authURL, fl, err := QoderAuthURL()
	if err != nil {
		return fmt.Errorf("Qoder sign-in: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.st.URL = authURL
	s.stop = cancel
	s.mu.Unlock()
	go func() {
		defer cancel()
		user, err := QoderCompleteSignIn(ctx, fl)
		if err != nil {
			s.finish(SignInState{State: "failed", Error: "Qoder: " + err.Error()})
			return
		}
		s.finish(SignInState{State: "done", User: user, Using: true})
	}()
	return nil
}
