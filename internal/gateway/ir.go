// Package gateway is the local LLM endpoint agents talk to. It serves the
// wire APIs coding agents speak — OpenAI Chat Completions, OpenAI
// Responses, Anthropic Messages and Google Gemini — and forwards each call to whichever
// provider serves the requested model, translating between the APIs when
// the provider does not speak the one the agent used.
package gateway

import (
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// The APIs are close cousins; everything below is the shape they have
// in common. A request is parsed into it, and a reply is produced from it.

// Kind is what a part of a message holds.
type Kind string

const (
	Text       Kind = "text"
	Image      Kind = "image"
	File       Kind = "file"
	ToolCall   Kind = "tool_call"
	ToolResult Kind = "tool_result"
	Thinking   Kind = "thinking"
	Search     Kind = "web_search" // a web search run for the model: its query and hits
)

// Part is one block of a message.
type Part struct {
	Kind Kind
	Text string // text, thinking, or a tool result's output

	// image or file
	MediaType string
	Data      string // base64
	URL       string
	// Detail is an image's detail as the client asked for it (low, high,
	// auto, or Responses' original), which a Chat or Responses upstream
	// is given back (chatDetail, responsesDetail)
	Detail string

	// tool_call
	ID   string
	Name string
	Args json.RawMessage // a JSON object

	// tool_result
	CallID     string
	IsError    bool
	Images     []Part         // the images the tool returned beside its text
	Standalone map[string]any // native Responses notification with no call ID
	// Loads are the tools a tool's result loads, by name: Claude Code's
	// ToolSearch answers with tool_reference blocks naming the deferred
	// tools it found (#2660)
	Loads []string

	// thinking: its signature (Anthropic's, or a Gemini thought's). On a
	// tool call, and on text, Gemini's thoughtSignature: a tool call's
	// comes only from Google, as gemini_signature.go carries it, and
	// text's only from a Gemini API (#1445)
	Signature string
	// SignedBy is the API that signed a thinking Part (sealAnthropic for
	// Claude's), empty when it isn't known: only Claude's signature goes
	// to a Responses client, in its reasoning item's encrypted_content
	// (claudeseal)
	SignedBy string
	// thinking an upstream sealed, as its API wrote it: Anthropic's
	// redacted_thinking block, a Responses reasoning item with its id,
	// summary and encrypted_content. Only that API's builder, encoder and
	// renderer write it, byte for byte; to any other it is reasoning with
	// no text, or Text's (#1445). SealedBy is the API (sealAnthropic,
	// sealResponses).
	Sealed   json.RawMessage
	SealedBy string

	// web_search: Text is the query
	Hits []Hit

	cache string // opted-in Claude bridge breakpoint TTL; empty for none

	// block reports the part came from one content block of the client's
	// own: a client-marks prompt ends a rendered block there even when the
	// client put no mark on it, or an earlier round's marked boundary is
	// merged away and its cache write is found no more (#3835)
	block bool
}

// The APIs a Part's Sealed reasoning is in.
const (
	sealAnthropic = "anthropic"
	sealResponses = "responses"
)

// sealedBy reports whether p is reasoning api sealed.
func (p Part) sealedBy(api string) bool {
	return p.Kind == Thinking && p.SealedBy == api && len(p.Sealed) > 0
}

// Hit is a page a web search found.
type Hit struct {
	Title string `json:"title"`
	URL   string `json:"url"`
	// PageAge is how old the search said the page is, "" when it didn't
	PageAge string `json:"page_age,omitempty"`
	// Cited is what the search said of the page (a search API's snippet,
	// the text a searcher's own citation quotes), which a citation of it
	// quotes (citesOf); "" when it said nothing of it alone
	Cited string `json:"cited_text,omitempty"`
}

// attachmentText is the fallback when a protocol cannot carry a Gemini file.
// Inline data has no URL to show and must not be relabeled as an image.
func attachmentText(p Part) string {
	if p.URL != "" {
		return "[attachment " + p.MediaType + ": " + p.URL + "]"
	}
	return "[attachment " + p.MediaType + "]"
}

// Message is one turn.
type Message struct {
	Role  string // user | assistant; system | developer on Responses-to-Responses
	Parts []Part
}

// joinSplitCalls joins an assistant message onto the assistant message
// before it when that one made tool calls: Anthropic's Messages takes
// consecutive assistant messages as one turn, and an agent can send a
// turn's parallel tool_use blocks split over two of them, answered by one
// user message (#1275). A builder that closes a turn's calls at the next
// assistant message would otherwise answer the first calls as interrupted
// and lose their real results. ms is left as it is.
func joinSplitCalls(ms []Message) []Message {
	out := make([]Message, 0, len(ms))
	for _, m := range ms {
		if n := len(out); n > 0 && m.Role == "assistant" && out[n-1].Role == "assistant" && hasCalls(out[n-1].Parts) {
			out[n-1].Parts = append(out[n-1].Parts, m.Parts...)
			continue
		}
		out = append(out, Message{Role: m.Role, Parts: append([]Part(nil), m.Parts...)})
	}
	return out
}

// hasCalls reports whether parts hold a tool call.
func hasCalls(parts []Part) bool {
	for _, p := range parts {
		if p.Kind == ToolCall {
			return true
		}
	}
	return false
}

// Tool is a function the model may call.
type Tool struct {
	Name        string
	Description string
	Schema      json.RawMessage // JSON schema of the arguments
	Strict      bool            // the client asked for its arguments held to the schema
	// Deferred is a tool the client marked defer_loading: Claude Code's
	// tools that ToolSearch loads, offered from where they were loaded
	// (Part.Loads), not from the start (#2660)
	Deferred bool
}

// Request is a call to a model, whichever API it arrived in.
type Request struct {
	// clientCache is non-nil only for a request read with its client's
	// marks: the Claude bridge's explicit opt-in, and a Messages ask
	// translated to an Anthropic upstream (parseTranslated, #4571).
	clientCache *clientCache
	// cacheTTL is how long a Claude bridge run started for the request
	// keeps its prompt cache (claudeRunTTL), which the marks magpie adds
	// follow (runMark); "" is an hour
	cacheTTL string
	// askedTTL is the longest TTL the request's own cache marks ask for
	// (askedCacheTTL), "" for one that marks nothing or came in another API
	askedTTL string
	// pickedTTL is the cache lifetime the user of the computer the
	// request's Claude Code runs on picked, "default" for none, "" when
	// unknown (claudeTTLOf)
	pickedTTL string
	// leanPrompt has the Claude bridge give the caller's instructions as
	// the run's system prompt, in place of Claude Code's (ClaudeLeanPrompt).
	leanPrompt bool
	// appendPrompt: the Claude bridge gives the caller's instructions
	// after Claude Code's own system prompt (appendsPrompt), set as the
	// run starts.
	appendPrompt bool
	// envElsewhere: the caller's instructions name a working directory the
	// Claude bridge's run is not in (a client on another computer, or in
	// Windows with Claude Code in WSL), so the run's own prompt describes
	// another machine than the one the tools run on (envElsewhereNote).
	envElsewhere bool
	// envInTurns: that working directory is named in a message (Claude
	// Code's environment as a {role: "system"} message), not in the
	// caller's instructions (envTurnsNote).
	envInTurns bool
	// envDir: that working directory, as the caller names it.
	envDir string
	// envUnknown: the caller is on another computer (remoteCaller) and its
	// instructions name no working directory, so the run's own prompt
	// describes a machine no tool runs on, and nothing says which does
	// (envUnknownNote).
	envUnknown bool
	// remoteCaller: the request came from another computer (not local).
	remoteCaller bool
	// instructionsKept is set on a request whose read kept the client's
	// instruction roles in its messages (readResponsesFor, #2532): a
	// Responses client's own system and developer items, which a relay
	// may be checking them by. A translated request's system message
	// (Claude Code's environment, envInTurns) has no such standing:
	// buildResponses folds it into the instructions, as the Chat and
	// Anthropic builds do (#4632).
	instructionsKept bool
	// branch, for the Claude bridge, is the session of the run whose
	// reply made the calls this request answers after another request
	// did: its run branches off it there (subscriptionBridge.branch).
	branch *savedSession
	// told, for the Claude bridge's interactive mode, is a run's being
	// told the conversation whole, not given it as a transcript
	// (replaySession), as after Claude Code couldn't go on from one
	told bool
	// replayed: the run started was given the conversation as a
	// transcript (replaySession)
	replayed bool
	// interactive, for the Claude bridge, is that the run answering this
	// request is Claude Code's interactive mode, not claude -p: what a
	// refusal it ends in is told with (thirdPartyRefused, #4503)
	interactive bool
	// tuiRefused, for the Claude bridge, is a request whose agent's
	// system prompt Anthropic refused in the interactive mode as a
	// third-party app's (#4503): its run is claude -p, which is served
	// such prompts, with the interactive mode on all the same
	// (thirdPartyPrint)
	tuiRefused bool
	// hourMarks has the cache marks magpie adds on Anthropic's Messages
	// (anthropicBody) last an hour: a Responses client's (Codex's), whose
	// turns are often more than five minutes apart and whose own vendor
	// keeps a thread's prompt cached an hour (chatGPTCold). Off when the
	// client asked prompt_cache_retention "in_memory", or the upstream
	// turned the ttl away (hourMarksRefused).
	hourMarks bool
	// videoOff is set on a request built for a model known not to take
	// video (videoInput): a video of an earlier turn is told of in words
	// (attachmentText) where it would go as a video block (video_input.go)
	videoOff bool
	// draws asks the model for images beside text: a drawing asked on
	// Gemini's API of a model not named for images (drawCodeAssist).
	draws bool

	// strictSaid has a Chat request's tools say strict as each is set,
	// false included, as OpenCode's own do: Zen's free tier
	// (zenFreeTools).
	strictSaid bool

	// subagent: the request is from a subagent the agent started, as
	// Claude Code (x-claude-code-agent-id) and Codex (collab_spawn) tell
	// it, for a rule's subagent condition. Other agents don't say.
	subagent bool

	// systemParts: each block of the system prompt as the client sent it
	// (Anthropic's system blocks, a system or developer message, Responses'
	// instructions), before magpie adds to or joins them, for a rule's
	// system condition (SucroseMoE on X). systemRaw holds the ones still
	// as JSON (a string or text blocks), read only when a rule asks
	// (systemTexts): a Claude Code system prompt is tens of KB.
	systemParts []string
	systemRaw   []json.RawMessage

	Model      string
	System     string
	Messages   []Message
	Tools      []Tool
	ToolChoice string // "" | auto | none | required | name:<tool>
	MaxTokens  int
	Temp       *float64
	TopP       *float64
	Stop       []string
	Stream     bool
	Effort     string // low | medium | high | xhigh | max, when the client asked
	Thinking   bool   // the client asked for visible reasoning
	// ThinkOff is the client turning reasoning off: effort "none" (which
	// Effort reads as low, for vendors with no way to turn it off) or an
	// Anthropic request with thinking disabled.
	ThinkOff  bool
	Parallel  *bool // parallel tool calls allowed
	WebSearch bool  // the client offered its provider's own web search
	// WebAccess is the external_web_access of the client's web_search
	// tool, as it sent it: Codex's web_search mode (false for "cached",
	// true for "live"), which magpie's own search for a model that can't
	// is made in (withWebAccess, #5018)
	WebAccess json.RawMessage
	Fast      bool // the client asked for priority processing: service_tier priority (Codex's Fast mode)
	// Ultrafast is Codex's Ultrafast (service_tier "ultrafast"), which a
	// ChatGPT account on a plan with it offers on its models; Fast is set
	// with it, so where there is no Ultrafast the request goes fast
	Ultrafast bool
	// Tier is the service_tier the client sent, as it sent it. It goes on
	// as it is to a provider the user added by its address (OwnTier):
	// a relay of theirs may serve Fast and Ultrafast where magpie can't
	// tell (hsiangron on X: both were left out)
	Tier string
	// OwnTier is set for such a provider (gateway.go, as each attempt is
	// built); one that turns the field away is asked again without it
	// (optionalFields)
	OwnTier bool
	// OwnEffort is set when Effort is magpie's own ask, not the client's:
	// auto mode's classifier asked to reason least of a model whose levels
	// aren't known (fitAutoModeClassifier). An upstream that turns it away
	// is asked at low, or without it, as it would have been
	// (ownEffortRefused)
	OwnEffort bool
	// CacheKey is the client's prompt_cache_key (Codex sends its thread's
	// id), which OpenAI, and relays in front of it, route a conversation by
	// to where its prompt is cached.
	CacheKey string
	// Include is a Responses client's include, the extra output it asked
	// for (Codex's reasoning.encrypted_content), which a Responses upstream
	// is asked for too: a relay may refuse a request without it (#315).
	Include []string
	// ClientMetadata and Text are a Responses client's client_metadata
	// (Codex's installation and session ids, which a relay may check, #374)
	// and text (its verbosity), which go on as they were sent when the
	// request is built again for a Responses upstream; no other API takes
	// them. text's format is read into Format.
	ClientMetadata json.RawMessage
	Text           json.RawMessage
	// AccessPrograms is a client's access_programs, the program a request
	// asks the model under (OpenAI's Daybreak: {"cyber":"daybreak_blue"}),
	// which Codex CLI sends the ChatGPT backend on every turn it picked one
	// for (codex-rs's ResponsesApiRequest.access_programs). It goes on as it
	// was sent, null and all, when the request is built again for a
	// Responses upstream, whose answer to a value it doesn't take
	// (invalid_access_program) is the client's to read; no other API takes
	// it (magpie-releases#18). Only OpenAI's upstreams are sent it
	// (keepsAccessPrograms, #3172).
	AccessPrograms json.RawMessage
	// Metadata is an Anthropic client's metadata (Claude Code's user_id),
	// which goes on as it was sent when the request is built again for an
	// Anthropic upstream: a relay that serves only Claude Code turns a
	// request without it away (#359).
	Metadata json.RawMessage
	// ImageConfig is a Gemini client's generationConfig.imageConfig, the
	// size and shape it asks an image model to draw at (imageSize,
	// aspectRatio, and Vertex AI's fields beside them), which goes on as
	// it was sent when the request is built again for an upstream that
	// speaks Gemini, inside Code Assist's envelope too
	// (buildCodeAssistSent). Left out, the picture came back at the
	// model's default size (#3820). No other API is known to take it.
	ImageConfig json.RawMessage
	// GenConfig holds a Gemini client's generationConfig keys magpie
	// doesn't read or write itself — topK, seed, the penalties, responseModalities, mediaResolution, speechConfig,
	// responseFormat — which go on as they were sent when the request is
	// built again for an upstream that speaks Gemini's own API (the Gemini
	// one and Vertex AI, geminiRequest): left out, everything else the
	// client asked was dropped (#3937). Code Assist's and Antigravity's
	// envelope has no documented spelling of them, so they are not passed
	// there.
	GenConfig map[string]json.RawMessage
	// Safeguards are the caller's safety context, opaque to the gateway.
	Safeguards    json.RawMessage
	SafeguardBeta string
	// Format is the shape the client asked the answer in (structured
	// output: format.go), nil for plain text. Text, a Responses client's,
	// holds it no more: it is asked for again in the upstream's own words.
	Format *Format
	// FormatTold is set when Format was told in the system prompt instead
	// (inSystem): the client still wants the answer as JSON.
	FormatTold bool
	// GeminiCompat is the upstream being Gemini's OpenAI-compatible API
	// (AI Studio's, or a proxy in front of it on this machine or the LAN),
	// which gives the model's thoughts only when asked in thinking_config.
	GeminiCompat bool
	// OffLevel is the level such an API is asked to think at when the
	// client turned reasoning off: Gemini 3 can't stop thinking, and thinks
	// least at minimal, or at its lowest level where it has no minimal or
	// turned minimal away (geminiLevels). "" is minimal.
	OffLevel string
	// Resume is set on a request built to go on with a reply the client
	// already has part of (continuation.go): its last message is that
	// part, an assistant message the model goes on from, not a turn
	// answered.
	Resume bool
	// LastIsTurn is set on a request from a client whose API reads a
	// conversation ending with the assistant's message as a turn already
	// said, to answer after (OpenAI's Chat and Responses), not one to go
	// on from (Anthropic's prefill): to a model that takes no prefill it
	// is asked with a user turn after it (prefill.go).
	LastIsTurn bool
	// Namespaced are the tools a Responses client offered inside a
	// namespace, by the flat name the model is offered them under.
	Namespaced map[string]nsTool
	// LoadedInPlace has a Responses upstream that takes an additional_tools
	// item (loadsInPlace) given a deferred tool a result loaded in such an
	// item right after that result, not in the top-level tools: the tools
	// and the conversation sent before stay as they were, so its prompt
	// cache does (#2660). Every other upstream gets them in tools.
	LoadedInPlace bool
	// CR is set when the model asked is one of the glm-5.3-flash* routes
	// whose replicas stitch CR into a call's arguments (#4256): the
	// arguments reach the client with the stitched CR dropped
	// (tool_args_cr.go).
	CR bool
}

// nsTool is a tool as a Responses client knows it: by its namespace and its
// name in it (Codex's collaboration.spawn_agent). Search is Codex's own
// tool search, offered to the model as a function and handed back as the
// tool_search_call Codex runs. Custom is a custom (freeform) tool, offered
// as a function taking its input, its call handed back as the
// custom_tool_call Codex runs.
type nsTool struct {
	Namespace, Name string
	Search, Custom  bool
}

// EventKind is what a streamed event carries.
type EventKind int

const (
	KStart     EventKind = iota // MsgID, Model, Usage (input side)
	KText                       // Text
	KThink                      // Text (reasoning)
	KSig                        // Text (thinking signature), Name the API that signed it if known
	KToolStart                  // ID, Name
	KToolArgs                   // Text (partial JSON of the arguments)
	KStop                       // Stop
	KUsage                      // Usage
	KError                      // Text
	KSearch                     // Text (the query), Hits: a web search run for the model
	KImage                      // Name (media type), Text (base64): an image the model made
	// KThinkStart: a new block of reasoning begins (Anthropic's thinking
	// block, a Responses reasoning item, ID its id): what follows is not
	// the one before's, even with nothing between them
	KThinkStart
	// KSealed: Text is reasoning the upstream sealed, whole as its API
	// wrote it, Name that API (Part.Sealed): Anthropic's redacted_thinking
	// block, a Responses reasoning item as it was done
	KSealed
	// KTextSig: Text is Gemini's thoughtSignature on the text before it
	KTextSig
	// KAlive: nothing of the reply, only that its provider is still heard
	// from (a keepalive, an event with nothing for the client) after
	// keepaliveGap of nothing else: a reply read in rounds (searchReply)
	// keeps its client alive with it as a translated stream does (#2316)
	KAlive
)

// Event is one thing a streaming reply said.
type Event struct {
	Kind   EventKind
	Status int // upstream HTTP status for KError, when known
	Text   string
	ID     string
	Name   string
	MsgID  string
	Model  string
	Stop   string // stop | length | tool | filter
	// Code: for KError, the source error or safety-filter code (rate_limit,
	// server_error, bio_policy, content_filter…); RequestID: the vendor's id for the
	// request the event is of, when it is known by then
	Code             string
	RequestID        string
	Usage            Usage
	Hits             []Hit
	SafeguardResults json.RawMessage
	// StopDetails: Anthropic's stop_details, what its classifier said of
	// a refusal (#1768), carried to the client as it came
	StopDetails json.RawMessage
	// Estimated: the vendor's estimated_tokens on an empty thinking_delta,
	// its live progress beat during a long think (#5390); nil when the
	// delta carried none
	Estimated *int

	// Wait: for KError, how long the vendor said to wait before asking
	// again, when it said; sent on as Retry-After
	Wait time.Duration
	// Said: for KError, the vendor's own error as {"error":{…}}, when it
	// said the request itself is at fault; its code and param go to a
	// client answered whole (writeVendorError)
	Said json.RawMessage
}

// Usage counts tokens.
type Usage struct {
	Input      int `json:"input"`
	Output     int `json:"output"`
	CacheRead  int `json:"cache_read"`
	CacheWrite int `json:"cache_write"`
	// CacheWrite1h is how many of the CacheWrite tokens were written to
	// be kept for an hour, which Anthropic bills at 2× input where a
	// 5-minute write is 1.25×, when its usage says so (cache_creation's
	// ephemeral_1h_input_tokens)
	CacheWrite1h int `json:"cache_write_1h,omitempty"`
	// cacheCreationReported distinguishes a reported zero or pure-5m split
	// from usage that did not report TTLs. It is wire evidence, not a counter.
	cacheCreationReported bool
	// cacheCreationHasCounts describes the parsed update's own split.
	// It is never accumulated from earlier usage.
	cacheCreationHasCounts bool
	// cacheUnknown: the reply said its vendor counts no cache tokens
	// (a plugin's magpie_cache_unknown), so a CacheRead of 0 is not a miss
	cacheUnknown bool
	// searches: the web searches the reply says its model ran with its
	// provider's own tool (searchTally), for the request log's Web search
	// rows; told nowhere else
	searches int
	// restart: the usage of a request that goes on with a reply another
	// began (after): its prompt counts are the reply's from now on, a
	// zero too, as what the earlier one counted is the reply's Deferred
	restart bool

	Reasoning int `json:"reasoning"`
	// Served: the model the vendor's reply says answered, when it named
	// one — which may not be the one it was asked for
	Served string `json:"served,omitempty"`
	// Upstream: the provider an aggregator says answered behind it
	// (OpenRouter's "provider": DeepInfra, Novita …), when it says one
	Upstream string `json:"upstream,omitempty"`
	// RequestID: the id the vendor gave the request, from its reply's
	// headers (Claude Code's own for a subscription); ErrType: what a
	// failed request's error body called the error
	RequestID string `json:"request_id,omitempty"`
	// ResponseID is the final client response ID, independent of request headers.
	ResponseID string `json:"response_id,omitempty"`
	ErrType    string `json:"err_type,omitempty"`
	// Stop is why the upstream said its reply ended, in its own words
	// (stop_reason, finish_reason, a Responses status): "" when it said
	// none, as a stream that just stops does (upstreamStop)
	Stop string `json:"stop,omitempty"`
	// Tier is the service tier the reply says it ran at (upstreamTier),
	// "" when it says none
	Tier string `json:"tier,omitempty"`
	// Deferred is what the reply counted of earlier requests of its
	// conversation, which their own replies couldn't (cUsage's
	// MagpieDeferred): billed with this one, never told as its prompt
	Deferred *Usage `json:"deferred,omitempty"`
	// Credits is what the reply said the request cost in its plan's own
	// credits, nil when it said none: WorkBuddy's usage.credit, the
	// figure its app shows on each reply as "Credits Consumed" (#2512)
	Credits *float64 `json:"credits,omitempty"`
}

// billed is what the request is billed for: its own count, and what it
// counted of earlier ones (Deferred).
// cacheNotCounted is whether the vendor counted no cache for this usage:
// it said so (cacheUnknown) and no try of it reported a cache token. The
// Routing page and the ledger leave such a prompt out of a hit rate (#3464).
func (u Usage) cacheNotCounted() bool {
	return u.cacheUnknown && u.CacheRead+u.CacheWrite == 0
}

func (u Usage) billed() Usage {
	if d := u.Deferred; d != nil {
		u.Input += d.Input
		u.Output += d.Output
		u.CacheRead += d.CacheRead
		u.CacheWrite += d.CacheWrite
		u.CacheWrite1h += d.CacheWrite1h
		u.Reasoning += d.Reasoning
		u.Deferred = nil
	}
	return u
}

// prompt is every token the prompt came to, as OpenAI's and Gemini's
// counts have it: Anthropic's leaves out what was read from its cache and
// what was written to it.
func (u Usage) prompt() int {
	return u.Input + u.CacheRead + u.CacheWrite
}

func (u *Usage) add(v Usage) {
	if v.restart {
		u.Input, u.CacheRead, u.CacheWrite, u.CacheWrite1h = v.Input, v.CacheRead, v.CacheWrite, v.CacheWrite1h
	}
	if v.Input > 0 {
		u.Input = v.Input
	}
	if v.Output > 0 {
		u.Output = v.Output
	}
	if v.CacheRead > 0 {
		u.CacheRead = v.CacheRead
	}
	if v.CacheWrite > 0 {
		u.CacheWrite = v.CacheWrite
	}
	if v.CacheWrite1h > 0 {
		u.CacheWrite1h = v.CacheWrite1h
	}
	u.cacheCreationReported = u.cacheCreationReported || v.cacheCreationReported
	u.cacheUnknown = u.cacheUnknown || v.cacheUnknown
	u.searches = max(u.searches, v.searches)
	if v.Reasoning > 0 {
		u.Reasoning = v.Reasoning
	}
	if v.Served != "" {
		u.Served = v.Served
	}
	if v.Upstream != "" {
		u.Upstream = v.Upstream
	}
	if v.Tier != "" {
		u.Tier = v.Tier
	}
	if v.RequestID != "" {
		u.RequestID = v.RequestID
	}
	if v.ResponseID != "" {
		u.ResponseID = v.ResponseID
	}
	if v.ErrType != "" {
		u.ErrType = v.ErrType
	}
	if v.Stop != "" {
		u.Stop = v.Stop
	}
	if v.Deferred != nil {
		u.Deferred = v.Deferred
	}
	if v.Credits != nil {
		u.Credits = v.Credits
	}
}

// Result is a whole reply, for non-streaming clients.
type Result struct {
	ID               string
	Model            string
	Parts            []Part
	Stop             string
	Usage            Usage
	SafeguardResults json.RawMessage
	StopDetails      json.RawMessage
}

// collector assembles a Result from events. Encoders use the same logic to
// know what the reply contained so far.
type collector struct {
	res  Result
	args strings.Builder // arguments of the open tool call
	// text is what the part at textAt-1 has had added to its text since,
	// written into the part by flush: adding each delta to the part's
	// string copied the whole reply so far every time
	text   strings.Builder
	textAt int
	// fresh: a KThinkStart said the next reasoning is a block of its own
	fresh bool
	err   string
	// the error's status and kind, as its event gave them
	errStatus int
	errCode   string
	errSaid   json.RawMessage
}

func (c *collector) last(k Kind) *Part {
	if n := len(c.res.Parts); n > 0 && c.res.Parts[n-1].Kind == k {
		return &c.res.Parts[n-1]
	}
	return nil
}

// open is the reasoning the next of it goes on: the last part, when it is
// reasoning not sealed whole, and no new block has begun since.
func (c *collector) open() *Part {
	if c.fresh {
		return nil
	}
	if p := c.last(Thinking); p != nil && len(p.Sealed) == 0 {
		return p
	}
	return nil
}

// grow adds s to the text of the part at i, the last one.
func (c *collector) grow(i int, s string) {
	if c.textAt != i+1 {
		c.flush()
		c.text.WriteString(c.res.Parts[i].Text)
		c.textAt = i + 1
	}
	c.text.WriteString(s)
}

// flush writes the text grow gathered into its part.
func (c *collector) flush() {
	if c.textAt > 0 {
		c.res.Parts[c.textAt-1].Text = c.text.String()
		c.text = strings.Builder{}
		c.textAt = 0
	}
}

func (c *collector) closeTool() {
	if p := c.last(ToolCall); p != nil && p.Args == nil {
		s := strings.TrimSpace(c.args.String())
		if s == "" {
			s = "{}"
		}
		p.Args = json.RawMessage(s)
		c.args.Reset()
	}
}

func (c *collector) add(ev Event) {
	if len(ev.SafeguardResults) > 0 {
		c.res.SafeguardResults = ev.SafeguardResults
	}
	if len(ev.StopDetails) > 0 {
		c.res.StopDetails = ev.StopDetails
	}
	switch ev.Kind {
	case KStart:
		c.res.ID, c.res.Model = ev.MsgID, ev.Model
		c.res.Usage.add(ev.Usage)
	case KText:
		if p := c.last(Text); p != nil {
			c.grow(len(c.res.Parts)-1, ev.Text)
		} else {
			c.closeTool()
			c.res.Parts = append(c.res.Parts, Part{Kind: Text, Text: ev.Text})
		}
	case KThinkStart:
		c.fresh = true
		return
	case KThink:
		if p := c.open(); p != nil {
			c.grow(len(c.res.Parts)-1, ev.Text)
		} else {
			c.closeTool()
			c.res.Parts = append(c.res.Parts, Part{Kind: Thinking, Text: ev.Text})
		}
	case KSig:
		// a block signed with no text in it (Claude's thinking when its
		// display is omitted) is a block all the same (#1445)
		if p := c.open(); p != nil {
			p.Signature += ev.Text
			if ev.Name != "" {
				p.SignedBy = ev.Name
			}
		} else if c.fresh {
			c.closeTool()
			c.res.Parts = append(c.res.Parts, Part{Kind: Thinking, Signature: ev.Text, SignedBy: ev.Name})
		}
	case KSealed:
		// a Responses item's summary came before it as reasoning of its
		// own: the item is that reasoning, sealed
		if p := c.open(); p != nil && ev.Name == sealResponses && p.Signature == "" {
			p.Sealed, p.SealedBy = json.RawMessage(ev.Text), ev.Name
		} else {
			c.closeTool()
			c.res.Parts = append(c.res.Parts, Part{Kind: Thinking, Sealed: json.RawMessage(ev.Text), SealedBy: ev.Name})
		}
	case KTextSig:
		if p := c.last(Text); p != nil && p.Signature == "" {
			p.Signature = ev.Text
		} else {
			c.closeTool()
			c.res.Parts = append(c.res.Parts, Part{Kind: Text, Signature: ev.Text})
		}
	case KToolStart:
		c.closeTool()
		c.res.Parts = append(c.res.Parts, Part{Kind: ToolCall, ID: ev.ID, Name: ev.Name})
	case KToolArgs:
		c.args.WriteString(ev.Text)
	case KStop:
		c.closeTool()
		c.res.Stop = ev.Stop
	case KUsage:
		c.res.Usage.add(ev.Usage)
		c.res.Usage.add(Usage{RequestID: ev.RequestID})
		if ev.Model != "" {
			c.res.Model = ev.Model // another model went on with the reply
		}
	case KError:
		c.err, c.errStatus, c.errCode, c.errSaid = ev.Text, ev.Status, ev.Code, ev.Said
	case KSearch:
		c.closeTool()
		c.res.Parts = append(c.res.Parts, Part{Kind: Search, Text: ev.Text, Hits: ev.Hits})
	case KImage:
		c.closeTool()
		c.res.Parts = append(c.res.Parts, Part{Kind: Image, MediaType: ev.Name, Data: ev.Text})
	}
	switch ev.Kind {
	case KStart, KUsage, KError, KAlive:
	default:
		// what began has had its first content, or something else came
		c.fresh = false
	}
}

func (c *collector) finish() Result {
	c.flush()
	c.closeTool()
	// a reply of tool calls ended "stop" (a relay's end_turn beside its
	// tool_use blocks, 蓝猫 on Discord) is the client's to run the calls
	// of: told it stopped, an agent ends its turn there
	if c.res.Stop == "" || c.res.Stop == "stop" {
		c.res.Stop = "stop"
		for _, p := range c.res.Parts {
			if p.Kind == ToolCall {
				c.res.Stop = "tool"
			}
		}
	}
	return c.res
}

// saidAnything reports whether a reply has more than thinking: text, a
// call, a search or an image. A turn that only thought and then failed is
// the failure, not an answer with nothing in it.
func saidAnything(parts []Part) bool {
	for _, p := range parts {
		if p.Kind != Thinking {
			return true
		}
	}
	return false
}

// argsOf is a tool call's arguments as a JSON object, never empty.
func argsOf(p Part) json.RawMessage {
	if len(p.Args) == 0 || strings.TrimSpace(string(p.Args)) == "" {
		return json.RawMessage("{}")
	}
	return p.Args
}

// argsString is the same as a string, for the APIs that want one.
func argsString(p Part) string { return string(argsOf(p)) }

// parseArgs turns the string form of arguments into an object; a string
// that is not JSON is wrapped so nothing is lost.
func parseArgs(s string) json.RawMessage {
	s = strings.TrimSpace(s)
	if s == "" {
		return json.RawMessage("{}")
	}
	if json.Valid([]byte(s)) && strings.HasPrefix(s, "{") {
		return json.RawMessage(s)
	}
	b, _ := json.Marshal(map[string]string{"input": s})
	return b
}

// text joins the text parts of a message.
func text(parts []Part) string {
	var b strings.Builder
	for _, p := range parts {
		if p.Kind == Text {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// systemTexts is each block of the system prompt as the client sent it
// (systemParts, systemRaw).
func (r *Request) systemTexts() []string {
	out := slices.Clone(r.systemParts)
	for _, raw := range r.systemRaw {
		var s string
		if decodeJSON(raw, &s) == nil {
			if s != "" {
				out = append(out, s)
			}
			continue
		}
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if decodeJSON(raw, &blocks) == nil {
			for _, b := range blocks {
				if b.Text != "" && (b.Type == "text" || b.Type == "input_text") {
					out = append(out, b.Text)
				}
			}
		}
	}
	return out
}

// lastUserTexts is each text of the request's last user message as the
// client sent it, the agent's reminders and all; none when that message
// holds only tool results.
func lastUserTexts(r *Request) []string {
	for i := len(r.Messages) - 1; i >= 0; i-- {
		if r.Messages[i].Role != "user" {
			continue
		}
		var out []string
		for _, p := range r.Messages[i].Parts {
			if p.Kind == Text && p.Text != "" {
				out = append(out, p.Text)
			}
		}
		return out
	}
	return nil
}

// stringOrText reads a JSON value that is either a string or an array of
// {type:"text", text} blocks (Anthropic's system, tool results…).
func stringOrText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if decodeJSON(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ToolName string `json:"tool_name"`
	}
	if decodeJSON(raw, &blocks) == nil {
		var b strings.Builder
		for _, x := range blocks {
			switch x.Type {
			case "text", "input_text", "output_text":
				b.WriteString(x.Text)
			case "tool_reference":
				// Claude Code's ToolSearch loads a deferred tool by naming
				// it; every tool is already offered to a model elsewhere, so
				// it is told the tool is there, not handed an empty result
				if b.Len() > 0 {
					b.WriteString("\n")
				}
				b.WriteString(toolLoadedText(x.ToolName))
			}
		}
		return b.String()
	}
	return ""
}

// toolOutput reads a tool's result: its text, as stringOrText reads it,
// and the images in it. Anthropic's tool_result holds image blocks, a
// Responses function_call_output input_image parts and a Chat tool message,
// from clients that send them, image_url parts. An image named only by a
// vendor's file id has nothing to carry and is left out.
func toolOutput(raw json.RawMessage) (string, []Part) {
	text := stringOrText(raw)
	var blocks []struct {
		Type   string `json:"type"`
		Source *struct {
			MediaType string `json:"media_type"`
			Data      string `json:"data"`
			URL       string `json:"url"`
		} `json:"source"`
		ImageURL json.RawMessage `json:"image_url"`
		Detail   string          `json:"detail"` // a Responses input_image's
	}
	if len(raw) == 0 || raw[0] != '[' || decodeJSON(raw, &blocks) != nil {
		return text, nil
	}
	var images []Part
	for _, b := range blocks {
		switch b.Type {
		case "image":
			if s := b.Source; s != nil && s.Data != "" {
				images = append(images, Part{Kind: Image, MediaType: s.MediaType, Data: s.Data})
			} else if s != nil && s.URL != "" {
				images = append(images, Part{Kind: Image, URL: s.URL})
			}
		case "input_image", "image_url":
			var u string
			detail := b.Detail
			if decodeJSON(b.ImageURL, &u) != nil {
				var o struct {
					URL    string `json:"url"`
					Detail string `json:"detail"`
				}
				decodeJSON(b.ImageURL, &o)
				u, detail = o.URL, o.Detail
			}
			if u != "" {
				im := imagePart(u)
				im.Detail = detail
				images = append(images, im)
			}
		}
	}
	return text, images
}

// effortOf normalises the reasoning effort names the APIs use.
func effortOf(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "minimal", "none":
		return "low"
	case "low", "medium", "high", "xhigh", "max":
		return strings.ToLower(s)
	case "ultra": // Codex's max, with its own agents to hand work to
		return "max"
	}
	// a vendor's own word for a level (Trae's light, extra_high), which
	// an agent sends for a model listing it; fitted back to the model's
	// own word on the way out (fitEffort)
	if l, ok := catalog.EffortAliases[strings.ToLower(strings.TrimSpace(s))]; ok {
		return l
	}
	return ""
}

// effortRank orders the reasoning levels agents and vendors name.
var effortRank = catalog.EffortRank

// ByStrength is efforts weakest first, in effortRank's order; a level it
// doesn't know keeps its place among the others of its kind, after them.
func ByStrength(efforts []string) []string {
	rank := func(e string) int {
		if i := slices.Index(effortRank, e); i >= 0 {
			return i
		}
		return len(effortRank)
	}
	out := slices.Clone(efforts)
	slices.SortStableFunc(out, func(a, b string) int { return rank(a) - rank(b) })
	return out
}

// fitEffort is the level of the model's own nearest the one asked for — a
// tie goes up — or the one asked for when the model's aren't known. Codex
// asks "medium" of a model it was given no levels for, and an agent's
// setting can outlive the model it was picked for; GLM-5.3 takes low, high
// and max only.
//
// Codex's ultra is no API's level, whatever a list says (ChatGPT's lists
// it for Codex's picker; magpie offers it on Copilot's gpt-6.1-sol, #656):
// it is sent as max, or the nearest the model has below it.
func fitEffort(want string, levels []string) string {
	if want == "ultra" {
		want = "max"
		levels = slices.DeleteFunc(slices.Clone(levels), func(l string) bool { return l == "ultra" })
	}
	return catalog.NearestEffort(want, levels)
}

// budgetOf is the Anthropic thinking budget for an effort level.
func budgetOf(effort string) int {
	switch effort {
	case "low":
		return 4096
	case "medium":
		return 10000
	case "high":
		return 24000
	case "xhigh", "max":
		return 32000
	}
	return 10000
}

// ThinkingBudget is the Anthropic thinking budget the gateway reads an
// effort level as, for an agent's config that has to name one (ZCode's).
func ThinkingBudget(effort string) int { return budgetOf(effortOf(effort)) }

// effortOfBudget goes the other way, for Anthropic clients asking others.
func effortOfBudget(n int) string {
	switch {
	case n <= 0:
		return ""
	case n <= 4096:
		return "low"
	case n <= 12000:
		return "medium"
	case n <= 24000:
		return "high"
	}
	return "xhigh"
}
