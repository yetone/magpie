package agent

// CodeBuddy Code (Tencent's CLI, npm @tencent-ai/codebuddy-code: codebuddy,
// cbc) is one package for the international edition (codebuddy.ai) and the
// China one (codebuddy.cn, CODEBUDDY_INTERNET_ENVIRONMENT=internal). Both
// take models of one's own from ~/.codebuddy/models.json ($CODEBUDDY_CONFIG_DIR
// if set), in WorkBuddy's format (workbuddy.go):
//
//	{"models":[{"id":"deepseek/pro","name":…,"vendor":"magpie",
//	  "apiKey":"magpie-codebuddy","url":"http://127.0.0.1:3425/v1/chat/completions",
//	  "maxInputTokens":…,"maxOutputTokens":…,"supportsToolCall":true,…}],
//	 "availableModels":[…]}
//
// and pick up a change to it without a restart. Such a model needs no
// CodeBuddy sign-in: the CLI asks url for it under Bearer apiKey. The model
// a session starts on is settings.json's "model" (an id from that list, or
// one of CodeBuddy's own), read when a session starts. Its requests say
// "CLI/<ver> CodeBuddy/<ver>", so they are told apart by their key.

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/edit"
)

func codebuddy(home string) *Agent { return codebuddyIn(here(home)) }

// codebuddyIn is CodeBuddy Code at a place: this machine's home, or a WSL
// distro's (see wsl.go), where CODEBUDDY_CONFIG_DIR isn't read and its
// models name the gateway as the distro reaches it, with the key it takes
// from there.
func codebuddyIn(at place) *Agent {
	dir := at.getenv("CODEBUDDY_CONFIG_DIR")
	if dir == "" {
		dir = filepath.Join(at.home, ".codebuddy")
	}
	path := filepath.Join(dir, "models.json")
	write := func(on bool) error { return buddyWriteAt(path, "codebuddy", on, at) }
	settings := filepath.Join(dir, "settings.json")
	model := func() string { v, _ := edit.GetJSON(settings, "model"); return v }
	// ours is whether id is one of magpie's models in models.json
	ours := func(id string) bool {
		d, _ := workbuddyRead(path)
		return slices.ContainsFunc(d.models, func(raw json.RawMessage) bool { e, ok := workbuddyMine(raw); return ok && e.ID == id })
	}
	return &Agent{
		ID: "codebuddy", Name: "CodeBuddy Code", Icon: "codebuddy-color", Aliases: []string{"codebuddy-code", "cbc"},
		UA: []string{"codebuddy"}, Spelled: prefixed,
		Bin: "codebuddy", Dir: dir, Path: path,
		Sync: func() error {
			if !workbuddyWired(path) {
				return nil
			}
			return write(true)
		},
		Notice: func() string {
			if Running(`(^|/)(codebuddy|cbc|codebuddy-code)( |$)`) {
				return "CodeBuddy Code reads its model when a session starts — restart open codebuddy sessions, or run /clear in them, to use this."
			}
			return ""
		},
		Fields: []Field{{
			Key: "model", Label: "model",
			Get: func() string {
				m := model()
				if m != "" && ours(m) {
					return magpieID + "/" + m
				}
				return m
			},
			Set: func(v string) error {
				if ref, ok := strings.CutPrefix(v, magpieID+"/"); ok && isMagpie(ref) {
					if err := write(true); err != nil {
						return err
					}
					return edit.SetJSON(settings, edit.KV{Path: "model", Value: ref})
				}
				// CodeBuddy's own: magpie's models come out of its list
				if err := write(false); err != nil {
					return err
				}
				if v == "" {
					return edit.DelJSON(settings, "model")
				}
				return edit.SetJSON(settings, edit.KV{Path: "model", Value: v})
			},
			Options: func(cur map[string]string) []Option {
				var out []Option
				if v := cur["model"]; v != "" && !usesMagpie(v) {
					out = append(out, Option{Value: v, Icon: modelIcon("", v)})
				}
				return append(out, viaMagpie("codebuddy", magpieID+"/")...)
			},
		}},
	}
}
