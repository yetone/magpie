package agent

// DeepSeek Harness (dsh) boots from its shipped rows and then applies a
// personal patch list: a YAML list of {id, config} entries, each replacing
// the whole config of the row with that id. Since 0.1.5 every profile has
// its own, ~/.dsh/profiles/<name>/cordis.patch.yml (web and desktop read it
// live); before that there was one, ~/.dsh/config.yaml.
//
// Since 0.1.5 magpie is one of dsh's custom model providers: a route named
// magpie in the llm-pi-ai entry's providers (what dsh's Models page writes
// for "Add model provider"), with the gateway as its endpoint, the catalog
// as its models and, as the key, a credential named in apiKeyEnv, which
// magpie puts in ~/.dsh/.env and dsh's own key store (the desktop app reads
// only the store); agent-default-model names the model new
// sessions start on. dsh's own DeepSeek row (llm-deepseek) is left to dsh:
// taken over, it read as DeepSeek in dsh's Models page, and a DeepSeek key
// entered there went to the gateway under magpie's credential name, over
// magpie's own (Discord: 集成进 dsh 会被 dsh 覆写). A route of the user's in
// the same entry stays as it is, and so does one dsh adds beside magpie's.
//
// Before 0.1.5, which has no custom providers, magpie took over
// llm-deepseek, with the key in the entry, and set agent-loop's main agent
// (the TUI) and api-gateway's route (`dsh -p`, `dsh web`). Entries magpie
// writes whole are marked as magpie's, the user's own entries stay as they
// are, and one magpie replaces is stashed and put back when it steps out.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/maphash"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	satomic "sync/atomic"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/beat"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/provider"
)

const dshMark = "# magpie"

// dshKeyRef is the credential dsh signs gateway requests with. It is not
// dshOldKeyRef, the one magpie's llm-deepseek entry named: dsh's Models page
// showed that entry as DeepSeek, so a DeepSeek key entered there may sit in
// dsh's own store under that name, which dsh reads over .env.
const (
	dshKeyRef    = "MAGPIE_GATEWAY_KEY"
	dshOldKeyRef = "MAGPIE_API_KEY"
)

// dshRoute is magpie's route among dsh's custom providers, and the
// provider agent-default-model names for a model through magpie.
const dshRoute = magpieID

// dshPiRow is the row custom providers are configured on, and its plugin.
const (
	dshPiRow    = "llm-pi-ai"
	dshPiPlugin = "@deepseek-ai/dsh-llm-pi-ai"
)

// dshModels are the models dsh reaches on its own, as it ships them — under
// its own name, not DeepSeek: magpie's DeepSeek provider is a group of that
// name too, and two would read as one listed twice (#115).
//
// They are @deepseek-ai/dsh-llm-deepseek's DEFAULT_MODELS, ids and names as
// dsh has them: deepseek-flash and deepseek-v4-pro since 0.1.6, with
// deepseek-flash named "DeepSeek-V4.1-Flash" since 0.2.1 ("DeepSeek-V41-
// Flash" from 0.1.5 to 0.2.0). deepseek-v4-flash was dsh's up to 0.1.5 only;
// a dsh of today answers it "Select an available model" (Kylin_Cloud on X).
// A profile that still names it keeps it: magpie doesn't rewrite it.
var dshModels = []Option{
	{Value: "deepseek-flash", Label: "DeepSeek-V4.1-Flash", Icon: "deepseek-color", Group: "DeepSeek Harness"},
	{Value: "deepseek-v4-pro", Label: "DeepSeek-V4-Pro", Icon: "deepseek-color", Group: "DeepSeek Harness"},
}

func dsh(home string) *Agent { return dshAt(here(home)) }

// dshIn is DeepSeek Harness in a WSL distro (see wsl.go).
func dshIn(at place) *Agent { return dshAt(at) }

// dshAt is DeepSeek Harness with its home at at.home, reaching the gateway
// as at does. A distro's DSH_HOME can't be read, so there it is ~/.dsh.
func dshAt(at place) *Agent {
	dir := at.getenv("DSH_HOME")
	if dir == "" {
		dir = at.join(".dsh")
	}
	// DeepSeek Harness Desktop, on this machine (a distro has none)
	desktop := func() bool { return at.spell == nil && (&Agent{ID: "dsh"}).HasApp() }
	// the patch list magpie makes for a dsh with no profile yet: the desktop
	// app's, else the web profile's; "" for a dsh before profiles, which
	// reads config.yaml (liby233: a dsh 0.2 in WSL that hadn't booted a
	// profile yet was wired in config.yaml, which it never reads)
	fresh := func() string {
		if desktop() {
			return dshDesktopFile(dir)
		}
		version := at.version
		if at.spell == nil {
			version = dshVersion()
		}
		if dshBeforeProfiles(dir, version, at.cold) {
			return ""
		}
		return dshWebFile(dir)
	}
	// the desktop app's profile is the one whose start is shown (#5222)
	lead := func() string {
		if desktop() {
			return "desktop"
		}
		return ""
	}
	a := dshOn(at, dir, "dsh", fresh, lead)
	a.ID, a.Name, a.Icon, a.Aliases = "dsh", "DeepSeek Harness", "deepseek-color", []string{"deepseek-harness"}
	a.UA = []string{"deepseek-harness"}
	a.Bin = "dsh"
	// the desktop app installed and not opened yet has no ~/.dsh (#2279)
	a.sign = desktop
	a.Notice = func() string {
		var notes []string
		if desktop() && dshUnopened(dir) {
			// nothing of dsh's runs on it yet to be restarted
			notes = append(notes, noticeDsh5.String())
		} else if len(dshProfiles(dir)) > 0 {
			// a session dsh already has keeps the model it was on,
			// running or not: agent-default-model is read for new
			// ones only (#2359)
			notes = append(notes, noticeDsh1.String())
		} else if Running(`DeepSeek Harness\.app/`, `(^|/)dsh( |$)`, `(^|/)deepseek-harness( |$)`) {
			// deepseek-harness is the desktop app's executable on Linux
			notes = append(notes, noticeDsh2.String())
		}
		if dshSearchesHere(dir) && !gateway.CanSearch() {
			notes = append(notes, noticeDsh3.String())
		}
		// only before 0.1.5 does magpie go through llm-deepseek
		if !dshModern(dir, fresh) && dshSettingsEndpoint(filepath.Join(dir, "settings.yaml")) {
			notes = append(notes, noticeDsh4.String())
		}
		return strings.Join(notes, " ")
	}
	// what magpie made for a desktop app never opened goes (#2279)
	a.Restore = func() error {
		if err := dshTidyUnopened(dir); err != nil {
			return err
		}
		return dshTidyUnbooted(dir)
	}
	return a
}

// dshModern says a dsh home's patch lists are its profiles' (dsh 0.1.5
// on): it has one, or fresh names the one magpie makes.
func dshModern(dir string, fresh func() string) bool {
	return len(dshProfilesHeld(dir)) > 0 || fresh() != ""
}

// dshOn is what a dsh home at dir is wired by, for the agent whose models
// list names (see dshListFor): fresh is the patch list magpie makes there
// while it has no profile ("" for config.yaml), and lead the profile whose
// start is shown ("" for the first). The caller names the agent, and gives
// it its sign, notice and restore.
func dshOn(at place, dir, list string, fresh, lead func() string) *Agent {
	// the gateway's address as dsh's route has it: behind the prefix a
	// local proxy took it through, when it did (DshThrough)
	gw := func() string {
		if at.cold {
			return at.gw()
		}
		return dshReach(dir, at.gw())
	}
	// profiles' patch lists, or one magpie makes, are what dsh reads
	modern := func() bool { return dshModern(dir, fresh) }
	path := filepath.Join(dir, "config.yaml")
	// a stopped distro's folder is never listed: that starts it (#3896)
	var files []string
	if !at.cold {
		files = dshProfiles(dir)
	}
	if len(files) > 0 {
		path = files[0]
	} else if f := fresh(); f != "" {
		path = f
	}
	joined := func() bool {
		files := dshProfilesHeld(dir)
		if len(files) == 0 {
			return false
		}
		_, items, err := dshLook(files[0]) // dshOver changes a copy
		return err == nil && dshWired(dshOver(dir, items))
	}
	a := &Agent{
		Spelled: prefixed,
		Dir:     dir, Path: path,
		Sync:  func() error { return dshSyncFresh(at, dir, gw(), fresh) },
		Check: func() string { return dshCheck(dir, gw()) },
		// a model of DeepSeek's own picked in dsh (saved in its settings)
		// leaves magpie's route in its list: dsh is still connected, its
		// /model has magpie's models beside DeepSeek's (Fate on Discord:
		// DSH went back to Not connected)
		Joined: joined,
		// connected so, Disconnect takes the route out, the start magpie
		// wrote and its key; the model picked in dsh stays its own
		Unwire: func() error {
			if usesMagpie(dshGet(dir, "")) {
				return nil // the model set back does it all
			}
			dshWrites.Lock()
			defer dshWrites.Unlock()
			for _, f := range dshProfiles(dir) {
				if err := dshKeepPick(f); err != nil {
					return err
				}
				if err := dshSetFile(f, "", true, nil, gw()); err != nil {
					return err
				}
			}
			if err := dshMirrorHome(dir, nil, gw(), true); err != nil {
				return err
			}
			if err := dshSettingsAgain(dir, nil, gw()); err != nil {
				return err
			}
			return dshEnv(at, dir, gw(), false, false)
		},
		Fields: []Field{{
			Key: "model", Label: "model",
			Get: func() string { return dshGet(dir, lead()) },
			Set: func(v string) error {
				// the desktop app not opened yet, or a dsh that hasn't
				// booted a profile: the profile is made as dsh makes it,
				// and dsh keeps the patch list it finds (#2279, liby233)
				if v != "" && len(dshProfiles(dir)) == 0 {
					if f := fresh(); f != "" {
						if err := dshMakeProfile(f); err != nil {
							return err
						}
					}
				}
				return dshSet(at, dir, v, gw())
			},
			Options: func(map[string]string) []Option {
				own := append([]Option{}, dshModels...)
				// since 0.1.5 dsh's own go to DeepSeek on its own key, its
				// llm-deepseek row left to it, and say so as every agent's
				// own do (EZN7L2C3, #955); before, magpie took that row over
				if modern() {
					for i := range own {
						own[i].Direct = "DeepSeek"
					}
				}
				return withVia(own, list, magpieID+"/")
			},
		}, {
			// the thinking effort sessions start with: agent-default-model's
			// reasoningEffort since 0.1.5 (a model through magpie offers its
			// own levels), llm-deepseek's before (off, low, high or max;
			// dsh's own default is high)
			Key: "effort", Label: "thinking",
			Get: func() string { return dshGetEffort(dir, lead()) },
			Set: func(v string) error { return dshSetEffort(dir, v, gw(), lead()) },
			Options: func(cur map[string]string) []Option {
				// a model through magpie that lists no levels has none in
				// dsh (its route gives it no reasoningEfforts), so none is
				// offered: dsh's own four were, and each was turned away
				if ref, ok := strings.CutPrefix(cur["model"], magpieID+"/"); ok && modern() {
					return static(dshLevels(list, ref)...)
				}
				return static(dshEfforts...)
			},
		}},
	}
	if list == "dsh" {
		a.Fields = append(a.Fields, dshCompactField(func() bool { return !at.cold && joined() }, list))
	}
	a.through = func(prefix string) error { return dshThrough(at, dir, prefix, fresh) }
	return a
}

// dshCompactField picks the model DeepSeek Harness compacts its context
// on, whatever model the session is on (#5395): unset, dsh compacts on the
// session's latest model, so a switch away from one whose provider is out
// of its limit failed on its compaction. dsh's own setting for it is in
// each agent preset's whole config, which a profile patch can only replace
// whole, so it is magpie's setting (settings.DshCompactModel), read by
// the gateway on each request (gateway dshCompactTo): Live, and a change
// needs no restart. Offered while dsh is on magpie's route; DS Bot, whose
// requests the gateway can't tell from dsh's, has none of its own. Its
// models are spelt as the model field's, magpie/<id>, so `magpie dsh
// deepseek/flash` is still the model (the CLI gives a bare value to the
// first field offering it), and either spelling is taken. joined looks at
// nothing at a cold place: a stopped distro's files start it (#3896).
func dshCompactField(joined func() bool, list string) Field {
	return Field{
		Key: "compact_model", Label: "compaction model", Quiet: true, Live: true,
		Get: func() string {
			if m := provider.DshCompactModel(); m != "" {
				return magpieID + "/" + m
			}
			return ""
		},
		Set: func(v string) error {
			return provider.SetDshCompactModel(strings.TrimPrefix(v, magpieID+"/"))
		},
		Options: func(map[string]string) []Option {
			if !joined() {
				return nil
			}
			return viaMagpie(list, magpieID+"/")
		},
	}
}

// dshApps are the homes of the apps that run a dsh of their own on a home
// apart from ~/.dsh (DS Bot's), by folder, each with the id of the agent
// whose model list magpie writes there: a model taken out of DS Bot's list
// in magpie is not written into its home. Filled as each agent is built.
var dshApps sync.Map

// dshListFor is the agent whose models magpie writes into the dsh file or
// home at path: the app's whose home holds it, else dsh's.
func dshListFor(path string) string {
	id := "dsh"
	if path == "" {
		return id
	}
	p := filepath.Clean(path)
	dshApps.Range(func(k, v any) bool {
		if rel, err := filepath.Rel(k.(string), p); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			id = v.(string)
			return false
		}
		return true
	})
	return id
}

// dshProfiles are the profiles' patch lists (dsh 0.1.5 on), web's first.
func dshProfiles(dir string) []string {
	files, _ := filepath.Glob(filepath.Join(dir, "profiles", "*", "cordis.patch.yml"))
	var web, rest []string
	for _, f := range files {
		if filepath.Base(filepath.Dir(f)) == "web" {
			web = append(web, f)
		} else {
			rest = append(rest, f)
		}
	}
	return append(web, rest...)
}

// dshProfilesHeld is dshProfiles looked at once while a request holds
// what it reads (provider.Held), for the looks a state poll makes for
// each row (dshModern, Joined, dshLead): each listed the profiles' folder
// again, ~2.5 MB a 40 polls. Read only; a write goes through
// dshProfiles.
func dshProfilesHeld(dir string) []string {
	return provider.Held("dsh profiles\x00"+dir, func() []string { return dshProfiles(dir) })
}

// dshProfileTemplate is the patch list dsh writes for a new profile
// (PROFILE_PATCH_TEMPLATE in DeepSeek Harness Desktop 0.2.0-rc.2's
// lib/main.js, initProfile).
const dshProfileTemplate = "# Your patch layer for this dsh profile, applied after every bundle layer:\n" +
	"# a top-level YAML array of loader patch entries (id-targeted config\n" +
	"# overrides, disables, and insert lists; `!!js` expressions allowed).\n" +
	"[]\n"

// dshDesktopFile is the desktop app's profile's patch list: the app's
// profile is $DSH_HOME/profiles/desktop (resolveDesktopPaths).
func dshDesktopFile(dir string) string {
	return filepath.Join(dir, "profiles", "desktop", "cordis.patch.yml")
}

// dshWebFile is the web profile's patch list, $DSH_HOME/profiles/web: the
// profile `dsh web` boots, the first of dsh's shipped ones (web, headless,
// acp) that magpie makes for a dsh CLI that has booted none yet. A profile
// dsh makes later is given magpie's route by the sync (dshFillNewProfiles).
func dshWebFile(dir string) string {
	return filepath.Join(dir, "profiles", "web", "cordis.patch.yml")
}

// dshProfilesSince is the first dsh whose patch lists are its profiles'.
// That is every dsh on npm: 0.0.1-rc.1, the first, already boots
// $DSH_HOME/profiles/<name>, and none of 0.0.1-rc.1, 0.0.1-rc.5,
// 0.1.0-rc.8, 0.1.1-rc.1/rc.2, 0.1.2-rc.1, 0.1.3-alpha.2 or 0.2.1-rc.5
// reads a config.yaml. A dsh saying an older version is one from before.
const dshProfilesSince = "0.0.1-rc.1"

// dshVersion is the version of the dsh CLI on this machine, "" when there is
// none or it didn't say; a var for tests.
var dshVersion = func() string { return (&Agent{ID: "dsh", Bin: "dsh"}).InstalledVersion() }

// dshBeforeProfiles says a dsh with no profile at dir is one from before
// profiles, which reads ~/.dsh/config.yaml: by its version where that is
// known, else only where config.yaml holds an entry that isn't magpie's
// (what such a dsh, or its user, wrote there). A dsh since profiles makes
// none until one is first booted, and reads no config.yaml at all. cold is
// a stopped distro's, whose files are not read.
func dshBeforeProfiles(dir, version string, cold bool) bool {
	if version != "" {
		return Newer(dshProfilesSince, version)
	}
	if cold {
		return false
	}
	_, items, err := dshRead(filepath.Join(dir, "config.yaml"))
	return err == nil && slices.ContainsFunc(items, func(it dshItem) bool { return !it.magpie })
}

// dshMakeDesktop makes the desktop app's patch list, as dsh's template,
// for an app installed and never opened (#2279, liangjinyuan-sihan): with
// no profile at all magpie had nowhere to write, and DeepSeek Harness
// Desktop has no ~/.dsh until its first start. The app's initProfile
// writes each of a profile's files only where it is missing ("Existing
// files are never touched"), so on its first start it adds its
// package.json and pnpm-workspace.yaml beside this one and keeps
// magpie's entries. The folder is owner-only, as the app makes it.
func dshMakeDesktop(dir string) error { return dshMakeProfile(dshDesktopFile(dir)) }

// dshMakeProfile makes the patch list f of a profile dsh hasn't made yet, as
// dsh's template; dsh's initProfile adds the profile's other files beside
// it on its first boot and keeps it.
func dshMakeProfile(f string) error {
	if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(f); err == nil {
		return nil
	}
	return os.WriteFile(f, []byte(dshProfileTemplate), 0o644)
}

// dshUnopened says the desktop app's profile is one magpie made and the
// app hasn't started on yet: it has no package.json, which the app's first
// start writes.
func dshUnopened(dir string) bool { return dshUnbooted(dshDesktopFile(dir)) }

// dshUnbooted says the profile whose patch list is f is one magpie made and
// dsh hasn't booted yet: it has no package.json, which dsh's first boot of
// it writes (initProfile).
func dshUnbooted(f string) bool {
	if !isFile(f) {
		return false
	}
	_, err := os.Stat(filepath.Join(filepath.Dir(f), "package.json"))
	return errors.Is(err, os.ErrNotExist)
}

// dshTidyUnopened takes out, on Disconnect, what magpie made for a desktop
// app never opened since: the profile's patch list back to dsh's template
// with nothing beside it, and the folders that leaves empty. A patch list
// holding anything else, or a profile the app has started on, stays.
func dshTidyUnopened(dir string) error { return dshTidyMade(dir, dshDesktopFile(dir)) }

// dshTidyUnbooted is dshTidyUnopened for the web profile magpie made for a
// dsh CLI that has booted none (liby233).
func dshTidyUnbooted(dir string) error { return dshTidyMade(dir, dshWebFile(dir)) }

// dshTidyMade takes out the profile patch list f magpie made, while dsh has
// not booted it and it is dsh's template alone, and the folders that leaves
// empty.
func dshTidyMade(dir, f string) error {
	if !dshUnbooted(f) {
		return nil
	}
	b, err := os.ReadFile(f)
	if err != nil || strings.ReplaceAll(string(b), "\r\n", "\n") != dshProfileTemplate {
		return nil
	}
	if es, err := os.ReadDir(filepath.Dir(f)); err != nil || len(es) != 1 {
		return nil
	}
	if err := os.Remove(f); err != nil {
		return err
	}
	// only folders left empty: os.Remove refuses one that isn't
	for _, d := range []string{filepath.Dir(f), filepath.Join(dir, "profiles")} {
		if os.Remove(d) != nil {
			break
		}
	}
	return nil
}

// dshHomePatch is dsh's home patch layer, $DSH_HOME/cordis.patch.yml: it is
// applied over every profile's own, so an entry there replaces the profile's
// of the same id, whatever magpie wrote in that one (#804).
func dshHomePatch(dir string) string { return filepath.Join(dir, "cordis.patch.yml") }

// dshOver is a profile's patch list as dsh goes by it. A home patch only
// replaces the fields it supplies: config replaces the whole config, but
// disabled:false alone leaves the profile's config intact. For reading only.
func dshOver(dir string, items []dshItem) []dshItem {
	_, home, err := dshLook(dshHomePatch(dir))
	if err != nil || len(home) == 0 {
		return items
	}
	out := append([]dshItem{}, items...)
	for _, patch := range home {
		i := dshFindLast(out, patch.id)
		if i < 0 {
			out = append(out, patch)
			continue
		}
		n, err := dshNode(out[i])
		p, patchErr := dshNodeShared(patch) // only read: its values go into n
		if err != nil || patchErr != nil {
			out = append(out, patch)
			continue
		}
		if name, prev := yamlKey(p, "name"), yamlKey(n, "name"); name != nil && prev != nil && name.Value != "" && name.Value != prev.Value {
			continue // dsh ignores a patch naming a different plugin
		}
		for k := 0; k+1 < len(p.Content); k += 2 {
			if key := p.Content[k].Value; key != "id" && key != "name" {
				yamlSetKey(n, key, p.Content[k+1])
			}
		}
		if lines, err := dshNodeLines(n); err == nil {
			out[i].lines = lines
		}
	}
	return out
}

// dshConfigIndex is the last patch supplying this row's config. A later
// metadata-only patch must not hide it or acquire a replacement config.
func dshConfigIndex(items []dshItem, id string) int {
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].id == id {
			if n, err := dshNodeShared(items[i]); err == nil && yamlKey(n, "config") != nil {
				return i
			}
		}
	}
	return -1
}

// dshHomeHas reports whether the home layer overrides this row's config.
func dshHomeHas(dir, id string) bool {
	_, home, err := dshRead(dshHomePatch(dir))
	return err == nil && dshConfigIndex(home, id) >= 0
}

// dshMirrorHome gives the home layer what magpie wrote in the profiles, for
// the entries it overrides them with: magpie's route in a llm-pi-ai entry
// there (the user's routes beside it stay), and in an agent-default-model
// there the model magpie set. Without it dsh ran the home layer's snapshot
// while magpie read as connected (#804). An entry the home layer doesn't
// have is not added: the profiles' own then apply, as written. start is
// true only for an explicit model pick or disconnect. Catalog refreshes
// leave the home layer's start model alone, as they do the profiles'.
func dshMirrorHome(dir string, models []catalog.Model, gw string, start bool) error {
	files := dshProfiles(dir)
	if len(files) == 0 {
		return nil
	}
	home := dshHomePatch(dir)
	head, items, err := dshRead(home)
	if err != nil {
		return nil
	}
	_, web, err := dshRead(files[0])
	if err != nil {
		return nil
	}
	before := dshJoin(items)
	if dshConfigIndex(items, dshPiRow) >= 0 {
		on := dshWired(web)
		if !on || len(models) > 0 {
			if items, err = dshPutRoute(items, on, models, gw); err != nil {
				return err
			}
		}
	}
	// a start of the home layer's own on a model now on another route of
	// magpie's goes with it, as a profile's does (dshPutRoute)
	if len(models) > 0 {
		items = dshStartOnItsRoute(items, models)
	}
	if i := dshConfigIndex(items, "agent-default-model"); start && i >= 0 {
		j := dshFindLast(web, "agent-default-model")
		switch {
		case j >= 0 && dshOurs(web[j]):
			if !dshOurs(items[i]) {
				stash(map[string]string{dshStashKey(home, "agent-default-model"): strings.Join(items[i].lines, "\n")})
			}
			items[i] = dshItem{id: "agent-default-model", magpie: true, lines: append([]string{}, web[j].lines...)}
		case dshOurs(items[i]):
			// magpie's start gone from the profiles: the user's back, or none
			if old := unstash(dshStashKey(home, "agent-default-model")); old != "" {
				items[i] = dshItem{id: "agent-default-model", lines: strings.Split(old, "\n")}
			} else {
				items = append(items[:i], items[i+1:]...)
			}
		}
	}
	if dshJoin(items) == before {
		return nil
	}
	return dshWrite(home, head, items)
}

func dshJoin(items []dshItem) string {
	var b strings.Builder
	for _, it := range items {
		b.WriteString(strings.Join(it.lines, "\n"))
		b.WriteString("\n")
	}
	return b.String()
}

// dshItem is one entry of the patch list, as its lines.
type dshItem struct {
	id     string
	magpie bool
	lines  []string
}

var dshIDLine = lazyre.New(`^(?:- |  )id:\s*['"]?([^'"#\s]+)['"]?\s*(#.*)?$`)

// dshParse splits the patch list at path into what comes before its first
// entry and the entries. A list written in flow style ([ {id: a} ], which
// dsh's own writers keep a profile's [] in) is read as the same list in block
// style, and written back so; a file that is no list is left alone.
func dshParse(path, raw string) (head []string, items []dshItem, err error) {
	if b, ok := edit.BlockList(raw); ok {
		raw = b
	}
	opened := false // a "---" or "[]" came before the first entry
	for _, l := range splitLinesKeep(raw) {
		// magpie's notes are left out, and dshWrite puts them back: a
		// route is compared with the lines magpie would write
		if edit.IsNote(l) {
			continue
		}
		t := strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(l, "- ") || l == "-":
			items = append(items, dshItem{lines: []string{l}})
		case len(items) == 0:
			// "---" is YAML's document start marker: a patch list may open
			// with one (#2667), and it stays in the head it is written back
			// with, so the file keeps it. Only at the left edge and before
			// anything but comments: a second one starts a second
			// document, which is no list magpie can edit.
			marker := dshDocStart(l) && !opened
			if t != "" && !strings.HasPrefix(t, "#") && t != "[]" && !marker {
				return nil, nil, fmt.Errorf("%s is not a list of entries magpie can edit", path)
			}
			if t != "" && !strings.HasPrefix(t, "#") {
				opened = true
			}
			if t != "[]" {
				head = append(head, l)
			}
			continue
		case t != "" && !strings.HasPrefix(t, "#") && !strings.HasPrefix(l, " "):
			return nil, nil, fmt.Errorf("%s is not a list of entries magpie can edit", path)
		default:
			items[len(items)-1].lines = append(items[len(items)-1].lines, l)
		}
		it := &items[len(items)-1]
		if m := dshIDLine.FindStringSubmatch(l); m != nil && it.id == "" {
			it.id = m[1]
			it.magpie = strings.TrimSpace(m[2]) == dshMark
		}
	}
	return head, items, nil
}

func splitLinesKeep(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func dshRead(path string) ([]string, []dshItem, error) {
	b, err := edit.Read(path)
	if err != nil {
		return nil, nil, err
	}
	return dshParse(path, string(b))
}

// dshParsed is a patch list as dshParse gave it.
type dshParsed struct {
	head  []string
	items []dshItem
}

// dshLook is dshRead for a look that writes nothing: parsed once each time
// the file changes and shared, so read only. The window's 15s drift poll
// read and parsed every profile's list again on each ask, a third of what
// forty asks allocated with real accounts.
func dshLook(path string) ([]string, []dshItem, error) {
	dshUsed()
	d, err := filememo.Read("dsh list", path, func(b []byte) (dshParsed, error) {
		head, items, err := dshParse(path, string(edit.TrimBOM(b)))
		return dshParsed{head, items}, err
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil // as dshRead, whose edit.Read gives no bytes
	}
	return d.head, d.items, err
}

// dshWrite puts a patch list back together.
func dshWrite(path string, head []string, items []dshItem) error {
	out := append([]string{}, head...)
	for _, it := range items {
		out = append(out, it.lines...)
	}
	if len(items) == 0 {
		if _, err := os.Stat(path); err != nil {
			return nil // nothing was there, nothing to write
		}
		out = append(out, "[]") // dsh wants a list, even an empty one
	}
	// a route on the gateway says so (edit.Mark)
	orig, _ := edit.Read(path)
	out = edit.MarkYAMLLines(out, string(orig))
	return edit.WriteAtomic(path, []byte(strings.Join(out, "\n")+"\n"))
}

func dshFind(items []dshItem, id string) int {
	for i, it := range items {
		if it.id == id {
			return i
		}
	}
	return -1
}

// dshFindLast is the entry of that id dsh goes by: the last, as its own
// config editor edits.
func dshFindLast(items []dshItem, id string) int {
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].id == id {
			return i
		}
	}
	return -1
}

// dshNode reads one entry as YAML: its mapping ({id, name, config}).
func dshNode(it dshItem) (*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(strings.Join(it.lines, "\n")), &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.SequenceNode || len(doc.Content[0].Content) != 1 || doc.Content[0].Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("the %s entry is not one magpie can edit", it.id)
	}
	return doc.Content[0].Content[0], nil
}

// dshNodeShared is dshNode for a look: the window's 15s drift poll asks
// each profile's entries for their config several times over (dshCheck:
// dshHomeHas, dshOver, dshConfigIndex, dshRoutesIn, dshConfig), and a parse
// each time was 257 MB in 20 polls of a copy of the owner's real ~/.dsh.
// An entry's text is parsed once and its node shared, so whoever gets it
// only reads it; a caller changing the node takes dshNode's own copy.
func dshNodeShared(it dshItem) (*yaml.Node, error) {
	// keyed by a hash of the lines, checked against a copy of them, so a
	// look that finds the entry allocates nothing: joining the lines into a
	// key each time was most of what the drift poll's dsh check still made
	dshUsed()
	key := dshLinesHash(it.lines)
	dshNodes.Lock()
	n, ok := dshNodes.find(key, it.lines)
	dshNodes.Unlock()
	if !ok {
		var err error
		if n, err = dshNode(it); err != nil {
			n = nil
		}
		dshNodes.Lock()
		if dshNodes.m == nil || dshNodes.size >= dshNodesMax {
			dshNodes.m, dshNodes.size = map[uint64][]dshNodeEntry{}, 0
		}
		if _, there := dshNodes.find(key, it.lines); !there {
			dshNodes.m[key] = append(dshNodes.m[key], dshNodeEntry{slices.Clone(it.lines), n})
			dshNodes.size++
		}
		dshNodes.Unlock()
	}
	if n == nil {
		return nil, fmt.Errorf("the %s entry is not one magpie can edit", it.id)
	}
	return n, nil
}

// dshNodesMax bounds the parsed entries kept: a few profiles' dozens of
// entries, with room for the old text of ones just changed.
const dshNodesMax = 512

var dshNodeSeed = maphash.MakeSeed()

// dshLinesHash hashes an entry's lines as the text they make.
func dshLinesHash(lines []string) uint64 {
	var h maphash.Hash
	h.SetSeed(dshNodeSeed)
	for _, l := range lines {
		h.WriteString(l)
		h.WriteByte('\n')
	}
	return h.Sum64()
}

type dshNodeEntry struct {
	lines []string // a copy: a caller may change its own slice later
	n     *yaml.Node
}

type dshNodeCache struct {
	sync.Mutex
	m    map[uint64][]dshNodeEntry
	size int
}

// find is the node parsed from these lines; the caller holds the lock.
func (c *dshNodeCache) find(key uint64, lines []string) (*yaml.Node, bool) {
	for _, e := range c.m[key] {
		if slices.Equal(e.lines, lines) {
			return e.n, true
		}
	}
	return nil, false
}

var dshNodes dshNodeCache

// dshNodeLines writes an entry read with dshNode back as lines.
func dshNodeLines(n *yaml.Node) ([]string, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&yaml.Node{Kind: yaml.SequenceNode, Content: []*yaml.Node{n}}); err != nil {
		return nil, err
	}
	enc.Close()
	return splitLinesKeep(buf.String()), nil
}

func yamlKey(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

func yamlSetKey(n *yaml.Node, key string, v *yaml.Node) {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			n.Content[i+1] = v
			return
		}
	}
	n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, v)
}

func yamlDelKey(n *yaml.Node, key string) {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			n.Content = append(n.Content[:i], n.Content[i+2:]...)
			return
		}
	}
}

// yamlMapping is the mapping at key, made when there is none (or the value
// there is not one).
func yamlMapping(n *yaml.Node, key string) *yaml.Node {
	if v := yamlKey(n, key); v != nil && v.Kind == yaml.MappingNode {
		return v
	}
	v := &yaml.Node{Kind: yaml.MappingNode}
	yamlSetKey(n, key, v)
	return v
}

// dshConfig reads the scalar fields of an entry's config: dsh writes them
// as it likes, quoted or not.
func dshConfig(it dshItem) map[string]string {
	n, err := dshNodeShared(it)
	if err != nil {
		return nil
	}
	out := map[string]string{}
	if c := yamlKey(n, "config"); c != nil && c.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(c.Content); i += 2 {
			if v := c.Content[i+1]; v.Kind == yaml.ScalarNode {
				out[c.Content[i].Value] = v.Value
			}
		}
	}
	return out
}

// dshRoutesIn are magpie's routes among the custom providers of a patch
// list, by key: none when there are none.
func dshRoutesIn(items []dshItem) map[string]*yaml.Node {
	i := dshConfigIndex(items, dshPiRow)
	if i < 0 {
		return nil
	}
	n, err := dshNodeShared(items[i])
	if err != nil {
		return nil
	}
	providers := yamlKey(yamlKey(n, "config"), "providers")
	var out map[string]*yaml.Node
	for _, k := range dshKinds {
		if r := yamlKey(providers, k.key); r != nil {
			if out == nil {
				out = map[string]*yaml.Node{}
			}
			out[k.key] = r
		}
	}
	return out
}

// dshOldWiring reports whether a profile still has what magpie wrote before
// it was a custom provider: dsh's own DeepSeek row taken over.
func dshOldWiring(items []dshItem) bool {
	i := dshFind(items, "llm-deepseek")
	return i >= 0 && items[i].magpie
}

// dshOurs reports whether an entry is magpie's: marked so, or — should dsh
// rewrite it without the mark — a model on magpie's route, which is gone
// with it.
func dshOurs(it dshItem) bool {
	return it.magpie || it.id == "agent-default-model" && isDshRoute(dshConfig(it)["provider"])
}

// dshWired reports whether a profile's patch list has a route of magpie's.
func dshWired(items []dshItem) bool { return len(dshRoutesIn(items)) > 0 }

// dshServes reports whether magpie gives dsh this model now: the route
// written again would list it, where one naming a model magpie no longer
// gives dsh would not.
func dshServes(dir, id string) bool {
	for _, m := range sharedModels(dshListFor(dir)) {
		if m.ID == id {
			return true
		}
	}
	return false
}

// dshRouteLists reports whether a route of magpie's carries a model with
// this id; a route there is none of lists none.
func dshRouteLists(route *yaml.Node, id string) bool {
	models := yamlKey(route, "models")
	if models == nil {
		return false
	}
	for _, m := range models.Content {
		if v := yamlKey(m, "id"); v != nil && v.Value == id {
			return true
		}
	}
	return false
}

var dshModelLine = lazyre.New(`^\s+model:\s*(.+?)\s*$`)

// dshLead is the profile whose start magpie shows: the one named lead
// when there is one (the desktop app's when DeepSeek Harness Desktop is
// installed, DS Bot's in its home), else the first, web's; "" before
// profiles. dsh saves a model picked in it into the profile in
// use alone (ConfigEditor.edit in dsh-config-editor), so the profiles can
// start on different models, each the user's own. Read from web's alone,
// the Agents page showed a model the desktop app's sessions don't start
// on, and picking what it showed changed nothing there (#5222).
func dshLead(dir, lead string) string {
	files := dshProfilesHeld(dir)
	if len(files) == 0 {
		return ""
	}
	if lead != "" {
		for _, f := range files {
			if filepath.Base(filepath.Dir(f)) == lead {
				return f
			}
		}
	}
	return files[0]
}

// dshGet reads the model new sessions start on: the one last picked in dsh,
// saved in its settings (0.1.x), else the patch list's, the lead profile's
// (dshLead).
func dshGet(dir, lead string) string {
	f := dshLead(dir, lead)
	if f == "" {
		return dshGetLegacy(filepath.Join(dir, "config.yaml"))
	}
	_, items, err := dshLook(f)
	if err != nil {
		return ""
	}
	return dshStart(dir, items)
}

// dshStart reads the model a profile's sessions start on: the one last picked
// in dsh, saved in its settings, which go over every profile, else the
// agent-default-model entry of the home layer or the profile's own. dsh 0.2
// keeps a pick in that entry itself: a settings.yaml left by 0.1.x is
// imported into the profile it next boots and renamed settings.yaml.imported,
// which nothing reads (#1392).
func dshStart(dir string, items []dshItem) string {
	items = dshOver(dir, items)
	sel := dshStartSel(dir, items)
	switch {
	case sel["model"] == "":
		return ""
	case isDshRoute(sel["provider"]),
		// set before magpie was a provider of its own, not yet moved
		sel["provider"] == "deepseek-official" && dshOldWiring(items):
		return magpieID + "/" + sel["model"]
	}
	return sel["model"]
}

// dshStartSel is the start of a profile's patch list read over the home
// layer (dshOver), as dsh names it: provider (the route) and model.
func dshStartSel(dir string, items []dshItem) map[string]string {
	sel := edit.GetYAMLMap(filepath.Join(dir, "settings.yaml"), "agent-default-model")
	if sel["model"] == "" {
		if i := dshFindLast(items, "agent-default-model"); i >= 0 {
			sel = dshConfig(items[i])
		}
	}
	return sel
}

// dshGetLegacy reads agent-loop's model from a config.yaml before 0.1.5.
func dshGetLegacy(path string) string {
	_, items, err := dshLook(path)
	if err != nil {
		return ""
	}
	model := ""
	if i := dshFind(items, "agent-loop"); i >= 0 {
		for _, l := range items[i].lines {
			if m := dshModelLine.FindStringSubmatch(l); m != nil {
				model = yamlScalar(m[1])
				break
			}
		}
	}
	if model != "" && dshOldWiring(items) {
		return magpieID + "/" + model
	}
	return model
}

// DshThrough points magpie's route in dsh's patch lists (a, DeepSeek
// Harness) at the gateway through prefix, an address a local proxy takes the
// gateway's after (billion-context's http://127.0.0.1:8787/bili/), "" for
// the gateway itself. dsh must be on magpie already. bili's own plugin for
// dsh (dsh plugin add billion-context) rides proxy settings of dsh's
// process, which a provider at http://127.0.0.1 never takes; its docs'
// /bili/ prefix takes any client whose model address can be edited. A
// sync, a pick and the serving round keep the prefix (dshReach).
func DshThrough(a *Agent, prefix string) error {
	if a == nil || a.ID != "dsh" || a.through == nil {
		return fmt.Errorf("not DeepSeek Harness")
	}
	return a.through(prefix)
}

// DshURL is the baseURL of magpie's first route in dsh's connected patch
// list (a, DeepSeek Harness), "" without one.
func DshURL(a *Agent) string {
	_, items, err := dshLook(a.Path)
	if err != nil {
		return ""
	}
	r := dshRoutesIn(dshOver(a.Dir, items))
	for _, k := range dshKinds {
		if n := r[k.key]; n != nil {
			if v := yamlKey(n, "baseURL"); v != nil && v.Kind == yaml.ScalarNode {
				return v.Value
			}
		}
	}
	return ""
}

// dshReach is gw behind the prefix magpie's routes in dsh's profiles go
// through (a local proxy's route on loopback before the gateway's address,
// DshThrough), gw itself when they go to the gateway or anywhere else.
func dshReach(dir, gw string) string {
	for _, f := range dshProfiles(dir) {
		_, items, err := dshLook(f)
		if err != nil {
			continue
		}
		for _, n := range dshRoutesIn(dshOver(dir, items)) {
			v := yamlKey(n, "baseURL")
			if v == nil || v.Kind != yaml.ScalarNode {
				continue
			}
			base := strings.TrimSuffix(strings.TrimSpace(v.Value), "/")
			base = strings.TrimSuffix(base, "/v1")
			if p, ok := strings.CutSuffix(base, gw); ok && p != "" && loopbackWithPath(strings.TrimSuffix(p, "/")) {
				return p + gw
			}
		}
	}
	return gw
}

// dshThrough is DshThrough for the dsh at a place.
func dshThrough(at place, dir, prefix string, fresh func() string) error {
	if !dshWired(dshOverAll(dir)) {
		return fmt.Errorf("connect DeepSeek Harness to magpie first: pick one of magpie's models for it on the Agents page")
	}
	if len(sharedModels(dshListFor(dir))) == 0 {
		return fmt.Errorf("magpie lists no models for DeepSeek Harness yet")
	}
	return dshSyncFresh(at, dir, prefix+at.gw(), fresh)
}

// dshOverAll is the profiles' patch lists as dsh goes by them, one list.
func dshOverAll(dir string) []dshItem {
	var all []dshItem
	for _, f := range dshProfiles(dir) {
		if _, items, err := dshLook(f); err == nil {
			all = append(all, dshOver(dir, items)...)
		}
	}
	return all
}

// dshCheck says what keeps a dsh on one of magpie's models from reaching
// the gateway: magpie's route (its llm-deepseek entry before 0.1.5) pointed
// elsewhere, the model it starts on one the route doesn't list, or — since
// 0.1.5 — the key it names gone from .env, or another key under that name in
// dsh's own store, which it reads first.
func dshCheck(dir, gw string) string {
	if !usesMagpie(dshGet(dir, "")) {
		return ""
	}
	files := dshProfiles(dir)
	if len(files) == 0 {
		path := filepath.Join(dir, "config.yaml")
		_, items, _ := dshLook(path)
		base := ""
		if i := dshFind(items, "llm-deepseek"); i >= 0 {
			for _, l := range items[i].lines {
				if k, v, ok := strings.Cut(strings.TrimSpace(l), ":"); ok && k == "baseURL" {
					base = yamlScalar(strings.TrimSpace(v))
				}
			}
		}
		get := func(string) (string, bool) { return base, base != "" }
		return wiringOff("DeepSeek Harness", path, get, "baseURL", gw+"/v1")
	}
	_, items, _ := dshLook(files[0])
	routeFile := files[0]
	if dshHomeHas(dir, dshPiRow) {
		routeFile = dshHomePatch(dir)
		// the home layer's llm-pi-ai goes over every profile's: without
		// magpie's route it lists none of magpie's models anywhere
		if !dshWired(dshOver(dir, items)) && dshWired(items) {
			return "DeepSeek Harness's home patch layer (" + routeFile + ") sets llm-pi-ai over every profile without magpie's route, so sessions list none of magpie's models; Apply again writes it there too"
		}
	}
	// a profile of dsh's (its desktop app's) without magpie's route: where
	// the start still names one of magpie's models the route was stripped by
	// dsh's own save, and the serving round writes it back within a round of
	// magpie serving — so that one is not said, unless there is no catalog
	// for the round to write it from. A profile taken off magpie for real,
	// its start with it, is said as before, and so is one still on the old
	// wiring, which dshSync moves rather than this round writing beside.
	canWriteBack := len(sharedModels(dshListFor(dir))) > 0
	for _, f := range files[1:] {
		_, items, err := dshLook(f)
		if err != nil || dshWired(dshOver(dir, items)) {
			continue
		}
		if canWriteBack && !dshOldWiring(items) && dshStartsOnMagpie(dir, items) {
			continue
		}
		return "DeepSeek Harness's " + filepath.Base(filepath.Dir(f)) + " profile (" + f + ") has none of magpie's models, so sessions there list DeepSeek's own alone"
	}
	routes := dshRoutesIn(dshOver(dir, items))
	// the route gone entirely while the start still names one of magpie's
	// models is dsh's own save stripping it: the round writes it back, so
	// the check is quiet unless nothing can write it (no catalog below). A
	// start of dsh's own or of the user's fell through usesMagpie already,
	// and one on the old wiring is not this route's to speak for.
	if len(routes) == 0 && !dshOldWiring(items) && dshStartsOnMagpie(dir, items) {
		return dshCheckStripped(dir, gw)
	}
	if len(routes) == 0 && dshOldWiring(items) {
		// not moved yet; the next sync does it. dsh's DeepSeek row taken
		// over speaks Chat Completions.
		get := func(k string) (string, bool) {
			v, ok := dshConfig(items[dshFind(items, "llm-deepseek")])[k]
			return v, ok
		}
		if off := wiringOff("DeepSeek Harness", routeFile, get, "baseURL", dshBaseURL(dshKinds[0].api, gw)); off != "" {
			return off
		}
	}
	for _, k := range dshKinds {
		route := routes[k.key]
		if route == nil {
			continue
		}
		get := func(key string) (string, bool) {
			if v := yamlKey(route, key); v != nil && v.Kind == yaml.ScalarNode {
				return v.Value, true
			}
			return "", false
		}
		api, _ := get("api")
		if off := wiringOff("DeepSeek Harness", routeFile, get, "baseURL", dshBaseURL(dshAPI(api, k.api), gw)); off != "" {
			return off
		}
	}
	// dsh counts a model its provider doesn't list as none at all and refuses
	// the turn, so a model of magpie's a profile's route has not is as
	// unusable there as a route pointed elsewhere: a catalog that moved on, a
	// profile written elsewhere, or one dsh's Models page wrote leave it that
	// way. Every profile carries a route of its own, and dsh runs whichever
	// one its session names, so every profile is asked.
	for _, f := range files {
		_, it, err := dshLook(f)
		if err != nil {
			continue
		}
		over := dshOver(dir, it)
		routes := dshRoutesIn(over)
		if len(routes) == 0 {
			continue // no route yet: the sync this asks for writes one
		}
		if dshHomeHas(dir, dshPiRow) {
			f = dshHomePatch(dir)
		}
		// the route the start names (magpie's first for one still on dsh's
		// DeepSeek row), which must list its model
		on := dshStartSel(dir, over)["provider"]
		if !isDshRoute(on) {
			on = dshRoute
		}
		if ref, ok := strings.CutPrefix(dshStart(dir, it), magpieID+"/"); ok && !dshRouteLists(routes[on], ref) {
			// a route out of date is written again — Apply again, or picking
			// a model of magpie's in dsh, does it; one naming a model magpie
			// no longer gives dsh is not, so that one asks for another model
			// rather than for a click that cannot help
			if dshServes(dir, ref) {
				return "DeepSeek Harness starts on " + magpieID + "/" + ref + ", which magpie's route in " + f + " doesn't list: a session there fails with no such configured model until that route is written again (Apply again, or pick one of magpie's models in dsh)"
			}
			return "DeepSeek Harness starts on " + magpieID + "/" + ref + ", which magpie no longer gives dsh: a session there fails with no such configured model until another of magpie's is picked in dsh"
		}
	}
	env := filepath.Join(dir, ".env")
	if off := wiringOff("DeepSeek Harness", env, func(k string) (string, bool) { return edit.GetEnvFile(env, k) },
		dshKeyRef, keyAt(gw)); off != "" {
		return off
	}
	creds := filepath.Join(dir, ".credentials.yaml")
	if v, ok := edit.GetYAML(creds, "refs."+dshKeyRef); ok && v != keyAt(gw) && !gatewayTakes(v) {
		if onAnotherMachine(gw) {
			return "DeepSeek Harness's own key store (" + creds + ") holds another " + dshKeyRef + ", which it uses over magpie's; remove it there (dsh's Models page, Magpie's key) to go through magpie"
		}
		return "DeepSeek Harness's own key store (" + creds + ") holds another " + dshKeyRef + ", which it uses over magpie's and this magpie refuses; Apply again puts magpie's key there (Disconnect puts this one back)"
	} else if !ok && dshCredsOurs(creds) {
		// the desktop app doesn't read .env (#969)
		return "DeepSeek Harness's own key store (" + creds + ") doesn't hold " + dshKeyRef + ": the desktop app reads the key from there, not from .env, and its sessions fail with no credential for provider route " + dshRoute + " until it is written (Apply again)"
	}
	return ""
}

// dshCheckStripped says what a route dsh's own save stripped still stands on:
// quiet where a round of the serving loop can write it back, said only where
// nothing can (no catalog to write it from — the key check still runs, since
// a route written without its key would fail the turn all the same).
func dshCheckStripped(dir, gw string) string {
	if len(sharedModels(dshListFor(dir))) == 0 {
		return "DeepSeek Harness starts on one of magpie's models, but magpie's route is out of " + dshHomePatch(dir) + " and the profiles' and there is no model list to write it back from; pick a model of magpie's again once it has one"
	}
	env := filepath.Join(dir, ".env")
	if off := wiringOff("DeepSeek Harness", env, func(k string) (string, bool) { return edit.GetEnvFile(env, k) },
		dshKeyRef, keyAt(gw)); off != "" {
		return off
	}
	return ""
}

// yamlScalar reads a plain or quoted YAML scalar.
func yamlScalar(v string) string {
	if i := strings.Index(v, " #"); i >= 0 {
		v = strings.TrimSpace(v[:i])
	}
	if strings.HasPrefix(v, `"`) {
		var s string
		if json.Unmarshal([]byte(v), &s) == nil {
			return s
		}
	}
	return strings.Trim(v, `'"`)
}

func dshStashKey(path, id string) string { return "dsh:" + path + ":" + id }

// dshWrites serializes magpie's own writers of dsh's patch lists: the sync a
// catalog change asks for, a model picked here, and the round magpie does
// while it serves the gateway. Two of them reading one file and then writing
// it would leave whichever read first as its content.
var dshWrites sync.Mutex

// dshSet writes v as the model new sessions start on: a catalog model
// through the gateway, one of dsh's own models directly, or "" for dsh's own
// default. Every profile gets it; config.yaml only where there are none.
func dshSet(at place, dir, v, gw string) error {
	dshWrites.Lock()
	defer dshWrites.Unlock()
	return dshSetLocked(at, dir, v, gw)
}

// dshSetLocked is dshSet with dshWrites held.
func dshSetLocked(at place, dir, v, gw string) error {
	ref, viaGateway := strings.CutPrefix(v, magpieID+"/")
	if viaGateway && !isMagpie(ref) {
		return fmt.Errorf("unknown model %q", v)
	}
	legacy := filepath.Join(dir, "config.yaml")
	models := magpieModels(dshListFor(dir))
	files := dshProfiles(dir)
	if len(files) == 0 {
		return dshSetFile(legacy, v, false, models, gw)
	}
	for _, f := range files {
		if err := dshSetFile(f, v, true, models, gw); err != nil {
			return err
		}
	}
	if err := dshMirrorHome(dir, models, gw, true); err != nil {
		return err
	}
	if err := dshSettingsAgain(dir, models, gw); err != nil {
		return err
	}
	// what an older dsh was given is no use now
	if _, err := os.Stat(legacy); err == nil {
		if err := dshSetFile(legacy, "", false, models, gw); err != nil {
			return err
		}
	}
	if err := dshEnv(at, dir, gw, viaGateway, true); err != nil {
		return err
	}
	// a model picked in dsh 0.1.x is saved in its settings and goes over
	// the patch list; picking one here takes over from it
	settings := filepath.Join(dir, "settings.yaml")
	if v != "" && edit.GetYAMLMap(settings, "agent-default-model") != nil {
		return edit.DelYAML(settings, "agent-default-model")
	}
	return nil
}

// dshKeepPick hands a profile's start back to the user where dsh saved their
// own pick into magpie's agent-default-model entry. Since 0.2 dsh no longer
// keeps a pick in ~/.dsh/settings.yaml: its /model (and the one-time import
// that renames settings.yaml to settings.yaml.imported) writes the config of
// the profile's last agent-default-model entry, which is magpie's, mark and
// all (#1392). A start there off magpie's route is the user's then, and
// Disconnect leaves it as it is, as it left a pick in settings.yaml before;
// what magpie stashed from before it wrote the entry is older than that pick.
// One still on the old wiring names DeepSeek's row for magpie's models and
// stays magpie's.
func dshKeepPick(f string) error {
	head, items, err := dshRead(f)
	if err != nil || dshOldWiring(items) {
		return nil
	}
	i := dshFind(items, "agent-default-model")
	if i < 0 || !items[i].magpie {
		return nil
	}
	if p := dshConfig(items[i])["provider"]; p == "" || isDshRoute(p) || dshConfig(items[i])["model"] == "" {
		return nil
	}
	lines := append([]string{}, items[i].lines...)
	lines[0] = strings.TrimRight(strings.Replace(lines[0], dshMark, "", 1), " ")
	items[i] = dshItem{id: items[i].id, lines: lines}
	unstash(dshStashKey(f, "agent-default-model"))
	return dshWrite(f, head, items)
}

// dshEnv puts the key for the gateway in $DSH_HOME/.env and dsh's own key
// store, or with on false takes it out; the name magpie's llm-deepseek entry
// used goes either way.
func dshEnv(at place, dir, gw string, on, take bool) error {
	if err := dshCreds(at, dir, gw, on, take); err != nil {
		return err
	}
	env := filepath.Join(dir, ".env")
	if on {
		if v, ok := edit.GetEnvFile(env, dshKeyRef); !ok || v != keyAt(gw) {
			if err := edit.SetEnvFile(env, edit.KV{Path: dshKeyRef, Value: keyAt(gw)}); err != nil {
				return err
			}
		}
	}
	for _, k := range []string{dshKeyRef, dshOldKeyRef} {
		// the old name only as magpie left it
		if v, ok := edit.GetEnvFile(env, k); ok && !(k == dshKeyRef && on) && (k == dshKeyRef || ourKey(v)) {
			if err := edit.DelEnvFile(env, k); err != nil {
				return err
			}
		}
	}
	return nil
}

// dshCreds puts the key for the gateway in dsh's own key store,
// $DSH_HOME/.credentials.yaml (refs), or with on false takes magpie's out.
// Only the product CLI loads the .env files into what dsh resolves keys
// from: the desktop app reads this store and its own environment, and with
// the key in .env alone its sessions failed with "no credential for provider
// route magpie" (#969). The store wins over .env, so a key of the user's
// there is left as it is by the sync and the serving round (the check says
// so). take is a model picked, or Apply again: that replaces a key there
// this gateway refuses, which only fails every turn through magpie, and
// stashes it for Disconnect to put back (#1419, DannyJRu: Reconnect said
// done and the row stayed red). A key the gateway takes stays, and so does
// any key for a gateway on another machine, which only that one can judge.
// dsh refuses a store any other user can read, so a new one is made
// owner-only; one in a layout other than version 1 is dsh's to move and
// isn't touched.
func dshCreds(at place, dir, gw string, on, take bool) error {
	creds := filepath.Join(dir, ".credentials.yaml")
	raw, err := os.ReadFile(creds)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if !dshCredsOurs(creds) {
		return nil
	}
	if on {
		if err := dshCredsPut(creds, dir, gw, take, raw); err != nil {
			return err
		}
		// one there already, the user's or an older magpie's, may be
		// readable by others: dsh refuses it then
		return at.ownerOnly(creds)
	}
	for _, k := range []string{dshKeyRef, dshOldKeyRef} {
		if v, ok := edit.GetYAML(creds, "refs."+k); ok && ourKey(v) {
			if err := edit.DelYAML(creds, "refs."+k); err != nil {
				return err
			}
		}
	}
	if v := unstash(dshStashKey(creds, dshKeyRef)); v != "" {
		if _, ok := edit.GetYAML(creds, "refs."+dshKeyRef); !ok {
			return edit.SetYAML(creds, edit.KV{Path: "refs." + dshKeyRef, Value: v})
		}
	}
	return nil
}

// dshCredsPut is dshCreds putting the key in.
func dshCredsPut(creds, dir, gw string, take bool, raw []byte) error {
	v, ok := edit.GetYAML(creds, "refs."+dshKeyRef)
	if ok && v == keyAt(gw) {
		return nil
	}
	if ok && !ourKey(v) {
		if !take || gatewayTakes(v) || onAnotherMachine(gw) {
			return nil
		}
		stash(map[string]string{dshStashKey(creds, dshKeyRef): v})
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(creds, []byte("version: 1\n"), 0o600); err != nil {
			return err
		}
	}
	return edit.SetYAML(creds, edit.KV{Path: "refs." + dshKeyRef, Value: keyAt(gw)})
}

// dshCredsOurs says whether magpie may write dsh's key store: there is none
// yet, or it is in the version 1 layout.
func dshCredsOurs(creds string) bool {
	raw, err := os.ReadFile(creds)
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	if err != nil {
		return false
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return true
	}
	v, ok := edit.GetYAML(creds, "version")
	return ok && v == "1"
}

// dshSettings is $DSH_HOME/settings.yaml: since dsh 0.2 the CLI and the
// desktop app import it into the active profile once and rename it, but
// deepseek-acp (dsh in an editor over ACP: Zed, CodeG) reads it and nothing
// else. Its llm-pi-ai section is the routes deepseek-acp has, and only a
// settings.yaml that names llm-pi-ai loads the plugin there at all
// (composition/pi-ai.js, settingsMentionsPiAi), so with magpie's route in
// the profiles alone an editor listed DeepSeek's own models (liby233).
func dshSettings(dir string) string { return filepath.Join(dir, "settings.yaml") }

// dshSettingsRoute puts magpie's routes for models under llm-pi-ai's
// providers in settings.yaml, or with on false takes them out, as
// dshPutProviders does in a patch list: the user's routes and every other
// section stay. A settings.yaml there isn't made for a route off, nor for
// an empty catalog; one magpie can't read is left as it is. dsh 0.2 eats
// the file at each start (it renames it to settings.yaml.imported, over the
// one before), so it is written again whenever magpie syncs; an import of
// the user's own before magpie's first is kept aside once, as
// settings.yaml.imported.kept.
func dshSettingsRoute(dir string, on bool, models []catalog.Model, gw string) error {
	if on && len(models) == 0 {
		return nil
	}
	path := dshSettings(dir)
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		if !on {
			return nil
		}
		providers := &yaml.Node{Kind: yaml.MappingNode}
		if err := dshPutProviders(providers, true, models, gw); err != nil {
			return err
		}
		if err := dshKeepImported(dir); err != nil {
			return err
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if raw == nil { // dsh keeps its settings owner-only too
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				return err
			}
		}
		return edit.SetYAML(path, edit.KV{Path: dshPiRow + ".providers", Value: providers})
	}
	var doc yaml.Node
	if yaml.Unmarshal(raw, &doc) != nil || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil
	}
	pi := yamlKey(doc.Content[0], dshPiRow)
	if pi == nil || pi.Kind == yaml.ScalarNode && pi.ShortTag() == "!!null" {
		if !on {
			return nil
		}
		providers := &yaml.Node{Kind: yaml.MappingNode}
		if err := dshPutProviders(providers, true, models, gw); err != nil {
			return err
		}
		return edit.SetYAML(path, edit.KV{Path: dshPiRow + ".providers", Value: providers})
	}
	if pi.Kind != yaml.MappingNode {
		return nil // not a section dsh reads: the user's to mend
	}
	return edit.EditYAMLNode(path, dshPiRow, func(n *yaml.Node) (bool, bool) {
		if n == nil || n.Kind != yaml.MappingNode {
			return false, false
		}
		before, _ := yaml.Marshal(n)
		providers := yamlMapping(n, "providers")
		if err := dshPutProviders(providers, on, models, gw); err != nil {
			return false, false
		}
		if !on && len(providers.Content) == 0 {
			yamlDelKey(n, "providers")
		}
		after, _ := yaml.Marshal(n)
		if bytes.Equal(before, after) {
			return false, false
		}
		// a section left empty by magpie's routes going goes with them
		return true, len(n.Content) == 0
	})
}

// dshSettingsAgain gives settings.yaml magpie's routes while the profiles
// have them (the web's, first, as dshMirrorHome reads it), and takes them
// out when they don't. A dsh before profiles is left as it is.
func dshSettingsAgain(dir string, models []catalog.Model, gw string) error {
	files := dshProfiles(dir)
	if len(files) == 0 {
		return nil
	}
	_, web, err := dshRead(files[0])
	if err != nil {
		return nil
	}
	return dshSettingsRoute(dir, dshWired(web), models, gw)
}

// dshKeepImported copies settings.yaml.imported, what dsh imported from the
// user's settings.yaml, to settings.yaml.imported.kept before magpie makes a
// settings.yaml of its own: dsh's next start renames that one over it. One
// that is only magpie's route, or one kept already, isn't copied.
func dshKeepImported(dir string) error {
	imported := dshSettings(dir) + ".imported"
	raw, err := os.ReadFile(imported)
	if err != nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	kept := imported + ".kept"
	if _, err := os.Stat(kept); err == nil {
		return nil
	}
	if dshOnlyOurRoute(raw) {
		return nil
	}
	return os.WriteFile(kept, raw, 0o600)
}

// dshOnlyOurRoute says raw, a settings.yaml, holds nothing but magpie's
// routes under llm-pi-ai: what dshSettingsRoute makes.
func dshOnlyOurRoute(raw []byte) bool {
	var doc map[string]any
	if yaml.Unmarshal(raw, &doc) != nil || len(doc) != 1 {
		return false
	}
	pi, ok := doc[dshPiRow].(map[string]any)
	if !ok || len(pi) != 1 {
		return false
	}
	providers, ok := pi["providers"].(map[string]any)
	if !ok || len(providers) == 0 {
		return false
	}
	for k := range providers {
		if dshKindOf(k) == nil {
			return false
		}
	}
	return true
}

// ownerOnly takes the group's and others' bits off a file of dsh's: its
// key store is refused, and deepseek-acp won't start, when anyone else can
// read it ("run chmod 600"). Windows has no such bits, and dsh doesn't ask
// there. In a WSL distro the file is chmodded there: one written through
// \\wsl.localhost comes out 0644 whatever mode magpie asks for.
func (p place) ownerOnly(path string) error {
	if p.chmod != nil {
		return p.chmod(path)
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm()&0o077 == 0 {
		return nil
	}
	return os.Chmod(path, fi.Mode().Perm()&^0o077)
}

// dshSetFile writes v into one patch list; modern is the layout of dsh
// 0.1.5 on.
func dshSetFile(path, v string, modern bool, models []catalog.Model, gw string) error {
	head, items, err := dshRead(path)
	if err != nil {
		return err
	}
	ref, viaGateway := strings.CutPrefix(v, magpieID+"/")
	effort := ""
	if modern {
		effort = dshEffortOf(items)
	} else {
		effort = dshEffortIn(items)
	}
	put := func(id string, lines []string) {
		i := dshFind(items, id)
		if i >= 0 && !dshOurs(items[i]) {
			stash(map[string]string{dshStashKey(path, id): strings.Join(items[i].lines, "\n")})
		}
		it := dshItem{id: id, magpie: true, lines: lines}
		if i >= 0 {
			items[i] = it
		} else {
			items = append(items, it)
		}
	}
	drop := func(id string) {
		i := dshFind(items, id)
		if i < 0 || !dshOurs(items[i]) {
			return
		}
		if old := unstash(dshStashKey(path, id)); old != "" {
			items[i] = dshItem{id: id, lines: strings.Split(old, "\n")}
			return
		}
		items = append(items[:i], items[i+1:]...)
	}

	switch {
	case v == "":
		drop("agent-default-model")
		drop("api-gateway")
		drop("agent-loop")
		drop("llm-deepseek")
		if items, err = dshPutRoute(items, false, nil, gw); err != nil {
			return err
		}
		if modern {
			items = dshPutSearch(items, dshProfileHome(path), "", gw)
		}
	case modern && viaGateway:
		drop("llm-deepseek") // magpie's before it was a provider of its own
		if items, err = dshPutRoute(items, true, models, gw); err != nil {
			return err
		}
		if !contains(dshLevels(dshListFor(path), ref), effort) {
			effort = ""
		}
		put("agent-default-model", dshDefaultLines(dshRouteFor(ref, models), ref, effort))
		items = dshPutSearch(items, dshProfileHome(path), dshSearchModel(ref), gw)
	case modern:
		drop("llm-deepseek")
		if items, err = dshPutRoute(items, false, nil, gw); err != nil {
			return err
		}
		items = dshPutSearch(items, dshProfileHome(path), "", gw)
		if !contains(dshEfforts, effort) {
			effort = ""
		}
		put("agent-default-model", dshDefaultLines("deepseek-official", v, effort))
	case viaGateway:
		put("llm-deepseek", dshProviderLines(effort, models, gw))
		put("agent-loop", dshLoopLines(ref))
		put("api-gateway", dshRouteLines(ref))
	default:
		drop("api-gateway")
		drop("llm-deepseek")
		put("agent-loop", dshLoopLines(v))
	}
	return dshWrite(path, head, items)
}

// dshPutRoute puts magpie's route, with the catalog as it is now, among the
// custom providers of the llm-pi-ai entry, or with on false takes it out.
// Other routes there, the user's or ones dsh added beside magpie's, stay; an
// entry left with nothing goes.
func dshPutRoute(items []dshItem, on bool, models []catalog.Model, gw string) ([]dshItem, error) {
	i := dshConfigIndex(items, dshPiRow)
	if i < 0 {
		i = dshFindLast(items, dshPiRow)
	}
	if i < 0 && !on {
		return items, nil
	}
	var n *yaml.Node
	if i >= 0 {
		var err error
		if n, err = dshNode(items[i]); err != nil {
			return nil, err
		}
	} else {
		items = append(items, dshItem{id: dshPiRow, magpie: true, lines: []string{"- id: " + dshPiRow + " " + dshMark, "  name: " + yamlQuote(dshPiPlugin)}})
		i = len(items) - 1
		n, _ = dshNode(items[i])
	}
	config := yamlMapping(n, "config")
	providers := yamlMapping(config, "providers")
	if err := dshPutProviders(providers, on, models, gw); err != nil {
		return nil, err
	}
	if on {
		items = dshStartOnItsRoute(items, models)
	} else {
		if len(providers.Content) == 0 {
			yamlDelKey(config, "providers")
		}
		if len(config.Content) == 0 {
			yamlDelKey(n, "config")
		}
		if yamlKey(n, "config") == nil && len(n.Content) <= 4 { // id and name alone
			return append(items[:i], items[i+1:]...), nil
		}
	}
	lines, err := dshNodeLines(n)
	if err != nil {
		return nil, err
	}
	items[i] = dshItem{id: dshPiRow, magpie: items[i].magpie, lines: lines}
	return items, nil
}

// dshPutProviders puts magpie's routes for models into providers, a
// llm-pi-ai config's providers mapping, or with on false takes them out. The
// user's own routes there stay, and so do the protocol and compat switches
// picked for magpie's in dsh's Models page.
func dshPutProviders(providers *yaml.Node, on bool, models []catalog.Model, gw string) error {
	if !on {
		for _, k := range dshKinds {
			yamlDelKey(providers, k.key)
		}
		return nil
	}
	// each model on the route of the API it is best asked on; a route
	// left with none goes, the first stays when every one would
	byRoute := map[string][]catalog.Model{}
	for _, m := range models {
		byRoute[dshRouteOf(m)] = append(byRoute[dshRouteOf(m)], m)
	}
	for _, k := range dshKinds {
		ms := byRoute[k.key]
		if len(ms) == 0 && (k.key != dshRoute || len(models) > 0) {
			yamlDelKey(providers, k.key)
			continue
		}
		// the wire protocol the user picked for the route in dsh's
		// Models page stays, when it is one the gateway speaks
		api := ""
		if v := yamlKey(yamlKey(providers, k.key), "api"); v != nil && v.Kind == yaml.ScalarNode {
			api = v.Value
		}
		var route yaml.Node
		cfg := dshKindConfig(k, ms, api, gw)
		cur := yamlKey(providers, k.key)
		dshOutputsKept(&cfg, cur)
		if err := route.Encode(cfg); err != nil {
			return err
		}
		// what dsh's Models page added to it (a default level, headers)
		// stays; magpie's fields are put as they are now
		if cur != nil && cur.Kind == yaml.MappingNode {
			dshKeepCompat(cur, &route)
			for j := 0; j+1 < len(route.Content); j += 2 {
				if route.Content[j].Value == dshIdleKey && yamlKey(cur, dshIdleKey) != nil {
					continue // the user's own wait, or the one magpie wrote
				}
				yamlSetKey(cur, route.Content[j].Value, route.Content[j+1])
			}
		} else {
			yamlSetKey(providers, k.key, &route)
		}
	}
	return nil
}

// dshKeepCompat carries the compat switches set by hand in dsh's Models page
// (maxTokensField, say) from route cur, as it is in the file, into route,
// as magpie writes it now (#2504, h4rk8s): a model that works only with
// them would fail again after each sync. They stay on the same model of a
// route still on the same protocol; a model new to the route, or a route
// whose protocol changed, starts without them. A switch magpie sets itself
// is put as magpie has it now.
func dshKeepCompat(cur, route *yaml.Node) {
	api := yamlKey(cur, "api")
	if api == nil || yamlKey(route, "api") == nil || api.Value != yamlKey(route, "api").Value {
		return
	}
	old := map[string]*yaml.Node{}
	if ms := yamlKey(cur, "models"); ms != nil && ms.Kind == yaml.SequenceNode {
		for _, m := range ms.Content {
			c := yamlKey(m, "compat")
			if id := yamlKey(m, "id"); id != nil && c != nil && c.Kind == yaml.MappingNode {
				old[id.Value] = c
			}
		}
	}
	ms := yamlKey(route, "models")
	if ms == nil || ms.Kind != yaml.SequenceNode {
		return
	}
	for _, m := range ms.Content {
		id := yamlKey(m, "id")
		if id == nil || old[id.Value] == nil {
			continue
		}
		c := yamlMapping(m, "compat")
		for j := 0; j+1 < len(old[id.Value].Content); j += 2 {
			if yamlKey(c, old[id.Value].Content[j].Value) == nil {
				yamlSetKey(c, old[id.Value].Content[j].Value, old[id.Value].Content[j+1])
			}
		}
	}
}

// dshOutputsKept keeps a maxTokens the user typed into one of magpie's
// models on route cur, in dsh's Models page or by hand, in r, the route as
// magpie writes it (outputsKept). One record serves every file dsh keeps
// the route in: what magpie wanted for a model is the same in each.
func dshOutputsKept(r *dshPiRoute, cur *yaml.Node) {
	want := map[string]int{}
	for _, m := range r.Models {
		if m.MaxTokens > 0 {
			want[m.ID] = m.MaxTokens
		}
	}
	if len(want) == 0 {
		return
	}
	have := map[string]int{}
	if cur != nil && cur.Kind == yaml.MappingNode {
		if ms := yamlKey(cur, "models"); ms != nil && ms.Kind == yaml.SequenceNode {
			for _, m := range ms.Content {
				id, mt := yamlKey(m, "id"), yamlKey(m, "maxTokens")
				if id != nil && mt != nil {
					n, _ := strconv.Atoi(mt.Value)
					have[id.Value] = n
				}
			}
		}
	}
	kept := outputsKept("dsh:routes", want, have)
	for i := range r.Models {
		if n := kept[r.Models[i].ID]; n > 0 {
			r.Models[i].MaxTokens = n
		}
	}
}

// dshPiRoute is magpie's route as llm-pi-ai takes a custom provider (the
// shape dsh's Models page writes): one of the gateway's APIs (dshAPI), the
// key a credential named here, the catalog as its models.
type dshPiRoute struct {
	DisplayName string       `yaml:"displayName"`
	APIKeyEnv   string       `yaml:"apiKeyEnv"`
	API         string       `yaml:"api"`
	BaseURL     string       `yaml:"baseURL"`
	Models      []dshPiModel `yaml:"models"`
	// StreamIdleTimeoutMs is dshIdleWait.
	StreamIdleTimeoutMs int `yaml:"streamIdleTimeoutMs,omitempty"`
}

// dshIdleWait is how long llm-pi-ai waits on magpie's route for the next
// event of a reply before it gives the turn up, in ms: the gateway's own
// wait for a reply's headers (upstreamHeaderWait, 10 minutes). llm-pi-ai's
// own is 5 minutes (DEFAULT_STREAM_IDLE_TIMEOUT_MS, 0.1.5 to 0.2.2), and
// it counts pi-ai's events only: the ": keepalive" comments the gateway
// sends while a request waits in a provider's queue (keepQueued) or on a
// slow first byte (keepQuiet) aren't events, and pi-ai has none before the
// reply's headers. So a turn queued behind dsh's own title request on a
// plugin that serves one at a time (magpie-gemini-web, maxConcurrency 1)
// was cut at 5 minutes, recorded as 499, while magpie and the plugin were
// still within their waits (tim on Discord). Codex's comment-blind limit
// is met by agentQuietMost in the gateway; dsh's is a setting of the
// route. A value already on the route (one set in dsh, or by hand) stays.
const dshIdleWait = 600000

// dshIdleKey is the route's field dshIdleWait goes in.
const dshIdleKey = "streamIdleTimeoutMs"

// dshPiModel is one model of the route. Without contextWindow and maxTokens
// dsh takes every model for a 128K one writing 16K at most, and without
// input for a text-only one; without reasoningEfforts it offers none.
type dshPiModel struct {
	ID               string       `yaml:"id"`
	Name             string       `yaml:"name,omitempty"`
	ContextWindow    int          `yaml:"contextWindow,omitempty"`
	MaxTokens        int          `yaml:"maxTokens,omitempty"`
	Input            []string     `yaml:"input,omitempty,flow"`
	ReasoningEfforts *dshPiLevels `yaml:"reasoningEfforts,omitempty"`
	Compat           *dshPiCompat `yaml:"compat,omitempty"`
}

// dshPiCompat is a model's compat switches. On Anthropic's Messages API
// pi-ai asks a model for a thinking budget unless forceAdaptiveThinking is
// set, and a Claude that thinks only adaptively turns a budget away. Z.ai's
// GLM-5.2 and GLM-5.3 are told so too: a budget can't ask them for max
// (dshKindConfig). On OpenAI's Responses API supportsStrictMode has pi-ai
// say strict: false on an ordinary tool: with strict left out the API
// normalizes a tool's schema as a strict-mode one, where every property is
// required, and a tool's optional parameters come back filled (#3055).
type dshPiCompat struct {
	ForceAdaptiveThinking bool `yaml:"forceAdaptiveThinking,omitempty"`
	SupportsStrictMode    bool `yaml:"supportsStrictMode,omitempty"`
}

// dshAPIs are the wire protocols of llm-pi-ai's (pi-ai's api names) a route
// of magpie's may speak, the gateway serving each: openai-completions at
// /v1/chat/completions, openai-responses at /v1/responses and
// anthropic-messages at /v1/messages. dsh's Models page offers them as
// OpenAI Chat Completions, OpenAI Responses and Anthropic Messages, set for
// a route as a whole (llm-pi-ai has no api of a model's own); the gateway
// takes every model on each.
var dshAPIs = []string{"openai-completions", "openai-responses", "anthropic-messages"}

// dshKind is one of magpie's routes in llm-pi-ai: its key among the
// providers (what agent-default-model and a session name it by), the wire
// protocol it is written with, and the name dsh's model list groups it
// under.
type dshKind struct {
	key, api, name string
}

// dshKinds are magpie's routes, one per protocol a model is best asked on
// (Discord, 01huadalang: 可以按不同协议分成组来注入，例如 chat 一个组，response
// 一个组). llm-pi-ai sets the api for a route as a whole, so one route had
// every model on Chat Completions, and the gateway translated each turn of
// a GPT served on Responses or a Claude served on Messages from Chat: its
// sealed reasoning (Responses' encrypted_content, Messages' signed
// thinking) never went back to it between tool calls. A model goes on the
// route of the API its provider serves it on natively (catalog.Model.APIs,
// as Pi's, omp's and droid's are asked), and every other one — a group, a
// Chat provider's, a sign-in served through its agent's own API — on
// magpie's first route, which keeps its key.
var dshKinds = []dshKind{
	{dshRoute, "openai-completions", "Magpie"},
	{dshRoute + "-responses", "openai-responses", "Magpie · Responses"},
	{dshRoute + "-anthropic", "anthropic-messages", "Magpie · Anthropic"},
}

// isDshRoute reports whether a provider route dsh names is one of magpie's.
func isDshRoute(key string) bool {
	return dshKindOf(key) != nil
}

// dshKindOf is magpie's route of this key, nil for a route of anyone else's.
func dshKindOf(key string) *dshKind {
	for i := range dshKinds {
		if dshKinds[i].key == key {
			return &dshKinds[i]
		}
	}
	return nil
}

// dshRouteOf is the route of magpie's a model goes on: the one for the API
// its provider serves it on natively, the first otherwise.
func dshRouteOf(m catalog.Model) string {
	switch {
	case slices.Contains(m.APIs, string(provider.Responses)):
		return dshKinds[1].key
	case slices.Contains(m.APIs, string(provider.Anthropic)):
		return dshKinds[2].key
	}
	return dshRoute
}

// dshRouteFor is the route of magpie's the catalog model id goes on.
func dshRouteFor(id string, models []catalog.Model) string {
	for _, m := range models {
		if m.ID == id {
			return dshRouteOf(m)
		}
	}
	return dshRoute
}

// dshAPI is the wire protocol a route of magpie's speaks, given the one it
// has now and the one it is written with: the one it has when the gateway
// speaks it — a user who switched a route to OpenAI Responses in dsh's
// Models page keeps it, where each round put Chat Completions back
// (Discord, 01huadalang: 这些协议怎么改 responses，在 dsh 改了一会就会被 magpie
// 接管) — and the route's own otherwise, none or one the gateway has no
// endpoint for.
func dshAPI(cur, own string) string {
	if contains(dshAPIs, cur) {
		return cur
	}
	return own
}

// dshBaseURL is the gateway's address for a route on api. pi-ai hands the
// base to the vendor's SDK as it is, and Anthropic's adds /v1/messages
// itself, so Anthropic's Messages API is at the gateway's root; OpenAI's
// SDK adds only /chat/completions or /responses, so those are at its /v1.
func dshBaseURL(api, gw string) string {
	if api == "anthropic-messages" {
		return gw
	}
	return gw + "/v1"
}

// dshPiLevels maps each of dsh's thinking levels a model takes to the
// reasoning_effort sent for it; a level left out is not offered.
type dshPiLevels struct {
	Off     string `yaml:"off,omitempty"`
	Minimal string `yaml:"minimal,omitempty"`
	Low     string `yaml:"low,omitempty"`
	Medium  string `yaml:"medium,omitempty"`
	High    string `yaml:"high,omitempty"`
	XHigh   string `yaml:"xhigh,omitempty"`
	Max     string `yaml:"max,omitempty"`
}

// dshThinking are dsh's thinking levels (pi-ai's), in its order.
var dshThinking = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}

// dshModelLevels are the levels of dsh's a catalog model takes, each with
// the effort sent for it: the model's own name for it (modelEffort), so a
// plugin's light and extra_high are dsh's low and xhigh and are sent as
// light and extra_high, and off for its none.
func dshModelLevels(efforts []string) ([]string, *dshPiLevels) {
	var levels []string
	var out dshPiLevels
	for _, l := range dshThinking {
		w := l
		if l == "off" {
			w = "none"
		}
		e := modelEffort(efforts, w)
		if e == "" {
			continue
		}
		levels = append(levels, l)
		switch l {
		case "off":
			out.Off = e
		case "minimal":
			out.Minimal = e
		case "low":
			out.Low = e
		case "medium":
			out.Medium = e
		case "high":
			out.High = e
		case "xhigh":
			out.XHigh = e
		case "max":
			out.Max = e
		}
	}
	// dsh turns away a model offering no level but off
	if len(levels) == 0 || len(levels) == 1 && levels[0] == "off" {
		return nil, nil
	}
	return levels, &out
}

// dshLevels are the thinking levels dsh offers for a catalog model.
func dshLevels(list, ref string) []string {
	for _, m := range sharedModels(list) {
		if m.ID == ref {
			levels, _ := dshModelLevels(m.Efforts)
			return levels
		}
	}
	return nil
}

// dshMaxTokens is the maxTokens magpie lists m with. dsh takes a listed
// maxTokens as every request's output and reserves it, with 65536 of
// headroom, out of contextWindow before it decides when to compact
// (compaction-basic's resolveCompactSpec: the threshold is the smaller of
// 80% of the window and window - maxTokens - 65536). A vendor's reply limit
// near its window leaves dsh compacting at a sliver of it, or failing to
// (#2823): 500000 with 393216 compacted at 41248; 524288 with 524288, and
// 131072 with 65536, left it no budget at all. So the vendor's figure is
// held to what leaves dsh half the window to compact in (window/2 - 65536),
// or to a quarter of the window, as VS Code's and Copilot's are, where that
// is more: a window under 262144 has no half to spare. A quarter alone cut
// DeepSeek's 393216 on its 1000000 window to 250000, where dsh compacts at
// 541248 with all of it (#3298); 500000 with 393216 is held to 184464 and
// compacts at 250000. A Max output the user set stays as they set it
// (outputOr).
func dshMaxTokens(m catalog.Model) int {
	if m.Context <= 0 {
		return maxTokens(m)
	}
	room := max(m.Context/4, m.Context/2-dshHeadroom)
	return outputOr(m, min(maxTokens(m), max(1, room)))
}

// dshHeadroom is compaction-basic's default headroomTokens: what dsh keeps
// free beside a request's maxTokens before it compacts.
const dshHeadroom = 65536

// dshRouteConfig is magpie's first route with every one of models on it.
func dshRouteConfig(models []catalog.Model, api, gw string) dshPiRoute {
	return dshKindConfig(dshKinds[0], models, api, gw)
}

// dshKindConfig is magpie's route k for models, of the catalog one round
// read: what the round decides from and what it writes come from the same
// list. api is the wire protocol the route has now, kept when the gateway
// speaks it (dshAPI). A model's reasoningEfforts go on every one of them:
// llm-pi-ai makes them pi-ai's thinkingLevelMap, sent as reasoning_effort
// on Chat Completions, reasoning.effort on Responses and, for a Claude
// thinking adaptively, output_config.effort on Messages.
func dshKindConfig(k dshKind, models []catalog.Model, api, gw string) dshPiRoute {
	api = dshAPI(api, k.api)
	r := dshPiRoute{DisplayName: k.name, APIKeyEnv: dshKeyRef, API: api, BaseURL: dshBaseURL(api, gw), Models: []dshPiModel{}, StreamIdleTimeoutMs: dshIdleWait}
	for _, m := range models {
		e := dshPiModel{ID: m.ID, Name: m.Name, ContextWindow: m.Context}
		if m.Output > 0 {
			e.MaxTokens = dshMaxTokens(m)
		}
		switch {
		case m.Images:
			e.Input = []string{"text", "image"}
		case m.ImageInput != nil:
			e.Input = []string{"text"}
		}
		_, e.ReasoningEfforts = dshModelLevels(m.Efforts)
		// each switch is magpie's own, put as magpie has it now: a user's
		// hand-set one of the same name says what magpie writes again says
		switch {
		// pi-ai's budget path clamps max to high (16384), so a model that
		// takes adaptive thinking with its level (Z.ai's GLM-5.3) is asked
		// adaptively too: pi-ai then sends the level itself, max as max
		case api == "anthropic-messages" && (gateway.AdaptiveThinking(m.ID) || gateway.AdaptiveWithEffort(m.ID)):
			e.Compat = &dshPiCompat{ForceAdaptiveThinking: true}
		case api == "openai-responses":
			// by the api the route stands on (the one the user picked in
			// dsh's Models page kept), not by its name: a tool of a turn
			// asked there with strict left out has every property taken as
			// required, its optional ones filled (#3055)
			e.Compat = &dshPiCompat{SupportsStrictMode: true}
		}
		r.Models = append(r.Models, e)
	}
	return r
}

// dshStartOnItsRoute moves a start (agent-default-model) on a model of
// magpie's to the route that model is on now: dsh finds a model by its
// route and id, and fails a session started on a route that no longer has
// it. A start on a model by an id magpie lists it by no longer (a found
// group's group/auto-<slug>, now auto/<model>; a renamed provider's) moves
// onto the id the route lists: dsh finds no model by the old one, and its
// desktop app showed no model picked (zyh6036 on X). The effort and
// everything else in the entry stay, and so does a start on a model magpie
// no longer gives dsh (dshCheck says so).
func dshStartOnItsRoute(items []dshItem, models []catalog.Model) []dshItem {
	for i, it := range items {
		if it.id != "agent-default-model" {
			continue
		}
		c := dshConfig(it)
		if !isDshRoute(c["provider"]) || c["model"] == "" {
			continue
		}
		want, id := "", c["model"]
		// each ref is worked out only when the one before it isn't listed:
		// PublicRef and RenamedRef of an auto ref look at the whole catalog
		refs := []func(string) string{
			func(id string) string { return id },
			provider.PublicRef,
			provider.RenamedRef,
			func(id string) string { return provider.PublicRef(provider.RenamedRef(id)) },
		}
		for _, of := range refs {
			ref := of(c["model"])
			for _, m := range models {
				if m.ID == ref {
					want, id = dshRouteOf(m), ref
				}
			}
			if want != "" {
				break
			}
		}
		if want == "" || want == c["provider"] && id == c["model"] {
			continue
		}
		n, err := dshNode(it)
		if err != nil {
			continue
		}
		yamlSetKey(yamlMapping(n, "config"), "provider", &yaml.Node{Kind: yaml.ScalarNode, Value: want})
		yamlSetKey(yamlMapping(n, "config"), "model", &yaml.Node{Kind: yaml.ScalarNode, Value: id, Style: yaml.DoubleQuotedStyle})
		if lines, err := dshNodeLines(n); err == nil {
			items[i].lines = lines
		}
	}
	return items
}

func yamlQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// dshProviderLines is the llm-deepseek entry pointing a dsh before 0.1.5 at
// the gateway, with the catalog as the models its /model offers and the key
// in the entry. effort is the thinking effort sessions start with, high
// (dsh's own default) when "".
func dshProviderLines(effort string, models []catalog.Model, gw string) []string {
	if effort == "" {
		effort = "high"
	}
	lines := []string{
		"- id: llm-deepseek " + dshMark,
		"  config:",
		"    apiKey: " + yamlQuote(keyAt(gw)),
		"    baseURL: " + yamlQuote(gw+"/v1"),
		"    thinking: enabled",
		"    reasoningEffort: " + effort,
		"    models:",
	}
	ms := models
	if len(ms) == 0 {
		lines[len(lines)-1] = "    models: []"
	}
	for _, m := range ms {
		lines = append(lines, "      - id: "+yamlQuote(m.ID), "        name: "+yamlQuote(m.Name))
		// what dsh would otherwise take for every model: a million tokens
		// of context, 256K out, and text only
		if m.Context > 0 {
			lines = append(lines, fmt.Sprintf("        contextWindow: %d", m.Context))
		}
		if m.Output > 0 {
			lines = append(lines, fmt.Sprintf("        maxTokens: %d", dshMaxTokens(m)))
		}
		if m.Images {
			lines = append(lines, "        inputModalities: [text, image]")
		}
	}
	return lines
}

// dshDefaultLines is the agent-default-model entry: the model new sessions
// start on, in every entry point, on provider's route, and the thinking
// effort they start with when effort isn't "".
func dshDefaultLines(provider, model, effort string) []string {
	lines := []string{
		"- id: agent-default-model " + dshMark,
		"  config:",
		"    provider: " + provider,
		"    model: " + yamlQuote(model),
	}
	if effort != "" {
		lines = append(lines, "    reasoningEffort: "+yamlQuote(effort))
	}
	return lines
}

// dshLoopLines is the agent-loop entry dsh ships, with model in place.
func dshLoopLines(model string) []string {
	return []string{
		"- id: agent-loop " + dshMark,
		"  config:",
		"    agents:",
		"      - id: main",
		"        provider: deepseek-official",
		"        model: " + yamlQuote(model),
		"        cwd: !!js process.cwd()",
	}
}

// dshRouteLines is the api-gateway entry: the route headless and web
// sessions start on.
func dshRouteLines(model string) []string {
	return []string{
		"- id: api-gateway " + dshMark,
		"  config:",
		"    provider: deepseek-official",
		"    model: " + yamlQuote(model),
	}
}

// dshSync puts the catalog as it is now into magpie's route (its
// llm-deepseek entry before 0.1.5), in every patch list that has it; a
// profile still on dsh's DeepSeek row taken over moves to the route.
// Nothing else in the patch lists changes.
func dshSync(dir, gw string) error {
	return dshSyncFresh(here(""), dir, gw, func() string { return "" })
}

// dshSyncFresh is dshSync for a dsh whose first profile, when it has none
// yet, is fresh's patch list ("" for a dsh before profiles). A model of
// magpie's set in config.yaml, which no dsh since profiles reads, goes to the
// profiles when none of them is wired: magpie wrote it there for a dsh that
// hadn't booted a profile yet (liby233), and dsh's first boot made the
// profile without it.
func dshSyncFresh(at place, dir, gw string, fresh func() string) error {
	dshWrites.Lock()
	defer dshWrites.Unlock()
	models := magpieModels(dshListFor(dir))
	files := dshProfiles(dir)
	made := ""
	if len(files) == 0 {
		if made = fresh(); made == "" {
			return dshSyncLegacy(filepath.Join(dir, "config.yaml"), models, gw)
		}
	}
	if v := dshGetLegacy(filepath.Join(dir, "config.yaml")); dshCarried(v, files) && edit.GetYAMLMap(filepath.Join(dir, "settings.yaml"), "agent-default-model") == nil {
		if made != "" {
			if err := dshMakeProfile(made); err != nil {
				return err
			}
		}
		return dshSetLocked(at, dir, v, gw)
	}
	if len(files) == 0 {
		return nil
	}
	defer func() {
		// the key under the name the route gives it, moved from the old one
		if _, items, err := dshRead(files[0]); err == nil && dshWired(items) {
			dshEnv(at, dir, gw, true, false)
		}
	}()
	for _, f := range files {
		_, items, err := dshRead(f)
		if err != nil {
			continue
		}
		if dshOldWiring(items) {
			if err := dshMove(f, items, models, gw); err != nil {
				return err
			}
			continue
		}
		if !dshWired(items) {
			continue
		}
		if _, err := dshRouteAgain(f, models, gw); err != nil {
			return err
		}
	}
	if err := dshFillNewProfiles(files, models, gw); err != nil {
		return err
	}
	if err := dshMirrorHome(dir, models, gw, false); err != nil {
		return err
	}
	return dshSettingsAgain(dir, models, gw)
}

// dshCarried says v, the model config.yaml starts on, is one of magpie's
// that profiles which have none of magpie's route take over.
func dshCarried(v string, files []string) bool {
	ref, ok := strings.CutPrefix(v, magpieID+"/")
	if !ok || !isMagpie(ref) {
		return false
	}
	for _, f := range files {
		_, items, err := dshRead(f)
		if err != nil || dshWired(items) || dshOldWiring(items) {
			return false
		}
		// a start of the user's own, picked in dsh since
		if i := dshFindLast(items, "agent-default-model"); i >= 0 && !dshOurs(items[i]) {
			return false
		}
	}
	return true
}

// dshStartsOnMagpie reports whether the model a profile's sessions start on
// is one of magpie's, read the way dsh starts sessions.
func dshStartsOnMagpie(dir string, items []dshItem) bool {
	return strings.HasPrefix(dshStart(dir, items), magpieID+"/")
}

// dshRouteAgain writes magpie's route again in one patch list where it is no
// longer what magpie would write, and says whether it wrote; magpie's web
// search entry in a profile goes to the gateway's address with it. It is the one
// thing that may be done again at any time: the route is magpie's list of
// its own models, while the model a session starts on is the user's pick
// (see dshCheck). A patch list without magpie's route is written back only
// while the start still names one of magpie's models — dsh's own save keeps
// a snapshot and can write the list back without the route, and stripping
// the route under a start magpie still owes is not the user taking magpie
// out; a start of dsh's own or of the user's says that they were. One
// written between the read and the write is left as it is: it is the newer
// of the two, and the next round reads it. models is the catalog the round
// read: with none there is no route to write, and writing an empty one
// would take a working list away.
func dshRouteAgain(f string, models []catalog.Model, gw string) (bool, error) {
	what, err := dshRouteAgainSaid(f, models, gw)
	return what != "", err
}

// dshRouteAgainSaid is dshRouteAgain saying what it wrote, for the log:
// "route", or "search" when only magpie's web search entry moved to follow
// the model the profile starts on now — what dsh's own save of a model
// picked in it leaves for the next round, which is not magpie writing over
// that pick (#5222); "" for nothing written.
func dshRouteAgainSaid(f string, models []catalog.Model, gw string) (string, error) {
	if len(models) == 0 {
		return "", nil
	}
	raw, err := edit.Read(f)
	if err != nil {
		return "", nil
	}
	head, items, err := dshParse(f, string(raw))
	if err != nil || !dshWired(items) {
		// no route of magpie's in the list: write it back only where the
		// start still names one of magpie's models — the route was stripped
		// under it, not taken out. dshPutRoute reads the entry dsh wrote in
		// its own style, so the route is added beside the user's own. A list
		// still on the old wiring is dshSync's to move (dshMove), not this
		// round's to write beside.
		if err != nil || dshOldWiring(items) || !dshStartsOnMagpie(dirOf(f), items) {
			return "", nil
		}
		written, err := dshWriteParsed(f, head, items, models, gw, raw)
		if !written {
			return "", err
		}
		return "route", err
	}
	before := dshJoin(items)
	if items, err = dshPutRoute(items, true, models, gw); err != nil {
		return "", nil
	}
	what := "search"
	if dshJoin(items) != before {
		what = "route"
	}
	if dshProfileList(f) {
		items = dshSearchAgain(items, dshProfileHome(f), gw)
	}
	if dshJoin(items) == before {
		return "", nil
	}
	if now, err := edit.Read(f); err != nil || !bytes.Equal(now, raw) {
		return "", nil
	}
	return what, dshWrite(f, head, items)
}

// dshProfileList reports whether f is a profile's own patch list rather than
// the home layer, which applies over every profile's.
func dshProfileList(f string) bool {
	return filepath.Base(filepath.Dir(filepath.Dir(f))) == "profiles"
}

// dirOf is the dsh home a patch list belongs to.
func dirOf(f string) string {
	if dshProfileList(f) {
		return filepath.Dir(filepath.Dir(filepath.Dir(f)))
	}
	return filepath.Dir(f)
}

// dshWriteParsed adds the route to the list as it was read, and writes it
// back when the file still holds what was read. The home layer is only
// written into where it already holds an llm-pi-ai entry of its own: its
// config replaces every profile's whole entry (dshOver), so one magpie added
// there would take the user's own providers out of dsh's list (#804). A
// profile's own list may be given the entry, as magpie's apply path does.
func dshWriteParsed(f string, head []string, items []dshItem, models []catalog.Model, gw string, raw []byte) (bool, error) {
	if !dshProfileList(f) && dshFindLast(items, dshPiRow) < 0 {
		return false, nil
	}
	before := dshJoin(items)
	if items, err := dshPutRoute(items, true, models, gw); err != nil {
		return false, nil
	} else if dshJoin(items) == before {
		return false, nil
	} else if now, err := edit.Read(f); err != nil || !bytes.Equal(now, raw) {
		return false, nil
	} else {
		return true, dshWrite(f, head, items)
	}
}

// dshWiredEvery is how often a magpie serving the gateway looks at dsh's
// patch lists.
const dshWiredEvery = 30 * time.Second

// KeepDshWired keeps magpie's route in dsh's patch lists as magpie's catalog
// is now, for as long as magpie serves the gateway. dsh reads a patch list
// live, so one left behind by something else writing the file — dsh's own
// Models page, an older magpie — fails every session there with no such
// configured model until the route is written again. What is written is
// magpie's own list, never the model a session starts on.
func KeepDshWired(ctx context.Context) {
	keepDshWired(ctx, dshWiredEvery)
}

// keepDshWired is KeepDshWired at an interval the tests can shorten. One
// round at a time, each after the one before it has finished, and a round
// that could not write something is said once rather than every round.
func keepDshWired(ctx context.Context, every time.Duration) {
	var said dshSaidOnce
	rounds := dshHomesRounds()
	for {
		if trouble := dshRoundsOnce(rounds); said.first(trouble) {
			log.Print(trouble)
		}
		select {
		case <-ctx.Done():
			return
		case <-beat.Next(every):
		}
	}
}

// dshSaidOnce is what a round could not write, as it was last said: a round
// repeating it says nothing, so a profile magpie cannot write to is reported
// once rather than every 30s.
type dshSaidOnce struct{ last string }

// first reports whether this trouble has not been said yet, and is to be said.
func (s *dshSaidOnce) first(trouble string) bool {
	if trouble == s.last {
		return false
	}
	s.last = trouble
	return trouble != ""
}

// dshModelsFor is how long a round takes magpie's models as built before
// while provider.StampNow says nothing changed: what the stamp can't see
// (another app rewriting an account's file, a model retired by its date)
// reaches dsh's lists within it, and a new day builds them again for the
// models retired by date. At 5 minutes each build (~42 MB allocated with a
// real catalog) was most of what an idle magpie allocated.
const dshModelsFor = 30 * time.Minute

// dshModelsStale says whether models built at at are to be built again now.
func dshModelsStale(at, now time.Time) bool {
	if now.Sub(at) >= dshModelsFor {
		return true
	}
	y1, m1, d1 := at.Date()
	y2, m2, d2 := now.Date()
	return y1 != y2 || m1 != m2 || d1 != d2
}

// dshHomesRounds are a round for each dsh home magpie keeps wired: dsh's
// own, and DS Bot's, whose app runs its own dsh on a home of its own
// (dsbotHome) and reads its patch list live as dsh does.
func dshHomesRounds() []*dshRounds {
	return []*dshRounds{{}, {home: dsbotDataDir, list: dsbotID}}
}

// dshRoundsOnce does each of rounds once, and says what any could not write.
func dshRoundsOnce(rounds []*dshRounds) string {
	var trouble []string
	for _, r := range rounds {
		if t := r.once(); t != "" {
			trouble = append(trouble, t)
		}
	}
	return strings.Join(trouble, "; ")
}

// dshRounds is what keepDshWired's last round read, so that a round with
// nothing changed since does nothing. Every 30 seconds a round built
// magpie's whole catalog and every route again to write nothing: 12 GB of
// the 35.7 GB a user's magpie allocated in 48 minutes, with ~157 MB live.
// A round is done again when dsh's files it reads, the gateway's address
// or magpie's models are other than when the last one found nothing to
// write. The zero dshRounds does every round.
type dshRounds struct {
	// home is the dsh home the rounds keep wired, "" when there is none
	// or it can't be told; nil: dsh's own (dshHome). list is the agent
	// whose models are written there; "": dsh's.
	home func() string
	list string
	// the stamp and time the models were last built at, their sum in
	// last: the list itself is not kept between rounds, where it was the
	// largest thing an idle magpie held (~9 MB with real accounts, and the
	// catalog it was built from with it), and is built again only for a
	// round that writes
	stamp provider.Stamp
	at    time.Time
	built bool
	// what the last round went by, and whether it wrote nothing, nothing
	// else wrote while it ran, and nothing troubled it
	last dshRoundKey
	done bool
	// the last magpie's rounds were looked at (dshRoundKept)
	seeded bool
}

// dshRoundKey is what a round goes by: dsh's files it reads, the gateway's
// address and magpie's models, each as a sum.
type dshRoundKey struct {
	files  uint64
	gw     string
	models uint64
}

// dshFilesSum sums the files a round reads, by path and bytes: each
// profile's patch list, the home layer, and the settings, keys and old
// config dsh reads beside them. A file that can't be read for another
// reason than not being there sums differently every time: unknown, never
// taken for what it was last round. The sum is the same in every magpie
// process, so a start can go by the last one's (dshRoundKept).
func dshFilesSum(dir string, files []string) uint64 {
	h := sha256.New()
	bp := dshSumBufs.Get().(*[]byte)
	defer dshSumBufs.Put(bp)
	sum := func(f string) {
		io.WriteString(h, f)
		h.Write([]byte{0})
		switch err := dshSumFile(h, f, *bp); {
		case err == nil:
		case errors.Is(err, os.ErrNotExist):
			h.Write([]byte{2})
		default:
			io.WriteString(h, time.Now().String())
		}
		h.Write([]byte{0})
	}
	for _, f := range files {
		sum(f)
	}
	for _, name := range []string{"cordis.patch.yml", "settings.yaml", ".env", ".credentials.yaml", "config.yaml"} {
		sum(filepath.Join(dir, name))
	}
	return dshSum64(h.Sum(nil))
}

// dshSum64 is a sha256 sum's first 8 bytes.
func dshSum64(b []byte) uint64 { return binary.BigEndian.Uint64(b[:8]) }

// dshSumFile writes 1 and f's bytes into h, read through buf rather than
// whole: a patch list names every model magpie serves, and each round of
// both dsh homes read every file of them into memory again only to hash
// it. The sum is the same as for the bytes written at once.
func dshSumFile(h io.Writer, f string, buf []byte) error {
	r, err := os.Open(f)
	if err != nil {
		return err
	}
	defer r.Close()
	h.Write([]byte{1})
	for {
		n, err := r.Read(buf)
		h.Write(buf[:n])
		switch {
		case err == io.EOF:
			return nil
		case err != nil:
			return err
		}
	}
}

// dshSumBufs are dshFilesSum's read buffers.
var dshSumBufs = sync.Pool{New: func() any { b := make([]byte, 32<<10); return &b }}

// dshModelsSum sums the models a round writes the route from.
func dshModelsSum(models []catalog.Model) uint64 {
	b, err := json.Marshal(models)
	if err != nil {
		return uint64(time.Now().UnixNano()) // unknown: done again
	}
	sum := sha256.Sum256(b)
	return dshSum64(sum[:])
}

// dshWiredOnce writes magpie's route again in every patch list whose route
// is not what magpie would write now, in each dsh home, and says what could
// not be written.
func dshWiredOnce() string {
	return dshRoundsOnce(dshHomesRounds())
}

// once is dshWiredOnce, done only when something the round reads changed
// since r's last round that found nothing to write.
func (r *dshRounds) once() string {
	dshWrites.Lock()
	defer dshWrites.Unlock()
	home, list := r.home, r.list
	if home == nil {
		home = dshHome
	}
	if list == "" {
		list = "dsh"
	}
	dir := home()
	if dir == "" {
		r.done = false
		return ""
	}
	files := dshProfiles(dir)
	if len(files) == 0 {
		// no dsh: the catalog isn't built for nothing every round, ~34 MB
		// of garbage a minute in a magpie at rest
		r.done = false
		return ""
	}
	// one catalog for the whole round: outside a Hold, each PublicRef,
	// RenamedRef and Exposed the round asks built every provider's models
	// again, 158 MB of what a real account's magpie allocated in its first
	// minute
	defer provider.Hold()()
	key := dshRoundKey{files: dshFilesSum(dir, files), gw: gateway.URL(), models: r.last.models}
	// the stamp is read before the models are built: a change while they
	// are moves it past the one kept, and the next round builds them again
	var models []catalog.Model
	if stamp := provider.StampNow(); !r.built || stamp != r.stamp || dshModelsStale(r.at, time.Now()) {
		models = magpieModels(list)
		r.stamp, r.at, r.built = stamp, time.Now(), true
		key.models = dshModelsSum(models)
	}
	if !r.done && !r.seeded {
		// a start's first round: the last magpie's, when it found nothing
		// to write with what this one reads
		r.seeded = true
		r.last, r.done = dshRoundKept(dir, list)
	}
	if r.done && key == r.last {
		return ""
	}
	if models == nil {
		models = magpieModels(list)
	}
	dshRoundsWorked.Add(1)
	trouble := dshWiredWith(dir, files, models)
	// kept only when the round wrote nothing and nothing else wrote while
	// it ran: a file written by either is read again next round
	r.last, r.done = key, trouble == "" && dshFilesSum(dir, dshProfiles(dir)) == key.files
	if r.done {
		dshKeepRound(dir, list, key)
	}
	return trouble
}

// dshWiredWith is a round's work, with the models it writes the route from.
func dshWiredWith(dir string, files []string, models []catalog.Model) string {
	var trouble []string
	// a prefix a local proxy has put before the gateway stays (DshThrough)
	gw := dshReach(dir, gateway.URL())
	// a profile dsh has just made — the desktop app's, opened for the first
	// time while magpie runs — is its empty template: given magpie's route
	// and start as the catalog sync would, rather than left listing dsh's
	// own models alone until magpie's next start (star on Discord). One with
	// entries of its own and no route is the user's, left to Reapply.
	fill := []string{}
	fresh := false
	for _, f := range files {
		_, items, err := dshRead(f)
		switch {
		case err != nil:
		case len(items) == 0:
			fill, fresh = append(fill, f), true
		case dshWired(items):
			fill = append(fill, f)
		}
	}
	if fresh {
		if err := dshFillNewProfiles(fill, models, gw); err != nil {
			trouble = append(trouble, "giving dsh's new profile magpie's route: "+dshWriteError(err))
		}
	}
	files = append(files, dshHomePatch(dir)) // left as it is without magpie's route
	for _, f := range files {
		what, err := dshRouteAgainSaid(f, models, gw)
		if err != nil {
			trouble = append(trouble, fmt.Sprintf("writing dsh's route again in %s: %s", f, dshWriteError(err)))
			continue
		}
		switch what {
		case "route":
			log.Printf("wrote dsh's route again in %s", f)
		case "search":
			// agent-default-model left as dsh saved it (#5222)
			log.Printf("dsh's web search in %s follows the model it starts on now", f)
		}
	}
	// deepseek-acp's, which dsh 0.2 takes away at each start
	if err := dshSettingsAgain(dir, models, gw); err != nil {
		trouble = append(trouble, "writing dsh's route again in "+dshSettings(dir)+": "+dshWriteError(err))
	}
	return strings.Join(trouble, "; ")
}

// dshWriteError is why a write failed, without the name of the temporary file
// the atomic write makes: that name is new every attempt, and a trouble that
// reads differently every round would be said every round (see dshSaidOnce).
func dshWriteError(err error) string {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Op + ": " + pe.Err.Error()
	}
	return err.Error()
}

// dshHome is where dsh keeps its profiles: $DSH_HOME, or ~/.dsh.
func dshHome() string {
	if dir := appdir.Getenv("DSH_HOME"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".dsh")
}

// dshMove moves a profile magpie wired by taking over dsh's DeepSeek row
// onto magpie's route, the model and effort kept.
func dshMove(f string, items []dshItem, models []catalog.Model, gw string) error {
	model := ""
	if i := dshFindLast(items, "agent-default-model"); i >= 0 {
		if c := dshConfig(items[i]); c["provider"] == "deepseek-official" {
			model = c["model"]
		}
	}
	if model == "" || !isMagpie(model) {
		// nothing of magpie's to start on: dsh's own row back alone
		head, items, err := dshRead(f)
		if err != nil {
			return err
		}
		if old := unstash(dshStashKey(f, "llm-deepseek")); old != "" {
			items[dshFind(items, "llm-deepseek")] = dshItem{id: "llm-deepseek", lines: strings.Split(old, "\n")}
		} else {
			i := dshFind(items, "llm-deepseek")
			items = append(items[:i], items[i+1:]...)
		}
		return dshWrite(f, head, items)
	}
	return dshSetFile(f, magpieID+"/"+model, true, models, gw)
}

// dshSyncLegacy is dshSync on a config.yaml before 0.1.5.
func dshSyncLegacy(f string, models []catalog.Model, gw string) error {
	head, items, err := dshRead(f)
	if err != nil {
		return nil
	}
	i := dshFind(items, "llm-deepseek")
	if i < 0 || !items[i].magpie {
		return nil
	}
	if len(models) == 0 {
		return nil
	}
	lines := dshProviderLines(dshEffortIn(items), models, gw)
	if strings.Join(lines, "\n") == strings.Join(items[i].lines, "\n") {
		return nil
	}
	items[i].lines = lines
	return dshWrite(f, head, items)
}

// dshFillNewProfiles gives a profile dsh made after magpie set it up — the
// desktop app's, opened for the first time after the web's was wired —
// magpie's route, and the model magpie set in the others when it starts on
// none of the user's: until then it lists dsh's own models alone. Where no
// wired profile starts on magpie's route (the user picked one of dsh's own
// there), the route alone is given, beside dsh's models (zyh6036 on X).
func dshFillNewProfiles(files []string, models []catalog.Model, gw string) error {
	if len(models) == 0 {
		return nil
	}
	model, wired := "", false
	var bare []string
	for _, f := range files {
		_, items, err := dshRead(f)
		if err != nil {
			continue
		}
		if !dshWired(items) {
			bare = append(bare, f)
			continue
		}
		wired = true
		if i := dshFindLast(items, "agent-default-model"); i >= 0 && model == "" {
			if c := dshConfig(items[i]); isDshRoute(c["provider"]) {
				model = c["model"]
			}
		}
	}
	if !wired {
		return nil // dsh not set up through magpie: nothing to give
	}
	for _, f := range bare {
		head, items, err := dshRead(f)
		if err != nil {
			continue
		}
		if i := dshFind(items, "agent-default-model"); model != "" && (i < 0 || dshOurs(items[i])) {
			if err := dshSetFile(f, magpieID+"/"+model, true, models, gw); err != nil {
				return err
			}
			continue
		}
		// a model of the user's: magpie's are listed beside it
		if items, err = dshPutRoute(items, true, models, gw); err != nil {
			continue
		}
		if err := dshWrite(f, head, items); err != nil {
			return err
		}
	}
	return nil
}

// dshEfforts are the thinking efforts dsh's own DeepSeek row takes: its
// reasoningEffort is off, low, high or max (low since 0.1.1), sent as
// reasoning_effort before 0.1.7 and output_config.effort from it on.
var dshEfforts = []string{"off", "low", "high", "max"}

// dshEffortIn is the reasoningEffort of the llm-deepseek entry magpie
// wrote, "" when there is none.
func dshEffortIn(items []dshItem) string {
	i := dshFind(items, "llm-deepseek")
	if i < 0 || !items[i].magpie {
		return ""
	}
	for _, l := range items[i].lines {
		if k, v, ok := strings.Cut(strings.TrimSpace(l), ":"); ok && k == "reasoningEffort" {
			return yamlScalar(strings.TrimSpace(v))
		}
	}
	return ""
}

// dshEffortOf is the effort sessions start with in a profile (0.1.5 on):
// agent-default-model's, else, not yet moved, magpie's llm-deepseek's.
func dshEffortOf(items []dshItem) string {
	if i := dshFindLast(items, "agent-default-model"); i >= 0 {
		if e := dshConfig(items[i])["reasoningEffort"]; e != "" {
			return e
		}
	}
	return dshEffortIn(items)
}

// dshFiles are the patch lists magpie writes: every profile's, else
// config.yaml.
func dshFiles(dir string) []string {
	if files := dshProfiles(dir); len(files) > 0 {
		return files
	}
	return []string{filepath.Join(dir, "config.yaml")}
}

// DshPatchFiles are the patch lists of the dsh whose home is dir, as magpie
// writes them: every profile's (web's first), else config.yaml.
func DshPatchFiles(dir string) []string { return dshFiles(dir) }

// dshGetEffort reads the effort sessions start with, in the lead profile
// (dshLead).
func dshGetEffort(dir, lead string) string {
	f := dshLead(dir, lead)
	if f == "" {
		_, items, err := dshLook(filepath.Join(dir, "config.yaml"))
		if err != nil {
			return ""
		}
		return dshEffortIn(items)
	}
	_, items, err := dshLook(f)
	if err != nil {
		return ""
	}
	return dshEffortOf(dshOver(dir, items))
}

// dshSetEffort writes v as the effort sessions start with: into
// agent-default-model (0.1.5 on), or into magpie's llm-deepseek entry
// before, dsh's own row being used whole when there is none. The levels
// offered are the lead profile's model's (dshLead), as the picker shows it.
func dshSetEffort(dir, v, gw, lead string) error {
	dshWrites.Lock()
	defer dshWrites.Unlock()
	files := dshProfiles(dir)
	if len(files) > 0 {
		if dshHomeHas(dir, "agent-default-model") {
			files = append(files, dshHomePatch(dir))
		}
		model := dshGet(dir, lead)
		ref, viaGateway := strings.CutPrefix(model, magpieID+"/")
		levels := dshEfforts
		if viaGateway {
			levels = dshLevels(dshListFor(dir), ref)
		}
		route := dshRoute
		if viaGateway {
			route = dshRouteFor(ref, magpieModels(dshListFor(dir)))
		}
		if v != "" && len(levels) == 0 {
			return fmt.Errorf("DeepSeek Harness has no thinking levels for %s: magpie knows of none the model takes", ref)
		}
		if v != "" && !contains(levels, v) {
			return fmt.Errorf("DeepSeek Harness takes an effort of %s for this model, not %q", strings.Join(levels, ", "), v)
		}
		if model == "" {
			if v == "" {
				return nil
			}
			return fmt.Errorf("pick a model for DeepSeek Harness first; the effort is kept with it")
		}
		for _, f := range files {
			head, items, err := dshRead(f)
			if err != nil {
				continue
			}
			// each profile keeps the model it starts on
			p, m := "deepseek-official", ref
			if viaGateway {
				p = route
			}
			if i := dshConfigIndex(items, "agent-default-model"); i >= 0 {
				if c := dshConfig(items[i]); c["model"] != "" && c["provider"] != "" {
					p, m = c["provider"], c["model"]
				}
			}
			i := dshConfigIndex(items, "agent-default-model")
			if i < 0 {
				i = dshFindLast(items, "agent-default-model")
			}
			if i >= 0 && !dshOurs(items[i]) {
				stash(map[string]string{dshStashKey(f, "agent-default-model"): strings.Join(items[i].lines, "\n")})
			}
			it := dshItem{id: "agent-default-model", magpie: true, lines: dshDefaultLines(p, m, v)}
			if i >= 0 {
				items[i] = it
			} else {
				items = append(items, it)
			}
			if err := dshWrite(f, head, items); err != nil {
				return err
			}
		}
		return nil
	}

	if v != "" && !contains(dshEfforts, v) {
		return fmt.Errorf("DeepSeek Harness takes an effort of %s, not %q", strings.Join(dshEfforts, ", "), v)
	}
	f := filepath.Join(dir, "config.yaml")
	head, items, err := dshRead(f)
	if err == nil {
		if i := dshFind(items, "llm-deepseek"); i >= 0 && items[i].magpie {
			models := magpieModels(dshListFor(dir))
			if len(models) == 0 {
				return fmt.Errorf("DeepSeek Harness has no models to write: magpie has none to hand it")
			}
			items[i].lines = dshProviderLines(v, models, gw)
			return dshWrite(f, head, items)
		}
	}
	if v != "" {
		return fmt.Errorf("pick a model through magpie for DeepSeek Harness first; the effort is kept with magpie's DeepSeek entry")
	}
	return nil
}

// dshSettingsEndpoint reports whether dsh's own settings carry an
// llm-deepseek section with an endpoint or key, which dsh puts over the
// patch list.
func dshSettingsEndpoint(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	in := false
	for _, l := range splitLinesKeep(string(b)) {
		if l != "" && !strings.HasPrefix(l, " ") && !strings.HasPrefix(l, "#") {
			in = strings.HasPrefix(l, "llm-deepseek:")
			continue
		}
		t := strings.TrimSpace(l)
		if in && (strings.HasPrefix(t, "baseURL:") || strings.HasPrefix(t, "apiKey:")) {
			return true
		}
	}
	return false
}

// what dsh says after a change (notice.go)
var (
	noticeDsh1 = newNotice("New dsh sessions start on this. A session made before keeps its model, open now or reopened later, until you pick another in it with /model or the model button by its message box. A dsh started with -p or in a terminal reads it at start-up.")
	noticeDsh2 = newNotice("dsh reads its config at start-up — restart open dsh sessions to use this.")
	noticeDsh3 = newNotice("dsh's web search goes through magpie, which has nothing to search with yet: set up a provider that searches by itself (DeepSeek, Claude, Codex, …) or a search API under Settings › Web search.")
	noticeDsh4 = newNotice("~/.dsh/settings.yaml sets its own DeepSeek endpoint or key, which dsh puts over magpie's; clear it in dsh's Models page to go through magpie.")
	noticeDsh5 = newNotice("DeepSeek Harness Desktop hasn't been opened yet: it starts on this the first time it opens.")
)

// dshDocStart says whether l is YAML's document start marker, "---" at the
// left edge with at most a comment after it.
func dshDocStart(l string) bool {
	r, ok := strings.CutPrefix(l, "---")
	if !ok {
		return false
	}
	if r == "" {
		return true
	}
	if r[0] != ' ' && r[0] != '\t' {
		return false
	}
	r = strings.TrimSpace(r)
	return r == "" || strings.HasPrefix(r, "#")
}

// dshRoundRecord is what the last magpie's rounds found nothing to write with:
// a start's first round reads every dsh profile through the yaml emitter,
// ~43 MB of a real account's first minute, to learn the same again.
// It is this magpie version's only: another may write the files otherwise.
type dshRoundRecord struct {
	Version string                      `json:"version"`
	Rounds  map[string]dshRoundRecorded `json:"rounds"`
}

type dshRoundRecorded struct {
	Files  uint64 `json:"files"`
	GW     string `json:"gw"`
	Models uint64 `json:"models"`
}

var dshKeptMu sync.Mutex

// dshRoundsWorked counts the rounds that read and wrote dsh's files.
var dshRoundsWorked satomic.Int64

func dshKeptPath() string {
	c := appdir.Cache()
	if c == "" {
		return ""
	}
	return filepath.Join(c, "dsh-rounds.json")
}

// dshKeptRead is the record, or none when it can't be read, is
// another version's or this magpie is a dev build.
func dshKeptRead() (dshRoundRecord, bool) {
	p := dshKeptPath()
	if p == "" || gateway.Version == "" || gateway.Version == "dev" {
		return dshRoundRecord{}, false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return dshRoundRecord{}, false
	}
	var k dshRoundRecord
	if json.Unmarshal(b, &k) != nil || k.Version != gateway.Version {
		return dshRoundRecord{}, false
	}
	return k, true
}

// dshRoundKept is the key the last magpie's round of dir and list ended
// on with nothing to write, and whether there is one.
func dshRoundKept(dir, list string) (dshRoundKey, bool) {
	dshKeptMu.Lock()
	defer dshKeptMu.Unlock()
	k, ok := dshKeptRead()
	if !ok {
		return dshRoundKey{}, false
	}
	r, ok := k.Rounds[dir+"\x00"+list]
	if !ok {
		return dshRoundKey{}, false
	}
	return dshRoundKey{files: r.Files, gw: r.GW, models: r.Models}, true
}

// dshKeepRound records a round that found nothing to write, for the next
// magpie's first round.
func dshKeepRound(dir, list string, key dshRoundKey) {
	p := dshKeptPath()
	if p == "" || gateway.Version == "" || gateway.Version == "dev" {
		return
	}
	dshKeptMu.Lock()
	defer dshKeptMu.Unlock()
	k, ok := dshKeptRead()
	if !ok {
		k = dshRoundRecord{Version: gateway.Version}
	}
	r := dshRoundRecorded{Files: key.files, GW: key.gw, Models: key.models}
	id := dir + "\x00" + list
	if was, ok := k.Rounds[id]; ok && was == r {
		return
	}
	if k.Rounds == nil {
		k.Rounds = map[string]dshRoundRecorded{}
	}
	k.Rounds[id] = r
	b, err := json.Marshal(k)
	if err != nil {
		return
	}
	os.MkdirAll(filepath.Dir(p), 0o700)
	edit.WriteAtomic(p, b)
}
