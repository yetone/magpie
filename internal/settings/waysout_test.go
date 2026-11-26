package settings

import (
	"slices"
	"testing"
)

// ClaudeWaysOut keeps regions in capitals, IPs and CIDR ranges, and
// refuses anything else (mamba on Discord).
func TestClaudeWaysOut(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := Save(Settings{ClaudeWaysOut: []string{" us", "JP", "203.0.113.9", "2001:db8::/32", "", "US"}}); err != nil {
		t.Fatal(err)
	}
	if got := Load().ClaudeWaysOut; !slices.Equal(got, []string{"US", "JP", "203.0.113.9", "2001:db8::/32"}) {
		t.Fatalf("kept %q", got)
	}
	for _, bad := range []string{"USA", "United States", "203.0.113.300", "203.0.113.0/33", "U1"} {
		if err := Save(Settings{ClaudeWaysOut: []string{bad}}); err == nil {
			t.Errorf("%q was saved", bad)
		}
	}
}
