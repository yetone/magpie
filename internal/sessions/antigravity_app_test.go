package sessions

import (
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// antigravitySchema is a conversation database's tables as Antigravity
// 2.23.0 makes them (yygutn's `.schema` on #2190).
const antigravitySchema = "CREATE TABLE `trajectory_meta` (`trajectory_id` text,`cascade_id` text,`trajectory_type` integer,`source` integer,PRIMARY KEY (`trajectory_id`));\n" +
	"CREATE TABLE `steps` (`idx` integer,`step_type` integer NOT NULL DEFAULT 0,`status` integer NOT NULL DEFAULT 0,`has_subtrajectory` numeric NOT NULL DEFAULT false,`metadata` blob,`error_details` blob,`permissions` blob,`task_details` blob,`render_info` blob,`step_payload` blob,`step_format` integer NOT NULL DEFAULT 0,PRIMARY KEY (`idx`));\n" +
	"CREATE INDEX `idx_steps_status` ON `steps`(`status`);\n" +
	"CREATE INDEX `idx_steps_step_type` ON `steps`(`step_type`);\n" +
	"CREATE TABLE `gen_metadata` (`idx` integer,`data` blob,`size` integer NOT NULL DEFAULT 0,PRIMARY KEY (`idx`));\n" +
	"CREATE TABLE `executor_metadata` (`idx` integer,`data` blob,PRIMARY KEY (`idx`));\n" +
	"CREATE TABLE `parent_references` (`idx` integer,`data` blob,PRIMARY KEY (`idx`));\n" +
	"CREATE TABLE `trajectory_metadata_blob` (`id` text DEFAULT \"main\",`data` blob,PRIMARY KEY (`id`));\n" +
	"CREATE TABLE `battle_mode_infos` (`idx` integer,`data` blob,PRIMARY KEY (`idx`));"

// antigravityModelField is gen_metadata.data's field 19 as yygutn quoted
// it on #2190, the same for generators 0, 1 and 2: "gemini-3.8-flash-n".
// The rest of the row is the request's text, which wasn't posted, so here
// the field is the whole of the message in field 1 it sits in.
const antigravityModelField = "9A011267656D696E692D332E382D666C6173682D6E"

// antigravityFixture makes a conversation database in the app's data
// folder under home, in WAL mode as the app keeps it, with the four step
// rows yygutn posted on #2190 (testdata/antigravity/steps.tsv, the bytes
// as posted), a step with no metadata, a user's step whose metadata has
// no model call, and generators 0 to 2. Step 11's generator (5) has no
// row.
func antigravityFixture(t *testing.T, home, id string) string {
	t.Helper()
	dir := filepath.Join(home, ".gemini", "antigravity", "conversations")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, id+".db")
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(antigravitySchema); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/antigravity/steps.tsv")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		idx, h, _ := strings.Cut(line, "\t")
		b, err := hex.DecodeString(strings.TrimSpace(h))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("INSERT INTO steps (idx, step_type, metadata) VALUES (?, 15, ?)", idx, b); err != nil {
			t.Fatal(err)
		}
	}
	// a step with no metadata, and one whose metadata is only its time
	// (the first 13 bytes of step 1's: field 1)
	user, _ := hex.DecodeString("0A0B08DEBCB6D60610B8AAB35C")
	if _, err := db.Exec("INSERT INTO steps (idx, step_type) VALUES (2, 1)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO steps (idx, step_type, metadata) VALUES (0, 14, ?)", user); err != nil {
		t.Fatal(err)
	}
	field, _ := hex.DecodeString(antigravityModelField)
	gen := append([]byte{0x0A, byte(len(field))}, field...)
	for i := range 3 {
		if _, err := db.Exec("INSERT INTO gen_metadata (idx, data, size) VALUES (?, ?, ?)", i, gen, len(gen)); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

// #2190 (yygutn), xxooioo on Discord: the Antigravity app's own calls,
// made without magpie, reach the usage ledger and the Sessions page,
// with the tokens yygutn's figures for those rows give.
func TestAntigravityAppCalls(t *testing.T) {
	claude, _ := setup(t)
	home := filepath.Join(filepath.Dir(claude), "home")
	const id = "37c3c786-9a11-44be-97df-b8fc1ccebc78"
	antigravityFixture(t, home, id)

	var got []Call
	for _, c := range Calls(time.Time{}) {
		if c.Agent == "antigravity" {
			got = append(got, c)
		}
	}
	slices.SortFunc(got, func(a, b Call) int { return a.Time.Compare(b.Time) })
	want := []struct {
		sec    int64
		nanos  int64
		model  string
		tokens Tokens
	}{
		{1791860318, 193779000, "gemini-3.8-flash", Tokens{Input: 13317, Output: 621}},
		{1791860328, 770822000, "gemini-3.8-flash", Tokens{Input: 14184, Output: 164}},
		{1791860333, 626675000, "gemini-3.8-flash", Tokens{Input: 15531, Output: 194}},
		{1791860353, 992204000, "", Tokens{Input: 4254, Output: 276, CacheRead: 18561}},
	}
	if len(got) != len(want) {
		t.Fatalf("want %d calls (none from a step without a model call), got %d: %+v", len(want), len(got), got)
	}
	msgs := map[string]bool{}
	for i, w := range want {
		c := got[i]
		at := time.Unix(w.sec, w.nanos)
		if !c.Time.Equal(at) || c.Session != id || c.Tokens != w.tokens || c.Model != w.model {
			t.Errorf("call %d: %v %s %q %+v, want %v %s %q %+v", i, c.Time, c.Session, c.Model, c.Tokens, at, id, w.model, w.tokens)
		}
		if c.Msg == "" || msgs[c.Msg] {
			t.Errorf("call %d: step id %q to count it once by is empty or taken", i, c.Msg)
		}
		msgs[c.Msg] = true
	}

	list := List(0)
	i := slices.IndexFunc(list, func(s Session) bool { return s.Agent == "antigravity" })
	if i < 0 {
		t.Fatalf("no Antigravity session in %+v", list)
	}
	s := list[i]
	if s.ID != id || !s.ReadOnly {
		t.Errorf("session %s read only %v, want %s read only", s.ID, s.ReadOnly, id)
	}
	var sum Tokens
	for _, m := range s.Models {
		sum.add(m.Tokens)
	}
	if want := (Tokens{Input: 13317 + 14184 + 15531 + 4254, Output: 621 + 164 + 194 + 276, CacheRead: 18561}); sum != want {
		t.Errorf("session tokens %+v, want %+v (models %+v)", sum, want, s.Models)
	}
}

// A response id two steps share is one call, counted at its last step.
func TestAntigravityAppOneResponseOnce(t *testing.T) {
	claude, _ := setup(t)
	home := filepath.Join(filepath.Dir(claude), "home")
	path := antigravityFixture(t, home, "b9eace9a-b15a-4dde-a440-b52e02016fa5")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var b []byte
	if err := db.QueryRow("SELECT metadata FROM steps WHERE idx = 1").Scan(&b); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO steps (idx, metadata) VALUES (4, ?)", b); err != nil {
		t.Fatal(err)
	}
	calls, err := antigravityRead(path)
	if err != nil {
		t.Fatal(err)
	}
	var steps []string
	for _, c := range calls {
		steps = append(steps, strconv.FormatInt(c.step, 10))
	}
	if got := strings.Join(steps, ","); got != "4,3,5,11" {
		t.Errorf("calls at steps %s, want 4,3,5,11 (step 1's response once, at step 4)", got)
	}
}

// A metadata row cut short is no call, and the rest are still read.
func TestAntigravityAppCutRow(t *testing.T) {
	b, _ := hex.DecodeString("0A0C08E8BCB6D60610F09EC7EF021802320C08EDBCB6D60610E08CA0A2023A0C08EDBCB6D60610A09BAAA402420C08EDBCB6D60610A09BAAA4024A7508A60A")
	if _, _, ok := agAppStep(b); ok {
		t.Error("a row cut inside its model call read as a call")
	}
}

// oioio's Antigravity database (#help, 11-06) names gemini-4-argon's
// generator "gemini-4-argon-d": Google's deployment of that model, as
// gemini-3.8-flash-n is 3.8 Flash's. It counts as gemini-4-argon, so Usage
// shows one model at its price. A lone letter after another maker's id, or
// a Gemini id whose last word is longer, stays as written.
func TestAntigravityAppDeploymentName(t *testing.T) {
	gen := func(field int, m string) []byte {
		inner := append(binary.AppendUvarint(nil, uint64(field)<<3|2), byte(len(m)))
		inner = append(inner, m...)
		return append([]byte{1<<3 | 2, byte(len(inner))}, inner...)
	}
	for in, want := range map[string]string{
		"gemini-4-argon-d":      "gemini-4-argon",
		"gemini-3.8-flash-n":    "gemini-3.8-flash",
		"gemini-3.8-flash-lite": "gemini-3.8-flash-lite",
		"gemini-3.8-flash-nano": "gemini-3.8-flash-nano",
		"gemini-4-argon":        "gemini-4-argon",
		"gemini-d":              "gemini-d",
		"gpt-5-d":               "gpt-5-d",
		"claude-sonnet-4-5-n":   "claude-sonnet-4-5-n",
		"gemini-4-argon-D":      "gemini-4-argon-D",
	} {
		for _, field := range []int{19, 21, 22} {
			if got := agAppModel(gen(field, in)); got != want {
				t.Errorf("field %d %q: %q, want %q", field, in, got, want)
			}
		}
	}
}
