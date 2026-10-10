package provider

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/testenv"
)

// A sign-in magpie wrote on several lines, read through the cache from the
// keychain as hex, is written again on one line without the read hanging:
// the write used to cache it under the lock the read held, and every later
// read of Claude Code's sign-in waited on it for good.
func TestClaudeKeychainHexIsWrittenAgainOnRead(t *testing.T) {
	shellFakes(t)
	home := claudeHome(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("USER", "tester")
	c := claudeCredentials{OAuth: claudeAuth{AccessToken: "live", SubscriptionType: "max"}}
	indented, _ := c.marshal()
	bin := filepath.Join(home, "bin")
	os.MkdirAll(bin, 0o755)
	item := filepath.Join(bin, "item")
	if err := os.WriteFile(item, []byte(hex.EncodeToString(indented)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	testenv.Program(t, filepath.Join(bin, "security"), `#!/bin/sh
case "$1" in
find-generic-password) cat '`+item+`' ;;
add-generic-password)
	while [ $# -gt 0 ]; do
		if [ "$1" = -w ]; then printf '%s\n' "$2" > '`+item+`'; fi
		shift
	done ;;
*) exit 1 ;;
esac
`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	claudeKeychain = true
	forgetClaudeCredential()

	read := func() (claudeCredentials, claudeCredentialLocation, bool) {
		type result struct {
			c   claudeCredentials
			loc claudeCredentialLocation
			ok  bool
		}
		done := make(chan result, 1)
		go func() {
			c, loc, ok := claudeCredential()
			done <- result{c, loc, ok}
		}()
		select {
		case r := <-done:
			return r.c, r.loc, r.ok
		case <-time.After(10 * time.Second):
			// the hung read holds claudeCacheMu, which isolate's cleanup
			// takes: t.Fatal, or a panic here, would sit out -timeout
			// running it. A panic off the test's goroutine ends the binary
			// now, with every goroutine's stack
			debug.SetTraceback("all")
			go func() { panic("reading Claude Code's sign-in hung") }()
			select {}
		}
	}
	got, loc, ok := read()
	if !ok || got.OAuth.AccessToken != "live" || !loc.keychain || loc.account != "tester" {
		t.Fatalf("read %v %+v %+v", ok, got.OAuth, loc)
	}
	b, _ := os.ReadFile(item)
	if line := strings.TrimSpace(string(b)); strings.Contains(line, "\n") || !strings.HasPrefix(line, "{") {
		t.Fatalf("not written again on one line: %q", b)
	}
	// the cache is served, and read again once forgotten
	if got, _, ok := read(); !ok || got.OAuth.AccessToken != "live" {
		t.Fatalf("cached read %v %+v", ok, got.OAuth)
	}
	forgetClaudeCredential()
	if got, _, ok := read(); !ok || got.OAuth.AccessToken != "live" {
		t.Fatalf("read again %v %+v", ok, got.OAuth)
	}
}
