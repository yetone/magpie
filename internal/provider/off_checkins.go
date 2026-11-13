package provider

// OffCheckins are the accounts a daily check-in reaches whose provider
// the user switched off (wxkwxk666 on X: 禁用的供应商也要能签到). The Usage
// page leaves a switched-off provider's cards out (visibleQuotas), and the
// daily loop leaves it alone (leftAlone), so these are shown in a folded
// group of their own, each to be checked in by hand (CheckInAccount),
// which switches nothing back on.
//
// Each is a card with no windows: the provider, its name and icon, the
// account, and its check-in, found by its own key. No allowance is
// read and nothing is asked; a provider whose account was removed from
// magpie (Hidden) is not listed.
func OffCheckins() []SubscriptionQuota {
	off := map[string]Provider{}
	for _, p := range All() {
		if p.Off && !p.Hidden {
			off[p.ID] = p
		}
	}
	if len(off) == 0 {
		return nil
	}
	var qs []SubscriptionQuota
	seen := map[string]bool{}
	skip := checkinOff()
	for _, c := range checkiners() {
		var st map[string]WorkBuddyCheckin
		for _, a := range c.accounts() {
			p, ok := off[a.provider]
			if !ok || !a.On || a.key == "" || seen[c.by+"\x00"+a.key] {
				continue
			}
			seen[c.by+"\x00"+a.key] = true
			name, icon := p.Name, p.Icon
			if pp, ok := PluginOf(a.provider); ok {
				name, icon = pluginCard(pp)
			}
			if name == "" {
				name = a.provider
			}
			// marked by its own key, not by its name as WithCheckins does
			// a card: two sign-ins can share a name (plugins#254)
			q := SubscriptionQuota{Provider: a.provider, Name: name, Icon: icon, User: a.User, Windows: []QuotaWindow{},
				Checkins: true, CheckinBy: c.by, CheckinKey: a.key, CheckinOff: skip[a.key]}
			if st == nil {
				st = shownCheckins(c.path)
			}
			if r, ok := st[a.key]; ok {
				r.User, r.By = a.User, c.by
				q.Checkin = &r
			}
			qs = append(qs, q)
		}
	}
	return InProviderOrder(qs)
}
