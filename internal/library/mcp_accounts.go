package library

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/yetone/magpie/internal/mcpauth"
)

// A remote server can hold several accounts (Elorynth on X: several OAuth
// sign-ins or Authorization headers, one in use, switched in a click). A
// key is an Account of the server's own, headers laid over the server's
// Headers; an OAuth sign-in is one of mcpauth's, kept with the others. The
// one in use is what every agent is given: a key's headers are written
// into each agent's entry by `live` before the entry is built (through),
// and a sign-in is what the gateway's /mcp/<name> sends, so a switch is a
// change like any other and each agent's file follows it.

const maxAccountName = 60

// Account is one set of headers a server is asked with.
type Account struct {
	ID      string            `json:"id"`
	Name    string            `json:"name"`
	Headers map[string]string `json:"headers,omitempty"`
}

// live is the server as the agents are given it: with the headers of the
// account in use laid over its own, a name either has by any case going
// to the account's.
func (s *Server) live() *Server {
	i := slices.IndexFunc(s.Accounts, func(a Account) bool { return a.ID == s.Account })
	if s.Account == "" || i < 0 {
		return s
	}
	c := *s
	c.Headers = maps.Clone(s.Headers)
	if c.Headers == nil {
		c.Headers = map[string]string{}
	}
	for k, v := range s.Accounts[i].Headers {
		for have := range c.Headers {
			if strings.EqualFold(have, k) {
				delete(c.Headers, have)
			}
		}
		c.Headers[k] = v
	}
	return &c
}

// checkAccounts cleans the accounts: a server by command has none, each
// has a name (not another's, at most maxAccountName characters) and an id,
// and the one in use is one of them.
func (s *Server) checkAccounts() error {
	if !s.Remote() {
		s.Accounts, s.Account = nil, ""
		return nil
	}
	seenName, seenID := map[string]bool{}, map[string]bool{}
	for i := range s.Accounts {
		a := &s.Accounts[i]
		a.Name = strings.TrimSpace(a.Name)
		if a.Name == "" || len([]rune(a.Name)) > maxAccountName {
			return fmt.Errorf("%s: an account's name is from 1 to %d characters", s.Name, maxAccountName)
		}
		if key := strings.ToLower(a.Name); seenName[key] {
			return fmt.Errorf("%s: two accounts are called %s", s.Name, a.Name)
		} else {
			seenName[key] = true
		}
		if a.ID == "" || seenID[a.ID] {
			return fmt.Errorf("%s: an account has no id of its own", s.Name)
		}
		seenID[a.ID] = true
		for k := range a.Headers {
			if strings.TrimSpace(k) == "" {
				delete(a.Headers, k)
			}
		}
	}
	if len(s.Accounts) == 0 {
		s.Accounts = nil
	}
	if s.Account != "" && !seenID[s.Account] {
		s.Account = ""
	}
	return nil
}

// AddServerAccount gives the server another account, a set of headers (a
// key as Authorization), and puts it in use.
func AddServerAccount(name, label string, headers map[string]string) (*Result, error) {
	h := map[string]string{}
	for k, v := range headers {
		if k = strings.TrimSpace(k); k != "" {
			h[k] = v
		}
	}
	if len(h) == 0 {
		return nil, fmt.Errorf("an account needs a header, as Authorization")
	}
	return change(func(l *Library) error {
		s := l.server(name)
		if s == nil {
			return fmt.Errorf("no server called %s", name)
		}
		if !s.Remote() {
			return fmt.Errorf("%s runs as a command: accounts are for a server by URL", name)
		}
		top := 0
		for _, a := range s.Accounts {
			n, _ := strconv.Atoi(a.ID)
			top = max(top, n)
		}
		s.Accounts = append(s.Accounts, Account{ID: strconv.Itoa(top + 1), Name: label, Headers: h})
		s.Account = s.Accounts[len(s.Accounts)-1].ID
		return s.checkAccounts()
	})
}

// UseServerAccount puts an account of the server in use: "" is the
// server's own headers.
func UseServerAccount(name, id string) (*Result, error) {
	return change(func(l *Library) error {
		s := l.server(name)
		if s == nil {
			return fmt.Errorf("no server called %s", name)
		}
		if id != "" && !slices.ContainsFunc(s.Accounts, func(a Account) bool { return a.ID == id }) {
			return fmt.Errorf("%s has no such account", name)
		}
		s.Account = id
		return nil
	})
}

// RemoveServerAccount takes an account away from the server; the one in
// use going puts the server's own headers back in use.
func RemoveServerAccount(name, id string) (*Result, error) {
	return change(func(l *Library) error {
		s := l.server(name)
		if s == nil {
			return fmt.Errorf("no server called %s", name)
		}
		i := slices.IndexFunc(s.Accounts, func(a Account) bool { return a.ID == id })
		if i < 0 {
			return fmt.Errorf("%s has no such account", name)
		}
		s.Accounts = slices.Delete(slices.Clone(s.Accounts), i, i+1)
		return s.checkAccounts()
	})
}

// SignInServerAccount is SignInServer as another account of the server,
// put in use once signed in.
func SignInServerAccount(ctx context.Context, name, label string) (mcpauth.State, error) {
	return signInServer(ctx, name, &label)
}

// UseSignIn puts one of magpie's sign-ins to the server in use.
func UseSignIn(name, id string) error { return mcpauth.Use(name, id) }

// RemoveSignIn forgets one of magpie's sign-ins to the server.
func RemoveSignIn(name, id string) error { return mcpauth.RemoveAccount(name, id) }
