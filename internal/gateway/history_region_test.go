package gateway

import "testing"

// A route kept with a try marked swapped for WorkBuddy AI's
// deepseek-v4.1-flash-sg answered as deepseek-v4.1-flash (the Singapore
// deployment's id and the model's own name, plugins#628) is read as not
// swapped, so the Routing page's "served" tag goes from old routes too;
// deepseek-v4.1-flash-sg answered by deepseek-v4.1-pro stays swapped.
func TestRouteKeptRegionTailNotSwapped(t *testing.T) {
	r := &Route{Swapped: true, Served: "deepseek-v4.1-flash", Tries: []Try{{ID: "workbuddy-ai", Model: "deepseek-v4.1-flash-sg", Served: "deepseek-v4.1-flash", Swapped: true}}}
	r.routedAgain()
	if r.Swapped || r.Tries[0].Swapped {
		t.Fatalf("still swapped: %+v", r)
	}
	r = &Route{Swapped: true, Served: "deepseek-v4.1-pro", Tries: []Try{{ID: "workbuddy-ai", Model: "deepseek-v4.1-flash-sg", Served: "deepseek-v4.1-pro", Swapped: true}}}
	r.routedAgain()
	if !r.Swapped || !r.Tries[0].Swapped {
		t.Fatalf("a real swap was cleared: %+v", r)
	}
}
