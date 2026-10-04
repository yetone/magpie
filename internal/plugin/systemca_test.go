package plugin

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// Bun checks TLS against its own roots: behind a proxy that re-signs HTTPS
// with a root the user installed, `bun add` failed with
// UNABLE_TO_VERIFY_LEAF_SIGNATURE (chenyyi001 on X, installing
// @magpie-community/opencode-kiro-auth). Every bun magpie runs is given
// the system's roots, unless the user names a file of their own.
func TestBunIsGivenTheSystemsRoots(t *testing.T) {
	old := systemCAFile
	t.Cleanup(func() { systemCAFile = old })
	systemCAFile = func() string { return "/cache/bun/system-ca-abc.pem" }

	cmd := bunCommand(context.Background(), "bun", t.TempDir(), "add", "x")
	if !slices.Contains(cmd.Env, "NODE_EXTRA_CA_CERTS=/cache/bun/system-ca-abc.pem") {
		t.Fatalf("bun add isn't given the system's roots: %v", caVars(cmd.Env))
	}

	t.Setenv("NODE_EXTRA_CA_CERTS", "/mine.pem")
	cmd = bunCommand(context.Background(), "bun", t.TempDir(), "add", "x")
	if got := caVars(cmd.Env); !slices.Equal(got, []string{"NODE_EXTRA_CA_CERTS=/mine.pem"}) {
		t.Fatalf("the user's own NODE_EXTRA_CA_CERTS = %v, want it alone", got)
	}

	systemCAFile = func() string { return "" }
	if got := caEnv(nil); got != nil {
		t.Fatalf("with nothing to add: %v", got)
	}
}

func caVars(env []string) []string {
	var out []string
	for _, kv := range env {
		if strings.HasPrefix(strings.ToUpper(kv), "NODE_EXTRA_CA_CERTS=") {
			out = append(out, kv)
		}
	}
	return out
}
