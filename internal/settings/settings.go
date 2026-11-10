// Package settings keeps the few preferences the desktop app has: which
// palette to paint with, which language to speak, how the agents are
// arranged. Everything else magpie knows is derived from the agents' own
// files.
//
// The file is ~/.config/magpie/settings.json; a missing file means "follow
// the system" for both.
package settings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"net/url"
	"os"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/capre"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/fonts"
	"github.com/yetone/magpie/internal/lastgood"
	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/redact"
	"github.com/yetone/magpie/internal/steady"
)

// CodexAutoReviewSession is the CodexAutoReview that runs Codex's
// auto-review on the session's own model.
const CodexAutoReviewSession = "session"

// CodexSubagentLead is CodexSubagentModel's value that puts every Codex
// subagent on the model its lead's own turns go on, whatever the lead
// names for it in spawn_agent.
const CodexSubagentLead = "lead"

// CodexTierStandard is the CodexServiceTier that sends Codex's requests
// with no service tier, the standard one, whatever Codex asked; the
// sentinel codex-rs itself uses for that (SERVICE_TIER_DEFAULT_REQUEST_VALUE).
const CodexTierStandard = "default"

// CodexServiceTiers are the values CodexServiceTier may take, "" (as
// Codex asks) first.
var CodexServiceTiers = []string{"", CodexTierStandard, "priority", "ultrafast"}

// Settings is what the user chose. "" and "system" both mean "follow the OS".
type Settings struct {
	Theme string `json:"theme,omitempty"` // system | light | dark
	// Colorblind switches chart series (--c1…--c7) to an Okabe–Ito palette
	// and gives each quota curve window, and its legend key, a dash of its
	// own (#2073).
	Colorblind bool `json:"colorblind,omitempty"`
	// ReduceMotion stills magpie's animations as the system's Reduce
	// Motion does, for a computer or phone that the moving Routing page
	// keeps busy (#3813).
	ReduceMotion bool `json:"reduceMotion,omitempty"`
	// NoBird keeps magpie's bird off the loading skeletons and the
	// Plugins page's README, for a small server where its watch for them
	// is work enough to matter (Yayoi_no_yume on X). Off means shown.
	NoBird bool   `json:"noBird,omitempty"`
	Lang   string `json:"lang,omitempty"` // system | en | zh
	Tray   string `json:"tray,omitempty"` // what clicking the tray icon opens: panel | window
	// PanelShortcut is the key combination that opens the tray's quick
	// panel from any app (ben hanks on Discord), as Wails names it
	// ("Cmd+Shift+M"); "" is none, the default, so nothing clashes until
	// the user records one. This computer's own (KeepOwn): a Mac's Cmd is
	// another keyboard's Win key, and what is free differs per computer.
	PanelShortcut string `json:"panelShortcut,omitempty"`
	// SessionTerminal is the terminal that opens a resumed session or an
	// agent: a Mac app by bundle id, on Windows wt, pwsh or powershell, on
	// Linux the terminal's program. "" and "system" are the system's (a
	// Mac's .command file association). This computer's own (KeepOwn):
	// another computer may not have that terminal.
	SessionTerminal string `json:"sessionTerminal,omitempty"`
	// Currency is what a cost — the Usage page's, the tray panel's, the
	// TUI's and the CLI's — is shown converted to: usd (its native
	// currency, list prices being in dollars) or cny, at a live exchange
	// rate (see internal/fx). A vendor's own balance, already in its own
	// currency (a Chinese relay's ¥), is never touched by this.
	Currency string `json:"currency,omitempty"`
	// ChineseUnits shortens a large count in 万 and 亿 when magpie speaks
	// Chinese (8000 万 rather than 80M); otherwise it is in K, M and B, as
	// prices per million tokens and context windows (200K, 1M) are, in
	// Chinese too (#740). It means nothing in English. It replaces
	// westernUnits, which asked for K/M/B: what that chose is now the
	// default, so an old file's is left unread.
	ChineseUnits bool `json:"chineseUnits,omitempty"`
	// Dock keeps magpie in the Mac's Dock as well as the menu bar, for a
	// menu bar too full to show its icon.
	Dock bool `json:"dock,omitempty"`
	// DockWindow is the With window choice: magpie is in the Dock, and so
	// in Command-Tab, only while its window is open. It is what magpie
	// does with neither Dock nor DockNever set (ShouguangCao on X: the
	// window was out of Command-Tab, so going back to it to paste a key
	// copied elsewhere took the menu bar), so it is still written for an
	// older magpie reading the file, which took none for Hide.
	DockWindow bool `json:"dockWindow,omitempty"`
	// DockNever keeps magpie out of the Dock and Command-Tab even while
	// its window is open, the Hide choice. Dock wins over it.
	DockNever bool `json:"dockNever,omitempty"`
	// DockSettled says this file's Dock choice is read with With window as
	// the default. A file written before that (v0.1.2060) has none, and
	// Hide was its default, stored as nothing: a user who chose Hide can't
	// be told from one who never chose, so such a file with no choice is
	// read as Hide (settleDock), never moved into the Dock by the new
	// default. A new install, with no file, is settled from the start.
	// Every save writes it.
	DockSettled bool `json:"dockSettled,omitempty"`
	// Proxy for magpie's own requests to vendors: "" follows the
	// environment and then the system, "direct" uses none, anything else
	// is the proxy (http://, https:// or socks5://; host:port means http).
	Proxy string `json:"proxy,omitempty"`
	// ProxyCustom is the last proxy of the user's own Proxy held, kept
	// while Proxy is "" or "direct" so that choosing Custom again in
	// Settings brings back the address and login typed (Zane on Discord).
	// Save sets it from Proxy, and it is stored as Proxy is. This
	// computer's own (KeepOwn), as Proxy is.
	ProxyCustom string `json:"proxyCustom,omitempty"`
	// PluginProxy is the proxy magpie downloads plugins through: the
	// plugins list, npm's registry, GitHub, the plugins repo's dist
	// branch, Bun's own download, and bun add, update and git clone
	// (JonahFu on X: GitHub needs a proxy where the vendors don't). ""
	// follows Proxy, as every download did before; "direct" uses none;
	// anything else is the proxy, as Proxy takes it. A plugin's requests
	// to its vendor are API requests and follow Proxy (and the
	// provider's or account's own), never this. This computer's own
	// (KeepOwn), as Proxy is.
	PluginProxy string `json:"pluginProxy,omitempty"`
	// PluginProxyCustom is PluginProxy's ProxyCustom.
	PluginProxyCustom string `json:"pluginProxyCustom,omitempty"`
	OTel              OTel   `json:"otel,omitempty"`
	// GitHubToken is a GitHub token the library's requests to GitHub's
	// API carry, raising its rate limit from 60 requests an hour to 5,000.
	// It is a secret: the Settings page is told only a masked one, and a
	// backup or sync without keys leaves it out, as it does LANKey.
	GitHubToken string `json:"githubToken,omitempty"`
	// Redact keeps secrets in what agents send (API keys, private keys,
	// tokens, passwords) from the vendors behind magpie: they go as
	// placeholders, and come back as they were. RedactPersonal does the same
	// for emails, phone numbers and ID and bank card numbers, and
	// RedactWords for the user's own words. RedactRules are the user's own
	// rules for secrets magpie's don't know (a gateway's oc_sk_… key), a
	// prefix or a pattern each, masked with the secrets while Redact is on.
	// RedactKinds turns the other kinds of personal data on or off one by
	// one, by redact.Categories' ids (ssn, ip, home…), under
	// RedactPersonal: only the user's choices, a kind not in it is as its
	// category's On says. RedactModel has a local model (internal/pii) find
	// what the rules can't as well, a name or an address in free text,
	// under Redact and RedactPersonal; it is this computer's own, as the
	// model is downloaded on it. RedactShape sends a key as a made-up key
	// of its own form (sk_… as another sk_…) instead of a placeholder
	// (redact.Options.Shape; Charles Cao on X). RedactModelName is the
	// local model picked (pii.PrivacyFilter, pii.AstrLinkGuard); "" for
	// none picked, which pii.Choose reads. It is this computer's, as
	// RedactModel is. RedactModelScore is each model's threshold the user
	// set, by its id: masking drops what the model is less sure of (an
	// AstrLink Guard user on X found 0.85 to 0.95 right for it). A model
	// not in it has its own default (pii.Model.MinScore); this
	// computer's too. RedactModelOff is the user turning the model off
	// (Settings → Privacy's Off, a download cancelled, the model deleted):
	// since 2026-10-28 it is on by default (masking.ModelWanted), and
	// RedactModel says the user turned it on. RedactModelAuto says the
	// default downloaded AstrLink Guard: a user who had a model on disk
	// with RedactModel off before then had turned it off, and this one
	// hasn't.
	Redact           bool               `json:"redact,omitempty"`
	RedactModelScore map[string]float64 `json:"redactModelScore,omitempty"`
	RedactShape      bool               `json:"redactShape,omitempty"`
	RedactModel      bool               `json:"redactModel,omitempty"`
	RedactModelName  string             `json:"redactModelName,omitempty"`
	RedactModelOff   bool               `json:"redactModelOff,omitempty"`
	RedactModelAuto  bool               `json:"redactModelAuto,omitempty"`
	RedactPersonal   bool               `json:"redactPersonal,omitempty"`
	RedactKinds      map[string]bool    `json:"redactKinds,omitempty"`
	RedactWords      []string           `json:"redactWords,omitempty"`
	// RedactWordsAnyCase matches RedactWords in any case (ZoeCats on
	// Discord); off, the default, a word is matched as written.
	RedactWordsAnyCase bool `json:"redactWordsAnyCase,omitempty"`
	// RedactWordsWhole matches RedactWords only as whole words (acmilan_
	// on Discord); off, the default, a word is found inside longer ones too.
	RedactWordsWhole bool          `json:"redactWordsWhole,omitempty"`
	RedactRules      []redact.Rule `json:"redactRules,omitempty"`
	// RedactActions is what is done with a value of a kind (redact.Hit's
	// Kind: API_KEY, EMAIL, a rule's own…) other than masking it, where
	// the user chose: redact.Warn sends it as written and records it,
	// redact.Block holds the request back. RedactAllow are values sent
	// as written whatever finds them, each whole, in any case (#1977).
	RedactActions map[string]string `json:"redactActions,omitempty"`
	RedactAllow   []string          `json:"redactAllow,omitempty"`
	// RedactRouteTo is the model an agent's request goes to, as written,
	// when masking would change it (jessyoungyu on X: 「百度这家公司怎么样」
	// reached a cloud model as a placeholder): "" masks it as before. Only
	// a model whose every route the user set to go unmasked counts
	// (provider.SkipsRedaction, the gateway's unredactedRoute), judged on
	// each request; one that isn't now is as unset, masked.
	RedactRouteTo string `json:"redactRouteTo,omitempty"`
	// LAN shares the gateway on the local network; remote callers must use
	// named caller keys. LANKey is retained for older Magpie versions.
	// LAN is this computer's own (KeepOwn; Lullaby on Discord: sharing
	// turned on at home came on at work too). LANKey and LANKeyID name the
	// default key, which rides the synced gateway-key store, so they follow
	// the folder: turned on on another computer, sharing there takes up the
	// same key.
	LAN    bool   `json:"lan,omitempty"`
	LANKey string `json:"lanKey,omitempty"`
	// CORSOrigins are the web pages (scheme://host[:port]) and browser
	// extensions (chrome-extension://<id>, moz-extension://<uuid>,
	// safari-web-extension://<uuid>) whose scripts may call the gateway
	// from a browser (#1051): a preflight from one is
	// answered, and its calls carry the CORS headers that let it read the
	// reply, each with an enabled gateway key. None by default: a page
	// gets no CORS headers, as before.
	CORSOrigins []string `json:"corsOrigins,omitempty"`
	// AllowIPs are the addresses (IPs and CIDR ranges) other computers may
	// reach the gateway from at all (lc on Discord); any other is refused
	// 403 even with a gateway key. None: every address, as before. This
	// computer is always let in. MAGPIE_ALLOW_IPS, where set, is used in
	// its place (gateway.AllowIPs). This computer's own (KeepOwn), as LAN
	// is: the addresses are of the network this computer is on.
	AllowIPs []string `json:"allowIPs,omitempty"`
	// MTLS has other computers show a client certificate magpie issued
	// (`magpie mtls`, Settings → Network and sharing) before anything is
	// answered (hhdaxia on X, #5596): the gateway and magpie web's page
	// are served over TLS with magpie's own CA, and a remote request
	// without a good certificate is refused. This computer's own
	// (KeepOwn), as LAN is.
	MTLS bool `json:"mtls,omitempty"`
	// FetchPrivate are the private hosts and ranges (a name, name:port, an
	// IP or a CIDR range) magpie may download a URL from that it didn't
	// choose itself: an image or video a request names or a vendor answers
	// with, or a page web_fetch reads. None by default: such a URL is
	// downloaded from public addresses only, whoever sent the request and
	// from wherever (gateway.publicClient). In settings.json only; it names
	// this computer's own network, so it isn't synced (KeepOwn).
	FetchPrivate []string `json:"fetchPrivate,omitempty"`
	// RouterNote is what this computer's MCP router says of itself to the
	// agents that connect to it (packinggoodsllc_07668 on Discord: two
	// magpies, one in China and one in Los Angeles, serve the same tools,
	// and an agent fetching Google should pick the one in Los Angeles):
	// in its initialize instructions, its serverInfo title, and before each
	// tool's description (gateway.hubWhere). "" is this computer's name, said
	// only to an agent on another computer. This computer's own (KeepOwn).
	RouterNote string `json:"routerNote,omitempty"`
	// RouterAllTools gives every agent the router's tools whole. Off, an
	// agent that doesn't search its MCP tools itself and is given more
	// than a few is given two in their place, one that finds a tool and
	// one that runs it (gateway.hubOnDemand; ArvinCharl on X: the tools
	// filled the context). This computer's own (KeepOwn).
	RouterAllTools bool `json:"routerAllTools,omitempty"`
	// Port is the gateway's port on this computer, 0 for DefaultPort.
	// MAGPIE_ADDR, where it is set, comes first (GatewayAddr).
	Port int `json:"port,omitempty"`
	// LANKeyID remembers the default named key created when sharing is enabled.
	LANKeyID string `json:"lanKeyId,omitempty"`
	// RequestArchive keeps each call the gateway serves — its headers and
	// bodies both ways, secrets taken out — in the S3 bucket sync keeps
	// its backup in (gateway/archive.go), for looking into a request later.
	RequestArchive bool `json:"requestArchive,omitempty"`
	// RequestArchiveMaxMB is how much of each body the archive keeps, in
	// MiB: 0 for 32, at most 1024 (#447)
	RequestArchiveMaxMB int `json:"requestArchiveMaxMB,omitempty"`
	// RequestArchiveLocal keeps the request archive on this computer's disk,
	// in magpie's folder (gateway/archive_local.go), while sync has no S3
	// bucket for it (CyberCaius on Discord). Its own switch, separate from
	// RequestArchive, which sync carries to every computer: this computer's
	// own (KeepOwn), so a request body is written to a disk only where the
	// user turned it on.
	RequestArchiveLocal bool `json:"requestArchiveLocal,omitempty"`
	// RequestArchiveLocalMB is how large the archive on this computer grows
	// before its oldest calls are removed, in MiB: 0 for 1024. In
	// settings.json only; this computer's own (KeepOwn).
	RequestArchiveLocalMB int `json:"requestArchiveLocalMB,omitempty"`
	// GatewayConversations is local, opt-in recording of session traffic.
	// Only SetGatewayConversations changes it; other saves keep local consent.
	GatewayConversations bool `json:"gatewayConversations,omitempty"`
	// SessionReads is which agents' own session files magpie may read
	// (RustCats on X: a first open listed every agent's history unasked).
	// Nil is not settled yet (sessions.SettleReads decides it once). Only
	// SetSessionReads changes it; other saves keep what the file holds,
	// and it is this computer's own (KeepOwn): its agents and their files
	// are this computer's.
	SessionReads *SessionReads `json:"sessionReads,omitempty"`
	// CodexWarmup starts a ChatGPT account's next window as soon as the
	// last one resets, with one tiny request, so it counts from then (a
	// Codex window starts at its first use): "" off, "week" the weekly
	// window, "all" the 5-hour one too.
	CodexWarmup string `json:"codexWarmup,omitempty"`
	// ClaudeWarmup is CodexWarmup for the Claude accounts, the request
	// sent through Claude Code.
	ClaudeWarmup string `json:"claudeWarmup,omitempty"`
	// CodexWarmAt starts each ChatGPT account's 5-hour window at a time of
	// day of the user's choosing, local "15:04", with the same tiny
	// request: an account whose 5-hour window isn't running then gets one,
	// so the windows line up with the day (06:00 gives three by 21:00, where
	// the first use at 9 gives two by the end of it); "" off. It works
	// with CodexWarmup or without it. ClaudeWarmAt is the Claude accounts'.
	// Each is CodexWarmAts' (ClaudeWarmAts') first, all a magpie before
	// them read: a file with it and no list has that one time, and it is
	// kept written for an older magpie to go on starting the day's first.
	CodexWarmAt  string `json:"codexWarmAt,omitempty"`
	ClaudeWarmAt string `json:"claudeWarmAt,omitempty"`
	// CodexWarmAts are the times of day CodexWarmAt's start is made at,
	// each once a day, earliest first: 09:00, 15:05 and 19:10 start a
	// window at each, where it isn't running (#1260); none when empty.
	// ClaudeWarmAts is the Claude accounts'.
	CodexWarmAts  []string `json:"codexWarmAts,omitempty"`
	ClaudeWarmAts []string `json:"claudeWarmAts,omitempty"`
	// CodexWarmAtOf is a ChatGPT account's own time of day for that, by
	// its name in lower case, "off" for none: two accounts started hours
	// apart take over from one another, where at one time they run out
	// together (#957). An account not in it has CodexWarmAt.
	CodexWarmAtOf map[string]string `json:"codexWarmAtOf,omitempty"`
	// CodexAutoReset are the ChatGPT accounts (lower-case) that spend one
	// of their rate-limit resets by themselves once their weekly window is
	// used up and no other account can take the request: at most one a
	// week each (see provider.AutoUseCodexReset); and one about to run out
	// unused CodexResetLead before it does, or at once when the account is
	// held up past then (provider.SpendExpiringCodexResets).
	CodexAutoReset []string `json:"codexAutoReset,omitempty"`
	// CodexAutoResetOnly narrows an account in CodexAutoReset to one of
	// those two times (#2078, alanwalk: Codex's own resets come often, so
	// a week used up is worth waiting out, while a card about to run out
	// isn't): "week" for its week used up only, "expiring" for the one
	// about to run out only. An account not in it uses one at both, as
	// every account did before the two were split.
	CodexAutoResetOnly map[string]string `json:"codexAutoResetOnly,omitempty"`
	// CodexResetLead is how many minutes before a reset about to run out
	// unused an account with CodexAutoReset's "expiring" spends it (the
	// owner, 10-30: 把提前用卡的时间做成可配置的): from CodexResetLeadLeast
	// to CodexResetLeadMost, 0 for CodexResetLeadDefault. Earlier loses
	// what the windows have left then (#718), so the default is late.
	// One for every account: whether an account spends one is its own
	// say, how early is the same trade for each. Read through
	// ResetLead, which takes a value out of range for the default.
	CodexResetLead int `json:"codexResetLead,omitempty"`
	// CodexNoCredits are the ChatGPT accounts (lower-case) that don't
	// spend their credits: once a usage window is used up, routing holds
	// the account as used up and goes on to the user's other accounts,
	// groups and fallbacks, where by default the vendor answers on its
	// credits (see provider.CodexCredits).
	CodexNoCredits []string `json:"codexNoCredits,omitempty"`
	// CodexCreditsLast are the ChatGPT accounts (lower-case) that spend
	// their credits only after everything else: once a usage window is
	// used up, routing tries the account after every account, key and
	// fallback not on its credits, instead of in its place. Not one of
	// CodexNoCredits: an account set not to spend them is held.
	CodexCreditsLast []string `json:"codexCreditsLast,omitempty"`
	// CodexCreditsFirst are the ChatGPT accounts (lower-case) that spend
	// their credits before routing moves to another account: a window used
	// up doesn't send requests or Codex's sign-in elsewhere while the
	// account goes on on its credits (jojonas_xyz on X). Not one of
	// CodexNoCredits nor CodexCreditsLast.
	CodexCreditsFirst []string `json:"codexCreditsFirst,omitempty"`
	// WorkBuddyCheckin presses WorkBuddy's daily check-in (签到) for each
	// signed-in WorkBuddy (China) account once a Beijing day, claiming the
	// credits it gives while its event runs.
	WorkBuddyCheckin bool `json:"workbuddyCheckin,omitempty"`
	// TraeCheckin presses Trae CN's daily check-in (每日签到) for each
	// signed-in Trae CN account (its plugin's) once a Beijing day, claiming
	// the credits it gives.
	TraeCheckin bool `json:"traeCheckin,omitempty"`
	// MiniMaxCheckin presses MiniMax Code's daily check-in (签到) for each
	// signed-in MiniMax Code (China) account (its plugin's) once a Beijing
	// day, claiming the credits it gives (#811).
	MiniMaxCheckin bool `json:"minimaxCheckin,omitempty"`
	// QoderCheckin claims Qoder's daily credits for each signed-in Qoder
	// and Qoder CN account (its plugin's) once a Beijing day (ARNO on
	// Discord).
	QoderCheckin bool `json:"qoderCheckin,omitempty"`
	// PluginCheckins turns a plugin's own daily check-in (auth.checkin)
	// on or off, by the plugin's provider id; one not set follows its
	// vendor's switch above, which the plugin took over, else is off.
	PluginCheckins map[string]bool `json:"pluginCheckins,omitempty"`
	// PluginOptions are the user's values of the switches plugins declare
	// for their providers (auth.options, #2038): provider id → the
	// option's key → on. A key not here takes the plugin's default.
	PluginOptions map[string]map[string]bool `json:"pluginOptions,omitempty"`
	// CheckinOff are the accounts left out of their vendor's daily
	// check-in while it is on (#1902), each by its entry in the vendor's
	// check-in file: "<site or provider>|<account id>" ("workbuddy|<uid>",
	// "trae-cn|<uid>", a plugin's "<provider>|<sign-in key>", e.g.
	// "autoclaw|autoclaw#x1y2z3"; before plugins#254 a plugin's was
	// "<provider>|<display name>", which provider.adoptLegacyPluginCheckins
	// carries over).
	CheckinOff []string `json:"checkinOff,omitempty"`
	// CheckinAt is the local time of day ("15:04") every daily check-in,
	// built-in and plugin, waits for before it asks for the vendor's day
	// (times_ang on X: 可以调整自动签到的时间吗); "" asks as the day turns,
	// as before. It holds the day's first ask only: the vendor's own turn
	// (a plugin's until) and the tries after it are kept, so no day is
	// skipped (provider.checkinGate).
	CheckinAt string `json:"checkinAt,omitempty"`
	// ReplyModel is what a reply's model name says when the request went
	// out under another name than the one asked for (a group's member, a
	// model's own id, a provider/model id): "" the model the agent asked
	// for (issennknight on Discord: an agent warned that
	// deepseek-v4.1-flash came back as deepseek-v4.1-flash-1017), "member"
	// magpie's provider/model id of the member that answered
	// (workbuddy/glm-5.3-flash), for agents that count usage by it (#822),
	// "vendor" the vendor's own text, as every reply said before the
	// asked-for name (ZoeCats on Discord). One the vendor named as asked
	// keeps the vendor's name, a dated one too. Each is for every agent,
	// Claude Code, Claude Desktop and Codex included (gateway's
	// replyNamingOf). A request's X-Magpie-Response-Model says it for
	// that request alone. "asked" is read as "".
	ReplyModel string `json:"replyModel,omitempty"`
	// MemberModel is ReplyModel "member" as a magpie before ReplyModel
	// kept it: read as that when ReplyModel isn't set, and written beside
	// it for such a magpie (normal).
	MemberModel bool `json:"memberModel,omitempty"`
	// NoLoopGuard lets a streamed reply run on when its reasoning or text
	// is stuck in a loop of the same few lines (#1359). Off, as by
	// default, the gateway ends such a reply with an error.
	NoLoopGuard bool `json:"noLoopGuard,omitempty"`
	// ClaudeLeanPrompt starts a Claude subscription's Claude Code run with
	// the caller's own instructions as its system prompt
	// (--system-prompt-file), in place of Claude Code's (about 1.1K tokens
	// a request on Sonnet 5.5), and not in its first message. Still the
	// real Claude Code, its billing line and identity line kept; off, as by
	// default, the run is the shape every Claude Code run has.
	ClaudeLeanPrompt bool `json:"claudeLeanPrompt,omitempty"`
	// ClaudePrintMode runs a Claude subscription's Claude Code as `claude
	// -p` always. Off, as by default, a model request runs in Claude
	// Code's interactive mode, as a person runs it, and never as -p: the
	// plan counts a -p request at about twice an interactive one, and a -p
	// run starts without the interactive mode's cache. A request it can't
	// take (safeguards, a JSON schema, …), or one it doesn't start for,
	// fails saying why. Reading the plan's allowance (/usage, no model
	// request) is -p either way. (An "Interactive only" claudeTUIOnly,
	// v0.1.2448, is what Off is now, and is no longer read.)
	ClaudePrintMode bool `json:"claudePrintMode,omitempty"`
	// ClaudeRunsMost is how many Claude Code processes a Claude
	// subscription may have running at once: conversations' runs, those
	// waiting for a next turn or on tool results, and one-off tests and
	// allowance reads alike. 0, as by default, has magpie size it from
	// this machine's memory on Linux (its cgroup's limit, else MemTotal),
	// and leaves it unbounded elsewhere: each Claude Code holds about
	// 100 MiB of its own, and a 512 MiB service was OOM-killed (#2956).
	// It is this machine's, not taken from another's settings.
	ClaudeRunsMost int `json:"claudeRunsMost,omitempty"`
	// RefusalRetries is how many more times a request the vendor's safety
	// filter refused, with nothing said, is asked of another account or
	// routing group member (#248), unset for every one left. 0 hands the
	// refusal straight back: to an agent on Anthropic's Messages API as
	// the vendor's own reply, stop_reason "refusal", which Claude Code
	// answers by switching to its fallback model itself (DD on Discord).
	RefusalRetries *int `json:"refusalRetries,omitempty"`
	// RetryAfterCap is the longest Retry-After, in seconds, an agent is
	// told (#5371, tlerbao): a plan's quota running out rests its account
	// until the plan's reset, hours away, and an agent with a fallback
	// chain of its own waited that long before it moved on. The account
	// rests as long either way. Unset, the wait goes out as it is.
	RetryAfterCap *int `json:"retryAfterCap,omitempty"`
	// NoAccountDiscovery leaves every agent's sign-in unread (#1535,
	// MaxHaiCom): no credential file, Keychain item or agent CLI is asked
	// who is signed in, and no subscription is offered; API-key providers
	// work as before (see AccountDiscoveryOff, provider/discovery.go). Off,
	// as by default, subscriptions are found as ever. This computer's own
	// (KeepOwn), set only by its own switch or `magpie accounts discovery`.
	NoAccountDiscovery bool `json:"noAccountDiscovery,omitempty"`
	// SignInLinkOnly keeps an account's sign-in page out of the default
	// browser: the window (and the command line) shows its link to copy
	// into a browser the user picks, a separate or fingerprint browser
	// (max2020 on Discord). Off, as by default, the page opens as ever. A
	// plugin whose own CLI opens its page (Grok Build on macOS and
	// Windows) still does: magpie can't stop that.
	SignInLinkOnly bool `json:"signInLinkOnly,omitempty"`
	// NoStats stops the one event a day that counts magpie's users (see
	// internal/stats).
	NoStats bool `json:"noStats,omitempty"`
	// NoUsageStats keeps which agents, providers and models magpie is
	// used with out of that event, which then counts the user only.
	NoUsageStats bool `json:"noUsageStats,omitempty"`
	// NoUpdatePill keeps the header's Update pill away when a newer magpie
	// is out; UpdateSkip is the one version it was hidden for, and a newer
	// one brings it back. Either way magpie still downloads the version and
	// puts it in as it quits, and Settings' version row still offers it.
	NoUpdatePill bool   `json:"noUpdatePill,omitempty"`
	UpdateSkip   string `json:"updateSkip,omitempty"`
	// NoAutoUpdate stops magpie asking for a newer version by itself, and
	// so downloading one (#472): only Settings' Check, or magpie update,
	// asks then. UpdateEvery is how often it asks otherwise, in minutes:
	// one of UpdateEveries, 0 for every six hours.
	NoAutoUpdate bool `json:"noAutoUpdate,omitempty"`
	UpdateEvery  int  `json:"updateEvery,omitempty"`
	// NoAutoDownload has a check, by the clock or Settings' Check, only
	// say a newer version is out (with its notes and size) rather than
	// download it (#2296): a metered network or a proxy billed by traffic
	// downloads it only on a click (Download update, the header's Update,
	// magpie update). Off, as by default, a newer version is downloaded
	// as soon as it is found.
	NoAutoDownload bool `json:"noAutoDownload,omitempty"`
	// AutoRestart has the app restart into an update it downloaded by
	// itself, once the gateway it serves has nothing in flight, rather than
	// waiting for a click or for quitting (#2096). Off by default: a
	// restart ends the window and whatever an agent sends next waits for
	// the gateway to be back. Nothing happens with NoAutoUpdate on.
	AutoRestart bool `json:"autoRestart,omitempty"`
	// UpdateMirror is a GitHub download mirror an update's file is fetched
	// through, its prefix put before the release's github.com URL
	// (https://mirror.example/https://github.com/…): "" for none, which is
	// the default — magpie names no mirror of its own. The file is still
	// checked against the SHA-256 the update feed at usemagpie.ai gives,
	// never one from the mirror. magpie update mirror sets it.
	UpdateMirror string `json:"updateMirror,omitempty"`
	// UpdateChannel is which versions an update is offered: "" every
	// version as it comes out, the default; UpdateStable ("stable") one a
	// week, the newest that had been out three days as the week began, or
	// an urgent one (a security hole, a ban risk, lost data) at once, from
	// the update feed's ?channel=stable (ssharpcc on X). Settings' Update
	// channel and magpie update channel set it.
	UpdateChannel string `json:"updateChannel,omitempty"`
	// Vision is the model that describes an image to a model that can't see
	// it: a model's id (provider/model, group/<id>), "off" to turn such an
	// image away, or empty for one magpie picks (see gateway.seer).
	Vision string `json:"vision,omitempty"`
	// VisionEffort is how hard the Image recognition model thinks as it
	// reads an image (MOMO on Discord): a level of catalog.EffortRank,
	// fitted to the model's own levels when it is asked, or empty for
	// magpie's choice, the least the model has (gateway.visionEffort).
	VisionEffort string `json:"visionEffort,omitempty"`
	// ImageGen is the model magpie's generate_image tool draws with (the
	// gateway's /v1/images/generations when a request names no model): a
	// model's id, "off", or empty for one magpie picks (gateway.drawer).
	// "off" turns the video a request that names no model gets off too
	// (gateway.videomaker).
	ImageGen string `json:"imageGen,omitempty"`
	// VideoGen is the model a video request that names none is made with
	// (generate_video, the gateway's /v1/videos): a model's id, or empty
	// for one magpie picks (gateway.videomaker). ImageGen's "off" turns it
	// off too.
	VideoGen string `json:"videoGen,omitempty"`
	// ImageTimeout is how long, in minutes, a vendor is given to draw an
	// image and send it (neighbads on X: a slow image endpoint needs more
	// than 5): from 1 to ImageTimeoutMost, 0 for ImageTimeoutDefault.
	// ImageWait reads it.
	ImageTimeout int `json:"imageTimeout,omitempty"`
	// ClassifyTimeout is how long, in seconds, a routing group's classifier
	// is given to say which intent a turn is (#2934: a Kiro subscription's
	// haiku needed more than 8): from 1 to ClassifyTimeoutMost, 0 for
	// ClassifyTimeoutDefault. ClassifyWait reads it.
	ClassifyTimeout int `json:"classifyTimeout,omitempty"`
	// GroupSink, GroupFirstToken and GroupPatience are what every routing
	// group does that says nothing of its own (jiezhengj on Discord: set
	// once, not in each group), as provider.Group's Sink, FirstToken and
	// Patience: one rate limited with quota left goes to the back; the
	// seconds a member may take to its first token, 0 to wait; the seconds
	// a request is kept while every member is busy, 0 for
	// provider.PatienceDefault and -1 for off. A group that sets its own
	// keeps it (provider.Group.Sinks, FirstTokenOr, Waits).
	GroupSink       bool `json:"groupSink,omitempty"`
	GroupFirstToken int  `json:"groupFirstToken,omitempty"`
	GroupPatience   int  `json:"groupPatience,omitempty"`
	// BusyLoad puts a model whose plugin reports it busier than this
	// percent behind the others it could go to (plugin.ModelLoad,
	// axelburks on Discord): from 1 to BusyLoadMost-1, past 100 for a
	// vendor that queues, 0 for off. A model with no
	// load reported, or one older than plugin.LoadFresh, keeps its place.
	BusyLoad int `json:"busyLoad,omitempty"`
	// Searcher is the provider that searches the web for a model that
	// can't: "<provider>" with the small model magpie picks of it,
	// "<provider>/<model>", or empty for the one magpie picks
	// (gateway.searcher). "off" allows only configured search APIs.
	// One that is gone, off or can't search gives way to magpie's pick.
	Searcher string `json:"searcher,omitempty"`
	// SearcherThen are the searchers asked in turn, in this order, when
	// the one Searcher names fails or finds nothing, each written as
	// Searcher is (axelburks on Discord: a second provider when the first
	// fails). Read only while Searcher names one; magpie's own pick hands
	// on to the next it would pick by itself.
	SearcherThen []string `json:"searcherThen,omitempty"`
	// SearcherEffort is how hard the searcher's model thinks as it searches
	// (MOMO on Discord): a level of catalog.EffortRank, fitted to the
	// model's own levels when it is asked, or empty to send none, the
	// model's own default (gateway.searchEffort).
	SearcherEffort string `json:"searcherEffort,omitempty"`
	// SearchFirst is what a model that can't search the web is searched
	// for with first, when both a provider and a search API can (#928):
	// SearchFirstAPI for the search APIs, the provider's search after them
	// when they fail; empty for the provider first, as before.
	SearchFirst string `json:"searchFirst,omitempty"`
	// SearchAll is that magpie searches for every model, one whose
	// provider searches the web by itself too (cybercaius on Discord):
	// the client's search is answered by the searcher and the search APIs,
	// as for a model that can't, and the provider is sent no search tool.
	// Only while magpie can search for it (gateway.magpieSearches).
	SearchAll bool `json:"searchAll,omitempty"`
	// SearchCount is how many pages a client's search is answered with
	// when it names no count, as Claude Code's WebSearch and Codex's
	// web_search never do (#5545, mdwsk88): from 1 to SearchCountMost, 0
	// for SearchCountDefault. A web_search call that names a count (the
	// web-search MCP server's) is answered with that.
	SearchCount int `json:"searchCount,omitempty"`
	// TrayUsages are the subscriptions and plans whose windows are shown
	// beside the tray icon, in the order shown, each by its provider and
	// account ("claude|a@b.c"); none when empty.
	TrayUsages []string `json:"trayUsages,omitempty"`
	// TrayUsage is TrayUsages' first, all a magpie before them read: a file
	// with it and no TrayUsages shows that one, and it is kept written for
	// an older magpie to go on showing it.
	TrayUsage string `json:"trayUsage,omitempty"`
	// TrayUsageEvery is how often, in minutes, that text is brought up to
	// date; 0 is every 3 (one of TrayEvery).
	TrayUsageEvery int `json:"trayUsageEvery,omitempty"`
	// TrayNoLogos draws the Mac menu bar's cards without their logos: each
	// is its windows stacked alone, a thin line between one card and the
	// next.
	TrayNoLogos bool `json:"trayNoLogos,omitempty"`
	// TrayNoBird leaves magpie's bird out of the Mac menu bar while the
	// cards are there, the cards alone (KevinXC on Discord); a click on one
	// still opens the panel, and with no cards the bird is shown.
	TrayNoBird bool `json:"trayNoBird,omitempty"`
	// TrayNoNumber keeps magpie's icon in Windows' tray while cards are
	// picked; off by default, the icon is the first card's figure
	// (Benjaminliang_1 on X), as Windows' tray shows no text beside it.
	TrayNoNumber bool `json:"trayNoNumber,omitempty"`
	// TrayPace puts each window's Pace number beside its figure in the
	// menu bar ("42% +12%"), as the Usage page's cards have it (fflake33
	// on Discord); off by default, as it widens every card.
	TrayPace bool `json:"trayPace,omitempty"`
	// TrayNoCost leaves money out of magpie's own totals in the menu bar:
	// the today and last-7-days cards, their tooltips and the tooltip with
	// no cards say the tokens alone (BussanQ on Discord); off by default,
	// the cost shown.
	TrayNoCost bool `json:"trayNoCost,omitempty"`
	// TrayWindows are, by subscription or plan (a provider's id, every
	// account of it alike), the windows its menu bar card shows, each by
	// how long it runs ("5h", "7d", "month": gui.trayWindowKind), so a
	// vendor reordering or renaming them changes nothing (StrikingRaw on
	// X: Claude's 5h alone, not two small numbers). A provider not named
	// shows every window, as before; one whose named windows are all gone
	// shows every window too, never an empty card.
	TrayWindows map[string][]string `json:"trayWindows,omitempty"`
	// Lightweight lets the webview of a window closed — the tray panel or
	// the main window — go once it has stayed closed a while, and makes it
	// again when it is opened (#580): less memory, a moment's wait. This
	// computer's own (KeepOwn). Nil is off: a window made again on each
	// opening was too slow to have by default (the owner, 10-21: "太卡");
	// one who turned it on keeps it on (LightweightOn).
	Lightweight *bool `json:"lightweight,omitempty"`
	// QuitNoAsk lets a Quit the user chooses quit at once while agents go
	// through magpie, without the question that names them (gui's
	// quitask.go). Off by default, it asks; the question's "Don't ask
	// again" sets it, and Settings › General › Ask before quitting turns
	// it back off (zhujiu on Discord).
	QuitNoAsk bool `json:"quitNoAsk,omitempty"`
	// KeepAwake keeps this computer from going to sleep by itself while
	// agents work through the gateway and for a while after (xiao_wang24004
	// on X; internal/awake). This computer's own (KeepOwn).
	KeepAwake bool `json:"keepAwake,omitempty"`
	// NoWSLAgents stops magpie looking in WSL on its own, on Windows
	// (#1264): no distro is listed or probed for agents, their sessions or
	// Claude Code, and the agents found there before aren't shown.
	NoWSLAgents bool `json:"noWSLAgents,omitempty"`
	// KeepAwakeDisplay keeps the display on too while KeepAwake holds the
	// computer awake (#975, Hu9956: an agent recording the screen to check
	// its work found it locked). This computer's own (KeepOwn).
	KeepAwakeDisplay bool `json:"keepAwakeDisplay,omitempty"`
	// GatewayMode is whether `magpie web` shows only what a gateway serving
	// other machines needs (Player on Discord): "on", "off", or "" to
	// decide by itself — on for `magpie web --gateway`, or with no agents
	// on this machine. This computer's own (KeepOwn).
	GatewayMode string `json:"gatewayMode,omitempty"`
	// QuotaLeft shows a subscription's windows by how much of each is left,
	// not used: the Usage page, the tray panel and the menu bar alike.
	QuotaLeft bool `json:"quotaLeft,omitempty"`
	// QuotaReads is when magpie reads subscriptions' allowances and keys'
	// balances from their vendors (#1518, RooobinYe): "" whenever it needs
	// them (the Usage page and tray, routing, alerts, the switch to an
	// account with room), "asked" only when the user asks — the Usage page
	// opened or refreshed, a card's refresh, `magpie quota`/`accounts`.
	// Otherwise the readings kept from the last time stand, marked as of
	// then; an account the vendor turned away for its quota is still read.
	// This computer's own (KeepOwn): magpie at login comes up before the
	// proxy app, and its reads went out direct.
	QuotaReads string `json:"quotaReads,omitempty"`
	// UsageAlert is how much of a subscription's or plan's window, in
	// percent, is used when magpie says so with a notification (#368):
	// once for each time the window runs, for every window routing counts
	// (not one set aside, as on-demand spending is); 0 is off.
	UsageAlert int `json:"usageAlert,omitempty"`
	// BalanceAlert is the balance a key or account has fallen to when
	// magpie says so, in that balance's own currency or credits, once until
	// it is topped up past it again; 0 is off.
	BalanceAlert float64 `json:"balanceAlert,omitempty"`
	// ResetReminder is how many hours before a renewal magpie says what
	// would be lost by it (#720): a weekly or monthly window with much of
	// it left before it renews, a reset credit unspent before it runs out;
	// once each. 0 is off.
	ResetReminder int `json:"resetReminder,omitempty"`
	// TurnDoneAlert is how many seconds an agent's turn runs, from the
	// user's prompt to its last reply, before magpie says it is done with a
	// notification (itieniu on X): a long task set going while the user
	// works elsewhere. Only turns the app's own gateway serves; 0 is off.
	TurnDoneAlert int `json:"turnDoneAlert,omitempty"`
	// MaskAlert has magpie say with a notification when masking swaps a
	// value it never masked before (Must2333 on X): its kind and how many,
	// never the value, a burst said once and at most once a minute. Only
	// requests the app's own gateway serves; off until turned on.
	MaskAlert bool `json:"maskAlert,omitempty"`
	// PlainNames has the model lists magpie gives agents name each model
	// by its name alone, without its provider's or "routing group" after it
	// (#335) — but for two in one list that would read the same, which keep
	// it (see provider.Labels).
	PlainNames bool `json:"plainNames,omitempty"`
	// PlainOwnNames, with PlainNames off, has those lists name a model the
	// user gave a name of their own by that name alone, just as they wrote
	// it (#92: "Opus 5.5", not "Opus 5.5 · Claude Code"), the vendor's names
	// keeping their provider's after them (see provider.Labels).
	PlainOwnNames bool `json:"plainOwnNames,omitempty"`
	// AgentSuffix is an agent's own Provider in model names, by the id
	// its lists are kept by (lower-cased): "on", "own" or "off", over
	// PlainNames and PlainOwnNames for that agent alone (KillTry27355 on
	// X). An agent it doesn't name follows those two (provider.LabelsFor).
	AgentSuffix map[string]string `json:"agentSuffix,omitempty"`
	// BareModelIDs has the gateway's /v1/models list each model by its id
	// without the provider as well ("gpt-5.6-sol" beside
	// "codex/gpt-5.6-sol"), for clients moved from another proxy that ask
	// for bare ids (love1sbug on X; provider.BareEntries). Off by default.
	BareModelIDs bool `json:"bareModelIDs,omitempty"`
	// CodexAgentsV1 has the OpenAI models magpie hands Codex — the ChatGPT
	// account's own, their codex/ ids, the groups one is in, and those a
	// provider that seals as that backend does serves (catalog.Model.Seals:
	// a relay of it, brethhbbm on X) — say
	// multi_agent_version "v1" (#141): their subagents are then handed
	// their tasks as text, which a magpie-served subagent can read, where
	// V2's are sealed by OpenAI's server, and it is V1 that a ChatGPT
	// lead's subagent on a magpie-served model needs (a V2 task to a
	// non-ChatGPT model fails). Codex's own features.multi_agent_v2 would
	// still win: where it is on in the config magpie routes, magpie says so
	// (internal/agent/codex.go, and the Settings row) and leaves it to the
	// user to turn off. A thread keeps the version it started with.
	CodexAgentsV1 bool `json:"codexAgentsV1,omitempty"`
	// CodexTitles is where the requests Codex makes for a thread's title
	// (thread_title, thread_title_reconsideration: a hidden turn of their
	// own, on Codex's own model through its ChatGPT sign-in) go (#705): ""
	// as Codex sends them, "off" answered by magpie with no title and sent
	// nowhere, or a model's id (provider/model, group/<id>) that writes it.
	CodexTitles string `json:"codexTitles,omitempty"`
	// CodexDescriptions is where the request Codex makes for a thread's
	// description (thread_description: its own hidden turn, on Codex's own
	// model through its ChatGPT sign-in) goes: "" as Codex sends it, "off"
	// answered by magpie with no description and sent nowhere, or a
	// model's id (provider/model, group/<id>) that writes it. It is not
	// CodexTitles: a description asks for {"description"} alone.
	CodexDescriptions string `json:"codexDescriptions,omitempty"`
	// CodexPredictions is where the request Codex's app makes to predict
	// the user's next message for its composer (composer_predictions: a
	// hidden turn on a fork of the whole conversation, on the thread's own
	// model through its ChatGPT sign-in) goes (#1970, pluo): "" as Codex
	// sends it, "off" answered by magpie with no prediction and sent
	// nowhere, or a model's id (provider/model, group/<id>) that writes it.
	CodexPredictions string `json:"codexPredictions,omitempty"`
	// CodexAutoReview is the model Codex's auto-review (approvals_reviewer
	// = "auto_review": the guardian that decides an approval in the user's
	// place) runs on (#938): a model's id (provider/model, group/<id>) put
	// in every entry of the list magpie hands Codex as
	// auto_review_model_override, which Codex takes before its own
	// codex-auto-review or the conversation's model at low effort. ""
	// leaves the list as it was. CodexAutoReviewSession has each of
	// magpie's entries name itself, so the review runs on the session's
	// model when that is one of magpie's, not on codex-auto-review through
	// the ChatGPT sign-in (cybercaius on Discord).
	CodexAutoReview string `json:"codexAutoReview,omitempty"`
	// CodexSubagentModel is the model the gateway puts every subagent
	// Codex spawns on (a request x-openai-subagent names collab_spawn),
	// whatever model its lead asked for in spawn_agent: a model a ChatGPT
	// account in magpie serves (provider/model), since a subagent's task
	// is sealed for one (willz on Discord). "" leaves each on the model its
	// lead asked for; CodexSubagentLead puts each on the model its lead's
	// own turns go on, even when the lead names another.
	CodexSubagentModel string `json:"codexSubagentModel,omitempty"`
	// CodexServiceTier is the service tier the gateway puts on every
	// request Codex sends, whatever Codex asked (JackyFill on X): "" as
	// Codex asks it (its /fast, Ultra Fast or neither), CodexTierStandard
	// with no tier at all, "priority" (Codex's Fast) or "ultrafast" (its
	// Ultra Fast). Codex's words, as codex-rs ServiceTier::request_value
	// sends them; its Flex is not offered, the ChatGPT backend takes none.
	CodexServiceTier string `json:"codexServiceTier,omitempty"`
	// DesktopSubagentModel is the model the gateway puts Claude Desktop's
	// subagent tool turns on. "" leaves them on the model their lead asks
	// for; tool-less helpers and the lead's own turns are never moved.
	DesktopSubagentModel string `json:"desktopSubagentModel,omitempty"`
	// DesktopSubagentEffort fixes Desktop's subagent tool turns at this
	// reasoning level. Empty keeps the effort each request asks for.
	DesktopSubagentEffort string `json:"desktopSubagentEffort,omitempty"`
	// DshCompactModel is the model the gateway puts DeepSeek Harness's
	// compaction of its context on, whatever model the session is on:
	// a switch away from a model whose provider has run out of its limit
	// compacts on the old model first, and failed with it (#5395). ""
	// leaves each compaction on the model it asks for.
	DshCompactModel string `json:"dshCompactModel,omitempty"`
	// FullContext has Codex, Claude Code and Grok Build told a model's whole context
	// window. Off, a window above WorkingWindow is told as WorkingWindow,
	// so they compact a long conversation there instead of sending ever
	// more of it on every turn (X: Chen, a 1M DeepSeek model through
	// Codex and Claude Code took 70–90s to its first token at 550K):
	// what OpenAI does with its own models, 272K though they can take
	// more, and Anthropic with its, 200K unless a [1m] one is picked.
	FullContext bool `json:"fullContext,omitempty"`
	// DesktopLongest has Claude Desktop list a model of 1M tokens or more
	// once, as its 1M entry (its id with Claude Code's "[1m]"), instead of
	// twice (#1272): Desktop adds a "1M context window" entry beside any
	// gateway model whose max_input_tokens is 1M or more and whose id has no
	// "[1m]", and adds none to one whose id has it. Off, as before, it shows
	// both, the plain one compacting at Claude Code's 200K.
	DesktopLongest bool `json:"desktopLongest,omitempty"`
	// DesktopIsolated runs Claude Desktop on magpie in a folder of its own
	// (#5671): magpie's 3p mode goes into
	// ~/Library/Application Support/Claude-Magpie-3p instead of Claude and
	// Claude-3p, and ~/Applications/Claude Magpie.app starts Desktop with
	// --user-data-dir set to it, beside the user's own signed-in Desktop,
	// which is left as it is. macOS only (desktopdir.Isolated).
	DesktopIsolated bool `json:"desktopIsolated,omitempty"`
	// GuideHidden: the user put away the Agents page's Get started card
	// (wangye1644294 on X) before finishing it. Kept here, not in the
	// page's storage, so the panel, the window and a reload agree.
	GuideHidden bool `json:"guideHidden,omitempty"`
	// CompactAt is the window told for a longer one when FullContext is
	// off, in tokens: 0 is WorkingWindow (#876: 272K was the only one).
	// A provider's or a model's own (ModelCompacts) comes before it.
	CompactAt int `json:"compactAt,omitempty"`
	// AgentCompacts is Long conversations of an agent's own, by its kind
	// (CompactAgents), over CompactAt and FullContext for that agent alone
	// (molianyin4227 on Discord: models differ, so one size for every
	// agent fits none). An agent with none follows those two (ForAgent).
	AgentCompacts map[string]AgentCompact `json:"agentCompacts,omitempty"`
	// CodexAsyncCompact has Codex (0.163 on) compact a long conversation
	// in the background, while its turn goes on, once it is past
	// CodexAsyncCompactPercent of the model's window (#2490, lantern2077
	// on Discord): [features.async_compaction] enabled in the config Codex
	// is routed through, and capabilities.remote_compaction = "v2" on
	// magpie's provider table, without which Codex never starts one on a
	// provider other than OpenAI's. A value the user set there is theirs
	// and stays (internal/agent/codex_async.go).
	CodexAsyncCompact bool `json:"codexAsyncCompact,omitempty"`
	// CodexAsyncCompactPercent is that share, 1–100; 0 is Codex's own, 65.
	CodexAsyncCompactPercent int `json:"codexAsyncCompactPercent,omitempty"`
	// ChinaMirror is the Plugins page's 「国内镜像」 switch: the plugin list,
	// npm (the plugins' packages and what npm says of them) and Bun's
	// downloads are asked of mirrors in China first, and of their official
	// addresses after (see source.China).
	ChinaMirror bool `json:"chinaMirror,omitempty"`
	// NPMRegistry is the npm registry set on the Plugins page or with
	// `magpie plugin registry`: plugins are installed from it and npm's
	// answers asked of it, before a registry the user's .npmrc, bunfig or
	// shell names and before the 「国内镜像」 switch (plugin.Registry).
	// "" leaves it to those.
	NPMRegistry string `json:"npmRegistry,omitempty"`
	// TextSize is how large the window's and the tray panel's pages are
	// drawn, in percent (one of TextSizes): the webviews' own zoom, as a
	// browser's, so the text and everything around it grow together.
	TextSize int `json:"textSize,omitempty"`
	// UIFont and CodeFont are this computer's installed faces. Nil keeps
	// the platform's stack (or Omarchy's). Sync never replaces them.
	UIFont   *fonts.Face `json:"uiFont,omitempty"`
	CodeFont *fonts.Face `json:"codeFont,omitempty"`
	// How the agents are listed, by agent id. AgentOrder comes first, as
	// ordered; an agent it doesn't name (one installed since) follows in
	// magpie's own order. A hidden agent is folded away at the bottom of the
	// list; a shown one stays in view even while nothing is set on it, which
	// otherwise folds it away too. The agents' own files never hear of it.
	AgentOrder   []string `json:"agentOrder,omitempty"`
	AgentsHidden []string `json:"agentsHidden,omitempty"`
	AgentsShown  []string `json:"agentsShown,omitempty"`
	// AgentsHere are the agents, by id, whose picks (model, effort…) stay
	// on this computer (chenxiang0010 on Discord), as an MCP server can:
	// sync, a Restore and a backup file neither bring another computer's
	// picks in for one nor take this computer's up. This computer's own.
	AgentsHere []string `json:"agentsHere,omitempty"`
	// UsageOrder is how the Usage page's cards are listed, by provider id,
	// as they were dragged there; one it doesn't name follows in magpie's
	// own order. Only the page's: the order providers are tried in is the
	// Providers page's.
	UsageOrder []string `json:"usageOrder,omitempty"`
	// PanelUsageHidden are the subscriptions, by provider id, that the tray
	// panel's Allowances tab leaves out (H20 on Discord). It is what the
	// panel shows, nothing more: routing, caps, the Usage page and the menu
	// bar's cells still have them. The panel's order is UsageOrder.
	PanelUsageHidden []string `json:"panelUsageHidden,omitempty"`
	// UsageAgentsHidden are the clients, by the id the ledger records them
	// under ("curl", "Go-http-client", an agent's id), that the Usage page's
	// Agents list leaves out (ikunbots on X). It is that list's only: the
	// ledger keeps their calls, and the totals and the chart count them.
	UsageAgentsHidden []string `json:"usageAgentsHidden,omitempty"`
	// UsageCardsHidden are the allowance cards the Usage page's Overview
	// leaves out (whqtian_07012 on Discord), each as its tray card id (the
	// provider id, then "|" and the account when there is one) and "|plan"
	// or "|balance", so StepFun's balance card goes while its plan card
	// stays. Only what that page shows: routing, caps, counting, the tray
	// panel (PanelUsageHidden is its own) and the menu bar's cells keep them.
	UsageCardsHidden []string `json:"usageCardsHidden,omitempty"`
	// ClientAgents counts a client magpie doesn't know as one of the agents
	// it does, on the Usage and Sessions pages (#1958, NorethSea): a bridge
	// that starts Codex's app-server under a name of its own
	// ("hermes-codex-thread-bridge") is counted as Codex. Keyed by the
	// client's name as the ledger records it, lower-cased, to the agent's
	// id. Only what the pages report: the ledger keeps the client's own
	// name, and routing, keys, caps and budgets go on by it. A pair taken
	// out counts the client on its own again, the history with it.
	ClientAgents map[string]string `json:"clientAgents,omitempty"`
	// UsageCleared are the user's clears of the Usage page's figures
	// (CrazyMouse_TT on X: a local model benchmarked again from nothing),
	// each the calls up to its time, of one provider's model or of
	// everything. Only what the usage pages report: usage.jsonl keeps every
	// call, and a key's spending limit, the allowances' window holds and
	// the gateway's own reads of its log still count them. It rides the
	// settings part of a sync, so a clear holds on every computer that
	// syncs with this one, other computers' shared days included.
	UsageCleared []UsageClear `json:"usageCleared,omitempty"`
	// Visible narrows the models an agent is shown, by agent id: the
	// families (the tag a provider or group is given), provider ids and
	// group ids its lists hold. An agent it doesn't name is shown them all.
	Visible map[string][]string `json:"visible,omitempty"`
	// HiddenModels are the catalog entries (their ids, "<provider>/<model>"
	// or a group's) taken out of an agent's lists one by one, by agent id,
	// after Visible: a model not named here, a new one among them, is shown.
	HiddenModels map[string][]string `json:"hiddenModels,omitempty"`
	// DisabledModels are models turned off for every request through magpie
	// (#1867), each an exact id or a glob ("*fable*"), matched ignoring case
	// against "<provider>/<model>" and the bare model id: they are left out
	// of every list, and the gateway refuses them on every route.
	DisabledModels []string `json:"disabledModels,omitempty"`
	// PickedModels, for an agent it names, are the only catalog entries its
	// lists show, after Visible (#1337): the user switched the agent to
	// "only models I pick", so a model that comes later, of a new provider
	// or an old one, is not shown until it is ticked. HiddenModels is not
	// read for such an agent. An agent named with no entries is shown none.
	PickedModels map[string][]string `json:"pickedModels,omitempty"`
	// OwnModelsHidden are the agents, by id, whose pickers leave out the
	// models the agent reaches on its own, not through magpie (DeepSeek
	// Harness's own DeepSeek models, #1642): only magpie's are offered, and
	// one the agent is set to stays.
	OwnModelsHidden []string `json:"ownModelsHidden,omitempty"`
	// AgentEfforts are the reasoning efforts the gateway asks for on an
	// agent's requests, by agent id, for an agent whose own config can't
	// carry one (Cursor Private Inference, #1003): one of the levels in
	// provider.MemberEfforts.
	AgentEfforts map[string]string `json:"agentEfforts,omitempty"`
	// OrderedModels is the order an agent's lists put its models in, by
	// agent id and then entry id, as the user dragged them on the Agents
	// page (Codex's, #855): the ones named first, any other after them.
	OrderedModels map[string][]string `json:"orderedModels,omitempty"`
	// FastPicks are the catalog entries ("<provider>/<model>") an agent's
	// picks of are sent in their vendor's fast mode, by agent id (#954):
	// switched by the model in the agent's picker, as a routing group's
	// member is (Group.Fast), without making a group of it.
	FastPicks map[string][]string `json:"fastPicks,omitempty"`
	// UltraPicks are the fast picks an agent's requests for are sent as
	// Ultrafast instead (#1954), by agent id: each is in FastPicks too. An
	// account that doesn't offer Ultrafast on the model is sent priority.
	UltraPicks map[string][]string `json:"ultraPicks,omitempty"`

	// The three maps below, and every one added beside them, are the
	// per-model ones: a field named Model* whose type is a map[string]X,
	// keyed "<provider id>/<model id>" (or "<provider id>/*", for all of
	// that provider's models — CheckModelKey). That is the whole of the
	// convention, and it is what RenamePerModel and the GUI's saving of
	// the settings go by, each of them walking the fields by it rather
	// than by a list kept up to date by hand: a map added here later is
	// moved when a provider is renamed and kept when another page saves
	// the settings, with no line written for it in either place. A field
	// whose keys are not a model's is not one of these, and is not named
	// Model* — Visible, which is by agent id, among them.
	// ModelNames are the names the user gave models, by "<provider
	// id>/<model id>": agents, the gateway's model list and magpie itself
	// show them for the vendor's (see provider.SetModelName).
	ModelNames map[string]string `json:"modelNames,omitempty"`
	// ModelEfforts are the reasoning levels the user keeps of a model's,
	// by "<provider id>/<model id>": the lists magpie hands out offer only
	// those, while the vendor is still asked at any level it has; for a
	// model with no levels known they are the levels given it, and the
	// gateway fits a request to them (see provider.SetModelEfforts,
	// magpie model spec).
	ModelEfforts map[string][]string `json:"modelEfforts,omitempty"`
	// ModelReasonless are the models the user said take no reasoning ask,
	// by "<provider id>/<model id>": magpie offers them no levels and
	// sends them no reasoning field, on any API, though their list says
	// they think (see provider.SetModelReasonless; yuexian7 on Discord: a
	// model models.dev lists with reasoning answered reasoning_effort
	// with 422).
	ModelReasonless map[string]bool `json:"modelReasonless,omitempty"`
	// ModelImages is whether the user said a model takes images, by
	// "<provider id>/<model id>". Absent leaves it to the vendor's list.
	ModelImages map[string]bool `json:"modelImages,omitempty"`
	// ModelPrices is what a model costs the user, in USD per million
	// tokens, by "<provider id>/<model id>", and "*" for every model of
	// that provider: a provider models.dev does not list, or one that
	// resells at a multiplier, is otherwise priced at whatever its maker's
	// list price is. "*/<model id>" (AnyProvider) is that model from any
	// provider not priced above, and a session's model named with no
	// provider at all: one whose provider is gone, or one models.dev no
	// longer lists.
	ModelPrices map[string]ModelPrice `json:"modelPrices,omitempty"`
	// ModelOutputs is the most a reply of a model may hold, by "<provider
	// id>/<model id>", and "*" for every model of that provider. Absent
	// leaves it to the vendor's own list and to models.dev, as a context
	// the user has not set is. A model's own value wins over its
	// provider's, and the agents' own files are told of either
	// (see provider.SetModelOutput).
	ModelOutputs map[string]int `json:"modelOutputs,omitempty"`
	// ModelCompacts is where Codex and Claude Code compact a conversation
	// on a model, by "<provider id>/<model id>", and "*" for every model
	// of that provider (#876): the window they are told when the model's
	// own is longer, over CompactAt and FullContext. One at or above the
	// model's window is its whole window.
	ModelCompacts map[string]int `json:"modelCompacts,omitempty"`
	// ModelWires is the name to send a vendor for a model magpie knows by
	// another, by "<provider id>/<model id>", and "*" for every model of that
	// provider. A "*" in the name is the model itself, so one name covers a
	// relay that namespaces its models — vendor-c/* asks for model-3 as
	// vendor-c/model-3 — while a name with no "*" in it sends every model of
	// that key under that one name. Everything else — the catalog agents see,
	// the routing groups, the usage records and what a call is priced at —
	// keeps the name magpie knows the model by.
	ModelWires map[string]string `json:"modelWires,omitempty"`
	// ModelIDs are the ids the user lists a provider's models by, from
	// "<provider id>/<the id magpie lists it by>" to the id the provider
	// itself serves it under (zeewell on X: deepseek-v4.1-flash-1017 known
	// as deepseek-v4.1-flash). The model is listed, picked, grouped, found
	// in routing groups, limited by gateway keys and recorded in usage by
	// that id, and a request for it goes out under the provider's own, as
	// a ModelWires name does (Wires). Keyed by the id it is known by, so one
	// id of a provider is one of its models; a model of the provider's own
	// with that id is not listed while another goes by it. Absent leaves
	// each model to the provider's id (provider.SetModelID).
	ModelIDs map[string]string `json:"modelIDs,omitempty"`
	// ModelAPIs is the one API a model is asked on at its provider, by
	// "<provider id>/<model id>": chat, responses or anthropic, for a relay
	// whose one key serves some models on one and others on another
	// (01huadalang on Discord). Absent leaves it to the vendor's list and
	// to each URL the provider has (see provider.SetModelAPI).
	ModelAPIs map[string]string `json:"modelAPIs,omitempty"`
	// ModelSameAs is the model another vendor sells under another name
	// that a model is, by "<provider id>/<model id>": the routing groups
	// magpie finds (provider's autoGroups) merge it with that one rather
	// than by its own id, for an id no rule of magpie's matches up
	// (kyzhouxu, #583), and a call to it that no list prices is costed at
	// that model's maker's price (provider.SamePriceIn, #2344). Absent
	// leaves it to its id.
	ModelSameAs map[string]string `json:"modelSameAs,omitempty"`
	// UsageAliases are the model ids the user counts as one in Usage and
	// Sessions, from an id to the name it is counted under
	// ("deepseek-v4.1-flash": "deepseek-flash", SlouchyQuill507 on
	// Discord): what spent the same model under two names is one row there.
	// Only how usage is grouped and shown changes: no request, model list
	// or price reads it (provider.SetUsageAlias). Keyed by a bare model id,
	// not "<provider>/<model>", so it is not named Model* and is not one
	// of the per-model maps a provider's rename moves. Absent is none.
	UsageAliases map[string]string `json:"usageAliases,omitempty"`
	// ModelHeaders are the request headers the user gave one model of a
	// provider, by "<provider id>/<model id>", sent on that provider's
	// requests for the model after the provider's own Headers: a name the
	// provider has too, in any case, is the model's (axelburks on
	// Discord). Absent is the provider's alone (provider.SetModelHeaders).
	ModelHeaders map[string]map[string]string `json:"modelHeaders,omitempty"`
	// ModelSystemPrompts are the system prompts the user gave one model,
	// by "<provider id>/<model id>": the gateway adds the text after the
	// agent's own system prompt on every request for that model, whatever
	// agent sends it (liucl on X). Absent is the agent's prompt as sent
	// (provider.ModelSystemPrompt).
	ModelSystemPrompts map[string]string `json:"modelSystemPrompts,omitempty"`
	// ModelKinds is what the user said a model makes, by "<provider
	// id>/<model id>": "image" or "video", for a provider set up with a
	// base URL whose own model list doesn't say so (shen-joe, #1399).
	// Absent leaves it to the vendor's list and the model's name (see
	// provider.SetModelKind).
	ModelKinds map[string]string `json:"modelKinds,omitempty"`
	// The main window's size as it last settled, width and height, so it
	// opens at it again after a restart: the size it is restored to, kept as
	// it was while it is maximised, and a side fitted to a smaller screen
	// keeps the larger one (gui's settle).
	Window []int `json:"window,omitempty"`
	// WindowMaximised: the main window was maximised (zoomed, on the Mac)
	// when it last settled, so it opens maximised again; Window is still the
	// size it is restored to.
	WindowMaximised bool `json:"windowMaximised,omitempty"`
	// WindowAt is where the main window was last left, its top left corner
	// x and y in the system's coordinates across every screen (points on
	// the Mac, DIPs on Windows, X11's pixels on Linux), so it opens there
	// again after a restart rather than in the middle (jorbenzhu on
	// Discord). Kept only while it isn't maximised, full screen or
	// minimised; not at all under Wayland, where a window can't place
	// itself. gui's openAt moves it onto a screen there now.
	WindowAt []int `json:"windowAt,omitempty"`
}

// ModelPrice is the price of one model as the user states it. Each part is a
// pointer so that a file leaving one out is told so rather than billing that
// part of the call at zero: a model is priced whole or not at all.
type ModelPrice struct {
	Input      *float64 `json:"input,omitempty"`
	Output     *float64 `json:"output,omitempty"`
	CacheRead  *float64 `json:"cache_read,omitempty"`
	CacheWrite *float64 `json:"cache_write,omitempty"`
	// CacheWrite1h is a 1-hour cache write's price, absent where none is
	// given: such a write is then counted at 2× input for a Claude model,
	// else at the 5-minute price (catalog.OneHourFor), and on Copilot,
	// which bills an hour's write as a 5-minute one, at the 5-minute price.
	CacheWrite1h *float64 `json:"cache_write_1h,omitempty"`
	// Tiers are the prices of a request whose input is over a size, each
	// whole: "magpie model price … --tier 272k …" (PAMI on Discord).
	Tiers []catalog.Tier `json:"tiers,omitempty"`
}

// priceParts are the parts of a price, in the order they are asked for and
// in the order ModelPrice holds them, named as a message about one says
// them: "cache read" and "cache write", the words the CLI and the README
// use, not the cache_read and cache_write of the file's own keys, which
// are the disk format and stay as they are.
var priceParts = [...]string{"input", "output", "cache read", "cache write"}

// Price is the price the user stated, when every part of it is given and is a
// number a vendor could charge. The second return names the first part that is
// not, empty when the price is usable.
func (m ModelPrice) Price() (catalog.Price, string) {
	parts := []*float64{m.Input, m.Output, m.CacheRead, m.CacheWrite}
	for i, v := range parts {
		if v == nil || math.IsNaN(*v) || math.IsInf(*v, 0) || *v < 0 {
			return catalog.Price{}, priceParts[i]
		}
	}
	bad := func(v float64) bool { return math.IsNaN(v) || math.IsInf(v, 0) || v < 0 }
	if m.CacheWrite1h != nil && bad(*m.CacheWrite1h) {
		return catalog.Price{}, "1-hour cache write"
	}
	for i, t := range m.Tiers {
		if t.Above <= 0 || i > 0 && t.Above <= m.Tiers[i-1].Above ||
			bad(t.Input) || bad(t.Output) || bad(t.CacheRead) || bad(t.CacheWrite) || bad(t.CacheWrite1h) {
			return catalog.Price{}, fmt.Sprintf("tier %d", i+1)
		}
	}
	p := catalog.Price{
		Input: *m.Input, Output: *m.Output,
		CacheRead: *m.CacheRead, CacheWrite: *m.CacheWrite,
		Tiers: slices.Clone(m.Tiers),
	}
	if m.CacheWrite1h != nil {
		p.CacheWrite1h = *m.CacheWrite1h
	}
	return p, ""
}

// StatedPrice is a price as ModelPrices keeps it: every part given, the
// 1-hour cache write only when it is.
func StatedPrice(p catalog.Price) ModelPrice {
	m := ModelPrice{Input: new(p.Input), Output: new(p.Output), CacheRead: new(p.CacheRead), CacheWrite: new(p.CacheWrite), Tiers: slices.Clone(p.Tiers)}
	if p.CacheWrite1h > 0 {
		m.CacheWrite1h = new(p.CacheWrite1h)
	}
	return m
}

// CheckModelPrice is whether a price the user gives is one magpie will bill a
// call at: the key names a model the way the per-model maps key one, and
// every part of the price is a number a vendor could charge, naming the first
// that is not. It is where a price is checked rather than in Save: one a hand
// edit left broken in the file is skipped where it is read, and must not stop
// an unrelated setting from being saved. That covers a part that is missing,
// which is what a file can be edited into: JSON has no NaN or Infinity, and
// Load discards the whole file rather than half of it, so neither reaches
// here. Both are still refused, from a price typed in or passed in.
func CheckModelPrice(key string, m ModelPrice) error {
	if model, every := strings.CutPrefix(key, AnyProvider); every {
		if model == "" || model == "*" {
			return fmt.Errorf("a price for every provider names one model, such as */claude-opus-4.6, not %q", key)
		}
	} else if err := CheckModelKey("price", key); err != nil {
		return err
	}
	if _, bad := m.Price(); strings.HasPrefix(bad, "tier ") {
		return fmt.Errorf("the price of %q has a %s that is not over a size above 0 and above the tier before it, with every part finite and not negative", key, bad)
	} else if bad != "" {
		return fmt.Errorf("the price of %q needs %s %s price that is present, finite and not negative", key, anArticle(bad), bad)
	}
	return nil
}

// AnyProvider begins a ModelPrices key that prices a model whichever
// provider it came through, its id lower-cased after it.
const AnyProvider = "*/"

// UpdateStable is UpdateChannel's Stable: a version a week, an urgent one
// at once.
const UpdateStable = "stable"

// What a reply's model name says (ReplyModel): the model asked for is "".
const (
	ReplyAsked  = "asked" // read as ""
	ReplyMember = "member"
	ReplyVendor = "vendor"
)

// ReplyModels are ReplyModel's values besides its default "".
var ReplyModels = []string{ReplyMember, ReplyVendor}

// anArticle is the article a word takes where a message names it, so that an
// input price is not "a input price".
func anArticle(word string) string {
	if strings.ContainsRune("aeiou", rune(word[0])) {
		return "an"
	}
	return "a"
}

// Arrange puts items in the order the user gave the agents, those named
// first and the rest after in the order they came, and splits off the
// hidden ones, which keep that order too. id names an item's agent.
func Arrange[T any](s Settings, items []T, id func(T) string) (shown, hidden []T) {
	rank := map[string]int{}
	for i, x := range s.AgentOrder {
		if _, dup := rank[x]; !dup {
			rank[x] = i
		}
	}
	sorted := slices.Clone(items)
	slices.SortStableFunc(sorted, func(a, b T) int {
		ra, oka := rank[id(a)]
		rb, okb := rank[id(b)]
		switch {
		case oka && okb:
			return ra - rb
		case oka:
			return -1
		case okb:
			return 1
		}
		return 0
	})
	for _, x := range sorted {
		if slices.Contains(s.AgentsHidden, id(x)) {
			hidden = append(hidden, x)
		} else {
			shown = append(shown, x)
		}
	}
	return shown, hidden
}

// ImageTimeoutDefault is the minutes an image is given to be drawn when
// ImageTimeout is 0, and ImageTimeoutMost the most it can be set to.
const (
	ImageTimeoutDefault = 5
	ImageTimeoutMost    = 60
)

// ImageWait is how long a vendor is given to draw an image and send it:
// ImageTimeout's minutes, ImageTimeoutDefault's when it is 0 (or below),
// and no more than ImageTimeoutMost's when settings.json was written by
// hand past it.
func (s Settings) ImageWait() time.Duration {
	m := s.ImageTimeout
	if m < 1 {
		m = ImageTimeoutDefault
	}
	return time.Duration(min(m, ImageTimeoutMost)) * time.Minute
}

// CodexResetLeadDefault is CodexResetLead's minutes when it is unset, and
// CodexResetLeadLeast and CodexResetLeadMost the range it can be set in;
// CodexResetLeads the choices the app's menu offers, besides its Custom…
// (any minutes in range, as the CLI takes). Three minutes is the least
// (shangyuan0818 on Discord asked for less than 15): the loop wakes at the
// lead's start, a try is bounded at two minutes (the pass's), and a
// failed one is tried again a minute after it began, or at once when that
// is past, so a try hung to its bound still leaves a retry with ~56s before
// the reset runs out; at two a hung try leaves none. app.js's RESET_LEADS
// and resetLeadMinutes, and Custom…'s words, say it too.
const (
	CodexResetLeadDefault = 30
	CodexResetLeadLeast   = 3
	CodexResetLeadMost    = 24 * 60
)

var CodexResetLeads = []int{3, 5, 10, 15, 30, 60, 120, 180, 360, 720, 1440}

// ResetLead is how long before a Codex reset about to run out unused it
// is spent by itself: CodexResetLead's minutes, CodexResetLeadDefault's
// when it is 0 or out of range (written by hand), never none.
func (s Settings) ResetLead() time.Duration {
	m := s.CodexResetLead
	if m < CodexResetLeadLeast || m > CodexResetLeadMost {
		m = CodexResetLeadDefault
	}
	return time.Duration(m) * time.Minute
}

// SearchCountDefault is the pages a search is answered with when
// SearchCount is 0, and SearchCountMost the most it can be set to: Brave
// and Tavily return no more than 20.
const (
	SearchCountDefault = 6
	SearchCountMost    = 20
)

// SearchPages is how many pages a search that names no count is answered
// with: SearchCount, SearchCountDefault when it is 0 (or below), and no
// more than SearchCountMost when settings.json was written by hand past it.
func (s Settings) SearchPages() int {
	if s.SearchCount < 1 {
		return SearchCountDefault
	}
	return min(s.SearchCount, SearchCountMost)
}

// BusyLoadMost is past the highest BusyLoad: plugin.LoadMost, the most a
// plugin's reported load is kept at, so a threshold at it moves nothing.
const BusyLoadMost = 10000

// ClassifyTimeoutDefault is the seconds a classifier is given when
// ClassifyTimeout is 0, and ClassifyTimeoutMost the most it can be set to:
// the turn waits on it before it goes anywhere, with nothing sent to its
// agent meanwhile.
const (
	ClassifyTimeoutDefault = 8
	ClassifyTimeoutMost    = 60
)

// ClassifyWait is how long a classifier is given to answer:
// ClassifyTimeout's seconds, ClassifyTimeoutDefault's when it is 0 (or
// below), and no more than ClassifyTimeoutMost's when settings.json was
// written by hand past it.
func (s Settings) ClassifyWait() time.Duration {
	n := s.ClassifyTimeout
	if n < 1 {
		n = ClassifyTimeoutDefault
	}
	return time.Duration(min(n, ClassifyTimeoutMost)) * time.Second
}

// Themes and Langs are the accepted values, in the order the UI offers them.
var (
	Themes     = []string{"system", "light", "dark"}
	Langs      = []string{"system", "en", "zh", "zh-TW", "ja", "de"}
	Trays      = []string{"panel", "window"}
	Currencies = []string{"usd", "cny"}
	// Warmups are CodexWarmup's and ClaudeWarmup's values, off as "".
	Warmups = []string{"", "week", "all"}
	// TrayEvery are TrayUsageEvery's values, in minutes.
	TrayEvery = []int{1, 3, 5, 10, 30}
	// UpdateChannels are UpdateChannel's values: every version, Stable.
	UpdateChannels = []string{"", UpdateStable}
	// UpdateEveries are UpdateEvery's values, in minutes.
	UpdateEveries = []int{30, 60, 360, 1440}
	// TextSizes are TextSize's values, in percent. None is under 100: the
	// webviews' zoom on Windows and Linux (Wails' SetZoom) goes no lower.
	TextSizes = []int{100, 110, 125, 150}
)

// SearchFirst's values: a provider's search first, saved as "", or the
// search APIs first.
const (
	SearchFirstModel = "model"
	SearchFirstAPI   = "api"
)

// DefaultTextSize is the text size of settings that have never had one
// saved. Windows' and Linux's body text is 14px against magpie's 13px at
// 100%, a size smaller than what is around it there, so they start at
// 110% (#914); the Mac's is 13px, as magpie's. A size once saved, 100%
// too, is kept as it is. A var so tests can be any system.
var DefaultTextSize = func() int {
	if runtime.GOOS == "windows" || runtime.GOOS == "linux" {
		return 110
	}
	return 100
}

var validTerminalBundleID = capre.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]*$`, 255)

// providerID is how a provider's id is spelled: lower-case letters, digits
// and dashes, as provider.Slug derives it (a custom provider's id is the
// same, the site it is on when its name has none).
var providerID = lazyre.New(`^[a-z0-9][a-z0-9-]*$`)

// CheckModelKey is whether a key of one of the per-model maps names a model
// the way all of them do: "<provider id>/<model id>" — or "<provider id>/*"
// for all of that provider's models. A model id may have slashes of its own
// (vendor/model), so only the first one ends the provider's id. what names
// the setting being checked, for the error.
//
// It is the one rule a model key is held to, and it lives here, beside the
// maps it keys. Every change that takes such a key from the user checks it
// here rather than spelling the shape out again, so what is written into a
// map and what is refused come out of the same rule — one rule, and not one
// each branch can drift from on its own.
func CheckModelKey(what, key string) error {
	pid, model, ok := strings.Cut(key, "/")
	if !ok || model == "" {
		return fmt.Errorf("%s must be a model as <provider>/<model>, such as openai/gpt-5-mini, or <provider>/*, not %q", what, key)
	}
	if !providerID.MatchString(pid) {
		return fmt.Errorf("%s must name a provider before the model's id, such as openai/gpt-5-mini, not %q", what, key)
	}
	return nil
}

// perModelFields are the fields of t that are per-model: those named Model*
// whose type is a map[string]X (see ModelNames). The name and the type are
// all that is looked at, so a field the caller means by another key is taken
// for a per-model map all the same, and a field of a type that is not
// map[string]X is left out however it is named.
func perModelFields(t reflect.Type) []reflect.StructField {
	var out []reflect.StructField
	for i := range t.NumField() {
		f := t.Field(i)
		if strings.HasPrefix(f.Name, "Model") && f.Type.Kind() == reflect.Map && f.Type.Key().Kind() == reflect.String {
			out = append(out, f)
		}
	}
	return out
}

// PerModelKeys calls fn with the name of every per-model map of s and the
// map itself, the settings' own values and not copies, so fn may change them
// (see ModelNames). A field that is not per-model is not passed; the name
// comes with the map so a caller that reports on the maps it was given can
// tell them apart.
func PerModelKeys(s *Settings, fn func(name string, m reflect.Value)) {
	v := reflect.ValueOf(s).Elem()
	for _, f := range perModelFields(v.Type()) {
		fn(f.Name, v.FieldByIndex(f.Index))
	}
}

// CarryPerModel puts cur's per-model maps into in's, whole as they are: the
// ones a page that sends only its own choices would otherwise save as
// nothing, and so lose. Every per-model map is carried, whichever page wrote
// it, so a map added to the settings later needs nothing said of it here.
func CarryPerModel(in, cur *Settings) {
	dst, src := reflect.ValueOf(in).Elem(), reflect.ValueOf(cur).Elem()
	for _, f := range perModelFields(dst.Type()) {
		dst.FieldByIndex(f.Index).Set(src.FieldByIndex(f.Index))
	}
}

// WorkingWindow is the context window Codex and Claude Code are told for a
// model with a larger one, unless FullContext: Codex's own for OpenAI's
// models that can take more.
const WorkingWindow = 272000

// LightweightOn says whether lightweight mode is on: only when the user
// turned it on. It was on for a user who never chose, since a hidden
// webview kept for good holds what its page drew while hidden (~20 MB a
// minute on Windows under steady requests, #2515), but each opening then
// waited for the page to load again.
func (s Settings) LightweightOn() bool { return s.Lightweight != nil && *s.Lightweight }

// Working is the context window an agent is told for a model with one of
// n tokens (see FullContext).
func (s Settings) Working(n int) int {
	if w := s.Compact(); w > 0 && n > w {
		return w
	}
	return n
}

// AgentCompact is one agent's Long conversations: Full its FullContext,
// At its CompactAt (0 is WorkingWindow).
type AgentCompact struct {
	At   int  `json:"at,omitempty"`
	Full bool `json:"full,omitempty"`
}

// CompactAgents are the agents whose configs magpie writes a compaction
// window into, by the key each reads: Codex's model catalog
// (auto_compact_token_limit), Claude Code's settings.json
// (CLAUDE_CODE_AUTO_COMPACT_WINDOW, autoCompactWindow) and Grok Build's
// model tables (context_window). Only these take an AgentCompact.
var CompactAgents = []string{"codex", "claude", "grok"}

// ForAgent is s as the agent of that kind reads Long conversations: its
// own AgentCompact in CompactAt and FullContext, when it has one.
func (s Settings) ForAgent(kind string) Settings {
	if c, ok := s.AgentCompacts[kind]; ok {
		s.CompactAt, s.FullContext = c.At, c.Full
	}
	return s
}

// Compact is where a conversation on a longer window is compacted when
// neither its model nor its provider says (ModelCompacts): CompactAt,
// else WorkingWindow, and 0 under FullContext, the model's whole window.
func (s Settings) Compact() int {
	if s.FullContext {
		return 0
	}
	if s.CompactAt > 0 {
		return s.CompactAt
	}
	return WorkingWindow
}

// KeepOwn puts back cur's settings that are this computer's own, which a
// sync or a restored backup never brings from another: the window's size,
// place and whether it was maximised, the proxy (and plugin downloads'), the gateway's port, whether it
// is shared on the local network and the addresses let in to it, what
// its MCP router says of itself, the Dock, gateway mode, whether WSL is
// looked in, whether agents' sign-ins are read, when allowances are read, the terminal app a session resumes
// in, the quick panel's shortcut, whether the local masking model is on
// (it is downloaded on this computer alone), how many Claude Code processes may run (its memory),
// and what the menu bar or tray shows beside magpie's icon (yoooo on
// Discord: usage turned off on a Mac came back from a Windows box that
// shows it; Lullaby: sharing turned on at home came on at work).
func (s *Settings) KeepOwn(cur Settings) {
	s.GatewayConversations = cur.GatewayConversations
	s.SessionReads = cur.SessionReads
	s.UIFont, s.CodeFont = cur.UIFont, cur.CodeFont
	s.Window, s.Proxy, s.Port, s.Dock, s.DockWindow, s.Lightweight = cur.Window, cur.Proxy, cur.Port, cur.Dock, cur.DockWindow, cur.Lightweight
	s.DockNever, s.DockSettled = cur.DockNever, cur.DockSettled
	s.ProxyCustom = cur.ProxyCustom
	s.PluginProxy, s.PluginProxyCustom = cur.PluginProxy, cur.PluginProxyCustom
	s.WindowMaximised, s.KeepAwake, s.KeepAwakeDisplay = cur.WindowMaximised, cur.KeepAwake, cur.KeepAwakeDisplay
	s.WindowAt = cur.WindowAt
	s.TrayUsages, s.TrayUsage, s.TrayUsageEvery, s.TrayNoLogos, s.TrayNoBird, s.TrayPace, s.TrayNoCost = cur.TrayUsages, cur.TrayUsage, cur.TrayUsageEvery, cur.TrayNoLogos, cur.TrayNoBird, cur.TrayPace, cur.TrayNoCost
	s.TrayWindows, s.TrayNoNumber = cur.TrayWindows, cur.TrayNoNumber
	s.GatewayMode = cur.GatewayMode
	s.NoWSLAgents = cur.NoWSLAgents
	s.QuotaReads = cur.QuotaReads
	s.NoAccountDiscovery = cur.NoAccountDiscovery
	s.LAN, s.AllowIPs, s.MTLS = cur.LAN, cur.AllowIPs, cur.MTLS
	s.RouterNote, s.RouterAllTools = cur.RouterNote, cur.RouterAllTools
	s.FetchPrivate = cur.FetchPrivate
	s.SessionTerminal = cur.SessionTerminal
	s.PanelShortcut = cur.PanelShortcut
	s.RedactModel, s.RedactModelName, s.RedactModelScore = cur.RedactModel, cur.RedactModelName, cur.RedactModelScore
	s.RedactModelOff, s.RedactModelAuto = cur.RedactModelOff, cur.RedactModelAuto
	s.ClaudeRunsMost = cur.ClaudeRunsMost
	s.RequestArchiveLocal, s.RequestArchiveLocalMB = cur.RequestArchiveLocal, cur.RequestArchiveLocalMB
	s.AgentsHere = cur.AgentsHere
}

// Wires is ModelWires with the provider's own id of each model the user
// lists by another (ModelIDs): what a request for "<provider>/<id>" goes out
// under. A name the user gave the model itself in ModelWires is kept, and a
// provider's "*" name is the provider's own id in its "*". With no ModelIDs
// it is ModelWires itself; else it is made again only when either map holds
// something else than the last time. It is read, never written to.
func (s Settings) Wires() map[string]string {
	if len(s.ModelIDs) == 0 {
		return s.ModelWires
	}
	wiresMemo.Lock()
	defer wiresMemo.Unlock()
	if wiresMemo.out != nil && maps.Equal(wiresMemo.ids, s.ModelIDs) && maps.Equal(wiresMemo.wires, s.ModelWires) {
		return wiresMemo.out
	}
	out := make(map[string]string, len(s.ModelWires)+len(s.ModelIDs))
	maps.Copy(out, s.ModelWires)
	for k, own := range s.ModelIDs {
		pid, _, ok := strings.Cut(k, "/")
		own = strings.TrimSpace(own)
		if !ok || own == "" || strings.TrimSpace(s.ModelWires[k]) != "" {
			continue
		}
		if all := strings.TrimSpace(s.ModelWires[pid+"/*"]); all != "" {
			own = strings.ReplaceAll(all, "*", own)
		}
		out[k] = own
	}
	wiresMemo.ids, wiresMemo.wires, wiresMemo.out = maps.Clone(s.ModelIDs), maps.Clone(s.ModelWires), out
	return out
}

// wiresMemo is the Wires last made, and copies of the two maps it was
// made of: compared by what they hold, as a caller may change its own
// settings' maps in place.
var wiresMemo struct {
	sync.Mutex
	ids, wires, out map[string]string
}

// MoveModel moves what the user said of the model keyed from
// ("<provider>/<model>") to the key to, in every per-model map but
// ModelIDs, and says whether anything moved. A map with something under
// to already keeps it, and what from had there stays where it was: what
// the user said of the model now keyed to is theirs and is not written
// over.
func (s *Settings) MoveModel(from, to string) bool {
	moved := false
	PerModelKeys(s, func(name string, m reflect.Value) {
		if name == "ModelIDs" || m.IsNil() {
			return
		}
		fk, tk := reflect.ValueOf(from).Convert(m.Type().Key()), reflect.ValueOf(to).Convert(m.Type().Key())
		v := m.MapIndex(fk)
		if !v.IsValid() || m.MapIndex(tk).IsValid() {
			return
		}
		m.SetMapIndex(fk, reflect.Value{})
		m.SetMapIndex(tk, v)
		moved = true
	})
	return moved
}

// RenamePerModel moves what the user said of a provider's models to the id
// it has now: in every per-model map (see ModelNames) each key beginning
// with from+"/" is rewritten to to+"/", and it says whether any key moved at
// all. A provider that changes its id keeps the names, levels, image
// answers and everything else given to its models, each map moved whether or
// not the ones before it moved anything.
func (s *Settings) RenamePerModel(from, to string) bool {
	moved := false
	PerModelKeys(s, func(_ string, m reflect.Value) {
		if renameInMap(m, from, to) {
			moved = true
		}
	})
	return moved
}

// renameInMap is RenamePerModel for one of the maps: every key of from+"/..."
// is written as to+"/...", and it says whether one moved. The new key is
// built as a string and then converted to the map's own key type, because a
// field keyed by a named string type (map[modelKey]X) is per-model all the
// same, and a plain string is not assignable to one. Every per-model field
// the settings have today is keyed by string, so nothing but a test on such
// a type reaches that conversion.
func renameInMap(m reflect.Value, from, to string) bool {
	moved := false
	for _, k := range m.MapKeys() {
		rest, ok := strings.CutPrefix(k.String(), from+"/")
		if !ok {
			continue
		}
		v := m.MapIndex(k)
		m.SetMapIndex(k, reflect.Value{})
		m.SetMapIndex(reflect.ValueOf(to+"/"+rest).Convert(m.Type().Key()), v)
		moved = true
	}
	return moved
}

// Path is the settings file.
func Path() string { return appdir.Join(Dir(), "settings.json") }

// Dir is the folder every magpie file lives in: the data folder beside a
// portable magpie (appdir.Portable), else ~/.config/magpie.
func Dir() string { return appdir.Config() }

// Portable is the data folder of a portable magpie, or "" when installed.
func Portable() string { return appdir.Portable() }

// Serialize reads with saves too: the editor preserves hard links by
// writing them in place rather than replacing their inode.
var fileMu sync.RWMutex

// writing is set while fileMu is held for a write: what a write is made
// from is read from the disk even while a request holds what was read
// (filememo.Hold), or a change another magpie or the user made in the
// held second would be written over. Only set or read under fileMu.
var writing bool

// lockFile takes fileMu for a write, and returns what lets it go.
func lockFile() (unlock func()) {
	fileMu.Lock()
	writing = true
	return func() {
		writing = false
		fileMu.Unlock()
	}
}

// Load reads the settings; anything missing or unreadable is the default.
func Load() Settings {
	fileMu.RLock()
	defer fileMu.RUnlock()
	return load()
}

// LoadChecked is Load, and an error when what the file holds is unknown:
// it is there but can't be read, or it doesn't parse and has no last good
// generation to read instead. The Settings returned then are Load's (the
// defaults), which a caller guarding something with a setting (the
// gateway's allow list) must not take for the user's own: a failed read
// is unknown, never empty. No file, or an empty one, is no settings and
// not an error — the cases Save writes over, where it refuses the others.
func LoadChecked() (Settings, error) {
	fileMu.RLock()
	defer fileMu.RUnlock()
	return loadChecked()
}

// Shared is Load without a copy of its own: the settings every caller of
// Shared is given for the same settings.json, for a look made on each
// gateway request (redaction, wires, efforts, images). Only for reading:
// a caller never writes into its maps or slices (append reallocates, as
// each slice is held at its length), and one that changes anything takes
// Load's copy instead. Unreadable, it is Load's defaults as they are.
func Shared() Settings {
	fileMu.RLock()
	defer fileMu.RUnlock()
	b, err := filememo.Read("settings", Path(), func(b []byte) ([]byte, error) { return b, nil })
	if err != nil {
		return load()
	}
	if s, ok := sharedFor(b); ok {
		return s
	}
	s, err := parse(b)
	if err != nil {
		return s
	}
	if s, ok := sharedFor(b); ok {
		return s
	}
	return s
}

// SharedChecked is Shared with LoadChecked's error: the settings without
// a copy of their own while settings.json parses whole, and LoadChecked's
// copy (or its error) otherwise. Read only, as Shared is: the gateway's
// allow list asks on every request the GUI and gateway serve, and a copy
// each time was most of what a state poll's Settings cost.
func SharedChecked() (Settings, error) {
	fileMu.RLock()
	defer fileMu.RUnlock()
	if !writing {
		if b, err := filememo.Read("settings", pathNow(), func(b []byte) ([]byte, error) { return b, nil }); err == nil {
			if s, ok := sharedFor(b); ok {
				return s, nil
			}
		}
	}
	return loadChecked()
}

// SharedModelIDs is Shared().ModelIDs, read for each model a list is
// built of: only that field is parsed, once per change of the file, and a
// missing file has none without the defaults Shared builds each time.
// Unreadable or unparsable, it is what Shared says. It is shared: the
// caller must not change it.
func SharedModelIDs() map[string]string {
	fileMu.RLock()
	ids, err := filememo.Read("settings-model-ids", pathNow(), func(b []byte) (map[string]string, error) {
		var s struct {
			ModelIDs map[string]string `json:"modelIDs"`
		}
		err := json.Unmarshal(b, &s)
		return s.ModelIDs, err
	})
	fileMu.RUnlock()
	if err == nil {
		return ids
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return Shared().ModelIDs
}

// pathNow is Path, built again only when what it is built from changed:
// SharedModelIDs is read several times for each list, and Path's joins
// were most of what building one allocated.
func pathNow() string {
	k := pathFrom{appdir.Portable(), os.Getenv("XDG_CONFIG_HOME"), os.Getenv("HOME"), os.Getenv("USERPROFILE")}
	if m := lastPath.Load(); m != nil && m.from == k {
		return m.path
	}
	path := Path()
	lastPath.Store(&pathMemo{k, path})
	return path
}

type pathFrom struct{ portable, xdg, home, profile string }

type pathMemo struct {
	from pathFrom
	path string
}

var lastPath atomic.Pointer[pathMemo]

// sharedFor is the shared Settings parse keeps for contents b, when it
// keeps them.
func sharedFor(b []byte) (Settings, bool) {
	parsed.Lock()
	defer parsed.Unlock()
	if parsed.raw == nil || !bytes.Equal(parsed.raw, b) {
		return Settings{}, false
	}
	if !parsed.normed {
		parsed.norm, parsed.normed = parsed.s.clone().normal().clone(), true
	}
	return parsed.norm, true
}

// load is called with fileMu held for reading or writing.
func load() Settings {
	s, _ := loadChecked()
	return s
}

// loadChecked is LoadChecked, called with fileMu held.
func loadChecked() (Settings, error) {
	var s Settings
	// read again only once the file changed: a look at the agents asks for
	// the settings for every model of every agent (hundreds of reads, a
	// fifth of the Agents page's wait)
	through := filememo.Read[[]byte]
	if writing {
		through = filememo.ReadNow[[]byte]
	}
	b, err := through("settings", Path(), func(b []byte) ([]byte, error) { return b, nil })
	if err != nil {
		if os.IsNotExist(err) {
			// a new install: With window is its default
			s.DockSettled = true
			return s.normal(), nil
		}
		return s.settleDock().normal(), fmt.Errorf("could not read settings at %s: %w", Path(), err)
	}
	return parse(b)
}

// Defaults are the settings of a new install, with no settings.json:
// what `magpie settings list` shows as each one's default, and what
// `magpie settings reset` puts back.
func Defaults() Settings {
	return Settings{DockSettled: true}.normal()
}

// settleDock reads a Dock choice written before With window was the
// default (DockSettled unset) as that magpie did: no choice is Hide.
func (s Settings) settleDock() Settings {
	if s.DockSettled {
		return s
	}
	if !s.Dock && !s.DockWindow && !s.DockNever {
		s.DockNever = true
	}
	s.DockSettled = true
	return s
}

// parsed is the last settings.json that parsed whole and what it read as,
// before normal (s), and after it for Shared (norm, once asked): a
// gateway request asks for the settings a dozen times over, and parsing
// the file each time was most of what a request allocated. Keyed on the
// bytes themselves, so only the same contents read as the same settings.
var parsed struct {
	sync.Mutex
	raw    []byte
	s      Settings
	norm   Settings
	normed bool
}

// parse is settings.json's contents b read as Settings, from its last good
// generation when b doesn't parse. Each call has a Settings of its own to
// change: the one kept for the same bytes is cloned, never handed out.
func parse(b []byte) (Settings, error) {
	parsed.Lock()
	if parsed.raw != nil && bytes.Equal(parsed.raw, b) {
		s := parsed.s
		parsed.Unlock()
		return s.clone().normal(), nil
	}
	parsed.Unlock()
	var s Settings
	var err error
	parses.Add(1)
	perr := json.Unmarshal(bytes.TrimPrefix(b, []byte("\xef\xbb\xbf")), &s)
	if perr == nil {
		s = s.settleDock()
		// only what parsed whole is kept: a last good generation read in
		// its place is read again, as is a file that can't be read at all
		parsed.Lock()
		parsed.raw, parsed.s, parsed.norm, parsed.normed = bytes.Clone(b), s, Settings{}, false
		parsed.Unlock()
		return s.clone().normal(), nil
	}
	// one left all zero by a crash (#1505) is its last good generation
	bak, ferr := lastgood.Fallback(Path(), b, validSettings)
	if ferr == nil {
		s = Settings{}
		_ = json.Unmarshal(bytes.TrimPrefix(bak, []byte("\xef\xbb\xbf")), &s)
	} else if len(bytes.TrimSpace(bytes.TrimPrefix(b, []byte("\xef\xbb\xbf")))) != 0 {
		err = fmt.Errorf("could not read settings at %s: %w", Path(), perr)
	}
	return s.settleDock().normal(), err
}

// parses counts settings.json's parses (Parses).
var parses atomic.Int64

// Parses is how many times settings.json has been parsed in this process,
// for a test that a look holding the settings doesn't read them again for
// each thing it looks at.
func Parses() int64 { return parses.Load() }

// validSettings is settings.json's contents read as Settings.
func validSettings(b []byte) bool {
	var s Settings
	return json.Unmarshal(bytes.TrimPrefix(b, []byte("\xef\xbb\xbf")), &s) == nil
}

// CheckProxy says whether p is a proxy setting magpie takes: "" (follow),
// "direct", or an http://, https:// or socks5:// address (host:port
// meaning http). The global Proxy and a provider's own are both checked
// with it.
func CheckProxy(p string) error {
	p = strings.TrimSpace(p)
	if p == "" || p == "direct" {
		return nil
	}
	raw := p
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || !slices.Contains([]string{"http", "https", "socks5", "socks5h"}, u.Scheme) {
		return fmt.Errorf("proxy must look like http://127.0.0.1:7890 or socks5://127.0.0.1:1080")
	}
	return nil
}

// DefaultPort is the gateway's port unless Settings or MAGPIE_ADDR say
// another.
const DefaultPort = 3425

// CheckPort says what is wrong with a port for the gateway: one from 1024
// to 65535 (below that a system wants an administrator to listen on it),
// or 0 for the default.
func CheckPort(p int) error {
	if p != 0 && (p < 1024 || p > 65535) {
		return fmt.Errorf("the gateway's port is a number from 1024 to 65535, not %d", p)
	}
	return nil
}

// GatewayAddr is where the gateway listens: MAGPIE_ADDR when it is set,
// else the port Settings has, on loopback (Magic_zero on Discord: 3425 was
// taken, and a port of one's own is easier to tell apart).
func GatewayAddr() string {
	if a := os.Getenv("MAGPIE_ADDR"); a != "" {
		return a
	}
	return SavedAddr()
}

// SavedAddr is GatewayAddr with MAGPIE_ADDR left aside: the address of the
// magpie whose settings these are, which one the variable moves (magpie-dev,
// a sandbox) runs beside.
func SavedAddr() string {
	p := Shared().Port
	if p == 0 || CheckPort(p) != nil {
		p = DefaultPort
	}
	return fmt.Sprintf("127.0.0.1:%d", p)
}

// Save validates and writes the settings, preserving current recording
// consent and which agents' sessions may be read.
func Save(s Settings) error {
	defer lockFile()()
	return save(s, false)
}

// SessionReads is the user's answer to which agents' session files magpie
// reads. Before it is asked (Asked false) no agent's are read. Then an
// agent named in Agents is read as it says, and any other as All says:
// "allow all" is All with no Agents, so an agent installed later is read
// too, and "allow selected" names each agent found then.
type SessionReads struct {
	Asked  bool            `json:"asked"`
	All    bool            `json:"all,omitempty"`
	Agents map[string]bool `json:"agents,omitempty"`
}

// Reads says whether agent's session files may be read.
func (r *SessionReads) Reads(agent string) bool {
	if r == nil || !r.Asked {
		return false
	}
	if on, ok := r.Agents[agent]; ok {
		return on
	}
	return r.All
}

// SetSessionReads changes which agents' sessions may be read, as edit
// returns it from what the file holds now (nil when not settled), so a
// save elsewhere from an older snapshot neither undoes nor brings it
// back. A settings file that can't be read is left as it is, with the
// error.
func SetSessionReads(edit func(*SessionReads) *SessionReads) error {
	defer lockFile()()
	s, err := loadChecked()
	if err != nil {
		return err
	}
	var cur *SessionReads
	if s.SessionReads != nil {
		c := s.clone().SessionReads
		cur = c
	}
	s.SessionReads = edit(cur)
	return saveOwn(s, false, true)
}

// SetGatewayConversations changes local recording consent without overwriting
// other settings from an earlier snapshot.
func SetGatewayConversations(on bool) error {
	defer lockFile()()
	s := load()
	s.GatewayConversations = on
	return save(s, true)
}

// SetMTLS sets MTLS alone, from what the file holds now. A settings file
// that can't be read is left as it is, with the error.
func SetMTLS(on bool) error {
	defer lockFile()()
	s, err := loadChecked()
	if err != nil {
		return err
	}
	s.MTLS = on
	return save(s, false)
}

// SetQuitNoAsk sets QuitNoAsk alone, from what the file holds now (the
// Quit question's "Don't ask again"). A settings file that can't be read
// is left as it is, with the error.
func SetQuitNoAsk(on bool) error {
	defer lockFile()()
	s, err := loadChecked()
	if err != nil {
		return err
	}
	s.QuitNoAsk = on
	return save(s, false)
}

// RouterNoteMost is how many characters RouterNote keeps: a few words,
// said before every tool's description.
const RouterNoteMost = 80

// CleanRouterNote is note as RouterNote keeps it: one line, its spaces
// collapsed, RouterNoteMost characters at most.
func CleanRouterNote(note string) string {
	note = strings.Join(strings.Fields(note), " ")
	if r := []rune(note); len(r) > RouterNoteMost {
		note = strings.TrimSpace(string(r[:RouterNoteMost]))
	}
	return note
}

// SetRouterNote sets RouterNote alone, from what the file holds now. A
// settings file that can't be read is left as it is, with the error.
func SetRouterNote(note string) error {
	defer lockFile()()
	s, err := loadChecked()
	if err != nil {
		return err
	}
	s.RouterNote = CleanRouterNote(note)
	return save(s, true)
}

// SetRouterAllTools sets RouterAllTools alone, from what the file holds
// now. A settings file that can't be read is left as it is, with the error.
func SetRouterAllTools(on bool) error {
	defer lockFile()()
	s, err := loadChecked()
	if err != nil {
		return err
	}
	s.RouterAllTools = on
	return save(s, true)
}

// EditCheckinOff changes CheckinOff alone, as edit returns it from what the
// file holds now, so a save elsewhere from an older snapshot isn't undone.
// A settings file that can't be read is left as it is, with the error.
func EditCheckinOff(edit func([]string) []string) error {
	defer lockFile()()
	s, err := loadChecked()
	if err != nil {
		return err
	}
	off := edit(slices.Clone(s.CheckinOff))
	if len(off) == 0 {
		off = nil
	}
	s.CheckinOff = off
	return save(s, false)
}

// SetGroupDefaults sets what every routing group follows when it sets no
// value of its own (GroupSink, GroupFirstToken, GroupPatience), from what
// the file holds now, so a save elsewhere from an older snapshot isn't
// undone. A settings file that can't be read is left as it is, with the
// error.
func SetGroupDefaults(sink bool, firstToken, patience int) error {
	defer lockFile()()
	s, err := loadChecked()
	if err != nil {
		return err
	}
	s.GroupSink, s.GroupFirstToken, s.GroupPatience = sink, firstToken, patience
	return save(s, false)
}

// EditUsageAliases changes UsageAliases alone, as edit returns them from
// what the file holds now, so a save elsewhere from an older snapshot isn't
// undone. A settings file that can't be read is left as it is, with the
// error: what it holds is unknown, not "no aliases". edit is given a copy,
// and an error from it saves nothing.
func EditUsageAliases(edit func(map[string]string) (map[string]string, error)) error {
	defer lockFile()()
	s, err := loadChecked()
	if err != nil {
		return err
	}
	m, err := edit(maps.Clone(s.UsageAliases))
	if err != nil {
		return err
	}
	if len(m) == 0 {
		m = nil
	}
	s.UsageAliases = m
	return save(s, false)
}

// EditPluginOptions changes PluginOptions alone, as edit returns them from
// what the file holds now (provider id → option key → on), as
// EditUsageAliases does. edit is given a deep copy; an error from it, or a
// settings file that can't be read, saves nothing.
func EditPluginOptions(edit func(map[string]map[string]bool) (map[string]map[string]bool, error)) error {
	defer lockFile()()
	s, err := loadChecked()
	if err != nil {
		return err
	}
	cp := make(map[string]map[string]bool, len(s.PluginOptions))
	for id, o := range s.PluginOptions {
		cp[id] = maps.Clone(o)
	}
	m, err := edit(cp)
	if err != nil {
		return err
	}
	for id, o := range m {
		if len(o) == 0 {
			delete(m, id)
		}
	}
	if len(m) == 0 {
		m = nil
	}
	s.PluginOptions = m
	return save(s, false)
}

// SetRedactModel turns the local masking model on or off, on its own, as
// Settings → Privacy does once the model is downloaded; off is the
// user's own (RedactModelOff), which the default never turns on again.
func SetRedactModel(on bool) error {
	defer lockFile()()
	s := load()
	s.RedactModel, s.RedactModelOff = on, !on
	return save(s, false)
}

// UseDefaultRedactModel saves name as the model on by default once it is
// downloaded (RedactModelAuto), unless the user picked one or turned it
// off in between: it says whether it did.
func UseDefaultRedactModel(name string) (bool, error) {
	defer lockFile()()
	s := load()
	if s.RedactModelName != "" || s.RedactModelOff || s.RedactModel {
		return false, nil
	}
	s.RedactModelName, s.RedactModelAuto = name, true
	return true, save(s, false)
}

// SetRedactModelName keeps the local model the user picked while it is
// off, for turning it on.
func SetRedactModelName(name string) error {
	defer lockFile()()
	s := load()
	s.RedactModelName = name
	return save(s, false)
}

// SetRedactModelScore keeps score as the local model id's threshold; 0
// is back to the model's own.
func SetRedactModelScore(id string, score float64) error {
	defer lockFile()()
	s := load()
	m := maps.Clone(s.RedactModelScore)
	if m == nil {
		m = map[string]float64{}
	}
	if score == 0 {
		delete(m, id)
	} else {
		m[id] = score
	}
	if len(m) == 0 {
		m = nil
	}
	s.RedactModelScore = m
	return save(s, false)
}

// UseRedactModel turns the local masking model on as name, the model
// the user picked, once that model is downloaded.
func UseRedactModel(name string) error {
	defer lockFile()()
	s := load()
	s.RedactModel, s.RedactModelName, s.RedactModelOff = true, name, false
	return save(s, false)
}

// save is called under fileMu. Only the consent setter may replace recording.
func save(s Settings, recording bool) error { return saveOwn(s, recording, false) }

// saveOwn is save; reads says s's SessionReads is the one to write, where
// any other save keeps the file's.
func saveOwn(s Settings, recording, reads bool) error {
	defer filememo.Forget() // read again, where a request holds it
	// what is written is settled: a Settings read from an older file was
	// settled as it was read, and one made without reading has its Dock
	// choice read as that file's would be
	s = s.settleDock().normal()
	if !slices.Contains(Themes, s.Theme) {
		return fmt.Errorf("theme must be one of %v, not %q", Themes, s.Theme)
	}
	if !slices.Contains(Langs, s.Lang) {
		return fmt.Errorf("language must be one of %v, not %q", Langs, s.Lang)
	}
	if !slices.Contains(Trays, s.Tray) {
		return fmt.Errorf("tray must be one of %v, not %q", Trays, s.Tray)
	}
	if s.SessionTerminal != "" && s.SessionTerminal != "system" && !validTerminalBundleID.MatchString(s.SessionTerminal) {
		return fmt.Errorf("session terminal must be an app bundle id or system, not %q", s.SessionTerminal)
	}
	for _, choice := range []*fonts.Face{s.UIFont, s.CodeFont} {
		if err := fonts.Validate(choice); err != nil {
			return err
		}
	}
	if !slices.Contains(Currencies, s.Currency) {
		return fmt.Errorf("currency must be one of %v, not %q", Currencies, s.Currency)
	}
	if !slices.Contains(Warmups, s.CodexWarmup) {
		return fmt.Errorf("codex warm-up must be off, week or all, not %q", s.CodexWarmup)
	}
	if !slices.Contains(Warmups, s.ClaudeWarmup) {
		return fmt.Errorf("claude warm-up must be off, week or all, not %q", s.ClaudeWarmup)
	}
	for _, at := range slices.Concat(s.CodexWarmAts, s.ClaudeWarmAts) {
		if _, _, ok := Clock(at); !ok {
			return fmt.Errorf("a warm-up's time of day must look like 06:00, not %q", at)
		}
	}
	for user, at := range s.CodexWarmAtOf {
		if _, _, ok := Clock(at); at != "off" && !ok {
			return fmt.Errorf("%s's warm-up time of day must look like 06:00 or be off, not %q", user, at)
		}
	}
	if _, _, ok := Clock(s.CheckinAt); s.CheckinAt != "" && !ok {
		return fmt.Errorf("the check-in's time of day must look like 09:00, not %q", s.CheckinAt)
	}
	if !slices.Contains(TrayEvery, s.TrayUsageEvery) {
		return fmt.Errorf("the menu bar's usage is refreshed every %v minutes, not %d", TrayEvery, s.TrayUsageEvery)
	}
	if !slices.Contains(UpdateEveries, s.UpdateEvery) {
		return fmt.Errorf("magpie checks for updates every %v minutes, not %d", UpdateEveries, s.UpdateEvery)
	}
	if !slices.Contains(UpdateChannels, s.UpdateChannel) {
		return fmt.Errorf("the update channel is %q (stable) or empty (every version), not %q", UpdateStable, s.UpdateChannel)
	}
	if m := s.UpdateMirror; m != "" {
		if u, err := url.Parse(m); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return fmt.Errorf("an update mirror is an http(s) address put before the github.com URL, like https://mirror.example/, not %q", m)
		}
	}
	if s.ImageTimeout < 0 || s.ImageTimeout > ImageTimeoutMost {
		return fmt.Errorf("image generation is given from 1 to %d minutes, or 0 for %d, not %d", ImageTimeoutMost, ImageTimeoutDefault, s.ImageTimeout)
	}
	if s.ClassifyTimeout < 0 || s.ClassifyTimeout > ClassifyTimeoutMost {
		return fmt.Errorf("a routing group's classifier is given from 1 to %d seconds, or 0 for %d, not %d", ClassifyTimeoutMost, ClassifyTimeoutDefault, s.ClassifyTimeout)
	}
	if s.GroupFirstToken < 0 {
		return fmt.Errorf("a routing group's member is given a number of seconds to its first token, or 0 to wait, not %d", s.GroupFirstToken)
	}
	// provider.PatienceOff and provider.PatienceLongest, which settings
	// can't import
	if s.GroupPatience < -1 || s.GroupPatience > 240 {
		return fmt.Errorf("a routing group whose every member is busy waits from 1 to 240 seconds, 0 for 60 or -1 for not at all, not %d", s.GroupPatience)
	}
	if s.SearchCount < 0 || s.SearchCount > SearchCountMost {
		return fmt.Errorf("a search is answered with from 1 to %d results, or 0 for %d, not %d", SearchCountMost, SearchCountDefault, s.SearchCount)
	}
	if s.BusyLoad < 0 || s.BusyLoad >= BusyLoadMost {
		return fmt.Errorf("busy models go last over a load from 1 to %d%%, or 0 for off, not %d", BusyLoadMost-1, s.BusyLoad)
	}
	if s.UsageAlert < 0 || s.UsageAlert > 100 {
		return fmt.Errorf("a usage alert is at a percentage from 1 to 100, or 0 for off, not %d", s.UsageAlert)
	}
	if s.ResetReminder < 0 || s.ResetReminder > 168 {
		return fmt.Errorf("a reset reminder is from 1 to 168 hours before, or 0 for off, not %d", s.ResetReminder)
	}
	if s.TurnDoneAlert < 0 || s.TurnDoneAlert > 86400 {
		return fmt.Errorf("a turn is said done after 1 to 86400 seconds, or 0 for off, not %d", s.TurnDoneAlert)
	}
	if math.IsNaN(s.BalanceAlert) || math.IsInf(s.BalanceAlert, 0) || s.BalanceAlert < 0 {
		return fmt.Errorf("a balance alert is at an amount of 0 or more (0 for off), not %v", s.BalanceAlert)
	}
	if s.ReplyModel != "" && !slices.Contains(ReplyModels, s.ReplyModel) {
		return fmt.Errorf("a reply's model names %q (the model asked for), %q or %q, not %q", ReplyAsked, ReplyMember, ReplyVendor, s.ReplyModel)
	}
	if s.QuotaReads != "" && s.QuotaReads != "asked" {
		return fmt.Errorf(`allowances are read "" (whenever magpie needs them) or "asked" (only when asked), not %q`, s.QuotaReads)
	}
	if err := CheckPort(s.Port); err != nil {
		return err
	}
	if !slices.Contains(TextSizes, s.TextSize) {
		return fmt.Errorf("text size must be one of %v percent, not %d", TextSizes, s.TextSize)
	}
	s.OTel.Endpoint = strings.TrimRight(strings.TrimSpace(s.OTel.Endpoint), "/")
	if err := s.OTel.Check(); err != nil {
		return err
	}
	s.Proxy = strings.TrimSpace(s.Proxy)
	if err := CheckProxy(s.Proxy); err != nil {
		return err
	}
	if s.Proxy != "" && s.Proxy != "direct" {
		s.ProxyCustom = s.Proxy
	}
	s.PluginProxy = strings.TrimSpace(s.PluginProxy)
	if err := CheckProxy(s.PluginProxy); err != nil {
		return fmt.Errorf("plugin downloads: %w", err)
	}
	if s.PluginProxy != "" && s.PluginProxy != "direct" {
		s.PluginProxyCustom = s.PluginProxy
	}
	s.Vision = strings.TrimSpace(s.Vision)
	if s.Vision != "" && s.Vision != "off" && !strings.Contains(s.Vision, "/") {
		return fmt.Errorf("the vision model must be a model's id such as openai/gpt-5-mini, or off, not %q", s.Vision)
	}
	s.CodexTitles = strings.TrimSpace(s.CodexTitles)
	if s.CodexTitles != "" && s.CodexTitles != "off" && !strings.Contains(s.CodexTitles, "/") {
		return fmt.Errorf("the model for Codex's titles must be a model's id such as openai/gpt-5-mini, or off, not %q", s.CodexTitles)
	}
	s.CodexDescriptions = strings.TrimSpace(s.CodexDescriptions)
	if s.CodexDescriptions != "" && s.CodexDescriptions != "off" && !strings.Contains(s.CodexDescriptions, "/") {
		return fmt.Errorf("the model for Codex's descriptions must be a model's id such as openai/gpt-5-mini, or off, not %q", s.CodexDescriptions)
	}
	s.CodexPredictions = strings.TrimSpace(s.CodexPredictions)
	if s.CodexPredictions != "" && s.CodexPredictions != "off" && !strings.Contains(s.CodexPredictions, "/") {
		return fmt.Errorf("the model for Codex's composer predictions must be a model's id such as openai/gpt-5-mini, or off, not %q", s.CodexPredictions)
	}
	s.CodexAutoReview = strings.TrimSpace(s.CodexAutoReview)
	if s.CodexAutoReview != "" && s.CodexAutoReview != CodexAutoReviewSession && !strings.Contains(s.CodexAutoReview, "/") {
		return fmt.Errorf("the model for Codex's auto-review must be a model's id such as openai/gpt-5-mini, not %q", s.CodexAutoReview)
	}
	s.CodexSubagentModel = strings.TrimSpace(s.CodexSubagentModel)
	if s.CodexSubagentModel != "" && s.CodexSubagentModel != CodexSubagentLead && !strings.Contains(s.CodexSubagentModel, "/") {
		return fmt.Errorf("the model for Codex's subagents must be a model's id such as codex/gpt-5.5, not %q", s.CodexSubagentModel)
	}
	s.CodexServiceTier = strings.TrimSpace(s.CodexServiceTier)
	if !slices.Contains(CodexServiceTiers, s.CodexServiceTier) {
		return fmt.Errorf("Codex's service tier must be one of %q, not %q", CodexServiceTiers[1:], s.CodexServiceTier)
	}
	s.RedactRouteTo = strings.TrimSpace(s.RedactRouteTo)
	if s.RedactRouteTo != "" && !strings.Contains(s.RedactRouteTo, "/") {
		return fmt.Errorf("the model for requests masking would change must be a model's id such as ollama/qwen3, not %q", s.RedactRouteTo)
	}
	s.DesktopSubagentModel = strings.TrimSpace(s.DesktopSubagentModel)
	if s.DesktopSubagentModel != "" && !strings.Contains(s.DesktopSubagentModel, "/") {
		return fmt.Errorf("the model for Claude Desktop's subagents must be a model's id such as deepseek/deepseek-v4, not %q", s.DesktopSubagentModel)
	}
	s.DshCompactModel = strings.TrimSpace(s.DshCompactModel)
	if s.DshCompactModel != "" && !strings.Contains(s.DshCompactModel, "/") {
		return fmt.Errorf("the model for DeepSeek Harness's compaction must be a model's id such as deepseek/deepseek-v4, not %q", s.DshCompactModel)
	}
	s.DesktopSubagentEffort = strings.ToLower(strings.TrimSpace(s.DesktopSubagentEffort))
	if e := s.DesktopSubagentEffort; e != "" && (!slices.Contains(catalog.EffortRank, e) || e == "ultra") {
		return fmt.Errorf("%q is not a reasoning effort for Claude Desktop's subagents", e)
	}
	s.Searcher = strings.TrimSpace(s.Searcher)
	var then []string
	for _, v := range s.SearcherThen {
		if v = strings.TrimSpace(v); v != "" && v != "off" && v != s.Searcher && !slices.Contains(then, v) {
			then = append(then, v)
		}
	}
	s.SearcherThen = then
	for _, e := range []*string{&s.VisionEffort, &s.SearcherEffort} {
		if *e = strings.ToLower(strings.TrimSpace(*e)); *e != "" && !slices.Contains(catalog.EffortRank, *e) {
			return fmt.Errorf("a model's reasoning must be one of %s, or empty for the default, not %q", strings.Join(catalog.EffortRank, ", "), *e)
		}
	}
	if s.SearchFirst = strings.TrimSpace(s.SearchFirst); s.SearchFirst == SearchFirstModel {
		s.SearchFirst = ""
	}
	if s.SearchFirst != "" && s.SearchFirst != SearchFirstAPI {
		return fmt.Errorf("web search must go first to %q or %q, not %q", SearchFirstModel, SearchFirstAPI, s.SearchFirst)
	}
	s.ImageGen = strings.TrimSpace(s.ImageGen)
	if s.ImageGen != "" && s.ImageGen != "off" && !strings.Contains(s.ImageGen, "/") {
		return fmt.Errorf("the image generation model must be a model's id such as openai/gpt-image-2, or off, not %q", s.ImageGen)
	}
	s.VideoGen = strings.TrimSpace(s.VideoGen)
	if s.VideoGen != "" && !strings.Contains(s.VideoGen, "/") {
		return fmt.Errorf("the video generation model must be a model's id such as grok/grok-imagine-video, not %q", s.VideoGen)
	}
	rules, err := redact.CheckRules(s.RedactRules)
	if err != nil {
		return err
	}
	s.RedactRules = rules
	if s.RedactActions, err = redactActions(s.RedactActions); err != nil {
		return err
	}
	if s.RedactAllow, err = redactAllow(s.RedactAllow); err != nil {
		return err
	}
	s.AgentOrder, s.AgentsHidden, s.AgentsShown = ids(s.AgentOrder), ids(s.AgentsHidden), ids(s.AgentsShown)
	s.AgentsHere = ids(s.AgentsHere)
	s.UsageOrder = ids(s.UsageOrder)
	s.PanelUsageHidden = ids(s.PanelUsageHidden)
	s.UsageAgentsHidden = ids(s.UsageAgentsHidden)
	s.UsageCardsHidden = ids(s.UsageCardsHidden)
	s.ClientAgents = clientAgents(s.ClientAgents)
	s.UsageCleared = usageCleared(s.UsageCleared)
	s.TrayUsages = ids(s.TrayUsages)
	s.TrayWindows = trayWindows(s.TrayWindows)
	for i, u := range s.CodexAutoReset {
		s.CodexAutoReset[i] = strings.ToLower(u)
	}
	s.CodexAutoReset = ids(s.CodexAutoReset)
	only := map[string]string{}
	for u, c := range s.CodexAutoResetOnly {
		if u = strings.ToLower(u); (c == "week" || c == "expiring") && slices.Contains(s.CodexAutoReset, u) {
			only[u] = c
		}
	}
	s.CodexAutoResetOnly = nil
	if len(only) > 0 {
		s.CodexAutoResetOnly = only
	}
	for i, u := range s.CodexNoCredits {
		s.CodexNoCredits[i] = strings.ToLower(u)
	}
	s.CodexNoCredits = ids(s.CodexNoCredits)
	for i, u := range s.CodexCreditsLast {
		s.CodexCreditsLast[i] = strings.ToLower(u)
	}
	s.CodexCreditsLast = ids(s.CodexCreditsLast)
	for i, u := range s.CodexCreditsFirst {
		s.CodexCreditsFirst[i] = strings.ToLower(u)
	}
	s.CodexCreditsFirst = ids(s.CodexCreditsFirst)
	s.TrayUsage = ""
	if len(s.TrayUsages) > 0 {
		s.TrayUsage = s.TrayUsages[0]
	}
	// Load may have returned defaults or only part of an unreadable file.
	// Do not replace it, including its permissions, with those values.
	consent := false
	var storedReads *SessionReads
	if b, err := steady.ReadFile(Path()); err == nil {
		b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
		if len(bytes.TrimSpace(b)) != 0 {
			var stored Settings
			if err := json.Unmarshal(b, &stored); err != nil {
				// a file a crash left all zero (#1505) is saved over once it
				// is kept aside, when Load had its last good generation
				bak, ferr := lastgood.Fallback(Path(), b, validSettings)
				if ferr != nil {
					return fmt.Errorf("could not read settings at %s; repair or move that file aside before saving: %w", Path(), err)
				}
				stored = Settings{}
				_ = json.Unmarshal(bytes.TrimPrefix(bak, []byte("\xef\xbb\xbf")), &stored)
			}
			consent = stored.GatewayConversations
			storedReads = stored.SessionReads
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("could not read settings at %s: %w", Path(), err)
	}
	if !recording {
		s.GatewayConversations = consent
	}
	if !reads {
		s.SessionReads = storedReads
	}
	// the file there now is kept first, before anything below can make
	// one: as the last good generation, or aside when it doesn't read
	if err := lastgood.Keep(Path(), validSettings, 0o600); err != nil {
		return err
	}
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	// Restrict existing settings without changing the owner's permissions.
	if fi, err := os.Stat(Path()); err == nil && fi.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(Path(), fi.Mode().Perm()&0o700); err != nil {
			return err
		}
	}
	// Open without truncating: read-only settings must still reject saves,
	// and a new file must have the private mode WriteAtomic will preserve.
	f, err := os.OpenFile(Path(), os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := edit.WriteAtomic(Path(), append(b, '\n')); err != nil {
		return err
	}
	// a request holding the catalog holds the settings too: it sees these
	if catalog.Forget != nil {
		catalog.Forget()
	}
	return nil
}

func (s Settings) normal() Settings {
	if s.Theme == "" {
		s.Theme = "system"
	}
	if s.Lang == "" {
		s.Lang = "system"
	}
	if s.Tray == "" {
		s.Tray = "panel"
	}
	if s.Currency == "" {
		s.Currency = "usd"
	}
	if s.CodexWarmup == "off" {
		s.CodexWarmup = ""
	}
	if s.ClaudeWarmup == "off" {
		s.ClaudeWarmup = ""
	}
	if s.TrayUsageEvery == 0 {
		s.TrayUsageEvery = 3
	}
	if s.UpdateEvery == 0 {
		s.UpdateEvery = 360
	}
	if s.TextSize == 0 {
		s.TextSize = DefaultTextSize()
	}
	// one card, as a magpie before TrayUsages kept it; an empty list
	// sent on purpose (all of them turned off) stays empty
	if s.TrayUsages == nil && s.TrayUsage != "" {
		s.TrayUsages = []string{s.TrayUsage}
	}
	// the times of day: the one a magpie before the lists kept, when the
	// list isn't there (an empty one sent on purpose stays empty), each as
	// 06:00 whichever way it came (6:00, 06:00:00), earliest first, once;
	// and the first kept as the one for that older magpie
	for _, w := range []struct {
		at  *string
		ats *[]string
	}{{&s.CodexWarmAt, &s.CodexWarmAts}, {&s.ClaudeWarmAt, &s.ClaudeWarmAts}} {
		if *w.ats == nil && strings.TrimSpace(*w.at) != "" {
			*w.ats = []string{*w.at}
		}
		*w.ats = clocks(*w.ats)
		*w.at = ""
		if len(*w.ats) > 0 {
			*w.at = (*w.ats)[0]
		}
	}
	// the check-in's time of day, as 09:00 whichever way it came
	s.CheckinAt = strings.TrimSpace(s.CheckinAt)
	if h, m, ok := Clock(s.CheckinAt); ok {
		s.CheckinAt = fmt.Sprintf("%02d:%02d", h, m)
	}
	// a reply's model name: memberModel, as a magpie before replyModel
	// kept it, when replyModel isn't set, and kept beside it for that
	// magpie
	switch s.ReplyModel {
	case "":
		if s.MemberModel {
			s.ReplyModel = ReplyMember
		}
	case ReplyAsked:
		s.ReplyModel = ""
	}
	s.MemberModel = s.ReplyModel == ReplyMember
	// an account's own, by its name in lower case; one with none follows
	// CodexWarmAt and isn't kept
	if s.CodexWarmAtOf != nil {
		of := map[string]string{}
		for user, at := range s.CodexWarmAtOf {
			user, at = strings.ToLower(strings.TrimSpace(user)), strings.ToLower(strings.TrimSpace(at))
			if h, m, ok := Clock(at); ok {
				at = fmt.Sprintf("%02d:%02d", h, m)
			}
			if user != "" && at != "" {
				of[user] = at
			}
		}
		s.CodexWarmAtOf = of
		if len(of) == 0 {
			s.CodexWarmAtOf = nil
		}
	}
	return s
}

// Clock reads a time of day, "06:00" (seconds, as a time field may send
// them, are dropped), as its hour and minute.
func Clock(at string) (hour, min int, ok bool) {
	for _, layout := range []string{"15:04", "15:04:05"} {
		if t, err := time.Parse(layout, at); err == nil {
			return t.Hour(), t.Minute(), true
		}
	}
	return 0, 0, false
}

// clocks is ats as 06:00 (one Clock can't read kept as it came, for Save
// to name), earliest first, with no empties or repeats; nil when none,
// and an empty list stays one.
func clocks(ats []string) []string {
	if ats == nil {
		return nil
	}
	out := []string{}
	for _, at := range ats {
		at = strings.TrimSpace(at)
		if h, m, ok := Clock(at); ok {
			at = fmt.Sprintf("%02d:%02d", h, m)
		}
		if at != "" && !slices.Contains(out, at) {
			out = append(out, at)
		}
	}
	slices.Sort(out)
	return out
}

// ids trims, drops empties and repeats, and keeps the first of each.
func ids(in []string) []string {
	var out []string
	for _, x := range in {
		if x = strings.TrimSpace(x); x != "" && !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}

// trayWindows is TrayWindows as kept: each provider's kinds trimmed, once
// each, and a provider with none left out, as it shows every window.
func trayWindows(m map[string][]string) map[string][]string {
	out := map[string][]string{}
	for p, kinds := range m {
		if p, kinds = strings.TrimSpace(p), ids(kinds); p != "" && len(kinds) > 0 {
			out[p] = kinds
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// clientAgents is ClientAgents as kept: the clients' names lower-cased and
// trimmed, an empty name or agent left out.
func clientAgents(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		k, v = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
		if k != "" && v != "" {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// UsageClear is one clear of the Usage page's figures: the calls up to At
// (Unix milliseconds) of Model at Provider, as the page's Models list
// names them (the model counted under its usage alias, the provider under
// the id it is shown by now), stop being counted there. Both "" is every
// call.
type UsageClear struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	At       int64  `json:"at"`
}

// Time is when the clear was made.
func (c UsageClear) Time() time.Time { return time.UnixMilli(c.At) }

// Covers is whether c clears every call o clears: everything, or the same
// model, up to a time no earlier.
func (c UsageClear) Covers(o UsageClear) bool {
	return c.At >= o.At && (c.Provider == "" && c.Model == "" || c.Provider == o.Provider && c.Model == o.Model)
}

// usageCleared is UsageCleared as kept: one without a time left out, and
// one another covers (a later clear of everything, or of the same model)
// dropped, so the list holds no more than one a model.
func usageCleared(in []UsageClear) []UsageClear {
	var out []UsageClear
	for i, c := range in {
		c.Provider, c.Model = strings.TrimSpace(c.Provider), strings.TrimSpace(c.Model)
		if c.At <= 0 {
			continue
		}
		covered := false
		for j, o := range in {
			o.Provider, o.Model = strings.TrimSpace(o.Provider), strings.TrimSpace(o.Model)
			// of two the same, the first is kept
			if j != i && o.At > 0 && o.Covers(c) && (!c.Covers(o) || j < i) {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, c)
		}
	}
	return out
}

// ClearUsage adds a clear of the Usage page's figures up to at: of model at
// provider, or of everything with both "". It changes UsageCleared alone,
// from what the file holds now, so a save elsewhere from an older snapshot
// isn't undone; a settings file that can't be read is left as it is, with
// the error.
func ClearUsage(provider, model string, at time.Time) error {
	provider, model = strings.TrimSpace(provider), strings.TrimSpace(model)
	if model == "" && provider != "" {
		return fmt.Errorf("a clear of %q's usage names its model", provider)
	}
	if at.UnixMilli() <= 0 {
		return fmt.Errorf("a clear of usage needs a time")
	}
	defer lockFile()()
	s, err := loadChecked()
	if err != nil {
		return err
	}
	s.UsageCleared = usageCleared(append(slices.Clone(s.UsageCleared), UsageClear{Provider: provider, Model: model, At: at.UnixMilli()}))
	return save(s, false)
}

// UsageClearedNow is Load().UsageCleared, parsed again only once the file
// changed: every read of the usage pages asks for it.
func UsageClearedNow() []UsageClear {
	fileMu.RLock()
	defer fileMu.RUnlock()
	m, err := filememo.Read("settings.usageCleared", Path(), func(b []byte) ([]UsageClear, error) {
		if len(bytes.TrimSpace(b)) == 0 {
			return nil, nil
		}
		var s struct {
			UsageCleared []UsageClear `json:"usageCleared"`
		}
		if err := json.Unmarshal(b, &s); err != nil {
			return nil, err
		}
		return usageCleared(s.UsageCleared), nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		// what load makes of a file that doesn't parse (its last good
		// generation), not none
		return usageCleared(load().UsageCleared)
	}
	return m
}

// ClientAgentsNow is Load().ClientAgents, parsed again only once the file
// changed: the usage pages ask for it on every read of the ledger, which
// the gateway does too, and the whole settings file is more to parse.
func ClientAgentsNow() map[string]string {
	fileMu.RLock()
	defer fileMu.RUnlock()
	m, err := filememo.Read("settings.clientAgents", Path(), func(b []byte) (map[string]string, error) {
		if len(bytes.TrimSpace(b)) == 0 {
			return nil, nil
		}
		var s struct {
			ClientAgents map[string]string `json:"clientAgents"`
		}
		if err := json.Unmarshal(b, &s); err != nil {
			return nil, err
		}
		return clientAgents(s.ClientAgents), nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		// what load makes of a file that doesn't parse (its last good
		// generation), not none
		return clientAgents(load().ClientAgents)
	}
	return m
}
