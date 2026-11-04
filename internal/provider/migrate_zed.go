package provider

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Zed's accounts on @magpie-community/opencode-zed-auth: the access token
// as the oauth entry's access, the rest the calls need as its refresh
// (Zed has no refresh token: the pair lasts until Zed refuses it).
func init() {
	movers["zed"] = &mover{
		pkg:    "@magpie-community/opencode-zed-auth",
		min:    "0.1.17", // a thinking display Zed doesn't know ("updates" from Claude Code) is not sent, and "highlights" goes as summarized (plugins#708); 0.1.15: an Anthropic request's context_management reaches Zed with compact_20260112 edits only, which Zed reads, not Claude Code's clear_thinking_20251015 it refuses (nllay, plugins#636); 0.1.14: a Gemini model's effort variants are keyed lowercase whatever case Zed's /models lists them in, so pi's thinking levels match them, and its thinking level goes to Zed uppercase, which Zed reads, not "low" it refuses (Yezike-yyy, ump45nose, plugins#215); 0.1.13: a Gemini request's tool-calling mode goes to Zed in lowercase, which Zed reads, not AUTO it refuses (atticuskk, #4657); 0.1.12: a string message content goes to Zed as a text block, which Zed takes, not a string it refuses (Been on Discord); 0.1.11: a {role: "system"} message inside an Anthropic request goes to Zed as user text in place, not a role Zed refuses (Roy on Discord); 0.1.10: the organization with a plan is picked, and a 402 for the saved one moves the account (plugins#232); 0.1.9: a signed-in account whose model list was never read says why; 0.1.8: a free account's plan reads "No plan", as the built-in's; 0.1.6: a failure's status and its sign-in mark as the built-in's; a model token refused while listing marks the account
		agents: []string{"zed"},
		out: func() ([]Moving, error) {
			var out []Moving
			for _, l := range zedLogins() {
				c := l.creds
				out = append(out, Moving{User: l.User, First: l.Active, On: l.On, Lapsed: l.Lapsed != "", Plan: l.Plan, Auth: map[string]any{
					"type":      "oauth",
					"access":    c.Access,
					"refresh":   jsonText(zedRefresh(c)),
					"expires":   0,
					"accountId": l.User,
				}})
			}
			return out, nil
		},
		back: func(ls []savedLogin, user string, auth map[string]any) ([]savedLogin, string, error) {
			var r struct {
				UserID   any             `json:"userId"`
				SystemID string          `json:"systemId"`
				Org      string          `json:"org"`
				Plan     string          `json:"plan"`
				Login    string          `json:"login"`
				Name     string          `json:"name"`
				Models   json.RawMessage `json:"models"`
			}
			access := str(auth["access"])
			if json.Unmarshal([]byte(str(auth["refresh"])), &r) != nil || r.UserID == nil || access == "" {
				return ls, "", errors.New("Zed: an unreadable plugin sign-in")
			}
			if user == "" {
				user = str(auth["accountId"])
			}
			i := backInto(&ls, "zed", user)
			c, _ := zedSaved(ls[i])
			// the plugin may keep the id as the number Zed gives
			c.UserID, c.Access, c.SystemID, c.Org, c.Plan = strings.Trim(jsonText(r.UserID), `"`), access, r.SystemID, r.Org, r.Plan
			if r.Login != "" {
				c.Login = r.Login
			}
			if r.Name != "" {
				c.Name = r.Name
			}
			if len(c.Models) == 0 && len(r.Models) > 0 {
				c.Models = r.Models // its list is back before its first read
			}
			b, err := json.Marshal(c)
			if err != nil {
				return ls, "", err
			}
			ls[i].Auth, ls[i].Lapsed, ls[i].Seen = b, "", time.Now().UTC().Truncate(time.Second)
			return ls, ls[i].User, nil
		},
	}
}

// zedRefresh is what the plugin keeps as the account's refresh: with the
// list Zed last gave the account, so the plugin can serve that until it
// reads its own, not the families it declares for a sign-in with none.
func zedRefresh(c zedCreds) map[string]any {
	r := map[string]any{"userId": c.UserID, "systemId": c.SystemID, "org": c.Org, "plan": c.Plan, "login": c.Login, "name": c.Name}
	if json.Valid(c.Models) && len(c.Models) > 0 {
		r["models"] = c.Models
	}
	return r
}

// backInto is the index in ls of agent's saved account user, added (on)
// when there is none.
func backInto(ls *[]savedLogin, agent, user string) int {
	for i, l := range *ls {
		if l.Agent == agent && strings.EqualFold(l.User, user) && !l.own() {
			return i
		}
	}
	*ls = append(*ls, savedLogin{Agent: agent, User: user, On: true, Seen: time.Now().UTC().Truncate(time.Second)})
	return len(*ls) - 1
}

func jsonText(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func str(v any) string {
	s, _ := v.(string)
	return s
}
