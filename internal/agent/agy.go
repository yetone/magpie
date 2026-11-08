package agent

// Antigravity's CLI, agy, keeps its settings in
// ~/.gemini/antigravity-cli/settings.json. With "modelProvider": "gemini" it
// asks a Gemini API endpoint rather than Google's own backend, and lists the
// models of customModelsConfig.customModels, each {apiProvider, modelName},
// the one picked being "model". magpie's gateway speaks the Gemini API, so
// magpie adds one custom model per catalog model, keyed
// "magpie/<provider>/<model>", and sets modelProvider and model; the user's
// own are stashed and put back when magpie steps out. The endpoint and key
// agy takes only from its environment (GOOGLE_GEMINI_BASE_URL and
// GEMINI_API_KEY; no .env file), so magpie can't set them: the agent's row
// gives the command that starts agy with them (see Agent.Launch). That
// names the model with --model too: agy 1.2 asks for a custom model picked
// in settings.json or its /model picker with no model name ("model is
// empty"); only one given on the command line resolves.
//
// So that typing agy just works (bestony on Discord: the command was
// copied at each start), magpie puts a function named agy in the profile
// of each shell the user has, between "# >>> magpie agy >>>" and
// "# <<< magpie agy <<<" (update.ShellFunction), while agy is on one of
// magpie's models. It runs `magpie agy run`, which starts the real agy
// with the two variables and --model when settings.json's model is still
// magpie's and as it is otherwise (cli/agy_cli.go); PowerShell's asks
// `magpie agy env` for them, since the app it would run is a GUI program
// PowerShell doesn't wait for. A terminal open before takes the command
// from the row.

import (
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/jsonc"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/update"
)

const agyModels = "customModelsConfig.customModels"

// AgyLaunch is the command that starts agy on magpie's gateway and one of
// its custom models, in the shell of this system. Its key names agy: its
// requests go out as Google's Gemini SDK's (User-Agent google-genai-sdk/…),
// with nothing of agy's own, so the gateway knows them by the key
// (x-goog-api-key) alone. A command copied before, with the plain token,
// still reaches magpie.
func AgyLaunch(model string) string {
	return agyLaunchAt(gateway.URL(), agentKey("agy"), model, runtime.GOOS == "windows")
}

// agyLaunchAt is AgyLaunch for agy reaching the gateway at gw with key, in
// PowerShell or a POSIX shell.
func agyLaunchAt(gw, key, model string, powershell bool) string {
	if powershell {
		return `$env:GEMINI_API_KEY="` + key + `"; $env:GOOGLE_GEMINI_BASE_URL="` + gw + `"; agy --model '` + strings.ReplaceAll(model, "'", "''") + `'`
	}
	return "GEMINI_API_KEY=" + key + " GOOGLE_GEMINI_BASE_URL=" + gw + " agy --model '" + strings.ReplaceAll(model, "'", `'\''`) + "'"
}

// AgyStart is what starts agy on magpie, the variables and the model,
// while settings.json's model is one of magpie's; nil and "" otherwise,
// when agy starts as it is.
func AgyStart() (env []string, model string) {
	home, _ := homes()
	m := jsonGet(filepath.Join(home, ".gemini", "antigravity-cli", "settings.json"), "model")()
	if !usesMagpie(m) {
		return nil, ""
	}
	return []string{"GEMINI_API_KEY=" + agentKey("agy"), "GOOGLE_GEMINI_BASE_URL=" + gateway.URL()}, m
}

// agyShell gives the user's shells the function agy, or takes it out of
// them; only for agy on this machine. What fails leaves the setting as it
// is: the row's command still starts it.
func agyShell(home string, on bool) {
	if here, _ := homes(); here != home {
		return
	}
	if on {
		update.ShellFunction("agy", agyFunction)
	} else {
		update.DropShellFunction("agy")
	}
}

// agyFunction is the function agy in a shell of kind, which runs prog
// (this magpie) to start it; the agy on PATH when prog is gone.
func agyFunction(kind, prog string) string {
	switch kind {
	case "fish":
		q := "'" + strings.NewReplacer(`\`, `\\`, "'", `\'`).Replace(prog) + "'"
		return "function agy\n    if test -x " + q + "\n        " + q + " agy run $argv\n    else\n        command agy $argv\n    end\nend"
	case "powershell":
		q := "'" + strings.ReplaceAll(prog, "'", "''") + "'"
		return `function agy {
    $real = Get-Command agy -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
    if (-not $real) { Write-Error 'agy is not installed'; return }
    $start = @{}
    if (Test-Path -LiteralPath ` + q + `) {
        foreach ($l in @(& ` + q + ` agy env)) { $k, $v = "$l" -split '=', 2; if ($v) { $start[$k] = $v } }
    }
    if (-not $start.model) { & $real @args; return }
    $was = $env:GEMINI_API_KEY, $env:GOOGLE_GEMINI_BASE_URL
    $env:GEMINI_API_KEY = $start.GEMINI_API_KEY; $env:GOOGLE_GEMINI_BASE_URL = $start.GOOGLE_GEMINI_BASE_URL
    try {
        if (@($args | Where-Object { "$_" -match '^--?model(=|$)' }).Count) { & $real @args } else { & $real --model $start.model @args }
    } finally { $env:GEMINI_API_KEY = $was[0]; $env:GOOGLE_GEMINI_BASE_URL = $was[1] }
}`
	}
	q := "'" + strings.ReplaceAll(prog, "'", `'\''`) + "'"
	return agyPosixHead + "  if [ -x " + q + " ]; then " + q + ` agy run "$@"; else command agy "$@"; fi` + "\n}"
}

// agyPosixHead opens the function agy in a POSIX shell's profile. It is
// "agy() {", not bash's "function agy {", which dash refuses: a ~/.profile
// that reads ~/.bashrc is read by sh -lc too, as Debian's dash (ljlwlcz on
// X: fnOS, where an SSH probe's sh -lc failed on it). An alias named agy
// would be expanded in "agy()", so it is taken out first, on a line of its
// own, since a shell expands aliases a line at a time.
const agyPosixHead = "unalias agy 2>/dev/null || true\nagy() {\n"

// agyDistroProfiles are the profiles of a WSL distro's shells the
// function goes in, as magpie opens them: bash's ~/.bashrc (which the
// ~/.profile of a login bash reads on Debian and Ubuntu), zsh's ~/.zshrc
// and fish's config.fish, each where it is already, and the one of the
// user's login shell in the distro's /etc/passwd (bash when it says
// nothing) made where it isn't, as fish's is where ~/.config/fish is. The
// distro's ZDOTDIR and XDG_CONFIG_HOME aren't read.
func agyDistroProfiles(at place) []update.ShellProfile {
	login := distroLoginShell(at)
	fish := at.join(".config", "fish", "config.fish")
	return []update.ShellProfile{
		{Kind: "posix", Path: at.join(".bashrc"), Create: login == "bash"},
		{Kind: "posix", Path: at.join(".zshrc"), Create: login == "zsh"},
		{Kind: "fish", Path: fish, Create: login == "fish" || at.isDir(filepath.Dir(fish))},
	}
}

// distroLoginShell is the name of the login shell /etc/passwd gives the
// distro's user, the one whose home is at's; "bash" when it can't say.
func distroLoginShell(at place) string {
	if at.sys == nil {
		return "bash"
	}
	b, err := os.ReadFile(at.sys("/etc/passwd"))
	if err != nil {
		return "bash"
	}
	home := at.native(at.home)
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Split(strings.TrimRight(l, "\r"), ":")
		if len(f) >= 7 && f[5] == home && f[6] != "" {
			return path.Base(f[6])
		}
	}
	return "bash"
}

// agyDistroModel is the sed script that prints settings.json's model while
// it is one of magpie's, for the function in a distro, where no magpie
// runs to read it. Its model is a line of its own as magpie writes the
// file; one written on a single line is found too, so long as nothing
// after it is named "model".
const agyDistroModel = `s|^.*"model"[[:space:]]*:[[:space:]]*"\(` + magpieID + `/[^"]*\)".*$|\1|p`

// agyDistroFunction is the function agy in a distro's shell of kind:
// while settings.json's model is magpie's, the agy on PATH started with
// the gateway at gw, its key and --model (none when one is given), as
// `magpie agy run` does on this machine; as it is otherwise.
func agyDistroFunction(kind, key, gw string) string {
	q := func(v string) string { return "'" + strings.ReplaceAll(v, "'", `'\''`) + "'" }
	settings := `"$HOME/.gemini/antigravity-cli/settings.json"`
	if kind == "fish" {
		fq := func(v string) string { return "'" + strings.NewReplacer(`\`, `\\`, "'", `\'`).Replace(v) + "'" }
		return "function agy\n" +
			"    set -l m (sed -n " + fq(agyDistroModel) + " " + settings + " 2>/dev/null | head -n 1)\n" +
			"    if test -z \"$m\"\n        command agy $argv\n        return\n    end\n" +
			"    for a in $argv\n        switch $a\n            case '--model' '--model=*' '-model' '-model=*'\n                set m ''\n        end\n    end\n" +
			"    if test -n \"$m\"\n        set argv --model $m $argv\n    end\n" +
			"    env GEMINI_API_KEY=" + fq(key) + " GOOGLE_GEMINI_BASE_URL=" + fq(gw) + " agy $argv\nend"
	}
	return agyPosixHead +
		"  local m a\n" +
		"  m=$(sed -n " + q(agyDistroModel) + " " + settings + " 2>/dev/null | head -n 1)\n" +
		`  if [ -z "$m" ]; then command agy "$@"; return; fi` + "\n" +
		`  for a in "$@"; do case "$a" in --model|--model=*|-model|-model=*) m=;; esac; done` + "\n" +
		`  if [ -n "$m" ]; then set -- --model "$m" "$@"; fi` + "\n" +
		"  GEMINI_API_KEY=" + q(key) + " GOOGLE_GEMINI_BASE_URL=" + q(gw) + ` command agy "$@"` + "\n}"
}

// agyCustom reads customModels as it is, raw JSON by key.
func agyCustom(path string) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	raw, err := edit.Read(path)
	if err != nil || len(raw) == 0 {
		return out
	}
	gjson.GetBytes(jsonc.ToJSONInPlace(raw), agyModels).ForEach(func(k, v gjson.Result) bool {
		out[k.String()] = json.RawMessage(v.Raw)
		return true
	})
	return out
}

func agy(home string) *Agent { return agyIn(here(home)) }

// agyIn is agy at a place: this machine's home, or a WSL distro's, whose
// shells get a function of their own (agyDistroFunction).
func agyIn(at place) *Agent {
	home := at.home
	dir := at.open(filepath.Join(home, ".gemini", "antigravity-cli"))
	path := filepath.Join(dir, "settings.json")
	get := jsonGet(path, "model")
	provider := jsonGet(path, "modelProvider")
	stashModel, stashProvider := "agy:"+path+":model", "agy:"+path+":modelProvider"
	// setModels writes customModels with the user's own and, when with,
	// magpie's; none left takes the key out
	setModels := func(with bool) error {
		ms := map[string]any{}
		for k, v := range agyCustom(path) {
			if !strings.HasPrefix(k, magpieID+"/") {
				ms[k] = v
			}
		}
		if with {
			for _, m := range sharedModels("agy") {
				ms[magpieID+"/"+m.ID] = map[string]string{"apiProvider": "API_PROVIDER_GOOGLE_GEMINI", "modelName": m.ID}
			}
		}
		if len(ms) > 0 {
			return edit.SetJSON(path, edit.KV{Path: agyModels, Value: ms})
		}
		if err := edit.DelJSON(path, agyModels); err != nil {
			return err
		}
		if v, ok := edit.GetJSON(path, "customModelsConfig"); ok && strings.TrimSpace(v) == "{}" {
			return edit.DelJSON(path, "customModelsConfig")
		}
		return nil
	}
	// put sets key to v, or takes it out for ""
	put := func(key, v string) error {
		if v == "" {
			return edit.DelJSON(path, key)
		}
		return edit.SetJSON(path, edit.KV{Path: key, Value: v})
	}
	// the function agy, in this machine's shells or the distro's
	distro := at.sys != nil
	gw, key := at.gw, func() string { return agentKeyAt("agy", at.gw()) }
	shell := func(on bool) {
		if !distro {
			agyShell(home, on)
			return
		}
		if on {
			k, g := key(), gw()
			update.ShellFunctionAt(agyDistroProfiles(at), "agy", func(kind string) string { return agyDistroFunction(kind, k, g) })
		} else {
			update.DropShellFunctionAt(agyDistroProfiles(at), "agy")
		}
	}
	shellIn := func() []string {
		if !distro {
			return update.ShellFunctionIn("agy")
		}
		return update.ShellFunctionInAt(agyDistroProfiles(at), "agy")
	}
	launch := func(model string) string {
		if !distro {
			return AgyLaunch(model)
		}
		return agyLaunchAt(gw(), key(), model, false)
	}
	return &Agent{
		ID: "agy", Name: "Antigravity CLI", Icon: "antigravity-color", Aliases: []string{"antigravity-cli"}, Spelled: prefixed, NoNames: true,
		Bin: "agy", Dir: dir, Path: path,
		Launch: func() string {
			if v := get(); usesMagpie(v) {
				return launch(v)
			}
			return ""
		},
		Typed: func() string {
			if !usesMagpie(get()) || len(shellIn()) == 0 {
				return ""
			}
			return "agy"
		},
		Sync: func() error {
			on := usesMagpie(get())
			shell(on)
			if !on {
				return nil
			}
			return setModels(true)
		},
		Notice: func() string {
			v := get()
			if !usesMagpie(v) {
				return ""
			}
			if in := shellIn(); len(in) > 0 {
				for i := range in {
					if distro {
						in[i] = at.native(in[i])
					} else {
						in[i] = update.TildeHome(in[i])
					}
				}
				return noticeAgyTyped.say("profiles", strings.Join(in, ", "), "command", launch(v))
			}
			return noticeAgy.say("command", launch(v))
		},
		Check: func() string {
			v := get()
			if !usesMagpie(v) {
				return ""
			}
			if _, ok := agyCustom(path)[v]; !ok {
				return "Antigravity CLI's custom model " + v + " (settings.json) is gone, so it no longer reaches magpie"
			}
			if p := provider(); p != "gemini" {
				return "Antigravity CLI's modelProvider (settings.json) is " + orDefault(p) + ", not gemini, so it asks Google directly"
			}
			// a distro's function names the gateway's address: one WSL
			// changed on a restart (NAT) no longer reaches it
			if distro {
				for _, p := range shellIn() {
					if b, err := os.ReadFile(p); err == nil && !strings.Contains(string(b), "GOOGLE_GEMINI_BASE_URL='"+gw()+"'") {
						return "Antigravity CLI's function agy in " + at.native(p) + " names magpie at an address it no longer has"
					}
				}
			}
			return ""
		},
		Fields: []Field{{
			Key: "model", Label: "model",
			Get: get,
			Set: func(v string) error {
				cur := get()
				if ref, ok := strings.CutPrefix(v, magpieID+"/"); ok && isMagpie(ref) {
					if !usesMagpie(cur) {
						stash(map[string]string{stashModel: cur, stashProvider: provider()})
					}
					if err := setModels(true); err != nil {
						return err
					}
					if err := edit.SetJSON(path, edit.KV{Path: "modelProvider", Value: "gemini"}, edit.KV{Path: "model", Value: v}); err != nil {
						return err
					}
					shell(true)
					return nil
				}
				if usesMagpie(cur) {
					// back to what the user had
					if v == "" {
						v = unstash(stashModel)
					} else {
						forget(stashModel)
					}
					if err := put("modelProvider", unstash(stashProvider)); err != nil {
						return err
					}
				}
				if err := put("model", v); err != nil {
					return err
				}
				if usesMagpie(cur) {
					shell(false)
				}
				return setModels(false)
			},
			Options: func(cur map[string]string) []Option {
				custom := agyCustom(path)
				// its own say they go to their provider, not through magpie,
				// as every agent's own do (EZN7L2C3, #834)
				var own []Option
				for k := range custom {
					if !strings.HasPrefix(k, magpieID+"/") {
						to := "Google"
						if p := gjson.GetBytes(custom[k], "apiProvider").String(); strings.Contains(p, "ANTHROPIC") {
							to = "Anthropic"
						} else if strings.Contains(p, "OPENAI") {
							to = "OpenAI"
						}
						own = append(own, Option{Value: k, Icon: modelIcon("", gjson.GetBytes(custom[k], "modelName").String()), Direct: to})
					}
				}
				sort.Slice(own, func(i, j int) bool { return own[i].Value < own[j].Value })
				if c := cur["model"]; c != "" && !usesMagpie(c) && custom[c] == nil {
					own = append([]Option{{Value: c, Icon: modelIcon("", c), Direct: "Google"}}, own...)
				}
				return withVia(group("Antigravity CLI", own), "agy", magpieID+"/")
			},
		}, agyEffortField(func() bool { return !at.cold && usesMagpie(get()) })},
	}
}

// agyEffortField is how hard agy's models reason, picked in its row. agy
// 1.5 has --effort for one session only, which it allows for a model
// whose name it knows and turns away for magpie's ("not adjustable" in
// /effort, xyenon on Discord), and settings.json has no key for it
// (--settings: unknown setting "effort"). Its request asks
// thinkingBudget -1, the model's own choice. The gateway asks its
// requests for the level picked here (provider.AgentEffort), as the
// model takes it (withGeminiEffort); with none they go as agy asks.
// Shown only while agy is on magpie's models, and never read at a
// stopped distro (#3896).
func agyEffortField(on func() bool) Field {
	return Field{
		Key: "effort", Label: "effort",
		Get: func() string { return provider.AgentEffort("agy") },
		Set: func(v string) error { return provider.SetAgentEffort("agy", v) },
		Options: func(map[string]string) []Option {
			if !on() {
				return nil
			}
			return pickedEfforts("agy")
		},
	}
}

// what agy says after a change (notice.go)
var (
	noticeAgy      = newNotice("agy takes magpie's gateway and model only from how it is started — start it with {command}")
	noticeAgyTyped = newNotice("Type agy in a new terminal: magpie added it to {profiles}, to start agy on magpie's gateway and model (in a terminal open now: {command}).")
)
