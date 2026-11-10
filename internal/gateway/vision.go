package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/provider"
)

// A model that can't see images is given what a model that can says of
// them: each image in the request is described once by the vision model
// (the Settings' Vision, or one magpie picks) and the description goes in
// the image's place, in the current turn, older turns and tool results
// alike. With Vision off, or no model that sees, such an image is turned
// away as before (textOnlyBody).

// Agents are told a model that can't see takes images while one that can
// describes them to it.
func init() {
	provider.Described = func() bool {
		// whether one sees, not which account a request goes to
		defer provider.Unwaited()()
		_, ok := seer()
		return ok
	}
}

// VisionAgent is the User-Agent of the descriptions magpie asks for.
const VisionAgent = "magpie-vision/1"

// visionTimeout is how long the vendor has to describe an image, from when
// the request went out to it (each try's own), and visionWaitMost how long
// a description may take in all, the waits for a slot or a resting provider
// with it (sideDeadline, #2318): a reasoning model describing a dense
// screenshot can take more than two minutes by itself.
var visionTimeout, visionWaitMost = 3 * time.Minute, 10 * time.Minute

// visionParallel is how many images of a request are described at once.
const visionParallel = 4

// sightsKept is how many descriptions are kept, the least recently used
// dropped first: an agent sends a conversation's images again every turn,
// and a long Computer Use one holds hundreds of screenshots (fflake33).
// A description is a few kilobytes.
const sightsKept = 2048

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

// describeForKey holds the request an image is described for: the Routing
// view names it by the description's row, in that request's session.
type describeForKey struct{}

type describeFor struct {
	call    *CallFor
	session string
}

func withDescribeFor(ctx context.Context, agent, model, session string, unknown bool) context.Context {
	return context.WithValue(ctx, describeForKey{}, describeFor{&CallFor{Agent: agent, Model: model, Unknown: unknown, Missing: VisionMissing()}, session})
}

func describedFor(ctx context.Context) *CallFor {
	f, _ := ctx.Value(describeForKey{}).(describeFor)
	return f.call
}

// blindTo is whether a describer is to describe images for pid/model: its
// list says it takes none (in), or says nothing and magpie counts it
// text-only — as agents were told before Vision had them told every model
// takes images (provider.Described): models.dev doesn't say it sees either.
//
// One that turned an image away since magpie started (sightRefused) is
// blind whatever its list or models.dev says: models.dev's DeepSeek-V4.1-
// Flash takes images, and a relay serving it with text only refused the
// image with a 400 that sent the request on to the same model at another
// provider, past the Image recognition model the user picked (xyenon).
func (s *Server) blindTo(pid, model string, in *bool) bool {
	if s.sightRefused(pid, model) {
		return true
	}
	if in != nil {
		return !*in
	}
	e, ok := provider.ServedEntryOf(pid + "/" + model)
	return ok && !e.Images
}

// seer is the model that describes images: the Settings' Vision while it
// resolves, else AutoVision's. None when Vision is off or no model sees.
// A Vision that no longer resolves (its provider removed or off, the model
// gone) is not described with in silence by AutoVision's: VisionMissing
// names it, the description's row in Routing and Settings › Models say it
// is missing, and a request turned away for want of a describer says so.
func seer() (string, bool) {
	switch v := provider.HeldSettings().Vision; v {
	case "off":
		return "", false
	case "":
	default:
		if pickedMissing(v) == "" {
			return v, true
		}
	}
	m := AutoVision()
	return m, m != ""
}

// VisionMissing is the model Settings › Models › Image recognition names
// when magpie can't find it any more, so that AutoVision's describes in its
// place. "" when none is named, Vision is off, or it resolves.
func VisionMissing() string { return pickedMissing(provider.HeldSettings().Vision) }

// pickedMissing is a model the user picked in Settings (v) that doesn't
// resolve: "" for none picked, off, or one magpie finds.
func pickedMissing(v string) string {
	if v == "" || v == "off" {
		return ""
	}
	if _, _, ok := provider.Resolve(v); ok {
		return ""
	}
	return v
}

// missingSeerNote is what a request turned away, or whose image wasn't
// described, says of a picked Image recognition model that is missing.
func missingSeerNote() string {
	if v := VisionMissing(); v != "" {
		return fmt.Sprintf("; the Image recognition model picked in magpie's Settings, %q, isn't set up any more: pick another in Settings → Models → Image recognition", v)
	}
	return ""
}

// textOnlyNote is what a request turned away for its image says of why
// magpie takes the model for text-only and where that is changed, so the
// reader turns on the model's switch rather than trying another model or
// reinstalling the agent (KimYx0207 on X, over mark_li21's Codex paste): the
// user's own answer, else what the provider's list says; for a group, that
// none of its members is marked as taking images.
func textOnlyNote(p provider.Provider, model string, group string) string {
	if group != "" {
		return fmt.Sprintf("; no model in the group %s is marked as taking images: turn on Accepts images for one that does, in its provider's Model settings in magpie", group)
	}
	name := p.Name
	if name == "" {
		name = p.ID
	}
	if v, ok := provider.ImageOverride(p.ID, model); ok && !v {
		return fmt.Sprintf("; Accepts images is turned off for it in magpie, in Providers → %s → Model settings", name)
	}
	return fmt.Sprintf("; %s's model list marks it as not taking images: if it does, turn on Accepts images for it in magpie, in Providers → %s → Model settings", name, name)
}

// noSeerNote is what a request turned away for an image says of why no
// model described it: the picked one is missing, Image recognition is
// off, or none of the models the user enabled can see.
func noSeerNote() string {
	if n := missingSeerNote(); n != "" {
		return n
	}
	if provider.HeldSettings().Vision == "off" {
		return "; Image recognition is off in magpie's Settings → Models"
	}
	if _, ok := seer(); !ok {
		return "; none of the models enabled in magpie can see images to describe it: enable one that can, or pick one in Settings → Models → Image recognition"
	}
	return ""
}

// autoSeerNote is what a request whose image wasn't described says of a
// describer magpie picked itself, which the user may not know of.
func autoSeerNote(see string) string {
	if provider.HeldSettings().Vision != "" {
		return ""
	}
	return fmt.Sprintf("; magpie picked %s of the models you enabled: pick another in Settings → Models → Image recognition", see)
}

// AutoVision is the model magpie picks to describe images when the
// Settings name none: a small model that sees, of the models the user
// enabled on the providers switched on (smallModel) — a
// Claude account's, a Codex account's, the cheapest of an API key's,
// another account's. One with allowance left goes before one whose every
// account or key is out of it (smallReady), whatever their order: the
// one out is picked only when every model that sees is, so that agents
// are still told images are described (provider.Described), and
// askVisionWith says when it is back instead of asking (#1801). "" when
// no model sees.
func AutoVision() string {
	m, _ := autoVision()
	return m
}

// autoVision is AutoVision's pick, and whether it has allowance left.
func autoVision() (string, bool) {
	if r := seersRanked(nil); len(r) > 0 {
		return r[0].model, r[0].ready
	}
	return "", false
}

// rankedSeer is a model seersRanked names, and whether it has allowance.
type rankedSeer struct {
	model string
	ready bool
}

// seersRanked is each switched-on provider's small model that sees, in
// the order AutoVision picks among them: allowance left first, then a
// Claude account's, a Codex account's, an API key's, another account's,
// then the cheaper, then the provider listed first. A model skip holds is
// passed over for the provider's next one (the describers a failed
// description already asked, describeWith).
func seersRanked(skip func(string) bool) []rankedSeer {
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
	type ranked struct {
		rankedSeer
		tier int
		cost float64
	}
	var rs []ranked
	for _, p := range provider.All() {
		if !p.On() || p.DecideOnly() {
			continue
		}
		cs := keptSmallCands(p, "sees", seesAt(p.ID))
		if skip != nil {
			cs = slices.DeleteFunc(slices.Clone(cs), func(c smallCand) bool { return skip(p.ID + "/" + c.id) })
		}
		c, ready := smallReadyOf(p, cs, provider.Chat)
		if c.id == "" {
			continue
		}
		rs = append(rs, ranked{rankedSeer{p.ID + "/" + c.id, ready}, tier(p), c.cost})
	}
	slices.SortStableFunc(rs, func(a, b ranked) int {
		switch {
		case a.ready != b.ready:
			if a.ready {
				return -1
			}
			return 1
		case a.tier != b.tier:
			return a.tier - b.tier
		case a.cost < b.cost:
			return -1
		case a.cost > b.cost:
			return 1
		}
		return 0
	})
	out := make([]rankedSeer, len(rs))
	for i, r := range rs {
		out[i] = r.rankedSeer
	}
	return out
}

// autoSpent is why AutoVision's model isn't asked to describe an image:
// it is out of its allowance, as every model that sees is (spentModel),
// and would only be refused again. nil when it has allowance, or when
// the Settings name the model, which is asked as the user picked it.
func autoSpent(model string) error {
	if !autoDescribes() {
		return nil
	}
	p, m, ok := provider.Resolve(model)
	if !ok {
		return nil
	}
	back, spent := spentModel(p, m, provider.Chat, time.Now())
	if !spent {
		return nil
	}
	when := ""
	if !back.IsZero() {
		when = " until " + heldClock(back, time.Now())
	}
	return fmt.Errorf("%s is out of its allowance%s, as is every other model magpie could describe it with, and none is asked until one is back", model, when)
}

// autoDescribes is whether the model that describes images is
// AutoVision's: the Settings name none, or one that is missing.
func autoDescribes() bool {
	v := provider.HeldSettings().Vision
	return v == "" || pickedMissing(v) != ""
}

// sees is whether a model takes images.
func sees(m catalog.Model) bool {
	return m.Images && (m.ImageInput == nil || *m.ImageInput)
}

// seesAt is sees for provider pid's models, the user's answer first
// (provider.SetModelImage, magpie model spec --vision): a model they
// marked text-only is never picked to describe images, and one they
// marked as seeing is. smallKept is stamped by settings.json too, so a
// change is read on the next pick.
func seesAt(pid string) func(catalog.Model) bool {
	return func(m catalog.Model) bool {
		if v, ok := provider.ImageOverride(pid, m.ID); ok {
			return v
		}
		return sees(m)
	}
}

// sight is a description, once it is done.
type sight struct {
	done chan struct{}
	text string
	by   string // the model that described it: model, or one in its place
	err  error
	// answered means the model call is over, even while a successful inline
	// description is still being saved. Cancellation must not stop that save.
	answered   bool
	persistent bool // inline bytes, never a URL whose contents can change
	// waiting is how many requests wait on it now, and stop ends the
	// work of describing it: the last of them to go stops it (sightMu)
	waiting int
	stop    context.CancelFunc
}

// sightKey is what a description is kept under: the image's own bytes,
// as an inline image's data URL carries them, or the address it is at,
// and the level picked in Settings to describe images at. The same
// picture is described once, whichever request, protocol or data URL's
// type sends it, and whichever model describes it; a level picked since
// reads it again.
func sightKey(src string) string {
	h := sha256.New()
	h.Write([]byte(provider.HeldSettings().VisionEffort + "\x00"))
	if b, ok := dataURLBytes(src); ok {
		h.Write([]byte("bytes\x00"))
		h.Write(b)
	} else {
		h.Write([]byte("src\x00"))
		h.Write([]byte(src))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// dataURLBytes is the bytes of a base64 data URL, false for anything else.
func dataURLBytes(src string) ([]byte, bool) {
	rest, ok := strings.CutPrefix(src, "data:")
	if !ok {
		return nil, false
	}
	meta, data, ok := strings.Cut(rest, ",")
	if !ok || !strings.HasSuffix(strings.ToLower(meta), ";base64") {
		return nil, false
	}
	// strictly: text that only decodes loosely to the same bytes is
	// another image's, as far as is known
	for _, enc := range []*base64.Encoding{base64.StdEncoding.Strict(), base64.RawStdEncoding.Strict(), base64.URLEncoding.Strict(), base64.RawURLEncoding.Strict()} {
		if b, err := enc.DecodeString(data); err == nil {
			return b, true
		}
	}
	return nil, false
}

// describe is what model says of the image at src, asked once for each
// image: a request that sends it again, or sends it while it is being
// described, is given the same description. by is the model that
// described it: model, or the next that sees when model couldn't
// (describeWith).
//
// A description is kept under the image (sightKey), not the model that
// was asked: Pkh on Discord had AutoVision's pick change between turns,
// and every image of a long conversation was described again, one
// request sending 63 descriptions at once. A new image is asked of
// model first again, so a pick that failed once is tried on each new
// image and not given up for good (kelvinjason11 on X).
//
// The description is the work of no one request: one that goes away
// leaves it to the others waiting on it, and the last to go stops it
// and leaves nothing behind, so the next asks again. Only a failure of
// the describing itself is handed to those waiting; it isn't kept.
func (s *Server) describe(ctx context.Context, model, src string) (text, by string, err error) {
	return s.describeKeyed(ctx, model, src, sightKey(src))
}

func (s *Server) describeKeyed(ctx context.Context, model, src, key string) (text, by string, err error) {
	if err := ctx.Err(); err != nil {
		return "", model, err
	}
	s.sightMu.Lock()
	e, ok := s.sights[key]
	if ok {
		// used again, so the last to go: a long conversation's first
		// images, still sent every turn, outlast those of conversations
		// that have ended
		if i := slices.Index(s.sightOrder, key); i >= 0 {
			s.sightOrder = append(append(s.sightOrder[:i:i], s.sightOrder[i+1:]...), key)
		}
	} else {
		work, stop := context.WithCancel(context.WithoutCancel(ctx))
		_, persistent := dataURLBytes(src)
		e = &sight{done: make(chan struct{}), stop: stop, persistent: persistent}
		s.keepSightLocked(key, e)
		if persistent {
			s.beginSightSave()
		}
		go func() {
			if persistent {
				defer s.endSightSave()
			}
			s.see(work, e, model, src, key)
		}()
	}
	e.waiting++
	s.sightMu.Unlock()
	select {
	case <-e.done:
		s.sightMu.Lock()
		e.waiting--
		s.sightMu.Unlock()
		if e.by == "" {
			return e.text, model, e.err
		}
		return e.text, e.by, e.err
	case <-ctx.Done():
		s.sightMu.Lock()
		e.waiting--
		answered := e.answered
		if e.waiting == 0 && !answered {
			select {
			case <-e.done:
			default:
				// no one waits for work still in the model: it stops, and the
				// next to send the image asks again
				s.forgetSightLocked(key, e)
				if e.stop != nil {
					e.stop()
				}
			}
		}
		s.sightMu.Unlock()
		if answered {
			// The model has answered: let its successful description finish
			// saving before the request returns, so an immediate restart or
			// handover can't read an older cache.
			<-e.done
		}
		return "", model, ctx.Err()
	}
}

// see describes the image for e, on work, which ends when no one waits.
func (s *Server) see(work context.Context, e *sight, model, src, key string) {
	text, by, err := s.describeWith(work, model, src, visionSystem, "Describe this image.")
	s.sightMu.Lock()
	e.text, e.by, e.err = text, by, err
	e.answered = true
	if err != nil {
		// a failure isn't kept: the next request asks again
		s.forgetSightLocked(key, e)
	}
	s.sightMu.Unlock()

	if err == nil && e.persistent {
		// Persist before releasing the waiters: an immediate restart must
		// find this description. A write failure only loses that saving.
		s.saveSight(key, text, by)
	}

	s.sightMu.Lock()
	if err == nil && s.sights[key] == nil {
		// answered as the last to wait went (or while it was being saved):
		// kept for the next, as before
		s.keepSightLocked(key, e)
	}
	close(e.done)
	s.sightMu.Unlock()
	e.stop()
}

// keepSightLocked keeps e under key, the most recently used, and lets
// the least recently used go past sightsKept; sightMu is held.
func (s *Server) keepSightLocked(key string, e *sight) {
	if s.sights == nil {
		s.sights = map[string]*sight{}
	}
	s.sights[key] = e
	s.sightOrder = append(s.sightOrder, key)
	for len(s.sightOrder) > sightsKept {
		delete(s.sights, s.sightOrder[0])
		s.sightOrder = s.sightOrder[1:]
	}
}

// forgetSightLocked takes e, kept under key, out of the cache; sightMu is
// held.
func (s *Server) forgetSightLocked(key string, e *sight) {
	if s.sights[key] != e {
		return
	}
	delete(s.sights, key)
	if i := slices.Index(s.sightOrder, key); i >= 0 {
		s.sightOrder = slices.Delete(s.sightOrder, i, i+1)
	}
}

// notDescribed puts on the Routing page a request whose images weren't
// described before it was routed: call says why. Its describers' rows
// stood in for it while it waited (noteRoute), and without this it had
// none of its own once it ended (Pkh on Discord: only "Image
// description" rows, the request they were for gone). One magpie
// answered for, held too long (giveUp), has its own already.
func (s *Server) notDescribed(r *http.Request, call *Call, start time.Time) {
	if f := inflightOf(r.Context()); f != nil {
		f.mu.Lock()
		answered := f.unrouted != nil && f.unrouted.ended
		f.mu.Unlock()
		if answered {
			return
		}
	}
	rt := s.trace.beginFor(r, Route{Time: start, Agent: call.Agent, Session: sessionOf(r.Header), Kind: call.Kind, For: call.For, Model: call.Model, Provider: "magpie"})
	ms := time.Since(start).Milliseconds()
	s.trace.update(rt, func(t *Route) {
		t.Done, t.Status, t.Error, t.Millis = true, call.Status, call.Error, ms
	})
	call.Route = rt.ID
}

// visionTries is how many models are asked about one image, the first
// with those in its place.
const visionTries = 3

// describeWith asks model about the image at src (askVisionWith), and
// when it fails, the next model that sees, as AutoVision ranks them, up
// to visionTries in all and visionWaitMost all told. by is the model that
// answered. kelvinjason11 on X: the one model picked to describe images
// failing failed every turn with an image, though other models that see
// were enabled.
//
// The model picked in Settings is always asked first; the others are
// asked only once it fails, and the Routing view's row of the one that
// answered names the one asked before it and why (CallFor.Instead). A
// model out of its allowance isn't asked in another's place. A request
// its agent canceled asks no other.
func (s *Server) describeWith(ctx context.Context, model, src, system, text string) (said, by string, err error) {
	deadline := time.Now().Add(visionWaitMost)
	tried := []string{model}
	var fails []visionTry
	ask, askCtx := model, ctx
	for {
		said, err = s.askVisionWith(askCtx, ask, src, system, text, time.Until(deadline))
		if err == nil {
			return said, ask, nil
		}
		fails = append(fails, visionTry{ask, err})
		if ctx.Err() != nil || len(tried) >= visionTries || !time.Now().Before(deadline) {
			break
		}
		// those with allowance go first, so the first is out of it only
		// when every one is
		r := seersRanked(func(m string) bool {
			if slices.Contains(tried, m) {
				return true
			}
			pid, id, _ := strings.Cut(m, "/")
			return s.sightRefused(pid, id)
		})
		if len(r) == 0 || !r[0].ready {
			break
		}
		next := r[0].model
		askCtx = describingInstead(ctx, ask, model, err)
		tried = append(tried, next)
		ask = next
	}
	return "", model, visionFailed(fails)
}

// visionTry is a model asked about an image, and why it didn't answer.
type visionTry struct {
	model string
	err   error
}

// visionFailed is why no model asked about an image answered: the first
// one's reason as it was, then each asked in its place, by name.
type visionFailed []visionTry

func (f visionFailed) Error() string {
	var b strings.Builder
	for i, t := range f {
		if i == 0 {
			b.WriteString(t.err.Error())
			continue
		}
		fmt.Fprintf(&b, "; nor could %s, asked in its place: %v", t.model, t.err)
	}
	return b.String()
}

func (f visionFailed) Unwrap() []error {
	out := make([]error, len(f))
	for i, t := range f {
		out[i] = t.err
	}
	return out
}

// describingInstead is ctx for a model asked about an image in failed's
// place: the Routing view's row says which was asked before and why, and
// whether that was the one picked in Settings (first, as Settings names it).
func describingInstead(ctx context.Context, failed, first string, err error) context.Context {
	f, _ := ctx.Value(describeForKey{}).(describeFor)
	g := CallFor{}
	if f.call != nil {
		g = *f.call
	}
	g.Instead, g.Why = failed, err.Error()
	g.Picked = failed == first && !autoDescribes()
	f.call = &g
	return context.WithValue(ctx, describeForKey{}, f)
}

// askVision asks model through the gateway itself, as a client would, what
// the image at src shows.
func (s *Server) askVision(ctx context.Context, model, src string) (string, error) {
	return s.askVisionWith(ctx, model, src, visionSystem, "Describe this image.", visionWaitMost)
}

// askVisionWith asks model about the image at src: system, and the user's
// text beside the image. most is how long it may take, the waits for a
// slot or a resting provider with it: what describeWith has left.
func (s *Server) askVisionWith(ctx context.Context, model, src, system, text string, most time.Duration) (string, error) {
	if err := autoSpent(model); err != nil {
		return "", err
	}
	ctx, cancel := sideDeadline(context.WithValue(ctx, describingKey{}, true), "image description", visionTimeout, most)
	defer cancel()
	req := map[string]any{
		"model":  model,
		"stream": false,
		"messages": []map[string]any{
			{"role": "system", "content": system},
			{"role": "user", "content": []map[string]any{
				{"type": "text", "text": text},
				{"type": "image_url", "image_url": map[string]string{"url": src}},
			}},
		},
		"max_tokens": 4096,
	}
	if effort := visionEffort(model); effort != "" {
		req["reasoning_effort"] = effort
		// a level picked in Settings is given room to think in
		if picked := sideEffort(model, provider.HeldSettings().VisionEffort); picked != "" {
			req["max_tokens"] = replyRoom(4096, picked)
		}
	}
	body, _ := json.Marshal(req)
	r, err := http.NewRequestWithContext(ownAsk(magpieChose(ctx)), http.MethodPost, "http://magpie/v1/chat/completions", nil)
	if err != nil {
		return "", err
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("User-Agent", VisionAgent)
	if f, _ := ctx.Value(describeForKey{}).(describeFor); f.session != "" {
		// in the Routing view beside the request it describes for
		r.Header.Set(SessionHeader, f.session)
	}
	w := httptest.NewRecorder()
	s.serve(w, r, provider.Chat, body)
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
	parsed := json.Unmarshal(w.Body.Bytes(), &out) == nil
	if parsed && w.Code < 300 && out.Error == nil && len(out.Choices) > 0 {
		if said := strings.TrimSpace(out.Choices[0].Message.Content); said != "" {
			// a description that came whole is kept, though what asked
			// for it ended as it came: it was paid for
			return said, nil
		}
	}
	if t, ok := sideTimeout(ctx); ok {
		return "", t
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if !parsed {
		return "", fmt.Errorf("%d, not an answer", w.Code)
	}
	if w.Code >= 300 || out.Error != nil {
		msg := http.StatusText(w.Code)
		if out.Error != nil && out.Error.Message != "" {
			msg = out.Error.Message
		}
		return "", errors.New(msg)
	}
	return "", errors.New("no description")
}

// seenBody is the body with each of its images described by model in its
// place. An image of the latest turn that can't be described is an error:
// the model would be asked about what it isn't given. One in an older turn
// or a tool's result is left out, as textOnlyBody leaves it.
func (s *Server) seenBody(ctx context.Context, proto provider.Protocol, body []byte, model string) ([]byte, error) {
	// the images described before a route is begun: a wait magpie knows
	// of (unrouted.go)
	defer noteWait(ctx, "image description")()
	// each picture once, however many times and however it is sent
	keyOf := map[string]string{}
	var keys, srcs []string
	have := map[string]bool{}
	walkImages(proto, body, func(im imageAt) (json.RawMessage, error) {
		if im.Src == "" {
			return nil, nil
		}
		if _, ok := keyOf[im.Src]; !ok {
			k := sightKey(im.Src)
			keyOf[im.Src] = k
			if !have[k] {
				have[k] = true
				keys, srcs = append(keys, k), append(srcs, im.Src)
			}
		}
		return nil, nil
	})
	type result struct {
		text, by string
		err      error
	}
	results := make([]result, len(srcs))
	var wg sync.WaitGroup
	slots := make(chan struct{}, visionParallel)
	for i, src := range srcs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// one waiting for a slot when the request ends asks no one
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				results[i] = result{by: model, err: ctx.Err()}
				return
			}
			defer func() { <-slots }()
			text, by, err := s.describeKeyed(ctx, model, src, keys[i])
			results[i] = result{text, by, err}
		}()
	}
	wg.Wait()
	of := map[string]result{}
	for i, k := range keys {
		of[k] = results[i]
	}
	return walkImages(proto, body, func(im imageAt) (json.RawMessage, error) {
		if im.Src == "" {
			if im.Current() {
				return nil, errors.New("the image is a file only its vendor can open")
			}
			return textBlock(proto, imageOmitted), nil
		}
		r := of[keyOf[im.Src]]
		if r.err != nil {
			if im.Current() {
				return nil, r.err
			}
			return textBlock(proto, "[Image omitted: this model accepts text only, and the image couldn't be described.]"), nil
		}
		// on its own lines: a translation may join it to the text around it
		return textBlock(proto, "\n[Image, as "+r.by+" describes it for this model, which can't see images:]\n"+r.text+"\n[End of the image's description]\n"), nil
	})
}

// smallModelWhere is smallModel among the provider's models that keep
// holds, what it costs, and whether it has allowance left (smallReady);
// "" when there is none. kind names keep (keptSmallCands).
func smallModelWhere(p provider.Provider, kind string, keep func(catalog.Model) bool) (string, float64, bool) {
	c, ready := smallReadyOf(p, keptSmallCands(p, kind, keep), provider.Chat)
	if c.id == "" {
		return "", 0, false
	}
	return c.id, c.cost, ready
}

// imageRefusedWords are how a vendor turns away an image its model can't
// take: DeepSeek's 400 "Failed to deserialize the JSON body into the
// target type: messages[0]: unknown variant `image_url`, expected `text`"
// (and Responses' input_image, Messages' image, so spelt), OpenAI's
// "image_url is only supported by certain models", OpenRouter's "No
// endpoints found that support image input", vLLM's "is not a multimodal
// model", and the plain "does not support image input" / "image input is
// not supported" / "不支持图片" of the rest.
var imageRefusedWords = lazyre.New("(?i)unknown variant [`'\"\\\\]*(?:image_url|input_image|image)\\b|image_url is only supported by certain models|no endpoints found that support image|not a multi-?modal model|(?:does not|doesn't|do not|don't|cannot|can't|unable to) (?:support|accept|process|handle|take|read) (?:the )?(?:image|images|vision|multi-?modal)|\\b(?:image|images|vision|multi-?modal|image_url|input_image)(?: (?:input|inputs|content|parts?))? (?:is |are )?(?:not supported|unsupported|not enabled|not allowed)|不支持(?:图片|图像|多模态|视觉|识图)")

// imageRefused says a failure was the model turning the request's image
// away: what a describer fixes by describing it, not what another
// provider of the model should be sent instead (refuseSight).
func imageRefused(status int, body []byte) bool {
	switch status {
	case http.StatusBadRequest, http.StatusUnsupportedMediaType, http.StatusUnprocessableEntity:
	default:
		return false
	}
	return imageRefusedWords.Match(wordsOf(body))
}

// refuseSight records that pid's model turned an image away (Server.blind):
// each later image to it is described (blindTo) or, with nothing to
// describe it, sent only to a model that sees.
func (s *Server) refuseSight(pid, model string) { s.blind.Store(pid+"/"+model, true) }

// sightRefused is whether pid's model turned an image away (refuseSight).
func (s *Server) sightRefused(pid, model string) bool {
	_, ok := s.blind.Load(pid + "/" + model)
	return ok
}
