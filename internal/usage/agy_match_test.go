package usage

import (
	"testing"
	"time"

	"github.com/yetone/magpie/internal/sessions"
)

// #2097: an agy step that went through magpie is counted once. agy 1.2.16,
// run in a sandbox through a fake Gemini endpoint, sent no session header
// (only User-Agent google-genai-sdk/…, X-Goog-Api-Key), so the gateway's
// record names no session; its transcript's step began at 06:12:41Z, to
// the second, and counted input 702, cache read 300, output 9. A long step
// ends well past the 2 s every other session file's end time is held to.
func TestAgyStepThroughMagpieCountedOnce(t *testing.T) {
	began := time.Date(2026, 10, 14, 6, 12, 41, 0, time.UTC)
	rec := Record{Time: began.Add(990 * time.Millisecond), Agent: "agy", Provider: "relay", Model: "m",
		Input: 702, CacheRead: 300, Output: 9, Millis: 95_000, Status: 200}
	step := sessions.Call{Time: began, Agent: "agy", Session: "8ac63d53-9162-4a29-af29-bda3428931ae",
		Tokens: sessions.Tokens{Input: 702, CacheRead: 300, Output: 9}}
	direct := sessions.Call{Time: began.Add(10 * time.Minute), Agent: "agy", Session: step.Session,
		Tokens: sessions.Tokens{Input: 3013, CacheRead: 14973, Output: 1518}}

	m := gatewayMatches([]Record{rec}, []sessions.Call{step, direct})
	if !m[0] {
		t.Error("the step the gateway logged is counted again as agy's own call")
	}
	if m[1] {
		t.Error("a step agy made without magpie was taken for the gateway's")
	}

	// a step the gateway's call ended before is not that call
	early := step
	early.Time = rec.Time.Add(time.Duration(rec.Millis)*time.Millisecond + 3*time.Second)
	if gatewayMatches([]Record{rec}, []sessions.Call{early})[0] {
		t.Error("a step begun after the gateway's call ended was matched to it")
	}
	// another agent's session-less record is no agy step's
	other := rec
	other.Agent = "codex"
	if gatewayMatches([]Record{other}, []sessions.Call{step})[0] {
		t.Error("an agy step matched another agent's record")
	}
}

// An agy not connected on its own row follows the Antigravity app's
// gateway, with the app's key, so the gateway records its calls as the
// app's: Chamberlain_zyz on X saw each hour's tokens under both Antigravity
// (39,104,506) and Antigravity CLI (38,562,861). Its steps are matched to
// the app's records, which name no session, as to agy's own; an app record
// that names a session is not one.
func TestAgyStepThroughTheAppsGatewayCountedOnce(t *testing.T) {
	began := time.Date(2026, 11, 2, 9, 0, 12, 0, time.UTC)
	rec := Record{Time: began.Add(4 * time.Second), Agent: "antigravity", Provider: "deepseek", Model: "deepseek-v4.1-flash",
		Input: 41210, CacheRead: 96000, Output: 812, Millis: 6_000, Status: 200}
	step := sessions.Call{Time: began, Agent: "agy", Session: "5c1f0e1a-7b9d-4c51-9a8e-0d3b2f6a1e44",
		Tokens: sessions.Tokens{Input: 41210, CacheRead: 96000, Output: 812}}
	if !gatewayMatches([]Record{rec}, []sessions.Call{step})[0] {
		t.Error("an agy step sent through the app's gateway is counted again as agy's own call")
	}
	named := rec
	named.Session = "app-conversation"
	if gatewayMatches([]Record{named}, []sessions.Call{step})[0] {
		t.Error("an app record naming its own session was taken for an agy step")
	}
}

// #2190: the Antigravity app's own conversation database counts every
// call the app made, the ones it sent through magpie too, and names the
// conversation's id, never a session the gateway was sent. A call that
// went through magpie is the gateway's record, matched by its tokens and
// a time after the step began (step 11 of yygutn's rows on #2190: 4254 in,
// 18561 read from the cache, 276 out); one made straight to Google is the
// app's own.
func TestAntigravityAppCallThroughMagpieCountedOnce(t *testing.T) {
	began := time.Unix(1791860353, 992204000)
	rec := Record{Time: began.Add(11 * time.Second), Agent: "antigravity", Provider: "antigravity", Model: "gemini-3.8-flash",
		Input: 4254, CacheRead: 18561, Output: 276, Millis: 10_500, Status: 200}
	step := sessions.Call{Time: began, Agent: "antigravity", Session: "37c3c786-9a11-44be-97df-b8fc1ccebc78",
		Model: "gemini-3.8-flash-n", Tokens: sessions.Tokens{Input: 4254, CacheRead: 18561, Output: 276}}
	direct := sessions.Call{Time: began.Add(-35 * time.Second), Agent: "antigravity", Session: step.Session,
		Model: "gemini-3.8-flash-n", Tokens: sessions.Tokens{Input: 13317, Output: 621}}
	m := gatewayMatches([]Record{rec}, []sessions.Call{step, direct})
	if !m[0] {
		t.Error("an app call the gateway logged is counted again as the app's own")
	}
	if m[1] {
		t.Error("an app call made straight to Google was taken for the gateway's")
	}
}
