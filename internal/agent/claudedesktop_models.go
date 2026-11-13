package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
)

// While a model is picked for one of Claude Desktop's tiers, magpie's
// profile carries inferenceModels (gateway.DesktopInferenceModels): the
// list Desktop's Code tab hands Claude Code each pick's behavesAs from,
// so a pick runs with the prompt of the Claude model of its tier
// (moer2831). Without a pick there is none, and Desktop finds magpie's
// models on its own as before. The list is magpie's only while it is what
// magpie wrote: desktopModelsMark keeps the hash of each list magpie
// wrote (the one before a write too, until the write is done), and a list
// with any other hash is the user's, which magpie never writes over or
// takes away. Desktop reads its profile at start-up only.
const desktopModelsMark = "claude-desktop.models"

// desktopDiscoveryMark says magpie wrote the profile's
// modelDiscoveryEnabled (gateway.DesktopDiscovers): magpie takes it away
// only then, and only while it is still true. One the user wrote, either
// way, stays as they wrote it.
const desktopDiscoveryMark = "claude-desktop.discovery"

// desktopModelsHash is a list's hash, the same however it is spaced.
func desktopModelsHash(raw []byte) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// desktopSyncModels writes p's inferenceModels as magpie lists them now,
// takes away the one magpie wrote when nothing is picked, and leaves the
// user's own alone.
func desktopSyncModels(p desktopPaths) error {
	if !desktopWired(p) {
		return nil
	}
	want, ok := gateway.DesktopInferenceModels()
	if !ok {
		return nil // the picks unknown: what is there stays
	}
	key := desktopModelsMark + p.key
	marks := strings.Fields(stashShared()[key])
	b, err := edit.Read(p.prof)
	if err != nil || b == nil {
		return err // no profile: desktopOn writes one, then this
	}
	raw, has := edit.GetJSONRaw(p.prof, "inferenceModels")
	if has && !slices.Contains(marks, desktopModelsHash([]byte(raw))) {
		return desktopSyncDiscovery(p, false) // the list is the user's
	}
	discovers, _ := gateway.DesktopDiscovers()
	if err := desktopSyncDiscovery(p, want != nil && discovers); err != nil {
		return err
	}
	if want == nil {
		if has {
			if err := edit.DelJSON(p.prof, "inferenceModels"); err != nil {
				return err
			}
		}
		forget(key)
		return nil
	}
	next, err := json.Marshal(want)
	if err != nil {
		return err
	}
	h := desktopModelsHash(next)
	if has && desktopModelsHash([]byte(raw)) == h {
		stash(map[string]string{key: h})
		return nil
	}
	// marked before it is written: a write cut short leaves a list magpie
	// still knows for its own
	stash(map[string]string{key: strings.Join(append(marks, h), " ")})
	if err := edit.SetJSON(p.prof, edit.KV{Path: "inferenceModels", Value: json.RawMessage(next)}); err != nil {
		return err
	}
	stash(map[string]string{key: h})
	return nil
}

// desktopSyncDiscovery has Desktop read /v1/models beside magpie's
// inferenceModels while on, where Desktop's Code tab finds a later tier of
// a model picked for two (gateway.DesktopDiscovers), and takes back the
// modelDiscoveryEnabled magpie wrote once off. Unset, Desktop skips
// /v1/models while the list names no bare tier word.
func desktopSyncDiscovery(p desktopPaths, on bool) error {
	key := desktopDiscoveryMark + p.key
	ours := stashShared()[key] != ""
	raw, has := edit.GetJSONRaw(p.prof, "modelDiscoveryEnabled")
	if ours && (!has || strings.TrimSpace(raw) != "true") {
		forget(key) // taken away or changed: the user's now
		ours = false
	}
	switch {
	case on && !has:
		stash(map[string]string{key: "1"})
		return edit.SetJSON(p.prof, edit.KV{Path: "modelDiscoveryEnabled", Value: true})
	case !on && ours:
		if err := edit.DelJSON(p.prof, "modelDiscoveryEnabled"); err != nil {
			return err
		}
		forget(key)
	}
	return nil
}
