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
	"math"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/redact"
)

// Settings is what the user chose. "" and "system" both mean "follow the OS".
type Settings struct {
	Theme string `json:"theme,omitempty"` // system | light | dark
	Lang  string `json:"lang,omitempty"`  // system | en | zh
	Tray  string `json:"tray,omitempty"`  // what clicking the tray icon opens: panel | window
	// SessionTerminal is the Mac app that opens a resumed session, by bundle
	// id. "" and "system" follow the .command file association.
	SessionTerminal string `json:"sessionTerminal,omitempty"`
	// Currency is what a cost — the Usage page's, the tray panel's, the
	// TUI's and the CLI's — is shown converted to: usd (its native
	// currency, list prices being in dollars) or cny, at a live exchange
	// rate (see internal/fx). A vendor's own balance, already in its own
	// currency (a Chinese relay's ¥), is never touched by this.
	Currency string `json:"currency,omitempty"`
	// WesternUnits shortens a large count in K, M and B even when magpie
	// speaks Chinese, which otherwise says it in 万 and 亿 (8000 万
	// rather than 80M). It means nothing in English.
	WesternUnits bool `json:"westernUnits,omitempty"`
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
	OTel  OTel   `json:"otel,omitempty"`
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
	// LAN shares the gateway on the local network; remote callers must use
	// named caller keys. LANKey is retained for older Magpie versions.
	LAN    bool   `json:"lan,omitempty"`
	LANKey string `json:"lanKey,omitempty"`
	// LANKeyID remembers the default named key created when sharing is enabled.
	LANKeyID string `json:"lanKeyId,omitempty"`
	// RequestArchive keeps each call the gateway serves — its headers and
	// bodies both ways, secrets taken out — in the S3 bucket sync keeps
	// its backup in (gateway/archive.go), for looking into a request later.
	RequestArchive bool `json:"requestArchive,omitempty"`
	// RequestArchiveMaxMB is how much of each body the archive keeps, in
	// MiB: 0 for 32, at most 1024 (#447)
	RequestArchiveMaxMB int `json:"requestArchiveMaxMB,omitempty"`
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
	// CodexAutoReset are the ChatGPT accounts (lower-case) that spend one
	// of their rate-limit resets by themselves once their weekly window is
	// used up and no other account can take the request: at most one a
	// week each (see provider.AutoUseCodexReset).
	CodexAutoReset []string `json:"codexAutoReset,omitempty"`
	// WorkBuddyCheckin presses WorkBuddy's daily check-in (签到) for each
	// signed-in WorkBuddy (China) account once a Beijing day, claiming the
	// credits it gives while its event runs.
	WorkBuddyCheckin bool `json:"workbuddyCheckin,omitempty"`
	// NoStats stops the one event a day that counts magpie's users (see
	// internal/stats).
	NoStats bool `json:"noStats,omitempty"`
	// NoUpdatePill keeps the header's Update pill away when a newer magpie
	// is out; UpdateSkip is the one version it was hidden for, and a newer
	// one brings it back. Either way magpie still downloads the version and
	// puts it in as it quits, and Settings' version row still offers it.
	NoUpdatePill bool   `json:"noUpdatePill,omitempty"`
	UpdateSkip   string `json:"updateSkip,omitempty"`
	// Vision is the model that describes an image to a model that can't see
	// it: a model's id (provider/model, group/<id>), "off" to turn such an
	// image away, or empty for one magpie picks (see gateway.seer).
	Vision string `json:"vision,omitempty"`
	// ImageGen is the model magpie's generate_image tool draws with (the
	// gateway's /v1/images/generations when a request names no model): a
	// model's id, "off", or empty for one magpie picks (gateway.drawer).
	// "off" turns the video a request that names no model gets off too
	// (gateway.videomaker).
	ImageGen string `json:"imageGen,omitempty"`
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
	// QuotaLeft shows a subscription's windows by how much of each is left,
	// not used: the Usage page, the tray panel and the menu bar alike.
	QuotaLeft bool `json:"quotaLeft,omitempty"`
	// UsageAlert is how much of a subscription's or plan's window, in
	// percent, is used when magpie says so with a notification (#368):
	// once for each time the window runs, for every window routing counts
	// (not one set aside, as on-demand spending is); 0 is off.
	UsageAlert int `json:"usageAlert,omitempty"`
	// BalanceAlert is the balance a key or account has fallen to when
	// magpie says so, in that balance's own currency or credits, once until
	// it is topped up past it again; 0 is off.
	BalanceAlert float64 `json:"balanceAlert,omitempty"`
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
	// TextSize is how large the window's and the tray panel's pages are
	// drawn, in percent (one of TextSizes): the webviews' own zoom, as a
	// browser's, so the text and everything around it grow together.
	TextSize int `json:"textSize,omitempty"`
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
	// HiddenModels are the catalog entries (their ids, "<provider>/<model>"
	// or a group's) taken out of an agent's lists one by one, by agent id,
	// after Visible: a model not named here, a new one among them, is shown.
	HiddenModels map[string][]string `json:"hiddenModels,omitempty"`

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
	// those (see provider.SetModelEfforts).
	ModelEfforts map[string][]string `json:"modelEfforts,omitempty"`
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
	// ModelWires is the name to send a vendor for a model magpie knows by
	// another, by "<provider id>/<model id>", and "*" for every model of that
	// provider. A "*" in the name is the model itself, so one name covers a
	// relay that namespaces its models — vendor-c/* asks for model-3 as
	// vendor-c/model-3 — while a name with no "*" in it sends every model of
	// that key under that one name. Everything else — the catalog agents see,
	// the routing groups, the usage records and what a call is priced at —
	// keeps the name magpie knows the model by.
	ModelWires map[string]string `json:"modelWires,omitempty"`
	// The main window's size when it was last resized, width and height,
	// so it opens at it again after a restart.
	Window []int `json:"window,omitempty"`
}

// ModelPrice is the price of one model as the user states it. Each part is a
// pointer so that a file leaving one out is told so rather than billing that
// part of the call at zero: a model is priced whole or not at all.
type ModelPrice struct {
	Input      *float64 `json:"input,omitempty"`
	Output     *float64 `json:"output,omitempty"`
	CacheRead  *float64 `json:"cache_read,omitempty"`
	CacheWrite *float64 `json:"cache_write,omitempty"`
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
	return catalog.Price{
		Input: *m.Input, Output: *m.Output,
		CacheRead: *m.CacheRead, CacheWrite: *m.CacheWrite,
	}, ""
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
	if _, bad := m.Price(); bad != "" {
		return fmt.Errorf("the price of %q needs %s %s price that is present, finite and not negative", key, anArticle(bad), bad)
	}
	return nil
}

// AnyProvider begins a ModelPrices key that prices a model whichever
// provider it came through, its id lower-cased after it.
const AnyProvider = "*/"

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

// Themes and Langs are the accepted values, in the order the UI offers them.
var (
	Themes     = []string{"system", "light", "dark"}
	Langs      = []string{"system", "en", "zh"}
	Trays      = []string{"panel", "window"}
	Currencies = []string{"usd", "cny"}
	// Warmups are CodexWarmup's and ClaudeWarmup's values, off as "".
	Warmups = []string{"", "week", "all"}
	// TrayEvery are TrayUsageEvery's values, in minutes.
	TrayEvery = []int{1, 3, 5, 10, 30}
	// TextSizes are TextSize's values, in percent. None is under 100: the
	// webviews' zoom on Windows and Linux (Wails' SetZoom) goes no lower.
	TextSizes = []int{100, 110, 125, 150}
)

var validTerminalBundleID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,254}$`)

// providerID is how a provider's id is spelled: lower-case letters, digits
// and dashes, as provider.Slug derives it (a custom provider's id is the
// same, the site it is on when its name has none).
var providerID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

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

// KeepOwn puts back cur's settings that are this computer's own, which a
// sync or a restored backup never brings from another: the window's size,
// the proxy, the Dock, and what the menu bar or tray shows beside magpie's
// icon (yoooo on Discord: usage turned off on a Mac came back from a
// Windows box that shows it).
func (s *Settings) KeepOwn(cur Settings) {
	s.Window, s.Proxy, s.Dock, s.DockWindow = cur.Window, cur.Proxy, cur.Dock, cur.DockWindow
	s.TrayUsages, s.TrayUsage, s.TrayUsageEvery, s.TrayNoLogos = cur.TrayUsages, cur.TrayUsage, cur.TrayUsageEvery, cur.TrayNoLogos
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
		return fmt.Errorf("proxy must look like http://127.0.0.1:7890 or socks5://127.0.0.1:1080, not %q", p)
	}
	return nil
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
	if s.SessionTerminal != "" && s.SessionTerminal != "system" && !validTerminalBundleID.MatchString(s.SessionTerminal) {
		return fmt.Errorf("session terminal must be an app bundle id or system, not %q", s.SessionTerminal)
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
	for _, at := range []string{s.CodexWarmAt, s.ClaudeWarmAt} {
		if _, _, ok := Clock(at); at != "" && !ok {
			return fmt.Errorf("a warm-up's time of day must look like 06:00, not %q", at)
		}
	}
	if !slices.Contains(TrayEvery, s.TrayUsageEvery) {
		return fmt.Errorf("the menu bar's usage is refreshed every %v minutes, not %d", TrayEvery, s.TrayUsageEvery)
	}
	if s.UsageAlert < 0 || s.UsageAlert > 100 {
		return fmt.Errorf("a usage alert is at a percentage from 1 to 100, or 0 for off, not %d", s.UsageAlert)
	}
	if math.IsNaN(s.BalanceAlert) || math.IsInf(s.BalanceAlert, 0) || s.BalanceAlert < 0 {
		return fmt.Errorf("a balance alert is at an amount of 0 or more (0 for off), not %v", s.BalanceAlert)
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
	s.TrayUsages = ids(s.TrayUsages)
	for i, u := range s.CodexAutoReset {
		s.CodexAutoReset[i] = strings.ToLower(u)
	}
	s.CodexAutoReset = ids(s.CodexAutoReset)
	s.TrayUsage = ""
	if len(s.TrayUsages) > 0 {
		s.TrayUsage = s.TrayUsages[0]
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
	return os.WriteFile(Path(), append(b, '\n'), 0o600)
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
	if s.TextSize == 0 {
		s.TextSize = 100
	}
	// one card, as a magpie before TrayUsages kept it; an empty list
	// sent on purpose (all of them turned off) stays empty
	if s.TrayUsages == nil && s.TrayUsage != "" {
		s.TrayUsages = []string{s.TrayUsage}
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
