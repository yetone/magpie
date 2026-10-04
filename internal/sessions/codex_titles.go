package sessions

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// TitleApplication is an actual name write, not just a chat's current name.
// Keeping old writes lets an association survive a later manual rename.
type TitleApplication struct {
	Digest  string
	Updated time.Time
}
type titleRecipient struct {
	ID      string
	Updated time.Time
}

var codexTitles struct {
	sync.Mutex
	path         string
	size, offset int64
	modified     time.Time
	file         os.FileInfo
	names        map[string]string
	last         map[string]TitleApplication
	recipients   map[string][]titleRecipient
	writes       int
	overflow     bool
}

const titleApplicationLimit = 100000

func codexTitleIndexPath() string { return filepath.Join(CodexDir(), "session_index.jsonl") }
func CodexTitleDigest(name string) string {
	name = strings.Join(strings.Fields(name), " ")
	if name == "" {
		return ""
	}
	return codexFingerprint("title", name)
}

// CodexTitles reads only the append-only name index. It caches unchanged
// files and reads newly appended complete lines from the previous offset.
func CodexTitles(ids []string) map[string]string { return codexTitlesAt(codexTitleIndexPath(), ids) }
func codexTitlesAt(path string, ids []string) map[string]string {
	out := make(map[string]string)
	if len(ids) == 0 {
		return out
	}
	codexTitles.Lock()
	defer codexTitles.Unlock()
	if !loadCodexTitles(path) {
		return out
	}
	for _, id := range ids {
		if name := codexTitles.names[id]; name != "" {
			out[id] = name
		}
	}
	return out
}

// CodexTitleRecipients includes chats whose requests never crossed Magpie.
// They still count as conflicting recipients of a generated title.
func CodexTitleRecipients(digests []string) map[string][]TitleApplication {
	out := map[string][]TitleApplication{}
	if len(digests) == 0 {
		return out
	}
	codexTitles.Lock()
	defer codexTitles.Unlock()
	if !loadCodexTitles(codexTitleIndexPath()) || codexTitles.overflow {
		return out
	}
	for _, digest := range digests {
		for _, recipient := range codexTitles.recipients[digest] {
			out[recipient.ID] = append(out[recipient.ID], TitleApplication{digest, recipient.Updated})
		}
	}
	return out
}

// Caller holds codexTitles. Truncation, replacement or same-size rewriting
// rebuilds the cache; an incomplete last line waits for the next append.
func loadCodexTitles(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	if codexTitles.path == path && codexTitles.size == info.Size() && codexTitles.modified.Equal(info.ModTime()) {
		return true
	}
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	appendOnly := codexTitles.path == path && codexTitles.file != nil && os.SameFile(codexTitles.file, info) && info.Size() > codexTitles.size
	if !appendOnly {
		codexTitles.offset = 0
		codexTitles.names = map[string]string{}
		codexTitles.last = map[string]TitleApplication{}
		codexTitles.recipients = map[string][]titleRecipient{}
		codexTitles.writes, codexTitles.overflow = 0, false
	}
	if _, err := file.Seek(codexTitles.offset, 0); err != nil {
		return false
	}
	scan := bufio.NewScanner(file)
	scan.Buffer(make([]byte, 4096), 1<<20)
	offset := codexTitles.offset
	scan.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			offset += int64(i + 1)
			return i + 1, data[:i], nil
		}
		return 0, nil, nil // never commit an unterminated row
	})
	cutoff := time.Now().AddDate(0, 0, -31)
	for scan.Scan() {
		var row struct {
			ID      string    `json:"id"`
			Name    string    `json:"thread_name"`
			Updated time.Time `json:"updated_at"`
		}
		if json.Unmarshal(scan.Bytes(), &row) != nil || row.ID == "" {
			continue
		}
		name := strings.Join(strings.Fields(row.Name), " ")
		if name == "" {
			continue
		}
		chars := []rune(name)
		codexTitles.names[row.ID] = string(chars[:min(len(chars), 200)])
		if row.Updated.Before(cutoff) || codexTitles.overflow {
			continue
		}
		a := TitleApplication{CodexTitleDigest(name), row.Updated}
		prev, seen := codexTitles.last[row.ID]
		if seen && prev == a {
			continue
		}
		if codexTitles.writes >= titleApplicationLimit {
			codexTitles.overflow = true
			codexTitles.last = nil
			codexTitles.recipients = nil
			continue
		}
		codexTitles.last[row.ID] = a
		codexTitles.recipients[a.Digest] = append(codexTitles.recipients[a.Digest], titleRecipient{row.ID, a.Updated})
		codexTitles.writes++
	}
	if scan.Err() != nil {
		codexTitles.path = ""
		return false
	}
	codexTitles.path, codexTitles.size, codexTitles.modified, codexTitles.file, codexTitles.offset = path, info.Size(), info.ModTime(), info, offset
	return true
}
