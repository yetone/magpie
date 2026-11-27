package agent

// Dim alpha (arcships/dim, the `dima` command of npm's dimcode-alpha) has
// a built-in provider named Magpie (default-catalog.json: Responses at
// http://127.0.0.1:3425/v1, an API key, no models until listed). Picking
// one of magpie's models in Dim's row fills that provider in, through the
// API of Dim's daemon (dim_daemon.go), the way Dim's own Providers
// settings do: magpie's key for Dim, the gateway's address when its port
// isn't 3425, on; its models listed from magpie's /v1/models (the limits
// Dim needs given where magpie's list has none); and the model picked
// made Dim's model. So dima, and anything else on Dim's daemon, runs on
// magpie however it is started. Each model is listed by the name the
// agents' lists give it, its provider's after it (the gateway's
// display_name for Dim's key), and listed again when those change
// (dimListed). No other provider of Dim's is touched,
// and a key, or an address of neither Dim's nor magpie's, the user gave
// the Magpie provider is left as it is: magpie only picks the model on
// it. What was there before is kept in magpie's dim.json, beside the
// model, and Disconnect (or picking Dim's own) puts it back where it is
// still as magpie left it. Nothing reads or uses Dim's own account
// (magpie dropped the DimAgent subscription at its vendor's request,
// v0.1.471).
//
// When Dim's daemon can't be reached or doesn't take magpie's model, dima
// is started on magpie the way Dim documents for any OpenAI-compatible
// endpoint (docs/design/model-startup.md) instead: DIM_BASE_URL with
// DIM_API_FORMAT, DIM_API_KEY, DIM_MODEL, and DIM_MAX_CONTEXT_TOKENS with
// DIM_MAX_OUTPUT_TOKENS, read for that one run only. magpie puts a
// function named dima in each shell the user has, between "# >>> magpie
// dima >>>" and "# <<< magpie dima <<<" (update.ShellFunction), which
// runs `magpie dim run` (cli/start_cli.go) to start the real dima with
// them. The format is openai-compatible, Dim's generic provider, not
// openai-chat, which Dim takes for OpenAI's own. Once the daemon is
// reached (a Sync while it runs), the provider is filled in and the
// function goes.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/sessions"
	"github.com/yetone/magpie/internal/update"
)

// dimFile is magpie's file holding the model picked for Dim.
func dimFile() string { return filepath.Join(appdir.Config(), "dim.json") }

// dimDir is Dim alpha's folder: $DIMCODE_HOME as given, else
// ~/.dimcode-alpha (dim-cli's default_home; ~/.dimcode is the older dimcode).
// Sessions reads Dim's sessions from the same folder (sessions.DimDirIn).
func dimDir(at place) string {
	return sessions.DimDirIn(at.getenv("DIMCODE_HOME"), at.join)
}

// dimContext and dimOutput are the limits Dim is given for a model the
// catalog knows none of (Dim refuses to start one without both).
const (
	dimContext = 128000
	dimOutput  = 32000
)

// dimLaunch is how dima is started on magpie: the variables Dim reads,
// with the model in DIM_MODEL (a --model flag would come before a
// subcommand, which Dim refuses).
func dimLaunch(gw, key, model string) *launchSpec {
	ctx, out := dimLimits(model)
	return &launchSpec{id: "dim", bin: "dima", env: map[string]string{
		"DIM_BASE_URL":           gw + "/v1",
		"DIM_API_FORMAT":         "openai-compatible",
		"DIM_API_KEY":            key,
		"DIM_MODEL":              strings.TrimPrefix(model, magpieID+"/"),
		"DIM_MAX_CONTEXT_TOKENS": strconv.Itoa(ctx),
		"DIM_MAX_OUTPUT_TOKENS":  strconv.Itoa(out),
	}}
}

// dimLimits are the context and output limits of a magpie/<ref> model
// for Dim: the catalog's, else dimContext (held to the user's cap) and
// dimOutput.
func dimLimits(model string) (ctx, out int) {
	ref := strings.TrimPrefix(model, magpieID+"/")
	for _, m := range sharedModels("dim") {
		if m.ID == ref {
			ctx, out = m.Context, m.Output
			break
		}
	}
	if ctx <= 0 {
		ctx = unknownWindow(dimContext)
	}
	if out <= 0 || out > ctx {
		out = min(dimOutput, ctx)
	}
	return ctx, out
}

// dimCommand is what starts dima on magpie's model in the shell of this
// system. The variables are put as they are (none needs quoting).
func dimCommand(gw, key, model string, powershell bool) string {
	l := dimLaunch(gw, key, model)
	var b strings.Builder
	for _, kv := range l.vars(gw, key) {
		k, v, _ := strings.Cut(kv, "=")
		if powershell {
			b.WriteString(`$env:` + k + "=" + psQuote(v) + "; ")
		} else {
			b.WriteString(k + "=" + posixQuote(v) + " ")
		}
	}
	return b.String() + "dima"
}

func dim(home string) *Agent { return dimIn(here(home)) }

func dimIn(at place) *Agent {
	file := dimFile()
	dir := dimDir(at)
	model := jsonGet(file, "model")
	gw, key := gateway.URL, func() string { return agentKey("dim") }
	on := func() bool { return usesMagpie(model()) }
	spec := func() *launchSpec { return dimLaunch(gw(), key(), model()) }
	local := func() bool { h, _ := homes(); return h == at.home }
	// wired: Dim's own Magpie provider is on magpie's model, so dima runs
	// on it as it is
	wired := func() bool {
		st := readDimState(file)
		return usesMagpie(st.Model) && st.Dim != nil && st.Dim.Picked != "" && st.Dim.Picked == strings.TrimPrefix(st.Model, magpieID+"/")
	}
	shell := func(set bool) {
		if !local() {
			return
		}
		if set {
			update.ShellFunction("dima", spec().function)
		} else {
			update.DropShellFunction("dima")
		}
	}
	shellIn := func() []string {
		if !local() {
			return nil
		}
		return update.ShellFunctionIn("dima")
	}
	launch := func() string { return dimCommand(gw(), key(), model(), runtime.GOOS == "windows") }
	return &Agent{
		ID: "dim", Name: "Dim", Icon: "dim-color", Aliases: []string{"dima", "dimcode-alpha"}, Spelled: prefixed, NoNames: true,
		Bin: "dima", Dir: dir, Path: file,
		Launch: func() string {
			if !on() || wired() {
				return ""
			}
			return launch()
		},
		Typed: func() string {
			if !on() || wired() || len(shellIn()) == 0 {
				return ""
			}
			return "dima"
		},
		Start: func() Starting {
			s := Starting{Bin: "dima"}
			if v := model(); on() {
				s.Model = v
				if !wired() {
					s.Env = spec().vars(gw(), key())
				}
			}
			return s
		},
		Sync: func() error {
			if !on() {
				shell(false)
				return nil
			}
			// a running daemon gets magpie's address and key again (a new
			// port, a new key), and one a Dim connected before this
			// version never had is given them now; none is started here
			if local() {
				dimWire(dir, file, model(), false, !wired())
			}
			shell(!wired())
			return nil
		},
		Notice: func() string {
			if !on() || wired() {
				return ""
			}
			if in := shellIn(); len(in) > 0 {
				for i := range in {
					in[i] = update.TildeHome(in[i])
				}
				return noticeLaunchTyped.say("agent", "Dim", "bin", "dima", "profiles", strings.Join(in, ", "), "command", launch())
			}
			return noticeLaunch.say("agent", "Dim", "command", launch())
		},
		Fields: []Field{{
			Key: "model", Label: "model",
			Get: func() string {
				if v := model(); usesMagpie(v) {
					return v
				}
				return ""
			},
			Set: func(v string) error {
				if ref, ok := strings.CutPrefix(v, magpieID+"/"); ok && isMagpie(ref) {
					st := readDimState(file)
					st.Model = v
					if err := writeDimState(file, st); err != nil {
						return err
					}
					// Dim's Magpie provider, else (no Dim daemon to
					// reach, the gateway not answering it) the variables
					// of a dima function as before
					shell(!local() || dimWire(dir, file, v, true, true) != nil)
					return nil
				}
				// Dim's own model is its own to pick: magpie steps out
				// and puts back what it changed
				if local() {
					if err := dimUnwire(dir, file); err != nil {
						return err
					}
				}
				if err := os.Remove(file); err != nil && !os.IsNotExist(err) {
					return err
				}
				shell(false)
				return nil
			},
			Options: func(map[string]string) []Option {
				return withVia(nil, "dim", magpieID+"/")
			},
		}},
	}
}

// dimDefault is the base URL of Dim's built-in Magpie provider
// (crates/dim-app-server/resources/default-catalog.json).
const dimDefault = "http://127.0.0.1:3425/v1"

// dimRecord is what magpie changed in Dim, kept in dim.json beside the
// model so Disconnect can put it back.
type dimRecord struct {
	// Wrote: magpie gave Dim's Magpie provider its address and key. Not
	// when the user had given it a key, or an address of neither Dim's
	// nor magpie's, of their own: those are left as they are.
	Wrote bool   `json:"wrote"`
	URL   string `json:"url,omitempty"` // the base URL magpie gave it
	Key   string `json:"key,omitempty"` // a hash of the key magpie gave it
	// the provider's base URL before magpie, and Dim's model (on
	// Disconnect the provider is turned off: Dim clears a key only so)
	BaseURL   string        `json:"baseUrl,omitempty"`
	Selection *dimSelection `json:"selection"`
	Picked    string        `json:"picked,omitempty"` // the model magpie picked in Dim
	// Listed is dimListed when Dim last listed the provider's models
	Listed string `json:"listed,omitempty"`
}

type dimState struct {
	Model string     `json:"model,omitempty"`
	Dim   *dimRecord `json:"dim,omitempty"`
}

func readDimState(file string) dimState {
	var st dimState
	if b, err := os.ReadFile(file); err == nil {
		json.Unmarshal(b, &st)
	}
	return st
}

func writeDimState(file string, st dimState) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	return edit.WriteAtomic(file, append(b, '\n'))
}

// dimListed stands for the models Dim's Magpie provider lists and the
// names it shows them by: the gateway tells Dim each model by the name the
// agents' lists give it, its provider's after it (T_t__biu on X: two
// vendors' DeepSeek read the same in Dim), and Dim takes the names only
// when it lists the models again (provider.refresh).
func dimListed() string {
	h := sha256.New()
	for _, m := range sharedModels("dim") {
		fmt.Fprintf(h, "%s\x00%s\x00", m.ID, m.Name)
	}
	return hex.EncodeToString(h.Sum(nil)[:8])
}

func dimKeyHash(k string) string {
	s := sha256.Sum256([]byte(k))
	return hex.EncodeToString(s[:8])
}

// dimWire puts Dim's built-in Magpie provider on magpie's gateway: its
// base URL (when the port isn't Dim's 3425), magpie's key for Dim, and on;
// and, with pick, lists its models and makes model Dim's model, giving
// it the limits Dim needs when magpie's /v1/models has none. Nothing else
// of Dim's is changed, and a key or address the user gave that provider
// is theirs: magpie only picks the model on it. What was there before is
// kept in dim.json the first time, for dimUnwire.
func dimWire(dir, file, model string, start, pick bool) error {
	dimMu.Lock()
	defer dimMu.Unlock()
	c, err := dimConnect(dir, start)
	if err != nil {
		return err
	}
	defer c.close()
	st := readDimState(file)
	st.Model = model
	p, err := c.provider(magpieID)
	if err != nil {
		return err
	}
	url, key := gateway.URL()+"/v1", agentKey("dim")
	rec := st.Dim
	if rec == nil {
		was, err := c.selection()
		if err != nil {
			return err
		}
		if was != nil {
			was = &dimSelection{Provider: was.Provider, Model: was.Model}
		}
		base := dimURL(p.Connection.BaseURL)
		own := p.Connection.CredentialConfigured || (base != dimDefault && base != dimURL(url))
		rec = &dimRecord{Wrote: !own, BaseURL: p.Connection.BaseURL, Selection: was}
		st.Dim = rec
		// kept before Dim is written, so a change cut short is put back
		if err := writeDimState(file, st); err != nil {
			return err
		}
	}
	if rec.Wrote && (rec.URL == "" || dimURL(p.Connection.BaseURL) == dimURL(rec.URL)) {
		patch := map[string]any{}
		if dimURL(p.Connection.BaseURL) != dimURL(url) {
			patch["baseUrl"] = url
		}
		if !p.Enabled {
			patch["enabled"] = true
		}
		if !p.Connection.CredentialConfigured || rec.Key != dimKeyHash(key) {
			patch["credential"] = map[string]any{"action": "set", "value": key}
		}
		if len(patch) > 0 {
			if err := c.update(p, patch); err != nil {
				return err
			}
		}
		rec.URL, rec.Key = url, dimKeyHash(key)
		if err := writeDimState(file, st); err != nil {
			return err
		}
	}
	listed := dimListed()
	if !pick {
		// Dim's list of a provider magpie picked a model on follows
		// magpie's models and their names (a provider renamed or added,
		// the Provider in model names setting), listed again once they
		// changed: a Dim connected before its list had the providers in
		// it is listed again on the first Sync
		if rec.Picked == "" || rec.Listed == listed {
			return nil
		}
		if err := c.call("provider.refresh", map[string]any{"providerId": magpieID}, nil); err != nil {
			return err
		}
		rec.Listed = listed
		return writeDimState(file, st)
	}
	ref := strings.TrimPrefix(model, magpieID+"/")
	var cat dimCatalog
	if err := c.call("provider.refresh", map[string]any{"providerId": magpieID}, &cat); err != nil {
		return err
	}
	rec.Listed = listed
	i := slices.IndexFunc(cat.Models, func(m dimModel) bool { return m.ID == ref })
	if i < 0 {
		return fmt.Errorf("Dim's Magpie provider doesn't list %s", ref)
	}
	if miss := cat.Models[i].MissingLimits; len(miss) > 0 {
		// Dim picks no model without both limits (provider.select's
		// provider_invalid_config); magpie gives the ones it lacks
		ctx, out := dimLimits(model)
		e := map[string]any{}
		for _, m := range miss {
			switch m {
			case "contextWindow":
				e[m] = ctx
			case "maxOutputTokens":
				e[m] = out
			}
		}
		if err := c.call("provider.models.edit", map[string]any{"providerId": magpieID, "expectedVersion": cat.Version, "modelId": ref, "edit": e}, nil); err != nil {
			return err
		}
	}
	cur, err := c.selection()
	if err != nil {
		return err
	}
	if cur == nil || cur.Provider != magpieID || cur.Model != ref {
		if err := c.pick(magpieID, ref, cur); err != nil {
			return err
		}
	}
	rec.Picked = ref
	return writeDimState(file, st)
}

// dimUnwire puts back what dimWire changed, where it is still as magpie
// left it: Dim's model before (Dim can't go back to none: a Dim that had
// none keeps magpie's, which says no model is available once the
// provider is off), and the Magpie provider's address, key (cleared) and
// off. A value the user changed since is theirs and stays.
func dimUnwire(dir, file string) error {
	rec := readDimState(file).Dim
	if rec == nil {
		return nil
	}
	dimMu.Lock()
	defer dimMu.Unlock()
	c, err := dimConnect(dir, true)
	if errors.Is(err, errDimAway) && proc.FindTool("dima") == "" {
		return nil // Dim is gone, its settings with it
	}
	if err != nil {
		return err
	}
	defer c.close()
	if was := rec.Selection; rec.Picked != "" && was != nil && (was.Provider != magpieID || was.Model != rec.Picked) {
		if cur, err := c.selection(); err == nil && cur != nil && cur.Provider == magpieID && cur.Model == rec.Picked {
			c.pick(was.Provider, was.Model, cur) // a model since gone stays unpicked
		}
	}
	if !rec.Wrote {
		return nil
	}
	p, err := c.provider(magpieID)
	if err != nil {
		return err
	}
	if rec.URL == "" || dimURL(p.Connection.BaseURL) != dimURL(rec.URL) {
		return nil
	}
	// Dim clears a key only with the provider turned off in the same patch
	patch := map[string]any{"enabled": false}
	if p.Connection.CredentialConfigured {
		patch["credential"] = map[string]any{"action": "clear"}
	}
	if b := rec.BaseURL; b != "" && dimURL(b) != dimURL(p.Connection.BaseURL) {
		patch["baseUrl"] = b
	}
	return c.update(p, patch)
}
