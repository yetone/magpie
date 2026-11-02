package gateway

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/base64"
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
// A Grok subscription uses the Imagine API of the backend Grok Build talks
// to: <cli-chat-proxy.grok.com/v1>/videos/generations starts one and answers
// with its request_id. Volcengine Agent Plan uses Ark's asynchronous content
// generation tasks under its /api/plan/v3 base.
//
// Another magpie's videos (a Remote magpie's, #545) are asked of it, at its
// own videos API: its id for one goes, encoded, in the id given here, and
// what it answers comes back with this magpie's id and name for the model.
// A provider set up with a base URL whose own model list marks a model
// "kind": "video" (#1446) is asked the same way, at the OpenAI-shaped
// videos API under its base, on the key that started the video.

// grokVideoModels are the video models a Grok subscription makes videos with.
var grokVideoModels = []catalog.Model{
	{ID: "grok-imagine-video", Name: "Grok Imagine Video"},
	{ID: "grok-imagine-video-1.5", Name: "Grok Imagine Video 1.5"},
}

// grokVideoAspects are the aspect ratios Grok's video API takes.
var grokVideoAspects = []string{"1:1", "16:9", "9:16", "4:3", "3:4", "3:2", "2:3"}

// maxVideoBytes is the most of a video the gateway passes on.
const maxVideoBytes = 512 << 20

// videoTimeout is how long a video's vendor is given to answer each of its
// calls: to take the video on, say how it goes, and send it. A video is
// made between them, so Settings' Image generation timeout isn't it.
const videoTimeout = 5 * time.Minute

// Videomakers are the models a provider can make videos with: a Grok
// subscription's, another magpie's, a preset plan's models when magpie
// implements that vendor's video API, a MiniMax API key's H3 models
// (video_minimax.go), and those a provider's own model list marks as video
// models (customVideomakers).
func Videomakers(p provider.Provider) []catalog.Model {
	if p.IsRemoteMagpie() {
		out := catalog.LiveVideomakers(p.ID)
		for i := range out {
			out[i].Provider = p.ID
		}
		return out
	}
	if out := p.PlanVideos(); len(out) > 0 {
		return out
	}
	if filmsMiniMax(p) {
		return minimaxVideomakers(p)
	}
	if !drawsGrok(p) {
		return customVideomakers(p)
	}
	out := slices.Clone(grokVideoModels)
	for i := range out {
		out[i].Provider = p.ID
	}
	return out
}

// customVideomakers are the video models of a provider set up with a base
// URL — not a subscription's account, not another magpie, not a preset plan
// whose own video API magpie speaks — that its fetched model list marks
// "kind": "video" (catalog.LiveVideomakers, #1446), or the user said make
// videos (provider.SetModelKind, #1399). They are asked at the
// OpenAI-shaped videos API under its base, as another magpie's are. A
// model's name alone makes none of them one: a list that doesn't say so,
// and a user who doesn't, leave the provider making no videos. A plugin's
// are the models it says give video (capabilities.output.video), asked
// through its fetch on the account (XYenon's Google AI Studio plugin, Veo).
func customVideomakers(p provider.Provider) []catalog.Model {
	if p.IsPlugin() && !drawsGrok(p) {
		var out []catalog.Model
		for _, m := range p.Available() {
			if slices.Contains(m.OutputModalities, "video") {
				m.Provider = p.ID
				out = append(out, m)
			}
		}
		return out
	}
	if p.Account != nil || p.IsRemoteMagpie() || len(p.PlanVideos()) > 0 || filmsMiniMax(p) || videoBase(p) == "" {
		return nil
	}
	out := catalog.LiveVideomakers(p.ID)
	for i := range out {
		out[i].Provider = p.ID
	}
	if isArk(p) {
		// a Volcengine Ark key's Seedance models (pay-as-you-go, or one
		// added by hand), those its list has and those the user picked, as
		// Drawers takes a Seedream: its list marks no kind, and Ark makes
		// them at its own content-generation tasks (arkFilms)
		ms := p.Available()
		for _, id := range p.Models {
			ms = append(ms, catalog.Model{ID: id, Name: id})
		}
		for _, m := range ms {
			if arkVideoModel(m.ID) && !slices.ContainsFunc(out, func(o catalog.Model) bool { return o.ID == m.ID }) {
				m.Provider, m.Films = p.ID, true
				out = append(out, m)
			}
		}
	}
	// those the user said make videos, whatever the list says, and none
	// they said draws (shen-joe, #1399: a relay whose /models marks none)
	for _, id := range p.KindModels(provider.KindVideo) {
		if !slices.ContainsFunc(out, func(m catalog.Model) bool { return m.ID == id }) {
			out = append(out, kindModel(p, id))
		}
	}
	return slices.DeleteFunc(out, func(m catalog.Model) bool { return p.ModelKind(m.ID) == provider.KindImage })
}

// videoBase is the root of the OpenAI-shaped videos API of another magpie
// or a provider set up with a base URL: its Chat base, or a Responses-only
// relay's Responses base, under which it serves the images API too; a
// plugin's fetch whatever API its chat is on (Provider.OpenAIBase).
func videoBase(p provider.Provider) string {
	return strings.TrimRight(cmp.Or(p.OpenAIBase(), p.Base(provider.Responses)), "/")
}

// filmsCustom is whether p makes model as its model list says
// (customVideomakers): at its OpenAI-shaped videos API, or, at Volcengine
// Ark, at Ark's tasks (arkFilms).
func filmsCustom(p provider.Provider, model string) bool {
	return slices.ContainsFunc(customVideomakers(p), func(m catalog.Model) bool { return m.ID == model })
}

// arkVideoModel is whether id, a model of a Volcengine Ark key's list, is
// one of Ark's Seedance video models (doubao-seedance-1-0-pro-250528,
// doubao-seedance-2-0-260128…).
func arkVideoModel(id string) bool {
	return strings.Contains(strings.ToLower(id), "seedance")
}

// arkFilms is whether p makes model at Volcengine Ark's asynchronous
// content-generation tasks (<base>/contents/generations/tasks, 火山方舟
// 视频生成 API, https://www.volcengine.com/docs/82379/1520757): the
// preset's Agent Plan's Seedance, or a Seedance model of any other Ark
// key. Ark has no OpenAI-shaped videos API, so such a key's video models
// are never asked at <base>/videos.
func arkFilms(p provider.Provider, model string) bool {
	if p.IsRemoteMagpie() {
		return false
	}
	if p.Preset == "volcengine" && slices.ContainsFunc(p.PlanVideos(), func(m catalog.Model) bool { return m.ID == model }) {
		return true
	}
	return isArk(p) && filmsCustom(p, model)
}

// arkTasks is where p, at Volcengine Ark, starts and is asked after a video.
func arkTasks(p provider.Provider) string {
	return cmp.Or(strings.TrimRight(p.Base(provider.Responses), "/"), strings.TrimRight(p.Base(provider.Chat), "/")) + "/contents/generations/tasks"
}

// keyMark is the mark of the key p sends with, in the id of a video it
// started at its OpenAI-shaped videos API: the vendor keeps a video for the
// key that started it, so it is asked after on that key, whichever comes
// first now. "0" for a provider with no key (a local server); a plugin's
// account's signer, the vendor keeping it for that account.
func keyMark(p provider.Provider) string {
	if p.IsPlugin() {
		return "a" + signer(p)
	}
	if p.Key == "" {
		return "0"
	}
	return "k" + provider.KeyID(p.Key)
}

// customVideoID is the id of a video p started at its OpenAI-shaped videos
// API, by the vendor's id for it, which goes in base64 (it may hold dots).
func customVideoID(p provider.Provider, theirs string, t time.Time) string {
	return "video_" + p.ID + "." + base64.RawURLEncoding.EncodeToString([]byte(theirs)) + "." + strconv.FormatInt(t.Unix(), 10) + "." + keyMark(p)
}

// markedKey is p on its key whose mark is by (keyMark), while that key is
// still on.
func markedKey(p provider.Provider, by string) (provider.Provider, bool) {
	if p.IsPlugin() {
		// the plugin's account that started it, while it is still on
		for _, q := range append([]provider.Provider{p}, p.AlsoOn()...) {
			if keyMark(q) == by {
				return q, true
			}
		}
		return p, false
	}
	if p.Key == "" {
		return p, by == "0"
	}
	for _, k := range p.KeysOn() {
		if q := p.WithKey(k); keyMark(q) == by && (videoBase(q) != "" || filmsMiniMax(q)) {
			return q, true
		}
	}
	return p, false
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
// can. A MiniMax API key's H3 comes after every other: it is paid by the
// second from the key's balance, and a key set up for chat would
// otherwise take videos a subscription made before.
func AutoVideomaker() string {
	paid := ""
	for _, p := range provider.All() {
		if !p.On() || p.DecideOnly() {
			continue
		}
		if ms := Videomakers(p); len(ms) > 0 {
			if filmsMiniMax(p) {
				if paid == "" {
					paid = p.ID + "/" + ms[0].ID
				}
				continue
			}
			return p.ID + "/" + ms[0].ID
		}
	}
	return paid
}

// bareVideomaker is the provider that makes videos with model named without
// its provider: grok-imagine-video, as the vendor names it, not only
// grok/grok-imagine-video.
func bareVideomaker(name string) (provider.Provider, string, bool) {
	if strings.Contains(name, "/") {
		return provider.Provider{}, "", false
	}
	for _, p := range provider.All() {
		if !p.On() || p.DecideOnly() {
			continue
		}
		for _, m := range Videomakers(p) {
			if m.ID == name {
				return p, name, true
			}
		}
	}
	// any grok-imagine-video*, as videosCreate takes with the provider
	// named: the vendor's newer ones work before magpie lists them (#1857)
	for _, p := range provider.All() {
		if p.On() && !p.DecideOnly() && drawsGrok(p) && strings.HasPrefix(name, grokVideoFamily) {
			return p, name, true
		}
	}
	return provider.Provider{}, "", false
}

// grokVideoFamily is the family of a Grok subscription's video models.
const grokVideoFamily = "grok-imagine-video"

// filming is one videos request, whichever shape it came in.
type filming struct {
	Model, Prompt, Seconds, Size string
	Start                        *picture  // the first frame to animate
	End                          *picture  // the last frame, where the video ends (MiniMax)
	References                   []picture // images the video draws its subjects from
	// the request's other fields, which magpie doesn't read: a provider's
	// own videos API takes them as they came (Agnes's mode, aspect_ratio;
	// #1399, shen-joe: "mode 不能为空")
	Extra map[string]any
}

// filmingRead are the fields of a videos request magpie reads itself.
var filmingRead = []string{"model", "prompt", "seconds", "size", "input_reference", "last_frame", "reference_images", "reference_images[]"}

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
		for k := range m.Value {
			if !slices.Contains(filmingRead, k) {
				if f.Extra == nil {
					f.Extra = map[string]any{}
				}
				f.Extra[k] = get(k)
			}
		}
		if fhs := m.File["input_reference"]; len(fhs) > 0 {
			pic, err := readPart(fhs[0])
			if err != nil {
				return f, err
			}
			f.Start = &pic
		}
		if fhs := m.File["last_frame"]; len(fhs) > 0 {
			pic, err := readPart(fhs[0])
			if err != nil {
				return f, err
			}
			f.End = &pic
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
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return f, err
		}
		var in struct {
			Model           string          `json:"model"`
			Prompt          string          `json:"prompt"`
			Seconds         json.RawMessage `json:"seconds"`
			Size            string          `json:"size"`
			InputReference  json.RawMessage `json:"input_reference"`
			LastFrame       json.RawMessage `json:"last_frame"`
			ReferenceImages json.RawMessage `json:"reference_images"`
		}
		if err := json.Unmarshal(body, &in); err != nil {
			return f, fmt.Errorf("the request isn't JSON: %v", err)
		}
		f = filming{Model: in.Model, Prompt: in.Prompt, Size: in.Size, Seconds: strings.Trim(strings.TrimSpace(string(in.Seconds)), `"`)}
		var all map[string]any
		json.Unmarshal(body, &all)
		for k, v := range all {
			if !slices.Contains(filmingRead, k) {
				if f.Extra == nil {
					f.Extra = map[string]any{}
				}
				f.Extra[k] = v
			}
		}
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
		if srcs := imageSources(in.LastFrame); len(srcs) > 0 {
			pic, err := pictureOf(srcs[0])
			if err != nil {
				return f, err
			}
			f.End = &pic
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

// volcengineVideoBody is f as Ark's content-generation API takes it: text
// and images in content, each image's role saying what it is (the first
// frame, the last frame, a subject to draw from), duration, ratio and
// resolution as fields of their own, and the request's other fields, which
// OpenAI's API doesn't have, as the caller sent them: Ark's own
// (generate_audio, seed, camera_fixed, watermark…), which a caller of
// OpenAI's shape can only send that way.
func volcengineVideoBody(model string, f filming) []byte {
	content := []map[string]any{{"type": "text", "text": f.Prompt}}
	frame := func(pic *picture, role string) {
		content = append(content, map[string]any{"type": "image_url", "image_url": map[string]string{"url": pic.dataURL()}, "role": role})
	}
	if f.Start != nil {
		frame(f.Start, "first_frame")
	}
	if f.End != nil {
		frame(f.End, "last_frame")
	}
	for i := range f.References {
		frame(&f.References[i], "reference_image")
	}
	req := map[string]any{}
	for k, v := range f.Extra {
		// a multipart form's fields are text: "false" and "42" are what
		// Ark takes as false and 42
		if str, ok := v.(string); ok {
			var typed any
			if json.Unmarshal([]byte(str), &typed) == nil {
				switch typed.(type) {
				case bool, float64:
					v = typed
				}
			}
		}
		req[k] = v
	}
	req["model"], req["content"] = model, content
	if f.Seconds != "" {
		req["duration"], _ = strconv.Atoi(f.Seconds)
	}
	if ar := aspectAmong(f.Size, grokVideoAspects); ar != "" {
		req["ratio"] = ar
	}
	if res := pixelResolution(f.Size); res != "" {
		req["resolution"] = res
	}
	b, _ := json.Marshal(req)
	return b
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
	Content struct {
		VideoURL string `json:"video_url"`
	} `json:"content"`
	Duration float64 `json:"duration"`
	Error    struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// videoObject is a video as OpenAI's API gives it.
func videoObject(id, model string, started time.Time, st videoState, f filming) map[string]any {
	status, progress := "queued", 0
	switch st.Status {
	case "done", "succeeded":
		status, progress = "completed", 100
	case "running":
		status, progress = "in_progress", st.Progress
	case "failed", "expired", "cancelled":
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
	case st.Duration > 0:
		obj["seconds"] = strconv.FormatFloat(st.Duration, 'f', -1, 64)
	case f.Seconds != "":
		obj["seconds"] = f.Seconds
	}
	if status == "failed" {
		code, msg := st.Error.Code, st.Error.Message
		switch st.Status {
		case "expired":
			code, msg = "expired", "the vendor no longer keeps this video"
		case "cancelled":
			code, msg = "cancelled", "the video was cancelled"
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
	if ferr == nil && filmsMiniMax(*found) {
		// a MiniMax task is its key's account's: asked on that key
		q, ok := markedKey(*found, by)
		if !ok {
			return p, "", started, fmt.Errorf("this video was started with a key of %s that is no longer on: turn it back on to get the video", found.Name)
		}
		return q, vendorID, started, nil
	}
	if ferr == nil && !drawsGrok(*found) && !found.IsRemoteMagpie() && len(found.PlanVideos()) == 0 && len(customVideomakers(*found)) > 0 {
		// asked on the key that started it
		q, ok := markedKey(*found, by)
		if !ok {
			return p, "", started, fmt.Errorf("this video was started with a key of %s that is no longer on: turn it back on to get the video", found.Name)
		}
		return q, vendorID, started, nil
	}
	if ferr != nil || !drawsGrok(*found) && !found.IsRemoteMagpie() && len(found.PlanVideos()) == 0 {
		return p, "", started, fmt.Errorf("no provider %q makes videos here", pid)
	}
	if found.Account == nil && found.PlansMixed() {
		// asked on the key that started it, at its plan's endpoint
		q, ok := markedKey(*found, by)
		if !ok {
			return p, "", started, fmt.Errorf("this video was started with a key of %s that is no longer on: turn it back on to get the video", found.Name)
		}
		return q, vendorID, started, nil
	}
	if signer(*found) != by {
		return p, "", started, fmt.Errorf("this video was started with another %s account than the one signed in now: sign back in to it to get the video", found.Name)
	}
	return *found, vendorID, started, nil
}

// remoteVideoID is the id of a video another magpie (p) is making, by its
// id there: that has dots in it, so it goes in base64.
func remoteVideoID(p provider.Provider, theirs string, t time.Time) string {
	return videoID(p, base64.RawURLEncoding.EncodeToString([]byte(theirs)), t)
}

// theirVideoID is the other magpie's id of a video, from remoteVideoID's.
func theirVideoID(vendorID string) string {
	b, _ := base64.RawURLEncoding.DecodeString(vendorID)
	return string(b)
}

// remoteVideoBody is f as another magpie's videos API takes it: OpenAI's,
// the images as data URLs.
func remoteVideoBody(model string, f filming) []byte {
	req := map[string]any{}
	for k, v := range f.Extra {
		req[k] = v
	}
	req["model"], req["prompt"] = model, f.Prompt
	if f.Seconds != "" {
		req["seconds"] = f.Seconds
	}
	if f.Size != "" {
		req["size"] = f.Size
	}
	if f.Start != nil {
		req["input_reference"] = f.Start.dataURL()
	}
	if f.End != nil {
		req["last_frame"] = f.End.dataURL()
	}
	var refs []string
	for _, pic := range f.References {
		refs = append(refs, pic.dataURL())
	}
	if len(refs) > 0 {
		req["reference_images"] = refs
	}
	b, _ := json.Marshal(req)
	return b
}

// asOurs is another magpie's video object with this magpie's id for it and
// name for its model.
func asOurs(b []byte, p provider.Provider, id string) (map[string]any, error) {
	var obj map[string]any
	if err := json.Unmarshal(b, &obj); err != nil || obj == nil {
		return nil, fmt.Errorf("%s's answer isn't a video's: %v", p.Name, err)
	}
	obj["id"] = id
	if m, _ := obj["model"].(string); m != "" {
		obj["model"] = p.ID + "/" + m
	}
	return obj, nil
}

// filmsOpenAI is whether p makes videos at an OpenAI-shaped videos API:
// another magpie, or a provider whose model list marks video models.
func filmsOpenAI(p provider.Provider) bool {
	return p.IsRemoteMagpie() || len(customVideomakers(p)) > 0 && !isArk(p)
}

// remoteVideoURL is where another magpie, or a provider's OpenAI-shaped
// videos API, answers for a video, with suffix ("" or "/content").
func remoteVideoURL(p provider.Provider, vendorID, suffix string) string {
	return videoBase(p) + "/videos/" + url.PathEscape(theirVideoID(vendorID)) + suffix
}

// videoStatus asks the vendor how a video is going.
func (s *Server) videoStatus(ctx context.Context, p provider.Provider, vendorID string) (videoState, int, error) {
	if filmsMiniMax(p) {
		return s.minimaxVideoStatus(ctx, p, vendorID)
	}
	var st videoState
	at := strings.TrimRight(p.Base(provider.Responses), "/") + "/videos/" + url.PathEscape(vendorID)
	if p.Preset == "volcengine" || isArk(p) {
		at = arkTasks(p) + "/" + url.PathEscape(vendorID)
	}
	b, code, err := s.sendAs(ctx, p, http.MethodGet, at, "", nil, true)
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
	inputBody, admitted := s.requestBody(w, r, provider.Chat)
	if !admitted {
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(inputBody))
	defer func() {
		if r.MultipartForm != nil {
			r.MultipartForm.RemoveAll()
		}
	}()
	start := time.Now()
	f, err := readFilming(r)
	if err != nil {
		writeError(w, provider.Chat, 400, err.Error())
		return
	}
	who := callerOf(r)
	call := Call{Time: start, From: provider.Chat, Agent: who.agent, Via: who.via, Model: f.Model, headers: headerLogOf(r.Context())}
	usage.Saw(agentOf(r))
	fail := func(code int, msg string) {
		call.Status, call.Error, call.Millis = code, msg, time.Since(start).Milliseconds()
		writeError(w, provider.Chat, code, msg)
		s.record(call)
	}
	named := f.Model != "" // a gateway key's models hold one the caller names (#882)
	if f.Model == "" {
		m, ok := videomaker()
		if !ok {
			fail(400, "no model to make videos with: sign in to a Grok subscription or set up a provider that makes videos (a MiniMax API key makes them with MiniMax-H3), and leave Settings → Images → Image generation on, or name one")
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
	g, ms, isGroup := drawingGroup(f.Model, p, model)
	if isGroup {
		r = heldByGroup(r, g) // held to the group's accounts, if it is
	}
	keyWho, keyHeld := keyHolds(r)
	if keyHeld && named && (isGroup && !groupAllowed(keyWho, g, ms) || !isGroup && !modelAllowed(keyWho, p, model)) {
		fail(403, keyModelError(keyWho, f.Model))
		return
	}
	if msg, off := modelOff(p, model, f.Model); off { // disabled by the user (#1867)
		fail(403, msg)
		return
	}
	// the models to start the video on, the next asked while one fails: a
	// routing group's members in its routing's order, as a drawing's
	// (#2005), else the one
	tries := []provider.Member{{Provider: p, Model: model}}
	if isGroup {
		var paused []provider.Paused
		if ms, paused = provider.PausedOut(g.Live(), ms, agentOf(r), "", ruleClock()); len(ms) == 0 && len(paused) > 0 {
			fail(503, pausedError(f.Model, paused))
			return
		}
		var keeps map[string]bool
		if keyHeld {
			keeps = groupKeeps(keyWho, g, ms)
		}
		t := s.drawingTries(g, ms, keeps)
		if len(t) == 0 && keeps != nil {
			fail(403, keyModelError(keyWho, f.Model))
			return
		}
		if len(t) > 0 {
			tries = t
		}
	}
	call.To = provider.Chat
	var unmask func()
	prompt := f.Prompt
	var hits maskHits
	w, f.Prompt, unmask, hits, err = redactedPromptHits(r.Context(), w, f.Prompt)
	defer unmask()
	// the fields passed on as they came are masked as the prompt is: a
	// negative_prompt is the user's text all the same
	var extra maskHits
	if err == nil {
		err = maskStrings(r.Context(), f.Extra, &extra)
	}
	if err != nil { // fails closed
		fail(maskStatus(err), err.Error())
		return
	}
	n := extra.n
	hits.add(extra)
	sent := f.Prompt
	if b, e := json.Marshal(f.Extra); e == nil && extra.n > 0 {
		sent += " " + string(b)
	}
	// the record names the member that had it last (noteMaskedUntil)
	// the Routing page lists it as it does a chat, each member asked a
	// try of its one route (sideRoute); its polls and download are the
	// same video, and add none
	var grp *GroupRef
	if isGroup {
		grp = groupRef(g, ms)
	}
	side := s.beginSide(r, start, call.Model, p.ID, grp)
	masked, noted := noteMaskedUntil(r, "video", hits, sent, side.id(), cmp.Or(call.Provider, p.ID), call.Model)
	side.masked(masked)
	defer func() { noted(call.Provider, model) }()
	if n > 0 && f.Prompt == prompt { // the prompt's mask wraps w already
		var done func()
		w, done = restoring(w)
		defer done()
	}
	ctx, cancel := drawWithin(r.Context(), videoTimeout)
	defer cancel()
	var (
		v       videoStart
		code    int
		skipped []string
	)
	for i, m := range tries {
		model = m.Model
		at := time.Now()
		v, code, err = s.startVideo(ctx, r, m.Provider, m.Model, f)
		p, call.Provider = v.p, v.p.ID
		side.tryAt(candidateOf(v.p, m.Model), at)(code, err)
		// a group's next member is asked as a chat's would be: while this
		// one can't make videos, its vendor fails, is out of quota or
		// doesn't serve the model; a refusal of the request is the request's
		if err == nil || ctx.Err() != nil || i+1 == len(tries) || !v.unfit && !retryable(code, []byte(err.Error())) {
			break
		}
		skipped = append(skipped, m.Provider.ID+"/"+m.Model+": "+err.Error())
	}
	call.Fallback = strings.Join(skipped, "; ")
	if err != nil && !v.asked {
		// turned away before any vendor was asked: nothing was spent
		msg := err.Error()
		if len(skipped) > 0 {
			msg += " (tried first: " + call.Fallback + ")"
		}
		side.end(p, model, code, errors.New(msg), Usage{})
		fail(code, msg)
		return
	}
	b, remote, custom, field, onKey := v.answer, v.remote, v.custom, v.field, v.onKey
	call.Millis, call.Status = time.Since(start).Milliseconds(), code
	var started map[string]any
	theirs := ""
	if err == nil {
		json.Unmarshal(b, &started)
		if theirs, _ = started[field].(string); theirs == "" {
			code, err = 502, fmt.Errorf("%s started no video: its answer had no %s", p.Name, field)
			call.Status = code
		}
	}
	side.end(p, model, call.Status, err, Usage{})
	appendUsage(r, usage.Record{RouteID: side.id(), Operation: "generate_content", Time: start, Agent: call.Agent, Via: call.Via, Provider: p.ID, Host: p.Where(), Model: model, Requested: call.Model, ProviderAccount: accountOf(p),
		Millis: call.Millis, Status: call.Status, Session: sessionOf(r.Header)})
	if err != nil {
		call.Error = err.Error()
		s.record(call)
		msg := err.Error()
		if len(skipped) > 0 {
			msg += " (tried first: " + call.Fallback + ")"
		}
		writeError(w, provider.Chat, code, msg)
		return
	}
	s.record(call)
	if remote {
		obj, _ := asOurs(b, p, remoteVideoID(p, theirs, start))
		writeJSON(w, 200, obj)
		return
	}
	if custom {
		obj, _ := asOurs(b, p, customVideoID(p, theirs, start))
		if _, ok := obj["model"]; !ok {
			obj["model"] = p.ID + "/" + model
		}
		writeJSON(w, 200, obj)
		return
	}
	id := videoID(p, theirs, start)
	if onKey {
		id = strings.TrimSuffix(id, "."+signer(p)) + "." + keyMark(p)
	}
	writeJSON(w, 200, videoObject(id, p.ID+"/"+model, start, videoState{}, f))
}

// videoStart is how starting a video on one model went: the key or
// account it went out on, and where and how its answer is read.
type videoStart struct {
	p              provider.Provider
	answer         []byte
	field          string // the answer's field naming the vendor's video
	remote, custom bool
	asked          bool // a vendor was asked: what it answered is recorded
	unfit          bool // the model makes no videos: none was asked
	// onKey: started on one of keys on plans of their own, which its id
	// names (keyMark) to be asked after on, at that plan's endpoint
	onKey bool
}

// startVideo starts f's video on p's model: at another magpie's videos
// API, a provider's OpenAI-shaped one, Volcengine's tasks or Grok's. A
// failure before any vendor is asked (the model makes no videos, a key may
// use none of the provider's keys) has asked unset.
func (s *Server) startVideo(ctx context.Context, r *http.Request, p provider.Provider, model string, f filming) (videoStart, int, error) {
	// the provider's other keys to start the video on, when the first
	// says it doesn't serve the model
	var keys []candidate
	onKey := false
	if !p.IsRemoteMagpie() && filmsCustom(p, model) {
		// at a provider's own videos API: sent on the first key a chat
		// for the model would go to (#1657), never one whose own list of
		// models leaves it out, nor one the calling key may not use
		// (#905); the video's id names the key, which it is asked after on
		cs, code, msg := s.onKeys(r, p, model, f.Model)
		if code != 0 {
			return videoStart{p: p}, code, errors.New(msg)
		}
		p, keys = cs[0].p, cs[1:]
		// an Ark key's video is a task of that key's account: its id names
		// the key, as a plan's does, and it is asked after there
		onKey = isArk(p)
	} else if makesMiniMaxVideo(p, model) {
		// on the first key a chat for the model would go to that the
		// calling key may use, the next while one's plan doesn't make H3
		// (a Token Plan's); the video's id names the key it is asked on
		cs, code, msg := s.onKeys(r, p, model, f.Model)
		if code != 0 {
			return videoStart{p: p}, code, errors.New(msg)
		}
		p, keys = cs[0].p, cs[1:]
		onKey = true
	} else if !p.IsRemoteMagpie() && p.PlansMixed() {
		// keys on plans of their own (KeyAccount.Plan): sent on a key
		// whose plan makes the model, the Agent Plan's Seedance on the
		// Agent Plan's key, never a pay-as-you-go key beside it
		cs, code, msg := s.onKeys(r, p, model, f.Model)
		if code != 0 {
			return videoStart{p: p}, code, errors.New(msg)
		}
		p = cs[0].p
		onKey = true
	} else if keyWho, held := accountHolds(r); held {
		// held to some accounts (#905): sent on a key the calling key may
		// use, of those in use — not the provider's first alone, which a
		// key held to a later one was refused by — or none, refused
		var ok bool
		if p, ok = allowedKey(keyWho, p, model); !ok {
			return videoStart{p: p}, 403, errors.New(keyAccountsError(keyWho, f.Model))
		}
	}
	v := videoStart{p: p, remote: p.IsRemoteMagpie(), onKey: onKey}
	volcengine := arkFilms(p, model)
	v.custom = !v.remote && !volcengine && filmsCustom(p, model)
	minimax := !v.remote && !v.custom && makesMiniMaxVideo(p, model)
	// any grok-imagine-video*, not only those listed: the vendor's newer ones work before magpie names them
	if !v.remote && !v.custom && !volcengine && !minimax && (!drawsGrok(p) || !strings.HasPrefix(model, grokVideoFamily)) {
		v.unfit = true
		return v, 400, fmt.Errorf(`%s/%s can't make videos: magpie makes videos with a Grok subscription's grok-imagine-video, Volcengine Agent Plan's or an Ark key's Seedance models, a MiniMax API key's MiniMax-H3 models, another magpie's, and a provider's models its own model list marks "kind": "video" or that you mark a Video model (right-click the model in the provider's editor: Makes…)`, p.ID, model)
	}
	// another magpie is asked at its videos API, which says itself what
	// it can't make
	at, field := strings.TrimRight(p.Base(provider.Responses), "/")+"/videos/generations", "request_id"
	var body []byte
	switch {
	case v.remote, v.custom:
		// a provider's OpenAI-shaped videos API is asked as another
		// magpie's is: OpenAI's body, its answer its own
		at, field, body = videoBase(p)+"/videos", "id", remoteVideoBody(model, f)
	case volcengine:
		at, field, body = arkTasks(p), "id", volcengineVideoBody(model, f)
	case minimax:
		var err error
		if body, err = minimaxVideoBody(model, f); err != nil {
			return v, 400, err
		}
		at, field = minimaxHost(p)+"/v2/video_generation", "task_id"
	default:
		var err error
		if body, err = grokVideoBody(model, f); err != nil {
			return v, 400, err
		}
	}
	v.field, v.asked = field, true
	b, code, err := s.send(ctx, p, at, "application/json", body, true)
	// a key answering 404 that it doesn't serve the model hands it to the
	// next (#1657), as a drawing does; the id names the key that started it
	for ; err != nil && len(keys) > 0 && ctx.Err() == nil && (keyLacksModel(code, err) || minimax && minimaxLacksH3(b)); keys = keys[1:] {
		p = keys[0].p
		b, code, err = s.send(ctx, p, at, "application/json", body, true)
	}
	if minimax {
		if err != nil {
			err = minimaxError(code, b, err)
		} else if msg, mcode := minimaxSaid(b); msg != "" {
			// an answer of 200 that says it failed (base_resp)
			code, err = 502, minimaxError(0, b, fmt.Errorf("%s: %s%s", p.Name, msg, codeUnlessSaid(msg, mcode)))
		}
	}
	v.p, v.answer = p, b
	return v, code, err
}

// videoAsker is p on a key or account the calling key may use, to ask
// after a video (#905), as starting one is held: a video at the
// OpenAI-shaped videos API of a provider set up with a base URL, or at an
// Ark key's tasks, on the key that started it, which the calling key must
// be allowed; any other on the
// first key or account it may use, as videosCreate picks. What the calling
// key is told when it may use none, else "".
func videoAsker(r *http.Request, p provider.Provider) (provider.Provider, string) {
	who, held := accountHolds(r)
	if !held {
		return p, ""
	}
	if !p.IsRemoteMagpie() && (filmsOpenAI(p) || filmsMiniMax(p) || isArk(p) && len(customVideomakers(p)) > 0) {
		if p.Key == "" && p.Account == nil || accountAllowed(who, candidate{p: p}) {
			return p, ""
		}
		return p, keyAccountsError(who, p.ID)
	}
	q, ok := allowedKey(who, p, "")
	if !ok {
		return p, keyAccountsError(who, p.ID)
	}
	return q, ""
}

// videoModelHeld is what the calling key is told when its models hold it
// from a video of p's model (#882), as they hold it from starting one: ""
// when they don't. A video whose model the vendor doesn't say is held.
func videoModelHeld(r *http.Request, p provider.Provider, model string) string {
	who, held := keyHolds(r)
	if !held || model != "" && modelAllowed(who, p, model) {
		return ""
	}
	if model == "" {
		return fmt.Sprintf("The gateway key %q may not ask after this video: %s didn't say which model makes it.", who.KeyName, p.Name)
	}
	return keyModelError(who, p.ID+"/"+model)
}

// openAIVideo is how a video at an OpenAI-shaped videos API (another
// magpie's, a provider's) is going, with this magpie's id for it, and the
// model making it as that API names it.
func (s *Server) openAIVideo(ctx context.Context, p provider.Provider, vendorID, id string) (map[string]any, string, int, error) {
	b, code, err := s.sendAs(ctx, p, http.MethodGet, remoteVideoURL(p, vendorID, ""), "", nil, true)
	if err != nil {
		return nil, "", code, err
	}
	obj, err := asOurs(b, p, id)
	if err != nil {
		return nil, "", 502, err
	}
	model, _ := obj["model"].(string)
	return obj, strings.TrimPrefix(model, p.ID+"/"), 200, nil
}

// videosGet answers how a video is going.
func (s *Server) videosGet(w http.ResponseWriter, r *http.Request) {
	// A later poll can echo the prompt masked when the video was created.
	w, done := restoring(w)
	defer done()
	id := r.PathValue("id")
	p, vendorID, started, err := videoMaker(id)
	if err != nil {
		writeError(w, provider.Chat, 404, err.Error())
		return
	}
	p, held := videoAsker(r, p)
	if held != "" {
		writeError(w, provider.Chat, 403, held)
		return
	}
	ctx, cancel := drawWithin(r.Context(), videoTimeout)
	defer cancel()
	if filmsOpenAI(p) {
		obj, model, code, err := s.openAIVideo(ctx, p, vendorID, id)
		if err != nil {
			writeError(w, provider.Chat, code, err.Error())
			return
		}
		if held := videoModelHeld(r, p, model); held != "" {
			writeError(w, provider.Chat, 403, held)
			return
		}
		writeJSON(w, 200, obj)
		return
	}
	st, code, err := s.videoStatus(ctx, p, vendorID)
	if err != nil {
		writeError(w, provider.Chat, code, err.Error())
		return
	}
	model := st.Model
	if model == "" {
		model = "grok-imagine-video"
	}
	if held := videoModelHeld(r, p, model); held != "" {
		writeError(w, provider.Chat, 403, held)
		return
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
	p, held := videoAsker(r, p)
	if held != "" {
		writeError(w, provider.Chat, 403, held)
		return
	}
	ctx, cancel := drawWithin(r.Context(), videoTimeout)
	defer cancel()
	if filmsOpenAI(p) {
		if _, models := keyHolds(r); models {
			// the bytes say no model: the video's own answer does
			_, model, code, err := s.openAIVideo(ctx, p, vendorID, id)
			if err != nil {
				writeError(w, provider.Chat, code, err.Error())
				return
			}
			if held := videoModelHeld(r, p, model); held != "" {
				writeError(w, provider.Chat, 403, held)
				return
			}
		}
		s.remoteVideoContent(ctx, w, p, vendorID)
		return
	}
	st, code, err := s.videoStatus(ctx, p, vendorID)
	if err != nil {
		writeError(w, provider.Chat, code, err.Error())
		return
	}
	if held := videoModelHeld(r, p, cmp.Or(st.Model, "grok-imagine-video")); held != "" {
		writeError(w, provider.Chat, 403, held)
		return
	}
	if st.Status != "done" && st.Status != "succeeded" {
		obj := videoObject(id, "", time.Time{}, st, filming{})
		msg := fmt.Sprintf("the video isn't ready: it is %v", obj["status"])
		if e, ok := obj["error"].(map[string]string); ok {
			msg += ": " + e["message"]
		}
		writeError(w, provider.Chat, 409, msg)
		return
	}
	if st.Video.URL == "" {
		st.Video.URL = st.Content.VideoURL
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
	// the URL is in the vendor's answer, so it is fetched as one a caller
	// names is: public addresses, or the vendor's own host (vendorClient)
	res, err := vendorClient(p).Do(req)
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

// remoteVideoContent passes on the bytes of a video another magpie, or a
// provider's OpenAI-shaped videos API, made, or what it says of one it
// hasn't.
func (s *Server) remoteVideoContent(ctx context.Context, w http.ResponseWriter, p provider.Provider, vendorID string) {
	ctx = p.Via(ctx)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, remoteVideoURL(p, vendorID, "/content"), nil)
	if err != nil {
		writeError(w, provider.Chat, 502, err.Error())
		return
	}
	// magpie's, or the agent's own where the provider passes it on (#1851)
	stampUA(ctx, p, req.Header)
	if p.IsRemoteMagpie() {
		// vendors are told no caller
		req.Header.Set("User-Agent", "magpie/"+Version)
		passOnCaller(ctx, req)
	}
	if err := p.Sign(ctx, req, provider.Chat, nil); err != nil {
		writeError(w, provider.Chat, 502, err.Error())
		return
	}
	res, err := p.Do(s.client, req)
	if err != nil {
		writeError(w, provider.Chat, 502, fmt.Sprintf("the video couldn't be fetched: %v", err))
		return
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		said, ok := provider.Unsaid(b)
		if !ok {
			said = vendorMessage(b)
		}
		writeError(w, provider.Chat, res.StatusCode, p.Name+": "+said)
		return
	}
	w.Header().Set("Content-Type", cmp.Or(res.Header.Get("Content-Type"), "video/mp4"))
	if res.ContentLength > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(res.ContentLength, 10))
	}
	w.WriteHeader(200)
	io.Copy(w, io.LimitReader(res.Body, maxVideoBytes))
}

// maskStrings masks every string in v (a decoded JSON value) in place as a
// prompt is masked, adding what it swapped to h; an error when it can't
// (fail closed).
func maskStrings(ctx context.Context, v any, h *maskHits) error {
	one := func(str string) (string, error) {
		_, m, got, err := maskTextHits(ctx, str)
		h.add(got)
		return m, err
	}
	var err error
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			if str, ok := e.(string); ok {
				x[k], err = one(str)
			} else {
				err = maskStrings(ctx, e, h)
			}
			if err != nil {
				return err
			}
		}
	case []any:
		for i, e := range x {
			if str, ok := e.(string); ok {
				x[i], err = one(str)
			} else {
				err = maskStrings(ctx, e, h)
			}
			if err != nil {
				return err
			}
		}
	}
	return nil
}
