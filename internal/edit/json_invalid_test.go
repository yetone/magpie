package edit

import (
	"bytes"
	"os"
	"testing"
)

func TestJSONEditsPreserveInvalidFiles(t *testing.T) {
	for _, input := range []string{
		`{"model":"old", "keep":`,
		`{"model":"old" "keep":true}`,
		`{"model":"old"} {"keep":true}`,
	} {
		for name, edit := range map[string]func(string) error{
			"set":           func(p string) error { return SetJSON(p, KV{"model", "new"}) },
			"delete":        func(p string) error { return DelJSON(p, "model") },
			"delete absent": func(p string) error { return DelJSON(p, "missing") },
		} {
			t.Run(name+"/"+input, func(t *testing.T) {
				p := tmpFile(t, "settings.json", input)
				if err := edit(p); err == nil {
					t.Error("editing invalid JSON succeeded")
				}
				if got, err := os.ReadFile(p); err != nil || string(got) != input {
					t.Errorf("invalid config was changed: %q, %v", got, err)
				}
			})
		}
		for name, changes := range map[string]struct {
			set []KV
			del []string
		}{
			"set":    {set: []KV{{"model", "new"}}},
			"delete": {del: []string{"model"}},
		} {
			t.Run("preview/"+name+"/"+input, func(t *testing.T) {
				raw := []byte(input)
				if _, err := PatchJSON(raw, changes.set, changes.del); err == nil {
					t.Error("previewing an edit of invalid JSON succeeded")
				}
				if !bytes.Equal(raw, []byte(input)) {
					t.Fatal("preview changed input")
				}
			})
		}
	}
}

func TestJSONItemEditsPreserveInvalidFiles(t *testing.T) {
	where := map[string]string{"name": "magpie"}
	for _, input := range []string{
		`[{"name":"magpie"}, {"name":]`,
		`[{"name":"magpie"} {"name":"keep"}]`,
		`[{"name":"magpie"}] [{"name":"keep"}]`,
	} {
		for name, edit := range map[string]func(string) error{
			"set": func(p string) error {
				return SetJSONItem(p, where, map[string]string{"name": "magpie", "model": "new"})
			},
			"delete": func(p string) error { return DelJSONItem(p, where) },
		} {
			t.Run(name+"/"+input, func(t *testing.T) {
				p := tmpFile(t, "chatLanguageModels.json", input)
				if err := edit(p); err == nil {
					t.Error("editing invalid JSON array succeeded")
				}
				if got, err := os.ReadFile(p); err != nil || string(got) != input {
					t.Errorf("invalid config was changed: %q, %v", got, err)
				}
			})
		}
	}
}
