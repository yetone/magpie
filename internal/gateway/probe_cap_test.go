package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// Open Design's glm-5.3-flash, as plugins#827's log has it: a reply that
// thinks, closes its block and ends, with no message_delta or
// message_stop, when the model test's 16 tokens are spent.
func probeCapStream(model string) []string {
	return []string{
		anthStart(model, 5),
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		anthThink("The user says hi. I should"),
		`{"type":"content_block_stop","index":0}`,
	}
}

func probeCapAsk(maxTokens string, stream bool) string {
	s := "false"
	if stream {
		s = "true"
	}
	return `{"model":"up/glm-5.3-flash","max_tokens":` + maxTokens + `,"stream":` + s + `,"messages":[{"role":"user","content":"hi"}]}`
}

// A short probe's reply that thought its whole small budget and ended
// with no end said ended at its length, whole or streamed (plugins#827:
// every plain test of such a model was a 502 "ended before it was
// complete", waited out through the patience). A reply asked for more
// that ends so is still a cut, and so is one that spent nothing.
func TestProbeCapEndsAtLength(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := map[bool]string{false: "whole", true: "stream"}[stream]
		t.Run(name, func(t *testing.T) {
			up := func(events ...string) {
				fresh(t)
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					for _, ev := range events {
						w.Write([]byte("data: " + ev + "\n\n"))
					}
				}))
				t.Cleanup(srv.Close)
				if err := provider.Save(provider.Provider{ID: "up", Name: "UP", Key: "k", Models: []string{"glm-5.3-flash"}, Anthropic: srv.URL}); err != nil {
					t.Fatal(err)
				}
			}
			up(probeCapStream("glm-5.3-flash")...)
			code, body := post(t, "/v1/chat/completions", probeCapAsk("16", stream))
			if code != 200 || strings.Contains(body, "ended before") || !strings.Contains(body, `"finish_reason":"length"`) {
				t.Errorf("probe's reply at its cap: status %d: %s", code, body)
			}
			// asked for more than a probe's worth: a cut
			code, body = post(t, "/v1/chat/completions", probeCapAsk("4096", stream))
			if !strings.Contains(body, "ended before it was complete") || (!stream && code != 502) {
				t.Errorf("a long reply cut short served as ended (status %d): %s", code, body)
			}
			// nothing spent: a cut, whatever its cap
			up(anthStart("glm-5.3-flash", 5))
			code, body = post(t, "/v1/chat/completions", probeCapAsk("16", stream))
			if !strings.Contains(body, "ended before it was complete") || (!stream && code != 502) {
				t.Errorf("a reply that spent nothing served as ended (status %d): %s", code, body)
			}
		})
	}
}

// A Responses plugin's stream asked whole, capped at a probe's worth and
// ended with no response.completed after spending some of it, is the
// response incomplete at max_output_tokens, not a 502; one capped past a
// probe's worth is still the 502.
func TestGatheredProbeCapIncomplete(t *testing.T) {
	stream := sse(
		`data: {"type":"response.created","response":{"id":"r1","object":"response","model":"m","status":"in_progress"}}`,
		`data: {"type":"response.reasoning_summary_text.delta","delta":"The user"}`)
	for _, c := range []struct {
		capped int
		code   int
	}{{16, 200}, {4096, 502}, {0, 502}} {
		g := &gathered{h: http.Header{}, status: 200, capped: c.capped}
		g.h.Set("Content-Type", "text/event-stream")
		g.Write([]byte(stream))
		rec := httptest.NewRecorder()
		code, _ := g.finish(rec, provider.Provider{Name: "p"}, true, "", "")
		if code != c.code {
			t.Errorf("capped %d: status %d, want %d: %s", c.capped, code, c.code, rec.Body)
			continue
		}
		if code == 200 && (!strings.Contains(rec.Body.String(), `"status":"incomplete"`) || !strings.Contains(rec.Body.String(), `"reason":"max_output_tokens"`)) {
			t.Errorf("capped %d: %s", c.capped, rec.Body)
		}
	}
	// a stream that spent nothing is still the 502
	g := &gathered{h: http.Header{}, status: 200, capped: 16}
	g.h.Set("Content-Type", "text/event-stream")
	g.Write([]byte(sse(`data: {"type":"response.created","response":{"id":"r1","object":"response","status":"in_progress"}}`)))
	rec := httptest.NewRecorder()
	if code, _ := g.finish(rec, provider.Provider{Name: "p"}, true, "", ""); code != 502 {
		t.Errorf("nothing spent: status %d: %s", code, rec.Body)
	}
}

// A client that says how long it waits (provider.WaitHeader: a model test
// asking a remote magpie) is told a busy failure while it still waits:
// no retry pause or patience round is taken that would end past it
// (plugins#827: the test waited 20s, the server held its 502 through the
// provider's patience, and the test said only that nothing answered).
// A client that said nothing, or a wait long enough, is retried as
// before.
func TestClientWaitBoundsPatience(t *testing.T) {
	const busy = `{"type":"error","error":{"type":"api_error","message":"upstream went away"}}`
	for _, c := range []struct {
		name  string
		wait  string
		retry bool
	}{{"short wait", "1", false}, {"no wait said", "", true}, {"unreadable", "soon", true}, {"long wait", "3600", true}} {
		t.Run(c.name, func(t *testing.T) {
			u := &busyUpstream{replies: []busyReply{{code: 502, body: busy}}}
			s := busyProvider(t, u)
			scaled(t, 300*time.Millisecond)
			gw := httptest.NewServer(s.Handler())
			t.Cleanup(gw.Close)
			req, _ := http.NewRequest("POST", gw.URL+"/v1/messages", strings.NewReader(singleAsk(false)))
			req.Header.Set("Content-Type", "application/json")
			if c.wait != "" {
				req.Header.Set(provider.WaitHeader, c.wait)
			}
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			res.Body.Close()
			if res.StatusCode != 502 {
				t.Errorf("status %d, want the 502", res.StatusCode)
			}
			if n := u.asked(); (n > 1) != c.retry {
				t.Errorf("upstream asked %d times, retried want %v", n, c.retry)
			}
		})
	}
}
