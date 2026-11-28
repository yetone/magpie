package gateway

import (
	"net/http"
	"testing"
	"time"
)

// A turn counts as running a tool from the reply that asked for one until
// its next reply that called none, or toolRunMax after it: an agent closed
// while its tool ran never says so. A failed reply, or one cut short, ends
// it too (the agent asks again, a request in flight, or stops); each
// session is its own, and a request made on the side is none of them.
func TestToolRunsEnd(t *testing.T) {
	var tr toolRuns
	now := time.Now()
	asked := func() *toolScan { return &toolScan{saw: true, ended: true} }
	route := func(session string, tools *toolScan) *Route {
		return &Route{turn: true, Agent: "claude", Session: session, Status: http.StatusOK, tools: tools}
	}
	tr.end(route("a", asked()), now)
	if !tr.any(now.Add(toolRunMax - time.Minute)) {
		t.Fatal("a tool running for less than toolRunMax")
	}
	if tr.any(now.Add(toolRunMax + time.Minute)) {
		t.Fatal("a turn left running a tool counted past toolRunMax")
	}

	tr.end(route("a", asked()), now)
	tr.end(route("b", asked()), now)
	tr.end(route("a", &toolScan{ended: true}), now)
	if !tr.any(now) {
		t.Fatal("session a's end ended session b's tool run")
	}
	failed := route("b", asked())
	failed.Status = http.StatusBadGateway
	tr.end(failed, now)
	if tr.any(now) {
		t.Fatal("a failed reply left the turn running a tool")
	}

	tr.end(route("c", &toolScan{saw: true, cut: true, ended: true}), now)
	if tr.any(now) {
		t.Fatal("a reply cut short counted as asking for a tool")
	}
	side := route("d", asked())
	side.turn = false
	tr.end(side, now)
	if tr.any(now) {
		t.Fatal("a request made on the side counted as the agent's turn")
	}
}
