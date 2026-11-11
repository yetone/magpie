package library

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/jsonc"

	"github.com/yetone/magpie/internal/edit"
)

// An agent whose instructions are a string inside a JSON settings file
// (Target.Field: WorkBuddy's personalization.customPrompt in
// app/app-config.json) is given them there. Everything else in the file is
// the app's and stays as it is, key order and indentation included; what
// is in the field is the user's own text and magpie's part around it, as
// in an instructions file.

// instrRead is what the agent reads at t.Instructions: the file, or for a
// Field target the text at its key path. A file that isn't JSON, or a
// field that holds something other than text, is an error: it is unknown
// what the user has there, and nothing is written over it.
func instrRead(t *Target) ([]byte, error) {
	raw, err := edit.Read(t.Instructions)
	if err != nil || t.Field == "" {
		return raw, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	stripped := jsonc.ToJSONInPlace(bytes.Clone(raw))
	if !json.Valid(stripped) {
		return nil, fmt.Errorf("%s is not JSON magpie can read", t.Instructions)
	}
	r := gjson.GetBytes(stripped, t.Field)
	switch {
	case !r.Exists() || r.Type == gjson.Null:
		return nil, nil
	case r.Type != gjson.String:
		return nil, fmt.Errorf("%s in %s is not text: left as it is", t.Field, t.Instructions)
	}
	return []byte(r.String()), nil
}

// instrText is instrRead as the page reads it: trimmed, "" when unread.
func instrText(t *Target) string {
	b, _ := instrRead(t)
	return strings.TrimSpace(strings.ReplaceAll(string(b), "\r\n", "\n"))
}

// instrSave writes next as what the agent reads at t.Instructions; "" takes
// it out: the file, or for a Field target the field alone (and the object
// holding it, once that has nothing else, so a file the app never wrote
// into is left as it was).
func instrSave(t *Target, next string) error {
	if t.Field == "" {
		if next == "" {
			return edit.Remove(t.Instructions)
		}
		return edit.WriteAtomic(t.Instructions, []byte(next))
	}
	if next = strings.TrimSpace(next); next != "" {
		return edit.SetJSON(t.Instructions, edit.KV{Path: t.Field, Value: next})
	}
	if err := edit.DelJSON(t.Instructions, t.Field); err != nil {
		return err
	}
	raw, err := edit.Read(t.Instructions)
	if err != nil {
		return err
	}
	if i := strings.LastIndex(t.Field, "."); i > 0 {
		parent := gjson.GetBytes(jsonc.ToJSONInPlace(bytes.Clone(raw)), t.Field[:i])
		if parent.IsObject() && len(parent.Map()) == 0 {
			return edit.DelJSON(t.Instructions, t.Field[:i])
		}
	}
	return nil
}

// instrEmpty empties what the agent reads at t.Instructions without taking
// the file away, as an apply does (#5709): an instructions file stays,
// empty, and a field goes out of its JSON.
func instrEmpty(t *Target) error {
	if t.Field == "" {
		return edit.WriteAtomic(t.Instructions, nil)
	}
	return instrSave(t, "")
}
