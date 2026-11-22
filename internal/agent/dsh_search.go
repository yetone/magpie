package agent

// dsh's web search is a plugin of its own, web-search-deepseek: each search
// is one Anthropic Messages request with the web_search_20250305 server
// tool to <baseURL>/messages, DeepSeek's by default, signed with the key
// its apiKeyEnv names (DEEPSEEK_API_KEY). A dsh that reaches its models
// through magpie alone has no such key, and every search failed asking for
// one (Packing1 on Discord). While dsh starts on a model of magpie's, magpie
// points the row at the gateway, with its own key and that model, or the
// searcher Settings › Models › Web search names when the user named one
// (dshSearchModel): the gateway answers the server tool as Anthropic does,
// by the model's own search or magpie's (web_search_tool_result blocks
// either way). A row of
// the user's, or a DeepSeek key dsh has of its own, is left to search as it
// does.

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
)

// dshSearchRow is dsh's DeepSeek web search row, and its plugin.
const (
	dshSearchRow    = "web-search-deepseek"
	dshSearchPlugin = "@deepseek-ai/dsh-web-search-deepseek"
	dshSearchKeyRef = "DEEPSEEK_API_KEY"
)

// dshSearchModel is the model magpie's search row asks, for a dsh that
// starts on start (a model of magpie's, without its prefix): the searcher
// Settings › Models › Web search names, when the user named one, else start. Asking
// start sent every search to the model dsh works on, which, unable to
// search by itself, asked the named searcher in turn: two requests a
// search, and a model that takes one call at a time timed out (我只会玩单机
// on Discord).
func dshSearchModel(start string) string {
	if m := gateway.NamedSearchModel(); m != "" {
		return m
	}
	return start
}

// dshSearchLines is magpie's web-search-deepseek entry: the gateway's
// Messages API (the plugin adds /messages to the base) on its key, asking
// model.
func dshSearchLines(model, gw string) []string {
	return []string{
		"- id: " + dshSearchRow + " " + dshMark,
		"  name: " + yamlQuote(dshSearchPlugin),
		"  config:",
		"    apiKeyEnv: " + dshKeyRef,
		"    baseURL: " + yamlQuote(gw+"/v1"),
		"    model: " + yamlQuote(model),
	}
}

// dshOwnSearchKey reports whether dsh has a DeepSeek key of its own to
// search with: in its .env, or in its own key store (dsh's Models page).
func dshOwnSearchKey(dir string) bool {
	if v, ok := edit.GetEnvFile(filepath.Join(dir, ".env"), dshSearchKeyRef); ok && strings.TrimSpace(v) != "" {
		return true
	}
	creds := filepath.Join(dir, ".credentials.yaml")
	if v, ok := edit.GetYAML(creds, "refs."+dshSearchKeyRef); ok && v != "" {
		return true
	}
	return edit.GetYAMLMap(creds, "records."+dshSearchKeyRef) != nil
}

// dshProfileHome is the dsh home a profile's patch list is in:
// <home>/profiles/<name>/cordis.patch.yml.
func dshProfileHome(path string) string {
	return filepath.Dir(filepath.Dir(filepath.Dir(path)))
}

// dshPutSearch points dsh's web search at the gateway, asking model, or with
// model "" takes magpie's entry out, in patch list f. An entry of the
// user's stays as it is, and so does dsh's own search when it has a
// DeepSeek key to search with. While magpie's entry is there, dsh is also
// told to search with it (dshPinSearch).
func dshPutSearch(items []dshItem, f, model, gw string) []dshItem {
	items = dshPutSearchRow(items, dshProfileHome(f), model, gw)
	i := dshFindLast(items, dshSearchRow)
	return dshPinSearch(items, f, i >= 0 && items[i].magpie)
}

// dshPutSearchRow is dshPutSearch's web-search-deepseek entry, in the patch
// list of the dsh home dir.
func dshPutSearchRow(items []dshItem, dir, model, gw string) []dshItem {
	i := dshFindLast(items, dshSearchRow)
	if i >= 0 && !items[i].magpie {
		return items
	}
	if model == "" || dshOwnSearchKey(dir) {
		if i >= 0 {
			items = append(items[:i], items[i+1:]...)
		}
		return items
	}
	it := dshItem{id: dshSearchRow, magpie: true, lines: dshSearchLines(model, gw)}
	if i >= 0 {
		items[i] = it
	} else {
		items = append(items, it)
	}
	return items
}

// dshSearchAgain writes magpie's search entry again for the gateway at gw,
// in patch list f: the gateway's address may have moved since, and the
// searcher Settings names may have been picked or cleared. With none named,
// the entry asks the magpie model dsh starts on, else keeps the model it
// asks.
func dshSearchAgain(items []dshItem, f, gw string) []dshItem {
	i := dshFindLast(items, dshSearchRow)
	if i < 0 || !items[i].magpie {
		return dshPinSearch(items, f, false)
	}
	model := dshConfig(items[i])["model"]
	if start, ok := strings.CutPrefix(dshStart(dshProfileHome(f), items), magpieID+"/"); ok {
		model = start
	}
	if model == "" {
		return items
	}
	return dshPutSearch(items, f, dshSearchModel(model), gw)
}

// dsh-web (the web row) picks the provider each web search runs on. Up to
// 0.2.2-alpha.2 dsh's base composition named deepseek-official, the
// web-search-deepseek plugin, for every search, so magpie's entry for that
// plugin served them all. 0.2.2-rc.3 named deepseek-search-api instead,
// and 0.2.2-rc.5 names none, so dsh-web picks deepseek-official on
// dsh's own DeepSeek routes and deepseek-search-api, DeepSeek's Search API
// ("Incurs charges, paid by the account or API key chosen under Billing"),
// on every other: a session on a model of magpie's searched there, past
// magpie, on the user's DeepSeek balance (#7461). While magpie's search
// entry is there, magpie's web row names deepseek-official as every base
// before 0.2.2 did, beside the fetchProvider every base names (a patch
// entry's config replaces the base's whole), so to a dsh before 0.2.2 it
// changes nothing.
const (
	dshWebRow    = "web"
	dshWebPlugin = "@deepseek-ai/dsh-web"
	dshWebSearch = "deepseek-official"
	dshWebFetch  = "http"
	// dsh-web's own override of the provider, which a config's searchProvider
	// overrides in turn (WebRuntime.selectSearchProvider)
	dshWebSearchEnv = "DSH_WEB_SEARCH_PROVIDER"
)

// dshWebLines is magpie's web row.
func dshWebLines() []string {
	return []string{
		"- id: " + dshWebRow + " " + dshMark,
		"  name: " + yamlQuote(dshWebPlugin),
		"  config:",
		"    fetchProvider: " + dshWebFetch,
		"    searchProvider: " + dshWebSearch,
	}
}

// dshWebOurs reports whether a web row is magpie's as magpie wrote it. dsh's
// settings page keeps the mark when it writes another provider into it.
func dshWebOurs(it dshItem) bool {
	if !it.magpie {
		return false
	}
	c := dshConfig(it)
	return len(c) == 2 && c["searchProvider"] == dshWebSearch && c["fetchProvider"] == dshWebFetch
}

// dshWebPinKey records that magpie's web row was in patch list f.
func dshWebPinKey(f string) string { return dshStashKey(f, "web-pinned") }

// dshPinSearch puts magpie's web row in patch list f (on), or takes it out.
// A web row of the user's, or one dsh's settings page changed, stays, and
// none is written where the user picked a provider in dsh's home patch or
// its DSH_WEB_SEARCH_PROVIDER, or where magpie's row was before and is gone:
// "Automatic" on dsh's Web search page takes the row out (its config is the
// base's then).
func dshPinSearch(items []dshItem, f string, on bool) []dshItem {
	i := dshFindLast(items, dshWebRow)
	ours := i >= 0 && dshWebOurs(items[i])
	if !on {
		if ours {
			items = append(items[:i:i], items[i+1:]...)
			stash(map[string]string{dshWebPinKey(f): ""})
		}
		return items
	}
	if i >= 0 || unstashPeek(dshWebPinKey(f)) != "" || dshSearchPicked(dshProfileHome(f)) {
		return items
	}
	stash(map[string]string{dshWebPinKey(f): "1"})
	return append(items, dshItem{id: dshWebRow, magpie: true, lines: dshWebLines()})
}

// dshSearchPicked reports whether the user picked dsh's search provider
// outside the profile's patch list: a web row in the home patch, which dsh
// applies over the profile's, or DSH_WEB_SEARCH_PROVIDER in dsh's .env or
// here. A home patch that can't be read counts as picked.
func dshSearchPicked(dir string) bool {
	if v, ok := edit.GetEnvFile(filepath.Join(dir, ".env"), dshWebSearchEnv); ok && strings.TrimSpace(v) != "" {
		return true
	}
	if strings.TrimSpace(os.Getenv(dshWebSearchEnv)) != "" {
		return true
	}
	_, home, err := dshLook(dshHomePatch(dir))
	return err != nil || dshFindLast(home, dshWebRow) >= 0
}

// dshSearchesHere reports whether dsh's web search goes through magpie: its
// first profile has magpie's entry.
func dshSearchesHere(dir string) bool {
	files := dshProfiles(dir)
	if len(files) == 0 {
		return false
	}
	_, items, err := dshLook(files[0])
	if err != nil {
		return false
	}
	i := dshFindLast(items, dshSearchRow)
	return i >= 0 && items[i].magpie
}
