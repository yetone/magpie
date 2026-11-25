package gateway

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// A group's own classifier instructions (sloceanxc on X: magpie's, written
// for a coding assistant, misled the classifier on theirs) go to the
// classifier in place of magpie's, on the turn's real path: the intents
// still listed by number and the answer still a number, which routes the
// turn as any answer does.
func TestGroupClassifyPromptReachesTheClassifier(t *testing.T) {
	s, _, _, c := intented(t, provider.Rule{Use: "b/big", Intent: "a story to write"})
	const own = "The user writes fiction with the assistant. A request to continue a scene is a story to write, even when it names code."
	g, _, _ := provider.FindGroup("group/r")
	g.ClassifyPrompt = "  " + own + "\n"
	if err := provider.SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	if g, _, _ = provider.FindGroup("group/r"); g.ClassifyPrompt != own {
		t.Fatalf("saved %q", g.ClassifyPrompt)
	}
	c.set("1", 0)
	_, r := postOK(t, s, "s1", chat("continue the scene where the hacker types her code", nil, 0, ""))
	if r.Rule == nil || r.Rule.Use != "b/big" || r.Rule.Classified == nil || r.Rule.Classified.Intent != "a story to write" {
		t.Fatalf("not routed by the answer: %+v", r.Rule)
	}
	var asked struct {
		Messages []struct{ Role, Content string } `json:"messages"`
	}
	if err := json.Unmarshal([]byte(c.last()), &asked); err != nil || len(asked.Messages) != 2 {
		t.Fatalf("asked %v %s", err, c.last())
	}
	sys, q := asked.Messages[0].Content, asked.Messages[1].Content
	if !strings.HasPrefix(sys, own+"\n\n") || !strings.Contains(sys, "Answer with the number only") {
		t.Fatalf("system %q", sys)
	}
	// magpie's own way of judging is left out: the user's words replace it
	if strings.Contains(sys, "every message has one") || strings.Contains(sys, "fits the message best") {
		t.Fatalf("magpie's guidance kept beside the group's: %q", sys)
	}
	if !strings.Contains(q, "1. a story to write") || !strings.Contains(q, "<message>\ncontinue the scene") {
		t.Fatalf("question %q", q)
	}
}

// Without instructions of its own a group's classifier is told magpie's,
// which name no kind of assistant: magpie serves any agent.
func TestClassifyPromptNamesNoKindOfAssistant(t *testing.T) {
	var asked struct {
		Messages []struct{ Role, Content string } `json:"messages"`
	}
	if err := json.Unmarshal(classifyBody("m", "", []string{"x", "y"}, before{}, "hi"), &asked); err != nil {
		t.Fatal(err)
	}
	sys := asked.Messages[0].Content
	if sys != classifyPrompt || !strings.Contains(sys, "Answer with the number only") {
		t.Fatalf("system %q", sys)
	}
	for _, s := range []string{classifyPrompt, effortPrompt, intentAsk(before{}), effortAsk(before{})} {
		if strings.Contains(strings.ToLower(s), "coding") {
			t.Errorf("names coding: %q", s)
		}
	}
	// blank instructions are none
	if got := classifySystem(" \n "); got != classifyPrompt {
		t.Fatalf("blank: %q", got)
	}
}

// Jev is asked by the group's instructions too: its intent question is
// theirs, and magpie's word on judging levels by their work stays out.
func TestGroupClassifyPromptAsksJev(t *testing.T) {
	const own = "Kinds are about which language the user writes in."
	var body struct {
		Questions map[string]struct {
			Instructions string `json:"instructions"`
		} `json:"questions"`
	}
	b := jevBody("jev-latest", []string{"english", "chinese"}, map[string]string{"english": "a", "chinese": "b"}, before{Prompt: own}, false, "你好")
	if err := json.Unmarshal(b, &body); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"intent", "level"} {
		in := body.Questions[k].Instructions
		if !strings.HasPrefix(in, own) || strings.Contains(in, "Which kind of request fits it best") || strings.Contains(in, "judge the work") {
			t.Errorf("%s: %q", k, in)
		}
	}
}

// The answer kept for a message is kept for the instructions it was asked
// under: another group's, or the same group's once its instructions
// change, asks again rather than taking an answer given under others.
func TestClassifyKeyCoversThePrompt(t *testing.T) {
	n := 0
	ask := func(_ access.Identity, _ string, intents []string, prev before, _ bool, _ string) (verdict, error) {
		n++
		return verdict{Intent: intents[0], Said: "1"}, nil
	}
	intents := []string{"a story to write", "code to fix"}
	for i, c := range []struct {
		prompt string
		asks   int
		cached bool
	}{
		{"", 1, false},
		{"", 1, true},
		{"The user writes fiction.", 2, false},
		{"The user writes fiction.", 2, true},
		{"The user writes code.", 3, false},
	} {
		_, cached, err := classify(ask, access.Identity{}, t.Name(), intents, before{Prompt: c.prompt}, false, "continue the scene")
		if err != nil || cached != c.cached || n != c.asks {
			t.Fatalf("%d (%q): cached %v, %d asks, %v; want cached %v, %d asks", i, c.prompt, cached, n, err, c.cached, c.asks)
		}
	}
}
