package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/routes"
)

// keeyangyy (#3938): a client that sends no session header and opens every
// conversation on a user message it writes alone, the same for every
// conversation started in one folder, with what the user typed after it.
// The texts are the report's own.
const (
	openingEnv   = "<environment_context>cwd=/w/proj ...</environment_context>"
	openingAsked = "the question the user actually typed"
	openingOther = "write the release notes for 0.2"
)

// openingProtocol builds a request's turns as one protocol has them.
type openingProtocol struct {
	name  string
	body  func(turns ...string) string
	user  func(text string) string
	reply func(text string) string
	image string // a user message that is a screenshot alone
}

func openingProtocols() []openingProtocol {
	const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="
	q := strconv.Quote
	join := func(turns []string) string { return strings.Join(turns, ",") }
	gemini := func(turns ...string) string {
		return `{"systemInstruction":{"parts":[{"text":"You are a coding agent."}]},"contents":[` + join(turns) + `]}`
	}
	geminiReply := func(s string) string { return `{"role":"model","parts":[{"text":` + q(s) + `}]}` }
	geminiImage := `"parts":[{"inlineData":{"mimeType":"image/png","data":"` + png + `"}}]}`
	return []openingProtocol{
		{"chat",
			func(turns ...string) string {
				return `{"model":"m","messages":[{"role":"system","content":"..."},` + join(turns) + `]}`
			},
			func(s string) string { return `{"role":"user","content":` + q(s) + `}` },
			func(s string) string { return `{"role":"assistant","content":` + q(s) + `}` },
			`{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` + png + `"}}]}`},
		{"messages",
			func(turns ...string) string {
				return `{"model":"m","max_tokens":1024,"system":"You are a coding agent.","messages":[` + join(turns) + `]}`
			},
			func(s string) string { return `{"role":"user","content":[{"type":"text","text":` + q(s) + `}]}` },
			func(s string) string { return `{"role":"assistant","content":[{"type":"text","text":` + q(s) + `}]}` },
			`{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + png + `"}}]}`},
		{"responses",
			func(turns ...string) string {
				return `{"model":"m","instructions":"You are a coding agent.","input":[` + join(turns) + `]}`
			},
			func(s string) string {
				return `{"type":"message","role":"user","content":[{"type":"input_text","text":` + q(s) + `}]}`
			},
			func(s string) string {
				return `{"type":"message","role":"assistant","content":[{"type":"output_text","text":` + q(s) + `}]}`
			},
			`{"type":"message","role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,` + png + `"}]}`},
		{"gemini", gemini,
			func(s string) string { return `{"role":"user","parts":[{"text":` + q(s) + `}]}` },
			geminiReply, `{"role":"user",` + geminiImage},
		// Gemini takes a content without a role as the user's
		{"gemini without roles", gemini,
			func(s string) string { return `{"parts":[{"text":` + q(s) + `}]}` },
			geminiReply, `{` + geminiImage},
	}
}

// openingID is the id a request's first user message alone was hashed to,
// before #3938 and still for a conversation that opens in the user's words.
func openingID(msg string) string {
	sum := sha256.Sum256([]byte(msg))
	return "magpie-" + hex.EncodeToString(sum[:12])
}

func TestConversationsOnOneEnvironmentMessageAreApart(t *testing.T) {
	id := func(body string) string { return conversationID(http.Header{}, []byte(body)) }
	for _, p := range openingProtocols() {
		t.Run(p.name, func(t *testing.T) {
			env := p.user(openingEnv)
			a := id(p.body(env, p.user(openingAsked)))
			b := id(p.body(env, p.user(openingOther)))
			if a == b || a == openingID(env) {
				t.Errorf("two conversations opened on one environment message: %s and %s (the message alone: %s)", a, b, openingID(env))
			}
			// a later turn repeats the opening, the answer and the user's next message after it
			if got := id(p.body(env, p.user(openingAsked), p.reply("Looked into it."), p.user("thanks, now the tests"))); got != a {
				t.Errorf("the first conversation's next turn is %s, not %s", got, a)
			}
			if got := id(p.body(env, p.user(openingOther), p.reply("Done."), p.user("and the changelog"))); got != b {
				t.Errorf("the second conversation's next turn is %s, not %s", got, b)
			}
			// a screenshot is the user's own as their words are: the
			// opening ends at it, before the message they send after it
			shot := id(p.body(env, p.image))
			if got := id(p.body(env, p.image, p.user("what is this error?"))); shot == openingID(env) || got != shot {
				t.Errorf("opened on the agent's message and a screenshot: %s, and %s with the user's next message (the agent's alone: %s)", shot, got, openingID(env))
			}

			// one that opens in the user's words is what it always was:
			// its first user message alone, sha256's first 12 bytes in hex
			asked := p.user(openingAsked)
			want := openingID(asked)
			for name, body := range map[string]string{
				"alone":                          p.body(asked),
				"a later turn":                   p.body(asked, p.reply("Looked into it."), p.user("thanks")),
				"the agent's message after it":   p.body(asked, p.user(openingEnv)),
				"the agent's, then a later turn": p.body(asked, p.user(openingEnv), p.reply("Looked into it."), p.user("thanks")),
			} {
				if got := id(body); got != want {
					t.Errorf("%s: %s, not the first user message's own %s", name, got, want)
				}
			}
		})
	}

	// An agent's system or developer message between them is passed over.
	for name, pair := range map[string][2]string{
		"chat": {
			`{"messages":[{"role":"system","content":"..."},{"role":"user","content":"` + openingEnv + `"},{"role":"developer","content":"Approval policy: ask."},{"role":"user","content":"` + openingAsked + `"}]}`,
			`{"messages":[{"role":"system","content":"..."},{"role":"user","content":"` + openingEnv + `"},{"role":"user","content":"` + openingAsked + `"}]}`,
		},
		"responses": {
			`{"input":[{"type":"message","role":"developer","content":"..."},{"type":"message","role":"user","content":"` + openingEnv + `"},{"type":"message","role":"system","content":"Approval policy: ask."},{"type":"message","role":"user","content":"` + openingAsked + `"}]}`,
			`{"input":[{"type":"message","role":"developer","content":"..."},{"type":"message","role":"user","content":"` + openingEnv + `"},{"type":"message","role":"user","content":"` + openingAsked + `"}]}`,
		},
	} {
		if got, want := id(pair[0]), id(pair[1]); got != want {
			t.Errorf("%s: the developer message between them moved the id: %s, not %s", name, got, want)
		}
	}

	// dsh's messages route (internal/sessions/testdata/dsh-gateway) marks
	// its last user message for the cache: its runtime context, after what
	// the user typed. The mark moves on to the next turn's, and the id
	// stays the typed message's own, which the Context page finds dsh's
	// sessions by (dshConvs).
	typed := `{"role":"user","content":"` + openingAsked + `"}`
	runtime := func(mark string) string {
		return `{"role":"user","content":[{"type":"text","text":"Current runtime context. This snapshot supersedes earlier runtime-context snapshots."` + mark + `}]}`
	}
	marked := runtime(`,"cache_control":{"type":"ephemeral"}`)
	for name, body := range map[string]string{
		"its first turn": `{"model":"m","messages":[` + typed + `,` + marked + `]}`,
		"its next":       `{"model":"m","messages":[` + typed + `,` + runtime("") + `,{"role":"assistant","content":[{"type":"text","text":"Done."}]},{"role":"user","content":"thanks"},` + marked + `]}`,
	} {
		if got, want := id(body), openingID(typed); got != want {
			t.Errorf("dsh's %s: %s, not the typed message's own %s", name, got, want)
		}
	}

	// What the opening is not: a history its client cut, which starts with
	// a tool's result, has no opening left, and its first user message is
	// hashed alone as before, whatever the user says after it; nor is a
	// result after the agent's message part of the opening.
	cut := `{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_gone","content":"exit 0"}]}`
	if got, want := id(`{"model":"m","messages":[`+cut+`,{"role":"user","content":"go on"},{"role":"assistant","content":"On it."}]}`), openingID(cut); got != want {
		t.Errorf("a history cut at a tool's result: %s, not its first message's own %s", got, want)
	}
	env := `{"role":"user","content":"` + openingEnv + `"}`
	if got, want := id(`{"model":"m","messages":[`+env+`,`+cut+`,{"role":"user","content":"go on"}]}`), openingID(env); got != want {
		t.Errorf("a tool's result after the agent's message: %s, not the first message's own %s", got, want)
	}
	// A message without a role is no one's in Chat and Responses, and
	// Responses' calls, their outputs and reasoning have none: each ends
	// the opening too, as a function's result does in Gemini's.
	respEnv := `{"type":"message","role":"user","content":"` + openingEnv + `"}`
	respAsked := `{"type":"message","role":"user","content":"` + openingAsked + `"}`
	gemEnv := `{"role":"user","parts":[{"text":"` + openingEnv + `"}]}`
	gemAsked := `{"role":"user","parts":[{"text":"` + openingAsked + `"}]}`
	result := `"parts":[{"functionResponse":{"name":"shell","response":{"output":"exit 0"}}}]}`
	for name, c := range map[string][2]string{
		"chat, a message without a role": {`{"messages":[` + env + `,{"content":"preface"},{"role":"user","content":"` + openingAsked + `"}]}`, env},
		"responses, a call":              {`{"input":[` + respEnv + `,{"type":"function_call","call_id":"call_1","name":"shell","arguments":"{}"},` + respAsked + `]}`, respEnv},
		"responses, a call's output":     {`{"input":[` + respEnv + `,{"type":"function_call_output","call_id":"call_1","output":"exit 0"},` + respAsked + `]}`, respEnv},
		"responses, reasoning":           {`{"input":[` + respEnv + `,{"type":"reasoning","summary":[]},` + respAsked + `]}`, respEnv},
		"gemini, a function's result":    {`{"contents":[` + gemEnv + `,{"role":"user",` + result + `,` + gemAsked + `]}`, gemEnv},
		"gemini, one in its own role":    {`{"contents":[` + gemEnv + `,{"role":"function",` + result + `,` + gemAsked + `]}`, gemEnv},
	} {
		if got, want := id(c[0]), openingID(c[1]); got != want {
			t.Errorf("%s after the agent's message: %s, not the first message's own %s", name, got, want)
		}
	}
	// A Responses input that is one string is hashed as it was.
	if got, want := id(`{"model":"m","input":"`+openingAsked+`"}`), openingID(`"`+openingAsked+`"`); got != want {
		t.Errorf("a string input: %s, not %s", got, want)
	}
}

// keeyangyy's client (#3938), Reasonix 1.x, sends no session header and
// opens every conversation on its snapshot of the workspace, the same for
// each one started there, before what the user typed: the snapshot is the
// agent's (injected), so its conversations are apart as Codex's are, in
// each protocol, and each keeps its id on later turns.
func TestReasonixConversationsInOneWorkspaceAreApart(t *testing.T) {
	id := func(body string) string { return conversationID(http.Header{}, []byte(body)) }
	for _, p := range openingProtocols() {
		snap := p.user(reasonixContext)
		a := id(p.body(snap, p.user(openingAsked)))
		b := id(p.body(snap, p.user(openingOther)))
		if a == b || a == openingID(snap) {
			t.Errorf("%s: two conversations opened on one snapshot: %s and %s (the snapshot alone: %s)", p.name, a, b, openingID(snap))
		}
		if got := id(p.body(snap, p.user(openingAsked), p.reply("Looked into it."), p.user("thanks, now the tests"))); got != a {
			t.Errorf("%s: the first conversation's next turn is %s, not %s", p.name, got, a)
		}
	}
}

// An agent marks its newest user message for the cache and moves the mark
// on to the next turn's: the report's opening goes out as [environment]
// [typed, marked], then [environment][typed][reply][next, marked], and so
// on. The typed message goes on unmarked as it was, as a plain string
// (dsh's way with one part) or on a message marked whole. Every turn has
// the first turn's id.
func TestCacheMarkMovingOnKeepsTheID(t *testing.T) {
	id := func(body string) string { return conversationID(http.Header{}, []byte(body)) }
	q := strconv.Quote
	const mark = `,"cache_control":{"type":"ephemeral"}`
	const png = `"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="`
	ps := map[string]openingProtocol{}
	for _, p := range openingProtocols() {
		ps[p.name] = p
	}
	chat := func(content string) string { return `{"role":"user","content":` + content + `}` }
	resp := func(content string) string { return `{"type":"message","role":"user","content":` + content + `}` }
	text := func(kind string) func(s, mark string) string {
		return func(s, mark string) string { return `[{"type":"` + kind + `","text":` + q(s) + mark + `}]` }
	}
	shot := map[string]func(s, mark string) string{
		"chat": func(s, mark string) string {
			return `[{"type":"text","text":` + q(s) + `},{"type":"image_url","image_url":{"url":"data:image/png;base64,` + png[1:] + `}` + mark + `}]`
		},
		"messages": func(s, mark string) string {
			return `[{"type":"text","text":` + q(s) + `},{"type":"image","source":{"type":"base64","media_type":"image/png","data":` + png + `}` + mark + `}]`
		},
		"responses": func(s, mark string) string {
			return `[{"type":"input_text","text":` + q(s) + `},{"type":"input_image","image_url":"data:image/png;base64,` + png[1:] + mark + `}]`
		},
	}
	type spelling struct{ marked, plain func(s string) string }
	for _, c := range []struct {
		name, p string
		spelling
	}{
		{"messages, one part", "messages", spelling{func(s string) string { return chat(text("text")(s, mark)) }, func(s string) string { return chat(text("text")(s, "")) }}},
		{"messages, a string unmarked", "messages", spelling{func(s string) string { return chat(text("text")(s, mark)) }, func(s string) string { return chat(q(s)) }}},
		{"messages, a screenshot", "messages", spelling{func(s string) string { return chat(shot["messages"](s, mark)) }, func(s string) string { return chat(shot["messages"](s, "")) }}},
		{"chat, one part", "chat", spelling{func(s string) string { return chat(text("text")(s, mark)) }, func(s string) string { return chat(text("text")(s, "")) }}},
		{"chat, a string unmarked", "chat", spelling{func(s string) string { return chat(text("text")(s, mark)) }, func(s string) string { return chat(q(s)) }}},
		{"chat, the message marked", "chat", spelling{func(s string) string { return `{"role":"user","content":` + q(s) + mark + `}` }, func(s string) string { return chat(q(s)) }}},
		{"chat, a screenshot", "chat", spelling{func(s string) string { return chat(shot["chat"](s, mark)) }, func(s string) string { return chat(shot["chat"](s, "")) }}},
		{"responses, one part", "responses", spelling{func(s string) string { return resp(text("input_text")(s, mark)) }, func(s string) string { return resp(text("input_text")(s, "")) }}},
		{"responses, a string unmarked", "responses", spelling{func(s string) string { return resp(text("input_text")(s, mark)) }, func(s string) string { return resp(q(s)) }}},
		{"responses, a screenshot", "responses", spelling{func(s string) string { return resp(shot["responses"](s, mark)) }, func(s string) string { return resp(shot["responses"](s, "")) }}},
	} {
		p := ps[c.p]
		env, r := p.user(openingEnv), p.reply("Done.")
		first := id(p.body(env, c.marked(openingAsked)))
		if first == openingID(env) || first == id(p.body(env, c.marked(openingOther))) {
			t.Errorf("%s: the first turn is %s, the environment message's own or the other conversation's", c.name, first)
		}
		for n, body := range []string{
			p.body(env, c.plain(openingAsked), r, c.marked("now the tests")),
			p.body(env, c.plain(openingAsked), r, c.plain("now the tests"), r, c.marked("and the changelog")),
		} {
			if got := id(body); got != first {
				t.Errorf("%s: turn %d is %s, not the first turn's %s", c.name, n+2, got, first)
			}
		}
	}
}

// An opening of several messages is read where it is, as one message
// always was: every request of a conversation hashes it again (its
// affinity, its trace, and more), and the user's message in it can hold
// a screenshot of a megabyte. Hashing it allocates a small part of the
// body (image_input_test.go's bar).
func TestOpeningHashReadsInPlace(t *testing.T) {
	shot := strings.Repeat("iVBORw0KGgo", 100_000)
	turns := []string{`{"role":"user","content":"` + openingEnv + `"}`,
		`{"role":"user","content":[{"type":"text","text":"what is this error?"},{"type":"image_url","image_url":{"url":"data:image/png;base64,` + shot + `"},"cache_control":{"type":"ephemeral"}}]}`}
	for i := range 50 {
		turns = append(turns, `{"role":"assistant","content":"Looked into it."}`, `{"role":"user","content":"and step `+strconv.Itoa(i)+`"}`)
	}
	body := []byte(`{"model":"m","messages":[` + strings.Join(turns, ",") + `]}`)
	ctx, done := context.WithCancel(context.Background())
	defer done()
	holdBody(ctx, body) // as readRequestBody holds a request's
	id := conversationID(http.Header{}, body)
	if id == openingID(turns[0]) {
		t.Fatal("the environment message was hashed alone")
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	const n = 5
	for range n {
		if conversationID(http.Header{}, body) != id {
			t.Fatal("the conversation's id changed")
		}
	}
	runtime.ReadMemStats(&after)
	per := (after.TotalAlloc - before.TotalAlloc) / n
	if per > uint64(len(body))/8 {
		t.Fatalf("the id of a %d-byte body opening on a screenshot allocated %d bytes", len(body), per)
	}
	t.Logf("the id of a %d-byte body opening on a screenshot allocated %d bytes", len(body), per)
}

// keyedStreams is keyed answering a request that asks for a stream with
// one, as the gateway asks a Chat vendor for on /v1/messages.
type keyedStreams struct{ keyed }

func (k *keyedStreams) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	if !bytes.Contains(body, []byte(`"stream":true`)) {
		r.Body = io.NopCloser(bytes.NewReader(body))
		k.keyed.ServeHTTP(w, r)
		return
	}
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	k.tried = append(k.tried, key)
	w.Header().Set("Content-Type", "text/event-stream")
	io.WriteString(w, `data: {"id":"x","choices":[{"index":0,"delta":{"role":"assistant","content":"from `+key+`"}}]}`+"\n\n"+
		`data: {"id":"x","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3000,"completion_tokens":5}}`+"\n\n"+
		"data: [DONE]\n\n")
}

// The report's case through the gateway: a provider with two keys, each
// conversation kept on its own for the session and new ones handed round
// in turn. A second conversation started in the same folder goes to the
// key the first isn't on, as a new one, and each one's next turn stays
// where it began. So does a conversation on /v1/messages, its cache mark
// moving on to each turn's newest message, and the reporter's client's.
func TestNewConversationsInOneFolderAreRoutedApart(t *testing.T) {
	fresh(t)
	v := &keyedStreams{}
	serveOn(t, "aff", "k1", []string{"m"}, v, "k2")
	if err := provider.SetRouting("aff", provider.Rotate); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetAffinity("aff", provider.AffinitySession); err != nil {
		t.Fatal(err)
	}
	routes.Forget("aff")
	s := New()
	routed := func(path, body string) string {
		if code, out := postTo(t, s, path, body); code != http.StatusOK {
			t.Fatalf("%d %s", code, out)
		}
		r := s.trace.routes[len(s.trace.routes)-1]
		if r.Affinity == nil {
			t.Fatalf("no affinity traced: %+v", r)
		}
		return v.tried[len(v.tried)-1] + " " + r.Affinity.Why + " " + r.Conv
	}
	turn := func(opening string, typed ...string) string {
		msgs := `{"role":"system","content":"..."},{"role":"user","content":` + strconv.Quote(opening) + `}`
		for i, said := range typed {
			if i > 0 {
				msgs += `,{"role":"assistant","content":"Done."}`
			}
			msgs += `,{"role":"user","content":"` + said + `"}`
		}
		return routed("/v1/chat/completions", `{"model":"aff/m","messages":[`+msgs+`]}`)
	}
	first := strings.Fields(turn(openingEnv, openingAsked))
	second := strings.Fields(turn(openingEnv, openingOther))
	if first[1] != "first" || second[1] != "first" || second[0] == first[0] || second[2] == first[2] {
		t.Fatalf("the second conversation went to %s as %q, conv %s; the first is on %s, conv %s", second[0], second[1], second[2], first[0], first[2])
	}
	for _, c := range []struct {
		typed []string
		was   []string
	}{
		{[]string{openingAsked, "thanks, now the tests"}, first},
		{[]string{openingOther, "and the changelog"}, second},
	} {
		if got := strings.Fields(turn(openingEnv, c.typed...)); got[0] != c.was[0] || got[1] != "session" || got[2] != c.was[2] {
			t.Errorf("%q's next turn went to %s as %q, conv %s; it began on %s, conv %s", c.typed[0], got[0], got[1], got[2], c.was[0], c.was[2])
		}
	}

	said := func(text, mark string) string {
		return `{"role":"user","content":[{"type":"text","text":"` + text + `"` + mark + `}]}`
	}
	const marked = `,"cache_control":{"type":"ephemeral"}`
	reply := `{"role":"assistant","content":[{"type":"text","text":"Done."}]}`
	messages := func(turns ...string) string {
		return routed("/v1/messages", `{"model":"aff/m","max_tokens":64,"system":"You are a coding agent.","messages":[`+
			said(openingEnv, "")+`,`+strings.Join(turns, ",")+`]}`)
	}
	third := strings.Fields(messages(said("fix the flaky test", marked)))
	if third[1] != "first" || third[2] == first[2] || third[2] == second[2] {
		t.Fatalf("the conversation on /v1/messages went to %s as %q, conv %s (the others' %s and %s)", third[0], third[1], third[2], first[2], second[2])
	}
	for n, turns := range [][]string{
		{said("fix the flaky test", ""), reply, said("now the lint", marked)},
		{said("fix the flaky test", ""), reply, said("now the lint", ""), reply, said("and push it", marked)},
	} {
		if got := strings.Fields(messages(turns...)); got[0] != third[0] || got[1] != "session" || got[2] != third[2] {
			t.Errorf("its turn %d on /v1/messages went to %s as %q, conv %s; it began on %s, conv %s", n+2, got[0], got[1], got[2], third[0], third[2])
		}
	}

	// the report's client, Reasonix 1.x, opening each conversation on its
	// snapshot of the workspace, the same in every one started there
	fourth := strings.Fields(turn(reasonixContext, openingAsked))
	fifth := strings.Fields(turn(reasonixContext, openingOther))
	if fourth[1] != "first" || fifth[1] != "first" || fifth[0] == fourth[0] || fifth[2] == fourth[2] {
		t.Fatalf("Reasonix's second conversation went to %s as %q, conv %s; its first is on %s, conv %s", fifth[0], fifth[1], fifth[2], fourth[0], fourth[2])
	}
	for _, c := range []struct{ typed, was []string }{
		{[]string{openingAsked, "thanks, now the tests"}, fourth},
		{[]string{openingOther, "and the changelog"}, fifth},
	} {
		if got := strings.Fields(turn(reasonixContext, c.typed...)); got[0] != c.was[0] || got[1] != "session" || got[2] != c.was[2] {
			t.Errorf("Reasonix's %q next turn went to %s as %q, conv %s; it began on %s, conv %s", c.typed[0], got[0], got[1], got[2], c.was[0], c.was[2])
		}
	}
}
