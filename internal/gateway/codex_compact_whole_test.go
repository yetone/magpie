package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// #2274 (huangzuomin): Codex compacting through magpie on
// antigravity/gemini-3.8-flash asked for 1169 summaries in 6 hours. Each
// summary's stream was let through at its first words, so Antigravity
// cutting it short ("the reply ended before it was complete"), or a 429
// "Individual quota reached" halfway, was the only answer: the next
// account was never asked, nobody rested, and Codex got a 502 it asked
// again and again on.

// compactPair: "ag" (the model Codex compacts on) falls back to "spare",
// each a fake on proto's wire.
func compactPair(t *testing.T, proto provider.Protocol, ag, spare *fake) {
	t.Helper()
	noRests()
	t.Cleanup(noRests)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, x := range []struct {
		p provider.Provider
		f *fake
	}{
		{provider.Provider{ID: "ag", Name: "AG", Key: "k", Models: []string{"m1"}, Fallback: []string{"spare/m1"}}, ag},
		{provider.Provider{ID: "spare", Name: "Spare", Key: "k", Models: []string{"m1"}}, spare},
	} {
		up := httptest.NewServer(x.f)
		t.Cleanup(up.Close)
		switch proto {
		case provider.Chat:
			x.p.Chat = up.URL + "/v1"
		default:
			x.p.Responses = up.URL + "/v1"
		}
		if err := provider.Save(x.p); err != nil {
			t.Fatal(err)
		}
	}
}

// codexCompactOf is compact404Request on model, in conversation key.
func codexCompactOf(model, key string) string {
	b := strings.Replace(compact404Request, `"model":"fake/m1"`, `"model":"`+model+`","prompt_cache_key":"`+key+`"`, 1)
	return b
}

// the summary's stream, cut or failed after its first words, on each wire
func cutReplies(proto provider.Protocol) map[string]string {
	if proto == provider.Chat {
		// (a Chat stream may end without [DONE] and be whole, so one that
		// just stops isn't cut: errEndedShort)
		return map[string]string{
			"quota halfway": sse(`data: {"choices":[{"delta":{"content":"PARTIAL summary of"}}]}`,
				`data: {"error":{"message":"Individual quota reached","code":429}}`),
		}
	}
	return map[string]string{
		"cut": sse(`data: {"type":"response.created","response":{"id":"r1"}}`,
			`data: {"type":"response.output_text.delta","delta":"PARTIAL summary of"}`,
			`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"PARTIAL summary of"}]}}`),
		"quota halfway": sse(`data: {"type":"response.created","response":{"id":"r1"}}`,
			`data: {"type":"response.output_text.delta","delta":"PARTIAL summary of"}`,
			`data: {"type":"response.failed","response":{"id":"r1","status":"failed","error":{"code":"rate_limit_exceeded","message":"Individual quota reached"}}}`),
		"incomplete": sse(`data: {"type":"response.created","response":{"id":"r1"}}`,
			`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"PARTIAL summary of"}]}}`,
			`data: {"type":"response.incomplete","response":{"id":"r1","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[]}}`),
	}
}

func wholeSummary(proto provider.Protocol) string {
	if proto == provider.Chat {
		return sse(`data: {"choices":[{"delta":{"content":"SUMMARY"}}]}`,
			`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`, `data: [DONE]`)
	}
	return sse(`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"SUMMARY"}]}}`,
		`data: {"type":"response.completed","response":{"id":"r2","status":"completed","output":[]}}`)
}

// A summary cut short, or failed halfway, goes to the next candidate, and
// the whole one it writes is the compaction; nothing of the cut one is.
func TestCodexCompactCutSummaryFailsOver(t *testing.T) {
	for _, proto := range []provider.Protocol{provider.Responses, provider.Chat} {
		for name, cut := range cutReplies(proto) {
			t.Run(string(proto)+"/"+name, func(t *testing.T) {
				ag := &fake{t: t, reply: cut}
				spare := &fake{t: t, reply: wholeSummary(proto)}
				compactPair(t, proto, ag, spare)
				code, body := codexPost(t, codexCompactOf("ag/m1", "thread-1"))
				if code != 200 {
					t.Fatalf("%d %s", code, body)
				}
				if got := compactedText(t, body); got != "SUMMARY" {
					t.Errorf("summary %q, want the spare's whole one", got)
				}
				if spare.calls != 1 {
					t.Errorf("spare asked %d times", spare.calls)
				}
				_, rested := restOf("ag")
				if want := name != "incomplete"; rested != want {
					// a vendor that broke off rests; a model that reached
					// its output limit is no fault of the account
					t.Errorf("ag rested %v, want %v", rested, want)
				}
			})
		}
	}
}

// With nobody left, a summary that failed or ended incomplete is no
// summary: Codex is told why, and not handed the words before the failure
// as the conversation's summary. (One whose stream merely broke off is
// followed by magpie's own summary, TestCodexCompactCutAloneCompactsLocally.)
func TestCodexCompactCutSummaryIsNoSummary(t *testing.T) {
	for name, cut := range cutReplies(provider.Responses) {
		if name == "cut" {
			continue
		}
		t.Run(name, func(t *testing.T) {
			f := &fake{t: t, reply: cut}
			setup(t, provider.Responses, f)
			code, body := codexPost(t, compact404Request)
			if code < 400 || strings.Contains(body, magpieCompaction) {
				t.Fatalf("%d %s", code, body)
			}
		})
	}
}

// With nobody left to fail over to, a summary stream the candidate cut
// short is followed by magpie's own summary, as when no model can be asked
// at all (#3526): Codex, told a 502, asked the same huge summary over three
// times and the guard then held the conversation, unable to compact or go
// on. The words before the cut are never the summary, and the cut summary
// isn't asked again as text — the cut was in what the model wrote, not in
// what it was asked.
func TestCodexCompactCutAloneCompactsLocally(t *testing.T) {
	f := &fake{t: t, reply: cutReplies(provider.Responses)["cut"]}
	setup(t, provider.Responses, f)
	code, body := codexPost(t, compact404Request)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	sum := compactedText(t, body)
	if strings.Contains(sum, "PARTIAL") {
		t.Errorf("the cut stream's words are the summary: %q", sum)
	}
	for _, want := range []string{"no summary", "the reply ended before it was complete", "fix the login bug", "now add a test"} {
		if !strings.Contains(sum, want) {
			t.Errorf("summary %q, want it to say %q", sum, want)
		}
	}
	if f.calls < 1 || strings.Contains(string(f.got), "This is the conversation so far") {
		// the summary as text is another ask of the same huge prompt: it
		// isn't tried, the last one the fake saw is the summary request
		t.Errorf("the summary was asked again as text (%d asks, last %q)", f.calls, f.got)
	}
}

// One conversation's compaction that fails compactFailsMost times in a
// row is answered at once after, with a 400 Codex doesn't ask again on,
// no model asked; another conversation's still is, and one that worked
// clears the count.
func TestCodexCompactKeepsFailingIsHeld(t *testing.T) {
	// a summary that ended incomplete still fails: only one whose stream
	// merely broke off is magpie's own now (#3526)
	f := &fake{t: t, reply: cutReplies(provider.Responses)["incomplete"]}
	setup(t, provider.Responses, f)
	s := New()
	for i := range compactFailsMost {
		if rec := codexPostTo(s, codexCompactOf("fake/m1", "thread-1")); rec.Code < 500 {
			t.Fatalf("try %d: %d %s", i, rec.Code, rec.Body)
		}
	}
	asked := f.calls
	rec := codexPostTo(s, codexCompactOf("fake/m1", "thread-1"))
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "Couldn't compact the conversation: fake/m1 failed 3 times in a row") || !strings.Contains(rec.Body.String(), "switch this conversation to another model") {
		t.Fatalf("held: %d %s", rec.Code, rec.Body)
	}
	if f.calls != asked {
		t.Errorf("the model was asked again (%d calls, %d before)", f.calls, asked)
	}
	if rec := codexPostTo(s, codexCompactOf("fake/m1", "thread-2")); rec.Code == 400 || f.calls == asked {
		t.Errorf("another conversation: %d, calls %d", rec.Code, f.calls)
	}

	// a summary that came clears it
	s2 := New()
	f.reply = cutReplies(provider.Responses)["incomplete"]
	for range compactFailsMost - 1 {
		codexPostTo(s2, codexCompactOf("fake/m1", "thread-3"))
	}
	f.reply = wholeSummary(provider.Responses)
	if rec := codexPostTo(s2, codexCompactOf("fake/m1", "thread-3")); rec.Code != 200 {
		t.Fatalf("whole: %d %s", rec.Code, rec.Body)
	}
	f.reply = cutReplies(provider.Responses)["incomplete"]
	codexPostTo(s2, codexCompactOf("fake/m1", "thread-3"))
	if rec := codexPostTo(s2, codexCompactOf("fake/m1", "thread-3")); rec.Code == 400 {
		t.Errorf("held after a summary came: %s", rec.Body)
	}
}

// The same cut reaches codexCompact in two more shapes (Edward4226 on
// #3526): an error event saying it, and a 502 whose message is it — a
// translated reply serve held whole and found cut, or a relay in front
// passing that on. Each is followed by magpie's own summary too, never
// asked again as text; a 502 saying anything else stays Codex's failure.
func TestCodexCompactCutShapesCompactLocally(t *testing.T) {
	cut := "fake: " + errEndedShort.Error()
	for _, tc := range []struct {
		name string
		f    *fake
	}{
		{"stream error", &fake{reply: sse(`data: {"type":"error","message":"` + cut + `"}`)}},
		{"HTTP 502", &fake{code: 502, ctype: "application/json", reply: `{"error":{"message":"` + cut + `"}}`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.f
			f.t = t
			setup(t, provider.Responses, f)
			code, body := codexPost(t, compact404Request)
			if code != 200 {
				t.Fatalf("%d %s", code, body)
			}
			got := compactedText(t, body)
			for _, want := range []string{"fix the login bug", "now add a test", errEndedShort.Error()} {
				if !strings.Contains(got, want) {
					t.Errorf("local summary lacks %q:\n%s", want, got)
				}
			}
			if strings.Contains(string(f.got), "This is the conversation so far") {
				t.Errorf("the summary was asked again as text (%d asks)", f.calls)
			}
		})
	}
	t.Run("HTTP 502 saying more", func(t *testing.T) {
		f := &fake{t: t, code: 502, ctype: "application/json", reply: `{"error":{"message":"` + cut + `; and the relay is down"}}`}
		setup(t, provider.Responses, f)
		if code, body := codexPost(t, compact404Request); code != 502 || strings.Contains(body, magpieCompaction) {
			t.Fatalf("%d %s", code, body)
		}
	})
}
