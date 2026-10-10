package agent

// Bub keeps its settings in ~/.bub/config.yml (BUB_HOME moves its data, not
// this file): the model under model, spelled "<provider>:<model>", the
// provider one of any-llm's or a name under providers, each an endpoint that
// speaks one of any-llm's APIs (type) with its own api_base and api_key.
// magpie adds itself there as the provider "magpie" (the gateway, spoken to
// as OpenAI chat completions) and sets model to "magpie:<provider>/<model>";
// a model through magpie is "magpie/<provider>/<model>" here. The model the
// user had is stashed and put back when magpie steps out; fallback_models
// and the user's own providers are left alone. A model of Bub's own picked
// after that, in Bub or on the Agents page, leaves magpie's provider in
// providers: Bub stays joined — ,model can name one of magpie's models
// again any time — and only Disconnect takes the provider out.

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/proc"
)

func bub(home string) *Agent {
	at := here(home)
	dir := filepath.Join(home, ".bub")
	path := filepath.Join(dir, "config.yml")
	key := "bub:" + path + ":"
	getKey := func(k string) string { v, _ := edit.GetYAML(path, k); return v }
	gwKey := func() string { return agentKeyAt("bub", at.gw()) }
	onMagpie := func() bool { return strings.HasPrefix(getKey("model"), magpieID+":") }
	// joined: magpie's provider is in providers, though the model is one
	// of Bub's own
	joined := func() bool { _, ok := edit.GetYAML(path, "providers."+magpieID+".api_base"); return ok }
	// made notes that magpie is about to make the mapping at k, so drop
	// can take it out again once it is empty; one the user had stays
	made := func(k string) map[string]string {
		if _, ok := edit.GetYAMLText(path, k); ok {
			return nil
		}
		return map[string]string{key + k: "made"}
	}
	drop := func(k string) error {
		if unstash(key+k) != "made" {
			return nil
		}
		if v, ok := edit.GetYAMLText(path, k); ok && v == "{}" {
			return edit.DelYAML(path, k)
		}
		return nil
	}
	// unwire takes magpie's provider out
	unwire := func() error {
		if !joined() {
			return nil
		}
		if err := edit.DelYAML(path, "providers."+magpieID); err != nil {
			return err
		}
		return drop("providers")
	}
	// restore puts back the model the user had before magpie, magpie's
	// provider left beside it
	restore := func() error {
		if v := unstash(key + "model"); v != "" {
			return edit.SetYAML(path, edit.KV{Path: "model", Value: v})
		}
		return edit.DelYAML(path, "model")
	}
	return &Agent{
		ID: "bub", Name: "Bub", Icon: "bub", Spelled: prefixed,
		Bin: "bub", Dir: dir, Path: path,
		Sync: func() error {
			return syncYAML(path, "providers."+magpieID, func() any { return bubProvider{Type: "openai", APIBase: at.v1(), APIKey: gwKey()} })
		},
		Joined: joined,
		// Disconnect takes magpie's provider out; the model, when it is
		// magpie's, goes back by the field
		Unwire: unwire,
		Notice: func() string {
			if Running(`(^|/)bub (gateway|chat)( |$)`) {
				return "Bub reads its settings at start-up — restart bub gateway and open bub chat sessions to use this."
			}
			return ""
		},
		Check: func() string {
			if !onMagpie() {
				return ""
			}
			if errors.Is(bubReadsProviders(), errBubOld) {
				return errBubOld.Error()
			}
			return wiringOff("Bub", path, func(k string) (string, bool) { return edit.GetYAML(path, "providers."+magpieID+"."+k) },
				"api_base", at.v1(), "api_key", gwKey(), "type", "openai")
		},
		Fields: []Field{{
			Key: "model", Label: "model",
			Get: func() string {
				v := getKey("model")
				if ref, ok := strings.CutPrefix(v, magpieID+":"); ok {
					return magpieID + "/" + ref
				}
				return v
			},
			Set: func(v string) error {
				if v == "" {
					if onMagpie() {
						return restore()
					}
					return edit.DelYAML(path, "model")
				}
				if ref, ok := strings.CutPrefix(v, magpieID+"/"); ok && isMagpie(ref) {
					if err := bubReadsProviders(); errors.Is(err, errBubOld) {
						return err
					}
					if !onMagpie() {
						stash(map[string]string{key + "model": getKey("model")})
						stash(made("providers"))
					}
					return edit.SetYAML(path,
						edit.KV{Path: "providers." + magpieID, Value: bubProvider{Type: "openai", APIBase: at.v1(), APIKey: gwKey()}},
						edit.KV{Path: "model", Value: magpieID + ":" + ref},
					)
				}
				// a model of Bub's own: magpie's provider stays for ,model
				// to name again; only Disconnect takes it out
				return edit.SetYAML(path, edit.KV{Path: "model", Value: v})
			},
			Options: func(cur map[string]string) []Option {
				return append(ownOptions("", cur["model"]), viaMagpie("bub", magpieID+"/")...)
			},
		}, {
			// completion_args.reasoning_effort, passed with every model call;
			// unset, Bub sends none and the model's own default holds
			Key: "effort", Label: "effort",
			Get: func() string { return getKey("completion_args.reasoning_effort") },
			Set: func(v string) error {
				if v == "" {
					if err := edit.DelYAML(path, "completion_args.reasoning_effort"); err != nil {
						return err
					}
					return drop("completion_args")
				}
				if getKey("completion_args.reasoning_effort") == "" {
					stash(made("completion_args"))
				}
				return edit.SetYAML(path, edit.KV{Path: "completion_args.reasoning_effort", Value: v})
			},
			Options: func(map[string]string) []Option {
				return static("none", "minimal", "low", "medium", "high", "xhigh")
			},
		}},
	}
}

// bubProvider is magpie's entry under Bub's providers. The key names Bub to
// the gateway: its requests carry the OpenAI SDK's User-Agent, not its own.
type bubProvider struct {
	Type    string `yaml:"type"`
	APIBase string `yaml:"api_base"`
	APIKey  string `yaml:"api_key"`
}

var errBubOld = errors.New("this Bub doesn't read providers, which magpie needs to be one of its model's providers — update Bub (bubbuild/bub#343)")

// bubReadsProviders asks the Python that runs the bub on PATH whether its
// settings have providers: an older Bub takes "magpie:" for an any-llm
// provider it doesn't know and fails every turn. errBubOld when they don't;
// another error when it can't be told (no bub on PATH, its Python not
// found or not answering), which leaves the choice to the user. The answer
// is kept while bub is the same file.
func bubReadsProviders() error {
	bin, err := exec.LookPath("bub")
	if err != nil {
		return err
	}
	st, err := os.Stat(bin)
	if err != nil {
		return err
	}
	k := goProgramKey{bin, st.Size(), st.ModTime()}
	bubAsked.Lock()
	defer bubAsked.Unlock()
	if v, ok := bubAsked.m[k]; ok {
		return v
	}
	err = askBub(bin)
	if bubAsked.m == nil {
		bubAsked.m = map[goProgramKey]error{}
	}
	bubAsked.m[k] = err
	return err
}

var bubAsked struct {
	sync.Mutex
	m map[goProgramKey]error
}

func askBub(bin string) error {
	py := bubPython(bin)
	if py == "" {
		return errors.New("bub's Python not found")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := proc.CommandContext(ctx, py, "-c",
		"from bub.builtin.settings import AgentSettings as s; print('providers' in s.model_fields)").Output()
	if err != nil {
		return err
	}
	switch strings.TrimSpace(string(out)) {
	case "True":
		return nil
	case "False":
		return errBubOld
	}
	return errors.New("bub's Python said " + strings.TrimSpace(string(out)))
}

var bubShebang = regexp.MustCompile(`["']?(/[^"'\s]*python[0-9.]*)["']?`)

// bubPython is the interpreter a bub launcher runs: python.exe beside a
// Windows one, else the one its first lines name — the #! line, or the
// exec line pip writes under #!/bin/sh when the path is too long for #!.
func bubPython(bin string) string {
	if runtime.GOOS == "windows" {
		py := filepath.Join(filepath.Dir(bin), "python.exe")
		if _, err := os.Stat(py); err == nil {
			return py
		}
		return ""
	}
	f, err := os.Open(bin)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for i := 0; i < 3 && sc.Scan(); i++ {
		if m := bubShebang.FindStringSubmatch(sc.Text()); m != nil {
			return m[1]
		}
	}
	return ""
}
