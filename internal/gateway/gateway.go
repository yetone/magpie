package gateway

import (
	"bufio"
	"bytes"
	"cmp"
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
	"net/http/httptrace"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/awake"

	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/redact"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

// DefaultAddr is where the gateway listens unless Settings' port or
// MAGPIE_ADDR says otherwise.
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
	ua := r.Header.Get("User-Agent")
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(ua)), "cursor") {
		// cursor-agent talks to Cursor's backend alone, never to a
		// gateway: a Cursor User-Agent here is Cursor Private Inference's
		// ("Cursor-CLI/…") sent with a key other than its own, the one
		// typed in its Open configuration or a model's settings ("magpie"),
		// and it is shown its models as with its key (mamba on Discord)
		return "cursor-local"
	}
	return usage.AgentOf(ua)
}

// StandIn is the model an agent is set to use in place of one it named that
// magpie doesn't serve it: Claude Code asks for claude-haiku-… by name for
// its small tasks, whatever its haiku tier is set to, and that goes to the
// tier's model rather than to some provider that happens to list the name.
// "" leaves the model as asked. Set by main.
var StandIn func(agent, model string) string

// wiresKey carries the upstream names in force on a request's context: read
// once where the request comes in rather than once per place a name is
// looked up, since settings.Load reads and parses the whole file every call
// and one chat request looks names up in several places — the id a try went
// out under, the body the vendor is sent, the token count asked of it. The
// names of one request are then one set, the way the ledger reads them once
// for a whole report.
type wiresKey struct{}

// withWires carries the names in force on r's context, read at most once and
// only where something asks for one: a request refused before any name is
// looked up pays for nothing.
func withWires(r *http.Request) *http.Request {
	load := sync.OnceValue(func() map[string]string { return settings.Load().ModelWires })
	return r.WithContext(context.WithValue(r.Context(), wiresKey{}, load))
}

// wiresOf is the names in force for ctx: the ones withWires read, or the
// ones in force now for a request magpie made for itself and never carried
// them on.
func wiresOf(ctx context.Context) map[string]string {
	if load, ok := ctx.Value(wiresKey{}).(func() map[string]string); ok {
		return load()
	}
	return settings.Load().ModelWires
}

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

// claudeFamily is a full id of one of the model families Claude Code has a
// tier for, as Claude Code or a wrapper of it names one: claude-sonnet-4-6,
// claude-fable-5-1, claude-haiku-4-5-20251001, claude-3-5-sonnet-20241022,
// whatever the version, matched by family so a release magpie has never
// heard of is one too. A provider's "a/claude-…" is not one.
var claudeFamily = regexp.MustCompile(`^claude-(?:[0-9][0-9.-]*-)?(?:opus|sonnet|haiku|fable)(?:[-.@:]|$)`)

// claudeTierStandIn is the model Claude Code is set to use for the tier of
// a full Claude id it names (StandIn), even one magpie serves: Claude Code
// asks for claude-haiku-… by name for its small tasks, and a wrapper (T3
// Code, KevinXC on Discord) asks for every model by its full id, so
// claude-sonnet-5 went to the Claude account magpie serves it on, spent,
// while claude-sonnet-4-6, which no provider of the user's listed, went to
// the sonnet tier. Every id of a family now goes to its tier, as Claude
// Code's own aliases do. A routing group of the user's own named by the id
// still has it; one magpie found (auto-…) was never picked for it. "" when
// the id is no such one, the agent isn't Claude Code, or Claude Code isn't
// routed through magpie (StandIn says nothing).
func claudeTierStandIn(agent, asked string) string {
	if StandIn == nil || agent != "claude" || !claudeFamily.MatchString(strings.ToLower(strings.TrimSuffix(asked, "[1m]"))) {
		return ""
	}
	if gid, ok := provider.GroupFor(asked); ok && !strings.HasPrefix(gid, provider.GroupPrefix+"auto-") {
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

// Addr is the listen address: MAGPIE_ADDR, else Settings' port on
// loopback (settings.GatewayAddr).
func Addr() string { return settings.GatewayAddr() }

// SavedURL is URL by Settings alone, MAGPIE_ADDR left aside: where the
// installed magpie, whose settings these are, is reached.
func SavedURL() string { return "http://" + settings.SavedAddr() }

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
	s := ServedBy()
	return s.Running, s.Window
}

// Served is what the magpie answering at the gateway's address says of
// itself.
type Served struct {
	Running bool   // a magpie gateway answers
	Window  bool   // its routing can be watched in its window (#110)
	Version string // its version; "" from one that doesn't say
}

// ServedBy asks the address which magpie answers there. Another magpie can
// keep the port after this one is updated (a magpie serve left running, a
// second copy, one that didn't quit), and every agent's request is then
// still that one's to send: in #506 a Factory 403 came worded as v0.1.550
// and earlier word it to two people whose magpie said v0.1.630, so none of
// the fixes since reached their requests. Its version tells it apart.
func ServedBy() Served {
	c := &http.Client{Timeout: 700 * time.Millisecond}
	res, err := c.Get(URL() + "/")
	if err != nil {
		return Served{}
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	var info struct {
		Name    string `json:"name"`
		Version string `json:"version"`
		Window  bool   `json:"window"`
	}
	if json.Unmarshal(b, &info) != nil || info.Name != "magpie" {
		return Served{Running: bytes.Contains(b, []byte(`"magpie"`)), Window: bytes.Contains(b, []byte(`"window":true`))}
	}
	return Served{Running: true, Window: info.Window, Version: info.Version}
}

// Call is one request the gateway handled, for the status views.
type Call struct {
	Time  time.Time `json:"time"`
	Agent string    `json:"agent"`         // who called, from the client's User-Agent
	Via   string    `json:"via,omitempty"` // the computer a remote magpie's request came from (AgentHeader)
	// Kind: what the agent made the call for, when it isn't its turn —
	// a Codex subagent's (callKind) — "" for a turn
	Kind string `json:"kind,omitempty"`
	// For: the request a call of magpie's own (a web search) was made for
	For      *CallFor          `json:"for,omitempty"`
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
	// Archive: "<date>/<id>", where the request archive keeps the call,
	// when it was on (archive.go); wire what it keeps besides
	Archive string `json:"archive,omitempty"`
	wire    *wire
	// otelIn/otelOut are the whole bodies for the OTLP export when it asks
	// for them uncut (#538): nil leaves it to the 256 KiB bodies above,
	// which is all the Recent-calls page keeps
	otelIn, otelOut       []byte
	otelInCut, otelOutCut bool
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
	titlePrompts titlePrompts
	// codexTurns is the model each Codex last had a turn of its own
	// answered on, by caller (codexMemoryStandIn)
	codexTurns sync.Map
	// the listener, swapped when the gateway is shared on the network or
	// taken off it (see Relisten)
	lnMu sync.Mutex
	ln   net.Listener
	// the images described for models that can't see them (vision.go)
	sightMu    sync.Mutex
	sights     map[string]*sight
	sightOrder []string
	// the requests out at each key or account with a MaxConcurrency, and
	// those waiting their turn (concurrency.go)
	lanes         lanes
	requestLimits requestLimits
	// what a restart would cut short (busy.go)
	busyCounters
}

// New makes a gateway.
func New() *Server {
	redact.SetKeyPath(filepath.Join(settings.Dir(), "redact.key"))
	return &Server{
		// a provider with a proxy of its own is sent through a transport
		// kept for that proxy (#237)
		client: &http.Client{Transport: netproxy.Dispatch(&http.Transport{
			Proxy:                 netproxy.Func,
			ResponseHeaderTimeout: 10 * time.Minute,
			MaxIdleConnsPerHost:   8,
			IdleConnTimeout:       90 * time.Second,
			ForceAttemptHTTP2:     true,
		})},
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
	// Whole bodies belong only to the export, never to the Recent-calls ring.
	// This is a copy: the serving call still needs them for withBodies.
	c.otelIn, c.otelOut = nil, nil
	if c.wire != nil {
		c.Archive = c.wire.name
		archive(c)
		c.wire = nil
	}
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
	migrateLANKeyBestEffort()
	ln, err := Listen(listenAddr())
	if err != nil {
		return err
	}
	s.lnMu.Lock()
	s.ln = ln
	s.lnMu.Unlock()
	stopOTel := usage.StartOTel()
	defer stopOTel()
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
	// and spends a Codex reset about to run out unused, for the accounts
	// that let it
	go provider.KeepResetsFromRunningOut(ctx)
	// and checks the WorkBuddy accounts in for the day's credits
	go provider.KeepWorkBuddyCheckedIn(ctx)
	// and the Trae CN accounts (#694), and the MiniMax Code ones (#811)
	go provider.KeepTraeCheckedIn(ctx)
	go provider.KeepMiniMaxCheckedIn(ctx)
	// and the Qoder ones' daily credits
	go provider.KeepQoderCheckedIn(ctx)
	// and the accounts of the plugins that check in themselves
	go provider.KeepPluginsCheckedIn(ctx)
	// and moves the built-in subscriptions being retired onto their plugins
	go provider.KeepRetiringMoved(ctx)
	// and keeps the community's plugins up to date, noting others' updates, and the Bun they run on
	go plugin.KeepUpdated(ctx)
	go plugin.KeepBunUpdated(ctx)
	// and, when settings say to, keeps the computer awake while agents work
	go awake.Keep(ctx, func() awake.Level {
		switch set := settings.Load(); {
		case !set.KeepAwake:
			return awake.Off
		case set.KeepAwakeDisplay:
			return awake.Display
		}
		return awake.System
	}, func() awake.State {
		b := s.Busy()
		return awake.State{Busy: b.Any(), Last: b.Last}
	})
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
	// an OpenAI-compatible base URL answers too: Empryo lists a provider with
	// no key of its own only when its baseURL does
	mux.HandleFunc("GET /v1", s.info)
	mux.HandleFunc("GET /v1/{$}", s.info)
	mux.HandleFunc("GET /v1/models", s.models)
	mux.HandleFunc("GET /models", s.models)
	mux.HandleFunc("GET /v1/models/{id...}", s.model)
	mux.HandleFunc("GET /muse-code/models", s.museModels)
	// Claude Desktop's third-party gateway looks for one here before it
	// takes the address
	mux.HandleFunc("GET /api/hello", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"name": "magpie", "version": Version})
	})
	mux.HandleFunc("GET /v1/magpie/quotas", s.quotas)
	mux.HandleFunc("GET /v1/magpie/quotas/history", s.quotasHistory)
	mux.HandleFunc("GET "+provider.RemoteCardsPath, s.quotaCards)
	mux.HandleFunc("POST "+provider.RemoteRefreshPath, s.quotaCardsRefresh)
	mux.HandleFunc("GET /v1/magpie/route", s.sessionRoute)
	mux.HandleFunc("GET /v1/magpie/concurrency", s.concurrency)
	mux.HandleFunc("GET /v1/magpie/limit", s.keyLimit)
	mux.HandleFunc("GET /v1/magpie/omarchy", s.omarchyTheme)
	mux.HandleFunc("OPTIONS /v1/magpie/omarchy", s.omarchyTheme)
	mux.HandleFunc("POST /v1/chat/completions", s.handle(provider.Chat))
	mux.HandleFunc("POST /chat/completions", s.handle(provider.Chat))
	mux.HandleFunc("POST /v1/responses", s.handle(provider.Responses))
	mux.HandleFunc("POST /responses", s.handle(provider.Responses))
	mux.HandleFunc("GET /v1/responses", responsesOverHTTP)
	mux.HandleFunc("GET /responses", responsesOverHTTP)
	mux.HandleFunc("POST /v1/messages", keepsSignIn(s.handle(provider.Anthropic)))
	mux.HandleFunc("POST /messages", keepsSignIn(s.handle(provider.Anthropic)))
	mux.HandleFunc("POST /v1/systemone", s.serveSystemOne)
	mux.HandleFunc("POST /v1/messages/count_tokens", keepsSignIn(s.countTokens))
	mux.HandleFunc("POST /v1/images/generations", s.images(false))
	mux.HandleFunc("POST /images/generations", s.images(false))
	mux.HandleFunc("POST /v1/images/edits", s.images(true))
	mux.HandleFunc("POST /images/edits", s.images(true))
	mux.HandleFunc("POST /v1/embeddings", s.retrieve("/embeddings", "embeddings"))
	mux.HandleFunc("POST /embeddings", s.retrieve("/embeddings", "embeddings"))
	mux.HandleFunc("POST /v1/rerank", s.retrieve("/rerank", "rerank"))
	mux.HandleFunc("POST /rerank", s.retrieve("/rerank", "rerank"))
	mux.HandleFunc("POST /v1/videos", s.videosCreate)
	mux.HandleFunc("POST /videos", s.videosCreate)
	mux.HandleFunc("GET /v1/videos/{id}", s.videosGet)
	mux.HandleFunc("GET /videos/{id}", s.videosGet)
	mux.HandleFunc("GET /v1/videos/{id}/content", s.videosContent)
	mux.HandleFunc("GET /videos/{id}/content", s.videosContent)
	mux.HandleFunc("POST /_magpie/claude-mcp/{token}", s.subscription.mcpCall)
	mux.HandleFunc("/mcp/{name}", s.mcpProxy)
	mux.HandleFunc(CodexPath+"/", s.codexBackend)
	mux.HandleFunc("GET /v1beta/models", s.geminiModels)
	mux.HandleFunc("POST /v1beta/models/{call...}", s.gemini)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, provider.Chat, http.StatusNotFound, "magpie serves /v1/chat/completions, /v1/responses, /v1/messages, /v1/systemone, /v1/images/generations, /v1/images/edits, /v1/videos, /v1/embeddings, /v1/rerank and /v1beta/models/*")
	})
	return s.counted(corsGuard(callerGuard(withCaller(keyLimited(mux)))))
}

// responsesOverHTTP answers a GET on /v1/responses, which is how a client
// opens Responses over a WebSocket (#1005): magpie relays Responses over
// HTTP alone, its stream as SSE, so the upgrade is told 426 — the answer a
// client falls back to HTTP on at once (Codex does, as on CodexPath) — in
// place of the 404 every unknown path gets, which read as a broken
// gateway. A plain GET is told to POST.
func responsesOverHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		writeError(w, provider.Responses, http.StatusUpgradeRequired, "magpie relays Responses over HTTP only: send POST /v1/responses (streamed as SSE); Responses over a WebSocket is not served")
		return
	}
	w.Header().Set("Allow", "POST")
	writeError(w, provider.Responses, http.StatusMethodNotAllowed, "POST /v1/responses")
}

func (s *Server) info(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"name": "magpie", "version": Version, "models": len(provider.Catalog()), "window": Window,
		"apis": []string{"/v1/chat/completions", "/v1/responses", "/v1/messages", "/v1/systemone", "/v1beta/models/{model}:generateContent", "/v1/images/generations", "/v1/images/edits", "/v1/videos", "/v1/embeddings", "/v1/rerank", "/v1/magpie/quotas", "/v1/magpie/quotas/history", "/v1/magpie/route"}})
}

// quotas is what is left of every subscription, plan and key magpie has,
// for an agent choosing where to send its work (magpie quota --json is the
// same). It names the accounts and their balances, so it answers this
// machine, and another only with the key of the gateway shared on the
// local network — never a gateway MAGPIE_ADDR opens without one.
func (s *Server) quotas(w http.ResponseWriter, r *http.Request) {
	if !local(r) && !sharedWith(r) {
		writeError(w, provider.Chat, http.StatusForbidden, "magpie's quotas are told to another machine only when magpie is shared on the local network (Settings → Share on local network) and the request carries its API key (Authorization: Bearer <key> or x-api-key: <key>)")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	writeJSON(w, 200, map[string]any{"object": "list", "data": provider.QuotaReport(ctx, time.Now())})
}

// quotasHistory is what was left of each account's windows over time, as
// this magpie (and the computers sharing usage with it) read them (#651):
// ?days= back (45 at most), ?provider= and ?user= to pick an account.
// /v1/magpie/quotas stays the snapshot.
func (s *Server) quotasHistory(w http.ResponseWriter, r *http.Request) {
	if !local(r) && !sharedWith(r) {
		writeError(w, provider.Chat, http.StatusForbidden, "magpie's quotas are told to another machine only when magpie is shared on the local network (Settings → Share on local network) and the request carries its API key (Authorization: Bearer <key> or x-api-key: <key>)")
		return
	}
	q := r.URL.Query()
	writeJSON(w, 200, map[string]any{"object": "list", "data": provider.QuotaHistories(provider.QuotaHistorySince(q.Get("days"), time.Now()), q.Get("provider"), q.Get("user"))})
}

// quotaCards is the Usage page's cards as this magpie last read them, for
// another magpie that has this one as its provider (remote-magpie) to
// show: its own cache, however old, and no vendor asked for it.
func (s *Server) quotaCards(w http.ResponseWriter, r *http.Request) {
	if !local(r) && !sharedWith(r) {
		writeError(w, provider.Chat, http.StatusForbidden, "magpie's quotas are told to another machine only when magpie is shared on the local network (Settings → Share on local network) and the request carries its API key (Authorization: Bearer <key> or x-api-key: <key>)")
		return
	}
	writeJSON(w, 200, map[string]any{"object": "list", "data": provider.CachedCards(time.Now())})
}

// remoteRefreshes is when another magpie last had each card read again
// here: one refreshed by hand is read at most once in remoteRefreshGap,
// however often it is asked, so the vendor sees no more than this
// computer's own refresh button would make it.
var remoteRefreshes = struct {
	sync.Mutex
	at map[string]time.Time
}{at: map[string]time.Time{}}

const remoteRefreshGap = 30 * time.Second

// quotaCardsRefresh is a card's refresh pressed on another magpie: the
// card (?provider=, &user=) is read again here, or every card when none
// is named, then the cards are answered as quotaCards answers them.
func (s *Server) quotaCardsRefresh(w http.ResponseWriter, r *http.Request) {
	if !local(r) && !sharedWith(r) {
		writeError(w, provider.Chat, http.StatusForbidden, "magpie's quotas are told to another machine only when magpie is shared on the local network (Settings → Share on local network) and the request carries its API key (Authorization: Bearer <key> or x-api-key: <key>)")
		return
	}
	id, user := r.URL.Query().Get("provider"), r.URL.Query().Get("user")
	if strings.Contains(id, "/") || provider.IsRemoteCard(id) { // a card this magpie has from another: not passed on
		writeError(w, provider.Chat, http.StatusBadRequest, "this magpie reads only its own cards again")
		return
	}
	key := id + "|" + strings.ToLower(user)
	remoteRefreshes.Lock()
	due := time.Since(remoteRefreshes.at[key]) >= remoteRefreshGap
	if due {
		remoteRefreshes.at[key] = time.Now()
	}
	remoteRefreshes.Unlock()
	if due {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		if id == "" {
			provider.ReadAllCards(ctx)
		} else {
			provider.RefreshUsage(ctx, id, user)
		}
		cancel()
	}
	writeJSON(w, 200, map[string]any{"object": "list", "data": provider.CachedCards(time.Now())})
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
		"owned_by": e.Provider.ID, "display_name": e.Name, "reasoning": e.Reasoning || len(levels) > 0, "supported_reasoning_levels": levels}
	// the window, as the names clients read it by: a group's context
	// (magpie group set … context=) included
	if e.Context > 0 {
		m["context_window"], m["context_length"], m["max_input_tokens"] = e.Context, e.Context, e.Context
	}
	if e.Output > 0 {
		m["max_output_tokens"] = e.Output
	}
	// for another magpie that has this one as its provider (remote-magpie):
	// the APIs a request for the model goes on as it is, so it sends each
	// one on an API of these rather than having it translated twice, and
	// whether it takes images
	if native := nativeEndpoints(e); len(native) > 0 {
		m["native_endpoints"] = native
	}
	if e.Images {
		m["modalities"] = map[string]any{"input": []string{"text", "image"}}
	} else if e.ImageInput != nil {
		m["modalities"] = map[string]any{"input": []string{"text"}}
	}
	return m
}

// cursorLocalModel adds what Cursor Private Inference reads of a model
// (#299): api_types, the APIs it may ask it on — those the model is relayed
// on as it is, so a Claude served on Anthropic Messages is asked there and
// keeps its thinking; any, for one translated anyway — and capabilities,
// its window, output and whether it takes images, without which it falls
// back to Chat and limits of its own.
//
// Its picker (cursor-agent-exec, 3.23.12) keeps a row's capabilities only
// when they say it streams, calls tools and writes text (supports_tool_use,
// supports_streaming, output_modalities); without them it shows the model
// with no Reasoning control (mamba on Discord). Which levels it offers it
// decides itself, from the id after its last "/" (claude-opus-4-7, gpt-5.5,
// gemini-*, which needs supports_reasoning), and it sends a Claude's level
// only on Anthropic Messages, so a Claude any API would do is asked there.
func cursorLocalModel(m map[string]any, e provider.Entry) {
	names := map[string]string{"/v1/chat/completions": "chat_completions", "/v1/responses": "responses", "/v1/messages": "anthropic_messages"}
	types := []string{}
	for _, p := range nativeEndpoints(e) {
		types = append(types, names[p])
	}
	if len(types) == 0 {
		types = []string{"chat_completions", "responses", "anthropic_messages"}
	}
	bare := strings.ToLower(e.ID[strings.LastIndex(e.ID, "/")+1:])
	if strings.HasPrefix(bare, "claude-") && slices.Contains(types, "anthropic_messages") {
		types = []string{"anthropic_messages"}
	}
	m["api_types"] = types
	c := map[string]any{"supports_tool_use": true, "supports_streaming": true, "output_modalities": []string{"text"},
		"supports_reasoning": e.Reasoning || len(e.Efforts) > 0}
	if len(e.Efforts) > 0 {
		c["reasoning_effort"] = e.Efforts
	}
	if e.Context > 0 {
		c["context_length"] = e.Context
	}
	if e.Output > 0 {
		c["max_output_tokens"] = e.Output
	}
	if e.Images {
		c["supports_vision"] = true
	} else if e.ImageInput != nil {
		c["supports_vision"] = false
	}
	m["capabilities"] = c
}

// nativeEndpoints are the paths a request for the model is relayed on to
// its provider as it is: the APIs the provider serves it on. None for a
// routing group, whose members may speak any, or a model every request
// to is translated anyway (a subscription served through its agent's own
// API).
func nativeEndpoints(e provider.Entry) []string {
	p := e.Provider
	if e.Group != "" || p.Native(e.Model) == "" {
		return nil
	}
	apis := p.APIs(e.Model)
	var out []string
	for _, pr := range p.Speaks() {
		if slices.Contains(provider.Protocols, pr) && (apis == nil || slices.Contains(apis, pr)) {
			out = append(out, map[provider.Protocol]string{provider.Chat: "/v1/chat/completions", provider.Responses: "/v1/responses", provider.Anthropic: "/v1/messages"}[pr])
		}
	}
	return out
}

// drawerObjects are the image models magpie draws with, as another magpie
// asks for them in its list (provider.DrawersHeader): "kind": "image",
// which an agent's list never has, so it takes none for a model to chat
// with.
func drawerObjects() []map[string]any {
	var out []map[string]any
	for _, p := range provider.All() {
		if !p.On() || p.DecideOnly() {
			continue
		}
		for _, m := range Drawers(p) {
			name := cmp.Or(m.Name, m.ID)
			o := map[string]any{"id": p.ID + "/" + m.ID, "object": "model", "type": "model", "kind": "image", "created": 0,
				"owned_by": p.ID, "display_name": name, "magpie_label": name + " · " + p.Name,
				"modalities": map[string]any{"input": []string{"text"}, "output": []string{"image"}}}
			if m.Images {
				o["modalities"] = map[string]any{"input": []string{"text", "image"}, "output": []string{"image"}}
			}
			out = append(out, o)
		}
	}
	return out
}

// videomakerObjects are the video models magpie makes videos with, as
// another magpie asks for them (provider.VideomakersHeader): "kind": "video".
func videomakerObjects() []map[string]any {
	var out []map[string]any
	for _, p := range provider.All() {
		if !p.On() || p.DecideOnly() {
			continue
		}
		for _, m := range Videomakers(p) {
			name := cmp.Or(m.Name, m.ID)
			out = append(out, map[string]any{"id": p.ID + "/" + m.ID, "object": "model", "type": "model", "kind": "video", "created": 0,
				"owned_by": p.ID, "display_name": name, "magpie_label": name + " · " + p.Name,
				"modalities": map[string]any{"input": []string{"text", "image"}, "output": []string{"video"}}})
		}
	}
	return out
}

// catalogFor is the catalog as the agent asking is shown it.
func catalogFor(r *http.Request) []provider.Entry {
	shown, _ := provider.CatalogFor(agentOf(r))
	return keyAllowed(r, shown)
}

func (s *Server) models(w http.ResponseWriter, r *http.Request) {
	data := []map[string]any{}
	shown := catalogFor(r)
	if agentOf(r) == "claude-desktop" {
		data = desktopModels(shown)
	} else {
		labels := provider.Labels(shown)
		// for another magpie: how it searches the web for each model
		magpie := r.Header.Get(provider.DrawersHeader) != ""
		searches := magpie && canSearch()
		for i, e := range shown {
			m := modelObject(e)
			if magpie {
				if how := webSearchOf(e, searches); how != "" {
					m["web_search"] = how
				}
			}
			// for another magpie: its name as the agents' lists here call
			// it, with its provider's after it unless the user wants it
			// plain, so two providers' models of one name are told apart
			// there as they are here
			m["magpie_label"] = labels[i]
			if agentOf(r) == "cursor-local" {
				cursorLocalModel(m, e)
			}
			data = append(data, m)
		}
	}
	if r.Header.Get(provider.DrawersHeader) != "" {
		data = append(data, drawerObjects()...)
	}
	if r.Header.Get(provider.VideomakersHeader) != "" {
		data = append(data, videomakerObjects()...)
	}
	// ?format=text: the ids one a line, to paste into a client that takes
	// its models typed by hand, one a line, and asks no list of its own
	// (ZCode's custom provider), from a browser on another computer with
	// the gateway key as ?key=
	if r.URL.Query().Get("format") == "text" {
		var b strings.Builder
		for _, m := range data {
			fmt.Fprintln(&b, m["id"])
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, b.String())
		return
	}
	out := map[string]any{"object": "list", "data": data, "has_more": false}
	if len(data) > 0 {
		out["first_id"], out["last_id"] = data[0]["id"], data[len(data)-1]["id"]
	}
	writeJSON(w, 200, out)
}

func (s *Server) model(w http.ResponseWriter, r *http.Request) {
	id := unprefixed(r.PathValue("id"))
	for _, e := range keyAllowed(r, provider.Catalog()) {
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

// askedAt splits a model asked for at an effort of its own,
// "<model>:<level>" (before a [1m] mark) — a Claude Code tier magpie set one
// on (#536) — into the model and the level, "" for none. As a routing
// group's member is read (#189), only a level's name after the last colon is
// one, and a model a provider has by the whole id ("x:high") is taken whole.
func askedAt(id string) (string, string) {
	bare, marked := strings.CutSuffix(id, "[1m]")
	m, e := provider.MemberEffort(bare)
	if e == "" {
		return id, ""
	}
	if marked {
		m += "[1m]"
	}
	return m, e
}

// estimatedMoved are the built-ins that estimated a count, as their
// plugins do, moved or beside the built-in (kiro-plugin); Command Code's
// whatever its plan, as the plugin alone knows a Go key.
var estimatedMoved = []string{"cursor", "grok", "devin", "kiro", "qoder", "zed", "factory", "zcode", provider.CommandCodePlanID}

// countTokens answers Anthropic's count_tokens: through the provider when
// it implements counting, else a rough estimate. A failed connection or
// limited key yields to the next key; other failures reach the client.
func (s *Server) countTokens(w http.ResponseWriter, r *http.Request) {
	body, ok := s.requestBody(w, r, provider.Anthropic)
	if !ok {
		return
	}
	var err error
	var model string
	body, model, err = requestModel(body)
	if err != nil {
		writeError(w, provider.Anthropic, 400, err.Error())
		return
	}
	// Count the same masked prompt that generation sends to the vendor.
	plain := body
	w, body, unmask := redacted(w, body)
	defer unmask()
	if m := claudeTierStandIn(agentOf(r), model); m != "" {
		model = m // counted on the model the request will go to
	}
	model, _ = askedAt(model) // counted as the model, whatever its effort
	id := unprefixed(model)
	if sid, ok := provider.AutoStandIn(id); ok {
		id = sid
	}
	p, model, ok := provider.Resolve(id)
	// a gateway key held to some models (#882) counts no tokens on one it
	// may not use, as it would be refused serving it
	if countHeld(w, r, provider.Anthropic, id) {
		return
	}
	// and counts the unmasked one where generation sends that
	if _, ms, isGroup := provider.FindGroup(id); ok && unredactedRoute(p, isGroup, ms) {
		body = plain
	}
	s.countOn(w, r, p, model, ok, body)
}

// countOn counts body's tokens for model on p, resolved when ok.
func (s *Server) countOn(w http.ResponseWriter, r *http.Request, p provider.Provider, model string, ok bool, body []byte) {
	// Claude Subscription generations run through the Claude Code binary. Its
	// OAuth token must not take a direct HTTP side path just for token counting.
	if ok && p.Account != nil && (p.Account.Agent == "claude" || p.Account.Agent == "cursor" || p.Account.Agent == "grok" || p.Account.Agent == "devin" || p.Account.Agent == "kiro" || p.Account.Agent == "qoder" || p.Account.Agent == provider.QoderCNID || p.Account.Agent == "zed" || p.Account.Agent == "factory" || p.Account.Agent == "gemini" || p.Account.Agent == "antigravity" ||
		// ZCode itself never counts, and zcode.z.ai answers count_tokens
		// with an error every time: one more request its firewall weighs
		p.Account.Agent == "zcode") ||
		ok && p.Account != nil && p.Account.Agent == provider.CommandCodePlanID && cmdGoing(r.Context(), p) ||
		ok && p.IsPlugin() && (slices.Contains(estimatedMoved, p.ID) || slices.Contains(estimatedMoved, p.PluginProvider())) {
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
		// the accounts or keys the calling key may not use are left out of
		// the counts too (#905): counted on one it may, and where it may
		// use none, the local estimate below says the count
		if who, held := accountHolds(r); held {
			counts = slices.DeleteFunc(counts, func(c candidate) bool { return !accountAllowed(who, c) })
		}
	}
	// the names in force, read at most once however many candidates are
	// counted, and not at all where there are none
	wires := sync.OnceValue(func() map[string]string { return settings.Load().ModelWires })
	for i, c := range counts {
		res, err := s.forward(r.Context(), c.p, provider.Anthropic, "/v1/messages/count_tokens", rewriteModel(body, provider.UpstreamNameIn(wires(), c.p.ID, model)), r.Header)
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
			writeError(w, provider.Anthropic, res.StatusCode, c.p.Explain(c.p.Name+": "+provider.APIError(b, res.Status), res.StatusCode, b))
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
		body, ok := s.requestBody(w, r, from)
		if !ok {
			return
		}
		var err error
		body, _, err = requestModel(body)
		if err != nil {
			writeError(w, from, 400, err.Error())
			return
		}
		if from == provider.Chat {
			body = thinkingEffort(body)
		}
		// Codex with magpie as its provider asks for a thread's title
		// here (#743)
		if from == provider.Responses {
			if to := codexTitlesTo(r.Header, body, true); to != "" {
				s.codexTitle(w, r, body, to)
				return
			}
		}
		s.serveAgent(w, r, from, body)
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
	body, ok := s.requestBody(w, r, provider.Gemini)
	if !ok {
		return
	}
	// one spelling, for what reads the body after — the images a model
	// that can't see is shown in words of (image_input.go) too (#934)
	body = geminiCamel(body)
	var err error
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
		// a gateway key held to some models (#882) counts no tokens on one
		// it may not use, as it would be refused serving it
		if countHeld(w, r, provider.Gemini, model) {
			return
		}
		s.geminiCount(w, model, body)
		return
	default:
		writeError(w, provider.Gemini, 404, "unknown method "+method)
		return
	}
	s.serveAgent(w, r, provider.Gemini, withFields(body, map[string]any{"model": model, "stream": stream}))
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
	// the upstream names in force for this request, read once here rather
	// than once per place a name is looked up below
	r = withWires(r)
	// secrets go as placeholders and come back as they were; the log has
	// what the vendor saw and said. plain is the body as the agent wrote
	// it, for a request that goes only where the user said it may go
	// unmasked (unredactedRoute)
	plain := body
	w, body, unmask := redacted(w, body)
	defer unmask()
	// the reply's model the member that answered, when asked for (#822)
	w, r, named := withMemberNames(w, r)
	defer named()
	requestBody, requestTruncated := captureRequestBody(body)
	// the OTLP export may want the bodies uncut (#538): the request is
	// already whole in memory, so keep it as it came, and the reply goes to
	// a spool as it streams
	wholeBodies := usage.OTelWhole()
	var otelIn []byte
	if wholeBodies {
		otelIn = body
	}
	// a model asked for at an effort of its own (a Claude Code tier, #536)
	// is the model, every try of it asked for that effort
	asked, askedEffort := askedAt(modelOf(body))
	if askedEffort != "" {
		body = rewriteModel(body, asked)
		plain = rewriteModel(plain, asked)
	}
	capture := &captureResponseWriter{ResponseWriter: w}
	if wholeBodies {
		capture.otel = &spool{limit: wholeBodyLimit, pattern: "magpie-otel-*"}
	}
	w = capture
	// the agent it is recorded as, the one on the computer it was passed
	// on from for a remote magpie's request; agent the one that sent it,
	// which what is done with it goes by
	who, agent := callerOf(r), agentOf(r)
	metadata := requestSessionMetadata(r.Header, body)
	var clientResponseID string // only a reply released to the client owns this ID
	call := Call{Time: start, From: from, Model: unprefixed(modelOf(body)), Agent: who.agent, Via: who.via, Kind: requestCallKind(r.Header, metadata),
		RequestBody: requestBody, RequestTruncated: requestTruncated, otelIn: otelIn, wire: archiving(r, capture, start, body)}
	defer discardArchive(capture)
	r, telemetry := beginOTelRequest(r, call.Kind, body)
	defer func() { telemetry.end(call, time.Since(start).Milliseconds()) }()
	switch call.Kind {
	case "web_search":
		call.For = searchFor(r.Context())
	case "vision":
		call.For = describedFor(r.Context())
	}
	usage.Saw(agent)
	finishCapture := func() {
		call.ResponseBody = capture.body.text()
		call.ResponseTruncated = capture.body.truncated
		if capture.otel != nil {
			call.otelOutCut = capture.otel.cut()
			if b, err := capture.otel.read(); err == nil {
				call.otelOut = b
			}
		}
	}
	// the usage row of a request turned away before any provider was asked:
	// the ledger says it failed and why. The recent-calls entry is the
	// usual one at the end of serve, so a caller that only wants the row
	// (a lane refusal, which returns to serve's end) does not call
	// turnedAway and record twice.
	logged := func() {
		rec := usage.Record{Time: start, Agent: call.Agent, Via: call.Via, Provider: call.Provider, Model: call.Model, Requested: call.Model,
			Millis: call.Millis, Status: call.Status, Rejected: true, Session: sessionOf(r.Header), NativeSession: nativeSessionOf(r.Header), Kind: call.Kind, Endpoint: endpointOf(r, from, ""), Archive: call.archiveName()}
		failedWith(&rec, call.Status, call.Error, "")
		withBodies(&rec, &call)
		appendUsage(r, rec)
	}
	// a request turned away before any provider was asked is in the log
	// as the failure it was, with the reason
	turnedAway := func() {
		finishCapture()
		call.Millis = time.Since(start).Milliseconds()
		s.record(call)
		logged()
	}
	// a model's id without a provider in it that names a routing group is
	// the group's, as "group/<id>" is, rather than one provider's that
	// serves it: the Routing view shows the group it went to
	asked = call.Model
	if agent == "claude-desktop" {
		asked = desktopTurn(asked, body)
	}
	// a stand-in that is a tier at an effort of its own ("<model>:<level>",
	// #536) is that model at that level, as were it asked for so
	at := func(m string) string {
		m, e := askedAt(m)
		if askedEffort == "" {
			askedEffort = e
		}
		return m
	}
	if m := claudeTierStandIn(agent, asked); m != "" {
		// Claude Code (or a wrapper, T3 Code) naming one of Anthropic's
		// models by its full id: the model it is set to use for that tier,
		// whether or not magpie serves the id too
		asked = at(m)
	} else if id, ok := provider.GroupFor(asked); ok {
		asked = id
	} else if id, ok := provider.AutoStandIn(asked); ok {
		// a group magpie found, while the user has those off: its model
		// from one provider, rather than refused
		asked = id
	} else if m := standIn(agent, asked); m != "" {
		asked = at(m)
	}
	if m := s.codexMemoryStandIn(r, call.Agent, agent, call.Kind, asked); m != "" {
		asked = m
	}
	p, model, ok := provider.Resolve(asked)
	if ok {
		s.rememberCodexTurn(r, call.Agent, agent, call.Kind, asked)
	}
	if !ok {
		call.Status, call.Error = 404, "unknown model"
		if off, isOff := provider.SwitchedOff(asked); isOff {
			call.Error = "provider switched off"
			writeError(w, from, 404, switchedOff(off, call.Model))
			turnedAway()
			return
		}
		msg := fmt.Sprintf("magpie knows no model %q", call.Model)
		if g, ok := provider.DisabledGroup(asked); ok {
			call.Error = "group switched off"
			msg = fmt.Sprintf("the routing group %s is switched off in Magpie, so %q is not served; switch it on again on Magpie's Routing page to use it", g.Name, call.Model)
		} else if g, ok := emptyGroup(asked); ok {
			// a group of its own with nothing in it now — its patterns
			// match no model served (#766) — said so, not the whole list
			msg = emptyGroupError(g)
		} else if ids := provider.IDs(); len(ids) > 0 {
			msg += "; it has " + strings.Join(ids, ", ")
		} else {
			msg += "; add a provider in magpie first"
		}
		writeError(w, from, 404, msg)
		turnedAway()
		return
	}
	if p.DecidesModel(model) {
		// Jev answers questions about a message, not the message
		call.Status, call.Error = 400, "a decision model"
		writeError(w, from, 400, fmt.Sprintf("%s only decides a routing group's model and effort; it holds no conversation", call.Model))
		turnedAway()
		return
	}
	// a routing group's rules pick the member that goes first, looked at
	// before any image is taken out of the request: one may be for images
	g, ms, isGroup := provider.FindGroup(asked)
	g = g.Live() // a manual group's rules wait
	// a model on this machine or the local network that the user set to
	// go unmasked gets the request as written (lc on Discord); the log
	// keeps the masked one
	if unredactedRoute(p, isGroup, ms) {
		body = plain
	}
	// a gateway key held to some models (#882) is refused another, or a
	// group it doesn't name with one it may not use in it
	keyWho, keyHeld := keyHolds(r)
	_, accHeld := accountHolds(r)
	chose, _ := r.Context().Value(magpieChoseKey{}).(bool)
	if keyHeld && (isGroup && !groupAllowed(keyWho, g, ms) || !isGroup && !modelAllowed(keyWho, p, model)) {
		call.Status, call.Error = 403, "model not allowed for the gateway key"
		writeError(w, from, 403, keyModelError(keyWho, call.Model))
		turnedAway()
		return
	}
	// a Codex subagent's task its lead sealed — the lead answered by a
	// ChatGPT account, the group's own or Codex's — goes only to a ChatGPT
	// account, the lead's first (#619), or back to the Responses provider
	// that answered the lead, a relay of the ChatGPT backend (#1109); with
	// none, it is turned away before anyone is asked
	sealedTask := from == provider.Responses && hasSealedAgentMessage(body)
	sealedLead := ""
	if sealedTask {
		parent := metadata.Parent
		if parent == "" {
			parent = r.Header.Get("x-codex-parent-thread-id")
		}
		scope := p.ID + "/" + model
		if isGroup {
			scope = provider.GroupPrefix + g.ID
		}
		sealedLead = leadProvider(scope, parent)
		if !(isGroup && slices.ContainsFunc(ms, func(m provider.Member) bool { return s.sealedReader(m.Provider, m.Model, sealedLead) }) || !isGroup && s.sealedReader(p, model, sealedLead)) {
			call.Status, call.Error = 400, "sealed subagent task"
			writeError(w, from, 400, sealedTaskError(call.Model, sealedLead))
			turnedAway()
			return
		}
	}
	var hit *RuleHit
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
		hit = ruleFor(ruleAt, g, ms, ruleReq, agent, ask)
	}
	// Some clients send images even when the selected model is known to
	// accept text only. Reject a new image and omit images from history.
	var imageInput *bool
	if isGroup {
		// whether a member takes them
		imageInput = membersImageInput(ms)
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
	// a model its list says nothing of is text-only to a describer, as
	// agents are told: while one describes images they are told every
	// model takes them, and send the images on to be described
	if imageInput == nil && !isGroup && hasImage(from, body) && blindTo(p.ID, model, nil) {
		if _, ok := seeing(); ok {
			no := false
			imageInput = &no
		}
	}
	if imageInput != nil && !*imageInput {
		var currentImage bool
		if see, ok := seeing(); ok && hasImage(from, body) {
			seen, err := s.seenBody(withDescribeFor(r.Context(), call.Agent, call.Model, sessionOf(r.Header)), from, body, see)
			if err != nil {
				call.Status, call.Error = 502, "image not described"
				writeError(w, from, 502, fmt.Sprintf("model %q can't see images, and %s couldn't describe the image for it: %v", call.Model, see, err))
				turnedAway()
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
			turnedAway()
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
	leadScope := scope // where the lead of a subagent is kept
	if isGroup {
		// a routing group: every member's keys or accounts weighed together
		cands, pl = s.planGroup(g, ms, from)
		group = groupRef(g, ms)
		scope, mode, rotate = provider.GroupPrefix+g.ID, g.Affinity, g.Routing == provider.Rotate
		leadScope = scope
		if words != "" {
			// a subagent a rule sends elsewhere mustn't take its agent's
			// conversation with it: each keeps where it is on its own
			scope += "|" + words
		}
	} else {
		cands, pl = s.plan(p, model, from)
		if provider.IsFastPick(agent, p.ID+"/"+model) {
			// the agent's pick of it is sent in its vendor's fast mode
			// (#954), as a group's member may be; its fallbacks, other
			// models, go as they are
			fastPick(cands, pl, p.ID, model)
		}
	}
	agentPick := provider.AgentEffort(agent)
	if sealedTask {
		cands, pl = s.sealedReaders(cands, pl, sealedLead)
	}
	var accountHeld bool // every candidate was an account or key the calling key may not use
	if keyHeld || accHeld {
		who := keyWho
		var members map[string]bool
		if chose {
			// magpie's own call for the key — a web search, a picture
			// described, a Codex title: of the models the user picked, so
			// the models don't hold it, but the spend lands on an account,
			// and the accounts hold it too
			who.Models = nil
		} else if isGroup {
			members = groupKeeps(keyWho, g, ms)
		}
		cands, pl, accountHeld = allowedCandidates(who, cands, pl, members)
	}
	// a Codex reset spent before any try, on an account held as it won't
	// spend its credits
	var resetFirst *AutoReset
	if len(cands) == 0 && len(pl.held) > 0 && strings.TrimSpace(r.Header.Get(AccountHeader)) == "" &&
		!slices.ContainsFunc(pl.left, func(w Weighed) bool { return !w.Barred && !w.Held && w.Capped == 0 }) {
		// every account there is is held, and one held only as it won't
		// spend its credits is out of its allowance at the vendor's own
		// 100%: one that spends its resets by itself, its week used up,
		// spends one and is tried, as one the vendor refused would be
		if pick, out, ok := s.autoReset(r.Context(), nil, pl.held[0], pl.held); ok {
			cands = []candidate{pick}
			pl.order, pl.left = unhold(pl.order, pl.left, pick)
			resetFirst = &AutoReset{Who: pick.p.Account.User, Text: out.Text(), Agent: pick.p.Account.Agent}
		}
	}
	if len(cands) == 0 && slices.ContainsFunc(pl.left, func(w Weighed) bool { return w.Capped > 0 }) &&
		!slices.ContainsFunc(pl.left, func(w Weighed) bool { return !w.Barred && !w.Held && w.Capped == 0 }) {
		// every account there is is held at its usage cap: used up, as far
		// as routing goes, until a window renews
		msg, back := cappedError(call.Model, pl.left, time.Now())
		if d := time.Until(back); d > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(d.Seconds())+1))
		}
		call.Status, call.Error = 429, cappedRecord(pl.left)
		writeError(w, from, 429, msg)
		turnedAway()
		return
	}
	if len(cands) == 0 && accountHeld {
		// every account or key the model had is one the calling key may
		// not use (#905), the plan's left saying so: told before the barred
		// and ready ones it may also have
		call.Status, call.Error = 403, "every account outside the key's accounts"
		writeError(w, from, 403, keyAccountsError(keyWho, call.Model))
		turnedAway()
		return
	}
	if len(cands) == 0 && len(pl.left) > 0 && !slices.ContainsFunc(pl.left, func(w Weighed) bool { return !w.Barred }) {
		// every account or key there is was set not to serve the model
		call.Status, call.Error = 403, "every account barred"
		writeError(w, from, 403, barredError(call.Model, pl.left))
		turnedAway()
		return
	}
	if len(cands) == 0 {
		call.Status, call.Error = 404, "no member ready"
		writeError(w, from, 404, fmt.Sprintf("none of %s's models is ready", call.Model))
		turnedAway()
		return
	}
	// the conversation stays with who answered it last, while its
	// affinity says: what the vendor cached of it is read again. Who
	// answered is remembered with only one to route to as well, so that a
	// key or account added while the conversation runs doesn't take it over
	// (#63): its reasoning, sealed by the account that wrote it, is refused
	// by another.
	cands, pl, aff, stuck := affine(scope, mode, rotate, r.Header, from, body, cands, pl)
	leadAccount := ""
	if sealedTask && !aff.Kept {
		parent := metadata.Parent
		if parent == "" {
			parent = r.Header.Get("x-codex-parent-thread-id")
		}
		cands, pl, leadAccount = leadFirst(leadScope, parent, cands, pl)
	}
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
	if ruleReq != nil && !modelRuled(hit, ms, cands) {
		nested, nestedAt, cands, pl = s.nestedRules(ruleAt, ruleReq, agent, ask, ms, cands, pl, aff)
	}
	// the effort a group's decision model picked for the turn: the
	// outermost group's that did
	effort, wanted := "", ""
	if hit != nil {
		effort, wanted = hit.Pick, hit.Wanted
	}
	for _, n := range nested {
		if effort == "" && n.Rule != nil {
			effort, wanted = n.Rule.Pick, n.Rule.Wanted
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
			seenGroup = sync.OnceValues(func() ([]byte, error) {
				return s.seenBody(withDescribeFor(r.Context(), call.Agent, call.Model, sessionOf(r.Header)), from, body, see)
			})
		}
	}
	if isGroup && seenGroup == nil {
		// without a model to describe it, a new image goes only to the
		// members that see it (#756) — to members magpie knows nothing
		// of when none is known to see, as a model of its own would be
		// sent it. With one, the members keep their order, a text-only
		// one given it described.
		if _, currentImage := textOnlyBody(from, body); currentImage {
			var sees, unknown []candidate
			var seesAt, unknownAt []Weighed
			for i, c := range cands {
				in := membersImageInput([]provider.Member{{Provider: c.p, Model: c.model}})
				switch {
				case !blindTo(c.p.ID, c.model, in):
					sees, seesAt = append(sees, c), append(seesAt, pl.order[i])
				case in == nil:
					unknown, unknownAt = append(unknown, c), append(unknownAt, pl.order[i])
				}
			}
			cands, pl.order = sees, seesAt
			if len(sees) == 0 {
				cands, pl.order = unknown, unknownAt
			}
			if len(cands) == 0 {
				call.Status, call.Error = 400, "model does not support image input"
				writeError(w, from, 400, fmt.Sprintf("model %q does not support image input", call.Model))
				turnedAway()
				return
			}
		}
	}
	pin := strings.TrimSpace(r.Header.Get(AccountHeader))
	if pin != "" {
		var status int
		var msg string
		if cands, pl, status, msg = pinTo(pin, cands, pl); status != 0 {
			call.Status, call.Error = status, "account pinned: "+pin
			writeError(w, from, status, msg)
			finishCapture()
			s.record(call)
			return
		}
	}
	shown := aff
	if len(cands) == 1 {
		shown = nil // nobody else to stay away from
	}
	link := s.titlePrompts.observe(r, body, metadata, call.Kind, start)
	tr := s.trace.begin(Route{imageTurn: drawingTurnID(metadata.Turn), imageCaller: codexTurnKey(r, call.Agent), TitleLink: link, Pinned: pin, Time: start, Agent: call.Agent, Session: sessionOf(r.Header), Conv: convOf(r.Header, body), ParentSession: titleParentSession(r.Header, metadata, call.Kind), Kind: call.Kind, For: call.For, Model: call.Model, Effort: requestEffort(from, body), Provider: p.ID, Group: group, Rule: hit, Nested: nested, SealedTask: sealedTask, LeadAccount: leadAccount, Affinity: shown, Order: pl.order, Left: pl.left})
	if telemetry != nil {
		telemetry.routeID = tr.ID
	}
	promptRead := s.inspectPrompt(tr, from, body)
	var lastTried provider.Provider // the last try's, for its model's context window
	var skipped []string
	sent := ""       // the reasoning the last try's model was asked for
	where := ""      // the last try's provider.Where, for the usage
	again := 0       // times the last one left has been tried again
	resealed := 0    // what of the conversation another account sealed was taken out: its reasoning, then its compaction
	repicked := 0    // times Copilot's Auto was asked again for a model the account is served
	floored := false // the reply's length raised to what the provider takes
	plainFor := ""   // the account and model asked again without effort updates (#617)
	// the key or subscription account the last try went to (#557)
	providerKeyID, providerKeyName, providerAccount := "", "", ""
	var other *Try                 // the first failure that wasn't an allowance run out
	autoReset := resetFirst != nil // a Codex or Claude reset looked at, once a request
	// what the tries' held streams sent the agent ahead of a reply (#751)
	kept := &keptAlive{proto: from}
	var sentMs int64 // ms from the request to its answering try going to the vendor
	streams := streamOf(body)
	if from == provider.Gemini {
		streams = strings.Contains(r.URL.Path, "streamGenerateContent")
	}
	gaveWay := map[string]bool{} // keys and accounts that gave way, full, to one free (laneMate)
	for i := 0; i < len(cands); i++ {
		c := cands[i]
		if j := s.laneMate(cands, i, isGroup, gaveWay); j > i {
			// its key or account is full, and another of its provider's,
			// or of its group, has a slot free: that one is asked first,
			// this one keeps its place after it (#892)
			gaveWay[c.who()] = true
			cs := slices.Clone(cands)
			cands = slices.Insert(slices.Delete(cs, i, i+1), j, c)
			i--
			continue
		}
		last := i == len(cands)-1
		// the last one's failure is held too when an earlier one failed,
		// for its allowance running out to be told as that one's error
		// and once the agent has the stream's headers from an earlier try's
		// keepalives, for a failure to be told as the stream's error
		hw := newHoldWriter(w, !last || again < max(lastRetries, rateRetries) || other != nil || kept.sent || autoPicks(c) && repicked < 2)
		// Claude's and GPT's reasoning is held for a refusal after it only
		// while another candidate could answer instead: the last one's
		// refusal isn't asked again, so holding it only kept a lone relay's
		// thinking from Claude Code and Codex until the text began, all at
		// once (iTianbao on X). Gemini's stays held on the last one too,
		// for a reply that only reasoned to be asked again (#667).
		hw.thinkingShown = !refusesAfterThinking(c.model) || last && modelFamily(c.model) != ""
		hw.ctx, hw.alive, hw.streams = r.Context(), kept, streams
		if isGroup && g.FirstToken > 0 && !last && streams {
			// slow to start, the next member is asked (Group.FirstToken)
			hw.firstWait = time.Duration(g.FirstToken) * time.Second
		}
		call.Provider, call.To, call.Usage = c.p.ID, "", Usage{}
		model = c.model
		lastTried = c.p
		where = c.p.Where()
		providerKeyID, providerKeyName = "", ""
		if c.p.Account == nil && c.p.Key != "" {
			providerKeyID, providerKeyName = provider.KeyID(c.p.Key), c.p.KeyName
		}
		providerAccount = accountOf(c.p)
		began := time.Now()
		hw.first.start = began
		var wrote atomic.Int64
		attemptBody := body
		if from == provider.Responses {
			// reasoning another sealed, which this one refused earlier in
			// the conversation, isn't sent to be refused again; its own is
			if b, ok := withoutRefused(stuck, c.who(), attemptBody); ok {
				attemptBody = b
			}
		}
		if isGroup {
			in := membersImageInput([]provider.Member{{Provider: c.p, Model: c.model}})
			if seenGroup != nil && blindTo(c.p.ID, c.model, in) {
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
					failTo(w, kept, from, 502, fmt.Sprintf("model %q can't see images, and %s couldn't describe the image for it: %v", c.p.ID+"/"+c.model, see, err))
					break
				}
				attemptBody = b
			} else if in != nil && !*in {
				attemptBody, _ = textOnlyBody(from, body) // omit images in prior turns and tool results
			}
		}
		// a group tells agents the longest reply a member of it gives
		// (groupEntries); each member is asked for no more than its own
		if isGroup {
			if n := outputLimit(c.p, c.model); n > 0 {
				attemptBody = withMaxOutput(from, attemptBody, n)
			}
		}
		picked := false // the effort asked for in place of the agent's
		// a member fixed at an effort (#189), else the model asked for at
		// one (#536)
		// the levels the group offers (Group.Levels) bound what the agent,
		// a model's suffix or the turn's pick asks for (#671); a member the
		// user fixed at an effort is asked for it
		var levels []string
		if isGroup {
			levels = groupLevels(g, ms, c)
		}
		fixed := cmp.Or(c.effort, withinLevels(askedEffort, levels))
		if fixed == "" && effort == "" && agentPick != "" && c.p.Thinks(c.model) {
			// the effort picked in magpie for an agent whose config can't
			// carry one (Cursor Private Inference, #1003), in place of the
			// level it sent, on a model that reasons; a member fixed at
			// one, a suffix and the turn's pick are more particular
			fixed = withinLevels(agentPick, levels)
		}
		if fixed != "" {
			// a member fixed at an effort is asked for it, at the level its
			// model has nearest, whatever the agent asked or the turn's
			// pick: even a request that asked for no reasoning
			attemptBody = withFixedEffort(from, attemptBody, fitLevel(fixed, c.p.Efforts(c.model)))
		} else if effort != "" {
			// the level this model has nearest to the one picked; one whose
			// levels aren't known isn't asked for more than high, which
			// every vendor with levels takes. The conversation kept a
			// higher one for its cache (keepsEffort) unless an effort
			// update carries the turn's own without losing it.
			pick := effort
			if wanted != "" && from == provider.Responses && plainFor != c.who()+"|"+c.model && takesEffortUpdates(c.p, c.model) {
				pick = wanted
			}
			if b := withEffort(from, attemptBody, fitLevel(withinLevels(pick, levels), c.p.Efforts(c.model))); !bytes.Equal(b, attemptBody) {
				attemptBody, picked = b, true
			}
		} else if e := agentEffort(from, attemptBody); e != "" && e != "none" {
			// the agent's own level, where the group doesn't offer it
			if want := withinLevels(e, levels); want != e {
				attemptBody = withEffort(from, attemptBody, want)
			}
		}
		// a member sent fast is, in its vendor's words, where its model
		// has a fast mode; else it goes as the agent asked
		fast := c.fast && provider.CanFast(c.p, c.model)
		if fast {
			attemptBody = withFast(from, attemptBody)
		}
		// the reasoning the model is asked for, whoever chose it
		sent = sentEffort(from, attemptBody, c.p, c.model)
		if !takesEffort(c, sent) {
			if j := effortMate(cands[i+1:], c, sent); j >= 0 {
				// its plan doesn't take this level, which another account
				// of its member may (#520): that one first, this one after
				j += i + 1
				cs := slices.Clone(cands)
				cands = slices.Insert(slices.Delete(cs, i, i+1), j, c)
				i--
				continue
			}
		}
		// an effort changed mid-thread goes as an update in the history,
		// which keeps the prompt the upstream cached (#617)
		updated := false
		if from == provider.Responses && plainFor != c.who()+"|"+c.model && takesEffortUpdates(c.p, c.model) {
			if b, ok := withEffortUpdates(stuck, c.who(), c.model, sent, attemptBody); ok {
				attemptBody, updated = b, true
			}
		}
		s.trace.update(tr, func(t *Route) {
			t.imageProvider = c.p.ID
			t.Tries = append(t.Tries, Try{ID: c.rest, Model: c.model, Effort: sent, Picked: picked, Fixed: fixed, Fast: fast, Start: began})
		})
		held := false     // answered as its vendor did a moment ago, without asking
		var queued int64  // ms it waited for a slot of its key's or account's
		var laneErr error // turned away by its key's or account's queue
		// the models Copilot's Auto picked for it, where from, and which
		// were refused (#256)
		autoPicked := func() []provider.AutoPick { return nil }
		if said, ok := verifyHeld(c.restKey()); ok && last {
			// the account must be verified first (#152): the agent's
			// reconnects are told so again, not sent on to a vendor that
			// just refused it
			held, call.Status, call.Error = true, http.StatusForbidden, said
			writeError(hw, from, call.Status, said)
		} else {
			// a stream that fails before anything is said is let go at
			// once (hw.stop), not read on until the vendor hangs up
			ctx, stop := context.WithCancel(r.Context())
			if isGroup {
				ctx = groupTry(ctx)
			}
			hw.stop = stop
			if autoPicks(c) {
				// the reply names the pick it went as last (#256: it
				// named one Copilot refused)
				p := c.p
				ctx, autoPicked = provider.WithAutoPicks(ctx, func(m string) { noteMember(hw, r, p, m) })
			}
			// when the request last went out to the vendor, its body
			// written: what came before is magpie's, what after the
			// vendor's (Record.Sent)
			ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{WroteRequest: func(httptrace.WroteRequestInfo) {
				wrote.Store(time.Now().UnixNano())
			}})
			// a key or account with a MaxConcurrency is asked once one of
			// its slots is free, in turn; the agent gone while it waits,
			// nothing is sent (the 499 below)
			// its queue full, or waited out (QueueLimit, QueueWait), it is
			// turned away below
			waited := time.Now()
			release, err := s.lanes.take(ctx, c.who(), c.p.LaneLimit(), c.p.QueueLimit, time.Duration(c.p.QueueWait)*time.Second)
			queued = time.Since(waited).Milliseconds()
			if errors.Is(err, errQueueFull) || errors.Is(err, errQueueWait) {
				laneErr = err
			}
			if err == nil {
				if queued > 0 {
					s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1].Queued = queued })
				}
				func() {
					// the slot is the vendor's until the reply is read to
					// its end or the agent has gone: attempt returns then
					defer release()
					defer hw.watch()()
					call.Status, call.Error = s.attempt(hw, r.WithContext(ctx), from, c.p, c.model, attemptBody, &call)
				}()
			}
			stop()
		}
		if laneErr != nil {
			// its key's or account's queue was full, or it waited as long
			// as it may: nobody failed, and nobody rests; the next is
			// asked, else the agent is told (#892)
			call.Status, call.Error = http.StatusTooManyRequests, laneMessage(c, laneErr, queued)
			s.trace.update(tr, func(t *Route) {
				tt := &t.Tries[len(t.Tries)-1]
				tt.Done, tt.Status, tt.Error, tt.Queued, tt.Millis = true, call.Status, call.Error, queued, time.Since(began).Milliseconds()
			})
			if !last {
				skipped = append(skipped, c.label()+": "+call.Error)
				continue
			}
			if !kept.sent {
				w.Header().Set("Retry-After", "1")
			}
			failTo(w, kept, from, call.Status, call.Error)
			// nobody was asked, and the agent is told: the ledger gets
			// the row, as any other turn-away leaves one, and the usual
			// entry in Recent calls is the one serve writes at its end
			call.Millis = time.Since(start).Milliseconds()
			logged()
			break
		}
		hw.settle()
		if hw.passing {
			clientResponseID = call.Usage.ResponseID
		}
		outgrew := false // a failure that didn't say so was the request's length
		if !hw.passing && !hw.refused && hw.code() >= 400 {
			if req, err := parse(from, attemptBody); err == nil {
				if msg, ok := tooLongUnsaid(c.p, c.model, estimate(req), hw.code(), hw.errBody()); ok {
					// told as an overflow: not asked again, of this one or
					// another, and nobody rests
					hw.failure, hw.failMsg, outgrew = http.StatusBadRequest, msg, true
				}
			}
		}
		if hw.failure != 0 { // the stream failed before any of it was sent
			call.Status, call.Error = hw.failure, c.p.Name+": "+hw.failMsg
		}
		if hw.refused {
			// the vendor's safety filter, with nothing said (#248)
			call.Error = refusedError(c.p, c.model, hw.failMsg)
		}
		try := Try{ID: c.rest, Model: c.model, Effort: sent, Picked: picked, Fixed: fixed, Fast: fast, Start: began, Done: true, Status: call.Status, Millis: time.Since(began).Milliseconds(), Error: call.Error, Queued: queued,
			Served: call.Usage.Served, Upstream: call.Usage.Upstream, Auto: autoPicked()}
		if resetFirst != nil {
			// the reset spent for it before it was asked
			try.Reset, resetFirst = resetFirst, nil
		}
		asName := provider.SentNameOnIn(wiresOf(r.Context()), c.p.ID, accountAgent(c.p), c.model, sent)
		try.Swapped, try.Routed = swapped(asName, call.Usage.Served), usage.GroupRouted(asName, call.Usage.Served)
		try.TTFT, try.FirstText = hw.first.ms()
		// the request's, from when it came as its ms are: the time before
		// this try, the ones that failed first, is in it
		call.TTFT, call.FirstText = sinceStart(began.Sub(start), hw.first.first), sinceStart(began.Sub(start), hw.first.text)
		sentMs = 0
		if at := wrote.Load(); at > 0 && call.TTFT > 0 {
			sentMs = min(max(time.Unix(0, at).Sub(start).Milliseconds(), 1), call.TTFT)
		}
		if r.Context().Err() != nil && !hw.ended {
			// the agent went away: nobody failed, and nobody else is asked
			call.Status, call.Error = 499, "the agent canceled the request"
			try.Status, try.Error, try.Fail = call.Status, call.Error, failCanceled
			telemetry.attempt(call, try, c.p.ID, sent, hw, capture)
			s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1] = try })
			break
		}
		if hw.slow {
			// no first content in the group's FirstToken, and nothing of
			// it sent: the next member is asked, and this one doesn't
			// rest, a long prompt being slow to start anywhere
			call.Status, call.Error = http.StatusGatewayTimeout, fmt.Sprintf("%s: no first token in %ds", c.p.Name, g.FirstToken)
			try.Status, try.Error, try.Fail = call.Status, call.Error, failSlow
			call.TTFT, call.FirstText = 0, 0
			telemetry.attempt(call, try, c.p.ID, sent, hw, capture)
			s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1] = try })
			if other == nil {
				other = &Try{Status: call.Status, Error: call.Error}
			}
			skipped = append(skipped, c.label()+": "+call.Error)
			continue
		}
		telemetry.attempt(call, try, c.p.ID, sent, hw, capture)
		if autoPicks(c) && repicked < 2 && !hw.passing && hw.code() == http.StatusBadRequest &&
			(provider.CopilotRefusal(hw.errBody()) || wrongEndpoint(hw.code(), hw.errBody())) {
			// Copilot refused the model its Auto picked, and Auto has
			// picked another, which is asked for on the APIs it is
			// served on (#256)
			repicked++
			s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1] = try })
			i--
			continue
		}
		if resealed < 2 && from == provider.Responses && !hw.passing && hw.code() >= 400 && foreignReasoning.Match(hw.errBody()) {
			// the conversation moved here from another account or vendor,
			// whose sealed reasoning this one can't read: asked again
			// without it, and what it refused isn't sent here again. xAI
			// says so as a 400, or as the stream's error, which may be
			// read as another status. Refused again, or with no reasoning
			// to leave out, it's the compaction OpenAI sealed that goes
			// (waroy: Codex compacted on GPT, then switched to Grok)
			b, ok := []byte(nil), false
			if resealed == 0 {
				resealed = 1
				if b, ok = withoutReasoning(body); ok {
					refused(stuck, c.who(), attemptBody, "reasoning")
				}
			}
			if !ok {
				resealed = 2
				if b, ok = withoutCompaction(body); ok {
					refused(stuck, c.who(), attemptBody, compactionKinds...)
				}
			}
			if ok {
				body = b
				try.Fail = failForeign
				s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1] = try })
				i--
				continue
			}
		}
		if updated && !outgrew && !hw.passing && hw.code() == 400 {
			// the upstream turned the update items away: asked again as
			// the agent sent it, and if that goes, never sent them again
			plainFor = c.who() + "|" + c.model
			try.Fail = failUpdate
			s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1] = try })
			i--
			continue
		}
		if plainFor == c.who()+"|"+c.model && call.Status > 0 && call.Status < 400 {
			effortUpdatesRefused(c.who(), c.model)
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
		if !last && hw.failed() && hw.refused {
			// another account or model may answer what this one refused;
			// this one isn't set aside, as nothing is wrong with it, and
			// what the refusal cost is logged on its own
			try.Fail = failRefused
			s.trace.update(tr, func(t *Route) {
				t.Tries[len(t.Tries)-1] = try
				if call.To != "" {
					t.Usage = append(t.Usage, routeUsage(call.Provider, c.model, call.Usage)...)
				}
			})
			skipped = append(skipped, c.label()+": "+call.Error)
			matesFirst(cands[i+1:], c)
			if call.To != "" {
				rec := usage.Record{RouteID: tr.ID, Time: began, Agent: call.Agent, Via: call.Via, Provider: call.Provider, Host: where, Model: c.model,
					ProviderKeyID: providerKeyID, ProviderKeyName: providerKeyName, ProviderAccount: providerAccount,
					Requested: call.Model, Served: call.Usage.Served, Upstream: call.Usage.Upstream,
					Input: call.Usage.Input, Output: call.Usage.Output, CacheRead: call.Usage.CacheRead,
					CacheWrite: call.Usage.CacheWrite, CacheWrite1h: call.Usage.CacheWrite1h, Reasoning: call.Usage.Reasoning, Effort: sent, Millis: time.Since(began).Milliseconds(), Status: call.Status,
					TTFT: try.TTFT, FirstText: try.FirstText, Session: sessionOf(r.Header), NativeSession: nativeSessionOf(r.Header), Kind: call.Kind,
					RequestID: call.Usage.RequestID, Endpoint: endpointOf(r, from, call.To), Archive: call.archiveName()}
				failedWith(&rec, call.Status, call.Error, call.Usage.ErrType)
				// what this account answered is its refusal, the reply
				// captured so far being no one's yet
				refusal := &Call{RequestBody: call.RequestBody, RequestTruncated: call.RequestTruncated, ResponseBody: string(hw.errBody())}
				if call.otelIn != nil {
					refusal.otelIn, refusal.otelOut = call.otelIn, hw.errBody()
				}
				withBodies(&rec, refusal)
				appendUsage(r, rec)
			}
			continue
		}
		if !last && hw.failed() && hw.turnedAway {
			// another member may answer what this account was turned away
			// from, and this one isn't set aside: Antigravity refused the
			// request, whatever quota the account has left, so nothing is
			// wrong with it and no usage is asked again (#666). Its own
			// mates are asked last, not first: they carry the same system
			// prompt, so each is turned away just the same before the group
			// reaches a member that answers
			try.Fail = failRefused
			s.trace.update(tr, func(t *Route) {
				t.Tries[len(t.Tries)-1] = try
				if call.To != "" {
					t.Usage = append(t.Usage, routeUsage(call.Provider, c.model, call.Usage)...)
				}
			})
			skipped = append(skipped, c.label()+": "+call.Error)
			matesLast(cands[i+1:], c)
			if call.To != "" {
				rec := usage.Record{RouteID: tr.ID, Time: began, Agent: call.Agent, Via: call.Via, Provider: call.Provider, Host: where, Model: c.model,
					ProviderKeyID: providerKeyID, ProviderKeyName: providerKeyName, ProviderAccount: providerAccount,
					Requested: call.Model, Served: call.Usage.Served,
					Input: call.Usage.Input, Output: call.Usage.Output, CacheRead: call.Usage.CacheRead,
					CacheWrite: call.Usage.CacheWrite, CacheWrite1h: call.Usage.CacheWrite1h, Reasoning: call.Usage.Reasoning, Effort: sent, Millis: time.Since(began).Milliseconds(), Status: call.Status,
					TTFT: try.TTFT, FirstText: try.FirstText, Session: sessionOf(r.Header), NativeSession: nativeSessionOf(r.Header), Kind: call.Kind,
					RequestID: call.Usage.RequestID, Endpoint: endpointOf(r, from, call.To), Archive: call.archiveName()}
				failedWith(&rec, call.Status, call.Error, call.Usage.ErrType)
				// what this account answered is its refusal, the reply
				// captured so far being no one's yet
				refusal := &Call{RequestBody: call.RequestBody, RequestTruncated: call.RequestTruncated, ResponseBody: string(hw.errBody())}
				if call.otelIn != nil {
					refusal.otelIn, refusal.otelOut = call.otelIn, hw.errBody()
				}
				withBodies(&rec, refusal)
				appendUsage(r, rec)
			}
			continue
		}
		if !last && hw.failed() && promptRefused(hw.code(), hw.errBody()) {
			// WorkBuddy's "Illegal API invocation from an unapproved
			// channel": its security policy turns away the agent's system
			// prompt (Claude Code's own, #182), whichever account and
			// reasoning level it is asked at (Discord, lemon: both WorkBuddy
			// AI accounts rested over an agent's chat, for every agent).
			// Nothing is wrong with the account, so it doesn't rest; its
			// mates, carrying the same prompt, are asked last
			if other == nil {
				other = &Try{Status: call.Status, Error: call.Error}
			}
			try.Fail = failPrompt
			s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1] = try })
			skipped = append(skipped, c.label()+": "+call.Error)
			matesLast(cands[i+1:], c)
			continue
		}
		if c.p.Account != nil && !hw.passing && hw.code() >= 400 && hw.code() < 500 && modelTakes(c, sent) {
			if takes, ok := effortRefused(hw.errBody(), sent); ok {
				// the account's plan doesn't take the level, which the
				// model has (#520: a Free ChatGPT account, gpt-6-luna at
				// high): another account is asked, and this one doesn't
				// rest, as it serves the model at other levels
				noteEffortRefused(c, sent, takes)
				try.Fail = failEffort
				s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1] = try })
				skipped = append(skipped, c.label()+": "+call.Error)
				if !last {
					matesFirst(cands[i+1:], c)
					continue
				}
				call.Status, call.Error = http.StatusBadRequest, effortError(c, sent, takes, call.Error)
				failTo(w, kept, from, call.Status, call.Error)
				break
			}
		}
		if !last && !hw.passing && !hw.refused && (hw.code() == http.StatusRequestEntityTooLarge || overflowed(hw.code(), hw.errBody())) {
			// too long for this member's model, and as long on every
			// account of it, but another member — another model, or this
			// one at another vendor — may hold it (#700: the overflow went
			// to Kimi Code, which took 0.85 of the request for the model's
			// window, where the next member would have answered). Only
			// those not known to be too small are left to ask; none, and
			// the agent is told, to compact. A byte cap (413) also belongs
			// to this member: another vendor may take the same body. It
			// doesn't say the request exceeds another model's window, so
			// no token estimate filters those members. Nobody rests.
			tokens := 0
			if hw.code() != http.StatusRequestEntityTooLarge {
				if req, err := parse(from, attemptBody); err == nil {
					tokens = estimate(req)
				}
			}
			if left := withRoom(cands[i+1:], c, tokens); len(left) > 0 {
				if other == nil {
					other = &Try{Status: call.Status, Error: call.Error}
				}
				try.Fail = failOverflow
				if hw.code() == http.StatusRequestEntityTooLarge {
					try.Fail = failOther
				}
				s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1] = try })
				skipped = append(skipped, c.label()+": "+call.Error)
				cands = append(cands[:i+1:i+1], left...)
				continue
			}
		}
		if !last && hw.failed() && shapeRefused(hw.code(), hw.errBody()) {
			// a request this vendor's API can't read (xAI's 422 over an
			// input item it doesn't know, #350) another's may: the next is
			// asked once, as each is, and this one doesn't rest
			if other == nil {
				other = &Try{Status: call.Status, Error: call.Error}
			}
			try.Fail = failShape
			s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1] = try })
			skipped = append(skipped, c.label()+": "+call.Error)
			continue
		}
		if !last && hw.failed() && failure(hw.code(), hw.errBody()) == failProxy {
			// the proxy didn't take the connection: the request never got
			// to the vendor, so this one doesn't rest, and a member that
			// goes another way (or the proxy back up) may still answer (#381)
			if other == nil {
				other = &Try{Status: call.Status, Error: call.Error}
			}
			try.Fail = failProxy
			s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1] = try })
			skipped = append(skipped, c.label()+": "+call.Error)
			continue
		}
		if !last && hw.failed() && failureOf(c, hw.code(), hw.errBody()) == failAuth {
			// the account's sign-in is gone, refused by Anthropic: no rest
			// brings it back, so none is told; it is passed over until it
			// is signed in again (provider/claude_auth.go), and the next
			// one is asked
			if other == nil {
				other = &Try{Status: call.Status, Error: call.Error}
			}
			try.Fail = failAuth
			s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1] = try })
			skipped = append(skipped, c.label()+": "+call.Error)
			continue
		}
		if wait, ok := passing(hw.code(), hw.header, hw.errBody(), again); ok && !last && hw.failed() && spentAfter(cands[i+1:]) {
			// the others left are out of their allowance (Discord, waroy: a
			// Codex account run out, Grok busy a moment): this one is the
			// last that may answer, and is tried again as the last is
			try.Fail, try.Again = failureOf(c, hw.code(), hw.errBody()), wait.Milliseconds()
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
		if !last && hw.failed() {
			if f := failure(hw.code(), hw.errBody()); other == nil && f != failQuota && f != failCredit {
				other = &Try{Status: call.Status, Error: call.Error}
			}
			rest := s.restAfterMarked(c, hw.code(), hw.header, hw.errBody(), hw.sharedPool)
			try.Fail, try.Rest = rest.Why, &rest
			s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1] = try })
			skipped = append(skipped, c.label()+": "+call.Error)
			continue
		}
		if wait, ok := passing(hw.code(), hw.header, hw.errBody(), again); ok && hw.failed() {
			// nobody else is left: the same one again, after a moment
			try.Fail, try.Again = failureOf(c, hw.code(), hw.errBody()), wait.Milliseconds()
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
		// (not for one account pinned: the others weren't asked)
		if !autoReset && pin == "" && last && other == nil && hw.failed() && !hw.passing && failure(hw.code(), hw.errBody()) == failQuota {
			// everyone is out of their allowance: a Codex or Claude account
			// the user lets spend its resets by itself, its week used up,
			// spends one and is asked again
			autoReset = true
			if pick, out, ok := s.autoReset(r.Context(), cands, c, pl.held); ok {
				try.Fail = failQuota
				try.Reset = &AutoReset{Who: pick.p.Account.User, Text: out.Text(), Agent: pick.p.Account.Agent}
				s.trace.update(tr, func(t *Route) {
					t.Tries[len(t.Tries)-1] = try
					t.Order, t.Left = unhold(t.Order, t.Left, pick)
				})
				skipped = append(skipped, c.label()+": "+call.Error, pick.label()+": used one of its resets by itself ("+out.Text()+")")
				cands = append(cands[:len(cands):len(cands)], pick)
				continue
			}
		}
		if hw.refused && !hw.passing {
			// nobody is left: the agent is told it was refused, as a
			// request it shouldn't send again as it is, not handed an empty
			// reply it would ask again for, paying for each
			failTo(w, kept, from, refusedStatus, call.Error)
		} else if f := failure(call.Status, []byte(call.Error)); other != nil && !hw.passing && call.Status >= 400 && (f == failQuota || f == failCredit) {
			// the last one left is out of its allowance, but one before it
			// failed otherwise: the agent is told that one's error, not
			// the allowance's — Codex, told its usage is exhausted, stops
			// taking input though another member would answer next time
			// (Discord, waroy)
			try.Fail = f
			s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1] = try })
			call.Status, call.Error = other.Status, other.Error
			failTo(w, kept, from, call.Status, call.Error)
			break
		} else if outgrew {
			// in the agent's own words for it, for it to compact
			failTo(w, kept, from, call.Status, hw.failMsg)
		} else if !hw.passing && hw.code() == http.StatusRequestEntityTooLarge {
			// No member left took the body; only now add the byte-limit hint.
			failTo(w, kept, from, call.Status, call.Error)
		} else {
			hw.release()
			if hw.passing {
				clientResponseID = call.Usage.ResponseID
			}
		}
		if call.Status < 400 && call.Error != "" && r.Context().Err() == nil {
			// the reply had begun, its 200 sent, and broke off (#733:
			// Cursor's "Unable to reach the model provider" after some of
			// the text): not an answer. What the agent got stays as it is,
			// but the conversation isn't kept here for the agent's retry,
			// and a vendor that failed (not a reply too long, nor one its
			// filter refused) rests, so the retry goes to another member. A
			// reply the agent stopped itself is booked as before
			try.Fail = failureOf(c, call.Status, []byte(call.Error))
			// a compaction a rule sent to a model of its own isn't a turn of
			// the conversation: it leaves the stick where it was when it
			// answers (below), and breaking off mid-reply forgets nothing
			// either (#776's follow-up)
			if hit == nil || !hit.Compact {
				unanswered(stuck, c)
			}
			if hw.refusedAfter {
				// the vendor's filter refused the turn it had begun, as it
				// refused one before any of it was said (#248): the account
				// is at fault no more for it, and nobody rests
				try.Fail = failRefused
			} else if lateRests(call.Error) {
				rest := s.restAfter(c, call.Status, hw.header, []byte(call.Error))
				try.Fail, try.Rest = rest.Why, &rest
			}
		} else if call.Status < 400 {
			servedCandidate(c, call.Usage.Input+call.Usage.Output+call.Usage.CacheRead+call.Usage.CacheWrite)
			// a compaction a rule sent to a model of its own leaves the
			// conversation, and the turn's size, where they were
			if hit == nil || !hit.Compact {
				answered(stuck, c, aff.Turn, call.Usage.CacheRead)
			}
			if hit != nil && !hit.Compact {
				ruleAnswered(ruleAt, call.Usage)
			}
			for _, at := range nestedAt {
				ruleAnswered(at, call.Usage)
			}
		} else if hw.refused {
			try.Fail = failRefused
		} else if promptRefused(call.Status, []byte(call.Error)) {
			try.Fail = failPrompt
		} else {
			try.Fail = failureOf(c, call.Status, []byte(call.Error))
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
	call.Usage.ResponseID = clientResponseID
	finishCapture()
	// a reply the vendor gave that is no use to its caller all the same —
	// a title request answered with no title (#743) — is in the Routing
	// and Usage views with why
	if check := replyCheck(r.Context()); check != nil && call.Status < 400 && call.Error == "" && !call.ResponseTruncated {
		call.Error = check(call.ResponseBody)
	}
	replyDigest := ""
	if link != nil && isTitleKind(call.Kind) && call.Status < 400 && call.Error == "" && !call.ResponseTruncated {
		replyDigest = titleReplyDigest([]byte(call.ResponseBody), replyTitleShape(r.Context()))
	}
	prompt, window := promptRead(), 0
	if prompt != nil && lastTried.ID != "" {
		window = windowOf(lastTried, model)
	}
	s.trace.update(tr, func(t *Route) {
		t.Done, t.Status, t.Error, t.Millis = true, call.Status, call.Error, call.Millis
		if t.TitleLink != nil && isTitleKind(t.Kind) && call.Status < 400 && call.Error == "" && !call.ResponseTruncated {
			link := *t.TitleLink
			link.Reply = replyDigest
			t.TitleLink = &link
		}
		if call.To != "" {
			t.Usage = append(t.Usage, routeUsage(call.Provider, model, call.Usage)...)
		}
		t.Tokens = call.Usage.Input + call.Usage.Output + call.Usage.CacheRead + call.Usage.CacheWrite
		t.Output, t.Reasoning, t.TTFT, t.FirstText = call.Usage.Output, call.Usage.Reasoning, call.TTFT, call.FirstText
		if prompt != nil {
			t.Prompt = prompt.calibrated(promptCounted(t.Usage), window)
		}
		if n := len(t.Tries); n > 0 && call.Status < 400 {
			t.Served, t.Swapped, t.Routed = t.Tries[n-1].Served, t.Tries[n-1].Swapped, t.Tries[n-1].Routed
			t.Upstream = t.Tries[n-1].Upstream
		}
	})
	s.record(call)
	if call.To != "" {
		rec := usage.Record{RouteID: tr.ID, Time: start, Agent: call.Agent, Via: call.Via, Provider: call.Provider, Host: where, Model: model,
			ProviderKeyID: providerKeyID, ProviderKeyName: providerKeyName, ProviderAccount: providerAccount,
			Requested: call.Model, Served: call.Usage.Served, Upstream: call.Usage.Upstream,
			Input: call.Usage.Input, Output: call.Usage.Output, CacheRead: call.Usage.CacheRead,
			CacheWrite: call.Usage.CacheWrite, CacheWrite1h: call.Usage.CacheWrite1h, Reasoning: call.Usage.Reasoning, Effort: sent, Millis: call.Millis, Status: call.Status,
			TTFT: call.TTFT, FirstText: call.FirstText, Sent: sentMs, Session: sessionOf(r.Header), NativeSession: nativeSessionOf(r.Header), Kind: call.Kind,
			RequestID: call.Usage.RequestID, ResponseID: call.Usage.ResponseID, Endpoint: endpointOf(r, from, call.To), Stop: call.Usage.Stop, Archive: call.archiveName()}
		failedWith(&rec, call.Status, call.Error, call.Usage.ErrType)
		withBodies(&rec, &call)
		appendUsage(r, rec)
	}
}

// refusedError is what the agent is told of a refusal by the vendor's
// safety filter, with nothing said before it (#248).
func refusedError(p provider.Provider, model, why string) string {
	return fmt.Sprintf("%s refused this request (%s) via %s", model, why, p.Name)
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
	// Normalize for the actual destination, separately on each fallback.
	// Native ChatGPT accounts accept standalone tool outputs themselves.
	if from == provider.Responses && (p.Account == nil || p.Account.Agent != "codex") {
		body = orphanedToolOutputs(body)
	}
	// every request to the provider goes through its own proxy, if it has
	// one (#237)
	r = r.WithContext(p.Via(r.Context()))
	// the reply says who answered it (#822)
	noteMember(w, r, p, model)
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
	if p.Account != nil && (p.Account.Agent == "qoder" || p.Account.Agent == provider.QoderCNID) {
		call.To = from
		return s.serveQoder(w, r, from, p, model, body, &call.Usage)
	}
	// Zed's through cloud.zed.dev, written in the API of the model's own
	// provider, with the account magpie signed in to
	if p.Account != nil && p.Account.Agent == "zed" {
		call.To = from
		return s.serveZed(w, r, from, p, model, body, &call.Usage)
	}
	// Command Code's Go plan has no Provider API: it is asked where the
	// CLI asks, in the CLI's own format; its other plans go on as keys do
	if p.Account != nil && p.Account.Agent == provider.CommandCodePlanID {
		if api, key, ok := cmdGenerate(r.Context(), p); ok {
			call.To = from
			return s.serveCommandCode(w, r, from, p, api, key, model, body, &call.Usage)
		}
	}
	// Copilot's Auto (all a Student plan may pick) is asked which model it
	// picks, as Copilot's clients do, and the request goes to that model on
	// the APIs it is served on, with the session's token
	if model == provider.CopilotAuto && p.Account != nil && p.Account.Agent == "copilot" {
		r = r.WithContext(provider.WithAutoPrompt(r.Context(), lastUserText(from, body)))
	}
	if ctx, m, err := p.ResolveAuto(r.Context(), model); err != nil {
		msg := p.Name + ": " + err.Error()
		return writeError(w, from, 502, msg), msg
	} else if m != model {
		r, model = r.WithContext(ctx), m
		noteMember(w, r, p, model)
	}
	// a backend that only streams gets a non-streaming request translated
	// (the provider is always streamed on that path) rather than relayed
	relay := slices.Contains(s.usable(p, model), from) && (p.Account == nil || !p.Account.Stream || streamOf(body))
	// a Claude model is asked on Messages whatever API the client spoke,
	// for the cache breakpoints the translation adds: OpenCode speaks Chat
	// for every model, and a relay drops cache_control on Chat, so every
	// turn was billed uncached (ReturnTrue on Discord, after #997)
	if relay && from != provider.Anthropic && p.OnMessages(model) && slices.Contains(s.usable(p, model), provider.Anthropic) {
		relay = false
	}
	// a web search offered is done by the provider, or by magpie for it,
	// which a relayed request can't. The request is translated on the
	// client's own API all the same, not on Chat: Codex offers web_search
	// on every turn, and on a provider serving Responses each went out as
	// Chat (#997, Xiaomi MiMo)
	ownAPI := false
	// except a subagent's task its lead sealed (#1109): only the server
	// that sealed it reads it, and a translation would drop it
	if relay && searchAsked(from, body) && (from == provider.Chat && !p.IsRemoteMagpie() || !searchesModel(p, from, model)) && !(from == provider.Responses && hasSealedAgentMessage(body)) {
		relay, ownAPI = false, true
	}
	// Zen's free models are asked as OpenCode asks them (zenfree.go)
	if p.OpenCodeFree(model) {
		relay = false
	}
	// Factory's generate route always answers SSE, and droid sends no stream
	// field. A client that asked for one JSON body is translated, which
	// reads that SSE and writes the JSON. Relaying it would hand the client
	// the data: lines under a 200.
	if relay && from == provider.Gemini && !streamOf(body) {
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
	if ownAPI && slices.Contains(to, from) && !firstElsewhere(p, model, from) {
		to = []provider.Protocol{from}
	}
	call.To = to[0]
	return s.translate(w, r, p, from, to[0], model, body, &call.Usage)
}

// firstElsewhere is whether model is best asked on an API other than
// from at p, which usable puts first: Responses for an OpenAI model on
// OpenAI's API, Messages for a Claude model where it is served.
func firstElsewhere(p provider.Provider, model string, from provider.Protocol) bool {
	return p.ResponsesFirst(model) && from != provider.Responses || p.MessagesFirst(model) && from != provider.Anthropic
}

// autoPicks is whether c is Copilot's Auto, which picks the model itself.
func autoPicks(c candidate) bool {
	return c.model == provider.CopilotAuto && c.p.Account != nil && c.p.Account.Agent == "copilot"
}

// lastUserText is the text of the request's last user turn, which
// Copilot's Auto picks a model for.
func lastUserText(from provider.Protocol, body []byte) string {
	req, err := parse(from, body)
	if err != nil {
		return ""
	}
	for i := len(req.Messages) - 1; i >= 0; i-- {
		m := req.Messages[i]
		if m.Role != "user" {
			continue
		}
		var b strings.Builder
		for _, p := range m.Parts {
			if p.Kind == Text {
				if b.Len() > 0 {
					b.WriteString("\n")
				}
				b.WriteString(p.Text)
			}
		}
		if b.Len() > 0 {
			return b.String()
		}
	}
	return ""
}

// markOpenRouterSharedPool keeps an upstream routing fact in the held attempt,
// rather than exposing it as a response header.
func markOpenRouterSharedPool(w http.ResponseWriter) {
	if h, ok := w.(*holdWriter); ok {
		h.sharedPool = true
	}
}

// markAntigravityTurnsAway keeps in the held attempt that the account was
// asked with the system prompt Antigravity turns away, so a failure on it
// isn't held against the account.
func markAntigravityTurnsAway(w http.ResponseWriter) {
	if h, ok := w.(*holdWriter); ok {
		h.turnedAway = true
	}
}

// markAntigravityRefused marks the attempt when what the account said is the
// refusal Antigravity answers such a system prompt with. said is what the
// failure said once the status is gone: the 429's body arrives with its 429,
// and the reply's own error event arrives with 200 from translate and only
// its message, the Code Assist decoder keeping neither its code nor its
// status. An error event of any other kind says something else and marks
// nothing.
func markAntigravityRefused(w http.ResponseWriter, p provider.Provider, system, said string) {
	if said == "" || !antigravityRefuses(said) || accountAgent(p) != "antigravity" || !antigravityTurnsAway(system) {
		return
	}
	markAntigravityTurnsAway(w)
}

// forward sends a request to the provider. On Anthropic's messages, a
// provider that turns away betas it doesn't know by name (Bedrock's: 400
// Unexpected value(s) `x` for the `anthropic-beta` header) is asked again
// once without them, and they're left out for it from then on. An account's
// 403 or 400 it can mend (Provider.Retry) is asked again, a few times at most.
func (s *Server) forward(ctx context.Context, p provider.Provider, to provider.Protocol, path string, body []byte, in http.Header) (*http.Response, error) {
	res, err := s.forwardOnce(ctx, p, to, path, body, in)
	if err == nil && (res.StatusCode == http.StatusForbidden || res.StatusCode == http.StatusBadRequest) && p.Retries() {
		// an account that can mend what the refusal names (a Factory org
		// the server won't take; a model Copilot's Auto picked that the
		// account is refused, for which Auto picks another) is asked again
		for range 3 {
			b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
			res.Body.Close()
			res.Body = io.NopCloser(bytes.NewReader(b))
			if !p.Retry(ctx, body, res.StatusCode, b) {
				break
			}
			if m := p.AutoNext(ctx); m != "" && !slices.Contains(s.usable(p, m), to) {
				// Auto's next pick isn't served on this API (gpt-4.1 on
				// chat alone, after gpt-5.3-codex on /responses, #256): the
				// refusal is passed on, and the request made again for it
				// on its own API (the candidates' loop)
				break
			}
			if res, err = s.forwardOnce(ctx, p, to, path, body, in); err != nil || res.StatusCode != http.StatusForbidden && res.StatusCode != http.StatusBadRequest {
				return res, err
			}
		}
	}
	// and what of Claude Code's newest asks it turns away (thinking.display
	// "updates", a turn's output_config in messages: anthropic_shapes.go),
	// each learned once
	for range 3 {
		if err != nil || to != provider.Anthropic || res.StatusCode != http.StatusBadRequest {
			return res, err
		}
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		res.Body = io.NopCloser(bytes.NewReader(b))
		betas := len(s.refuseBetas(p, b)) > 0
		if shapes := s.refuseShapes(p, b, body); !betas && !shapes {
			return res, nil
		}
		res, err = s.forwardOnce(ctx, p, to, path, body, in)
	}
	return res, err
}

// forwardOnce is one request to the provider, as forward makes it.
func (s *Server) forwardOnce(ctx context.Context, p provider.Provider, to provider.Protocol, path string, body []byte, in http.Header) (*http.Response, error) {
	ctx = p.Via(ctx)
	body = deepseekToolPatterns(p, to, body)
	body = toolOneOfAsAnyOf(p, to, body)
	body = kimiToolEnumTypes(p, to, body)
	body = clinePin(p, to, body)
	if to == provider.Anthropic {
		body = s.bodyBetas(p, body)
		body = s.withoutRefusedShapes(p, body)
		// what every path to an Anthropic endpoint sends, relayed or
		// built, with the model named as the vendor names it
		body = adaptiveThinking(body)
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
		asked := askedBetas(in)
		if gjson.GetBytes(body, "speed").String() == "fast" && provider.HostOf(p.Base(to)) == "api.anthropic.com" {
			asked = append(slices.Clone(asked), claudeFastBeta) // a group's member sent fast
		}
		if bs := s.betas(p, asked); len(bs) > 0 {
			req.Header.Set("anthropic-beta", strings.Join(bs, ","))
		} else {
			req.Header.Del("anthropic-beta")
		}
	}
	if p.Account == nil && (to == provider.Responses || to == provider.Chat) && fromCodex(in) {
		// a relay that serves only Codex (Discord: "This account only
		// allows Codex official clients") knows it by its User-Agent,
		// originator and x-codex- headers, which go on as Codex sent them,
		// as they do when Codex talks to the relay itself; its key to
		// magpie never does
		for k, vs := range in {
			if codexClientHeader(k) {
				req.Header[k] = slices.Clone(vs)
			}
		}
	}
	if p.IsOpenCode() {
		// as OpenCode itself sends it, which Zen's free tier asks for
		provider.OpenCodeClient(req.Header, conversationID(in, body))
	}
	if p.IsCline() {
		// as Cline's desktop app sends it: its free models are 403'd
		// ("only available via Cline product surfaces") without
		provider.ClineClient(req.Header)
	}
	if p.IsKilo() {
		// as the Kilo CLI sends it, signed out when the provider has no
		// key: the gateway serves its free models to anyone
		provider.KiloClient(req.Header, p.Key, conversationID(in, body))
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
	if p.IsPlugin() {
		req.Header.Set(provider.ConversationHeader, conversationID(in, body))
	}
	if p.IsRemoteMagpie() {
		passOnCaller(ctx, req)
		markSearching(ctx, req)
	}
	if err := p.Sign(ctx, req, to, body); err != nil {
		return nil, err
	}
	if to == provider.Anthropic {
		s.fitUserBetas(p, req.Header)
	}
	res, err := p.Do(s.client, req)
	if err != nil {
		return res, err
	}
	// Cline's whole replies come in {success, data}; a web page or
	// nothing at all, served 200, is the 502 it stands for (#1012)
	return notAnAPIReply(clineUnwrapped(p, res), ""), nil
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

// fromCodex is a request Codex sent — its CLI, exec, the IDE extension or
// the desktop app — by the User-Agent (codex_cli_rs/0.159.2 (Mac OS 26.6.0;
// arm64) kitty, Codex Desktop/0.162.3 …) or the originator it gives
// (codex_cli_rs, codex_exec, codex_vscode, Codex Desktop).
func fromCodex(in http.Header) bool {
	for _, v := range []string{in.Get("User-Agent"), in.Get("originator")} {
		if strings.HasPrefix(strings.ToLower(v), "codex") {
			return true
		}
	}
	return false
}

// codexClientHeader is one of the headers Codex tells itself by to an API
// it talks to with a key: its User-Agent and originator, the session and
// thread (session-id, thread-id), and what codexHeader lets through. Never
// the key it was given, nor its sign-in's attestation.
func codexClientHeader(k string) bool {
	switch strings.ToLower(k) {
	case "user-agent", "originator", "session-id", "thread-id":
		return true
	}
	return codexHeader(k)
}

// passthrough relays a request the provider understands as-is, with the
// model name swapped for the provider's own. The token counts the reply
// carries are read on the way past into u. done is false, with nothing
// written, when the provider serves the model on another of its endpoints
// but not this one.
func (s *Server) passthrough(w http.ResponseWriter, r *http.Request, p provider.Provider, proto provider.Protocol, model string, body []byte, u *Usage) (status int, msg string, done bool) {
	upstream := provider.UpstreamNameIn(wiresOf(r.Context()), p.ID, model)
	body = rewriteModel(body, upstream)
	searchFn := false // Codex's tool search sent as a function
	// a model that wants its reasoning text back (#388, #1104)
	replay := proto == provider.Responses && (replaysReasoning(model, p.Host()) || replaysReasoning(upstream, p.Host()))
	switch proto {
	case provider.Responses:
		// Responses Lite's tools, as an input item, go as OpenAI takes them
		// only to OpenAI (#350)
		if (p.Account == nil || p.Account.Agent != "codex") && !strings.HasSuffix(p.Host(), "openai.com") {
			body = liftAdditionalTools(body)
		}
		// only the ChatGPT backend runs Codex's tool search as Codex sends it
		if p.Account == nil || p.Account.Agent != "codex" {
			body, searchFn = searchAsFunction(body)
		}
		body = forVendor(p, body)
		if replay {
			// its reasoning goes back as text, never left out: DeepSeek
			// refuses a request without it (#1104)
			body = withReasoningText(body)
		} else if strings.HasSuffix(p.Host(), "openai.com") || p.IsAzure() || !s.fits(p.ID, bareReasoningRefused(model), proto) {
			// reasoning magpie gave Codex, an id with nothing sealed in
			// it, which they'd look up and not find (#1008), nor does an
			// upstream that has turned it away before (#1044)
			body = withoutBareReasoning(body)
		}
		// Relays enforce OpenAI's item ID prefixes too, including during
		// compaction. call_id stays unchanged so tool outputs remain paired.
		body = callItemIDs(body)
		// xAI's API turns away a tool_choice with no tools beside it ("A
		// tool_choice was set on the request but no tools were specified"),
		// and Copilot's /responses, in front of it for Grok, with a bare
		// 400: Codex's compaction summary goes without its tools (#378).
		// Anywhere else it goes as asked, which a relay checking Codex's
		// shape wants (#292).
		if p.Host() == "api.x.ai" || p.Account != nil && p.Account.Agent == "copilot" {
			body = withoutLoneToolChoice(body)
		}
	case provider.Chat:
		body = developerAsSystem(body)
		// Gemini's thought signatures, which came to the client in its
		// calls' ids, go back where Gemini wants them; another upstream
		// gets the calls' own ids (#687)
		body = chatCallSignatures(body, geminiCompat(p.Host(), model))
		if p.Preset == "mistral" || p.Host() == "api.mistral.ai" {
			// an earlier turn's reasoning_content, which Mistral turns
			// away, as the thinking part it takes (#494)
			body = mistralThinking(body)
		}
		if strings.HasSuffix(p.Host(), "openai.com") || p.IsAzure() {
			// Qwen's switch and Kimi Code's (thinking: {type: …}), which
			// OpenAI turns away as arguments it doesn't know, and Azure
			// OpenAI as well
			body = withoutFields(body, "enable_thinking", "thinking")
		}
		if p.IsBedrock() || p.IsAzure() {
			// Azure's reasoning deployments (o4-mini, gpt-5) turn
			// max_tokens away as Bedrock's GPT models do
			body = asCompletionTokens(body)
		}
	case provider.Gemini:
		// the handler put stream in the body so serve can tell a
		// streamGenerateContent from a generateContent. droid's generate
		// body has no such field, and Factory ignores it.
		body = withoutFields(body, "stream")
	case provider.Anthropic:
		if p.IsBedrock() {
			// Claude Code's metadata.user_id, a JSON string these days, is
			// not the plain id Bedrock checks it against (#176)
			body = withoutFields(body, "metadata")
		}
		if !anthropicModel.MatchString(model) {
			body = thinkingOffUnlessAsked(body)
			// Claude Code's auto mode classifier, on a vendor's model that
			// may think whatever it is told (#250); a Claude model's request
			// goes as it was sent, safeguards and all
			body = autoModeClassifierBody(model, body)
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
	if proto != provider.Anthropic {
		body = s.withoutRefused(p.ID, proto, body)
	}
	// Codex's image tool, which a vendor that knows no namespaces turned
	// away before (#949)
	dropImage := proto == provider.Responses && mayDropImageTool(p)
	if dropImage && !s.fits(p.ID, imageToolRefused, proto) {
		body, _ = withoutImageTool(body)
	}
	// a Grok subscription is given Codex's namespaced functions flat
	// (grokBody), and Zed's plugin likewise (ZedBody); a call to one goes
	// back under its namespace (#404)
	var named map[string]nsTool
	if proto == provider.Responses && p.Account != nil && (p.Account.Agent == "grok" || p.PluginProvider() == "grok" || p.PluginProvider() == "zed") {
		named = namespacedIn(body)
	}
	res, err := s.forward(r.Context(), p, proto, path, p.Prepare(body), r.Header)
	if err != nil {
		return writeError(w, proto, 502, p.Name+": "+err.Error()), err.Error(), true
	}
	// a vendor that turns away the fields it doesn't know (Mistral's
	// extra_forbidden for OpenCode's store, #393) is asked again without
	// the optional ones it named, and not sent them again once that works
	var refused []string
	for proto != provider.Anthropic && badRequest(res.StatusCode) {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		res.Body = io.NopCloser(bytes.NewReader(b))
		fs := refusedOptional(res.StatusCode, b, body)
		// and an earlier turn's thinking sent back inside messages, which
		// DeepSeek and Kimi want and a stricter vendor turns away (#494)
		var ms []string
		if proto == provider.Chat {
			ms = refusedInMessages(res.StatusCode, b, body)
		}
		if len(fs) == 0 && len(ms) == 0 && proto == provider.Responses && refusesInput(res.StatusCode, b) {
			// and reasoning with nothing sealed in it, which a relay in
			// front of OpenAI turns away as OpenAI does, naming only the
			// input (#1044): asked once more without it. A DeepSeek
			// model wants its reasoning back (#388), so it goes to every
			// other upstream until one refuses it, and is never asked
			// without it: that refusal is over something else, and the
			// mark would leave its reasoning out of every later request
			// (#1104).
			if nb := withoutBareReasoning(body); !replay && !bytes.Equal(nb, body) {
				refused = append(refused, bareReasoningRefused(model))
				body = nb
				if res, err = s.forward(r.Context(), p, proto, path, p.Prepare(body), r.Header); err != nil {
					return writeError(w, proto, 502, p.Name+": "+err.Error()), err.Error(), true
				}
				continue
			}
		}
		if len(fs) == 0 && len(ms) == 0 {
			// and a bare 400 over Codex's image tool, which a vendor
			// that knows no namespaces gives (#949): asked once more
			// without it
			if !dropImage {
				break
			}
			nb, had := withoutImageTool(body)
			if !had {
				break
			}
			refused = append(refused, imageToolRefused)
			body = nb
			if res, err = s.forward(r.Context(), p, proto, path, p.Prepare(body), r.Header); err != nil {
				return writeError(w, proto, 502, p.Name+": "+err.Error()), err.Error(), true
			}
			continue
		}
		refused = append(refused, fs...)
		body = withoutFields(body, fs...)
		for _, f := range ms {
			refused = append(refused, messageField(f))
		}
		body = withoutMessageFields(body, ms...)
		if res, err = s.forward(r.Context(), p, proto, path, p.Prepare(body), r.Header); err != nil {
			return writeError(w, proto, 502, p.Name+": "+err.Error()), err.Error(), true
		}
	}
	if res.StatusCode < 400 {
		for _, f := range refused {
			s.markUnfit(p.ID, f, proto)
		}
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
	u.RequestID = requestID(res.Header)
	if res.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		msg := p.Explain(p.Name+": "+provider.APIError(b, res.Status), res.StatusCode, b)
		if wrongEndpoint(res.StatusCode, b) {
			s.markUnfit(p.ID, model, proto)
			if len(s.usable(p, model)) > 0 {
				u.RequestID = "" // another endpoint answers, with its own id
				return res.StatusCode, msg, false
			}
			msg += wrongAPINote(p, model, res)
		}
		if p.Preset == "openrouter" && openRouterSharedPool(b) {
			markOpenRouterSharedPool(w)
		}
		keepRetry(w.Header(), res.Header, b)
		u.ErrType = provider.ErrorType(b)
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
	// calls a model wrote into its text are made calls (textcalls.go)
	var written *textCallTidy
	if names := chatToolNames(body); proto == provider.Chat && sse && names != nil {
		written = &textCallTidy{names: names}
	}
	var whole *chatWhole
	if proto == provider.Chat && !sse && strings.Contains(res.Header.Get("Content-Type"), "json") {
		whole = &chatWhole{}
	}
	var sigs *sigTidy
	if proto == provider.Chat && geminiCompat(p.Host(), model) {
		sigs = &sigTidy{sse: sse}
	}
	var search *searchTidy
	if searchFn && sse {
		search = &searchTidy{}
	}
	var spaces *nsTidy
	if named != nil {
		spaces = &nsTidy{named: named, sse: sse}
	}
	buf := make([]byte, 32<<10)
	var rerr error
	for {
		n, err := rd.Read(buf)
		if n > 0 {
			sniff.write(buf[:n])
			out := buf[:n]
			if tidy != nil {
				out = tidy.write(out)
			}
			if written != nil {
				out = written.write(out)
			}
			if whole != nil {
				out = whole.write(out)
			}
			if search != nil {
				out = search.write(out)
			}
			if spaces != nil {
				out = spaces.write(out)
			}
			if sigs != nil {
				out = sigs.write(out)
			}
			if _, werr := w.Write(out); werr != nil {
				return res.StatusCode, "", true
			}
			if f != nil {
				f.Flush()
			}
		}
		if err != nil {
			rerr = err
			break
		}
	}
	// what's held at the end goes through the filters after the one
	// that held it
	sign := func(out []byte) []byte {
		if sigs != nil {
			return sigs.write(out)
		}
		return out
	}
	if tidy != nil {
		out := tidy.flush()
		if written != nil {
			out = append(written.write(out), written.flush()...)
		}
		w.Write(sign(out))
	} else if written != nil {
		w.Write(sign(written.flush()))
	}
	if whole != nil {
		out := whole.flush()
		if spaces != nil {
			out = spaces.write(out)
		}
		w.Write(sign(out))
	}
	if search != nil {
		out := search.flush()
		if spaces != nil {
			out = spaces.write(out)
		}
		w.Write(sign(out))
	}
	if spaces != nil {
		w.Write(sign(spaces.flush()))
	}
	if sigs != nil {
		w.Write(sigs.flush())
	}
	if sse {
		sniff.drain()
	}
	if sse && r.Context().Err() == nil && !sniff.whole() {
		// the upstream died mid-reply, or ended it short of its last
		// event: say so in the stream rather than end it as if whole,
		// which a client reads as a reply cut off for no reason (#370:
		// dsh's "stream ended before message_stop", not retried). A Chat
		// stream may end without [DONE] and be whole, so only a read that
		// failed counts there.
		var failed string
		switch {
		case rerr != nil && rerr != io.EOF:
			failed = cutMidReply(p.Name, rerr)
		case proto == provider.Anthropic || proto == provider.Responses:
			failed = p.Name + ": the reply ended before it was complete"
		}
		if failed != "" {
			w.Write(streamFailure(proto, failed))
			if f != nil {
				f.Flush()
			}
			return res.StatusCode, failed, true
		}
	}
	return res.StatusCode, sniff.failed, true
}

// cutMidReply is what a stream whose read failed mid-reply is ended with:
// the connection lost, said in so many words. Go's own "unexpected EOF"
// (every stream on an HTTP/2 connection that dropped ends so) was taken by
// dsh's pi-ai for an error it doesn't retry, and the turn failed (#470).
func cutMidReply(name string, err error) string {
	return name + ": connection lost mid-reply (" + err.Error() + ")"
}

// streamFailure is an error event ending a stream in proto, as each
// protocol's own server sends one mid-reply.
func streamFailure(proto provider.Protocol, msg string) []byte {
	var name string
	var v map[string]any
	switch proto {
	case provider.Chat:
		v = map[string]any{"error": map[string]any{"message": msg, "type": "api_error"}}
	case provider.Responses:
		name = "response.failed"
		v = map[string]any{"type": name, "response": map[string]any{"object": "response", "status": "failed",
			"error": map[string]any{"code": "server_error", "message": msg}}}
	default:
		name = "error"
		v = map[string]any{"type": name, "error": map[string]any{"type": "api_error", "message": msg}}
	}
	b, _ := json.Marshal(v)
	var out []byte
	if name != "" {
		out = append(out, "event: "+name+"\n"...)
	}
	out = append(out, "data: "...)
	out = append(out, b...)
	return append(out, "\n\n"...)
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
// Responses-first (and some Responses-only), and Claude where Anthropic's
// Messages API is served, which alone keeps its cache_control and
// thinking (#997).
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
	if p.MessagesFirst(model) {
		sort.SliceStable(out, func(i, j int) bool { return out[i] == provider.Anthropic && out[j] != provider.Anthropic })
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
	if req.ThinkOff && req.Effort != "none" && slices.Contains(p.Efforts(model), "none") {
		// the client turned reasoning off (Effort reads that as low), and
		// the model can stop thinking: asked for none (#899)
		r := *req
		r.Effort, req = "none", &r
	}
	if req.Effort != "" {
		if e := fitFor(p, model, req.Effort); e != req.Effort {
			r := *req
			r.Effort, req = e, &r
		}
	}
	// the names in force, read once however many endpoints the request is
	// built for below
	wires := wiresOf(ctx)
	web := req.WebSearch
	// the cache key was left out to see if it was what the upstream refused
	dropped := false
	// Codex's image tool was left out to see if it was (#949), and the
	// request with it, to go back to when it wasn't; tried once
	var withImage *Request
	imageTried := false
	for {
		if mayDropImageTool(p) && !s.fits(p.ID, imageToolRefused, to) {
			req, _ = withoutImageToolReq(req)
		}
		// only a provider that searches by itself is asked to
		if want := web && searchesFor(p, to, model, req); want != req.WebSearch {
			r := *req
			r.WebSearch, req = want, &r
		}
		if req.CacheKey != "" && !s.fits(p.ID, cacheKeyField, to) {
			r := *req
			r.CacheKey, req = "", &r
		}
		if offEffort(req.Effort) && !s.fits(p.ID, offRefused(model), to) {
			r := *req
			r.Effort, req = onEffort(p, model), &r
		}
		if forcesTool(req.ToolChoice) && !s.fits(p.ID, forcedRefused(model), to) {
			req = unforced(req)
		}
		if req.Format != nil && !s.fits(p.ID, formatRefused(model), to) {
			req = req.inSystem()
		}
		if to == provider.Anthropic && p.IsBedrock() && req.Metadata != nil {
			// not the plain id Bedrock checks metadata.user_id against (#176)
			r := *req
			r.Metadata, req = nil, &r
		}
		if want := to == provider.Chat && geminiCompat(p.Host(), model) && s.fits(p.ID, thinkingConfigField, to); want != req.GeminiCompat {
			r := *req
			r.GeminiCompat, req = want, &r
		}
		if req.GeminiCompat && req.ThinkOff {
			if l := geminiOffLevel(p, model, s.fits(p.ID, offRefused(model), to)); l != req.OffLevel {
				r := *req
				r.OffLevel, req = l, &r
			}
		}
		body, err := build(to, req, model, p.Host(), p.RejectsTemperature(model))
		if err != nil {
			return nil, to, err
		}
		if to == provider.Chat && (p.IsBedrock() || p.IsAzure()) {
			body = asCompletionTokens(body)
		}
		if to == provider.Chat && req.WebSearch && p.IsRemoteMagpie() {
			// the other magpie searches for it (search_remote.go)
			body = withFields(body, map[string]any{"web_search_options": map[string]any{}})
		}
		if to == provider.CodeAssist && p.Account != nil {
			// the envelope is named in there, where the id it carries is
			// known: on Antigravity that is the variant the effort picked,
			// not the model magpie knows
			body = codeAssistBody(p, req, model, wires)
		} else if wire := provider.UpstreamNameIn(wires, p.ID, model); wire != model {
			// the vendor's own name for the model goes in the model field
			// and nowhere else: everything above shaped the request from
			// the model magpie knows, which is what those decisions are
			// about
			body = rewriteModel(body, wire)
		}
		path := pathOf(to)
		if req.Resume && to == provider.Chat && chatPrefill(p.Host(), model) == "prefix" {
			// DeepSeek serves its prefix mode under /beta, beside the /v1
			// the base carries
			p.Chat = strings.TrimSuffix(p.Chat, "/v1")
			path = "/beta" + path
		}
		res, err := s.forward(ctx, p, to, path, p.Prepare(body), in)
		if err != nil || res.StatusCode < 400 {
			if dropped && err == nil {
				// it was the key: not sent there again
				s.markUnfit(p.ID, cacheKeyField, to)
			}
			if withImage != nil && err == nil {
				// it was the image tool: not offered there again
				s.markUnfit(p.ID, imageToolRefused, to)
			}
			return res, to, err
		}
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		res.Body = io.NopCloser(bytes.NewReader(b))
		if req.GeminiCompat && res.StatusCode == http.StatusBadRequest && geminiMinimalRefused.Match(b) && s.fits(p.ID, offRefused(model), to) {
			// minimal turned away by a Gemini that hasn't it, reasoning
			// off or asked for at minimal: the fields were taken, and the
			// model is asked again at its lowest, and so from then on
			s.markUnfit(p.ID, offRefused(model), to)
			continue
		}
		if req.GeminiCompat && refusesThinkingConfig(res.StatusCode, b) {
			// Gemini's own fields turned away (a proxy that isn't in front
			// of Google after all, or Google changing them): asked as
			// before, with reasoning_effort, and not sent them again
			s.markUnfit(p.ID, thinkingConfigField, to)
			continue
		}
		if req.Format != nil && refusesFormat(res.StatusCode, b) {
			// structured output turned away by the model or the relay in
			// front of it (DeepSeek takes json_object only; a schema
			// Anthropic can't hold): asked again with the format in the
			// system prompt, and so from then on
			s.markUnfit(p.ID, formatRefused(model), to)
			req = req.inSystem()
			continue
		}
		if offEffort(req.Effort) && res.StatusCode == http.StatusBadRequest && effortLevelsNamed.Match(b) {
			// reasoning turned off, which the model refuses naming the
			// levels it takes (Command Code's `expected one of "low"|…`
			// for Claude Code's auto mode classifier, #394): asked again
			// at its lowest, and so from then on
			s.markUnfit(p.ID, offRefused(model), to)
			r := *req
			r.Effort, req = onEffort(p, model), &r
			continue
		}
		if forcesTool(req.ToolChoice) && res.StatusCode == http.StatusBadRequest && toolChoiceRefused.Match(b) {
			// a model that can't be made to call a tool (#668): asked
			// again with the call left to it, and so from then on
			s.markUnfit(p.ID, forcedRefused(model), to)
			req = unforced(req)
			continue
		}
		if to == provider.Chat && res.StatusCode == http.StatusBadRequest && req.Effort != "none" &&
			toolsWithoutEffort.Match(b) && !s.servesElsewhere(p, model, to) && s.fits(p.ID, offRefused(model), to) {
			// tools with reasoning refused on chat, and no Responses API
			// to take them to: asked again without reasoning (#176)
			r := *req
			r.Effort, req = "none", &r
			continue
		}
		if withImage != nil {
			// not the image tool: offered again, and the next guess tried
			req, withImage = withImage, nil
		} else if !imageTried && mayDropImageTool(p) && badRequest(res.StatusCode) && !wrongEndpoint(res.StatusCode, b) && !provider.CopilotRefusal(b) {
			// a bare 400 over Codex's image tool, which a vendor's API
			// that can't take it gives (#949): asked again without it,
			// and not offered it again once that works
			if r, had := withoutImageToolReq(req); had {
				withImage, req, imageTried = req, r, true
				continue
			}
		}
		if req.CacheKey != "" && badRequest(res.StatusCode) && !wrongEndpoint(res.StatusCode, b) && !provider.CopilotRefusal(b) {
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

// optionalFields are request fields OpenAI's APIs (or an agent's own
// vendor, Kimi's and Qwen's thinking switches) take that a request does
// without: an upstream that refuses one by name is asked again without it.
var optionalFields = []string{"store", "metadata", "service_tier", cacheKeyField, "prompt_cache_retention",
	"safety_identifier", "stream_options", "parallel_tool_calls", "verbosity", "thinking", "enable_thinking"}

// refusedOptional lists optional fields an upstream explicitly rejects as
// unsupported. An invalid value must reach the client without dropping the
// field or caching it as unsupported for later requests.
func refusedOptional(status int, msg, body []byte) []string {
	if !badRequest(status) {
		return nil
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil {
		return nil
	}
	var fault any
	if json.Unmarshal(msg, &fault) != nil {
		fault = string(msg)
	}
	var out []string
	for _, f := range optionalFields {
		if _, ok := m[f]; !ok {
			continue
		}
		if unsupportedOptionalField(fault, f) {
			out = append(out, f)
		}
	}
	return out
}

var unknownOptionalField = regexp.MustCompile("(?i)\\b(?:unknown (?:name|field|parameter)|unsupported (?:parameter|field|property|argument)|unrecognized (?:request )?(?:argument|parameter)(?: supplied)?):?\\s*['\"\\x60]([a-z_]+)['\"\\x60]")
var unquotedUnsupportedOptionalField = regexp.MustCompile("(?i)\\bunsupported (?:parameter|field|property|argument):\\s*([a-z_]+)\\b")
var rejectedOptionalField = regexp.MustCompile("(?i)(?:^|\\b(?:parameter|field|property|argument)\\s+)['\"\\x60]([a-z_]+)['\"\\x60]\\s+(?:is\\s+)?(?:unsupported|not supported)\\b")

func unsupportedOptionalField(fault any, field string) bool {
	switch v := fault.(type) {
	case string:
		// A proxy can embed the vendor's JSON error in prose. Prefer its
		// structure to text matching, which could mistake an input echo
		// or a different parameter's error for a refusal.
		rest := v
		for {
			i := strings.IndexAny(rest, "{[")
			if i < 0 {
				break
			}
			var inner any
			dec := json.NewDecoder(strings.NewReader(rest[i:]))
			if dec.Decode(&inner) != nil {
				// not JSON there ([HTTP 400]): look on past the bracket
				rest = rest[i+1:]
				continue
			}
			if hasOptionalErrorObject(inner) {
				return unsupportedOptionalField(inner, field)
			}
			// [400] can be a status prefix. Look past it for a JSON
			// error object before falling back to the original prose.
			rest = rest[i+int(dec.InputOffset()):]
		}
		for _, match := range unknownOptionalField.FindAllStringSubmatchIndex(v, -1) {
			if v[match[2]:match[3]] != field {
				continue
			}
			// Gemini names a nested field with "at 'path'"; only an
			// absent or empty path identifies a top-level request field.
			tail := strings.ToLower(strings.TrimSpace(v[match[1]:]))
			if strings.HasPrefix(tail, "at ") && !strings.HasPrefix(tail, "at '':") && !strings.HasPrefix(tail, "at \"\":") {
				continue
			}
			return true
		}
		for _, match := range unquotedUnsupportedOptionalField.FindAllStringSubmatch(v, -1) {
			if match[1] == field {
				return true
			}
		}
		for _, match := range rejectedOptionalField.FindAllStringSubmatch(v, -1) {
			if match[1] == field {
				return true
			}
		}
	case []any:
		for _, item := range v {
			if unsupportedOptionalField(item, field) {
				return true
			}
		}
	case map[string]any:
		if v["type"] == "extra_forbidden" {
			loc, _ := v["loc"].([]any)
			return len(loc) == 1 && loc[0] == field || len(loc) == 2 && loc[0] == "body" && loc[1] == field
		}
		// A located validation error concerns the value, not support for
		// the top-level field. Never inspect its input echo.
		if _, located := v["loc"]; located {
			return false
		}
		if v["code"] == "unsupported_value" || v["type"] == "unsupported_value" {
			return false
		}
		param, _ := v["param"].(string)
		if param != "" && param != field {
			return false
		}
		if v["code"] == "unsupported_parameter" && param == field {
			return true
		}
		for _, key := range []string{"error", "message", "detail", "details", "errors", "description", "metadata", "raw"} {
			if unsupportedOptionalField(v[key], field) {
				return true
			}
		}
	}
	return false
}

func hasOptionalErrorObject(fault any) bool {
	switch v := fault.(type) {
	case map[string]any:
		return true
	case []any:
		for _, item := range v {
			if hasOptionalErrorObject(item) {
				return true
			}
		}
	}
	return false
}

// withoutRefused leaves out of body the optional fields the provider has
// refused on proto before.
func (s *Server) withoutRefused(providerID string, proto provider.Protocol, body []byte) []byte {
	var drop []string
	for _, f := range optionalFields {
		if !s.fits(providerID, f, proto) {
			drop = append(drop, f)
		}
	}
	if len(drop) > 0 {
		body = withoutFields(body, drop...)
	}
	if proto != provider.Chat {
		return body
	}
	var ms []string
	for _, f := range messageReasoning {
		if !s.fits(providerID, messageField(f), proto) {
			ms = append(ms, f)
		}
	}
	return withoutMessageFields(body, ms...)
}

// messageField is the name a field of a request's messages is remembered
// under in unfit once a provider has refused it.
func messageField(f string) string { return "messages[]." + f }

// refusedInMessages lists the thinking fields (messageReasoning) an
// upstream turned a Chat request away for where its messages carry them:
// extra_forbidden located in the messages, as Mistral's loc ["body",
// "messages",2,"assistant","reasoning_content"] (#494).
func refusedInMessages(status int, msg, body []byte) []string {
	if !badRequest(status) {
		return nil
	}
	var fault any
	if i := bytes.IndexByte(msg, '{'); i < 0 || json.Unmarshal(msg[i:], &fault) != nil {
		return nil
	}
	var out []string
	var walk func(v any)
	walk = func(v any) {
		switch v := v.(type) {
		case []any:
			for _, x := range v {
				walk(x)
			}
		case map[string]any:
			if loc, _ := v["loc"].([]any); v["type"] == "extra_forbidden" && len(loc) >= 2 {
				if loc[0] == "body" {
					loc = loc[1:]
				}
				f, _ := loc[len(loc)-1].(string)
				if loc[0] == "messages" && slices.Contains(messageReasoning, f) && !slices.Contains(out, f) &&
					bytes.Contains(body, []byte(`"`+f+`"`)) {
					out = append(out, f)
				}
				return
			}
			for _, x := range v {
				walk(x)
			}
		}
	}
	walk(fault)
	return out
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
		// Copilot's /v1/messages, for a model it serves on other APIs
		// alone (GPT-4.1, Gemini, GPT-5.6: #754); a model it doesn't
		// serve at all is model_not_supported there
		"no model endpoints available given user constraints",
		"is not supported for format",    // OpenCode: "Model grok-4.7 is not supported for format anthropic"
		"does not support this protocol", // OpenCode, with a key: "Model does not support this protocol"
	} {
		if strings.Contains(msg, phrase) {
			return true
		}
	}
	return false
}

// wrongAPINote goes after an upstream's word that the model isn't served
// on the API it was asked on (wrongEndpoint), when no other API is left to
// ask: the URL asked, and the APIs the vendor's list serves the model on
// that the provider has no URL for, so "Model does not support this
// protocol" says which protocol, and what to set (#1215).
func wrongAPINote(p provider.Provider, model string, res *http.Response) string {
	note := ""
	if res != nil && res.Request != nil && res.Request.URL != nil {
		u := *res.Request.URL
		u.RawQuery, u.User = "", nil
		note = " (asked at " + u.String()
	}
	paths := map[provider.Protocol]string{provider.Chat: "/v1/chat/completions", provider.Responses: "/v1/responses", provider.Anthropic: "/v1/messages"}
	var missing []string
	for _, a := range p.APIs(model) {
		if paths[a] != "" && p.Base(a) == "" {
			missing = append(missing, paths[a])
		}
	}
	if len(missing) > 0 {
		if note == "" {
			note = " ("
		} else {
			note += "; "
		}
		note += model + " is served on " + strings.Join(missing, " or ") + ", which " + p.Name + " has no URL for"
	}
	if note != "" {
		note += ")"
	}
	return note
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
	if from == provider.Anthropic {
		// Claude Code's auto mode classifier, on a model that reasons
		// whatever it is told (#250)
		fitAutoModeClassifier(p, model, request)
	}
	var zen *zenReply
	if p.OpenCodeFree(model) {
		zen = &zenReply{z: zenFreeTools(request)}
	}
	if request.WebSearch {
		// an API on which the provider searches by itself comes first;
		// without one, its model is given magpie's search. A searcher
		// asked for another model goes there too: on its first API
		// (DeepSeek's Chat) it had no search tool and answered from
		// nothing (#669)
		for _, t := range s.usable(p, model) {
			if searchesFor(p, t, model, request) {
				to = t
				break
			}
		}
		if !searching(r.Context()) && canSearchFor(p) && !searchesFor(p, to, model, request) {
			ask := s.askTranslated(p, to, model, r.Header, w.Header())
			if zen != nil {
				ask = zenRound(zen.z, ask)
			}
			// a Kimi Code plan's own searches stay on it
			r = r.WithContext(searchingOn(r.Context(), p))
			return s.searchReply(w, r, from, p.Name, request, u, ask)
		}
	}
	stream := request.Stream
	request.Stream = true
	if stream {
		return s.streamTranslated(w, r, p, from, to, request, model, zen, u)
	}
	res, actual, err := s.forwardTranslated(r.Context(), p, to, request, model, r.Header)
	if err != nil {
		return writeError(w, from, 502, p.Name+": "+err.Error()), err.Error()
	}
	defer res.Body.Close()
	u.RequestID = requestID(res.Header)
	if res.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		msg := p.Explain(p.Name+": "+provider.APIError(b, res.Status), res.StatusCode, b)
		if wrongEndpoint(res.StatusCode, b) {
			msg += wrongAPINote(p, model, res)
		}
		if res.StatusCode == http.StatusTooManyRequests && accountAgent(p) == "antigravity" && antigravityTurnsAway(request.System) {
			msg += " — " + antigravityTurnedAwayHint
			markAntigravityTurnsAway(w)
		}
		if p.Preset == "openrouter" && openRouterSharedPool(b) {
			markOpenRouterSharedPool(w)
		}
		keepRetry(w.Header(), res.Header, b)
		u.ErrType = provider.ErrorType(b)
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
	// a Gemini reply that says nothing is a failure, not a turn's end
	// (#667)
	empty := emptyFails(actual)
	// calls a model wrote into its text are made calls (textcalls.go)
	written := func(see func(Event)) (func(Event), func()) {
		names := textCallNames(request.Tools)
		if names == nil {
			return see, func() {}
		}
		t := &textCallSee{names: names, see: see}
		return t.event, t.release
	}
	var col collector
	see, held := written(zenSee(zen, col.add))
	err = readSSE(rd, func(_, data string) error {
		u.add(Usage{Upstream: upstreamOf([]byte(data))})
		return dec(data, see)
	})
	held()
	if err != nil {
		// a partial answer is not an answer
		msg := p.Name + ": " + err.Error()
		return writeError(w, from, 502, msg), msg
	}
	if col.err != "" && !saidAnything(col.res.Parts) {
		markAntigravityRefused(w, p, request.System, col.err)
		return writeError(w, from, 502, p.Name+": "+col.err), col.err
	}
	if empty && !saidAnything(col.res.Parts) && answersNothing(col.res.Stop) {
		msg := p.Name + ": " + emptyReply
		return writeError(w, from, 502, msg), msg
	}
	if zen != nil {
		zen.end(col.add)
	}
	res2 := col.finish()
	u.add(res2.Usage)
	u.add(Usage{Served: res2.Model})
	out := renderUsage(from, res2, request, u)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	w.Write(out)
	return 200, col.err
}

// emptyReply is what a reply that says nothing fails with.
const emptyReply = "an empty reply (the model answered nothing; try again)"

// emptyFails tells whether a reply in proto that says nothing is a
// failure. Gemini's, Antigravity's among them, now and then ends with
// nothing, or only reasoning, and a STOP — or with no finishReason at all
// — which went to the agent as a turn ended as it should, content [] and
// no usage, and the agent's run stopped mid-task (#667). Gemini CLI takes
// such a stream for an invalid one and asks again. Claude ending a turn
// with nothing after a tool's result is its answer, so no other
// protocol's is.
func emptyFails(proto provider.Protocol) bool {
	return proto == provider.CodeAssist || proto == provider.Gemini
}

// answersNothing tells whether a reply with nothing said ended for no
// reason of its own: not cut at its length (all reasoning), nor by a
// safety filter (told as a refusal, #248).
func answersNothing(stop string) bool {
	return stop != "length" && stop != "filter"
}

// saysSomething tells whether an event is any of a reply an agent shows or
// acts on: text, a call, a search, an image.
func saysSomething(ev Event) bool {
	switch ev.Kind {
	case KText:
		return ev.Text != ""
	case KToolStart, KSearch, KImage:
		return true
	}
	return false
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
	case provider.Gemini:
		// Factory's generateContent. No other provider speaks Gemini upstream.
		return "/generate"
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
	// a call's id may carry Gemini's thought signature (gemini_signature.go)
	unsignCalls(req)
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

func build(proto provider.Protocol, r *Request, model, host string, rejectTemp bool) ([]byte, error) {
	switch proto {
	case provider.Chat:
		return buildChat(r, model, host, rejectTemp), nil
	case provider.Responses:
		return buildResponses(r, model, host, rejectTemp), nil
	case provider.Gemini:
		return buildGemini(r, model)
	}
	out := buildAnthropic(r, model)
	if r.Fast && host == "api.anthropic.com" && provider.ClaudeFast(model) {
		// Claude's fast mode, on the models that have it (its beta header
		// goes with it: forwardOnce)
		out = withFields(out, map[string]any{"speed": "fast"})
	}
	return out, nil
}

func decoder(proto provider.Protocol) func(data string, emit func(Event)) error {
	switch proto {
	case provider.Chat:
		d := &chatDecoder{}
		return d.decode
	case provider.Responses:
		d := &responsesDecoder{}
		return d.decode
	case provider.CodeAssist, provider.Gemini:
		// Factory's generateContent is the same Gemini chunks, without
		// Code Assist's {response} wrapper, which the decoder also reads.
		d := &codeAssistDecoder{}
		return d.decode
	}
	d := &anthropicDecoder{server: map[int]bool{}, whole: map[int]bool{}}
	return d.decode
}

type streamEncoder interface {
	event(Event)
	finish()
	// keepalive tells the client the reply goes on, as its protocol does
	// with no answer to give: one its idle timeout counts (#436)
	keepalive()
}

// keepaliveGap is how long a translated reply's client may hear nothing
// while its provider is heard from (a keepalive, an event that has nothing
// for the client) before it is sent a keepalive of its own.
var keepaliveGap = time.Second

// keepaliveEvery is how often a relayed reply that has gone quiet is kept
// alive, and keepaliveLongest how long it is kept so with no event: a
// reply stuck longer is left for the client's own idle timeout to end.
var keepaliveEvery, keepaliveLongest = 15 * time.Second, 5 * time.Minute

// relayEvents hands see each of events until they end or see says stop,
// keeping the client of sw alive while none comes (keepaliveEvery, for up
// to keepaliveLongest since the last).
func relayEvents(events <-chan Event, sw *sseWriter, enc streamEncoder, see func(Event) bool) {
	tick := time.NewTicker(keepaliveEvery)
	defer tick.Stop()
	last := time.Now()
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return
			}
			last = time.Now()
			if !see(ev) {
				return
			}
		case <-tick.C:
			if time.Since(last) < keepaliveLongest && sw.quiet() >= keepaliveEvery/2 {
				enc.keepalive()
			}
		}
	}
}

func encoder(proto provider.Protocol, w *sseWriter, r *Request, usage ...*Usage) streamEncoder {
	enc := makeEncoder(proto, w, r)
	if len(usage) > 0 && usage[0] != nil {
		return &usageEncoder{streamEncoder: enc, usage: usage[0]}
	}
	return enc
}

func makeEncoder(proto provider.Protocol, w *sseWriter, r *Request) streamEncoder {
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

// AccountHeader pins a request to one of a subscription's accounts, by its
// user (an email, a login) or its id in the routing trace: only it is
// tried, and when it can't take the request the caller is told why rather
// than another account answering — a probe of one account needs that one.
// It goes no further than magpie.
const AccountHeader = "X-Magpie-Account"

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

// nativeSessionOf keeps the client session even when magpie's header overrides it.
func nativeSessionOf(in http.Header) string {
	for _, h := range sessionHeaders {
		if v := strings.TrimSpace(in.Get(h)); v != "" {
			return v[:min(len(v), 128)]
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

// offEffort is an effort turning reasoning off, or as near off as asked.
func offEffort(e string) bool { return e == "none" || e == "minimal" }

// onEffort is the lowest level offered with reasoning on, or low when none
// are known. A catalog's minimal must not be picked again after off was
// refused; the catalog itself stays as it was.
func onEffort(p provider.Provider, model string) string {
	levels := slices.DeleteFunc(slices.Clone(p.Efforts(model)), offEffort)
	return fitEffort("low", levels)
}

// geminiOffLevel is the level Gemini's OpenAI-compatible API is asked to
// think at for model with reasoning turned off: minimal, its least, where
// its levels have it or aren't known; its lowest where they haven't, or
// once it turned minimal away (fits false). Gemini 3 turns away a level it
// doesn't have, gemini-3.8-flash minimal with a 400.
func geminiOffLevel(p provider.Provider, model string, fits bool) string {
	if !fits {
		return onEffort(p, model)
	}
	return fitEffort("minimal", p.Efforts(model))
}

// offRefused is how unfit remembers a provider refusing reasoning turned
// off for model.
func offRefused(model string) string { return "reasoning off\x00" + model }

// forcedRefused is how unfit remembers a provider refusing a tool_choice
// that forces a call (a named tool, or required) for model.
func forcedRefused(model string) string { return "tool forced\x00" + model }

// forcesTool reports whether a tool_choice forces a call.
func forcesTool(choice string) bool {
	return choice == "required" || strings.HasPrefix(choice, "name:")
}

// toolChoiceRefused is a model turning a forced tool_choice away. Claude
// Opus 5.5, Sonnet 5.5 and Fable 5.1 answer any {type:"tool"} or
// {type:"any"} with `tool_choice: type "tool" and "any" are not supported
// for this model.`, on Anthropic's API and OpenRouter alike, whatever the
// schema; and a vendor that holds the forced tool to strict mode instead
// answers `tools.N.custom: For 'object' type, 'additionalProperties' must
// be explicitly set to false` (#668), which Anthropic says only of a
// strict tool, and magpie sends none.
var toolChoiceRefused = regexp.MustCompile(`(?i)tool_choice\W+type\W+tool\W+and\W+any\W+are not supported|tools\.\d+\.custom\W+for\W+object\W+type\W+additionalProperties\W+must be explicitly set to false`)

// unforced is req with its forced tool_choice left to the model: a named
// tool is the only one offered, as the Claude subscription's bridge does,
// and required becomes auto.
func unforced(req *Request) *Request {
	r := *req
	if name, ok := strings.CutPrefix(r.ToolChoice, "name:"); ok {
		r.Tools = slices.DeleteFunc(slices.Clone(r.Tools), func(t Tool) bool { return t.Name != name })
	}
	r.ToolChoice = "auto"
	return &r
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

// withoutLoneToolChoice leaves out a tool_choice sent with no tools; with
// none to choose from it says nothing.
func withoutLoneToolChoice(body []byte) []byte {
	if !bytes.Contains(body, []byte(`"tool_choice"`)) {
		return body
	}
	var q struct {
		Tools []json.RawMessage `json:"tools"`
	}
	if json.Unmarshal(body, &q) != nil || len(q.Tools) > 0 {
		return body
	}
	return withoutFields(body, "tool_choice")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false) // a vendor's <, > and & as it wrote them (#260)
	enc.Encode(v)
}

// failTo answers the agent with an error: as writeError does, or as its
// stream's error once a held stream sent it the stream's headers to keep
// it alive (keepAlive), where a status can no longer be said.
func failTo(w http.ResponseWriter, a *keptAlive, proto provider.Protocol, status int, msg string) int {
	if a == nil || !a.sent {
		return writeError(w, proto, status, msg)
	}
	streamError(w, proto, status, msg)
	return status
}

// streamError writes an error as an event of the client's stream, its
// headers already sent.
func streamError(w http.ResponseWriter, proto provider.Protocol, status int, msg string) {
	sw := newSSEWriter(w)
	sw.begun = true
	ev := Event{Kind: KError, Text: msg, Status: status}
	if tooLong(status, msg) {
		ev.Code = "context_length_exceeded"
	}
	makeEncoder(proto, sw, &Request{}).event(ev)
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
	case status == 413 && proto == provider.Anthropic:
		typ = "request_too_large"
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
	if h, held := w.(*holdWriter); status == http.StatusRequestEntityTooLarge && (!held || !h.hold) {
		const hint = "This is a request-body byte limit; reduce images or large tool results, or start a new conversation with a short summary."
		if !strings.Contains(msg, hint) {
			msg += " — " + hint
		}
	}
	var v any
	switch proto {
	case provider.Anthropic:
		v = map[string]any{"type": "error", "error": map[string]any{"type": typ, "message": msg}}
	case provider.Gemini:
		st := map[int]string{400: "INVALID_ARGUMENT", 408: "DEADLINE_EXCEEDED", 413: "INVALID_ARGUMENT", 401: "UNAUTHENTICATED", 403: "PERMISSION_DENIED", 404: "NOT_FOUND",
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
// context length", "context window"… and the second line, as pi
// (packages/ai/src/utils/overflow.ts) collected them: Gemini's
// "input token count … exceeds the maximum",
// xAI's "maximum prompt length is", Groq's "reduce the length of the
// messages", OpenRouter's "maximum allowed input length", Together's
// "longer than the model's context length", llama.cpp's "available
// context size", LM Studio's "greater than the context length", Copilot's
// "prompt token count of", MiniMax's "context window exceeds limit",
// Kimi's "exceeded model token limit", DS4's "configured context size",
// z.ai's "Prompt too long" / "Prompt exceeds max length" and
// model_context_window_exceeded, Ollama's "exceeded max context length",
// DashScope's "Range of input length should be", "token limit exceeded".
var tooLongRe = regexp.MustCompile(`(?i)context_length_exceeded|prompt is too long|input is too long|(exceeds?|exceeded|over|beyond)( the)?( model'?s?)?( maximum)? context|context (length|limit|window) (exceeded|is exceeded)|maximum context length|too many (input |prompt )?tokens|上下文(长度)?(超|过长)|超(过|出)(了)?(模型)?(的)?(最大)?上下文` +
	`|input token count.*exceeds the maximum|maximum prompt length is \d|reduce the length of the messages|maximum allowed input length|longer than the model'?s context length|available context size|greater than the context length|prompt token count of [\d,]+ exceeds the limit|context window exceeds limit|exceeded model token limit|configured context size|prompt too long|prompt exceeds max length|context_window_exceeded|exceeded (max |maximum )?context length|range of input length should be|token limit exceeded`)

// overflowed says a refused request was too long for the model — a
// request error (400, 413, 422) whose words say so.
func overflowed(status int, body []byte) bool {
	return (status == 400 || status == 413 || status == 422) && tooLong(status, string(body))
}

// tooLong is whether a vendor's error says the conversation no longer
// fits. One about max_tokens is left alone: the reply's allowance, not
// the conversation, is what is too big there, and compacting won't help.
// Nor is a rate limit, however it counts ("Too many tokens, please wait",
// Bedrock's; "tokens per minute"): told the prompt is too long, Claude Code
// compacts, and again after the next one, until it gives up as thrashing.
func tooLong(status int, msg string) bool {
	if status < 400 || status >= 500 || status == http.StatusTooManyRequests {
		return false
	}
	m := strings.ToLower(msg)
	if strings.Contains(m, "max_tokens") || strings.Contains(m, "max_output_tokens") || strings.Contains(m, "max_completion_tokens") {
		return false
	}
	if paceWords.MatchString(msg) {
		return false
	}
	return tooLongRe.MatchString(msg)
}

// paceWords say a limit on how fast or how much, not on one prompt's size.
var paceWords = regexp.MustCompile(`(?i)rate.?limit|per (minute|hour|day)|\bTP[MD]\b|please wait|try again later|throttl`)
