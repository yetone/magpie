package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// RemoteAgentModelsPath is where a remote magpie tells the models its
// agents are on (KoiLx0 on X: several computers on one remote magpie, each
// setting every agent's models again by hand, and again after each change
// there). It is answered as the Library is: to the computer itself and to
// a full-access key of a magpie shared on the local network.
const RemoteAgentModelsPath = "/v1/magpie/agent-models"

// RemoteAgentModelsWait is how long a remote magpie has to tell its picks.
const RemoteAgentModelsWait = 8 * time.Second

const errRemoteNoAgentModels = "remote magpie doesn't tell its agents' models; update magpie on that computer"

// AgentModels is what a magpie tells of its agents' models: by agent id,
// each model field's key and the catalog ref it is on there (group/smart,
// deepseek/deepseek-chat).
type AgentModels struct {
	Agents map[string]map[string]string `json:"agents"`
}

// RemotePicks is one Remote magpie's AgentModels, under its provider id
// here: a ref there is Provider+"/"+ref in this magpie's catalog.
type RemotePicks struct {
	Provider string
	Agents   map[string]map[string]string
}

// pickedMagpies are the Remote magpies whose agents' picks are followed:
// on, with an address and a key, and not this magpie itself.
func pickedMagpies() []Provider {
	var out []Provider
	for _, p := range storedFile().Providers {
		if strings.TrimSpace(p.Anthropic) == "" {
			continue
		}
		if p = normalize(p); !hasEndpoint(p) {
			continue
		}
		if p.IsRemoteMagpie() && !p.Off && p.Anthropic != "" && p.Key != "" && !p.SelfLeftOut() {
			out = append(out, p)
		}
	}
	return out
}

// HasPickedMagpie reports whether a Remote magpie's picks could be asked:
// a caller with nothing to follow asks no one.
func HasPickedMagpie() bool { return len(pickedMagpies()) > 0 }

// RemoteAgentPicks asks every Remote magpie, in the order they were added,
// the models its agents are on. One that can't tell (unreachable, older,
// a held key) is left out, its error in errs by provider id.
func RemoteAgentPicks(ctx context.Context) (out []RemotePicks, errs map[string]error) {
	for _, p := range pickedMagpies() {
		b, err := remoteBodyAs(ctx, p, http.MethodGet, RemoteAgentModelsPath, RemoteAgentModelsWait, 1<<20, errRemoteNoAgentModels)
		var m AgentModels
		if err == nil {
			err = json.Unmarshal(b, &m)
		}
		if err != nil {
			if errs == nil {
				errs = map[string]error{}
			}
			errs[p.ID] = err
			continue
		}
		out = append(out, RemotePicks{Provider: p.ID, Agents: m.Agents})
	}
	return out, errs
}
