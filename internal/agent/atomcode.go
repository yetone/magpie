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
	"strconv"
	"strings"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
)

// atomcodeAccount is the account table magpie writes, and atomcodeModels the
// header prefix of every model table it writes. Both are quoted, so a prefix
// names magpie's alone ("provider_accounts.magpie" would also cover a user's
// "magpie-2").
var atomcodeAccount = `provider_accounts."` + magpieID + `"`
var atomcodeModels = `models."` + magpieID + "/"

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
			if name, ok := atomcodeModelName(t); ok && name == k {
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
	writeMagpie := func() error {
		return edit.SetTOMLTables(path, []string{atomcodeAccount, atomcodeModels}, atomcodeTables())
	}
	dropMagpie := func() error {
		return edit.SetTOMLTables(path, []string{atomcodeAccount, atomcodeModels}, nil)
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
				if strings.HasPrefix(t, atomcodeAccount) || strings.HasPrefix(t, atomcodeModels) {
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
			t, err := edit.GetTOMLTable(path, atomcodeTable(k))
			if err != nil {
				return err.Error()
			}
			if t == nil {
				return "AtomCode's [models." + strconv.Quote(k) + "] (config.toml) is gone, so it no longer reaches magpie"
			}
			a, err := edit.GetTOMLTable(path, atomcodeAccount)
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
					return edit.SetTOMLTop(path, edit.KV{Path: "default_model", Value: v})
				}
				// magpie was in use: the defaults the user had come back
				// where AtomCode still has them
				saved := stashLoad()
				p, m := saved[key+":default_provider"], saved[key+":default_model"]
				if p != "" && !usesMagpie(p) {
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
					forget(key + ":default_provider", key + ":default_model")
					return nil
				}
				// a model of the user's own picked: it is the current one
				if err := edit.SetTOMLTop(path, edit.KV{Path: "default_model", Value: v}); err != nil {
					return err
				}
				if err := dropMagpie(); err != nil {
					return err
				}
				forget(key + ":default_provider", key + ":default_model")
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
				t, err := edit.GetTOMLTable(path, atomcodeTable(k))
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
				if v == "" {
					return edit.DelTOMLKey(path, atomcodeTable(k), "reasoning_effort")
				}
				return edit.SetTOMLKey(path, atomcodeTable(k), "reasoning_effort", v)
			},
			Options: func(c map[string]string) []Option {
				if ref, ok := strings.CutPrefix(c["model"], magpieID+"/"); ok {
					for _, m := range magpieModels("atomcode") {
						if m.ID == ref && len(m.Efforts) > 0 {
							return static(m.Efforts...)
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
func atomcodeTables() []edit.Table {
	out := []edit.Table{{Name: atomcodeAccount, KVs: []edit.KV{
		{Path: "provider", Value: "openai"},
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
		}
		if len(m.Efforts) > 0 {
			var levels []string
			for _, e := range m.Efforts {
				levels = append(levels, strconv.Quote(e))
			}
			kvs = append(kvs, edit.KV{Path: "reasoning_effort_levels", Value: edit.Raw("[" + strings.Join(levels, ", ") + "]")})
		}
		out = append(out, edit.Table{Name: atomcodeTable(atomcodeKey(m.ID)), KVs: kvs})
	}
	return out
}

// atomcodeModelName decodes one model table's key, bare or quoted,
// rejecting nested tables.
func atomcodeModelName(table string) (string, bool) {
	k, ok := strings.CutPrefix(table, "models.")
	if !ok {
		return "", false
	}
	if u, err := strconv.Unquote(k); err == nil {
		return u, true
	}
	if len(k) >= 2 && k[0] == '\'' && k[len(k)-1] == '\'' {
		k = k[1 : len(k)-1]
		return k, !strings.Contains(k, "'")
	}
	return k, k != "" && !strings.ContainsAny(k, ".\"'")
}

// atomcodeOwnModels are the models of the user's own in AtomCode's config,
// and the current one: its CodingPlan sign-in's and any account they added,
// each shown under its model table's key.
func atomcodeOwnModels(path, cur string) []Option {
	tables, _ := edit.TOMLTables(path)
	seen := map[string]bool{}
	var out []Option
	for _, t := range tables {
		k, ok := atomcodeModelName(t)
		if !ok || seen[k] || usesMagpie(k) {
			continue
		}
		seen[k] = true
		m, _ := edit.GetTOMLTable(path, t)
		out = append(out, Option{Value: k, Icon: modelIcon("", m["model"])})
	}
	if cur != "" && !seen[cur] && !usesMagpie(cur) {
		var icon string
		if m, err := edit.GetTOMLTable(path, atomcodeTable(cur)); err == nil {
			icon = modelIcon("", m["model"])
		}
		out = append([]Option{{Value: cur, Icon: icon}}, out...)
	}
	return group("AtomCode", out)
}
