package usage

import (
	"encoding/csv"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestProviderAndCallerIdentitiesRemainIndependent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	now := time.Now()
	recs := []Record{
		{Time: now.Add(-time.Minute), Provider: "relay", Model: "m", ProviderKeyID: "primary", ProviderKeyName: "Primary", CallerKeyID: "desk", CallerKeyName: "Desk", Input: 10, Status: 200},
		{Time: now, Provider: "relay", Model: "m", ProviderKeyID: "backup", ProviderKeyName: "Backup", CallerKeyID: "desk", CallerKeyName: "Desk", Input: 20, Status: 200},
		{Time: now, Provider: "relay", Model: "m", ProviderKeyID: "backup", ProviderKeyName: "Backup", CallerKeyID: "server", CallerKeyName: "Server", Input: 30, Status: 200},
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
	var out strings.Builder
	if err := WriteCSV(&out, rows); err != nil {
		t.Fatal(err)
	}
	cells, err := csv.NewReader(strings.NewReader(out.String())).ReadAll()
	if err != nil || len(cells) != 3 {
		t.Fatalf("CSV: %v, %s", err, out.String())
	}
	if !slices.Equal(cells[0][len(cells[0])-4:], []string{"provider_key_id", "provider_key_name", "caller_key_id", "caller_key_name"}) {
		t.Fatal("provider columns must precede caller columns", cells[0])
	}
	for i, row := range rows {
		want := []string{row.ProviderKeyID, row.ProviderKeyName, row.CallerKeyID, row.CallerKeyName}
		if !slices.Equal(cells[i+1][len(cells[0])-4:], want) {
			t.Fatal("CSV lost an identity", cells[i+1], want)
		}
		encoded, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		if err := json.Unmarshal(encoded, &fields); err != nil {
			t.Fatal(err)
		}
		if fields["providerKeyId"] != row.ProviderKeyID || fields["providerKeyName"] != row.ProviderKeyName || fields["callerKeyId"] != "desk" || fields["callerKeyName"] != "Desk" || fields["keyId"] != nil || fields["keyName"] != nil {
			t.Fatal("JSON identities must be explicit", fields)
		}
	}
}
