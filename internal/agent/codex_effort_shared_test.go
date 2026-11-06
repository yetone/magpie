package agent

import (
	"fmt"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// Codex's effort field reads the models magpie keeps for it, not a copy
// of the whole list on each look: the window asks it on every poll.
func TestCodexEffortOptionsReadTheKeptList(t *testing.T) {
	home, _ := codexHome(t, `{"OPENAI_API_KEY":"sk-x"}`, "model = \"fake/m1\"\nmodel_provider = \"magpie\"\n")
	var ids []string
	for i := range 400 {
		ids = append(ids, fmt.Sprintf("m%d", i))
	}
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: ids}); err != nil {
		t.Fatal(err)
	}
	var effort *Field
	cx := codex(home)
	for i := range cx.Fields {
		if cx.Fields[i].Key == "effort" {
			effort = &cx.Fields[i]
		}
	}
	if effort == nil {
		t.Fatal("no effort field")
	}
	cur := map[string]string{"model": "fake/m1"}
	effort.Options(cur)
	// a copy of 400 models is one allocation, but of ~100 KB: count bytes
	// less the look's StampNow, whose os.Environ copies the environment:
	// every t.Setenv of the package before this one made it longer (an
	// Unsetenv leaves its slot), ~56 KB a look by the time this runs
	per, stamp := leastBytes(50, func() { effort.Options(cur) }), leastBytes(50, func() { provider.StampNow() })
	if per > stamp && per-stamp > 20_000 {
		t.Fatalf("a look at Codex's efforts allocated %d bytes past its stamp's %d, want at most 20000", per-stamp, stamp)
	}
}
