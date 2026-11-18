package gateway

// Alibaba's Token Plan (the Qwen AI platform's, Bailian's, Model Studio's
// and Qwen Cloud's) makes videos with Wan 3.0 and HappyHorse 1.1 at
// DashScope's own asynchronous API (help.aliyun.com/zh/model-studio/
// wan3-video-generation-api-reference, docs.qwencloud.com's Token Plan
// "Integrate multimodal generation", read 11-18):
//
//	POST <root>/api/v1/services/aigc/video-generation/video-synthesis
//	X-DashScope-Async: enable
//	{"model": …, "input": {"prompt": …, "media": [{"type": "first_frame"|
//	  "last_frame"|"reference_image", "url": <URL or data URL>}…]},
//	 "parameters": {"resolution": "1080P"|"720P"|"480P", "ratio": "16:9"…,
//	  "duration": 5, "watermark": false…}}
//
// answers with output.task_id; GET <root>/api/v1/tasks/<task_id> with
// output.task_status (PENDING, RUNNING, SUCCEEDED, FAILED, CANCELED,
// UNKNOWN) and, once it has succeeded, output.video_url, an MP4 kept for 24
// hours. The task answer names no model, so the video's id carries it.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// filmsDashScope is whether p is a key of a DashScope plan that names video
// models (the Token Plans' Wan 3.0 and HappyHorse): not a subscription's
// account, another magpie or a plugin.
func filmsDashScope(p provider.Provider) bool {
	return p.Account == nil && !p.IsPlugin() && !p.IsRemoteMagpie() && dashScope(p) && len(p.PlanVideos()) > 0
}

// makesDashScopeVideo is whether model is one of p's plan's video models.
func makesDashScopeVideo(p provider.Provider, model string) bool {
	return filmsDashScope(p) && slices.ContainsFunc(p.PlanVideos(), func(m catalog.Model) bool { return m.ID == model })
}

// dashScopeVideoRatios are the ratios DashScope's video API takes, besides
// adaptive (the first frame's own, its default).
var dashScopeVideoRatios = []string{"21:9", "16:9", "4:3", "1:1", "3:4", "9:16"}

// dashScopeInput are the fields of the caller's own that go in input, not
// parameters.
var dashScopeInput = []string{"negative_prompt"}

// dashScopeVideoBody is f as DashScope's video-synthesis API takes it: the
// prompt and images in input, each image's type saying what it is, the
// resolution and ratio from size unless the caller named their own, the
// length in whole seconds, and the caller's other fields as they came
// (watermark, audio, prompt_extend, seed…), DashScope's own.
func dashScopeVideoBody(model string, f filming) ([]byte, error) {
	if len(f.References) > 10 {
		return nil, fmt.Errorf("DashScope takes at most 10 reference images, not %d", len(f.References))
	}
	input := map[string]any{"prompt": f.Prompt}
	params := map[string]any{}
	for k, v := range f.Extra {
		if f.Form {
			v = formValue(k, v, []string{"resolution", "ratio", "negative_prompt"})
		}
		if slices.Contains(dashScopeInput, k) {
			input[k] = v
		} else {
			params[k] = v
		}
	}
	var media []map[string]string
	add := func(pic picture, kind string) {
		media = append(media, map[string]string{"type": kind, "url": pic.dataURL()})
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
	if len(media) > 0 {
		input["media"] = media
	}
	if _, ok := params["resolution"]; !ok {
		if r := pixelResolution(f.Size); r != "" {
			params["resolution"] = strings.ToUpper(r)
		}
	} else if s, ok := params["resolution"].(string); ok {
		params["resolution"] = strings.ToUpper(strings.TrimSpace(s))
	}
	if _, ok := params["ratio"]; !ok {
		size := strings.TrimSpace(f.Size)
		if a, b, ok := strings.Cut(size, ":"); ok {
			size = a + "x" + b
		}
		if r := aspectAmong(size, dashScopeVideoRatios); r != "" {
			params["ratio"] = r
		}
	}
	if f.Seconds != "" {
		n, err := strconv.Atoi(strings.TrimSpace(f.Seconds))
		if err != nil {
			return nil, fmt.Errorf("seconds %q isn't a whole number of seconds, as DashScope's video models take", f.Seconds)
		}
		params["duration"] = n
	}
	if _, ok := params["watermark"]; !ok {
		params["watermark"] = false
	}
	return json.Marshal(map[string]any{"model": model, "input": input, "parameters": params})
}

// dashScopeVideoURL is where p starts a video.
func dashScopeVideoURL(p provider.Provider) string {
	return dashScopeRoot(p) + "/api/v1/services/aigc/video-generation/video-synthesis"
}

// dashScopeAsync is the header DashScope starts a task with, and without
// which its video API answers that it takes none but async calls.
var dashScopeAsync = http.Header{"X-DashScope-Async": {"enable"}}

// dashScopeStarted is the task DashScope's answer b starts, as videosCreate
// reads one: {"task_id": <the video's vendor id>}, the model put in it as
// dashScopeTask reads it back. An answer with no task says what DashScope
// said.
func dashScopeStarted(p provider.Provider, model string, b []byte) ([]byte, error) {
	var res struct {
		Output struct {
			TaskID string `json:"task_id"`
		} `json:"output"`
	}
	if json.Unmarshal(b, &res) != nil || res.Output.TaskID == "" {
		if said := dashScopeSaid(b); said != "" {
			return nil, fmt.Errorf("%s: %s", p.Name, said)
		}
		return nil, fmt.Errorf("%s started no video: its answer had no output.task_id: %.200s", p.Name, b)
	}
	out, _ := json.Marshal(map[string]string{"task_id": base64.RawURLEncoding.EncodeToString([]byte(model + " " + res.Output.TaskID))})
	return out, nil
}

// dashScopeTask is the model and DashScope's task id a video's vendor id
// holds (dashScopeStarted).
func dashScopeTask(vendorID string) (model, task string, ok bool) {
	b, err := base64.RawURLEncoding.DecodeString(vendorID)
	if err != nil {
		return "", "", false
	}
	model, task, ok = strings.Cut(string(b), " ")
	return model, task, ok && task != "" && !strings.ContainsAny(task, "/?#")
}

// dashScopeVideoStatus asks DashScope how a video task is going, as a
// videoState.
func (s *Server) dashScopeVideoStatus(ctx context.Context, p provider.Provider, vendorID string) (videoState, int, error) {
	var st videoState
	model, task, ok := dashScopeTask(vendorID)
	if !ok {
		return st, 404, errors.New("this isn't the id of a video magpie started at DashScope")
	}
	at := dashScopeRoot(p) + "/api/v1/tasks/" + url.PathEscape(task)
	b, code, err := s.sendAs(ctx, p, http.MethodGet, at, "", nil, true)
	if err != nil {
		if said := dashScopeSaid(b); said != "" {
			err = fmt.Errorf("%s: %d %s: %s", provider.HostOf(at), code, http.StatusText(code), said)
		}
		return st, code, err
	}
	var res struct {
		Output struct {
			Status   string `json:"task_status"`
			VideoURL string `json:"video_url"`
			Code     string `json:"code"`
			Message  string `json:"message"`
		} `json:"output"`
		Usage struct {
			Duration float64 `json:"duration"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(b, &res); err != nil || res.Output.Status == "" {
		if said := dashScopeSaid(b); said != "" {
			return st, 502, fmt.Errorf("%s: %s", p.Name, said)
		}
		return st, 502, fmt.Errorf("%s's answer isn't a video task's: %.200s", p.Name, b)
	}
	st.Model, st.Duration = model, res.Usage.Duration
	switch res.Output.Status {
	case "SUCCEEDED":
		st.Status, st.Video.URL = "succeeded", res.Output.VideoURL
	case "RUNNING":
		st.Status = "running"
	case "FAILED", "UNKNOWN":
		// UNKNOWN: a task DashScope no longer has, past its 24 hours
		st.Status = "failed"
		st.Error.Code, st.Error.Message = res.Output.Code, res.Output.Message
		if res.Output.Status == "UNKNOWN" && st.Error.Message == "" {
			st.Status, st.Error.Code = "expired", "expired"
		}
	case "CANCELED":
		st.Status = "cancelled"
	default: // PENDING
		st.Status = "queued"
	}
	return st, 200, nil
}
