package gateway

import (
	"slices"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// mediaMembers is the routing group ref with its members ready now for
// an API that is no chat: embeddings, rerank, speech, transcriptions and
// videos. A provider's picks are of its chat models, and these models are
// in no chat list (catalog's textModel and pluginTalks leave embeddings,
// rerankers and speech models out when a list is fetched; videos are
// apart), so a member of a provider with picks was dropped as unpicked
// and a group of them was a 404 (XYenon on Discord). Such a member is
// kept, as a drawing's image models are (FindDrawingGroup, jzw690 on X).
// A chat model the user unticked (#3911) is on its provider's list and
// still stays out.
func mediaMembers(ref string) (provider.Group, []provider.Member, bool) {
	return provider.FindGroupAlso(ref, offChatList)
}

// offChatList is whether p's list of models to chat with lacks model:
// one made for another API, which no pick can name.
func offChatList(p provider.Provider, model string) bool {
	return !slices.ContainsFunc(provider.KeptListing(p).Available, func(m catalog.Model) bool { return m.ID == model })
}

// mediaGroupFirst is the first member of the group ref for an API that is
// no chat (mediaMembers): such a group is no model agents are offered, so
// Resolve, which reads their catalog, doesn't take it.
func mediaGroupFirst(ref string) (provider.Provider, string, bool) {
	if !provider.IsGroupRef(ref) {
		return provider.Provider{}, "", false
	}
	if _, ms, ok := mediaMembers(ref); ok && len(ms) > 0 {
		return ms[0].Provider, ms[0].Model, true
	}
	return provider.Provider{}, "", false
}
