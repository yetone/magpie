package catalog

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// The catalog magpie ships with (creeksuen on X). Where models.dev can't
// be reached (a network in China, a proxy not set yet), a first run had
// no catalog at all: no prices, context windows, reasoning levels or
// names, and a plugin's provider listed no models. The binary carries a
// copy of models.dev's api.json from when it was built. It answers when
// no catalog file can be read, and Seed writes it where Sync keeps the
// catalog, dated when it was taken, so the plugin host reads it too and
// Stale still has the real one fetched as soon as models.dev answers.
//
// To refresh it:
//
//go:generate sh -c "curl -fsS https://models.dev/api.json | gzip -9n > bundled/api.json.gz"
//
// and set bundledAt to the time it was fetched.

//go:embed bundled/api.json.gz
var bundledGz []byte

// bundledAt is when bundled/api.json.gz was fetched from models.dev.
var bundledAt = time.Date(2026, 11, 10, 10, 11, 0, 0, time.UTC)

// bundledOn is whether the shipped copy answers. A test binary has it off,
// so a test's catalog is the one it wrote, or none; the tests of the copy
// turn it on.
var bundledOn = !testing.Testing()

var bundled = sync.OnceValue(func() []byte {
	r, err := gzip.NewReader(bytes.NewReader(bundledGz))
	if err != nil {
		return nil
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return nil
	}
	return b
})

// bundledCatalog is the shipped copy, or nil when it is off.
func bundledCatalog() []byte {
	if !bundledOn {
		return nil
	}
	return bundled()
}

// Seed writes the shipped copy where Sync keeps the catalog when no
// catalog file is there, or the one there is older than the copy, dated
// when the copy was taken: Stale says so once it is a day old, and the
// fetch that follows replaces it. A sync in progress is left to finish,
// and a copy that can't be written is still read from the binary (load).
// It is cheap when there is nothing to do, a stat or two.
func Seed() {
	b := bundledCatalog()
	if b == nil || !seedWanted() {
		return
	}
	select {
	case syncCh <- struct{}{}:
	default:
		return // a sync is writing the catalog now
	}
	defer func() { <-syncCh }()
	if !seedWanted() {
		return
	}
	had := Source() != ""
	if err := writeWhole(CachePath(), b, bundledAt); err != nil {
		log.Println("catalog: shipped copy:", err)
		return
	}
	if had {
		// an older catalog was read before: the copy answers now
		Reset()
		Touched()
	}
}

// seedWanted: no catalog file, or the one read is older than the copy.
func seedWanted() bool {
	src := Source()
	if src == "" {
		return true
	}
	st, err := os.Stat(src)
	return err == nil && st.ModTime().Before(bundledAt)
}

// writeWhole writes b to p through a file beside it renamed over p, so a
// reader (load, the plugin host's mdev) never reads it half written, and a
// failed write keeps the file as it was. A non-zero mtime dates it. It
// isn't flushed to disk first (a flush on Windows can take a second): a
// copy a power cut leaves empty is passed over by load for the next.
func writeWhole(p string, b []byte, mtime time.Time) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), "."+filepath.Base(p)+"-*")
	if err != nil {
		return err
	}
	fail := func(err error) error {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		return fail(err)
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return fail(err)
	}
	if !mtime.IsZero() {
		if err := os.Chtimes(tmp.Name(), mtime, mtime); err != nil {
			return fail(err)
		}
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		return fail(err)
	}
	return nil
}

// parses reports whether b reads as a provider catalog.
func parses(b []byte) bool {
	var m map[string]json.RawMessage
	return json.Unmarshal(b, &m) == nil && len(m) > 0
}
