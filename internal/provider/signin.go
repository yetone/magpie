package provider

// Adding a subscription from magpie itself. magpie runs the vendor's own
// browser sign-in — the one Claude Code's /login and `codex login` run, with
// their OAuth clients — takes the tokens at a callback on this machine, and
// keeps the account with the others in logins.json. An agent that has no
// account yet is signed in to it straight away; otherwise it stays one
// click away, beside the rest.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// The vendors' sign-in pages; vars so tests can point them elsewhere.
var (
	claudeAuthorizeURL = "https://claude.com/cai/oauth/authorize"
	codexAuthorizeURL  = "https://auth.openai.com/oauth/authorize"
	// codexCallbackAddr is fixed: OpenAI only sends Codex's client back to
	// port 1455.
	codexCallbackAddr = "127.0.0.1:1455"
)

// claudeScopes are what Claude Code asks for at /login.
const claudeScopes = "org:create_api_key user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload user:plugins"

const codexScopes = "openid profile email offline_access api.connectors.read api.connectors.invoke"

// signInTimeout is how long a sign-in waits for the browser.
var signInTimeout = 10 * time.Minute

// SignInState is where a sign-in stands, for the window to show.
type SignInState struct {
	ID    string `json:"id"`
	Agent string `json:"agent"`
	URL   string `json:"url"`             // the vendor's page, to open or copy
	Code  string `json:"code,omitempty"`  // what to type there, for a device code
	State string `json:"state"`           // waiting, done, failed or canceled
	User  string `json:"user,omitempty"`  // the account, once done
	Plan  string `json:"plan,omitempty"`  //
	Using bool   `json:"using,omitempty"` // the agent was signed in to it too
	Error string `json:"error,omitempty"`
}

type signInFlow struct {
	mu       sync.Mutex
	st       SignInState
	verifier string
	state    string
	redirect string
	srv      *http.Server
	stop     func() // ends an agent's own login command, when that is the sign-in
	done     chan struct{}
}

var signIns = struct {
	sync.Mutex
	m map[string]*signInFlow
}{m: map[string]*signInFlow{}}

func randomToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// StartSignIn begins adding an account to an agent: open the returned URL
// in a browser and the rest happens on its own; SignInStatus follows it.
func StartSignIn(agent string) (SignInState, error) {
	s := &signInFlow{verifier: randomToken(48), state: randomToken(24), done: make(chan struct{})}
	s.st = SignInState{ID: randomToken(9), Agent: agent, State: "waiting"}
	sum := sha256.Sum256([]byte(s.verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	var ln net.Listener
	var err error
	q := url.Values{}
	switch agent {
	case "claude":
		if ln, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
			return SignInState{}, err
		}
		s.redirect = fmt.Sprintf("http://localhost:%d/callback", ln.Addr().(*net.TCPAddr).Port)
		q.Set("code", "true")
		q.Set("client_id", claudeClientID)
		q.Set("response_type", "code")
		q.Set("redirect_uri", s.redirect)
		q.Set("scope", claudeScopes)
		q.Set("code_challenge", challenge)
		q.Set("code_challenge_method", "S256")
		q.Set("state", s.state)
		s.st.URL = claudeAuthorizeURL + "?" + q.Encode()
	case "codex":
		if ln, err = listenCodexCallback(); err != nil {
			return SignInState{}, err
		}
		_, port, _ := net.SplitHostPort(codexCallbackAddr)
		s.redirect = "http://localhost:" + port + "/auth/callback"
		q.Set("response_type", "code")
		q.Set("client_id", codexClientID)
		q.Set("redirect_uri", s.redirect)
		q.Set("scope", codexScopes)
		q.Set("code_challenge", challenge)
		q.Set("code_challenge_method", "S256")
		q.Set("id_token_add_organizations", "true")
		q.Set("codex_cli_simplified_flow", "true")
		q.Set("state", s.state)
		q.Set("originator", "codex_cli_rs")
		s.st.URL = codexAuthorizeURL + "?" + q.Encode()
	case "cursor":
		// Cursor has no sign-in of its own to borrow: its CLI signs in
		if err := startCursorSignIn(s); err != nil {
			return SignInState{}, err
		}
	case "grok":
		// so is Grok: its CLI signs in with a device code
		if err := startGrokSignIn(s); err != nil {
			return SignInState{}, err
		}
	case "devin":
		// Devin's too: `devin auth login` opens its own link
		if err := startDevinSignIn(s); err != nil {
			return SignInState{}, err
		}
	case "copilot":
		// GitHub's device code, as Copilot's editors sign in
		if err := startCopilotSignIn(s); err != nil {
			return SignInState{}, err
		}
	default:
		return SignInState{}, fmt.Errorf("magpie can't sign in to %s accounts", agent)
	}

	if ln != nil {
		mux := http.NewServeMux()
		mux.HandleFunc("/", s.callback)
		s.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
		go func() { _ = s.srv.Serve(ln) }()
	}
	go func() {
		select {
		case <-s.done:
		case <-time.After(signInTimeout):
			s.finish(SignInState{State: "failed", Error: "the sign-in timed out; start it again"})
		}
	}()

	signIns.Lock()
	// only one sign-in per agent at a time: a newer one replaces the older
	for id, o := range signIns.m {
		if o.st.Agent == agent {
			o.finish(SignInState{State: "canceled"})
			delete(signIns.m, id)
		}
	}
	signIns.m[s.st.ID] = s
	signIns.Unlock()
	return s.status(), nil
}

// listenCodexCallback takes Codex's callback port. A `codex login` left
// waiting there is asked to stop first, the way Codex itself does it.
func listenCodexCallback() (net.Listener, error) {
	for i := 0; i < 10; i++ {
		ln, err := net.Listen("tcp", codexCallbackAddr)
		if err == nil {
			return ln, nil
		}
		if i == 0 {
			c := http.Client{Timeout: 2 * time.Second}
			if resp, err := c.Get("http://" + codexCallbackAddr + "/cancel"); err == nil {
				resp.Body.Close()
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return nil, errors.New("port 1455, where ChatGPT sends the sign-in back, is busy; close any other Codex sign-in and try again")
}

// SignInStatus reports on a sign-in StartSignIn began.
func SignInStatus(id string) (SignInState, bool) {
	signIns.Lock()
	s, ok := signIns.m[id]
	signIns.Unlock()
	if !ok {
		return SignInState{}, false
	}
	return s.status(), true
}

// CancelSignIn stops waiting for the browser.
func CancelSignIn(id string) {
	signIns.Lock()
	s, ok := signIns.m[id]
	signIns.Unlock()
	if ok {
		s.finish(SignInState{State: "canceled"})
	}
}

// WaitSignIn blocks until a sign-in is over, for the command line.
func WaitSignIn(ctx context.Context, id string) (SignInState, error) {
	signIns.Lock()
	s, ok := signIns.m[id]
	signIns.Unlock()
	if !ok {
		return SignInState{}, errors.New("no such sign-in")
	}
	select {
	case <-s.done:
		return s.status(), nil
	case <-ctx.Done():
		s.finish(SignInState{State: "canceled"})
		return s.status(), ctx.Err()
	}
}

func (s *signInFlow) status() SignInState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st
}

// finish records the outcome once and lets the callback server go.
func (s *signInFlow) finish(out SignInState) bool {
	s.mu.Lock()
	if s.st.State != "waiting" {
		s.mu.Unlock()
		return false
	}
	out.ID, out.Agent, out.URL, out.Code = s.st.ID, s.st.Agent, s.st.URL, s.st.Code
	s.st = out
	s.mu.Unlock()
	if out.State == "done" {
		// signing in again brings back an account removed from magpie
		_ = ShowAccount(out.Agent)
	}
	close(s.done)
	if s.stop != nil {
		s.stop()
	}
	if s.srv == nil {
		return true
	}
	go func() {
		// after the browser has had its page
		time.Sleep(time.Second)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.srv.Shutdown(ctx)
	}()
	return true
}

func (s *signInFlow) callback(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/callback", "/auth/callback":
	case "/cancel":
		s.finish(SignInState{State: "canceled"})
		w.WriteHeader(http.StatusNoContent)
		return
	default:
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	if s.status().State != "waiting" {
		signInPage(w, false, "This sign-in is over", "Start it again from magpie.")
		return
	}
	if e := q.Get("error"); e != "" {
		msg := q.Get("error_description")
		if msg == "" {
			msg = e
		}
		s.finish(SignInState{State: "failed", Error: msg})
		signInPage(w, false, "Sign-in didn't finish", msg)
		return
	}
	if q.Get("state") != s.state || q.Get("code") == "" {
		// not ours: someone else's page, or a stale tab
		signInPage(w, false, "This link isn't from magpie's sign-in", "Start it again from magpie.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	l, err := s.exchange(ctx, q.Get("code"))
	if err == nil {
		var using bool
		if using, err = addLogin(l); err == nil {
			s.finish(SignInState{State: "done", User: l.User, Plan: l.Plan, Using: using})
			signInPage(w, true, "You're signed in", fmt.Sprintf("%s is added to magpie. You can close this tab.", l.User))
			return
		}
	}
	s.finish(SignInState{State: "failed", Error: err.Error()})
	signInPage(w, false, "Sign-in didn't finish", err.Error())
}

// exchange trades the code for tokens and makes them into a login.
func (s *signInFlow) exchange(ctx context.Context, code string) (savedLogin, error) {
	if s.st.Agent == "codex" {
		return codexExchange(ctx, code, s.verifier, s.redirect)
	}
	return claudeExchange(ctx, code, s.verifier, s.redirect, s.state)
}

func postToken(ctx context.Context, tokenURL, ctype string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error            any    `json:"error"`
			ErrorDescription string `json:"error_description"`
		}
		_ = json.Unmarshal(b, &e)
		msg := e.ErrorDescription
		if msg == "" {
			if m, ok := e.Error.(map[string]any); ok {
				msg, _ = m["message"].(string)
			} else if s, ok := e.Error.(string); ok {
				msg = s
			}
		}
		if msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
		return fmt.Errorf("the sign-in was refused (%d): %s", resp.StatusCode, msg)
	}
	return json.Unmarshal(b, out)
}

func claudeExchange(ctx context.Context, code, verifier, redirect, state string) (savedLogin, error) {
	body, _ := json.Marshal(map[string]string{"grant_type": "authorization_code", "code": code,
		"redirect_uri": redirect, "client_id": claudeClientID, "code_verifier": verifier, "state": state})
	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Scope        string `json:"scope"`
		Account      struct {
			UUID  string `json:"uuid"`
			Email string `json:"email_address"`
		} `json:"account"`
		Organization struct {
			UUID string `json:"uuid"`
			Name string `json:"name"`
		} `json:"organization"`
	}
	if err := postToken(ctx, claudeTokenURL, "application/json", body, &tok); err != nil {
		return savedLogin{}, err
	}
	if tok.AccessToken == "" || tok.RefreshToken == "" {
		return savedLogin{}, errors.New("Claude sent back no token")
	}
	acct := map[string]any{"accountUuid": tok.Account.UUID, "emailAddress": tok.Account.Email,
		"organizationUuid": tok.Organization.UUID}
	if tok.Organization.Name != "" {
		acct["organizationName"] = tok.Organization.Name
	}
	c := claudeCredentials{raw: map[string]any{}, OAuth: claudeAuth{
		AccessToken: tok.AccessToken, RefreshToken: tok.RefreshToken,
		ExpiresAt: time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second).UnixMilli(),
		Scopes:    strings.Fields(tok.Scope),
	}}
	// the plan comes with the profile, as it does for Claude Code
	if p, err := claudeProfile(ctx, tok.AccessToken); err == nil {
		c.OAuth.SubscriptionType = claudePlans[p.Organization.Type]
		c.OAuth.RateLimitTier = p.Organization.RateLimitTier
		if p.Account.Email != "" {
			acct["emailAddress"] = p.Account.Email
		}
		if p.Account.DisplayName != "" {
			acct["displayName"] = p.Account.DisplayName
		}
		if p.Organization.BillingType != "" {
			acct["billingType"] = p.Organization.BillingType
		}
		acct["hasExtraUsageEnabled"] = p.Organization.ExtraUsage
	}
	email, _ := acct["emailAddress"].(string)
	if email == "" {
		return savedLogin{}, errors.New("Claude didn't say which account signed in")
	}
	auth, err := c.marshal()
	if err != nil {
		return savedLogin{}, err
	}
	profile, _ := json.Marshal(acct)
	return savedLogin{Agent: "claude", User: claudeUser(email, c.OAuth.SubscriptionType, acct), Plan: c.OAuth.SubscriptionType, Auth: auth, Profile: profile}, nil
}

// claudePlans names Claude's organization types the way Claude Code does.
var claudePlans = map[string]string{"claude_max": "max", "claude_pro": "pro", "claude_enterprise": "enterprise", "claude_team": "team"}

type claudeProfileInfo struct {
	Account struct {
		Email       string `json:"email"`
		DisplayName string `json:"display_name"`
	} `json:"account"`
	Organization struct {
		Type          string `json:"organization_type"`
		RateLimitTier string `json:"rate_limit_tier"`
		BillingType   string `json:"billing_type"`
		ExtraUsage    bool   `json:"has_extra_usage_enabled"`
	} `json:"organization"`
}

func claudeProfile(ctx context.Context, token string) (claudeProfileInfo, error) {
	var p claudeProfileInfo
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, claudeBase+"/api/oauth/profile", nil)
	if err != nil {
		return p, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return p, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return p, fmt.Errorf("profile: %s", resp.Status)
	}
	return p, json.NewDecoder(resp.Body).Decode(&p)
}

func codexExchange(ctx context.Context, code, verifier, redirect string) (savedLogin, error) {
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect},
		"client_id": {codexClientID}, "code_verifier": {verifier}}
	var tok struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := postToken(ctx, codexTokenURL, "application/x-www-form-urlencoded", []byte(form.Encode()), &tok); err != nil {
		return savedLogin{}, err
	}
	if tok.AccessToken == "" || tok.RefreshToken == "" || tok.IDToken == "" {
		return savedLogin{}, errors.New("ChatGPT sent back no token")
	}
	id := jwtClaims(tok.IDToken)
	user := claimString(id, "email")
	if user == "" {
		return savedLogin{}, errors.New("ChatGPT didn't say which account signed in")
	}
	// auth.json as `codex login` writes it
	auth, err := json.MarshalIndent(map[string]any{
		"OPENAI_API_KEY": nil,
		"auth_mode":      "chatgpt",
		"tokens": map[string]string{"id_token": tok.IDToken, "access_token": tok.AccessToken,
			"refresh_token": tok.RefreshToken, "account_id": claimString(id, "https://api.openai.com/auth", "chatgpt_account_id")},
		"last_refresh": time.Now().UTC().Format(time.RFC3339Nano),
	}, "", "  ")
	if err != nil {
		return savedLogin{}, err
	}
	return savedLogin{Agent: "codex", User: user, Plan: claimString(id, "https://api.openai.com/auth", "chatgpt_plan_type"), Auth: auth}, nil
}

// addLogin keeps a freshly signed-in account. The agent is signed in to it
// too when it has no account yet, or had this one: the new tokens replace
// the old, so one refresh token stays in one place.
func addLogin(l savedLogin) (using bool, err error) {
	loginsMu.Lock()
	defer loginsMu.Unlock()
	l.Seen = time.Now().UTC().Truncate(time.Second)
	live, signedIn := liveLogin(l.Agent)
	using = !signedIn || sameLogin(live, l)
	ls := readLogins()
	if signedIn && !using {
		// the current account, as fresh as the agent has it
		live.Seen = l.Seen
		ls = upsertLogin(ls, live)
	}
	// a second account is in use beside the first straight away, as a
	// second key is: it takes over when the first runs out
	l.On = !using
	if err := writeLogins(upsertLogin(ls, l)); err != nil {
		return false, err
	}
	if using {
		switch l.Agent {
		case "codex":
			err = writePrivate(codexAuthPath(), append(bytes.TrimSpace(l.Auth), '\n'))
		case "claude":
			err = putClaudeLogin(l)
		}
		if err != nil {
			return false, err
		}
	}
	loginsSeenAt = time.Time{}
	forgetAccountCaches()
	return using, nil
}

// signInPage is what the browser shows at the end: magpie's, in its own
// quiet black and white, never the vendor's "return to the CLI".
func signInPage(w http.ResponseWriter, ok bool, title, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
	}
	mark := `<path d="M5 12.5l4.5 4.5L19 7.5"/>`
	if !ok {
		mark = `<path d="M7 7l10 10M17 7L7 17"/>`
	}
	fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>magpie · %[1]s</title><style>
:root{--bg:#fafafa;--fg:#111;--muted:#6b6b6b;--line:#e4e4e4;--card:#fff}
@media (prefers-color-scheme:dark){:root{--bg:#0e0e0e;--fg:#f2f2f2;--muted:#9a9a9a;--line:#262626;--card:#161616}}
*{box-sizing:border-box}html,body{height:100%%;margin:0}
body{background:var(--bg);color:var(--fg);font:15px/1.5 -apple-system,BlinkMacSystemFont,"Segoe UI",system-ui,sans-serif;display:grid;place-items:center;padding:16px}
.card{width:100%%;max-width:380px;background:var(--card);border:1px solid var(--line);border-radius:16px;padding:32px 28px;text-align:center;animation:in .4s cubic-bezier(.2,.8,.2,1)}
.mark{width:44px;height:44px;margin:0 auto 18px;border-radius:50%%;display:grid;place-items:center;background:var(--fg);color:var(--bg)}
.mark svg{width:22px;height:22px;fill:none;stroke:currentColor;stroke-width:2.2;stroke-linecap:round;stroke-linejoin:round}
h1{font-size:18px;font-weight:620;margin:0 0 6px;letter-spacing:-.01em}p{margin:0;color:var(--muted);font-size:14px;overflow-wrap:anywhere}
.by{margin-top:22px;font-size:12px;color:var(--muted);letter-spacing:.02em}
@keyframes in{from{opacity:0;transform:translateY(6px) scale(.98)}}
</style></head><body><div class="card"><div class="mark"><svg viewBox="0 0 24 24">%[3]s</svg></div>
<h1>%[1]s</h1><p>%[2]s</p><div class="by">magpie</div></div></body></html>`, html.EscapeString(title), html.EscapeString(msg), mark)
}
