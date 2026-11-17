package agent

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
)

// The protocol omp asks magpie's models on, picked in omp's row as Pi's is
// (pi_protocol.go; imty42 on X). Unpicked, magpie's provider in models.yml
// is on openai-completions and each model on the API its provider serves
// natively (ompProviderAt): a group whose members don't all take Responses
// stays on Chat (provider.GroupAPI), and an api typed into models.yml was
// written over on the next sync, so an omp that wanted every model on
// Responses had no way to get there. omp takes each of piProtocols for its
// provider and its models (KnownApi in pi-catalog types.ts, the api enum of
// models-config-schema-bundle.ts, omp 18.9.1), and the gateway serves every
// model on each. A pick puts every chat model on it; the provider's api
// typed into models.yml by hand stays (ompKept) and shows, until a pick is
// made after it.

// ompProtocolKey is where the stash keeps the protocol picked for the omp
// whose agent folder is dir at a place, "" for each model's own: each
// profile is an omp of its own.
func ompProtocolKey(at place, dir string) string { return at.key("omp.protocol") + ":" + dir }

// ompProtocol is the protocol picked for the omp at key.
func ompProtocol(key string) string {
	if v := stashShared()[key]; slices.Contains(piProtocols, v) {
		return v
	}
	return ""
}

// ompOn is magpie's entry e with every chat model asked on the api on, as
// it is for "". On anthropic-messages a model names the gateway's root, as
// omp adds the /v1 the provider's baseUrl has, and thinks as ompProviderAt
// has a Messages model think; on Chat and Responses it is asked an effort.
// An image model stays on openai-images.
func ompOn(e ompProviderEntry, on string) ompProviderEntry {
	if on == "" {
		return e
	}
	gw := strings.TrimSuffix(e.BaseURL, "/v1")
	e.API = on
	e.Models = slices.Clone(e.Models)
	for i := range e.Models {
		m := &e.Models[i]
		if m.Kind != "" {
			continue
		}
		m.API, m.BaseURL = "", ""
		mode := "effort"
		if on == "anthropic-messages" {
			m.API, m.BaseURL = on, gw
			mode = "budget"
			if gateway.AdaptiveThinking(m.ID) {
				mode = "anthropic-adaptive"
			}
		}
		if m.Thinking != nil {
			th := *m.Thinking
			th.Mode = mode
			m.Thinking = &th
		}
	}
	return e
}

// ompAPIKept is e with the provider's api the user typed into the
// models.yml at path in place of magpie's, as Pi's (pi_api_kept.go): one
// that is none of the APIs magpie has written there (piAPIsWanted, only
// ever openai-completions before there was a record) is theirs. What e
// wants is recorded as magpie's. A model's api is magpie's, the list being
// magpie's whole.
func ompAPIKept(path string, e ompProviderEntry) ompProviderEntry {
	want := e.API
	if file, _ := edit.GetYAML(path, "providers."+magpieID+".api"); file != "" && file != want && !slices.Contains(piAPIsWanted(path), file) {
		e.API = file
	}
	stashMu.Lock()
	defer stashMu.Unlock()
	m := stashLoad()
	rec := map[string][]string{}
	if s := m[apisWantedKey(path)]; s != "" && json.Unmarshal([]byte(s), &rec) != nil {
		rec = map[string][]string{}
	}
	was, known := rec[piProviderAPI]
	if !known {
		was = []string{"openai-completions"}
	}
	if slices.Contains(was, want) && known {
		return e
	}
	if !slices.Contains(was, want) {
		was = append(slices.Clone(was), want)
		if len(was) > outputsRemembered {
			was = was[len(was)-outputsRemembered:]
		}
	}
	rec[piProviderAPI] = was
	b, _ := json.Marshal(rec)
	m[apisWantedKey(path)] = string(b)
	stashWrite(m)
	return e
}
