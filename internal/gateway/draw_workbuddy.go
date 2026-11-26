package gateway

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/provider"
)

// A WorkBuddy plan draws with the image models its product config lists:
// GET <site>/v3/config, signed as a chat is, names them among its models by
// tag ("text-to-image", "image-to-image"); which ones goes by the account,
// its build and the User-Agent its sign-in sends (the China build lists
// hunyuan-image-v3.0-art to the desktop app, hunyuan-image-alpha and its
// -edit to the CLI; WorkBuddy AI gpt-image-2.5-sunburst). They are asked as
// WorkBuddy's CLI asks them, at <site>/v2/images/generations and
// /v2/images/edits, in JSON, an edit's images as data URLs; the answer is
// OpenAI's wrapped in WorkBuddy's {code, msg, data}. Hunyuan answers with
// an image's URL, GPT Image with its bytes when asked for b64_json. Each
// image costs the plan's credits, so AutoDrawer never picks one.
//
// It is the same for the built-in and for a WorkBuddy moved onto its
// plugin (@magpie-community/opencode-workbuddy-auth): magpie shapes the
// request, the plugin's fetch signs and sends it.

// wbImagesFor is how long a plan's image models are kept before its config
// is asked again; wbImagesRetry how long a failed ask waits to be retried.
const (
	wbImagesFor   = time.Hour
	wbImagesRetry = 5 * time.Minute
)

// drawsWorkBuddy is whether p is a WorkBuddy plan, the China build's or
// WorkBuddy AI's: the built-in's, or the WorkBuddy plugin's.
func drawsWorkBuddy(p provider.Provider) bool {
	return isWorkBuddy(p) && p.Base(provider.Chat) != ""
}

// wbImageModel is an image or video model a WorkBuddy config lists, a
// video model with what it takes (video_workbuddy.go).
type wbImageModel struct {
	ID    string       `json:"id"`
	Name  string       `json:"name"`
	Tags  []string     `json:"tags"`
	Video *wbVideoSpec `json:"videoGeneration"`
}

func (m wbImageModel) tagged(tag string) bool {
	return slices.ContainsFunc(m.Tags, func(t string) bool { return strings.EqualFold(strings.TrimSpace(t), tag) })
}

// wbEditor is the model an edit of model's images is asked of: model when
// it takes images, else the one listed for that beside it (WorkBuddy's CLI
// pairs hunyuan-image-alpha with hunyuan-image-alpha-edit). "" for none.
func wbEditor(ms []wbImageModel, model string) string {
	for _, m := range ms {
		if m.ID == model && m.tagged("image-to-image") {
			return m.ID
		}
	}
	for _, m := range ms {
		if m.ID == model+"-edit" && m.tagged("image-to-image") {
			return m.ID
		}
	}
	return ""
}

// wbImageList is what a plan's config last said, or why it couldn't.
type wbImageList struct {
	models []wbImageModel
	at     time.Time
	err    error
	asking bool
}

var wbImages = struct {
	sync.Mutex
	m map[string]*wbImageList
}{m: map[string]*wbImageList{}}

// wbConfigClient asks a config the gateway isn't asking for a request:
// through the account's proxy, as the gateway's own client does.
var wbConfigClient = &http.Client{Timeout: 30 * time.Second, Transport: netproxy.Dispatch(&http.Transport{
	Proxy:             netproxy.Func,
	ForceAttemptHTTP2: true,
})}

func wbImagesKey(p provider.Provider) string { return p.ID + "\x00" + accountOf(p) }

// wbImageModelsOf is the image and video models p's config lists, as last
// asked within wbImagesFor. With wait it asks the config when that is
// older and answers with what it says; without, it answers at once with
// what it has (nothing, the first time) and asks in the background.
func wbImageModelsOf(ctx context.Context, client *http.Client, p provider.Provider, wait bool) ([]wbImageModel, error) {
	key := wbImagesKey(p)
	wbImages.Lock()
	e := wbImages.m[key]
	if e != nil && !e.at.IsZero() {
		ttl := wbImagesFor
		if e.err != nil {
			ttl = wbImagesRetry
		}
		if time.Since(e.at) < ttl {
			ms, err := e.models, e.err
			wbImages.Unlock()
			if len(ms) > 0 {
				err = nil
			}
			return ms, err
		}
	}
	if e == nil {
		e = &wbImageList{}
		wbImages.m[key] = e
	}
	if !wait {
		ms := e.models
		if !e.asking {
			e.asking = true
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				wbAskImages(ctx, client, p, key)
			}()
		}
		wbImages.Unlock()
		return ms, nil
	}
	wbImages.Unlock()
	return wbAskImages(ctx, client, p, key)
}

// wbAskImages asks p's config for its image models and keeps the answer:
// a failure keeps the models it had.
func wbAskImages(ctx context.Context, client *http.Client, p provider.Provider, key string) ([]wbImageModel, error) {
	ms, err := wbFetchImageModels(ctx, client, p)
	wbImages.Lock()
	defer wbImages.Unlock()
	e := wbImages.m[key]
	if e == nil {
		e = &wbImageList{}
		wbImages.m[key] = e
	}
	e.asking, e.at, e.err = false, time.Now(), err
	if err == nil {
		e.models = ms
	}
	if err != nil && len(e.models) > 0 {
		return e.models, nil
	}
	return ms, err
}

// wbFetchImageModels asks <site>/v3/config, signed as p's chats are, for
// the models it tags as drawing or filming.
func wbFetchImageModels(ctx context.Context, client *http.Client, p provider.Provider) ([]wbImageModel, error) {
	origin := p.Origin(ctx)
	if origin == "" {
		return nil, fmt.Errorf("%s: no site to ask for its image models", p.Name)
	}
	ctx = p.Via(ctx)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+"/v3/config", nil)
	if err != nil {
		return nil, err
	}
	if err := p.Sign(ctx, req, provider.Chat, nil); err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	res, err := p.Do(client, req) // a moved one's through its plugin
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s's config: %d%s", p.Name, res.StatusCode, errorSaid(b))
	}
	var env struct {
		Code json.RawMessage `json:"code"`
		Msg  string          `json:"msg"`
		Data struct {
			Models []wbImageModel `json:"models"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &env); err != nil {
		return nil, fmt.Errorf("%s's config: %v", p.Name, err)
	}
	if c := strings.Trim(string(env.Code), `"`); c != "" && c != "0" && c != "null" {
		return nil, fmt.Errorf("%s's config: %s (%s)", p.Name, env.Msg, c)
	}
	var out []wbImageModel
	for _, m := range env.Data.Models {
		if m.ID != "" && (m.tagged("text-to-image") || m.tagged("image-to-image") || m.films()) {
			out = append(out, m)
		}
	}
	return out, nil
}

// wbDrawers are the models a WorkBuddy plan draws with: those its config
// tags text-to-image, as last asked (asked again in the background when
// that's old). One that edits too, itself or by the model beside it, takes
// images.
func wbDrawers(p provider.Provider) []catalog.Model {
	ms, _ := wbImageModelsOf(context.Background(), wbConfigClient, p, false)
	var out []catalog.Model
	for _, m := range ms {
		if !m.tagged("text-to-image") {
			continue
		}
		out = append(out, catalog.Model{ID: m.ID, Name: cmp.Or(m.Name, m.ID), Provider: p.ID, Images: wbEditor(ms, m.ID) != ""})
	}
	return out
}

// drawWorkBuddy asks WorkBuddy's images API as its CLI does: GPT Image with
// OpenAI's parameters and b64_json, Hunyuan with the prompt, n and size
// alone; an edit goes to the model that edits (wbEditor), its images as
// data URLs.
func (s *Server) drawWorkBuddy(ctx context.Context, p provider.Provider, model string, d drawing) (drawn, int, error) {
	target := model
	if len(d.Images) > 0 {
		ms, err := wbImageModelsOf(ctx, s.client, p, true)
		if err != nil {
			return drawn{}, 502, err
		}
		if target = wbEditor(ms, model); target == "" {
			return drawn{}, 400, fmt.Errorf("%s draws %s from a prompt only: its config lists no model to edit images with", p.Name, model)
		}
	}
	gpt := strings.Contains(strings.ToLower(target), "gpt-image")
	req := map[string]any{"model": target, "prompt": d.Prompt, "n": d.N}
	if d.Size != "" && (gpt || d.Size != "auto") {
		req["size"] = d.Size
	}
	if gpt {
		for k, v := range map[string]string{"quality": d.Quality, "background": d.Background, "output_format": d.Format} {
			if v != "" {
				req[k] = v
			}
		}
		req["response_format"] = "b64_json"
	}
	url := strings.TrimRight(p.Base(provider.Chat), "/") + "/images/generations"
	if len(d.Images) > 0 {
		var imgs []string
		for _, pic := range d.Images {
			imgs = append(imgs, pic.dataURL())
		}
		req["image"] = imgs
		if gpt && d.Mask != nil {
			req["mask"] = d.Mask.dataURL()
		}
		url = strings.TrimRight(p.Base(provider.Chat), "/") + "/images/edits"
	}
	body, _ := json.Marshal(req)
	b, code, err := s.send(ctx, p, url, "application/json", body, true)
	if err != nil {
		return drawn{}, code, err
	}
	// OpenAI's answer is WorkBuddy's data
	var env struct {
		Code json.RawMessage `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(b, &env) == nil && len(env.Code) > 0 {
		if c := strings.Trim(string(env.Code), `"`); c != "0" {
			return drawn{}, 502, fmt.Errorf("%s: %s (%s)", provider.HostOf(url), cmp.Or(env.Msg, "error"), c)
		}
		if len(env.Data) > 0 && env.Data[0] == '{' {
			b = env.Data
		}
	}
	return readImagesAnswer(p, url, b, code)
}
