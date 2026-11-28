package provider

import (
	"cmp"
	"context"
	"encoding/json"
	"maps"
	"strings"

	"github.com/yetone/magpie/internal/settings"
)

// One ChatGPT plan, two ways in (jctaoo on Discord): an account signed in
// through Sign in with ChatGPT (chatgpt-api) and the same account signed in
// to Codex draw on one plan's five-hour and weekly limits, while only the
// Codex account's card reads them. A whole-window estimate (usage's
// WithWindowHolds) over the Codex card counts the chatgpt-api account's
// calls too, by this, and routing weighs the chatgpt-api account by the
// Codex account's reading (samePlanAllowances): unknown, it went after
// every known account though it spends the same plan (tmr11235 on X).

// SamePlanAccounts maps each ChatGPT API account to the Codex account that
// is the same ChatGPT account, both as provider + "\x00" + lowercased name,
// the way the usage ledger's calls are keyed. They are matched on what the
// tokens say, never on a name: the ChatGPT account (workspace) id and the
// user's id, both known and both equal. Two Team seats of one email share
// a name and a user, not an account id. OpenAI's Sign in with ChatGPT
// tokens say neither, so an account is also matched to the Codex sign-in
// whose app list was read holding its client id once (siwcTwinKnown,
// chatgpt_app_twin.go); nothing is asked here.
func SamePlanAccounts() map[string]string {
	ls := sharedLogins()
	type who struct{ account, user string }
	codex := map[who]string{}
	for _, l := range ls {
		if l.Agent != "codex" {
			continue
		}
		var a codexAuth
		if json.Unmarshal(l.Auth, &a) != nil {
			continue
		}
		w := who{codexAccountID(a), chatgptUserID(a.Tokens.IDToken, a.Tokens.AccessToken)}
		if w.account == "" || w.user == "" {
			continue
		}
		if _, ok := codex[w]; !ok {
			codex[w] = "codex\x00" + strings.ToLower(l.User)
		}
	}
	out := map[string]string{}
	for _, l := range ls {
		if l.Agent != ChatGPTAPIID {
			continue
		}
		c, ok := siwcSaved(l)
		if !ok {
			continue
		}
		key := ChatGPTAPIID + "\x00" + strings.ToLower(l.User)
		w := who{chatgptAccountID(c.IDToken, c.Access), chatgptUserID(c.IDToken, c.Access)}
		if to, ok := codex[w]; ok && w.account != "" && w.user != "" {
			out[key] = to
			continue
		}
		// OpenAI's own tokens say neither: the Codex sign-in whose app
		// list holds this account's client id (siwcTwin), as read already
		if to, ok := siwcTwinKnown(siwcAppID(c)); ok {
			out[key] = to
		}
	}
	return out
}

// chatgptAccountID is the ChatGPT account (workspace) the first of toks
// that names one is for.
func chatgptAccountID(toks ...string) string {
	for _, t := range toks {
		if a := claimString(jwtClaims(t), "https://api.openai.com/auth", "chatgpt_account_id"); a != "" {
			return a
		}
	}
	return ""
}

// chatgptUserID is the ChatGPT user the first of toks that names one is
// for: its chatgpt_user_id, as OpenAI's own tokens say it beside the
// account id.
func chatgptUserID(toks ...string) string {
	for _, t := range toks {
		auth := jwtClaims(t)
		if u := cmp.Or(claimString(auth, "https://api.openai.com/auth", "chatgpt_user_id"), claimString(auth, "https://api.openai.com/auth", "user_id")); u != "" {
			return u
		}
	}
	return ""
}

// samePlanAllowances is own, the ChatGPT API accounts' allowances by user,
// with each one that is the same ChatGPT account as a Codex one given that
// Codex account's: one plan, one five hours and one week, which only the
// Codex sign-in can read. One read of its own stands. own is not changed.
func samePlanAllowances(own map[string]Allowance) map[string]Allowance {
	same := SamePlanAccounts()
	if len(same) == 0 {
		return own
	}
	var codex, out map[string]Allowance
	for _, l := range sharedLogins() {
		if l.Agent != ChatGPTAPIID {
			continue
		}
		if _, has := own[l.User]; has {
			continue
		}
		to, ok := same[ChatGPTAPIID+"\x00"+strings.ToLower(l.User)]
		if !ok {
			continue
		}
		if codex == nil {
			codex = Allowances("codex")
		}
		for u, a := range codex {
			if "codex\x00"+strings.ToLower(u) != to {
				continue
			}
			if out == nil {
				out = maps.Clone(own)
				if out == nil {
					out = map[string]Allowance{}
				}
			}
			out[l.User] = a
			break
		}
	}
	if out == nil {
		return own
	}
	return out
}

// samePlanReadings is own, the ChatGPT API accounts' readings as their
// account list shows them, with each one that is the same ChatGPT account
// as a Codex one (SamePlanAccounts) showing that Codex account's reading
// instead of siwcNoUsage (tmr11235 on X: an account signed in to both,
// only for the Codex row to show its windows). OpenAI counts a Sign in
// with ChatGPT app's requests against the plan's included ChatGPT Work
// and Codex allowance, the one pool /wham/usage reads. The reading is the
// Codex account's own, through its cache (someLoginUsage), so nothing is
// asked that its own row wouldn't ask. The plan's resets, the ones magpie
// spent, its credits and the plan's end go with it (tmr11235 on X: "补一下
// 套餐重置卡什么的"), since they are the plan's and a reset used from either
// row resets both; only the Codex app's hold stays on the Codex row, being
// the Codex app's. SamePlan names the Codex account. An account with no
// Codex twin, or whose twin has nothing read, keeps its own (siwcNoUsage
// or "not read yet"), and a switched-off one says so. own is not changed.
func samePlanReadings(ctx context.Context, own map[string]SubscriptionQuota) map[string]SubscriptionQuota {
	if len(own) == 0 {
		return own
	}
	same := SamePlanAccounts()
	if len(same) == 0 {
		return own
	}
	codex, _ := usageLogins("codex")
	byKey := map[string]Login{}
	for _, l := range codex {
		byKey["codex\x00"+strings.ToLower(l.User)] = l
	}
	twins := map[string]Login{}
	var want []Login
	var users []string
	for user, q := range own {
		if len(q.Windows) > 0 || q.Balance != "" || strings.Contains(q.Error, errLeftAlone.Error()) {
			continue // one read of its own, or switched off, stands
		}
		l, ok := byKey[same[ChatGPTAPIID+"\x00"+strings.ToLower(user)]]
		if !ok {
			continue
		}
		twins[user] = l
		want, users = append(want, l), append(users, l.User)
	}
	if len(twins) == 0 {
		return own
	}
	var read map[string]SubscriptionQuota
	if !settings.AccountDiscoveryOff() && leftAlone("codex") {
		read = keptLoginUsage(want)
	} else {
		read = someLoginUsage(ctx, "codex", users...)
	}
	out := maps.Clone(own)
	for user, l := range twins {
		q, ok := read[l.User]
		if !ok {
			continue
		}
		mine := own[user]
		if len(q.Windows) == 0 && !q.ReadDeferred {
			if q.Error == "" {
				continue
			}
			// the twin's read failed: why, not where else to look
			mine.Error, mine.SeeAt, mine.SamePlan = q.Error, "", l.User
			out[user] = mine
			continue
		}
		q.Provider, q.Name, q.Icon, q.User = mine.Provider, mine.Name, mine.Icon, mine.User
		q.Plan = cmp.Or(q.Plan, mine.Plan)
		q.SeeAt = ""
		q.Held = false
		q.SamePlan = l.User
		out[user] = q
	}
	return out
}
