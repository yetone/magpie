package agent

// AtomCode (atomgit.com/atomgit_atomcode/atomcode, a Rust terminal agent)
// keeps its settings in ~/.atomcode/config.toml ($ATOMCODE_HOME). A model is
// a [models."<key>"] table naming a [provider_accounts.<account>] table —
// what its own CodingPlan sign-in writes since 5.0.7: the account holds the
// protocol, the gateway's /v1 and the key; the model table holds the model
// string, the context window (AtomCode compacts as it nears it), the output
// limit, whether it sees images, the reasoning levels its picker offers and
// the one in use. magpie adds the account "magpie" and one model table per
// catalog model, keyed "magpie/<provider>/<model>" — the model string goes
// to the gateway as it is, for it to resolve — and points default_provider
// and default_model at the one picked, default_model winning when both are
// set; the defaults the user had are stashed and put back when magpie steps
// out. Requests are Chat Completions with Bearer auth and a User-Agent of
// atomcode/<version>, which the gateway knows apart; the key is AtomCode's
// own, so a request is its agent's even when the header is overridden.

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
)

// atomcodeAccount is the account table magpie writes, and atomcodeModels the
// header prefix of every model table it writes.
var atomcodeAccount = `provider_accounts."` + magpieID + `"`
var atomcodeModels = `models."` + magpieID + "/"
var atomcodeTablePrefixes = []string{atomcodeModels, "models.'" + magpieID + "/"}
var atomcodeAccountNames = []string{atomcodeAccount, "provider_accounts." + magpieID, "provider_accounts.'" + magpieID + "'"}

var atomcodeEffortLevels = []string{"low", "medium", "high", "xhigh", "max"}

// atomcodeContext is what AtomCode is told of a model whose context magpie
// doesn't know: it has to be told one, and compacts as it nears it.
const atomcodeContext = 128000

// atomcodeKey is the model table key of a catalog ref — the value
// default_provider and default_model name.
func atomcodeKey(ref string) string { return magpieID + "/" + ref }

// atomcodeTable is the header of the model table a key names. Magpie's are
// always written quoted; the user's own are found by their decoded names
// (atomcodeOwnModels), not built ones.
func atomcodeTable(key string) string { return "models." + strconv.Quote(key) }

func atomcode(home string) *Agent {
	dir := filepath.Join(home, ".atomcode")
	if h := os.Getenv("ATOMCODE_HOME"); filepath.IsAbs(h) {
		dir = filepath.Clean(h)
	}
	path := filepath.Join(dir, "config.toml")
	key := "atomcode:" + path
	get := func(k string) string { v, _ := edit.GetTOMLTop(path, k); return v }
	// cur is the model table key in use: default_model, which wins, else
	// default_provider.
	cur := func() string {
		if v := get("default_model"); v != "" {
			return v
		}
		return get("default_provider")
	}
	// own reports whether AtomCode still has a model table by this key.
	own := func(k string) bool {
		ts, err := edit.TOMLTables(path)
		if err != nil {
			return false
		}
		for _, t := range ts {
			if name, ok := kimiModelKey(t); ok && name == k {
				return true
			}
			if name, ok := atomcodeProviderName(t); ok && name == k {
				return true
			}
		}
		return false
	}
	// setDefaults points both defaults at the model table key v, or takes
	// both out for "".
	setDefaults := func(v string) error {
		if v == "" {
			return edit.DelTOMLTop(path, "default_provider", "default_model")
		}
		return edit.SetTOMLTop(path,
			edit.KV{Path: "default_provider", Value: v},
			edit.KV{Path: "default_model", Value: v})
	}
	setOwn := func(v string) error {
		if atomcodeHasLegacyProvider(path, v) {
			if err := edit.DelTOMLTop(path, "default_model"); err != nil {
				return err
			}
			return edit.SetTOMLTop(path, edit.KV{Path: "default_provider", Value: v})
		}
		return edit.SetTOMLTop(path, edit.KV{Path: "default_model", Value: v})
	}
	writeMagpie := func() error {
		return edit.SetTOMLTablesMatching(path, atomcodeAccountNames, atomcodeTablePrefixes, atomcodeTables(path))
	}
	dropMagpie := func() error {
		return edit.SetTOMLTablesMatching(path, atomcodeAccountNames, atomcodeTablePrefixes, nil)
	}
	return atomic(&Agent{
		ID: "atomcode", Name: "AtomCode", Icon: "atomcode",
		UA:  []string{"atomcode"},
		Bin: "atomcode", Dir: dir, Path: path,
		Sync: func() error {
			ts, err := edit.TOMLTables(path)
			if err != nil {
				return err
			}
			for _, t := range ts {
				if atomcodeAccountTable(t) || atomcodeMagpieModelTable(t) {
					return writeMagpie()
				}
			}
			return nil
		},
		Notice: func() string {
			if Running(`(^|/)atomcode( |$)`) {
				return "AtomCode reads its settings at start-up — restart open atomcode sessions to use this."
			}
			return ""
		},
		Check: func() string {
			k := cur()
			if !usesMagpie(k) {
				return ""
			}
			t, _, err := atomcodeModelValues(path, k)
			if err != nil {
				return err.Error()
			}
			if t == nil {
				return "AtomCode's [models." + strconv.Quote(k) + "] (config.toml) is gone, so it no longer reaches magpie"
			}
			a, err := atomcodeAccountValues(path)
			if err != nil {
				return err.Error()
			}
			if a == nil {
				return "AtomCode's [" + atomcodeAccount + "] (config.toml) is gone, so it no longer reaches magpie"
			}
			return wiringOff("AtomCode", path, func(f string) (string, bool) { v, ok := a[f]; return v, ok },
				"base_url", gatewayV1(), "api_key", gateway.TokenFor("atomcode"))
		},
		Fields: []Field{{
			Key: "model", Label: "model",
			Get: cur,
			Set: func(v string) error {
				was := cur()
				if ref, ok := strings.CutPrefix(v, magpieID+"/"); ok && isMagpie(ref) {
					if !usesMagpie(was) {
						stash(map[string]string{
							key + ":default_provider": get("default_provider"),
							key + ":default_model":    get("default_model"),
						})
					}
					if err := writeMagpie(); err != nil {
						return err
					}
					return setDefaults(v)
				}
				if !usesMagpie(was) {
					// no model of magpie's was in use: the pick moves
					// default_model, default_provider stays as it is
					if v == "" {
						return edit.DelTOMLTop(path, "default_provider", "default_model")
					}
					return setOwn(v)
				}
				// magpie was in use: the defaults the user had come back
				// where AtomCode still has them
				saved := stashLoad()
				p, m := saved[key+":default_provider"], saved[key+":default_model"]
				if p != "" && !usesMagpie(p) && own(p) {
					if err := edit.SetTOMLTop(path, edit.KV{Path: "default_provider", Value: p}); err != nil {
						return err
					}
				} else {
					if err := edit.DelTOMLTop(path, "default_provider"); err != nil {
						return err
					}
				}
				if v == "" {
					if m != "" && !usesMagpie(m) && own(m) {
						if err := edit.SetTOMLTop(path, edit.KV{Path: "default_model", Value: m}); err != nil {
							return err
						}
					} else {
						if err := edit.DelTOMLTop(path, "default_model"); err != nil {
							return err
						}
					}
					if err := dropMagpie(); err != nil {
						return err
					}
					forget(key+":default_provider", key+":default_model")
					return nil
				}
				// a model of the user's own picked: it is the current one
				if err := setOwn(v); err != nil {
					return err
				}
				if err := dropMagpie(); err != nil {
					return err
				}
				forget(key+":default_provider", key+":default_model")
				return nil
			},
			Options: func(c map[string]string) []Option {
				return append(atomcodeOwnModels(path, c["model"]), viaMagpie("atomcode", magpieID+"/")...)
			},
		}, {
			// the current model table's reasoning_effort, sent with every
			// request to it; one table per model keeps one with each
			Key: "effort", Label: "effort",
			Get: func() string {
				k := cur()
				if !usesMagpie(k) {
					return ""
				}
				t, _, err := atomcodeModelValues(path, k)
				if err != nil || t == nil {
					return ""
				}
				return t["reasoning_effort"]
			},
			Set: func(v string) error {
				k := cur()
				if !usesMagpie(k) {
					return fmt.Errorf("pick AtomCode's model first; the effort is kept with its model")
				}
				_, table, err := atomcodeModelValues(path, k)
				if err != nil {
					return err
				}
				if v == "" {
					// clearing a gone table's effort is a no-op, nothing to
					// orphan; a real table loses its reasoning_effort
					if table == "" {
						return nil
					}
					return edit.DelTOMLKey(path, table, "reasoning_effort")
				}
				if table == "" {
					// the table is gone (Drift says so too): a fresh one of
					// nothing but an effort would be an orphan
					return fmt.Errorf("AtomCode's [models.%s] (config.toml) is gone — pick the model again", strconv.Quote(k))
				}
				ref := strings.TrimPrefix(k, magpieID+"/")
				var offered []string
				for _, m := range magpieModels("atomcode") {
					if m.ID == ref {
						offered = atomcodeSupportedEfforts(m.Efforts)
						break
					}
				}
				if !slices.Contains(offered, v) {
					return fmt.Errorf("AtomCode does not support effort %q for this model", v)
				}
				return edit.SetTOMLKey(path, table, "reasoning_effort", v)
			},
			Options: func(c map[string]string) []Option {
				if ref, ok := strings.CutPrefix(c["model"], magpieID+"/"); ok {
					for _, m := range magpieModels("atomcode") {
						if m.ID == ref {
							return static(atomcodeSupportedEfforts(m.Efforts)...)
						}
					}
				}
				return nil
			},
		}},
	}, path)
}

// atomcodeTables are magpie's account and model tables: the gateway spoken
// to as AtomCode's own Chat Completions, one model table per catalog model
// with the model's window (AtomCode compacts as it nears it), its output
// limit, whether it sees images, and the reasoning levels its picker offers.
// No reasoning_effort is picked for the user — the vendor's default stands
// until they choose one.
func atomcodeTables(path string) []edit.Table {
	efforts := atomcodeExistingEfforts(path)
	out := []edit.Table{{Name: atomcodeAccount, KVs: []edit.KV{
		// openai-compatible is the documented preset for a custom endpoint
		{Path: "provider", Value: "openai-compatible"},
		{Path: "base_url", Value: gatewayV1()},
		{Path: "api_key", Value: gateway.TokenFor("atomcode")},
	}}}
	for _, m := range magpieModels("atomcode") {
		ctx := m.Context
		if ctx <= 0 {
			ctx = atomcodeContext
		}
		kvs := []edit.KV{
			{Path: "account", Value: magpieID},
			{Path: "model", Value: m.ID},
			{Path: "context_window", Value: ctx},
		}
		// an output above the window is cut to it — the window AtomCode is
		// told when magpie doesn't know the model's is atomcodeContext
		if n := maxTokens(m); n > 0 {
			if m.Context <= 0 && n > atomcodeContext {
				n = atomcodeContext
			}
			kvs = append(kvs, edit.KV{Path: "max_tokens", Value: n})
		}
		if m.Images {
			kvs = append(kvs, edit.KV{Path: "supports_vision", Value: true})
		} else {
			kvs = append(kvs, edit.KV{Path: "supports_vision", Value: false})
		}
		supported := atomcodeSupportedEfforts(m.Efforts)
		if len(supported) > 0 {
			var levels []string
			for _, e := range supported {
				levels = append(levels, strconv.Quote(e))
			}
			kvs = append(kvs, edit.KV{Path: "reasoning_effort_levels", Value: edit.Raw("[" + strings.Join(levels, ", ") + "]")})
		}
		if effort := efforts[atomcodeKey(m.ID)]; effort != "" {
			kvs = append(kvs, edit.KV{Path: "reasoning_effort", Value: effort})
		}
		out = append(out, edit.Table{Name: atomcodeTable(atomcodeKey(m.ID)), KVs: kvs})
	}
	return out
}

// atomcodeOwnModels are the models of the user's own in AtomCode's config,
// and the current one: both new model tables and legacy providers.* entries.
func atomcodeOwnModels(path, cur string) []Option {
	tables, _ := edit.TOMLTables(path)
	seen := map[string]bool{}
	var out []Option
	for _, t := range tables {
		k, ok := kimiModelKey(t)
		if !ok {
			k, ok = atomcodeProviderName(t)
		}
		if !ok || seen[k] || usesMagpie(k) {
			continue
		}
		seen[k] = true
		m, _ := edit.GetTOMLTable(path, t)
		out = append(out, Option{Value: k, Icon: modelIcon("", m["model"])})
	}
	if cur != "" && !seen[cur] && !usesMagpie(cur) {
		var icon string
		if m, _, err := atomcodeModelValues(path, cur); err == nil {
			icon = modelIcon("", m["model"])
		}
		out = append([]Option{{Value: cur, Icon: icon}}, out...)
	}
	return group("AtomCode", out)
}

func atomcodeProviderName(table string) (string, bool) {
	k, ok := strings.CutPrefix(table, "providers.")
	if !ok {
		return "", false
	}
	return kimiModelKey("models." + k)
}

func atomcodeHasLegacyProvider(path, key string) bool {
	tables, _ := edit.TOMLTables(path)
	for _, table := range tables {
		if name, ok := atomcodeProviderName(table); ok && name == key {
			return true
		}
	}
	return false
}

func atomcodeAccountTable(table string) bool {
	for _, name := range atomcodeAccountNames {
		if table == name {
			return true
		}
	}
	return false
}

func atomcodeMagpieModelTable(table string) bool {
	k, ok := kimiModelKey(table)
	return ok && strings.HasPrefix(k, magpieID+"/")
}

func atomcodeAccountValues(path string) (map[string]string, error) {
	for _, name := range atomcodeAccountNames {
		v, err := edit.GetTOMLTable(path, name)
		if err != nil {
			return nil, err
		}
		if v != nil {
			return v, nil
		}
	}
	return nil, nil
}

func atomcodeModelValues(path, key string) (map[string]string, string, error) {
	tables, err := edit.TOMLTables(path)
	if err != nil {
		return nil, "", err
	}
	for _, table := range tables {
		if decoded, ok := kimiModelKey(table); ok && decoded == key {
			values, err := edit.GetTOMLTable(path, table)
			return values, table, err
		}
	}
	return nil, "", nil
}

func atomcodeExistingEfforts(path string) map[string]string {
	out := map[string]string{}
	tables, _ := edit.TOMLTables(path)
	for _, table := range tables {
		key, ok := kimiModelKey(table)
		if !ok || !strings.HasPrefix(key, magpieID+"/") {
			continue
		}
		values, _ := edit.GetTOMLTable(path, table)
		if values["reasoning_effort"] != "" {
			out[key] = values["reasoning_effort"]
		}
	}
	return out
}

func atomcodeSupportedEfforts(efforts []string) []string {
	var out []string
	for _, level := range atomcodeEffortLevels {
		for _, effort := range efforts {
			if effort == level {
				out = append(out, level)
				break
			}
		}
	}
	return out
}
