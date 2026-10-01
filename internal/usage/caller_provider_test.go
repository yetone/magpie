package usage

import (
	"encoding/csv"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/sessions"
)

func TestProviderAndCallerIdentitiesRemainIndependent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	now := time.Now()
	recs := []Record{
		{Time: now.Add(-time.Minute), RouteID: 123, Provider: "relay", Model: "m", ProviderKeyID: "primary", ProviderKeyName: "Primary", CallerKeyID: "desk", CallerKeyName: "Desk", Input: 10, Status: 200},
		{Time: now, RouteID: 456, Provider: "relay", Model: "m", ProviderKeyID: "backup", ProviderKeyName: "Backup", CallerKeyID: "desk", CallerKeyName: "Desk", Input: 20, Status: 200},
		{Time: now, RouteID: 456, Provider: "relay", Model: "m", ProviderKeyID: "backup", ProviderKeyName: "Backup", CallerKeyID: "server", CallerKeyName: "Server", Input: 30, Status: 200},
	}
	s := summarize(All, now, recs)
	if len(s.ProviderKeys) != 2 || len(s.CallerKeys) != 2 || s.Input != 60 {
		t.Fatalf("independent summaries: %+v", s)
	}
	for _, g := range s.ProviderKeys {
		want := 10
		if g.ProviderKeyID == "backup" {
			want = 50
		}
		if g.Input != want || g.CallerKeyID != "" {
			t.Fatalf("provider grouping mixed with caller: %+v", g)
		}
	}
	for _, g := range s.CallerKeys {
		if g.Input != 30 || g.ProviderKeyID != "" {
			t.Fatalf("caller grouping mixed with provider: %+v", g)
		}
	}
	rows, total, _ := ledger(All.Since(now), Filter{CallerKey: "desk"}, recs)
	if len(rows) != 2 || total.Input != 30 {
		t.Fatalf("caller filter across upstream keys: %+v, %+v", rows, total)
	}
	if rows, total, _ := ledger(All.Since(now), Filter{RouteID: 456}, recs); len(rows) != 2 || total.Input != 50 {
		t.Fatalf("route filter across caller keys: %+v, %+v", rows, total)
	}
	if rows, total, _ := ledger(All.Since(now), Filter{RouteID: 456, CallerKey: "desk"}, recs); len(rows) != 1 || total.Input != 20 || rows[0].ProviderKeyID != "backup" || rows[0].CallerKeyID != "desk" {
		t.Fatalf("combined route and caller filter: %+v, %+v", rows, total)
	}
	if rows, total, _ := ledger(All.Since(now), Filter{RouteID: 123, CallerKey: "server"}, recs); len(rows) != 0 || total.Calls != 0 {
		t.Fatalf("combined filters must intersect: %+v, %+v", rows, total)
	}
	var out strings.Builder
	if err := WriteCSV(&out, rows); err != nil {
		t.Fatal(err)
	}
	cells, err := csv.NewReader(strings.NewReader(out.String())).ReadAll()
	if err != nil || len(cells) != 3 {
		t.Fatalf("CSV: %v, %s", err, out.String())
	}
	columns := []string{"provider_key_id", "provider_key_name", "route_id", "caller_key_id", "caller_key_name"}
	indexes := make([]int, len(columns))
	for i, name := range columns {
		indexes[i] = slices.Index(cells[0], name)
		if indexes[i] < 0 || (i > 0 && indexes[i] <= indexes[i-1]) {
			t.Fatal("missing or reordered identity columns", cells[0])
		}
	}
	if !slices.Equal(cells[0][len(cells[0])-2:], columns[3:]) {
		t.Fatal("provider columns must precede caller columns", cells[0])
	}
	for i, row := range rows {
		want := []string{row.ProviderKeyID, row.ProviderKeyName, strconv.FormatInt(row.RouteID, 10), row.CallerKeyID, row.CallerKeyName}
		for j, index := range indexes {
			if cells[i+1][index] != want[j] {
				t.Fatal("CSV lost an identity", cells[i+1], want)
			}
		}
		encoded, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		if err := json.Unmarshal(encoded, &fields); err != nil {
			t.Fatal(err)
		}
		if fields["route_id"] != float64(row.RouteID) || fields["providerKeyId"] != row.ProviderKeyID || fields["providerKeyName"] != row.ProviderKeyName || fields["callerKeyId"] != "desk" || fields["callerKeyName"] != "Desk" || fields["keyId"] != nil || fields["keyName"] != nil {
			t.Fatal("JSON identities must be explicit", fields)
		}
	}
}

func TestPackedRequestPageCallerAndRouteIdentity(t *testing.T) {
	pageHome(t)
	now := time.Now()
	for i := 0; i < 80; i++ {
		caller := "desk"
		if i%2 != 0 {
			caller = "server"
		}
		Append(Record{Time: now.Add(time.Duration(i) * time.Second), Agent: "codex", Provider: "relay", Model: "m", RouteID: 456, CallerKeyID: caller, CallerKeyName: strings.ToUpper(caller), ProviderKeyID: "primary", ProviderKeyName: "Primary", Input: 10, Output: 1, Status: 200})
	}
	// The first query creates the compressed snapshot; a different filter must
	// decode it instead of reusing the first materialized page.
	for _, filter := range []Filter{{}, {CallerKey: "desk", RouteID: 456, Provider: "relay", Model: "m"}, {CallerKey: "server"}} {
		for repeat := 0; repeat < 2; repeat++ {
			page := QueryPage(All, filter, 0, 100)
			want := 80
			if filter.CallerKey != "" {
				want = 40
			}
			if page.Total != want || len(page.Rows) != want || page.Sum.Input != want*10 {
				t.Fatalf("caller/route filter lost requests: %+v, rows=%d sum=%+v", filter, len(page.Rows), page.Sum)
			}
			if len(page.CallerKeys) != 2 {
				t.Fatalf("selected caller hid other keys: %+v", page.CallerKeys)
			}
			for _, group := range page.CallerKeys {
				if group.Calls != 40 || group.Input != 400 || group.CallerKeyName != strings.ToUpper(group.CallerKeyID) || group.ProviderKeyID != "" {
					t.Fatalf("caller grouping mixed with provider: %+v", group)
				}
			}
			equalPage(t, page, pageFromLedger(All, filter, 0, 100, LedgerOf(All, Filter{})))
			for _, row := range page.Rows {
				if row.RouteID != 456 || row.CallerKeyID == "" || row.CallerKeyName != strings.ToUpper(row.CallerKeyID) || row.ProviderKeyID != "primary" || row.ProviderKeyName != "Primary" || (filter.CallerKey != "" && row.CallerKeyID != filter.CallerKey) {
					t.Fatalf("compressed cache lost independent identities: %+v", row)
				}
			}
		}
	}
	requestCache.Lock()
	packed := len(requestCache.gateway.Archive) > 0
	requestCache.Unlock()
	if !packed {
		t.Fatal("test must exercise a compressed gateway snapshot")
	}
}

func TestCallerPageKeepsUnattributedSessionsSeparate(t *testing.T) {
	pageHome(t)
	now := time.Now()
	recs := []Record{
		{Time: now, Agent: "codex", Provider: "relay", Model: "m", CallerKeyID: "desk", CallerKeyName: "Desk", Input: 10, Status: 200},
		{Time: now, Agent: "codex", Provider: "relay", Model: "m", CallerKeyID: "server", CallerKeyName: "Server", Input: 20, Status: 200},
		{Time: now, Agent: "codex", Model: "unknown", CallerKeyID: "desk", CallerKeyName: "Desk", Status: 404, Rejected: true, Error: "unknown model"},
		{Time: now, Agent: "codex", Provider: "relay", Model: "m", Input: 30, Status: 200},
	}
	for i := range recs {
		recs[i].Time = now.Add(time.Duration(i) * time.Second)
		Append(recs[i])
	}
	logs := []sessions.Call{{Time: now.Add(-time.Second), Agent: "claude", Session: "local", Model: "m", Tokens: sessions.Tokens{Input: 40}}}
	rows, sum, agents, providers := ledgerWith(time.Time{}, Filter{}, recs, logs)
	all := Ledgered{Rows: rows, Sum: sum, Agents: agents, Providers: providers}
	gateway, local := &rowChunk{}, &rowChunk{Source: sessions.CallSource{Path: "/local"}}
	for i, row := range rows {
		if row.Source == "log" {
			local.add(row, "local-message", int64(i), false)
		} else {
			gateway.add(row, "", int64(i), false)
		}
	}
	for _, filter := range []Filter{{}, {CallerKey: "desk"}, {CallerKey: "server"}, {CallerKey: "desk", Failed: true}, {CallerKey: "missing"}} {
		page := buildRequestPage(All, filter, 0, 100, gateway, []*rowChunk{local})
		equalPage(t, page, pageFromLedger(All, filter, 0, 100, all))
		if len(page.CallerKeys) != 2 || page.CallerKeys[0].CallerKeyID != "server" || page.CallerKeys[0].Input != 20 || page.CallerKeys[1].CallerKeyID != "desk" || page.CallerKeys[1].Calls != 1 || page.CallerKeys[1].Input != 10 {
			t.Fatalf("unattributed sessions or rejections inflated caller groups: %+v", page.CallerKeys)
		}
		for _, row := range page.Rows {
			if filter.CallerKey != "" && row.Source == "log" {
				t.Fatal("local session wrongly attributed to a gateway key", row)
			}
		}
	}
}
