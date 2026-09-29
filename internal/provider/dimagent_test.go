package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/dimagent"
)

// dimagentKeep signs one account in with a token that runs for days, so
// nothing has to be renewed for the round a test is asking about.
func dimagentKeep(t *testing.T, user string) {
	t.Helper()
	signIn(t)
	dimagentAdd(t, user, "acc", "ref", time.Now().Add(3*24*time.Hour))
}

// dimagentAdd keeps one account with the tokens named, running out when the
// time said. A token already inside the day's head start is renewed on the
// next round. Every account is signed in afresh: one kept from an earlier
// case would still carry what that case wrote to it, its lapse included.
func dimagentAdd(t *testing.T, user, access, refresh string, expiresAt time.Time) {
	t.Helper()
	auth, err := json.Marshal(dimagentCreds{UID: "1", Access: access, Refresh: refresh,
		ExpiresAt: expiresAt.UnixMilli(), Plan: "Lite"})
	if err != nil {
		t.Fatal(err)
	}
	addSideLoginMust(t, savedLogin{Agent: "dimagent", User: user, Plan: "Lite", Auth: auth})
}

func addSideLoginMust(t *testing.T, l savedLogin) {
	t.Helper()
	if err := addSideLogin(l, "", func(savedLogin) {}); err != nil {
		t.Fatal(err)
	}
}

// dimagentKept is the account as it stands now: its tokens and what it says
// of a sign-in the vendor refused.
func dimagentKept(t *testing.T, user string) (access, refresh, lapses string) {
	t.Helper()
	for _, l := range dimagentLogins() {
		if l.User == user {
			return l.creds.Access, l.creds.Refresh, l.Lapsed
		}
	}
	t.Fatalf("no DimAgent account %q kept", user)
	return "", "", ""
}

// dimagentSite points DimAgent's site at a stub, so a round a test asks for
// goes to it and nowhere else.
func dimagentSite(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	old := dimagentAPI
	dimagentAPI = srv.URL
	t.Cleanup(func() { dimagentAPI = old })
}

// dimagentUsage points it at a stub that answers one allowance body, and
// fails a round that asks for anything else.
func dimagentUsage(t *testing.T, body string) {
	t.Helper()
	dimagentSite(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != dimagent.UsagePath {
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer acc" {
			t.Errorf("the usage asked without the account's token: %q", r.Header.Get("Authorization"))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
}

// dimagentRefused is the site of a token round the upstream ends: it answers
// every ask with the status named.
func dimagentRefused(t *testing.T, status int) {
	t.Helper()
	dimagentSite(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"message":"no such session"}`))
	})
}

// dimagentTokenRound is the site of a token round that answers with the pair
// named, counting every ask so a test can say the refresh token was spent
// once and not twice over.
func dimagentTokenRound(t *testing.T, access, refresh string, asks *atomic.Int32) {
	t.Helper()
	dimagentSite(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != dimagent.TokenEndpointPath {
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = r.ParseForm()
		asks.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": access,
			"refresh_token": refresh, "expires_in": 7 * 24 * 3600})
	})
}

// A listing fetched with the old token must not undo a renewal completed
// while the upstream was preparing that listing.
func TestDimAgentModelsKeepConcurrentRefresh(t *testing.T) {
	dimagentKeep(t, "me")
	started, release := make(chan struct{}), make(chan struct{})
	var unblock sync.Once
	defer unblock.Do(func() { close(release) })
	const models = `{"data":[{"id":"gpt-4o"}]}`
	dimagentSite(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			if r.Header.Get("Authorization") != "Bearer acc" {
				t.Error("model request did not start with the old token")
			}
			close(started)
			<-release
			_, _ = w.Write([]byte(models))
		case dimagent.TokenEndpointPath:
			_, _ = w.Write([]byte(`{"access_token":"acc-new","refresh_token":"ref-new","expires_in":604800}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := dimagentFetchModels(ctx, "me"); result <- err }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("model request did not start")
	}
	// Move the saved token into its refresh window while the listing waits.
	dimagentMu.Lock()
	l, _ := dimagentLookup("me")
	c, _ := dimagentSaved(l)
	c.ExpiresAt = time.Now().Add(time.Minute).UnixMilli()
	err := dimagentEdit("me", c, false)
	dimagentMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	renewed, err := dimagentFresh(ctx, "me")
	if err != nil {
		t.Fatal(err)
	}
	unblock.Do(func() { close(release) })
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	l, _ = dimagentLookup("me")
	kept, _ := dimagentSaved(l)
	if kept.Access != renewed.Access || kept.Refresh != renewed.Refresh || kept.ExpiresAt != renewed.ExpiresAt {
		t.Fatalf("model fetch overwrote rotated credentials: %+v", kept)
	}
	listed, err := dimagent.ParseModels(kept.Models)
	if err != nil || len(listed) != 1 || listed[0].ID != "gpt-4o" || l.Renewed.IsZero() {
		t.Fatal("model listing or refresh metadata was lost")
	}
}

// Two requests that both find one account's token spent must spend its
// refresh token once: a second ask with a token the upstream already turned
// over is refused as a replay, which would end the account.
func TestDimAgentConcurrentRefresh(t *testing.T) {
	signIn(t)
	var asks atomic.Int32
	dimagentTokenRound(t, "acc-new", "ref-new", &asks)
	dimagentAdd(t, "me", "acc", "ref", time.Now().Add(time.Minute))

	var wg sync.WaitGroup
	start := make(chan struct{})
	got := make(chan dimagentCreds, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			c, err := dimagentFresh(context.Background(), "me")
			if err != nil {
				errs <- err
				return
			}
			got <- c
			errs <- nil
		}()
	}
	close(start)
	wg.Wait()
	close(got)
	close(errs)
	if n := asks.Load(); n != 1 {
		t.Fatalf("the refresh token was spent %d times", n)
	}
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for c := range got {
		if c.Access != "acc-new" || c.Refresh != "ref-new" {
			t.Fatalf("a caller was handed %q / %q", c.Access, c.Refresh)
		}
	}
	access, refresh, lapses := dimagentKept(t, "me")
	if access != "acc-new" || refresh != "ref-new" || lapses != "" {
		t.Fatalf("the rotated pair not kept: %q %q lapsed %q", access, refresh, lapses)
	}
}

// Once the upstream has turned the pair over, its reply has to be kept even
// when the request that needed the token is gone: dropping it would leave the
// spent one saved and sign the account out for good.
func TestDimAgentRefreshOutlivesAbortedRequest(t *testing.T) {
	signIn(t)
	release := make(chan struct{})
	var once sync.Once
	asked := make(chan struct{})
	dimagentSite(t, func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(asked) })
		<-release
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "acc-new",
			"refresh_token": "ref-new", "expires_in": 7 * 24 * 3600})
	})
	var free sync.Once
	unblock := func() { free.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	dimagentAdd(t, "me", "acc", "ref", time.Now().Add(time.Minute))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := dimagentFresh(ctx, "me"); done <- err }()
	<-asked
	cancel() // the agent gave up on its request while the renewal was in flight
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if access, refresh, _ := dimagentKept(t, "me"); access != "acc-new" || refresh != "ref-new" {
		t.Fatalf("the rotated pair went with the request: %q %q", access, refresh)
	}
}

// Only the upstream saying this sign-in is gone ends an account. A refusal
// that carries its status ends it and says so on the account; a hiccup of any
// other kind leaves it alone, because the tokens it has may still work.
func TestDimAgentRefusedRefreshLapses(t *testing.T) {
	for _, c := range []struct {
		status int
		lapse  bool
	}{{http.StatusUnauthorized, true}, {http.StatusForbidden, true},
		{http.StatusInternalServerError, false}, {http.StatusGatewayTimeout, false}} {
		user := fmt.Sprintf("at%d", c.status)
		t.Run(user, func(t *testing.T) {
			signIn(t)
			dimagentRefused(t, c.status)
			dimagentAdd(t, user, "acc", "ref", time.Now().Add(time.Minute))
			got, err := dimagentFresh(context.Background(), user)
			if c.lapse && err == nil {
				t.Fatal("the upstream refused and the account answered")
			}
			if !c.lapse && (err != nil || got.Access != "acc") {
				t.Fatalf("a hiccup failed a token that still runs: %v", err)
			}
			_, _, lapses := dimagentKept(t, user)
			if (lapses != "") != c.lapse {
				t.Errorf("HTTP %d: lapsed %q, want marked %v", c.status, lapses, c.lapse)
			}
			if lapses != "" && !strings.Contains(lapses, "sign in again") {
				t.Errorf("the lapse doesn't say what to do: %q", lapses)
			}
		})
	}
}

// A renewal runs on the account's own clock, not on the lock the other
// subscriptions are read through: one account renewing can't hold up the
// page's whole list, or a switch asked of another one.
func TestDimAgentRefreshDoesNotBlockLogins(t *testing.T) {
	signIn(t)
	release := make(chan struct{})
	var once, free sync.Once
	asked := make(chan struct{})
	dimagentSite(t, func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(asked) })
		<-release
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "acc-new",
			"refresh_token": "ref-new", "expires_in": 7 * 24 * 3600})
	})
	unblock := func() { free.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	dimagentAdd(t, "me", "acc", "ref", time.Now().Add(time.Minute))
	dimagentAdd(t, "other", "a2", "r2", time.Now().Add(3*24*time.Hour))

	done := make(chan error, 1)
	go func() { _, err := dimagentFresh(context.Background(), "me"); done <- err }()
	<-asked
	begin := time.Now()
	_ = Logins("dimagent")
	_ = Logins("qoder")
	if err := SetLoginOn("dimagent", "other", false); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(begin); d > 500*time.Millisecond {
		t.Fatalf("the list waited %v behind a DimAgent renewal", d)
	}
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// the renewal wrote back into the file as it stood then, keeping the
	// change asked of it while the round was in flight
	for _, l := range Logins("dimagent") {
		if l.User == "other" && l.On {
			t.Fatal("the renewal undid a change made while it ran")
		}
	}
	if access, _, _ := dimagentKept(t, "me"); access != "acc-new" {
		t.Fatalf("the renewed pair wasn't saved: %q", access)
	}
}
