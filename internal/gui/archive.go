package gui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/yetone/magpie/internal/davsync"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/settings"
)

// archiveStore is the bucket a call is read back from: sync's S3 one, or
// a test's.
type archiveStore interface {
	Open(ctx context.Context, name string) (io.ReadCloser, int64, error)
}

var archiveBucket = func() (archiveStore, bool) {
	b, ok := davsync.S3Bucket()
	return b, ok
}

// archivePreviewBody is the most of a body the page is sent to show; a
// longer one is left out, for the file to be downloaded (#447). Its size
// is told here, so a large body never reaches the page.
const archivePreviewBody = 256 << 10

// archiveReadMost is the largest archive read here to be shown: two bodies
// of the archive's 32 MiB and their escapes. A larger one is only
// downloaded, never read into memory.
const archiveReadMost = 80 << 20

// archivePreview is an archived call as the page shows it: a body longer
// than archivePreviewBody left out (Omitted), or, for an archive over
// archiveReadMost, nothing but its size (Large).
type archivePreview struct {
	ID       string        `json:"id,omitempty"`
	Call     *gateway.Call `json:"call,omitempty"`
	Request  *previewPart  `json:"request,omitempty"`
	Response *previewPart  `json:"response,omitempty"`
	Bytes    int64         `json:"bytes"` // the archive file's, -1 when not known
	Large    bool          `json:"large,omitempty"`
}

type previewPart struct {
	gateway.ArchivePart
	Omitted bool `json:"omitted,omitempty"`
}

func preview(p gateway.ArchivePart) *previewPart {
	if p.Size == 0 {
		p.Size = int64(len(p.Body)) // archived before #447
	}
	out := &previewPart{ArchivePart: p}
	if len(p.Body) > archivePreviewBody {
		out.Body, out.Omitted = "", true
	}
	return out
}

// openArchive is the call named by r's ?date= and ?id=, as it is read from
// the bucket, its size, and "<date>-<id>"; the error is the page's to show.
func openArchive(r *http.Request) (io.ReadCloser, int64, string, error) {
	date, id := r.URL.Query().Get("date"), r.URL.Query().Get("id")
	name, ok := gateway.ArchiveName(date, id)
	if !ok {
		return nil, 0, "", fmt.Errorf("%s/%s is not a request archive's date and id", date, id)
	}
	b, ok := archiveBucket()
	if !ok {
		return nil, 0, "", errors.New("The request archive goes to the S3 bucket sync keeps its backup in, and sync isn't set up with one")
	}
	body, size, err := b.Open(r.Context(), name)
	if errors.Is(err, davsync.ErrNoObject) {
		return nil, 0, "", errors.New("Not in the archive yet: it is uploaded just after the call, or the upload failed (magpie's log says why)")
	}
	if err != nil {
		return nil, 0, "", err
	}
	return body, size, date + "-" + id, nil
}

// archiveRoutes are the request archive: its switch on the Gateway page, a
// call read back from the bucket by its date and id, for the Gateway and
// Usage pages, and its file, downloaded whole (gateway/archive.go).
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
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		body, size, name, err := openArchive(r.WithContext(ctx))
		if err != nil {
			fail(rw, err)
			return
		}
		defer body.Close()
		if size > archiveReadMost {
			writeJSON(rw, archivePreview{Bytes: size, Large: true})
			return
		}
		data, err := io.ReadAll(io.LimitReader(body, archiveReadMost+1))
		if err != nil {
			fail(rw, err)
			return
		}
		if len(data) > archiveReadMost {
			writeJSON(rw, archivePreview{Bytes: -1, Large: true})
			return
		}
		var a gateway.Archived
		if err := json.Unmarshal(data, &a); err != nil {
			fail(rw, fmt.Errorf("%s in the bucket is not a request archive: %v", name, err))
			return
		}
		writeJSON(rw, archivePreview{ID: a.ID, Call: &a.Call, Request: preview(a.Request), Response: preview(a.Response), Bytes: int64(len(data))})
	})
	// the archive's file, whole and as the bucket has it, to the browser
	// that asks: magpie web's
	mux.HandleFunc("GET /api/archive/file", func(rw http.ResponseWriter, r *http.Request) {
		body, size, name, err := openArchive(r)
		if err != nil {
			fail(rw, err)
			return
		}
		defer body.Close()
		rw.Header().Set("Content-Type", "application/json")
		rw.Header().Set("Content-Disposition", `attachment; filename="magpie-request-`+name+`.json"`)
		if size >= 0 {
			rw.Header().Set("Content-Length", fmt.Sprint(size))
		}
		io.Copy(rw, body)
	})
	// and, in the app, to Downloads
	mux.HandleFunc("POST /api/archive/export", func(rw http.ResponseWriter, r *http.Request) {
		body, _, name, err := openArchive(r)
		if err != nil {
			fail(rw, err)
			return
		}
		defer body.Close()
		path, err := saveDownload(downloads(), "magpie-request-"+name, ".json", body)
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, map[string]any{"path": tilde(path)})
	})
}

// saveDownload writes what r reads to dir/<stem><ext>, or <stem>-2<ext>…
// when that is there: whole, or not at all.
func saveDownload(dir, stem, ext string, r io.Reader) (string, error) {
	tmp, err := os.CreateTemp(dir, "."+stem+"-*.part")
	if err != nil {
		return "", err
	}
	_, err = io.Copy(tmp, r)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	name := filepath.Join(dir, stem+ext)
	for i := 2; ; i++ { // never over an earlier one
		if _, err := os.Stat(name); err != nil {
			break
		}
		name = filepath.Join(dir, fmt.Sprintf("%s-%d%s", stem, i, ext))
	}
	if err := os.Rename(tmp.Name(), name); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return name, nil
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
