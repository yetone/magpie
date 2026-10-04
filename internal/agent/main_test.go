package agent

import (
	"os"
	"testing"

	"github.com/yetone/magpie/internal/testenv"
)

// TestMain gives the package a home of its own (testenv). A home an agent is
// found through can sit outside HOME — DSH_HOME does — and the package's
// tests sandbox HOME alone: left as the developer has it, a test that picks
// a model writes it into the real ~/.dsh/profiles, and dsh then refuses
// every turn with a model its provider does not list. internal/gateway,
// internal/provider and internal/usage isolate themselves the same way.
func TestMain(m *testing.M) {
	// whether Codex's ChatGPT account is out of its allowance is asked of
	// OpenAI; never from here
	codexUsedUp = func() bool { return false }
	// Sandboxed config writes must never write into the real OS keychain.
	zedCredential = func(string) error { return nil }
	// Pi's and omp's model registries are read from their installs; never
	// this machine's
	nodeModulesOf = func(string, []string) []string { return nil }
	os.Exit(testenv.Run(m))
}
