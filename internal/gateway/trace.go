package gateway

// The routing trace: what routing did with each request, as it did it, for
// the Gateway view to play back. Every account or key in the order it was
// weighed and what put it there — the share of its allowance used and when
// that renews, the tokens it served lately, whose turn it was, why it
// rests — then each try, what it answered, and how long one that failed
// now sits out. It's recorded where the decisions are made, from the same
// values they are made with; nothing is worked out again afterwards.

import (
	"context"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// traceKeep is how many requests the trace keeps.
const traceKeep = 60

// Route is one request's way through routing.
// ModelMove is a stand-in that put a request on another model than the one
// it named: To, the model it went on (before a routing group's pick), and
// Why, a word the Routing page says it by. A Desktop's own call (desktopMove's
// reasons), Claude Code's tiers (movedClaudeTier, movedClassifier), an
// agent's configured stand-in (movedStandIn), Codex's last turn's model
// (movedCodexTurn) and Privacy's unmasked route (movedPrivacy). Without it
// a row read "asked for chatflowsai/claude-opus-5-5 → agentrouter" with
// nothing to say a title, not the user's turn, was put on the chat's model
// (Neo on Discord).
type ModelMove struct {
	To  string `json:"to"`
	Why string `json:"why"`
}

type Route struct {
	// CallerKey: the gateway key the request came with, by its id and the
	// name it had then, never its secret: a key held to a budget, models
	// or accounts is told the routes of its own requests alone
	// (sessionLatest, #1981), and the Routing page groups the requests by
	// key (acezan on Discord). None for a request without one.
	CallerKey     string `json:"callerKey,omitempty"`
	CallerKeyName string `json:"callerKeyName,omitempty"`
	imageTurn     string
	imageCaller   string
	imageProvider string
	// turn: a request of an agent's turn, for the notification that it is
	// done (turns); dirOf, the folder its body says, read once as a turn
	// starts; tools, whether its reply called a tool; offFeed, it left
	// the trace unfinished (drop, trim) and the route-events feed was
	// told it went, so it hears no more of it
	turn    bool
	offFeed bool
	dirOf   func() string
	tools   *toolScan
	// window: the context window of the model the try under way went to,
	// known as it goes out, so a request still answering shows how full
	// its window is (livePrompt) and not only once it is done
	window        int
	Seq           int64        `json:"seq"` // the trace's count when it last changed
	ID            int64        `json:"id"`
	Time          time.Time    `json:"time"`
	Agent         string       `json:"agent"`
	ParentSession string       `json:"parentSession,omitempty"` // title helper's explicit originating chat; does not affect routing
	Session       string       `json:"session,omitempty"`       // the client's session id, never inferred from its model or account
	TitleLink     *TitleLink   `json:"titleLink,omitempty"`     // digests only; display evidence, never account affinity
	ParentMatched bool         `json:"parentMatched,omitempty"` // parent inferred for this view, not supplied by Codex
	Usage         []RouteUsage `json:"usage,omitempty"`         // token tiers of billable tries; priced when read
	Kind          string       `json:"kind,omitempty"`          // what the call is for, as Call's
	For           *CallFor     `json:"for,omitempty"`           // the request it was made for, as Call's
	Model         string       `json:"model"`                   // as the agent asked
	Effort        string       `json:"effort,omitempty"`        // the reasoning the agent asked for; "" for none
	Tier          string       `json:"tier,omitempty"`          // the service tier the agent asked for (bodyTier); "" for none
	Provider      string       `json:"provider"`                // the provider the model resolved to
	Group         *GroupRef    `json:"group,omitempty"`         // the routing group the agent asked for
	Rule          *RuleHit     `json:"rule,omitempty"`          // the group's rules for it, when it has any
	SealedTask    bool         `json:"sealedTask,omitempty"`    // only ChatGPT accounts can read this subagent's task
	LeadAccount   string       `json:"leadAccount,omitempty"`   // the parent account put first for a sealed task
	// Compaction: the key or account that sealed the native compaction
	// the request carries, the only one it went to (#1099): Routing says
	// so, not that it is the only one on for the model (#2639)
	Compaction string `json:"compaction,omitempty"`
	// Subagent is a Codex or Claude Desktop subagent's request put on
	// the model set for its subagents, or kept on the one asked for and why
	Subagent *SubagentPick `json:"subagent,omitempty"`
	// Moved: the request was served on another model than Model names,
	// by one of magpie's stand-ins, and why (ModelMove); nil when it went
	// where it asked, or to a routing group by its name
	Moved *ModelMove `json:"moved,omitempty"`
	// Sub: the Claude Code subagent the request is of (subagentOf), whose
	// session header is its conversation's; "" for the conversation's own
	Sub string `json:"sub,omitempty"`
	// SubagentEffort is Desktop's configured effort. Tries keep the actual
	// level after member overrides and fitting to the model.
	SubagentEffort string `json:"subagentEffort,omitempty"`
	// SubagentID is the Claude Code subagent whose turn this is
	// (subagentTurnOf), the id the ledger's Subagent keeps too. Claude Code
	// names no kind for it, so Kind stays empty and the Routing page tags
	// it as Codex's subagents are.
	SubagentID string `json:"subagentId,omitempty"`
	// Nested: the rules of the groups in the group, down the way to the
	// one that went first, each as it decided
	Nested   []NestedRule `json:"nested,omitempty"`
	Affinity *Affinity    `json:"affinity,omitempty"` // its conversation, and whether it stayed put
	Pinned   string       `json:"pinned,omitempty"`   // the account AccountHeader named: only it was tried
	Order    []Weighed    `json:"order"`              // who was to try it, first first
	Left     []Weighed    `json:"left,omitempty"`
	Tries    []Try        `json:"tries"`
	// Masked: what masking swapped in the request before it went, nil
	// for nothing (noteMasked)
	Masked *Masked `json:"masked,omitempty"`
	Done   bool    `json:"done"`
	Status int     `json:"status,omitempty"`
	Error  string  `json:"error,omitempty"`
	// Fail: why the failure the agent was told failed (toldFail), set
	// when it was an earlier try's than the last (Try.Earlier, or every
	// account after it passed over by a middleware): the request's row
	// pairs Status with it, not with the last try's own Fail (danzi002 on
	// Discord: an earlier account's 529 server_is_overloaded read
	// "529 · quota used up" beside the last account's usage limit)
	Fail string `json:"fail,omitempty"`
	// Canceled: the agent canceled the request before it was answered
	// (usage.CanceledBy). Its agent saw no error, so it isn't one of
	// the errors the page counts. Set as it is done, and as a day's
	// history is read, for a route kept before it was
	Canceled bool  `json:"canceled,omitempty"`
	Millis   int64 `json:"ms,omitempty"`
	Tokens   int   `json:"tokens,omitempty"`
	Output   int   `json:"out,omitempty"` // of Tokens, the reply's
	// Reasoning: of Output, the reply's reasoning, which its speed leaves
	// out (usage.DecodeOf)
	Reasoning int `json:"reasoning,omitempty"`
	// TTFT: ms from the request to its reply's first content (text,
	// reasoning or a tool call), FirstText to its first text, as Millis
	// counts: streamed replies only (#196)
	TTFT      int64 `json:"ttft,omitempty"`
	FirstText int64 `json:"firstText,omitempty"`
	// FirstReasoning is the start of paced visible reasoning, absent for hidden reasoning.
	FirstReasoning  int64 `json:"firstReasoning,omitempty"`
	ReasoningHidden bool  `json:"reasoningHidden,omitempty"`
	// Flow: the ms its content took to come (Call.Flow)
	Flow int64 `json:"flow,omitempty"`
	// Sent: ms from the request to its answering try's request written
	// to the vendor, and of it Own magpie's, Retried the tries that
	// failed first, Queued waiting for a slot, as the usage record has
	// them (Record.Sent); the rest of Sent is connecting and sending
	Sent    int64 `json:"sent,omitempty"`
	Own     int64 `json:"own,omitempty"`
	Retried int64 `json:"retried,omitempty"`
	Queued  int64 `json:"queued,omitempty"`
	// Phase: "redact" while it is being masked and not yet done
	// (noteRedacting), so its row says masking rather than routing;
	// Masking: the ms of Own that went on masking it, all of it when Own
	// is unknown (maskingMs)
	Phase   string `json:"phase,omitempty"`
	Masking int64  `json:"masking,omitempty"`
	// Bytes, ContentAt and EstTok: the reply as it reaches the agent
	// (live.go), the bytes it has had, the epoch ms content last came, and
	// the output tokens estimated from it, 0 while its pair has no rate
	Bytes     int64 `json:"bytes,omitempty"`
	ContentAt int64 `json:"contentAt,omitempty"`
	EstTok    int   `json:"estTok,omitempty"`
	// Held: the file name, in HeldDir, of the report saved of a request
	// magpie held too long before routing it and stopped (giveUp)
	Held string `json:"held,omitempty"`
	// Served: the model the reply says answered, as the last try has it;
	// Swapped: another than the one that try asked for; Routed: that try
	// asked another magpie's routing group, and Served is its member
	Served  string `json:"served,omitempty"`
	Swapped bool   `json:"swapped,omitempty"`
	Routed  bool   `json:"routed,omitempty"`
	// Upstream: the provider an aggregator said answered behind it
	Upstream string `json:"upstream,omitempty"`
	// Prompt: what the prompt held, part by part, and the window it went
	// into (prompt.go)
	Prompt *Prompt `json:"prompt,omitempty"`
	// Conv: the conversation a request without a session id is part of,
	// as a digest of its first user turn (conversationID)
	Conv string `json:"conv,omitempty"`
	// ResponseID: the id of the reply object actually released to the
	// client, as the client read it, set when the route is done. The agent
	// stores that id with the reply (dsh's replay state), so a request
	// whose agent keeps it can be found again after the fact even when its
	// conversation opening is one a fork copied (dshConvs)
	ResponseID string `json:"responseId,omitempty"`
	// LastConvs: the ids of the user messages the request's turn carries,
	// newest first (dshLastConvs): a dsh fork copies its source's opening,
	// so both answer the same Conv, and a request still answering has no
	// reply id yet. The turn's messages are what say which branch it is
	// of, as that branch's own file has them (sessions' TurnConvs, gui's
	// ownerLast). Only a live route needs them: the history keeps none
	// (saveRoute)
	LastConvs []string `json:"lastConvs,omitempty"`
	// Folder: the last two levels of the folder its agent says it works
	// in (shortFolder), never the whole path; a request that says none
	// takes its session's, as said last (#1068)
	Folder string `json:"folder,omitempty"`
	// Archive: "<date>/<id>", where the request archive keeps the call,
	// when it was on (Call.Archive), for the page to read its bodies back
	// once the recent calls have let them go
	Archive string `json:"archive,omitempty"`
}

// RouteUsage is one billable attempt's pricing inputs, kept in routing history.
// Its JSON keys also read the earlier history that stored full usage records.
type RouteUsage struct {
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	Input      int    `json:"in"`
	Output     int    `json:"out"`
	CacheRead  int    `json:"cache_read,omitempty"`
	CacheWrite int    `json:"cache_write,omitempty"`
	Reasoning  int    `json:"reasoning,omitempty"`
	// CacheUnknown: the vendor counted no cache tokens, so the Routing
	// page shows no hit rate rather than 0% (Kiro, zeewell on X)
	CacheUnknown bool `json:"cache_unknown,omitempty"`
	// Credits is what the reply said the request cost in its plan's
	// credits (WorkBuddy's usage.credit, #2512), the Routing page's as
	// the Usage page's; nil when it said none (jiabubu on Discord)
	Credits *float64 `json:"credits,omitempty"`
}

// PricingRecord lets the routing view reuse the ledger's effective prices.
func (u RouteUsage) PricingRecord() usage.Record {
	return usage.Record{Provider: u.Provider, Model: u.Model, Input: u.Input,
		Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite, Reasoning: u.Reasoning}
}

// GroupRef is the routing group a request asked for.
type GroupRef struct {
	ID string `json:"id"`
	// Ref: the id agents pick it by (provider.Group.Ref), auto/<model>
	// for a group magpie found, which the Routing page names it by
	Ref      string   `json:"ref,omitempty"`
	Name     string   `json:"name"`
	Routing  string   `json:"routing"`
	Affinity string   `json:"affinity"`
	Auto     bool     `json:"auto,omitempty"`
	Sink     bool     `json:"sink,omitempty"` // provider.Group.Sink, as it applies
	Members  []string `json:"members"`        // those ready, as provider/model[:effort fixed on it]
	// Subs: the groups in the group, at any depth, outermost first
	Subs []SubGroup `json:"subs,omitempty"`
	// Via: for each of Members, the groups in the group it is of, as
	// "fast>cheap" ("" for the group's own), when it has groups in it
	Via []string `json:"via,omitempty"`
	// Fast: those of Members sent in their vendor's fast mode
	Fast []string `json:"fast,omitempty"`
	// Paused: the models a pause rule left out of the request, so not in
	// Members (provider.PausedOut)
	Paused []provider.Paused `json:"paused,omitempty"`
}

// SubGroup is a routing group in the group a request asked for.
type SubGroup struct {
	ID      string `json:"id"`
	Ref     string `json:"ref,omitempty"` // as GroupRef's
	Name    string `json:"name"`
	Routing string `json:"routing"`
	In      string `json:"in"`              // the group it is in
	Rules   int    `json:"rules,omitempty"` // how many rules it has
}

// NestedRule is a group in the group's rules, as they decided.
type NestedRule struct {
	Group string   `json:"group"`
	Name  string   `json:"name"`
	Rule  *RuleHit `json:"rule"`
}

// groupRef is the trace's g, with its models ms.
func groupRef(g provider.Group, ms []provider.Member) *GroupRef {
	ref := &GroupRef{ID: g.ID, Ref: g.Ref(), Name: g.Name, Routing: g.Routing, Affinity: g.Affinity, Auto: g.Auto, Sink: sinks(g.Sinks(), g.Routing)}
	seen := map[string]bool{}
	for _, m := range ms {
		ref.Members = append(ref.Members, m.Spelled())
		if m.Fast {
			ref.Fast = append(ref.Fast, ref.Members[len(ref.Members)-1])
		}
		ref.Via = append(ref.Via, strings.Join(m.Groups(), ">"))
		in := g.ID
		for _, v := range m.Via {
			if !seen[v.ID] {
				seen[v.ID] = true
				ref.Subs = append(ref.Subs, SubGroup{ID: v.ID, Ref: v.Ref(), Name: v.Name, Routing: v.Routing, In: in, Rules: len(v.Rules)})
			}
			in = v.ID
		}
	}
	if len(ref.Subs) == 0 {
		ref.Via = nil
	}
	return ref
}

// SeatPrice is what a weighed one's model costs on it at the request's
// time, as Cheapest first went by it: a million tokens each, in the
// catalog's dollars, Peak when that is its vendor's peak-hour price.
type SeatPrice struct {
	Input     float64 `json:"input"`
	Output    float64 `json:"output"`
	CacheRead float64 `json:"cacheRead,omitempty"`
	Peak      bool    `json:"peak,omitempty"`
}

// Weighed is one account or key as routing weighed it.
type Weighed struct {
	ID       string  `json:"id"` // what rests after a failure
	Provider string  `json:"provider"`
	Name     string  `json:"name"` // the provider's
	Icon     string  `json:"icon,omitempty"`
	Preset   string  `json:"preset,omitempty"`
	Who      string  `json:"who,omitempty"` // the account, or the key's name or its masked self
	Kind     string  `json:"kind"`          // "account", "key", or "provider" when it has one
	Agent    string  `json:"agent,omitempty"`
	Plan     string  `json:"plan,omitempty"`
	Model    string  `json:"model"`
	Fixed    string  `json:"fixed,omitempty"` // the effort the group's member it is of is fixed at
	Fast     bool    `json:"fast,omitempty"`  // the group's member it is of is sent fast
	Routing  string  `json:"routing"`         // its provider's: "", order, rotate, usage
	Fallback bool    `json:"fallback,omitempty"`
	Shared   bool    `json:"shared,omitempty"` // its provider has more than one on
	Known    bool    `json:"known,omitempty"`  // the vendor said what the account has left
	Learns   bool    `json:"learns,omitempty"` // not known, but its answer will tell
	Used     float64 `json:"used"`             // share of the allowance counting the model, used
	// WeekLow: Used is at 90% or more for its week alone, so Smart and Use
	// before expiry keep it with those that have room rather than for
	// last (routes.Seat.WeekLow); false under another routing
	WeekLow bool `json:"weekLow,omitempty"`
	// Amount of Limit in Unit: the count that share is of, when the
	// vendor counts it so (WorkBuddy's credits, #659)
	Amount float64 `json:"amount,omitempty"`
	Limit  float64 `json:"limit,omitempty"`
	Unit   string  `json:"unit,omitempty"`
	// Balance is a prepaid wallet's left in Unit (left.bal, #3950): the
	// Routing page tells the figure, not a share
	Balance *float64    `json:"balance,omitempty"`
	Renews  []time.Time `json:"renews,omitempty"` // when those windows renew, the biggest first
	// Soon is Renews as Smart sorted by them (left.soon,
	// Allowance.Renewal): a credit pack's end where it comes before its
	// window's reset, an auto-used reset, a window not started taken to
	// run its span from now. Lapse is when the soonest credits left are
	// lost (left.lapse), Smart's tie-break: the Routing page tells the
	// order by these, the keys it was sorted by (#5646)
	Soon     []time.Time       `json:"soon,omitempty"`
	Lapse    *time.Time        `json:"lapse,omitempty"`
	Pace     float64           `json:"pace,omitempty"`     // remaining allowance per hour until its window resets
	Due      *time.Time        `json:"due,omitempty"`      // when the allowance window used for pace resets
	Tokens   float64           `json:"tokens,omitempty"`   // least used: tokens it served lately
	Free     bool              `json:"free,omitempty"`     // its model costs nothing there (free first)
	Expires  *time.Time        `json:"expires,omitempty"`  // free quota by expiry: when a free one's quota is first lost
	LateTTFT float64           `json:"lateTtft,omitempty"` // speed first: its ms to the first token lately, 0 if not measured (speed.go)
	LateTPS  float64           `json:"lateTps,omitempty"`  // speed first: its tokens a second lately, 0 if not told
	Price    *SeatPrice        `json:"price,omitempty"`    // cheapest first: what its model costs on it now, nil if not known
	Turn     bool              `json:"turn,omitempty"`     // in turn: it was this one's turn
	Fit      int               `json:"fit,omitempty"`      // keyFit
	Speaks   provider.Protocol `json:"speaks,omitempty"`   // a key made for one protocol only
	Rest     *Rest             `json:"rest,omitempty"`     // resting after a failure, when the request came
	Unlisted bool              `json:"unlisted,omitempty"`
	// UseFirst: its provider is set to be used first, which Smart puts
	// ahead of the other providers' (provider.Provider.UseFirst)
	UseFirst bool `json:"useFirst,omitempty"`
	// Window: left out as the request is longer than this, its model's
	// context window (roomy), unless every one with room is out of its
	// allowance (#3749)
	Window int `json:"window,omitempty"`
	// DueBy: "reset" when Due is when an auto-used Codex reset about to
	// run out starts the windows again, sooner than they renew (#717);
	// "expiry" when it is when credits in them expire, sooner than they
	// renew (Allowance.Pace)
	DueBy string `json:"dueBy,omitempty"`
	// Unreached: how many of Renews' windows, the biggest first, Smart
	// passes over, as what they have left can't be used up before they
	// renew unless the next one's is used first: the next one ranks it
	// (left.unreached, #5639)
	Unreached int `json:"unreached,omitempty"`
	// Restarts: when that reset starts them again, set when it is sooner
	// than its biggest window renews — Smart goes by it then (#718)
	Restarts *time.Time `json:"restarts,omitempty"`
	// Barred: left out as the user set it not to serve the model, its
	// own list of models leaving it out (#474)
	Barred bool `json:"barred,omitempty"`
	// Held: left out as the gateway key asking may not use its account or
	// key (#905), or a routing group it was reached through is held to
	// other accounts (Group.Accounts): HeldBy names that group
	Held   bool   `json:"held,omitempty"`
	HeldBy string `json:"heldBy,omitempty"`
	// HeldOwn: HeldBy's group names this model on another account of
	// its provider (Member.Account, EastonXvii on X), rather than holding
	// the whole group to some
	HeldOwn bool `json:"heldOwn,omitempty"`
	// Capped: left out as held at the usage cap the user set on the
	// account, this cap in percent; Used is then its fullest window's
	// share, CapBack when the last window at or past it renews
	Capped  int        `json:"capped,omitempty"`
	CapBack *time.Time `json:"capBack,omitempty"`
	// CapAmount: held (Capped) at a window's fixed amount of its own unit
	// rather than a share: CapCount of it used, in CapUnit
	CapAmount float64 `json:"capAmount,omitempty"`
	CapCount  float64 `json:"capCount,omitempty"`
	CapUnit   string  `json:"capUnit,omitempty"`
	// NoCredits: held at 100% (Capped), a Codex account the user set not
	// to spend its credits once its allowance is used up
	NoCredits bool `json:"noCredits,omitempty"`
	// Unread: held (Capped) by a careful provider as its usage can't be
	// told, why: not read yet, the read failed, or too old near its cap
	// (provider.Provider.CapCareful)
	Unread string `json:"unread,omitempty"`
	// SpendCap: held (Capped, at 100) as the key has spent the spend cap
	// set on it, in USD: Spent of it over SpendPeriod (day, month or
	// total, counted from SpendSince)
	SpendCap    float64    `json:"spendCap,omitempty"`
	Spent       float64    `json:"spent,omitempty"`
	SpendPeriod string     `json:"spendPeriod,omitempty"`
	SpendSince  *time.Time `json:"spendSince,omitempty"`
	// Credits: a Codex account that goes on past its allowance on its
	// credits, and is let spend them: all but used up, it still answers,
	// and goes before those used up with nothing to go on with
	Credits bool `json:"credits,omitempty"`
	// CreditsLast: such an account, on its credits now, that the user set
	// to spend them only after everything else: tried after every
	// candidate not on its credits (restLast)
	CreditsLast bool `json:"creditsLast,omitempty"`
	// Busy: the load its plugin reported for the model, a percent over
	// the user's settings.BusyLoad, so it was tried after every ready
	// candidate that isn't (restLast)
	Busy float64 `json:"busy,omitempty"`
	// CreditsFirst: such an account (Credits) the user set to spend its
	// credits before another account: all but used up, it keeps its
	// place, new conversations and kept ones included
	CreditsFirst bool `json:"creditsFirst,omitempty"`
	// Rank: its place in its provider's own list of accounts or keys, the
	// order the provider's page shows and a drag sets (#217); routing may
	// weigh them in another
	Rank int `json:"rank,omitempty"`
	// Aside: a key made for another protocol than the keys routed over,
	// tried only after them
	Aside bool `json:"aside,omitempty"`
	// Reserve: an account or key held back, its provider's accounts at
	// once (provider.AtOnce, this many) taken by those before it in the
	// user's order: tried, aside, only after they all failed
	Reserve int `json:"reserve,omitempty"`
	// NoURL: its keys are all set to this protocol, which its provider has
	// no URL for, so it was tried on the URLs it has (#1702)
	NoURL provider.Protocol `json:"noURL,omitempty"`
	// Via: the groups in the group it is of, outermost first, when it is
	// of a group in the group asked for
	Via []string `json:"via,omitempty"`
	// UsedRenews: when the window Used is the share of renews, apart
	// from Renews' biggest first: a row says the two together, so of one
	// window, a Factory account's week and not its month (#1778)
	UsedRenews *time.Time `json:"usedRenews,omitempty"`
	// Sunk: when it was rate limited with quota left, sending it to the
	// back of an order that sinks (sink.go); nil when it didn't, or the
	// order doesn't sink
	Sunk *time.Time `json:"sunk,omitempty"`
}

// Try is one candidate trying the request.
type Try struct {
	ID     string `json:"id"`
	Model  string `json:"model,omitempty"`  // the model it was asked for: a group's members may share a provider's keys
	Effort string `json:"effort,omitempty"` // the reasoning it was sent at, fitted to its model's levels; "" for none
	Picked bool   `json:"picked,omitempty"` // Effort is the turn's pick, in place of the agent's
	// Fixed: the effort the group's member it went to is fixed at, which
	// Effort is (fitted to the model's levels) whatever was asked
	Fixed string `json:"fixed,omitempty"`
	// RuleEffort: the reasoning the group's rule asks the member for
	// (Rule.UseEffort, #2633), which Effort is whatever was asked
	RuleEffort string `json:"ruleEffort,omitempty"`
	// MappedFrom: the level asked for, which the group's member it went
	// to sends Effort in place of (Group.EffortMaps)
	MappedFrom string    `json:"mappedFrom,omitempty"`
	Fast       bool      `json:"fast,omitempty"` // sent in its vendor's fast mode, as the group's member it went to is
	Start      time.Time `json:"start"`
	Done       bool      `json:"done"`
	Status     int       `json:"status,omitempty"`
	Millis     int64     `json:"ms,omitempty"`
	// TTFT: ms from Start to its reply's first content, FirstText to its
	// first text, when it streamed any (#196)
	TTFT      int64 `json:"ttft,omitempty"`
	FirstText int64 `json:"firstText,omitempty"`
	// firstMs: ms from Start to the first content, as it came, while the
	// try still streams (firstContent); the route-events feed's alone,
	// TTFT being told once the try is done
	firstMs int64
	// SentBy: what sent its request to the vendor when magpie's own
	// client didn't, so it has no Net and the Routing page says why
	// (sentBy)
	SentBy string `json:"sentBy,omitempty"`
	// FirstReasoning is the start of paced visible reasoning, absent for hidden reasoning.
	FirstReasoning  int64 `json:"firstReasoning,omitempty"`
	ReasoningHidden bool  `json:"reasoningHidden,omitempty"`
	// Flow: the ms its content took to come (Call.Flow)
	Flow int64 `json:"flow,omitempty"`
	// Served: the model its reply said answered, when it named one;
	// Swapped: another model than Model, not just its dated name; Routed:
	// Model is another magpie's routing group, and Served the member it
	// routed to (usage.GroupRouted)
	Served  string `json:"served,omitempty"`
	Swapped bool   `json:"swapped,omitempty"`
	Routed  bool   `json:"routed,omitempty"`
	// Upstream: the provider an aggregator said answered behind it
	// (OpenRouter's DeepInfra, Novita …)
	Upstream string `json:"upstream,omitempty"`
	// Pin: the upstream the try asked the Cline API to serve it from
	// (provider.ClinePassAsked), "" when it asked for none
	Pin string `json:"pin,omitempty"`
	// Tier: the service tier it went out at, once a fast pick or an
	// account without Ultrafast had changed it; TierServed: the one its
	// reply said it ran at, when it said one (#1784)
	Tier       string `json:"tier,omitempty"`
	TierServed string `json:"tierServed,omitempty"`
	Fail       string `json:"fail,omitempty"` // why it failed, as rest tells it
	Error      string `json:"error,omitempty"`
	Rest       *Rest  `json:"rest,omitempty"`  // how long it now sits out; none when it was the last to try
	Again      int64  `json:"again,omitempty"` // ms waited before it was tried again, the last one left
	// Kept: Again was waited though others were left, because the
	// conversation is kept on this one for what its vendor cached of it,
	// so it is asked once more before it moves (keptAgain, jiabubu)
	Kept bool `json:"kept,omitempty"`
	// Replan: every member had failed in a way that passes, so after
	// Again the Replan of them that may answer by then were asked again,
	// in turn, within the group's Patience (seconds) for the request
	// (#1418)
	Replan   int `json:"replan,omitempty"`
	Patience int `json:"patience,omitempty"`
	// Gone: the agent canceled the request in the pause Again was to be
	// waited, so nobody was asked again (#1894: the Routing page's story
	// otherwise ended on "tried again in 1s", a retry that never came)
	Gone bool `json:"gone,omitempty"`
	// Earlier: the last try, its allowance run out, whose error the agent
	// wasn't told: it got the first failure before it that wasn't an
	// allowance's (Discord, waroy). Whether a try went on is the next
	// try in Tries, which is appended only as it is asked (#2281)
	Earlier bool `json:"earlier,omitempty"`
	// errType: the kind of error the failure's body named (Usage.ErrType),
	// kept for the one told in place of a later try's (Earlier), so the
	// row's kind is that failure's and not the later try's
	errType string
	// Queued: ms it waited for one of its key's or account's slots, the
	// provider's MaxConcurrency out already (concurrency.go)
	Queued int64 `json:"queued,omitempty"`
	// Net: where its time went up to its reply's first byte (TryNet)
	Net *TryNet `json:"net,omitempty"`
	// Reset: its week used up and nobody else left, one of the account's
	// Codex resets was spent by itself (the user's setting) — on Who, and
	// what spending it did — and the request asked again
	Reset *AutoReset `json:"reset,omitempty"`
	// Credits: a Codex account that holds credits, refused as out of its
	// allowance, was asked again at once, for the vendor to spend its
	// credits on the turn (creditsAgain)
	Credits bool `json:"credits,omitempty"`
	// Auto: the models Copilot's Auto picked for it, in turn — where each
	// pick came from and Copilot's refusal of it — Model being "auto"
	Auto []provider.AutoPick `json:"auto,omitempty"`
}

// AutoReset is a Codex reset spent by itself, on Who's account. (Claude's
// resets were taken out in 9bf6d4352 and aren't coming back: reading or
// spending one means magpie calling Anthropic with the account's token,
// #1972.)
type AutoReset struct {
	Who   string `json:"who"`
	Text  string `json:"text"`
	Agent string `json:"agent,omitempty"` // the account's agent, as Account.Agent has it
}

type planned struct {
	order, left []Weighed
	// held: the accounts left out (in left) as they won't spend their
	// credits, for one that spends its resets by itself to spend one
	held []candidate
	// capped: the accounts left out (in left) as they are past the usage
	// cap set on them, for a conversation that stays strictly on one to
	// go on there (provider.AffinityStrict, #1797)
	capped []candidate
	// unlisted: the accounts left out (in left) as their own list lacks
	// the model, their rows beside them in unlistedW, kept for the
	// checkpoint narrowing: the one that sealed a conversation's compaction
	// is tried for it as the last resort an account whose list lacks the
	// model is, though no other can read it
	unlisted  []candidate
	unlistedW []Weighed
}

func weighed(c candidate, p provider.Provider, wg weighing, fallback bool, from provider.Protocol) Weighed {
	w := Weighed{ID: c.rest, Provider: p.ID, Name: p.Name, Icon: p.Icon, Preset: p.Preset, Model: c.model, Fixed: c.effort, Fast: c.fast,
		Routing: p.Routing, Fallback: fallback, Shared: c.rest != p.ID, Rank: c.rank, NoURL: c.noURL, Reserve: c.reserve, UseFirst: c.p.UseFirst}
	switch {
	case c.p.Account != nil:
		w.Kind, w.Who, w.Agent, w.Plan = "account", c.p.Account.User, c.p.Account.Agent, c.p.Account.Plan
		if w.Agent == "plugin" {
			// a plugin's account is told as its provider's: a moved Grok's
			// plan reads as the built-in's did
			w.Agent = c.p.ID
		}
	case c.rest != p.ID:
		w.Kind, w.Who = "key", c.p.KeyName
		if w.Who == "" {
			w.Who = provider.Mask(c.p.Key)
		}
		w.Fit, w.Speaks = keyFit(c.p, c.model, from), c.p.KeyProtocol
	default:
		w.Kind = "provider"
	}
	if l, ok := wg.lefts[c.allowanceKey()]; ok {
		w.Known, w.Used, w.Renews, w.Pace, w.Credits, w.CreditsFirst = true, l.used, l.renews, l.pace, l.credits, l.first
		w.Unreached, w.Soon = l.unreached, l.soon
		if !l.lapse.IsZero() {
			lp := l.lapse
			w.Lapse = &lp
		}
		w.WeekLow = l.weekLow && keepsWeek(p.Routing)
		if !l.fullest.IsZero() {
			ur := l.fullest
			w.UsedRenews = &ur
		}
		if l.of > 0 {
			w.Amount, w.Limit, w.Unit = l.amount, l.of, l.unit
		}
		if l.balIn != "" {
			b := l.bal
			w.Balance, w.Unit = &b, l.balUnit
		}
		if !l.due.IsZero() {
			due := l.due
			w.Due = &due
			switch {
			case l.dueRestart:
				w.DueBy = "reset"
			case l.dueExpiry:
				w.DueBy = "expiry"
			}
		}
		if rs := l.restarts; !rs.IsZero() && len(l.soon) > 0 && rs.Equal(l.soon[0]) {
			w.Restarts = &rs
		}
	} else if wg.lefts != nil {
		w.Learns = learns(c, wg.lefts)
	}
	if wg.tokens != nil {
		w.Tokens = wg.tokens[c.rest]
	}
	if wg.free != nil {
		w.Free = wg.free[c.rest]
	}
	if t, ok := wg.expires[c.rest]; ok {
		w.Expires = &t
	}
	if sp, ok := wg.speed[c.restID()]; ok {
		w.LateTTFT, w.LateTPS = math.Round(sp[0]), math.Round(sp[1]*10)/10
	}
	if pr, ok := wg.price[c.restID()]; ok {
		w.Price = &SeatPrice{Input: pr.in, Output: pr.out, CacheRead: pr.cache, Peak: pr.peak}
	}
	if sinks(p.Sink, p.Routing) {
		markSunk(&w, c)
	}
	return w
}

// trace keeps the last requests' routes, and wakes those waiting for more.
type trace struct {
	mu     sync.Mutex
	seq    int64
	ids    int64
	routes []*Route
	wake   chan struct{}
	totals Totals
	// activity is the route-events feed's last changes (route_events.go)
	activity routeActivityLog
	// bodies are the last requests' masked bodies, for their prompts'
	// text (PromptText): in memory only, never in the history
	bodies []keptBody
	// turns follows agents' turns, to say when one is done (TurnDone)
	turns turns
	// folders: each session's folder, as its requests said it last, for
	// the requests that don't say it (a title, a topic check)
	folders map[string]sessionFolder
	// liftSeq is the seq a rest was last lifted by hand at (lifted): a
	// page waiting hears of it, though no route changed
	liftSeq int64
}

// lifted tells the pages waiting that a rest was lifted (Unrest).
func (t *trace) lifted() {
	t.mu.Lock()
	t.changed()
	t.liftSeq = t.seq
	t.mu.Unlock()
}

type sessionFolder struct {
	folder string
	at     time.Time
}

// Totals count the requests routed since the gateway started.
type Totals struct {
	Requests int `json:"requests"` // answered, one way or the other
	Rerouted int `json:"rerouted"` // tries that failed and handed the request on
	Errors   int `json:"errors"`   // requests whose agent got an error
}

// TraceState is the routes changed since a seq, and the totals.
type TraceState struct {
	Seq    int64   `json:"seq"`
	Routes []Route `json:"routes"`
	Totals Totals  `json:"totals"`
	// IDs are every route the trace keeps now, changed or not: a page that
	// stopped reading while out of sight drops a request it last heard of
	// under way that has since ended and gone from the trace
	IDs []int64 `json:"ids,omitempty"`
	// By is the magpie whose routing this is, when it is another magpie's
	// (OwnerTrace): the console names it from here, so the name follows the
	// trace when another magpie takes the gateway over (#2606)
	By *TraceBy `json:"by,omitempty"`
	// Lifted is when each rest lifted before its time was lifted, by the
	// key it rested by (Rest.Key): the routes kept from before still say
	// it rested, and the page shows it resting no more
	Lifted map[string]time.Time `json:"lifted,omitempty"`
}

// TraceBy names the magpie a trace was read from.
type TraceBy struct {
	Pid    int    `json:"pid"`
	Serves string `json:"serves"`
}

func (t *trace) changed() {
	t.seq++
	if t.wake != nil {
		close(t.wake)
		t.wake = nil
	}
}

// beginFor begins req's route, marked with the gateway key req came with.
// Every route of a request begins here, so a held key's own routes are
// told it: one begun without (begin alone) is told to no held key.
func (t *trace) beginFor(req *http.Request, r Route) *Route {
	r.CallerKey, r.CallerKeyName = callerKeyOf(req.Context())
	rt := t.begin(r)
	noteRoute(req.Context(), rt)
	return rt
}

// callerKeyOf is the gateway key ctx's request came with: its id and name.
func callerKeyOf(ctx context.Context) (id, name string) {
	who := access.Caller(ctx)
	if who.KeyID == "" {
		return "", ""
	}
	return who.KeyID, who.KeyName
}

func (t *trace) begin(r Route) *Route {
	// the body is read for its folder before the lock: a long one takes
	// a while, and every route waits on the lock
	if r.dirOf != nil {
		dir := r.dirOf()
		r.dirOf = func() string { return dir }
		r.Folder = shortFolder(dir)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sessionFolder(&r, time.Now())
	if t.ids == 0 {
		// ids go on from one run to the next, the history keeping them
		// all: counted on from the time the gateway began
		t.ids = time.Now().UnixMilli()
	}
	t.ids++
	r.ID = t.ids
	if r.Tries == nil {
		r.Tries = []Try{}
	}
	rp := &r
	t.turns.begin(rp, time.Now())
	t.routes = append(t.routes, rp)
	t.trim()
	t.changed()
	rp.Seq = t.seq
	t.noteActivity(rp, "request_started")
	return rp
}

// foldersKeep is how many sessions' folders are kept, the oldest said
// going first when there are more.
const foldersKeep = 512

// sessionFolder gives r its session's folder when it says none, and keeps
// the one it says for those that come after: an agent's requests on the
// side, a title or a topic check, have no full system prompt to say it.
// A request with no session id has only its own (#1068).
func (t *trace) sessionFolder(r *Route, now time.Time) {
	key := func(s string) string {
		if s == "" {
			return ""
		}
		return r.Agent + "\x00" + s
	}
	own, parent := key(r.Session), key(r.ParentSession)
	if r.Folder == "" {
		for _, k := range []string{own, parent} {
			if f, ok := t.folders[k]; ok && k != "" {
				r.Folder = f.folder
				return
			}
		}
		return
	}
	if own == "" {
		return
	}
	if t.folders == nil {
		t.folders = map[string]sessionFolder{}
	}
	t.folders[own] = sessionFolder{r.Folder, now}
	for len(t.folders) > foldersKeep {
		oldest := ""
		for k, f := range t.folders {
			if oldest == "" || f.at.Before(t.folders[oldest].at) {
				oldest = k
			}
		}
		delete(t.folders, oldest)
	}
}

// shortFolder is the last two levels of dir, "/"-joined: enough to tell
// one project from another, without the rest of the path (#1068). A
// Windows drive is no level; "" for none.
func shortFolder(dir string) string {
	parts := strings.FieldsFunc(strings.TrimSpace(dir), func(c rune) bool { return c == '/' || c == '\\' })
	if len(parts) > 0 && len(parts[0]) == 2 && parts[0][1] == ':' {
		parts = parts[1:]
	}
	if len(parts) > 2 {
		parts = parts[len(parts)-2:]
	}
	return strings.Join(parts, "/")
}

// trim keeps traceKeep routes, the oldest finished ones going first: a
// request still going isn't dropped for those that came after it (#436),
// as the history has only finished ones, unless more than twice traceKeep
// are going at once.
func (t *trace) trim() {
	for len(t.routes) > traceKeep {
		i := slices.IndexFunc(t.routes, func(r *Route) bool { return r.Done })
		if i < 0 {
			if len(t.routes) <= 2*traceKeep {
				return
			}
			i = 0
		}
		gone := t.routes[i]
		t.routes = slices.Delete(t.routes, i, i+1)
		if !gone.Done {
			// a request still going is dropped only past twice traceKeep:
			// the route-events feed is told it went, as it hears of no end
			t.noteDropped(gone)
		}
	}
}

// drop takes rt off the trace, a route that only stood for a request
// until its own began (unrouted.go).
func (t *trace) drop(rt *Route) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if i := slices.Index(t.routes, rt); i >= 0 {
		t.routes = slices.Delete(t.routes, i, i+1)
		t.changed()
		if !rt.Done {
			t.noteDropped(rt)
		}
	}
}

// update changes a route under the lock.
// maskedOf is r's Masked, read under the lock.
func (t *trace) maskedOf(r *Route) *Masked {
	if r == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return r.Masked
}

func (t *trace) update(r *Route, f func(r *Route)) {
	if r == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	done := r.Done
	before := activityOf(r)
	f(r)
	if r.Done && !done {
		t.totals.Requests++
		for i, try := range r.Tries {
			if try.Rest != nil && i < len(r.Tries)-1 { // the last rests with nobody after it (failVerify)
				t.totals.Rerouted++
			}
		}
		r.Canceled = usage.CanceledBy(r.Status, r.Error)
		if (r.Status >= 400 || r.Error != "") && !r.Canceled {
			t.totals.Errors++
		}
		t.turns.end(r, turnFinal(r), time.Now())
		// read: the scan's context is its request's, which keeps the
		// request on it (inflight's unrouted watch) and so its body; a
		// socket turn's is the whole conversation, in memory, and 60
		// routes are kept
		r.tools = nil
		if keepsRoutes() {
			c := *r
			c.Order = append([]Weighed{}, r.Order...) // [] for none: the GUI reads it as a list
			c.Left = append([]Weighed(nil), r.Left...)
			c.Tries = append([]Try{}, r.Tries...)
			c.Usage = append([]RouteUsage(nil), r.Usage...)
			go saveRoute(c)
		}
	}
	t.changed()
	r.Seq = t.seq
	if after := activityOf(r); before != after {
		t.noteActivity(r, activityChange(before, after))
	}
}

// liveBytes writes a reply's running totals on its route (live.go): the
// bytes the agent has had, when content last came, and its estimated
// output tokens. Each write wakes the pages waiting, which gather a
// moment before they read (traceGather), so a stream's many writes reach
// a page a few times a second, and the last before a pause is not lost.
func (t *trace) liveBytes(rt *Route, bytes, contentAt int64, est int) {
	if rt == nil {
		return
	}
	t.mu.Lock()
	rt.Bytes, rt.ContentAt, rt.EstTok = bytes, contentAt, est
	t.changed()
	rt.Seq = t.seq
	t.mu.Unlock()
}

// gatewayTrace tells this gateway's routing to another magpie on the same
// computer, which does not serve the gateway and so has no trace of its own
// (#2606, fatcutegg: the trace is the serving process's, and a second magpie
// showed none of it). Its console reads this to show the same Routing page.
// This computer alone, and never to a held key: the trace names accounts,
// what they served, folders and why accounts rest. A request through a proxy
// or tunnel on this computer isn't local, nor is a LAN one while shared.
func (s *Server) gatewayTrace(w http.ResponseWriter, r *http.Request) {
	if !local(r) {
		writeError(w, provider.Chat, http.StatusForbidden, "magpie's routing is told to this computer alone")
		return
	}
	if refuseHeld(w, r, heldRefused) {
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	var wait time.Duration
	if r.URL.Query().Get("wait") != "" {
		wait = 25 * time.Second
	}
	st := s.Trace(r.Context(), after, wait)
	writeJSON(w, 200, map[string]any{"object": "trace", "now": time.Now(), "seq": st.Seq, "routes": st.Routes, "totals": st.Totals, "ids": st.IDs, "lifted": st.Lifted, "pid": os.Getpid(), "serves": Serves})
}

// ownerURL is the gateway's address on loopback, where the magpie serving
// it is read by another magpie on this computer (OwnerTrace, OwnerLanes).
// An address on another interface, or another host, gives "": the trace is
// read from this computer alone, never from wherever MAGPIE_ADDR points.
func ownerURL() string {
	h, p, err := net.SplitHostPort(Addr())
	if err != nil {
		return ""
	}
	ip := net.ParseIP(strings.Trim(h, "[]"))
	switch {
	case h == "", strings.EqualFold(h, "localhost"), ip != nil && ip.IsUnspecified():
		return "http://" + net.JoinHostPort("127.0.0.1", p)
	case ip != nil && ip.IsLoopback():
		return "http://" + net.JoinHostPort(ip.String(), p)
	}
	return ""
}

// OwnerTrace reads the routing the magpie serving the gateway's address has
// decided, for a magpie on this computer that doesn't serve it: its console
// reads this and shows the same Routing page (#2606: the trace is the
// serving process's alone, so a second magpie showed none of it). ok is
// false when nothing answers there, or a magpie too old to tell its trace,
// or one that refuses: unknown, never an empty trace.
func OwnerTrace(ctx context.Context, after int64, wait time.Duration) (TraceState, bool) {
	base := ownerURL()
	if base == "" {
		return TraceState{}, false
	}
	q := url.Values{}
	if after > 0 {
		q.Set("after", strconv.FormatInt(after, 10))
	}
	if wait > 0 {
		q.Set("wait", "1")
	}
	// the serving magpie holds a long poll up to 25s; a hung one is let go
	// a few seconds past that, and at once when the page stops asking
	c := &http.Client{Timeout: wait + 5*time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/magpie/trace?"+q.Encode(), nil)
	if err != nil {
		return TraceState{}, false
	}
	res, err := c.Do(req)
	if err != nil {
		return TraceState{}, false
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return TraceState{}, false
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return TraceState{}, false
	}
	var out struct {
		Object string               `json:"object"`
		Seq    int64                `json:"seq"`
		Routes []Route              `json:"routes"`
		Totals Totals               `json:"totals"`
		IDs    []int64              `json:"ids"`
		Lifted map[string]time.Time `json:"lifted"`
		Pid    int                  `json:"pid"`
		Serves string               `json:"serves"`
	}
	if decodeJSON(b, &out) != nil || out.Object != "trace" || out.Routes == nil {
		return TraceState{}, false
	}
	return TraceState{Seq: out.Seq, Routes: out.Routes, Totals: out.Totals, IDs: out.IDs, Lifted: out.Lifted, By: &TraceBy{Pid: out.Pid, Serves: out.Serves}}, true
}

// OwnerLanes reads the lanes of the magpie serving the gateway's address, for
// a console that doesn't serve it (the providers' rows show the same pills
// wherever the gateway runs). ok is false when nothing answers there. It is
// asked on every state read, so it waits no longer than ServedBy does.
func OwnerLanes() (map[string]Lane, bool) {
	base := ownerURL()
	if base == "" {
		return nil, false
	}
	c := &http.Client{Timeout: 700 * time.Millisecond}
	res, err := c.Get(base + "/v1/magpie/concurrency")
	if err != nil {
		return nil, false
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, false
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, false
	}
	var out struct {
		Data map[string]Lane `json:"data"`
	}
	if decodeJSON(b, &out) != nil || out.Data == nil {
		return nil, false
	}
	return out.Data, true
}

// Trace is the routes changed after seq, oldest first. With none, it waits
// up to wait for one.
func (s *Server) Trace(ctx context.Context, after int64, wait time.Duration) TraceState {
	t := &s.trace
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	for {
		t.mu.Lock()
		if t.seq < after {
			after = 0 // the gateway started over since
		}
		st := TraceState{Seq: t.seq, Routes: []Route{}, Totals: t.totals, IDs: make([]int64, 0, len(t.routes))}
		for _, r := range t.routes {
			st.IDs = append(st.IDs, r.ID)
			if r.Seq > after {
				c := *r
				c.Order = append([]Weighed{}, r.Order...) // [] for none: the GUI reads it as a list
				c.Left = append([]Weighed(nil), r.Left...)
				c.Tries = append([]Try{}, r.Tries...)
				c.Usage = append([]RouteUsage(nil), r.Usage...)
				st.Routes = append(st.Routes, c)
			}
		}
		if len(st.Routes) > 0 || wait <= 0 || t.liftSeq > after {
			t.mu.Unlock()
			st.Lifted = liftedRests()
			return st
		}
		if t.wake == nil {
			t.wake = make(chan struct{})
		}
		wake := t.wake
		t.mu.Unlock()
		select {
		case <-wake:
			// a request's way is told in a burst (begun, its prompt read,
			// a try sent, answered, its usage), each change waking every
			// page waiting: gathered a moment, a page hears of the burst
			// once, not 4-8 times a second while agents are busy
			gather := time.NewTimer(traceGather)
			select {
			case <-gather.C:
			case <-ctx.Done():
				gather.Stop()
			}
		case <-deadline.C:
			st.Lifted = liftedRests()
			return st
		case <-ctx.Done():
			return st
		}
	}
}

// traceGather is how long a waiting Trace gathers changes after the first
// one wakes it.
var traceGather = 250 * time.Millisecond

// swapped reports whether served is another model than sent: not the same
// name, however dated, pinned or prefixed (usage.Swapped).
func swapped(sent, served string) bool { return usage.Swapped(sent, served) }

// routeUsage retains only what pricing needs, without another copy of the
// request's metadata. Unknown token counts have no price, including failures.
// What the reply counted of earlier requests (Deferred) is priced as a tier
// of its own before the request's, which stays last: the prompt is read
// from the last (promptCounted), and is the request's own.
func routeUsage(id, model string, u Usage) []RouteUsage {
	var out []RouteUsage
	if d := u.Deferred; d != nil && d.Input+d.Output+d.CacheRead+d.CacheWrite > 0 {
		out = append(out, RouteUsage{Provider: id, Model: model, Input: d.Input, Output: d.Output,
			CacheRead: d.CacheRead, CacheWrite: d.CacheWrite, Reasoning: d.Reasoning})
	}
	if u.Input+u.Output == 0 {
		return out
	}
	return append(out, RouteUsage{Provider: id, Model: model, Input: u.Input, Output: u.Output,
		CacheRead: u.CacheRead, CacheWrite: u.CacheWrite, Reasoning: u.Reasoning,
		CacheUnknown: u.cacheNotCounted(), Credits: u.Credits})
}
