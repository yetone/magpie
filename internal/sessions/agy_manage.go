package sessions

import (
	"path/filepath"
	"slices"
)

// agySubIDs are the conversations agyFold filed under a session's files.
func agySubIDs(fs []file) []string {
	var out []string
	for _, f := range fs {
		if !f.main && safeID.MatchString(f.m().sid) && !slices.Contains(out, f.m().sid) {
			out = append(out, f.m().sid)
		}
	}
	return out
}

// agyDataDir is the agy data folder a conversation's files are in: a
// transcript is brain/<id>/.system_generated/logs/transcript_full.jsonl in
// it, a conversation listed from its row alone conversations/<id>.db.
func agyDataDir(fs []file) string {
	for _, f := range fs {
		if !f.main {
			continue
		}
		up := 5
		if agyRowOnly(f.path) {
			up = 2
		}
		d := f.path
		for range up {
			d = filepath.Dir(d)
		}
		return d
	}
	return ""
}
