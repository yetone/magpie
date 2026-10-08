package provider

import (
	"encoding/json"
	"errors"
	"time"
)

// WorkBuddy's and WorkBuddy AI's accounts on
// @magpie-community/opencode-workbuddy-auth (one package, a provider for
// each build). The desktop app's own sign-in goes as a marker ({source:
// "desktop"}, the package from 0.1.1 on): the plugin reads the app's file
// on each request, as the built-in does, so no refresh token is held by
// both. An account magpie signed in goes whole.
func init() {
	for _, w := range []*wbSite{wbCN, wbAI} {
		movers[w.id] = &mover{
			pkg:    "@magpie-community/opencode-workbuddy-auth",
			min:    "0.1.12", // a chat's tools get shapes WorkBuddy AI's models take, so ZCode's and DeepSeek Harness's chats on deepseek-v4.1-flash aren't 400 "Invalid request parameters" (wyh on Discord); one user send is one request in the usage list: every step carries one X-Conversation-Request-ID, and both builds name the client (使用端) (plugins #48); credits used up (code 14018) say so, with the nested error message (plugins #25); Codex's and Claude Code's chats pass WorkBuddy's channel check (#182); a failure's status and its sign-in mark as the built-in's; each model's credit rate; the sign-in renewed ahead of time through auth.refresh; the credits' count beside their share (#659)
			agents: []string{w.id},
			out: func() ([]Moving, error) {
				var out []Moving
				for _, a := range wbLogins(w) {
					m := Moving{User: a.User, First: a.Active, On: a.On, Lapsed: a.Lapsed != "", Own: a.own, Plan: a.Plan}
					if a.own {
						m.Auth = map[string]any{"type": "oauth", "source": "desktop", "access": "", "refresh": "", "expires": 0,
							"accountId": a.User, "uid": a.creds.UID}
					} else {
						c := a.creds
						m.Auth = map[string]any{"type": "oauth", "access": c.Access, "refresh": c.Refresh, "expires": c.ExpiresAt,
							"accountId": a.User, "uid": c.UID, "domain": c.Domain}
						if c.RefreshExpiresAt > 0 {
							m.Auth["refreshExpiresAt"] = c.RefreshExpiresAt
						}
						if c.TokenType != "" {
							m.Auth["tokenType"] = c.TokenType
						}
					}
					out = append(out, m)
				}
				return out, nil
			},
			back: func(ls []savedLogin, user string, auth map[string]any) ([]savedLogin, string, error) {
				if str(auth["source"]) != "" {
					// the desktop app's: nothing of it was the plugin's
					return ls, firstNonEmpty(user, str(auth["accountId"])), nil
				}
				c := wbCreds{UID: str(auth["uid"]), Access: str(auth["access"]), Refresh: str(auth["refresh"]),
					ExpiresAt: msOf(auth["expires"]), RefreshExpiresAt: msOf(auth["refreshExpiresAt"]),
					Domain: str(auth["domain"]), TokenType: str(auth["tokenType"])}
				if c.UID == "" || c.Access == "" {
					return ls, "", errors.New(w.name + ": an unreadable plugin sign-in")
				}
				if user == "" {
					user = str(auth["accountId"])
				}
				i := backInto(&ls, w.id, user)
				b, err := json.Marshal(c)
				if err != nil {
					return ls, "", err
				}
				ls[i].Auth, ls[i].Lapsed, ls[i].Seen = b, "", time.Now().UTC().Truncate(time.Second)
				wbTokens.Lock()
				delete(wbTokens.m, w.id+"|"+c.UID)
				wbTokens.Unlock()
				return ls, ls[i].User, nil
			},
		}
	}
}

// msOf is a time in unix ms as JSON gave it.
func msOf(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	}
	return 0
}
