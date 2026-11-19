package plugin

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/testenv"
)

// raceVendor is a vendor that spends a refresh token once (plugins
// #331): the first exchange of r1 gives r2, and r1 again, or a token it
// never gave, is invalid_grant. hold, when set, keeps the first exchange
// from answering until it is closed, and arrived says it came.
type raceVendor struct {
	mu        sync.Mutex
	valid     map[string]string // refresh token → the one it gives
	exchanges []string
	hold      chan struct{}
	arrived   chan struct{}
	auths     []string // each request's Authorization
	store     string   // plugin-auth.json
}

func newRaceVendor(t *testing.T) *raceVendor {
	t.Helper()
	v := &raceVendor{valid: map[string]string{"r1": "r2"}, arrived: make(chan struct{}, 8), store: AuthPath()}
	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		v.mu.Lock()
		v.exchanges = append(v.exchanges, string(b))
		next, ok := v.valid[string(b)]
		delete(v.valid, string(b)) // spent, whatever comes of the answer
		first := len(v.exchanges) == 1
		v.mu.Unlock()
		v.arrived <- struct{}{}
		if first && v.hold != nil {
			<-v.hold
		}
		if key, ok := strings.CutPrefix(string(b), "r-raced "); ok {
			// a magpie from before the lock spent it first and saved
			// what it got, then this one is turned away
			var all map[string]map[string]any
			raw, _ := os.ReadFile(v.store)
			json.Unmarshal(raw, &all)
			all[key]["access"], all[key]["refresh"] = "a-elsewhere", "r-elsewhere"
			all[key]["expires"] = time.Now().Add(time.Hour).UnixMilli()
			raw, _ = json.Marshal(all)
			os.WriteFile(v.store, raw, 0o600)
		}
		if !ok {
			w.WriteHeader(400)
			io.WriteString(w, `{"error":"invalid_grant"}`)
			return
		}
		fmt.Fprintf(w, `{"access":"a-%s","refresh":%q}`, next, next)
	}))
	t.Cleanup(tokens.Close)
	api := testenv.FakeVendor(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v.mu.Lock()
		v.auths = append(v.auths, r.Header.Get("Authorization"))
		v.mu.Unlock()
		io.WriteString(w, "ok")
	}))
	t.Cleanup(api.Close)
	t.Setenv("RACE_TOKEN_URL", tokens.URL)
	return v
}

func (v *raceVendor) seen() (exchanges, auths []string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]string(nil), v.exchanges...), append([]string(nil), v.auths...)
}

// raceAccount adds the race plugin and an account due for renewal.
func raceAccount(t *testing.T, ctx context.Context, refresh string) string {
	t.Helper()
	abs, _ := filepath.Abs("testdata/renewrace/index.js")
	if _, err := Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	soon := time.Now().Add(time.Minute).UnixMilli() // inside the 5 minutes
	k, err := Import(ctx, "raceco", map[string]any{"type": "oauth", "refresh": refresh, "access": "a-old", "expires": soon, "accountId": "me@race"})
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func raceSend(ctx context.Context, account string) error {
	res, err := Fetch(ctx, FetchRequest{Provider: "raceco", Account: account, Model: "race-1", NPM: "@ai-sdk/openai-compatible",
		URL: os.Getenv("FAKE_BASE") + "/chat/completions", Method: "POST", Body: []byte(`{}`)})
	if err != nil {
		return err
	}
	io.ReadAll(res.Body)
	return res.Body.Close()
}

func raceStored(key string) map[string]any {
	var all map[string]map[string]any
	b, _ := os.ReadFile(AuthPath())
	json.Unmarshal(b, &all)
	return all[key]
}

// changeStored sets the refresh token of the account at key in the store.
func changeStored(key, refresh string) error {
	var all map[string]map[string]any
	raw, err := os.ReadFile(AuthPath())
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &all); err != nil {
		return err
	}
	all[key]["refresh"] = refresh
	raw, _ = json.Marshal(all)
	return os.WriteFile(AuthPath(), raw, 0o600)
}

// TestRenewOtherHost is the second magpie of TestRenewOnceAcrossHosts:
// its host starts, it says "ready", and on "go" it sends a request on the
// account, says "renewing" once its host is renewing the sign-in, and
// "said …" for whatever its host says of the sign-in.
func TestRenewOtherHost(t *testing.T) {
	account := os.Getenv("MAGPIE_RENEW_OTHER")
	if account == "" {
		t.Skip("run by TestRenewOnceAcrossHosts")
	}
	t.Cleanup(Settle)
	var mu sync.Mutex
	out := func(s string) { mu.Lock(); os.Stdout.WriteString(s + "\n"); mu.Unlock() }
	OnSignIn(func(_, _, s string) { out("said " + s) })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	h, err := get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	out("ready")
	if line, _ := bufio.NewReader(os.Stdin).ReadString('\n'); line != "go\n" {
		t.Fatalf("told %q", line)
	}
	sent := make(chan error, 1)
	go func() { sent <- raceSend(ctx, account) }()
	// renewing, or done already: a host that didn't wait for the other's
	// renewal, which the first magpie's checks then find
	for h.renewing.Load() == 0 && len(sent) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	out("renewing")
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
	Told()
	out("sent")
}

// plugins #331: each magpie process runs a host of its own, and two hosts
// renewing one sign-in at once each spent its refresh token, so a vendor
// that spends one once turned the second away and the account was marked
// signed out under the token the first had saved. Two hosts, two
// processes, one store: the first exchange is held until the second host
// is renewing too. The token is exchanged once, neither says the sign-in
// expired, and both send the new token.
func TestRenewOnceAcrossHosts(t *testing.T) {
	sandbox(t)
	v := newRaceVendor(t)
	v.hold = make(chan struct{})
	release := sync.OnceFunc(func() { close(v.hold) })
	t.Cleanup(release)
	var mu sync.Mutex
	var said []string
	OnSignIn(func(_, _, s string) { mu.Lock(); said = append(said, s); mu.Unlock() })
	t.Cleanup(func() { OnSignIn(nil) })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	key := raceAccount(t, ctx, "r1")

	other := exec.Command(os.Args[0], "-test.run=^TestRenewOtherHost$", "-test.count=1", "-test.v")
	other.Env = append(os.Environ(), "MAGPIE_RENEW_OTHER="+key)
	stdin, _ := other.StdinPipe()
	stdout, _ := other.StdoutPipe()
	other.Stderr = os.Stderr
	if err := other.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { other.Process.Kill(); other.Wait() })
	lines := make(chan string, 64)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	var otherSaid []string
	expect := func(want string) {
		t.Helper()
		for l := range lines {
			if l == want {
				return
			}
			if s, ok := strings.CutPrefix(l, "said "); ok {
				otherSaid = append(otherSaid, s)
			}
		}
		t.Fatalf("the other magpie ended before %q", want)
	}
	expect("ready")

	sent := make(chan error, 1)
	go func() { sent <- raceSend(ctx, key) }()
	<-v.arrived // this host's exchange, held
	io.WriteString(stdin, "go\n")
	expect("renewing")
	release()
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
	expect("sent")
	if err := other.Wait(); err != nil {
		t.Fatalf("the other magpie: %v", err)
	}
	Told()

	exchanges, auths := v.seen()
	if len(exchanges) != 1 {
		t.Errorf("exchanges = %v, want r1 once", exchanges)
	}
	mu.Lock()
	all := append(append([]string(nil), said...), otherSaid...)
	mu.Unlock()
	for _, s := range all {
		if s == "expired" {
			t.Errorf("a host said the sign-in expired: this one %v, the other %v", said, otherSaid)
		}
	}
	if len(auths) != 2 || auths[0] != "Bearer a-r2" || auths[1] != "Bearer a-r2" {
		t.Errorf("requests went with %v, want the new token twice", auths)
	}
	if a := raceStored(key); a["refresh"] != "r2" || a["access"] != "a-r2" {
		t.Errorf("kept %v", a)
	}
}

// A renewal lock left by a magpie that died is taken over at once: the
// renewal is made inside the wait a request gives it, not after the
// lock's minute. A token the vendor turns away still says the sign-in
// expired, and leaves it as it was; one turned away because another
// magpie spent it and saved the new one meanwhile doesn't.
func TestRenewLockOfDeadHostAndRefusal(t *testing.T) {
	sandbox(t)
	v := newRaceVendor(t)
	said := make(chan string, 16)
	OnSignIn(func(_, _, s string) { said <- s })
	t.Cleanup(func() { OnSignIn(nil) })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	key := raceAccount(t, ctx, "r1")

	dead := exec.Command(os.Args[0], "-test.run=^$")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(key))
	lock := AuthPath() + ".renew-" + hex.EncodeToString(sum[:])[:16] + ".lock"
	held := fmt.Sprintf(`{"pid":%d,"at":%d}`, dead.Process.Pid, time.Now().UnixMilli())
	if err := os.WriteFile(lock, []byte(held), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := raceSend(ctx, key); err != nil {
		t.Fatal(err)
	}
	if s := waitSaid(t, said); s != "renewed" {
		t.Fatalf("said %q", s)
	}
	if exchanges, auths := v.seen(); len(exchanges) != 1 || len(auths) != 1 || auths[0] != "Bearer a-r2" {
		t.Fatalf("exchanges %v, requests %v: the dead magpie's lock was waited out", exchanges, auths)
	}
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Fatalf("the lock is left: %v", err)
	}

	refused, err := Import(ctx, "raceco", map[string]any{"type": "oauth", "refresh": "r-never", "access": "a-old", "expires": time.Now().Add(time.Minute).UnixMilli(), "accountId": "refused@race"})
	if err != nil {
		t.Fatal(err)
	}
	if err := raceSend(ctx, refused); err != nil {
		t.Fatal(err)
	}
	if s := waitSaid(t, said); s != "expired" {
		t.Fatalf("a refused token said %q, want expired", s)
	}
	if a := raceStored(refused); a["refresh"] != "r-never" || a["access"] != "a-old" {
		t.Fatalf("a refused account was changed: %v", a)
	}

	// turned away because another magpie, one that didn't take the lock,
	// spent the token and saved what it got: not signed out, and its
	// token is the one sent
	raced, err := Import(ctx, "raceco", map[string]any{"type": "oauth", "refresh": "r-raced", "access": "a-old", "expires": time.Now().Add(time.Minute).UnixMilli(), "accountId": "raced@race"})
	if err != nil {
		t.Fatal(err)
	}
	if err := changeStored(raced, "r-raced "+raced); err != nil {
		t.Fatal(err)
	}
	if err := raceSend(ctx, raced); err != nil {
		t.Fatal(err)
	}
	if err := raceSend(ctx, raced); err != nil {
		t.Fatal(err)
	}
	Told()
	select {
	case s := <-said:
		t.Fatalf("a token another magpie renewed meanwhile said %q", s)
	default:
	}
	if _, auths := v.seen(); auths[len(auths)-1] != "Bearer a-elsewhere" {
		t.Fatalf("requests went with %v, want the other magpie's token last", auths)
	}
}

// Two Kubernetes pods on one volume (a rolling update's old pod and new
// one) each have pids of their own namespace, so a renewal lock the other
// pod's host holds names a pid that is no process here. It was taken over
// as a dead host's, and both pods spent one refresh token. A lock from
// another host is waited for (up to its age) instead: no exchange is made
// while it is held, and once the other pod saved its renewal and let go,
// this one sends that token without spending its own.
func TestRenewLockOfAnotherPodIsWaitedFor(t *testing.T) {
	sandbox(t)
	v := newRaceVendor(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	key := raceAccount(t, ctx, "r1")

	dead := exec.Command(os.Args[0], "-test.run=^$")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(key))
	lock := AuthPath() + ".renew-" + hex.EncodeToString(sum[:])[:16] + ".lock"
	held := fmt.Sprintf(`{"pid":%d,"host":"magpie-7d9f-old pid:[4026532001]","at":%d}`, dead.Process.Pid, time.Now().UnixMilli())
	if err := os.WriteFile(lock, []byte(held), 0o600); err != nil {
		t.Fatal(err)
	}
	sent := make(chan error, 1)
	go func() { sent <- raceSend(ctx, key) }()
	time.Sleep(1500 * time.Millisecond)
	if exchanges, _ := v.seen(); len(exchanges) != 0 {
		t.Fatalf("exchanged %v while another pod's host held the renewal lock", exchanges)
	}
	// the other pod's renewal: saved, then the lock let go
	var all map[string]map[string]any
	raw, err := os.ReadFile(AuthPath())
	if err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(raw, &all)
	all[key]["access"], all[key]["refresh"] = "a-other-pod", "r-other-pod"
	all[key]["expires"] = time.Now().Add(time.Hour).UnixMilli()
	raw, _ = json.Marshal(all)
	if err := os.WriteFile(AuthPath(), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	os.Remove(lock)
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
	Told()
	exchanges, auths := v.seen()
	if len(exchanges) != 0 {
		t.Errorf("exchanged %v: the other pod's renewal was spent again", exchanges)
	}
	if len(auths) != 1 || auths[0] != "Bearer a-other-pod" {
		t.Errorf("requests went with %v, want the other pod's token", auths)
	}
}
