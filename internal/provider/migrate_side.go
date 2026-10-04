package provider

// Movers for the subscriptions whose agent keeps an account of its own
// (Devin, Grok, Command Code, Cursor): the plugin takes that one the way
// its own "CLI's sign-in" method does, reading or copying what the CLI
// keeps, and the built-in keeps reading it after a move back; magpie's
// further accounts go over as the plugin keeps a sign-in.

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

func init() {
	movers["devin"] = &mover{
		pkg:    "@magpie-community/opencode-devin-auth",
		min:    "0.1.7", // an account named as Devin names its key, not the CLI's own (plugins#17)
		agents: []string{"devin"},
		// a variant picked before the families were one model (swe-2-high)
		// goes to Devin as it is, through the plugin too, which keeps the
		// picks it is given; Adaptive and Fusion the built-in never served
		served: func(model string, listed []string) bool {
			if id := strings.ToLower(model); id == "adaptive" || id == "fusion" {
				return true
			}
			base := devinBase(model)
			return base != model && slices.Contains(listed, base)
		},
		out: func() ([]Moving, error) {
			var out []Moving
			for _, l := range devinLogins() {
				key, server, err := DevinAuthAt(l.Home)
				if err != nil {
					continue
				}
				// the CLI's own account has its plan from the CLI, as the
				// built-in shows it, not saved on its row
				plan := l.Plan
				if l.Home == "" {
					if _, p, ok := devinIdentity(); ok && p != "" {
						plan = p
					}
				}
				md := map[string]any{"email": l.User}
				if plan != "" {
					md["plan"] = plan
				}
				if s := strings.TrimRight(server, "/"); s != "" && s != devinServer {
					md["server"] = s
				}
				if l.Home == "" {
					md["cli"] = true // the plugin reads the CLI's key again, as the built-in does
				}
				out = append(out, Moving{User: l.User, First: l.Active, On: l.On, Lapsed: l.Lapsed != "", Plan: plan, Own: l.Home == "",
					Auth: map[string]any{"type": "api", "key": key, "metadata": md}})
			}
			return out, nil
		},
		back: func(ls []savedLogin, user string, auth map[string]any) ([]savedLogin, string, error) {
			key := str(auth["key"])
			md, _ := auth["metadata"].(map[string]any)
			if key == "" {
				return ls, "", errors.New("Devin: an unreadable plugin sign-in")
			}
			if own, _, err := DevinAuthAt(""); err == nil && (own == key || md["cli"] == true) {
				return ls, ownUser(ls, "devin", str(md["email"])), nil
			}
			if user == "" {
				user = str(md["email"])
			}
			if user == "" {
				return ls, "", errors.New("Devin: a plugin sign-in with no account")
			}
			i := backInto(&ls, "devin", user)
			home := ls[i].Home
			if home == "" {
				// the home the account had before the move, which it still has
				home = devinHomeWith(key)
			}
			if k, _, err := DevinAuthAt(home); home == "" || err != nil || k != key {
				if home == "" {
					h, err := newDevinHome()
					if err != nil {
						return ls, "", err
					}
					home = h
				}
				if err := os.MkdirAll(filepath.Dir(devinCredentialsAt(home)), 0o700); err != nil {
					return ls, "", err
				}
				if err := writePrivate(devinCredentialsAt(home), devinCredentials(key, str(md["server"]), "", "")); err != nil {
					return ls, "", err
				}
			}
			ls[i].Home, ls[i].Lapsed, ls[i].Seen = home, "", time.Now().UTC().Truncate(time.Second)
			if p := str(md["plan"]); p != "" {
				ls[i].Plan = p
			}
			return ls, ls[i].User, nil
		},
	}

	// Grok's plugin reads the CLI's auth.json in the home its sign-in
	// names, as the built-in does: the homes stay where they are.
	movers["grok"] = &mover{
		pkg:    "@magpie-community/opencode-grok-auth",
		min:    "0.1.6", // a failure's status and its sign-in mark as the built-in's; grok-4.7's reasoning levels; a token Grok refuses early reads as expired; the sign-in renewed ahead of time through auth.refresh
		agents: []string{"grok"},
		out: func() ([]Moving, error) {
			var out []Moving
			for _, l := range grokLogins() {
				c, ok := readGrokCredential(l.Home)
				if !ok {
					continue
				}
				var exp int64
				if !c.ExpiresAt.IsZero() {
					exp = c.ExpiresAt.UnixMilli()
				}
				out = append(out, Moving{User: l.User, First: l.Active, On: l.On, Lapsed: l.Lapsed != "", Plan: l.Plan, Own: samePath(l.Home, GrokHome()),
					Auth: map[string]any{"type": "oauth", "refresh": l.Home, "access": c.Key, "expires": exp, "accountId": firstNonEmpty(c.Email, l.User)}})
			}
			return out, nil
		},
		back: func(ls []savedLogin, user string, auth map[string]any) ([]savedLogin, string, error) {
			home := str(auth["refresh"])
			if home == "" {
				return ls, "", errors.New("Grok: an unreadable plugin sign-in")
			}
			if samePath(home, GrokHome()) {
				return ls, ownUser(ls, "grok", str(auth["accountId"])), nil
			}
			if c, ok := readGrokCredential(home); ok && c.Email != "" {
				user = c.Email
			} else if user == "" {
				user = str(auth["accountId"])
			}
			if user == "" {
				return ls, "", errors.New("Grok: a plugin sign-in with no account")
			}
			i := backInto(&ls, "grok", user)
			ls[i].Home, ls[i].Lapsed, ls[i].Seen = home, "", time.Now().UTC().Truncate(time.Second)
			return ls, ls[i].User, nil
		},
	}

	movers[CommandCodePlanID] = &mover{
		pkg:    "@magpie-community/opencode-commandcode-auth",
		min:    "0.1.8", // a failure's status and its sign-in mark as the built-in's; a Go account lists Go's models; a model the plugin doesn't know takes pictures as models.dev says
		agents: []string{CommandCodePlanID},
		out: func() ([]Moving, error) {
			var out []Moving
			for _, l := range cmdLogins() {
				md := map[string]any{"email": l.User}
				if l.auth.UserID != "" {
					md["userId"] = l.auth.UserID
				}
				if l.auth.KeyName != "" {
					md["keyName"] = l.auth.KeyName
				}
				if l.Plan != "" {
					md["plan"] = l.Plan
				}
				if l.Own {
					md["cli"] = true // the plugin reads the CLI's key again, as the built-in does
				}
				out = append(out, Moving{User: l.User, First: l.Active, On: l.On, Lapsed: l.Lapsed != "", Plan: l.Plan, Own: l.Own,
					Auth: map[string]any{"type": "api", "key": l.auth.APIKey, "metadata": md}})
			}
			return out, nil
		},
		back: func(ls []savedLogin, user string, auth map[string]any) ([]savedLogin, string, error) {
			key := str(auth["key"])
			md, _ := auth["metadata"].(map[string]any)
			if key == "" {
				return ls, "", errors.New("Command Code: an unreadable plugin sign-in")
			}
			if _, own, ok := cmdOwn(); ok && (own.APIKey == key || md["cli"] == true) {
				return ls, ownUser(ls, CommandCodePlanID, str(md["email"])), nil
			}
			i := -1
			// the same key signed in under another name is the same account
			for j, l := range ls {
				if a, ok := cmdSaved(l); ok && l.Agent == CommandCodePlanID && a.APIKey == key {
					i = j
				}
			}
			if i < 0 {
				if user == "" {
					user = str(md["email"])
				}
				if user == "" {
					return ls, "", errors.New("Command Code: a plugin sign-in with no account")
				}
				i = backInto(&ls, CommandCodePlanID, user)
			}
			a, _ := cmdSaved(ls[i])
			a.APIKey = key
			if v := str(md["userId"]); v != "" {
				a.UserID = v
			}
			if v := str(md["keyName"]); v != "" {
				a.KeyName = v
			}
			if a.UserName == "" && a.UserID != ls[i].User {
				a.UserName = ls[i].User
			}
			ls[i].Auth, ls[i].Lapsed, ls[i].Seen = []byte(jsonText(a)), "", time.Now().UTC().Truncate(time.Second)
			if p := str(md["plan"]); p != "" {
				ls[i].Plan = p
			}
			return ls, ls[i].User, nil
		},
	}

	// Cursor's one account is cursor-agent's own, which magpie never keeps:
	// the plugin reads cursor-agent's token as the built-in does.
	movers["cursor"] = &mover{
		pkg:    "@magpie-community/opencode-cursor-auth",
		min:    "0.1.9", // a failure's status and its sign-in mark as the built-in's; Max Mode models retried in Max Mode; glm-5.3 listed; catalog entries written as CallDynamicTool calls, an empty turn retried (plugins #11); a tool step's usage from the dashboard's usage event, cache included (#676)
		agents: []string{"cursor"},
		out: func() ([]Moving, error) {
			if CursorExecutable() == "" || cursorSignedOut() {
				return nil, nil
			}
			user, plan, ok := cursorIdentity()
			if !ok || user == "" {
				return nil, nil
			}
			a := map[string]any{"type": "oauth", "access": "", "refresh": cursorCLIMark, "expires": 0, "accountId": user}
			if plan != "" {
				a["plan"] = plan
			}
			return []Moving{{User: user, First: true, On: true, Own: true, Auth: a}}, nil
		},
		back: func(ls []savedLogin, user string, auth map[string]any) ([]savedLogin, string, error) {
			if str(auth["refresh"]) == cursorCLIMark {
				return ls, firstNonEmpty(user, str(auth["accountId"])), nil
			}
			// the built-in has nowhere to keep a token of its own
			// signed in in the browser through the plugin: the built-in
			// Cursor has nowhere to keep it
			return ls, "", errStays
		},
	}
}

const (
	devinServer   = "https://server.codeium.com"
	cursorCLIMark = "cursor-agent" // the Cursor plugin's sign-in that reads cursor-agent's token
)

// ownUser is the account the agent's own sign-in is saved as, else user.
func ownUser(ls []savedLogin, agent, user string) string {
	for _, l := range ls {
		if l.Agent == agent && l.own() && l.User != "" {
			return l.User
		}
	}
	return user
}

func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// devinHomeWith is the home of magpie's signed in with key, "" for none.
func devinHomeWith(key string) string {
	es, _ := os.ReadDir(devinAccountsDir())
	for _, e := range es {
		home := filepath.Join(devinAccountsDir(), e.Name())
		if k, _, err := DevinAuthAt(home); e.IsDir() && err == nil && k == key {
			return home
		}
	}
	return ""
}
