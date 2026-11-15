package gateway

import (
	"github.com/yetone/magpie/internal/access"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// Routing groups nested in one another, each with rules a decision model
// judges, share one classifier wait for the request, not one each
// (heatonz on Discord, v0.1.2215): each level asked its own classifier
// with the whole wait, so a request through three ruled levels whose
// classifiers didn't answer was held three waits before it was routed,
// past the unrouted watch's bar, and answered 504 "held this request for
// 2m0s without routing it". A level asked after the wait is spent goes by
// its group's order at once, and a classifier cut short by the shared
// wait isn't rested for it: it had less than its own wait.
func TestNestedGroupsShareTheClassifierWait(t *testing.T) {
	fresh(t)
	resetClassified(t)
	was := classifyUnit
	classifyUnit = 60 * time.Millisecond // the 8 seconds unset are 480ms
	t.Cleanup(func() { classifyUnit = was })
	// three waits are 1.44s, held past 1.1s; one wait routes at ~0.5s
	unroutedLimits(t, 50*time.Millisecond, 1100*time.Millisecond)
	unknownLimit(t, 30*time.Second)

	a := &keyed{}
	serveOn(t, "a", "ka", []string{"m"}, a)
	var mu sync.Mutex
	asked := map[string]int{}
	stop := make(chan struct{})
	serveOn(t, "c", "kc", []string{"cls1", "cls2", "cls3"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		for _, m := range []string{"cls1", "cls2", "cls3"} {
			if strings.Contains(string(b), `"`+m+`"`) {
				asked[m]++
			}
		}
		mu.Unlock()
		// never answers, as a classifier that is down or overloaded
		select {
		case <-r.Context().Done():
		case <-stop:
		}
	}))
	t.Cleanup(func() { close(stop) }) // runs before the server closes
	for _, g := range []provider.Group{
		{ID: "inner", Name: "Inner", Members: []string{"a/m"}, Routing: provider.Ordered,
			Classifier: "c/cls3", Rules: []provider.Rule{{Use: "a/m", Intent: "bug"}}},
		{ID: "mid", Name: "Mid", Members: []string{"group/inner"}, Routing: provider.Ordered,
			Classifier: "c/cls2", Rules: []provider.Rule{{Use: "group/inner", Intent: "bug"}}},
		{ID: "outer", Name: "Outer", Members: []string{"group/mid"}, Routing: provider.Ordered,
			Classifier: "c/cls1", Rules: []provider.Rule{{Use: "group/mid", Intent: "bug"}}},
	} {
		if err := provider.SaveGroup(g); err != nil {
			t.Fatal(err)
		}
	}
	s := New()
	t0 := time.Now()
	code, body := postAs(t, s, "nest1", `{"model":"group/outer","messages":[{"role":"user","content":"fix this crash"}]}`)
	took := time.Since(t0)
	if code != 200 || !strings.Contains(body, "from ka") {
		t.Fatalf("after %s: %d %s", took, code, body)
	}
	r := lastRoute(s)
	if r.Rule == nil || r.Rule.Classified == nil || !strings.Contains(r.Rule.Classified.Error, "no answer") {
		t.Fatalf("the outer group's classifier: %+v", r.Rule)
	}
	if len(r.Nested) != 2 {
		t.Fatalf("nested rules: %+v", r.Nested)
	}
	for _, n := range r.Nested {
		if n.Rule == nil || n.Rule.Classified == nil || !strings.Contains(n.Rule.Classified.Error, "spent") {
			t.Fatalf("%s's classifier, after the wait was spent above it: %+v", n.Group, n.Rule)
		}
	}
	mu.Lock()
	if asked["cls1"] == 0 {
		t.Errorf("the outer classifier wasn't asked: %v", asked)
	}
	mu.Unlock()
	// only the classifier given its whole wait rests for not answering
	classified.Lock()
	_, rest1 := classified.failed["c/cls1"]
	_, rest2 := classified.failed["c/cls2"]
	_, rest3 := classified.failed["c/cls3"]
	classified.Unlock()
	if !rest1 || rest2 || rest3 {
		t.Fatalf("resting: cls1 %v cls2 %v cls3 %v", rest1, rest2, rest3)
	}
}

// A decision model's key cut off by what a request had left of the
// classifier wait isn't rested for it: it was given less than its own
// wait, so the next request asks it first again.
func TestDecisionKeyCutShortIsNotRested(t *testing.T) {
	fresh(t)
	resetClassified(t)
	was := classifyUnit
	classifyUnit = 25 * time.Millisecond // the 8 seconds unset are 200ms
	t.Cleanup(func() { classifyUnit = was })
	var mu sync.Mutex
	var asked []string
	stop := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		mu.Unlock()
		io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-stop:
		}
	}))
	t.Cleanup(up.Close)
	t.Cleanup(func() { close(stop) })
	if err := provider.Save(provider.Provider{ID: "jw", Name: "JW", Key: "w1", Keys: []provider.KeyAccount{{Key: "w2"}}, Decide: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	s := New()
	_, err := s.askClassifierIn(50*time.Millisecond, access.Identity{}, "jw/jev-latest", []string{"feature", "bug"}, before{}, false, "fix this")
	if err == nil || !strings.Contains(err.Error(), "left of the request's classifier wait") {
		t.Fatalf("cut short: %v", err)
	}
	if _, err := s.askClassifier("jw/jev-latest", []string{"feature", "bug"}, before{}, false, "fix this"); err == nil {
		t.Fatal("a decision model that never answers answered")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(asked) != 2 || asked[0] != "w1" || asked[1] != "w1" {
		t.Fatalf("asked %v: the key cut short was rested", asked)
	}
}
