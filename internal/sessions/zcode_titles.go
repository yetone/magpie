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
		out = append(out, file{agent: "zcode", path: path + "#" + id, mod: ms(updated),
			more: &fileMore{sid: id, oc: zcDB{ocDB{db}}}})
	}
	return ocRoots(out, parent)
}
