package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// nativeSealedReply is a Responses stream whose output is one compaction item,
// sealed by the vendor as enc.
func nativeSealedReply(enc string) string {
	item := `{"type":"compaction","encrypted_content":"` + enc + `"}`
	return sse(
		`data: {"type":"response.created","response":{"id":"resp_c1","status":"in_progress","output":[]}}`,
		`data: {"type":"response.output_item.done","output_index":0,"item":`+item+`}`,
		`data: {"type":"response.completed","response":{"id":"resp_c1","status":"completed","output":[`+item+`],"usage":{"input_tokens":9,"output_tokens":2,"total_tokens":11}}}`)
}

func nativeOn(t *testing.T, id string) {
	t.Helper()
	if err := provider.SetNativeCompaction(id, true); err != nil {
		t.Fatal(err)
	}
}

// Codex offers web_search on every turn, which has a turn to a provider
// that doesn't search translated: a compaction is relayed all the same
const compactTurn = `{"model":"%s","stream":true,"instructions":"You are Codex","tools":[{"type":"web_search"}],"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"fix the bug"}]},{"type":"compaction_trigger"}]}`

func nativeAsk(t *testing.T, model, body string) (int, string) {
	t.Helper()
	return codexPost(t, strings.Replace(body, "%s", model, 1))
}

// #1099: a provider set to compact natively is asked Codex's
// compaction_trigger as Codex sent it, and the compaction it seals goes
// back to Codex unchanged; the next turn carries it to the same provider
// as it is. One not set to keeps magpie's summary (magpie1:).
func TestNativeCompactionPassesTheTrigger(t *testing.T) {
	f := &fake{t: t, reply: nativeSealedReply("gAAAAB-sealed-by-relay")}
	setup(t, provider.Responses, f)
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("a relay's compaction reached OpenAI")
		w.WriteHeader(400)
	})

	// off: magpie summarises, the trigger never reaches the relay
	f.reply = sse(
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"SUMMARY"}]}}`,
		`data: {"type":"response.completed","response":{"id":"resp_s","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`)
	code, body := nativeAsk(t, "fake/m1", compactTurn)
	if code != 200 || strings.Contains(string(f.got), "compaction_trigger") || !strings.Contains(body, magpieCompaction) {
		t.Fatalf("off: %d %s\nrelay got %s", code, body, f.got)
	}

	// on
	nativeOn(t, "fake")
	f.reply = nativeSealedReply("gAAAAB-sealed-by-relay")
	code, body = nativeAsk(t, "fake/m1", compactTurn)
	if code != 200 {
		t.Fatalf("on: %d %s", code, body)
	}
	if !strings.Contains(string(f.got), `{"type":"compaction_trigger"}`) || strings.Contains(string(f.got), "CONTEXT CHECKPOINT COMPACTION") {
		t.Errorf("the relay wasn't asked Codex's own compaction: %s", f.got)
	}
	if f.path != "/v1/responses" {
		t.Errorf("asked at %s", f.path)
	}
	if !strings.Contains(body, `"encrypted_content":"gAAAAB-sealed-by-relay"`) || strings.Contains(body, magpieCompaction) {
		t.Errorf("Codex got %s", body)
	}

	// the next turn takes the sealed compaction to the relay as it is
	f.reply = sse(`data: {"type":"response.completed","response":{"id":"resp_n","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`)
	code, body = codexPost(t, `{"model":"fake/m1","stream":true,"tools":[{"type":"web_search"}],"input":[{"type":"compaction","encrypted_content":"gAAAAB-sealed-by-relay"},{"type":"message","role":"user","content":[{"type":"input_text","text":"go on"}]}]}`)
	if code != 200 || !strings.Contains(string(f.got), `"encrypted_content":"gAAAAB-sealed-by-relay"`) {
		t.Errorf("next turn: %d %s\nrelay got %s", code, body, f.got)
	}
}

// A relay that refuses compaction_trigger has its own error go back to
// Codex, not a summary of magpie's in its place.
func TestNativeCompactionRefusalGoesBack(t *testing.T) {
	f := &fake{t: t}
	f.refuse = func(body []byte) (int, string) {
		if strings.Contains(string(body), "compaction_trigger") {
			return 400, `{"error":{"message":"Invalid value: 'compaction_trigger'. Supported values are: 'message', 'reasoning'.","type":"invalid_request_error","param":"input[1].type"}}`
		}
		return 0, ""
	}
	setup(t, provider.Responses, f)
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(400) })
	nativeOn(t, "fake")
	code, body := nativeAsk(t, "fake/m1", compactTurn)
	if code != 400 || !strings.Contains(body, "Invalid value: 'compaction_trigger'") || strings.Contains(body, magpieCompaction) {
		t.Fatalf("%d %s", code, body)
	}
	if f.calls != 1 {
		t.Errorf("asked %d times", f.calls)
	}
}

// A compaction one key sealed never goes to another provider, which would
// refuse it, nor goes without it: the request is answered an error saying
// why. The writer, asked again, still reads it.
func TestNativeCompactionStaysWithItsWriter(t *testing.T) {
	f := &fake{t: t, reply: nativeSealedReply("gAAAAB-sealed-by-a")}
	setup(t, provider.Responses, f)
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(400) })
	g := &fake{t: t, reply: sse(`data: {"type":"response.completed","response":{"id":"resp_b","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`)}
	up := httptest.NewServer(g)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "other", Name: "Other", Key: "k2", Models: []string{"m1"}, Responses: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	nativeOn(t, "fake")
	nativeOn(t, "other")
	if code, body := nativeAsk(t, "fake/m1", compactTurn); code != 200 || !strings.Contains(body, "gAAAAB-sealed-by-a") {
		t.Fatalf("compact: %d %s", code, body)
	}
	next := `{"model":"%s","stream":true,"input":[{"type":"compaction","encrypted_content":"gAAAAB-sealed-by-a"},{"type":"message","role":"user","content":[{"type":"input_text","text":"go on"}]}]}`
	code, body := nativeAsk(t, "other/m1", next)
	if code != 400 || !strings.Contains(body, "compacted natively") || !strings.Contains(body, "fake#") {
		t.Fatalf("another provider: %d %s", code, body)
	}
	if g.calls != 0 {
		t.Errorf("the other provider was sent it: %s", g.got)
	}
	f.reply = g.reply
	if code, body := nativeAsk(t, "fake/m1", next); code != 200 || !strings.Contains(string(f.got), "gAAAAB-sealed-by-a") {
		t.Errorf("the writer: %d %s", code, body)
	}
}

// GitHub Copilot's GPT models answer on Responses, so a Copilot account
// set to compact natively is asked Codex's compaction as it was sent; its
// Claude models, on chat completions, keep magpie's summary.
func TestNativeCompactionCopilot(t *testing.T) {
	var mu sync.Mutex
	var asked []string
	copilotFake(t, "gho_compact1099", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/models":
			io.WriteString(w, `{"data":[
			  {"id":"gpt-5.3-codex","name":"GPT-5.3-Codex","model_picker_enabled":true,"policy":{"state":"enabled"},"supported_endpoints":["/responses"],"capabilities":{"type":"chat"}},
			  {"id":"claude-sonnet-4.5","name":"Claude Sonnet 4.5","model_picker_enabled":true,"policy":{"state":"enabled"},"supported_endpoints":["/chat/completions"],"capabilities":{"type":"chat"}}]}`)
		case "/responses":
			asked = append(asked, "/responses "+string(b))
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, nativeSealedReply("gAAAAB-sealed-by-copilot"))
		case "/chat/completions":
			asked = append(asked, "/chat/completions "+string(b))
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"SUMMARY\"}}]}\n\ndata: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n")
		default:
			w.WriteHeader(404)
		}
	})
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(400) })
	p, err := provider.Find("copilot")
	if err != nil || !p.CarriesCompaction() {
		t.Fatalf("copilot: %v %+v", err, p)
	}
	nativeOn(t, "copilot")
	if p, _ := provider.Find("copilot"); !p.NativeCompaction {
		t.Fatal("a Copilot account didn't keep native compaction on")
	}
	code, body := nativeAsk(t, "copilot/gpt-5.3-codex", compactTurn)
	if code != 200 || !strings.Contains(body, `"encrypted_content":"gAAAAB-sealed-by-copilot"`) {
		t.Fatalf("gpt: %d %s", code, body)
	}
	mu.Lock()
	if len(asked) != 1 || !strings.HasPrefix(asked[0], "/responses ") || !strings.Contains(asked[0], `{"type":"compaction_trigger"}`) {
		t.Errorf("Copilot was asked %q", asked)
	}
	asked = nil
	mu.Unlock()

	code, body = nativeAsk(t, "copilot/claude-sonnet-4.5", compactTurn)
	if code != 200 || !strings.Contains(body, magpieCompaction) {
		t.Fatalf("claude: %d %s", code, body)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, a := range asked {
		if strings.Contains(a, "compaction_trigger") {
			t.Errorf("Claude on Copilot was asked the trigger: %s", a)
		}
	}
}

// #2271 (JasonBuildAI): a native compaction its provider couldn't answer,
// sealed by the fallback it went to, whose own native compaction is off,
// is that fallback's to read: it is recorded as the writer, the next turn
// carrying it goes there as it is, and a provider that can't read it is
// never sent it, nor asked without it, but answered why.
func TestNativeCompactionFallbackSealStaysWithItsWriter(t *testing.T) {
	noRests()
	t.Cleanup(noRests)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(400) })
	done := sse(`data: {"type":"response.completed","response":{"id":"resp_n","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`)
	fa := &fake{t: t, reply: done}
	fa.refuse = func(body []byte) (int, string) {
		if strings.Contains(string(body), "compaction_trigger") {
			return 500, `{"error":{"message":"upstream down"}}`
		}
		return 0, ""
	}
	fb := &fake{t: t, reply: nativeSealedReply("gAAAAB-sealed-by-fb")}
	other := &fake{t: t, reply: done}
	for _, x := range []struct {
		p provider.Provider
		f *fake
	}{
		{provider.Provider{ID: "fa", Name: "FA", Key: "k", Models: []string{"m1"}, Fallback: []string{"fb/m1"}}, fa},
		{provider.Provider{ID: "fb", Name: "FB", Key: "k", Models: []string{"m1"}}, fb},
		{provider.Provider{ID: "other", Name: "Other", Key: "k", Models: []string{"m1"}}, other},
	} {
		up := httptest.NewServer(x.f)
		t.Cleanup(up.Close)
		x.p.Responses = up.URL + "/v1"
		if err := provider.Save(x.p); err != nil {
			t.Fatal(err)
		}
	}
	nativeOn(t, "fa") // fb and other are not

	code, body := nativeAsk(t, "fa/m1", compactTurn)
	if code != 200 || !strings.Contains(body, "gAAAAB-sealed-by-fb") || !strings.Contains(string(fb.got), "compaction_trigger") {
		t.Fatalf("compact: %d %s\nfb got %s", code, body, fb.got)
	}

	// as Codex sends it, offering web_search, which has a turn to a
	// provider that doesn't search translated
	next := `{"model":"%s","stream":true,"tools":[{"type":"web_search"}],"input":[{"type":"compaction","encrypted_content":"gAAAAB-sealed-by-fb"},{"type":"message","role":"user","content":[{"type":"input_text","text":"go on"}]}]}`
	code, body = nativeAsk(t, "other/m1", next)
	if code != 400 || !strings.Contains(body, "compacted natively") || !strings.Contains(body, "fb#") {
		t.Fatalf("another provider: %d %s\nother got %s", code, body, other.got)
	}
	if strings.Contains(body, "native compaction is on") {
		t.Errorf("says native compaction is on for its writer, whose isn't: %s", body)
	}
	if other.calls != 0 {
		t.Errorf("the other provider was asked: %s", other.got)
	}

	fb.reply = done
	fa.got, fb.got = nil, nil
	code, body = nativeAsk(t, "fa/m1", next)
	if code != 200 || !strings.Contains(string(fb.got), `"encrypted_content":"gAAAAB-sealed-by-fb"`) {
		t.Errorf("the writer: %d %s\nfb got %s", code, body, fb.got)
	}
	if strings.Contains(string(fa.got), "gAAAAB-sealed-by-fb") {
		t.Errorf("fa, which can't read it, was sent it: %s", fa.got)
	}
}

// #2517: a conversation compacted natively by an account goes only to it,
// so when that account is held at its usage cap the request is refused
// saying so, not as if every account that serves the model were held: the
// other, with room, wasn't tried for the compaction, and routing can't
// move it there.
func TestNativeCompactionWriterAtItsCap(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	b := newResetBackend(t)
	capUsage(t, map[string]float64{"me@example.com": 75, "spare@example.com": 20})
	if err := provider.SetRouting("codex", provider.Ordered); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetAccountCap("codex", "me@example.com", 70); err != nil {
		t.Fatal(err)
	}
	wroteCheckpoint("gAAAAB-sealed-by-me", candidate{p: provider.Provider{ID: "codex", Account: &provider.Account{User: "me@example.com"}}})
	srv := New()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(`{"model":"gpt-5.5","stream":true,"input":[{"type":"compaction","encrypted_content":"gAAAAB-sealed-by-me"},{"type":"message","role":"user","content":[{"type":"input_text","text":"go on"}]}]}`))
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	req.Header.Set("chatgpt-account-id", "acct-1")
	srv.Handler().ServeHTTP(rec, req)
	code, body := rec.Code, rec.Body.String()
	if tried, _ := b.seen(); code != 429 || tried != "" {
		t.Fatalf("%d tried %q: %s", code, tried, body)
	}
	for _, want := range []string{"rate_limit_error", "one account this conversation can go to", "me@example.com", "past its 70% cap", "compacted natively", "routing mode", "account-cap"} {
		if !strings.Contains(body, want) {
			t.Errorf("no %q in %s", want, body)
		}
	}
	for _, not := range []string{"every account", "spare@example.com"} {
		if strings.Contains(body, not) {
			t.Errorf("%q in %s", not, body)
		}
	}
	if calls := srv.Recent(); len(calls) == 0 || calls[0].Status != 429 || calls[0].Error != "native compaction's account at its usage cap" {
		t.Fatalf("request log %+v", calls)
	}

	// without the compaction, the account with room takes it
	if code, body := resetPost(t, srv); code != 200 || !strings.Contains(body, "pong") {
		t.Fatalf("no compaction: %d %s", code, body)
	}
}

// A conversation compacted natively by an account whose own list has since
// dropped the model goes to it still: only it can read the compaction, and
// an account whose list lacks the model is the last resort tried when none
// lists it (perKeyBarred). The other account, whose list has the model, is
// never sent it. (#2642: the request was refused saying the writer is
// "switched off, removed, or no longer serves" though it is on, and the
// advice to switch it on again changed nothing.)
func TestNativeCompactionWriterWhoseListLacksTheModel(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	b := newResetBackend(t)
	// the spare's plan lists the model; the writer's no longer does
	if err := catalog.SaveLive("codex@"+provider.KeyID("spare@example.com"), provider.CodexBase, []catalog.Model{{ID: "gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("codex@"+provider.KeyID("me@example.com"), provider.CodexBase, []catalog.Model{{ID: "gpt-4.1"}}); err != nil {
		t.Fatal(err)
	}
	wroteCheckpoint("gAAAAB-sealed-by-me", candidate{p: provider.Provider{ID: "codex", Account: &provider.Account{User: "me@example.com"}}})
	srv := New()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(`{"model":"gpt-5.5","stream":true,"input":[{"type":"compaction","encrypted_content":"gAAAAB-sealed-by-me"},{"type":"message","role":"user","content":[{"type":"input_text","text":"go on"}]}]}`))
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	req.Header.Set("chatgpt-account-id", "acct-1")
	srv.Handler().ServeHTTP(rec, req)
	code, body := rec.Code, rec.Body.String()
	tried, _ := b.seen()
	if code != 200 || tried != "acct-1" {
		t.Fatalf("%d tried %q: %s", code, tried, body)
	}
	if !strings.Contains(body, "pong") {
		t.Errorf("the writer's answer: %s", body)
	}
	if strings.Contains(tried, "acct-2") {
		t.Errorf("the spare, whose list has the model, was asked: %q", tried)
	}
	// tried, its row leaves the routing page's left: not both attempted
	// and unlisted, skipped
	for _, w := range lastRoute(srv).Left {
		if w.Who == "me@example.com" {
			t.Errorf("the writer, tried, still left out: %+v", w)
		}
	}
}

// The same through a routing group: the conversation's model is the group,
// whose member is the codex provider, so the plan is planLevel's keys, not
// plan's add, and the writer whose own list has since dropped the model is
// tried still, the spare never asked. (#2642: keys kept the left-out
// writer's row alone, so a request routed through the group was refused
// 400 all the same.)
func TestNativeCompactionWriterThroughAGroupWhoseListLacksTheModel(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	b := newResetBackend(t)
	// the spare's plan lists the model; the writer's no longer does
	if err := catalog.SaveLive("codex@"+provider.KeyID("spare@example.com"), provider.CodexBase, []catalog.Model{{ID: "gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("codex@"+provider.KeyID("me@example.com"), provider.CodexBase, []catalog.Model{{ID: "gpt-4.1"}}); err != nil {
		t.Fatal(err)
	}
	nativeOn(t, "codex")
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"codex/gpt-5.5"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	wroteCheckpoint("gAAAAB-sealed-by-me", candidate{p: provider.Provider{ID: "codex", Account: &provider.Account{User: "me@example.com"}}})
	srv := New()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(`{"model":"group/g","stream":true,"input":[{"type":"compaction","encrypted_content":"gAAAAB-sealed-by-me"},{"type":"message","role":"user","content":[{"type":"input_text","text":"go on"}]}]}`))
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	req.Header.Set("chatgpt-account-id", "acct-1")
	srv.Handler().ServeHTTP(rec, req)
	code, body := rec.Code, rec.Body.String()
	tried, _ := b.seen()
	if code != 200 || tried != "acct-1" {
		t.Fatalf("%d tried %q: %s", code, tried, body)
	}
	if !strings.Contains(body, "pong") {
		t.Errorf("the writer's answer: %s", body)
	}
	if strings.Contains(tried, "acct-2") {
		t.Errorf("the spare, whose list has the model, was asked: %q", tried)
	}
	// tried, its row leaves the routing page's left: not both attempted
	// and unlisted, skipped
	for _, w := range lastRoute(srv).Left {
		if w.Who == "me@example.com" {
			t.Errorf("the writer, tried, still left out: %+v", w)
		}
	}
}

// #2582, #2642: Codex compacting one of its own models through magpie's
// Codex accounts compacts natively only with the codex provider's native
// compaction on. Off (the default), the trigger never reaches the ChatGPT
// backend: magpie's summary comes back, which any account reads, and no
// account is recorded as the conversation's only reader. Before, the
// trigger was relayed whatever the setting and the seal bound the
// conversation to the account that answered, so switching to another
// account refused it.
func TestCodexOwnModelCompactionHonoursTheToggle(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	var mu sync.Mutex
	var asked []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		asked = append(asked, r.Header.Get("chatgpt-account-id")+" "+string(b))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if strings.Contains(string(b), "compaction_trigger") {
			io.WriteString(w, nativeSealedReply("gAAAAB-sealed-by-chatgpt"))
			return
		}
		io.WriteString(w, sse(
			`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"SUMMARY"}]}}`,
			`data: {"type":"response.completed","response":{"id":"resp_s","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`))
	}))
	t.Cleanup(up.Close)
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })
	sealed := func(enc string) (checkpointWriter, bool) {
		return checkpointOf([]byte(`{"input":[{"type":"compaction","encrypted_content":"` + enc + `"}]}`))
	}

	code, body := nativeAsk(t, "gpt-5.5", compactTurn)
	if code != 200 || !strings.Contains(body, magpieCompaction) || strings.Contains(body, "gAAAAB-sealed-by-chatgpt") {
		t.Fatalf("off: %d %s", code, body)
	}
	mu.Lock()
	for _, a := range asked {
		if strings.Contains(a, "compaction_trigger") {
			t.Errorf("off, the trigger reached the ChatGPT backend: %s", a)
		}
	}
	if len(asked) == 0 {
		t.Error("off, no account was asked for the summary")
	}
	asked = nil
	mu.Unlock()
	if cp, ok := sealed("gAAAAB-sealed-by-chatgpt"); ok {
		t.Errorf("off, a writer was recorded: %+v", cp)
	}

	nativeOn(t, "codex")
	code, body = nativeAsk(t, "gpt-5.5", compactTurn)
	if code != 200 || !strings.Contains(body, `"encrypted_content":"gAAAAB-sealed-by-chatgpt"`) || strings.Contains(body, magpieCompaction) {
		t.Fatalf("on: %d %s", code, body)
	}
	mu.Lock()
	if len(asked) != 1 || !strings.Contains(asked[0], `{"type":"compaction_trigger"}`) {
		t.Errorf("on, the backend was asked %q", asked)
	}
	mu.Unlock()
	if cp, ok := sealed("gAAAAB-sealed-by-chatgpt"); !ok || cp.Who != "codex@me@example.com" {
		t.Errorf("on, the writer recorded is %+v %v", cp, ok)
	}
}

// #2642: a conversation whose native compaction's account can't be asked
// is told why, as magpie's settings say, and what lets it go on — not
// "switched off, removed, or no longer serves" of an account that is on.
// No other account is asked.
func TestNativeCompactionWriterGoneSaysWhy(t *testing.T) {
	ask := func(t *testing.T, enc string) (int, string) {
		t.Helper()
		return codexPost(t, `{"model":"gpt-5.5","stream":true,"input":[{"type":"compaction","encrypted_content":"`+enc+`"},{"type":"message","role":"user","content":[{"type":"input_text","text":"go on"}]}]}`)
	}
	account := func(user string) candidate {
		return candidate{p: provider.Provider{ID: "codex", Account: &provider.Account{User: user}}, model: "gpt-5.5"}
	}
	for _, c := range []struct {
		name, writer string
		set          func(t *testing.T)
		want, not    []string
	}{
		{"spare switched off", "spare@example.com",
			func(t *testing.T) {
				if err := provider.SetLoginOn("codex", "spare@example.com", false); err != nil {
					t.Fatal(err)
				}
			},
			[]string{"switched off in magpie", "switch spare@example.com on again", "the others can stay on beside it"},
			[]string{"no longer signed in", "paused"}},
		{"own paused", "me@example.com",
			func(t *testing.T) {
				if err := provider.SetLoginOn("codex", "me@example.com", false); err != nil {
					t.Fatal(err)
				}
			},
			[]string{"it is paused", "turn me@example.com on again"},
			[]string{"no longer signed in", "switched off in magpie"}},
		{"kept from the model", "me@example.com",
			func(t *testing.T) {
				if err := provider.SetAccountModels("codex", "me@example.com", []string{"gpt-4.1"}); err != nil {
					t.Fatal(err)
				}
			},
			[]string{"set not to serve gpt-5.5 (Model settings)", "let me@example.com serve gpt-5.5 again"},
			[]string{"no longer signed in", "switched off in magpie", "paused"}},
		{"signed out", "gone@example.com", func(t *testing.T) {},
			[]string{"gone@example.com is no longer signed in to magpie", "sign in to gone@example.com again"},
			[]string{"switched off in magpie", "paused"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			codexSignedIn(t, "spare@example.com")
			b := newResetBackend(t)
			c.set(t)
			enc := "gAAAAB-sealed-" + strings.ReplaceAll(c.name, " ", "-")
			wroteCheckpoint(enc, account(c.writer))
			code, body := ask(t, enc)
			if tried, _ := b.seen(); code != 400 || tried != "" {
				t.Fatalf("%d tried %q: %s", code, tried, body)
			}
			for _, want := range append(c.want, "compacted natively by codex@"+c.writer, "sealed (encrypted) to that account", "start a new conversation", "Native compaction is off for codex now") {
				if !strings.Contains(body, want) {
					t.Errorf("no %q in %s", want, body)
				}
			}
			for _, not := range append(c.not, "switched off, removed, or no longer serves", "Switch codex@") {
				if strings.Contains(body, not) {
					t.Errorf("%q in %s", not, body)
				}
			}
		})
	}
}

// #2639: a turn carrying a native compaction goes only to the account
// that sealed it, and its route says so (Compaction), for Routing to say
// that is why no other account was asked, not that it is the only one on.
func TestNativeCompactionRouteNamesItsWriter(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	noRests()
	t.Cleanup(noRests)
	claims := func(m map[string]any) string {
		b, _ := json.Marshal(m)
		return "h." + base64.RawURLEncoding.EncodeToString(b) + ".s"
	}
	auth := func(email, acct string) map[string]any {
		return map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
			"id_token":      claims(map[string]any{"email": email}),
			"access_token":  claims(map[string]any{"exp": time.Now().Add(time.Hour).Unix(), "who": acct}),
			"refresh_token": "r-" + acct, "account_id": acct}}
	}
	os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	os.WriteFile(filepath.Join(home, ".codex", "auth.json"), mustJSON(auth("pro@example.com", "acct-pro")), 0o600)
	os.MkdirAll(filepath.Dir(provider.Path()), 0o755)
	os.WriteFile(filepath.Join(filepath.Dir(provider.Path()), "logins.json"), mustJSON([]map[string]any{
		{"agent": "codex", "user": "biz@example.com", "on": true, "seen": time.Now(), "auth": auth("biz@example.com", "acct-biz")},
	}), 0o600)
	provider.ForgetAccounts()
	t.Cleanup(provider.ForgetAccounts)

	var mu sync.Mutex
	var tried []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		if strings.HasSuffix(r.URL.Path, "/models") {
			io.WriteString(w, `{"models":[{"slug":"gpt-5.5","visibility":"list"}]}`)
			return
		}
		mu.Lock()
		tried = append(tried, r.Header.Get("chatgpt-account-id"))
		mu.Unlock()
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r1","model":"gpt-5.5"}}`,
			`data: {"type":"response.output_text.delta","delta":"pong"}`,
			`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":7,"output_tokens":1}}}`))
	}))
	t.Cleanup(up.Close)
	old := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = old })
	p, err := provider.Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	wroteCheckpoint("gAAAAB-sealed-by-biz", candidate{p: provider.Provider{ID: "codex", Account: &provider.Account{User: "biz@example.com"}}, model: "gpt-5.5"})

	s := New()
	ask := func(session, body string) Route {
		t.Helper()
		tried = nil
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
		req.Header.Set("session_id", session)
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "pong") {
			t.Fatalf("%s: %d %s", session, rec.Code, rec.Body.String())
		}
		for _, r := range s.Trace(context.Background(), 0, 0).Routes {
			if r.Session == session {
				return r
			}
		}
		t.Fatalf("%s: no route traced", session)
		return Route{}
	}
	r := ask("sealed-1", `{"model":"codex/gpt-5.5","stream":true,"input":[{"type":"compaction","encrypted_content":"gAAAAB-sealed-by-biz"},{"type":"message","role":"user","content":[{"type":"input_text","text":"go on"}]}]}`)
	if strings.Join(tried, ",") != "acct-biz" || r.Compaction != "codex@biz@example.com" || len(r.Order) != 1 {
		t.Fatalf("sealed: tried %v, compaction %q, order %+v", tried, r.Compaction, r.Order)
	}
	r = ask("plain-1", `{"model":"codex/gpt-5.5","stream":true,"input":"ping"}`)
	if r.Compaction != "" || len(r.Order) != 2 {
		t.Fatalf("no compaction: compaction %q, order %+v", r.Compaction, r.Order)
	}
}

// A legacy /responses/compact carrying a recorded seal is judged here, as
// the same request on /responses is (serve, checkpointReaders, #2642):
// the writer on in magpie answers it, signed as the writer — relayed as
// it came, the compact reaches the sign-in, which another account's seal
// refuses (#1099, #2271), its reason unread (#2727). A writer gone is
// refused with the reason, also when the sign-in is paused and the
// compact would otherwise relay on another account (compactOn); a seal
// the sign-in wrote while it can be asked relays as it came; a request
// signing with its own chatgpt-account-id (a Codex on WSL or another
// CODEX_HOME), and a seal a magpie key's candidate wrote, are not judged
// here.
// sealedCompactAsked sends Codex's legacy /responses/compact carrying the seal
// enc, signed with auth (Codex's ChatGPT token, or a gateway key) and
// naming acct when it isn't empty, to a fake ChatGPT backend; it returns
// the answer and the account ids the backend was asked as.
func sealedCompactAsked(t *testing.T, enc, auth, acct string) (int, string, string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, r.Header.Get("chatgpt-account-id"))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r1"}}`,
			`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":9,"output_tokens":2}}}`))
	}))
	t.Cleanup(up.Close)
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })
	body := `{"model":"gpt-5.5","stream":true,"input":[{"type":"compaction","encrypted_content":"` + enc + `"},{"type":"message","role":"user","content":[{"type":"input_text","text":"go on"}]}]}`
	req := httptest.NewRequest("POST", CodexPath+"/responses/compact", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+auth)
	if acct != "" {
		req.Header.Set("chatgpt-account-id", acct)
	}
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, req)
	mu.Lock()
	defer mu.Unlock()
	return rec.Code, rec.Body.String(), strings.Join(seen, ",")
}

// sealedBy is the candidate a Codex account's compaction was recorded as
// sealed by.
func sealedBy(user string) candidate {
	return candidate{p: provider.Provider{ID: "codex", Account: &provider.Account{User: user}}, model: "gpt-5.5"}
}

func TestNativeCompactionCompactJudgesItsWriter(t *testing.T) {
	compact := func(enc, acct string) (int, string, string) {
		t.Helper()
		return sealedCompactAsked(t, enc, "chatgpt-token", acct)
	}
	account := sealedBy

	t.Run("a writer on in magpie answers it, signed as the writer", func(t *testing.T) {
		codexSignedIn(t, "biz@example.com") // the sign-in is me@example.com (acct-1); biz@example.com (acct-2) is on
		nativeOn(t, "codex")
		wroteCheckpoint("gAAAAB-sealed-by-biz", account("biz@example.com"))
		code, body, seen := compact("gAAAAB-sealed-by-biz", "")
		if code != 200 || seen != "acct-2" {
			t.Fatalf("%d, the backend saw %q: %s", code, seen, body)
		}
	})
	t.Run("a writer gone is refused here, saying why", func(t *testing.T) {
		codexSignedIn(t)
		nativeOn(t, "codex")
		wroteCheckpoint("gAAAAB-sealed-by-gone", account("gone@example.com"))
		code, body, seen := compact("gAAAAB-sealed-by-gone", "")
		if code != 400 || seen != "" {
			t.Fatalf("%d, the backend saw %q: %s", code, seen, body)
		}
		for _, want := range []string{"compacted natively by codex@gone@example.com", "gone@example.com is no longer signed in to magpie", "sign in to gone@example.com again"} {
			if !strings.Contains(body, want) {
				t.Errorf("no %q in %s", want, body)
			}
		}
	})
	t.Run("the sign-in's own seal relays", func(t *testing.T) {
		codexSignedIn(t)
		nativeOn(t, "codex")
		wroteCheckpoint("gAAAAB-sealed-by-me", account("me@example.com"))
		code, body, seen := compact("gAAAAB-sealed-by-me", "")
		if code != 200 || seen != "" { // relayed as it came: no account id added
			t.Fatalf("%d, the backend saw %q: %s", code, seen, body)
		}
	})
	t.Run("a request naming another account is not judged", func(t *testing.T) {
		codexSignedIn(t)
		nativeOn(t, "codex")
		wroteCheckpoint("gAAAAB-sealed-by-gone", account("gone@example.com"))
		code, body, seen := compact("gAAAAB-sealed-by-gone", "acct-9")
		if code != 200 || seen != "acct-9" { // relayed as it came: the header goes untouched
			t.Fatalf("%d, the backend saw %q: %s", code, seen, body)
		}
	})
	t.Run("a seal a key wrote is not judged", func(t *testing.T) {
		codexSignedIn(t)
		nativeOn(t, "codex")
		wroteCheckpoint("gAAAAB-sealed-by-key", candidate{p: provider.Provider{ID: "codex", Key: "ck_test"}})
		code, body, seen := compact("gAAAAB-sealed-by-key", "")
		if code != 200 || seen != "" { // relayed as it came: no account id added
			t.Fatalf("%d, the backend saw %q: %s", code, seen, body)
		}
	})
	t.Run("on the compactOn path a gone writer's seal is refused too", func(t *testing.T) {
		codexSignedIn(t, "biz@example.com")
		if err := provider.SetLoginOn("codex", "me@example.com", false); err != nil {
			t.Fatal(err)
		}
		nativeOn(t, "codex")
		wroteCheckpoint("gAAAAB-sealed-by-gone", account("gone@example.com"))
		code, body, seen := compact("gAAAAB-sealed-by-gone", "")
		if code != 400 || seen != "" {
			t.Fatalf("%d, the backend saw %q: %s", code, seen, body)
		}
		if !strings.Contains(body, "gone@example.com is no longer signed in to magpie") {
			t.Errorf("body %s", body)
		}
	})
	t.Run("on the compactOn path the writer on answers it", func(t *testing.T) {
		codexSignedIn(t, "biz@example.com")
		if err := provider.SetLoginOn("codex", "me@example.com", false); err != nil {
			t.Fatal(err)
		}
		nativeOn(t, "codex")
		wroteCheckpoint("gAAAAB-sealed-by-biz", account("biz@example.com"))
		code, body, seen := compact("gAAAAB-sealed-by-biz", "")
		if code != 200 || seen != "acct-2" {
			t.Fatalf("%d, the backend saw %q: %s", code, seen, body)
		}
	})
}

// A gateway key held to some accounts (#905) holds a legacy compact to
// them where the compact goes: to the seal's writer when it answers it,
// not to the sign-in the compact would otherwise relay on. A writer the
// key may not use is refused with nothing asked, though the sign-in is
// one it may; a writer it may use answers it, though the sign-in isn't.
func TestNativeCompactionCompactKeepsTheKeysAccounts(t *testing.T) {
	for _, c := range []struct {
		name, allowed string
		code          int
		seen, says    string
	}{
		{"the writer is not the key's", "codex/me@example.com", 403, "", "sealed by biz@example.com, and only that account can read it"},
		{"the writer is the key's", "codex/biz@example.com", 200, "acct-2", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			codexSignedIn(t, "biz@example.com") // the sign-in is me@example.com (acct-1)
			nativeOn(t, "codex")
			keys, secrets := newCaller(t, "Held")
			if _, err := access.Update("accounts-key", access.Change{Key: keys[0].ID, Accounts: []string{c.allowed}}); err != nil {
				t.Fatal(err)
			}
			wroteCheckpoint("gAAAAB-sealed-by-biz", sealedBy("biz@example.com"))
			code, body, seen := sealedCompactAsked(t, "gAAAAB-sealed-by-biz", secrets[0], "")
			if code != c.code || seen != c.seen || !strings.Contains(body, c.says) {
				t.Fatalf("%d, the backend saw %q: %s", code, seen, body)
			}
		})
	}
}

// A trigger Codex asks of a provider set to compact natively is kept only
// when a key that lists the model can be asked it on the Responses API.
// When the vendor's lists hold the model to a key that speaks no
// Responses there (its key is set to Chat, so WithKey leaves it no
// Responses URL), the trigger is not kept: magpie summarises instead,
// which the listed key reads, as it does when native compaction is off.
// Before, the trigger was kept because the provider (whose setting is
// what kept it) carries it, and then refused saying none of the keys that
// serve the model can be asked it on Responses — a dead end, when
// magpie's summary was one turn away.
func TestNativeCompactionTriggerSummarisedWhenNoListedKeyCarriesIt(t *testing.T) {
	noRests()
	t.Cleanup(noRests)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	// the summary comes back through the chat key, the one whose list
	// holds the model
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"content":"SUMM"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"content":"ARY"},"finish_reason":"stop"}]}`,
		`data: [DONE]`)}
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	// the first key is set to chat: it serves the model, but not on
	// Responses; the second speaks nothing in particular and would carry
	// the compaction, but the vendor's list doesn't hold the model to it
	p := provider.Provider{ID: "fake", Name: "Fake", Key: "kchat", KeyProtocol: provider.Chat, Models: []string{"m1"}}
	p.Chat = up.URL + "/v1"
	p.Responses = up.URL + "/v1"
	p.Keys = []provider.KeyAccount{{Key: "kresp"}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("fake", up.URL+"/v1", []catalog.Model{
		{ID: "m1", Keys: []string{provider.KeyID("kchat")}},
		{ID: "m2", Keys: []string{provider.KeyID("kresp")}},
	}); err != nil {
		t.Fatal(err)
	}
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(400) })
	nativeOn(t, "fake")
	code, body := nativeAsk(t, "fake/m1", compactTurn)
	if code != 200 || f.calls != 1 || !strings.Contains(string(f.got), "CONTEXT CHECKPOINT COMPACTION") || strings.Contains(string(f.got), "compaction_trigger") {
		t.Fatalf("%d, the relay asked %d times: %s", code, f.calls, body)
	}
	// the summary went by the chat key, the only one that lists the model;
	// the responses key, whose own list lacks it, was never asked
	if f.path != "/v1/chat/completions" || f.head.Get("Authorization") != "Bearer kchat" {
		t.Errorf("the summary didn't go by the listed key: %s %q", f.path, f.head.Get("Authorization"))
	}
	if !strings.Contains(body, magpieCompaction) || strings.Contains(body, "gAAAAB") {
		t.Errorf("Codex got %s", body)
	}
}

// A trigger Codex asks of a provider whose listed key can be asked the
// compaction on Responses still goes to it as Codex sent it — the gate
// that summarises when no listed key carries it changes nothing here —
// and the key whose own list lacks the model stays unasked, as an
// ordinary request leaves it.
func TestNativeCompactionTriggerThroughTheListedKeyThatCarriesIt(t *testing.T) {
	noRests()
	t.Cleanup(noRests)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	f := &fake{t: t, reply: nativeSealedReply("gAAAAB-sealed-by-listed")}
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	// neither key is set to a protocol, so the first can be asked the
	// compaction on Responses; the vendor's list holds the model to it
	// alone, and another model to the second
	p := provider.Provider{ID: "fake", Name: "Fake", Key: "kcarries", Models: []string{"m1"}}
	p.Chat = up.URL + "/v1"
	p.Responses = up.URL + "/v1"
	p.Keys = []provider.KeyAccount{{Key: "kother"}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("fake", up.URL+"/v1", []catalog.Model{
		{ID: "m1", Keys: []string{provider.KeyID("kcarries")}},
		{ID: "m2", Keys: []string{provider.KeyID("kother")}},
	}); err != nil {
		t.Fatal(err)
	}
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(400) })
	nativeOn(t, "fake")
	code, body := nativeAsk(t, "fake/m1", compactTurn)
	if code != 200 || f.calls != 1 {
		t.Fatalf("%d, the relay asked %d times: %s", code, f.calls, body)
	}
	if !strings.Contains(string(f.got), `{"type":"compaction_trigger"}`) || strings.Contains(string(f.got), "CONTEXT CHECKPOINT COMPACTION") {
		t.Errorf("the relay wasn't asked Codex's own compaction: %s", f.got)
	}
	if f.path != "/v1/responses" || f.head.Get("Authorization") != "Bearer kcarries" {
		t.Errorf("the trigger didn't go by the listed key: %s %q", f.path, f.head.Get("Authorization"))
	}
	if !strings.Contains(body, `"encrypted_content":"gAAAAB-sealed-by-listed"`) || strings.Contains(body, magpieCompaction) {
		t.Errorf("Codex got %s", body)
	}
}

// Deciding whether a provider set to compact natively keeps Codex's
// trigger asks nothing of its routing. The decision is made on every one
// of Codex's turns, compacting or not, before the turn is planned; a plan
// weighs the keys, and a provider routed in turn would move its turn along
// for a request that never went (a plan in the decision did: each turn
// began two keys along, not one).
func TestNativeCompactionDecisionLeavesTheTurnsAlone(t *testing.T) {
	noRests()
	t.Cleanup(noRests)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	setHome(t, t.TempDir())
	p := provider.Provider{ID: "fake", Name: "Fake", Key: "k1", Models: []string{"m1"}, Routing: provider.Rotate,
		Responses: "http://127.0.0.1:1/v1", Keys: []provider.KeyAccount{{Key: "k2"}, {Key: "k3"}}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	nativeOn(t, "fake")
	s := New()
	first := func() string {
		q, _, _ := provider.Resolve("fake/m1")
		cs, _ := s.plan(q, "m1", provider.Responses)
		return cs[0].p.Key
	}
	if !s.nativeCompactionFor("fake/m1") {
		t.Fatal("a provider set to compact natively, whose keys carry it, doesn't keep the trigger")
	}
	next := map[string]string{"k1": "k2", "k2": "k3", "k3": "k1"}
	for range 3 {
		was := first()
		s.nativeCompactionFor("fake/m1")
		if now := first(); now != next[was] {
			t.Fatalf("a turn began at %s, the next at %s: the decision moved the turn along", was, now)
		}
	}
}

// echo_ts (Discord #feedback): Codex's turns on GitHub Copilot, compacted
// natively there, and its auto-review on codex-auto-review through the
// ChatGPT sign-in. The review carries the conversation's history, the
// Copilot compaction in it, and OpenAI refused every review: 400
// invalid_encrypted_content "The encrypted content P4ih...bDU could not be
// verified". A seal goes only to its writer: the review, a call beside
// the conversation, goes without it, its instructions and tool evidence
// kept; a turn of the conversation on a model the writer doesn't serve is
// refused saying why, never relayed to be refused there.
func TestForeignCompactionLeftOutOfAutoReview(t *testing.T) {
	const seal = "P4ihCopilotSealedThisbDU"
	copilotFake(t, "gho_echo_ts", func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/models":
			io.WriteString(w, `{"data":[{"id":"gpt-5.3-codex","name":"GPT-5.3-Codex","model_picker_enabled":true,"policy":{"state":"enabled"},"supported_endpoints":["/responses"],"capabilities":{"type":"chat"}}]}`)
		case "/responses":
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, nativeSealedReply(seal))
		default:
			w.WriteHeader(404)
		}
	})
	var mu sync.Mutex
	var openai []string
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		openai = append(openai, r.URL.Path+" "+string(b))
		mu.Unlock()
		if strings.Contains(string(b), seal) {
			w.WriteHeader(400)
			io.WriteString(w, `{"error":{"message":"The encrypted content P4ih...bDU could not be verified. Reason: Encrypted content could not be decrypted or parsed.","type":"invalid_request_error","code":"invalid_encrypted_content"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r1"}}`,
			`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":9,"output_tokens":2}}}`))
	})
	g := &fake{t: t, reply: sse(`data: {"type":"response.completed","response":{"id":"resp_o","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`)}
	up := httptest.NewServer(g)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "other", Name: "Other", Key: "k2", Models: []string{"m1"}, Responses: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	nativeOn(t, "copilot")
	if code, body := nativeAsk(t, "copilot/gpt-5.3-codex", compactTurn); code != 200 || !strings.Contains(body, seal) {
		t.Fatalf("Copilot's compaction: %d %s", code, body)
	}
	ask := func(path, model, kind string) (int, string) {
		t.Helper()
		body := `{"model":"` + model + `","stream":true,"instructions":"You are the reviewer","input":[
		  {"type":"message","role":"developer","content":[{"type":"input_text","text":"approval policy: on-request"}]},
		  {"type":"compaction","encrypted_content":"` + seal + `"},
		  {"type":"message","role":"user","content":[{"type":"input_text","text":"clean the build folder"}]},
		  {"type":"function_call","call_id":"call_1","name":"shell","arguments":"{\"command\":[\"ls\",\"build\"]}"},
		  {"type":"function_call_output","call_id":"call_1","output":"app.o main.o"},
		  {"type":"message","role":"user","content":[{"type":"input_text","text":"Review: rm -rf build"}]}]}`
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", CodexPath+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer chatgpt-token")
		req.Header.Set("chatgpt-account-id", "acct-1")
		if kind != "" {
			req.Header.Set("x-openai-subagent", kind)
		}
		New().Handler().ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	kept := func(t *testing.T, got string) {
		t.Helper()
		if strings.Contains(got, seal) || strings.Contains(got, `"compaction"`) {
			t.Errorf("Copilot's seal went on: %s", got)
		}
		for _, want := range []string{"You are the reviewer", "approval policy: on-request", "clean the build folder", `"function_call_output"`, "app.o main.o", "Review: rm -rf build"} {
			if !strings.Contains(got, want) {
				t.Errorf("%q left out: %s", want, got)
			}
		}
	}

	t.Run("the auto-review on codex-auto-review, through the sign-in", func(t *testing.T) {
		mu.Lock()
		openai = nil
		mu.Unlock()
		code, body := ask("/responses", "codex-auto-review", "guardian")
		mu.Lock()
		defer mu.Unlock()
		if code != 200 || len(openai) != 1 {
			t.Fatalf("%d %s\nOpenAI got %q", code, body, openai)
		}
		kept(t, openai[0])
	})
	t.Run("the auto-review on a model of another provider's", func(t *testing.T) {
		g.got = nil
		code, body := ask("/responses", "other/m1", "guardian")
		if code != 200 || g.calls == 0 {
			t.Fatalf("%d %s", code, body)
		}
		kept(t, string(g.got))
	})
	t.Run("a subagent on a model of another provider's", func(t *testing.T) {
		g.got = nil
		code, body := ask("/responses", "other/m1", "collab_spawn")
		if code != 200 {
			t.Fatalf("%d %s", code, body)
		}
		kept(t, string(g.got))
	})
	t.Run("a thread's title on a model of another provider's", func(t *testing.T) {
		g.got = nil
		code, body := ask("/responses", "other/m1", "thread_title")
		if code != 200 || strings.Contains(string(g.got), seal) {
			t.Fatalf("%d %s\nother got %s", code, body, g.got)
		}
	})
	t.Run("a turn on one of Codex's own models is refused saying why", func(t *testing.T) {
		mu.Lock()
		openai = nil
		mu.Unlock()
		code, body := ask("/responses", "gpt-5.5", "")
		if code != 400 || !strings.Contains(body, "compacted natively") || !strings.Contains(body, "copilot") {
			t.Errorf("%d %s", code, body)
		}
		code, body = ask("/responses/compact", "gpt-5.5", "")
		if code != 400 || !strings.Contains(body, "compacted natively") {
			t.Errorf("compact: %d %s", code, body)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(openai) != 0 {
			t.Errorf("OpenAI was sent Copilot's seal: %q", openai)
		}
	})
	t.Run("a turn on another provider's model is refused as before", func(t *testing.T) {
		calls := g.calls
		code, body := ask("/responses", "other/m1", "")
		if code != 400 || !strings.Contains(body, "compacted natively") || g.calls != calls {
			t.Errorf("%d %s", code, body)
		}
	})
	t.Run("the writer still reads it", func(t *testing.T) {
		mu.Lock()
		openai = nil
		mu.Unlock()
		if code, body := ask("/responses", "copilot/gpt-5.3-codex", "guardian"); code != 200 {
			t.Errorf("%d %s", code, body)
		}
	})
}
