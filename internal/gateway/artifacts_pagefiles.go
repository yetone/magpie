package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/artifact"
	"github.com/yetone/magpie/internal/lazyre"
)

// A page's files (docs/subsystems/artifacts.md, "Files") are the files of
// the folder its version declares (artifact.FilesDir), as Claude's files
// capability is a Claude Code project's: the viewer's shell asks the
// viewer once, and posts each call here with its own token. The page
// never reaches this route; the folder is opened as an os.Root, so no
// path or link leads out of it, and .git is never listed or read.
const (
	artifactFilesMost  = 600       // calls a minute, for each artifact
	artifactFilesPage  = 500       // entries a list answers at most
	artifactFilesRead  = 25 << 20  // bytes a read answers at most
	artifactFilesSaved = 512 << 20 // bytes of a file a download saves at most
)

var artifactFilesCalls = newMinuteLimit(artifactFilesMost)

// artifactFilesRefused are the types a download never saves: programs and
// scripts, as the contract has it.
var artifactFilesRefused = map[string]bool{
	".exe": true, ".js": true, ".mjs": true, ".cjs": true, ".bat": true, ".cmd": true, ".com": true, ".msi": true,
	".ps1": true, ".sh": true, ".app": true, ".scr": true, ".vbs": true, ".jar": true, ".dll": true, ".apk": true,
	".command": true, ".pkg": true, ".dmg": true, ".lnk": true, ".hta": true, ".wsf": true,
}

func artifactFilesError(w http.ResponseWriter, status int, code, msg string, more map[string]any) {
	e := map[string]any{"code": code, "message": msg}
	for k, v := range more {
		e[k] = v
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"error": e})
}

// filesPath is a path the page gave, as the root reads it: "" or "." is
// the folder itself; a path is relative, with /, and no part of it empty,
// . or .., nor .git. ok is false for anything else.
func filesPath(p string) (string, bool) {
	if p == "" || p == "." {
		return ".", true
	}
	if len(p) > 4096 || strings.ContainsAny(p, "\\\x00") || !fs.ValidPath(p) {
		return "", false
	}
	for _, part := range strings.Split(p, "/") {
		if gitPart(part) {
			return "", false
		}
	}
	return p, true
}

// gitShort is the short (8.3) name Windows may give .git: GIT~1.
var gitShort = lazyre.New(`(?i)^git~[0-9]+$`)

// gitPart is whether a path part names .git as a file system may open
// it: in any case (macOS's and Windows' folders don't tell .GIT from
// .git), with the dots and spaces Windows drops from a name's end, or
// by its short name.
func gitPart(part string) bool {
	n := strings.TrimRight(part, ". ")
	return strings.EqualFold(n, ".git") || gitShort.MatchString(n)
}

// filesInGit is whether p, in the folder at dir, is .git or in it once
// its links are followed: a link inside the folder may lead into .git,
// which os.Root lets it, as it stays in the folder. A path that can't be
// resolved isn't: opening it fails on its own.
func filesInGit(dir, p string) bool {
	base, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return false
	}
	real, err := filepath.EvalSymlinks(filepath.Join(dir, filepath.FromSlash(p)))
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(base, real)
	if err != nil {
		return true
	}
	return slices.ContainsFunc(strings.Split(filepath.ToSlash(rel), "/"), gitPart)
}

// filesRevision is what tells one state of a file from the next: its
// size and when it was changed.
func filesRevision(fi fs.FileInfo) string {
	h := sha256.Sum256([]byte(strconv.FormatInt(fi.Size(), 10) + ":" + strconv.FormatInt(fi.ModTime().UnixNano(), 10)))
	return hex.EncodeToString(h[:8])
}

// filesBefore is whether a comes before b in the order a walk takes them:
// part by part, so a folder's own files come before the next folder.
func filesBefore(a, b string) bool {
	as, bs := strings.Split(a, "/"), strings.Split(b, "/")
	for i := 0; i < len(as) && i < len(bs); i++ {
		if as[i] != bs[i] {
			return as[i] < bs[i]
		}
	}
	return len(as) < len(bs)
}

type filesEntry struct {
	Path        string `json:"path"`
	IsDirectory bool   `json:"isDirectory"`
	Size        int64  `json:"size"`
}

// filesList is one page of the entries under dir, after cursor when
// there is one, and the cursor of the next page if there is more.
func filesList(fsys fs.FS, dir string, recursive bool, cursor string) ([]filesEntry, string, error) {
	fi, err := fs.Stat(fsys, dir)
	if err != nil {
		return nil, "", err
	}
	if !fi.IsDir() {
		return nil, "", errNotFolder
	}
	out := []filesEntry{}
	more := false
	err = fs.WalkDir(fsys, dir, func(p string, d fs.DirEntry, err error) error {
		if p == dir {
			return err
		}
		if err != nil {
			// a folder that can't be read was listed already, and isn't
			// gone into
			return nil
		}
		if gitPart(d.Name()) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		descend := recursive && d.IsDir()
		if cursor != "" && !filesBefore(cursor, p) {
			// already given: go on only into the folders that hold the
			// cursor
			if descend && (p == cursor || strings.HasPrefix(cursor, p+"/")) {
				return nil
			}
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if len(out) == artifactFilesPage {
			more = true
			return fs.SkipAll
		}
		e := filesEntry{Path: p, IsDirectory: d.IsDir()}
		if info, err := d.Info(); err == nil && !d.IsDir() {
			e.Size = info.Size()
		}
		out = append(out, e)
		if d.IsDir() && !descend {
			return fs.SkipDir
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	next := ""
	if more {
		next = out[len(out)-1].Path
	}
	return out, next, nil
}

var errNotFolder = errors.New("not a folder")

// artifactFiles is POST <page>/_api/files from the shell: {t, version,
// op, path, recursive, cursor, offset, length, revision}. op where
// answers {folder, name}, the folder the version reads; list, {entries,
// cursor?}; read, the bytes asked for with X-Magpie-Size and
// X-Magpie-Revision; stat, {name, size, revision} of a file a download
// may save; save, the whole file as an attachment, if it is still at
// revision. Errors are the contract's FilesError.
func (s *Server) artifactFiles(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	var c struct {
		Token     string `json:"t"`
		Version   int    `json:"version"`
		Op        string `json:"op"`
		Path      string `json:"path"`
		Recursive bool   `json:"recursive"`
		Cursor    string `json:"cursor"`
		Offset    *int64 `json:"offset"`
		Length    *int64 `json:"length"`
		Revision  string `json:"revision"`
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 32<<10))
	if err != nil || len(body) >= 32<<10 || json.Unmarshal(body, &c) != nil {
		artifactFilesError(w, http.StatusBadRequest, "bad_request", "a call is a small JSON object", nil)
		return
	}
	if !local(r) || !loopbackHost(r.Host) {
		artifactFilesError(w, http.StatusForbidden, "not_found", "an artifact reads files in a browser on this computer, at "+URL(), nil)
		return
	}
	id, n, ok := ArtifactRef(r.PathValue("ref"))
	if !ok || strings.Contains(r.PathValue("ref"), "/") {
		artifactFilesError(w, http.StatusNotFound, "not_found", "no such artifact here", nil)
		return
	}
	a, err := artifact.Get(id)
	if err != nil {
		artifactFilesError(w, http.StatusNotFound, "not_found", "the artifact is gone", nil)
		return
	}
	if !artifact.ShellTokenOK(id, c.Token) {
		artifactFilesError(w, http.StatusForbidden, "not_found", "only the viewer's shell reads files", nil)
		return
	}
	if c.Version <= 0 {
		c.Version = a.Latest().N
		if n > 0 {
			c.Version = n
		}
	}
	v := a.Version(c.Version)
	if v == nil || !v.Capabilities.Has("files") || v.FilesDir == "" {
		artifactFilesError(w, http.StatusForbidden, "not_found", "this version of the page doesn't declare files", nil)
		return
	}
	if c.Op == "where" {
		writeJSON(w, http.StatusOK, map[string]any{"folder": v.FilesDir, "name": filepath.Base(v.FilesDir)})
		return
	}
	if !artifactFilesCalls.allow(id, time.Now()) {
		artifactFilesError(w, http.StatusTooManyRequests, "rate_limited", fmt.Sprintf("a page makes at most %d file calls a minute", artifactFilesMost), map[string]any{"retryAfterMs": 5000})
		return
	}
	p, ok := filesPath(c.Path)
	if !ok {
		artifactFilesError(w, http.StatusBadRequest, "bad_request", "a path is relative to the project's folder, with /, and no . or .. parts", nil)
		return
	}
	if p != "." && filesInGit(v.FilesDir, p) {
		artifactFilesError(w, http.StatusBadRequest, "bad_request", "a path is relative to the project's folder, with /, and no . or .. parts", nil)
		return
	}
	root, err := os.OpenRoot(v.FilesDir)
	if err != nil {
		artifactFilesError(w, http.StatusNotFound, "not_found", "the project's folder isn't there", nil)
		return
	}
	defer root.Close()
	switch c.Op {
	case "list":
		if c.Cursor != "" {
			if cp, ok := filesPath(c.Cursor); !ok || cp == "." || p != "." && !strings.HasPrefix(cp, p+"/") {
				artifactFilesError(w, http.StatusBadRequest, "bad_request", "the cursor isn't one this folder's list gave", nil)
				return
			}
		}
		entries, next, err := filesList(root.FS(), p, c.Recursive, c.Cursor)
		if err != nil {
			artifactFilesError(w, http.StatusNotFound, "not_found", "no folder at "+c.Path, nil)
			return
		}
		out := map[string]any{"entries": entries}
		if next != "" {
			out["cursor"] = next
		}
		writeJSON(w, http.StatusOK, out)
	case "read":
		off, length := int64(0), int64(artifactFilesRead)
		if c.Offset != nil {
			off = *c.Offset
		}
		if c.Length != nil {
			length = *c.Length
		}
		if off < 0 || length < 0 || length > artifactFilesRead {
			artifactFilesError(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("offset is 0 or more, length from 0 to %d", artifactFilesRead), nil)
			return
		}
		f, fi, ok := filesOpen(w, root, p, c.Path)
		if !ok {
			return
		}
		defer f.Close()
		buf := []byte{}
		if off < fi.Size() {
			buf = make([]byte, min(length, fi.Size()-off))
			k, err := f.ReadAt(buf, off)
			if err != nil && !errors.Is(err, io.EOF) {
				artifactFilesError(w, http.StatusServiceUnavailable, "unavailable", "the file couldn't be read", nil)
				return
			}
			buf = buf[:k]
		}
		if after, err := f.Stat(); err != nil || filesRevision(after) != filesRevision(fi) {
			artifactFilesError(w, http.StatusConflict, "changed", "the file changed while it was read", nil)
			return
		}
		h.Set("Content-Type", "application/octet-stream")
		h.Set("X-Magpie-Size", strconv.FormatInt(fi.Size(), 10))
		h.Set("X-Magpie-Revision", filesRevision(fi))
		w.WriteHeader(http.StatusOK)
		w.Write(buf)
	case "stat", "save":
		if p == "." {
			artifactFilesError(w, http.StatusBadRequest, "bad_request", "a download is of a file, not a folder", nil)
			return
		}
		// as a name's last dots and spaces, which Windows drops, are not
		if ext := strings.ToLower(path.Ext(strings.TrimRight(p, ". "))); artifactFilesRefused[ext] {
			artifactFilesError(w, http.StatusBadRequest, "bad_request", "an artifact doesn't save programs or scripts ("+ext+")", nil)
			return
		}
		f, fi, ok := filesOpen(w, root, p, c.Path)
		if !ok {
			return
		}
		defer f.Close()
		if fi.Size() > artifactFilesSaved {
			artifactFilesError(w, http.StatusRequestEntityTooLarge, "too_large", fmt.Sprintf("a download is at most %d MiB", artifactFilesSaved>>20), nil)
			return
		}
		if c.Op == "stat" {
			writeJSON(w, http.StatusOK, map[string]any{"name": path.Base(p), "size": fi.Size(), "revision": filesRevision(fi)})
			return
		}
		if c.Revision != "" && c.Revision != filesRevision(fi) {
			artifactFilesError(w, http.StatusConflict, "changed", "the file changed since the viewer was asked", nil)
			return
		}
		h.Set("Content-Type", "application/octet-stream")
		h.Set("Content-Length", strconv.FormatInt(fi.Size(), 10))
		h.Set("X-Magpie-Revision", filesRevision(fi))
		h.Set("Content-Disposition", "attachment; filename*=UTF-8''"+strings.ReplaceAll(url.PathEscape(path.Base(p)), "'", "%27"))
		w.WriteHeader(http.StatusOK)
		io.Copy(w, io.LimitReader(f, fi.Size()))
	default:
		artifactFilesError(w, http.StatusBadRequest, "bad_request", "op is where, list, read, stat or save", nil)
	}
}

// filesOpen opens the file at p for reading, or answers the page's error:
// not_found when there is none, bad_request for a folder.
func filesOpen(w http.ResponseWriter, root *os.Root, p, given string) (*os.File, fs.FileInfo, bool) {
	if p == "." {
		artifactFilesError(w, http.StatusBadRequest, "bad_request", "read takes a file's path", nil)
		return nil, nil, false
	}
	f, err := root.Open(filepath.FromSlash(p))
	if err != nil {
		artifactFilesError(w, http.StatusNotFound, "not_found", "no file at "+given, nil)
		return nil, nil, false
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		artifactFilesError(w, http.StatusNotFound, "not_found", "no file at "+given, nil)
		return nil, nil, false
	}
	if fi.IsDir() {
		f.Close()
		artifactFilesError(w, http.StatusBadRequest, "bad_request", given+" is a folder", nil)
		return nil, nil, false
	}
	if !fi.Mode().IsRegular() {
		f.Close()
		artifactFilesError(w, http.StatusNotFound, "not_found", "no file at "+given, nil)
		return nil, nil, false
	}
	return f, fi, true
}
