// Package provider holds the model vendors magpie can reach: where each one
// lives, which protocols it speaks, the API key the user typed in, and which
// of its models should show up in the agents' pickers.
//
// Provider keys are never read from environment variables. A provider is
// exactly what the user entered, kept in ~/.config/magpie/providers.json
// (mode 0600).
package provider

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/lastgood"
	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/steady"
)

// Protocol is a wire API magpie can speak to an upstream.
type Protocol string

const (
	Chat      Protocol = "chat"      // OpenAI Chat Completions
	Responses Protocol = "responses" // OpenAI Responses
	Anthropic Protocol = "anthropic" // Anthropic Messages
	Gemini    Protocol = "gemini"    // Google Gemini's generateContent: served to clients; spoken upstream at a custom provider's Gemini URL, Vertex AI and Factory's generate route
)

// Protocols in the order magpie prefers them when it has to translate.
var Protocols = []Protocol{Chat, Responses, Anthropic}

// Provider is one configured vendor.
type Provider struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Was are ids the provider had before it was renamed (see Rename):
	// a model still picked by one of them reaches it.
	Was []string `json:"was,omitempty"`
	// Gave are ids it had that another provider took after it (a rename
	// onto one, a provider added under one), each with when: the calls
	// recorded under such an id before then were this one's (Renames).
	Gave []Gave `json:"gave,omitempty"`
	Icon string `json:"icon,omitempty"`
	// IconFrom is the picture's address a custom provider's Icon was
	// fetched from in its editor, shown there again (hoobnn): only a
	// record, never fetched again
	IconFrom string `json:"iconFrom,omitempty"`
	Preset   string `json:"preset,omitempty"` // preset this was created from, if any
	Key      string `json:"key"`              // API key, as typed by the user

	// AccessKeyID and SecretAccessKey are a Volcengine account's access
	// key (TakesVolcAccessKey), what Ark tells its Coding or Agent Plan's
	// windows to (volcengine_usage.go), or a Tencent Cloud account's API
	// key, its SecretId and SecretKey (TakesTencentAccessKey), what TokenHub
	// tells Token Plan 企业版's usage to (tencent_usage.go); not the plan's
	// inference key in Key. They are kept, backed up and synced as the keys are: the Secret
	// is never sent to the GUI, and a backup without keys has neither.
	AccessKeyID     string `json:"accessKeyID,omitempty"`
	SecretAccessKey string `json:"secretAccessKey,omitempty"`

	// KeyName names the key in use, and Keys are the provider's other
	// accounts: keys saved to switch to (see keys.go).
	KeyName string       `json:"keyName,omitempty"`
	Keys    []KeyAccount `json:"keys,omitempty"`
	// KeyProtocol, when set, is the one protocol the first key is good
	// for: a relay that hands out one key for Anthropic and another for
	// OpenAI (see KeyAccount.Protocol).
	KeyProtocol Protocol `json:"keyProtocol,omitempty"`
	// KeyWeight is the first key's weight (see KeyAccount.Weight).
	KeyWeight int `json:"keyWeight,omitempty"`
	// KeyPlan is the first key's plan (see KeyAccount.Plan).
	KeyPlan string `json:"keyPlan,omitempty"`

	// Base URLs, one per protocol the vendor serves natively. magpie appends
	// the usual paths: chat/responses bases end in /v1 (OpenAI style),
	// the Anthropic base is the root (what ANTHROPIC_BASE_URL takes).
	Chat      string `json:"chat,omitempty"`
	Responses string `json:"responses,omitempty"`
	Anthropic string `json:"anthropic,omitempty"`
	// Gemini is the base of Google's Gemini API, or of one that answers as
	// it does (…/v1beta): models/{model}:streamGenerateContent is asked
	// under it, with the key in x-goog-api-key, and models under it lists
	// them (#1346).
	Gemini string `json:"gemini,omitempty"`
	// GeminiFilled is that magpie filled Gemini in once, for a provider
	// at AI Studio's OpenAI-compatible URL (aiStudioNative): a Gemini URL
	// the user cleared after that stays empty.
	GeminiFilled bool `json:"geminiFilled,omitempty"`
	// Decide is the base of a decision API (TypeSafe's System One, which
	// Jev answers), for routing groups' choices of model and effort. The
	// provider may also serve conversations on the other endpoints.
	Decide string `json:"decide,omitempty"`
	// BaseAPI is the API the user gave a custom provider's Base URL as,
	// in its editor: "chat", "responses", "anthropic", "gemini" or "decide". The
	// editor shows that pick again, where it would otherwise show the
	// first API with a URL (01huadalang: Responses picked and saved came
	// back as OpenAI compatible). Requests go by which URLs are set.
	BaseAPI string `json:"baseAPI,omitempty"`

	// Fallback is where a request goes when this provider can't take it —
	// out of quota, rate limited, overloaded or down — before any of the
	// reply has been sent: models as agents pick them (provider/model),
	// tried in order.
	Fallback []string `json:"fallback,omitempty"`

	// Routing is how requests spread over the keys or accounts it has on:
	// "" smart, the first while it has quota to spare, then whichever has
	// the most; "order" in order, the next one only when the one before
	// can't take it; "rotate" each in turn; "usage" the least used first;
	// "pace" the one with the most remaining allowance per hour until
	// its window resets first, so less allowance is lost at reset.
	// The window can be shorter than a week. "weight" by each key's
	// weight, a key with 3 taking three requests to one with 1's.
	// "drain" one until it is used up, then the next, which keeps them
	// when an earlier one renews (routes.Drain). Whichever it is, one out
	// of credit, out of quota, rate limited or failing is passed over for
	// as long as that lasts.
	Routing string `json:"routing,omitempty"`

	// Sink sends a key or account that a 429 rate limited while it still
	// had quota to the back of the order Routing gives (01huadalang): it
	// is tried again only after every one not rate limited since — so the
	// load goes round, rather than back to the first each time its rest
	// ends. Off, the default, it keeps its place. Not for "rotate", which
	// goes round already. See gateway.sink.
	Sink bool `json:"sink,omitempty"`

	// UseFirst puts its keys or accounts ahead of the other providers'
	// where Smart weighs them together: a group of several providers'
	// models (hzh_1119 on Discord: an OpenAI API key's monthly credit
	// spent before the ChatGPT subscription serving the same models). One
	// resting after a failure, out of credit or out of quota (a 429
	// insufficient_quota) is passed over as ever, and one all but used up
	// goes with the others that are. Off, the default, Smart goes as it
	// always has (routes.Seat.Lead).
	UseFirst bool `json:"useFirst,omitempty"`

	// NoStandIn keeps another model's request off it (sonnnnnnnnnnnna on
	// X): a request that went first to a provider not so marked, such as
	// a local uncensored model that is down or out of room, is never sent
	// on to it as a fallback or a routing group's next member, so text
	// written for that model can't get a vendor's account banned. One
	// that goes first to it, or to another provider so marked, still
	// moves on as ever. Off, the default, it stands in for any. See
	// gateway.standInsOut.
	NoStandIn bool `json:"noStandIn,omitempty"`

	// Affinity is how long a conversation stays with the key or account
	// that answered it, so the vendor's prompt cache it filled is read
	// again rather than lost (see Affinities): "" auto, "session",
	// "turn", "off", "strict".
	Affinity string `json:"affinity,omitempty"`

	// KeepLogin, on Codex's or Claude Code's subscription, keeps the agent
	// signed in to the account the user made first: magpie doesn't sign it
	// in to another when that one runs low or out (#524). The gateway still
	// spreads its requests over the accounts that are on, as Routing says.
	KeepLogin bool `json:"keepLogin,omitempty"`
	// KeepLoginAs, with KeepLogin, is the account the agent is kept signed
	// in to whichever is first in the order the gateway tries them: the one
	// the user uses the agent as, while the first is only the one whose
	// allowance is spent first (#524). Empty, it is the first.
	KeepLoginAs string `json:"keepLoginAs,omitempty"`
	// SwitchOnly, on Codex's subscription, has magpie only move Codex's
	// own sign-in (auth.json) to the next account with room and back:
	// Codex's requests then go straight to OpenAI, never through the
	// gateway for account failover, however many of its accounts are on
	// (#1907). On Claude's subscription it keeps the accounts to Claude
	// Code's own sign-in, which magpie still moves: the gateway serves no
	// request on them (KeptToAgent; myduso on Discord).
	SwitchOnly bool `json:"switchOnly,omitempty"`
	// RestartApp, on Codex's subscription, has magpie restart the Codex
	// app's own app-server for a change it makes by itself that the app
	// works without: the switch back once the account it moved off has
	// room (SwitchWhenSpent), and giving Codex its account back at the
	// reset. The ChatGPT app shows an ended app-server as an error and
	// relaunches (#4550), so it is off unless the user turns it on
	// (atom0964 on Discord); the app then stays on its account until it
	// is restarted, from the switch notice or by hand. A switch the user
	// makes, a Connect or Disconnect, and whatever leaves the app on a
	// used-up account (a switch off it, becoming its provider for it)
	// restart it either way: the app greys Send there (8b0132b3d).
	RestartApp bool `json:"restartApp,omitempty"`
	// KeepSelf, on a Remote magpie that turned out to be this magpie
	// (SelfAnswered), keeps it in routing anyway: left alone, such a
	// provider is left out (SelfLeftOut). It is this computer's own, as
	// sync's copy of the provider on the other computer points at a
	// magpie that is not that one (hanslin1666 on Discord).
	KeepSelf bool `json:"keepSelf,omitempty"`

	// MaxConcurrency is how many requests may be out at the vendor at once
	// on each of its keys or accounts (Discord, Lemon: a Codex account is
	// risk-controlled past five or six at once); the rest wait their turn,
	// in the order they came (see Concurrency). nil follows what a
	// plugin's provider says it takes, else none; 0 is no limit.
	MaxConcurrency *int `json:"maxConcurrency,omitempty"`
	// MaxAccounts is how many of its accounts or keys take requests at
	// the same time (01huadalang on Discord: ZCode's firewall blocks the
	// address that three of its accounts ask from at once, and two are
	// fine). Routing goes over the first that many it would ask, in its
	// order and not resting; the others wait as reserves until one of
	// those rests (out of quota, rate limited, failing). nil follows what
	// a plugin's provider says, else none; 0 is no limit (see AtOnce).
	MaxAccounts *int `json:"maxAccounts,omitempty"`
	// AccountConcurrency is, for one account or key, its own limit over
	// MaxConcurrency (#892): an account by its name in lower case, a key
	// by its KeyID; 0 there is no limit for it. One not in it takes the
	// provider's (see LaneLimit).
	AccountConcurrency map[string]int `json:"accountConcurrency,omitempty"`
	// AccountWeights is, for a subscription, each account's weight under
	// Weighted routing, by its name in lower case (rubbzha on X: a Max
	// account and a Pro one shared by how much more one allows); one not
	// in it weighs 1. A key's weight is its own (KeyAccount.Weight).
	AccountWeights map[string]int `json:"accountWeights,omitempty"`
	// QueueLimit is how many requests may wait for a slot of each key or
	// account at once (#892); one more is turned away as the queue being
	// full. 0 is no bound.
	QueueLimit int `json:"queueLimit,omitempty"`
	// QueueWait is how long, in seconds, a request waits for a slot before
	// it is turned away as having waited too long (#892). 0 waits as long
	// as it takes.
	QueueWait int `json:"queueWait,omitempty"`
	// MaxRPM is how many requests a minute each of its keys or accounts
	// sends the vendor, over a rolling 60 seconds (coeo91 on Discord:
	// OpenRouter's free models take 20 a minute, which a limit at once
	// can't keep to); one more waits for room (see RPMLimit). 0 is no
	// limit.
	MaxRPM int `json:"maxRPM,omitempty"`
	// QuotaRest is how many minutes one of its keys sits out once the
	// vendor says it is out of its allowance without saying when it is
	// back (#1772: Ollama Cloud's weekly limit); 0 is the gateway's own
	// quarter of an hour. A time the vendor names stands over it.
	QuotaRest int `json:"quotaRest,omitempty"`
	// QuotaResetAt is the time of day a key of it out of its allowance is
	// back, when its vendor refuses for a day's allowance without saying
	// when (#2008: a free tier's "today's free-model token quota"): "08:00"
	// on this computer's clock, or "00:00+08:00" on the vendor's. Set, it
	// stands over QuotaRest; a time the vendor names stands over both.
	QuotaResetAt string `json:"quotaResetAt,omitempty"`
	// RestCap is the most, in minutes, one of its keys or accounts sits
	// out while it keeps failing or keeps being rate limited as soon as
	// its rest is over (#2008); 0 is the gateway's own, 10 minutes for a
	// failure and 30 for a rate limit. The vendor's own "try again in"
	// still stands over it. RestCapOff (-1): no rest at all after a
	// failure that says nothing of why, a rate limit's as the gateway's own.
	RestCap int `json:"restCap,omitempty"`
	// LimitRestOff keeps its keys and accounts asked when the vendor says
	// they are out of quota or answers 429 (zoecats on Discord: a local
	// aggregator says "quota exhausted" for a moment while it switches
	// upstreams, and a rest of half an hour held every request back until
	// magpie restarted). Off by default: such a refusal rests it as ever.
	LimitRestOff bool `json:"limitRestOff,omitempty"`

	// PriceRate is what the provider charges against the official price
	// (ITea312, #819): a relay that bills 0.8× or 1.5× of it. It scales the
	// provider's list price, else its maker's, wherever a cost is counted;
	// a price the user set for one of its models stands as set. 0 is the
	// price as listed; at most three decimals (PriceRateOK).
	PriceRate float64 `json:"priceRate,omitempty"`
	// CountFree is the user's word that the provider costs them nothing
	// (jiabubu on Discord: ModelScope's free daily quota): every model of
	// it counts as free where routing weighs that, as a model its vendor
	// lists free does (FreeModel: Free first spends it first, Cheapest
	// weighs it at nothing). Prices and the costs Usage counts stay as
	// listed: it says what to spend first, not what a call cost.
	CountFree bool `json:"countFree,omitempty"`
	// Billing is the user's word on how the provider bills: BillingPlan
	// or BillingPAYG (EV_John_Liu on X); "" has magpie read it
	// (BillingKind). It only marks the provider on Providers and Routing.
	Billing string `json:"billing,omitempty"`
	// RateWindows are the time-of-day windows it bills at its own rate
	// (#2463): a peak at 2× or 3×, a discount off peak. The first window
	// that holds a call's time multiplies the price again, on top of
	// PriceRate; outside every window the price rate alone counts. At
	// most RateWindowsMax of them (CheckRateWindows).
	RateWindows []RateWindow `json:"rateWindows,omitempty"`

	// Headers are extra HTTP request headers sent to the vendor, as the user
	// typed them but for ${sessionId} and ${threadId} in a value, the
	// request's conversation ids (SentHeaders, conversation.go). They ride on every request magpie makes to a plain
	// key+URL provider — forwarded calls, connectivity tests, and model-list
	// fetches — applied after the auth headers, so the user can override those
	// when a gateway insists on a private scheme. Signed-in agent accounts
	// ignore them: their auth is the agent's own.
	Headers map[string]string `json:"headers,omitempty"`

	// ExtraBody are JSON fields added to the body of every request to the
	// vendor (#1604), filling in what the body lacks, or replacing it for a
	// name written "!name"; ${sessionId} and ${threadId} in a value are the
	// conversation's ids (extrabody.go, conversation.go). Signed-in agent accounts ignore it, as Headers.
	ExtraBody ExtraBody `json:"extraBody,omitempty"`

	// ReplyHeaders are headers of the vendor's reply passed on to the
	// agent, beside DefaultReplyHeaders, whatever their case (#4145): a
	// relay's own request id (X-Oneapi-Request-Id), for the agent's side
	// to match with the relay's logs. Streamed or whole, an error too, on
	// every API, translated or not; only the reply the agent is given
	// has its headers passed on. One magpie writes itself, or that would
	// act on magpie's address, never is (ReplyHeaderPassable).
	ReplyHeaders []string `json:"replyHeaders,omitempty"`

	// PassHeaders are headers of the agent's request passed on to the
	// vendor as it sent them, whatever their case (3jRzfF52Qx on X): a
	// value that changes per request or per client, which a fixed Header
	// can't give. The client's stands over a Header of the same name;
	// with none sent the Header goes. Every API, count_tokens, a group's
	// member, a plugin's fetch; a signed-in account's signing has the
	// last word. One that carries magpie's key or the connection's own
	// never is (RequestHeaderPassable).
	PassHeaders []string `json:"passHeaders,omitempty"`

	// Searches says the vendor answers a web search tool offered on its
	// Anthropic or Responses API by itself (web_search_20250305,
	// web_search): a relay in front of Anthropic's or OpenAI's API, which
	// magpie can't tell from its host. A request offering one then goes to
	// it as the client sent it, rather than given magpie's search (#359).
	Searches bool `json:"searches,omitempty"`

	// CodexAsSent has a request Codex sends on the Responses API go to it
	// as Codex sent it, its model name alone swapped: a relay that checks
	// it is Codex's own (#1758: "Codex 客户端请求特征无效", 10014), which
	// the shape magpie fits it to for another backend (Responses Lite's
	// additional_tools lifted into tools, the tool search as a function,
	// OpenAI-only fields left out) fails. Off, the default, keeps that
	// fitting, which a backend that isn't OpenAI's needs (#350).
	CodexAsSent bool `json:"codexAsSent,omitempty"`

	// ImageFilesOff, `image-files=off`, has DeepSeek's own API sent an
	// agent's inline images inline on every turn. By default each goes up
	// once through DeepSeek's Files API, for a day, and the turns name it
	// by its file id (gateway's deepseek_files.go, #5407).
	ImageFilesOff bool `json:"imageFilesOff,omitempty"`

	// UpstreamProviders, on a Remote magpie, `upstream-providers=on`, names each
	// of its models in the agents' lists by the provider serving it on
	// the other magpie, the first in a chain of them: "Claude Sonnet 5 ·
	// Office › RelayA", whatever that magpie's own Provider in model names
	// is, so one model from two of its providers is told apart here
	// (lcc580 on X). Off, the default, names each as that magpie's list
	// does. The ids stay as they are: office/relaya/claude-sonnet-5.
	UpstreamProviders bool `json:"upstreamProviders,omitempty"`

	// PromptAsWritten, on Antigravity's subscription, has the system prompt
	// go to Google exactly as the agent wrote it. By default the few lines
	// Google answers with a 429 "Resource has been exhausted" whatever
	// quota is left (Claude Code's billing line, the Claude Agent SDK's
	// identity line, Codex's GPT-5 identity lines, Hermes Agent's earlier
	// default persona) go with a zero-width space inside them, which Google
	// lets through and the model reads the same (gateway's
	// antigravityCloak). Set with `magpie provider set antigravity
	// prompt=as-written`; prompt=default turns it back.
	PromptAsWritten bool `json:"promptAsWritten,omitempty"`

	// AdditionalTools says the provider's Responses API takes an
	// additional_tools input item, as OpenAI's does: a deferred tool an
	// Anthropic client's tool search loaded goes to it in such an item
	// where the search loaded it, not in the top-level tools, which keeps
	// the prompt's cached prefix (#2660). OpenAI's own API and a ChatGPT
	// (Codex) account are sent one without it (#4738: the ChatGPT backend
	// takes the item where it was loaded, and its cache is kept); another
	// vendor isn't unless this is set, since xAI's turns the item away
	// (#350).
	AdditionalTools bool `json:"additionalTools,omitempty"`
	// AdditionalToolsOff, `additional-tools=off`, sends a loaded tool in
	// the top-level tools even where magpie's default would send the item.
	AdditionalToolsOff bool `json:"additionalToolsOff,omitempty"`

	// PassErrors, `errors=pass`, is the user's word that the client, not
	// magpie, deals with what the provider's errors say (ne9ko9 on
	// Discord): a request to it is asked once, its error goes to the
	// agent as it came (its status, and its body as written where the
	// agent's API is the provider's own, else in the agent's shape with
	// the vendor's type, code and words), nothing about it is inferred
	// (a prompt too long, a busy relay), it is not asked again in place
	// or of a fallback or a group's next member, and none of its keys
	// rests for it. Off, the default, magpie handles them. Only a provider
	// of a key takes it (TakesPassErrors).
	PassErrors bool `json:"passErrors,omitempty"`

	// Patience is how many seconds more a request to this provider alone,
	// not through a group, may be kept once it has failed in a way that
	// passes (overloaded, unavailable, rate limited for a moment) and the
	// same-one retries are spent, as a group's Patience is (zoecats on
	// Discord): 0 keeps it PatienceDefault, PatienceOff hands the agent
	// the error at once, and it is at most PatienceLongest. Set with
	// `magpie provider set <id> patience=`; a group it is in keeps the
	// group's own.
	Patience int `json:"patience,omitempty"`

	// Socket has a Responses API provider's turns go over the Responses
	// WebSocket magpie speaks to chatgpt.com (#5781, matrix0487 on
	// Discord): SocketAuto tries it and falls back to HTTP, and leaves the
	// host to HTTP for a while once it refuses the handshake, as
	// chatgpt.com is; SocketOn tries it every time and falls back without
	// that. "", the default, is HTTP alone. Only a provider of a key with a
	// Responses URL takes it (TakesSocket). Set with `magpie provider set
	// <id> socket=`.
	Socket string `json:"socket,omitempty"`

	// NativeCompaction has Codex's compaction_trigger go to the provider
	// as Codex sent it, and the compaction the provider writes back go to
	// Codex as it came (#1099), where magpie otherwise asks the model for
	// a summary and hands Codex that (its "magpie1:" compaction). Only a
	// provider whose wire carries it takes it (CarriesCompaction): a
	// Responses API (Sub2API, a remote magpie), a ChatGPT account, GitHub
	// Copilot's GPT models. Such a compaction is sealed by the key or
	// account that wrote it, so its conversation stays there, or is
	// refused, never sent elsewhere to be dropped. Off, the default,
	// keeps magpie's summary.
	NativeCompaction bool `json:"nativeCompaction,omitempty"`

	// MagpieUserAgent has every request go to the vendor with magpie's own
	// User-Agent (magpie/<version>). Off, the default, a key+URL provider is
	// sent the User-Agent the agent gave (pi, opencode, Claude Code…) as it
	// gave it (#1851, nb5p on Discord): a coding plan that serves only
	// agents it names, or the user's own gateway behind magpie, knows them
	// by it. One the agent didn't give, and magpie's own requests, go as
	// magpie either way. A signed-in account, a plugin's and one that goes
	// as a client of its vendor's own keep the identity their signing
	// gives (see PassesUserAgent). The opt-in "passUserAgent" it replaces
	// is the default now, so a file that still says it reads the same.
	MagpieUserAgent bool `json:"magpieUserAgent,omitempty"`

	// PinUpstream, on the Cline API, has its DeepSeek models answered only
	// by DeepSeek's own API (White Immortal on Discord) and its GLM models
	// only by Z.ai's (sdytman on Discord): Cline routes a model through an
	// AI gateway that may serve it from any host, and the maker's own
	// keeps its prompt cache. Since 2026-11-06 Cline refuses the
	// providerOptions field that pinned all but glm-5.3-flash, so only
	// that model is pinned now (#5442); the saved value is kept as set.
	// See ClinePin.
	PinUpstream bool `json:"pinUpstream,omitempty"`

	// AuthHeader is which header the key goes in on a third-party
	// Anthropic API, which is sent both x-api-key and Authorization:
	// Bearer by default (AuthBoth). A vendor that refuses the two together
	// (Aloha on Discord: "x-api-key 和 Authorization 不可以同时存在") is
	// sent one, AuthXAPIKey or AuthBearer: picked in the editor, or
	// learned from that refusal (RetryOneAuth). Anthropic's, Bedrock's and
	// Azure's own APIs, which take x-api-key alone, ignore it.
	AuthHeader string `json:"authHeader,omitempty"`

	// Unredacted has requests to a provider go as the agent wrote them,
	// unmasked by Settings' redaction, which keeps secrets from vendors:
	// for a model on this machine or the local network (Ollama, LM Studio,
	// a vLLM box; lc on Discord). It is the user's word, not the
	// address's (AcMilan on Discord), on a subscription or a plugin's
	// provider too (isee1_1 on Discord): the editor cautions while it is
	// on for one not local, and a relay run locally, or Ollama's cloud
	// models, pass a request on to a vendor whatever the address. A
	// signed-in account keeps it with its model picks (withPicks). See
	// SkipsRedaction.
	Unredacted bool `json:"unredacted,omitempty"`

	// InsecureTLS has magpie take the certificate of the provider's own
	// HTTPS host as it is, unchecked (#2383, fangzhengjin: a vLLM on the
	// LAN, Ollama behind a self-signed cert). Only a provider the user
	// added takes it (TakesInsecureTLS), and only for the hosts of its own
	// base URLs (UncheckedHosts); see Via.
	InsecureTLS bool `json:"insecureTLS,omitempty"`

	// HTTP1 has magpie speak HTTP/1.1 to the provider's own hosts, never
	// HTTP/2 (kepner_53726 on Discord: a company gateway whose HTTP/2
	// connections broke mid-reply with "tls: bad record MAC" while
	// HTTP/1.1 to it held). Only a provider the user added takes it
	// (TakesInsecureTLS), for the hosts of its own base URLs
	// (UncheckedHosts); see Via.
	HTTP1 bool `json:"http1,omitempty"`

	// ClientCert and ClientKey are the PEM files of a client certificate
	// magpie shows the provider's own HTTPS hosts when they ask for one,
	// and ServerCA a PEM CA their certificate may be signed by beside the
	// system's roots (#5596, clxsh, and hhdaxia on X: Remote magpie to a
	// magpie at home behind an Nginx that requires a client certificate,
	// or one with `magpie mtls` on). Only a provider the user added, and
	// Remote magpie, takes them (TakesClientCert), for the hosts of its
	// own base URLs (UncheckedHosts), on every request made for it:
	// models, the connection test, requests, quotas; see Via.
	ClientCert string `json:"clientCert,omitempty"`
	ClientKey  string `json:"clientKey,omitempty"`
	ServerCA   string `json:"serverCA,omitempty"`

	// ServerPin is the SHA-256 (lower-case hex) of the one server
	// certificate its own HTTPS hosts are taken with, trusted as it is
	// (Zhangzhangi on X: a magpie at home on a domain of its own with a
	// self-signed certificate, which macOS calls not standards
	// compliant): the certificate's chain, name and dates aren't checked
	// there, and any other certificate is refused. "" checks them as
	// usual. Taken where TakesClientCert is; see Via and PeekServerCert.
	ServerPin string `json:"serverPin,omitempty"`

	// Proxy is the proxy magpie's requests to this provider go through
	// (#237: Codex through one, a vendor at home without): "" follows
	// the global one (Settings' Proxy, the environment's, the system's),
	// "direct" none, anything else the proxy's address (http://, https://,
	// socks5://; host:port means http). Signed-in accounts keep it too.
	Proxy string `json:"proxy,omitempty"`
	// AccountProxies is, for a provider holding several accounts or keys
	// (Codex's accounts, a relay's keys…), the proxy of each one that has
	// one of its own, as Proxy takes one: an account by its name in lower
	// case, a key by its KeyID. One not in it follows Proxy (see
	// ProxyChoice).
	AccountProxies map[string]string `json:"accountProxies,omitempty"`
	// AccountModels is, for a provider holding several accounts or keys,
	// the models each one the user narrowed serves, and no others (#474):
	// an account by its name in lower case, a key by its KeyID. One not in
	// it serves every model the provider does (see account_models.go).
	AccountModels map[string][]string `json:"accountModels,omitempty"`
	// AccountModelsAll is the keys, by KeyID, the user set back to every
	// model (All models, or `accountmodels` with none): a Fetch models
	// that ticks a key's own list for it (key_models_refresh.go) leaves
	// these as the user set them.
	AccountModelsAll map[string]bool `json:"accountModelsAll,omitempty"`
	// AccountCaps is, for a subscription, the share (1–99) of its usage
	// windows each account the user capped is used to at most, by its name
	// in lower case: past it, routing takes the account for used up until
	// the window renews (see account_caps.go). One not in it has no cap.
	AccountCaps map[string]int `json:"accountCaps,omitempty"`
	// AccountWindowCaps is, for a subscription, the share of each usage
	// window an account is used to at most where the user set one for that
	// window apart (willz on Discord), by the account's name in lower case
	// and then the window's WindowCapID: 1–99, or 100 for none on it. A
	// window not in it takes the account's AccountCaps.
	AccountWindowCaps map[string]map[string]int `json:"accountWindowCaps,omitempty"`
	// AccountWindowAmounts is, for a subscription, the fixed amount of a
	// window's own unit (credits, requests, tokens) an account is used to
	// at most where the user set one in place of a share (9won_25273 on
	// Discord), by account and window as AccountWindowCaps. A window has
	// one or the other.
	AccountWindowAmounts map[string]map[string]float64 `json:"accountWindowAmounts,omitempty"`
	// CapStop, for a subscription whose accounts are capped, ends there
	// (plugins#349): while every account is held at its cap, a request is
	// answered 429, as the vendor would, rather than sent on to Fallback.
	// Off, the default, a fallback takes it. See gateway.Server.plan.
	CapStop bool `json:"capStop,omitempty"`
	// CapCareful is a capped account's usage read again before a request
	// is sent near its cap, and the account held while its usage can't be
	// told (plugins#349): unknown is not spent. Off, the default, routing
	// goes by the last reading, read again each minute. See
	// gateway.carefulDoubt.
	CapCareful bool `json:"capCareful,omitempty"`
	// AccountNames is, for a subscription, the name the user gave each
	// account they named (#1515: a team's seats told apart by who holds
	// them, not by a masked email), by the account's name in lower case.
	// It is magpie's own: the vendor's or plugin's sign-in never sees it
	// (see account_names.go).
	AccountNames map[string]string `json:"accountNames,omitempty"`
	// KeySpendCaps is, for a provider of API keys, how much each key the
	// user capped may spend, in USD as the Usage page prices its calls,
	// a day, a month or in all, by its KeyID: past it, routing takes the
	// key for used up until the period renews (see key_spend.go).
	KeySpendCaps map[string]SpendCap `json:"keySpendCaps,omitempty"`
	// KeyOwnBalances is, for a provider of API keys whose vendor tells no
	// balance, what the user typed is left on each key, by its KeyID: the
	// key's card shows it less what magpie priced its calls at since
	// (see key_balance_own.go).
	KeyOwnBalances map[string]OwnBalance `json:"keyOwnBalances,omitempty"`

	// BalanceURL, when set, is where the vendor tells what is left on a
	// key, asked with the key the way a chat request carries it; BalancePath
	// picks the amount out of the JSON reply (see balance.go). The vendors
	// magpie knows need neither.
	BalanceURL  string `json:"balanceURL,omitempty"`
	BalancePath string `json:"balancePath,omitempty"`
	// BalanceMethod is how BalanceURL is asked: "" (or GET), or POST with
	// no body, for an endpoint that answers only a POST (#2368). Any other
	// is refused when the balance is read (balanceMethodOf).
	BalanceMethod string `json:"balanceMethod,omitempty"`
	// BalanceToken is what a vendor tells the whole account's balance to,
	// where a key is told only what is left on itself: AiHubMix's system
	// access token, or a new-api relay's for its /api/user/self named in
	// BalanceURL (see TakesBalanceToken). It is asked with nothing else but
	// the provider's headers, when the endpoint is one named.
	BalanceToken string `json:"balanceToken,omitempty"`
	// ZhipuTeam, for a Zhipu or Z.ai key on a team's GLM Coding Plan, is
	// the team's organization and project, from the BigModel console: the
	// team's windows are told to the key only with them (see
	// zhipuKeyTeamWindows). nil for a key of the user's own plan.
	ZhipuTeam *ZhipuTeam `json:"zhipuTeam,omitempty"`
	// Vertex, for a Google Vertex AI provider, is the project and location
	// its requests go to and the Google credentials that sign them, in
	// place of a key (see vertex.go).
	Vertex *Vertex `json:"vertex,omitempty"`

	// ModelsURL, when set, is where the vendor lists its models, for one
	// that lists them away from the base URL requests go to (Xiaomi MiMo's
	// plans are served at their own hosts, the list at api.xiaomimimo.com).
	ModelsURL string `json:"modelsURL,omitempty"`
	// ListOff is the editor's Fetch the model list unticked (axelburks on
	// Discord): the vendor's list is never asked for, by a Fetch, a
	// Refresh, a test or a schedule, and the models served are the ids the
	// user added alone, not models.dev's nor a list fetched before. For a
	// vendor whose list names models by ids that can't be asked for
	// (Volcengine Ark's, where each is asked by an endpoint id the user
	// made). A key's provider only (ListsOff).
	ListOff bool `json:"listOff,omitempty"`

	// Models the user chose to expose. Empty means "the preset's picks, or
	// everything the vendor lists when that list is short" — unless NoModels.
	Models []string `json:"models,omitempty"`
	// NoModels is the user's Select none (#1818): with no Models picked,
	// none of its models are served — not to agents, not to any routing
	// group, not by a "provider/model" id — while its balance and usage
	// are still read and shown. Without it, no picks is the vendor's list
	// as ever, so a file saved before it reads as it did. A pick clears it.
	NoModels bool `json:"noModels,omitempty"`
	// BestInFamily is the editor's Best in family (Steven on Discord): with
	// no Models picked, of the vendor's list only the newest model of each
	// family is served (catalog.BestInFamily), so Opus 5.6, once listed,
	// takes Opus 5.5's place by itself. A request that names another of its
	// models is still served, as one that names a model left unpicked is. A
	// pick or Select none clears it (normalize); a file without it reads as
	// the vendor's list, as before.
	BestInFamily bool `json:"bestInFamily,omitempty"`
	// PickedFrom are the models the vendor listed when Models were saved: one
	// listed since is served beside picks that held every one listed then
	// (withNewlyListed), and one listed then and left unpicked stays out.
	PickedFrom []string `json:"pickedFrom,omitempty"`
	// Delisted are picks the provider's list stopped listing (dropGonePicks):
	// out of Models, so not served, until a list has them again, when they
	// go back among the picks (relistPicks). A plugin's list shrinks for a
	// while and comes back (多喝热水 on Discord: Our Free Model lists its
	// account channels' models only once its service has read them after a
	// restart, and a free pool it finds down leaves its list until it is
	// up), and the user's picks came back with it no more. Every save keeps
	// it but for what it picks again; Select none clears it.
	Delisted []string `json:"delisted,omitempty"`
	// DelistedAt is when the list left out the last pick still in Models,
	// so that every pick is in Delisted: none is served until then and
	// pickWait after it (waitsForPicks), and the list is served as if none
	// were picked from then on. A vendor takes a model off for good too:
	// WorkBuddy AI's free deepseek-v4.1-flash, a Free plan account's one
	// pick, left its list for hy3, and the account was served none
	// (maxleebeta on Discord). Zero, while some pick is still in Models, or
	// for picks set aside before it was kept.
	DelistedAt time.Time `json:"delistedAt,omitzero"`
	// RefreshEvery, for a provider of the user's own, is how often the
	// gateway fetches its model list by itself ("6h", "1d"; none, only when
	// asked), as Fetch models does (#2135). See autolist.go.
	RefreshEvery string `json:"refreshEvery,omitempty"`
	// AutoInclude are patterns over a model's id (a glob with *, or a
	// regular expression after "re:"): a model a fetch newly lists that one
	// matches joins Models, beside the picks already there (#2135).
	AutoInclude []string `json:"autoInclude,omitempty"`
	// Unlisted keeps the provider's own models out of the list agents see:
	// it serves only through the routing groups it is in, and by its
	// "provider/model" ids.
	Unlisted bool `json:"unlisted,omitempty"`
	// HiddenDrawers are the provider's image models the user unticked in
	// its editor (321sssrt on X: an OpenAI key lists a dozen): left out of
	// every list one is picked from (Settings › Models › Images, another magpie's
	// list, Aside's) and of Automatic's choice, as an unticked chat model
	// is left out of the agents' lists. A deny list beside Models, not in
	// it: Models is what agents chat with, empty meaning the vendor's list,
	// so an image model put there would be offered to chat with, and
	// hiding one would mean picking every chat model. A request that names
	// one still draws with it, and so does the one Settings › Models › Images
	// names: a hidden pick is shown as picked, and said to be hidden.
	HiddenDrawers []string `json:"hiddenDrawers,omitempty"`
	// HiddenVideomakers are its video models unticked in its editor, as
	// HiddenDrawers are its image models (axelburks on Discord: a
	// Volcengine Ark key lists a dozen Seedance models): left out of
	// what lists video models to pick from (generate_video's, another
	// magpie's, /v1/models' ?kind=video) and of Automatic's choice. A
	// request that names one still makes its video.
	HiddenVideomakers []string `json:"hiddenVideomakers,omitempty"`
	// Off switches the provider off without removing it: its keys and
	// settings stay, but agents aren't given its models, no request,
	// routing group or fallback goes to it, and its balance isn't asked,
	// until it is switched on again (#163) — for a key out of quota.
	Off bool `json:"off,omitempty"`
	// Contexts is how long a request the user says a model takes, in
	// tokens, over what the vendor or models.dev says: by model id, "*"
	// for all the provider's models. Agents are told it.
	Contexts map[string]int `json:"contexts,omitempty"`
	// Family is a tag the provider's models go by in which agents are
	// shown them (settings' Visible), with the provider's id.
	Family string `json:"family,omitempty"`

	Catalog string `json:"catalog,omitempty"` // models.dev id, for names and reasoning levels
	Website string `json:"website,omitempty"`
	KeysURL string `json:"keysUrl,omitempty"`

	// IconURL is a picture the vendor named in an import link, to be fetched
	// once the user confirms. It is only a carrier between parsing and that
	// fetch: Save drops it, so it never reaches providers.json.
	IconURL string `json:"iconUrl,omitempty"`

	// Hidden is set on an account the user removed from magpie; the
	// agent stays signed in, magpie just leaves it alone.
	Hidden bool `json:"hidden,omitempty"`
	// Quiet is set on a removed account whose "Add it back" line the user
	// dismissed: it is offered again only from the Add sheet (#116).
	Quiet bool `json:"quiet,omitempty"`
	// Tucked is set on a quiet one the user hid from the Add sheet too:
	// it is listed there only behind "Show N hidden", or when searched
	// for by name (#116).
	Tucked bool `json:"tucked,omitempty"`
	// Forgot is set on a removed account the user signed out of magpie
	// while its agent stays signed in, which magpie never signs out
	// (Claude Code): it is no longer listed among the removed, and stays
	// hidden until it is added back by a new sign-in (ikunbots on X).
	Forgot bool `json:"forgot,omitempty"`

	// Account is set when the provider is an agent the user signed in to
	// (see account.go); it is derived, never stored.
	Account *Account `json:"-"`

	// keyView is set on p.WithKey(k): p as that one key is asked with,
	// at its plan, whose keys no longer tell plans apart (plansMixed)
	keyView bool
}

type file struct {
	Providers []Provider `json:"providers"`
	Groups    []Group    `json:"groups,omitempty"`
	// Searches are the web search APIs a model's search goes to when no
	// provider can search (see search_api.go).
	Searches []SearchAPI `json:"searches,omitempty"`
	// NoAutoGroups: the user turned off the groups magpie finds on its
	// own (SetAutoGroups); the groups they made or changed stay.
	NoAutoGroups bool `json:"noAutoGroups,omitempty"`
	// AutoGroupSkip are the providers, by id, the user keeps out of the
	// groups magpie finds (SetAutoGroupSkip; chenxiang0010 on Discord):
	// their models are in none, so a model only one other provider serves
	// is no group. The groups the user made or changed keep them.
	AutoGroupSkip []string `json:"autoGroupSkip,omitempty"`
	// Order is the order the user put the providers in on the Providers
	// tab (#499), by id; one not in it follows those that are, in the
	// order it was added (see SetOrder).
	Order []string `json:"order,omitempty"`
	// GroupOrder is the order the user put the routing groups in on the
	// Routing page (#779), by id, found ones among them; one not in it
	// follows those that are (see SetGroupOrder).
	GroupOrder []string `json:"groupOrder,omitempty"`
	// PluginLeft are the picks of a removed plugin's providers, set aside
	// when it was removed and put back when it is added again (#2057; see
	// plugin_left.go).
	PluginLeft []LeftRecord `json:"pluginLeft,omitempty"`
	// Deleted are the providers the user deleted, so the calls made to
	// one stay its own when another is added under its id (Renames).
	Deleted []Deleted `json:"deleted,omitempty"`
}

// Path is the file the user's providers live in.
func Path() string { return appdir.Join(appdir.Config(), "providers.json") }

// load is the file for a read that goes on without it: start-up, the
// gateway, the catalog. One that can't be read is taken as empty there;
// what lists the providers to the user says so (FileError), and an edit
// refuses (read).
func load() file {
	f, _ := read()
	return f
}

// storedFile is load for a read that changes nothing, parsed once until the
// file changes: the gateway asked for one provider's proxy (storedPicks)
// and whether it is left alone (leftAlone) for each request and each
// background look, each a parse of the whole file, 3.6 GB of a live
// magpie's 33 GB in 53 minutes. What it returns is shared: the caller
// must not change it, nor a list or map in it.
func storedFile() file {
	f, err := filememo.Read("providers", Path(), parseProviders)
	if err != nil {
		// missing, or a file that doesn't decode: read's own way, its
		// last good generation
		return load()
	}
	return f
}

func parseProviders(b []byte) (file, error) {
	var f file
	err := json.Unmarshal(bytes.TrimPrefix(b, utf8BOM), &f)
	return f, err
}

// ErrUnreadable is a providers.json that is there but can't be read or
// decoded: never an empty catalog, to list as none, back up or write over.
var ErrUnreadable = errors.New("providers.json can't be read")

type unreadableError struct {
	path string
	err  error
}

func (e *unreadableError) Error() string {
	return fmt.Sprintf("%s can't be read (%v); magpie left it unchanged — fix it or move it aside", e.path, e.err)
}

func (e *unreadableError) Unwrap() []error { return []error{ErrUnreadable, e.err} }

// Reads used for an edit must keep errors: a broken file is not an empty
// catalog to write over. Only a missing file is a first use.
// fileReads counts read, for the test that a page's look at whether the
// file parses doesn't parse it again.
var fileReads atomic.Int64

func read() (file, error) {
	fileReads.Add(1)
	var f file
	b, err := steady.ReadFile(Path())
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, &unreadableError{Path(), err}
	}
	// An editor that saves with a byte-order mark leaves JSON that Go's
	// decoder refuses; read as broken, the file fell back to its .bak, an
	// older generation without what was saved since (#1821).
	b = bytes.TrimPrefix(b, utf8BOM)
	if err := json.Unmarshal(b, &f); err != nil {
		// one left all zero by a crash (#1505) is its last good generation
		bak, ferr := lastgood.Fallback(Path(), b, validProviders)
		if ferr != nil {
			return file{}, &unreadableError{Path(), err}
		}
		f = file{}
		if err := json.Unmarshal(bak, &f); err != nil {
			return file{}, &unreadableError{Path(), err}
		}
	}
	return f, nil
}

// validProviders is providers.json's contents read as the catalog.
func validProviders(b []byte) bool {
	var f file
	return json.Unmarshal(bytes.TrimPrefix(b, utf8BOM), &f) == nil
}

var utf8BOM = []byte("\xef\xbb\xbf")

// FileError is why providers.json can't be read, nil when it can or isn't
// there: for what lists the providers to say so, not "none yet".
func FileError() error {
	// a file that parses, parsed once until it changes (storedFile): each
	// Providers page parsed the whole of it again only to see it does
	if _, err := filememo.Read("providers", Path(), parseProviders); err == nil {
		return nil
	}
	_, err := read()
	return err
}

// Routings is the routing of each provider and group providers.json sets
// one for, read from the file alone: for what asks often (the rotation
// plugin's InUse), without building the catalog and reading every sign-in.
func Routings() ([]string, error) {
	f, err := read()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, p := range f.Providers {
		if p.Routing != "" {
			out = append(out, p.Routing)
		}
	}
	for _, g := range f.Groups {
		if g.Routing != "" {
			out = append(out, g.Routing)
		}
	}
	return out, nil
}

// store replaces providers.json whole, through a file renamed over it, so
// that a read at that moment sees the old catalog or the new one, never a
// file cut short.
func store(f file) error {
	p := Path()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	// a group as it was read has the models its patterns matched then
	// among its members: they are found again each time, never written
	f.Groups = slices.Clone(f.Groups)
	for i := range f.Groups {
		f.Groups[i] = f.Groups[i].stored()
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := lastgood.Keep(p, validProviders, 0o600); err != nil {
		return err
	}
	if err := writePrivate(p, append(b, '\n')); err != nil {
		return err
	}
	pruneIcons(f)
	// what agents were handed of the catalog may be out of date now
	catalog.Touched()
	return nil
}

// All lists the configured providers in the order they were added, then
// the signed-in agents. An entry in the file with no URL is only the
// model picks for one of those accounts. A request that holds the catalog
// (Hold) builds it once: each build reads every agent's sign-in, a keychain
// item or a CLI's among them, and Codex's /models asked for it seven times
// over, past the 5 s Codex waits for the list (#746).
func All() []Provider {
	shared := sharedAll()
	out := make([]Provider, len(shared))
	for i, p := range shared {
		out[i] = p.clone() // callers change theirs, Keys[i] and Headers included
	}
	return out
}

// sharedAll is All without its copies, only for reading: the providers a
// request holds, less the accounts discovery off leaves out. Find, a
// model's price and a value resolved each copied every provider, every
// account's keys and headers, to read one (~15 MB in 2 minutes of an
// idle magpie with its window open).
func sharedAll() []Provider {
	held := heldOf("all", allProviders)
	// asked once, from the settings a held look has read already: asked for
	// each account, settings.json was parsed for every account of every
	// model's price (agent.magpieModels, the GUI's drift every 15s and dsh's
	// round every 30s), most of an idle magpie's CPU (xsw on Discord)
	off := sync.OnceValue(func() bool { return settings.AccountDiscoveryOffIn(heldSettings()) })
	for i, p := range held {
		if p.Account != nil && off() && !p.publicAccount() {
			out := held[:i:i] // the first append copies
			for _, p := range held[i+1:] {
				if p.Account == nil || p.publicAccount() {
					out = append(out, p)
				}
			}
			return out
		}
	}
	return held
}

// clone is p with its settings unshared: slices, maps and pointers copied.
func (p Provider) clone() Provider {
	p.Was = slices.Clone(p.Was)
	p.Gave = slices.Clone(p.Gave)
	p.Keys = slices.Clone(p.Keys)
	p.Fallback = slices.Clone(p.Fallback)
	p.Models = slices.Clone(p.Models)
	p.PickedFrom = slices.Clone(p.PickedFrom)
	p.Delisted = slices.Clone(p.Delisted)
	p.AutoInclude = slices.Clone(p.AutoInclude)
	p.Headers = maps.Clone(p.Headers)
	p.ExtraBody = maps.Clone(p.ExtraBody)
	p.ReplyHeaders = slices.Clone(p.ReplyHeaders)
	p.PassHeaders = slices.Clone(p.PassHeaders)
	p.AccountProxies = maps.Clone(p.AccountProxies)
	p.AccountCaps = maps.Clone(p.AccountCaps)
	p.AccountNames = maps.Clone(p.AccountNames)
	p.KeySpendCaps = maps.Clone(p.KeySpendCaps)
	p.KeyOwnBalances = maps.Clone(p.KeyOwnBalances)
	if p.AccountWindowCaps != nil {
		m := make(map[string]map[string]int, len(p.AccountWindowCaps))
		for k, v := range p.AccountWindowCaps {
			m[k] = maps.Clone(v)
		}
		p.AccountWindowCaps = m
	}
	if p.AccountWindowAmounts != nil {
		m := make(map[string]map[string]float64, len(p.AccountWindowAmounts))
		for k, v := range p.AccountWindowAmounts {
			m[k] = maps.Clone(v)
		}
		p.AccountWindowAmounts = m
	}
	p.AccountConcurrency = maps.Clone(p.AccountConcurrency)
	p.AccountWeights = maps.Clone(p.AccountWeights)
	p.Contexts = maps.Clone(p.Contexts)
	p.AccountModelsAll = maps.Clone(p.AccountModelsAll)
	if p.AccountModels != nil {
		m := make(map[string][]string, len(p.AccountModels))
		for k, v := range p.AccountModels {
			m[k] = slices.Clone(v)
		}
		p.AccountModels = m
	}
	if p.MaxConcurrency != nil {
		v := *p.MaxConcurrency
		p.MaxConcurrency = &v
	}
	if p.MaxAccounts != nil {
		v := *p.MaxAccounts
		p.MaxAccounts = &v
	}
	if p.ZhipuTeam != nil {
		v := *p.ZhipuTeam
		p.ZhipuTeam = &v
	}
	if p.Vertex != nil {
		v := *p.Vertex
		p.Vertex = &v
	}
	return p // Account, the sign-in's runtime, stays shared
}

// AllBuilt counts the times All built the list anew, for tests.
var AllBuilt atomic.Int64

// AllBuiltAt is when the build All last counted was asked for (allProviders
// entered, the hold's rule read at once after), for a test to judge a build
// by when it was asked, not by when the request that asked began (#2965).
var AllBuiltAt atomic.Int64

func allProviders() []Provider {
	AllBuilt.Add(1)
	AllBuiltAt.Store(time.Now().UnixNano())
	f := load()
	out, picks := ownOf(f)
	for _, a := range Accounts() {
		if _, taken := find(out, a.ID); taken || picks[a.ID].Hidden {
			continue
		}
		a = withPicks(a, picks[a.ID])
		if a.ID == "cursor" { // picked before its efforts were one model
			a.Models = cursorPicks(a.Models)
		}
		if a.ID == "antigravity" { // picked before its levels were one model
			a.Models = antigravityPicks(a.Models)
		}
		out = append(out, a)
	}
	return ordered(out, f.Order)
}

// ownOf is f's providers of the user's own, and the model picks it keeps
// for the signed-in accounts by id.
func ownOf(f file) (own []Provider, picks map[string]Provider) {
	picks = map[string]Provider{}
	for _, p := range f.Providers {
		p = normalize(p)
		if !hasEndpoint(p) {
			picks[p.ID] = p
			continue
		}
		own = append(own, p)
	}
	return own, picks
}

// withPicks is the signed-in account a with what the user set on its
// provider (pk, as providers.json keeps it): its models, routing, limits
// and queue. The account the agent is signed in to gets them in All, and
// each other account in use beside it in AlsoOn, so that every account of
// the provider keeps the same settings (#1899: a second WorkBuddy AI
// account queued 62s with the wait set to 1s).
func withPicks(a, pk Provider) Provider {
	return accountSettings(a, pk)
}

// accountSettings is a with every setting a signed-in account keeps in
// providers.json taken from pk. It is the one list both ways go through:
// Save writes what it copies, and withPicks reads the same back. Two lists
// drifted apart, and a setting on one alone was lost: #4738's
// additional-tools=on on a Codex account was on neither, PaulT's price
// rate on neither, an account's extraBody and reply headers were read
// back but never written, and its rests out of quota and after failures
// written but never read back.
func accountSettings(a, pk Provider) Provider {
	a.PickedFrom, a.Delisted, a.DelistedAt = pk.PickedFrom, pk.Delisted, pk.DelistedAt
	a.Models, a.Unlisted, a.Off, a.Fallback, a.Routing, a.Affinity, a.KeepLogin, a.KeepLoginAs, a.Contexts, a.Family = pk.Models, pk.Unlisted, pk.Off, pk.Fallback, pk.Routing, pk.Affinity, pk.KeepLogin, pk.KeepLoginAs, pk.Contexts, pk.Family
	a.SwitchOnly, a.RestartApp = pk.SwitchOnly, pk.RestartApp
	a.Sink, a.NoModels, a.BestInFamily = pk.Sink, pk.NoModels, pk.BestInFamily
	a.NativeCompaction = pk.NativeCompaction
	a.PromptAsWritten = pk.PromptAsWritten
	// a deferred tool a tool search loaded goes where it was loaded, set
	// with `magpie provider set <id> additional-tools=on` (#2660, #4738)
	a.AdditionalTools, a.AdditionalToolsOff = pk.AdditionalTools, pk.AdditionalToolsOff
	// Send requests unmasked, the user's for a subscription and a
	// plugin's provider too (isee1_1 on Discord)
	a.Unredacted = pk.Unredacted
	a.Proxy, a.AccountProxies, a.AccountModels, a.AccountModelsAll = pk.Proxy, pk.AccountProxies, pk.AccountModels, pk.AccountModelsAll
	a.AccountCaps, a.AccountWindowCaps, a.AccountWindowAmounts = pk.AccountCaps, pk.AccountWindowCaps, pk.AccountWindowAmounts
	a.CapStop, a.CapCareful = pk.CapStop, pk.CapCareful
	a.UseFirst = pk.UseFirst
	a.NoStandIn = pk.NoStandIn
	a.AccountNames = pk.AccountNames
	a.MaxConcurrency, a.PinUpstream = pk.MaxConcurrency, pk.PinUpstream
	a.MaxAccounts = pk.MaxAccounts
	a.AccountConcurrency, a.QueueLimit, a.QueueWait = pk.AccountConcurrency, pk.QueueLimit, pk.QueueWait
	a.AccountWeights = pk.AccountWeights
	a.MaxRPM = pk.MaxRPM
	// how long a request to it alone is kept while it is busy
	a.Patience = pk.Patience
	// how long its accounts rest out of quota, and the most they rest
	// while they keep failing (#2008); or not at all for a limit
	a.QuotaRest, a.QuotaResetAt, a.RestCap, a.LimitRestOff = pk.QuotaRest, pk.QuotaResetAt, pk.RestCap, pk.LimitRestOff
	// what it bills against the official price (#819): an account's editor
	// sets it as a key provider's does (PaulT on Discord, Codex)
	a.PriceRate = pk.PriceRate
	// counted free by the user's word, every account alike
	a.CountFree = pk.CountFree
	// and how it bills, by the user's word (BillingKind)
	a.Billing = pk.Billing
	// the hours the provider bills at its own rate (#2463), as the editor
	// keeps them for the account like the rest of these
	a.RateWindows = pk.RateWindows
	// the extra body fields written for it, which a ChatGPT account's
	// requests take (WithExtraBody, magpie-releases#18)
	a.ExtraBody = pk.ExtraBody
	// the image models unticked in its editor (HiddenDrawers)
	a.HiddenDrawers = pk.HiddenDrawers
	a.HiddenVideomakers = pk.HiddenVideomakers
	// and the headers of its replies passed on (#4145)
	a.ReplyHeaders = pk.ReplyHeaders
	// and the headers of the agent's requests passed on
	a.PassHeaders = pk.PassHeaders
	return a
}

// Hidden lists the signed-in accounts the user removed from magpie. The
// file is read once, as parsed until it changes: a parse of it for each
// account was 68 MB of the Providers page's 1.4 GB in twenty loads.
func Hidden() []Provider {
	var out []Provider
	saved := storedFile().Providers
	// most users removed none: then no account is made at all (Accounts
	// reads every sign-in, ~0.6 MB each poll of the Providers page,
	// which asks twice, here and through Excluded)
	if !slices.ContainsFunc(saved, func(p Provider) bool { return p.Hidden }) {
		return nil
	}
	for _, a := range Accounts() {
		for _, p := range saved {
			if p.ID == a.ID && p.Hidden {
				a.Quiet, a.Tucked = p.Quiet, p.Tucked
				out = append(out, a)
			}
		}
	}
	return out
}

func find(ps []Provider, id string) (Provider, bool) {
	for _, p := range ps {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

// Find looks a provider up by id (or name, case-insensitively).
func Find(id string) (*Provider, error) {
	p, err := findIn(sharedAll(), id)
	if p != nil {
		c := p.clone() // the caller's to change, as All's are
		p = &c
	}
	return p, err
}

func findIn(all []Provider, id string) (*Provider, error) {
	if p := lookIn(all, id); p != nil {
		return p, nil
	}
	return nil, fmt.Errorf("no provider %q — magpie providers lists them%s", id, readElsewhere())
}

// lookIn is findIn without its error, which looks on disk for where else
// this magpie's files may be (readElsewhere): a request's model not found
// among the providers asked it twice, for nothing (#3394).
func lookIn(all []Provider, id string) *Provider {
	q := strings.ToLower(strings.TrimSpace(id))
	// an id before a name: a provider of the user's called WorkBuddy isn't
	// the workbuddy subscription
	if i := slices.IndexFunc(all, func(p Provider) bool { return p.ID == q }); i >= 0 {
		return &all[i]
	}
	for _, p := range all {
		if strings.ToLower(p.Name) == q {
			return &p
		}
	}
	for _, p := range all {
		if slices.Contains(p.Was, q) {
			return &p
		}
	}
	return nil
}

// readElsewhere says, for a provider not found, that this magpie reads its
// files from another folder than the app's when XDG_CONFIG_HOME moves them
// and the app's has some (蒙面人 on Discord: a terminal's magpie had no
// Antigravity, which the window showed signed in).
func readElsewhere() string {
	def := appdir.Redirected()
	if def == "" || !isFile(filepath.Join(def, "providers.json")) && !isFile(filepath.Join(def, "logins.json")) {
		return ""
	}
	return fmt.Sprintf("\n  this magpie reads its files from %s, as XDG_CONFIG_HOME says; the app opened from the Dock or Start menu keeps them in %s. Run it with XDG_CONFIG_HOME unset (env -u XDG_CONFIG_HOME magpie …) to use those", appdir.Config(), def)
}

var idRe = lazyre.New(`[^a-z0-9]+`)

// Slug derives an id from a name: "My Relay" → "my-relay".
func Slug(name string) string {
	return strings.Trim(idRe.ReplaceAllString(strings.ToLower(name), "-"), "-")
}

// Save adds or replaces a provider.
func Save(p Provider) error {
	// a key given a Vertex AI provider is refused here, before normalize
	// drops it: it would never be sent
	if p.IsVertex() && len(p.Keys) > 0 {
		return errors.New("Google Vertex AI takes one API key")
	}
	// a base URL with no key is refused before normalize drops it
	if p.IsVertex() && p.Vertex != nil && strings.TrimSpace(p.Vertex.BaseURL) != "" && strings.TrimSpace(p.Key) == "" {
		return errors.New("a base URL is for an API key: Google credentials are sent only to Google's own address")
	}
	p = normalize(p)
	p.IconURL = "" // import-only: never stored
	// where a picture came from goes only with that picture
	if p.IconFrom = strings.TrimSpace(p.IconFrom); !strings.HasPrefix(p.Icon, iconPrefix) {
		p.IconFrom = ""
	}
	if p.ID == "" {
		p.ID = Slug(p.Name)
	}
	// an id stored already is the provider's, whatever it is: one put in
	// providers.json by hand ("b.ai") was refused on every Save, so the
	// editor could neither change it nor rename it to one that is right
	// (01huadalang on Discord: 我不管改成什么都显示不能用 b.ai)
	if p.ID == "" || p.ID != Slug(p.ID) && !stored(p.ID) {
		return fmt.Errorf("provider id must be lowercase letters, digits and dashes, not %q", p.ID)
	}
	if p.ID == "magpie" {
		return errors.New(`"magpie" is what agents call the gateway itself; pick another id`)
	}
	if p.ID == strings.TrimSuffix(GroupPrefix, "/") {
		return errors.New(`"group" starts the ids of routing groups; pick another id`)
	}
	if p.ID == autoID && !stored(p.ID) { // one saved before keeps it (autoRefsOn)
		return errAutoID
	}
	if err := settings.CheckProxy(p.Proxy); err != nil {
		return err
	}
	if err := checkAccountProxies(p.AccountProxies); err != nil {
		return err
	}
	if p.Name == "" {
		p.Name = p.ID
	}
	if _, ok := find(Accounts(), p.ID); ok || p.ID == "kiro" && (p.Key != "" || stored(p.ID)) {
		// an account keeps only the user's model picks; the rest is the
		// agent's own sign-in. One the user removed stays removed: only
		// ShowAccount brings it back. Kiro's alone also keeps a key, which
		// it takes in place of a sign-in — so saving one is how a Kiro
		// that isn't signed in is added.
		key := ""
		if p.ID == "kiro" {
			key = p.Key
		}
		p = accountSettings(Provider{ID: p.ID, Key: key, Hidden: hiddenAccount(p.ID), Quiet: quietAccount(p.ID), Tucked: tuckedAccount(p.ID)}, p)
	} else {
		p.AccountProxies = keyProxies(p) // a provider of keys proxies each key apart
		if subscriptionID(p.ID) && !stored(p.ID) {
			// taken, it would hide that subscription once signed in
			return fmt.Errorf("%q is the id of the %s subscription; pick another name", p.ID, p.ID)
		}
		if p.IsVertex() {
			if err := p.Vertex.check(p.Key != ""); err != nil {
				return err
			}
		} else if !hasEndpoint(p) {
			if p.Preset == AzurePreset {
				return errors.New("Azure OpenAI needs your resource's endpoint, e.g. https://<resource>.openai.azure.com")
			}
			return errors.New("a provider needs a base URL")
		}
		if p.InsecureTLS && !p.TakesInsecureTLS() {
			return fmt.Errorf("%s is a preset's: only a provider you added with your own base URL can skip the certificate check", p.Name)
		}
		if p.HTTP1 && !p.TakesInsecureTLS() {
			return fmt.Errorf("%s is a preset's: only a provider you added with your own base URL can be held to HTTP/1.1", p.Name)
		}
		if v := strings.TrimSpace(p.ServerPin); v != "" {
			if p.ServerPin = netproxy.NormalPin(v); p.ServerPin == "" {
				return fmt.Errorf("%s: %q isn't a certificate's SHA-256 fingerprint (64 hex digits)", p.ID, v)
			}
			if !p.TakesServerPin() {
				return fmt.Errorf("%s: a trusted server certificate is only for an https:// address of your own: a provider you added with a base URL of its own, Remote magpie, or a preset you pointed at your own server", p.ID)
			}
		}
		if p.ClientCert != "" || p.ClientKey != "" || p.ServerCA != "" {
			if !p.TakesClientCert() {
				return fmt.Errorf("%s: a client certificate or server CA is only for a provider you added with a base URL of its own, or Remote magpie", p.ID)
			}
			for _, f := range []*string{&p.ClientCert, &p.ClientKey, &p.ServerCA} {
				v, err := CertPath(*f)
				if err != nil {
					return fmt.Errorf("%s: %w", p.ID, err)
				}
				*f = v
			}
			if err := netproxy.CheckClientCert(p.ClientCert, p.ClientKey, p.ServerCA); err != nil {
				return fmt.Errorf("%s: %w", p.ID, err)
			}
		}
		if strings.Contains(p.Decide, WorkspaceID) {
			return errors.New("Bailian's decision model is asked at your workspace's host: give its workspace ID (workspace=… or the editor's Workspace ID), or pick the Token Plan")
		}
		if p.Key == "" && !keyOptional(p) && !p.IsVertex() {
			return fmt.Errorf("%s needs an API key", p.Name)
		}
	}
	f, err := read()
	if err != nil {
		return err
	}
	i := slices.IndexFunc(f.Providers, func(q Provider) bool { return q.ID == p.ID })
	var was *Provider
	if i >= 0 {
		was = &f.Providers[i]
	}
	p.PickedFrom = listedWith(p, was)
	p.Delisted = delistedWith(p, was)
	p.DelistedAt = delistedAtWith(p, was)
	if was != nil {
		if p.Was == nil {
			p.Was = was.Was
		}
		if p.Gave == nil {
			p.Gave = was.Gave
		}
		if sameStored(*was, p) {
			// Save pressed on an editor left as it was: providers.json
			// would be written byte for byte as it is, and its write
			// handed the catalog to every agent again (agent.SyncCatalog),
			// seconds of CPU with many agents set up (#1962, witt-bit)
			return nil
		}
		f.Providers[i] = p
	} else {
		// a new provider under an id another had: what was recorded under
		// it until now stays that one's
		giveID(f.Providers, -1, p.ID, time.Now())
		f.Providers = append(f.Providers, p)
	}
	if err := store(f); err != nil {
		return err
	}
	if p.ID == "claude" {
		// an account's proxy picked in the editor reaches Claude Code at
		// once, not at the gateway's next look (#1807)
		syncClaudeProxyEnvNow()
	}
	return nil
}

// sameStored is whether a and b are written to providers.json alike.
func sameStored(a, b Provider) bool {
	x, err := json.Marshal(a)
	if err != nil {
		return false
	}
	y, err := json.Marshal(b)
	return err == nil && bytes.Equal(x, y)
}

// delistedWith is what p.Delisted is saved as: what was set aside before,
// as an editor sends none and couldn't show them to unpick, less what p
// picks again; none after Select none, or the vendor's list picked in
// place of picks (no picks saved over some).
func delistedWith(p Provider, was *Provider) []string {
	if p.ServesNone() || len(p.Models) == 0 && was != nil && len(was.Models) > 0 {
		return nil
	}
	d := p.Delisted
	if d == nil && was != nil {
		d = was.Delisted
	}
	d = slices.DeleteFunc(slices.Clone(d), func(id string) bool { return slices.Contains(p.Models, id) })
	if len(d) == 0 {
		return nil
	}
	return d
}

// delistedAtWith is what p.DelistedAt is saved as: zero while some pick is
// in Models or none is set aside; else p's, or what was kept before when p
// has none, as a save of another setting keeps the wait going.
func delistedAtWith(p Provider, was *Provider) time.Time {
	if len(p.Models) > 0 || len(p.Delisted) == 0 {
		return time.Time{}
	}
	if p.DelistedAt.IsZero() && was != nil {
		return was.DelistedAt
	}
	return p.DelistedAt
}

// listedWith is what p.PickedFrom is saved as: none without picks; what was
// kept with the picks when they are the same picks; and the models listed
// now when the picks are new, as the user picked them from that list. While
// some picks are set aside, the list is known to be short, so those listed
// when the picks were saved before are kept too: a model the user saw and
// left unpicked isn't new when the list that left it out for a while has it
// again (an Our Free Model channel not read yet when the picks were saved).
// A list short of no pick is taken as it is: what it left out and lists
// again later is new (TestClaudePicksOfEveryModelTakeOnesListedSince). A
// provider of keys saved before PickedFrom was kept is given it at its next
// Save, the same picks too, as withNewlyListed serves those picks as they
// are until it has one (TestLegacyPicksOfEveryModelGetPickedFromAtSave).
func listedWith(p Provider, was *Provider) []string {
	if len(p.Models) == 0 {
		return nil
	}
	same := was != nil && slices.Equal(normalModels(was.Models), normalModels(p.Models))
	if same && was.PickedFrom != nil {
		return was.PickedFrom
	}
	from := p
	if q, err := Find(p.ID); err == nil {
		from = *q // with its account, whose list it is
	}
	if same && (from.Account != nil || p.Account != nil) {
		return was.PickedFrom // a subscription keeps the date rule
	}
	var ids []string
	for _, m := range from.Available() {
		ids = append(ids, m.ID)
	}
	if was != nil && len(ids) > 0 && len(delistedWith(p, was)) > 0 {
		for _, id := range was.PickedFrom {
			if !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

// Add saves a provider the user just added, beside those already here: an
// id in use — the preset's, or the one its name slugs to — moves on to the
// next free one (anthropic-2), and a name in use gets the same number, so a
// second key of a vendor, or one key for another workspace, is a provider of
// its own rather than one replacing the first. It answers the id saved.
func Add(p Provider) (string, error) {
	return add(p, true)
}

func add(p Provider, once bool) (string, error) {
	p.ID = strings.ToLower(strings.TrimSpace(p.ID))
	if p.ID == "" {
		p.ID = Slug(p.Name)
	}
	if p.ID == "" {
		// a name with no Latin letters or digits in it (中转站) slugs to
		// nothing: the id is the site's instead
		p.ID = hostID(p)
	}
	// the same key on the same host with the same headers, at the same
	// addresses, is the one already here, not another: adding it twice
	// would only split its usage. One key a relay takes in both formats,
	// added once for its OpenAI API and once for its Anthropic API, is two
	// providers the user asked for (energetic_wolf_99424 on Discord: the
	// second was refused as the key already used). A copy the user asked
	// for is taken (AddCopy).
	for _, h := range All() {
		if once && h.Account == nil && sameProvider(h, normalize(p)) && servesAll(h, normalize(p)) {
			return "", fmt.Errorf("%s is already added with that key (%s); magpie provider key %s <key> changes its key", h.Name, h.ID, h.ID)
		}
	}
	p.ID, p.Name = freeID(p.ID), freeName(p.Name)
	if err := Save(p); err != nil {
		return p.ID, err
	}
	if p.Preset != "" {
		// a provider added from a partner counts for it (partner_events.go)
		CountPartner(PartnerAdded, p.Preset)
	}
	return p.ID, nil
}

// servesAll says h is asked at every address p is added with, each for the
// same API: p brings no API, and no address of one, that h lacks.
func servesAll(h, p Provider) bool {
	for _, u := range [][2]string{{h.Chat, p.Chat}, {h.Responses, p.Responses}, {h.Anthropic, p.Anthropic}, {h.Gemini, p.Gemini}} {
		if u[1] != "" && u[0] != u[1] {
			return false
		}
	}
	return true
}

// AddCopy adds p, a copy the user made of the provider from (#268), beside
// it: what the form doesn't carry — the keys, the balance token, how
// requests spread over the keys, where they fall back to — is from's where
// p leaves it out. The same key on the same host is taken, the copy being
// asked for (another model list, another endpoint). A signed-in account is
// never copied: its sign-in is the agent's.
func AddCopy(p Provider, from string) (string, error) {
	src, err := Find(from)
	if err != nil {
		return "", err
	}
	if src.Account != nil {
		return "", fmt.Errorf("%s is a signed-in account, which can't be copied", src.Name)
	}
	if p.Key == "" {
		p.Key, p.KeyName, p.KeyProtocol, p.KeyWeight, p.KeyPlan = src.Key, src.KeyName, src.KeyProtocol, src.KeyWeight, src.KeyPlan
		p.Keys = slices.Clone(src.Keys)
		p.Routing, p.Sink, p.Affinity = src.Routing, src.Sink, src.Affinity
	}
	if p.BalanceToken == "" {
		p.BalanceToken = src.BalanceToken
	}
	// the access key as the copy's editor showed it: the form sends its ID
	// but never the saved Secret, which went missing from the copy (#1821)
	if p.SecretAccessKey == "" && (p.AccessKeyID == "" || p.AccessKeyID == src.AccessKeyID) {
		p.AccessKeyID, p.SecretAccessKey = src.AccessKeyID, src.SecretAccessKey
	}
	if p.ZhipuTeam == nil {
		p.ZhipuTeam = src.ZhipuTeam
	}
	if p.Vertex == nil && src.Vertex != nil {
		v := *src.Vertex
		p.Vertex = &v
	}
	if p.Fallback == nil {
		p.Fallback = slices.Clone(src.Fallback)
	}
	p.Unlisted = p.Unlisted || src.Unlisted
	if p.HiddenDrawers == nil {
		p.HiddenDrawers = slices.Clone(src.HiddenDrawers)
	}
	if p.HiddenVideomakers == nil {
		p.HiddenVideomakers = slices.Clone(src.HiddenVideomakers)
	}
	p.Searches = p.Searches || src.Searches
	p.Unredacted = p.Unredacted || src.Unredacted
	p.NoStandIn = p.NoStandIn || src.NoStandIn
	p.CountFree = p.CountFree || src.CountFree
	if p.Billing == "" {
		p.Billing = src.Billing
	}
	p.NativeCompaction = p.NativeCompaction || src.NativeCompaction
	if p.Socket == "" {
		p.Socket = src.Socket
	}
	p.MagpieUserAgent = p.MagpieUserAgent || src.MagpieUserAgent
	if p.Website == "" {
		p.Website = src.Website
	}
	if p.KeysURL == "" {
		p.KeysURL = src.KeysURL
	}
	return add(p, false)
}

// hostID is an id for a provider from the host it is on: api.relay.com is
// relay, and one on an IP address, or with no address, is custom.
func hostID(p Provider) string {
	for _, u := range []string{p.Chat, p.Responses, p.Anthropic, p.Gemini} {
		h := hostOf(u)
		if host, _, err := net.SplitHostPort(h); err == nil {
			h = host
		}
		if h = strings.Trim(h, "[]"); h == "" || net.ParseIP(h) != nil {
			continue
		}
		labels := strings.Split(h, ".")
		if len(labels) > 1 {
			labels = labels[:len(labels)-1] // the .com
		}
		for len(labels) > 1 && (labels[0] == "api" || labels[0] == "www") {
			labels = labels[1:]
		}
		if id := Slug(strings.Join(labels, "-")); id != "" {
			return id
		}
	}
	return "custom"
}

// freeName is name, or "name 2", "name 3"… whichever no provider is called,
// since providers are found by name as well as id.
func freeName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	taken := map[string]bool{}
	for _, p := range All() {
		taken[strings.ToLower(p.Name)] = true
	}
	for n, try := 2, name; ; n++ {
		if !taken[strings.ToLower(try)] {
			return try
		}
		try = name + " " + itoa(n)
	}
}

// accountIDs are the ids of the subscriptions magpie can list (account.go).
var accountIDs = []string{"antigravity", "claude", "codex", CommandCodePlanID, "copilot", "cursor", "devin", "factory", "gemini", "grok", "kiro", MiMoID, ChatGPTAPIID, "qoder", QoderCNID, "workbuddy", WorkBuddyAIID, "zcode", "zed"}

func stored(id string) bool {
	for _, p := range load().Providers {
		if p.ID == id {
			return true
		}
	}
	return false
}

func hiddenAccount(id string) bool {
	for _, p := range load().Providers {
		if p.ID == id {
			return p.Hidden
		}
	}
	return false
}

func quietAccount(id string) bool {
	for _, p := range load().Providers {
		if p.ID == id {
			return p.Quiet
		}
	}
	return false
}

func tuckedAccount(id string) bool {
	for _, p := range load().Providers {
		if p.ID == id {
			return p.Tucked
		}
	}
	return false
}

// TuckAccount hides a removed account from the Add sheet's "Removed from
// magpie" too (on), or lists it there again (off). Tucked, it is quiet as
// well: no reminder line either (#116).
func TuckAccount(id string, on bool) error {
	f, err := read()
	if err != nil {
		return err
	}
	for i := range f.Providers {
		if f.Providers[i].ID == id && f.Providers[i].Hidden {
			f.Providers[i].Tucked = on
			if on {
				f.Providers[i].Quiet = true
			}
			return store(f)
		}
	}
	return nil
}

// QuietAccount stops reminding the user of an account they removed: its
// "Add it back" line goes, and it is offered only from the Add sheet.
func QuietAccount(id string) error {
	f, err := read()
	if err != nil {
		return err
	}
	for i := range f.Providers {
		if f.Providers[i].ID == id && f.Providers[i].Hidden {
			f.Providers[i].Quiet = true
			return store(f)
		}
	}
	return nil
}

// forgetKeptAccount marks a removed account signed out of magpie while its
// agent's own sign-in stays: Excluded leaves it out.
func forgetKeptAccount(id string) error {
	f, err := read()
	if err != nil {
		return err
	}
	for i := range f.Providers {
		if f.Providers[i].ID == id && f.Providers[i].Hidden {
			f.Providers[i].Forgot = true
			return store(f)
		}
	}
	return nil
}

// ShowAccount brings back the signed-in account of an agent the user had
// removed from magpie.
func ShowAccount(id string) error {
	f, err := read()
	if err != nil {
		return err
	}
	for i := range f.Providers {
		if f.Providers[i].ID == id && f.Providers[i].Hidden {
			f.Providers[i].Hidden, f.Providers[i].Quiet, f.Providers[i].Tucked, f.Providers[i].Forgot = false, false, false, false
			return store(f)
		}
	}
	return nil
}

// Delete removes a provider. An account, a built-in's or a plugin's, is
// only hidden from magpie (its accounts and model picks kept, shown again
// from Hidden or by signing in); signing out is each account's Remove.
func Delete(id string) error {
	f, err := read()
	if err != nil {
		return err
	}
	if _, ok := find(Accounts(), id); ok {
		for i := range f.Providers {
			if f.Providers[i].ID == id {
				f.Providers[i].Hidden = true
				return store(f)
			}
		}
		f.Providers = append(f.Providers, Provider{ID: id, Hidden: true})
		return store(f)
	}
	keep := f.Providers[:0]
	found := false
	for _, p := range f.Providers {
		if p.ID == id {
			found = true
			// its calls stay its own, not a provider's added later under
			// its id (cybercaius on Discord)
			f.Deleted = append(f.Deleted, Deleted{ID: p.ID, Name: p.Name, Icon: p.Icon, At: time.Now(), Was: slices.Clone(p.Was), Gave: slices.Clone(p.Gave)})
			continue
		}
		keep = append(keep, p)
	}
	if !found {
		return fmt.Errorf("no provider %q", id)
	}
	f.Providers = keep
	if err := store(f); err != nil {
		return err
	}
	// its StepFun sign-in goes with it, not to a provider added later
	// under the same id
	return forgetStepFun(id)
}

// keyOptional is true for a server on this computer or on the user's own
// network — a local model, a company's internal relay at a LAN address —
// which often takes no key. A keyless Codex relay at 192.168.x.x:8080 was
// left out of a CC Switch import ("it has no API key") and couldn't be
// saved by hand, while the same server at 127.0.0.1 could.
func keyOptional(p Provider) bool {
	if pr := Preset(p.Preset); pr != nil && pr.NoKey {
		return true
	}
	h := p.Host()
	if strings.HasPrefix(h, "localhost") || strings.HasPrefix(h, "127.0.0.1") || strings.HasPrefix(h, "0.0.0.0") {
		return true
	}
	return ownNetwork(h)
}

// cgnat is carrier-grade NAT's 100.64/10, where Tailscale puts its peers.
var cgnat = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

// ownNetwork is a host[:port] on the user's own network, read from the
// name alone (no lookup: Ready asks it often): a private, link-local,
// loopback or carrier-grade NAT address, or a .local name — what
// netproxy's onLAN sends around a proxy, without resolving names.
func ownNetwork(hostport string) bool {
	h := hostport
	if host, _, err := net.SplitHostPort(hostport); err == nil {
		h = host
	}
	h = strings.ToLower(strings.TrimSuffix(strings.Trim(h, "[]"), "."))
	if strings.HasSuffix(h, ".local") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || cgnat.Contains(ip))
}

func normalize(p Provider) Provider {
	p.ID = strings.ToLower(strings.TrimSpace(p.ID))
	p.Name = strings.TrimSpace(p.Name)
	if p.Patience < 0 {
		p.Patience = PatienceOff
	}
	p.Patience = min(p.Patience, PatienceLongest)
	if v, ok := ParseSocket(p.Socket); ok {
		p.Socket = v
	} else {
		p.Socket = ""
	}
	p.Key = strings.TrimSpace(p.Key)
	p.Proxy = strings.TrimSpace(p.Proxy)
	p.AccountProxies = normalAccountProxies(p.AccountProxies)
	p.AccountModels = normalAccountModels(p.AccountModels)
	p.AccountModelsAll = normalModelsAll(p.AccountModelsAll, p.AccountModels)
	p.AccountCaps = normalAccountCaps(p.AccountCaps)
	p.AccountWindowCaps = normalWindowCaps(p.AccountWindowCaps)
	p.AccountWindowAmounts = normalWindowAmounts(p.AccountWindowAmounts)
	p.AccountNames = normalAccountNames(p.AccountNames)
	p.KeySpendCaps = normalKeySpendCaps(p.KeySpendCaps)
	p.KeyOwnBalances = normalKeyOwnBalances(p.KeyOwnBalances)
	if len(p.Models) > 0 {
		p.NoModels = false // a pick is no longer Select none
	}
	if len(p.Models) > 0 || p.NoModels {
		p.BestInFamily = false // nor Best in family
	}
	p.ZhipuTeam = p.ZhipuTeam.normal()
	p.Vertex = p.Vertex.Normal()
	if !p.IsVertex() {
		p.Vertex = nil
	} else {
		// asked only at the address its project and location make (or its
		// BaseURL, with a key), with a Google token that no other address
		// is to be sent: the URLs of a provider put in providers.json by
		// hand are dropped. It has one key, or none.
		p.Chat, p.Responses, p.Anthropic, p.Gemini, p.Decide = "", "", "", "", ""
		p.Key = strings.TrimSpace(p.Key)
		p.KeyName, p.Keys, p.KeyProtocol, p.KeyWeight, p.KeyPlan = "", nil, "", 0, ""
		if p.Key == "" && p.Vertex != nil && p.Vertex.BaseURL != "" {
			// no key to go with it: Google's token must not go there
			v := *p.Vertex
			v.BaseURL = ""
			p.Vertex = v.Normal()
		}
	}
	p.remoteMagpieEndpoints()
	for _, u := range []*string{&p.Chat, &p.Responses, &p.Anthropic, &p.Gemini, &p.Decide, &p.Website, &p.KeysURL} {
		*u = strings.TrimRight(strings.TrimSpace(*u), "/")
		if *u != "" && !strings.Contains(*u, "://") {
			*u = "https://" + *u
		}
	}
	// the Anthropic base is the root /v1/messages is asked at: one given as
	// .../v1 or .../v1/messages, as vendors' docs often show it, would have
	// the version sent twice and every message turned away (404) while the
	// model list, asked at both, still answers
	for _, suf := range []string{"/v1/messages", "/v1"} {
		if b, ok := strings.CutSuffix(p.Anthropic, suf); ok && strings.Contains(b, "://") && len(b) > len("https://") {
			p.Anthropic = b
			break
		}
	}
	p.Gemini = GeminiBase(p.Gemini)
	p.aiStudioNative()
	// a pick whose URL is gone (cleared from the CLI) is no pick
	if p.BaseAPI != "" && p.baseOf(p.BaseAPI) == "" {
		p.BaseAPI = ""
	}
	p.Models = cleanList(p.Models)
	p.Fallback = cleanList(p.Fallback)
	// a provider saved under an id a preset carried before (presetAliases:
	// qianfan's first day's, Tencent Cloud's plan and TokenHub's two) is
	// the preset since: its endpoints say the region, and its own id
	// stays, so whatever the agents, groups and usage named it by keeps
	// finding it
	if a, ok := presetAliases[p.Preset]; ok {
		p.Preset = a.preset
	}
	// a preset's endpoints its vendor has moved since (Tencent Cloud's
	// Token Plan 个人版, ysm119) are where they are now
	p.moveEndpoints()
	// OpenCode Zen serves its free models (-free) signed out, to the key
	// OpenCode itself sends then: a Zen provider saved with no key of its
	// own asks with that one
	if anon := p.AnonymousKey(); anon != "" && p.Key == "" {
		p.Key = anon
	}
	// a Zen provider saved before its preset had System One for Jev
	// (01huadalang: its Jev was asked as a chat model, and failed), an
	// OpenRouter one before its decision models were listed (ARNO)
	if (p.Preset == "opencode-zen" || p.Preset == "openrouter") && p.Decide == "" && p.Chat != "" {
		if pr := Preset(p.Preset); pr != nil {
			p.Decide = pr.Decide
		}
	}
	// an oMLX, MLX-Serve or Ollama provider saved before its preset had
	// System One (#1641; Nanka on Discord) asks it where its chat
	// completions are, at whatever address it was given
	if (p.Preset == OMLXPreset || p.Preset == MLXServePreset || p.Preset == "ollama" || p.Preset == "ollama-cloud") && p.Decide == "" && p.Chat != "" {
		p.Decide = p.Chat
	}
	// one asked at Volcengine Ark's pay as you go, its preset's or a
	// custom one of either protocol, answers System One on the same key:
	// its decision model is offered with no URL to type (a user on
	// Discord found no System One endpoint for Ark, custom or preset)
	if p.Decide == "" {
		p.Decide = p.arkSystemOne()
	}
	if p.Routing != Ordered && p.Routing != Rotate && p.Routing != LeastUsed && p.Routing != Pace && p.Routing != Weighted && p.Routing != FreeFirst && p.Routing != Fastest && p.Routing != Drain && !PluginRouting(p.Routing) {
		p.Routing = ""
	}
	if !slices.Contains(Affinities, p.Affinity) {
		p.Affinity = ""
	}
	if !slices.Contains(AuthChoices, p.AuthHeader) {
		p.AuthHeader = AuthBoth
	}
	if p.MaxConcurrency != nil && *p.MaxConcurrency < 0 {
		p.MaxConcurrency = new(int)
	}
	if p.MaxAccounts != nil {
		v := min(max(*p.MaxAccounts, 0), MaxLimit)
		p.MaxAccounts = &v
	}
	p.AccountConcurrency = normalAccountConcurrency(p.AccountConcurrency)
	p.AccountWeights = normalAccountWeights(p.AccountWeights)
	p.QueueLimit, p.QueueWait = min(max(p.QueueLimit, 0), MaxQueueLimit), min(max(p.QueueWait, 0), MaxQueueWait)
	p.MaxRPM = min(max(p.MaxRPM, 0), MaxRPMLimit)
	p.QuotaRest = min(max(p.QuotaRest, 0), MaxQuotaRest)
	if p.RestCap != RestCapOff {
		p.RestCap = min(max(p.RestCap, 0), MaxQuotaRest)
	}
	if _, err := ParseResetAt(p.QuotaResetAt); err != nil {
		p.QuotaResetAt = ""
	}
	p.Catalog = strings.Join(p.Catalogs(), ", ")
	// a Bedrock provider saved before the preset had its Responses API
	// (#176) gets it where its chat completions are: the runtime serves both
	// at /openai/v1
	if p.Responses == "" && strings.HasSuffix(p.Chat, "/openai/v1") && p.IsBedrock() {
		p.Responses = p.Chat
	}
	// Azure OpenAI's resource, however its endpoint was pasted, is asked
	// on its v1 API, chat completions and Responses both (azure.go)
	p.azureEndpoints()
	// OpenCode Zen's or Go's other APIs, beside the one it was given (#1215)
	p.openCodeEndpoints()
	// Bedrock Mantle's other APIs, beside the one it was given
	p.mantleEndpoints()
	// a preset's provider keeps its headers too: the preset gives the
	// endpoints and catalog, the headers say which workspace or app it is
	p.Headers = cleanHeaders(p.Headers)
	p.ExtraBody = cleanExtraBody(p.ExtraBody)
	p.ReplyHeaders = cleanReplyHeaders(p.ReplyHeaders)
	p.PassHeaders = cleanRequestHeaders(p.PassHeaders)
	if pr := Preset(p.Preset); pr != nil {
		if p.Icon == "" {
			p.Icon = pr.Icon
		}
		if p.Catalog == "" {
			p.Catalog = pr.Catalog
		}
		if p.Website == "" {
			p.Website = pr.Website
		}
		if p.KeysURL == "" {
			p.KeysURL = pr.KeysURL
		}
		// a region's own key page goes with its endpoints (Qianfan's pay
		// as you go makes its keys on the IAM page, the plans at the
		// plan console), and its own docs and catalog (Tencent Cloud's
		// TokenHub, models.dev's tencent-tokenhub, beside its plan's none)
		if r := p.regionOf(pr); r != nil {
			p.KeysURL = cmp.Or(r.KeysURL, p.KeysURL)
			p.Website = cmp.Or(r.Website, p.Website)
			// the catalog follows the region unless the user gave one
			// of their own: the preset's or a region's is magpie's
			if slices.ContainsFunc(pr.Regions, func(x Region) bool { return x.Catalog != "" }) &&
				(p.Catalog == pr.Catalog || slices.ContainsFunc(pr.Regions, func(x Region) bool { return x.Catalog == p.Catalog })) {
				p.Catalog = cmp.Or(r.Catalog, pr.Catalog)
			}
		}
	}
	return p
}

// Catalogs are the models.dev ids the provider's models are looked up in,
// first match wins: a gateway that resells several vendors names them all
// ("openai, deepseek").
func (p Provider) Catalogs() []string {
	return cleanList(strings.FieldsFunc(strings.ToLower(p.Catalog), func(r rune) bool { return r == ',' || r == ' ' }))
}

func cleanList(xs []string) []string {
	var out []string
	for _, x := range xs {
		if x = strings.TrimSpace(x); x != "" && !contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}

// cleanHeaders trims header names and values and drops entries with an empty
// name, returning nil when nothing is left so the field stays out of the JSON.
func cleanHeaders(h map[string]string) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		if k = strings.TrimSpace(k); k != "" {
			out[k] = strings.TrimSpace(v)
		}
	}
	out = userIDHeader(out)
	if len(out) == 0 {
		return nil
	}
	return out
}

// userIDHeader is h with a header named by digits alone and no value
// read as the new-api user id it is, New-Api-User: the user id typed in
// a header's name box, where magpie's old hint asked for a header
// (zxc56823 on Discord, "272298" with its value left empty). Sent as it
// was, the relay is told a header it never reads and still no user. One
// with a New-Api-User of its own already has it; the digits are dropped.
// h itself when it has none.
func userIDHeader(h map[string]string) map[string]string {
	var out map[string]string
	for k, v := range h {
		if strings.TrimSpace(v) != "" || !allDigits(strings.TrimSpace(k)) {
			continue
		}
		if out == nil {
			out = maps.Clone(h)
		}
		delete(out, k)
		has := false
		for n, w := range out {
			if strings.EqualFold(n, NewAPIUserHeader) && strings.TrimSpace(w) != "" {
				has = true
			}
		}
		if !has {
			for n := range out {
				if strings.EqualFold(n, NewAPIUserHeader) {
					delete(out, n)
				}
			}
			out[NewAPIUserHeader] = strings.TrimSpace(k)
		}
	}
	if out == nil {
		return h
	}
	return out
}

// NewAPIUserHeader is where a new-api relay's /api/user/self wants the
// user's id, beside the access token.
const NewAPIUserHeader = "New-Api-User"

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// baseOf is the URL saved for one of the editor's Base URL APIs (BaseAPI),
// "" for an API it doesn't know.
func (p Provider) baseOf(api string) string {
	switch api {
	case "chat":
		return p.Chat
	case "responses":
		return p.Responses
	case "anthropic":
		return p.Anthropic
	case "gemini":
		return p.Gemini
	case "decide":
		return p.Decide
	}
	return ""
}

// Base returns the base URL for a protocol, or "" when the vendor lacks it.
func (p Provider) Base(proto Protocol) string {
	switch proto {
	case Chat:
		// Vertex AI's partner models, on Google's OpenAI-compatible chat
		// completions at the project (VertexPartner)
		if p.IsVertex() {
			if b := p.vertexBase(); b != "" && !p.VertexExpress() {
				return b + "/endpoints/openapi"
			}
			return ""
		}
		return p.Chat
	case Responses:
		return p.Responses
	case Anthropic:
		// Vertex AI's Claude models, on Anthropic's Messages API at the
		// project, each at a path of its own (VertexClaudeRequest)
		if p.IsVertex() {
			if p.VertexExpress() {
				return "" // express mode serves Gemini alone
			}
			return p.vertexBase()
		}
		return p.Anthropic
	case CodeAssist:
		if p.Account != nil {
			return p.Account.codeAssist
		}
	case Gemini:
		// Factory's Gemini models are generateContent at /api/llm/g, not
		// Code Assist; Vertex AI's at the user's project (VertexPath)
		if p.FactoryGemini() {
			return factoryAPI + "/api/llm/g/v1"
		}
		if p.IsVertex() {
			return p.vertexBase()
		}
		return p.Gemini
	}
	return ""
}

// speaksLooked counts Speaks' looks, for a test.
var speaksLooked atomic.Int64

// Speaks lists the protocols the vendor serves natively, preferred first.
func (p Provider) Speaks() []Protocol {
	speaksLooked.Add(1)
	return slices.Collect(p.speaking)
}

// speaking is Speaks one protocol at a time, for a look that wants only
// the first (Host): a list built for it was 8 MB of what twenty polls of
// the Providers page allocated with real accounts.
func (p Provider) speaking(yield func(Protocol) bool) {
	// a Google sign-in speaks Code Assist, and only that
	if p.Account != nil && p.Account.codeAssist != "" && !p.IsPlugin() {
		yield(CodeAssist)
		return
	}
	for _, pr := range Protocols {
		if p.Base(pr) != "" && !yield(pr) {
			return
		}
	}
	// a plugin's Gemini models, beside what else it serves
	if p.IsPlugin() && p.Account.codeAssist != "" && !yield(CodeAssist) {
		return
	}
	// Factory's Gemini models, on generateContent. A model droid didn't
	// list stays on the other three (factoryAPIs); this is not one of them.
	// A custom provider's Gemini API comes after the others it has.
	// Vertex AI's Gemini models, on generateContent at its project, after
	// the chat completions its partner models are served on and the
	// Messages API its Claude models are (ListedAPIs gives each model its
	// one).
	if p.IsVertex() {
		if p.vertexBase() != "" {
			yield(Gemini)
		}
	} else if p.FactoryGemini() || p.Gemini != "" {
		yield(Gemini)
	}
}

// FactoryGemini is Factory's sign-in, whose Gemini models are asked on its
// own generate route (…/generate, the model in the body), not at
// models/{model}:streamGenerateContent as Google's Gemini API asks them.
func (p Provider) FactoryGemini() bool { return p.ID == "factory" && p.Account != nil }

// ResponsesFirst: an OpenAI model on OpenAI's API, Copilot's, PipeLLM's or Bedrock's,
// which is best asked on the Responses API though Chat serves it too; and
// every model where only the Responses API tells what the vendor's cache
// answered (ResponsesTellsCache).
func (p Provider) ResponsesFirst(model string) bool {
	if p.ResponsesTellsCache() {
		return true
	}
	if p.Responses != "" && p.IsBedrock() {
		return bedrockGPT(model)
	}
	// Azure OpenAI's deployments are named as the user likes; one named
	// for its model (gpt-5-codex, o4-mini) is taken for it
	if p.Responses == "" || (p.ID != "copilot" && HostOf(p.Responses) != "api.openai.com" && HostOf(p.Responses) != "api.pipellm.ai" && !p.IsAzure()) {
		return false
	}
	return openAIModel(model)
}

// ResponsesTellsCache is a provider whose Responses API says how much of
// the prompt its cache answered (input_tokens_details.cached_tokens) and
// what the model reasoned, while its Chat API, served by the same cache,
// says neither: ModelScope's API-Inference, by its preset or its host
// (#1578, sparrowbits: Claude Desktop, translated to Chat, read 0 of 34.9M
// tokens from cache; Codex on Responses, 94%). A translated request goes
// there on Responses, and an agent that asks each model on one API is
// wired to it. Other vendors with both URLs keep Chat: their Chat reports
// its cache (Groq, Volcengine Ark), their Claude-format requests go to
// their Anthropic URL as they are, or nothing is known of their Responses
// API (a relay the user added).
func (p Provider) ResponsesTellsCache() bool {
	if p.Responses == "" || p.Chat == "" {
		return false
	}
	return p.Preset == "modelscope" || HostOf(p.Responses) == "api-inference.modelscope.cn"
}

// MessagesFirst: a Claude model where the provider has Anthropic's Messages
// API, which is best asked there though its Chat or Responses API serves
// it too — a relay that serves both drops cache_control on Chat, so every
// turn was billed uncached (#997; ReturnTrue on Discord: 0% cache hits
// until the relay's OpenAI URL was left empty), and thinking with it.
func (p Provider) MessagesFirst(model string) bool {
	return p.Anthropic != "" && claudeModel(model)
}

// OnMessages: a Claude model MessagesFirst asks on Messages whatever API
// the client spoke, not only when the client's isn't served — unless the
// user set the API it is asked on, or the vendor's list names the APIs it
// serves it on (Copilot's Claude on Chat and Messages), where a request is
// relayed on the client's own API as before.
func (p Provider) OnMessages(model string) bool {
	if !p.MessagesFirst(model) {
		return false
	}
	if _, ok := p.ModelAPI(model); ok {
		return false
	}
	return p.ListedAPIs(model) == nil
}

// claudeModel is whether model is one of Anthropic's Claude models by its
// name, after any vendor prefix (anthropic/claude-sonnet-4.5) or as
// Bedrock names it.
func claudeModel(model string) bool {
	m := strings.ToLower(model[strings.LastIndex(model, "/")+1:])
	return strings.HasPrefix(m, "claude") || bedrockClaude(m)
}

// ClaudeModel is claudeModel for another package: whether model, after any
// vendor prefix, is one of Anthropic's Claude models by its name.
func ClaudeModel(model string) bool { return claudeModel(model) }

// openAIModel is whether model is one of OpenAI's own by its name: a GPT,
// a Codex or an o-series model, after any vendor prefix.
func openAIModel(model string) bool {
	m := strings.ToLower(model[strings.LastIndex(model, "/")+1:])
	return strings.HasPrefix(m, "gpt-") || strings.HasPrefix(m, "codex") ||
		len(m) > 1 && m[0] == 'o' && m[1] >= '0' && m[1] <= '9'
}

// Native is the API model is best asked on at this provider: one it serves
// the model on itself, so a request on it is relayed as it is rather than
// translated — Responses for a ChatGPT sign-in, or an OpenAI model on
// OpenAI's API or Copilot's; Anthropic's Messages for a Claude model
// where that is served; the API a custom provider's Base URL was given as
// (BaseAPI); Chat where that is served. "" when every
// request is translated anyway: a sign-in served through its agent's own
// API (Claude Code's binary, Devin, Code Assist).
func (p Provider) Native(model string) Protocol {
	if p.Account != nil {
		switch p.Account.Agent {
		case "claude", "devin":
			return ""
		}
	}
	apis := p.APIs(model)
	var out []Protocol
	for _, pr := range p.Speaks() {
		if slices.Contains(Protocols, pr) && (apis == nil || slices.Contains(apis, pr)) {
			out = append(out, pr)
		}
	}
	if len(out) == 0 {
		return ""
	}
	if p.ResponsesFirst(model) && slices.Contains(out, Responses) {
		return Responses
	}
	if p.MessagesFirst(model) && slices.Contains(out, Anthropic) {
		return Anthropic
	}
	// the API the user gave a custom provider's Base URL as, where it
	// serves the model: a relay whose Chat URL was found beside it (Detect
	// sets every API that answers) is still the Responses one they picked
	// (nb5p on Discord: dsh, Pi and droid asked its GPTs on Chat)
	if pr := Protocol(p.BaseAPI); pr != "" && slices.Contains(out, pr) {
		return pr
	}
	return out[0]
}

// Host is the vendor's API host, for display.
func (p Provider) Host() string {
	if p.Account != nil && p.Account.moved {
		return p.Account.wasHost // not plugin://<id>
	}
	for pr := range p.speaking {
		if u := p.Base(pr); u != "" {
			return HostOf(u)
		}
	}
	if p.Decide != "" {
		return HostOf(p.Decide)
	}
	return ""
}

// Where is what the provider's calls go to, as usage keeps it: the API's
// host, and for a subscription who is signed in there too. The id alone
// can't tell: it can be given to another vendor or account later.
func (p Provider) Where() string {
	h := p.Host()
	if p.Account != nil && p.Account.User != "" {
		if h == "" {
			return p.Account.User
		}
		return h + " as " + p.Account.User
	}
	return h
}

// IsOpenCode reports whether the provider is OpenCode's gateway (Zen or Go),
// which routes and caches by conversation and turns away requests that do
// not name one in x-opencode-session.
func (p Provider) IsOpenCode() bool { return openCodeAt(p.Host()) }

// openCodeAt is OpenCodeURL for a host as Host gives it, its port and
// any user aside, read without parsing a URL: the providers' page asks
// IsOpenCode for each model, and url.Parse there was 7 MB of what twenty
// window polls allocated.
func openCodeAt(h string) bool {
	if i := strings.LastIndexByte(h, '@'); i >= 0 {
		h = h[i+1:]
	}
	if strings.HasPrefix(h, "[") {
		return false // an IPv6 address
	}
	if i := strings.IndexByte(h, ':'); i >= 0 {
		h = h[:i]
	}
	return openCodeHost(h)
}

// IsBedrock reports whether the provider is Amazon Bedrock: its runtime
// (bedrock-runtime.{region}.amazonaws.com) or its Mantle endpoint
// (bedrock-mantle.{region}.api.aws), made from either's preset or at
// either's host. The two take the same Bedrock API key and share what
// Bedrock asks of a request: x-api-key alone on Messages, its own names
// for Anthropic's betas, no metadata.user_id, max_completion_tokens on
// chat completions, Claude on Messages and OpenAI's GPT models on
// Responses first. What only the runtime does (no list of models) is the
// preset's; what only Mantle does is IsBedrockMantle's.
func (p Provider) IsBedrock() bool {
	if p.Preset == "bedrock" || p.IsBedrockMantle() {
		return true
	}
	h := p.Host()
	return strings.HasPrefix(h, "bedrock-runtime.") && strings.HasSuffix(h, ".amazonaws.com")
}

// IsBedrockMantle reports whether the provider is Amazon Bedrock's Mantle
// endpoint (@marktoown on X): made from its preset, or added by its
// address at bedrock-mantle.{region}.api.aws.
func (p Provider) IsBedrockMantle() bool {
	return p.Preset == "bedrock-mantle" || mantleHost(p.Host())
}

// mantleHost is a host of Bedrock's Mantle endpoint.
func mantleHost(h string) bool {
	return strings.HasPrefix(h, "bedrock-mantle.") && strings.HasSuffix(h, ".api.aws")
}

// mantleOpenAI is a model Mantle serves on its OpenAI APIs under
// /openai/v1 rather than /v1, as the model's card says: OpenAI's closed
// GPT models (GPT-6 Sol, GPT-5.5) and xAI's Grok. gpt-oss, Gemma and the
// other open models are at /v1. Asked at the other one, Mantle answers
// "model `x` isn't supported on this route".
func mantleOpenAI(model string) bool {
	return bedrockGPT(model) || strings.HasPrefix(strings.ToLower(model), "xai.")
}

// MantleBase is the base a request for model goes to on Mantle's chat
// completions or Responses API, given a Mantle provider's base for it
// (IsBedrockMantle): one ending /v1 or /openai/v1 goes at whichever of the
// two serves the model, or at the other when other is set (the model was
// refused at the first, so the gateway learned its prefix). A base ending
// otherwise, one the user typed, is given back as it is.
func MantleBase(base, model string, other bool) string {
	root, ok := strings.CutSuffix(strings.TrimRight(strings.TrimSpace(base), "/"), "/v1")
	if !ok {
		return base
	}
	root = strings.TrimSuffix(root, "/openai")
	if mantleOpenAI(model) != other {
		return root + "/openai/v1"
	}
	return root + "/v1"
}

// mantleEndpoints fills in the APIs of Bedrock's Mantle endpoint a
// provider added by its address was saved without: chat completions and
// Responses at /v1, Messages at /anthropic, as the preset gives them. A
// chat or Responses URL given at /openai/v1 is moved to /v1, where Mantle
// lists its models (/openai/v1/models is 404); the gateway still asks each
// model at the prefix that serves it (MantleBase). Any other URL the user
// gave an API keeps it.
func (p *Provider) mantleEndpoints() {
	if p.Preset != "" && p.Preset != "bedrock-mantle" {
		return
	}
	root := ""
	for _, b := range []string{p.Chat, p.Responses, p.Anthropic} {
		if u, err := url.Parse(strings.TrimSpace(b)); err == nil && u.Scheme != "" && mantleHost(strings.ToLower(u.Hostname())) {
			root = u.Scheme + "://" + u.Host
			break
		}
	}
	if root == "" {
		return
	}
	for _, b := range []*string{&p.Chat, &p.Responses} {
		if strings.TrimRight(strings.TrimSpace(*b), "/") == root+"/openai/v1" {
			*b = root + "/v1"
		}
	}
	p.Chat = cmp.Or(p.Chat, root+"/v1")
	p.Responses = cmp.Or(p.Responses, p.Chat)
	p.Anthropic = cmp.Or(p.Anthropic, root+"/anthropic")
}

// bedrockClaude is a Bedrock id of a Claude model: a model id
// (anthropic.claude-opus-4-8) or an inference profile's
// (apac.anthropic.claude-opus-5-5).
func bedrockClaude(model string) bool {
	return strings.Contains(strings.ToLower(model), "anthropic.claude")
}

// bedrockGPT is a Bedrock id of one of OpenAI's closed GPT models
// (global.openai.gpt-6-luna), which its runtime serves on the Responses
// API as well as chat completions, and with function tools and reasoning
// on Responses alone (#176: "Function tools with reasoning_effort are not
// supported for global.openai.gpt-6-luna in /v1/chat/completions"). Not
// gpt-oss, which the runtime serves on chat completions alone.
func bedrockGPT(model string) bool {
	m := strings.ToLower(model)
	return strings.Contains(m, "openai.gpt-") && !strings.Contains(m, "gpt-oss")
}

// HostOf pulls the host out of a URL, for display.
func HostOf(u string) string {
	u = strings.TrimSpace(u)
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	}
	if i := strings.IndexAny(u, "/?#"); i >= 0 {
		u = u[:i]
	}
	return strings.ToLower(u)
}

// Mask hides all but the ends of a secret.
func Mask(s string) string {
	if len(s) <= 8 {
		return strings.Repeat("•", len(s))
	}
	return s[:4] + "…" + s[len(s)-4:]
}

// Ready reports whether the provider can be used: it has a key, needs
// none, is a signed-in agent, or is Vertex AI at a project (whose
// credentials are read when a request is signed).
func (p Provider) Ready() bool {
	if p.IsVertex() {
		// signed with a Google token, at the address its project makes
		return p.vertexBase() != ""
	}
	return p.Account != nil || p.Key != "" || keyOptional(p)
}

// On is whether the provider takes requests: ready, not switched off,
// not kept to its agent's own sign-in (KeptToAgent) and not a Remote
// magpie that is this magpie (SelfLeftOut).
func (p Provider) On() bool { return p.Ready() && !p.Off && !p.KeptToAgent() && !p.SelfLeftOut() }

// KeptToAgent is whether the provider is Claude's subscription with
// SwitchOnly on: its accounts are Claude Code's own sign-in alone, which
// magpie moves to the next account with room and back, and the gateway
// sends no request to them, so none goes through Claude Code's SDK run,
// which spends more of the allowance than Claude Code's own sessions
// (myduso on Discord). Codex's SwitchOnly only keeps failover out of its
// config.
func (p Provider) KeptToAgent() bool {
	return p.SwitchOnly && p.ID == "claude" && p.Account != nil && p.Account.Agent == "claude"
}

// SetOff switches a provider off, or on again (see Provider.Off). Saving
// it brings the model lists written into agents' files up to date.
func SetOff(id string, off bool) error {
	p, err := Find(id)
	if err != nil {
		return err
	}
	p.Off = off
	return Save(*p)
}

// SwitchedOff is the provider switched off in magpie that a model id
// names: as "provider/model", or a model only switched-off providers list.
// A request for it is refused as that, not as a model magpie doesn't know.
func SwitchedOff(id string) (Provider, bool) {
	id = strings.TrimSuffix(strings.TrimSpace(id), "[1m]")
	if pid, _, ok := strings.Cut(id, "/"); ok {
		if p, err := Find(pid); err == nil && (p.Off || p.KeptToAgent() || p.SelfLeftOut()) {
			return *p, true
		}
	}
	for _, p := range All() {
		if !p.Off && !p.KeptToAgent() && !p.SelfLeftOut() || !p.Ready() {
			continue
		}
		if slices.ContainsFunc(p.Exposed(), func(m catalog.Model) bool { return m.ID == id }) {
			return p, true
		}
	}
	return Provider{}, false
}
