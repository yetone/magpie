package gui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/yetone/magpie/internal/davsync"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/settings"
)

// archiveRoutes are the Gateway page's request archive: its switch, and a
// call read back from the bucket by its date and id (gateway/archive.go).
func archiveRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/settings/archive", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ On bool }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if _, ok := davsync.S3Bucket(); in.On && !ok {
			fail(rw, errors.New("The request archive goes to the S3 bucket sync keeps its backup in: set up Sync and backup in Settings with an s3:// address first"))
			return
		}
		s := settings.Load()
		s.RequestArchive = in.On
		if err := settings.Save(s); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, archiveState())
	})
	mux.HandleFunc("GET /api/archive", func(rw http.ResponseWriter, r *http.Request) {
		date, id := r.URL.Query().Get("date"), r.URL.Query().Get("id")
		name, ok := gateway.ArchiveName(date, id)
		if !ok {
			fail(rw, fmt.Errorf("%s/%s is not a request archive's date and id", date, id))
			return
		}
		b, ok := davsync.S3Bucket()
		if !ok {
			fail(rw, errors.New("The request archive goes to the S3 bucket sync keeps its backup in, and sync isn't set up with one"))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		data, err := b.Get(ctx, name)
		if errors.Is(err, davsync.ErrNoObject) {
			fail(rw, errors.New("Not in the archive yet: it is uploaded just after the call, or the upload failed (magpie's log says why)"))
			return
		}
		if err != nil {
			fail(rw, err)
			return
		}
		var a gateway.Archived
		if err := json.Unmarshal(data, &a); err != nil {
			fail(rw, fmt.Errorf("%s in the bucket is not a request archive: %v", name, err))
			return
		}
		writeJSON(rw, a)
	})
}

// archiveJSON is the request archive as the Gateway page shows it: on or
// off, the bucket it goes to (none when sync isn't to S3), and why the last
// upload failed, if it did.
type archiveJSON struct {
	On     bool   `json:"on"`
	Bucket string `json:"bucket,omitempty"`
	Error  string `json:"error,omitempty"`
}

func archiveState() archiveJSON {
	a := archiveJSON{On: settings.Load().RequestArchive}
	if b, ok := davsync.S3Bucket(); ok {
		a.Bucket = b.Where()
	}
	if a.On {
		a.Error, _ = gateway.ArchiveError()
	}
	return a
}
