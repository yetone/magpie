package gateway

import (
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A Muse (Meta) subscription draws with Muse Image on the Responses API
// (ljlwlcz on X: "both Muse plugins don't support image generation"): the
// model muse-image-1.0, the prompt as input, the picture in the answer's
// image_generation_call, read from a JSON body and from an event stream.
func TestMusePluginDraws(t *testing.T) {
	png := base64.StdEncoding.EncodeToString(pngBytes)
	for name, answer := range map[string]struct{ ct, body string }{
		"json": {"application/json", `{"id":"resp_1","status":"completed","output":[{"type":"reasoning","id":"rs_1","summary":[]},{"type":"message","id":"m","role":"assistant","content":[]},{"type":"image_generation_call","id":"ig_1","status":"completed","result":"` + png + `"}],"usage":{"input_tokens":7,"output_tokens":3}}`},
		"stream": {"text/event-stream", "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"image_generation_call\",\"id\":\"ig_1\",\"result\":\"" + png + "\"}}\n\n" +
			"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"type\":\"image_generation_call\",\"id\":\"ig_1\",\"result\":\"" + png + "\"}],\"usage\":{\"input_tokens\":7,\"output_tokens\":3}}}\n\n"},
	} {
		t.Run(name, func(t *testing.T) {
			var mu sync.Mutex
			var paths, bodies []string
			pid := besideFake(t, "muse", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				mu.Lock()
				paths, bodies = append(paths, r.URL.Path), append(bodies, string(b))
				mu.Unlock()
				w.Header().Set("Content-Type", answer.ct)
				io.WriteString(w, answer.body)
			}))
			p, err := provider.Find(pid)
			if err != nil {
				t.Fatal(err)
			}
			if ds := Drawers(*p); len(ds) != 1 || ds[0].ID != "muse-image-1.0" || ds[0].Provider != pid {
				t.Fatalf("drawers %v", ds)
			}
			code, a, raw := postImages(t, New(), "/v1/images/generations", "application/json", `{"model":"`+pid+`/muse-image-1.0","prompt":"a magpie"}`)
			if code != 200 || len(a.Data) != 1 {
				t.Fatalf("%d %s", code, raw)
			}
			// a newer Muse Image, named before magpie lists it
			if code, a, raw = postImages(t, New(), "/v1/images/generations", "application/json", `{"model":"muse-image-1.1","prompt":"a magpie"}`); code != 200 || len(a.Data) != 1 {
				t.Fatalf("bare name: %d %s", code, raw)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(paths) != 2 || paths[0] != "/v1/responses" || !strings.Contains(bodies[0], `"model":"muse-image-1.0"`) || !strings.Contains(bodies[0], `"input":"a magpie"`) || !strings.Contains(bodies[1], `"model":"muse-image-1.1"`) {
				t.Fatalf("sent %v %v", paths, bodies)
			}
		})
	}
}
