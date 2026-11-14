package tui

import (
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// usageShown is the cards the Usage page leaves as the app's Overview does
// (onkashiko on X: a card hidden in the app's window still showed
// elsewhere), provider.ShownQuotas. Only what is shown.
func usageShown(qs []provider.SubscriptionQuota, s settings.Settings) []provider.SubscriptionQuota {
	return provider.ShownQuotas(qs, s)
}
