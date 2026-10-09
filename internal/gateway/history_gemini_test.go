package gateway

import "testing"

// A route kept with a try marked swapped for Antigravity's
// gemini-3.8-flash answered as gemini-3.8-flash-n (Google's name for the
// deployment serving it, dumplings on Discord) is read as not swapped;
// gpt-5 answered by gpt-5-mini stays swapped.
func TestRouteKeptGeminiServingNameNotSwapped(t *testing.T) {
	r := &Route{Swapped: true, Served: "gemini-3.8-flash-n", Tries: []Try{{Model: "gemini-3.8-flash", Served: "gemini-3.8-flash-n", Swapped: true}}}
	r.routedAgain()
	if r.Swapped || r.Tries[0].Swapped {
		t.Fatalf("still swapped: %+v", r)
	}
	r = &Route{Swapped: true, Served: "gpt-5-mini", Tries: []Try{{Model: "gpt-5", Served: "gpt-5-mini", Swapped: true}}}
	r.routedAgain()
	if !r.Swapped || !r.Tries[0].Swapped {
		t.Fatalf("a real swap was cleared: %+v", r)
	}
}
