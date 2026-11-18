package gateway

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/codexsummary"
)

// hey_boyce on X: OpenAI turned a turn away for magpie's own compaction
// ("The encrypted content for item cmp_magpie_1794916786407450000 could
// not be verified"). Asked again, the item goes as the summary it holds,
// not dropped with it, and every other item stays; an item of OpenAI's
// own that it refuses is still left out.
func TestRefusedMagpieCompactionGoesAsText(t *testing.T) {
	msg := []byte(`{"error":{"code":"invalid_encrypted_content","message":"The encrypted content for item cmp_magpie_1794916786407450000 could not be verified. Reason: Encrypted content could not be decrypted or parsed.","param":null,"type":"invalid_request_error"}}`)
	body, _ := json.Marshal(map[string]any{"model": "gpt-5.4", "input": []any{
		map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "first"}}},
		map[string]any{"type": "compaction", "id": "cmp_magpie_1794916786407450000", "encrypted_content": codexsummary.Encrypted("we fixed the parser")},
		map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "next"}}},
	}})
	got, ok := withoutUnreadable(body, msg)
	if !ok {
		t.Fatal("not asked again")
	}
	var q struct {
		Input []struct {
			Type    string `json:"type"`
			ID      string `json:"id"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal(got, &q); err != nil {
		t.Fatal(err)
	}
	if len(q.Input) != 3 {
		t.Fatalf("%d items asked again, want 3: %s", len(q.Input), got)
	}
	mid := q.Input[1]
	if mid.Type != "message" || len(mid.Content) != 1 || !strings.HasSuffix(mid.Content[0].Text, "\nwe fixed the parser") || !strings.HasPrefix(mid.Content[0].Text, codexsummary.Prefix) {
		t.Fatalf("the compaction went as %+v", mid)
	}
	if strings.Contains(string(got), "magpie1:") {
		t.Fatalf("the sealed form still goes: %s", got)
	}

	// OpenAI's own compaction it can't read is left out
	body, _ = json.Marshal(map[string]any{"model": "gpt-5.4", "input": []any{
		map[string]any{"type": "compaction", "id": "cmp_0abc", "encrypted_content": "gAAAAB"},
		map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "next"}}},
	}})
	msg = []byte(`{"error":{"code":"invalid_encrypted_content","message":"The encrypted content for item cmp_0abc could not be verified."}}`)
	if got, ok = withoutUnreadable(body, msg); !ok || strings.Contains(string(got), "cmp_0abc") || !strings.Contains(string(got), "next") {
		t.Fatalf("OpenAI's own: %v %s", ok, got)
	}
}
