package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

func TestAnthropicImageSourceFields(t *testing.T) {
	for _, tc := range []struct {
		name string
		part Part
		want map[string]string
	}{
		{"base64", Part{Kind: Image, MediaType: "image/png", Data: pngA}, map[string]string{"type": "base64", "media_type": "image/png", "data": pngA}},
		{"dataURL", Part{Kind: Image, MediaType: "image/png", Data: pngA, URL: "data:image/png;base64," + pngA}, map[string]string{"type": "base64", "media_type": "image/png", "data": pngA}},
		{"url", Part{Kind: Image, URL: "https://example.test/screenshot.png"}, map[string]string{"type": "url", "url": "https://example.test/screenshot.png"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(imageBlock(tc.part))
			if err != nil {
				t.Fatal(err)
			}
			var block struct {
				Type   string            `json:"type"`
				Source map[string]string `json:"source"`
			}
			if err := json.Unmarshal(body, &block); err != nil {
				t.Fatal(err)
			}
			if block.Type != "image" || !reflect.DeepEqual(block.Source, tc.want) {
				t.Fatalf("image source includes missing or extra fields: %s; want %v", body, tc.want)
			}
		})
	}
}

func TestMovedAnthropicImagesKeepSourceVariant(t *testing.T) {
	for _, source := range []struct {
		name string
		url  string
		want map[string]string
	}{
		{"base64", "data:image/png;base64," + pngA, map[string]string{"type": "base64", "media_type": "image/png", "data": pngA}},
		{"url", "https://example.test/screenshot.png", map[string]string{"type": "url", "url": "https://example.test/screenshot.png"}},
	} {
		for _, history := range []bool{false, true} {
			name := source.name + "/user"
			if history {
				name = source.name + "/toolHistory"
			}
			t.Run(name, func(t *testing.T) {
				// The catalog caches data across movedFake's temporary homes.
				catalog.Reset()
				t.Cleanup(catalog.Reset)
				seen := make(chan map[string]any, 1)
				movedFake(t, "factory", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, _ := io.ReadAll(r.Body)
					var request map[string]any
					if err := json.Unmarshal(body, &request); err != nil {
						http.Error(w, err.Error(), 400)
						return
					}
					var images []map[string]string
					var walk func(any)
					walk = func(value any) {
						switch x := value.(type) {
						case []any:
							for _, child := range x {
								walk(child)
							}
						case map[string]any:
							if x["type"] == "image" {
								b, _ := json.Marshal(x["source"])
								var image map[string]string
								json.Unmarshal(b, &image)
								images = append(images, image)
							}
							walk(x["content"])
						}
					}
					walk(request["messages"])
					select {
					case seen <- request:
					default:
					}
					if len(images) != 1 || !reflect.DeepEqual(images[0], source.want) {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(400)
						io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"image.source contains fields from another variant: Extra inputs are not permitted"}}`)
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					for _, event := range []string{
						`{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"fake-claude","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
						`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
						`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
						`{"type":"content_block_stop","index":0}`,
						`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
						`{"type":"message_stop"}`,
					} {
						fmt.Fprintf(w, "data: %s\n\n", event)
					}
				}))
				image := map[string]any{"type": "input_image", "image_url": source.url}
				input := []any{map[string]any{"role": "user", "content": []any{image}}}
				if history {
					input = []any{
						map[string]any{"role": "user", "content": "view the screenshot"},
						map[string]any{"type": "function_call", "name": "view_image", "call_id": "call_1", "arguments": "{}"},
						map[string]any{"type": "function_call_output", "call_id": "call_1", "output": []any{map[string]any{"type": "input_text", "text": "screenshot"}, image}},
						map[string]any{"role": "assistant", "content": "image received"},
						map[string]any{"role": "user", "content": "continue with text"},
					}
				}
				body, _ := json.Marshal(map[string]any{"model": "factory/fake-claude", "input": input, "max_output_tokens": 32})
				rec := httptest.NewRecorder()
				New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(string(body))))
				if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"text":"ok"`) {
					t.Fatalf("translated image rejected by moved provider: %d %s", rec.Code, rec.Body.String())
				}
				request := <-seen
				if history {
					wire, _ := json.Marshal(request["messages"])
					if !strings.Contains(string(wire), `"tool_use_id":"call_1"`) || !strings.Contains(string(wire), `"text":"screenshot"`) || !strings.Contains(string(wire), `"text":"continue with text"`) {
						t.Fatalf("tool history or subsequent text lost: %s", wire)
					}
				}
			})
		}
	}
}
