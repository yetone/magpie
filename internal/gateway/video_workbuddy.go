package gateway

// A WorkBuddy plan makes videos with the models its product config tags
// "text-to-video" and "image-to-video" (GET <site>/v3/config, the list
// wbImageModelsOf keeps; the China build's lists kling-video-v3 and
// minimax-video-h3, plugins#1585), each with its videoGeneration spec:
// the resolutions, ratios and lengths it takes, and its defaults. They are
// asked as WorkBuddy's CLI asks them (@tencent-ai/codebuddy-code, the
// VideoGen tool's VideoService, read in WorkBuddy AI's 5.6.2 and the China
// build's 5.5.6):
//
//	POST <site>/v2/videos/generations
//	{"prompt": …, "model": …, "seconds": 5, "negative_prompt": "",
//	 "watermark": true, "image_url": <data URL>, "last_image_url": …,
//	 "extra_parameters": {"resolution": "720P", "enable_audio": true,
//	  "aspect_ratio": "16:9"}}
//
// answers {code, msg, requestId, data: {id}}, a code other than 0 an
// error; POST <site>/v2/videos/tasks {"task_id": id} answers {code, msg,
// data: {status, data: [{url}], usage: {credit}}}, status "completed" or
// "failed" once it is done, and the video is a plain GET of data[0].url.
// aspect_ratio goes with text alone: a first frame gives the video its
// shape. The task answer names no model, so the video's id carries it, as
// DashScope's does. Each video costs the plan's credits (the China build's
// tool says 50 to 100 for five seconds), so AutoVideomaker never picks
// one.
//
// It is the same for the built-in and for a WorkBuddy moved onto its
// plugin: magpie shapes the request, the plugin's fetch signs and sends it.

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// wbVideoSpec is what a WorkBuddy config says a video model takes.
type wbVideoSpec struct {
	AspectRatios        []string `json:"aspectRatios"`
	DefaultAspectRatio  string   `json:"defaultAspectRatio"`
	Resolutions         []string `json:"resolutions"`
	DefaultResolution   string   `json:"defaultResolution"`
	DefaultSeconds      int      `json:"defaultSeconds"`
	MinSeconds          int      `json:"minSeconds"`
	MaxSeconds          int      `json:"maxSeconds"`
	SupportedSeconds    []int    `json:"supportedSeconds"`
	AudioMode           string   `json:"audioMode"` // "switchable", "always-on"
	DefaultEnableAudio  *bool    `json:"defaultEnableAudio"`
	MaxPromptLength     int      `json:"maxPromptLength"`
	SupportsFirstFrame  *bool    `json:"supportsFirstFrame"`
	SupportsLastFrame   *bool    `json:"supportsFirstLastFrame"`
	LastNeedsFirstFrame bool     `json:"lastFrameRequiresFirstFrame"`
}

// filmsWorkBuddy is whether p is a WorkBuddy plan, whose video models are
// those its config lists.
func filmsWorkBuddy(p provider.Provider) bool { return drawsWorkBuddy(p) }

func (m wbImageModel) films() bool {
	return m.tagged("text-to-video") || m.tagged("image-to-video")
}

// wbVideomakers are the models a WorkBuddy plan makes videos with, as its
// config last said (asked again in the background when that's old).
func wbVideomakers(p provider.Provider) []catalog.Model {
	ms, _ := wbImageModelsOf(context.Background(), wbConfigClient, p, false)
	var out []catalog.Model
	for _, m := range ms {
		if m.films() {
			out = append(out, catalog.Model{ID: m.ID, Name: cmp.Or(m.Name, m.ID), Provider: p.ID, Films: true})
		}
	}
	return out
}

// wbVideoModel is model as p's config lists it, asking the config when what
// magpie has is old: ok false when it lists no such video model.
func (s *Server) wbVideoModel(ctx context.Context, p provider.Provider, model string) (wbImageModel, bool, error) {
	ms, err := wbImageModelsOf(ctx, s.client, p, true)
	for _, m := range ms {
		if m.ID == model && m.films() {
			return m, true, nil
		}
	}
	return wbImageModel{}, false, err
}

// wbVideoNames are the video models ms lists, for a refusal to name.
func wbVideoNames(ms []catalog.Model) string {
	var ids []string
	for _, m := range ms {
		ids = append(ids, m.ID)
	}
	if len(ids) == 0 {
		return "none"
	}
	return strings.Join(ids, ", ")
}

// wbResolutionLines is the frame's shorter side each of WorkBuddy's
// resolution names means, to pick one by a size's pixels.
func wbResolutionLines(r string) float64 {
	r = strings.ToUpper(strings.TrimSpace(r))
	switch r {
	case "2K":
		return 1440
	case "4K":
		return 2160
	}
	n, err := strconv.ParseFloat(strings.TrimSuffix(r, "P"), 64)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// wbResolutionFor is the one of rs nearest a WIDTHxHEIGHT size by its
// shorter side: "" when size names no pixels.
func wbResolutionFor(size string, rs []string) string {
	w, h, ok := strings.Cut(strings.ToLower(strings.TrimSpace(size)), "x")
	if !ok {
		return ""
	}
	x, err1 := strconv.ParseFloat(w, 64)
	y, err2 := strconv.ParseFloat(h, 64)
	if err1 != nil || err2 != nil || x <= 0 || y <= 0 {
		return ""
	}
	best, bestD := "", math.Inf(1)
	for _, r := range rs {
		if n := wbResolutionLines(r); n > 0 {
			if d := math.Abs(math.Log(math.Min(x, y) / n)); d < bestD {
				best, bestD = r, d
			}
		}
	}
	return best
}

// wbVideoText reads a field of the caller's own that WorkBuddy takes as
// text.
func wbVideoText(f filming, k string) (string, bool, error) {
	v, ok := f.Extra[k]
	if !ok || v == nil {
		return "", false, nil
	}
	s, isText := v.(string)
	if !isText {
		return "", true, fmt.Errorf("%s is text, not %v", k, v)
	}
	return strings.TrimSpace(s), true, nil
}

// wbVideoBody is f as WorkBuddy's videos API takes it for model, whose
// config spec is m: the resolution, ratio and length its spec takes, its
// own defaults where the caller said none, frames as data URLs (fetched
// when the caller gave a URL, as WorkBuddy's CLI fetches them). What the
// model can't take is said here, before WorkBuddy is asked and anything
// is spent.
func (s *Server) wbVideoBody(ctx context.Context, model string, m wbImageModel, f filming) ([]byte, error) {
	spec := wbVideoSpec{}
	if m.Video != nil {
		spec = *m.Video
	}
	if len(f.References) > 0 {
		return nil, fmt.Errorf("WorkBuddy's %s takes no reference images: send the image the video starts from as input_reference", model)
	}
	framed := f.Start != nil || f.End != nil
	if framed && !m.tagged("image-to-video") {
		return nil, fmt.Errorf("WorkBuddy's %s films from a prompt only: its config lists it for text-to-video alone", model)
	}
	if !framed && !m.tagged("text-to-video") {
		return nil, fmt.Errorf("WorkBuddy's %s films an image: send the image the video starts from as input_reference", model)
	}
	if f.Start != nil && spec.SupportsFirstFrame != nil && !*spec.SupportsFirstFrame {
		return nil, fmt.Errorf("WorkBuddy's %s takes no first frame", model)
	}
	if f.End != nil && spec.SupportsLastFrame != nil && !*spec.SupportsLastFrame {
		return nil, fmt.Errorf("WorkBuddy's %s takes no last frame", model)
	}
	if f.End != nil && f.Start == nil && spec.LastNeedsFirstFrame {
		return nil, fmt.Errorf("WorkBuddy's %s takes a last frame only with the first (input_reference)", model)
	}
	most := cmp.Or(spec.MaxPromptLength, 2500)
	if n := utf8.RuneCountInString(f.Prompt); n > most {
		return nil, fmt.Errorf("WorkBuddy's %s takes a prompt of at most %d characters, not %d", model, most, n)
	}
	neg, _, err := wbVideoText(f, "negative_prompt")
	if err != nil {
		return nil, err
	}
	if n := utf8.RuneCountInString(neg); n > 2500 {
		return nil, fmt.Errorf("WorkBuddy takes a negative_prompt of at most 2500 characters, not %d", n)
	}

	// the length: the spec's default, or one within its range
	seconds := cmp.Or(spec.DefaultSeconds, 5)
	if f.Seconds != "" {
		n, err := parseSeconds(strings.TrimSpace(f.Seconds))
		if err != nil {
			return nil, err
		}
		switch {
		case len(spec.SupportedSeconds) > 0:
			if !slices.Contains(spec.SupportedSeconds, n) {
				return nil, fmt.Errorf("WorkBuddy's %s makes videos of %v seconds, not %d", model, spec.SupportedSeconds, n)
			}
		case spec.MinSeconds > 0 && spec.MaxSeconds > 0:
			if n < spec.MinSeconds || n > spec.MaxSeconds {
				return nil, fmt.Errorf("WorkBuddy's %s makes videos of %d to %d seconds, not %d", model, spec.MinSeconds, spec.MaxSeconds, n)
			}
		case n != seconds:
			return nil, fmt.Errorf("WorkBuddy's %s makes videos of %d seconds only", model, seconds)
		}
		seconds = n
	}

	// the resolution: the caller's, else the nearest of the spec's to
	// size, else the spec's default
	resolutions := spec.Resolutions
	if len(resolutions) == 0 {
		resolutions = []string{"720P", "1080P"} // the CLI's own, for a model with no spec
	}
	resolution := ""
	if r, named, err := wbVideoText(f, "resolution"); err != nil {
		return nil, err
	} else if named {
		i := slices.IndexFunc(resolutions, func(o string) bool { return strings.EqualFold(o, r) })
		if i < 0 {
			return nil, fmt.Errorf("WorkBuddy's %s makes videos at %s, not %s", model, strings.Join(resolutions, ", "), r)
		}
		resolution = resolutions[i]
	}
	if resolution == "" {
		resolution = wbResolutionFor(f.Size, resolutions)
	}
	if resolution == "" {
		resolution = cmp.Or(spec.DefaultResolution, resolutions[0])
	}
	extra := map[string]any{"resolution": resolution}

	// sound: the caller's, else the spec's default
	audio := spec.DefaultEnableAudio == nil || *spec.DefaultEnableAudio
	if v, ok := f.Extra["enable_audio"]; ok && v != nil {
		if f.Form {
			v = formValue("enable_audio", v, nil)
		}
		b, isBool := v.(bool)
		if !isBool {
			return nil, fmt.Errorf("enable_audio is true or false, not %v", v)
		}
		if !b && spec.AudioMode == "always-on" {
			return nil, fmt.Errorf("WorkBuddy's %s always makes its videos with sound", model)
		}
		audio = b
	}
	extra["enable_audio"] = audio

	// the ratio, with text alone: the caller's, else the nearest of the
	// spec's to size, else the spec's default
	ratios := slices.DeleteFunc(slices.Clone(spec.AspectRatios), func(r string) bool { return !strings.Contains(r, ":") })
	if len(spec.AspectRatios) == 0 {
		ratios = []string{"16:9", "9:16", "1:1"} // the CLI's own
	}
	r, named, err := wbVideoText(f, "aspect_ratio")
	if err != nil {
		return nil, err
	}
	if framed && named {
		return nil, errors.New("WorkBuddy takes no aspect_ratio with a frame: the image gives the video its shape")
	}
	if !framed {
		ratio := ""
		if named {
			listed := spec.AspectRatios
			if len(listed) == 0 {
				listed = ratios
			}
			if !slices.Contains(listed, r) {
				return nil, fmt.Errorf("WorkBuddy's %s makes videos at %s, not %s", model, strings.Join(listed, ", "), r)
			}
			ratio = r
		} else if size := strings.TrimSpace(f.Size); size != "" {
			if a, b, ok := strings.Cut(size, ":"); ok {
				size = a + "x" + b
			}
			ratio = aspectAmong(size, ratios)
		}
		if ratio == "" && strings.Contains(spec.DefaultAspectRatio, ":") {
			ratio = spec.DefaultAspectRatio
		}
		// none said: 16:9, as WorkBuddy's CLI sends with text alone, not
		// a default that is no ratio (MiniMax-H3's "adaptive")
		extra["aspect_ratio"] = cmp.Or(ratio, "16:9")
	}

	req := map[string]any{"prompt": f.Prompt, "model": model, "seconds": seconds, "negative_prompt": neg, "watermark": true}
	for k, pic := range map[string]*picture{"image_url": f.Start, "last_image_url": f.End} {
		if pic == nil {
			continue
		}
		u := pic.dataURL()
		if pic.URL != "" {
			// WorkBuddy's CLI sends a frame given by URL as its bytes
			b, mt, err := s.fetchPicture(ctx, pic.URL)
			if err != nil {
				return nil, err
			}
			u = picture{Mime: mt, Data: b}.dataURL()
		}
		req[k] = u
	}
	req["extra_parameters"] = extra
	return json.Marshal(req)
}

// wbEnvelope is WorkBuddy's {code, msg, data}.
type wbEnvelope struct {
	Code      json.RawMessage `json:"code"`
	Msg       string          `json:"msg"`
	RequestID string          `json:"requestId"`
	Data      json.RawMessage `json:"data"`
}

// refused is what WorkBuddy said when the envelope's code isn't 0: "" when
// it is.
func (e wbEnvelope) refused(p provider.Provider) string {
	c := strings.Trim(string(e.Code), `"`)
	if c == "" || c == "0" || c == "null" {
		return ""
	}
	return fmt.Sprintf("%s: %s (%s)", p.Name, cmp.Or(e.Msg, "error"), c)
}

// wbVideoStarted is the task WorkBuddy's answer b starts, as videosCreate
// reads one: {"task_id": <the video's vendor id>}, the model put in it
// (dashScopeTask reads it back).
func wbVideoStarted(p provider.Provider, model string, b []byte) ([]byte, error) {
	var env wbEnvelope
	if err := json.Unmarshal(b, &env); err != nil {
		return nil, fmt.Errorf("%s's answer isn't a video task's: %.200s", p.Name, b)
	}
	if msg := env.refused(p); msg != "" {
		return nil, errors.New(msg)
	}
	var data struct {
		ID json.RawMessage `json:"id"`
	}
	json.Unmarshal(env.Data, &data)
	id := strings.Trim(string(data.ID), `"`)
	if id == "" || id == "null" {
		return nil, fmt.Errorf("%s started no video: its answer had no data.id: %.200s", p.Name, b)
	}
	out, _ := json.Marshal(map[string]string{"task_id": base64.RawURLEncoding.EncodeToString([]byte(model + " " + id))})
	return out, nil
}

// wbVideoStatus asks WorkBuddy how a video task is going, as a videoState.
func (s *Server) wbVideoStatus(ctx context.Context, p provider.Provider, vendorID string) (videoState, int, error) {
	var st videoState
	model, task, ok := dashScopeTask(vendorID)
	if !ok {
		return st, 404, errors.New("this isn't the id of a video magpie started at WorkBuddy")
	}
	body, _ := json.Marshal(map[string]string{"task_id": task})
	at := strings.TrimRight(p.Base(provider.Chat), "/") + "/videos/tasks"
	b, code, err := s.sendAs(ctx, p, http.MethodPost, at, "application/json", body, true)
	if err != nil {
		return st, code, err
	}
	var env wbEnvelope
	if err := json.Unmarshal(b, &env); err != nil {
		return st, 502, fmt.Errorf("%s's answer isn't a video task's: %.200s", p.Name, b)
	}
	if msg := env.refused(p); msg != "" {
		return st, 502, errors.New(msg)
	}
	var data struct {
		Status string `json:"status"`
		Data   []struct {
			URL string `json:"url"`
		} `json:"data"`
		Message string `json:"message"`
		Error   any    `json:"error"`
	}
	if err := json.Unmarshal(env.Data, &data); err != nil || data.Status == "" {
		return st, 502, fmt.Errorf("%s's answer isn't a video task's: %.200s", p.Name, b)
	}
	st.Model = model
	switch strings.ToLower(data.Status) {
	case "completed", "succeeded", "success":
		st.Status = "succeeded"
		if len(data.Data) > 0 {
			st.Video.URL = data.Data[0].URL
		}
	case "failed", "failure", "error":
		st.Status, st.Error.Code = "failed", "failed"
		st.Error.Message = data.Message
		switch e := data.Error.(type) {
		case string:
			st.Error.Message = cmp.Or(e, st.Error.Message)
		case map[string]any:
			if m, ok := e["message"].(string); ok && m != "" {
				st.Error.Message = m
			}
		}
	case "queued", "pending", "submitted", "waiting":
		st.Status = "queued"
	default:
		st.Status = "running"
	}
	return st, 200, nil
}
