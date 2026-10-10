package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// copilotFlowServer is GitHub's device flow, where token answers the
// token endpoint and user /user.
func copilotFlowServer(t *testing.T, token, user http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login/device/code":
			json.NewEncoder(w).Encode(map[string]any{"device_code": "dc", "user_code": "ABCD-1234", "verification_uri": "https://github.com/login/device", "interval": 0})
		case "/login/oauth/access_token":
			token(w, r)
		case "/user":
			user(w, r)
		case "/copilot_internal/user":
			json.NewEncoder(w).Encode(map[string]any{"copilot_plan": "individual"})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	oldDev, oldTok, oldCU, oldGU := gitHubDeviceURL, gitHubTokenURL, CopilotUserURL, GitHubUserURL
	gitHubDeviceURL, gitHubTokenURL = srv.URL+"/login/device/code", srv.URL+"/login/oauth/access_token"
	CopilotUserURL, GitHubUserURL = srv.URL+"/copilot_internal/user", srv.URL+"/user"
	t.Cleanup(func() { gitHubDeviceURL, gitHubTokenURL, CopilotUserURL, GitHubUserURL = oldDev, oldTok, oldCU, oldGU })
}

// copilotSignInEnds is how a Copilot sign-in ended, failing the test when
// it is still waiting after d.
func copilotSignInEnds(t *testing.T, d time.Duration) SignInState {
	t.Helper()
	st, err := StartSignInAt("copilot", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	end, err := WaitSignIn(ctx, st.ID)
	if err != nil {
		CancelSignIn(st.ID)
		t.Fatalf("the sign-in never ended: %+v %v", end, err)
	}
	return end
}

// GitHub's device page said "your device is now connected", and magpie
// never finished (#723, jia2): a token endpoint that keeps failing was
// asked again, silently, until the sign-in timed out 10 minutes later. It
// now fails with what GitHub answered.
func TestCopilotSignInPollKeepsFailing(t *testing.T) {
	signIn(t)
	copilotFlowServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte(`{"message":"upstream connect error"}`))
	}, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"login": "jia2"})
	})
	old := copilotPollMisses
	copilotPollMisses = 2
	defer func() { copilotPollMisses = old }()

	st := copilotSignInEnds(t, 15*time.Second)
	if st.State != "failed" || !strings.Contains(st.Error, "upstream connect error") {
		t.Fatalf("%+v", st)
	}
}

// A miss between answers is a hiccup: the sign-in still finishes.
func TestCopilotSignInPollHiccup(t *testing.T) {
	signIn(t)
	var n atomic.Int32
	copilotFlowServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch n.Add(1) {
		case 1:
			w.WriteHeader(http.StatusBadGateway)
		case 2:
			json.NewEncoder(w).Encode(map[string]any{"error": "authorization_pending"})
		case 3:
			w.WriteHeader(http.StatusBadGateway)
		default:
			json.NewEncoder(w).Encode(map[string]any{"access_token": "gho_jia"})
		}
	}, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"login": "jia2"})
	})
	old := copilotPollMisses
	copilotPollMisses = 2
	defer func() { copilotPollMisses = old }()

	if st := copilotSignInEnds(t, 15*time.Second); st.State != "done" || st.User != "jia2" {
		t.Fatalf("%+v", st)
	}
}

// After the token, asking whose account it is hung for as long as GitHub
// (or a proxy before api.github.com) held the request: the sign-in now
// fails, saying so, once copilotFinishWait has passed.
func TestCopilotSignInUserHangs(t *testing.T) {
	signIn(t)
	release := make(chan struct{})
	copilotFlowServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"access_token": "gho_jia"})
	}, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	defer close(release)
	old := copilotFinishWait
	copilotFinishWait = 300 * time.Millisecond
	defer func() { copilotFinishWait = old }()

	st := copilotSignInEnds(t, 10*time.Second)
	if st.State != "failed" || !strings.Contains(st.Error, "whose account it is") {
		t.Fatalf("%+v", st)
	}
}
