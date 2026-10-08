package gateway

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestCodexCompactResponsesRoutes(t *testing.T) {
	for _, path := range []string{"/v1/responses", "/responses", CodexPath + "/responses"} {
		for _, protocol := range []provider.Protocol{provider.Chat, provider.Responses} {
			for _, grouped := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/group=%t", path, protocol, grouped), func(t *testing.T) {
					upstream := &fake{t: t, reply: sse(
						`data: {"id":"c1","choices":[{"delta":{"content":"SUMMARY"},"finish_reason":"stop"}]}`,
						`data: [DONE]`)}
					if protocol == provider.Responses {
						upstream.reply = sse(
							`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"SUMMARY"}]}}`,
							`data: {"type":"response.completed","response":{"id":"resp_summary","status":"completed","output":[]}}`)
					}
					setup(t, protocol, upstream)
					model := "fake/m1"
					if grouped {
						if err := provider.SaveGroup(provider.Group{Name: "Compact", Members: []string{model}, Routing: provider.Ordered}); err != nil {
							t.Fatal(err)
						}
						model = "group/compact"
					}
					code, body := post(t, path, fmt.Sprintf(`{"model":%q,"stream":true,"tools":[{"type":"function","name":"shell","parameters":{"type":"object"}}],"input":[{"type":"message","role":"user","content":"fix the bug"},{"type":"compaction_trigger"}]}`, model))
					if code != 200 {
						t.Fatalf("compact: %d %s", code, body)
					}
					var compactItems []map[string]any
					completed := false
					for _, event := range events(body) {
						if event["type"] == "response.output_item.done" {
							item, _ := event["item"].(map[string]any)
							if item["type"] == "compaction" {
								compactItems = append(compactItems, item)
							}
						}
						if event["type"] == "response.completed" {
							completed = true
						}
					}
					if len(compactItems) != 1 || !completed {
						t.Fatalf("remote compaction v2 needs exactly one compaction item and completion: %s", body)
					}
					if got := compactedText(t, body); got != "SUMMARY" {
						t.Fatalf("summary: %q", got)
					}
					if strings.Contains(string(upstream.got), `"tools"`) || !strings.Contains(string(upstream.got), "CONTEXT CHECKPOINT COMPACTION") || strings.Contains(string(upstream.got), "compaction_trigger") {
						t.Fatalf("summary request: %s", upstream.got)
					}
					item, err := json.Marshal(compactItems[0])
					if err != nil {
						t.Fatal(err)
					}
					code, body = post(t, path, fmt.Sprintf(`{"model":%q,"stream":true,"input":[%s,{"type":"message","role":"user","content":"continue"}]}`, model, item))
					if code != 200 || !strings.Contains(string(upstream.got), "SUMMARY") || !strings.Contains(string(upstream.got), codexSummaryPrefix) || strings.Contains(string(upstream.got), magpieCompaction) {
						t.Fatalf("continue: %d %s; upstream: %s", code, body, upstream.got)
					}
				})
			}
		}
	}
}
