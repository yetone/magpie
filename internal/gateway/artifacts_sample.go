package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/provider"
)

// A page asking a model (docs/subsystems/artifacts.md, "Sample"): what
// claude.use("sample") sends, one round at a time, to <page>/_api/sample.
// Each round goes through magpie's own routing, as an agent's request
// does (routed, failed over, masked, counted in Usage under the agent
// "artifacts"), and its answer comes back to the page as NDJSON lines
// (sampleLine). A call with tools is several rounds: the page runs its
// tools between them and sends the results back (runtime.js).

const (
	// SampleAgent is the agent a page's calls are counted under in Usage.
	SampleAgent = "artifacts"
	// artifactSampleBody is the most one round may be: 256 KiB of text,
	// the tool rounds so far, and the images, downsized on the page
	artifactSampleBody = 48 << 20
	// artifactSampleText is the most text a call's turns may hold
	artifactSampleText = 256 << 10
	// how many images a call may carry, how large each may be once the
	// page has downsized it, and how large a file the page takes in
	artifactSampleImages     = 8
	artifactSampleImageBytes = 5 << 20
	artifactSampleImageInput = 20_000_000
	// how many tools a call may offer, and their limits
	artifactSampleTools      = 32
	artifactSampleToolDesc   = 1 << 10
	artifactSampleToolSchema = 4 << 10
	// how many rounds run at once for an artifact, how many more wait
	// their turn, and how many it makes in a minute; more is rate_limited
	artifactSampleRunning = 2
	artifactSampleWaiting = 6
	artifactSampleMost    = 60
	// a round's longest, after which what it wrote is the answer, cut short
	artifactSampleRound = 5 * time.Minute
	// the reply's limit, and how long an answer is replayed
	artifactSampleTokens = 16000
	artifactSampleGC     = 5 * time.Minute
	artifactSampleGCMost = 24 * time.Hour
	artifactSampleKept   = 256
)

// artifactSampleImageTypes are the images a call may carry.
var artifactSampleImageTypes = []string{"image/jpeg", "image/png", "image/webp", "image/gif"}

// sampleFraming is the system prompt of every round: what the model is
// answering, said of no product and no vendor, since it goes upstream
// (LESSONS.md: text magpie adds to a prompt names neither).
const (
	sampleFraming      = "Your reply is shown on an interactive web page to the person using it. The conversation is what the page sent: an earlier assistant turn is the page's record of an earlier reply."
	sampleFramingJSON  = " A program reads your reply as JSON: reply with only the JSON value asked for, with no other text."
	sampleFramingTools = " The tools are the page's own functions. Call them when they help; your last message, after any tool use, is the answer."
)

// sampleCall is one round, as runtime.js sends it.
type sampleCall struct {
	Token    string       `json:"t"`
	JSON     bool         `json:"json"`
	Messages []sampleTurn `json:"messages"`
	Tools    []sampleTool `json:"tools"`
	// Last: the call's last round, which answers rather than calls a tool
	Last   bool          `json:"last"`
	Tier   string        `json:"modelTier"`
	Model  string        `json:"model"`
	Effort string        `json:"effort"`
	Images []sampleImage `json:"images"`
	Cache  struct {
		Off     bool  `json:"off"`
		GC      int64 `json:"gcTime"`
		Refresh bool  `json:"refresh"`
	} `json:"cache"`
}

// sampleTurn is a turn: a string, or (in a call with tools) the blocks
// of a round's answer and of the tools' results.
type sampleTurn struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type sampleTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type sampleImage struct {
	Type string `json:"type"`
	Data string `json:"data"` // base64
}

// sampleError is a refusal the page reads, by the contract's code.
type sampleError struct {
	status    int
	code, msg string
}

func (e *sampleError) Error() string { return e.msg }

func sampleRefuse(status int, code, msg string) *sampleError {
	return &sampleError{status: status, code: code, msg: msg}
}

var (
	sampleToolName = lazyre.New(`^[A-Za-z0-9_-]{1,128}$`)
	sampleQuick    = lazyre.New(`(?i)haiku|flash|(^|[-_/ .:])(mini|nano|lite|small|fast|instant)($|[-_/ .:])`)
	sampleComplex  = lazyre.New(`(?i)opus|(^|[-_/ .:])pro($|[-_/ .:])|(^|[-_/ .:])max($|[-_/ .:])|ultra`)
	sampleBalanced = lazyre.New(`(?i)sonnet`)
	sampleClaude   = lazyre.New(`(?i)claude`)
)

// sampleTierOf is the tier a model counts as, by its name: Haiku and the
// other small ones quick, Opus and the other large ones complex.
func sampleTierOf(name string) string {
	switch {
	case sampleQuick.MatchString(name):
		return "quick"
	case sampleComplex.MatchString(name):
		return "complex"
	}
	return "default"
}

var sampleTierRank = map[string]int{"quick": 0, "default": 1, "complex": 2}

// sampleSpent: every account or key routing would try for e is out of
// its allowance (spentModel); a group's are its members', not told here.
func sampleSpent(e provider.Entry, now time.Time) bool {
	if e.Group != "" || e.Provider == nil {
		return false
	}
	_, spent := spentModel(*e.Provider, e.Model, provider.Anthropic, now)
	return spent
}

// sampleModel is the model a round goes to, the tier it counts as, and
// the model named in the call when that one answers: the named one
// while the catalog r is shown has it, else the tier's (by its name,
// Claude's first, one with allowance left before one out of it), else
// the first. With images, only a model that takes them; never one in
// tried. "" when none can.
func sampleModel(shown []provider.Entry, c *sampleCall, images bool, now time.Time, tried map[string]bool) (id, tier, named string) {
	var ok []provider.Entry
	for _, e := range shown {
		if (!images || e.Images) && !tried[e.ID] && (e.Provider == nil || !tried[sampleProviderKey(e.Provider.ID)]) {
			ok = append(ok, e)
		}
	}
	if len(ok) == 0 {
		return "", "", ""
	}
	want := c.Tier
	if want == "" || want == "inherit" {
		want = "default"
	}
	if m := c.Model; m != "" {
		for _, e := range ok {
			if (e.ID == m || e.Model == m || strings.HasSuffix(e.ID, "/"+m)) && !sampleSpent(e, now) {
				t := sampleTierOf(e.ID + " " + e.Model)
				if sampleTierRank[want] > sampleTierRank[t] {
					t = want
				}
				return e.ID, t, m
			}
		}
	}
	best, pick := -1, ok[0]
	for _, e := range ok {
		name := e.ID + " " + e.Model
		score := 0
		if !sampleSpent(e, now) {
			score += 8
		}
		if sampleTierOf(name) == want {
			score += 2
			if want == "default" && sampleBalanced.MatchString(name) {
				score += 2
			}
		}
		if sampleClaude.MatchString(name) {
			score++
		}
		if score > best {
			best, pick = score, e
		}
	}
	return pick.ID, sampleTierOf(pick.ID + " " + pick.Model), ""
}

// sampleRequest is the Messages request a round is: the turns checked,
// a turn's string its text, consecutive turns of one role as one, the
// images with the last turn the page wrote, the tools as Anthropic's.
func sampleRequest(c *sampleCall, model string) (map[string]any, *sampleError) {
	if len(c.Messages) == 0 {
		return nil, sampleRefuse(http.StatusBadRequest, "invalid_request", "the input is empty")
	}
	if c.Messages[0].Role != "user" || c.Messages[len(c.Messages)-1].Role != "user" {
		return nil, sampleRefuse(http.StatusBadRequest, "invalid_request", "the turns start and end on a user turn")
	}
	textBytes := 0
	var msgs []map[string]any
	lastText := -1 // the last turn the page wrote, as a string
	for i, t := range c.Messages {
		if t.Role != "user" && t.Role != "assistant" {
			return nil, sampleRefuse(http.StatusBadRequest, "invalid_request", fmt.Sprintf("turn %d's role is %q: a turn is user or assistant", i, t.Role))
		}
		var blocks []map[string]any
		var s string
		if json.Unmarshal(t.Content, &s) == nil {
			if strings.TrimSpace(s) == "" {
				return nil, sampleRefuse(http.StatusBadRequest, "invalid_request", fmt.Sprintf("turn %d is empty", i))
			}
			textBytes += len(s)
			blocks = []map[string]any{{"type": "text", "text": s}}
		} else if err := json.Unmarshal(t.Content, &blocks); err != nil || len(blocks) == 0 {
			return nil, sampleRefuse(http.StatusBadRequest, "invalid_request", fmt.Sprintf("turn %d's content is a string", i))
		} else if err := sampleBlocksOK(t.Role, blocks); err != nil {
			return nil, err
		}
		if n := len(msgs); n > 0 && msgs[n-1]["role"] == t.Role {
			// read as one turn
			prev := msgs[n-1]["content"].([]map[string]any)
			msgs[n-1]["content"] = append(prev, blocks...)
		} else {
			msgs = append(msgs, map[string]any{"role": t.Role, "content": blocks})
		}
		if s != "" && t.Role == "user" {
			lastText = len(msgs) - 1
		}
	}
	if textBytes > artifactSampleText {
		return nil, sampleRefuse(http.StatusRequestEntityTooLarge, "prompt_too_large", fmt.Sprintf("the input is %d KiB of text; a call takes at most %d KiB", textBytes>>10, artifactSampleText>>10))
	}
	if len(c.Images) > 0 {
		if lastText < 0 {
			return nil, sampleRefuse(http.StatusBadRequest, "invalid_request", "images go with a user turn the page wrote")
		}
		var imgs []map[string]any
		for _, im := range c.Images {
			imgs = append(imgs, map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": im.Type, "data": im.Data}})
		}
		content := msgs[lastText]["content"].([]map[string]any)
		msgs[lastText]["content"] = append(imgs, content...)
	}
	system := sampleFraming
	if c.JSON {
		system += sampleFramingJSON
	}
	req := map[string]any{"model": model, "max_tokens": artifactSampleTokens, "stream": true, "messages": msgs}
	if len(c.Tools) > 0 {
		system += sampleFramingTools
		names := map[string]bool{}
		var tools []map[string]any
		for i, t := range c.Tools {
			if !sampleToolName.MatchString(t.Name) || names[t.Name] {
				return nil, sampleRefuse(http.StatusBadRequest, "invalid_request", fmt.Sprintf("tools[%d]'s name is 1-128 of A-Z a-z 0-9 _ -, unique in the list", i))
			}
			names[t.Name] = true
			if strings.TrimSpace(t.Description) == "" || len(t.Description) > artifactSampleToolDesc {
				return nil, sampleRefuse(http.StatusBadRequest, "invalid_request", fmt.Sprintf("tools[%d] (%s) needs a description of at most 1 KB", i, t.Name))
			}
			schema := map[string]any{"type": "object", "properties": map[string]any{}}
			if s := bytes.TrimSpace(t.InputSchema); len(s) > 0 && string(s) != "null" {
				var v map[string]any
				if len(s) > artifactSampleToolSchema || json.Unmarshal(s, &v) != nil || v["type"] != "object" {
					return nil, sampleRefuse(http.StatusBadRequest, "invalid_request", fmt.Sprintf("tools[%d] (%s)'s inputSchema is a JSON Schema of type object, at most 4 KB", i, t.Name))
				}
				schema = v
			}
			tools = append(tools, map[string]any{"name": t.Name, "description": t.Description, "input_schema": schema})
		}
		req["tools"] = tools
		if c.Last {
			req["tool_choice"] = map[string]any{"type": "none"}
		}
	}
	req["system"] = system
	return req, nil
}

// sampleBlocksOK: a round's answer the page sends back (text, thinking,
// tool calls) or its tools' results, nothing else.
func sampleBlocksOK(role string, blocks []map[string]any) *sampleError {
	for _, b := range blocks {
		typ, _ := b["type"].(string)
		ok := false
		switch role {
		case "assistant":
			ok = slices.Contains([]string{"text", "thinking", "redacted_thinking", "tool_use"}, typ)
		case "user":
			ok = typ == "text" || typ == "tool_result"
			if typ == "tool_result" {
				if _, s := b["content"].(string); !s && b["content"] != nil {
					ok = false
				}
			}
		}
		if !ok {
			return sampleRefuse(http.StatusBadRequest, "invalid_request", fmt.Sprintf("a %s turn holds no %q block", role, typ))
		}
	}
	return nil
}

// sampleImagesOK: the images are of the types a call takes, each small
// enough once downsized, and no more of them than a call carries.
func sampleImagesOK(ims []sampleImage) *sampleError {
	if len(ims) > artifactSampleImages {
		return sampleRefuse(http.StatusBadRequest, "image_rejected", fmt.Sprintf("a call carries at most %d images", artifactSampleImages))
	}
	for i, im := range ims {
		if !slices.Contains(artifactSampleImageTypes, im.Type) {
			return sampleRefuse(http.StatusBadRequest, "image_rejected", fmt.Sprintf("image %d is %q; an image is JPEG, PNG, WebP or GIF", i, im.Type))
		}
		b, err := base64.StdEncoding.DecodeString(im.Data)
		if err != nil || len(b) == 0 {
			return sampleRefuse(http.StatusBadRequest, "image_rejected", fmt.Sprintf("image %d isn't one", i))
		}
		if len(b) > artifactSampleImageBytes {
			return sampleRefuse(http.StatusBadRequest, "image_rejected", fmt.Sprintf("image %d is over %d MiB once downsized", i, artifactSampleImageBytes>>20))
		}
	}
	return nil
}

// ---- the queue --------------------------------------------------------------

// sampleQueue lets artifactSampleRunning rounds of an artifact run at
// once and artifactSampleWaiting more wait their turn.
type sampleQueue struct {
	sync.Mutex
	m map[string]*sampleSlots
}

type sampleSlots struct {
	run     chan struct{}
	waiting int
}

var artifactSampleQueue = &sampleQueue{m: map[string]*sampleSlots{}}

// enter waits for a turn of artifact id's: release ends it. ok is false
// when too many wait already; err when ctx ends first.
func (q *sampleQueue) enter(ctx context.Context, id string) (release func(), ok bool, err error) {
	q.Lock()
	s := q.m[id]
	if s == nil {
		s = &sampleSlots{run: make(chan struct{}, artifactSampleRunning)}
		q.m[id] = s
	}
	select {
	case s.run <- struct{}{}:
		q.Unlock()
		return func() { <-s.run }, true, nil
	default:
	}
	if s.waiting >= artifactSampleWaiting {
		q.Unlock()
		return nil, false, nil
	}
	s.waiting++
	q.Unlock()
	defer func() {
		q.Lock()
		s.waiting--
		q.Unlock()
	}()
	select {
	case s.run <- struct{}{}:
		return func() { <-s.run }, true, nil
	case <-ctx.Done():
		return nil, true, ctx.Err()
	}
}

var artifactSamples = newMinuteLimit(artifactSampleMost)

// ---- the cache --------------------------------------------------------------

// sampleAnswer is an answer kept to replay to the same call.
type sampleAnswer struct {
	text      string
	truncated bool
	tier      string
	named     string
	at        time.Time
	gc        time.Duration
}

var sampleCache = struct {
	sync.Mutex
	m map[string]*sampleAnswer
}{m: map[string]*sampleAnswer{}}

// sampleKey is what makes two calls the same: the artifact, the verb,
// every turn, the tier, the model, the effort and the images.
func sampleKey(id string, c *sampleCall) string {
	h := sha256.New()
	b, _ := json.Marshal([]any{id, c.JSON, c.Messages, c.Tier, c.Model, c.Effort, c.Images})
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}

// sampleGC is how long a call's answer may be replayed: its gcTime, at
// most a day, else five minutes.
func sampleGC(c *sampleCall) time.Duration {
	if c.Cache.GC <= 0 {
		return artifactSampleGC
	}
	return min(time.Duration(c.Cache.GC)*time.Millisecond, artifactSampleGCMost)
}

// sampleCached is the answer kept for key, while it is younger than the
// window it was stored with and the one asking now.
func sampleCached(key string, gc time.Duration, now time.Time) *sampleAnswer {
	sampleCache.Lock()
	defer sampleCache.Unlock()
	a := sampleCache.m[key]
	if a == nil || now.Sub(a.at) >= min(a.gc, gc) {
		return nil
	}
	return a
}

func sampleKeep(key string, a *sampleAnswer) {
	sampleCache.Lock()
	defer sampleCache.Unlock()
	if len(sampleCache.m) >= artifactSampleKept {
		var oldest string
		for k, v := range sampleCache.m {
			if a.at.Sub(v.at) >= v.gc {
				delete(sampleCache.m, k)
				continue
			}
			if oldest == "" || v.at.Before(sampleCache.m[oldest].at) {
				oldest = k
			}
		}
		if len(sampleCache.m) >= artifactSampleKept && oldest != "" {
			delete(sampleCache.m, oldest)
		}
	}
	sampleCache.m[key] = a
}

var sampleFence = lazyre.New("(?s)```[A-Za-z0-9_-]*\\s*\\n(.*?)\\n?```")

// sampleJSONOK is whether text holds the one JSON value sample.json
// reads (runtime.js's parseJSON): the whole text, else one fence's
// body, else from the first { or [ to the last } or ].
func sampleJSONOK(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" {
		return false
	}
	if json.Valid([]byte(t)) {
		return true
	}
	if m := sampleFence.Regexp().FindAllStringSubmatch(t, 2); len(m) == 1 && json.Valid([]byte(strings.TrimSpace(m[0][1]))) {
		return true
	}
	i, j := strings.IndexAny(t, "{["), strings.LastIndexAny(t, "}]")
	return i >= 0 && j > i && json.Valid([]byte(t[i:j+1]))
}

// ---- the round --------------------------------------------------------------

// sampleLine is one line of a round's answer: a piece of its text, its
// end (why it stopped, the tier and model that answered, and the blocks
// the page sends back in a call with tools), or why it failed.
type sampleLine struct {
	Text  string         `json:"text,omitempty"`
	End   *sampleEnd     `json:"end,omitempty"`
	Error *sampleLineErr `json:"error,omitempty"`
}

type sampleEnd struct {
	Stop      string           `json:"stop"`
	Truncated bool             `json:"truncated"`
	Tier      string           `json:"tier"`
	Model     string           `json:"model,omitempty"`
	Content   []map[string]any `json:"content,omitempty"`
	Cached    bool             `json:"cached,omitempty"`
}

type sampleLineErr struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Reason  string `json:"reason,omitempty"`
}

// artifactSample runs one round of a page's sample call. The page is
// the owner's, on this computer, as every artifact here is, so no
// consent is asked: what bounds it is the queue, the minute's rounds,
// and Usage, where every round shows under SampleAgent.
func (s *Server) artifactSample(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, artifactSampleBody+1))
	var c sampleCall
	if err == nil && len(body) <= artifactSampleBody {
		err = json.Unmarshal(body, &c)
	}
	if err != nil || len(body) > artifactSampleBody {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if len(body) > artifactSampleBody {
			artifactAPIError(w, http.StatusRequestEntityTooLarge, "prompt_too_large", fmt.Sprintf("a call is at most %d MiB, its images too", artifactSampleBody>>20))
			return
		}
		artifactAPIError(w, http.StatusBadRequest, "transform_error", "the call isn't JSON")
		return
	}
	a, n, ok := artifactAPIOpen(w, r, c.Token, "sample")
	if !ok {
		return
	}
	if len(c.Images) > 0 {
		if v := a.Version(n); v == nil || !v.Capabilities.SampleImages() {
			artifactAPIError(w, http.StatusForbidden, "images_unavailable", "this version of the page doesn't declare {sample: {images: true}}")
			return
		}
		if e := sampleImagesOK(c.Images); e != nil {
			artifactAPIError(w, e.status, e.code, e.msg)
			return
		}
	}
	if c.Tier != "" && !slices.Contains([]string{"default", "complex", "quick", "inherit"}, c.Tier) {
		artifactAPIError(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("modelTier is default, complex or quick, not %q", c.Tier))
		return
	}
	if !slices.Contains([]string{"low", "medium", "high"}, c.Effort) {
		c.Effort = ""
	}
	if len(c.Tools) > artifactSampleTools {
		artifactAPIError(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("a call offers at most %d tools", artifactSampleTools))
		return
	}
	now := time.Now()
	caches := len(c.Tools) == 0 && !c.Cache.Off
	key := ""
	if caches {
		key = sampleKey(a.ID, &c)
		if !c.Cache.Refresh {
			if got := sampleCached(key, sampleGC(&c), now); got != nil {
				sampleReplay(w, got)
				return
			}
		}
	}
	shown := catalogFor(sampleInner(r, nil))
	tried := map[string]bool{}
	model, tier, named := sampleModel(shown, &c, len(c.Images) > 0, now, tried)
	if model == "" {
		if len(c.Images) > 0 {
			artifactAPIError(w, http.StatusServiceUnavailable, "images_unavailable", "none of the models enabled in magpie takes images")
			return
		}
		artifactAPIError(w, http.StatusServiceUnavailable, "sampling_disabled", "magpie has no model to answer with: add a provider in magpie first")
		return
	}
	req, e := sampleRequest(&c, model)
	if e != nil {
		artifactAPIError(w, e.status, e.code, e.msg)
		return
	}
	if !artifactSamples.allow(a.ID, now) {
		artifactAPIError(w, http.StatusTooManyRequests, "rate_limited", fmt.Sprintf("a page asks at most %d times a minute", artifactSampleMost))
		return
	}
	release, ok, err := artifactSampleQueue.enter(r.Context(), a.ID)
	if err != nil {
		return // the page went away while it waited
	}
	if !ok {
		artifactAPIError(w, http.StatusTooManyRequests, "rate_limited", fmt.Sprintf("%d calls of this page are running and %d waiting", artifactSampleRunning, artifactSampleWaiting))
		return
	}
	defer release()
	var got, first *sampleRoundResult
	tries := 0
	for {
		raw, _ := json.Marshal(req)
		raw = WithSideEffort(raw, model, c.Effort)
		got = s.sampleRound(w, r, raw)
		if got == nil {
			return
		}
		if got.failed == nil {
			break
		}
		// it failed before a word: an account or model that can't
		// answer (a dead key, a spent plan, a vendor down) gives the
		// call to the next model, a few at most; what the request
		// itself got wrong goes to the page
		tried[model] = true
		tries++
		if first == nil {
			first = got
		}
		if got.status == http.StatusUnauthorized || got.status == http.StatusPaymentRequired || got.status == http.StatusForbidden {
			// the key or the account: its other models can't answer either
			for _, e := range shown {
				if e.ID == model && e.Provider != nil {
					tried[sampleProviderKey(e.Provider.ID)] = true
				}
			}
		}
		next := ""
		if sampleTryNext(got.status, got.failed.Code) && tries < artifactSampleModels {
			next, tier, named = sampleModel(shown, &c, len(c.Images) > 0, now, tried)
		}
		if next == "" {
			// the first model's reason: the one the call was meant for
			sampleFail(w, first.status, first.failed)
			return
		}
		model = next
		if req, e = sampleRequest(&c, model); e != nil {
			artifactAPIError(w, e.status, e.code, e.msg)
			return
		}
	}
	got.Tier, got.Model = tier, named
	if got.Model != "" {
		// the tier the call counted as: the named model's, or the one
		// asked for if higher
		if t := c.Tier; sampleTierRank[t] > sampleTierRank[got.Tier] {
			got.Tier = t
		}
	}
	text := got.text
	end := sampleEnd{Stop: got.stop, Truncated: got.truncated, Tier: got.Tier, Model: got.Model, Content: got.content}
	sampleWrite(w, sampleLine{End: &end})
	if caches && got.stop != "refusal" && strings.TrimSpace(text) != "" && (!c.JSON || !got.truncated && sampleJSONOK(text)) {
		sampleKeep(key, &sampleAnswer{text: text, truncated: got.truncated, tier: got.Tier, named: got.Model, at: time.Now(), gc: sampleGC(&c)})
	}
}

// sampleReplay answers with a kept answer: its text whole, then its end.
func sampleReplay(w http.ResponseWriter, a *sampleAnswer) {
	sampleStart(w)
	sampleWrite(w, sampleLine{Text: a.text})
	sampleWrite(w, sampleLine{End: &sampleEnd{Stop: "end_turn", Truncated: a.truncated, Tier: a.tier, Model: a.named, Cached: true,
		Content: []map[string]any{{"type": "text", "text": a.text}}}})
}

func sampleStart(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "application/x-ndjson")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
}

// sampleWrite sends one line and flushes it; false when the page is gone.
func sampleWrite(w http.ResponseWriter, l sampleLine) bool {
	b, _ := json.Marshal(l)
	if _, err := w.Write(append(b, '\n')); err != nil {
		return false
	}
	http.NewResponseController(w).Flush()
	return true
}

// sampleInner is a round's request into the gateway's own handler, as
// an agent's from this computer is, under SampleAgent's token: none of
// the page's own headers go with it, nor the caller withCaller read from
// them, whose agent and User-Agent are the page's browser's ("Mozilla"
// in Usage, and the browser's User-Agent to a provider that is sent the
// agent's, until a real Pi run's page showed it).
func sampleInner(r *http.Request, body []byte) *http.Request {
	in, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, "/v1/messages", bytes.NewReader(body))
	in.RemoteAddr = "127.0.0.1:0"
	in.Host = r.Host
	in.RequestURI = "/v1/messages"
	in.Pattern = "POST /v1/messages"
	in.Header.Set("Content-Type", "application/json")
	in.Header.Set("Accept", "text/event-stream")
	in.Header.Set("anthropic-version", "2023-06-01")
	in.Header.Set("x-api-key", TokenFor(SampleAgent))
	return in.WithContext(context.WithValue(in.Context(), callerCtx{}, callerFrom(in)))
}

// sampleRoundResult is what a round wrote.
type sampleRoundResult struct {
	text, stop, Tier, Model string
	truncated               bool
	content                 []map[string]any
	// failed before anything was written: the page hasn't been told
	failed *sampleLineErr
	status int
}

// artifactSampleModels is how many models one call tries.
const artifactSampleModels = 3

// sampleProviderKey marks a provider tried, beside the model ids.
func sampleProviderKey(id string) string { return "\x00provider:" + id }

// sampleTryNext: a round that failed so could go to another model: the
// account or the model refused or is down, not the request.
func sampleTryNext(status int, code string) bool {
	switch {
	case code == "prompt_too_large" || code == "image_rejected" || code == "invalid_request":
		return false
	case status == http.StatusGatewayTimeout:
		return false // the round's time is spent
	}
	return status == http.StatusUnauthorized || status == http.StatusPaymentRequired || status == http.StatusForbidden ||
		status == http.StatusNotFound || status == http.StatusTooManyRequests || status >= 500
}

// sampleFail tells the page a call failed before a word of it.
func sampleFail(w http.ResponseWriter, status int, le *sampleLineErr) {
	if status < 400 {
		status = http.StatusBadGateway
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"error": le})
}

// sampleRound serves body through the gateway's Messages handler and
// streams its text to the page as it comes. nil when it failed, which
// the page has been told, or the page went away.
func (s *Server) sampleRound(w http.ResponseWriter, r *http.Request, body []byte) *sampleRoundResult {
	ctx, cancel := context.WithTimeout(r.Context(), artifactSampleRound)
	defer cancel()
	in := sampleInner(r.WithContext(ctx), body)
	type block struct {
		start map[string]any
		text  strings.Builder
		args  strings.Builder
		sig   string
	}
	blocks := map[int]*block{}
	var order []*block
	var res sampleRoundResult
	var all strings.Builder
	started, gone := false, false
	var errMsg, errType string
	tap := &sseTap{h: http.Header{}, on: func(name string, data []byte) bool {
		var ev map[string]any
		if json.Unmarshal(data, &ev) != nil {
			return true
		}
		idx := -1
		if f, ok := ev["index"].(float64); ok {
			idx = int(f)
		}
		switch name {
		case "content_block_start":
			cb, _ := ev["content_block"].(map[string]any)
			b := &block{start: cb}
			blocks[idx] = b
			order = append(order, b)
		case "content_block_delta":
			b := blocks[idx]
			if b == nil {
				return true
			}
			d, _ := ev["delta"].(map[string]any)
			switch d["type"] {
			case "text_delta":
				t, _ := d["text"].(string)
				if t == "" {
					return true
				}
				b.text.WriteString(t)
				all.WriteString(t)
				if !started {
					sampleStart(w)
					started = true
				}
				if !sampleWrite(w, sampleLine{Text: t}) {
					gone = true
					return false
				}
			case "thinking_delta":
				t, _ := d["thinking"].(string)
				b.text.WriteString(t)
			case "signature_delta":
				t, _ := d["signature"].(string)
				b.sig += t
			case "input_json_delta":
				t, _ := d["partial_json"].(string)
				b.args.WriteString(t)
			}
		case "message_delta":
			d, _ := ev["delta"].(map[string]any)
			if t, _ := d["stop_reason"].(string); t != "" {
				res.stop = t
			}
		case "error":
			e, _ := ev["error"].(map[string]any)
			errMsg, _ = e["message"].(string)
			errType, _ = e["type"].(string)
			if errMsg == "" {
				errMsg = string(data)
			}
		}
		return true
	}}
	keepsSignIn(s.handle(provider.Anthropic))(tap, in)
	if gone || r.Context().Err() != nil {
		return nil
	}
	timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)
	fail := func(status int, msg string) *sampleRoundResult {
		code := sampleCode(status, errType, msg)
		le := &sampleLineErr{Code: code, Message: msg}
		if code == "rate_limited" && sampleUsageLimit(msg) {
			le.Reason = "usage_limit"
		}
		if !started {
			return &sampleRoundResult{failed: le, status: status}
		}
		sampleWrite(w, sampleLine{Error: le})
		return nil
	}
	switch {
	case timedOut && started:
		// the round's time is up: what it wrote is the answer, cut short
		res.stop, res.truncated = "max_tokens", true
	case timedOut:
		return fail(http.StatusGatewayTimeout, "the model wrote nothing in "+artifactSampleRound.String())
	case tap.status != 0 && tap.status != http.StatusOK || !tap.sse:
		msg := tap.body.String()
		var e struct {
			Error struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(tap.body.Bytes(), &e) == nil && e.Error.Message != "" {
			msg, errType = e.Error.Message, e.Error.Type
		}
		status := tap.status
		if status == 0 || status == http.StatusOK {
			status = http.StatusBadGateway
		}
		return fail(status, msg)
	case errMsg != "":
		status := http.StatusBadGateway
		if errType == "rate_limit_error" {
			status = http.StatusTooManyRequests
		}
		return fail(status, errMsg)
	}
	if res.stop == "max_tokens" {
		res.truncated = true
	}
	if !started {
		sampleStart(w)
	}
	for _, b := range order {
		switch b.start["type"] {
		case "text":
			if b.text.Len() > 0 {
				res.content = append(res.content, map[string]any{"type": "text", "text": b.text.String()})
			}
		case "thinking":
			res.content = append(res.content, map[string]any{"type": "thinking", "thinking": b.text.String(), "signature": b.sig})
		case "redacted_thinking":
			res.content = append(res.content, b.start)
		case "tool_use":
			input := map[string]any{}
			json.Unmarshal([]byte(b.args.String()), &input)
			if len(input) == 0 {
				if v, ok := b.start["input"].(map[string]any); ok {
					input = v
				}
			}
			res.content = append(res.content, map[string]any{"type": "tool_use", "id": b.start["id"], "name": b.start["name"], "input": input})
		}
	}
	res.text = all.String()
	return &res
}

// sampleCode is the contract's code for a failed round.
func sampleCode(status int, typ, msg string) string {
	l := strings.ToLower(msg)
	switch {
	case status == http.StatusTooManyRequests || typ == "rate_limit_error":
		return "rate_limited"
	case status == http.StatusRequestEntityTooLarge || strings.Contains(l, "prompt is too long") || strings.Contains(l, "context length") || strings.Contains(l, "context window") || strings.Contains(l, "too many tokens"):
		return "prompt_too_large"
	case status == http.StatusBadRequest && strings.Contains(l, "image"):
		return "image_rejected"
	case status == http.StatusBadRequest && (strings.Contains(l, "tool") || strings.Contains(l, "schema")):
		return "invalid_request"
	}
	return "upstream_error"
}

// sampleUsageLimit: a 429 that is the account's allowance, not a flood.
func sampleUsageLimit(msg string) bool {
	l := strings.ToLower(msg)
	return strings.Contains(l, "usage") || strings.Contains(l, "quota") || strings.Contains(l, "limit reached") || strings.Contains(l, "credit")
}
