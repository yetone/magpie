// Package settingsapi is what the Settings page's save writes: the fields
// POST /api/settings takes from the page, how a value is checked, and the
// save itself. The window's handler and `magpie settings` both save through
// Apply, so a value the CLI sets is checked and kept exactly as one the
// page sets (#4745).
package settingsapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// Keys are the settings POST /api/settings saves from what it is sent, by
// their JSON names, in the order `magpie settings list` shows them. Every
// other field of settings.Settings is carried from the file (Merge), set
// by a route of its own or kept by settings.Save. TestKeysAreWhatTheSaveWrites
// fails when a field the save writes is missing here, or one here isn't
// written.
var Keys = []string{
	// Appearance
	"theme", "colorblind", "reduceMotion", "noBird", "lang", "uiFont", "codeFont",
	// the tray, the Dock and this computer
	"tray", "dock", "dockWindow", "dockNever", "lightweight", "keepAwake", "keepAwakeDisplay",
	"noWSLAgents", "sessionTerminal", "signInLinkOnly", "quitNoAsk",
	// the menu bar's allowance cards
	"trayUsages", "trayUsage", "trayUsageEvery", "trayNoLogos", "trayNoBird", "trayNoNumber", "trayPace", "trayNoCost", "trayWindows",
	// usage and alerts
	"currency", "chineseUnits", "usageAlert", "balanceAlert", "resetReminder", "turnDoneAlert", "maskAlert",
	// network
	"proxy",
	// privacy
	"redact", "redactPersonal", "redactKinds", "redactWords", "redactWordsAnyCase", "redactWordsWhole", "redactShape",
	"noStats", "noUsageStats", "otel",
	// warm-ups and check-ins
	"codexWarmup", "claudeWarmup", "codexWarmAt", "claudeWarmAt", "codexWarmAts", "claudeWarmAts",
	"workbuddyCheckin", "traeCheckin", "minimaxCheckin", "qoderCheckin",
	// models: replies, vision, images, search
	"memberModel", "noLoopGuard", "claudeLeanPrompt", "claudePrintMode", "refusalRetries",
	"vision", "visionEffort", "imageGen", "imageTimeout", "classifyTimeout",
	"searcher", "searcherThen", "searcherEffort", "searchFirst",
	// updates
	"noUpdatePill", "noAutoUpdate", "updateEvery", "noAutoDownload", "autoRestart", "updateChannel",
}

// Choices are the values a setting with a closed set takes, for
// `magpie settings list` to name: settings' own lists, which settings.Save
// checks a saved value against (a value outside them is refused there,
// from the window as from the CLI). A setting's "" is its default.
var Choices = map[string][]string{
	"theme":          settings.Themes,
	"lang":           settings.Langs,
	"tray":           settings.Trays,
	"currency":       settings.Currencies,
	"codexWarmup":    {"off", "week", "all"},
	"claudeWarmup":   {"off", "week", "all"},
	"trayUsageEvery": ints(settings.TrayEvery),
	"updateChannel":  {settings.UpdateStable},
	"updateEvery":    ints(settings.UpdateEveries),
	"searchFirst":    {settings.SearchFirstModel, settings.SearchFirstAPI},
}

func ints(v []int) []string {
	out := make([]string, len(v))
	for i, n := range v {
		out[i] = strconv.Itoa(n)
	}
	return out
}

// Field is the struct field of settings.Settings saved under the JSON name
// key, and whether there is one.
func Field(key string) (reflect.StructField, bool) {
	t := reflect.TypeFor[settings.Settings]()
	for i := range t.NumField() {
		f := t.Field(i)
		if name, _, _ := strings.Cut(f.Tag.Get("json"), ","); name == key {
			return f, true
		}
	}
	return reflect.StructField{}, false
}

// Value is key's value in s, as it is saved.
func Value(s settings.Settings, key string) (any, bool) {
	f, ok := Field(key)
	if !ok {
		return nil, false
	}
	return reflect.ValueOf(s).FieldByIndex(f.Index).Interface(), true
}

// Known says whether key is one Apply saves.
func Known(key string) bool { return slices.Contains(Keys, key) }

// Body is what the Settings page sends to set key to value (its JSON) and
// leave everything else as cur has it: cur whole, with key sent even when
// it is a zero value, which a field omitted would not be (an omitted
// noBird is "keep", false is "show the bird").
func Body(cur settings.Settings, key string, value json.RawMessage) ([]byte, error) {
	if !Known(key) {
		return nil, fmt.Errorf("unknown setting %q", key)
	}
	b, err := json.Marshal(cur)
	if err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	m[key] = value
	return json.Marshal(m)
}

// Apply is POST /api/settings: body read over the settings saved now
// (Merge), checked and saved. It returns what was saved and what was there
// before, for the caller's reactions to a change (the window's Dock, fonts,
// tray menu and alerts). web is a page served to a browser, whose fonts are
// another computer's and never saved.
func Apply(body []byte, web bool) (in, cur settings.Settings, err error) {
	// Unreadable settings are never carried as the defaults (an empty
	// allow list, every address let in): Save would refuse them too.
	cur, err = settings.LoadChecked()
	if err != nil {
		return in, cur, err
	}
	in, err = Merge(body, cur, web)
	if err != nil {
		return in, cur, err
	}
	if err := settings.Save(in); err != nil {
		return in, cur, err
	}
	// whether agents are told every model takes images follows Vision
	// (provider.Described)
	if strings.TrimSpace(in.Vision) != strings.TrimSpace(cur.Vision) {
		catalog.Touched()
	}
	return in, cur, nil
}

// Merge is the settings body sets over cur: the page's own choices from
// body, and everything else as cur has it. It writes nothing.
func Merge(body []byte, cur settings.Settings, web bool) (settings.Settings, error) {
	var in settings.Settings
	dec := json.NewDecoder(bytes.NewReader(body))
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return in, err
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return in, err
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	// the Settings page sends its own choices; how the agents are
	// arranged is the Agents page's, and the window's size its own; both stay as they are.
	// Older pages do not send fonts. An explicit null restores the
	// default, while an omitted field (or a browser save) preserves it.
	if _, sent := fields["uiFont"]; !sent || web {
		in.UIFont = cur.UIFont
	}
	if _, sent := fields["codeFont"]; !sent || web {
		in.CodeFont = cur.CodeFont
	}
	// and pages from before it do not send how often a refusal is asked
	// again, which null sets back to every one left
	if _, sent := fields["refusalRetries"]; !sent {
		in.RefusalRetries = cur.RefusalRetries
	}
	// nor whether updates download by themselves (#2296)
	if _, sent := fields["noAutoDownload"]; !sent {
		in.NoAutoDownload = cur.NoAutoDownload
	}
	// nor the update channel (ssharpcc on X)
	if _, sent := fields["updateChannel"]; !sent {
		in.UpdateChannel = cur.UpdateChannel
	}
	// nor whether the masked words match in any case
	if _, sent := fields["redactWordsAnyCase"]; !sent {
		in.RedactWordsAnyCase = cur.RedactWordsAnyCase
	}
	// nor whether they match only as whole words
	if _, sent := fields["redactWordsWhole"]; !sent {
		in.RedactWordsWhole = cur.RedactWordsWhole
	}
	// nor whether a key goes as a made-up one of its form
	if _, sent := fields["redactShape"]; !sent {
		in.RedactShape = cur.RedactShape
	}
	// nor how long an image is given to be drawn
	if _, sent := fields["imageTimeout"]; !sent {
		in.ImageTimeout = cur.ImageTimeout
	}
	// nor how long a routing group's classifier is given (#2934)
	if _, sent := fields["classifyTimeout"]; !sent {
		in.ClassifyTimeout = cur.ClassifyTimeout
	}
	// nor whether animations are stilled (#3813)
	if _, sent := fields["reduceMotion"]; !sent {
		in.ReduceMotion = cur.ReduceMotion
	}
	// nor whether the loading bird is kept off
	if _, sent := fields["noBird"]; !sent {
		in.NoBird = cur.NoBird
	}
	// nor whether a Quit asks first while agents go through magpie
	// (zhujiu on Discord), which the Quit question sets too
	if _, sent := fields["quitNoAsk"]; !sent {
		in.QuitNoAsk = cur.QuitNoAsk
	}
	// nor whether a value masked for the first time is told (Must2333)
	if _, sent := fields["maskAlert"]; !sent {
		in.MaskAlert = cur.MaskAlert
	}
	in.AgentOrder, in.AgentsHidden, in.AgentsShown, in.AgentsHere = cur.AgentOrder, cur.AgentsHidden, cur.AgentsShown, cur.AgentsHere
	in.Window, in.WindowMaximised, in.WindowAt = cur.Window, cur.WindowMaximised, cur.WindowAt // the window's own, as it was last resized and moved
	// and what other pages keep here: which models an agent is shown, and
	// everything the user said of a model anywhere else in the app, set on
	// its own. The per-model maps are carried whole rather than named one
	// by one, so a map added later is not silently dropped here.
	//
	// HiddenModels, PickedModels and OrderedModels are the other way
	// round — keyed by agent, not by "<provider>/<model>" — so they are
	// not among them, and belong to the Agents page.
	in.Visible, in.HiddenModels, in.OrderedModels = cur.Visible, cur.HiddenModels, cur.OrderedModels
	in.PickedModels = cur.PickedModels       // "only models I pick" (#1337)
	in.OwnModelsHidden = cur.OwnModelsHidden // an agent's own models left out (#1642)
	in.FastPicks = cur.FastPicks             // switched in the agents' pickers (#954)
	in.UltraPicks = cur.UltraPicks           // and sent Ultrafast (#1954)
	in.AgentEfforts = cur.AgentEfforts       // picked in an agent's row (#1003)
	in.PluginCheckins = cur.PluginCheckins   // set on its own (plugin-checkin)
	in.PluginOptions = cur.PluginOptions     // set on its own (plugin-option)
	in.CheckinOff = cur.CheckinOff           // set on its own (checkin-account)
	settings.CarryPerModel(&in, &cur)
	in.LAN, in.LANKey = cur.LAN, cur.LANKey
	in.LANKeyID = cur.LANKeyID
	in.Port = cur.Port                                 // set on its own (port), which moves the gateway
	in.CORSOrigins = cur.CORSOrigins                   // set on its own (cors)
	in.AllowIPs = cur.AllowIPs                         // set on its own (allowips)
	in.FetchPrivate = cur.FetchPrivate                 // in settings.json only
	in.RouterNote = cur.RouterNote                     // set on its own (the Library's servers/hub-note)
	in.GitHubToken = cur.GitHubToken                   // set on its own (github-token), never sent to the page
	in.ProxyCustom = cur.ProxyCustom                   // set by settings.Save from the proxy, never by the page
	in.RequestArchive = cur.RequestArchive             // the Gateway page's, set on its own
	in.RequestArchiveMaxMB = cur.RequestArchiveMaxMB   // in settings.json only
	in.ClaudeRunsMost = cur.ClaudeRunsMost             // magpie claude-code runs, or settings.json (#2956)
	in.GatewayConversations = cur.GatewayConversations // Sessions' explicit recording consent
	in.RedactRules = cur.RedactRules                   // the masking rules, set on their own
	in.RedactModel = cur.RedactModel                   // set on its own (privacy-model)
	in.RedactModelName = cur.RedactModelName           // set on its own (privacy-model)
	in.RedactModelScore = cur.RedactModelScore         // set on its own (privacy-model)
	in.RedactModelOff = cur.RedactModelOff             // set on its own (privacy-model)
	in.RedactModelAuto = cur.RedactModelAuto           // set on its own (masking)
	in.RedactActions = cur.RedactActions               // set on their own (redact-actions)
	in.RedactAllow = cur.RedactAllow                   // set on its own (redact-allow)
	in.DisabledModels = cur.DisabledModels             // set on their own (disabled-models, #1867)
	// the request archive on this computer: the archive's switch while no
	// S3 bucket, and its size in settings.json only
	in.RequestArchiveLocal, in.RequestArchiveLocalMB = cur.RequestArchiveLocal, cur.RequestArchiveLocalMB
	// used or left is the Usage page's toggle as much as Settings', set on its own
	in.QuotaLeft = cur.QuotaLeft
	in.QuotaReads = cur.QuotaReads // set on its own (quota-reads)
	in.SearchAll = cur.SearchAll   // set on its own (search-all)
	in.UsageOrder = cur.UsageOrder // the Usage page's, dragged there
	// and what the tray panel's Allowances tab leaves out, set there
	in.PanelUsageHidden = cur.PanelUsageHidden
	// and the clients the Usage page's Agents list leaves out, set there
	in.UsageAgentsHidden = cur.UsageAgentsHidden
	// and the allowance cards the Usage page's Overview leaves out, set there
	in.UsageCardsHidden = cur.UsageCardsHidden
	// and the names models' usage is counted under (magpie model alias)
	in.UsageAliases = cur.UsageAliases
	// and the clears of usage made on the Usage page
	in.UsageCleared = cur.UsageCleared
	// and the clients it counts as agents, set there too (#1958)
	in.ClientAgents = cur.ClientAgents
	// how agents' lists name models, set on its own for the agents to be told
	in.PlainNames, in.PlainOwnNames = cur.PlainNames, cur.PlainOwnNames
	in.BareModelIDs = cur.BareModelIDs // set on its own (bare-ids)
	in.BusyLoad = cur.BusyLoad         // and so is this (busy-load)
	in.CodexAgentsV1 = cur.CodexAgentsV1
	in.FullContext = cur.FullContext // set on its own (full-context)
	in.CompactAt = cur.CompactAt     // and so is the threshold
	// Codex's background compaction, set on its own (codex-async-compact)
	in.CodexAsyncCompact, in.CodexAsyncCompactPercent = cur.CodexAsyncCompact, cur.CodexAsyncCompactPercent
	// Claude Desktop's list, set in its row on the Agents page
	in.DesktopLongest = cur.DesktopLongest
	// and the Get started card put away, from the card itself
	in.GuideHidden = cur.GuideHidden

	in.CodexTitles = cur.CodexTitles             // set on its own (codex-titles)
	in.CodexDescriptions = cur.CodexDescriptions // set on its own (codex-descriptions)
	in.CodexPredictions = cur.CodexPredictions   // and codex-predictions
	// and so is the model Codex's auto-review runs on (codex-auto-review)
	in.CodexAutoReview = cur.CodexAutoReview
	// and the models Codex's and Desktop's subagents are put on, set
	// in their Agents rows
	in.CodexSubagentModel = cur.CodexSubagentModel
	in.DesktopSubagentModel = cur.DesktopSubagentModel
	// and the model for requests masking would change (redact-route)
	in.RedactRouteTo = cur.RedactRouteTo
	in.DesktopSubagentEffort = cur.DesktopSubagentEffort
	in.ChinaMirror = cur.ChinaMirror // the Plugins page's, set on its own
	in.NPMRegistry = cur.NPMRegistry // and its npm registry
	// which Codex accounts spend a reset by themselves, set on the Usage card
	in.CodexAutoReset = cur.CodexAutoReset
	in.CodexAutoResetOnly = cur.CodexAutoResetOnly // and at which of the two times
	in.CodexResetLead = cur.CodexResetLead         // and how early before one expires (codex-reset-lead)
	// and which of them spend their credits, set there too
	in.CodexNoCredits = cur.CodexNoCredits
	in.CodexCreditsLast = cur.CodexCreditsLast   // and which only after everything else
	in.CodexCreditsFirst = cur.CodexCreditsFirst // and which before another account
	// and each Codex account's own daily warm-up (codex-warm-at)
	in.CodexWarmAtOf = cur.CodexWarmAtOf
	// and the text size, which the keyboard changes too (text-size)
	in.TextSize = cur.TextSize
	// and the quick panel's shortcut, set on its own (panel-shortcut)
	in.PanelShortcut = cur.PanelShortcut
	// the Dock choice's reading, which the page never sets: the file's
	// as read (settings.DockSettled), so a save from the page doesn't
	// read its With window as an older file's Hide
	in.DockSettled = cur.DockSettled
	// the version the Update pill was hidden for, set from the pill
	in.UpdateSkip = cur.UpdateSkip
	// the GitHub mirror updates come through, set by magpie update mirror
	in.UpdateMirror = cur.UpdateMirror
	// gateway mode, set on its own (gateway-mode)
	in.GatewayMode = cur.GatewayMode
	in.NoAccountDiscovery = cur.NoAccountDiscovery // set on its own (account-discovery, or the CLI)
	if v := strings.TrimSpace(in.Vision); v != "" && v != "off" && v != cur.Vision {
		if _, _, ok := provider.Resolve(v); !ok {
			return in, fmt.Errorf("no model %s to describe images", v)
		}
	}
	if v := strings.TrimSpace(in.ImageGen); v != "" && v != "off" && v != cur.ImageGen {
		if _, _, ok := provider.Resolve(v); !ok {
			return in, fmt.Errorf("no model %s to generate images", v)
		}
	}
	// the searchers after the one named, set in their own row: a save
	// that doesn't send them (prefsKeep has none) keeps them, and an
	// empty list clears them
	if in.SearcherThen == nil {
		in.SearcherThen = cur.SearcherThen
	}
	for _, v := range append([]string{in.Searcher}, in.SearcherThen...) {
		if v = strings.TrimSpace(v); v != "" && v != "off" && v != cur.Searcher && !slices.Contains(cur.SearcherThen, v) {
			id, _, _ := strings.Cut(v, "/")
			if !slices.ContainsFunc(gateway.Searchers(), func(c gateway.SearcherChoice) bool { return c.Provider.ID == id }) {
				return in, fmt.Errorf("%s can't search the web for other models", id)
			}
		}
	}
	return in, nil
}
