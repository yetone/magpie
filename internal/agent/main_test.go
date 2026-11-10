package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/testenv"
)

// TestMain gives the package a home of its own (testenv). A home an agent is
// found through can sit outside HOME — DSH_HOME does — and the package's
// tests sandbox HOME alone: left as the developer has it, a test that picks
// a model writes it into the real ~/.dsh/profiles, and dsh then refuses
// every turn with a model its provider does not list. internal/gateway,
// internal/provider and internal/usage isolate themselves the same way.
func TestMain(m *testing.M) {
	if len(os.Args) == 3 && os.Args[1] == DryRunArg {
		if err := DryRun(os.Args[2]); err != nil {
			os.Stderr.WriteString(err.Error())
			os.Exit(1)
		}
		os.Exit(0)
	}
	// whether Codex's ChatGPT account is out of its allowance is asked of
	// OpenAI; never from here
	codexUsedUp = func() bool { return false }
	// a thread a Codex holds open is looked at again later, on its own
	// goroutine, which would outlive its test; the tests about it set
	// their own wait
	codexThreadsAgain = func() {}
	codexThreadsFor.Store(0)
	// Sandboxed config writes must never write into the real OS keychain.
	zedCredential = func(string) error { return nil }
	// Pi's and omp's model registries are read from their installs; never
	// this machine's
	nodeModulesOf = func(string, []string) []string { return nil }
	// Microsoft Store apps are read from the current user's AppX registry;
	// never this machine's (a Windows box with the Codex app installed)
	windowsStorePackages = func() []string { return nil }
	// nor what an installer registered there (DeepSeek Harness Desktop's
	// folder, #2279)
	windowsRegisteredApp = func(string) (string, string) { return "", "" }
	// nor where this Mac's Spotlight has an app (DeepSeek Harness.app
	// outside /Applications, #2325)
	spotlightApps = func(string) []string { return nil }
	// whether OpenMausBot is open is never this machine's: one open there
	// refused every test that turns magpie on for each agent; the tests
	// about it say their own
	ombRunning = func() bool { return false }
	// nor which OpenCode this machine has: an OpenCode 2 on PATH turned
	// every OpenCode test's file into OpenCode 2's words; the tests about
	// it say their own (opencode_v2_test.go)
	openCodeVersion = func() string { return "" }
	// a desktop app's Restart never quits or opens one of this machine's
	// (the Codex app, Claude Desktop, WorkBuddy the developer runs); the
	// restart tests stand in for each step
	appQuit = func(context.Context, string) error { return errors.New("no app is quit in a test") }
	appOpen = func(context.Context, string) error { return errors.New("no app is opened in a test") }
	winProcs = func(string) []winProc { return nil }
	winClose = func([]int) error { return errors.New("no app is closed in a test") }
	winEnd = func(int) error { return errors.New("no app is ended in a test") }
	winLaunch = func(winProc) error { return errors.New("no app is opened in a test") }
	// Aside's default is changed through the aside binary, which answers for
	// the account the developer is actually running: a test that picks a
	// model would change the real Aside's default, and the running one would
	// keep the old one, so the test would read back a file nothing had
	// applied. The Aside tests stand in for it themselves.
	asideSet = func(string, string) error { return errors.New("aside: no Aside in a test") }
	asideRead = func(string) (map[string]json.RawMessage, error) { return nil, errors.New("aside: no Aside in a test") }
	// an address an agent is pointed at is tried over the network (Drift,
	// #1013); never from a test, whose reach tests stand in for it
	reachProbe = func(string) bool { return true }
	// nor is the way Codex goes there read from this machine's proxy
	// settings, or a proxy tried; the proxy tests say both
	codexProxyEnv = func(string) string { return "" }
	// nor Claude Code's provider switches read from the login shell; the
	// cloud tests say what one there does
	claudeShellEnv = func(string) string { return "" }
	codexSystemProxy = func() netproxy.Proxy { return netproxy.Proxy{} }
	proxyProbe = func(string, string) bool { return true }
	// nor an address of the agent's own asked whether magpie answers
	// there (a reverse proxy, #1985): known, and not magpie, as for a
	// relay; the proxy-kept tests answer it
	magpieAt = func(string) (bool, bool) { return false, true }
	os.Exit(testenv.Run(m))
}
