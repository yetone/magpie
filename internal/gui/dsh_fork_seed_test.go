package gui

import (
	"testing"

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
