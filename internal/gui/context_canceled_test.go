package gui

import (
	"testing"
	"time"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/usage"
)

// TestContextLeavesCanceledOutOfErrors: a request its agent canceled — a
// person stopped the reply, or sent the next message while it came — is
// one of the agent's calls but no error it saw, so the Context page's
// error rate leaves it out. magpie's own limit, logged 499 too, stays one.
func TestContextLeavesCanceledOutOfErrors(t *testing.T) {
	var routes []gateway.Route
	for i, chat := range []int{10000, 20000, 30000} {
		routes = append(routes, ctxRoute(int64(i+1), "claude", "s1", 20000, chat, 200000, 1000))
	}
	at := time.Unix(1_790_000_000, 0)
	routes = append(routes,
		gateway.Route{ID: 10, Time: at, Agent: "claude", Done: true, Status: usage.StatusCanceled, Error: usage.AgentCanceled},
		gateway.Route{ID: 11, Time: at, Agent: "claude", Done: true, Status: usage.StatusCanceled, Error: usage.AgentCanceled},
		gateway.Route{ID: 12, Time: at, Agent: "claude", Done: true, Status: usage.StatusCanceled, Error: "magpie stopped waiting: its search had no answer in 2m0s"},
		gateway.Route{ID: 13, Time: at, Agent: "claude", Done: true, Status: 529, Error: "overloaded"},
	)
	a := contextOf(routes, 7).Agents[0]
	if a.Errors != 2 || a.Calls != 7 {
		t.Fatalf("errors %d calls %d, want 2 errors (magpie's limit and the 529) of 7 calls", a.Errors, a.Calls)
	}
}
