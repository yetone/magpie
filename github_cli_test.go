package main

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/davsync"
)

func TestGitHubCmd(t *testing.T) {
	webdavHome(t)
	if err := githubCmd([]string{"set", "branch=main"}); err == nil || !strings.Contains(err.Error(), "is off") {
		t.Fatalf("changing GitHub sync while off: %v", err)
	}
	if err := githubCmd([]string{"on", "https://github.com/owner/repo"}); err == nil || !strings.Contains(err.Error(), "not a GitHub address") {
		t.Fatalf("a wrong scheme should fail before asking for secrets: %v", err)
	}
	if err := githubCmd([]string{"on", "github://owner/repo", "token=fixture-secret"}); err == nil || !strings.Contains(err.Error(), "asked for") {
		t.Fatalf("a token should never enter shell history: %v", err)
	}
	c := davsync.Config{URL: "github://owner/repo/folder", Branch: "sync/settings", Password: "fixture-sync-token",
		Passphrase: "fixture-sync-passphrase", Keys: true, Agents: true}
	if err := davsync.Configure(c); err != nil {
		t.Fatal(err)
	}
	for _, k := range []syncKind{webdavKind, s3Kind} {
		for _, args := range [][]string{{"off"}, {"set", "keys=no"}} {
			if err := syncCmd(k, args); err == nil || !strings.Contains(err.Error(), "magpie github") {
				t.Fatalf("%s %v changed GitHub sync: %v", k.cmd, args, err)
			}
		}
	}
	if got, ok := davsync.Load(); !ok || got.URL != c.URL || !got.Keys {
		t.Fatal("another sync command altered the active GitHub setup")
	}
	if err := githubCmd(nil); err != nil {
		t.Fatal(err)
	}
	if err := githubCmd([]string{"auto", "15"}); err != nil || davsync.Status().Auto != 15 {
		t.Fatalf("GitHub auto interval: %v", err)
	}
	if err := githubCmd([]string{"off"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := davsync.Load(); ok {
		t.Fatal("GitHub off left sync configured")
	}
}
