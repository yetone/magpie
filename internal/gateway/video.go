package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

// The gateway makes videos: POST /v1/videos, GET /v1/videos/{id} and
// GET /v1/videos/{id}/content take OpenAI's Videos API and answer in its
// shape, so its SDKs work unchanged. A video takes seconds to minutes, so a
// request starts one and answers at once with its id; the id is what asks
// for it again. It says who is making the video, video_<provider>.<the
// vendor's id>.<when it started>, so the gateway keeps nothing.
//
// Only a Grok subscription makes videos so far, at the Imagine API of the
// backend Grok Build talks to: <cli-chat-proxy.grok.com/v1>/videos/generations
// starts one and answers with its request_id, and /videos/{request_id} is
// answered 202 while it is made and 200 with the video's URL when it is.

// grokVideoModels are the video models a Grok subscription makes videos with.
var grokVideoModels = []catalog.Model{
	{ID: "grok-imagine-video", Name: "Grok Imagine Video"},
	{ID: "grok-imagine-video-1.5", Name: "Grok Imagine Video 1.5"},
}

// grokVideoAspects are the aspect ratios Grok's video API takes.
var grokVideoAspects = []string{"1:1", "16:9", "9:16", "4:3", "3:4", "3:2", "2:3"}

// maxVideoBytes is the most of a video the gateway passes on.
const maxVideoBytes = 512 << 20

// Videomakers are the models a provider can make videos with.
func Videomakers(p provider.Provider) []catalog.Model {
	if !drawsGrok(p) {
		return nil
	}
	out := slices.Clone(grokVideoModels)
	for i := range out {
		out[i].Provider = p.ID
	}
	return out
}

// videomaker is the model a request that names none makes its video with:
// AutoVideomaker's, unless the Settings turned image generation off, which
// turns video off with it. None when it is off or no provider makes videos.
func videomaker() (string, bool) {
	if settings.Load().ImageGen == "off" {
		return "", false
	}
	m := AutoVideomaker()
	return m, m != ""
}

// AutoVideomaker is the model a request that names none makes its video
// with: the first model of the first provider that makes any. "" when none
// can.
func AutoVideomaker() string {
	for _, p := range provider.All() {
		if !p.On() || p.Decides() {
			continue
		}
		if ms := Videomakers(p); len(ms) > 0 {
			return p.ID + "/" + ms[0].ID
		}
	}
	return ""
}

// bareVideomaker is the provider that makes videos with model named without
// its provider: grok-imagine-video, as the vendor names it, not only
// grok/grok-imagine-video.
func bareVideomaker(name string) (provider.Provider, string, bool) {
	if strings.Contains(name, "/") {
		return provider.Provider{}, "", false
	}
	for _, p := range provider.All() {
		if !p.On() || p.Decides() {
			continue
		}
		for _, m := range Videomakers(p) {
			if m.ID == name {
				return p, name, true
			}
		}
	}
	return provider.Provider{}, "", false
}

// filming is one videos request, whichever shape it came in.
type filming struct {
	Model, Prompt, Seconds, Size string
	Start                        *picture  // the first frame to animate
	References                   []picture // images the video draws its subjects from
}

// readFilming reads a videos request: JSON, or the multipart form OpenAI's
// takes, the image to start from its input_reference.
func readFilming(r *http.Request) (filming, error) {
	var f filming
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if ct == "multipart/form-data" {
		if err := r.ParseMultipartForm(64 << 20); err != nil {
			return f, err
		}
		m := r.MultipartForm
		get := func(k string) string {
			if v := m.Value[k]; len(v) > 0 {
				return strings.TrimSpace(v[0])
			}
			return ""
		}
		f.Model, f.Prompt, f.Seconds, f.Size = get("model"), get("prompt"), get("seconds"), get("size")
		if fhs := m.File["input_reference"]; len(fhs) > 0 {
			pic, err := readPart(fhs[0])
			if err != nil {
				return f, err
			}
			f.Start = &pic
		}
		for _, k := range []string{"reference_images", "reference_images[]"} {
			for _, fh := range m.File[k] {
				pic, err := readPart(fh)
				if err != nil {
					return f, err
				}
				f.References = append(f.References, pic)
			}
		}
	} else {
		body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
		if err != nil {
			return f, err
		}
		var in struct {
			Model           string          `json:"model"`
			Prompt          string          `json:"prompt"`
			Seconds         json.RawMessage `json:"seconds"`
			Size            string          `json:"size"`
			InputReference  json.RawMessage `json:"input_reference"`
			ReferenceImages json.RawMessage `json:"reference_images"`
		}
		if err := json.Unmarshal(body, &in); err != nil {
			return f, fmt.Errorf("the request isn't JSON: %v", err)
		}
		f = filming{Model: in.Model, Prompt: in.Prompt, Size: in.Size, Seconds: strings.Trim(strings.TrimSpace(string(in.Seconds)), `"`)}
		if f.Seconds == "null" {
			f.Seconds = ""
		}
		if srcs := imageSources(in.InputReference); len(srcs) > 0 {
			pic, err := pictureOf(srcs[0])
			if err != nil {
				return f, err
			}
			f.Start = &pic
		}
		for _, src := range imageSources(in.ReferenceImages) {
			pic, err := pictureOf(src)
			if err != nil {
				return f, err
			}
			f.References = append(f.References, pic)
		}
	}
	f.Model = strings.TrimSpace(f.Model)
	if strings.TrimSpace(f.Prompt) == "" {
		return f, errors.New("say what to film in prompt")
	}
	if f.Seconds != "" {
		n, err := parseSeconds(f.Seconds)
		if err != nil {
			return f, err
		}
		f.Seconds = strconv.Itoa(n)
	}
	return f, nil
}

// decimalNumber is a number as JSON writes it, not as Go's ParseFloat also
// takes it (hex, inf, nan, underscores).
var decimalNumber = regexp.MustCompile(`^[0-9]+(\.[0-9]*)?([eE][+-]?[0-9]+)?$`)

// parseSeconds is a duration in whole seconds, however it was written: 6,
// "6", 6.0, 6e0. What the vendor's limit makes of a length it says itself.
func parseSeconds(v string) (int, error) {
	whole := func() (int, bool) {
		if !decimalNumber.MatchString(v) {
			return 0, false
		}
		n, err := strconv.ParseFloat(v, 64)
		if err != nil || n != math.Trunc(n) || n < 1 || n > math.MaxInt32 {
			return 0, false
		}
		return int(n), true
	}
	if n, ok := whole(); ok {
		return n, nil
	}
	return 0, fmt.Errorf("seconds is a whole number of at least 1, not %q", v)
}

// videoResolution is the resolution Grok's video API is asked for when size
// says WIDTHxHEIGHT: by the shorter side, and no more than the model makes
// (grok-imagine-video stops at 720p, which the vendor turns 1080p away for;
// 1.5 makes 1080p). "" when size names no pixels.
func videoResolution(model, size string) string {
	res := pixelResolution(size)
	if res == "1080p" && model == "grok-imagine-video" {
		return "720p"
	}
	return res
}

func pixelResolution(size string) string {
	w, h, ok := strings.Cut(strings.ToLower(strings.TrimSpace(size)), "x")
	if !ok {
		return ""
	}
	x, err1 := strconv.ParseFloat(w, 64)
	y, err2 := strconv.ParseFloat(h, 64)
	if err1 != nil || err2 != nil || x <= 0 || y <= 0 {
		return ""
	}
	switch short := math.Min(x, y); {
	case short >= 1080:
		return "1080p"
	case short >= 720:
		return "720p"
	}
	return "480p"
}

// grokVideoBody is f as Grok's video API takes it: the duration in whole
// seconds, an aspect ratio and a resolution rather than a size, the first
// frame as image and the subjects to draw from as reference_images. What
// the API turns away (a duration out of range) it says itself.
func grokVideoBody(model string, f filming) ([]byte, error) {
	req := map[string]any{"model": model, "prompt": f.Prompt}
	if f.Seconds != "" {
		n, err := parseSeconds(f.Seconds)
		if err != nil {
			return nil, err
		}
		req["duration"] = n
	}
	if ar := aspectAmong(f.Size, grokVideoAspects); ar != "" {
		req["aspect_ratio"] = ar
	}
	if res := videoResolution(model, f.Size); res != "" {
		req["resolution"] = res
	}
	if f.Start != nil {
		req["image"] = map[string]string{"url": f.Start.dataURL()}
	}
	if len(f.References) > 0 {
		var refs []map[string]string
		for _, pic := range f.References {
			refs = append(refs, map[string]string{"url": pic.dataURL()})
		}
		req["reference_images"] = refs
	}
	return json.Marshal(req)
}

// videoID is the id of a video p is making for vendorID, started at t. It
// names the provider, the vendor's id, the start and whose sign-in started it.
func videoID(p provider.Provider, vendorID string, t time.Time) string {
	return "video_" + p.ID + "." + vendorID + "." + strconv.FormatInt(t.Unix(), 10) + "." + signer(p)
}

// signer is a short mark of the account p signs in as, so a video can be
// told from those of the account signed in before a switch.
func signer(p provider.Provider) string {
	if p.Account == nil {
		return "0"
	}
	sum := sha256.Sum256([]byte(p.Account.User))
	return hex.EncodeToString(sum[:3])
}

// idPart is what a provider id or a vendor's request id is made of; an id
// is put in a URL, so nothing that would change its path gets through.
var idPart = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// parseVideoID is the provider, vendor id, start and signer of a video's id.
func parseVideoID(id string) (providerID, vendorID string, started time.Time, by string, ok bool) {
	rest, found := strings.CutPrefix(id, "video_")
	if !found {
		return
	}
	parts := strings.Split(rest, ".")
	if len(parts) != 4 || !idPart.MatchString(parts[0]) || !idPart.MatchString(parts[1]) || !idPart.MatchString(parts[3]) {
		return
	}
	sec, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return
	}
	return parts[0], parts[1], time.Unix(sec, 0), parts[3], true
}

// videoState is how the vendor says a video is going.
type videoState struct {
	Status   string `json:"status"`
	Progress int    `json:"progress"`
	Model    string `json:"model"`
	Video    struct {
		URL      string  `json:"url"`
		Duration float64 `json:"duration"`
	} `json:"video"`
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// videoObject is a video as OpenAI's API gives it.
func videoObject(id, model string, started time.Time, st videoState, f filming) map[string]any {
	status, progress := "queued", 0
	switch st.Status {
	case "done":
		status, progress = "completed", 100
	case "failed", "expired":
		status, progress = "failed", st.Progress
	default:
		if st.Progress > 0 {
			status, progress = "in_progress", st.Progress
		}
	}
	obj := map[string]any{"id": id, "object": "video", "created_at": started.Unix(), "status": status, "progress": progress, "model": model}
	if f.Prompt != "" {
		obj["prompt"] = f.Prompt
	}
	if f.Size != "" {
		obj["size"] = f.Size
	}
	switch {
	case st.Video.Duration > 0:
		obj["seconds"] = strconv.FormatFloat(st.Video.Duration, 'f', -1, 64)
	case f.Seconds != "":
		obj["seconds"] = f.Seconds
	}
	if status == "failed" {
		code, msg := st.Error.Code, st.Error.Message
		if st.Status == "expired" {
			code, msg = "expired", "the vendor no longer keeps this video"
		}
		if msg == "" {
			msg = "the video failed"
		}
		obj["error"] = map[string]string{"code": code, "message": msg}
	}
	return obj
}

// videoMaker is the provider a video's id names, when it makes videos.
func videoMaker(id string) (p provider.Provider, vendorID string, started time.Time, err error) {
	pid, vendorID, started, by, ok := parseVideoID(id)
	if !ok {
		return p, "", started, fmt.Errorf("%q isn't the id of a video magpie is making", id)
	}
	found, ferr := provider.Find(pid)
	if ferr != nil || !drawsGrok(*found) {
		return p, "", started, fmt.Errorf("no provider %q makes videos here", pid)
	}
	if signer(*found) != by {
		return p, "", started, fmt.Errorf("this video was started with another %s account than the one signed in now: sign back in to it to get the video", found.Name)
	}
	return *found, vendorID, started, nil
}

// videoStatus asks the vendor how a video is going.
func (s *Server) videoStatus(ctx context.Context, p provider.Provider, vendorID string) (videoState, int, error) {
	var st videoState
	b, code, err := s.sendAs(ctx, p, http.MethodGet, strings.TrimRight(p.Base(provider.Responses), "/")+"/videos/"+url.PathEscape(vendorID), "", nil, true)
	if err != nil {
		return st, code, err
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return st, 502, fmt.Errorf("%s's answer isn't a video's: %v", p.Name, err)
	}
	return st, code, nil
}

// videosCreate starts a video and answers with its id.
func (s *Server) videosCreate(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	f, err := readFilming(r)
	if err != nil {
		writeError(w, provider.Chat, 400, err.Error())
		return
	}
	who := callerOf(r)
	call := Call{Time: start, From: provider.Chat, Agent: who.agent, Via: who.via, Model: f.Model}
	usage.Saw(agentOf(r))
	fail := func(code int, msg string) {
		call.Status, call.Error, call.Millis = code, msg, time.Since(start).Milliseconds()
		writeError(w, provider.Chat, code, msg)
		s.record(call)
	}
	if f.Model == "" {
		m, ok := videomaker()
		if !ok {
			fail(400, "no model to make videos with: sign in to a Grok subscription and leave Settings → Images → Image generation on, or name one")
			return
		}
		f.Model, call.Model = m, m
	}
	p, model, ok := provider.Resolve(f.Model)
	if !ok {
		p, model, ok = bareVideomaker(f.Model)
	}
	if !ok {
		if off, isOff := provider.SwitchedOff(f.Model); isOff {
			fail(404, switchedOff(off, f.Model))
			return
		}
		fail(404, fmt.Sprintf("magpie knows no model %q to make videos with", f.Model))
		return
	}
	call.Provider, call.To = p.ID, provider.Chat
	// any grok-imagine-video*, not only those listed: the vendor's newer ones work before magpie names them
	if len(Videomakers(p)) == 0 || !strings.HasPrefix(model, "grok-imagine-video") {
		fail(400, fmt.Sprintf("%s/%s can't make videos: magpie makes videos with a Grok subscription's grok-imagine-video", p.ID, model))
		return
	}
	body, err := grokVideoBody(model, f)
	if err != nil {
		fail(400, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), drawTimeout)
	defer cancel()
	b, code, err := s.send(ctx, p, strings.TrimRight(p.Base(provider.Responses), "/")+"/videos/generations", "application/json", body, true)
	call.Millis, call.Status = time.Since(start).Milliseconds(), code
	var started struct {
		ID string `json:"request_id"`
	}
	if err == nil {
		if json.Unmarshal(b, &started) != nil || started.ID == "" {
			code, err = 502, fmt.Errorf("%s started no video: its answer had no request_id", p.Name)
			call.Status = code
		}
	}
	appendUsage(r, usage.Record{Operation: "generate_content", Time: start, Agent: call.Agent, Via: call.Via, Provider: p.ID, Host: p.Where(), Model: model, Requested: call.Model,
		Millis: call.Millis, Status: call.Status, Session: sessionOf(r.Header)})
	if err != nil {
		call.Error = err.Error()
		s.record(call)
		writeError(w, provider.Chat, code, err.Error())
		return
	}
	s.record(call)
	writeJSON(w, 200, videoObject(videoID(p, started.ID, start), p.ID+"/"+model, start, videoState{}, f))
}

// videosGet answers how a video is going.
func (s *Server) videosGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, vendorID, started, err := videoMaker(id)
	if err != nil {
		writeError(w, provider.Chat, 404, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), drawTimeout)
	defer cancel()
	st, code, err := s.videoStatus(ctx, p, vendorID)
	if err != nil {
		writeError(w, provider.Chat, code, err.Error())
		return
	}
	model := st.Model
	if model == "" {
		model = "grok-imagine-video"
	}
	writeJSON(w, 200, videoObject(id, p.ID+"/"+model, started, st, filming{}))
}

// videosContent sends the video's bytes once it is made.
func (s *Server) videosContent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, vendorID, _, err := videoMaker(id)
	if err != nil {
		writeError(w, provider.Chat, 404, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), drawTimeout)
	defer cancel()
	st, code, err := s.videoStatus(ctx, p, vendorID)
	if err != nil {
		writeError(w, provider.Chat, code, err.Error())
		return
	}
	if st.Status != "done" {
		obj := videoObject(id, "", time.Time{}, st, filming{})
		msg := fmt.Sprintf("the video isn't ready: it is %v", obj["status"])
		if e, ok := obj["error"].(map[string]string); ok {
			msg += ": " + e["message"]
		}
		writeError(w, provider.Chat, 409, msg)
		return
	}
	if st.Video.URL == "" {
		writeError(w, provider.Chat, 502, p.Name+" made the video but gave no URL for it")
		return
	}
	req, err := http.NewRequestWithContext(p.Via(ctx), http.MethodGet, st.Video.URL, nil)
	if err != nil {
		writeError(w, provider.Chat, 502, err.Error())
		return
	}
	res, err := s.client.Do(req)
	if err != nil {
		writeError(w, provider.Chat, 502, fmt.Sprintf("the video couldn't be fetched: %v", err))
		return
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		writeError(w, provider.Chat, 502, fmt.Sprintf("the video couldn't be fetched: %d %s", res.StatusCode, http.StatusText(res.StatusCode)))
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	if res.ContentLength > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(res.ContentLength, 10))
	}
	w.WriteHeader(200)
	io.Copy(w, io.LimitReader(res.Body, maxVideoBytes))
}
