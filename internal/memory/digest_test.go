package memory

import (
	"os"
	"strings"
	"testing"
	"time"
)

// What agents save reaches the digest when it is published, never at the
// save itself, so an agent's instructions don't change mid-session.
func TestDigestChangesOnlyWhenPublished(t *testing.T) {
	at := fresh(t)
	save(t, "Answer in Chinese", Preference, Global)
	if d, _ := Digest("codex"); d != "" {
		t.Fatalf("unpublished digest %q", d)
	}
	if changed, err := Publish(); err != nil || !changed {
		t.Fatalf("publish: %v, %v", changed, err)
	}
	d, _ := Digest("codex")
	if !strings.Contains(d, "Preferences:\n- Answer in Chinese") {
		t.Fatalf("digest %q", d)
	}
	save(t, "Keep commits small", Rule, Global)
	if d2, _ := Digest("codex"); d2 != d {
		t.Fatal("a save changed the digest")
	}
	*at = at.Add(time.Hour)
	if changed, _ := PublishDue(); changed {
		t.Fatal("published again within a day")
	}
	*at = at.Add(24 * time.Hour)
	if changed, _ := PublishDue(); !changed {
		t.Fatal("not published after a day")
	}
	if d, _ := Digest("codex"); !strings.Contains(d, "Rules:\n- Keep commits small") {
		t.Fatalf("digest after a day %q", d)
	}
	if changed, _ := Publish(); changed {
		t.Fatal("an unchanged publish said it changed")
	}
}

func TestDigestPartsAndBudget(t *testing.T) {
	at := fresh(t)
	old := globalBudget
	globalBudget = 120
	t.Cleanup(func() { globalBudget = old })

	save(t, strings.Repeat("a", 60), Fact, Global)
	*at = at.Add(time.Minute)
	save(t, strings.Repeat("b", 60), Fact, Global)
	pin := save(t, "pinned one", Episode, Global)
	tr := true
	if _, err := Edit(pin.ID, Input{Pinned: &tr}, true); err != nil {
		t.Fatal(err)
	}
	save(t, "only codex sees this", Rule, "agent:codex")
	p := &Project{Key: "github.com/yetone/magpie", Name: "magpie", Roots: []string{"/w/magpie"}}
	if _, _, err := Save(Input{Text: str("tests run on mote"), Kind: str(Rule), Scope: str(ScopeOf(p))}, FromAgent, nil, p); err != nil {
		t.Fatal(err)
	}
	gone := save(t, "archived one", Rule, Global)
	if _, err := Forget(gone.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := Publish(); err != nil {
		t.Fatal(err)
	}
	d, _ := Digest("codex")
	for _, want := range []string{"pinned one", strings.Repeat("b", 60), "Only for you:\n" + "Rules:\n- only codex sees this", "When working in /w/magpie:\nRules:\n- tests run on mote"} {
		if !strings.Contains(d, want) {
			t.Errorf("digest lacks %q:\n%s", want, d)
		}
	}
	// over budget: the older of the two long ones is left out, whole
	for _, not := range []string{strings.Repeat("a", 60), "archived one"} {
		if strings.Contains(d, not) {
			t.Errorf("digest has %q:\n%s", not, d)
		}
	}
	if other, _ := Digest("claude"); strings.Contains(other, "only codex") {
		t.Errorf("another agent got codex's own:\n%s", other)
	}
	for _, name := range []string{"magpie", "Magpie", "OpenAI", "Anthropic"} {
		if strings.Contains(strings.SplitN(d, "\n\n", 3)[1], name) {
			t.Errorf("the digest's header names %s", name)
		}
	}
}

// The digest defers to the project's own instruction files: a memory that
// disagrees with them loses, and a project's rules are pointed to its
// files (flying_crp on X: memory and AGENTS.md said opposite things, and
// a project's rule went into memory). The header names neither this app
// nor a vendor.
func TestDigestDefersToTheProjectsFiles(t *testing.T) {
	fresh(t)
	save(t, "Answer in Chinese", Preference, Global)
	if _, err := Publish(); err != nil {
		t.Fatal(err)
	}
	d, _ := Digest("codex")
	head := strings.SplitN(d, "\n\n", 3)[1]
	for _, want := range []string{
		"The project's own instruction files (AGENTS.md and the like) and docs come first",
		"where a memory disagrees with them, follow the project's files",
		"A rule or fact of one project belongs in that project's files",
		"memory keeps what holds for the user across projects",
	} {
		if !strings.Contains(head, want) {
			t.Errorf("the header lacks %q:\n%s", want, head)
		}
	}
	for _, name := range []string{"magpie", "claude", "codex", "openai", "anthropic", "gemini"} {
		if strings.Contains(strings.ToLower(head), name) {
			t.Errorf("the header names %s", name)
		}
	}
}

// A memory the user writes in several lines keeps them: on save, on an
// edit, in its file, and in the digest, where the lines after the first
// are indented under its item (_meouwu on X: the line breaks were gone).
func TestMultiLineMemoryKeepsItsLines(t *testing.T) {
	fresh(t)
	text := "Release steps:\n1. tag the commit\n2. push the tag by name\n\n   - never --tags"
	m, _, err := Save(Input{Text: str(text + "\n"), Kind: str(Procedure), Scope: str(Global)}, FromUser, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m.Text != text {
		t.Fatalf("saved %q, want %q", m.Text, text)
	}
	text += "\n3. wait for the assets"
	if _, err := Edit(m.ID, Input{Text: str(text)}, true); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(in(m.ID + ".md"))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := decode(m.ID, b, time.Now()); err != nil || got.Text != text {
		t.Fatalf("the file reads %q (%v), want %q", got.Text, err, text)
	}
	if got, _ := Get(m.ID); got.Text != text {
		t.Fatalf("Get %q, want %q", got.Text, text)
	}
	if _, err := Publish(); err != nil {
		t.Fatal(err)
	}
	d, _ := Digest("codex")
	want := "How things are done:\n- Release steps:\n  1. tag the commit\n  2. push the tag by name\n\n     - never --tags\n  3. wait for the assets"
	if !strings.Contains(d, want) {
		t.Fatalf("digest %q, want it to hold %q", d, want)
	}
}

func TestIndented(t *testing.T) {
	for in, want := range map[string]string{
		"one line":                  "one line",
		"  a\r\nb  \n\n\tc\n":       "a\n> b\n\n> \tc",
		"a \t\n   \nb":              "a\n\n> b",
		"\n\nonly after blanks\n\n": "only after blanks",
	} {
		if got := Indented(in, "> "); got != want {
			t.Errorf("Indented(%q) = %q, want %q", in, got, want)
		}
	}
}

// #7780: SetDigestOmitWhy omits "(why: ...)" rationale suffixes from the
// published digest (freeing budget room for more entries) while preserving
// stored Why fields; turning it back off restores the suffixes.
func TestDigestOmitWhy(t *testing.T) {
	at := fresh(t)
	old := globalBudget
	globalBudget = 80
	t.Cleanup(func() { globalBudget = old })

	why1 := strings.Repeat("reason ", 8)
	m1, _, err := Save(Input{Text: str("Use pnpm"), Kind: str(Rule), Scope: str(Global), Why: str(why1)}, FromUser, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	*at = at.Add(-time.Minute) // older, so it only fits once why1 is omitted
	if _, _, err := Save(Input{Text: str("Keep commits small"), Kind: str(Rule), Scope: str(Global), Why: str("easier review")}, FromUser, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := Publish(); err != nil {
		t.Fatal(err)
	}
	if omit, err := DigestOmitWhy(); err != nil || omit {
		t.Fatalf("default DigestOmitWhy = %v, %v; want false", omit, err)
	}
	d, err := Digest("codex")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d, "(why: ") || strings.Contains(d, "Keep commits small") {
		t.Fatalf("with why on, expected why suffix and budget overflow:\n%s", d)
	}

	changed, err := SetDigestOmitWhy(true)
	if err != nil || !changed {
		t.Fatalf("SetDigestOmitWhy(true): changed=%v err=%v", changed, err)
	}
	if omit, err := DigestOmitWhy(); err != nil || !omit {
		t.Fatalf("DigestOmitWhy after true = %v, %v", omit, err)
	}
	d, err = Digest("codex")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(d, "(why:") {
		t.Fatalf("digest still contains (why:):\n%s", d)
	}
	if !strings.Contains(d, "- Use pnpm") || !strings.Contains(d, "- Keep commits small") {
		t.Fatalf("omitting why should fit both entries within budget:\n%s", d)
	}
	// stored Why remains intact on disk and in All()/Get()
	if got, err := Get(m1.ID); err != nil || got.Why != strings.TrimSpace(why1) {
		t.Fatalf("stored Why lost: got %q (%v)", got.Why, err)
	}
	// subsequent Publish keeps the setting
	if changed, err := Publish(); err != nil || changed {
		t.Fatalf("re-publish after omitWhy: changed=%v err=%v", changed, err)
	}
	// toggling back off restores "(why: ...)"
	changed, err = SetDigestOmitWhy(false)
	if err != nil || !changed {
		t.Fatalf("SetDigestOmitWhy(false): changed=%v err=%v", changed, err)
	}
	if d, err = Digest("codex"); err != nil || !strings.Contains(d, "(why: ") {
		t.Fatalf("turning omitWhy off did not restore why suffix:\n%s (%v)", d, err)
	}
}
