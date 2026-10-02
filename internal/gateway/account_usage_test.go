package gateway

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// Each call's record names the subscription account that answered it, as
// the Routing trace names it (#557): the one X-Magpie-Account pinned, on
// magpie's endpoint and on Codex's own, and never a token of the account's.
func TestUsageNamesPinnedAccount(t *testing.T) {
	twoAccounts(t)
	s := New()
	for path, model := range pinPaths {
		for _, who := range []string{"spare@example.com", "me@example.com"} {
			if code, body := pinnedPost(t, s, path, model, who); code != 200 {
				t.Fatalf("%s %s: %d %s", path, who, code, body)
			}
			r := lastUsage(t)
			if r.ProviderAccount != who || r.Account() != who || r.Provider != "codex" {
				t.Fatalf("%s pinned to %s: recorded %q (provider %q)", path, who, r.ProviderAccount, r.Provider)
			}
			if tr := s.trace.routes[len(s.trace.routes)-1]; len(tr.Order) != 1 || tr.Order[0].Who != r.ProviderAccount {
				t.Fatalf("%s: the trace says %+v, the record %q", path, tr.Order, r.ProviderAccount)
			}
		}
	}
	b, err := os.ReadFile(usage.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"providerAccount":"spare@example.com"`) {
		t.Fatalf("no providerAccount in the ledger: %s", b)
	}
	for _, secret := range []string{"chatgpt-token", "r-acct", "access_token", "h.ey"} {
		if strings.Contains(string(b), secret) {
			t.Fatalf("%q in the ledger: %s", secret, b)
		}
	}
}

// After a failover the account that took over is the one recorded, the
// refused try kept as the first account's.
func TestUsageNamesAccountThatTookOver(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	if err := provider.SetRouting("codex", provider.Ordered); err != nil {
		t.Fatal(err)
	}
	var tried []string
	chatgptRefusing(t, &tried, refusing400)
	s := New()
	if code, body := codexAskOn(s, `{"model":"gpt-5.5","stream":true,"input":"ping"}`); code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	recs := usage.Load(time.Time{})
	if len(recs) != 2 {
		t.Fatalf("want the refusal and the answer: %+v", recs)
	}
	last := recs[len(recs)-1]
	if last.ProviderAccount != "spare@example.com" || last.Status != 200 || last.Input != 7 {
		t.Fatalf("answer recorded as %q: %+v", last.ProviderAccount, last)
	}
	for _, r := range recs[:len(recs)-1] {
		if r.ProviderAccount != "me@example.com" || r.Status != 400 {
			t.Fatalf("refused try recorded as %q: %+v", r.ProviderAccount, r)
		}
	}
}
