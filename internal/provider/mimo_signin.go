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

// Xiaomi MiMo's sign-in, run by magpie. The app signs in to the Xiaomi
// account in a window of its own; magpie asks account.xiaomi.com for a
// long-poll sign-in for the MiMo server's service (sid mimosgp) instead,
// the one Xiaomi's QR sign-in makes: its page, opened in the browser, is
// Xiaomi's own sign-in (or its QR code, scanned with a Xiaomi phone), and
// the poll answers once it is done with the account's passToken. That
// signs the account on at the MiMo server (mimoSession), as the app's
// window does.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// mimoLongPoll is account.xiaomi.com's longPolling/loginUrl answer.
type mimoLongPoll struct {
	Code     int    `json:"code"`
	LoginURL string `json:"loginUrl"` // Xiaomi's sign-in page for this ticket
	QR       string `json:"qr"`       // the same, as a QR code image
	LP       string `json:"lp"`       // where the sign-in's outcome is waited for
	Timeout  int    `json:"timeout"`  // seconds the ticket lasts
	Desc     string `json:"desc"`
}

// mimoPassed is what the poll answers once the account signed in.
type mimoPassed struct {
	Code      int         `json:"code"`
	UserID    json.Number `json:"userId"`
	CUserID   string      `json:"cUserId"`
	PassToken string      `json:"passToken"`
	Location  string      `json:"location"`
	Desc      string      `json:"desc"`
}

// mimoJSON reads one of account.xiaomi.com's answers, which start with
// "&&&START&&&".
func mimoJSON(b []byte, v any) error {
	s := strings.TrimSpace(string(b))
	s = strings.TrimPrefix(s, "&&&START&&&")
	return json.Unmarshal([]byte(s), v)
}

// mimoServiceOf is the Xiaomi service a MiMo server signs in to, and where
// Xiaomi sends the browser back: read off the server's own redirect to
// account.xiaomi.com, so they are what the app's window is given.
func mimoServiceOf(ctx context.Context, base string) (sid, callback string) {
	sid, callback = "mimosgp", strings.TrimRight(base, "/")+"/sts"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/user/xiaomi/me", nil)
	if err != nil {
		return
	}
	mimoHeaders(req)
	c := &http.Client{Timeout: 15 * time.Second, Transport: mimoClient.Transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := c.Do(req)
	if err != nil {
		return
	}
	res.Body.Close()
	loc, err := url.Parse(res.Header.Get("Location"))
	if err != nil {
		return
	}
	q := loc.Query()
	if v := q.Get("sid"); v != "" {
		sid = v
	}
	if v := q.Get("callback"); v != "" {
		callback = v
	}
	return
}

// mimoAccountGet asks account.xiaomi.com as the app's sign-in window does.
func mimoAccountGet(ctx context.Context, client *http.Client, u, deviceID string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", mimoUA)
	req.Header.Set("Cookie", "deviceId="+deviceID+"; pass_ua=pc; uLocale=zh_CN")
	res, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	return b, res.StatusCode, err
}

func startMiMoSignIn(s *signInFlow) error {
	base := mimoBaseOf(mimoDefaultRegion)
	deviceID := mimoDeviceID()
	ctx, cancel := context.WithCancel(context.Background())
	start, stop := context.WithTimeout(ctx, 20*time.Second)
	defer stop()
	sid, callback := mimoServiceOf(start, base)
	q := url.Values{}
	q.Set("_group", "DEFAULT")
	q.Set("_qrsize", "240")
	q.Set("qs", "%3Fsid%3D"+url.QueryEscape(sid)+"%26_json%3Dtrue")
	q.Set("callback", callback)
	q.Set("_hasLogo", "false")
	q.Set("sid", sid)
	q.Set("serviceParam", "")
	q.Set("_locale", "en_US")
	b, status, err := mimoAccountGet(start, mimoClient, mimoAccountSite+"/longPolling/loginUrl?"+q.Encode(), deviceID)
	if err != nil {
		cancel()
		return fmt.Errorf("Xiaomi MiMo sign-in: %w", err)
	}
	var lp mimoLongPoll
	if err := mimoJSON(b, &lp); err != nil || lp.Code != 0 || lp.LoginURL == "" || lp.LP == "" {
		cancel()
		return fmt.Errorf("Xiaomi MiMo sign-in: account.xiaomi.com answered %d %s", status, firstNonEmpty(lp.Desc, strings.TrimSpace(string(b[:min(len(b), 200)]))))
	}
	s.mu.Lock()
	s.st.URL = lp.LoginURL
	s.stop = cancel
	s.mu.Unlock()

	go func() {
		defer cancel()
		life := time.Duration(lp.Timeout) * time.Second
		if life <= 0 || life > signInTimeout {
			life = signInTimeout
		}
		wait, done := context.WithTimeout(ctx, life)
		defer done()
		passed, err := mimoWaitPoll(wait, lp.LP, deviceID)
		if ctx.Err() != nil {
			return // canceled
		}
		if err != nil {
			s.finish(SignInState{State: "failed", Error: err.Error()})
			return
		}
		user, err := mimoSignedInWith(ctx, passed, deviceID)
		if err != nil {
			s.finish(SignInState{State: "failed", Error: err.Error()})
			return
		}
		s.finish(SignInState{State: "done", User: user, Using: strings.EqualFold(activeOf(mimoSide()), user)})
	}()
	return nil
}

// mimoWaitPoll waits on the long poll until the account has signed in or
// the ticket runs out.
func mimoWaitPoll(ctx context.Context, lp, deviceID string) (mimoPassed, error) {
	// each poll is held open by Xiaomi until something happens or its own
	// time is up
	client := &http.Client{Timeout: 70 * time.Second, Transport: mimoClient.Transport}
	for {
		b, status, err := mimoAccountGet(ctx, client, lp, deviceID)
		if ctx.Err() != nil {
			return mimoPassed{}, errors.New("the Xiaomi sign-in page expired before the sign-in finished; start it again")
		}
		if err == nil && status == http.StatusOK {
			var p mimoPassed
			if mimoJSON(b, &p) == nil && p.PassToken != "" && p.UserID.String() != "" {
				return p, nil
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
}

// mimoSignedInWith signs the account on at the MiMo server (at its own
// region's, as the app moves it), keeps it and reads its plan.
func mimoSignedInWith(ctx context.Context, p mimoPassed, deviceID string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	c := mimoCreds{UserID: p.UserID.String(), CUserID: p.CUserID, PassToken: p.PassToken, DeviceID: deviceID,
		Region: mimoDefaultRegion, Base: mimoBaseOf(mimoDefaultRegion)}
	cookies, me, err := mimoSession(ctx, c, c.Base)
	if errors.Is(err, ErrMiMoSignIn) {
		return "", errors.New("Xiaomi signed the account in, but the MiMo server didn't take it; try again")
	}
	if err != nil {
		return "", err
	}
	if r := strings.ToUpper(strings.TrimSpace(me.Data.Region)); r != "" && r != c.Region {
		if nb := mimoBaseOf(r); nb != "" && nb != c.Base {
			// the account is served at its own region's server; kept where
			// it is when that one won't sign it on, as the app does
			if ck, m2, err := mimoSession(ctx, c, nb); err == nil {
				cookies, me, c.Base, c.Region = ck, m2, nb, r
			}
		}
	}
	c.Cookies, c.Issued, c.Name = cookies, time.Now().UTC(), me.name()
	who := firstNonEmpty(me.Data.UserID.String(), c.UserID)
	auth, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	if err := addSideLogin(savedLogin{Agent: MiMoID, User: who, Auth: auth}, "", func(savedLogin) {}); err != nil {
		return "", err
	}
	// signed in again: whatever Xiaomi refused before is over
	_ = editSideLogin(MiMoID, who, func(ls []savedLogin, i int) ([]savedLogin, error) {
		ls[i].Lapsed = ""
		return ls, nil
	})
	if plan, _, err := mimoPlan(ctx, who); err == nil {
		_ = editSideLogin(MiMoID, who, func(ls []savedLogin, i int) ([]savedLogin, error) {
			ls[i].Plan = plan
			return ls, nil
		})
	}
	forgetAccountCaches()
	return who, nil
}
