package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// advisedUsage is the usage of a reply whose advisor ran once, word for
// word Anthropic's example on its advisor tool page ("Usage and billing",
// platform.claude.com/docs/en/agents-and-tools/tool-use/advisor-tool):
// no real reply with an advisor call was captured. The top-level counts
// are the executor's alone; the advisor_message iteration is the
// advisor's, at its own model's rates (#6977).
const advisedUsage = `{"input_tokens":1760,"cache_read_input_tokens":412,"cache_creation_input_tokens":0,"output_tokens":531,"iterations":[{"type":"message","input_tokens":412,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"output_tokens":89},{"type":"advisor_message","model":"claude-opus-5-5","input_tokens":823,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"output_tokens":1612},{"type":"message","input_tokens":1348,"cache_read_input_tokens":412,"cache_creation_input_tokens":0,"output_tokens":442}]}`

// advisedReply is a reply to a turn whose advisor ran at the vendor, its
// usage advisedUsage: whole, or streamed with the iterations on its
// message_delta, as Anthropic's page says a stream tells them.
func advisedReply(stream bool) string {
	if !stream {
		return `{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-5-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":` + advisedUsage + `}`
	}
	return sse(
		`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-5-5","content":[],"usage":{"input_tokens":1760,"cache_read_input_tokens":412,"cache_creation_input_tokens":0,"output_tokens":1}}}`,
		`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
		`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`,
		`event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":`+advisedUsage+`}`,
		`event: message_stop`+"\n"+`data: {"type":"message_stop"}`)
}

// The usage a reply's advisor calls ran up is read beside the executor's,
// whole and streamed, and the executor's counts are the top-level ones as
// they came: nothing is taken from them or added to them.
func TestAdvisorIterationsRead(t *testing.T) {
	for _, stream := range []bool{false, true} {
		ct := "application/json"
		if stream {
			ct = "text/event-stream"
		}
		s := newSniffer(provider.Anthropic, ct)
		s.write([]byte(advisedReply(stream)))
		u := s.usage()
		if u.Input != 1760 || u.Output != 531 || u.CacheRead != 412 || u.CacheWrite != 0 {
			t.Errorf("stream=%v: the executor's usage %+v, want 1760 in, 531 out, 412 read", stream, u)
		}
		if u.advised == nil || len(u.advised.calls) != 1 {
			t.Fatalf("stream=%v: advisor calls %+v, want one", stream, u.advised)
		}
		c := u.advised.calls[0]
		if c.served != "claude-opus-5-5" || c.use.Input != 823 || c.use.Output != 1612 || c.use.CacheRead+c.use.CacheWrite != 0 {
			t.Errorf("stream=%v: the advisor's call %+v", stream, c)
		}
	}
	// a stream's later message_delta lists the earlier calls again: it
	// stands for the reply's, never added to the one before
	var u Usage
	for range 2 {
		var a aUsage
		if err := json.Unmarshal([]byte(advisedUsage), &a); err != nil {
			t.Fatal(err)
		}
		u.add(a.usage())
	}
	if len(u.advised.calls) != 1 {
		t.Fatalf("the same iterations told twice made %d calls", len(u.advised.calls))
	}
	// iterations with no advisor call in them (Copilot's message_delta)
	// leave the usage as it was
	var a aUsage
	if err := json.Unmarshal([]byte(`{"input_tokens":5,"output_tokens":2,"iterations":[{"type":"message","input_tokens":5,"output_tokens":2}]}`), &a); err != nil {
		t.Fatal(err)
	}
	if got := a.usage(); got != (Usage{Input: 5, Output: 2}) {
		t.Fatalf("no advisor call: %+v", got)
	}
}

// A Claude Code turn with its advisor on, sent as it is to a provider's
// Messages API, where Anthropic's server runs the advisor: its call is a
// usage row of its own under the advisor's model, by magpie's id (here
// claude-opus-5.5, which Claude Code names claude-opus-5-5 and the
// iteration names as the vendor does), priced as that model, and the
// request's own row is the executor's usage as the reply said it (Refeccd
// on Discord: the advisor's Opus tokens were in no row). The Routing page
// prices it too.
func TestAdvisorCallHasItsOwnRow(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
				} else {
					w.Header().Set("Content-Type", "application/json")
				}
				io.WriteString(w, advisedReply(stream))
			}))
			t.Cleanup(srv.Close)
			if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Anthropic: srv.URL, Models: []string{"claude-opus-5.5", "claude-sonnet-5-5"}}); err != nil {
				t.Fatal(err)
			}
			body := advisorTurn("relay/claude-sonnet-5-5", "relay/claude-opus-5-5")
			if !stream {
				body = strings.Replace(body, `"stream":true`, `"stream":false`, 1)
			}
			s := New()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/v1/messages?beta=true", strings.NewReader(body))
			req.Header.Set("User-Agent", "claude-cli/2.1.300 (external, cli)")
			req.Header.Set("Authorization", "Bearer "+Token)
			s.Handler().ServeHTTP(rec, req)
			if rec.Code != 200 {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
			route := lastRoute(s)
			var rows []usage.Record
			deadline := time.Now().Add(10 * time.Second)
			for {
				rows = rows[:0]
				for _, u := range usage.Load(time.Time{}) {
					if u.RouteID == route.ID {
						rows = append(rows, u)
					}
				}
				if len(rows) >= 2 || time.Now().After(deadline) {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			var own, advised []usage.Record
			for _, r := range rows {
				if r.Kind == usage.KindAdvisor {
					advised = append(advised, r)
				} else {
					own = append(own, r)
				}
			}
			if len(own) != 1 || own[0].Model != "claude-sonnet-5-5" || own[0].Input != 1760 || own[0].Output != 531 || own[0].CacheRead != 412 {
				t.Fatalf("the request's own row: %+v", own)
			}
			if len(advised) != 1 {
				t.Fatalf("advisor rows: %+v (every row %+v)", advised, rows)
			}
			a := advised[0]
			if a.Provider != "relay" || a.Model != "claude-opus-5.5" || a.Served != "claude-opus-5-5" || a.Input != 823 || a.Output != 1612 ||
				a.CacheRead+a.CacheWrite != 0 || a.Status != 200 || a.HandedOn || a.Session != own[0].Session || a.Agent != own[0].Agent {
				t.Fatalf("the advisor's row: %+v", a)
			}
			var models []string
			for _, u := range route.Usage {
				models = append(models, fmt.Sprintf("%s:%d/%d", u.Model, u.Input, u.Output))
			}
			if got := strings.Join(models, ","); got != "claude-opus-5.5:823/1612,claude-sonnet-5-5:1760/531" {
				t.Fatalf("Routing's usage %s", got)
			}
			if promptCounted(route.Usage) != 1760+412 {
				t.Fatalf("the prompt read from the advisor's usage: %d", promptCounted(route.Usage))
			}
		})
	}
}
