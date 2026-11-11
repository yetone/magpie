package usage

import (
	"testing"
	"time"
)

// A gateway window excludes its local midnight, including after the day rolls over.
func TestGatewaySessionWindowMidnight(t *testing.T) {
	resetUsageTest(t)
	midnight := time.Date(2026, 10, 7, 0, 0, 0, 0, time.Local)
	next := midnight.AddDate(0, 0, 1)
	for _, record := range []struct {
		id string
		at time.Time
	}{
		{"at-start", midnight},
		{"after-start", midnight.Add(time.Nanosecond)},
		{"at-next", next},
		{"after-next", next.Add(time.Nanosecond)},
	} {
		Append(Record{Time: record.at, Agent: "pi", Session: record.id, Provider: "p", Model: "m", Input: 1, Status: 200})
	}
	for _, test := range []struct {
		since time.Time
		want  map[string]bool
	}{
		{midnight, map[string]bool{"after-start": true, "at-next": true, "after-next": true}},
		{next, map[string]bool{"after-next": true}},
	} {
		got, _ := GatewaySessionWindow(test.since)
		if len(got) != len(test.want) {
			t.Fatalf("since %v: got %+v, want %v", test.since, got, test.want)
		}
		for _, session := range got {
			if !test.want[session.ID] {
				t.Fatalf("since %v: unexpected session %s", test.since, session.ID)
			}
		}
	}
}
