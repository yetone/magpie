package sessions

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// Markdown export must use the same Reasonix reader as the conversation
// page, including native stores rather than only the legacy JSONL layout.
func TestMarkdownReasonix(t *testing.T) {
	for _, codec := range []string{"legacy", "linear-v3", "linear-v3.1", "events-v3", "linear-v4"} {
		t.Run(codec, func(t *testing.T) {
			var legacyPath string
			if codec == "legacy" {
				legacyPath, _ = reasonixFixture(t)
			} else {
				copyReasonixStore(t, codec, "sessions-v99")
			}
			s := reasonixOnly(t)
			var before []byte
			if legacyPath != "" {
				before, _ = os.ReadFile(legacyPath)
			}
			tr, err := TranscriptOf(s)
			if err != nil {
				t.Fatal(err)
			}
			var b bytes.Buffer
			if err := WriteMarkdown(&b, s, "Reasonix Studio"); err != nil {
				t.Fatal(err)
			}
			md := b.String()
			if !strings.Contains(md, "**Agent:** Reasonix Studio") || !strings.Contains(md, "## User") || !strings.Contains(md, "## Assistant") {
				t.Fatal("export lacks conversation headings")
			}
			for _, p := range tr.Parts {
				if p.Text != "" && !strings.Contains(md, p.Text) {
					t.Fatalf("export lost %s content", p.Kind)
				}
			}
			if legacyPath != "" {
				after, _ := os.ReadFile(legacyPath)
				if !bytes.Equal(before, after) {
					t.Fatal("export changed source conversation")
				}
			}
		})
	}
}
