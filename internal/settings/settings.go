// Package settings keeps the few preferences the desktop app has: which
// palette to paint with, which language to speak, how the agents are
// arranged. Everything else magpie knows is derived from the agents' own
// files.
//
// The file is ~/.config/magpie/settings.json; a missing file means "follow
// the system" for both.
package settings

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/redact"
)

// Settings is what the user chose. "" and "system" both mean "follow the OS".
type Settings struct {
	Theme string `json:"theme,omitempty"` // system | light | dark
	Lang  string `json:"lang,omitempty"`  // system | en | zh
	Tray  string `json:"tray,omitempty"`  // what clicking the tray icon opens: panel | window
	// Dock keeps magpie in the Mac's Dock as well as the menu bar, for a
	// menu bar too full to show its icon.
	Dock bool `json:"dock,omitempty"`
	// DockWindow shows it in the Dock only while its window is open, so
	// Cmd-Tab reaches the window without an icon kept there the rest of
	// the time. Dock wins over it.
	DockWindow bool `json:"dockWindow,omitempty"`
	// Proxy for magpie's own requests to vendors: "" follows the
	// environment and then the system, "direct" uses none, anything else
	// is the proxy (http://, https:// or socks5://; host:port means http).
	Proxy string `json:"proxy,omitempty"`
	// Redact keeps secrets in what agents send (API keys, private keys,
	// tokens, passwords) from the vendors behind magpie: they go as
	// placeholders, and come back as they were. RedactPersonal does the same
	// for emails, phone numbers and ID and bank card numbers, and
	// RedactWords for the user's own words. RedactRules are the user's own
	// rules for secrets magpie's don't know (a gateway's oc_sk_… key), a
	// prefix or a pattern each, masked with the secrets while Redact is on.
	Redact         bool          `json:"redact,omitempty"`
	RedactPersonal bool          `json:"redactPersonal,omitempty"`
	RedactWords    []string      `json:"redactWords,omitempty"`
	RedactRules    []redact.Rule `json:"redactRules,omitempty"`
	// LAN shares the gateway on the local network, for agents on other
	// machines; a request from one must carry LANKey as its API key, a
	// key magpie makes when LAN is first turned on.
	LAN    bool   `json:"lan,omitempty"`
	LANKey string `json:"lanKey,omitempty"`
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
	CodexWarmAt  string `json:"codexWarmAt,omitempty"`
	ClaudeWarmAt string `json:"claudeWarmAt,omitempty"`
	// WorkBuddyCheckin presses WorkBuddy's daily check-in (签到) for each
	// signed-in WorkBuddy (China) account once a Beijing day, claiming the
	// credits it gives while its event runs.
	WorkBuddyCheckin bool `json:"workbuddyCheckin,omitempty"`
	// NoStats stops the one event a day that counts magpie's users (see
	// internal/stats).
	NoStats bool `json:"noStats,omitempty"`
	// Vision is the model that describes an image to a model that can't see
	// it: a model's id (provider/model, group/<id>), "off" to turn such an
	// image away, or empty for one magpie picks (see gateway.seer).
	Vision string `json:"vision,omitempty"`
	// ImageGen is the model magpie's generate_image tool draws with (the
	// gateway's /v1/images/generations when a request names no model): a
	// model's id, "off", or empty for one magpie picks (gateway.drawer).
	ImageGen string `json:"imageGen,omitempty"`
	// TrayUsage is the subscription or plan whose windows are shown beside
	// the tray icon, by its provider and account ("claude|a@b.c"); "" none.
	TrayUsage string `json:"trayUsage,omitempty"`
	// TrayUsageEvery is how often, in minutes, that text is brought up to
	// date; 0 is every 3 (one of TrayEvery).
	TrayUsageEvery int `json:"trayUsageEvery,omitempty"`
	// QuotaLeft shows a subscription's windows by how much of each is left,
	// not used: the Usage page, the tray panel and the menu bar alike.
	QuotaLeft bool `json:"quotaLeft,omitempty"`
	// How the agents are listed, by agent id. AgentOrder comes first, as
	// ordered; an agent it doesn't name (one installed since) follows in
	// magpie's own order. A hidden agent is folded away at the bottom of the
	// list; a shown one stays in view even while nothing is set on it, which
	// otherwise folds it away too. The agents' own files never hear of it.
	AgentOrder   []string `json:"agentOrder,omitempty"`
	AgentsHidden []string `json:"agentsHidden,omitempty"`
	AgentsShown  []string `json:"agentsShown,omitempty"`
	// Visible narrows the models an agent is shown, by agent id: the
	// families (the tag a provider or group is given), provider ids and
	// group ids its lists hold. An agent it doesn't name is shown them all.
	Visible map[string][]string `json:"visible,omitempty"`
	// ModelNames are the names the user gave models, by "<provider
	// id>/<model id>": agents, the gateway's model list and magpie itself
	// show them for the vendor's (see provider.SetModelName).
	ModelNames map[string]string `json:"modelNames,omitempty"`
	// ModelEfforts are the reasoning levels the user keeps of a model's,
	// by "<provider id>/<model id>": the lists magpie hands out offer only
	// those (see provider.SetModelEfforts).
	ModelEfforts map[string][]string `json:"modelEfforts,omitempty"`
	// The main window's size when it was last resized, width and height,
	// so it opens at it again after a restart.
	Window []int `json:"window,omitempty"`
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

// Themes and Langs are the accepted values, in the order the UI offers them.
var (
	Themes = []string{"system", "light", "dark"}
	Langs  = []string{"system", "en", "zh"}
	Trays  = []string{"panel", "window"}
	// Warmups are CodexWarmup's and ClaudeWarmup's values, off as "".
	Warmups = []string{"", "week", "all"}
	// TrayEvery are TrayUsageEvery's values, in minutes.
	TrayEvery = []int{1, 3, 5, 10, 30}
)

// Path is the settings file.
func Path() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "magpie", "settings.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "magpie", "settings.json")
}

// Dir is the folder every magpie file lives in.
func Dir() string { return filepath.Dir(Path()) }

// Load reads the settings; anything missing or unreadable is the default.
func Load() Settings {
	var s Settings
	if b, err := os.ReadFile(Path()); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s.normal()
}

// Save validates and writes the settings.
func Save(s Settings) error {
	s = s.normal()
	if !slices.Contains(Themes, s.Theme) {
		return fmt.Errorf("theme must be one of %v, not %q", Themes, s.Theme)
	}
	if !slices.Contains(Langs, s.Lang) {
		return fmt.Errorf("language must be one of %v, not %q", Langs, s.Lang)
	}
	if !slices.Contains(Trays, s.Tray) {
		return fmt.Errorf("tray must be one of %v, not %q", Trays, s.Tray)
	}
	if !slices.Contains(Warmups, s.CodexWarmup) {
		return fmt.Errorf("codex warm-up must be off, week or all, not %q", s.CodexWarmup)
	}
	if !slices.Contains(Warmups, s.ClaudeWarmup) {
		return fmt.Errorf("claude warm-up must be off, week or all, not %q", s.ClaudeWarmup)
	}
	for _, at := range []string{s.CodexWarmAt, s.ClaudeWarmAt} {
		if _, _, ok := Clock(at); at != "" && !ok {
			return fmt.Errorf("a warm-up's time of day must look like 06:00, not %q", at)
		}
	}
	if !slices.Contains(TrayEvery, s.TrayUsageEvery) {
		return fmt.Errorf("the menu bar's usage is refreshed every %v minutes, not %d", TrayEvery, s.TrayUsageEvery)
	}
	s.Proxy = strings.TrimSpace(s.Proxy)
	if s.Proxy != "" && s.Proxy != "direct" {
		raw := s.Proxy
		if !strings.Contains(raw, "://") {
			raw = "http://" + raw
		}
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || !slices.Contains([]string{"http", "https", "socks5", "socks5h"}, u.Scheme) {
			return fmt.Errorf("proxy must look like http://127.0.0.1:7890 or socks5://127.0.0.1:1080, not %q", s.Proxy)
		}
	}
	s.Vision = strings.TrimSpace(s.Vision)
	if s.Vision != "" && s.Vision != "off" && !strings.Contains(s.Vision, "/") {
		return fmt.Errorf("the vision model must be a model's id such as openai/gpt-5-mini, or off, not %q", s.Vision)
	}
	s.ImageGen = strings.TrimSpace(s.ImageGen)
	if s.ImageGen != "" && s.ImageGen != "off" && !strings.Contains(s.ImageGen, "/") {
		return fmt.Errorf("the image generation model must be a model's id such as openai/gpt-image-1, or off, not %q", s.ImageGen)
	}
	rules, err := redact.CheckRules(s.RedactRules)
	if err != nil {
		return err
	}
	s.RedactRules = rules
	s.AgentOrder, s.AgentsHidden, s.AgentsShown = ids(s.AgentOrder), ids(s.AgentsHidden), ids(s.AgentsShown)
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(Path(), append(b, '\n'), 0o644)
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
	if s.CodexWarmup == "off" {
		s.CodexWarmup = ""
	}
	if s.ClaudeWarmup == "off" {
		s.ClaudeWarmup = ""
	}
	if s.TrayUsageEvery == 0 {
		s.TrayUsageEvery = 3
	}
	// a time of day as 06:00 whichever way it came (6:00, 06:00:00)
	for _, at := range []*string{&s.CodexWarmAt, &s.ClaudeWarmAt} {
		*at = strings.TrimSpace(*at)
		if h, m, ok := Clock(*at); ok {
			*at = fmt.Sprintf("%02d:%02d", h, m)
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
