package plugin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A models hook that said its vendor refused the sign-in (signIn
// "expired") isn't asked again for that sign-in at once: two dead Zed
// sign-ins were listed, and refused by Zed, ~30 times a minute
// (JZGiVvkSOA75376 on X). A new sign-in is asked at once.
func TestRefusedListIsNotAskedAgain(t *testing.T) {
	sandbox(t)
	log := filepath.Join(t.TempDir(), "models.log")
	t.Setenv("FAKE_MODELS_LOG", log)
	asked := func() int {
		b, _ := os.ReadFile(log)
		return strings.Count(string(b), "asked r-models-gone")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("testdata/fake/index.js")
	if _, err := Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	k, err := Import(ctx, "fakeco", map[string]any{"type": "oauth", "refresh": "r-models-gone", "access": "a", "expires": 9e15, "accountId": "gone@fake"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Providers(ctx); err != nil {
		t.Fatal(err)
	}
	first := asked()
	if first == 0 {
		t.Fatal("the models hook wasn't asked for the account")
	}
	for range 3 {
		if _, err := Providers(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if got := asked(); got != first {
		t.Fatalf("3 more listings asked the hook of a refused sign-in %d more times, want 0", got-first)
	}
	// signed in again: asked at once
	if err := Restore(ctx, "fakeco", k, map[string]any{"type": "oauth", "refresh": "r-models-gone", "access": "a2", "expires": 9e15, "accountId": "gone@fake"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Providers(ctx); err != nil {
		t.Fatal(err)
	}
	if got := asked(); got <= first {
		t.Fatal("a new sign-in's models weren't asked")
	}
}
