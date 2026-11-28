package provider

import (
	"context"
	"strings"
	"testing"
)

// xuanwo on X: a request agy's own users never send puts the account at
// risk. magpie has no warm-up for an agy CLI account: Warm up now and All
// accounts refuse it before looking for the account, so nothing is sent.
func TestAgyHasNoWarmUp(t *testing.T) {
	claudeHome(t)
	ctx := context.Background()
	if err := WarmNow(ctx, agyCLIID, "agy@fake"); err == nil || !strings.Contains(err.Error(), "has no warm-up") {
		t.Errorf("WarmNow(agy-cli) = %v", err)
	}
	if _, err := WarmAll(ctx, agyCLIID); err == nil || !strings.Contains(err.Error(), "has no warm-up") {
		t.Errorf("WarmAll(agy-cli) = %v", err)
	}
}
