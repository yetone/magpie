package gui

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/artifact"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/library"
)

// Work › Artifacts (docs/subsystems/artifacts.md): the pages agents
// published through Magpie Artifacts (`magpie mcp artifacts`), newest
// first, each with its versions. The page opens one in the browser, at
// the gateway's address for it (/a/<id>/, a version at /a/<id>@<n>/), and
// deletes one with every version it has.
//
// The user shares one on usemagpie.ai (artifact.ShareLatest), where anyone
// with its link opens it, and stops sharing it.
//
//   - GET /api/artifacts lists them;
//   - POST /api/artifacts/delete {"id"} deletes one;
//   - POST /api/artifacts/share {"id"} shares one, answering {"url"};
//   - POST /api/artifacts/unshare {"id"} takes its shared link down.

type artifactVersionJSON struct {
	N     int       `json:"n"`
	At    time.Time `json:"at"`
	Label string    `json:"label,omitempty"`
	Agent string    `json:"agent,omitempty"`
	// the agent's name, as the Agents page says it ("codex" is Codex); an
	// agent magpie doesn't know goes by what it was called
	AgentName string `json:"agentName,omitempty"`
	Files     int    `json:"files"`
	Size      int64  `json:"size"`
	URL       string `json:"url"`
}

type artifactJSON struct {
	ID          string                `json:"id"`
	Title       string                `json:"title"`
	Description string                `json:"description,omitempty"`
	Created     time.Time             `json:"created"`
	Updated     time.Time             `json:"updated"`
	URL         string                `json:"url"`
	Versions    []artifactVersionJSON `json:"versions"` // newest first
	// Shared is its link on usemagpie.ai, SharedVersion the version it
	// shows; "" when it isn't shared
	Shared        string `json:"shared,omitempty"`
	SharedVersion int    `json:"sharedVersion,omitempty"`
}

func artifactsJSON() ([]artifactJSON, error) {
	list, err := artifact.List()
	if err != nil {
		return nil, err
	}
	out := make([]artifactJSON, 0, len(list))
	names := map[string]string{}
	if len(list) > 0 {
		for _, g := range agent.All() {
			names[g.ID] = g.Name
		}
	}
	nameOf := func(id string) string {
		if n := names[id]; n != "" {
			return n
		}
		return id
	}
	for _, a := range list {
		j := artifactJSON{ID: a.ID, Title: a.Title, Description: a.Description, Created: a.Created, Updated: a.Updated,
			URL: gateway.ArtifactURL(a.ID, 0), Versions: make([]artifactVersionJSON, 0, len(a.Versions))}
		for i := len(a.Versions) - 1; i >= 0; i-- {
			v := a.Versions[i]
			vj := artifactVersionJSON{N: v.N, At: v.At, Label: v.Label, Agent: v.Agent, Files: len(v.Files), Size: v.Size, URL: gateway.ArtifactURL(a.ID, v.N)}
			if v.Agent != "" && v.Agent != artifact.PageAgent {
				vj.AgentName = nameOf(v.Agent)
			}
			j.Versions = append(j.Versions, vj)
		}
		if sh, err := artifact.Shared(a.ID); err == nil && sh != nil {
			j.Shared, j.SharedVersion = sh.URL, sh.Version
		}
		out = append(out, j)
	}
	return out, nil
}

// artifactsAgentNames is the names, as the Agents page says them, of the
// agents Magpie Artifacts is given to; nil when the library can't be read,
// so the page says nothing rather than "no agent".
func artifactsAgentNames() []string {
	ids, err := library.ArtifactsAgents()
	if err != nil {
		return nil
	}
	names := map[string]string{}
	for _, g := range agent.All() {
		names[g.ID] = g.Name
	}
	out := []string{}
	for _, id := range ids {
		n := names[id]
		if base, _, ok := strings.Cut(id, "@"); n == "" && ok {
			n = names[base] // one of an agent's own copies, as codex@work
		}
		if n == "" {
			n = id
		}
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}

func artifactRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/artifacts", func(rw http.ResponseWriter, r *http.Request) {
		list, err := artifactsJSON()
		if err != nil {
			fail(rw, err)
			return
		}
		out := map[string]any{"artifacts": list}
		// with none yet, the page says which agents can make one
		if len(list) == 0 {
			out["agents"] = artifactsAgentNames()
		}
		writeJSON(rw, out)
	})
	mux.HandleFunc("POST /api/artifacts/delete", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ ID string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		// one deleted already (by an agent, or another window) is gone
		// as asked
		if err := artifact.Delete(in.ID); err != nil && !errors.Is(err, artifact.ErrNotFound) {
			fail(rw, err)
			return
		}
		writeJSON(rw, map[string]bool{"deleted": true})
	})
	mux.HandleFunc("POST /api/artifacts/share", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ ID string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		sh, err := artifact.ShareLatest(r.Context(), in.ID)
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, map[string]any{"url": sh.URL, "version": sh.Version})
	})
	// the shared page, opened as its owner: a one-time code in its link
	mux.HandleFunc("POST /api/artifacts/owner-link", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ ID string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		u, err := artifact.OwnerLink(r.Context(), in.ID)
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, map[string]string{"url": u})
	})
	mux.HandleFunc("POST /api/artifacts/unshare", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ ID string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if err := artifact.Unshare(r.Context(), in.ID); err != nil && !errors.Is(err, artifact.ErrNotFound) {
			fail(rw, err)
			return
		}
		writeJSON(rw, map[string]bool{"unshared": true})
	})
}
