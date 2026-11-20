package agent

// Kigi (ELY-Open-Source/Kigi-CLI) is a community fork of Grok Build that
// signs in to many vendors' plans. It keeps Grok Build's config.toml, in
// ~/.kigi (or $KIGI_SHARE_DIR): [models] default and
// default_reasoning_effort, and models of the user's own as [model."<id>"]
// tables with model, base_url, api_key, api_backend and context_window, as
// Kigi's ModelEntryConfig reads them. So magpie wires it as it wires Grok
// Build (grokLikeIn): one "magpie/<provider>/<model>" table per catalog
// model, pointed at the gateway with magpie's own key, and the default
// picked here. Kigi compacts at 85% of a model's window as Grok Build does
// (its DEFAULT_AUTO_COMPACT_THRESHOLD_PERCENT), and reads the same
// [features] campaigns switch over its remote campaign patches.
//
// Its User-Agent is "kigi/<version> (<os>; <arch>)".

import (
	"path/filepath"
	"strconv"
	"strings"

	"github.com/yetone/magpie/internal/edit"
)

func kigi(home string) *Agent { return kigiIn(here(home)) }

// kigiIn is Kigi at a place: this machine's home, or a WSL distro's (see
// wsl.go), where KIGI_SHARE_DIR isn't read.
func kigiIn(at place) *Agent { return grokLikeIn(kigiKind, at) }

var kigiKind = grokKind{
	id: "kigi", name: "Kigi", icon: "kigi",
	aliases: []string{"kigi-cli"},
	ua:      []string{"kigi"},
	env:     "KIGI_SHARE_DIR", dir: ".kigi",
	proc: `(^|/)kigi( |$)`, notice: noticeKigi,
	options: func(dir, path, cur string) []Option {
		return withVia(kigiOwnOptions(dir, path, cur), "kigi", magpieID+"/")
	},
}

// kigiOwnOptions are the models Kigi offers on its own sign-ins and keys:
// as it last listed them (models_cache.json in its home, the shape Grok
// Build's has), then the user's own [model."<id>"] tables, and the current
// one. Kigi asks each itself, so each says whose endpoint it is (Direct).
// dir and path empty: a stopped distro's, whose files aren't read.
func kigiOwnOptions(dir, path, cur string) []Option {
	seen := map[string]bool{}
	var out []Option
	add := func(id, name, direct string) {
		if id == "" || seen[id] || strings.HasPrefix(id, magpieID+"/") {
			return
		}
		seen[id] = true
		o := Option{Value: id, Icon: modelIcon("", id), Direct: direct}
		if name != "" && name != id {
			o.Label = name
		}
		out = append(out, o)
	}
	if dir != "" {
		for _, m := range grokCache(filepath.Join(dir, "models_cache.json")) {
			add(m.id, m.name, "Kigi")
		}
	}
	if path != "" {
		_ = edit.EachTOMLTable(path, func(t edit.TOMLTable) {
			id, ok := strings.CutPrefix(t.Name(), "model.")
			if !ok {
				return
			}
			if u, err := strconv.Unquote(id); err == nil {
				id = u
			}
			direct := "Kigi"
			if h := hostOf(t.Scalar("base_url")); h != "" {
				direct = h
			}
			add(id, t.Scalar("name"), direct)
		})
	}
	if cur != "" && !seen[cur] && !strings.HasPrefix(cur, magpieID+"/") {
		out = append([]Option{{Value: cur, Icon: modelIcon("", cur), Direct: "Kigi"}}, out...)
	}
	return group("Kigi", out)
}

// what kigi says after a change (notice.go)
var noticeKigi = newNotice("Kigi reads its settings at start-up — restart open kigi sessions to use this.")
