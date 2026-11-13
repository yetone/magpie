package provider

// Claude Code 2.1.323 and later keep an account's models in a catalog of
// their own beside .claude.json: <config dir>/cache/model-catalog/
// <organizationUuid>-<first 12 hex of sha256(accountUuid)>-cc.json, a
// version-2 file whose catalog.config.models are what its /model picker
// offers that account (#6418, pwn2ooown). The name says whose it is, so
// ~/.claude's folder keeps the catalogs of every account that was signed in
// there, and only the one of the account a .claude.json names is read with
// it. The file's shape and name are read from Claude Code's own on a Mac
// (11-14): models with id, name and runtime (max_input_tokens,
// max_output_tokens, effort_levels, capabilities).

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/filememo"
)

// claudeCatalog is what one model-catalog file says of an account's models.
type claudeCatalog struct {
	// at is when Claude Code fetched it (fetchedAt), in ms
	at     float64
	models []claudeExtra
}

func parseClaudeCatalog(b []byte) (claudeCatalog, error) {
	var raw struct {
		Version   int     `json:"version"`
		FetchedAt float64 `json:"fetchedAt"`
		Catalog   *struct {
			Surface string `json:"surface"`
			Config  *struct {
				Models []struct {
					ID      string `json:"id"`
					Name    string `json:"name"`
					Runtime struct {
						MaxInput     int      `json:"max_input_tokens"`
						MaxOutput    int      `json:"max_output_tokens"`
						Efforts      []string `json:"effort_levels"`
						Capabilities []string `json:"capabilities"`
					} `json:"runtime"`
				} `json:"models"`
			} `json:"config"`
		} `json:"catalog"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return claudeCatalog{}, err
	}
	// another version's or surface's file is a shape not known here: unknown,
	// never "no models"
	if raw.Version != 2 || raw.Catalog == nil || raw.Catalog.Surface != "cc" || raw.Catalog.Config == nil {
		return claudeCatalog{}, errors.New("not a version-2 Claude Code model catalog")
	}
	out := claudeCatalog{at: raw.FetchedAt}
	seen := map[string]bool{}
	for _, m := range raw.Catalog.Config.Models {
		id := claudeAPIID(m.ID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		r := m.Runtime
		out.models = append(out.models, claudeExtra{
			id: id, name: strings.TrimSpace(m.Name),
			context: max(r.MaxInput, 0), output: max(r.MaxOutput, 0),
			efforts: slices.Clip(r.Efforts), images: slices.Contains(r.Capabilities, "mm_images"),
		})
	}
	return out, nil
}

// claudeCatalogName is the file Claude Code keeps the account's catalog
// in; "" when the account isn't known.
func claudeCatalogName(org, account string) string {
	if org == "" || account == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(account))
	return org + "-" + hex.EncodeToString(sum[:])[:12] + "-cc.json"
}

// claudeCatalogDirs are the model-catalog folders an account's catalog may
// be in, for the .claude.json at profile: its own config directory's, and
// that of Claude Code as it runs here, which keeps every account's that
// was signed in to it.
func claudeCatalogDirs(profile string) []string {
	own := appdir.ClaudeDir()
	if profile != claudeProfilePath() {
		own = filepath.Dir(profile)
	}
	dirs := []string{filepath.Join(own, "cache", "model-catalog")}
	if here := filepath.Join(appdir.ClaudeDir(), "cache", "model-catalog"); here != dirs[0] {
		dirs = append(dirs, here)
	}
	return dirs
}

// claudeCatalogOf is the catalog Claude Code keeps for the account of the
// .claude.json at profile: of its copies, the one fetched last.
func claudeCatalogOf(profile, org, account string) (claudeCatalog, bool) {
	name := claudeCatalogName(org, account)
	if name == "" {
		return claudeCatalog{}, false
	}
	var best claudeCatalog
	found := false
	for _, d := range claudeCatalogDirs(profile) {
		if c, ok := readKeepingGood("claude catalog", filepath.Join(d, name), parseClaudeCatalog); ok && (!found || c.at > best.at) {
			best, found = c, true
		}
	}
	return best, found
}

// goodReads are the last parse of each file read through readKeepingGood.
var goodReads sync.Map // goodKey → any

type goodKey struct{ kind, path string }

// readKeepingGood is filememo.Read of path, with a file that doesn't
// parse now (Claude Code halfway through writing it) taken as what it
// said last, never as one with nothing in it; a file gone is gone.
func readKeepingGood[T any](kind, path string, parse func([]byte) (T, error)) (T, bool) {
	k := goodKey{kind, path}
	v, err := filememo.Read(kind, path, parse)
	if err == nil {
		goodReads.Store(k, v)
		return v, true
	}
	if errors.Is(err, fs.ErrNotExist) {
		goodReads.Delete(k)
		var zero T
		return zero, false
	}
	if g, ok := goodReads.Load(k); ok {
		return g.(T), true
	}
	var zero T
	return zero, false
}

// withCatalog is m with the models its account's catalog offers that its
// .claude.json doesn't name. m is filememo's shared value: it is copied,
// never changed.
func (m claudeCodeModels) withCatalog(c claudeCatalog) claudeCodeModels {
	m.extra = slices.Clone(m.extra)
	for _, x := range c.models {
		if i := slices.IndexFunc(m.extra, func(e claudeExtra) bool { return e.id == x.id }); i >= 0 {
			if m.extra[i].name == "" {
				m.extra[i].name = x.name
			}
			e := &m.extra[i]
			e.context, e.output, e.efforts, e.images = x.context, x.output, x.efforts, x.images
			continue
		}
		m.extra = append(m.extra, x)
	}
	m.at = max(m.at, c.at)
	return m
}
