package davsync

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/klauspost/compress/zstd"
	"github.com/yetone/magpie/internal/backup"
)

// ServerFile is what the server held, or answered with, where the backup
// should be, when it isn't one: what the Settings page says of it, and
// whether Upload may write this computer's setup over it. It never carries
// the file's contents — only its size, its kind, and a page's title.
type ServerFile struct {
	// What is "empty", "cut" (a magpie backup cut short), "page" (a web
	// page), "xml", "format" (another app's JSON file, named by its
	// "format"), "json" or "other"
	What   string `json:"what"`
	Size   int    `json:"size"`
	Title  string `json:"title,omitempty"`  // a page's <title>
	Format string `json:"format,omitempty"` // a JSON file's own "format"
	Type   string `json:"type,omitempty"`   // the kind of file, as its first bytes tell
	Moved  string `json:"moved,omitempty"`  // where a redirect took the read: host and path
	// Replace is whether the file is one Upload may write over: a page or
	// an XML answer is the server's (or a proxy's), not a file at that path
	Replace bool `json:"replace,omitempty"`
}

// notBackup is the error for a server file that doesn't open as a backup:
// what it is and what to do, instead of only "not a magpie backup".
type notBackup struct {
	f     ServerFile
	where string // the file and server, as messages name them
	cmd   string // the CLI command: "webdav" or "s3"
	err   error
}

func (e *notBackup) Unwrap() error { return e.err }

func (e *notBackup) Error() string {
	f := e.f
	upload := fmt.Sprintf("upload this computer's setup over it (Settings › Sync and backup, or magpie %s upload); the server's copy is kept in the sync folder first", e.cmd)
	switch f.What {
	case "empty":
		return fmt.Sprintf("%s is empty (0 bytes): a write to it didn't finish. To start it again, %s", e.where, upload)
	case "cut":
		return fmt.Sprintf("%s is a magpie backup cut short or damaged (%d bytes): a write to it was interrupted. Sync on the computer that wrote it last to rebuild it, or %s", e.where, f.Size, upload)
	case "page", "xml":
		got := "a web page"
		if f.Title != "" {
			got += fmt.Sprintf(" (%q)", f.Title)
		}
		if f.What == "xml" {
			got = "an XML document"
			if f.Title != "" {
				got += fmt.Sprintf(" (<%s>)", f.Title)
			}
		}
		got += " instead of the file"
		if f.Moved != "" {
			got += ", after a redirect to " + f.Moved
		}
		if e.cmd == "s3" {
			return fmt.Sprintf("reading %s, the server answered with %s: check the endpoint is the S3 API's, not the storage's website or console", e.where, got)
		}
		return fmt.Sprintf("reading %s, the server answered with %s: check the address is the server's WebDAV address (坚果云: https://dav.jianguoyun.com/dav/, Nextcloud: https://…/remote.php/dav/files/<user>/), not its web page, and that no sign-in page or proxy stands in front of it", e.where, got)
	case "format":
		return fmt.Sprintf("%s is a %q file, not a magpie backup: another app keeps a file there. Point sync at another folder, or %s", e.where, f.Format, upload)
	case "json":
		return fmt.Sprintf("%s is a JSON file that isn't a magpie backup (%d bytes). Point sync at another folder, or %s", e.where, f.Size, upload)
	}
	return fmt.Sprintf("%s isn't a magpie backup: it is %s (%d bytes). Point sync at another folder, or %s", e.where, f.Type, f.Size, upload)
}

// fileWhere names the backup file and the server in messages.
func fileWhere(c Config) string {
	if c.S3() {
		if s, err := newS3(c); err == nil {
			return s.where()
		}
		return "the file in the S3 bucket"
	}
	return "the file " + folder + "/" + file + " on the WebDAV server"
}

func cmdOf(c Config) string {
	if c.S3() {
		return "s3"
	}
	return "webdav"
}

// described is err, from opening data as a backup, said as what data is
// when it isn't a backup at all; any other error stays as it is.
func described(c Config, data []byte, err error) error {
	if !errors.Is(err, backup.ErrCorrupt) && !errors.Is(err, backup.ErrNotBackup) {
		return err
	}
	var nb *notBackup
	if errors.As(err, &nb) {
		return err
	}
	return &notBackup{f: whatIs(data), where: fileWhere(c), cmd: cmdOf(c), err: err}
}

var titleRe = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
var rootRe = regexp.MustCompile(`<([A-Za-z_][\w.:-]*)`)

// whatIs tells what a body that isn't a backup is, from its bytes alone.
func whatIs(data []byte) ServerFile {
	f := ServerFile{Size: len(data), Replace: true}
	trim := bytes.TrimSpace(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")))
	head := strings.ToLower(string(trim[:min(len(trim), 512)]))
	switch {
	case len(data) == 0:
		f.What = "empty"
	case backupHead(trim):
		f.What = "cut"
	case strings.HasPrefix(head, "<") && (strings.Contains(head, "<html") ||
		strings.HasPrefix(http.DetectContentType(trim), "text/html")):
		f.What, f.Replace = "page", false
		if m := titleRe.FindSubmatch(trim[:min(len(trim), 64<<10)]); m != nil {
			f.Title = clip(strings.Join(strings.Fields(html.UnescapeString(string(m[1]))), " "), 80)
		}
	case strings.HasPrefix(head, "<?xml") || strings.HasPrefix(head, "<"):
		f.What, f.Replace = "xml", false
		body := trim
		if strings.HasPrefix(head, "<?xml") {
			if i := bytes.Index(body, []byte("?>")); i >= 0 {
				body = body[i+2:]
			}
		}
		if m := rootRe.FindSubmatch(body); m != nil {
			f.Title = clip(string(m[1]), 40)
		}
	case json.Valid(trim):
		var top struct {
			Format any `json:"format"`
		}
		if json.Unmarshal(trim, &top) == nil {
			if s, ok := top.Format.(string); ok && s != "" {
				f.What, f.Format = "format", clip(s, 40)
				return f
			}
		}
		f.What = "json"
	default:
		f.What = "other"
		f.Type, _, _ = mime.ParseMediaType(http.DetectContentType(data))
		if f.Type == "" {
			f.Type = "application/octet-stream"
		}
	}
	return f
}

// backupHead is whether b begins like a sealed backup, as Seal writes it.
func backupHead(b []byte) bool {
	head := strings.Map(func(r rune) rune {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, string(b[:min(len(b), 64)]))
	const want = `{"format":"magpie-backup"`
	return strings.HasPrefix(head, want) || len(head) > 0 && strings.HasPrefix(want, head)
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

// Magic numbers of the compressed forms a server may send the file in.
var (
	gzipMagic = []byte{0x1f, 0x8b}
	zstdMagic = []byte{0x28, 0xb5, 0x2f, 0xfd}
)

// unpacked is a read's body as the file is: the Content-Encoding a server
// sent it in though asked for it as it is — gzip, deflate, zstd — undone,
// and a gzip or zstd body sent without saying so (a proxy that dropped the
// header) undone too. A backup is JSON, so a body that begins with either's
// magic number is never the file as it is. A header that names an encoding
// the body isn't in (a server that says gzip and sends the file) is
// ignored; one magpie can't undo (br) is said plainly.
func unpacked(kind string, data []byte, encoding string) ([]byte, error) {
	enc := strings.ToLower(strings.TrimSpace(encoding))
	if i := strings.LastIndexByte(enc, ','); i >= 0 { // the last applied is undone first
		enc = strings.TrimSpace(enc[i+1:])
	}
	switch {
	case bytes.HasPrefix(data, gzipMagic):
		return readAll(gzip.NewReader(bytes.NewReader(data)))
	case bytes.HasPrefix(data, zstdMagic):
		d, err := zstd.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		defer d.Close()
		return readAll(d, nil)
	case enc == "deflate" && len(data) > 0 && data[0] != '{':
		if out, err := readAll(zlib.NewReader(bytes.NewReader(data))); err == nil {
			return out, nil
		}
		return readAll(flate.NewReader(bytes.NewReader(data)), nil)
	case enc == "br" && len(data) > 0 && data[0] != '{':
		return nil, fmt.Errorf("the %s server sent the backup compressed with Brotli (Content-Encoding: br), though magpie asked for it as it is, and magpie can't unpack that: turn off Brotli compression for it on the server, or on the proxy in front of it", kind)
	}
	return data, nil
}

func readAll[R io.Reader](r R, err error) ([]byte, error) {
	if err != nil {
		return nil, err
	}
	return io.ReadAll(io.LimitReader(r, 64<<20))
}
