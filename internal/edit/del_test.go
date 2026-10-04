package edit

import (
	"os"
	"testing"
)

func TestDelJSONMiddleKeepsLayout(t *testing.T) {
	in := "{\n  \"env\": {\n    \"A\": \"1\",\n    \"B\": \"2\",\n    \"C\": \"3\"\n  },\n  \"model\": \"m\"\n}\n"
	p := tmpFile(t, "s.json", in)
	if err := DelJSON(p, "env.B"); err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"env\": {\n    \"A\": \"1\",\n    \"C\": \"3\"\n  },\n  \"model\": \"m\"\n}\n"
	if got := read(t, p); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	// last member: preceding comma goes
	if err := DelJSON(p, "env.C", "nope.x"); err != nil {
		t.Fatal(err)
	}
	want = "{\n  \"env\": {\n    \"A\": \"1\"\n  },\n  \"model\": \"m\"\n}\n"
	if got := read(t, p); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	// only member: parent collapses to {}
	if err := DelJSON(p, "env.A"); err != nil {
		t.Fatal(err)
	}
	want = "{\n  \"env\": {},\n  \"model\": \"m\"\n}\n"
	if got := read(t, p); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	// top-level last member
	if err := DelJSON(p, "model"); err != nil {
		t.Fatal(err)
	}
	want = "{\n  \"env\": {}\n}\n"
	if got := read(t, p); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestDelJSONCompactAndComments(t *testing.T) {
	p := tmpFile(t, "c.jsonc", "{\"a\":1,\"b\":{\"x\":true},\"c\":3}")
	if err := DelJSON(p, "a", "b.x"); err != nil {
		t.Fatal(err)
	}
	if got := read(t, p); got != "{\"b\":{},\"c\":3}" {
		t.Fatalf("got %q", got)
	}
	p = tmpFile(t, "d.jsonc", "{\n  // keep me\n  \"a\": 1, // trailing\n  \"b\": 2\n}\n")
	if err := DelJSON(p, "b"); err != nil {
		t.Fatal(err)
	}
	if got := read(t, p); got != "{\n  // keep me\n  \"a\": 1 // trailing\n}\n" {
		t.Fatalf("got %q", got)
	}
}

func TestEnvFile(t *testing.T) {
	p := tmpFile(t, ".env", "# comment\nFOO=bar\nexport BAZ=\"q x\"\n")
	if v, ok := GetEnvFile(p, "BAZ"); !ok || v != "q x" {
		t.Fatalf("got %q %v", v, ok)
	}
	if err := SetEnvFile(p, KV{"FOO", "new"}, KV{"KEY", "sk-1"}); err != nil {
		t.Fatal(err)
	}
	if got := read(t, p); got != "# comment\nFOO=new\nexport BAZ=\"q x\"\nKEY=sk-1\n" {
		t.Fatalf("got %q", got)
	}
	if err := DelEnvFile(p, "FOO", "KEY"); err != nil {
		t.Fatal(err)
	}
	if got := read(t, p); got != "# comment\nexport BAZ=\"q x\"\n" {
		t.Fatalf("got %q", got)
	}
	// the keys all it held: the file goes, not a blank line left
	only := tmpFile(t, "only.env", "GEMINI_API_KEY=magpie\nGOOGLE_GEMINI_BASE_URL=http://127.0.0.1:3425\n")
	if err := DelEnvFile(only, "GEMINI_API_KEY", "GOOGLE_GEMINI_BASE_URL"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(only); !os.IsNotExist(err) {
		t.Fatalf("left %q", read(t, only))
	}
	np := tmpFile(t, "new.env", "")
	if err := SetEnvFile(np, KV{"A", "1"}); err != nil {
		t.Fatal(err)
	}
	if got := read(t, np); got != "A=1\n" {
		t.Fatalf("got %q", got)
	}
}
