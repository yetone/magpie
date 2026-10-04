package edit

import "testing"

// A YAML file that uses \r\n for every line break keeps it when SetYAML,
// DelYAML or EditYAMLStrings rewrite it: the encoder writes \n, which turned
// the whole file into LF.

func TestSetYAMLKeepsCRLF(t *testing.T) {
	p := tmpFile(t, "config.yaml", "a: 1\r\nb:\r\n  c: 2\r\n")
	if err := SetYAML(p, KV{Path: "b.c", Value: 3}, KV{Path: "d", Value: "4"}); err != nil {
		t.Fatal(err)
	}
	if got, want := read(t, p), "a: 1\r\nb:\r\n  c: 3\r\nd: \"4\"\r\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if err := DelYAML(p, "a"); err != nil {
		t.Fatal(err)
	}
	if got, want := read(t, p), "b:\r\n  c: 3\r\nd: \"4\"\r\n"; got != want {
		t.Fatalf("after delete got %q, want %q", got, want)
	}
}

func TestSetYAMLKeepsLF(t *testing.T) {
	p := tmpFile(t, "config.yaml", "a: 1\nb: 2\n")
	if err := SetYAML(p, KV{Path: "c", Value: 3}); err != nil {
		t.Fatal(err)
	}
	if got, want := read(t, p), "a: 1\nb: 2\nc: 3\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
