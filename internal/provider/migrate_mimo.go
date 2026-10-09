package provider

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// MiMo's accounts on @magpie-community/opencode-mimo-auth, as its sign-in
// keeps one: the Xiaomi account (what signs it on again) as refresh, the
// session's cookies as access, expiring a day after they were set, when
// the app signs on again.
func init() {
	movers[MiMoID] = &mover{
		pkg:    "@magpie-community/opencode-mimo-auth",
		min:    "0.1.9", // an account with a Token Plan and no app membership is answered by its Token Plan
		agents: []string{MiMoID},
		out: func() ([]Moving, error) {
			var out []Moving
			for _, l := range mimoLogins() {
				c := l.creds
				cookies := c.Cookies
				if cookies == nil {
					cookies = map[string]string{}
				}
				var expires int64
				if !c.Issued.IsZero() {
					expires = c.Issued.Add(24 * time.Hour).UnixMilli()
				}
				out = append(out, Moving{User: l.User, First: l.Active, On: l.On, Lapsed: l.Lapsed != "", Plan: l.Plan, Auth: map[string]any{
					"type":      "oauth",
					"refresh":   jsonText(map[string]any{"userId": c.UserID, "cUserId": c.CUserID, "passToken": c.PassToken, "deviceId": c.DeviceID, "region": c.Region, "base": c.Base}),
					"access":    jsonText(cookies),
					"expires":   expires,
					"accountId": c.UserID,
				}})
			}
			return out, nil
		},
		back: func(ls []savedLogin, user string, auth map[string]any) ([]savedLogin, string, error) {
			var r struct {
				UserID    any    `json:"userId"`
				CUserID   any    `json:"cUserId"`
				PassToken string `json:"passToken"`
				DeviceID  string `json:"deviceId"`
				Region    string `json:"region"`
				Base      string `json:"base"`
			}
			if json.Unmarshal([]byte(str(auth["refresh"])), &r) != nil || r.UserID == nil || r.PassToken == "" {
				return ls, "", errors.New("Xiaomi MiMo: an unreadable plugin sign-in")
			}
			var cookies map[string]string
			_ = json.Unmarshal([]byte(str(auth["access"])), &cookies)
			uid := idText(r.UserID)
			if user == "" {
				user = uid
			}
			i := backInto(&ls, MiMoID, user)
			c, _ := mimoSaved(ls[i])
			c.UserID, c.CUserID, c.PassToken = uid, firstNonEmpty(idText(r.CUserID), c.CUserID), r.PassToken
			c.DeviceID = firstNonEmpty(r.DeviceID, c.DeviceID)
			c.Region = firstNonEmpty(r.Region, c.Region)
			c.Base = firstNonEmpty(r.Base, c.Base)
			c.Cookies = cookies
			// the session was set a day before the plugin would sign on again
			if e := num(auth["expires"]); e > 0 {
				c.Issued = time.UnixMilli(e).Add(-24 * time.Hour).UTC()
			} else {
				c.Issued = time.Time{}
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

// idText is an id a plugin may keep as a number or a string.
func idText(v any) string {
	if v == nil {
		return ""
	}
	return strings.Trim(jsonText(v), `"`)
}
