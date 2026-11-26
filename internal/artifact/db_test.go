package artifact

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// withDB is a new artifact whose version 1 declares caps, and its store
// as the agent reaches it.
func withDB(t *testing.T, caps string) (*Artifact, *DB) {
	t.Helper()
	sandbox(t)
	c, err := ParseCapabilities(json.RawMessage(caps))
	if err != nil {
		t.Fatal(err)
	}
	a, err := PublishPage(Publish{Page: []byte("<!doctype html><title>Board</title><p>x"), Capabilities: c})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeDB(a.ID) })
	db, err := OpenDB(a.ID, 0, Viewer{Agent: true})
	if err != nil {
		t.Fatal(err)
	}
	return a, db
}

func code(err error) string {
	var de *DBError
	if errors.As(err, &de) {
		return de.Code
	}
	if err == nil {
		return ""
	}
	return "other: " + err.Error()
}

func TestCheckPath(t *testing.T) {
	for _, c := range []struct {
		p   string
		doc bool
		ok  bool
	}{
		{"tasks", false, true},
		{"tasks/t1", true, true},
		{"tasks/t1/notes", false, true},
		{"tasks", true, false},
		{"tasks/t1", false, false},
		{"", false, false},
		{"tasks//t1", true, false},
		{"tasks/..", true, false},
		{"tasks/a b", true, false},
		{"tasks/" + strings.Repeat("a", 201), true, false},
		{strings.Repeat("a/", 16) + "a", false, false},
		{"data/users/u_1:x@y+z~", false, true},
		{"data/users/u_1:x@y+z~/k", true, true},
	} {
		if _, err := CheckPath(c.p, c.doc); (err == nil) != c.ok {
			t.Errorf("CheckPath(%q, %v) = %v", c.p, c.doc, err)
		} else if err != nil && code(err) != "invalid_argument" {
			t.Errorf("CheckPath(%q): code %s", c.p, code(err))
		}
	}
}

// A document is set, merged into, read back and deleted; update needs it
// there; a delete of one not there is done; each write bumps its version.
func TestDBWrites(t *testing.T) {
	_, db := withDB(t, `{"db":{}}`)
	if d, err := db.Get("tasks/t1"); d != nil || err != nil {
		t.Fatalf("a document before any write: %+v %v", d, err)
	}
	if err := db.Update("tasks/t1", map[string]any{"a": 1.0}); code(err) != "invalid_argument" {
		t.Fatalf("update of a missing document: %v", err)
	}
	if err := db.Set("tasks/t1", map[string]any{"title": "one", "meta": map[string]any{"x": 1.0, "y": 2.0}, "tags": []any{"a", "b"}}); err != nil {
		t.Fatal(err)
	}
	if err := db.Update("tasks/t1", map[string]any{"meta": map[string]any{"y": 3.0}, "tags": []any{"c"}}); err != nil {
		t.Fatal(err)
	}
	d, err := db.Get("tasks/t1")
	if err != nil || d == nil {
		t.Fatal(d, err)
	}
	got, _ := json.Marshal(d.Data)
	if string(got) != `{"meta":{"x":1,"y":3},"tags":["c"],"title":"one"}` || d.Version != 2 || d.ID != "t1" || d.Path != "tasks/t1" {
		t.Fatalf("after update: %s version %d id %s", got, d.Version, d.ID)
	}
	if err := db.Set("tasks/t1", nil); code(err) != "invalid_argument" {
		t.Errorf("a set with no body: %v", err)
	}
	if err := db.Set("tasks", map[string]any{}); code(err) != "invalid_argument" {
		t.Errorf("a set at a collection: %v", err)
	}
	deep := map[string]any{}
	for i, m := 0, deep; i < 40; i++ {
		n := map[string]any{}
		m["n"] = n
		m = n
	}
	if err := db.Set("tasks/deep", deep); code(err) != "invalid_argument" {
		t.Errorf("a body 40 deep: %v", err)
	}
	if err := db.Set("tasks/big", map[string]any{"s": strings.Repeat("x", 257<<10)}); code(err) == "" {
		t.Errorf("a body over 256 KiB was written")
	}
	for range 2 {
		if err := db.Delete("tasks/t1"); err != nil {
			t.Fatal(err)
		}
	}
	if d, _ := db.Get("tasks/t1"); d != nil {
		t.Fatal("deleted, still there")
	}
}

// A batch is written whole or not at all.
func TestDBBatchAllOrNothing(t *testing.T) {
	_, db := withDB(t, `{"db":{}}`)
	err := db.Batch([]Op{{Op: "set", Path: "a/1", Data: map[string]any{"n": 1.0}}, {Op: "update", Path: "a/missing", Data: map[string]any{"n": 2.0}}})
	if code(err) != "invalid_argument" {
		t.Fatalf("a batch with a bad update: %v", err)
	}
	if d, _ := db.Get("a/1"); d != nil {
		t.Fatal("a refused batch wrote its first set")
	}
	if err := db.Batch([]Op{{Op: "set", Path: "a/1", Data: map[string]any{}}, {Op: "set", Path: "a/2", Data: map[string]any{}}, {Op: "delete", Path: "a/1"}}); err != nil {
		t.Fatal(err)
	}
	if docs, _, _ := Stats(db.id); docs != 1 {
		t.Fatalf("%d documents after set, set, delete", docs)
	}
	if err := db.Batch(make([]Op, 501)); code(err) != "invalid_argument" {
		t.Errorf("501 writes: %v", err)
	}
}

func TestDBQuery(t *testing.T) {
	_, db := withDB(t, `{"db":{}}`)
	rows := map[string]map[string]any{
		"a": {"n": 3.0, "tag": "x", "tags": []any{"red"}},
		"b": {"n": 1.0, "tag": "y", "tags": []any{"red", "blue"}},
		"c": {"n": 2.0, "tag": "x"},
		"d": {"tag": "z"},
		"e": {"n": "2", "tag": "x"},
	}
	for id, data := range rows {
		if err := db.Set("items/"+id, data); err != nil {
			t.Fatal(err)
		}
	}
	db.Set("other/z", map[string]any{"n": 1.0})
	ids := func(q Query) string {
		t.Helper()
		docs, err := db.Run(q)
		if err != nil {
			t.Fatalf("%+v: %v", q, err)
		}
		var out []string
		for _, d := range docs {
			out = append(out, d.ID)
		}
		return strings.Join(out, ",")
	}
	for _, c := range []struct {
		q    Query
		want string
	}{
		{Query{Collection: "items"}, "a,b,c,d,e"},
		{Query{Collection: "items", OrderBy: "n"}, "b,c,a,e,d"},
		{Query{Collection: "items", OrderBy: "n", Desc: true}, "e,a,c,b,d"},
		{Query{Collection: "items", Where: []Filter{{"tag", "==", "x"}}}, "a,c,e"},
		{Query{Collection: "items", Where: []Filter{{"n", ">", 1.0}}}, "a,c"},
		{Query{Collection: "items", Where: []Filter{{"n", "<=", 2.0}, {"tag", "!=", "y"}}}, "c"},
		{Query{Collection: "items", Where: []Filter{{"tag", "in", []any{"y", "z"}}}}, "b,d"},
		{Query{Collection: "items", Where: []Filter{{"tag", "not-in", []any{"x"}}}}, "b,d"},
		{Query{Collection: "items", Where: []Filter{{"tags", "array-contains", "red"}}}, "a,b"},
		{Query{Collection: "items", OrderBy: "n", Limit: 2}, "b,c"},
	} {
		if got := ids(c.q); got != c.want {
			t.Errorf("%+v: %s, want %s", c.q, got, c.want)
		}
	}
	many := make([]Filter, 11)
	for i := range many {
		many[i] = Filter{"n", "==", 1.0}
	}
	for _, q := range []Query{
		{Collection: "items", Where: many},
		{Collection: "items", Where: []Filter{{"n", "~", 1.0}}},
		{Collection: "items", Where: []Filter{{"n", "in", 1.0}}},
		{Collection: "items", Limit: 1001},
		{Collection: "items/a"},
	} {
		if _, err := db.Run(q); code(err) != "invalid_argument" {
			t.Errorf("%+v: %v", q, err)
		}
	}
}

// The rules of the db capability, as a viewer meets them: all read the
// votes and only the owner writes them, each writes their own vote, and
// data/users/<id>/ is the viewer's alone, the owner's included.
func TestDBRules(t *testing.T) {
	a, agent := withDB(t, `{"db":{"rules":[{"path":"votes","read":"view","write":"owner"},{"path":"votes/{self}","write":"interact"},{"path":"admin","read":"admin","write":"admin"}]}}`)
	as := func(id string, l Level) *DB {
		db, err := OpenDB(a.ID, 0, Viewer{ID: id, Level: l})
		if err != nil {
			t.Fatal(err)
		}
		return db
	}
	alice, bob, owner, viewer := as("alice", LevelInteract), as("bob", LevelInteract), as("own", LevelOwner), as("vic", LevelView)
	if err := alice.Set("votes/alice", map[string]any{"v": "yes"}); err != nil {
		t.Fatalf("alice's own vote: %v", err)
	}
	if err := alice.Set("votes/bob", map[string]any{"v": "no"}); code(err) != "invalid_argument" {
		t.Errorf("alice wrote bob's vote: %v", err)
	}
	if err := viewer.Set("votes/vic", map[string]any{"v": "no"}); code(err) != "invalid_argument" {
		t.Errorf("a viewer wrote: %v", err)
	}
	if err := owner.Set("votes/bob", map[string]any{"v": "tidied"}); err != nil {
		t.Errorf("the owner tidying: %v", err)
	}
	if d, _ := bob.Get("votes/alice"); d == nil {
		t.Error("bob can't read alice's vote")
	}
	if docs, _ := viewer.Run(Query{Collection: "votes"}); len(docs) != 2 {
		t.Errorf("a viewer reads %d votes", len(docs))
	}
	agent.Set("admin/x", map[string]any{})
	if d, _ := alice.Get("admin/x"); d != nil {
		t.Error("a contributor read an admin document")
	}
	if d, _ := owner.Get("admin/x"); d == nil {
		t.Error("the owner can't read an admin document")
	}
	// each viewer's own folder
	if err := alice.Set("data/users/alice/prefs", map[string]any{"dark": true}); err != nil {
		t.Fatalf("alice's own data: %v", err)
	}
	if d, _ := owner.Get("data/users/alice/prefs"); d != nil {
		t.Error("the owner read alice's own data")
	}
	if docs, _ := owner.Run(Query{Collection: "data/users/alice"}); len(docs) != 0 {
		t.Error("the owner listed alice's own data")
	}
	if err := owner.Set("data/users/alice/prefs", map[string]any{}); code(err) != "invalid_argument" {
		t.Errorf("the owner wrote alice's own data: %v", err)
	}
	if d, _ := alice.Get("data/users/alice/prefs"); d == nil {
		t.Error("alice can't read her own data")
	}
	if d, _ := agent.Get("data/users/alice/prefs"); d == nil {
		t.Error("the agent reaches everything, and can't read alice's")
	}
}

func TestParseCapabilities(t *testing.T) {
	for _, bad := range []string{
		`{"permissions":{}}`,
		`{"teleport":{}}`,
		`{"db":[]}`,
		`{"db":{"rules":[{"path":"a","read":"root"}]}}`,
		`{"db":{"rules":[{"path":"a","read":"admin","write":"view"}]}}`,
		`{"db":{"rules":[{"path":"a","read":"view"},{"path":"a","write":"owner"}]}}`,
		`{"db":{"rules":[{"path":"a","read":"view","write":"self"}]}}`,
		`{"db":{"rules":[{"path":"a","read":"view"},{"path":"a/{self}","write":"interact"}]}}`,
		`{"db":{"rules":[{"path":"{self}/a"}]}}`,
		`{"user":{"scopes":["phone"]}}`,
		`{"room":{"topics":{"move":"view"}}}`,
		`{"room":{"topics":{"Move":"interact"}}}`,
		`{"room":{"topics":"move"}}`,
		`{"room":{"topics":{` + manyTopics(65) + `}}}`,
		`[]`,
	} {
		if _, err := ParseCapabilities(json.RawMessage(bad)); err == nil {
			t.Errorf("%s was taken", bad)
		}
	}
	for _, good := range []string{
		`{"db":{},"user":{}}`,
		`{"downloads":true,"room":{}}`,
		`{"db":{"rules":[{"path":"signoffs","read":"view","write":"self"},{"path":"signoffs/{self}","read":"view","write":"interact"}]}}`,
		`{"user":{"scopes":["profile","email"]}}`,
		`{"room":{"topics":{"move":"interact","chat.say":"admin"}}}`,
		`{"room":{"topics":{` + manyTopics(64) + `}}}`,
	} {
		if _, err := ParseCapabilities(json.RawMessage(good)); err != nil {
			t.Errorf("%s: %v", good, err)
		}
	}
	if c, err := ParseCapabilities(nil); c != nil || err != nil {
		t.Errorf("none given: %v %v", c, err)
	}
	if c, err := ParseCapabilities(json.RawMessage(`{}`)); c == nil || len(c) != 0 || err != nil {
		t.Errorf("{}: %v %v", c, err)
	}
}

// manyTopics is n room topics opened to Contributors, as a declaration's
// object members.
func manyTopics(n int) string {
	t := make([]string, n)
	for i := range t {
		t[i] = fmt.Sprintf(`"t%d":"interact"`, i)
	}
	return strings.Join(t, ",")
}

// A publish that leaves capabilities out keeps the version before's; {}
// clears them.
func TestCapabilitiesCarryForward(t *testing.T) {
	a, _ := withDB(t, `{"db":{},"user":{}}`)
	b, err := PublishPage(Publish{ID: a.ID, Page: []byte("<p>2"), Base: 1})
	if err != nil {
		t.Fatal(err)
	}
	if v := b.Latest(); !v.Capabilities.Has("db") || !v.Capabilities.Has("user") {
		t.Fatalf("version 2 declares %v", v.Capabilities)
	}
	c, err := PublishPage(Publish{ID: a.ID, Page: []byte("<p>3"), Base: 2, Capabilities: Capabilities{}})
	if err != nil {
		t.Fatal(err)
	}
	if v := c.Latest(); len(v.Capabilities) != 0 {
		t.Fatalf("version 3 declares %v after {}", v.Capabilities)
	}
	// it is kept with the version, on disk
	again, err := Get(a.ID)
	if err != nil || !again.Version(1).Capabilities.Has("db") || again.Version(3).Capabilities.Has("db") {
		t.Fatalf("read again: %+v %v", again, err)
	}
}

func TestDBQuotas(t *testing.T) {
	_, db := withDB(t, `{"db":{}}`)
	wasDocs, wasBytes := maxDocs, maxDataBytes
	t.Cleanup(func() { maxDocs, maxDataBytes = wasDocs, wasBytes })
	maxDocs = 2
	db.Set("a/1", map[string]any{})
	db.Set("a/2", map[string]any{})
	if err := db.Set("a/3", map[string]any{}); code(err) != "quota_exceeded" {
		t.Fatalf("a third document: %v", err)
	}
	if err := db.Set("a/2", map[string]any{"rewritten": true}); err != nil {
		t.Fatalf("rewriting one at the count: %v", err)
	}
	if _, err := db.Acquire("a/3", "h", 0, nil); code(err) != "quota_exceeded" {
		t.Fatalf("a lease making a third: %v", err)
	}
	db.Delete("a/1")
	if err := db.Set("a/3", map[string]any{}); err != nil {
		t.Fatalf("after a delete: %v", err)
	}
	maxDocs = 100
	maxDataBytes = 100
	if err := db.Set("a/4", map[string]any{"s": strings.Repeat("x", 100)}); code(err) != "quota_exceeded" {
		t.Fatalf("over the bytes: %v", err)
	}
	// the totals follow every write, deletes included
	docs, size, err := Stats(db.id)
	if err != nil || docs != 2 || size != int64(len(`{"rewritten":true}`)+len(`{}`)) {
		t.Fatalf("stats %d %d %v", docs, size, err)
	}
}

func TestDBAcquire(t *testing.T) {
	_, db := withDB(t, `{"db":{}}`)
	l, err := db.Acquire("locks/turn", "alice", 5*time.Second, map[string]any{"by": "alice"})
	if err != nil || !l.Acquired || l.Holder != "alice" || l.Version != 1 || time.Until(l.ExpiresAt) > 5*time.Second || time.Until(l.ExpiresAt) < 4*time.Second {
		t.Fatalf("alice: %+v %v", l, err)
	}
	l2, err := db.Acquire("locks/turn", "bob", time.Second, map[string]any{"by": "bob"})
	if err != nil || l2.Acquired || !l2.ExpiresAt.Equal(l.ExpiresAt) {
		t.Fatalf("bob, while alice holds it: %+v %v", l2, err)
	}
	if d, _ := db.Get("locks/turn"); d.Data["by"] != "alice" {
		t.Fatalf("bob's refused lease wrote %v", d.Data)
	}
	if l3, _ := db.Acquire("locks/turn", "alice", time.Hour, nil); !l3.Acquired || time.Until(l3.ExpiresAt) > 10*time.Minute {
		t.Fatalf("alice again, an hour clamped: %+v", l3)
	}
	if _, err := db.Acquire("locks/turn", "", 0, nil); code(err) != "invalid_argument" {
		t.Errorf("no holder: %v", err)
	}
}

// A write is told to whoever watches the artifact's store, by path.
func TestDBWatch(t *testing.T) {
	_, db := withDB(t, `{"db":{}}`)
	ch, stop := Watch(db.id)
	defer stop()
	db.Set("a/1", map[string]any{})
	select {
	case c := <-ch:
		if len(c.Paths) != 1 || c.Paths[0] != "a/1" {
			t.Fatalf("told %+v", c)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a write wasn't told")
	}
}

// The store goes with its artifact, and isn't made again for one gone.
func TestDBGoesWithArtifact(t *testing.T) {
	a, db := withDB(t, `{"db":{}}`)
	if err := db.Set("a/1", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(folder(a.ID), dataName)
	if fi, err := os.Stat(p); err != nil {
		t.Fatal(err)
	} else if fi.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Errorf("data.db is %v", fi.Mode().Perm())
	}
	if err := Delete(a.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.Set("a/2", map[string]any{}); err == nil {
		t.Fatal("a write after the artifact went")
	}
	if _, err := os.Stat(folder(a.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the folder: %v", err)
	}
	if _, err := OpenDB(a.ID, 0, Viewer{Agent: true}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("OpenDB after: %v", err)
	}
}

// A page's token opens its own store alone, and the key it is made with
// is no artifact.
func TestPageToken(t *testing.T) {
	sandbox(t)
	a, _ := PublishPage(Publish{Page: []byte("<p>a")})
	b, _ := PublishPage(Publish{Page: []byte("<p>b")})
	ta, err := PageToken(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !PageTokenOK(a.ID, ta) || PageTokenOK(b.ID, ta) || PageTokenOK(a.ID, "") || PageTokenOK(a.ID, ta[:len(ta)-1]) {
		t.Fatal("a token opens what it shouldn't, or not its own")
	}
	if all, err := List(); err != nil || len(all) != 2 {
		t.Fatalf("List: %d %v", len(all), err)
	}
	if fi, err := os.Stat(filepath.Join(Dir(), secretName)); err != nil {
		t.Fatal(err)
	} else if fi.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Errorf(".secret is %v", fi.Mode().Perm())
	}
}

// The runtime goes in ahead of the page's own scripts, and nothing in it
// or its config ends its <script> early.
func TestWithRuntime(t *testing.T) {
	if strings.Contains(strings.ToLower(string(runtimeJS)), "</script") || strings.Contains(string(runtimeJS), "<!--") {
		t.Fatal("runtime.js would end or confuse its <script>")
	}
	c := &RuntimeConfig{API: "/a/x/_api", Token: "</script><script>alert(1)</script>", Caps: map[string]any{}}
	for _, c2 := range []struct{ page, before string }{
		{"<!doctype html><html lang=en><head><meta charset=utf-8><script>mine()</script>", "<script>mine()"},
		{"<!doctype html><html><body><script>mine()</script>", "<body>"},
		{"<!doctype html><p>hi<script>mine()</script>", "<p>hi"},
		{"<header><script>mine()</script>", "<header>"},
	} {
		out := string(WithRuntime([]byte(c2.page), c))
		rt := strings.Index(out, "<script>"+string(runtimeJS[:40]))
		if rt < 0 || rt > strings.Index(out, c2.before) {
			t.Errorf("%s: the runtime isn't ahead of %s:\n%.200s", c2.page, c2.before, out)
		}
		if strings.Count(out, "</script>") != strings.Count(c2.page, "</script>")+1 {
			t.Errorf("%s: the config ends its script early", c2.page)
		}
	}
	shared := string(WithRuntime([]byte("<p>x"), SharedRuntime(nil)))
	if !strings.Contains(shared, `"shared":true`) || strings.Contains(shared, `"token":"`+"x") {
		t.Errorf("the shared runtime: %.300s", shared[len(shared)-300:])
	}
}

func ExampleCheckPath() {
	_, err := CheckPath("tasks", true)
	fmt.Println(err)
	// Output: invalid_argument: "tasks" has 1 segments, so it is a collection: a document's path has an even number
}
