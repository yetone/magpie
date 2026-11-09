package usage

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The address a call came from (geesehowardt7 on X) is kept with the call
// and read back on the Usage page, and stays on this computer: the day
// sync shares with the others leaves it out.
func TestClientIPKeptHereNotShared(t *testing.T) {
	reportedHome(t)
	now := time.Now().Add(-time.Hour)
	Append(Record{Time: now, Agent: "claude", Provider: "anthropic", Model: "claude-opus-5-5", Input: 1000, Output: 100, Status: 200, ClientIP: "192.168.1.9"})

	page := QueryPage(All, Filter{}, 0, 50)
	if len(page.Rows) != 1 || page.Rows[0].ClientIP != "192.168.1.9" {
		t.Fatalf("the ledger's row: %+v", page.Rows)
	}
	n := 0
	for _, calls := range OwnDays(now.Add(-time.Hour)) {
		for _, c := range calls {
			n++
			b, _ := json.Marshal(c)
			if c.ClientIP != "" || strings.Contains(string(b), "192.168.1.9") {
				t.Fatalf("a shared call has the address: %s", b)
			}
		}
	}
	if n != 1 {
		t.Fatalf("%d shared calls", n)
	}
}
