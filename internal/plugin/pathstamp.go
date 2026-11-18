package plugin

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// A plugin added by its path (a file or folder of the user's own, as a
// plugin's author has it while writing it) is loaded by the host as it
// was when the host started. An edit to it went unseen until magpie was
// quit and opened again (yansheng1003 on X: "每次改了需要重新magpie"), so
// what the host loaded is looked at as plugins.json is (checkList): once
// its files change, the host is started again with them, as the
// middleware already is (middleware.stamp).

// pathsEvery is how often the path plugins' files are looked at: checkList
// runs on every call to a plugin, and a folder is walked.
var pathsEvery = time.Second

// pathsMax bounds the files looked at in one plugin's folder: a path
// plugin pointed at a large tree is seen changing in its first files only.
const pathsMax = 2000

var pathsSeen struct {
	sync.Mutex
	at    time.Time
	list  string // plugins.json's stamp the paths were found under
	stamp string
}

// seenStamp is what the host loaded the plugins from: plugins.json and
// what is installed (listStamp), and the files of the plugins added by
// their path.
func seenStamp() string { return listStamp() + "|" + pathsStamp() }

// pathsStamp is the files of every plugin on in plugins.json that was
// added by its path: their names, times and sizes. Walked at most once a
// pathsEvery, unless plugins.json changed since.
func pathsStamp() string {
	ls := listStamp()
	pathsSeen.Lock()
	defer pathsSeen.Unlock()
	if pathsSeen.list == ls && time.Since(pathsSeen.at) < pathsEvery {
		return pathsSeen.stamp
	}
	var b strings.Builder
	for _, e := range list().Plugins {
		if e.Off || !IsPath(e.Spec) {
			continue
		}
		stampPath(&b, Target(e.Spec))
	}
	pathsSeen.at, pathsSeen.list, pathsSeen.stamp = time.Now(), ls, b.String()
	return pathsSeen.stamp
}

// stampPath writes root's files to b: root itself when it is a file, else
// what is under it, its dependencies (node_modules) and hidden folders
// (.git) left out.
func stampPath(b *strings.Builder, root string) {
	n := 0
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable: as it is, nothing to compare
		}
		if d.IsDir() {
			if p != root && (d.Name() == "node_modules" || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if n++; n > pathsMax {
			return filepath.SkipAll
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			if t, err := os.Stat(p); err == nil {
				fi = t
			}
		}
		fmt.Fprint(b, p, ":", fi.ModTime().UnixNano(), ":", fi.Size(), ";")
		return nil
	})
}
