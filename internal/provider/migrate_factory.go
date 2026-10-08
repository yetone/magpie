package provider

import (
	"encoding/json"
	"errors"
	"time"
)

// Factory's accounts on @magpie-community/opencode-factory-auth, as its
// device sign-in keeps one: the WorkOS pair as access and refresh (the
// refresh token rotates, so it moves rather than being copied), the org
// and region droid needs beside them.
func init() {
	movers["factory"] = &mover{
		pkg:    "@magpie-community/opencode-factory-auth",
		min:    "0.1.19", // Claude Code compaction/runtime metadata and quoted session summaries (#1132); pi's opening sentence cut to its first half (#952); DeepSeek V4.1 Flash, Kimi K3 and GLM-5.2 offering none, so reasoning turned off stops their thinking (#899); requests saying droid 0.233.0 (JL: every model 403); Claude Code 2.1.288's runtime context as a system message of text blocks (#658); a tool whose input_schema has anyOf/oneOf/allOf at its root (Codex's automation_update) wrapped for Claude models (#646); a failure's status and its sign-in mark as the built-in's; Claude 5's system-role metadata as Factory takes it; Claude Code's standalone model-switch system updates; tool results quoting Claude Code's fixed phrases, and Claude Code's directory updates carrying a model; the sign-in renewed ahead of time through auth.refresh; Claude Code with a global CLAUDE.md or a SessionStart hook, and Claude Desktop's model line (#634)
		agents: []string{"factory"},
		out: func() ([]Moving, error) {
			ls := factoryLogins()
			// no refresh of the built-in's runs while the pairs are read
			factoryMu.Lock()
			defer factoryMu.Unlock()
			var out []Moving
			for _, l := range ls {
				c := l.creds
				out = append(out, Moving{User: l.User, First: l.Active, On: l.On, Lapsed: l.Lapsed != "", Plan: l.Plan, Auth: map[string]any{
					"type":                 "oauth",
					"access":               c.Access,
					"refresh":              c.Refresh,
					"expires":              c.ExpiresAt,
					"orgId":                c.Org,
					"email":                c.Email,
					"userId":               c.UserID,
					"activeOrganizationId": c.Active,
					"region":               c.Region,
					"premBaseHost":         c.Prem,
					"accountId":            l.User,
				}})
			}
			return out, nil
		},
		back: func(ls []savedLogin, user string, auth map[string]any) ([]savedLogin, string, error) {
			access := str(auth["access"])
			if access == "" {
				return ls, "", errors.New("Factory: an unreadable plugin sign-in")
			}
			if user == "" {
				user = firstNonEmpty(str(auth["accountId"]), str(auth["email"]), str(auth["userId"]))
			}
			i := backInto(&ls, "factory", user)
			c, _ := factorySaved(ls[i])
			c.Access, c.Refresh, c.ExpiresAt = access, str(auth["refresh"]), num(auth["expires"])
			c.Org = firstNonEmpty(str(auth["orgId"]), c.Org)
			c.Active = firstNonEmpty(str(auth["activeOrganizationId"]), c.Active)
			c.Email = firstNonEmpty(str(auth["email"]), c.Email)
			c.UserID = firstNonEmpty(str(auth["userId"]), c.UserID)
			c.Region = firstNonEmpty(str(auth["region"]), c.Region)
			c.Prem = firstNonEmpty(str(auth["premBaseHost"]), c.Prem)
			b, err := json.Marshal(c)
			if err != nil {
				return ls, "", err
			}
			ls[i].Auth, ls[i].Lapsed, ls[i].Seen = b, "", time.Now().UTC().Truncate(time.Second)
			return ls, ls[i].User, nil
		},
	}
}
