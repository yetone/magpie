package library

import (
	"path/filepath"
	"strings"
	"testing"
)

// lysyscy8 on X (0.1.3667, Windows): the Library's Instructions page leaves
// out MiniMax Code and WorkBuddy. MiniMax Code reads <data dir>/AGENTS.md
// before every conversation (~/.minimax, or $MINIMAX_DATA_DIR): magpie's
// part goes into it and the user's own text stays.
func TestMiniMaxCodeInstructions(t *testing.T) {
	h := sandbox(t)
	t.Setenv("MINIMAX_DATA_DIR", "")
	write(t, filepath.Join(h, ".minimax/config.yaml"), "")
	f := filepath.Join(h, ".minimax/AGENTS.md")
	write(t, f, "My own rule.\n")
	if tg := targetByID("minimax-code"); tg == nil || tg.Instructions != f || tg.Field != "" {
		t.Fatalf("%+v", tg)
	}
	shared := "Be brief."
	ok(t)(SaveInstructions(InstructionsChange{Shared: &shared, Agents: []string{"minimax-code"}}))
	s := read(t, f)
	if !strings.HasPrefix(s, "My own rule.\n") || !strings.Contains(s, blockBegin+"\nBe brief.") {
		t.Fatalf("AGENTS.md:\n%s", s)
	}
	ok(t)(SaveInstructions(InstructionsChange{Agents: []string{}}))
	if s := read(t, f); s != "My own rule.\n" {
		t.Fatalf("after taking magpie's part back:\n%q", s)
	}

	d := filepath.Join(h, "mm")
	t.Setenv("MINIMAX_DATA_DIR", d)
	write(t, filepath.Join(d, "config.yaml"), "")
	if tg := targetByID("minimax-code"); tg == nil || tg.Instructions != filepath.Join(d, "AGENTS.md") {
		t.Fatalf("with MINIMAX_DATA_DIR: %+v", tg)
	}
}

const wbConfig = `{
  "locale": "zh-CN",
  "personalization": {
    "toneStyle": "friendly",
    "customPrompt": "Answer in Chinese."
  },
  "zeta": { "keep": [1, 2] }
}
`

func wbSandbox(t *testing.T, content string) (string, string) {
	t.Helper()
	h := sandbox(t)
	t.Setenv("WORKBUDDY_CONFIG_DIR", "")
	write(t, filepath.Join(h, ".workbuddy/models.json"), "")
	holdApp(t, h, "WorkBuddy.app", "com.tencent.workbuddy.mac")
	f := filepath.Join(h, ".workbuddy/app/app-config.json")
	if content != "" {
		write(t, f, content)
	}
	return h, f
}

// WorkBuddy's instructions are personalization.customPrompt in its
// app/app-config.json (the Custom Instructions of Personalization). The
// user's prompt stays in the field around magpie's part, and every other
// key of the file stays as it was; taking magpie's part back leaves the
// file as it was, byte for byte.
func TestWorkBuddyInstructionsAreAJSONField(t *testing.T) {
	_, f := wbSandbox(t, wbConfig)
	if tg := targetByID("workbuddy"); tg == nil || tg.Instructions != f || tg.Field != "personalization.customPrompt" {
		t.Fatalf("%+v", tg)
	}
	shared := "Be brief."
	ok(t)(SaveInstructions(InstructionsChange{Shared: &shared, Agents: []string{"workbuddy"}}))
	s := read(t, f)
	for _, want := range []string{`"locale": "zh-CN"`, `"toneStyle": "friendly"`, `"zeta": { "keep": [1, 2] }`, "Answer in Chinese.", "Be brief."} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q:\n%s", want, s)
		}
	}
	if !strings.HasPrefix(instrText(targetByID("workbuddy")), "Answer in Chinese.") {
		t.Errorf("the user's prompt isn't first:\n%s", s)
	}
	if i, j := strings.Index(s, "locale"), strings.Index(s, "zeta"); i > j {
		t.Errorf("key order changed:\n%s", s)
	}
	ok(t)(SaveInstructions(InstructionsChange{Agents: []string{}}))
	if s := read(t, f); s != wbConfig {
		t.Fatalf("after taking magpie's part back:\n%s", s)
	}
}

// With no prompt of the user's, and no personalization at all, magpie's part
// makes them, and taking it back leaves no empty object where the app had
// none (an empty one would stop it reading its legacy settings.json).
func TestWorkBuddyInstructionsFromNothing(t *testing.T) {
	_, f := wbSandbox(t, `{"locale":"en"}`+"\n")
	shared := "Be brief."
	ok(t)(SaveInstructions(InstructionsChange{Shared: &shared, Agents: []string{"workbuddy"}}))
	if s := read(t, f); !strings.Contains(s, `"customPrompt"`) || !strings.Contains(s, "Be brief.") || !strings.Contains(s, `"locale"`) {
		t.Fatalf("%s", s)
	}
	ok(t)(SaveInstructions(InstructionsChange{Agents: []string{}}))
	if s := strings.TrimSpace(read(t, f)); strings.Contains(s, "personalization") || !strings.Contains(s, `"locale"`) {
		t.Fatalf("after taking magpie's part back:\n%s", s)
	}
}

// A file that isn't JSON, or a prompt that isn't text, is unknown: nothing
// is written over it, and the agent is reported.
func TestWorkBuddyInstructionsUnreadFileIsLeftAlone(t *testing.T) {
	for name, content := range map[string]string{
		"not JSON":    `{"locale": `,
		"not text":    `{"personalization":{"customPrompt":{"a":1}}}`,
		"parent null": `{"personalization":null,"x":1}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, f := wbSandbox(t, content)
			shared := "Be brief."
			res, err := SaveInstructions(InstructionsChange{Shared: &shared, Agents: []string{"workbuddy"}})
			if err != nil {
				t.Fatal(err)
			}
			if name == "parent null" {
				// null is nothing there: the prompt is made, the rest kept
				if s := read(t, f); !strings.Contains(s, "Be brief.") || !strings.Contains(s, `"x":1`) && !strings.Contains(s, `"x": 1`) {
					t.Fatalf("%s", s)
				}
				return
			}
			if s := read(t, f); s != content {
				t.Errorf("file changed:\n%s", s)
			}
			if len(res.Problems) == 0 {
				t.Errorf("no problem reported")
			}
		})
	}
}

// ImportInstructions takes the prompt the user wrote in WorkBuddy into the
// library, and leaves the field empty and every other key.
func TestWorkBuddyInstructionsImport(t *testing.T) {
	_, f := wbSandbox(t, wbConfig)
	ok(t)(ImportInstructions("workbuddy"))
	v, err := ReadInstructions()
	if err != nil || !strings.Contains(v.Shared, "Answer in Chinese.") {
		t.Fatalf("%v %+v", err, v)
	}
	s := read(t, f)
	if !strings.Contains(s, `"toneStyle": "friendly"`) || !strings.Contains(s, `"locale"`) || !strings.Contains(s, `"zeta"`) {
		t.Fatalf("other keys lost:\n%s", s)
	}
}

// WorkBuddy AI keeps the same file in its own folder.
func TestWorkBuddyAIInstructions(t *testing.T) {
	h := sandbox(t)
	write(t, filepath.Join(h, ".workbuddy-ai/models.json"), "")
	holdApp(t, h, "WorkBuddy AI.app", "com.workbuddy.workbuddy-ai")
	tg := targetByID("workbuddy-ai")
	if tg == nil || tg.Instructions != filepath.Join(h, ".workbuddy-ai/app/app-config.json") || tg.Field != "personalization.customPrompt" {
		t.Fatalf("%+v", tg)
	}
}
