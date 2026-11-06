package gateway

// DashScope (Alibaba Cloud Model Studio, Bailian, the Qwen AI platform and
// Qwen Cloud, pay as you go and Token Plan alike) draws its Qwen-Image and
// Wan 2.7 image models on its own API, not the OpenAI images API: a POST to
// <host>/api/v1/services/aigc/multimodal-generation/generation, Bearer the
// same key, of
//
//	{"model": …, "input": {"messages": [{"role": "user", "content":
//	  [{"image": <URL or data URL>}…, {"text": <prompt>}]}]},
//	 "parameters": {"n": …, "size": "W*H", "watermark": false}}
//
// answered with the images in output.choices[*].message.content[*].image,
// each a URL kept for 24 hours (docs.qwencloud.com, "Qwen-Image text-to-
// image" and the Token Plan's "Integrate multimodal generation"), the only
// API documented for them. magpie sent them to the OpenAI images API at
// /compatible-mode/v1, and on its 404 to chat completions, whose answer has
// no image magpie reads: "qwen-image-3.0-pro drew nothing" (judi0713 on
// Discord; which of the two answered is not seen from here).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// dashScopeDraws is whether model draws on DashScope's multimodal-
// generation API: the Qwen-Image models (qwen-image-3.0-pro, qwen-image-
// edit-plus…) and Wan 2.7's image ones. Older Wan text-to-image models
// draw on another, asynchronous API and are left as they were.
func dashScopeDraws(p provider.Provider, model string) bool {
	m := strings.ToLower(model)
	return dashScope(p) && (strings.HasPrefix(m, "qwen-image") || strings.HasPrefix(m, "wan2.7-image"))
}

// dashScopeRoot is the host's root the native API is under: the base given
// with its compatible-mode, Anthropic or native path cut off.
func dashScopeRoot(p provider.Provider) string {
	base := strings.TrimRight(p.Base(provider.Chat), "/")
	if base == "" {
		base = strings.TrimRight(p.Base(provider.Anthropic), "/")
	}
	for _, cut := range []string{"/compatible-mode/", "/apps/", "/api/v1"} {
		if i := strings.Index(base, cut); i >= 0 {
			return base[:i]
		}
	}
	return base
}

// dashScopeSize is size as DashScope takes it, W*H, or "" for the model's
// own pick. A ratio is an error saying what to send instead.
func dashScopeSize(size string) (string, error) {
	size = strings.TrimSpace(size)
	switch {
	case size == "" || strings.EqualFold(size, "auto"):
		return "", nil
	case strings.Contains(size, ":"):
		return "", fmt.Errorf("DashScope's image models take no aspect ratio as size: give WIDTHxHEIGHT (e.g. 1328x1328, at most 2048x2048 in pixels), or leave size out for the model to pick one from the prompt")
	}
	return strings.ReplaceAll(strings.ToLower(size), "x", "*"), nil
}

// dashScopeBody is d as the multimodal-generation API takes it.
func dashScopeBody(model string, d drawing) ([]byte, error) {
	if d.Mask != nil {
		return nil, errors.New("DashScope's image models take no mask: say in the prompt which part of the image to change")
	}
	if d.Markup != nil {
		return nil, errors.New("DashScope's image models take no markup layer: say in the prompt what to change")
	}
	size, err := dashScopeSize(d.Size)
	if err != nil {
		return nil, err
	}
	var content []map[string]string
	for _, pic := range d.Images {
		content = append(content, map[string]string{"image": pic.dataURL()})
	}
	content = append(content, map[string]string{"text": d.Prompt})
	params := map[string]any{"watermark": false}
	if d.N > 1 {
		params["n"] = d.N
	}
	if size != "" {
		params["size"] = size
	}
	return json.Marshal(map[string]any{
		"model":      model,
		"input":      map[string]any{"messages": []map[string]any{{"role": "user", "content": content}}},
		"parameters": params,
	})
}

func (s *Server) drawDashScope(ctx context.Context, p provider.Provider, model string, d drawing) (drawn, int, error) {
	body, err := dashScopeBody(model, d)
	if err != nil {
		return drawn{}, http.StatusBadRequest, err
	}
	url := dashScopeRoot(p) + "/api/v1/services/aigc/multimodal-generation/generation"
	b, code, err := s.send(ctx, p, url, "application/json", body, true)
	if err != nil {
		if said := dashScopeSaid(b); said != "" {
			err = fmt.Errorf("%s: %d %s: %s", provider.HostOf(url), code, http.StatusText(code), said)
		}
		return drawn{}, code, err
	}
	return readDashScopeAnswer(url, b, code)
}

// dashScopeSaid is a DashScope error's words, {"code", "message"}, or "".
func dashScopeSaid(b []byte) string {
	var e struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal(b, &e) != nil || e.Code == "" && e.Message == "" {
		return ""
	}
	if e.Code == "" {
		return e.Message
	}
	return e.Message + " (" + e.Code + ")"
}

// readDashScopeAnswer takes every image of every choice; an answer with
// none says what DashScope said, or how it ended.
func readDashScopeAnswer(url string, b []byte, code int) (drawn, int, error) {
	var res struct {
		Output struct {
			Choices []struct {
				FinishReason string `json:"finish_reason"`
				Message      struct {
					Content []struct {
						Image string `json:"image"`
						Text  string `json:"text"`
					} `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		} `json:"output"`
	}
	if err := json.Unmarshal(b, &res); err != nil {
		return drawn{}, 502, fmt.Errorf("%s's answer isn't DashScope's: %v", provider.HostOf(url), err)
	}
	var out drawn
	var texts, ends []string
	for _, c := range res.Output.Choices {
		for _, part := range c.Message.Content {
			switch {
			case part.Image != "":
				out.Images = append(out.Images, picture{URL: part.Image})
			case strings.TrimSpace(part.Text) != "":
				texts = append(texts, part.Text)
			}
		}
		if c.FinishReason != "" && c.FinishReason != "stop" {
			ends = append(ends, c.FinishReason)
		}
	}
	out.Text = strings.Join(texts, "\n")
	if len(out.Images) == 0 {
		said := dashScopeSaid(b)
		if said == "" {
			said = out.Text
		}
		if said == "" && len(ends) > 0 {
			said = "it ended with " + strings.Join(ends, ", ")
		}
		return out, saidStatus(said), fmt.Errorf("%s answered with no image%s", provider.HostOf(url), vendorSaid(said))
	}
	return out, code, nil
}
