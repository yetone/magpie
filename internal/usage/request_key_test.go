package usage

import (
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// The Requests page packs rows into its cache and filters the rows it reads
// back: the upstream key a call used must survive that, so a search by the
// key's name finds the call there as it does in the ledger, and the summary
// still groups the calls by key beside the remote route they came by.
func TestRequestPageKeepsProviderKey(t *testing.T) {
	pageHome(t)
	now := time.Now()
	personal, team := provider.KeyID("personal-secret"), provider.KeyID("team-secret")
	for i := 0; i < 6; i++ {
		id, name := personal, "Personal"
		if i%2 == 1 {
			id, name = team, "Team"
		}
		Append(Record{Time: now.Add(time.Duration(i) * time.Second), Agent: "codex", Via: "studio", Provider: "relay", Model: "m",
			ProviderKeyID: id, ProviderKeyName: name, Input: 10, Output: 1, Status: 200})
	}
	for repeat := 0; repeat < 2; repeat++ {
		page := QueryPage(All, Filter{Query: "team"}, 0, 100)
		if page.Total != 3 || len(page.Rows) != 3 {
			t.Fatalf("key name search: total %d, %+v", page.Total, page.Rows)
		}
		for _, r := range page.Rows {
			if r.ProviderKeyID != team || r.ProviderKeyName != "Team" || r.Via != "studio" {
				t.Fatalf("row lost its key or route: %+v", r)
			}
		}
		if page := QueryPage(All, Filter{Query: personal[:8]}, 0, 100); page.Total != 3 {
			t.Fatalf("key id search: %d", page.Total)
		}
	}
	s := Summarize(All)
	got := map[string]Group{}
	for _, g := range s.ProviderKeys {
		got[g.ID] = g
	}
	if len(got) != 2 || got["relay#"+team].Calls != 3 || got["relay#"+team].ProviderKeyName != "Team" || got["relay#"+personal].Calls != 3 {
		t.Fatalf("summary keys: %+v", s.ProviderKeys)
	}
}

// A link from Routing opens the Requests page on the one call a route made:
// the route id must survive the page's packed rows, or the filter finds
// nothing and the rows lose the link back.
func TestRequestPageKeepsRouteID(t *testing.T) {
	pageHome(t)
	now := time.Now()
	for i := 0; i < 4; i++ {
		Append(Record{RouteID: int64(100 + i), Time: now.Add(time.Duration(i) * time.Second), Agent: "codex", Provider: "relay", Model: "m", Input: 10, Output: 1, Status: 200})
	}
	for repeat := 0; repeat < 2; repeat++ {
		page := QueryPage(All, Filter{RouteID: 102}, 0, 100)
		if page.Total != 1 || len(page.Rows) != 1 || page.Rows[0].RouteID != 102 {
			t.Fatalf("route filter: total %d, %+v", page.Total, page.Rows)
		}
		for _, r := range QueryPage(All, Filter{}, 0, 100).Rows {
			if r.RouteID < 100 || r.RouteID > 103 {
				t.Fatalf("row lost its route id: %+v", r)
			}
		}
	}
}
