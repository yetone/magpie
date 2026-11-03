package gateway

import (
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// Speed first weighs a seat by the pace usage.DecodeOf tells: a reply that
// came whole 255 ms after an 8.2 s first token (pluviophile714 on Discord,
// 1,539 tok/s from gpt-6.1-sol) gives its seat a first token, not a pace,
// so a relay that holds replies doesn't pass for the fastest.
func TestShortWindowAfterLongWaitNotesNoPace(t *testing.T) {
	forgetSpeeds()
	t.Cleanup(forgetSpeeds)
	c := candidate{p: provider.Provider{ID: "v"}, rest: "v", model: "gpt-6.1-sol"}
	t0 := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	noteTrySpeed(c, Try{Millis: 8455, TTFT: 8200, FirstText: 8200, Flow: 230}, 392, 0, t0)
	if ttft, tps := speedOf(c, t0); ttft != 8200 || tps != 0 {
		t.Fatalf("burst after a long wait: ttft %v, tps %v; want 8200, 0", ttft, tps)
	}
	// a streamed answer keeps its pace: 392 tokens over 4 s
	noteTrySpeed(c, Try{Millis: 12200, TTFT: 8200, FirstText: 8200, Flow: 4000}, 392, 0, t0)
	if _, tps := speedOf(c, t0); tps != 98 {
		t.Fatalf("streamed answer: tps %v; want 98", tps)
	}
}
