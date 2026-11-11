package sessions

import (
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/provider"
)

// ZCode's desktop app names its sessions in an index of its own,
// ~/.zcode/v2/tasks-index.sqlite (in the data folder the user chose in its
// settings): a task per session, its task_id the CLI database's session id
// and its title the name the app shows, the user's own when they renamed it
// (title_overridden). The CLI database's title is the model's guess, or
// nothing at all (title_source "default"), so a session ZCode shows by name
// read as its first prompt here (Flandern1211 on X).

type zcodeTask struct {
	title   string
	updated int64
}

var zcodeTasks struct {
	sync.Mutex
	path  string
	stamp string
	byID  map[string]zcodeTask
}

func zcodeTasksPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(appdir.ZCodeDataIn(appdir.ZCodeHome(home)), ".zcode", "v2", "tasks-index.sqlite")
}

// zcodeTaskOf is the app's task for a session id, read again only when the
// index or its write-ahead log changed. An index that can't be read names
// nothing: the last good read stays.
func zcodeTaskOf(sid string) (zcodeTask, bool) {
	path := zcodeTasksPath()
	if path == "" {
		return zcodeTask{}, false
	}
	stamp := ""
	for _, p := range []string{path, path + "-wal"} {
		if fi, err := os.Stat(p); err == nil {
			stamp += fi.ModTime().String() + "/" + strconv.FormatInt(fi.Size(), 10) + ";"
		}
	}
	zcodeTasks.Lock()
	defer zcodeTasks.Unlock()
	if zcodeTasks.path != path || zcodeTasks.stamp != stamp {
		if m, ok := readZCodeTasks(path); ok {
			zcodeTasks.byID = m
		} else if zcodeTasks.path != path {
			zcodeTasks.byID = nil
		}
		zcodeTasks.path, zcodeTasks.stamp = path, stamp
	}
	t, ok := zcodeTasks.byID[sid]
	return t, ok
}

func readZCodeTasks(path string) (map[string]zcodeTask, bool) {
	if !fileExists(path) {
		return nil, false
	}
	db, err := provider.OpenReadOnly(path)
	if err != nil {
		return nil, false
	}
	defer db.Close()
	rows, err := db.Query(`SELECT task_id, title, updated_at FROM tasks WHERE deleted = 0`)
	if err != nil {
		return nil, false
	}
	defer rows.Close()
	out := map[string]zcodeTask{}
	for rows.Next() {
		var id, title string
		var updated int64
		if rows.Scan(&id, &title, &updated) != nil {
			return nil, false
		}
		out[id] = zcodeTask{title, updated}
	}
	return out, rows.Err() == nil
}
