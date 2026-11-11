package usage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/sessions"
)

// nori_tsukiji on X: with ZCode on the same computer as magpie, the Usage
// page's "Local session" calls were the very calls magpie had routed, so each
// was counted twice. A ZCode request names its session without the sess_
// prefix ZCode's database gives it (zcodeBareID), so the database's calls
// never shared a session with the gateway's records and were never paired
// with them. Whatever provider id ZCode wrote beside a reply, a call the
// gateway recorded is the gateway's alone.
func TestZCodeCallsPairWithTheGatewaysRecords(t *testing.T) {
	end := holdClock(t, time.Date(2026, 11, 12, 12, 0, 0, 0, time.Local)).Add(-time.Minute)
	for _, c := range []struct{ name, sid, header, provider string }{
		{"a session", "sess_7c1f", "7c1f", "zai"},
		{"a provider ZCode names its own way", "sess_7c1f", "7c1f", "custom_magpie"},
		{"a subagent", "sess_subagent_agent_9a2", "9a2", "zai"},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := sessionHomeOf(t)
			os.MkdirAll(filepath.Join(home, ".zcode", "cli", "db"), 0700)
			db, err := sql.Open("sqlite", filepath.Join(home, ".zcode", "cli", "db", "db.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			for _, q := range []string{
				"CREATE TABLE session (id text PRIMARY KEY, project_id text NOT NULL, parent_id text, slug text NOT NULL, directory text NOT NULL, title text NOT NULL, title_source text, version text NOT NULL, time_created integer NOT NULL, time_updated integer NOT NULL, time_archived integer, tokens_input integer DEFAULT 0 NOT NULL)",
				"CREATE TABLE message (id text PRIMARY KEY, session_id text NOT NULL, time_created integer NOT NULL, time_updated integer NOT NULL, data text NOT NULL)",
				"CREATE TABLE part (id text PRIMARY KEY, message_id text NOT NULL, session_id text NOT NULL, time_created integer NOT NULL, time_updated integer NOT NULL, data text NOT NULL)",
			} {
				if _, err := db.Exec(q); err != nil {
					t.Fatal(err)
				}
			}
			ms := end.Add(-time.Hour).UnixMilli()
			if _, err := db.Exec("INSERT INTO session (id, project_id, parent_id, slug, directory, title, title_source, version, time_created, time_updated) VALUES (?, 'p', NULL, 's', '/work/z', 'Port', 'ai', '3.17.2', ?, ?)", c.sid, ms, ms); err != nil {
				t.Fatal(err)
			}
			var recs []string
			for i, k := range []struct{ in, read, out int }{{9000, 8000, 120}, {9400, 8400, 90}} {
				at := end.Add(time.Duration(i-2) * time.Minute)
				data := fmt.Sprintf(`{"id":"msg_%d","role":"assistant","modelId":"glm-5.3","providerID":%q,"tokens":{"input":%d,"output":%d,"reasoning":0,"cache":{"read":%d,"write":0}},"time":{"created":%d,"completed":%d}}`,
					i, c.provider, k.in, k.out, k.read, at.Add(-5*time.Second).UnixMilli(), at.UnixMilli())
				if _, err := db.Exec("INSERT INTO message VALUES (?, ?, ?, ?, ?)", fmt.Sprintf("msg_%d", i), c.sid, at.UnixMilli(), at.UnixMilli(), data); err != nil {
					t.Fatal(err)
				}
				r := Record{Time: at.Add(-5 * time.Second), Millis: 5000, Agent: "zcode", Provider: "zhipu", Model: "glm-5.3", Served: "glm-5.3", Status: 200,
					Input: k.in - k.read, Output: k.out, CacheRead: k.read, Session: c.header, NativeSession: c.header, Endpoint: "/v1/messages"}
				j, _ := json.Marshal(r)
				recs = append(recs, string(j))
			}
			os.MkdirAll(filepath.Dir(Path()), 0700)
			os.WriteFile(Path(), []byte(strings.Join(recs, "\n")+"\n"), 0600)
			sessions.Reset()

			p := QueryPage(Today, Filter{}, 0, 100)
			if p.Total != 2 || p.Sum.Calls != 2 {
				t.Fatalf("%d rows, %d calls, want the gateway's 2: %+v", p.Total, p.Sum.Calls, p.Rows)
			}
			for _, r := range p.Rows {
				if r.Source == "log" {
					t.Fatalf("a routed call is listed again from ZCode's database: %+v", r)
				}
			}
			if d := Direct(Today); d.Totals.Calls != 0 {
				t.Fatalf("Direct counts %d calls ZCode made through magpie", d.Totals.Calls)
			}
		})
	}
}

// sessionHomeOf is pageHome with the home it made.
func sessionHomeOf(t *testing.T) string {
	t.Helper()
	pageHome(t)
	return os.Getenv("HOME")
}
