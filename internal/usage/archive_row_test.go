package usage

import (
	"fmt"
	"testing"
	"time"
)

// A request's row on the Usage page names where the request archive keeps
// it, as its line in the log does, packed away and read back too (#447);
// the Claude message id that follows it is untouched.
func TestRowKeepsArchive(t *testing.T) {
	c := &rowChunk{}
	now := time.Now()
	for i := range 80 {
		r := Row{Record: Record{Time: now.Add(time.Duration(i) * time.Second), Agent: "claude", Model: "m", Input: i, Archive: fmt.Sprintf("2026-10-02/1010%02d-00000000000000aa", i)}}
		c.add(r, fmt.Sprint("msg", i), int64(i), false)
	}
	for _, chunk := range []*rowChunk{c, c.pack().unpack()} {
		if chunk == nil || len(chunk.Rows) != 80 {
			t.Fatal("not read back")
		}
		for i := range chunk.Rows {
			if r := chunk.row(i); r.Archive != fmt.Sprintf("2026-10-02/1010%02d-00000000000000aa", i) || r.Input != i {
				t.Fatalf("row %d: archive %q", i, r.Archive)
			}
			if msg := chunk.Strings[chunk.Rows[i].Text[rowMsg]]; msg != fmt.Sprint("msg", i) {
				t.Fatalf("row %d: message %q", i, msg)
			}
		}
	}
}
