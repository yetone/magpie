package agent

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"slices"
	"strings"
	"sync"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
	"gopkg.in/yaml.v3"
)

// Most agents can't ask the gateway which models it has: magpie writes the
// catalog into a file of the agent's own (Pi's models.json, OpenCode's
// provider block, Codex's model catalog …) when a model is picked through
// magpie. That list was the catalog as it was then, so a provider added
// later never reached the agent, and one removed stayed listed. SyncCatalog
// rewrites each such list whenever the catalog may have changed.

var syncing struct {
	sync.Mutex
	running, again bool
}

// SyncCatalog brings every model list magpie wrote into an agent's files up
// to the catalog as it is now. It is what catalog.Changed is set to, so a
// provider saved or removed, or a vendor's list fetched anew, reaches the
// agents; one already running asks for another round rather than waiting.
// Errors are left for the picker: a file magpie can't write now is one the
// next change, or a pick, writes.
func SyncCatalog() {
	syncing.Lock()
	if syncing.running {
		syncing.again = true
		syncing.Unlock()
		return
	}
	syncing.running = true
	syncing.Unlock()
	for {
		// the catalog built once for every agent's lists, not for each
		// look-up of each (thousands at magpie's start, with 30 providers)
		release := provider.Hold()
		for _, a := range All() {
			if a.Sync != nil {
				_ = a.Sync()
			}
		}
		release()
		syncing.Lock()
		if !syncing.again {
			syncing.running = false
			syncing.Unlock()
			return
		}
		syncing.again = false
		syncing.Unlock()
	}
}

// syncJSON rewrites the value at key of a JSON file, if the file has one
// there and it differs: an agent that watches its files isn't told of a
// change that isn't one.
func syncJSON(path, key string, value func() any) error {
	cur, ok := edit.GetJSON(path, key)
	if !ok {
		return nil
	}
	v := value()
	if sameJSON(cur, v) {
		return nil
	}
	return edit.SetJSON(path, edit.KV{Path: key, Value: v})
}

// theirsKept is value, magpie's provider block for the agent, with what
// else the block at key of the JSON file holds kept: a key magpie doesn't
// write there, at any depth, is the user's or another tool's (Pi's
// providers.magpie.compat.sendSessionAffinityHeaders, which
// pi-cache-optimizer adds, #1103) and stays. The keys named in whole (the
// model list) are magpie's alone, so a model gone from the catalog leaves
// the agent's list.
func theirsKept(path, key string, value func() any, whole ...string) func() any {
	return func() any {
		v := value()
		cur, ok := edit.GetJSON(path, key)
		if !ok {
			return v
		}
		var have, want map[string]json.RawMessage
		b, err := json.Marshal(v)
		if err != nil || json.Unmarshal([]byte(cur), &have) != nil || have == nil || json.Unmarshal(b, &want) != nil || want == nil {
			return v
		}
		return mergeTheirs(have, want, whole)
	}
}

// mergeTheirs is want with each key of have that want lacks, objects in
// both merged alike; a key named in whole is want's as it is.
func mergeTheirs(have, want map[string]json.RawMessage, whole []string) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(have)+len(want))
	for k, w := range want {
		out[k] = w
	}
	for k, h := range have {
		w, ok := want[k]
		if !ok {
			out[k] = h
			continue
		}
		if slices.Contains(whole, k) {
			continue
		}
		var hm, wm map[string]json.RawMessage
		if json.Unmarshal(h, &hm) != nil || hm == nil || json.Unmarshal(w, &wm) != nil || wm == nil {
			continue
		}
		if b, err := json.Marshal(mergeTheirs(hm, wm, nil)); err == nil {
			out[k] = b
		}
	}
	return out
}

// syncJSONInOrder is syncJSON for a block whose keys' order the agent reads
// (OpenCode lists a model's variants in theirs): one that says the same in
// another order is rewritten too, as an older magpie wrote the variants
// alphabetically (#713).
func syncJSONInOrder(path, key string, value func() any) error {
	cur, ok := edit.GetJSON(path, key)
	if !ok {
		return nil
	}
	v := value()
	if sameJSON(cur, v) && sameOrder(cur, v) {
		return nil
	}
	return edit.SetJSON(path, edit.KV{Path: key, Value: v})
}

// sameOrder reports whether raw JSON and what v marshals to read alike token
// by token, keys in the same order; whitespace and escapes aside.
func sameOrder(raw string, v any) bool {
	b, err := json.Marshal(v)
	if err != nil {
		return false
	}
	x, y := json.NewDecoder(strings.NewReader(raw)), json.NewDecoder(bytes.NewReader(b))
	x.UseNumber()
	y.UseNumber()
	for {
		a, errA := x.Token()
		c, errC := y.Token()
		if errA != nil || errC != nil {
			return errA == io.EOF && errC == io.EOF
		}
		if a != c {
			return false
		}
	}
}

// sameJSON reports whether raw JSON says what v marshals to.
func sameJSON(raw string, v any) bool {
	b, err := json.Marshal(v)
	if err != nil {
		return false
	}
	var x, y any
	if json.Unmarshal([]byte(raw), &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

// syncYAML is syncJSON for a YAML file.
func syncYAML(path, key string, value func() any) error {
	raw, err := edit.Read(path)
	if err != nil || raw == nil {
		return nil
	}
	var doc any
	if yaml.Unmarshal(raw, &doc) != nil {
		return nil
	}
	for _, k := range strings.Split(key, ".") {
		m, _ := doc.(map[string]any)
		if doc = m[k]; doc == nil {
			return nil
		}
	}
	v := value()
	b, err := yaml.Marshal(v)
	if err != nil {
		return err
	}
	var want any
	if yaml.Unmarshal(b, &want) == nil && reflect.DeepEqual(doc, want) {
		return nil
	}
	return edit.SetYAML(path, edit.KV{Path: key, Value: v})
}
