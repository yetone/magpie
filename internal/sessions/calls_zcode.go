package sessions

import (
	"database/sql"
	"encoding/json"
	"sort"
	"strings"
	"sync"
)

// ZCode's calls: the same OpenCode tables its reader uses (zcode.go), one
// call per assistant message, with the AI SDK's tokens — input with the
// cache in it, output with the reasoning. A message's model is read the way
// the tables hold it: modelID, or model.id under a message written by a newer
// ZCode, and the provider the reply came from beside it. The rows are read
// here rather than taken from the session summary, because the summary counts
// a reply once however many of its lines carry it, while each assistant
// message is a call of its own.
//
// As with dsh, the Sessions page read these all along and the usage ledger
// did not, so ZCode's tokens never reached the Usage page.
//
// A reply through magpie's gateway is left out here as OpenCode's is
// (ocGatewayProvider): ZCode records the provider it was told to use, and
// the route magpie writes says "magpie", so unlike dsh's this one is a
// provider id magpie itself chose and can be trusted to mean the gateway.

// zcCallList is the last listing of ZCode's database, kept while the
// database and its WAL are as they were. Its lock is the one ocCallList
// uses: callSources runs on more than one read at a time, and two of them
// finding the stamp changed would write this at once.
var zcCallList struct {
	sync.Mutex
	stamp string
	files []file
}

// zcodeCallFiles are ZCode's sessions as call sources.
func zcodeCallFiles() []file {
	path := zcodeDB()
	if !fileExists(path) {
		return nil
	}
	stamp := ocStamp(path)
	zcCallList.Lock()
	defer zcCallList.Unlock()
	if stamp != "" && stamp == zcCallList.stamp {
		return append([]file(nil), zcCallList.files...)
	}
	var out []file
	withOCCallDB(path, func(db *sql.DB) {
		out = openCodeDBFilesIn("zcode", path, db, "COUNT(m.id) + COALESCE(SUM(m.time_updated), 0)")
	})
	for i := range out {
		out[i].oc = nil // the handle is not kept; a read opens it again
	}
	zcCallList.stamp, zcCallList.files = stamp, out
	return append([]file(nil), out...)
}

// readZCodeCalls reads a session's calls whole, as readOpenCodeCalls does.
// ZCode is OpenCode's tables with a title source of its own, so its store is
// zcDB: the model id is modelId or the older modelID, and its tokens the AI
// SDK's.
func readZCodeCalls(f file) (*callFile, bool) {
	st := &callFile{Agent: "zcode", Path: f.path, Session: strings.TrimPrefix(f.key, "zcode:")}
	i := strings.LastIndex(f.path, "#")
	if i < 0 {
		return st, false
	}
	path, sid := f.path[:i], f.path[i+1:]
	var info ocInfo
	var msgs [][]byte
	if !withOCCallDB(path, func(db *sql.DB) {
		var store ocStore = zcDB{ocDB{db}}
		if ocHasTable(db, "session_v2") {
			var n int
			if db.QueryRow(`SELECT COUNT(*) FROM session_v2 WHERE id = ?`, sid).Scan(&n) == nil && n > 0 {
				store = ocV2DB{db}
			}
		}
		info, _ = store.info(sid)
		msgs = store.messages(sid)
	}) {
		return st, false
	}
	if st.Session == "" {
		st.Session = info.ID
	}
	for _, b := range msgs {
		var m ocMessage
		if json.Unmarshal(b, &m) != nil || m.Role != "assistant" || ocGatewayProvider(m.ProviderID) {
			continue
		}
		if c, ok := ocCall(st, m, info.Directory); ok {
			c.Agent = "zcode"
			st.Calls = append(st.Calls, c)
		}
	}
	sort.SliceStable(st.Calls, func(i, j int) bool {
		if !st.Calls[i].Time.Equal(st.Calls[j].Time) {
			return st.Calls[i].Time.Before(st.Calls[j].Time)
		}
		return st.Calls[i].Msg < st.Calls[j].Msg
	})
	return st, true
}
