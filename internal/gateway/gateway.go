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
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/redact"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

// DefaultAddr is where the gateway listens unless MAGPIE_ADDR says otherwise.
const DefaultAddr = "127.0.0.1:3425"

// Token is the bearer token agents are told to use. The gateway only
// listens on loopback and accepts anything, but agents insist on one.
const Token = "magpie"

// TokenFor is the token an agent that says nothing of itself in its
// User-Agent is told to use, so the gateway still knows it: Alma's requests
// go out as the AI SDK's ("ai-sdk/openai/…"), with nothing of Alma's own.
func TokenFor(agent string) string { return Token + "-" + agent }

// agentOf is the agent a request came from: the one its token names
// (TokenFor), else the one its User-Agent does.
func agentOf(r *http.Request) string {
	if id, ok := strings.CutPrefix(callerKey(r), Token+"-"); ok && id != "" {
		return usage.AgentOf(id)
	}
	if isClaudeDesktop(r) {
		return "claude-desktop"
	}
	return usage.AgentOf(r.Header.Get("User-Agent"))
}

// StandIn is the model an agent is set to use in place of one it named that
// magpie doesn't serve it: Claude Code asks for claude-haiku-… by name for
// its small tasks, whatever its haiku tier is set to, and that goes to the
// tier's model rather than to some provider that happens to list the name.
// "" leaves the model as asked. Set by main.
var StandIn func(agent, model string) string

// standIn is StandIn's model for one magpie shows no entry for: not a
// catalog id or model, nor a ready provider's "provider/model".
func standIn(agent, asked string) string {
	if StandIn == nil || !unserved(asked) {
		return ""
	}
	m := StandIn(agent, asked)
	if m == asked {
		return ""
	}
	return m
}

// unserved: magpie shows no entry for the model — not a catalog id or
// model, nor the "provider/model" of a provider that is on (a routing
// group's id is taken as served). A switched-off provider's is unserved: a
// session that started before it was switched off still asks for the model
// its agent was then on, and the agent has been moved since (#200).
func unserved(asked string) bool {
	if asked == "" || strings.HasPrefix(asked, provider.GroupPrefix) {
		return false
	}
	if _, ok := provider.GroupFor(asked); ok {
		return false
	}
	id := strings.TrimSuffix(asked, "[1m]")
	for _, e := range provider.Catalog() {
		if e.ID == id || e.Model == id {
			return false
		}
	}
	if pid, _, ok := strings.Cut(id, "/"); ok {
		if p, err := provider.Find(pid); err == nil && p.On() {
			return false
		}
	}
	return true
}

// switchedOff says a request for model went nowhere because p, which
// serves it, is switched off in magpie (provider.Provider.Off).
func switchedOff(p provider.Provider, model string) string {
	return fmt.Sprintf("%s is switched off in Magpie, so %q is not served; switch it on again in Magpie's Providers to use it", p.Name, model)
}

// Version is set by main.
var Version = "dev"

// Addr is the listen address.
func Addr() string {
	if a := os.Getenv("MAGPIE_ADDR"); a != "" {
		return a
	}
	return DefaultAddr
}

// URL is the base URL agents use, e.g. http://127.0.0.1:3425: a gateway
// listening on every interface is reached here on loopback.
func URL() string {
	a := Addr()
	if h, p, err := net.SplitHostPort(a); err == nil && (h == "" || net.ParseIP(h) != nil && net.ParseIP(h).IsUnspecified()) {
		a = net.JoinHostPort("127.0.0.1", p)
	}
	return "http://" + a
}

// Window says this process shows the routing of the gateway it serves:
// the app's window (a tray's too, when opened) or magpie web's page, and
// not magpie serve, which has none. Set by the gui.
var Window bool

// Running reports whether a gateway answers at the address.
func Running() bool {
	running, _ := Serving()
	return running
}

// Serving asks the address what answers there: whether a magpie gateway
// does, and whether the magpie serving it has a window its routing can be
// watched in (#110).
func Serving() (running, window bool) {
	c := &http.Client{Timeout: 700 * time.Millisecond}
	res, err := c.Get(URL() + "/")
	if err != nil {
		return false, false
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	return bytes.Contains(b, []byte(`"magpie"`)), bytes.Contains(b, []byte(`"window":true`))
}

// Call is one request the gateway handled, for the status views.
type Call struct {
	Time  time.Time `json:"time"`
	Agent string    `json:"agent"` // who called, from the client's User-Agent
	// Kind: what the agent made the call for, when it isn't its turn —
	// a Codex subagent's (callKind) — "" for a turn
	Kind     string            `json:"kind,omitempty"`
	Model    string            `json:"model"`
	Provider string            `json:"provider"`
	From     provider.Protocol `json:"from"`
	To       provider.Protocol `json:"to"`
	Status   int               `json:"status"`
	Millis   int64             `json:"ms"`
	// TTFT: ms from the request to its reply's first content — text,
	// reasoning or a tool call — and FirstText to its first text, when
	// it was streamed (#196)
	TTFT              int64  `json:"ttft,omitempty"`
	FirstText         int64  `json:"firstText,omitempty"`
	Error             string `json:"error,omitempty"`
	Fallback          string `json:"fallback,omitempty"` // providers that failed first, and why
	Usage             Usage  `json:"usage"`
	RequestBody       string `json:"requestBody,omitempty"`
	ResponseBody      string `json:"responseBody,omitempty"`
	RequestTruncated  bool   `json:"requestTruncated,omitempty"`
	ResponseTruncated bool   `json:"responseTruncated,omitempty"`
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
	// the listener, swapped when the gateway is shared on the network or
	// taken off it (see Relisten)
	lnMu sync.Mutex
	ln   net.Listener
	// the images described for models that can't see them (vision.go)
	sightMu    sync.Mutex
	sights     map[string]*sight
	sightOrder []string
}

// New makes a gateway.
func New() *Server {
	redact.SetKeyPath(filepath.Join(settings.Dir(), "redact.key"))
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

// ListenAndServe runs the gateway until ctx ends, and returns once the
// requests in flight then have finished: within 2s, or, handing over, as
// long as a stream takes. A bind error means another magpie is already
// serving, which is fine for the caller to ignore.
func (s *Server) ListenAndServe(ctx context.Context) error {
	loadLANKey()
	ln, err := Listen(listenAddr())
	if err != nil {
		return err
	}
	s.lnMu.Lock()
	s.ln = ln
	s.lnMu.Unlock()
	srv := &http.Server{Handler: lanGuard(s.Handler()), ReadHeaderTimeout: 30 * time.Second, IdleTimeout: 5 * time.Minute}
	// the magpie serving the gateway, and only it, keeps the saved accounts
	// signed in, so two never refresh one sign-in at once
	go provider.KeepLoginsAlive(ctx)
	// and signs Codex and Claude Code in to their next account when the
	// one they are on is spent
	go provider.KeepOnAnAccountWithRoom(ctx)
	// and, when settings say to, starts its accounts' next windows as the last reset
	go provider.KeepCodexWindowsWarm(ctx)
	go provider.KeepClaudeWindowsWarm(ctx, warmClaude)
	// and checks the WorkBuddy accounts in for the day's credits
	go provider.KeepWorkBuddyCheckedIn(ctx)
	for _, f := range WhileServing {
		go f(ctx)
	}
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		<-ctx.Done()
		wait := 2 * time.Second
		if Handover {
			wait = 15 * time.Minute
		}
		c, cancel := context.WithTimeout(context.Background(), wait)
		defer cancel()
		srv.Shutdown(c)
		s.subscription.abortAll()
	}()
	for {
		err := srv.Serve(ln)
		if err == nil || errors.Is(err, http.ErrServerClosed) {
			<-drained
			return nil
		}
		// Relisten closed it to move the gateway: serve the one it opened
		s.lnMu.Lock()
		next := s.ln
		s.lnMu.Unlock()
		if next == ln {
			return err
		}
		ln = next
	}
}

// Handler routes the client APIs.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.info)
	mux.HandleFunc("GET /v1/models", s.models)
	mux.HandleFunc("GET /models", s.models)
	mux.HandleFunc("GET /v1/models/{id...}", s.model)
	// Claude Desktop's third-party gateway looks for one here before it
	// takes the address
	mux.HandleFunc("GET /api/hello", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"name": "magpie", "version": Version})
	})
	mux.HandleFunc("GET /v1/magpie/quotas", s.quotas)
	mux.HandleFunc("POST /v1/chat/completions", s.handle(provider.Chat))
	mux.HandleFunc("POST /chat/completions", s.handle(provider.Chat))
	mux.HandleFunc("POST /v1/responses", s.handle(provider.Responses))
	mux.HandleFunc("POST /responses", s.handle(provider.Responses))
	mux.HandleFunc("POST /v1/messages", s.handle(provider.Anthropic))
	mux.HandleFunc("POST /messages", s.handle(provider.Anthropic))
	mux.HandleFunc("POST /v1/systemone", s.serveSystemOne)
	mux.HandleFunc("POST /v1/messages/count_tokens", s.countTokens)
	mux.HandleFunc("POST /v1/images/generations", s.images(false))
	mux.HandleFunc("POST /images/generations", s.images(false))
	mux.HandleFunc("POST /v1/images/edits", s.images(true))
	mux.HandleFunc("POST /images/edits", s.images(true))
	mux.HandleFunc("POST /_magpie/claude-mcp/{token}", s.subscription.mcpCall)
	mux.HandleFunc(CodexPath+"/", s.codexBackend)
	mux.HandleFunc("GET /v1beta/models", s.geminiModels)
	mux.HandleFunc("POST /v1beta/models/{call...}", s.gemini)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, provider.Chat, http.StatusNotFound, "magpie serves /v1/chat/completions, /v1/responses, /v1/messages, /v1/systemone, /v1/images/generations, /v1/images/edits and /v1beta/models/*")
	})
	return mux
}

func (s *Server) info(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"name": "magpie", "version": Version, "models": len(provider.Catalog()), "window": Window,
		"apis": []string{"/v1/chat/completions", "/v1/responses", "/v1/messages", "/v1/systemone", "/v1beta/models/{model}:generateContent", "/v1/images/generations", "/v1/images/edits", "/v1/magpie/quotas"}})
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
	m := map[string]any{"id": e.ID, "object": "model", "type": "model", "created": 0, "created_at": "2025-01-01T00:00:00Z",
		"owned_by": e.Provider.ID, "display_name": e.Name, "reasoning": len(levels) > 0, "supported_reasoning_levels": levels}
	// the window, as the names clients read it by: a group's context
	// (magpie group set … context=) included
	if e.Context > 0 {
		m["context_window"], m["context_length"], m["max_input_tokens"] = e.Context, e.Context, e.Context
	}
	if e.Output > 0 {
		m["max_output_tokens"] = e.Output
	}
	return m
}

// catalogFor is the catalog as the agent asking is shown it.
func catalogFor(r *http.Request) []provider.Entry {
	shown, _ := provider.CatalogFor(agentOf(r))
	return shown
}

func (s *Server) models(w http.ResponseWriter, r *http.Request) {
	data := []map[string]any{}
	shown := catalogFor(r)
	if agentOf(r) == "claude-desktop" {
		data = desktopModels(shown)
	} else {
		for _, e := range shown {
			data = append(data, modelObject(e))
		}
	}
	out := map[string]any{"object": "list", "data": data, "has_more": false}
	if len(data) > 0 {
		out["first_id"], out["last_id"] = data[0]["id"], data[len(data)-1]["id"]
	}
	writeJSON(w, 200, out)
}

func (s *Server) model(w http.ResponseWriter, r *http.Request) {
	id := unprefixed(r.PathValue("id"))
	for _, e := range provider.Catalog() {
		if e.ID == id {
			writeJSON(w, 200, modelObject(e))
			return
		}
	}
	writeError(w, provider.Chat, 404, "unknown model "+id)
}

// unprefixed is the model magpie serves by an id Claude Desktop was given
// for it (claudeLooking): anthropic/magpie-<number>, mythos-magpie-<number>,
// magpie-<number>.anthropic.<Claude model>, or, as it listed them
// before, "anthropic/" put in front of magpie's id. An id that is magpie's
// as it stands (a provider named anthropic) is left alone.
func unprefixed(id string) string {
	if real, ok := aliased(id); ok {
		return real
	}
	rest, ok := strings.CutPrefix(id, "anthropic/")
	if !ok || rest == "" {
		return id
	}
	if _, _, ok := provider.Resolve(id); ok {
		return id
	}
	if _, ok := provider.GroupFor(id); ok {
		return id
	}
	return rest
}

// countTokens answers Anthropic's count_tokens: through the provider when
// it implements counting, else a rough estimate. A failed connection or
// limited key yields to the next key; other failures reach the client.
func (s *Server) countTokens(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, provider.Anthropic, 400, err.Error())
		return
	}
	var model string
	body, model, err = requestModel(body)
	if err != nil {
		writeError(w, provider.Anthropic, 400, err.Error())
		return
	}
	// Count the same masked prompt that generation sends to the vendor.
	w, body, unmask := redacted(w, body)
	defer unmask()
	p, model, ok := provider.Resolve(unprefixed(model))
	// Claude Subscription generations run through the Claude Code binary. Its
	// OAuth token must not take a direct HTTP side path just for token counting.
	if ok && p.Account != nil && (p.Account.Agent == "claude" || p.Account.Agent == "cursor" || p.Account.Agent == "grok" || p.Account.Agent == "devin" || p.Account.Agent == "kiro" || p.Account.Agent == "qoder" || p.Account.Agent == "gemini" || p.Account.Agent == "antigravity") {
		req, err := parseAnthropic(body)
		if err != nil {
			writeError(w, provider.Anthropic, 400, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"input_tokens": estimate(req)})
		return
	}
	var counts []candidate
	if ok {
		// Keep the provider's key order and cooldowns, but never count a
		// fallback model: it may use a different tokenizer.
		p.Fallback = nil
		for _, c := range s.candidates(p, model, provider.Anthropic) {
			// A relay's OpenAI-only key can't count Anthropic tokens.
			if c.p.Anthropic != "" && slices.Contains(s.usable(c.p, model), provider.Anthropic) {
				counts = append(counts, c)
			}
		}
	}
	for i, c := range counts {
		res, err := s.forward(r.Context(), c.p, provider.Anthropic, "/v1/messages/count_tokens", rewriteModel(body, model), r.Header)
		if err != nil {
			if r.Context().Err() == nil {
				s.restAfter(c, http.StatusBadGateway, nil, []byte(err.Error()))
				if i+1 < len(counts) {
					continue
				}
			}
			writeError(w, provider.Anthropic, 502, c.p.Name+": "+err.Error())
			return
		}
		if res.StatusCode >= 400 {
			b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
			res.Body.Close()
			if unsupportedCount(res.StatusCode, b) {
				break
			}
			if res.StatusCode == http.StatusTooManyRequests {
				s.restAfter(c, res.StatusCode, res.Header, b)
				if i+1 < len(counts) {
					continue
				}
			}
			keepRetry(w.Header(), res.Header, b)
			writeError(w, provider.Anthropic, res.StatusCode, c.p.Name+": "+provider.APIError(b, res.Status))
			return
		}
		defer res.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(res.StatusCode)
		io.Copy(w, res.Body)
		return
	}
	req, err := parseAnthropic(body)
	if err != nil {
		writeError(w, provider.Anthropic, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"input_tokens": estimate(req)})
}

// Match the counting operation or the endpoint itself, not an unsupported
// image, model, API key or tool parameter mentioned in the same error.
var unsupportedCountWords = regexp.MustCompile(`(?i)` +
	`^(unsupported|unimplemented|not (supported|implemented))$|` +
	`\b(count_tokens|counttokens|token counting|counting tokens) ((is )?not (supported|implemented)|is (unsupported|unimplemented))\b|` +
	`\b(unsupported|unimplemented|does not support|doesn't support) (count_tokens|counttokens|token counting|counting tokens)\b|` +
	`^(this |the )?(unsupported|unimplemented) (endpoint|api|method|operation)(:|$)|` +
	`^(this |the )?(endpoint|api|method|operation) ((is )?not (supported|implemented)|is (unsupported|unimplemented))\b|` +
	`(不支持|未实现)\s*(count_tokens|counttokens|token\s*计数|令牌计数)\s*(接口|功能)?\s*($|[，。,:：;；])|` +
	`(count_tokens|counttokens|token\s*计数|令牌计数)\s*(接口|功能)?\s*(暂|尚)?(不支持|未实现)\s*($|[，。,:：;；])|` +
	`^(该|此|本)?(端点|接口)\s*(暂|尚)?(不支持|未实现)\s*($|[，。,:：;；])`)

// unsupportedCount recognizes a missing optional endpoint. This says
// nothing about support for /messages itself.
func unsupportedCount(status int, body []byte) bool {
	switch status {
	case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return true
	case http.StatusBadRequest:
		return unsupportedCountWords.MatchString(strings.Trim(provider.APIError(body, ""), ": \t\r\n.。"))
	}
	return false
}

// handle is the request path of one client API.
func (s *Server) handle(from provider.Protocol) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeError(w, from, 400, err.Error())
			return
		}
		body, _, err = requestModel(body)
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
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, provider.Gemini, 400, err.Error())
		return
	}
	if err := decodeRequest(body, &struct{}{}); err != nil {
		writeError(w, provider.Gemini, 400, err.Error())
		return
	}
	model, err = validateModel(model)
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

// geminiCount unwraps generateContentRequest, if present, and estimates the
// original prompt locally; nothing is sent upstream or needs masking.
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
	call := Call{Time: start, From: from, Model: unprefixed(modelOf(body)), Agent: agentOf(r), Kind: callKind(r.Header),
		RequestBody: requestBody, RequestTruncated: requestTruncated}
	usage.Saw(call.Agent)
	finishCapture := func() {
		call.ResponseBody = capture.body.text()
		call.ResponseTruncated = capture.body.truncated
	}
	// a model's id without a provider in it that names a routing group is
	// the group's, as "group/<id>" is, rather than one provider's that
	// serves it: the Routing view shows the group it went to
	asked := call.Model
	if call.Agent == "claude-desktop" {
		asked = desktopTurn(asked, body)
	}
	if id, ok := provider.GroupFor(asked); ok {
		asked = id
	} else if m := standIn(call.Agent, asked); m != "" {
		asked = m
	}
	p, model, ok := provider.Resolve(asked)
	if !ok {
		call.Status, call.Error = 404, "unknown model"
		if off, isOff := provider.SwitchedOff(asked); isOff {
			call.Error = "provider switched off"
			writeError(w, from, 404, switchedOff(off, call.Model))
			finishCapture()
			s.record(call)
			return
		}
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
	if p.Decides() {
		// Jev answers questions about a message, not the message
		call.Status, call.Error = 400, "a decision model"
		writeError(w, from, 400, fmt.Sprintf("%s only decides a routing group's model and effort; it holds no conversation", call.Model))
		finishCapture()
		s.record(call)
		return
	}
	// a routing group's rules pick the member that goes first, looked at
	// before any image is taken out of the request: one may be for images
	g, ms, isGroup := provider.FindGroup(asked)
	var hit *RuleHit
	var ruled []provider.Member
	var ruleAt, words string
	var ruleReq *Request // parsed for the rules of the group or a group in it
	// a classifier's own call to a group (a group's classifier may be one)
	// asks no classifier in turn: one that is, however far round, the group
	// asking would ask itself for ever
	ask := s.askClassifier
	if r.Header.Get("User-Agent") == RouterAgent {
		ask = nil
	}
	if isGroup && slices.ContainsFunc(ms, func(m provider.Member) bool {
		return g.Ruled() || slices.ContainsFunc(m.Via, provider.Group.Ruled)
	}) {
		if req, err := parse(from, body); err == nil {
			ruleReq, ruleAt, words = req, ruleKey(g, r.Header, req), firstWords(req)
		}
	}
	if ruleReq != nil && g.Ruled() {
		hit = ruleFor(ruleAt, g, ms, ruleReq, call.Agent, ask)
		ruled = ruleMembers(hit, ms)
	}
	// Some clients send images even when the selected model is known to
	// accept text only. Reject a new image and omit images from history.
	var imageInput *bool
	if isGroup {
		// the member a rule put first, or what every member takes
		imageInput = membersImageInput(ms, ruled)
	} else {
		var known *bool
		for _, m := range p.Available() {
			if m.ID == model {
				known = m.ImageInput
				break
			}
		}
		_, imageInput = provider.ApplyImage(p.ID, model, false, known)
	}
	// Unless a model that sees describes them to it (vision.go).
	seeing := sync.OnceValues(func() (string, bool) {
		if describing(r.Context()) {
			return "", false
		}
		return seer()
	})
	if imageInput != nil && !*imageInput {
		var currentImage bool
		if see, ok := seeing(); ok && hasImage(from, body) {
			seen, err := s.seenBody(r.Context(), from, body, see)
			if err != nil {
				call.Status, call.Error = 502, "image not described"
				writeError(w, from, 502, fmt.Sprintf("model %q can't see images, and %s couldn't describe the image for it: %v", call.Model, see, err))
				finishCapture()
				s.record(call)
				return
			}
			body = seen
		} else {
			body, currentImage = textOnlyBody(from, body)
		}
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
		var ok bool
		cands, pl, ok = applyRule(hit, ms, cands, pl)
		if ok && aff.Kept && cands[0].rest != was.rest {
			aff.Kept, aff.Why = false, "rule"
		}
	}
	// the rules of the groups in the group, down the one that goes first
	var nested []NestedRule
	var nestedAt []string
	if ruleReq != nil {
		nested, nestedAt, cands, pl = s.nestedRules(ruleAt, ruleReq, call.Agent, ask, ms, cands, pl, aff)
	}
	// the effort a group's decision model picked for the turn: the
	// outermost group's that did
	effort := ""
	if hit != nil {
		effort = hit.Pick
	}
	for _, n := range nested {
		if effort == "" && n.Rule != nil {
			effort = n.Rule.Pick
		}
	}
	// A vision rule may choose a vision model even when the group has
	// text-only fallbacks. A failure must not send a current user image to
	// one of them. Historical images and tool results are omitted per
	// candidate below, so a text-only fallback can still answer those.
	// With a model to describe them, a text-only member is given the images
	// described instead.
	// Described only when such a member is tried: a rule that sends the
	// images to a model that sees asks for no description.
	var seenGroup func() ([]byte, error)
	if isGroup && hasImage(from, body) {
		if see, ok := seeing(); ok {
			seenGroup = sync.OnceValues(func() ([]byte, error) { return s.seenBody(r.Context(), from, body, see) })
		}
	}
	if isGroup && seenGroup == nil {
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
	tr := s.trace.begin(Route{Time: start, Agent: call.Agent, Kind: call.Kind, Model: call.Model, Effort: requestEffort(from, body), Provider: p.ID, Group: group, Rule: hit, Nested: nested, Affinity: shown, Order: pl.order, Left: pl.left})
	var skipped []string
	sent := ""        // the reasoning the last try's model was asked for
	where := ""       // the last try's provider.Where, for the usage
	again := 0        // times the last one left has been tried again
	resealed := false // the conversation's reasoning sealed by another account taken out
	floored := false  // the reply's length raised to what the provider takes
	for i := 0; i < len(cands); i++ {
		c := cands[i]
		last := i == len(cands)-1
		hw := newHoldWriter(w, !last || again < lastRetries)
		call.Provider, call.To, call.Usage = c.p.ID, "", Usage{}
		where = c.p.Where()
		began := time.Now()
		hw.first.start = began
		attemptBody := body
		if from == provider.Responses {
			// reasoning another sealed, which this one refused earlier in
			// the conversation, isn't sent to be refused again; its own is
			if b, ok := withoutRefused(stuck, c.who(), attemptBody); ok {
				attemptBody = b
			}
		}
		if isGroup {
			if in := membersImageInput([]provider.Member{{Provider: c.p, Model: c.model}}, nil); in != nil && !*in {
				if seenGroup != nil {
					b, err := seenGroup()
					if err != nil {
						// only an image of the latest turn fails to be described
						call.Error = "image not described"
						if !last {
							skipped = append(skipped, c.label()+": "+call.Error)
							continue
						}
						see, _ := seeing()
						call.Status = 502
						writeError(w, from, 502, fmt.Sprintf("model %q can't see images, and %s couldn't describe the image for it: %v", c.p.ID+"/"+c.model, see, err))
						break
					}
					attemptBody = b
				} else {
					attemptBody, _ = textOnlyBody(from, body) // omit images in prior turns and tool results
				}
			}
		}
		picked := false // the effort asked for in place of the agent's
		if c.effort != "" {
			// a member fixed at an effort (#189) is asked for it, at the
			// level its model has nearest, whatever the agent asked or the
			// turn's pick: even a request that asked for no reasoning
			attemptBody = withFixedEffort(from, attemptBody, fitLevel(c.effort, c.p.Efforts(c.model)))
		} else if effort != "" {
			// the level this model has nearest to the one picked; one whose
			// levels aren't known isn't asked for more than high, which
			// every vendor with levels takes
			if b := withEffort(from, attemptBody, fitLevel(effort, c.p.Efforts(c.model))); !bytes.Equal(b, attemptBody) {
				attemptBody, picked = b, true
			}
		}
		// the reasoning the model is asked for, whoever chose it
		sent = sentEffort(from, attemptBody, c.p, c.model)
		s.trace.update(tr, func(t *Route) {
			t.Tries = append(t.Tries, Try{ID: c.rest, Model: c.model, Effort: sent, Picked: picked, Fixed: c.effort, Start: began})
		})
		held := false // answered as its vendor did a moment ago, without asking
		if said, ok := verifyHeld(c.restKey()); ok && last {
			// the account must be verified first (#152): the agent's
			// reconnects are told so again, not sent on to a vendor that
			// just refused it
			held, call.Status, call.Error = true, http.StatusForbidden, said
			writeError(hw, from, call.Status, said)
		} else {
			call.Status, call.Error = s.attempt(hw, r, from, c.p, c.model, attemptBody, &call)
		}
		if hw.failure != 0 { // the stream failed before any of it was sent
			call.Status, call.Error = hw.failure, c.p.Name+": "+hw.failMsg
		}
		try := Try{ID: c.rest, Model: c.model, Effort: sent, Picked: picked, Fixed: c.effort, Start: began, Done: true, Status: call.Status, Millis: time.Since(began).Milliseconds(), Error: call.Error,
			Served: call.Usage.Served, Swapped: swapped(c.model, call.Usage.Served)}
		try.TTFT, try.FirstText = hw.first.ms()
		// the request's, from when it came as its ms are: the time before
		// this try, the ones that failed first, is in it
		call.TTFT, call.FirstText = sinceStart(began.Sub(start), hw.first.first), sinceStart(began.Sub(start), hw.first.text)
		if r.Context().Err() != nil && !hw.ended {
			// the agent went away: nobody failed, and nobody else is asked
			call.Status, call.Error = 499, "the agent canceled the request"
			try.Status, try.Error, try.Fail = call.Status, call.Error, failCanceled
			s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1] = try })
			break
		}
		if !resealed && from == provider.Responses && !hw.passing && hw.code() >= 400 && foreignReasoning.Match(hw.errBody()) {
			// the conversation moved here from another account or vendor,
			// whose sealed reasoning this one can't read: asked again
			// without it, and what it refused isn't sent here again. xAI
			// says so as a 400, or as the stream's error, which may be
			// read as another status
			if b, ok := withoutReasoning(body); ok {
				refused(stuck, c.who(), attemptBody)
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
			rest := s.restAfterMarked(c, hw.code(), hw.header, hw.errBody(), hw.sharedPool)
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
			servedCandidate(c, call.Usage.Input+call.Usage.Output+call.Usage.CacheRead+call.Usage.CacheWrite)
			answered(stuck, c, aff.Turn, call.Usage.CacheRead)
			if hit != nil {
				ruleAnswered(ruleAt, call.Usage)
			}
			for _, at := range nestedAt {
				ruleAnswered(at, call.Usage)
			}
		} else {
			try.Fail = failure(call.Status, []byte(call.Error))
			if try.Fail == failVerify && !held {
				// the last one left rests too, for the app to show and the
				// next requests to be held
				rest := s.restAfter(c, call.Status, hw.header, []byte(call.Error))
				try.Rest = &rest
			}
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
		t.Output, t.TTFT, t.FirstText = call.Usage.Output, call.TTFT, call.FirstText
		if n := len(t.Tries); n > 0 && call.Status < 400 {
			t.Served, t.Swapped = t.Tries[n-1].Served, t.Tries[n-1].Swapped
		}
	})
	s.record(call)
	if call.To != "" {
		usage.Append(usage.Record{Time: start, Agent: call.Agent, Provider: call.Provider, Host: where, Model: model,
			Requested: call.Model, Served: call.Usage.Served,
			Input: call.Usage.Input, Output: call.Usage.Output, CacheRead: call.Usage.CacheRead,
			CacheWrite: call.Usage.CacheWrite, Reasoning: call.Usage.Reasoning, Effort: sent, Millis: call.Millis, Status: call.Status,
			TTFT: call.TTFT, FirstText: call.FirstText, Session: sessionOf(r.Header), Kind: call.Kind})
	}
}

// sinceStart is a time d into a try that began before into the request,
// as ms from the request's start; 0 for none.
func sinceStart(before, d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	return (before + d).Milliseconds()
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
	// Cursor's through the API its CLI talks to, with the CLI's sign-in
	if p.Account != nil && p.Account.Agent == "cursor" {
		call.To = from
		return s.serveCursor(w, r, from, model, body, &call.Usage)
	}
	// Devin's through the API its CLI talks to, with the CLI's sign-in or
	// one magpie keeps beside it
	if p.Account != nil && p.Account.Agent == "devin" {
		call.To = from
		return s.serveDevin(w, r, from, p.Account.Home, model, body, &call.Usage)
	}
	// Kiro's is served through its own API, with kiro-cli's or the Kiro
	// IDE's sign-in, or a Kiro API key saved on the provider
	if p.Account != nil && p.Account.Agent == "kiro" {
		call.To = from
		return s.serveKiro(w, r, from, p, model, body, &call.Usage)
	}
	// Qoder is served through the API the client talks to, signed with the
	// COSY envelope, with the account magpie signed in to.
	if p.Account != nil && p.Account.Agent == "qoder" {
		call.To = from
		return s.serveQoder(w, r, from, p, model, body, &call.Usage)
	}
	// a backend that only streams gets a non-streaming request translated
	// (the provider is always streamed on that path) rather than relayed
	relay := slices.Contains(s.usable(p, model), from) && (p.Account == nil || !p.Account.Stream || streamOf(body))
	// a web search offered is done by the provider, or by magpie for it,
	// which a relayed request can't
	if relay && searchAsked(from, body) && (from == provider.Chat || !searchesItself(p, from)) {
		relay = false
	}
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

// markOpenRouterSharedPool keeps an upstream routing fact in the held attempt,
// rather than exposing it as a response header.
func markOpenRouterSharedPool(w http.ResponseWriter) {
	if h, ok := w.(*holdWriter); ok {
		h.sharedPool = true
	}
}

// forward sends a request to the provider. On Anthropic's messages, a
// provider that turns away betas it doesn't know by name (Bedrock's: 400
// Unexpected value(s) `x` for the `anthropic-beta` header) is asked again
// once without them, and they're left out for it from then on.
func (s *Server) forward(ctx context.Context, p provider.Provider, to provider.Protocol, path string, body []byte, in http.Header) (*http.Response, error) {
	res, err := s.forwardOnce(ctx, p, to, path, body, in)
	if err != nil || to != provider.Anthropic || res.StatusCode != http.StatusBadRequest {
		return res, err
	}
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	res.Body.Close()
	res.Body = io.NopCloser(bytes.NewReader(b))
	if len(s.refuseBetas(p, b)) == 0 {
		return res, nil
	}
	return s.forwardOnce(ctx, p, to, path, body, in)
}

// forwardOnce is one request to the provider, as forward makes it.
func (s *Server) forwardOnce(ctx context.Context, p provider.Provider, to provider.Protocol, path string, body []byte, in http.Header) (*http.Response, error) {
	if to == provider.Anthropic {
		body = s.bodyBetas(p, body)
	}
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
		if p.Account == nil && fromClaudeCode(in) {
			// a relay that serves only Claude Code (#179: "only accessible
			// via the official Claude CLI") knows it by its own headers,
			// which go on as it sent them; its key to magpie never does
			for k, vs := range in {
				if claudeCodeHeader(k) {
					req.Header[k] = slices.Clone(vs)
				}
			}
		}
		if bs := s.betas(p, in.Values("anthropic-beta")); len(bs) > 0 {
			req.Header.Set("anthropic-beta", strings.Join(bs, ","))
		} else {
			req.Header.Del("anthropic-beta")
		}
	}
	if p.IsOpenCode() {
		req.Header.Set("x-opencode-session", conversationID(in, body))
	}
	if p.Account != nil && p.Account.Agent == "codex" {
		// what Codex says about the request goes on as codexUpstream
		// relays it — a subagent's kind, the turn's metadata, a turn on
		// Luna Reserve — the account signing it being the pool's
		for k, vs := range in {
			if codexHeader(k) {
				req.Header[k] = slices.Clone(vs)
			}
		}
	}
	if err := p.Sign(ctx, req, to, body); err != nil {
		return nil, err
	}
	return s.client.Do(req)
}

// fromClaudeCode is a request Claude Code sent, by the User-Agent it gives
// (claude-cli/2.1.0 (external, cli)).
func fromClaudeCode(in http.Header) bool {
	return strings.HasPrefix(in.Get("User-Agent"), "claude-cli/")
}

// claudeCodeHeader is one of the headers Claude Code tells itself by: its
// User-Agent, x-app, the anthropic- ones, its SDK's X-Stainless- ones and
// its x-claude-code- ones. Never the key it was given.
func claudeCodeHeader(k string) bool {
	k = strings.ToLower(k)
	if k == "user-agent" || k == "x-app" {
		return true
	}
	for _, p := range []string{"anthropic-", "x-stainless-", "x-claude-code-"} {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	return false
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
		if strings.HasSuffix(p.Host(), "openai.com") {
			// Qwen's switch and Kimi Code's (thinking: {type: …}), which
			// OpenAI turns away as arguments it doesn't know
			body = withoutFields(body, "enable_thinking", "thinking")
		}
		if p.IsBedrock() {
			body = asCompletionTokens(body)
		}
	case provider.Anthropic:
		if p.IsBedrock() {
			// Claude Code's metadata.user_id, a JSON string these days, is
			// not the plain id Bedrock checks it against (#176)
			body = withoutFields(body, "metadata")
		}
		if !anthropicModel.MatchString(model) {
			body = thinkingOffUnlessAsked(body)
		}
		if effortInOutputConfig.MatchString(model) {
			body = withOutputEffort(body, p.Efforts(model))
		}
	}
	// the effort as the agent sent it, fitted to the model's levels: Qoder's
	// permission check asks "none", which Command Code turns away
	asked := bodyEffort(proto, body)
	if e := fitFor(p, model, asked); asked != "" && e != asked {
		body = withBodyEffort(proto, body, e)
	}
	path := pathOf(proto)
	if proto == provider.Anthropic && p.Account == nil && fromClaudeCode(r.Header) && r.URL.Query().Get("beta") == "true" {
		path += "?beta=true" // as Claude Code asks it
	}
	res, err := s.forward(r.Context(), p, proto, path, p.Prepare(body), r.Header)
	if err != nil {
		return writeError(w, proto, 502, p.Name+": "+err.Error()), err.Error(), true
	}
	if e := bodyEffort(proto, body); res.StatusCode == http.StatusBadRequest && (e == "none" || e == "minimal" || proto == provider.Chat && hasReasoningDisabled(body)) {
		// A model can refuse reasoning turned off. If it names its levels,
		// try low; if OpenRouter requires reasoning, leave the level to it.
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		res.Body = io.NopCloser(bytes.NewReader(b))
		if (e == "none" || e == "minimal") && effortLevelsNamed.Match(b) {
			if res, err = s.forward(r.Context(), p, proto, path, p.Prepare(withBodyEffort(proto, body, "low")), r.Header); err != nil {
				return writeError(w, proto, 502, p.Name+": "+err.Error()), err.Error(), true
			}
		} else if proto == provider.Chat && mandatoryReasoning.Match(b) {
			if nb, ok := withoutReasoningOff(body); ok {
				if res, err = s.forward(r.Context(), p, proto, path, p.Prepare(nb), r.Header); err != nil {
					return writeError(w, proto, 502, p.Name+": "+err.Error()), err.Error(), true
				}
			}
		}
	}
	if proto == provider.Chat && res.StatusCode == http.StatusBadRequest && bodyEffort(proto, body) != "none" {
		// tools with reasoning refused on chat, and no Responses API to
		// take them to: asked again without reasoning (#176)
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		res.Body = io.NopCloser(bytes.NewReader(b))
		if toolsWithoutEffort.Match(b) && !s.servesElsewhere(p, model, proto) {
			if res, err = s.forward(r.Context(), p, proto, path, p.Prepare(withBodyEffort(proto, body, "none")), r.Header); err != nil {
				return writeError(w, proto, 502, p.Name+": "+err.Error()), err.Error(), true
			}
		}
	}
	if proto == provider.Anthropic && res.StatusCode == http.StatusBadRequest {
		// a model that always thinks refuses thinking turned off: asked
		// again with it left to the model
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		res.Body = io.NopCloser(bytes.NewReader(b))
		if nb, ok := withoutThinkingOff(body); ok && (alwaysThinks.Match(b) || mandatoryReasoning.Match(b)) {
			if res, err = s.forward(r.Context(), p, proto, path, p.Prepare(nb), r.Header); err != nil {
				return writeError(w, proto, 502, p.Name+": "+err.Error()), err.Error(), true
			}
		}
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
		if p.Preset == "openrouter" && openRouterSharedPool(b) {
			markOpenRouterSharedPool(w)
		}
		keepRetry(w.Header(), res.Header, b)
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
	var tidy *chatTidy
	if proto == provider.Chat && sse {
		tidy = &chatTidy{}
	}
	buf := make([]byte, 32<<10)
	for {
		n, err := rd.Read(buf)
		if n > 0 {
			sniff.write(buf[:n])
			out := buf[:n]
			if tidy != nil {
				out = tidy.write(out)
			}
			if _, werr := w.Write(out); werr != nil {
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
	if tidy != nil {
		w.Write(tidy.flush())
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
	if p.ResponsesFirst(model) {
		sort.SliceStable(out, func(i, j int) bool { return out[i] == provider.Responses && out[j] != provider.Responses })
	}
	return out
}

// forwardTranslated sends one translated, streaming request upstream. A
// provider can serve a model on some of its endpoints and not others —
// OpenAI's and Copilot's newest models answer only /responses, Copilot's
// Claude models only /chat/completions — so when it says the model isn't
// served on this one, the request is built again for the next endpoint it
// speaks, and the model is remembered there.
func (s *Server) forwardTranslated(ctx context.Context, p provider.Provider, to provider.Protocol, req *Request, model string, in http.Header) (*http.Response, provider.Protocol, error) {
	if req.Effort != "" {
		if e := fitFor(p, model, req.Effort); e != req.Effort {
			r := *req
			r.Effort, req = e, &r
		}
	}
	web := req.WebSearch
	// the cache key was left out to see if it was what the upstream refused
	dropped := false
	for {
		// only a provider that searches by itself is asked to
		if want := web && searchesItself(p, to); want != req.WebSearch {
			r := *req
			r.WebSearch, req = want, &r
		}
		if req.CacheKey != "" && !s.fits(p.ID, cacheKeyField, to) {
			r := *req
			r.CacheKey, req = "", &r
		}
		body := build(to, req, model, p.Host(), p.RejectsTemperature(model))
		if to == provider.Chat && p.IsBedrock() {
			body = asCompletionTokens(body)
		}
		if to == provider.CodeAssist && p.Account != nil {
			body = buildCodeAssist(req, model, p.Account.Agent)
		}
		res, err := s.forward(ctx, p, to, pathOf(to), p.Prepare(body), in)
		if err != nil || res.StatusCode < 400 {
			if dropped && err == nil {
				// it was the key: not sent there again
				s.markUnfit(p.ID, cacheKeyField, to)
			}
			return res, to, err
		}
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		res.Body = io.NopCloser(bytes.NewReader(b))
		if to == provider.Chat && res.StatusCode == http.StatusBadRequest && req.Effort != "none" &&
			toolsWithoutEffort.Match(b) && !s.servesElsewhere(p, model, to) {
			// tools with reasoning refused on chat, and no Responses API
			// to take them to: asked again without reasoning (#176)
			r := *req
			r.Effort, req = "none", &r
			continue
		}
		if req.CacheKey != "" && badRequest(res.StatusCode) && !wrongEndpoint(res.StatusCode, b) {
			// a vendor that turns away fields it doesn't know is asked again
			// without the cache key, and not sent it again once that works —
			// at once when its error names the key; not every error does
			if refusesField(res.StatusCode, b, cacheKeyField) {
				s.markUnfit(p.ID, cacheKeyField, to)
			} else {
				dropped = true
			}
			r := *req
			r.CacheKey, req = "", &r
			continue
		}
		dropped = false
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

// toolsWithoutEffort is a chat completions endpoint refusing function tools
// with reasoning, which it takes on Responses or with reasoning_effort
// "none": Bedrock's for its GPT models (#176: "Function tools with
// reasoning_effort are not supported for global.openai.gpt-6-luna in
// /v1/chat/completions. To use function tools, use /v1/responses or set
// reasoning_effort to 'none'.").
var toolsWithoutEffort = regexp.MustCompile(`(?is)tools with reasoning_effort are not supported.*reasoning_effort to .?none`)

// OpenRouter rejects an explicit reasoning-off request for some endpoints.
var mandatoryReasoning = regexp.MustCompile(`(?i)reasoning is mandatory for this endpoint and cannot be disabled`)

// servesElsewhere reports whether p serves model on an API besides proto
// that hasn't turned it away.
func (s *Server) servesElsewhere(p provider.Provider, model string, proto provider.Protocol) bool {
	return slices.ContainsFunc(s.usable(p, model), func(x provider.Protocol) bool { return x != proto })
}

// cacheKeyField is the client's prompt cache key as a request carries it
// upstream; a provider that refused it is remembered under it in unfit.
const cacheKeyField = "prompt_cache_key"

// refusesField recognizes a vendor turning a request away for a field it
// doesn't take — Gemini's "Unknown name", Mistral's extra_forbidden,
// Groq's "unsupported" — by the field's name in the error.
func refusesField(status int, body []byte, field string) bool {
	return badRequest(status) && bytes.Contains(body, []byte(field))
}

// badRequest is a status an upstream refuses a request's contents with.
func badRequest(status int) bool {
	return status == http.StatusBadRequest || status == http.StatusUnprocessableEntity
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
		"is not supported for format",    // OpenCode: "Model grok-4.7 is not supported for format anthropic"
		"does not support this protocol", // OpenCode, with a key: "Model does not support this protocol"
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
	if from == provider.Responses && hasUnportableCurrentImage(body) {
		msg := "input_image without image_url cannot be translated; send an image_url or use a native Responses route"
		return writeError(w, from, 400, msg), msg
	}
	request, err := parse(from, body)
	if err != nil {
		return writeError(w, from, 400, err.Error()), err.Error()
	}
	if request.WebSearch && !searching(r.Context()) {
		// an API on which the provider searches by itself comes first;
		// without one, its model is given magpie's search
		for _, t := range s.usable(p, model) {
			if searchesItself(p, t) {
				to = t
				break
			}
		}
		if _, _, ok := searcher(); ok && !searchesItself(p, to) {
			return s.searchReply(w, r, from, p.Name, request, u, s.askTranslated(p, to, model, r.Header, w.Header()))
		}
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
		if p.Preset == "openrouter" && openRouterSharedPool(b) {
			markOpenRouterSharedPool(w)
		}
		keepRetry(w.Header(), res.Header, b)
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
		enc := encoder(from, newSSEWriter(w), request)
		var failed string
		serr := readSSE(rd, func(_, data string) error {
			return dec(data, func(ev Event) {
				switch ev.Kind {
				case KError:
					failed = ev.Text
				case KStart, KUsage:
					u.add(ev.Usage)
					u.add(Usage{Served: ev.Model}) // the model the vendor says answered
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
	u.add(Usage{Served: res2.Model})
	out := render(from, res2, request)
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

// parse is shared by translated routes and account/subscription backends.
// Only the allowlist contracts below promise a required callable function:
// other protocols have server, custom and MCP tools the IR cannot render.
func parse(proto provider.Protocol, body []byte) (*Request, error) {
	var req *Request
	var err error
	switch proto {
	case provider.Chat:
		req, err = parseChat(body)
	case provider.Responses:
		req, err = parseResponses(body)
	case provider.Gemini:
		req, err = parseGemini(body)
	default:
		req, err = parseAnthropic(body)
	}
	if err != nil {
		return nil, err
	}
	if req.ToolChoice == "required" && len(req.Tools) == 0 && !req.WebSearch && requiredAllowlist(proto, body) {
		return nil, fmt.Errorf("required tool choice has no callable tools after filtering")
	}
	return req, nil
}

func requiredAllowlist(proto provider.Protocol, body []byte) bool {
	switch proto {
	case provider.Responses:
		var q struct {
			ToolChoice struct{ Type, Mode string } `json:"tool_choice"`
		}
		return json.Unmarshal(body, &q) == nil && q.ToolChoice.Type == "allowed_tools" && q.ToolChoice.Mode == "required"
	case provider.Gemini:
		var q struct {
			ToolConfig struct {
				FunctionCallingConfig struct {
					Mode                 string   `json:"mode"`
					AllowedFunctionNames []string `json:"allowedFunctionNames"`
				} `json:"functionCallingConfig"`
			} `json:"toolConfig"`
		}
		return json.Unmarshal(body, &q) == nil && strings.EqualFold(q.ToolConfig.FunctionCallingConfig.Mode, "ANY") && len(q.ToolConfig.FunctionCallingConfig.AllowedFunctionNames) > 0
	}
	return false
}

func build(proto provider.Protocol, r *Request, model, host string, rejectTemp bool) []byte {
	switch proto {
	case provider.Chat:
		return buildChat(r, model, host, rejectTemp)
	case provider.Responses:
		return buildResponses(r, model, host, rejectTemp)
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
	d := &anthropicDecoder{server: map[int]bool{}}
	return d.decode
}

type streamEncoder interface {
	event(Event)
	finish()
}

func encoder(proto provider.Protocol, w *sseWriter, r *Request) streamEncoder {
	model := r.Model
	switch proto {
	case provider.Chat:
		return &chatEncoder{w: w, model: model}
	case provider.Responses:
		return &responsesEncoder{w: w, model: model, named: r.Namespaced}
	case provider.Gemini:
		return &geminiEncoder{w: w, model: model}
	}
	return &anthropicEncoder{w: w, model: model}
}

func render(proto provider.Protocol, res Result, r *Request) []byte {
	model := r.Model
	switch proto {
	case provider.Chat:
		return renderChat(res, model)
	case provider.Responses:
		return renderResponses(res, model, r.Namespaced)
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

// decodeRequest requires one complete JSON object before fields are rewritten
// or a request is routed. In particular, null is not an empty object.
func decodeRequest(body []byte, dst any) error {
	body = bytes.TrimSpace(body)
	if len(body) == 0 || body[0] != '{' {
		return errors.New("invalid request: expected a JSON object")
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("invalid request: %w", err)
	}
	return nil
}

func validateModel(model string) (string, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return "", errors.New("invalid request: model must be a nonempty string")
	}
	if p, m, ok := strings.Cut(model, "/"); ok && (p == "" || m == "") {
		return "", errors.New("invalid request: expected provider/model with both parts nonempty")
	}
	return model, nil
}

// requestModel checks the envelope without restricting vendor-specific fields.
// Only a model with surrounding whitespace needs its body rewritten.
func requestModel(body []byte) ([]byte, string, error) {
	var q struct {
		Model string `json:"model"`
	}
	if err := decodeRequest(body, &q); err != nil {
		return nil, "", err
	}
	model, err := validateModel(q.Model)
	if err != nil {
		return nil, "", err
	}
	if model != q.Model {
		body = withModel(body, model)
	}
	return body, model, nil
}

func modelOf(body []byte) string {
	var v struct {
		Model string `json:"model"`
	}
	json.Unmarshal(body, &v)
	return v.Model
}

// rewriteModel swaps the model field, keeping every other byte as it was:
// the fields in their order, and the text unescaped. A relay that only lets
// Claude Code in (packy) takes a body with its fields sorted and its < and >
// escaped, as re-encoding leaves it, for one tampered with (#179).
func rewriteModel(body []byte, model string) []byte {
	v := gjson.GetBytes(body, "model")
	if v.Type == gjson.String && v.Str == model {
		return body
	}
	if v.Type != gjson.String || v.Index <= 0 || !gjson.ValidBytes(body) {
		return withFields(body, map[string]any{"model": model})
	}
	name, _ := json.Marshal(model)
	out := make([]byte, 0, len(body)+len(name))
	out = append(out, body[:v.Index]...)
	out = append(out, name...)
	return append(out, body[v.Index+len(v.Raw):]...)
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

// SessionHeader names the session a call is part of, for the usage log to
// tell several sessions on one model apart; without it, the session an
// agent names itself in sessionHeaders is taken.
const SessionHeader = "X-Magpie-Session"

// sessionOf is the session a request names, "" when it names none.
func sessionOf(in http.Header) string {
	for _, h := range append([]string{SessionHeader}, sessionHeaders...) {
		if v := strings.TrimSpace(in.Get(h)); v != "" {
			if len(v) > 128 {
				v = v[:128]
			}
			return v
		}
	}
	return ""
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
		Contents []json.RawMessage `json:"contents"`
	}
	// An undecodable body still gets an id: the hash of the whole body.
	_ = json.Unmarshal(body, &m)
	items := m.Messages
	geminiContents := len(items) == 0 && len(m.Contents) > 0
	if geminiContents {
		items = m.Contents // Gemini generateContent repeats the first user turn.
	}
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
		// Gemini treats an omitted role as user; Chat and Responses do not.
		if json.Unmarshal(it, &r) == nil && (r.Role == "user" || (geminiContents && r.Role == "")) {
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

// effortLevelsNamed is an error that lists the reasoning levels a model
// takes, as one refusing "none" does: Command Code's `expected one of
// "low"|"medium"|"high"|"xhigh"|"max"`.
var effortLevelsNamed = regexp.MustCompile(`(?i)\blow\b\W+(?:medium|high)\b`)

// bodyEffort is the reasoning effort a Chat or Responses request asks for,
// or an Anthropic one in its output_config.
func bodyEffort(proto provider.Protocol, body []byte) string {
	var v struct {
		ReasoningEffort string `json:"reasoning_effort"`
		Reasoning       *struct {
			Effort string `json:"effort"`
		} `json:"reasoning"`
		OutputConfig *struct {
			Effort string `json:"effort"`
		} `json:"output_config"`
	}
	if json.Unmarshal(body, &v) != nil {
		return ""
	}
	switch {
	case proto == provider.Chat:
		return v.ReasoningEffort
	case proto == provider.Responses && v.Reasoning != nil:
		return v.Reasoning.Effort
	case proto == provider.Anthropic && v.OutputConfig != nil:
		return v.OutputConfig.Effort
	}
	return ""
}

// withBodyEffort asks a Chat, Responses or Anthropic request for effort
// instead, keeping the rest of its reasoning settings. Anthropic's is
// output_config.effort, which Claude Desktop sends at any of low…max
// whatever the model takes.
func withBodyEffort(proto provider.Protocol, body []byte, effort string) []byte {
	switch proto {
	case provider.Anthropic:
		var v struct {
			OutputConfig map[string]any `json:"output_config"`
		}
		if json.Unmarshal(body, &v) != nil || v.OutputConfig == nil {
			return body
		}
		v.OutputConfig["effort"] = effort
		return withFields(body, map[string]any{"output_config": v.OutputConfig})
	case provider.Chat:
		return withFields(body, map[string]any{"reasoning_effort": effort})
	case provider.Responses:
		var v struct {
			Reasoning map[string]any `json:"reasoning"`
		}
		if json.Unmarshal(body, &v) != nil || v.Reasoning == nil {
			return body
		}
		v.Reasoning["effort"] = effort
		return withFields(body, map[string]any{"reasoning": v.Reasoning})
	}
	return body
}

func hasReasoningDisabled(body []byte) bool {
	_, ok := withoutReasoningOff(body)
	return ok
}

// withoutReasoningOff removes only explicit Chat reasoning-off settings.
// Leaving the effort to the model avoids guessing which nonzero level it
// accepts when OpenRouter says reasoning is mandatory.
func withoutReasoningOff(body []byte) ([]byte, bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil {
		return nil, false
	}
	changed := false
	var effort string
	if json.Unmarshal(fields["reasoning_effort"], &effort) == nil && strings.EqualFold(effort, "none") {
		delete(fields, "reasoning_effort")
		changed = true
	}
	var reasoning map[string]json.RawMessage
	if json.Unmarshal(fields["reasoning"], &reasoning) == nil && reasoning != nil {
		var enabled bool
		if json.Unmarshal(reasoning["enabled"], &enabled) == nil && !enabled {
			delete(reasoning, "enabled")
			changed = true
		}
		if json.Unmarshal(reasoning["effort"], &effort) == nil && strings.EqualFold(effort, "none") {
			delete(reasoning, "effort")
			changed = true
		}
		if len(reasoning) == 0 {
			delete(fields, "reasoning")
		} else if changed {
			fields["reasoning"], _ = json.Marshal(reasoning)
		}
	}
	if !changed {
		return nil, false
	}
	out, err := json.Marshal(fields)
	return out, err == nil
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
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false) // <, > and & as the agent wrote them
	if enc.Encode(m) != nil {
		return body
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n"))
}

// withoutFields drops fields the vendor refuses to see: Qoder asks every
// model for Qwen's enable_thinking, which OpenAI turns away as an
// unrecognized argument.
// asCompletionTokens asks a chat request's reply length as
// max_completion_tokens in place of max_tokens, as Bedrock's OpenAI
// endpoint takes it: its GPT models turn max_tokens away (#176).
func asCompletionTokens(body []byte) []byte {
	if !bytes.Contains(body, []byte(`"max_tokens"`)) {
		return body
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if dec.Decode(&m) != nil {
		return body
	}
	n, ok := m["max_tokens"]
	if !ok {
		return body
	}
	delete(m, "max_tokens")
	if _, has := m["max_completion_tokens"]; !has {
		m["max_completion_tokens"] = n
	}
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

func withoutFields(body []byte, fields ...string) []byte {
	found := false
	for _, f := range fields {
		found = found || bytes.Contains(body, []byte(`"`+f+`"`))
	}
	if !found {
		return body
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return body
	}
	for _, f := range fields {
		delete(m, f)
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
