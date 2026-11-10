package sessions

import (
	"fmt"
	"strings"
	"testing"
)

// A uuid is kept by its bytes, so it is found again in any case and no
// other id is taken for it; an id that is not a uuid is kept too.
func TestUUIDSetHoldsWhatWasAdded(t *testing.T) {
	s := uuidSet{}
	var ids []string
	for i := range 2000 {
		ids = append(ids, fmt.Sprintf("%08x-%04x-4%03x-a%03x-%012x", i*7919, i, i%4096, i%4096, i*104729))
	}
	for _, id := range ids[:1000] {
		s.add(id)
	}
	s.add("msg_01-not-a-uuid")
	for i, id := range ids {
		if got := s.has(id); got != (i < 1000) {
			t.Fatalf("has(%s) = %v", id, got)
		}
	}
	if !s.has(strings.ToUpper(ids[3])) {
		t.Fatal("a uuid isn't found in upper case")
	}
	if !s.has("msg_01-not-a-uuid") || s.has("msg_02-not-a-uuid") || s.has("") {
		t.Fatal("an id that isn't a uuid is kept wrong")
	}
	if len(s) != 1001 {
		t.Fatalf("%d kept, want 1001", len(s))
	}
}
