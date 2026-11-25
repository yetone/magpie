package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// phases is each assistant message's phase, in order, as the stream's
// output_item.done events gave them, then as its last event's output did.
func phases(body string) (streamed, ended []string) {
	for _, ev := range events(body) {
		switch ev["type"] {
		case "response.output_item.done":
			if it, _ := ev["item"].(map[string]any); it["type"] == "message" {
				p, _ := it["phase"].(string)
				streamed = append(streamed, p)
			}
		case "response.completed", "response.incomplete", "response.failed":
			res, _ := ev["response"].(map[string]any)
			out, _ := res["output"].([]any)
			for _, o := range out {
				if it, _ := o.(map[string]any); it["type"] == "message" {
					p, _ := it["phase"].(string)
					ended = append(ended, p)
				}
			}
		}
	}
	return
}

func encoded(evs ...Event) string {
	rec := httptest.NewRecorder()
	enc := encoder(provider.Responses, newSSEWriter(rec), &Request{Model: "m"})
	for _, ev := range evs {
		enc.event(ev)
	}
	enc.finish()
	return rec.Body.String()
}

// #2437: a translated model's messages say their phase, as OpenAI's do, so
// Codex folds what was said on the way to a call under "Worked for …" and
// shows the answer apart.
func TestTranslatedMessagesSayPhase(t *testing.T) {
	for _, tc := range []struct {
		name string
		evs  []Event
		want string
	}{
		{"said before a call", []Event{{Kind: KText, Text: "Let me look."}, {Kind: KToolStart, ID: "c1", Name: "shell"}, {Kind: KToolArgs, Text: `{}`}, {Kind: KStop, Stop: "tool"}}, "commentary"},
		{"said before reasoning and a call", []Event{{Kind: KText, Text: "Let me look."}, {Kind: KThink, Text: "hm"}, {Kind: KToolStart, ID: "c1", Name: "shell"}, {Kind: KToolArgs, Text: `{}`}, {Kind: KStop, Stop: "tool"}}, "commentary"},
		{"said last", []Event{{Kind: KText, Text: "Done."}, {Kind: KStop, Stop: "stop"}}, "final_answer"},
		{"said last, reasoning after", []Event{{Kind: KText, Text: "Done."}, {Kind: KThink, Text: "hm"}, {Kind: KStop, Stop: "stop"}}, "final_answer"},
		{"cut at the length", []Event{{Kind: KText, Text: "Done"}, {Kind: KStop, Stop: "length"}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := encoded(tc.evs...)
			streamed, ended := phases(body)
			if len(streamed) != 1 || streamed[0] != tc.want {
				t.Fatalf("streamed phases %q, want %q\n%s", streamed, tc.want, body)
			}
			if len(ended) != 1 || ended[0] != tc.want {
				t.Fatalf("ended phases %q, want %q\n%s", ended, tc.want, body)
			}
		})
	}
}

// Two messages around a call: the first is commentary, the last the answer.
func TestTranslatedMessagesAroundACall(t *testing.T) {
	body := encoded(
		Event{Kind: KText, Text: "Checking."},
		Event{Kind: KToolStart, ID: "c1", Name: "shell"},
		Event{Kind: KToolArgs, Text: `{}`},
		Event{Kind: KText, Text: "Found it."},
		Event{Kind: KStop, Stop: "stop"},
	)
	if streamed, ended := phases(body); strings.Join(streamed, ",") != "commentary,final_answer" || strings.Join(ended, ",") != "commentary,final_answer" {
		t.Fatalf("streamed %q, ended %q\n%s", streamed, ended, body)
	}
}

// A keepalive while a message waits for its phase goes out at once, and
// sequence numbers rise in the order events are written.
func TestHeldMessageKeepsSequence(t *testing.T) {
	body := encoded(
		Event{Kind: KText, Text: "Checking."},
		Event{Kind: KAlive},
		Event{Kind: KToolStart, ID: "c1", Name: "shell"},
		Event{Kind: KToolArgs, Text: `{}`},
	)
	last := -1.0
	for _, ev := range events(body) {
		n, ok := ev["sequence_number"].(float64)
		if !ok {
			continue
		}
		if n <= last {
			t.Fatalf("sequence %v after %v\n%s", n, last, body)
		}
		last = n
	}
	if streamed, _ := phases(body); strings.Join(streamed, ",") != "commentary" {
		t.Fatalf("streamed %q", streamed)
	}
}

func TestRenderedMessagesSayPhase(t *testing.T) {
	res := Result{Stop: "tool", Parts: []Part{
		{Kind: Text, Text: "Checking."},
		{Kind: ToolCall, ID: "c1", Name: "shell", Args: json.RawMessage(`{}`)},
	}}
	var out struct {
		Output []map[string]any `json:"output"`
	}
	if err := json.Unmarshal(renderResponses(res, "m", nil), &out); err != nil {
		t.Fatal(err)
	}
	if out.Output[0]["phase"] != "commentary" {
		t.Fatalf("output %v", out.Output)
	}
	res = Result{Stop: "stop", Parts: []Part{{Kind: Text, Text: "Done."}}}
	out.Output = nil
	if err := json.Unmarshal(renderResponses(res, "m", nil), &out); err != nil {
		t.Fatal(err)
	}
	if out.Output[0]["phase"] != "final_answer" {
		t.Fatalf("output %v", out.Output)
	}
}

func tidied(t *testing.T, sse bool, chunks ...string) string {
	t.Helper()
	p := &phaseTidy{sse: sse}
	var out []byte
	for _, c := range chunks {
		// a byte at a time, as an upstream may cut it
		for i := range len(c) {
			out = append(out, p.write([]byte{c[i]})...)
		}
	}
	return string(append(out, p.flush()...))
}

const (
	evMsg   = `data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"m1","role":"assistant","content":[{"type":"output_text","text":"Checking."}]}}`
	evRsAdd = `data: {"type":"response.output_item.added","output_index":1,"item":{"type":"reasoning","id":"rs1"}}`
	evRs    = `data: {"type":"response.output_item.done","output_index":1,"item":{"type":"reasoning","id":"rs1","summary":[]}}`
	evFcAdd = `data: {"type":"response.output_item.added","output_index":2,"item":{"type":"function_call","id":"fc1","call_id":"c1","name":"shell","arguments":""}}`
	evDone  = `data: {"type":"response.completed","response":{"id":"r","status":"completed","output":[{"type":"message","id":"m1","role":"assistant","content":[]}],"usage":{"input_tokens":5,"output_tokens":2}}}`
	evCut   = `data: {"type":"response.incomplete","response":{"id":"r","status":"incomplete","output":[{"type":"message","id":"m1","role":"assistant","content":[]}]}}`
)

// A Responses upstream relayed as it is: its messages with no phase get
// one, by the same rule; one it gave a phase keeps it.
func TestRelayedMessagesSayPhase(t *testing.T) {
	for _, tc := range []struct {
		name          string
		chunks        []string
		streamed, end string
	}{
		{"before a call", []string{sse(evMsg, evRsAdd, evRs, evFcAdd, evDone)}, "commentary", "final_answer"},
		{"last, reasoning after", []string{sse(evMsg, evRsAdd, evRs, evDone)}, "final_answer", "final_answer"},
		{"cut short", []string{sse(evMsg, evCut)}, "", ""},
		{"stream ends without an end", []string{sse(evMsg, evRsAdd)}, "", ""},
		{"upstream's own phase", []string{sse(strings.Replace(evMsg, `"role":"assistant"`, `"role":"assistant","phase":"commentary"`, 1), strings.Replace(evDone, `"role":"assistant"`, `"role":"assistant","phase":"commentary"`, 1))}, "commentary", "commentary"},
		{"CRLF", []string{strings.ReplaceAll(sse(evMsg, evFcAdd, evDone), "\n", "\r\n")}, "commentary", "final_answer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := tidied(t, true, tc.chunks...)
			streamed, ended := phases(strings.ReplaceAll(body, "\r\n", "\n"))
			if strings.Join(streamed, ",") != tc.streamed {
				t.Fatalf("streamed %q, want %q\n%s", streamed, tc.streamed, body)
			}
			if tc.end != "" || len(ended) > 0 {
				if strings.Join(ended, ",") != tc.end {
					t.Fatalf("ended %q, want %q\n%s", ended, tc.end, body)
				}
			}
			// nothing is lost or reordered but the phase
			want := strings.Join(tc.chunks, "")
			if got := strings.NewReplacer(`,"phase":"commentary"`, "", `,"phase":"final_answer"`, "", `"annotations":[],`, "").Replace(body); tc.name != "upstream's own phase" && len(got) != len(want) {
				t.Fatalf("relayed %d bytes, want %d\n%s", len(got), len(want), body)
			}
		})
	}
}

func TestRelayedWholeReplySaysPhase(t *testing.T) {
	whole := `{"id":"r","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[]},{"type":"function_call","call_id":"c1","name":"shell","arguments":"{}"},{"type":"message","role":"assistant","content":[]}],"usage":{"input_tokens":12345678901234567890}}`
	got := tidied(t, false, whole)
	var res struct {
		Output []map[string]any `json:"output"`
	}
	if err := json.Unmarshal([]byte(got), &res); err != nil {
		t.Fatal(err)
	}
	if res.Output[0]["phase"] != "commentary" || res.Output[2]["phase"] != "final_answer" || !strings.Contains(got, "12345678901234567890") {
		t.Fatalf("%s", got)
	}
}

// Through the gateway: a Responses provider whose model writes no phase.
func TestRelayedStreamSaysPhase(t *testing.T) {
	fresh(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(evMsg, evFcAdd,
			`data: {"type":"response.output_item.done","output_index":2,"item":{"type":"function_call","id":"fc1","call_id":"c1","name":"shell","arguments":"{}"}}`,
			`data: {"type":"response.completed","response":{"id":"r","status":"completed","output":[{"type":"message","id":"m1","role":"assistant","content":[]},{"type":"function_call","id":"fc1","call_id":"c1","name":"shell","arguments":"{}"}]}}`))
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Models: []string{"gpt-5.4"}, Responses: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	rec := codexPostTo(New(), `{"model":"relay/gpt-5.4","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
	if streamed, ended := phases(rec.Body.String()); rec.Code != 200 || strings.Join(streamed, ",") != "commentary" || strings.Join(ended, ",") != "commentary" {
		t.Fatalf("%d streamed %q ended %q\n%s", rec.Code, streamed, ended, rec.Body)
	}
}
