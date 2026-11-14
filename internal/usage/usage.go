// Package usage keeps the token count of every call the gateway serves, so
// magpie can show what each agent and model consumed and roughly what it cost.
// Records go to one JSON-lines file next to providers.json. Metadata leaves
// the machine only when OTLP export is explicitly enabled.
package usage

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// Record is one call.
type Record struct {
	Operation       string    `json:"operation,omitempty"`
	RouteID         int64     `json:"route_id,omitempty"` // the gateway Route, shared by its attempts
	Time            time.Time `json:"t"`
	Agent           string    `json:"agent"` // magpie agent id, or the client's product name
	Provider        string    `json:"provider"`
	Host            string    `json:"host,omitempty"`          // where the call went: provider.Where then
	ProviderKeyID   string    `json:"providerKeyId,omitempty"` // fingerprint of the API key actually used
	ProviderKeyName string    `json:"providerKeyName,omitempty"`
	// ProviderAccount is the subscription account that answered the call,
	// as the Routing trace names it (provider.Account.User: an email, a
	// login): the one that took over after a failover, the one pinned by
	// X-Magpie-Account. Never a token. "" for a key or a provider without
	// an account, and in a record written before it was kept (#557).
	ProviderAccount string `json:"providerAccount,omitempty"`
	CallerKeyID     string `json:"callerKeyId,omitempty"`
	CallerKeyName   string `json:"callerKeyName,omitempty"`
	// CallerIssuerID is the key CallerKeyID was issued under (an issued,
	// short-lived key: access.IssueKey), whose limits count this call too.
	CallerIssuerID string `json:"callerIssuerId,omitempty"`
	// SessionProvider is the provider ID recorded by the agent. SessionAccount
	// identifies the session's creator, not the account used for an API request.
	// Neither field establishes an upstream route from today's configuration.
	SessionProvider string `json:"session_provider,omitempty"`
	SessionAccount  string `json:"session_account,omitempty"`
	// SessionOfficialLogin marks a matched account's explicit official login
	// metadata, not this call's endpoint or billing authentication.
	SessionOfficialLogin bool   `json:"session_official_login,omitempty"`
	Model                string `json:"model"` // the provider's model id
	// Requested is the model id the agent asked for (a magpie alias, a
	// routing group, provider/model…), and Served the model the vendor's
	// reply says answered, when it named one: a ledger to set beside the
	// vendor's own bill. Neither is in a record written before they were.
	Requested string `json:"req,omitempty"`
	Served    string `json:"served,omitempty"`
	// Upstream: the provider an aggregator (OpenRouter …) said answered
	// behind it — DeepInfra, Novita — when its reply named one
	Upstream string `json:"upstream,omitempty"`
	Input    int    `json:"in"`
	Output   int    `json:"out"`
	// CacheRead is the prompt tokens read from the vendor's cache, and
	// CacheWrite those written to it (Anthropic's cache creation,
	// cache_creation_input_tokens); neither is in Input. Both are written
	// on every record, 0 too, as in and out are, so a call whose prompt
	// missed the cache reads as that rather than as one whose cache wasn't
	// kept (#1839). A record from before they were has neither: 0.
	CacheRead  int `json:"cache_read"`
	CacheWrite int `json:"cache_write"`
	// CacheWrite1h is how many of CacheWrite were written to be kept for
	// an hour (Anthropic's 1-hour TTL, billed at 2× input where 5 minutes'
	// is 1.25×); the rest were for 5 minutes. A record from before it was
	// kept has none: all its writes are counted as 5-minute ones, as they
	// were.
	CacheWrite1h int `json:"cache_write_1h,omitempty"`
	// CacheUnknown: the vendor counted no cache tokens for this call (a
	// plugin's magpie_cache_unknown: Kiro caches but tells only credits),
	// so its CacheRead of 0 is not a miss. A hit rate leaves its prompt
	// out (Totals.CacheUnknown, #3464). A record from before it was kept
	// has none: its 0 still reads as a miss.
	CacheUnknown bool `json:"cache_unknown,omitempty"`
	Reasoning    int  `json:"reasoning,omitempty"`
	// Effort is the reasoning the model was asked for — a routing group's
	// pick for the turn, or the agent's own — as it takes it; "" for none
	Effort string `json:"effort,omitempty"`
	// The service tier, as OpenAI's service_tier words it ("priority",
	// "ultrafast", "flex", "default"; "fast" for Claude's speed "fast"):
	// TierAsked what the client's request asked for, TierSent what went
	// out to the vendor on this try, after a fast pick or a group member
	// set Fast asked for priority and an account whose plan doesn't offer
	// Ultrafast was asked for priority in its place, and TierServed what
	// the vendor's reply said it ran at. Each is "" when there was none:
	// TierServed is never taken from what was asked or sent, only from a
	// reply that names it (#1784)
	TierAsked  string `json:"tier_asked,omitempty"`
	TierSent   string `json:"tier_sent,omitempty"`
	TierServed string `json:"tier_served,omitempty"`
	Millis     int64  `json:"ms"`
	// TTFT: ms from the request to its reply's first content — text,
	// reasoning or a tool call — and FirstText to its first text, counted
	// as Millis is, so Millis-TTFT is how long the reply took to write;
	// none for a reply that wasn't streamed (#196)
	TTFT      int64 `json:"ttft_ms,omitempty"`
	FirstText int64 `json:"first_text_ms,omitempty"`
	// Compaction is Codex's explicit native marker. Nil means no matching
	// native Codex call established whether a gateway row compacted.
	Compaction *bool `json:"compaction,omitempty"`
	// FirstReasoning is the start of paced visible reasoning, absent for hidden reasoning.
	FirstReasoning int64 `json:"first_reasoning_ms,omitempty"`
	// ReasoningHidden: the reply reasoned and its stream doesn't tell how
	// much — sealed or summarised, or streamed with no reasoning count in
	// its usage (#4545: Anthropic's usage never has one), when Reasoning is
	// 0 for unknown, not for none. A reply of no text then tells no speed
	// (Decode).
	ReasoningHidden bool `json:"reasoning_hidden,omitempty"`
	// Sent: ms from the request to its answering try going out to the
	// vendor, its body written, as TTFT is counted: magpie's own time and
	// the tries that failed first are before it, and TTFT-Sent is how
	// long the vendor took to its first content. 0 where it isn't known
	// (a reply not streamed, or a vendor magpie doesn't reach over HTTP).
	Sent int64 `json:"sent_ms,omitempty"`
	// Of Sent, what was magpie's: Own is magpie's own work on the request
	// (reading it, translating it, choosing whom to ask), Retried the
	// tries that failed first and the pauses after them, Queued the wait
	// for a slot of a key's or account's MaxConcurrency or MaxRPM; the
	// rest, Sent-Own-Retried-Queued, is connecting to the vendor and
	// sending it the body. #1532: a vendor that held a try ~100 s and
	// said 503 read as 104 s "In magpie". 0 in a record written before
	// they were, where Sent is all that is known.
	Own int64 `json:"own_ms,omitempty"`
	// Masking: the ms of Own that went on masking the request (Settings
	// → Privacy: the rules, and the local model with its wait for a
	// turn), all of it where Own is unknown; 0 where nothing was masked,
	// or in a record from before it was kept.
	Masking int64 `json:"masking_ms,omitempty"`
	Retried int64 `json:"retried_ms,omitempty"`
	Queued  int64 `json:"queued_ms,omitempty"`
	// Flow: the ms the reply's content took to come, from where its speed
	// is counted (DecodeOf): from a tenth of its content to nine tenths,
	// spread over the whole. A reply a vendor held and let go in one burst
	// has a flow of next to none, however long its end came after its
	// first content, and so tells no speed (John on Discord: a Kimi Code
	// reply read 1,367 tok/s). 0 where it isn't known.
	Flow   int64 `json:"flow_ms,omitempty"`
	Status int   `json:"status"`
	// Error is why a call failed, in the vendor's words and cut short;
	// ErrType what its body called the error (rate_limit_error,
	// usage_limit_reached); RequestID the id the vendor gave the call; and
	// Endpoint the path the agent called, with the one it was sent to when
	// it went out in another protocol (/v1/messages → /v1/chat/completions)
	Error     string `json:"err,omitempty"`
	ErrType   string `json:"err_type,omitempty"`
	RequestID string `json:"rid,omitempty"`
	// ResponseID is the response object ID actually sent to the client.
	ResponseID string `json:"response_id,omitempty"`
	// MessageID is the id the agent's own session file gives the call's
	// message (Claude Code's msg_…), on a call read from such a file. Not
	// persisted: it is read back with the file, and told by the request
	// feed (FeedOriginals).
	MessageID string `json:"-"`
	Endpoint  string `json:"ep,omitempty"`
	// Stop is why the upstream said its reply ended, in its own words
	// (stop_reason, finish_reason …): "" when it said none, a stream that
	// just stopped. Told apart by it, a reply that ended too soon is the
	// upstream's own end_turn or a cut (蓝猫 on Discord).
	Stop string `json:"stop,omitempty"`
	// Session is the conversation the call was part of, as its agent names
	// it (X-Magpie-Session, or the session header Claude Code, Codex or
	// OpenCode sends): several sessions on one model told apart
	Session string `json:"session,omitempty"`
	// NativeSession retains the client header when X-Magpie-Session overrides it.
	NativeSession string `json:"native_session,omitempty"`
	// Subagent is the subagent that made the call, as its agent names it
	// (Claude Code's x-claude-code-agent-id, the agentId of its
	// subagents/agent-<id>.jsonl): Session stays the conversation it was
	// spawned in, so its calls are counted there too. ParentAgent is the
	// subagent that spawned it in turn (x-claude-code-parent-agent-id), ""
	// when the conversation itself did or the agent doesn't say.
	Subagent    string `json:"subagent,omitempty"`
	ParentAgent string `json:"parent_agent,omitempty"`
	// Rejected is a request refused locally before an upstream was contacted.
	Rejected bool `json:"rejected,omitempty"`
	// Tries is how many tries the gateway made at the request this row
	// ends, as its route lists them, when there was more than one: a
	// request answered on a later try was answered, not failed (小一 on
	// Discord). HandedOn marks the row of one try that failed and handed
	// the request on to the next (a vendor's refusal kept for what it
	// billed): it is not the request's outcome, which a later row with
	// the same RouteID is, so it is neither a request nor a failure of
	// its own. Neither is in a record written before they were; the log
	// index tells HandedOn from such a record's route (handedOn).
	Tries    int  `json:"tries,omitempty"`
	HandedOn bool `json:"handed_on,omitempty"`
	// UpBytes is how many bytes of this call's request actually went up to
	// the vendor on the wire, its tries summed: the plain body where it
	// goes as read, less where it went compressed (zstd to a ChatGPT
	// backend, provider.CodexCompressed). A try gone over a connection
	// that then failed still counts, as a connection's own counter would,
	// and so does a single ask's second request (RetryOneAuth …). A
	// record from before it was kept has none (#4456).
	UpBytes int `json:"up_bytes,omitempty"`
	// PlainBytes is the request's last try as it went into that ask,
	// before compression, what UpBytes would have been without one: beside
	// it, a compressed turn's discount and a retried request's re-sends
	// both read off the row. None when no try went out.
	PlainBytes int `json:"plain_bytes,omitempty"`
	// DownBytes is how many bytes of the vendor's replies to this call's
	// tries magpie read, tries summed as UpBytes is: a reply's body as it
	// came, after the HTTP client undid a gzip the vendor sent it in, so
	// more than the wire's for a gzipped reply. A record from before it was
	// kept has none.
	DownBytes int `json:"down_bytes,omitempty"`
	// ImageFilesSaved is how many fewer bytes went up because DeepSeek's
	// image files named the request's inline images by file id (#5407):
	// the inline base64 its tries left out, less the images this request
	// uploaded for it. Below 0 when an upload cost more than it saved this
	// time. None where no image went by file.
	ImageFilesSaved int `json:"image_files_saved,omitempty"`
	// ImageFiles is how many of the request's inline images went by file
	// instead, the most of any one try (a retry sends the same ones again):
	// what ImageFilesSaved was saved on (matrix0487 on Discord). None in a
	// row from before it was kept.
	ImageFiles int `json:"image_files,omitempty"`
	// UpParts is what UpBytes was made of, where the body was read to
	// know (UpParts); nil where it wasn't: a socket's delta, a request
	// whose images went by file, a row from before it was kept
	// (theforeveriris on X: 8.6 GB up from plain-text projects).
	UpParts *UpParts `json:"up_parts,omitempty"`
	// Socket says the request went over the Responses WebSocket, its
	// last try (provider.Socket, matrix0487 on Discord): UpBytes is then
	// what the socket was sent, a delta on a kept socket.
	Socket bool `json:"ws,omitempty"`
	// Kind is what the agent made the call for when it isn't a turn of
	// the conversation: a Codex subagent's (review, compact, guardian…)
	Kind string `json:"kind,omitempty"`
	// Via is the computer whose magpie passed the call on to this one (a
	// Remote magpie provider there), Agent being the agent's on it; "" when
	// no other magpie forwarded it (including direct LAN/container clients).
	Via string `json:"via,omitempty"`
	// Archive is where the request archive keeps the call, "<date>/<id>"
	// (gateway/archive.go), when it was on: the Usage page reads it back
	// by it long after Recent calls has let the call go (#447)
	Archive string `json:"archive,omitempty"`
	// Computer is the other computer the call was made on, by its id, for
	// a call brought here by sync (#542); "" for one made here, as every
	// call in usage.jsonl is
	Computer string `json:"computer,omitempty"`
	// Source is SourceReported for a call a client told magpie of through
	// POST /v1/magpie/usage/requests (#1874), one that went straight to its
	// vendor and that magpie never saw; "" for the gateway's own. Reported
	// calls count in the totals and are told apart, and filtered, on the
	// page.
	Source string `json:"source,omitempty"`
	// OwnCost is what such a client said the call cost, in USD. nil when it
	// said nothing: the call is then priced at magpie's own prices, as the
	// gateway's calls are, and priced again when those change.
	OwnCost *float64 `json:"cost,omitempty"`
	// Credits is what the vendor said the call cost in its plan's own
	// credits (WorkBuddy's usage.credit, #2512), nil when it said none
	Credits *float64 `json:"credits,omitempty"`
	// Watched is a row of a request an Observer watched (observe.go): it
	// has the request's attempts already, so it can pass the row over.
	// Never persisted.
	Watched bool `json:"-"`
	// ClientIP is the address the request came to the gateway from, as the
	// connection gave it (the gateway trusts no forwarding header, so behind
	// a proxy it is the proxy's): a loopback address for this computer. It
	// stays on this computer: sync (SharedCalls) and the gateway's
	// /v1/magpie/usage/requests leave it out. "" for a row kept before it
	// was recorded, a call a client reported, or magpie's own.
	ClientIP string `json:"client_ip,omitempty"`
	// Local is true only for verified loopback gateway requests. It is used
	// for local-session deduplication, never persisted or exported.
	Local bool `json:"-"`
	// BodyIn and BodyOut are the request and reply as the gateway
	// captured them, filled only while an Observer wants them (an OTLP
	// export with bodies on, #538) and never written to usage.jsonl.
	BodyIn  string `json:"-"`
	BodyOut string `json:"-"`
	// pricedAs is the provider the call is priced as when it is shown
	// under another: a deleted provider's merged into one on the Usage
	// page, which costs what it did (provider.Renames.Both). Never kept.
	pricedAs string
}

// renameBy has r's provider the one its calls are shown under now
// (Renames.Of), and keeps the one it is priced as beside it when they
// differ.
func (r *Record) renameBy(rn provider.Renames) {
	shown, own := rn.Both(r.Provider, r.Time)
	r.Provider, r.pricedAs = shown, ""
	if own != shown {
		r.pricedAs = own
	}
}

// priceID is the provider r is priced as: its own, not one it is shown
// under after a merge (renameBy).
func (r Record) priceID() string {
	if r.pricedAs != "" {
		return r.pricedAs
	}
	return r.Provider
}

// Path is the log file: ~/.config/magpie/usage.jsonl (XDG-aware).
func Path() string { return filepath.Join(filepath.Dir(provider.Path()), "usage.jsonl") }

var mu sync.Mutex

// loads counts Load's reads of the whole log, for the tests: the ledger
// reads the gateway's calls from its index instead.
var loads int

// Append writes one record. Errors are swallowed: accounting must never
// break a call.
func Append(r Record) {
	if r.Time.IsZero() {
		r.Time = Clock()
	}
	if !r.IsProbe() { // magpie's own test is no call to export
		observeLedger(r)
	}
	defer codexUsedNew(r)
	defer spentNew(r)
	b, err := json.Marshal(r)
	if err != nil {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	path := Path()
	f, before := openLog(path)
	if f == nil {
		return
	}
	if !holdLog {
		defer f.Close()
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		if holdLog {
			f.Close()
			heldLog.f = nil
		}
		return
	}
	after, _ := statLogHandle(f)
	noteLogAppend(path, before, after)
}

// heldLog is the log Append keeps open between calls, under mu: every
// request appends, and an open and a close of usage.jsonl each time was
// most of Append's time (#3725). It is written to only while Path still
// names that same file, so a log moved, removed or replaced is opened
// again. Windows closes it after each write, since a file open there
// can't be removed or renamed.
var heldLog struct {
	path string
	f    *os.File
}

const holdLog = runtime.GOOS != "windows"

// openLog returns the log to append to and its state before the write,
// or nil when it can't be opened.
func openLog(path string) (*os.File, os.FileInfo) {
	if f := heldLog.f; f != nil {
		heldLog.f = nil
		if heldLog.path == path {
			before, err := statLogHandle(f)
			now, nerr := statLogFile(path)
			if err == nil && nerr == nil && sameLogFile(before, now) {
				heldLog.f = f
				return f, before
			}
		}
		f.Close()
	}
	// the folder is made only when it is missing: every request appends,
	// and MkdirAll stat'ed it each time (#3725)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if errors.Is(err, fs.ErrNotExist) && os.MkdirAll(filepath.Dir(path), 0o755) == nil {
		f, err = os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	}
	if err != nil {
		return nil, nil
	}
	before, _ := statLogHandle(f)
	if holdLog {
		heldLog.path, heldLog.f = path, f
	}
	return f, before
}

// IsRejected also recognizes local rejections written before the explicit flag.
func (r Record) IsRejected() bool { return r.Rejected || r.Provider == "" && r.Status >= 400 }

// IsReported is a call an app reported itself (#1874), which never went
// through magpie. It is shown and counted on the Usage page only: nothing
// that acts on live traffic (a key's budget, a window's holds, the Sessions
// page's gateway sessions, what a session was routed to, Codex's sealed
// answers or remembered model) reads it, since it may be a backfill of
// calls long past and none of them were magpie's to route.
func (r Record) IsReported() bool { return r.Source == SourceReported }

// Load streams complete records from the log. Historical request bodies are
// not retained by a global cache after the caller finishes with this snapshot.
func Load(since time.Time) []Record {
	mu.Lock()
	loads++
	f, err := os.Open(Path())
	mu.Unlock()
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []Record
	reader := bufio.NewReaderSize(f, 64<<10)
	for {
		b, err := reader.ReadBytes('\n')
		if err != nil {
			break
		} // an unfinished line is left for the next read
		var r Record
		if json.Unmarshal(b, &r) == nil && !r.IsProbe() && (since.IsZero() || !r.Time.Before(since)) {
			out = append(out, r)
		}
	}
	return out
}

// Known is an agent as its requests name it.
type Known struct {
	ID    string
	Names []string // its id and aliases, which a record may already carry
	UA    []string // what its User-Agent begins with
}

// Agents lists the agents magpie knows. Package agent sets it from each
// agent's own description, so they are listed in one place only.
var Agents func() []Known

// PluginAgents lists the agents plugins add, which come and go as
// plugins do; package agent sets it.
var PluginAgents func() []Known

var builtinAgents = sync.OnceValue(func() []Known {
	if Agents == nil {
		return nil
	}
	return Agents()
})

// knownAgents are magpie's own agents, then those plugins add.
func knownAgents() []Known {
	own := builtinAgents()
	if PluginAgents == nil {
		return own
	}
	more := PluginAgents()
	if len(more) == 0 {
		return own
	}
	return append(slices.Clip(own), more...)
}

// IsAgent says whether id is an agent magpie knows, its own or a plugin's,
// rather than the product name of some other client (Python-urllib).
func IsAgent(id string) bool {
	return slices.ContainsFunc(knownAgents(), func(k Known) bool { return k.ID == id || slices.Contains(k.Names, id) })
}

// libraries are the product names of HTTP libraries, runtimes and API
// SDKs as AgentOf names a client by its User-Agent, lower-cased. A
// request that says only one of these is from a script, or from a tool
// built on the library, and says nothing of which: it is no agent of its
// own (theforeveriris on X: Bun, curl and python-httpx each had an
// agent's scorecard on the Context tab).
var libraries = map[string]bool{
	// command lines and API tools
	"curl": true, "wget": true, "httpie": true, "postmanruntime": true, "insomnia": true,
	// JavaScript
	"bun": true, "node": true, "deno": true, "node-fetch": true, "undici": true, "axios": true, "got": true, "ky": true,
	// Python
	"python": true, "python-httpx": true, "httpx": true, "python-requests": true, "requests": true,
	"python-urllib": true, "urllib3": true, "aiohttp": true,
	// Go, JVM, Ruby, PHP, Dart, Rust
	"go-http-client": true, "go-resty": true, "resty": true, "okhttp": true, "java": true, "java-http-client": true,
	"apache-httpclient": true, "reactor-netty": true, "ruby": true, "faraday": true, "rest-client": true,
	"guzzlehttp": true, "dart": true, "reqwest": true, "libcurl": true,
	// the vendors' SDKs, which an app or a script calls a model with
	"openai": true, "asyncopenai": true, "anthropic": true, "asyncanthropic": true, "ai-sdk": true, "google-genai-sdk": true,
}

// IsLibrary says whether a client, by the name AgentOf gives it (a
// record's Agent, old records' too), is an HTTP library, a runtime or an
// API SDK rather than an agent: curl, Bun, python-httpx, Go-http-client,
// OpenAI/Python.
func IsLibrary(id string) bool { return libraries[strings.ToLower(strings.TrimSpace(id))] }

// AgentOf names the agent behind a client User-Agent. Known agents map to
// their magpie id; anything else keeps its product name.
func AgentOf(ua string) string {
	ua = strings.TrimSpace(ua)
	name, _, _ := strings.Cut(ua, "/")
	name, _, _ = strings.Cut(name, " ")
	l := strings.ToLower(name)
	// a name first: cursor-local is its own, though Cursor's UA (cursor)
	// begins it
	for _, k := range knownAgents() {
		if slices.Contains(k.Names, l) {
			return k.ID
		}
	}
	for _, k := range knownAgents() {
		if slices.ContainsFunc(k.UA, func(p string) bool { return strings.HasPrefix(l, p) }) {
			return k.ID
		}
	}
	if name == "" {
		return "other"
	}
	return name
}

// ---- summaries --------------------------------------------------------------

// Period is a window of time to sum over.
type Period string

const (
	Today Period = "today"
	Week  Period = "7d"
	Month Period = "30d"
	All   Period = "all"
)

// Periods lists the windows in order.
var Periods = []Period{Today, Week, Month, All}

// CostAt is what the call costs at a price: at the tier its input reaches,
// its 1-hour cache writes at theirs, and at the hour it was made (an
// off-peak price doubles at peak, Price.In). A reported call that said
// what it cost costs that, at any price.
func (r Record) CostAt(p catalog.Price) float64 {
	if r.OwnCost != nil {
		return *r.OwnCost // a reported call's own cost (#1874)
	}
	return p.In(r.Time).CostSplit(r.Input, r.Output, r.CacheRead, r.CacheWrite, r.CacheWrite1h)
}

// Totals is a sum of calls.
type Totals struct {
	// Calls are the requests, Errors those the agent got an error for, and
	// Retried those answered after a try that failed first (Record.Tries):
	// a try handed on is in neither count, but its tokens are in the sums.
	// Canceled are those the agent canceled before they were answered
	// (Record.Canceled): neither an error nor answered
	Calls    int `json:"calls"`
	Errors   int `json:"errors"`
	Retried  int `json:"retried,omitempty"`
	Canceled int `json:"canceled,omitempty"`
	// Loops are those of Errors magpie ended for looping (LoopErrType):
	// a model's count of them says how often it got stuck (#1858)
	Loops      int `json:"loops,omitempty"`
	Input      int `json:"input"`
	Output     int `json:"output"`
	CacheRead  int `json:"cache_read"`
	CacheWrite int `json:"cache_write"`
	// CacheWrite1h is how many of CacheWrite were 1-hour writes
	CacheWrite1h int `json:"cache_write_1h,omitempty"`
	// CacheUnknown is how many of the prompt's tokens (Input, CacheRead
	// and CacheWrite) were of calls whose vendor counted no cache
	// (Record.CacheUnknown), CacheUnknownCalls how many calls they were:
	// a hit rate is CacheRead over the prompt less CacheUnknown, and none
	// when nothing is left (#3464)
	CacheUnknown      int     `json:"cache_unknown,omitempty"`
	CacheUnknownCalls int     `json:"cache_unknown_calls,omitempty"`
	Reasoning         int     `json:"reasoning"`
	Cost              float64 `json:"cost"`     // USD at the effective price, for the priced calls
	Unpriced          int     `json:"unpriced"` // calls with tokens but no known price
	// Credits are what the vendors said Credited of the calls cost in
	// their plans' own credits (Record.Credits): the rest said nothing
	Credits  float64 `json:"credits,omitempty"`
	Credited int     `json:"credited,omitempty"`
	// Timed: the answered calls whose first token was timed (streamed),
	// TTFT the sum of their ttft_ms; DecodeMs the time from it to the end
	// of those that wrote any, over which DecodeOut tokens came (a
	// reasoning reply's answer, Record.Decode): their mean wait, and how
	// fast they wrote (#196)
	Timed     int   `json:"timed,omitempty"`
	TTFT      int64 `json:"ttft_ms,omitempty"`
	DecodeMs  int64 `json:"decode_ms,omitempty"`
	DecodeOut int   `json:"decode_out,omitempty"`
	// DecodeN is how many replies DecodeMs and DecodeOut are of: the ones
	// whose speed was measured, which a speed is shown with, so a group of
	// 260 calls whose speed is one reply's says so (Fallmonkey on Discord)
	DecodeN int `json:"decode_n,omitempty"`
	// FloorOut tokens over FloorMs are the least FloorN replies read from a
	// session file wrote at, those whose speed wasn't measured (Row.Floor):
	// what a group shows, as "≥", beside how many it is of, after its
	// measured speed when it has one. They are never in Speed.
	FloorOut int   `json:"floor_out,omitempty"`
	FloorMs  int64 `json:"floor_ms,omitempty"`
	FloorN   int   `json:"floor_n,omitempty"`
	// UpBytes, DownBytes and ImageFilesSaved are the rows' own summed
	// (Record.UpBytes …): what went up to the vendors and came back, of
	// the rows that kept it; none when no row did
	UpBytes         int64 `json:"up_bytes,omitempty"`
	DownBytes       int64 `json:"down_bytes,omitempty"`
	ImageFilesSaved int64 `json:"image_files_saved,omitempty"`
	// UpCalls is how many rows sent any bytes up, what UpBytes is spread
	// over; UpMeasured the bytes of those whose parts were read
	// (Record.UpParts), and UpSystem, UpTools and UpImages those parts
	// summed: the conversation is UpMeasured less the three
	UpCalls    int   `json:"up_calls,omitempty"`
	UpMeasured int64 `json:"up_measured,omitempty"`
	UpSystem   int64 `json:"up_system,omitempty"`
	UpTools    int64 `json:"up_tools,omitempty"`
	UpImages   int64 `json:"up_images,omitempty"`
}

// UpParts is a request's bytes up by what they were: the agent's own
// prompt and instructions (system prompt, skills, memory), the tools'
// definitions, and the images and documents it sent inline, each scaled
// to the bytes that went up (Record.UpBytes). The rest is the
// conversation: the messages, the tool calls and their results, which an
// agent sends again whole on every request.
type UpParts struct {
	System int `json:"system,omitempty"`
	Tools  int `json:"tools,omitempty"`
	Images int `json:"images,omitempty"`
}

// addBytes adds r's bytes on the wire (Totals.UpBytes …).
func (t *Totals) addBytes(r *Record) {
	t.UpBytes += int64(r.UpBytes)
	t.DownBytes += int64(r.DownBytes)
	t.ImageFilesSaved += int64(r.ImageFilesSaved)
	if r.UpBytes > 0 {
		t.UpCalls++
		if u := r.UpParts; u != nil {
			t.UpMeasured += int64(r.UpBytes)
			t.UpSystem += int64(u.System)
			t.UpTools += int64(u.Tools)
			t.UpImages += int64(u.Images)
		}
	}
}

// plusBytes adds what addBytes counted of another sum.
func (t *Totals) plusBytes(d *Totals) {
	t.UpBytes += d.UpBytes
	t.DownBytes += d.DownBytes
	t.ImageFilesSaved += d.ImageFilesSaved
	t.UpCalls += d.UpCalls
	t.UpMeasured += d.UpMeasured
	t.UpSystem += d.UpSystem
	t.UpTools += d.UpTools
	t.UpImages += d.UpImages
}

// Tokens is what went in and out, excluding cache traffic.
func (t Totals) Tokens() int { return t.Input + t.Output }

// MeanTTFT is the mean ms to the first token of the timed calls, 0 for none.
func (t Totals) MeanTTFT() int64 {
	if t.Timed == 0 {
		return 0
	}
	return t.TTFT / int64(t.Timed)
}

// Speed is how fast the timed calls wrote, in tokens a second after their
// first: 0 for none.
func (t Totals) Speed() float64 {
	if t.DecodeMs <= 0 {
		return 0
	}
	return float64(t.DecodeOut) / (float64(t.DecodeMs) / 1000)
}

// Floor is the least the replies whose speed wasn't measured wrote at, in
// tokens a second (Row.Floor): 0 for none.
func (t Totals) Floor() float64 {
	if t.FloorMs <= 0 {
		return 0
	}
	return float64(t.FloorOut) / (float64(t.FloorMs) / 1000)
}

// A reply's speed is its output tokens over its decode window, the ms
// from its first content to its end (#196; one that reasoned, DecodeOf); a window counts only when it
// timed the writing (#731). One that is shorter than MinDecodeMs, or over
// which the tokens would have come faster than MaxDecodeSpeed a second,
// didn't: the reply came in one burst at its end, written before the
// window opened — a turn that is one big tool call from Gemini, whose
// functionCall comes whole, has its first content a millisecond before
// its end, and 8264 tokens in 1 ms read 8,264,000 tok/s. Such a reply
// tells no speed and is left out of the summed one, where its tokens
// over next to no time would lift everyone's; its TTFT still counts.
// The bounds are wide of any real stream: the fastest vendors write a few
// thousand tokens a second, and a tenth of a second is the least a
// stream's timing tells anything by. routing/client.js's speedOf has the same.
const (
	MinDecodeMs    = 100
	MaxDecodeSpeed = 10000
)

// DecodeWindow is the ms a reply of out tokens, ms long with its first
// content at ttft, took to write them: 0 when it wasn't timed, wrote
// nothing, or came too fast to tell a speed by (above).
func DecodeWindow(out int, ms, ttft int64) int64 {
	w := ms - ttft
	if out <= 0 || ttft <= 0 || w < MinDecodeMs || int64(out)*1000 > MaxDecodeSpeed*w {
		return 0
	}
	return w
}

// DecodeOf is what a reply's speed is told by: the tokens it was seen to
// write and the ms it took (tony on Discord: gpt-6.1-sol read 163 tok/s,
// with a 14 s first token). A reply that reasoned counts only its answer —
// its output less the reasoning, from its first text — as most vendors
// keep the reasoning out of the stream (OpenAI's is encrypted, its summary
// sent when it's done; Claude's may come with no text): its tokens were
// written before the first content, over time the window from it doesn't
// hold, and counting them read a 2,000-token think and a 200-token answer
// in 1.5 s as 1,467 tok/s. Its time goes to the wait before the answer.
// With no answer text, paced visible reasoning can time all output from
// firstReasoning. Missing evidence (including older records), hidden reasoning
// and an unknown flow leave the speed untold. The
// window is DecodeWindow's: 0 for a reply that tells none.
//
// A reply its vendor held back and let go in a burst at its end tells
// none either (John on Discord: Kimi Code answering OpenCode read 1,367,
// then 2,237 and 2,012 tok/s). The burst is told two ways, as the reply's
// text came before it or with it:
//
//   - flow, when known (Record.Flow), is how long its content took to come
//     from where its window starts. A window HeldShare times longer had
//     its content come in a small part of it: the rest was a wait while
//     the vendor wrote and held it. The time it took to come is not the
//     time it took to write (a burst let go over 236 ms read 528 tokens at
//     2,237 tok/s), and that time is not seen, so it tells no speed. A
//     stream that came as it was written has a flow about its window.
//   - a reply that reasoned is timed from its first text, its reasoning
//     written before. When the answer would have been written over
//     ThinkPace times faster than its reasoning came before it, from the
//     first content to the first text, its first text came with the burst
//     and the wait before it was the answer being written and held (22
//     reasoning tokens over 10.5 s, then 528 answer tokens in 236 ms). A
//     reasoning hidden from the stream (OpenAI's, Claude's omitted) comes
//     before the first content and leaves this time short; one that
//     streams comes at the pace its answer does.
//   - a window under HeldWindowMs after a wait HeldShare times longer: the
//     reply came whole in a moment after a long silence, and what it was
//     written over is not seen (pluviophile714 on Discord: gpt-6.1-sol,
//     392 tokens 0.25 s after an 8.2 s first token, read 1,539 tok/s where
//     the vendor writes ~70). Its content can come spread over that moment
//     (a relay replaying a held reply, OpenAI's answer let go after its
//     hidden reasoning), so flow doesn't tell it. A stream as it is written
//     takes a second or more to write a few hundred tokens; one that takes
//     under a second is too short a sample to tell a speed by against such
//     a wait, so a short answer after a long think tells none either.
func DecodeOf(out, reasoning int, ms, ttft, firstText, flow int64, firstReasoning ...int64) (tokens int, w int64) {
	start := ttft
	if reasoning > 0 {
		if firstText > 0 {
			out, start = out-reasoning, firstText
		} else if len(firstReasoning) > 0 && firstReasoning[0] >= ttft && flow > 0 && reasoning <= out {
			start = firstReasoning[0]
		} else {
			return 0, 0
		}
	}
	if ttft <= 0 {
		return 0, 0
	}
	if w = DecodeWindow(out, ms, start); w == 0 {
		return 0, 0
	}
	if flow > 0 && flow*HeldShare < w {
		return 0, 0
	}
	if w < HeldWindowMs && start > HeldShare*w {
		return 0, 0
	}
	if reasoning > 0 && firstText > 0 && int64(out)*(firstText-ttft) > ThinkPace*int64(reasoning)*w {
		return 0, 0
	}
	return out, w
}

// HeldShare, ThinkPace and HeldWindowMs tell a reply held back and let go
// in a burst (DecodeOf): its content came in under a quarter of its window,
// its answer at over 20 times the pace its reasoning came, or its window
// was under a second and under a quarter of the wait before it.
// routing/client.js's decodeOf and usage/page.js's ledDecode have the same.
const (
	HeldShare    = 4
	ThinkPace    = 20
	HeldWindowMs = 1000
)

// Decode is the record's DecodeOf.
func (r Record) Decode() (tokens int, w int64) {
	if r.ReasoningHidden && r.FirstText <= 0 {
		return 0, 0
	}
	return DecodeOf(r.Output, r.Reasoning, r.Millis, r.TTFT, r.FirstText, r.Flow, r.FirstReasoning)
}

// FormatCredits is what t's credited calls cost in their plans' credits,
// as their replies said (#2512): "" when none said.
func FormatCredits(t Totals) string {
	switch {
	case t.Credited == 0:
		return ""
	case t.Credits > 0 && t.Credits < 0.01:
		return "<0.01 credits"
	}
	return strconv.FormatFloat(math.Round(t.Credits*100)/100, 'f', -1, 64) + " credits"
}

// FormatCost renders an effective-price cost, kept in USD everywhere it's
// stored, as the CLI and TUI show it: at amountUSD's own price when
// currency isn't "cny", else converted at rate (CNY per one USD, from
// internal/fx; a rate of 0 or below also falls back to USD, a stale or
// missing rate being no reason to hide the number). Whole dollars or yuan
// above 100, cents above 1, else thousandths, so a fraction of a cent
// still shows as something.
func FormatCost(amountUSD float64, currency string, rate float64) string {
	amount, sign := amountUSD, "$"
	if currency == "cny" && rate > 0 {
		amount, sign = amountUSD*rate, "¥"
	}
	switch {
	case amount >= 100:
		return fmt.Sprintf("%s%.0f", sign, amount)
	case amount >= 1:
		return fmt.Sprintf("%s%.2f", sign, amount)
	default:
		return fmt.Sprintf("%s%.3f", sign, amount)
	}
}

// countCache counts r's prompt as one whose cache wasn't counted when its
// vendor said so, or never counts it (a record kept before it said so).
func (t *Totals) countCache(r Record) {
	if r.cacheNotCounted() {
		t.CacheUnknown += r.cacheUnknownIn()
		t.CacheUnknownCalls++
	}
}

// CountsNoCache is whether a provider's vendor never counts cache tokens,
// so a call of it with none recorded was not a miss: Kiro caches but tells
// only credits. Calls kept before its plugin said so (opencode-kiro-auth
// 0.1.16's magpie_cache_unknown) carry no CacheUnknown, and are read as
// having it (#3464). The provider is the built-in's id, or the plugin's
// own when it is signed in under that.
func CountsNoCache(provider string) bool {
	return provider == "kiro" || provider == "kiro-plugin"
}

// cacheNotCounted is whether r's vendor counted no cache for it: it said
// so, or it never counts any and r has none (kept before it said so).
func (r Record) cacheNotCounted() bool {
	return r.CacheUnknown || r.CacheRead+r.CacheWrite == 0 && CountsNoCache(r.Provider)
}

// cacheUnknownIn is r's prompt when its vendor counted no cache, else 0.
func (r Record) cacheUnknownIn() int {
	if !r.cacheNotCounted() {
		return 0
	}
	return r.Input + r.CacheRead + r.CacheWrite
}

// HitRate is the share of the prompt read from the cache, of the calls
// whose vendor counted it; false when none did (#3464).
func (t Totals) HitRate() (float64, bool) {
	p := t.Input + t.CacheRead + t.CacheWrite - t.CacheUnknown
	if p <= 0 {
		return 0, false
	}
	return float64(t.CacheRead) / float64(p), true
}

// HitPct is a hit rate (0 to 1) as a percent, whole unless rounding would
// say 100% or 0% of a prompt not all or none cached, then to one or two
// decimals (ZhangXiaof48473 on X); the GUI's hitPct says it the same way.
func HitPct(h float64) string {
	s, v := "", 0.0
	for d := 0; d <= 2; d++ {
		s = strconv.FormatFloat(100*h, 'f', d, 64)
		if v, _ = strconv.ParseFloat(s, 64); h <= 0 || h >= 1 || (v > 0 && v < 100) {
			break
		}
	}
	switch {
	case h < 1 && v >= 100:
		s = "99.99"
	case h > 0 && v <= 0:
		s = "0.01"
	}
	return s + "%"
}

func (t *Totals) add(r Record, price *catalog.Price) { t.addFrom(Row{Record: r}, price) }

// addFrom is add for a ledger row, whose speed is told as Row.Decode tells
// it (a Codex turn of one call read from its session file, by its own rule).
func (t *Totals) addFrom(row Row, price *catalog.Price) {
	r := row.Record
	t.count(r)
	t.Input += r.Input
	t.Output += r.Output
	t.CacheRead += r.CacheRead
	t.CacheWrite += r.CacheWrite
	t.CacheWrite1h += r.CacheWrite1h
	t.countCache(r)
	t.Reasoning += r.Reasoning
	t.addBytes(&r)
	if r.TTFT > 0 && r.Answered() {
		t.Timed++
		t.TTFT += r.TTFT
		if n, w := row.Decode(); w > 0 {
			t.DecodeMs += w
			t.DecodeOut += n
			t.DecodeN++
		}
	}
	if r.Input+r.Output == 0 {
		return
	}
	if price == nil {
		t.Unpriced++
		return
	}
	t.Cost += r.CostAt(*price)
}

// LoopErrType is a record's ErrType for a reply magpie ended because the
// model was stuck in a loop (#1359), told apart from what vendors call
// their errors.
const LoopErrType = "reply_loop"

// count counts r as the request it ends: failed, canceled by its agent,
// answered on a retry, or answered; a try handed on to another is no
// request of its own.
func (t *Totals) count(r Record) {
	// a try handed on is billed its credits too, though it is no request
	if r.Credits != nil {
		t.Credits += *r.Credits
		t.Credited++
	}
	if r.HandedOn {
		return
	}
	t.Calls++
	switch {
	case r.Canceled():
		t.Canceled++
	case r.Failed():
		t.Errors++
		if r.ErrType == LoopErrType {
			t.Loops++
		}
	case r.Tries > 1:
		t.Retried++
	}
}

// LostRequest is whether r ends a request the agent got an error for: a
// failed row that no later try took over from.
func (r Record) LostRequest() bool { return r.Failed() && !r.HandedOn }

// FailedTry is whether a try of r's request failed: r failed (whether the
// agent got the error or a later try took over), or r answered after a
// try that failed first. What Usage's Failed filter keeps: Routing shows
// those tries failed, and a request a fallback answered had none of its
// own failures listed under Failed (maohy915 on X).
func (r Record) FailedTry() bool { return r.Failed() || r.RetriedRequest() }

// RetriedRequest is whether r ends a request that was answered after a try
// that failed first (not one its agent canceled after a failed try).
func (r Record) RetriedRequest() bool { return !r.HandedOn && r.Answered() && r.Tries > 1 }

// Group is the share of one agent or model.
type Group struct {
	ID              string `json:"id"`
	Provider        string `json:"provider,omitempty"` // models only
	Model           string `json:"model,omitempty"`
	ProviderKeyID   string `json:"providerKeyId,omitempty"`
	ProviderKeyName string `json:"providerKeyName,omitempty"`
	// Account is the subscription account of an Accounts group: "" for
	// the calls of an account's provider whose record names none
	Account       string `json:"account,omitempty"`
	CallerKeyID   string `json:"callerKeyId,omitempty"`
	CallerKeyName string `json:"callerKeyName,omitempty"`
	// Host is where the calls went, when the provider's id has gone to
	// more than one place, or elsewhere than the provider goes now: its
	// calls are then told apart by it, not summed under the id.
	Host string `json:"host,omitempty"`
	// Agent is the agent a session is of (sessions only).
	Agent string `json:"agent,omitempty"`
	Totals
	// Own is the share of these calls magpie made by itself (Record.ByMagpie):
	// a routing classifier's, a web search's or an image description's,
	// counted in Totals too, since they went to the same key or account
	// (#1839). Filled for models, keys and accounts; a value, so a
	// summary's copies share nothing.
	Own OwnShare `json:"own,omitzero"`
}

// OwnShare is what magpie's own calls of a group came to.
type OwnShare struct {
	Calls  int     `json:"calls"`
	Tokens int     `json:"tokens"` // in and out, cache aside, as Totals.Tokens
	Cost   float64 `json:"cost"`
}

// addOwn counts r in g's own share when magpie made it by itself.
func (g *Group) addOwn(r Record, price *catalog.Price) {
	if !r.ByMagpie() {
		return
	}
	if !r.HandedOn {
		g.Own.Calls++
	}
	g.Own.Tokens += r.Input + r.Output
	if price != nil && r.Input+r.Output > 0 {
		g.Own.Cost += r.CostAt(*price)
	}
}

// ByMagpie is whether magpie made the call by itself rather than an agent:
// its routing classifier, System One's decision, a web search or an
// image description it asked for (#1839).
func (r Record) ByMagpie() bool {
	id := AgentOf(r.Agent)
	// "magpie" as the agents know it; its own User-Agents (magpie-router/1,
	// magpie-search/1) where they aren't loaded
	return id == "magpie" || strings.HasPrefix(id, "magpie-")
}

// who is where a subscription's calls went as the account signed in
// ("dee@example.com" of "api.factory.ai as dee@example.com"), the host
// alone for the rest. The account is what tells one place from another: a
// built-in moved onto its plugin sends the same account's calls through
// plugin://…, which isn't another place.
func who(where string) string {
	if i := strings.LastIndex(where, " as "); i >= 0 {
		return where[i+4:]
	}
	return where
}

// Account is the subscription account that answered the call: the one the
// record names, or, in a record written before it named one, the account
// its Host said the call went out as then ("chatgpt.com as dee@example.com")
// — what the record itself says, never today's sign-in. "" when neither
// tells (#557).
func (r Record) Account() string {
	if r.ProviderAccount != "" {
		return r.ProviderAccount
	}
	if i := strings.LastIndex(r.Host, " as "); i >= 0 {
		return r.Host[i+4:]
	}
	return ""
}

// Point is one bar of the timeline.
type Point struct {
	Label string    `json:"label"`
	Time  time.Time `json:"time"`
	Totals
}

// Summary is a period, summed up.
type Summary struct {
	Period Period    `json:"period"`
	Since  time.Time `json:"since"`
	Totals
	Bucket       string  `json:"bucket"` // hour | day | week
	Series       []Point `json:"series"`
	Agents       []Group `json:"agents"`
	Models       []Group `json:"models"`
	ProviderKeys []Group `json:"providerKeys"`
	// Accounts are the subscription accounts' calls, by provider and the
	// account that answered (#557)
	Accounts   []Group `json:"accounts"`
	CallerKeys []Group `json:"callerKeys"`
	// Sessions are the calls that named their session, by session, the
	// most tokens first: all of them, or the top few of SessionCount where
	// it was asked for that way (SummarizeTop).
	Sessions     []Group `json:"sessions"`
	SessionCount int     `json:"sessionCount"`
}

// Summarize caches the four periods over an indexed log snapshot. Callers
// receive their own result slices, without retaining historical Records.
func Summarize(p Period) Summary {
	return indexedSummary(p, localNow())
}

func summarize(p Period, now time.Time, recs []Record) Summary {
	var first time.Time
	keys := map[string]bool{}
	for _, r := range recs {
		if !r.IsRejected() && (first.IsZero() || r.Time.Before(first)) {
			first = r.Time
		}
		if r.ProviderKeyID != "" {
			keys[r.Provider] = true
		}
	}
	return summarizeFrom(p, now, first, keys, func(fn func(Record)) {
		for _, r := range recs {
			fn(r)
		}
	})
}

func summarizeFrom(p Period, now time.Time, first time.Time, historicalKeys map[string]bool, read func(func(Record))) Summary {
	u := newSummaryRun(p, now, first, historicalKeys)
	hosts := newSummaryHosts()
	u.read(read, func(r Record) { hosts.note(r, u.since) })
	keyed := u.keying(hosts)
	counts := u.newCounts()
	u.read(read, func(r Record) { counts.add(u, &keyed, r) })
	return counts.summary(u)
}

// ---- last seen --------------------------------------------------------------

// seen is when each agent's latest request reached the gateway in this
// process — at its start, where a record is written only once it is answered.
var seen sync.Map // agent id → time.Time

// Via is where the gateway sent a session's calls: one model at one
// reasoning effort, how often and how much.
type Via struct {
	Provider string    `json:"provider"`
	Model    string    `json:"model"`
	Effort   string    `json:"effort,omitempty"`
	Calls    int       `json:"calls"`
	Tokens   int       `json:"tokens"` // in and out, as Totals counts them
	Last     time.Time `json:"last"`
	// Credits are what Credited of the calls cost in the plan's own
	// credits, as their replies said (Record.Credits)
	Credits  float64 `json:"credits,omitempty"`
	Credited int     `json:"credited,omitempty"`
}

// Vias is what the gateway sent each session's calls to since a time, by
// agent id and the session's id ("codex|<id>"), the most calls first.
func Vias(since time.Time) ViaIndex {
	return cachedVias(since)
}

// LogVersion is a number for the log as it stands, which changes when a
// call is written to it, and whether there is one: a synthetic log
// (LogCalls), or one not read to its end, has none; no log yet is one. The Sessions page
// keys an unchanged list on it.
func LogVersion() (uint64, bool) {
	if LogCalls != nil {
		return 0, false
	}
	s := readLogSnapshot()
	return s.version, s.info == nil || s.complete()
}

// A provider renamed since is listed by the id it has now (renames): the
// Sessions tab named a renamed one by its old id (cybercaius).
func buildVias(snapshot *logSnapshot, since time.Time, renames provider.Renames) *viaLists {
	counted := map[string]viaAcc{}
	countVias(snapshot.blocks, since, renames, nil, counted, keptStrings{})
	return listedVias(&viaLists{m: map[string][]Via{}, first: map[string]time.Time{}}, counted)
}

// viaAcc is one session's lists as counted, in the order its calls first
// named each, and when its first call was.
type viaAcc struct {
	vs    []Via
	first time.Time
}

// countVias counts blocks' calls into own, a session's counts begun from
// a copy of base's: base is never written.
func countVias(blocks []*rowChunk, since time.Time, renames provider.Renames, base, own map[string]viaAcc, keep keptStrings) {
	visitBlocks(blocks, since, func(r Record) {
		if r.IsRejected() || r.IsReported() || r.Session == "" || r.Model == "" {
			return
		}
		r.Provider, r.Model, r.Effort = keep.of(renames.Of(r.Provider, r.Time)), keep.of(r.Model), keep.of(r.Effort)
		k := ReportedAs(r.Agent) + "|" + r.Session
		a, seen := own[k]
		if !seen {
			if b, ok := base[k]; ok {
				a, seen = viaAcc{vs: slices.Clone(b.vs), first: b.first}, true
			}
		}
		if !seen || r.Time.Before(a.first) {
			a.first = r.Time
		}
		vs := a.vs
		i := slices.IndexFunc(vs, func(v Via) bool { return v.Provider == r.Provider && v.Model == r.Model && v.Effort == r.Effort })
		if i < 0 {
			vs, i = append(vs, Via{Provider: r.Provider, Model: r.Model, Effort: r.Effort}), len(vs)
		}
		vs[i].Calls++
		vs[i].Tokens += r.Input + r.Output
		if r.Credits != nil {
			vs[i].Credits += *r.Credits
			vs[i].Credited++
		}
		if r.Time.After(vs[i].Last) {
			vs[i].Last = r.Time
		}
		a.vs = vs
		own[k] = a
	})
}

// listedVias is lists with each session counted in counted listed in
// place of its own, the most calls first; lists itself is not written.
func listedVias(lists *viaLists, counted map[string]viaAcc) *viaLists {
	if len(counted) == 0 {
		return lists
	}
	out := &viaLists{m: maps.Clone(lists.m), first: maps.Clone(lists.first)}
	for k, a := range counted {
		vs := slices.Clone(a.vs)
		sort.SliceStable(vs, func(i, j int) bool { return vs[i].Calls > vs[j].Calls })
		out.m[k], out.first[k] = vs, a.first
	}
	return out
}

// Saw notes a request from an agent arriving now.
func Saw(agent string) { seen.Store(agent, time.Now()) }

// LastSeen is when a request from the agent last reached the gateway: this
// process's own, else the newest in the log's last stretch. Zero if none.
func LastSeen(agent string) time.Time {
	var last time.Time
	if t, ok := seen.Load(agent); ok {
		last = t.(time.Time)
	}
	mu.Lock()
	defer mu.Unlock()
	f, err := os.Open(Path())
	if err != nil {
		return last
	}
	defer f.Close()
	const tail = 256 << 10
	if st, err := f.Stat(); err == nil && st.Size() > tail {
		f.Seek(st.Size()-tail, 0)
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var r Record
		if json.Unmarshal(sc.Bytes(), &r) == nil && r.Agent == agent && r.Via == "" && !r.IsProbe() && r.Time.After(last) {
			last = r.Time
		}
	}
	return last
}
