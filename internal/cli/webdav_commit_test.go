package cli

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/davsync"
)

// magpie github set commit-as= names who the sync's commits are by
// (#4579), as git writes an identity, or puts back the default.
func TestCommitAsArg(t *testing.T) {
	for _, tc := range []struct {
		in, name, email string
		ok              bool
	}{
		{"magpie-sync[bot] <123456+magpie-sync[bot]@users.noreply.github.com>", "magpie-sync[bot]", "123456+magpie-sync[bot]@users.noreply.github.com", true},
		{"  Ada Lovelace<ada@example.com> ", "Ada Lovelace", "ada@example.com", true},
		{"default", "", "", true},
		{"Default", "", "", true},
		{"Ada", "", "", false},
		{"<ada@example.com>", "", "", false},
		{"Ada <>", "", "", false},
		{"Ada <ada@example.com", "", "", false},
		{"Ada <ada@example.com> x", "", "", false},
	} {
		name, email, ok := commitAs(tc.in)
		if ok != tc.ok || ok && (name != tc.name || email != tc.email) {
			t.Errorf("commit-as=%q: %q %q %v", tc.in, name, email, ok)
		}
	}
	if !strings.Contains(githubUsage, "commit-as") || !strings.Contains(githubUsage, davsync.DefaultCommitName) {
		t.Fatal("magpie github help doesn't say commit-as and its default")
	}
}
