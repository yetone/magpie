package tui

import (
	"cmp"
	"slices"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// usageShown is the cards the Usage page leaves as the app's Overview does
// (onkashiko on X: a card hidden in the app's window still showed
// elsewhere): a card hidden there (settings.UsageCardsHidden, by its tray
// card id and kind, cardHideKey in the app) is left out, and so are the
// rows hidden there (settings.UsageWindowsHidden, by provider and the row's
// group), unless that would leave a card with none. Only what is shown.
func usageShown(qs []provider.SubscriptionQuota, s settings.Settings) []provider.SubscriptionQuota {
	if qs == nil || len(s.UsageCardsHidden)+len(s.UsageWindowsHidden) == 0 {
		return qs
	}
	out := make([]provider.SubscriptionQuota, 0, len(qs))
	for _, q := range qs {
		if slices.Contains(s.UsageCardsHidden, cardHideKey(q)) {
			continue
		}
		if q.Error == "" && len(q.Windows) > 0 && len(s.UsageWindowsHidden) > 0 {
			ws := slices.DeleteFunc(slices.Clone(q.Windows), func(w provider.QuotaWindow) bool {
				return slices.Contains(s.UsageWindowsHidden, q.Provider+"|"+cmp.Or(w.Pool, w.Family, w.Name))
			})
			if len(ws) > 0 {
				q.Windows = ws
			}
		}
		out = append(out, q)
	}
	return out
}

// cardHideKey is how the app keeps a hidden card: its tray card id, then
// "|balance" for a balance alone, else "|plan".
func cardHideKey(q provider.SubscriptionQuota) string {
	id := q.Provider
	if q.User != "" {
		id += "|" + q.User
	}
	if q.Balance != "" && len(q.Windows) == 0 && q.Plan == "" {
		return id + "|balance"
	}
	return id + "|plan"
}
