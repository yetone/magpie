package plugin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Two accounts of one name that the plugin tells apart by a uid (WorkBuddy's
// nickname and uid) are both kept: the second took the first one's place
// (#413). The same uid again replaces its own sign-in.
func TestPluginSameNameOtherUID(t *testing.T) {
	sandbox(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("testdata/fake/index.js")
	if _, err := Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	signIn := func(team string) Saved {
		t.Helper()
		a, err := Authorize(ctx, "fakeco", 1, map[string]string{"where": "work", "team": team}, NewAccount)
		if err != nil {
			t.Fatal(err)
		}
		s, err := Finish(ctx, a.Session, "good")
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	saved := func() map[string]map[string]any {
		t.Helper()
		var m map[string]map[string]any
		b, _ := os.ReadFile(AuthPath())
		json.Unmarshal(b, &m)
		return m
	}
	first, second := signIn("pat/u1"), signIn("pat/u2")
	if first.Account == second.Account {
		t.Fatalf("the second took the first's place: %+v %+v", first, second)
	}
	if m := saved(); len(m) != 2 || m[first.Account]["uid"] != "u1" || m[second.Account]["uid"] != "u2" {
		t.Fatalf("saved %v", m)
	}
	if again := signIn("pat/u1"); again != first {
		t.Fatalf("u1 again as %+v, not %+v", again, first)
	}
	if m := saved(); len(m) != 2 {
		t.Fatalf("after u1 again %v", m)
	}
}
