package catalog

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

// A vendor's list of n models, claude-1 … claude-n, answered a page at a
// time as Anthropic's own is: after_id names the id the next page starts
// after, and has_more says whether one is to come. The page is size ids
// long unless the question asks for more (limit, which Anthropic takes),
// and every question is kept, so a test can see what the fetch carried on
// from.
type pagedList struct {
	mu    sync.Mutex
	n     int
	size  int
	asked []string
	fixed bool // a relay that pages its own way and takes no limit
}

func (l *pagedList) serve(rw http.ResponseWriter, r *http.Request) {
	l.mu.Lock()
	defer l.mu.Unlock()
	after := r.URL.Query().Get("after_id")
	l.asked = append(l.asked, after)
	start := 0
	if after != "" {
		i, err := strconv.Atoi(strings.TrimPrefix(after, "claude-"))
		if err != nil || i < 1 || i >= l.n {
			http.Error(rw, "no such id: "+after, http.StatusBadRequest)
			return
		}
		start = i // after claude-7 the list goes on with claude-8
	}
	size := l.size
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && !l.fixed {
		size = n
	}
	end := min(start+size, l.n)
	var b strings.Builder
	b.WriteString(`{"data":[`)
	for i := start + 1; i <= end; i++ {
		if i > start+1 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"id":"claude-%d","name":"Claude %d","supported_endpoints":["/messages"]}`, i, i)
	}
	more := end < l.n
	last := ""
	if more {
		last = fmt.Sprintf("claude-%d", end)
	}
	fmt.Fprintf(&b, `],"has_more":%t,"first_id":"claude-%d","last_id":%q}`, more, start+1, last)
	rw.Write([]byte(b.String()))
}

func claudeIDs(n int) []string {
	var out []string
	for i := 1; i <= n; i++ {
		out = append(out, fmt.Sprintf("claude-%d", i))
	}
	return out
}

// A list that pages is read to its last page, not to its first: a base
// whose only list is a paged one — Anthropic's own, where twenty of the
// models came to a page and the rest were on the pages after — used to be
// taken for one that could not be asked at all, and the provider that
// preset makes was left with no models and an error (main listed twenty).
// The question after the first carries the id that page ended at, and
// asks for as many ids as the vendor takes.
func TestFetchFollowsThePagesOfAList(t *testing.T) {
	var l pagedList
	l.n, l.size = 25, 20 // Anthropic's page size, and a list a page short of two
	srv := httptest.NewServer(http.HandlerFunc(l.serve))
	defer srv.Close()

	ms, at, err := FetchAt(context.Background(), srv.URL+"/v1", "k", true, nil)
	if err != nil {
		t.Fatalf("a paged list is no answer: %v", err)
	}
	if at != srv.URL+"/v1/models" {
		t.Errorf("answered at %q", at)
	}
	if want := claudeIDs(25); !slices.Equal(ids(ms), want) {
		t.Errorf("listed %d models, want all 25 (%v)", len(ms), ids(ms))
	}
	if want := []string{"", "claude-20"}; !slices.Equal(l.asked, want) {
		t.Errorf("asked %q, want the first page and then the one after claude-20", l.asked)
	}
}

// The same list from a relay that pages twenty at a time whatever the
// question asks for: the pages are still followed, and the whole list is
// still what the base serves.
func TestFetchFollowsAPageAListAnswersItsOwnWay(t *testing.T) {
	var l pagedList
	l.n, l.size, l.fixed = 25, 20, true
	srv := httptest.NewServer(http.HandlerFunc(l.serve))
	defer srv.Close()

	ms, _, err := FetchAt(context.Background(), srv.URL+"/v1", "k", true, nil)
	if err != nil {
		t.Fatalf("a paged list is no answer: %v", err)
	}
	if want := claudeIDs(25); !slices.Equal(ids(ms), want) {
		t.Errorf("listed %d models, want all 25 (%v)", len(ms), ids(ms))
	}
	if want := []string{"", "claude-20"}; !slices.Equal(l.asked, want) {
		t.Errorf("asked %q, want the first page and then the one after claude-20", l.asked)
	}
}

// Half a list is not the list: a page that fails takes the whole answer
// with it, so the base is one that could not be asked and keeps what it
// listed last time — never the twenty it did get.
func TestFetchTakesAPagedListAsNoAnswerWhenAPageFails(t *testing.T) {
	var l pagedList
	l.n, l.size = 25, 20
	var pages int
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if pages++; pages > 1 {
			http.Error(rw, "the vendor fell over", http.StatusInternalServerError)
			return
		}
		l.serve(rw, r)
	}))
	defer srv.Close()

	ms, _, err := FetchAt(context.Background(), srv.URL+"/v1", "k", true, nil)
	if err == nil {
		t.Fatalf("took the first page for the list: %v", ids(ms))
	}
	if ms != nil {
		t.Errorf("listed a page of the list anyway: %v", ids(ms))
	}
}

// A list that pages forever is no answer either: it ends at the same id
// again, and one that goes on for good ends at the cap, not by asking
// until the vendor says so.
func TestFetchTakesAListThatPagesWithoutEndAsNoAnswer(t *testing.T) {
	// the same page under every question, ending at an id it has named
	// already
	repeat := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Write([]byte(`{"data":[{"id":"claude-1"}],"has_more":true,"first_id":"claude-1","last_id":"claude-1"}`))
	}))
	defer repeat.Close()
	if _, _, err := FetchAt(context.Background(), repeat.URL+"/v1", "k", true, nil); err == nil {
		t.Error("a list that answers the same page for ever was read as the list")
	}

	// one that goes on, a new id after every question
	var asked int
	endless := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		asked++
		rw.Write([]byte(fmt.Sprintf(`{"data":[{"id":"claude-%d"}],"has_more":true,"first_id":"claude-1","last_id":"claude-%d"}`, asked, asked+1)))
	}))
	defer endless.Close()
	_, _, err := FetchAt(context.Background(), endless.URL+"/v1", "k", true, nil)
	if err == nil {
		t.Fatal("a list that never ends was read as the list")
	}
	// each URL FetchAt falls back on is read to the cap of its own, and no
	// further: the base is not asked for ever
	if want := fmt.Sprintf("still paging after %d pages", modelPages); !strings.Contains(err.Error(), want) {
		t.Errorf("error %q, want it to say %q", err, want)
	}
	if asked > 3*modelPages {
		t.Errorf("asked %d pages, want at most the cap of %d for each of the URLs asked", asked, modelPages)
	}
}

// A list that says it has more without naming the id to carry on from
// cannot be read past its first page, and so is no answer at all.
func TestFetchTakesAPagedListWithoutAnIdAsNoAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Write([]byte(`{"data":[{"id":"claude-1"}],"has_more":true,"first_id":"claude-1"}`))
	}))
	defer srv.Close()
	ms, _, err := FetchAt(context.Background(), srv.URL+"/v1", "k", true, nil)
	if err == nil {
		t.Fatalf("a list that cannot be paged was read as the list: %v", ids(ms))
	}
	if ms != nil {
		t.Errorf("listed a page of the list anyway: %v", ids(ms))
	}
}
