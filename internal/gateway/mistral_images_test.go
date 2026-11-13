package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

const mistralTooMany = `{"object":"error","message":"Total number of images exceeds the maximum allowed of 20.","type":"invalid_request_error","param":null,"code":null}`

// mistralImageRefuse answers as Mistral does a request with more than 20
// images (regain4125 on X).
func mistralImageRefuse(body []byte) (int, string) {
	if bytes.Count(body, []byte(`data:image/png`)) > 20 {
		return 400, mistralTooMany
	}
	return 0, ""
}

// shots is n turns of a Codex conversation that each read one screenshot,
// as a Chat client sends them.
func chatShots(n int) string {
	var b strings.Builder
	b.WriteString(`{"model":"m1","stream":false,"messages":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"role":"user","content":[{"type":"text","text":"shot %d"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA%d"}}]}`, i, i)
	}
	b.WriteString(`]}`)
	return b.String()
}

func responsesShots(n int) string {
	var b strings.Builder
	b.WriteString(`{"model":"m1","stream":false,"input":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"type":"message","role":"user","content":[{"type":"input_text","text":"shot %d"},{"type":"input_image","image_url":"data:image/png;base64,AAAA%d"}]}`, i, i)
	}
	b.WriteString(`]}`)
	return b.String()
}

func sentImages(f *fake) int { return bytes.Count(f.got, []byte(`data:image/png`)) }

const mistralImgStreamOK = "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"m1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"No.\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"

// replyFor is a fake's good reply on a route: whole for Chat, streamed for
// the Responses route, which the gateway asks its Chat upstream to stream.
func replyFor(path string) (string, string) {
	if path == "/v1/responses" {
		return mistralImgStreamOK, "text/event-stream"
	}
	return chatWholeOK, "application/json"
}

// Past 20 images a request to Mistral loses its oldest ones to a short
// text, ten at a time (mistralImageStep), on the Chat route and on the
// Responses route Codex uses; the newest and every turn's text stay. The same conversation to another
// vendor goes with every image, and one of 20 or fewer is untouched.
func TestMistralImageLimit(t *testing.T) {
	for _, route := range []struct {
		name, path string
		proto      provider.Protocol
		body       func(int) string
	}{
		{"chat", "/v1/chat/completions", provider.Chat, chatShots},
		{"responses", "/v1/responses", provider.Chat, responsesShots},
	} {
		t.Run(route.name, func(t *testing.T) {
			reply, ctype := replyFor(route.path)
			f := &fake{t: t, reply: reply, ctype: ctype, refuse: mistralImageRefuse}
			up := setup(t, route.proto, f)
			if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"},
				Preset: "mistral", Chat: up.URL + "/v1"}); err != nil {
				t.Fatal(err)
			}
			srv := New()
			code, body := postTo(t, srv, route.path, route.body(26))
			if code != 200 {
				t.Fatalf("%d: %s", code, body)
			}
			if n := sentImages(f); n != 16 {
				t.Errorf("%d images sent, want 16", n)
			}
			sent := string(f.got)
			for _, want := range []string{"shot 0", "shot 5", "shot 25", "AAAA25", "AAAA10"} {
				if !strings.Contains(sent, want) {
					t.Errorf("sent lacks %q: %s", want, sent)
				}
			}
			if strings.Contains(sent, `AAAA9"`) || !strings.Contains(sent, mistralImagesLeft) {
				t.Errorf("oldest image kept or no placeholder: %s", sent)
			}
			// within the limit: unchanged
			f.calls = 0
			if code, _ := postTo(t, srv, route.path, route.body(20)); code != 200 || sentImages(f) != 20 || strings.Contains(string(f.got), mistralImagesLeft) {
				t.Errorf("20 images: %s", f.got)
			}
		})
	}

	t.Run("another vendor", func(t *testing.T) {
		f := &fake{t: t, reply: chatWholeOK, ctype: "application/json"}
		up := setup(t, provider.Chat, f)
		if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"},
			Chat: up.URL + "/v1"}); err != nil {
			t.Fatal(err)
		}
		code, body := postTo(t, New(), "/v1/chat/completions", chatShots(26))
		if code != 200 || sentImages(f) != 26 || strings.Contains(string(f.got), mistralImagesLeft) {
			t.Errorf("%d: %d images, %s", code, sentImages(f), body)
		}
		f.reply, f.ctype = mistralImgStreamOK, "text/event-stream"
		code, body = postTo(t, New(), "/v1/responses", responsesShots(26))
		if code != 200 || sentImages(f) != 26 {
			t.Errorf("responses %d: %d images, %s", code, sentImages(f), body)
		}
	})
}

// regain4125 on X: a Codex conversation reading a screenshot a turn, past
// 20 of them, missed Mistral's prompt cache on nearly all of its 400K
// tokens at every turn, as the cut moved by one image each time and
// changed the prompt from the oldest kept image on. Each turn's request
// is the one before with the new turn added, until the cut moves ten
// images later, and never more than 20 images go.
func TestMistralImageLimitKeepsPrefix(t *testing.T) {
	messages := func(n int) []string {
		var q struct {
			Messages []json.RawMessage `json:"messages"`
		}
		if err := json.Unmarshal(mistralImageLimit([]byte(chatShots(n))), &q); err != nil {
			t.Fatal(err)
		}
		out := make([]string, len(q.Messages))
		for i, m := range q.Messages {
			out[i] = string(m)
		}
		return out
	}
	moved := 0
	prev := messages(20)
	for n := 21; n <= 60; n++ {
		cur := messages(n)
		if got := strings.Count(strings.Join(cur, ","), `data:image/png`); got > mistralMaxImages {
			t.Fatalf("%d shots: %d images sent", n, got)
		}
		if !slices.Equal(cur[:len(prev)], prev) {
			moved++
		}
		prev = cur
	}
	// the cut moves at 21, 31, 41 and 51 shots
	if moved != 4 {
		t.Fatalf("prefix changed on %d of 40 turns, want 4", moved)
	}
}
