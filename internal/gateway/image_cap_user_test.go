package gateway

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// The two refusals #6849 quotes, byte for byte as the reporter gave them.
const (
	milionseekImageCap = "100milionseek: At most 8 images per request (MAX_IMAGES)."
	coarenaImageCap    = "Coarena: A request may carry at most 8 images."
)

// capImages counts a body's images, a chat image_url and a Messages image
// block alike.
func capImages(b string) int {
	return strings.Count(b, `"type":"image_url"`) + strings.Count(b, `"type":"image"`)
}

// imageCapFake saves provider id, with no account, on a fake speaking
// proto that serves m1 and m2, caps its MaxImages. The fake refuses a
// request of more than most images (0: never) with refusal as a 400
// JSON error, counted in refused, answers the others with a stream in
// proto, and keeps every body.
func imageCapFake(t *testing.T, id string, proto provider.Protocol, caps map[string]int, most int, refusal string) (*[]string, *atomic.Int32) {
	t.Helper()
	fresh(t)
	var mu sync.Mutex
	var bodies []string
	var refused atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
		if most > 0 && capImages(string(b)) > most {
			refused.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"error":{"message":%q,"type":"invalid_request_error"}}`, refusal)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if proto == provider.Anthropic {
			io.WriteString(w, anthropicSSE("SEEN"))
			return
		}
		io.WriteString(w, sse(
			`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"SEEN"}}]}`,
			`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1}}`,
			`data: [DONE]`))
	}))
	t.Cleanup(up.Close)
	p := provider.Provider{ID: id, Name: id, Key: "k", Models: []string{"m1", "m2"}, MaxImages: caps}
	if proto == provider.Anthropic {
		p.Anthropic = up.URL + "/v1"
	} else {
		p.Chat = up.URL + "/v1"
	}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	return &bodies, &refused
}

// A cap set for one model holds for it, and the one for all models for
// the others (#6849: Images per request takes model=n beside n).
func TestMaxImagesPerModel(t *testing.T) {
	sent, _ := imageCapFake(t, "relay", provider.Chat, map[string]int{"*": 8, "m2": 5}, 0, "")
	s := New()
	for _, c := range []struct {
		model string
		want  int
	}{{"m1", 8}, {"m2", 5}} {
		if rec := postCapChat(t, s, imageChat("relay/"+c.model, 11, 1)); rec.Code != 200 {
			t.Fatalf("%s: %d %s", c.model, rec.Code, rec.Body.String())
		}
		got := (*sent)[len(*sent)-1]
		if n := capImages(got); n != c.want {
			t.Errorf("%s: sent %d images, not %d", c.model, n, c.want)
		}
		if !strings.Contains(got, "SHOT90") || !strings.Contains(got, "the latest ask") {
			t.Errorf("%s: the latest user message was changed", c.model)
		}
	}
}

// A model listed by an id of the user's own (SetModelID) keeps the cap
// set on it: the cap moves to the new id, and the request, which names
// the vendor's id, still finds it.
func TestMaxImagesOnAModelListedByItsOwnID(t *testing.T) {
	sent, _ := imageCapFake(t, "relay", provider.Chat, map[string]int{"m1-1017": 3, "*": 9}, 0, "")
	p, err := provider.Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("relay", p.Chat, []catalog.Model{{ID: "m1-1017"}, {ID: "m2"}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := provider.SetModelID("relay/m1-1017", "m1"); err != nil {
		t.Fatal(err)
	}
	if p, _ = provider.Find("relay"); p.MaxImages["m1"] != 3 || p.MaxImages["m1-1017"] != 0 {
		t.Fatalf("the cap didn't move to the new id: %v", p.MaxImages)
	}
	if rec := postCapChat(t, New(), imageChat("relay/m1", 11, 1)); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	got := (*sent)[len(*sent)-1]
	if !strings.Contains(got, `"model":"m1-1017"`) {
		t.Fatalf("not asked by the vendor's id: %.200s", got)
	}
	if n := capImages(got); n != 3 {
		t.Errorf("sent %d images, not m1's 3", n)
	}
}

// An agent's screenshots come back inside tool_result blocks (Claude
// Code's Read of an image, a browser tool's screenshot): they count, and
// the oldest of them go first, while the latest user message's are kept
// and every tool_result stays paired with its tool_use.
func TestMaxImagesCountsToolResults(t *testing.T) {
	sent, _ := imageCapFake(t, "relay", provider.Anthropic, map[string]int{"*": 2}, 0, "")
	img := func(tag string) string {
		return `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + tag + `iVBORw0KGgo` + strings.Repeat("A", 64) + `"}}`
	}
	body := `{"model":"relay/m1","max_tokens":100,"stream":true,"messages":[` +
		`{"role":"user","content":[{"type":"text","text":"look"},` + img("OLDA") + `]},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"tu_1","name":"screenshot","input":{}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_1","content":[{"type":"text","text":"shot"},` + img("MIDA") + `,` + img("MIDB") + `]}]},` +
		`{"role":"assistant","content":"two screens"},` +
		`{"role":"user","content":[{"type":"text","text":"the latest ask"},` + img("NEWA") + `]}]}`
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if len(*sent) != 1 {
		t.Fatalf("asked %d times, want 1", len(*sent))
	}
	got := (*sent)[0]
	if n := capImages(got); n != 2 {
		t.Errorf("sent %d images, not 2: %s", n, got)
	}
	if strings.Contains(got, "OLDA") || strings.Contains(got, "MIDA") || !strings.Contains(got, "MIDB") || !strings.Contains(got, "NEWA") {
		t.Errorf("not the two oldest left out, the newest kept: %s", got)
	}
	if strings.Count(got, imageOverCap) != 2 {
		t.Errorf("the left-out images aren't said: %s", got)
	}
	if !strings.Contains(got, `"tool_use_id":"tu_1"`) || !strings.Contains(got, `"id":"tu_1"`) {
		t.Errorf("the tool call lost its pair: %s", got)
	}
}

// 100milionseek's and Coarena's refusals, in the reporter's words, teach
// the cap of that provider's model with no MaxImages set: the refused
// turn is asked again with the oldest images left out, the next is fitted
// before it is sent, and the provider's other model is not.
func TestImageCapLearnedFromRelayWords(t *testing.T) {
	for _, refusal := range []string{milionseekImageCap, coarenaImageCap} {
		t.Run(strings.SplitN(refusal, ":", 2)[0], func(t *testing.T) {
			sent, refused := imageCapFake(t, "relay", provider.Chat, nil, 8, refusal)
			s := New()
			if rec := postCapChat(t, s, imageChat("relay/m1", 11, 1)); rec.Code != 200 {
				t.Fatalf("the turn failed: %d %s", rec.Code, rec.Body.String())
			}
			if len(*sent) != 2 || refused.Load() != 1 {
				t.Fatalf("asked %d times, refused %d; want 2 and 1", len(*sent), refused.Load())
			}
			if n := capImages((*sent)[1]); n != 8 {
				t.Errorf("asked again with %d images, not 8", n)
			}
			if rec := postCapChat(t, s, imageChat("relay/m1", 11, 1)); rec.Code != 200 || len(*sent) != 3 || refused.Load() != 1 {
				t.Fatalf("the next turn: %d, %d asked, %d refused", rec.Code, len(*sent), refused.Load())
			}
			if n := capImages((*sent)[2]); n != 8 {
				t.Errorf("the next turn sent %d images, not 8", n)
			}
			// the cap is m1's: m2's first turn goes whole
			postCapChat(t, s, imageChat("relay/m2", 11, 1))
			if n := capImages((*sent)[3]); n != 12 {
				t.Errorf("m2's first request carried %d images, not its 12", n)
			}
		})
	}
}

// A request to a provider with no cap, or within its cap, is the very
// slice it came as: nothing parsed, nothing copied.
func TestImageCapNoneIsTheSameBytes(t *testing.T) {
	fresh(t)
	s := New()
	body := []byte(imageChat("m1", 11, 1))
	for _, p := range []provider.Provider{
		{ID: "none"},
		{ID: "within", MaxImages: map[string]int{"*": 12}},
		{ID: "other-model", MaxImages: map[string]int{"m2": 2}},
	} {
		// another provider's learned cap is not this one's
		s.imageCaps.learn("elsewhere\x00m1", 2)
		got := s.withinImageCap(p, provider.Chat, "/v1/chat/completions", body)
		if &got[0] != &body[0] || len(got) != len(body) {
			t.Errorf("%s: the body was rewritten", p.ID)
		}
	}
}
