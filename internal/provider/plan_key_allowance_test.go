package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// planKeyServer answers Kimi Code's /usages as #1016's key had them (5
// hours 49 used, 51 left; the week 45 used), and Zhipu's quota for a
// pay-as-you-go GLM key: no plan, no windows. It counts the asks of each.
func planKeyServer(t *testing.T, down *atomic.Bool) (kimi, glm *atomic.Int32) {
	t.Helper()
	kimi, glm = &atomic.Int32{}, &atomic.Int32{}
	five := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339Nano)
	week := time.Now().Add(26 * time.Hour).UTC().Format(time.RFC3339Nano)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("X-Host") + r.URL.Path {
		case "api.kimi.com/coding/v1/usages":
			kimi.Add(1)
			if down.Load() || r.Header.Get("Authorization") != "Bearer sk-kimi-a" {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.Write([]byte(`{"usage":{"limit":"100","used":"45","resetTime":"` + week + `"},
				"limits":[{"window":{"duration":300,"timeUnit":"TIME_UNIT_MINUTE"},"detail":{"limit":"100","remaining":"51","resetTime":"` + five + `"}}]}`))
		case "open.bigmodel.cn/api/monitor/usage/quota/limit":
			if r.URL.RawQuery == "" { // not the team's ask behind it (type=2)
				glm.Add(1)
			}
			w.Write([]byte(`{"success":true,"data":{"limits":[]}}`))
		default:
			// a team's quota asked of a key with no plan of its own
			if !strings.Contains(r.URL.Path, "quota") {
				t.Errorf("asked %s%s", r.Header.Get("X-Host"), r.URL.Path)
			}
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	old := http.DefaultClient.Transport
	http.DefaultClient.Transport = rewrite{srv}
	t.Cleanup(func() { http.DefaultClient.Transport = old })
	return kimi, glm
}

var kimiKey = Provider{ID: "kimi-code-cn", Name: "Kimi Code (China)", Preset: "kimi-code-cn", Key: "sk-kimi-a",
	Chat: "https://api.kimi.com/coding/v1", Anthropic: "https://api.kimi.com/coding"}

// #1016: a plan bought with a key (Kimi Code's) tells routing its
// windows as a sub2api key's limits do — read behind the request, never
// waited for — so the Routing page can show what it has left, as the
// Usage page does; a key at another vendor is never asked.
func TestPlanKeyAllowance(t *testing.T) {
	keyLimitsHome(t)
	var down atomic.Bool
	kimi, _ := planKeyServer(t, &down)
	if err := Save(kimiKey); err != nil {
		t.Fatal(err)
	}
	if a, ok := KeyAllowance(kimiKey); ok || a != nil {
		t.Fatalf("known before it was read: %+v", a)
	}
	a := waitAllowance(t, kimiKey, func(Allowance) bool { return true })
	used, renews := a.For("kimi-for-coding", time.Now())
	if used != 49 || len(renews) != 2 || !renews[0].After(renews[1]) {
		t.Fatalf("used %v, renews %v of %+v", used, renews, a)
	}
	if id := KeyAllowanceID(kimiKey); id == "" {
		t.Fatal("a plan key has no id for its windows renewed")
	}
	if n := kimi.Load(); n != 1 {
		t.Fatalf("asked %d times", n)
	}
	// a key at a vendor that sells no plan to a key isn't asked, nor known
	for _, p := range []Provider{
		{ID: "deepseek", Key: "sk-ds", Chat: "https://api.deepseek.com/v1"},
		{ID: "kimi-api", Key: "sk-moon", Chat: "https://api.moonshot.cn/v1"},
	} {
		if _, ok := KeyAllowance(p); ok || KeyAllowanceID(p) != "" {
			t.Errorf("%s reads key windows", p.ID)
		}
	}
}

// The Usage page's read of a plan's card is what routing goes by at
// once, and the card kept on disk stands in for it after a restart while
// the vendor fails.
func TestPlanKeyAllowanceFromItsCard(t *testing.T) {
	keyLimitsHome(t)
	forgetPlanQuotas()
	t.Cleanup(forgetPlanQuotas)
	var down atomic.Bool
	kimi, _ := planKeyServer(t, &down)
	if err := Save(kimiKey); err != nil {
		t.Fatal(err)
	}
	qs := PlanQuotas(context.Background())
	if len(qs) != 1 || len(qs[0].Windows) != 2 {
		t.Fatalf("cards: %+v", qs)
	}
	a, ok := KeyAllowance(kimiKey)
	if used, _ := a.For("", time.Now()); !ok || used != 49 {
		t.Fatalf("after the card's read: %v %+v", ok, a)
	}
	if n := kimi.Load(); n != 1 {
		t.Fatalf("asked %d times: routing asked again what the card had just read", n)
	}

	// restarted, the vendor down: the card on disk stands in
	down.Store(true)
	keyAllowances.Lock()
	keyAllowances.m = nil
	keyAllowances.Unlock()
	lastQuotas.Lock()
	lastQuotas.m, lastQuotas.loaded = nil, false
	lastQuotas.Unlock()
	a, ok = KeyAllowance(kimiKey)
	if used, _ := a.For("", time.Now()); !ok || used != 49 {
		t.Fatalf("from the card on disk: %v %+v", ok, a)
	}
	// and the failed read behind it leaves it known
	waitRead(t)
	if a, ok := KeyAllowance(kimiKey); !ok || len(a) != 2 {
		t.Fatalf("after a failed read: %v %+v", ok, a)
	}
}

// A pay-as-you-go key at a vendor that sells plans too (a GLM key at
// BigModel) has no windows: it is asked again only every half hour, not
// every minute it is used, in case a plan is bought.
func TestPayAsYouGoKeyAskedSeldom(t *testing.T) {
	keyLimitsHome(t)
	var down atomic.Bool
	_, glm := planKeyServer(t, &down)
	p := Provider{ID: "zhipu", Key: "glm-a", Chat: "https://open.bigmodel.cn/api/paas/v4"}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	KeyAllowance(p)
	waitRead(t)
	if n := glm.Load(); n != 1 {
		t.Fatalf("asked %d times", n)
	}
	back := func(d time.Duration) {
		keyAllowances.Lock()
		for _, e := range keyAllowances.m {
			e.at = time.Now().Add(-d)
		}
		keyAllowances.Unlock()
	}
	back(2 * time.Minute)
	if _, ok := KeyAllowance(p); ok {
		t.Fatal("a key with no windows is known")
	}
	waitRead(t)
	if n := glm.Load(); n != 1 {
		t.Fatalf("asked again %d times two minutes on", n-1)
	}
	back(31 * time.Minute)
	KeyAllowance(p)
	waitRead(t)
	if n := glm.Load(); n != 2 {
		t.Fatalf("asked %d times half an hour on", n)
	}
}

// waitRead waits for the reads behind KeyAllowance to end.
func waitRead(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		busy := false
		keyAllowances.Lock()
		for _, e := range keyAllowances.m {
			busy = busy || e.loading
		}
		keyAllowances.Unlock()
		if !busy {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("a read never ended")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
