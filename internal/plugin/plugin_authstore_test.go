package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func authFile(t *testing.T) map[string]map[string]any {
	t.Helper()
	var all map[string]map[string]any
	b, _ := os.ReadFile(AuthPath())
	json.Unmarshal(b, &all)
	return all
}

// A host stopped (magpie quitting, a plugin updated) while it renews a
// sign-in lets the renewal end and its new token be saved: the vendor
// has spent the old refresh token, so a host killed 2 seconds in left
// the account signed out.
func TestStopLetsARenewalEnd(t *testing.T) {
	sandbox(t)
	t.Setenv("FAKE_BASE", "http://127.0.0.1:1/v1")
	logf := filepath.Join(t.TempDir(), "renewals")
	t.Setenv("RENEW_LOG", logf)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("testdata/renew/index.js")
	if _, err := Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	soon := time.Now().Add(time.Minute).UnixMilli()
	key, err := Import(ctx, "renewco", map[string]any{"type": "oauth", "refresh": "r-slow", "access": "old", "expires": soon, "accountId": "slow@renew"})
	if err != nil {
		t.Fatal(err)
	}

	// a call starts the renewal and is given up on once it has: the host
	// answers nothing, and only the renewal goes on. Given up on after a
	// set time instead, a call not yet sent then never started it (#2743).
	callCtx, giveUp := context.WithCancel(ctx)
	called := make(chan error, 1)
	go func() {
		called <- Call(callCtx, "load", map[string]any{"provider": "renewco", "account": key}, nil)
	}()
	for {
		if b, _ := os.ReadFile(logf); strings.Contains(string(b), "r-slow") {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("the renewal never started")
		}
		time.Sleep(20 * time.Millisecond)
	}
	giveUp()
	select {
	case <-called:
	case <-ctx.Done():
		t.Fatal("the given-up call never returned")
	}
	hostMu.Lock()
	h := current
	hostMu.Unlock()
	if h == nil {
		t.Fatal("no host runs")
	}
	// the plugin has started renewing; magpie hears it from the host's
	// output, which a busy machine reads late
	for h.renewing.Load() == 0 {
		if ctx.Err() != nil {
			t.Fatal("magpie never heard the host is renewing")
		}
		time.Sleep(20 * time.Millisecond)
	}
	start := time.Now()
	Restart()
	// a host still holding the given-up call's slot (its abort not yet
	// written) is stopped in the background once it lets go: the test
	// waits for it to end, not for Restart to return
	select {
	case <-h.dead:
	case <-ctx.Done():
		t.Fatal("the stopped host never ended")
	}
	if a := authFile(t)[key]; a["refresh"] != "r-slow+" || a["access"] != "a-1" {
		t.Fatalf("after stopping %v in, kept %v, not the renewed token", time.Since(start).Round(time.Millisecond), a)
	}
	if d := time.Since(start); d > stopRenewing+time.Second {
		t.Fatalf("stopping took %v", d)
	}
}

// A host with no renewal under way stops as fast as before.
func TestStopWithoutRenewalIsQuick(t *testing.T) {
	sandbox(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("testdata/fake/index.js")
	if _, err := Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := get(ctx); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	Restart()
	if d := time.Since(start); d > stopWait+time.Second {
		t.Fatalf("stopping took %v", d)
	}
}

// Two hosts run at once while one is restarted, both saving sign-ins to
// plugin-auth.json: each change is made to the file as it is then, so
// neither writes the other's accounts away.
func TestTwoHostsKeepEachOthersSignIns(t *testing.T) {
	sandbox(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("testdata/renew/index.js")
	if _, err := Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	h1, err := get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	h2, _, err := startOn(ctx, os.Getenv("MAGPIE_BUN"))
	if err != nil {
		t.Fatal(err)
	}
	defer h2.stop()

	const n = 40
	var wg sync.WaitGroup
	errs := make(chan error, 2*n)
	for _, side := range []struct {
		h    *host
		name string
	}{{h1, "a"}, {h2, "b"}} {
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				k := fmt.Sprintf("renewco#%s%d", side.name, i)
				errs <- side.h.call(ctx, "import", map[string]any{"provider": "renewco", "key": k,
					"auth": map[string]any{"type": "api", "key": "key-" + k}}, nil)
			}()
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	all := authFile(t)
	var lost []string
	for _, side := range []string{"a", "b"} {
		for i := range n {
			k := fmt.Sprintf("renewco#%s%d", side, i)
			if all[k]["key"] != "key-"+k {
				lost = append(lost, k)
			}
		}
	}
	if len(lost) > 0 {
		t.Fatalf("%d of %d sign-ins lost: %v", len(lost), 2*n, lost)
	}
	if _, err := os.Stat(AuthPath() + ".lock"); !os.IsNotExist(err) {
		t.Fatalf("the lock is left behind: %v", err)
	}
}
