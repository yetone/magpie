package plugin

import (
	"context"
	"runtime"
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
	// one the environment sets already is the user's and wins (below);
	// the ambient one a shell may have isn't this test's
	t.Setenv("NODE_EXTRA_CA_CERTS", "")

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
	if got := caEnv(nil, ""); got != nil {
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

// Bun on Windows can't open a NODE_EXTRA_CA_CERTS path that isn't ASCII
// (#1232: a Chinese user name), so there it is given the way to the file
// from where it runs, which is under the same user folder.
func TestBunIsGivenAnASCIIWayToTheRoots(t *testing.T) {
	user := `C:\Users\张三`
	ca := user + `\.cache\magpie\bun\system-ca-abc.pem`
	for _, c := range []struct{ name, p, dir, goos, want string }{
		{"under a Chinese user name", ca, user + `\.config\magpie\plugins`, "windows", `..\..\..\.cache\magpie\bun\system-ca-abc.pem`},
		{"an ASCII path as it is", `C:\Users\bob\.cache\magpie\bun\system-ca-abc.pem`, `C:\Users\bob\x`, "windows", `C:\Users\bob\.cache\magpie\bun\system-ca-abc.pem`},
		{"another drive, no way there", ca, `D:\plugins`, "windows", ca},
		{"no way there in ASCII", ca, `C:\其他\plugins`, "windows", ca},
		{"Bun elsewhere reads it whole", "/home/张三/.cache/magpie/bun/system-ca-abc.pem", "/home/张三/.config/magpie/plugins", "linux", "/home/张三/.cache/magpie/bun/system-ca-abc.pem"},
	} {
		if c.goos == "windows" && runtime.GOOS != "windows" {
			// filepath.Rel works on this OS's paths; Windows' are tried there
			continue
		}
		if got := bunReadable(c.p, c.dir, c.goos); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	if !ascii(`..\..\x.pem`) || ascii(`C:\Users\张三`) {
		t.Fatal("ascii")
	}
}
