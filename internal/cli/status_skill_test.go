package cli

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// The magpie-api skill (internal/gateway/testdata/magpie-api, the
// gateway's TestMagpieAPISkillMatchesTheHandlers) tells an agent that
// magpie wasn't wired into to find the gateway by `magpie status --json`'s
// url: the field is the one that command writes.
func TestStatusJSONHasTheURLTheAPISkillReads(t *testing.T) {
	b, err := os.ReadFile("../gateway/testdata/magpie-api/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "`magpie status --json` prints its `url`") || !strings.Contains(statusUsage, "--json") {
		t.Fatal("the skill names another way to find the gateway")
	}
	out, err := json.Marshal(status{URL: "http://127.0.0.1:3425"})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if json.Unmarshal(out, &m); m["url"] != "http://127.0.0.1:3425" {
		t.Fatalf("magpie status --json writes %s", out)
	}
}
