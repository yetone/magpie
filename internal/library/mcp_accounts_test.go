package library

import (
	"path/filepath"
	"strings"
	"testing"
)

// A server holds several Authorization headers, one in use (Elorynth on
// X): switching rewrites what every agent is given, adding and removing
// keep the server's own headers, and the account stays through a save
// from the editor, which doesn't send it.
func TestServerAccountsSwitchEveryAgent(t *testing.T) {
	h := sandbox(t)
	codex := filepath.Join(h, ".codex/config.toml")
	srv := Server{Name: "web", Transport: "http", URL: "https://x.example/mcp",
		Headers: map[string]string{"Authorization": "Bearer base-key", "X-Team": "a"}, Agents: []string{"codex", "claude"}}
	ok(t)(SaveServer("", srv))
	if !strings.Contains(read(t, codex), "base-key") {
		t.Fatalf("the server's own key isn't written:\n%s", read(t, codex))
	}

	ok(t)(AddServerAccount("web", "work", map[string]string{"authorization": "Bearer work-key"}))
	cfg := read(t, codex)
	if !strings.Contains(cfg, "work-key") || strings.Contains(cfg, "base-key") {
		t.Fatalf("the new account isn't the one in use:\n%s", cfg)
	}
	if !strings.Contains(cfg, "X-Team") {
		t.Errorf("a header the account doesn't set went:\n%s", cfg)
	}
	_, err := AddServerAccount("web", "Work", map[string]string{"Authorization": "x"})
	if err == nil {
		t.Errorf("two accounts called alike")
	}

	// an editor's save doesn't send the accounts
	got := lib(t).server("web")
	save := *got
	save.Accounts, save.Account = nil, ""
	ok(t)(SaveServer("web", save))
	if s := lib(t).server("web"); len(s.Accounts) != 1 || s.Account != s.Accounts[0].ID {
		t.Fatalf("a save lost the accounts: %+v", s)
	}

	ok(t)(UseServerAccount("web", ""))
	if cfg := read(t, codex); !strings.Contains(cfg, "base-key") || strings.Contains(cfg, "work-key") {
		t.Fatalf("back to the server's own headers:\n%s", cfg)
	}
	_, err = UseServerAccount("web", "nope")
	if err == nil {
		t.Errorf("an account that isn't there was put in use")
	}
	id := lib(t).server("web").Accounts[0].ID
	ok(t)(UseServerAccount("web", id))
	ok(t)(RemoveServerAccount("web", id))
	if s := lib(t).server("web"); len(s.Accounts) != 0 || s.Account != "" {
		t.Errorf("not removed: %+v", s)
	}
	if cfg := read(t, codex); !strings.Contains(cfg, "base-key") || strings.Contains(cfg, "work-key") {
		t.Errorf("the account in use removed, the agents keep its key:\n%s", cfg)
	}
}

func TestServerAccountsNotForACommand(t *testing.T) {
	sandbox(t)
	ok(t)(SaveServer("", Server{Name: "loc", Transport: "stdio", Command: "x", Agents: []string{"codex"}}))
	if _, err := AddServerAccount("loc", "a", map[string]string{"Authorization": "x"}); err == nil {
		t.Errorf("a command was given an account")
	}
	if _, err := AddServerAccount("loc", "a", nil); err == nil {
		t.Errorf("an account with no header")
	}
}

// A backup or a sync carries no account's key, and brings it back from
// the copy here.
func TestServerAccountKeysAreSecrets(t *testing.T) {
	secret := func(k string) bool { return strings.EqualFold(k, "Authorization") }
	s := Server{Name: "web", Transport: "http", URL: "https://x.example/mcp",
		Accounts: []Account{{ID: "1", Name: "w", Headers: map[string]string{"Authorization": "Bearer sk-secret-123456"}}}}
	b := &Bundle{MCP: []*Server{&s}}
	b.WithoutSecrets(secret)
	if strings.Contains(mustJSON(t, b), "sk-secret-123456") {
		t.Errorf("an account's key is carried: %s", mustJSON(t, b))
	}
	back := b.WithSecrets(&Bundle{MCP: []*Server{&s}}, secret)
	if back.MCP[0].Accounts[0].Headers["Authorization"] != "Bearer sk-secret-123456" {
		t.Errorf("not brought back: %s", mustJSON(t, back))
	}
}

func lib(t *testing.T) *Library {
	t.Helper()
	l, err := load()
	if err != nil {
		t.Fatal(err)
	}
	return l
}
