package agent

import (
	"strings"
	"testing"
)

// PowerShell runs a GUI program (magpie.exe is linked as one) that ends a
// pipeline in the background: it neither waits for it nor reads what it
// prints, so `@(& magpie agy env)` is empty and agy starts on the user's
// own sign-in with none of magpie's models (CyberCaius on Discord). Every
// PowerShell function that asks magpie for its env pipes the answer on.
func TestPowerShellEnvReadIsPiped(t *testing.T) {
	prog := `C:\Users\o'neil\.local\bin\magpie.exe`
	fns := map[string]string{
		"agy":         agyFunction("powershell", prog),
		"plugged agy": newLaunch(agyLaunchDesc(), `C:\cfg.json`).function("powershell", prog),
	}
	for name, fn := range fns {
		var calls int
		for _, line := range strings.Split(fn, "\n") {
			if !strings.Contains(line, "' agy env") {
				continue
			}
			calls++
			if !strings.Contains(line, "' agy env | ") || strings.Contains(line, "@(&") {
				t.Errorf("%s: magpie's env ends its pipeline, so a GUI magpie.exe isn't read:\n%s", name, line)
			}
		}
		if calls != 1 {
			t.Errorf("%s: %d calls of `agy env`, want 1:\n%s", name, calls, fn)
		}
	}
}
