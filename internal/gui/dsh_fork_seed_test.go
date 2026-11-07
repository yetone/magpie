package gui

import (
	"testing"
	"time"

	"github.com/yetone/magpie/internal/sessions"
)

// dsh writes no parentSession for a fork it makes today, so sessions' ForkOf
// is empty for it: the opening its seed carried (SeedConvs) is what says the
// fork came from the session it copied, and its own replies are named for it
// rather than for that session. A reply id of another conversation still goes
// by that conversation's opening, as the guard for vendors' repeated ids asks.
func TestDshForkWithoutAParentIsNamedByItsReply(t *testing.T) {
	list := []sessions.Session{
		{Agent: "dsh", ID: "source", Convs: []string{"magpie-open"}},
		{Agent: "dsh", ID: "fork", SeedConvs: []string{"magpie-open"}, ResponseIDs: []string{"chatcmpl-fork"}},
		{Agent: "dsh", ID: "other", Convs: []string{"magpie-other"}, ResponseIDs: []string{"chatcmpl-other"}},
	}
	of := routeOwners(list)
	for _, c := range []struct {
		response, conv, want string
	}{
		{"chatcmpl-fork", "magpie-open", "fork"},    // the fork's own turn
		{"", "magpie-open", "source"},               // the source's, by its opening
		{"chatcmpl-other", "magpie-open", "source"}, // another conversation's reply id
		{"chatcmpl-fork", "", "fork"},               // the reply alone
	} {
		if got := ownerOf("dsh", c.response, c.conv, of); got != c.want {
			t.Errorf("reply %q, opening %q: %q, want %q", c.response, c.conv, got, c.want)
		}
	}
}

// A fork's own request opens as its source's conversation does, and the reply
// id that names it is read from the fork's file a moment after the gateway
// ended the request: a call whose reply names no session yet is neither the
// source's nor the fork's, and the tab leaves it out of both rather than
// counting it under the source. A call without a reply id — dsh's history
// before #5491 recorded them — goes by its opening, the source's, as it always
// did, and so does one old enough that no reply is coming: the source's card
// keeps its history. The read after the reply is written names the fork.
func TestDshForkRouteAwaitingItsReplyIsNeithers(t *testing.T) {
	source := sessions.Session{Agent: "dsh", ID: "source", Convs: []string{"magpie-open"}}
	fork := sessions.Session{Agent: "dsh", ID: "fork", SeedConvs: []string{"magpie-open"}}
	list := []sessions.Session{source, fork}
	now := time.Now()
	routes := []ctxRec{
		{Agent: "dsh", Conv: "magpie-open", ResponseID: "chatcmpl-fork", Done: true, Time: now}, // the fork's turn, its reply unread
		{Agent: "dsh", Conv: "magpie-open", Done: true, Time: now},                              // no reply id: its opening's
		{Agent: "dsh", Conv: "magpie-open", ResponseID: "chatcmpl-gone", Done: true,
			Time: now.Add(-2 * time.Minute)}, // older than convsKept: no reply is coming
		{Agent: "dsh", Conv: "magpie-open", ResponseID: "chatcmpl-long", Done: true,
			Time: now.Add(-2 * time.Minute), Millis: 100_000}, // begun before the window, ended within it
	}
	joinConvs(routes, func() []sessions.Session { return list })
	if routes[0].Session != "" || !routes[0].Unmatched {
		t.Errorf("the fork's turn, its reply unread: session %q, unmatched %v; want neither's", routes[0].Session, routes[0].Unmatched)
	}
	if routes[3].Session != "" || !routes[3].Unmatched {
		t.Errorf("a long call that ended 20s ago: session %q, unmatched %v; want neither's", routes[3].Session, routes[3].Unmatched)
	}
	for _, i := range []int{1, 2} {
		if routes[i].Session != "source" || routes[i].Unmatched {
			t.Errorf("route %d: session %q, unmatched %v; want the opening's, the source's", i, routes[i].Session, routes[i].Unmatched)
		}
	}
	list[1].ResponseIDs = []string{"chatcmpl-fork"}
	for i := range routes {
		routes[i].Session, routes[i].Unmatched = "", false
	}
	joinConvs(routes, func() []sessions.Session { return list })
	if routes[0].Session != "fork" || routes[0].Unmatched {
		t.Errorf("the fork's own turn: session %q, unmatched %v; want the fork", routes[0].Session, routes[0].Unmatched)
	}
}
