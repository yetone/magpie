package agent

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	satomic "sync/atomic"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/codexcat"
	"github.com/yetone/magpie/internal/codexrelay"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/lastgood"
	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/sessions"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

// Codex talks the OpenAI Responses API. Signed in to ChatGPT, it is
// routed through magpie with `openai_base_url` alone: Codex keeps its
// built-in OpenAI provider and sign-in, so its own models, its threads
// (listed per provider) and the Codex app's model picker stay as they are,
// and magpie's gateway passes its own models through to OpenAI while it
// answers the rest — the model list included, so magpie's models join
// OpenAI's in /model. Not signed in, the built-in provider can't run, and
// magpie is a provider of its own: a [model_providers.magpie] table,
// `model_provider = "magpie"`, and a model catalog file for /model. So it
// is too for Codex signed in with an API key, often one a relay issued:
// Codex then never asks for the model list (its models manager skips the
// fetch for an API key), so its picker was the models it was built with
// and the one set, magpie's others missing, and the built-in ones went to
// OpenAI with that key (#322). So it is too for a ChatGPT account the
// Codex app holds, out of its allowance with no credits to go on with
// (provider.CodexUsedUp), which it won't send anything for, whoever serves
// the model, and when the user asks for it (the sign-in field's api): under
// the takeover (codex.out) the Codex app is then in its API state rather
// than signed in to ChatGPT, its chats, projects and remote control gone
// with it till the reset; for api, with room, it keeps its account
// (requires_openai_auth, openaiAuth). A window at 100% alone
// is not that: credits carry the account on, the app keeps sending, and
// Codex stays signed in beside magpie's models. The base URL is set then
// too: a thread started on the built-in provider is opened on it again, and
// a magpie model picked in it would otherwise go to the ChatGPT backend.
//
// The [model_providers.magpie] table stays once written. A thread keeps the
// provider it was started on, and one started on magpie can't be opened
// again without the table ("Model provider `magpie` not found"), whichever
// way Codex is routed now. Codex looks model_provider up in the tables as
// it loads config.toml (codex-rs config: "Model provider `{id}` not
// found"), so `model_provider = "magpie"` without the table keeps it from
// loading its config at all, a new thread too: magpie never writes the one
// without the other, takes model_provider out whenever it steps out as the
// provider (disconnected, unwired, joined beside the sign-in), and Sync
// writes the table back when something else took it.

// codexStandIn is the model Codex is set to use, when magpie is its
// provider, for a model it names that magpie doesn't serve. Codex asks for
// some by a name of OpenAI's whatever its provider: auto-review of an
// approval goes out as "codex-auto-review" (the guardian's reviewer model,
// "independent of current login"), which magpie answered 404 "magpie knows
// no model", and Codex reported the review as failed. Codex itself reviews
// with the turn's model when its catalog lacks the reviewer; so does magpie.
// Signed in, Codex's own names go on to OpenAI (codexUpstream) instead.
func codexStandIn(path string) string {
	if p, _ := edit.GetTOMLTop(path, "model_provider"); p != magpieID {
		return ""
	}
	m, _ := edit.GetTOMLTop(path, "model")
	return m
}

func codex(home string) *Agent { return codexIn(here(home)) }

// The two notices magpie's multi-agent V1 setting (#141) can carry: Codex's
// own features.multi_agent_v2 is on in the config magpie routes and takes the
// V1 over, or magpie couldn't read that config to tell. magpie writes nothing
// here — the setting is the user's own to turn off.
var (
	codexV1ConflictNotice  = newNotice("Codex's own multi_agent_v2 is on in the config magpie routes, which takes over the multi-agent V1 magpie's models ask for; turn it off in Codex's config to run subagents on magpie's models.")
	codexV1ReadErrorNotice = newNotice("Codex's multi-agent V1 is on, but magpie couldn't read Codex's config to check its own multi_agent_v2.")
)

// multiAgentV2OnIn reads one [features] table for Codex's own multi_agent_v2,
// read-only: the bare multi_agent_v2 boolean, or enabled in the
// [features.multi_agent_v2] table, spelled as a table, an inline object or a
// dotted key. set reports whether magpie recognizes the key as on or off. A
// table whose enabled is left out, or any other value (a string "true"), is
// not recognized, so magpie reads it as nothing and warns on nothing — Codex
// itself rejects such a config at load (multi_agent_v2 is a bool or a table
// there), so this is magpie's read of an invalid file, not Codex's default.
func multiAgentV2OnIn(features map[string]any) (on, set bool) {
	switch v := features["multi_agent_v2"].(type) {
	case bool:
		return v, true
	case map[string]any:
		enabled, ok := v["enabled"].(bool)
		return enabled, ok
	}
	return false, false
}

// codexMultiAgentV2On reports whether a Codex config explicitly turns its own
// features.multi_agent_v2 on: the bare multi_agent_v2 = true key, or enabled =
// true in the [features.multi_agent_v2] table (a table, an inline object or a
// dotted key), read at the top level and, on the legacy in-file profile shape,
// in the profile the config selects. Codex 0.133.0 applied that overlay itself
// (core/config/mod.rs: Features::from_sources with base = [features] and
// profile = [profiles.<name>.features], the profile applied on top), and
// magpie's own Check already reads profiles.<profile> the same way; that legacy
// shape is what this mirrors. From 0.134 Codex dropped in-file profiles: an
// in-file `profile` key is an error and `--profile` selects a separate
// {profile}.config.toml layer, so this is not a claim about which profile a
// modern Codex is running under — only about the same in-file shape magpie's
// Check reads. The whole file is decoded (codexTopOf, as codexEnableEfforts
// reads it). A config magpie can't read is an error, never taken for off; no
// config file has no key, nothing Codex turns V2 on with.
func codexMultiAgentV2On(path string) (bool, error) {
	doc, err := codexTopOf(path)
	if err != nil {
		return false, err
	}
	top, _ := doc.Features.(map[string]any)
	on, _ := multiAgentV2OnIn(top)
	// the legacy in-file profile's features override the top level's, as Codex
	// 0.133.0 applied them (base then profile) and as magpie's Check reads
	// profiles.<profile>: take the profile's value when it sets one, else keep
	// the top level's. Only the profile the config names counts.
	if profile, _ := doc.Profile.(string); profile != "" {
		profiles, _ := doc.Profiles.(map[string]any)
		p, _ := profiles[profile].(map[string]any)
		pf, _ := p["features"].(map[string]any)
		if pon, set := multiAgentV2OnIn(pf); set {
			on = pon
		}
	}
	return on, nil
}

// codexTop is the tables of Codex's config magpie's looks decode the file
// for. A config trusting every project Codex was opened in holds thousands
// of [projects."…"] tables (the owner's 10,932, in 1.5 MB), and decoding
// those again for each look was 3 MB a look, over 20 MB a catalog sync.
type codexTop struct {
	Features       any `toml:"features"`
	ModelProviders any `toml:"model_providers"`
	Desktop        any `toml:"desktop"`
	Profile        any `toml:"profile"`
	Profiles       any `toml:"profiles"`
	Analytics      any `toml:"analytics"`
}

// codexTopOf is the config at path as codexTop, decoded again only once the
// file changes; it is looked at each time (filememo.ReadNow), as what is
// written is made from it. What it returns is shared: nothing in it is
// changed. A missing file is an empty config.
func codexTopOf(path string) (*codexTop, error) {
	doc, err := filememo.ReadNow("codex top", path, func(b []byte) (*codexTop, error) {
		var doc codexTop
		if err := toml.Unmarshal(edit.TOMLWithout(edit.TrimBOM(b), "projects"), &doc); err != nil {
			return nil, err
		}
		return &doc, nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return &codexTop{}, nil
	}
	return doc, err
}

// codexSubAgentsV1Warning is the notice for the config Codex is routed
// through (path, routed): settings.CodexAgentsV1 is on and Codex's own
// multi_agent_v2 is explicitly on there, so Codex's V2 wins over the V1
// magpie's own models ask for and a subagent on one can't read its task.
// "" when the setting is off, Codex isn't routed through magpie here, or its
// own multi_agent_v2 is off; a config magpie can't read is told, not taken
// for off.
func codexSubAgentsV1Warning(path string, routed bool) string {
	if !codexcat.V1() || !routed {
		return ""
	}
	on, err := codexMultiAgentV2On(path)
	if err != nil {
		return codexV1ReadErrorNotice.String()
	}
	if !on {
		return ""
	}
	return codexV1ConflictNotice.String()
}

// CodexSubAgentsV1Warning is codexSubAgentsV1Warning for the Codex config
// here, as the Settings row asks it (the page can't reach the config itself).
// "" when there is no Codex, or no conflict.
func CodexSubAgentsV1Warning() string {
	a, err := Find("codex")
	if err != nil || a.Routed == nil {
		return ""
	}
	return codexSubAgentsV1Warning(a.Path, a.Routed())
}

// codexIn is Codex as it lives at a place: this machine's home, or a WSL
// distro's (see wsl.go).
// codexRunning is whether a Codex runs here, whose list a change reaches
// only once it restarts; a var so tests can say.
var codexRunning = func() bool { return Running(`(^|/)codex( |$)`) }

// codexEfforts are the effort control's choices for a model among ms:
// Default first, with the level Codex takes for it when none is set, as
// the catalog entry it reads says it, then the model's levels.
func codexEfforts(ms []catalog.Model, model string) []Option {
	e := catalog.Efforts(ms, model)
	if len(e) == 0 {
		return static("low", "medium", "high", "xhigh")
	}
	d := Option{Value: "", Note: "Codex's default for the model", Takes: codexcat.TakesEffort(ms, model)}
	if d.Takes != "" {
		d.Note += ": " + d.Takes
	}
	return append([]Option{d}, static(e...)...)
}

// catalogParses is whether b is a model list magpie writes for Codex, the
// shape Keep reads of the file there.
func catalogParses(b []byte) bool {
	var v struct {
		Models []map[string]any `json:"models"`
	}
	return json.Unmarshal(b, &v) == nil
}

// writeOwnList writes the model list magpie keeps for Codex at path. A file
// there that doesn't parse is copied aside first: Keep (#1450) holds
// nothing of a file it can't read, so writing the new list over one would
// end the keys the user added by hand with no copy (#2263's rule for
// magpie's state files, which this file had missed). An error stops the
// write.
func writeOwnList(path string, b []byte) error {
	if err := lastgood.KeepUnparsed(path, catalogParses); err != nil {
		return err
	}
	return edit.WriteAtomic(path, b)
}

func codexIn(at place) *Agent {
	dir := at.codexHome()
	path := filepath.Join(dir, "config.toml")
	catalogPath := filepath.Join(dir, "magpie-models.json")
	get := func(k string) string { v, _ := edit.GetTOMLTop(path, k); return v }
	// this machine's Codex reaches the gateway at the address its config
	// gives, when that is magpie's on another host: the Codex app on
	// Windows with its agent in WSL reads Windows' config.toml from WSL,
	// where 127.0.0.1 is the distro's own, so the user points it at
	// Windows as WSL sees it, or a forward of theirs (#816); magpie keeps
	// that address, checks against it and writes it again
	if at.spell == nil && at.base == nil {
		at.base = func() string { return codexKeptGateway(path) }
	}
	// the catalog as the config names it: relative to config.toml on
	// Windows, where a C:\ path is no file to the same Codex reading it
	// from WSL (#816); Codex reads a relative one from its home
	catalogRef := at.native(catalogPath)
	if at.spell == nil && runtime.GOOS == "windows" {
		catalogRef = filepath.Base(catalogPath)
	}
	// ownCatalog: the config names magpie's catalog, by any spelling
	ownCatalog := func(c string) bool {
		if c == "" {
			return false
		}
		if c == catalogRef || c == at.native(catalogPath) {
			return true
		}
		return at.spell == nil && !filepath.IsAbs(c) && filepath.Clean(filepath.Join(dir, c)) == filepath.Clean(catalogPath)
	}
	// ownList is magpie's model list for Codex as written to catalogPath:
	// what the user added by hand to the list there (a key magpie doesn't
	// write, supports_reasoning_summaries on a model magpie gave none) is
	// kept, not written over by the next model switch or sync (#1450).
	// Each model's default effort is the one this config starts new
	// sessions at, or the model's nearest (codexcat.ModelDefault), which
	// Codex takes on a switch to it. The models Codex used are among the
	// first 100, which is all the Codex app reads (codexcat.Fit).
	list := func() []byte {
		return codexcat.CatalogUsed(magpieModels("codex"), get("model_reasoning_effort"), usage.CodexUsed())
	}
	ownList := func() []byte {
		cur, _ := edit.Read(catalogPath)
		return codexcat.Keep(cur, list())
	}
	// listGone: magpie's list is known not to be there; one that can't be
	// looked at is not taken for gone
	listGone := func() bool {
		_, err := os.Stat(catalogPath)
		return errors.Is(err, fs.ErrNotExist)
	}
	// unnameGoneList takes model_catalog_json out of the config where it
	// names magpie's list and the list is gone, so Codex can load its
	// config again
	unnameGoneList := func() error {
		if !ownCatalog(get("model_catalog_json")) || !listGone() {
			return nil
		}
		return edit.DelTOMLTop(path, "model_catalog_json")
	}
	// theirListGone: a model_catalog_json of the user's names no file, as
	// Codex reads it (relative to its home). In a WSL distro its spelling
	// is the distro's, which this side can't look at: not taken for gone.
	theirListGone := func(c string) bool {
		if at.spell != nil {
			return false
		}
		if !filepath.IsAbs(c) {
			c = filepath.Join(dir, c)
		}
		_, err := os.Stat(c)
		return errors.Is(err, fs.ErrNotExist)
	}
	// dropList takes the list away as magpie steps out as Codex's
	// provider; one holding what the user added stays, which the config
	// no longer names, for ownList to keep when magpie is back
	//
	// The config never names it once it is gone: Codex reads the file as
	// it loads its config, and with it missing loads none at all ("No such
	// file or directory (os error 2)"), the CLI and the ChatGPT app both
	// (zwaydot on X: Codex didn't open after magpie's Disconnect). The
	// key was taken out only while model_provider was still magpie's, and
	// the file went whatever the provider: one moved off magpie by the app,
	// another tool or a hand edit left the key naming a removed file.
	dropList := func() {
		defer unnameGoneList()
		cur, err := edit.Read(catalogPath)
		if err != nil {
			return
		}
		b := list()
		if string(codexcat.Keep(cur, b)) == string(b) {
			// one that doesn't parse is copied aside before it goes: Keep
			// held nothing of it, so removing it outright would end the
			// user's own keys with no copy
			if err := lastgood.KeepUnparsed(catalogPath, catalogParses); err != nil {
				return
			}
			os.Remove(catalogPath)
		}
	}
	asProvider := func() bool { return get("model_provider") == magpieID }
	viaBase := func() bool {
		u := get("openai_base_url")
		return isCodexGatewayOn(u, at.host()) || strings.TrimSuffix(u, "/") == at.codexURL()
	}
	routed := func() bool { return asProvider() || viaBase() }
	// joined: connected by Join, on a model of Codex's own (its last pick)
	// with magpie's beside it, by the base URL
	joined := func() bool { return viaBase() && stashShared()[at.key("codex.joined")] == "1" }
	// beside: set on one of magpie's models beside the ChatGPT sign-in, by
	// the base URL, Codex's own models in its list too: one of them written
	// in by Codex (its /model, or a Codex app still running on its old
	// pick) leaves it connected, its config still on magpie's gateway
	// (#940: it read as not connected), the change told as drift. One
	// picked on the Agents page still takes magpie out (set).
	beside := func() bool { return viaBase() && stashShared()[at.key("codex.beside")] == "1" }
	// chatGPT is whether Codex is signed in to ChatGPT. With account
	// discovery off (#1535) auth.json stays unread, and Codex is taken to be
	// as magpie last wired it, beside a ChatGPT sign-in or not, so turning
	// discovery off never rewires Codex by itself.
	//
	// A Codex that reaches the gateway from another machine (a WSL distro
	// under NAT, or MAGPIE_ADDR pinned beyond loopback) is wired as one
	// with no ChatGPT sign-in: beside its sign-in it would send the gateway
	// its ChatGPT token, and from there the gateway takes a named key
	// alone, so every request was a 401 (#2145, lazydao). With magpie as
	// its provider it sends the key magpie gives it (gwKey); its own models
	// still go straight to OpenAI on its sign-in.
	chatGPT := func() bool {
		if onAnotherMachine(at.gw()) {
			return false
		}
		if settings.AccountDiscoveryOff() {
			return joined() || beside()
		}
		// a read that says nothing (auth.json caught while Codex rewrites
		// it) leaves Codex as magpie last wired it: beside its sign-in, or
		// magpie its provider while the Codex app holds the account
		// (codex.out), which Sync puts back once it has room
		signedIn, known := codexChatGPTKnown(dir)
		if !known {
			return joined() || beside() || stashShared()[at.key("codex.out")] == "1"
		}
		return signedIn
	}
	// magpie is in Codex's config: on one of magpie's models, or joined;
	// or, routed, on the model of the group magpie set it to as Codex
	// spells it (gpt-6.1-sol for group/auto-gpt-6-1-sol, #750), which the
	// gateway takes as that group
	wired := func() bool {
		m := get("model")
		return isMagpie(m) || joined() || beside() || routed() && setAsGroup(cmp.Or(at.id, "codex"), "model", m)
	}
	// deskList is the list magpie hands Codex beside its ChatGPT sign-in,
	// written to a file of its own (deskPath) and named by
	// model_catalog_json, where the Codex app is installed: the app's
	// picker shows only the models of Codex's list a flag of OpenAI's
	// names while Codex is on its built-in provider, magpie's never among
	// them, unless Codex reads its list from a file (laddieyao on
	// Discord: GPT alone in the app, magpie's in the CLI). It is not
	// magpie's catalog as a provider (catalogPath, ownCatalog): Codex is
	// on its built-in provider, with its own models from the ChatGPT
	// backend's list as the backend sent them (CodexDesktopList).
	deskPath := filepath.Join(dir, "magpie-desktop-models.json")
	deskRef := at.native(deskPath)
	if at.spell == nil && runtime.GOOS == "windows" {
		deskRef = filepath.Base(deskPath)
	}
	deskCatalog := func(c string) bool {
		if c == "" {
			return false
		}
		if c == deskRef || c == at.native(deskPath) {
			return true
		}
		return at.spell == nil && !filepath.IsAbs(c) && filepath.Clean(filepath.Join(dir, c)) == filepath.Clean(deskPath)
	}
	deskGone := func() bool {
		_, err := os.Stat(deskPath)
		return errors.Is(err, fs.ErrNotExist)
	}
	// deskWanted: magpie is connected beside the ChatGPT sign-in (joined,
	// or on one of magpie's models), not as Codex's provider, on this
	// machine's Codex, and the Codex app reads this config. The CLI alone
	// keeps asking the gateway for the list, which follows every change at
	// once; a Codex here for account failover alone has no models of
	// magpie's to show.
	deskWanted := func() bool {
		if at.spell != nil || asProvider() || !viaBase() || !(joined() || beside()) || !chatGPT() {
			return false
		}
		for _, r := range codexReaders(at) {
			if r.Kind == "app" {
				return true
			}
		}
		return false
	}
	// deskList writes the list for the Codex app again, or takes it out
	// where it isn't wanted: the key, and the file. A list not known now
	// (the backend not asked yet, or not answering) leaves the file as it
	// was, never an empty one; a key naming a file that is gone goes, as
	// Codex loads no config then. A model_catalog_json of the user's own
	// stands: theirs is what Codex reads.
	deskList := func() error {
		named := deskCatalog(get("model_catalog_json"))
		if !deskWanted() {
			if named {
				if err := edit.DelTOMLTop(path, "model_catalog_json"); err != nil {
					return err
				}
			}
			if err := os.Remove(deskPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			return nil
		}
		if c := get("model_catalog_json"); c != "" && !named {
			if err := os.Remove(deskPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			return nil
		}
		var b []byte
		ok := false
		if CodexDesktopList != nil {
			b, ok = CodexDesktopList()
		}
		if !ok {
			if named && deskGone() {
				return edit.DelTOMLTop(path, "model_catalog_json")
			}
			return nil
		}
		if cur, err := edit.Read(deskPath); err != nil || string(cur) != string(b) {
			if err := edit.WriteAtomic(deskPath, b); err != nil {
				return err
			}
		}
		if !named {
			return edit.SetTOMLTop(path, edit.KV{Path: "model_catalog_json", Value: deskRef})
		}
		return nil
	}
	// read only (each model's efforts, on every poll of the effort
	// field): the list kept, not a copy of it each time, 5 MB a minute on
	// a live magpie
	models := func() []catalog.Model {
		switch {
		case asProvider():
			return sharedModels("codex")
		case viaBase():
			own, more := catalog.CodexShared(), sharedModels("codex")
			return append(append(make([]catalog.Model, 0, len(own)+len(more)), own...), more...)
		}
		return catalog.CodexShared()
	}
	// relist hands Codex its models again after the effort new sessions
	// start at changed, which their default efforts follow: magpie's
	// catalog written again, or the list the gateway hands Codex beside
	// its sign-in marked stale (its tag carries the effort)
	relist := func() error {
		switch {
		case asProvider() && ownCatalog(get("model_catalog_json")):
			b := ownList()
			if cur, _ := edit.Read(catalogPath); string(cur) != string(b) {
				return writeOwnList(catalogPath, b)
			}
		case viaBase():
			if err := codexStaleCache(filepath.Join(dir, "models_cache.json"), provider.CodexListTag()); err != nil {
				return err
			}
		}
		return deskList()
	}
	var dropSubEffort func() error
	// keep the effort valid for the model: one set that the model doesn't
	// take becomes the model's nearest (xhigh on a group of high and max
	// becomes max, codexcat.ModelDefault), not the middle of the road. One
	// left unset stays unset — Codex then takes the model's own default,
	// which is what Default on the effort control asked for (lgtm: Default
	// turned into medium on its own). Routed, the Codex app offers
	// magpie's models' efforts too (#310).
	settle := func() error {
		if routed() {
			if err := codexEnableEfforts(path, magpieModels("codex")); err != nil {
				return err
			}
		}
		ms := models()
		model, effort := get("model"), get("model_reasoning_effort")
		if e := catalog.Efforts(ms, model); len(e) > 0 && effort != "" && !contains(e, effort) {
			to := codexcat.ModelDefault(e, effort)
			if err := edit.SetTOMLTop(path, edit.KV{Path: "model_reasoning_effort", Value: to}); err != nil {
				return err
			}
			// magpie's own change, which Disconnect undoes while it stands
			stash(map[string]string{at.key("codex.effort_set"): to})
			if err := relist(); err != nil {
				return err
			}
		}
		// the Codex app's list as magpie is wired now, or taken out
		if err := deskList(); err != nil {
			return err
		}
		return dropSubEffort()
	}
	// magpie as a provider Codex can name; its threads started on magpie do
	// Codex's background compaction, as Settings has it (#2490): written
	// where Codex is routed through magpie, taken back where it isn't
	asyncCompaction := func(routed bool) error {
		// a compact limit tune wrote here before goes to Long
		// conversations, the one window magpie keeps (7eleven)
		if at.id == "" && at.spell == nil {
			tuneMigrateCodex(at.home)
		}
		s := settings.Load()
		on := s.CodexAsyncCompact && routed
		return codexAsyncSync(path, at.key("codex.async"), routed, s.CodexAsyncCompact, s.CodexAsyncCompactPercent, on && codexAsyncTableOK(at))
	}
	// keptBearerToken is the table's bearer token when it is one the
	// gateway takes: a caller key of the gateway's own the user put in
	// magpie's token's place goes through magpie all the same, so theirs
	// stays, as a sync keeps one (keyHolds); only a value the gateway would
	// refuse is wired over (#2545). The sharing key magpie made is magpie's
	// own, however much Authenticate takes it, so it never counts as theirs
	// (Default): sharing going off brings gateway.Token back
	keptBearerToken := func(table string) string {
		tok := at.gwKey()
		if t, err := edit.GetTOMLTable(path, table); err == nil {
			if v := t["experimental_bearer_token"]; v != tok && keyHolds(v, tok) && !access.Default(v) {
				return v
			}
		}
		return tok
	}
	// openaiAuth: whether magpie's provider table says
	// requires_openai_auth, and whether that is known. Without it Codex's
	// app-server answers account/read and getAuthStatus with no account and
	// no token for a provider that is magpie, and the Codex app (ChatGPT.app
	// 26.1021) switches to an identity of no account: its ChatGPT mode's
	// chats and projects ("Work") are gone, as are Chrome control and cloud
	// tasks, while magpie's models still answer (Qyburn3 on X). With it the
	// app keeps the account, and Codex still sends magpie's key
	// (experimental_bearer_token) to magpie, no ChatGPT-Account-ID, its
	// tools the same (image_gen kept): tried with ChatGPT.app 26.1021 and
	// its codex 0.164.0-alpha.11 against a capturing server.
	//
	// Only for a ChatGPT sign-in with room. One the Codex app holds (used
	// up, codexUsedUp) would have the app read its rate limit again and
	// grey Send for magpie's models too, which is what codex.out exists to
	// get past (#540, c54478904). An API key alone has no account to keep,
	// and Codex drops its image tool for a provider requiring OpenAI auth
	// (uses_openai_actor_authorization); no sign-in at all would have the
	// app ask for one. A sign-in read that says nothing leaves the table
	// as it is.
	//
	// requires_openai_auth = false is never magpie's (it writes true or
	// nothing): the user wrote it, to have the Codex app on magpie with no
	// ChatGPT account of its own (蛋仔 on Discord), and it stays.
	ownNoAuth := func() bool {
		t, _ := edit.GetTOMLTable(path, "model_providers."+magpieID)
		return t["requires_openai_auth"] == "false"
	}
	openaiAuth := func() (want, known bool) {
		if ownNoAuth() {
			return false, false
		}
		signedIn, known := codexChatGPTKnown(dir)
		if !known {
			return false, false
		}
		return signedIn && !codexUsedUp(), true
	}
	putProvider := func() error {
		kvs := []edit.KV{
			{Path: "name", Value: "magpie"},
			{Path: "base_url", Value: at.v1()},
			{Path: "wire_api", Value: "responses"},
			{Path: "experimental_bearer_token", Value: keptBearerToken("model_providers." + magpieID)},
			// Codex offers its built-in image tool (image_gen.imagegen) and
			// the other OpenAI-provider tools only to a provider it reads as
			// the OpenAI actor: one that does not require OpenAI auth but
			// carries x-openai-actor-authorization. Without this header the
			// model is never given the tool, so a Codex on magpie can't draw
			// (the request that names model/draws never happens at all).
			// uses_openai_actor_authorization() is exactly this check.
			// An Inline table: a config that holds the headers as their own
			// [model_providers.magpie.http_headers] table, or as dotted keys,
			// gets the header set there, the user's others kept; written
			// inline beside that table it defined http_headers twice and
			// every model switch failed (wztlink1013 on Discord).
			{Path: "http_headers", Value: edit.Inline{{Path: "x-openai-actor-authorization", Value: "magpie"}}},
		}
		// the table's capabilities, the user's or the background
		// compaction's, aren't lost as it is written again
		if caps := codexProviderCaps(path); caps != nil {
			kvs = append(kvs, edit.KV{Path: "capabilities", Value: caps})
		}
		want, known := openaiAuth()
		if want {
			kvs = append(kvs, edit.KV{Path: "requires_openai_auth", Value: true})
		} else if ownNoAuth() {
			kvs = append(kvs, edit.KV{Path: "requires_openai_auth", Value: false})
		}
		if err := edit.SetTOMLTable(path, "model_providers."+magpieID, kvs...); err != nil {
			return err
		}
		if t, _ := edit.GetTOMLTable(path, "model_providers."+magpieID); !want && known && t["requires_openai_auth"] != "" {
			if err := edit.DelTOMLKey(path, "model_providers."+magpieID, "requires_openai_auth"); err != nil {
				return err
			}
		}
		return asyncCompaction(true)
	}
	// authStale: the magpie table says requires_openai_auth otherwise
	// than openaiAuth wants, for Sync to write it again
	authStale := func() bool {
		want, known := openaiAuth()
		if !known {
			return false
		}
		t, err := edit.GetTOMLTable(path, "model_providers."+magpieID)
		if err != nil || t == nil {
			return false
		}
		return (t["requires_openai_auth"] == "true") != want
	}
	hasProvider := func() bool {
		t, err := edit.GetTOMLTable(path, "model_providers."+magpieID)
		return err == nil && t != nil
	}
	// noProvider: the config was read and has no such table; one magpie
	// can't read is told by Check as it is, and written to by nothing
	noProvider := func() bool {
		t, err := edit.GetTOMLTable(path, "model_providers."+magpieID)
		return err == nil && t == nil
	}
	// namesMagpie: the config has Codex on magpie as its provider, at its
	// top or in a profile. Codex then reads [model_providers.magpie] as it
	// loads the config, before any thread: without the table it loads no
	// config at all ("Model provider `magpie` not found"), the app and the
	// CLI both, whatever the model.
	namesMagpie := func() bool {
		if asProvider() {
			return true
		}
		names, _ := edit.TOMLTables(path)
		for _, n := range names {
			if strings.HasPrefix(n, "profiles.") {
				if t, _ := edit.GetTOMLTable(path, n); t["model_provider"] == magpieID {
					return true
				}
			}
		}
		return false
	}
	// dropProvider takes magpie out as the provider Codex is on; the table
	// stays for the threads started on it
	dropProvider := func() error {
		if !asProvider() {
			return nil
		}
		// a model list of the user's own, put in by hand since, stays
		keys := []string{"model_provider"}
		if c := get("model_catalog_json"); c == "" || ownCatalog(c) {
			keys = append(keys, "model_catalog_json")
		}
		if err := edit.DelTOMLTop(path, keys...); err != nil {
			return err
		}
		dropList()
		return nil
	}
	// profileNamed are the provider tables a profile of config.toml names:
	// the user's to pick, each left as it is
	profileNamed := func(names []string) map[string]bool {
		named := map[string]bool{}
		for _, n := range names {
			if strings.HasPrefix(n, "profiles.") {
				if t, _ := edit.GetTOMLTable(path, n); t["model_provider"] != "" {
					named[t["model_provider"]] = true
				}
			}
		}
		return named
	}
	// bringBack moves the threads started while magpie was Codex's
	// provider (model_provider "magpie") onto the provider Codex is on now,
	// once it is off magpie: Codex lists only the threads of its current
	// provider (app-server thread/list, the TUI's resume picker), so after
	// a Disconnect, a hand edit, or magpie stepping back beside the
	// ChatGPT sign-in, every thread started on magpie was gone from Codex
	// (kaizaoji on X). Only magpie's own threads move, never those of a
	// provider of the user's (CC Switch's, #887); none while a profile
	// still names magpie, whose threads they are; and a copy of each is
	// kept in magpie's trash (MoveCodexProvider). A thread Codex wrote to
	// in the last minute, or one a Codex has open, waits for the next
	// Sync. This machine's Codex only: a distro's state database isn't
	// magpie's to open.
	bringBack := func() {
		if at.spell != nil || namesMagpie() {
			return
		}
		to := sessions.CodexProviderIn(dir)
		if to == magpieID {
			return
		}
		// in place first where it can be (bringOver's, and those started
		// on magpie that Codex hasn't packed): no copy of each, no rewrite
		if len(to) == len(magpieID) {
			moved, err := sessions.RelabelCodexThreads(dir, magpieID, to)
			if len(moved) > 0 {
				log.Printf("codex: %d thread(s) on magpie moved back to %s, where Codex lists them", len(moved), to)
			}
			if err != nil {
				log.Println("codex: moving magpie's threads back:", err)
			}
		}
		// then a paginated thread by its row alone, packed or not (bringOver's, and those
		// started on magpie): Codex reads its provider there, so none of
		// a big history is copied to the trash or rewritten
		retagged, err := sessions.RetagPagedCodexThreads(dir, magpieID, to)
		if len(retagged) > 0 {
			log.Printf("codex: %d paginated thread(s) on magpie moved back to %s, where Codex resumes them", len(retagged), to)
		}
		if err != nil {
			log.Println("codex: moving magpie's paginated threads back:", err)
		}
		moved, err := sessions.MoveCodexThreads(dir, magpieID, to)
		if len(moved) > 0 {
			log.Printf("codex: %d thread(s) started on magpie moved to %s, where Codex lists them", len(moved), to)
		}
		if err != nil {
			log.Println("codex: moving magpie's threads:", err)
		}
		// one written to in the last minute (the thread the takeover's
		// turn ran in, as the account came back) stayed on magpie, and
		// nothing else may Sync for hours: the Codex app then can't resume
		// it on the provider it is on now, so Retry fails the same way
		// each time and its model reads unavailable (nb5p on Discord).
		// Another Sync comes once the thread is quiet.
		if sessions.CodexThreadsBusyOn(dir, magpieID) {
			codexThreadsLater(dir, magpieID)
		}
	}
	// lent are the threads lendOwn moved off OpenAI, and the provider of
	// the user's they went to; or bringOver, onto magpie, from Codex on
	// that provider before
	type lentMove struct {
		To  string   `json:"to"`
		IDs []string `json:"ids"`
	}
	lent := func() lentMove {
		var l lentMove
		json.Unmarshal([]byte(stashShared()[at.key("codex.lent")]), &l)
		return l
	}
	// bringOver is bringBack the other way: while magpie is Codex's
	// provider, the threads Codex started on OpenAI before are listed
	// where Codex is now. Codex's resume picker and the app's thread list
	// show the threads of the provider Codex is on alone, so with magpie
	// its provider (no ChatGPT sign-in, an API key, magpie API, an account
	// out of its allowance) `codex resume` said "No sessions yet" over a
	// history that was all there (notnico64 on Discord). They move in
	// place (RelabelCodexThreads): those Codex has packed, a week after
	// their last turn, stay on OpenAI, as a rewrite of every one would
	// copy the whole history. bringBack moves them back as Codex leaves
	// magpie. Only the threads of the provider Codex was on before magpie
	// (stash codex.provider, OpenAI when none was set), and OpenAI's when
	// that was another one (below): those of any other provider stay.
	// None while a profile is chosen, whose provider is the one Codex
	// lists, none of a provider a profile names, which the user picks to
	// go on with them there, and none from a distro's Codex.
	//
	// The provider Codex was on before can be one of the user's own, a
	// relay's table or CC Switch's "custom". Its threads stayed on it at
	// first (#887 asked for a Move by hand: Codex 0.159 only left them out
	// of its list), but Codex 0.166 lists them in the app and refuses to
	// open one on magpie ("This chat uses custom, but your current
	// provider is magpie", hxy920830 on X: "原来的会话可以看到，但是加载不
	// 出来模型了"), so they move as OpenAI's do. Disconnected, or with CC
	// Switch back on that provider, bringBack gives them back to it. An id
	// of magpie's length moves in place; any other is rewritten with a
	// copy kept, the last 30 days of it as the packed ones below; older
	// ones from Sessions.
	//
	// Codex 0.166 resumes a thread only on the provider it is on now (the
	// app: "This chat uses openai, but your current provider is magpie.
	// Switch to openai to continue", 毅 on Discord), so a packed thread
	// left on OpenAI could no longer be reopened at all. Those whose last
	// turn was in the last 30 days move too, rewritten with a copy kept
	// as bringBack's are (MovePackedCodexThreads), newest first and
	// packedBudget bytes a sync: the rest of a big history follows over
	// the next syncs, each asked for at once while some are left (one
	// left for a sync hours away was refused all that while, Lice9735 on
	// X), and older ones from Sessions (ResumeNow moves one
	// as it is reopened).
	//
	// A paginated thread (Codex 0.166 on) goes by its row in Codex's state
	// database, not its files: Codex lists and resumes it on the provider
	// the row names. Every one moves by that row alone, of any age, packed
	// or not, any id's length (RetagPagedCodexThreads): the 30 days and
	// the budget above left an API-key user's older OpenAI threads, and
	// the Codex app refused each one ("This chat uses openai, but your
	// current provider is magpie", Parry_Moody and Cyrus on Discord).
	// bringBack moves them back the same way.
	bringOver := func() {
		if at.spell != nil || !asProvider() || sessions.CodexProviderIn(dir) != magpieID {
			return
		}
		from := stashShared()[at.key("codex.provider")]
		if from == "" {
			from = "openai"
		}
		// CC Switch's official route is bringOfficial's
		if from == magpieID || from == ccSwitchOfficial {
			return
		}
		names, _ := edit.TOMLTables(path)
		if from != "openai" && profileNamed(names)[from] {
			return
		}
		_, more := codexOnto(dir, from, magpieID)
		// one the Codex app has open (the chat on screen as magpie became
		// the provider) or wrote to this minute stayed on from: it moves
		// once it is let go (crypto0xfish on Discord)
		if sessions.CodexThreadsBusyOn(dir, from) {
			codexThreadsLater(dir, from)
		}
		// Codex on a provider of the user's before magpie (openai-http, a
		// relay's table, CC Switch's custom) still has the threads it
		// started on OpenAI before that one: the Codex app lists them and
		// refuses each on magpie as it did on that provider ("此聊天使用的
		// 是 OpenAI，但你当前的提供商是 magpie", Lice9735 on X). They
		// come to magpie too, kept as lendOwn keeps those it lends that
		// provider (codex.lent): bringBack gives every magpie thread to
		// the provider Codex goes back to, and giveLent gives these back
		// to OpenAI once Codex is on it again. None while a profile names
		// OpenAI, whose threads the user goes on with there.
		if from != "openai" && !profileNamed(names)["openai"] {
			ids, m := codexOnto(dir, "openai", magpieID)
			more = more || m
			if len(ids) > 0 {
				l := lent()
				if l.To != from {
					l = lentMove{To: from}
				}
				for _, id := range ids {
					if !slices.Contains(l.IDs, id) {
						l.IDs = append(l.IDs, id)
					}
				}
				out, _ := json.Marshal(l)
				stash(map[string]string{at.key("codex.lent"): string(out)})
			}
			if sessions.CodexThreadsBusyOn(dir, "openai") {
				codexThreadsLater(dir, "openai")
			}
		}
		// the budget left threads for the next sync, which may be hours
		// away: ask for it now
		if more {
			codexThreadsAgain()
		}
	}
	// mirrorsTaken are the mirrors takeMirrors pointed at magpie (#1796)
	mirrorsTaken := func() []string {
		var ids []string
		json.Unmarshal([]byte(stashShared()[at.key("codex.mirrors")]), &ids)
		return ids
	}
	// isCCSwitchMirror reports whether a model_providers table is CC Switch's
	// official OpenAI mirror: name = "OpenAI", requires_openai_auth = true, and no
	// base_url (or magpie's codexURL while failing over, or while
	// takeMirrors has it).
	isCCSwitchMirror := func(p string) bool {
		if p == "" || p == "openai" || p == magpieID {
			return false
		}
		t, err := edit.GetTOMLTable(path, "model_providers."+p)
		if err != nil || t["name"] != "OpenAI" || t["requires_openai_auth"] != "true" {
			return false
		}
		u := t["base_url"]
		return u == "" || (u == at.codexURL() && (stashShared()[at.key("codex.mirror_failover")] == p || slices.Contains(mirrorsTaken(), p)))
	}
	// bringOfficial moves the threads of CC Switch's "cc-switch-official"
	// to the provider Codex is on with magpie (OpenAI beside the sign-in,
	// or magpie). CC Switch 3.17 to 3.20 sent the official ChatGPT account
	// through its own proxy under that id; its threads are OpenAI's, and
	// CC Switch 4 moves them back to "openai" itself (#7948). Codex 0.166
	// resumes a thread only on the provider it is on now, so one of them
	// reopened beside magpie said "This chat uses cc-switch-official, but
	// your current provider is openai" (AcMilan on Discord). So do the
	// threads of CC Switch's OpenAI mirror (isCCSwitchMirror: "custom",
	// name OpenAI, the sign-in, no base_url of its own), which are the
	// ChatGPT account's as much: the Codex app names a provider by its
	// table's name, and refused them with "This chat uses OpenAI, but your
	// current provider is OpenAI" (Chris on Discord), Codex on the built-in
	// OpenAI and the thread on the mirror. None moves while config.toml or
	// a profile picks that id, whose threads they are then (lendOwn brings
	// OpenAI's to a mirror at the top), and none from a distro's Codex. A
	// mirror magpie took down from the top (stash codex.provider) is left
	// to bringBeside and bringOver, which give its threads back at the
	// Disconnect that puts it back. A copy of each is kept
	// (MoveCodexThreads); config.toml is left as it is.
	bringOfficial := func() {
		if at.spell != nil || !routed() {
			return
		}
		to := sessions.CodexProviderIn(dir)
		if to != "openai" && to != magpieID {
			return
		}
		names, _ := edit.TOMLTables(path)
		named := profileNamed(names)
		ids := []string{ccSwitchOfficial}
		for _, n := range names {
			if id, ok := strings.CutPrefix(n, "model_providers."); ok && !strings.Contains(id, ".") && id != ccSwitchOfficial && isCCSwitchMirror(id) {
				ids = append(ids, id)
			}
		}
		for _, from := range ids {
			if from == to || get("model_provider") == from || named[from] || from != ccSwitchOfficial && from == stashShared()[at.key("codex.provider")] {
				continue
			}
			retagged, err := sessions.RetagPagedCodexThreads(dir, from, to)
			if len(retagged) > 0 {
				log.Printf("codex: %d paginated thread(s) of CC Switch's official route %s moved to %s, where Codex resumes them", len(retagged), from, to)
			}
			if err != nil {
				log.Printf("codex: moving CC Switch's official paginated threads of %s: %v", from, err)
			}
			moved, err := sessions.MoveCodexThreads(dir, from, to)
			if len(moved) > 0 {
				log.Printf("codex: %d thread(s) of CC Switch's official route %s moved to %s, where Codex resumes them", len(moved), from, to)
			}
			if err != nil {
				log.Printf("codex: moving CC Switch's official threads of %s: %v", from, err)
			}
		}
	}
	// brought are the threads bringBeside moved onto OpenAI, and the
	// provider they were on
	type besideMove struct {
		From string   `json:"from"`
		IDs  []string `json:"ids"`
	}
	brought := func() besideMove {
		var b besideMove
		json.Unmarshal([]byte(stashShared()[at.key("codex.brought")]), &b)
		return b
	}
	// bringBeside is bringOver for Codex on OpenAI beside its sign-in,
	// routed through magpie by the base URL, with a provider of the user's
	// in the stash that config.toml no longer names: before #4347 the
	// beside wiring took the user's model_provider (CC Switch's "custom")
	// down, and Codex 0.166 refused every thread started on it ("This
	// chat uses custom, but your current provider is openai. Switch to
	// custom to continue", yikelizi9438 on Discord, the Codex app on
	// Windows). Those threads move to OpenAI, where Codex resumes them
	// through magpie, as bringOver moves them onto magpie: in place when
	// the id is OpenAI's length, the last 30 days of them rewritten with a
	// copy kept otherwise. Each one moved is kept (codex.brought) for the
	// Disconnect, which puts the provider back, to give them back to it
	// (giveBeside); OpenAI's own threads stay. None while a profile is
	// chosen or names that provider, and none from a distro's Codex.
	bringBeside := func() {
		if at.spell != nil || asProvider() || !viaBase() || get("model_provider") != "" || sessions.CodexProviderIn(dir) != "openai" {
			return
		}
		from := stashShared()[at.key("codex.provider")]
		if from == "" || from == "openai" || from == magpieID || from == ccSwitchOfficial {
			return
		}
		names, _ := edit.TOMLTables(path)
		if profileNamed(names)[from] {
			return
		}
		ids, more := codexOnto(dir, from, "openai")
		if sessions.CodexThreadsBusyOn(dir, from) {
			codexThreadsLater(dir, from)
		}
		if more {
			codexThreadsAgain()
		}
		if len(ids) == 0 {
			return
		}
		b := brought()
		if b.From != from {
			b = besideMove{From: from}
		}
		for _, id := range ids {
			if !slices.Contains(b.IDs, id) {
				b.IDs = append(b.IDs, id)
			}
		}
		out, _ := json.Marshal(b)
		stash(map[string]string{at.key("codex.brought"): string(out)})
	}
	// giveBeside gives the threads bringBeside moved back to the provider
	// they were on, once Codex is on it again (the Disconnect put it
	// back), or moves them on to to (magpie made Codex's provider, whose
	// threads bringBack gives that provider at the Disconnect). Only those
	// still on OpenAI move; either way the record goes.
	giveBeside := func(to string) {
		b := brought()
		if b.From == "" || at.spell != nil {
			forget(at.key("codex.brought"))
			return
		}
		if to == "" {
			if to = sessions.CodexProviderIn(dir); to != b.From {
				// Codex is on another provider now: they stay on OpenAI
				forget(at.key("codex.brought"))
				return
			}
		}
		retagged, err := sessions.RetagPagedCodexThreadIDs(dir, "openai", to, b.IDs)
		if err != nil {
			log.Printf("codex: moving %s's paginated threads to %s: %v", b.From, to, err)
		}
		moved, err := sessions.MoveCodexThreadIDs(dir, "openai", to, b.IDs)
		if n := len(moved) + len(retagged); n > 0 {
			log.Printf("codex: %d thread(s) started on %s moved from OpenAI to %s", n, b.From, to)
		}
		if err != nil {
			log.Printf("codex: moving %s's threads to %s: %v", b.From, to, err)
		}
		forget(at.key("codex.brought"))
	}
	// followBeside: magpie made Codex's provider since, the threads
	// bringBeside moved onto OpenAI go on to magpie, as bringOver's do
	followBeside := func() {
		if asProvider() && brought().From != "" && sessions.CodexProviderIn(dir) == magpieID {
			giveBeside(magpieID)
		}
	}
	// The provider tables in Codex's config a reopened thread goes to,
	// each one's base URL by its id: CC Switch's, and any other a Codex
	// thread was started on. A thread keeps the provider it was started
	// on, and CC Switch moves every third-party thread onto its "custom"
	// one, so one reopened there still went to the relay, a magpie model
	// picked in it too, which the relay didn't know. A table of the user's
	// own, or one a newer CC Switch names after its card, did the same:
	// an old thread sent "apikeyfan/gpt-6.1-sol" straight to the relay
	// ("not supported by any configured account") while a new one worked.
	// While a magpie model is on, those tables go through magpie as well;
	// a table a profile names is the user's to pick, and stays as it is,
	// and so does one no thread was started on.
	ccSwitchTables := func() map[string]string {
		names, _ := edit.TOMLTables(path)
		named := profileNamed(names)
		out := map[string]string{}
		var threads []string
		read := false
		for _, n := range names {
			id, ok := strings.CutPrefix(n, "model_providers.")
			if !ok || id == magpieID || codexBuiltinProvider[id] || named[id] || isCCSwitchMirror(id) {
				continue
			}
			t, _ := edit.GetTOMLTable(path, n)
			if t["base_url"] == "" {
				continue
			}
			if !ccSwitchProvider.MatchString(id) {
				// the state database is read only when there is a table
				// it can decide, and once
				if !read {
					threads, read = sessions.CodexThreadProviders(dir), true
				}
				if !slices.Contains(threads, id) {
					continue
				}
			}
			out[id] = t["base_url"]
		}
		return out
	}
	// putThreadTables writes a table for each provider a Codex thread was
	// started on that config.toml has none of, pointed at magpie. The
	// Codex app resumes a thread on its own provider, and without that
	// table it loads no config for it: "Model provider `custom` not found",
	// a thread started through CC Switch reopened after CC Switch rewrote
	// config.toml without its table (#1372). Codex's built-in providers
	// need none, and magpie's own is putProvider's. A table written so
	// stays, as magpie's own does, for the threads that name it.
	putThreadTables := func() error {
		// the providers config.toml has, however spelled (a table, inline,
		// dotted keys); one that doesn't parse is left alone
		doc, err := codexTopOf(path)
		if err != nil {
			return nil
		}
		providers, ok := doc.ModelProviders.(map[string]any)
		if !ok && doc.ModelProviders != nil {
			return nil
		}
		for _, id := range sessions.CodexThreadProviders(dir) {
			if _, ok := providers[id]; ok || id == magpieID || codexBuiltinProvider[id] {
				continue
			}
			if err := edit.SetTOMLTable(path, "model_providers."+id,
				edit.KV{Path: "name", Value: id},
				edit.KV{Path: "base_url", Value: at.v1()},
				edit.KV{Path: "wire_api", Value: "responses"},
				edit.KV{Path: "experimental_bearer_token", Value: keptBearerToken("model_providers." + id)},
			); err != nil {
				return err
			}
		}
		return nil
	}
	// takeMirrors points CC Switch's OpenAI mirrors (the official OpenAI on
	// the ChatGPT sign-in, under an id of its own and with no base_url) at
	// magpie's ChatGPT backend while Codex's built-in provider goes there
	// by openai_base_url, which a table of another id doesn't follow. A
	// thread started on one ("custom") and reopened went straight to
	// ChatGPT, which refused the magpie model picked in it ("The
	// 'group/intent-auto' model is not supported when using Codex with a
	// ChatGPT account"), and magpie never saw the request (#1796). Through
	// magpie it is answered as the built-in provider's threads are: a
	// magpie model by magpie, Codex's own on the ChatGPT accounts. A table
	// a profile names stays as it is.
	takeMirrors := func() error {
		if !viaBase() {
			return nil
		}
		names, _ := edit.TOMLTables(path)
		named := profileNamed(names)
		taken := mirrorsTaken()
		for _, n := range names {
			id, ok := strings.CutPrefix(n, "model_providers.")
			if !ok || named[id] || !isCCSwitchMirror(id) {
				continue
			}
			if t, _ := edit.GetTOMLTable(path, n); t["base_url"] != "" {
				continue
			}
			if err := edit.SetTOMLKey(path, n, "base_url", at.codexURL()); err != nil {
				return err
			}
			if !slices.Contains(taken, id) {
				taken = append(taken, id)
			}
		}
		if len(taken) == 0 {
			return nil
		}
		b, _ := json.Marshal(taken)
		stash(map[string]string{at.key("codex.mirrors"): string(b)})
		return nil
	}
	// giveMirrors takes the base URL takeMirrors wrote off again; one
	// changed since is left as it is now
	giveMirrors := func() error {
		taken := mirrorsTaken()
		if len(taken) == 0 {
			return nil
		}
		for _, id := range taken {
			t, err := edit.GetTOMLTable(path, "model_providers."+id)
			if err != nil || t["base_url"] != at.codexURL() || stashShared()[at.key("codex.mirror_failover")] == id {
				continue
			}
			if err := edit.DelTOMLKey(path, "model_providers."+id, "base_url"); err != nil {
				return err
			}
		}
		forget(at.key("codex.mirrors"))
		return nil
	}
	// takeTables points CC Switch's tables at magpie, each one's own base
	// URL kept in the stash for giveTables (a table already on magpie's
	// keeps the one kept for it), and writes the tables threads name that
	// are gone (putThreadTables), and takes CC Switch's OpenAI mirrors
	// (takeMirrors)
	takeTables := func() error {
		if err := putThreadTables(); err != nil {
			return err
		}
		if err := takeMirrors(); err != nil {
			return err
		}
		was := map[string]string{}
		json.Unmarshal([]byte(stashShared()[at.key("codex.tables")]), &was)
		for id, u := range ccSwitchTables() {
			if u == at.v1() {
				continue
			}
			if err := edit.SetTOMLKey(path, "model_providers."+id, "base_url", at.v1()); err != nil {
				return err
			}
			was[id] = u
		}
		if len(was) == 0 {
			return nil
		}
		b, _ := json.Marshal(was)
		stash(map[string]string{at.key("codex.tables"): string(b)})
		return nil
	}
	// giveTables puts the base URLs back, on the tables still pointed at
	// magpie; one changed since is left as it is now. Each table is read
	// as it is, not through ccSwitchTables: that counts a table of the
	// user's own only while a thread names it, and bringOver had moved
	// its threads onto magpie by then, so the Disconnect forgot the
	// relay's URL and left the user's provider pointed at magpie
	// (darwish4021 on Discord)
	giveTables := func() error {
		if err := giveMirrors(); err != nil {
			return err
		}
		var was map[string]string
		json.Unmarshal([]byte(stashShared()[at.key("codex.tables")]), &was)
		for id, u := range was {
			if t, err := edit.GetTOMLTable(path, "model_providers."+id); err != nil || t["base_url"] != at.v1() {
				continue
			}
			if err := edit.SetTOMLKey(path, "model_providers."+id, "base_url", u); err != nil {
				return err
			}
		}
		forget(at.key("codex.tables"))
		return nil
	}
	// api: the user wants magpie as Codex's provider even while Codex is
	// signed in to ChatGPT
	api := func() bool { return stashShared()[at.key("codex.login")] == "api" }
	// out reports whether the Codex app holds the ChatGPT account Codex is
	// signed in to (codexUsedUp), for magpie to become Codex's provider
	// while it does (#540): never with the sign-in kept on ChatGPT
	// (chatgpt), where the user would rather the Codex app wait for the
	// account than leave its ChatGPT state
	out := func() bool { return stashShared()[at.key("codex.login")] != "chatgpt" && codexUsedUp() }
	dropBase := func() error {
		forget(at.key("codex.failover"))
		if !viaBase() {
			return nil
		}
		return edit.DelTOMLTop(path, "openai_base_url")
	}
	dropMirrorFailover := func() error {
		p := stashShared()[at.key("codex.mirror_failover")]
		if p == "" {
			return nil
		}
		forget(at.key("codex.mirror_failover"))
		t, _ := edit.GetTOMLTable(path, "model_providers."+p)
		if t["base_url"] == at.codexURL() {
			return edit.DelTOMLKey(path, "model_providers."+p, "base_url")
		}
		return nil
	}
	// the model spawned subagents start on, when not the parent's; one of
	// magpie's goes when magpie steps out, as Codex could no longer find it
	subagent := func() (string, error) {
		agents, err := edit.GetTOMLTable(path, "agents")
		return agents["default_subagent_model"], err
	}
	// the model Codex writes its memories with (Yc on Discord): [memories]
	// extract_model summarises a thread, consolidation_model folds them in
	// (codex-rs/config/src/types.rs). Unset, Codex takes models of its own
	// whatever its model (from 0.167.0 gpt-6-luna and gpt-6.1-terra, before
	// that gpt-5.6-luna and gpt-5.6-terra); magpie sets both to one pick.
	memories := func() (string, error) {
		m, err := edit.GetTOMLTable(path, "memories")
		return cmp.Or(m["consolidation_model"], m["extract_model"]), err
	}
	dropMemories := func() error {
		for _, k := range []string{"extract_model", "consolidation_model"} {
			if err := edit.DelTOMLKey(path, "memories", k); err != nil {
				return err
			}
		}
		return nil
	}
	// one of magpie's goes too: Codex could no longer find it
	dropSubagent := func() error {
		if m, err := memories(); err != nil {
			return err
		} else if isMagpie(m) {
			if err := dropMemories(); err != nil {
				return err
			}
		}
		model, err := subagent()
		if err != nil {
			return err
		}
		if !isMagpie(model) {
			return nil
		}
		return edit.DelTOMLKey(path, "agents", "default_subagent_model")
	}
	// the effort spawned subagents start at, when not the parent's
	// (core/src/agent/child_config.rs): checked against the model they run
	// on, theirs or the parent's, and a spawn at one it lacks fails, so one
	// that model doesn't take goes
	subEffort := func() (string, error) {
		agents, err := edit.GetTOMLTable(path, "agents")
		return agents["default_subagent_reasoning_effort"], err
	}
	subModel := func() string {
		if m, _ := subagent(); m != "" {
			return m
		}
		return get("model")
	}
	subEfforts := func() []string { return catalog.Efforts(models(), subModel()) }
	dropSubEffort = func() error {
		e, err := subEffort()
		if err != nil || e == "" {
			return err
		}
		if l := subEfforts(); len(l) > 0 && !contains(l, e) {
			return edit.DelTOMLKey(path, "agents", "default_subagent_reasoning_effort")
		}
		return nil
	}
	// the user disconnected Codex and hasn't connected it since: magpie
	// writes nothing of its own back (paynezhuang on Discord: the base URL
	// was back after every update)
	disconnected := func() bool { return stashShared()[at.key("codex"+disconnectedKey)] == "1" }
	// lendOwn is bringBeside the other way: Codex on a provider of the
	// user's at the top (CC Switch's "custom", left there when the user
	// came to magpie, which keeps it since #4347), the threads it started
	// on OpenAI before could not be continued at all. Codex 0.166 resumes
	// a thread only on the provider it is on now ("This chat uses openai,
	// but your current provider is custom. Switch to openai to continue",
	// harris on Discord, the Codex app after CC Switch), and switching
	// would refuse every thread of theirs instead. Those threads move to
	// that provider, where Codex resumes them, as bringBeside moves that
	// provider's onto OpenAI: in place when the id is OpenAI's length, the
	// last 30 days of them rewritten with a copy kept otherwise, a
	// paginated one by its row. Each one moved is kept (codex.lent) for
	// giveLent to give back once Codex is on OpenAI again. CC Switch's
	// OpenAI mirror at the top is such a provider too: the Codex app
	// names both by the name "OpenAI" and refused OpenAI's threads with
	// "This chat uses OpenAI, but your current provider is OpenAI" (Chris
	// on Discord). None while a
	// profile is chosen (Codex lists that one's threads) or names OpenAI,
	// none once the user disconnected Codex from magpie, and none from a
	// distro's Codex.
	lendOwn := func() {
		if at.spell != nil || disconnected() {
			return
		}
		to := get("model_provider")
		if to == "" || to == "openai" || to == magpieID || to == ccSwitchOfficial || sessions.CodexProviderIn(dir) != to {
			return
		}
		names, _ := edit.TOMLTables(path)
		if profileNamed(names)["openai"] {
			return
		}
		ids, more := codexOnto(dir, "openai", to)
		if sessions.CodexThreadsBusyOn(dir, "openai") {
			codexThreadsLater(dir, "openai")
		}
		if more {
			codexThreadsAgain()
		}
		if len(ids) == 0 {
			return
		}
		l := lent()
		if l.To != to {
			l = lentMove{To: to}
		}
		for _, id := range ids {
			if !slices.Contains(l.IDs, id) {
				l.IDs = append(l.IDs, id)
			}
		}
		out, _ := json.Marshal(l)
		stash(map[string]string{at.key("codex.lent"): string(out)})
	}
	// giveLent gives the threads lendOwn moved back to OpenAI once Codex
	// is on OpenAI again (CC Switch's official card, the user's provider
	// taken out): those still on the provider they went to. While Codex is
	// on that provider, or on magpie (bringOver took them there, and
	// bringBack gives them to the provider Codex goes back to), the record
	// stays; on any other provider it goes, and they stay where they are.
	giveLent := func() {
		l := lent()
		if l.To == "" {
			return
		}
		now := sessions.CodexProviderIn(dir)
		if now == l.To || now == magpieID {
			return
		}
		if now == "openai" && at.spell == nil {
			retagged, err := sessions.RetagPagedCodexThreadIDs(dir, l.To, "openai", l.IDs)
			if err != nil {
				log.Printf("codex: moving paginated threads back from %s to OpenAI: %v", l.To, err)
			}
			moved, err := sessions.MoveCodexThreadIDs(dir, l.To, "openai", l.IDs)
			if n := len(moved) + len(retagged); n > 0 {
				log.Printf("codex: %d thread(s) started on OpenAI moved back from %s", n, l.To)
			}
			if err != nil {
				log.Printf("codex: moving threads back from %s to OpenAI: %v", l.To, err)
			}
		}
		forget(at.key("codex.lent"))
	}
	// Codex on one of its own models goes through magpie too while more of
	// its ChatGPT accounts are on there, so one out of its allowance hands
	// the turn to the next; with none, or once the user disconnected Codex,
	// it goes straight to OpenAI again
	failover := func() error {
		if wired() || asProvider() {
			return nil
		}
		p := get("model_provider")
		mirror := isCCSwitchMirror(p)
		if !mirror {
			_ = dropMirrorFailover()
		}
		if p != "" && p != "openai" && !mirror {
			return nil
		}
		// a Codex that reaches the gateway from another machine is never
		// pointed at the Codex path: the gateway takes a named key there,
		// so the turn failover handed it came back a 401 (#2145)
		on := !disconnected() && codexFailover() && !onAnotherMachine(at.gw())
		// failover is magpie's own idea, not the user's: never handed
		// Codex's requests while a proxy they'd go through doesn't pass
		// them on to magpie, which would have every turn fail with 502
		// (codex_proxy.go); one written before goes again
		if on && at.spell == nil && !codexProxyOK(path, at.gw()) {
			on = false
		}
		if mirror {
			t, _ := edit.GetTOMLTable(path, "model_providers."+p)
			hasGateway := t["base_url"] == at.codexURL()
			switch {
			case on && !hasGateway:
				if err := edit.SetTOMLKey(path, "model_providers."+p, "base_url", at.codexURL()); err != nil {
					return err
				}
				stash(map[string]string{at.key("codex.mirror_failover"): p})
			case !on && hasGateway:
				if err := edit.DelTOMLKey(path, "model_providers."+p, "base_url"); err != nil {
					return err
				}
				forget(at.key("codex.mirror_failover"))
			}
			return nil
		}
		// the base URL failover wrote is marked (codex.failover), and only
		// that one goes again: one the user wrote in config.toml by hand,
		// to have Codex's own models go through magpie, stays (#856: it was
		// gone again each time magpie started). One already there while
		// failover is on is taken as failover's, as a magpie from before
		// the mark wrote it.
		owned := stashShared()[at.key("codex.failover")] == "1"
		switch {
		case on && !viaBase():
			if err := edit.SetTOMLTop(path, edit.KV{Path: "openai_base_url", Value: at.codexURL()}); err != nil {
				return err
			}
			stash(map[string]string{at.key("codex.failover"): "1"})
		case on && !owned:
			stash(map[string]string{at.key("codex.failover"): "1"})
		case !on && viaBase() && owned:
			return dropBase()
		}
		return nil
	}
	// failingOver: Codex reaches magpie for account failover alone, by the
	// base URL failover wrote (codex.failover) or by CC Switch's OpenAI
	// mirror pointed at magpie for it (codex.mirror_failover), with nothing
	// of magpie's connected. It read as not connected while every request
	// went through magpie, magpie's models in its list (#1385).
	failingOver := func() bool {
		if wired() || asProvider() {
			return false
		}
		s := stashLoad()
		if p := get("model_provider"); p != "" && p != "openai" {
			t, _ := edit.GetTOMLTable(path, "model_providers."+p)
			return s[at.key("codex.mirror_failover")] == p && t["base_url"] == at.codexURL()
		}
		return viaBase() && s[at.key("codex.failover")] == "1"
	}
	modelOptions := func(withMagpie bool) []Option {
		var own []Option
		// each of Codex's own says which way it goes, as Claude Code's do:
		// to its provider directly, or through magpie while routed by the
		// base URL or with magpie its provider (EZN7L2C3, #834: Codex 下拉
		// 选项中没有 via magpie)
		p := get("model_provider")
		to, through := "OpenAI", routed()
		if p != "" && p != magpieID && p != "openai" {
			// a provider of the user's own, or CC Switch's, pointed at
			// magpie or not
			t, _ := edit.GetTOMLTable(path, "model_providers."+p)
			to, through = p, t["base_url"] == at.codexURL() || t["base_url"] == at.v1()
		}
		// beside its sign-in on one of magpie's models, not joined, one of
		// its own picked here takes magpie out (set, #940) unless failover
		// puts the base URL back: it said via magpie, and the pick went
		// straight to OpenAI with nothing asked (NancyTom18 on X)
		ownThrough := through
		if through && (p == "" || p == "openai") && beside() && !asProvider() && !api() &&
			!(joined() && chatGPT()) && !(codexFailover() && !disconnected()) {
			ownThrough = false
		}
		say := func(own []Option) []Option {
			for i := range own {
				if ownThrough {
					own[i].Via = true
				} else {
					own[i].Direct = to
				}
			}
			return own
		}
		if p != "" && p != magpieID && !isCCSwitchMirror(p) {
			own = say(group(p, codexNamed(catalog.Codex())))
		} else {
			own = codexInMagpie(say(group("OpenAI", codexNamed(ownCodex()))))
			// with magpie Codex's provider (magpie API, or a magpie model
			// picked with no ChatGPT sign-in beside it), Codex's own models
			// are reached through magpie on a ChatGPT account there: picked
			// so, set so (#701). One magpie doesn't serve is picked off
			// magpie, straight to OpenAI, and says so: it said via magpie,
			// and picking it took Codex off magpie at once (#1269)
			if api() || asProvider() {
				for i, o := range own {
					if id := codexOwnViaMagpie(o.Value); id != "" {
						own[i].Value, own[i].Ref = id, id
					} else if !api() {
						own[i].Via, own[i].Direct = false, "OpenAI"
					}
				}
			}
		}
		if !withMagpie {
			return own
		}
		// the models magpie serves on the Codex accounts in it (codex/…)
		// are left out while they would only add a hop: Codex on its own
		// ChatGPT sign-in, that one account alone in magpie. On a relay or
		// a key they are not Codex's sign-in, and over several accounts
		// magpie routes; offered via magpie then, beside the direct ones
		// (#1603: Codex on CC Switch's custom relay, two accounts, GPT
		// models direct only). Routed, Codex's own say via magpie already.
		onSignIn := (p == "" || p == "openai" || isCCSwitchMirror(p)) && chatGPT()
		if !through && (!onSignIn || codexFailover()) {
			return withVia(own, "codex", "")
		}
		return append(own, codexViaMagpieFor(own)...)
	}
	// unroute takes magpie out as the way Codex reaches its models and puts
	// back what the stash kept from before magpie was wired in (its
	// provider, catalog and effort); it answers the model Codex was on
	// then, for Unwire to go back to
	unroute := func() (string, error) {
		forget(at.key("codex.out"), at.key("codex.joined"), at.key("codex.beside"))
		if err := giveTables(); err != nil {
			return "", err
		}
		if !routed() {
			return "", nil
		}
		if err := dropSubagent(); err != nil {
			return "", err
		}
		if err := dropBase(); err != nil {
			return "", err
		}
		if err := dropProvider(); err != nil {
			return "", err
		}
		was := unstash(at.key("codex.model"))
		// what was there before magpie is put back only where magpie's
		// change still stands: a key the user set by hand since keeps
		// theirs (yhwangl on X)
		var back []edit.KV
		if p := unstash(at.key("codex.provider")); p != "" && p != magpieID && get("model_provider") == "" {
			back = append(back, edit.KV{Path: "model_provider", Value: p})
		}
		// the catalog of the user's own put back while its file is there:
		// one removed since would keep Codex from loading its config
		if c := unstash(at.key("codex.catalog")); c != "" && !ownCatalog(c) && !deskCatalog(c) && !theirListGone(c) && (get("model_catalog_json") == "" || deskCatalog(get("model_catalog_json"))) {
			back = append(back, edit.KV{Path: "model_catalog_json", Value: c})
		}
		// the effort, where it is still one magpie set: picked on the
		// Agents page, or moved to one its model has. One taken out
		// reads the same however it was, and comes back.
		id := at.id
		if id == "" {
			id = "codex"
		}
		ours := map[string]bool{"": true}
		if set := unstash(at.key("codex.effort_set")); set != "" {
			ours[set] = true
		}
		if r := appliedOf(id).Fields["effort"]; r != "" {
			ours[r] = true
		}
		if e := unstash(at.key("codex.effort")); e != "" && ours[get("model_reasoning_effort")] {
			back = append(back, edit.KV{Path: "model_reasoning_effort", Value: e})
		}
		if len(back) > 0 {
			if err := edit.SetTOMLTop(path, back...); err != nil {
				return "", err
			}
		}
		return was, nil
	}
	set := func(v string) error {
		if v == "" {
			if err := dropSubagent(); err != nil {
				return err
			}
			if err := giveTables(); err != nil {
				return err
			}
			if err := dropMirrorFailover(); err != nil {
				return err
			}
			// Codex as installed: OpenAI, its own catalog, its default model
			if err := dropBase(); err != nil {
				return err
			}
			// Codex as installed: OpenAI, its own catalog, its default
			// model. A provider of the user's at the top stays (#4347):
			// a reset takes magpie's keys out, not theirs, and Codex
			// goes on resuming the threads it saved with theirs
			keys := []string{"model", "model_catalog_json"}
			if p := get("model_provider"); p == "" || p == "openai" || p == magpieID {
				keys = append(keys, "model_provider")
			}
			if err := edit.DelTOMLTop(path, keys...); err != nil {
				return err
			}
			dropList()
			forget(at.key("codex.model"), at.key("codex.effort"), at.key("codex.effort_set"), at.key("codex.provider"), at.key("codex.catalog"), at.key("codex.out"), at.key("codex.joined"), at.key("codex.beside"), at.key("codex.brought"))
			return deskList()
		}
		// magpie API: magpie stays Codex's provider whichever model is the
		// default, so one of Codex's own goes as magpie serves it, on the
		// ChatGPT account there; taking magpie out for it took magpie's
		// models out of Codex's picker with it (#701)
		if api() && !isMagpie(v) {
			id := codexOwnViaMagpie(v)
			if id == "" {
				return fmt.Errorf("Codex's sign-in is magpie API, and magpie serves no %s: switch Codex's ChatGPT subscription on in magpie, or set Codex's sign-in to ChatGPT", v)
			}
			v = id
		}
		if isMagpie(v) {
			if err := dropMirrorFailover(); err != nil {
				return err
			}
			if !routed() {
				stash(map[string]string{at.key("codex.model"): get("model"), at.key("codex.effort"): get("model_reasoning_effort"),
					at.key("codex.provider"): get("model_provider"), at.key("codex.catalog"): get("model_catalog_json")})
			}
			// a ChatGPT account the Codex app holds (out of allowance and
			// of credits) keeps it from sending at all, a magpie model's
			// request too; as a provider of Codex's own, magpie is past
			// that. Wired so for that alone, it is marked (codex.out), for
			// Sync to put it back beside the sign-in once the account has
			// room again.
			// Beside the sign-in only from a provider at the top that is
			// magpie's to take down: its own, none, or CC Switch's mirror
			// of OpenAI, as Join has it. A provider of the user's (CC
			// Switch's custom, an older takeover's) is not taken down
			// silently (#4347): every thread saved with it stopped
			// resuming (modelProviderMismatch). The takeover names magpie
			// and keeps the user's spelling in the stash, for Unwire to
			// put back.
			p := get("model_provider")
			ours := asProvider() || p == "" || p == "openai" || isCCSwitchMirror(p)
			chatgpt := !api() && chatGPT()
			if chatgpt && !out() && ours {
				forget(at.key("codex.out"))
				if err := dropProvider(); err != nil {
					return err
				}
				// for the threads started while magpie was the provider
				if err := putProvider(); err != nil {
					return err
				}
				// the base URL is the built-in provider's, and a catalog
				// file would stand in for the list magpie hands out
				if err := edit.DelTOMLTop(path, "model_provider", "model_catalog_json"); err != nil {
					return err
				}
				if err := edit.SetTOMLTop(path,
					edit.KV{Path: "openai_base_url", Value: at.codexURL()},
					edit.KV{Path: "model", Value: v},
				); err != nil {
					return err
				}
				stash(map[string]string{at.key("codex.beside"): "1"})
				if err := takeTables(); err != nil {
					return err
				}
				return settle()
			}
			// codex.out is for an account the takeover stands in for: a
			// provider of the user's taken over is magpie's doing, not
			// the account's, so Sync never steps Codex out of it
			if chatgpt && ours {
				stash(map[string]string{at.key("codex.out"): "1"})
			} else {
				forget(at.key("codex.out"))
			}
			// magpie is its provider now, not beside the sign-in
			forget(at.key("codex.beside"))
			if err := putProvider(); err != nil {
				return err
			}
			if err := writeOwnList(catalogPath, ownList()); err != nil {
				return err
			}
			// a thread started on Codex's built-in provider stays on it when
			// opened again, and its model can be switched to one of magpie's
			// there (the Codex app's, ChatGPT Desktop's picker): the base
			// URL sends that one to magpie too, not to the ChatGPT backend,
			// which refuses it (#259). A base URL of the user's own stays.
			kv := []edit.KV{
				{Path: "model_provider", Value: magpieID},
				{Path: "model_catalog_json", Value: catalogRef},
				{Path: "model", Value: v},
			}
			if u := get("openai_base_url"); u == "" || viaBase() {
				kv = append(kv, edit.KV{Path: "openai_base_url", Value: at.codexURL()})
			}
			if err := edit.SetTOMLTop(path, kv...); err != nil {
				return err
			}
			if err := takeTables(); err != nil {
				return err
			}
			// the threads from before, where Codex lists them now
			bringOver()
			followBeside()
			return settle()
		}
		// joined, a model of Codex's own is picked beside magpie's, as in
		// Codex's /model: it stays connected
		if joined() && !api() && chatGPT() {
			// magpie made its provider for a used-up account (codex.out)
			// and that no longer holds (kept on ChatGPT, or room again):
			// back on the sign-in as Join leaves it. Only the model set
			// left magpie the provider of one of Codex's own for good
			// (eddie.zhan on Discord, Always ChatGPT from the takeover)
			if asProvider() && stashShared()[at.key("codex.out")] == "1" && !out() {
				forget(at.key("codex.out"), at.key("codex.out_own"), at.key("codex.out_stand"))
				if err := edit.DelTOMLTop(path, "model_provider", "model_catalog_json"); err != nil {
					return err
				}
				if err := edit.SetTOMLTop(path,
					edit.KV{Path: "openai_base_url", Value: at.codexURL()},
					edit.KV{Path: "model", Value: v},
				); err != nil {
					return err
				}
				return settle()
			}
			if err := edit.SetTOMLTop(path, edit.KV{Path: "model", Value: v}); err != nil {
				return err
			}
			return settle()
		}
		if _, err := unroute(); err != nil {
			return err
		}
		if err := edit.SetTOMLTop(path, edit.KV{Path: "model", Value: v}); err != nil {
			return err
		}
		if err := failover(); err != nil {
			return err
		}
		return settle()
	}

	a := atomic(&Agent{
		ID: "codex", Name: "Codex", Icon: "codex-color", Bin: "codex", Dir: dir, Path: path,
		Telemetry: codexTelemetry(at, path),
		UA:        []string{"codex", "acp-extension-codex"}, // The ACP adapter's default client name becomes Codex's originator.
		// Codex as it was before magpie: its default puts it back as
		// installed, OpenAI and its default model, where this brings back
		// the provider and model the user had
		Unwire: func() error {
			now := get("model")
			was, err := unroute()
			if err != nil {
				return err
			}
			if err := dropSubagent(); err != nil {
				return err
			}
			// the model left alone where magpie had none to take over; a
			// model of Codex's own picked while joined stays, its last pick
			switch {
			case now != "" && !isMagpie(now):
			case was != "" && !isMagpie(was):
				err = edit.SetTOMLTop(path, edit.KV{Path: "model", Value: was})
			case isMagpie(get("model")):
				err = edit.DelTOMLTop(path, "model")
			}
			if err != nil {
				return err
			}
			dropList()
			forget(at.key("codex.model"), at.key("codex.effort"), at.key("codex.effort_set"), at.key("codex.provider"), at.key("codex.catalog"))
			// its threads go on without magpie: the compactions magpie made
			// in them written as Codex's own (a distro's Codex locks its
			// files where this machine's locks can't reach)
			if at.spell == nil {
				portCodexHome(dir, codexPortUnwire, true)
			}
			// and are listed where Codex now is
			bringBack()
			// and those bringBeside moved onto OpenAI go back to theirs
			giveBeside("")
			return settle()
		},
		// signed in with ChatGPT, Codex reaches magpie by the base URL with
		// its own models still there: connected so, it stays on the model it
		// was on, the one last picked in its /model (the owner: 让 Codex 记住
		// 上次的选择), and magpie's models join its list
		Join: func() (bool, error) {
			if api() || !chatGPT() || out() || isMagpie(get("model")) {
				return false, nil
			}
			if p := get("model_provider"); p != "" && p != "openai" && !isCCSwitchMirror(p) {
				return false, nil
			}
			if err := dropMirrorFailover(); err != nil {
				return false, err
			}
			if !routed() {
				stash(map[string]string{at.key("codex.model"): get("model"), at.key("codex.effort"): get("model_reasoning_effort"),
					at.key("codex.provider"): get("model_provider"), at.key("codex.catalog"): get("model_catalog_json")})
			}
			forget(at.key("codex.out"))
			if err := dropProvider(); err != nil {
				return false, err
			}
			// for the threads started on one of magpie's models
			if err := putProvider(); err != nil {
				return false, err
			}
			if err := edit.DelTOMLTop(path, "model_provider", "model_catalog_json"); err != nil {
				return false, err
			}
			if err := edit.SetTOMLTop(path, edit.KV{Path: "openai_base_url", Value: at.codexURL()}); err != nil {
				return false, err
			}
			stash(map[string]string{at.key("codex.joined"): "1"})
			if err := takeTables(); err != nil {
				return false, err
			}
			if err := codexStaleCache(filepath.Join(dir, "models_cache.json"), provider.CodexListTag()); err != nil {
				return false, err
			}
			return true, settle()
		},
		// joined as the GUI and drift tell it: Codex's own models in its
		// list beside magpie's. With magpie its provider (magpie API, or a
		// ChatGPT account the Codex app holds) they are not, though the
		// mark stays for Sync to put it back beside the sign-in: the
		// Agents row said "its own models stay in /model" while the Codex
		// app was meant to be in its API state (#2267)
		Joined:    func() bool { return joined() && !asProvider() },
		AppOut:    func() bool { return asProvider() && !api() && chatGPT() && stashShared()[at.key("codex.out")] == "1" },
		AppOutOff: func() bool { return stashShared()[at.key("codex.login")] == "chatgpt" || !routed() },
		HoldMatters: func() bool {
			return stashShared()[at.key("codex.login")] == "" && chatGPT() &&
				(viaBase() || asProvider() && stashShared()[at.key("codex.out")] == "1")
		},
		Beside:      beside,
		Routed:      routed,
		FailingOver: failingOver,
		EndFailover: func() error {
			if !failingOver() {
				return nil
			}
			return errors.Join(dropMirrorFailover(), dropBase())
		},
		// what no Disconnect could name, marks gone: only what names magpie
		// itself goes, by the same gates a Disconnect takes them by
		TakeLeftovers: func() (bool, error) {
			took := false
			if viaBase() {
				if err := dropBase(); err != nil {
					return false, err
				}
				took = true
			}
			if asProvider() {
				if err := dropProvider(); err != nil {
					return false, err
				}
				took = true
			}
			return took, nil
		},
		OwnVia: codexOwnViaMagpie,
		SideModel: func() string {
			if m := get("model"); viaBase() && !asProvider() && isMagpie(m) {
				return m
			}
			return ""
		},
		Sync: func() error {
			// after the sync, which may have put Codex back beside its
			// sign-in or made magpie its provider, and whatever it failed at
			defer lendOwn()
			defer giveLent()
			defer followBeside()
			defer bringBeside()
			defer bringOfficial()
			defer bringOver()
			defer bringBack()
			// model_catalog_json naming magpie's list with the list gone,
			// as an older magpie's Disconnect could leave it (zwaydot on
			// X): Codex loads no config at all then. Connected or not, the
			// list is written again while magpie is the provider, else the
			// key goes, at the next start, with no reconnect.
			if ownCatalog(get("model_catalog_json")) && listGone() {
				if asProvider() {
					if err := writeOwnList(catalogPath, ownList()); err != nil {
						return err
					}
				} else if err := unnameGoneList(); err != nil {
					return err
				}
			}
			// the Codex app's list, written again as magpie is wired now
			// (the backend's list or magpie's models changed), or taken out
			// where it is no longer wanted: a hand edit, another tool
			if err := deskList(); err != nil {
				return err
			}
			if err := asyncCompaction(routed()); err != nil {
				return err
			}
			// model_provider = "magpie" left with its table gone (taken by
			// another tool, or a hand edit), which keeps Codex from loading
			// its config at all, connected to magpie or not (Tystem on
			// Discord): the table is written back, as it stays once written
			if namesMagpie() && noProvider() {
				if err := putProvider(); err != nil {
					return err
				}
			}
			// the sign-in came or went, or the account ran out or has
			// room again: requires_openai_auth follows (openaiAuth), on a
			// table an older magpie wrote too
			if authStale() {
				if err := putProvider(); err != nil {
					return err
				}
			}
			if err := failover(); err != nil {
				return err
			}
			// magpie API on one of Codex's own models, as an older magpie
			// left it unwired: wired again, as picking it does now (#701);
			// not one the user disconnected, which this left the same
			if m := get("model"); api() && !disconnected() && !isMagpie(m) && !asProvider() && codexOwnViaMagpie(m) != "" {
				return set(m)
			}
			// on a magpie model by the base URL alone with no ChatGPT
			// sign-in, as an older magpie left a Codex signed in with an
			// API key: its picker never had magpie's models (#322)
			if m := get("model"); isMagpie(m) && viaBase() && get("model_provider") == "" && !chatGPT() {
				return set(m)
			}
			// the ChatGPT account ran out on one of Codex's own models,
			// joined beside the sign-in (MasterYi on Discord: the Codex
			// app greyed Send, a magpie model's turn too), as in #540
			// below: magpie becomes Codex's provider on that model as
			// magpie serves it, the one picked kept (codex.out_own) to go
			// back to once the account has room. One magpie serves on no
			// account of its own is left as it is, and so is one magpie
			// could serve on that same account alone, with no fallback
			// and no other account (provider.CodexServesWhileOut): every
			// turn would be refused there too, and the takeover only hid
			// the account and its reset and restarted the app twice
			// (xiaoq6285 on Discord, one Plus account)
			if m := get("model"); !isMagpie(m) && !api() && chatGPT() && joined() && out() && provider.CodexServesWhileOut(m) {
				if id := codexOwnViaMagpie(m); id != "" {
					stash(map[string]string{at.key("codex.out_own"): m})
					return set(id)
				}
			}
			// magpie can't serve the model picked on that account, but the
			// Codex app greys Send for every model while the account is
			// out (shamen, chenhan, xiaoq6285 on Discord): with a model of
			// magpie's on no account of the held one, magpie becomes
			// Codex's provider on that one so the user can send at all.
			// This reverses f37742e14, which left Codex on its sign-in to
			// keep the account's menu: a grey Send for every model costs
			// more. Once per held/unheld transition: on the stand-in the
			// model is magpie's, so the branch is not met again; the
			// stand-in is kept (codex.out_stand) for the way back below.
			if m := get("model"); !isMagpie(m) && !api() && chatGPT() && joined() && out() && !provider.CodexServesWhileOut(m) {
				if id := codexOtherModel(); id != "" {
					stash(map[string]string{at.key("codex.out_own"): m, at.key("codex.out_stand"): id})
					return set(id)
				}
			}
			// put back before #540's way back, which would take
			// codex/<model> for a magpie model picked beside the sign-in
			if own := stashShared()[at.key("codex.out_own")]; own != "" {
				m := get("model")
				switch {
				case stashShared()[at.key("codex.out")] != "1" || !asProvider() ||
					codexOwnOf(m) != own && m != stashShared()[at.key("codex.out_stand")]:
					// moved since (another model picked, magpie API, a
					// disconnect): that stands
					forget(at.key("codex.out_own"), at.key("codex.out_stand"))
				case !out():
					// back beside the sign-in on the one picked, as Join
					// leaves it: Codex's own sign-in, magpie's models joined
					forget(at.key("codex.out_own"), at.key("codex.out"), at.key("codex.out_stand"))
					if err := edit.DelTOMLTop(path, "model_provider", "model_catalog_json"); err != nil {
						return err
					}
					if err := edit.SetTOMLTop(path,
						edit.KV{Path: "openai_base_url", Value: at.codexURL()},
						edit.KV{Path: "model", Value: own},
					); err != nil {
						return err
					}
					stash(map[string]string{at.key("codex.joined"): "1"})
					return settle()
				}
			}
			// the ChatGPT account ran out (of its allowance and its
			// credits) after a magpie model was picked beside its sign-in:
			// the Codex app then sends nothing, a magpie model's turn
			// included, in a new thread or an old one (#540), so magpie
			// becomes Codex's provider as set does for an account already
			// out; and once the account has room again (or Codex is on
			// one with room), Codex's own models join magpie's again
			if m := get("model"); isMagpie(m) && !api() && chatGPT() {
				switch {
				case viaBase() && !asProvider() && out():
					return set(m)
				case asProvider() && stashShared()[at.key("codex.out")] == "1" && !out():
					return set(m)
				}
			}
			// a table taken away before (by an older magpie) comes back
			// while magpie is wired, for the threads that name it
			if wired() && viaBase() && !hasProvider() {
				if err := putProvider(); err != nil {
					return err
				}
			}
			// CC Switch's tables, left from before magpie took them over
			if wired() && routed() {
				if err := takeTables(); err != nil {
					return err
				}
			}
			switch {
			case asProvider() && ownCatalog(get("model_catalog_json")):
				// set up by a magpie from before #259: the threads started
				// on Codex's built-in provider reach magpie too
				if get("openai_base_url") == "" {
					if err := edit.SetTOMLTop(path, edit.KV{Path: "openai_base_url", Value: at.codexURL()}); err != nil {
						return err
					}
				}
				b := ownList()
				if cur, _ := edit.Read(catalogPath); string(cur) != string(b) {
					if err := writeOwnList(catalogPath, b); err != nil {
						return err
					}
				}
			case failingOver():
				// the list the gateway hands a Codex there for failover
				// alone: its own models, without magpie's (#1385)
				return codexStaleCache(filepath.Join(dir, "models_cache.json"), provider.CodexOwnListTag())
			case viaBase():
				if err := codexStaleCache(filepath.Join(dir, "models_cache.json"), provider.CodexListTag()); err != nil {
					return err
				}
			default:
				return nil
			}
			// a model that now has levels it had none of before (models.dev
			// synced, a vendor's list fetched) keeps an effort it takes
			if isMagpie(get("model")) {
				return settle()
			}
			// on a model of Codex's own (the app's last pick), the app's
			// picker still lists magpie's: the efforts they take now are
			// offered there too. A group that came to take max kept only
			// high in the app, with no control, while the config was on
			// Codex's own model (#2127)
			if routed() {
				return codexEnableEfforts(path, magpieModels("codex"))
			}
			return nil
		},
		Check: func() string {
			if namesMagpie() && noProvider() {
				return "Codex's config names magpie as its model_provider but has no [model_providers.magpie], so Codex can't load its config (\"Model provider `magpie` not found\"): magpie writes it back when it next syncs, or connect Codex again"
			}
			if !wired() {
				return ""
			}
			// a profile's settings win over the top level's, magpie's included
			if p := get("profile"); p != "" {
				t, err := edit.GetTOMLTable(path, "profiles."+p)
				if err != nil {
					return err.Error()
				}
				for _, k := range []string{"model", "model_provider", "openai_base_url", "model_catalog_json"} {
					if v, ok := t[k]; ok && v != get(k) {
						return "Codex's profile " + p + " sets its own " + k + " (" + v + "), which Codex takes over magpie's"
					}
				}
			}
			switch {
			case asProvider():
				t, err := edit.GetTOMLTable(path, "model_providers."+magpieID)
				if err != nil {
					return err.Error()
				}
				if t["base_url"] != at.v1() || !keyHolds(t["experimental_bearer_token"], at.gwKey()) || t["wire_api"] != "responses" {
					return "Codex's [model_providers.magpie] no longer points at magpie's gateway (" + at.v1() + ")"
				}
				if c := get("model_catalog_json"); !ownCatalog(c) {
					return "Codex's model_catalog_json is no longer magpie's list"
				}
				if _, err := os.Stat(catalogPath); err != nil {
					return "magpie's model list for Codex (" + catalogPath + ") is gone"
				}
			case viaBase():
				if u := get("openai_base_url"); strings.TrimSuffix(u, "/") != at.codexURL() {
					return "Codex's openai_base_url is " + u + ", not magpie's gateway at " + at.codexURL()
				}
			default:
				return "Codex's config no longer sends its model through magpie (no openai_base_url or model_provider of magpie's), so Codex asks OpenAI for a model OpenAI doesn't have"
			}
			for id, u := range ccSwitchTables() {
				if u != at.v1() {
					return "Codex's [model_providers." + id + "] sends to " + u + ", not magpie's gateway: a Codex thread started on it goes there when reopened, a magpie model picked in it too"
				}
			}
			return ""
		},
		// every prompt typed into Codex goes into history.jsonl
		LastUsed: func() time.Time { return lastJSONLTime(filepath.Join(dir, "history.jsonl"), "ts", "text") },
		// the Codex app writes no history.jsonl, but it and the TUI log each
		// model request they open
		Reached: func(since time.Time) (time.Time, string, bool) { return codexReached(dir, since) },
		// the app-server behind the Codex app (and every codex TUI) builds
		// its model list once, at start-up. Codex 0.162's TUI attaches to a
		// background app-server it starts once and leaves running (its
		// daemon_auto_start, on by default on every OS), so closing every
		// codex session keeps the old list: a new one shows Codex's own
		// models until that daemon is restarted, while the desktop app's
		// own app-server, restarted with the app, has magpie's (TJHHHH,
		// luci). With no codex session on it magpie restarts it itself
		// (codexDaemonNotice); with one, the user says when.
		Notice: func() string {
			var out []string
			if w := codexSubAgentsV1Warning(path, routed()); w != "" {
				out = append(out, w)
			}
			if at.spell == nil {
				if n, ok := codexDaemonNotice(path); ok {
					return strings.Join(append(out, n), " ")
				}
			}
			if codexRunning() {
				out = append(out, codexRestartAll)
			}
			return strings.Join(out, " ")
		},
		Fields: []Field{
			{
				Key: "model", Label: "model",
				Get:     func() string { return get("model") },
				Set:     set,
				Options: func(map[string]string) []Option { return modelOptions(true) },
			},
			{
				Key: "effort", Label: "effort",
				// unset reads as Default, which it is; the level Codex then
				// takes is on the Default option (Takes), not the value, so
				// picking Default doesn't read back as medium (lgtm)
				Get: func() string { return get("model_reasoning_effort") },
				// set, magpie's models start at it too when Codex is switched
				// to one, or at their nearest (relist, codexcat.ModelDefault):
				// it fell back to the middle of the road (Creat.wu on Discord)
				Set: func(v string) error {
					var err error
					if v == "" {
						err = edit.DelTOMLTop(path, "model_reasoning_effort")
					} else {
						err = edit.SetTOMLTop(path, edit.KV{Path: "model_reasoning_effort", Value: v})
					}
					if err != nil {
						return err
					}
					return relist()
				},
				// Default first, with the level Codex takes for the model
				// when none is set, as the catalog entry it reads says it
				Options: func(cur map[string]string) []Option { return codexEfforts(models(), cur["model"]) },
			},
			{
				// Codex lists only the first five models in the spawn_agent
				// tool it gives the model, its own ahead of magpie's (the
				// gateway names the rest there, spawnAgentModels), and a
				// subagent is put on one of magpie's here without the lead
				// naming it. It is only the model a
				// spawn that names none starts on: unset, the lead's own,
				// but a lead naming another in spawn_agent gets that one
				// (core/src/agent/child_config.rs), so the GUI says the
				// lead picks rather than "same as model" (willz on Discord)
				Key: "subagent", Label: "subagents", Quiet: true,
				Get: func() string { v, _ := subagent(); return v },
				Set: func(v string) error {
					if v == "" {
						return edit.DelTOMLKey(path, "agents", "default_subagent_model")
					}
					if isMagpie(v) && !routed() {
						return fmt.Errorf("pick a model through magpie for Codex first; its subagents can then have one of their own")
					}
					if err := edit.SetTOMLKey(path, "agents", "default_subagent_model", v); err != nil {
						return err
					}
					return dropSubEffort()
				},
				Options: func(map[string]string) []Option { return modelOptions(routed()) },
			},
			{
				// the model magpie puts every subagent on, whatever the
				// lead asked for in spawn_agent (willz on Discord): kept in
				// magpie's settings, not Codex's config, since the gateway
				// rewrites the subagent's request. Every model magpie serves
				// is offered (#1633: a lead on a chat provider, its
				// subagents wanted there too); a subagent whose task a
				// ChatGPT-backed lead sealed stays on the model asked for
				// unless the one set is a ChatGPT account's (the gateway).
				// It is the one setting for the subagents' model the GUI
				// shows: beside the subagents one above, the two read alike
				// and nobody could tell them apart (the owner). So a pick
				// here, Default too, takes away [agents]
				// default_subagent_model, and Default is then the lead's
				// model or the one it names, as it says. "lead"
				// (provider.CodexSubagentLead, the GUI's Same as the lead)
				// puts every subagent on the lead's own model, whatever it
				// names (cleverpigo on X)
				Key: "subagent_model", Label: "subagent model", Quiet: true,
				Get: provider.CodexSubagentModel,
				Set: func(v string) error {
					m, err := subagent()
					if err != nil {
						return err
					}
					if err := provider.SetCodexSubagentModel(v); err != nil || m == "" {
						return err
					}
					if err := edit.DelTOMLKey(path, "agents", "default_subagent_model"); err != nil {
						return err
					}
					return dropSubEffort()
				},
				Options: func(map[string]string) []Option {
					var out, groups []Option
					for _, e := range provider.CodexSubagentModels() {
						if e.Group != "" {
							groups = append(groups, Option{Value: e.ID, Label: e.Name, Note: "routing group · via magpie",
								Icon: e.Provider.Icon, Icons: e.Icons, Group: RoutingGroups, Ref: e.ID})
							continue
						}
						note := e.Provider.Name + " · via magpie"
						if a := e.Provider.Account; a != nil && a.User != "" {
							note = a.User + " · via magpie"
						}
						out = append(out, Option{Value: e.ID, Label: e.Name, Note: note, Icon: e.Provider.Icon,
							Group: e.Provider.Name, Ref: e.ID, Context: e.Context})
					}
					return append(out, groups...)
				},
			},
			{
				// [agents] default_subagent_reasoning_effort: unset, a
				// subagent runs at the session's effort, or at its model's
				// default when it has a model of its own
				Key: "subagent_effort", Label: "subagent effort", Quiet: true,
				Get: func() string { v, _ := subEffort(); return v },
				Set: func(v string) error {
					if v == "" {
						return edit.DelTOMLKey(path, "agents", "default_subagent_reasoning_effort")
					}
					if l := subEfforts(); len(l) > 0 && !contains(l, v) {
						return fmt.Errorf("Codex runs %s at %s, not %q: it would refuse to start a subagent", subModel(), strings.Join(l, ", "), v)
					}
					return edit.SetTOMLKey(path, "agents", "default_subagent_reasoning_effort", v)
				},
				Options: func(map[string]string) []Option {
					if e := subEfforts(); len(e) > 0 {
						return static(e...)
					}
					return static("low", "medium", "high", "xhigh")
				},
			},
			{
				// [memories] extract_model and consolidation_model, one
				// pick for both; Default removes them, for Codex's own
				Key: "memories", Label: "memories", Quiet: true,
				Get: func() string { v, _ := memories(); return v },
				Set: func(v string) error {
					if v == "" {
						return dropMemories()
					}
					if isMagpie(v) && !routed() {
						return fmt.Errorf("pick a model through magpie for Codex first; its memories can then be written with one of magpie's")
					}
					for _, k := range []string{"extract_model", "consolidation_model"} {
						if err := edit.SetTOMLKey(path, "memories", k, v); err != nil {
							return err
						}
					}
					return nil
				},
				Options: func(map[string]string) []Option { return modelOptions(routed()) },
			},
			{
				// how Codex takes magpie's models: beside its ChatGPT
				// sign-in (openai_base_url), magpie its provider only while
				// the Codex app holds the account (""); beside it always,
				// held or not (chatgpt); or with magpie as its provider,
				// the Codex app keeping its ChatGPT account while it has
				// room (requires_openai_auth, openaiAuth): Codex's
				// getAuthStatus has no token for a provider without it, and
				// the app lost its chats, projects and Chrome control
				// (#1824, Qyburn3 on X). Kept in the stash, where set("")
				// leaves it.
				Key: "login", Label: "sign-in", Quiet: true,
				Get: func() string { return stashShared()[at.key("codex.login")] },
				Set: func(v string) error {
					if v != "" && v != "api" && v != "chatgpt" {
						return fmt.Errorf("sign-in is api, chatgpt or empty (ChatGPT), not %q", v)
					}
					stash(map[string]string{at.key("codex.login"): v})
					m := get("model")
					switch {
					case v != "api" && codexOwnOf(m) != "":
						// back beside the sign-in, Codex's own model goes to
						// OpenAI itself again, not by a hop through magpie
						return set(codexOwnOf(m))
					case isMagpie(m):
						return set(m)
					case v == "api" && codexOwnViaMagpie(m) != "":
						// magpie API on one of Codex's own models: wired as
						// for magpie's, so its models are in Codex's picker
						return set(m)
					}
					return nil
				},
				Options: func(map[string]string) []Option {
					return []Option{
						{Value: "", Label: "ChatGPT", Note: "magpie's models join Codex's own; Codex stays signed in to ChatGPT, and while the Codex app blocks its account (out of allowance, no credits left) magpie is Codex's provider, so the app still sends"},
						{Value: "chatgpt", Label: "Always ChatGPT", Note: "as ChatGPT, and kept so while the Codex app blocks its account: magpie never becomes Codex's provider. The Codex app may then send nothing till the account has room; Codex CLI goes on through magpie"},
						{Value: "api", Label: "magpie API", Note: "magpie is Codex's provider, with magpie's models only; the Codex app keeps its ChatGPT account (its chats, projects and Chrome control) while the account has room"},
					}
				},
			},
		},
	}, path, catalogPath)
	// this machine's Codex: the address its config keeps is tried (Drift),
	// and moved to WSL's new one when that changed (#1013)
	if at.spell == nil {
		a.reach = at.base
		a.viaProxy = func() (bool, bool, string, string) { return codexThroughProxy(path, at.gw()) }
		a.move = func(from, to string) error { return codexMoveGateway(path, from, to) }
	}
	return a
}

// ccSwitchOfficial is the provider id CC Switch 3.17 to 3.20 gave the
// official ChatGPT account through its local proxy.
const ccSwitchOfficial = "cc-switch-official"

// packedSince and packedBudget bound codexOnto's move of the threads it
// rewrites: those with a turn in the last 30 days, 128 MB of them a call,
// another asked for at once while some are left. packedBudget is a var
// for a test.
const packedSince = 30 * 24 * time.Hour

var packedBudget int64 = 128 << 20

// ccSwitchProvider matches the ids of the provider tables CC Switch writes
// into Codex's config: "custom", and "cc-switch", "cc-switch-2"… from its
// older versions. Its "cc-switch-official" is its own proxy to OpenAI.
var ccSwitchProvider = lazyre.New(`^(custom|cc-switch(-[0-9]+)?)$`)

// codexBuiltinProvider is the providers Codex has built in, which a thread
// can name with no table in config.toml (codex-rs model-provider-info:
// built_in_model_providers); "ollama-chat" is one it no longer has, and
// tells the user how to fix itself.
var codexBuiltinProvider = map[string]bool{
	"openai": true, "amazon-bedrock": true, "amazon-bedrock-runtime": true,
	"ollama": true, "lmstudio": true, "ollama-chat": true,
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// The reasoning efforts the Codex app knows, in its order (the enum of its
// enabled-reasoning-efforts setting), and those it offers while the
// setting is unset.
var (
	codexAppEfforts   = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra", "persistent"}
	codexAppEffortsOn = []string{"low", "medium", "high", "xhigh", "ultra", "persistent"}
)

// codexEnableEfforts adds the efforts magpie's models take to the Codex
// app's [desktop] enabled-reasoning-efforts in config.toml: its model
// picker offers a model's efforts only when they are there, so a model's
// "max" or "minimal" was never offered (#310). The user's entries stay,
// only what is missing is added, and nothing is written when nothing is;
// an effort the app doesn't know is left out, as it would drop the whole
// setting for it. A [desktop] spelled some other way (inline, dotted
// keys) is left alone.
func codexEnableEfforts(path string, ms []catalog.Model) error {
	doc, err := codexTopOf(path)
	if err != nil {
		return nil
	}
	desktop, ok := doc.Desktop.(map[string]any)
	if !ok && doc.Desktop != nil {
		return nil
	}
	on := codexAppEffortsOn
	if desktop != nil {
		if tables, _ := edit.TOMLTables(path); !slices.Contains(tables, "desktop") {
			return nil
		}
		if v, ok := desktop["enabled-reasoning-efforts"]; ok {
			xs, ok := v.([]any)
			if !ok {
				return nil
			}
			on = nil
			for _, x := range xs {
				s, ok := x.(string)
				if !ok {
					return nil
				}
				on = append(on, s)
			}
		}
	}
	add := false
	out := slices.Clone(on)
	for _, e := range codexAppEfforts {
		if slices.Contains(on, e) {
			continue
		}
		for _, m := range ms {
			if slices.Contains(m.Efforts, e) {
				out, add = append(out, e), true
				break
			}
		}
	}
	if !add {
		return nil
	}
	q := make([]string, len(out))
	for i, e := range out {
		q[i] = strconv.Quote(e)
	}
	return edit.SetTOMLKey(path, "desktop", "enabled-reasoning-efforts", edit.Raw("["+strings.Join(q, ", ")+"]"))
}

// ownCodex is Codex's own models, narrowed to the ones ticked on its ChatGPT
// subscription in magpie when any are — the subscription switched off, its
// picks narrow nothing, as it serves no agent anything.
func ownCodex() []catalog.Model {
	ms := catalog.CodexShared() // only read (codexNamed), on every poll
	picks := codexPicks()
	if len(picks) == 0 {
		return ms
	}
	out := make([]catalog.Model, 0, min(len(picks), len(ms)))
	for _, m := range ms {
		if slices.Contains(picks, m.ID) {
			out = append(out, m)
		}
	}
	if len(out) == 0 {
		return ms
	}
	return out
}

// codexPicks is the models ticked on Codex's ChatGPT subscription, none
// while it is switched off; kept as the agents' lists are (builtModels):
// Find built every provider afresh to read them, on each poll of the
// Agents page, 4 MB a minute on a live magpie for the same few ids.
// Codex's own list (catalog.Codex, its models_cache.json) is read each
// time, as before.
func codexPicks() []string {
	return builtCodexPicks.shared("", func() []string {
		p, err := provider.Find("codex")
		if err != nil || p.Off {
			return nil
		}
		return slices.Clip(p.Models)
	})
}

var builtCodexPicks stampedLists[string]

// codexNamed is Codex's own models as its picker offers them, each under
// its name, as every model of magpie's is (viaMagpie): the picker drew
// the slug (gpt-6-astra) in front and the name after it in grey, the
// only list in magpie that did (nb5p on Discord). The value stays the
// slug Codex is set to.
func codexNamed(ms []catalog.Model) []Option {
	out := options(ms, "")
	for i, m := range ms {
		if m.Name != "" {
			out[i].Label = m.Name
		}
	}
	return out
}

// codexInMagpie marks Served those of Codex's own models that magpie's list
// shows Codex on a ChatGPT account of the user's (codex/gpt-5.5): its
// pickers leave out magpie's codex/… as only a hop (codexViaMagpieFor), so these
// are how they are offered, and the agent's own models switched off
// (provider.OwnModelsHidden) never takes them out. It did once a routing
// group gave the picker a catalog entry, and Codex's picker was the group
// alone (shiqi_40904 on Discord).
func codexInMagpie(own []Option) []Option {
	on := map[string]bool{}
	for _, m := range servedOnCodex.get("", codexServedModels) {
		on[m] = true
	}
	for i := range own {
		own[i].Served = on[own[i].Value]
	}
	return own
}

// servedOnCodex is codexServedModels kept as magpie's lists are
// (stampedLists): Codex's options, built per field of its Drift on every
// drift poll, built Codex's whole catalog each time for these few names
// (0.2 GB of the 2.6 GB a real config allocated over 20 polls).
var servedOnCodex stampedLists[string]

// codexServedModels is the models magpie's list shows Codex on a ChatGPT
// account of the user's.
func codexServedModels() []string {
	shown, _ := provider.CatalogFor("codex")
	var out []string
	for _, e := range shown {
		if provider.CodexOwn(e) {
			out = append(out, e.Model)
		}
	}
	return out
}

// codexViaMagpieFor is viaMagpieFor("codex") keeping the models magpie
// serves on a ChatGPT account that own, Codex's own list, leaves out: one
// Codex hides from its picker (gpt-6.1-terra) but the user ticked on the
// subscription was offered nowhere (ysm119 on Discord), as the hop is all
// that reaches it. Those come first, in own's group: a "Codex" section
// of their own beside it read as a second kind of model (ysm119).
func codexViaMagpieFor(own []Option) []Option {
	in := make(map[string]bool, 2*len(own))
	for _, o := range own {
		in[o.Value] = true
		if m := codexOwnOf(o.Value); m != "" {
			in[m] = true
		}
	}
	// one slice, hidden ones first: on every poll of Codex's model field
	via := viaMagpieShared("codex", "")
	out := make([]Option, 0, len(via))
	for _, o := range via {
		if strings.HasPrefix(o.Value, "codex/") && !in[codexOwnOf(o.Value)] {
			if len(own) > 0 {
				o.Group, o.GroupIcon = own[0].Group, own[0].GroupIcon
			}
			out = append(out, o)
		}
	}
	for _, o := range via {
		if !strings.HasPrefix(o.Value, "codex/") {
			out = append(out, o)
		}
	}
	return out
}

// codexOwnViaMagpie is the catalog id magpie serves one of Codex's own
// models by on a ChatGPT account of the user's ("codex/gpt-5.5"), those
// shown to Codex first; "" when none serves it.
func codexOwnViaMagpie(model string) string {
	if model == "" || isMagpie(model) {
		return ""
	}
	shown, _ := provider.CatalogFor("codex")
	for _, es := range [][]provider.Entry{shown, provider.Catalog()} {
		for _, e := range es {
			if a := e.Provider.Account; e.Group == "" && e.Model == model && a != nil && a.Agent == "codex" {
				return e.ID
			}
		}
	}
	return ""
}

// codexOtherModel is a model of magpie's that no ChatGPT account serves
// (a provider's or a key's own), the one magpie takes Codex over on while
// the account is out and cannot serve what was picked. "" when magpie has
// none.
func codexOtherModel() string {
	shown, _ := provider.CatalogFor("codex")
	for _, es := range [][]provider.Entry{shown, provider.Catalog()} {
		for _, e := range es {
			if a := e.Provider.Account; e.Group == "" && e.ID != "" && isMagpie(e.ID) && !provider.IsGroupRef(e.ID) && (a == nil || a.Agent != "codex") {
				return e.ID
			}
		}
	}
	return ""
}

// codexOwnOf is the model of Codex's own a catalog id serves on a ChatGPT
// account (codexOwnViaMagpie's way back); "" for any other.
func codexOwnOf(id string) string {
	if !isMagpie(id) || provider.IsGroupRef(id) {
		return ""
	}
	p, model, ok := provider.Resolve(id)
	if !ok || p.Account == nil || p.Account.Agent != "codex" {
		return ""
	}
	return model
}

// codexGatewayURL is where Codex's built-in OpenAI provider is pointed to
// reach magpie.
func codexGatewayURL() string { return gateway.URL() + gateway.CodexPath }

// codexKeptGateway is the gateway's URL for this machine's Codex: the
// address its config names when that is magpie's gateway on a host other
// than this one's loopback (openai_base_url at the Codex path, or
// [model_providers.magpie]'s base_url with magpie's key, that one first
// while magpie is Codex's provider), else gateway.URL(). It is http or
// https, on any port or the scheme's own, and may have a path before
// magpie's: a company's reverse proxy at https://ai.corp.example/magpie,
// or one on this machine at http://127.0.0.1:8788/<route> (#1985).
func codexKeptGateway(path string) string {
	kept := func(u, suffix string) string {
		base, ok := strings.CutSuffix(strings.TrimSuffix(strings.TrimSpace(u), "/"), suffix)
		if !ok || !onAnotherMachine(base) && !loopbackWithPath(base) {
			return ""
		}
		if p, err := url.Parse(base); err != nil || p.User != nil || p.RawQuery != "" || p.Fragment != "" {
			return ""
		}
		return base
	}
	base, _ := edit.GetTOMLTop(path, "openai_base_url")
	var table string
	if t, _ := edit.GetTOMLTable(path, "model_providers."+magpieID); ourKey(t["experimental_bearer_token"]) {
		table = kept(t["base_url"], "/v1")
	}
	// the one Codex is on first: magpie's table when it is the provider
	if p, _ := edit.GetTOMLTop(path, "model_provider"); p == magpieID && table != "" {
		return table
	}
	return cmp.Or(kept(base, gateway.CodexPath), table, gateway.URL())
}

// codexMoveGateway points Codex's config at to where it names magpie's
// gateway at from: openai_base_url and [model_providers.magpie]'s
// base_url, each only when it is at from; anything else is the user's.
func codexMoveGateway(path, from, to string) error {
	from, to = strings.TrimSuffix(from, "/"), strings.TrimSuffix(to, "/")
	if u, _ := edit.GetTOMLTop(path, "openai_base_url"); strings.HasPrefix(u, from+"/") {
		if err := edit.SetTOMLTop(path, edit.KV{Path: "openai_base_url", Value: to + strings.TrimPrefix(u, from)}); err != nil {
			return err
		}
	}
	if t, _ := edit.GetTOMLTable(path, "model_providers."+magpieID); ourKey(t["experimental_bearer_token"]) && strings.HasPrefix(t["base_url"], from+"/") {
		return edit.SetTOMLKey(path, "model_providers."+magpieID, "base_url", to+strings.TrimPrefix(t["base_url"], from))
	}
	return nil
}

// isCodexGatewayOn reports whether an openai_base_url is magpie's gateway
// reached at host, on whichever port it listened on then.
func isCodexGatewayOn(u, host string) bool {
	return strings.HasPrefix(u, "http://"+host+":") && strings.HasSuffix(strings.TrimSuffix(u, "/"), gateway.CodexPath)
}

// codexStaleCache ages Codex's cached model list if it isn't the one magpie
// would hand out now (its ETag carries the list's tag), so the next Codex to
// start asks the gateway again rather than showing the old one until the
// cache ages on its own; the rest of the cache stays as Codex wrote it.
func codexStaleCache(path, tag string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var c map[string]json.RawMessage
	if json.Unmarshal(b, &c) != nil {
		return nil
	}
	var etag string
	json.Unmarshal(c["etag"], &etag)
	if codexcat.Tagged(etag, tag) {
		return nil
	}
	old := time.Unix(0, 0).UTC().Format(time.RFC3339)
	var at string
	if json.Unmarshal(c["fetched_at"], &at) == nil && at == old {
		return nil
	}
	c["fetched_at"], _ = json.Marshal(old)
	out, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return edit.WriteAtomic(path, out)
}

// codexOnto moves the threads of the Codex home dir on the provider from
// to the provider to, where Codex lists and resumes them: in place when
// the ids are the same length (RelabelCodexThreads), a paginated one by
// its row at any age (RetagPagedCodexThreads), and the rest whose last
// turn is in the last packedSince rewritten with a copy kept, newest
// first and packedBudget bytes a call. It says the ids it moved, and more
// when the budget left some for another call. bringOver, bringBeside and
// lendOwn move with it.
func codexOnto(dir, from, to string) (ids []string, more bool) {
	if len(from) == len(to) {
		moved, err := sessions.RelabelCodexThreads(dir, from, to)
		ids = append(ids, moved...)
		if err != nil {
			log.Printf("codex: moving %s's threads to %s: %v", from, to, err)
		}
	}
	retagged, err := sessions.RetagPagedCodexThreads(dir, from, to)
	ids = append(ids, retagged...)
	if err != nil {
		log.Printf("codex: moving %s's paginated threads to %s: %v", from, to, err)
	}
	move := sessions.MovePackedCodexThreads
	if len(from) != len(to) {
		move = sessions.MoveRecentCodexThreads
	}
	packed, more, err := move(dir, from, to, time.Now().Add(-packedSince), packedBudget)
	for _, m := range packed {
		ids = append(ids, m.ID)
	}
	if err != nil {
		log.Printf("codex: moving %s's threads to %s: %v", from, to, err)
	}
	if len(ids) > 0 {
		log.Printf("codex: %d thread(s) started on %s moved to %s, where Codex resumes them", len(ids), from, to)
	}
	return ids, more
}

// codexThreadsAgain is the Sync codexThreadsLater asks for; a test
// replaces it. Set in init: a var's initializer naming SyncCatalogSoon
// would be an initialization cycle through the agents' list.
var codexThreadsAgain func()

func init() { codexThreadsAgain = SyncCatalogSoon }

// codexThreadsWait is how often codexThreadsLater looks again: within
// it of a thread going quiet, or of the Codex app letting it go, the
// Sync is asked for.
var codexThreadsWait = 15 * time.Second

// codexThreadsFor is how long codexThreadsLater keeps looking, in
// nanoseconds: a thread the Codex app keeps open all day waits for the
// next Sync after it. Read at each look, so a test ends a wait by
// setting it to 0.
var codexThreadsFor satomic.Int64

func init() { codexThreadsFor.Store(int64(12 * time.Hour)) }

var (
	codexThreadsMu      sync.Mutex
	codexThreadsPending = map[string]bool{}
)

// codexThreadsLater runs codexThreadsAgain once no thread of the Codex
// home dir on the provider p is being written to or held open
// (sessions.CodexThreadsBusyOn), for the threads a move off p left there:
// bringBack's on magpie as they were being written to (nb5p on Discord),
// and bringOver's and lendOwn's on OpenAI that the Codex app had open as
// magpie became Codex's provider. Those the app refuses to go on with
// ("This chat uses OpenAI, but your current provider is magpie",
// crypto0xfish on Discord) while it keeps them loaded, and once it lets
// them go (the chat closed, the app quit) it resumes them on the provider
// their row names, so they have to move by then; nothing else may Sync
// for hours. It looks every codexThreadsWait, which is a database read
// and a try of the locks Codex holds, for up to codexThreadsFor. One wait
// at a time for each home and provider: another call while one is
// pending adds none.
func codexThreadsLater(dir, p string) {
	key := dir + "\x00" + p
	codexThreadsMu.Lock()
	if codexThreadsPending[key] {
		codexThreadsMu.Unlock()
		return
	}
	codexThreadsPending[key] = true
	codexThreadsMu.Unlock()
	done := func() {
		codexThreadsMu.Lock()
		delete(codexThreadsPending, key)
		codexThreadsMu.Unlock()
	}
	wait := codexThreadsWait
	go func() {
		for start := time.Now(); time.Since(start) < time.Duration(codexThreadsFor.Load()); {
			time.Sleep(wait)
			if !sessions.CodexThreadsBusyOn(dir, p) {
				// let go first: the Sync it asks for may find another
				// thread busy and ask again
				done()
				codexThreadsAgain()
				return
			}
		}
		done()
	}()
}

// codexUsedUp reports whether the ChatGPT account Codex is signed in to
// is held by the Codex app, out of its allowance with no credits to go on
// with (provider.CodexUsedUpKnown). A var so tests can say.
//
// What is known is used, never a reading asked for here: this runs from
// SyncCatalog, which a write's own request runs before it answers (a
// routing group's Save), and asking waits on the vendor — 1.1s of a
// user's Save. KeepOnAnAccountWithRoom asks on its own loop with the time
// for it, and tells the catalog when the answer changes.
var codexUsedUp = func() bool {
	return provider.CodexUsedUpKnown()
}

// codexFailover reports whether Codex is signed in to a ChatGPT account
// with more of its accounts on in magpie, behind it, and the user hasn't
// asked magpie to only switch its sign-in (provider.SwitchOnly).
func codexFailover() bool {
	// the user had magpie only switch Codex's own sign-in (#1907)
	if provider.SwitchOnly("codex") {
		return false
	}
	for _, p := range provider.Accounts() {
		if p.Account != nil && p.Account.Agent == "codex" {
			return len(p.AlsoOn()) > 0
		}
	}
	return false
}

// EndCodexFailover takes account failover out of this machine's Codex
// as the magpie serving its gateway quits (EndFailover): Codex goes
// straight to OpenAI again rather than retrying a gateway that's gone.
// The app-server codex sessions share read the old base URL as it started
// and keeps it, and KeepCodexDaemonCurrent ends with magpie, so it is
// restarted here, when no session is on it (thisisvainxx on X: back on
// its own config, Codex took "several restarts"). ended says it took
// failover out: a thread open in the Codex app still sends to the
// gateway's address, which the caller leaves a relay at (codexrelay).
func EndCodexFailover() (ended bool, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return false, err
	}
	a := codex(home)
	if !a.FailingOver() {
		return false, nil
	}
	if err := a.EndFailover(); err != nil {
		return false, err
	}
	codexDaemonCheck(a.Path)
	return true, nil
}

// TakeCodexLeftovers takes out of this machine's Codex config what magpie
// wrote that no Disconnect could name: the base URL at magpie's gateway,
// and magpie as its provider, where the marks magpie keeps them by are
// gone with magpie's own files (#3866). It answers whether it changed
// anything.
func TakeCodexLeftovers() (bool, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return false, err
	}
	return codex(home).TakeLeftovers()
}

// RelayOpenCodex leaves a relay at the gateway's address for the Codex
// processes running as magpie quits (codexrelay.Start), when the address
// is this computer's own and Codex is running at all.
func RelayOpenCodex() error {
	u, err := url.Parse(gateway.URL())
	if err != nil {
		return err
	}
	if ip := net.ParseIP(u.Hostname()); !strings.EqualFold(u.Hostname(), "localhost") && (ip == nil || !ip.IsLoopback()) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	pids := codexPIDs(ctx)
	if len(pids) == 0 {
		return nil
	}
	return startRelay(u.Host, pids)
}

// startRelay and codexPIDs are vars so tests start no relay and list
// Codex processes of their own.
var (
	startRelay = codexrelay.Start
	codexPIDs  = provider.CodexPIDs
)

// CodexDesktopList is the list a Codex beside its ChatGPT sign-in is
// handed, as the file the Codex app reads it from (deskList); ok false
// while it isn't known. Set to gateway.CodexDesktopCatalog; nil writes
// none.
var CodexDesktopList func() (b []byte, ok bool)

// CodexOwnOnly reports whether the Codex that asks the gateway for its
// model list there (/backend-api/codex/models) reaches magpie for account
// failover alone (FailingOver), to be handed only its own models (#1385).
// That is this machine's Codex; a request doesn't say whether it came from
// a Codex in a WSL distro instead, so one there connected to magpie (what
// magpie set on it, or its joined/beside mark in the stash) keeps magpie's
// models in the list. No distro is probed for it.
func CodexOwnOnly() bool {
	home, err := os.UserHomeDir()
	if err != nil || !codex(home).FailingOver() {
		return false
	}
	appliedMu.Lock()
	set := appliedLoad()
	appliedMu.Unlock()
	for id := range set {
		if strings.HasPrefix(id, "codex@wsl:") {
			return false
		}
	}
	// a distro's name may have dots of its own (Ubuntu-24.04)
	for k, v := range stashLoad() {
		if strings.HasPrefix(k, "codex@wsl:") && v == "1" && (strings.HasSuffix(k, ".joined") || strings.HasSuffix(k, ".beside")) {
			return false
		}
	}
	return true
}

// CodexSideModel is the model this machine's Codex is set to when it is
// one of magpie's, picked beside its ChatGPT sign-in (SideModel), for the
// gateway to answer the Codex app's side turns on (gateway.CodexSideModel).
func CodexSideModel() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return codex(home).SideModel()
}

// CodexSideSet says whether this machine's Codex config names the model
// for a side call itself: "memories" for [memories] extract_model or
// consolidation_model. A config
// that can't be read counts as set, so the gateway leaves the call as Codex
// sent it (gateway.CodexSideSet).
func CodexSideSet(which string) bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return true
	}
	path := filepath.Join(here(home).codexHome(), "config.toml")
	switch which {
	case "memories":
		t, err := edit.GetTOMLTable(path, "memories")
		return err != nil || t["extract_model"] != "" || t["consolidation_model"] != ""
	}
	return true
}

// codexChatGPT reports whether Codex is signed in to a ChatGPT account:
// its built-in provider then runs and asks for the model list at
// openai_base_url. Signed in with an API key alone, it asks for none.
func codexChatGPT(dir string) bool {
	signedIn, _ := codexChatGPTKnown(dir)
	return signedIn
}

// codexChatGPTKnown is codexChatGPT with whether the read said anything:
// known is false for an auth.json caught while Codex rewrites it in place
// (empty, cut off) or a keychain read that failed, which is no answer
// (provider.CodexSignInKnown). Wiring goes by known alone: taking such a
// read for "no ChatGPT sign-in" made magpie Codex's provider, and the
// Codex app showed "magpie" for its account and magpie's catalog for its
// models, on and off (lucy on Discord, v0.1.1394).
func codexChatGPTKnown(dir string) (signedIn, known bool) {
	if settings.AccountDiscoveryOff() {
		return false, true
	}
	// forced_login_method = "api" in Codex's own config (another tool or
	// guide writes it; magpie never does): Codex won't use a ChatGPT
	// sign-in then. The TUI logs it out on start and the app server reads
	// no account (openai/codex login/src/auth/manager.rs
	// enforce_login_restrictions, load_auth), so wired beside it the
	// Codex app asked for a sign-in and offered an API key alone (Tiger on
	// Discord: "重启 codex 之后登不回去了"). Counted as no sign-in, magpie
	// is the provider and Codex needs none. The line is the user's own and
	// stays: taking it out is how they sign in to ChatGPT again.
	if v, ok := edit.GetTOMLTop(filepath.Join(dir, "config.toml"), "forced_login_method"); ok && strings.EqualFold(strings.TrimSpace(v), "api") {
		return false, true
	}
	var a struct {
		Tokens *struct {
			Access string `json:"access_token"`
		} `json:"tokens"`
	}
	// auth.json, or the macOS keychain item when Codex's config.toml
	// keeps the sign-in there (#2367)
	b, ok, known := provider.CodexSignInKnown(dir)
	if !ok {
		return false, known
	}
	if json.Unmarshal(b, &a) != nil {
		// valid JSON that isn't an object: not a sign-in Codex wrote
		return false, false
	}
	if a.Tokens == nil || a.Tokens.Access == "" {
		return false, true
	}
	// a sign-in OpenAI refused to refresh for good (revoked, or failed for
	// good, #4308): Codex keeps its auth.json, but beside it every turn
	// fails at Codex's workspace routing discovery, magpie's models too.
	// None to magpie, as forced api above, until it is signed in again
	// (another refresh token). A record that can't be read says nothing.
	if gone, known := provider.CodexSignInGone(dir, b); !known || gone {
		return false, known
	}
	return true, true
}

// codexReached reads Codex's newest model request since a time — the
// newest day of it at most — from the log database the Codex app and the
// TUI share: the address it opened, and whether that was refused (nothing
// listening there). Zero when none is logged.
//
// Every Drift asks it, so it is read again only when the database or its
// -wal changed, or the day it covers moved past what it found (#1665: a
// read per ask was ~2,500 opens of logs_2.sqlite in 25s of a start, each
// one file system work a Windows filter driver adds its own to).
func codexReached(dir string, since time.Time) (at time.Time, to string, refused bool) {
	logs, _ := filepath.Glob(filepath.Join(dir, "logs_*.sqlite"))
	if len(logs) == 0 {
		return
	}
	slices.Sort(logs)
	path := logs[len(logs)-1]
	key := reachedKey{path: path, since: since.Unix(), db: stampOf(path), wal: stampOf(path + "-wal")}
	reachedMu.Lock()
	if c, ok := reachedMemo[dir]; ok && c.key == key && time.Since(c.read) < reachedFor &&
		(c.at.IsZero() || time.Since(c.at) < 24*time.Hour) {
		reachedMu.Unlock()
		return c.at, c.to, c.refused
	}
	reachedMu.Unlock()
	at, to, refused = codexReachedRead(path, since)
	reachedMu.Lock()
	reachedMemo[dir] = reachedRead{key: key, read: time.Now(), at: at, to: to, refused: refused}
	reachedMu.Unlock()
	return
}

// reachedFor is how long a read of Codex's log stands while its files say
// nothing changed: a file system whose times are coarse can miss a write
// that kept the size.
const reachedFor = 15 * time.Second

type reachedKey struct {
	path    string
	since   int64
	db, wal fileStamp
}

type fileStamp struct{ size, mod int64 }

func stampOf(path string) fileStamp {
	fi, err := os.Stat(path)
	if err != nil {
		return fileStamp{size: -1}
	}
	return fileStamp{size: fi.Size(), mod: fi.ModTime().UnixNano()}
}

type reachedRead struct {
	key     reachedKey
	read    time.Time
	at      time.Time
	to      string
	refused bool
}

var (
	reachedMu   sync.Mutex
	reachedMemo = map[string]reachedRead{}
	// reachedOpens counts the reads of the database, for the tests
	reachedOpens satomic.Int64
)

func codexReachedRead(path string, since time.Time) (at time.Time, to string, refused bool) {
	reachedOpens.Add(1)
	db, err := provider.OpenReadOnly(path)
	if err != nil {
		return
	}
	defer db.Close()
	from := max(since.Unix(), time.Now().Add(-24*time.Hour).Unix())
	rows, err := db.Query(`SELECT ts, feedback_log_body FROM logs
		WHERE ts > ? AND target = 'codex_api::endpoint::responses_websocket'
		ORDER BY ts DESC, ts_nanos DESC, id DESC LIMIT 20`, from)
	if err != nil {
		return
	}
	defer rows.Close()
	// newest first: a refusal comes before the attempt it answers
	failedAt := map[string]bool{}
	for rows.Next() {
		var ts int64
		var body string
		if rows.Scan(&ts, &body) != nil {
			continue
		}
		if _, u, ok := strings.Cut(body, "failed to connect to websocket: "); ok {
			if _, u, ok := strings.Cut(u, "url: "); ok && (strings.Contains(body, "Connection refused") || strings.Contains(body, "os error 10061")) {
				failedAt[strings.TrimSpace(u)] = true
			}
			continue
		}
		if _, u, ok := strings.Cut(body, "connecting to websocket: "); ok {
			u = strings.TrimSpace(u)
			return time.Unix(ts, 0), u, failedAt[u]
		}
	}
	return
}
