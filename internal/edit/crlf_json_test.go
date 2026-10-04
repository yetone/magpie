package edit

import "testing"

// A JSON file that uses \r\n for every line break keeps it when a member is
// added or replaced: the lines an edit adds used to carry a bare \n, so the
// file ended up with mixed endings.

func TestSetJSONKeepsCRLF(t *testing.T) {
	p := tmpFile(t, "settings.json", "{\r\n  \"a\": 1,\r\n  \"n\": {\r\n    \"x\": 1\r\n  }\r\n}\r\n")
	if err := SetJSON(p, KV{Path: "b", Value: 2}, KV{Path: "n.y", Value: []string{"p"}}); err != nil {
		t.Fatal(err)
	}
	want := "{\r\n  \"b\": 2,\r\n  \"a\": 1,\r\n  \"n\": {\r\n    \"y\": [\r\n      \"p\"\r\n    ],\r\n    \"x\": 1\r\n  }\r\n}\r\n"
	if got := read(t, p); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSetJSONKeepsMixedEndings(t *testing.T) {
	p := tmpFile(t, "settings.json", "{\r\n  \"a\": 1\n}\n")
	if err := SetJSON(p, KV{Path: "b", Value: 2}); err != nil {
		t.Fatal(err)
	}
	if got, want := read(t, p), "{\n  \"b\": 2,\r\n  \"a\": 1\n}\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
