package gateway

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

// The gateway draws: /v1/images/generations and /v1/images/edits take
// OpenAI's images request and answer in its shape, whichever vendor draws.
// A model on an images API (gpt-image, dall-e, imagen, flux, seedream,
// qwen-image…) is asked there; one that answers in chat with pictures
// (gemini-*-image, gpt-5-image, anything on OpenRouter) is asked on chat
// completions with modalities; Gemini at Google is asked on its own
// generateContent. A request that names no model draws with the Settings'
// image generation model, or one magpie picks (AutoDrawer).

// DrawAgent is the User-Agent magpie's generate_image tool adds to its own.
const DrawAgent = "magpie-image/1"

const drawTimeout = 5 * time.Minute

// maxDrawings is how many images one request may ask for.
const maxDrawings = 4

// drawer is the model a request that names none draws with: the Settings'
// ImageGen while it resolves, else AutoDrawer's. None when it is off or no
// provider draws.
func drawer() (string, bool) {
	switch v := settings.Load().ImageGen; v {
	case "off":
		return "", false
	case "":
	default:
		if _, _, ok := provider.Resolve(v); ok {
			return v, true
		}
	}
	m := AutoDrawer()
	return m, m != ""
}

// codexDrawers are the image models a ChatGPT account draws with, at
// its Codex backend's images API, as Codex CLI does (gpt-image-2 is the one
// it asks for).
var codexDrawers = []catalog.Model{
	{ID: "gpt-image-2", Name: "GPT Image 2", Released: "2026-01-01"},
	{ID: "gpt-image-2.5", Name: "GPT Image 2.5", Released: "2026-06-01"},
}

// grokDrawers are the image models a Grok subscription (SuperGrok, X Premium+)
// draws with, at the Imagine API of the backend Grok Build's image_gen tool
// calls: cli-chat-proxy.grok.com/v1/images/generations.
var grokDrawers = []catalog.Model{
	{ID: "grok-imagine-image", Name: "Grok Imagine"},
	{ID: "grok-imagine-image-quality", Name: "Grok Imagine Quality"},
}

// drawsGrok is whether p is a Grok subscription, which draws at the Imagine
// API of the backend Grok Build talks to: the built-in's, or the Grok
// plugin's, which signs what it is sent there as the built-in does.
func drawsGrok(p provider.Provider) bool {
	return p.Account != nil && (p.Account.Agent == "grok" || p.PluginProvider() == "grok") && p.Base(provider.Responses) != ""
}

// drawsCodex is whether p is a ChatGPT account, which draws at its Codex
// backend's images API.
func drawsCodex(p provider.Provider) bool {
	return p.Account != nil && p.Account.Agent == "codex" && p.Base(provider.Responses) != ""
}

// Drawers are the models a provider can draw with: its catalogs' models
// that make images, those its own model list names that do, and those of
// its own picks named for images; a ChatGPT account's GPT Image, a Grok
// subscription's Grok Imagine.
func Drawers(p provider.Provider) []catalog.Model {
	if drawsCodex(p) || drawsGrok(p) {
		out := slices.Clone(codexDrawers)
		if drawsGrok(p) {
			out = slices.Clone(grokDrawers)
		}
		for i := range out {
			out[i].Provider = p.ID
		}
		return out
	}
	// any other subscription is asked through its agent's own API, which
	// draws nothing magpie can ask for yet
	if p.Account != nil || p.Base(provider.Chat) == "" {
		return nil
	}
	var out []catalog.Model
	have := map[string]bool{}
	for _, c := range p.Catalogs() {
		for _, m := range catalog.Drawers(c) {
			if !have[m.ID] {
				have[m.ID] = true
				out = append(out, m)
			}
		}
	}
	for _, m := range catalog.LiveDrawers(p.ID) {
		if !have[m.ID] {
			have[m.ID] = true
			m.Provider = p.ID
			out = append(out, m)
		}
	}
	for _, id := range p.Models {
		if !have[id] && catalog.DrawsID(id) {
			have[id] = true
			out = append(out, catalog.Model{ID: id, Provider: p.ID})
		}
	}
	return out
}

// AutoDrawer is the model magpie draws with when the Settings name none:
// a ChatGPT account's, which its plan pays for, else the cheapest of the
// models the API keys set up can draw with, the newest of those whose price
// isn't known. "" when none can.
func AutoDrawer() string {
	best, bestCost, bestDate := "", 0.0, ""
	for _, p := range provider.All() {
		if !p.On() || p.Decides() {
			continue
		}
		for _, m := range Drawers(p) {
			cost := 1e9
			switch {
			case drawsCodex(p), drawsGrok(p):
				cost = 0 // a ChatGPT or Grok plan's images come with it
			case m.Price != nil:
				cost = m.Price.Input + m.Price.Output
			}
			if best == "" || cost < bestCost || cost == bestCost && m.Released > bestDate {
				best, bestCost, bestDate = p.ID+"/"+m.ID, cost, m.Released
			}
		}
	}
	return best
}

// picture is an image sent along to draw from, or one drawn.
type picture struct {
	Mime string
	Data []byte
	URL  string // an image the vendor keeps, when it gave no bytes
}

func (p picture) dataURL() string {
	if p.URL != "" {
		return p.URL
	}
	return "data:" + p.Mime + ";base64," + base64.StdEncoding.EncodeToString(p.Data)
}

// drawing is one images request, whichever shape it came in.
type drawing struct {
	Model, Prompt, Size, Quality, Background, Format string
	N                                                int
	Images                                           []picture // to edit, or draw from
	Mask                                             *picture
}

// drawn is what a vendor gave back.
type drawn struct {
	Images  []picture
	Text    string // what a chat model said besides
	Revised string // the prompt as an images API rewrote it
	Input   int
	Output  int
}

func (s *Server) images(edit bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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
		d, err := readDrawing(r)
		if err != nil {
			writeError(w, provider.Chat, 400, err.Error())
			return
		}
		if edit && len(d.Images) == 0 {
			writeError(w, provider.Chat, 400, "an edit needs the image to edit")
			return
		}
		who := callerOf(r)
		call := Call{Time: start, From: provider.Chat, Agent: who.agent, Via: who.via, Model: d.Model}
		usage.Saw(agentOf(r))
		fail := func(code int, msg string) {
			call.Status, call.Error, call.Millis = code, msg, time.Since(start).Milliseconds()
			writeError(w, provider.Chat, code, msg)
			s.record(call)
		}
		if d.Model == "" {
			m, ok := drawer()
			if !ok {
				fail(400, "no model to draw with: pick one in magpie's Settings → Images → Image generation, or name one")
				return
			}
			d.Model, call.Model = m, m
		}
		p, model, ok := provider.Resolve(d.Model)
		if !ok {
			if off, isOff := provider.SwitchedOff(d.Model); isOff {
				fail(404, switchedOff(off, d.Model))
				return
			}
			fail(404, fmt.Sprintf("magpie knows no model %q to draw with", d.Model))
			return
		}
		call.Provider, call.To = p.ID, provider.Chat
		ctx, cancel := context.WithTimeout(r.Context(), drawTimeout)
		defer cancel()
		out, code, err := s.draw(ctx, p, model, d)
		call.Millis = time.Since(start).Milliseconds()
		call.Status, call.Usage.Input, call.Usage.Output = code, out.Input, out.Output
		if err == nil && len(out.Images) == 0 {
			code, err = 502, errors.New(model+" drew nothing"+vendorSaid(out.Text))
			call.Status = code
		}
		providerKeyID, providerKeyName := "", ""
		if p.Account == nil && p.Key != "" {
			providerKeyID, providerKeyName = provider.KeyID(p.Key), p.KeyName
		}
		appendUsage(r, usage.Record{Operation: "generate_content", Time: start, Agent: call.Agent, Via: call.Via, Provider: p.ID, Host: p.Where(), Model: model, Requested: call.Model, ProviderKeyID: providerKeyID, ProviderKeyName: providerKeyName, ProviderAccount: accountOf(p),
			Input: out.Input, Output: out.Output, Millis: call.Millis, Status: call.Status, Session: sessionOf(r.Header)})
		if err != nil {
			call.Error = err.Error()
			s.record(call)
			writeError(w, provider.Chat, code, err.Error())
			return
		}
		s.record(call)
		data := make([]map[string]any, 0, len(out.Images))
		for _, im := range out.Images {
			e := map[string]any{}
			if im.URL != "" {
				e["url"] = im.URL
			} else {
				e["b64_json"] = base64.StdEncoding.EncodeToString(im.Data)
				e["mime_type"] = im.Mime
			}
			if out.Revised != "" {
				e["revised_prompt"] = out.Revised
			}
			data = append(data, e)
		}
		res := map[string]any{"created": start.Unix(), "model": p.ID + "/" + model, "data": data,
			"usage": map[string]int{"input_tokens": out.Input, "output_tokens": out.Output, "total_tokens": out.Input + out.Output}}
		if out.Text != "" {
			res["text"] = out.Text
		}
		writeJSON(w, 200, res)
	}
}

func vendorSaid(text string) string {
	if text = strings.TrimSpace(text); text == "" {
		return ""
	}
	if len(text) > 300 {
		text = text[:300] + "…"
	}
	return ": " + text
}

// readDrawing reads an images request: JSON, or the multipart form OpenAI's
// edits take.
func readDrawing(r *http.Request) (drawing, error) {
	var d drawing
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if ct == "multipart/form-data" {
		if err := r.ParseMultipartForm(64 << 20); err != nil {
			return d, err
		}
		f := r.MultipartForm
		get := func(k string) string {
			if v := f.Value[k]; len(v) > 0 {
				return strings.TrimSpace(v[0])
			}
			return ""
		}
		d.Model, d.Prompt, d.Size, d.Quality, d.Background, d.Format = get("model"), get("prompt"), get("size"), get("quality"), get("background"), get("output_format")
		d.N, _ = strconv.Atoi(get("n"))
		for _, k := range []string{"image", "image[]"} {
			for _, fh := range f.File[k] {
				pic, err := readPart(fh)
				if err != nil {
					return d, err
				}
				d.Images = append(d.Images, pic)
			}
		}
		if fhs := f.File["mask"]; len(fhs) > 0 {
			pic, err := readPart(fhs[0])
			if err != nil {
				return d, err
			}
			d.Mask = &pic
		}
	} else {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return d, err
		}
		var in struct {
			Model          string          `json:"model"`
			Prompt         string          `json:"prompt"`
			Size           string          `json:"size"`
			Quality        string          `json:"quality"`
			Background     string          `json:"background"`
			Format         string          `json:"output_format"`
			N              int             `json:"n"`
			Image          json.RawMessage `json:"image"`
			Images         json.RawMessage `json:"images"`
			Mask           json.RawMessage `json:"mask"`
			ResponseFormat string          `json:"response_format"`
		}
		if err := json.Unmarshal(body, &in); err != nil {
			return d, fmt.Errorf("the request isn't JSON: %v", err)
		}
		d = drawing{Model: in.Model, Prompt: in.Prompt, Size: in.Size, Quality: in.Quality, Background: in.Background, Format: in.Format, N: in.N}
		for _, raw := range []json.RawMessage{in.Image, in.Images} {
			for _, src := range imageSources(raw) {
				pic, err := pictureOf(src)
				if err != nil {
					return d, err
				}
				d.Images = append(d.Images, pic)
			}
		}
		if srcs := imageSources(in.Mask); len(srcs) > 0 {
			pic, err := pictureOf(srcs[0])
			if err != nil {
				return d, err
			}
			d.Mask = &pic
		}
	}
	d.Model = strings.TrimSpace(d.Model)
	if strings.TrimSpace(d.Prompt) == "" {
		return d, errors.New("say what to draw in prompt")
	}
	if d.N <= 0 {
		d.N = 1
	}
	if d.N > maxDrawings {
		return d, fmt.Errorf("n is at most %d", maxDrawings)
	}
	return d, nil
}

// imageSources are the images a JSON field gives: a URL, {"image_url": …},
// or a list of either.
func imageSources(raw json.RawMessage) []string {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return []string{one}
	}
	var obj struct {
		ImageURL json.RawMessage `json:"image_url"`
		URL      string          `json:"url"`
	}
	if json.Unmarshal(raw, &obj) == nil && (len(obj.ImageURL) > 0 || obj.URL != "") {
		if obj.URL != "" {
			return []string{obj.URL}
		}
		return imageSources(obj.ImageURL)
	}
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) == nil {
		var out []string
		for _, e := range list {
			out = append(out, imageSources(e)...)
		}
		return out
	}
	return nil
}

// pictureOf is the image at a data: URL, or an http one left for the vendor
// to fetch.
func pictureOf(src string) (picture, error) {
	src = strings.TrimSpace(src)
	if rest, ok := strings.CutPrefix(src, "data:"); ok {
		meta, data, ok := strings.Cut(rest, ",")
		if !ok || !strings.HasSuffix(meta, ";base64") {
			return picture{}, errors.New("an image's data: URL must be base64")
		}
		b, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			return picture{}, fmt.Errorf("an image's data: URL isn't base64: %v", err)
		}
		return picture{Mime: strings.TrimSuffix(meta, ";base64"), Data: b}, nil
	}
	if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") {
		return picture{URL: src}, nil
	}
	return picture{}, errors.New("an image is a data: or http(s) URL")
}

func readPart(fh *multipart.FileHeader) (picture, error) {
	f, err := fh.Open()
	if err != nil {
		return picture{}, err
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		return picture{}, err
	}
	mt := fh.Header.Get("Content-Type")
	if mt == "" || mt == "application/octet-stream" {
		mt = http.DetectContentType(b)
	}
	return picture{Mime: mt, Data: b}, nil
}

// drawVia is the API a model draws on at a provider.
type drawVia int

const (
	viaImages drawVia = iota // /images/generations, /images/edits
	viaChat                  // chat completions with modalities
	viaGemini                // Google's own generateContent
)

func (v drawVia) String() string {
	return [...]string{"images API", "chat completions", "generateContent"}[v]
}

func isGoogle(p provider.Provider) bool {
	return provider.HostOf(p.Base(provider.Chat)) == "generativelanguage.googleapis.com"
}

// viaFor is where model is best asked to draw at p.
func viaFor(p provider.Provider, model string) drawVia {
	switch {
	case p.IsRemoteMagpie():
		// another magpie's images API asks the model's vendor the way
		// that model draws there
		return viaImages
	case isGoogle(p) && strings.Contains(strings.ToLower(model), "gemini"):
		return viaGemini
	case provider.HostOf(p.Base(provider.Chat)) == "openrouter.ai":
		return viaChat
	case catalog.ImagesAPI(model):
		return viaImages
	}
	return viaChat
}

// draw asks the provider for d's images, on the API model draws on there,
// and on the other when that one isn't served.
func (s *Server) draw(ctx context.Context, p provider.Provider, model string, d drawing) (drawn, int, error) {
	if drawsCodex(p) || drawsGrok(p) {
		return s.drawImages(ctx, p, model, d)
	}
	if p.Base(provider.Chat) == "" {
		return drawn{}, 400, fmt.Errorf("%s can't draw: magpie draws only through an OpenAI-compatible API, and %s has none", p.Name, p.Name)
	}
	via := viaFor(p, model)
	out, code, err := s.drawOn(ctx, p, model, d, via)
	if err != nil && (code == 404 || code == 405) && via != viaGemini {
		other := viaChat
		if via == viaChat {
			other = viaImages
		}
		if o, c, e := s.drawOn(ctx, p, model, d, other); e == nil {
			return o, c, nil
		}
	}
	return out, code, err
}

func (s *Server) drawOn(ctx context.Context, p provider.Provider, model string, d drawing, via drawVia) (drawn, int, error) {
	switch via {
	case viaGemini:
		return s.drawGemini(ctx, p, model, d)
	case viaChat:
		return s.drawChat(ctx, p, model, d)
	}
	return s.drawImages(ctx, p, model, d)
}

// send posts to the vendor and reads its answer; a failure's code is the
// vendor's, with its own message.
func (s *Server) send(ctx context.Context, p provider.Provider, url, contentType string, body []byte, sign bool) ([]byte, int, error) {
	return s.sendAs(ctx, p, http.MethodPost, url, contentType, body, sign)
}

// sendAs is send with the method: a video's progress is asked with a GET.
func (s *Server) sendAs(ctx context.Context, p provider.Provider, method, url, contentType string, body []byte, sign bool) ([]byte, int, error) {
	ctx = p.Via(ctx)
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return nil, 500, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if p.IsRemoteMagpie() {
		passOnCaller(ctx, req)
	}
	if sign {
		if err := p.Sign(ctx, req, provider.Chat, body); err != nil {
			return nil, 502, err
		}
		if drawsCodex(p) {
			// the images API answers in JSON, not the stream Codex's
			// signing asks of its Responses; the id is Codex CLI's own
			req.Header.Set("Accept", "application/json")
			req.Header.Set("x-codex-imagegen-request-id", newUUID())
		}
	} else {
		// Google's API keys go in their own header
		req.Header.Set("x-goog-api-key", p.Key)
		for k, v := range p.Headers {
			req.Header[k] = []string{v}
		}
	}
	res, err := p.Do(s.client, req) // a plugin's through the plugin
	if err != nil {
		if ctx.Err() != nil {
			return nil, 504, fmt.Errorf("%s didn't answer in %s", p.Name, drawTimeout)
		}
		return nil, 502, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 256<<20))
	if err != nil {
		return nil, 502, err
	}
	if res.StatusCode >= 300 {
		return b, res.StatusCode, fmt.Errorf("%s: %d %s%s", provider.HostOf(url), res.StatusCode, http.StatusText(res.StatusCode), vendorSaid(vendorMessage(b)))
	}
	return b, res.StatusCode, nil
}

// vendorMessage is the message of a vendor's error, or its body.
func vendorMessage(b []byte) string {
	var e struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(b, &e) == nil && len(e.Error) > 0 {
		var m struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(e.Error, &m) == nil && m.Message != "" {
			return m.Message
		}
		var s string
		if json.Unmarshal(e.Error, &s) == nil {
			return s
		}
	}
	return string(b)
}

// drawImages asks an images API: generations, or edits with the images
// sent along.
func (s *Server) drawImages(ctx context.Context, p provider.Provider, model string, d drawing) (drawn, int, error) {
	base := strings.TrimRight(p.Base(provider.Chat), "/")
	if drawsCodex(p) || drawsGrok(p) {
		base = strings.TrimRight(p.Base(provider.Responses), "/")
	}
	var body []byte
	var ct, url string
	m := strings.ToLower(model)
	if drawsGrok(p) {
		// Grok's Imagine API takes an aspect ratio, not a size or a quality
		req := map[string]any{"model": model, "prompt": d.Prompt, "n": d.N, "response_format": "b64_json"}
		if ar := aspectAmong(d.Size, grokAspects); ar != "" {
			req["aspect_ratio"] = ar
		}
		if len(d.Images) > 0 {
			var imgs []map[string]string
			for _, pic := range d.Images {
				imgs = append(imgs, map[string]string{"url": pic.dataURL(), "type": "image_url"})
			}
			req["images"] = imgs
			ct, url = "application/json", base+"/images/edits"
		} else {
			ct, url = "application/json", base+"/images/generations"
		}
		body, _ = json.Marshal(req)
	} else if len(d.Images) == 0 {
		req := map[string]any{"model": model, "prompt": d.Prompt, "n": d.N}
		for k, v := range map[string]string{"size": d.Size, "quality": d.Quality, "background": d.Background, "output_format": d.Format} {
			if v != "" {
				req[k] = v
			}
		}
		// only dall-e gives a URL unless asked; gpt-image turns the field away
		if strings.Contains(m, "dall-e") {
			req["response_format"] = "b64_json"
		}
		body, _ = json.Marshal(req)
		ct, url = "application/json", base+"/images/generations"
	} else if drawsCodex(p) {
		// ChatGPT's backend takes an edit only as JSON, each image a data URL
		req := map[string]any{"model": model, "prompt": d.Prompt, "n": d.N}
		for k, v := range map[string]string{"size": d.Size, "quality": d.Quality, "background": d.Background, "output_format": d.Format} {
			if v != "" {
				req[k] = v
			}
		}
		var imgs []map[string]string
		for _, pic := range d.Images {
			imgs = append(imgs, map[string]string{"image_url": pic.dataURL()})
		}
		req["images"] = imgs
		if d.Mask != nil {
			req["mask"] = map[string]string{"image_url": d.Mask.dataURL()}
		}
		body, _ = json.Marshal(req)
		ct, url = "application/json", base+"/images/edits"
	} else {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		for k, v := range map[string]string{"model": model, "prompt": d.Prompt, "n": strconv.Itoa(d.N), "size": d.Size, "quality": d.Quality, "background": d.Background, "output_format": d.Format} {
			if v != "" {
				mw.WriteField(k, v)
			}
		}
		if strings.Contains(m, "dall-e") {
			mw.WriteField("response_format", "b64_json")
		}
		field := "image"
		if len(d.Images) > 1 {
			field = "image[]"
		}
		pics := append([]picture(nil), d.Images...)
		fields := make([]string, len(pics))
		for i := range pics {
			fields[i] = field
		}
		if d.Mask != nil {
			pics, fields = append(pics, *d.Mask), append(fields, "mask")
		}
		for i, pic := range pics {
			if pic.URL != "" {
				b, mt, err := s.fetchPicture(ctx, pic.URL)
				if err != nil {
					return drawn{}, 400, err
				}
				pic = picture{Mime: mt, Data: b}
			}
			h := textproto.MIMEHeader{}
			h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename="image%d%s"`, fields[i], i, extOf(pic.Mime)))
			h.Set("Content-Type", pic.Mime)
			part, _ := mw.CreatePart(h)
			part.Write(pic.Data)
		}
		mw.Close()
		body, ct, url = buf.Bytes(), mw.FormDataContentType(), base+"/images/edits"
	}
	b, code, err := s.send(ctx, p, url, ct, body, true)
	if err != nil {
		return drawn{}, code, err
	}
	var res struct {
		Data []struct {
			B64     string `json:"b64_json"`
			URL     string `json:"url"`
			Revised string `json:"revised_prompt"`
		} `json:"data"`
		OutputFormat string `json:"output_format"`
		Usage        struct {
			Input  int `json:"input_tokens"`
			Output int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(b, &res); err != nil {
		return drawn{}, 502, fmt.Errorf("%s's answer isn't an images API's: %v", p.Name, err)
	}
	out := drawn{Input: res.Usage.Input, Output: res.Usage.Output}
	for _, e := range res.Data {
		if e.Revised != "" {
			out.Revised = e.Revised
		}
		if e.B64 != "" {
			data, err := base64.StdEncoding.DecodeString(e.B64)
			if err != nil {
				return drawn{}, 502, fmt.Errorf("%s's image isn't base64: %v", p.Name, err)
			}
			out.Images = append(out.Images, picture{Mime: http.DetectContentType(data), Data: data})
		} else if e.URL != "" {
			out.Images = append(out.Images, picture{URL: e.URL})
		}
	}
	return out, code, nil
}

// fetchPicture downloads an image an edit was given by URL, for an images
// API that takes only files.
func (s *Server) fetchPicture(ctx context.Context, url string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}
	res, err := s.client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("couldn't fetch the image at %s: %v", url, err)
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return nil, "", fmt.Errorf("couldn't fetch the image at %s: %s", url, res.Status)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	if err != nil {
		return nil, "", err
	}
	return b, http.DetectContentType(b), nil
}

func extOf(mimeType string) string {
	switch mimeType {
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	}
	return ".png"
}

// drawPrompt is the prompt with what an images API takes as parameters
// said in words, for a model asked in chat.
func drawPrompt(d drawing) string {
	var b strings.Builder
	b.WriteString(d.Prompt)
	if r := aspectOf(d.Size); r != "" {
		fmt.Fprintf(&b, "\n\nAspect ratio: %s.", r)
	}
	if d.Background == "transparent" {
		b.WriteString(" Transparent background.")
	}
	return b.String()
}

// drawChat asks a chat model to answer with an image, once for each image
// asked for.
func (s *Server) drawChat(ctx context.Context, p provider.Provider, model string, d drawing) (drawn, int, error) {
	content := []map[string]any{{"type": "text", "text": drawPrompt(d)}}
	for _, pic := range d.Images {
		content = append(content, map[string]any{"type": "image_url", "image_url": map[string]string{"url": pic.dataURL()}})
	}
	req := map[string]any{"model": model, "stream": false, "modalities": []string{"image", "text"},
		"messages": []map[string]any{{"role": "user", "content": content}}}
	if r := aspectOf(d.Size); r != "" {
		req["image_config"] = map[string]string{"aspect_ratio": r}
	}
	body, _ := json.Marshal(req)
	url := strings.TrimRight(p.Base(provider.Chat), "/") + "/chat/completions"
	return s.eachDrawing(d.N, func() (drawn, int, error) {
		b, code, err := s.send(ctx, p, url, "application/json", p.Prepare(body), true)
		if err != nil {
			return drawn{}, code, err
		}
		var res struct {
			Choices []struct {
				Message struct {
					Content json.RawMessage `json:"content"`
					Images  []struct {
						ImageURL struct {
							URL string `json:"url"`
						} `json:"image_url"`
					} `json:"images"`
					// AIHubMix's Gemini: the parts as Gemini gives them
					MultiMod []struct {
						InlineData struct {
							Data     string `json:"data"`
							MimeType string `json:"mime_type"`
						} `json:"inline_data"`
					} `json:"multi_mod_content"`
				} `json:"message"`
			} `json:"choices"`
			Usage struct {
				Prompt     int `json:"prompt_tokens"`
				Completion int `json:"completion_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(b, &res); err != nil {
			return drawn{}, 502, fmt.Errorf("%s's answer isn't chat completions': %v", p.Name, err)
		}
		out := drawn{Input: res.Usage.Prompt, Output: res.Usage.Completion}
		if len(res.Choices) == 0 {
			return out, code, nil
		}
		msg := res.Choices[0].Message
		var srcs []string
		for _, im := range msg.Images {
			srcs = append(srcs, im.ImageURL.URL)
		}
		for _, part := range msg.MultiMod {
			if in := part.InlineData; in.Data != "" {
				srcs = append(srcs, "data:"+cmp.Or(in.MimeType, "image/png")+";base64,"+in.Data)
			}
		}
		var text string
		if json.Unmarshal(msg.Content, &text) != nil {
			var parts []struct {
				Type     string          `json:"type"`
				Text     string          `json:"text"`
				ImageURL json.RawMessage `json:"image_url"`
			}
			json.Unmarshal(msg.Content, &parts)
			for _, part := range parts {
				text += part.Text
				srcs = append(srcs, imageSources(part.ImageURL)...)
			}
		}
		out.Text = strings.TrimSpace(text)
		// a model that puts the image in its text, as markdown
		if len(srcs) == 0 {
			for _, f := range strings.Split(text, "(data:image/")[1:] {
				if end := strings.IndexByte(f, ')'); end > 0 {
					srcs = append(srcs, "data:image/"+f[:end])
				}
			}
		}
		for _, src := range srcs {
			pic, err := pictureOf(src)
			if err != nil {
				return out, 502, fmt.Errorf("%s's image: %v", p.Name, err)
			}
			out.Images = append(out.Images, pic)
		}
		return out, code, nil
	})
}

// drawGemini asks Gemini at Google on its own API, which answers with
// images where its OpenAI-compatible one doesn't.
func (s *Server) drawGemini(ctx context.Context, p provider.Provider, model string, d drawing) (drawn, int, error) {
	parts := []map[string]any{{"text": drawPrompt(d)}}
	for _, pic := range d.Images {
		if pic.URL != "" {
			b, mt, err := s.fetchPicture(ctx, pic.URL)
			if err != nil {
				return drawn{}, 400, err
			}
			pic = picture{Mime: mt, Data: b}
		}
		parts = append(parts, map[string]any{"inlineData": map[string]string{"mimeType": pic.Mime, "data": base64.StdEncoding.EncodeToString(pic.Data)}})
	}
	cfg := map[string]any{"responseModalities": []string{"TEXT", "IMAGE"}}
	if r := aspectOf(d.Size); r != "" {
		cfg["imageConfig"] = map[string]string{"aspectRatio": r}
	}
	body, _ := json.Marshal(map[string]any{"contents": []map[string]any{{"role": "user", "parts": parts}}, "generationConfig": cfg})
	base := strings.TrimSuffix(strings.TrimRight(p.Base(provider.Chat), "/"), "/openai")
	url := base + "/models/" + model + ":generateContent"
	return s.eachDrawing(d.N, func() (drawn, int, error) {
		b, code, err := s.send(ctx, p, url, "application/json", body, false)
		if err != nil {
			return drawn{}, code, err
		}
		var res struct {
			Candidates []struct {
				Content struct {
					Parts []struct {
						Text       string `json:"text"`
						Thought    bool   `json:"thought"`
						InlineData *struct {
							MimeType string `json:"mimeType"`
							Data     string `json:"data"`
						} `json:"inlineData"`
					} `json:"parts"`
				} `json:"content"`
			} `json:"candidates"`
			Usage struct {
				Prompt     int `json:"promptTokenCount"`
				Candidates int `json:"candidatesTokenCount"`
			} `json:"usageMetadata"`
		}
		if err := json.Unmarshal(b, &res); err != nil {
			return drawn{}, 502, fmt.Errorf("%s's answer isn't Gemini's: %v", p.Name, err)
		}
		out := drawn{Input: res.Usage.Prompt, Output: res.Usage.Candidates}
		for _, c := range res.Candidates {
			for _, part := range c.Content.Parts {
				switch {
				case part.InlineData != nil && !part.Thought:
					data, err := base64.StdEncoding.DecodeString(part.InlineData.Data)
					if err != nil {
						return out, 502, fmt.Errorf("%s's image isn't base64: %v", p.Name, err)
					}
					out.Images = append(out.Images, picture{Mime: part.InlineData.MimeType, Data: data})
				case part.Text != "" && !part.Thought:
					out.Text += part.Text
				}
			}
		}
		out.Text = strings.TrimSpace(out.Text)
		return out, code, nil
	})
}

// eachDrawing asks once for each of n images, at once, for a vendor that
// draws one image an answer; what they drew and cost is summed.
func (s *Server) eachDrawing(n int, ask func() (drawn, int, error)) (drawn, int, error) {
	type result struct {
		out  drawn
		code int
		err  error
	}
	results := make([]result, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o, c, e := ask()
			results[i] = result{o, c, e}
		}()
	}
	wg.Wait()
	var out drawn
	code := 200
	var firstErr error
	for _, r := range results {
		out.Input += r.out.Input
		out.Output += r.out.Output
		out.Images = append(out.Images, r.out.Images...)
		if r.out.Text != "" && out.Text == "" {
			out.Text = r.out.Text
		}
		if r.err != nil && firstErr == nil {
			firstErr, code = r.err, r.code
		}
	}
	// some images drawn is an answer; none, the first failure
	if len(out.Images) == 0 && firstErr != nil {
		return out, code, firstErr
	}
	return out, 200, nil
}

// aspectOf is the ratio an images API size ("1536x1024") is, as the models
// that take a ratio name it: the nearest of those they take. "" for none
// or "auto".
func aspectOf(size string) string {
	return aspectAmong(size, []string{"1:1", "2:3", "3:2", "3:4", "4:3", "4:5", "5:4", "9:16", "16:9", "21:9"})
}

// grokAspects are the aspect ratios Grok's Imagine API takes.
var grokAspects = []string{"1:1", "16:9", "9:16", "4:3", "3:4", "3:2", "2:3", "2:1", "1:2", "19.5:9", "9:19.5", "20:9", "9:20"}

// aspectAmong is size as the nearest of the aspect ratios in ratios: a
// ratio given as one passes through, WIDTHxHEIGHT is matched to the closest.
func aspectAmong(size string, ratios []string) string {
	w, h, ok := strings.Cut(strings.ToLower(strings.TrimSpace(size)), "x")
	if !ok {
		if strings.Contains(size, ":") {
			return strings.TrimSpace(size)
		}
		return ""
	}
	x, err1 := strconv.ParseFloat(w, 64)
	y, err2 := strconv.ParseFloat(h, 64)
	if err1 != nil || err2 != nil || x <= 0 || y <= 0 {
		return ""
	}
	best, bestD := "", math.Inf(1)
	for _, r := range ratios {
		a, b, _ := strings.Cut(r, ":")
		ra, _ := strconv.ParseFloat(a, 64)
		rb, _ := strconv.ParseFloat(b, 64)
		if d := math.Abs(math.Log(x/y) - math.Log(ra/rb)); d < bestD {
			best, bestD = r, d
		}
	}
	return best
}
