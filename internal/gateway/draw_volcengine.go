package gateway

// Volcengine Ark's images API (火山方舟 图片生成 API, POST
// <base>/images/generations, https://www.volcengine.com/docs/82379/1541523)
// is OpenAI-shaped in its answer but not in what it takes:
//
//   - watermark defaults to true: every image gets an "AI 生成" mark in its
//     corner unless the request says false;
//   - reference images go in the same JSON body, as image (a string, or an
//     array for several), each a URL or a data:image/<fmt>;base64 URL; there
//     is no edits endpoint and no multipart;
//   - there is no n: a request draws one image, or a group of up to 15
//     with sequential_image_generation "auto" (Seedream 4.x, 5.0 lite);
//   - size is a tier ("1K", "2K", "4K"…) or WIDTHxHEIGHT, never a ratio:
//     the doc says to give a ratio in the prompt, and lists the WxH each
//     ratio maps to at 2K;
//   - there is no quality and no mask.
//
// So magpie draws there as the doc says: watermark false unless the caller
// sends its own, the images as JSON, one request per image n asks for (each
// a group when the caller sends sequential_image_generation), and a ratio
// sent as the doc's 2K size for it, or turned away with what to do instead
// (JL on Discord). The caller's other fields of Ark's own go as they came,
// and response_format url is asked of Ark as url, its link answered
// (axelburks on Discord); stream is turned away, its events being Ark's.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// isArk is a provider at Volcengine Ark (ark.<region>.volces.com): the
// preset's plans or a provider added by hand on pay-as-you-go.
var isArk = func(p provider.Provider) bool {
	for _, base := range []string{p.Base(provider.Chat), p.Base(provider.Responses)} {
		if h := provider.HostOf(base); strings.HasPrefix(h, "ark.") && strings.HasSuffix(h, ".volces.com") {
			return true
		}
	}
	return false
}

// arkRatioSizes are the 2K sizes Ark's doc lists for each ratio (Seedream
// 4.0, 4.5 and 5.0 lite's table). Each is in every Seedream model's pixel
// range, 5.0 pro's [1280x720, 2048x2048x1.1025] included.
var arkRatioSizes = map[string]string{
	"1:1": "2048x2048", "4:3": "2304x1728", "3:4": "1728x2304",
	"16:9": "2848x1600", "9:16": "1600x2848", "3:2": "2496x1664",
	"2:3": "1664x2496", "21:9": "3136x1344",
}

// arkSize is size as Ark takes it: a tier or WIDTHxHEIGHT as given, a
// ratio its doc lists as that ratio's 2K size, "" for the model's own.
// Another ratio is an error that says what to send instead.
func arkSize(size string) (string, error) {
	size = strings.TrimSpace(size)
	switch {
	case size == "" || strings.EqualFold(size, "auto"):
		return "", nil
	case strings.Contains(size, ":"):
		if s, ok := arkRatioSizes[strings.ReplaceAll(size, " ", "")]; ok {
			return s, nil
		}
		return "", fmt.Errorf("Volcengine's images API takes no aspect ratio as size, and %q isn't one magpie can turn into a size (it can: 1:1, 4:3, 3:4, 16:9, 9:16, 3:2, 2:3, 21:9): describe the ratio in the prompt and leave size out, or give WIDTHxHEIGHT (e.g. 2048x2048)", size)
	}
	return size, nil
}

// arkImageTextFields are Ark's images fields that are text, kept as text
// from a multipart form.
var arkImageTextFields = []string{"model", "prompt", "size", "sequential_image_generation", "output_format", "background", "response_format"}

// arkBody is d as Ark's images API takes it, for one request.
func arkBody(model string, d drawing) ([]byte, error) {
	if d.Mask != nil {
		return nil, errors.New("Volcengine's images API takes no mask: say in the prompt which part of the image to change")
	}
	size, err := arkSize(d.Size)
	if err != nil {
		return nil, err
	}
	// the caller's own fields of Ark's first (watermark,
	// sequential_image_generation and its options, optimize_prompt_options,
	// tools, layer_decomposition…; axelburks on Discord), then what magpie
	// reads of OpenAI's: a watermark is off unless the caller asks for
	// one, and the image comes as Ark's URL when the caller asks for url
	req := map[string]any{"watermark": false}
	for k, v := range d.Extra {
		if d.Form {
			v = formValue(k, v, arkImageTextFields)
		}
		req[k] = v
	}
	if on, _ := req["stream"].(bool); on {
		return nil, errors.New("Volcengine's images API streams its images in events of its own, which /v1/images/generations doesn't give: leave stream out, or ask Ark's own API through magpie at /v1/magpie/native/<provider>/images/generations")
	}
	delete(req, "stream")
	req["model"] = model
	if d.Prompt != "" {
		req["prompt"] = d.Prompt
	}
	req["response_format"] = "b64_json"
	if strings.EqualFold(d.Want, "url") {
		req["response_format"] = "url"
	}
	if size != "" {
		req["size"] = size
	}
	switch f := strings.ToLower(d.Format); f {
	case "png", "jpeg":
		req["output_format"] = f
	case "jpg":
		req["output_format"] = "jpeg"
	}
	if strings.EqualFold(d.Background, "transparent") {
		req["background"] = "transparent"
	}
	switch len(d.Images) {
	case 0:
	case 1:
		req["image"] = d.Images[0].dataURL()
	default:
		var urls []string
		for _, pic := range d.Images {
			urls = append(urls, pic.dataURL())
		}
		req["image"] = urls
	}
	return json.Marshal(req)
}

// drawArk asks Ark's images API once for each of d's images.
func (s *Server) drawArk(ctx context.Context, p provider.Provider, model string, d drawing) (drawn, int, error) {
	body, err := arkBody(model, d)
	if err != nil {
		return drawn{}, http.StatusBadRequest, err
	}
	url := strings.TrimRight(p.Base(provider.Chat), "/")
	if url == "" {
		url = strings.TrimRight(p.Base(provider.Responses), "/")
	}
	url += "/images/generations"
	return s.eachDrawing(max(d.N, 1), func() (drawn, int, error) {
		b, code, err := s.send(ctx, p, url, "application/json", body, true)
		if err != nil {
			return drawn{}, code, err
		}
		return readImagesAnswer(p, url, b, code)
	})
}
