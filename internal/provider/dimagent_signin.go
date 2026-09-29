package provider

// DimAgent's sign-in, run by magpie: its OAuth page is opened, the browser
// comes back to the port its client registered (54321, fixed by the
// upstream), and the code the callback carries is traded for the account's
// tokens, which are kept with the other subscriptions.
//
// The port is the difficulty: the DimAgent app itself answers there while it
// runs, and only one process can. So magpie listens when it can and, when it
// can't, or when the browser is on another machine, the user pastes the
// callback URL the browser was turned away from — the same way a device flow
// is finished by hand.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/dimagent"
)

// dimagentCallbackAddr is where the browser is sent back; a var so tests can
// move it off the fixed port the real flow uses.
var dimagentCallbackAddr = fmt.Sprintf("127.0.0.1:%d", dimagent.CallbackPort)

// startDimAgentSignIn begins it: the page to open, and the listener waiting
// for the browser to come back.
func startDimAgentSignIn(s *signInFlow) error {
	pkce, err := dimagent.NewPKCE()
	if err != nil {
		return fmt.Errorf("DimAgent sign-in: %w", err)
	}
	state, err := dimagent.NewState()
	if err != nil {
		return fmt.Errorf("DimAgent sign-in: %w", err)
	}
	authURL, err := dimagent.AuthURL(dimagentAPI, state, pkce)
	if err != nil {
		return fmt.Errorf("DimAgent sign-in: %w", err)
	}
	// A busy port still permits the OAuth round: the user can paste the
	// browser's final callback URL instead of delivering it to our listener.
	ln, _ := net.Listen("tcp", dimagentCallbackAddr)
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.st.URL, s.stop = authURL, cancel
	s.st.PasteCallback, s.state = true, state
	s.dimagentDone = make(chan dimagentCallback, 1)
	done := s.dimagentDone
	s.mu.Unlock()

	if ln != nil {
		mux := http.NewServeMux()
		mux.HandleFunc("/auth/callback", func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			got := dimagentCallback{Code: q.Get("code"), State: q.Get("state"),
				Error: firstNonEmpty(q.Get("error"), q.Get("error_description"))}
			if err := s.submitDimAgentCallback(got); err != nil {
				signInPage(w, false, "Sign-in didn't finish", err.Error())
				return
			}
			select {
			case <-s.done:
				st := s.status()
				if st.State == "done" {
					signInPage(w, true, "You're signed in", "DimAgent is added to magpie. You can close this tab.")
				} else {
					signInPage(w, false, "Sign-in didn't finish", firstNonEmpty(st.Error, "the sign-in was canceled"))
				}
			case <-r.Context().Done():
			}
		})
		srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
		s.mu.Lock()
		s.srv = srv
		s.mu.Unlock()
		go func() { _ = srv.Serve(ln) }()
	}

	go func() {
		defer cancel()
		var got dimagentCallback
		select {
		case got = <-done:
		case <-ctx.Done():
			return
		case <-time.After(signInTimeout):
			s.finish(SignInState{State: "failed", Error: "the sign-in timed out; start it again"})
			return
		}
		if got.State != state {
			s.finish(SignInState{State: "failed", Error: "DimAgent's callback came back with another sign-in's state; start it again"})
			return
		}
		if got.Error != "" {
			s.finish(SignInState{State: "failed", Error: "DimAgent: " + got.Error})
			return
		}
		user, err := dimagentSignedInWith(ctx, got.Code, pkce)
		if err != nil {
			s.finish(SignInState{State: "failed", Error: err.Error()})
			return
		}
		ls := dimagentSide()
		s.finish(SignInState{State: "done", User: user, Using: strings.EqualFold(activeOf(ls), user)})
	}()
	return nil
}

type dimagentCallback struct{ Code, State, Error string }

// Both the local listener and a pasted URL go through the same state and
// one-shot checks before the authorization code can be exchanged.
func (s *signInFlow) submitDimAgentCallback(got dimagentCallback) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dimagentDone == nil || s.st.Agent != "dimagent" {
		return errors.New("this sign-in does not accept a callback URL")
	}
	if s.st.State != "waiting" || s.dimagentSubmitted {
		return errors.New("this sign-in is no longer waiting for a callback")
	}
	if got.State == "" || got.State != s.state {
		return errors.New("DimAgent's callback came back with another sign-in's state; use the URL from this sign-in")
	}
	if got.Code == "" && got.Error == "" {
		return errors.New("that URL carries no code")
	}
	s.dimagentSubmitted = true
	s.dimagentDone <- got
	return nil
}

// dimagentSignedInWith trades a code for tokens, keeps the account, and reads
// what the upstream lists for it.
func dimagentSignedInWith(ctx context.Context, code string, pkce *dimagent.PKCECodes) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tok, err := dimagent.ExchangeCode(ctx, dimagentClient, dimagentAPI, code, pkce)
	if err != nil {
		return "", err
	}
	c := dimagentCreds{Access: tok.AccessToken, Refresh: tok.RefreshToken, ExpiresAt: tok.Expiry().UnixMilli()}
	claims := dimagentTokenClaims(tok)
	c.UID, c.Sub, c.Nickname = claims.UID, claims.Sub, claims.Nickname
	if c.Nickname == "" {
		c.Nickname = claims.Email
	}
	if c.Sub == "" {
		c.Sub = claims.Email
	}
	c.Plan = tok.Plan()
	who := dimagentWho(c)
	// the account's own list, so the picker has it before its first request
	raw, err := dimagent.FetchModels(ctx, dimagentClient, dimagentAPI, c.Access)
	if err == nil {
		if _, perr := dimagent.ParseModels(raw); perr == nil {
			c.Models = raw
		}
	}
	auth, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	if err := addSideLogin(savedLogin{Agent: "dimagent", User: who, Plan: c.Plan, Auth: auth}, "", func(savedLogin) {}); err != nil {
		return "", err
	}
	forgetAccountCaches()
	return who, nil
}

// dimagentTokenClaims is who the account said it is, out of the id token, or
// the access token when there is no id token. magpie reads them for display
// only: the upstream is what decides who is calling.
func dimagentTokenClaims(tok *dimagent.TokenResponse) struct{ UID, Sub, Nickname, Email string } {
	var out struct{ UID, Sub, Nickname, Email string }
	for _, t := range []string{tok.IDToken, tok.AccessToken} {
		claims := jwtClaims(t)
		if claims == nil {
			continue
		}
		if out.UID == "" {
			out.UID = claimString(claims, "uid")
		}
		if out.Sub == "" {
			out.Sub = claimString(claims, "sub")
		}
		if out.Nickname == "" {
			out.Nickname = firstNonEmpty(claimString(claims, "preferred_username"), claimString(claims, "name"))
		}
		if out.Email == "" {
			out.Email = claimString(claims, "email")
		}
		if out.UID != "" && out.Sub != "" && out.Nickname != "" {
			break
		}
	}
	return out
}

// DimAgentCallbackFromPaste is a callback URL pasted by hand, when the
// listener couldn't be had: the browser was sent there and refused, and the
// address in its bar is what finishes the sign-in.
func dimAgentCallbackFromPaste(raw string) (dimagentCallback, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return dimagentCallback{}, errors.New("that isn't a URL")
	}
	if u.Scheme != "http" || u.User != nil || u.Port() != fmt.Sprint(dimagent.CallbackPort) ||
		(!strings.EqualFold(u.Hostname(), "localhost") && u.Hostname() != "127.0.0.1") {
		return dimagentCallback{}, fmt.Errorf("that isn't DimAgent's callback address (%s)", dimagent.RedirectURI)
	}
	if u.Path != "/auth/callback" {
		return dimagentCallback{}, fmt.Errorf("that isn't DimAgent's callback path (%s)", dimagent.RedirectURI)
	}
	q := u.Query()
	got := dimagentCallback{Code: q.Get("code"), State: q.Get("state"),
		Error: firstNonEmpty(q.Get("error"), q.Get("error_description"))}
	if got.Code == "" && got.Error == "" {
		return dimagentCallback{}, errors.New("that URL carries no code")
	}
	return got, nil
}
