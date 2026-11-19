package provider

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Antigravity's accounts on @magpie-community/opencode-antigravity-auth:
// the plugin keeps a Google sign-in as an oauth entry whose refresh is
// "<refresh token>|<project>", with the access token and its expiry beside
// it and the email as its accountId. An account the built-in had no
// project for yet goes with none, and the plugin asks Code Assist for it
// (loadCodeAssist, onboardUser) as the built-in did.
func init() {
	movers["antigravity"] = &mover{
		pkg:    "@magpie-community/opencode-antigravity-auth",
		min:    "0.2.4", // 0.2.4 says the id a request went out under (x-magpie-sent-model), the variant the effort picked, for the ledger and Routing; 0.2.3 Claude Code's identity line goes as "You're …" (#1151); 0.2.1 serves again as 0.1.18 did, for the users who have it, and is withdrawn for anyone new (plugin.WithdrawnOf; owner, 11-15: 正在使用的继续正常，加个提示，新用户不让下载); 0.2.0 retired it, sending nothing; 0.1.18 a variant at xhigh (Gemini 4 Hydrogen) is asked at xhigh (plugins#1169); 0.1.14: Gemini 4's late INVALID_ARGUMENT, a bare line in the stream, is asked again and says it isn't a region block (0.1.13 Codex's GPT-5 line reworded in a user's turn, 0.1.12 rewords instead of zero-width, 0.1.11 omp's "key words", 0.1.0 ported the built-in)
		agents: []string{"antigravity"},
		out: func() ([]Moving, error) {
			var out []Moving
			for _, l := range googleSavedLogins("antigravity") {
				a := l.acct.auth
				if a.RefreshToken == "" {
					continue
				}
				out = append(out, Moving{User: l.User, First: l.Active, On: l.On, Lapsed: l.Lapsed != "", Plan: l.Plan, Auth: map[string]any{
					"type":      "oauth",
					"refresh":   a.RefreshToken + "|" + a.Project,
					"access":    a.AccessToken,
					"expires":   a.Expiry,
					"accountId": l.User,
				}})
			}
			return out, nil
		},
		back: func(ls []savedLogin, user string, auth map[string]any) ([]savedLogin, string, error) {
			rt, project, _ := strings.Cut(str(auth["refresh"]), "|")
			if rt == "" {
				return ls, "", errors.New("Antigravity: an unreadable plugin sign-in")
			}
			if user == "" {
				user = str(auth["accountId"])
			}
			if user == "" {
				return ls, "", errors.New("Antigravity: a plugin sign-in with no account")
			}
			i := backInto(&ls, "antigravity", user)
			var a googleAuth
			_ = json.Unmarshal(ls[i].Auth, &a)
			a.RefreshToken, a.AccessToken = rt, str(auth["access"])
			if e, ok := auth["expires"].(float64); ok {
				a.Expiry = int64(e)
			}
			if project != "" {
				a.Project = project
			}
			b, err := json.Marshal(a)
			if err != nil {
				return ls, "", err
			}
			ls[i].Auth, ls[i].Lapsed, ls[i].Refused, ls[i].Seen = b, "", "", time.Now().UTC().Truncate(time.Second)
			if project != "" {
				ls[i].Project = project
			}
			return ls, ls[i].User, nil
		},
	}
}
