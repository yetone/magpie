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
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/awake"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/codexrelay"
	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/gwbudget"
	"github.com/yetone/magpie/internal/gwmw"
	"github.com/yetone/magpie/internal/gwtool"
	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/pluginrt"
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
	if id := pathAgent(r); id != "" {
		return id
	}
	if id, ok := strings.CutPrefix(callerKey(r), Token+"-"); ok && id != "" {
		return usage.AgentOf(id)
	}
	if isClaudeDesktop(r) {
		return "claude-desktop"
	}
	if isClaudeScience(r) {
		// Claude Science's client, an Anthropic SDK's User-Agent
		return ScienceID
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
	if codexForClaudeCode(r.Header) {
		return "codex"
	}
	return usage.AgentOf(ua)
}

// codexForClaudeCode is a Codex turn that Claude Code started under its
// own name. OpenAI's Codex plugin for Claude Code (/codex:review,
// /codex:rescue) runs `codex app-server` with clientInfo named "Claude
// Code", and Codex makes that its originator and User-Agent ("Claude
// Code/0.162.1 (Mac OS 26.2.0; arm64) … (Claude Code; 1.0.6)"), so the
// turn read as Claude Code's, and its calls were counted under Claude
// Code, never Codex (wyztony on X). Claude Code's own requests say
// claude-cli/ and never carry the headers Codex puts on each turn
// (x-codex-window-id, x-codex-turn-metadata), so the name with those is
// Codex's. Only Claude Code's name is taken back this way: an app that
// runs Codex as its own engine and names it after itself (Lody) is
// counted as that app.
func codexForClaudeCode(h http.Header) bool {
	if h.Get("X-Codex-Window-Id") == "" && h.Get("X-Codex-Turn-Metadata") == "" {
		return false
	}
	for _, v := range []string{h.Get("User-Agent"), h.Get("Originator")} {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(v)), "claude code") {
			return true
		}
	}
	return false
}

// startedByAgent says whether a request is from no agent magpie knows but
// from something an agent started with its environment (a hook's script): it
// came with the plain gateway token, not one magpie gave an agent, and
// its User-Agent names no agent.
func startedByAgent(r *http.Request, agent string) bool {
	if pathAgent(r) != "" {
		return false
	}
	if _, ok := strings.CutPrefix(callerKey(r), Token+"-"); ok {
		return false
	}
	return agent != "claude-desktop" && agent != "cursor-local" && agent != ScienceID && !usage.IsAgent(agent)
}

// StandIn is the model an agent is set to use in place of one it named that
// magpie doesn't serve it: Claude Code asks for claude-haiku-… by name for
// its small tasks, whatever its haiku tier is set to, and that goes to the
// tier's model rather than to some provider that happens to list the name.
// "" leaves the model as asked. Set by main.
var StandIn func(agent, model string) string

// SetToGroup reports whether magpie set one of agent's fields to the
// routing group gid ("group/<id>"): the user picked that found group for
// the agent, so a bare model's name it asks for by (#750) stays on the
// group rather than going to the agent's own subscription (ownFirst). Set
// by main; nil reads as no.
var SetToGroup func(agent, gid string) bool

// ownFirst is where a bare model's id agent asks for goes when the agent's
// own subscription in magpie serves it (provider.OwnRoute; freefcw on X:
// Codex's gpt-6-astra went to Devin through the group magpie found, and
// with found groups off to the first provider in the order). ok is false
// for a key held to some models, whose own rules pick, and for a found
// group magpie set the agent to.
func ownFirst(r *http.Request, agent, m string) (string, bool) {
	if _, held := keyHolds(r); held {
		return "", false
	}
	to, group, ok := provider.OwnRoute(agent, m)
	if !ok {
		return "", false
	}
	if group != "" && SetToGroup != nil && SetToGroup(agent, group) {
		return "", false
	}
	return to, true
}

// offSpreadRefused is the error for a bare model's id several providers
// serve, none the agent's own, while found groups are off (provider.OffSpread).
func offSpreadRefused(model string, ids []string) string {
	return fmt.Sprintf("%q is served by %s in Magpie and auto groups are off, so Magpie picks none of them: ask for one by its provider/model id, or turn auto groups on (magpie group auto on)", model, strings.Join(ids, ", "))
}

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
	load := sync.OnceValue(func() map[string]string { return settings.Shared().Wires() })
	return r.WithContext(context.WithValue(r.Context(), wiresKey{}, load))
}

// wiresOf is the names in force for ctx: the ones withWires read, or the
// ones in force now for a request magpie made for itself and never carried
// them on.
func wiresOf(ctx context.Context) map[string]string {
	if load, ok := ctx.Value(wiresKey{}).(func() map[string]string); ok {
		return load()
	}
	return settings.Shared().Wires()
}

// The reasons serve gives a model move of its own (ModelMove.Why);
// Claude Desktop's are desktopMove's.
const (
	movedClassifier  = "classifier"   // Claude Code's auto mode classifier: the model /model moved its tier to
	movedClaudeTier  = "claude-tier"  // Claude Code's full id of a Claude model: the model set for its tier
	movedStandIn     = "stand-in"     // a model magpie doesn't serve: the one the agent's config names
	movedCodexTurn   = "codex-turn"   // Codex's memory or auto-review call: its last turn's model
	movedCodexFollow = "codex-follow" // Codex's memory call on a bare default of its own magpie serves, nothing set for it: the conversation's model
	movedPrivacy     = "privacy"      // a request masking would change: Settings › Privacy's model for it
)

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
var claudeFamily = lazyre.New(`^claude-(?:[0-9][0-9.-]*-)?(?:opus|sonnet|haiku|fable)(?:[-.@:]|$)`)

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
//
// A client magpie doesn't know as an agent (Python-urllib, an Anthropic SDK)
// naming such an id that magpie doesn't serve is taken for one Claude Code
// started: a hook, statusline or plugin script inherits Claude Code's
// ANTHROPIC_BASE_URL and token, and asks for claude-sonnet-4-6 by name,
// which went nowhere, a 404 (tbxark on X). It gets Claude Code's tier too.
// An id the user does serve (a Claude account, an Anthropic key) goes
// where it is served, and an agent magpie knows keeps its own models.
func claudeTierStandIn(r *http.Request, agent, asked string) string {
	if StandIn == nil || !claudeFamily.MatchString(strings.ToLower(strings.TrimSuffix(asked, "[1m]"))) {
		return ""
	}
	if agent != "claude" {
		if !startedByAgent(r, agent) || !unserved(asked) {
			return ""
		}
		agent = "claude"
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
	if asked == "" || provider.IsGroupRef(asked) {
		return false
	}
	if _, ok := provider.GroupFor(asked); ok {
		return false
	}
	id := strings.TrimSuffix(asked, "[1m]")
	if catalogNames.get(struct{}{}, namesInCatalog)[id] {
		return false
	}
	if pid, _, ok := strings.Cut(id, "/"); ok {
		if p, err := provider.Find(pid); err == nil && p.On() {
			return false
		}
	}
	return true
}

// catalogNames is every catalog entry's id and model id, kept by
// provider.StampNow. unserved asks it on every request an agent sends
// (standIn), and each one built the whole catalog to look: 2.26 GB of the
// 9.8 GB a sandbox gateway allocated in 10 minutes of ~1,000 requests
// (procmem heap profile), with nothing changed between them.
var catalogNames stamped[struct{}, map[string]bool]

func namesInCatalog() map[string]bool {
	cat := provider.Catalog()
	out := make(map[string]bool, 2*len(cat))
	for i := range cat {
		out[cat[i].ID], out[cat[i].Model] = true, true
	}
	return out
}

// switchedOff says a request for model went nowhere because p, which
// serves it, is switched off in magpie (provider.Provider.Off), or kept to
// Claude Code's own sign-in (provider.Provider.KeptToAgent), or a Remote
// magpie that is this one (provider.Provider.SelfLeftOut).
func switchedOff(p provider.Provider, model string) string {
	if p.SelfLeftOut() && !p.Off {
		return fmt.Sprintf("%s is a Remote magpie that is this magpie itself, so %q is not served through it; ask for the model without %q in front, or tick \"Use it anyway\" on it in Magpie's Providers", p.Name, model, p.ID+"/")
	}
	if p.KeptToAgent() {
		return fmt.Sprintf("%s's accounts are kept to Claude Code's own sign-in in Magpie, so %q is not served; untick \"Only switch Claude Code's sign-in\" under its accounts in Magpie's Providers to use it", p.Name, model)
	}
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

// Serves says how this process was started (app, tray, web, serve or tui):
// the Routing page of another magpie on this computer, which shows this
// one's routing, names it (#2606). Set by the cli.
var Serves = "app"

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
	Pid     int    // its process id; 0 from one that doesn't say
	Serves  string // how it was started: app, tray, web, serve or tui; "" from one that doesn't say
}

// ServedBy asks the address which magpie answers there. Another magpie can
// keep the port after this one is updated (a magpie serve left running, a
// second copy, one that didn't quit), and every agent's request is then
// still that one's to send: in #506 a Factory 403 came worded as v0.1.550
// and earlier word it to two people whose magpie said v0.1.630, so none of
// the fixes since reached their requests. Its version tells it apart.
func ServedBy() Served { return ServedAt(URL()) }

// servedAt is ServedBy for the magpie at base.
func ServedAt(base string) Served {
	c := &http.Client{Timeout: 700 * time.Millisecond}
	res, err := c.Get(base + "/")
	if err != nil {
		return Served{}
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	var info struct {
		Name    string `json:"name"`
		Version string `json:"version"`
		Window  bool   `json:"window"`
		Pid     int    `json:"pid"`
		Serves  string `json:"serves"`
	}
	if decodeJSON(b, &info) != nil || info.Name != "magpie" {
		return Served{Running: bytes.Contains(b, []byte(`"magpie"`)), Window: bytes.Contains(b, []byte(`"window":true`))}
	}
	return Served{Running: true, Window: info.Window, Version: info.Version, Pid: info.Pid, Serves: info.Serves}
}

// ServedBusy is what the magpie serving the gateway has in flight, as its
// /v1/magpie/concurrency tells it: agent requests (those waiting for a
// slot among them) and Claude Code turns waiting on their caller's tools.
// ok is false when it doesn't say (a magpie from before 3b6ca0557, a
// refusal, no answer in time): unknown, which is never idle.
func ServedBusy() (n int, ok bool) {
	c := &http.Client{Timeout: 700 * time.Millisecond}
	res, err := c.Get(URL() + "/v1/magpie/concurrency")
	if err != nil {
		return 0, false
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return 0, false
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return 0, false
	}
	var out struct {
		Object   string `json:"object"`
		Requests *int   `json:"requests"`
		Waiting  *int   `json:"waiting"`
		Tools    *int   `json:"tools"`
	}
	if decodeJSON(b, &out) != nil || out.Object != "concurrency" || out.Requests == nil || out.Tools == nil {
		return 0, false
	}
	n = *out.Requests + *out.Tools
	if out.Waiting != nil && *out.Waiting > *out.Requests {
		n += *out.Waiting - *out.Requests
	}
	return n, true
}

// Taken reports whether anything accepts connections at the gateway's
// address, whether or not it answers ServedBy's question in time: a
// magpie busy starting, or slow on a small server, still holds the port.
func Taken() bool {
	c, err := net.DialTimeout("tcp", strings.TrimPrefix(URL(), "http://"), 500*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// Call is one request the gateway handled, for the status views.
type Call struct {
	// ID numbers the call in the Recent-calls ring, from 1, for the page
	// to ask for its bodies by (the ring's summary leaves them out, #1521)
	ID uint64 `json:"id,omitempty"`
	// Route: the Routing page's request (Route.ID) this call is, for it
	// to ask for the call's bodies by while the ring still has them
	Route int64     `json:"route,omitempty"`
	Time  time.Time `json:"time"`
	Agent string    `json:"agent"`         // who called, from the client's User-Agent
	Via   string    `json:"via,omitempty"` // the computer a remote magpie's request came from (AgentHeader)
	// Kind: what the agent made the call for, when it isn't its turn —
	// a Codex subagent's (callKind) — "" for a turn
	Kind string `json:"kind,omitempty"`
	// For: the request a call of magpie's own (a web search) was made for
	For *CallFor `json:"for,omitempty"`
	// Masked: what masking swapped in it, nil for nothing
	Masked   *Masked           `json:"masked,omitempty"`
	Model    string            `json:"model"`
	Provider string            `json:"provider"`
	From     provider.Protocol `json:"from"`
	To       provider.Protocol `json:"to"`
	Status   int               `json:"status"`
	Millis   int64             `json:"ms"`
	// TTFT: ms from the request to its reply's first content — text,
	// reasoning or a tool call — and FirstText to its first text, when
	// it was streamed (#196)
	TTFT      int64 `json:"ttft,omitempty"`
	FirstText int64 `json:"firstText,omitempty"`
	// FirstReasoning is the start of paced visible reasoning, absent for hidden reasoning.
	FirstReasoning  int64 `json:"firstReasoning,omitempty"`
	ReasoningHidden bool  `json:"reasoningHidden,omitempty"`
	// Flow: the ms its content took to come, from where its speed is
	// counted (firstToken.flowMs): next to none for a reply that came in
	// a burst, so it tells no speed
	Flow  int64  `json:"flow,omitempty"`
	Error string `json:"error,omitempty"`
	// Canceled: the agent canceled the call before it was answered
	// (usage.CanceledBy), which the page shows as no failure
	Canceled          bool   `json:"canceled,omitempty"`
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
	// headers: its headers both ways, as they went (wire_headers.go),
	// read masked by Headers
	headers *headerLog
	// wholeIn/wholeOut are the whole bodies for an observer that asks for
	// them uncut (the OTLP export, #538): nil leaves it to the 256 KiB
	// bodies above, which is all the Recent-calls page keeps
	wholeIn, wholeOut       []byte
	wholeInCut, wholeOutCut bool
	// maskIn: the request went as written (unredactedRoute) with masking
	// on, so an observer is handed its body masked here, not as it went
	maskIn *laterMasked
}

// Server is the gateway.
type Server struct {
	client *http.Client
	// ending closes once the gateway stops serving, for a stream with no
	// end of its own (routeEvents) to end before Shutdown, which would
	// wait its whole drain for it (streamsEnding)
	ending     chan struct{}
	endingOnce sync.Once
	endOnce    sync.Once

	mu     sync.Mutex
	recent []Call
	calls  uint64 // the last Call.ID given
	// unfit remembers the endpoints each provider's models were turned away
	// from, so every later turn goes straight to one that takes them.
	unfit map[string]bool
	// refusedThinking is the thinking blocks each provider and account
	// refused as signed for another conversation (thinking_bound.go)
	refusedThinking map[string]bool
	subscription    *subscriptionBridge
	// imageStore is the image MCP server's jobs and images (image_mcp.go)
	imageStore imageMCPStore
	// browser is the Magpie Browser extensions connected (browser.go)
	browser      browserRelay
	debug        bool
	trace        trace // what routing did with each request, for the Gateway view
	titlePrompts titlePrompts
	// compactFails are the summaries each Codex conversation failed to
	// get in a row, for a compaction that keeps failing to stop being
	// asked (codex_compact_guard.go, #2274)
	compactFails compactFails
	// bodyCaps are the byte caps providers refused a request over
	// (body_cap.go)
	bodyCaps bodyCaps
	// codexTurns is the model each Codex last had a turn of its own
	// answered on, by caller (codexMemoryStandIn)
	codexTurns sync.Map
	// held is the most prompt tokens each provider's model answered
	// (heldOf), by provider id and model
	held sync.Map
	// codexLeads is the model each Codex conversation's own turns last
	// went on, for its side calls to follow (codexFollowTo)
	codexLeads codexLeads
	// codexSpawns is the model each Codex subagent thread was spawned on,
	// which a resumed one goes back on (codexResumeTo)
	codexSpawns codexSpawns
	// the listener, swapped when the gateway is shared on the network or
	// taken off it (see Relisten)
	lnMu sync.Mutex
	ln   net.Listener
	// front is Front's handler, built once
	frontOnce sync.Once
	front     http.Handler
	// handedOff: another magpie serves the port now, and this one's end
	// waits for its requests in flight to finish (handover.go)
	handedOff atomic.Bool
	// notAccepting: this one let its listener go, for the magpie serving
	// the port after it to take every connection (stopAccepting); its
	// Serve ending then waits for the drain
	notAccepting atomic.Bool
	// the connections taken whose first request isn't read yet, which a
	// handover's end waits for (handover.go)
	unread unreadConns
	// sockets are the Responses WebSockets being served (responses_ws.go)
	sockets clientSockets
	// what it serves other computers TLS with, nil without (tls.go)
	tls *tlsServer
	// the images described for models that can't see them (vision.go);
	// completed inline descriptions also survive a restart (vision_cache.go)
	sightMu        sync.Mutex
	sights         map[string]*sight
	sightOrder     []string
	sightFile      string
	sightDiskLines int
	// inline descriptions that may still have to save, from their creation
	// until see has saved or given up; a shutdown lets the count reach zero
	sightSaveMu    sync.Mutex
	sightSaving    int
	sightSavesIdle chan struct{} // closed while none may save
	// blind are the models that turned an image away while this server
	// ran, by "<provider>/<model>" (refuseSight): kept for the run alone,
	// so a vendor that adds sight is asked again after a restart
	blind sync.Map
	// the requests out at each key or account with a MaxConcurrency, and
	// those waiting their turn (concurrency.go)
	lanes lanes
	// the requests in flight of each gateway key with a MaxConcurrency,
	// and those waiting their turn (key_slots.go)
	keyLanes lanes
	// each account's one turn at a time near its usage cap (cap_near.go)
	nearCap nearCaps
	// the requests each key or account with a MaxRPM sent in the last
	// minute, and those about to go (rpm.go)
	rpms          rpms
	requestLimits requestLimits
	// what a restart would cut short (portbusy.go)
	busyCounters
	// every request's bytes up and down, as they go (Traffic)
	traffic traffic
	// when Antigravity last turned away the system prompt it refuses
	// (UnixNano, 0 for never): while that is recent, a group skips it for
	// such a request when someone is left after it (antigravityForeseen)
	agTurnedAway atomic.Int64
	// thoughtMu keeps the count of each member's thinking replies that
	// went on to an answer in a row, by "<provider>/<model>", for a
	// vendor that may refuse after its thinking (holdsThinking, #4512,
	// #4166)
	thoughtMu    sync.Mutex
	thoughtClean map[string]int
}

// New makes a gateway.
func New() *Server {
	redact.SetKeyPath(filepath.Join(settings.Dir(), "redact.key"))
	s := &Server{
		// a request counted against a MaxRPM waits for room in its minute
		// first (rpm.go); the ChatGPT backend is spoken to over HTTP/1.1
		// (netproxy.HTTP1, which provider/codex_http1.go sets); a vendor's
		// redirect is followed to its own origin only, so its key goes
		// nowhere else (provider.KeepsCredentials)
		client:       &http.Client{Transport: rpmTransport{upstreamTransport()}, CheckRedirect: provider.KeepsCredentials},
		unfit:        make(map[string]bool),
		subscription: newSubscriptionBridge(),
		debug:        os.Getenv("MAGPIE_DEBUG") != "",
		sightFile:    sightsPath(),
	}
	s.loadSights()
	return s
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

// RouteCall is the call the Routing page's request route was, while it is
// still in the ring.
func (s *Server) RouteCall(route int64) (Call, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.recent) - 1; i >= 0; i-- {
		if route != 0 && s.recent[i].Route == route {
			return s.recent[i], true
		}
	}
	return Call{}, false
}

// RecentCall is the call numbered id while it is still in the ring.
func (s *Server) RecentCall(id uint64) (Call, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.recent {
		if c.ID == id {
			return c, true
		}
	}
	return Call{}, false
}

func (s *Server) record(c Call) {
	// Whole bodies belong only to the export, never to the Recent-calls ring.
	// This is a copy: the serving call still needs them for withBodies.
	c.wholeIn, c.wholeOut = nil, nil
	c.Canceled = usage.CanceledBy(c.Status, c.Error)
	if c.wire != nil {
		c.Archive = c.wire.name
		archive(c)
		c.wire = nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	c.ID = s.calls
	s.recent = keepRecent(s.recent, c)
	if s.debug {
		log.Printf("%s %s → %s (%s→%s) %d %dms %s", c.Model, c.Provider, c.Provider, c.From, c.To, c.Status, c.Millis, c.Error)
	} else if line, ok := failedCallLine(c, time.Now()); ok {
		log.Print(line)
	}
}

// failedCallLine is the app.log line of a call that failed: its status,
// or a 200 whose reply was cut, canceled or failed on its stream. Every
// failure was the Routing page's and the usage ledger's alone, so a
// report that came with app.log had no trace of it (wjh66 on Discord:
// Codex's "error decoding response body" after "reconnecting 5/5", and
// nothing in the log about the turns). The same failure again within
// failedLogEvery is counted, not logged again, so a client asking in a
// loop can't fill the log.
func failedCallLine(c Call, now time.Time) (string, bool) {
	if c.Status < 400 && c.Error == "" {
		return "", false
	}
	said := c.Error
	if r := []rune(said); len(r) > 300 {
		said = string(r[:300]) + "…"
	}
	key := c.Agent + "|" + c.Provider + "|" + c.Model + "|" + strconv.Itoa(c.Status) + "|" + said
	failedLog.Lock()
	defer failedLog.Unlock()
	if failedLog.last == nil {
		failedLog.last = map[string]failedSeen{}
	}
	seen := failedLog.last[key]
	if !seen.at.IsZero() && now.Sub(seen.at) < failedLogEvery {
		seen.skipped++
		failedLog.last[key] = seen
		return "", false
	}
	if len(failedLog.last) >= 256 {
		for k, v := range failedLog.last {
			if now.Sub(v.at) >= failedLogEvery {
				delete(failedLog.last, k)
			}
		}
	}
	failedLog.last[key] = failedSeen{at: now}
	agent := cmp.Or(c.Agent, "a client")
	line := fmt.Sprintf("gateway: %s's call to %s on %s failed: %d after %s: %s",
		agent, cmp.Or(c.Model, "(no model)"), cmp.Or(c.Provider, "(none)"), c.Status,
		(time.Duration(c.Millis) * time.Millisecond).Round(time.Millisecond), cmp.Or(said, "(nothing said)"))
	if seen.skipped > 0 {
		line += fmt.Sprintf(" (and %d times more like it since the last line)", seen.skipped)
	}
	return line, true
}

// failedLogEvery is how often the same failure is logged at most.
const failedLogEvery = time.Minute

type failedSeen struct {
	at      time.Time
	skipped int
}

var failedLog struct {
	sync.Mutex
	last map[string]failedSeen
}

// recentMost is how many calls the Recent-calls ring keeps.
const recentMost = 40

// keepRecent appends c to the ring and drops the oldest past recentMost,
// in place. Re-slicing the oldest off (ring[1:]) left it in the backing
// array, bodies and all, until an append outgrew the array and copied the
// ring to a new one, about twice its size: up to ~80 calls of up to
// 2×256 KiB of bodies each were kept for a page that shows 40.
func keepRecent(ring []Call, c Call) []Call {
	if len(ring) < recentMost {
		return append(ring, c)
	}
	n := copy(ring, ring[len(ring)-recentMost+1:])
	clear(ring[n:])
	ring = ring[:n]
	return append(ring, c)
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
	ScopeOwnBrowserKey()
	cfg, err := serverTLS()
	if err != nil {
		return err
	}
	s.lnMu.Lock()
	s.tls = cfg
	s.lnMu.Unlock()
	// the Codex relay a magpie quitting left at the port (codexrelay)
	// lets go of it, finishing the turns it streams
	if !Handover && !Inherited() {
		codexrelay.Leave()
	}
	ln, err := s.listen(listenAddr())
	if err != nil {
		if addrInUse(err) {
			// say who has it, when that can be read (portbusy.go)
			if b, ok := NoteHeld(); ok {
				return fmt.Errorf("%w (%s)", err, b.Sentence())
			}
		}
		return err
	}
	ClearHeld()
	s.lnMu.Lock()
	s.ln = ln
	s.lnMu.Unlock()
	recordListening(ln.Addr())
	defer forgetListening()
	// the keys out of credit a magpie before this one rested go on
	// resting, and climb on from their rung (rest_store.go)
	loadRests()
	// a socket handed over (handover.go): the magpie that had it stops
	// accepting once told this one serves on it
	if ack := takeAck(); ack != nil {
		go ack()
	}
	// the observers plugins registered (usage.Observer) run while this
	// process serves, and plugins turned on or off elsewhere (the window,
	// `magpie plugin builtin`) come and go here too
	stopObservers := usage.ServeObservers()
	defer stopObservers()
	// and the user's own routing groups are served while plugins/groups is
	// on (provider.GateGroups); one made a moment ago turns it on as it is
	// looked up, not on KeepFresh's next look
	stopGroups := provider.GateGroups(func() {
		if _, err := pluginrt.Refresh(); err != nil {
			log.Printf("plugins: %v", err)
		}
	})
	defer stopGroups()
	// and magpie's own tools (its web search) are offered and answered
	// while the plugin serving each is on (gwtool)
	stopTools := gwtool.Gate()
	defer stopTools()
	// and a request masking is wanted for, with no masker served, turns
	// plugins/redact on as it is sent rather than being refused until
	// KeepFresh's next look (gwmw.Served)
	stopMasking := gwmw.Refresh(func() {
		if _, err := pluginrt.Refresh(); err != nil {
			log.Printf("plugins: %v", err)
		}
	})
	defer stopMasking()
	// as does a request of a gateway key held to a limit for plugins/budget
	// (gwbudget.Limiting)
	stopLimits := gwbudget.Refresh(func() {
		if _, err := pluginrt.Refresh(); err != nil {
			log.Printf("plugins: %v", err)
		}
	})
	defer stopLimits()
	freshCtx, stopFresh := context.WithCancel(ctx)
	defer stopFresh()
	// what another process writes to them is seen at once, not on the
	// loops' next look (#3185)
	go filememo.Watch(freshCtx, pluginrt.Path(), settings.Path(), provider.Path(), plugin.ListPath(), plugin.ToolsPath(), access.Path())
	go pluginrt.KeepFresh(freshCtx, filememo.OthersEvery, settings.Path(), provider.Path(), plugin.ListPath(), plugin.ToolsPath(), access.Path())
	// and the MCP hub's servers that the Library no longer gives are ended
	go keepHubPruned(freshCtx)
	srv := &http.Server{Handler: s.closesWhenHandedOff(s.Front()), ReadHeaderTimeout: 30 * time.Second, IdleTimeout: 5 * time.Minute, ConnState: s.unread.note}
	// the magpie serving the gateway, and only it, keeps the saved accounts
	// signed in, so two never refresh one sign-in at once
	go provider.KeepLoginsAlive(ctx)
	// and tells the request hooks of an account to sign in again
	go keepHooksToldOfSignIns(ctx)
	// and signs Codex and Claude Code in to their next account when the
	// one they are on is spent
	go provider.KeepOnAnAccountWithRoom(ctx)
	// and has the Codex app read its provider again as magpie becomes it
	// for a used-up ChatGPT account, and after (its Send greys otherwise)
	go provider.KeepCodexAppOnItsProvider(ctx, s.CodexBusy)
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
	// and counts the calls those plugins' accounts made outside magpie
	// (Cursor's editor and cursor-agent) on the Usage page
	go usage.KeepOutsideCallsRead(ctx)
	// and moves the built-in subscriptions being retired onto their plugins
	go provider.KeepRetiringMoved(ctx)
	// and fetches the partner list every hour, asked for or not
	go provider.KeepPartnersFresh(ctx)
	go provider.KeepPluginsListed(ctx) // models a plugin's vendor drops or adds, while magpie runs
	// and renews the plugins' sign-ins as they come due, used or not (#5080)
	go provider.KeepPluginsSignedIn(ctx)
	// and those a signed-in account's vendor adds (a ChatGPT plan's new GPT)
	go provider.KeepAccountsListed(ctx)
	// and fetches the lists of the providers the user asked to keep fresh (#2135)
	go provider.KeepListsFresh(ctx)
	// and follows a Remote magpie's list as it changes there (KaCli)
	go provider.KeepRemoteListsFollowed(ctx)
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
	// and, on Windows, serves WSL distros under NAT at Windows' address on
	// their network (wsl.go)
	go s.serveWSL(ctx, srv)
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		<-ctx.Done()
		s.endStreams()
		c, cancel := context.WithTimeout(context.Background(), s.drainFor())
		defer cancel()
		if s.handingOver() {
			// another magpie takes every connection from here: this one
			// stops taking them, and each one it took, new or kept alive,
			// has its next request read before Shutdown, which closes one
			// it reads after it began unanswered (#3568, #3451)
			s.stopAccepting()
			s.unread.wait(c)
		}
		// a socket's connection was taken from the server (hijacked), so
		// Shutdown doesn't wait for it: each ends after its turn in flight
		sockets := make(chan struct{})
		go func() { defer close(sockets); s.sockets.drain(c) }()
		if err := srv.Shutdown(c); err != nil {
			// the requests still answering are cut where they are, and
			// an agent streaming one reads a reply broken off (Codex:
			// "error decoding response body")
			log.Printf("gateway: ending after %s with requests still in flight; they are cut (%v)", s.drainFor(), err)
		}
		<-sockets
		// An inline description may still have to save after its request
		// ended, or was canceled: wait for every such description to save or
		// give up, rather than have the next start describe the images again.
		if !s.waitSightSaves(c) {
			log.Printf("gateway: ending before image descriptions could all be saved; the next start may describe them again")
		}
		s.subscription.abortAll()
		provider.FlushServed() // what answered last, kept for the next start and the CLI
		flushSticks()          // who answered which conversation, for the next start or the magpie handed over to
		// the MCP servers the hub started end with the gateway
		StopHub()
	}()
	for {
		err := srv.Serve(ln)
		// or stopAccepting closed it: what this one took finishes once ctx ends
		if err == nil || errors.Is(err, http.ErrServerClosed) || s.notAccepting.Load() {
			s.unread.stop()
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
	mux := s.routes()
	// the goroutine dump first, ahead of every lock the rest can be stuck
	// on (dumpFirst)
	// a socket's turns go through all of it again (socketEntry); the
	// agent's Retry-After is bounded on the way out (retryCapped)
	return socketEntry(s.dumpFirst(retryCapped(s.counted(headerLogged(holdCatalog(corsGuard(callerGuard(readOnlyGuard(mux, withCaller(keyLimited(s.keySlots(mux, mux))))))))))))
}

// routes are the client APIs, each by its pattern: readOnlyGuard reads
// which one a request is for, and TestReadOnlyKeyEveryRoute that each is
// judged for a read-only key.
func (s *Server) routes() *routeMux {
	mux := &routeMux{ServeMux: http.NewServeMux()}
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
	// the Ollama API's model routes, for a client that takes a custom
	// endpoint only as an Ollama server (ollama.go)
	mux.HandleFunc("GET /api/version", s.ollamaVersion)
	mux.HandleFunc("GET /api/tags", s.ollamaTags)
	mux.HandleFunc("POST /api/show", s.ollamaShow)
	// and its chat routes, for a client that chats only through them
	// (Zed's Ollama provider; ollama_chat.go)
	mux.HandleFunc("POST /api/chat", s.ollamaChat(false))
	mux.HandleFunc("POST /api/generate", s.ollamaChat(true))
	mux.HandleFunc("GET /v1/magpie/quotas", s.quotas)
	mux.HandleFunc("GET /v1/magpie/usage", s.usageSummary)
	mux.HandleFunc("GET /v1/magpie/usage/requests", s.usageRequests)
	mux.HandleFunc("POST /v1/magpie/usage/requests", s.usageIngest)
	mux.HandleFunc("GET /v1/magpie/quotas/history", s.quotasHistory)
	mux.HandleFunc("GET "+provider.RemoteCardsPath, s.quotaCards)
	mux.HandleFunc("GET "+provider.RemoteLibraryPath, s.sharedLibrary)
	mux.HandleFunc("POST "+provider.RemoteRefreshPath, s.quotaCardsRefresh)
	mux.HandleFunc("POST "+provider.RemoteCodexResetPath, s.codexReset)
	mux.HandleFunc("GET /v1/magpie/route", s.sessionRoute)
	mux.HandleFunc("GET "+StatuslinePath, s.statusline)
	mux.HandleFunc("POST "+StatuslinePath, s.statusline)
	mux.HandleFunc("GET /v1/magpie/trace", s.gatewayTrace)
	mux.HandleFunc("GET "+routeSnapshotPath, s.routeSnapshot)
	mux.HandleFunc("GET "+routeEventsPath, s.routeEvents)
	mux.HandleFunc("GET /v1/magpie/concurrency", s.concurrency)
	mux.HandleFunc("GET /v1/magpie/limit", s.keyLimit)
	// the management API, for a key that may manage magpie (admin.go)
	mux.HandleFunc(AdminPath, s.admin)
	mux.HandleFunc("GET "+OwnMCPPath, s.ownMCPList)
	// short-lived keys, issued by a key let issue them or from loopback
	// (issue_keys.go)
	mux.HandleFunc("POST "+KeysPath, s.issueKey)
	mux.HandleFunc("GET "+KeysPath, s.issuedKeys)
	mux.HandleFunc("DELETE "+KeysPath+"/{id}", s.revokeIssuedKey)
	// and one traded for an OIDC token, by a rule the user wrote
	// (oidc_keys.go)
	mux.HandleFunc("POST "+OIDCKeysPath, s.oidcIssueKey)
	// a magpie's proof that it holds a gateway key, asked with no key by
	// another magpie looking for it at a new address (remote_prove.go)
	mux.HandleFunc("POST "+provider.RemoteProvePath, s.prove)
	mux.HandleFunc("GET "+KeyPagePath, s.keyPage)
	mux.HandleFunc("GET "+CAPath, s.caFile)
	mux.HandleFunc("GET "+keyPageData, s.keyInfo)
	// the join page, where another computer that knows the join password
	// gets a gateway key of its own (join.go)
	mux.HandleFunc("GET "+JoinPagePath, s.joinPage)
	mux.HandleFunc("GET "+joinData, s.joinOpen)
	mux.HandleFunc("POST "+joinData, s.joinKey)
	// the Claude Code processes the Claude subscription runs, for magpie
	// claude-code runs (loopback only)
	mux.HandleFunc("GET "+ClaudeRunsPath, s.claudeRuns)
	// a heap profile of this process, for magpie heap (loopback only)
	mux.HandleFunc("GET "+HeapPath, s.heapProfile)
	// a CPU profile of this process over some seconds, for magpie cpu
	// (loopback only)
	mux.HandleFunc("GET "+CPUPath, s.cpuProfile)
	// every goroutine's stack and wait, for magpie goroutines (loopback
	// only)
	mux.HandleFunc("GET "+GoroutinesPath, s.goroutineDump)
	// a restart into the version magpie update put in (loopback only)
	mux.HandleFunc("POST "+RestartPath, s.restartToUpdate)
	// an update another magpie using this one asks for (a full-access key)
	mux.HandleFunc("POST "+provider.RemoteUpdatePath, s.remoteUpdate)
	mux.HandleFunc("GET /v1/magpie/omarchy", s.omarchyTheme)
	mux.HandleFunc("OPTIONS /v1/magpie/omarchy", s.omarchyTheme)
	mux.HandleFunc("POST /v1/chat/completions", s.handle(provider.Chat))
	mux.HandleFunc("POST /chat/completions", s.handle(provider.Chat))
	mux.HandleFunc("POST /v1/responses", s.handle(provider.Responses))
	mux.HandleFunc("POST /responses", s.handle(provider.Responses))
	mux.HandleFunc("GET /v1/responses", s.responsesOverHTTP)
	mux.HandleFunc("GET /responses", s.responsesOverHTTP)
	mux.HandleFunc("POST /v1/messages", keepsSignIn(s.handle(provider.Anthropic)))
	mux.HandleFunc("POST /messages", keepsSignIn(s.handle(provider.Anthropic)))
	mux.HandleFunc("POST /v1/systemone", s.serveSystemOne)
	// Codex's web.run tool (anton on Discord)
	mux.HandleFunc("POST /v1"+AlphaSearchPath, s.alphaSearch)
	mux.HandleFunc("POST "+AlphaSearchPath, s.alphaSearch)
	mux.HandleFunc("POST /v1/messages/count_tokens", keepsSignIn(s.countTokens))
	mux.HandleFunc("POST /v1/images/generations", s.images(false))
	mux.HandleFunc("POST /images/generations", s.images(false))
	mux.HandleFunc("POST /v1/images/edits", s.images(true))
	mux.HandleFunc("POST /images/edits", s.images(true))
	mux.HandleFunc("POST /v1/embeddings", s.retrieve("/embeddings", "embeddings"))
	mux.HandleFunc("POST /embeddings", s.retrieve("/embeddings", "embeddings"))
	mux.HandleFunc("POST /v1/rerank", s.retrieve("/rerank", "rerank"))
	mux.HandleFunc("POST /rerank", s.retrieve("/rerank", "rerank"))
	// OpenAI's Decisions API, typed answers to questions on one input
	mux.HandleFunc("POST /v1/decisions", s.decide)
	mux.HandleFunc("POST /decisions", s.decide)
	// speech to text, for a client's voice input (#2053)
	mux.HandleFunc("POST /v1/audio/transcriptions", s.transcribe("/audio/transcriptions", "transcriptions"))
	mux.HandleFunc("POST /audio/transcriptions", s.transcribe("/audio/transcriptions", "transcriptions"))
	mux.HandleFunc("POST /v1/audio/translations", s.transcribe("/audio/translations", "translations"))
	mux.HandleFunc("POST /audio/translations", s.transcribe("/audio/translations", "translations"))
	mux.HandleFunc("POST /v1/audio/speech", s.speak)
	mux.HandleFunc("POST /audio/speech", s.speak)
	mux.HandleFunc("POST /v1/videos", s.videosCreate)
	mux.HandleFunc("POST /videos", s.videosCreate)
	mux.HandleFunc("GET /v1/videos/{id}", s.videosGet)
	mux.HandleFunc("GET /videos/{id}", s.videosGet)
	mux.HandleFunc("GET /v1/videos/{id}/content", s.videosContent)
	mux.HandleFunc("GET /videos/{id}/content", s.videosContent)
	// a provider's own API, as it is, at its host (native.go)
	for _, m := range nativeMethods {
		mux.HandleFunc(m+" "+NativePath+"{provider}/{path...}", s.native)
	}
	mux.HandleFunc("POST "+claudeMCPPath+"{token}", s.subscription.mcpCall)
	mux.HandleFunc("/mcp/{name}", s.mcpProxy)
	mux.HandleFunc("/mcp/run/{name}", runMCP)
	mux.HandleFunc(SearchMCPPath, s.searchMCP)
	mux.HandleFunc(VisionMCPPath, s.visionMCP)
	mux.HandleFunc(MemoryMCPPath, s.memoryMCP)
	mux.HandleFunc(ImageMCPPath, s.imageMCP)
	mux.HandleFunc(BrowserMCPPath, s.browserMCP)
	mux.HandleFunc("GET "+BrowserExtensionPath, s.browserExtension)
	mux.HandleFunc("GET "+BrowserStatusPath, s.browserStatus)
	mux.HandleFunc(ImageMCPFiles+"{name}", s.imageMCPFile)
	mux.HandleFunc(ToolsMCPPath, s.toolsMCP)
	mux.HandleFunc(ToolsMCPOne+"{plugin}", s.toolsMCPOne)
	mux.HandleFunc(HubMCPPath+"{agent}", s.hubMCP)
	mux.HandleFunc(HubMCPAll, s.hubMCP)
	mux.HandleFunc(CodexPath+"/", s.codexBackend)
	mux.HandleFunc("GET "+CodexCatalogPath, s.codexCatalog)
	mux.HandleFunc("GET /v1beta/models", s.geminiModels)
	mux.HandleFunc("POST /v1beta/models/{call...}", s.gemini)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, provider.Chat, http.StatusNotFound, "magpie serves /v1/chat/completions, /v1/responses, /v1/messages, /v1/systemone, /v1/images/generations, /v1/images/edits, /v1/videos, /v1/embeddings, /v1/rerank, /v1/decisions, /v1/audio/transcriptions, /v1/audio/translations, /v1/audio/speech and /v1beta/models/*")
	})
	return mux
}

// responsesOverHTTP answers a GET on /v1/responses. A WebSocket upgrade
// is Responses over a WebSocket (#1005, #4058), each turn served as its
// POST is (responses_ws.go). A plain GET is told to POST.
func (s *Server) responsesOverHTTP(w http.ResponseWriter, r *http.Request) {
	if wantsSocket(r) && s.serveResponsesSocket(w, r) {
		return
	}
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		writeError(w, provider.Responses, http.StatusUpgradeRequired, "magpie relays Responses over HTTP only: send POST /v1/responses (streamed as SSE); Responses over a WebSocket is not served")
		return
	}
	w.Header().Set("Allow", "POST")
	writeError(w, provider.Responses, http.StatusMethodNotAllowed, "POST /v1/responses")
}

// catalogSize is how many models the catalog has, as info says, kept by
// provider.StampNow: every magpie on this computer asks another which
// it is (ServedBy, a window's every 15s, each look at its state while it
// doesn't serve), and each question built the whole catalog to count it,
// 5.4 GB of a day's 36 GB of allocations.
var catalogSize stamped[struct{}, int]

func (s *Server) info(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"name": "magpie", "version": Version, "models": catalogSize.get(struct{}{}, func() int { return len(provider.Catalog()) }), "window": Window,
		"apis": []string{"/v1/chat/completions", "/v1/responses", "/v1/messages", "/v1/systemone", "/v1beta/models/{model}:generateContent", "/v1/images/generations", "/v1/images/edits", "/v1/videos", NativePath + "{provider}/{path}", "/v1/embeddings", "/v1/rerank", "/v1/decisions", "/v1/audio/transcriptions", "/v1/audio/translations", "/v1/audio/speech", "/v1/magpie/quotas", "/v1/magpie/quotas/history", "/v1/magpie/usage", "/v1/magpie/usage/requests", "/v1/magpie/route", StatuslinePath, "/v1/magpie/trace", OwnMCPPath, SearchMCPPath, VisionMCPPath, MemoryMCPPath, ImageMCPPath, BrowserMCPPath, ToolsMCPPath, HubMCPAll, HubMCPPath + "{agent}"}}
	// which process serves, for another magpie on this computer to name it
	// on its Routing page (#2606); not told to another machine
	if local(r) {
		out["pid"], out["serves"] = os.Getpid(), Serves
	}
	// its MCP router has a server to give, for another magpie that has
	// this one as a Remote magpie to give it a server of its own
	// (provider/remote_hub.go); told to whoever the router answers
	if (local(r) || sharedWith(r)) && hubServes() {
		out["mcp_hub"] = true
	}
	writeJSON(w, 200, out)
}

// quotas is what is left of every subscription, plan and key magpie has,
// for an agent choosing where to send its work (magpie quota --json is the
// same). It names the accounts and their balances, so it answers this
// machine, and another only with an enabled gateway key, which lanGuard
// asks of it while the gateway is shared or MAGPIE_ADDR puts it on the
// network. A key held to a budget, models or accounts is someone else's
// to use and is told none of it (refuseHeld), as are quotasHistory,
// quotaCards, quotaCardsRefresh and concurrency; one its owner lets be
// told its quotas is told those of what it may use (scopeQuotas, #2216).
func (s *Server) quotas(w http.ResponseWriter, r *http.Request) {
	if !local(r) && !sharedWith(r) {
		writeError(w, provider.Chat, http.StatusForbidden, "magpie's quotas are told to another machine only when magpie is shared on the local network (Settings → Network and sharing → Share on local network) and the request carries its API key (Authorization: Bearer <key> or x-api-key: <key>)")
		return
	}
	scope, ok := scopeQuotas(w, r)
	if !ok {
		return
	}
	// ?kept=1: what was last read, no vendor asked (magpie statusline,
	// run on every message, #1997)
	if r.URL.Query().Get("kept") == "1" {
		writeJSON(w, 200, map[string]any{"object": "list", "data": scope.quotas(provider.KeptQuotaReport(time.Now()))})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	writeJSON(w, 200, map[string]any{"object": "list", "data": scope.quotas(provider.QuotaReport(ctx, time.Now()))})
}

// quotasHistory is what was left of each account's windows over time, as
// this magpie (and the computers sharing usage with it) read them (#651):
// ?days= back (45 at most), ?provider= and ?user= to pick an account.
// /v1/magpie/quotas stays the snapshot.
func (s *Server) quotasHistory(w http.ResponseWriter, r *http.Request) {
	if !local(r) && !sharedWith(r) {
		writeError(w, provider.Chat, http.StatusForbidden, "magpie's quotas are told to another machine only when magpie is shared on the local network (Settings → Network and sharing → Share on local network) and the request carries its API key (Authorization: Bearer <key> or x-api-key: <key>)")
		return
	}
	scope, ok := scopeQuotas(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	writeJSON(w, 200, map[string]any{"object": "list", "data": scope.histories(provider.QuotaHistories(provider.QuotaHistorySince(q.Get("days"), time.Now()), q.Get("provider"), q.Get("user")))})
}

// quotaCards is the Usage page's cards as this magpie last read them, for
// another magpie that has this one as its provider (remote-magpie) to
// show: its own cache, however old, and no vendor asked for it.
func (s *Server) quotaCards(w http.ResponseWriter, r *http.Request) {
	if !local(r) && !sharedWith(r) {
		writeError(w, provider.Chat, http.StatusForbidden, "magpie's quotas are told to another machine only when magpie is shared on the local network (Settings → Network and sharing → Share on local network) and the request carries its API key (Authorization: Bearer <key> or x-api-key: <key>)")
		return
	}
	scope, ok := scopeQuotas(w, r)
	if !ok {
		return
	}
	writeCards(w, r, scope)
}

// SharedLibrary is the Library as another magpie is told it (JSON), set
// by the app: the gateway doesn't know the library package.
var SharedLibrary func() ([]byte, error)

// sharedLibrary tells another magpie that has this one as its provider
// the skills and prompts of the Library, to use them as its own (KoiLx0
// on X). Only to this computer and to a full-access key of a magpie
// shared on the local network: a key held to models, a budget or
// accounts is the user's limit on what that caller may use.
func (s *Server) sharedLibrary(w http.ResponseWriter, r *http.Request) {
	if !local(r) && !sharedWith(r) {
		writeError(w, provider.Chat, http.StatusForbidden, "magpie's Library is told to another machine only when magpie is shared on the local network (Settings → Share on local network) and the request carries its API key (Authorization: Bearer <key> or x-api-key: <key>)")
		return
	}
	if heldKey(access.Caller(r.Context())) {
		refuseHeld(w, r, heldQuotasOff)
		return
	}
	if SharedLibrary == nil {
		writeError(w, provider.Chat, http.StatusNotFound, "this magpie has no Library to tell")
		return
	}
	b, err := SharedLibrary()
	if err != nil {
		writeError(w, provider.Chat, http.StatusInternalServerError, "reading the Library: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(b)
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
		writeError(w, provider.Chat, http.StatusForbidden, "magpie's quotas are told to another machine only when magpie is shared on the local network (Settings → Network and sharing → Share on local network) and the request carries its API key (Authorization: Bearer <key> or x-api-key: <key>)")
		return
	}
	scope, ok := scopeQuotas(w, r)
	if !ok {
		return
	}
	id, user := r.URL.Query().Get("provider"), r.URL.Query().Get("user")
	// a card this magpie has from a Remote magpie of its own, which it
	// passes on (provider.CachedCards), is read again there; the card
	// standing for that remote itself, and any other id with a "/", is
	// none this magpie tells
	if remote := provider.IsRemoteCard(id); strings.Contains(id, "/") && !remote || remote && !strings.Contains(id, "/") {
		writeError(w, provider.Chat, http.StatusBadRequest, "this magpie reads again only the cards it tells")
		return
	}
	// a key told only some cards (#2216) has only those read again: a
	// card it isn't told is refused, as if held, and with none named
	// each card it is told is read, not every account here
	if scope != nil && id != "" && !scope.card(id, user) {
		refuseHeld(w, r, heldRefused)
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
		switch {
		case id != "":
			provider.RefreshUsage(ctx, id, user)
		case scope != nil:
			provider.RefreshEach(ctx, scope.cards(provider.CachedCards(time.Now())))
		default:
			provider.ReadAllCards(ctx)
		}
		cancel()
	}
	writeCards(w, r, scope)
}

// codexResetsAsked are the Codex accounts a reset is being spent on now
// (codexReset): a second ask while the first is out is refused, not
// spent as well.
var codexResetsAsked = struct {
	sync.Mutex
	m map[string]bool
}{m: map[string]bool{}}

// codexReset spends one of a Codex account's rate-limit resets, as the
// Usage page's "Use a reset" does (provider.SpendCodexReset, the same
// code), asked by another magpie's card or an app holding a gateway key
// (#3850, prgrmrwy). A reset can't be undone, so beyond what
// quotaCardsRefresh asks it takes a gateway key, from this computer too,
// never one that is read-only (readOnlyGuard refuses that before here)
// nor one held to a budget, models or accounts: a browser page or an app the gateway lets in on loopback
// without a key (Origin null, an app's scheme, a *.localhost page) is
// refused, as is a request with no key at all. The account is named
// (?user=), never "whichever is signed in", which a caller elsewhere
// can't see; ?credit= picks one of its resets.each.
func (s *Server) codexReset(w http.ResponseWriter, r *http.Request) {
	if !local(r) && !sharedWith(r) {
		writeError(w, provider.Chat, http.StatusForbidden, "magpie's quotas are told to another machine only when magpie is shared on the local network (Settings → Network and sharing → Share on local network) and the request carries its API key (Authorization: Bearer <key> or x-api-key: <key>)")
		return
	}
	who := access.Caller(r.Context())
	if who.KeyID == "" || who.ReadOnly {
		writeError(w, provider.Chat, http.StatusUnauthorized, "using a Codex reset can't be undone, so it takes an enabled magpie gateway key with full access (Gateway → Gateway keys), sent as Authorization: Bearer <key> or x-api-key, from this computer too; or use it on this computer's Usage page")
		return
	}
	// a key held to a budget, models or accounts is someone else's to
	// use: told some cards (#2216) or none, it spends no reset of the
	// owner's
	if refuseHeld(w, r, "this gateway key is held to a budget, models or accounts, so it uses none of this magpie's Codex resets: use a key with full access") {
		return
	}
	q := r.URL.Query()
	user, credit := strings.TrimSpace(q.Get("user")), q.Get("credit")
	if user == "" {
		writeError(w, provider.Chat, http.StatusBadRequest, "name the Codex account whose reset is used: ?user=<its email>")
		return
	}
	key := strings.ToLower(user)
	codexResetsAsked.Lock()
	busy := codexResetsAsked.m[key]
	codexResetsAsked.m[key] = true
	codexResetsAsked.Unlock()
	if busy {
		writeError(w, provider.Chat, http.StatusConflict, "a reset of "+user+" is being used already; read its card to see what it did")
		return
	}
	defer func() {
		codexResetsAsked.Lock()
		delete(codexResetsAsked.m, key)
		codexResetsAsked.Unlock()
	}()
	log.Printf("Codex reset of %s asked by gateway key %q", user, who.KeyName)
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	out, err := provider.SpendCodexReset(ctx, user, credit)
	cancel()
	if err != nil {
		writeError(w, provider.Chat, http.StatusBadGateway, err.Error())
		return
	}
	// the card read again, so the cards answered next show its windows
	// started again and a reset fewer
	if out.Code == "reset" {
		read, done := context.WithTimeout(context.WithoutCancel(r.Context()), 15*time.Second)
		provider.RefreshUsage(read, "codex", user)
		done()
	}
	writeJSON(w, 200, out)
}

func modelObject(e provider.Entry) map[string]any {
	type reasoningLevel struct {
		Effort string `json:"effort"`
	}
	levels := make([]reasoningLevel, 0, len(e.Efforts))
	for _, effort := range e.Efforts {
		levels = append(levels, reasoningLevel{Effort: effort})
	}
	// the same levels as plain names, [] when there are none: clients of
	// the Catwalk schema (Crush and kin) read this flat array to know which
	// reasoning efforts a model takes, and drop the effort from requests to
	// a model that names none (#2576). The objects stay for the readers the
	// list already has (catalog.levelsOf reads either)
	flat := append(make([]string, 0, len(e.Efforts)), e.Efforts...)
	m := map[string]any{"id": e.ID, "object": "model", "type": "model", "created": 0, "created_at": "2025-01-01T00:00:00Z",
		"owned_by": e.Provider.ID, "display_name": e.PlainName(), "reasoning": e.Thinks(), "supported_reasoning_levels": levels,
		"reasoning_levels": flat}
	// the window, as the names clients read it by: a group's context
	// (magpie group set … context=) included
	if e.Context > 0 {
		m["context_window"], m["context_length"], m["max_input_tokens"] = e.Context, e.Context, e.Context
	}
	// the reply limit, kept within that window as agents are told it
	// (#1438)
	if out := e.PublishedOutput(); out > 0 {
		m["max_output_tokens"] = out
	}
	// for another magpie that has this one as its provider (remote-magpie):
	// the APIs a request for the model goes on as it is, so it sends each
	// one on an API of these rather than having it translated twice, and
	// whether it takes images
	if native := provider.NativeEndpoints(e); len(native) > 0 {
		m["native_endpoints"] = native
	}
	// what it takes and gives: every kind its source listed, audio, video
	// and pdf too, not image alone (#1595)
	if in, out := e.Modalities(); in != nil || out != nil {
		mod := map[string]any{}
		if in != nil {
			mod["input"] = in
		}
		if out != nil {
			mod["output"] = out
		}
		m["modalities"] = mod
	}
	return m
}

// toolsTold says, in an OpenAI-compatible list, that a chat model takes
// tools, by the names clients read it under: models.dev's tool_call
// (beside its reasoning, which modelObject gives) and OpenRouter's
// supported_parameters, whose "tools" Cline, Kilo and kin read before they
// offer a model their tools. Without either, a client that matches what a
// model can do by its id (psikent on Discord: an SSH client) found none
// for a routing group, whose id is the user's own name, and wouldn't hand
// it a tool. Every chat model magpie lists takes tools — the gateway reads
// one written as text back as a call (textcalls.go) — so a group, all of
// whose members do, says so as well. The decision models and Claude
// Desktop's and Claude Science's lists, shaped for their own readers, are
// left as they were.
func toolsTold(m map[string]any, e provider.Entry) {
	m["tool_call"] = true
	params := []string{"tools", "tool_choice"}
	if e.Thinks() {
		params = append(params, "reasoning")
	}
	m["supported_parameters"] = params
}

// pricedModelObject is modelObject with the model's pricing, as the list
// gives it, for the callers the list tells it to.
func pricedModelObject(r *http.Request, e provider.Entry) map[string]any {
	m := modelObject(e)
	toolsTold(m, e)
	if local(r) || sharedWith(r) {
		if pr, ok := provider.EntryPriceIn(settings.Load(), provider.GroupFinder(), e); ok {
			m["pricing"] = catalog.OpenRouterPricing(pr)
		}
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
	for _, p := range provider.NativeEndpoints(e) {
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
	if out := e.PublishedOutput(); out > 0 {
		c["max_output_tokens"] = out
	}
	if e.Images {
		c["supports_vision"] = true
	} else if e.ImageInput != nil {
		c["supports_vision"] = false
	}
	m["capabilities"] = c
}

// drawerObjects are the image models magpie draws with, as another magpie
// asks for them in its list (provider.DrawersHeader): "kind": "image",
// which an agent's list never has, so it takes none for a model to chat
// with. A gateway key held to some models is told of those alone, as its
// chat list is: models it names that an account it may use serves (#2579).
func drawerObjects(r *http.Request) []map[string]any {
	who, held := keyListHolds(r)
	var out []map[string]any
	for _, p := range provider.All() {
		if !p.On() || p.DecideOnly() {
			continue
		}
		// less those unticked in its editor, as Settings › Models › Images lists them
		for _, m := range OfferedDrawers(p) {
			if held && (!modelAllowed(who, p, m.ID) || !accountServes(who, p, m.ID)) {
				continue
			}
			name := cmp.Or(m.Name, m.ID)
			o := map[string]any{"id": p.ID + "/" + m.ID, "object": "model", "type": "model", "kind": "image", "created": 0,
				"owned_by": p.ID, "display_name": name, "magpie_label": name + " · " + p.Name,
				"modalities": map[string]any{"input": []string{"text"}, "output": []string{"image"}},
				"magpie_via": catalog.ViaHere(m.Via)}
			// the endpoints it is drawn through, in new-api's words, which
			// Cherry Studio's new-api provider reads its kind from
			// (listModels.ts newApiFetcher): image-generation first
			ends := []string{"image-generation"}
			if m.Images {
				o["modalities"] = map[string]any{"input": []string{"text", "image"}, "output": []string{"image"}}
				ends = append(ends, "image-edit")
			}
			o["supported_endpoint_types"] = ends
			out = append(out, o)
		}
	}
	return out
}

// videomakerObjects are the video models magpie makes videos with, as
// another magpie asks for them (provider.VideomakersHeader): "kind": "video".
// A gateway key held to some models or accounts is told of those alone,
// as its chat list is (#2579).
func videomakerObjects(r *http.Request) []map[string]any {
	who, held := keyListHolds(r)
	var out []map[string]any
	for _, p := range provider.All() {
		if !p.On() || p.DecideOnly() {
			continue
		}
		for _, m := range OfferedVideomakers(p) {
			if held && (!modelAllowed(who, p, m.ID) || !accountServes(who, p, m.ID)) {
				continue
			}
			name := cmp.Or(m.Name, m.ID)
			out = append(out, map[string]any{"id": p.ID + "/" + m.ID, "object": "model", "type": "model", "kind": "video", "created": 0,
				"owned_by": p.ID, "display_name": name, "magpie_label": name + " · " + p.Name,
				"modalities":               map[string]any{"input": []string{"text", "image"}, "output": []string{"video"}},
				"supported_endpoint_types": []string{"openai-video"},
				"magpie_via":               catalog.ViaHere(m.Via)})
		}
	}
	return out
}

// viaOf is the magpies e's model passes through after this one, as the
// remote magpie serving it listed them; nil for a provider's own model.
// A routing group's are its members' together: a group with a remote
// magpie's group in it (two magpies each the other's remote, each with an
// auto/ group of the model) reaches every magpie that one does (catalog's
// magpie_via.go).
func viaOf(e provider.Entry) []string {
	if e.Group == "" {
		return e.Provider.RemoteVia(e.Model)
	}
	_, ms, _ := provider.FindGroup(e.ID)
	var via []string
	for _, m := range ms {
		for _, v := range m.Provider.RemoteVia(m.Model) {
			if !slices.Contains(via, v) {
				via = append(via, v)
			}
		}
	}
	return via
}

// catalogFor is the catalog as the agent asking is shown it.
func catalogFor(r *http.Request) []provider.Entry {
	agent := agentOf(r)
	shown, _ := provider.CatalogFor(agent)
	return keyListed(r, shown, provider.ModelOff(agent))
}

// listKinds is which of magpie's models a /v1/models call lists: its chat
// models alone, unless it names the kinds it wants in ?kind= (chat, image,
// video, comma separated; all for the three). Image and video models are
// listed only when asked for, each marked "kind" (drawerObjects,
// videomakerObjects): OpenAI's own list puts gpt-image and sora among its
// chat models with nothing to tell them apart, and the clients and agents
// that offer every id this list gives as a model to chat with (OpenCode's,
// Cherry Studio's, ZCode's pasted ?format=text) would offer an image model
// in their chat pickers (axelburks on Discord asked for them here). A kind
// it doesn't know is refused, not read as none.
//
// Cherry Studio is the exception: it can't ask, and it tells an image model
// from a chat one itself. Its provider of magpie (an OpenAI-compatible one)
// reads only each id, then looks the id's last segment up in its own model
// registry (provider-registry's models.json, by normalizeModelId), which
// marks gpt-image-2, dall-e-3, the gemini-*-image, imagen, seedream and
// grok-imagine models as image generation: those go to its Paintings page,
// not its chat picker (isNonChatModel). Without them listed, Paintings had
// no model of magpie's (pingqipai3 on X). So it is listed every kind.
func listKinds(r *http.Request) (chat, image, video bool, err error) {
	q, ok := r.URL.Query()["kind"]
	if !ok {
		// a list of ids pasted somewhere (?format=text) stays chat models
		media := cherryStudio(r) && r.URL.Query().Get("format") != "text"
		return true, media, media, nil
	}
	for _, v := range q {
		for k := range strings.SplitSeq(v, ",") {
			switch strings.ToLower(strings.TrimSpace(k)) {
			case "chat":
				chat = true
			case "image":
				image = true
			case "video":
				video = true
			case "all":
				chat, image, video = true, true, true
			default:
				return false, false, false, fmt.Errorf("kind %q: /v1/models lists the kinds chat, image and video (or all), comma separated", k)
			}
		}
	}
	return chat, image, video, nil
}

// cherryStudio is a request of Cherry Studio's: with the key magpie's link
// gives it (agentOf), or a key the user typed in by hand, by its User-Agent,
// Electron's own with the app's name in it ("Mozilla/5.0 (…)
// CherryStudio/2.1.2 Chrome/… Electron/…").
func cherryStudio(r *http.Request) bool {
	return agentOf(r) == "cherrystudio" || strings.Contains(r.Header.Get("User-Agent"), "CherryStudio/")
}

func (s *Server) models(w http.ResponseWriter, r *http.Request) {
	// Antigravity's app's catalog, each model with its window (antigravity_models.go)
	if r.URL.Query().Get("format") == AntigravityFormat {
		antigravityModels(w, r)
		return
	}
	if waitsForList(r) {
		// another magpie waiting for the list to change (models_wait.go)
		s.modelsOnceChanged(w, r)
		return
	}
	s.listModelsTagged(w, r)
}

func (s *Server) listModels(w http.ResponseWriter, r *http.Request) {
	data := []map[string]any{}
	chat, images, videos, err := listKinds(r)
	if err != nil {
		writeError(w, provider.Chat, 400, err.Error())
		return
	}
	// its chat models, unless ?kind= asks for image or video models alone
	if chat {
		shown := catalogFor(r)
		if agent := agentOf(r); agent == "claude-desktop" {
			data = desktopModels(shown, desktopKnowsMythos(r.Header.Get("User-Agent")))
			data = append(data, DesktopTierRows(shown, data)...)
		} else if agent == ScienceID {
			data = scienceModels(shown)
		} else {
			labels := provider.LabelsFor(agent, shown)
			// for another magpie: how it searches the web for each model
			magpie := r.Header.Get(provider.DrawersHeader) != ""
			searches := magpie && canSearch()
			// for another magpie, what each model costs here, so its usage
			// there is counted at the same price: told only to this computer
			// and to one this magpie is shared with by a gateway key, as the
			// prices are the user's own settings
			//
			// and to any client, OpenRouter's pricing for each model it knows
			// a price of (cleary5779 on Discord: an agent built on magpie
			// shows what a call costs), told to the same callers: the price is
			// what the Usage page counts the model at, the user's own included.
			// The settings, groups and providers are read once for the list
			// (EntryPricer), not once a model
			told := local(r) || sharedWith(r)
			prices := told && r.Header.Get(provider.PricesHeader) != ""
			var priceOf func(provider.Entry) (catalog.Price, bool)
			if told {
				priceOf = provider.EntryPricer(settings.Load(), provider.GroupFinder())
			}
			// the tiers Codex here is offered on each model (#1234), for every
			// client: another magpie offers them to its own Codex, and Cindy
			// shows its Fast switch on a model whose tiers hold priority
			// (#1914). A model with none says nothing
			tiers := codexTiers(shown)
			// each model by its bare id as well, after the provider/model ids,
			// when the user asked for it (settings.BareModelIDs): a client
			// moved from another proxy asks for gpt-5.6-sol (love1sbug on X).
			// Never for another magpie, whose provider would list each twice.
			// Each row is the entry the bare id reaches, its label that one's.
			rows, ids := slices.Clip(shown), map[int]string{}
			if !magpie && provider.BareModelIDs() {
				labels = slices.Clip(labels)
				for _, b := range provider.BareEntries(shown) {
					ids[len(rows)] = b.ID
					rows = append(rows, shown[b.Of])
					labels = append(labels, labels[b.Of])
				}
			}
			for i, e := range rows {
				m := modelObject(e)
				toolsTold(m, e)
				if id, ok := ids[i]; ok {
					m["id"] = id
				}
				if magpie {
					if how := webSearchOf(e, searches); how != "" {
						m["web_search"] = how
					}
					// the magpies it goes through, this one last, so a
					// magpie that is this one's remote too doesn't take
					// back its own models (catalog's magpie_via.go)
					m["magpie_via"] = catalog.ViaHere(viaOf(e))
					// the provider serving it, the first in a chain of
					// magpies, for one that names its models by it
					// (provider.UpstreamNames, lcc580 on X)
					m["magpie_by"] = e.ServedBy()
				}
				if ts := tiers[e.ID]; len(ts) > 0 {
					m["service_tiers"] = ts
				}
				if told {
					if pr, ok := priceOf(e); ok {
						m["pricing"] = catalog.OpenRouterPricing(pr)
						if prices {
							m["magpie_price"] = pr
						}
					}
				}
				// for another magpie: its name as the agents' lists here call
				// it, with its provider's after it unless the user wants it
				// plain, so two providers' models of one name are told apart
				// there as they are here
				m["magpie_label"] = labels[i]
				// for DeepSeek Harness: the same name as its name. Its
				// Models page's model discovery (llm-pi-ai's readListing)
				// names a row by name, then display_name, and display_name
				// is the name alone, so two providers' models of one name
				// read the same there, Kimi-K3.1-Pro of WorkBuddy and of
				// Qoder CN (LeiWang_2000 on X), where the route magpie
				// writes for it names them as its list does (dshKindConfig)
				if agent == "dsh" {
					m["name"] = labels[i]
				}
				if agent == "cursor-local" {
					cursorLocalModel(m, e)
				}
				data = append(data, m)
			}
		}
	}
	if images || r.Header.Get(provider.DrawersHeader) != "" {
		data = append(data, drawerObjects(r)...)
	}
	if videos || r.Header.Get(provider.VideomakersHeader) != "" {
		data = append(data, videomakerObjects(r)...)
	}
	if r.Header.Get(provider.DecidersHeader) != "" {
		// the decision models its own pickers offer: of a provider with
		// picks (OpenRouter's twenty-odd), the picked ones, so another
		// magpie lists what its Gateway page shows (GokoShiki on Discord)
		deciders := keyAllowed(r, provider.OfferedDeciders(nil))
		for i := range deciders {
			deciders[i].Name = cmp.Or(deciders[i].Name, deciders[i].Model)
		}
		labels := provider.LabelsFor(agentOf(r), deciders)
		for i, e := range deciders {
			m := modelObject(e)
			m["kind"], m["magpie_label"] = "decision", labels[i]
			m["native_endpoints"] = []string{"/v1/systemone"}
			m["magpie_via"] = catalog.ViaHere(viaOf(e))
			m["magpie_by"] = e.ServedBy()
			data = append(data, m)
		}
	}
	// the names the calling key gives models, where the models were
	aliasNames(r, data)
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
	// the catalog the agent asking is listed from (/v1/models): Codex's
	// own ChatGPT models a group listing alone keeps out of Catalog are
	// still in Codex's, and found here too
	shown := keyListed(r, provider.CatalogOf(agentOf(r)), nil)
	for _, e := range shown {
		if e.ID == id || e.Group != "" && provider.SameRef(e.ID, id) {
			if agentOf(r) == ScienceID {
				// by the alias it asked by, which it keeps what it reads
				// under
				writeJSON(w, 200, scienceObject(e, provider.LabelsFor(ScienceID, []provider.Entry{e})[0]))
				return
			}
			o := pricedModelObject(r, e)
			if name, ok := access.Caller(r.Context()).AliasOf(e.ID); ok {
				o["id"] = name
			}
			writeJSON(w, 200, o)
			return
		}
	}
	// by its bare id, as the list gives it with that setting on
	if provider.BareModelIDs() {
		if e, ok := provider.BareEntry(shown, id); ok {
			writeJSON(w, 200, pricedModelObject(r, e))
			return
		}
	}
	writeError(w, provider.Chat, 404, "unknown model "+id)
}

// unprefixed is the model magpie serves by an id Claude Desktop was given
// for it (claudeLooking): anthropic/magpie-<number>, mythos-magpie-<number>,
// magpie-<number>.anthropic.<Claude model>, or, as it listed them
// before, "anthropic/" put in front of magpie's id — or by its flat
// spelling (unflat) or its Claude version dashed (provider.ClaudeUndashed).
// An id that is magpie's as it stands (a provider named
// anthropic) is left alone.
func unprefixed(id string) string {
	if real, ok := aliased(id); ok {
		return real
	}
	if real := unflat(id); real != id {
		return real
	}
	if real, ok := provider.ClaudeUndashed(id); ok {
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
//
// A level spelled "off" (":off") is none, as a body's effort "off" is
// (offAsNone).
func askedAt(id string) (string, string) {
	bare, marked := strings.CutSuffix(id, "[1m]")
	m, e := provider.MemberEffort(bare)
	if strings.Contains(e, "+") {
		return id, "" // a group's member at several levels, not one asked
	}
	if i := strings.LastIndex(bare, ":"); e == "" && i > 0 && strings.EqualFold(bare[i+1:], "off") {
		m, e = provider.MemberEffort(bare[:i] + ":none")
	}
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

// decisionModelRefused is what a conversation or a token count asked of
// a decision model is told: it answers System One's questions alone.
func decisionModelRefused(model string) string {
	return fmt.Sprintf("%s only decides a routing group's model and effort; it holds no conversation", model)
}

// decisionGroupRefused is decisionModelRefused for a group of them.
func decisionGroupRefused(g provider.Group) string {
	return fmt.Sprintf("the routing group %s holds decision models: it only decides a routing group's model and effort, asked at /v1/systemone; it holds no conversation", g.Name)
}

// countTokens answers Anthropic's count_tokens: through the provider when
// it implements counting, else a rough estimate. A failed connection or
// limited key yields to the next key; other failures reach the client.
func (s *Server) countTokens(w http.ResponseWriter, r *http.Request) {
	body, ok := s.requestBody(w, r, provider.Anthropic)
	if !ok {
		return
	}
	// counted as the HEIC photos in it will be sent (heic.go), their
	// base64 read as it will be sent (base64_input.go)
	body = heicAsJPEG(provider.Anthropic, stdBase64Body(provider.Anthropic, body))
	var err error
	var model string
	body, model, err = requestModel(body)
	if err != nil {
		writeError(w, provider.Anthropic, 400, err.Error())
		return
	}
	named := unprefixed(model)
	if m := claudeTierStandIn(r, agentOf(r), model); m != "" {
		model = m // counted on the model the request will go to
	}
	model, _ = askedAt(model) // counted as the model, whatever its effort
	id := unprefixed(model)
	if own, ok := ownFirst(r, agentOf(r), id); ok {
		id = own // counted where serve sends it
	} else if sid, ok := provider.AutoStandInFor(agentOf(r), id); ok {
		id = sid
	}
	pick := desktopSubagentPick(r, agentOf(r), named, id, body)
	if pick.Moved() {
		id = pick.To
	}
	p, model, ok := provider.Resolve(id)
	// a gateway key held to some models (#882) counts no tokens on one it
	// may not use, as it would be refused serving it
	if countHeld(w, r, provider.Anthropic, id) {
		return
	}
	// a routing group held to some accounts counts on one of them, or
	// the local estimate says the count, as for a key's (countOn)
	r = heldByGroupAt(r, id)
	// nor on one the user disabled (#1867)
	if countOff(w, provider.Anthropic, id) {
		return
	}
	// nor on a decision model or a group of them, which hold no
	// conversation: refused as the request itself would be, not sent to
	// the vendor's count_tokens (coeo91 on Discord: OpenRouter refuses a
	// decisions model anywhere but its decisions endpoint)
	if ok && p.DecidesModel(model) {
		writeError(w, provider.Anthropic, 400, decisionModelRefused(named))
		return
	}
	if g, _, isGroup := provider.FindGroup(id); !ok && isGroup && g.Decides() {
		writeError(w, provider.Anthropic, 400, decisionGroupRefused(g))
		return
	}
	// Count the same masked prompt that generation sends to the vendor,
	// and the one as written where generation sends that, masked only
	// then (#3973)
	if _, ms, isGroup := provider.FindGroup(id); !ok || !unredactedRoute(p, model, isGroup, ms) {
		if fp, before := maskedBefore(r); before {
			// masked by the magpie that passed it on (serve)
			r = withMaskedAs(r, fp)
		} else {
			rw, b, done, hits, err := redactedHits(maskedFor(r.Context(), p, model, isGroup, ms), w, body)
			if err != nil {
				writeError(w, provider.Anthropic, maskStatus(err), err.Error())
				return
			}
			// counted where generation goes: the model set for a request
			// masking would change, as written (maskRouteFor)
			if to := maskRouteFor(agentTurn(r), hits); to != "" {
				p, model, ok = provider.Resolve(to)
			} else {
				w, body = rw, b
				r = withMaskedAs(r, ownMaskedAs(r.Context()))
				defer done()
			}
		}
	}
	if level := desktopSubagentEffort(r, agentOf(r), body, pick); level != "" && ok && !provider.Reasonless(p.ID, model) {
		body = withFixedEffort(provider.Anthropic, body, fitLevel(level, p.Efforts(model)))
	}
	s.countOn(w, r, p, model, ok, body)
}

// countOn counts body's tokens for model on p, resolved when ok.
func (s *Server) countOn(w http.ResponseWriter, r *http.Request, p provider.Provider, model string, ok bool, body []byte) {
	// the headers the user gave the model, on its count as on its turns
	r = r.WithContext(provider.WithModel(r.Context(), p.ID, model))
	// the model's own system prompt is counted as it is sent
	if ok {
		body = withModelSystem(r, p, model, provider.Anthropic, body)
	}
	// Claude Subscription generations run through the Claude Code binary. Its
	// OAuth token must not take a direct HTTP side path just for token counting.
	if ok && p.Account != nil && (p.Account.Agent == "claude" || p.Account.Agent == "cursor" || p.Account.Agent == "grok" || p.Account.Agent == "devin" || p.Account.Agent == "kiro" || p.Account.Agent == "qoder" || p.Account.Agent == provider.QoderCNID || p.Account.Agent == "zed" || p.Account.Agent == "factory" || p.Account.Agent == "gemini" || p.Account.Agent == "antigravity" ||
		// ZCode itself never counts, and zcode.z.ai answers count_tokens
		// with an error every time: one more request its firewall weighs
		p.Account.Agent == "zcode") ||
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
			// A relay's OpenAI-only key can't count Anthropic tokens;
			// Vertex AI counts Claude's at the project (Base)
			if c.p.Base(provider.Anthropic) != "" && slices.Contains(s.usable(c.p, model), provider.Anthropic) {
				counts = append(counts, c)
			}
		}
		// the accounts or keys the calling key may not use are left out of
		// the counts too (#905): counted on one it may, and where it may
		// use none, the local estimate below says the count
		if who, held := accountHolds(r); held {
			counts = slices.DeleteFunc(counts, func(c candidate) bool { return !accountAllowed(who, c) })
		}
		// a count doesn't wait for room in a MaxRPM's minute: one with
		// none now is left out, and where none has room the estimate
		// below says the count (rpm.go)
		counts = slices.DeleteFunc(counts, func(c candidate) bool { return !s.rpms.free(c.who(), c.p.RPMLimit()) })
		// of those resting, only the one back soonest (#1799)
		counts = probeOne(counts)
	}
	// the names in force, read at most once however many candidates are
	// counted, and not at all where there are none
	wires := sync.OnceValue(func() map[string]string { return settings.Shared().Wires() })
	for i, c := range counts {
		res, err := s.forward(r.Context(), c.p, provider.Anthropic, "/v1/messages/count_tokens", rewriteModel(body, provider.UpstreamNameIn(wires(), c.p.ID, model)), r.Header)
		if tooMany := (*errRPM)(nil); errors.As(err, &tooMany) {
			break // its minute filled since: nobody rests, the estimate answers
		}
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
			keepRetryOut(w.Header(), res.Header, b)
			passReplyHeaders(w.Header(), c.p, res)
			writeError(w, provider.Anthropic, res.StatusCode, explained(c.p, model, res, b))
			return
		}
		defer res.Body.Close()
		passReplyHeaders(w.Header(), c.p, res)
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
var unsupportedCountWords = lazyre.New(`(?i)` +
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
		var model string
		body, model, err = requestModel(body)
		if err != nil {
			writeError(w, from, 400, err.Error())
			return
		}
		noteAsked(r.Context(), model)
		// Qoder 1.1.82 and later name no session: the one of its own
		// session files the request holds the prompts of (qoder_session.go)
		r = qoderSession(r, from, body)
		// a Claude Code Message Thread magpie keeps no history for: sent
		// again whole, without it (message_threads.go, #2480)
		if from == provider.Anthropic && namesThread(body) {
			refuseThread(w)
			return
		}
		// a thread Codex started on magpie, on one of Codex's own models
		// magpie serves nothing for: on Codex's own sign-in, as the
		// ChatGPT backend's route sends it (codexOwnOffMagpie)
		if from == provider.Responses {
			if r2, why := codexOwnOffMagpie(r, body, model); r2 != nil {
				s.codexBackend(w, r2)
				return
			} else if why != "" {
				writeError(w, from, 404, why)
				return
			}
		}
		// Codex with magpie as its provider: spawn_agent names every
		// model a subagent may be put on, not only Codex's first five
		// (Gearclickist on Discord)
		if from == provider.Responses && (fromCodex(r.Header) || agentOf(r) == "codex") {
			body = spawnAgentModels(r, body, model, false)
			// the service tier set for Codex, in place of the one it
			// asked (codexTiered)
			body = codexTiered(r, body, model)
		}
		// the service tier the gateway key is set to, over the client's
		// ask and the Codex setting (keyTiered)
		if from == provider.Responses || from == provider.Chat {
			body = keyTiered(r, body, model)
		}
		if from == provider.Chat {
			body = thinkingEffort(body)
			// OpenCode 1's promptCacheKey (#5468)
			body = cacheKeyCamel(body)
			// a tool Pi adds during a conversation, in a message of its
			// own: in the request's tools for every route, the relay's
			// and every check that reads them (#2322)
			body = liftMessageTools(body)
			// a tool in MCP's, Anthropic's or Responses' shape, which
			// no Chat API takes, in the Chat shape for every route (#2622)
			body = shapeChatTools(body)
		}
		// Claude Code's headers, for a test asked as Claude Code to ask
		// with (provider.TestAs), whatever serves this model: a
		// subscription's, a Chat provider's or a translated one's request
		// never takes them on, and Claude Code may never be on a relay's
		// Messages model before the test is asked (neiko on Discord)
		if from == provider.Anthropic && claudeCodeAsks(r) {
			seen := http.Header{}
			for k, vs := range r.Header {
				if claudeCodeHeader(k) {
					seen[k] = vs
				}
			}
			// Claude Code's own, from another magpie too
			seen.Set("User-Agent", agentUA(r.Context(), r.Header))
			provider.SawClient(provider.ClientClaudeCode, seen)
		}
		// Codex with magpie as its provider asks for a thread's title
		// here (#743)
		if from == provider.Responses {
			// on the conversation's model, not the one the app chose
			// for it (codexSideTo)
			session := func() string { return s.codexSideSession(r, body) }
			if to := codexTitlesTo(r.Header, body, true, session); to != "" {
				s.codexTitle(w, r, body, to)
				return
			}
			if to := codexDescriptionsTo(r.Header, body, true, session); to != "" {
				s.codexDescription(w, r, body, to)
				return
			}
			if to := codexSummariesTo(r.Header, body, true, session); to != "" {
				s.codexSummary(w, r, body, to)
				return
			}
			if to := codexCommitsTo(r.Header, body, true, session); to != "" {
				s.codexCommit(w, r, body, to)
				return
			}
			if to := codexPredictionsTo(r.Header, body, true, session); to != "" {
				s.codexPrediction(w, r, body, to)
				return
			}
			// Codex compacts remotely, a compaction_trigger here, on a
			// provider table named "OpenAI" (CC Switch's relay tables,
			// which magpie points here, #1301): the summary is magpie's to
			// make, as on CodexPath, and magpie's summaries go back as text;
			// a provider set to compact natively is asked Codex's own (#1099)
			if bytes.Contains(body, []byte(`"compaction`)) {
				var compact bool
				if body, compact = codexInputAs(body, true, s.nativeCompactionFor(modelOf(body))); compact {
					s.codexCompact(w, r, body)
					return
				}
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
	noteAsked(r.Context(), model)
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
		if countOff(w, provider.Gemini, model) { // nor a disabled one (#1867)
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
	if decodeJSON(body, &wrap) == nil && len(wrap.Inner) > 0 {
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
	// a client's "off" is magpie's none, for every route and check below
	// that reads the body's effort (plugins#334)
	body = offAsNone(from, body)
	// inline base64 goes in the standard alphabet, padded, which every
	// vendor reads, on every route below and for every check that reads
	// an image's bytes (base64_input.go, #5697)
	body = stdBase64Body(from, body)
	// a HEIC photo goes as JPEG (or PNG), which every vendor takes, on
	// every route below (heic.go)
	body = heicAsJPEG(from, body)
	// the upstream names in force for this request, read once here rather
	// than once per place a name is looked up below
	r = withWires(r)
	// secrets go as placeholders and come back as they were; the log has
	// what the vendor saw and said. plain is the body as the agent wrote
	// it, for a request that goes only where the user said it may go
	// unmasked (unredactedRoute). The body is masked once the route is
	// known (below): one that goes as written isn't masked on its way,
	// where masking it took up to minutes for nothing (#3973)
	plain := body
	var maskedHits maskHits
	maskDone, sentWhole := false, false
	unmask := func() {}
	askedFor := modelOf(body) // as it reaches serve, which an error in its stream names (keptAlive)
	// the reply's model the one asked for when it went out under another
	// name, or the member that answered, when asked for (#822). Both
	// writers hold a whole body until the handler is done, the namer
	// outside: the values are put back first, into the namer, and then it
	// writes what it holds. The other way round it wrote nothing, and the
	// values came back after it, into a buffer no one wrote: a 200 with
	// an empty body whenever a value was masked (#4129)
	w, r, named := withMemberNames(w, r, from, askedFor)
	defer func() {
		unmask()
		named()
	}()
	// an observer may want the bodies uncut (the OTLP export, #538): the
	// request is already whole in memory, so keep it as it came, and the
	// reply goes to a spool as it streams
	wholeBodies := usage.ObservedBodies() == usage.WholeBodies
	// a model asked for at an effort of its own (a Claude Code tier, #536)
	// is the model, every try of it asked for that effort
	asked, askedEffort := askedAt(askedFor)
	if askedEffort != "" {
		body = rewriteModel(body, asked)
		plain = rewriteModel(plain, asked)
	}
	// the body as the agent wrote it is rarely the one read (requestModel,
	// spawnAgentModels and the Lite lift each write a new one), and its
	// turn, folder and git are read from it two or three times (affine,
	// dirOf, a pause by folder): held, one parse serves them all
	// (factsOf), where each read parsed the whole conversation again,
	// ~27 MB a time for a 4 MB Codex request (XiaoYin_OK on X)
	holdBody(r.Context(), plain)
	capture := &captureResponseWriter{ResponseWriter: w}
	if wholeBodies {
		capture.whole = &spool{limit: wholeBodyLimit, pattern: "magpie-whole-*"}
	}
	w = capture
	// the agent it is recorded as, the one on the computer it was passed
	// on from for a remote magpie's request; agent the one that sent it,
	// which what is done with it goes by
	who, agent := callerOf(r), agentOf(r)
	metadata := requestSessionMetadata(r.Header, body)
	var clientResponseID string // only a reply released to the client owns this ID
	call := Call{Time: start, From: from, Model: unprefixed(modelOf(body)), Agent: who.agent, Via: who.via, Kind: requestCallKind(r.Header, metadata),
		wire: archiving(r, capture, start, nil), headers: headerLogOf(r.Context())}
	// an agent compacting its conversation is recorded as the compaction
	// it is, as Codex's are by their header: Claude Code's compaction is a
	// call its transcript never records (skipTranscript), so this row is
	// the only place it is counted, and as a plain turn it read as no
	// compaction at all (shangyuan0818 on Discord), the OTel export left
	// it to that transcript, and its reply ended the agent's turn
	if call.Kind == "" && factsOf(from, plain).compact {
		call.Kind = KindCompact
	}
	// Claude Code's auto mode classifier and AgentSummary name themselves
	// nowhere: told by their content, recorded as inferred
	if call.Kind == "" {
		call.Kind = inferredKind(r.Header, from, body)
	}
	// the reply counted as it reaches the agent, for the Routing page
	// while it is out (#4951); learned from once it ends
	capture.live = liveBegin(inflightOf(r.Context()), call.Model, from)
	defer func() { capture.live.finish(call.Status >= 400, call.Usage.Output) }()
	// the log has the body as it went: masked, or as written where it
	// went so
	logBody := func(b []byte) {
		call.RequestBody, call.RequestTruncated = captureRequestBody(b)
		if wholeBodies {
			call.wholeIn = b
		}
		if call.wire != nil {
			call.wire.body = b
		}
	}
	defer discardArchive(capture)
	r, watched := observeRequest(r, call.Kind, body)
	// what the request's tries send up to the vendor is summed in its
	// context, where forwardOnce and codexBackend add each try's bytes,
	// for the ledger row to read (#4456)
	r = r.WithContext(s.countBytes(r.Context()))
	defer func() { watched.end(call, time.Since(start).Milliseconds()) }()
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
		if capture.whole != nil {
			call.wholeOutCut = capture.whole.cut()
			if b, err := capture.whole.read(); err == nil {
				call.wholeOut = b
			}
		}
	}
	// the usage row of a request turned away before any provider was asked:
	// the ledger says it failed and why. The recent-calls entry is the
	// usual one at the end of serve, so a caller that only wants the row
	// (a lane refusal, which returns to serve's end) does not call
	// turnedAway and record twice.
	logged := func() {
		rec := usage.Record{RouteID: call.Route, Time: start, Agent: call.Agent, Via: call.Via, Provider: call.Provider, Model: call.Model, Requested: call.Model,
			Millis: call.Millis, Status: call.Status, Rejected: true, Session: sessionOf(r.Header), NativeSession: nativeSessionOf(r.Header), Kind: call.Kind, Endpoint: endpointOf(r, from, "", provider.Provider{}, ""), Archive: call.archiveName()}
		failedWith(&rec, call.Status, call.Error, call.Usage.ErrType)
		withBodies(&rec, &call)
		appendUsage(r, rec)
	}
	// a request turned away before any provider was asked is in the log
	// as the failure it was, with the reason
	turnedAway := func() {
		if !maskDone {
			// sent nowhere: the log has it masked, or not at all
			logBody(maskedCopy(r.Context(), body))
		}
		finishCapture()
		call.Millis = time.Since(start).Milliseconds()
		s.record(call)
		logged()
	}
	// a model's id without a provider in it that names a routing group is
	// the group's, as "group/<id>" is, rather than one provider's that
	// serves it: the Routing view shows the group it went to
	asked = call.Model
	// a stand-in below that puts the request on another model says so
	// on its route, and why (Route.Moved)
	var moved *ModelMove
	move := func(to, why string) {
		if why != "" && to != "" && to != call.Model {
			moved = &ModelMove{To: to, Why: why}
		}
	}
	desktopCheck := false // Claude Desktop's check that the gateway answers
	if agent == "claude-desktop" {
		sub, _ := subagentOf(r.Header)
		probe := desktopProbe(body)
		desktopCheck = probe
		var why string
		asked, why = desktopMove(asked, body, claudeCodeAsks(r), sub != "")
		move(asked, why)
		if probe {
			// tagged on the Routing page and in the ledger as the check it
			// is: Desktop names its model itself, the first listed when no
			// Claude-named model is, so the row read as Desktop asking for
			// a model the user had left while the answer came from the
			// one chosen in its menu (cybercaius on Discord, v0.1.2212)
			if call.Kind == "" {
				call.Kind = KindDesktopCheck
			}
			// its check, asked for room every model answers in
			if b, ok := withTokenFloor(body, desktopProbeTokens); ok {
				body = b
			}
			if b, ok := withTokenFloor(plain, desktopProbeTokens); ok {
				plain = b
			}
		}
	} else if agent == ScienceID {
		asked = scienceTurn(asked, body)
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
	// a name that is a routing group's — its id or name bare, or Codex's
	// spelling of a group's model (#750) — is that group, as asked for or
	// as an agent's config names its stand-in
	grouped := func(m string) string {
		if id, ok := ownFirst(r, agent, m); ok {
			// a bare name the agent's own subscription serves: there,
			// before a group magpie found or another vendor
			return id
		}
		if id, ok := keyGroupFor(r, m); ok {
			return id
		}
		if id, ok := provider.AutoStandInFor(agent, m); ok {
			// a group magpie found, while the user has those off: its
			// model from one provider, rather than refused
			return id
		}
		return m
	}
	if m := classifierStandIn(agent, from, asked, body); m != "" {
		// Claude Code's auto mode classifier on its sonnet tier, which
		// follows a main model /model has moved: the model it moved to
		asked = grouped(at(m))
		move(m, movedClassifier)
	} else if m := claudeTierStandIn(r, agent, asked); m != "" {
		// Claude Code (or a wrapper, T3 Code) naming one of Anthropic's
		// models by its full id: the model it is set to use for that tier,
		// whether or not magpie serves the id too
		asked = grouped(at(m))
		move(m, movedClaudeTier)
	} else if g := grouped(asked); g != asked {
		asked = g
	} else if m := standIn(agent, asked); m != "" {
		// Codex's config on a group by its bare name (model = "my-group",
		// or gpt-6.1-sol for group/auto-gpt-6-1-sol): its turns reached the
		// group, and its auto-review's codex-auto-review, stood in for by
		// that same name, went on unresolved, a 404 (Adam on Discord)
		asked = grouped(at(m))
		move(m, movedStandIn)
	}
	if m, why := s.codexMemoryStandIn(r, call.Agent, agent, call.Kind, asked, body); m != "" {
		asked = m
		move(m, why)
	}
	// DSH's compaction on the model set for it, not the session's, whose
	// provider may be the one out of its limit (#5395)
	if m := dshCompactTo(r, agent, call.Kind, asked); m != "" {
		asked = m
		move(m, movedDshCompact)
	}
	// and, its provider out of its limit, on the same model from another
	// provider: DSH compacts on the model it switches away from
	if m := s.dshCompactElsewhere(r, agent, call.Kind, asked, from); m != "" {
		asked = m
		move(m, movedDshCompactRested)
	}
	// a Codex subagent on the model set for Codex's subagents, whatever
	// its lead asked for (willz on Discord), and a Claude Desktop
	// subagent's tool turn on the one set for Desktop's; one already put
	// on it by the ChatGPT backend's route is recorded as asked for the
	// model it was
	subagent := subagentPickOf(r)
	if subagent == nil {
		subagent = s.codexSubagentPick(r, agent, call.Kind, call.Model, body, false)
	}
	var desktopPick *SubagentPick
	if subagent == nil {
		desktopPick = desktopSubagentPick(r, agent, call.Model, asked, body)
		subagent = desktopPick
	}
	if subagent.Moved() {
		call.Model, asked = subagent.Asked, subagent.To
	}
	subagentEffort := desktopSubagentEffort(r, agent, body, desktopPick)
	if subagentEffort != "" {
		askedEffort = subagentEffort
	}
	// any Claude Code subagent's turn, moved or not, for the Routing page's tag
	subagentID := subagentTurnOf(r, call.Kind, body, desktopPick != nil || subagentEffort != "")
	if _, held := keyHolds(r); !held {
		// auto groups off: a bare name several vendors serve, none the
		// agent's own, goes to none of them (freefcw on X)
		if ids := provider.OffSpread(agent, asked); len(ids) > 0 {
			call.Status, call.Error = 400, "model on several providers"
			writeError(w, from, 400, offSpreadRefused(call.Model, ids))
			turnedAway()
			return
		}
	}
	p, model, ok := provider.Resolve(asked)
	// a model the user disabled (#1867) — asked by its id, the model a
	// sub-agent was put on, a group with every model in it disabled — is
	// refused here, before anything is sent anywhere; a group with one
	// left goes on without the rest (provider.membersIn)
	if msg, off := idOff(asked); off {
		call.Status, call.Error = 403, "model disabled"
		writeError(w, from, 403, msg)
		turnedAway()
		return
	}
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
		msg := unknownModel(call.Model, "")
		if g, ok := provider.DisabledGroup(asked); ok {
			call.Error = "group switched off"
			msg = fmt.Sprintf("the routing group %s is switched off in Magpie, so %q is not served; switch it on again on Magpie's Routing page to use it", g.Name, call.Model)
		} else if g, _, ok := provider.FindGroup(asked); ok && g.Decides() {
			// a group of decision models answers System One, not this
			call.Status, call.Error = 400, "a decision group"
			writeError(w, from, 400, decisionGroupRefused(g))
			turnedAway()
			return
		} else if g, ok := emptyGroup(asked); ok {
			// a group of its own with nothing in it now — its patterns
			// match no model served (#766) — said so, not the whole list
			msg = emptyGroupError(g)
		}
		writeError(w, from, 404, msg)
		turnedAway()
		return
	}
	if p.DecidesModel(model) {
		// Jev answers questions about a message, not the message
		call.Status, call.Error = 400, "a decision model"
		writeError(w, from, 400, decisionModelRefused(call.Model))
		turnedAway()
		return
	}
	// a routing group's rules pick the member that goes first, looked at
	// before any image is taken out of the request: one may be for images
	g, ms, isGroup := provider.FindGroup(asked)
	g = g.Live() // a manual group's rules wait
	// a model on this machine or the local network that the user set to
	// go unmasked gets the request as written (lc on Discord), and it
	// isn't masked on its way, the request archive on or not: the archive
	// keeps it as it went, its secrets taken out as every archived body's
	// are, and an observer that wants the bodies is handed a copy masked
	// when it is first handed one, after the try (maskIn, #3973)
	if sentWhole = unredactedRoute(p, model, isGroup, ms); !sentWhole {
		if fp, before := maskedBefore(r); before {
			// masked by the magpie that passed it on, by the same
			// settings: it goes on as it came, that magpie puts its
			// values back, and a further magpie is told so too
			r = withMaskedAs(r, fp)
		} else {
			rw, b, done, hits, err := redactedHits(maskedFor(r.Context(), p, model, isGroup, ms), capture.ResponseWriter, body)
			if err != nil { // fails closed: never sent unmasked
				log.Printf("gateway: %v", err)
				call.Status, call.Error = maskStatus(err), err.Error()
				writeError(w, from, call.Status, err.Error())
				return
			}
			if to := maskRouteFor(r, hits); to != "" {
				// masking would change it, and the user set a model of
				// their own for that (Settings › Privacy, jessyoungyu on
				// X): it goes there as written, the masked copy and its
				// writer let go unused, and on from there only where
				// unmasked requests may go; its failure is the answer
				log.Printf("gateway: %d value(s) masking would change, sent as written to %s (Settings › Privacy)", hits.n, to)
				p, model, _ = provider.Resolve(to)
				g, ms, isGroup = provider.FindGroup(to)
				g = g.Live()
				sentWhole = true
				move(to, movedPrivacy)
			} else {
				capture.ResponseWriter, body, unmask, maskedHits = rw, b, done, hits
				r = withMaskedAs(r, ownMaskedAs(r.Context()))
			}
		}
	}
	if sentWhole && usage.ObservedBodies() != usage.NoBodies {
		call.maskIn = &laterMasked{ctx: context.WithoutCancel(r.Context()), plain: body}
	}
	maskDone = true
	logBody(body)
	// a routing group held to some accounts holds the request to them
	// from here on, as a gateway key's accounts do
	if isGroup {
		r = heldByGroup(r, g)
	}
	// a gateway key held to some models (#882) is refused another, or a
	// group it wasn't given (groupAllowed)
	keyWho, keyHeld := keyHolds(r)
	_, accHeld := accountHolds(r)
	chose, _ := r.Context().Value(magpieChoseKey{}).(bool)
	if isGroup && !groupGiven(r, keyWho, keyHeld, g, ms) || keyHeld && !isGroup && !modelAllowed(keyWho, p, model) {
		// the model the key was checked against is named, not the one the
		// client sent: an agent that stands a request on another model
		// (Desktop's check on its list's first, a stand-in, a tier) was
		// refused for that one, and the sent one the key may well hold
		// (#4175); a group as the key's lists name it (its ref), a model
		// as provider/model, which a privacy move may have changed since
		checked := p.ID + "/" + model
		if isGroup {
			checked = g.Ref()
		}
		st, msg := keyModelRefusal(w, keyWho, checked)
		call.Status, call.Error = st, "model not allowed for the gateway key"
		if st == http.StatusTooManyRequests {
			call.Error, call.Usage.ErrType = msg, "gateway_key_limit"
		}
		writeError(w, from, st, msg)
		turnedAway()
		return
	}
	// a model a pause rule holds for now is out of the group: not first,
	// not a failover, not where the conversation was (John on Discord: a
	// model paused in its vendor's peak hours)
	var paused []provider.Paused
	if isGroup {
		// a pause by folder (#1606) reads the folder the agent says it
		// works in; one that says none is outside every folder
		folder := ""
		if provider.PausesByFolder(g, ms) {
			folder = factsOf(from, body).dir
		}
		ms, paused = provider.PausedOut(g, ms, agent, folder, ruleClock())
		if len(ms) == 0 && len(paused) > 0 {
			call.Status, call.Error = 503, "every model paused"
			writeError(w, from, 503, pausedError(call.Model, paused))
			turnedAway()
			return
		}
	}
	// a Codex subagent's task its lead sealed — the lead answered by a
	// ChatGPT account, the group's own or Codex's — goes only to a ChatGPT
	// account, the lead's first (#619), or back to the Responses provider
	// that answered the lead, a relay of the ChatGPT backend (#1109); a
	// lead's turn with its subagent's sealed reply in it, back to the one
	// that has answered the lead's conversation (#1237); with none, it is
	// turned away before anyone is asked
	sealedTask := from == provider.Responses && hasSealedAgentMessage(body)
	sealedLead := ""
	var sealers []string
	if sealedTask {
		parent := metadata.Parent
		if parent == "" {
			parent = r.Header.Get("x-codex-parent-thread-id")
		}
		scope := p.ID + "/" + model
		if isGroup {
			scope = provider.GroupPrefix + g.ID
		}
		conv := conversationID(r.Header, body)
		sealedLead = leadProvider(scope, parent)
		sealers = []string{sealedLead, leadProvider(scope, affinityID(r.Header, body))}
		readable := func() bool {
			return isGroup && slices.ContainsFunc(ms, func(m provider.Member) bool { return s.sealedReader(m.Provider, m.Model, sealers) }) || !isGroup && s.sealedReader(p, model, sealers)
		}
		if !readable() {
			// affinity let the lead go (a day since, a restart, its reply
			// broken off): the usage log still has who answered it (#1367)
			logged := loggedAnswerers(parent, conv)
			sealers = append(sealers, logged...)
			if sealedLead == "" && len(logged) > 0 {
				sealedLead = logged[0]
			}
		}
		if sealedLead == "" {
			sealedLead = sealers[1] // a lead's own turn: who answered it
		}
		if !readable() {
			// a sealed message, not a task, is left out with a note in its
			// place: the lead or subagent it was sent to goes on where it
			// is, and the sender's final result still reaches its lead as
			// text (#1613)
			if b, ok := withoutSealedMessages(body); ok {
				body = b
				sealedTask = hasSealedAgentMessage(body)
			}
		}
		if sealedTask && !readable() {
			call.Status, call.Error = 400, "sealed subagent task"
			// a lead answered by a chat provider, then on one of Codex's
			// own models, is remembered as answered by the chat provider,
			// which seals nothing: Codex's model sealed it (plugins#70)
			earlier := ""
			if sealedLead != "" && !canSeal(sealedLead) {
				earlier, sealedLead = sealedLead, ""
			}
			writeError(w, from, 400, sealedTaskError(call.Model, sealedLead, earlier))
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
	//
	// Nor does Claude Desktop's check (desktopProbe): it is no user's turn,
	// and it has 10 seconds to be answered as Desktop starts (8 from Test
	// connection). Asked of a group with intent rules or Effort auto, its
	// "." waited on the classifier first, up to the Classifier timeout's 8
	// seconds, so the first check after a start was cancelled as a 499
	// and the second, its verdict kept or the classifier resting, passed
	// (CyberCaius on Discord, v0.1.1847). It goes by the group's order,
	// as a model's check does.
	var ask classifier
	if r.Header.Get("User-Agent") != RouterAgent && !desktopCheck {
		// a wait magpie knows of, before a route is begun (unrouted.go),
		// one for every group the request goes through
		ask = s.askWaited(r.Context())
	}
	if isGroup && slices.ContainsFunc(ms, func(m provider.Member) bool {
		return g.Ruled() || slices.ContainsFunc(m.Via, provider.Group.Ruled)
	}) {
		if req, err := parse(from, body); err == nil {
			ruleReq, ruleAt, words = req, ruleKey(g, r.Header, req), firstWords(req)
			ruleReq.subagent = subOf(r.Header) != "" ||
				(agent == "codex" || fromCodex(r.Header)) && usage.PurposeOf(call.Kind) == "kind:collab_spawn"
		}
	}
	if ruleReq != nil && g.Ruled() {
		hit = ruleFor(ruleAt, g, ms, ruleReq, agent, ask, "")
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
	//
	// One that turned an image away since magpie started is text-only
	// whatever its list or models.dev says, with a model to describe the
	// image: the model picked is given the description, not left to fail
	// over to the same model at another provider that sees (xyenon)
	unknownSight := false
	if !isGroup && (imageInput == nil || *imageInput) && hasImage(from, body) && s.blindTo(p.ID, model, imageInput) {
		if _, ok := seeing(); ok {
			no := false
			imageInput, unknownSight = &no, imageInput == nil && !s.sightRefused(p.ID, model)
		}
	}
	if imageInput != nil && !*imageInput {
		var currentImage bool
		if see, ok := seeing(); ok && hasImage(from, body) {
			seen, err := s.seenBody(withDescribeFor(r.Context(), call.Agent, call.Model, sessionOf(r.Header), unknownSight), from, body, see)
			if err != nil {
				call.Status, call.Error = 502, "image not described"
				if r.Context().Err() != nil {
					call.Status, call.Error = canceled(r.Context())
				}
				s.notDescribed(r, &call, start)
				writeError(w, from, 502, fmt.Sprintf("model %q can't see images, and %s couldn't describe the image for it: %v%s%s", call.Model, see, err, missingSeerNote(), autoSeerNote(see)))
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
			group := ""
			if isGroup {
				group = g.Name
			}
			writeError(w, from, 400, fmt.Sprintf("model %q does not support image input%s%s", call.Model, textOnlyNote(p, model, group), noSeerNote()))
			turnedAway()
			return
		}
	}
	// a video for a model its list says takes none is turned away, not
	// left out for the model to answer the words alone (#6135); a group's
	// members are picked by it below
	if !isGroup && bytes.Contains(body, []byte("video")) && noVideo(p.ID, model) && hasCurrentVideo(from, body) {
		call.Status, call.Error = 400, "model does not support video input"
		writeError(w, from, 400, videoRefused(call.Model, ""))
		turnedAway()
		return
	}
	// a careful provider's capped accounts read again now where what is
	// known of their usage is too old to send on (plugins#349)
	if isGroup {
		if ps, models := carefulOf(p, model, ms); anyCareful(ps) {
			readCareful(r.Context(), ps, models)
		}
	} else if p.CapCareful || len(p.Fallback) > 0 {
		if ps, models := carefulOf(p, model, nil); anyCareful(ps) {
			readCareful(r.Context(), ps, models)
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
	scope, mode, rotate := p.ID+"/"+model, p.Affinity, routingOf(p.Routing) == provider.Rotate
	spread := spreadsSessions(mode, p.Routing)
	leadScope := scope // where the lead of a subagent is kept
	if isGroup {
		// a routing group: every member's keys or accounts weighed together
		cands, pl = s.planGroup(g, ms, from)
		group = groupRef(g, ms)
		group.Paused = paused
		scope, mode, rotate = provider.GroupPrefix+g.ID, g.Affinity, routingOf(g.Routing) == provider.Rotate
		spread = spreadsSessions(mode, g.Routing)
		leadScope = scope
		if words != "" {
			// a subagent a rule sends elsewhere mustn't take its agent's
			// conversation with it: each keeps where it is on its own
			scope += "|" + words
		}
	} else {
		// a routing group among the fallbacks is routed as a request to it
		// would be, its pause rules too (pausedOut's)
		pausedIn := func(fg provider.Group, fms []provider.Member) []provider.Member {
			folder := ""
			if provider.PausesByFolder(fg, fms) {
				folder = factsOf(from, body).dir
			}
			kept, _ := provider.PausedOut(fg, fms, agent, folder, ruleClock())
			return kept
		}
		cands, pl = s.planPaused(p, model, from, pausedIn)
		if len(cands) == 0 && len(pl.capped) > 0 && !p.CapStop && !namedProvider(r, call.Model) || handedOnOf(r) != nil {
			// every account held at the usage cap the user set, or Codex's
			// own sign-in handed on (codexFallBack), and no fallback left:
			// the groups the user built with this model in them go on from
			// it, as a request to the group would (the Codex app asks
			// Codex's own model, never the group). Not for a request that
			// named the provider itself ("claude/…", another magpie's
			// call): that one is answered the cap's 429 unless the
			// provider has fallbacks of its own (Anotiaww on X)
			// Codex's auto-review on codex-auto-review, which no group has,
			// goes on where its conversation's turns go (codexReviewAfter)
			if q, ok := groupsAfter(p, model, s.codexReviewAfter(r, p, model, body)...); ok {
				cands, pl = s.planPaused(q, model, from, pausedIn)
			}
		}
		if handedOnOf(r) != nil {
			// Codex's own sign-in, asked as often as it is asked: only
			// the fallbacks are left (codexFallBack)
			cands, pl = fallbacksOnly(cands, pl, p.ID, model)
			if lead := s.codexReviewLead(r, p, model, body); lead != "" && !strings.Contains(lead, "/") {
				// and an auto-review's, on the conversation's model through
				// that same sign-in, in a group it goes on to
				cands, pl = fallbacksOnly(cands, pl, p.ID, lead)
			}
		}
		if provider.IsFastPick(agent, p.ID+"/"+model) {
			// the agent's pick of it is sent in its vendor's fast mode
			// (#954), as a group's member may be; its fallbacks, other
			// models, go as they are
			fastPick(cands, pl, p.ID, model, provider.IsUltraPick(agent, p.ID+"/"+model))
		} else if tier := provider.ModelTier(p.ID, model); tier != "" {
			// the speed the user gave the model in its Model settings, for
			// every agent that has no pick of its own for it
			fastPick(cands, pl, p.ID, model, tier == "ultrafast")
		}
	}
	agentPick := provider.AgentEffort(agent)
	if sealedTask {
		cands, pl = s.sealedReaders(cands, pl, sealers)
	}
	// a compaction a provider set to compact natively sealed goes only to
	// the key or account that sealed it, or is refused saying so: never
	// to another, to be refused, dropped or summarised (#1099). Codex's
	// trigger goes only where it can be asked as Codex sent it.
	var checkpoint *checkpointWriter
	if from == provider.Responses {
		if cp, ok := checkpointOf(body); ok {
			checkpoint = &cp
			kept, keptPl, left := checkpointReaders(cands, pl, cp)
			if !left && compactionAside(call.Kind, asked) {
				// a call beside the conversation (an approval's review, a
				// memory, a subagent) on a model its writer doesn't serve:
				// asked without the seal, which no other can read (echo_ts:
				// a Copilot compaction sent to codex-auto-review, refused
				// as invalid_encrypted_content every review). The
				// conversation's own turns are still held to the writer
				if b, ok := withoutCompaction(body); ok {
					body, checkpoint, left = b, nil, true
					kept, keptPl = cands, pl
					if p, ok := withoutCompaction(plain); ok {
						plain = p
					}
				}
			}
			cands, pl = kept, keptPl
			if !left {
				call.Status, call.Error = 400, "native compaction sealed elsewhere"
				writeError(w, from, 400, checkpointError(call.Model, cp))
				turnedAway()
				return
			}
		}
		if compactionTriggerIn(body) {
			var left bool
			if cands, pl, left = s.compactionCarriers(cands, pl); !left {
				call.Status, call.Error = 400, "native compaction not carried"
				writeError(w, from, 400, carrierError(call.Model))
				turnedAway()
				return
			}
		}
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
		spent, anySpent := spentAmong(keyWho, cands)
		cands, pl, accountHeld = allowedCandidates(who, cands, pl, members)
		if len(cands) == 0 && anySpent {
			// every model left is one a spent limit of the key's counts
			// (zola_xynb): said as the limit, not as a model gone
			st, msg := spentRefusal(w, spent)
			call.Status, call.Error, call.Usage.ErrType = st, msg, "gateway_key_limit"
			writeError(w, from, st, msg)
			turnedAway()
			return
		}
	}
	// a conversation that stays strictly where it is goes on on its
	// account past the usage cap set on it, and is refused there, not
	// moved, while the account is held as it won't spend its credits
	// (#1797)
	cands, pl, strictHeld := strictKeep(scope, mode, r.Header, body, cands, pl)
	// a Codex reset spent before any try, on an account held as it won't
	// spend its credits
	var resetFirst *AutoReset
	if len(cands) == 0 && len(pl.held) > 0 && strings.TrimSpace(r.Header.Get(AccountHeader)) == "" &&
		(strictHeld != nil || !slices.ContainsFunc(pl.left, func(w Weighed) bool { return !w.Barred && !w.Held && w.Capped == 0 && !w.StandIn })) {
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
	if len(cands) == 0 && (strictHeld != nil || slices.ContainsFunc(pl.left, func(w Weighed) bool { return w.Capped > 0 }) &&
		!slices.ContainsFunc(pl.left, func(w Weighed) bool { return !w.Barred && !w.Held && w.Capped == 0 && !w.StandIn })) {
		// every account there is is held at its usage cap: used up, as far
		// as routing goes, until a window renews — or the one a strict
		// conversation stays on is
		held := pl.left
		if strictHeld != nil {
			held = strictHeld
		}
		var msg, record string
		var back time.Time
		if sealer := checkpointHeld(checkpoint, held, pl); sealer != nil {
			// only the account that sealed the conversation's native
			// compaction was a candidate, and it is held: the others
			// weren't tried for that, not for being held too (#2517)
			msg, back = checkpointCappedError(call.Model, *checkpoint, sealer, time.Now())
			record = checkpointCappedRecord(sealer)
		} else {
			msg, back = cappedError(call.Model, held, time.Now())
			if strictHeld != nil {
				msg += ". The conversation stays strictly on that account (Stays: Strict), so magpie doesn't move it to another"
			}
			record = cappedRecord(held)
		}
		if d := time.Until(back); d > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(d.Seconds())+1))
		}
		call.Status, call.Error = 429, record
		writeError(w, from, 429, msg)
		turnedAway()
		return
	}
	if len(cands) == 0 && accountHeld {
		// every account or key the model had is one the calling key may
		// not use (#905), the plan's left saying so: told before the barred
		// and ready ones it may also have
		call.Status, call.Error = 403, "every account outside the key's accounts"
		if keyWho.Group != "" {
			call.Error = "every account outside the group's accounts"
		}
		writeError(w, from, 403, keyAccountsError(keyWho, call.Model))
		turnedAway()
		return
	}
	if len(cands) == 0 && slices.ContainsFunc(pl.left, func(w Weighed) bool { return w.HeldBy != "" }) &&
		!slices.ContainsFunc(pl.left, func(w Weighed) bool { return !w.Barred && !w.Held && w.Capped == 0 && !w.StandIn }) {
		// every one left was held by a group in the group, or a group a
		// fallback reached, held to other accounts
		call.Status, call.Error = 403, "every account outside the group's accounts"
		writeError(w, from, 403, groupsHeldError(call.Model, pl.left))
		turnedAway()
		return
	}
	if len(cands) == 0 && slices.ContainsFunc(pl.left, func(w Weighed) bool { return w.Barred }) && !slices.ContainsFunc(pl.left, func(w Weighed) bool { return !w.Barred && !w.StandIn }) {
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
	before := len(cands)
	inOrder := routingOf(p.Routing) == provider.Ordered
	if isGroup {
		inOrder = routingOf(g.Routing) == provider.Ordered
	}
	cands, pl, aff, stuck := affine(scope, mode, rotate, spread, inOrder, hit != nil && hit.Ended > 0, r.Header, from, body, plain, cands, pl)
	// a strict conversation left with its own account of several: its
	// failure rests the account as it would with the others still there,
	// for new conversations to go elsewhere meanwhile (#1797)
	strictAlone := aff.Why == "strict" && len(cands) < before
	defer opened(stuck)
	leadAccount := ""
	if sealedTask && !aff.Kept {
		parent := metadata.Parent
		if parent == "" {
			parent = r.Header.Get("x-codex-parent-thread-id")
		}
		cands, pl, leadAccount = leadFirst(leadScope, parent, cands, pl)
	}
	if hit != nil && hit.Use != "" {
		// the rule's member first, over whoever answered last, as a turn
		// begins; within it, whoever answered last stays while it is of
		// the rule's member, the member's others behind it (#1946)
		cands, pl = ruleOver(hit, ms, cands, pl, aff)
	}
	// the groups in the group as the rule's has them: a model it shares
	// with a group ahead of it is its, for its rules (#2361)
	ruled := ms
	if led := ruleLed(hit, ms, cands); strings.HasPrefix(led, provider.GroupPrefix) {
		ruled = homed(ms, []string{led})
	}
	// the rules of the groups in the group, down the one that goes first
	var nested []NestedRule
	var nestedAt []string
	if ruleReq != nil && !modelRuled(hit, ms, cands) {
		picked := ""
		if hit != nil {
			picked = hit.Pick
		}
		nested, nestedAt, cands, pl = s.nestedRules(ruleAt, ruleReq, agent, ask, ruled, cands, pl, aff, picked)
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
	// groupUnknown is whether the member the images are first described
	// for is counted text-only for knowing nothing of it (CallFor.Unknown)
	groupUnknown := false
	if isGroup && hasImage(from, body) {
		if see, ok := seeing(); ok {
			seenGroup = sync.OnceValues(func() ([]byte, error) {
				return s.seenBody(withDescribeFor(r.Context(), call.Agent, call.Model, sessionOf(r.Header), groupUnknown), from, body, see)
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
				case !s.blindTo(c.p.ID, c.model, in):
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
				writeError(w, from, 400, fmt.Sprintf("model %q does not support image input%s%s", call.Model, textOnlyNote(p, model, g.Name), noSeerNote()))
				turnedAway()
				return
			}
		}
	}
	if isGroup && hasCurrentVideo(from, body) {
		// a video goes only to the members that take one, else to those
		// magpie knows nothing of, else is turned away: a member that takes
		// none would answer the words alone (#6135)
		var takes, unknown []candidate
		var takesAt, unknownAt []Weighed
		for i, c := range cands {
			switch v := videoInput(c.p.ID, c.model); {
			case v == nil:
				unknown, unknownAt = append(unknown, c), append(unknownAt, pl.order[i])
			case *v:
				takes, takesAt = append(takes, c), append(takesAt, pl.order[i])
			}
		}
		cands, pl.order = takes, takesAt
		if len(takes) == 0 {
			cands, pl.order = unknown, unknownAt
		}
		if len(cands) == 0 {
			call.Status, call.Error = 400, "model does not support video input"
			writeError(w, from, 400, videoRefused(call.Model, g.Name))
			turnedAway()
			return
		}
	}
	pin := strings.TrimSpace(r.Header.Get(AccountHeader))
	if pin != "" {
		var status int
		var msg string
		if cands, pl, status, msg = pinTo(pin, cands, pl); status != 0 {
			call.Status, call.Error = status, "account pinned: "+pinnedName(pin)
			writeError(w, from, status, msg)
			finishCapture()
			s.record(call)
			return
		}
	}
	// a member whose model is known to take less than the request isn't
	// asked: it would only refuse it. Those left out are the last resort,
	// once every one with room is out of its allowance (small, below)
	cands, pl, small, smallW := roomy(cands, pl, from, body)
	// a member whose model refused this very conversation as too long at
	// no more than this size is asked last: what it refused once the
	// grown conversation is refused again, and the member that answered
	// is asked first (#3453)
	cands, pl = behindOverflow(stuck, cands, pl, len(body))
	// requests that came together go to as many keys or accounts as are
	// alike for them, each counted where it goes until it ends (#5242);
	// one kept, pinned, sealed to its account, or a new conversation
	// leastHeld spread is counted where it is
	poolRouting, poolGroup := p.Routing, (*provider.Group)(nil)
	if isGroup {
		poolRouting, poolGroup = g.Routing, &g
	}
	leastHeldIt := aff.Why == "first" && (rotate || spread)
	reorder := !aff.Kept && aff.Why != "strict" && pin == "" && leadAccount == "" && checkpoint == nil && !leastHeldIt
	cands, pl, unburst := burstSpread(poolRouting, poolGroup, hit != nil && hit.Use != "", reorder, cands, pl)
	defer unburst()
	// of those resting, only the one back soonest is asked (#1799); the
	// whole list still finds a Codex account to spend a reset on
	all := cands
	cands = probeOne(cands)
	var cacheReport *Request
	if wantsClientCache(r, from) && slices.ContainsFunc(cands, func(c candidate) bool { return c.p.Account != nil && c.p.Account.Agent == "claude" }) {
		var err error
		cacheReport, err = parseAnthropicRequest(body, true)
		if err != nil {
			call.Status, call.Error = 400, err.Error()
			writeError(w, from, 400, err.Error())
			turnedAway()
			return
		}
		// Tool drops are known before a queue or slow run selection can
		// send streaming headers. Message drops wait for that selection.
		cacheReport.clientCache.fresh = true
	}
	shown := aff
	if len(cands) == 1 && aff.Why != "strict" {
		shown = nil // nobody else to stay away from; strict left one alone (#1797)
	}
	link := s.titlePrompts.observe(r, body, metadata, call.Kind, start)
	turn, tools := agentTurnOf(r, call.Kind)
	if turn {
		capture.tools = tools
	}
	tierAsked := bodyTier(from, body) // the service tier the client asked for (#1784)
	tr := s.trace.beginFor(r, Route{turn: turn, dirOf: dirOf(from, plain), tools: tools, imageTurn: drawingTurnID(metadata.Turn), imageCaller: codexTurnKey(r, call.Agent), TitleLink: link, Pinned: pinnedName(pin), Time: start, Agent: call.Agent, Session: sessionOf(r.Header), Conv: convOf(r.Header, body), LastConvs: dshLastConvs(call.Agent, body), ParentSession: titleParentSession(r.Header, metadata, call.Kind), Kind: call.Kind, For: call.For, Model: call.Model, Effort: requestEffort(from, body), Tier: tierAsked, Provider: p.ID, Group: group, Rule: hit, Nested: nested, SealedTask: sealedTask, LeadAccount: leadAccount, Compaction: compactionWho(checkpoint), Subagent: subagent, Moved: moved, SubagentEffort: subagentEffort, Sub: subOf(r.Header), SubagentID: subagentID, Affinity: shown, Order: pl.order, Left: append(slices.Clip(pl.left), smallW...)})
	if watched != nil {
		watched.routeID = tr.ID
	}
	// what masking swapped, recorded for Settings → Privacy, unless the
	// request went as written. The record names the provider and model
	// the last try went to, not the group's first member, which a
	// failover may never have reached
	if !sentWhole {
		m, done := noteMaskedUntil(r, "chat", maskedHits, sentOf(maskedHits, body), tr.ID, p.ID, call.Model)
		if m != nil {
			s.trace.update(tr, func(t *Route) { t.Masked = m })
		}
		defer func() { done(call.Provider, model) }()
	}
	// the card's text is the masked body, even where the request went
	// unmasked: masked by PromptText when it went whole (#3973)
	promptRead := s.inspectPrompt(tr, from, body, body, sentWhole)
	var lastTried provider.Provider // the last try's, for its model's context window and the usage's endpoint
	var skipped []string
	sent := ""       // the reasoning the last try's model was asked for
	wentAt := ""     // and the level that went, once translated (wireEffort, #1636)
	wentTier := ""   // the service tier the last try went out at (noteWireTier, #1784)
	where := ""      // the last try's provider.Where, for the usage
	again := 0       // times the last one left has been tried again
	resealed := 0    // what of the conversation another account sealed was taken out: its reasoning, then its compaction
	repicked := 0    // times Copilot's Auto was asked again for a model the account is served
	floored := false // the reply's length raised to what the provider takes
	plainFor := ""   // the account and model asked again without effort updates (#617)
	// the account and model asked again for a reply that fits the window
	// the prompt leaves, and how long that is (roomCap)
	roomFor, roomCap := "", 0
	// the key or subscription account the last try went to (#557)
	providerKeyID, providerKeyName, providerAccount := "", "", ""
	var other *Try                          // the first failure that wasn't an allowance run out
	var forbade []forbidden                 // the plain 403s and 5xx rested so far (forbiddenAlike)
	sameRefusal := false                    // the last try's 5xx was a mate's word for word: the request's
	autoReset := resetFirst != nil          // a Codex or Claude reset looked at, once a request
	creditsAsked := map[string]bool{}       // Codex accounts asked again onto their credits, once each (creditsAgain)
	emptied := map[string]map[string]bool{} // a provider's model's accounts whose reply said nothing, this request (emptyAccounts)
	regionAsked := map[string]int{}         // a provider's model refused for where the request came from, times this request (#5182)
	// the subscription/model pairs an account refused as a model it doesn't
	// serve, for the accounts whose own list lacks it to be asked next
	// (unlistedNext); none for a request pinned to an account, a
	// conversation sealed to one or kept strictly on one
	unservedBy := map[string]bool{}
	unlisted, unlistedW := pl.unlisted, pl.unlistedW
	if pin != "" || checkpoint != nil || aff.Why == "strict" {
		unlisted, unlistedW = nil, nil
	}
	// what the tries' held streams sent the agent ahead of a reply (#751)
	kept := &keptAlive{proto: from, model: askedFor, told: start, whole: wantsWhole(r.Context()), clientEnd: clientEnd(r.Header, start)}
	if k := handedOnOf(r); k != nil && !kept.whole {
		// Codex's own sign-in waited for first, with Codex kept alive
		kept = k
	}
	var sentMs int64 // ms from the request to its answering try going to the vendor
	// of it, magpie's own, the tries that failed first and the wait for a
	// slot (usage.Record.Own, Retried, Queued: #1532)
	var ownMs, retriedMs, queuedMs int64
	var firstBegan time.Time // when the first try began
	streams := streamOf(body)
	kept.streams = streams
	kept.codex = fromCodex(r.Header) || codexForClaudeCode(r.Header)
	if from == provider.Gemini {
		streams = strings.Contains(r.URL.Path, "streamGenerateContent")
	}
	gaveWay := map[string]bool{} // keys and accounts that gave way, full, to one free (laneMate, waitMate)
	// the slot a request waiting in a full lane took of the key or account
	// it went to instead (waitMate), for that one's try; given back if it
	// isn't the one tried next, or when serve ends with it unused
	var handed struct {
		who     string
		release func()
		waited  time.Duration // how long it waited where it was, the first try's
	}
	defer func() {
		if handed.release != nil {
			handed.release()
		}
	}()
	// a group's members that failed in a way that passes are asked again,
	// once each has been, for up to its Patience (#1418): a member of a
	// group in the group uses the innermost that sets Patience (#2410)
	var patience, patienceFallback time.Duration
	if isGroup {
		patienceFallback = g.Waits()
	} else {
		// one provider asked for, not a group: its accounts busy are kept
		// for its own Patience, as long by default as a group that sets
		// none (zoecats on Discord: a relay's short 429 went straight to
		// the agent once the few same-one retries were spent)
		patienceFallback = waitsOf(p.Patience, singlePatience)
	}
	patience = maxWaits(cands, patienceFallback)
	var busyNow []busyTry  // the members of this round that failed so
	rounds := 0            // the rounds they were asked again in
	var waitFrom time.Time // when every member had first failed so
	refusals := 0          // the tries the safety filter refused so
	// a reply passed through whose reasoning looped before it said a word,
	// asked again (loopReasks); reasked, the times a translated one was
	// asked again in place, tries the route doesn't list (#2436)
	reloops, reasked := 0, 0
	// the try whose plugin ended the turn on its upstream's stream dying
	// mid-call (plugins#38), asked again where it stood (cutReasks, #2873)
	cuts := 0
	// the conversation's own key or account, kept for what its vendor
	// cached, asked once more after a moment for a failure that passes
	// before it moves (keptAgain, jiabubu)
	keptAsked := false
	// ended is when the last try's reply ended: the request's ms end there,
	// not after what magpie writes down of it once it has (#1939: sticks
	// and the provider's last answer, saved to disk, took seconds on a busy
	// disk, and the reply's speed counted them as its own)
	var ended time.Time
	// the compaction a provider set to compact natively seals, read on the
	// way to Codex, and who sealed it remembered once it has answered
	// (#1099)
	var sealedBy *candidate
	wantWhole := wantsWhole(r.Context())
	if from == provider.Responses && compactionTriggerIn(body) {
		watch := &compactionWatch{w: w}
		w = watch
		defer func() {
			if sealedBy != nil {
				if enc := watch.sealed(); enc != "" {
					wroteCheckpoint(enc, *sealedBy)
				}
			}
		}()
	}
	// the plan as it stands for the tries: whether the one that answers
	// stood in for a member listed ahead of its own (standsInFor)
	plannedCs, plannedOrder := slices.Clone(cands), pl.order
	// the row of the try before, when it failed, billed something and
	// handed the request on, written once the next try begins
	var failedRow func()
	walkBegan := time.Now() // the serial walk's start, for groupBudget (#5113)
	// the turn near its cap the try before took, given back as the next
	// begins or the request ends (cap_near.go)
	var nearDone func()
	defer func() {
		if nearDone != nil {
			nearDone()
		}
	}()
	nearBehind := map[string]bool{} // accounts near their cap moved behind the rest once
	var nearInKey time.Duration     // the key's slot waited for before an account's turn near its cap
	for i := 0; i < len(cands); i++ {
		if failedRow != nil {
			failedRow()
			failedRow = nil
		}
		if nearDone != nil {
			nearDone()
			nearDone = nil
		}
		c := cands[i]
		if q, ok := provider.Readdressed(c.p); ok {
			// a Remote magpie saved at another address since the
			// request read it is asked there, a retry of it too
			// (le13ure on X)
			c.p = q
		}
		if handed.release != nil && handed.who != c.who() {
			handed.release()
			handed.release = nil
		}
		if handed.release != nil {
			// it holds a slot of this one already: no giving way
		} else if j := s.laneMate(cands, i, isGroup, gaveWay); j > i {
			// its key or account is full, and another of its provider's,
			// or of its group, has a slot free: that one is asked first,
			// this one keeps its place after it (#892)
			gaveWay[c.who()] = true
			cs := slices.Clone(cands)
			cands = slices.Insert(slices.Delete(cs, i, i+1), j, c)
			i--
			continue
		}
		if i < len(cands)-1 {
			_, _, spent := quotaHeld(cands[i:])
			if _, _, timed := timedHeld(cands[i:]); spent || timed {
				// every key left is out of its allowance, resting until it
				// is back, or every one left is limited till a time its
				// vendor named: none is asked, and the agent is told so at
				// once (#1772; hsjzg58958 on X)
				cands = cands[:i+1]
			}
		}
		if by := accountSkipped(r.Context(), c.p, c.model); by != "" {
			// a middleware's onAccount passed the account over for this
			// request: the next is asked, as past one that can't be used,
			// before anything of this try is done (no reset spent, no rest,
			// nothing held against it)
			skipped = append(skipped, c.label()+": passed over by middleware "+by)
			if i < len(cands)-1 {
				continue
			}
			// every one left was passed over: never sent to one all the
			// same (no fail-open). The agent is told the failure of one
			// asked before, else what it is told with none ready
			msg := ""
			switch {
			case other != nil:
				call.Status, call.Error, call.Usage.ErrType = other.Status, other.Error, other.errType
				s.trace.update(tr, func(t *Route) { t.Fail = toldFail(other) })
			case call.Status < 400:
				call.Status, call.Error = 404, "every account passed over by a middleware"
				msg = fmt.Sprintf("none of %s's models is ready", call.Model)
			}
			failTo(w, kept, from, call.Status, cmp.Or(msg, call.Error))
			break
		}
		if used, near := nearCapOf(c, time.Now()); near && aff.Why != "strict" && (len(cands) > 1 || userCapped(c)) && capHeld(c.p, c.p, c.model, time.Now()) == nil {
			// within nearCapPoints of its usage cap, the account takes one
			// turn at a time (dyzdyz010 on Discord): busy, the turn goes to
			// the next one first, this one kept behind them, else waits for
			// the turn before it and is held by what that one left. A
			// conversation kept strictly on it goes on past its cap, as
			// strictKeep keeps it, and an account already held when its try
			// begins was let in past its cap on purpose (a reset spent for
			// it, its credits), so is not held again here.
			//
			// The slot of its gateway key's is taken first, as every try
			// takes it before its lane: a turn kept on this account after
			// a passing failure holds the key's slot, and one that took
			// the account's turn it gave back, then waited for the key's,
			// left each waiting for the other's till the agents hung up
			// (ox0pine on X: a key held to one provider stuck in routing)
			waited, err := keySlot(r.Context())
			nearInKey += waited
			if err != nil {
				call.Status, call.Error = canceled(r.Context())
				break
			}
			done, ok := s.nearCap.tryTake(c.who())
			if !ok && i < len(cands)-1 && !nearBehind[c.who()] {
				nearBehind[c.who()] = true
				cands = append(slices.Delete(slices.Clone(cands), i, i+1), c)
				i--
				continue
			}
			if !ok {
				// the wait is a known one (noteWait), and the Routing page
				// says the turn is queued, not that routing goes on
				// (tmr11235 on X: "路由中" until the turn before it ended)
				stopQueue := s.showQueued(r.Context(), tr)
				done, err = s.nearCap.take(r.Context(), c.who())
				stopQueue()
				if err != nil {
					// the agent went away while it waited: nothing was sent
					call.Status, call.Error = canceled(r.Context())
					break
				}
				nearCapAgain(r.Context(), c, used)
			}
			nearDone = done
			if h := capHeld(c.p, c.p, c.model, time.Now()); h != nil {
				nearDone()
				nearDone = nil
				c.capped = h
				ws := barredOf([]candidate{c}, c.p, false, from, nil)
				msg, back := cappedError(call.Model, ws, time.Now())
				skipped = append(skipped, c.label()+": "+msg)
				if i < len(cands)-1 {
					continue
				}
				// held at its cap by the turn before it: the agent is told
				// the failure of one asked before, else the cap
				if other != nil {
					call.Status, call.Error, call.Usage.ErrType = other.Status, other.Error, other.errType
					s.trace.update(tr, func(t *Route) { t.Fail = toldFail(other) })
					failTo(w, kept, from, call.Status, call.Error)
					break
				}
				if d := time.Until(back); d > 0 && !kept.sent {
					w.Header().Set("Retry-After", strconv.Itoa(int(d.Seconds())+1))
				}
				call.Status, call.Error = 429, cappedRecord(ws)
				failTo(w, kept, from, 429, msg)
				break
			}
		}
		if !autoReset && c.onCredits() {
			// going to the account on its credits, its week used up: a
			// reset it spends by itself when the week runs out goes
			// first (resetBeforeCredits)
			if out, ok := s.resetBeforeCredits(r.Context(), c); ok {
				autoReset = true
				resetFirst = &AutoReset{Who: c.p.Account.User, Text: out.Text(), Agent: c.p.Account.Agent}
			}
		}
		last := i == len(cands)-1
		// the last one's failure is held too when an earlier one failed,
		// for its allowance running out to be told as that one's error
		// and once the agent has the stream's headers from an earlier try's
		// keepalives, for a failure to be told as the stream's error
		// a provider whose errors the user leaves to the client
		// (errors=pass, ne9ko9 on Discord) is held for nothing: its reply,
		// an error too, goes to the agent as it comes, and nothing is
		// asked again, of it or of the next candidate. Only a stream the
		// agent has the headers of already is held, to be told in it
		pass := c.p.PassesErrors()
		hw := newHoldWriter(w, kept.sent || !pass && (!last || again < max(lastRetries, rateRetries) || other != nil || autoPicks(c) && repicked < 2 ||
			patience > 0 && rounds < replanRounds && (waitFrom.IsZero() || time.Since(waitFrom) < patience) || wantWhole))
		// a compaction's summary is held to its end, for a cut or an
		// error halfway to go to the next candidate (#2274)
		hw.wantWhole = wantWhole && !pass
		// Claude's and GPT's reasoning is held for a refusal after it only
		// while another candidate could answer instead: the last one's
		// refusal isn't asked again, so holding it only kept a lone relay's
		// thinking from Claude Code and Codex until the text began, all at
		// once (iTianbao on X). Gemini's goes as it comes there too: a
		// reply that only reasoned is asked again in place, its reasoning
		// kept and the next try's after it (streamTranslated, #667), so
		// holding it kept Codex on a Gemini model with nothing at all,
		// not even response.created, for the whole of its thinking
		// (LuckymanJC on Discord). DeepSeek's goes as it comes there too
		// once its member has gone on from its thinking to an answer
		// thoughtCleanMost times running (holdsThinking, #4512): its
		// filter refuses after thinking hardly ever, and held, the agent
		// waited every request's whole thinking out.
		// Claude's, GPT's and Gemini's go as they come the same way while
		// a refusal after them can still be handed over (hw.over): held,
		// a group's Claude showed Claude Code its thinking only with its
		// text, all at once, and never its "thinking with <effort>
		// effort" (#4166). Set below, once hw.over is.
		hw.ctx, hw.alive, hw.streams = r.Context(), kept, streams
		// a Claude subscription account's refusal of a big conversation
		// isn't told again to the subscription's other accounts
		// (tooBigToRetell), decided before the try, for its refusal not to
		// be handed over to nobody
		retell, noRetell := 0, false
		if !last && c.claudeAccount() && slices.ContainsFunc(cands[i+1:], candidate.claudeAccount) {
			retell, noRetell = tooBigToRetell(from, body)
		}
		if !pass && handsOver(streams, last || noRetell && len(otherThanClaudeAccounts(cands[i+1:])) == 0, wantWhole, from, refusals) {
			// reasoning that went through as it came, and a refusal after
			// it with nothing yet said: the next candidate answers in the
			// same stream (refusalHandover; yhwangl on X)
			hw.over = &refusalHandover{proto: from, seq: -1}
		}
		// the reasoning's hold, above, once the handover is known
		hw.thinkingShown = !s.holdsThinking(c, hw.over != nil) || last
		report := cacheReport
		if c.p.Account == nil || c.p.Account.Agent != "claude" {
			report = nil
		}
		cacheReportTry(hw, report)
		if streams && !settings.Load().NoLoopGuard {
			// a reply stuck in a loop is ended, not let run to its limit
			// (#1359)
			hw.loop = &loopGuard{}
			hw.reloop = reloops < loopReasks && !pass
		}
		// a member of a group in the group is timed by the innermost
		// group that sets a FirstToken (#2246), else by the group asked
		// for, which follows the default for every group when it sets none
		firstToken := c.firstToken
		if firstToken == 0 && isGroup {
			firstToken = g.FirstTokenOr()
		}
		if isGroup && firstToken > 0 && !last && streams && !pass {
			// slow to start, the next member is asked (Group.FirstToken)
			hw.firstWait = time.Duration(firstToken) * time.Second
			if left := groupBudget - time.Since(walkBegan); left < hw.firstWait {
				// the members before it used the walk's time: it gets
				// what is left, so the walk ends before the client's
				hw.firstWait, hw.budgeted = max(left, time.Millisecond), true
			}
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
		ended = time.Time{}
		if firstBegan.IsZero() {
			firstBegan = began
		}
		hw.first.start = began
		hw.first.onFirst = s.trace.firstContent(tr)
		// when the request that last went out began to (GetConn), and
		// when its body was written
		var asking, asked, wrote atomic.Int64
		var nt netTrace // where the try's time went (TryNet)
		attemptBody := body
		if from == provider.Responses {
			// what another sealed, which this one refused earlier in the
			// conversation, isn't sent to be refused again; its own is
			if b, ok := withoutRefused(stuck, c.vendor(), attemptBody); ok {
				attemptBody = b
			}
		}
		if isGroup {
			in := membersImageInput([]provider.Member{{Provider: c.p, Model: c.model}})
			if seenGroup != nil && s.blindTo(c.p.ID, c.model, in) {
				groupUnknown = in == nil
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
					if r.Context().Err() != nil {
						call.Status, call.Error = canceled(r.Context())
					}
					failTo(w, kept, from, call.Status, fmt.Sprintf("model %q can't see images, and %s couldn't describe the image for it: %v%s%s", c.p.ID+"/"+c.model, see, err, missingSeerNote(), autoSeerNote(see)))
					break
				}
				attemptBody = b
			} else if in != nil && !*in {
				attemptBody, _ = textOnlyBody(from, body) // omit images in prior turns and tool results
			}
		}
		// a middleware's onProvider changes what this provider is sent
		attemptBody = triedOn(r.Context(), c.p, c.model, attemptBody)
		// the system prompt the user gave this model goes after the
		// agent's own (model_system.go)
		attemptBody = withModelSystem(r, c.p, c.model, from, attemptBody)
		if !s.fits(c.p.ID, webpRefused, from) {
			// a provider that took a webp picture for one it can't read
			// (webp.go) has every one of its requests' webps written again
			// before they leave, its later turns not paying the refusal
			// again (#4732)
			attemptBody = webpAsJPEG(from, attemptBody)
		}
		// Codex's detail "original" goes only where it is taken (qiu_eth)
		if from == provider.Responses && !takesOriginalDetail(c.p) {
			attemptBody = originalAsHigh(attemptBody)
		}
		// a value masking swapped for a placeholder: the model is told to
		// use it as written (treeMan on Discord). A conversation's values
		// are masked again on every turn, so once one is, every later
		// turn is told the same, and its cached prefix holds
		if maskedHits.n > 0 && notesPlaceholders(c.p, from, attemptBody) {
			attemptBody = withPlaceholderNote(from, attemptBody)
		}
		if hint := s.antigravityForeseen(c.p, from, attemptBody); !last && hint != "" {
			// Antigravity would only turn it away, as it did lately, and
			// someone after it may answer: it isn't asked, and nothing is
			// held against the account (#2156). If nobody after it answers
			// but with a quota, the agent is told this, as it would be had
			// Antigravity been asked
			if other == nil {
				other = &Try{Status: turnedAwayStatus, Error: c.p.Name + ": " + hint, errType: turnedAwayErrType}
			}
			skipped = append(skipped, c.label()+": "+hint)
			continue
		}
		// a group tells agents the longest reply a member of it gives
		// (groupEntries); each member is asked for no more than its own.
		// Outside a group only the user's own Max output lowers a request
		// (userOutput): a vendor's figure may be stale, the user's is asked
		if isGroup {
			if n := outputLimit(c.p, c.model); n > 0 {
				attemptBody = withMaxOutput(from, attemptBody, n)
			}
		} else if n := userOutput(c.p, c.model); n > 0 {
			attemptBody = withMaxOutput(from, attemptBody, n)
		}
		// a request naming no reply limit, to an API that would cut it
		// short (DeepSeek's 8K), asks for the model's own, relayed or
		// translated (withOwnOutput)
		attemptBody = withOwnOutput(c.p, c.model, from, attemptBody)
		attemptBody = withThinkingRoom(c.p, c.model, from, attemptBody)
		if roomFor == c.who()+"|"+c.model {
			if b, ok := withReplyCap(from, attemptBody, roomCap); ok {
				attemptBody = b
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
		// the reasoning the rule that put this member first asks it for
		// (#2633), over the agent's, a suffix's and the turn's pick; a
		// member fixed at an effort keeps its own
		ruled := ""
		if c.effort == "" {
			if ruled = ruleEffort(c, hit, nested, ms); ruled != "" {
				fixed = ""
			}
		}
		if fixed == "" && ruled == "" && effort == "" && agentPick != "" && !agyLevelAsked(agent, from, attemptBody) && c.p.Thinks(c.model) {
			// the effort picked in magpie for an agent whose config can't
			// carry one (Cursor Private Inference, #1003), in place of the
			// level it sent, on a model that reasons; a member fixed at
			// one, a suffix and the turn's pick are more particular
			fixed = withinLevels(agentPick, levels)
		}
		// a member that sends another level than the one asked (its
		// EffortMaps, LuckymanJC on Discord: one vendor's high is
		// another's xhigh) is sent that, whoever asked: a suffix, the
		// agent's pick in magpie, the turn's pick or the agent itself. A
		// rule's effort is the user's for this member already
		mappedFrom, mapped := "", ""
		if c.effort == "" && ruled == "" && len(c.effortMap) > 0 {
			in := fixed
			if in == "" && effort != "" {
				in = withinLevels(effort, levels)
			}
			if in == "" {
				in = withinLevels(askedLevel(from, attemptBody), levels)
			}
			if to, ok := mappedLevel(c.effortMap, in); ok {
				mappedFrom, mapped = in, to
			}
		}
		if mapped != "" {
			// "none" turns its reasoning off; another level is asked at
			// the one its model has nearest, as a fixed member's is
			if mapped != "none" {
				mapped = fitLevel(mapped, c.p.Efforts(c.model))
			}
			attemptBody = withModelEffort(from, attemptBody, c.model, mapped)
		} else if ruled != "" {
			// asked even of a request that asked for no reasoning, as a
			// member fixed at an effort is; already fitted to its levels
			attemptBody = withModelEffort(from, attemptBody, c.model, ruled)
		} else if fixed != "" {
			// a member fixed at an effort is asked for it, at the level its
			// model has nearest, whatever the agent asked or the turn's
			// pick: even a request that asked for no reasoning
			attemptBody = withModelEffort(from, attemptBody, c.model, fitLevel(fixed, c.p.Efforts(c.model)))
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
		ownFast := fast && provider.OwnFast(c.p, c.model)
		if fast && (!ownFast || from == provider.Chat || from == provider.Responses) {
			// the user's own provider is never sent Claude's speed "fast"
			// in the agent's words, which a relay of theirs may turn
			// away: it is asked for priority on its OpenAI API (#1615,
			// forwardTranslated)
			attemptBody = withFast(from, attemptBody)
		}
		// a pick sent Ultrafast (#1954) asks for it in OpenAI's words; in
		// Claude's, which have none, the try carries it (withUltra)
		ultra := fast && c.ultra && !ownFast
		if ultra && (from == provider.Chat || from == provider.Responses) {
			attemptBody = withFields(attemptBody, map[string]any{"service_tier": "ultrafast"})
		}
		// a member known not to think is sent no reasoning ask (#950): the
		// group reasons for the members that do, and asking one that can't
		// is what some vendors turn away with a 400. Every other field goes
		// as the agent sent it, and which member goes first is unchanged.
		// One the user fixed at an effort is left as it is: that is the
		// user saying this model does think, over a catalog that may be
		// wrong about it (the Trae plugin's own model, #950)
		quiet := isGroup && c.effort == "" && len(c.levels) == 0 && quietTo(c.p.ID, c.model)
		if provider.Reasonless(c.p.ID, c.model) {
			// a model the user said takes no reasoning ask is sent none,
			// in a group or not, at a fixed effort or not: not even
			// reasoning turned off (yuexian7 on Discord: its vendor
			// answers reasoning_effort with 422)
			quiet = true
			attemptBody, picked = withoutAnyReasoning(from, attemptBody), false
		} else if quiet {
			attemptBody, picked = withoutReasoningAsk(from, attemptBody), false
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
		// Ultrafast goes first to an account whose plan offers it, and an
		// account whose own list doesn't is asked for priority (Fast)
		// instead, which every plan with Fast takes (yxinyu715 on X): asked
		// by the client in either of OpenAI's APIs, or by a pick (#1954)
		openAI := from == provider.Chat || from == provider.Responses
		if (openAI && askedTier(attemptBody) == "ultrafast" || ultra && from == provider.Anthropic) && !takesTier(c, "ultrafast") {
			if j := tierMate(cands[i+1:], c, "ultrafast"); j >= 0 {
				j += i + 1
				cs := slices.Clone(cands)
				cands = slices.Insert(slices.Delete(cs, i, i+1), j, c)
				i--
				continue
			}
			if openAI {
				attemptBody = withFields(attemptBody, map[string]any{"service_tier": "priority"})
			}
			ultra = false
		}
		// an effort changed mid-thread goes as an update in the history,
		// which keeps the prompt the upstream cached (#617)
		updated := false
		if from == provider.Responses && !quiet && plainFor != c.who()+"|"+c.model && takesEffortUpdates(c.p, c.model) {
			if b, ok := withEffortUpdates(stuck, c.who(), c.model, sent, attemptBody); ok {
				attemptBody, updated = b, true
			}
		}
		window := windowOf(c.p, c.model) // as the route's end reads it
		s.trace.update(tr, func(t *Route) {
			t.imageProvider = c.p.ID
			t.Tries = append(t.Tries, Try{ID: c.rest, Model: c.model, Effort: sent, Picked: picked, Fixed: fixed, RuleEffort: ruled, MappedFrom: mappedFrom, Fast: fast, Start: began})
			t.window = window
			if t.Prompt != nil && !t.Done {
				t.Prompt = livePrompt(t.Prompt, window)
			}
		})
		held := false           // answered as its vendor did a moment ago, without asking
		heldRestricted := false // held as an account its vendor restricted (restrictedHeld)
		var heldBack time.Time  // when the first of those held out of their allowance is back
		var queued int64        // ms it waited for a slot of its key's or account's
		var laneErr error       // turned away by its key's or account's queue
		at := &wireAt{}         // the level a translated request was built at
		// the models Copilot's Auto picked for it, where from, and which
		// were refused (#256)
		autoPicked := func() []provider.AutoPick { return nil }
		entryOf := func() (string, string) { return "", "" }
		if said, ok := verifyHeld(c); ok && last {
			// the account must be verified first (#152): the agent's
			// reconnects are told so again, not sent on to a vendor that
			// just refused it
			held, call.Status, call.Error = true, http.StatusForbidden, said
			noteMember(hw, r, c.p, c.model)
			writeError(hw, from, call.Status, said)
		} else if status, said, ok := restrictedHeld(c); ok && last {
			// its vendor restricted the account, and it rests so: the
			// refusal is given again without asking (restrictedHeld),
			// until the rest ends or is lifted
			held, heldRestricted, call.Status, call.Error = true, true, status, said
			noteMember(hw, r, c.p, c.model)
			writeError(hw, from, call.Status, said)
		} else if rested, back, ok := quotaHeld(cands[i:]); ok && last {
			// it rests out of its allowance, and so does every key that
			// was left (#1772: four Ollama Cloud keys out for the week,
			// each asked again, and refused, on every request): its
			// refusal is given again without asking, with when the first
			// of them is back as Retry-After, for an agent that waits as
			// told to ask then rather than at once
			msg := fmt.Sprintf("%s (out of its allowance: not asked again before %s)", rested.said, heldClock(back, time.Now()))
			held, heldBack, call.Status, call.Error = true, back, rested.Status, c.p.Name+": "+msg
			// the answer names who held it, as one asked would: a magpie
			// using this one rests that model of it alone (remoteModelRest)
			noteMember(hw, r, c.p, c.model)
			writeError(hw, from, call.Status, msg)
		} else if rested, back, ok := timedHeld(cands[i:]); ok && last {
			// it rests on a 429 till the time its vendor named, and so
			// does every one that was left (hsjzg58958 on X: limited for
			// 300 seconds, each account was asked again about every half a
			// second as the agent retried, until the vendor flagged them):
			// the agent is answered here, with when the first of them is
			// back as Retry-After. Set on the held answer too, so it isn't
			// taken for a moment's limit and asked again in a second
			// (passing) or in the patience's rounds (busy)
			said := rested.Error
			if said == "" {
				said = http.StatusText(rested.Status)
			}
			what := "rate limited"
			if rested.Why != failRate {
				what = "out of its allowance"
			}
			msg := fmt.Sprintf("%s (%s: not asked again before %s)", said, what, heldClock(back, time.Now()))
			held, heldBack, call.Status, call.Error = true, back, rested.Status, c.p.Name+": "+msg
			if wait := time.Until(back); wait > 0 {
				hw.Header().Set("Retry-After", strconv.Itoa(int((wait+time.Second-1)/time.Second)))
			}
			noteMember(hw, r, c.p, c.model)
			if rested.out != nil && from == provider.Responses {
				// a ChatGPT account used up: Codex is told as the backend
				// told it, usage_limit_reached with its plan, reset and
				// windows, which it shows and doesn't retry
				// (TestCodexEveryAccountOutToldAsTheBackend)
				writeCodexOut(hw, rested.outHeader, call.Status, msg, rested.out)
			} else {
				writeError(hw, from, call.Status, msg)
			}
		} else {
			// a stream that fails before anything is said is let go at
			// once (hw.stop), not read on until the vendor hangs up
			ctx, stop := context.WithCancel(withWireAt(r.Context(), at))
			if isGroup {
				ctx = groupTry(ctx)
			}
			if ownFast {
				ctx = withOwnFast(ctx)
			}
			if ultra && from == provider.Anthropic {
				ctx = withUltra(ctx)
			}
			if !last {
				// another candidate is asked if this one can't answer
				// now: a plugin whose vendor queues the request says so
				// at once instead of waiting in the queue (FallbackHeader)
				ctx = withFallback(ctx)
			}
			hw.stop = stop
			if sentBy(c.p) == "claude" {
				// the entry point its Claude Code run takes, for the
				// try's row (Try.Entry)
				ctx, entryOf = withEntry(ctx)
			}
			if autoPicks(c) {
				// the reply names the pick it went as last (#256: it
				// named one Copilot refused)
				p := c.p
				ctx, autoPicked = provider.WithAutoPicks(ctx, func(m string) { noteMember(hw, r, p, m) })
			}
			// when the request last went out to the vendor, its body
			// written: what came after is the vendor's (Record.Sent).
			// Before it, from when it began to go out, is connecting and
			// sending the body, and before that magpie's (#1532); and each
			// step of its way out, for the Routing page to say whose time
			// a slow turn was (TryNet)
			ctx = httptrace.WithClientTrace(ctx, nt.hooks(&asking, &asked, &wrote))
			// the headers of its vendor's reply the provider passes on go
			// with the try's own, to the agent if it is the one it gets
			// (#4145)
			ctx = withReplySink(ctx, c.p.ID, hw.heardUpstream)
			if from != provider.Responses {
				// the reasoning its calls carry that this one refused
				// earlier in the conversation isn't sent to be refused
				// again (reasoning_carry.go, #4798)
				ctx = withSealScope(ctx, c.p.ID, stuck, c.vendor())
			}
			// a key or account with a MaxConcurrency is asked once one of
			// its slots is free, in turn; the agent gone while it waits,
			// nothing is sent (the 499 below)
			// its queue full, or waited out (QueueLimit, QueueWait), it is
			// turned away below
			// queued is only the time it stood in its lane's queue and
			// waited for its minute, not the keepalive's start and stop
			// around them: a provider with no limit, or a slot and room
			// free at once, never reads as queued (#2153: 1ms on a busy
			// machine)
			// a streaming agent waiting is kept alive meanwhile
			queuedAlive := hw.keepQueued()
			var release func()
			var inLane time.Duration
			var mate candidate
			// the slot of its gateway key's first, once for the request
			// (key_slots.go): its agent gone meanwhile, nothing is sent
			inKey, err := keySlot(ctx)
			inKey, nearInKey = inKey+nearInKey, 0
			if err == nil && handed.release != nil {
				// it left a full lane's queue for this one, whose slot it
				// took as it did (waitMate): the wait there is its queue
				release, handed.release = handed.release, nil
				inLane, handed.waited = handed.waited, 0
			} else if err == nil {
				// waiting, it goes to the first other key or account with
				// room, not resting, once there is one (01huadalang on
				// Discord: the requests already waiting for a third ZCode
				// account waited on once the first two were back)
				release, inLane, err = s.lanes.take(ctx, c.who(), c.p.LaneLimit(), c.p.QueueLimit, time.Duration(c.p.QueueWait)*time.Second, func() (bool, time.Time) {
					m, rel, next, ok := s.waitMate(cands, i, isGroup, gaveWay, all)
					if !ok {
						return false, next
					}
					mate, handed.who, handed.release = m, m.who(), rel
					return true, time.Time{}
				})
			}
			if errors.Is(err, errMoved) {
				// nothing was sent, and nobody failed: the one with room is
				// asked now, this one keeps its place after it. The try
				// never went out, so the route lists it no more
				queuedAlive()
				stop()
				s.trace.update(tr, func(t *Route) { t.Tries = t.Tries[:len(t.Tries)-1] })
				if firstBegan.Equal(began) {
					// the first try's wait is the next one's queue, not a
					// failed try's time
					firstBegan, handed.waited = time.Time{}, inKey+inLane
				}
				gaveWay[c.who()] = true
				cands = movedAhead(cands, i, mate)
				i--
				continue
			}
			var inMinute time.Duration
			if err == nil && c.p.RPMLimit() > 0 {
				// with a MaxRPM, it then waits for room in the minute, and
				// goes counted in it; the requests it makes on the way
				// count too (rpm.go)
				if inMinute, err = s.rpms.wait(ctx, c.who(), c.p.RPMLimit(), rpmWait(c.p)); err != nil {
					release()
				} else {
					ctx = s.paidFor(ctx, c.p, c.who())
				}
			}
			queuedAlive()
			queued = (inKey + inLane + inMinute).Milliseconds()
			var tooMany *errRPM
			if errors.Is(err, errQueueFull) || errors.Is(err, errQueueWait) || errors.As(err, &tooMany) {
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
					defer notePhase(ctx, "upstream")()
					call.Status, call.Error = s.attempt(hw, r.WithContext(ctx), from, c.p, c.model, attemptBody, &call)
				}()
			}
			stop()
		}
		if handed.release != nil && handed.who == c.who() {
			// answered without asking (verifyHeld, restrictedHeld,
			// quotaHeld): the slot taken for it goes back
			handed.release()
			handed.release = nil
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
				// a reserve of its provider is not asked in its place: a
				// full lane fails nobody, and the reserve is held back for
				// the provider not to be used from one account more at
				// once (keepAtOnce)
				cands = dropReserves(cands, i, c.p.ID)
			}
			if i < len(cands)-1 {
				skipped = append(skipped, c.label()+": "+call.Error)
				continue
			}
			if !kept.sent {
				after := "1"
				if e := (*errRPM)(nil); errors.As(laneErr, &e) {
					after = e.retryAfter() // when the minute has room
				}
				w.Header().Set("Retry-After", after)
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
		if hw.handedOver {
			// its refusal came after its reasoning went to the agent: the
			// next try's stream follows it as the same reply
			w = spliceAfter(w, hw.over)
		}
		reasked += hw.reasked
		if hw.code() > 0 && hw.code() < 400 && !hw.refused {
			// an answer, passing or held whole: the model holds a prompt
			// this size (tooLongUnsaid)
			s.noteHeld(c.p.ID, c.model, call.Usage.prompt())
		}
		if hw.passing {
			clientResponseID = call.Usage.ResponseID
		}
		if !hw.passing && !hw.refused && modelRetired(hw.code(), hw.errBody()) {
			// the vendor's list may still name it (OpenCode Zen's
			// exo-free): it leaves the lists of this provider's models; a
			// refusal is about the request, whatever it says (#3274)
			provider.Retire(c.p.ID, c.model)
		}
		outgrew := false // a failure that didn't say so was the request's length
		if !hw.passing && !hw.refused && hw.code() >= 400 {
			if req, err := parse(from, attemptBody); err == nil {
				if msg, ok := tooLongUnsaid(c.p, c.model, estimate(req), s.heldOf(c.p.ID, c.model), hw.code(), hw.errBody()); ok {
					// told as an overflow: not asked again, of this one or
					// another, and nobody rests
					hw.failure, hw.failMsg, outgrew = http.StatusBadRequest, msg, true
				}
			}
		}
		if hw.failure != 0 { // the stream failed before any of it was sent
			call.Status, call.Error = hw.failure, c.p.Name+": "+hw.failMsg
			if hw.turnedAway {
				// Antigravity's refusal said as the stream's error event:
				// its words, and why, as its 429 is told (#1425)
				call.Error = c.p.Name + ": " + provider.APIError([]byte(hw.failMsg), hw.failMsg)
			}
		}
		if hw.turnedAway {
			s.agTurnedAway.Store(time.Now().UnixNano())
		}
		if hw.turnedAway && !strings.HasSuffix(call.Error, hw.turnedAwayHint) {
			call.Error += " — " + hw.turnedAwayHint
		}
		if hw.refused {
			// the vendor's safety filter, with nothing said (#248)
			call.Error = refusedError(c.p, c.model, hw.failMsg)
		}
		if hw.looped != "" {
			// ended for looping (#1359): the agent had its 200 and the
			// stream's error; logged apart from the vendor's own errors
			call.Status, call.Error, call.Usage.ErrType = hw.status, c.p.Name+": "+hw.looped, loopErrType
		}
		if wentAt = sent; at.set {
			wentAt = at.effort
		}
		wentTier = at.tier
		ended = time.Now()
		try := Try{ID: c.rest, Model: c.model, Effort: wentAt, Picked: picked, Fixed: fixed, RuleEffort: ruled, MappedFrom: mappedFrom, Fast: fast, Start: began, Done: true, Status: call.Status, Millis: ended.Sub(began).Milliseconds(), Error: call.Error, Queued: queued,
			Served: call.Usage.Served, Upstream: call.Usage.Upstream, Pin: c.p.ClinePassAsked(c.model), Tier: wentTier, TierServed: call.Usage.Tier, Auto: autoPicked(),
			Net: nt.timing(began, queued), SentBy: sentBy(c.p)}
		if resetFirst != nil {
			// the reset spent for it before it was asked
			try.Reset, resetFirst = resetFirst, nil
		}
		asName := provider.SentNameOnIn(wiresOf(r.Context()), c.p.ID, accountAgent(c.p), c.model, sent)
		try.Entry, try.EntryWhy = entryOf()
		try.Swapped, try.Routed = swapped(asName, call.Usage.Served) && !usage.ListedAs(c.p.ID, c.model, call.Usage.Served), usage.GroupRouted(asName, call.Usage.Served)
		try.TTFT, try.FirstText = hw.first.ms()
		try.FirstReasoning = hw.first.reasoningMs(call.To)
		try.ReasoningHidden = hw.first.hidesReasoning(call.To, call.Usage.billed().Reasoning)
		call.ReasoningHidden = try.ReasoningHidden
		try.Flow = hw.first.flowFor(call.Usage.Reasoning)
		call.Flow = try.Flow
		// the request's, from when it came as its ms are: the time before
		// this try, the ones that failed first, is in it
		call.TTFT, call.FirstText = sinceStart(began.Sub(start), hw.first.first), sinceStart(began.Sub(start), hw.first.text)
		call.FirstReasoning = 0
		if try.FirstReasoning > 0 {
			call.FirstReasoning = sinceStart(began.Sub(start), hw.first.reasoning)
		}
		// A try that fails from here and hands the request on, to another
		// account or member or the same one again, is in the ledger with
		// what its vendor billed for it, as a refusal's is: a Claude
		// subscription run that failed after Anthropic began its reply had
		// written the whole conversation to that account's cache, and the
		// next account wrote it again, while the Usage page counted the
		// second alone, "2 tries" the only sign of the first (hhxk233 and
		// Kayrainforest on X). A failure that billed nothing has no row.
		failedRow = func() {
			if call.To == "" {
				return
			}
			u := call.Usage.billed()
			if u.Input+u.Output+u.CacheRead+u.CacheWrite == 0 {
				return
			}
			rec := usage.Record{RouteID: tr.ID, Time: began, Agent: call.Agent, Via: call.Via, Provider: call.Provider, Host: where, Model: c.model,
				ProviderKeyID: providerKeyID, ProviderKeyName: providerKeyName, ProviderAccount: providerAccount,
				Requested: call.Model, Served: call.Usage.Served, Upstream: call.Usage.Upstream,
				Input: u.Input, Output: u.Output, CacheRead: u.CacheRead,
				CacheWrite: u.CacheWrite, CacheWrite1h: u.CacheWrite1h, CacheUnknown: u.cacheNotCounted(), Reasoning: u.Reasoning, Credits: call.Usage.Credits, Effort: wentAt, Millis: try.Millis, Status: call.Status,
				TierAsked: tierAsked, TierSent: wentTier, TierServed: call.Usage.Tier,
				TTFT: try.TTFT, FirstText: try.FirstText, FirstReasoning: try.FirstReasoning, ReasoningHidden: try.ReasoningHidden, Flow: try.Flow, Session: sessionOf(r.Header), NativeSession: nativeSessionOf(r.Header), Kind: call.Kind,
				RequestID: call.Usage.RequestID, Endpoint: endpointOf(r, from, call.To, c.p, c.model), Archive: call.archiveName(), HandedOn: true}
			failedWith(&rec, call.Status, call.Error, call.Usage.ErrType)
			failed := &Call{RequestBody: call.RequestBody, RequestTruncated: call.RequestTruncated, ResponseBody: string(hw.errBody()), maskIn: call.maskIn}
			if call.wholeIn != nil {
				failed.wholeIn, failed.wholeOut = call.wholeIn, hw.errBody()
			}
			withBodies(&rec, failed)
			appendUsage(r, rec)
		}
		sentMs, ownMs, retriedMs, queuedMs = 0, 0, 0, 0
		if at := wrote.Load(); at > 0 && call.TTFT > 0 {
			sentMs = min(max(time.Unix(0, at).Sub(start).Milliseconds(), 1), call.TTFT)
			ownMs, retriedMs, queuedMs = splitSent(sentMs, start, firstBegan, began, asked.Load(), queued)
		}
		if r.Context().Err() != nil && !hw.ended {
			// the agent went away: nobody failed, and nobody else is asked
			call.Status, call.Error = canceled(r.Context())
			try.Status, try.Error, try.Fail = call.Status, call.Error, failCanceled
			watched.attempt(call, try, c.p.ID, sent, hw, capture)
			s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1] = try })
			break
		}
		if hw.slow {
			// no first content in the group's FirstToken, and nothing of
			// it sent: the next member is asked, and this one doesn't
			// rest the first time, a long prompt being slow to start
			// anywhere; slow again and again, its model rests on this
			// account (restSlow)
			call.Status, call.Error = http.StatusGatewayTimeout, fmt.Sprintf("%s: no first token in %ds", c.p.Name, firstToken)
			try.Status, try.Error, try.Fail = call.Status, call.Error, failSlow
			if hw.budgeted {
				// cut short by the walk's budget, not slow of its own
				// accord: nobody rests for it
				call.Error = fmt.Sprintf("%s: no first token within the group's %ds budget", c.p.Name, int(groupBudget.Seconds()))
				try.Error = call.Error
			} else if rest, ok := restSlow(c, time.Now()); ok {
				try.Rest = &rest
			}
			call.TTFT, call.FirstText, call.Flow, call.FirstReasoning = 0, 0, 0, 0
			watched.attempt(call, try, c.p.ID, sent, hw, capture)
			s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1] = try })
			if hw.budgeted || time.Since(walkBegan) >= groupBudget {
				// the group's time is spent: no further member is asked
				// to start after the client has given up, and the error
				// names the budget (#5113)
				msg := fmt.Sprintf("no member of the group answered within its %ds budget", int(groupBudget.Seconds()))
				call.Status, call.Error = http.StatusGatewayTimeout, msg
				failTo(w, kept, from, call.Status, msg)
				break
			}
			if other == nil {
				other = &Try{Status: call.Status, Error: call.Error, errType: call.Usage.ErrType}
			}
			skipped = append(skipped, c.label()+": "+call.Error)
			continue
		}
		if hw.relooped {
			// its reasoning ran into a loop before it said a word (#1359),
			// and it was let go: asked again, once, of the same one, as a
			// translated reply is in place; asked again, it mostly doesn't
			// loop. The agent keeps what it has, and the next try's stream
			// follows it. Nobody rests: the model looped, not the account
			// (#2436: pi's Chat to WorkBuddy was ended at once)
			reloops++
			call.Error = c.p.Name + ": " + reloopedError
			try.Status, try.Error, try.Fail = call.Status, call.Error, failLoop
			call.TTFT, call.FirstText, call.Flow, call.FirstReasoning = 0, 0, 0, 0
			watched.attempt(call, try, c.p.ID, sent, hw, capture)
			s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1] = try })
			select {
			case <-time.After(retryPause):
			case <-r.Context().Done():
			}
			i--
			continue
		}
		watched.attempt(call, try, c.p.ID, sent, hw, capture)
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
		if resealed == 0 && from != provider.Responses && !hw.passing && hw.code() >= 400 && foreignReasoning.Match(hw.errBody()) {
			// the same refusal of a client of another API: the sealed
			// reasoning its calls' ids carry (reasoning_carry.go), which
			// a Responses upstream other than the one that sealed it
			// can't read, and which every later turn of the client's
			// history carries again. Asked again without it, and never
			// sent here again in the conversation (#4798)
			resealed = 2
			if refuseCarried(stuck, c.vendor(), from, attemptBody) {
				try.Fail = failForeign
				s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1] = try })
				i--
				continue
			}
		}
		if resealed < 2 && from == provider.Responses && !hw.passing && hw.code() >= 400 && foreignReasoning.Match(hw.errBody()) {
			// the conversation moved here from another account or vendor,
			// whose seals this one can't read: asked again without them
			// (its reasoning, and each message's, call's and web search's
			// sealed copy, #3887), and what it refused isn't sent here
			// again. xAI says so as a 400, or as the stream's error, which
			// may be read as another status. Refused again, or with
			// nothing else sealed to leave out, it's the compaction OpenAI
			// sealed that goes (waroy: Codex compacted on GPT, then
			// switched to Grok)
			b, ok := []byte(nil), false
			if resealed == 0 {
				resealed = 1
				if b, ok = withoutSeals(body); ok {
					refused(stuck, c.vendor(), attemptBody, b)
				}
			}
			// a compaction sealed where native compaction is on is never
			// left out: its writer's refusal goes back as it came (#1099)
			if !ok && checkpoint == nil && !s.compactsNatively(c.p, c.model) {
				resealed = 2
				if b, ok = withoutCompaction(body); ok {
					refused(stuck, c.vendor(), attemptBody, b)
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
		if !hw.passing && !s.sightRefused(c.p.ID, c.model) && imageRefused(hw.code(), hw.errBody()) && hasImage(from, attemptBody) {
			// the model turned the image away, its list or models.dev
			// having said it sees: it is text-only from now on (blindTo),
			// and with a model to describe images it is asked again with
			// them described, before any other candidate — the user picked
			// this one, and the Image recognition model is what they set
			// for it. A pick of DeepSeek-V4.1-Flash at a relay serving it
			// text-only went on to the same model at another provider
			// (xyenon). With nothing to describe them it goes on as
			// before, to a candidate that may see.
			s.refuseSight(c.p.ID, c.model)
			if isGroup && seenGroup != nil {
				try.Fail = failSight
				s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1] = try })
				i--
				continue
			}
			if see, ok := seeing(); ok && !isGroup {
				if seen, err := s.seenBody(withDescribeFor(r.Context(), call.Agent, call.Model, sessionOf(r.Header), false), from, body, see); err == nil {
					body = seen
					try.Fail = failSight
					s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1] = try })
					i--
					continue
				}
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
		if roomFor != c.who()+"|"+c.model && !hw.passing && (hw.code() == 400 || hw.code() == 422) {
			// the window holds the prompt but not the reply asked for with
			// it: DeepSeek counts max_tokens in its 1M window, and a client
			// asking its full 384K for a 700K conversation (max_completion_
			// tokens, passed as max_tokens since #2355) was refused as too
			// long, where every other member of its group answered
			// (molvqingtai on Discord). The same one is asked again for what
			// the window has left, as the vendor counted it
			if left := replyLeft(wordsOf(hw.errBody())); left >= leastReplyLeft {
				if _, ok := withReplyCap(from, attemptBody, left); ok {
					roomFor, roomCap = c.who()+"|"+c.model, left
					try.Fail = failRoom
					s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1] = try })
					i--
					continue
				}
			}
		}
		if hw.failed() && hw.refused {
			refusals++
			// the member's filter refused beside its thinking: its
			// reasoning is held again (holdsThinking, #4512)
			s.thoughtRefused(c)
			if !refusalAsksAgain(refusals) {
				// asked as many times as Settings' RefusalRetries lets a
				// refusal be: nobody else is, and the agent has it now
				cands = cands[:i+1]
				last = true
			} else if noRetell {
				// another Claude subscription account is a new Claude Code
				// told the whole conversation, a cache written again on that
				// account, for a refusal about the request's content that it
				// most likely gives too: a 900K conversation refused cost a
				// Pro account most of its 5 hours (Aisiverto on X). Other
				// members are still asked
				cands = append(cands[:i+1], otherThanClaudeAccounts(cands[i+1:])...)
				last = i == len(cands)-1
				log.Printf("claude: %s refused a conversation of ~%dK tokens; the subscription's other accounts aren't told it again", c.label(), retell/1000)
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
					Input: call.Usage.billed().Input, Output: call.Usage.billed().Output, CacheRead: call.Usage.billed().CacheRead,
					CacheWrite: call.Usage.billed().CacheWrite, CacheWrite1h: call.Usage.billed().CacheWrite1h, CacheUnknown: call.Usage.billed().cacheNotCounted(), Reasoning: call.Usage.billed().Reasoning, Credits: call.Usage.Credits, Effort: wentAt, Millis: try.Millis, Status: call.Status,
					TierAsked: tierAsked, TierSent: wentTier, TierServed: call.Usage.Tier,
					TTFT: try.TTFT, FirstText: try.FirstText, FirstReasoning: try.FirstReasoning, ReasoningHidden: try.ReasoningHidden, Flow: try.Flow, Session: sessionOf(r.Header), NativeSession: nativeSessionOf(r.Header), Kind: call.Kind,
					RequestID: call.Usage.RequestID, Endpoint: endpointOf(r, from, call.To, c.p, c.model), Archive: call.archiveName(), HandedOn: true}
				failedWith(&rec, call.Status, call.Error, call.Usage.ErrType)
				// what this account answered is its refusal, the reply
				// captured so far being no one's yet
				refusal := &Call{RequestBody: call.RequestBody, RequestTruncated: call.RequestTruncated, ResponseBody: string(hw.errBody()), maskIn: call.maskIn}
				if call.wholeIn != nil {
					refusal.wholeIn, refusal.wholeOut = call.wholeIn, hw.errBody()
				}
				withBodies(&rec, refusal)
				appendUsage(r, rec)
			}
			failedRow = nil // its row is written
			continue
		}
		if !last && hw.failed() && hw.turnedAway {
			// another member may answer what this account was turned away
			// from, and this one isn't set aside: Antigravity refused the
			// request, whatever quota the account has left, so nothing is
			// wrong with it and no usage is asked again (#666). Its own
			// mates are asked last, not first: they carry the same system
			// prompt, so each is turned away just the same before the group
			// reaches a member that answers. It is told as the agent's prompt
			// turned away, as WorkBuddy's is, not a quota (#1425); and if
			// nobody after it answers but with a quota of its own, that is
			// what the agent is told, at a status it doesn't retry
			if other == nil {
				other = &Try{Status: turnedAwayStatus, Error: call.Error, errType: turnedAwayErrType}
			}
			try.Fail = failPrompt
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
					Input: call.Usage.billed().Input, Output: call.Usage.billed().Output, CacheRead: call.Usage.billed().CacheRead,
					CacheWrite: call.Usage.billed().CacheWrite, CacheWrite1h: call.Usage.billed().CacheWrite1h, CacheUnknown: call.Usage.billed().cacheNotCounted(), Reasoning: call.Usage.billed().Reasoning, Credits: call.Usage.Credits, Effort: wentAt, Millis: try.Millis, Status: call.Status,
					TierAsked: tierAsked, TierSent: wentTier, TierServed: call.Usage.Tier,
					TTFT: try.TTFT, FirstText: try.FirstText, FirstReasoning: try.FirstReasoning, ReasoningHidden: try.ReasoningHidden, Flow: try.Flow, Session: sessionOf(r.Header), NativeSession: nativeSessionOf(r.Header), Kind: call.Kind,
					RequestID: call.Usage.RequestID, Endpoint: endpointOf(r, from, call.To, c.p, c.model), Archive: call.archiveName(), HandedOn: true}
				failedWith(&rec, call.Status, call.Error, call.Usage.ErrType)
				// what this account answered is its refusal, the reply
				// captured so far being no one's yet
				refusal := &Call{RequestBody: call.RequestBody, RequestTruncated: call.RequestTruncated, ResponseBody: string(hw.errBody()), maskIn: call.maskIn}
				if call.wholeIn != nil {
					refusal.wholeIn, refusal.wholeOut = call.wholeIn, hw.errBody()
				}
				withBodies(&rec, refusal)
				appendUsage(r, rec)
			}
			failedRow = nil // its row is written
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
				other = &Try{Status: call.Status, Error: call.Error, errType: call.Usage.ErrType}
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
					other = &Try{Status: call.Status, Error: call.Error, errType: call.Usage.ErrType}
				}
				try.Fail = failOverflow
				if hw.code() == http.StatusRequestEntityTooLarge {
					try.Fail = failOther
				} else {
					// the conversation is remembered what this model
					// refused as too long, to ask it last next time
					// (#3453); a byte cap (413 above) isn't the
					// conversation too long for the model, and isn't one
					noteOverflow(stuck, c, len(body))
				}
				s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1] = try })
				skipped = append(skipped, c.label()+": "+call.Error)
				cands = append(cands[:i+1:i+1], left...)
				continue
			}
		}
		if !hw.passing && badRequest(hw.code()) && hw.mayAskAgain() && zhipuImageRefusal.Match(wordsOf(hw.errBody())) {
			if b := webpAsJPEG(from, attemptBody); !bytes.Equal(b, attemptBody) {
				// GLM can't read a webp picture (Claude Desktop's browser
				// screenshot, #4732), and the session's history carries it,
				// so every turn was refused again: written here as a JPEG,
				// which it takes, and this member asked once more — from
				// then on written before the request leaves (webpRefused).
				// A picture gone as an URL is none's to write: the member
				// falls through, and the next is asked (shapeRefused)
				s.markUnfit(c.p.ID, webpRefused, from)
				try.Fail = failShape
				s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1] = try })
				i--
				continue
			}
		}
		if !last && hw.failed() && shapeRefused(hw.code(), hw.errBody()) {
			// a request this vendor's API can't read (xAI's 422 over an
			// input item it doesn't know, #350) another's may: the next is
			// asked once, as each is, and this one doesn't rest
			if other == nil {
				other = &Try{Status: call.Status, Error: call.Error, errType: call.Usage.ErrType}
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
				other = &Try{Status: call.Status, Error: call.Error, errType: call.Usage.ErrType}
			}
			try.Fail = failProxy
			s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1] = try })
			skipped = append(skipped, c.label()+": "+call.Error)
			continue
		}
		if !last && hw.failed() && offlineFailure(c, hw.code(), hw.errBody()) {
			// this computer has no network (usrname1833 on Discord): the
			// request never reached the vendor, so this one doesn't rest,
			// and is asked first again once the network is back
			if other == nil {
				other = &Try{Status: call.Status, Error: call.Error, errType: call.Usage.ErrType}
			}
			try.Fail = failOffline
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
				other = &Try{Status: call.Status, Error: call.Error, errType: call.Usage.ErrType}
			}
			try.Fail = failAuth
			s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1] = try })
			skipped = append(skipped, c.label()+": "+call.Error)
			continue
		}
		// the ChatGPT backend's 502 "response protection is unavailable"
		// (vs on Discord): the same history fails the same on every one
		// of its accounts and models, so none of them is asked it again,
		// nor this one after a moment, and nobody rests — 62 compactions
		// had cost 616 tries, and the accounts their allowances. Another
		// vendor left in the group is asked, once
		protected := !hw.passing && !hw.refused && protectionRefused(hw.code(), hw.errBody())
		// Command Code's Zero Data Retention setting refused the model
		// (plugins #246): every account of the provider is sent the same
		// header, so none of them is asked the model again, and nobody
		// rests — it answers as soon as the switch is off. Another member,
		// its other models among them, is asked
		zdr := !hw.passing && !hw.refused && hw.code() >= 400 && hw.header.Get(zdrHeader) != ""
		// no room on this computer for another Claude Code process
		// (#2956): no account's fault, and each Claude account would wait
		// for the same room, so none rests, none is asked next, nor this
		// one again after a moment. Another provider left is asked
		noRoom := hw.failed() && claudeNoRoom(hw.code(), hw.errBody())
		if protected || zdr || noRoom {
			if other == nil {
				other = &Try{Status: call.Status, Error: call.Error, errType: call.Usage.ErrType}
			}
			try.Fail = failOther
			left := elsewhere(cands[i+1:], c)
			if zdr {
				left = notMates(cands[i+1:], c)
			}
			if len(left) > 0 {
				if !noRoom {
					try.Fail = failShape
				}
				s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1] = try })
				skipped = append(skipped, c.label()+": "+call.Error)
				cands = append(cands[:i+1:i+1], left...)
				continue
			}
			cands = cands[:i+1]
			last = true
		}
		// nor is it asked again after a moment
		protected = protected || zdr || noRoom
		if !hw.passing && !hw.refused && hw.failed() && regionRefused(hw.code(), wordsOf(hw.errBody())) {
			// the vendor refused the model for where the request came from
			// (regionWords). Once, it may be the edge a kept-alive
			// connection reached: the next account, or this one again, is
			// asked on a new connection (regionFresh) and may answer
			// (#5182, ggdayup: answered 12s on). Refused again, it is the
			// place: Google serves some models to no request from it
			// (taoxiaoxin: gemini-3.8-flash refused, Claude models on the
			// same account answered), so none of the provider's other
			// accounts is asked that model, nor this one again. Nobody
			// rests (restAfterMarked); another provider or model left is
			// asked, and the agent is told why (regionNote)
			regionAsked[c.p.ID+"\x00"+c.model]++
			if regionAsked[c.p.ID+"\x00"+c.model] > 1 {
				cands = append(cands[:i+1:i+1], notMates(cands[i+1:], c)...)
				busyNow = slices.DeleteFunc(busyNow, func(b busyTry) bool { return b.c.p.ID == c.p.ID && b.c.model == c.model })
				last = i == len(cands)-1
				protected = true
			}
		}
		if !hw.passing && !hw.refused && hw.failed() && riskRefused(c, hw.code(), hw.header, hw.errBody()) {
			// the vendor's risk control restricted the account (WorkBuddy's
			// 403 11140, ZCode's 3012): its risk control reads the same
			// request on whichever account it comes from, so none of the
			// provider's other accounts is sent it, now or in a later
			// round of this request (_teamtaylor_ on Discord: one request
			// sent on from account to account got each restricted in
			// turn, until its tries ran out). It rests as restricted;
			// another provider left is asked, else the agent is told
			cands = append(cands[:i+1:i+1], elsewhere(cands[i+1:], c)...)
			busyNow = slices.DeleteFunc(busyNow, func(b busyTry) bool { return b.c.p.ID == c.p.ID })
			small, smallW = elsewhereWeighed(small, smallW, c)
			last = i == len(cands)-1
		}
		if wait, ok := passing(hw.code(), hw.header, hw.errBody(), again); ok && !protected && !last && hw.failed() && spentAfter(cands[i+1:]) {
			// the others left are out of their allowance (Discord, waroy: a
			// Codex account run out, Grok busy a moment): this one is the
			// last that may answer, and is tried again as the last is
			try.Fail, try.Again = failureOf(c, hw.code(), hw.errBody()), wait.Milliseconds()
			s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1] = try })
			skipped = append(skipped, c.label()+": "+call.Error)
			again++
			if pauseUntilReaddressed(r.Context(), wait, readdressedOf([]candidate{c})) {
				i--
				continue
			}
			call.Status, call.Error = canceled(r.Context())
			try.Gone = true
			s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1] = try })
			break
		}
		if !last && hw.failed() && codexReset(hw.code(), hw.errBody()) {
			// ChatGPT closed the connection before answering a large
			// request (#1628): it may on any account, it often passes, and
			// no account is at fault for it, so the next is asked and this
			// one doesn't rest (ylorn on Discord: acct-1 had rested for it).
			// The last one left is asked again after a moment, as for any
			// 503, before the agent is told
			if other == nil {
				other = &Try{Status: call.Status, Error: call.Error, errType: call.Usage.ErrType}
			}
			try.Fail = failOther
			s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1] = try })
			skipped = append(skipped, c.label()+": "+call.Error)
			continue
		}
		if !last && hw.failed() && hw.wantWhole && strings.HasPrefix(hw.failMsg, incompleteReply) {
			// a summary the model cut short — its output limit reached —
			// is the model's doing, not the account's: the next is asked,
			// and nobody rests (#2274)
			if other == nil {
				other = &Try{Status: call.Status, Error: call.Error, errType: call.Usage.ErrType}
			}
			try.Fail = failOther
			s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1] = try })
			skipped = append(skipped, c.label()+": "+call.Error)
			continue
		}
		if !last && hw.failed() && strings.Contains(call.Error, emptyReply) {
			// said by a second account of the model, it is about the
			// request and no account: the rest of that model's accounts
			// are not asked (emptyAccounts); other providers and models
			// still are
			ek := c.p.ID + "\x00" + c.model
			if emptied[ek] == nil {
				emptied[ek] = map[string]bool{}
			}
			emptied[ek][c.who()] = true
			if len(emptied[ek]) >= emptyAccounts {
				cands = append(cands[:i+1:i+1], notMates(cands[i+1:], c)...)
				last = i == len(cands)-1
			}
		}
		if !last && hw.failed() && strings.Contains(call.Error, emptyReply) {
			// a reply that said nothing is a failure of the model or
			// request, not the account (#1221): the account doesn't rest,
			// and mates of the same model, likely to end empty too, are
			// asked last
			if other == nil {
				other = &Try{Status: call.Status, Error: call.Error, errType: call.Usage.ErrType}
			}
			try.Fail = failOther
			s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1] = try })
			skipped = append(skipped, c.label()+": "+call.Error)
			matesLast(cands[i+1:], c)
			continue
		}
		plain403 := hw.failed() && !hw.refused && plainForbidden(c, hw.code(), hw.errBody())
		// a 5xx that says nothing of why, from a provider answering other
		// requests: alike from two accounts, it is the request's too
		var words5xx string
		sameRefusal = false
		if !plain403 && hw.failed() && !hw.refused && !hw.turnedAway && plainUnavailable(c, hw.code(), hw.errBody()) && answeredLately(c.p.ID) {
			words5xx = refusalWords(hw.code(), hw.errBody())
		}
		if alike, others := forbiddenAlike(forbade, c, words5xx); (plain403 || words5xx != "") && len(alike) > 0 {
			// a mate refused this request with a plain 403 too, or the
			// same 5xx in the same words: the request is what is refused,
			// not each account (plugins#243, mycf_711's 503s). Neither
			// rests, the mate's rest taken back, and its other mates are
			// asked last, as for a prompt turned away; the last one left
			// isn't asked again after a moment, as one busy is
			sameRefusal = words5xx != ""
			for _, f := range alike {
				unforbid(f)
			}
			forbade = others // each taken back once
			try.Fail = failOther
			s.trace.update(tr, func(t *Route) {
				for _, f := range alike {
					if f.try >= 0 && f.try < len(t.Tries) {
						t.Tries[f.try].Rest = nil
					}
				}
				if !last {
					t.Tries[len(t.Tries)-1] = try
				}
			})
			if !last {
				if other == nil {
					other = &Try{Status: call.Status, Error: call.Error, errType: call.Usage.ErrType}
				}
				skipped = append(skipped, c.label()+": "+call.Error)
				matesLast(cands[i+1:], c)
				continue
			}
		}
		if hw.failed() && !hw.passing && !hw.turnedAway && !hw.refused && !creditsAsked[c.rest] && creditsAgain(c, hw.code(), hw.errBody()) {
			// a Codex account that holds credits, refused as out of its
			// allowance: ChatGPT may turn away the turn that uses the
			// window up before it spends the credits, and answers the
			// next on them (Zhenzhen on Discord: the running session
			// ended "usage exhausted" while a new one answered). It is
			// asked again at once, once a request, rather than the turn
			// moved off it or ended
			creditsAsked[c.rest] = true
			try.Fail, try.Credits = failQuota, true
			s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1] = try })
			skipped = append(skipped, c.label()+": "+call.Error+" (asked again, to spend its credits)")
			i--
			continue
		}
		if hw.failed() && !hw.passing && len(unlisted) > 0 && unservedTry(c, hw.code(), hw.errBody()) {
			unservedBy[c.p.ID+"/"+c.model] = true
		}
		if key := c.p.ID + "/" + c.model; unservedBy[key] && hw.failed() && !hw.passing && !hw.turnedAway && !hw.refused && !outgrew &&
			!slices.ContainsFunc(cands[i+1:], func(x candidate) bool { return x.p.ID+"/"+x.model == key }) {
			// every account of the subscription that lists the model has
			// been asked, and one refused it as a model it doesn't serve:
			// its accounts whose own list lacks it are asked next, before
			// any other member, as a list fetched before the account was
			// given the model is stale (01huadalang on Discord: Gemini 4
			// on one Antigravity account of several), rather than the
			// agent told the first one's error. The one that refused
			// rests, for that model alone
			var next []candidate
			var nextW []Weighed
			next, nextW, unlisted, unlistedW = unlistedNext(unlisted, unlistedW, key)
			if len(next) > 0 && last {
				if f := failure(hw.code(), hw.errBody()); other == nil && f != failQuota && f != failCredit {
					other = &Try{Status: call.Status, Error: call.Error, errType: call.Usage.ErrType}
				}
				rest := s.restAfterMarked(c, hw.code(), hw.header, hw.errBody(), hw.sharedPool)
				try.Fail, try.Rest = rest.Why, &rest
				s.trace.update(tr, func(t *Route) {
					t.Tries[len(t.Tries)-1] = try
					t.Order, t.Left = askUnlisted(t.Order, t.Left, c, nextW)
				})
				skipped = append(skipped, c.label()+": "+call.Error)
				cands = append(cands[:len(cands):len(cands)], next...)
				continue
			}
			if len(next) > 0 {
				// the rest of the order after them; this try is rested below
				cands = slices.Concat(cands[:i+1], next, cands[i+1:])
				s.trace.update(tr, func(t *Route) { t.Order, t.Left = askUnlisted(t.Order, t.Left, c, nextW) })
			}
		}
		if !last && hw.failed() && (strings.Contains(call.Error, finishFailed) || strings.Contains(call.Error, emptyCompleted)) {
			// a 200 stream the vendor ended with finish_reason "error"
			// (#2471), or with a response.completed that carried nothing
			// and counted no token (also #2471): the turn failed, as the
			// words say, but the account is at fault no more than one
			// whose reply said nothing (#1221) or was cut short by its
			// output limit (#2274): the next is asked, and nobody rests
			if other == nil {
				other = &Try{Status: call.Status, Error: call.Error, errType: call.Usage.ErrType}
			}
			try.Fail = failOther
			s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1] = try })
			skipped = append(skipped, c.label()+": "+call.Error)
			continue
		}
		if !last && hw.failed() && failure(hw.code(), hw.errBody()) == failCut {
			// a plugin ended the turn on purpose, its upstream's stream
			// having died mid-call (plugins#38: "the gateway can retry or
			// fail over"). Nobody rests: the parser reads the same but the
			// cut doesn't come again, so the same one is asked again once,
			// then the next member as any failure's is (#2873)
			try.Fail = failCut
			if other == nil {
				other = &Try{Status: call.Status, Error: call.Error, errType: call.Usage.ErrType}
			}
			s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1] = try })
			skipped = append(skipped, c.label()+": "+call.Error)
			if cuts < cutReasks {
				cuts++
				select {
				case <-time.After(retryPause):
				case <-r.Context().Done():
				}
				i--
			}
			continue
		}
		if wait, ok := keptAgain(aff, c, hw, keptAsked); ok && !last && !kept.tooQuiet(wait) {
			// the one the conversation is kept on, for what its vendor
			// cached of it, failed a way that passes — overloaded, a 5xx,
			// timed out, not a rate limit, quota or sign-in: it is asked
			// once more after a moment, without resting, before the
			// conversation moves and the next one is sent it all uncached
			// (jiabubu on Discord: a channel that errs once and is back a
			// moment later had the conversation moved off it at once)
			keptAsked = true
			try.Fail, try.Again = failureOf(c, hw.code(), hw.errBody()), wait.Milliseconds()
			try.Kept = true
			s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1] = try })
			skipped = append(skipped, c.label()+": "+call.Error)
			select {
			case <-time.After(wait):
				i--
				continue
			case <-r.Context().Done():
				call.Status, call.Error = canceled(r.Context())
				try.Gone = true
				s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1] = try })
			}
			break
		}
		if !last && hw.failed() {
			if f := failure(hw.code(), hw.errBody()); other == nil && f != failQuota && f != failCredit {
				other = &Try{Status: call.Status, Error: call.Error, errType: call.Usage.ErrType}
			}
			rest := s.restAfterMarked(c, hw.code(), hw.header, hw.errBody(), hw.sharedPool)
			try.Fail, try.Rest = rest.Why, &rest
			at := -1
			s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1], at = try, len(t.Tries)-1 })
			if plain403 || words5xx != "" {
				forbade = append(forbade, forbidden{c: c, rest: rest, try: at, words: words5xx})
			}
			skipped = append(skipped, c.label()+": "+call.Error)
			if waitsOf(c.patience, patienceFallback) > 0 && !hw.turnedAway && busy(hw.code(), hw.errBody()) && !routedThere(hw.header) {
				busyNow = noteBusy(busyNow, c, hw.header)
			}
			continue
		}
		// (a plain provider's: not ChatGPT's reset of a large request,
		// which Codex is told at its 503 with what may help, as Codex's
		// own one-account relay leaves it, codexBusy)
		// The last one left out of its allowance, after a member before it
		// failed in a way that passes, has those asked again as well, and
		// rests as they did: the relay's moment of 503 had the agent told
		// it, or the last one's 429, while the relay would answer a moment
		// on (dbydd on Discord: an in-order group of sub2api relays, a
		// Codex account out of its quota last). One already resting out of
		// its allowance, asked only as the one back soonest, isn't: the
		// members before it were asked again as the last that may answer
		// (spentAfter) and the agent is told theirs
		lastSpent := last && isGroup && len(busyNow) > 0 && !hw.passing && hw.failed() && !busy(hw.code(), hw.errBody()) &&
			slices.Contains([]string{failQuota, failCredit}, failure(hw.code(), hw.errBody())) && !spentAfter([]candidate{c})
		if _, same := passing(hw.code(), hw.header, hw.errBody(), again); last && patience > 0 && !protected && !sameRefusal && !hw.turnedAway && !hw.refused && hw.failed() && (busy(hw.code(), hw.errBody()) && (!routedThere(hw.header) || len(busyNow) > 0) || lastSpent) &&
			(isGroup || !codexReset(hw.code(), hw.errBody())) &&
			(len(busyNow) > 0 || waitsOf(c.patience, patienceFallback) > 0 && !same) {
			// every member has failed, each in a way that passes — busy,
			// overloaded, rate limited for a moment — with nothing of a
			// reply sent: after a pause, longer each round and no shorter
			// than a Retry-After, those back by then are asked again in
			// turn, while each member's Patience for the request lasts
			// (#1418: Codex's turn ended on server_is_overloaded, to be
			// told "continue" by hand; #2410: a nested group's own).
			// Another member busy too, this one isn't asked again alone
			// first, as the last one left is: they take turns. The
			// patience is counted from the first time every member had
			// failed, when the agent would have had the error
			if waitFrom.IsZero() {
				waitFrom = time.Now()
			}
			if waitsOf(c.patience, patienceFallback) > 0 && !lastSpent && !routedThere(hw.header) {
				// (another magpie's routed answer isn't asked again:
				// its own patience had its turn, routedThere)
				busyNow = noteBusy(busyNow, c, hw.header)
			}
			// nor once Codex would hang up in the pause, for want of an
			// event (agentQuietMost): it has the error instead
			memberWait := waitsOf(c.patience, patienceFallback)
			if memberWait == 0 {
				memberWait = patience
			}
			if next, wait, ok := replan(busyNow, rounds, waitFrom, patienceFallback); ok && !kept.tooQuiet(wait) && !kept.outlives(wait) {
				try.Fail, try.Again = failureOf(c, hw.code(), hw.errBody()), wait.Milliseconds()
				if lastSpent {
					rest := s.restAfterMarked(c, hw.code(), hw.header, hw.errBody(), hw.sharedPool)
					try.Fail, try.Rest = rest.Why, &rest
				}
				if isGroup {
					// a plain provider's is told as the last one left
					// asked again: it has no members taking turns
					try.Replan, try.Patience = len(next), int(memberWait/time.Second)
				}
				s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1] = try })
				skipped = append(skipped, c.label()+": "+call.Error)
				rounds, busyNow = rounds+1, nil
				if pauseAlive(r.Context(), w, kept, streams, start, wait, readdressedOf(next)) {
					cands = append(cands[:i+1:i+1], next...)
					continue
				}
				call.Status, call.Error = canceled(r.Context())
				try.Gone = true
				s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1] = try })
				break
			}
		}
		if wait, ok := passing(hw.code(), hw.header, hw.errBody(), again); ok && !protected && !sameRefusal && !hw.turnedAway && hw.failed() && waitFrom.IsZero() && !kept.tooQuiet(wait) && !kept.outlives(wait) {
			// nobody else is left: the same one again, after a moment (not
			// what Antigravity turned away: it turns it away again; nor
			// once the group's patience is spent)
			try.Fail, try.Again = failureOf(c, hw.code(), hw.errBody()), wait.Milliseconds()
			s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1] = try })
			skipped = append(skipped, c.label()+": "+call.Error)
			again++
			if pauseUntilReaddressed(r.Context(), wait, readdressedOf([]candidate{c})) {
				i--
				continue
			}
			call.Status, call.Error = canceled(r.Context())
			try.Gone = true
			s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1] = try })
			break
		}
		// (not for one account pinned: the others weren't asked)
		if !autoReset && pin == "" && last && other == nil && hw.failed() && !hw.passing && !hw.turnedAway && failure(hw.code(), hw.errBody()) == failQuota {
			// everyone is out of their allowance: a Codex or Claude account
			// the user lets spend its resets by itself, its week used up,
			// spends one and is asked again
			autoReset = true
			if pick, out, ok := s.autoReset(r.Context(), all, c, pl.held); ok {
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
		// the body is read for why last, once the try is known to have
		// failed: a reply held whole that passed is the whole reply, and
		// failure over a 24MB one held its client past its timeout
		if last && len(small) > 0 && pin == "" && other == nil && hw.failed() && !hw.passing && !hw.turnedAway && !hw.refused && !outgrew &&
			(try.Fail == failQuota || try.Fail == failCredit || slices.Contains([]string{failQuota, failCredit}, failure(hw.code(), hw.errBody()))) {
			// every one with room is out of its allowance: those roomy
			// left out as too small for the request are asked after all,
			// those not resting, for the agent to be told to compact
			// rather than to wait for an allowance hours away (Kai on
			// Discord: Claude's two accounts spent, its Fallback a Codex
			// model of 272K). This one rests, as one before the last does
			var next []candidate
			var nextW []Weighed
			for k, x := range small {
				if _, resting := restingOf(x); resting {
					continue
				}
				next = append(next, x)
				if len(smallW) == len(small) {
					w := smallW[k]
					w.Window = 0 // asked after all: weighed, not left out
					nextW = append(nextW, w)
				}
			}
			small = nil
			if len(next) > 0 {
				rest := s.restAfterMarked(c, hw.code(), hw.header, hw.errBody(), hw.sharedPool)
				try.Fail, try.Rest = rest.Why, &rest
				s.trace.update(tr, func(t *Route) {
					t.Tries[len(t.Tries)-1] = try
					t.Order = append(slices.Clip(t.Order), nextW...)
					t.Left = slices.DeleteFunc(slices.Clone(t.Left), func(l Weighed) bool {
						return l.Window > 0 && slices.ContainsFunc(nextW, func(w Weighed) bool { return w.ID == l.ID && w.Model == l.Model })
					})
				})
				skipped = append(skipped, c.label()+": "+call.Error)
				cands = append(cands[:len(cands):len(cands)], next...)
				continue
			}
		}
		if last && !held && !hw.passing && hw.failed() && quotaStatus(hw.code()) && (c.p.Account == nil || !hw.turnedAway && try.Fail != failPrompt) && (try.Fail == failQuota || try.Fail == failCredit || failure(hw.code(), hw.errBody()) == failQuota || failure(hw.code(), hw.errBody()) == failCredit) {
			// the last key left is out of its allowance: it rests as the
			// ones before it did, for the next request to be answered
			// from the rests until the first of them is back (#1772). An
			// account too: not resting, it stayed the one a relay's moment
			// of failure went on to, and its 429 the agent's, request
			// after request, while the relay before it would answer
			// (dbydd on Discord), once its own refusal is read as out of
			// quota or credit (not Antigravity's 429 to a prompt).
			// quotaHeld still holds keys alone, and autoReset lifts the
			// rest of an account it spends a reset of
			rest := s.restAfterMarked(c, hw.code(), hw.header, hw.errBody(), hw.sharedPool)
			try.Fail, try.Rest = rest.Why, &rest
		} else if last && !held && !hw.passing && hw.failed() && hw.code() == http.StatusTooManyRequests && !hw.turnedAway && try.Fail != failPrompt && failure(hw.code(), hw.errBody()) == failRate && vendorSaidWhen(hw.header, hw.errBody()) {
			// the last one left is rate limited till a time its vendor
			// named: it rests till then too, for the agent's retries to be
			// answered from the rest (timedHeld) rather than sent on to
			// the vendor, which flags an account asked so (hsjzg58958 on
			// X). A 429 naming no time rests nobody, as before: a relay's
			// moment of saturation is asked again
			rest := s.restAfterMarked(c, hw.code(), hw.header, hw.errBody(), hw.sharedPool)
			try.Fail, try.Rest = rest.Why, &rest
		}
		if !hw.passing && hw.code() == http.StatusTooManyRequests && hw.header.Get("Retry-After") == "" {
			// the vendor said nothing of when: the agent is told when
			// magpie asks again, the first of those it rested back
			if heldBack.IsZero() {
				heldBack, _ = soonestBack(cands[:i+1])
			}
			if wait := time.Until(heldBack); wait > 0 {
				w.Header().Set("Retry-After", strconv.Itoa(int((wait+time.Second-1)/time.Second)))
			}
		}
		if hw.refused && !hw.passing && from == provider.Anthropic && hw.handBack() {
			// nobody is left, and the agent speaks Anthropic's Messages:
			// it has the refusal as the vendor said it, stop_reason
			// "refusal" and its stop_details, which Claude Code answers by
			// asking its own fallback model (DD on Discord), where a 400
			// is only an error to it
		} else if hw.refused && !hw.passing {
			// nobody is left: the agent is told it was refused, as a
			// request it shouldn't send again as it is, not handed an empty
			// reply it would ask again for, paying for each
			failTo(w, kept, from, refusedStatus, call.Error)
		} else if hw.turnedAway && hw.failed() {
			// nobody is left to answer what Antigravity turned away: the
			// agent is told why, at a status it doesn't retry, not the 429
			// it would ask again for ten times over, each turned away the
			// same (#1425). The account isn't at fault, and doesn't rest
			call.Status = turnedAwayStatus
			failTo(w, kept, from, call.Status, call.Error)
		} else if f := failure(call.Status, []byte(call.Error)); other != nil && !hw.passing && call.Status >= 400 && (f == failQuota || f == failCredit) {
			// the last one left is out of its allowance, but one before it
			// failed otherwise: the agent is told that one's error, not
			// the allowance's — Codex, told its usage is exhausted, stops
			// taking input though another member would answer next time
			// (Discord, waroy)
			try.Fail, try.Earlier = f, true
			s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1] = try })
			// and its row says that one's kind too: a 529 overload told
			// beside the last account's usage_limit_reached read as the
			// overload being the quota (蛋仔 on Discord)
			call.Status, call.Error, call.Usage.ErrType = other.Status, other.Error, other.errType
			s.trace.update(tr, func(t *Route) { t.Fail = toldFail(other) })
			failTo(w, kept, from, call.Status, call.Error)
			break
		} else if outgrew {
			// in the agent's own words for it, for it to compact
			failTo(w, kept, from, call.Status, hw.failMsg)
		} else if !hw.passing && hw.code() == http.StatusRequestEntityTooLarge {
			// No member left took the body; only now add the byte-limit hint.
			failTo(w, kept, from, call.Status, call.Error)
		} else if !hw.passing && hw.code() >= 400 && kept.compactsInStream(call.Status, call.Error) {
			// the vendor said the conversation is past the model's window:
			// Codex is told in its stream, the one place it compacts on it
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
			try.Fail = failureOf(c, failedNamed(hw.header, call.Status), []byte(call.Error))
			// a compaction a rule sent to a model of its own isn't a turn of
			// the conversation: it leaves the stick where it was when it
			// answers (below), and breaking off mid-reply forgets nothing
			// either (#776's follow-up). A conversation kept strictly
			// forgets nothing: it never moves, so the retry goes back to
			// the one it is on, its failure the agent's, as any other's
			// there is (imwhatiam123 on Discord: Stays: Strict moved a
			// session from Qoder CN to WorkBuddy AI after a reply broke
			// off, the stick gone and the retry routed as a new one)
			if (hit == nil || !hit.Compact) && aff.Why != "strict" {
				unanswered(stuck, c)
			}
			if hw.looped != "" {
				// the model looped (#1359): its account and key are not at
				// fault, and nobody rests
				try.Fail = failLoop
			} else if hw.refusedAfter {
				// the vendor's filter refused the turn it had begun, as it
				// refused one before any of it was said (#248): the account
				// is at fault no more for it, and nobody rests
				try.Fail = failRefused
			} else if named := failedNamed(hw.header, namedStatus([]byte(call.Error))); named >= 400 && named < 500 && !retryable(named, []byte(call.Error)) {
				// the error frame named a status that is the request's
				// fault, as it would be before the stream: Antigravity's
				// 400 INVALID_ARGUMENT after its thinking (zRain on
				// Discord, sse_bare.go) fails the same on every account,
				// and nobody rests, as for that 400 sent before the 200
			} else if lateRests(call.Error) {
				rest := s.restAfter(c, call.Status, hw.header, []byte(call.Error))
				try.Fail, try.Rest = rest.Why, &rest
			}
			if hw.looped == "" && !hw.refusedAfter {
				// an upstream's error frame that named its status (#4855,
				// mycf_711: wps-lingxi's "（HTTP 503）" rows read status=200)
				// is booked at it, in the Routing and Usage views, though
				// the agent's 200 went out before it
				if named := failedNamed(hw.header, namedStatus([]byte(call.Error))); named >= 400 {
					call.Status = named
				}
			}
		} else if call.Status < 400 {
			servedCandidate(c, call.Usage.Input+call.Usage.Output+call.Usage.CacheRead+call.Usage.CacheWrite)
			// a thinking reply that went on to an answer is one more in the
			// row that trusts the member's reasoning as it comes
			// (holdsThinking, #4512); one whose filter's content_filter
			// ended it with nothing said takes the trust back (plugins #540)
			if hw.refusedAfter {
				s.thoughtRefused(c)
			} else if hw.sawThinking() && call.Usage.Output > 0 {
				s.thoughtAnswered(c)
			}
			// how fast it answered, for Speed first (speed.go)
			noteTrySpeed(c, try, call.Usage.Output, call.Usage.Reasoning, ended)
			// a compaction a rule sent to a model of its own leaves the
			// conversation, and the turn's size, where they were
			if hit == nil || !hit.Compact {
				answeredStanding(stuck, c, aff.Turn, call.Usage, standsInFor(c, plannedCs, plannedOrder))
				// what it answered, it holds: whatever it refused as too
				// long on the way is forgotten (#3453)
				forgetOverflow(stuck, c)
			}
			// the trigger was kept, so whoever answered sealed it: a
			// fallback whose own native compaction is off too, whose seal
			// only it can read (#2271)
			sealedBy = &c
			if hit != nil && !hit.Compact {
				ruleAnswered(ruleAt, call.Usage, coldAfter(c.p))
			}
			for _, at := range nestedAt {
				ruleAnswered(at, call.Usage, coldAfter(c.p))
			}
		} else if hw.refused {
			try.Fail = failRefused
		} else if hw.turnedAway {
			// Antigravity turned the agent's system prompt away (#666), at
			// whatever quota the account has: not a quota used up (#1425)
			try.Fail = failPrompt
		} else if promptRefused(call.Status, []byte(call.Error)) {
			try.Fail = failPrompt
		} else {
			try.Fail = failureOf(c, call.Status, []byte(call.Error))
			if heldRestricted {
				// its words may be the vendor's alone, read as
				// restricted by its code (WorkBuddy's 11140), gone here
				try.Fail = failRestricted
			}
			if offlineFailure(c, call.Status, []byte(call.Error)) {
				// the last one left failed for want of a network
				// (offline.go): told so, and it doesn't rest
				try.Fail = failOffline
			}
			// and one its vendor restricted (riskRefused), for the next
			// request not to be sent to it again
			// A refusal given again without asking (restrictedHeld)
			// rests nobody anew: its rest stands as it was taken, and
			// ends when it would
			if ((try.Fail == failVerify || try.Fail == failRestricted) && !held || strictAlone) && !heldRestricted {
				// the last one left rests too, for the app to show and the
				// next requests to be held
				rest := s.restAfter(c, call.Status, hw.header, []byte(call.Error))
				try.Rest = &rest
			}
		}
		if try.Fail == failRefused {
			// the filter refused after (or beside) the try's thinking: the
			// member's reasoning is held again (holdsThinking, #4512)
			s.thoughtRefused(c)
		}
		s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1] = try })
		break
	}
	if len(skipped) > 0 {
		call.Fallback = strings.Join(skipped, "; ")
	}
	call.Millis = time.Since(start).Milliseconds()
	if !ended.IsZero() {
		call.Millis = ended.Sub(start).Milliseconds()
	}
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
	tries := 0 // the request's tries, as its route lists them
	s.trace.update(tr, func(t *Route) {
		t.Done, t.Status, t.Error, t.Millis = true, call.Status, call.Error, call.Millis
		// the id the agent reads beside the reply, so the session that
		// keeps it can be found after the fact (dsh's replays)
		t.ResponseID = call.Usage.ResponseID
		tries = len(t.Tries) + reasked
		if t.TitleLink != nil && isTitleKind(t.Kind) && call.Status < 400 && call.Error == "" && !call.ResponseTruncated {
			link := *t.TitleLink
			link.Reply = replyDigest
			t.TitleLink = &link
		}
		if call.To != "" {
			t.Usage = append(t.Usage, routeUsage(call.Provider, model, call.Usage)...)
		}
		t.Tokens = call.Usage.Input + call.Usage.Output + call.Usage.CacheRead + call.Usage.CacheWrite
		t.FirstReasoning, t.ReasoningHidden = call.FirstReasoning, call.ReasoningHidden
		t.Output, t.Reasoning, t.TTFT, t.FirstText, t.Flow = call.Usage.Output, call.Usage.Reasoning, call.TTFT, call.FirstText, call.Flow
		t.Sent, t.Own, t.Retried, t.Queued = sentMs, ownMs, retriedMs, queuedMs
		t.Masking = maskingMs(r.Context(), ownMs)
		if prompt != nil {
			t.Prompt = prompt.calibrated(promptCounted(t.Usage), window)
		}
		if n := len(t.Tries); n > 0 && call.Status < 400 {
			t.Served, t.Swapped, t.Routed = t.Tries[n-1].Served, t.Tries[n-1].Swapped, t.Tries[n-1].Routed
			t.Upstream = t.Tries[n-1].Upstream
		}
		t.Archive = call.archiveName()
	})
	call.Route = tr.ID
	call.Masked = s.trace.maskedOf(tr)
	s.record(call)
	if call.To != "" {
		rec := usage.Record{RouteID: tr.ID, Time: start, Agent: call.Agent, Via: call.Via, Provider: call.Provider, Host: where, Model: model,
			ProviderKeyID: providerKeyID, ProviderKeyName: providerKeyName, ProviderAccount: providerAccount,
			Requested: call.Model, Served: call.Usage.Served, Upstream: call.Usage.Upstream,
			Input: call.Usage.billed().Input, Output: call.Usage.billed().Output, CacheRead: call.Usage.billed().CacheRead,
			CacheWrite: call.Usage.billed().CacheWrite, CacheWrite1h: call.Usage.billed().CacheWrite1h, CacheUnknown: call.Usage.billed().cacheNotCounted(), Reasoning: call.Usage.billed().Reasoning, Credits: call.Usage.Credits, Effort: wentAt, Millis: call.Millis, Status: call.Status,
			TierAsked: tierAsked, TierSent: wentTier, TierServed: call.Usage.Tier,
			TTFT: call.TTFT, FirstText: call.FirstText, FirstReasoning: call.FirstReasoning, ReasoningHidden: call.ReasoningHidden, Flow: call.Flow, Sent: sentMs, Own: ownMs, Masking: maskingMs(r.Context(), ownMs), Retried: retriedMs, Queued: queuedMs, Session: sessionOf(r.Header), NativeSession: nativeSessionOf(r.Header), Kind: call.Kind,
			RequestID: call.Usage.RequestID, ResponseID: call.Usage.ResponseID, Endpoint: endpointOf(r, from, call.To, lastTried, model), Stop: call.Usage.Stop, Archive: call.archiveName()}
		if tries > 1 {
			// answered, or not, after a try that failed first: the ledger
			// counts it once, as the request it is (小一 on Discord)
			rec.Tries = tries
		}
		if up := upBytesOf(r.Context()); up != nil {
			rec.UpBytes, rec.PlainBytes, rec.Socket = up.wire, up.plain, up.socket
			rec.DownBytes, rec.ImageFilesSaved, rec.ImageFiles = up.downOf(), up.saved, up.images
		}
		failedWith(&rec, call.Status, call.Error, call.Usage.ErrType)
		withBodies(&rec, &call)
		appendUsage(r, rec)
		if call.Status < 400 {
			// what its model searched with its provider's own tool
			logNativeSearches(r.Context(), call.Usage.searches, start, call.Agent, call.Provider, where, model)
		}
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
	// the try that answers is the one whose rate estimates the reply's
	// tokens (live.go), after a fallback another than the first
	liveOfInflight(inflightOf(r.Context())).routed(p.ID, unprefixed(model))
	// the tier the reply said, unknown where the vendor's word tells
	// nothing (servedTierAt)
	defer func() { call.Usage.Tier = servedTierAt(p.VendorHost(), call.Usage.Tier) }()
	// Normalize for the actual destination, separately on each fallback.
	// Native ChatGPT accounts accept standalone tool outputs themselves.
	if from == provider.Responses && (p.Account == nil || p.Account.Agent != "codex") {
		body = joinToolRuns(body)
		body = orphanedToolOutputs(body)
		body = pairToolItems(body)
	}
	if from == provider.Chat {
		// a call kept with an empty id, which a strict upstream turns
		// the whole request away for (#2611); before the relay, the
		// translation and a plugin's fetch alike
		body = chatCallIDs(body)
	}
	// a response the request named that magpie doesn't hold goes only to
	// a provider it is relayed to as written (prev_response.go): Claude's
	// and the ChatGPT backend's accounts drop it, as a translation does
	noteAnswered(r, "", "")
	if id := prevLost(r); id != "" && p.Account != nil && (p.Account.Agent == "claude" || p.Account.Agent == "codex") {
		return writePrevNotFound(w, id)
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
	// (the provider is always streamed on that path) rather than relayed,
	// except a plugin's on Responses, which is relayed streamed and given
	// back whole (gathers)
	relayable := slices.Contains(s.usable(p, model), from) && (p.Account == nil || !p.Account.Stream || streamOf(body) || gathers(p, from, body))
	relay := relayable
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
	// that sealed it reads it, and a translation would drop it; and Codex's
	// turn to a relay that takes it as Codex sent it (codexAsSent): the
	// translation merged the developer message into instructions, sent
	// apply_patch as a function, left web_search out and added
	// max_output_tokens, the shape such a relay turns away (maralica on
	// Discord). Its web_search goes to the relay as sent.
	if relay && searchedForIt(r.Context(), p, from, model, body) && !(from == provider.Responses && (hasSealedAgentMessage(body) || codexAsSent(p, r.Header))) {
		relay, ownAPI = false, true
	}
	// Claude Code's advisor, which magpie runs for a model whose upstream
	// can't (advisor_serve.go): a relay would leave it out (advisorTools)
	if relay && advisedHere(p, from, from, model, body, wiresOf(r.Context())) {
		relay, ownAPI = false, true
	}
	// Copilot seals an argument the tools mark "encrypted" (spawn_agent's
	// message, MultiAgentV2) and then can't open what it sealed: the
	// subagent's turn came back "Encrypted function output content could
	// not be decrypted or decoded" (2026-10-17, a real account). Translated,
	// the marks are left out and the call goes back to Codex unsealed.
	if relay && from == provider.Responses && p.Account != nil && p.Account.Agent == "copilot" && asksSealing(body) {
		relay, ownAPI = false, true
	}
	// Zen's free models are asked as OpenCode asks them (zenfree.go)
	if p.OpenCodeFree(model) {
		relay = false
	}
	// Factory's generate route always answers SSE, and droid sends no stream
	// field; a Gemini API is always asked streamGenerateContent
	// (upstreamPath). A client that asked for one JSON body is translated,
	// which reads that SSE and writes the JSON. Relaying it would hand the
	// client the data: lines under a 200. Vertex AI answers generateContent
	// whole, and a translation would drop what the client asked that magpie
	// doesn't read (its safetySettings, topK, schema, thinkingBudget 0)
	if relay && from == provider.Gemini && !streamOf(body) && !p.IsVertex() {
		relay = false
		// one asking an image's size or shape (imageConfig) is translated
		// on the provider's Gemini API, the only one that takes it, not on
		// the Chat API usable puts first, where the picture came at the
		// model's default size (#3820); any other goes to the first API
		// usable gives
		if gjson.GetBytes(body, "generationConfig.imageConfig").IsObject() {
			ownAPI = true
		}
	}
	// a Qwen model that thinks gives one JSON body only with its thinking
	// off: DashScope turns the request away with "parameter.enable_thinking
	// must be set to false for non-streaming calls". One asked for whole is
	// asked streamed on Chat and given back whole, its thinking as the
	// model would have done it (ZekeXiao on X: a group's classifier on the
	// Qwen Token Plan, which magpie asks for one body)
	if relay && from == provider.Chat && !streamOf(body) && dashScope(p) {
		relay, ownAPI = false, true
	}
	// Codex's compaction trigger, or a compaction sealed where native
	// compaction is on, goes as it came: a translation would leave it out
	// (#1099)
	keep := s.keepsCompaction(from, p, model, body)
	// Codex's custom tools, to a model that turned them away before
	// (custom_tool.go, #2656): translated on the client's own API, which
	// offers them as functions
	customOff := func() bool {
		if from != provider.Responses || !hasCustomTool(body) {
			return false
		}
		// Grok's, on Copilot's /responses in front of it: xAI takes no
		// custom tool (#3392), and Copilot's answer says no more than the
		// status — "422 Unprocessable Entity: Unprocessable Entity"
		// (#2951, #4374: six requests of one turn, all refused) — so
		// nothing tells magpie what to learn from. They go as functions
		// from the first ask.
		if p.Account != nil && p.Account.Agent == "copilot" && grokModel(model) {
			return true
		}
		return !s.fits(p.ID, customToolRefused(model), from)
	}
	if relay && !keep && customOff() {
		relay, ownAPI = false, true
	}
	// and a backend that turned a request for one body away because it
	// only streams (stream_only.go) is asked streamed, translated
	streamOnly := (from == provider.Chat || from == provider.Responses) && !streamOf(body) && !s.fits(p.ID, streamOnlyRefused(model), from)
	if relay && !keep && streamOnly {
		relay, ownAPI = false, true
	}
	if keep && relayable {
		relay, ownAPI = true, false
	}
	if relay {
		call.To = from
		// a web search magpie ran for the model, replayed to a provider
		// that runs none: left out, as a translation leaves it, since the
		// provider's own chat form answers it with nothing (withoutSearchCalls).
		// A relay Codex's turns go to as sent, and a native compaction,
		// keep what Codex sent.
		if from == provider.Responses && !keep && !searchesModel(p, from, model) && !codexAsSent(p, r.Header) {
			body = withoutSearchCalls(body)
		}
		status, msg, done := s.passthrough(w, r, p, from, model, body, &call.Usage)
		if done {
			return status, msg
		}
		// the model isn't served on the client's own API: speak another;
		// or it turned the custom tools away just now: speak it translated,
		// except a compaction, which a translation would leave out
		if customOff() || !s.fits(p.ID, streamOnlyRefused(model), from) && !streamOf(body) {
			if keep {
				return writeError(w, from, status, msg), msg
			}
			ownAPI = true
		}
	}
	if keep {
		msg := fmt.Sprintf("%s doesn't serve %s on the Responses API, so Codex's native compaction can't be asked of it", p.Name, model)
		return writeError(w, from, 400, msg), msg
	}
	to := s.usable(p, model)
	if len(to) == 0 {
		// every API turned the model away: the one the user picked for it
		// is asked again, not the first the provider has a URL for, which
		// they said it isn't asked on (01huadalang: picked for Responses,
		// sent as chat after one refusal there)
		if proto, ok := p.ModelAPI(model); ok {
			to = []provider.Protocol{proto}
		} else {
			to = p.Speaks()
		}
	}
	if len(to) == 0 {
		msg := p.Name + " has no endpoint configured"
		return writeError(w, from, 502, msg), msg
	}
	if ownAPI && slices.Contains(to, from) && !firstElsewhere(p, model, from) {
		to = []provider.Protocol{from}
	}
	if id := prevLost(r); id != "" {
		return writePrevNotFound(w, id)
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

// lastUserText is what the user said last (userSaid), which Copilot's Auto
// picks a model for — not a notice or reminder the agent put in after it.
func lastUserText(from provider.Protocol, body []byte) string {
	req, err := parse(from, body)
	if err != nil {
		return ""
	}
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if m := req.Messages[i]; m.Role == "user" {
			if said := userSaid(m); said != "" {
				return said
			}
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
// asked with system, the system prompt Antigravity turns away, so a failure
// on it isn't held against the account, and the hint that says why.
func markAntigravityTurnsAway(w http.ResponseWriter, system string) {
	if h, ok := w.(*holdWriter); ok {
		h.turnedAway = true
		h.turnedAwayHint = antigravityHint(system)
	}
}

// markAntigravityRefused marks the attempt when what the account said is the
// refusal Antigravity answers such a system prompt with. said is what the
// failure said once the status is gone: the 429's body arrives with its 429,
// and the reply's own error event arrives with 200 from translate and only
// its message, the Code Assist decoder keeping neither its code nor its
// status. An error event of any other kind says something else and marks
// nothing.
// It says whether it marked it.
func markAntigravityRefused(w http.ResponseWriter, p provider.Provider, system, said string) bool {
	if said == "" || !antigravityTurnedAway(p, system, said) {
		return false
	}
	markAntigravityTurnsAway(w, system)
	return true
}

// forward sends a request to the provider. On Anthropic's messages, a
// provider that turns away betas it doesn't know by name (Bedrock's: 400
// Unexpected value(s) `x` for the `anthropic-beta` header) is asked again
// once without them, and they're left out for it from then on. An account's
// 403 or 400 it can mend (Provider.Retry) is asked again, a few times at most.
func (s *Server) forward(ctx context.Context, p provider.Provider, to provider.Protocol, path string, body []byte, in http.Header) (res *http.Response, err error) {
	// the reply it returns says which of its headers go to the agent
	// (#4145), not one asked before it
	defer func() {
		if err == nil {
			heardReply(ctx, p, res)
			heardNet() // the network is up (offline.go)
		}
	}()
	// a way of a key's request that asks outside serve's tries waits for
	// the key's slot here (key_slots.go); taken already, it goes on
	if _, err := keySlot(ctx); err != nil {
		return nil, err
	}
	if p.ImageFilesAt(to) {
		// which images a try named by file (deepseek_files.go)
		ctx = context.WithValue(ctx, dsSentKey{}, &dsSent{})
	}
	res, err = s.forwardOnce(ctx, p, to, path, body, in)
	if err != nil {
		// a Remote magpie whose computer came back at another address
		// is asked there (provider's remote_moved.go)
		if q, ok := provider.SeekMoved(ctx, p, err); ok {
			p = q
			res, err = s.forwardOnce(ctx, p, to, path, body, in)
		}
	}
	if err == nil && res.StatusCode >= 400 {
		// a file DeepSeek no longer holds: uploaded again, or the
		// images sent inline (deepseek_files.go). The file id was
		// magpie's, so this one is asked again whatever the provider's
		// errors setting
		res, err = s.dsRetryGone(ctx, p, to, path, body, in, res)
	}
	if p.PassesErrors() {
		// its errors are the client's to deal with (errors=pass): asked
		// once, never again in another shape
		return res, err
	}
	if err == nil && res.StatusCode >= 400 {
		// a body over the provider's byte cap: asked again with its
		// oldest images left out, or said as a 413 (body_cap.go)
		res, err = s.refitOverCap(ctx, p, to, path, body, in, res)
	}
	if err == nil && mantleRoutes(p, to) {
		res, err = s.mantleReroute(ctx, p, to, path, body, in, res)
	}
	if err == nil {
		res, err = s.retryTokenField(ctx, p, to, path, body, in, res)
	}
	if err == nil && s.refuseThinkingBack(p, to, body, res) {
		// DeepSeek's model behind a name that doesn't say so, asking for
		// a tool turn's thinking another model's turn didn't leave
		// (thinking_back.go): asked once more with it filled in
		res.Body.Close()
		res, err = s.forwardOnce(ctx, p, to, path, body, in)
	}
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
	// each learned once, and the thinking blocks Claude says were signed
	// for another conversation or account (thinking_bound.go)
	for range 3 {
		if err != nil || to != provider.Anthropic || res.StatusCode != http.StatusBadRequest && res.StatusCode != http.StatusUnprocessableEntity {
			return res, err
		}
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		res.Body = io.NopCloser(bytes.NewReader(b))
		sent := s.withoutRefusedShapes(p, body)
		betas := len(s.refuseBetas(p, b)) > 0
		shapes := s.refuseShapes(p, b, body)
		if bound := s.refuseBoundThinking(p, b, sent); !betas && !shapes && !bound {
			return res, nil
		}
		res, err = s.forwardOnce(ctx, p, to, path, body, in)
	}
	return res, err
}

// forwardOnce is one request to the provider, as forward makes it.
func (s *Server) forwardOnce(ctx context.Context, p provider.Provider, to provider.Protocol, path string, body []byte, in http.Header) (*http.Response, error) {
	reqCtx := ctx
	ctx = s.metered(p.Via(ctx), p, "")
	body = deepseekToolPatterns(p, to, body)
	body = grokToolPatterns(p, to, body)
	body = deepseekTokenField(p, to, body)
	body = s.chatTokenField(p, to, body)
	body = toolOneOfAsAnyOf(p, to, body)
	body = copilotGeminiToolsFit(p, to, body)
	body = copilotGeminiToolShapes(p, to, body)
	body = copilotGrokFlat(p, to, body)
	body = kimiToolEnumTypes(p, to, body)
	body = kimiToolRefTypes(p, to, body)
	body = geminiTypes(body, copilotGemini(p, body))
	body = kimiLockedSampling(p, to, body)
	body = clinePin(p, to, body)
	body = codexImagesFit(p, to, body)
	// the tool turns' thinking a provider's model asked for before
	// (thinking_back.go)
	body = s.withThinkingBack(p, to, body)
	// DeepSeek's own API: inline images it holds for this key named by
	// file id, uploaded once (deepseek_files.go)
	body, byFile := s.withDeepSeekFiles(reqCtx, p, to, path, body)
	// under the byte cap the provider refused a larger request over,
	// its oldest images left out (body_cap.go)
	body = s.withinCap(p, to, path, body)
	if to == provider.Responses {
		// whatever the route, Claude's blocks never reach a Responses API
		body = withoutClaudeSeals(body)
	}
	if to == provider.Responses && !keepsAccessPrograms(p, in) {
		// Codex's Cyber program, relayed or built, goes to OpenAI alone
		// (#3172); before the user's own fields, which may name one
		body = withoutAccessPrograms(body)
	}
	if to == provider.Gemini && !p.FactoryGemini() {
		// the model is in a Gemini API's and Vertex AI's path
		// (upstreamPath), as Google's own SDK asks it; Factory's generate
		// route reads it from the body
		body = withoutFields(body, "model")
	}
	var betas []string
	if to == provider.Anthropic {
		body = s.bodyBetas(p, body)
		// what every path to an Anthropic endpoint sends, relayed or
		// built, with the model named as the vendor names it; before the
		// shapes a provider refused are taken out, as it asks a display
		if m := modelOf(body); m == "" || s.fits(p.ID, thinkingNeeded(m), to) {
			// not for a model whose upstream said thinking must be
			// "enabled" (thinkingNeededRefusal): that type it keeps
			body = adaptiveThinking(body)
		}
		body = s.withoutRefusedShapes(p, body)
		// the thinking blocks it refused as another conversation's
		// (thinking_bound.go), every route to it alike
		body = s.withoutRefusedThinking(p, body)
		// and those another vendor signed, to Claude (thinking_bound.go)
		body = s.withoutForeignThinking(p, body)
		if deferredOutUpFront(p, to, body) {
			// Claude Code's deferred-tool blocks, for which Kimi's
			// Anthropic endpoint turns the whole request away with a 400
			// that names nothing to learn from (#2042). DeepSeek's own
			// Anthropic endpoint now takes a tool_addition at no position
			// it can appear in (#5404, a 2026-11-05 check against
			// api.deepseek.com), so its blocks come out up front too,
			// not after a refusal taught it. OpenCode's
			// DeepSeek route hides the refusal behind a 422 "Endpoint
			// is unavailable."; other OpenCode models keep their blocks.
			body = withoutDeferredTools(body)
		}
		body = thinkingOffAsTaken(body, path)
		body = samplingAsTaken(body, path)
		asked := askedBetas(in)
		if gjson.GetBytes(body, "speed").String() == "fast" && provider.HostOf(p.Base(to)) == "api.anthropic.com" {
			asked = append(slices.Clone(asked), claudeFastBeta) // a group's member sent fast
		}
		// auto mode's review goes to Claude alone (automode.go)
		betas, body = s.serverReview(p, asked, body)
		if byFile {
			betas = dsBeta(betas)
		}
	}
	// the conversation ${sessionId} and ${threadId} stand for in the
	// user's own fields and headers for the provider (conversation.go);
	// its Subagent is the request's Claude Code subagent, for
	// ${subagentId} in an extra body value (yetone/magpie#2937)
	conv := provider.Conversation{Session: cmp.Or(sessionOf(in), conversationID(in, body)), Subagent: subOf(in)}
	if p.AsksThread(ctx) {
		conv.Thread = threadOf(in, body)
	}
	ctx = provider.WithConversation(ctx, conv)
	// the agent's own headers the provider lists, which Sign passes on to
	// it alone (PassHeaders)
	ctx = provider.WithClientHeaders(ctx, p.ID, in)
	if !countsTokens(path) {
		// the user's own fields for the provider (#1604), last, so the
		// body is as it goes out when they fill in what it lacks, and
		// before it is signed
		extra := p.WithExtraBody(body, conv)
		if to == provider.Anthropic && !bytes.Equal(extra, body) {
			// a thinking budget the user's fields put in is fitted to a
			// model that thinks only adaptively as the agent's is, its
			// display to the provider
			extra = s.withoutRefusedShapes(p, adaptiveThinking(extra))
		}
		body = extra
	}
	if !countsTokens(path) {
		// the tier it goes at, the user's own fields in it (#1784)
		noteWireTier(ctx, to, body)
	}
	if to == provider.Anthropic && p.IsVertex() {
		// Claude at the project, as Anthropic's Vertex SDK asks it: the
		// model and whether it streams in the path, anthropic_version in
		// the body; last, as the body goes out, and every route to it
		// alike (relayed, built, counted)
		at, b, ok := provider.VertexClaudeRequest(path, body)
		if !ok {
			return nil, errors.New("a request to Claude on Vertex AI names no model")
		}
		path, body = at, b
	}
	base := p.Base(to)
	zenFree := p.IsOpenCode() && p.OpenCodeFree(gjson.GetBytes(body, "model").String())
	if zenFree {
		// Zen's free tier answers at OpenCode 2's paths alone (zenfree.go)
		base = provider.OpenCodeFreeURL(base, to)
	}
	if mantleRoutes(p, to) {
		// Bedrock Mantle serves each model under /v1 or /openai/v1
		// (mantle.go)
		m := gjson.GetBytes(body, "model").String()
		base = provider.MantleBase(base, m, !s.fits(p.ID, mantleRouteKey(m), to))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, nil)
	if err != nil {
		return nil, err
	}
	// the body goes once it is answered, not when a stream ends (#3383)
	provider.SetBody(req, body)
	defer provider.LetGo(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	// the agent's own where the provider passes it on, else magpie's
	// (#1851); a signed-in account's signing, a client the vendor serves
	// (OpenCode's, Cline's, Kilo's) and the user's own header put theirs
	// on after
	ua := agentUA(ctx, in)
	req.Header.Set("User-Agent", userAgentFor(p, ua))
	if v := mistralAffinity(p, in, conv.Session); v != "" {
		req.Header.Set(mistralAffinityHeader, v)
	}
	if to == provider.Anthropic {
		req.Header.Set("anthropic-version", "2023-06-01")
		for _, h := range []string{"anthropic-version", "anthropic-beta"} {
			if v := in.Get(h); v != "" {
				req.Header.Set(h, v)
			}
		}
		if p.Account == nil && claudeCodeUA(ua) {
			// a relay that serves only Claude Code (#179: "only accessible
			// via the official Claude CLI") knows it by its own headers,
			// which go on as it sent them; its key to magpie never does.
			// Its User-Agent is the agent's (agentUA): from another
			// magpie, the one that magpie named, not magpie's own
			for k, vs := range in {
				if claudeCodeHeader(k) {
					req.Header[k] = slices.Clone(vs)
				}
			}
			req.Header.Set("User-Agent", ua)
		}
		if len(betas) > 0 {
			req.Header.Set("anthropic-beta", strings.Join(betas, ","))
		} else {
			req.Header.Del("anthropic-beta")
		}
	}
	if p.Account == nil && (to == provider.Responses || to == provider.Chat) && fromCodex(in) {
		// a relay that serves only Codex (Discord: "This account only
		// allows Codex official clients") knows it by its User-Agent,
		// originator and x-codex- headers, which go on as Codex sent them,
		// as they do when Codex talks to the relay itself; its key to
		// magpie never does. A test asked as Codex asks with them
		// (provider.TestAs)
		seen := http.Header{}
		lite := liteKept(p, to, true, modelOf(body))
		for k, vs := range in {
			if !lite && strings.EqualFold(k, liteHeader) {
				// the body left Lite (fromLite), so does its header
				continue
			}
			if codexClientHeader(k) {
				req.Header[k] = slices.Clone(vs)
				seen[k] = vs
			}
		}
		if ua != "" {
			// the agent's, as agentUA has it: from another magpie, the
			// one that magpie named, not magpie's own
			req.Header.Set("User-Agent", ua)
			seen.Set("User-Agent", ua)
		}
		if codexNamed(in) {
			provider.SawClient(provider.ClientCodex, seen)
		}
	}
	if p.Account == nil && biliProxied(req.URL.Path) {
		// a billion-context proxy put in front of the provider (#3052)
		// knows a cooperative plugin's session by its x-bili- headers,
		// which go on as the agent sent them: the plugin's name, the
		// conversation and the context window it serves. None of them is
		// a key. Only to a URL in bili's /bili/<upstream> form, the one
		// its README gives for a gateway: a vendor or a signed-in
		// account is never sent them.
		for k, vs := range in {
			if biliHeader(k) {
				req.Header[k] = slices.Clone(vs)
			}
		}
	}
	if provider.OpenCodeURL(req.URL.String()) {
		if zenFree {
			// only what OpenCode 2.2.5 sends its gateway: an agent's own
			// headers (Claude Code's x-stainless-*, Codex's originator)
			// would mark the request as not OpenCode's (#3491)
			kept := http.Header{}
			for _, k := range []string{"Content-Type", "Anthropic-Version", "Anthropic-Beta"} {
				if v := req.Header.Values(k); len(v) > 0 {
					kept[k] = v
				}
			}
			req.Header = kept
		}
		// as OpenCode itself sends it, which Zen's free tier asks for,
		// with the conversation's own session, which Zen and Go route and
		// cache by and answer 400 without, from the folder the agent says
		// it works in
		provider.OpenCodeClient(req.Header, openCodeSeed(in, body), openCodeDir(factsOf(to, body).dir))
		if v := in.Get("x-opencode-project"); v != "" {
			// OpenCode's own, asking through magpie
			req.Header.Set("x-opencode-project", v)
		}
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
		// the session and subagent the agent named, for a plugin that
		// keeps state per conversation (plugins#620)
		req.Header.Del(provider.SessionToPluginHeader)
		req.Header.Del(provider.SubagentToPluginHeader)
		if v := sessionOf(in); v != "" {
			req.Header.Set(provider.SessionToPluginHeader, v)
		}
		if v := subOf(in); v != "" {
			req.Header.Set(provider.SubagentToPluginHeader, v)
		}
		tellWorkPlace(req.Header, to, body)
		req.Header.Del(provider.FallbackHeader)
		if hasFallback(ctx) {
			req.Header.Set(provider.FallbackHeader, "1")
		}
	}
	if p.IsRemoteMagpie() {
		// as magpie, whatever a copy above put on (Claude Code's or
		// Codex's own, #179): the other magpie takes X-Magpie-* — the
		// agent, the computer, the hops that stop a loop, the cache
		// lifetime, the agent's User-Agent — only from a magpie's request
		// (magpieAsks; linkaww)
		req.Header.Set("User-Agent", magpieUA())
		passOnCaller(ctx, req)
		passOnUA(req, ua)
		markSearching(ctx, req)
	}
	if err := p.Sign(ctx, req, to, body); err != nil {
		return nil, err
	}
	// a sign-in its vendor revoked isn't sent again: the next member is
	// asked, and the agent told to sign in again (#1793)
	if why, ok := provider.RevokedSignIn(p.Account, req); ok {
		return revokedReply(req, why), nil
	}
	if to == provider.Anthropic {
		s.fitUserBetas(p, req.Header)
	}
	sent := len(body)
	if p.Account != nil && p.Account.Agent == "codex" {
		// a turn goes zstd-compressed, as Codex's own client sends it;
		// sent plain, ChatGPT answered a 1.1 MB one 503 (ylorn on X)
		sent = provider.CodexCompressed(req, body)
	}
	// the bytes go on the wire whether the vendor answers or not, counted
	// for the request's ledger row (#4456)
	sentUp(ctx, sent, len(body))
	// a request asked again in the same ask (RetryOneAuth and the others
	// below) goes on the wire again too, as long as it went
	again := func(r *http.Request) (*http.Response, error) {
		n := sent
		if r.ContentLength > 0 {
			n = int(r.ContentLength)
		}
		sentUp(ctx, n, len(body))
		res, err := p.Do(s.client, r)
		return gotDown(ctx, res), err
	}
	res, err := p.Do(s.client, req)
	// what comes back is counted as it is read, as what went is (#4456)
	res = gotDown(ctx, res)
	if err == nil && res.StatusCode >= 400 && provider.OpenCodeURL(req.URL.String()) {
		logOpenCodeRefusal(p, req, res.StatusCode)
	}
	if err == nil && to == provider.Anthropic {
		// a vendor that takes the key in x-api-key or Authorization
		// alone, refusing both (Aloha on Discord): asked again with one
		res, err = provider.RetryOneAuth(p, req, res, again)
	}
	if err == nil && to == provider.Chat {
		// a GLM Coding Plan's key at Zhipu's or Z.ai's pay-as-you-go
		// endpoint, refused there for want of a balance (1113): asked at
		// the plan's endpoint, and the provider moved there when it serves
		// it; else the refusal says what to change (@cleverpigo on X)
		res, err = provider.RetryZhipuCoding(p, req, res, again)
	}
	if err == nil && provider.OpenCodeURL(req.URL.String()) {
		// a model OpenCode Go answers "Model is unavailable": one of Zen's
		// free models asked at Zen's free tier, signed out as OpenCode
		// asks it; else the refusal says where it is served (51bit.eth on
		// Discord)
		res, err = provider.RetryOpenCodeFreeOnZen(p, to, req, res, again)
	}
	if err != nil {
		return res, err
	}
	// Cline's whole replies come in {success, data}; a web page or
	// nothing at all, served 200, is the 502 it stands for (#1012)
	// OpenCode's gateway refusing a request before the model was asked,
	// a 400 whose body is no error at all (#3740), is the 502 such a
	// fault of a moment stands for
	// Zhipu's and Z.ai's Responses API refuses a key 200 in the platform's
	// envelope: the 401 (or 429 …) it stands for, so the next key or
	// member is asked and the agent told why (@xanahopper on X)
	res = notAnAPIReply(clineUnwrapped(p, provider.ZhipuEnveloped(req, openCodeBareRefusal(req, res))), "")
	if p.Account != nil && p.Account.Agent == "codex" {
		// a large request ChatGPT closed the connection on: asked again,
		// nobody resting for it, and said what may help (#1628)
		res = codexDropped(res, sent)
	}
	// what a Codex account's reply says it has used, for its cap (#1295)
	heardCodexLimits(p, res)
	noteSignIn(ctx, p.Account, req, res)
	return res, nil
}

// countsTokens is a path that only counts a request's tokens (Anthropic's
// count_tokens): no reply is asked, so a provider's extra body fields, for
// asking one, aren't sent there.
func countsTokens(path string) bool {
	return strings.Contains(path, "count_tokens")
}

// fromClaudeCode is a request Claude Code sent, by the User-Agent it gives
// (claude-cli/2.1.0 (external, cli)).
func fromClaudeCode(in http.Header) bool {
	return claudeCodeUA(in.Get("User-Agent"))
}

// claudeCodeUA is Claude Code's User-Agent.
func claudeCodeUA(ua string) bool {
	return strings.HasPrefix(ua, "claude-cli/")
}

// claudeCodeAsks is whether r is Claude Code's: by its User-Agent, or,
// from another magpie, by the agent's that magpie named (agentUA).
func claudeCodeAsks(r *http.Request) bool {
	return claudeCodeUA(agentUA(r.Context(), r.Header))
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

// fromCodex is a request Codex sent — its CLI, exec, the IDE extension,
// desktop app or ACP adapter — by the User-Agent (codex_cli_rs/0.159.2
// (Mac OS 26.6.0; arm64) kitty, Codex Desktop/0.162.3 …) or the originator it gives
// (codex_cli_rs, codex_exec, codex_vscode, Codex Desktop, acp-extension-codex).
// VS Code's Agents window runs `codex app-server` under the name
// vscode_agent_host, which Codex sends as both: those are Codex's turns
// too, though they are counted under VS Code (agent.vscodeAgentHostUA).
// Without it one of Codex's own models asked there was answered 404
// "magpie knows no model" rather than going on Codex's sign-in
// (codexOwnOffMagpie; justruman on Discord).
func fromCodex(in http.Header) bool {
	return codexNamed(in) || hostedCodex(in)
}

// codexNamed is a request whose User-Agent or originator is one of Codex's
// own names, not an app's that runs it: only those are Codex's shape for
// a test asked as Codex (provider.SawClient).
func codexNamed(in http.Header) bool {
	for _, v := range []string{in.Get("User-Agent"), in.Get("originator")} {
		v = strings.ToLower(v)
		if strings.HasPrefix(v, "codex") || strings.HasPrefix(v, "acp-extension-codex") {
			return true
		}
	}
	return false
}

// hostedCodex is a Codex an app runs under the app's own name and that
// name only Codex is run under: VS Code's agent host (its codexAgent.ts
// CLIENT_INFO). An app that runs Codex as its own engine under a name it
// uses for more than that (Lody) is not told apart this way.
func hostedCodex(in http.Header) bool {
	for _, v := range []string{in.Get("User-Agent"), in.Get("originator")} {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(v)), "vscode_agent_host") {
			return true
		}
	}
	return false
}

// codexAsSent says a Responses request goes to p as Codex sent it, its
// model name alone swapped: Codex's own, to a relay set codex=as-sent,
// which checks that a request is Codex's own (#1758).
func codexAsSent(p provider.Provider, in http.Header) bool {
	return p.CodexAsSent && p.Account == nil && fromCodex(in)
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

// biliHeader is one of the headers a billion-context (bili) proxy put in
// front of the provider asks a cooperative plugin by: x-bili-plugin, its
// conversation and its context window, all x-bili- prefixed and none a key.
func biliHeader(k string) bool {
	return strings.HasPrefix(strings.ToLower(k), "x-bili-")
}

// biliProxied is whether a request's path goes through a billion-context
// proxy in its prefix form, http://host:8787/bili/<the upstream's URL>.
func biliProxied(path string) bool {
	return strings.HasPrefix(path, "/bili/")
}

// passthrough relays a request the provider understands as-is, with the
// model name swapped for the provider's own. The token counts the reply
// carries are read on the way past into u. done is false, with nothing
// written, when the provider serves the model on another of its endpoints
// but not this one.
func (s *Server) passthrough(w http.ResponseWriter, r *http.Request, p provider.Provider, proto provider.Protocol, model string, body []byte, u *Usage) (status int, msg string, done bool) {
	// the headers the user gave the model, on p's requests for it
	r = r.WithContext(provider.WithModel(r.Context(), p.ID, model))
	// the reply limit the client named, read before the body is rewritten:
	// a reply cut at its length that said nothing is a void to fail at
	// once, unless it was asked for little (#2972)
	capped := 0
	if proto == provider.Chat {
		capped = int(gjson.GetBytes(body, "max_tokens").Int())
		if capped == 0 {
			capped = int(gjson.GetBytes(body, "max_completion_tokens").Int())
		}
	}
	upstream := provider.UpstreamNameIn(wiresOf(r.Context()), p.ID, model)
	body = rewriteModel(body, upstream)
	if proto == provider.Chat {
		body = withMistralCacheKey(p, r.Header, body)
	}
	if proto == provider.Responses {
		// Claude's blocks magpie gave a Responses client go back to Claude
		// alone; to this API the item is its summary, as before
		body = withoutClaudeSeals(body)
	}
	// a client that asked for one body, of an account that only streams:
	// asked streamed, the stream read here (gathered)
	var gather *gathered
	if gathers(p, proto, body) {
		gather = &gathered{h: http.Header{}, status: http.StatusOK, capped: int(gjson.GetBytes(body, "max_output_tokens").Int())}
		body = withFields(body, map[string]any{"stream": true})
	}
	searchFn := false            // Codex's tool search sent as a function
	geminiWhole := false         // a Gemini client's generateContent
	var toolSeen map[string]bool // the call ids the client holds (tool_use_ids.go)
	// a model that wants its reasoning text back (#388, #1104)
	replay := proto == provider.Responses && (replaysReasoning(model, p.VendorHost()) || replaysReasoning(upstream, p.VendorHost()))
	switch proto {
	case provider.Responses:
		if codexAsSent(p, r.Header) {
			// as Codex sent it, its model name alone swapped: a relay
			// that checks it is Codex's own turns away the shape fitted
			// for other backends (#1758)
			break
		}
		if p.RejectsTemperature(model) {
			// a model whose list refuses them (a plugin's temperature:
			// false, #2492), as the translation of one leaves them out
			body = withoutFields(body, "temperature", "top_p")
		}
		// Responses Lite's shape, its tools as an input item (#350) and its
		// reasoning context, goes only to OpenAI; anyone else is sent what
		// Codex sends a model that isn't Lite
		if !liteKept(p, proto, fromCodex(r.Header), modelOf(body)) {
			body = fromLite(body)
		}
		// only the ChatGPT backend runs Codex's tool search as Codex sends it
		if p.Account == nil || p.Account.Agent != "codex" {
			body, searchFn = searchAsFunction(body)
		}
		body = forVendor(p, body, asksCompaction(r.Context()))
		// Ark takes web_search in the shape its docs give, and only the include
		// values it knows (search_ark.go)
		body = forArkSearch(p, body)
		if replay {
			// its reasoning goes back as text, never left out: DeepSeek
			// refuses a request without it (#1104)
			body = withReasoningText(body)
		} else {
			if strings.HasSuffix(p.VendorHost(), "openai.com") || p.IsAzure() || !s.fits(p.ID, bareReasoningRefused(model), proto) {
				// reasoning magpie gave Codex, an id with nothing sealed in
				// it, which they'd look up and not find (#1008), nor does an
				// upstream that has turned it away before (#1044)
				body = withoutBareReasoning(body)
			}
			if strings.HasSuffix(p.VendorHost(), "openai.com") || p.IsAzure() || p.Account != nil && p.Account.Agent == "codex" || !s.fits(p.ID, reasoningTextRefused(model), proto) {
				// another vendor's reasoning with its text in it (a
				// routing group's DeepSeek turn), which OpenAI and the
				// ChatGPT backend take only empty, nor does an upstream
				// that has turned it away before (#1411)
				body = withoutReasoningText(body)
			}
			if strings.HasSuffix(p.VendorHost(), "openai.com") || p.IsAzure() || p.Account != nil && p.Account.Agent == "codex" || !s.fits(p.ID, citationOffsetRefused(model), proto) {
				// a citation another vendor's reply wrote without the
				// offsets OpenAI's schema wants (Volcengine Ark's
				// url_citation, #4921), which the ChatGPT backend
				// refuses the whole replay over, nor does an upstream
				// that has turned one away before
				body = withoutOffsetlessCitations(body)
			}
		}
		// Relays enforce OpenAI's item ID prefixes too, including during
		// compaction. call_id stays unchanged so tool outputs remain paired.
		body = callItemIDs(body)
		// and an item id with a character OpenAI's don't have, another
		// vendor's (Muse Spark's "rs_…:rs_…"), to an upstream that checks
		// them (echo_ts)
		if validatesItemIDs(p) || !s.fits(p.ID, itemIDsRefused(model), proto) {
			body = plainItemIDs(body)
		}
		// xAI's API turns away a tool_choice with no tools beside it ("A
		// tool_choice was set on the request but no tools were specified"),
		// and Copilot's /responses, in front of it for Grok, with a bare
		// 400: Codex's compaction summary goes without its tools (#378).
		// Anywhere else it goes as asked, which a relay checking Codex's
		// shape wants (#292).
		if refusesLoneToolChoice(p) {
			body = withoutLoneToolChoice(body)
		}
	case provider.Chat:
		if p.RejectsTemperature(model) {
			// a model whose list refuses them (a plugin's temperature:
			// false, #2492): a translation leaves them out (build), so
			// does the relay of one the provider understands as it came
			body = withoutFields(body, "temperature", "top_p")
		}
		body = developerAsSystem(body)
		// a content of text parts, which a vendor's server that takes a
		// string alone turns the whole request away for (Atria on Discord)
		body = textPartsAsString(body)
		// a turn the client cut off before it said anything, which
		// DeepSeek and Kimi turn every later request away for (#2599)
		body = silentAssistantsOut(body)
		// a call no tool message answers, or an answer to no call, which
		// Kimi and OpenAI turn the whole request away for: what a fork
		// at an earlier turn or a stopped reply leaves (Paidaixinbao on X)
		body = toolPairsOut(body)
		if mistral := p.VendorPreset() == "mistral" || p.VendorHost() == "api.mistral.ai"; !mistral && (chatReplaysReasoning(buildHost(p), model) || chatReplaysReasoning(buildHost(p), upstream)) {
			// a tool call the client kept no thinking for, which DeepSeek
			// turns the whole request away for (#1462)
			body = withReasoningOnToolTurns(body)
		}
		// a client that took the gateway for an Ollama server (its
		// /api/tags and /api/show are answered in Ollama's shape) sends
		// Ollama's per-request options beside its Chat fields: Hermes
		// Agent's {"num_ctx": …} from /api/show's context length. No Chat
		// API reads them, Ollama's own /v1 included (its
		// ChatCompletionRequest has no options), and a strict one turns
		// the whole request away: Gemini's OpenAI-compatible API over a
		// name it doesn't know (#3097), Mistral with 422 extra_forbidden
		// (lijiho96940561 on X). Their runner settings have no place
		// there, as the Ollama route's own body builder judges
		// (ollamaChatBody), so no upstream is sent them
		body = withoutOllamaOptions(body)
		compat := geminiCompat(p.VendorHost(), model)
		// Gemini's thought signatures, which came to the client in its
		// calls' ids, go back where Gemini wants them; another upstream
		// gets the calls' own ids (#687)
		body = chatCallSignatures(body, compat, p.ID, true)
		if p.VendorPreset() == "mistral" || p.VendorHost() == "api.mistral.ai" {
			// an earlier turn's reasoning_content, which Mistral turns
			// away, as the thinking part it takes (#494)
			body = mistralThinking(body)
			body = mistralImageLimit(body)
		}
		if isKimi(p, proto) {
			// another vendor's reasoning_details (MiMo's, with a stream's
			// index in each entry), which Kimi turns away in any shape:
			// its thinking as the reasoning_content Kimi takes (#1765)
			body = reasoningDetailsAsContent(body)
		}
		if strings.HasSuffix(p.VendorHost(), "openai.com") || p.IsAzure() {
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
		geminiWhole = !streamOf(body)
		body = withoutFields(body, "stream")
		// a signature another provider gave goes to none (ownSignatures)
		body = geminiBodySignatures(body, p.ID, true)
	case provider.Anthropic:
		// what the client holds, taken before the upstream gets its own
		// call ids back below, and those given back to it (tool_use_ids.go,
		// #4910)
		toolSeen = clientToolUseIDs(body)
		body = saltedCallIDsBack(body)
		// Claude Code's advisor, named by magpie's id (advisor.go)
		body = advisorTools(body, p, upstream, wiresOf(r.Context()), keyAllows(r, p))
		if p.IsBedrock() {
			// Claude Code's metadata.user_id, a JSON string these days, is
			// not the plain id Bedrock checks it against (#176)
			body = withoutFields(body, "metadata")
		}
		if dropsBillingLine(p, proto, model, upstream) {
			// Claude Code's billing line, which changes every turn, to a
			// vendor's model: its prompt cache missed from the first
			// byte (#1600, billing_line.go)
			body = withoutBillingLine(body)
		}
		if anthropicReplaysThinking(p.VendorHost(), model) || anthropicReplaysThinking(p.VendorHost(), upstream) {
			// a tool_use turn with no thinking, or Claude's redacted
			// thinking (a routing group's Claude turn), which DeepSeek
			// turns the whole request away for (#1611)
			body = withThinkingOnToolTurns(body)
		}
		if !anthropicModel.MatchString(model) && !provider.Reasonless(p.ID, model) {
			body = thinkingOffUnlessAsked(body)
			// Claude Code's auto mode classifier, on a vendor's model that
			// may think whatever it is told (#250); a Claude model's request
			// goes as it was sent, safeguards and all
			body = autoModeClassifierBody(model, body)
		}
		if outputEffortAt(p.VendorHost(), model) {
			levels := p.Efforts(model)
			if len(levels) == 0 && p.VendorHost() == deepseekHost {
				levels = deepseekEfforts
			}
			body = withOutputEffort(body, levels)
		}
		if !s.fits(p.ID, offRefused(model), proto) {
			// a model that refused thinking turned off before (below):
			// left to the model from the start, not refused again on
			// every request (auto mode's classifier asks on every action)
			if nb, ok := withoutThinkingOff(body); ok {
				body = nb
			}
		}
		if !s.fits(p.ID, thinkingNeeded(model), proto) {
			// a model whose upstream said thinking must be enabled (below):
			// sent thinking from the start, not refused on every request
			if nb, ok := withThinkingEnabled(body); ok {
				body = nb
			}
		}
	}
	// the effort as the agent sent it, fitted to the model's levels: Qoder's
	// permission check asks "none", which Command Code turns away; and
	// reasoning off at a level the model turned away before (below), at its
	// lowest
	asked := bodyEffort(proto, body)
	effort := fitFor(p, model, asked)
	if offEffort(effort) && s.turnedAway(p, model, effort, proto) {
		effort = onEffort(p, model)
	}
	if asked != "" && effort != asked {
		body = withBodyEffort(proto, body, effort)
	}
	path := upstreamPath(p, proto, upstream)
	if proto == provider.Gemini && p.IsVertex() && geminiWhole {
		path = provider.VertexWholePath(upstream) // answered as one JSON body
	}
	if proto == provider.Anthropic && p.Account == nil && claudeCodeAsks(r) && r.URL.Query().Get("beta") == "true" {
		path += "?beta=true" // as Claude Code asks it
	}
	if proto != provider.Anthropic {
		body = s.withoutRefused(p.ID, proto, body)
	}
	// a member of Codex's passthrough the upstream turned away before
	// (passthrough_refused.go)
	body = s.withoutRefusedPassthrough(p.ID, proto, body)
	// a conversation the client ended with the assistant's message, to a
	// model that took no prefill before, is asked with a user turn after
	// it (prefill.go, #1447)
	turnAfter := (proto == provider.Chat || proto == provider.Responses) && !s.fits(p.ID, prefillRefused(model), proto)
	if turnAfter {
		body, _ = userLastBody(proto == provider.Chat, body)
	}
	// Codex's image tool, which a vendor that knows no namespaces turned
	// away before (#949)
	dropImage := proto == provider.Responses && mayDropImageTool(p)
	if dropImage && !s.fits(p.ID, imageToolRefused, proto) {
		body, _ = withoutImageTool(body)
	}
	// a tool with a union at its parameters' root, to an upstream that
	// takes none (object_root.go, #1271)
	if s.foldsRoots(p, model, proto) {
		body, _ = objectRootsBody(proto, body)
	}
	// a tool's strict, to an upstream that turned it away before
	// (strict_refused.go, #1980)
	if !s.fits(p.ID, toolStrictRefused, proto) {
		body, _ = withoutToolStrict(proto, body)
	}
	// a stub for each tool the history called and tools lacks, to an
	// upstream that turned such a call away before (undeclared_tools.go)
	if !s.fits(p.ID, historyToolsRefused, proto) {
		body, _, _ = withHistoryTools(proto, body)
	}
	// structured output an upstream turned away before (#2126): a schema
	// as any JSON object, or the format told in the system prompt alone
	if !s.fits(p.ID, formatRefused(model), proto) {
		body, _ = formatInBody(proto, body, false)
	} else if !s.fits(p.ID, schemaRefused(model), proto) {
		body, _ = formatInBody(proto, body, true)
	}
	// xAI's own API takes neither a sealed reasoning item's null content
	// nor a system turn in Messages (xai_body.go, #3392)
	body = xaiBody(p, proto, body)
	// a Grok subscription is given Codex's namespaced functions flat
	// (grokBody), and Zed's plugin likewise (ZedBody); a call to one goes
	// back under its namespace (#404). Any vendor asked as Codex sends it
	// has its namespaced calls read by Codex: one without the empty
	// encrypted_function_args list reads as sealed (#3183). OpenAI's own
	// API and a ChatGPT account answer the mark themselves, and their
	// replies go back as they came.
	var named map[string]nsTool
	if proto == provider.Responses && !sealsItsOwn(p) {
		named = namespacedIn(body)
	}
	// a ChatGPT account is given a declared tool whose name its backend
	// refuses under another; a call to it goes back by the agent's
	// (codex_names.go)
	var renamed map[string]string
	if proto == provider.Responses && accountAgent(p) == "codex" {
		renamed = codexRenamedIn(body)
	}
	// the new input alone, after the id the provider keeps the previous
	// response under, when it answered that one (prev_response.go)
	// and when not, the items it brought back without the ids that name
	// them where they were made, when that was another provider or key
	asSent, sentWhole, delta := body, body, false
	var remote *remoteTurn
	if proto == provider.Responses {
		sentWhole = prevElsewhere(r, p, body, false)
		if body, delta = prevDelta(r, p, body); !delta {
			body = sentWhole
		}
		// and to another magpie, which Codex's store false would leave
		// sent whole each turn: asked to keep it, and sent the input after
		// the turn it kept last (remote_delta.go)
		if remote = remoteTurnFor(r, p, body); !delta && remote != nil {
			r = r.WithContext(withKeepTurn(r.Context()))
			body, delta = remote.delta(body)
		}
	}
	res, err := s.forward(r.Context(), p, proto, path, p.Prepare(body), r.Header)
	if err != nil {
		return writeError(w, proto, 502, p.Name+": "+err.Error()), err.Error(), true
	}
	if res.StatusCode == http.StatusBadRequest && (proto == provider.Chat || proto == provider.Gemini) && signaturesReach(p, proto, upstream) {
		// a signature Google won't take (signatureRefused), one given
		// before magpie noted whose it was: asked again once without
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		res.Body = io.NopCloser(bytes.NewReader(b))
		if signatureRefused.Match(b) {
			bare := geminiBodySignatures(body, p.ID, false)
			if proto == provider.Chat {
				bare = chatCallSignatures(body, true, p.ID, false)
			}
			if !bytes.Equal(bare, body) {
				body = bare
				if res, err = s.forward(r.Context(), p, proto, path, p.Prepare(body), r.Header); err != nil {
					return writeError(w, proto, 502, p.Name+": "+err.Error()), err.Error(), true
				}
			}
		}
	}
	if (delta || proto == provider.Responses && prevHeld(r)) && (badRequest(res.StatusCode) || res.StatusCode == http.StatusNotFound) {
		// one it no longer holds: the conversation whole; and one another
		// of its resources made (prevForeign): the whole, with the items
		// magpie brought back naming nothing. Asked again once, and only
		// with a body that differs.
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		res.Body = io.NopCloser(bytes.NewReader(b))
		next := body
		switch {
		case proto == provider.Responses && prevForeign(res.StatusCode, b):
			next = prevElsewhere(r, p, asSent, true)
		case delta && prevRefused(res.StatusCode, b):
			next = sentWhole
		}
		if !bytes.Equal(next, body) {
			body = next
			if delta && remote != nil {
				remote.refused()
			}
			if res, err = s.forward(r.Context(), p, proto, path, p.Prepare(body), r.Header); err != nil {
				return writeError(w, proto, 502, p.Name+": "+err.Error()), err.Error(), true
			}
		}
	}
	if proto == provider.Responses {
		route := ""
		if prevKeptBy(p) && !storeOff(body) {
			route = prevRoute(p)
		}
		noteAnswered(r, route, prevRoute(p))
	}
	if res.StatusCode >= 400 && p.PassesErrors() {
		// the user leaves its errors to the client (errors=pass): the
		// vendor's error goes as it came, and nothing is asked again in
		// another shape. Above, magpie only took back its own: a
		// signature it gave, the previous response it named
		defer res.Body.Close()
		u.RequestID = requestID(res.Header)
		status, msg := passError(w, p, proto, proto, res, u)
		return status, msg, true
	}
	// structured output turned away (Xiaomi MiMo's Token Plan takes no
	// json_schema, only text and json_object, #2126): a schema is asked
	// for as any JSON object with the schema in the system prompt, and if
	// that is turned away too the format is told there alone; each, once
	// it is taken, from then on. What the client asked for still reaches
	// the model, in words
	for _, object := range []bool{true, false} {
		if !badRequest(res.StatusCode) || bodyFormat(proto, body) == nil {
			break
		}
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		res.Body = io.NopCloser(bytes.NewReader(b))
		if !refusesFormat(res.StatusCode, b) {
			break
		}
		nb, ok := formatInBody(proto, body, object)
		if !ok {
			continue
		}
		mark := formatRefused(model)
		if object {
			mark = schemaRefused(model)
		}
		body = nb
		if res, err = s.forward(r.Context(), p, proto, path, p.Prepare(body), r.Header); err != nil {
			return writeError(w, proto, 502, p.Name+": "+err.Error()), err.Error(), true
		}
		if res.StatusCode < 400 {
			s.markUnfit(p.ID, mark, proto)
		}
	}
	// a history call to a tool the request doesn't declare, or calls with
	// no tools, turned away (pi's and T3 Code's tool_search,
	// undeclared_tools.go): asked once more with a stub declared for each
	// such tool, and so from then on
	if badRequest(res.StatusCode) && s.fits(p.ID, historyToolsRefused, proto) {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		res.Body = io.NopCloser(bytes.NewReader(b))
		if nb, names, toolless := withHistoryTools(proto, body); len(names) > 0 && refusesHistoryTools(b, names, toolless) {
			body = nb
			if res, err = s.forward(r.Context(), p, proto, path, p.Prepare(body), r.Header); err != nil {
				return writeError(w, proto, 502, p.Name+": "+err.Error()), err.Error(), true
			}
			if res.StatusCode < 400 {
				s.markUnfit(p.ID, historyToolsRefused, proto)
			}
		}
	}
	// a vendor that turns away the fields it doesn't know (Mistral's
	// extra_forbidden for OpenCode's store, #393) is asked again without
	// the optional ones it named, and not sent them again once that works
	var refused []string
	for proto != provider.Anthropic && badRequest(res.StatusCode) {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		res.Body = io.NopCloser(bytes.NewReader(b))
		if proto == provider.Responses && customToolRefusal.Match(wordsOf(b)) && hasCustomTool(body) {
			// a custom tool turned away (DeepSeek's "Unsupported custom
			// tool: 'exec'", #2656): the request is translated on the same
			// API, its custom tools offered as functions (custom_tool.go),
			// and from then on
			res.Body.Close()
			s.markUnfit(p.ID, customToolRefused(model), proto)
			return res.StatusCode, p.Name + ": " + provider.APIError(b, res.Status), false
		}
		if gather == nil && refusesWhole(proto, body, b) {
			// a backend that only streams (stream_only.go): the request
			// is translated on the same API, which asks it streamed and
			// gives the client one body, and so from then on
			res.Body.Close()
			s.markUnfit(p.ID, streamOnlyRefused(model), proto)
			return res.StatusCode, p.Name + ": " + provider.APIError(b, res.Status), false
		}
		fs := refusedOptional(res.StatusCode, b, body)
		// and an earlier turn's thinking sent back inside messages, which
		// DeepSeek and Kimi want and a stricter vendor turns away (#494)
		var ms []string
		if proto == provider.Chat {
			ms = refusedInMessages(res.StatusCode, b, body)
		}
		if len(fs) == 0 && len(ms) == 0 && !turnAfter && refusesPrefill(res.StatusCode, b) {
			// the model takes no prefill (#1447: Volcengine's Agent Plan
			// for Codex's resumed reply): the conversation is asked once
			// more with a user turn after the assistant's message, and so
			// from then on
			if nb, ok := userLastBody(proto == provider.Chat, body); ok {
				turnAfter = true
				refused = append(refused, prefillRefused(model))
				body = nb
				if res, err = s.forward(r.Context(), p, proto, path, p.Prepare(body), r.Header); err != nil {
					return writeError(w, proto, 502, p.Name+": "+err.Error()), err.Error(), true
				}
				continue
			}
		}
		if len(fs) == 0 && len(ms) == 0 && rootUnionRefusal.Match(b) {
			// a tool's parameters refused for a union at their root
			// (#1271): asked once more with them folded to an object
			if nb, changed := objectRootsBody(proto, body); changed {
				refused = append(refused, rootUnionRefused(model))
				body = nb
				if res, err = s.forward(r.Context(), p, proto, path, p.Prepare(body), r.Header); err != nil {
					return writeError(w, proto, 502, p.Name+": "+err.Error()), err.Error(), true
				}
				continue
			}
		}
		if len(fs) == 0 && len(ms) == 0 && toolStrictRefusal.Match(b) {
			// a tool's strict refused, OpenCode's "strict": false (#1980):
			// asked once more without it, and not sent it again
			if nb, changed := withoutToolStrict(proto, body); changed {
				refused = append(refused, toolStrictRefused)
				body = nb
				if res, err = s.forward(r.Context(), p, proto, path, p.Prepare(body), r.Header); err != nil {
					return writeError(w, proto, 502, p.Name+": "+err.Error()), err.Error(), true
				}
				continue
			}
		}
		if len(fs) == 0 && len(ms) == 0 && proto == provider.Responses {
			// a member of Codex's passthrough metadata the backend doesn't
			// know yet (passthrough_refused.go): asked once more without
			// it, and not sent it again
			if f, ok := refusedPassthrough(b); ok && s.fits(p.ID, passthroughRefused(f), proto) {
				if nb, had := withoutPassthrough(body, f); had {
					refused = append(refused, passthroughRefused(f))
					body = nb
					if res, err = s.forward(r.Context(), p, proto, path, p.Prepare(body), r.Header); err != nil {
						return writeError(w, proto, 502, p.Name+": "+err.Error()), err.Error(), true
					}
					continue
				}
			}
		}
		if len(fs) == 0 && len(ms) == 0 && proto == provider.Responses && !replay && refusesReasoningText(res.StatusCode, b, body) {
			// and reasoning with its text in it, which a relay in front
			// of OpenAI turns away as OpenAI does ("Invalid
			// 'input[n].content': array too long", #1411): asked once
			// more without it, and not sent it again
			if nb := withoutReasoningText(body); !bytes.Equal(nb, body) {
				refused = append(refused, reasoningTextRefused(model))
				body = nb
				if res, err = s.forward(r.Context(), p, proto, path, p.Prepare(body), r.Header); err != nil {
					return writeError(w, proto, 502, p.Name+": "+err.Error()), err.Error(), true
				}
				continue
			}
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
		if len(fs) == 0 && len(ms) == 0 && proto == provider.Responses && refusesCitationOffsets(res.StatusCode, b) {
			// and a citation without the offsets OpenAI's schema wants,
			// which the ChatGPT backend and the relays in front of it
			// turn the whole replay away over ("Missing required
			// parameter: 'input[n].content[m].annotations[k].start_index'",
			// #4921): asked once more without it, and not sent one again
			if nb := withoutOffsetlessCitations(body); !bytes.Equal(nb, body) {
				refused = append(refused, citationOffsetRefused(model))
				body = nb
				if res, err = s.forward(r.Context(), p, proto, path, p.Prepare(body), r.Header); err != nil {
					return writeError(w, proto, 502, p.Name+": "+err.Error()), err.Error(), true
				}
				continue
			}
		}
		if len(fs) == 0 && len(ms) == 0 && proto == provider.Responses && itemIDRefusal(b) {
			// and an input item's id with a character OpenAI's don't
			// have, which a relay in front of OpenAI turns away as it
			// does (echo_ts): asked once more with ids it takes, and so
			// from then on
			if nb := plainItemIDs(body); !bytes.Equal(nb, body) {
				refused = append(refused, itemIDsRefused(model))
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
		body = withoutMessageReasoning(body, ms)
		if res, err = s.forward(r.Context(), p, proto, path, p.Prepare(body), r.Header); err != nil {
			return writeError(w, proto, 502, p.Name+": "+err.Error()), err.Error(), true
		}
	}
	if res.StatusCode < 400 {
		for _, f := range refused {
			s.markUnfit(p.ID, f, proto)
		}
	}
	offRetried := false
	if e := bodyEffort(proto, body); res.StatusCode == http.StatusBadRequest && (e == "none" || e == "minimal" || proto == provider.Chat && hasReasoningDisabled(body)) {
		// A model can refuse reasoning turned off. If it names its levels,
		// or Gemini the level it hasn't, try its lowest, and from then on;
		// if OpenRouter requires reasoning, leave the level to it.
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		res.Body = io.NopCloser(bytes.NewReader(b))
		if gemini := geminiRefusedLevel(b) != ""; (e == "none" || e == "minimal") && (effortLevelsNamed.Match(b) || gemini) {
			refused := offRefused(model)
			if gemini || otherOffNamed[e].Match(b) {
				// the level turned away, not reasoning off: AI Studio
				// takes none from gemini-3.8-flash, not minimal, and
				// OpenAI lists none where gpt-5.1 turns minimal away
				refused = levelRefused(model, e)
			}
			s.markUnfit(p.ID, refused, proto)
			offRetried = true
			if res, err = s.forward(r.Context(), p, proto, path, p.Prepare(withBodyEffort(proto, body, onEffort(p, model))), r.Header); err != nil {
				return writeError(w, proto, 502, p.Name+": "+err.Error()), err.Error(), true
			}
		} else if proto == provider.Chat && mandatoryReasoning.Match(b) {
			if nb, ok := withoutReasoningOff(body); ok {
				offRetried = true
				if res, err = s.forward(r.Context(), p, proto, path, p.Prepare(nb), r.Header); err != nil {
					return writeError(w, proto, 502, p.Name+": "+err.Error()), err.Error(), true
				}
			}
		}
	}
	if e := bodyEffort(proto, body); res.StatusCode == http.StatusBadRequest && proto != provider.Anthropic && !offRetried && (e == "" || offEffort(e)) {
		// no reasoning effort, or none, turned away by a model that needs
		// one (a Codex title on a model other than its own asks none,
		// cybercaius on Discord): asked again once at its lowest, low
		// where its levels aren't known. Only that refusal: any other 400
		// goes back as it came
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		res.Body = io.NopCloser(bytes.NewReader(b))
		if needsReasoning(e, b) {
			if offEffort(e) {
				s.markUnfit(p.ID, offRefused(model), proto)
			}
			if res, err = s.forward(r.Context(), p, proto, path, p.Prepare(withModelEffort(proto, body, model, onEffort(p, model))), r.Header); err != nil {
				return writeError(w, proto, 502, p.Name+": "+err.Error()), err.Error(), true
			}
		}
	}
	if proto == provider.Chat && res.StatusCode == http.StatusBadRequest && bodyEffort(proto, body) != "none" {
		// tools with reasoning refused on chat, and no Responses API to
		// take them to: asked again without reasoning (#176)
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		res.Body = io.NopCloser(bytes.NewReader(b))
		if toolsWithoutEffort.Match(b) && !s.servesElsewhere(p, model, proto) && !s.turnedAway(p, model, "none", proto) {
			off, err := s.forward(r.Context(), p, proto, path, p.Prepare(withBodyEffort(proto, body, "none")), r.Header)
			if err != nil {
				return writeError(w, proto, 502, p.Name+": "+err.Error()), err.Error(), true
			}
			ob, _ := io.ReadAll(io.LimitReader(off.Body, 1<<20))
			off.Body.Close()
			off.Body = io.NopCloser(bytes.NewReader(ob))
			if off.StatusCode == http.StatusBadRequest && effortLevelsNamed.Match(ob) {
				// none refused too, naming the levels the model takes
				// (#2670): remembered, and not asked again, and the tools
				// refusal is the answer, which wrongEndpoint reads: no
				// other API left, a group's next member is asked, and the
				// agent is told the Responses URL to set
				s.markUnfit(p.ID, offRefused(model), proto)
				// the refusal is the reply, its headers the ones passed on
				heardReply(r.Context(), p, res)
			} else {
				res = off
			}
		}
	}
	if proto == provider.Anthropic && res.StatusCode == http.StatusBadRequest {
		// a model that always thinks refuses thinking turned off: asked
		// again with it left to the model, and so from then on
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		res.Body = io.NopCloser(bytes.NewReader(b))
		// Sonnet 5.5 by a name thinkingOffAsTaken doesn't know asks for
		// between_tools in place of disabled (#1454)
		if nb, ok := withBetweenTools(body); ok && asksBetweenTools.Match(b) {
			if res, err = s.forward(r.Context(), p, proto, path, p.Prepare(nb), r.Header); err != nil {
				return writeError(w, proto, 502, p.Name+": "+err.Error()), err.Error(), true
			}
		} else if nb, ok := withoutThinkingOff(body); ok && refusesThinkingOff(b) {
			s.markUnfit(p.ID, offRefused(model), proto)
			if res, err = s.forward(r.Context(), p, proto, path, p.Prepare(nb), r.Header); err != nil {
				return writeError(w, proto, 502, p.Name+": "+err.Error()), err.Error(), true
			}
		} else if nb, ok := withThinkingEnabled(body); ok && thinkingNeededRefusal.Match(b) {
			// thinking left out or off, turned away by an upstream that
			// takes the model only thinking (Claude Desktop's check, a
			// title): asked again thinking at the least budget, and so
			// from then on
			s.markThinkingNeeded(p, proto, model, upstream)
			if res, err = s.forward(r.Context(), p, proto, path, p.Prepare(nb), r.Header); err != nil {
				return writeError(w, proto, 502, p.Name+": "+err.Error()), err.Error(), true
			}
		} else if nb, ok := withoutSampling(body); ok && samplingDeprecated.Match(b) {
			// a Claude by a name samplingAsTaken doesn't know refusing
			// temperature, top_p or top_k (#1454)
			if res, err = s.forward(r.Context(), p, proto, path, p.Prepare(nb), r.Header); err != nil {
				return writeError(w, proto, 502, p.Name+": "+err.Error()), err.Error(), true
			}
		}
	}
	defer res.Body.Close()
	u.RequestID = requestID(res.Header)
	if res.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		msg := explained(p, model, res, b)
		if s.noChannelElsewhere(p, model, proto, res.StatusCode, b) {
			s.markUnfit(p.ID, model, proto)
			u.RequestID = ""
			return res.StatusCode, msg, false
		}
		if wrongEndpoint(res.StatusCode, b) {
			s.markUnfit(p.ID, model, proto)
			if len(s.usable(p, model)) > 0 {
				u.RequestID = "" // another endpoint answers, with its own id
				return res.StatusCode, msg, false
			}
			msg += wrongAPINote(p, model, res, b)
		}
		if p.VendorPreset() == "openrouter" && openRouterSharedPool(b) {
			markOpenRouterSharedPool(w)
		}
		keepRetry(w.Header(), res.Header, b)
		u.ErrType = provider.ErrorType(b)
		if codexOutRefusal(p, proto, res.StatusCode, b) {
			return writeCodexOut(w, res.Header, res.StatusCode, msg, b), msg, true
		}
		return writeVendorErrorFrom(w, proto, proto, p, res, msg, b), msg, true
	}
	if remote != nil && res.Header.Get(KeptTurnHeader) != "" {
		// the other magpie kept the turn: what it answered is kept here
		// once relayed, for the next turn's delta (remote_delta.go)
		var kept func()
		res.Body, kept = remote.tee(res.Body, res.Header.Get("Content-Type"))
		defer kept()
	}
	rd, sse := eventStream(res)
	client := w
	if gather != nil {
		w = gather
	}
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
	// a Chat reply asked whole is written once it has ended: whether it
	// was a void cut at its length (#2972) is told at its end, and the
	// error said in its place wants the status too
	wholeHold := proto == provider.Chat && !sse
	if !wholeHold {
		w.WriteHeader(res.StatusCode)
	}
	f, _ := w.(http.Flusher)
	sniff := newSniffer(proto, res.Header.Get("Content-Type"))
	defer func() { u.add(sniff.usage()) }()
	var tidy *chatTidy
	if proto == provider.Chat && sse {
		tidy = &chatTidy{}
	}
	// calls a model wrote into its text are made calls (textcalls.go)
	var written interface {
		write([]byte) []byte
		flush() []byte
	}
	if names := chatToolNames(body); proto == provider.Chat && sse && names != nil {
		written = &textCallTidy{names: names}
	} else if proto == provider.Anthropic && names != nil {
		// a Messages reply, streamed or whole (textcalls_anthropic.go)
		written = &anthropicTextCalls{names: names, sse: sse}
	} else if proto == provider.Responses && names != nil {
		// a Responses reply, streamed or whole (textcalls_relay.go)
		written = &responsesTextCalls{names: names, sse: sse, seq: -1}
	} else if names := geminiToolNames(body); proto == provider.Gemini && names != nil {
		// a Gemini reply, streamed or whole (textcalls_relay.go)
		written = &geminiTextCalls{names: names, sse: sse}
	}
	// a reply that came whole: JSON, or one under text/plain or no type
	// that eventStream found isn't a stream
	var whole *chatWhole
	if ct := res.Header.Get("Content-Type"); proto == provider.Chat && !sse && (strings.Contains(ct, "json") || ct == "" || strings.HasPrefix(ct, "text/plain")) {
		whole = &chatWhole{names: chatToolNames(body)}
	}
	var sigs *sigTidy
	if proto == provider.Chat && geminiCompat(p.VendorHost(), model) {
		sigs = &sigTidy{sse: sse, by: p.ID}
	} else if proto == provider.Gemini {
		// a Gemini client keeps the signatures itself: noted as p's
		sigs = &sigTidy{sse: sse, by: p.ID, native: true}
	}
	var search *searchTidy
	if searchFn && sse {
		search = &searchTidy{}
	}
	var spaces *nsTidy
	if named != nil || renamed != nil {
		spaces = &nsTidy{named: named, back: renamed, sse: sse}
	}
	// a reasoning item another vendor wrote with its text in it, in a reply
	// relayed as it came, made into one magpie could have (#2855): the
	// client stores what it is handed and replays it where OpenAI's rules
	// hold, on magpie or off it
	var made *reasoningTidy
	if proto == provider.Responses {
		made = &reasoningTidy{sse: sse}
	}
	// a model's 2500.0 in a call's arguments, which Codex turns away
	// (grok_integral.go, #1431, #3152: Grok's models and muse-spark alike)
	var ints *intTidy
	if proto == provider.Responses || proto == provider.Chat || proto == provider.Anthropic {
		ints = &intTidy{proto: proto, sse: sse}
	}
	// a glm-5.3-flash* replica's CR stitched into a call's argument
	// fragments goes (tool_args_cr.go, #4256); any other model's reply
	// goes as it came
	var crArgs *crTidy
	if (proto == provider.Responses || proto == provider.Chat || proto == provider.Anthropic) && (glmFlash(model) || glmFlash(upstream)) {
		crArgs = &crTidy{proto: proto, sse: sse}
	}
	// a message the upstream gave no phase gets one, as OpenAI's own
	// models' do (phase.go, #2437); it goes last, as it holds events back
	var phases *phaseTidy
	if proto == provider.Responses {
		phases = &phaseTidy{sse: sse}
	}
	// each item keeps the id it was added with (itemids.go): Copilot gives
	// it another in every event
	var ids *idTidy
	if proto == provider.Responses && sse {
		ids = &idTidy{}
	}
	// a thinking block with no signature, which a Messages client may read
	// as required (Grok Build), gets an empty one, as Anthropic's own
	// blocks start (thinking_signature.go)
	var thinkSigs *sigFieldTidy
	if proto == provider.Anthropic {
		thinkSigs = &sigFieldTidy{sse: sse}
	}
	// and a tool_use id the client has seen again is salted, so Claude
	// Code's Set of the session keeps both (#4910, tool_use_ids.go)
	var toolIDs *toolUseIDs
	if proto == provider.Anthropic {
		toolIDs = &toolUseIDs{seen: toolSeen, sse: sse}
	}
	// anything the backend says outside the Responses event enum (the
	// account's codex.* events, response.metadata, keepalive), which only
	// Codex has a name for (responses_enum.go); any other client gets the
	// reply without it
	var enumTidy *responsesEnumTidy
	if proto == provider.Responses && sse && !fromCodex(r.Header) && agentOf(r) != "codex" {
		enumTidy = &responsesEnumTidy{}
	}
	// out is what a filter let through, through ids, phases, toolIDs and
	// thinkSigs
	out := func(b []byte) []byte {
		if ids != nil {
			b = ids.write(b)
		}
		if phases != nil {
			b = phases.write(b)
		}
		if toolIDs != nil {
			b = toolIDs.write(b)
		}
		if thinkSigs != nil {
			b = thinkSigs.write(b)
		}
		return b
	}
	buf := make([]byte, 32<<10)
	var rerr error
	// atLine says whether what the client has ends a line, so what is
	// appended below begins one of its own: appended after a line still
	// pending, an event's name lands inside that line's data, and the
	// client reads one event it can't parse where the protocol's error was
	// meant (TestPassthroughStreamCutMidLine)
	atLine := true
	// pending is the line the upstream has begun but not ended: a stream
	// read stops there on a failure, and what of it went out already is
	// data the client's SSE parser dies on — a half event's JSON doesn't
	// parse — so the error event appended after it is never reached
	// (#2580: a stream Z.ai reset mid-line reached pi as "malformed
	// server-sent event JSON"). Only lines that ended go out as they
	// come; the half line waits for its rest, unheld puts it out when
	// the reply ends whole, and dropHalf throws it away when the failure
	// is appended, for no client can read it and the error is what there
	// is left to say
	var pending []byte
	// a Chat stream's tail, from the read whose chunk says why the reply
	// ended while nothing has been said in it: whether that end is a void
	// to fail at once (#2972) is told when the stream is over, and the
	// end itself goes out only then. And a Chat reply asked whole, held
	// for the same verdict.
	var heldTail []byte
	holdTail := false
	var heldBody []byte
	write := func(b []byte) (int, error) {
		if wholeHold {
			if len(heldBody)+len(b) <= 8<<20 {
				heldBody = append(heldBody, b...)
				return len(b), nil
			}
			// more than we hold for a verdict: the reply goes as it came,
			// and no void is failed in it
			wholeHold = false
			w.WriteHeader(res.StatusCode)
			w.Write(heldBody)
			heldBody = nil
		}
		if !sse {
			if len(b) > 0 {
				atLine = b[len(b)-1] == '\n'
			}
			return w.Write(b)
		}
		if holdTail {
			heldTail = append(heldTail, b...)
			return len(b), nil
		}
		pending = append(pending, b...)
		i := bytes.LastIndexByte(pending, '\n')
		var err error
		if i >= 0 {
			_, err = w.Write(pending[:i+1])
			pending = pending[i+1:]
			atLine = true
		}
		return len(b), err
	}
	// unheld puts out the half line a whole reply ended inside: it goes
	// as it came, as a stream the upstream itself ended short
	unheld := func() {
		if len(pending) > 0 {
			w.Write(pending)
			pending = nil
		}
	}
	// dropHalf throws the line still pending away, unless it is a whole
	// event's data the upstream stopped short of ending (its JSON parses,
	// or [DONE]): that one is no half event, and goes out as before, with
	// apart putting the blank line after it
	dropHalf := func() {
		if l := bytes.TrimSpace(pending); bytes.HasPrefix(l, []byte("data:")) {
			if d := bytes.TrimSpace(l[5:]); string(d) == "[DONE]" || json.Valid(d) {
				unheld()
				atLine = false
				return
			}
		}
		pending = pending[:0]
		atLine = true
	}
	// apart is what goes before an event magpie appends: a blank line,
	// after the line still pending if the upstream's last write stopped
	// inside one
	apart := func() []byte {
		if atLine {
			return []byte("\n")
		}
		return []byte("\n\n")
	}
	for {
		n, err := rd.Read(buf)
		if n > 0 {
			sniff.write(buf[:n])
			if proto == provider.Chat && sse && gather == nil && sniff.u.Stop != "" && !sniff.tail.said() {
				// the reply's end is in this or a later read, and nothing
				// has been said in it: hold what follows for the void's
				// verdict (#2972). A reply that has said something is
				// never held: its cut is the truncation it always was.
				holdTail = true
			}
			out := buf[:n]
			if tidy != nil {
				out = tidy.write(out)
			}
			// spaces before written: a whole reply (no sse) is held by the
			// first filter that takes it, and the call's namespace must be
			// worded for Codex before the text calls are read out of the
			// message items (#3183)
			if spaces != nil {
				out = spaces.write(out)
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
			// crArgs before ints: ints runs on every reply now and holds
			// one that came whole to its own flush, which crArgs, after it,
			// would never see (#3152 beside #4256's glm-5.3-flash routes)
			if crArgs != nil {
				out = crArgs.write(out)
			}
			if made != nil {
				out = made.write(out)
			}
			if ints != nil {
				out = ints.write(out)
			}
			if sigs != nil {
				out = sigs.write(out)
			}
			if ids != nil {
				out = ids.write(out)
			}
			if phases != nil {
				out = phases.write(out)
			}
			if toolIDs != nil {
				out = toolIDs.write(out)
			}
			if thinkSigs != nil {
				out = thinkSigs.write(out)
			}
			if enumTidy != nil {
				out = enumTidy.write(out)
			}
			if _, werr := write(out); werr != nil {
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
	sign := func(b []byte) []byte {
		if crArgs != nil {
			b = crArgs.write(b)
		}
		if made != nil {
			b = made.write(b)
		}
		if ints != nil {
			b = ints.write(b)
		}
		if sigs != nil {
			b = sigs.write(b)
		}
		return out(b)
	}
	if tidy != nil {
		out := tidy.flush()
		if written != nil {
			out = append(written.write(out), written.flush()...)
		}
		write(sign(out))
	} else if written != nil {
		write(sign(written.flush()))
	}
	if whole != nil {
		out := whole.flush()
		if spaces != nil {
			out = spaces.write(out)
		}
		write(sign(out))
	}
	if search != nil {
		out := search.flush()
		if spaces != nil {
			out = spaces.write(out)
		}
		write(sign(out))
	}
	if spaces != nil {
		write(sign(spaces.flush()))
	}
	if crArgs != nil {
		b := crArgs.flush()
		if made != nil {
			// made holds a whole reply until its own drain below, as ints
			// does: a reply crArgs held to its end goes through it too
			b = made.write(b)
		}
		if ints != nil {
			// ints holds a whole reply until its own drain below, so the
			// tail ints has not seen goes through it here first
			b = ints.write(b)
		}
		if sigs != nil {
			b = sigs.write(b)
		}
		write(out(b))
	}
	if made != nil {
		// past crArgs, drained above: what made held goes on through the
		// filters after it only
		b := made.flush()
		if ints != nil {
			b = ints.write(b)
		}
		if sigs != nil {
			b = sigs.write(b)
		}
		write(out(b))
	}
	if ints != nil {
		b := ints.flush()
		if sigs != nil {
			b = sigs.write(b)
		}
		write(out(b))
	}
	if sigs != nil {
		write(out(sigs.flush()))
	}
	if ids != nil {
		b := ids.flush()
		if phases != nil {
			b = phases.write(b)
		}
		write(b)
	}
	if phases != nil {
		write(phases.flush())
	}
	if toolIDs != nil {
		// the salted tail goes on through thinkSigs, which holds a whole
		// reply until its own drain below
		b := toolIDs.flush()
		if thinkSigs != nil {
			b = thinkSigs.write(b)
		}
		write(b)
	}
	if thinkSigs != nil {
		write(thinkSigs.flush())
	}
	if enumTidy != nil {
		write(enumTidy.flush())
	}
	if sse {
		sniff.drain()
	} else {
		sniff.settleWhole()
	}
	if sniff.failedNamed != 0 {
		// the stream's error frame named its own status: the failure is
		// booked and rested as it (saidStatusHeader, #4855), while the
		// frame itself goes to the client as it came
		noteSaidStatus(w, sniff.failedNamed)
	}
	if gather != nil {
		// the gathered stream is read here, not by the client: it gets
		// every byte, a last line the upstream never ended included, as
		// readSSE reads an event that ends with the body
		unheld()
		cut := ""
		if rerr != nil && rerr != io.EOF {
			cut = cutMidReply(p.Name, rerr)
		}
		status, msg := gather.finish(client, p, sse, cut, sniff.failed)
		return status, msg, true
	}
	// a Chat stream of Gemini's OpenAI-compatible API is whole only with
	// its finish_reason, [DONE] or not (saysItsEnd)
	unfinished := proto == provider.Chat && sniff.failed == "" && sniff.u.Stop == "" && saysItsEnd(p, proto, upstream)
	if fin := sniff.tail.finish(); sse && unfinished && fin != "" && (rerr == nil || rerr == io.EOF) && r.Context().Err() == nil {
		// only the finish_reason was lost, after the generation was done
		// (chatTail): the client gets it, and the reply is whole. The
		// event the upstream began and never ended has no rest coming:
		// dropped when it is nothing a client can parse (dropHalf)
		dropHalf()
		w.Write(append(apart(), "data: "+string(finishChunk(fin))+"\n\n"...))
		if !sniff.ended {
			w.Write([]byte("data: [DONE]\n\n"))
		}
		if f != nil {
			f.Flush()
		}
		sniff.u.Stop, unfinished = fin, false
	}
	if sse && r.Context().Err() == nil && (!sniff.whole() || unfinished) {
		// the upstream died mid-reply, or ended it short of its last
		// event: say so in the stream rather than end it as if whole,
		// which a client reads as a reply cut off for no reason (#370:
		// dsh's "stream ended before message_stop", not retried). A Chat
		// stream may end without [DONE] and be whole, so only a read that
		// failed counts there, Gemini's own aside; a Gemini one ends on a
		// candidate's finishReason (#1644).
		var failed string
		switch {
		case rerr != nil && rerr != io.EOF:
			failed = cutMidReply(p.Name, rerr)
		case proto == provider.Anthropic || proto == provider.Responses || proto == provider.Gemini || unfinished:
			failed = p.Name + ": " + errEndedShort.Error()
		}
		if failed != "" {
			// what is appended begins after a blank line, as an event of
			// its own (TestPassthroughStreamEndsOnLineEnd); the half
			// event the upstream left is dropped (#2580): a client
			// reading it dies on its JSON before the error is reached
			dropHalf()
			tail := sniff.resp
			if rc, ok := written.(*responsesTextCalls); ok && int64(rc.seq) >= tail.next {
				// the stream's events were numbered again on their way
				// out: the failure follows the last the client got
				tail.next = int64(rc.seq) + 1
			}
			tail.model = cmp.Or(sniff.served, model)
			w.Write(append(apart(), streamFailureAt(proto, failed, tail)...))
			if proto == provider.Chat && rerr != nil && rerr != io.EOF {
				// a Chat stream the upstream severed ends on [DONE] too: a
				// client that reads to it stops there and says the error it
				// read, instead of waiting on a connection that only
				// closes (#3197). One that ended cleanly ends at the error,
				// as a cut reply is not to end as if whole
				// (TestGeminiCompatRelayEndedShort).
				w.Write([]byte("data: [DONE]\n\n"))
			}
			if f != nil {
				f.Flush()
			}
			return res.StatusCode, failed, true
		}
	}
	if holdTail {
		if cutVoid(sniff.u.Stop, !sniff.tail.said(), sniff.u.Output, capped) &&
			replyCheck(r.Context()) == nil {
			// a void cut at its length, and nothing the agent acts on in
			// it: the held end is dropped, the error said in its place,
			// [DONE] after — no reply that ended as it should with nothing
			// in it, and no ask again for the budget to burn twice (#2972)
			msg := cutVoidFailed(p.Name, capped)
			dropHalf()
			w.Write(append(apart(), streamFailure(provider.Chat, msg)...))
			w.Write([]byte("data: [DONE]\n\n"))
			if f != nil {
				f.Flush()
			}
			return res.StatusCode, msg, true
		}
		// not a void, or a short probe's answer: the end goes as it came
		holdTail = false
		if _, err := write(heldTail); err != nil {
			return res.StatusCode, "", true
		}
		if f != nil {
			f.Flush()
		}
		heldTail = nil
	}
	if wholeHold {
		// the reply was held whole: its own words tell the verdict
		sniff.tail.see(heldBody)
		var v struct {
			Usage *cUsage `json:"usage"`
		}
		spent := 0
		if json.Unmarshal(heldBody, &v) == nil && v.Usage != nil {
			spent = v.Usage.CompletionTokens
		}
		if cutVoid(upstreamStop(heldBody), !sniff.tail.said(), spent, capped) &&
			replyCheck(r.Context()) == nil {
			msg := cutVoidFailed(p.Name, capped)
			return writeError(w, proto, 502, msg), msg, true
		}
		w.WriteHeader(res.StatusCode)
		w.Write(heldBody)
		heldBody = nil
	}
	if sse {
		unheld()
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

// noteSaidStatus carries the status a relayed stream's error frame named
// for itself onto the try's writer (#4855), for the routing loop and
// restAfter to read (namedStatusHeader); the client never sees it.
func noteSaidStatus(w http.ResponseWriter, status int) {
	if h, ok := w.(*holdWriter); ok {
		h.note(namedStatusHeader, strconv.Itoa(status))
	}
}

// streamFailure is an error event ending a stream in proto, as each
// protocol's own server sends one mid-reply.
func streamFailure(proto provider.Protocol, msg string) []byte {
	return streamFailureAt(proto, msg, respTail{})
}

// streamFailureAt is streamFailure after what a Responses stream said of
// its response (tail). A response.failed is a whole Response, as OpenAI's
// own is: Grok Build reads it as async-openai's, which needs the
// response's id, created_at, model and output, and the event's
// sequence_number (one without created_at was "serialization error:
// missing field `created_at`", plugins#852). The response is the one the
// stream began, numbered after its last event.
func streamFailureAt(proto provider.Protocol, msg string, tail respTail) []byte {
	var name string
	var v map[string]any
	switch proto {
	case provider.Chat:
		v = map[string]any{"error": map[string]any{"message": msg, "type": "api_error"}}
	case provider.Responses:
		name = "response.failed"
		id := cmp.Or(tail.id, "resp_"+newID())
		created := tail.created
		if created <= 0 {
			created = time.Now().Unix()
		}
		v = map[string]any{"type": name, "sequence_number": tail.next, "response": map[string]any{
			"id": id, "object": "response", "created_at": created, "status": "failed",
			"model": tail.model, "output": []any{}, "parallel_tool_calls": true, "tool_choice": "auto", "tools": []any{},
			"error": map[string]any{"code": "server_error", "message": msg}}}
	case provider.Gemini:
		// Gemini's own mid-stream error, as geminiEncoder writes it: a
		// data line alone, Google's SDK reads no event names
		v = map[string]any{"error": map[string]any{"code": 502, "message": msg, "status": "UNAVAILABLE"}}
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
// says so for most vendors; where it doesn't, the body's first bytes
// decide, and the header is set right for whoever reads it. The ChatGPT
// backend sends no type at all, and a vendor's stream may come under
// another one (application/octet-stream, application/json): read as a
// whole reply, it reached the agent only at its end, its thinking all at
// once (uk.jent on Discord: MiniMax Code to Grok Build, "Thought for
// 0.0s"). A JSON or binary body never begins with a line of an event
// stream.
func eventStream(res *http.Response) (io.Reader, bool) {
	// an error the upstream wrote as a JSON line of its own goes on as
	// the data event it means (bareErrors): to a decoder, a sniffer and
	// a client the stream is relayed to alike
	if strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		return bareErrors(res.Body), true
	}
	br := bufio.NewReaderSize(res.Body, 4<<10)
	head, _ := br.Peek(16)
	if eventsStart(bytes.TrimLeft(head, " \t\r\n")) {
		res.Header.Set("Content-Type", "text/event-stream")
		return bareErrors(br), true
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
		if s.fits(p.ID, model, proto) && (apis == nil && !p.NamedOnlyOn(proto) || slices.Contains(apis, proto)) {
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
	// the headers the user gave the model, on p's requests for it
	ctx = provider.WithModel(ctx, p.ID, model)
	if req.ThinkOff && req.Effort != "none" && slices.Contains(p.Efforts(model), "none") {
		// the client turned reasoning off (Effort reads that as low), and
		// the model can stop thinking: asked for none (#899)
		r := *req
		r.Effort, req = "none", &r
	}
	if provider.Reasonless(p.ID, model) {
		// the user said it takes no reasoning ask: built with none, not
		// even reasoning turned off (yuexian7 on Discord)
		r := *req
		r.Effort, r.ThinkOff, r.Thinking, req = "", false, false, &r
	}
	if req.Effort != "" {
		if e := fitFor(p, model, req.Effort); e != req.Effort {
			r := *req
			r.Effort, req = e, &r
		}
	}
	// Gemini's signatures another provider gave go to none (ownSignatures)
	req = ownSignatures(req, p.ID)
	if p.IsVertex() && !provider.VertexPartner(model) && !provider.VertexClaude(model) {
		// Gemini's levels; a partner model on chat completions, and
		// Claude on Anthropic's Messages API, are sent their effort as
		// at any vendor of that API
		req = vertexEffort(req, p.Efforts(model))
	}
	// the names in force, read once however many endpoints the request is
	// built for below
	wires := wiresOf(ctx)
	web := req.WebSearch
	// the cache key was left out to see if it was what the upstream refused
	dropped := false
	// the conversation's cache key given a provider that may need one when
	// the client named none (ownCacheKey); "" once it was left out for a
	// refusal
	ownKey := ""
	if req.CacheKey == "" && ownCacheKey(p) {
		ownKey = cacheKeyOf(in, req)
	}
	// Codex's image tool was left out to see if it was (#949), and the
	// request with it, to go back to when it wasn't; tried once
	var withImage *Request
	imageTried := false
	// Gemini's thinking levels turned away this time (geminiRefusedLevel)
	var levelsRefused []string
	// the request before #176 asked it without reasoning, and the tools
	// refusal that made it ask (toolsWithoutEffort): none turned away too,
	// that refusal is the answer (#2670)
	var beforeOff *Request
	var toolsRefusal []byte
	offAsked := false
	// the reply limit was filled in, the client having named none
	// (fillsOutput)
	filledOutput := false
	for {
		if req.hourMarks && !s.fits(p.ID, hourMarksRefused, to) {
			r := *req
			r.hourMarks, req = false, &r
		}
		if mayDropImageTool(p) && !s.fits(p.ID, imageToolRefused, to) {
			req, _ = withoutImageToolReq(req)
		}
		// Anthropic's Messages are folded as they're built (objectSchema)
		if to != provider.Anthropic && s.foldsRoots(p, model, to) {
			req, _ = objectRootsReq(req)
		}
		// only a provider that searches by itself is asked to
		if want := web && searchesFor(p, to, model, req); want != req.WebSearch {
			r := *req
			r.WebSearch, req = want, &r
		}
		if ownKey != "" {
			// only Chat and Responses carry it
			want := ""
			if (to == provider.Chat || to == provider.Responses) && s.fits(p.ID, cacheKeyField, to) {
				want = ownKey
			}
			if req.CacheKey != want {
				r := *req
				r.CacheKey, req = want, &r
			}
		}
		if req.CacheKey != "" && !s.fits(p.ID, cacheKeyField, to) {
			r := *req
			r.CacheKey, req = "", &r
		}
		if want := to == provider.Responses && loadsInPlace(p) && s.fits(p.ID, additionalToolsItem, to); want != req.LoadedInPlace {
			r := *req
			r.LoadedInPlace, req = want, &r
		}
		if req.OwnEffort && req.Effort != "" && !s.fits(p.ID, ownEffortField(model), to) {
			r := *req
			r.Effort, req = "", &r
		}
		if offEffort(req.Effort) && !s.fits(p.ID, offRefused(model), to) {
			r := *req
			r.Effort, req = onEffort(p, model), &r
		}
		if to == provider.Anthropic && req.ThinkOff && !s.fits(p.ID, offRefused(model), to) {
			req = thinkingBackOn(p, model, req)
		}
		if to != provider.Anthropic && (req.ThinkOff || req.Effort == "" || offEffort(req.Effort)) && !s.fits(p.ID, thinkingNeeded(model), to) {
			// a model whose upstream said thinking must be enabled: asked
			// to reason at its least on Chat or Responses (Anthropic's
			// body is given thinking once built, below)
			req = thinkingBackOn(p, model, req)
		}
		if forcesTool(req.ToolChoice) && !s.fits(p.ID, forcedRefused(model), to) {
			req = unforced(req)
		}
		if req.Format != nil && !s.fits(p.ID, formatRefused(model), to) {
			req = req.inSystem()
		} else if req.Format != nil && to != provider.Anthropic && !s.fits(p.ID, schemaRefused(model), to) {
			req = req.asObject()
		}
		if req.LastIsTurn && !req.Resume && req.endsWithAssistant() && !s.fits(p.ID, prefillRefused(model), to) {
			// a model that takes no prefill is asked to answer after the
			// client's last, assistant, message (prefill.go, #1447)
			req = req.userLast()
		}
		if to == provider.Anthropic && p.IsBedrock() && req.Metadata != nil {
			// not the plain id Bedrock checks metadata.user_id against (#176)
			r := *req
			r.Metadata, req = nil, &r
		}
		if want := to == provider.Chat && geminiCompat(p.VendorHost(), model) && s.fits(p.ID, thinkingConfigField, to); want != req.GeminiCompat {
			r := *req
			r.GeminiCompat, req = want, &r
		}
		if req.GeminiCompat {
			// Gemini 3 turns away a thinking level the model hasn't
			// (gemini-3.8-flash has no minimal): reasoning off goes at the
			// least it has, and an effort at the nearest, of the levels
			// not turned away before
			levels := s.geminiLevels(p, model, to)
			if req.ThinkOff {
				if l := fitEffort("minimal", levels); l != req.OffLevel {
					r := *req
					r.OffLevel, req = l, &r
				}
			} else if e := fitEffort(req.Effort, levels); slices.Contains(gemini3Levels, req.Effort) && e != req.Effort {
				r := *req
				r.Effort, req = e, &r
			}
		}
		// the tier the client asked for goes to a provider the user added
		// by its address as it was asked (Request.Tier), and to another
		// magpie, which offered it and serves it as its own Codex would be
		// (#1234), and to a provider plugin, on its Chat or Responses, when
		// it is one the plugin says the model is served at (#2514)
		own := p.Preset == "" && p.Account == nil || p.IsRemoteMagpie()
		if p.IsPlugin() {
			// fast mode in Claude's words (speed "fast", a fast pick from
			// an agent on its API) is the priority tier, or ultrafast for
			// a pick sent Ultrafast, on a plugin that says the model is
			// served at it (Lovest on Discord)
			tier := req.Tier
			if tier == "" && req.Fast {
				tier = "priority"
				if req.Ultrafast || isUltra(ctx) {
					tier = "ultrafast"
				}
			}
			own = tier != "" && (to == provider.Chat || to == provider.Responses) && slices.Contains(provider.PluginTiers(p, model), tier)
			if own && tier != req.Tier {
				r := *req
				r.Tier, req = tier, &r
			}
		}
		if p.IsRemoteMagpie() && req.Tier == "" && req.Fast && (to == provider.Chat || to == provider.Responses) {
			// fast mode in Claude's words (a fast pick from an agent on
			// its API, littlekk88 on Discord) is the tier another magpie
			// offers it at: priority, or ultrafast for a pick sent
			// Ultrafast where it offers that. Its Chat or Responses reads
			// service_tier alone
			tiers := provider.RemoteTiers(p, model)
			tier := "priority"
			if (req.Ultrafast || isUltra(ctx)) && slices.Contains(tiers, "ultrafast") {
				tier = "ultrafast"
			}
			if slices.Contains(tiers, tier) {
				r := *req
				r.Tier, req = tier, &r
			}
		}
		if own := own && s.fits(p.ID, tierField, to); own != req.OwnTier {
			r := *req
			r.OwnTier, req = own, &r
		}
		// a member or pick the user sent fast on a provider they added is
		// asked for priority on its OpenAI API whatever API the agent
		// spoke (#1615); one that turned the field away isn't (own is
		// false then)
		if req.OwnTier && req.Tier == "" && isOwnFast(ctx) && (to == provider.Chat || to == provider.Responses) {
			r := *req
			r.Tier, req = "priority", &r
		}
		// a pick sent Ultrafast from an agent on Claude's API, whose words
		// for it are speed "fast" (#1954): on the ChatGPT backend alone
		// (buildResponses)
		if req.Fast && !req.Ultrafast && isUltra(ctx) && to == provider.Responses {
			r := *req
			r.Ultrafast, req = true, &r
		}
		// Anthropic's API needs a reply limit: one the client didn't name
		// is the model's own, not a flat 16384 (#1792, askedOutput). So on
		// a vendor's Chat or Responses, which may read none as less than
		// the model gives (fillsOutput), unless it turned the one filled
		// in away before
		if req.MaxTokens <= 0 && fillsOutput(p, to) && (to == provider.Anthropic || s.fits(p.ID, outputFillRefused(model), to)) {
			n := askedOutput(p, model)
			if to != provider.Anthropic {
				// the window holds the prompt and the reply together
				n = roomedOutput(n, contextOf(p, model), estimate(req))
			}
			if n > 0 {
				r := *req
				r.MaxTokens, req = n, &r
				filledOutput = to != provider.Anthropic
			}
		}
		wire := provider.UpstreamNameIn(wires, p.ID, model)
		sent := req
		if dropsBillingLine(p, to, model, wire) && billingHeader.MatchString(req.System) {
			// Claude Code's billing line, which changes every turn, as the
			// first words of another API's prompt or a vendor's model's:
			// its prompt cache missed from the first byte (#1600,
			// billing_line.go)
			r := *req
			r.System, sent = withoutBillingHeader(r.System), &r
		}
		body, err := buildFor(p, to, sent, model)
		if err != nil {
			return nil, to, err
		}
		noteWireEffort(ctx, to, sent)
		if to == provider.Anthropic && !s.fits(p.ID, thinkingNeeded(model), to) {
			// a model whose upstream said thinking must be enabled
			// (thinkingNeededRefusal): a request built thinking off, or
			// with none (a short one), goes thinking at the least budget
			if nb, ok := withThinkingEnabled(body); ok {
				body = nb
			}
		}
		if to == provider.Chat && (p.IsBedrock() || p.IsAzure()) {
			body = asCompletionTokens(body)
		}
		if to == provider.Chat && (p.VendorPreset() == "mistral" || p.VendorHost() == "api.mistral.ai") {
			// an earlier turn's reasoning, which the build replays in
			// reasoning_content for a model whose name says DeepSeek
			// wherever it is served (chatReplaysReasoning), and Mistral
			// turns that field away (#494): the thinking part it takes,
			// as the relayed route converts it. The Chat route alone had
			// it; every follow-up of a Codex tool loop was refused, 422
			// extra_forbidden (#4906)
			body = mistralThinking(body)
			body = mistralImageLimit(body)
		}
		if to == provider.Responses && !keepsLoneNone(p) {
			// the "none" buildResponses keeps for a request that turned
			// tools off with none declared goes to the Grok plugin alone,
			// which reads it before adding x_search and drops it before
			// Grok (0.1.18, #3110). Any other upstream was never sent it:
			// xAI turns it away (#378), and no other plugin's vendor has
			// been tried with it
			body = withoutLoneToolChoice(body)
		}
		if to == provider.Chat && req.WebSearch && p.IsRemoteMagpie() {
			// the other magpie searches for it (search_remote.go)
			body = withFields(body, map[string]any{"web_search_options": map[string]any{}})
		}
		if !s.fits(p.ID, historyToolsRefused, to) {
			// a stub for each tool the history called and tools lacks,
			// to an upstream that turned such a call away before
			// (undeclared_tools.go)
			body, _, _ = withHistoryTools(to, body)
		}
		// xAI's own API takes neither a sealed reasoning item's null
		// content nor a system turn in Messages (xai_body.go, #3392) —
		// the built body as the relayed one
		body = xaiBody(p, to, body)
		if to == provider.CodeAssist && p.Account != nil {
			// the envelope is named in there, where the id it carries is
			// known: on Antigravity that is the variant the effort picked,
			// not the model magpie knows
			body = codeAssistBody(p, req, model, wires)
		} else if wire != model {
			// the vendor's own name for the model goes in the model field
			// and nowhere else: everything above shaped the request from
			// the model magpie knows, which is what those decisions are
			// about
			body = rewriteModel(body, wire)
		}
		path := upstreamPath(p, to, wire)
		if req.Resume && to == provider.Chat && chatPrefill(p.VendorHost(), model) == "prefix" {
			// DeepSeek serves its prefix mode under /beta, beside the /v1
			// the base carries
			p.Chat = strings.TrimSuffix(p.Chat, "/v1")
			path = "/beta" + path
		}
		fctx := ctx
		if p.IsPlugin() && req.Effort != "" {
			// the effort picked, which a plugin's model with variants is
			// told in X-Magpie-Effort: the body says only what Gemini's
			// thinkingConfig can, and loses minimal and xhigh
			fctx = provider.WithEffort(ctx, req.Effort)
		}
		res, err := s.forward(fctx, p, to, path, p.Prepare(body), in)
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
		if p.PassesErrors() {
			// its errors are the client's (errors=pass): not asked again
			return res, to, nil
		}
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		res.Body = io.NopCloser(bytes.NewReader(b))
		if filledOutput && outputFieldRefused(res.StatusCode, b) {
			// the model's own limit, filled in, turned away (a list or
			// models.dev saying more than the vendor takes): asked again
			// naming none, as the client did, and so from then on
			log.Printf("gateway: %s turned away max tokens %d for %s (%d); asked naming none from now on", p.ID, req.MaxTokens, model, res.StatusCode)
			s.markUnfit(p.ID, outputFillRefused(model), to)
			r := *req
			r.MaxTokens, req, filledOutput = 0, &r, false
			continue
		}
		if req.LoadedInPlace && refusesField(res.StatusCode, b, additionalToolsItem) && bytes.Contains(body, []byte(`"`+additionalToolsItem+`"`)) {
			// an upstream that turns the item away is asked again with the
			// loaded tools among the others, and not sent it again: the
			// prompt's prefix changes from here, as before #2660, and the
			// log says so rather than the cache seeming kept
			log.Printf("gateway: %s turned away additional_tools (%d); loaded tools go in tools from now on", p.ID, res.StatusCode)
			s.markUnfit(p.ID, additionalToolsItem, to)
			r := *req
			r.LoadedInPlace, req = false, &r
			continue
		}
		if req.hourMarks && to == provider.Anthropic && hourMarksRefusal(res.StatusCode, b) {
			// an upstream that turns the hour's cache marks away (a relay
			// that knows no ttl) is asked again with Anthropic's default
			// five minutes, and so from then on
			log.Printf("gateway: %s turned away 1h cache marks for %s (%d); marked for 5m from now on", p.ID, model, res.StatusCode)
			s.markUnfit(p.ID, hourMarksRefused, to)
			r := *req
			r.hourMarks, req = false, &r
			continue
		}
		if to == provider.Anthropic && res.StatusCode == http.StatusBadRequest &&
			samplingDeprecated.Match(b) && (req.Temp != nil || req.TopP != nil) {
			// a Claude by a name samplingAsTaken can't read refuses the
			// sampling a Chat or Responses client sent (#1454): asked once
			// more without it, as a relayed request is
			r := *req
			r.Temp, r.TopP = nil, nil
			req = &r
			continue
		}
		if to == provider.Anthropic && req.ThinkOff && res.StatusCode == http.StatusBadRequest && refusesThinkingOff(b) {
			// a vendor's model that always thinks (GLM-5.3, #699) refuses
			// the thinking off a client asked for (#1636): asked again
			// thinking at its least, as it was before, and so from then on
			s.markUnfit(p.ID, offRefused(model), to)
			req = thinkingBackOn(p, model, req)
			continue
		}
		if res.StatusCode == http.StatusBadRequest && thinkingNeededRefusal.Match(b) && s.fits(p.ID, thinkingNeeded(model), to) {
			if _, off := withThinkingEnabled(body); to == provider.Anthropic && off || to != provider.Anthropic && (req.ThinkOff || req.Effort == "" || offEffort(req.Effort)) {
				// sent no thinking, or thinking off, which the upstream
				// takes the model only with (Claude Desktop's check, a
				// title): asked again thinking at its least, and so from
				// then on
				s.markThinkingNeeded(p, to, model, wire)
				continue
			}
		}
		if req.GeminiCompat && res.StatusCode == http.StatusBadRequest {
			if l := geminiRefusedLevel(b); l != "" {
				// a thinking level the model hasn't, turned away: Gemini's
				// fields were taken (not refusesThinkingConfig), and the
				// model is asked again at the nearest level it has left,
				// and so from then on. One turned away again, with no
				// other left, goes back as it came.
				if slices.Contains(levelsRefused, l) {
					return res, to, nil
				}
				levelsRefused = append(levelsRefused, l)
				s.markUnfit(p.ID, levelRefused(model, l), to)
				continue
			}
		}
		if req.endsWithAssistant() && refusesPrefill(res.StatusCode, b) {
			// the model takes no prefill (#1447), remembered for it alone.
			// A conversation the client ended with the assistant's message
			// is asked again with a user turn after it; magpie's own
			// continuation of a cut reply isn't (streamTranslated)
			s.markUnfit(p.ID, prefillRefused(model), to)
			if req.LastIsTurn && !req.Resume {
				req = req.userLast()
				continue
			}
		}
		if req.GeminiCompat && refusesThinkingConfig(res.StatusCode, b) {
			// Gemini's own fields turned away (a proxy that isn't in front
			// of Google after all, or Google changing them): asked as
			// before, with reasoning_effort, and not sent them again
			s.markUnfit(p.ID, thinkingConfigField, to)
			continue
		}
		if badRequest(res.StatusCode) && s.fits(p.ID, historyToolsRefused, to) {
			if _, names, toolless := withHistoryTools(to, body); len(names) > 0 && refusesHistoryTools(b, names, toolless) {
				// a history call to a tool the request doesn't declare,
				// or calls with no tools, turned away (pi's and T3 Code's
				// tool_search): asked again with a stub declared for each
				// such tool, and so from then on
				s.markUnfit(p.ID, historyToolsRefused, to)
				continue
			}
		}
		if badRequest(res.StatusCode) && rootUnionRefusal.Match(b) && s.fits(p.ID, rootUnionRefused(model), to) {
			if _, changed := objectRootsReq(req); changed {
				// a tool's parameters refused for a union at their root
				// (#1271): asked again with them folded to an object, and
				// so from then on
				s.markUnfit(p.ID, rootUnionRefused(model), to)
				continue
			}
		}
		if req.Format != nil && refusesFormat(res.StatusCode, b) {
			if to != provider.Anthropic && req.Format.Type == "json_schema" {
				// a schema turned away by an upstream that takes any
				// JSON object (DeepSeek; Xiaomi MiMo's Token Plan,
				// #2126): asked again for an object, the schema in the
				// system prompt, and so from then on
				s.markUnfit(p.ID, schemaRefused(model), to)
				req = req.asObject()
				continue
			}
			// structured output turned away by the model or the relay in
			// front of it (a schema Anthropic can't hold, an object
			// refused too): asked again with the format in the system
			// prompt, and so from then on
			s.markUnfit(p.ID, formatRefused(model), to)
			req = req.inSystem()
			continue
		}
		if offEffort(req.Effort) && res.StatusCode == http.StatusBadRequest && (effortLevelsNamed.Match(b) || p.IsVertex() && geminiRefusedLevel(b) != "") {
			// reasoning turned off, which the model refuses naming the
			// levels it takes (Command Code's `expected one of "low"|…`
			// for Claude Code's auto mode classifier, #394), or Vertex AI
			// refuses minimal for: asked again at its lowest, and so from
			// then on
			s.markUnfit(p.ID, offRefused(model), to)
			if beforeOff != nil {
				// the none was #176's, for tools the upstream takes with no
				// reasoning on chat (#2670: a relay that names none and
				// refuses it): any other level is refused as the first
				// was, so the request isn't asked again at its lowest. The
				// tools refusal is the answer, which wrongEndpoint reads:
				// the provider has no other API to try, a group's next
				// member is asked (shapeRefused), and the agent is told the
				// Responses URL to set
				req, b = beforeOff, toolsRefusal
				beforeOff = nil
				res.Body = io.NopCloser(bytes.NewReader(b))
			} else {
				r := *req
				r.Effort, req = onEffort(p, model), &r
				continue
			}
		}
		if (req.Effort == "" || offEffort(req.Effort)) && beforeOff == nil && res.StatusCode == http.StatusBadRequest && needsReasoning(req.Effort, b) {
			// no reasoning effort, or none, turned away by a model that
			// needs one (a Codex title on a model other than its own
			// asks none, cybercaius on Discord): asked again once at its
			// lowest, low where its levels aren't known
			if offEffort(req.Effort) {
				s.markUnfit(p.ID, offRefused(model), to)
			}
			r := *req
			r.Effort, req = onEffort(p, model), &r
			continue
		}
		if req.OwnEffort {
			if refused, value := ownEffortRefused(res.StatusCode, b, req.Effort); refused {
				// the effort magpie added for auto mode's classifier
				// (fitAutoModeClassifier) turned away: none, by a model
				// that reasons, is asked again at low; any other refusal,
				// the field's, without it, as before. Remembered either way
				r := *req
				if value && offEffort(req.Effort) {
					s.markUnfit(p.ID, offRefused(model), to)
					r.Effort = onEffort(p, model)
				} else {
					s.markUnfit(p.ID, ownEffortField(model), to)
					r.Effort = ""
				}
				req = &r
				continue
			}
		}
		if res.StatusCode == http.StatusBadRequest && signaturesReach(p, to, model) && signatureRefused.Match(b) {
			// a signature Google won't take (a client changed it, or
			// another provider gave it before magpie noted whose it was):
			// the calls waved through instead, for this request
			if r, had := withoutSignatures(req); had {
				req = r
				continue
			}
		}
		if forcesTool(req.ToolChoice) && res.StatusCode == http.StatusBadRequest && toolChoiceRefused.Match(b) {
			// a model that can't be made to call a tool (#668): asked
			// again with the call left to it, and so from then on
			s.markUnfit(p.ID, forcedRefused(model), to)
			req = unforced(req)
			continue
		}
		if to == provider.Chat && res.StatusCode == http.StatusBadRequest && req.Effort != "none" &&
			toolsWithoutEffort.Match(b) && !s.servesElsewhere(p, model, to) && !offAsked && !s.turnedAway(p, model, "none", to) {
			// tools with reasoning refused on chat, and no Responses API
			// to take them to: asked again without reasoning (#176). Once
			// none is known refused it isn't asked again (the loop would
			// take it up to the lowest level, refused the same): the
			// refusal goes on as one of the endpoint (#2670)
			beforeOff, toolsRefusal, offAsked = req, b, true
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
		if req.OwnTier && req.Tier != "" && slices.Contains(refusedOptional(res.StatusCode, b, body), tierField) {
			// the field turned away: asked again without it, and not sent
			// it again. A value it doesn't offer reaches the client as the
			// upstream said it, as on a relayed request
			s.markUnfit(p.ID, tierField, to)
			r := *req
			r.Tier, req = "", &r
			continue
		}
		// magpie's own key is what a refusal is about only when it says so:
		// another 400 is not asked again for it
		ownKeyKept := ownKey != "" && req.CacheKey == ownKey && !refusesField(res.StatusCode, b, cacheKeyField)
		if req.CacheKey != "" && !ownKeyKept && badRequest(res.StatusCode) && !wrongEndpoint(res.StatusCode, b) && !provider.CopilotRefusal(b) {
			// a vendor that turns away fields it doesn't know is asked again
			// without the cache key, and not sent it again once that works —
			// at once when its error names the key; not every error does
			if refusesField(res.StatusCode, b, cacheKeyField) {
				s.markUnfit(p.ID, cacheKeyField, to)
			} else {
				dropped = true
			}
			ownKey = ""
			r := *req
			r.CacheKey, req = "", &r
			continue
		}
		dropped = false
		// Responses is asked first there only for what it tells of the
		// cache, not because the model needs it: a model the vendor doesn't
		// serve on it (404) is asked on Chat, as before #1578
		cacheOnly := to == provider.Responses && res.StatusCode == http.StatusNotFound && p.ResponsesTellsCache()
		if !cacheOnly && !wrongEndpoint(res.StatusCode, b) && !s.noChannelElsewhere(p, model, to, res.StatusCode, b) {
			return res, to, nil
		}
		s.markUnfit(p.ID, model, to)
		next := s.usable(p, model)
		if len(next) == 0 {
			return res, to, nil
		}
		to, beforeOff = next[0], nil
	}
}

// toolsWithoutEffort is a chat completions endpoint refusing function tools
// with reasoning, which it takes on Responses or with reasoning_effort
// "none": Bedrock's for its GPT models (#176: "Function tools with
// reasoning_effort are not supported for global.openai.gpt-6-luna in
// /v1/chat/completions. To use function tools, use /v1/responses or set
// reasoning_effort to 'none'.").
var toolsWithoutEffort = lazyre.New(`(?is)tools with reasoning_effort are not supported.*reasoning_effort to .?none`)

// effortRequired is an upstream turning a request away for asking no
// reasoning effort, or none, from a model that needs one: "reasoning
// effort is required", "missing required parameter: reasoning.effort",
// "this model requires a reasoning effort", "reasoning cannot be
// disabled". A refusal of a level the model hasn't, or of the field
// itself, is not one.
var effortRequired = lazyre.New(`(?i)reasoning[ _.]?effort\W+(?:is\s+)?(?:required|mandatory|missing)\b|(?:missing|requires?|needs?)\s+(?:a\s+|the\s+)?(?:required\s+)?(?:parameter\W+)?reasoning(?:[ _.]?effort)?\b|reasoning\s+is\s+(?:required|mandatory)\b|reasoning\W+(?:cannot|can't|can not)\s+be\s+(?:disabled|turned off)`)

// needsReasoning is b turning effort away as effortRequired does, or,
// for reasoning asked off (none, minimal), in the words of a model that
// always thinks (alwaysThinks), which name no levels: Kimi Code's K3.1
// Pro answers a Chat request's none "K3.1 Pro does not support disabled
// thinking mode" as its Messages endpoint does (#3739).
func needsReasoning(effort string, b []byte) bool {
	return effortRequired.Match(b) || offEffort(effort) && alwaysThinks.Match(wordsOf(b))
}

// OpenRouter rejects an explicit reasoning-off request for some endpoints.
var mandatoryReasoning = lazyre.New(`(?i)reasoning is mandatory for this endpoint and cannot be disabled`)

// servesElsewhere reports whether p serves model on an API besides proto
// that hasn't turned it away.
func (s *Server) servesElsewhere(p provider.Provider, model string, proto provider.Protocol) bool {
	return slices.ContainsFunc(s.usable(p, model), func(x provider.Protocol) bool { return x != proto })
}

// additionalToolsItem is the Responses input item a deferred tool goes to
// the upstream in where a result loaded it (Request.LoadedInPlace); a
// provider that refused it is remembered under it in unfit.
const additionalToolsItem = "additional_tools"

// loadsInPlace is whether p's Responses API is sent a tool an Anthropic
// client's tool search loaded in an additional_tools item at the place it
// was loaded (#2660): OpenAI's own API with a key, whose tool search guide
// documents the item and that its tools load at the end of the context
// so the cached prefix is kept, and a provider the user said takes it
// (provider.Provider.AdditionalTools), and a ChatGPT (Codex) account:
// the ChatGPT backend takes the item where it was loaded, the model calls
// the tools in it, and the cached prefix is kept (#4738: 7680 tokens read
// from cache on each load where it read none with the tools moved up).
// Nothing else is, unless the user says so: xAI's Responses API answers
// 422 "unknown item type" (#350), and a relay, another vendor's sign-in
// or a plugin's vendor hasn't been tried. additional-tools=off
// (AdditionalToolsOff) turns the default off.
func loadsInPlace(p provider.Provider) bool {
	if p.AdditionalToolsOff {
		return false
	}
	if p.AdditionalTools {
		return true
	}
	if p.Account != nil {
		return p.Account.Agent == "codex"
	}
	h := p.VendorHost()
	return !p.IsPlugin() && !p.IsAzure() && (h == "api.openai.com" || strings.HasSuffix(h, ".api.openai.com"))
}

// cacheKeyField is the client's prompt cache key as a request carries it
// upstream; a provider that refused it is remembered under it in unfit.
const cacheKeyField = "prompt_cache_key"

// ownCacheKey says a request translated for p goes with a prompt cache
// key for its conversation when the client sent none (an Anthropic
// client's never has one): OpenAI's API routes a conversation by it to
// where its prompt is cached, and a relay in front of it can refuse a
// request without it ("prompt_cache_key is required", CyberCaius's
// AnyRouter on Discord). Only OpenAI's own API with a key, Mistral's
// (its prompt_cache_key keeps a conversation on the server that holds its
// cache; daoche_wuzui on X), and a provider
// the user added by its address, which may be such a relay: a vendor's
// preset, a signed-in account (the ChatGPT backend gets its own, as Codex
// CLI sends it), a plugin, Bedrock and Vertex are sent nothing they didn't
// get before, nor is a provider whose extraBody names the field: the
// user's value is the one sent. One whose 400 names the field is asked again without it and
// not sent it again (forwardTranslated); another 400 is not retried for
// it.
func ownCacheKey(p provider.Provider) bool {
	if p.Account != nil || p.IsPlugin() || p.IsBedrock() || p.IsVertex() {
		return false
	}
	if _, ok := p.ExtraBody[cacheKeyField]; ok {
		return false // the user's own extraBody key (#1604) is theirs to fill
	}
	if _, ok := p.ExtraBody["!"+cacheKeyField]; ok {
		return false
	}
	if p.AddedByAddress() || isMistral(p) {
		return true
	}
	h := p.VendorHost()
	return h == "api.openai.com" || strings.HasSuffix(h, ".api.openai.com")
}

func isMistral(p provider.Provider) bool {
	return p.Preset == "mistral" || p.Host() == "api.mistral.ai"
}

// mistralAffinityHeader is the header Mistral routes a conversation's
// requests by, to the server that holds its KV cache. Mistral's own Vibe
// sends it every turn (its CHANGELOG: "KV cache warming via x-affinity";
// vibe/_runtime_config.py request_headers), and pi-ai's
// buildMistralHeaders sets it from the session id unless the model's or
// the call's headers name one (daoche_wuzui on X).
const mistralAffinityHeader = "x-affinity"

// mistralAffinity is the x-affinity a request to Mistral's API goes with:
// the client's own, as it sent it, else a hash of its session or
// conversation, never the id itself, the same for every turn of it. Empty
// for any other vendor, and for a request with no conversation to name.
// The user's own headers for the provider are put on after it.
func mistralAffinity(p provider.Provider, in http.Header, session string) string {
	if !isMistral(p) || p.Account != nil || p.IsPlugin() {
		return ""
	}
	if v := strings.TrimSpace(in.Get(mistralAffinityHeader)); v != "" {
		return v[:min(len(v), 128)]
	}
	if session == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("mistral-affinity\x00" + session))
	return hex.EncodeToString(sum[:16])
}

// withMistralCacheKey gives a Chat body bound for Mistral the
// prompt_cache_key Mistral routes a conversation by to where its prompt is
// cached (daoche_wuzui on X: poor cache hits). The key is a hash of the
// session or conversation, never the id itself, and a key the client sent,
// or the user's extraBody, is the one kept. A body with no conversation to
// name gets none.
func withMistralCacheKey(p provider.Provider, in http.Header, body []byte) []byte {
	if !isMistral(p) || !ownCacheKey(p) || gjson.GetBytes(body, cacheKeyField).Exists() {
		return body
	}
	id := cmp.Or(sessionOf(in), conversationID(in, body))
	if id == "" {
		return body
	}
	sum := sha256.Sum256([]byte("mistral\x00" + id))
	// written in ahead of the first member, so the client's own bytes
	// (and the order its messages were sent in, for the cache) stay as sent
	i := bytes.IndexByte(body, '{')
	if i < 0 || len(bytes.TrimSpace(body[:i])) > 0 {
		return body
	}
	rest := bytes.TrimLeft(body[i+1:], " \t\r\n")
	sep := ","
	if len(rest) == 0 || rest[0] == '}' {
		sep = ""
	}
	out := make([]byte, 0, len(body)+96)
	out = append(out, body[:i+1]...)
	out = append(out, `"`+cacheKeyField+`":"`+hex.EncodeToString(sum[:])+`"`+sep...)
	return append(out, body[i+1:]...)
}

// cacheKeyOf is the prompt cache key of the conversation req is a turn
// of: the session the client names (sessionOf), else its first user
// message, which every later turn repeats, hashed into a UUID's shape, so
// it is the same for every turn and names nothing of the client, the
// user or magpie.
func cacheKeyOf(in http.Header, req *Request) string {
	h := sha256.New()
	if v := sessionOf(in); v != "" {
		io.WriteString(h, "session\x00"+v)
	} else {
		io.WriteString(h, "first\x00")
		for _, m := range req.Messages {
			if m.Role != "user" {
				continue
			}
			for _, pt := range m.Parts {
				fmt.Fprintf(h, "%v\x00%s\x00%s\x00%s\x00%s\x00", pt.Kind, pt.Text, pt.MediaType, pt.Data, pt.URL)
			}
			break
		}
	}
	x := hex.EncodeToString(h.Sum(nil)[:16])
	return x[:8] + "-" + x[8:12] + "-" + x[12:16] + "-" + x[16:20] + "-" + x[20:]
}

// refusesField recognizes a vendor turning a request away for a field it
// doesn't take — Gemini's "Unknown name", Mistral's extra_forbidden,
// Groq's "unsupported" — by the field's name in the error.
func refusesField(status int, body []byte, field string) bool {
	return badRequest(status) && bytes.Contains(body, []byte(field))
}

// hourMarksRefused is an upstream that turned away the hour-long cache
// marks magpie adds (Request.hourMarks), as remembered in unfit.
const hourMarksRefused = "cache_control.ttl"

// hourMarksRefusal is a 400 over a cache mark's ttl: a relay that knows no
// ttl, or a 1h mark after a 5m one.
func hourMarksRefusal(status int, b []byte) bool {
	return badRequest(status) && bytes.Contains(b, []byte("ttl")) && bytes.Contains(b, []byte("cache_control"))
}

// tierField is the client's service tier as a request carries it upstream
// (Request.Tier); a provider that refused it is remembered under it in unfit.
const tierField = "service_tier"

// optionalFields are request fields OpenAI's APIs (or an agent's own
// vendor, Kimi's and Qwen's thinking switches) take that a request does
// without: an upstream that refuses one by name is asked again without it.
var optionalFields = []string{"store", "metadata", tierField, cacheKeyField, "prompt_cache_retention",
	"prompt_cache_options", "safety_identifier", "stream_options", "parallel_tool_calls", "verbosity", "thinking", "enable_thinking"}

// refusedOptional lists optional fields an upstream explicitly rejects as
// unsupported. An invalid value must reach the client without dropping the
// field or caching it as unsupported for later requests.
func refusedOptional(status int, msg, body []byte) []string {
	if !badRequest(status) {
		return nil
	}
	var m map[string]json.RawMessage
	if decodeJSON(body, &m) != nil {
		return nil
	}
	var fault any
	if decodeJSON(msg, &fault) != nil {
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

var unknownOptionalField = lazyre.New("(?i)\\b(?:unknown (?:name|field|parameter)|unsupported (?:parameter|field|property|argument)|unrecognized (?:request )?(?:argument|parameter)(?: supplied)?):?\\s*['\"\\x60]([a-z_]+)['\"\\x60]")
var unquotedUnsupportedOptionalField = lazyre.New("(?i)\\bunsupported (?:parameter|field|property|argument):\\s*([a-z_]+)\\b")

// chineseRefusedOptionalField is a relay refusing a field in Chinese:
// 基元律动's "未知请求字段：prompt_cache_retention" (ldl123292 on X).
var chineseRefusedOptionalField = lazyre.New("(?:未知|未识别|不支持|不允许|无效)(?:的)?(?:请求)?(?:字段|参数|属性)\\s*[:：]?\\s*['\"\\x60「“]?([a-z_]+)")

var rejectedOptionalField = lazyre.New("(?i)(?:^|\\b(?:parameter|field|property|argument)\\s+)['\"\\x60]([a-z_]+)['\"\\x60]\\s+(?:is\\s+)?(?:unsupported|not supported)\\b")

// leadingRejectedOptionalField is a message that starts with the field's
// bare name: OpenAI's "prompt_cache_options is not supported on this
// model" (#2154). Only at the start, so a field named further on, in an
// echo of the input, is not taken for it.
var leadingRejectedOptionalField = lazyre.New("(?i)^\\s*([a-z_]+)\\s+is\\s+(?:unsupported|not supported)\\b")

// valueRejectedForField is a vendor naming the field after the value it
// turned away: litellm's "'priority' is not supported for 'service_tier'
// on this model" (#3978). The name after "for" is what separates this
// from a value's refusal ("... is not supported with this model"),
// which reaches the client as it came.
var valueRejectedForField = lazyre.New("(?i)['\"\\x60][^'\"\\x60]{0,64}['\"\\x60]\\s+is\\s+(?:unsupported|not supported)\\s+for\\s+['\"\\x60]([a-z_]+)['\"\\x60]")

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
		for _, match := range chineseRefusedOptionalField.FindAllStringSubmatch(v, -1) {
			if match[1] == field {
				return true
			}
		}
		for _, match := range rejectedOptionalField.FindAllStringSubmatch(v, -1) {
			if match[1] == field {
				return true
			}
		}
		for _, match := range valueRejectedForField.FindAllStringSubmatch(v, -1) {
			if match[1] == field {
				return true
			}
		}
		if match := leadingRejectedOptionalField.FindStringSubmatch(v); match != nil && match[1] == field {
			return true
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
	return withoutMessageReasoning(body, ms)
}

// messageField is the name a field of a request's messages is remembered
// under in unfit once a provider has refused it.
func messageField(f string) string { return "messages[]." + f }

// refusedInMessages lists the thinking fields (messageReasoning) an
// upstream turned a Chat request away for where its messages carry them:
// extra_forbidden located in the messages, as Mistral's loc ["body",
// "messages",2,"assistant","reasoning_content"] (#494), or one named at a
// message's position, as Kimi's validator (and a relay in front of it)
// names it: "the reasoning_details at position 2 entry 0 must not contain
// streaming index" (#1765).
func refusedInMessages(status int, msg, body []byte) []string {
	if !badRequest(status) {
		return nil
	}
	if m := fieldAtPosition.FindSubmatch(msg); m != nil {
		if f := string(m[1]); bytes.Contains(body, []byte(`"`+f+`"`)) {
			return []string{f}
		}
	}
	var fault any
	if i := bytes.IndexByte(msg, '{'); i < 0 || decodeJSON(msg[i:], &fault) != nil {
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

// fieldAtPosition is Kimi's refusal of a thinking field in a message.
var fieldAtPosition = lazyre.New(`\bthe (reasoning_details|reasoning_content|reasoning) at position \d+\b`)

// badRequest is a status an upstream refuses a request's contents with.
func badRequest(status int) bool {
	return status == http.StatusBadRequest || status == http.StatusUnprocessableEntity
}

// wrongEndpoint is catalog.WrongEndpoint, whose words a model's test
// reads too (#3766).
func wrongEndpoint(status int, body []byte) bool { return catalog.WrongEndpoint(status, body) }

// noChannelElsewhere is whether a relay said it has no channel for model on
// proto (catalog.NoChannel) while p speaks another API that may serve it. A
// relay with channels per API (New API) answers a GPT asked on its Anthropic
// URL so, and the request goes on to the next API, as for wrongEndpoint.
func (s *Server) noChannelElsewhere(p provider.Provider, model string, proto provider.Protocol, status int, body []byte) bool {
	return catalog.NoChannel(status, body) && slices.ContainsFunc(s.usable(p, model), func(o provider.Protocol) bool { return o != proto })
}

// wrongAPINote goes after an upstream's word that the model isn't served
// on the API it was asked on (wrongEndpoint), when no other API is left to
// ask: the URL asked, and the APIs the vendor's list serves the model on
// that the provider has no URL for, so "Model does not support this
// protocol" says which protocol, and what to set (#1215).
func wrongAPINote(p provider.Provider, model string, res *http.Response, body []byte) string {
	note := ""
	if res != nil && res.Request != nil && res.Request.URL != nil {
		u := *res.Request.URL
		u.RawQuery, u.User = "", nil
		note = " (asked at " + u.String()
	}
	paths := map[provider.Protocol]string{provider.Chat: "/v1/chat/completions", provider.Responses: "/v1/responses", provider.Anthropic: "/v1/messages"}
	var missing []string
	apis := p.APIs(model)
	if asksResponses(body) && !slices.Contains(apis, provider.Responses) {
		// the vendor's error names the API, where its list doesn't (#1308)
		apis = append(apis, provider.Responses)
	}
	for _, a := range apis {
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

// asksResponses is an upstream's word that the model is served, or served
// with what was asked, on OpenAI's Responses API: where a provider has no
// URL for it, wrongAPINote says to set one.
func asksResponses(body []byte) bool {
	msg := strings.ToLower(string(wordsOf(body)))
	for _, phrase := range []string{"requires responses", "use v1/responses", "use /v1/responses", "only supported in v1/responses", "only supported in /v1/responses", "改用 /v1/responses"} {
		if strings.Contains(msg, phrase) {
			return true
		}
	}
	return false
}

// cacheControlField is the mark a Messages client of Anthropic's puts on a
// block of its own.
var cacheControlField = []byte("cache_control")

// parseTranslated parses the request a translation rebuilds. An Anthropic
// request to an Anthropic-speaking upstream that carries cache_control of
// its own is read with its marks (#4571), so the rebuild can keep them
// where the client put them: a turn whose history only grew then reads
// the turn before at the boundary the client marked, instead of writing
// the whole history again. A set that fails the checks any Messages
// request must pass goes the default way, its marks dropped as before,
// not refused with a 400 the old translation never gave (#3835). Every
// other request is one bytes.Contains away from this, a scan next to the
// parse of the same body.
func parseTranslated(from, to provider.Protocol, body []byte, advised bool) (*Request, error) {
	if from == provider.Anthropic && to == provider.Anthropic && bytes.Contains(body, cacheControlField) {
		if req, err := readAdvised(body, true, false, advised); err == nil {
			return afterParse(from, body, req)
		}
	}
	if advised && from == provider.Anthropic {
		req, err := readAdvised(body, false, false, true)
		if err != nil {
			return nil, err
		}
		return afterParse(from, body, req)
	}
	return parseRequest(from, body, false, to == provider.Responses)
}

// translate serves a client API the provider lacks by speaking another
// one to it. The provider is always streamed; the client gets whichever
// it asked for.
func (s *Server) translate(w http.ResponseWriter, r *http.Request, p provider.Provider, from, to provider.Protocol, model string, body []byte, u *Usage) (int, string) {
	if from == provider.Responses && hasUnportableCurrentImage(body) {
		msg := "input_image without image_url cannot be translated; send an image_url or use a native Responses route"
		return writeError(w, from, 400, msg), msg
	}
	// Claude Code's advisor, run by magpie for a model whose upstream
	// can't (advisor_serve.go)
	advised := advisedHere(p, from, to, model, body, wiresOf(r.Context()))
	request, err := parseTranslated(from, to, body, advised)
	if err != nil {
		return writeError(w, from, 400, err.Error()), err.Error()
	}
	if request.WebSearch {
		// an API on which the provider searches by itself comes first;
		// without one, its model is given magpie's search. A searcher
		// asked for another model goes there too: on its first API
		// (DeepSeek's Chat) it had no search tool and answered from
		// nothing (#669)
		for _, t := range s.usable(p, model) {
			if searchesFor(p, t, model, request) && !magpieSearches(r.Context(), p) {
				if from == provider.Responses && (to == provider.Responses) != (t == provider.Responses) {
					request, err = parseRequest(from, body, false, t == provider.Responses)
					if err != nil {
						return writeError(w, from, 400, err.Error()), err.Error()
					}
				}
				to = t
				break
			}
		}
	}
	// the carried reasoning this key or account refused in the
	// conversation (reasoning_carry.go, #4798)
	withoutRefusedCarry(r.Context(), p, request)
	// a glm-5.3-flash* replica's CR stitched into a call's arguments
	// (tool_args_cr.go, #4256)
	request.CR = glmServed(wiresOf(r.Context()), p, model)
	// Grok's reasoning, carried in its calls' ids (reasoning_carry.go)
	askSealedReasoning(p, from, request)
	// a model's 2500.0 in a call's arguments, which Codex turns away, is
	// rewritten at the encoder and at render (grok_integral.go, #1431,
	// #3152)
	if from == provider.Anthropic {
		// Claude Code's auto mode classifier, on a model that reasons
		// whatever it is told (#250)
		fitAutoModeClassifier(p, model, request)
	}
	var zen *zenReply
	if p.OpenCodeFree(model) {
		zen = &zenReply{z: zenFreeTools(request)}
	}
	search := request.WebSearch && !searching(r.Context()) && canSearchFor(p) && (!searchesFor(p, to, model, request) || magpieSearches(r.Context(), p))
	if search || request.Advisor != "" {
		ask := antigravityRound(w, p, s.askTranslated(p, to, model, r.Header, w.Header()))
		if zen != nil {
			ask = zenRound(zen.z, ask)
		}
		if search {
			// a Kimi Code plan's own searches stay on it
			r = r.WithContext(searchingOn(r.Context(), p))
		}
		return s.searchReply(w, r, from, p.Name, request, u, ask, search)
	}
	if msg := unportableVideo(to, request); msg != "" {
		// told, not left out for the model to answer the words alone (#6135)
		return writeError(w, from, 400, msg), msg
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
	if res.StatusCode >= 400 && p.PassesErrors() {
		return passError(w, p, from, actual, res, u)
	}
	if res.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		msg := explained(p, model, res, b)
		if wrongEndpoint(res.StatusCode, b) {
			msg += wrongAPINote(p, model, res, b)
		}
		turnedAway := res.StatusCode == http.StatusTooManyRequests && antigravityTurnedAway(p, request.System, string(b))
		if turnedAway {
			msg += " — " + antigravityHint(request.System)
			markAntigravityTurnsAway(w, request.System)
		}
		if p.VendorPreset() == "openrouter" && openRouterSharedPool(b) {
			markOpenRouterSharedPool(w)
		}
		keepRetry(w.Header(), res.Header, b)
		u.ErrType = provider.ErrorType(b)
		if turnedAway {
			u.ErrType = turnedAwayErrType
		}
		return writeVendorErrorFrom(w, from, actual, p, res, msg, b), msg
	}
	dec := carryReasoning(decoderFor(p, actual, request.Tools), from, actual)
	rd, sse := eventStream(res)
	if !sse {
		// the provider ignored stream:true; read the whole reply as one
		// event stream would be wrong, so give up cleanly
		b, _ := io.ReadAll(io.LimitReader(rd, 1<<20))
		msg := p.Name + " " + didNotStream + ": " + provider.APIError(b, "unexpected reply")
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
	ended := false // the upstream said its reply ended
	spent := false // the reply spent some of its tokens (endsAtProbeCap)
	var tail chatTail
	var found searchTally // the searches it ran by itself
	see, held := written(zenSee(zen, func(ev Event) {
		spent = spent || spends(ev)
		col.add(ev)
	}))
	err = readSSE(rd, func(_, data string) error {
		tail.see([]byte(data))
		found.see(actual, []byte(data))
		u.add(Usage{Upstream: upstreamOf([]byte(data)), Tier: upstreamTier([]byte(data)), searches: found.count()})
		ended = ended || replyEnds(actual, data, upstreamStop([]byte(data)))
		return dec(data, see)
	})
	if fin := tail.finish(); err == nil && !ended && actual == provider.Chat && fin != "" && saysItsEnd(p, actual, model) {
		// Gemini's OpenAI-compatible API lost only the finish_reason
		// after its generation was done (chatTail)
		ended, err = true, dec(string(finishChunk(fin)), see)
	}
	held()
	if err != nil {
		// a partial answer is not an answer
		msg := p.Name + ": " + err.Error()
		return writeError(w, from, 502, msg), msg
	}
	// a reply the vendor refused part way is no answer either: handed
	// whole, its words so far went as a finished turn and the refusal as
	// a failure after it, which rested the account (#3274). Nothing has
	// reached the client yet, so it is told the refusal, as it would be
	// with nothing said
	if col.err != "" && !saidAnything(col.res.Parts) && thinkingNeededRefusal.MatchString(col.err) && s.fits(p.ID, thinkingNeeded(model), actual) {
		// the refusal of a request sent no thinking, carried in the
		// stream rather than as a 400 (forwardTranslated asks again for
		// that one): remembered, and asked once more, thinking
		s.markThinkingNeeded(p, actual, model, provider.UpstreamNameIn(wiresOf(r.Context()), p.ID, model))
		return s.translate(w, r, p, from, to, model, body, u)
	}
	if col.err != "" && (!saidAnything(col.res.Parts) || col.errSaid != nil) {
		said := col.err
		if markAntigravityRefused(w, p, request.System, said) {
			said += " — " + antigravityHint(request.System)
			u.ErrType = turnedAwayErrType
		}
		if code := gjson.GetBytes(col.errSaid, "error.code").String(); code != "" {
			e, _ := json.Marshal(map[string]string{"code": code, "message": col.err})
			if note, ok := policyRefusal(e); ok {
				// the safety filter's: said beside the error, which loses
				// its code in Anthropic's shape, for settle to ask the next
				// account and rest nobody (#3274), as keepRetry notes a
				// status refusal
				w.Header().Set(refusedHeader, note)
			}
		}
		// a request the vendor refused in its stream is answered as
		// refused, with its code (magpie-releases#18); anything else is
		// the vendor failing, with the status its words would have come
		// with: a minute limit in a 200 stream is a 429, not a 502
		return writeVendorError(w, from, cmp.Or(col.errStatus, streamStatus(said)), p.Name+": "+said, col.errSaid), said
	}
	if col.err == "" && !ended && saysItsEnd(p, actual, model) && r.Context().Err() == nil && endsAtProbeCap(actual, request.MaxTokens, spent) {
		// a short probe's reply that stopped at its own small limit with
		// no end said (endsAtProbeCap, plugins#827): ended at its length,
		// as the vendor should have said
		col.add(Event{Kind: KStop, Stop: "length"})
		ended = true
	}
	if col.err == "" && !ended && saysItsEnd(p, actual, model) && r.Context().Err() == nil {
		// an Anthropic stream that just stopped — no stop_reason, no
		// message_stop — or a Responses one with no response.completed is
		// a reply cut short, not a finished one, as a streaming client is
		// told (streamTranslated): handed whole, a non-streaming client
		// ends its turn a few words in, as if done. A Gemini one is told
		// by its finishReason (geminiEnds, #1643)
		msg := p.Name + ": " + errEndedShort.Error()
		return writeError(w, from, 502, msg), msg
	}
	if (empty || repliesCounted(actual) && uncounted(col.res.Usage)) && !saidAnything(col.res.Parts) && answersNothing(col.res.Stop) {
		msg := p.Name + ": " + emptyReply
		return writeError(w, from, 502, msg), msg
	}
	if cutVoidAt(actual) && replyCheck(r.Context()) == nil &&
		cutVoid(col.res.Stop, !saidAnything(col.res.Parts), col.res.Usage.Output, request.MaxTokens) {
		// at once, asked again nowhere: a void's budget is spent, and a
		// second ask at the same max_tokens would mostly spend it twice
		// over before the error (#2972, its review)
		msg := cutVoidFailed(p.Name, request.MaxTokens)
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

// repliesCounted tells whether every whole reply in proto counts its tokens:
// Anthropic's message_start counts the prompt, Responses' response.completed
// its usage. One of these that says nothing and counted nothing is no reply
// at all, whatever its end says: #2086's proxy ended a Codex turn with a
// stop and no tokens, which went to Codex as a completed turn with no
// message, and Codex didn't ask again. Claude's own empty end_turn counts
// its prompt and stays an answer.
func repliesCounted(proto provider.Protocol) bool {
	return proto == provider.Anthropic || proto == provider.Responses
}

// uncounted tells whether a reply's usage counted no token at all.
func uncounted(u Usage) bool {
	return u.Input+u.Output+u.CacheRead+u.CacheWrite == 0
}

// answersNothing tells whether a reply with nothing said ended for no
// reason of its own: not cut at its length (all reasoning), nor by a
// safety filter (told as a refusal, #248).
func answersNothing(stop string) bool {
	return stop != "length" && stop != "filter"
}

// cutVoidTokens is a short probe's worth of output tokens. A reply capped
// at, or cut having spent no more than, this said nothing because it was
// asked for little — a title's, a probe's (probeTranslated asks 16) —
// not because the model went void (#2972 review).
const cutVoidTokens = 1024

// cutVoidAt tells whether a reply in proto cut at its length that says
// nothing is a void: a Chat upstream's is (#2972: qoder/dfmodel ended
// four replies of one turn with 48,000 tokens each, every one of them
// reasoning, and the agent, told the output limit was reached, went on
// at the user's "continue" and burned the same void again). A Gemini's
// and a Code Assist's goes as it came (#667), and a title, description
// or prediction asked of the reply keeps the response.incomplete it was
// (#1466): its cut is wanted, a small budget at a time.
func cutVoidAt(proto provider.Protocol) bool {
	return proto == provider.Chat
}

// cutVoid says whether this reply was one: cut at its length having said
// nothing — no text and no call, only thinking, or nothing at all — and
// no short probe's answer: the reply spent past a probe's worth, or the
// request hadn't capped its reply small (#2972 review).
func cutVoid(stop string, saidNothing bool, spent, cappedAt int) bool {
	return saidNothing && stop == "length" &&
		(spent > cutVoidTokens || cappedAt <= 0 || cappedAt > cutVoidTokens)
}

// cutVoidWords is what says a reply was cut at its length having said
// nothing: the same failure every ask, so the same one is not asked again
// after a moment for it (passing), though a group's next member still is.
const cutVoidWords = "cut at its length, the whole output spent on reasoning"

// cutVoidFailed is what a reply cut at its length that said nothing fails
// with: the empty reply's error — a group asks its next member for one
// and rests nobody (#1221) — said with why, for the agent to try again or
// ask for less reasoning rather than go on (#2972).
func cutVoidFailed(name string, cappedAt int) string {
	msg := name + ": " + emptyReply + " — " + cutVoidWords
	if cappedAt > 0 {
		// the limit the request itself named, and what to do about it
		msg += fmt.Sprintf(" (the request's limit was %d tokens: raise max_tokens in the agent, or ask for less reasoning)", cappedAt)
	}
	return msg
}

// saysSomething tells whether an event is any of a reply an agent shows or
// acts on: text, a call, a search, an image.
func saysSomething(ev Event) bool {
	switch ev.Kind {
	case KText:
		return ev.Text != ""
	case KToolStart, KSearch, KImage, KAdvice:
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
		// Factory's generateContent; a Gemini API's and Vertex AI's paths
		// name the model (upstreamPath)
		return "/generate"
	}
	return "/v1/messages"
}

// upstreamPath is the path under p's base for proto a request for model
// (the vendor's name for it) goes to: a Gemini API's names the model, and
// is always the stream, as Factory's generate route always answers one;
// any other API's is pathOf's. Vertex AI's names its publisher too
// (provider.VertexPath).
func upstreamPath(p provider.Provider, proto provider.Protocol, model string) string {
	if proto == provider.Gemini && p.IsVertex() {
		return provider.VertexPath(model)
	}
	if proto == provider.Gemini && !p.FactoryGemini() {
		return provider.GeminiPath(model, true)
	}
	return pathOf(proto)
}

// parse is shared by translated routes and account/subscription backends.
// Only the allowlist contracts below promise a required callable function:
// other protocols have server, custom and MCP tools the IR cannot render.
func parse(proto provider.Protocol, body []byte) (*Request, error) {
	return parseIn(proto, body, false)
}

// parseIn is parse, for facts only as readResponses, readChat and
// readAnthropic say.
func parseIn(proto provider.Protocol, body []byte, facts bool) (*Request, error) {
	return parseRequest(proto, body, facts, false)
}

// parseRequest keeps native instruction messages for Responses-to-Responses
// turns. Routing facts and subscription backends keep their existing mapping.
func parseRequest(proto provider.Protocol, body []byte, facts, keepInstructions bool) (*Request, error) {
	var req *Request
	var err error
	switch proto {
	case provider.Chat:
		req, err = readChat(body, facts)
	case provider.Responses:
		req, err = readResponsesFor(body, facts, keepInstructions)
	case provider.Gemini:
		req, err = parseGemini(body)
	default:
		req, err = readAnthropic(body, false, facts)
	}
	if err != nil {
		return nil, err
	}
	return afterParse(proto, body, req)
}

// afterParse is what every parse of a client's request does after reading
// it, whichever reader read it (parseRequest, parseTranslated).
func afterParse(proto provider.Protocol, body []byte, req *Request) (*Request, error) {
	// a call's id may carry Gemini's thought signature (gemini_signature.go)
	unsignCalls(req)
	// and the sealed reasoning a Responses upstream gave before it
	// (reasoning_carry.go)
	unreasonCalls(req)
	req.LastIsTurn = proto == provider.Chat || proto == provider.Responses
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
		return decodeJSON(body, &q) == nil && q.ToolChoice.Type == "allowed_tools" && q.ToolChoice.Mode == "required"
	case provider.Gemini:
		var q struct {
			ToolConfig struct {
				FunctionCallingConfig struct {
					Mode                 string   `json:"mode"`
					AllowedFunctionNames []string `json:"allowedFunctionNames"`
				} `json:"functionCallingConfig"`
			} `json:"toolConfig"`
		}
		return decodeJSON(body, &q) == nil && strings.EqualFold(q.ToolConfig.FunctionCallingConfig.Mode, "ANY") && len(q.ToolConfig.FunctionCallingConfig.AllowedFunctionNames) > 0
	}
	return false
}

// buildFor is build for p: Vertex AI's Gemini request has the model in its
// path, and its own thinking and signatures (buildVertex).
func buildFor(p provider.Provider, proto provider.Protocol, r *Request, model string) ([]byte, error) {
	if proto == provider.Gemini && p.IsVertex() {
		return buildVertex(r, model)
	}
	if proto == provider.Responses && p.Account != nil && p.Account.Agent == "codex" && !r.instructionsKept {
		// the account's send shapes the instructions itself (codexBody):
		// the client's system rides first as a developer item and the
		// input's system items stand as developer ones, in place — the
		// fold would move them out of conversation order (#4632)
		c := *r
		c.instructionsKept = true
		r = &c
	}
	if !r.videoOff && anyVideo(r) && noVideo(p.ID, model) {
		// a video of an earlier turn, to a model that takes none, is told
		// of in words (#6135)
		c := *r
		c.videoOff = true
		r = &c
	}
	return build(proto, r, model, buildHost(p), p.RejectsTemperature(model))
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
	replay := anthropicReplaysThinking(host, model)
	out := anthropicBody(r, model, host, replay)
	if replay {
		// a tool call with no thinking before it, which DeepSeek turns
		// the whole request away for (#1611)
		out = withThinkingOnToolTurns(out)
	}
	if r.Fast && host == "api.anthropic.com" && provider.ClaudeFast(model) {
		// Claude's fast mode, on the models that have it (its beta header
		// goes with it: forwardOnce)
		out = withFields(out, map[string]any{"speed": "fast"})
	}
	return out, nil
}

// decoderFor is decoder for p's replies. A plugin's Chat text is read as
// OpenCode reads it, as it came: the plugin knows its vendor and gives its
// reasoning apart, so a <think> at the head of its text is the model's
// words, not a block of thinking to take out (Qoder's, #2634).
//
// A ChatGPT account is given a declared tool whose name its backend
// refuses under another (provider.CodexToolName); the model's call to it
// comes back by the name the agent declared, from tools, the request's.
//
// Each Gemini thought signature the reply gives is noted as p's
// (signersNoted), so it goes back to p alone (ownSignatures).
func decoderFor(p provider.Provider, proto provider.Protocol, tools []Tool) func(data string, emit func(Event)) error {
	return signersNoted(decoderOf(p, proto, tools), p.ID)
}

func decoderOf(p provider.Provider, proto provider.Protocol, tools []Tool) func(data string, emit func(Event)) error {
	if proto == provider.Chat && p.IsPlugin() {
		d := &chatDecoder{past: true}
		return d.decode
	}
	dec := decoder(proto)
	if proto == provider.Responses && accountAgent(p) == "codex" {
		if back := codexNamesBack(tools); back != nil {
			return func(data string, emit func(Event)) error {
				return dec(data, func(ev Event) {
					if n, ok := back[ev.Name]; ok {
						ev.Name = n
					}
					emit(ev)
				})
			}
		}
	}
	return dec
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

// translatedQuietLongest is how long a translated reply's upstream may
// say nothing at all, its stream open, while magpie keeps the client
// alive every keepaliveEvery (keepThroughSilence). A reply that comes in
// one burst at its end (claude-opus-5-5 at effort max, a relay that
// buffers) is silent for its whole generation, which runs past 10
// minutes, longer than the 10 minutes magpie gives a vendor for its
// headers (upstreamHeaderWait); a dead connection ends sooner by itself
// (HTTP2Health, TCP keepalive). Past it the client's own idle timeout
// ends the wait: Claude Code's, 300s after its last ping counted.
var translatedQuietLongest = 30 * time.Minute

// pingEvery is the least time between two of Anthropic's ping events
// magpie sends one client; a keepalive due sooner is a comment. Claude
// Code (2.1.311) starts its 300s stream watchdog again on a ping only
// for 30 in a row (Gcr): pings a second apart, as magpie sent them
// while an upstream sent its own keepalives, spend those in 30s, and the
// watchdog ends the reply 300s later. One a minute lasts
// translatedQuietLongest,
// and the comments between keep the bytes going for a proxy and for
// Claude Code's byte watchdog.
var pingEvery = time.Minute

// keepThroughSilence keeps a translated reply's client alive while its
// upstream, its 200 and stream begun, says nothing at all: keep is
// called every every (keepaliveEvery, read by the caller on its
// request's own goroutine, since this one can outlive it), until stop, while the upstream was last
// heard (heard, at each event or comment it sends) less than
// translatedQuietLongest ago. keep is called holding lock, which the
// caller holds around everything else it writes to the client. Before,
// such a reply sent its client nothing until the upstream spoke, and
// Claude Code's watchdog ended it (youyo.p on Discord).
func keepThroughSilence(every time.Duration, lock sync.Locker, keep func()) (heard, stop func()) {
	var mu sync.Mutex
	last := time.Now()
	done, over := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(over)
		tick := time.NewTicker(every)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				mu.Lock()
				quiet := time.Since(last)
				mu.Unlock()
				if quiet >= translatedQuietLongest {
					continue
				}
				lock.Lock()
				keep()
				lock.Unlock()
			}
		}
	}()
	heard = func() {
		mu.Lock()
		last = time.Now()
		mu.Unlock()
	}
	stop = func() {
		close(done)
		<-over
	}
	return heard, stop
}

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
		enc = &usageEncoder{streamEncoder: enc, usage: usage[0]}
	}
	// a model's whole floats in a call's arguments reach the client as
	// integers (grok_integral.go, #1431, #3152), outside usageEncoder,
	// which reads its encoder's reply id
	enc = &wholeEncoder{streamEncoder: enc}
	if r.CR {
		enc = &crEncoder{streamEncoder: enc}
	}
	if r.wantsJSON() {
		// a fenced JSON answer reaches a structured-output client as the
		// JSON it asked for (format_fence.go)
		enc = &fenceEncoder{streamEncoder: enc}
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
	// a model's whole floats in a call's arguments reach the client as
	// integers (grok_integral.go, #1431, #3152)
	res = wholeResult(res)
	if r.CR {
		res = crResult(res)
	}
	res = unfencedResult(res, r)
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

// dashScope reports whether p is Alibaba Cloud's Model Studio (DashScope,
// Bailian, Qwen Cloud): Qwen's presets, or one of its hosts given as a
// custom provider.
func dashScope(p provider.Provider) bool {
	switch p.VendorPreset() {
	case "qwen", "qwen-cn", "qwen-token-plan", "bailian-token-plan", "alibaba-token-plan", "qwencloud", "qwencloud-token-plan":
		return true
	}
	h := p.VendorHost()
	return strings.HasPrefix(h, "dashscope") && strings.HasSuffix(h, ".aliyuncs.com") ||
		strings.HasSuffix(h, ".maas.aliyuncs.com") || h == "maas.qianwenaiapi.com" || strings.HasSuffix(h, ".maas.qianwenaiapi.com") ||
		h == "maas.qwencloudapi.com" || strings.HasSuffix(h, ".maas.qwencloudapi.com")
}

func streamOf(body []byte) bool {
	var v struct {
		Stream bool `json:"stream"`
	}
	decodeJSON(body, &v)
	return v.Stream
}

// decodeRequest requires one complete JSON object before fields are rewritten
// or a request is routed. In particular, null is not an empty object.
func decodeRequest(body []byte, dst any) error {
	body = bytes.TrimSpace(body)
	if len(body) == 0 || body[0] != '{' {
		return errors.New("invalid request: expected a JSON object")
	}
	if err := decodeJSON(body, dst); err != nil {
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
	if real, ok := provider.ClaudeUndashed(model); ok {
		// Claude Code is given a dotted Claude id with dashes, which it
		// otherwise reads as Claude Opus 4 (provider.ClaudeSpelled)
		return real, nil
	}
	// a found group's auto/<model> as group/auto-<slug>, the one id its
	// key limits, routing, stats and usage rows know it by
	return provider.CanonicalRef(unflat(model)), nil
}

// unflat is the catalog id a flat one stands for (provider.Unflat): MiniMax
// Code asks for "bb-codex~gpt-6.1-sol", as its SubAgents can't name a model
// with a slash in it (#1387). The request goes on as the catalog id, so its
// key's models, its routing, the middleware and the usage row see that. An
// id that resolves as it is stays as it is.
func unflat(id string) string {
	if !strings.Contains(id, provider.FlatSep) || strings.Contains(id, "/") {
		return id
	}
	if _, _, ok := provider.Resolve(id); ok {
		return id
	}
	if real, ok := provider.Unflat(id); ok {
		return real
	}
	return id
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
	decodeJSON(body, &v)
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
// (to its own gateway, and to everyone else), Pi, Codex, Claude Code,
// WorkBuddy / CodeBuddy (X-Conversation-ID) and DSH (#2505).
var sessionHeaders = []string{
	"x-opencode-session", "x-session-affinity", "x-session-id",
	"session_id", "session-id", "x-claude-code-session-id",
	"x-conversation-id", "x-deepseek-harness-session-id",
}

// dshCompactHeader is how DSH marks a request that compacts its context
// (#2505). Only the mark says so: a request without it is never taken for
// one by what its prompt says.
const dshCompactHeader = "x-deepseek-harness-compact"

// dshCompacts reports whether h carries DSH's compaction mark, set.
func dshCompacts(h http.Header) bool {
	switch strings.ToLower(strings.TrimSpace(h.Get(dshCompactHeader))) {
	case "", "0", "false", "no", "off":
		return false
	}
	return true
}

// SessionHeader names the session a call is part of, for the usage log to
// tell several sessions on one model apart; without it, the session an
// agent names itself in sessionHeaders is taken.
const SessionHeader = "X-Magpie-Session"

// AccountHeader pins a request to one of a subscription's accounts, by its
// user (an email, a login) or its id in the routing trace, percent-encoded
// when it isn't ASCII (pinnedName): only it is tried, and when it can't take the request the caller is told why rather
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

// subagentOf is the subagent a request is of, and the subagent that
// spawned that one, as Claude Code names them to a gateway
// (x-claude-code-agent-id, x-claude-code-parent-agent-id; neither is sent
// for the conversation's own turns). Its session header stays the
// conversation's, so the call is told apart within it, not from it.
func subagentOf(in http.Header) (agent, parent string) {
	agent = strings.TrimSpace(in.Get("x-claude-code-agent-id"))
	if agent == "" {
		return "", ""
	}
	parent = strings.TrimSpace(in.Get("x-claude-code-parent-agent-id"))
	return agent[:min(len(agent), 128)], parent[:min(len(parent), 128)]
}

// subOf is the subagent a request is of, as subagentOf has it.
func subOf(in http.Header) string {
	sub, _ := subagentOf(in)
	return sub
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
// from how the conversation opens, which every later turn repeats: its first
// user message, or, when the agent wrote that one alone, the user messages
// from it through the first that holds anything of the user's own
// (conversationOpening). One message is hashed as the request has it, as
// it always was; several are hashed as writeOpening reads them, so the
// cache mark an agent moves on from turn to turn doesn't move the id.
// affinityID is the conversation a turn's account is kept for: an agent's
// own session (conversationID), else the caller's X-Magpie-Session, else
// the conversation's hash. Two callers that send the same opening with
// their own X-Magpie-Session are two conversations (#5242). It is magpie's
// own key and never goes upstream: conversationID, which a vendor or a
// plugin is sent (Kilo, ConversationHeader), doesn't read X-Magpie-Session.
func affinityID(in http.Header, body []byte) string {
	for _, h := range sessionHeaders {
		if v := strings.TrimSpace(in.Get(h)); v != "" {
			return v
		}
	}
	if v := strings.TrimSpace(in.Get(SessionHeader)); v != "" {
		return v
	}
	return conversationID(in, body)
}

func conversationID(in http.Header, body []byte) string {
	for _, h := range sessionHeaders {
		if v := strings.TrimSpace(in.Get(h)); v != "" {
			return v
		}
	}
	var m struct {
		Messages json.RawMessage `json:"messages"`
		Input    json.RawMessage `json:"input"`
		Contents json.RawMessage `json:"contents"`
	}
	// An undecodable body still gets an id: the hash of the whole body.
	_ = decodeJSONView(body, &m)
	// The turns are the body's own bytes, not a copy of each: this runs on
	// every request, and a 1.5 MB conversation's copy was ~2.8 MB of each
	// request's allocations (yg6888 on X).
	items, contents := elementsOf(m.Messages), elementsOf(m.Contents)
	geminiContents := len(items) == 0 && len(contents) > 0
	if geminiContents {
		items = contents // Gemini generateContent repeats the first user turn.
	}
	if len(items) == 0 {
		items = elementsOf(m.Input)
	}
	first := []byte(m.Input)
	if len(items) > 0 {
		first = items[0]
	}
	if len(first) == 0 {
		first = body
	}
	opening := [][]byte{first}
	for i, it := range items {
		var r struct {
			Role string `json:"role"`
		}
		// Gemini treats an omitted role as user; Chat and Responses do not.
		if decodeJSONView(it, &r) == nil && (r.Role == "user" || (geminiContents && r.Role == "")) {
			opening = conversationOpening(items[i:], geminiContents)
			break
		}
	}
	h := sha256.New()
	if len(opening) == 1 {
		h.Write(opening[0])
	} else {
		w := bufio.NewWriterSize(h, 512)
		for i, msg := range opening {
			if i > 0 {
				w.WriteByte('\n') // two messages never hash as one message's bytes
			}
			writeOpening(w, msg)
		}
		w.Flush()
	}
	return "magpie-" + hex.EncodeToString(h.Sum(nil)[:12])
}

// dshLastConvs are the ids of the user messages a dsh request's turn
// carries, newest first: the run of user messages its newest one ends,
// at most four (sessions' dshTurnKept), each hashed as sessions' TurnConvs
// spell it (dshConvs' first, the message with its text alone). A fork
// copies its source's opening, so the opening names both; the turn's own
// messages, which the branch's file has before the request goes out, are
// what tell a request still answering which one it is of (Route.LastConvs,
// gui's ownerLast). The typed message is not always the newest: dsh sends
// its runtime context after it, the same in every session of a folder. nil
// for another agent, or a newest message that isn't text alone.
func dshLastConvs(agent string, body []byte) []string {
	if agent != "dsh" {
		return nil
	}
	var m struct {
		Messages json.RawMessage `json:"messages"`
	}
	if decodeJSONView(body, &m) != nil {
		return nil
	}
	items := elementsOf(m.Messages)
	var out []string
	for i := len(items) - 1; i >= 0 && len(out) < 4; i-- {
		// read by its members, not decoded: a long run of tool steps is
		// passed over on the way to the turn's messages
		user, content := false, []byte(nil)
		if it := items[i]; len(it) > 0 && it[0] == '{' {
			eachMember(it, func(k, v []byte) bool {
				switch {
				case keyIs(k, "role"):
					user = string(v) == `"user"`
				case keyIs(k, "content"):
					content = v
				}
				return true
			})
		}
		if !user {
			if out != nil {
				break // the run of the turn's user messages ends here
			}
			continue
		}
		t, ok := oneTextPart(content)
		if !ok {
			c := trimJSON(content)
			if out == nil && toolResultsOnly(c) {
				continue // Messages' answer to a tool call, the turn's step
			}
			if len(c) == 0 || c[0] != '"' {
				if out != nil {
					break
				}
				return nil
			}
			t = c
		}
		sum := sha256.Sum256(joinBytes(`{"role":"user","content":`, t, `}`))
		out = append(out, "magpie-"+hex.EncodeToString(sum[:12]))
	}
	return out
}

// toolResultsOnly says content, a user message's, is a list of
// tool_result parts and nothing else: the Messages API's answer to a tool
// call, which a turn's later steps end on.
func toolResultsOnly(content []byte) bool {
	if len(content) == 0 || content[0] != '[' {
		return false
	}
	n, ok := 0, true
	eachElement(content, func(el []byte) bool {
		n++
		typed := false
		if len(el) > 0 && el[0] == '{' {
			eachMember(el, func(k, v []byte) bool {
				if keyIs(k, "type") {
					typed = string(v) == `"tool_result"`
					return false
				}
				return true
			})
		}
		ok = typed
		return ok
	})
	return ok && n > 0
}

// joinBytes is a, b and c in one slice: the spelling above is written as
// it goes, the request's own text bytes between them.
func joinBytes(a string, b []byte, c string) []byte {
	out := make([]byte, 0, len(a)+len(b)+len(c))
	out = append(out, a...)
	out = append(out, b...)
	return append(out, c...)
}

// writeOpening writes msg, one message of an opening of several
// (conversationOpening), the way their hash reads it: every object's
// members in key order with no space between, without a cache mark at any
// depth, and a content of one text part as that part's text. An agent
// marks its newest user message for the cache and moves the mark on to the
// next turn's, some spelling the message as a list of one marked part
// while it is the newest and as a plain string after (dsh does). Strings,
// numbers and the rest go as the request spells them, read in place.
func writeOpening(w *bufio.Writer, msg []byte) {
	msg = trimJSON(msg)
	switch {
	case len(msg) == 0:
	case msg[0] == '{':
		type member struct{ key, val []byte }
		var ms []member
		eachMember(msg, func(k, v []byte) bool {
			if !keyIs(k, "cache_control") {
				ms = append(ms, member{k, v})
			}
			return true
		})
		slices.SortStableFunc(ms, func(a, b member) int { return bytes.Compare(a.key, b.key) })
		w.WriteByte('{')
		for i, m := range ms {
			if i > 0 {
				w.WriteByte(',')
			}
			w.Write(m.key)
			w.WriteByte(':')
			if keyIs(m.key, "content") {
				if text, ok := oneTextPart(m.val); ok {
					m.val = text
				}
			}
			writeOpening(w, m.val)
		}
		w.WriteByte('}')
	case msg[0] == '[':
		w.WriteByte('[')
		n := 0
		eachElement(msg, func(el []byte) bool {
			if n > 0 {
				w.WriteByte(',')
			}
			n++
			writeOpening(w, el)
			return true
		})
		w.WriteByte(']')
	default:
		w.Write(msg)
	}
}

// oneTextPart is the text of content, a message's, when it is a list of
// one text part and nothing else beside a cache mark: the string literal
// as the request has it.
func oneTextPart(content []byte) ([]byte, bool) {
	content = trimJSON(content)
	if len(content) == 0 || content[0] != '[' {
		return nil, false
	}
	var part []byte
	n := 0
	eachElement(content, func(el []byte) bool {
		part, n = el, n+1
		return n < 2
	})
	if n != 1 || part[0] != '{' {
		return nil, false
	}
	var text []byte
	typed, ok := false, true
	eachMember(part, func(k, v []byte) bool {
		switch {
		case keyIs(k, "cache_control"):
		case keyIs(k, "type"):
			typed = string(v) == `"text"` || string(v) == `"input_text"`
			ok = typed
		case keyIs(k, "text") && v[0] == '"':
			text = v
		default:
			ok = false
		}
		return ok
	})
	return text, ok && typed && text != nil
}

// conversationOpening is how a conversation opens. items are its turns from
// its first user message on (gemini: Gemini's contents, where an omitted
// role is the user's). A first message that holds anything of the user's
// own (userOpens) opens it alone, and the id is what it always was. One the
// agent wrote alone opens it with the user messages after it, through the
// first that holds the user's own: some agents open every conversation on
// such a message, the same in every conversation started in one folder (an
// <environment_context> block, an instruction file), and send what the
// user typed after it. Hashed alone, every such conversation was one, kept
// on the account that answered the first and never moved on by rotate
// (keeyangyy, #3938). A system or developer message between them is the
// agent's instructions, passed over. A reply, a call, its output or a
// tool's result ends the opening; with nothing of the user's before it,
// the first message alone is the opening, as before. A history that starts
// with a tool's result was cut by its client: its opening is gone, and
// what follows the cut moves with it.
func conversationOpening(items []json.RawMessage, gemini bool) [][]byte {
	open := [][]byte{items[0]}
	for _, it := range items[1:] {
		var r struct {
			Role string `json:"role"`
		}
		if decodeJSONView(it, &r) != nil {
			break
		}
		if r.Role == "system" || r.Role == "developer" {
			continue
		}
		if r.Role != "user" && !(gemini && r.Role == "") {
			break
		}
		if len(open) == 1 {
			// read only once another user message follows: most
			// conversations open on one, which goes alone either way
			if theirs, result := userOpens(open[0]); theirs || result {
				break
			}
		}
		theirs, result := userOpens(it)
		if result {
			break
		}
		open = append(open, it)
		if theirs {
			return open
		}
	}
	return open[:1]
}

// userMedia are the content types of an image, a file or a recording a
// user sends in a message of theirs: Chat's, Messages' and Responses'.
var userMedia = []string{"image", "image_url", "input_image", "document", "file", "input_file", "input_audio"}

// userOpens is whether a user message, raw as its request has it, holds
// anything of the user's own: their words (userWords, which tells them
// from what the agent put in) or an image, a file or a recording. result
// is whether it holds a tool's result. One that can't be read is taken as
// the user's.
func userOpens(msg []byte) (theirs, result bool) {
	var m struct {
		Content json.RawMessage `json:"content"`
		Parts   json.RawMessage `json:"parts"` // Gemini's
	}
	if decodeJSONView(msg, &m) != nil {
		return true, false
	}
	var s string
	if decodeJSONView(m.Content, &s) == nil {
		return userWords(s) != "", false
	}
	for _, parts := range [][]json.RawMessage{elementsOf(m.Content), elementsOf(m.Parts)} {
		for _, p := range parts {
			var b struct {
				Type             string          `json:"type"`
				Text             string          `json:"text"`
				Thought          bool            `json:"thought"`
				InlineData       json.RawMessage `json:"inlineData"`
				FileData         json.RawMessage `json:"fileData"`
				FunctionResponse json.RawMessage `json:"functionResponse"`
			}
			if decodeJSONView(p, &b) != nil {
				continue
			}
			switch {
			case b.Type == "tool_result" || b.FunctionResponse != nil:
				result = true
			case b.InlineData != nil || b.FileData != nil || slices.Contains(userMedia, b.Type):
				theirs = true
			case b.Type == "text" || b.Type == "input_text" || b.Type == "" && !b.Thought:
				theirs = theirs || userWords(b.Text) != ""
			}
		}
	}
	return theirs, result
}

// offSpelled is a client's effort spelled "off", which no OpenAI API
// takes: a client asking reasoning off by that word (plugins#334, raw
// reasoning_effort "off" to a Command Code DeepSeek fell back to its lowest
// level and still thought).
func offSpelled(e string) bool { return strings.EqualFold(strings.TrimSpace(e), "off") }

// offAsNone is a Chat or Responses body asking reasoning "off"
// (reasoning_effort, or reasoning.effort) asking for magpie's own off
// level, none, instead: what every check and route reads the effort by,
// and what fitEffort fits to the model's own off level, or its least
// where it has none, as "none" asked is. A body with no such field goes
// as it came.
func offAsNone(from provider.Protocol, body []byte) []byte {
	if from != provider.Chat && from != provider.Responses || !saysOff(body) {
		return body
	}
	var v struct {
		ReasoningEffort *string                    `json:"reasoning_effort"`
		Reasoning       map[string]json.RawMessage `json:"reasoning"`
	}
	if decodeJSON(body, &v) != nil {
		return body
	}
	fields := map[string]any{}
	var e string
	if v.ReasoningEffort != nil && offSpelled(*v.ReasoningEffort) {
		fields["reasoning_effort"] = "none"
	}
	if decodeJSON(v.Reasoning["effort"], &e) == nil && offSpelled(e) {
		v.Reasoning["effort"] = json.RawMessage(`"none"`)
		fields["reasoning"] = v.Reasoning
	}
	if len(fields) == 0 {
		return body
	}
	return withFields(body, fields)
}

// saysOff is whether body has an effort field valued "off" anywhere,
// looked for without parsing it: serve looks at every request's body,
// however large, and only one naming off is parsed (offAsNone).
func saysOff(body []byte) bool {
	key := []byte(`effort"`)
	for rest := body; ; {
		i := bytes.Index(rest, key)
		if i < 0 {
			return false
		}
		rest = rest[i+len(key):]
		v := bytes.TrimLeft(rest, " \t\r\n")
		if v, ok := bytes.CutPrefix(v, []byte(":")); ok {
			v = bytes.TrimLeft(v, " \t\r\n")
			if s, ok := bytes.CutPrefix(v, []byte(`"`)); ok {
				if j := bytes.IndexByte(s, '"'); j >= 0 && offSpelled(string(s[:j])) {
					return true
				}
			}
		}
	}
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

// geminiLevels are the thinking levels model is asked at on Gemini's
// OpenAI-compatible API, less those it turned away before (turnedAway):
// its own, or Gemini 3's where they aren't known or none of its own is
// left.
func (s *Server) geminiLevels(p provider.Provider, model string, to provider.Protocol) []string {
	left := func(levels []string) []string {
		return slices.DeleteFunc(slices.Clone(levels), func(l string) bool {
			return s.turnedAway(p, model, l, to)
		})
	}
	if levels := left(p.Efforts(model)); len(levels) > 0 {
		return levels
	}
	return left(gemini3Levels)
}

// offRefused is how unfit remembers a provider refusing reasoning turned
// off for model.
func offRefused(model string) string { return "reasoning off\x00" + model }

// levelRefused is how unfit remembers Gemini turning away thinking level
// for model.
func levelRefused(model, level string) string { return "thinking level\x00" + model + "\x00" + level }

// turnedAway says model turned level away before: Gemini that level
// (levelRefused), or, for none and minimal, a provider refusing reasoning
// off as a whole (offRefused).
func (s *Server) turnedAway(p provider.Provider, model, level string, proto provider.Protocol) bool {
	return !s.fits(p.ID, levelRefused(model, level), proto) || offEffort(level) && !s.fits(p.ID, offRefused(model), proto)
}

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
var toolChoiceRefused = lazyre.New(`(?i)tool_choice\W+type\W+tool\W+and\W+any\W+are not supported|tools\.\d+\.custom\W+for\W+object\W+type\W+additionalProperties\W+must be explicitly set to false`)

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
var effortLevelsNamed = lazyre.New(`(?i)\blow\b\W+(?:medium|high)\b`)

// otherOffNamed is, for one level of reasoning off, the other named in an
// error: one refusing that level and listing the other among those taken
// (OpenAI's "'minimal' is not supported with the 'gpt-5.1' model.
// Supported values are: 'none', 'low', 'medium', and 'high'."). The level is
// matched quoted, as errors listing levels quote them (in JSON a double
// quote comes escaped), so a "None" in a sentence isn't it.
var otherOffNamed = map[string]*lazyre.RE{
	"none":    lazyre.New(`(?i)['"]minimal\\?['"]`),
	"minimal": noneQuoted,
}

// noneQuoted is reasoning effort none named in an error, quoted, as
// errors listing levels quote them (in JSON a double quote comes escaped).
var noneQuoted = lazyre.New(`(?i)['"]none\\?['"]`)

// bodyEffort is the reasoning effort a Chat or Responses request asks for,
// or an Anthropic one in its output_config, or a Gemini one in its
// generationConfig's thinkingConfig. A Responses request that
// changed it mid-thread asks for its last configuration_update's
// (effortUpdated).
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
	if decodeJSON(body, &v) != nil {
		return ""
	}
	switch {
	case proto == provider.Chat:
		return v.ReasoningEffort
	case proto == provider.Responses && v.Reasoning != nil:
		if e := effortUpdated(body); e != "" {
			return e
		}
		return v.Reasoning.Effort
	case proto == provider.Anthropic && v.OutputConfig != nil:
		return v.OutputConfig.Effort
	case proto == provider.Gemini:
		return geminiBodyEffort(body)
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
		if decodeJSON(body, &v) != nil || v.OutputConfig == nil {
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
		if decodeJSON(body, &v) != nil || v.Reasoning == nil {
			return body
		}
		if effortUpdated(body) != "" {
			// changed mid-thread: where the agent's last change went
			return withEffortUpdated(body, effort)
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
	if decodeJSON(body, &fields) != nil {
		return nil, false
	}
	changed := false
	var effort string
	if decodeJSON(fields["reasoning_effort"], &effort) == nil && strings.EqualFold(effort, "none") {
		delete(fields, "reasoning_effort")
		changed = true
	}
	var reasoning map[string]json.RawMessage
	if decodeJSON(fields["reasoning"], &reasoning) == nil && reasoning != nil {
		var enabled bool
		if decodeJSON(reasoning["enabled"], &enabled) == nil && !enabled {
			delete(reasoning, "enabled")
			changed = true
		}
		if decodeJSON(reasoning["effort"], &effort) == nil && strings.EqualFold(effort, "none") {
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

// keepsLoneNone is whether p is sent the lone tool_choice "none" a
// request that turned tools off with none declared keeps (#3110): only the
// Grok plugin, which reads it and leaves it out before Grok itself.
func keepsLoneNone(p provider.Provider) bool {
	return p.PluginProvider() == "grok" && !refusesLoneToolChoice(p)
}

// refusesLoneToolChoice is whether p's Responses API turns away a
// tool_choice with no tools beside it: xAI's, and Copilot's /responses in
// front of it for Grok (#378).
func refusesLoneToolChoice(p provider.Provider) bool {
	return p.VendorHost() == "api.x.ai" || p.Account != nil && p.Account.Agent == "copilot"
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
	if decodeJSON(body, &q) != nil || len(q.Tools) > 0 {
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
	if a != nil && !a.sent && a.compactsInStream(status, msg) {
		newSSEWriter(w).begin()
		a.sent = true
	}
	if a == nil || !a.sent {
		return writeError(w, proto, status, msg)
	}
	streamError(w, proto, a.model, status, msg)
	return status
}

// streamError writes an error as an event of the client's stream, its
// headers already sent. Wherever the protocol says a model with it (Chat's
// first chunk, Anthropic's message_start, Responses' response.created and
// the events after it), it is model, the one the agent asked for, none
// from cutLoop (#3792): with none, fx took Chat's first chunk for a broken
// one and stopped there (#3728).
func streamError(w http.ResponseWriter, proto provider.Protocol, model string, status int, msg string) {
	sw := newSSEWriter(w)
	sw.begun = true
	text := saysPassing(status, saysBusy(status, msg))
	ev := Event{Kind: KError, Text: text, Status: status}
	overflow := tooLong(status, msg)
	if overflow {
		ev.Code = "context_length_exceeded"
	} else if proto == provider.Responses && saidOverloaded(status, msg) {
		// The ChatGPT backend says its overload in a stream as
		// response.failed with server_is_overloaded (#1418), which Codex
		// reads as the model at capacity and stops on (codex-rs codex-api
		// sse/responses_error.rs). A busy status told once Codex has the
		// stream's headers went as server_error, which Codex retries as a
		// dropped stream: five more turns, each asked again by magpie,
		// "reconnecting 5/5" and the last error alone (wjh66 on Discord)
		ev.Code = "server_is_overloaded"
	}
	// A 413 before the headers go out is typed request_too_large
	// (writeErrorSaid). Once they have, Pi reads only this event's text
	// (#2372) and does not compact on "Request Entity Too Large". Gemini
	// keeps its own status shape.
	byteCap := status == http.StatusRequestEntityTooLarge && !overflow
	ownShape := proto == provider.Gemini
	alreadySaid := strings.Contains(strings.ToLower(text), "request_too_large")
	if byteCap && !ownShape && !alreadySaid {
		ev.Text = text + " (request_too_large)"
	}
	if byteCap && proto == provider.Responses {
		// The Responses API has one code that says the input must be
		// smaller. Codex reads only the code: server_error had it send
		// the same body again five times ("reconnecting 5/5"), refused
		// each time, while context_length_exceeded has it stop and
		// compact, its compaction trimming the oldest items for as long
		// as that is refused too (codex-rs codex-api
		// sse/responses_error.rs, core compact.rs; celestial_eagle_99230
		// on Discord)
		ev.Code = "context_length_exceeded"
	}
	makeEncoder(proto, sw, &Request{Model: model}).event(ev)
}

// failOverloaded is how the Routing page words a failure told in a
// later try's place (Route.Fail) that was the vendor too busy for the
// request (saidOverloaded): a word for the row only. Routing reads such a
// failure as failOther, which rests the account a moment at most
// (vendorOverloaded); it is never a rest's Why.
const failOverloaded = "overloaded"

// toldFail says why the failure o, told the agent in a later try's
// place, failed, as the Routing page words it: the vendor overloaded
// (danzi002 on Discord: ChatGPT's 529 server_is_overloaded, on an
// account with its allowance left, read as "quota used up" beside the
// last account's usage limit), the agent's prompt turned away, or as
// failure reads its status and words.
func toldFail(o *Try) string {
	switch {
	case o.errType == turnedAwayErrType:
		return failPrompt
	case saidOverloaded(o.Status, string(wordsOf([]byte(o.Error)))):
		return failOverloaded
	}
	return failure(o.Status, []byte(o.Error))
}

// saidOverloaded says a failure is the vendor too busy for the request:
// a 529, or a 5xx whose words say overloaded.
func saidOverloaded(status int, msg string) bool {
	return status == 529 || (status >= 500 && strings.Contains(strings.ToLower(msg), "overloaded"))
}

// passingSaid matches the words an agent that reads only an error's text
// takes for a failure worth asking again (Pi's isRetryableError, #2372):
// one of the usual statuses, a timeout, a rate limit, a busy or
// unreachable server.
var passingSaid = lazyre.New(`(?i)\b(429|500|502|503|504)\b|timed? ?out|rate.?limit|too many requests|overloaded|service.?unavailable|server.?error|internal.?error|connection.?(error|refused|reset)|retry your request|try your request again`)

// saysPassing is msg, an error sent in a stream whose status the agent
// can no longer be told, with the status said in it when it is one a
// moment may clear (408, 429, 5xx) and msg says so in no words an agent
// knows: "Upstream service temporarily unavailable" and "no first token in
// 30s" reached Pi, which judges by the text alone, as errors not worth
// asking again, while Claude Code and Codex, told the same as a status,
// asked again and were answered (#2372).
func saysPassing(status int, msg string) string {
	if status != http.StatusRequestTimeout && status != http.StatusTooManyRequests && status < 500 || passingSaid.MatchString(msg) {
		return msg
	}
	return fmt.Sprintf("%s (%d: you can retry your request)", msg, status)
}

// busyWords are how a relay says, under a request-error status, that it
// was busy for a moment: "Concurrency limit exceeded for account, please
// retry later" and "Too many pending requests, please retry later" in a
// 400, "The service is temporarily unavailable. Please retry later." with
// no status at all (#2372, the texts zminds' Pi sessions recorded).
var busyWords = lazyre.New(`(?i)concurrency limit|too many (pending|concurrent|in-?flight) requests|temporarily unavailable|(please )?(retry|try again) later|并发|稍后(再)?重?试`)

// spentWords say an account is out of what it pays for, which no retry
// clears: Pi's own deny-list keeps such a text from being asked again, and
// magpie doesn't tell it otherwise.
var spentWords = lazyre.New(`(?i)quota|insufficient|balance|credit|billing|budget|usage.?limit|余额|额度|欠费|套餐|用量`)

// saysBusy is msg, a request error's text (400, 409, 422), with "you can
// retry your request" said in it when its words say only that the relay
// was busy: Pi, which judges an error by its text alone, ended the turn
// on each, where a moment later the same request was answered (#2372).
// A request's own fault, an account out of credit and a conversation too
// long are left as they were said.
func saysBusy(status int, msg string) string {
	switch status {
	case http.StatusBadRequest, http.StatusConflict, http.StatusUnprocessableEntity:
	default:
		return msg
	}
	if !busyWords.MatchString(msg) || spentWords.MatchString(msg) || tooLong(status, msg) || passingSaid.MatchString(msg) {
		return msg
	}
	return msg + " (busy: you can retry your request)"
}

// writeError answers in the client's own error shape.
func writeError(w http.ResponseWriter, proto provider.Protocol, status int, msg string) int {
	return writeErrorSaid(w, proto, status, msg, nil, nil)
}

// writeVendorError answers a vendor's error status in the client's own
// shape, as writeError does, keeping the code and param the vendor named
// (an OpenAI-shaped error's strings) for a client of OpenAI's shape: the
// ChatGPT backend's invalid_access_program on access_programs.cyber
// (magpie-releases#18) reaches the agent as the backend said it.
func writeVendorError(w http.ResponseWriter, proto provider.Protocol, status int, msg string, body []byte) int {
	var code, param any
	if e := gjson.GetBytes(body, "error"); e.IsObject() {
		if c := e.Get("code"); c.Type == gjson.String && c.String() != "" {
			code = c.String()
		}
		if p := e.Get("param"); p.Type == gjson.String && p.String() != "" {
			param = p.String()
		}
	}
	return writeErrorSaid(w, proto, status, msg, code, param)
}

// requestBytesHint is what a 413 is told: a cap on the request's bytes.
const requestBytesHint = "This is a request-body byte limit, not the model's context: compact the conversation (/compact in Codex or Claude Code), or start a new one with a short summary; fewer images or smaller tool results also help."

// writeErrorSaid is writeError with the code and param an OpenAI-shaped
// answer names, nil for none.
func writeErrorSaid(w http.ResponseWriter, proto provider.Protocol, status int, msg string, code, param any) int {
	typ := errorTypeFor(status)
	// a relay busy for a moment, said so Pi asks again (#2372)
	msg = saysBusy(status, msg)
	if tooLong(status, msg) {
		// said the way the client's own API says it, so the agent
		// compacts the conversation and tries again rather than stopping
		status, typ, code = 400, "invalid_request_error", "context_length_exceeded"
		if proto == provider.Anthropic && !strings.Contains(strings.ToLower(msg), "prompt is too long") {
			msg = "prompt is too long: " + msg
		}
	}
	if h, held := w.(*holdWriter); status == http.StatusRequestEntityTooLarge && (!held || !h.hold) {
		if !strings.Contains(msg, requestBytesHint) {
			msg += " — " + requestBytesHint
		}
	}
	return writeErrorAs(w, proto, status, typ, msg, code, param)
}

// errorTypeFor is the error type an OpenAI- or Anthropic-shaped answer
// names for status.
func errorTypeFor(status int) string {
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
	case status == 413:
		// Anthropic's own type, in every shape: pi (overflow.ts) compacts
		// and tries once more on it, and an OpenAI-shaped api_error left
		// it stopped (@wilsonxuuuu on X)
		typ = "request_too_large"
	case status == 429:
		typ = "rate_limit_error"
	case status == 529:
		typ = "overloaded_error"
	}
	return typ
}

// writeErrorAs writes an error in proto's shape, as said: nothing in it
// is read again or changed.
func writeErrorAs(w http.ResponseWriter, proto provider.Protocol, status int, typ, msg string, code, param any) int {
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
		v = map[string]any{"error": map[string]any{"message": msg, "type": typ, "code": code, "param": param}}
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
// "longer than the model's context length" or, without the 's, "longer
// than the model context length" (#4040, easyrev), llama.cpp's "available
// context size", LM Studio's "greater than the context length", Copilot's
// "prompt token count of", MiniMax's "context window exceeds limit",
// Kimi's "exceeded model token limit", DS4's "configured context size",
// z.ai's "Prompt too long" / "Prompt exceeds max length" and
// model_context_window_exceeded, Ollama's "exceeded max context length",
// DashScope's "Range of input length should be", "token limit exceeded",
// a relay's own cap on one request's input, "Input token exceed the limit"
// (#2445: B.ai, under the model's window, which another member holds).
// "Context window is full" is how Kiro-backed relays (kiro.rs,
// kiro2claude) say Kiro's CONTENT_LENGTH_EXCEEDS_THRESHOLD, a 400
// (ergou0954_54426 on Discord, through a new-api relay).
// The third line is Chinese that names the input rather than the context
// (#4420, MYCF711): a group member's 400 "内容太长了，请精简输入或开启新对话",
// z.ai's code 1261 "Prompt 超长", iFlytek Spark's 10907 "token数量超过上限。
// 对话历史+问题的字数太多，需要精简输入", and "输入长度超过限制" /
// "输入内容超过模型最大token数". Each names what was sent (内容, 输入,
// 提示词, prompt…) beside its length, so "名称超出最大长度" (a field) or
// "输出过长" (the reply) isn't taken; bare "token数量超过上限" isn't either,
// as a day's allowance is said so too.
var tooLongRe = lazyre.New(`(?i)context_length_exceeded|prompt is too long|input is too long|(exceeds?|exceeded|over|beyond)( the)?( model'?s?)?( maximum)? context|context (length|limit|window) (exceeded|is exceeded)|context (window )?(is )?full\b|maximum context length|too many (input |prompt )?tokens|上下文(长度)?(超|过长)|超(过|出)(了)?(模型)?(的)?(最大)?上下文` +
	`|input token count.*exceeds the maximum|maximum prompt length is \d|reduce the length of the messages|maximum allowed input length|longer than the model'?s? context length|available context size|greater than the context length|prompt token count of [\d,]+ exceeds the limit|context window exceeds limit|exceeded model token limit|configured context size|prompt too long|prompt exceeds max length|context_window_exceeded|exceeded (max |maximum )?context length|range of input length should be|token limit exceeded|input tokens? (count )?(exceeds?|exceeded) (the )?(max(imum)? )?limit` +
	`|(内容|输入|文本|提示词?|prompt|问题|对话|消息)(内容|文本)?(长度)?\s*(太长|过长|超长)|字数(太多|过多)` +
	`|(内容|输入|文本|提示词?|prompt|问题|对话|消息)[^，。,.;；!！]{0,12}超(过|出)[^，。,.;；!！]{0,12}(长度|token|令牌|字数)` +
	`|(输入|提示词?|prompt)\s*的?\s*(长度|token|令牌|字数)[^，。,.;；!！]{0,8}超(过|出)`)

// overflowed says a refused request was too long for the model — a
// request error (400, 413, 422) whose words say so.
func overflowed(status int, body []byte) bool {
	// the words are matched decoded, too (#1669): 上下文 escaped as \uXXXX
	return (status == 400 || status == 413 || status == 422) && tooLong(status, string(wordsOf(body)))
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

// paceWords say a limit on how fast or how much, not on one prompt's size,
// in Chinese too (#4420): "内容太长，已达到速率限制" is the momentary cap.
var paceWords = lazyre.New(`(?i)rate.?limit|per (minute|hour|day)|\bTP[MD]\b|please wait|try again later|throttl|速率|频繁|频率|限流|并发|每(秒|分钟|小时|天|日)|稍后(再试|重试)`)

// splitSent tells what of sent, the ms from the request (start) to its
// answering try's body written, was magpie's own work, the tries that
// failed first with their pauses (from the first try's start to this
// one's), and this try's wait for a slot (queued). asked is when the
// request that went out began to (GetConn, UnixNano; 0 when unknown):
// after it is connecting and sending the body, which is neither. #1532:
// OpenCode Go held a try ~100 s before its 503, and the detail called the
// whole of it magpie's.
func splitSent(sent int64, start, firstBegan, began time.Time, asked, queued int64) (own, retried, waited int64) {
	retried = min(max(began.Sub(firstBegan).Milliseconds(), 0), sent)
	waited = min(max(queued, 0), sent-retried)
	own = sent - retried - waited
	if asked > 0 {
		// before the first try, and in this one up to its going out
		before := firstBegan.Sub(start) + time.Unix(0, asked).Sub(began)
		own = min(max(before.Milliseconds()-waited, 0), own)
	}
	if own < 1 {
		// under a ms, still known: a record with none has no split
		own = 1
	}
	return own, retried, waited
}
