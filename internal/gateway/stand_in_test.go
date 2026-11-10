package gateway

import (
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// countOrDown counts the requests it is sent, and answers each, or with a
// 503 while down.
type countOrDown struct {
	n    atomic.Int32
	down bool
	name string
}

func (c *countOrDown) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	c.n.Add(1)
	w.Header().Set("Content-Type", "application/json")
	if c.down {
		w.WriteHeader(503)
		io.WriteString(w, `{"error":{"message":"kv cache full","type":"server_error"}}`)
		return
	}
	io.WriteString(w, `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"from `+c.name+`"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`)
}

// A provider set never to be a fallback for another model (NoStandIn,
// sonnnnnnnnnnnna on X) gets no request that went first to a provider not
// so set: a local uncensored model that is down or out of room, as a
// fallback, as a routing group's next member, or as a fallback group's
// member, its request is never sent on to the marked vendor, the first
// time or once the local one rests. A request that goes first to a marked
// provider still moves on to another marked one, and to an unmarked one.
func TestNoStandInTakesNoOtherModelsRequest(t *testing.T) {
	fresh(t)
	local := &countOrDown{down: true, name: "local"}
	cloud := &countOrDown{down: true, name: "cloud"}
	cloud2 := &countOrDown{name: "cloud2"}
	spare := &countOrDown{name: "spare"}
	serveOn(t, "local", "kl", []string{"l"}, local)
	serveOn(t, "cloud", "kc", []string{"c"}, cloud)
	serveOn(t, "cloud2", "kc2", []string{"c2"}, cloud2)
	serveOn(t, "spare", "ks", []string{"s"}, spare)
	setFallback := func(id string, to ...string) {
		t.Helper()
		p, err := provider.Find(id)
		if err != nil {
			t.Fatal(err)
		}
		p.Fallback = to
		if err := provider.Save(*p); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"cloud", "cloud2"} {
		if err := provider.SetNoStandIn(id, true); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SaveGroup(provider.Group{ID: "mixed", Name: "Mixed", Members: []string{"local/l", "cloud2/c2"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{ID: "marked", Name: "Marked", Members: []string{"cloud/c", "cloud2/c2"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	s := New()
	ask := func(model string) (int, string) {
		t.Helper()
		return postAs(t, s, "", `{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`)
	}

	// the local model's own fallbacks: the marked ones never asked, a
	// spare not marked still is
	setFallback("local", "cloud2/c2", "mixed", "spare/s")
	for range 2 { // the second time with local resting
		if code, body := ask("local/l"); code != 200 || !strings.Contains(body, "from spare") {
			t.Fatalf("local down: %d %s, want the spare", code, body)
		}
	}
	if n := cloud2.n.Load(); n != 0 {
		t.Fatalf("cloud2, marked Never a fallback, asked %d times", n)
	}
	r := s.trace.routes[len(s.trace.routes)-1]
	var told bool
	for _, w := range r.Left {
		told = told || w.StandIn && w.Provider == "cloud2"
	}
	if !told {
		t.Fatalf("trace's left says nothing of cloud2: %+v", r.Left)
	}

	// with no unmarked one left, the local model's failure is the answer
	setFallback("local", "cloud2/c2")
	if code, _ := ask("local/l"); code == 200 {
		t.Fatalf("local down, only a marked fallback: %d, want local's failure", code)
	}
	// a routing group with the local model first
	if code, _ := ask("mixed"); code == 200 || cloud2.n.Load() != 0 {
		t.Fatalf("mixed group: %d, cloud2 asked %d times", code, cloud2.n.Load())
	}

	// a request that went first to a marked provider goes on to another
	if code, body := ask("marked"); code != 200 || !strings.Contains(body, "from cloud2") {
		t.Fatalf("marked group: %d %s, want cloud2 after cloud", code, body)
	}
	setFallback("cloud", "cloud2/c2")
	before := cloud2.n.Load()
	if code, body := ask("cloud/c"); code != 200 || !strings.Contains(body, "from cloud2") || cloud2.n.Load() != before+1 {
		t.Fatalf("cloud down, falling back to cloud2: %d %s", code, body)
	}

	// unmarked again, cloud2 stands in for the local model as ever
	if err := provider.SetNoStandIn("cloud2", false); err != nil {
		t.Fatal(err)
	}
	if code, body := ask("local/l"); code != 200 || !strings.Contains(body, "from cloud2") {
		t.Fatalf("cloud2 not marked: %d %s", code, body)
	}
}
