package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

func TestResponsesFileIDRouting(t *testing.T) {
	for _, tc := range []struct {
		name, endpoint, body string
		imageInput           bool
		wantStatus           int
		wantUpstream         string
	}{
		{"chat-current", "chat", `{"model":"probe/m","input":[{"role":"user","content":[{"type":"input_text","text":"read"},{"type":"input_image","file_id":"file-abc"}]}]}`, true, 400, ""},
		{"anthropic-current", "anthropic", `{"model":"probe/m","input":[{"role":"user","content":[{"type":"input_image","file_id":"file-abc"}]}]}`, true, 400, ""},
		{"native-current", "responses", `{"model":"probe/m","input":[{"role":"user","content":[{"type":"input_text","text":"read"},{"type":"input_image","file_id":"file-abc"}]}]}`, true, 200, `"file_id":"file-abc"`},
		{"chat-url", "chat", `{"model":"probe/m","input":[{"role":"user","content":[{"type":"input_text","text":"read"},{"type":"input_image","image_url":"data:image/png;base64,aGVsbG8="}]}]}`, true, 200, "aGVsbG8="},
		{"chat-history-text-only", "chat", `{"model":"probe/m","input":[{"role":"user","content":[{"type":"input_image","file_id":"file-abc"}]},{"role":"assistant","content":[{"type":"output_text","text":"seen"}]},{"role":"user","content":[{"type":"input_text","text":"now"}]}]}`, false, 200, "Image omitted"},
		{"chat-current-text-only", "chat", `{"model":"probe/m","input":[{"role":"user","content":[{"type":"input_image","file_id":"file-abc"}]}]}`, false, 400, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fresh(t)
			noVision(t)
			var sent []string
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				sent = append(sent, string(b))
				w.Header().Set("Content-Type", "application/json")
				if tc.endpoint == "chat" {
					if strings.Contains(string(b), `"stream":true`) {
						w.Header().Set("Content-Type", "text/event-stream")
						io.WriteString(w, sse(`data: {"id":"x","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"}}]}`, `data: {"id":"x","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, `data: [DONE]`))
					} else {
						io.WriteString(w, `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
					}
				} else {
					io.WriteString(w, `{"id":"x","output":[]}`)
				}
			}))
			defer up.Close()
			p := provider.Provider{ID: "probe", Key: "key", Models: []string{"m"}}
			switch tc.endpoint {
			case "chat":
				p.Chat = up.URL + "/v1"
			case "anthropic":
				p.Anthropic = up.URL + "/v1"
			case "responses":
				p.Responses = up.URL + "/v1"
			}
			if err := provider.Save(p); err != nil {
				t.Fatal(err)
			}
			if err := catalog.SaveLive("probe", up.URL+"/v1", []catalog.Model{{ID: "m", Images: tc.imageInput, ImageInput: imageInputBool(tc.imageInput)}}); err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(tc.body)))
			if rec.Code != tc.wantStatus {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantStatus == 400 && !strings.Contains(rec.Body.String(), "image") {
				t.Fatalf("unclear rejection: %s", rec.Body.String())
			}
			if tc.wantUpstream == "" {
				if len(sent) != 0 {
					t.Fatalf("unexpected upstream request: %v", sent)
				}
			} else if len(sent) != 1 || !strings.Contains(sent[0], tc.wantUpstream) {
				t.Fatalf("upstream requests: %v, want %s", sent, tc.wantUpstream)
			}
		})
	}
}
