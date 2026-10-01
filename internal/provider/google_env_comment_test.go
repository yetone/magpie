package provider

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGoogleEnvInlineComment(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".env")
	os.WriteFile(p, []byte("GOOGLE_CLOUD_PROJECT=my-project-123 # work project\n"), 0o600)
	if got := envFileValue(p, "GOOGLE_CLOUD_PROJECT"); got != "my-project-123" {
		t.Fatalf("got %q", got)
	}
}
