package sessions

import (
	"os"
	"path/filepath"
)

// ZCode's CLI, OpenCode made over, keeps its sessions in OpenCode's tables
// in a database of its own, ~/.zcode/cli/db/db.sqlite: a session row with
// how its title came about (title_source), and its messages and parts as
// JSON documents. A message counts as OpenCode's does but for its tokens,
// the AI SDK's: input with the cache in it, output with the reasoning. Its
// model is modelId (the older ones say modelID too), and a user message
// says who it is from (semantics.origin), a prompt typed a real_user's. A
// subagent's work is a session whose parent is the one it ran in, as in
// OpenCode. ZCode is a desktop app: no command picks a session up again.

// ZCodeDir is ZCode's folder, ~/.zcode, under Go's home. Its CLI's
// database stays there, USERPROFILE on Windows, even where the app keeps
// the rest under a HOME the user set (appdir.ZCodeHome): ZCode 3.14.3 for
// Windows, run with both, wrote cli\db\db.sqlite under USERPROFILE and
// v2 under HOME.
func ZCodeDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".zcode")
}

func zcodeDB() string { return filepath.Join(ZCodeDir(), "cli", "db", "db.sqlite") }

func zcodeFiles() []file {
	if !Reads("zcode") {
		return nil
	}
	if db := zcodeDB(); fileExists(db) {
		return openCodeDBFiles("zcode", db)
	}
	return nil
}

// zcDB is ZCode's database: OpenCode's, with a session's title its own only
// when it was not the default one.
type zcDB struct{ ocDB }

func (s zcDB) info(sid string) (ocInfo, bool) {
	var i ocInfo
	var source string
	err := s.db.QueryRow(`SELECT id, COALESCE(parent_id, ''), directory, title, COALESCE(title_source, ''), time_created, time_updated FROM session WHERE id = ?`, sid).
		Scan(&i.ID, &i.ParentID, &i.Directory, &i.Title, &source, &i.Time.Created, &i.Time.Updated)
	if source == "default" {
		i.Title = ""
	}
	// the name the app shows, when it has one
	if t, ok := zcodeTaskOf(sid); ok && title(t.title) != "" && !ocDefaultTitle(title(t.title)) {
		i.Title = t.title
	}
	return i, err == nil
}

// copied says whether a message is a copy of one still in the database.
// ZCode copies a session's messages into another for a side chat on a
// selection, a fork and a checkpoint restored (ZCode 3.17's CLI): each
// copy gets an id and session of its own and keeps the original's time,
// model and tokens, and its metadata.forkOrigin names the message it was
// copied from, {sessionId, messageId}. The copy made no call, so its
// tokens are the original's, and counting both counted the session again
// on the day it ran (Sky, skyhuang1994: a side chat opened on 11-02 from a
// session of 10-31 put 0.42B more GLM-5.3 tokens on 10-31, where ZCode's
// own model_usage has none for it). A copy of a copy names the copy it
// was made from, so one copy is counted only when what it came from is
// gone: the session deleted, or never in this database. ZCode's own
// notices of a fork carry a forkOrigin of another shape, with no
// messageId, and are no copies.
func (s zcDB) copied(m ocMessage) bool {
	if m.Metadata == nil || m.Metadata.ForkOrigin == nil {
		return false
	}
	id := m.Metadata.ForkOrigin.MessageID
	if id == "" || id == m.ID {
		return false
	}
	var n int
	return s.db.QueryRow(`SELECT COUNT(*) FROM message WHERE id = ?`, id).Scan(&n) == nil && n > 0
}
