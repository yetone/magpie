package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// The Anthropic preset's own one base, and a list of 25 models that comes
// twenty at a time as Anthropic's does (2026-10-06, #1006): a paged reply
// used to be no answer at all, so a provider whose only base is that one
// had nothing to fall back on — where main listed the twenty of the first
// page, the fetch answered an error and no models at all, and the pick the
// user had of one of the five on the second page went with it. The whole
// list is what the base serves, and all of it is what the fetch keeps.
func TestFetchFollowsThePagesOfASingleAnthropicBase(t *testing.T) {
	oneHome(t)
	var mu sync.Mutex
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		after := r.URL.Query().Get("after_id")
		asked = append(asked, after)
		mu.Unlock()
		start := 0
		if after != "" {
			i, err := strconv.Atoi(strings.TrimPrefix(after, "claude-"))
			if err != nil || i < 1 || i >= 25 {
				http.Error(w, "no such id: "+after, http.StatusBadRequest)
				return
			}
			start = i
		}
		// the page size the question asks for, where it asks for one
		// (limit, which Anthropic takes); twenty otherwise
		size := 20
		if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 {
			size = n
		}
		var b strings.Builder
		b.WriteString(`{"data":[`)
		for i := start + 1; i <= min(start+size, 25); i++ {
			if i > start+1 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, `{"id":"claude-%d","display_name":"Claude %d","supported_endpoints":["/messages"]}`, i, i)
		}
		more := start+size < 25
		last := ""
		if more {
			last = fmt.Sprintf("claude-%d", start+size)
		}
		fmt.Fprintf(&b, `],"has_more":%t,"first_id":"claude-%d","last_id":%q}`, more, start+1, last)
		w.Write([]byte(b.String()))
	}))
	defer srv.Close()

	p, err := FromPreset("anthropic")
	if err != nil {
		t.Fatal(err)
	}
	// only the preset's one base, pointed at the list that pages
	p.ID, p.Key, p.Anthropic = "anthropic-paged", "sk-ant", srv.URL+"/v1"
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	q, err := Find("anthropic-paged")
	if err != nil {
		t.Fatal(err)
	}
	ms, err := q.Fetch(context.Background())
	if err != nil {
		t.Fatalf("fetch of a paged list at the one base: %v", err)
	}
	var want []string
	for i := 1; i <= 25; i++ {
		want = append(want, fmt.Sprintf("claude-%d", i))
	}
	if got := idsOf(ms); !slices.Equal(got, want) {
		t.Fatalf("listed %d models, want all 25:\n got %v\nwant %v", len(ms), got, want)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(asked) != 2 || asked[0] != "" || asked[1] != "claude-20" {
		t.Errorf("asked %v, want the first page and then the one after claude-20", asked)
	}
}
