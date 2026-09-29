package provider

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/dimagent"
)

func dimagentSignInPort(t *testing.T, busy bool) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if busy {
		t.Cleanup(func() { _ = ln.Close() })
	} else {
		_ = ln.Close()
	}
	old := dimagentCallbackAddr
	dimagentCallbackAddr = addr
	t.Cleanup(func() { dimagentCallbackAddr = old })
	return addr
}

func dimagentWaitSignIn(t *testing.T, id string) SignInState {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	st, err := WaitSignIn(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestDimAgentSignInCallbacks(t *testing.T) {
	for _, mode := range []string{"browser", "paste", "busy port"} {
		t.Run(mode, func(t *testing.T) {
			signIn(t)
			addr := dimagentSignInPort(t, mode == "busy port")
			var exchanges atomic.Int32
			var challenge string
			dimagentSite(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case dimagent.TokenEndpointPath:
					exchanges.Add(1)
					_ = r.ParseForm()
					hash := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
					if r.Form.Get("code") != "test-code" || r.Form.Get("redirect_uri") != dimagent.RedirectURI ||
						base64.RawURLEncoding.EncodeToString(hash[:]) != challenge {
						t.Error("exchange did not use this sign-in's code, redirect and PKCE")
					}
					fmt.Fprintf(w, `{"access_token":%q,"refresh_token":"fresh-refresh","expires_in":604800}`,
						fakeJWT(map[string]any{"sub": "new@x", "email": "new@x"}))
				case "/v1/models":
					_, _ = io.WriteString(w, `{"data":[{"id":"gpt-4o"}]}`)
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					w.WriteHeader(404)
				}
			})
			st, err := StartSignIn("dimagent")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { CancelSignIn(st.ID) })
			if st.State != "waiting" || !st.PasteCallback || st.URL == "" {
				t.Fatalf("sign-in did not remain available: %+v", st)
			}
			auth, _ := url.Parse(st.URL)
			challenge = auth.Query().Get("code_challenge")
			query := url.Values{"code": {"test-code"}, "state": {auth.Query().Get("state")}}
			if mode == "browser" {
				resp, err := http.Get("http://" + addr + "/auth/callback?" + query.Encode())
				if err != nil {
					t.Fatal(err)
				}
				body, _ := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				if !strings.Contains(html.UnescapeString(string(body)), "You're signed in") {
					t.Fatalf("browser result: %s", body)
				}
			} else if err := SubmitSignInCallback(st.ID, dimagent.RedirectURI+"?"+query.Encode()); err != nil {
				t.Fatal(err)
			}
			finished := dimagentWaitSignIn(t, st.ID)
			if finished.State != "done" || finished.User != "new@x" || exchanges.Load() != 1 {
				t.Fatalf("result %+v, exchanges %d", finished, exchanges.Load())
			}
			l, ok := dimagentLookup("new@x")
			c, valid := dimagentSaved(l)
			if !ok || !valid || c.Refresh != "fresh-refresh" || len(c.Models) == 0 {
				t.Fatal("sign-in did not save the credentials and models")
			}
			if err := SubmitSignInCallback(st.ID, dimagent.RedirectURI+"?"+query.Encode()); err == nil {
				t.Fatal("accepted a callback after sign-in finished")
			}
		})
	}
}

func TestDimAgentCallbackValidationAndCancel(t *testing.T) {
	signIn(t)
	dimagentSignInPort(t, true)
	st, err := StartSignIn("dimagent")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { CancelSignIn(st.ID) })
	auth, _ := url.Parse(st.URL)
	query := url.Values{"code": {"test-code"}, "state": {auth.Query().Get("state")}}
	for _, raw := range []string{
		"http://localhost.attacker.test:54321/auth/callback?" + query.Encode(),
		"http://127.0.0.1.attacker.test:54321/auth/callback?" + query.Encode(),
		"http://user@localhost:54321/auth/callback?" + query.Encode(),
		"http://localhost:1234/auth/callback?" + query.Encode(),
		"https://localhost:54321/auth/callback?" + query.Encode(),
		"http://localhost:54321/other?" + query.Encode(),
		dimagent.RedirectURI + "?state=" + query.Get("state"),
		dimagent.RedirectURI + "?code=test-code&state=wrong",
	} {
		if err := SubmitSignInCallback(st.ID, raw); err == nil {
			t.Errorf("accepted invalid callback %s", raw)
		}
	}
	if current, _ := SignInStatus(st.ID); current.State != "waiting" {
		t.Fatalf("invalid input ended the sign-in: %+v", current)
	}
	CancelSignIn(st.ID)
	if err := SubmitSignInCallback(st.ID, dimagent.RedirectURI+"?"+query.Encode()); err == nil {
		t.Fatal("accepted a canceled callback")
	}
	if dimagentWaitSignIn(t, st.ID).State != "canceled" {
		t.Fatal("sign-in was not canceled")
	}
}

func TestDimAgentCallbackOnlyExchangedOnce(t *testing.T) {
	signIn(t)
	dimagentSignInPort(t, true)
	started, release := make(chan struct{}), make(chan struct{})
	var unblock sync.Once
	defer unblock.Do(func() { close(release) })
	var exchanges atomic.Int32
	dimagentSite(t, func(w http.ResponseWriter, r *http.Request) {
		if exchanges.Add(1) == 1 {
			close(started)
		}
		<-release
		w.WriteHeader(http.StatusUnauthorized)
	})
	st, err := StartSignIn("dimagent")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { CancelSignIn(st.ID) })
	auth, _ := url.Parse(st.URL)
	raw := dimagent.RedirectURI + "?code=test-code&state=" + auth.Query().Get("state")
	if err := SubmitSignInCallback(st.ID, raw); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("exchange did not start")
	}
	if err := SubmitSignInCallback(st.ID, raw); err == nil {
		t.Fatal("accepted a second callback while exchanging the code")
	}
	unblock.Do(func() { close(release) })
	if dimagentWaitSignIn(t, st.ID).State != "failed" || exchanges.Load() != 1 {
		t.Fatalf("exchanged the callback %d times", exchanges.Load())
	}
}

func TestDimAgentBrowserShowsExchangeFailure(t *testing.T) {
	signIn(t)
	addr := dimagentSignInPort(t, false)
	dimagentSite(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) })
	st, err := StartSignIn("dimagent")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { CancelSignIn(st.ID) })
	auth, _ := url.Parse(st.URL)
	resp, err := http.Get("http://" + addr + "/auth/callback?code=test-code&state=" + auth.Query().Get("state"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(html.UnescapeString(string(body)), "Sign-in didn't finish") || dimagentWaitSignIn(t, st.ID).State != "failed" {
		t.Fatalf("browser reported success before the exchange: %s", body)
	}
}
