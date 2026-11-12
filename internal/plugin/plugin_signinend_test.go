package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/testenv"
)

// signInEnd adds testdata/signinend, whose sign-ins wait on $HOME/go as
// AStudio's waits on its desktop app's session file (plugins#658).
func signInEnd(t *testing.T) (context.Context, string) {
	t.Helper()
	sandbox(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	abs, _ := filepath.Abs("testdata/signinend/index.js")
	if _, err := Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	return ctx, os.Getenv("HOME")
}

// markedWithin is whether the plugin wrote $HOME/name within d.
func markedWithin(home, name string, d time.Duration) bool {
	for end := time.Now().Add(d); ; {
		if _, err := os.Stat(filepath.Join(home, name)); err == nil {
			return true
		}
		if time.Now().After(end) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// cancelledFinish starts the method's sign-in and gives its wait up once
// the plugin is waiting (it marks $HOME/waiting, or its vendor is asked),
// as the GUI's Cancel does (signInFlow.stop).
func cancelledFinish(t *testing.T, ctx context.Context, method int, waiting <-chan struct{}) {
	t.Helper()
	a, err := Authorize(ctx, "signinend", method, nil, NewAccount)
	if err != nil {
		t.Fatal(err)
	}
	wait, cancel := context.WithCancel(ctx)
	go func() {
		if waiting != nil {
			<-waiting
		} else {
			markedWithin(os.Getenv("HOME"), "waiting", 30*time.Second)
		}
		cancel()
	}()
	if _, err := Finish(wait, a.Session, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("Finish after cancel = %v, want context.Canceled", err)
	}
}

// #5016: a sign-in cancelled in magpie saved the account all the same
// when the plugin's wait came true after it. What the plugin answers once
// the sign-in is given up is dropped.
func TestCancelledSignInSavesNothing(t *testing.T) {
	ctx, home := signInEnd(t)
	cancelledFinish(t, ctx, 0, nil)
	if err := os.WriteFile(filepath.Join(home, "go"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if !markedWithin(home, "returned", 30*time.Second) {
		t.Fatal("the plugin's wait never came true")
	}
	// one more answer from the host, after the callback's result was dealt with
	if _, err := Providers(ctx); err != nil {
		t.Fatal(err)
	}
	if SignedIn("signinend") {
		b, _ := os.ReadFile(AuthPath())
		t.Fatalf("a cancelled sign-in was saved: %s", b)
	}
	// and the next sign-in is the user's
	a, err := Authorize(ctx, "signinend", 0, nil, NewAccount)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := Finish(ctx, a.Session, ""); err != nil || got != (Saved{"signinend", "signinend"}) {
		t.Fatalf("Finish = %+v, %v", got, err)
	}
	if b, _ := os.ReadFile(AuthPath()); !strings.Contains(string(b), `"tok-late"`) {
		t.Fatalf("auth file: %s", b)
	}
}

// #5016: the plugin's wait is told the sign-in is over (magpie's hook:
// callback(code, { signal })), so it stops instead of waiting out its own
// minutes.
func TestCancelledSignInEndsThePluginsWait(t *testing.T) {
	ctx, home := signInEnd(t)
	cancelledFinish(t, ctx, 1, nil)
	if !markedWithin(home, "aborted", 20*time.Second) {
		t.Fatal("the plugin's wait was never told the sign-in was cancelled")
	}
}

// #5016: a sign-in that polls its vendor stops polling once cancelled:
// the fetch it made for the sign-in fails.
func TestCancelledSignInEndsItsFetch(t *testing.T) {
	asked := make(chan struct{})
	gone := make(chan struct{})
	testenv.FakeVendor(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(asked)
		select {
		case <-r.Context().Done():
			close(gone)
		case <-time.After(time.Minute):
		}
	}))
	ctx, home := signInEnd(t)
	cancelledFinish(t, ctx, 7, asked)
	if !markedWithin(home, "fetch-aborted", 20*time.Second) {
		t.Fatal("the sign-in's fetch went on after it was cancelled")
	}
	select {
	case <-gone:
	case <-time.After(20 * time.Second):
		t.Fatal("the vendor's request was never closed")
	}
	if b, _ := os.ReadFile(filepath.Join(home, "fetch-aborted")); string(b) != "AbortError" {
		t.Errorf("the fetch failed with %q, want AbortError", b)
	}
}

// plugins#658: an oauth method's authorize that fails ({ type: "failed",
// error }) reached the user as "s.a.callback is not a function"; the
// plugin's reason is the error, for every method type.
func TestSignInFailsWithThePluginsReason(t *testing.T) {
	ctx, _ := signInEnd(t)
	for _, c := range []struct {
		method int
		want   string
	}{
		{2, "this AStudio account is banned"},
		{3, "Agnes Code signs in on Windows only"},
		{4, "signinend's sign-in gave nothing to finish it with"},
	} {
		a, err := Authorize(ctx, "signinend", c.method, nil, NewAccount)
		if err == nil {
			_, err = Finish(ctx, a.Session, "x")
		}
		if !errors.Is(err, ErrFailed) || !strings.HasSuffix(err.Error(), ": "+c.want) {
			t.Errorf("method %d: %v, want the sign-in failed: %s", c.method, err, c.want)
		}
	}
	if _, err := APIKey(ctx, "signinend", 5, nil, "k", NewAccount); !errors.Is(err, ErrFailed) || !strings.HasSuffix(err.Error(), ": that key is revoked") {
		t.Errorf("api method: %v", err)
	}
	a, err := Authorize(ctx, "signinend", 6, nil, NewAccount)
	if err != nil || a.Method != "code" {
		t.Fatalf("Authorize = %+v, %v", a, err)
	}
	if _, err := Finish(ctx, a.Session, "x"); !errors.Is(err, ErrFailed) || !strings.HasSuffix(err.Error(), ": that code expired") {
		t.Errorf("code callback: %v", err)
	}
	if SignedIn("signinend") {
		t.Error("a failed sign-in was saved")
	}
}

// plugins#998: an OAuth success with no refresh field said "signed in"
// and kept nothing, since a sign-in is kept as OAuth only when its result
// has one. One with an access is kept; one with nothing to keep fails.
func TestSignInWithNoRefreshIsKept(t *testing.T) {
	ctx, _ := signInEnd(t)
	a, err := Authorize(ctx, "signinend", 9, nil, NewAccount)
	if err == nil {
		_, err = Finish(ctx, a.Session, "")
	}
	if !errors.Is(err, ErrFailed) || !strings.HasSuffix(err.Error(), ": signinend's sign-in gave nothing to keep") {
		t.Errorf("a success with nothing to keep: %v", err)
	}
	if SignedIn("signinend") {
		t.Fatal("nothing was there to keep, and something was")
	}
	a, err = Authorize(ctx, "signinend", 8, nil, NewAccount)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Finish(ctx, a.Session, ""); err != nil {
		t.Fatalf("Finish = %v", err)
	}
	if !SignedIn("signinend") {
		t.Fatal("a sign-in with an access and no refresh was not kept")
	}
	for _, v := range authRaw() {
		var got struct {
			Type    string  `json:"type"`
			Access  string  `json:"access"`
			Refresh *string `json:"refresh"`
		}
		if json.Unmarshal(v, &got) == nil && got.Access == "acc-only" {
			if got.Type != "oauth" || got.Refresh == nil || *got.Refresh != "" {
				t.Errorf("kept as %s", v)
			}
			return
		}
	}
	t.Errorf("no account holds the access: %v", authRaw())
}
