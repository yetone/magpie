package gateway

// A Muse (Meta) subscription draws with Muse Image on the Responses API:
// POST /responses with model muse-image-1.0 and the prompt as input, the
// picture coming back base64 in the answer's image_generation_call (Meta's
// "Image generation with Muse Image"). Reference images and an image to
// edit go in as input_image parts of the same input. The plugin signs
// POST /responses only, so the single-shot /images endpoints are not asked.
// Its fetch asks for an event stream, so the answer is read as either a
// JSON body or the stream's events.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

func (s *Server) drawMuse(ctx context.Context, p provider.Provider, model string, d drawing) (drawn, int, error) {
	var input any = d.Prompt
	if len(d.Images) > 0 {
		parts := []map[string]any{{"type": "input_text", "text": d.Prompt}}
		for _, pic := range d.Images {
			u := pic.dataURL()
			if pic.URL != "" {
				u = pic.URL
			}
			parts = append(parts, map[string]any{"type": "input_image", "image_url": u})
		}
		input = []map[string]any{{"role": "user", "content": parts}}
	}
	body, _ := json.Marshal(map[string]any{"model": model, "input": input, "store": false})
	url := strings.TrimRight(p.Base(provider.Responses), "/") + "/responses"
	return s.eachDrawing(max(d.N, 1), func() (drawn, int, error) {
		b, code, err := s.send(ctx, p, url, "application/json", body, true)
		if err != nil {
			return drawn{}, code, err
		}
		return readMuseAnswer(p, url, b, code)
	})
}

// readMuseAnswer is the images in a Responses answer: the
// image_generation_call items' result, whether the answer is one JSON body
// or an event stream (its output_item.done and completed events).
func readMuseAnswer(p provider.Provider, url string, b []byte, code int) (drawn, int, error) {
	type item struct {
		Type   string `json:"type"`
		ID     string `json:"id"`
		Result string `json:"result"`
	}
	type response struct {
		Output []item `json:"output"`
		Usage  struct {
			Input  int `json:"input_tokens"`
			Output int `json:"output_tokens"`
		} `json:"usage"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	var items []item
	var res response
	if t := bytes.TrimSpace(b); len(t) > 0 && t[0] == '{' {
		if err := json.Unmarshal(t, &res); err != nil {
			return drawn{}, 502, fmt.Errorf("%s's answer isn't a Responses API's: %v", p.Name, err)
		}
		items = res.Output
	} else {
		sc := bufio.NewScanner(bytes.NewReader(b))
		sc.Buffer(make([]byte, 0, 1<<20), 256<<20)
		for sc.Scan() {
			data, ok := strings.CutPrefix(sc.Text(), "data:")
			if !ok {
				continue
			}
			var ev struct {
				Type     string   `json:"type"`
				Item     item     `json:"item"`
				Response response `json:"response"`
				Error    *struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal([]byte(strings.TrimSpace(data)), &ev) != nil {
				continue
			}
			switch ev.Type {
			case "response.output_item.done":
				items = append(items, ev.Item)
			case "response.completed":
				items = append(items, ev.Response.Output...)
				res.Usage = ev.Response.Usage
			case "response.failed", "error":
				if ev.Response.Error != nil {
					res.Error = ev.Response.Error
				} else if ev.Error != nil {
					res.Error = ev.Error
				}
			}
		}
	}
	out := drawn{Input: res.Usage.Input, Output: res.Usage.Output}
	seen := map[string]bool{}
	for _, it := range items {
		if it.Type != "image_generation_call" || it.Result == "" || it.ID != "" && seen[it.ID] {
			continue
		}
		seen[it.ID] = true
		data, err := base64.StdEncoding.DecodeString(it.Result)
		if err != nil {
			return drawn{}, 502, fmt.Errorf("%s's image isn't base64: %v", p.Name, err)
		}
		out.Images = append(out.Images, picture{Mime: http.DetectContentType(data), Data: data})
	}
	if len(out.Images) == 0 {
		var why string
		if res.Error != nil && res.Error.Message != "" {
			why = ": " + res.Error.Message
		} else {
			why = vendorSaid(vendorMessage(b))
		}
		return out, 502, fmt.Errorf("%s answered with no image%s", provider.HostOf(url), why)
	}
	return out, code, nil
}
