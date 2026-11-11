package agent

import (
	"fmt"
	"os"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// Pi's Drift is asked on every /api/drift poll, each its own request, and
// writing the entry of a routing group found the group's members each
// time, to see whether one speaks Anthropic's Messages API (#5037's
// promptCache): ~3.4 MB a poll with real accounts. What it found is kept
// across requests while the files stay the same, so a poll after the
// first finds no group's members.
func TestPiGroupEntryKeepsItsMembers(t *testing.T) {
	syncHome(t)
	models := ""
	var ids, members []string
	for i := range 40 {
		id := fmt.Sprintf("claude-opus-5-%d", i)
		if i > 0 {
			models += ","
		}
		models += fmt.Sprintf(`%q: {"id":%q,"name":"Claude %d","limit":{"context":200000}}`, id, id, i)
		ids = append(ids, id)
		members = append(members, "ap/"+id)
	}
	if err := os.WriteFile(catalog.CachePath(), []byte(`{"ap": {"models": {`+models+`}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	if err := provider.Save(provider.Provider{ID: "ap", Name: "Anthropic Plugin", Key: "k", Anthropic: "http://127.0.0.1:2", Models: ids}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{ID: "auto", Name: "Auto", Members: members}); err != nil {
		t.Fatal(err)
	}
	group := catalog.Model{ID: "group/auto", Name: "Auto"}
	// one request, as an /api/drift poll is
	request := func(fn func()) {
		release := provider.Hold()
		defer release()
		fn()
	}
	if e := piModelJSON(group, "", true); e["promptCache"] == nil {
		t.Fatalf("group/auto: no promptCache: %v", e)
	}
	request(func() { piModelJSON(group, "", true) }) // the first request finds them
	// counted, not measured in bytes: the process's allocations include
	// every goroutine earlier tests left running (#6178)
	before := provider.GroupsFound()
	for range 5 {
		request(func() { piModelJSON(group, "", true) })
	}
	if n := provider.GroupsFound() - before; n != 0 {
		t.Fatalf("5 polls' group entries found the group's members %d times: they are found again on every poll", n)
	}
	request(func() {
		if e := piModelJSON(group, "", true); e["promptCache"] == nil {
			t.Fatalf("group/auto: no promptCache kept: %v", e)
		}
	})
}
