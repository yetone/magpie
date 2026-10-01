package sessions

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var codexTitles struct {
	sync.Mutex
	path     string
	size     int64
	modified time.Time
	names    map[string]string
}

func codexTitleIndexPath() string {
	return filepath.Join(CodexDir(), "session_index.jsonl")
}

// CodexTitles reads Codex's append-only name index, never its configuration or
// conversation contents. Later entries replace earlier names after a rename.
// The result contains only the requested IDs; missing names remain unknown.
func CodexTitles(ids []string) map[string]string {
	return codexTitlesAt(codexTitleIndexPath(), ids)
}

func codexTitlesAt(path string, ids []string) map[string]string {
	out := make(map[string]string)
	if len(ids) == 0 {
		return out
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return out
	}
	codexTitles.Lock()
	defer codexTitles.Unlock()
	if codexTitles.path != path || codexTitles.size != info.Size() || !codexTitles.modified.Equal(info.ModTime()) {
		file, err := os.Open(path)
		if err != nil {
			return out
		}
		defer file.Close()
		names := make(map[string]string)
		scan := bufio.NewScanner(file)
		scan.Buffer(make([]byte, 4096), 1<<20)
		for scan.Scan() {
			var row struct {
				ID   string `json:"id"`
				Name string `json:"thread_name"`
			}
			if json.Unmarshal(scan.Bytes(), &row) != nil || row.ID == "" {
				continue
			}
			name := strings.Join(strings.Fields(row.Name), " ")
			if name != "" {
				names[row.ID] = string([]rune(name)[:min(len([]rune(name)), 200)])
			}
		}
		if scan.Err() != nil {
			return out
		}
		codexTitles.path, codexTitles.size, codexTitles.modified, codexTitles.names = path, info.Size(), info.ModTime(), names
	}
	for _, id := range ids {
		if name := codexTitles.names[id]; name != "" {
			out[id] = name
		}
	}
	return out
}
