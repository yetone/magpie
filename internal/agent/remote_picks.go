package agent

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

// The models a Remote magpie's agents are on, followed here (KoiLx0 on X:
// several computers use one remote magpie, and each had every agent's
// models set again by hand, and again after each change on the remote).
//
//   - The remote tells, for each agent connected there, the catalog ref of
//     each model field (SharedModels, GET provider.RemoteAgentModelsPath).
//   - An agent connected here starts on the remote's picks, where this
//     magpie lists them (How "remote"), after the models it was on when
//     it was switched off and after a Codex that keeps its own.
//   - A field set from the remote, or found on the remote's pick, is
//     recorded (<agent>.<field>.remote in the stash). While it still holds
//     that value, a change on the remote is followed (FollowRemotePicks,
//     every FollowRemoteEvery). A value the user picks here is theirs:
//     the record goes, and the field is left alone until it is on the
//     remote's pick again.

func init() {
	gateway.SharedAgentModels = func() ([]byte, error) {
		return json.Marshal(provider.AgentModels{Agents: SharedModels()})
	}
}

// FollowRemoteEvery is how often a Remote magpie's picks are asked.
const FollowRemoteEvery = 5 * time.Minute

// remoteAsk is how long a connect waits for a Remote magpie's picks it
// has none of yet.
const remoteAsk = 4 * time.Second

// SharedModels is, by agent id, the catalog ref of each model field of
// every agent connected here: what another magpie with this one as its
// provider starts its agents on.
func SharedModels() map[string]map[string]string {
	out := map[string]map[string]string{}
	for _, a := range DetectedIn(All()) {
		if m := a.sharedModels(); len(m) > 0 {
			out[a.ID] = m
		}
	}
	return out
}

// sharedModels is SharedModels for one agent: its model fields' refs,
// none while it isn't connected.
func (a *Agent) sharedModels() map[string]string {
	if a.Native != nil || !a.Wired() {
		return nil
	}
	out := map[string]string{}
	vals := a.Values()
	for _, f := range a.Fields {
		v := vals[f.Key]
		if f.Own || f.Options == nil || v == "" || v == magpieID {
			continue
		}
		for _, o := range f.Options(vals) {
			if o.Value == v && o.Ref != "" {
				out[f.Key] = o.Ref
				break
			}
		}
	}
	return out
}

var remoteCache struct {
	sync.Mutex
	at    time.Time
	picks []provider.RemotePicks
}

// remotePicks are the Remote magpies' picks, asked again when older than
// fresh (0: always). Never asked while there is no Remote magpie.
func remotePicks(ctx context.Context, fresh time.Duration) []provider.RemotePicks {
	if !provider.HasPickedMagpie() {
		return nil
	}
	remoteCache.Lock()
	at, picks := remoteCache.at, remoteCache.picks
	remoteCache.Unlock()
	if fresh > 0 && !at.IsZero() && time.Since(at) < fresh {
		return picks
	}
	picks, errs := provider.RemoteAgentPicks(ctx)
	for id, err := range errs {
		log.Printf("agent: the models %s's agents are on: %v", id, err)
	}
	remoteCache.Lock()
	remoteCache.at, remoteCache.picks = time.Now(), picks
	remoteCache.Unlock()
	return picks
}

// remotePickFor is the option of opts a Remote magpie's agent of a's id
// has its field key on, the first remote that has one listed here.
func remotePickFor(id, key string, opts []Option, picks []provider.RemotePicks) (Option, bool) {
	for _, rp := range picks {
		ref := rp.Agents[id][key]
		if ref == "" {
			continue
		}
		pre := rp.Provider + "/"
		for _, o := range opts {
			if rest, ok := strings.CutPrefix(o.Ref, pre); ok && provider.SameRef(rest, ref) {
				return o, true
			}
		}
	}
	return Option{}, false
}

func remoteKey(id, key string) string { return id + "." + key + ".remote" }

// connectRemote starts a on the models a Remote magpie's agent of its id
// is on. ok is false when no remote has a pick this magpie lists, or the
// picks leave a not connected.
func (a *Agent) connectRemote() (c Connection, ok bool, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), remoteAsk)
	defer cancel()
	picks := remotePicks(ctx, FollowRemoteEvery)
	if len(picks) == 0 {
		return Connection{}, false, nil
	}
	set := map[string]string{}
	for _, f := range a.Fields {
		if f.Own || f.Options == nil {
			continue
		}
		o, found := remotePickFor(a.ID, f.Key, f.Options(a.Values()), picks)
		if !found {
			continue
		}
		if err := a.Apply(f.Key, o.Value); err != nil {
			return Connection{}, false, err
		}
		if c.Field == "" {
			c = Connection{How: "remote", Field: f.Key, Value: o.Value}
		}
		set[remoteKey(a.ID, f.Key)] = f.Get()
	}
	if len(set) == 0 {
		return Connection{}, false, nil
	}
	stash(set)
	return c, a.Wired(), nil
}

var lastFollow time.Time

// FollowRemotePicks puts each connected agent's field that is on a Remote
// magpie's pick onto that pick's change, at most every FollowRemoteEvery.
func FollowRemotePicks(ctx context.Context) {
	if time.Since(lastFollow) < FollowRemoteEvery || !provider.HasPickedMagpie() {
		return
	}
	lastFollow = time.Now()
	followRemote(ctx)
}

func followRemote(ctx context.Context) {
	cctx, cancel := context.WithTimeout(ctx, 2*provider.RemoteAgentModelsWait)
	picks := remotePicks(cctx, 0)
	cancel()
	if len(picks) == 0 {
		return
	}
	for _, a := range DetectedIn(All()) {
		if a.Native != nil || a.Disconnected() || !a.Wired() {
			continue
		}
		a.followRemote(picks)
	}
}

// followRemote is followRemote for one agent.
func (a *Agent) followRemote(picks []provider.RemotePicks) {
	for _, f := range a.Fields {
		if f.Own || f.Options == nil {
			continue
		}
		vals := a.Values()
		opts := f.Options(vals)
		o, found := remotePickFor(a.ID, f.Key, opts, picks)
		if !found {
			continue
		}
		k := remoteKey(a.ID, f.Key)
		rec, cur := stashShared()[k], vals[f.Key]
		switch {
		case onPick(cur, o, opts):
			// on the remote's pick: followed from now on
			if rec != cur {
				stash(map[string]string{k: cur})
			}
		case rec != "" && cur == rec:
			// still what was taken from the remote: the remote changed it
			if err := a.Apply(f.Key, o.Value); err != nil {
				log.Printf("agent: %s's %s from the remote magpie: %v", a.Name, f.Key, err)
				continue
			}
			stash(map[string]string{k: f.Get()})
		case rec != "":
			// picked here since: the user's own from now on
			forget(k)
		}
	}
}

// onPick reports whether cur, a field's value, is the model o is.
func onPick(cur string, o Option, opts []Option) bool {
	if cur == o.Value {
		return true
	}
	for _, x := range opts {
		if x.Value == cur && x.Ref != "" {
			return provider.SameRef(x.Ref, o.Ref)
		}
	}
	return false
}
