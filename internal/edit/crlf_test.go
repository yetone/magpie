package edit

import "testing"

// A file that uses \r\n for every line break keeps it when a line is added,
// replaced or removed: Windows editors save dotenv, TOML and YAML configs that
// way, and a lone \n in the middle of one shows up as a changed line in diffs
// and trips tools that read the file line by line.

func TestSetEnvFileKeepsCRLF(t *testing.T) {
	p := tmpFile(t, ".env", "A=1\r\nB=2\r\n")
	if err := SetEnvFile(p, KV{Path: "A", Value: "9"}, KV{Path: "C", Value: "3"}); err != nil {
		t.Fatal(err)
	}
	if got, want := read(t, p), "A=9\r\nB=2\r\nC=3\r\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if err := DelEnvFile(p, "B"); err != nil {
		t.Fatal(err)
	}
	if got, want := read(t, p), "A=9\r\nC=3\r\n"; got != want {
		t.Fatalf("after delete got %q, want %q", got, want)
	}
}

func TestSetEnvFileKeepsMixedEndings(t *testing.T) {
	p := tmpFile(t, ".env", "A=1\r\nB=2\n")
	if err := SetEnvFile(p, KV{Path: "C", Value: "3"}); err != nil {
		t.Fatal(err)
	}
	if got, want := read(t, p), "A=1\r\nB=2\nC=3\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSetTOMLKeepsCRLF(t *testing.T) {
	p := tmpFile(t, "config.toml", "top = 1\r\n[a]\r\nk = \"v\"\r\n[b]\r\nz = 2\r\n")
	if err := SetTOMLTop(p, KV{Path: "top", Value: 9}, KV{Path: "t2", Value: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := SetTOMLKey(p, "a", "n", "added"); err != nil {
		t.Fatal(err)
	}
	if err := SetTOMLTable(p, "c", KV{Path: "q", Value: "1"}); err != nil {
		t.Fatal(err)
	}
	if err := DelTOMLKey(p, "b", "z"); err != nil {
		t.Fatal(err)
	}
	want := "top = \"9\"\r\nt2 = \"x\"\r\n[a]\r\nk = \"v\"\r\nn = \"added\"\r\n\r\n[c]\r\nq = \"1\"\r\n"
	if got := read(t, p); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSetYAMLTopKeepsCRLF(t *testing.T) {
	p := tmpFile(t, "config.yaml", "A: 1\r\nB: 2\r\n")
	if err := SetYAMLTop(p, KV{Path: "A", Value: "9"}, KV{Path: "C", Value: "3"}); err != nil {
		t.Fatal(err)
	}
	got := read(t, p)
	for i := 0; i < len(got); i++ {
		if got[i] == '\n' && (i == 0 || got[i-1] != '\r') {
			t.Fatalf("bare \\n at %d in %q", i, got)
		}
	}
	if err := DelYAMLTop(p, "B"); err != nil {
		t.Fatal(err)
	}
	got = read(t, p)
	for i := 0; i < len(got); i++ {
		if got[i] == '\n' && (i == 0 || got[i-1] != '\r') {
			t.Fatalf("after delete: bare \\n at %d in %q", i, got)
		}
	}
}
