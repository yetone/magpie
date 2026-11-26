package artifact

import (
	"encoding/json"
	"strings"
	"time"
)

// A live share (docs/subsystems/artifacts.md, "Live sharing") keeps the
// page's data on usemagpie.ai while it is shared: the store goes there
// whole when the page is first shared (DumpDocs, the share's data part),
// its viewers write it there, and it comes back whole when the page stops
// being shared (RestoreDocs), so what they wrote stays the page's.

// SharedDoc is one document as a share carries it: site/livedb.js's dump
// and seed.
type SharedDoc struct {
	Path    string         `json:"path"`
	Data    map[string]any `json:"data"`
	Version int64          `json:"version"`
	Updated time.Time      `json:"updated"`
}

// DumpDocs is every document in artifact id's store, by path.
func DumpDocs(id string) ([]SharedDoc, error) {
	if _, err := readMeta(id); err != nil {
		return nil, err
	}
	h, err := handle(id, false)
	if err != nil {
		return nil, unavailable(err)
	}
	out := []SharedDoc{}
	if h == nil {
		return out, nil
	}
	rows, err := h.Query(`SELECT path, body, version, updated FROM docs ORDER BY path`)
	if err != nil {
		return nil, unavailable(err)
	}
	defer rows.Close()
	for rows.Next() {
		var path, body string
		var version, updated int64
		if err := rows.Scan(&path, &body, &version, &updated); err != nil {
			return nil, unavailable(err)
		}
		d, err := readDoc(path, body, version, updated)
		if err != nil {
			return nil, unavailable(err)
		}
		out = append(out, SharedDoc{Path: d.Path, Data: d.Data, Version: d.Version, Updated: d.Updated})
	}
	return out, rows.Err()
}

// RestoreDocs makes artifact id's store hold docs and nothing else, each
// at its own version and time, all or none. A document that isn't one
// the store takes (its path, its body, the store's size) refuses the
// whole of it, and the store stays as it was.
func RestoreDocs(id string, docs []SharedDoc) error {
	type row struct {
		path, coll string
		body       []byte
		version    int64
		updated    int64
	}
	var ready []row
	var size int64
	seen := map[string]bool{}
	for _, d := range docs {
		if _, err := CheckPath(d.Path, true); err != nil {
			return err
		}
		if seen[d.Path] {
			return invalid("%s comes twice", d.Path)
		}
		seen[d.Path] = true
		body, err := checkBody(d.Data)
		if err != nil {
			return invalid("%s: %v", d.Path, err)
		}
		size += int64(len(body))
		r := row{path: d.Path, coll: d.Path[:strings.LastIndex(d.Path, "/")], body: body, version: max(d.Version, 1), updated: d.Updated.UnixMilli()}
		if d.Updated.IsZero() {
			r.updated = time.Now().UnixMilli()
		}
		ready = append(ready, r)
	}
	if int64(len(ready)) > maxDocs {
		return quota("the artifact's database holds %d documents, the most it may", maxDocs)
	}
	if size > maxDataBytes {
		return quota("the artifact's database would hold over %d MiB, the most it may", maxDataBytes>>20)
	}
	if _, err := readMeta(id); err != nil {
		return err
	}
	h, err := handle(id, true)
	if err != nil {
		return unavailable(err)
	}
	tx, err := h.Begin()
	if err != nil {
		return unavailable(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM docs`); err != nil {
		return unavailable(err)
	}
	for _, r := range ready {
		if _, err := tx.Exec(`INSERT INTO docs(path, coll, body, version, updated) VALUES(?, ?, ?, ?, ?)`, r.path, r.coll, string(r.body), r.version, r.updated); err != nil {
			return unavailable(err)
		}
	}
	if _, err := tx.Exec(`UPDATE totals SET docs = ?, bytes = ?`, len(ready), size); err != nil {
		return unavailable(err)
	}
	if err := tx.Commit(); err != nil {
		return unavailable(err)
	}
	// every path: the page's listeners read again
	tell(Change{Artifact: id})
	return nil
}

// sharedDocsJSON is the store as the share's data part.
func sharedDocsJSON(id string) ([]byte, error) {
	docs, err := DumpDocs(id)
	if err != nil {
		return nil, err
	}
	return json.Marshal(docs)
}
