package agent

// Claude Desktop runs on a third-party gateway in its "3p" deployment mode:
// no Anthropic sign-in, and its Code and Cowork tabs work on any plan (a
// Free account signed in sees only "Get Claude Code" there). The mode is
// claude_desktop_config.json's deploymentMode, in its own folder and in
// Claude-3p beside it; the gateway is a profile in Claude-3p/configLibrary,
// the one _meta.json applies. magpie writes "3p" in both, but Desktop reads
// only Claude-3p's, and not even that one when the profile disables its
// chooser (desktop3p):
//
//	Claude/claude_desktop_config.json      {"deploymentMode":"3p",…}
//	Claude-3p/claude_desktop_config.json   {"deploymentMode":"3p",…}
//	Claude-3p/configLibrary/<id>.json      {"inferenceProvider":"gateway",
//	  "inferenceGatewayBaseUrl":"http://127.0.0.1:3425",
//	  "inferenceGatewayApiKey":"magpie-claude-desktop",
//	  "inferenceGatewayAuthScheme":"bearer",
//	  "disableDeploymentModeChooser":true,"coworkEgressAllowedHosts":["*"],
//	  "toolSearchEnabled":true}
//	Claude-3p/configLibrary/_meta.json     {"appliedId":"<id>",
//	  "entries":[…,{"id":"<id>","name":"magpie"}]}
//
// as CC Switch writes them (claude_desktop_config.rs). The folders are in
// ~/Library/Application Support on macOS, %LOCALAPPDATA% on Windows (the
// MSIX package's LocalCache\Local for a packaged Desktop) and
// $XDG_CONFIG_HOME on Linux, as internal/desktopdir finds them. A
// packaged Desktop gets all of it twice, in the package's folders and the
// real ones: up to 2.x it reads the package's copy, from 2.31226 the real
// %LOCALAPPDATA%\Claude-3p (desktopdir.Sets). With no inferenceModels Desktop lists the
// gateway's /v1/models, which the gateway gives it by ids it keeps (see
// gateway/desktop.go). Desktop reads all this at start-up only.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/desktopdir"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

// desktopProfileID is magpie's profile in Desktop's configLibrary: a
// UUID like the ones Desktop makes, its last part "magpie" in hex.
const desktopProfileID = "00000000-0000-4000-8000-6d6167706965"

// desktopPaths are the files and folders magpie writes for Claude Desktop.
type desktopPaths struct {
	key                 string // the stash's keys end with it: "" or ".package"
	dir, dir3p          string // Claude, Claude-3p
	config, config3p    string // their claude_desktop_config.json
	library, meta, prof string // configLibrary, its _meta.json, magpie's profile
	// Desktop's own settings (config.json, its "locale" among them) in its
	// userData signed in with Anthropic (desktopdir's Data: Claude, or
	// %APPDATA%\Claude on Windows) and in Claude-3p, its userData in 3p
	// mode. magpie only ever adds the language to the second.
	app, app3p string
	// mcpFrom is the claude_desktop_config.json whose MCP servers the 3p
	// mode is given (desktopCarryMCP): config, but for the isolated
	// Desktop, whose own file is Claude-Magpie-3p's, Claude's
	mcpFrom string
}

func desktopPathsOf(dir, dir3p string) desktopPaths {
	lib := filepath.Join(dir3p, "configLibrary")
	return desktopPaths{
		dir: dir, dir3p: dir3p,
		config:   filepath.Join(dir, "claude_desktop_config.json"),
		config3p: filepath.Join(dir3p, "claude_desktop_config.json"),
		library:  lib, meta: filepath.Join(lib, "_meta.json"), prof: filepath.Join(lib, desktopProfileID+".json"),
		app: filepath.Join(dir, "config.json"), app3p: filepath.Join(dir3p, "config.json"),
		mcpFrom: filepath.Join(dir, "claude_desktop_config.json"),
	}
}

// desktopDirs are Desktop's Claude and Claude-3p folders on goos: the ones
// magpie writes deploymentMode and its profile into (desktopdir.Find).
func desktopDirs(goos, home string, getenv func(string) string) (string, string) {
	d := desktopdir.Find(goos, home, getenv)
	return d.Mode, d.ThreeP
}

// desktopSets are the sets of Desktop's folders magpie wires: the
// isolated Desktop's alone while it is on (desktopIsolatedOn), else
// desktopUsualSets.
func desktopSets(goos, home string, getenv func(string) string) []desktopPaths {
	if p, ok := desktopIsolatedSet(goos, home, getenv); ok && desktopIsolatedOn() {
		return []desktopPaths{p}
	}
	return desktopUsualSets(goos, home, getenv)
}

// desktopUsualSets are those of every set of Desktop's folders, Find's
// first: for a packaged Desktop the package's and the real ones, each with
// its own stash.
func desktopUsualSets(goos, home string, getenv func(string) string) []desktopPaths {
	var out []desktopPaths
	for _, d := range desktopdir.Sets(goos, home, getenv) {
		p := desktopPathsOf(d.Mode, d.ThreeP)
		p.app = filepath.Join(d.Data, "config.json")
		if d.Package != "" {
			p.key = ".package"
		}
		out = append(out, p)
	}
	return out
}

// DesktopConfigs3p are the claude_desktop_config.json files Claude Desktop
// reads in its 3p mode: there its whole userData is Claude-3p, its MCP
// servers too (%LOCALAPPDATA%\Claude-3p on Windows, and the MSIX package's;
// Claude-3p beside Claude elsewhere), and the isolated Desktop's
// Claude-Magpie-3p on a Mac, whose only file it is (the library gives it
// the servers where that folder is).
func DesktopConfigs3p(home string) []string {
	var out []string
	for _, p := range desktopUsualSets(desktopdir.OS, home, os.Getenv) {
		out = append(out, p.config3p)
	}
	if p, ok := desktopIsolatedSet(desktopdir.OS, home, os.Getenv); ok {
		out = append(out, p.config3p)
	}
	return out
}

func claudeDesktop(home string) *Agent {
	sets := desktopdir.Sets(desktopdir.OS, home, os.Getenv)
	ps := desktopSets(desktopdir.OS, home, os.Getenv)
	// its row shows the usual folder's file, the library's too, whichever
	// Desktop magpie wires
	p := desktopUsualSets(desktopdir.OS, home, os.Getenv)[0]
	iso := ps[0].key == desktopIsolatedKey
	return &Agent{
		ID: "claude-desktop", Name: "Claude Desktop", Icon: "claude-color", Aliases: []string{"claude-app"},
		Dir: p.dir, Path: p.config,
		detect: func() bool {
			// its own data (%APPDATA%\Claude on Windows, or the MSIX
			// package's), the folder magpie writes or Claude-3p
			for _, s := range sets {
				for _, d := range s.All() {
					if isDir(d) {
						return true
					}
				}
			}
			// the isolated Desktop's folder
			if iso && isDir(ps[0].dir3p) {
				return true
			}
			// installed on Windows and not yet opened: none of them yet
			return desktopdir.Installed(desktopdir.OS, home, os.Getenv)
		},
		Notice: func() string {
			if iso {
				if desktopWiredAny(ps) {
					return noticeClaudedesktopIsolated.String()
				}
				return noticeClaudedesktopIsolatedOff.String()
			}
			if desktopWiredAny(ps) {
				return noticeClaudedesktop1.String() + " " + noticeClaudedesktop3.String()
			}
			return noticeClaudedesktop2.String() + " " + noticeClaudedesktop4.String()
		},
		Check: func() string {
			if !desktopWiredAny(ps) {
				return ""
			}
			for _, p := range desktopRead(ps) {
				if msg := desktopCheck(p); msg != "" {
					if len(ps) > 1 {
						msg += " (" + p.dir3p + ")"
					}
					return msg
				}
			}
			return ""
		},
		Fields: append([]Field{{
			Key: "provider", Label: "provider",
			Get: func() string {
				if desktopWiredAny(ps) {
					return magpieID
				}
				return ""
			},
			Set: func(v string) error {
				on := desktopOn
				if v == "" {
					on = desktopOff
				}
				desktopAdoptStash(ps)
				src := desktopLocaleFrom(ps)
				for _, p := range ps {
					if src != "" {
						p.app = src
					}
					if err := on(p); err != nil {
						return err
					}
				}
				// its Code tab is Claude Code, told what Desktop's ids for
				// magpie's models can do while that one runs on magpie
				_ = claude(home).Sync()
				// and a running distro's, which a Code tab session with
				// WSL as its host runs on (a stopped one is written on
				// the round after it starts)
				for _, a := range wslAgents() {
					if strings.HasPrefix(a.ID, "claude@wsl:") && a.Sync != nil {
						_ = a.Sync()
					}
				}
				return nil
			},
			Options: func(map[string]string) []Option {
				return []Option{{Value: magpieID, Label: "magpie", Icon: "magpie",
					Note: "Desktop's third-party gateway: Code and Cowork on magpie's models, no Anthropic sign-in (restart Desktop)"}}
			},
		}}, append(desktopTierFields(home, ps), desktopSubagentField(ps), desktopSubagentEffortField(ps))...),
		// a Desktop already on magpie gets the import and tool search
		// policies too (desktopSeedImport, desktopSeedToolSearch), and a
		// profile for magpie's gateway it applies a key the gateway takes
		// (desktopKeyApplied)
		Sync: func() error {
			var errs []error
			for _, p := range ps {
				if desktopWired(p) {
					if err := desktopSeedImport(p); err != nil {
						errs = append(errs, err)
					}
					if err := desktopSeedToolSearch(p); err != nil {
						errs = append(errs, err)
					}
					if err := desktopKeyApplied(p); err != nil {
						errs = append(errs, err)
					}
					// the models and picks as they are now (a provider
					// saved or removed reaches it through SyncCatalog)
					if err := desktopSyncModels(p); err != nil {
						errs = append(errs, err)
					}
					// a Desktop on magpie from before the servers were
					// carried gets them once
					if stashShared()[desktopMCPSeen+p.key] == "" {
						if err := desktopCarryMCP(p); err != nil {
							errs = append(errs, err)
						}
					}
				}
			}
			return joinErrs(errs)
		},
	}
}

// desktopRead are the sets of Desktop's folders whose wiring counts: the
// one whose Claude-3p Desktop ran in last, which is where it reads
// deploymentMode and the gateway from, or every set when it has run in
// none. Magpie writes both of a packaged Desktop's sets, not knowing the
// build: 2.31226's MSIX keeps Claude-3p out of the package
// (ExcludedDirectory), so the package's copy is one Desktop no longer
// reads, and one gone from there (emptied by an update) isn't Desktop off
// magpie (dumplings on Discord: "配置已变更" while Desktop ran on magpie).
func desktopRead(ps []desktopPaths) []desktopPaths {
	var newest time.Time
	var at []desktopPaths
	for _, p := range ps {
		if t := desktopdir.LastRun(p.dir3p); t.After(newest) {
			newest, at = t, []desktopPaths{p}
		}
	}
	if at == nil {
		return ps
	}
	return at
}

// desktopCheck is what keeps one set of Desktop's folders off magpie's
// gateway, "" for nothing.
func desktopCheck(p desktopPaths) string {
	prof := p.prof
	if id, _ := edit.GetJSON(p.meta, "appliedId"); id != desktopProfileID {
		// one for magpie's gateway the user applied is on magpie too
		if prof = desktopAppliedGateway(p); prof == "" {
			return "Claude Desktop uses another gateway configuration now (appliedId in configLibrary/_meta.json), not magpie's"
		}
	}
	if !desktop3p(p, prof) {
		return "Claude Desktop's deploymentMode (Claude-3p/claude_desktop_config.json) is 1p now, so it signs in with Anthropic rather than using magpie"
	}
	want := agentKey("claude-desktop")
	return wiringOff("Claude Desktop", prof, func(k string) (string, bool) {
		v, ok := edit.GetJSON(prof, k)
		if k != "inferenceGatewayApiKey" {
			return v, ok
		}
		// Desktop seals the key at rest (desktopOpen): the one it opens
		// to is the one it sends; one magpie can't open is unknown, and
		// not said to be changed
		open, known := desktopOpen(k, v)
		if !known {
			return want, true
		}
		if prof != p.prof && desktopKeyTaken(open, want) {
			return want, true // a key of magpie's other than Desktop's own
		}
		return open, ok
	}, "inferenceGatewayBaseUrl", gateway.URL(), "inferenceGatewayApiKey", want)
}

// desktop3p: Desktop starts in its 3p mode with prof, the gateway profile
// it applies, as Desktop decides it (m4 in 4.4212's and 2.7032's
// index.pre.js): with the profile's disableDeploymentModeChooser, whatever
// deploymentMode says, and otherwise unless Claude-3p's
// claude_desktop_config.json holds "1p" — none is 3p. The Claude folder's
// deploymentMode isn't read, and Desktop deletes Claude-3p's itself: a
// stored "1p" with no claude.ai sign-in is dropped at start and Desktop
// relaunches in 3p ("deploymentMode written { mode: undefined }"), so
// requiring "3p" in both files called a Desktop on magpie off it (dev_quj
// on X: "已不经过 magpie：配置已变更" after a restart).
func desktop3p(p desktopPaths, prof string) bool {
	if c, _ := edit.GetJSON(prof, "disableDeploymentModeChooser"); c == "true" {
		return true
	}
	m, _ := edit.GetJSON(p.config3p, "deploymentMode")
	return m != "1p"
}

// desktopKeyTaken: the gateway takes v where magpie gives Desktop want,
// as keyHolds says, or v is a key of magpie's own (gateway.Token, or one
// naming an agent) where want is one too: the gateway takes those alike.
func desktopKeyTaken(v, want string) bool {
	return keyHolds(v, want) || gatewayKey(want) && v != "" && gatewayKey(v)
}

// desktopAppliedGateway is the configLibrary profile Desktop applies when
// it is not magpie's own but a gateway profile for magpie's gateway, ""
// for none. Desktop's "Import configuration" makes one: it adds the file
// it is given as a new entry under an id of its own (configLibrary's
// createConfig, a random UUID) and writes the file's settings into it, so
// magpie's profile exported on one computer and imported on another is
// applied there under another id, with the connectors (managedMcpServers)
// and settings the user made in it (aa86222 on Discord: 配置已变更 while
// Desktop ran on magpie). It is the user's profile: magpie keeps it
// applied and sets nothing in it but a key the gateway refuses.
func desktopAppliedGateway(p desktopPaths) string {
	id, _ := edit.GetJSON(p.meta, "appliedId")
	if id == "" || id == desktopProfileID || filepath.Base(id) != id || strings.HasPrefix(id, ".") {
		return ""
	}
	f := filepath.Join(p.library, id+".json")
	b, err := os.ReadFile(f)
	if err != nil || !desktopObjectBytes(b) {
		return ""
	}
	var prof struct {
		Provider string `json:"inferenceProvider"`
		BaseURL  string `json:"inferenceGatewayBaseUrl"`
	}
	if json.Unmarshal(b, &prof) != nil || prof.Provider != "gateway" || !desktopIsOurGateway(prof.BaseURL) {
		return ""
	}
	return f
}

// desktopKeyApplied gives the profile desktopAppliedGateway finds the
// key magpie gives Desktop, only where the one it holds is a key this
// gateway won't take (another magpie's, from the computer it was exported
// on): one it takes stays, and so does one Desktop sealed that magpie
// can't open (desktopOpen). Its address and every other setting, its
// connectors among them, stay as they are.
func desktopKeyApplied(p desktopPaths) error {
	f := desktopAppliedGateway(p)
	if f == "" {
		return nil
	}
	want := agentKey("claude-desktop")
	v, _ := edit.GetJSON(f, "inferenceGatewayApiKey")
	// a key Desktop sealed that magpie can't open is left as it is
	if open, known := desktopOpen("inferenceGatewayApiKey", v); !known || desktopKeyTaken(open, want) {
		return nil
	}
	kvs := []edit.KV{{Path: "inferenceGatewayApiKey", Value: want}}
	if v, _ := edit.GetJSON(f, "inferenceGatewayAuthScheme"); v == "" {
		kvs = append(kvs, edit.KV{Path: "inferenceGatewayAuthScheme", Value: "bearer"})
	}
	return edit.SetJSON(f, kvs...)
}

// desktopTierFields pick the model each of Claude Code's tiers runs on in
// Desktop's Code tab, its subagents' sonnet, haiku or opus among them
// (gateway.DesktopTiers). Unset, the chat's model runs it, or, for a chat
// on a Claude model, its provider's model of the tier: another provider's
// Claude model, or a Claude model beside a chat on another vendor's, is
// never used unpicked. Desktop reads them from the
// gateway's /v1/models when it starts, and a pick's behavesAs from its
// profile's inferenceModels (desktopSyncModels), also at start-up.
func desktopTierFields(home string, ps []desktopPaths) []Field {
	var fields []Field
	for _, tier := range gateway.DesktopTierNames {
		fields = append(fields, Field{
			Key: tier, Label: tier, Quiet: true,
			Get: func() string { return gateway.DesktopTiers()[tier] },
			Set: func(v string) error {
				v = gateway.DesktopCatalogID(v)
				if v != "" && !isMagpie(v) {
					return fmt.Errorf("%s: %q is not a model magpie serves", tier, v)
				}
				if err := gateway.SetDesktopTier(tier, v); err != nil {
					return err
				}
				// the profile's list of models, whose behavesAs a pick
				// has, and the window the Code tab's Claude Code holds it
				// to (claudeBudget's behaves)
				var errs []error
				for _, p := range ps {
					if err := desktopSyncModels(p); err != nil {
						errs = append(errs, err)
					}
				}
				if desktopWiredAny(ps) {
					_ = claude(home).Sync()
				}
				return joinErrs(errs)
			},
			Options: func(map[string]string) []Option {
				if !desktopWiredAny(ps) {
					return nil
				}
				return viaMagpie("claude-desktop", "")
			},
		})
	}
	return fields
}

// desktopSubagentField picks the model every subagent turn in Desktop's
// Code tab and Cowork runs on, whatever its lead or tier asked for. It
// writes nothing to Desktop's config, which has no key for it, and the
// Claude Code it runs ignores CLAUDE_CODE_SUBAGENT_MODEL in ~/.claude
// while Desktop manages its provider: it is magpie's setting
// (settings.DesktopSubagentModel), which the gateway reads on each
// request (gateway desktopSubagentPick), so it is Live and a change needs
// no restart of Desktop. Labelled as Codex's "subagent model", it gets the
// same square on the Agents page. A Desktop alias picked is kept by its
// catalog id.
func desktopSubagentField(ps []desktopPaths) Field {
	return Field{
		Key: "subagent_model", Label: "subagent model", Quiet: true, Live: true,
		Get: provider.DesktopSubagentModel,
		Set: func(v string) error {
			return provider.SetDesktopSubagentModel(gateway.DesktopCatalogID(v))
		},
		Options: func(map[string]string) []Option {
			if !desktopWiredAny(ps) {
				return nil
			}
			return viaMagpie("claude-desktop", "")
		},
	}
}

// Desktop's subagent effort is a live gateway setting, like its subagent
// model. With no model picked, a subagent may use any of Desktop's models,
// so all levels are offered and the gateway fits each to the actual model.
func desktopSubagentEffortField(ps []desktopPaths) Field {
	return Field{
		Key: "subagent_effort", Label: "subagent effort", Quiet: true, Live: true,
		Get: provider.DesktopSubagentEffort,
		Set: provider.SetDesktopSubagentEffort,
		Options: func(map[string]string) []Option {
			if !desktopWiredAny(ps) {
				return nil
			}
			target := provider.DesktopSubagentModel()
			if target == "" {
				return static(provider.MemberEfforts...)
			}
			for _, e := range provider.Served() {
				if e.ID != target {
					continue
				}
				if provider.Reasonless(e.Provider.ID, e.Model) {
					return nil
				}
				var levels []string
				for _, level := range e.Efforts {
					if slices.Contains(provider.MemberEfforts, level) {
						levels = append(levels, level)
					}
				}
				if len(levels) > 0 {
					return static(levels...)
				}
			}
			return static(provider.MemberEfforts...)
		},
	}
}

// desktopWired: _meta.json lists magpie's profile.
func desktopWired(p desktopPaths) bool {
	entries, _, err := desktopEntries(p.meta)
	return err == nil && slices.ContainsFunc(entries, desktopOurs)
}

// desktopWiredAny: any set of Desktop's folders is on magpie.
func desktopWiredAny(ps []desktopPaths) bool { return slices.ContainsFunc(ps, desktopWired) }

// desktopEntries reads _meta.json's entries, and whether the file is there.
func desktopEntries(meta string) ([]json.RawMessage, bool, error) {
	b, err := edit.Read(meta)
	if err != nil || b == nil {
		return nil, false, err
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return nil, true, nil
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, true, errors.New(meta + ": not a JSON object")
	}
	var entries []json.RawMessage
	if raw, ok := doc["entries"]; ok {
		if err := json.Unmarshal(raw, &entries); err != nil {
			return nil, true, errors.New(meta + ": entries is not a list")
		}
	}
	return entries, true, nil
}

func desktopEntryID(raw json.RawMessage) string {
	var e struct {
		ID string `json:"id"`
	}
	json.Unmarshal(raw, &e)
	return e.ID
}

func desktopOurs(raw json.RawMessage) bool { return desktopEntryID(raw) == desktopProfileID }

// desktopObject: the file is missing, empty or a JSON object — one magpie
// can edit without losing anything.
func desktopObject(path string) error {
	b, err := edit.Read(path)
	if err != nil || len(bytes.TrimSpace(b)) == 0 {
		return err
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(b, &m) != nil {
		return errors.New(path + ": not a JSON object")
	}
	return nil
}

// The stash keeps what Desktop had before magpie: each file's
// deploymentMode ("" when it had none), the profile applied, and the
// files and folders magpie made, so switching off puts all of it back.
// It also keeps magpie's profile as it was when switched off, the
// settings the user made in it from Desktop's window among them, so
// switching on again brings them back (#2035).
// A package's folders keep theirs under the same keys ending in ".package".
const (
	desktopStashed = "claude-desktop.on"
	desktopMode    = "claude-desktop.mode"
	desktopMode3p  = "claude-desktop.mode3p"
	desktopApplied = "claude-desktop.applied"
	desktopMade    = "claude-desktop.made"
	desktopProfile = "claude-desktop.profile"
	desktopLocale  = "claude-desktop.locale"
	desktopImport  = "claude-desktop.import"
	desktopSearch  = "claude-desktop.tool-search"
	desktopMCP     = "claude-desktop.mcp"
	desktopMCPSeen = "claude-desktop.mcp-seen"
)

var desktopStashKeys = []string{desktopStashed, desktopMode, desktopMode3p, desktopApplied, desktopMade, desktopProfile, desktopLocale, desktopImport, desktopSearch, desktopMCP, desktopMCPSeen, desktopModelsMark}

// desktopAdoptStash moves the stash a packaged Desktop's folders got
// before they had keys of their own (when magpie wrote only one set) onto
// the package's keys: the stash is theirs when the package's folders are
// on magpie and the real ones aren't.
func desktopAdoptStash(ps []desktopPaths) {
	var pkg, real *desktopPaths
	for i := range ps {
		if ps[i].key == ".package" {
			pkg = &ps[i]
		} else {
			real = &ps[i]
		}
	}
	if pkg == nil || real == nil || !desktopWired(*pkg) || desktopWired(*real) {
		return
	}
	m := stashLoad()
	if m[desktopStashed] == "" || m[desktopStashed+pkg.key] != "" {
		return
	}
	kv := map[string]string{}
	for _, k := range desktopStashKeys {
		kv[k+pkg.key], kv[k] = m[k], ""
	}
	stash(kv)
}

// desktopOn points Claude Desktop at the gateway: deploymentMode 3p in both
// config files, magpie's profile written and applied. Every other key, and
// every other profile, stays as it is.
func desktopOn(p desktopPaths) error {
	// nothing is written unless every file can be
	for _, f := range []string{p.config, p.config3p, p.prof} {
		if err := desktopObject(f); err != nil {
			return err
		}
	}
	entries, _, err := desktopEntries(p.meta)
	if err != nil {
		return err
	}
	wired := slices.ContainsFunc(entries, desktopOurs)
	if !wired {
		var made []string
		for _, f := range []string{p.dir, p.config, p.dir3p, p.config3p, p.library, p.meta, p.app3p} {
			if _, err := os.Stat(f); err != nil {
				made = append(made, f)
			}
		}
		mode, _ := edit.GetJSON(p.config, "deploymentMode")
		mode3p, _ := edit.GetJSON(p.config3p, "deploymentMode")
		applied, _ := edit.GetJSON(p.meta, "appliedId")
		k := p.key
		forget(desktopMode+k, desktopMode3p+k, desktopApplied+k, desktopMade+k, desktopLocale+k)
		stash(map[string]string{desktopStashed + k: "1", desktopMode + k: mode, desktopMode3p + k: mode3p,
			desktopApplied + k: applied, desktopMade + k: strings.Join(made, "\n")})
	}

	// the profile: the gateway's address and key set, Desktop's own
	// settings in it (the user's, from its window) kept, and those it had
	// when switched off put back (#2035)
	if b, _ := edit.Read(p.prof); len(bytes.TrimSpace(b)) == 0 {
		if err := os.MkdirAll(p.library, 0o755); err != nil {
			return err
		}
		was := []byte("{}\n")
		if kept := unstash(desktopProfile + p.key); kept != "" && desktopObjectBytes([]byte(kept)) {
			was = []byte(kept)
		} else if own := desktopOwnGatewayProfile(p); own != nil {
			was = own
		}
		if err := os.WriteFile(p.prof, was, 0o600); err != nil {
			return err
		}
	}
	kvs := []edit.KV{
		{Path: "inferenceProvider", Value: "gateway"},
		{Path: "inferenceGatewayBaseUrl", Value: gateway.URL()},
		{Path: "inferenceGatewayApiKey", Value: agentKey("claude-desktop")},
		{Path: "inferenceGatewayAuthScheme", Value: "bearer"},
	}
	// policies seeded once: the user may tighten them in Desktop
	if _, ok := edit.GetJSON(p.prof, "disableDeploymentModeChooser"); !ok {
		kvs = append(kvs, edit.KV{Path: "disableDeploymentModeChooser", Value: true})
	}
	if _, ok := edit.GetJSON(p.prof, "coworkEgressAllowedHosts"); !ok {
		kvs = append(kvs, edit.KV{Path: "coworkEgressAllowedHosts", Value: []string{"*"}})
	}
	if err := edit.SetJSON(p.prof, kvs...); err != nil {
		return err
	}
	if err := desktopSeedImport(p); err != nil {
		return err
	}
	if err := desktopSeedToolSearch(p); err != nil {
		return err
	}

	// a profile for magpie's gateway the user applied since magpie's
	// (one imported from another computer) stays applied, its connectors
	// with it; it gets a key the gateway takes. Switching on the first
	// time, one applied starts magpie's profile instead (#2035, above).
	theirs := wired && desktopAppliedGateway(p) != ""
	if theirs {
		if err := desktopKeyApplied(p); err != nil {
			return err
		}
	}

	// _meta.json: magpie's entry (where it was, if it was), applied
	if !wired {
		entry, _ := json.Marshal(map[string]string{"id": desktopProfileID, "name": "magpie"})
		entries = append(entries, entry)
		if err := edit.SetJSON(p.meta, edit.KV{Path: "entries", Value: entries}); err != nil {
			return err
		}
	}
	if !theirs {
		if err := edit.SetJSON(p.meta, edit.KV{Path: "appliedId", Value: desktopProfileID}); err != nil {
			return err
		}
	}

	desktopCarryLocale(p)
	if err := desktopCarryMCP(p); err != nil {
		return err
	}
	// a pick for a tier: the list its behavesAs goes in
	if err := desktopSyncModels(p); err != nil {
		return err
	}

	// the mode last: until here Desktop still starts as it did
	for _, f := range []string{p.config3p, p.config} {
		if err := edit.SetJSON(f, edit.KV{Path: "deploymentMode", Value: "3p"}); err != nil {
			return err
		}
	}
	return nil
}

// desktopImportOn is Desktop's own "Claude.ai data import" policy
// (claudeAiImport, a 3p-only key of Desktop 1.28929 and later) turned on,
// with its banner shown only when there is something to import.
var desktopImportOn = map[string]any{"enabled": true, "bannerBehavior": "detect"}

// desktopSeedImport lets Desktop on magpie bring in the sessions it had
// signed in with Anthropic. 3p mode runs in a userData of its own
// (Claude-3p), whose Code and Cowork sessions are keyed by its install id
// and a fixed org (claude-code-sessions/<ant-did>/00000000-0000-4000-8000-
// 000000000001), so the Code tab came up empty and the signed-in sessions,
// in Claude/claude-code-sessions/<account>/<org>, were out of sight until
// Desktop was switched back (lambdua on X: 切换账号，对话都能全都不见).
// Nothing is lost; Desktop only offers its importer (Settings → Import,
// "earlier Claude sessions on this computer": Claude/, other Claude*
// folders and ~/.claude/projects, copied with the originals left in place)
// when claudeAiImport.enabled is true, false by default (Desktop 2.7032's
// B9/VKi). It is seeded into magpie's profile once, where the profile has
// no claudeAiImport of its own: one the user set, or took away in
// Desktop's window since, stays theirs. Run on every switch on and from
// Sync, so a Desktop already on magpie gets it at magpie's next start.
// The mark is read, never unstashed: it outlives the check and a switch
// off, or the next Sync would write the policy back.
func desktopSeedImport(p desktopPaths) error {
	return desktopSeedOnce(p, desktopImport, "claudeAiImport", desktopImportOn)
}

// desktopSeedToolSearch turns on Desktop's own tool search policy
// (toolSearchEnabled, 3p only, off by default) in magpie's profile. Off,
// Desktop starts Claude Code with CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS=1
// (Desktop 2.7032's Ztr "off" in the session env), and Claude Code 2.1.312
// and 2.1.316 then keep tool search off whatever ENABLE_TOOL_SEARCH says
// (its oae: "standard" while experimental betas are off, unless an admin
// tier forces it), so every MCP tool's schema went whole on each request
// (bigcat1800 on Discord: Desktop 4.4212, 13 MCP servers, ~162K tokens of
// tools; a sandbox CLI with 60 MCP tools sent 83 tools, 205KB). On, with
// the gateway provider and a bundled Claude Code of 2.1.247 or later,
// Desktop gives the CLI ENABLE_TOOL_SEARCH=force as a managed setting
// ("host"): the same CLI sent 13 tools, MCP ones behind ToolSearch, 56KB,
// asking only tool-search-tool-2025-10-19 of the betas. The gateway
// carries deferred tools to every upstream (Tool.Deferred,
// withoutDeferredTools). Seeded once, as claudeAiImport is: a value the
// user set in Desktop's window, false too, or one they took away since,
// stays theirs.
func desktopSeedToolSearch(p desktopPaths) error {
	return desktopSeedOnce(p, desktopSearch, "toolSearchEnabled", true)
}

// desktopSeedOnce writes key in magpie's profile once, where the profile
// has none, and marks it in the stash under mark: once marked it is never
// written again, so the user's own value, or their taking it away, holds.
func desktopSeedOnce(p desktopPaths, mark, key string, value any) error {
	if stashShared()[mark+p.key] != "" {
		return nil
	}
	if desktopObject(p.prof) != nil {
		return nil
	}
	if _, ok := edit.GetJSON(p.prof, key); !ok {
		if err := edit.SetJSON(p.prof, edit.KV{Path: key, Value: value}); err != nil {
			return err
		}
	}
	stash(map[string]string{mark + p.key: "1"})
	return nil
}

// desktopServers is the mcpServers object of a claude_desktop_config.json,
// nil when it has none or can't be read as one.
func desktopServers(path string) map[string]json.RawMessage {
	raw, ok := edit.GetJSONRaw(path, "mcpServers")
	if !ok {
		return nil
	}
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &m) != nil {
		return nil
	}
	return m
}

// desktopCarryMCP gives Desktop's 3p mode the MCP servers the user added
// signed in with Anthropic (Settings → Developer). 3p mode reads its
// servers from Claude-3p's claude_desktop_config.json, a file of its own,
// so on magpie Desktop's connector list had none of the user's
// (aa86222 on Discord: connectors didn't sync). Each server Claude's file
// has and Claude-3p's doesn't is copied; one of the same name in Claude-3p
// (the user's, or the library's, which writes both) stays as it is. What
// was copied is kept in the stash, so off takes back each one still as it
// was copied. The connectors of a claude.ai account don't come along:
// Desktop reaches them through claude.ai with that account
// (/api/organizations/<org>/mcp/servers/…), and 3p mode has none.
func desktopCarryMCP(p desktopPaths) error {
	stash(map[string]string{desktopMCPSeen + p.key: "1"})
	own := desktopServers(p.mcpFrom)
	if p.mcpFrom == p.config3p || len(own) == 0 || desktopObject(p.config3p) != nil {
		return nil
	}
	if _, has := edit.GetJSONRaw(p.config3p, "mcpServers"); has && desktopServers(p.config3p) == nil {
		return nil // not an object: Desktop's, or the user's, left alone
	}
	have := desktopServers(p.config3p)
	carried := map[string]json.RawMessage{}
	if prev := stashShared()[desktopMCP+p.key]; prev != "" {
		json.Unmarshal([]byte(prev), &carried)
	}
	merged := map[string]json.RawMessage{}
	for k, v := range have {
		merged[k] = v
	}
	added := false
	for name, v := range own {
		if _, ok := merged[name]; ok {
			continue
		}
		merged[name], carried[name] = v, v
		added = true
	}
	if !added {
		return nil
	}
	if err := edit.SetJSON(p.config3p, edit.KV{Path: "mcpServers", Value: merged}); err != nil {
		return err
	}
	b, _ := json.Marshal(carried)
	stash(map[string]string{desktopMCP + p.key: string(b)})
	return nil
}

// desktopUncarryMCP takes the servers desktopCarryMCP copied out of
// Claude-3p again, each only while it is still as copied: one the user
// changed since is theirs. An mcpServers left empty goes with them.
func desktopUncarryMCP(p desktopPaths) error {
	prev := unstash(desktopMCP + p.key)
	var carried map[string]json.RawMessage
	if prev == "" || json.Unmarshal([]byte(prev), &carried) != nil || len(carried) == 0 {
		return nil
	}
	have := desktopServers(p.config3p)
	if have == nil {
		return nil
	}
	gone := false
	for name, v := range carried {
		if cur, ok := have[name]; ok && desktopSameJSON(cur, v) {
			delete(have, name)
			gone = true
		}
	}
	if !gone {
		return nil
	}
	if len(have) == 0 {
		return edit.DelJSON(p.config3p, "mcpServers")
	}
	return edit.SetJSON(p.config3p, edit.KV{Path: "mcpServers", Value: have})
}

func desktopSameJSON(a, b json.RawMessage) bool {
	var x, y any
	return json.Unmarshal(a, &x) == nil && json.Unmarshal(b, &y) == nil && reflect.DeepEqual(x, y)
}

// desktopLocaleName is a language Desktop takes for its "locale": the
// names of its catalogs (lit in Desktop 2.7032's main process,
// /^[a-z]{2,3}(-[A-Za-z0-9]{2,8})*\.json$/).
var desktopLocaleName = regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)

// desktopLocaleFrom is the config.json, of every set's signed-in
// userData, that names a language: the first set's (where Desktop runs)
// before the others, so a packaged Desktop's language reaches the real
// Claude-3p too, which 2.31226 reads. "" for none.
func desktopLocaleFrom(ps []desktopPaths) string {
	for _, p := range ps {
		if loc, ok := edit.GetJSON(p.app, "locale"); ok && desktopLocaleName.MatchString(loc) {
			return p.app
		}
	}
	return ""
}

// desktopCarryLocale gives Desktop's 3p mode the language the user picked
// signed in with Anthropic. Claude-3p is a userData of its own, so its
// config.json starts with no "locale" and Desktop falls back to the
// system's languages: the user's Chinese was gone once magpie turned 3p
// on (memory and Osal on Discord). Only "locale" is written, only into a
// config.json that is missing or a JSON object without one: a language
// set in 3p mode, by Desktop or by hand, is the user's and stays. What
// can't be read is left alone. The value written is stashed, so switching
// off can take a config.json magpie made for it away again.
//
// It holds only for a language Desktop's 3p window has. That window is
// its bundled UI (Resources/ion-dist), with 11 catalogs and no Chinese up
// to 4.23118.0 at least; on every start it asks the main process for its
// own pick (requestLocaleChange: its localStorage, else navigator.languages
// mapped onto those 11, else en-US), and the main process writes that into
// Claude-3p/config.json. So a Chinese "locale" there, carried or set by
// hand, is "en-US" again after each start, by Desktop and not by magpie
// (Dx25 on Discord, Windows), and nothing magpie writes can keep it.
func desktopCarryLocale(p desktopPaths) {
	if p.app == "" || p.app3p == "" || p.app == p.app3p {
		return
	}
	loc, ok := edit.GetJSON(p.app, "locale")
	if !ok || !desktopLocaleName.MatchString(loc) {
		return
	}
	if desktopObject(p.app3p) != nil {
		return
	}
	if _, has := edit.GetJSON(p.app3p, "locale"); has {
		return
	}
	if err := os.MkdirAll(p.dir3p, 0o755); err != nil {
		return
	}
	if edit.SetJSON(p.app3p, edit.KV{Path: "locale", Value: loc}) == nil {
		stash(map[string]string{desktopLocale + p.key: loc})
	}
}

// desktopOff puts Claude Desktop back as it was before magpie: each
// deploymentMode as it was (gone where there was none), the profile it had
// applied, magpie's profile and entry removed, and the files and folders
// magpie made gone again if nothing else went into them.
func desktopOff(p desktopPaths) error {
	entries, _, err := desktopEntries(p.meta)
	if err != nil {
		return err
	}
	k := p.key
	stashed := unstash(desktopStashed+k) != ""
	mode, mode3p, applied := unstash(desktopMode+k), unstash(desktopMode3p+k), unstash(desktopApplied+k)
	var made []string
	if m := unstash(desktopMade + k); m != "" {
		made = strings.Split(m, "\n")
	}
	if !stashed && !slices.ContainsFunc(entries, desktopOurs) {
		return nil
	}

	for f, was := range map[string]string{p.config: mode, p.config3p: mode3p} {
		if cur, _ := edit.GetJSON(f, "deploymentMode"); cur != "3p" {
			continue // changed since, by Desktop or the user: theirs
		}
		if was != "" {
			err = edit.SetJSON(f, edit.KV{Path: "deploymentMode", Value: was})
		} else {
			err = edit.DelJSON(f, "deploymentMode")
		}
		if err != nil {
			return err
		}
	}

	if slices.ContainsFunc(entries, desktopOurs) {
		entries = slices.DeleteFunc(entries, desktopOurs)
		if len(entries) == 0 && slices.Contains(made, p.meta) {
			err = edit.DelJSON(p.meta, "entries")
		} else {
			if entries == nil {
				entries = []json.RawMessage{}
			}
			err = edit.SetJSON(p.meta, edit.KV{Path: "entries", Value: entries})
		}
		if err != nil {
			return err
		}
	}
	if id, _ := edit.GetJSON(p.meta, "appliedId"); id == desktopProfileID {
		// the profile applied before, if it is still there, else the first
		// one left, else none
		next := ""
		if applied != "" && applied != desktopProfileID && slices.ContainsFunc(entries, func(e json.RawMessage) bool { return desktopEntryID(e) == applied }) {
			next = applied
		} else if len(entries) > 0 {
			next = desktopEntryID(entries[0])
		}
		if next != "" {
			err = edit.SetJSON(p.meta, edit.KV{Path: "appliedId", Value: next})
		} else {
			err = edit.DelJSON(p.meta, "appliedId")
		}
		if err != nil {
			return err
		}
	}
	// the profile goes, and what is in it is kept for the next on: the
	// settings the user made in it are theirs (#2035)
	if b, err := os.ReadFile(p.prof); err == nil && desktopObjectBytes(b) && !desktopEmpty(b) {
		stash(map[string]string{desktopProfile + k: string(b)})
	}
	if err := os.Remove(p.prof); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	if err := desktopUncarryMCP(p); err != nil {
		return err
	}

	// Claude-3p's config.json magpie made only to carry the language:
	// gone again when it still holds nothing but that language
	if loc := unstash(desktopLocale + k); loc != "" && slices.Contains(made, p.app3p) {
		if b, err := os.ReadFile(p.app3p); err == nil {
			var m map[string]json.RawMessage
			var only string
			if json.Unmarshal(b, &m) == nil && len(m) == 1 && json.Unmarshal(m["locale"], &only) == nil && only == loc {
				os.Remove(p.app3p)
			}
		}
	}

	// what magpie made, deepest first: a file only if it is an empty
	// object now, a folder only if it is empty
	for _, f := range slices.Backward(made) {
		st, err := os.Stat(f)
		if err != nil {
			continue
		}
		if st.IsDir() {
			os.Remove(f)
			continue
		}
		if b, _ := os.ReadFile(f); desktopEmpty(b) {
			os.Remove(f)
		}
	}
	return nil
}

// desktopOwnGatewayProfile is the profile the user made themselves in
// Desktop's window for magpie's gateway, the one applied before magpie
// applied its own: its settings (chat tab, default effort, allowed hosts
// and whatever Desktop adds) start magpie's profile, so switching on
// doesn't trade them for magpie's defaults (#2035). A profile for another
// gateway, Bedrock or Vertex is not one: its models and settings are for
// somewhere else. nil when there is none.
func desktopOwnGatewayProfile(p desktopPaths) []byte {
	id, _ := edit.GetJSON(p.meta, "appliedId")
	if id == desktopProfileID {
		id = unstash(desktopApplied + p.key)
	}
	if id == "" || id == desktopProfileID || filepath.Base(id) != id || strings.HasPrefix(id, ".") {
		return nil
	}
	b, err := os.ReadFile(filepath.Join(p.library, id+".json"))
	if err != nil || !desktopObjectBytes(b) {
		return nil
	}
	var prof struct {
		Provider string `json:"inferenceProvider"`
		BaseURL  string `json:"inferenceGatewayBaseUrl"`
	}
	if json.Unmarshal(b, &prof) != nil || prof.Provider != "gateway" || !desktopIsOurGateway(prof.BaseURL) {
		return nil
	}
	return b
}

// desktopIsOurGateway: u is magpie's gateway, by its port on this machine
// (127.0.0.1, localhost or ::1, with or without /v1).
func desktopIsOurGateway(u string) bool {
	a, err := url.Parse(strings.TrimSpace(u))
	if err != nil || a.Port() == "" {
		return false
	}
	ours, err := url.Parse(gateway.URL())
	if err != nil || a.Port() != ours.Port() {
		return false
	}
	h := a.Hostname()
	if h == ours.Hostname() || h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// desktopObjectBytes: b is a JSON object.
func desktopObjectBytes(b []byte) bool {
	var m map[string]json.RawMessage
	return json.Unmarshal(b, &m) == nil && m != nil
}

func desktopEmpty(b []byte) bool {
	var m map[string]json.RawMessage
	return json.Unmarshal(b, &m) == nil && len(m) == 0
}

// what claudedesktop says after a change (notice.go)
var (
	noticeClaudedesktop1 = newNotice("Claude Desktop reads this at start-up — quit and reopen it to run on magpie (Code and Cowork, no Anthropic sign-in).")
	noticeClaudedesktop2 = newNotice("Claude Desktop reads this at start-up — quit and reopen it to sign in with Anthropic again.")
	// Desktop's 3p mode runs in a userData of its own (Claude-3p, chosen in
	// its main process before the window opens), so the chats, projects and
	// Code sessions of the Anthropic sign-in in Claude/ aren't shown there.
	// magpie never touches Claude/'s data; off brings them back
	// (xgddm88 on X: 之前的工程和会话记录全没了).
	noticeClaudedesktop3 = newNotice("Chats and projects from your Anthropic sign-in stay in Desktop's own profile, not shown on magpie; nothing is deleted, and turning this off brings them back.")
	noticeClaudedesktop4 = newNotice("Your chats and projects from before are back there.")
	// the isolated Desktop (#5671): magpie's is its own app, and the
	// user's Claude.app stays signed in with Anthropic
	noticeClaudedesktopIsolated    = newNotice("Open Claude Magpie from Applications to use Claude Desktop on magpie. Your own Claude Desktop stays signed in with Anthropic.")
	noticeClaudedesktopIsolatedOff = newNotice("Quit Claude Magpie if it is open: it no longer runs on magpie.")
)
