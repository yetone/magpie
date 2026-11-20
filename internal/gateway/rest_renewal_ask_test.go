package gateway

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// RookieRicardoR on X: a GLM key refused out of its allowance till the
// time Zhipu named, then given a reset (Zhipu's 重置卡) on the vendor's
// side, was still answered "limit reached" by magpie on every request
// till that time, the vendor never asked. A reading of the key's windows
// that finds the one it was out of full no more has the vendor asked once
// again; the rest itself stands (#2718), and refused again with the same
// time the key is held till it, however often its windows read less than
// full.
func TestRenewalAsksAVendorTimedRestOnce(t *testing.T) {
	fresh(t)
	provider.ForgetBalances()
	t.Cleanup(provider.ForgetBalances)
	old := keyAllowance
	keyAllowance = provider.KeyAllowance // the key's windows as read, here
	t.Cleanup(func() { keyAllowance = old })
	reset := time.Now().Add(3 * time.Hour).Truncate(time.Second)
	var mu sync.Mutex
	used := 800
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		u := used
		mu.Unlock()
		fmt.Fprintf(w, `{"isValid":true,"mode":"quota_limited","status":"active","rate_limits":[{"window":"5h","limit":800,"used":%d,"remaining":%d,"reset_at":%q}]}`, u, 800-u, reset.Format(time.RFC3339))
	}))
	defer relay.Close()
	if err := provider.Save(provider.Provider{ID: "kr", Name: "KR", Key: "sk-r", Models: []string{"glm-5.2"},
		Chat: relay.URL + "/v1", BalanceURL: relay.URL + "/v1/usage"}); err != nil {
		t.Fatal(err)
	}
	saved, err := provider.Find("kr")
	if err != nil {
		t.Fatal(err)
	}
	c := perKey(*saved, "glm-5.2", provider.Chat)[0]
	t.Cleanup(func() { clearRest(c.restKey()) })
	provider.KeyBalances(context.Background()) // its five hours read: used up
	s := &Server{}
	refusal := []byte(fmt.Sprintf(`{"error":{"message":"Usage limit reached. Your usage will reset at %s UTC+8, upgrade for more","type":"rate_limit_exceeded"}}`,
		reset.In(zhipuZone).Format("2006-01-02 15:04:05")))
	refuse := func() {
		t.Helper()
		if r := s.restAfter(c, 429, http.Header{}, refusal); r.Why != failQuota || r.By != "resets" {
			t.Fatalf("refused till %s: rests by %s (%s)", reset.Format(time.TimeOnly), r.By, r.Why)
		}
	}
	held := func() bool {
		_, _, ok := timedHeld([]candidate{c})
		return ok
	}
	read := func(u int) {
		mu.Lock()
		used = u
		mu.Unlock()
		provider.ForgetBalances()
		provider.KeyBalances(context.Background())
	}
	refuse()
	if !held() {
		t.Fatal("refused till a named time: not held")
	}
	read(800) // read again, still used up: nothing changes
	if !held() {
		t.Fatal("a reading that still finds the window full let the hold go")
	}
	read(0) // the reset bought on the vendor's side
	if held() {
		t.Fatal("the window read renewed, and the key is still held till the vendor's time: the vendor is never asked")
	}
	if _, ok := restOf(c.restKey()); !ok {
		t.Fatal("the renewal lifted the rest itself; only the hold is let go (#2718)")
	}
	refuse() // asked, and refused again with the same time
	if !held() {
		t.Fatal("refused again with the same time: not held")
	}
	read(800)
	read(0) // its windows read less than full again, the same time standing
	if !held() {
		t.Fatal("asked again for the same named time: a renewal asks once for it")
	}
}
