package cli

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// magpie provider set billing=plan marks a provider on a plan (EV_John_Liu
// on X), show says so, billing=auto hands it back to magpie's reading, and
// any other value is refused and leaves the saved one alone.
func TestProviderSetBilling(t *testing.T) {
	groupsHome(t)
	if _, err := printed(t, func() error { return providerCmd([]string{"provider", "set", "a", "billing=plan"}) }); err != nil {
		t.Fatal(err)
	}
	if p := mustFind(t, "a"); p.Billing != provider.BillingPlan || p.BillingKind() != provider.BillingPlan {
		t.Fatalf("billing=plan: %q", p.Billing)
	}
	out, err := printed(t, func() error { return providerCmd([]string{"provider", "a"}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "billing") || !strings.Contains(out, "by your word") {
		t.Fatalf("show doesn't say how it bills:\n%s", out)
	}
	for _, v := range []string{"maybe", "mixed", "--help"} {
		err := providerCmd([]string{"provider", "set", "a", "billing=" + v})
		if err == nil || !strings.Contains(err.Error(), "billing=plan|payg|auto") {
			t.Errorf("billing=%q: %v", v, err)
		}
		if mustFind(t, "a").Billing != provider.BillingPlan {
			t.Errorf("a refused billing=%q changed it", v)
		}
	}
	if _, err := printed(t, func() error { return providerCmd([]string{"provider", "set", "a", "billing=payg"}) }); err != nil {
		t.Fatal(err)
	}
	if mustFind(t, "a").Billing != provider.BillingPAYG {
		t.Fatal("billing=payg not saved")
	}
	if _, err := printed(t, func() error { return providerCmd([]string{"provider", "set", "a", "billing=auto"}) }); err != nil {
		t.Fatal(err)
	}
	if mustFind(t, "a").Billing != "" {
		t.Fatal("billing=auto left the user's word")
	}
}
