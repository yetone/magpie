package provider

// Signing in to Kiro from magpie, the way the Kiro IDE does: Kiro's sign-in
// page (app.kiro.dev) offers Google, GitHub, AWS Builder ID and IAM
// Identity Center, and sends the browser back to a port on this machine.
// Google and GitHub come back with a code Kiro's auth service trades for
// tokens; Builder ID and Identity Center come back with where to sign in
// at AWS, which is then done with a client registered for it, as the IDE
// does. Each account signed in is magpie's own, kept in a home of its own
// (kiro_accounts.go), so kiro-cli's and the IDE's are left as they are.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
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
	"strings"
	"time"
)

// Where Kiro signs in; vars so tests can answer them.
var (
	kiroPortalURL   = "https://app.kiro.dev"
	kiroAuthService = "https://prod.us-east-1.auth.desktop.kiro.dev"
	kiroOIDC        = func(region string) string { return "https://oidc." + region + ".amazonaws.com" }
	// the ports Kiro's sign-in page sends the browser back to, the IDE's
	kiroCallbackPorts = []int{3128, 4649, 6588, 8008, 9091, 49153, 50153, 51153, 52153, 53153}
)

// kiroScopes are what the IDE asks AWS for.
var kiroScopes = []string{"codewhisperer:completions", "codewhisperer:analysis", "codewhisperer:conversations",
	"codewhisperer:transformations", "codewhisperer:taskassist"}

// kiroIDEUA is how the IDE names itself to Kiro's auth service.
var kiroIDEUA = func() string {
	h, _ := os.Hostname()
	sum := sha256.Sum256([]byte("magpie:" + h))
	return "KiroIDE-1.1.70-" + hex.EncodeToString(sum[:])
}()

// kiroSaved is a Kiro sign-in of magpie's as it is kept: the IDE's shape, with
// an AWS sign-in's client beside it.
type kiroSaved struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresAt    string `json:"expiresAt"`
	AuthMethod   string `json:"authMethod"` // social | IdC
	Provider     string `json:"provider"`   // Google, Github, BuilderId, Enterprise
	Region       string `json:"region,omitempty"`
	ProfileArn   string `json:"profileArn,omitempty"`
	ClientID     string `json:"clientId,omitempty"`
	ClientSecret string `json:"clientSecret,omitempty"`
}

// readKiroFile reads a sign-in made in magpie.
func readKiroFile(path string) (kiroCred, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return kiroCred{}, false
	}
	var t kiroSaved
	if json.Unmarshal(b, &t) != nil || t.AccessToken == "" {
		return kiroCred{}, false
	}
	c := kiroCred{access: t.AccessToken, refresh: t.RefreshToken, region: t.Region, profile: t.ProfileArn,
		clientID: t.ClientID, clientSecret: t.ClientSecret, idePath: path}
	c.expires, _ = time.Parse(time.RFC3339Nano, t.ExpiresAt)
	if c.region == "" {
		c.region = "us-east-1"
	}
	if strings.EqualFold(t.AuthMethod, "IdC") && c.clientID != "" {
		c.method = "idc"
	} else {
		c.method, c.social = "social", true
	}
	return c, true
}

// kiroFlow is where a Kiro sign-in is between its two pages.
type kiroFlow struct {
	port int
	// an AWS sign-in, once Kiro's page has sent the browser on to it
	region, clientID, clientSecret, provider string
	state, verifier                          string
}

// startKiroSignIn opens Kiro's sign-in page, to come back to one of the
// ports it sends browsers back to.
func startKiroSignIn(s *signInFlow, challenge string) (net.Listener, error) {
	var ln net.Listener
	var err error
	for _, p := range kiroCallbackPorts {
		if ln, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p)); err == nil {
			break
		}
	}
	if ln == nil {
		return nil, errors.New("the ports Kiro's sign-in comes back to are all busy; close the Kiro IDE's sign-in and try again")
	}
	port := ln.Addr().(*net.TCPAddr).Port
	s.kiro = &kiroFlow{port: port}
	s.redirect = fmt.Sprintf("http://localhost:%d", port)
	q := url.Values{}
	q.Set("state", s.state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("redirect_uri", s.redirect)
	q.Set("redirect_from", "KiroIDE")
	s.mu.Lock()
	s.st.URL = kiroPortalURL + "/signin?" + q.Encode()
	s.mu.Unlock()
	return ln, nil
}

// kiroCallback is the browser back from Kiro's page, or from AWS's.
func (s *signInFlow) kiroCallback(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/oauth/callback", "/signin/callback":
	default:
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	if s.status().State != "waiting" {
		signInPage(w, false, "This sign-in is over", "Start it again from magpie.")
		return
	}
	fail := func(msg string) {
		s.finish(SignInState{State: "failed", Error: msg})
		signInPage(w, false, "Sign-in didn't finish", msg)
	}
	if e := q.Get("error"); e != "" {
		msg := q.Get("error_description")
		if msg == "" {
			msg = e
		}
		fail(msg)
		return
	}
	k := s.kiro
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var saved kiroSaved
	switch {
	case k.state != "" && q.Get("state") == k.state:
		// back from AWS
		if q.Get("code") == "" {
			fail("AWS sent back no code")
			return
		}
		var tok struct {
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
			ExpiresIn    int    `json:"expiresIn"`
		}
		err := kiroSignInPost(ctx, kiroOIDC(k.region)+"/token", map[string]string{"clientId": k.clientID, "clientSecret": k.clientSecret,
			"grantType": "authorization_code", "redirectUri": k.awsRedirect(), "code": q.Get("code"), "codeVerifier": k.verifier}, &tok)
		if err == nil && tok.AccessToken == "" {
			err = errors.New("AWS sent back no token")
		}
		if err != nil {
			fail(err.Error())
			return
		}
		saved = kiroSaved{AccessToken: tok.AccessToken, RefreshToken: tok.RefreshToken, ExpiresAt: kiroExpiry(tok.ExpiresIn),
			AuthMethod: "IdC", Provider: k.provider, Region: k.region, ClientID: k.clientID, ClientSecret: k.clientSecret}
	case q.Get("state") != s.state:
		// not ours: someone else's page, or a stale tab
		signInPage(w, false, "This link isn't from magpie's sign-in", "Start it again from magpie.")
		return
	default:
		switch opt := q.Get("login_option"); opt {
		case "google", "github":
			var tok struct {
				AccessToken  string `json:"accessToken"`
				RefreshToken string `json:"refreshToken"`
				ProfileArn   string `json:"profileArn"`
				ExpiresIn    int    `json:"expiresIn"`
			}
			err := kiroSignInPost(ctx, kiroAuthService+"/oauth/token", map[string]string{"code": q.Get("code"),
				"code_verifier": s.verifier, "redirect_uri": s.redirect + r.URL.Path + "?login_option=" + opt}, &tok)
			if err == nil && tok.AccessToken == "" {
				err = errors.New("Kiro sent back no token")
			}
			if err != nil {
				fail(err.Error())
				return
			}
			provider := map[string]string{"google": "Google", "github": "Github"}[opt]
			saved = kiroSaved{AccessToken: tok.AccessToken, RefreshToken: tok.RefreshToken, ExpiresAt: kiroExpiry(tok.ExpiresIn),
				AuthMethod: "social", Provider: provider, Region: "us-east-1", ProfileArn: tok.ProfileArn}
		case "builderid", "awsidc", "internal":
			// on to AWS, with a client registered for this sign-in
			issuer, region := q.Get("issuer_url"), q.Get("idc_region")
			if issuer == "" || region == "" {
				fail("Kiro's page didn't say where to sign in at AWS")
				return
			}
			var reg struct {
				ClientID     string `json:"clientId"`
				ClientSecret string `json:"clientSecret"`
			}
			err := kiroSignInPost(ctx, kiroOIDC(region)+"/client/register", map[string]any{"clientName": "Kiro IDE", "clientType": "public",
				"scopes": kiroScopes, "grantTypes": []string{"authorization_code", "refresh_token"},
				"redirectUris": []string{"http://127.0.0.1/oauth/callback"}, "issuerUrl": issuer}, &reg)
			if err == nil && reg.ClientID == "" {
				err = errors.New("AWS registered no client")
			}
			if err != nil {
				fail(err.Error())
				return
			}
			k.region, k.clientID, k.clientSecret = region, reg.ClientID, reg.ClientSecret
			k.provider = map[string]string{"builderid": "BuilderId", "awsidc": "Enterprise", "internal": "Internal"}[opt]
			k.state, k.verifier = randomToken(24), randomToken(48)
			sum := sha256.Sum256([]byte(k.verifier))
			a := url.Values{}
			a.Set("response_type", "code")
			a.Set("client_id", k.clientID)
			a.Set("redirect_uri", k.awsRedirect())
			a.Set("scopes", strings.Join(kiroScopes, ","))
			a.Set("state", k.state)
			a.Set("code_challenge", base64.RawURLEncoding.EncodeToString(sum[:]))
			a.Set("code_challenge_method", "S256")
			http.Redirect(w, r, kiroOIDC(region)+"/authorize?"+a.Encode(), http.StatusFound)
			return
		case "external_idp":
			fail("magpie can't sign in with a company's own identity provider yet; sign in with `kiro-cli login`, and magpie uses that")
			return
		default:
			fail(fmt.Sprintf("Kiro's page came back with a sign-in magpie doesn't know (%q)", opt))
			return
		}
	}
	home, err := newKiroHome()
	if err != nil {
		fail(err.Error())
		return
	}
	b, _ := json.MarshalIndent(saved, "", "  ")
	if err := writePrivate(filepath.Join(home, kiroTokenFile), append(b, '\n')); err != nil {
		removeKiroHome(home)
		fail(err.Error())
		return
	}
	user, err := addKiroLogin(home)
	if err != nil {
		removeKiroHome(home)
		fail(err.Error())
		return
	}
	forgetAccountCaches()
	var plan string
	using := false
	for _, l := range kiroLogins() {
		if strings.EqualFold(l.User, user) {
			plan, using = l.Plan, l.Active
		}
	}
	s.finish(SignInState{State: "done", User: user, Plan: plan, Using: using})
	signInPage(w, true, "You're signed in", fmt.Sprintf("%s is added to magpie. You can close this tab.", user))
}

// awsRedirect is where AWS sends the browser back.
func (k *kiroFlow) awsRedirect() string {
	return fmt.Sprintf("http://127.0.0.1:%d/oauth/callback", k.port)
}

func kiroExpiry(in int) string {
	if in <= 0 {
		in = 3600
	}
	return time.Now().Add(time.Duration(in) * time.Second).UTC().Format(time.RFC3339Nano)
}

// kiroSignInPost posts JSON to Kiro's auth service or AWS's sign-in.
func kiroSignInPost(ctx context.Context, u string, body any, dst any) error {
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", kiroIDEUA)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode/100 != 2 {
		var e struct {
			Message          string `json:"message"`
			Error            string `json:"error"`
			ErrorDescription string `json:"error_description"`
		}
		_ = json.Unmarshal(out, &e)
		msg := e.ErrorDescription
		if msg == "" {
			msg = e.Message
		}
		if msg == "" {
			msg = e.Error
		}
		if msg == "" {
			msg = http.StatusText(res.StatusCode)
		}
		return fmt.Errorf("the sign-in was refused (%d): %s", res.StatusCode, msg)
	}
	return json.Unmarshal(out, dst)
}
