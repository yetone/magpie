package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func clientResponseID(t *testing.T, proto provider.Protocol, body string, stream bool) string {
	t.Helper()
	id := ""
	see := func(data string) {
		var obj map[string]json.RawMessage
		if json.Unmarshal([]byte(data), &obj) != nil {
			return
		}
		if stream {
			if proto == provider.Responses {
				if json.Unmarshal(obj["response"], &obj) != nil {
					return
				}
			}
			if proto == provider.Anthropic {
				if json.Unmarshal(obj["message"], &obj) != nil {
					return
				}
			}
		}
		key := "id"
		if proto == provider.Gemini {
			key = "responseId"
		}
		var got string
		json.Unmarshal(obj[key], &got)
		if got != "" {
			if id != "" && got != id {
				t.Fatalf("client received inconsistent IDs %q %q", id, got)
			}
			id = got
		}
	}
	if stream {
		if err := readSSE(strings.NewReader(body), func(_, data string) error { see(data); return nil }); err != nil {
			t.Fatal(err)
		}
	} else {
		see(body)
	}
	if id == "" {
		t.Fatalf("client response ID missing in %s", body)
	}
	return id
}

func TestEncoderUsageKeepsActualClientResponseID(t *testing.T) {
	for _, proto := range []provider.Protocol{provider.Chat, provider.Responses, provider.Anthropic, provider.Gemini} {
		for _, upstreamID := range []string{"", "upstream", "resp_original"} {
			for _, stream := range []bool{false, true} {
				t.Run(string(proto)+"/"+upstreamID+"/"+map[bool]string{true: "stream", false: "json"}[stream], func(t *testing.T) {
					req := &Request{Model: "model", Stream: stream}
					u := Usage{RequestID: "request-header"}
					rec := httptest.NewRecorder()
					if stream {
						enc := encoder(proto, newSSEWriter(rec), req, &u)
						enc.event(Event{Kind: KStart, MsgID: upstreamID})
						enc.event(Event{Kind: KText, Text: "answer"})
						enc.event(Event{Kind: KUsage, Usage: Usage{Input: 10, Output: 2}})
						enc.finish()
					} else {
						rec.Write(renderUsage(proto, Result{ID: upstreamID, Model: "model", Parts: []Part{{Kind: Text, Text: "answer"}}}, req, &u))
					}
					want := clientResponseID(t, proto, rec.Body.String(), stream)
					if u.ResponseID != want || u.RequestID != "request-header" {
						t.Fatalf("usage %+v, client %q", u, want)
					}
					if upstreamID == "upstream" && proto == provider.Responses && want != "resp_upstream" {
						t.Fatal(want)
					}
				})
			}
		}
	}
}

func TestSnifferResponseIDIgnoresItemIDs(t *testing.T) {
	for _, tt := range []struct {
		name           string
		proto          provider.Protocol
		ct, body, want string
	}{
		{"response JSON", provider.Responses, "application/json", `{"id":"resp_a","output":[{"id":"msg_tool"}],"usage":{"input_tokens":10,"output_tokens":2}}`, "resp_a"},
		{"response multiline truncated SSE", provider.Responses, "text/event-stream", "data: {\"type\":\"response.created\",\n" + "data: \"response\":{\"id\":\"resp_a\"}}\n\n" + "data: {\"type\":\"response.output_item.added\",\"id\":\"item_event\",\"item\":{\"id\":\"msg_tool\"}}\n", "resp_a"},
		{"response error terminal", provider.Responses, "text/event-stream", "data: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_failed\",\"error\":{\"message\":\"failed\"}}}\n\n", "resp_failed"},
		{"response only item", provider.Responses, "text/event-stream", "data: {\"type\":\"response.output_item.added\",\"id\":\"item_event\",\"item\":{\"id\":\"msg_tool\"}}\n\n", ""},
		{"Claude blocks", provider.Anthropic, "text/event-stream", "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_a\"}}\n\ndata: {\"type\":\"content_block_start\",\"id\":\"block\",\"content_block\":{\"id\":\"tool_b\"}}\n\n", "msg_a"},
		{"chat error ID", provider.Chat, "text/event-stream", "data: {\"id\":\"error-event\",\"error\":{\"message\":\"failed\"}}\n\n", ""},
		{"chat JSON", provider.Chat, "application/json", `{"id":"chatcmpl_a","choices":[{"message":{"tool_calls":[{"id":"tool_b"}]}}]}`, "chatcmpl_a"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := newSniffer(tt.proto, tt.ct)
			for _, b := range []byte(tt.body) {
				s.write([]byte{b})
			}
			if u := s.usage(); u.ResponseID != tt.want {
				t.Fatalf("response ID %q want %q", u.ResponseID, tt.want)
			}
		})
	}
}

func TestGatewayPersistsTranslatedClientResponseID(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{true: "stream", false: "json"}[stream], func(t *testing.T) {
			fresh(t)
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("X-Request-Id", "req-header")
				w.Write([]byte("data: {\"id\":\"chatcmpl-upstream\",\"model\":\"model\",\"choices\":[{\"delta\":{\"content\":\"answer\"}}]}\n\ndata: {\"id\":\"chatcmpl-upstream\",\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n"))
			}))
			defer up.Close()
			if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Chat: up.URL + "/v1", Models: []string{"model"}}); err != nil {
				t.Fatal(err)
			}
			body := `{"model":"fake/model","input":"hi","stream":` + map[bool]string{true: "true", false: "false"}[stream] + `}`
			rec := httptest.NewRecorder()
			New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body)))
			if rec.Code != 200 {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			id := clientResponseID(t, provider.Responses, rec.Body.String(), stream)
			got := lastRecord(t)
			if got.ResponseID != id || got.RequestID != "req-header" || id != "resp_chatcmpl-upstream" {
				t.Fatalf("record %+v client %s", got, id)
			}
		})
	}
}
