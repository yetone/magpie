package usage

import (
	"os"
	"reflect"
	"testing"
	"time"
)

// A settled session file the agent wrote a line to that is no call, or
// only touched, keeps the settled days: named by the file's mtime, each
// such touch paired the whole history again on every poll of the
// Requests page and the heatmap (GrayRabbit, 1-3 s a poll on 300k calls).
func TestSettledFileTouchedPairsRecentOnly(t *testing.T) {
	now, settled, paired, restart := pageHistory(t)
	heats := []int{}
	heatPairing = func(n int) { heats = append(heats, n) }
	t.Cleanup(func() { heatPairing = nil })
	page := QueryPage(All, Filter{}, 0, 1)
	heat := HeatmapOf(Filter{})
	if len(*paired) != 1 || len(heats) != 1 {
		t.Fatalf("first: pairings %v, heat %v", *paired, heats)
	}
	all, heatAll := (*paired)[0], heats[0]
	// most a query paired since from, -1 for none
	most := func(n []int, from int) int {
		m := -1
		for _, v := range n[from:] {
			m = max(m, v)
		}
		return m
	}
	touches := []func(){
		func() { // a line that is no call
			f, err := os.OpenFile(settled, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			f.WriteString(`{"timestamp":"` + now.UTC().Format(time.RFC3339Nano) + `","type":"event_msg","payload":{"type":"agent_message","message":"done"}}` + "\n")
			f.Close()
		},
		func() { // the mtime alone
			at := now.Add(-time.Minute)
			if err := os.Chtimes(settled, at, at); err != nil {
				t.Fatal(err)
			}
		},
	}
	for i, touch := range touches {
		touch()
		p, h := len(*paired), len(heats)
		if got := QueryPage(All, Filter{}, 0, 1); most(*paired, p) >= all || !reflect.DeepEqual(got, page) {
			t.Fatalf("touch %d: paired %v of %d: %+v, was %+v", i, (*paired)[p:], all, got, page)
		}
		if got := HeatmapOf(Filter{}); most(heats, h) >= heatAll || !reflect.DeepEqual(got, heat) {
			t.Fatalf("touch %d: the heatmap paired %v of %d: %+v, was %+v", i, heats[h:], heatAll, got, heat)
		}
	}
	// and from disk, on the next start
	restart()
	requestCache.Lock()
	requestCache.heatParts, requestCache.heatOld, requestCache.heats = nil, nil, nil
	requestCache.Unlock()
	p, h := len(*paired), len(heats)
	if got := QueryPage(All, Filter{}, 0, 1); most(*paired, p) >= all || !reflect.DeepEqual(got, page) {
		t.Fatalf("started again: paired %v of %d: %+v, was %+v", (*paired)[p:], all, got, page)
	}
	if got := HeatmapOf(Filter{}); most(heats, h) >= heatAll || !reflect.DeepEqual(got, heat) {
		t.Fatalf("started again: the heatmap paired %v of %d", heats[h:], heatAll)
	}
	restart()
	requestCache.Lock()
	requestCache.heatParts, requestCache.heatOld, requestCache.heats = nil, nil, nil
	requestCache.Unlock()
	os.Remove(pageDiskPath())
	os.Remove(heatDiskPath())
	if cold := QueryPage(All, Filter{}, 0, 1); !reflect.DeepEqual(cold, page) {
		t.Fatalf("cold %+v, kept %+v", cold, page)
	}
	if cold := HeatmapOf(Filter{}); !reflect.DeepEqual(cold, heat) {
		t.Fatalf("the heatmap cold %+v, kept %+v", cold, heat)
	}
}
