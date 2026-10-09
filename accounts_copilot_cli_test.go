package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// magpie accounts switch|forget copilot tells one login on two hosts apart
// (#1220): by <login>@<host> or --host, the login alone only when it names
// one account.
func TestAccountsCopilotHosts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	t.Setenv("COPILOT_HOME", filepath.Join(home, ".copilot"))
	logins := filepath.Join(filepath.Dir(provider.Path()), "logins.json")
	if err := os.MkdirAll(filepath.Dir(logins), 0o700); err != nil {
		t.Fatal(err)
	}
	// a.ghe.com's as magpie saved it before #1220, b.ghe.com's as after
	if err := os.WriteFile(logins, []byte(`[
  {"agent": "copilot", "user": "mona", "seen": "2026-10-05T10:00:00Z", "on": true, "auth": {"host": "a.ghe.com", "oauth_token": "gho_a"}},
  {"agent": "copilot", "user": "mona@b.ghe.com", "seen": "2026-10-06T10:00:00Z", "on": true, "auth": {"host": "b.ghe.com", "oauth_token": "gho_b"}},
  {"agent": "copilot", "user": "hubot", "seen": "2026-10-06T10:00:00Z", "on": true, "auth": {"oauth_token": "gho_h"}}
]`), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		t.Helper()
		return said(t, func() error { return accountsCmd(append([]string{"accounts"}, args...)) })
	}
	first := func() string {
		t.Helper()
		for _, l := range provider.Logins("copilot") {
			if l.Active {
				return l.User
			}
		}
		return ""
	}
	if _, err := run("switch", "copilot", "mona"); err == nil || !strings.Contains(err.Error(), "mona@a.ghe.com") || !strings.Contains(err.Error(), "--host") {
		t.Fatalf("an ambiguous login was taken: %v", err)
	}
	if _, err := run("switch", "copilot", "mona@b.ghe.com"); err != nil || first() != "mona@b.ghe.com" {
		t.Fatalf("switch mona@b.ghe.com: %v, first %s", err, first())
	}
	if out, err := run("switch", "copilot", "MONA", "--host", "https://a.ghe.com/"); err != nil || first() != "mona@a.ghe.com" || !strings.Contains(out, "magpie now uses mona@a.ghe.com") {
		t.Fatalf("switch --host: %v %q, first %s", err, out, first())
	}
	if _, err := run("switch", "copilot", "hubot", "--host", "evil.example.com"); err == nil {
		t.Fatal("--host took a host that isn't GitHub's")
	}
	if _, err := run("switch", "claude", "a@example.com", "--host", "a.ghe.com"); err == nil {
		t.Fatal("--host taken for Claude Code")
	}
	if _, err := run("forget", "copilot", "mona", "--host=a.ghe.com"); err != nil {
		t.Fatal(err)
	}
	// the login alone now names its one account
	if _, err := run("switch", "copilot", "mona"); err != nil || first() != "mona@b.ghe.com" {
		t.Fatalf("switch mona: %v, first %s", err, first())
	}
	var saved []struct {
		User string            `json:"user"`
		Auth map[string]string `json:"auth"`
	}
	b, _ := os.ReadFile(logins)
	if err := json.Unmarshal(b, &saved); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, l := range saved {
		got = append(got, l.User+"="+l.Auth["oauth_token"])
	}
	if strings.Join(got, " ") != "hubot=gho_h mona@b.ghe.com=gho_b" {
		t.Fatalf("logins.json: %v", got)
	}
}
