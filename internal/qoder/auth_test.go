package qoder

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestAuthorizationURL checks the device-flow page carries PKCE S256 and the
// client id, and returns the verifier+nonce the later poll needs.
func TestAuthorizationURL(t *testing.T) {
	f := NewDeviceFlow(nil)
	authURL, verifier, nonce, err := f.Authorization()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(authURL, DeviceFlowHost+DeviceSelectAccountsPath) {
		t.Fatalf("url %q", authURL)
	}
	for _, q := range []string{"challenge=", "challenge_method=S256", "client_id=" + ClientID, "nonce=", "redirect_uri="} {
		if !strings.Contains(authURL, strings.TrimSuffix(q, "=")) {
			t.Fatalf("url missing %q: %s", q, authURL)
		}
	}
	if verifier == "" || nonce == "" {
		t.Fatal("no verifier/nonce")
	}
	u, _ := url.Parse(authURL)
	if u.Query().Get("machine_id") != f.MachineID() {
		t.Fatal("login machine id differs from flow")
	}
	second, _, _, _ := f.Authorization()
	u, _ = url.Parse(second)
	if u.Query().Get("machine_id") != f.MachineID() {
		t.Fatal("flow changed its machine id")
	}
}

// TestDeviceFlowAndRefresh runs the real poll -> jobToken -> refresh rounds
// against a stub of the OpenAPI host (repointed through openAPIHost), checking
// each round trips the right fields and headers.
func TestDeviceFlowAndRefresh(t *testing.T) {
	var pollHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case DeviceTokenPollPath:
			pollHits++
			if pollHits < 2 { // first try: not authorized yet
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "dt-abc", "refresh_token": "drt", "user_id": "uid-9"})
		case JobTokenPath:
			if r.Header.Get("Authorization") != "Bearer dt-abc" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "jt-xyz", "refresh_token": "jrt", "expires_in": 3600000})
		case JobTokenRefreshPath:
			var b struct {
				RefreshToken string `json:"refresh_token"`
			}
			_ = json.NewDecoder(r.Body).Decode(&b)
			if b.RefreshToken != "jrt" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "jt-new", "refresh_token": "jrt2", "expires_in": 7200000})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	// Repoint the package host at the stub so the real round-trip functions run
	// against it (they build their URLs from openAPIHost + the path constants).
	host := openAPIHost
	openAPIHost = srv.URL
	defer func() { openAPIHost = host }()
	client := srv.Client()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	f := NewDeviceFlow(client)
	dt, err := f.PollDeviceToken(ctx, "nonce", "verifier", time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if dt.Token != "dt-abc" || dt.UserID != "uid-9" {
		t.Fatalf("poll: %+v", dt)
	}
	jt, err := f.JobToken(ctx, dt.Token)
	if err != nil {
		t.Fatal(err)
	}
	if jt.Token != "jt-xyz" || jt.Expiry() != time.Hour {
		t.Fatalf("jobToken: %+v", jt)
	}
	updated, err := RefreshJobToken(ctx, client, "jrt")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Token != "jt-new" || updated.RefreshToken != "jrt2" || updated.Expiry() != 2*time.Hour {
		t.Fatalf("refresh: %+v", updated)
	}
}

// TestFetchUserInfo checks the account endpoint is asked with the device token
// and its email and name are decoded.
func TestFetchUserInfo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != UserInfoPath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer dt-abc" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"id":"u1","name":"Ethan Xu","email":"e@x.com","source":"sso.google"}`))
	}))
	defer srv.Close()
	host := openAPIHost
	openAPIHost = srv.URL
	defer func() { openAPIHost = host }()

	ui, err := FetchUserInfo(context.Background(), srv.Client(), "dt-abc")
	if err != nil {
		t.Fatal(err)
	}
	if ui.Email != "e@x.com" || ui.Name != "Ethan Xu" || ui.ID != "u1" {
		t.Fatalf("userinfo: %+v", ui)
	}
	if _, err := FetchUserInfo(context.Background(), srv.Client(), ""); err == nil {
		t.Fatal("empty device token should error")
	}
}

// TestParseModels checks enabled, routable chat models are kept with magpie's
// fields, and aggregate "auto" and disabled entries are dropped.
func TestParseModels(t *testing.T) {
	body := []byte(`{"chat":[
		{"key":"qfmodel","display_name":"Qwen3.8-Flash","enable":true,"is_vl":true,"max_input_tokens":200000},
		{"key":"auto","enable":true},
		{"key":"off-model","enable":false}]}`)
	ms, err := ParseModels(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 {
		t.Fatalf("want 1 routable model, got %d: %+v", len(ms), ms)
	}
	m := ms[0]
	if m.ID != "qfmodel" || m.Name != "Qwen3.8-Flash" || m.Provider != ProviderKey || m.Context != 200000 || !m.Images {
		t.Fatalf("model mapping wrong: %+v", m)
	}
}
