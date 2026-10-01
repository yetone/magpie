package gui

import (
	"slices"
	"testing"
	"time"
)

// Settings can turn the menu bar's logos off (Jevin on Discord: the small
// logos make it look worse): each cell is then its windows alone, with no
// logo nor letter to draw, the rows as they were.
func TestTrayPlain(t *testing.T) {
	cells, _, _ := trayUsageView(trayCards(), time.Now(), false)
	plain := trayPlain(cells)
	if len(plain) != len(cells) {
		t.Fatalf("%d cells, want %d", len(plain), len(cells))
	}
	for i, c := range plain {
		if !c.Plain || c.Icon != nil || c.Mono || c.Letter != "" || !slices.Equal(c.Rows, cells[i].Rows) {
			t.Errorf("cell %d: %+v", i, c)
		}
		// the cells as drawn with logos are left as they were
		if cells[i].Plain || len(cells[i].Icon) == 0 {
			t.Errorf("cell %d with logos changed: plain %v icon %d bytes", i, cells[i].Plain, len(cells[i].Icon))
		}
	}
	if trayPlain(nil) == nil || len(trayPlain(nil)) != 0 {
		t.Errorf("no cells: %+v", trayPlain(nil))
	}
}
