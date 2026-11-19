package gateway

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A group routed Small context first sends a request to the member with
// the smallest context window that holds it (yg6888 on X: Cursor's
// Composer at 200K and Grok 4.7 at 256K never answered in a group of 1M
// models under Smart). The 1M member is listed first, so Smart sends it
// everything; routed fit a short request goes to the small one, and one
// longer than the small one's window to the big one, the small one left
// out as too small. The trace tells each row's window. Smart on the same
// group still goes to the listed first.
func TestSmallContextFirstGroupKeepsTheBigOneForLongRequests(t *testing.T) {
	fresh(t)
	up := httptest.NewServer(http.HandlerFunc(streamed))
	t.Cleanup(up.Close)
	to, _ := url.Parse(up.URL)
	for _, p := range []provider.Provider{
		{ID: "big", Name: "Big", Key: "sk-b", Chat: "https://big.example/v1", Models: []string{"m"}, Contexts: map[string]int{"m": 1_000_000}},
		{ID: "small", Name: "Small", Key: "sk-s", Chat: "https://small.example/v1", Models: []string{"m"}, Contexts: map[string]int{"m": 4_000}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	group := func(routing string) {
		t.Helper()
		if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"big/m", "small/m"}, Routing: routing}); err != nil {
			t.Fatal(err)
		}
	}
	s := New()
	s.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r.URL.Scheme, r.URL.Host = to.Scheme, to.Host
		return http.DefaultTransport.RoundTrip(r)
	})}
	ask := func(text string) *Route {
		t.Helper()
		code, body := postAs(t, s, "", `{"model":"group/g","stream":true,"messages":[{"role":"user","content":"`+text+`"}]}`)
		if code != 200 {
			t.Fatalf("code %d: %s", code, body)
		}
		return s.trace.routes[len(s.trace.routes)-1]
	}
	long := strings.Repeat("lorem ipsum dolor sit amet ", 2000) // ~54K characters, past 4K tokens

	group(provider.SmallFirst)
	if g, _, ok := provider.FindGroup("group/g"); !ok || g.Routing != provider.SmallFirst {
		t.Fatalf("the group didn't keep Small context first: %+v", g)
	}
	short := ask("hi")
	if short.Order[0].Provider != "small" {
		t.Fatalf("a short request went to %q first, want small: %+v", short.Order[0].Provider, short.Order)
	}
	if short.Order[0].Context != 4_000 {
		t.Fatalf("the small row doesn't tell its window: %+v", short.Order[0])
	}
	if len(short.Tries) != 1 || short.Tries[0].ID != short.Order[0].ID {
		t.Fatalf("a short request wasn't served by the small one alone: %+v", short.Tries)
	}
	big := ask(long)
	if big.Order[0].Provider != "big" {
		t.Fatalf("a long request went to %q first, want big: %+v", big.Order[0].Provider, big.Order)
	}
	for _, w := range big.Order {
		if w.Provider == "small" && w.Window != 4_000 {
			t.Fatalf("the small one wasn't left out as too small for the long request: %+v", w)
		}
	}

	// Smart on the same group: the listed first, and no row tells a window
	group("")
	smart := ask("hi")
	if smart.Order[0].Provider != "big" {
		t.Fatalf("Smart went to %q first, want the listed first: %+v", smart.Order[0].Provider, smart.Order)
	}
	for _, w := range smart.Order {
		if w.Context != 0 {
			t.Fatalf("a Smart row tells a window: %+v", w)
		}
	}
}

// Small context first is a group's: a provider saved with it keeps Smart.
func TestSmallContextFirstIsAGroupsOnly(t *testing.T) {
	fresh(t)
	if err := provider.Save(provider.Provider{ID: "rl", Name: "Relay", Key: "k", Chat: "https://relay.example/v1", Models: []string{"m"}, Routing: provider.SmallFirst}); err != nil {
		t.Fatal(err)
	}
	if p, err := provider.Find("rl"); err != nil || p.Routing != "" {
		t.Fatalf("a provider kept routing fit, want Smart: %v %+v", err, p)
	}
}
