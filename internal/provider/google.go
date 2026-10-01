package provider

// Google's subscriptions: Gemini CLI's and Antigravity's Google sign-ins.
// Both are served by Google's Code Assist backend (cloudcode-pa), which
// speaks Gemini's API wrapped in an envelope of its own, under a Google
// Cloud project Code Assist gives the account. magpie signs in with each
// app's own OAuth client, keeps the tokens in logins.json and refreshes
// them in memory; Gemini CLI's own sign-in (~/.gemini/oauth_creds.json) is
// read, never written. The gateway speaks the envelope (gateway/codeassist.go).
//
// Google no longer serves Gemini CLI's sign-in to individuals: only a Code
// Assist Standard or Enterprise account, with a Google Cloud project of
// its own, gets an answer. Antigravity's serves individuals, but Google
// may suspend an account it sees used outside Antigravity.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
)

// CodeAssist is Google's Code Assist API, which magpie only speaks
// upstream, to a Gemini CLI or Antigravity account. Not in Protocols: no
// other provider serves it.
const CodeAssist Protocol = "codeassist"

// googleApp is one of Google's apps whose sign-in magpie borrows.
type googleApp struct {
	agent, name, icon, website string
	clientID, clientSecret     string
	scopes                     []string
	callback                   string // the path the browser comes back to
	loopback                   string // the host it comes back to
	base                       string // where requests go
	loadBase                   string // where the account's project is found
}

// Google's endpoints; vars so tests can point them elsewhere.
var (
	googleAuthorizeURL    = "https://accounts.google.com/o/oauth2/v2/auth"
	googleTokenURL        = "https://oauth2.googleapis.com/token"
	googleUserInfoURL     = "https://www.googleapis.com/oauth2/v2/userinfo?alt=json"
	codeAssistProd        = "https://cloudcode-pa.googleapis.com"
	codeAssistDaily       = "https://daily-cloudcode-pa.googleapis.com"
	antigravityVersionURL = "https://antigravity-hub-auto-updater-974169037036.us-central1.run.app/manifest/latest-arm64-mac.yml"
)

var geminiApp = googleApp{
	agent: "gemini", name: "Gemini CLI", icon: "geminicli-color", website: "https://github.com/google-gemini/gemini-cli",
	// Gemini CLI's own OAuth client, an installed app's (its "secret" is
	// no secret: it ships in the CLI)
	clientID:     "681255809395-oo8ft2oprdrnp9e3aqf6av3hmdib135j.apps.googleusercontent.com",
	clientSecret: "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",
	scopes: []string{"https://www.googleapis.com/auth/cloud-platform",
		"https://www.googleapis.com/auth/userinfo.email", "https://www.googleapis.com/auth/userinfo.profile"},
	callback: "/oauth2callback", loopback: "127.0.0.1",
}

var antigravityApp = googleApp{
	agent: "antigravity", name: "Antigravity", icon: "antigravity-color", website: "https://antigravity.google",
	clientID:     "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com",
	clientSecret: "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",
	scopes: []string{"https://www.googleapis.com/auth/cloud-platform",
		"https://www.googleapis.com/auth/userinfo.email", "https://www.googleapis.com/auth/userinfo.profile",
		"https://www.googleapis.com/auth/cclog", "https://www.googleapis.com/auth/experimentsandconfigs"},
	callback: "/oauth-callback", loopback: "localhost",
}

func googleAppOf(agent string) (googleApp, bool) {
	switch agent {
	case "gemini":
		a := geminiApp
		a.base, a.loadBase = codeAssistProd, codeAssistProd
		return a, true
	case "antigravity":
		a := antigravityApp
		a.base, a.loadBase = codeAssistDaily, codeAssistProd
		return a, true
	}
	return googleApp{}, false
}

// AntigravityRisk is what magpie says before an Antigravity account is
// added.
const AntigravityRisk = "Google may suspend an Antigravity account it sees used outside Antigravity. Use one you can afford to lose."

// ClaudeRisk is what magpie says before a Claude account is added, as the
// window does.
const ClaudeRisk = "Anthropic may suspend or ban a Claude account it sees used outside its own apps. magpie sends requests through Claude Code, but Anthropic may still act on them; you use it at your own risk. Use an account you can afford to lose."

// geminiCLIVersion is the Gemini CLI magpie says it is.
const geminiCLIVersion = "0.61.0"

// googleAuth is a Google sign-in as logins.json keeps it; Gemini CLI's
// oauth_creds.json has the same fields.
type googleAuth struct {
	AccessToken  string `json:"access_token,omitempty"`
	RefreshToken string `json:"refresh_token"`
	Expiry       int64  `json:"expiry_date,omitempty"` // Unix ms
	// Project is the Google Cloud project requests are billed to: the one
	// Code Assist gave the account, or one the user named (Code Assist
	// Standard and Enterprise ask for one).
	Project string `json:"project,omitempty"`
}

// googleAccount is one signed-in Google account of an app.
type googleAccount struct {
	app  googleApp
	user string
	auth googleAuth
	own  bool // Gemini CLI's own sign-in
}

// geminiDir is where Gemini CLI keeps its sign-in.
func geminiDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".gemini")
}

// geminiOwnLogin reads Gemini CLI's own Google sign-in. One another app's
// OAuth client minted there isn't Gemini CLI's: Google refreshes a token
// only for the client that minted it, so it is left alone. (Antigravity's
// CLI, agy, keeps its sign-in in the system keyring, not here.)
func geminiOwnLogin() (googleAccount, bool) {
	path := filepath.Join(geminiDir(), "oauth_creds.json")
	var a googleAuth
	if !readJSON(path, &a) || a.RefreshToken == "" {
		return googleAccount{}, false
	}
	var id struct {
		IDToken string `json:"id_token"`
	}
	readJSON(path, &id)
	if c := googleClientOf(id.IDToken); c != "" && c != geminiApp.clientID {
		return googleAccount{}, false
	}
	var accts struct {
		Active string `json:"active"`
	}
	readJSON(filepath.Join(geminiDir(), "google_accounts.json"), &accts)
	user := accts.Active
	if user == "" {
		user = "Google"
	}
	// the project Gemini CLI is told to use, in its own .env
	a.Project = envFileValue(filepath.Join(geminiDir(), ".env"), "GOOGLE_CLOUD_PROJECT")
	if a.Project == "" {
		a.Project = savedGoogleProject("gemini", user)
	}
	app, _ := googleAppOf("gemini")
	return googleAccount{app: app, user: user, auth: a, own: true}, true
}

// googleClientOf is the OAuth client a Google ID token was minted for, ""
// when there is none to tell.
func googleClientOf(idToken string) string {
	c := jwtClaims(idToken)
	if azp := claimString(c, "azp"); azp != "" {
		return azp
	}
	return claimString(c, "aud")
}

// envFileValue is one KEY=value of a dotenv file.
func envFileValue(path, key string) string {
	v, _ := edit.GetEnvFile(path, key)
	return v
}

// savedGoogleProject is the project the user named for the agent's own
// account, which logins.json keeps beside it.
func savedGoogleProject(agent, user string) string {
	loginsMu.Lock()
	defer loginsMu.Unlock()
	for _, l := range readLogins() {
		if l.Agent == agent && strings.EqualFold(l.User, user) {
			return l.Project
		}
	}
	return ""
}

// googleSaved is the sign-in of an account magpie keeps.
func googleSaved(agent string, l savedLogin) (googleAccount, bool) {
	var a googleAuth
	if l.own() || json.Unmarshal(l.Auth, &a) != nil || a.RefreshToken == "" {
		return googleAccount{}, false
	}
	if l.Project != "" {
		a.Project = l.Project
	}
	app, _ := googleAppOf(agent)
	return googleAccount{app: app, user: l.User, auth: a}, true
}

type googleLogin struct {
	Login
	acct googleAccount
}

// googleLogins lists an app's accounts, the first in use first.
func googleLogins(agent string) []googleLogin {
	own, hasOwn := googleAccount{}, false
	if agent == "gemini" {
		own, hasOwn = geminiOwnLogin()
	}
	ownUser := ""
	if hasOwn {
		ownUser = own.user
	}
	var out []googleLogin
	for _, l := range sideLogins(agent, ownUser, func(l savedLogin) bool {
		_, ok := googleSaved(agent, l)
		return ok
	}) {
		a := own
		if !l.saved.own() {
			a, _ = googleSaved(agent, l.saved)
		}
		a.user = l.User
		out = append(out, googleLogin{l.Login, a})
	}
	return out
}

func googleSide(agent string) []sideLogin {
	var out []sideLogin
	for _, g := range googleLogins(agent) {
		out = append(out, sideLogin{Login: g.Login})
	}
	return out
}

func googleLoginList(agent string) []Login { return loginsOf(googleSide(agent)) }

func switchGoogleLogin(agent, user string) error {
	return switchSideLogin(agent, user, googleSide(agent))
}

func setGoogleLoginOn(agent, user string, on bool) error {
	return setSideLoginOn(agent, user, on, googleSide(agent))
}

func forgetGoogleLogin(agent, user string) error {
	return forgetSideLogin(agent, user, googleSide(agent), nil)
}

// addGoogleLogin keeps an account magpie just signed in.
func addGoogleLogin(agent, user, plan string, a googleAuth) error {
	auth, _ := json.Marshal(a)
	ownUser := ""
	if own, ok := geminiOwnLogin(); ok && agent == "gemini" {
		ownUser = own.user
	}
	return addSideLogin(savedLogin{Agent: agent, User: user, Plan: plan, Auth: auth, Project: a.Project}, ownUser, func(savedLogin) {})
}

// SetGoogleProject names the Google Cloud project an account's requests
// go to, "" to let Code Assist pick; Code Assist Standard and Enterprise
// need one.
func SetGoogleProject(agent, user, project string) error {
	if _, ok := googleAppOf(agent); !ok {
		return fmt.Errorf("only Gemini CLI and Antigravity accounts have a Google Cloud project")
	}
	found := false
	for _, l := range googleLogins(agent) {
		if strings.EqualFold(l.User, user) {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("no %s account %q", agent, user)
	}
	googleState.Lock()
	for k := range googleState.projects {
		if strings.HasPrefix(k, agent+"\x00") {
			delete(googleState.projects, k)
		}
	}
	googleState.Unlock()
	return editSideLogin(agent, user, func(ls []savedLogin, i int) ([]savedLogin, error) {
		ls[i].Project = strings.TrimSpace(project)
		return ls, nil
	})
}

// googleState is what magpie learned of each sign-in while it runs: the
// access token, refreshed in memory (Google's refresh tokens don't turn
// over, so nothing needs writing back), and the project and plan Code
// Assist gave it.
var googleState = struct {
	sync.Mutex
	tokens   map[string]googleAuth // by refresh token
	projects map[string]googleProject
	flags    map[string]geminiFlags // by refresh token and project
}{tokens: map[string]googleAuth{}, projects: map[string]googleProject{}, flags: map[string]geminiFlags{}}

type googleProject struct {
	id, plan string
}

// token is a live access token for the account.
func (g googleAccount) token(ctx context.Context) (string, error) {
	googleState.Lock()
	t, ok := googleState.tokens[g.auth.RefreshToken]
	googleState.Unlock()
	if !ok {
		t = g.auth
	}
	if t.AccessToken != "" && time.Until(time.UnixMilli(t.Expiry)) > 2*time.Minute {
		return t.AccessToken, nil
	}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {g.auth.RefreshToken},
		"client_id": {g.app.clientID}, "client_secret": {g.app.clientSecret}}
	var fresh struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := postToken(ctx, googleTokenURL, "application/x-www-form-urlencoded", []byte(form.Encode()), &fresh); err != nil || fresh.AccessToken == "" {
		how := "sign in again from magpie"
		if g.own {
			how = "run gemini and sign in again"
		}
		msg := "no token"
		if err != nil {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s is signed out (%s); %s", g.app.name, msg, how)
	}
	t = googleAuth{AccessToken: fresh.AccessToken, RefreshToken: g.auth.RefreshToken,
		Expiry: time.Now().Add(time.Duration(fresh.ExpiresIn) * time.Second).UnixMilli()}
	googleState.Lock()
	googleState.tokens[g.auth.RefreshToken] = t
	googleState.Unlock()
	return t.AccessToken, nil
}

// userAgent is what the app says it is to Code Assist.
func (g googleApp) userAgent(model string) string {
	if g.agent == "antigravity" {
		return "antigravity/hub/" + antigravityVersion() + " " + runtime.GOOS + "/" + runtime.GOARCH
	}
	platform := map[string]string{"darwin": "darwin", "windows": "win32", "linux": "linux"}[runtime.GOOS]
	arch := map[string]string{"amd64": "x64", "arm64": "arm64"}[runtime.GOARCH]
	if model == "" {
		model = "gemini-2.5-pro"
	}
	return fmt.Sprintf("GeminiCLI/%s/%s (%s; %s; terminal)", geminiCLIVersion, model, platform, arch)
}

var antigravityVer struct {
	sync.Mutex
	v  string
	at time.Time
}

// antigravityVersion is the newest Antigravity's version, which its
// requests carry; asked of its updater at most every few hours.
func antigravityVersion() string {
	antigravityVer.Lock()
	defer antigravityVer.Unlock()
	if antigravityVer.v != "" && time.Since(antigravityVer.at) < 6*time.Hour {
		return antigravityVer.v
	}
	antigravityVer.at = time.Now()
	if antigravityVer.v == "" {
		antigravityVer.v = "2.9.1"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, antigravityVersionURL, nil)
	if err != nil {
		return antigravityVer.v
	}
	req.Header.Set("User-Agent", "electron-builder")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return antigravityVer.v
	}
	defer res.Body.Close()
	sc := bufio.NewScanner(io.LimitReader(res.Body, 64<<10))
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "version:"); ok {
			v = strings.Trim(strings.TrimSpace(v), `'"`)
			if v != "" && strings.Trim(v, "0123456789.") == "" {
				antigravityVer.v = v
			}
			break
		}
	}
	return antigravityVer.v
}

// call posts one of Code Assist's methods.
func (g googleAccount) call(ctx context.Context, base, method string, body, out any, headers map[string]string) error {
	tok, err := g.token(ctx)
	if err != nil {
		return err
	}
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1internal:"+method, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("User-Agent", g.app.userAgent(""))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", g.app.name, APIError(rb, res.Status))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(rb, out)
}

type codeAssistTier struct {
	ID                                 string `json:"id"`
	Name                               string `json:"name"`
	IsDefault                          bool   `json:"isDefault"`
	UserDefinedCloudaicompanionProject bool   `json:"userDefinedCloudaicompanionProject"`
}

// companionProject is Code Assist's project, which comes as an id or as
// {id, name}.
type companionProject string

func (p *companionProject) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*p = companionProject(s)
		return nil
	}
	var o struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(b, &o); err != nil {
		return nil
	}
	*p = companionProject(o.ID)
	return nil
}

// project is the Google Cloud project the account's requests go to, found
// (and the account set up for Code Assist, the first time) the way the
// app does it, and the plan it is on.
func (g googleAccount) project(ctx context.Context) (googleProject, error) {
	key := g.app.agent + "\x00" + g.auth.RefreshToken
	googleState.Lock()
	p, ok := googleState.projects[key]
	googleState.Unlock()
	if ok {
		return p, nil
	}
	var err error
	if g.app.agent == "antigravity" {
		p, err = g.antigravityProject(ctx)
	} else {
		p, err = g.geminiProject(ctx)
	}
	if err != nil {
		return googleProject{}, err
	}
	googleState.Lock()
	googleState.projects[key] = p
	googleState.Unlock()
	return p, nil
}

type loadCodeAssistReply struct {
	CurrentTier             *codeAssistTier  `json:"currentTier"`
	PaidTier                *codeAssistTier  `json:"paidTier"`
	AllowedTiers            []codeAssistTier `json:"allowedTiers"`
	CloudaicompanionProject companionProject `json:"cloudaicompanionProject"`
	IneligibleTiers         []struct {
		ReasonCode    string `json:"reasonCode"`
		ReasonMessage string `json:"reasonMessage"`
		TierID        string `json:"tierId"`
	} `json:"ineligibleTiers"`
}

func (r loadCodeAssistReply) plan() string {
	t := r.PaidTier
	if t == nil || t.Name == "" {
		t = r.CurrentTier
	}
	if t == nil {
		return ""
	}
	if t.Name != "" {
		return t.Name
	}
	return t.ID
}

func (r loadCodeAssistReply) defaultTier() codeAssistTier {
	for _, t := range r.AllowedTiers {
		if t.IsDefault {
			return t
		}
	}
	if len(r.AllowedTiers) > 0 {
		return r.AllowedTiers[0]
	}
	return codeAssistTier{ID: "legacy-tier", UserDefinedCloudaicompanionProject: true}
}

// geminiProject sets the account up as Gemini CLI does (its setupUser).
func (g googleAccount) geminiProject(ctx context.Context) (googleProject, error) {
	meta := map[string]any{"ideType": "IDE_UNSPECIFIED", "platform": "PLATFORM_UNSPECIFIED", "pluginType": "GEMINI"}
	want := g.auth.Project
	body := map[string]any{"metadata": meta}
	if want != "" {
		meta["duetProject"] = want
		body["cloudaicompanionProject"] = want
	}
	var load loadCodeAssistReply
	if err := g.call(ctx, g.app.loadBase, "loadCodeAssist", body, &load, nil); err != nil {
		return googleProject{}, err
	}
	if load.CurrentTier != nil {
		if load.CloudaicompanionProject != "" {
			return googleProject{string(load.CloudaicompanionProject), load.plan()}, nil
		}
		if want != "" {
			return googleProject{want, load.plan()}, nil
		}
		return googleProject{}, g.needsProject("")
	}
	tier := load.defaultTier()
	if tier.UserDefinedCloudaicompanionProject && want == "" {
		why := ""
		for _, t := range load.IneligibleTiers {
			if t.ReasonMessage != "" {
				why = t.ReasonMessage
				break
			}
		}
		return googleProject{}, g.needsProject(why)
	}
	req := map[string]any{"tierId": tier.ID, "metadata": meta}
	if tier.ID != "free-tier" {
		req["cloudaicompanionProject"] = want
	}
	id, err := g.onboard(ctx, g.app.loadBase, req, nil)
	if err != nil {
		return googleProject{}, err
	}
	if id == "" {
		id = want
	}
	if id == "" {
		return googleProject{}, g.needsProject("")
	}
	return googleProject{id, tier.Name}, nil
}

// needsProject says a Code Assist Standard or Enterprise account needs a
// Google Cloud project named, with Google's reason when it gave one.
func (g googleAccount) needsProject(why string) error {
	msg := "Google serves Gemini CLI's sign-in only to Gemini Code Assist Standard and Enterprise, which bill a Google Cloud project: name it with `magpie accounts project gemini " + g.user + " <project-id>`"
	if g.own {
		msg += " or GOOGLE_CLOUD_PROJECT in ~/.gemini/.env"
	}
	msg += ". A personal Google account is served to Antigravity instead: add it under Antigravity in magpie"
	if why != "" {
		msg = why + " — " + msg
	}
	return errors.New(msg)
}

// antigravityProject sets the account up as Antigravity does.
func (g googleAccount) antigravityProject(ctx context.Context) (googleProject, error) {
	if g.auth.Project != "" {
		return googleProject{id: g.auth.Project}, nil
	}
	var load loadCodeAssistReply
	body := map[string]any{"metadata": map[string]any{"ideType": "ANTIGRAVITY"}}
	if err := g.call(ctx, g.app.loadBase, "loadCodeAssist", body, &load, nil); err != nil {
		return googleProject{}, err
	}
	if load.CloudaicompanionProject != "" {
		return googleProject{string(load.CloudaicompanionProject), load.plan()}, nil
	}
	tier := load.defaultTier()
	if tier.ID == "" || tier.ID == "legacy-tier" {
		tier.ID = "free-tier"
	}
	req := map[string]any{"tier_id": tier.ID, "metadata": map[string]any{
		"ide_type": "ANTIGRAVITY", "ide_version": antigravityVersion(), "ide_name": "antigravity"}}
	hdr := map[string]string{
		"User-Agent":        g.app.userAgent("") + " google-api-nodejs-client/10.3.0",
		"X-Goog-Api-Client": "gl-node/22.21.1",
	}
	id, err := g.onboard(ctx, g.app.base, req, hdr)
	if err != nil {
		return googleProject{}, err
	}
	if id == "" {
		// Google's reason, when it gave one: an account it won't serve
		// Antigravity to (its region, its age) says so in ineligibleTiers
		msg := "Antigravity hasn't set this Google account up (it gave no project); sign in to the Antigravity app with it once, then try again"
		for _, t := range load.IneligibleTiers {
			if t.ReasonMessage != "" {
				msg = "Antigravity won't serve this Google account: " + t.ReasonMessage
				break
			}
		}
		return googleProject{}, errors.New(msg)
	}
	return googleProject{id, tier.Name}, nil
}

// onboardPoll is how long onboarding waits between asks; a var for tests.
var onboardPoll = 2 * time.Second

// onboard sets the account up for Code Assist, asking again until it is
// done, and returns the project it was given.
func (g googleAccount) onboard(ctx context.Context, base string, req map[string]any, hdr map[string]string) (string, error) {
	for i := 0; i < 5; i++ {
		var lro struct {
			Done     bool `json:"done"`
			Response struct {
				Project companionProject `json:"cloudaicompanionProject"`
			} `json:"response"`
		}
		if err := g.call(ctx, base, "onboardUser", req, &lro, hdr); err != nil {
			return "", err
		}
		if lro.Done {
			return string(lro.Response.Project), nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(onboardPoll):
		}
	}
	return "", errors.New(g.app.name + ": setting the account up for Code Assist is taking long; try again in a minute")
}

// ---- models -------------------------------------------------------------------

// geminiModels are the models Gemini CLI (0.61) offers, when Code Assist
// doesn't say which this account has; which it has is
// geminiFlags.offered.
var geminiModels = []catalog.Model{
	{ID: "gemini-3.1-pro-preview", Name: "Gemini 3.1 Pro Preview"},
	{ID: "gemini-3-pro-preview", Name: "Gemini 3 Pro Preview"},
	{ID: "gemini-3-flash-preview", Name: "Gemini 3 Flash Preview"},
	{ID: "gemini-3.8-flash", Name: "Gemini 3.8 Flash"},
	{ID: "gemini-3.5-flash", Name: "Gemini 3.5 Flash"},
	{ID: "gemini-3.5-flash-lite", Name: "Gemini 3.5 Flash Lite"},
	{ID: "gemini-3.1-flash-lite", Name: "Gemini 3.1 Flash Lite"},
	{ID: "gemini-2.5-pro", Name: "Gemini 2.5 Pro"},
	{ID: "gemini-2.5-flash", Name: "Gemini 2.5 Flash"},
	{ID: "gemini-2.5-flash-lite", Name: "Gemini 2.5 Flash Lite"},
}

// geminiFlags are the experiments Code Assist has an account in that
// decide which models Gemini CLI offers it, and the id each goes out as.
type geminiFlags struct {
	gemini31        bool // Gemini 3.1 Pro launched to it
	noPro           bool // it has no Pro model
	latestFlash     bool // Gemini 3.8 Flash in place of 3.5 Flash
	latestFlashLite bool // Gemini 3.5 Flash Lite in place of 3.1 Flash Lite
	at              time.Time
}

// Gemini CLI's ids of those experiments (its ExperimentFlags).
const (
	flagGemini31Pro     = 45760185
	flagProNoAccess     = 45768879
	flagLatestFlash     = 45842815
	flagLatestFlashLite = 45827489
)

// flags asks Code Assist which experiments the account is in, as Gemini
// CLI does when it starts, at most every hour; none is on when it can't
// say, as in the CLI.
func (g googleAccount) flags(ctx context.Context, project string) geminiFlags {
	key := g.auth.RefreshToken + "\x00" + project
	googleState.Lock()
	f, ok := googleState.flags[key]
	googleState.Unlock()
	if ok && time.Since(f.at) < time.Hour {
		return f
	}
	platform := map[string]string{"darwin/amd64": "DARWIN_AMD64", "darwin/arm64": "DARWIN_ARM64", "linux/amd64": "LINUX_AMD64",
		"linux/arm64": "LINUX_ARM64", "windows/amd64": "WINDOWS_AMD64"}[runtime.GOOS+"/"+runtime.GOARCH]
	if platform == "" {
		platform = "PLATFORM_UNSPECIFIED"
	}
	meta := map[string]any{"ideName": "IDE_UNSPECIFIED", "pluginType": "GEMINI", "ideVersion": geminiCLIVersion,
		"platform": platform, "updateChannel": "stable", "duetProject": project}
	var res struct {
		Flags []struct {
			FlagID    int64 `json:"flagId"`
			BoolValue bool  `json:"boolValue"`
		} `json:"flags"`
	}
	f = geminiFlags{at: time.Now()}
	if err := g.call(ctx, g.app.base, "listExperiments", map[string]any{"project": project, "metadata": meta}, &res, nil); err == nil {
		for _, fl := range res.Flags {
			switch fl.FlagID {
			case flagGemini31Pro:
				f.gemini31 = fl.BoolValue
			case flagProNoAccess:
				f.noPro = fl.BoolValue
			case flagLatestFlash:
				f.latestFlash = fl.BoolValue
			case flagLatestFlashLite:
				f.latestFlashLite = fl.BoolValue
			}
		}
	}
	googleState.Lock()
	googleState.flags[key] = f
	googleState.Unlock()
	return f
}

// offered is what Gemini CLI's /model offers the account (its
// getAvailableModelOptions): previews only to one with quota for one,
// 3.1 Pro in place of 3 Pro once launched to it, and one Flash and one
// Flash Lite, the latest where Google has rolled it out to it.
func (f geminiFlags) offered(preview bool) []catalog.Model {
	skip := map[string]bool{
		"gemini-3.1-pro-preview": !f.gemini31 || !preview || f.noPro,
		"gemini-3-pro-preview":   f.gemini31 || !preview || f.noPro,
		"gemini-3-flash-preview": !preview,
		"gemini-3.8-flash":       !f.latestFlash,
		"gemini-3.5-flash":       f.latestFlash,
		"gemini-3.5-flash-lite":  !f.latestFlashLite,
		"gemini-3.1-flash-lite":  f.latestFlashLite,
		"gemini-2.5-pro":         f.noPro,
		"gemini-2.5-flash":       f.latestFlash, // the CLI's 2.5 Flash is 3.8 Flash then
	}
	var out []catalog.Model
	for _, m := range geminiModels {
		if !skip[m.ID] {
			m.Images = true
			out = append(out, m)
		}
	}
	return out
}

// wire is the id Gemini CLI sends Code Assist a model as (its
// getBackendModelMappings): 3.5 Flash goes as 3.8 Flash where that is
// rolled out, else as gemini-3-flash, and 3.1 Flash Lite as 3.5 Flash
// Lite where that is; 3.8 Flash and 3.5 Flash Lite where they aren't go
// as the account's Flash and Flash Lite.
func (f geminiFlags) wire(model string) string {
	switch {
	case f.latestFlash && (model == "gemini-3.5-flash" || model == "gemini-3-flash"):
		return "gemini-3.8-flash"
	case !f.latestFlash && model == "gemini-3.5-flash":
		return "gemini-3-flash"
	case f.latestFlashLite && model == "gemini-3.1-flash-lite":
		return "gemini-3.5-flash-lite"
	case !f.latestFlash && model == "gemini-3.8-flash":
		// asked for by an account it isn't rolled out to (picked from the
		// list magpie falls back on): Code Assist may not serve it that
		// account, so it goes as the Flash the account has (蒙面人: 502)
		return "gemini-3-flash"
	case !f.latestFlashLite && model == "gemini-3.5-flash-lite":
		return "gemini-3.1-flash-lite"
	}
	return model
}

// shown is the model Gemini CLI takes a quota bucket to be for (its
// refreshUserQuota): Code Assist's gemini-3-flash is 3.5 or 3.8 Flash.
func (f geminiFlags) shown(model string) string {
	switch model {
	case "gemini-3-flash", "gemini-3.5-flash", "gemini-3.8-flash":
		if f.latestFlash {
			return "gemini-3.8-flash"
		}
		return "gemini-3.5-flash"
	case "gemini-3.1-flash-lite", "gemini-3.5-flash-lite":
		if f.latestFlashLite {
			return "gemini-3.5-flash-lite"
		}
		return "gemini-3.1-flash-lite"
	}
	return model
}

// antigravityModels are the models Antigravity offers, when it doesn't say.
var antigravityModels = []catalog.Model{
	{ID: "gemini-pro-agent", Name: "Gemini 3.1 Pro (High)"},
	{ID: "gemini-3.1-pro-low", Name: "Gemini 3.1 Pro (Low)"},
	{ID: "gemini-3-flash", Name: "Gemini 3 Flash"},
	{ID: "gemini-3.1-flash-lite", Name: "Gemini 3.1 Flash Lite"},
	{ID: "claude-opus-4-6-thinking", Name: "Claude Opus 4.6 (Thinking)"},
	{ID: "claude-sonnet-4-6", Name: "Claude Sonnet 4.6"},
	{ID: "gpt-oss-120b-medium", Name: "GPT-OSS 120B (Medium)"},
}

// antigravityHidden are the models Antigravity lists that aren't for chat.
var antigravityHidden = map[string]bool{
	"chat_20706": true, "chat_23310": true, "tab_flash_lite_preview": true, "tab_jump_flash_lite_preview": true,
	"gemini-2.5-flash-thinking": true, "gemini-2.5-pro": true,
}

// googleModelInfo is one model an account has, with how much of its
// allowance is left.
type googleModelInfo struct {
	catalog.Model
	remaining float64 // 0..1, -1 not known
	resets    time.Time
}

// modelInfo asks Code Assist which models the account has.
func (g googleAccount) modelInfo(ctx context.Context) ([]googleModelInfo, error) {
	p, err := g.project(ctx)
	if err != nil {
		return nil, err
	}
	var out []googleModelInfo
	if g.app.agent == "antigravity" {
		var res struct {
			Models map[string]struct {
				DisplayName string `json:"displayName"`
				QuotaInfo   *struct {
					RemainingFraction *float64 `json:"remainingFraction"`
					ResetTime         string   `json:"resetTime"`
				} `json:"quotaInfo"`
			} `json:"models"`
		}
		if err := g.call(ctx, g.app.base, "fetchAvailableModels", map[string]any{"project": p.id}, &res, nil); err != nil {
			return nil, err
		}
		for id, m := range res.Models {
			if antigravityHidden[id] || strings.HasPrefix(id, "chat_") || strings.HasPrefix(id, "tab_") {
				continue
			}
			mi := googleModelInfo{Model: catalog.Model{ID: id, Name: m.DisplayName}, remaining: -1}
			if q := m.QuotaInfo; q != nil {
				if q.RemainingFraction != nil {
					mi.remaining = *q.RemainingFraction
				} else {
					mi.remaining = 0 // Antigravity leaves it out once none is left
				}
				mi.resets, _ = time.Parse(time.RFC3339, q.ResetTime)
			}
			out = append(out, mi)
		}
	} else {
		var res struct {
			Buckets []struct {
				ModelID           string   `json:"modelId"`
				RemainingFraction *float64 `json:"remainingFraction"`
				ResetTime         string   `json:"resetTime"`
				TokenType         string   `json:"tokenType"`
			} `json:"buckets"`
		}
		if err := g.call(ctx, g.app.base, "retrieveUserQuota", map[string]any{"project": p.id}, &res, nil); err != nil {
			return nil, err
		}
		f := g.flags(ctx, p.id)
		seen := map[string]bool{}
		for _, b := range res.Buckets {
			id := f.shown(b.ModelID)
			if id == "" || seen[id] || strings.HasSuffix(id, "_vertex") {
				continue
			}
			seen[id] = true
			mi := googleModelInfo{Model: catalog.Model{ID: id}, remaining: -1}
			if b.RemainingFraction != nil {
				mi.remaining = *b.RemainingFraction
			}
			mi.resets, _ = time.Parse(time.RFC3339, b.ResetTime)
			out = append(out, mi)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// models is the account's models for the catalog, named as magpie knows
// them where Code Assist doesn't name them. Gemini CLI's are the ones the
// CLI offers the account, not its quota's buckets: those name the models
// it has an allowance of, by Code Assist's ids (gemini-3-flash is the
// CLI's 3.5 Flash), and the CLI doesn't list by them.
func (g googleAccount) models(ctx context.Context) ([]catalog.Model, error) {
	infos, err := g.modelInfo(ctx)
	if err != nil {
		return nil, err
	}
	if g.app.agent != "antigravity" {
		p, err := g.project(ctx)
		if err != nil {
			return nil, err
		}
		// previews only once a bucket is for one, as in the CLI
		preview := false
		for _, mi := range infos {
			preview = preview || strings.Contains(mi.ID, "-preview")
		}
		return g.flags(ctx, p.id).offered(preview), nil
	}
	fallback := g.fallbackModels()
	names := map[string]string{}
	for _, m := range fallback {
		names[m.ID] = m.Name
	}
	var out []catalog.Model
	for _, mi := range infos {
		m := mi.Model
		if m.Name == "" {
			m.Name = names[m.ID]
		}
		m.Images = !strings.HasPrefix(m.ID, "gpt-oss")
		out = append(out, m)
	}
	if len(out) == 0 {
		return fallback, nil
	}
	return out, nil
}

func (g googleAccount) fallbackModels() []catalog.Model {
	src := geminiModels
	if g.app.agent == "antigravity" {
		src = antigravityModels
	}
	out := make([]catalog.Model, len(src))
	for i, m := range src {
		m.Images = !strings.HasPrefix(m.ID, "gpt-oss")
		out[i] = m
	}
	return out
}

// quota is how much of each model's allowance the account has used.
func (g googleAccount) quota(ctx context.Context, plan string) SubscriptionQuota {
	q := SubscriptionQuota{Provider: g.app.agent, Name: g.app.name, Icon: g.app.icon, Plan: plan, User: g.user, Windows: []QuotaWindow{}}
	infos, err := g.modelInfo(ctx)
	if err != nil {
		q.Error = err.Error()
		return q
	}
	for _, mi := range infos {
		if mi.remaining < 0 {
			continue
		}
		w := QuotaWindow{Name: mi.ID, Used: (1 - mi.remaining) * 100, Model: mi.ID}
		if mi.Name != "" {
			w.Name = mi.Name
		}
		if g.app.agent == "antigravity" {
			w.Family = antigravityVendor(mi.Model)
		}
		if !mi.resets.IsZero() {
			t := mi.resets
			w.ResetsAt = &t
			w.ResetSecs = int64(max(time.Until(t), 0) / time.Second)
		}
		q.Windows = append(q.Windows, w)
	}
	if g.app.agent == "antigravity" {
		q.Windows = append(q.Windows, g.pools(ctx, q.Windows)...)
	}
	if p, err := g.project(ctx); err == nil && q.Plan == "" {
		q.Plan = p.plan
	}
	return q
}

// pools are an Antigravity account's shared allowances, a 5-hour and a
// weekly one for each group of models drawing on one quota ("Gemini
// Models", "Claude and GPT models"), as retrieveUserQuotaSummary reports
// them; fetchAvailableModels gives each model only the 5-hour figure, the
// same for every model of a group. Each per-model window in ws is given
// its group's name as its Pool, so the GUI shows one row a group. None
// when the summary can't be read: the per-model windows stand alone. They
// are Aside: routing goes on reading the per-model windows, as before.
func (g googleAccount) pools(ctx context.Context, ws []QuotaWindow) []QuotaWindow {
	p, err := g.project(ctx)
	if err != nil {
		return nil
	}
	var res struct {
		Groups []struct {
			DisplayName string `json:"displayName"`
			Description string `json:"description"`
			Buckets     []struct {
				BucketID          string   `json:"bucketId"`
				Window            string   `json:"window"`
				RemainingFraction *float64 `json:"remainingFraction"`
				ResetTime         string   `json:"resetTime"`
				DisplayName       string   `json:"displayName"`
				Disabled          bool     `json:"disabled"`
			} `json:"buckets"`
		} `json:"groups"`
	}
	if err := g.call(ctx, g.app.base, "retrieveUserQuotaSummary", map[string]any{"project": p.id}, &res, nil); err != nil {
		return nil
	}
	var out []QuotaWindow
	for _, gr := range res.Groups {
		pool := poolName(gr.DisplayName)
		if pool == "" {
			continue
		}
		n := 0
		for _, b := range gr.Buckets {
			if b.Disabled {
				continue
			}
			w := QuotaWindow{Pool: pool, Used: 100, Aside: true}
			switch strings.ToLower(b.Window) {
			case "5h":
				w.Name, w.Span = "5 hours", 5*time.Hour
			case "weekly":
				w.Name, w.Span = "7 days", 7*24*time.Hour
			default:
				for _, s := range []string{b.DisplayName, b.Window, b.BucketID} {
					if w.Name == "" {
						w.Name = s
					}
				}
			}
			if w.Name == "" {
				continue
			}
			if b.RemainingFraction != nil {
				w.Used = (1 - *b.RemainingFraction) * 100
			} // left out once none is left, as in fetchAvailableModels
			if t, err := time.Parse(time.RFC3339, b.ResetTime); err == nil {
				w.ResetsAt = &t
				w.ResetSecs = int64(max(time.Until(t), 0) / time.Second)
			}
			out = append(out, w)
			n++
		}
		if n == 0 {
			continue
		}
		// a model is in the group its name or description names its
		// family in ("Models within this group: Claude Opus, Claude
		// Sonnet, GPT-OSS")
		about := strings.ToLower(gr.DisplayName + " " + gr.Description)
		for i := range ws {
			if ws[i].Pool == "" && ws[i].Family != "" && strings.Contains(about, strings.ToLower(ws[i].Family)) {
				ws[i].Pool = pool
			}
		}
	}
	return out
}

// PooledWindows are ws as a page of text shows them: a pool's own windows,
// named with their pool ("Gemini · 7 days"), in place of the per-model
// windows drawing on it; ws as it is when it has no pool.
func PooledWindows(ws []QuotaWindow) []QuotaWindow {
	if !slices.ContainsFunc(ws, func(w QuotaWindow) bool { return w.Pool != "" && w.Model == "" }) {
		return ws
	}
	var out []QuotaWindow
	for _, w := range ws {
		switch {
		case w.Pool == "":
			out = append(out, w)
		case w.Model == "":
			w.Name = w.Pool + " · " + w.Name
			out = append(out, w)
		}
	}
	return out
}

// poolName is a quota group's name without "models" at its end, its "and"
// an ampersand: "Gemini Models" is Gemini, "Claude and GPT models" Claude
// & GPT.
func poolName(s string) string {
	s = strings.TrimSpace(s)
	if l := strings.ToLower(s); strings.HasSuffix(l, " models") {
		s = strings.TrimSpace(s[:len(s)-len(" models")])
	}
	return strings.ReplaceAll(s, " and ", " & ")
}

// antigravityVendor is the family a model of Antigravity's is in — Gemini,
// Claude, GPT-OSS — as its id begins; one it doesn't know by its name's
// first word. (antigravityFamily is one model's levels, a narrower thing.)
func antigravityVendor(m catalog.Model) string {
	id := strings.ToLower(m.ID)
	switch {
	case strings.HasPrefix(id, "gemini"):
		return "Gemini"
	case strings.HasPrefix(id, "claude"):
		return "Claude"
	case strings.HasPrefix(id, "gpt-oss"):
		return "GPT-OSS"
	}
	if f, _, _ := strings.Cut(strings.TrimSpace(m.Name), " "); f != "" {
		return f
	}
	f, _, _ := strings.Cut(m.ID, "-")
	return f
}

// ---- the provider -------------------------------------------------------------

// googleProvider is an app's Google account as a provider.
func googleProvider(g googleAccount, plan string) Provider {
	acct := &Account{Agent: g.app.agent, User: g.user, Plan: plan, Stream: true, codeAssist: g.app.base}
	acct.sign = func(ctx context.Context, req *http.Request, body []byte) error {
		tok, err := g.token(ctx)
		if err != nil {
			return err
		}
		p, err := g.project(ctx)
		if err != nil {
			return err
		}
		var f geminiFlags
		if g.app.agent != "antigravity" {
			f = g.flags(ctx, p.id)
		}
		out, model, err := codeAssistEnvelope(g.app.agent, body, p.id, f)
		if err != nil {
			return err
		}
		req.Body = io.NopCloser(bytes.NewReader(out))
		req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(out)), nil }
		req.ContentLength = int64(len(out))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("User-Agent", g.app.userAgent(model))
		req.Header.Del("Accept")
		return nil
	}
	acct.models = g.fallbackModels
	acct.fetch = func(ctx context.Context) ([]catalog.Model, error) {
		ms, err := g.models(ctx)
		if err != nil {
			return nil, err
		}
		return ms, catalog.SaveLive(g.app.agent, g.app.base, ms)
	}
	return Provider{ID: g.app.agent, Name: g.app.name, Icon: g.app.icon, Website: g.app.website, Account: acct}
}

// codeAssistEnvelope finishes a request the gateway built — {model,
// request} — with what only the account knows: its project, the ids the
// app sends along, and (Gemini CLI's) the id the model goes out as.
func codeAssistEnvelope(agent string, body []byte, project string, flags geminiFlags) ([]byte, string, error) {
	var env map[string]any
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, "", err
	}
	model, _ := env["model"].(string)
	env["project"] = project
	id := randomToken(12)
	if agent == "antigravity" {
		env["userAgent"] = "antigravity"
		env["requestType"] = "agent"
		env["requestId"] = "agent-" + id
		if r, ok := env["request"].(map[string]any); ok {
			r["sessionId"] = antigravitySession(r)
		}
	} else {
		env["user_prompt_id"] = id
		// the User-Agent keeps the model asked for, as the CLI's does
		if model != "" {
			env["model"] = flags.wire(model)
		}
	}
	out, err := json.Marshal(env)
	return out, model, err
}

// antigravitySession names the conversation as Antigravity does: from its
// first message, so every turn of one conversation has the same.
func antigravitySession(r map[string]any) string {
	first := ""
	if cs, ok := r["contents"].([]any); ok {
		for _, c := range cs {
			cm, _ := c.(map[string]any)
			if cm["role"] != "user" {
				continue
			}
			parts, _ := cm["parts"].([]any)
			for _, p := range parts {
				if t, ok := p.(map[string]any)["text"].(string); ok && t != "" {
					first = t
					break
				}
			}
			break
		}
	}
	if first == "" {
		first = randomToken(16)
	}
	sum := sha256.Sum256([]byte(first))
	n := int64(binary.BigEndian.Uint64(sum[:8]) & 0x7fffffffffffffff)
	return "-" + strconv.FormatInt(n, 10)
}

// googleAccountOf is the app's account in use first.
func googleAccountOf(agent string) (Provider, bool) {
	ls := googleLogins(agent)
	if len(ls) == 0 {
		return Provider{}, false
	}
	return googleProvider(ls[0].acct, ls[0].Plan), true
}

// googleAlsoOn is the app's accounts in use behind the first.
func googleAlsoOn(agent string) []Provider {
	var out []Provider
	for _, l := range googleLogins(agent) {
		if !l.Active && l.On {
			out = append(out, googleProvider(l.acct, l.Plan))
		}
	}
	return out
}

// googleLoginQuota is one account's allowance.
func googleLoginQuota(ctx context.Context, l Login) SubscriptionQuota {
	for _, g := range googleLogins(l.Agent) {
		if strings.EqualFold(g.User, l.User) {
			return g.acct.quota(ctx, l.Plan)
		}
	}
	return SubscriptionQuota{Provider: l.Agent, Plan: l.Plan, Windows: []QuotaWindow{}, Error: "not signed in"}
}

// ---- signing in -----------------------------------------------------------------

// startGoogleSignIn builds the Google sign-in page's link for the app.
func startGoogleSignIn(s *signInFlow, app googleApp, port int, challenge string) {
	s.redirect = fmt.Sprintf("http://%s:%d%s", app.loopback, port, app.callback)
	q := url.Values{}
	q.Set("client_id", app.clientID)
	q.Set("redirect_uri", s.redirect)
	q.Set("response_type", "code")
	q.Set("scope", strings.Join(app.scopes, " "))
	q.Set("access_type", "offline")
	q.Set("prompt", "consent")
	q.Set("state", s.state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	s.st.URL = googleAuthorizeURL + "?" + q.Encode()
}

// googleExchange trades the code for tokens, finds whose account it is and
// sets it up for Code Assist.
func googleExchange(ctx context.Context, app googleApp, code, verifier, redirect string) (googleAccount, string, error) {
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect},
		"client_id": {app.clientID}, "client_secret": {app.clientSecret}, "code_verifier": {verifier}}
	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := postToken(ctx, googleTokenURL, "application/x-www-form-urlencoded", []byte(form.Encode()), &tok); err != nil {
		return googleAccount{}, "", err
	}
	if tok.AccessToken == "" || tok.RefreshToken == "" {
		return googleAccount{}, "", errors.New("Google sent back no token")
	}
	g := googleAccount{app: app, auth: googleAuth{AccessToken: tok.AccessToken, RefreshToken: tok.RefreshToken,
		Expiry: time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second).UnixMilli()}}
	who, err := googleWho(ctx, tok.AccessToken)
	if err != nil {
		return googleAccount{}, "", err
	}
	g.user = who
	// kept even when Code Assist has no project for it yet: a Standard
	// account gets one named afterwards
	plan := ""
	if p, err := g.project(ctx); err == nil {
		g.auth.Project, plan = p.id, p.plan
	}
	return g, plan, nil
}

// googleWho is whose Google account an access token is.
func googleWho(ctx context.Context, access string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, googleUserInfoURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+access)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	var who struct {
		Email string `json:"email"`
	}
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK || json.Unmarshal(b, &who) != nil || who.Email == "" {
		return "", errors.New("Google didn't say whose account this is")
	}
	return who.Email, nil
}
