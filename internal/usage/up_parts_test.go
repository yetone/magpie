package usage

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unsafe"
)

// A row keeps what its bytes up were made of (Record.UpParts) in 255ths
// of them, packed and read back, so each part is within a 255th of the
// request; a row that kept none reads back none, and keeping it made no
// row larger. usage.jsonl has the parts as they were.
func TestRowUpPartsKept(t *testing.T) {
	at := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	parts := UpParts{System: 120_000, Tools: 40_000, Images: 1_000}
	c := &rowChunk{}
	c.add(Row{Record: Record{Time: at, UpBytes: 1_000_000, UpParts: &parts, Tries: 3, Status: 529}}, "", 0, false)
	c.add(Row{Record: Record{Time: at, UpBytes: 1_000_000}}, "", 1, false)
	c.add(Row{Record: Record{Time: at, UpParts: &parts}}, "", 2, false) // nothing went up: no parts to be shares of
	for i, c := range []*rowChunk{c, c.pack().unpack()} {
		a, old, none := c.row(0), c.row(1), c.row(2)
		u := a.UpParts
		if u == nil {
			t.Fatalf("%d: the parts weren't kept", i)
		}
		for _, p := range [][2]int{{u.System, parts.System}, {u.Tools, parts.Tools}, {u.Images, parts.Images}} {
			if d := p[0] - p[1]; d < -1_000_000/255 || d > 1_000_000/255 {
				t.Fatalf("%d: read back %+v, was %+v", i, *u, parts)
			}
		}
		if a.Tries != 3 || a.Status != 529 {
			t.Fatalf("%d: beside them, %d tries and status %d", i, a.Tries, a.Status)
		}
		if old.UpParts != nil || none.UpParts != nil {
			t.Fatalf("%d: a row without parts reads back %+v, %+v", i, old.UpParts, none.UpParts)
		}
	}
	if n := unsafe.Sizeof(packedRow{}); n != 152 {
		t.Fatalf("a packed row is %d bytes, was 152", n)
	}
	line, _ := json.Marshal(Record{Time: at, UpBytes: 9, UpParts: &UpParts{System: 4}})
	var back Record
	if err := json.Unmarshal(line, &back); err != nil || back.UpParts == nil || back.UpParts.System != 4 || !strings.Contains(string(line), `"up_parts":{"system":4}`) {
		t.Fatalf("usage.jsonl's line %s read back %+v (%v)", line, back.UpParts, err)
	}
	if line, _ := json.Marshal(Record{Time: at, UpBytes: 9}); strings.Contains(string(line), "up_parts") {
		t.Fatalf("a row with no parts says some: %s", line)
	}
}

// A period's totals, and each agent's, say what their bytes up were made
// of: how many requests sent any (for the bytes a request), the bytes of
// those whose parts were read, and the parts summed. From the rows and
// from the request page's cells alike, and from records.
func TestTotalsSumUpParts(t *testing.T) {
	pageHome(t)
	now := holdClock(t, time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local))
	records := []Record{
		{Time: now, Provider: "a", Agent: "codex", Model: "m", Input: 10, UpBytes: 2550, UpParts: &UpParts{System: 510, Tools: 1020}, Status: 200},
		{Time: now, Provider: "a", Agent: "codex", Model: "m", Input: 10, UpBytes: 5100, UpParts: &UpParts{Images: 2600}, Status: 200},
		{Time: now, Provider: "b", Agent: "claude", Model: "n", Input: 10, UpBytes: 700, Status: 200}, // not read
		{Time: now, Provider: "b", Agent: "claude", Model: "n", Input: 10, Status: 200},               // nothing went
	}
	gateway := &rowChunk{}
	all := Ledgered{}
	for i, r := range records {
		row := Row{Record: r}
		gateway.add(row, "", int64(i), false)
		all.Rows = append(all.Rows, row)
	}
	page := buildRequestPage(Today, Filter{}, 0, 100, gateway, nil)
	var recs, rows, sum Totals
	for _, r := range records {
		recs.add(r, nil)
	}
	for _, r := range all.Rows {
		rows.addRow(r)
	}
	sum.plus(&rows)
	for name, s := range map[string]Totals{"page": page.Sum, "records": recs, "rows": rows, "plus": sum} {
		t.Run(name, func(t *testing.T) {
			if s.UpBytes != 8350 || s.UpCalls != 3 || s.UpMeasured != 7650 || s.UpSystem != 510 || s.UpTools != 1020 || s.UpImages != 2600 {
				t.Fatalf("the period: %+v", s)
			}
		})
	}
	agents := map[string]Totals{}
	for _, sh := range page.By["agent"] {
		agents[sh.ID] = sh.Totals
	}
	if c := agents["codex"]; c.UpCalls != 2 || c.UpMeasured != 7650 || c.UpImages != 2600 {
		t.Fatalf("codex: %+v", c)
	}
	if c := agents["claude"]; c.UpCalls != 1 || c.UpMeasured != 0 || c.UpBytes != 700 {
		t.Fatalf("claude: %+v", c)
	}
}
