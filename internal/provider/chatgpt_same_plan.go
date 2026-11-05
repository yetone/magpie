package provider

import (
	"cmp"
	"encoding/json"
	"strings"
)

// One ChatGPT plan, two ways in (jctaoo on Discord): an account signed in
// through Sign in with ChatGPT (chatgpt-api) and the same account signed in
// to Codex draw on one plan's five-hour and weekly limits, while only the
// Codex account's card reads them. A whole-window estimate (usage's
// WithWindowHolds) over the Codex card counts the chatgpt-api account's
// calls too, by this.

// SamePlanAccounts maps each ChatGPT API account to the Codex account that
// is the same ChatGPT account, both as provider + "\x00" + lowercased name,
// the way the usage ledger's calls are keyed. They are matched on what the
// tokens say, never on a name: the ChatGPT account (workspace) id and the
// user's id, both known and both equal. Two Team seats of one email share
// a name and a user, not an account id; an account whose tokens don't say
// both is matched to none.
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
	if len(codex) == 0 {
		return out
	}
	for _, l := range ls {
		if l.Agent != ChatGPTAPIID {
			continue
		}
		c, ok := siwcSaved(l)
		if !ok {
			continue
		}
		w := who{chatgptAccountID(c.IDToken, c.Access), chatgptUserID(c.IDToken, c.Access)}
		if w.account == "" || w.user == "" {
			continue
		}
		if to, ok := codex[w]; ok {
			out[ChatGPTAPIID+"\x00"+strings.ToLower(l.User)] = to
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
