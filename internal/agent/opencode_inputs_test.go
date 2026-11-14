package agent

import (
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// OpenCode is told every kind a model takes that OpenCode knows, audio,
// video and pdf too, not text and image alone (Parlevent_ on X). A model
// that takes text alone is told nothing, as before.
func TestOpenCodeInputs(t *testing.T) {
	for _, c := range []struct {
		m    catalog.Model
		want []string
	}{
		{catalog.Model{ID: "g", Images: true, InputModalities: []string{"text", "image", "video", "audio", "pdf"}}, []string{"text", "image", "audio", "video", "pdf"}},
		{catalog.Model{ID: "g-noimg", InputModalities: []string{"text", "image", "audio"}}, []string{"text", "audio"}},
		{catalog.Model{ID: "v", Images: true}, []string{"text", "image"}},
		{catalog.Model{ID: "t", InputModalities: []string{"text"}}, nil},
		{catalog.Model{ID: "odd", InputModalities: []string{"text", "embedding"}}, nil},
	} {
		if got := openCodeInputs(c.m); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.m.ID, got, c.want)
		}
	}
}
