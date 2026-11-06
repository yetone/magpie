package gateway

import (
	"bytes"
	"cmp"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// magpie's image generation, the model Settings › Images › Image generation
// names, is also an MCP server the gateway serves itself, beside image
// recognition's (JonahFu on X): Magpie Image (internal/imagemcp) is a stdio
// server the Library gives an agent on this computer, which a gateway in
// Docker, or an agent on another computer, has none of. Streamable HTTP at
// ImageMCPPath, served and guarded as VisionMCPPath is (serveMCP), with
// Magpie Image's tools and arguments (ImageToolProps, VideoToolProps)
// but where to save: the gateway can't write into the agent's project, so
// an image comes back in the answer, as MCP image content, and at a URL
// the gateway keeps it at for an hour (ImageMCPFiles); a video at the
// gateway's own /v1/videos/{id}/content. What draws is the images API
// itself (s.images, s.videosCreate), called in-process with the MCP
// request's caller, so a gateway key's models, limits and usage hold it
// as they hold /v1/images.

// ImageMCPPath is where the image generation MCP server is served.
const ImageMCPPath = "/mcp/magpie/image"

// ImageMCPFiles is where the images it made are served from.
const ImageMCPFiles = ImageMCPPath + "/files/"

// Under these keys of a tool result's _meta the image server tells a
// client that saves what it made (magpie tool) what its text tells an
// agent: the id to wait on with generation_result, and each file made, at
// its URL, and whether it is in the answer itself.
const (
	PendingMeta = "usemagpie.ai/pending"
	FilesMeta   = "usemagpie.ai/files"
)

// MadeFile is one file a tool result's FilesMeta names.
type MadeFile struct {
	URL    string `json:"url"`
	Inline bool   `json:"inline,omitempty"`
}

// withMeta is res with v under its _meta's key k.
func withMeta(res map[string]any, k string, v any) map[string]any {
	res["_meta"] = map[string]any{k: v}
	return res
}

// The tools Magpie Image and the image MCP server have.
const (
	GenerateImageTool    = "generate_image"
	GenerateVideoTool    = "generate_video"
	GenerationResultTool = "generation_result"
)

// ImageToolProps is generate_image's arguments but where to save: images
// says how an image to draw from may be given.
func ImageToolProps(images string) map[string]any {
	return map[string]any{
		"prompt":           map[string]any{"type": "string", "description": "What to draw, or how to change the reference images."},
		"size":             map[string]any{"type": "string", "description": "WIDTHxHEIGHT (1024x1024, 1536x1024, 1024x1536) or an aspect ratio (16:9). Default: the model's."},
		"n":                map[string]any{"type": "integer", "minimum": 1, "maximum": 4, "description": "How many images (default 1)."},
		"reference_images": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Images to edit or draw from: " + images + "."},
		"quality":          map[string]any{"type": "string", "description": "low, medium, high, xhigh, max or auto, for models that take it (xhigh and max: GPT Image 2.5 and later)."},
		"complexity":       map[string]any{"type": "string", "description": "low, medium or high: how much detail GPT Image 2.6 and later put in (default high)."},
		"background":       map[string]any{"type": "string", "description": "transparent, opaque or auto, for models that take it."},
	}
}

// VideoToolProps is generate_video's arguments but where to save: image
// says how the image to animate may be given, images how those its
// subjects are drawn from may.
func VideoToolProps(image, images string) map[string]any {
	return map[string]any{
		"prompt":           map[string]any{"type": "string", "description": "What happens in the video: the subject, its motion, the camera."},
		"seconds":          map[string]any{"type": "integer", "minimum": 1, "maximum": 15, "description": "Length in whole seconds (1-15). Default: the model's (about 8)."},
		"size":             map[string]any{"type": "string", "description": "An aspect ratio (16:9, 9:16, 1:1, 4:3, 3:4, 3:2, 2:3) or WIDTHxHEIGHT (1280x720; its shorter side picks 480p, 720p or 1080p). Default: the model's."},
		"image":            map[string]any{"type": "string", "description": "An image to animate, the video's first frame: " + image + "."},
		"reference_images": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Images the video's subjects are drawn from: " + images + "."},
	}
}

// ResultToolProps is generation_result's arguments.
func ResultToolProps() map[string]any {
	return map[string]any{
		"id": map[string]any{"type": "string", "description": "The id " + GenerateImageTool + " or " + GenerateVideoTool + " answered with."},
	}
}

// imageMCPWithin is how long a call waits for its image or video before it
// answers with an id to wait on (generation_result), as Magpie Image's
// answerWithin: agents give a tool call as little as 30 s (#575).
var imageMCPWithin = 20 * time.Second

// imageMCPPoll is how often a call asks after its video.
var imageMCPPoll = 3 * time.Second

const (
	// imageMCPKeep is how long an image's file, and a job, is kept.
	imageMCPKeep = time.Hour
	// imageMCPFilesMost is how many bytes of images are kept at once; the
	// oldest go first.
	imageMCPFilesMost = 256 << 20
	// imageMCPInline is the largest image put in the answer itself.
	imageMCPInline = 5 << 20
	// imageMCPJobMost is how long a job may draw.
	imageMCPJobMost = 15 * time.Minute
)

const imageMCPFrom = "an http(s) URL or a data URL (data:image/png;base64,...); an absolute file path only for an agent on the computer magpie runs on"

// imageMCPTools are the server's tools as r's caller is given them: the
// models each generate tool may be asked for are those its key may use.
func imageMCPTools(r *http.Request) []any {
	ro := func(props map[string]any, video bool) map[string]any {
		_, shown, def := mediaModelsFor(r, video)
		props["model"] = mediaModelProp(video, shown, def)
		return map[string]any{"type": "object", "properties": props, "required": []string{"prompt"}}
	}
	return []any{
		map[string]any{
			"name":  GenerateImageTool,
			"title": "Generate image",
			"description": "Generate an image from a text prompt, or edit/combine images given as reference_images, with the image model set in Magpie (Settings → Images → Image generation) or the one model names. " +
				"The image comes back in the answer and at a URL Magpie keeps it at for an hour (the link opens without a header, in a browser too, until it expires); nothing is saved in your project, so save it there yourself if you need it. " +
				"When it is not ready within about 20 seconds, the answer is an id to wait on with " + GenerationResultTool + " instead. Write a detailed prompt: subject, style, composition, colours, any text to render exactly.",
			"inputSchema": ro(ImageToolProps(imageMCPFrom+"s"), false),
			"annotations": map[string]any{"readOnlyHint": false, "destructiveHint": false, "openWorldHint": true},
		},
		map[string]any{
			"name":  GenerateVideoTool,
			"title": "Generate video",
			"description": "Generate a short video from a text prompt, optionally starting from an image to animate, with a video model set up in Magpie (the one model names, else the default). It takes from ten seconds to a few minutes; " +
				"the answer is the URL to download the mp4 from (with the same Authorization header this server is called with), or, when it is not ready within about 20 seconds, an id to wait on with " + GenerationResultTool + ". " +
				"Nothing is saved in your project. Describe the subject, the motion and the camera: what moves, how, and what the shot is.",
			"inputSchema": ro(VideoToolProps(imageMCPFrom, imageMCPFrom+"s"), true),
			"annotations": map[string]any{"readOnlyHint": false, "destructiveHint": false, "openWorldHint": true},
		},
		map[string]any{
			"name":  GenerationResultTool,
			"title": "Generation result",
			"description": "Wait for an image or video that " + GenerateImageTool + " or " + GenerateVideoTool + " said is still being made, by the id it gave, and get it. " +
				"It answers within about " + strconv.Itoa(int(imageMCPWithin/time.Second)) + " seconds: if it is still not ready, call this again with the same id (don't generate it again).",
			"inputSchema": map[string]any{"type": "object", "properties": ResultToolProps(), "required": []string{"id"}},
			"annotations": map[string]any{"readOnlyHint": true, "openWorldHint": false},
		},
	}
}

// imageMCPStore is what the image server keeps between calls: the jobs
// still drawing or drawn within the hour, and the images they made.
type imageMCPStore struct {
	mu      sync.Mutex
	jobs    map[string]*imageJob
	files   map[string]*imageFile
	order   []string // files, oldest first
	bytes   int
	running sync.WaitGroup
	// secret signs each file's URL (imageFileToken): made at the first
	// file, never written anywhere, so a restart, which loses the files,
	// loses the URLs with them
	secret []byte
}

// imageJob is one generate_image call's drawing: it goes on after the call
// that started it has answered.
type imageJob struct {
	id, key string
	started time.Time
	done    chan struct{}
	result  map[string]any // the tool result, once done
	// use is what the call that started it used, which a
	// generation_result's row names too
	use *mcpUse
}

type imageFile struct {
	data      []byte
	mime, key string
	made      time.Time
}

func (s *Server) imageMCP(w http.ResponseWriter, r *http.Request) {
	serveMCP(w, r, "magpie's image generation", imageServerName, visionMCPBody, s.imageMCPCall)
}

// imageMCPCall answers one request: its result, or its JSON-RPC error.
func (s *Server) imageMCPCall(r *http.Request, m mcpMessage) (any, map[string]any) {
	switch m.Method {
	case "initialize":
		return map[string]any{
			"protocolVersion": mcpVersion(m),
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "magpie-image", "title": "Magpie Image", "version": Version},
			"instructions":    GenerateImageTool + " draws with the image model set in magpie (Settings → Images → Image generation) and " + GenerateVideoTool + " makes a short video; images come back in the answer and at a URL, videos at a URL, nothing is saved in your project.",
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": imageMCPTools(r)}, nil
	case MediaModelMethod:
		var p struct {
			Tool  string `json:"tool"`
			Model string `json:"model"`
		}
		json.Unmarshal(m.Params, &p)
		if p.Tool != GenerateImageTool && p.Tool != GenerateVideoTool {
			return nil, map[string]any{"code": -32602, "message": "no tool " + p.Tool + " takes a model"}
		}
		ref, err := mediaModelFor(r, p.Tool, p.Model)
		if err != nil {
			return nil, map[string]any{"code": -32602, "message": err.Error()}
		}
		return map[string]any{"model": ref}, nil
	case MCPCalledMethod:
		return imageMCPCalled(r, m.Params)
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(m.Params, &p); err != nil {
			return nil, map[string]any{"code": -32602, "message": "invalid params: " + err.Error()}
		}
		switch p.Name {
		case GenerateImageTool:
			return s.imageMCPDraw(r, p.Arguments), nil
		case GenerateVideoTool:
			return s.imageMCPFilm(r, p.Arguments), nil
		case GenerationResultTool:
			var a struct {
				ID string `json:"id"`
			}
			json.Unmarshal(p.Arguments, &a)
			return s.imageMCPResult(r, strings.TrimSpace(a.ID)), nil
		}
		return nil, map[string]any{"code": -32602, "message": "unknown tool " + p.Name + ": this server has " + GenerateImageTool + ", " + GenerateVideoTool + " and " + GenerationResultTool}
	}
	return nil, map[string]any{"code": -32601, "message": "method not found: " + m.Method}
}

// imageGenOff is the tools' answer while Settings turned image generation
// off, which turns video off with it.
func imageGenOff() (map[string]any, bool) {
	if settings.Load().ImageGen != "off" {
		return nil, false
	}
	return toolResult("Image generation is off in magpie: pick a model for it in Settings → Images → Image generation", true), true
}

// imageMCPDraw starts generate_image's drawing and answers with what it
// made, or, while it is still drawing after imageMCPWithin, with its id.
func (s *Server) imageMCPDraw(r *http.Request, raw json.RawMessage) map[string]any {
	if off, ok := imageGenOff(); ok {
		return off
	}
	var a struct {
		Prompt     string   `json:"prompt"`
		Model      string   `json:"model"`
		Size       string   `json:"size"`
		N          int      `json:"n"`
		References []string `json:"reference_images"`
		Quality    string   `json:"quality"`
		Complexity string   `json:"complexity"`
		Background string   `json:"background"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return toolResult("invalid arguments: "+err.Error(), true)
	}
	if strings.TrimSpace(a.Prompt) == "" {
		return toolResult(GenerateImageTool+" needs a prompt", true)
	}
	req := map[string]any{"prompt": a.Prompt, "n": max(1, min(a.N, maxDrawings)), "response_format": "b64_json"}
	// a model named goes to the images API named, so a gateway key's
	// models hold it there (#882)
	if strings.TrimSpace(a.Model) != "" {
		ref, err := mediaModelFor(r, GenerateImageTool, a.Model)
		if err != nil {
			return toolResult(err.Error(), true)
		}
		req["model"] = ref
	}
	for k, v := range map[string]string{"size": a.Size, "quality": a.Quality, "complexity": a.Complexity, "background": a.Background} {
		if v != "" {
			req[k] = v
		}
	}
	edit := len(a.References) > 0
	size := len(a.Prompt)
	if edit {
		var images []map[string]string
		for _, ref := range a.References {
			src, err := mcpImageSrc(r, strings.TrimSpace(ref), GenerateImageTool)
			if err != nil {
				return toolResult(err.Error(), true)
			}
			size += len(src)
			images = append(images, map[string]string{"image_url": src})
		}
		req["images"] = images
	}
	who := access.Caller(r.Context())
	release, err := reserveFor(who, int64(size), "", limitClock())
	if err != nil {
		return toolResult(err.Error(), true)
	}
	body, _ := json.Marshal(req)
	path := "/v1/images/generations"
	if edit {
		path = "/v1/images/edits"
	}
	base := imageMCPBase(r)
	// the drawing goes on when the call has answered: a job of its own,
	// with the call's caller (its key, its agent) but not its end
	in := inProcess(r, context.WithoutCancel(r.Context()), http.MethodPost, path, body)
	j := s.imageMCPStart(who.KeyID, mcpUseOf(r.Context()), func() map[string]any {
		defer release()
		ctx, cancel := context.WithTimeout(in.Context(), imageMCPJobMost)
		defer cancel()
		rec := &replyRecorder{header: http.Header{}}
		s.images(edit)(rec, in.WithContext(ctx))
		return s.imageMCPDrawn(rec, who.KeyID, base)
	})
	return s.imageMCPAwait(r, j)
}

// imageMCPDrawn is the tool result of the images API's answer: each image
// in it and at its URL.
func (s *Server) imageMCPDrawn(rec *replyRecorder, key, base string) map[string]any {
	var out struct {
		Model string `json:"model"`
		Data  []struct {
			B64     string `json:"b64_json"`
			URL     string `json:"url"`
			Mime    string `json:"mime_type"`
			Revised string `json:"revised_prompt"`
		} `json:"data"`
		Text string `json:"text"`
	}
	json.Unmarshal(rec.body.Bytes(), &out)
	if rec.code() != http.StatusOK {
		return toolResult("no image: "+imageMCPSaid(rec.body.Bytes()), true)
	}
	if len(out.Data) == 0 {
		return toolResult("no image came back", true)
	}
	var content []any
	var urls, lost, revised []string
	var files []MadeFile
	for i, d := range out.Data {
		var img []byte
		mt := d.Mime
		var err error
		switch {
		case d.B64 != "":
			if img, err = base64.StdEncoding.DecodeString(d.B64); err != nil {
				lost = append(lost, fmt.Sprintf("image %d came back broken: %v", i+1, err))
				continue
			}
		case d.URL != "":
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			img, mt, err = FetchImage(ctx, PublicClient(), d.URL)
			cancel()
			if err != nil {
				lost = append(lost, fmt.Sprintf("image %d was drawn but couldn't be downloaded (%v); the vendor keeps it at %s for a while: download it from there", i+1, err, d.URL))
				continue
			}
		default:
			continue
		}
		// what the bytes are, over what the vendor says
		if sniffed := http.DetectContentType(img); strings.HasPrefix(sniffed, "image/") {
			mt = sniffed
		}
		if !strings.HasPrefix(mt, "image/") {
			mt = "image/png"
		}
		urls = append(urls, base+ImageMCPFiles+s.imageMCPKeepFile(img, mt, key))
		inline := len(img) <= imageMCPInline
		if inline {
			content = append(content, map[string]any{"type": "image", "data": base64.StdEncoding.EncodeToString(img), "mimeType": mt})
		}
		files = append(files, MadeFile{URL: urls[len(urls)-1], Inline: inline})
		if d.Revised != "" {
			revised = append(revised, d.Revised)
		}
	}
	if len(urls) == 0 {
		return toolResult(strings.Join(lost, "\n"), true)
	}
	var b strings.Builder
	what := "The image is"
	if len(urls) > 1 {
		what = "The images are"
	}
	fmt.Fprintf(&b, "Generated with %s. %s in this answer and can be downloaded for an hour from (the link opens without a header, in a browser too, until it expires; whoever has it can open the image):\n", out.Model, what)
	for _, u := range urls {
		fmt.Fprintf(&b, "- %s\n", u)
	}
	if len(content) < len(urls) {
		fmt.Fprintf(&b, "Images over %d MB are only at their URL.\n", imageMCPInline>>20)
	}
	for _, l := range lost {
		fmt.Fprintf(&b, "Not saved: %s\n", l)
	}
	if len(revised) > 0 {
		fmt.Fprintf(&b, "The model drew from this prompt: %s\n", revised[0])
	}
	if t := strings.TrimSpace(out.Text); t != "" {
		fmt.Fprintf(&b, "The model said: %s\n", t)
	}
	content = append([]any{map[string]any{"type": "text", "text": b.String()}}, content...)
	return withMeta(map[string]any{"content": content, "isError": false}, FilesMeta, files)
}

// imageMCPFilm starts generate_video's video and answers with where to
// download it, or, while it is still being made after imageMCPWithin,
// with its id.
func (s *Server) imageMCPFilm(r *http.Request, raw json.RawMessage) map[string]any {
	if off, ok := imageGenOff(); ok {
		return off
	}
	var a struct {
		Prompt     string   `json:"prompt"`
		Model      string   `json:"model"`
		Seconds    any      `json:"seconds"`
		Size       string   `json:"size"`
		Image      string   `json:"image"`
		References []string `json:"reference_images"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return toolResult("invalid arguments: "+err.Error(), true)
	}
	if strings.TrimSpace(a.Prompt) == "" {
		return toolResult(GenerateVideoTool+" needs a prompt", true)
	}
	req := map[string]any{"prompt": a.Prompt}
	if strings.TrimSpace(a.Model) != "" {
		ref, err := mediaModelFor(r, GenerateVideoTool, a.Model)
		if err != nil {
			return toolResult(err.Error(), true)
		}
		req["model"] = ref
	}
	switch v := a.Seconds.(type) {
	case nil:
	case float64:
		if v != float64(int(v)) || v < 1 || v > 15 {
			return toolResult("seconds is a whole number from 1 to 15", true)
		}
		req["seconds"] = strconv.Itoa(int(v))
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || n < 1 || n > 15 {
			return toolResult("seconds is a whole number from 1 to 15", true)
		}
		req["seconds"] = strconv.Itoa(n)
	default:
		return toolResult("seconds is a whole number from 1 to 15", true)
	}
	if a.Size != "" {
		req["size"] = a.Size
	}
	size := len(a.Prompt)
	if a.Image != "" {
		src, err := mcpImageSrc(r, strings.TrimSpace(a.Image), GenerateVideoTool)
		if err != nil {
			return toolResult(err.Error(), true)
		}
		size += len(src)
		req["input_reference"] = map[string]string{"image_url": src}
	}
	if len(a.References) > 0 {
		var refs []map[string]string
		for _, ref := range a.References {
			src, err := mcpImageSrc(r, strings.TrimSpace(ref), GenerateVideoTool)
			if err != nil {
				return toolResult(err.Error(), true)
			}
			size += len(src)
			refs = append(refs, map[string]string{"image_url": src})
		}
		req["reference_images"] = refs
	}
	release, err := reserveFor(access.Caller(r.Context()), int64(size), "", limitClock())
	if err != nil {
		return toolResult(err.Error(), true)
	}
	body, _ := json.Marshal(req)
	rec := &replyRecorder{header: http.Header{}}
	s.videosCreate(rec, inProcess(r, r.Context(), http.MethodPost, "/v1/videos", body))
	release()
	if rec.code() != http.StatusOK {
		return toolResult("no video: "+imageMCPSaid(rec.body.Bytes()), true)
	}
	var v struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(rec.body.Bytes(), &v) != nil || v.ID == "" {
		return toolResult("no video: the answer has no id: "+imageMCPSaid(rec.body.Bytes()), true)
	}
	return s.imageMCPVideo(r, v.ID)
}

// imageMCPVideo asks after video id until it is made, fails, or
// imageMCPWithin has passed.
func (s *Server) imageMCPVideo(r *http.Request, id string) map[string]any {
	deadline := time.Now().Add(imageMCPWithin)
	started := time.Now()
	for {
		in := inProcess(r, r.Context(), http.MethodGet, "/v1/videos/"+id, nil)
		in.SetPathValue("id", id)
		rec := &replyRecorder{header: http.Header{}}
		s.videosGet(rec, in)
		if rec.code() != http.StatusOK {
			return toolResult(fmt.Sprintf("asking after the video (%s): %s", id, imageMCPSaid(rec.body.Bytes())), true)
		}
		var v struct {
			Status  string `json:"status"`
			Model   string `json:"model"`
			Seconds string `json:"seconds"`
			Error   *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		json.Unmarshal(rec.body.Bytes(), &v)
		// the video's model, provider/model, for a call that only asks after it
		mcpUseOf(r.Context()).usedRef(v.Model)
		switch v.Status {
		case "completed":
			length := ""
			if v.Seconds != "" {
				length = v.Seconds + "-second "
			}
			at := imageMCPBase(r) + "/v1/videos/" + id + "/content"
			return withMeta(toolResult(fmt.Sprintf("Generated a %svideo with %s. Download the mp4 from (with the same Authorization header as this server, from another computer):\n- %s\n", length, v.Model, at), false), FilesMeta, []MadeFile{{URL: at}})
		case "failed":
			msg := "it gave no reason"
			if v.Error != nil && v.Error.Message != "" {
				msg = v.Error.Message
			}
			return toolResult("the video failed: "+msg, true)
		}
		if !time.Now().Add(imageMCPPoll).Before(deadline) {
			return withMeta(toolResult(fmt.Sprintf("The video is still being made (%s, %d s so far; it can take a few minutes). It goes on without this call. "+
				"Don't ask for it again: call %s with id %q to wait for it and get where to download it.\n", cmp.Or(v.Status, "queued"), int(time.Since(started)/time.Second), GenerationResultTool, id), false), PendingMeta, id)
		}
		select {
		case <-r.Context().Done():
			return toolResult("the call ended before the video was made: call "+GenerationResultTool+" with id "+strconv.Quote(id), true)
		case <-time.After(imageMCPPoll):
		}
	}
}

// imageMCPResult is generation_result's answer for id: a video's, by its
// own id, or a drawing's, started by the same key.
func (s *Server) imageMCPResult(r *http.Request, id string) map[string]any {
	if id == "" {
		return toolResult(GenerationResultTool+" needs the id "+GenerateImageTool+" or "+GenerateVideoTool+" answered with", true)
	}
	if strings.HasPrefix(id, "video_") {
		return s.imageMCPVideo(r, id)
	}
	st := &s.imageStore
	st.mu.Lock()
	j := st.jobs[id]
	st.mu.Unlock()
	if j == nil || j.key != access.Caller(r.Context()).KeyID {
		return toolResult(fmt.Sprintf("no image is being made as %q (magpie may have been restarted, or it was made over an hour ago): generate it again", id), true)
	}
	return s.imageMCPAwait(r, j)
}

// imageMCPStart runs draw as a job started by key, in a call that is use.
func (s *Server) imageMCPStart(key string, use *mcpUse, draw func() map[string]any) *imageJob {
	st := &s.imageStore
	j := &imageJob{id: "img_" + randomHex(12), key: key, started: time.Now(), done: make(chan struct{}), use: use}
	st.mu.Lock()
	if st.jobs == nil {
		st.jobs = map[string]*imageJob{}
	}
	for id, old := range st.jobs {
		select {
		case <-old.done:
			if time.Since(old.started) > imageMCPKeep {
				delete(st.jobs, id)
			}
		default:
		}
	}
	st.jobs[j.id] = j
	st.running.Add(1)
	st.mu.Unlock()
	go func() {
		defer st.running.Done()
		defer close(j.done)
		j.result = draw()
	}()
	return j
}

// imageMCPAwait waits up to imageMCPWithin for j.
func (s *Server) imageMCPAwait(r *http.Request, j *imageJob) map[string]any {
	limit := time.NewTimer(imageMCPWithin)
	defer limit.Stop()
	select {
	case <-j.done:
		mcpUseOf(r.Context()).from(j.use)
		return j.result
	case <-limit.C:
	case <-r.Context().Done():
	}
	return withMeta(toolResult(fmt.Sprintf("The image is still being made (%d s so far; it can take a few minutes). It goes on without this call. "+
		"Don't ask for it again: call %s with id %q to wait for it and get it.\n", int(time.Since(j.started)/time.Second), GenerationResultTool, j.id), false), PendingMeta, j.id)
}

// imageMCPKeepFile keeps img for an hour, for key, and is its file's name
// with the token that opens it (?t=, imageFileToken).
func (s *Server) imageMCPKeepFile(img []byte, mt, key string) string {
	st := &s.imageStore
	name := randomHex(16) + extOf(mt)
	made := time.Now()
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.files == nil {
		st.files = map[string]*imageFile{}
	}
	if st.secret == nil {
		st.secret = make([]byte, 32)
		rand.Read(st.secret)
	}
	st.files[name] = &imageFile{data: img, mime: mt, key: key, made: made}
	at := name + "?" + imageTokenParam + "=" + imageFileToken(st.secret, name, key, made.Add(imageMCPKeep).Unix())
	st.order = append(st.order, name)
	st.bytes += len(img)
	for len(st.order) > 1 && (st.bytes > imageMCPFilesMost || time.Since(st.files[st.order[0]].made) > imageMCPKeep) {
		old := st.order[0]
		st.order = st.order[1:]
		st.bytes -= len(st.files[old].data)
		delete(st.files, old)
	}
	return at
}

// imageTokenParam is the query parameter of a file's URL that holds its
// token (imageFileToken).
const imageTokenParam = "t"

// imageFileToken is what opens the file name, made for key, until exp
// (Unix seconds), without a header (dhanyqaz on Discord: a model hands a
// person the URL, and a browser sends no key): "<exp>.<HMAC-SHA256 of
// name, exp and key under the store's secret>". It names neither the key
// nor its id, only proves the gateway handed the URL out; a token for one
// file, key or expiry is no other's.
func imageFileToken(secret []byte, name, key string, exp int64) string {
	m := hmac.New(sha256.New, secret)
	fmt.Fprintf(m, "magpie image file\x00%s\x00%d\x00%s", name, exp, key)
	return strconv.FormatInt(exp, 10) + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// imageTokenAsked: r is a GET or HEAD of one of the image server's files
// with a token and no key, which lanGuard lets through to imageMCPFile
// for it to judge the token; anything else is judged as every request is.
func imageTokenAsked(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead || r.URL.RawPath != "" {
		return false
	}
	name, ok := strings.CutPrefix(r.URL.Path, ImageMCPFiles)
	return ok && name != "" && !strings.Contains(name, "/") && r.URL.Query().Get(imageTokenParam) != "" && len(callerKeys(r)) == 0
}

// tokenOpens: r's token is the one the gateway made for f, named name,
// and it hasn't expired; under st.mu.
func (st *imageMCPStore) tokenOpens(r *http.Request, name string, f *imageFile) bool {
	t := r.URL.Query().Get(imageTokenParam)
	exp, _, ok := strings.Cut(t, ".")
	if !ok || st.secret == nil {
		return false
	}
	n, err := strconv.ParseInt(exp, 10, 64)
	if err != nil || time.Now().Unix() > n {
		return false
	}
	return hmac.Equal([]byte(t), []byte(imageFileToken(st.secret, name, f.key, n)))
}

// imageMCPFile serves an image the server made: to the key that had it
// made, guarded as the server is, or to whoever has the URL it was handed
// out at, with its token, within the hour. The token is the only thing
// that lets a request without that key in: the allow list, sharing and a
// web page's Origin are judged as for every request.
func (s *Server) imageMCPFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "GET an image here", http.StatusMethodNotAllowed)
		return
	}
	name := r.PathValue("name")
	st := &s.imageStore
	st.mu.Lock()
	f := st.files[name]
	if f != nil && time.Since(f.made) > imageMCPKeep {
		f = nil
	}
	tokened := f != nil && st.tokenOpens(r, name, f)
	st.mu.Unlock()
	// the key that had it made is still on: revoking a key takes its
	// links with it (read outside the store's lock: it reads a file)
	tokened = tokened && (f.key == "" || access.On(f.key))
	if !local(r) && !sharedWith(r) && !(tokened && remoteKeyed()) {
		if remoteKeyed() && len(callerKeys(r)) == 0 {
			// lanGuard let a token through that opens nothing: refused
			// as a request without a key is
			writeError(w, provider.Chat, http.StatusUnauthorized, refusedKey(""))
			return
		}
		http.Error(w, "magpie's images are served to another machine only when magpie is shared on the local network and the request carries a gateway key (Authorization: Bearer <key>) or the link's own token", http.StatusForbidden)
		return
	}
	if o := r.Header.Get("Origin"); o != "" && !corsAllowed(o) {
		http.Error(w, "magpie's images are served to agents, not web pages", http.StatusForbidden)
		return
	}
	// another key's image is as good as none: its name says nothing
	if f == nil || !tokened && f.key != access.Caller(r.Context()).KeyID {
		http.Error(w, "no such image: images are kept for an hour, and only for the key that had them made or the link they were handed out at", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", f.mime)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("Content-Length", strconv.Itoa(len(f.data)))
	if r.Method == http.MethodHead {
		return
	}
	w.Write(f.data)
}

// imageMCPBase is the address the agent reaches the gateway at, for the
// URLs it is given.
func imageMCPBase(r *http.Request) string {
	if u := publicURL(); u != "" {
		return u
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// inProcess is r, an MCP request, as a request of the gateway's own API at
// path with body, carrying r's caller (its key, its agent) under ctx.
func inProcess(r *http.Request, ctx context.Context, method, path string, body []byte) *http.Request {
	in := r.Clone(ctx)
	in.Method = method
	u := *r.URL
	u.Path, u.RawPath, u.RawQuery = path, "", ""
	in.URL = &u
	in.RequestURI = path
	in.Body, in.ContentLength = http.NoBody, 0
	if body != nil {
		in.Body, in.ContentLength = io.NopCloser(bytes.NewReader(body)), int64(len(body))
		in.Header.Set("Content-Type", "application/json")
	} else {
		in.Header.Del("Content-Type")
	}
	in.Header.Del("Content-Encoding")
	in.Header.Del("Content-Length")
	// a reply that comes back compressed couldn't be read here
	in.Header.Del("Accept-Encoding")
	in.Header.Del("Mcp-Session-Id")
	return in
}

// replyRecorder takes what one of the gateway's handlers answers.
type replyRecorder struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (w *replyRecorder) Header() http.Header { return w.header }

func (w *replyRecorder) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
}

func (w *replyRecorder) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.body.Len()+len(b) > 512<<20 {
		return 0, errors.New("the reply is too large")
	}
	return w.body.Write(b)
}

func (w *replyRecorder) code() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

// imageMCPSaid is the message of an error the gateway answered with, else the
// answer itself.
func imageMCPSaid(data []byte) string {
	var e struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &e) == nil && e.Error != nil && e.Error.Message != "" {
		return e.Error.Message
	}
	s := strings.TrimSpace(string(data))
	if len(s) > 500 {
		s = s[:500] + "…"
	}
	return s
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}
