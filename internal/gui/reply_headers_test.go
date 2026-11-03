package gui

import (
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// The editor refuses the reply headers, and the request headers to pass, a
// save would (#4145): its lists are provider.ReplyHeaderPassable's and
// RequestHeaderPassable's.
func TestReplyHeadersNeverAsTheProvidersAre(t *testing.T) {
	js, err := os.ReadFile("assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	listOf := func(name string) []string {
		m := regexp.MustCompile(`const ` + name + ` = (\[[^\]]*\]);`).FindSubmatch(js)
		if m == nil {
			t.Fatalf("no %s in app.js", name)
		}
		var out []string
		if err := json.Unmarshal(m[1], &out); err != nil {
			t.Fatal(err)
		}
		slices.Sort(out)
		return out
	}
	names, prefixes := provider.ReplyHeadersNever()
	lower := func(xs []string) []string {
		out := make([]string, len(xs))
		for i, x := range xs {
			out[i] = strings.ToLower(x)
		}
		slices.Sort(out)
		return out
	}
	if got, want := listOf("REPLY_NEVER"), lower(names); !slices.Equal(got, want) {
		t.Errorf("app.js REPLY_NEVER %q, provider %q", got, want)
	}
	if got, want := listOf("REPLY_NEVER_PREFIX"), lower(prefixes); !slices.Equal(got, want) {
		t.Errorf("app.js REPLY_NEVER_PREFIX %q, provider %q", got, want)
	}
	// and the request headers to pass, provider.RequestHeaderPassable's
	names, prefixes = provider.RequestHeadersNever()
	if got, want := listOf("REQUEST_NEVER"), lower(names); !slices.Equal(got, want) {
		t.Errorf("app.js REQUEST_NEVER %q, provider %q", got, want)
	}
	if got, want := listOf("REQUEST_NEVER_PREFIX"), lower(prefixes); !slices.Equal(got, want) {
		t.Errorf("app.js REQUEST_NEVER_PREFIX %q, provider %q", got, want)
	}
}
