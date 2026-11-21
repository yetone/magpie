package usage

import (
	"reflect"
	"testing"
	"time"
)

// TestTotalsPlusAddsEveryField fails when a field added to Totals is left
// out of plus: the Requests page's groups would read it as 0.
func TestTotalsPlusAddsEveryField(t *testing.T) {
	var d Totals
	v := reflect.ValueOf(&d).Elem()
	for i := 0; i < v.NumField(); i++ {
		switch f := v.Field(i); f.Kind() {
		case reflect.Int, reflect.Int64:
			f.SetInt(int64(i + 1))
		case reflect.Float64:
			f.SetFloat(float64(i) + 0.5)
		case reflect.Pointer: // Rates, one reply's speed and one's floor
			f.Set(reflect.ValueOf(&Rates{Speed: speedHist{{uint16(i), 1}}, Floor: speedHist{{uint16(i), 1}}}))
		default:
			t.Fatalf("Totals.%s is a %s: plus and this test need it", v.Type().Field(i).Name, f.Kind())
		}
	}
	var got Totals
	got.plus(&d)
	got.plus(&d)
	w := reflect.ValueOf(&got).Elem()
	for i := 0; i < v.NumField(); i++ {
		name := v.Type().Field(i).Name
		switch a, b := w.Field(i), v.Field(i); a.Kind() {
		case reflect.Float64:
			if a.Float() != 2*b.Float() {
				t.Errorf("plus left out %s: %v of %v", name, a.Float(), 2*b.Float())
			}
		case reflect.Pointer:
			if r := got.Rates; r == nil || r.Speed.total() != 2 || r.Floor.total() != 2 {
				t.Errorf("plus left out %s: %+v", name, r)
			}
		default:
			if a.Int() != 2*b.Int() {
				t.Errorf("plus left out %s: %d of %d", name, a.Int(), 2*b.Int())
			}
		}
	}
}

// TestPartPlusIsAdd: a part counts a row from its sums as add counts it
// from the row, for each kind of row a chart has.
func TestPartPlusIsAdd(t *testing.T) {
	credits := 2.5
	rows := []Row{
		{Record: Record{Time: time.Now(), Agent: "claude", Input: 10, Output: 1200, CacheRead: 3, CacheWrite: 4, Status: 200, TTFT: 50, FirstText: 60, Millis: 3000}, Source: "log", Priced: true, Cost: 0.25},
		{Record: Record{Agent: "codex", Input: 7, Output: 500, Reasoning: 200, Status: 200, TTFT: 900, Millis: 4000}, Source: "log"},
		{Record: Record{Agent: "opencode", Input: 5, Status: 429, Error: "limited", TTFT: 10, Millis: 20}},
		{Record: Record{Agent: "opencode", Input: 5, Output: 9, HandedOn: true, Status: 500, Credits: &credits}},
		{Record: Record{Agent: "claude", Input: 5, Output: 9, Tries: 2, Status: 200, TTFT: 30, FirstText: 31, Millis: 400}, Priced: true, Cost: 1e-7},
	}
	for i, r := range rows {
		var want, got Part
		want.add(r)
		var d Totals
		d.addRow(r)
		got.plus(&d)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("row %d: plus %+v, add %+v", i, got, want)
		}
	}
}
