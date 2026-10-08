package provider

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/yetone/magpie/internal/qoder"
)

// Qoder's and Qoder CN's accounts on @magpie-community/opencode-qoder-auth,
// which serves both sites as providers "qoder" and "qoder-cn", as its device
// sign-in keeps one: the job token pair as access and refresh (the refresh
// token is spent on use, so it moves rather than being copied), the device
// token pair and machine id beside them. A Qoder CN account that chats on
// its device token (DeviceChat) says so, its device pair as access and
// refresh.
func init() {
	for _, id := range []string{"qoder", QoderCNID} {
		movers[id] = qoderMover(id)
	}
}

func qoderMover(id string) *mover {
	return &mover{
		pkg:    "@magpie-community/opencode-qoder-auth",
		min:    "0.2.10", // international check-in finds the device identity runtime Qoder CLI caches on macOS and Linux and the Linux desktop installs in /opt/Qoder (ARNO on Discord); sends Qoder's native device identity, without which the daily 100 credits aren't listed (#1268); Qoder CN enterprise VPC (0.2.8); the daily credits claimed through the account's fetch (ARNO on Discord); Qoder CN as "qoder-cn", its device-token chat as the built-in's; each model's credit rate; the sign-in renewed ahead of time through auth.refresh; token usage read; each model's context the largest window Qoder offers it (#722); the window a request is sent holds the reply's max_tokens (#700)
		agents: []string{id},
		out: func() ([]Moving, error) {
			// no refresh of the built-in's runs while the pairs are read
			ls := qoderLoginsOf(id)
			qoderMu.Lock()
			defer qoderMu.Unlock()
			var out []Moving
			for _, l := range ls {
				c, ok, _ := qoderCurrent(l.saved)
				if !ok {
					continue
				}
				out = append(out, Moving{User: l.User, First: l.Active, On: l.On, Lapsed: l.Lapsed != "", Plan: l.Plan, Auth: map[string]any{
					"type":          "oauth",
					"access":        c.Token,
					"refresh":       c.RefreshToken,
					"expires":       c.ExpiresAt,
					"accountId":     l.User,
					"uid":           c.UID,
					"email":         c.Email,
					"name":          c.Name,
					"machineId":     c.MachineID,
					"deviceToken":   c.DeviceToken,
					"deviceRefresh": c.DeviceRefresh,
				}})
				if c.DeviceChat {
					out[len(out)-1].Auth["deviceChat"] = true
				}
			}
			return out, nil
		},
		back: func(ls []savedLogin, user string, auth map[string]any) ([]savedLogin, string, error) {
			access, uid := str(auth["access"]), str(auth["uid"])
			if access == "" || uid == "" {
				return ls, "", errors.New(qoder.SiteOf(id).Name + ": an unreadable plugin sign-in")
			}
			if user == "" {
				user = firstNonEmpty(str(auth["accountId"]), str(auth["email"]), uid)
			}
			i := backInto(&ls, id, user)
			c, _ := qoderSaved(ls[i])
			c.UID, c.Token, c.RefreshToken, c.ExpiresAt = uid, access, str(auth["refresh"]), num(auth["expires"])
			c.Email = firstNonEmpty(str(auth["email"]), c.Email)
			c.Name = firstNonEmpty(str(auth["name"]), c.Name)
			c.MachineID = firstNonEmpty(str(auth["machineId"]), c.MachineID)
			c.DeviceToken = firstNonEmpty(str(auth["deviceToken"]), c.DeviceToken)
			c.DeviceRefresh = firstNonEmpty(str(auth["deviceRefresh"]), c.DeviceRefresh)
			c.DeviceChat = auth["deviceChat"] == true
			b, err := json.Marshal(c)
			if err != nil {
				return ls, "", err
			}
			ls[i].Auth, ls[i].Lapsed, ls[i].Seen = b, "", time.Now().UTC().Truncate(time.Second)
			return ls, ls[i].User, nil
		},
	}
}

// num is a JSON number read into any (a float64, or an int64 before it
// was written out) as an int64.
func num(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	case json.Number:
		i, _ := n.Int64()
		return i
	}
	return 0
}
