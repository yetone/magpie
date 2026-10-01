package provider

// Adding a subscription from magpie itself. magpie runs the vendor's own
// browser sign-in — the one `codex login` runs, with its OAuth client —
// takes the tokens at a callback on this machine, and keeps the account
// with the others in logins.json. A Claude account is signed in by Claude
// Code itself (claude_signin.go). An agent that has no
// account yet is signed in to it straight away; otherwise it stays one
// click away, beside the rest.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
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
	codexAuthorizeURL = "https://auth.openai.com/oauth/authorize"
	devinAuthorizeURL = "https://app.devin.ai/auth/cli/continue"
	// codexCallbackAddr is fixed: OpenAI only sends Codex's client back to
	// port 1455.
	codexCallbackAddr = "127.0.0.1:1455"
)

const codexScopes = "openid profile email offline_access api.connectors.read api.connectors.invoke"

// signInTimeout is how long a sign-in waits for the browser.
var signInTimeout = 10 * time.Minute

// SignInState is where a sign-in stands, for the window to show.
type SignInState struct {
	ID            string `json:"id"`
	Agent         string `json:"agent"`
	URL           string `json:"url"`                     // the vendor's page, to open or copy
	Code          string `json:"code,omitempty"`          // what to type there, for a device code
	State         string `json:"state"`                   // installing, waiting, done, failed or canceled
	PasteCallback bool   `json:"pasteCallback,omitempty"` // a callback URL can also finish this sign-in
	// PasteCode is a plugin's sign-in finished by the code its page shows
	PasteCode bool `json:"pasteCode,omitempty"`
	// PasteKey is a sign-in an API key made on KeysURL also finishes
	// (Command Code's, whose page posts its key where a pasted address
	// can't carry it)
	PasteKey     bool   `json:"pasteKey,omitempty"`
	KeysURL      string `json:"keysURL,omitempty"`
	Instructions string `json:"instructions,omitempty"` // a plugin's words for its page
	// Installing is the CLI being installed before the sign-in can start
	Installing string `json:"installing,omitempty"`
	User       string `json:"user,omitempty"`  // the account, once done
	Plan       string `json:"plan,omitempty"`  //
	Using      bool   `json:"using,omitempty"` // the agent was signed in to it too
	// Again is an account listed already: its sign-in was renewed rather
	// than a new account added (#413)
	Again bool   `json:"again,omitempty"`
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
	kiro     *kiroFlow
	site     string           // where to sign in, for an agent with more than one (ZCode: "zai" or "bigmodel")
	plugin   string           // a plugin's sign-in session, finished with the code pasted back
	claude   *claudeCLISignIn // Claude Code's own sign-in, run by magpie
	// claimed is a callback being traded for the account: the browser's own
	// or a pasted address, whichever came first
	claimed bool
	done    chan struct{}
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
//
// An agent signed in with a CLI that isn't installed has it installed first:
// the sign-in is then "installing", and gets its URL when that is done.
func StartSignIn(agent string) (SignInState, error) {
	// "zcode:bigmodel" is ZCode signed in on BigModel (智谱)
	agent, site, _ := strings.Cut(agent, ":")
	return StartSignInAt(agent, site)
}

// StartSignInAt is StartSignIn on one of the sites an agent signs in on:
// ZCode's "zai" (the default) or "bigmodel".
func StartSignInAt(agent, site string) (SignInState, error) {
	// one moved onto its plugin signs in there: an account signed in to
	// here would be the built-in's, which nothing serves now
	if Moved(agent) {
		return SignInState{}, fmt.Errorf("%s runs on its plugin: sign in through the plugin (magpie plugin login %s)", agent, agent)
	}
	s := &signInFlow{verifier: randomToken(48), state: randomToken(24), done: make(chan struct{}), site: site}
	s.st = SignInState{ID: randomToken(9), Agent: agent, State: "waiting"}
	cli, install := missingCLI(agent)
	var installing context.Context
	if install {
		s.st.State, s.st.Installing = "installing", cli.Name
		// canceling the sign-in stops the installer
		installing, s.stop = context.WithCancel(context.Background())
	} else if err := s.begin(); err != nil {
		return SignInState{}, err
	}
	timeout := signInTimeout
	if install {
		timeout += installTimeout
	}
	go func() {
		select {
		case <-s.done:
		case <-time.After(timeout):
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
	if install {
		go s.installThenBegin(installing, cli)
	}
	return s.status(), nil
}

// installThenBegin installs the CLI the sign-in needs, then starts it.
func (s *signInFlow) installThenBegin(ctx context.Context, cli agentCLI) {
	err := installCLI(ctx, cli)
	s.mu.Lock()
	s.stop = nil
	s.mu.Unlock()
	if err == nil {
		forgetAccountCaches()
		err = s.begin()
	}
	if err != nil {
		s.finish(SignInState{State: "failed", Error: err.Error()})
		return
	}
	s.mu.Lock()
	if s.st.State == "installing" {
		s.st.State, s.st.Installing = "waiting", ""
		s.mu.Unlock()
		return
	}
	// canceled while it began: let go of what it started
	stop, srv := s.stop, s.srv
	s.mu.Unlock()
	if stop != nil {
		stop()
	}
	if srv != nil {
		_ = srv.Close()
	}
}

// begin starts the vendor's sign-in: the URL to open, and whatever waits
// for the browser to come back.
func (s *signInFlow) begin() error {
	agent := s.st.Agent
	sum := sha256.Sum256([]byte(s.verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	var ln net.Listener
	var err error
	q := url.Values{}
	switch agent {
	case "claude":
		// Claude Code's own `claude auth login`: magpie makes no part of it
		if err := startClaudeSignIn(s); err != nil {
			return err
		}
	case "codex":
		if ln, err = listenCodexCallback(); err != nil {
			return err
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
			return err
		}
	case "grok":
		// so is Grok: its CLI signs in with a device code
		if err := startGrokSignIn(s); err != nil {
			return err
		}
	case "devin":
		// `devin auth login` is a TUI over this same PKCE + localhost
		// callback, so magpie runs the round itself
		if ln, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
			return err
		}
		s.redirect = fmt.Sprintf("http://127.0.0.1:%d/callback", ln.Addr().(*net.TCPAddr).Port)
		q.Set("redirect_uri", s.redirect)
		q.Set("state", s.state)
		q.Set("prompt", "select_account")
		q.Set("code_challenge", challenge)
		q.Set("code_challenge_method", "S256")
		q.Set("cli_pkce_marker", "1")
		// set with the lock: the window follows a sign-in that installed
		// devin first while this runs
		s.mu.Lock()
		s.st.URL = devinAuthorizeURL + "?" + q.Encode()
		s.mu.Unlock()
	case "kiro":
		// Kiro's sign-in page, as the Kiro IDE opens it
		if ln, err = startKiroSignIn(s, challenge); err != nil {
			return err
		}
	case "copilot":
		// GitHub's device code, as Copilot's editors sign in
		if err := startCopilotSignIn(s); err != nil {
			return err
		}
	case "workbuddy", WorkBuddyAIID:
		// Tencent's sign-in, as WorkBuddy (CodeBuddy) or WorkBuddy AI makes it
		if err := startWorkBuddySignIn(s, wbSiteOf(agent)); err != nil {
			return err
		}
	case CommandCodePlanID:
		// Command Code's browser sign-in, as its CLI makes it
		if err := startCommandCodeSignIn(s); err != nil {
			return err
		}
	case "qoder", QoderCNID:
		// Qoder's device flow on the account's site (qoder.com or qoder.cn),
		// run by magpie and kept in its own store
		if err := startQoderSignIn(s, qoderSiteOf(agent)); err != nil {
			return err
		}
	case "zed":
		// Zed's own sign-in: zed.dev sends the browser back to a port magpie
		// listens on, with the account's token encrypted to magpie's key
		if err := startZedSignIn(s); err != nil {
			return err
		}
	case "factory":
		// WorkOS's device code, as droid signs in to Factory
		if err := startFactorySignIn(s); err != nil {
			return err
		}
	case MiMoID:
		// Xiaomi's long-poll sign-in, for the MiMo server's service
		if err := startMiMoSignIn(s); err != nil {
			return err
		}
	case "zcode":
		// Z.ai's or BigModel's sign-in, as ZCode makes it
		if err := startZCodeSignIn(s, s.site); err != nil {
			return err
		}
	case "gemini", "antigravity":
		// Google's sign-in, under the app's own OAuth client
		app, _ := googleAppOf(agent)
		if ln, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
			return err
		}
		startGoogleSignIn(s, app, ln.Addr().(*net.TCPAddr).Port, challenge)
	default:
		return fmt.Errorf("magpie can't sign in to %s accounts", agent)
	}

	if ln != nil {
		mux := http.NewServeMux()
		mux.HandleFunc("/", s.callback)
		srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
		s.mu.Lock()
		s.srv = srv
		// a browser that can't reach this port (magpie on a server, in
		// Docker) ends on a page that won't load: its address finishes it
		s.st.PasteCallback = true
		s.mu.Unlock()
		go func() { _ = srv.Serve(ln) }()
	}
	return nil
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

// SubmitSignInCallback finishes a browser sign-in whose callback could not
// reach this machine — the address the browser ended on, pasted — or a
// plugin's with the code its page showed.
func SubmitSignInCallback(id, raw string) error {
	signIns.Lock()
	s, ok := signIns.m[id]
	signIns.Unlock()
	if !ok {
		return errors.New("no such sign-in")
	}
	if s.plugin != "" {
		return s.pluginCode(raw)
	}
	if s.claude != nil {
		return s.claudePaste(raw)
	}
	if s.status().Agent == CommandCodePlanID {
		return s.commandCodeKey(raw)
	}
	return s.pastedCallback(raw)
}

// pastedCallback takes the address a sign-in's browser was sent back to,
// for a magpie that browser can't reach. Only this sign-in's own address is
// taken: its port and path, and its state. It goes through the handler the
// callback port serves, so it is checked and traded for the account just as
// the browser's own would be, and only once: whichever of the two comes
// first finishes the sign-in, and the other waits for how that went.
func (s *signInFlow) pastedCallback(raw string) error {
	s.mu.Lock()
	st, srv, redirect := s.st, s.srv, s.redirect
	s.mu.Unlock()
	if !st.PasteCallback || srv == nil {
		return errors.New("this sign-in can't be finished from a pasted address")
	}
	if st.State != "waiting" {
		return errors.New("this sign-in is over; start it again")
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "http" || u.RawQuery == "" {
		return errors.New("paste the whole address the browser ended on, starting with http://")
	}
	if !s.ownCallback(u, redirect) {
		return errors.New("that address isn't from this sign-in: paste the one its browser tab ended on")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+u.Host+u.RequestURI(), nil)
	if err != nil {
		return err
	}
	rep := &pastedReply{h: http.Header{}}
	srv.Handler.ServeHTTP(rep, req)

	s.mu.Lock()
	claimed := s.claimed
	s.mu.Unlock()
	if !claimed {
		// Kiro's page sends an AWS sign-in on to AWS: the next page to open
		if loc := rep.h.Get("Location"); rep.code/100 == 3 && strings.HasPrefix(loc, "https://") && s.kiro != nil {
			s.mu.Lock()
			s.st.URL = loc
			s.mu.Unlock()
			return nil
		}
		if st := s.status(); st.State == "failed" {
			return errors.New(st.Error)
		}
		return errors.New("that address didn't finish the sign-in; start it again")
	}
	select {
	case <-s.done:
	case <-ctx.Done():
		return errors.New("the sign-in is still finishing; magpie shows the account when it's done")
	}
	switch st := s.status(); st.State {
	case "done":
		return nil
	case "failed":
		return errors.New(st.Error)
	}
	return errors.New("the sign-in was canceled")
}

// ownCallback says whether a pasted address is where this sign-in's page
// sends the browser back to.
func (s *signInFlow) ownCallback(u *url.URL, redirect string) bool {
	want, err := url.Parse(redirect)
	if err != nil || u.Port() != want.Port() {
		return false
	}
	switch strings.ToLower(u.Hostname()) {
	case "localhost", "127.0.0.1", "::1":
	default:
		return false
	}
	q := u.Query()
	switch {
	case s.st.Agent == "zed":
		// no state: the token in it is encrypted to this sign-in's key
		return q.Get("user_id") != "" && q.Get("access_token") != ""
	case s.kiro != nil:
		if u.Path != "/oauth/callback" && u.Path != "/signin/callback" {
			return false
		}
		s.mu.Lock()
		aws := s.kiro.state
		s.mu.Unlock()
		return q.Get("state") == s.state || (aws != "" && q.Get("state") == aws)
	}
	return u.Path == want.Path && q.Get("state") == s.state
}

// pastedReply is what the callback handler answers a pasted address; only
// its status and where it sends the browser on to count.
type pastedReply struct {
	h    http.Header
	code int
}

func (p *pastedReply) Header() http.Header { return p.h }
func (p *pastedReply) WriteHeader(code int) {
	if p.code == 0 {
		p.code = code
	}
}
func (p *pastedReply) Write(b []byte) (int, error) {
	p.WriteHeader(http.StatusOK)
	return len(b), nil
}

// claim takes a sign-in's callback for one caller: the browser's own and a
// pasted address can both arrive, and a code is traded only once.
func (s *signInFlow) claim() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.st.State != "waiting" || s.claimed {
		return false
	}
	s.claimed = true
	return true
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
	if s.st.State != "waiting" && s.st.State != "installing" {
		s.mu.Unlock()
		return false
	}
	out.ID, out.Agent, out.URL, out.Code = s.st.ID, s.st.Agent, s.st.URL, s.st.Code
	s.st = out
	stop, srv := s.stop, s.srv
	s.mu.Unlock()
	if out.State == "done" {
		// signing in again brings back an account removed from magpie
		_ = ShowAccount(out.Agent)
	}
	close(s.done)
	if stop != nil {
		stop()
	}
	if srv == nil {
		return true
	}
	go func() {
		// after the browser has had its page
		time.Sleep(time.Second)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()
	return true
}

func (s *signInFlow) callback(w http.ResponseWriter, r *http.Request) {
	if s.kiro != nil {
		s.kiroCallback(w, r)
		return
	}
	switch r.URL.Path {
	case "/callback", "/auth/callback", "/oauth2callback", "/oauth-callback":
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
	if !s.claim() {
		// its address was pasted too, and that one is being finished
		signInPage(w, false, "This sign-in is already finishing", "magpie shows the account when it's done.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if app, ok := googleAppOf(s.st.Agent); ok {
		s.googleDone(ctx, w, app, q.Get("code"))
		return
	}
	l, err := s.exchange(ctx, q.Get("code"))
	if err == nil && s.st.Agent == "devin" {
		// the exchange kept it already: the CLI's own, or one beside it
		using := false
		for _, d := range devinLogins() {
			if strings.EqualFold(d.User, l.User) {
				using = d.Active
			}
		}
		s.finish(SignInState{State: "done", User: l.User, Plan: l.Plan, Using: using})
		signInPage(w, true, "You're signed in", fmt.Sprintf("%s is added to magpie. You can close this tab.", l.User))
		return
	}
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

// googleDone finishes a Gemini CLI or Antigravity sign-in: the account is
// kept beside the others, in use at once.
func (s *signInFlow) googleDone(ctx context.Context, w http.ResponseWriter, app googleApp, code string) {
	g, plan, err := googleExchange(ctx, app, code, s.verifier, s.redirect)
	if err == nil {
		err = addGoogleLogin(app.agent, g.user, plan, g.auth)
	}
	if err != nil {
		s.finish(SignInState{State: "failed", Error: err.Error()})
		signInPage(w, false, "Sign-in didn't finish", err.Error())
		return
	}
	using := false
	if own, ok := geminiOwnLogin(); ok && app.agent == "gemini" && strings.EqualFold(own.user, g.user) {
		using = true
	}
	s.finish(SignInState{State: "done", User: g.user, Plan: plan, Using: using})
	signInPage(w, true, "You're signed in", fmt.Sprintf("%s is added to magpie. You can close this tab.", g.user))
}

// exchange trades the code for tokens and makes them into a login.
func (s *signInFlow) exchange(ctx context.Context, code string) (savedLogin, error) {
	switch s.st.Agent {
	case "codex":
		return codexExchange(ctx, code, s.verifier, s.redirect)
	}
	return devinExchange(ctx, code, s.verifier, s.redirect)
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
			Detail           string `json:"detail"`
			Message          string `json:"message"` // Connect RPC errors
		}
		_ = json.Unmarshal(b, &e)
		msg := e.ErrorDescription
		if msg == "" {
			msg = e.Detail
		}
		if msg == "" {
			msg = e.Message
		}
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

// claudeLogin is a Claude sign-in as magpie keeps it.
func claudeLogin(c claudeCredentials, acct map[string]any) (savedLogin, error) {
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
	return codexLogin(tok.IDToken, tok.AccessToken, tok.RefreshToken, "")
}

// codexLogin is a ChatGPT sign-in as magpie keeps it: auth.json as `codex
// login` writes it. accountID is used when the ID token doesn't name one.
func codexLogin(idToken, accessToken, refreshToken, accountID string) (savedLogin, error) {
	id := jwtClaims(idToken)
	user := codexUser(id)
	if user == "" {
		return savedLogin{}, errors.New("ChatGPT didn't say which account signed in")
	}
	if a := claimString(id, "https://api.openai.com/auth", "chatgpt_account_id"); a != "" {
		accountID = a
	}
	auth, err := json.MarshalIndent(map[string]any{
		"OPENAI_API_KEY": nil,
		"auth_mode":      "chatgpt",
		"tokens": map[string]string{"id_token": idToken, "access_token": accessToken,
			"refresh_token": refreshToken, "account_id": accountID},
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
	ls := readLogins()
	using = !signedIn || sameLogin(live, l)
	if first := claudeStandIn(ls); !signedIn && l.Agent == "claude" && first != "" {
		// logged out of Claude Code with accounts in magpie: it stays so,
		// as the user left it (a claude.ai sign-in beside magpie's token
		// has Claude Code warn), and the account is magpie's alone. The
		// one served first till now, seen last, stays on behind it.
		using = false
		for i := range ls {
			if ls[i].Agent == "claude" && strings.EqualFold(ls[i].User, first) && !ls[i].Paused && !sameLogin(ls[i], l) {
				ls[i].On = true
			}
		}
	}
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
	if l.Agent == "claude" {
		// signed in afresh: what Claude Code kept for it beside the
		// agent's own is an older sign-in, and gives way
		forgetClaudeDir(l.User)
	}
	loginsSeenAt = time.Time{}
	forgetAccountCaches()
	return using, nil
}

// signInPage is what the browser shows at the end: magpie's, in its own
// quiet black and white, never the vendor's "return to the CLI".
// magpieLogo is magpie's mark (build/icon/magpie-small.svg), signing the
// page the browser lands on after a sign-in.
//
//go:embed magpie.svg
var magpieLogo string

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
.by{margin-top:24px;display:flex;align-items:center;justify-content:center;gap:7px;font-size:12.5px;font-weight:560;color:var(--muted);letter-spacing:.01em}
.by svg{width:20px;height:20px;fill:currentColor}
@keyframes in{from{opacity:0;transform:translateY(6px) scale(.98)}}
</style></head><body><div class="card"><div class="mark"><svg viewBox="0 0 24 24">%[3]s</svg></div>
<h1>%[1]s</h1><p>%[2]s</p><div class="by">%[4]smagpie</div></div></body></html>`, html.EscapeString(title), html.EscapeString(msg), mark, magpieLogo)
}
