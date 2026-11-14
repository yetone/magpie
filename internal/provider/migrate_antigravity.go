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
		min:    "0.1.12", // every turned-away line goes reworded, not broken up: Google reads past zero-width since 11-14 (0.1.11 omp's "key words", 0.1.0 ported the built-in)
		agents: []string{"antigravity"},
		out: func() ([]Moving, error) {
			var out []Moving
			for _, l := range googleLogins("antigravity") {
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
