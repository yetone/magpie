package gateway

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/steady"
	"github.com/yetone/magpie/internal/usage"
)

// A Codex subagent resumed with resume_agent went on its lead's model, not
// the one it was spawned on (HoGee_xxl on X). Codex's multi-agent V1, which
// magpie's own models ask for, builds the resumed subagent's config from
// the lead's current turn (codex-rs core/src/agent/child_config.rs,
// build_agent_resume_config → build_agent_shared_config sets config.model
// to turn.model_info().slug), and resume_single_agent_from_rollout
// (core/src/agent/control/spawn.rs) starts the thread on that config
// without reading back the model the thread has stored; neither the
// spawn_agent model nor [agents] default_subagent_model is asked again.
// Multi-agent V2's own reload does put a thread back on its stored model
// (spawn.rs, ensure_v2_agent_loaded: config.model = stored_model), so
// keeping a subagent on the model it was spawned with is Codex's own rule,
// which the V1 path misses.
//
// So the gateway notes the model each Codex subagent thread first asked
// for, and puts a later request of that thread asking for another back on
// it. Within a V1 thread the model never changes otherwise: a subagent has
// no /model. Kept on disk (codex-subagents.json, next to the providers) so
// a subagent resumed after magpie restarted, an update's included, is
// still known. A model set for every subagent (codexSubagentPick) still
// moves the request after this.

// codexSpawnsKept is how many subagent threads are remembered, the latest.
const codexSpawnsKept = 512

// codexSpawnKeep is how long a subagent thread's model is remembered after
// its last request.
const codexSpawnKeep = 30 * 24 * time.Hour

type codexSpawn struct {
	Model string    `json:"model"`
	At    time.Time `json:"at"`
}

// codexSpawns is the model each Codex subagent thread was spawned on, by
// its thread id.
type codexSpawns struct {
	mu   sync.Mutex
	m    map[string]codexSpawn
	from string // the file m was read from
}

func codexSpawnsPath() string {
	return filepath.Join(filepath.Dir(provider.Path()), "codex-subagents.json")
}

// codexSubagentThread is the thread a Codex subagent's request is of:
// thread_id in its turn metadata. Codex's session_id is shared by a root
// thread and every subagent under it, so it names a subagent only in a
// Codex from before that, where it differs from the parent thread.
func codexSubagentThread(h http.Header, meta sessionMetadata) string {
	if t := strings.TrimSpace(meta.Thread); t != "" && len(t) <= 128 {
		return t
	}
	parent := strings.TrimSpace(meta.Parent)
	if parent == "" {
		parent = strings.TrimSpace(h.Get("x-codex-parent-thread-id"))
	}
	if s := sessionOf(h); s != "" && parent != "" && s != parent {
		return s
	}
	return ""
}

// threadOf is the thread a request names apart from its session, for
// ${threadId} (provider/conversation.go): Codex's thread_id, in its turn
// metadata or its thread-id header. "" when it names none — no other
// agent does — and ${threadId} is then the session's.
func threadOf(h http.Header, body []byte) string {
	for _, t := range []string{requestSessionMetadata(h, body).Thread, h.Get("thread-id")} {
		if t = strings.TrimSpace(t); t != "" && len(t) <= 128 {
			return t
		}
	}
	return ""
}

// codexResumeTo is the model a Codex subagent's request goes on instead
// of asked: the one its thread was spawned on, when it asks for another.
// "" leaves the request as it came: not a Codex subagent's, a thread not
// seen before (noted now), or its own model asked for.
func (s *Server) codexResumeTo(r *http.Request, agent, kind, asked string, meta sessionMetadata) string {
	if agent != "codex" && !fromCodex(r.Header) || asked == "" || usage.PurposeOf(kind) != "kind:collab_spawn" {
		return ""
	}
	thread := codexSubagentThread(r.Header, meta)
	if thread == "" {
		return ""
	}
	to := s.codexSpawns.keep(thread, asked)
	// a spawn model of magpie's it no longer serves (its provider removed
	// or switched off) is not gone back to: the subagent was sent there,
	// to a 404, after the user had removed the provider and moved Codex
	// to another model (jockyer on X). It goes on as asked, and that is
	// its model from now on.
	if strings.Contains(to, "/") && unserved(to) {
		s.codexSpawns.moved(thread, to, asked)
		return ""
	}
	return to
}

// moved notes asked as thread's model in place of from, the model it was
// spawned on, which magpie no longer serves.
func (c *codexSpawns) moved(thread, from, asked string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if v, ok := c.m[thread]; !ok || v.Model != from {
		return
	}
	now := time.Now()
	c.m[thread] = codexSpawn{Model: asked, At: now}
	c.save(c.from, now)
}

// keep notes asked as thread's model when thread is new, and says the
// model it was spawned on when that isn't asked.
func (c *codexSpawns) keep(thread, asked string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	path := codexSpawnsPath()
	if c.m == nil || c.from != path {
		c.m, c.from = readCodexSpawns(path), path
	}
	now := time.Now()
	if v, ok := c.m[thread]; ok && now.Sub(v.At) < codexSpawnKeep {
		v.At = now
		c.m[thread] = v
		if v.Model != asked {
			return v.Model
		}
		return ""
	}
	c.m[thread] = codexSpawn{Model: asked, At: now}
	// written as a thread is first seen, once a spawn, not once a request
	c.save(path, now)
	return ""
}

func readCodexSpawns(path string) map[string]codexSpawn {
	m := map[string]codexSpawn{}
	b, err := os.ReadFile(path)
	if err != nil {
		return m
	}
	// a file that can't be read is no memory: each thread is noted afresh
	if decodeJSON(b, &m) != nil {
		return map[string]codexSpawn{}
	}
	return m
}

// save writes the latest threads, only for this user to read. Called
// with c.mu held.
func (c *codexSpawns) save(path string, now time.Time) {
	ids := make([]string, 0, len(c.m))
	for id, v := range c.m {
		if now.Sub(v.At) >= codexSpawnKeep || v.Model == "" {
			delete(c.m, id)
			continue
		}
		ids = append(ids, id)
	}
	if len(ids) > codexSpawnsKept {
		sort.Slice(ids, func(i, j int) bool { return c.m[ids[i]].At.After(c.m[ids[j]].At) })
		for _, id := range ids[codexSpawnsKept:] {
			delete(c.m, id)
		}
	}
	b, err := json.Marshal(c.m)
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return
	}
	path, err = edit.Target(path) // a symlink stays, its target written
	if err != nil {
		return
	}
	tmp := path + ".magpie-tmp"
	if os.WriteFile(tmp, b, 0o600) != nil {
		return
	}
	if steady.Rename(tmp, path) != nil {
		os.Remove(tmp)
	}
}
