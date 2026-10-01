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

func TestEnvFileValueQuotedInlineComment(t *testing.T) {
	for _, tc := range []struct{ name, line, want string }{
		{"double quoted", `GOOGLE_CLOUD_PROJECT="my-proj" # work`, "my-proj"},
		{"single quoted hash", `GOOGLE_CLOUD_PROJECT='a#b' # c`, "a#b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), ".env")
			if err := os.WriteFile(p, []byte(tc.line+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if got := envFileValue(p, "GOOGLE_CLOUD_PROJECT"); got != tc.want {
				t.Fatalf("envFileValue = %q, want %q", got, tc.want)
			}
		})
	}
}
