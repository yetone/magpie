// Package gui hosts the desktop app: a tray panel and a regular window that
// share one small web UI. The UI talks to Go over a tiny JSON API served by
// the same handler that serves the static assets, so no binding generator or
// bundler is involved.
package gui

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
	"io/fs"
	"maps"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/applog"
	"github.com/yetone/magpie/internal/autostart"
	"github.com/yetone/magpie/internal/bark"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/fonts"
	"github.com/yetone/magpie/internal/fx"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/ghtoken"
	"github.com/yetone/magpie/internal/lastgood"
	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/library"
	"github.com/yetone/magpie/internal/mcpauth"
	"github.com/yetone/magpie/internal/middleware"
	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/notices"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/profile"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/redact"
	"github.com/yetone/magpie/internal/sessions"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/settingsapi"
	"github.com/yetone/magpie/internal/update"
	"github.com/yetone/magpie/internal/usage"
)

// cliBehind is the terminal's magpie command when it's a copied file rather
// than the installer's link — a copy can't follow updates (#531's lesson) —
// told once, and only until the user dismisses it for this version. The
// check is a bare Lstat: telling a *stale* copy from a current one would
// mean running the old binary, and `magpie version` isn't read-only — it
// migrates the user's settings first, with the old build's migrations. Only
// `magpie update`, which the user started, reads versions (update.StaleCLI).
// "" when the command is the link or absent, this isn't the Mac, or the
// advice was dismissed for this version.
func cliBehind() string {
	cliBehindOnce.Do(func() {
		cli := update.CopiedCLI()
		if cli == "" {
			return
		}
		if cliQuiet() {
			return
		}
		cliBehindMu.Lock()
		cliBehindVal = tilde(cli)
		cliBehindMu.Unlock()
	})
	cliBehindMu.Lock()
	defer cliBehindMu.Unlock()
	return cliBehindVal
}

// cliQuiet is whether the advice was dismissed for this version; the
// dismiss is kept per version, so the next app update asks once more.
// cliQuietFile holds the version it was dismissed at.
const cliQuietFile = "cli-behind-quiet"

func cliQuiet() bool {
	b, err := os.ReadFile(filepath.Join(settings.Dir(), cliQuietFile))
	return err == nil && strings.TrimSpace(string(b)) == Version
}

// setCLIQuiet dismisses the advice until the next version.
func setCLIQuiet() error {
	if err := os.MkdirAll(settings.Dir(), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(settings.Dir(), cliQuietFile), []byte(Version+"\n"), 0o644)
}

// cliBehindRoutes serves the advice's "Hide until the next version".
func cliBehindRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/cli-behind/quiet", func(rw http.ResponseWriter, r *http.Request) {
		if err := setCLIQuiet(); err != nil {
			fail(rw, err)
			return
		}
		cliBehindMu.Lock()
		cliBehindVal = "" // kept away until the next version; the Once already ran
		cliBehindMu.Unlock()
		rw.WriteHeader(http.StatusNoContent)
	})
	// Settings' Command line: what `magpie` runs in each shell, and this
	// app's put there when asked (update.AddCLI); shell names the one whose
	// profile gets ~/.local/bin
	mux.HandleFunc("GET /api/cli", func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, update.ReadCLI())
	})
	// Settings › About's Open-source licenses: the notices this binary
	// carries (notices.Text, THIRD_PARTY_NOTICES)
	mux.HandleFunc("GET /api/notices", func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, map[string]string{"text": notices.Text})
	})
	mux.HandleFunc("POST /api/cli", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Shell string `json:"shell"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		v, err := update.AddCLI(in.Shell)
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, v)
	})
}

var cliBehindOnce = new(sync.Once) // a var so tests can ask again
var cliBehindMu sync.Mutex         // the dismiss handler writes cliBehindVal while state() reads it on other goroutines
var cliBehindVal string

// Version is the build's version string, shown in Settings.
var Version = "dev"

// Windows is what the API needs from the host application.
type Windows interface {
	HidePanel()
	// ShowMain brings the window up, on the named tab when view is set.
	ShowMain(view string)
	Quit()
	// OpenURL hands a link to the system browser. The error is the browser's
	// not starting, which a sign-in shows with its link (#2043).
	OpenURL(url string) error
	// OpenFolder shows a folder in the system file manager.
	OpenFolder(path string) error
	// ChooseFolder asks for a folder in the system's picker: "" when the
	// user cancels it.
	ChooseFolder(title string) (string, error)
	// Copy puts text on the system clipboard, which the page's own
	// navigator.clipboard can't always reach from inside the app.
	Copy(text string) bool
	// FitPanel asks for the panel to be tall enough for its content.
	FitPanel(height int, g Glide)
	// TintPanel paints the panel's tint behind the page, where the system
	// keeps up with the panel's size; false when it can't, for the page to
	// go on painting it itself.
	TintPanel(rgba [4]uint8, ms int) bool
	// TintTitleBar paints the window's title bar the page's colour, where
	// the system draws one (Windows); false where there is none to paint.
	TintTitleBar(rgba [4]uint8, dark bool) bool
	// SetTextSize zooms the window's and the panel's pages to percent
	// (settings.TextSizes), the panel's size with them.
	SetTextSize(percent int)
}

type fieldJSON struct {
	Key     string         `json:"key"`
	Label   string         `json:"label"`
	Value   string         `json:"value"`
	Options []agent.Option `json:"options"`
	// List is, in place of Options, which of the state's OptionLists the
	// field offers, from 1 (shareOptions); 0 when Options is its own
	List int `json:"list,omitempty"`
}

type agentJSON struct {
	ID     string      `json:"id"`
	Name   string      `json:"name"`
	Icon   string      `json:"icon"`
	Path   string      `json:"path"`
	Fields []fieldJSON `json:"fields"`
	// Drift: its config no longer does what magpie set, and how to set it again
	Drift *agent.Drift `json:"drift,omitempty"`
	// Wired: magpie is in its config, which its menu's Disconnect takes out
	Wired bool `json:"wired,omitempty"`
	// Import: an app that takes magpie by its own link (Cindy), and
	// whether it has magpie already; omitted when the app has no status reader
	Import string `json:"import,omitempty"`
	Added  *bool  `json:"added,omitempty"`
	// Launch: the command that starts an agent taking the gateway only
	// from its environment (agy) on magpie, to copy
	Launch string `json:"launch,omitempty"`
	// Typed: what starts it on magpie typed in a new terminal, a function
	// magpie gave the user's shells (agy); the launch is for one open now
	Typed string `json:"typed,omitempty"`
	// Models: how many of the catalog its lists show, for an agent that
	// picks among it (agent_models.go)
	Models *modelCountJSON `json:"models,omitempty"`
	// Menu: its own model menu is the models picked here, no one model to
	// start on (agent.ListsModels): ZCode's, Cursor Private Inference's
	Menu bool `json:"menu,omitempty"`
	// NoNames: its own list doesn't name magpie's models as Names in agents'
	// lists says (agent.NoNames), for that setting to say so
	NoNames bool `json:"noNames,omitempty"`
	// Source: what an agent not connected runs on now (agent.Source), and
	// Stale: copies of a connected one still running on the list they
	// started with (agent.Stale), for the line under its name
	Source string `json:"source,omitempty"`
	Stale  int    `json:"stale,omitempty"`
	// StaleCopies: those copies, each with what it is and when it
	// started, for the opened row to say which is left to reopen and how
	StaleCopies []agent.StaleCopy `json:"staleCopies,omitempty"`
	// AppRestart: its desktop app (Claude Desktop, WorkBuddy) running on
	// what it read before magpie's last change, for the row's Restart
	// (agent.AppBehind)
	AppRestart *agent.AppRun `json:"appRestart,omitempty"`
	// Isolated: Claude Desktop's Claude Magpie app (#5671), only where it
	// can be (macOS): whether it is on, and the app
	Isolated *agent.DesktopIsolated `json:"isolated,omitempty"`
	// Telemetry: the agent's own telemetry to its vendor, where magpie
	// knows the switch for it (agent/telemetry.go)
	Telemetry *agent.TelemetryState `json:"telemetry,omitempty"`
	// Joined: connected with its own models still in its list (Codex
	// signed in with ChatGPT, agent.Agent.Join)
	Joined bool `json:"joined,omitempty"`
	// AppOut: magpie is Codex's provider while the ChatGPT account it is
	// signed in to is used up (agent.Agent.AppOut), which turns the Codex
	// app's ChatGPT side off until the reset
	AppOut *appOutJSON `json:"appOut,omitempty"`
	// AppBack: magpie gave Codex back its ChatGPT account at the reset
	// and left the Codex app on the app-server it started while magpie
	// was its provider, which shows no account or usage till it restarts
	// (provider.CodexAppBack), for the row's Restart
	AppBack bool `json:"appBack,omitempty"`
	// AppSnag: the Codex app showed its error page after magpie restarted
	// its app-server and magpie couldn't reopen it (provider.CodexAppSnagged)
	AppSnag bool `json:"appSnag,omitempty"`
	// Failover: not connected, yet its requests go through magpie for
	// account failover alone (agent.Agent.FailingOver, #1385)
	Failover bool `json:"failover,omitempty"`
	// CLIMissing: its settings are here, its CLI isn't (#843), which
	// the row says with none of its settings offered (#2177), and Install
	// another agent offers it again where magpie knows how
	CLIMissing bool               `json:"cliMissing,omitempty"`
	Native     *agent.NativeState `json:"native,omitempty"`
	// Command: the line that starts it (agentCommand), which its row menu
	// copies; Terminal: and opens in a terminal, this system having one
	Command  string `json:"command,omitempty"`
	Terminal bool   `json:"terminal,omitempty"`
	// OpenApp: its row menu opens the desktop app, not only a terminal
	// (Codex, by the setting codexOpen, Yink88564391 on X)
	OpenApp bool `json:"openApp,omitempty"`
}

// clientJSON is an agent, or another client the gateway knows, as a
// request from it is drawn.
type clientJSON struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Icon string `json:"icon"`
}

type profileJSON struct {
	Name    string `json:"name"`
	Summary string `json:"summary"`
	// Library is what the profile gives out from the library, for one
	// saved with its setup
	Library *profileLibraryJSON `json:"library,omitempty"`
	// Agents is what it holds, by agent, to be read before it is applied
	// (#467); a value that reads as a key or a token is left out
	Agents []profile.Group `json:"agents"`
	// InUse: the setup is this profile now, applying it would change
	// nothing (profile.InUse); the page marks it so (superhtoooth on X:
	// nothing said which profile was in use)
	InUse bool `json:"inUse,omitempty"`
}

type profileLibraryJSON struct {
	Servers      int  `json:"servers"`      // given to at least one agent
	Skills       int  `json:"skills"`       // given to at least one agent
	Instructions bool `json:"instructions"` // some agent gets them
}

type stateJSON struct {
	Agents   []agentJSON   `json:"agents"`
	Clients  []clientJSON  `json:"clients"` // who a request may come from, by id
	Profiles []profileJSON `json:"profiles"`
	Catalog  string        `json:"catalog"`
	Notice   string        `json:"notice,omitempty"` // advice after a change, e.g. "restart Codex"
	// Connected is how the 「接入」 switch chose the model the agent starts
	// on, in the answer to agents/connect (agent.Connection)
	Connected *agent.Connection `json:"connected,omitempty"`
	// CLIBehind is the terminal's magpie command, told to the user when
	// it's a copied file rather than the installer's link: a copy can't
	// follow updates (#531's lesson). "" when it's the link or absent, or
	// the advice was dismissed for this version.
	CLIBehind string `json:"cliBehind,omitempty"`
	// ConfigBlocked is the folder this user can't write that keeps magpie
	// from saving its files (a ~/.config sudo left root's), which the
	// window offers to make theirs again; "" when magpie can save
	ConfigBlocked string            `json:"configBlocked,omitempty"`
	Settings      settings.Settings `json:"settings"`
	// FX is the dollar-to-yuan rate the cny currency choice shows costs at,
	// here too (not only in settingsJSON) so a cost drawn before the reader
	// ever opens Settings already converts, if cny was chosen last time.
	FX fxJSON `json:"fx"`
	// Unlisted are the models kept for routing groups, which the pickers
	// don't offer: a filter that finds one of them says why it isn't there
	Unlisted []unlistedJSON `json:"unlisted,omitempty"`
	// Recovered are magpie's files a crash left unreadable (all zero,
	// #1505) that were read from their last good generation: the page
	// tells the user once each
	Recovered []lastgood.Note `json:"recovered,omitempty"`
	// Last: this is the state kept from the last read (laststate.go), for
	// the page to draw while the fresh one is read
	Last bool `json:"last,omitempty"`
	// Served is how many models magpie serves, groups left out: the Get
	// started card's first step ("add a model source") is done once
	// there is one, agents found or not
	Served int `json:"served"`
	// GatewayOther is a magpie of another version serving the gateway in
	// this one's place, which the Agents page says (gateway_older.go)
	GatewayOther *gatewaySkew `json:"gatewayOther,omitempty"`
	// GatewayHeld: the program that has the gateway's port when this magpie
	// couldn't listen on it, as far as it could be read (gateway.PortHeld)
	GatewayHeld *gateway.PortHeld `json:"gatewayHeld,omitempty"`
	// OptionLists are the fields' option lists, each once, that a field's
	// List names (shareOptions), as indexes into Options, where each option
	// is once: with real accounts 13 agents' model pickers offered the same
	// 51KB catalog, 650KB of a 690KB state, beside a few models of their
	// own. Last of the fields: writeStateJSON writes them after the rest.
	OptionLists [][]int32       `json:"optionLists,omitempty"`
	Options     []*agent.Option `json:"options,omitempty"`
}

// unlistedJSON is a model of a provider kept for routing groups, and the
// groups ("group/<id>") it is used through, none when it is in no group.
type unlistedJSON struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Provider string   `json:"provider"`
	Icon     string   `json:"icon,omitempty"`
	Groups   []string `json:"groups"`
}

// unlistedModels lists provider.Unlisted for the page.
func unlistedModels() []unlistedJSON {
	es := provider.Unlisted()
	if len(es) == 0 {
		return nil
	}
	in := provider.MemberGroups()
	out := make([]unlistedJSON, 0, len(es))
	for _, e := range es {
		gs := in[e.ID]
		if gs == nil {
			gs = []string{}
		}
		out = append(out, unlistedJSON{ID: e.ID, Name: e.Name, Provider: e.Provider.Name, Icon: e.Provider.Icon, Groups: gs})
	}
	return out
}

// fxJSON is a USD→CNY rate as the UI shows it: the number a cost is
// multiplied by, when it was last learned (unset for Fallback, never
// learned from anywhere), and whether that's stale — the Settings page's
// currency row puts these in its tooltip.
type fxJSON struct {
	Rate  float64    `json:"rate"`
	At    *time.Time `json:"at,omitempty"`
	Stale bool       `json:"stale"`
}

// currentFX is the rate known now, never waited for: a stale one is asked
// for behind it and shown on the next look (#541: the state waited up to
// 4s on the network after each TTL, at every morning's start-up).
func currentFX() fxJSON {
	r := fx.Soon()
	out := fxJSON{Rate: r.CNYPerUSD, Stale: r.Stale()}
	if !r.At.IsZero() {
		at := r.At
		out.At = &at
	}
	return out
}

// settingsJSON is the Settings page: the two choices plus the facts it shows.
type settingsJSON struct {
	settings.Settings
	Version string `json:"version"`
	Dir     string `json:"dir"`     // where magpie keeps its files, as shown
	Gateway string `json:"gateway"` // the local endpoint
	// Builtins are the built-in plugins: each one's switch, whether it
	// runs and what of it is in use (builtins.go)
	Builtins []builtinJSON `json:"builtins"`
	// PanelKeys is whether the quick panel's shortcut can be set here
	// (panelkey.go)
	PanelKeys panelKeysJSON `json:"panelKeys"`
	// JSRoutings are the routings JS gateway plugins serve now
	// ("<plugin>/<name>"), for the routing pickers
	JSRoutings []middleware.Routing `json:"jsRoutings"`
	// PublicURL is MAGPIE_PUBLIC_URL while other machines are let in
	// (shared, or MAGPIE_ADDR on the network): where they reach the
	// gateway, shown beside the local address agents here are pointed at
	// (#1551)
	PublicURL string `json:"publicURL,omitempty"`
	// PublicKind is gateway.URLKind's of PublicURL: "local" (a private
	// network's address), "internet", or "" for a name; the page names
	// it by that, not as public whatever it is
	PublicKind string `json:"publicKind,omitempty"`
	// MAGPIE_ADDR, when it sets the gateway's address over Settings' port
	AddrEnv string `json:"addrEnv,omitempty"`
	// MAGPIE_ALLOW_IPS, when it sets the allow list over Settings', and
	// the list it sets; OnNetwork: MAGPIE_ADDR listens on the network, so
	// the list is shown even while sharing is off
	AllowIPsEnv     string   `json:"allowIPsEnv,omitempty"`
	AllowIPsFromEnv []string `json:"allowIPsFromEnv,omitempty"`
	OnNetwork       bool     `json:"onNetwork,omitempty"`
	// the client certificates issued and still good (mtls.go), while
	// other computers can reach magpie or mTLS is on; MTLSSaved: the
	// folder the one just issued was saved to
	MTLSCerts []mtlsCert `json:"mtlsCerts,omitempty"`
	MTLSErr   string     `json:"mtlsError,omitempty"`
	MTLSSaved string     `json:"mtlsSaved,omitempty"`
	// JoinOn: a password is set to join with (join.go); the password
	// itself is never sent
	JoinOn bool `json:"joinOn,omitempty"`
	// Dir is the data folder beside a portable magpie (#508)
	Portable bool `json:"portable,omitempty"`
	// Log is the log the desktop app writes (applog), as shown; "" for a
	// magpie writing none (magpie web)
	Log string `json:"log,omitempty"`
	// WSL is whether there is WSL to look in for agents (Windows), for
	// Settings' Detect agents in WSL (#1264)
	WSL bool `json:"wsl,omitempty"`
	// Rekeyed is what an agent beyond loopback, given the sharing key,
	// couldn't be set again with when sharing changed (agent.Rekey)
	Rekeyed string `json:"rekeyed,omitempty"`
	// Notice is a Settings row's warning when a setting can't take effect:
	// the Codex subagents row when Codex's own multi_agent_v2 is on in the
	// config magpie routes (#141)
	Notice string `json:"notice,omitempty"`
	// Mac apps that explicitly handle .command files, for resumed sessions.
	TerminalApps    []terminalChoice `json:"terminalApps,omitempty"`
	TerminalDefault string           `json:"terminalDefault,omitempty"`
	OTelEnv         bool             `json:"otelEnv,omitempty"`
	// NoAccountsEnv is MAGPIE_NO_ACCOUNTS set, which keeps account
	// discovery off for this run whatever Settings has (#1535)
	NoAccountsEnv bool `json:"noAccountsEnv,omitempty"`
	// whether the page is in gateway mode and why (gatewayMode); Web says
	// it is served by magpie web, where the mode can be set
	GatewayOn  bool   `json:"gatewayOn,omitempty"`
	GatewayWhy string `json:"gatewayWhy,omitempty"`
	// GatewayPaused: the user paused the gateway (gatewaypause.go)
	GatewayPaused bool `json:"gatewayPaused,omitempty"`
	Web           bool `json:"web,omitempty"`
	// the proxy vendor requests go through now, and where it came from:
	// settings, environment, system, off or none
	// SavedProxies are the named proxies, without their passwords
	SavedProxies []savedProxyJSON `json:"proxies"`
	ProxyNow     string           `json:"proxyNow"`
	ProxySource  string           `json:"proxySource"`
	// whether magpie opens at login: the system's record, not a setting;
	// LoginAllow, the record is there but the user switched it off in
	// System Settings › Login Items, which is where it's allowed again
	Login      bool `json:"login"`
	LoginAllow bool `json:"loginAllow,omitempty"`
	// the model that describes images when Vision names none, and those
	// that can be named
	VisionAuto   string     `json:"visionAuto,omitempty"`
	VisionModels []modelRef `json:"visionModels"`
	// the Vision the user picked when magpie can't find it any more:
	// VisionAuto describes in its place, and the row says so
	VisionMissing string `json:"visionMissing,omitempty"`
	// VisionNotMarked are the custom providers (AddedByAddress) with a model left
	// out of VisionModels only because nothing says it takes images: the
	// user can say so in the provider's Model settings (nexnull on Discord)
	VisionNotMarked []string `json:"visionNotMarked,omitempty"`
	// the reasoning levels of the model that describes images now, and the
	// one it is asked at when one is picked (VisionEffort fitted to them)
	VisionEfforts   []string `json:"visionEfforts,omitempty"`
	VisionEffortNow string   `json:"visionEffortNow,omitempty"`
	// the models Codex's thread titles may be sent to (CodexTitles, #705)
	TitleModels []modelRef `json:"titleModels"`
	// the models a request masking would change may go to as written
	// (RedactRouteTo): those whose every route is set to go unmasked
	// (gateway.MaskRoute)
	MaskRouteModels []modelRef `json:"maskRouteModels"`
	// the providers the user set to Send requests unmasked
	// (provider.SkipsRedaction), listed in Settings › Privacy, each a way
	// to its editor, so the switch is found from where masking is
	// (CrazyMouse_TT on X)
	UnmaskedProviders []modelRef `json:"unmaskedProviders"`
	// what each of the disabled models (#1867) turns off now, by pattern
	DisabledHits map[string][]string `json:"disabledHits,omitempty"`
	// the model magpie's generate_image tool draws with when ImageGen
	// names none, and those that can be named
	ImageGenAuto string `json:"imageGenAuto,omitempty"`
	// the ImageGen the user picked when magpie can't find it any more
	ImageGenMissing string `json:"imageGenMissing,omitempty"`
	// the listed model the ImageGen the user picked stands for, when it
	// isn't listed itself: a ChatGPT account's gpt-image-2.5, saved when
	// magpie listed it, is its one GPT Image (gateway.ListedDrawer, #1865)
	ImageGenShown  string     `json:"imageGenShown,omitempty"`
	ImageGenModels []modelRef `json:"imageGenModels"`
	// the ImageGen the user picked is one unticked in its provider's
	// editor since (provider.HiddenDrawers): it still draws, and is
	// listed, as picked, with the row saying it is hidden
	ImageGenHidden bool `json:"imageGenHidden,omitempty"`
	// the model a video request that names none is made with when
	// VideoGen names none (gateway.AutoVideomaker), the VideoGen picked
	// when magpie can't find it any more, and those that can be picked:
	// each provider's video models, a custom provider's the user marked
	// (#1399) among them, less those unticked in its editor
	VideoGenAuto    string     `json:"videoGenAuto,omitempty"`
	VideoGenMissing string     `json:"videoGenMissing,omitempty"`
	VideoGenModels  []modelRef `json:"videoGenModels"`
	// the web search APIs a model's search goes to when no provider can
	// search (#419), their keys masked; the ones that can be added; and
	// the provider that searches first, if one does
	SearchAPIs     []searchAPIJSON    `json:"searchAPIs"`
	SearchVendors  []searchVendorJSON `json:"searchVendors"`
	SearchProvider string             `json:"searchProvider,omitempty"`
	// the providers Settings' Searcher may name, the one magpie picks when
	// it names none, why the one it names isn't used (gateway.Searcher*),
	// and the relays said to search that are never picked automatically (#359)
	SearchChoices []searchChoiceJSON `json:"searchChoices"`
	SearchAuto    string             `json:"searchAuto,omitempty"`
	SearchUnused  string             `json:"searchUnused,omitempty"`
	// the reasoning levels of the model that searches now, and the one it
	// is asked at when one is picked (SearcherEffort fitted to them)
	SearchEfforts   []string `json:"searchEfforts,omitempty"`
	SearchEffortNow string   `json:"searchEffortNow,omitempty"`
	SearchRelays    []string `json:"searchRelays,omitempty"`
	// the last search the one it names didn't make, since it last
	// searched: no other provider searched in its place (Gearclickist on
	// Discord)
	SearchFailed *searchFailedJSON `json:"searchFailed,omitempty"`
	// the providers on that aren't offered, as they can't search the web
	// by themselves (#825)
	SearchLeftOut []string `json:"searchLeftOut,omitempty"`
	// the GitHub token the library asks GitHub with, masked, and where it
	// is from ("settings", GITHUB_TOKEN or GH_TOKEN); never the token
	GitHubTokenMask string `json:"githubTokenMask,omitempty"`
	GitHubTokenFrom string `json:"githubTokenFrom,omitempty"`
	// the token set in Settings was refused by GitHub (401): it is still
	// shown, masked, while requests go without a token (#3926)
	GitHubTokenRefused bool `json:"githubTokenRefused,omitempty"`
	// whether a Bark device key is set; never the key (EV_John_Liu on X)
	BarkKeySet bool `json:"barkKeySet,omitempty"`
	// where other machines reach the gateway while it is shared
	LANURLs []string `json:"lanURLs,omitempty"`
	// LANURLs are a container's own addresses, not the host's: the page
	// offers the one it was opened at instead, or says how to set it
	LANContainer bool `json:"lanContainer,omitempty"`
	// where other containers on this host reach the gateway while magpie
	// runs in one (gateway.ContainerHostURL)
	LANContainerHost string `json:"lanContainerHost,omitempty"`
	// a LazyCat app's domain, which only devices with LazyCat's client
	// reach (gateway.LazyCatURL), and whether the box's LAN addresses are
	// listed too: when not, the page says how the port gets there
	LANLazyCat    string `json:"lanLazyCat,omitempty"`
	LANLazyCatBox bool   `json:"lanLazyCatBox,omitempty"`
	// those of LANURLs at an internet address, which anyone online can
	// reach, not only this network
	LANPublic []string `json:"lanPublic,omitempty"`
	// when the Codex warm-up last started an account's window
	CodexWarmed *time.Time `json:"codexWarmed,omitempty"`
	// and the Claude warm-up
	ClaudeWarmed *time.Time `json:"claudeWarmed,omitempty"`
	// the ChatGPT accounts signed in, each of which can have a daily
	// warm-up time of its own (#957)
	CodexUsers []string `json:"codexUsers,omitempty"`
	// and the Claude accounts, which Warm up now picks from
	ClaudeUsers []string `json:"claudeUsers,omitempty"`
	// the Codexes here that kept the background compaction's table (and
	// its share) out of the config at the last sync: too old for it
	// (before 0.163.0-alpha.8), or of no known version (codex_async.go)
	CodexAsyncHeld []agent.CodexReader `json:"codexAsyncHeld,omitempty"`
	// the keys this machine's Codex config has that Codex reads before
	// Long conversations (model_context_window,
	// model_auto_compact_token_limit; agent.CodexCompactHeld), written
	// as the row names them: "[profiles.p] key = value, ..."
	CodexCompactKeys string `json:"codexCompactKeys,omitempty"`
	// Claude Code's two prompt-cache lifetimes as it reads them
	// (agent.TuneRead): the rows beside its compaction in its settings
	ClaudeCache []claudeCacheJSON `json:"claudeCache,omitempty"`
	// ClaudeAttribution: whether Claude Code adds its Co-Authored-By trailer
	// ("on", "off", or "custom" for the user's own text)
	ClaudeAttribution string `json:"claudeAttribution,omitempty"`
	// the magpie model Codex's config runs, when its own window is no
	// larger than Long conversations' threshold: Codex compacts it at 90%
	// of that window (codexWindowUnder)
	CodexWindow *codexRunWindow `json:"codexWindow,omitempty"`
	// whether a WorkBuddy (China) account is signed in, and each one's
	// last daily check-in
	WorkBuddy         bool                        `json:"workbuddy"`
	WorkBuddyCheckins []provider.WorkBuddyCheckin `json:"workbuddyCheckins,omitempty"`
	// and a Trae CN account (its plugin's), and theirs (#694)
	Trae         bool                        `json:"trae"`
	TraeCheckins []provider.WorkBuddyCheckin `json:"traeCheckins,omitempty"`
	// and a MiniMax Code (China) account (its plugin's), and theirs (#811)
	MiniMax         bool                        `json:"minimax"`
	MiniMaxCheckins []provider.WorkBuddyCheckin `json:"minimaxCheckins,omitempty"`
	// and a Qoder or Qoder CN account (its plugin's), and theirs
	Qoder         bool                        `json:"qoder"`
	QoderCheckins []provider.WorkBuddyCheckin `json:"qoderCheckins,omitempty"`
	// every account each vendor's daily check-in reaches, and whether the
	// user left it out (#1902)
	CheckinAccounts []provider.CheckinAccount `json:"checkinAccounts,omitempty"`
	// and the plugins' providers that check in themselves (auth.checkin),
	// each with its switch and its accounts' last check-ins
	CheckinPlugins []provider.PluginCheckin `json:"checkinPlugins,omitempty"`
	// FX is the dollar-to-yuan rate the cny currency choice shows costs at
	FX fxJSON `json:"fx"`
	// NotifyProblem is why a usage alert set wouldn't be seen: "denied"
	// (notifications turned off for magpie) or "unavailable"
	NotifyProblem string `json:"notifyProblem,omitempty"`
}

// searchAPIJSON is a search API as the Settings page shows it.
type searchAPIJSON struct {
	Vendor string `json:"vendor"`
	Name   string `json:"name"`
	Key    string `json:"key,omitempty"` // masked
	URL    string `json:"url,omitempty"`
	Ready  bool   `json:"ready"`
	// Keys is how many keys it has, Pick how a search picks among them
	// (provider.SearchPick*), and Quota whether what each has left can be
	// read (gateway.SearchQuotas)
	Keys  int    `json:"keys,omitempty"`
	Pick  string `json:"pick,omitempty"`
	Quota bool   `json:"quota,omitempty"`
	// Off: switched off, kept but never asked (#2286)
	Off bool `json:"off,omitempty"`
	// KeyList is each key in its order, masked, on or off, and resting
	// (辰林鸭~ on Discord)
	KeyList []searchKeyJSON `json:"keyList"`
}

// searchKeyJSON is one key of a search API as its list shows it: never
// the key itself, which only Copy and Show ask for.
type searchKeyJSON struct {
	Ref    string `json:"ref"` // provider.SearchKeyRef
	Masked string `json:"masked"`
	Off    bool   `json:"off,omitempty"`
	// Rest is why the gateway asks it last now, RestUntil until when
	// (gateway.SearchKeyRest)
	Rest      string    `json:"rest,omitempty"`
	RestUntil time.Time `json:"restUntil,omitzero"`
}

// searchChoiceJSON is a provider that can search for a model that can't,
// with the model it searches with when none is named, and its models.
type searchChoiceJSON struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Icon      string     `json:"icon,omitempty"`
	Small     string     `json:"small"`
	SmallName string     `json:"smallName,omitempty"`
	Models    []modelRef `json:"models"`
	// Service is a Kimi Code plan, which searches by its search service:
	// named by itself, with no model
	Service bool `json:"service,omitempty"`
	// Own is a Google sign-in or an AI Studio key, which searches for its
	// own models first
	Own bool `json:"own,omitempty"`
	// Search is a ChatGPT account, which searches by the ChatGPT backend's
	// search ("<id>/@search") besides its models
	Search bool `json:"search,omitempty"`
}

type searchVendorJSON struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	KeysURL string `json:"keysURL,omitempty"`
	NeedURL bool   `json:"needURL,omitempty"` // one the user runs
	// KeyOptional: it can be added with no key (You.com's free search, #1626)
	KeyOptional bool `json:"keyOptional,omitempty"`
	// its own address, the one used while no base URL is set (#1580)
	Base string `json:"base,omitempty"`
	// Auth: a plugin's API that signs in as the plugin's provider, taking
	// no key (provider.SearchVendor.Auth)
	Auth *searchAuthJSON `json:"auth,omitempty"`
}

// searchAuthJSON is the provider a search API signs in as: its name, and
// how many of its accounts are on (0 with none signed in).
type searchAuthJSON struct {
	Name     string `json:"name"`
	SignedIn bool   `json:"signedIn"`
	On       int    `json:"on"`
}

func searchState(s *settingsJSON) {
	names := map[string]string{}
	for _, e := range provider.Served() {
		names[e.ID] = cmp.Or(e.Name, e.Model)
	}
	s.SearchAPIs, s.SearchVendors = []searchAPIJSON{}, []searchVendorJSON{}
	for _, a := range provider.StoredSearchAPIs() {
		j := searchAPIJSON{Vendor: a.Vendor, Name: a.Name(), URL: a.URL, Ready: a.Ready(), Keys: len(a.Keys()), Pick: a.Pick, Quota: gateway.CanReadSearchQuota(a), Off: a.Off}
		if a.Key != "" {
			j.Key = a.MaskedKey()
		}
		j.KeyList = []searchKeyJSON{}
		for _, k := range a.SearchKeys() {
			kj := searchKeyJSON{Ref: provider.SearchKeyRef(k.Key), Masked: provider.MaskSearchKey(k.Key), Off: k.Off}
			if !k.Off {
				kj.Rest, kj.RestUntil = gateway.SearchKeyRest(a.Vendor, k.Key)
			}
			j.KeyList = append(j.KeyList, kj)
		}
		s.SearchAPIs = append(s.SearchAPIs, j)
	}
	for _, v := range provider.AllSearchVendors() {
		j := searchVendorJSON{ID: v.ID, Name: v.Name, KeysURL: v.KeysURL, NeedURL: v.WantsURL(), KeyOptional: v.KeyOptional, Base: v.Base}
		if v.Auth != "" {
			name, p := provider.SearchSignIn(v)
			j.Auth = &searchAuthJSON{Name: name}
			if p != nil {
				j.Auth.SignedIn = true
				j.Auth.On = gateway.SearchAccountsOn(*p)
			}
		}
		s.SearchVendors = append(s.SearchVendors, j)
	}
	s.SearchProvider = gateway.Searcher()
	s.SearchAuto, s.SearchUnused = gateway.AutoSearcher(), gateway.SearcherUnused()
	s.SearchEfforts, s.SearchEffortNow = gateway.SearchEfforts(), gateway.SearchEffortNow()
	if f, ok := gateway.SearcherFailed(); ok {
		s.SearchFailed = &searchFailedJSON{Searcher: f.Searcher, Why: f.Why, NothingFound: f.NothingFound, NotSearched: f.NotSearched, Instead: f.Instead, At: f.At}
	}
	s.SearchChoices = []searchChoiceJSON{}
	for _, c := range gateway.Searchers() {
		p := c.Provider
		j := searchChoiceJSON{ID: p.ID, Name: p.Name, Icon: p.Icon, Small: c.Small, SmallName: names[p.ID+"/"+c.Small], Models: []modelRef{}, Service: c.Service, Own: c.Own, Search: c.Search}
		for _, m := range c.Models {
			id := p.ID + "/" + m.ID
			j.Models = append(j.Models, modelRef{ID: id, Name: cmp.Or(names[id], m.Name, m.ID), Provider: p.ID, PName: p.Name, Icon: p.Icon})
		}
		s.SearchChoices = append(s.SearchChoices, j)
	}
	for _, p := range gateway.RelaysSaidToSearch() {
		s.SearchRelays = append(s.SearchRelays, p.Name)
	}
	for _, p := range provider.All() {
		if p.On() && !slices.ContainsFunc(s.SearchChoices, func(c searchChoiceJSON) bool { return c.ID == p.ID }) && !slices.Contains(s.SearchRelays, p.Name) {
			s.SearchLeftOut = append(s.SearchLeftOut, p.Name)
		}
	}
}

// searchFailedJSON is gateway.SearchFailure as Settings shows it.
type searchFailedJSON struct {
	Searcher     string    `json:"searcher"`
	Why          string    `json:"why,omitempty"`
	NothingFound bool      `json:"nothingFound,omitempty"`
	NotSearched  bool      `json:"notSearched,omitempty"`
	Instead      string    `json:"instead,omitempty"` // who, named after it, searched in its place
	At           time.Time `json:"at"`
}

// loginState is whether magpie opens at login, as the system will act on
// it. A var for the tests: the real one asks the user's own system.
var loginState = autostart.Enabled

// codexRunWindow is a model and the window magpie tells Codex it has.
type codexRunWindow struct {
	Model  string `json:"model"`
	Window int    `json:"window"`
	// Size is Window as the page writes a window (contextsText): 128K, 1M;
	// "n" is its slot in the row's words, which take the struct whole
	Size string `json:"n"`
}

func tokensSize(n int) string {
	switch {
	case n%1_000_000 == 0:
		return strconv.Itoa(n/1_000_000) + "M"
	case n%1000 == 0:
		return strconv.Itoa(n/1000) + "K"
	}
	return strconv.Itoa(n)
}

// codexKeysText names Codex's config keys as the Long conversations row
// shows them, each with its profile and value.
func codexKeysText(hs []agent.CodexCompactHold) string {
	var b []string
	for _, h := range hs {
		k := h.Key + " = " + h.Value
		if h.Profile != "" {
			k = "[profiles." + h.Profile + "] " + k
		}
		b = append(b, k)
	}
	return strings.Join(b, ", ")
}

// codexWindowUnder is model's window when Long conversations' threshold
// doesn't come below it, so Codex compacts at 90% of the window instead
// (zgjfjfl on Discord, 10-31: a relay's gpt-6-A at 128K, Long
// conversations at 544K, compacting near 128K). Under Full window, or
// with a threshold set on the model or its provider, it is nil: the row
// says what the user chose. A provider's model is looked up at its
// provider alone (ServedEntryOf), a group in the catalog, since this is
// read on every settings read.
func codexWindowUnder(st settings.Settings, model string) *codexRunWindow {
	if model == "" || st.FullContext {
		return nil
	}
	e, ok := provider.ServedEntryOf(model)
	if !ok && strings.HasPrefix(model, provider.GroupPrefix) {
		e, ok = provider.EntryOf(model)
	}
	if !ok || e.Context <= 0 || provider.CompactFor(st, e, provider.GroupFinder()) > 0 || st.Working(e.Context) < e.Context {
		return nil
	}
	return &codexRunWindow{Model: model, Window: e.Context, Size: tokensSize(e.Context)}
}

func settingsState() settingsJSON {
	s := settingsJSON{Settings: settings.Load(), Version: Version, Dir: tilde(settings.Dir()), Portable: settings.Portable() != "", Gateway: gateway.URL()}
	s.AddrEnv = os.Getenv("MAGPIE_ADDR")
	s.Log = tilde(applog.Current())
	s.Builtins = builtinsState()
	s.PanelKeys = panelKeysState()
	s.JSRoutings = middleware.Routings()
	s.OnNetwork = gateway.OnNetwork()
	if list, env := gateway.AllowIPs(); env {
		s.AllowIPsEnv, s.AllowIPsFromEnv = os.Getenv(gateway.AllowIPsEnv), list
	}
	if s.LAN || s.OnNetwork || s.MTLS {
		s.MTLSCerts, s.MTLSErr = mtlsCerts()
	}
	s.JoinOn = access.JoinOn()
	if s.LAN || gateway.OnNetwork() {
		s.PublicURL = gateway.PublicURL()
		s.PublicKind = gateway.URLKind(s.PublicURL)
	}
	s.WSL = runtime.GOOS == "windows"
	s.Web = webPage.Load()
	s.GatewayOn, s.GatewayWhy = gatewayMode(s.Web)
	s.GatewayPaused = pausedNow()
	s.LANKey = "" // the retained credential belongs on disk, not in UI state
	s.BarkKeySet, s.BarkKey = strings.TrimSpace(s.BarkKey) != "", ""
	// the GitHub token, masked, and where the library's requests take one
	// from: Settings, or the environment variable named
	own := strings.TrimSpace(s.GitHubToken)
	s.GitHubToken = ""
	if tok, from := library.GitHubToken(); tok != "" {
		s.GitHubTokenMask, s.GitHubTokenFrom = provider.Mask(tok), from
	} else if _, refused := ghtoken.SettingsRefused(); refused && own != "" {
		// a refused token set here is still the user's: shown as refused,
		// not as if none were set, while requests go without one (#3926)
		s.GitHubTokenMask, s.GitHubTokenFrom, s.GitHubTokenRefused = provider.Mask(own), "settings", true
	}
	if found, err := discoverTerminals(); err == nil {
		for _, app := range found.Apps {
			s.TerminalApps = append(s.TerminalApps, terminalChoice{ID: app.ID, Name: app.Name})
		}
		s.TerminalDefault = found.Default
	}
	s.FX = currentFX()
	if (s.UsageAlert > 0 || s.BalanceAlert > 0 || s.ResetReminder > 0 || s.TurnDoneAlert > 0 || s.MaskAlert) && notifyProblem != nil {
		s.NotifyProblem = notifyProblem()
	}
	s.ProxyNow, s.ProxySource = netproxy.Describe()
	s.Settings.Proxies, s.SavedProxies = nil, savedProxies() // passwords stay on disk
	s.NoAccountsEnv = settings.AccountDiscoveryOff() && !s.NoAccountDiscovery
	for _, name := range []string{"MAGPIE_OTEL_ENABLED", "MAGPIE_OTEL_ENDPOINT", "MAGPIE_OTEL_HEADERS", "MAGPIE_OTEL_METRICS", "MAGPIE_OTEL_BODIES", "MAGPIE_OTEL_BODIES_WHOLE", "MAGPIE_OTEL_SESSIONS"} {
		if _, ok := os.LookupEnv(name); ok {
			s.OTelEnv = true
		}
	}
	login := loginState()
	s.Login, s.LoginAllow = login != autostart.Off, login == autostart.NeedsAllowing
	if s.LAN {
		s.LANURLs, s.LANContainer = gateway.LANURLs(), gateway.ContainerAddrs()
		s.LANContainerHost = gateway.ContainerHostURL()
		s.LANLazyCat, s.LANLazyCatBox = gateway.LazyCatURL(), gateway.LazyCatBox()
		s.LANPublic = gateway.PublicURLs(s.LANURLs)
	}
	s.CodexWarmed, s.ClaudeWarmed = latest(provider.CodexWarmed()), latest(provider.ClaudeWarmed())
	s.CodexUsers, s.ClaudeUsers = loginUsers("codex"), loginUsers("claude")
	s.CodexAsyncHeld = agent.CodexAsyncHeld()
	s.CodexCompactKeys = codexKeysText(agent.CodexCompactHeld(""))
	s.ClaudeCache = claudeCacheState()
	s.ClaudeAttribution = agent.ClaudeAttribution("")
	s.CodexWindow = codexWindowUnder(s.Settings.ForAgent("codex"), agent.CodexModel(""))
	s.WorkBuddy, s.WorkBuddyCheckins = provider.HasWorkBuddy(), provider.WorkBuddyCheckins()
	s.Trae, s.TraeCheckins = provider.HasTrae(), provider.TraeCheckins()
	s.MiniMax, s.MiniMaxCheckins = provider.HasMiniMax(), provider.MiniMaxCheckins()
	s.Qoder, s.QoderCheckins = provider.HasQoder(), provider.QoderCheckins()
	s.CheckinPlugins = provider.PluginCheckins()
	s.CheckinAccounts = provider.CheckinAccounts()
	s.VisionAuto, s.VisionModels, s.VisionMissing = gateway.AutoVision(), []modelRef{}, gateway.VisionMissing()
	s.VisionEfforts, s.VisionEffortNow = gateway.VisionEfforts(), gateway.VisionEffortNow()
	for _, e := range provider.Served() {
		if e.Images && (e.ImageInput == nil || *e.ImageInput) && (e.Group != "" || e.Provider.Ready()) {
			m := modelRef{ID: e.ID, Name: e.Name, Provider: e.Provider.ID, PName: e.Provider.Name, Icon: e.Provider.Icon}
			if e.Group != "" {
				m.Provider, m.PName = "", e.Group
			}
			s.VisionModels = append(s.VisionModels, m)
		} else if e.Group == "" && !e.Images && e.ImageInput == nil && e.Provider.AddedByAddress() && e.Provider.Ready() &&
			!slices.Contains(s.VisionNotMarked, cmp.Or(e.Provider.Name, e.Provider.ID)) {
			// unknown, not answered: one its list or the user said no to
			// (ModelImages) isn't counted
			s.VisionNotMarked = append(s.VisionNotMarked, cmp.Or(e.Provider.Name, e.Provider.ID))
		}
	}
	s.TitleModels, s.MaskRouteModels = []modelRef{}, []modelRef{}
	// only a provider or a group set to go unmasked can take one (most have none)
	unmaskedAny := slices.ContainsFunc(provider.All(), provider.Provider.SkipsRedaction) ||
		slices.ContainsFunc(provider.Groups(), func(g provider.Group) bool { return g.Redact == provider.RedactOff })
	s.UnmaskedProviders = []modelRef{}
	for _, p := range provider.All() {
		if p.SkipsRedaction() {
			s.UnmaskedProviders = append(s.UnmaskedProviders, modelRef{ID: p.ID, Name: p.Name, Provider: p.ID, PName: p.Name, Icon: p.Icon})
		}
	}
	for _, pat := range s.DisabledModels {
		if s.DisabledHits == nil {
			s.DisabledHits = map[string][]string{}
		}
		s.DisabledHits[pat] = provider.DisabledHits(pat)
	}
	for _, e := range provider.Served() {
		if e.Group != "" || e.Provider.Ready() && !e.Provider.DecideOnly() {
			m := modelRef{ID: e.ID, Name: e.Name, Provider: e.Provider.ID, PName: e.Provider.Name, Icon: e.Provider.Icon}
			if e.Group != "" {
				m.Provider, m.PName = "", e.Group
			}
			s.TitleModels = append(s.TitleModels, m)
			if (e.Group != "" || e.Provider.SkipsRedaction()) && unmaskedAny && gateway.MaskRoute(e.ID) {
				s.MaskRouteModels = append(s.MaskRouteModels, m)
			}
		}
	}
	searchState(&s)
	s.ImageGenAuto, s.ImageGenModels, s.ImageGenMissing = gateway.AutoDrawer(), []modelRef{}, gateway.DrawerMissing()
	for _, p := range provider.All() {
		if !p.On() || p.DecideOnly() {
			continue
		}
		// those unticked in its editor left out, but the one picked here
		// (321sssrt on X): a hidden pick still draws, and shows as picked
		for _, m := range gateway.Drawers(p) {
			if p.DrawerHidden(m.ID) && gateway.ListedDrawer(s.ImageGen) != p.ID+"/"+m.ID {
				continue
			}
			name := m.Name
			if name == "" {
				name = m.ID
			}
			s.ImageGenModels = append(s.ImageGenModels, modelRef{ID: p.ID + "/" + m.ID, Name: name, Provider: p.ID, PName: p.Name, Icon: p.Icon, VendorPicks: gateway.VendorPicksDrawer(p)})
		}
	}
	// the routing groups with a member that draws (7up6666 on X): a
	// group picked here draws on its members in its routing's order. One
	// of image models only isn't served to agents (jzw690 on X), so it is
	// listed from the groups themselves
	listed := map[string]bool{}
	for _, e := range provider.Served() {
		if e.Group != "" && gateway.GroupDraws(e.ID) {
			listed[e.ID] = true
			s.ImageGenModels = append(s.ImageGenModels, modelRef{ID: e.ID, Name: e.Name, PName: e.Group})
		}
	}
	for _, g := range provider.Groups() {
		if ref := g.Ref(); !g.Hidden && !g.Disabled && !listed[ref] && gateway.GroupDraws(ref) {
			s.ImageGenModels = append(s.ImageGenModels, modelRef{ID: ref, Name: cmp.Or(g.Name, g.ID), PName: g.ID})
		}
	}
	if v := gateway.ListedDrawer(s.ImageGen); v != s.ImageGen {
		s.ImageGenShown = v
	}
	s.ImageGenHidden = gateway.DrawerHidden(s.ImageGen)
	s.VideoGenAuto, s.VideoGenModels, s.VideoGenMissing = gateway.AutoVideomaker(), []modelRef{}, gateway.VideomakerMissing()
	for _, p := range provider.All() {
		if !p.On() || p.DecideOnly() {
			continue
		}
		// one unticked in its editor is left out, but the one picked here
		for _, m := range gateway.Videomakers(p) {
			if p.VideomakerHidden(m.ID) && s.VideoGen != p.ID+"/"+m.ID {
				continue
			}
			s.VideoGenModels = append(s.VideoGenModels, modelRef{ID: p.ID + "/" + m.ID, Name: cmp.Or(m.Name, m.ID), Provider: p.ID, PName: p.Name, Icon: p.Icon})
		}
	}
	// the Codex subagents row's warning: magpie's multi-agent V1 doesn't reach
	// Codex where its own multi_agent_v2 is on in the config magpie routes, and
	// magpie writes nothing there — the setting is the user's to turn off
	// (#141). Shown on every read, so the row carries it after a reload too.
	if s.CodexAgentsV1 {
		s.Notice = agent.CodexSubAgentsV1Warning()
	}
	return s
}

// latest is the latest of ts, nil when there is none.
func latest(ts map[string]time.Time) *time.Time {
	var out *time.Time
	for _, t := range ts {
		if out == nil || t.After(*out) {
			out = &t
		}
	}
	return out
}

// onDock puts the app in the Mac's Dock or takes it out as the settings say,
// when the Settings page changes them; set by the process that has the app.
var onDock func(settings.Settings)

// assetTypes are the page's own files' types. The file server takes them
// from Windows' registry, which another program may have changed — an SVG
// served as something else draws no icon.
var assetTypes = map[string]string{".svg": "image/svg+xml", ".png": "image/png", ".css": "text/css; charset=utf-8",
	".js": "text/javascript; charset=utf-8", ".html": "text/html; charset=utf-8"}

// addAssetTypes is called by Handler, not init: the mime package reads
// and parses the system's whole type table (/etc/apache2/mime.types,
// ~18,000 allocations) the first time a type is added, and a launch that
// serves no page (`magpie statusline` on every Claude Code message) never
// needs it.
var addAssetTypes = sync.OnceFunc(registerAssetTypes)

func registerAssetTypes() {
	for ext, t := range assetTypes {
		mime.AddExtensionType(ext, t)
	}
}

// revalidated serves the page's own files to be asked for again each time,
// by their content's hash: an embedded file has no date, so they went out
// with nothing to check them by, and a cache in front of `magpie web` (a
// proxy, a CDN, a tunnel's) could keep an older version's app.js under the
// new index.html after an update: a page without what that version added,
// such as the request archive switch (Jorben on Discord). An unchanged
// file is a 304.
//
// The page itself names each of its scripts and styles with its content's
// hash (app.js?v=…), so a cache that kept a file from before these headers
// were sent, and goes on serving it whatever magpie says now, is never asked
// for it again: a Docker user behind an HTTPS proxy had v0.1.630's page run
// v0.1.582's app.js and routing.js (incognito and a hard refresh alike),
// whose first lines looked for an element the page no longer had, and the
// page was blank under its tabs.
func revalidated(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}
		sum, page, ok := staticSum(name, r.URL.Path == "/")
		if !ok {
			next.ServeHTTP(rw, r)
			return
		}
		rw.Header().Set("ETag", `"`+hex.EncodeToString(sum[:12])+`"`)
		rw.Header().Set("Cache-Control", "no-cache")
		// a file asked for by the address the page names it by, its
		// content's hash, is the same at that address for good: a browser
		// keeps it and doesn't ask again, which a slow link to `magpie web`
		// feels at every load (akic404 on Discord). Another version's hash,
		// or none, is asked for again as before.
		if v := r.URL.Query().Get("v"); v != "" && v == hex.EncodeToString(sum[:6]) {
			rw.Header().Set("Cache-Control", "max-age=31536000, immutable")
		}
		if r.URL.Path == "/" {
			rw.Header().Set("Content-Type", "text/html; charset=utf-8")
			http.ServeContent(rw, r, "index.html", time.Time{}, bytes.NewReader(page))
			return
		}
		next.ServeHTTP(rw, r)
	})
}

// pageFile is a script or stylesheet of the page's own, named in it: one
// of the page's, or of a built-in plugin's under plugins/<id>/.
var pageFile = lazyre.New(`(src|href)="([A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)*\.(?:js|css))"`)

// versionedPage is the page with each of its own scripts and stylesheets
// named with its content's hash; one not among the page's files (boot.js,
// which the API writes) keeps its name.
// staticSum is the sha256 of a static file, or with root of the page as
// versionedPage names its files, and that page: made once in a build,
// whose files never change (staticFixed). Each load read every file
// whole and hashed it again, the page's scripts twice, ~17 MB of a page
// load with app.js alone over a megabyte (#2278). ok is false for a file
// that can't be read, which isn't kept.
func staticSum(name string, root bool) (sum [32]byte, page []byte, ok bool) {
	key := name
	if root {
		key = "/"
	}
	if staticFixed {
		if v, hit := staticSums.Load(key); hit {
			a := v.(staticFile)
			return a.sum, a.page, true
		}
	}
	b, err := fs.ReadFile(staticFS(), name)
	if err != nil {
		return sum, nil, false
	}
	if root {
		b = versionedPage(b)
		page = b
	}
	sum = sha256.Sum256(b)
	if staticFixed {
		staticSums.Store(key, staticFile{sum, page})
	}
	return sum, page, true
}

// staticSums are staticSum's answers by name ("/" for the page).
var staticSums sync.Map

type staticFile struct {
	sum  [32]byte
	page []byte
}

func versionedPage(page []byte) []byte {
	return pageFile.ReplaceAllFunc(page, func(m []byte) []byte {
		g := pageFile.FindSubmatch(m)
		sum, _, ok := staticSum(string(g[2]), false)
		if !ok {
			return m
		}
		return []byte(fmt.Sprintf(`%s="%s?v=%s"`, g[1], g[2], hex.EncodeToString(sum[:6])))
	})
}

// Handler serves the embedded UI and the JSON API.
// gw is the gateway this process serves, or nil when another magpie has it
// (for now: see startBackend).
func Handler(w Windows, gw *gateway.Server) http.Handler {
	addAssetTypes()
	if gw != nil {
		served.Store(gw)
	}
	// magpie web's own page, not the app's served to a browser, which the
	// app's windows share their settings state with
	if ownWeb(w) {
		webPage.Store(true)
	}
	mux := http.NewServeMux()
	page := devPage(revalidated(http.FileServer(http.FS(staticFS()))))
	mux.Handle("/", http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if warmsSessions(r) {
			warmSessions()
		}
		page.ServeHTTP(rw, r)
	}))
	devRoutes(mux)
	pluginPageRoutes(mux, isWeb(w))
	feedbackRoutes(mux)
	appearanceRoutes(mux)
	// boot.js hands the page the saved language and theme before it paints:
	// they came only with the settings, so the tabs showed English first
	mux.HandleFunc("GET /boot.js", func(rw http.ResponseWriter, r *http.Request) {
		s := settings.Load()
		// and the text size, which the Mac's header measures against the
		// traffic lights
		boot := map[string]any{"lang": s.Lang, "theme": s.Theme, "textSize": s.TextSize, "web": isWeb(w)}
		// and the version that served the page: a browser's page reloads
		// once another answers in its place (visamzh on X), as after its
		// own restart to update (backAsNew)
		boot["version"] = Version
		// and the clock the system shows, 12 or 24 hours, which no webview
		// tells the page (iXerol on X); a browser tab keeps its own
		if !isWeb(w) {
			if hc := systemHourCycle(); hc != "" {
				boot["hourCycle"] = hc
			}
			if loc := systemLocale(); loc != "" {
				boot["locale"] = loc
			}
		}
		if s.Colorblind {
			boot["colorblind"] = true
		}
		if s.ReduceMotion {
			boot["reduceMotion"] = true
		}
		if s.NoBird {
			boot["noBird"] = true
		}
		// a browser tab keeps its own font choice (fonts.js)
		if !isWeb(w) {
			boot["uiFont"], boot["codeFont"] = s.UIFont, s.CodeFont
		}
		// and whether this app's webview would crash on a <video> (Linux
		// without gst-plugins-good, webkitPlaysVideo), so the pages show
		// none. Here, not in the settings: the page's settings come from
		// /api/state, which never said it, so the masking demo set its
		// video and the web process aborted (avk458 on X, omarchy)
		if !isWeb(w) && !webkitPlaysVideo() {
			boot["noVideo"] = true
		}
		// and whether it is a gateway's page alone (gatewaymode.go), so the
		// pages left out never show
		if on, _ := gatewayMode(ownWeb(w)); on {
			boot["gateway"] = true
		}
		// and the built-in plugins it leaves out (builtins.go), gone
		// before the first paint rather than after the settings come
		list := builtinsState()
		boot["featuresOff"] = builtinsOff(list)
		// and where the gateway listens, so the sidebar's line at its foot
		// is drawn from the start, not held back until providers has come
		// in (#1519's read waits for the agents); only this process's own,
		// for one served by another magpie is the providers' read to find
		if served.Load() != nil {
			side := map[string]any{"url": gateway.URL(), "running": true, "boot": true}
			// and MAGPIE_PUBLIC_URL while other machines are let in, which
			// a page they open (magpie web) shows instead (noahhrreion on
			// Discord: in Docker the line said the container's loopback)
			// and, shared, the addresses other machines use, which the
			// window's line names from the first paint (coeo91 on Discord)
			if s.LAN || gateway.OnNetwork() {
				side["lan"], side["lanURLs"] = true, gateway.LANURLs()
				// in a container they are its own: the line names the host's
				if gateway.ContainerAddrs() {
					side["container"] = true
				}
				if u := gateway.ContainerHostURL(); u != "" {
					side["containerHost"] = u
				}
				if u := gateway.LazyCatURL(); u != "" {
					side["lazyCat"], side["lazyCatBox"] = u, gateway.LazyCatBox()
				}
				if u := gateway.PublicURL(); u != "" {
					side["publicURL"] = u
				}
			}
			boot["sideGateway"] = side
		}
		// and the settings of those the page draws (kernel.js)
		boot["pluginConfig"] = builtinConfig(list)
		// on Omarchy the page takes its theme's look before it paints
		if th, ok := omarchyTheme(); ok {
			boot["omarchy"] = th
		}
		// and the accent and wallpaper of the user's own (appearance.go),
		// drawn before the first paint
		boot["look"] = lookKey(s)
		b, _ := json.Marshal(boot)
		rw.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		rw.Header().Set("Cache-Control", "no-store")
		out := append(append([]byte("window.bootPrefs = "), b...), ";\n"...)
		if css := lookCSS(s); css != "" {
			c, _ := json.Marshal(css)
			out = append(out, `(function(){var e=document.createElement("style");e.id="lookCSS";e.textContent=`...)
			out = append(append(out, c...), ";document.head.appendChild(e)})();\n"...)
		}
		rw.Write(out)
	})
	// the Omarchy theme as it is now, asked again every few seconds so a
	// theme picked in Omarchy's menu reaches the page at once; null off Omarchy
	mux.HandleFunc("GET /api/omarchy", func(rw http.ResponseWriter, r *http.Request) {
		if th, ok := omarchyTheme(); ok {
			writeJSON(rw, th)
			return
		}
		writeJSON(rw, nil)
	})
	omarchyRoutes(mux, w)
	fontRoutes(mux, isWeb(w), !fonts.Available, fonts.List)
	mux.HandleFunc("GET /api/state", func(rw http.ResponseWriter, r *http.Request) {
		// the panel and its model picker load from here: an account still
		// without its vendor's list (one whose try at start-up failed) is
		// asked again in the background, not only from the Providers page
		provider.FetchNewSoon(8 * time.Second)
		// ?last=1: the state kept from the last read, at once (#1519);
		// null when none is kept
		if r.URL.Query().Get("last") == "1" {
			if s, ok := lastKnown(); ok {
				writeJSON(rw, s)
			} else {
				writeJSON(rw, nil)
			}
			return
		}
		s := state()
		if sum, ok := writeStateJSON(rw, s); ok {
			keepStateSummed(s, sum)
		}
	})
	mux.HandleFunc("POST /api/set", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Agent, Field, Value string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		a, err := agent.Find(in.Agent)
		if err != nil {
			fail(rw, err)
			return
		}
		f := a.Field(in.Field)
		if f == nil {
			http.Error(rw, "unknown field", http.StatusBadRequest)
			return
		}
		wired := a.Wired()
		if err := a.Pick(f.Key, strings.TrimSpace(in.Value)); err != nil {
			fail(rw, err)
			return
		}
		if !wired {
			resumeOnWire(a)
		}
		s := state()
		if a.Notice != nil && (!f.Live || !wired && a.Wired()) {
			s.Notice = a.Notice()
		}
		writeJSON(rw, s)
	})
	// reapply sets again what magpie set on an agent something else
	// rewrote; keep takes the agent as it is now
	// what drifted, per agent: cheap enough to ask while the window is up,
	// so a config rewritten elsewhere shows without a reload
	mux.HandleFunc("GET /api/drift", func(rw http.ResponseWriter, r *http.Request) {
		out := map[string]*agent.Drift{}
		for _, a := range agent.DetectedIn(heldAgents()) {
			if d := a.Drift(); d != nil {
				out[a.ID] = d
			}
		}
		writeJSON(rw, out)
	})
	// the agents' CLIs: their versions and the newest (#202), as far as
	// they're known within a moment — the rest are asked on meanwhile, and
	// pending says to ask again soon
	// the Agents page's refresh: looks for the agents on this machine again,
	// the answers kept a while because asking was slow forgotten (#844)
	mux.HandleFunc("POST /api/agents/rescan", func(rw http.ResponseWriter, r *http.Request) {
		agent.Rescan()
		writeJSON(rw, state())
	})
	mux.HandleFunc("GET /api/agents/cli", func(rw http.ResponseWriter, r *http.Request) {
		clis, pending := agent.CLIs(3 * time.Second)
		writeJSON(rw, map[string]any{"agents": clis, "pending": pending})
	})
	// the agents magpie knows that aren't here, with their vendors' install
	// commands to copy (#727)
	mux.HandleFunc("GET /api/agents/install", func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, agent.Installs())
	})
	// Aside's explicit offline model write. Normal model changes require its runtime.
	mux.HandleFunc("POST /api/agents/stage/{id}", func(rw http.ResponseWriter, r *http.Request) {
		a, err := agent.Find(r.PathValue("id"))
		if err != nil {
			fail(rw, err)
			return
		}
		var in struct{ Field, Value string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if a.Native == nil || a.Native.Stage == nil {
			fail(rw, fmt.Errorf("this agent does not support staged model settings"))
			return
		}
		value, err := a.Spell(in.Field, in.Value)
		if err != nil {
			fail(rw, err)
			return
		}
		if err := a.Native.Stage(in.Field, value); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, state())
	})
	// updates one the way it was installed; what it is afterwards comes
	// back with an error too
	mux.HandleFunc("POST /api/agents/cli/{id}", func(rw http.ResponseWriter, r *http.Request) {
		a, err := agent.Find(r.PathValue("id"))
		if err != nil {
			fail(rw, err)
			return
		}
		c, err := a.UpdateCLI()
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, c)
	})
	// quits the agent's desktop app and opens it again, so it reads what
	// magpie wrote: the row's Restart, after the user said yes to it
	// Claude Desktop's Claude Magpie app on or off (#5671): the app made or
	// taken away, and a Desktop on magpie moved into its folder or back
	mux.HandleFunc("POST /api/settings/desktop-isolated", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ On bool }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		home, _ := os.UserHomeDir()
		if _, err := agent.SetDesktopIsolated(home, in.On); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, state())
	})
	// its Code tab's Claude Code on a folder of its own, or back on
	// ~/.claude (#6459): the app made again with the folder or without
	mux.HandleFunc("POST /api/settings/desktop-isolated-claude", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ On bool }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		home, _ := os.UserHomeDir()
		if err := agent.SetDesktopIsolatedClaude(home, in.On); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, state())
	})
	// an agent's own telemetry off, or back (agent/telemetry.go)
	mux.HandleFunc("POST /api/agents/telemetry/{id}", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Off bool }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		a, err := agent.Find(r.PathValue("id"))
		if err != nil {
			fail(rw, err)
			return
		}
		if err := a.SetTelemetry(in.Off); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, state())
	})
	mux.HandleFunc("POST /api/agents/restart/{id}", func(rw http.ResponseWriter, r *http.Request) {
		a, err := agent.Find(r.PathValue("id"))
		if err != nil {
			fail(rw, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		if err := a.RestartApp(ctx); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, state())
	})
	// what disconnecting an agent changes in its files, line by line, for
	// the dialog asking it (agent.DisconnectPreview); its files when the
	// preview can't be had
	mux.HandleFunc("GET /api/agents/preview/{id}", func(rw http.ResponseWriter, r *http.Request) {
		a, err := agent.Find(r.PathValue("id"))
		if err != nil {
			fail(rw, err)
			return
		}
		exe, err := os.Executable()
		if err != nil {
			fail(rw, err)
			return
		}
		if a.Native != nil {
			plan, err := a.Native.Disconnect()
			if err != nil {
				writeJSON(rw, map[string]any{"error": err.Error()})
				return
			}
			writeJSON(rw, map[string]any{"changes": plan.Preview(), "revision": plan.Revision(a.ID)})
			return
		}
		changes, err := agent.DisconnectPreview(a, exe)
		out := map[string]any{"changes": changes}
		if err != nil {
			out["error"] = err.Error()
		}
		writeJSON(rw, out)
	})
	mux.HandleFunc("POST /api/agents/disconnect-offline/{id}", func(rw http.ResponseWriter, r *http.Request) {
		a, err := agent.Find(r.PathValue("id"))
		if err != nil {
			fail(rw, err)
			return
		}
		if a.Native == nil || a.Native.ExecuteOffline == nil {
			fail(rw, fmt.Errorf("this agent does not support offline disconnect"))
			return
		}
		var in struct {
			Revision string `json:"revision"`
		}
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		plan, err := a.Native.Disconnect()
		if err != nil {
			fail(rw, err)
			return
		}
		if in.Revision == "" || in.Revision != plan.Revision(a.ID) {
			fail(rw, fmt.Errorf("Disconnect preview changed; preview it again"))
			return
		}
		if err := a.Native.ExecuteOffline(plan); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, state())
	})
	mux.HandleFunc("POST /api/agents/{action}/{id}", func(rw http.ResponseWriter, r *http.Request) {
		a, err := agent.Find(r.PathValue("id"))
		if err != nil {
			fail(rw, err)
			return
		}
		var how *agent.Connection
		switch r.PathValue("action") {
		case "reapply":
			err = a.Reapply()
		case "keep":
			a.Keep()
		case "connect":
			var c agent.Connection
			c, err = a.ConnectHow()
			how = &c
			if err == nil {
				resumeOnWire(a)
			}
		case "disconnect":
			err = a.Disconnect()
		default:
			http.NotFound(rw, r)
			return
		}
		if err != nil {
			fail(rw, err)
			return
		}
		s := state()
		if a.Notice != nil {
			s.Notice = a.Notice()
		}
		s.Connected = how
		writeJSON(rw, s)
	})
	mux.HandleFunc("POST /api/profile/{action}", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Name, To string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		in.Name = strings.TrimSpace(in.Name)
		in.To = strings.TrimSpace(in.To)
		var err error
		var applied profile.Applied
		switch r.PathValue("action") {
		case "save":
			var p profile.Profile
			if p, err = profile.Snapshot(); err == nil {
				err = profile.Save(in.Name, p)
			}
		case "use":
			var ps map[string]profile.Profile
			if ps, err = profile.Load(); err == nil {
				applied, err = profile.Apply(ps[in.Name])
			}
			if applied.Library != nil {
				lastProblems.Lock()
				lastProblems.p = applied.Library.Problems
				lastProblems.Unlock()
			}
		case "delete":
			err = profile.Delete(in.Name)
		case "rename":
			err = profile.Rename(in.Name, in.To)
		default:
			http.NotFound(rw, r)
			return
		}
		if err != nil {
			fail(rw, err)
			return
		}
		s := state()
		writeJSON(rw, struct {
			stateJSON
			Changed int `json:"changed"`
			// Library is what bringing the profile's library setup back did
			Library *library.Result `json:"library,omitempty"`
		}{s, applied.Changed, applied.Library})
	})
	mux.HandleFunc("POST /api/sync", func(rw http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if err := catalog.Sync(ctx); err != nil {
			fail(rw, err)
			return
		}
		provider.RefreshLists(ctx)
		writeJSON(rw, state())
	})
	providerRoutes(mux, w)
	importRoutes(mux)
	usageRoutes(mux, w)
	callerKeyRoutes(mux)
	sessionRoutes(mux, w)
	sessionManageRoutes(mux, w)
	backupRoutes(mux, w)
	dataFolderRoutes(mux, w)
	mtlsRoutes(mux, w)
	joinRoutes(mux)
	httpsRoutes(mux, w)
	uninstallRoutes(mux, w)
	archiveRoutes(mux)
	libraryRoutes(mux, w)
	updateRoutes(mux, w)
	whatsNewRoutes(mux)
	cliBehindRoutes(mux)
	configRepairRoutes(mux, !isWeb(w))
	gatewayFixRoutes(mux)
	gatewayPauseRoutes(mux)
	gatewayModeRoutes(mux)
	if !isWeb(w) {
		appPageRoutes(mux)
		remoteRoutes(mux, w)
	}
	privacyModelRoutes(mux)
	maskingRoutes(mux)
	builtinRoutes(mux)
	mux.HandleFunc("GET /api/settings", func(rw http.ResponseWriter, r *http.Request) {
		access.MigrateLegacyLANKeyBestEffort()
		writeJSON(rw, settingsState())
	})
	mux.HandleFunc("POST /api/settings", func(rw http.ResponseWriter, r *http.Request) {
		// read, checked and saved as `magpie settings set` saves (#4745)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			fail(rw, err)
			return
		}
		in, cur, err := settingsapi.Apply(body, isWeb(w))
		if err != nil {
			fail(rw, err)
			return
		}
		if onFonts != nil && (!sameFont(in.UIFont, cur.UIFont) || !sameFont(in.CodeFont, cur.CodeFont)) {
			onFonts()
		}
		if (in.Dock != cur.Dock || in.DockWindow != cur.DockWindow || in.DockNever != cur.DockNever) && onDock != nil {
			onDock(in)
		}
		// the cards the menu bar shows, any of them (TrayUsage is only the first),
		// how often, with their logos or not, beside the bird or not, as
		// Windows' icon or not, and
		// with their Pace numbers or not, with magpie's totals' cost or not,
		// which of each subscription's windows, and the language the tooltip
		// is in (yi_time)
		if (!slices.Equal(settings.Load().TrayUsages, cur.TrayUsages) || in.TrayUsageEvery != cur.TrayUsageEvery ||
			in.TrayNoLogos != cur.TrayNoLogos || in.TrayNoBird != cur.TrayNoBird || in.TrayNoNumber != cur.TrayNoNumber || in.TrayPace != cur.TrayPace ||
			in.TrayNoCost != cur.TrayNoCost || !maps.EqualFunc(in.TrayWindows, cur.TrayWindows, slices.Equal) || in.Lang != cur.Lang || in.ChineseUnits != cur.ChineseUnits) && onTrayUsage != nil {
			onTrayUsage()
		}
		// an alert turned on or moved is looked at now, the Mac asked for its
		// leave to notify as it is turned on (#368)
		if (in.UsageAlert != cur.UsageAlert || in.BalanceAlert != cur.BalanceAlert || in.ResetReminder != cur.ResetReminder || in.TurnDoneAlert != cur.TurnDoneAlert || in.MaskAlert != cur.MaskAlert) &&
			(in.UsageAlert > 0 || in.BalanceAlert > 0 || in.ResetReminder > 0 || in.TurnDoneAlert > 0 || in.MaskAlert) && onAlerts != nil {
			onAlerts()
		}
		// the tray menu follows the page's language (#301)
		if in.Lang != cur.Lang && onLang != nil {
			onLang()
		}
		// an update check that failed, without the proxy set just now, is
		// tried again through it, not in six hours (#294), and marked so
		// before this answers: the page stops asking on the old error
		if strings.TrimSpace(in.Proxy) != strings.TrimSpace(cur.Proxy) && updates.json().State == "error" && updates.begin() {
			go updates.run()
		}
		// another update channel: the feed is asked again on it, so the
		// version row and the header's Update follow (a download from
		// every version is dropped on Stable, run), unless automatic
		// updates are off and nothing was found
		if in.UpdateChannel != cur.UpdateChannel && (!in.NoAutoUpdate || updates.json().Latest != "") && updates.begin() {
			go updates.run()
		}
		writeJSON(rw, settingsState())
	})
	// whether usage reads as used or left: the Usage page's toggle and
	// Settings', for every meter and the menu bar alike (#122)
	mux.HandleFunc("POST /api/settings/quota-left", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ On bool }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		s := settings.Load()
		changed := s.QuotaLeft != in.On
		s.QuotaLeft = in.On
		if err := settings.Save(s); err != nil {
			fail(rw, err)
			return
		}
		if changed && onTrayUsage != nil {
			onTrayUsage()
		}
		writeJSON(rw, settingsState())
	})
	// when allowances are read: whenever magpie needs them, or only when
	// the user asks (#1518)
	mux.HandleFunc("POST /api/settings/quota-reads", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Reads string `json:"reads"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		s := settings.Load()
		s.QuotaReads = in.Reads
		if err := settings.Save(s); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
	// whether magpie searches for a model whose provider searches by itself
	// too (Settings › Models › Web search)
	mux.HandleFunc("POST /api/settings/search-all", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			On bool `json:"on"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		s := settings.Load()
		s.SearchAll = in.On
		if err := settings.Save(s); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
	// how many results a search that names no count is answered with
	// (Settings › Models › Web search, #5545): 0 for the default; one
	// out of range is refused by Save and nothing is written
	mux.HandleFunc("POST /api/settings/search-count", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ N int }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		s := settings.Load()
		if s.SearchCount != in.N {
			s.SearchCount = in.N
			if err := settings.Save(s); err != nil {
				fail(rw, err)
				return
			}
		}
		writeJSON(rw, settingsState())
	})
	// every agent's own telemetry off, over each agent's row (OipsAnthony
	// on X): saved, the agents' files follow at once; an agent that
	// couldn't is named in refused, the switch saved all the same
	mux.HandleFunc("POST /api/settings/agent-telemetry", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Off bool `json:"off"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		var out struct {
			settingsJSON
			Refused string `json:"refused,omitempty"`
		}
		if err := agent.SetTelemetryAll(in.Off); err != nil {
			if settings.Load().AgentTelemetryOff != in.Off {
				fail(rw, err)
				return
			}
			out.Refused = err.Error()
		}
		out.settingsJSON = settingsState()
		writeJSON(rw, out)
	})
	// whether agents' sign-ins are read and served as subscriptions
	// (#1535): saved, the accounts read before are forgotten at once
	mux.HandleFunc("POST /api/settings/account-discovery", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Off bool `json:"off"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		s := settings.Load()
		s.NoAccountDiscovery = in.Off
		if err := settings.Save(s); err != nil {
			fail(rw, err)
			return
		}
		provider.ForgetAccounts()
		writeJSON(rw, settingsState())
	})
	// the version the header's Update pill is hidden for, until a newer one
	// is out: set from the pill, cleared ("") from Settings
	mux.HandleFunc("POST /api/settings/update-skip", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Version string `json:"version"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		s := settings.Load()
		s.UpdateSkip = strings.TrimSpace(in.Version)
		if s.UpdateSkip != "" {
			updates.quietAgain() // a dismissal holds for the quiet, not only the next release
		}
		if err := settings.Save(s); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
	// whether the agents' lists name a model with its provider's after it or
	// alone (#335): their files are written again, and Codex asks again
	mux.HandleFunc("POST /api/settings/plain-names", func(rw http.ResponseWriter, r *http.Request) {
		// Mode is on, own (#92: not on the names the user gave) or off; a
		// body of On alone is the two-way switch's, On meaning plain
		var in struct {
			On   bool
			Mode string
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		set := func() error { return provider.SetPlainNames(in.On) }
		if in.Mode != "" {
			set = func() error { return provider.SetSuffixMode(in.Mode) }
		}
		if err := set(); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
	// whether the gateway's /v1/models lists each model by its id without
	// the provider as well (settings.BareModelIDs, love1sbug on X)
	mux.HandleFunc("POST /api/settings/bare-ids", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ On bool }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if err := provider.SetBareModelIDs(in.On); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
	// the load over which a model its plugin says is busy goes behind
	// the others (settings.BusyLoad, axelburks on Discord): 0 for off
	mux.HandleFunc("POST /api/settings/busy-load", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ At int }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		s := settings.Load()
		if s.BusyLoad != in.At {
			s.BusyLoad = in.At
			if err := settings.Save(s); err != nil {
				fail(rw, err)
				return
			}
		}
		writeJSON(rw, settingsState())
	})
	// whether Codex's OpenAI models say multi-agent V1 (#141): Codex's lists
	// are written again and asked for again
	mux.HandleFunc("POST /api/settings/codex-agents-v1", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ On bool }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if err := provider.SetCodexAgentsV1(in.On); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
	// the Agents page's Get started card put away, or back (GuideHidden)
	mux.HandleFunc("POST /api/settings/guide", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Hidden bool }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		s := settings.Load()
		s.GuideHidden = in.Hidden
		if err := settings.Save(s); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
	// whether Claude Desktop lists a model of 1M or more once, by its 1M id
	// (settings.DesktopLongest, #1272): it reads the list as it starts
	mux.HandleFunc("POST /api/settings/desktop-longest", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ On bool }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		s := settings.Load()
		was := s.DesktopLongest
		s.DesktopLongest = in.On
		if err := settings.Save(s); err != nil {
			fail(rw, err)
			return
		}
		// a model listed by its 1M id leaves the window Claude Code is
		// told for Desktop's others, or joins it (#1458)
		if was != in.On {
			catalog.Touched()
		}
		writeJSON(rw, settingsState())
	})
	// whether Codex and Claude Code are told a model's whole context window
	// or the working one (settings.FullContext): their lists are written
	// again
	mux.HandleFunc("POST /api/settings/full-context", func(rw http.ResponseWriter, r *http.Request) {
		// At, when given, is a threshold to compact at instead, in
		// tokens: 0 is the working window again (#876)
		var in struct {
			On bool
			At *int
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		err := provider.SetFullContext(in.On)
		if err == nil && !in.On && in.At != nil {
			err = provider.SetCompactAt(*in.At)
		}
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
	// the longest context window any model is told (settings.ContextCap):
	// At 0 takes the cap away; a model's or provider's own window stays
	mux.HandleFunc("POST /api/settings/context-cap", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ At int }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if err := provider.SetContextCap(in.At); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
	// whether that longest context also holds a window the user set
	// (settings.ContextCapAll)
	mux.HandleFunc("POST /api/settings/context-cap-mode", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ All bool }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if err := provider.SetContextCapAll(in.All); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
	// an agent's own Long conversations (settings.AgentCompacts): Own
	// false follows the one for every agent again
	mux.HandleFunc("POST /api/settings/agent-compact", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Agent     string
			Own, Full bool
			At        int
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		var c *settings.AgentCompact
		if in.Own {
			c = &settings.AgentCompact{At: in.At, Full: in.Full}
		}
		if err := provider.SetAgentCompact(in.Agent, c); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
	// the windows one agent is told, apart from every other's
	// (settings.AgentContexts): Contexts replaces them, none takes them away
	mux.HandleFunc("POST /api/settings/agent-contexts", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Agent    string
			Contexts map[string]int
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		a, err := agent.Find(in.Agent)
		if err != nil {
			fail(rw, err)
			return
		}
		if err := provider.SetAgentContexts(a.ListsFor(), in.Contexts); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
	// an agent's own Provider in model names (settings.AgentSuffix): Mode
	// "" follows the one for every agent again
	mux.HandleFunc("POST /api/settings/agent-suffix", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Agent, Mode string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		a, err := agent.Find(in.Agent)
		if err != nil {
			fail(rw, err)
			return
		}
		if err := provider.SetAgentSuffix(a.ListsFor(), in.Mode); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
	// takes out of Codex's config the keys it reads before Long
	// conversations (agent.CodexCompactHeld), on the user's click
	mux.HandleFunc("POST /api/settings/codex-compact-held/drop", func(rw http.ResponseWriter, r *http.Request) {
		if err := agent.DropCodexCompactHolds(""); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
	// Codex's background compaction (#2490): on or off, and the share of
	// a model's window it starts at (0: Codex's own, 65), written into the
	// config Codex is routed through
	mux.HandleFunc("POST /api/settings/codex-async-compact", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			On      bool
			Percent *int
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		pct := settings.Load().CodexAsyncCompactPercent
		if in.Percent != nil {
			pct = *in.Percent
		}
		if err := provider.SetCodexAsyncCompact(in.On, pct); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
	// where Codex's request for a thread's description goes: "" as Codex
	// sends it, "off", or a model's id. It is separate from the title
	// setting.
	mux.HandleFunc("POST /api/settings/codex-descriptions", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Model string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		v := strings.TrimSpace(in.Model)
		if v != "" && v != "off" {
			if _, _, ok := provider.Resolve(v); !ok {
				fail(rw, fmt.Errorf("no model %s to write Codex's descriptions", v))
				return
			}
		}
		s := settings.Load()
		s.CodexDescriptions = v
		if err := settings.Save(s); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
	// where the Codex app's request to predict the user's next message
	// goes (#1970): "" as Codex sends it, "off", or a model's id
	mux.HandleFunc("POST /api/settings/codex-predictions", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Model string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		v := strings.TrimSpace(in.Model)
		if v != "" && v != "off" {
			if _, _, ok := provider.Resolve(v); !ok {
				fail(rw, fmt.Errorf("no model %s to write Codex's composer predictions", v))
				return
			}
		}
		s := settings.Load()
		s.CodexPredictions = v
		if err := settings.Save(s); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
	// where Codex's requests for a thread's title go (#705): "" as Codex
	// sends them, "off", or a model's id
	// the models turned off for every request through magpie (#1867), each
	// an id or a glob, the list sent whole
	mux.HandleFunc("POST /api/settings/disabled-models", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Models []string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if err := provider.SetDisabledModels(in.Models); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
	// Settings › Proxy's Test (#2076): the address Cloudflare sees a request
	// from through the proxy in force, and without it
	mux.HandleFunc("POST /api/proxy/check", handleProxyCheck)
	proxyRoutes(mux)
	mux.HandleFunc("POST /api/pipelines/check", handlePipelineCheck)
	mux.HandleFunc("POST /api/settings/codex-titles", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Model string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		v := strings.TrimSpace(in.Model)
		if v != "" && v != "off" {
			if _, _, ok := provider.Resolve(v); !ok {
				fail(rw, fmt.Errorf("no model %s to write Codex's titles", v))
				return
			}
		}
		s := settings.Load()
		s.CodexTitles = v
		if err := settings.Save(s); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
	// the model an agent's request masking would change goes to as
	// written (jessyoungyu on X): "" masks it as before; else a model whose
	// every route the user set to go unmasked (gateway.MaskRoute)
	mux.HandleFunc("POST /api/settings/redact-route", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Model string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		v := strings.TrimSpace(in.Model)
		if v != "" && !gateway.MaskRoute(v) {
			fail(rw, fmt.Errorf("%s can't take requests unmasked: pick a model on this computer or your network whose provider is set to Send requests unmasked, with no fallback elsewhere", v))
			return
		}
		s := settings.Load()
		s.RedactRouteTo = v
		if err := settings.Save(s); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
	// the model Codex's auto-review runs on (#938): "" as Codex picks it,
	// or a model's id, named in every entry of Codex's list
	mux.HandleFunc("POST /api/settings/codex-auto-review", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Model string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		v := strings.TrimSpace(in.Model)
		if v != "" && v != settings.CodexAutoReviewSession {
			// Resolve intentionally accepts arbitrary names under a known
			// provider. An approval reviewer must be a model or group magpie
			// actually lists, including providers kept unlisted for routing.
			if !slices.ContainsFunc(provider.Served(), func(e provider.Entry) bool { return e.ID == v || e.Group != "" && provider.SameRef(e.ID, v) }) {
				fail(rw, fmt.Errorf("no model %s for Codex's auto-review", v))
				return
			}
		}
		if err := provider.SetCodexAutoReview(v); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
	// the service tier every request of Codex's goes at (JackyFill on X):
	// "" as Codex asks it, "default" with none, "priority" or "ultrafast"
	mux.HandleFunc("POST /api/settings/codex-service-tier", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Tier string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if err := provider.SetCodexServiceTier(in.Tier); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
	// the Codex client magpie presents to the ChatGPT backend: "" (Codex
	// CLI) or "desktop" (ouhthuo on X)
	mux.HandleFunc("POST /api/settings/codex-client", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Client string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if err := provider.SetCodexClient(in.Client); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
	// what Codex opens in from its Agents row menu: "", "app" or
	// "terminal" (Yink88564391 on X)
	mux.HandleFunc("POST /api/settings/codex-open", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Open string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if err := provider.SetCodexOpen(in.Open); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
	// Warm up now: one Codex or Claude account's warm-up sent at once, and
	// why it failed
	mux.HandleFunc("POST /api/warm-now", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Agent, User string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if err := provider.WarmNow(r.Context(), in.Agent, in.User); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, map[string]bool{"ok": true})
	})
	// whether a Codex account spends one of its resets by itself once its
	// week is used up, the Usage card's toggle, set on its own
	// a Codex account's own daily warm-up time: "06:00", "off", or "" to
	// follow the one for all (#957)
	mux.HandleFunc("POST /api/settings/codex-warm-at", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ User, At string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if err := provider.SetCodexWarmAt(in.User, in.At); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
	// a Codex account's auto-use of its resets: at both times, or with When
	// ("week" or "expiring") at that one alone, the other kept (#2078)
	mux.HandleFunc("POST /api/settings/codex-auto-reset", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			User string
			On   bool
			When string
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if strings.TrimSpace(in.User) == "" {
			fail(rw, fmt.Errorf("which Codex account?"))
			return
		}
		set := func() error { return provider.SetCodexAutoReset(in.User, in.On) }
		if in.When != "" {
			set = func() error { return provider.SetCodexAutoResetCase(in.User, in.When, in.On) }
		}
		if err := set(); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
	// how long before a Codex reset about to run out unused it is spent,
	// for every account with "before one expires" on; 0 is the default
	mux.HandleFunc("POST /api/settings/codex-reset-lead", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Minutes int }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if err := provider.SetCodexResetLead(in.Minutes); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
	// whether a Codex account spends its credits once its allowance is
	// used up, the Usage card's toggle, set on its own, and with Last
	// whether only after everything else (left as it is when not sent)
	mux.HandleFunc("POST /api/settings/codex-credits", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			User  string
			On    bool
			Last  *bool
			First *bool
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if strings.TrimSpace(in.User) == "" {
			fail(rw, fmt.Errorf("which Codex account?"))
			return
		}
		// on alone keeps first or last as it was; either said sets it,
		// and one account has one of them
		mode := provider.CreditsOff
		if in.On {
			mode = provider.CreditsOn
			switch was := provider.CodexCreditsMode(in.User); {
			case in.Last != nil && *in.Last:
				mode = provider.CreditsLast
			case in.First != nil && *in.First:
				mode = provider.CreditsFirst
			case in.Last == nil && in.First == nil && (was == provider.CreditsLast || was == provider.CreditsFirst):
				mode = was
			}
		}
		if err := provider.SetCodexCreditsMode(in.User, mode); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
	// whether WorkBuddy's daily check-in is pressed each day, the Usage
	// card's toggle (#694), set on its own as Settings' is
	mux.HandleFunc("POST /api/settings/workbuddy-checkin", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ On bool }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		s := settings.Load()
		s.WorkBuddyCheckin = in.On
		if err := settings.Save(s); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
	// and Trae CN's (#694)
	mux.HandleFunc("POST /api/settings/trae-checkin", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ On bool }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		s := settings.Load()
		s.TraeCheckin = in.On
		if err := settings.Save(s); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
	// and MiniMax Code's (#811)
	mux.HandleFunc("POST /api/settings/minimax-checkin", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ On bool }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		s := settings.Load()
		s.MiniMaxCheckin = in.On
		if err := settings.Save(s); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
	// and Qoder's daily credits
	mux.HandleFunc("POST /api/settings/qoder-checkin", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ On bool }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		s := settings.Load()
		s.QoderCheckin = in.On
		if err := settings.Save(s); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
	// and a plugin's own daily check-in, by its provider
	mux.HandleFunc("POST /api/settings/plugin-checkin", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Provider string
			On       bool
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if in.Provider == "" {
			fail(rw, errors.New("no provider"))
			return
		}
		s := settings.Load()
		s.PluginCheckins = maps.Clone(s.PluginCheckins)
		if s.PluginCheckins == nil {
			s.PluginCheckins = map[string]bool{}
		}
		s.PluginCheckins[in.Provider] = in.On
		if err := settings.Save(s); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
	// and a switch a plugin declares for its provider (auth.options, #2038),
	// by the plugin's own provider id, handed to the running host at once
	mux.HandleFunc("POST /api/settings/plugin-option", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Provider string
			Key      string
			On       bool
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if in.Provider == "" || !pluginOptionKey.MatchString(in.Key) {
			fail(rw, errors.New("no provider or no such option"))
			return
		}
		s := settings.Load()
		s.PluginOptions = maps.Clone(s.PluginOptions)
		if s.PluginOptions == nil {
			s.PluginOptions = map[string]map[string]bool{}
		}
		own := maps.Clone(s.PluginOptions[in.Provider])
		if own == nil {
			own = map[string]bool{}
		}
		own[in.Key] = in.On
		s.PluginOptions[in.Provider] = own
		if err := settings.Save(s); err != nil {
			fail(rw, err)
			return
		}
		// best effort: a host that misses it reads them when it next starts
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		_ = plugin.SetProviderOptions(ctx)
		cancel()
		writeJSON(rw, settingsState())
	})
	// and one account's daily check-in, whichever vendor's (#1902): off
	// leaves it out while its vendor's is on
	mux.HandleFunc("POST /api/settings/checkin-account", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Key string
			On  bool
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if in.Key == "" {
			fail(rw, errors.New("no account"))
			return
		}
		s := settings.Load()
		off := slices.DeleteFunc(slices.Clone(s.CheckinOff), func(k string) bool { return k == in.Key })
		if !in.On {
			off = append(off, in.Key)
		}
		if len(off) == 0 {
			off = nil
		}
		s.CheckinOff = off
		if err := settings.Save(s); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
	// how large the window and the panel are drawn: Settings' choice and
	// Ctrl/Cmd +, − and 0 in either, set on its own so a key pressed while
	// the Settings page saves something else is never undone by it
	mux.HandleFunc("POST /api/settings/text-size", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Size int }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		s := settings.Load()
		s.TextSize = in.Size
		if err := settings.Save(s); err != nil {
			fail(rw, err)
			return
		}
		if w != nil {
			w.SetTextSize(in.Size)
		}
		writeJSON(rw, settingsState())
	})
	// the quick panel's shortcut (panelkey.go): bound in the app first, so
	// one the system refuses is said and not saved, the one before kept
	mux.HandleFunc("POST /api/settings/panel-shortcut", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Shortcut string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		key, err := panelShortcut(in.Shortcut, runtime.GOOS)
		if err != nil {
			fail(rw, err)
			return
		}
		s := settings.Load()
		if key != "" && !panelKeysState().Can {
			fail(rw, errors.New("This desktop doesn't let apps take a shortcut"))
			return
		}
		if onPanelShortcut != nil {
			if err := onPanelShortcut(key); err != nil {
				fail(rw, err)
				return
			}
		}
		was := s.PanelShortcut
		s.PanelShortcut = key
		if err := settings.Save(s); err != nil {
			if onPanelShortcut != nil {
				_ = onPanelShortcut(was) // the saved one bound again
			}
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
	// the Agents page's order and what it folds away, in magpie's settings
	mux.HandleFunc("POST /api/agents/arrange", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Order, Hidden, Shown []string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		s := settings.Load()
		s.AgentOrder, s.AgentsHidden, s.AgentsShown = in.Order, in.Hidden, in.Shown
		if err := settings.Save(s); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
	// an agent whose picks stay on this computer, or go to the others again
	// (settings' AgentsHere, chenxiang0010 on Discord)
	mux.HandleFunc("POST /api/agents/here", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Agent string
			On    bool
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if in.Agent = strings.TrimSpace(in.Agent); in.Agent == "" {
			fail(rw, errors.New("no agent"))
			return
		}
		s, err := settings.LoadChecked()
		if err != nil {
			fail(rw, err)
			return
		}
		s.AgentsHere = slices.DeleteFunc(s.AgentsHere, func(x string) bool { return x == in.Agent })
		if in.On {
			s.AgentsHere = append(s.AgentsHere, in.Agent)
		}
		if err := settings.Save(s); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
	// the Usage page's order of its cards, which the tray panel's Allowances
	// tab follows, what that tab leaves out, the clients the page's Agents
	// list leaves out, the allowance cards its Overview leaves out
	// (CardsHidden), and a client it counts as one of the agents (Count,
	// #1958; As "" counts it on its own again), in magpie's settings; one
	// not sent stays as it is
	mux.HandleFunc("POST /api/usage/arrange", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Order         *[]string
			PanelHidden   *[]string
			AgentsHidden  *[]string
			CardsHidden   *[]string
			WindowsHidden *[]string
			Count         *struct{ Client, As string }
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		s := settings.Load()
		if in.Count != nil {
			m, err := countClient(s.ClientAgents, in.Count.Client, in.Count.As)
			if err != nil {
				fail(rw, err)
				return
			}
			s.ClientAgents = m
		}
		if in.Order != nil {
			s.UsageOrder = *in.Order
		}
		if in.PanelHidden != nil {
			s.PanelUsageHidden = *in.PanelHidden
		}
		if in.AgentsHidden != nil {
			s.UsageAgentsHidden = *in.AgentsHidden
		}
		if in.CardsHidden != nil {
			s.UsageCardsHidden = *in.CardsHidden
		}
		if in.WindowsHidden != nil {
			s.UsageWindowsHidden = *in.WindowsHidden
		}
		if err := settings.Save(s); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
	mux.HandleFunc("POST /api/settings/login", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ On bool }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if err := autostart.Set(in.On); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
	// Sharing controls exposure; the gateway's named caller keys authenticate
	// remote clients just as they do local ones.
	mux.HandleFunc("POST /api/settings/lan", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ On, NewKey bool }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		// the agents reaching the gateway now: one in a WSL distro under NAT
		// is given the sharing key, which this changes
		on := agent.OnGateway()
		if err := access.ConfigureLAN(in.On, in.NewKey); err != nil {
			fail(rw, err)
			return
		}
		if gw := served.Load(); gw != nil {
			if err := gw.Relisten(); err != nil {
				fail(rw, err)
				return
			}
		}
		out := settingsState()
		if _, err := agent.Rekey(on); err != nil {
			out.Rekeyed = err.Error()
		}
		writeJSON(rw, out)
	})
	// the web pages that may call the gateway from a browser (#1051), as
	// their origins; none takes them all away
	mux.HandleFunc("POST /api/settings/cors", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Origins []string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		origins, err := settings.CleanOrigins(in.Origins)
		if err != nil {
			fail(rw, err)
			return
		}
		s := settings.Load()
		s.CORSOrigins = origins
		if err := settings.Save(s); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
	// the allow list (lc on Discord): the addresses other computers may
	// reach magpie from, as IPs and CIDR ranges; none lets every address in.
	// One that would shut out the page saving it (magpie web, opened from
	// another computer) is refused, and so is any while MAGPIE_ALLOW_IPS
	// sets the list.
	mux.HandleFunc("POST /api/settings/allowips", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ IPs []string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if _, env := gateway.AllowIPs(); env {
			fail(rw, fmt.Errorf("%s sets the allow list; change it there", gateway.AllowIPsEnv))
			return
		}
		list, err := settings.CleanAllowIPs(in.IPs)
		if err != nil {
			fail(rw, err)
			return
		}
		if addr, out := gateway.KeepsOut(list, r); out {
			fail(rw, fmt.Errorf("that allow list would shut out %s, the address this page is open from: add it first", addr))
			return
		}
		s, err := settings.LoadChecked()
		if err != nil {
			fail(rw, err)
			return
		}
		s.AllowIPs = list
		if err := settings.Save(s); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
	// the gateway's port (Magic_zero on Discord), set on its own: it moves
	// the gateway and every agent connected to it
	mux.HandleFunc("POST /api/settings/port", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Port int }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		res, err := setPort(in.Port)
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, map[string]any{"settings": settingsState(), "port": res})
	})
	// the user's own masking rules, all of them each time: set on their own,
	// so a pattern that doesn't compile is said and the rest are kept (#195)
	mux.HandleFunc("POST /api/settings/redact-rules", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Rules []redact.Rule }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		s := settings.Load()
		s.RedactRules = in.Rules
		if err := settings.Save(s); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
	// one masking rule changed in place (nf1qaz2w on Discord): the rule at
	// At, which must still be Was, becomes Rule, checked as an added one is.
	// A rule that can't be used is said and every rule is kept as it was.
	mux.HandleFunc("POST /api/settings/redact-rule", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			At   int         `json:"at"`
			Was  redact.Rule `json:"was"`
			Rule redact.Rule `json:"rule"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		s := settings.Load()
		next, err := editRedactRule(s.RedactRules, in.At, in.Was, in.Rule)
		if err != nil {
			fail(rw, err)
			return
		}
		s.RedactRules = next
		if err := settings.Save(s); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
	// what is done with a value of a kind found, other than masking it
	// (warn, block), all of them each time (#1977)
	mux.HandleFunc("POST /api/settings/redact-actions", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Actions map[string]string `json:"actions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		s := settings.Load()
		s.RedactActions = in.Actions
		if err := settings.Save(s); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
	// the values masking lets go as written, all of them each time (#1977)
	mux.HandleFunc("POST /api/settings/redact-allow", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Allow []string `json:"allow"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		s := settings.Load()
		s.RedactAllow = in.Allow
		if err := settings.Save(s); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
	// the GitHub token the library's requests to GitHub carry: set, or
	// taken away with ""
	mux.HandleFunc("POST /api/settings/github-token", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		tok := strings.TrimSpace(in.Token)
		if strings.ContainsFunc(tok, func(c rune) bool { return c <= ' ' || c == 0x7f }) {
			fail(rw, fmt.Errorf("a GitHub token is one word, without spaces"))
			return
		}
		s := settings.Load()
		s.GitHubToken = tok
		if err := settings.Save(s); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
	// the Bark target every alert also goes to, and the time of the daily
	// summary (EV_John_Liu on X). A field left out stays; the key, "" takes it
	// away. The key is written and never read back by the page.
	mux.HandleFunc("POST /api/settings/bark", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Server *string `json:"server"`
			Key    *string `json:"key"`
			Daily  *string `json:"daily"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		s := settings.Load()
		if in.Server != nil {
			s.BarkServer = *in.Server
		}
		if in.Key != nil {
			s.BarkKey = *in.Key
		}
		if in.Daily != nil {
			s.BarkDaily = *in.Daily
		}
		if err := settings.CheckBark(s.BarkServer, s.BarkKey, s.BarkDaily); err != nil {
			fail(rw, err)
			return
		}
		if err := settings.Save(s); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
	// a push to the saved Bark target, to see that it arrives
	mux.HandleFunc("POST /api/bark/test", func(rw http.ResponseWriter, r *http.Request) {
		s := settings.Load()
		t := barkTarget(s)
		if !t.On() {
			fail(rw, fmt.Errorf("no Bark device key is set"))
			return
		}
		title, body := barkTestText(trayLang(s.Lang, systemLang))
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		if err := bark.Push(ctx, t, title, body); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, map[string]bool{"ok": true})
	})
	// the GitHub download mirror updates come through (#893), the same
	// setting `magpie update mirror` sets: a full https:// prefix, or ""
	// for GitHub itself. The feed and its checksums are never the mirror's.
	mux.HandleFunc("POST /api/settings/update-mirror", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Mirror string `json:"mirror"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		m := strings.TrimSpace(in.Mirror)
		if m != "" {
			if err := update.CheckMirrorURL(m); err != nil {
				fail(rw, err)
				return
			}
		}
		s := settings.Load()
		s.UpdateMirror = m
		if err := settings.Save(s); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
	// a web search API added, given a new key or address, switched on or
	// off, or taken away; or the APIs put in a new order (#2286)
	mux.HandleFunc("POST /api/settings/search-api", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Vendor, Key, URL, Pick string
			Remove                 bool
			On                     *bool
			Arrange                []string
			// Append: the keys typed for an API added before go after its
			// own, which stay, rather than in their place
			Append bool
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		var err error
		switch {
		case in.Arrange != nil:
			err = provider.ArrangeSearchAPIs(in.Arrange)
		case in.Remove:
			err = provider.RemoveSearchAPI(in.Vendor)
		case in.On != nil:
			err = provider.SwitchSearchAPI(in.Vendor, *in.On)
		case in.Append && in.Key != "" && slices.ContainsFunc(provider.StoredSearchAPIs(), func(a provider.SearchAPI) bool { return a.Vendor == in.Vendor }):
			if err = provider.SetSearchAPI(provider.SearchAPI{Vendor: in.Vendor, URL: in.URL, Pick: in.Pick}); err == nil {
				_, _, err = provider.AddSearchKeys(in.Vendor, in.Key)
			}
		default:
			err = provider.SetSearchAPI(provider.SearchAPI{Vendor: in.Vendor, Key: in.Key, URL: in.URL, Pick: in.Pick})
		}
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
	// one search API's list of keys (辰林鸭~ on Discord): keys added, one
	// removed or switched on or off, or the list put in a new order, each
	// key named by its ref (provider.SearchKeyRef), never by itself
	mux.HandleFunc("POST /api/settings/search-keys", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Vendor, Add, Ref string
			Remove           bool
			On               *bool
			Arrange          []string
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		var err error
		added, had := 0, 0
		switch {
		case in.Arrange != nil:
			err = provider.ArrangeSearchKeys(in.Vendor, in.Arrange)
		case in.Add != "":
			added, had, err = provider.AddSearchKeys(in.Vendor, in.Add)
		case in.Remove:
			err = provider.RemoveSearchKey(in.Vendor, in.Ref)
		case in.On != nil:
			err = provider.SwitchSearchKey(in.Vendor, in.Ref, *in.On)
		default:
			err = fmt.Errorf("nothing to change")
		}
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, map[string]any{"settings": settingsState(), "added": added, "had": had})
	})
	// a search API's saved key, for its row's Show button (OnurBen on
	// Discord); like a provider's, it never leaves this machine
	mux.HandleFunc("POST /api/settings/search-key", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Vendor, Ref string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if in.Ref != "" { // one key of its list
			k, err := provider.SearchKeyOf(in.Vendor, in.Ref)
			if err != nil {
				fail(rw, err)
				return
			}
			writeJSON(rw, map[string]string{"key": k})
			return
		}
		for _, a := range provider.StoredSearchAPIs() {
			if a.Vendor == in.Vendor {
				writeJSON(rw, map[string]string{"key": a.Key})
				return
			}
		}
		fail(rw, fmt.Errorf("no search API %q", in.Vendor))
	})
	// what each of a search API's keys has used of its limit (SlouchyQuill
	// on Discord), the keys masked; fresh reads again what was read lately
	mux.HandleFunc("POST /api/settings/search-quota", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Vendor string
			Fresh  bool
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		for _, a := range provider.StoredSearchAPIs() {
			if a.Vendor == in.Vendor {
				qs := gateway.SearchQuotas(r.Context(), a, in.Fresh)
				if qs == nil {
					qs = []gateway.SearchKeyQuota{}
				}
				writeJSON(rw, map[string]any{"keys": qs})
				return
			}
		}
		fail(rw, fmt.Errorf("no search API %q", in.Vendor))
	})
	// the config folder only: the page names no path, so it can't open others
	mux.HandleFunc("POST /api/settings/reveal", func(rw http.ResponseWriter, r *http.Request) {
		if err := w.OpenFolder(settings.Dir()); err != nil {
			fail(rw, err)
			return
		}
		rw.WriteHeader(http.StatusNoContent)
	})
	// the app's log folder (applog), from Settings › About's Log row; the
	// page names no path here either
	mux.HandleFunc("POST /api/settings/log/reveal", func(rw http.ResponseWriter, r *http.Request) {
		path := applog.Current()
		if path == "" {
			fail(rw, errors.New("this magpie writes no log file"))
			return
		}
		if err := w.OpenFolder(filepath.Dir(path)); err != nil {
			fail(rw, err)
			return
		}
		rw.WriteHeader(http.StatusNoContent)
	})
	// the folder of the reports saved of requests held too long before
	// routing (gateway.HeldDir), from such a request's row on Routing
	mux.HandleFunc("POST /api/gateway/held/reveal", func(rw http.ResponseWriter, r *http.Request) {
		dir := gateway.HeldDir()
		if err := os.MkdirAll(dir, 0o700); err != nil {
			fail(rw, err)
			return
		}
		if err := w.OpenFolder(dir); err != nil {
			fail(rw, err)
			return
		}
		rw.WriteHeader(http.StatusNoContent)
	})
	// the agents a quit would cut off, for its question (quitask.go)
	mux.HandleFunc("GET /api/quit", func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, map[string]any{"agents": quitAgents()})
	})
	mux.HandleFunc("POST /api/window/{action}", func(rw http.ResponseWriter, r *http.Request) {
		switch r.PathValue("action") {
		case "hide":
			w.HidePanel()
		case "main":
			w.ShowMain(mainView(r.URL.Query()))
		case "quit":
			// the question's "Don't ask again" (noAsk=1, zhujiu on
			// Discord) is saved before the quit; a save that fails keeps
			// magpie open, saying why
			if r.URL.Query().Get("noAsk") == "1" {
				if err := settings.SetQuitNoAsk(true); err != nil {
					fail(rw, err)
					return
				}
			}
			// the user's Quit asks first while agents are connected
			// (quitask.go); the question's own Quit is now=1
			if a, ok := w.(quitAsker); ok && r.URL.Query().Get("now") == "" {
				go a.AskQuit()
			} else {
				w.Quit()
			}
		case "fit":
			if h, g, ok := parseFit(r.URL.Query()); ok {
				w.FitPanel(h, g)
			}
		case "tint":
			if c, ms, ok := parseTint(r.URL.Query()); ok && w.TintPanel(c, ms) {
				writeJSON(rw, map[string]bool{"ok": true})
				return
			}
		case "titlebar":
			if c, _, ok := parseTint(r.URL.Query()); ok && w.TintTitleBar(c, r.URL.Query().Get("dark") == "1") {
				writeJSON(rw, map[string]bool{"ok": true})
				return
			}
		}
		rw.WriteHeader(http.StatusNoContent)
	})
	agentModelsAPI(mux)
	speedsAPI(mux)
	modelPriceRoutes(mux)
	devListen(mux)
	return noFramedAPI(held(mux))
}

// noFramedAPI turns away a request for /api that no page of the window's
// own makes (apiFramed): a plugin's page in its frame among them.
func noFramedAPI(h http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if apiFramed(r) {
			http.Error(rw, "magpie's API answers magpie's own window only", http.StatusForbidden)
			return
		}
		h.ServeHTTP(rw, r)
	})
}

// held has a page's reads share one build of the catalog, which every row
// resolving its model rebuilt (provider.Hold): /api/state took 4s with a
// few hundred models. A write shares it too: a save answers with the whole
// state it wrote — every group's members, every model's facts — and that
// was a build per look-up, seconds on a slow disk. The write's own
// catalog.Touched drops what is held as it happens, so the answer is never
// what was read before it, and the request leaves nothing held for the next.
func held(h http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		api := strings.HasPrefix(r.URL.Path, "/api/")
		if api {
			defer provider.Hold()()
			// and the files read, not looked at again for each look-up
			defer filememo.Hold()()
		}
		if strings.HasPrefix(r.URL.Path, "/api/sessions") || strings.HasPrefix(r.URL.Path, "/api/usage") {
			// a page waits on the session files: the warm-up reading
			// them reads at full speed meanwhile
			defer sessions.Ask()()
			// and what the read threw away goes back once the page rests
			defer readingPage()()
			// and collected under GOGC 10 while it runs: a poll's reads
			// allocated ~330 MB to keep ~105 MB, and at the default 100 the
			// heap grew to twice what it kept before a collection, ~320 MB
			// in Activity Monitor during a poll. No memory limit: held as
			// close as what Go held, every poll collected over and over,
			// 5x the CPU.
			defer tightenGC()()
		}
		if !(api && r.Method == http.MethodGet) && r.URL.Path != "/api/update/using" {
			// anything that may have written drops what was held, once it
			// has answered with it (a page's word of input writes nothing)
			defer provider.Changed()
		}
		h.ServeHTTP(rw, r)
	})
}

func state() stateJSON {
	s := stateJSON{Agents: []agentJSON{}, Profiles: []profileJSON{}, Catalog: catalog.Source(), Settings: settings.Shared()} // only encoded, never changed
	// state() is asked for after nearly every click, so the rate — a
	// network fetch once every TTL — is only worth its rare latency when
	// cny is actually chosen; usd never looks at it
	if s.Settings.Currency == "cny" {
		s.FX = currentFX()
	}
	s.Unlisted = unlistedModels()
	s.CLIBehind = cliBehind()
	s.ConfigBlocked = configBlocked()
	if served.Load() == nil {
		s.GatewayOther = gatewayOther.Load()
		s.GatewayHeld = gateway.LastHeld()
	}
	for _, e := range provider.Served() {
		if e.Group == "" {
			s.Served++
		}
	}
	all := heldAgents()
	for _, a := range agent.ClientsIn(all) {
		s.Clients = append(s.Clients, clientJSON{ID: a.ID, Name: a.Name, Icon: a.Icon})
	}
	detected := agent.DetectedIn(all)
	now := make(map[string]map[string]string, len(detected))
	for _, a := range detected {
		if a.Follow != nil {
			_ = a.Follow()
		}
		vals := a.Values()
		now[a.ID] = vals
		aj := agentJSON{ID: a.ID, Name: a.Name, Icon: a.Icon, Path: tilde(a.Path), Fields: agentFields(a, vals)}
		aj.Models = agentModelCount(a, aj.Fields)
		aj.Menu = a.ListsModels
		aj.NoNames = a.NoNames
		aj.Drift = a.Drift()
		aj.Wired = a.Wired()
		if aj.Wired {
			aj.StaleCopies = a.StaleCopies()
			aj.Stale = len(aj.StaleCopies)
			aj.Joined = a.Joined != nil && a.Joined()
			if a.AppOut != nil && a.AppOut() {
				aj.AppOut = codexAppOut()
			} else if a.ID == "codex" && a.WSL == "" {
				aj.AppBack = provider.CodexAppBack()
			}
		} else {
			aj.Source = a.Source()
			aj.Failover = a.FailingOver != nil && a.FailingOver()
		}
		if a.ID == "codex" && a.WSL == "" {
			aj.AppSnag = provider.CodexAppSnagged()
		}
		aj.AppRestart = a.AppBehind()
		if a.ID == "claude-desktop" && a.WSL == "" {
			if iso, ok := agent.DesktopIsolatedHere(); ok {
				aj.Isolated = &iso
			}
		}
		if a.Telemetry != nil {
			// Settings › Privacy's switch for every agent reaches one
			// found since as it is seen
			all := agent.TelemetryAllOff()
			var allErr error
			if all {
				allErr = agent.KeepTelemetryOff(a)
			}
			st, err := a.Telemetry.Get()
			st.All = all
			if allErr != nil && err == nil {
				st.AllError = allErr.Error()
			}
			if err != nil {
				st.Error = err.Error()
			}
			aj.Telemetry = &st
		}
		if a.Import != nil {
			aj.Import = a.Import()
			if a.Added != nil {
				if added, known := a.Added(); known {
					aj.Added = &added
				}
			}
		}
		if a.Launch != nil {
			aj.Launch = a.Launch()
		}
		if a.Typed != nil && aj.Launch != "" {
			aj.Typed = a.Typed()
		}
		aj.CLIMissing = a.CLIMissing()
		// as agentCommand makes it, Launch asked once
		aj.Command = agentLine(a, aj.Launch, runtime.GOOS)
		aj.Terminal = terminalHere() && aj.Command != ""
		aj.OpenApp = a.ID == "codex" && codexOpensApp()
		if a.Native != nil {
			native := a.Native.Read()
			aj.Native = &native
		}
		s.Agents = append(s.Agents, aj)
	}
	if ps, err := profile.Load(); err == nil {
		for _, n := range profile.Names(ps) {
			pj := profileJSON{Name: n, Summary: profile.Summary(ps[n]), Agents: profile.DetailsIn(ps[n], all), InUse: profile.InUse(ps[n], detected, now)}
			if l := ps[n].Library; l != nil {
				servers, skills := l.On()
				pj.Library = &profileLibraryJSON{Servers: servers, Skills: skills, Instructions: l.GivesInstructions()}
			}
			s.Profiles = append(s.Profiles, pj)
		}
	}
	s.Recovered = lastgood.Recovered()
	return s
}

// signInOpened is a sign-in as it starts, with why its page didn't open in
// the browser: the window then shows the link to open by hand, instead of
// waiting on a page that never opened until the sign-in times out (#2043).
type signInOpened struct {
	provider.SignInState
	OpenError string `json:"openError,omitempty"`
	// LinkOnly is a sign-in whose page magpie didn't open, as the user
	// asked (settings.SignInLinkOnly): the window says to copy its link
	LinkOnly bool `json:"linkOnly,omitempty"`
}

// openSignIn opens an account sign-in's page, unless the user keeps those
// pages to a browser of their own (settings.SignInLinkOnly), and says how
// it went for signInOpened. linkOnly is this sign-in alone kept to its link
// (the window's "Sign in on another computer"), whatever the setting.
func openSignIn(w Windows, st provider.SignInState, linkOnly bool) signInOpened {
	if linkOnly || settings.Load().SignInLinkOnly {
		return signInOpened{SignInState: st, LinkOnly: true}
	}
	var failed string
	if w != nil && !st.Opened {
		failed = openError(w, st.URL)
	}
	return signInOpened{SignInState: st, OpenError: failed}
}

// mcpSignInOpened is signInOpened for a sign-in to an MCP server.
type mcpSignInOpened struct {
	mcpauth.State
	OpenError string `json:"openError,omitempty"`
	LinkOnly  bool   `json:"linkOnly,omitempty"`
}

// openMCPSignIn is openSignIn for a sign-in to an MCP server: kept to its
// link when the user keeps sign-in pages to a browser of their own
// (settings.SignInLinkOnly), as an account's is. It opened the page in the
// default browser whatever the setting (hkxhkxhkx on X).
func openMCPSignIn(w Windows, st mcpauth.State) mcpSignInOpened {
	if settings.Load().SignInLinkOnly {
		return mcpSignInOpened{State: st, LinkOnly: true}
	}
	if u, err := url.Parse(st.URL); st.URL != "" && (err != nil || !mcpauth.WebURL(u)) {
		// the server named it: only a web page is handed to the system
		return mcpSignInOpened{State: st, OpenError: "not a web page"}
	}
	return mcpSignInOpened{State: st, OpenError: openError(w, st.URL)}
}

// openError opens url in the browser, and says why it didn't open: "" when
// it did, or when there is no url.
func openError(w Windows, url string) string {
	if url == "" {
		return ""
	}
	if err := w.OpenURL(url); err != nil {
		return err.Error()
	}
	return ""
}

func writeJSON(rw http.ResponseWriter, v any) {
	rw.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(rw).Encode(v)
}

// writeJSONSame is writeJSON for a read the page asks for again and again:
// the answer's X-Magpie-Tag is a hash of its bytes, and a request whose
// X-Magpie-Had is that tag gets X-Magpie-Same and no body, which the page
// reads as the answer it has (api in app.js). The Sessions tab read a long
// history's ~8 MB list and ~0.7 MB of stats every 15 s, and parsed and
// compared them whole to find them the same. Not an ETag and a 304:
// WebView2 is sent no If-None-Match, and Wails turns its 304 into a 505.
// It returns the tag, or "" when v could not be written.
func writeJSONSame(rw http.ResponseWriter, r *http.Request, v any) string {
	return writeSame(rw, r, func(w io.Writer) error { return json.NewEncoder(w).Encode(v) })
}

// writeSame is writeJSONSame of the answer write writes to the writer it
// is given, called twice: once into the hash, for the tag, and once
// straight to rw when the page hasn't got it. The answer is never held
// whole: the stats' ~6 MB each, in buffers kept between polls, were
// 12.5 MB of a warm start's 169 MB live heap over a full history (#2278).
func writeSame(rw http.ResponseWriter, r *http.Request, write func(w io.Writer) error) string {
	h := sha256.New()
	if err := write(h); err != nil {
		fail(rw, err)
		return ""
	}
	tag := hex.EncodeToString(h.Sum(nil)[:16])
	rw.Header().Set("X-Magpie-Tag", tag)
	if r.Header.Get("X-Magpie-Had") == tag {
		rw.Header().Set("X-Magpie-Same", "1")
		rw.WriteHeader(http.StatusOK)
		return tag
	}
	rw.Header().Set("Content-Type", "application/json")
	out := bufio.NewWriterSize(rw, 32<<10)
	_ = write(out) // as the first time: it was written without a fault
	_ = out.Flush()
	return tag
}

// sentRows is each row's hash in the last few lists writeJSONRows sent, by
// the answer's tag, oldest first: a few, for a window and a browser tab or
// two asking at once.
var sentRows struct {
	sync.Mutex
	tags []string
	rows map[string][][16]byte
}

const sentRowsKept = 4

// writeJSONRows is writeJSONSame for an answer that is a long list, key,
// beside the fields of rest: one asked with the tag of a list sent before
// that changed since gets X-Magpie-Delta: key, and its list holds only the
// rows not in that one, each run of rows that was, in its order, written
// [from, count] of the list the page has. While an agent works, its
// session's row changes every poll, and the Sessions tab read all ~5000
// rows (~8 MB) again for it.
//
// The answer is never held whole. A first pass encodes each row into one
// small buffer, for its hash and the answer's tag; what is sent is
// encoded again, straight to rw, and in a delta only the rows the page
// hasn't got. The whole answer, built in a buffer to hash and send, was
// 25 MB of a warm start's 183 MB live heap over a full history (16,600
// sessions), kept between polls and grown again by each (#2278).
// It returns the tag, or "" when a row could not be encoded.
func writeJSONRows[T any](rw http.ResponseWriter, r *http.Request, key string, rows []T, rest any) string {
	tail, err := json.Marshal(rest)
	if err != nil {
		fail(rw, err)
		return ""
	}
	k, _ := json.Marshal(key)
	head := append(append([]byte{'{'}, k...), ":["...)
	end := []byte{']'}
	if len(tail) > 2 {
		end = append(append(end, ','), tail[1:]...)
	} else {
		end = append(end, '}')
	}
	end = append(end, '\n')
	var row bytes.Buffer
	enc := json.NewEncoder(&row)
	// encode is row i as Marshal gives it: Encode ends it with a newline.
	// It is handed a pointer: rows[i] as an any was a copy of the row
	// allocated for each row of each poll, ~2.7 MB a Sessions poll.
	encode := func(i int) ([]byte, error) {
		row.Reset()
		if err := enc.Encode(&rows[i]); err != nil {
			return nil, err
		}
		return row.Bytes()[:row.Len()-1], nil
	}
	whole := sha256.New()
	whole.Write(head)
	sums := make([][16]byte, len(rows))
	for i := range rows {
		b, err := encode(i)
		if err != nil {
			fail(rw, err)
			return ""
		}
		if i > 0 {
			whole.Write([]byte{','})
		}
		whole.Write(b)
		sum := sha256.Sum256(b)
		sums[i] = [16]byte(sum[:16])
	}
	whole.Write(end)
	tag := hex.EncodeToString(whole.Sum(nil)[:16])
	rw.Header().Set("X-Magpie-Tag", tag)
	had := r.Header.Get("X-Magpie-Had")
	if had == tag {
		rw.Header().Set("X-Magpie-Same", "1")
		rw.WriteHeader(http.StatusOK)
		return tag
	}
	sentRows.Lock()
	was, ok := sentRows.rows[had]
	if _, kept := sentRows.rows[tag]; !kept {
		if sentRows.rows == nil {
			sentRows.rows = map[string][][16]byte{}
		}
		if len(sentRows.tags) == sentRowsKept {
			delete(sentRows.rows, sentRows.tags[0])
			sentRows.tags = sentRows.tags[1:]
		}
		sentRows.tags = append(sentRows.tags, tag)
		sentRows.rows[tag] = sums
	}
	sentRows.Unlock()
	rw.Header().Set("Content-Type", "application/json")
	out := bufio.NewWriterSize(rw, 32<<10)
	defer out.Flush()
	out.Write(head)
	first := true
	item := func(b []byte) {
		if !first {
			out.WriteByte(',')
		}
		first = false
		out.Write(b)
	}
	if !ok || had == "" {
		for i := range rows {
			b, err := encode(i)
			if err != nil {
				// the first pass encoded it: nothing else changes a row
				return tag
			}
			item(b)
		}
		out.Write(end)
		return tag
	}
	at := make(map[[16]byte][]int, len(was))
	for j, s := range was {
		at[s] = append(at[s], j)
	}
	rw.Header().Set("X-Magpie-Delta", key)
	from, n := 0, 0
	run := func() {
		if n > 0 {
			item([]byte("[" + strconv.Itoa(from) + "," + strconv.Itoa(n) + "]"))
		}
		n = 0
	}
	for i, s := range sums {
		js := at[s]
		if len(js) == 0 {
			run()
			b, err := encode(i)
			if err != nil {
				return tag
			}
			item(b)
			continue
		}
		j := js[0]
		at[s] = js[1:]
		if n > 0 && j == from+n {
			n++
			continue
		}
		run()
		from, n = j, 1
	}
	run()
	out.Write(end)
	return tag
}

func fail(rw http.ResponseWriter, err error) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(http.StatusBadRequest)
	out := map[string]string{"error": err.Error()}
	var unavailable *agent.RuntimeUnavailableError
	if errors.As(err, &unavailable) {
		out["code"] = "runtime_unavailable"
		out["offline"] = string(unavailable.Offline)
	}
	// an app's import link that nothing here opens, or that opens a
	// program which is gone, said in the reader's language
	var noHandler *agent.NoLinkHandlerError
	if errors.As(err, &noHandler) {
		out["code"], out["app"], out["exe"], out["said"] = "no_link_handler", noHandler.App, noHandler.Exe, linkSaid[noHandler.Exe != ""]
	}
	// a save refused because a folder on the way to magpie's own isn't the
	// user's: the window offers to make it theirs again
	if permissionDenied(err) {
		if b := configBlocked(); b != "" {
			out["code"], out["blocked"] = "config_blocked", b
		}
	}
	var noModels *agent.NoModelsError
	if errors.As(err, &noModels) {
		out["code"] = "no_models"
		out["agent"] = noModels.Agent
	}
	// magpie has models, and none is left in the agent's lists (#1989)
	var allHidden *agent.AllHiddenError
	if errors.As(err, &allHidden) {
		out["code"] = "all_hidden"
		out["agent"] = allHidden.Agent
	}
	var signedIn *provider.SignedInError
	if errors.As(err, &signedIn) {
		out["code"], out["agent"], out["user"] = "signed_in", signedIn.Agent, signedIn.User
		if signedIn.Only {
			out["code"] = "signed_in_only"
		}
	}
	// a text that isn't a group to import (laincatx on X), said in the
	// reader's language
	var share *provider.ShareError
	if errors.As(err, &share) {
		out["code"], out["share"] = "share", share.Why
		out["format"], out["models"] = strconv.Itoa(share.Format), strings.Join(share.Models, ", ")
	}
	// an agent's npm update, or RTK's winget, that needed an administrator
	// and didn't get one, said in the reader's language
	var admin *agent.AdminError
	if errors.As(err, &admin) {
		out["code"], out["agent"], out["prefix"], out["command"], out["via"] = "needs_admin", admin.Agent, admin.Prefix, admin.Command, admin.Via
		if admin.Canceled {
			out["canceled"] = "1"
		} else if admin.Err != nil {
			out["reason"] = admin.Err.Error()
		}
	}
	// a bun this Windows or processor can't run at all: said, with what
	// to do, in the reader's language (zgcwkj on Discord)
	var bun *plugin.BunUnstartableError
	if errors.As(err, &bun) {
		out["code"], out["bun"], out["bunWhy"] = "bun_unstartable", bun.Bun, bun.Why
		out["said"] = bunSaid[bun.Why]
	}
	// a proxy's web page inside the text: its title, said in the reader's
	// language, never the markup
	if what, ok := webPageSaid(out["error"]); ok && out["said"] == "" {
		out["error"], out["said"], out["what"] = "A proxy in front of magpie answered, not magpie ("+what+")", "A proxy in front of magpie answered, not magpie ({what})", what
	}
	_ = json.NewEncoder(rw).Encode(out)
}

func tilde(p string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, home) {
		return "~" + p[len(home):]
	}
	return p
}

// loginUsers is agent's accounts signed in, by name, each once.
func loginUsers(agent string) []string {
	var out []string
	for _, l := range provider.Logins(agent) {
		if l.User != "" && !slices.ContainsFunc(out, func(u string) bool { return strings.EqualFold(u, l.User) }) {
			out = append(out, l.User)
		}
	}
	return out
}

// editRedactRule is rules with the one at at, which must still be was,
// changed to to: tidied and checked as an added rule is, in the same place.
// It is refused when that rule is no longer there (changed or removed
// meanwhile), when to has neither a prefix nor a pattern (taking a rule
// away is Remove), and when to is another rule already listed.
func editRedactRule(rules []redact.Rule, at int, was, to redact.Rule) ([]redact.Rule, error) {
	if at < 0 || at >= len(rules) || rules[at] != was {
		return nil, fmt.Errorf("that masking rule is no longer there: it was changed or removed meanwhile")
	}
	tidy, err := redact.CheckRules([]redact.Rule{to})
	if err != nil {
		return nil, err
	}
	if len(tidy) == 0 {
		return nil, fmt.Errorf("a masking rule needs a prefix or a regular expression")
	}
	for i, o := range rules {
		if i != at && o == tidy[0] {
			return nil, fmt.Errorf("the masking rule %s is there already", o.Kind)
		}
	}
	next := slices.Clone(rules)
	next[at] = tidy[0]
	return next, nil
}

// countClient is the user's ClientAgents with client counted as the agent
// as (#1958), or on its own again when as is "". Only a client magpie
// doesn't know is counted as another, and only as an agent it knows: an
// agent counted as another would be lost from its own row, and magpie's
// own calls are kept apart from the agents' (#1839).
func countClient(m map[string]string, client, as string) (map[string]string, error) {
	client, as = strings.TrimSpace(client), strings.TrimSpace(as)
	if client == "" {
		return nil, fmt.Errorf("no client to count")
	}
	out := maps.Clone(m)
	if out == nil {
		out = map[string]string{}
	}
	if as == "" {
		delete(out, strings.ToLower(client))
		return out, nil
	}
	if usage.IsAgent(client) || client == "other" || usage.AgentOf(client) == "magpie" || strings.HasPrefix(strings.ToLower(client), "magpie") {
		return nil, fmt.Errorf("%s is counted on its own", client)
	}
	if !usage.IsAgent(as) || as == "magpie" {
		return nil, fmt.Errorf("%s is not an agent magpie knows", as)
	}
	out[strings.ToLower(client)] = as
	return out, nil
}

// bunSaid is what the page says for a bun that can't start, by why: an
// i18n.js key the page's api() puts through t(), kept here so app.js
// carries one line for it, not both sentences
var bunSaid = map[string]string{
	"windows": "Bun, which runs plugins, can't start on this version of Windows: it needs Windows 10 version 1809 or later, or Windows Server 2019 or later. Update Windows to use plugins, or set MAGPIE_BUN to a bun that runs here.",
	"cpu":     "Bun, which runs plugins, can't start on this computer's processor: it needs an x64 processor with SSE4.2 (Intel or AMD from 2008 on; in a virtual machine, a CPU type that passes it through). Run magpie on such a processor to use plugins, or set MAGPIE_BUN to a bun that runs here.",
}

// linkSaid is what the page says for an app's import link that nothing
// here opens, by whether it names a program that is gone: an i18n.js key
// like bunSaid's, with {app} and {exe} filled from the reply
var linkSaid = map[bool]string{
	false: "Nothing on this computer opens {app}'s links, so {app} didn't open: open {app} once or install it again, then click Add magpie again",
	true:  "Nothing on this computer opens {app}'s links, so {app} didn't open: open {app} once or install it again, then click Add magpie again ({exe})",
}

// appOutJSON is Codex on magpie while its ChatGPT account is used up: the
// account, when it has room again (zero when the reading doesn't say), and
// whether a Codex app is left on the account, its app-server not
// restarted (provider.CodexAppStuck), for the user to reopen it.
type appOutJSON struct {
	User  string     `json:"user,omitempty"`
	Until *time.Time `json:"until,omitempty"`
	Stuck bool       `json:"stuck,omitempty"`
}

func codexAppOut() *appOutJSON {
	o := &appOutJSON{Stuck: provider.CodexAppStuck()}
	for _, l := range provider.Logins("codex") {
		if l.Active {
			o.User = l.User
		}
	}
	if u := provider.CodexOutUntil(); !u.IsZero() {
		o.Until = &u
	}
	return o
}
