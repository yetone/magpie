package sessions

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// testdata/antigravity-2.30-tools is the Antigravity app's data folder
// after one conversation its 2.30 language server had in a sandbox HOME on
// Windows, against a fake model that called read_file once: the
// conversation's database (its steps' text encrypted), its summary row, and
// brain/<id>/.system_generated/logs/transcript_full.jsonl, the steps in
// plain text.
const agAppTools = "a9b3e500-3594-49ed-b821-3418225e1990"

// agAppToolsParts are what that conversation said.
var agAppToolsParts = []Part{
	{Role: "user", Kind: "text", Text: "read a.txt"},
	{Role: "assistant", Kind: "text", Text: "Let me read it."},
	{Role: "assistant", Kind: "tool_use", Name: "read_file", Text: `{"Path":"C:\\Users\\yeton\\agyss\\e2e4\\home\\proj\\a.txt"}`},
	{Role: "tool", Kind: "tool_result", Text: "Showing lines 1-1 of 1\n1: hi"},
	{Role: "assistant", Kind: "text", Text: "a.txt says hi."},
}

func wantAgAppTools(t *testing.T, s Session) {
	t.Helper()
	if !s.Transcript {
		t.Fatalf("the session isn't marked readable: %+v", s)
	}
	tr, err := TranscriptOf(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Parts) != len(agAppToolsParts) {
		t.Fatalf("parts %+v", tr.Parts)
	}
	for i, p := range agAppToolsParts {
		if tr.Parts[i] != p {
			t.Errorf("part %d %+v, want %+v", i, tr.Parts[i], p)
		}
	}
}

// @yansheng1003 on X: an Antigravity 2.0 conversation showed only its
// title and preview on the Sessions page, not what was said. The app's
// transcript beside the database is read as agy's is: the user's turn,
// the replies, the tool call and its result; found by search, and in the
// Markdown export. Stable's, Insiders' and a WSL distro's.
func TestAntigravityAppTranscript(t *testing.T) {
	t.Run("stable", func(t *testing.T) {
		claude, _ := setup(t)
		home := filepath.Join(filepath.Dir(claude), "home")
		copyTree(t, "testdata/antigravity-2.30-tools", filepath.Join(home, ".gemini", "antigravity"))
		Reset()
		s := find(t, List(0), "antigravity", agAppTools)
		if s.Title != "a.txt says hi." {
			t.Errorf("title %q", s.Title)
		}
		wantAgAppTools(t, s)
		if _, found, err := SearchSession(context.Background(), s, NeedleOf("says hi")); err != nil || !found {
			t.Errorf("search: found %v, %v", found, err)
		}
		if _, found, _ := SearchSession(context.Background(), s, NeedleOf("never said")); found {
			t.Error("search found words never said")
		}
		var b strings.Builder
		if err := WriteMarkdown(&b, s, "Antigravity"); err != nil {
			t.Fatal(err)
		}
		for _, w := range []string{"read a.txt", "read_file", "a.txt says hi."} {
			if !strings.Contains(b.String(), w) {
				t.Errorf("markdown has no %q:\n%s", w, b.String())
			}
		}
	})
	t.Run("insiders", func(t *testing.T) {
		claude, _ := setup(t)
		home := filepath.Join(filepath.Dir(claude), "home")
		copyTree(t, "testdata/antigravity-2.30-tools", filepath.Join(home, ".gemini", "antigravity-insiders"))
		Reset()
		wantAgAppTools(t, find(t, List(0), "antigravity", "insiders-"+agAppTools))
	})
	t.Run("wsl", func(t *testing.T) {
		home, _ := wslDistro(t)
		copyTree(t, "testdata/antigravity-2.30-tools", filepath.Join(home, ".gemini", "antigravity"))
		s := find(t, List(0), "antigravity", agAppTools)
		if s.WSL == "" {
			t.Fatalf("not the distro's: %+v", s)
		}
		wantAgAppTools(t, s)
	})
}

// A conversation the app wrote no transcript for (antigravityFixture's,
// from #2190's rows) can't be shown, as before.
func TestAntigravityAppNoTranscript(t *testing.T) {
	claude, _ := setup(t)
	home := filepath.Join(filepath.Dir(claude), "home")
	const id = "37c3c786-9a11-44be-97df-b8fc1ccebc78"
	antigravityFixture(t, home, id)
	Reset()
	if _, err := TranscriptOf(find(t, List(0), "antigravity", id)); !errors.Is(err, ErrNoTranscript) {
		t.Fatalf("err %v, want ErrNoTranscript", err)
	}
}
