// Package gui hosts the desktop app: a tray panel and a regular window that
// share one small web UI. The UI talks to Go over a tiny JSON API served by
// the same handler that serves the static assets, so no binding generator or
// bundler is involved.
package gui

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/autostart"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/fx"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/library"
	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/profile"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/redact"
	"github.com/yetone/magpie/internal/settings"
)

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
}

type profileLibraryJSON struct {
	Servers      int  `json:"servers"`      // given to at least one agent
	Skills       int  `json:"skills"`       // given to at least one agent
	Instructions bool `json:"instructions"` // some agent gets them
}

type stateJSON struct {
	Agents   []agentJSON       `json:"agents"`
	Clients  []clientJSON      `json:"clients"` // who a request may come from, by id
	Profiles []profileJSON     `json:"profiles"`
	Catalog  string            `json:"catalog"`
	Notice   string            `json:"notice,omitempty"` // advice after a change, e.g. "restart Codex"
	Settings settings.Settings `json:"settings"`
	// FX is the dollar-to-yuan rate the cny currency choice shows costs at,
	// here too (not only in settingsJSON) so a cost drawn before the reader
	// ever opens Settings already converts, if cny was chosen last time.
	FX fxJSON `json:"fx"`
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

// currentFX asks internal/fx for the rate, bounded so a slow or absent
// network never holds up a page's worth of state; its own cache makes this
// return at once except right after each TTL (see internal/fx).
func currentFX() fxJSON {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	r := fx.Get(ctx)
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
	// Mac apps that explicitly handle .command files, for resumed sessions.
	TerminalApps    []terminalChoice `json:"terminalApps,omitempty"`
	TerminalDefault string           `json:"terminalDefault,omitempty"`
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
	// the model magpie's generate_image tool draws with when ImageGen
	// names none, and those that can be named
	ImageGenAuto   string     `json:"imageGenAuto,omitempty"`
	ImageGenModels []modelRef `json:"imageGenModels"`
	// where other machines reach the gateway while it is shared
	LANURLs []string `json:"lanURLs,omitempty"`
	// LANURLs are a container's own addresses, not the host's: the page
	// offers the one it was opened at instead, or says how to set it
	LANContainer bool `json:"lanContainer,omitempty"`
	// when the Codex warm-up last started an account's window
	CodexWarmed *time.Time `json:"codexWarmed,omitempty"`
	// and the Claude warm-up
	ClaudeWarmed *time.Time `json:"claudeWarmed,omitempty"`
	// whether a WorkBuddy (China) account is signed in, and each one's
	// last daily check-in
	WorkBuddy         bool                        `json:"workbuddy"`
	WorkBuddyCheckins []provider.WorkBuddyCheckin `json:"workbuddyCheckins,omitempty"`
	// FX is the dollar-to-yuan rate the cny currency choice shows costs at
	FX fxJSON `json:"fx"`
	// NotifyProblem is why a usage alert set wouldn't be seen: "denied"
	// (notifications turned off for magpie) or "unavailable"
	NotifyProblem string `json:"notifyProblem,omitempty"`
}

func settingsState() settingsJSON {
	s := settingsJSON{Settings: settings.Load(), Version: Version, Dir: tilde(settings.Dir()), Gateway: gateway.URL()}
	if found, err := discoverTerminals(); err == nil {
		for _, app := range found.Apps {
			s.TerminalApps = append(s.TerminalApps, terminalChoice{ID: app.ID, Name: app.Name})
		}
		s.TerminalDefault = found.Default
	}
	s.FX = currentFX()
	if (s.UsageAlert > 0 || s.BalanceAlert > 0) && notifyProblem != nil {
		s.NotifyProblem = notifyProblem()
	}
	s.ProxyNow, s.ProxySource = netproxy.Describe()
	s.Login = autostart.Enabled()
	if s.LAN {
		s.LANURLs, s.LANContainer = gateway.LANURLs(), gateway.ContainerAddrs()
	}
	s.CodexWarmed, s.ClaudeWarmed = latest(provider.CodexWarmed()), latest(provider.ClaudeWarmed())
	s.WorkBuddy, s.WorkBuddyCheckins = provider.HasWorkBuddy(), provider.WorkBuddyCheckins()
	s.VisionAuto, s.VisionModels = gateway.AutoVision(), []modelRef{}
	for _, e := range provider.Served() {
		if e.Images && (e.ImageInput == nil || *e.ImageInput) && (e.Group != "" || e.Provider.Ready()) {
			m := modelRef{ID: e.ID, Name: e.Name, Provider: e.Provider.ID, PName: e.Provider.Name, Icon: e.Provider.Icon}
			if e.Group != "" {
				m.Provider, m.PName = "", e.Group
			}
			s.VisionModels = append(s.VisionModels, m)
		}
	}
	s.ImageGenAuto, s.ImageGenModels = gateway.AutoDrawer(), []modelRef{}
	for _, p := range provider.All() {
		if !p.On() || p.Decides() {
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

// Handler serves the embedded UI and the JSON API.
// gw is the gateway this process serves, or nil when another magpie has it
// (for now: see startBackend).
func Handler(w Windows, gw *gateway.Server) http.Handler {
	if gw != nil {
		served.Store(gw)
	}
	mux := http.NewServeMux()
	mux.Handle("/", devPage(http.FileServer(http.FS(staticFS()))))
	devRoutes(mux)
	// boot.js hands the page the saved language and theme before it paints:
	// they came only with the settings, so the tabs showed English first
	mux.HandleFunc("GET /boot.js", func(rw http.ResponseWriter, r *http.Request) {
		s := settings.Load()
		// and the text size, which the Mac's header measures against the
		// traffic lights
		boot := map[string]any{"lang": s.Lang, "theme": s.Theme, "textSize": s.TextSize, "web": isWeb(w)}
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
	mux.HandleFunc("GET /api/state", func(rw http.ResponseWriter, r *http.Request) {
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
		if err := a.Apply(f.Key, strings.TrimSpace(in.Value)); err != nil {
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
	mux.HandleFunc("GET /api/agents/cli", func(rw http.ResponseWriter, r *http.Request) {
		clis, pending := agent.CLIs(3 * time.Second)
		writeJSON(rw, map[string]any{"agents": clis, "pending": pending})
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
	mux.HandleFunc("POST /api/agents/{action}/{id}", func(rw http.ResponseWriter, r *http.Request) {
		a, err := agent.Find(r.PathValue("id"))
		if err != nil {
			fail(rw, err)
			return
		}
		switch r.PathValue("action") {
		case "reapply":
			err = a.Reapply()
		case "keep":
			a.Keep()
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
	sessionRoutes(mux, w)
	backupRoutes(mux, w)
	libraryRoutes(mux, w)
	updateRoutes(mux, w)
	mux.HandleFunc("GET /api/settings", func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, settingsState())
	})
	mux.HandleFunc("POST /api/settings", func(rw http.ResponseWriter, r *http.Request) {
		var in settings.Settings
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		// the Settings page sends its own choices; how the agents are
		// arranged is the Agents page's, and the window's size its own; both stay as they are
		cur := settings.Load()
		in.AgentOrder, in.AgentsHidden, in.AgentsShown = cur.AgentOrder, cur.AgentsHidden, cur.AgentsShown
		in.Window = cur.Window // the window's own, as it was last resized
		// and what other pages keep here: which models an agent is shown, and
		// everything the user said of a model anywhere else in the app, set on
		// its own. The per-model maps are carried whole rather than named one
		// by one, so a map added later is not silently dropped here.
		//
		// HiddenModels is the other way round — keyed by agent, not by
		// "<provider>/<model>" — so it is not one of them, and belongs to the
		// Agents page.
		in.Visible, in.HiddenModels = cur.Visible, cur.HiddenModels
		settings.CarryPerModel(&in, &cur)
		in.LAN, in.LANKey = cur.LAN, cur.LANKey
		in.RedactRules = cur.RedactRules // the masking rules, set on their own
		// used or left is the Usage page's toggle as much as Settings', set on its own
		in.QuotaLeft = cur.QuotaLeft
		// how agents' lists name models, set on its own for the agents to be told
		in.PlainNames = cur.PlainNames
		// which Codex accounts spend a reset by themselves, set on the Usage card
		in.CodexAutoReset = cur.CodexAutoReset
		// and the text size, which the keyboard changes too (text-size below)
		in.TextSize = cur.TextSize
		// the version the Update pill was hidden for, set from the pill
		in.UpdateSkip = cur.UpdateSkip
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
		if err := settings.Save(in); err != nil {
			fail(rw, err)
			return
		}
		if (in.Dock != cur.Dock || in.DockWindow != cur.DockWindow) && onDock != nil {
			onDock(in)
		}
		// the cards the menu bar shows, any of them (TrayUsage is only the first),
		// how often, and with their logos or not
		if (!slices.Equal(settings.Load().TrayUsages, cur.TrayUsages) || in.TrayUsageEvery != cur.TrayUsageEvery ||
			in.TrayNoLogos != cur.TrayNoLogos) && onTrayUsage != nil {
			onTrayUsage()
		}
		// an alert turned on or moved is looked at now, the Mac asked for its
		// leave to notify as it is turned on (#368)
		if (in.UsageAlert != cur.UsageAlert || in.BalanceAlert != cur.BalanceAlert) &&
			(in.UsageAlert > 0 || in.BalanceAlert > 0) && onAlerts != nil {
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
		var in struct{ On bool }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if err := provider.SetPlainNames(in.On); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
	// whether a Codex account spends one of its resets by itself once its
	// week is used up, the Usage card's toggle, set on its own
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
	// sharing the gateway on the local network: on makes its key the first
	// time, new asks for another (the old one stops working)
	mux.HandleFunc("POST /api/settings/lan", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ On, NewKey bool }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		s := settings.Load()
		s.LAN = in.On
		if s.LANKey == "" || in.NewKey {
			s.LANKey = gateway.NewLANKey()
		}
		if err := settings.Save(s); err != nil {
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
	return mux
}

func state() stateJSON {
	s := stateJSON{Agents: []agentJSON{}, Profiles: []profileJSON{}, Catalog: catalog.Source(), Settings: settings.Load()}
	// state() is asked for after nearly every click, so the rate — a
	// network fetch once every TTL — is only worth its rare latency when
	// cny is actually chosen; usd never looks at it
	if s.Settings.Currency == "cny" {
		s.FX = currentFX()
	}
	for _, a := range agent.Clients() {
		s.Clients = append(s.Clients, clientJSON{ID: a.ID, Name: a.Name, Icon: a.Icon})
	}
	for _, a := range agent.Detected() {
		vals := a.Values()
		aj := agentJSON{ID: a.ID, Name: a.Name, Icon: a.Icon, Path: tilde(a.Path), Fields: agentFields(a, vals)}
		aj.Models = agentModelCount(a.ID, aj.Fields)
		aj.Drift = a.Drift()
		if a.Import != nil {
			aj.Import, aj.Added = a.Import(), a.Added != nil && a.Added()
		}
		if a.Launch != nil {
			aj.Launch = a.Launch()
		}
		s.Agents = append(s.Agents, aj)
	}
	if ps, err := profile.Load(); err == nil {
		for _, n := range profile.Names(ps) {
			pj := profileJSON{Name: n, Summary: profile.Summary(ps[n])}
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
	_ = json.NewEncoder(rw).Encode(map[string]string{"error": err.Error()})
}

func tilde(p string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, home) {
		return "~" + p[len(home):]
	}
	return p
}
