package usage

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

func TestRollingChart(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	// A half-hour offset must still align hourly bars to the local hour.
	zone := time.FixedZone("half-hour", 5*3600+30*60)
	now := time.Date(2026, 10, 1, 15, 35, 12, 123, zone)
	for _, tc := range []struct {
		bucket string
		step   time.Duration
		minute int
		count  int
	}{{"hour", time.Hour, 0, 60}, {"10m", 10 * time.Minute, 30, 120}} {
		t.Run(tc.bucket, func(t *testing.T) {
			current := time.Date(2026, 10, 1, 15, tc.minute, 0, 0, zone)
			from := current.Add(-time.Duration(tc.count-1) * tc.step)
			recs := []Record{
				{Time: from.Add(-time.Nanosecond), Input: 100},
				{Time: from, Input: 2, Output: 1},
				{Time: from.Add(tc.step - time.Nanosecond), Input: 3},
				{Time: from.Add(tc.step), Input: 7},
				{Time: now, Input: 11},
				{Time: now.Add(time.Nanosecond), Input: 1000},
			}
			original := summarize(Month, now, recs)
			got := summarizeChart(Month, tc.bucket, now, recs)
			if got.Totals != original.Totals || !reflect.DeepEqual(got.Agents, original.Agents) || !reflect.DeepEqual(got.Models, original.Models) {
				t.Fatal("changing the chart changed the period's totals or groups")
			}
			if got.Bucket != tc.bucket || len(got.Series) != tc.count || !got.ChartFrom.Equal(from) || got.ChartTo.Sub(*got.ChartFrom) != time.Duration(tc.count)*tc.step {
				t.Fatalf("range: %+v", got)
			}
			if got.Series[0].Input != 5 || got.Series[0].Output != 1 || got.Series[1].Input != 7 || got.Series[tc.count-1].Input != 11 {
				t.Fatalf("boundary calls: %+v", got.Series)
			}
			for i, point := range got.Series {
				if !point.Time.Equal(from.Add(time.Duration(i)*tc.step)) || point.Label != point.Time.Format("15:04") {
					t.Fatalf("point %d: %+v", i, point)
				}
				if i > 1 && i < tc.count-1 && point.Calls != 0 {
					t.Fatalf("empty bucket %d: %+v", i, point)
				}
			}
		})
	}
	for _, bucket := range []string{"", "unknown"} {
		if got, want := summarizeChart(Week, bucket, now, nil), summarize(Week, now, nil); !reflect.DeepEqual(got, want) {
			t.Fatalf("%q should keep the automatic chart", bucket)
		}
	}
}

func TestRollingChartAcrossMidnightAndDST(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	now := time.Date(2026, 10, 1, 0, 5, 0, 0, time.UTC)
	got := summarizeChart(Today, "10m", now, []Record{{Time: now.Add(-10 * time.Minute), Input: 12}})
	if got.Calls != 0 || got.Series[118].Input != 12 || got.Series[118].Label != "23:50" {
		t.Fatalf("yesterday's chart call with empty today totals: %+v", got)
	}
	zone, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	// The second 01:35 when the clock goes back: the current bar must be
	// the second 01:00, preserving its UTC offset.
	now = time.Date(2026, 11, 1, 6, 35, 0, 0, time.UTC).In(zone)
	got = summarizeChart(Today, "hour", now, []Record{{Time: now.Add(-5 * time.Minute), Input: 12}})
	if got.Series[59].Input != 12 || !got.Series[59].Time.Equal(time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC)) {
		t.Fatalf("repeated local hour: %+v", got.Series[59])
	}
}
