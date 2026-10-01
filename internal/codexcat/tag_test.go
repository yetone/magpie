package codexcat

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// A model whose window is known says so, rather than Codex taking it for
// the size it assumes of any; one not known leaves it to Codex.
func TestEntriesCarryContextWindow(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	var got struct {
		Models []map[string]any `json:"models"`
	}
	json.Unmarshal(Catalog([]catalog.Model{{ID: "relay/glm-4.6", Context: 204800}, {ID: "relay/x"}}), &got)
	if len(got.Models) != 2 || got.Models[0]["context_window"] != float64(204800) {
		t.Fatalf("%+v", got.Models)
	}
	if _, ok := got.Models[1]["context_window"]; ok {
		t.Errorf("unknown window: %+v", got.Models[1])
	}
}

// The ETag handed to Codex carries a tag of magpie's list: another list,
// another ETag, which Codex refetches on.
func TestETagTag(t *testing.T) {
	a := Tag([]catalog.Model{{ID: "p/m1"}})
	b := Tag([]catalog.Model{{ID: "p/m1"}, {ID: "q/m2"}})
	if a == b || a != Tag([]catalog.Model{{ID: "p/m1"}}) {
		t.Fatalf("tags %q %q", a, b)
	}
	for etag, want := range map[string]string{
		`W/"abc"`: `W/"abc+magpie-` + a + `"`,
		`"abc"`:   `"abc+magpie-` + a + `"`,
		"":        "+magpie-" + a,
	} {
		if got := WithTag(etag, a); got != want || !Tagged(got, a) || Tagged(got, b) {
			t.Errorf("WithTag(%q) = %q, want %q", etag, got, want)
		}
	}
	if Tagged(`W/"abc"`, a) {
		t.Error("the backend's own ETag is tagged")
	}
}
