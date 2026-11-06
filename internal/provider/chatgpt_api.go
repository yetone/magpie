package provider

// A ChatGPT API subscription is a ChatGPT plan used through OpenAI's own
// API, as OpenAI's Sign in with ChatGPT lets an open-source app that runs
// on the user's machine (#933): the account's access token is sent to
// api.openai.com/v1/responses with the agent's own instructions, nothing of
// Codex's in it. It sits beside the Codex subscription, which signs in as
// Codex CLI and asks ChatGPT's Codex backend, and changes nothing of that.
//
// The sign-in registers magpie with OpenAI on the way (a public client,
// no secret): the browser is sent to OpenAI's authorize page as
// dynamic_agent_client, and comes back with the client id OpenAI issued
// beside the code, which the code, every refresh and the sign-out go with.
// ext_agent_host_id names this machine, the same every time. Each account
// keeps its own client id. Plus shares one usage limit (five hours)
// between ChatGPT and every app it is used in; OpenAI says how much is left
// only at chatgpt.com/settings/usage.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/filelock"
)

// ChatGPTAPIID is the subscription's id, as a provider and in logins.json.
const ChatGPTAPIID = "chatgpt-api"

// Where OpenAI's sign-in and API are; vars so tests can point them
// elsewhere. siwcResource is what the token is for, whichever API answers.
var (
	siwcIssuer       = "https://auth.openai.com"
	siwcAuthorizeURL = "https://auth.openai.com/api/accounts/authorize"
	siwcTokenURL     = "https://auth.openai.com/api/accounts/oauth/token"
	siwcAPI          = "https://api.openai.com/v1"
	// siwcCallbackAddr is where the browser is sent back to first, as
	// OpenAI's own apps are; another port when that one is busy (a Codex
	// sign-in waiting there), only the port of a loopback address may vary
	siwcCallbackAddr = "127.0.0.1:1455"
)

const (
	siwcResource = "https://api.openai.com/v1"
	// siwcDynamicClient registers the app on its first sign-in
	siwcDynamicClient = "dynamic_agent_client"
	siwcAgentName     = "magpie"
	// siwcDirectScope lets the token be sent to api.openai.com itself
	siwcDirectScope = "chatgpt.tokens.use.direct"
	siwcScopes      = "openid profile email offline_access resource.invoke " + siwcDirectScope
	// siwcMargin is how long before it expires a token is refreshed, so no
	// request starts with one about to
	siwcMargin = 3 * time.Minute
	// SIWCUsageURL is where a ChatGPT account's usage is shown
	SIWCUsageURL = "https://chatgpt.com/settings/usage"
)

var siwcClient = &http.Client{Timeout: 30 * time.Second, CheckRedirect: KeepsCredentials}

// siwcNoUsage is what a ChatGPT API account's usage line says: OpenAI's
// API tells no plan usage, and the account added as ChatGPT (Codex's
// backend) does.
const siwcNoUsage = "a ChatGPT API account's usage isn't told through OpenAI's API, only at " + SIWCUsageURL + "; the same account added as ChatGPT shows it here"

// ErrSIWCSignIn is an account OpenAI no longer refreshes: it must be
// signed in again.
var ErrSIWCSignIn = errors.New("the ChatGPT sign-in has expired — sign in again")

// siwcCreds is what a sign-in leaves: the client id OpenAI issued, this
// machine's id it was issued for, who signed in and the tokens.
type siwcCreds struct {
	ClientID string    `json:"clientId"`
	HostID   string    `json:"hostId"`
	Subject  string    `json:"sub,omitempty"`
	Email    string    `json:"email,omitempty"`
	IDToken  string    `json:"idToken,omitempty"`
	Access   string    `json:"access"`
	Refresh  string    `json:"refresh"`
	Expires  time.Time `json:"expires"`
	Scopes   []string  `json:"scopes,omitempty"`
}

func siwcSaved(l savedLogin) (siwcCreds, bool) {
	var c siwcCreds
	if json.Unmarshal(l.Auth, &c) != nil || c.ClientID == "" || c.Refresh == "" {
		return siwcCreds{}, false
	}
	return c, true
}

func siwcSide() []sideLogin {
	return sideLogins(ChatGPTAPIID, "", func(l savedLogin) bool {
		_, ok := siwcSaved(l)
		return ok
	})
}

// siwcLoginList lists the accounts, one OpenAI no longer refreshes said
// on its row (siwcRefresh's Lapsed), which then offers Sign in again: the
// lapse was kept, and shown nowhere but on a failed request.
func siwcLoginList() []Login {
	ls := siwcSide()
	for i := range ls {
		if ls[i].Lapsed == "" {
			ls[i].Lapsed = ls[i].saved.Lapsed
		}
		if c, ok := siwcSaved(ls[i].saved); ok {
			ls[i].Until = chatgptUntil(time.Now(), c.IDToken, c.Access)
		}
	}
	return loginsOf(ls)
}

func switchSIWCLogin(user string) error { return switchSideLogin(ChatGPTAPIID, user, siwcSide()) }

func setSIWCLoginOn(user string, on bool) error {
	return setSideLoginOn(ChatGPTAPIID, user, on, siwcSide())
}

// forgetSIWCLogin removes an account, and has OpenAI revoke its refresh
// token: signed out, it is gone there too. The revoke leaves through the
// account's own proxy, as its sign-in and requests did (#1663), read
// before the account is forgotten.
func forgetSIWCLogin(user string) error {
	via := ViaLogin(context.Background(), ChatGPTAPIID, user)
	return forgetSideLogin(ChatGPTAPIID, user, siwcSide(), func(l savedLogin) {
		if c, ok := siwcSaved(l); ok {
			go func() {
				ctx, cancel := context.WithTimeout(via, 15*time.Second)
				defer cancel()
				_ = siwcRevoke(ctx, c)
			}()
		}
	})
}

func siwcLookup(user string) (siwcCreds, bool) {
	loginsMu.Lock()
	defer loginsMu.Unlock()
	for _, l := range readLogins() {
		if l.Agent == ChatGPTAPIID && strings.EqualFold(l.User, user) {
			return siwcSaved(l)
		}
	}
	return siwcCreds{}, false
}

// siwcHostID is this machine's id for OpenAI, made once and kept.
func siwcHostID() (string, error) {
	path := filepath.Join(appdir.Config(), "chatgpt-api-host")
	if b, err := os.ReadFile(path); err == nil {
		if id := strings.TrimSpace(string(b)); strings.HasPrefix(id, "urn:uuid:") {
			return id, nil
		}
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80 // a version 4 UUID
	h := hex.EncodeToString(b)
	id := "urn:uuid:" + h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	return id, os.WriteFile(path, []byte(id+"\n"), 0o600)
}

// ---- the sign-in ------------------------------------------------------------

// listenSIWCCallback takes the port the browser is sent back to.
func listenSIWCCallback() (net.Listener, error) {
	if ln, err := net.Listen("tcp", siwcCallbackAddr); err == nil {
		return ln, nil
	}
	return net.Listen("tcp", "127.0.0.1:0")
}

// siwcAuthorize is OpenAI's page the sign-in opens.
func siwcAuthorize(redirect, state, nonce, challenge, hostID string) string {
	q := url.Values{}
	q.Set("client_id", siwcDynamicClient)
	q.Set("agent_name_hint", siwcAgentName)
	q.Set("ext_agent_host_id", hostID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", redirect)
	q.Set("resource", siwcResource)
	q.Set("scope", siwcScopes)
	q.Set("state", state)
	q.Set("nonce", nonce)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	return siwcAuthorizeURL + "?" + q.Encode()
}

// siwcDone finishes a sign-in whose browser came back with q.
func (s *signInFlow) siwcDone(ctx context.Context, w http.ResponseWriter, q url.Values) {
	user, plan, err := siwcSignedIn(ctx, q.Get("code"), strings.TrimSpace(q.Get("client_id")), s.verifier, s.redirect, s.nonce, s.hostID)
	if err != nil {
		s.finish(SignInState{State: "failed", Error: err.Error()})
		signInPage(w, false, "Sign-in didn't finish", err.Error())
		return
	}
	s.finish(SignInState{State: "done", User: user, Plan: plan, Using: strings.EqualFold(activeOf(siwcSide()), user)})
	signInPage(w, true, "You're signed in", fmt.Sprintf("%s is added to magpie. You can close this tab.", user))
}

// siwcSignedIn trades the code for the account's tokens and keeps it.
func siwcSignedIn(ctx context.Context, code, clientID, verifier, redirect, nonce, hostID string) (user, plan string, err error) {
	if clientID == "" {
		return "", "", errors.New("ChatGPT sign-in: OpenAI didn't finish registering magpie (no client id came back); start it again")
	}
	tok, err := siwcPostToken(ctx, url.Values{"grant_type": {"authorization_code"}, "client_id": {clientID},
		"code": {code}, "code_verifier": {verifier}, "redirect_uri": {redirect}, "resource": {siwcResource}})
	if err != nil {
		return "", "", fmt.Errorf("ChatGPT sign-in: %w", err)
	}
	if tok.IDToken == "" {
		return "", "", errors.New("ChatGPT sign-in: OpenAI sent no ID token")
	}
	c, err := siwcFromToken(tok, siwcCreds{ClientID: clientID, HostID: hostID})
	if err != nil {
		return "", "", fmt.Errorf("ChatGPT sign-in: %w", err)
	}
	id := jwtClaims(tok.IDToken)
	if err := siwcCheckID(id, clientID, nonce); err != nil {
		return "", "", fmt.Errorf("ChatGPT sign-in: %w", err)
	}
	c.Subject, c.Email = claimString(id, "sub"), claimString(id, "email")
	user = firstNonEmpty(c.Email, c.Subject)
	if user == "" {
		return "", "", errors.New("ChatGPT sign-in: OpenAI didn't say which account signed in")
	}
	plan = siwcPlan(c)
	auth, err := json.Marshal(c)
	if err != nil {
		return "", "", err
	}
	if err := keepLogin(ctx, func() error {
		return addSideLogin(savedLogin{Agent: ChatGPTAPIID, User: user, Plan: plan, Auth: auth}, "", func(savedLogin) {})
	}); err != nil {
		return "", "", err
	}
	// signed in again: whatever OpenAI refused before is over
	_ = editSideLogin(ChatGPTAPIID, user, func(ls []savedLogin, i int) ([]savedLogin, error) {
		ls[i].Lapsed = ""
		return ls, nil
	})
	forgetAccountCaches()
	// the account's own list, so the picker has it before its first request
	if ms, err := siwcFetchModels(ctx, user); err == nil {
		_ = catalog.SaveLive(ChatGPTAPIID, siwcAPI, ms)
	}
	return user, plan, nil
}

// siwcCheckID checks the ID token is this sign-in's: OpenAI's, for the
// client it issued, with the nonce the sign-in sent, not expired.
func siwcCheckID(id map[string]any, clientID, nonce string) error {
	if id == nil {
		return errors.New("OpenAI sent an unreadable ID token")
	}
	if iss := claimString(id, "iss"); strings.TrimRight(iss, "/") != strings.TrimRight(siwcIssuer, "/") {
		return fmt.Errorf("the ID token is from %q, not OpenAI", iss)
	}
	aud := false
	switch v := id["aud"].(type) {
	case string:
		aud = v == clientID
	case []any:
		aud = slices.Contains(v, any(clientID))
	}
	if !aud {
		return errors.New("the ID token is for another app")
	}
	if claimString(id, "nonce") != nonce {
		return errors.New("the ID token isn't this sign-in's")
	}
	if exp, ok := id["exp"].(float64); ok && time.Unix(int64(exp), 0).Before(time.Now().Add(-time.Minute)) {
		return errors.New("the ID token has expired")
	}
	return nil
}

// siwcPlan is the ChatGPT plan a token names, "" when none does.
func siwcPlan(c siwcCreds) string {
	for _, t := range []string{c.IDToken, c.Access} {
		if p := claimString(jwtClaims(t), "https://api.openai.com/auth", "chatgpt_plan_type"); p != "" {
			return p
		}
	}
	return ""
}

// siwcTokenReply is the token endpoint's answer.
type siwcTokenReply struct {
	Access    string  `json:"access_token"`
	Refresh   string  `json:"refresh_token"`
	IDToken   string  `json:"id_token"`
	ExpiresIn float64 `json:"expires_in"`
	Scope     string  `json:"scope"`
}

// siwcTokenError is the token endpoint's refusal, with its OAuth code.
type siwcTokenError struct {
	Status int
	Code   string
	Msg    string
}

func (e *siwcTokenError) Error() string {
	return strings.TrimSpace(fmt.Sprintf("%s %s", firstNonEmpty(e.Code, http.StatusText(e.Status)), e.Msg))
}

func siwcPostToken(ctx context.Context, form url.Values) (siwcTokenReply, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, siwcTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return siwcTokenReply{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	res, err := siwcClient.Do(req)
	if err != nil {
		return siwcTokenReply{}, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		var e struct {
			Error any    `json:"error"`
			Desc  string `json:"error_description"`
			Code  string `json:"code"`
		}
		_ = json.Unmarshal(b, &e)
		out := &siwcTokenError{Status: res.StatusCode, Code: e.Code, Msg: e.Desc}
		switch v := e.Error.(type) {
		case string:
			out.Code = firstNonEmpty(out.Code, v)
		case map[string]any:
			code, _ := v["code"].(string)
			msg, _ := v["message"].(string)
			out.Code, out.Msg = firstNonEmpty(out.Code, code), firstNonEmpty(out.Msg, msg)
		}
		if out.Code == "" && out.Msg == "" {
			out.Msg = APIError(b, res.Status)
		}
		return siwcTokenReply{}, out
	}
	var tok siwcTokenReply
	if err := json.Unmarshal(b, &tok); err != nil {
		return siwcTokenReply{}, fmt.Errorf("unreadable token answer: %w", err)
	}
	return tok, nil
}

// siwcFromToken is c with the tokens of a token answer in it.
func siwcFromToken(tok siwcTokenReply, c siwcCreds) (siwcCreds, error) {
	if tok.Access == "" || tok.Refresh == "" {
		return c, errors.New("OpenAI sent no token")
	}
	if tok.ExpiresIn <= 0 {
		return c, errors.New("OpenAI's token has no lifetime")
	}
	scopes := strings.Fields(tok.Scope)
	if !slices.Contains(scopes, siwcDirectScope) {
		return c, errors.New("OpenAI didn't grant magpie the use of the subscription through its API (" + siwcDirectScope + "): this ChatGPT account may not be eligible")
	}
	c.Access, c.Refresh, c.Scopes = tok.Access, tok.Refresh, scopes
	c.Expires = time.Now().Add(time.Duration(tok.ExpiresIn * float64(time.Second))).UTC()
	if tok.IDToken != "" {
		c.IDToken = tok.IDToken
	}
	return c, nil
}

// siwcDead are the refusals of a refresh token that will never work again.
var siwcDead = []string{"invalid_grant", "invalid_refresh_token", "token_expired", "refresh_token_expired",
	"refresh_token_invalidated", "refresh_token_reused"}

// siwcRefreshing keeps two requests from refreshing at once, in this
// magpie and across magpies (two run at once while a restart hands the
// gateway over): the refresh token rotates, and the second would send the
// one the first used up. The account is looked up again once it is held.
var siwcRefreshing = filelock.Mutex{Path: func() string { return loginsPath() + ".siwc.lock" }}

// siwcFresh is an account's credentials with an access token that isn't
// about to expire, refreshed when it is, or when force is set (OpenAI
// turned the one in hand away).
func siwcFresh(ctx context.Context, user string, force bool) (siwcCreds, error) {
	c, ok := siwcLookup(user)
	if !ok {
		return siwcCreds{}, fmt.Errorf("no ChatGPT account %q", user)
	}
	if !force && time.Until(c.Expires) > siwcMargin {
		return c, nil
	}
	siwcRefreshing.Lock()
	defer siwcRefreshing.Unlock()
	// refreshed by another request while this one waited
	if n, ok := siwcLookup(user); ok && n.Refresh != c.Refresh && time.Until(n.Expires) > siwcMargin {
		return n, nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	tok, err := siwcPostToken(ctx, url.Values{"grant_type": {"refresh_token"}, "client_id": {c.ClientID},
		"refresh_token": {c.Refresh}, "resource": {siwcResource}})
	var te *siwcTokenError
	if errors.As(err, &te) && slices.Contains(siwcDead, te.Code) {
		msg := user + "'s ChatGPT sign-in has expired — sign in again"
		_ = editSideLogin(ChatGPTAPIID, user, func(ls []savedLogin, i int) ([]savedLogin, error) {
			ls[i].Lapsed = msg
			return ls, nil
		})
		return siwcCreds{}, fmt.Errorf("%s: %w", user, ErrSIWCSignIn)
	}
	if errors.As(err, &te) && te.Code == "invalid_client" {
		return siwcCreds{}, fmt.Errorf("ChatGPT: OpenAI no longer knows the client magpie registered for %s (%s) — sign in again", user, te.Error())
	}
	if err != nil {
		if !force && time.Until(c.Expires) > 0 {
			return c, nil // a hiccup: the token in hand still does
		}
		return siwcCreds{}, fmt.Errorf("ChatGPT token refresh: %w", err)
	}
	if c, err = siwcFromToken(tok, c); err != nil {
		return siwcCreds{}, fmt.Errorf("ChatGPT token refresh: %w", err)
	}
	auth, err := json.Marshal(c)
	if err != nil {
		return siwcCreds{}, err
	}
	if err := editSideLogin(ChatGPTAPIID, user, func(ls []savedLogin, i int) ([]savedLogin, error) {
		ls[i].Auth, ls[i].Lapsed = auth, ""
		if p := siwcPlan(c); p != "" {
			ls[i].Plan = p
		}
		return ls, nil
	}); err != nil {
		return siwcCreds{}, err
	}
	return c, nil
}

// siwcRevoke has OpenAI revoke an account's refresh token, at the
// revocation endpoint its discovery document names.
func siwcRevoke(ctx context.Context, c siwcCreds) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(siwcIssuer, "/")+"/.well-known/openid-configuration", nil)
	if err != nil {
		return err
	}
	res, err := siwcClient.Do(req)
	if err != nil {
		return err
	}
	var d struct {
		Revoke string `json:"revocation_endpoint"`
	}
	err = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&d)
	res.Body.Close()
	if err != nil || d.Revoke == "" {
		return errors.New("ChatGPT: OpenAI names no revocation endpoint")
	}
	form := url.Values{"token": {c.Refresh}, "token_type_hint": {"refresh_token"}, "client_id": {c.ClientID}}
	req, err = http.NewRequestWithContext(ctx, http.MethodPost, d.Revoke, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if res, err = siwcClient.Do(req); err != nil {
		return err
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("ChatGPT sign-out: %s", res.Status)
	}
	return nil
}

// ---- models and requests ----------------------------------------------------

// siwcFetchModels is the account's model list: OpenAI's /v1/models for
// the token, the ones it lists for a picker, in its order.
func siwcFetchModels(ctx context.Context, user string) ([]catalog.Model, error) {
	c, err := siwcFresh(ctx, user, false)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(siwcAPI, "/")+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Access)
	res, err := siwcClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode != http.StatusOK {
		msg := "ChatGPT models: " + APIError(b, res.Status)
		if more := siwcExplain(res.StatusCode, b); more != "" {
			msg += " — " + more
		}
		return nil, errors.New(msg)
	}
	ms, hidden := parseSIWCModels(b)
	ms = siwcWithCodex(ms, hidden)
	if len(ms) == 0 {
		return nil, errors.New("ChatGPT lists no models for this account")
	}
	return ms, nil
}

// siwcWithCodex adds to the account's catalog the models Codex lists for a
// ChatGPT plan: the token is served as Codex on the plan (a model it can't
// run is "not supported when using Codex with a ChatGPT account"), and runs
// Codex's newest models the catalog leaves out (#933: gpt-6-luna,
// gpt-6-sol and gpt-6.1-sol ran, listed by neither). Codex's list is the
// one magpie's Codex subscription last fetched, else Codex CLI's cache; a
// model the catalog hides stays out.
func siwcWithCodex(ms []catalog.Model, hidden []string) []catalog.Model {
	codex, _, ok := catalog.Live("codex")
	if !ok {
		codex = catalog.Codex()
	}
	for _, m := range codex {
		if m.ID == "" || strings.Contains(m.ID, "/") || slices.Contains(hidden, m.ID) || slices.ContainsFunc(ms, func(o catalog.Model) bool { return o.ID == m.ID }) {
			continue
		}
		m.Provider, m.APIs = "openai", []string{string(Responses)}
		ms = append(ms, m)
	}
	return ms
}

// parseSIWCModels gives the catalog's models to list, and the slugs of
// those it hides.
func parseSIWCModels(b []byte) (out []catalog.Model, hidden []string) {
	var list struct {
		Models []struct {
			Slug        string   `json:"slug"`
			DisplayName string   `json:"display_name"`
			Visibility  string   `json:"visibility"`
			Input       []string `json:"input_modalities"`
			Levels      []struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
			Context int `json:"context_window"`
			Max     int `json:"max_context_window"`
		} `json:"models"`
	}
	if json.Unmarshal(b, &list) != nil {
		return nil, nil
	}
	for _, m := range list.Models {
		if m.Slug == "" {
			continue
		}
		if m.Visibility != "" && m.Visibility != "list" {
			hidden = append(hidden, m.Slug)
			continue
		}
		mm := catalog.Model{ID: m.Slug, Name: m.DisplayName, Provider: "openai", Context: m.Context, APIs: []string{string(Responses)}}
		if m.Max > m.Context {
			mm.MaxContext = m.Max
		}
		if m.Input != nil {
			yes := slices.Contains(m.Input, "image")
			mm.ImageInput, mm.Images = &yes, yes
		}
		for _, l := range m.Levels {
			mm.Efforts = append(mm.Efforts, l.Effort)
		}
		out = append(out, mm)
	}
	return out, hidden
}

// siwcDropped are the Responses fields Sign in with ChatGPT refuses: the
// request goes without them. previous_response_id too: nothing is stored,
// so the whole conversation is sent each time.
var siwcDropped = []string{"background", "conversation", "max_output_tokens", "max_tool_calls", "metadata",
	"moderation", "multi_agent", "prompt", "prompt_cache_retention", "safety_identifier", "temperature",
	"top_logprobs", "top_p", "truncation", "user", "previous_response_id"}

// siwcHostedTools are the tools OpenAI runs itself that it won't run for
// a ChatGPT token: a request offering one is refused whole.
var siwcHostedTools = []string{"image_generation", "file_search", "code_interpreter", "computer_use",
	"computer_use_preview", "computer", "mcp", "tool_search"}

// siwcBody makes a Responses request one Sign in with ChatGPT takes:
// stored nowhere and streamed, without the fields and tools it refuses,
// and a system message as the developer one it takes in its place. The
// instructions stay the agent's own.
func siwcBody(body []byte) []byte {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if dec.Decode(&m) != nil {
		return body
	}
	for _, k := range siwcDropped {
		delete(m, k)
	}
	m["store"], m["stream"] = false, true
	switch in := m["input"].(type) {
	case string:
		m["input"] = []any{map[string]any{"type": "message", "role": "user",
			"content": []any{map[string]any{"type": "input_text", "text": in}}}}
	case []any:
		for _, it := range in {
			item, ok := it.(map[string]any)
			if !ok {
				continue
			}
			if item["role"] == "system" {
				item["role"] = "developer"
			}
			// Codex's Lite hands its tools over as an input item
			if tools, ok := item["tools"].([]any); ok && item["type"] == "additional_tools" {
				item["tools"] = siwcOwnTools(tools)
			}
		}
	}
	// the conversation is named, as on a Codex account: OpenAI keeps a
	// ChatGPT sign-in's cached prompt by it (#933)
	if k, _ := m["prompt_cache_key"].(string); k == "" {
		own, _ := m["instructions"].(string)
		input, _ := m["input"].([]any)
		if k = conversationKey(own, input); k != "" {
			m["prompt_cache_key"] = k
		}
	}
	if tools, ok := m["tools"].([]any); ok {
		if tools = siwcOwnTools(tools); len(tools) == 0 {
			delete(m, "tools")
			delete(m, "tool_choice")
		} else {
			m["tools"] = tools
		}
	}
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

// siwcOwnTools is tools without those OpenAI would run itself.
func siwcOwnTools(tools []any) []any {
	return slices.DeleteFunc(tools, func(t any) bool {
		tm, _ := t.(map[string]any)
		typ, _ := tm["type"].(string)
		return slices.Contains(siwcHostedTools, typ)
	})
}

// siwcErrorCode is the code an API refusal names: error.code, or a
// detail's.
func siwcErrorCode(body []byte) (code, param string) {
	var e struct {
		Error struct {
			Code  string `json:"code"`
			Type  string `json:"type"`
			Param string `json:"param"`
		} `json:"error"`
		Detail any `json:"detail"`
	}
	if json.Unmarshal(body, &e) != nil {
		return "", ""
	}
	if e.Error.Code != "" {
		return e.Error.Code, e.Error.Param
	}
	switch d := e.Detail.(type) {
	case map[string]any:
		code, _ = d["code"].(string)
		param, _ = d["param"].(string)
		return code, param
	case string:
		for _, c := range []string{"subscription_sharing_", "chatpass_v2_"} {
			if i := strings.Index(d, c); i >= 0 {
				end := strings.IndexFunc(d[i:], func(r rune) bool { return !(r == '_' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9') })
				if end < 0 {
					end = len(d) - i
				}
				return d[i : i+end], ""
			}
		}
	}
	return e.Error.Type, ""
}

// siwcExplain says what the user can do about a refusal of OpenAI's.
func siwcExplain(status int, body []byte) string {
	code, param := siwcErrorCode(body)
	switch {
	case code == "subscription_sharing_usage_limit_exceeded":
		return "the ChatGPT plan's usage limit, shared with ChatGPT and the other apps it is used in, is reached; see " + SIWCUsageURL
	case code == "subscription_sharing_usage_unavailable", code == "subscription_sharing_user_unavailable":
		return "OpenAI can't tell the account's usage right now; try again in a moment"
	case code == "subscription_sharing_user_not_eligible":
		return "this ChatGPT account can't be used through OpenAI's API (its plan isn't eligible)"
	case code == "subscription_sharing_unsupported_capability":
		if param != "" {
			return "a ChatGPT sign-in can't use " + param + " through OpenAI's API"
		}
		return "a ChatGPT sign-in can't use part of this request through OpenAI's API"
	case code == "subscription_sharing_route_not_supported":
		return "a ChatGPT sign-in can't use this endpoint of OpenAI's API"
	case code == "subscription_sharing_invalid_user":
		return "OpenAI no longer takes this ChatGPT sign-in; sign in again"
	case strings.HasPrefix(code, "chatpass_v2_"):
		return "ChatGPT refused the account (" + code + ")"
	case status == http.StatusUnauthorized:
		return "OpenAI turned the ChatGPT sign-in away; sign in again"
	}
	return ""
}

func siwcProvider(l Login) Provider {
	user := l.User
	a := &Account{Agent: ChatGPTAPIID, User: user, Plan: l.Plan, Stream: true}
	a.sign = func(ctx context.Context, req *http.Request, body []byte) error {
		c, err := siwcFresh(ctx, user, false)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.Access)
		if body != nil {
			sessionHeaders(req.Header, body)
		}
		return nil
	}
	a.body = siwcBody
	// the GPT models' efforts and windows, for those its list leaves out
	a.models = catalog.Codex
	a.fetch = func(ctx context.Context) ([]catalog.Model, error) {
		ms, err := siwcFetchModels(ctx, user)
		if err != nil {
			return nil, err
		}
		return ms, catalog.SaveLive(ChatGPTAPIID, siwcAPI, ms)
	}
	a.explain = siwcExplain
	return Provider{ID: ChatGPTAPIID, Name: "ChatGPT API", Icon: "openai", Responses: siwcAPI,
		Website: "https://chatgpt.com", Account: a}
}

func siwcAccount() (Provider, bool) {
	ls := siwcLoginList()
	if len(ls) == 0 {
		return Provider{}, false
	}
	return siwcProvider(ls[0]), true
}

// SIWCForTest points the sign-in and the API at test servers.
func SIWCForTest(issuer, api string) func() {
	oldI, oldA, oldT, oldS := siwcIssuer, siwcAuthorizeURL, siwcTokenURL, siwcAPI
	siwcIssuer, siwcAuthorizeURL, siwcTokenURL, siwcAPI = issuer, issuer+"/api/accounts/authorize", issuer+"/api/accounts/oauth/token", api
	return func() { siwcIssuer, siwcAuthorizeURL, siwcTokenURL, siwcAPI = oldI, oldA, oldT, oldS }
}

// siwcAlsoOn is the ChatGPT API accounts in use behind the first.
func siwcAlsoOn() []Provider {
	var out []Provider
	for _, l := range siwcLoginList() {
		if !l.Active && l.On {
			out = append(out, siwcProvider(l))
		}
	}
	return out
}
