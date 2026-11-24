package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/yetone/magpie/internal/artifact"
)

// capsLine says what a version's page may do beyond static HTML, or ""
// when it declares nothing.
func capsLine(v *artifact.Version) string {
	if v == nil || len(v.Capabilities) == 0 {
		return ""
	}
	names := make([]string, 0, len(v.Capabilities))
	for name := range v.Capabilities {
		names = append(names, name)
	}
	sort.Strings(names)
	var unserved []string
	for _, name := range names {
		if !slices.Contains(artifact.ServedCaps, name) {
			unserved = append(unserved, name)
		}
	}
	l := "capabilities: " + strings.Join(names, ", ")
	if len(unserved) > 0 {
		l += " (" + strings.Join(unserved, ", ") + " resolve null here)"
	}
	return l
}

const artifactDataActions = "get, list, query, set, update, str_replace, delete or batch"

// artifactData is artifact_data: the agent reaching the artifact's store,
// where it reads and writes everything (Viewer.Agent).
func artifactData(a artifactArgs) (string, error) {
	art, _, err := artifactNamed(artifactArgs{ID: a.ID})
	if err != nil {
		return "", err
	}
	db, err := artifact.OpenDB(art.ID, 0, artifact.Viewer{Agent: true})
	if err != nil {
		return "", err
	}
	show := func(v any) (string, error) {
		b, err := json.MarshalIndent(v, "", "  ")
		return string(b), err
	}
	// a write to a page that doesn't read it yet says so
	note := ""
	if !art.Latest().Capabilities.Has("db") {
		note = "\n(The page doesn't declare db, so it doesn't read this yet: publish it with capabilities {\"db\": {}}.)"
	}
	wrote := func(what string) (string, error) { return what + note, nil }
	switch a.Action {
	case "get":
		d, err := db.Get(a.Path)
		if err != nil {
			return "", err
		}
		if d == nil {
			return fmt.Sprintf("There is no document at %s.", a.Path) + note, nil
		}
		return show(d)
	case "list":
		if strings.Trim(a.Path, "/") == "" {
			cs, err := artifact.Collections(art.ID)
			if err != nil {
				return "", err
			}
			if len(cs) == 0 {
				return "The data is empty." + note, nil
			}
			docs, size, _ := artifact.Stats(art.ID)
			out, err := show(map[string]any{"collections": cs, "documents": docs, "bytes": size})
			return out + note, err
		}
		docs, err := db.Run(artifact.Query{Collection: a.Path, Limit: int(a.Limit)})
		if err != nil {
			return "", err
		}
		return show(map[string]any{"docs": docs})
	case "query":
		docs, err := db.Run(artifact.Query{Collection: a.Path, Where: a.Where, OrderBy: a.OrderBy, Desc: a.Desc, Limit: int(a.Limit)})
		if err != nil {
			return "", err
		}
		return show(map[string]any{"docs": docs})
	case "set", "update":
		m, err := docBody(a.Data)
		if err != nil {
			return "", err
		}
		if a.Action == "set" {
			err = db.Set(a.Path, m)
		} else {
			err = db.Update(a.Path, m)
		}
		if err != nil {
			return "", err
		}
		return wrote(fmt.Sprintf("Wrote %s; open views of the page show it now.", a.Path))
	case "str_replace":
		if a.OldStr == nil || a.NewStr == nil {
			return "", errors.New("give old_str and new_str")
		}
		d, err := db.Get(a.Path)
		if err != nil {
			return "", err
		}
		if d == nil {
			return "", fmt.Errorf("there is no document at %s", a.Path)
		}
		was, err := json.MarshalIndent(d.Data, "", "  ")
		if err != nil {
			return "", err
		}
		switch n := strings.Count(string(was), *a.OldStr); {
		case *a.OldStr == "" || n == 0:
			return "", fmt.Errorf("old_str isn't in %s's JSON (get shows it)", a.Path)
		case n > 1:
			return "", fmt.Errorf("old_str is in %s's JSON %d times: give more of it, so it is there once", a.Path, n)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(strings.Replace(string(was), *a.OldStr, *a.NewStr, 1)), &m); err != nil || m == nil {
			return "", fmt.Errorf("with that replaced, %s's body isn't a JSON object", a.Path)
		}
		if err := db.Set(a.Path, m); err != nil {
			return "", err
		}
		return wrote(fmt.Sprintf("Wrote %s; open views of the page show it now.", a.Path))
	case "delete":
		if err := db.Delete(a.Path); err != nil {
			return "", err
		}
		return wrote(fmt.Sprintf("Deleted %s.", a.Path))
	case "batch":
		if len(a.Ops) == 0 {
			return "", errors.New("give ops: the writes, each {op, path, data}")
		}
		if err := db.Batch(a.Ops); err != nil {
			return "", err
		}
		return wrote(fmt.Sprintf("Made %d writes; open views of the page show them now.", len(a.Ops)))
	case "":
		return "", errors.New("give action: " + artifactDataActions)
	}
	return "", fmt.Errorf("%q isn't an action: %s", a.Action, artifactDataActions)
}

// docBody is a set's or update's data: a JSON object.
func docBody(raw json.RawMessage) (map[string]any, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil, errors.New("give data: the document's body, a JSON object")
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return nil, errors.New("data is a JSON object: a document's body")
	}
	return m, nil
}
