package main

import (
	"encoding/csv"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	stats "github.com/yetone/magpie/internal/usage"
)

// magpie usage --csv writes the period's requests, newest first, one row
// each, with the model asked for, sent and served.
func TestUsageCSV(t *testing.T) {
	groupsHome(t)
	y, m, d := time.Now().Date()
	day := time.Date(y, m, d, 0, 0, 0, 0, time.Local) // today's, whenever this runs
	stats.Append(stats.Record{Time: day.Add(time.Second), Agent: "codex", Provider: "a", Model: "m", Requested: "fast", Served: "m-mini", Input: 10, Output: 2, Millis: 700, Status: 200})
	stats.Append(stats.Record{Time: day.Add(2 * time.Second), Agent: "claude", Provider: "b", Model: "gpt-5.5", Requested: "b/gpt-5.5", Millis: 30, Status: 502})
	stats.Append(stats.Record{Time: day.AddDate(0, 0, -3), Agent: "pi", Provider: "a", Model: "m", Input: 1, Status: 200})
	var b strings.Builder
	if err := usageTo(&b, []string{"usage", "--csv", "today"}); err != nil {
		t.Fatal(err)
	}
	recs, err := csv.NewReader(strings.NewReader(b.String())).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 3 || strings.Join(recs[0], ",") != strings.Join(stats.CSVHeader, ",") {
		t.Fatalf("%q", recs)
	}
	col := func(row []string, name string) string {
		for i, h := range recs[0] {
			if h == name {
				return row[i]
			}
		}
		t.Fatalf("no column %s", name)
		return ""
	}
	if r := recs[1]; col(r, "agent") != "claude" || col(r, "status") != "502" || col(r, "error") != "true" || col(r, "requested_model") != "b/gpt-5.5" {
		t.Errorf("newest %q", r)
	}
	if r := recs[2]; col(r, "requested_model") != "fast" || col(r, "model") != "m" || col(r, "served_model") != "m-mini" || col(r, "swapped") != "true" ||
		col(r, "input_tokens") != "10" || col(r, "output_tokens") != "2" || col(r, "duration_ms") != "700" || col(r, "error") != "false" {
		t.Errorf("swapped %q", r)
	}
	b.Reset()
	if err := usageTo(&b, []string{"usage", "all", "--csv"}); err != nil || strings.Count(b.String(), "\n") != 4 {
		t.Fatalf("all: %v %q", err, b.String())
	}
	if err := usageTo(&b, []string{"usage", "--csv", "today", "extra"}); err == nil {
		t.Fatal("took an extra argument")
	}
}

func TestUsageProviderKeys(t *testing.T) {
	groupsHome(t)
	id := provider.KeyID("fixture-provider-secret")
	stats.Append(stats.Record{Provider: "relay", Model: "m", ProviderKeyID: id, ProviderKeyName: "Team", Input: 30, Status: 200})
	out, err := said(t, func() error { return usageCmd([]string{"usage", "all"}) })
	if err != nil || !strings.Contains(out, "upstream provider keys") || !strings.Contains(out, "relay / Team") || strings.Contains(out, "fixture-provider-secret") {
		t.Fatal(out, err)
	}
	var csv strings.Builder
	if err := usageTo(&csv, []string{"usage", "--csv", "all"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(csv.String(), "provider_key_id,provider_key_name") || !strings.Contains(csv.String(), id+",Team") || strings.Contains(csv.String(), "fixture-provider-secret") {
		t.Fatal(csv.String())
	}
}
