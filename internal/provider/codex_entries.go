package provider

import (
	"context"
	"net/url"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
)

// remoteCodexList is where another magpie lists its models in Codex's
// shape, after its /v1: the gateway's CodexCatalogPath (#1281).
const remoteCodexList = "/codex/models"

// codexEntries are ms, the models a provider the user added by its address
// lists at its Responses base, with the entry each has in Codex's own shape
// where the vendor gives one (catalog.Model.Codex). A relay built for Codex
// (sub2api) lists its models plainly at /v1/models, ids and owners alone,
// and in Codex's shape — window, levels, instructions, tools — when asked
// with the client_version Codex asks with; magpie read the plain list, so
// Codex was handed windows and levels models.dev guessed for the ids
// (gpt-6.1-terra at 922000) where the relay had said its own (ctopsc on
// Discord). So the list is asked once more as Codex asks it, at the URL it
// answered at, when that list is fetched: a request more where the
// provider is added, its key changed or its list refreshed, never one
// each time Codex asks magpie for its list. The answer only adds to
// the plain list: a model it has that the plain list hasn't is not added,
// as the plain list is the one magpie has always served (a relay's image
// models are in it alone). A list already in Codex's shape is not asked
// again. One that can't be read says nothing new: each model keeps the
// entry the last list gave it, as a base that can't be asked keeps its
// models (#904); one read that is a plain list says the vendor gives none.
//
// Another magpie is asked for the list it hands a Codex on another
// computer (remoteCodexList), where a ChatGPT account it holds has its
// models as the backend lists them (codex/<slug>, base_instructions,
// model_messages, tools and tiers), and every other model as that magpie
// describes it to its own Codex. Without it this magpie made an entry of
// its own for each of them, as for any third party's model, and a Codex
// here was handed a remote ChatGPT account's GPT models with none of what
// Codex's own catalog says of them (Darkf1ames on X).
func (p Provider) codexEntries(ctx context.Context, list string, ms []catalog.Model) []catalog.Model {
	if p.Preset != "" && !p.IsRemoteMagpie() || p.Account != nil || slices.ContainsFunc(ms, func(m catalog.Model) bool { return len(m.Codex) > 0 }) {
		return ms
	}
	u, err := url.Parse(list)
	if err != nil {
		return ms
	}
	if p.IsRemoteMagpie() {
		path, ok := strings.CutSuffix(u.Path, "/models")
		if !ok {
			return ms
		}
		u.Path = path + remoteCodexList
	}
	q := u.Query()
	q.Set("client_version", codexVersion())
	u.RawQuery = q.Encode()
	got, err := catalog.FetchURL(ctx, u.String(), p.Key, false, p.listHeadersAt(list))
	if err != nil {
		got, _, _ = catalog.Live(p.ID)
	}
	byID := map[string]catalog.Model{}
	for _, m := range got {
		if len(m.Codex) > 0 {
			byID[m.ID] = m
		}
	}
	if len(byID) == 0 {
		return ms
	}
	out := slices.Clone(ms)
	for i := range out {
		if c, ok := byID[out[i].ID]; ok {
			withCodex(&out[i], c)
		}
	}
	return out
}

// withCodex gives m the Codex entry c has, and what that entry says of the
// model where m's own list said nothing.
func withCodex(m *catalog.Model, c catalog.Model) {
	m.Codex = c.Codex
	if m.Context == 0 {
		m.Context = c.Context
	}
	if m.MaxContext == 0 {
		m.MaxContext = c.MaxContext
	}
	if len(m.Efforts) == 0 {
		m.Efforts = c.Efforts
	}
	if m.ImageInput == nil && c.ImageInput != nil {
		m.ImageInput, m.Images, m.InputModalities = c.ImageInput, c.Images, c.InputModalities
	}
}
