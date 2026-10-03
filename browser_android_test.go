package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTermuxOpenInBrowser(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "url")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MAGPIE_BROWSER_TEST", out)
	script := "#!/system/bin/sh\n[ \"$#\" = 1 ] || exit 1\nprintf '%s' \"$1\" > \"$MAGPIE_BROWSER_TEST\"\n"
	if err := os.WriteFile(filepath.Join(dir, "termux-open-url"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	url := "https://example.com/oauth?state=a&code=$literal'quote"
	openInBrowser(url)
	deadline := time.Now().Add(3 * time.Second)
	for {
		b, err := os.ReadFile(out)
		if err == nil && string(b) == url {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("termux-open-url did not receive the URL intact: got %q, error=%v", b, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
