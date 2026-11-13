package sessions

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// The config.toml Daimon writes for its Kimi Code runtime, as Kimi 3.2.27's
// modelToToml builds it: the default alias on Kimi's own endpoint, and one
// whose provider magpie's kimi-desktop plugin pointed at its bridge.
const kimiWorkConfig = `default_model = "daimon-kimi-code"

[providers.daimon-kimi-code]
type = "kimi"
api_key = "sk-redacted"
base_url = "https://api.kimi.com/coding/v1"

[providers.bridged]
type = "kimi"
api_key = "sk-redacted"
base_url = "http://127.0.0.1:47213/v1"

[models.daimon-kimi-code]
provider = "daimon-kimi-code"
model = "k2p6-agent"
max_context_size = 262144

[models.bridged]
provider = "bridged"
model = "k2p6-agent"
max_context_size = 262144
`

func writeKimiWork(t *testing.T, share string, lines ...string) {
	t.Helper()
	root := kimiWorkRuntime(share)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config.toml"), []byte(kimiWorkConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	writeKimiWire(t, filepath.Join(root, "home"), "session_work", "main", lines...)
}

// CNSA0422: the Kimi desktop app's Work sessions are read from the share
// folder the app gives Daimon, their alias read as the model it stands for,
// and a call through magpie's bridge left to the gateway.
func TestKimiWorkCalls(t *testing.T) {
	for _, moved := range []bool{false, true} {
		name := "daimon-share"
		if moved {
			name = "moved share"
		}
		t.Run(name, func(t *testing.T) {
			claude, _ := setup(t)
			home := filepath.Join(filepath.Dir(claude), "home")
			ud := kimiDesktopUserData(home, runtime.GOOS)
			share := filepath.Join(ud, "daimon-share")
			if moved {
				share = filepath.Join(filepath.Dir(claude), "Kimi Share")
				if err := os.MkdirAll(ud, 0o755); err != nil {
					t.Fatal(err)
				}
				os.WriteFile(filepath.Join(ud, "daimon-storage.json"), []byte(`{"shareDir":`+strconvQuote(share)+`}`), 0o644)
			}
			own := strings.Replace(kimiRecordLine, `"kimi-code/k3"`, `"daimon-kimi-code"`, 1)
			bridged := strings.Replace(kimiRecordLine, `"kimi-code/k3"`, `"bridged"`, 1)
			bridged = strings.Replace(bridged, `1791696952640`, `1791696960000`, 1)
			writeKimiWork(t, share, own, bridged, "")
			var got []Call
			for _, c := range kimiCalls() {
				if c.Session == "session_work" {
					got = append(got, c)
				}
			}
			if len(got) != 1 {
				t.Fatalf("want the one call on Kimi's own endpoint, got %+v", got)
			}
			c := got[0]
			if c.Model != "k2p6-agent" || c.Tokens != (Tokens{Input: 20257, Output: 230}) || !c.Time.Equal(time.UnixMilli(1791696952640)) {
				t.Errorf("call %+v", c)
			}
			if f := Found(); !slices.Contains(f, "kimi") {
				t.Errorf("Found() = %v, want kimi for the Work sessions alone", f)
			}
		})
	}
}

func strconvQuote(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
}

// Each system's folder, as Electron's userData is there.
func TestKimiWorkShares(t *testing.T) {
	roaming := filepath.Join(t.TempDir(), "Roaming")
	t.Setenv("APPDATA", roaming)
	t.Setenv("XDG_CONFIG_HOME", "")
	home := filepath.Join("h", "u")
	for goos, want := range map[string]string{
		"darwin":  filepath.Join(home, "Library", "Application Support", "kimi-desktop", "daimon-share"),
		"windows": filepath.Join(roaming, "kimi-desktop", "daimon-share"),
		"linux":   filepath.Join(home, ".config", "kimi-desktop", "daimon-share"),
	} {
		got := kimiWorkSharesIn(home, goos)
		if !slices.Equal(got, []string{want, filepath.Join(home, ".kimi")}) {
			t.Errorf("%s: %v, want %s then ~/.kimi", goos, got, want)
		}
	}
}

func TestLoopbackURL(t *testing.T) {
	for s, want := range map[string]bool{
		"http://127.0.0.1:47213/v1":      true,
		"http://localhost:3425/v1":       true,
		"http://[::1]:3425":              true,
		"https://api.kimi.com/coding/v1": false,
		"":                               false,
	} {
		if loopbackURL(s) != want {
			t.Errorf("loopbackURL(%q) = %v", s, !want)
		}
	}
}
