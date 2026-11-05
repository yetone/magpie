package catalog

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// Two magpies that are each other's remote (cdredfox on Discord) list
// each other's models back: a model that left this magpie through the
// other, and comes back, is not taken again, so neither list grows a
// level on every fetch. One only through others is kept, with the magpies
// it passes, and so is one through this magpie alone: a remote that is
// this magpie itself.
func TestFetchLeavesOutWhatCameFromHere(t *testing.T) {
	self := Self()
	long := make([]string, MaxVia+1)
	for i := range long {
		long[i] = fmt.Sprintf("p%d", i)
	}
	q := func(via []string) string {
		s := make([]string, len(via))
		for i, v := range via {
			s[i] = `"` + v + `"`
		}
		return "[" + strings.Join(s, ",") + "]"
	}
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(rw, `{"object":"list","data":[
		  {"id":"up/m1","magpie_via":["peer"]},
		  {"id":"back/own/m1","magpie_via":%s},
		  {"id":"own/m2","magpie_via":%s},
		  {"id":"far/m3","magpie_via":%s},
		  {"id":"old/m4"}]}`, q([]string{self, "peer"}), q([]string{self}), q(long))
	}))
	defer srv.Close()
	ms, _, err := FetchAt(context.Background(), srv.URL+"/v1", "k", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, m := range ms {
		got[m.ID] = m.Via
	}
	if _, ok := got["back/own/m1"]; ok {
		t.Errorf("a model that went out of this magpie and came back was taken: %v", got)
	}
	if _, ok := got["far/m3"]; ok {
		t.Errorf("a model through more than %d magpies was taken: %v", MaxVia, got)
	}
	if v, ok := got["up/m1"]; !ok || !slices.Equal(v, []string{"peer"}) {
		t.Errorf("up/m1 = %v, %v; want kept through [peer]", v, ok)
	}
	if v, ok := got["own/m2"]; !ok || !slices.Equal(v, []string{self}) {
		t.Errorf("own/m2 = %v, %v; want kept through this magpie", v, ok)
	}
	if v, ok := got["old/m4"]; !ok || v != nil {
		t.Errorf("an older magpie's model = %v, %v; want kept, through nothing said", v, ok)
	}
}
