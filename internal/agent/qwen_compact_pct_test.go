package agent

import (
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

// Qwen Code compacts at the percent of its window the user picked for it
// in magpie (moooyo on Discord: compact once 65% of the context is used),
// through context.autoCompactThreshold, a fraction of the model's window,
// while magpie's models are in its settings. None picked takes magpie's
// out, and so does leaving magpie, pruning the "context" it alone made;
// one the user set themselves is theirs.
func TestQwenCompactPercent(t *testing.T) {
	_, path := qwenHome(t)
	os.WriteFile(path, []byte(qwenUser), 0o644)
	a, err := Find("qwen")
	if err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	expect := func(what, want string) {
		t.Helper()
		got, has := edit.GetJSON(path, qwenCompactPct)
		if !has {
			got = "-"
		}
		if got != want {
			t.Fatalf("%s: %s %q; want %q\n%s", what, qwenCompactPct, got, want, readFile(path))
		}
	}
	must(a.Apply("model", "magpie/deepseek/pro"))
	expect("none picked", "-")
	must(provider.SetAgentCompactPct("qwen", 65))
	must(a.Sync())
	expect("65 picked", "0.65")
	must(provider.SetAgentCompactPct("qwen", 80))
	must(a.Sync())
	expect("80 picked", "0.8")
	must(provider.SetAgentCompactPct("qwen", 0))
	must(a.Sync())
	expect("cleared", "-")
	if strings.Contains(readFile(path), `"context"`) {
		t.Fatalf("an empty context left behind:\n%s", readFile(path))
	}
	must(provider.SetAgentCompactPct("qwen", 65))
	must(a.Sync())
	must(a.Apply("model", "glm-5"))
	expect("back on the user's own model", "-")

	// the user's own stays as they set it
	must(edit.SetJSON(path, edit.KV{Path: qwenCompactPct, Value: 0.5}))
	must(a.Apply("model", "magpie/deepseek/pro"))
	expect("the user's own, 65 picked", "0.5")
	must(provider.SetAgentCompactPct("qwen", 0))
	must(a.Sync())
	expect("the user's own, cleared", "0.5")
	must(a.Apply("model", "glm-5"))
	expect("the user's own, off magpie", "0.5")
}
