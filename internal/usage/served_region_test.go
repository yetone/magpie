package usage

import "testing"

// WorkBuddy AI lists DeepSeek V4.1 Flash as deepseek-v4.1-flash-sg, its
// paid id for the Singapore deployment, and answers under the model's own
// name: the Routing page marked it "requested deepseek-v4.1-flash-sg ·
// served deepseek-v4.1-flash" (plugins#628, chansee97). A region after
// the name asked for, or after the name that answered, is the same model.
// Another model with a region after it is still a swap, as is one region
// answered as another.
func TestSwappedRegionTail(t *testing.T) {
	for _, c := range [][2]string{
		{"deepseek-v4.1-flash-sg", "deepseek-v4.1-flash"},
		{"deepseek-v4.1-flash", "deepseek-v4.1-flash-sg"},
		{"workbuddy-ai/deepseek-v4.1-flash-sg", "DeepSeek-V4.1-Flash"},
		{"glm-5.1-intl", "glm-5.1"},
		{"qwen3.8-plus_cn", "qwen3.8-plus"},
	} {
		if Swapped(c[0], c[1]) {
			t.Errorf("%s answered as %s is the model asked for", c[0], c[1])
		}
	}
	for _, c := range [][2]string{
		{"deepseek-v4.1-flash-sg", "deepseek-v4.1-pro"},
		{"deepseek-v4.1-pro-sg", "deepseek-v4.1-flash"},
		{"deepseek-v4.1-flash-sg", "deepseek-chat"},
		{"deepseek-v4.1-flash-sg", "deepseek-v4.1-flash-thinking"},
		{"gpt-5-sg", "gpt-5-mini"},
		{"sg", "us"},
	} {
		if !Swapped(c[0], c[1]) {
			t.Errorf("%s answered as %s is another model", c[0], c[1])
		}
	}
}

// A row kept marked swapped before plugins#628 is read back as not
// swapped; a real swap stays one.
func TestKeptRowRegionTailNotSwapped(t *testing.T) {
	c := &rowChunk{}
	c.add(Row{Record: Record{Provider: "workbuddy-ai", Model: "deepseek-v4.1-flash-sg", Served: "deepseek-v4.1-flash"}, Swapped: true}, "", 0, false)
	c.add(Row{Record: Record{Provider: "workbuddy-ai", Model: "deepseek-v4.1-flash-sg", Served: "deepseek-v4.1-pro"}, Swapped: true}, "", 1, false)
	for _, chunk := range []*rowChunk{c, c.pack().unpack()} {
		if chunk.row(0).Swapped || !chunk.row(1).Swapped {
			t.Fatalf("region %v, other model %v", chunk.row(0).Swapped, chunk.row(1).Swapped)
		}
	}
}
