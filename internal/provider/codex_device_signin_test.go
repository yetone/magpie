package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeCodexDevice answers Codex's device sign-in as auth.openai.com does:
// a code, 403 until it is entered (polls before entered), then the
// authorization code, traded at the token endpoint with the verifier and
// the device callback.
func fakeCodexDevice(t *testing.T, pending int32) (trades *atomic.Int32) {
	t.Helper()
	trades = new(atomic.Int32)
	var polls atomic.Int32
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("originator") != "codex_cli_rs" {
			http.Error(w, "not Codex", 400)
			return
		}
		switch r.URL.Path {
		case "/api/accounts/deviceauth/usercode":
			var in struct {
				ClientID string `json:"client_id"`
			}
			if json.NewDecoder(r.Body).Decode(&in) != nil || in.ClientID != codexClientID {
				http.Error(w, "bad client", 400)
				return
			}
			// as auth.openai.com answered on 2026-11-03
			w.Write([]byte(`{"device_auth_id":"deviceauth_6ae9","user_code":"WG0E-J29UL","interval":"0","expires_at":"2026-11-03T00:15:14.704241+00:00"}`))
		case "/api/accounts/deviceauth/token":
			var in struct {
				DeviceAuthID string `json:"device_auth_id"`
				UserCode     string `json:"user_code"`
			}
			json.NewDecoder(r.Body).Decode(&in)
			if in.DeviceAuthID != "deviceauth_6ae9" || in.UserCode != "WG0E-J29UL" {
				http.Error(w, "unknown", 400)
				return
			}
			if polls.Add(1) <= pending {
				w.WriteHeader(403)
				w.Write([]byte(`{"error":{"message":"Device authorization is unknown. Please try again.","code":"deviceauth_authorization_unknown"}}`))
				return
			}
			w.Write([]byte(`{"authorization_code":"dc","code_challenge":"ch","code_verifier":"ver-dc"}`))
		case "/oauth/token":
			r.ParseForm()
			trades.Add(1)
			if r.Form.Get("code") != "dc" || r.Form.Get("code_verifier") != "ver-dc" ||
				r.Form.Get("redirect_uri") != codexIssuer+"/deviceauth/callback" || r.Form.Get("client_id") != codexClientID {
				w.WriteHeader(400)
				json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant", "error_description": "bad code"})
				return
			}
			json.NewEncoder(w).Encode(map[string]any{
				"id_token": fakeJWT(map[string]any{"email": "dc@example.com",
					"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "pro", "chatgpt_account_id": "acct-dc"}}),
				"access_token":  fakeJWT(map[string]any{"exp": float64(time.Now().Add(time.Hour).Unix())}),
				"refresh_token": "r-dc",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fake.Close)
	oldIss, oldTok := codexIssuer, codexTokenURL
	t.Cleanup(func() { codexIssuer, codexTokenURL = oldIss, oldTok })
	codexIssuer, codexTokenURL = fake.URL, fake.URL+"/oauth/token"
	return trades
}

func waitSignIn(t *testing.T, id string) SignInState {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if st, _ := SignInStatus(id); st.State != "waiting" {
			return st
		}
	}
	st, _ := SignInStatus(id)
	t.Fatalf("still waiting: %+v", st)
	return st
}

// curtinp118 on Discord: magpie on a VPS, its web UI in a browser at home.
// The browser sign-in ends on localhost:1455, which is the home machine.
// A device code entered on ChatGPT's page signs in with nothing to bring
// back, and nothing listens on this computer for it.
func TestCodexSignInByDeviceCode(t *testing.T) {
	home := claudeHome(t)
	trades := fakeCodexDevice(t, 2)

	st, err := StartSignInWith("codex", SignInOptions{Site: CodexDeviceSite})
	if err != nil {
		t.Fatal(err)
	}
	defer CancelSignIn(st.ID)
	if st.Code != "WG0E-J29UL" || st.URL != codexIssuer+"/codex/device" {
		t.Fatalf("no device code shown: %+v", st)
	}
	if st.PasteCallback {
		t.Fatal("a device sign-in waits on a callback address")
	}
	st = waitSignIn(t, st.ID)
	if st.State != "done" || st.User != "dc@example.com" || !st.Using || st.Code != "WG0E-J29UL" {
		t.Fatalf("state %+v", st)
	}
	var a codexAuth
	readJSON(filepath.Join(home, ".codex", "auth.json"), &a)
	if a.Tokens.RefreshToken != "r-dc" || a.Tokens.AccountID != "acct-dc" {
		t.Fatalf("auth.json %+v", a)
	}
	if n := trades.Load(); n != 1 {
		t.Fatalf("%d trades", n)
	}
}

// Canceled before the code is entered: no account, no trade.
func TestCodexDeviceSignInCanceled(t *testing.T) {
	home := claudeHome(t)
	trades := fakeCodexDevice(t, 1<<30)
	st, err := StartSignInWith("codex", SignInOptions{Site: CodexDeviceSite})
	if err != nil {
		t.Fatal(err)
	}
	CancelSignIn(st.ID)
	if st, _ = SignInStatus(st.ID); st.State != "canceled" {
		t.Fatalf("state %+v", st)
	}
	if n := trades.Load(); n != 0 {
		t.Fatalf("%d trades", n)
	}
	var a codexAuth
	if readJSON(filepath.Join(home, ".codex", "auth.json"), &a); a.Tokens.RefreshToken != "" {
		t.Fatalf("auth.json written: %+v", a)
	}
}

// ChatGPT turning device codes away (404, as Codex reads it) says to use
// the browser's sign-in, not a bare status.
func TestCodexDeviceSignInOff(t *testing.T) {
	claudeHome(t)
	fake := httptest.NewServer(http.NotFoundHandler())
	defer fake.Close()
	old := codexIssuer
	codexIssuer = fake.URL
	defer func() { codexIssuer = old }()
	_, err := StartSignInWith("codex", SignInOptions{Site: CodexDeviceSite})
	if err == nil || !strings.Contains(err.Error(), "browser sign-in") {
		t.Fatalf("err %v", err)
	}
}
