package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestKimiLiteralQuotedModelKey(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(p, []byte("default_model = \"kimi k2.5\"\n\n[models.'kimi k2.5']\nprovider = \"moonshot\"\nmodel = \"kimi-k2.5\"\n\n[models.kimi-k2]\nprovider = \"moonshot\"\nmodel = \"kimi-k2\"\n"), 0o600)
	opts := kimiOwnOptions(p, "")
	var vals []string
	for _, o := range opts {
		vals = append(vals, o.Value)
	}
	t.Logf("options: %q", vals)
	found := false
	for _, v := range vals {
		if v == "kimi k2.5" {
			found = true
		}
	}
	if !found {
		t.Fatalf("single-quoted literal model key missing from options: %q", vals)
	}
}
