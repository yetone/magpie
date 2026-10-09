// Package gui hosts the desktop app: a tray panel and a regular window that
// share one small web UI. The UI talks to Go over a tiny JSON API served by
// the same handler that serves the static assets, so no binding generator or
// bundler is involved.
package gui

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/autostart"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/fonts"
	"github.com/yetone/magpie/internal/fx"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/library"
	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/profile"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/redact"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/update"
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

//go:embed assets
var assets embed.FS

// Windows is what the API needs from the host application.
type Windows interface {
	HidePanel()
	// ShowMain brings the window up, on the named tab when view is set.
	ShowMain(view string)
	Quit()
	// OpenURL hands a link to the system browser.
	OpenURL(url string)
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
	// whether it has magpie already
	Import string `json:"import,omitempty"`
	Added  bool   `json:"added,omitempty"`
	// Launch: the command that starts an agent taking the gateway only
	// from its environment (agy) on magpie, to copy
	Launch string `json:"launch,omitempty"`
	// Models: how many of the catalog its lists show, for an agent that
	// picks among it (agent_models.go)
	Models *modelCountJSON `json:"models,omitempty"`
	// Source: what an agent not connected runs on now (agent.Source), and
	// Stale: copies of a connected one still running on the list they
	// started with (agent.Stale), for the line under its name
	Source string `json:"source,omitempty"`
	Stale  int    `json:"stale,omitempty"`
	// StaleCopies: those copies, each with what it is and when it
	// started, for the opened row to say which is left to reopen and how
	StaleCopies []agent.StaleCopy `json:"staleCopies,omitempty"`
	// Joined: connected with its own models still in its list (Codex
	// signed in with ChatGPT, agent.Agent.Join)
	Joined bool `json:"joined,omitempty"`
	// Failover: not connected, yet its requests go through magpie for
	// account failover alone (agent.Agent.FailingOver, #1385)
	Failover bool `json:"failover,omitempty"`
	// CLIMissing: its settings are here, its CLI isn't (#843), which
	// the row says, and Install another agent offers it again
	CLIMissing bool               `json:"cliMissing,omitempty"`
	Native     *agent.NativeState `json:"native,omitempty"`
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
	CLIBehind string            `json:"cliBehind,omitempty"`
	Settings  settings.Settings `json:"settings"`
	// FX is the dollar-to-yuan rate the cny currency choice shows costs at,
	// here too (not only in settingsJSON) so a cost drawn before the reader
	// ever opens Settings already converts, if cny was chosen last time.
	FX fxJSON `json:"fx"`
	// Unlisted are the models kept for routing groups, which the pickers
	// don't offer: a filter that finds one of them says why it isn't there
	Unlisted []unlistedJSON `json:"unlisted,omitempty"`
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
	// MAGPIE_ADDR, when it sets the gateway's address over Settings' port
	AddrEnv string `json:"addrEnv,omitempty"`
	// Dir is the data folder beside a portable magpie (#508)
	Portable bool `json:"portable,omitempty"`
	// WSL is whether there is WSL to look in for agents (Windows), for
	// Settings' Detect agents in WSL (#1264)
	WSL bool `json:"wsl,omitempty"`
	// Mac apps that explicitly handle .command files, for resumed sessions.
	TerminalApps    []terminalChoice `json:"terminalApps,omitempty"`
	TerminalDefault string           `json:"terminalDefault,omitempty"`
	OTelEnv         bool             `json:"otelEnv,omitempty"`
	// whether the page is in gateway mode and why (gatewayMode); Web says
	// it is served by magpie web, where the mode can be set
	GatewayOn  bool   `json:"gatewayOn,omitempty"`
	GatewayWhy string `json:"gatewayWhy,omitempty"`
	Web        bool   `json:"web,omitempty"`
	// the proxy vendor requests go through now, and where it came from:
	// settings, environment, system, off or none
	ProxyNow    string `json:"proxyNow"`
	ProxySource string `json:"proxySource"`
	// whether magpie opens at login: the system's record, not a setting
	Login bool `json:"login"`
	// the model that describes images when Vision names none, and those
	// that can be named
	VisionAuto   string     `json:"visionAuto,omitempty"`
	VisionModels []modelRef `json:"visionModels"`
	// the Vision the user picked when magpie can't find it any more:
	// VisionAuto describes in its place, and the row says so
	VisionMissing string `json:"visionMissing,omitempty"`
	// the models Codex's thread titles may be sent to (CodexTitles, #705)
	TitleModels []modelRef `json:"titleModels"`
	// the model magpie's generate_image tool draws with when ImageGen
	// names none, and those that can be named
	ImageGenAuto string `json:"imageGenAuto,omitempty"`
	// the ImageGen the user picked when magpie can't find it any more
	ImageGenMissing string     `json:"imageGenMissing,omitempty"`
	ImageGenModels  []modelRef `json:"imageGenModels"`
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
	SearchRelays  []string           `json:"searchRelays,omitempty"`
	// the providers on that aren't offered, as they can't search the web
	// by themselves (#825)
	SearchLeftOut []string `json:"searchLeftOut,omitempty"`
	// the GitHub token the library asks GitHub with, masked, and where it
	// is from ("settings", GITHUB_TOKEN or GH_TOKEN); never the token
	GitHubTokenMask string `json:"githubTokenMask,omitempty"`
	GitHubTokenFrom string `json:"githubTokenFrom,omitempty"`
	// where other machines reach the gateway while it is shared
	LANURLs []string `json:"lanURLs,omitempty"`
	// LANURLs are a container's own addresses, not the host's: the page
	// offers the one it was opened at instead, or says how to set it
	LANContainer bool `json:"lanContainer,omitempty"`
	// when the Codex warm-up last started an account's window
	CodexWarmed *time.Time `json:"codexWarmed,omitempty"`
	// and the Claude warm-up
	ClaudeWarmed *time.Time `json:"claudeWarmed,omitempty"`
	// the ChatGPT accounts signed in, each of which can have a daily
	// warm-up time of its own (#957)
	CodexUsers []string `json:"codexUsers,omitempty"`
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
	// Own is a Google sign-in, which searches for its own models first
	Own bool `json:"own,omitempty"`
}

type searchVendorJSON struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	KeysURL string `json:"keysURL,omitempty"`
	NeedURL bool   `json:"needURL,omitempty"` // one the user runs
}

func searchState(s *settingsJSON) {
	names := map[string]string{}
	for _, e := range provider.Served() {
		names[e.ID] = cmp.Or(e.Name, e.Model)
	}
	s.SearchAPIs, s.SearchVendors = []searchAPIJSON{}, []searchVendorJSON{}
	for _, a := range provider.StoredSearchAPIs() {
		j := searchAPIJSON{Vendor: a.Vendor, Name: a.Name(), URL: a.URL, Ready: a.Ready()}
		if a.Key != "" {
			j.Key = provider.Mask(a.Key)
		}
		s.SearchAPIs = append(s.SearchAPIs, j)
	}
	for _, v := range provider.SearchVendors {
		s.SearchVendors = append(s.SearchVendors, searchVendorJSON{ID: v.ID, Name: v.Name, KeysURL: v.KeysURL, NeedURL: v.Base == ""})
	}
	s.SearchProvider = gateway.Searcher()
	s.SearchAuto, s.SearchUnused = gateway.AutoSearcher(), gateway.SearcherUnused()
	s.SearchChoices = []searchChoiceJSON{}
	for _, c := range gateway.Searchers() {
		p := c.Provider
		j := searchChoiceJSON{ID: p.ID, Name: p.Name, Icon: p.Icon, Small: c.Small, SmallName: names[p.ID+"/"+c.Small], Models: []modelRef{}, Service: c.Service, Own: c.Own}
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

func settingsState() settingsJSON {
	s := settingsJSON{Settings: settings.Load(), Version: Version, Dir: tilde(settings.Dir()), Portable: settings.Portable() != "", Gateway: gateway.URL()}
	s.AddrEnv = os.Getenv("MAGPIE_ADDR")
	s.WSL = runtime.GOOS == "windows"
	s.Web = webPage.Load()
	s.GatewayOn, s.GatewayWhy = gatewayMode(s.Web)
	s.LANKey = "" // the retained credential belongs on disk, not in UI state
	// the GitHub token, masked, and where the library's requests take one
	// from: Settings, or the environment variable named
	s.GitHubToken = ""
	if tok, from := library.GitHubToken(); tok != "" {
		s.GitHubTokenMask, s.GitHubTokenFrom = provider.Mask(tok), from
	}
	if found, err := discoverTerminals(); err == nil {
		for _, app := range found.Apps {
			s.TerminalApps = append(s.TerminalApps, terminalChoice{ID: app.ID, Name: app.Name})
		}
		s.TerminalDefault = found.Default
	}
	s.FX = currentFX()
	if (s.UsageAlert > 0 || s.BalanceAlert > 0 || s.ResetReminder > 0) && notifyProblem != nil {
		s.NotifyProblem = notifyProblem()
	}
	s.ProxyNow, s.ProxySource = netproxy.Describe()
	for _, name := range []string{"MAGPIE_OTEL_ENABLED", "MAGPIE_OTEL_ENDPOINT", "MAGPIE_OTEL_HEADERS", "MAGPIE_OTEL_METRICS", "MAGPIE_OTEL_BODIES", "MAGPIE_OTEL_BODIES_WHOLE", "MAGPIE_OTEL_SESSIONS"} {
		if _, ok := os.LookupEnv(name); ok {
			s.OTelEnv = true
		}
	}
	s.Login = autostart.Enabled()
	if s.LAN {
		s.LANURLs, s.LANContainer = gateway.LANURLs(), gateway.ContainerAddrs()
	}
	s.CodexWarmed, s.ClaudeWarmed = latest(provider.CodexWarmed()), latest(provider.ClaudeWarmed())
	s.CodexUsers = codexUsers()
	s.WorkBuddy, s.WorkBuddyCheckins = provider.HasWorkBuddy(), provider.WorkBuddyCheckins()
	s.Trae, s.TraeCheckins = provider.HasTrae(), provider.TraeCheckins()
	s.MiniMax, s.MiniMaxCheckins = provider.HasMiniMax(), provider.MiniMaxCheckins()
	s.Qoder, s.QoderCheckins = provider.HasQoder(), provider.QoderCheckins()
	s.CheckinPlugins = provider.PluginCheckins()
	s.VisionAuto, s.VisionModels, s.VisionMissing = gateway.AutoVision(), []modelRef{}, gateway.VisionMissing()
	for _, e := range provider.Served() {
		if e.Images && (e.ImageInput == nil || *e.ImageInput) && (e.Group != "" || e.Provider.Ready()) {
			m := modelRef{ID: e.ID, Name: e.Name, Provider: e.Provider.ID, PName: e.Provider.Name, Icon: e.Provider.Icon}
			if e.Group != "" {
				m.Provider, m.PName = "", e.Group
			}
			s.VisionModels = append(s.VisionModels, m)
		}
	}
	s.TitleModels = []modelRef{}
	for _, e := range provider.Served() {
		if e.Group != "" || e.Provider.Ready() && !e.Provider.DecideOnly() {
			m := modelRef{ID: e.ID, Name: e.Name, Provider: e.Provider.ID, PName: e.Provider.Name, Icon: e.Provider.Icon}
			if e.Group != "" {
				m.Provider, m.PName = "", e.Group
			}
			s.TitleModels = append(s.TitleModels, m)
		}
	}
	searchState(&s)
	s.ImageGenAuto, s.ImageGenModels, s.ImageGenMissing = gateway.AutoDrawer(), []modelRef{}, gateway.DrawerMissing()
	for _, p := range provider.All() {
		if !p.On() || p.DecideOnly() {
			continue
		}
		for _, m := range gateway.Drawers(p) {
			name := m.Name
			if name == "" {
				name = m.ID
			}
			s.ImageGenModels = append(s.ImageGenModels, modelRef{ID: p.ID + "/" + m.ID, Name: name, Provider: p.ID, PName: p.Name, Icon: p.Icon})
		}
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

func init() {
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
		b, err := fs.ReadFile(staticFS(), name)
		if err != nil {
			next.ServeHTTP(rw, r)
			return
		}
		if r.URL.Path == "/" {
			b = versionedPage(b)
		}
		sum := sha256.Sum256(b)
		rw.Header().Set("ETag", `"`+hex.EncodeToString(sum[:12])+`"`)
		rw.Header().Set("Cache-Control", "no-cache")
		if r.URL.Path == "/" {
			rw.Header().Set("Content-Type", "text/html; charset=utf-8")
			http.ServeContent(rw, r, "index.html", time.Time{}, bytes.NewReader(b))
			return
		}
		next.ServeHTTP(rw, r)
	})
}

// pageFile is a script or stylesheet of the page's own, named in it.
var pageFile = regexp.MustCompile(`(src|href)="([A-Za-z0-9_.-]+\.(?:js|css))"`)

// versionedPage is the page with each of its own scripts and stylesheets
// named with its content's hash; one not among the page's files (boot.js,
// which the API writes) keeps its name.
func versionedPage(page []byte) []byte {
	return pageFile.ReplaceAllFunc(page, func(m []byte) []byte {
		g := pageFile.FindSubmatch(m)
		b, err := fs.ReadFile(staticFS(), string(g[2]))
		if err != nil {
			return m
		}
		sum := sha256.Sum256(b)
		return []byte(fmt.Sprintf(`%s="%s?v=%s"`, g[1], g[2], hex.EncodeToString(sum[:6])))
	})
}

// Handler serves the embedded UI and the JSON API.
// gw is the gateway this process serves, or nil when another magpie has it
// (for now: see startBackend).
func Handler(w Windows, gw *gateway.Server) http.Handler {
	if gw != nil {
		served.Store(gw)
	}
	if isWeb(w) {
		webPage.Store(true)
	}
	mux := http.NewServeMux()
	mux.Handle("/", devPage(revalidated(http.FileServer(http.FS(staticFS())))))
	devRoutes(mux)
	// boot.js hands the page the saved language and theme before it paints:
	// they came only with the settings, so the tabs showed English first
	mux.HandleFunc("GET /boot.js", func(rw http.ResponseWriter, r *http.Request) {
		s := settings.Load()
		// and the text size, which the Mac's header measures against the
		// traffic lights
		boot := map[string]any{"lang": s.Lang, "theme": s.Theme, "textSize": s.TextSize, "web": isWeb(w)}
		if !isWeb(w) {
			boot["uiFont"], boot["codeFont"] = s.UIFont, s.CodeFont
		}
		// and whether it is a gateway's page alone (gatewaymode.go), so the
		// pages left out never show
		if on, _ := gatewayMode(isWeb(w)); on {
			boot["gateway"] = true
		}
		// on Omarchy the page takes its theme's look before it paints
		if th, ok := omarchyTheme(); ok {
			boot["omarchy"] = th
		}
		b, _ := json.Marshal(boot)
		rw.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		rw.Header().Set("Cache-Control", "no-store")
		rw.Write(append(append([]byte("window.bootPrefs = "), b...), ";\n"...))
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
	fontRoutes(mux, isWeb(w) || !fonts.Available, fonts.List)
	mux.HandleFunc("GET /api/state", func(rw http.ResponseWriter, r *http.Request) {
		// the panel and its model picker load from here: an account still
		// without its vendor's list (one whose try at start-up failed) is
		// asked again in the background, not only from the Providers page
		provider.FetchNewSoon(8 * time.Second)
		writeJSON(rw, state())
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
		if err := a.Pick(f.Key, strings.TrimSpace(in.Value)); err != nil {
			fail(rw, err)
			return
		}
		s := state()
		if a.Notice != nil {
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
		for _, a := range agent.Detected() {
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
		var in struct{ Name string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		in.Name = strings.TrimSpace(in.Name)
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
		for _, p := range provider.All() {
			if p.Ready() {
				c, cancel := context.WithTimeout(ctx, 8*time.Second)
				p.Fetch(c)
				cancel()
			}
		}
		writeJSON(rw, state())
	})
	providerRoutes(mux, w)
	importRoutes(mux)
	usageRoutes(mux, w)
	callerKeyRoutes(mux)
	sessionRoutes(mux, w)
	sessionManageRoutes(mux, w)
	backupRoutes(mux, w)
	archiveRoutes(mux)
	libraryRoutes(mux, w)
	updateRoutes(mux, w)
	whatsNewRoutes(mux)
	cliBehindRoutes(mux)
	gatewayFixRoutes(mux)
	gatewayModeRoutes(mux)
	mux.HandleFunc("GET /api/settings", func(rw http.ResponseWriter, r *http.Request) {
		access.MigrateLegacyLANKeyBestEffort()
		writeJSON(rw, settingsState())
	})
	mux.HandleFunc("POST /api/settings", func(rw http.ResponseWriter, r *http.Request) {
		var body json.RawMessage
		var in settings.Settings
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			fail(rw, err)
			return
		}
		if err := json.Unmarshal(body, &in); err != nil {
			fail(rw, err)
			return
		}
		// the Settings page sends its own choices; how the agents are
		// arranged is the Agents page's, and the window's size its own; both stay as they are
		cur := settings.Load()
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(body, &fields)
		// Older pages do not send fonts. An explicit null restores the
		// default, while an omitted field (or a browser save) preserves it.
		if _, sent := fields["uiFont"]; !sent || isWeb(w) {
			in.UIFont = cur.UIFont
		}
		if _, sent := fields["codeFont"]; !sent || isWeb(w) {
			in.CodeFont = cur.CodeFont
		}
		in.AgentOrder, in.AgentsHidden, in.AgentsShown = cur.AgentOrder, cur.AgentsHidden, cur.AgentsShown
		in.Window, in.WindowMaximised = cur.Window, cur.WindowMaximised // the window's own, as it was last resized
		// and what other pages keep here: which models an agent is shown, and
		// everything the user said of a model anywhere else in the app, set on
		// its own. The per-model maps are carried whole rather than named one
		// by one, so a map added later is not silently dropped here.
		//
		// HiddenModels, PickedModels and OrderedModels are the other way
		// round — keyed by agent, not by "<provider>/<model>" — so they are
		// not among them, and belong to the Agents page.
		in.Visible, in.HiddenModels, in.OrderedModels = cur.Visible, cur.HiddenModels, cur.OrderedModels
		in.PickedModels = cur.PickedModels     // "only models I pick" (#1337)
		in.FastPicks = cur.FastPicks           // switched in the agents' pickers (#954)
		in.AgentEfforts = cur.AgentEfforts     // picked in an agent's row (#1003)
		in.PluginCheckins = cur.PluginCheckins // set on its own (plugin-checkin below)
		settings.CarryPerModel(&in, &cur)
		in.LAN, in.LANKey = cur.LAN, cur.LANKey
		in.LANKeyID = cur.LANKeyID
		in.Port = cur.Port                               // set on its own (port below), which moves the gateway
		in.CORSOrigins = cur.CORSOrigins                 // set on its own (cors below)
		in.GitHubToken = cur.GitHubToken                 // set on its own (github-token below), never sent to the page
		in.RequestArchive = cur.RequestArchive           // the Gateway page's, set on its own
		in.RequestArchiveMaxMB = cur.RequestArchiveMaxMB // in settings.json only
		in.RedactRules = cur.RedactRules                 // the masking rules, set on their own
		// used or left is the Usage page's toggle as much as Settings', set on its own
		in.QuotaLeft = cur.QuotaLeft
		in.UsageOrder = cur.UsageOrder // the Usage page's, dragged there
		// and what the tray panel's Allowances tab leaves out, set there
		in.PanelUsageHidden = cur.PanelUsageHidden
		// how agents' lists name models, set on its own for the agents to be told
		in.PlainNames, in.PlainOwnNames = cur.PlainNames, cur.PlainOwnNames
		in.CodexAgentsV1 = cur.CodexAgentsV1
		in.FullContext = cur.FullContext // set on its own (full-context below)
		in.CompactAt = cur.CompactAt     // and so is the threshold
		// Claude Desktop's list, set in its row on the Agents page
		in.DesktopLongest = cur.DesktopLongest

		in.CodexTitles = cur.CodexTitles // set on its own (codex-titles below)
		// and so is the model Codex's auto-review runs on (codex-auto-review)
		in.CodexAutoReview = cur.CodexAutoReview
		in.ChinaMirror = cur.ChinaMirror // the Plugins page's, set on its own
		// which Codex accounts spend a reset by themselves, set on the Usage card
		in.CodexAutoReset = cur.CodexAutoReset
		// and which of them spend their credits, set there too
		in.CodexNoCredits = cur.CodexNoCredits
		// and each Codex account's own daily warm-up (codex-warm-at below)
		in.CodexWarmAtOf = cur.CodexWarmAtOf
		// and the text size, which the keyboard changes too (text-size below)
		in.TextSize = cur.TextSize
		// the version the Update pill was hidden for, set from the pill
		in.UpdateSkip = cur.UpdateSkip
		// the GitHub mirror updates come through, set by magpie update mirror
		in.UpdateMirror = cur.UpdateMirror
		// gateway mode, set on its own (gateway-mode)
		in.GatewayMode = cur.GatewayMode
		if v := strings.TrimSpace(in.Vision); v != "" && v != "off" && v != cur.Vision {
			if _, _, ok := provider.Resolve(v); !ok {
				fail(rw, fmt.Errorf("no model %s to describe images", v))
				return
			}
		}
		if v := strings.TrimSpace(in.ImageGen); v != "" && v != "off" && v != cur.ImageGen {
			if _, _, ok := provider.Resolve(v); !ok {
				fail(rw, fmt.Errorf("no model %s to generate images", v))
				return
			}
		}
		if v := strings.TrimSpace(in.Searcher); v != "" && v != cur.Searcher {
			id, _, _ := strings.Cut(v, "/")
			if !slices.ContainsFunc(gateway.Searchers(), func(c gateway.SearcherChoice) bool { return c.Provider.ID == id }) {
				fail(rw, fmt.Errorf("%s can't search the web for other models", id))
				return
			}
		}
		if err := settings.Save(in); err != nil {
			fail(rw, err)
			return
		}
		if onFonts != nil && (!sameFont(in.UIFont, cur.UIFont) || !sameFont(in.CodeFont, cur.CodeFont)) {
			onFonts()
		}
		// whether agents are told every model takes images follows Vision
		// (provider.Described)
		if strings.TrimSpace(in.Vision) != strings.TrimSpace(cur.Vision) {
			catalog.Touched()
		}
		if (in.Dock != cur.Dock || in.DockWindow != cur.DockWindow) && onDock != nil {
			onDock(in)
		}
		// the cards the menu bar shows, any of them (TrayUsage is only the first),
		// how often, with their logos or not, and beside the bird or not
		if (!slices.Equal(settings.Load().TrayUsages, cur.TrayUsages) || in.TrayUsageEvery != cur.TrayUsageEvery ||
			in.TrayNoLogos != cur.TrayNoLogos || in.TrayNoBird != cur.TrayNoBird) && onTrayUsage != nil {
			onTrayUsage()
		}
		// an alert turned on or moved is looked at now, the Mac asked for its
		// leave to notify as it is turned on (#368)
		if (in.UsageAlert != cur.UsageAlert || in.BalanceAlert != cur.BalanceAlert || in.ResetReminder != cur.ResetReminder) &&
			(in.UsageAlert > 0 || in.BalanceAlert > 0 || in.ResetReminder > 0) && onAlerts != nil {
			onAlerts()
		}
		// the tray menu follows the page's language (#301)
		if in.Lang != cur.Lang && onLang != nil {
			onLang()
		}
		// an update check that failed, without the proxy set just now, is
		// tried again through it, not in six hours (#294)
		if strings.TrimSpace(in.Proxy) != strings.TrimSpace(cur.Proxy) && updates.json().State == "error" {
			go updates.check()
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
	// whether Claude Desktop lists a model of 1M or more once, by its 1M id
	// (settings.DesktopLongest, #1272): it reads the list as it starts
	mux.HandleFunc("POST /api/settings/desktop-longest", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ On bool }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		s := settings.Load()
		s.DesktopLongest = in.On
		if err := settings.Save(s); err != nil {
			fail(rw, err)
			return
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
	// where Codex's requests for a thread's title go (#705): "" as Codex
	// sends them, "off", or a model's id
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
	// the model Codex's auto-review runs on (#938): "" as Codex picks it,
	// or a model's id, named in every entry of Codex's list
	mux.HandleFunc("POST /api/settings/codex-auto-review", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Model string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		v := strings.TrimSpace(in.Model)
		if v != "" {
			// Resolve intentionally accepts arbitrary names under a known
			// provider. An approval reviewer must be a model or group magpie
			// actually lists, including providers kept unlisted for routing.
			if !slices.ContainsFunc(provider.Served(), func(e provider.Entry) bool { return e.ID == v }) {
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
	mux.HandleFunc("POST /api/settings/codex-auto-reset", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			User string
			On   bool
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if strings.TrimSpace(in.User) == "" {
			fail(rw, fmt.Errorf("which Codex account?"))
			return
		}
		if err := provider.SetCodexAutoReset(in.User, in.On); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
	// whether a Codex account spends its credits once its allowance is
	// used up, the Usage card's toggle, set on its own
	mux.HandleFunc("POST /api/settings/codex-credits", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			User string
			On   bool
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if strings.TrimSpace(in.User) == "" {
			fail(rw, fmt.Errorf("which Codex account?"))
			return
		}
		if err := provider.SetCodexCredits(in.User, in.On); err != nil {
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
	// the Usage page's order of its cards, which the tray panel's Allowances
	// tab follows, and what that tab leaves out, in magpie's settings; one
	// not sent stays as it is
	mux.HandleFunc("POST /api/usage/arrange", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Order       *[]string
			PanelHidden *[]string
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		s := settings.Load()
		if in.Order != nil {
			s.UsageOrder = *in.Order
		}
		if in.PanelHidden != nil {
			s.PanelUsageHidden = *in.PanelHidden
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
		writeJSON(rw, settingsState())
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
	// a web search API added, given a new key or address, or taken away
	mux.HandleFunc("POST /api/settings/search-api", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Vendor, Key, URL string
			Remove           bool
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		var err error
		if in.Remove {
			err = provider.RemoveSearchAPI(in.Vendor)
		} else {
			err = provider.SetSearchAPI(provider.SearchAPI{Vendor: in.Vendor, Key: in.Key, URL: in.URL})
		}
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
	// a search API's saved key, for its row's Show button (OnurBen on
	// Discord); like a provider's, it never leaves this machine
	mux.HandleFunc("POST /api/settings/search-key", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Vendor string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
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
	// the config folder only: the page names no path, so it can't open others
	mux.HandleFunc("POST /api/settings/reveal", func(rw http.ResponseWriter, r *http.Request) {
		if err := w.OpenFolder(settings.Dir()); err != nil {
			fail(rw, err)
			return
		}
		rw.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /api/window/{action}", func(rw http.ResponseWriter, r *http.Request) {
		switch r.PathValue("action") {
		case "hide":
			w.HidePanel()
		case "main":
			w.ShowMain(mainView(r.URL.Query()))
		case "quit":
			w.Quit()
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
	devListen(mux)
	return held(mux)
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
		if !(api && r.Method == http.MethodGet) {
			// anything that may have written drops what was held, once it
			// has answered with it
			defer provider.Changed()
		}
		h.ServeHTTP(rw, r)
	})
}

func state() stateJSON {
	s := stateJSON{Agents: []agentJSON{}, Profiles: []profileJSON{}, Catalog: catalog.Source(), Settings: settings.Load()}
	// state() is asked for after nearly every click, so the rate — a
	// network fetch once every TTL — is only worth its rare latency when
	// cny is actually chosen; usd never looks at it
	if s.Settings.Currency == "cny" {
		s.FX = currentFX()
	}
	s.Unlisted = unlistedModels()
	s.CLIBehind = cliBehind()
	for _, a := range agent.Clients() {
		s.Clients = append(s.Clients, clientJSON{ID: a.ID, Name: a.Name, Icon: a.Icon})
	}
	for _, a := range agent.Detected() {
		if a.Follow != nil {
			_ = a.Follow()
		}
		vals := a.Values()
		aj := agentJSON{ID: a.ID, Name: a.Name, Icon: a.Icon, Path: tilde(a.Path), Fields: agentFields(a, vals)}
		aj.Models = agentModelCount(a, aj.Fields)
		aj.Drift = a.Drift()
		aj.Wired = a.Wired()
		if aj.Wired {
			aj.StaleCopies = a.StaleCopies()
			aj.Stale = len(aj.StaleCopies)
			aj.Joined = a.Joined != nil && a.Joined()
		} else {
			aj.Source = a.Source()
			aj.Failover = a.FailingOver != nil && a.FailingOver()
		}
		if a.Import != nil {
			aj.Import, aj.Added = a.Import(), a.Added != nil && a.Added()
		}
		if a.Launch != nil {
			aj.Launch = a.Launch()
		}
		aj.CLIMissing = a.CLIMissing()
		if a.Native != nil {
			native := a.Native.Read()
			aj.Native = &native
		}
		s.Agents = append(s.Agents, aj)
	}
	if ps, err := profile.Load(); err == nil {
		for _, n := range profile.Names(ps) {
			pj := profileJSON{Name: n, Summary: profile.Summary(ps[n]), Agents: profile.Details(ps[n])}
			if l := ps[n].Library; l != nil {
				servers, skills := l.On()
				pj.Library = &profileLibraryJSON{Servers: servers, Skills: skills, Instructions: l.GivesInstructions()}
			}
			s.Profiles = append(s.Profiles, pj)
		}
	}
	return s
}

func writeJSON(rw http.ResponseWriter, v any) {
	rw.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(rw).Encode(v)
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
	var noModels *agent.NoModelsError
	if errors.As(err, &noModels) {
		out["code"] = "no_models"
		out["agent"] = noModels.Agent
	}
	var signedIn *provider.SignedInError
	if errors.As(err, &signedIn) {
		out["code"], out["agent"], out["user"] = "signed_in", signedIn.Agent, signedIn.User
	}
	_ = json.NewEncoder(rw).Encode(out)
}

func tilde(p string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, home) {
		return "~" + p[len(home):]
	}
	return p
}

// codexUsers is the ChatGPT accounts signed in, by name, each once.
func codexUsers() []string {
	var out []string
	for _, l := range provider.Logins("codex") {
		if l.User != "" && !slices.ContainsFunc(out, func(u string) bool { return strings.EqualFold(u, l.User) }) {
			out = append(out, l.User)
		}
	}
	return out
}
