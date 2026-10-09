package provider

// A plugin's accounts show their allowance as the built-ins' do: on the
// usage page, in the menu bar, beside each account, and to the gateway,
// which passes over one used up. The plugin tells it (auth.usage, see
// internal/plugin/host.js); a built-in moved onto its plugin keeps its
// card's name and icon.

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/plugin"
)

// movedCards are the names and icons the built-ins' usage cards have,
// and the sites their providers link to.
var movedCards = map[string]struct{ name, icon, site string }{
	"zed":             {"Zed", "zed", "https://zed.dev"},
	"qoder":           {"Qoder", "qoder", "https://qoder.com"},
	"factory":         {"Factory", "factory", "https://factory.ai"},
	MiMoID:            {"Xiaomi MiMo", "mimocode", "https://mimo-ai.xiaomimimo.com"},
	CommandCodePlanID: {"Command Code", "commandcode", cmdStudio},
	"kiro":            {"Kiro", "kiro-color", "https://kiro.dev"},
	"zcode":           {"ZCode", "zcode", "https://zcode.z.ai"},
	"workbuddy":       {"WorkBuddy", "workbuddy-color", "https://www.codebuddy.cn"},
	WorkBuddyAIID:     {"WorkBuddy AI", "workbuddy-color", "https://www.workbuddy.ai"},
	"cursor":          {"Cursor", "cursor", "https://cursor.com"},
	"grok":            {"Grok (SuperGrok)", "xai", "https://x.ai/cli"},
	"devin":           {"Devin", "devin", "https://devin.ai"},
}

// builtinHost is the API host the built-in id shows when it served itself,
// "" for those that show none (their requests go through their own code).
func builtinHost(id string) string {
	switch id {
	case "grok":
		return HostOf(GrokBase)
	case CommandCodePlanID:
		return HostOf(cmdAPI)
	case "factory":
		return HostOf(factoryAPI)
	case "zcode":
		return HostOf(ZCodeZaiBase)
	case "workbuddy":
		return HostOf(wbCN.api())
	case WorkBuddyAIID:
		return HostOf(wbAI.api())
	}
	return ""
}

// movedNames are the built-ins' provider names that aren't their card's.
var movedNames = map[string]string{CommandCodePlanID: "Command Code Plan"}

// pluginCard is the name and icon pp's usage cards show.
func pluginCard(pp plugin.Provider) (string, string) {
	if c, ok := movedCards[pp.ID]; ok && Moved(pp.ID) {
		return c.name, c.icon
	}
	name := pp.Name
	if name == "" {
		name = pp.ID
	}
	return name, PluginIcon(pp)
}

// pluginUsageLogins are the accounts of pp whose allowance can be asked.
func pluginUsageLogins(pp plugin.Provider) []Login {
	if !pp.Usage || !pp.SignedIn || movingNow(pp.ID) {
		return nil
	}
	return pluginLoginList(pp)
}

// signInGone is a plugin's usage saying the account's sign-in is gone.
var signInGone = regexp.MustCompile(`(?i)sign-in has (expired|lapsed)|sign in again`)

// pluginLoginQuota is the allowance of one of a plugin provider's
// accounts, l.Agent being "plugin:" and its id.
func pluginLoginQuota(ctx context.Context, l Login) SubscriptionQuota {
	pp, ok := pluginOfAgent(l.Agent)
	name, icon := pluginCard(pp)
	q := SubscriptionQuota{Provider: PluginID(pp.ID), Name: name, Icon: icon, Plan: l.Plan, User: l.User, Windows: []QuotaWindow{}}
	if !ok {
		q.Error = "no such plugin provider"
		return q
	}
	key := ""
	for _, x := range pluginLogins(pp) {
		if strings.EqualFold(x.User, l.User) {
			key = x.acct.Key
		}
	}
	if key == "" {
		q.Error = "not signed in"
		return q
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	began := pluginSignInAt()
	u, err := plugin.AccountUsage(ctx, pp.ID, key)
	if err != nil {
		q.Error = err.Error()
		return q
	}
	// the plugin says what the read means for the sign-in, as a built-in's
	// usage read marked the account or left it; one that doesn't say has
	// an error to sign in again mark it and a clean read clear it. What a
	// request was answered while it read is newer, and stands.
	switch {
	case u.SignIn == "expired":
		notePluginLapseSince(pp, key, http.StatusUnauthorized, began)
	case u.SignIn == "renewed":
		notePluginLapseSince(pp, key, http.StatusOK, began)
	case u.SignIn == "kept":
	case signInGone.MatchString(u.Error):
		notePluginLapseSince(pp, key, http.StatusUnauthorized, began)
	case u.Error == "":
		notePluginLapseSince(pp, key, http.StatusOK, began)
	}
	keepPluginPlan(pp, key, u.Plan)
	return quotaOfPlugin(q, u)
}

// quotaOfPlugin fills q with what the plugin told.
func quotaOfPlugin(q SubscriptionQuota, u plugin.Usage) SubscriptionQuota {
	if u.Plan != "" {
		q.Plan = u.Plan
	}
	if u.User != "" {
		q.User = u.User
	}
	q.Balance, q.Renew, q.Error = u.Balance, u.Renew, u.Error
	if t, err := time.Parse(time.RFC3339, u.Until); err == nil {
		q.Until = &t
	}
	if r := u.Resets; r != nil {
		q.Resets = &ResetCredits{Count: r.Count, ByWindow: r.ByWindow, FiveHour: r.FiveHour, Weekly: r.Weekly}
		if t, err := time.Parse(time.RFC3339, r.Until); err == nil {
			q.Resets.Until = &t
		}
	}
	for _, x := range u.Windows {
		w := QuotaWindow{Name: x.Name, Used: x.Used, ResetSecs: x.ResetSecs, Display: x.Display,
			Amount: x.Amount, Limit: x.Limit, Unit: x.Unit,
			Span: time.Duration(x.Span * float64(time.Second)), Model: strings.ToLower(x.Model), Aside: x.Aside}
		if t, err := time.Parse(time.RFC3339, x.ResetsAt); err == nil {
			w.ResetsAt = &t
		}
		if set := lowerSet(x.Models); set != nil {
			w.matches = func(model string) bool { return set[strings.ToLower(model)] }
		} else if set := lowerSet(x.NotModels); set != nil {
			w.matches = func(model string) bool { return !set[strings.ToLower(model)] }
		}
		q.Windows = append(q.Windows, w)
	}
	return q
}

func lowerSet(ids []string) map[string]bool {
	if len(ids) == 0 {
		return nil
	}
	m := map[string]bool{}
	for _, id := range ids {
		m[strings.ToLower(id)] = true
	}
	return m
}

// pluginUsageFetches are a card's fetch for each plugin account that
// tells its allowance, but those placed already where their built-in's
// cards were.
func pluginUsageFetches(via func(string) context.Context, hidden, placed map[string]bool) []func() SubscriptionQuota {
	var out []func() SubscriptionQuota
	for _, pp := range plugin.Cached() {
		id := PluginID(pp.ID)
		if hidden[id] || placed[pp.ID] {
			continue
		}
		ls := pluginUsageLogins(pp)
		if len(ls) == 0 {
			continue
		}
		out = append(out, perLogin(via(id), ls, pluginCardFace(pp))...)
	}
	return out
}

// pluginUsageFetchesOf are the cards of the plugin provider id.
func pluginUsageFetchesOf(via func(string) context.Context, id string) []func() SubscriptionQuota {
	for _, pp := range plugin.Cached() {
		if pp.ID == id {
			return perLogin(via(id), pluginUsageLogins(pp), pluginCardFace(pp))
		}
	}
	return nil
}

// UsageAgent is the agent an account's allowance is asked for by
// (LoginUsage, Allowances): a plugin's accounts by their provider's.
func (a *Account) UsageAgent() string {
	if a.plugin != nil {
		return "plugin:" + a.plugin.ID
	}
	return a.Agent
}
