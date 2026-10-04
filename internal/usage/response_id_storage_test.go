package usage

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestResponseIdentityStorageRoundTrip(t *testing.T) {
	for _, location := range []*time.Location{time.UTC, time.FixedZone("zero-offset", 0), time.FixedZone("UTC+08", 8*60*60)} {
		t.Run(location.String(), func(t *testing.T) {
			testResponseIdentityStorageRoundTrip(t, location)
		})
	}
}

func testResponseIdentityStorageRoundTrip(t *testing.T, location *time.Location) {
	t.Helper()
	chunk := &rowChunk{}
	for i := 0; i < 80; i++ {
		chunk.add(Row{Record: Record{Time: time.Unix(int64(i), 0).In(location), RequestID: fmt.Sprint("req", i), ResponseID: fmt.Sprint("resp", i)}}, fmt.Sprint("msg", i), int64(i), false)
	}
	packed := chunk.pack()
	if len(packed.Archive) == 0 {
		t.Fatal("fixture was not packed")
	}
	for _, c := range []*rowChunk{chunk, packed.unpack()} {
		for i := range c.Rows {
			r := c.row(i)
			if r.ResponseID != fmt.Sprint("resp", i) || r.RequestID != fmt.Sprint("req", i) || c.Strings[c.Rows[i].Text[rowMsg]] != fmt.Sprint("msg", i) {
				t.Fatalf("packed identity changed %+v", r)
			}
		}
	}
	row := chunk.row(0)
	b, err := json.Marshal(row.Record)
	if err != nil {
		t.Fatal(err)
	}
	var rec Record
	if err = json.Unmarshal(b, &rec); err != nil {
		t.Fatalf("JSON round trip: %s %v", b, err)
	}
	// JSON preserves the instant and offset, not the Location's name or pointer.
	want := row.Record
	want.Time, rec.Time = want.Time.UTC(), rec.Time.UTC()
	if !reflect.DeepEqual(rec, want) {
		t.Fatalf("JSON round trip changed record: got %+v, want %+v", rec, want)
	}
	var out bytes.Buffer
	if err = WriteCSV(&out, []Row{row}); err != nil {
		t.Fatal(err)
	}
	csvRows, err := csv.NewReader(&out).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for i, key := range csvRows[0] {
		if key == "response_id" {
			found = true
			if csvRows[1][i] != "resp0" {
				t.Fatal(csvRows)
			}
		}
	}
	if !found {
		t.Fatal("response_id column missing")
	}
}
