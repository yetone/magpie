package edit

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnvBackslashRoundtrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".env")
	v := `C:\new folder\tmp`
	if e := SetEnvFile(p, KV{"MODEL_DIR", v}); e != nil {
		t.Fatal(e)
	}
	b, _ := os.ReadFile(p)
	t.Logf("written: %s", b)
	got, ok := GetEnvFile(p, "MODEL_DIR")
	if !ok || got != v {
		t.Fatalf("roundtrip %q -> %q (%v)", v, got, ok)
	}
}
