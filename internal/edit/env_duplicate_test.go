package edit

import "testing"

// A dotenv key that appears twice takes its last value (Gemini CLI reads the
// file with dotenv, where a later line overrides an earlier one), so reads
// return the last line and a write replaces it, rather than the first.

func TestGetEnvFileLastDuplicateWins(t *testing.T) {
	p := tmpFile(t, ".env", "A=1\nB=x\nA=2\n")
	if v, ok := GetEnvFile(p, "A"); !ok || v != "2" {
		t.Fatalf("A = %q, %v; want 2", v, ok)
	}
}

func TestSetEnvFileReplacesLastDuplicate(t *testing.T) {
	p := tmpFile(t, ".env", "A=1\nB=x\nA=2\n")
	if err := SetEnvFile(p, KV{Path: "A", Value: "3"}); err != nil {
		t.Fatal(err)
	}
	if got, want := read(t, p), "A=1\nB=x\nA=3\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if v, _ := GetEnvFile(p, "A"); v != "3" {
		t.Fatalf("A reads %q after setting 3", v)
	}
}
