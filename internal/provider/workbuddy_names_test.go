package provider

import (
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// Two models WorkBuddy names alike are told apart by what the second's id
// adds, or by its id.
func TestWorkBuddyDistinctNames(t *testing.T) {
	ms := wbDistinctNames([]catalog.Model{
		{ID: "deepseek-v4.1-flash", Name: "Deepseek-V4.1-Flash"},
		{ID: "deepseek-v4.1-flash-sg", Name: "Deepseek-V4.1-Flash"},
		{ID: "glm-5.3", Name: "GLM-5.3"},
		{ID: "glm-x", Name: "GLM-5.3"},
	})
	want := []string{"Deepseek-V4.1-Flash", "Deepseek-V4.1-Flash (SG)", "GLM-5.3", "GLM-5.3 (glm-x)"}
	for i, m := range ms {
		if m.Name != want[i] {
			t.Errorf("%s: %q, want %q", m.ID, m.Name, want[i])
		}
	}
}
