package provider

// A Claude account's models are the ones models.dev lists under anthropic
// and the ones Anthropic told Claude Code the account has beyond them.
// models.dev's list is what every account may call; a gated model, as
// Claude Mythos 5.1 is since models.dev took it out of that list
// (models.dev #11327), or one kept for some accounts (Opus 3), is only in
// what Claude Code was told. Claude Code asks Anthropic itself, at its
// start (/api/claude_cli/bootstrap), and keeps the answer in its global
// state file (.claude.json): additionalModelOptionsCache, the models its
// /model picker offers beyond its own; modelAccessCache, the models the
// account is or isn't entitled to; additionalModelCostsCache, their
// prices. magpie reads them back, as it reads the account's identity from
// the same file: nothing is asked of Anthropic in Claude Code's name
// (iebb on Discord: "最好以 claude code 返回为准").

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/settings"
)

// claudeCodeModels is what Claude Code keeps of an account's models.
type claudeCodeModels struct {
	// at is when Anthropic answered (additionalModelOptionsAnsweredAt),
	// in ms: of two files of one account, the later one's word goes
	at float64
	// extra are the models it offers beyond its own, by API id, with the
	// name it gives them
	extra []claudeExtra
	// entitled is, by API id, whether the account may call the model
	entitled map[string]bool
	// prices are Anthropic's, by API id, USD per million tokens
	prices map[string]catalog.Price
}

type claudeExtra struct{ id, name string }

func parseClaudeCodeModels(b []byte) (claudeCodeModels, error) {
	var raw struct {
		Options []struct {
			Value    *string `json:"value"`
			Label    string  `json:"label"`
			Disabled bool    `json:"disabled"`
		} `json:"additionalModelOptionsCache"`
		At     float64 `json:"additionalModelOptionsAnsweredAt"`
		Access []struct {
			APIName  string `json:"apiName"`
			Entitled *bool  `json:"entitled"`
		} `json:"modelAccessCache"`
		Costs map[string]struct {
			Input     float64  `json:"inputTokens"`
			Output    float64  `json:"outputTokens"`
			Write     float64  `json:"promptCacheWriteTokens"`
			Write1h   *float64 `json:"promptCacheWrite1hTokens"`
			CacheRead float64  `json:"promptCacheReadTokens"`
		} `json:"additionalModelCostsCache"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return claudeCodeModels{}, err
	}
	out := claudeCodeModels{at: raw.At}
	seen := map[string]bool{}
	for _, o := range raw.Options {
		if o.Value == nil || o.Disabled {
			continue
		}
		id := claudeAPIID(*o.Value)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out.extra = append(out.extra, claudeExtra{id, strings.TrimSpace(o.Label)})
	}
	for _, a := range raw.Access {
		if id := claudeAPIID(a.APIName); id != "" && a.Entitled != nil {
			if out.entitled == nil {
				out.entitled = map[string]bool{}
			}
			out.entitled[id] = *a.Entitled
			if *a.Entitled && !seen[id] {
				seen[id] = true
				out.extra = append(out.extra, claudeExtra{id: id})
			}
		}
	}
	for id, c := range raw.Costs {
		id = claudeAPIID(id)
		if id == "" || c.Input <= 0 || c.Output <= 0 {
			continue
		}
		p := catalog.Price{Input: c.Input, Output: c.Output, CacheRead: c.CacheRead, CacheWrite: c.Write}
		if c.Write1h != nil && *c.Write1h > 0 {
			p.CacheWrite1h = *c.Write1h
		}
		catalog.OneHourFor(id, &p)
		if out.prices == nil {
			out.prices = map[string]catalog.Price{}
		}
		out.prices[id] = p
	}
	return out, nil
}

// claudeAPIID is a model id as Claude Code names it, without the window
// it takes in the name (claude-mythos-5-1[1m]): the id the API knows. ""
// for one that isn't a Claude model's id (an alias, "default").
func claudeAPIID(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if i := strings.IndexByte(v, '['); i > 0 && strings.HasSuffix(v, "]") {
		v = v[:i]
	}
	if !strings.HasPrefix(v, "claude-") || strings.ContainsAny(v, " /") {
		return ""
	}
	return v
}

func readClaudeCodeModels(path string) (claudeCodeModels, bool) {
	m, err := filememo.Read("claude models", path, parseClaudeCodeModels)
	return m, err == nil
}

// claudeCodeFiles are the state files of every Claude Code magpie knows of
// an account in: the one Claude Code itself is signed in to, then each
// saved account's own config directory (claude_dirs.go). Listed once
// while a request holds the catalog: each Claude model's price asked for
// them, so a build of an agent's models read the folder once per model.
func claudeCodeFiles() []string {
	return slices.Clip(heldOf("claude code files", listClaudeCodeFiles))
}

func listClaudeCodeFiles() []string {
	if settings.AccountDiscoveryOff() {
		return nil
	}
	out := []string{claudeProfilePath()}
	saved, _ := filepath.Glob(filepath.Join(claudeDirsRoot(), "*", ".claude.json"))
	sort.Strings(saved)
	return append(out, saved...)
}

// claudeModelsOf is what Claude Code keeps of the account user's models:
// from its saved directory or Claude Code's own sign-in, whichever
// Anthropic answered last, as that file's account is the user's.
func claudeModelsOf(user string) (claudeCodeModels, bool) {
	var best claudeCodeModels
	found := false
	take := func(m claudeCodeModels) {
		if !found || m.at > best.at {
			best, found = m, true
		}
	}
	if user == "" {
		return best, false
	}
	if m, ok := readClaudeCodeModels(filepath.Join(claudeAccountDir(user), ".claude.json")); ok {
		take(m)
	}
	if acct, ok := claudeProfileAccount(); ok {
		email, _ := acct["emailAddress"].(string)
		email = strings.ToLower(strings.TrimSpace(email))
		u := strings.ToLower(strings.TrimSpace(user))
		if email != "" && (u == email || strings.HasPrefix(u, email+" · ")) {
			if m, ok := readClaudeCodeModels(claudeProfilePath()); ok {
				take(m)
			}
		}
	}
	return best, found
}

// claudeModels is the Claude provider's list: models.dev's anthropic list,
// and each model an account's Claude Code offers beyond it, described as
// models.dev knows it anywhere (its lab file for a gated one).
func claudeModels() []catalog.Model {
	out := catalog.Provider("anthropic")
	have := map[string]bool{}
	for _, m := range out {
		have[m.ID] = true
	}
	for _, f := range claudeCodeFiles() {
		cm, ok := readClaudeCodeModels(f)
		if !ok {
			continue
		}
		for _, x := range cm.extra {
			if have[x.id] {
				continue
			}
			have[x.id] = true
			m := catalog.Describe(x.id, x.name)
			m.Provider = "anthropic"
			if p, ok := cm.prices[x.id]; ok {
				m.Price = &p
			}
			out = append(out, m)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Released != out[j].Released {
			return out[i].Released > out[j].Released
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// claudeLists says whether the Claude account user has model, as its
// Claude Code was told: a model it isn't entitled to is left out, and one
// it offers or models.dev lists for every account is in. ok is false when
// no Claude Code of the account has said.
func claudeLists(user, model string) (has, ok bool) {
	cm, found := claudeModelsOf(user)
	if !found {
		return false, false
	}
	id := claudeAPIID(model)
	if id == "" {
		return true, true
	}
	if e, said := cm.entitled[id]; said {
		return e, true
	}
	return true, true
}

// ClaudeCodeModels is the list Claude Code, as it is signed in on this
// computer, offers: models.dev's anthropic list without what the account
// isn't entitled to, and the models Anthropic told it the account has
// beyond them (Claude Mythos 5.1).
func ClaudeCodeModels() []catalog.Model {
	if settings.AccountDiscoveryOff() {
		return catalog.Provider("anthropic")
	}
	cm, ok := readClaudeCodeModels(claudeProfilePath())
	return claudeModelsWith(cm, ok)
}

// claudeModelsWith is the provider's list as one account's Claude Code
// was told it: none it isn't entitled to, and of the models models.dev
// doesn't list for every account only its own.
func claudeModelsWith(cm claudeCodeModels, ok bool) []catalog.Model {
	if !ok {
		return catalog.Provider("anthropic")
	}
	base := map[string]bool{}
	for _, m := range catalog.Provider("anthropic") {
		base[m.ID] = true
	}
	own := map[string]bool{}
	for _, x := range cm.extra {
		own[x.id] = true
	}
	return slices.DeleteFunc(claudeModels(), func(m catalog.Model) bool {
		if e, said := cm.entitled[m.ID]; said && !e {
			return true
		}
		return !base[m.ID] && !own[m.ID]
	})
}

// claudeCodePrice is Anthropic's price for a model Claude Code was told
// of, from any account's: a gated model models.dev prices nowhere.
func claudeCodePrice(model string) (catalog.Price, bool) {
	id := claudeAPIID(model)
	if id == "" {
		return catalog.Price{}, false
	}
	for _, f := range claudeCodeFiles() {
		if cm, ok := readClaudeCodeModels(f); ok {
			if p, ok := cm.prices[id]; ok {
				return p, true
			}
		}
	}
	return catalog.Price{}, false
}
