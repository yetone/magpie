package agent

// Dim alpha (arcships/dim, the `dima` command of npm's dimcode-alpha) is
// started on magpie the way Dim documents for any OpenAI-compatible
// endpoint (docs/design/model-startup.md): DIM_BASE_URL with
// DIM_API_FORMAT, DIM_API_KEY, DIM_MODEL, and DIM_MAX_CONTEXT_TOKENS with
// DIM_MAX_OUTPUT_TOKENS for a model Dim doesn't know. Dim reads them for
// that one run only, beside a running daemon, and leaves its saved
// Providers alone; help, version and management commands ignore them.
// The format is openai-compatible, Dim's generic provider, not
// openai-chat, which Dim takes for OpenAI's own.
//
// Nothing of Dim's is written: not its home ($DIMCODE_HOME, else
// ~/.dimcode-alpha), its settings or its database, and nothing reads or
// uses Dim's own account (magpie dropped the DimAgent subscription at its
// vendor's request, v0.1.471). The model picked in Dim's row is in a file
// of magpie's, dim.json in magpie's folder. While it names one of
// magpie's models, magpie puts a function named dima in each shell the
// user has, between "# >>> magpie dima >>>" and "# <<< magpie dima <<<"
// (update.ShellFunction), which runs `magpie dim run` (cli/start_cli.go)
// to start the real dima with the variables; the row gives the command
// for a terminal open before. Picking Dim's own again (or Disconnect)
// removes the file and the function, and dima runs as it did.

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/update"
)

// dimFile is magpie's file holding the model picked for Dim.
func dimFile() string { return filepath.Join(appdir.Config(), "dim.json") }

// dimDir is Dim alpha's folder: $DIMCODE_HOME as given, else
// ~/.dimcode-alpha (dim-cli's default_home; ~/.dimcode is the older dimcode).
func dimDir(at place) string {
	if d := strings.TrimSpace(at.getenv("DIMCODE_HOME")); d != "" {
		return d
	}
	return at.join(".dimcode-alpha")
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
	model := jsonGet(file, "model")
	gw, key := gateway.URL, func() string { return agentKey("dim") }
	on := func() bool { return usesMagpie(model()) }
	spec := func() *launchSpec { return dimLaunch(gw(), key(), model()) }
	shell := func(set bool) {
		if h, _ := homes(); h != at.home {
			return
		}
		if set {
			update.ShellFunction("dima", spec().function)
		} else {
			update.DropShellFunction("dima")
		}
	}
	shellIn := func() []string {
		if h, _ := homes(); h != at.home {
			return nil
		}
		return update.ShellFunctionIn("dima")
	}
	launch := func() string { return dimCommand(gw(), key(), model(), runtime.GOOS == "windows") }
	return &Agent{
		ID: "dim", Name: "Dim", Icon: "dim-color", Aliases: []string{"dima", "dimcode-alpha"}, Spelled: prefixed, NoNames: true,
		Bin: "dima", Dir: dimDir(at), Path: file,
		Launch: func() string {
			if !on() {
				return ""
			}
			return launch()
		},
		Typed: func() string {
			if !on() || len(shellIn()) == 0 {
				return ""
			}
			return "dima"
		},
		Start: func() Starting {
			s := Starting{Bin: "dima"}
			if v := model(); on() {
				s.Env, s.Model = spec().vars(gw(), key()), v
			}
			return s
		},
		Sync: func() error {
			shell(on())
			return nil
		},
		Notice: func() string {
			if !on() {
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
					if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
						return err
					}
					if err := edit.SetJSON(file, edit.KV{Path: "model", Value: v}); err != nil {
						return err
					}
					shell(true)
					return nil
				}
				// Dim's own model is its own to pick: magpie steps out
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
