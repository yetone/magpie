package provider

import (
	"cmp"
	"slices"

	"github.com/yetone/magpie/internal/settings"
)

// What the app's Usage page hides from its Overview (onkashiko on X) is
// left out wherever an allowance is shown: the tray panel (page.js's
// panelPageCut), the menu bar's cells, tooltip and Windows' tray figure,
// and the TUI's Usage page. Only what is shown: routing, caps and counting
// keep every card.

// ShownQuotas is qs less the cards hidden (s.UsageCardsHidden) and the rows
// hidden (s.UsageWindowsHidden), as the Overview draws them.
func ShownQuotas(qs []SubscriptionQuota, s settings.Settings) []SubscriptionQuota {
	if qs == nil || len(s.UsageCardsHidden)+len(s.UsageWindowsHidden) == 0 {
		return qs
	}
	out := make([]SubscriptionQuota, 0, len(qs))
	for _, q := range qs {
		if QuotaCardHidden(q, s) {
			continue
		}
		out = append(out, QuotaRowsShown(q, s))
	}
	return out
}

// QuotaCardHidden is whether the Overview hides q's card.
func QuotaCardHidden(q SubscriptionQuota, s settings.Settings) bool {
	return len(s.UsageCardsHidden) > 0 && slices.Contains(s.UsageCardsHidden, QuotaHideKey(q))
}

// QuotaRowsShown is q less the rows the Overview hides, by provider and the
// row's group (a pool's or family's name, else the window's), unless that
// would leave it with none.
func QuotaRowsShown(q SubscriptionQuota, s settings.Settings) SubscriptionQuota {
	if q.Error != "" || len(q.Windows) == 0 || len(s.UsageWindowsHidden) == 0 {
		return q
	}
	ws := slices.DeleteFunc(slices.Clone(q.Windows), func(w QuotaWindow) bool {
		return slices.Contains(s.UsageWindowsHidden, q.Provider+"|"+cmp.Or(w.Pool, w.Family, w.Name))
	})
	if len(ws) > 0 {
		q.Windows = ws
	}
	return q
}

// QuotaHideKey is how the app keeps a hidden card (cardHideKey in
// page.js): its tray card id, then "|balance" for a balance alone, else
// "|plan".
func QuotaHideKey(q SubscriptionQuota) string {
	id := q.Provider
	if q.User != "" {
		id += "|" + q.User
	}
	if q.Balance != "" && len(q.Windows) == 0 && q.Plan == "" {
		return id + "|balance"
	}
	return id + "|plan"
}
