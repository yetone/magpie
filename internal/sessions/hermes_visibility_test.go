package sessions

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestHermesLiteralJSONText(t *testing.T) {
	for _, text := range []string{"[]", "null", `[{"name":"item"}]`, `[{"type":"text","text":"literal"}]`} {
		if got := hermesText(text); got != text {
			t.Fatalf("literal %q became %q", text, got)
		}
	}
	if got := hermesText("\x00json:" + `[{"type":"text","text":"encoded"}]`); got != "encoded" {
		t.Fatal(got)
	}
}

func TestHermesMessageVisibility(t *testing.T) {
	setup(t)
	root := filepath.Join(t.TempDir(), "hermes")
	t.Setenv("HERMES_HOME", root)
	path := hermesFixture(t, root, "", "visibility", false)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`ALTER TABLE messages ADD COLUMN active INTEGER DEFAULT 1;
 ALTER TABLE messages ADD COLUMN compacted INTEGER DEFAULT 0;
 UPDATE messages SET active=0 WHERE id=2;
 UPDATE messages SET active=0, compacted=1 WHERE id=1;`)
	if err != nil {
		t.Fatal(err)
	}
	h := newHermesDB(db)
	var texts []string
	err = h.eachMessage(db, "visibility", func(m hermesMessage) { texts = append(texts, hermesText(m.content)) })
	if err != nil {
		t.Fatal(err)
	}
	if len(texts) != 3 || texts[0] != "Please inspect this" {
		t.Fatalf("visible messages: %v", texts)
	}
}
