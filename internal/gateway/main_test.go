package gateway

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/agentenv"
	"github.com/yetone/magpie/internal/testenv"
)

// standInsEnv names the folder of a test binary's stand-in CLIs, for a
// test binary it starts again.
const standInsEnv = "MAGPIE_GATEWAY_TEST_STAND_INS"

func TestMain(m *testing.M) {
	code, err := isolatedTests(m)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		code = 1
	}
	os.Exit(code)
}

// Isolate every process, including the cold-start redaction tests. HOME
// alone does not isolate the macOS Keychain, and account caches eventually
// expire: discovery must keep finding only the test's sign-ins and CLIs.
func isolatedTests(m *testing.M) (int, error) {
	home, err := os.MkdirTemp("", "magpie-gateway-test-")
	if err != nil {
		return 1, err
	}
	defer os.RemoveAll(home)
	for name, value := range map[string]string{
		"HOME": home, "USERPROFILE": home,
		"XDG_CONFIG_HOME": filepath.Join(home, ".config"),
		"XDG_CACHE_HOME":  filepath.Join(home, ".cache"),
		"XDG_DATA_HOME":   filepath.Join(home, ".local", "share"),
		"APPDATA":         filepath.Join(home, "AppData", "Roaming"),
		"LOCALAPPDATA":    filepath.Join(home, "AppData", "Local"),
	} {
		if err := os.Setenv(name, value); err != nil {
			return 1, err
		}
	}
	for _, name := range agentenv.Vars {
		if err := os.Unsetenv(name); err != nil {
			return 1, err
		}
	}
	// Finding an inert CLI also prevents discovery from falling back to
	// one installed at an absolute system path. Tests can prepend their
	// own fakes, as fakeClaude does, and still use ordinary shell tools.
	// A test binary this one starts again (the cold-start redaction tests)
	// runs the stand-ins its parent made: on macOS a newly written program
	// waits for the system's first-run check, and under a full suite's load
	// a child's account discovery spent its whole 20s on new stand-ins.
	bin := os.Getenv(standInsEnv)
	if bin == "" {
		bin = filepath.Join(home, "bin")
		if err := os.Mkdir(bin, 0o755); err != nil {
			return 1, err
		}
		if err := testenv.StandIns(bin, []string{"security", "secret-tool", "claude", "codex", "cursor-agent", "devin", "grok", "kiro-cli"}); err != nil {
			return 1, err
		}
		defer testenv.RemovePrograms()
		if err := os.Setenv(standInsEnv, bin); err != nil {
			return 1, err
		}
	}
	if err := os.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH")); err != nil {
		return 1, err
	}
	// no route is written to disk behind a test's back; the history's own
	// tests call saveRoute themselves
	keepRoutes = false
	// no test asks a vendor: a provider with a real base URL and a made-up
	// key would, in the background (a plan key's windows, #1016), and a
	// plugin host's first start fetched models.dev
	testenv.Offline()
	return m.Run(), nil
}
