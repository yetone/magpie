package gateway

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// geminiSigUpstream is Gemini's OpenAI-compatible API as far as thought
// signatures go: the first turn answers with a call it signed, in
// extra_content, and a request that sends a call back checks the first
// call of each assistant turn has a signature, as Google does (#687).
func geminiSigUpstream(t *testing.T, sig string, stream bool) (*httptest.Server, *[]map[string]any) {
	var bodies []map[string]any
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		bodies = append(bodies, b)
		msgs, _ := b["messages"].([]any)
		answered := false
		for _, m := range msgs {
			m, _ := m.(map[string]any)
			calls, _ := m["tool_calls"].([]any)
			if len(calls) == 0 {
				continue
			}
			answered = true
			first, _ := calls[0].(map[string]any)
			extra, _ := first["extra_content"].(map[string]any)
			google, _ := extra["google"].(map[string]any)
			got, _ := google["thought_signature"].(string)
			if got == "" {
				w.WriteHeader(400)
				io.WriteString(w, `[{"error":{"code":400,"message":"Function call is missing a thought_signature in functionCall parts. This is required for tools to work correctly, and missing thought_signature may lead to degraded model performance. Additional data, function call `+"`default_api:bash`"+` , position 3. Please refer to https://ai.google.dev/gemini-api/docs/thought-signatures for more details.","status":"INVALID_ARGUMENT"}}]`)
				return
			}
		}
		if answered {
			sigReply(w, stream, map[string]any{"role": "assistant", "content": "done"}, "stop")
			return
		}
		call := map[string]any{"id": "function-call-7", "type": "function",
			"function":      map[string]any{"name": "bash", "arguments": `{"command":"ls"}`},
			"extra_content": map[string]any{"google": map[string]any{"thought_signature": sig}}}
		if stream {
			call["index"] = 0
		}
		sigReply(w, stream, map[string]any{"role": "assistant", "tool_calls": []any{call}}, "tool_calls")
	}))
	t.Cleanup(up.Close)
	return up, &bodies
}

func sigReply(w http.ResponseWriter, stream bool, msg map[string]any, finish string) {
	if !stream {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"id": "x", "object": "chat.completion", "model": "gemini-3.8-flash",
			"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": finish}},
			"usage":   map[string]any{"prompt_tokens": 5, "completion_tokens": 3}})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	for _, ch := range []map[string]any{
		{"id": "x", "model": "gemini-3.8-flash", "choices": []any{map[string]any{"index": 0, "delta": msg}}},
		{"id": "x", "model": "gemini-3.8-flash", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish}},
			"usage": map[string]any{"prompt_tokens": 5, "completion_tokens": 3}},
	} {
		b, _ := json.Marshal(ch)
		io.WriteString(w, "data: "+string(b)+"\n\n")
	}
	io.WriteString(w, "data: [DONE]\n\n")
}

func geminiSigProvider(t *testing.T, url string) *Server {
	t.Helper()
	fresh(t)
	p := provider.Provider{ID: "google", Name: "Google Gemini", Key: "k", Models: []string{"gemini-3.8-flash"}, Chat: url}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	return New()
}

func sigPost(t *testing.T, s *Server, path, body string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
	if rec.Code != 200 {
		t.Fatalf("%s: status %d: %s", path, rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// Pi on Gemini through magpie (#687): Pi speaks Chat, relayed to Gemini's
// OpenAI-compatible API as it is, and keeps a tool call's id but not its
// extra_content. The second request, the call and its result sent back,
// was turned away with "Function call is missing a thought_signature";
// the signature now comes back in the id and goes to Gemini where it
// wants it, under the call's own id.
func TestGeminiThoughtSignatureThroughPi(t *testing.T) {
	const sig = "CiQBjz1rX3Rob3VnaHQtc2lnbmF0dXJlLWJ5dGVzLS0+Pz8/Pw=="
	for _, stream := range []bool{true, false} {
		name := "whole"
		if stream {
			name = "streamed"
		}
		t.Run(name, func(t *testing.T) {
			up, bodies := geminiSigUpstream(t, sig, stream)
			s := geminiSigProvider(t, up.URL)
			first := sigPost(t, s, "/v1/chat/completions", `{"model":"google/gemini-3.8-flash","stream":`+map[bool]string{true: "true", false: "false"}[stream]+`,
				"reasoning_effort":"high","messages":[{"role":"user","content":"list files"}],
				"tools":[{"type":"function","function":{"name":"bash","parameters":{"type":"object"}}}]}`)
			// the id as Pi reads it: the first one in the reply
			i := strings.Index(first, `"id":"function-call-7`)
			if i < 0 {
				t.Fatalf("no call in the reply:\n%s", first)
			}
			id := first[i+len(`"id":"`):]
			id = id[:strings.IndexByte(id, '"')]
			// Pi's next request: the call as it kept it, no extra_content
			second := `{"model":"google/gemini-3.8-flash","stream":true,"reasoning_effort":"high","messages":[
				{"role":"user","content":"list files"},
				{"role":"assistant","content":null,"tool_calls":[{"id":"` + id + `","type":"function","function":{"name":"bash","arguments":"{\"command\":\"ls\"}"}}]},
				{"role":"tool","tool_call_id":"` + id + `","content":"a.txt"}],
				"tools":[{"type":"function","function":{"name":"bash","parameters":{"type":"object"}}}]}`
			out := sigPost(t, s, "/v1/chat/completions", second)
			if !strings.Contains(out, "done") {
				t.Fatalf("the second turn: %s", out)
			}
			sent := (*bodies)[len(*bodies)-1]
			msgs := sent["messages"].([]any)
			call := msgs[1].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)
			got := call["extra_content"].(map[string]any)["google"].(map[string]any)["thought_signature"]
			if got != sig || call["id"] != "function-call-7" || msgs[2].(map[string]any)["tool_call_id"] != "function-call-7" {
				t.Fatalf("Gemini was sent the call %v and the result for %v", call, msgs[2].(map[string]any)["tool_call_id"])
			}
		})
	}
}

// Claude Code, or any client magpie translates for, on Gemini's
// OpenAI-compatible API: the tool_use id carries the signature back the
// same way.
func TestGeminiThoughtSignatureThroughAnthropic(t *testing.T) {
	const sig = "c2lnbmF0dXJlLW9uZQ=="
	up, bodies := geminiSigUpstream(t, sig, true)
	s := geminiSigProvider(t, up.URL)
	first := sigPost(t, s, "/v1/messages", `{"model":"google/gemini-3.8-flash","max_tokens":1000,"stream":false,
		"messages":[{"role":"user","content":"list files"}],"tools":[{"name":"bash","input_schema":{"type":"object"}}]}`)
	var res struct {
		Content []struct {
			Type, ID string
		} `json:"content"`
	}
	json.Unmarshal([]byte(first), &res)
	id := ""
	for _, c := range res.Content {
		if c.Type == "tool_use" {
			id = c.ID
		}
	}
	if id == "" {
		t.Fatalf("no tool_use: %s", first)
	}
	sigPost(t, s, "/v1/messages", `{"model":"google/gemini-3.8-flash","max_tokens":1000,"stream":false,"messages":[
		{"role":"user","content":"list files"},
		{"role":"assistant","content":[{"type":"tool_use","id":"`+id+`","name":"bash","input":{"command":"ls"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"`+id+`","content":"a.txt"}]}],
		"tools":[{"name":"bash","input_schema":{"type":"object"}}]}`)
	sent := (*bodies)[len(*bodies)-1]
	msgs := sent["messages"].([]any)
	var call, result map[string]any
	for _, m := range msgs {
		m := m.(map[string]any)
		if cs, ok := m["tool_calls"].([]any); ok {
			call = cs[0].(map[string]any)
		}
		if m["role"] == "tool" {
			result = m
		}
	}
	if call == nil || result == nil {
		t.Fatalf("sent %v", msgs)
	}
	got := call["extra_content"].(map[string]any)["google"].(map[string]any)["thought_signature"]
	if got != sig || call["id"] != "function-call-7" || result["tool_call_id"] != "function-call-7" {
		t.Fatalf("Gemini was sent the call %v and the result for %v", call, result["tool_call_id"])
	}
}

// A call Gemini didn't sign — made by another model earlier in the
// conversation — goes to Gemini with the value Google documents for one,
// on the first call of the turn; a later call of a signed turn goes
// without. Another upstream gets the calls' own ids and no extra_content.
func TestGeminiCallsWithoutSignature(t *testing.T) {
	signed := signedID("call_a", "c2ln")
	body := []byte(`{"messages":[
		{"role":"assistant","tool_calls":[{"id":"toolu_1","type":"function","function":{"name":"a","arguments":"{}"}},{"id":"toolu_2","type":"function","function":{"name":"b","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"toolu_1","content":"x"},{"role":"tool","tool_call_id":"toolu_2","content":"y"},
		{"role":"assistant","tool_calls":[{"id":"` + signed + `","type":"function","function":{"name":"a","arguments":"{}"}},{"id":"call_b","type":"function","function":{"name":"b","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"` + signed + `","content":"x"},{"role":"tool","tool_call_id":"call_b","content":"y"}]}`)
	var q struct {
		Messages []struct {
			ToolCallID string `json:"tool_call_id"`
			ToolCalls  []struct {
				ID    string `json:"id"`
				Extra *struct {
					Google struct {
						Sig string `json:"thought_signature"`
					} `json:"google"`
				} `json:"extra_content"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(chatCallSignatures(body, true, "google", true), &q); err != nil {
		t.Fatal(err)
	}
	sigOf := func(m, c int) string {
		if e := q.Messages[m].ToolCalls[c].Extra; e != nil {
			return e.Google.Sig
		}
		return ""
	}
	if sigOf(0, 0) != skipSignature || sigOf(0, 1) != skipSignature {
		t.Errorf("an unsigned turn's calls went with %q, %q", sigOf(0, 0), sigOf(0, 1))
	}
	if sigOf(3, 0) != "c2ln" || sigOf(3, 1) != "" || q.Messages[3].ToolCalls[0].ID != "call_a" || q.Messages[4].ToolCallID != "call_a" {
		t.Errorf("a signed turn went as %+v", q.Messages[3:5])
	}
	other := string(chatCallSignatures(body, false, "google", true))
	if strings.Contains(other, sigMark) || strings.Contains(other, "extra_content") || !strings.Contains(other, `"id":"call_a"`) {
		t.Errorf("another upstream was sent %s", other)
	}

	// the same through magpie's own translation
	r := &Request{Messages: []Message{
		{Role: "assistant", Parts: []Part{{Kind: ToolCall, ID: "toolu_1", Name: "a"}}},
		{Role: "user", Parts: []Part{{Kind: ToolResult, CallID: "toolu_1", Text: "x"}}},
	}}
	if out := string(buildChat(r, "gemini-3.8-flash", aiStudioHost, false)); !strings.Contains(out, skipSignature) {
		t.Errorf("Gemini was sent %s", out)
	}
	if out := string(buildChat(r, "deepseek-chat", "api.deepseek.com", false)); strings.Contains(out, "extra_content") {
		t.Errorf("DeepSeek was sent %s", out)
	}
}

// An id carries any signature and gives it back exactly; one it doesn't
// carry stays as it was.
func TestSignedID(t *testing.T) {
	for _, c := range []struct{ id, sig string }{
		{"function-call-1", "CiQBjz1rX+/="},
		{"function-call-1", "c2ln"},
		{"", "c2ln"},
		{"call_x", "not base64!"},
		{"call_x", ""},
	} {
		got := signedID(c.id, c.sig)
		if strings.ContainsAny(got, "+/=! ") {
			t.Errorf("signedID(%q, %q) = %q, not an id every client takes", c.id, c.sig, got)
		}
		id, sig := unsignedID(got)
		if sig != c.sig || (c.id != "" && id != c.id) || id == "" {
			t.Errorf("signedID(%q, %q) = %q came back as %q, %q", c.id, c.sig, got, id, sig)
		}
	}
	if id, sig := unsignedID("toolu_01__ts__"); id != "toolu_01__ts__" || sig != "" {
		t.Errorf("a plain id read as %q, %q", id, sig)
	}
}

// sigFixtures are ids as magpie gives them for Gemini 3's calls: Gemini's
// own call id or magpie's call_<newID>, with a signature the size of
// vertexCallSig and of the 421-character one #1536 measured (315 bytes),
// and one that isn't canonical base64 (__tsr).
func sigFixtures() []struct{ id, sig string } {
	long := make([]byte, 315)
	for i := range long {
		long[i] = byte(i*131 + 7)
	}
	return []struct{ id, sig string }{
		{"function-call-7", vertexCallSig},
		{"call_18f2c3a4b5d6e7f800000001", base64.StdEncoding.EncodeToString(long)},
		{"call_0a1b2c", base64.StdEncoding.EncodeToString(long)},
		{"function-call-7", strings.TrimRight(gSigCall, "=")},
	}
}

// A client that shortens a call's id (#1536, jueda12: one caps ids at 40
// characters) sends back part of the signature. At three of four lengths
// what is left still decoded as base64, and Gemini answered the partial
// signature with 400 "Corrupted thought signature". An id cut anywhere
// now carries no signature, and stays as the client sent it.
func TestShortenedIDHasNoSignature(t *testing.T) {
	for _, c := range sigFixtures() {
		full := signedID(c.id, c.sig)
		if id, sig := unsignedID(full); id != c.id || sig != c.sig {
			t.Fatalf("signedID(%q, …) = %q came back as %q, %q", c.id, full, id, sig)
		}
		for n := range len(full) {
			cut := full[:n]
			if id, sig := unsignedID(cut); sig != "" || id != cut {
				t.Errorf("%q cut to %d: read as %q with a signature of %d characters", c.id, n, id, len(sig))
			}
		}
	}
}

// Ids magpie gave before #1536, with no length in their mark, still give
// their signatures back: a conversation started on an older magpie goes on.
func TestOldSignedIDsStillRead(t *testing.T) {
	for _, c := range sigFixtures() {
		var old string
		if b, err := base64.StdEncoding.DecodeString(c.sig); err == nil && base64.StdEncoding.EncodeToString(b) == c.sig {
			old = c.id + "__ts__" + base64.RawURLEncoding.EncodeToString(b)
		} else {
			old = c.id + "__tsr__" + base64.RawURLEncoding.EncodeToString([]byte(c.sig))
		}
		if id, sig := unsignedID(old); id != c.id || sig != c.sig {
			t.Errorf("old id %q read as %q, %q", old, id, sig)
		}
	}
	// literal ids of the old form (from ump45nose's patch on #1536)
	if id, sig := unsignedID("call_a__ts__c2ln"); id != "call_a" || sig != "c2ln" {
		t.Errorf("an old signed id read as %q, %q", id, sig)
	}
	if id, sig := unsignedID("call_a__tsr__bm90IGJhc2U2NCE"); id != "call_a" || sig != "not base64!" {
		t.Errorf("an old raw-signed id read as %q, %q", id, sig)
	}
	for _, id := range []string{"toolu_01__ts__", "call__tsx__ts", "a__ts12", "a__ts12__", "a__ts3__YWJj", "a__ts5__YWJj", "a__ts04__YWJj"} {
		if got, sig := unsignedID(id); got != id || sig != "" {
			t.Errorf("%q read as %q, %q", id, got, sig)
		}
	}
	if id, sig := unsignedID("a__tsx_b__ts4__YWJj"); id != "a__tsx_b" || sig != "YWJj" {
		t.Errorf("a mark after a look-alike read as %q, %q", id, sig)
	}
}

// Every path that takes a signature off an id sends Gemini the skip value,
// never part of a signature, for an id cut at any length: Chat relayed as
// it is, magpie's own Chat, Gemini and Code Assist (Antigravity) requests.
func TestShortenedIDGoesUnsigned(t *testing.T) {
	for _, c := range sigFixtures() {
		full := signedID(c.id, c.sig)
		for n := len(c.id) + 1; n < len(full); n++ {
			cut := full[:n]
			body := []byte(`{"messages":[{"role":"user","content":"list files"},
				{"role":"assistant","tool_calls":[{"id":"` + cut + `","type":"function","function":{"name":"bash","arguments":"{}"}}]},
				{"role":"tool","tool_call_id":"` + cut + `","content":"a.txt"}]}`)
			if relayed := string(chatCallSignatures(body, true, "google", true)); !strings.Contains(relayed, `"thought_signature":"`+skipSignature+`"`) {
				t.Fatalf("%q cut to %d: relayed as %s", c.id, n, relayed)
			}
			r := &Request{Messages: []Message{
				{Role: "user", Parts: []Part{{Kind: Text, Text: "list files"}}},
				{Role: "assistant", Parts: []Part{{Kind: ToolCall, ID: cut, Name: "bash"}}},
				{Role: "user", Parts: []Part{{Kind: ToolResult, CallID: cut, Text: "a.txt"}}},
			}}
			unsignCalls(r)
			if s := r.Messages[1].Parts[0].Signature; s != "" {
				t.Fatalf("%q cut to %d: the call has a signature of %d characters", c.id, n, len(s))
			}
			if out := string(buildChat(r, "gemini-3.8-flash", aiStudioHost, false)); !strings.Contains(out, skipSignature) {
				t.Fatalf("%q cut to %d: Gemini's Chat was sent %s", c.id, n, out)
			}
			if out := string(buildCodeAssist(r, "gemini-3.8-flash", "")); !strings.Contains(out, `"thoughtSignature":"`+skipSignature+`"`) {
				t.Fatalf("%q cut to %d: Code Assist was sent %s", c.id, n, out)
			}
			for _, p := range geminiParts(r.Messages[1].Parts) {
				if s, ok := p["thoughtSignature"]; ok {
					t.Fatalf("%q cut to %d: Gemini was sent the signature %v", c.id, n, s)
				}
			}
		}
	}
}

// The reporter's case through the gateway: a client keeps 40 characters
// of a call's id (#1536). Gemini's call id with a reference to its
// signature fits in 40 now (#2411), so Gemini gets the signature back;
// cut shorter, the call is sent with skip_thought_signature_validator.
func TestFortyCharacterIDThroughGateway(t *testing.T) {
	sig := sigFixtures()[1].sig
	for _, c := range []struct {
		keep int
		want string
	}{{40, sig}, {30, skipSignature}} {
		up, bodies := geminiSigUpstream(t, sig, true)
		s := geminiSigProvider(t, up.URL)
		first := sigPost(t, s, "/v1/chat/completions", `{"model":"google/gemini-3.8-flash","stream":true,"messages":[{"role":"user","content":"list files"}],
		"tools":[{"type":"function","function":{"name":"bash","parameters":{"type":"object"}}}]}`)
		i := strings.Index(first, `"id":"function-call-7`)
		if i < 0 {
			t.Fatalf("no call in the reply:\n%s", first)
		}
		id := first[i+len(`"id":"`):]
		id = id[:strings.IndexByte(id, '"')]
		if len(id) > 40 || len(id) <= c.keep-10 {
			t.Fatalf("the id %q is %d characters", id, len(id))
		}
		id = id[:min(len(id), c.keep)]
		out := sigPost(t, s, "/v1/chat/completions", `{"model":"google/gemini-3.8-flash","stream":true,"messages":[
		{"role":"user","content":"list files"},
		{"role":"assistant","content":null,"tool_calls":[{"id":"`+id+`","type":"function","function":{"name":"bash","arguments":"{\"command\":\"ls\"}"}}]},
		{"role":"tool","tool_call_id":"`+id+`","content":"a.txt"}],
		"tools":[{"type":"function","function":{"name":"bash","parameters":{"type":"object"}}}]}`)
		if !strings.Contains(out, "done") {
			t.Fatalf("kept %d, the second turn: %s", c.keep, out)
		}
		msgs := (*bodies)[len(*bodies)-1]["messages"].([]any)
		call := msgs[1].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)
		if got := call["extra_content"].(map[string]any)["google"].(map[string]any)["thought_signature"]; got != c.want {
			t.Fatalf("kept %d: Gemini was sent the call %v", c.keep, call)
		}
	}
}

// #2411 (liangxyz): a signature of several KB made a call's id as long,
// and LobeHub's PostgreSQL index (2704 bytes) turned the row away. The id
// now refers to the signature magpie keeps: 6.4KB, the size in the
// report, gives an id of 64 characters at most, and the signature comes
// back whole.
func TestLongSignatureGivesShortID(t *testing.T) {
	long := make([]byte, 4800)
	for i := range long {
		long[i] = byte(i*131 + 7)
	}
	sig := base64.StdEncoding.EncodeToString(long)
	for _, own := range []string{"function-call-7", "call_811277", ""} {
		id := signedID(own, sig)
		if len(id) > 64 {
			t.Fatalf("signedID(%q, %d bytes) is %d characters: %q", own, len(sig), len(id), id)
		}
		got, back := unsignedID(id)
		if back != sig || (own != "" && got != own) {
			t.Fatalf("%q came back as %q with %d bytes of signature", id, got, len(back))
		}
	}
}

// A reference magpie no longer has (pruned, or a folder from another
// computer) or whose file was changed reads as no signature, so the call
// goes to Gemini unsigned rather than with someone else's signature.
func TestKeptSignatureGoneOrChanged(t *testing.T) {
	sig := sigFixtures()[1].sig
	id := signedID("function-call-7", sig)
	if !strings.Contains(id, sigMark+sigKept) {
		t.Fatalf("the signature wasn't kept: %q", id)
	}
	key := id[strings.LastIndex(id, sigEnd)+len(sigEnd):]
	path := filepath.Join(sigDir(), key)
	if err := os.WriteFile(path, []byte(sigFixtures()[0].sig), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, back := unsignedID(id); got != id || back != "" {
		t.Fatalf("a changed file read as %q, %d bytes", got, len(back))
	}
	os.Remove(path)
	if got, back := unsignedID(id); got != id || back != "" {
		t.Fatalf("a gone file read as %q, %d bytes", got, len(back))
	}
	body := []byte(`{"messages":[{"role":"assistant","tool_calls":[{"id":"` + id + `","type":"function","function":{"name":"bash","arguments":"{}"}}]}]}`)
	if out := string(chatCallSignatures(body, true, "google", true)); !strings.Contains(out, `"thought_signature":"`+skipSignature+`"`) {
		t.Fatalf("relayed as %s", out)
	}
	// written again, it is read again
	if again := signedID("function-call-7", sig); again != id {
		t.Fatalf("the same signature has a new id %q", again)
	}
	if _, back := unsignedID(id); back != sig {
		t.Fatalf("a signature written again read as %d bytes", len(back))
	}
}

// Where magpie can't keep a signature (its folder can't be written), the
// id carries the signature itself, as before #2411.
func TestUnkeptSignatureRidesInTheID(t *testing.T) {
	dir := sigDir
	t.Cleanup(func() { sigDir = dir })
	blocked := filepath.Join(t.TempDir(), "file")
	os.WriteFile(blocked, nil, 0o600)
	sigDir = func() string { return filepath.Join(blocked, "gemini-signatures") }
	sig := sigFixtures()[1].sig
	id := signedID("function-call-7", sig)
	if strings.Contains(id, sigMark+sigKept) {
		t.Fatalf("kept in a folder that can't be written: %q", id)
	}
	if got, back := unsignedID(id); got != "function-call-7" || back != sig {
		t.Fatalf("%q came back as %q, %d bytes", id, got, len(back))
	}
}

// A signature not read for sigKeep goes when the folder is next pruned; one
// read lately stays.
func TestOldSignaturesPruned(t *testing.T) {
	tick, dir := sigPruneTick, sigDir
	t.Cleanup(func() { sigPruneTick, sigDir = tick, dir })
	folder := t.TempDir()
	sigPruneTick, sigDir = 0, func() string { return folder }
	oldID, keptID := signedID("a", sigFixtures()[0].sig), signedID("b", sigFixtures()[1].sig)
	key := func(id string) string { return filepath.Join(sigDir(), id[strings.LastIndex(id, sigEnd)+len(sigEnd):]) }
	past := time.Now().Add(-sigKeep - time.Hour)
	os.Chtimes(key(oldID), past, past)
	os.Chtimes(key(keptID), past, past)
	unsignedID(keptID)                // read: kept another sigKeep
	signedID("c", "c2lnbmF0dXJlLWM=") // a new signature written: the folder is pruned
	if _, err := os.Stat(key(oldID)); !os.IsNotExist(err) {
		t.Fatalf("an unread signature stayed: %v", err)
	}
	if _, back := unsignedID(keptID); back != sigFixtures()[1].sig {
		t.Fatal("a signature read lately was pruned")
	}
}
