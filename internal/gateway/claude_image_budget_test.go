package gateway

import (
	"fmt"
	"strings"
	"testing"
)

// ompImageGone is what omp (oh-my-pi 18.20.3, clampProviderContextImages)
// puts in a message whose every image its image budget took out.
const ompImageGone = "[image omitted: provider image limit]"

// ompPart is a part of a message as omp keeps its history: text, a
// thinking block, or an image (neither).
type ompPart struct {
	text  string
	think bool
}

type ompMessage struct {
	role  string
	parts []ompPart
}

// ompClamp is omp's history as it sends it to a provider it has no image
// budget for (5 images, snapcompact's DEFAULT_PROVIDER_IMAGE_BUDGET): the
// oldest images past the budget taken out, and a message left with
// nothing told ompImageGone.
func ompClamp(history []ompMessage, budget int) []string {
	images := 0
	for _, m := range history {
		for _, p := range m.parts {
			if p.text == "" && !p.think {
				images++
			}
		}
	}
	drop := images - budget
	var out []string
	for _, m := range history {
		var parts []string
		for _, p := range m.parts {
			switch {
			case p.think:
				parts = append(parts, `{"type":"thinking","thinking":"the rest","signature":"sig"}`)
			case p.text != "":
				parts = append(parts, fmt.Sprintf(`{"type":"text","text":%q}`, p.text))
			case drop > 0:
				drop--
			default:
				parts = append(parts, `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"`+onePNG+`"}}`)
			}
		}
		if len(parts) == 0 {
			parts = []string{fmt.Sprintf(`{"type":"text","text":%q}`, ompImageGone)}
		}
		out = append(out, fmt.Sprintf(`{"role":%q,"content":[%s]}`, m.role, strings.Join(parts, ",")))
	}
	return out
}

// omp keeps 5 images in a request to a provider it has no budget for, the
// oldest taken out as a new one comes (Yqihotaru on X): each turn whose
// user pastes an image takes one out of an earlier turn. A turn after a
// reply the client keeps as several text blocks, its words split by a
// thinking block (#5705), went to a new Claude Code, told the whole
// conversation from a transcript and all of it written to the prompt
// cache again: the run kept its reply as one text, which mediaMoved
// compared block by block. Each goes on in the run that had the
// conversation.
func TestClaudeOmpImageBudgetGoesOnInItsRun(t *testing.T) {
	const reply = "Looking at it:Here is the answer."
	for _, r := range []struct {
		name  string
		parts []ompPart // the reply as the client keeps it
	}{
		{"a reply in one block", []ompPart{{text: reply}}},
		{"a reply split by its thinking", []ompPart{{think: true}, {text: "Looking at it:"}, {think: true}, {text: "Here is the answer."}}},
	} {
		for _, c := range []struct {
			name string
			with func(i int) []ompPart // the user's message of turn i, with an image
		}{
			{"words and an image", func(i int) []ompPart { return []ompPart{{text: fmt.Sprintf("more %d 看这个", i)}, {}} }},
			{"an image alone", func(int) []ompPart { return []ompPart{{}} }},
			{"an image and words after", func(i int) []ompPart { return []ompPart{{}, {text: fmt.Sprintf("more %d 这是什么", i)}} }},
			{"two images", func(i int) []ompPart { return []ompPart{{text: fmt.Sprintf("more %d", i)}, {}, {}} }},
		} {
			t.Run(r.name+"/"+c.name, func(t *testing.T) {
				h := newReplyHarness(t, reply)
				var history []ompMessage
				for i := range 6 {
					history = append(history,
						ompMessage{"user", []ompPart{{text: fmt.Sprintf("第 %d 个问题", i)}, {}}},
						ompMessage{"assistant", []ompPart{{text: fmt.Sprintf("第 %d 个回答", i)}}})
				}
				for turn, image := range []bool{false, true, true, false, true, true, false, true} {
					ask := []ompPart{{text: fmt.Sprintf("more %d", turn)}}
					if image {
						ask = c.with(turn)
					}
					history = append(history, ompMessage{"user", ask})
					h.send("rules", []string{"read"}, ompClamp(history, 5)...)
					history = append(history, ompMessage{"assistant", r.parts})
					if n := len(h.runs()); n != 1 {
						t.Fatalf("turn %d (image %t): %d runs, want the one that had the conversation", turn, image, n)
					}
				}
			})
		}
	}
}
