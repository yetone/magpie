package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// The Agents page shows what disconnecting Claude Code would change by
// running Disconnect on a copy, in a process whose HOME is a temporary
// folder (DisconnectPreview). Claude Code's Disconnect builds magpie's
// providers there, and with them the accounts: the Claude sign-in read
// from the keychain, its account asked of Claude Code. On a Mac the
// keychain is found through HOME, and the copy's home has no login
// keychain: macOS asks "Keychain Not Found — A keychain cannot be found
// to store …" (Cancel / Reset To Defaults). jasonz3g on X, with two Claude
// accounts in magpie, cancelled it dozens of times after clicking Claude
// Code. The dry run reads no sign-ins: the files it changes are the
// agent's, and no account changes them.
func TestDisconnectPreviewReadsNoSignIns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no keychain")
	}
	home, _ := codexHome(t, "", "")
	ran := filepath.Join(t.TempDir(), "ran.log")
	bin := t.TempDir()
	for _, name := range []string{"security", "claude"} {
		testenv.Program(t, filepath.Join(bin, name), "#!/bin/sh\necho \""+name+" $*\" >> '"+ran+"'\nexit 44\n")
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	writeFile(t, filepath.Join(home, ".claude.json"), `{"oauthAccount":{"emailAddress":"jason@example.com"}}`)
	auth := func(tok string) string {
		return `{"claudeAiOauth":{"accessToken":"` + tok + `","refreshToken":"r-` + tok + `","expiresAt":4102444800000,"subscriptionType":"max"}}`
	}
	writeFile(t, filepath.Join(filepath.Dir(provider.Path()), "logins.json"), `[
 {"agent":"claude","user":"jason@example.com","plan":"max","seen":"2026-11-15T00:00:00Z","auth":`+auth("a1")+`,"profile":{"emailAddress":"jason@example.com"},"held":true},
 {"agent":"claude","user":"jason2@example.com","plan":"max","seen":"2026-11-15T00:00:00Z","on":true,"auth":`+auth("a2")+`,"profile":{"emailAddress":"jason2@example.com"}}
]`)
	accounts := func() string {
		os.Remove(ran)
		provider.ForgetAccounts()
		provider.Accounts()
		b, _ := os.ReadFile(ran)
		return strings.TrimSpace(string(b))
	}

	// what the dry run's process has that this one hasn't, home and its
	// folders aside: those would take this test's files away. It goes
	// first: the Claude sign-in read is kept for seconds, so a read out of
	// the dry run before it would stand in for one in it.
	moved := map[string]bool{"HOME": true, "USERPROFILE": true, "TMPDIR": true, testenv.Marker: true}
	var set []string
	for _, kv := range previewEnv(home, t.TempDir()) {
		k, v, _ := strings.Cut(kv, "=")
		if !moved[k] && !strings.HasPrefix(k, "XDG_") && os.Getenv(k) != v {
			t.Setenv(k, v)
			set = append(set, k)
		}
	}
	if got := accounts(); got != "" {
		t.Fatalf("the dry run read the sign-ins, under its temporary home:\n%s", got)
	}

	// the same accounts read out of the dry run reach the keychain on a
	// Mac: the fixture is one that asks it
	for _, k := range set {
		os.Unsetenv(k)
	}
	if got := accounts(); runtime.GOOS == "darwin" && !strings.Contains(got, "security find-generic-password") {
		t.Fatalf("the accounts were read without the keychain: %q", got)
	}
}
