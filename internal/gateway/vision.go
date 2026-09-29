package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// A model that can't see images is given what a model that can says of
// them: each image in the request is described once by the vision model
// (the Settings' Vision, or one magpie picks) and the description goes in
// the image's place, in the current turn, older turns and tool results
// alike. With Vision off, or no model that sees, such an image is turned
// away as before (textOnlyBody).

// VisionAgent is the User-Agent of the descriptions magpie asks for.
const VisionAgent = "magpie-vision/1"

const visionTimeout = 2 * time.Minute

// visionParallel is how many images of a request are described at once.
const visionParallel = 4

// sightsKept is how many descriptions are kept: an agent sends a
// conversation's images again every turn.
const sightsKept = 256

const visionSystem = `You describe images for an AI model that cannot see them. It will answer the user from your description alone, so leave nothing out that it may need.
- Transcribe all text exactly as written, keeping its layout: code, terminal output, error messages, logs, UI labels, menus, file names, numbers.
- For a screenshot of an app or page: which app or page it is, its layout, and the state of what is on it (selected, disabled, checked, highlighted, error states).
- For a chart or table: its kind, axes and labels, and every value you can read.
- For a diagram: its elements and how they are connected.
- For a photo or drawing: what it shows, with the details that matter.
Describe only what is there. Don't guess at what can't be read, say it can't be read. Don't answer questions or give advice. No preamble.`

// describingKey marks a description magpie asks for, which must not have
// its image described again.
type describingKey struct{}

func describing(ctx context.Context) bool {
	v, _ := ctx.Value(describingKey{}).(bool)
	return v
}

// seer is the model that describes images: the Settings' Vision while it
// resolves, else AutoVision's. None when Vision is off or no model sees.
func seer() (string, bool) {
	switch v := settings.Load().Vision; v {
	case "off":
		return "", false
	case "":
	default:
		if _, _, ok := provider.Resolve(v); ok {
			return v, true
		}
	}
	m := AutoVision()
	return m, m != ""
}

// AutoVision is the model magpie picks to describe images when the
// Settings name none: a small model that sees of the providers set up — a
// Claude account's, a Codex account's, the cheapest of an API key's,
// another account's. "" when no model sees.
func AutoVision() string {
	tier := func(p provider.Provider) int {
		switch {
		case p.Account != nil && p.Account.Agent == "claude":
			return 0
		case p.Account != nil && p.Account.Agent == "codex":
			return 1
		case p.Account == nil:
			return 2
		}
		return 3
	}
	best, bestTier, bestCost := "", 0, 0.0
	for _, p := range provider.All() {
		if !p.On() || p.Decides() {
			continue
		}
		t := tier(p)
		if best != "" && t > bestTier {
			continue
		}
		m, cost := smallModelWhere(p, sees)
		if m == "" {
			continue
		}
		if best == "" || t < bestTier || cost < bestCost {
			best, bestTier, bestCost = p.ID+"/"+m, t, cost
		}
	}
	return best
}

// sees is whether a model takes images.
func sees(m catalog.Model) bool {
	return m.Images && (m.ImageInput == nil || *m.ImageInput)
}

// sight is a description, once it is done.
type sight struct {
	done chan struct{}
	text string
	err  error
}

// describe is what model says of the image at src, asked once for each
// image: a request that sends it again, or sends it while it is being
// described, is given the same description.
func (s *Server) describe(ctx context.Context, model, src string) (string, error) {
	sum := sha256.Sum256([]byte(model + "\x00" + src))
	key := hex.EncodeToString(sum[:])
	s.sightMu.Lock()
	if e, ok := s.sights[key]; ok {
		s.sightMu.Unlock()
		select {
		case <-e.done:
			return e.text, e.err
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if s.sights == nil {
		s.sights = map[string]*sight{}
	}
	e := &sight{done: make(chan struct{})}
	s.sights[key] = e
	s.sightOrder = append(s.sightOrder, key)
	for len(s.sightOrder) > sightsKept {
		delete(s.sights, s.sightOrder[0])
		s.sightOrder = s.sightOrder[1:]
	}
	s.sightMu.Unlock()
	e.text, e.err = s.askVision(ctx, model, src)
	if e.err != nil {
		// a failure isn't kept: the next request asks again
		s.sightMu.Lock()
		if s.sights[key] == e {
			delete(s.sights, key)
		}
		s.sightMu.Unlock()
	}
	close(e.done)
	return e.text, e.err
}

// askVision asks model through the gateway itself, as a client would, what
// the image at src shows.
func (s *Server) askVision(ctx context.Context, model, src string) (string, error) {
	ctx, cancel := context.WithTimeout(context.WithValue(ctx, describingKey{}, true), visionTimeout)
	defer cancel()
	req := map[string]any{
		"model":  model,
		"stream": false,
		"messages": []map[string]any{
			{"role": "system", "content": visionSystem},
			{"role": "user", "content": []map[string]any{
				{"type": "text", "text": "Describe this image."},
				{"type": "image_url", "image_url": map[string]string{"url": src}},
			}},
		},
		"max_tokens": 4096,
	}
	if effort := classifyEffort(model); effort != "" {
		req["reasoning_effort"] = effort
	}
	body, _ := json.Marshal(req)
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://magpie/v1/chat/completions", nil)
	if err != nil {
		return "", err
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("User-Agent", VisionAgent)
	w := httptest.NewRecorder()
	s.serve(w, r, provider.Chat, body)
	if ctx.Err() != nil {
		return "", fmt.Errorf("no answer in %s", visionTimeout)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		return "", fmt.Errorf("%d, not an answer", w.Code)
	}
	if w.Code >= 300 || out.Error != nil {
		msg := http.StatusText(w.Code)
		if out.Error != nil && out.Error.Message != "" {
			msg = out.Error.Message
		}
		return "", errors.New(msg)
	}
	if len(out.Choices) == 0 || strings.TrimSpace(out.Choices[0].Message.Content) == "" {
		return "", errors.New("no description")
	}
	return strings.TrimSpace(out.Choices[0].Message.Content), nil
}

// seenBody is the body with each of its images described by model in its
// place. An image of the latest turn that can't be described is an error:
// the model would be asked about what it isn't given. One in an older turn
// or a tool's result is left out, as textOnlyBody leaves it.
func (s *Server) seenBody(ctx context.Context, proto provider.Protocol, body []byte, model string) ([]byte, error) {
	var srcs []string
	have := map[string]bool{}
	walkImages(proto, body, func(im imageAt) (json.RawMessage, error) {
		if im.Src != "" && !have[im.Src] {
			have[im.Src] = true
			srcs = append(srcs, im.Src)
		}
		return nil, nil
	})
	type result struct {
		text string
		err  error
	}
	results := make([]result, len(srcs))
	var wg sync.WaitGroup
	slots := make(chan struct{}, visionParallel)
	for i, src := range srcs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			text, err := s.describe(ctx, model, src)
			results[i] = result{text, err}
		}()
	}
	wg.Wait()
	of := map[string]result{}
	for i, src := range srcs {
		of[src] = results[i]
	}
	return walkImages(proto, body, func(im imageAt) (json.RawMessage, error) {
		if im.Src == "" {
			if im.Current() {
				return nil, errors.New("the image is a file only its vendor can open")
			}
			return textBlock(proto, imageOmitted), nil
		}
		r := of[im.Src]
		if r.err != nil {
			if im.Current() {
				return nil, r.err
			}
			return textBlock(proto, "[Image omitted: this model accepts text only, and the image couldn't be described.]"), nil
		}
		// on its own lines: a translation may join it to the text around it
		return textBlock(proto, "\n[Image, as "+model+" describes it for this model, which can't see images:]\n"+r.text+"\n[End of the image's description]\n"), nil
	})
}

// smallModelWhere is smallModel among the provider's models that keep
// holds, and what it costs; "" when there is none.
func smallModelWhere(p provider.Provider, keep func(catalog.Model) bool) (string, float64) {
	id := smallModel(p, keep)
	if id == "" {
		return "", 0
	}
	for _, m := range p.Available() {
		if m.ID == id && m.Price != nil {
			return id, m.Price.Input + m.Price.Output
		}
	}
	return id, 1e9
}
