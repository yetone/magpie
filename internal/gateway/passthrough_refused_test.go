package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// passthroughRefusalBody is the ChatGPT backend's 400 in xuanwo's
// screenshot (on X), the message as it reached the Codex app after
// magpie's "Codex: ".
const passthroughRefusalBody = `{"error":{"code":null,"message":"[ObjectParam] [input[2].internal_chat_message_metadata_passthrough.content_token_count] [unknown_parameter] Unknown parameter: 'input[2].internal_chat_message_metadata_passthrough.content_token_count'.","param":null,"type":"invalid_request_error"}}`

// passthroughTurn is a Codex turn whose items carry the passthrough with
// the member the backend doesn't know, and one item without a passthrough.
const passthroughTurn = `{"model":"%s","stream":false,"input":[` +
	`{"type":"message","role":"developer","content":[{"type":"input_text","text":"be brief"}],"internal_chat_message_metadata_passthrough":{"turn_id":"t1","content_token_count":[3]}},` +
	`{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},` +
	`{"type":"message","role":"user","content":[{"type":"input_text","text":"again"}],"internal_chat_message_metadata_passthrough":{"turn_id":"t2","content_item_kinds":["text"],"content_token_count":[1]}}]}`

func passthroughHas(q []byte, field string) bool {
	for _, it := range gjson.GetBytes(q, "input").Array() {
		if it.Get(passthroughKey + "." + field).Exists() {
			return true
		}
	}
	return false
}

// A newer Codex app's passthrough member the ChatGPT backend doesn't know
// turned every new conversation away (xuanwo on X). The turn is asked
// once more without that member, the rest of the passthrough kept, and
// the next turn goes without it at once.
func TestPassthroughMemberLeftOutWhereRefused(t *testing.T) {
	fresh(t)
	// an OpenAI host, which is sent the passthrough as Codex wrote it
	if err := provider.Save(provider.Provider{ID: "chatgpt", Name: "Codex", Key: "k", Responses: "https://api.openai.com/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var sent [][]byte
	s := New()
	s.client = &http.Client{Transport: countTransport(func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		sent = append(sent, b)
		mu.Unlock()
		if passthroughHas(b, "content_token_count") {
			return &http.Response{StatusCode: 400, Header: http.Header{"Content-Type": {"application/json"}}, Request: r,
				Body: io.NopCloser(strings.NewReader(passthroughRefusalBody))}, nil
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Request: r,
			Body: io.NopCloser(strings.NewReader(`{"id":"r1","object":"response","status":"completed","output":[{"type":"message","id":"m1","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":5,"output_tokens":2}}`))}, nil
	})}
	ask := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		body := strings.Replace(passthroughTurn, "%s", "chatgpt/m1", 1)
		s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body)))
		return rec
	}
	if rec := ask(); rec.Code != 200 || !strings.Contains(rec.Body.String(), "ok") {
		t.Fatalf("first turn: %d %s (asked %d)", rec.Code, rec.Body, len(sent))
	}
	if len(sent) != 2 || !passthroughHas(sent[0], "content_token_count") || passthroughHas(sent[1], "content_token_count") {
		t.Fatalf("asked %d times: %s", len(sent), sent)
	}
	again := sent[1]
	if gjson.GetBytes(again, "input.0."+passthroughKey+".turn_id").String() != "t1" ||
		gjson.GetBytes(again, "input.2."+passthroughKey+".content_item_kinds.0").String() != "text" ||
		gjson.GetBytes(again, "input.#").Int() != 3 || gjson.GetBytes(again, "input.2.content.0.text").String() != "again" {
		t.Fatalf("asked again with: %s", again)
	}
	if rec := ask(); rec.Code != 200 || len(sent) != 3 || passthroughHas(sent[2], "content_token_count") {
		t.Fatalf("next turn: %d, asked %d times", rec.Code, len(sent))
	}
}

// The refusal is known in each place it can name the passthrough; another
// unknown parameter, or an item's other field, is not taken for it.
func TestPassthroughRefusalWordings(t *testing.T) {
	for body, want := range map[string]string{
		passthroughRefusalBody: "content_token_count",
		"Codex: [ObjectParam] [input[55].internal_chat_message_metadata_passthrough.content_token_count] [unknown_parameter] Unknown parameter: 'input[55].internal_chat_message_metadata_passthrough.content_token_count'.":                     "content_token_count",
		`{"error":{"message":"Unknown parameter: 'input[0].internal_chat_message_metadata_passthrough.content_item_metadata'.","param":"input[0].internal_chat_message_metadata_passthrough.content_item_metadata","code":"unknown_parameter"}}`: "content_item_metadata",
		`{"error":{"message":"Unknown parameter: 'input[4].internal_chat_message_metadata_passthrough'.","code":"unknown_parameter"}}`:                                                                                                           "",
	} {
		got, ok := refusedPassthrough([]byte(body))
		if !ok || got != want {
			t.Errorf("%s: %q %v, want %q", body, got, ok, want)
		}
	}
	for _, body := range []string{
		`{"error":{"message":"Unknown parameter: 'input[0].foo'.","code":"unknown_parameter"}}`,
		`{"error":{"message":"Invalid type for 'input[0].internal_chat_message_metadata_passthrough.turn_id': expected a string.","code":"invalid_type"}}`,
		`{"error":{"message":"Unsupported parameter: 'store'."}}`,
	} {
		if f, ok := refusedPassthrough([]byte(body)); ok {
			t.Errorf("taken for a passthrough refusal (%q): %s", f, body)
		}
	}
}

// Only the member goes, from every item that has it; every other byte
// stays as Codex sent it. For the passthrough as a whole, each item's goes.
func TestWithoutPassthrough(t *testing.T) {
	body := []byte(strings.Replace(passthroughTurn, "%s", "gpt-6", 1))
	b, ok := withoutPassthrough(body, "content_token_count")
	if !ok || passthroughHas(b, "content_token_count") || !json.Valid(b) {
		t.Fatalf("%v %s", ok, b)
	}
	want := strings.NewReplacer(`,"content_token_count":[3]`, "", `,"content_token_count":[1]`, "").Replace(string(body))
	if string(b) != want {
		t.Fatalf("got  %s\nwant %s", b, want)
	}
	if _, ok := withoutPassthrough(b, "content_token_count"); ok {
		t.Fatal("removed it twice")
	}
	all, ok := withoutPassthrough(body, "")
	if !ok || strings.Contains(string(all), passthroughKey) || gjson.GetBytes(all, "input.#").Int() != 3 || gjson.GetBytes(all, "input.2.content.0.text").String() != "again" {
		t.Fatalf("%v %s", ok, all)
	}
	// the signed-in Codex relay asks again the same way
	if b2, ok := withoutUnreadable(body, []byte(passthroughRefusalBody)); !ok || string(b2) != want {
		t.Fatalf("withoutUnreadable: %v %s", ok, b2)
	}
}

// passthroughBody is a Codex conversation of n items, each with size
// bytes of text, in the shapes an input item can take: the passthrough
// first, last or missing, null, empty, an array, written with spaces and
// line breaks, with escaped names, and its key inside an item's text or a
// nested object, where it isn't the item's.
func passthroughBody(n, size int) []byte {
	text := strings.Repeat("x", size)
	var b strings.Builder
	b.WriteString(`{"model":"gpt-6","input":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		switch i % 9 {
		case 0:
			b.WriteString(`{"type":"message","role":"user","content":[{"type":"input_text","text":"` + text + `"}],"internal_chat_message_metadata_passthrough":{"turn_id":"t` + strconv.Itoa(i) + `","content_token_count":[3],"content_item_kinds":["text"]}}`)
		case 1:
			b.WriteString(`{"internal_chat_message_metadata_passthrough":{"content_token_count":[1]},"type":"message","role":"assistant","content":[{"type":"output_text","text":"` + text + `"}]}`)
		case 2:
			b.WriteString(`{"type":"message","role":"user","content":[{"type":"input_text","text":"` + text + `"}]}`)
		case 3:
			b.WriteString("\n  {\n    \"type\": \"message\",\n    \"content\": [{\"type\": \"input_text\", \"text\": \"" + text + "\"}],\n    \"internal_chat_message_metadata_passthrough\" : {\n      \"turn_id\" : \"t\",\n      \"content\\u005ftoken_count\" : [2]\n    }\n  }\n")
		case 4:
			b.WriteString(`{"type":"message","content":[{"type":"input_text","text":"\"internal_chat_message_metadata_passthrough\":{\"content_token_count\":[9]} ` + text + `"}],"internal_chat_message_metadata_passthrough":null}`)
		case 5:
			b.WriteString(`{"type":"function_call_output","output":{"internal_chat_message_metadata_passthrough":{"content_token_count":[4]}},"internal_chat_message_metadata_passthrough":{}}`)
		case 6:
			b.WriteString(`"` + text + `"`)
		case 7:
			b.WriteString(`{"type":"message","content":"` + text + `","internal_chat_message_metadata_passthrough":["content_token_count"]}`)
		case 8:
			b.WriteString(`{"type":"reasoning","encrypted_content":"` + text + `","internal_chat_message_metadata_passthrough":{"content_token_count":[5]}}`)
		}
	}
	b.WriteString(`],"stream":true}`)
	return []byte(b.String())
}

// beforeWithoutPassthroughIf is withoutPassthroughIf as it was before it
// read the body once: each item looked up again from the body's start.
// The one it is held to.
func beforeWithoutPassthroughIf(body []byte, drop func(field string) bool) ([]byte, bool) {
	if !gjson.ValidBytes(body) {
		return body, false
	}
	n := int(gjson.GetBytes(body, "input.#").Int())
	type cut struct {
		at, end int
		with    []byte
	}
	var cuts []cut
	for i := 0; i < n; i++ {
		item := gjson.GetBytes(body, "input."+strconv.Itoa(i))
		pt := gjson.GetBytes(body, "input."+strconv.Itoa(i)+"."+passthroughKey)
		if !pt.Exists() || pt.Index <= 0 || item.Index <= 0 {
			continue
		}
		if drop("") {
			var m map[string]json.RawMessage
			if json.Unmarshal([]byte(item.Raw), &m) != nil {
				continue
			}
			delete(m, passthroughKey)
			nb, err := json.Marshal(m)
			if err != nil {
				continue
			}
			cuts = append(cuts, cut{item.Index, item.Index + len(item.Raw), nb})
			continue
		}
		if !pt.IsObject() {
			continue
		}
		nb := []byte{'{'}
		had := false
		pt.ForEach(func(k, v gjson.Result) bool {
			if k.String() != "" && drop(k.String()) {
				had = true
				return true
			}
			if len(nb) > 1 {
				nb = append(nb, ',')
			}
			nb = append(append(append(nb, k.Raw...), ':'), v.Raw...)
			return true
		})
		if !had {
			continue
		}
		nb = append(nb, '}')
		cuts = append(cuts, cut{pt.Index, pt.Index + len(pt.Raw), nb})
	}
	if len(cuts) == 0 {
		return body, false
	}
	out := body
	for i := len(cuts) - 1; i >= 0; i-- {
		c := cuts[i]
		nb := make([]byte, 0, len(out)-(c.end-c.at)+len(c.with))
		nb = append(nb, out[:c.at]...)
		nb = append(nb, c.with...)
		out = append(nb, out[c.end:]...)
	}
	return out, true
}

// Reading the body once changes no byte of what goes out: every shape an
// item takes, every member dropped, one, several, none or the whole
// passthrough, gives what the item-by-item lookup gave. (A body with two
// "input" keys, which no client sends, is the one place they differ: the
// lookup counted the first one's items and could cut the second's.)
func TestWithoutPassthroughSameAsBefore(t *testing.T) {
	drops := map[string]func(string) bool{
		"none":    func(string) bool { return false },
		"member":  func(f string) bool { return f == "content_token_count" },
		"members": func(f string) bool { return f == "content_token_count" || f == "turn_id" },
		"every":   func(f string) bool { return f != "" },
		"whole":   func(f string) bool { return f == "" },
	}
	bodies := map[string][]byte{
		"conversation": passthroughBody(200, 40),
		"turn":         []byte(strings.Replace(passthroughTurn, "%s", "gpt-6", 1)),
		"no input":     []byte(`{"model":"gpt-6","internal_chat_message_metadata_passthrough":{"content_token_count":[1]}}`),
		"input object": []byte(`{"input":{"0":{"internal_chat_message_metadata_passthrough":{"content_token_count":[1]}}}}`),
		"input text":   []byte(`{"input":"internal_chat_message_metadata_passthrough"}`),
		"empty":        []byte(`{"input":[]}`),
		"not json":     []byte(`{"input":[{"internal_chat_message_metadata_passthrough":{"content_token_count":[1]}}`),
		"key twice":    []byte(`{"input":[{"internal_chat_message_metadata_passthrough":{"turn_id":"a"},"internal_chat_message_metadata_passthrough":{"content_token_count":[1]}}]}`),
		"spaced":       []byte("{ \"input\" :\n[ {\"internal_chat_message_metadata_passthrough\":{ \"content_token_count\" : [1] , \"turn_id\":\"t\" }} ,\n{\"x\":1} ] }"),
	}
	for bn, body := range bodies {
		for dn, drop := range drops {
			want, wantOK := beforeWithoutPassthroughIf(body, drop)
			got, gotOK := withoutPassthroughIf(body, drop)
			if gotOK != wantOK || string(got) != string(want) {
				t.Errorf("%s, %s: %v %s\nbefore %v %s", bn, dn, gotOK, got, wantOK, want)
			}
		}
	}
}

// A provider that never turned a passthrough member away, nearly every
// one, gets each request's body as it came without its being read: a long
// conversation was parsed item by item on every request (BigFish on
// Discord). Once one member is turned away, that member goes, for that
// provider alone.
func TestNoPassthroughRefusalReadsNothing(t *testing.T) {
	fresh(t)
	s := New()
	body := passthroughBody(50, 20)
	from := passthroughScans.Load()
	got := s.withoutRefusedPassthrough("chatgpt", provider.Responses, body)
	if n := passthroughScans.Load() - from; n != 0 || len(got) != len(body) || &got[0] != &body[0] {
		t.Fatalf("read %d times with nothing turned away; same body: %v", n, len(got) == len(body) && &got[0] == &body[0])
	}
	s.markUnfit("chatgpt", passthroughRefused("content_token_count"), provider.Responses)
	if got := s.withoutRefusedPassthrough("other", provider.Responses, body); &got[0] != &body[0] || passthroughScans.Load() != from {
		t.Fatal("another provider's refusal was applied")
	}
	if got := s.withoutRefusedPassthrough("chatgpt", provider.Chat, body); &got[0] != &body[0] || passthroughScans.Load() != from {
		t.Fatal("another API's refusal was applied")
	}
	got = s.withoutRefusedPassthrough("chatgpt", provider.Responses, body)
	want, _ := beforeWithoutPassthroughIf(body, func(f string) bool { return f == "content_token_count" })
	if string(got) != string(want) || passthroughHas(got, "content_token_count") || !passthroughHas(got, "turn_id") {
		t.Fatalf("member turned away still sent, or more went:\n%s", got)
	}
	s.markUnfit("chatgpt", passthroughRefused(""), provider.Responses)
	got = s.withoutRefusedPassthrough("chatgpt", provider.Responses, body)
	if want, _ := beforeWithoutPassthroughIf(body, func(f string) bool { return f == "" }); string(got) != string(want) {
		t.Fatalf("whole passthrough turned away: %s", got)
	}
}

// BenchmarkWithoutPassthrough is a long Codex conversation, ~20MB in
// 2000 items: sent to a provider that turned nothing away, and with one
// member turned away, read once and item by item as before.
func BenchmarkWithoutPassthrough(b *testing.B) {
	body := passthroughBody(2000, 10000)
	drop := func(f string) bool { return f == "content_token_count" }
	b.Run("unrefused", func(b *testing.B) {
		s := &Server{unfit: map[string]bool{}}
		b.SetBytes(int64(len(body)))
		for i := 0; i < b.N; i++ {
			s.withoutRefusedPassthrough("chatgpt", provider.Responses, body)
		}
	})
	b.Run("once", func(b *testing.B) {
		b.SetBytes(int64(len(body)))
		for i := 0; i < b.N; i++ {
			withoutPassthroughIf(body, drop)
		}
	})
	b.Run("before", func(b *testing.B) {
		b.SetBytes(int64(len(body)))
		for i := 0; i < b.N; i++ {
			beforeWithoutPassthroughIf(body, drop)
		}
	})
}
