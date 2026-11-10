package gateway

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/provider"
)

// MiniMax makes videos with its H3 models on any MiniMax API key, at its
// own asynchronous API (platform.minimax.io/docs/api-reference/
// video-generation-v2-create): POST <host>/v2/video_generation answers with
// a task_id, GET <host>/v2/query/video_generation/<task_id> with the task,
// whose content.url is the MP4 once it has succeeded. The host is the
// key's own: api.minimax.io for the international platform,
// api.minimaxi.com (or api.minimax.cn) for the Chinese one. H3 is paid
// from the pay-as-you-go balance; a Token Plan key or a video package
// doesn't make it, which the vendor says with its own code (2061, 2068).

// minimaxVideoModels are the H3 models, in the vendor's order. They make
// videos only: none of them is ever a chat model.
var minimaxVideoModels = []catalog.Model{
	{ID: "MiniMax-H3", Name: "MiniMax H3"},
	{ID: "MiniMax-H3-Highspeed", Name: "MiniMax H3 Highspeed"},
	{ID: "MiniMax-H3-Lite", Name: "MiniMax H3 Lite"},
	{ID: "MiniMax-H3-Max", Name: "MiniMax H3 Max"},
}

// minimaxVideoLimits is what each H3 model takes: its resolutions, the
// first its default, and its shortest video in seconds. All go to 15.
var minimaxVideoLimits = map[string]struct {
	resolutions []string
	minSeconds  int
}{
	"MiniMax-H3":           {[]string{"768P", "2K"}, 4},
	"MiniMax-H3-Highspeed": {[]string{"768P"}, 4},
	"MiniMax-H3-Lite":      {[]string{"768P", "480P"}, 4},
	"MiniMax-H3-Max":       {[]string{"768P", "480P"}, 5},
}

// minimaxVideoRatios are the aspect ratios MiniMax's video API takes, besides
// adaptive (the images' own).
var minimaxVideoRatios = []string{"21:9", "16:9", "4:3", "1:1", "3:4", "9:16"}

// isMiniMaxHost is whether host is MiniMax's API. A var so tests can point a
// MiniMax provider at a fake server, as isArk does.
var isMiniMaxHost = func(host string) bool {
	switch strings.ToLower(host) {
	case "api.minimax.io", "api.minimaxi.com", "api.minimax.cn":
		return true
	}
	return false
}

// minimaxHost is scheme://host of the MiniMax API p's key is for, from its
// Chat, Anthropic or Responses base: "" when p isn't MiniMax's.
func minimaxHost(p provider.Provider) string {
	for _, base := range []string{p.Base(provider.Chat), p.Base(provider.Anthropic), p.Base(provider.Responses)} {
		u, err := url.Parse(strings.TrimSpace(base))
		if err != nil || u.Host == "" || !isMiniMaxHost(u.Host) {
			continue
		}
		return u.Scheme + "://" + u.Host
	}
	return ""
}

// filmsMiniMax is whether p is a MiniMax API key, which makes videos with
// the H3 models: not a subscription's account, another magpie or a plugin.
func filmsMiniMax(p provider.Provider) bool {
	return p.Account == nil && !p.IsRemoteMagpie() && !p.IsPlugin() && minimaxHost(p) != ""
}

// minimaxVideomakers are the H3 models as p's.
func minimaxVideomakers(p provider.Provider) []catalog.Model {
	out := slices.Clone(minimaxVideoModels)
	for i := range out {
		out[i].Provider, out[i].Films = p.ID, true
	}
	return out
}

// makesMiniMaxVideo is whether model is one of p's H3 models.
func makesMiniMaxVideo(p provider.Provider, model string) bool {
	return filmsMiniMax(p) && slices.ContainsFunc(minimaxVideoModels, func(m catalog.Model) bool { return m.ID == model })
}

// minimaxVideoBody is f as MiniMax's video API takes it: the prompt and
// images in content, the images' roles telling a first or last frame from a
// subject reference, and the duration, resolution and ratio each model
// takes. What it can't take (frames and references together) is said here,
// before the vendor is asked and nothing is spent.
func minimaxVideoBody(model string, f filming) ([]byte, error) {
	lim, ok := minimaxVideoLimits[model]
	if !ok {
		lim = minimaxVideoLimits["MiniMax-H3"]
	}
	frames := f.Start != nil || f.End != nil
	if frames && len(f.References) > 0 {
		return nil, errors.New("MiniMax makes a video from a first or last frame, or from reference images, not both: send one or the other")
	}
	if len(f.References) > 9 {
		return nil, fmt.Errorf("MiniMax takes at most 9 reference images, not %d", len(f.References))
	}
	content := []map[string]any{{"type": "text", "text": f.Prompt}}
	add := func(pic picture, role string) {
		content = append(content, map[string]any{"type": "image_url", "image_url": map[string]string{"url": pic.dataURL()}, "role": role})
	}
	if f.Start != nil {
		add(*f.Start, "first_frame")
	}
	if f.End != nil {
		add(*f.End, "last_frame")
	}
	for _, pic := range f.References {
		add(pic, "reference_image")
	}
	// the length in whole seconds, 5 unless said, within the model's
	seconds := 5
	if f.Seconds != "" {
		seconds, _ = strconv.Atoi(f.Seconds)
	}
	seconds = min(max(seconds, lim.minSeconds), 15)
	req := map[string]any{"model": model, "content": content, "duration": seconds, "resolution": minimaxResolution(lim.resolutions, f)}
	// with a frame the video takes the image's shape (the vendor ignores
	// any other); text alone needs one, and Lite's references too
	if !frames {
		ratio := minimaxRatio(f)
		if ratio == "" && (len(f.References) == 0 || model == "MiniMax-H3-Lite") {
			ratio = "16:9"
		}
		if ratio != "" {
			req["ratio"] = ratio
		}
	}
	// what the caller set of MiniMax's own fields: prompt_expansion_mode
	// (H3 Max), never where the vendor would call back to
	if extra, ok := f.Extra["extra"].(map[string]any); ok && model == "MiniMax-H3-Max" {
		req["extra"] = extra
	}
	return json.Marshal(req)
}

// minimaxResolution is the resolution a video is asked for: the caller's
// own (resolution: "480P"), else by size's shorter side, among the model's;
// the model's default when neither says.
func minimaxResolution(allowed []string, f filming) string {
	if s, ok := f.Extra["resolution"].(string); ok && strings.TrimSpace(s) != "" {
		return strings.ToUpper(strings.TrimSpace(s)) // what the model can't make the vendor says
	}
	want := ""
	switch pixelResolution(f.Size) {
	case "1080p":
		want = "2K"
	case "720p":
		want = "768P"
	case "480p":
		want = "480P"
	}
	if slices.Contains(allowed, want) {
		return want
	}
	if want == "2K" || want == "480P" {
		// the nearest it makes: 768P
		if slices.Contains(allowed, "768P") {
			return "768P"
		}
	}
	return allowed[0]
}

// minimaxRatio is the ratio size names, or the caller's ratio field, as
// one MiniMax takes: the nearest of its ratios. "" when neither says.
func minimaxRatio(f filming) string {
	size := strings.TrimSpace(f.Size)
	if r, ok := f.Extra["ratio"].(string); ok && strings.TrimSpace(r) != "" {
		size = strings.TrimSpace(r)
	}
	if strings.EqualFold(size, "adaptive") {
		return "adaptive"
	}
	if a, b, ok := strings.Cut(size, ":"); ok {
		size = a + "x" + b
	}
	return aspectAmong(size, minimaxVideoRatios)
}

// minimaxCode is the code MiniMax put at the end of its message: "insufficient
// balance (1008)" is 1008.
var minimaxCode = lazyre.New(`\((\d{3,5})\)\s*$`)

// minimaxSaid is what MiniMax's answer b says went wrong, and its code: its
// OpenAI-shaped error, or the base_resp its older APIs answer with.
func minimaxSaid(b []byte) (msg, code string) {
	var e struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
		BaseResp struct {
			StatusCode int    `json:"status_code"`
			StatusMsg  string `json:"status_msg"`
		} `json:"base_resp"`
	}
	if json.Unmarshal(b, &e) != nil {
		return "", ""
	}
	if msg = e.Error.Message; msg == "" && e.BaseResp.StatusCode != 0 {
		return e.BaseResp.StatusMsg, strconv.Itoa(e.BaseResp.StatusCode)
	}
	if m := minimaxCode.FindStringSubmatch(msg); m != nil {
		code = m[1]
	}
	return msg, code
}

// minimaxWhy is what a MiniMax error code (or, without one, its HTTP
// status) means for the user: the key, the balance, the content or the
// key's plan. "" when it is none of those.
func minimaxWhy(code string, status int) string {
	switch {
	case code == "1004" || code == "2049" || code == "" && status == http.StatusUnauthorized:
		return "MiniMax turned the API key away: it is wrong, or was deleted. Put a working key in the MiniMax provider"
	case code == "1008" || code == "" && status == http.StatusPaymentRequired:
		return "the MiniMax account is out of balance: H3 videos are paid from the pay-as-you-go balance. Top it up on MiniMax's platform"
	case code == "1026":
		return "MiniMax refused the prompt or images as sensitive content: change what the video asks for"
	case code == "1027":
		return "MiniMax refused the video it made as sensitive content: change what the video asks for"
	case code == "2061" || code == "2068" || code == "2057":
		return "this MiniMax key's plan doesn't make H3 videos: a Token Plan or video package doesn't cover H3; it needs a pay-as-you-go API key"
	case code == "1002" || code == "1045" || code == "" && status == http.StatusTooManyRequests:
		return "MiniMax is making as many videos as this account may at once, or was asked too often: try again shortly"
	}
	return ""
}

// minimaxLacksH3 is whether MiniMax said the key's plan doesn't make the
// model: the next key, a pay-as-you-go one, may.
func minimaxLacksH3(b []byte) bool {
	_, code := minimaxSaid(b)
	return code == "2061" || code == "2068" || code == "2057"
}

// minimaxError is err (MiniMax turning a video away with status and body
// b) with what it means first: a bad key, an empty balance and refused
// content each said for what they are.
func minimaxError(status int, b []byte, err error) error {
	_, code := minimaxSaid(b)
	if why := minimaxWhy(code, status); why != "" {
		return fmt.Errorf("%s — %w", why, err)
	}
	return err
}

// minimaxTask is a MiniMax video task as its query answers it.
type minimaxTask struct {
	Task struct {
		ID       string  `json:"id"`
		Model    string  `json:"model"`
		Status   string  `json:"status"`
		Duration float64 `json:"duration"`
		Content  struct {
			URL string `json:"url"`
		} `json:"content"`
		Error struct {
			Code    json.RawMessage `json:"code"`
			Message string          `json:"message"`
		} `json:"error"`
	} `json:"task"`
}

// minimaxVideoStatus asks MiniMax how a video task is going, as a videoState.
func (s *Server) minimaxVideoStatus(ctx context.Context, p provider.Provider, taskID string) (videoState, int, error) {
	var st videoState
	at := minimaxHost(p) + "/v2/query/video_generation/" + url.PathEscape(taskID)
	b, code, err := s.sendAs(ctx, p, http.MethodGet, at, "", nil, true)
	if err != nil {
		return st, code, minimaxError(code, b, err)
	}
	var t minimaxTask
	if err := json.Unmarshal(b, &t); err != nil || t.Task.Status == "" {
		if msg, mcode := minimaxSaid(b); msg != "" {
			return st, 502, minimaxError(0, b, fmt.Errorf("%s: %s", p.Name, msg+codeUnlessSaid(msg, mcode)))
		}
		return st, 502, fmt.Errorf("%s's answer isn't a video task's: %.200s", p.Name, b)
	}
	st.Status, st.Model, st.Duration = t.Task.Status, t.Task.Model, t.Task.Duration
	st.Video.URL = t.Task.Content.URL
	if t.Task.Status == "failed" {
		st.Error.Code = strings.Trim(string(t.Task.Error.Code), `"`)
		st.Error.Message = t.Task.Error.Message
		if why := minimaxWhy(st.Error.Code, 0); why != "" {
			st.Error.Message = why + " — MiniMax: " + cmp.Or(t.Task.Error.Message, "the video failed") + " (" + st.Error.Code + ")"
		}
	}
	return st, 200, nil
}

// codeUnlessSaid is " (code)" when msg doesn't end with it already.
func codeUnlessSaid(msg, code string) string {
	if code == "" || strings.HasSuffix(strings.TrimSpace(msg), "("+code+")") {
		return ""
	}
	return " (" + code + ")"
}
