package sessions

// zcodeTitleFiles are ZCode's sessions as a title lookup needs them: the
// session rows alone, without the message join zcodeFiles makes for a
// session's size and last activity. Titles asks again every 15s, and that
// join over a large database took ~20s.
//
// mu is held by Titles, as openDB's handles expect.
func zcodeTitleFiles() []file {
	if !Reads("zcode") {
		return nil
	}
	path := zcodeDB()
	if !fileExists(path) {
		return nil
	}
	db := openDB(path)
	if db == nil {
		return nil
	}
	rows, err := db.Query(`SELECT id, COALESCE(parent_id, ''), time_updated FROM session`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	parent := map[string]string{}
	var out []file
	for rows.Next() {
		var id, par string
		var updated int64
		if rows.Scan(&id, &par, &updated) != nil {
			continue
		}
		parent[id] = par
		out = append(out, file{agent: "zcode", path: path + "#" + id, mod: modOf(ms(updated)),
			more: &fileMore{sid: id, oc: zcDB{ocDB{db}}}})
	}
	return ocRoots(out, parent)
}

// zcodeTitleStamps gives the ZCode sessions a name poll asks for the size
// and last activity zcodeFiles would give them, read from each one's own
// messages: ZCode writes a message without touching its session's row, so
// the row's time alone missed a first prompt typed since the last poll, and
// a stamp unlike the full listing's had the Sessions page and the poll each
// read the session again after the other. A session whose messages can't
// be read keeps its row's stamp.
func zcodeTitleStamps(files []file, want map[string]bool) {
	for i := range files {
		f := &files[i]
		if f.agent != "zcode" || !want[f.key] {
			continue
		}
		zc, ok := f.m().oc.(zcDB)
		if !ok {
			continue
		}
		var n, last int64
		if zc.db.QueryRow(`SELECT COUNT(id), COALESCE(MAX(time_updated), 0) FROM message WHERE session_id = ?`, f.m().sid).Scan(&n, &last) != nil {
			continue
		}
		f.size, f.mod = n, modOf(ms(max(f.mod.UnixMilli(), last)))
	}
}
