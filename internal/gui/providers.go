package gui

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/autostart"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/library"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/update"
	"github.com/yetone/magpie/internal/upstream"
	"github.com/yetone/magpie/internal/usage"
)

// The providers page: the vendors the user added, the presets they can add
// with one key, the gateway that fronts them, and who is routed where.

type modelJSON struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`              // the user's name for it, if they gave one
	Default string   `json:"default,omitempty"` // its own name, when the user gave it another
	Plain   string   `json:"plain,omitempty"`   // a remote magpie's model's name there alone, when Name has its provider there after it (catalog.Model's)
	By      string   `json:"by,omitempty"`      // a remote magpie's model's provider there, the first in a chain (catalog.Model's)
	Kept    []string `json:"kept,omitempty"`    // the reasoning levels the user keeps of Efforts, when not all
	Efforts []string `json:"efforts,omitempty"`
	Given   bool     `json:"given,omitempty"` // its levels aren't known: Efforts are those it can be given, Kept those it was
	// Reasonless: the user said it takes no reasoning ask
	// (provider.SetModelReasonless): none of Efforts is kept, and none is sent
	Reasonless bool    `json:"reasonless,omitempty"`
	Images     bool    `json:"images"`              // agents are told it can see images
	ImageSet   bool    `json:"imageSet,omitempty"`  // the user said so, rather than its vendor
	Own        bool    `json:"ownImages,omitempty"` // its vendor's answer, which a staged Restore default shows
	On         bool    `json:"on"`                  // exposed to agents
	Best       bool    `json:"best,omitempty"`      // the newest of its family in the list: what Best in family serves (provider.NewestInFamily)
	Context    int     `json:"context,omitempty"`   // the window agents are told: the user's, else Listed
	Output     int     `json:"output,omitempty"`    // the reply limit agents are told: provider.ReplyLimit within Context (provider.OutputWithin)
	Listed     int     `json:"listed,omitempty"`    // its window before the user's: its vendor's list's, else models.dev's; absent when Context
	Max        int     `json:"max,omitempty"`       // the most its context may be set to, above Listed
	Free       bool    `json:"free,omitempty"`      // costs the subscription nothing
	Zero       bool    `json:"zero,omitempty"`      // listed at $0 on this provider (provider.PricedFree), where Free is the vendor's word
	Rate       float64 `json:"rate,omitempty"`      // the credits a request costs the subscription, as a multiple
	RateWas    float64 `json:"rateWas,omitempty"`   // the rate before a discount running now
	// Load is how busy its plugin last said the model is, a percent, when
	// that was within plugin.LoadFresh (magpie.load); nil when unknown
	Load  *float64 `json:"load,omitempty"`
	API   string   `json:"api,omitempty"`   // the one API the user said it is asked on
	Auto  []string `json:"auto,omitempty"`  // the APIs its vendor's list says it is served on, what Auto asks it on
	NoAPI bool     `json:"noAPI,omitempty"` // the provider has a URL for none of them: agents aren't offered it (provider.NoAPIFor, #2094)
	Same  string   `json:"same,omitempty"`  // the model the user said it is the same as, for the groups magpie finds (#583)
	Merge string   `json:"merge,omitempty"` // what those groups merge it by when the user says nothing; absent when ID
	Kind  string   `json:"kind,omitempty"`  // what the user said it makes: image or video (#1399)
	// Headers are the request headers the user gave the model, sent after
	// the provider's (provider.SetModelHeaders)
	Headers map[string]string `json:"headers,omitempty"`
	// System is the system prompt the user gave the model, added after the
	// agent's own (provider.SetModelSystemPrompt)
	System string `json:"system,omitempty"`
	// Tier is the speed the user gave the model, "priority" or "ultrafast"
	// (provider.SetModelTier); CanFast says it has a fast mode to give,
	// CanUltra that an account offers the faster one (kdajenks on X)
	Tier     string `json:"tier,omitempty"`
	CanFast  bool   `json:"canFast,omitempty"`
	CanUltra bool   `json:"canUltra,omitempty"`
	// Vendor is the provider's own id of a model the user lists by
	// another (provider.SetModelID), "" for one listed by its own; IDs
	// says the provider's models can be given ids at all (a key's)
	Vendor string `json:"vendor,omitempty"`
	IDs    bool   `json:"ids,omitempty"`
	// Desktop is the number Claude Desktop's menu says beside this model
	// (gateway.DesktopNumber), so a row of that menu can be matched to its
	// provider (#2578): only for a model Claude Desktop, installed here,
	// is shown and names after its own catalog, "" for every other.
	Desktop string `json:"desktop,omitempty"`
	// what it costs, USD per million tokens (#819): the price the user set
	// for it, and its list price, its vendor's else its maker's, before the
	// provider's price rate
	Price *catalog.Price `json:"price,omitempty"`
	List  *catalog.Price `json:"list,omitempty"`
	// RemoteList says the list price is what the other magpie counts the
	// model at, as its list told (a Remote magpie's), not models.dev's
	RemoteList bool `json:"remoteList,omitempty"`
	// ListedOutput is its reply limit before the user's Max output: its
	// vendor's list's, else models.dev's, within Listed; Model settings'
	// Max output shows it when none is set (sayagoodbyeplz on Discord)
	ListedOutput int `json:"listedOutput,omitempty"` // absent when Output
}

type providerJSON struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Icon      string            `json:"icon"`
	IconFrom  string            `json:"iconFrom,omitempty"` // the address Icon was fetched from (provider.IconFrom)
	Preset    string            `json:"preset"`
	Host      string            `json:"host"`
	Chat      string            `json:"chat"`
	Responses string            `json:"responses"`
	Anthropic string            `json:"anthropic"`
	Gemini    string            `json:"gemini,omitempty"` // a Gemini API's base (#1346)
	Decide    string            `json:"decide,omitempty"` // a decision API: it only routes groups
	Catalog   string            `json:"catalog"`
	Website   string            `json:"website"`
	KeysURL   string            `json:"keysUrl"`
	Headers   map[string]string `json:"headers,omitempty"`
	// JSON fields added to each request's body (provider.ExtraBody, #1604)
	ExtraBody provider.ExtraBody `json:"extraBody,omitempty"`
	// headers of the vendor's reply passed on (provider.ReplyHeaders, #4145)
	ReplyHeaders []string `json:"replyHeaders,omitempty"`
	// headers of the agent's request passed on (provider.PassHeaders)
	PassHeaders []string `json:"passHeaders,omitempty"`
	// the API the user gave a custom provider's Base URL as (BaseAPI)
	BaseAPI string `json:"baseAPI,omitempty"`
	// the vendor searches the web by itself (provider.Searches)
	Searches bool `json:"searches"`
	// on the Cline API: its DeepSeek models are served by DeepSeek's own
	// API alone and its GLM models by Z.ai's (provider.PinUpstream), which
	// only it offers
	Cline       bool `json:"cline,omitempty"`
	PinUpstream bool `json:"pinUpstream"`
	// on this machine or the local network, set to go unmasked by
	// Settings' redaction (provider.Unredacted)
	Unredacted bool `json:"unredacted"`
	// every model of it counted free by the user's word, for Free first
	// and Cheapest (provider.CountFree)
	CountFree bool `json:"countFree,omitempty"`
	// how it bills, by the user's word ("" for magpie's reading), and as
	// it is taken: plan, payg, mixed or "" when unknown
	// (provider.BillingKind; EV_John_Liu on X)
	Billing     string `json:"billing,omitempty"`
	BillingKind string `json:"billingKind,omitempty"`
	// a provider the user added whose own HTTPS host's certificate is
	// taken as it is (provider.InsecureTLS, #2383); TakesInsecureTLS: it
	// may be, being the user's own and not a preset's or a sign-in's
	InsecureTLS      bool `json:"insecureTLS"`
	TakesInsecureTLS bool `json:"takesInsecureTLS"`
	// spoken HTTP/1.1 to, never HTTP/2 (provider.HTTP1); taken where
	// TakesInsecureTLS is
	HTTP1 bool `json:"http1"`
	// the client certificate, its key and the server CA it is reached
	// with (provider.ClientCert, #5596), as shown; TakesClientCert: it
	// may have them, being the user's own or Remote magpie
	ClientCert      string `json:"clientCert,omitempty"`
	ClientKey       string `json:"clientKey,omitempty"`
	ServerCA        string `json:"serverCA,omitempty"`
	TakesClientCert bool   `json:"takesClientCert,omitempty"`
	// the server certificate its own hosts are taken with alone, their
	// certificate otherwise unchecked (provider.ServerPin)
	ServerPin string `json:"serverPin,omitempty"`
	// TakesServerPin: an https:// address of the user's own to pin
	// (provider.PinHosts), a preset's pointed at their own server too
	TakesServerPin bool `json:"takesServerPin,omitempty"`
	// its Responses turns over the Responses WebSocket (provider.Socket,
	// #5781): auto, on or ""; TakesSocket: a provider of a key with a
	// Responses URL
	Socket      string `json:"socket"`
	TakesSocket bool   `json:"takesSocket"`
	// asked as usual after a quota refusal or a 429, never set aside for
	// it (provider.LimitRestOff)
	LimitRestOff bool `json:"limitRestOff"`
	// a Remote magpie's models named by the provider serving each there
	// (provider.UpstreamProviders, lcc580 on X)
	UpstreamProviders bool `json:"upstreamProviders,omitempty"`
	// DeepSeek's own API sent an agent's images inline on every turn, not
	// once by file id (provider.ImageFilesOff, #5407); TakesImageFiles:
	// it is DeepSeek's own API, so the editor offers it
	ImageFilesOff   bool `json:"imageFilesOff"`
	TakesImageFiles bool `json:"takesImageFiles,omitempty"`
	// Codex's compaction asked of it as Codex sent it, its sealed item
	// back as it came (provider.NativeCompaction, #1099); CarriesCompaction: its
	// wire can carry it, so the editor offers it
	NativeCompaction  bool `json:"nativeCompaction"`
	CarriesCompaction bool `json:"carriesCompaction,omitempty"`
	// requests go with the agent's own User-Agent, not magpie's
	// (provider.MagpieUserAgent off, #1851); OwnClient: they go as a client of
	// the vendor's own whatever is set (provider.OwnClient), so the
	// editor offers no such choice
	PassUserAgent bool `json:"passUserAgent"`
	OwnClient     bool `json:"ownClient,omitempty"`
	// its errors go to the agent as the vendor sent them, asked once
	// (provider.PassErrors, ne9ko9); TakesPassErrors: it is a provider
	// of a key, so the editor offers it
	PassErrors      bool `json:"passErrors"`
	TakesPassErrors bool `json:"takesPassErrors,omitempty"`
	// which header its key goes in on a third-party Anthropic API
	// (provider.AuthHeader: "" both, "x-api-key", "bearer"), and whether
	// it is one that sends both by default (provider.BothAuth)
	AuthHeader string `json:"authHeader"`
	BothAuth   bool   `json:"bothAuth,omitempty"`
	// the proxy its requests go through: "" the global one, "direct"
	// none, or an address (#237)
	Proxy string `json:"proxy"`
	// the proxy of each of a subscription's accounts that has one of its
	// own, by its name in lower case; the others follow Proxy
	AccountProxies map[string]string `json:"accountProxies,omitempty"`
	// the models each account or key the user narrowed serves alone, by
	// its name in lower case or its key's id (#474); the others serve all
	AccountModels map[string][]string `json:"accountModels,omitempty"`
	// the usage cap each account the user capped is held at, in percent
	// of its windows, by its name in lower case (provider.AccountCaps)
	AccountCaps map[string]int `json:"accountCaps,omitempty"`
	// the share of a window each account is held at where the user set
	// one for that window apart, by the account and then the window's
	// capId (provider.AccountWindowCaps); 100 is none on that window
	AccountWindowCaps map[string]map[string]int `json:"accountWindowCaps,omitempty"`
	// AccountWindowAmounts: a window capped at a fixed amount of its own
	// unit in place of a share (provider.AccountWindowAmounts)
	AccountWindowAmounts map[string]map[string]float64 `json:"accountWindowAmounts,omitempty"`
	// the names the user gave its accounts, by the account in lower
	// case (provider.AccountNames, #1515), and the name of the key plan
	// each account was found to stand in for (provider.SeatNameOf): the
	// name an account has until the user gives it one
	AccountNames map[string]string `json:"accountNames,omitempty"`
	AccountSeats map[string]string `json:"accountSeats,omitempty"`
	// the name a plugin's usage gives each account where it isn't the
	// accountId the account is kept by (Provider.PluginUser, #4416): a
	// masked phone for an IAM id, the name the account goes by until the
	// user gives it one
	AccountUsers map[string]string `json:"accountUsers,omitempty"`
	// where a custom provider's balance is asked (see provider.Balance)
	BalanceURL    string `json:"balanceURL,omitempty"`
	BalancePath   string `json:"balancePath,omitempty"`
	BalanceMethod string `json:"balanceMethod,omitempty"`
	// NoBalanceProbe: the look for a balance query is turned off
	NoBalanceProbe bool `json:"noBalanceProbe,omitempty"`
	// the query magpie found by itself for a relay added by hand with no
	// Balance URL, and its field (provider.BalanceFound)
	BalanceFound      string `json:"balanceFound,omitempty"`
	BalanceFoundField string `json:"balanceFoundField,omitempty"`
	// whether magpie looks for a balance query for it at all (the switch
	// aside), and whether it looked and found none
	BalanceProbe      bool `json:"balanceProbe,omitempty"`
	BalanceUndetected bool `json:"balanceUndetected,omitempty"`
	// its keys may have a balance typed by hand, their vendor telling
	// none (provider.TakesOwnBalance)
	TakesOwnBalance bool   `json:"takesOwnBalance,omitempty"`
	ModelsURL       string `json:"modelsURL,omitempty"`
	// its vendor's list is never asked for, and only the models added by
	// id are served (provider.Provider.ListOff); TakesListOff: a key's
	// provider, where it can be turned off
	ListOff      bool `json:"listOff,omitempty"`
	TakesListOff bool `json:"takesListOff,omitempty"`
	// TakesModelHeaders: a key's provider, whose models can each be given
	// headers of their own (provider.SetModelHeaders); a signed-in
	// account's and a plugin's carry their sign-in's
	TakesModelHeaders bool `json:"takesModelHeaders,omitempty"`
	// DecideAPIKind: a provider whose models can each be asked on its
	// System One URL once it has one beside a conversation URL
	// (provider.DecideAPI); the editor offers System One then
	DecideAPIKind bool `json:"decideAPIKind,omitempty"`
	// an account-wide balance token (provider.BalanceToken): whether the
	// vendor takes one, and whether one is saved; never the token itself
	BalanceToken struct {
		Takes bool `json:"takes"`
		Set   bool `json:"set"`
	} `json:"balanceToken"`
	// a StepFun provider's Step Plan windows, told only to a platform
	// sign-in: which site's, and whether one is kept
	StepPlan *stepPlanJSON `json:"stepPlan,omitempty"`
	// a Zhipu or Z.ai key's team, for a team's GLM Coding Plan (#236):
	// set, if empty, for those providers alone, which the editor asks it of
	ZhipuTeam *provider.ZhipuTeam `json:"zhipuTeam,omitempty"`
	// a Volcengine account's access key or a Tencent Cloud account's
	// SecretId and SecretKey, which its plan's windows are read with
	// (provider.AccessKeyVendor): whether the provider takes one, whose,
	// its ID, and whether a Secret is saved; never the Secret itself
	AccessKey *accessKeyJSON `json:"accessKey,omitempty"`
	// a Google Vertex AI provider's project, location and Google
	// credentials, which it is asked with in place of a key: set for it
	// alone, which the editor asks them of
	Vertex *provider.Vertex `json:"vertex,omitempty"`
	// ModelTest is why its models can't each be sent a test request, ""
	// when they can (provider.ModelTest): the editor says so on a chip's
	// right-click rather than offer no menu
	ModelTest string `json:"modelTest,omitempty"`
	// TestsAs is the agents a model's test can be asked as
	// (provider.TestAs), for a relay that serves only them: the chip's
	// right-click offers each, and says why one can't be when it can't
	TestsAs map[string]string `json:"testsAs,omitempty"`
	// TestPicks is the accounts or keys that are on when there are two
	// or more (provider.TestPicks): the chip's right-click offers a
	// test on one of them
	TestPicks []provider.TestPick `json:"testPicks,omitempty"`
	// DecideTest is set when its decision models can each be sent a
	// System One question (provider.AsksDecideModels): a mixed
	// provider's Jev too, beside its conversation models
	DecideTest bool `json:"decideTest,omitempty"`
	// MarksKinds is set when its models can be said to make images or
	// videos (provider.MarksKinds, #1399): a chip's right-click offers it
	MarksKinds bool `json:"marksKinds,omitempty"`
	// Deciders explicitly identifies decision models for OpenRouter and
	// remote magpie. An empty remote list suppresses name-based guessing.
	Deciders *[]string `json:"deciders,omitempty"`

	Key struct {
		Set      bool   `json:"set"`
		Masked   string `json:"masked"`
		Optional bool   `json:"optional"`
	} `json:"key"`
	// AnonKey is the key Add another key adds when nothing is pasted
	// (provider.AnonymousKey: OpenCode Zen's public, #1795), while the
	// provider doesn't have it yet
	AnonKey string   `json:"anonKey,omitempty"`
	Ready   bool     `json:"ready"`
	Chosen  []string `json:"chosen"` // the user's explicit picks, if any
	// Speaks: the APIs a provider of keys has a URL for, which a model's
	// own API set in the editor is checked against (NoAPI); none for a
	// sign-in, whose models are never left out for it
	Speaks   []string `json:"speaks,omitempty"`
	Fallback []string `json:"fallback"` // where requests go when this one can't take them
	Routing  string   `json:"routing"`  // how requests spread over its keys or accounts
	Affinity string   `json:"affinity"` // how long a conversation stays with who answered it
	// Sink: a key or account rate limited with quota left goes to the
	// back of the order (provider.Provider.Sink)
	Sink bool `json:"sink,omitempty"`
	// UseFirst: Smart puts it ahead of the other providers serving the
	// same model (provider.Provider.UseFirst), set on its own with usefirst
	UseFirst bool `json:"useFirst,omitempty"`
	// NoStandIn: never a fallback for, or a routing group's next after,
	// another provider's model (provider.Provider.NoStandIn), set on its
	// own with standin
	NoStandIn bool `json:"noStandIn,omitempty"`
	// KeepLogin: magpie keeps Codex or Claude Code signed in to the first
	// account rather than moving it on when that runs low (#524)
	KeepLogin bool `json:"keepLogin,omitempty"`
	// KeepLoginAs: the account it is kept signed in to instead of the
	// first, the gateway still trying them in their order
	KeepLoginAs string `json:"keepLoginAs,omitempty"`
	// SwitchOnly: magpie only moves Codex's own sign-in between its
	// accounts, never routing its requests for failover (#1907); on
	// Claude's, the accounts are kept to Claude Code's own sign-in and
	// serve no request through magpie (myduso on Discord)
	SwitchOnly bool `json:"switchOnly,omitempty"`
	// SignInMoves: how magpie moves Codex's or Claude Code's own sign-in
	// between its accounts (provider.SignInMovesOf), said beside them
	SignInMoves *provider.SignInMoves `json:"signInMoves,omitempty"`
	// RestartApp: magpie restarts the Codex app's app-server for an
	// account change it makes by itself (provider.Provider.RestartApp)
	RestartApp bool `json:"restartApp,omitempty"`
	// Self: a Remote magpie that turned out to be this magpie (sync
	// brought it from the computer that uses this one), left out of
	// routing unless KeepSelf (provider.Provider.SelfAnswered)
	Self     bool `json:"self,omitempty"`
	KeepSelf bool `json:"keepSelf,omitempty"`
	// ThisDevice: kept on this computer, out of sync (provider.Provider.ThisDevice)
	ThisDevice bool `json:"thisDevice,omitempty"`
	// LibraryMCP: a Remote magpie whose router is a Library server, or
	// is to be one (library.RemoteMCPOn)
	LibraryMCP bool `json:"libraryMCP,omitempty"`
	// LibraryShared: a Remote magpie whose Library's skills and prompts
	// are used here (library.BorrowedOn), and LibraryNote what its last
	// read said went wrong or was left as the user's (KoiLx0 on X)
	LibraryShared bool   `json:"libraryShared,omitempty"`
	LibraryNote   string `json:"libraryNote,omitempty"`
	// RemoteUpdate: a Remote magpie's word on its last ask to follow
	// this magpie's version (remoteUpdateNote)
	RemoteUpdate *remoteUpdateJSON `json:"remoteUpdate,omitempty"`
	// CapStop and CapCareful: how its accounts end at their caps
	// (plugins#349), set on their own with capstop and capcareful
	CapStop    bool `json:"capStop,omitempty"`
	CapCareful bool `json:"capCareful,omitempty"`
	// how many requests each of its keys or accounts has out at once, the
	// rest queued: the user's (null: not set), and what its plugin says
	// when the user set none (provider.Concurrency)
	MaxConcurrency    *int `json:"maxConcurrency"`
	PluginConcurrency int  `json:"pluginConcurrency,omitempty"`
	// how many of its keys or accounts take requests at once, the rest
	// held back as reserves: the user's (null: not set), and what its
	// plugin says when the user set none (provider.AtOnce; 01huadalang
	// on Discord, ZCode)
	MaxAccounts    *int `json:"maxAccounts"`
	PluginAccounts int  `json:"pluginAccounts,omitempty"`
	// each key's or account's own limit over it (#892), by its name in
	// lower case or its key id; 0 there is none
	AccountConcurrency map[string]int `json:"accountConcurrency,omitempty"`
	// each subscription account's weight under By weight routing, by its
	// name in lower case; one not in it weighs 1 (provider.AccountWeights)
	AccountWeights map[string]int `json:"accountWeights,omitempty"`
	// how many may wait for each key or account, and for how many seconds
	// (0: no bound)
	QueueLimit int `json:"queueLimit,omitempty"`
	QueueWait  int `json:"queueWait,omitempty"`
	// how many requests each key or account sends the vendor in any
	// minute, 0 for no limit (coeo91 on Discord)
	MaxRPM int `json:"maxRPM,omitempty"`
	// RateWindows are the time-of-day windows it bills at its own rate
	// (#2463), as the editor opens on them
	RateWindows []provider.RateWindow `json:"rateWindows,omitempty"`
	// RateNow is what it bills at now, the price rate times the window
	// of the hour (rateNowOf), 0 for 1×: read on this computer's clock,
	// which calls are counted on, not the browser's
	RateNow float64 `json:"rateNow,omitempty"`
	// what it charges against the official price, 0 for that (#819)
	PriceRate float64     `json:"priceRate,omitempty"`
	Models    []modelJSON `json:"models"`            // everything the vendor lists, exposed ones flagged
	Exposed   int         `json:"exposed"`           // how many reach the agents
	Draws     int         `json:"draws,omitempty"`   // how many of its models draw images (gateway.Drawers)
	DrawIDs   []string    `json:"drawIds,omitempty"` // those models' ids, listed apart in its editor
	// the names those are shown by where one isn't its id: a ChatGPT
	// account's gpt-image-2 is its one GPT Image, whichever model its
	// backend draws with (StarBits on X read the id as an old model)
	DrawNames map[string]string `json:"drawNames,omitempty"`
	// its vendor draws with the model it picks, whichever is named: a
	// ChatGPT account's one GPT Image (gateway.VendorPicksDrawer)
	DrawPicks bool `json:"drawPicks,omitempty"`
	// those of them unticked in its editor (provider.HiddenDrawers)
	HiddenDrawers []string `json:"hiddenDrawers,omitempty"`
	// with none listed to chat with, how many embedding and rerank models
	// it lists: served on /v1/embeddings and /v1/rerank, with no pick
	Retrieval int `json:"retrieval,omitempty"`
	// its video models (gateway.Videomakers), listed apart in its editor
	// as its image models are, and those unticked there
	// (provider.HiddenVideomakers)
	FilmIDs           []string `json:"filmIds,omitempty"`
	HiddenVideomakers []string `json:"hiddenVideomakers,omitempty"`
	Unlisted          bool     `json:"unlisted"` // its models serve only through routing groups
	NoModels          bool     `json:"noModels"` // Select none: none of its models served (#1818)
	// Best in family: with nothing picked, only the newest model of each
	// family it lists is served (provider.Provider.BestInFamily)
	BestInFamily bool `json:"bestInFamily"`
	// Groups are the routing groups ("group/<id>") each of its models is
	// in, by model id: what an unlisted one is still used through, and the
	// editor names those in none
	Groups map[string][]string `json:"groups,omitempty"`
	// KeptBy are the routing groups listing alone (Group.Unlists) that
	// keep each of its models out of agents' lists, by model id: its own
	// "Only through routing groups" switched off doesn't bring those back
	KeptBy   map[string][]string `json:"keptBy,omitempty"`
	Off      bool                `json:"off"`                // switched off: kept, but agents get none of its models
	Contexts map[string]int      `json:"contexts,omitempty"` // the windows the user set, "*" for all its models
	Outputs  map[string]int      `json:"outputs,omitempty"`  // the reply limits the user set (provider.OutputsOf)
	Compacts map[string]int      `json:"compacts,omitempty"` // where Codex and Claude Code compact on its models (provider.CompactsOf)
	Fetched  *time.Time          `json:"fetched,omitempty"`  // when the list came from the vendor; the page says how long ago in its language
	// ListError is why a plugin's account has only the plugin's defaults
	// (provider.ListError): the editor says so under its models
	ListError string             `json:"listError,omitempty"`
	Agents    []providerAgent    `json:"agents"` // detected agents, current ones flagged
	Sponsored bool               `json:"sponsored"`
	KeyList   []provider.KeyInfo `json:"keyList"`           // its keys, in the order requests try them
	Account   *accountJSON       `json:"account,omitempty"` // a signed-in agent, see provider.Account
	// Move is where a built-in subscription stands with the community
	// plugin that can run it (provider.Move): set for those that have one
	Move *moveJSON `json:"move,omitempty"`
	// ProxyGone names a saved proxy this provider (or one of its accounts)
	// chooses that isn't on this computer: its requests fail until another
	// is picked, as a missing proxy never means a direct connection.
	ProxyGone string `json:"proxyGone,omitempty"`
	// a subscription account's rests in force, by its user in lower case:
	// its own and each of its models' (maxleebeta on Discord: WorkBuddy's
	// 429 "将在 … 重置" rested a model, credits left, and nothing here said so)
	AccountRests map[string][]provider.KeyRest `json:"accountRests,omitempty"`
}

type moveJSON struct {
	Package string `json:"package"`
	// State is "" (built-in, never moved), "plugin", "back" or "failed";
	// a retired built-in's row is its plugin's, "plugin"
	State string `json:"state"`
	Error string `json:"error,omitempty"`
	// Why is a failed move's reason, which the page says in its language
	Why *provider.MoveWhy `json:"why,omitempty"`
}

// accessKeyJSON is a provider's account access key as the editor sees
// it: whose ("volcengine", "tencent"), the ID, and whether a Secret is
// saved, never the Secret.
type accessKeyJSON struct {
	Vendor    string `json:"vendor"`
	ID        string `json:"id"`
	SecretSet bool   `json:"secretSet"`
}

// presetAccessKey is whose account access key a preset's plans are read
// with ("volcengine", "tencent", "" for none) and, when only some of its
// regions are such a plan (Tencent Cloud's Token Plan 企业版 beside its
// personal plan and pay as you go, #1574), their ids; nil when all are.
func presetAccessKey(pr provider.PresetDef) (string, []string) {
	vendor := provider.AccessKeyVendor(provider.Provider{Chat: pr.Chat, Responses: pr.Responses, Anthropic: pr.Anthropic})
	var ids []string
	for _, r := range pr.Regions {
		if v := provider.AccessKeyVendor(provider.Provider{Chat: r.Chat, Responses: r.Responses, Anthropic: r.Anthropic}); v != "" {
			ids = append(ids, r.ID)
			if vendor == "" {
				vendor = v
			}
		}
	}
	if vendor == "" || len(ids) == len(pr.Regions) {
		return vendor, nil
	}
	return vendor, ids
}

// stepPlanJSON: whether a StepFun provider's own platform sign-in is kept,
// and where and how the user gets one
type stepPlanJSON struct {
	Site        string `json:"site"`
	SignedIn    bool   `json:"signedIn"`
	URL         string `json:"url"`
	Bookmarklet string `json:"bookmarklet"`
}

type accountJSON struct {
	provider.Account
	Agent string `json:"agent"`     // the agent's id
	Name  string `json:"agentName"` // the agent's name, for "from Codex CLI's sign-in"
	Icon  string `json:"agentIcon"`
	// Logins are the agent's accounts magpie remembers, to switch between
	Logins []provider.Login `json:"logins,omitempty"`
	// Builtin is a plugin's provider id, for a plugin beside a built-in
	// subscription (cursor-plugin's cursor): its plan named as the built-in's
	Builtin string `json:"builtin,omitempty"`
	// WSL is the distro Claude Code runs in, for a Windows with none of its own
	WSL string `json:"wsl,omitempty"`
}

// agentNamed is agent.Find among the agents built once for the request
// (provider.Held). Each account's row named its agent, and Find built all
// of them again for it: with the owner's config 58 MB of the 1.4 GB a
// magpie allocated over 20 window polls.
func agentNamed(q string) (*agent.Agent, error) {
	return agent.FindIn(heldAgents(), q)
}

// heldAgents is agent.All, built once while a request holds it: the
// Providers page, the state and the drift each looked at a list of their
// own beside the one accountLabel looks names up in, every agent built
// again (~0.2 MB a poll with real accounts). Shared, so read only.
func heldAgents() []*agent.Agent { return provider.Held("agent.All", agent.All) }

// accountLabel is the name and logo an account is shown with: its agent's,
// the subscription's for one no agent magpie configures, and a plugin's
// provider's for a plugin's sign-in, whose agent is "plugin" (#694 listed a
// removed Qoder as "plugin", with no logo).
func accountLabel(p provider.Provider) (name, icon string) {
	a := p.Account
	if a == nil {
		return p.Name, p.Icon
	}
	name, icon = a.Agent, "generic"
	if a.Agent == "factory" {
		// a Factory subscription is magpie's own sign-in, not Droid's
		name, icon = "Factory", "factory"
	} else if a.Agent == provider.MiMoID {
		// a Xiaomi MiMo account, not MiMo Code (the agent "mimo" also names)
		name, icon = "Xiaomi MiMo", "mimocode"
	} else if a.Agent == provider.ChatGPTAPIID {
		// a ChatGPT plan through OpenAI's API, not Codex's backend
		name, icon = "ChatGPT API", "openai"
	} else if ag, err := agentNamed(a.Agent); err == nil {
		name, icon = ag.Name, ag.Icon
	} else if a.Agent == "cursor" {
		// a Cursor subscription is served by the gateway, not an agent magpie configures
		name, icon = "Cursor CLI", "cursor"
	} else if a.Agent == "kiro" {
		// Kiro's sign-in is magpie's own, kiro-cli's or the Kiro IDE's
		name, icon = "Kiro", "kiro-color"
	} else if a.Agent == "antigravity" {
		name, icon = "Antigravity", "antigravity-color"
	} else if a.Agent == provider.WorkBuddyAIID {
		// WorkBuddy AI, the international build, isn't an agent magpie configures
		name, icon = "WorkBuddy AI", "workbuddy-color"
	} else if a.Agent == provider.CommandCodePlanID {
		// Command Code's CLI keeps the key its sign-in made
		name, icon = "Command Code", "commandcode"
	}
	if !p.IsPlugin() {
		return name, icon
	}
	if pp, ok := provider.PluginOf(p.ID); ok {
		// a plugin's sign-in: named for the provider it signs in to
		name, icon = pp.Name, pluginIcon(pp)
		if name == "" {
			name = p.Name
		}
		if provider.PluginServes(pp.ID) {
			icon = p.Icon // the built-in's, as it was
		}
		return name, icon
	}
	// its plugin not listed now: the provider's own name and logo
	name, icon = p.Name, p.Icon
	if icon == "" {
		icon = "generic"
	}
	return name, icon
}

type providerAgent struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Icon    string `json:"icon"`
	Current bool   `json:"current"` // this agent is on one of this provider's models now
	Model   string `json:"model,omitempty"`
	Group   string `json:"group,omitempty"` // through this routing group, one of whose members it is
	// Hides are this provider's models (by model id) the agent's own model
	// list leaves out, set on the Agents page (provider.ModelOff): the
	// editor's model picks are every agent's, so it says which agent
	// doesn't show one it has ticked (Kylin_Cloud on X)
	Hides []string `json:"hides,omitempty"`
}

type presetJSON struct {
	provider.PresetDef
	Added bool `json:"added"`
	// ZhipuTeam: a key of it may be on a team's GLM Coding Plan, whose
	// organization and project the editor offers to take
	ZhipuTeam bool `json:"zhipuTeam,omitempty"`
	// AccessKey: a plan whose windows are read with the account's access
	// key (Volcengine Ark's, Tencent Cloud's Token Plan 企业版), which the
	// editor offers to take; AccessKeyVendor whose, and AccessKeyRegions
	// the regions it is asked at when not all of them (presetAccessKey)
	AccessKey        bool     `json:"accessKey,omitempty"`
	AccessKeyVendor  string   `json:"accessKeyVendor,omitempty"`
	AccessKeyRegions []string `json:"accessKeyRegions,omitempty"`
	// Vertex: it is Google Vertex AI, whose editor asks for a Google Cloud
	// project and the Google credentials to sign with, not a key
	Vertex bool `json:"vertex,omitempty"`
	// OwnClient: its requests go as a client of the vendor's own
	// (provider.OwnClient), whatever User-Agent the agent gave
	OwnClient bool `json:"ownClient,omitempty"`
	// a partner's tagline by language, and the languages it is listed in
	Notes map[string]string `json:"notes,omitempty"`
	Langs []string          `json:"langs,omitempty"`
	// New: a partner listed since the add sheet last showed the partners,
	// and not added; the add button marks it
	New bool `json:"new,omitempty"`
}

type gatewayJSON struct {
	URL     string   `json:"url"`
	LAN     bool     `json:"lan"`
	LANURLs []string `json:"lanURLs,omitempty"`
	// PublicURL is MAGPIE_PUBLIC_URL when it is among LANURLs: Connect
	// starts there until the user picks an address (#1551)
	PublicURL string `json:"publicURL,omitempty"`
	// PublicKind is gateway.URLKind's of PublicURL, which Connect and
	// the list above it name it by
	PublicKind string `json:"publicKind,omitempty"`
	// LANPublic are those of LANURLs at an internet address, which anyone
	// online reaches, not only this network
	LANPublic []string `json:"lanPublic,omitempty"`
	// Container: LANURLs are a container's own addresses, which other
	// devices, and containers on other Docker networks, can't reach: the
	// page offers the host's it was opened at first (lanShown)
	Container bool `json:"container,omitempty"`
	// ContainerHost: where other containers on this host reach the
	// gateway while magpie runs in one (gateway.ContainerHostURL)
	ContainerHost string `json:"containerHost,omitempty"`
	// LazyCat: a LazyCat app's domain, which only devices with LazyCat's
	// client reach; LazyCatBox: the box's LAN addresses are listed too
	LazyCat    string `json:"lazyCat,omitempty"`
	LazyCatBox bool   `json:"lazyCatBox,omitempty"`
	// OnNetwork: MAGPIE_ADDR puts it beyond loopback (a server, a Docker
	// image), so every caller is shown a gateway key, and LAN is true too:
	// callers from elsewhere, a container's host among them, need one
	OnNetwork bool `json:"onNetwork,omitempty"`
	Running   bool `json:"running"`
	Mine      bool `json:"mine"` // this process serves it
	// Paused: the user paused it (gatewaypause.go), and nothing serves it
	Paused bool `json:"paused,omitempty"`
	Window bool `json:"window"` // the magpie serving it shows its routing
	// Version is another magpie's, serving it, and Older says it is older
	// than this one: agents' requests are then sent as that version sends
	// them, without this one's fixes (#506)
	Version string `json:"version,omitempty"`
	Older   bool   `json:"older,omitempty"`
	// Pid and Serves are the serving magpie's own: which process it is and
	// how it was started, for the Routing page to name the one that is
	// routing (#2606)
	Pid    int            `json:"pid,omitempty"`
	Serves string         `json:"serves,omitempty"`
	Models int            `json:"models"`
	Calls  []gateway.Call `json:"calls"`
	// Lanes are the keys and accounts with requests out or waiting under
	// a limit on requests at once (#892), by provider#keyid or
	// provider@account
	Lanes   map[string]gateway.Lane `json:"lanes,omitempty"`
	Groups  []gwGroupJSON           `json:"groups"`  // the catalog's routing groups, listed before the models
	Archive archiveJSON             `json:"archive"` // the request archive's switch, and where it goes
}

// gwGroupJSON is a routing group as the Gateway view lists it.
type gwGroupJSON struct {
	ID        string   `json:"id"` // group/<id>, what a request names
	Name      string   `json:"name"`
	Icons     []string `json:"icons"`     // its providers', one each
	Providers []string `json:"providers"` // their names, in the group's order
	// what agents are told of it, as of a model: its reasoning levels,
	// whether it takes images (every member does) and the context and
	// output its members all hold
	Efforts []string `json:"efforts,omitempty"`
	Images  bool     `json:"images,omitempty"`
	Context int      `json:"context,omitempty"`
	Output  int      `json:"output,omitempty"`
	Members []string `json:"members,omitempty"` // the models it sends to, provider/model, in its order
}

type excludedJSON struct {
	provider.Exclusion
	Name string `json:"agentName"`
	Icon string `json:"agentIcon"`
}

type providersJSON struct {
	Providers []providerJSON `json:"providers"`
	Presets   []presetJSON   `json:"presets"`
	Excluded  []excludedJSON `json:"excluded"` // sign-ins magpie found but will not share
	Gateway   gatewayJSON    `json:"gateway"`
	// CodexDaemon is the account Codex's background app-server is still
	// signed in to after Codex was switched to another; "" when none is
	// left behind (provider.CodexDaemonStale).
	CodexDaemon string `json:"codexDaemon,omitempty"`
	// CodexApp is the account the Codex desktop app is still signed in to
	// after Codex was switched to another, until it is quit and opened
	// again (provider.CodexAppStale)
	CodexApp string `json:"codexApp,omitempty"`
	// CodexProjects says the open Codex app shows fewer projects than
	// Codex's accounts have, after a switch (provider.CodexAppProjectsShort)
	CodexProjects bool `json:"codexProjects,omitempty"`
	// Plugins are the providers the plugins sign in to, for the add sheet
	Plugins []pluginSubJSON `json:"plugins"`
	// OnPlugins are the built-in subscriptions moved onto their plugins,
	// which the add sheet offers as the plugin's alone
	OnPlugins []string `json:"onPlugins,omitempty"`
	// Movable are the built-in subscriptions a community plugin can run,
	// deprecated in magpie itself: the add sheet marks them so
	Movable []string `json:"movable,omitempty"`
	// MovesTo is the plugin each of them goes to, for the add sheet's
	// offer to install it before signing in
	MovesTo map[string]string `json:"movesTo,omitempty"`
	// Unmoved are the retired built-ins whose accounts haven't moved onto
	// their plugins yet, served by nothing until they do, with why
	Unmoved []provider.MoveCandidate `json:"unmoved,omitempty"`
	// Moved is the agents the change moved off models it stopped serving
	// (agent.Reseat), for the page to say so.
	Moved []agent.Move `json:"moved,omitempty"`
	// Added and Had: of the keys pasted at once (keys/import), how many
	// were new and how many the provider had already.
	Added int `json:"added,omitempty"`
	Had   int `json:"had,omitempty"`
	// Removed: of the keys removed at once (keys/remove-many), how many.
	Removed int `json:"removed,omitempty"`
	// FileError is why providers.json can't be read (provider.FileError):
	// the page says so over what is listed, which is then the signed-in
	// accounts alone, never "add your first provider".
	FileError string `json:"fileError,omitempty"`
	// Fetching: accounts' lists are still being asked for
	// (provider.FetchingNew); the page asks again until they are in
	Fetching bool `json:"fetching,omitempty"`
}

// agentModel is the model an agent is on, as magpie's catalog names it.
func agentModel(a *agent.Agent) string {
	if len(a.Fields) == 0 {
		return ""
	}
	return strings.TrimPrefix(a.Fields[0].Get(), "magpie/")
}

// agentUse is what an agent is on now: read once for the page, not again
// for each provider (reading it may ask the agent itself, as Alma's does).
type agentUse struct {
	*agent.Agent
	pid, model string
	group      provider.Group
	members    []provider.Member
	inGroup    bool
	// off is the agent's own model list's picks (provider.ModelOff), nil
	// when it leaves nothing out, or another agent sharing its list (a WSL
	// one, an omp profile) says it already
	off func(id string) bool
}

func agentUses(agents []*agent.Agent, findGroup func(string) (provider.Group, []provider.Member, bool)) []agentUse {
	out := make([]agentUse, 0, len(agents))
	lists := map[string]bool{}
	for _, a := range agents {
		u := agentUse{Agent: a}
		if id := a.ListsFor(); !lists[id] {
			lists[id] = true
			if _, only := provider.PickedModels(id); only || len(provider.HiddenModels(id)) > 0 {
				u.off = provider.ModelOff(id)
			}
		}
		v := agentModel(a)
		if provider.IsGroupRef(v) {
			u.group, u.members, u.inGroup = findGroup(v)
		} else if pid, model, ok := strings.Cut(v, "/"); ok {
			if _, err := provider.Find(pid); err == nil {
				u.pid, u.model = pid, model
			}
		}
		out = append(out, u)
	}
	return out
}

// groupsOnce is find with each group's members found once: every agent
// on a group, and the gateway's list after them, asked for the same
// group's again, a Member over a kilobyte each (8 MB of forty polls of
// the Providers page with real accounts). The page only reads them.
func groupsOnce(find func(string) (provider.Group, []provider.Member, bool)) func(string) (provider.Group, []provider.Member, bool) {
	type found struct {
		g  provider.Group
		ms []provider.Member
		ok bool
	}
	seen := map[string]found{}
	return func(id string) (provider.Group, []provider.Member, bool) {
		if f, ok := seen[id]; ok {
			return f.g, f.ms, f.ok
		}
		g, ms, ok := find(id)
		seen[id] = found{g, ms, ok}
		return g, ms, ok
	}
}

// presetOwnClient says a provider added from pr goes as a client of its
// vendor's own (provider.OwnClient): OpenCode's, Cline's, Kilo's, another
// magpie.
func presetOwnClient(pr provider.PresetDef) bool {
	return provider.Provider{Preset: pr.ID, Chat: pr.Chat, Responses: pr.Responses, Anthropic: pr.Anthropic}.OwnClient()
}

// desktopNumbers are the numbers Claude Desktop's menu says beside the
// models it names after its own catalog (gateway.DesktopNumber), by
// "provider/model": none when Claude Desktop isn't installed, and only for
// the models it is shown (provider.CatalogFor), so no other chip carries
// a number nothing shows (#2578).
func desktopNumbers(agents []agentUse) map[string]string {
	if !slices.ContainsFunc(agents, func(u agentUse) bool { return u.Agent != nil && u.ID == "claude-desktop" }) {
		return nil
	}
	shown, _ := provider.CatalogFor("claude-desktop")
	out := map[string]string{}
	for _, e := range shown {
		if n := gateway.DesktopNumber(e); n != "" {
			out[e.ID] = n
		}
	}
	return out
}

func providerInfo(p provider.Provider, agents []agentUse) providerJSON {
	return providerInfoWith(p, p.Listing(), agents, desktopNumbers(agents))
}

// providerInfoWith is providerInfo with the Claude Desktop numbers read
// once for the whole list (desktopNumbers), and the provider's models
// given: the vendor's list read once for the picks, what is exposed and
// the newest of each family, as each read builds it whole.
func providerInfoWith(p provider.Provider, ls provider.Listing, agents []agentUse, desktop map[string]string) providerJSON {
	avail := ls.Available
	out := providerJSON{
		ID: p.ID, Name: p.Name, Icon: p.Icon, IconFrom: p.IconFrom, Preset: p.Preset, Host: p.Host(),
		Chat: p.Chat, Responses: p.Responses, Anthropic: p.Anthropic, Gemini: p.Gemini, Decide: p.Decide, BaseAPI: p.BaseAPI, ModelTest: p.ModelTest(), TestsAs: p.TestClients(), TestPicks: p.TestPicks(), DecideTest: p.AsksDecideModels(), MarksKinds: p.MarksKinds(),
		Catalog: p.Catalog, Website: p.Website, KeysURL: p.KeysURL,
		Proxy: p.Proxy, AccountProxies: p.AccountProxies, AccountModels: p.AccountModels, AccountCaps: p.AccountCaps, AccountWindowCaps: p.AccountWindowCaps, AccountWindowAmounts: p.AccountWindowAmounts, AccountNames: p.AccountNames, Headers: p.Headers, ExtraBody: p.ExtraBody, ReplyHeaders: p.ReplyHeaders, PassHeaders: p.PassHeaders, Searches: p.Searches, Cline: p.ClinePinnable(), PinUpstream: p.PinUpstream, Unredacted: p.Unredacted, CountFree: p.CountFree, Billing: p.Billing, BillingKind: p.BillingKind(), InsecureTLS: p.InsecureTLS, TakesInsecureTLS: p.TakesInsecureTLS(), HTTP1: p.HTTP1, ClientCert: tilde(p.ClientCert), ClientKey: tilde(p.ClientKey), ServerCA: tilde(p.ServerCA), TakesClientCert: p.TakesClientCert(), ServerPin: p.ServerPin, TakesServerPin: p.TakesServerPin(), Socket: p.Socket, TakesSocket: p.TakesSocket(), LimitRestOff: p.LimitRestOff, UpstreamProviders: p.UpstreamProviders, ImageFilesOff: p.ImageFilesOff, TakesImageFiles: p.TakesImageFiles(), NativeCompaction: p.NativeCompaction, CarriesCompaction: p.CarriesCompaction(), PassUserAgent: !p.MagpieUserAgent, OwnClient: p.OwnClient(), PassErrors: p.PassErrors, TakesPassErrors: p.TakesPassErrors(), AuthHeader: p.AuthHeader, BothAuth: p.BothAuth(), BalanceURL: p.BalanceURL, BalancePath: p.BalancePath, BalanceMethod: p.BalanceMethod, NoBalanceProbe: p.NoBalanceProbe, ModelsURL: p.ModelsURL, ListOff: p.ListsOff(), TakesListOff: p.Account == nil && !p.IsPlugin() && !p.DecideOnly(), TakesModelHeaders: p.Account == nil, DecideAPIKind: p.DecideAPIKind(),
		Ready: p.Ready(), Chosen: p.PicksOf(avail), Agents: make([]providerAgent, 0, len(agents)),
		Fallback: p.Fallback, Routing: p.Routing, Sink: p.Sink, UseFirst: p.UseFirst, NoStandIn: p.NoStandIn, Affinity: p.Affinity, KeepLogin: p.KeepLogin, KeepLoginAs: p.KeepLoginAs, SwitchOnly: p.SwitchOnly, RestartApp: p.RestartApp, Self: p.SelfAnswered(), KeepSelf: p.KeepSelf, ThisDevice: p.ThisDevice, CapStop: p.CapStop, CapCareful: p.CapCareful, Unlisted: p.Unlisted, NoModels: p.ServesNone(), BestInFamily: p.BestInFamily && len(p.Models) == 0 && !p.NoModels, Off: p.Off, Contexts: p.Contexts,
		MaxConcurrency: p.MaxConcurrency, PluginConcurrency: p.PluginConcurrency(), MaxAccounts: p.MaxAccounts, PluginAccounts: p.PluginAtOnce(), PriceRate: p.PriceRate,
		RateWindows: p.RateWindows, RateNow: rateNowOf(p, time.Now()),
		AccountConcurrency: p.AccountConcurrency, AccountWeights: p.AccountWeights, QueueLimit: p.QueueLimit, QueueWait: p.QueueWait, MaxRPM: p.MaxRPM,
		Outputs: provider.OutputsOf(p.ID), Compacts: provider.CompactsOf(p.ID),
	}
	if p.Account != nil && p.Account.Agent == p.ID {
		out.SignInMoves = provider.SignInMovesOf(p.ID)
	}
	if p.Account != nil {
		for _, r := range p.AccountRefs() {
			if s := provider.SeatNameOf(p.ID, r); s != "" {
				if out.AccountSeats == nil {
					out.AccountSeats = map[string]string{}
				}
				out.AccountSeats[strings.ToLower(strings.TrimSpace(r))] = s
			}
			if u := p.PluginUser(r); u != "" {
				if out.AccountUsers == nil {
					out.AccountUsers = map[string]string{}
				}
				out.AccountUsers[strings.ToLower(strings.TrimSpace(r))] = u
			}
		}
	}
	if p.BalanceURL == "" {
		out.BalanceFound, out.BalanceFoundField, _ = provider.BalanceFound(p)
		out.BalanceProbe, out.BalanceUndetected = provider.BalanceProbeState(p)
	}
	if out.Fallback == nil {
		out.Fallback = []string{}
	}
	if p.Account == nil {
		for _, s := range p.Speaks() {
			out.Speaks = append(out.Speaks, string(s))
		}
	}
	if out.Chosen == nil {
		out.Chosen = []string{}
	}
	if pr := provider.Preset(p.Preset); pr != nil {
		out.Sponsored = pr.Sponsored
		out.Key.Optional = pr.NoKey
	}
	out.BalanceToken.Takes, out.BalanceToken.Set = provider.TakesBalanceToken(p), p.BalanceToken != ""
	if provider.TakesZhipuTeam(p) {
		out.ZhipuTeam = &provider.ZhipuTeam{}
		if p.ZhipuTeam != nil {
			*out.ZhipuTeam = *p.ZhipuTeam
		}
	}
	// a provider of a preset with such a plan among its regions takes one
	// wherever it is, the editor showing it at those regions; a custom
	// provider none until it has one (provider.AsksAccessKey, ysm119)
	vendor := provider.AsksAccessKey(p)
	if pr := provider.Preset(p.Preset); vendor == "" && pr != nil {
		vendor, _ = presetAccessKey(*pr)
	}
	if vendor != "" || p.AccessKeyID != "" || p.SecretAccessKey != "" {
		out.AccessKey = &accessKeyJSON{Vendor: vendor, ID: p.AccessKeyID, SecretSet: p.SecretAccessKey != ""}
	}
	if p.IsVertex() {
		out.Vertex = &provider.Vertex{}
		if p.Vertex != nil {
			*out.Vertex = *p.Vertex
		}
	}
	if site := provider.StepFunSite(p); site != "" {
		out.StepPlan = &stepPlanJSON{site, provider.StepFunSignedIn(p.ID), provider.StepFunSignInURL(site), provider.StepFunBookmarklet()}
	}
	out.Key.Set = p.Key != ""
	out.Key.Masked = provider.Mask(p.Key)
	out.ProxyGone = provider.ProxyGone(p)
	if provider.BuiltinRetired(p.ID) {
		// only its plugin's row is listed now: built-in accounts not moved
		// yet are providersJSON.Unmoved's
		out.Move = &moveJSON{Package: provider.MovePackage(p.ID), State: provider.MovePlugin}
	} else if provider.Movable(p.ID) {
		m, _ := provider.MigrationOf(p.ID)
		out.Move = &moveJSON{Package: provider.MovePackage(p.ID), State: m.State, Error: m.Err, Why: m.Why}
		if m.State == provider.MoveMoving {
			out.Move.State = ""
		}
	}
	if anon := p.AnonymousKey(); anon != "" && p.Account == nil && !p.HasKey(anon) {
		out.AnonKey = anon
	}
	out.KeyList = p.KeyList()
	if out.KeyList == nil {
		out.KeyList = []provider.KeyInfo{}
	}
	// a key the gateway passes over after a failure says so on its row
	restKey := p.RestKeys()
	for i, k := range out.KeyList {
		if !k.On {
			continue
		}
		key := restKey(k)
		if r, ok := keyRestOf(key); ok {
			out.KeyList[i].Rest = &provider.KeyRest{Why: r.Why, Status: r.Status, Until: r.Until, Key: key, Probed: r.Probed, Rate: r.Rate}
		}
	}
	// and one with a spend cap, what it has spent of it (the gateway
	// holds it at the cap by the same count, gateway.spendHeld)
	for i, k := range out.KeyList {
		sc, ok := p.KeySpendCap(k.ID)
		if !ok {
			continue
		}
		start, reset := sc.Window(time.Now())
		ks := &provider.KeySpend{SpendCap: sc, Spent: usage.KeySpent(k.ID, start)}
		ks.Held = ks.Spent >= sc.USD
		if !reset.IsZero() {
			ks.Reset = &reset
		}
		out.KeyList[i].Spend = ks
	}
	// a key whose windows magpie reads takes a usage cap as an account
	// does, and says when the cap holds it (the gateway holds it by the
	// same reading, gateway.keyCapHeld)
	if p.TakesUsageCap() && p.Account == nil {
		now := time.Now()
		for i, k := range out.KeyList {
			out.KeyList[i].UsageCap = true
			ka, ok := p.KeyAccountOf(k.ID)
			if !ok {
				continue
			}
			if h := p.KeyCapHeld(ka, "", now, provider.KeyAllowance); h != nil {
				out.KeyList[i].CapHeld = &provider.KeyCapState{Used: h.Used, Cap: h.Cap, Back: h.Back}
			}
		}
	}
	// and one with a balance typed, what is left of it (the card on the
	// Usage page shows the same, provider.ownBalanceCard)
	out.TakesOwnBalance = p.TakesOwnBalance()
	for i, k := range out.KeyList {
		if e, ok := p.OwnBalanceEstimate(k.ID); ok {
			out.KeyList[i].Balance = &e
		}
	}
	if !out.Key.Set && p.Ready() {
		out.Key.Optional = true
	}
	for id, gs := range provider.MemberGroups() {
		if m, ok := strings.CutPrefix(id, p.ID+"/"); ok {
			if out.Groups == nil {
				out.Groups = map[string][]string{}
			}
			out.Groups[m] = gs
		}
	}
	for id, gs := range provider.GroupKept() {
		if m, ok := strings.CutPrefix(id, p.ID+"/"); ok {
			if out.KeptBy == nil {
				out.KeptBy = map[string][]string{}
			}
			out.KeptBy[m] = gs
		}
	}
	if a := p.Account; a != nil {
		out.Account = &accountJSON{Account: *a, Agent: a.Agent}
		out.Account.Name, out.Account.Icon = accountLabel(p)
		out.Account.Logins = provider.Logins(a.Agent)
		if a.Agent == "claude" {
			out.Account.WSL = provider.ClaudeInWSL()
		}
		if pp, ok := provider.PluginOf(p.ID); ok && p.IsPlugin() {
			// a plugin's sign-in: the page follows it by the provider's id
			out.Account.Agent, out.Account.Builtin = p.ID, pp.ID
			out.Account.Logins = provider.Logins(p.ID)
			if out.Icon == "" || out.Icon == "generic" {
				out.Icon = out.Account.Icon
			}
		}
	}
	if p.Account != nil {
		out.AccountRests = accountRests(p.ID)
	}
	exposed := map[string]bool{}
	exposedList := ls.Exposed
	for _, m := range exposedList {
		exposed[m.ID] = true
	}
	newest := map[string]bool{}
	for _, m := range ls.Newest {
		newest[m.ID] = true
	}
	names, kept := p.ModelNames(), p.ModelEfforts()
	// only read, so the settings every look shares: a copy of its own for
	// each provider was 16 MB of what twenty window polls allocated
	held := settings.Shared()
	sames := held.ModelSameAs
	// a list fetched before magpie kept each model's most: the one Codex
	// CLI keeps says it
	var most []catalog.Model
	if p.Account != nil && p.Account.Agent == "codex" {
		most = catalog.Codex()
	}
	// read once for the list: read for each model, three times over, it
	// was most of the Providers page's wait with many models (lml on
	// Discord, Windows)
	set := held
	noAPI := p.NoAPI()
	ultraModels := sync.OnceValue(provider.UltraModels)
	// the provider's catalogs worked out once, and each model priced once
	// for its list price and whether it is free
	pricer := p.Pricer()
	// each model's provider/model ref, made once: the settings' maps and
	// every agent's hidden list are looked up by it
	var refs []string
	named := func(m catalog.Model, on bool) modelJSON {
		ref := p.ID + "/" + m.ID
		refs = append(refs, ref)
		images := m.Images || catalog.SeesImages(m.ID)
		if m.ImageInput != nil {
			images = *m.ImageInput
		}
		own := images
		said, imageSet := provider.ImageOverrideIn(set, p.ID, m.ID)
		if imageSet {
			images = said
		}
		window := p.WindowIn(m, set)
		j := modelJSON{ID: m.ID, Name: m.Name, Efforts: provider.EffortsOf(m), On: on, Context: window, Output: provider.OutputWithin(window, p.ReplyLimitIn(m, set)), Listed: provider.ListedWindow(m), ListedOutput: provider.OutputWithin(provider.ListedWindow(m), p.ReplyLimitIn(m, settings.Settings{})), Max: m.MaxContext, Free: m.Free, Rate: m.Rate, RateWas: m.RateWas, Images: images, ImageSet: imageSet, Own: own, Best: newest[m.ID]}
		list, listed, free := pricer.Prices(set, m.ID)
		j.Zero = !m.Free && free
		// a plugin reports a model's load by its own id, not one the user
		// lists it by (axelburks on Discord)
		ownID := p.OwnModelID(m.ID)
		if l, _, ok := plugin.ModelLoad(p.PluginProvider(), ownID, time.Now()); ok {
			j.Load = &l
		}
		if i := slices.IndexFunc(most, func(c catalog.Model) bool { return c.ID == m.ID }); j.Max == 0 && i >= 0 {
			j.Max = most[i].MaxContext
		}
		if n, ok := names[m.ID]; ok {
			j.Default = cmp.Or(m.Name, m.ID)
			j.Name = n
		} else {
			j.Plain = m.Plain
		}
		if p.IsRemoteMagpie() {
			j.By = m.By
		}
		if api, ok := p.ModelAPI(m.ID); ok {
			j.API = string(api)
		}
		for _, a := range p.ListedAPIs(m.ID) {
			if slices.Contains(provider.Protocols, a) {
				j.Auto = append(j.Auto, string(a))
			}
		}
		j.NoAPI = noAPI(m.ID)
		j.Same = sames[ref]
		if ownID != m.ID {
			j.Vendor = ownID
		}
		j.IDs = p.TakesModelIDs()
		j.Kind = p.ModelKind(m.ID)
		if p.Account == nil {
			j.Headers = held.ModelHeaders[ref]
		}
		j.System = held.ModelSystemPrompts[ref]
		if j.CanFast = provider.CanFast(p, m.ID); j.CanFast {
			j.Tier = held.ModelTiers[ref]
			j.CanUltra = provider.UltraOn(p, m.ID, ultraModels())
		}
		// a value the page reads as its default isn't sent: the window and
		// reply limit the vendor listed are the ones in use unless the user
		// set their own, and most models merge by their own id
		if j.Merge = provider.MergeName(m.ID); j.Merge == m.ID {
			j.Merge = ""
		}
		if j.Listed == j.Context {
			j.Listed = 0
		}
		if j.ListedOutput == j.Output {
			j.ListedOutput = 0
		}
		j.Desktop = desktop[ref]
		if mp, ok := held.ModelPrices[ref]; ok {
			if pr, bad := mp.Price(); bad == "" {
				j.Price = &pr
			}
		}
		if listed {
			j.List, j.RemoteList = &list, p.RemotePriced(m.ID)
		} else if pr, ok := provider.MakerPrice(m.ID); ok {
			j.List = &pr
		}
		if j.List != nil {
			// an off-peak price, doubled at peak hours: the editor says so
			if k := p.PeakOf(m.ID); k != "" {
				j.List.Peak = k
			}
		} else if pr, ok := provider.SamePriceIn(held, p.ID, m.ID); ok {
			j.List = &pr // the model it is the same as, beside it (#2344)
		}
		if len(j.Efforts) == 0 {
			j.Efforts, j.Given = provider.Levels, true
		}
		if k, ok := kept[m.ID]; ok {
			j.Kept = slices.DeleteFunc(slices.Clone(j.Efforts), func(e string) bool { return !slices.Contains(k, e) })
		}
		if provider.Reasonless(p.ID, m.ID) {
			// none of its levels is kept: each is there to tick again
			j.Reasonless, j.Kept = true, nil
		}
		return j
	}
	// the rows counted before any is made, so the list is made once at its
	// size: grown by append for the decision models and the picks it
	// lacked, and again to put those picks in front, it was 8 MB of what
	// twenty polls of the Providers page allocated with real accounts
	seen := make(map[string]bool, len(avail))
	for _, m := range avail {
		seen[m.ID] = true
	}
	// Keep an explicit empty list for remote magpie: the editor must not
	// guess that a Jev Router chat model answers System One.
	var deciders []string
	if p.IsRemoteMagpie() {
		deciders = []string{}
	}
	var unlisted []catalog.Model // decision models the vendor list lacks
	for _, m := range p.DecisionModels() {
		deciders = append(deciders, m.ID)
		if !seen[m.ID] {
			seen[m.ID] = true
			unlisted = append(unlisted, m)
		}
	}
	// picks the vendor list does not know go first, so they are visible,
	// the last one picked foremost
	// counted first: a catalog.Model is large, and growing the list by
	// append for a provider whose picks the vendor list lacks was 6 MB of
	// what forty polls of the Providers page allocated with real accounts
	nFront := 0
	for _, m := range exposedList {
		if !seen[m.ID] {
			nFront++
		}
	}
	var front []catalog.Model
	if nFront > 0 {
		front = make([]catalog.Model, 0, nFront)
	}
	for _, m := range exposedList {
		if !seen[m.ID] {
			front = append(front, m)
			// one picked by hand that is a decision model all the same
			// (OpenRouter's inception/mercury-decide:free, coeo91 on
			// Discord) is tested and marked as one in the editor
			if deciders != nil && p.DecidesModel(m.ID) {
				deciders = append(deciders, m.ID)
			}
		}
	}
	out.Models = make([]modelJSON, 0, len(front)+len(avail)+len(unlisted))
	refs = make([]string, 0, cap(out.Models))
	for i := len(front) - 1; i >= 0; i-- {
		out.Models = append(out.Models, named(front[i], true))
	}
	for _, m := range avail {
		out.Models = append(out.Models, named(m, exposed[m.ID]))
	}
	for _, m := range unlisted {
		out.Models = append(out.Models, named(m, exposed[m.ID]))
	}
	if deciders != nil {
		out.Deciders = &deciders
	}
	out.Exposed = len(exposed)
	if len(out.Models) == 0 {
		// a vendor of embedding and rerank models alone (heatonz)
		for _, m := range append(catalog.LiveServing(p.ID, ""), p.PlanRetrieval("")...) {
			if m.Serves == catalog.ServesEmbedding || m.Serves == catalog.ServesRerank {
				out.Retrieval++
			}
		}
	}
	// its image models aren't among those agents chat with; the editor
	// lists them apart, as Settings → Models → Images is where one is picked
	for _, m := range gateway.DrawersOf(p, func() []catalog.Model { return ls.Available }) {
		out.DrawIDs = append(out.DrawIDs, m.ID)
		if m.Name != "" && m.Name != m.ID {
			if out.DrawNames == nil {
				out.DrawNames = map[string]string{}
			}
			out.DrawNames[m.ID] = m.Name
		}
	}
	out.Draws = len(out.DrawIDs)
	out.DrawPicks = out.Draws > 0 && gateway.VendorPicksDrawer(p)
	for _, id := range out.DrawIDs {
		if p.DrawerHidden(id) {
			out.HiddenDrawers = append(out.HiddenDrawers, id)
		}
	}
	// its video models likewise (axelburks on Discord: an Ark key's
	// Seedance had nowhere to be ticked)
	for _, m := range gateway.VideomakersOf(p, func() []catalog.Model { return ls.Available }) {
		out.FilmIDs = append(out.FilmIDs, m.ID)
		if p.VideomakerHidden(m.ID) {
			out.HiddenVideomakers = append(out.HiddenVideomakers, m.ID)
		}
	}
	if t, ok := p.Listed(); ok {
		out.Fetched = &t
	}
	out.ListError = p.ListError()
	for _, a := range agents {
		pa := providerAgent{ID: a.ID, Name: a.Name, Icon: a.Icon, Current: a.pid == p.ID, Model: a.model}
		if a.inGroup {
			for _, m := range a.members {
				if m.Provider.ID == p.ID {
					pa.Current, pa.Model, pa.Group = true, m.Model, a.group.Name
					break
				}
			}
		}
		if a.off != nil {
			for i, m := range out.Models {
				if a.off(refs[i]) {
					pa.Hides = append(pa.Hides, m.ID)
				}
			}
		}
		out.Agents = append(out.Agents, pa)
	}
	return out
}

// keyRestOf is gateway.RestOf, swapped in tests.
var keyRestOf = gateway.RestOf

// restsUnder is gateway.RestsUnder, swapped in tests.
var restsUnder = gateway.RestsUnder

// accountRests are the rests in force of a subscription's accounts, by
// user in lower case: an account's own (provider@user) and each of its
// models' (provider@user/model), as the gateway keys them (restKey,
// restID); nil when none rests.
func accountRests(id string) map[string][]provider.KeyRest {
	var out map[string][]provider.KeyRest
	for _, r := range restsUnder(id + "@") {
		user, model, _ := strings.Cut(strings.TrimPrefix(r.Key, id+"@"), "/")
		if user == "" {
			continue
		}
		if out == nil {
			out = map[string][]provider.KeyRest{}
		}
		out[user] = append(out[user], provider.KeyRest{Why: r.Why, Status: r.Status, Until: r.Until, Key: r.Key, Probed: r.Probed, By: r.By, Model: cmp.Or(r.Model, model), Error: r.Error, Rate: r.Rate})
	}
	return out
}

func providersState() providersJSON {
	// an account signed in since start-up is listed with its vendor's
	// models, not magpie's own list of them (#204): asked behind the page,
	// which is told so and asks again, never waited for (#541)
	provider.FetchNewBehind(8 * time.Second)
	agents := agent.DetectedIn(heldAgents())
	s := providersJSON{Providers: []providerJSON{}, Presets: []presetJSON{}, Excluded: []excludedJSON{}}
	s.OnPlugins = provider.OnPlugins()
	s.Movable = provider.MovableIDs()
	// a retired built-in whose plugin is installed is the plugin's, moved
	// or not: the add sheet signs in to it through the plugin alone
	for _, id := range s.Movable {
		if _, ok := provider.PluginOf(id); ok && provider.BuiltinRetired(id) && !slices.Contains(s.OnPlugins, id) {
			s.OnPlugins = append(s.OnPlugins, id)
		}
	}
	for _, id := range s.Movable {
		if s.MovesTo == nil {
			s.MovesTo = map[string]string{}
		}
		s.MovesTo[id] = provider.MovePackage(id)
	}
	for _, c := range provider.MoveCandidates() {
		if provider.BuiltinRetired(c.ID) {
			s.Unmoved = append(s.Unmoved, c)
		}
	}
	if err := provider.FileError(); err != nil {
		s.FileError = err.Error()
	}
	hidden := map[string]provider.Provider{}
	for _, p := range provider.Hidden() {
		hidden[p.ID] = p
	}
	for _, x := range provider.Excluded() {
		e := excludedJSON{Exclusion: x, Name: x.Agent, Icon: "generic"}
		if p, ok := hidden[x.Provider]; ok && x.Provider != "" {
			// a removed account as its row was named: a plugin's by the
			// provider it signs in to, not its agent "plugin" (#694)
			e.Name, e.Icon = accountLabel(p)
		} else if a, err := agentNamed(x.Agent); err == nil {
			e.Name, e.Icon = a.Name, a.Icon
		}
		s.Excluded = append(s.Excluded, e)
	}
	findGroup := groupsOnce(provider.GroupFinder())
	uses := agentUses(agents, findGroup)
	desktop := desktopNumbers(uses)
	have := map[string]bool{}
	var remoteMCP map[string]bool // read once, at the first Remote magpie
	all := provider.All()
	// both lists made once at their size: each grown by append was 6 MB of
	// what forty polls of the Providers page allocated with real accounts
	s.Providers = make([]providerJSON, 0, len(all))
	partners, presets := provider.Partners(), provider.Presets()
	s.Presets = make([]presetJSON, 0, len(partners)+len(presets))
	for _, p := range all {
		// a preset is added once any provider is its, whatever its id
		have[p.ID], have[p.Preset] = true, true
		info := providerInfoWith(p, provider.KeptListing(p), uses, desktop)
		if p.IsRemoteMagpie() {
			if remoteMCP == nil {
				remoteMCP = library.RemoteMCPOn()
			}
			info.LibraryMCP = remoteMCP[p.ID]
			info.LibraryShared = library.BorrowedOn()[p.ID]
			info.LibraryNote = library.BorrowNote(p.ID)
			info.RemoteUpdate = remoteUpdateNote(p.ID, time.Now())
		}
		s.Providers = append(s.Providers, info)
	}
	// partners first, as the add sheet lists them
	for _, pa := range partners {
		s.Presets = append(s.Presets, presetJSON{PresetDef: pa.PresetDef, Added: have[pa.ID], Notes: pa.Notes, Langs: pa.Langs, New: !have[pa.ID] && !provider.PartnerNoticed(pa.ID), OwnClient: presetOwnClient(pa.PresetDef)})
	}
	for _, pr := range presets {
		bases := provider.Provider{Chat: pr.Chat, Responses: pr.Responses, Anthropic: pr.Anthropic}
		team := provider.TakesZhipuTeam(bases)
		vendor, keyRegions := presetAccessKey(pr)
		s.Presets = append(s.Presets, presetJSON{PresetDef: pr, Added: have[pr.ID], ZhipuTeam: team, AccessKey: vendor != "", AccessKeyVendor: vendor, AccessKeyRegions: keyRegions, Vertex: pr.ID == provider.VertexPreset, OwnClient: presetOwnClient(pr)})
	}
	cat, models := provider.CatalogGroups()
	s.Gateway = gatewayJSON{URL: gateway.URL(), OnNetwork: gateway.OnNetwork(), Models: models, Calls: []gateway.Call{}, Groups: []gwGroupJSON{}}
	s.Gateway.LAN = settings.Load().LAN || s.Gateway.OnNetwork
	if s.Gateway.LAN {
		s.Gateway.LANURLs = gateway.LANURLs()
		s.Gateway.LANPublic = gateway.PublicURLs(s.Gateway.LANURLs)
		s.Gateway.Container, s.Gateway.ContainerHost = gateway.ContainerAddrs(), gateway.ContainerHostURL()
		s.Gateway.LazyCat, s.Gateway.LazyCatBox = gateway.LazyCatURL(), gateway.LazyCatBox()
		if u := gateway.PublicURL(); slices.Contains(s.Gateway.LANURLs, u) {
			s.Gateway.PublicURL, s.Gateway.PublicKind = u, gateway.URLKind(u)
		}
	}
	for _, e := range cat {
		if e.Group == "" {
			continue
		}
		// its lists are [] when empty, never null, which the page can't
		// read as a list (#1585)
		g := gwGroupJSON{ID: e.ID, Name: e.Name, Icons: append([]string{}, e.Icons...), Providers: []string{}, Efforts: e.Efforts, Images: e.Images, Context: e.Context, Output: e.PublishedOutput()}
		if _, ms, ok := findGroup(e.ID); ok {
			for _, m := range ms {
				if id := m.Provider.ID + "/" + m.Model; !slices.Contains(g.Members, id) {
					g.Members = append(g.Members, id)
				}
				if !slices.Contains(g.Providers, m.Provider.Name) {
					g.Providers = append(g.Providers, m.Provider.Name)
				}
			}
		}
		s.Gateway.Groups = append(s.Gateway.Groups, g)
	}
	s.Gateway.Paused = pausedNow()
	if gw := served.Load(); gw != nil {
		s.Gateway.Running, s.Gateway.Mine, s.Gateway.Window = true, true, true
		// the calls without their bodies, which only an opened row reads
		// (GET /api/gateway/call)
		s.Gateway.Calls = gw.Recent()
		for i := range s.Gateway.Calls {
			s.Gateway.Calls[i].RequestBody, s.Gateway.Calls[i].ResponseBody = "", ""
		}
		s.Gateway.Lanes = gw.Lanes()
	} else {
		o := gateway.ServedBy()
		s.Gateway.Running, s.Gateway.Window, s.Gateway.Version = o.Running, o.Window, o.Version
		s.Gateway.Older = o.Running && update.Newer(gateway.Version, o.Version)
		// the process that is routing, for the Routing page to name it
		// (#2606): another magpie on this computer, whose routing this
		// console shows through OwnerTrace, and its lanes for the rows
		s.Gateway.Pid, s.Gateway.Serves = o.Pid, o.Serves
		if o.Running {
			if l, ok := gateway.OwnerLanes(); ok {
				s.Gateway.Lanes = l
			}
		}
	}
	s.Gateway.Archive = archiveState()
	s.CodexDaemon = provider.CodexDaemonStale()
	s.CodexApp = provider.CodexAppStale()
	s.CodexProjects = provider.CodexAppProjectsShort()
	s.Plugins = pluginSubs()
	s.Fetching = provider.FetchingNew()
	return s
}

// failMove is fail with why the move failed, for the page to say it in
// its reader's language.
func failMove(rw http.ResponseWriter, err error) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(rw).Encode(map[string]any{"error": err.Error(), "why": provider.WhyOf(err)})
}

// errRetired answers a move back to a built-in subscription, which magpie
// no longer runs: its plugin does.
var errRetired = errors.New("magpie's built-in subscriptions are retired: their community plugins run them")

// moveProvider and adoptProvider are provider.Move and Adopt, for tests
// to stand in for.
var (
	moveProvider  = provider.Move
	adoptProvider = provider.Adopt
)

// moveContext keeps a move going though the page that asked for it goes
// (closed, reloaded): stopped halfway, a move leaves accounts in neither
// place until it is run again.
func moveContext(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Minute)
}

// sponsorMail is where the Add sheet's "Become a sponsor" writes to, the
// same address and subject as usemagpie.ai's; /api/open opens this one
// mailto: link and no other
const sponsorMail = "mailto:yetoneful@gmail.com?subject=Sponsoring%20magpie"

// appLink hands an app's import link over once something here opens it
// (agent.OpenAppLink); tests stand in for the system's answer.
var appLink = agent.OpenAppLink

func providerRoutes(mux *http.ServeMux, w Windows) {
	importAppsRoutes(mux)
	pluginRoutes(mux, w)
	traceRoutes(mux)
	contextRoutesAPI(mux)
	tuneRoutes(mux)
	groupRoutes(mux)
	localHostsRoutes(mux)
	catalogModelsRoutes(mux)
	// how each key's or account's requests stand under its limit on
	// requests at once (#892), read every two seconds while a provider's
	// rows are shown; empty when the gateway runs elsewhere
	mux.HandleFunc("GET /api/lanes", func(rw http.ResponseWriter, r *http.Request) {
		lanes := map[string]gateway.Lane{}
		if gw := served.Load(); gw != nil {
			lanes = gw.Lanes()
		} else if l, ok := gateway.OwnerLanes(); ok {
			// another magpie on this computer serves the gateway: its lanes
			// are the ones to show (the rows read the same wherever it runs)
			lanes = l
		}
		writeJSON(rw, lanes)
	})
	// what the vendors' own status pages say of the APIs the providers
	// call (#971), so a vendor's outage isn't taken for a sign-in or quota
	// problem. Asked by the Providers and Usage pages; a page is read at
	// most once in upstream.Fresh, and the first look waits a little for it.
	mux.HandleFunc("GET /api/upstream", func(rw http.ResponseWriter, r *http.Request) {
		of := map[string]string{}
		var ids []string
		for _, p := range provider.All() {
			if v := upstream.VendorOf(p.Chat, p.Responses, p.Anthropic); v != "" {
				of[p.ID] = v
				if !slices.Contains(ids, v) {
					ids = append(ids, v)
				}
			}
		}
		writeJSON(rw, map[string]any{"vendors": upstream.Wait(4*time.Second, ids...), "providers": of})
	})
	mux.HandleFunc("GET /api/providers", func(rw http.ResponseWriter, r *http.Request) {
		// ?wait: an account just signed in opens in the editor with its
		// vendor's list, worth the wait there (#204)
		if r.URL.Query().Has("wait") {
			provider.FetchNew(8 * time.Second)
		}
		// ?last=1: the list kept from the last read, at once (#1519);
		// null when none is kept
		if r.URL.Query().Get("last") == "1" {
			if b, ok := lastKnownProviders(); ok {
				rw.Header().Set("Content-Type", "application/json")
				rw.Write(b)
			} else {
				writeJSON(rw, nil)
			}
			return
		}
		s := providersState()
		if sum, ok := writeProvidersJSON(rw, s); ok {
			keepProvidersSummed(s, sum)
		}
	})
	// Recent-call bodies are read only when their row is opened, not with
	// every Providers or Gateway refresh (#1521): forty calls' bodies made
	// /api/providers 20 MB. The ring may have moved on by then. ?route= is
	// the Routing page's request instead of the ring's number (Fate on
	// Discord: a request there is opened to its request and response).
	mux.HandleFunc("GET /api/gateway/call", func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Cache-Control", "no-store")
		if gw := served.Load(); gw != nil {
			var c gateway.Call
			ok := false
			if id, err := strconv.ParseUint(r.URL.Query().Get("id"), 10, 64); err == nil {
				c, ok = gw.RecentCall(id)
			} else if route, err := strconv.ParseInt(r.URL.Query().Get("route"), 10, 64); err == nil {
				c, ok = gw.RouteCall(route)
			}
			if ok {
				writeJSON(rw, map[string]any{
					"requestBody": c.RequestBody, "responseBody": c.ResponseBody,
					"requestTruncated": c.RequestTruncated, "responseTruncated": c.ResponseTruncated,
					// the headers both ways, the client's and each upstream
					// request's, masked (usrname1833 on Discord)
					"headers": c.Headers(),
				})
				return
			}
		}
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(http.StatusNotFound)
		writeJSON(rw, map[string]string{"error": "This request is no longer in the recent calls."})
	})
	// the order the Providers tab lists them in, which is the order they
	// are tried in too (#499)
	mux.HandleFunc("POST /api/providers/arrange", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Order []string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if err := provider.SetOrder(in.Order); err != nil {
			fail(rw, err)
			return
		}
		writeProvidersJSON(rw, providersState())
	})
	// a picture for a provider, picked in the editor: kept by content before
	// the provider is saved, which then points at it. The page sends it as
	// base64 in JSON — the app's web view hands a scheme handler no body for
	// a File or Blob, so a picture posted as it is arrived empty — and the
	// bytes as they are are still taken.
	mux.HandleFunc("POST /api/icons", func(rw http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(io.LimitReader(r.Body, 2*provider.MaxIcon))
		if err != nil {
			fail(rw, err)
			return
		}
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			var in struct{ Data string }
			if err := json.Unmarshal(b, &in); err != nil {
				fail(rw, err)
				return
			}
			if b, err = base64.StdEncoding.DecodeString(in.Data); err != nil {
				fail(rw, err)
				return
			}
		}
		icon, err := provider.StoreIcon(b)
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, map[string]string{"icon": icon})
	})
	// the site's own icon, found from the provider's base URL (#12)
	mux.HandleFunc("POST /api/icons/favicon", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ URL, Name string }
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		icon, err := provider.FaviconFor(r.Context(), in.URL, in.Name)
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, map[string]string{"icon": icon})
	})
	// a picture's address typed for the icon, or with none the letter tile;
	// one that can't be used gives the tile too, and failed says why
	mux.HandleFunc("POST /api/icons/url", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ URL, Name, Base string }
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		icon, failed, err := provider.IconFromURL(r.Context(), in.URL, in.Name, in.Base)
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, map[string]string{"icon": icon, "failed": failed})
	})
	mux.HandleFunc("GET /api/icons/{name}", func(rw http.ResponseWriter, r *http.Request) {
		// a big picture comes scaled to what the window draws (#909)
		f, ctype := provider.IconServed(r.PathValue("name"))
		if f == "" {
			http.NotFound(rw, r)
			return
		}
		if ctype != "" {
			rw.Header().Set("Content-Type", ctype)
		}
		// an SVG is only ever drawn as an image, never run
		rw.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
		rw.Header().Set("Cache-Control", "max-age=31536000, immutable")
		http.ServeFile(rw, r, f)
	})
	mux.HandleFunc("POST /api/provider/{action}", func(rw http.ResponseWriter, r *http.Request) {
		var req struct {
			provider.Provider
			// Proxy is the proxy its requests go through (#237), "" to
			// follow the global one; a save that leaves it out keeps it
			Proxy *string `json:"proxy"`
			// InsecureTLS is whether the certificate of its own HTTPS
			// host is taken unchecked (#2383); a save that leaves it out
			// keeps it
			InsecureTLS *bool `json:"insecureTLS"`
			// ClientCert, ClientKey and ServerCA are the PEM files of a
			// client certificate it shows and a CA its server may be
			// signed by (#5596, provider.ClientCert); a save that leaves
			// them out keeps them
			ClientCert *string `json:"clientCert"`
			ClientKey  *string `json:"clientKey"`
			ServerCA   *string `json:"serverCA"`
			// ServerPin is the SHA-256 of the server certificate its own
			// hosts are taken with alone (provider.ServerPin), "" to check
			// them as usual; a save that leaves it out keeps it
			ServerPin *string `json:"serverPin"`
			// HTTP1 is whether its own hosts are spoken HTTP/1.1 to; a
			// save that leaves it out keeps it
			HTTP1 *bool `json:"http1"`
			// Socket is how its Responses turns go over the Responses
			// WebSocket (#5781): auto, on, or "" for HTTP alone; a save
			// that leaves it out keeps it
			Socket *string `json:"socket"`
			// LimitRestOff is whether a quota refusal or a 429 leaves it
			// asked as usual; a save that leaves it out keeps it
			LimitRestOff *bool `json:"limitRestOff"`
			// NoBalanceProbe turns off the look for a relay's balance query;
			// a save that leaves it out keeps it
			NoBalanceProbe *bool `json:"noBalanceProbe"`
			// UpstreamProviders, on a Remote magpie, names its models by the
			// provider serving each there; a save that leaves it out keeps it
			UpstreamProviders *bool `json:"upstreamProviders"`
			// ImageFilesOff is whether DeepSeek's own API is sent an
			// agent's images inline (#5407); a save that leaves it out
			// keeps it, as `magpie provider set <id> image-files=off` set it
			ImageFilesOff *bool `json:"imageFilesOff"`
			// PassErrors is whether its errors go to the agent as the
			// vendor sent them (ne9ko9); a save that leaves it out keeps
			// it, as `magpie provider set <id> errors=pass` set it
			PassErrors *bool `json:"passErrors"`
			// MaxConcurrency is how many requests each of its keys or
			// accounts has out at once: a number (0 none), null for what
			// its plugin says or none; a save that leaves it out keeps it
			MaxConcurrency json.RawMessage `json:"maxConcurrency"`
			// MaxAccounts is how many of its keys or accounts take
			// requests at once: a number (0 none), null for what its
			// plugin says or none; a save that leaves it out keeps it
			MaxAccounts json.RawMessage `json:"maxAccounts"`
			// QueueLimit and QueueWait are how many may wait for each key
			// or account and for how many seconds (#892): numbers, 0 or
			// null for no bound; a save that leaves them out keeps them
			QueueLimit json.RawMessage `json:"queueLimit"`
			QueueWait  json.RawMessage `json:"queueWait"`
			// MaxRPM is how many requests each key or account sends the
			// vendor in any minute: a number, 0 or null for no limit; a
			// save that leaves it out keeps it
			MaxRPM json.RawMessage `json:"maxRPM"`
			// Limit, for accountconcurrency: the account's or key's own
			// limit, 0 for none, null for the provider's (#892)
			Limit *int `json:"limit"`
			// RateWindows are the time-of-day windows it bills at its own
			// rate (#2463); a list as checked (CheckRateWindows), null or
			// [] for none; left out, they are kept
			RateWindows json.RawMessage `json:"rateWindows"`
			// PriceRate is what it charges against the official price
			// (#819): a number, null or 0 for none; left out, it is kept
			PriceRate    json.RawMessage `json:"priceRate"`
			AccountOrder []string        `json:"accountOrder"`
			// New is set by the editor's Add: the provider is one more, never
			// one replacing the provider that has its id or name
			New bool `json:"new"`
			// ClearBalanceToken drops the saved balance token, which a
			// blank one in the form otherwise keeps
			ClearBalanceToken bool `json:"clearBalanceToken"`
			// AccessKeyID is a Volcengine access key's ID or a Tencent
			// Cloud SecretId, "" for none; a save that leaves it out keeps
			// it. Its Secret (SecretAccessKey, a Tencent SecretKey) is kept
			// when sent blank, unless ClearAccessKey drops it.
			AccessKeyID    *string `json:"accessKeyID"`
			ClearAccessKey bool    `json:"clearAccessKey"`
			// From is the id the provider had: another is a rename
			From string `json:"from"`
			// LibraryMCP, on a Remote magpie, is its editor's switch: its
			// router a Library server (library.SetRemoteMCP); left out,
			// it stays as it is
			LibraryMCP *bool `json:"libraryMCP"`
			// LibraryShared, on a Remote magpie, is the switch for its
			// Library's skills and prompts (library.SetBorrow)
			LibraryShared *bool `json:"libraryShared"`
			// CopyOf, with New, is the provider the new one is a copy of
			// (#268): its key and what else the form doesn't carry are
			// taken from it when left out
			CopyOf string `json:"copyOf"`
			// Model and ModelName, for name: the name the user gives one
			// of its models, "" for its own again
			Model     string `json:"model"`
			ModelName string `json:"modelName"`
			// Efforts, for efforts: the reasoning levels it offers, none
			// for all it has
			Efforts []string `json:"efforts"`
			// Images, for images: whether the model takes images. Nil
			// gives the vendor's answer back.
			Images *bool `json:"images"`
			// ModelPrefs, for save: the names, levels and images the
			// editor's Model settings changed, by model id, made with the
			// rest of the Save and not a click at a time
			ModelPrefs map[string]provider.ModelPref `json:"modelPrefs"`
			// Outputs, for save: the reply limits the editor's Max output
			// says, by model id, "*" for all (provider.SetModelOutputs); a
			// save that leaves it out keeps them
			Outputs map[string]int `json:"outputs"`
			// Compacts, for save: the thresholds the editor's Compact at
			// says, by model id, "*" for all (provider.SetModelCompacts,
			// #876); a save that leaves it out keeps them
			Compacts map[string]int `json:"compacts"`
			// Routing and Affinity, for route, affinity and save: how
			// requests spread over its keys or accounts, and how long a
			// conversation stays with the one that answered it. The
			// editor's Save sends them only when picked there, a save
			// that leaves them out keeping them: the Routing page's Stays
			// was lost at each Save of the provider's editor, which never
			// sent it.
			Routing  *string `json:"routing"`
			Affinity *string `json:"affinity"`
			// Sink, for sink and save: whether one rate limited with
			// quota left goes to the back (provider.Provider.Sink); a
			// save that leaves it out keeps it
			Sink *bool `json:"sink"`
			// NativeCompaction, for save: Codex's compaction asked of it
			// as Codex sent it (provider.Provider.NativeCompaction); a save
			// that leaves it out keeps it
			NativeCompaction *bool `json:"nativeCompaction"`
			// PassUserAgent, for save: the editor's Send the agent's own
			// User-Agent (provider.Provider.MagpieUserAgent off); a save
			// that leaves it out keeps it
			PassUserAgent *bool `json:"passUserAgent"`
			// AuthHeader, for save: which header the key goes in on a
			// third-party Anthropic API (provider.Provider.AuthHeader); a
			// save that leaves it out keeps it, one learned too
			AuthHeader *string `json:"authHeader"`
			// Test, for test: models to send a request each, in place of
			// one per endpoint
			Test []string `json:"test"`
			// As, for test: the agent the request is asked as
			// (provider.TestAs: "codex", "claude-code"), "" as magpie
			As string `json:"as"`
			// Run, for test and teststop: the window's name for one
			// test, which its Stop ends (testRuns)
			Run string `json:"run"`
			// DetectModels, for detect: models to ask on each API, each
			// answered on its own, in place of Model
			DetectModels []string `json:"detectModels"`
			// Picked, for detect with no Model: the models the form has
			// picked and not saved yet, one of which each API is asked
			// for before the saved picks or the vendor's list (#1545)
			Picked []string `json:"picked"`
			// Account and Allow, for accountmodels: the account (its name)
			// or key (its id) and the models it alone serves, none for all
			// the provider's (#474)
			Account string   `json:"account"`
			Allow   []string `json:"allow"`
			// Cap, for accountcap: the share (1–99) of its windows the
			// account is used to at most, 0 for no cap; for windowcap, the
			// share of Window alone (1–99, 100 for none on it), 0 to have
			// it follow the account's cap
			Cap    int    `json:"cap"`
			Window string `json:"window"`
			// Windows, for windowcap, in place of Window: a family's
			// windows, which the GUI shows as one meter
			Windows []string `json:"windows"`
			// Amount, for windowcap in place of Cap: a fixed amount of the
			// window's own unit the account is used to at most, 0 to lift it
			// (provider.AccountWindowAmounts)
			Amount *float64 `json:"amount"`
			// Weight, for accountweight: the account's share under By
			// weight routing, 1 to provider.MaxKeyWeight (0 or 1: none)
			Weight int `json:"weight"`
			// Spend, for keyspendcap: the spend cap set on the key Account
			// (its id), in USD a day, a month or in all; none, or an
			// amount of 0, takes it off
			Spend *provider.SpendCap `json:"spend"`
			// Balance, for keyownbalance: the balance typed for the key
			// Account (its id), counted from now, in USD or, with Currency
			// "cny", in yuan; none takes it off
			Balance  *float64 `json:"balance"`
			Currency string   `json:"currency"`
			// Refill, for keybalancerefill: when the key's balance typed
			// is whole again; none for never
			Refill *provider.Refill `json:"refill"`
			// Expiring, for keyexpiring: a grant of credit that expires
			// on the key's balance typed, {amount, until}, added beside
			// the others; with Remove, the grant {at} (or every one
			// {until}) taken off; none takes every grant off
			Expiring *provider.Expiring `json:"expiring"`
			Remove   bool               `json:"remove"`
			// Alias, for accountname: the account's name of the user's
			// own, "" to take it off (#1515)
			Alias string `json:"alias"`
			// Typed, for test and models: the request carries the editor's
			// form, which is tried as it stands before a Save (see typed)
			Typed bool `json:"typed"`
			// Base, for detect: the base URL typed, asked as each API
			// takes it where the form has no URL of that API's own
			Base string `json:"base"`
			// NoModels, for save: the editor's Select none, kept with no
			// model picked (#1818); a save that leaves it out keeps it
			NoModels *bool `json:"noModels"`
			// BestInFamily, for save: the editor's Best in family, kept
			// with no model picked; a save that leaves it out keeps it
			BestInFamily *bool `json:"bestInFamily"`
			// ListOff, for save: Fetch the model list unticked; a save
			// that leaves it out keeps it
			ListOff *bool `json:"listOff"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			fail(rw, err)
			return
		}
		in := req.Provider
		if !provider.BillingOK(in.Billing) {
			fail(rw, fmt.Errorf("billing is plan, payg or empty, not %q", in.Billing))
			return
		}
		if req.NoModels != nil {
			in.NoModels = *req.NoModels
		}
		if req.BestInFamily != nil {
			in.BestInFamily = *req.BestInFamily
		}
		if req.ListOff != nil {
			in.ListOff = *req.ListOff
		}
		if req.Routing != nil {
			in.Routing = *req.Routing
		}
		if req.Affinity != nil {
			in.Affinity = *req.Affinity
		}
		if req.Sink != nil {
			in.Sink = *req.Sink
		}
		if req.NativeCompaction != nil {
			in.NativeCompaction = *req.NativeCompaction
		}
		if req.PassUserAgent != nil {
			in.MagpieUserAgent = !*req.PassUserAgent
		}
		if req.AuthHeader != nil {
			in.AuthHeader = *req.AuthHeader
		}
		var moved []agent.Move
		switch r.PathValue("action") {
		case "show":
			// a signed-in account the user removed, back with its picks
			if err := provider.ShowAccount(in.ID); err != nil {
				fail(rw, err)
				return
			}
		case "move":
			// a built-in subscription's accounts onto its community plugin,
			// with the models the editor has picked kept first: a failed
			// move says to untick one the plugin doesn't serve, and the
			// editor's Try again is pressed with it unticked but not saved
			// (noting_ever on X: deep-model unticked, and the move failed
			// on it all the same)
			if in.Models != nil && !in.NoModels {
				var err error
				if moved, err = agent.Reseat(func() error { return provider.SetModels(in.ID, in.Models) }); err != nil {
					fail(rw, err)
					return
				}
			}
			ctx, cancel := moveContext(r)
			defer cancel()
			if err := moveProvider(ctx, in.ID); err != nil {
				failMove(rw, err)
				return
			}
		case "adopt":
			// a built-in subscription not signed in to, onto its plugin
			// first: the plugin installed, and its sign-in the plugin's
			ctx, cancel := moveContext(r)
			defer cancel()
			if err := adoptProvider(ctx, in.ID); err != nil {
				failMove(rw, err)
				return
			}
		case "moveback":
			// the built-ins are retired: there is no going back to one
			fail(rw, errRetired)
			return
		case "quiet":
			// a removed account's "Add it back" line, dismissed (#116)
			if err := provider.QuietAccount(in.ID); err != nil {
				fail(rw, err)
				return
			}
		case "tuck", "untuck":
			// a removed account hidden from the Add sheet's "Removed from
			// magpie" too, or listed there again (#116)
			if err := provider.TuckAccount(in.ID, r.PathValue("action") == "tuck"); err != nil {
				fail(rw, err)
				return
			}
		case "forget":
			// a removed account signed out for good: adding it back
			// later signs in afresh rather than bringing it back (#694)
			if err := provider.ForgetAccount(in.ID); err != nil {
				fail(rw, err)
				return
			}
		case "keychain-retry":
			// Try again beside Codex's sign-in the macOS keychain didn't
			// give: a refusal kept is dropped and the item read once
			// (#2367); its answer, or that the prompt is still up
			if err := provider.RetryCodexKeychain(); err != nil {
				fail(rw, err)
				return
			}
		case "save":
			// a preset needs nothing but the key; a saved provider keeps
			// its key when the form left it blank
			if pr, err := provider.FromPreset(in.Preset); err == nil && in.Chat == "" && in.Responses == "" && in.Anthropic == "" && in.Gemini == "" {
				pr.Key, pr.Models, pr.Fallback, pr.Headers, pr.BalanceToken, pr.Contexts = in.Key, in.Models, in.Fallback, in.Headers, in.BalanceToken, in.Contexts
				pr.ZhipuTeam = in.ZhipuTeam
				pr.NoModels = in.NoModels
				pr.BestInFamily = in.BestInFamily
				pr.ListOff = in.ListOff
				pr.ExtraBody = in.ExtraBody
				pr.ReplyHeaders = in.ReplyHeaders
				pr.PassHeaders = in.PassHeaders
				pr.SecretAccessKey = in.SecretAccessKey
				pr.Vertex = in.Vertex
				pr.Searches = in.Searches
				pr.PinUpstream = in.PinUpstream
				pr.Unredacted = in.Unredacted
				pr.CountFree = in.CountFree
				pr.Billing = in.Billing
				pr.NativeCompaction = in.NativeCompaction
				pr.MagpieUserAgent = in.MagpieUserAgent
				pr.AuthHeader = in.AuthHeader
				// and what its editor sets beside them: how requests spread
				// over its keys, how long a conversation stays, the one
				// rate limited to the back, Unlisted and each key's proxy.
				// A decision-only preset (TypeSafe Jev) has no chat URL, so
				// every save of its editor came here, and In order went
				// back to Smart (fflake33 on Discord)
				pr.Routing, pr.Affinity, pr.Sink = in.Routing, in.Affinity, in.Sink
				pr.Unlisted = in.Unlisted
				pr.HiddenDrawers = in.HiddenDrawers
				pr.HiddenVideomakers = in.HiddenVideomakers
				pr.AccountProxies = in.AccountProxies
				if in.Name != "" {
					pr.Name = in.Name
				}
				if in.ID != "" {
					pr.ID = in.ID
				}
				// a decision API's address the user gave, such as the
				// Cloudflare one naming the account
				if d := strings.TrimSpace(in.Decide); d != "" && pr.Decide != "" {
					pr.Decide = d
				}
				in = pr
			}
			// fields the body can't be given are refused, not dropped
			// unseen (#1604)
			if why := provider.ExtraBodyOK(in.ExtraBody); why != "" {
				fail(rw, errors.New(why))
				return
			}
			// and reply headers magpie never passes on (#4145)
			if why := provider.ReplyHeadersOK(in.ReplyHeaders); why != "" {
				fail(rw, errors.New(why))
				return
			}
			// and request headers that carry magpie's key or the
			// connection's own
			if why := provider.RequestHeadersOK(in.PassHeaders); why != "" {
				fail(rw, errors.New(why))
				return
			}
			cc, keepCC, err := concurrencyOf(req.MaxConcurrency)
			if err != nil {
				fail(rw, err)
				return
			}
			in.MaxConcurrency = cc
			ma, keepMA, err := limitOf(req.MaxAccounts, "accounts at once")
			if err != nil {
				fail(rw, err)
				return
			}
			in.MaxAccounts = ma
			ql, keepQL, err := queueOf(req.QueueLimit, "queue length")
			if err != nil {
				fail(rw, err)
				return
			}
			qw, keepQW, err := queueOf(req.QueueWait, "queue wait")
			if err != nil {
				fail(rw, err)
				return
			}
			if err := provider.CheckQueue(ql, qw); err != nil {
				fail(rw, err)
				return
			}
			in.QueueLimit, in.QueueWait = ql, qw
			rpm, keepRPM, err := queueOf(req.MaxRPM, "requests a minute")
			if err != nil {
				fail(rw, err)
				return
			}
			if err := provider.CheckRPM(rpm); err != nil {
				fail(rw, err)
				return
			}
			in.MaxRPM = rpm
			rate, keepRate, err := priceRateOf(req.PriceRate)
			if err != nil {
				fail(rw, err)
				return
			}
			in.PriceRate = rate
			wins, keepWins, err := rateWindowsOf(req.RateWindows)
			if err != nil {
				fail(rw, err)
				return
			}
			if !keepWins {
				in.RateWindows = wins
			}
			// many keys pasted into the key field (361 on Discord: a provider
			// added with hundreds): the first is the key, the others its
			// accounts, as Paste several adds them
			var moreKeys []string
			if ks := provider.SplitKeys(in.Key); len(ks) > 1 {
				saved := req.From
				if saved == "" {
					saved = in.ID
				}
				if o, err := provider.Find(saved); req.New || err != nil || o.Key != in.Key {
					in.Key, moreKeys = ks[0], ks[1:]
				}
			}
			if req.AccessKeyID != nil {
				in.AccessKeyID = strings.TrimSpace(*req.AccessKeyID)
			}
			in.SecretAccessKey = strings.TrimSpace(in.SecretAccessKey)
			var old *provider.Provider
			if req.New && in.NativeCompaction && !in.CarriesCompaction() {
				fail(rw, fmt.Errorf("%s can't carry Codex's native compaction: only a provider with a Responses API, a ChatGPT account or GitHub Copilot can", cmp.Or(in.Name, in.ID)))
				return
			}
			if req.New {
				// a second one of a preset, or a name already in use, is
				// added beside the first under the next free id
				if req.Proxy != nil {
					in.Proxy = *req.Proxy
				}
				if req.InsecureTLS != nil {
					in.InsecureTLS = *req.InsecureTLS
				}
				in = withCerts(in, nil, req.ClientCert, req.ClientKey, req.ServerCA, req.ServerPin)
				if req.HTTP1 != nil {
					in.HTTP1 = *req.HTTP1
				}
				if req.Socket != nil {
					in.Socket, _ = provider.ParseSocket(*req.Socket)
				}
				if req.LimitRestOff != nil {
					in.LimitRestOff = *req.LimitRestOff
				}
				if req.NoBalanceProbe != nil {
					in.NoBalanceProbe = *req.NoBalanceProbe
				}
				if req.UpstreamProviders != nil {
					in.UpstreamProviders = *req.UpstreamProviders && in.IsRemoteMagpie()
				}
				if req.ImageFilesOff != nil {
					in.ImageFilesOff = *req.ImageFilesOff
				}
				if req.PassErrors != nil {
					in.PassErrors = *req.PassErrors
				}
				add := provider.Add
				if req.CopyOf != "" {
					add = func(p provider.Provider) (string, error) { return provider.AddCopy(p, req.CopyOf) }
				}
				id, err := add(in)
				if err != nil {
					fail(rw, err)
					return
				}
				in.ID = id
			} else {
				// a rename saves the rest under the id it had, then moves it
				to := strings.ToLower(strings.TrimSpace(in.ID))
				rename := req.From != "" && req.From != to
				if rename {
					if to == "" || to != provider.Slug(to) {
						fail(rw, fmt.Errorf("a provider's id must be lowercase letters, digits and dashes, not %q", in.ID))
						return
					}
					in.ID = req.From
				}
				old, _ = provider.Find(in.ID)
				if req.Proxy != nil {
					in.Proxy = *req.Proxy
				} else if old != nil {
					in.Proxy = old.Proxy
				}
				if req.InsecureTLS != nil {
					in.InsecureTLS = *req.InsecureTLS
				} else if old != nil {
					in.InsecureTLS = old.InsecureTLS
				}
				in = withCerts(in, old, req.ClientCert, req.ClientKey, req.ServerCA, req.ServerPin)
				if req.HTTP1 != nil {
					in.HTTP1 = *req.HTTP1
				} else if old != nil {
					in.HTTP1 = old.HTTP1
				}
				if req.Socket != nil {
					in.Socket, _ = provider.ParseSocket(*req.Socket)
				} else if old != nil {
					in.Socket = old.Socket
				}
				if req.LimitRestOff != nil {
					in.LimitRestOff = *req.LimitRestOff
				} else if old != nil {
					in.LimitRestOff = old.LimitRestOff
				}
				if req.NoBalanceProbe != nil {
					in.NoBalanceProbe = *req.NoBalanceProbe
				} else if old != nil {
					in.NoBalanceProbe = old.NoBalanceProbe
				}
				if req.UpstreamProviders != nil {
					in.UpstreamProviders = *req.UpstreamProviders && in.IsRemoteMagpie()
				} else if old != nil {
					in.UpstreamProviders = old.UpstreamProviders
				}
				// DeepSeek's images sent inline, set here or with `magpie
				// provider set <id> image-files=off` (#5407)
				if req.ImageFilesOff != nil {
					in.ImageFilesOff = *req.ImageFilesOff
				} else if old != nil {
					in.ImageFilesOff = old.ImageFilesOff
				}
				if req.PassErrors != nil {
					in.PassErrors = *req.PassErrors
				} else if old != nil {
					in.PassErrors = old.PassErrors
				}
				if keepCC && old != nil {
					in.MaxConcurrency = old.MaxConcurrency
				}
				if keepMA && old != nil {
					in.MaxAccounts = old.MaxAccounts
				}
				if keepQL && old != nil {
					in.QueueLimit = old.QueueLimit
				}
				if keepQW && old != nil {
					in.QueueWait = old.QueueWait
				}
				if keepRPM && old != nil {
					in.MaxRPM = old.MaxRPM
				}
				if keepRate && old != nil {
					in.PriceRate = old.PriceRate
				}
				if keepWins && old != nil {
					in.RateWindows = old.RateWindows
				}
				// how long its keys rest out of quota is set in
				// providers.json or with `magpie provider quota-rest`,
				// which the form doesn't show: kept as it was (#1772)
				if in.QuotaRest == 0 && old != nil {
					in.QuotaRest = old.QuotaRest
				}
				// and the time of day they are back, set with `magpie
				// provider quota-rest <id> until 08:00` (#2008)
				if in.QuotaResetAt == "" && old != nil {
					in.QuotaResetAt = old.QuotaResetAt
				}
				// and the most they sit out while they keep failing, set
				// with `magpie provider rest-cap` (#2008)
				if in.RestCap == 0 && old != nil {
					in.RestCap = old.RestCap
				}
				// and Codex's requests as sent, set with `magpie provider
				// set <id> codex=as-sent` (#1758)
				if !in.CodexAsSent && old != nil {
					in.CodexAsSent = old.CodexAsSent
				}
				// and Antigravity's prompt as written, set with `magpie
				// provider set antigravity prompt=as-written`
				if !in.PromptAsWritten && old != nil {
					in.PromptAsWritten = old.PromptAsWritten
				}
				// and that magpie filled the Gemini URL in once, which the
				// form doesn't send: a URL cleared in it stays cleared
				if !in.GeminiFilled && old != nil {
					in.GeminiFilled = old.GeminiFilled
				}
				// and additional_tools taken, set with `magpie provider
				// set <id> additional-tools=on` (#2660)
				if !in.AdditionalTools && old != nil {
					in.AdditionalTools = old.AdditionalTools
				}
				if !in.AdditionalToolsOff && old != nil {
					in.AdditionalToolsOff = old.AdditionalToolsOff
				}
				// and how long a request to it alone is kept while it is
				// busy, set with `magpie provider set <id> patience=`
				if in.Patience == 0 && old != nil {
					in.Patience = old.Patience
				}
				// each account's own proxy likewise: {} clears them
				if in.AccountProxies == nil && old != nil {
					in.AccountProxies = old.AccountProxies
				}
				// each account's own models are set on their own, with
				// accountmodels
				if old != nil {
					in.AccountModels, in.AccountModelsAll = old.AccountModels, old.AccountModelsAll
				}
				// and their usage caps, with accountcap and windowcap, their
				// limits on requests at once, with accountconcurrency, and
				// their weights, with accountweight
				if old != nil {
					in.AccountCaps, in.AccountWindowCaps, in.AccountWindowAmounts = old.AccountCaps, old.AccountWindowCaps, old.AccountWindowAmounts
					in.AccountConcurrency = old.AccountConcurrency
					in.AccountWeights = old.AccountWeights
					in.AccountNames = old.AccountNames     // with accountname
					in.KeySpendCaps = old.KeySpendCaps     // with keyspendcap
					in.KeyOwnBalances = old.KeyOwnBalances // with keyownbalance
				}
				// the image models unticked in its editor likewise: []
				// shows them all (321sssrt on X)
				if in.HiddenDrawers == nil && old != nil {
					in.HiddenDrawers = old.HiddenDrawers
				}
				if in.HiddenVideomakers == nil && old != nil {
					in.HiddenVideomakers = old.HiddenVideomakers
				}
				// a Zhipu key's team likewise: {} clears it
				if in.ZhipuTeam == nil && old != nil {
					in.ZhipuTeam = old.ZhipuTeam
				}
				// and its extra body fields: {} clears them
				if in.ExtraBody == nil && old != nil {
					in.ExtraBody = old.ExtraBody
				}
				// and the reply headers passed on: [] clears them
				if in.ReplyHeaders == nil && old != nil {
					in.ReplyHeaders = old.ReplyHeaders
				}
				if in.PassHeaders == nil && old != nil {
					in.PassHeaders = old.PassHeaders
				}
				// Select none likewise, while nothing is picked (#1818)
				if req.NoModels == nil && old != nil {
					in.NoModels = old.NoModels
				}
				// and Best in family (normalize drops it under a pick)
				if req.BestInFamily == nil && old != nil {
					in.BestInFamily = old.BestInFamily
				}
				// and the list turned off
				if req.ListOff == nil && old != nil {
					in.ListOff = old.ListOff
				}
				// a Volcengine access key likewise: its ID kept when left
				// out or sent blank (an editor opened before the key was
				// given sends ""), its Secret when sent blank, unless the
				// form dropped it (#1821)
				if in.AccessKeyID == "" && old != nil && !req.ClearAccessKey {
					in.AccessKeyID = old.AccessKeyID
				}
				if in.SecretAccessKey == "" && old != nil && !req.ClearAccessKey {
					in.SecretAccessKey = old.SecretAccessKey
				}
				// and a Vertex AI provider's project and credentials
				if in.Vertex == nil && old != nil {
					in.Vertex = old.Vertex
				}
				if in.Key == "" && old != nil {
					in.Key = old.Key
				}
				if in.BalanceToken == "" && old != nil && !req.ClearBalanceToken {
					in.BalanceToken = old.BalanceToken
				}
				if old != nil {
					// the other keys are kept apart, in the Accounts list
					in.Keys = old.Keys
					// the editor's picks, or as they were
					if req.Routing == nil {
						in.Routing = old.Routing
					}
					if req.Affinity == nil {
						in.Affinity = old.Affinity
					}
					if req.Sink == nil {
						in.Sink = old.Sink
					}
					if req.NativeCompaction == nil {
						in.NativeCompaction = old.NativeCompaction
					}
					if req.PassUserAgent == nil {
						in.MagpieUserAgent = old.MagpieUserAgent
					}
					if req.AuthHeader == nil {
						in.AuthHeader = old.AuthHeader
					}
					// its list's schedule and rules are set on their own, from
					// the CLI (#2135)
					in.RefreshEvery, in.AutoInclude = old.RefreshEvery, old.AutoInclude
					in.KeepLogin = old.KeepLogin // set on its own, with keeplogin
					in.KeepLoginAs = old.KeepLoginAs
					in.SwitchOnly, in.RestartApp = old.SwitchOnly, old.RestartApp // switchonly, restartapp
					in.KeepSelf = old.KeepSelf                                    // keepself
					in.ThisDevice = old.ThisDevice                                // thisdevice
					in.CapStop, in.CapCareful = old.CapStop, old.CapCareful       // capstop, capcareful
					in.UseFirst = old.UseFirst                                    // usefirst
					in.NoStandIn = old.NoStandIn                                  // standin
					in.Off = old.Off                                              // and this with off and on
					if in.Contexts == nil {
						in.Contexts = old.Contexts // a save that doesn't say
					}
					if in.Key == old.Key {
						in.KeyName, in.KeyProtocol, in.KeyWeight, in.KeyPlan = old.KeyName, old.KeyProtocol, old.KeyWeight, old.KeyPlan
					}
					// what the editor doesn't show, set from the CLI or the
					// TUI, is kept: its tag, website and key page, and on a
					// preset's provider its Balance and Models URLs too,
					// which only a custom one's editor has fields for
					in.Family = cmp.Or(in.Family, old.Family)
					in.Website = cmp.Or(in.Website, old.Website)
					in.KeysURL = cmp.Or(in.KeysURL, old.KeysURL)
					if provider.Preset(in.Preset) != nil {
						in.BalanceURL = cmp.Or(in.BalanceURL, old.BalanceURL)
						in.BalancePath = cmp.Or(in.BalancePath, old.BalancePath)
						in.BalanceMethod = cmp.Or(in.BalanceMethod, old.BalanceMethod)
						in.ModelsURL = cmp.Or(in.ModelsURL, old.ModelsURL)
					}
				}
				// the Base URL's API, as picked in the editor; a save that
				// doesn't say keeps it
				if in.BaseAPI == "" && old != nil {
					in.BaseAPI = old.BaseAPI
				}
				// a save that doesn't say keeps the icon, and where it came from
				if in.Icon == "" && old != nil && in.Preset == "" {
					in.Icon, in.IconFrom = old.Icon, old.IconFrom
				}
				// native compaction only where the wire carries it: a
				// signed-in account is told by the one saved
				if in.NativeCompaction && !in.CarriesCompaction() && (old == nil || old.Account == nil || !old.CarriesCompaction()) {
					fail(rw, fmt.Errorf("%s can't carry Codex's native compaction: only a provider with a Responses API, a ChatGPT account or GitHub Copilot can", cmp.Or(in.Name, in.ID)))
					return
				}
				if err := provider.Save(in); err != nil {
					fail(rw, err)
					return
				}
				if rename {
					if _, err := agent.RenameProvider(in.ID, to); err != nil {
						fail(rw, err)
						return
					}
					in.ID = to
				}
			}
			if len(moreKeys) > 0 {
				// those it has already are passed over, not an error
				if added, had, err := provider.AddKeys(in.ID, moreKeys, in.KeyProtocol); err != nil && !(added == 0 && had > 0) {
					fail(rw, err)
					return
				}
			}
			if len(req.ModelPrefs) > 0 {
				if err := provider.SetModelPrefs(in.ID, req.ModelPrefs); err != nil {
					fail(rw, err)
					return
				}
			}
			// a Remote magpie's router given a server or taken out, before
			// its list below asks it
			askHub := false
			if req.LibraryMCP != nil {
				if p, err := provider.Find(in.ID); err == nil && p.IsRemoteMagpie() && library.RemoteMCPOn()[in.ID] != *req.LibraryMCP {
					if _, err := library.SetRemoteMCP(in.ID, *req.LibraryMCP); err != nil {
						fail(rw, err)
						return
					}
					askHub = *req.LibraryMCP
				}
			}
			if req.LibraryShared != nil {
				if p, err := provider.Find(in.ID); err == nil && p.IsRemoteMagpie() && library.BorrowedOn()[in.ID] != *req.LibraryShared {
					if _, err := library.SetBorrow(in.ID, *req.LibraryShared); err != nil {
						fail(rw, err)
						return
					}
					if *req.LibraryShared {
						id := in.ID
						go func() {
							ctx, cancel := context.WithTimeout(context.Background(), 2*provider.RemoteLibraryWait)
							defer cancel()
							if _, err := library.UseRemoteLibrary(ctx, id); err != nil {
								log.Printf("library: the Library of %s: %v", id, err)
							}
						}()
					}
				}
			}
			provider.ForgetBalances()
			if old == nil || old.AccessKeyID != in.AccessKeyID || old.SecretAccessKey != in.SecretAccessKey {
				// the plan's windows are read afresh with the new access key
				provider.ForgetPlanQuotas()
			}
			// a new key means a new vendor list is worth a try; keep it short
			// (not for one switched off, which is asked nothing)
			if p, err := provider.Find(in.ID); err == nil && p.Ready() && !p.Off {
				ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
				// and each key it didn't have before is given the models
				// its own list has, beside it (PrefillNewKeys)
				had := map[string]map[string]bool{in.ID: {}}
				if old != nil {
					for _, k := range old.KeyList() {
						had[in.ID][k.ID] = true
					}
				}
				var wg sync.WaitGroup
				wg.Go(func() { provider.PrefillNewKeys(ctx, had, in.ID) })
				if old == nil || old.Key != p.Key {
					p.Fetch(ctx)
				} else if askHub {
					provider.AskRemoteHub(ctx, in.ID)
				}
				wg.Wait()
				cancel()
			}
			// after the list is fetched: a limit has to name a model it has
			if req.Outputs != nil {
				if err := provider.SetModelOutputs(in.ID, req.Outputs); err != nil {
					fail(rw, err)
					return
				}
			}
			if req.Compacts != nil {
				if err := provider.SetModelCompacts(in.ID, req.Compacts); err != nil {
					fail(rw, err)
					return
				}
			}
		case "key":
			// the saved key, for the editor's Show button; it never
			// leaves this machine (the panel is served on loopback).
			// With Account, the key of that id: each of several keys'
			// rows copies its own (#1480).
			if req.Account != "" {
				k, err := provider.KeyOf(in.ID, req.Account)
				if err != nil {
					fail(rw, err)
					return
				}
				writeJSON(rw, map[string]string{"key": k})
				return
			}
			p, err := provider.Find(in.ID)
			if err != nil {
				fail(rw, err)
				return
			}
			writeJSON(rw, map[string]string{"key": p.Key})
			return
		case "name":
			// the agents' own model lists follow, through catalog.Changed
			if err := provider.SetModelName(in.ID+"/"+req.Model, req.ModelName); err != nil {
				fail(rw, err)
				return
			}
		case "efforts":
			if err := provider.SetModelEfforts(in.ID+"/"+req.Model, req.Efforts); err != nil {
				fail(rw, err)
				return
			}
		case "images":
			if err := provider.SetModelImage(in.ID+"/"+req.Model, req.Images); err != nil {
				fail(rw, err)
				return
			}
		case "arrange":
			// Promoting the first row is the same account switch as Make first:
			// keep the agents' catalogs in sync with the new primary sign-in.
			var err error
			moved, err = agent.Reseat(func() error { return provider.SetAccountOrder(in.ID, req.AccountOrder) })
			if err != nil {
				fail(rw, err)
				return
			}
			agent.SyncCatalogSoon()
		case "accountmodels":
			if err := provider.SetAccountModels(in.ID, req.Account, req.Allow); err != nil {
				fail(rw, err)
				return
			}
		case "keylisted":
			// the models the key Account's own fetched list has, for the
			// key picker's Select all available; nothing saved
			ms := provider.KeyListed(in.ID, req.Account)
			writeJSON(rw, map[string]any{"models": ms, "known": ms != nil})
			return
		case "accountcap":
			if err := provider.SetAccountCap(in.ID, req.Account, req.Cap); err != nil {
				fail(rw, err)
				return
			}
		case "accountname":
			if err := provider.SetAccountName(in.ID, req.Account, req.Alias); err != nil {
				fail(rw, err)
				return
			}
		case "windowcap":
			if len(req.Windows) == 0 {
				req.Windows = []string{req.Window}
			}
			set := func() error { return provider.SetWindowCaps(in.ID, req.Account, req.Windows, req.Cap) }
			if req.Amount != nil {
				// a fixed amount of the window's unit, 0 to lift it
				set = func() error { return provider.SetWindowAmount(in.ID, req.Account, req.Windows, *req.Amount) }
			}
			if err := set(); err != nil {
				fail(rw, err)
				return
			}
		case "accountconcurrency":
			if err := provider.SetAccountConcurrency(in.ID, req.Account, req.Limit); err != nil {
				fail(rw, err)
				return
			}
		case "keyspendcap":
			var c *provider.SpendCap
			if req.Spend != nil && req.Spend.USD > 0 {
				c = &provider.SpendCap{USD: req.Spend.USD, Period: req.Spend.Period}
			}
			if err := provider.SetKeySpendCap(in.ID, req.Account, c); err != nil {
				fail(rw, err)
				return
			}
		case "keyownbalance":
			var b *provider.OwnBalance
			switch {
			case req.Balance == nil:
			case req.Currency == "cny":
				b = &provider.OwnBalance{CNY: req.Balance}
			case req.Currency == "" || req.Currency == "usd":
				b = &provider.OwnBalance{USD: *req.Balance}
			default:
				fail(rw, fmt.Errorf("a balance is in usd or cny, not %q", req.Currency))
				return
			}
			if err := provider.SetKeyOwnBalance(in.ID, req.Account, b); err != nil {
				fail(rw, err)
				return
			}
		case "keybalancerefill":
			if err := provider.SetKeyBalanceRefill(in.ID, req.Account, req.Refill); err != nil {
				fail(rw, err)
				return
			}
		case "keyexpiring":
			var err error
			switch {
			case req.Expiring == nil:
				err = provider.ClearKeyExpiring(in.ID, req.Account)
			case req.Remove:
				err = provider.RemoveKeyExpiring(in.ID, req.Account, *req.Expiring)
			default:
				err = provider.AddKeyExpiring(in.ID, req.Account, *req.Expiring)
			}
			if err != nil {
				fail(rw, err)
				return
			}
		case "accountweight":
			if err := provider.SetAccountWeight(in.ID, req.Account, req.Weight); err != nil {
				fail(rw, err)
				return
			}
		case "route":
			if err := provider.SetRouting(in.ID, in.Routing); err != nil {
				fail(rw, err)
				return
			}
		case "off", "on":
			// switched off, it stays with its keys, but agents are given
			// none of its models; the files they keep them in follow,
			// through catalog.Changed, and the agents on one of its models
			// are moved to another
			var err error
			if moved, err = agent.Reseat(func() error {
				return provider.SetOff(in.ID, r.PathValue("action") == "off")
			}); err != nil {
				fail(rw, err)
				return
			}
			provider.ForgetBalances()
		case "keeplogin":
			// Codex or Claude Code stays signed in to the first account, or
			// to one of the user's choosing (keepLoginAs), which signs it in
			// to that one now, and back to the first when let go (#524)
			var err error
			moved, err = agent.Reseat(func() error {
				if in.KeepLogin && in.KeepLoginAs != "" {
					return provider.SetKeepLoginAs(in.ID, in.KeepLoginAs)
				}
				return provider.SetKeepLogin(in.ID, in.KeepLogin)
			})
			if err != nil {
				fail(rw, err)
				return
			}
			agent.SyncCatalogSoon()
		case "switchonly":
			// magpie only switches Codex's own sign-in, its requests going
			// straight to OpenAI, or routes them for failover again
			// (#1907); the agent's config follows at once
			if err := provider.SetSwitchOnly(in.ID, in.SwitchOnly); err != nil {
				fail(rw, err)
				return
			}
			agent.SyncCatalogSoon()
		case "restartapp":
			// the Codex app's app-server restarted for an account change
			// magpie makes by itself, or left be (atom0964 on Discord)
			if err := provider.SetRestartApp(in.ID, in.RestartApp); err != nil {
				fail(rw, err)
				return
			}
		case "keepself":
			// a Remote magpie that is this magpie, used anyway or left out
			// (hanslin1666 on Discord)
			if err := provider.SetKeepSelf(in.ID, in.KeepSelf); err != nil {
				fail(rw, err)
				return
			}
		case "thisdevice":
			// kept on this computer: sync neither sends the provider out nor
			// lets another computer's copy replace or remove it (treydtw on X)
			if err := provider.SetThisDevice(in.ID, in.ThisDevice); err != nil {
				fail(rw, err)
				return
			}
		case "capstop":
			// a request ends at the accounts' caps, 429, or goes on to the
			// fallbacks (plugins#349)
			if err := provider.SetCapStop(in.ID, in.CapStop); err != nil {
				fail(rw, err)
				return
			}
		case "capcareful":
			// usage read again near a cap, an account held while it can't
			// be (plugins#349)
			if err := provider.SetCapCareful(in.ID, in.CapCareful); err != nil {
				fail(rw, err)
				return
			}
		case "affinity":
			if err := provider.SetAffinity(in.ID, in.Affinity); err != nil {
				fail(rw, err)
				return
			}
		case "sink":
			if err := provider.SetSink(in.ID, in.Sink); err != nil {
				fail(rw, err)
				return
			}
		case "usefirst":
			// ahead of the other providers where Smart weighs them
			// together (hzh_1119 on Discord)
			if err := provider.SetUseFirst(in.ID, in.UseFirst); err != nil {
				fail(rw, err)
				return
			}
		case "standin":
			// never a fallback for another model's request, so a local
			// model's request can't reach a vendor that bans for it
			// (sonnnnnnnnnnnna on X), or one again
			if err := provider.SetNoStandIn(in.ID, in.NoStandIn); err != nil {
				fail(rw, err)
				return
			}
		case "delete":
			var err error
			if moved, err = agent.Reseat(func() error { return provider.Delete(in.ID) }); err != nil {
				fail(rw, err)
				return
			}
		case "teststop":
			if stop, ok := testRuns.LoadAndDelete(req.Run); ok && req.Run != "" {
				stop.(context.CancelFunc)()
			}
			rw.WriteHeader(http.StatusNoContent)
			return
		case "test":
			p, err := provider.Find(in.ID)
			if err != nil {
				fail(rw, err)
				return
			}
			// the account or key the test is pinned to (provider.TestOn;
			// tmr11235 on X: "Test on account…" was posted but never read)
			ctx := provider.TestOn(r.Context(), req.Account)
			if req.Run != "" {
				var stop context.CancelFunc
				ctx, stop = context.WithCancel(ctx)
				defer stop()
				testRuns.Store(req.Run, stop)
				defer testRuns.Delete(req.Run)
			}
			saved := *p
			if req.Typed {
				q := withCerts(typed(*p, in, req.Proxy, req.InsecureTLS, req.HTTP1), p, req.ClientCert, req.ClientKey, req.ServerCA, req.ServerPin)
				p = &q
			}
			if len(req.Test) > 0 {
				// an image model's test draws a picture, which takes longer
				ctx, cancel := context.WithTimeout(provider.TestAs(ctx, req.As), 3*time.Minute)
				defer cancel()
				writeJSON(rw, map[string]any{"results": p.TestModels(ctx, req.Test)})
				return
			}
			ctx, cancel := context.WithTimeout(provider.TestAs(ctx, req.As), 25*time.Second)
			defer cancel()
			writeJSON(rw, struct {
				Results  []provider.Result `json:"results"`
				Provider providerJSON      `json:"provider"`
			}{p.Test(ctx), providerInfo(saved, agentUses(agent.Detected(), provider.GroupFinder()))})
			return
		case "detect":
			// which APIs answer at the URL typed (Model, or one from the
			// list for each), the form as it stands: a new provider's,
			// or a saved one's with its key when none is typed — a copy
			// not saved yet with the key of the one it copies (CopyOf),
			// as its Add takes it. Nothing is written: the provider is
			// only asked, never kept, listed or routed to (#1545).
			var p provider.Provider
			from := in.ID
			if from == "" && req.New {
				from = req.CopyOf
			}
			if from != "" {
				saved, err := provider.Find(from)
				if err != nil {
					fail(rw, err)
					return
				}
				p = *saved
			}
			p = withCerts(typed(p, in, req.Proxy, req.InsecureTLS, req.HTTP1), &p, req.ClientCert, req.ClientKey, req.ServerCA, req.ServerPin)
			if strings.TrimSpace(in.Decide) != "" {
				// a decision API is asked its smallest question, at
				// POST …/systemone (#647)
				ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
				defer cancel()
				writeJSON(rw, map[string]any{"results": []provider.Detection{p.DetectDecide(ctx, req.Model)}})
				return
			}
			if len(req.DetectModels) > 0 {
				// model by model, a few at a time (01huadalang: 应该能
				// 看出来选择的模型支持情况)
				ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
				defer cancel()
				each, sum, err := p.DetectModels(ctx, req.Base, req.DetectModels)
				if err != nil {
					fail(rw, err)
					return
				}
				writeJSON(rw, map[string]any{"results": sum, "models": each})
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
			defer cancel()
			got, err := p.Detect(ctx, req.Base, req.Model, req.Picked...)
			if err != nil {
				fail(rw, err)
				return
			}
			writeJSON(rw, map[string]any{"results": got})
			return
		case "balance":
			// the editor's check of a balance as it stands in the form,
			// before a Save: the saved provider with the form's URL, field,
			// headers and token (a blank one keeping the saved)
			p, err := provider.Find(in.ID)
			if err != nil {
				fail(rw, err)
				return
			}
			q := *p
			q.BalanceURL, q.BalancePath = strings.TrimSpace(in.BalanceURL), strings.TrimSpace(in.BalancePath)
			q.BalanceMethod = in.BalanceMethod
			if req.NoBalanceProbe != nil {
				q.NoBalanceProbe = *req.NoBalanceProbe
			}
			if q.BalanceURL == "" {
				// no URL named: the check looks for one again, whatever was
				// found before (provider.ForgetBalanceProbe)
				provider.ForgetBalanceProbe(q)
			}
			if in.Headers != nil {
				q.Headers = in.Headers
			}
			if in.BalanceToken != "" {
				q.BalanceToken = in.BalanceToken
			} else if req.ClearBalanceToken {
				q.BalanceToken = ""
			}
			ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
			defer cancel()
			amount, ok, err := provider.Balance(ctx, q)
			out := map[string]any{"ok": ok, "amount": amount}
			if err != nil {
				out["error"] = err.Error()
			}
			if u, field, looked := provider.BalanceFound(q); looked {
				// asked where relays tell a key its balance: where, or that
				// neither told it
				out["looked"], out["found"], out["foundField"] = true, u, field
				out["undetected"] = u == ""
			}
			writeJSON(rw, out)
			return
		case "unfetch":
			// the vendor's list, forgotten until the next Refresh
			if err := catalog.SaveLive(in.ID, "", nil); err != nil {
				fail(rw, err)
				return
			}
		case "peekcert":
			// the certificate the form's HTTPS address shows, taken
			// unchecked, for its editor to offer it to be trusted
			// (provider.ServerPin, Zhangzhangi on X)
			var p provider.Provider
			if pr, err := provider.FromPreset(in.Preset); err == nil {
				p = pr
			}
			if old, err := provider.Find(in.ID); err == nil && in.ID != "" {
				p = *old
			}
			p = withCerts(typed(p, in, req.Proxy, req.InsecureTLS, req.HTTP1), &p, req.ClientCert, req.ClientKey, req.ServerCA, req.ServerPin)
			ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
			defer cancel()
			seen, err := p.PeekServerCert(ctx)
			if err != nil {
				fail(rw, err)
				return
			}
			writeJSON(rw, seen)
			return
		case "list":
			// the vendor's list for the form as it stands, nothing saved:
			// the add form's Fetch models, picked from before the provider
			// is (#578: 添加供应商的时候，希望添加可以获取全模型的按钮)
			var p provider.Provider
			if pr, err := provider.FromPreset(in.Preset); err == nil {
				p = pr
			}
			if p.Name == "" {
				p.Name = cmp.Or(strings.TrimSpace(in.Name), "the vendor")
			}
			p = withCerts(typed(p, in, req.Proxy, req.InsecureTLS, req.HTTP1), &p, req.ClientCert, req.ClientKey, req.ServerCA, req.ServerPin)
			ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
			defer cancel()
			all, err := p.ListAll(ctx)
			if err != nil {
				fail(rw, err)
				return
			}
			type listed struct {
				ID   string `json:"id"`
				Name string `json:"name,omitempty"`
			}
			ms := catalog.Chat(all)
			out := make([]listed, 0, len(ms))
			for _, m := range ms {
				x := listed{ID: m.ID}
				if m.Name != m.ID {
					x.Name = m.Name
				}
				out = append(out, x)
			}
			// embedding and rerank models need no pick: they are served
			// on /v1/embeddings and /v1/rerank as listed (heatonz)
			retrieval := 0
			for _, m := range all {
				if m.Serves == catalog.ServesEmbedding || m.Serves == catalog.ServesRerank {
					retrieval++
				}
			}
			writeJSON(rw, map[string]any{"models": out, "retrieval": retrieval})
			return
		case "models":
			p, err := provider.Find(in.ID)
			if err != nil {
				fail(rw, err)
				return
			}
			// as long as the plugins' host bounds one providers RPC itself
			// (30s, plugin.refreshDeadline): a plugin's Fetch models is one
			// listing of every provider, which may spend all of it (#4065)
			ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
			defer cancel()
			ask := *p
			if req.Typed {
				ask = withCerts(typed(*p, in, req.Proxy, req.InsecureTLS, req.HTTP1), p, req.ClientCert, req.ClientKey, req.ServerCA, req.ServerPin)
			}
			ms, dropped, err := ask.Refetch(ctx)
			if err != nil {
				fail(rw, err)
				return
			}
			// the picks the vendor's list no longer has are gone from
			// the saved provider: the editor takes them out of its draft
			// too, or its Save wrote them back
			if q, err := provider.Find(p.ID); err == nil {
				p = q
			}
			writeJSON(rw, struct {
				Count    int          `json:"count"`
				Dropped  []string     `json:"dropped,omitempty"`
				Provider providerJSON `json:"provider"`
			}{fetchedCount(*p, len(ms)), dropped, providerInfo(*p, agentUses(agent.Detected(), provider.GroupFinder()))})
			return
		default:
			http.NotFound(rw, r)
			return
		}
		st := providersState()
		st.Moved = moved
		writeJSON(rw, st)
		// the tray panel's account switch draws the kept list: a change
		// made here is in it at once
		keepProviders(st)
	})
	// How much of its allowance each of an agent's accounts has used.
	mux.HandleFunc("GET /api/login/usage", func(rw http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()
		// and each account's daily check-in, for its row's Check in now
		agent := r.URL.Query().Get("agent")
		writeJSON(rw, provider.LoginCheckins(agent, provider.WithCapped(provider.LoginUsageShown(ctx, agent))))
	})
	// Switching the account an agent is signed in to, among those magpie
	// remembers, and forgetting one.
	mux.HandleFunc("POST /api/login/{action}", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Agent, User string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		var change func() error
		switch r.PathValue("action") {
		case "switch":
			change = func() error {
				err := provider.SwitchLogin(in.Agent, in.User)
				if err == nil {
					provider.NoteReordered(in.Agent)
				}
				return err
			}
		case "forget":
			change = func() error { return provider.ForgetLogin(in.Agent, in.User) }
		case "on", "off":
			change = func() error { return provider.SetLoginOn(in.Agent, in.User, r.PathValue("action") == "on") }
		case "access-only", "renewed":
			// kept access token only, never renewed (EastonXvii on X), or
			// renewed by magpie again
			change = func() error {
				return provider.SetLoginAccessOnly(in.Agent, in.User, r.PathValue("action") == "access-only")
			}
		default:
			http.NotFound(rw, r)
			return
		}
		// the last account signed out or off takes the sign-in's models
		// away: the agents on them are moved to others
		moved, err := agent.Reseat(change)
		if err != nil {
			fail(rw, err)
			return
		}
		// an agent on its own models goes through magpie while more of
		// its accounts are on, and straight to its vendor again once not
		agent.SyncCatalogSoon()
		st := providersState()
		st.Moved = moved
		writeJSON(rw, st)
		keepProviders(st) // as a provider action's: the panel's switch draws it
	})
	// whether a Claude subscription's runs are Claude Code's interactive
	// mode or -p, and why -p, for the Claude Code provider page; read when
	// the page opens, not in /api/providers' polls
	mux.HandleFunc("GET /api/claude/mode", func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, gateway.ClaudeModeNow())
	})
	// Codex's background app-server, left on the account before a switch
	// or on the model list before a change (the Agents row's Restart):
	// restarting it (Codex 0.162's sessions on it reconnect, after a turn
	// running, for up to a minute; an older Codex's end), or letting it be.
	mux.HandleFunc("POST /api/codex/daemon/{action}", func(rw http.ResponseWriter, r *http.Request) {
		switch r.PathValue("action") {
		case "restart":
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
			defer cancel()
			if err := provider.RestartCodexDaemon(ctx); err != nil {
				fail(rw, err)
				return
			}
			agent.CodexDaemonRestarted()
		case "dismiss":
			provider.DismissCodexDaemon()
		case "dismiss-app":
			provider.DismissCodexApp()
		case "dismiss-projects":
			provider.DismissCodexAppProjects()
		case "restart-app":
			// the Codex app left on the account before: its app-server
			// restarted now, after the user said yes (nb5p_hQ on X)
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
			defer cancel()
			if err := agent.RestartSwitchedCodexApp(ctx); err != nil {
				fail(rw, err)
				return
			}
		default:
			http.NotFound(rw, r)
			return
		}
		writeProvidersJSON(rw, providersState())
	})
	// The Codex desktop app left on the model list before a change (the
	// Agents row's Restart): quit it and open it again.
	mux.HandleFunc("POST /api/codex/app/reopen", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Bundle string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
		defer cancel()
		if err := agent.ReopenCodexApp(ctx, in.Bundle); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, map[string]bool{"ok": true})
	})
	// A provider's several keys: add one, put one in use, name or remove it.
	mux.HandleFunc("POST /api/keys/{action}", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			ID, Key, Name, Ref string
			Refs               []string
			Protocol           provider.Protocol
			Weight             int
			Plan               string
		}
		var added, had, removed int
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		var err error
		var moved []agent.Move
		// the keys it had, so a key added is asked for its own models
		// (PrefillNewKeys)
		var held map[string]map[string]bool
		if a := r.PathValue("action"); a == "add" || a == "import" {
			held = provider.KeysHeld(in.ID)
		}
		switch r.PathValue("action") {
		case "add":
			err = provider.AddKey(in.ID, in.Name, in.Key, in.Protocol)
		case "import":
			// many keys pasted at once, one a line or comma separated
			added, had, err = provider.AddKeys(in.ID, provider.SplitKeys(in.Key), in.Protocol)
		case "protocol":
			err = provider.SetKeyProtocol(in.ID, in.Ref, in.Protocol)
		case "weight":
			err = provider.SetKeyWeight(in.ID, in.Ref, in.Weight)
		case "plan":
			// the plan a key is for (KeyAccount.Plan): its endpoints and
			// its models are that plan's
			err = provider.SetKeyPlan(in.ID, in.Ref, in.Plan)
		case "use":
			err = provider.UseKey(in.ID, in.Ref)
		case "remove":
			// the last key gone, or off, takes the provider's models away
			moved, err = agent.Reseat(func() error { return provider.RemoveKey(in.ID, in.Ref) })
		case "remove-many":
			// the keys picked in a long list, or every dead one, at once
			moved, err = agent.Reseat(func() (err error) { removed, err = provider.RemoveKeys(in.ID, in.Refs); return err })
		case "rename":
			err = provider.RenameKey(in.ID, in.Ref, in.Name)
		case "replace":
			err = replaceKey(in.ID, in.Ref, in.Key)
		case "on", "off":
			moved, err = agent.Reseat(func() error { return provider.SetKeyOn(in.ID, in.Ref, r.PathValue("action") == "on") })
		default:
			http.NotFound(rw, r)
			return
		}
		if err != nil {
			fail(rw, err)
			return
		}
		// a key that now takes requests has its models asked for: which
		// models a key sees is known only from its own list, and an off
		// key's isn't asked (#76), so until then a relay that hands out a
		// key per group would send the key nothing, or everything
		if a := r.PathValue("action"); a == "add" || a == "import" || a == "on" || a == "plan" {
			if p, err := provider.Find(in.ID); err == nil && p.Ready() && !p.Off && (len(p.KeysOn()) > 1 || a == "plan") {
				ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
				// and a key added is given the models its own list has
				// beside it, in the same wait (Young on Discord)
				var wg sync.WaitGroup
				if held != nil {
					wg.Go(func() { provider.PrefillNewKeys(ctx, held, in.ID) })
				}
				p.Fetch(ctx)
				wg.Wait()
				cancel()
			}
		}
		st := providersState()
		st.Moved, st.Added, st.Had, st.Removed = moved, added, had, removed
		writeJSON(rw, st)
	})
	// Adding a subscription: magpie opens the vendor's sign-in in the
	// browser and the window follows it until the account is in.
	mux.HandleFunc("POST /api/signin", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Agent, Site string // Site: ZCode's "zai" or "bigmodel"
			// Proxy is the one picked to sign in through, kept as the
			// account's (#1663); none picked, the one its requests go through
			Proxy *string
			User  string // the account signed in to again
			// LinkOnly keeps this sign-in to its link: the page is not opened
			// here, to be copied to a browser on another computer
			LinkOnly bool
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		st, err := provider.StartSignInWith(in.Agent, provider.SignInOptions{Site: in.Site, Proxy: in.Proxy, User: in.User})
		if err != nil {
			fail(rw, err)
			return
		}
		// one that installs a CLI first has no URL yet: the window opens it
		// once there is one
		writeJSON(rw, openSignIn(w, st, in.LinkOnly))
	})
	mux.HandleFunc("GET /api/signin/{id}", func(rw http.ResponseWriter, r *http.Request) {
		st, ok := provider.SignInStatus(r.PathValue("id"))
		if !ok {
			http.NotFound(rw, r)
			return
		}
		writeJSON(rw, st)
	})
	mux.HandleFunc("POST /api/signin/{id}/cancel", func(rw http.ResponseWriter, r *http.Request) {
		provider.CancelSignIn(r.PathValue("id"))
		rw.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /api/signin/{id}/callback", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ URL string }
		if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if err := provider.SubmitSignInCallback(r.PathValue("id"), in.URL); err != nil {
			fail(rw, err)
			return
		}
		rw.WriteHeader(http.StatusNoContent)
	})
	// A Claude account from a setup-token (`claude setup-token`), which
	// Claude Code takes in CLAUDE_CODE_OAUTH_TOKEN: nothing is asked of
	// Anthropic, and the token is never echoed back.
	mux.HandleFunc("POST /api/signin/claude-token", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Token string }
		if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		user, err := provider.AddClaudeSetupToken(in.Token)
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, map[string]string{"user": user})
	})
	// Accounts brought in from another tool's export (Antigravity's, from
	// Antigravity Cockpit, Antigravity Manager, CLIProxyAPI; ChatGPT's and
	// Claude's from CLIProxyAPI or the agents' own files), each file's
	// text as it is; each checked with the vendor before it is kept.
	mux.HandleFunc("POST /api/signin/import", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Agent string
			Files []string
			// ChatGPT accounts kept with the file's access token only,
			// never renewed (EastonXvii on X)
			AccessOnly bool
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
		defer cancel()
		imp := provider.ImportGoogleAccounts
		if in.Agent == "codex" || in.Agent == "claude" {
			// ChatGPT and Claude sign-ins: CLIProxyAPI's auth files, Codex
			// CLI's auth.json, Claude Code's .credentials.json
			imp = provider.ImportLogins
			if in.AccessOnly && in.Agent == "codex" {
				imp = func(ctx context.Context, _ string, files []string) ([]provider.ImportedAccount, error) {
					return provider.ImportCodexAccessOnly(ctx, files)
				}
			}
		} else if in.Agent == "factory" {
			// Factory API keys (fk-…), as droid takes FACTORY_API_KEY (#506)
			imp = func(ctx context.Context, _ string, files []string) ([]provider.ImportedAccount, error) {
				return provider.ImportFactoryKeys(ctx, files)
			}
		}
		// the address a sign-in's browser ended on, pasted here rather than
		// in the sign-in's Callback URL field, finishes that sign-in: it is
		// no account file, and never goes to the vendor as a token
		res, rest := provider.PastedSignIns(in.Files)
		if len(rest) > 0 {
			more, err := imp(ctx, in.Agent, rest)
			if err != nil && len(res) == 0 {
				fail(rw, err)
				return
			}
			if err != nil {
				more = []provider.ImportedAccount{{Status: "failed", Error: err.Error()}}
			}
			res = append(res, more...)
		}
		agent.SyncCatalogSoon()
		writeJSON(rw, struct {
			Results   []provider.ImportedAccount `json:"results"`
			Providers providersJSON              `json:"providers"`
		}{res, providersState()})
	})
	// ChatGPT and Claude accounts taken out as a file Sub2API, CLIProxyAPI
	// or magpie imports (heyiwei5 and Rk1ngggg on X). The file holds live
	// sign-ins: it is written 0600 into Downloads, never sent to the page,
	// and its tokens are never logged.
	mux.HandleFunc("POST /api/signin/export", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Agent, Format string
			Users         []string
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		res, err := provider.ExportLogins(in.Agent, in.Format, in.Users)
		if err != nil {
			fail(rw, err)
			return
		}
		dir := downloads()
		stamp := time.Now().Format("2006-01-02")
		if in.Format == provider.ExportCLIProxy {
			// one file an account, in a folder of their own
			base := filepath.Join(dir, "magpie-cliproxyapi-"+in.Agent+"-"+stamp)
			dir = base
			for i := 2; ; i++ {
				if _, err := os.Stat(dir); err != nil {
					break
				}
				dir = fmt.Sprintf("%s-%d", base, i)
			}
		} else {
			res.Files[0].Name = "magpie-sub2api-" + in.Agent + "-" + stamp + ".json"
		}
		paths, err := provider.WriteExport(res, dir)
		if err != nil {
			fail(rw, err)
			return
		}
		shown := dir
		if len(paths) == 1 {
			shown = paths[0]
		}
		_ = w.OpenFolder(dir) // saved either way; the path is in the answer
		writeJSON(rw, struct {
			Path     string                     `json:"path"`
			Accounts []string                   `json:"accounts"`
			Skipped  []provider.ImportedAccount `json:"skipped"`
		}{tilde(shown), res.Accounts, res.Skipped})
	})
	// StepFun's Step Plan windows: the session the user copied from their
	// browser with magpie's bookmarklet, the provider's own (two on one
	// site can be two accounts, #2384), on the provider's platform
	stepfunOf := func(rw http.ResponseWriter, r *http.Request) (string, string, bool) {
		p, err := provider.Find(r.PathValue("id"))
		if err != nil || provider.StepFunSite(*p) == "" {
			http.NotFound(rw, r)
			return "", "", false
		}
		return p.ID, provider.StepFunSite(*p), true
	}
	mux.HandleFunc("POST /api/stepfun/{id}/session", func(rw http.ResponseWriter, r *http.Request) {
		id, site, ok := stepfunOf(rw, r)
		if !ok {
			return
		}
		var in struct{ Text string }
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		if err := provider.SaveStepFunPaste(ctx, id, site, in.Text); err != nil {
			fail(rw, err)
			return
		}
		writeProvidersJSON(rw, providersState())
	})
	mux.HandleFunc("POST /api/stepfun/{id}/signout", func(rw http.ResponseWriter, r *http.Request) {
		id, _, ok := stepfunOf(rw, r)
		if !ok {
			return
		}
		if err := provider.SignOutStepFun(id); err != nil {
			fail(rw, err)
			return
		}
		writeProvidersJSON(rw, providersState())
	})
	// the page copies through here first: in the app's window the
	// clipboard API is refused or missing, depending on the system
	mux.HandleFunc("POST /api/copy", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Text string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.Text == "" || !w.Copy(in.Text) {
			http.Error(rw, "", http.StatusNotImplemented)
			return
		}
		rw.WriteHeader(http.StatusNoContent)
	})
	// the add sheet counts what it showed of the partners and what was
	// opened of them (provider.CountPartner); only listed partners count.
	// Those shown are no longer new (provider.NoticePartners).
	mux.HandleFunc("POST /api/partner", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			What string
			IDs  []string
		}
		_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in)
		if len(in.IDs) <= provider.MaxPartners {
			provider.CountPartner(in.What, in.IDs...)
			if in.What == provider.PartnerShown {
				provider.NoticePartners(in.IDs...)
			}
		}
		rw.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /api/open", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ URL string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		// and System Settings' Login Items, where Open at login is allowed
		// again: that one pane, not System Settings' scheme at large; and
		// the Add sheet's "Become a sponsor", that one address, not mailto:
		if in.URL == sponsorMail || strings.HasPrefix(in.URL, "https://") || strings.HasPrefix(in.URL, "http://") || strings.HasPrefix(in.URL, agent.CindyScheme) || strings.HasPrefix(in.URL, agent.LodyScheme) || strings.HasPrefix(in.URL, agent.AionUiScheme) || strings.HasPrefix(in.URL, agent.CherryStudioScheme) || in.URL == autostart.LoginItemsURL {
			open := w.OpenURL
			// an app's import link is handed over only when something
			// here opens it: the system says nothing when nothing does,
			// and Add magpie did nothing at all (sungt, 10-29)
			app := func(u string) error { return appLink(u, w.OpenURL) }
			switch {
			case strings.HasPrefix(in.URL, agent.LodyScheme):
				// Lody on Linux is run with the link, which xdg-open
				// turned away (agent.OpenLody)
				open = func(u string) error { return agent.OpenLody(u, app) }
			case strings.HasPrefix(in.URL, agent.CindyScheme), strings.HasPrefix(in.URL, agent.AionUiScheme), strings.HasPrefix(in.URL, agent.CherryStudioScheme):
				open = app
			}
			// Open again on a sign-in says why it didn't open (#2043)
			if err := open(in.URL); err != nil {
				fail(rw, err)
				return
			}
		}
		rw.WriteHeader(http.StatusNoContent)
	})
}

// testRuns: the provider tests under way by the name the window gave each
// (Run), each with what ends it: Stop ends the test's requests to the
// vendor, here as well as by the fetch it aborts, which WebView2 doesn't
// end the request's context for (RIck on Discord: a test couldn't be
// stopped)
var testRuns sync.Map

// typed is the saved provider p as the editor's form has it (in), for
// Refresh and Test models before a Save: a key pasted to replace the saved
// one is the one asked with, and the URLs, headers, extra body and proxy
// typed are where and how. Nothing is saved. What the form left blank (the
// key above all) is the saved one's; the provider's other keys are kept. A
// Vertex AI provider's project, location, credentials file and service
// account are the ones typed, spelled as a Save keeps them, unless no
// project is.
func typed(p, in provider.Provider, proxy *string, insecureTLS, http1 *bool) provider.Provider {
	if k := strings.TrimSpace(in.Key); k != "" && k != p.Key {
		p.Key, p.KeyName, p.KeyProtocol, p.KeyWeight, p.KeyPlan = k, "", "", 0, ""
	}
	if v := in.Vertex.Normal(); p.IsVertex() && (v != nil && v.Project != "" || in.Vertex != nil && p.Key != "") {
		p.Vertex = v // with a key the project may be none: express mode
	}
	for _, f := range []struct {
		to *string
		v  string
	}{{&p.Chat, in.Chat}, {&p.Responses, in.Responses}, {&p.Anthropic, in.Anthropic}, {&p.Gemini, in.Gemini}, {&p.Decide, in.Decide}, {&p.ModelsURL, in.ModelsURL}} {
		if v := strings.TrimSpace(f.v); v != "" {
			*f.to = v
		}
	}
	if in.Headers != nil {
		p.Headers = in.Headers
	}
	if in.ExtraBody != nil {
		p.ExtraBody = in.ExtraBody
	}
	if proxy != nil {
		p.Proxy = *proxy
	}
	// asked unsaved as it would be saved: Via takes it only when p is the
	// user's own (TakesInsecureTLS)
	if insecureTLS != nil {
		p.InsecureTLS = *insecureTLS
	}
	if http1 != nil {
		p.HTTP1 = *http1
	}
	return p
}

// withCerts is p with the client certificate, key and server CA a save
// or an unsaved test typed (#5596), each one left out kept from old (nil:
// none); a ~ is taken as home, as a save keeps it, so an unsaved test
// reads the same files.
func withCerts(p provider.Provider, old *provider.Provider, cert, key, ca, pin *string) provider.Provider {
	var kept provider.Provider
	if old != nil {
		kept = *old
	}
	pick := func(typed *string, was string) string {
		if typed == nil {
			return was
		}
		v, err := provider.CertPath(*typed)
		if err != nil {
			return strings.TrimSpace(*typed) // the save says why
		}
		return v
	}
	p.ClientCert = pick(cert, kept.ClientCert)
	p.ClientKey = pick(key, kept.ClientKey)
	p.ServerCA = pick(ca, kept.ServerCA)
	p.ServerPin = kept.ServerPin
	if pin != nil {
		p.ServerPin = strings.TrimSpace(*pin) // Save keeps it as netproxy.NormalPin has it, or says why
	}
	return p
}

// queueOf is a save's queueLimit or queueWait: keep when the save left it
// out, 0 for null, else the number.
func queueOf(raw json.RawMessage, what string) (n int, keep bool, err error) {
	if len(raw) == 0 {
		return 0, true, nil
	}
	var v *int
	if err := json.Unmarshal(raw, &v); err != nil {
		return 0, false, fmt.Errorf("the %s must be a whole number, not %s", what, raw)
	}
	if v == nil {
		return 0, false, nil
	}
	return *v, false, nil
}

// priceRateOf is a save's priceRate: keep when the save left it out, 0
// for null, else the rate.
func priceRateOf(raw json.RawMessage) (r float64, keep bool, err error) {
	if len(raw) == 0 {
		return 0, true, nil
	}
	var n *float64
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0, false, fmt.Errorf("a price rate is a number, like 0.8, not %s", raw)
	}
	if n == nil {
		return 0, false, nil
	}
	if bad := provider.PriceRateOK(*n); bad != "" {
		return 0, false, fmt.Errorf("%s, not %v", bad, *n)
	}
	return *n, false, nil
}

// rateWindowsOf is a save's rateWindows: keep when the save left it out,
// none for null or an empty list, else the windows as checked
// (CheckRateWindows).
func rateWindowsOf(raw json.RawMessage) (ws []provider.RateWindow, keep bool, err error) {
	if len(raw) == 0 {
		return nil, true, nil
	}
	var list *[]provider.RateWindow
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, false, fmt.Errorf("rate windows are a list of windows, not %s", raw)
	}
	if list == nil || len(*list) == 0 {
		return nil, false, nil
	}
	if bad := provider.CheckRateWindows(*list); bad != "" {
		return nil, false, errors.New(bad)
	}
	return *list, false, nil
}

// rateNowOf is what p bills at at t (#2463): its price rate times the
// rate of the window holding t, 0 when that comes to 1× or p has no
// windows (a price rate alone is the editor's to show).
func rateNowOf(p provider.Provider, t time.Time) float64 {
	if len(p.RateWindows) == 0 {
		return 0
	}
	r := 1.0
	if p.PriceRate > 0 {
		r = p.PriceRate
	}
	if w := p.RateAt(t); w > 0 {
		r *= w
	}
	if r == 1 {
		return 0
	}
	return r
}

// concurrencyOf is a save's maxConcurrency: keep when the save left it
// out, nil for null (the plugin's, or none), else the number, 0 for none.
func concurrencyOf(raw json.RawMessage) (n *int, keep bool, err error) {
	return limitOf(raw, "max concurrent requests")
}

// limitOf is a save's limit named what, read as concurrencyOf reads
// maxConcurrency: keep when left out, nil for null, else 0 to 1000.
func limitOf(raw json.RawMessage, what string) (n *int, keep bool, err error) {
	if len(raw) == 0 {
		return nil, true, nil
	}
	if err := json.Unmarshal(raw, &n); err != nil {
		return nil, false, fmt.Errorf("%s must be a whole number, not %s", what, raw)
	}
	if n != nil && (*n < 0 || *n > 1000) {
		return nil, false, fmt.Errorf("%s must be from 0 to 1000, not %d", what, *n)
	}
	return n, false, nil
}

// fetchedCount is the number of models a refetch says it found: the
// models the editor lists from it, not the vendor's ids. Antigravity lists
// a model once per thinking level, which the editor shows as one model
// with those levels (and Cursor and Devin fold theirs the same way), so
// "Antigravity: 23 models" sat over 17 chips (#2069). The ids counted
// stand when the provider offers none.
func fetchedCount(p provider.Provider, ids int) int {
	if n := len(p.Available()); n > 0 {
		return n
	}
	return ids
}
