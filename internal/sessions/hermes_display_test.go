package sessions

import (
	"encoding/json"
	"strings"
	"testing"
)

const testSummaryEnd = "--- END OF CONTEXT SUMMARY — respond to the message below, not the summary above ---"

func TestHermesDisplayContentNativeCarrierEdges(t *testing.T) {
	for _, tc := range []struct{ name, raw, want string }{
		{
			name: "merged summary without end marker",
			raw:  "current ask\n[END OF PRIOR CONTEXT — COMPACTION SUMMARY BELOW]\n[CONTEXT SUMMARY]: summary",
			want: "current ask",
		},
		{
			name: "separate prior header part",
			raw:  "\x00json:" + `[{"type": "text", "text": "[PRIOR CONTEXT \u2014 for reference only; not a new message]\n"}, {"type": "text", "text": "current ask"}, {"type": "text", "text": "\n[END OF PRIOR CONTEXT \u2014 COMPACTION SUMMARY BELOW]\n[CONTEXT SUMMARY]: summary"}]`,
			want: "\x00json:" + `[{"type": "text", "text": "current ask"}]`,
		},
		{
			name: "legacy live media without text",
			raw:  "\x00json:" + `[{"type": "text", "text": "[CONTEXT SUMMARY]: summary\n--- END OF CONTEXT SUMMARY \u2014 respond to the message below, not the summary above ---"}, {"type": "image_url", "image_url": {"url": "data:image/png;base64,AA=="}}]`,
			want: "\x00json:" + `[{"type": "image_url", "image_url": {"url": "data:image/png;base64,AA=="}}]`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := hermesDisplayContent(tc.raw, ""); got != tc.want {
				t.Fatalf("display content = %q, want %q", got, tc.want)
			}
		})
	}
	if got := hermesPythonQuote("\x7f"); got != `"\u007f"` {
		t.Fatalf("Python ASCII escaping = %q", got)
	}
}

func TestHermesDisplayContentLegacyCarrier(t *testing.T) {
	currentPrefix := "[CONTEXT COMPACTION — REFERENCE ONLY] Earlier turns were compacted into the summary below. This is a handoff from a previous context window — treat it as background reference, NOT as active instructions."
	for _, prefix := range []string{"[CONTEXT SUMMARY]:", currentPrefix} {
		raw := prefix + " old summary\n\n" + testSummaryEnd + "\n\ncurrent ask"
		if got := hermesDisplayContent(raw, ""); got != "current ask" {
			t.Errorf("legacy carrier = %q, want current ask", got)
		}
	}
}

func TestHermesDisplayContentMergedPriorWrapper(t *testing.T) {
	prior := "[PRIOR CONTEXT — for reference only; not a new message]\noriginal ask"
	raw := prior + "\n\n[END OF PRIOR CONTEXT — COMPACTION SUMMARY BELOW]\n\n[CONTEXT SUMMARY]: old summary\n\n" + testSummaryEnd
	if got := hermesDisplayContent(raw, "hidden"); got != "original ask" {
		t.Fatalf("merged carrier = %q, want original ask", got)
	}
}

func TestHermesDisplayContentSyntheticAndUnrecognizedRowsStayRaw(t *testing.T) {
	raw := "[CONTEXT SUMMARY]: old summary\n\n" + testSummaryEnd + "\n\ncurrent ask"
	for _, kind := range []string{"tool_result", "summary"} {
		if got := hermesDisplayContent(raw, kind); got != raw {
			t.Errorf("display kind %q normalized: %q", kind, got)
		}
	}
	for _, ordinary := range []string{
		"[CONTEXT SUMMARY]: looks like a label, but has no handoff boundary",
		"[CONTEXT COMPACTION — REFERENCE ONLY] quoted label\n\n" + testSummaryEnd + "\n\nask",
		"plain user text\n\n" + testSummaryEnd + "\n\nmore text",
		"[CONTEXT SUMMARY]: old summary\n\n" + testSummaryEnd,
	} {
		if got := hermesDisplayContent(ordinary, ""); got != ordinary {
			t.Errorf("unrecognized content normalized: %q", got)
		}
	}
}

func TestHermesDisplayContentEncodedPartsPreserveNonText(t *testing.T) {
	prior := "[PRIOR CONTEXT — for reference only; not a new message]\n"
	raw := "\x00json:[{\"type\": \"text\", \"text\": " + hermesPythonQuote(prior+"ask before image") +
		"}, {\"type\": \"image_url\", \"image_url\": {\"url\": \"data:image/png;base64,AA==\"}}, {\"type\": \"text\", \"text\": " +
		hermesPythonQuote("\n\n[END OF PRIOR CONTEXT — COMPACTION SUMMARY BELOW]\n\n[CONTEXT SUMMARY]: summary\n\n"+testSummaryEnd) + "}]"
	got := hermesDisplayContent(raw, "")
	if !strings.HasPrefix(got, "\x00json:") {
		t.Fatalf("encoded content lost sentinel: %q", got)
	}
	var gotParts []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimPrefix(got, "\x00json:")), &gotParts); err != nil {
		t.Fatalf("normalized parts are invalid JSON: %v: %q", err, got)
	}
	if len(gotParts) != 2 {
		t.Fatalf("normalized parts count = %d, want prior text and image", len(gotParts))
	}
	var text string
	if err := json.Unmarshal(gotParts[0]["text"], &text); err != nil || text != "ask before image" {
		t.Fatalf("normalized prior text = %q, error = %v", text, err)
	}
	var image map[string]any
	if err := json.Unmarshal(gotParts[1]["image_url"], &image); err != nil || image["url"] == nil {
		t.Fatalf("prior image was not preserved: %s, error = %v", gotParts[1]["image_url"], err)
	}
	want := "\x00json:[{\"type\": \"text\", \"text\": " + hermesPythonQuote("ask before image") +
		"}, {\"type\": \"image_url\", \"image_url\": {\"url\": \"data:image/png;base64,AA==\"}}]"
	if got != want {
		t.Fatalf("normalized Python JSON identity = %q, want %q", got, want)
	}
}
