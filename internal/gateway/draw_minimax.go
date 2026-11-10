package gateway

// MiniMax draws its image-01 and image-01-live models on its own API, not
// the OpenAI images API (platform.minimax.io/docs/api-reference/
// image-generation-t2i and -i2i; platform.minimax.cn the same): a POST to
// <host>/v1/image_generation, Bearer the same key, of
//
//	{"model": "image-01", "prompt": …, "aspect_ratio": "16:9" | "width":
//	 W, "height": H, "n": 1…9, "response_format": "base64",
//	 "subject_reference": [{"type": "character", "image_file": <URL or
//	 data URL>}]}
//
// answered with data.image_base64 (or data.image_urls), metadata's
// success_count and failed_count (those blocked as unsafe), and base_resp
// {status_code, status_msg}, 0 when it went well. magpie sent image-01 to
// chat completions, whose answer was "invalid params, unknown model
// 'image-01' (2013)" (rocbomb123 on X, minimax-cn).

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// minimaxDrawModels are the image models every MiniMax API key draws with.
// image-01 draws from a prompt, or a portrait as its subject reference;
// image-01-live is the same in a style (style.style_type).
var minimaxDrawModels = []catalog.Model{
	{ID: "image-01", Name: "MiniMax Image-01", Draws: true, Images: true},
	{ID: "image-01-live", Name: "MiniMax Image-01 Live", Draws: true, Images: true},
}

// minimaxDrawRatios are the aspect ratios image_generation takes, each with
// the size it draws; 21:9 is image-01's alone.
var minimaxDrawRatios = map[string]string{
	"1:1": "1024x1024", "16:9": "1280x720", "4:3": "1152x864", "3:2": "1248x832",
	"2:3": "832x1248", "3:4": "864x1152", "9:16": "720x1280", "21:9": "1344x576",
}

// drawsMiniMax is whether model is one of a MiniMax API key's image models,
// drawn on its image_generation API.
func drawsMiniMax(p provider.Provider, model string) bool {
	return filmsMiniMax(p) && slices.ContainsFunc(minimaxDrawModels, func(m catalog.Model) bool { return strings.EqualFold(m.ID, model) })
}

// minimaxDrawers are the image models as p's.
func minimaxDrawers(p provider.Provider) []catalog.Model {
	out := slices.Clone(minimaxDrawModels)
	for i := range out {
		out[i].Provider = p.ID
	}
	return out
}

// minimaxShape is the size field as image_generation takes it: a ratio it
// lists (by the ratio, or the size it draws for one), else for image-01 a
// WIDTHxHEIGHT it can draw (512 to 2048, each a multiple of 8), else the
// nearest of its ratios. nil for none asked: the vendor's 1:1.
func minimaxShape(model, size string) map[string]any {
	size = strings.ToLower(strings.TrimSpace(size))
	if size == "" || size == "auto" {
		return nil
	}
	ratios := make([]string, 0, len(minimaxDrawRatios))
	for r, at := range minimaxDrawRatios {
		if r == "21:9" && model != "image-01" {
			continue
		}
		if size == r || size == at {
			return map[string]any{"aspect_ratio": r}
		}
		ratios = append(ratios, r)
	}
	slices.Sort(ratios)
	if w, h, ok := strings.Cut(size, "x"); ok && model == "image-01" {
		x, err1 := strconv.Atoi(w)
		y, err2 := strconv.Atoi(h)
		fits := func(n int) bool { return n >= 512 && n <= 2048 && n%8 == 0 }
		if err1 == nil && err2 == nil && fits(x) && fits(y) {
			return map[string]any{"width": x, "height": y}
		}
	}
	if r := aspectAmong(size, ratios); r != "" && slices.Contains(ratios, r) {
		return map[string]any{"aspect_ratio": r}
	}
	return nil
}

// minimaxDrawBody is d as image_generation takes it. What it can't take (a
// mask, a markup layer) is said before it is asked.
func minimaxDrawBody(model string, d drawing) ([]byte, error) {
	if d.Mask != nil {
		return nil, errors.New("MiniMax's image models take no mask: say in the prompt what to change")
	}
	if d.Markup != nil {
		return nil, errors.New("MiniMax's image models take no markup layer: say in the prompt what to change")
	}
	req := map[string]any{"model": model, "prompt": d.Prompt, "n": max(d.N, 1), "response_format": "base64"}
	for k, v := range minimaxShape(model, d.Size) {
		req[k] = v
	}
	if len(d.Images) > 0 {
		// a portrait the image is drawn of: MiniMax's only kind of reference
		var refs []map[string]string
		for _, pic := range d.Images {
			refs = append(refs, map[string]string{"type": "character", "image_file": pic.dataURL()})
		}
		req["subject_reference"] = refs
	}
	// what the caller set of MiniMax's own fields
	for _, k := range []string{"seed", "prompt_optimizer", "aigc_watermark", "style"} {
		if v, ok := d.Extra[k]; ok {
			req[k] = v
		}
	}
	return json.Marshal(req)
}

func (s *Server) drawMiniMax(ctx context.Context, p provider.Provider, model string, d drawing) (drawn, int, error) {
	body, err := minimaxDrawBody(model, d)
	if err != nil {
		return drawn{}, http.StatusBadRequest, err
	}
	url := minimaxHost(p) + "/v1/image_generation"
	b, code, err := s.send(ctx, p, url, "application/json", body, true)
	if err != nil {
		return drawn{}, code, minimaxDrawError(code, b, err)
	}
	return readMiniMaxDrawing(url, b, code)
}

// minimaxDrawWhy is what a MiniMax error code means for a drawing, and the
// status it is answered with; "" for one that says enough itself.
func minimaxDrawWhy(code string, status int) (string, int) {
	switch {
	case code == "1004" || code == "2049" || code == "" && status == http.StatusUnauthorized:
		return "MiniMax turned the API key away: it is wrong, or was deleted. Put a working key in the MiniMax provider", http.StatusUnauthorized
	case code == "1008" || code == "" && status == http.StatusPaymentRequired:
		return "the MiniMax account is out of balance: top it up on MiniMax's platform", http.StatusPaymentRequired
	case code == "1026":
		return "MiniMax refused the prompt as sensitive content: change what the image asks for", http.StatusBadRequest
	case code == "1002" || code == "" && status == http.StatusTooManyRequests:
		return "MiniMax was asked for images too often: try again shortly", http.StatusTooManyRequests
	case code == "2013":
		return "", http.StatusBadRequest
	}
	return "", 0
}

// minimaxDrawError is err (MiniMax turning a drawing away with status and
// body b) with what it means first.
func minimaxDrawError(status int, b []byte, err error) error {
	_, code := minimaxSaid(b)
	if why, _ := minimaxDrawWhy(code, status); why != "" {
		return fmt.Errorf("%s — %w", why, err)
	}
	return err
}

// readMiniMaxDrawing takes every image of an image_generation answer. Its
// base_resp says a failure in a 200; an answer with no image says why: the
// vendor's words, or that its safety check held them all back.
func readMiniMaxDrawing(url string, b []byte, code int) (drawn, int, error) {
	var res struct {
		Data struct {
			Base64 []string `json:"image_base64"`
			URLs   []string `json:"image_urls"`
		} `json:"data"`
		Metadata struct {
			// numbers, or numbers in strings, as the docs' example has them
			Failed json.RawMessage `json:"failed_count"`
		} `json:"metadata"`
		BaseResp struct {
			StatusCode int    `json:"status_code"`
			StatusMsg  string `json:"status_msg"`
		} `json:"base_resp"`
	}
	if err := json.Unmarshal(b, &res); err != nil {
		return drawn{}, 502, fmt.Errorf("%s's answer isn't MiniMax's: %v", provider.HostOf(url), err)
	}
	if c := res.BaseResp.StatusCode; c != 0 {
		mcode := strconv.Itoa(c)
		said := res.BaseResp.StatusMsg + codeUnlessSaid(res.BaseResp.StatusMsg, mcode)
		err := fmt.Errorf("%s: MiniMax: %s", provider.HostOf(url), said)
		why, status := minimaxDrawWhy(mcode, 0)
		if why != "" {
			err = fmt.Errorf("%s — %w", why, err)
		}
		if status == 0 {
			status = saidStatus(said)
		}
		return drawn{}, status, err
	}
	var out drawn
	for _, s := range res.Data.Base64 {
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
		if err != nil || len(raw) == 0 {
			continue
		}
		out.Images = append(out.Images, picture{Mime: http.DetectContentType(raw), Data: raw})
	}
	for _, u := range res.Data.URLs {
		if u != "" {
			out.Images = append(out.Images, picture{URL: u})
		}
	}
	if len(out.Images) == 0 {
		if n, _ := strconv.Atoi(strings.Trim(string(res.Metadata.Failed), `"`)); n > 0 {
			return out, http.StatusBadRequest, fmt.Errorf("%s answered with no image: MiniMax held back %d as sensitive content; change what the image asks for", provider.HostOf(url), n)
		}
		return out, 502, fmt.Errorf("%s answered with no image%s", provider.HostOf(url), vendorSaid(res.BaseResp.StatusMsg))
	}
	return out, code, nil
}
