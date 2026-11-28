package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/provider"
)

// A Gemini client asking responseMimeType application/json with no schema
// takes any JSON value, an array as well as an object. parseGemini says so
// in the system prompt already, so an upstream told the format in words
// (inSystem) is not told "a single JSON object" on top of it: told both, a
// model asked for a list wrapped it as {"items":[...]}. That is a Messages
// upstream, which can't be asked for any JSON in a field, and a Chat one
// that turned json_object away (DeepSeek's "This response_format type is
// unavailable now", formatRefused), asked again with it in words.
func TestGeminiJSONToldOnce(t *testing.T) {
	const ask = `{"systemInstruction":{"parts":[{"text":"be brief"}]},
		"contents":[{"role":"user","parts":[{"text":"list three colors as a JSON array"}]}],
		"generationConfig":{"temperature":0,"responseMimeType":"application/json"}}`
	toldOnce := func(t *testing.T, system string) {
		t.Helper()
		if strings.Contains(system, "JSON object") || strings.Count(system, "Respond with a single JSON value and nothing else.") != 1 {
			t.Errorf("system sent upstream:\n%s\nwant the one ask for any JSON value", system)
		}
		if !strings.Contains(system, "be brief") {
			t.Errorf("system sent upstream lost the client's instruction:\n%s", system)
		}
	}
	t.Run("messages", func(t *testing.T) {
		f := &fake{t: t, reply: sse(
			`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_1","model":"m1","usage":{"input_tokens":7}}}`,
			`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"[\"red\",\"green\",\"blue\"]"}}`,
			`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`,
			`event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
			`event: message_stop`+"\n"+`data: {"type":"message_stop"}`)}
		setup(t, provider.Anthropic, f)
		if code, body := post(t, "/v1beta/models/fake/m1:generateContent", ask); code != 200 {
			t.Fatalf("status %d: %s", code, body)
		}
		toldOnce(t, gjson.GetBytes(f.got, "system").Raw)
	})
	t.Run("chat that turns json_object away", func(t *testing.T) {
		fresh(t)
		up := &formatRefuser{object: true}
		srv := httptest.NewServer(up)
		t.Cleanup(srv.Close)
		if err := provider.Save(provider.Provider{ID: "ds", Name: "DS", Key: "k", Chat: srv.URL + "/v1", Models: []string{"deepseek-chat"}}); err != nil {
			t.Fatal(err)
		}
		if code, body := post(t, "/v1beta/models/ds/deepseek-chat:generateContent", ask); code != 200 {
			t.Fatalf("status %d: %s", code, body)
		}
		bodies := up.all()
		if len(bodies) != 2 || bodies[1]["response_format"] != nil {
			t.Fatalf("want json_object refused, then asked again without it: %s", jsonOf(bodies))
		}
		toldOnce(t, jsonOf(bodies[1]["messages"]))
	})
}
