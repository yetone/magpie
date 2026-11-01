package gateway

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/provider"
)

// webpShot is a 2x1 lossless webp, as Claude Desktop's browser tool
// answers a screenshot (#4732).
var webpShot = "UklGRh4AAABXRUJQVlA4TBEAAAAvAQAAAA+w//Mf8x8VMqL/AQA="

// zhipuFileRefusal is GLM's 400 [1214], its word for a content block it
// took for a file of its own and found no file_id, file_url or file_data
// in — what its Anthropic-style endpoint says of a webp picture (#4732).
const zhipuFileRefusal = `{"type":"error","error":{"type":"invalid_request_error","message":"[1214][messages[0].content[0].file必须传入file_id、file_url、file_data至少之一]"}}`

// zhipuFormatRefusal is GLM's 400 [1210] for an image it doesn't parse,
// the other of the reporter's two, the pair alternating (#4732).
const zhipuFormatRefusal = `{"error":{"code":"1210","message":"[1210][图片输入格式/解析错误]"}}`

// #4732: Claude Desktop's browser screenshot is a webp, GLM can't read
// one, and its 400 left the session stuck: the picture rides in the
// conversation's history, so every later turn was refused the same way.
// Asked again with the picture written as a JPEG, which it takes, and
// from then on written before it leaves, the session goes on.
func TestWebPRefusalHealed(t *testing.T) {
	var mts []string
	f := &fake{t: t, reply: anthropicHi}
	f.refuse = func(b []byte) (int, string) {
		mt := gjson.GetBytes(b, "messages.0.content.0.source.media_type").String()
		mts = append(mts, mt)
		if mt == "image/webp" {
			return 400, zhipuFileRefusal
		}
		return 0, ""
	}
	setup(t, provider.Anthropic, f)
	srv := New()
	ask := `{"model":"m1","max_tokens":32,"messages":[{"role":"user","content":[` +
		`{"type":"image","source":{"type":"base64","media_type":"image/webp","data":"` + webpShot + `"}},` +
		`{"type":"text","text":"这是什么"}]}]}`
	for turn, want := range [][]string{{"image/webp", "image/jpeg"}, {"image/jpeg"}} {
		mts = nil
		code, body := postTo(t, srv, "/v1/messages", ask)
		if code != 200 || len(mts) != len(want) {
			t.Fatalf("turn %d: %d %s, media types sent %q, want %q", turn+1, code, body, mts, want)
		}
		for i, mt := range want {
			if mts[i] != mt {
				t.Fatalf("turn %d: media type sent %d is %q, want %q (all %q)", turn+1, i, mts[i], mt, mts)
			}
		}
	}
}

// The same refusal with no webp of the request's to write again: the next
// provider is asked, as for a shape it can't read, and this one doesn't
// rest — nothing is wrong with it.
func TestZhipuImageRefusalFailsOver(t *testing.T) {
	plan := &fake{t: t, ctype: "application/json", code: 400, reply: zhipuFileRefusal}
	spare := &fake{t: t, ctype: "application/json", reply: `{"id":"from-spare","choices":[]}`}
	twoProviders(t, plan, spare)
	code, body := post(t, "/v1/chat/completions", chatReq)
	if code != 200 || !strings.Contains(body, "from-spare") || strings.Contains(body, "file_data") {
		t.Fatalf("%d %s", code, body)
	}
	if plan.calls != 1 {
		t.Fatalf("plan asked %d times, want 1", plan.calls)
	}
}

func TestZhipuImageRefusalWords(t *testing.T) {
	// the message \u-escaped, as a relay may pass it on (#1669)
	escaped := `{"error":{"code":"1214","message":"[1214][messages[0].content[0].file\u5fc5\u987b\u4f20\u5165file_id\u3001file_url\u3001file_data\u81f3\u5c11\u4e4b\u4e00]"}}`
	for b, want := range map[string]bool{
		zhipuFileRefusal:   true,
		zhipuFormatRefusal: true,
		escaped:            true,
		`{"error":{"message":"messages: field required"}}`:                 false,
		`{"error":{"message":"messages[1210].content: must have a role"}}`: false,
		`{"error":{"message":"Invalid API key"}}`:                          false,
		// Z.ai's other 1210, its word for a model that always thinks
		// (TestAnthropicPassthroughModelThatAlwaysThinks): the bare code,
		// without the double brackets or the picture's words, is none of
		// this family
		`{"type":"error","error":{"type":"1210","message":"GLM-5.3 always engages in thinking; use low, high, or max"}}`: false,
	} {
		if got := zhipuImageRefusal.Match(wordsOf([]byte(b))); got != want {
			t.Errorf("zhipuImageRefusal(%s) = %v, want %v", b, got, want)
		}
		if got := shapeRefused(400, []byte(b)); got != want {
			t.Errorf("shapeRefused(400, %s) = %v, want %v", b, got, want)
		}
		if got := retryable(400, []byte(b)); got != want {
			t.Errorf("retryable(400, %s) = %v, want %v", b, got, want)
		}
	}
}

func TestWebPAsJPEG(t *testing.T) {
	ask := []byte(`{"model":"m1","max_tokens":32,"messages":[{"role":"user","content":[` +
		`{"type":"image","source":{"type":"base64","media_type":"image/webp","data":"` + webpShot + `"}},` +
		`{"type":"text","text":"这是什么"}]}]}`)
	got := webpAsJPEG(provider.Anthropic, ask)
	mt := gjson.GetBytes(got, "messages.0.content.0.source.media_type").String()
	data, err := base64.StdEncoding.DecodeString(gjson.GetBytes(got, "messages.0.content.0.source.data").String())
	if err != nil {
		t.Fatalf("data: %v", err)
	}
	if mt != "image/jpeg" && mt != "image/png" {
		t.Fatalf("media type %q, want a written-again picture", mt)
	}
	if len(data) > 4 && !bytes.HasPrefix(data, []byte{0xff, 0xd8}) && !bytes.HasPrefix(data, []byte("\x89PNG")) {
		t.Fatalf("data is neither a JPEG nor a PNG: %x", data[:8])
	}
	if bytes.Contains(got, []byte(webpShot)) {
		t.Fatalf("the webp is still there: %s", got)
	}
	if same := webpAsJPEG(provider.Anthropic, []byte(chatReq)); !bytes.Equal(same, []byte(chatReq)) {
		t.Fatalf("a request with no webp was changed: %s", same)
	}
	if mayHoldWebP([]byte(chatReq)) {
		t.Fatal("mayHoldWebP saw a webp in a plain text request")
	}
}
