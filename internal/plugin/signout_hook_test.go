package plugin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// An account signed out while one of its hooks runs stays signed out: the
// hook's auth.set (a models list spending a rotating refresh token) lands
// after the sign-out and must not sign it in again (#5126: a failed move's
// account came back in the plugin this way). One not signed out keeps the
// token the hook saved.
func TestSignOutOutlastsRunningHook(t *testing.T) {
	sandbox(t)
	hold := filepath.Join(t.TempDir(), "hold")
	t.Setenv("FAKE_MODELS_HOLD", hold)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("testdata/fake/index.js")
	if _, err := Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(Settle)
	signIn := func() string {
		t.Helper()
		k, err := Import(ctx, "fakeco", map[string]any{"type": "oauth", "refresh": "rot-x", "access": "a", "expires": 9e15, "accountId": "x@fake"})
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	// the hook comes, and waits
	arrived := func() {
		t.Helper()
		for {
			if _, err := os.Stat(hold + ".in"); err == nil {
				return
			}
			select {
			case <-ctx.Done():
				t.Fatal("the models hook never came")
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	checked := func(key string) chan error {
		done := make(chan error, 1)
		go func() {
			_, err := Check(ctx, "fakeco", key)
			done <- err
		}()
		return done
	}

	// signed out while the hook holds the token it is about to save
	key := signIn()
	done := checked(key)
	arrived()
	if err := SignOut(ctx, "fakeco", ""); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hold, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	<-done
	if a := Auths("fakeco"); len(a) != 0 {
		t.Fatalf("the hook signed the account in again: %v", a)
	}

	// not signed out: the token it spent is replaced by one it saved
	key = signIn()
	if err := <-checked(key); err != nil {
		t.Fatal(err)
	}
	// (a listing the sign-in set off may spend it again too)
	if a := Auths("fakeco"); !strings.HasPrefix(fmt.Sprint(a[key]["refresh"]), "rot-x+") {
		t.Fatalf("the hook's renewed token wasn't kept: %v", a)
	}
}
