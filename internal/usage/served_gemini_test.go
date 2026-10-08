package usage

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// Antigravity answers every level of gemini-3.8-flash with modelVersion
// gemini-3.8-flash-n (dumplings on Discord; seen on a real account for
// gemini-3.8-flash-low, -medium and -high, with and without a thinking
// level): Google's name for the deployment serving that model, not another
// model. Another vendor's swap, another Gemini model and another vendor's
// "-n" are still swaps.
func TestSwappedGeminiServingName(t *testing.T) {
	for _, c := range [][2]string{
		{"gemini-3.8-flash", "gemini-3.8-flash-n"},
		{"antigravity/gemini-3.8-flash", "gemini-3.8-flash-n"},
		{"gemini-3.8-flash-low", "gemini-3.8-flash-n"},
		{"gemini-3.8-flash-medium", "gemini-3.8-flash-n"},
		{"gemini-3.8-flash-high", "gemini-3.8-flash-n"},
		{"gemini-3.8-flash", "models/gemini-3.8-flash-n"},
	} {
		if Swapped(c[0], c[1]) {
			t.Errorf("%s answered as %s is the model asked for", c[0], c[1])
		}
	}
	for _, c := range [][2]string{
		{"gpt-5", "gpt-5-mini"},
		{"openai/gpt-5", "gpt-5-mini-2025-08-07"},
		{"gpt-5", "gpt-5-n"},
		{"claude-sonnet-4-5", "claude-sonnet-4-5-n"},
		{"gemini-3.8-flash", "gemini-3.8-flash-lite"},
		{"gemini-3.8-flash", "gemini-3.8-flash-lite-n"},
		{"gemini-3.8-flash", "gemini-3.7-flash-n"},
		{"gemini-3.8-flash", "gemini-3.8-flash-nano"},
		{"gemini-3.8-flash-high", "gemini-3.8-flash-low"},
	} {
		if !Swapped(c[0], c[1]) {
			t.Errorf("%s answered as %s is another model", c[0], c[1])
		}
	}
}

// The requests log reads an Antigravity record of gemini-3.8-flash answered
// as gemini-3.8-flash-n as not swapped, at every level it went out at; a
// row of gpt-5 answered by gpt-5-mini is still marked.
func TestLedgerGeminiServingNameIsNoSwap(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	// Antigravity's own list on 2026-10-08 for the 3.8 Flash family
	raw := []catalog.Model{
		{ID: "gemini-3.8-flash-high", Name: "Gemini 3.8 Flash (High)"},
		{ID: "gemini-3.8-flash-low", Name: "Gemini 3.8 Flash (Low)"},
		{ID: "gemini-3.8-flash-medium", Name: "Gemini 3.8 Flash (Medium)"},
		{ID: "gemini-3.8-flash-tiered"},
	}
	if err := catalog.SaveLive("antigravity", "", raw); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	os.MkdirAll(filepath.Dir(Path()), 0o755)
	now := time.Now()
	Append(Record{Time: now, Provider: "openai", Host: "api.openai.com", Model: "gpt-5",
		Served: "gpt-5-mini-2025-08-07", Input: 10, Output: 1, Status: 200})
	for i, effort := range []string{"", "low", "medium", "high"} {
		Append(Record{Time: now.Add(time.Duration(i+1) * time.Minute), Provider: "antigravity",
			Host: "daily-cloudcode-pa.googleapis.com", Model: "gemini-3.8-flash", Effort: effort,
			Served: "gemini-3.8-flash-n", Input: 10, Output: 1, Status: 200})
	}
	rows, _, _ := Ledger(All, Filter{})
	page := QueryPage(All, Filter{}, 0, 100)
	for _, got := range [][]Row{rows, page.Rows} {
		if len(got) != 5 {
			t.Fatalf("rows %d: %+v", len(got), got)
		}
		for _, row := range got {
			if row.Model == "gpt-5" && !row.Swapped {
				t.Errorf("gpt-5 answered by gpt-5-mini is a swap: %+v", row)
			}
			if row.Model == "gemini-3.8-flash" && row.Swapped {
				t.Errorf("gemini-3.8-flash at %q answered as %q is the model asked for: %+v", row.Effort, row.Served, row)
			}
		}
	}
}

// A row kept marked swapped before is read back as not swapped when it was
// answered under Google's serving name; a real swap stays one.
func TestKeptRowGeminiServingNameNotSwapped(t *testing.T) {
	c := &rowChunk{}
	c.add(Row{Record: Record{Model: "gemini-3.8-flash", Served: "gemini-3.8-flash-n"}, Swapped: true}, "", 0, false)
	c.add(Row{Record: Record{Model: "gpt-5", Served: "gpt-5-mini"}, Swapped: true}, "", 1, false)
	for _, chunk := range []*rowChunk{c, c.pack().unpack()} {
		if chunk.row(0).Swapped || !chunk.row(1).Swapped {
			t.Fatalf("serving name %v, other model %v", chunk.row(0).Swapped, chunk.row(1).Swapped)
		}
	}
}
