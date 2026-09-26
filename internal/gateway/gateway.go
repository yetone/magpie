package gateway

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// DefaultAddr is where the gateway listens unless MAGPIE_ADDR says otherwise.
const DefaultAddr = "127.0.0.1:3425"

// Token is the bearer token agents are told to use. The gateway only
// listens on loopback and accepts anything, but agents insist on one.
const Token = "magpie"

// Version is set by main.
var Version = "dev"

// Addr is the listen address.
func Addr() string {
	if a := os.Getenv("MAGPIE_ADDR"); a != "" {
		return a
	}
	return DefaultAddr
}

// URL is the base URL agents use, e.g. http://127.0.0.1:3425.
func URL() string { return "http://" + Addr() }

// Running reports whether a gateway answers at the address.
func Running() bool {
	c := &http.Client{Timeout: 700 * time.Millisecond}
	res, err := c.Get(URL() + "/")
	if err != nil {
		return false
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	return bytes.Contains(b, []byte(`"magpie"`))
}

// Call is one request the gateway handled, for the status views.
type Call struct {
	Time              time.Time         `json:"time"`
	Agent             string            `json:"agent"` // who called, from the client's User-Agent
	Model             string            `json:"model"`
	Provider          string            `json:"provider"`
	From              provider.Protocol `json:"from"`
	To                provider.Protocol `json:"to"`
	Status            int               `json:"status"`
	Millis            int64             `json:"ms"`
	Error             string            `json:"error,omitempty"`
	Fallback          string            `json:"fallback,omitempty"` // providers that failed first, and why
	Usage             Usage             `json:"usage"`
	RequestBody       string            `json:"requestBody,omitempty"`
	ResponseBody      string            `json:"responseBody,omitempty"`
	RequestTruncated  bool              `json:"requestTruncated,omitempty"`
	ResponseTruncated bool              `json:"responseTruncated,omitempty"`
}

// Server is the gateway.
type Server struct {
	client *http.Client
	mu     sync.Mutex
	recent []Call
	// unfit remembers the endpoints each provider's models were turned away
	// from, so every later turn goes straight to one that takes them.
	unfit        map[string]bool
	subscription *subscriptionBridge
	debug        bool
	trace        trace // what routing did with each request, for the Gateway view
}

// New makes a gateway.
func New() *Server {
	return &Server{
		client: &http.Client{Transport: &http.Transport{
			Proxy:                 netproxy.Func,
			ResponseHeaderTimeout: 10 * time.Minute,
			MaxIdleConnsPerHost:   8,
			IdleConnTimeout:       90 * time.Second,
			ForceAttemptHTTP2:     true,
		}},
		unfit:        make(map[string]bool),
		subscription: newSubscriptionBridge(),
		debug:        os.Getenv("MAGPIE_DEBUG") != "",
	}
}

// Recent lists the last calls, newest first.
func (s *Server) Recent() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Call, len(s.recent))
	for i, c := range s.recent {
		out[len(s.recent)-1-i] = c
	}
	return out
}

func (s *Server) record(c Call) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recent = append(s.recent, c)
	if len(s.recent) > 40 {
		s.recent = s.recent[len(s.recent)-40:]
	}
	if s.debug {
		log.Printf("%s %s → %s (%s→%s) %d %dms %s", c.Model, c.Provider, c.Provider, c.From, c.To, c.Status, c.Millis, c.Error)
	}
}

// WhileServing is work only the magpie serving the gateway does, begun
// once it is bound and ended with it: one of the magpies running, never
// two at once.
var WhileServing []func(context.Context)

// ListenAndServe runs the gateway until ctx ends. A bind error means
// another magpie is already serving, which is fine for the caller to ignore.
func (s *Server) ListenAndServe(ctx context.Context) error {
	ln, err := net.Listen("tcp", Addr())
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 30 * time.Second, IdleTimeout: 5 * time.Minute}
	// the magpie serving the gateway, and only it, keeps the saved accounts
	// signed in, so two never refresh one sign-in at once
	go provider.KeepLoginsAlive(ctx)
	for _, f := range WhileServing {
		go f(ctx)
	}
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		srv.Shutdown(c)
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Handler routes the client APIs.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.info)
	mux.HandleFunc("GET /v1/models", s.models)
	mux.HandleFunc("GET /models", s.models)
	mux.HandleFunc("GET /v1/models/{id}", s.model)
	mux.HandleFunc("GET /v1/magpie/quotas", s.quotas)
	mux.HandleFunc("POST /v1/chat/completions", s.handle(provider.Chat))
	mux.HandleFunc("POST /chat/completions", s.handle(provider.Chat))
	mux.HandleFunc("POST /v1/responses", s.handle(provider.Responses))
	mux.HandleFunc("POST /responses", s.handle(provider.Responses))
	mux.HandleFunc("POST /v1/messages", s.handle(provider.Anthropic))
	mux.HandleFunc("POST /messages", s.handle(provider.Anthropic))
	mux.HandleFunc("POST /v1/messages/count_tokens", s.countTokens)
	mux.HandleFunc("POST /_magpie/claude-mcp/{token}", s.subscription.mcpCall)
	mux.HandleFunc(CodexPath+"/", s.codexBackend)
	mux.HandleFunc("GET /v1beta/models", s.geminiModels)
	mux.HandleFunc("POST /v1beta/models/{call...}", s.gemini)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, provider.Chat, http.StatusNotFound, "magpie serves /v1/chat/completions, /v1/responses, /v1/messages and /v1beta/models/*")
	})
	return mux
}

func (s *Server) info(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"name": "magpie", "version": Version, "models": len(provider.Catalog()),
		"apis": []string{"/v1/chat/completions", "/v1/responses", "/v1/messages", "/v1beta/models/{model}:generateContent", "/v1/magpie/quotas"}})
}

// quotas is what is left of every subscription, plan and key magpie has,
// for an agent choosing where to send its work (magpie quota --json is the
// same). It names the accounts and their balances, so it answers only on
// this machine, when the gateway listens beyond it too.
func (s *Server) quotas(w http.ResponseWriter, r *http.Request) {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		writeError(w, provider.Chat, http.StatusForbidden, "magpie's quotas are only told to this machine")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	writeJSON(w, 200, map[string]any{"object": "list", "data": provider.QuotaReport(ctx, time.Now())})
}

func modelObject(e provider.Entry) map[string]any {
	type reasoningLevel struct {
		Effort string `json:"effort"`
	}
	levels := make([]reasoningLevel, 0, len(e.Efforts))
	for _, effort := range e.Efforts {
		levels = append(levels, reasoningLevel{Effort: effort})
	}
	return map[string]any{"id": e.ID, "object": "model", "type": "model", "created": 0, "created_at": "2025-01-01T00:00:00Z",
		"owned_by": e.Provider.ID, "display_name": e.Name, "reasoning": len(levels) > 0, "supported_reasoning_levels": levels}
}

// catalogFor is the catalog as the agent asking is shown it.
func catalogFor(r *http.Request) []provider.Entry {
	shown, _ := provider.CatalogFor(usage.AgentOf(r.Header.Get("User-Agent")))
	return shown
}

func (s *Server) models(w http.ResponseWriter, r *http.Request) {
	data := []map[string]any{}
	for _, e := range catalogFor(r) {
		data = append(data, modelObject(e))
	}
	out := map[string]any{"object": "list", "data": data, "has_more": false}
	if len(data) > 0 {
		out["first_id"], out["last_id"] = data[0]["id"], data[len(data)-1]["id"]
	}
	writeJSON(w, 200, out)
}

func (s *Server) model(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	for _, e := range provider.Catalog() {
		if e.ID == id {
			writeJSON(w, 200, modelObject(e))
			return
		}
	}
	writeError(w, provider.Chat, 404, "unknown model "+id)
}

// countTokens answers Anthropic's count_tokens: through the provider when
// it speaks Anthropic, else a rough estimate.
func (s *Server) countTokens(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		writeError(w, provider.Anthropic, 400, err.Error())
		return
	}
	p, model, ok := provider.Resolve(modelOf(body))
	// Claude Subscription generations run through the Claude Code binary. Its
	// OAuth token must not take a direct HTTP side path just for token counting.
	if ok && p.Account != nil && (p.Account.Agent == "claude" || p.Account.Agent == "cursor" || p.Account.Agent == "grok" || p.Account.Agent == "devin" || p.Account.Agent == "gemini" || p.Account.Agent == "antigravity") {
		req, err := parseAnthropic(body)
		if err != nil {
			writeError(w, provider.Anthropic, 400, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"input_tokens": estimate(req)})
		return
	}
	if ok {
		// a relay's OpenAI-only key can't count Anthropic tokens
		q := p
		q.Anthropic = ""
		for _, c := range perKey(p, model, provider.Anthropic) {
			if c.p.Anthropic != "" {
				q = c.p
				break
			}
		}
		p = q
	}
	if ok && p.Anthropic != "" && slices.Contains(s.usable(p, model), provider.Anthropic) {
		res, err := s.forward(r.Context(), p, provider.Anthropic, "/v1/messages/count_tokens", rewriteModel(body, model), r.Header)
		if err == nil {
			defer res.Body.Close()
			if res.StatusCode < 400 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(res.StatusCode)
				io.Copy(w, res.Body)
				return
			}
		}
	}
	req, err := parseAnthropic(body)
	if err != nil {
		writeError(w, provider.Anthropic, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"input_tokens": estimate(req)})
}

// handle is the request path of one client API.
func (s *Server) handle(from provider.Protocol) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
		if err != nil {
			writeError(w, from, 400, err.Error())
			return
		}
		s.serve(w, r, from, body)
	}
}

// gemini serves /v1beta/models/{model}:{method}. The Gemini API keeps the
// model and the streaming choice in the URL; they move into the body so
// serve sees one shape.
func (s *Server) gemini(w http.ResponseWriter, r *http.Request) {
	call := r.PathValue("call")
	i := strings.LastIndex(call, ":") // model ids may hold a colon (ollama tags), methods never do
	if i < 0 {
		writeError(w, provider.Gemini, 404, "expected /v1beta/models/{model}:generateContent")
		return
	}
	model, method := call[:i], call[i+1:]
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		writeError(w, provider.Gemini, 400, err.Error())
		return
	}
	var stream bool
	switch method {
	case "generateContent":
	case "streamGenerateContent":
		stream = true
	case "countTokens":
		s.geminiCount(w, model, body)
		return
	default:
		writeError(w, provider.Gemini, 404, "unknown method "+method)
		return
	}
	s.serve(w, r, provider.Gemini, withFields(body, map[string]any{"model": model, "stream": stream}))
}

// geminiCount answers countTokens with a rough estimate.
func (s *Server) geminiCount(w http.ResponseWriter, model string, body []byte) {
	var wrap struct {
		Inner json.RawMessage `json:"generateContentRequest"`
	}
	if json.Unmarshal(body, &wrap) == nil && len(wrap.Inner) > 0 {
		body = wrap.Inner
	}
	req, err := parseGemini(body)
	if err != nil {
		writeError(w, provider.Gemini, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"totalTokens": estimate(req)})
}

func (s *Server) geminiModels(w http.ResponseWriter, r *http.Request) {
	models := []map[string]any{}
	for _, e := range catalogFor(r) {
		models = append(models, geminiModel(e.ID, e.Name))
	}
	writeJSON(w, 200, map[string]any{"models": models})
}

// estimate is a token count from sizes, for clients that ask before sending.
func estimate(req *Request) int {
	n := len(req.System)
	for _, m := range req.Messages {
		for _, p := range m.Parts {
			n += len(p.Text) + len(p.Args) + len(p.Name)
		}
	}
	for _, t := range req.Tools {
		n += len(t.Name) + len(t.Description) + len(t.Schema)
	}
	return n / 4
}

// serve routes one parsed-enough request to its provider.
func (s *Server) serve(w http.ResponseWriter, r *http.Request, from provider.Protocol, body []byte) {
	start := time.Now()
	// secrets go as placeholders and come back as they were; the log has
	// what the vendor saw and said
	w, body, unmask := redacted(w, body)
	defer unmask()
	requestBody, requestTruncated := captureRequestBody(body)
	capture := &captureResponseWriter{ResponseWriter: w}
	w = capture
	call := Call{Time: start, From: from, Model: modelOf(body), Agent: usage.AgentOf(r.Header.Get("User-Agent")),
		RequestBody: requestBody, RequestTruncated: requestTruncated}
	usage.Saw(call.Agent)
	finishCapture := func() {
		call.ResponseBody = capture.body.text()
		call.ResponseTruncated = capture.body.truncated
	}
	p, model, ok := provider.Resolve(call.Model)
	if !ok {
		call.Status, call.Error = 404, "unknown model"
		msg := fmt.Sprintf("magpie knows no model %q", call.Model)
		if ids := provider.IDs(); len(ids) > 0 {
			msg += "; it has " + strings.Join(ids, ", ")
		} else {
			msg += "; add a provider in magpie first"
		}
		writeError(w, from, 404, msg)
		finishCapture()
		s.record(call)
		return
	}
	// a routing group's rules pick the member that goes first, looked at
	// before any image is taken out of the request: one may be for images
	g, ms, isGroup := provider.FindGroup(call.Model)
	var hit *RuleHit
	var ruled []provider.Member
	var ruleAt, words string
	var ruleReq *Request // parsed for the rules of the group or a group in it
	if isGroup && slices.ContainsFunc(ms, func(m provider.Member) bool {
		return len(g.Rules) > 0 || slices.ContainsFunc(m.Via, func(v provider.Group) bool { return len(v.Rules) > 0 })
	}) {
		if req, err := parse(from, body); err == nil {
			ruleReq, ruleAt, words = req, ruleKey(g, r.Header, req), firstWords(req)
		}
	}
	if ruleReq != nil && len(g.Rules) > 0 {
		hit = ruleFor(ruleAt, g, ms, ruleReq, call.Agent, s.askClassifier)
		ruled = ruleMembers(hit, ms)
	}
	// Some clients send images even when the selected model is known to
	// accept text only. Reject a new image and omit images from history.
	var imageInput *bool
	if isGroup {
		// the member a rule put first, or what every member takes
		imageInput = membersImageInput(ms, ruled)
	} else {
		for _, m := range p.Available() {
			if m.ID == model {
				imageInput = m.ImageInput
				break
			}
		}
	}
	if imageInput != nil && !*imageInput {
		var currentImage bool
		body, currentImage = textOnlyBody(from, body)
		if req, err := parse(from, body); err == nil && !currentImage {
			for _, msg := range req.Messages {
				if slices.ContainsFunc(msg.Parts, func(part Part) bool { return part.Kind == Image }) {
					currentImage = true
					break
				}
			}
		}
		if currentImage {
			call.Status, call.Error = 400, "model does not support image input"
			writeError(w, from, 400, fmt.Sprintf("model %q does not support image input", call.Model))
			finishCapture()
			s.record(call)
			return
		}
	}
	// the primary, then its fallbacks while it can't take the request and
	// nothing has been sent yet
	var cands []candidate
	var pl planned
	var group *GroupRef
	// a conversation's affinity is to one model: another's cache is its
	// own, and an agent's side requests (a title, a summary) to a smaller
	// model leave the conversation where it is
	scope, mode, rotate := p.ID+"/"+model, p.Affinity, p.Routing == provider.Rotate
	if isGroup {
		// a routing group: every member's keys or accounts weighed together
		cands, pl = s.planGroup(g, ms, from)
		group = groupRef(g, ms)
		scope, mode, rotate = provider.GroupPrefix+g.ID, g.Affinity, g.Routing == provider.Rotate
		if words != "" {
			// a subagent a rule sends elsewhere mustn't take its agent's
			// conversation with it: each keeps where it is on its own
			scope += "|" + words
		}
	} else {
		cands, pl = s.plan(p, model, from)
	}
	if len(cands) == 0 {
		call.Status, call.Error = 404, "no member ready"
		writeError(w, from, 404, fmt.Sprintf("none of %s's models is ready", call.Model))
		finishCapture()
		s.record(call)
		return
	}
	// the conversation stays with who answered it last, while its
	// affinity says: what the vendor cached of it is read again. Who
	// answered is remembered with only one to route to as well, so that a
	// key or account added while the conversation runs doesn't take it over
	// (#63): its reasoning, sealed by the account that wrote it, is refused
	// by another.
	cands, pl, aff, stuck := affine(scope, mode, rotate, r.Header, from, body, cands, pl)
	if hit != nil && hit.Use != "" && !(hit.Held && aff.Kept) {
		// the rule's member first, over whoever answered last, as a turn
		// begins; within it, whoever answered last stays — the rule's
		// member, or who took over when it failed
		was := cands[0]
		ok := false
		if len(ruled) > 0 {
			cands, pl, ok = ruleFirst(ruled, cands, pl)
		}
		hit.Unready = !ok
		if ok && aff.Kept && cands[0].rest != was.rest {
			aff.Kept, aff.Why = false, "rule"
		}
	}
	// the rules of the groups in the group, down the one that goes first
	var nested []NestedRule
	var nestedAt []string
	if ruleReq != nil {
		nested, nestedAt, cands, pl = s.nestedRules(ruleAt, ruleReq, call.Agent, ms, cands, pl, aff)
	}
	// A vision rule may choose a vision model even when the group has
	// text-only fallbacks. A failure must not send a current user image to
	// one of them. Historical images and tool results are omitted per
	// candidate below, so a text-only fallback can still answer those.
	if isGroup {
		_, currentImage := textOnlyBody(from, body)
		if currentImage {
			var kept []candidate
			var order []Weighed
			for i, c := range cands {
				in := membersImageInput([]provider.Member{{Provider: c.p, Model: c.model}}, nil)
				if in != nil && !*in {
					continue
				}
				kept = append(kept, c)
				order = append(order, pl.order[i])
			}
			cands, pl.order = kept, order
			if len(cands) == 0 {
				call.Status, call.Error = 400, "model does not support image input"
				writeError(w, from, 400, fmt.Sprintf("model %q does not support image input", call.Model))
				finishCapture()
				s.record(call)
				return
			}
		}
	}
	shown := aff
	if len(cands) == 1 {
		shown = nil // nobody else to stay away from
	}
	tr := s.trace.begin(Route{Time: start, Agent: call.Agent, Model: call.Model, Provider: p.ID, Group: group, Rule: hit, Nested: nested, Affinity: shown, Order: pl.order, Left: pl.left})
	var skipped []string
	again := 0        // times the last one left has been tried again
	resealed := false // the conversation's reasoning sealed by another account taken out
	floored := false  // the reply's length raised to what the provider takes
	for i := 0; i < len(cands); i++ {
		c := cands[i]
		last := i == len(cands)-1
		hw := newHoldWriter(w, !last || again < lastRetries)
		call.Provider, call.To, call.Usage = c.p.ID, "", Usage{}
		began := time.Now()
		s.trace.update(tr, func(t *Route) { t.Tries = append(t.Tries, Try{ID: c.rest, Model: c.model, Start: began}) })
		attemptBody := body
		if isGroup {
			if in := membersImageInput([]provider.Member{{Provider: c.p, Model: c.model}}, nil); in != nil && !*in {
				attemptBody, _ = textOnlyBody(from, body) // omit images in prior turns and tool results
			}
		}
		call.Status, call.Error = s.attempt(hw, r, from, c.p, c.model, attemptBody, &call)
		if hw.failure != 0 { // the stream failed before any of it was sent
			call.Status, call.Error = hw.failure, c.p.Name+": "+hw.failMsg
		}
		try := Try{ID: c.rest, Model: c.model, Start: began, Done: true, Status: call.Status, Millis: time.Since(began).Milliseconds(), Error: call.Error}
		if r.Context().Err() != nil && !hw.ended {
			// the agent went away: nobody failed, and nobody else is asked
			call.Status, call.Error = 499, "the agent canceled the request"
			try.Status, try.Error, try.Fail = call.Status, call.Error, failCanceled
			s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1] = try })
			break
		}
		if !resealed && from == provider.Responses && !hw.passing && hw.code() == 400 && foreignReasoning.Match(hw.errBody()) {
			// the conversation moved here from another account, whose
			// sealed reasoning this one can't read: asked again without it
			if b, ok := withoutReasoning(body); ok {
				resealed, body = true, b
				try.Fail = failForeign
				s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1] = try })
				i--
				continue
			}
		}
		if !floored && !hw.passing && hw.code() == 400 {
			// asked for fewer tokens than this provider answers with (#64)
			if b, ok := withTokenFloor(body, tokenFloor(hw.errBody())); ok {
				floored, body = true, b
				try.Fail = failFloor
				s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1] = try })
				i--
				continue
			}
		}
		if !last && hw.failed() {
			rest := s.restAfter(c, hw.code(), hw.header, hw.errBody())
			try.Fail, try.Rest = rest.Why, &rest
			s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1] = try })
			skipped = append(skipped, c.label()+": "+call.Error)
			continue
		}
		if wait, ok := passing(hw.code(), hw.header, again); ok && again < lastRetries && hw.failed() {
			// nobody else is left: the same one again, after a moment
			try.Fail, try.Again = failure(hw.code(), hw.errBody()), wait.Milliseconds()
			s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1] = try })
			skipped = append(skipped, c.label()+": "+call.Error)
			again++
			select {
			case <-time.After(wait):
				i--
				continue
			case <-r.Context().Done():
				call.Status, call.Error = 499, "the agent canceled the request"
			}
			break
		}
		hw.release()
		model = c.model
		if call.Status < 400 {
			served(c.rest, call.Usage.Input+call.Usage.Output+call.Usage.CacheRead+call.Usage.CacheWrite)
			answered(stuck, c, aff.Turn, call.Usage.CacheRead)
			if hit != nil {
				ruleAnswered(ruleAt, call.Usage)
			}
			for _, at := range nestedAt {
				ruleAnswered(at, call.Usage)
			}
		} else {
			try.Fail = failure(call.Status, []byte(call.Error))
		}
		s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1] = try })
		break
	}
	if len(skipped) > 0 {
		call.Fallback = strings.Join(skipped, "; ")
	}
	call.Millis = time.Since(start).Milliseconds()
	finishCapture()
	s.trace.update(tr, func(t *Route) {
		t.Done, t.Status, t.Error, t.Millis = true, call.Status, call.Error, call.Millis
		t.Tokens = call.Usage.Input + call.Usage.Output + call.Usage.CacheRead + call.Usage.CacheWrite
	})
	s.record(call)
	if call.To != "" {
		usage.Append(usage.Record{Time: start, Agent: call.Agent, Provider: call.Provider, Model: model,
			Input: call.Usage.Input, Output: call.Usage.Output, CacheRead: call.Usage.CacheRead,
			CacheWrite: call.Usage.CacheWrite, Reasoning: call.Usage.Reasoning, Millis: call.Millis, Status: call.Status})
	}
}

// attempt sends a request to one provider. call.To stays empty when the
// provider has no endpoint to send it to.
func (s *Server) attempt(w http.ResponseWriter, r *http.Request, from provider.Protocol, p provider.Provider, model string, body []byte, call *Call) (int, string) {
	// A Claude Code subscription must run through the genuine binary. Direct
	// OAuth HTTP requests are content-classified as third-party traffic when
	// they carry another agent's harness (Pi, OpenCode, and others).
	if p.Account != nil && p.Account.Agent == "claude" {
		call.To = provider.Anthropic
		return s.serveClaudeSubscription(w, r, from, p, model, body, &call.Usage)
	}
	// Cursor's API belongs to its own clients: its CLI does the talking
	if p.Account != nil && p.Account.Agent == "cursor" {
		call.To = from
		start := func(ctx context.Context, req *Request) (*subscriptionRun, <-chan Event, error) {
			return s.subscription.startCursor(ctx, req, model)
		}
		return s.serveSubscription(w, r, from, "Cursor", model, body, &call.Usage, start)
	}
	// and so does Grok's
	if p.Account != nil && p.Account.Agent == "grok" {
		call.To = from
		start := func(ctx context.Context, req *Request) (*subscriptionRun, <-chan Event, error) {
			return s.subscription.startGrok(ctx, req, model, p.Account.Home)
		}
		return s.serveSubscription(w, r, from, "Grok", model, body, &call.Usage, start)
	}
	// and Devin's, which the CLI serves over ACP
	if p.Account != nil && p.Account.Agent == "devin" {
		call.To = from
		start := func(ctx context.Context, req *Request) (*subscriptionRun, <-chan Event, error) {
			return s.subscription.startDevin(ctx, req, model)
		}
		return s.serveSubscription(w, r, from, "Devin", model, body, &call.Usage, start)
	}
	// a backend that only streams gets a non-streaming request translated
	// (the provider is always streamed on that path) rather than relayed
	relay := slices.Contains(s.usable(p, model), from) && (p.Account == nil || !p.Account.Stream || streamOf(body))
	if relay {
		call.To = from
		if status, msg, done := s.passthrough(w, r, p, from, model, body, &call.Usage); done {
			return status, msg
		}
		// the model isn't served on the client's own API: speak another
	}
	to := s.usable(p, model)
	if len(to) == 0 {
		to = p.Speaks()
	}
	if len(to) == 0 {
		msg := p.Name + " has no endpoint configured"
		return writeError(w, from, 502, msg), msg
	}
	call.To = to[0]
	return s.translate(w, r, p, from, to[0], model, body, &call.Usage)
}

// forward sends a request to the provider.
func (s *Server) forward(ctx context.Context, p provider.Provider, to provider.Protocol, path string, body []byte, in http.Header) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.Base(to)+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("User-Agent", "magpie/"+Version)
	if to == provider.Anthropic {
		req.Header.Set("anthropic-version", "2023-06-01")
		for _, h := range []string{"anthropic-version", "anthropic-beta"} {
			if v := in.Get(h); v != "" {
				req.Header.Set(h, v)
			}
		}
	}
	if p.IsOpenCode() {
		req.Header.Set("x-opencode-session", conversationID(in, body))
	}
	if err := p.Sign(ctx, req, to, body); err != nil {
		return nil, err
	}
	return s.client.Do(req)
}

// passthrough relays a request the provider understands as-is, with the
// model name swapped for the provider's own. The token counts the reply
// carries are read on the way past into u. done is false, with nothing
// written, when the provider serves the model on another of its endpoints
// but not this one.
func (s *Server) passthrough(w http.ResponseWriter, r *http.Request, p provider.Provider, proto provider.Protocol, model string, body []byte, u *Usage) (status int, msg string, done bool) {
	body = rewriteModel(body, model)
	switch proto {
	case provider.Chat:
		body = developerAsSystem(body)
	case provider.Anthropic:
		body = thinkingOffUnlessAsked(body)
	}
	res, err := s.forward(r.Context(), p, proto, pathOf(proto), p.Prepare(body), r.Header)
	if err != nil {
		return writeError(w, proto, 502, p.Name+": "+err.Error()), err.Error(), true
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		msg := p.Name + ": " + provider.APIError(b, res.Status)
		if wrongEndpoint(res.StatusCode, b) {
			s.markUnfit(p.ID, model, proto)
			if len(s.usable(p, model)) > 0 {
				return res.StatusCode, msg, false
			}
		}
		keepRetry(w.Header(), res.Header)
		return writeError(w, proto, res.StatusCode, msg), msg, true
	}
	rd, sse := eventStream(res)
	h := w.Header()
	for _, k := range []string{"Content-Type", "Request-Id", "X-Request-Id"} {
		if v := res.Header.Get(k); v != "" {
			h.Set(k, v)
		}
	}
	if sse {
		h.Set("Cache-Control", "no-cache")
		h.Set("X-Accel-Buffering", "no")
	}
	w.WriteHeader(res.StatusCode)
	f, _ := w.(http.Flusher)
	sniff := newSniffer(proto, res.Header.Get("Content-Type"))
	defer func() { u.add(sniff.usage()) }()
	buf := make([]byte, 32<<10)
	for {
		n, err := rd.Read(buf)
		if n > 0 {
			sniff.write(buf[:n])
			if _, werr := w.Write(buf[:n]); werr != nil {
				return res.StatusCode, "", true
			}
			if f != nil {
				f.Flush()
			}
		}
		if err != nil {
			break
		}
	}
	return res.StatusCode, "", true
}

// eventStream reports whether a reply is server-sent events. The header
// says so for most vendors; the ChatGPT backend sends none, so the body's
// first bytes decide, and the header is filled in for whoever reads it.
func eventStream(res *http.Response) (io.Reader, bool) {
	ct := res.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "text/event-stream") {
		return res.Body, true
	}
	if ct != "" && !strings.HasPrefix(ct, "text/plain") {
		return res.Body, false
	}
	br := bufio.NewReaderSize(res.Body, 4<<10)
	head, _ := br.Peek(16)
	head = bytes.TrimLeft(head, " \t\r\n")
	for _, pfx := range []string{"event:", "data:", ":"} {
		if bytes.HasPrefix(head, []byte(pfx)) {
			res.Header.Set("Content-Type", "text/event-stream")
			return br, true
		}
	}
	return br, false
}

// fits reports whether the provider hasn't turned model away from proto.
func (s *Server) fits(providerID, model string, proto provider.Protocol) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.unfit[providerID+"\x00"+model+"\x00"+string(proto)]
}

func (s *Server) markUnfit(providerID, model string, proto provider.Protocol) {
	s.mu.Lock()
	s.unfit[providerID+"\x00"+model+"\x00"+string(proto)] = true
	s.mu.Unlock()
}

// usable lists the protocols p speaks that model is served on, as far as
// the provider says and hasn't turned it away, preferred first: Chat
// Completions, which every OpenAI-compatible vendor serves alike, except
// for OpenAI's own models where their makers serve them, whose newest are
// Responses-first (and some Responses-only).
func (s *Server) usable(p provider.Provider, model string) []provider.Protocol {
	apis := p.APIs(model)
	var out []provider.Protocol
	for _, proto := range p.Speaks() {
		if s.fits(p.ID, model, proto) && (apis == nil || slices.Contains(apis, proto)) {
			out = append(out, proto)
		}
	}
	if responsesFirst(p, model) {
		sort.SliceStable(out, func(i, j int) bool { return out[i] == provider.Responses && out[j] != provider.Responses })
	}
	return out
}

// responsesFirst: an OpenAI model on OpenAI's API or Copilot's.
func responsesFirst(p provider.Provider, model string) bool {
	if p.Responses == "" || (p.ID != "copilot" && provider.HostOf(p.Responses) != "api.openai.com") {
		return false
	}
	m := strings.ToLower(model[strings.LastIndex(model, "/")+1:])
	return strings.HasPrefix(m, "gpt-") || strings.HasPrefix(m, "codex") ||
		len(m) > 1 && m[0] == 'o' && m[1] >= '0' && m[1] <= '9'
}

// forwardTranslated sends one translated, streaming request upstream. A
// provider can serve a model on some of its endpoints and not others —
// OpenAI's and Copilot's newest models answer only /responses, Copilot's
// Claude models only /chat/completions — so when it says the model isn't
// served on this one, the request is built again for the next endpoint it
// speaks, and the model is remembered there.
func (s *Server) forwardTranslated(ctx context.Context, p provider.Provider, to provider.Protocol, req *Request, model string, in http.Header) (*http.Response, provider.Protocol, error) {
	if req.Effort != "" {
		if e := fitEffort(req.Effort, p.Efforts(model)); e != req.Effort {
			r := *req
			r.Effort, req = e, &r
		}
	}
	for {
		body := build(to, req, model, p.Host(), p.RejectsTemperature(model))
		if to == provider.CodeAssist && p.Account != nil {
			body = buildCodeAssist(req, model, p.Account.Agent)
		}
		res, err := s.forward(ctx, p, to, pathOf(to), p.Prepare(body), in)
		if err != nil || res.StatusCode < 400 {
			return res, to, err
		}
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		res.Body = io.NopCloser(bytes.NewReader(b))
		if !wrongEndpoint(res.StatusCode, b) {
			return res, to, nil
		}
		s.markUnfit(p.ID, model, to)
		next := s.usable(p, model)
		if len(next) == 0 {
			return res, to, nil
		}
		to = next[0]
	}
}

// wrongEndpoint recognizes the errors OpenAI-compatible servers give when a
// model exists but isn't served on the endpoint asked.
func wrongEndpoint(status int, body []byte) bool {
	if status < 400 || status >= 500 {
		return false
	}
	msg := strings.ToLower(string(body))
	for _, phrase := range []string{
		"not a chat model",
		"not supported in the v1/chat/completions",
		"not supported in /v1/chat/completions",
		"not supported in the v1/responses",
		"not supported in /v1/responses",
		"only supported in v1/responses",
		"only supported in /v1/responses",
		"use v1/completions",
		"use /v1/completions",
		"use v1/responses",
		"use /v1/responses",
		"use v1/chat/completions",
		"use /v1/chat/completions",
		"not accessible via the", // Copilot
		"unsupported_api_for_model",
	} {
		if strings.Contains(msg, phrase) {
			return true
		}
	}
	return false
}

// translate serves a client API the provider lacks by speaking another
// one to it. The provider is always streamed; the client gets whichever
// it asked for.
func (s *Server) translate(w http.ResponseWriter, r *http.Request, p provider.Provider, from, to provider.Protocol, model string, body []byte, u *Usage) (int, string) {
	request, err := parse(from, body)
	if err != nil {
		return writeError(w, from, 400, err.Error()), err.Error()
	}
	stream := request.Stream
	request.Stream = true
	res, actual, err := s.forwardTranslated(r.Context(), p, to, request, model, r.Header)
	if err != nil {
		return writeError(w, from, 502, p.Name+": "+err.Error()), err.Error()
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		msg := p.Name + ": " + provider.APIError(b, res.Status)
		keepRetry(w.Header(), res.Header)
		return writeError(w, from, res.StatusCode, msg), msg
	}
	dec := decoder(actual)
	rd, sse := eventStream(res)
	if !sse {
		// the provider ignored stream:true; read the whole reply as one
		// event stream would be wrong, so give up cleanly
		b, _ := io.ReadAll(io.LimitReader(rd, 1<<20))
		msg := p.Name + " did not stream: " + provider.APIError(b, "unexpected reply")
		return writeError(w, from, 502, msg), msg
	}
	if stream {
		enc := encoder(from, newSSEWriter(w), request.Model)
		var failed string
		serr := readSSE(rd, func(_, data string) error {
			return dec(data, func(ev Event) {
				switch ev.Kind {
				case KError:
					failed = ev.Text
				case KStart, KUsage:
					u.add(ev.Usage)
				}
				enc.event(ev)
			})
		})
		if serr != nil && failed == "" {
			// the upstream died mid-reply: say so in the client's own
			// protocol instead of finishing as if all went well
			failed = p.Name + ": " + serr.Error()
			enc.event(Event{Kind: KError, Text: failed})
		}
		if failed == "" {
			enc.finish()
		}
		return 200, failed
	}
	var col collector
	if err := readSSE(rd, func(_, data string) error {
		return dec(data, col.add)
	}); err != nil {
		// a partial answer is not an answer
		msg := p.Name + ": " + err.Error()
		return writeError(w, from, 502, msg), msg
	}
	if col.err != "" && len(col.res.Parts) == 0 {
		return writeError(w, from, 502, p.Name+": "+col.err), col.err
	}
	res2 := col.finish()
	u.add(res2.Usage)
	out := render(from, res2, request.Model)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	w.Write(out)
	return 200, col.err
}

// ---- protocol tables --------------------------------------------------------

func pathOf(proto provider.Protocol) string {
	switch proto {
	case provider.Chat:
		return "/chat/completions"
	case provider.Responses:
		return "/responses"
	case provider.CodeAssist:
		return "/v1internal:streamGenerateContent?alt=sse"
	}
	return "/v1/messages"
}

func parse(proto provider.Protocol, body []byte) (*Request, error) {
	switch proto {
	case provider.Chat:
		return parseChat(body)
	case provider.Responses:
		return parseResponses(body)
	case provider.Gemini:
		return parseGemini(body)
	}
	return parseAnthropic(body)
}

func build(proto provider.Protocol, r *Request, model, host string, rejectTemp bool) []byte {
	switch proto {
	case provider.Chat:
		return buildChat(r, model, host, rejectTemp)
	case provider.Responses:
		return buildResponses(r, model, rejectTemp)
	}
	return buildAnthropic(r, model)
}

func decoder(proto provider.Protocol) func(data string, emit func(Event)) error {
	switch proto {
	case provider.Chat:
		d := &chatDecoder{}
		return d.decode
	case provider.Responses:
		d := &responsesDecoder{}
		return d.decode
	case provider.CodeAssist:
		d := &codeAssistDecoder{}
		return d.decode
	}
	return decodeAnthropic
}

type streamEncoder interface {
	event(Event)
	finish()
}

func encoder(proto provider.Protocol, w *sseWriter, model string) streamEncoder {
	switch proto {
	case provider.Chat:
		return &chatEncoder{w: w, model: model}
	case provider.Responses:
		return &responsesEncoder{w: w, model: model}
	case provider.Gemini:
		return &geminiEncoder{w: w, model: model}
	}
	return &anthropicEncoder{w: w, model: model}
}

func render(proto provider.Protocol, res Result, model string) []byte {
	switch proto {
	case provider.Chat:
		return renderChat(res, model)
	case provider.Responses:
		return renderResponses(res, model)
	case provider.Gemini:
		return renderGemini(res, model)
	}
	return renderAnthropic(res, model)
}

// ---- small helpers ------------------------------------------------------------

func streamOf(body []byte) bool {
	var v struct {
		Stream bool `json:"stream"`
	}
	json.Unmarshal(body, &v)
	return v.Stream
}

func modelOf(body []byte) string {
	var v struct {
		Model string `json:"model"`
	}
	json.Unmarshal(body, &v)
	return v.Model
}

// rewriteModel swaps the model field, keeping every other field as it was.
func rewriteModel(body []byte, model string) []byte {
	return withFields(body, map[string]any{"model": model})
}

// developerAsSystem turns "developer" messages into "system" ones. Agents
// such as Pi send the developer role to reasoning models, which OpenAI
// accepts, but other Chat Completions backends (DeepSeek among them) reject
// the whole request; every backend accepts system, OpenAI included.
func developerAsSystem(body []byte) []byte {
	if !bytes.Contains(body, []byte(`"developer"`)) {
		return body
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return body
	}
	msgs, _ := m["messages"].([]any)
	changed := false
	for _, v := range msgs {
		if msg, ok := v.(map[string]any); ok && msg["role"] == "developer" {
			msg["role"] = "system"
			changed = true
		}
	}
	if !changed {
		return body
	}
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

// sessionHeaders are where agents already name their conversation: OpenCode
// (to its own gateway, and to everyone else), Pi, Codex and Claude Code.
var sessionHeaders = []string{
	"x-opencode-session", "x-session-affinity", "x-session-id",
	"session_id", "session-id", "x-claude-code-session-id",
}

// conversationID is a stable id for the conversation a request belongs to.
// It is the agent's own session id when it sends one; otherwise it is derived
// from the conversation's first user message, which every later turn repeats.
func conversationID(in http.Header, body []byte) string {
	for _, h := range sessionHeaders {
		if v := strings.TrimSpace(in.Get(h)); v != "" {
			return v
		}
	}
	var m struct {
		Messages []json.RawMessage `json:"messages"`
		Input    json.RawMessage   `json:"input"`
	}
	// An undecodable body still gets an id: the hash of the whole body.
	_ = json.Unmarshal(body, &m)
	items := m.Messages
	if len(items) == 0 && len(m.Input) > 0 && m.Input[0] == '[' {
		// A malformed input array leaves items empty, and the whole body is hashed.
		_ = json.Unmarshal(m.Input, &items)
	}
	first := []byte(m.Input)
	if len(items) > 0 {
		first = items[0]
	}
	for _, it := range items {
		var r struct {
			Role string `json:"role"`
		}
		if json.Unmarshal(it, &r) == nil && r.Role == "user" {
			first = it
			break
		}
	}
	if len(first) == 0 {
		first = body
	}
	sum := sha256.Sum256(first)
	return "magpie-" + hex.EncodeToString(sum[:12])
}

// withFields sets top-level fields, keeping every other field as it was.
func withFields(body []byte, fields map[string]any) []byte {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return body
	}
	for k, v := range fields {
		m[k] = v
	}
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// writeError answers in the client's own error shape.
func writeError(w http.ResponseWriter, proto provider.Protocol, status int, msg string) int {
	typ := "api_error"
	switch {
	case status == 400:
		typ = "invalid_request_error"
	case status == 401:
		typ = "authentication_error"
	case status == 403:
		typ = "permission_error"
	case status == 404:
		typ = "not_found_error"
	case status == 429:
		typ = "rate_limit_error"
	case status == 529:
		typ = "overloaded_error"
	}
	var code any
	if tooLong(status, msg) {
		// said the way the client's own API says it, so the agent
		// compacts the conversation and tries again rather than stopping
		status, typ, code = 400, "invalid_request_error", "context_length_exceeded"
		if proto == provider.Anthropic && !strings.Contains(strings.ToLower(msg), "prompt is too long") {
			msg = "prompt is too long: " + msg
		}
	}
	var v any
	switch proto {
	case provider.Anthropic:
		v = map[string]any{"type": "error", "error": map[string]any{"type": typ, "message": msg}}
	case provider.Gemini:
		st := map[int]string{400: "INVALID_ARGUMENT", 401: "UNAUTHENTICATED", 403: "PERMISSION_DENIED", 404: "NOT_FOUND",
			429: "RESOURCE_EXHAUSTED", 500: "INTERNAL", 502: "UNAVAILABLE", 503: "UNAVAILABLE", 529: "UNAVAILABLE"}[status]
		if st == "" {
			st = "UNKNOWN"
		}
		v = map[string]any{"error": map[string]any{"code": status, "message": msg, "status": st}}
	default:
		v = map[string]any{"error": map[string]any{"message": msg, "type": typ, "code": code, "param": nil}}
	}
	writeJSON(w, status, v)
	return status
}

// tooLongRe matches how vendors say a request is more than the model's
// context holds: OpenAI's context_length_exceeded, Anthropic's "prompt is
// too long", Volcengine's "Input exceeds the context limit", "maximum
// context length", "context window"…
var tooLongRe = regexp.MustCompile(`(?i)context_length_exceeded|prompt is too long|input is too long|(exceeds?|exceeded|over|beyond)( the)?( model'?s?)?( maximum)? context|context (length|limit|window) (exceeded|is exceeded)|maximum context length|too many (input |prompt )?tokens|上下文(长度)?(超|过长)|超(过|出)(了)?(模型)?(的)?(最大)?上下文`)

// tooLong is whether a vendor's error says the conversation no longer
// fits. One about max_tokens is left alone: the reply's allowance, not
// the conversation, is what is too big there, and compacting won't help.
func tooLong(status int, msg string) bool {
	if status < 400 || status >= 500 {
		return false
	}
	m := strings.ToLower(msg)
	if strings.Contains(m, "max_tokens") || strings.Contains(m, "max_output_tokens") || strings.Contains(m, "max_completion_tokens") {
		return false
	}
	return tooLongRe.MatchString(msg)
}
