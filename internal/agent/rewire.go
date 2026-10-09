package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The gateway's port is set in Settings (Magic_zero on Discord): every
// agent magpie connected has the gateway's URL written into its config, so
// moving the gateway moves them too. Before the port changes, OnGateway
// notes which agents reach the gateway where it is; once it has moved,
// Rewire sets each of them again, which writes the URL it has now.

// OnGateway are the agents connected to magpie whose configs reach the
// gateway at its address now: none of their wiring is off.
func OnGateway() []*Agent {
	var out []*Agent
	for _, a := range All() {
		if len(a.Fields) == 0 || !a.Wired() {
			continue
		}
		if d := a.Drift(); d != nil && d.Kind == "unwired" {
			continue // taken off magpie before: the user's to set again
		}
		out = append(out, a)
	}
	return out
}

// Rewire puts the gateway's address now into the configs of agents, taken
// by OnGateway before it moved: each one whose wiring is off now is set
// again, an agent joined to magpie beside its own models (Codex signed in
// with ChatGPT) joined again. It says the names of those it moved, and
// what one it couldn't move failed with. Cursor Private Inference's
// variables and dsh's routes, which magpie keeps on its own, follow too.
func Rewire(as []*Agent) (moved []string, err error) {
	var errs []error
	for _, a := range as {
		d := a.Drift()
		if d == nil || d.Kind != "unwired" {
			continue
		}
		// set again on the old URL, an agent reads as not routed through
		// magpie yet, and its set notes the old URL, magpie's token and
		// model as what it had before magpie: what it really had is kept,
		// for switching it off to put back
		was := stashLoad()
		e := reset(a)
		// the address magpie wrote follows it (wiredAt)
		for k, v := range stashLoad() {
			if strings.HasSuffix(k, wiredAt) {
				was[k] = v
			}
		}
		stashPut(was)
		if e != nil {
			errs = append(errs, fmt.Errorf("%s: %w", a.Name, e))
			continue
		}
		if a.Sync != nil {
			_ = a.Sync()
		}
		moved = append(moved, a.Name)
	}
	KeepCursorLocalEnv(context.Background())
	if trouble := dshWiredOnce(); trouble != "" {
		errs = append(errs, fmt.Errorf("%s", trouble))
	}
	if len(errs) > 0 {
		err = errs[0]
		for _, e := range errs[1:] {
			err = fmt.Errorf("%w; %w", err, e)
		}
	}
	return moved, err
}

// reset sets an agent's wiring again: one joined beside its own models
// joined again, any other's fields set again.
func reset(a *Agent) error {
	if a.Native != nil {
		return a.Native.Connect()
	}
	if a.Join != nil && a.Joined != nil && a.Joined() {
		ok, err := a.Join()
		if err != nil || ok {
			return err
		}
	}
	return a.Reapply()
}

// wiredAt ends a stash key holding the gateway address magpie last wrote
// into an agent's config, which a move of the gateway updates.
const wiredAt = ".wired_at"

// stashPut writes the stash as m has it.
func stashPut(m map[string]string) {
	b, _ := json.MarshalIndent(m, "", "  ")
	os.MkdirAll(filepath.Dir(stashPath()), 0o755)
	os.WriteFile(stashPath(), b, 0o600)
}
