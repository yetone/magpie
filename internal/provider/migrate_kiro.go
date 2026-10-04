package provider

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Kiro's accounts on @magpie-community/opencode-kiro-auth:
//   - kiro-cli's or the IDE's own sign-in goes as a marker ({source:
//     "kiro"}): the plugin reads it where they keep it, as the built-in did;
//   - an account magpie signed in (a home holding kiro-auth-token.json)
//     goes whole, its tokens the plugin's to refresh from then on; the
//     home stays, and moving back writes the plugin's tokens into it;
//   - a key saved on the provider goes as an API key account, first (with
//     a key, the built-in used nothing else), and comes off the provider
//     while the plugin has it.
func init() {
	movers["kiro"] = &mover{
		pkg:    "@magpie-community/opencode-kiro-auth",
		min:    "0.1.7", // a Builder ID sign-in asked with Builder ID's service profile, as the built-in (plugins#16)
		agents: []string{"kiro"},
		out: func() ([]Moving, error) {
			var out []Moving
			key := kiroKey()
			if key != "" {
				// named as the built-in named it, which per-account proxies
				// and headers are keyed on
				out = append(out, Moving{User: "Kiro API key", First: true, On: true, Auth: map[string]any{"type": "api", "key": key, "accountId": "Kiro API key"}})
			}
			for _, l := range kiroLogins() {
				// with a key, the built-in used nothing else: the accounts
				// go along off, the key alone in use
				m := Moving{User: l.User, First: l.Active && key == "", On: l.On && key == "", Lapsed: l.Lapsed != "", Plan: l.Plan}
				if l.Home == "" {
					m.Own = true
					m.Auth = map[string]any{"type": "oauth", "source": "kiro", "access": "", "refresh": "", "expires": 0, "accountId": l.User}
				} else {
					a, err := kiroOut(l.Home, l.User, l.Plan)
					if err != nil {
						return nil, err
					}
					m.Auth = a
				}
				out = append(out, m)
			}
			return out, nil
		},
		back: func(ls []savedLogin, user string, auth map[string]any) ([]savedLogin, string, error) {
			switch {
			case str(auth["type"]) == "api":
				// the key goes back onto the provider (give, or settle for
				// one saved in the plugin since)
				return ls, "", nil
			case str(auth["source"]) != "":
				// kiro-cli's or the IDE's: nothing of it was the plugin's
				return ls, firstNonEmpty(user, str(auth["accountId"])), nil
			}
			if str(auth["access"]) == "" {
				return ls, "", errors.New("Kiro: an unreadable plugin sign-in")
			}
			if user == "" {
				user = str(auth["accountId"])
			}
			i := backInto(&ls, "kiro", user)
			if ls[i].Home == "" {
				home, err := newKiroHome()
				if err != nil {
					return ls, "", err
				}
				ls[i].Home = home
			}
			s := kiroSaved{AccessToken: str(auth["access"]), RefreshToken: str(auth["refresh"]), AuthMethod: "social",
				Provider: str(auth["loginProvider"]), Region: str(auth["region"]), ProfileArn: str(auth["profileArn"]),
				ClientID: str(auth["clientId"]), ClientSecret: str(auth["clientSecret"])}
			if str(auth["method"]) == "idc" {
				s.AuthMethod = "IdC"
			}
			if ms, _ := auth["expires"].(float64); ms > 0 {
				s.ExpiresAt = time.UnixMilli(int64(ms)).UTC().Format(time.RFC3339Nano)
			}
			b, err := json.MarshalIndent(s, "", "  ")
			if err != nil {
				return ls, "", err
			}
			if err := writePrivate(filepath.Join(ls[i].Home, kiroTokenFile), b); err != nil {
				return ls, "", err
			}
			if p := str(auth["plan"]); p != "" {
				ls[i].Plan = p
			}
			ls[i].Lapsed, ls[i].Seen = "", time.Now().UTC().Truncate(time.Second)
			kiroForgetHome(ls[i].Home)
			return ls, ls[i].User, nil
		},
		take: func() (json.RawMessage, error) {
			k := kiroKey()
			if k == "" {
				return nil, nil
			}
			return json.RawMessage(jsonText(k)), setKiroKey("")
		},
		settle: func(auths map[string]map[string]any) error {
			for _, a := range auths {
				if k := str(a["key"]); str(a["type"]) == "api" && k != "" && kiroKey() == "" {
					return setKiroKey(k)
				}
			}
			return nil
		},
		give: func(kept json.RawMessage) error {
			var k string
			if json.Unmarshal(kept, &k) != nil || k == "" || kiroKey() != "" {
				return nil
			}
			return setKiroKey(k)
		},
	}
}

// kiroOut is the account magpie signed in in home, as the plugin keeps one.
func kiroOut(home, user, plan string) (map[string]any, error) {
	b, err := os.ReadFile(filepath.Join(home, kiroTokenFile))
	if err != nil {
		return nil, err
	}
	var t kiroSaved
	if json.Unmarshal(b, &t) != nil || t.AccessToken == "" {
		return nil, errors.New("Kiro: " + user + "'s sign-in can't be read")
	}
	a := map[string]any{"type": "oauth", "access": t.AccessToken, "refresh": t.RefreshToken, "expires": 0,
		"method": "social", "loginProvider": t.Provider, "region": firstNonEmpty(t.Region, "us-east-1"),
		"profileArn": t.ProfileArn, "accountId": user}
	if e, err := time.Parse(time.RFC3339Nano, t.ExpiresAt); err == nil {
		a["expires"] = e.UnixMilli()
	}
	// as readKiroFile tells them apart
	if strings.EqualFold(t.AuthMethod, "IdC") && t.ClientID != "" {
		a["method"] = "idc"
	}
	if t.ClientID != "" {
		a["clientId"], a["clientSecret"] = t.ClientID, t.ClientSecret
	}
	if plan != "" {
		a["plan"] = plan
	}
	return a, nil
}

// setKiroKey sets (or, "", clears) the key saved on the Kiro provider.
func setKiroKey(k string) error {
	f, err := read()
	if err != nil {
		return err
	}
	for i := range f.Providers {
		if f.Providers[i].ID == "kiro" {
			if f.Providers[i].Key == k {
				return nil
			}
			f.Providers[i].Key = k
			return store(f)
		}
	}
	if k == "" {
		return nil
	}
	f.Providers = append(f.Providers, Provider{ID: "kiro", Key: k})
	return store(f)
}

// kiroForgetHome drops what was cached of home's sign-in, rewritten.
func kiroForgetHome(home string) {
	kiroAuthCache.Lock()
	delete(kiroAuthCache.m, "\x00"+home)
	kiroAuthCache.Unlock()
}
