package agent

import (
	"fmt"
	"os"
	"runtime"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// Pi's Drift is asked on every /api/drift poll, each its own request, and
// writing the entry of a routing group found the group's members each
// time, to see whether one speaks Anthropic's Messages API (#5037's
// promptCache): ~3.4 MB a poll with real accounts. What it found is kept
// across requests while the files stay the same, so a poll's entry
// allocates less than half of one look-up of the group.
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
	// one request: what it allocates, the least of a few tries (other
	// goroutines allocate meanwhile too)
	request := func(fn func()) uint64 {
		least := ^uint64(0)
		for range 5 {
			release := provider.Hold()
			var a, b runtime.MemStats
			runtime.ReadMemStats(&a)
			fn()
			runtime.ReadMemStats(&b)
			release()
			least = min(least, b.TotalAlloc-a.TotalAlloc)
		}
		return least
	}
	if e := piModelJSON(group, "", true); e["promptCache"] == nil {
		t.Fatalf("group/auto: no promptCache: %v", e)
	}
	// what is held within a request (the entries) is warm in both
	request(func() { provider.FindGroup("group/auto") })
	request(func() { piModelJSON(group, "", true) })
	find := request(func() { provider.FindGroup("group/auto") })
	entry := request(func() { piModelJSON(group, "", true) })
	t.Logf("a look-up of the group %d bytes, the group's entry %d", find, entry)
	if entry*2 > find {
		t.Fatalf("a poll's group entry allocated %d bytes, a look-up of its members %d: the members are found again", entry, find)
	}
	if e := piModelJSON(group, "", true); e["promptCache"] == nil {
		t.Fatalf("group/auto: no promptCache kept: %v", e)
	}
}
