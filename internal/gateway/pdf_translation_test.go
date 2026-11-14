package gateway

import (
	"strings"
	"testing"
)

// A PDF only reaches the model when the request is relayed as sent; the
// moment magpie translates it, the file is lost — the Responses input_file
// and the Messages document are dropped whole, and the Chat file becomes
// the placeholder text "[attachment application/pdf]" (#5452). Each shape
// survives its translation now: the parts parse into file parts, and every
// protocol's encoder writes them in its own tongue.

const pdfB64 = "JVBERi0xLjQK" // "%PDF-1.4"

func TestResponsesInputFileSurvivesTranslation(t *testing.T) {
	r, err := parseResponses([]byte(`{"model":"m","input":[{"type":"input_file","filename":"doc.pdf","file_data":"data:application/pdf;base64,` + pdfB64 + `"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	var file *Part
	for i, m := range r.Messages {
		for j := range m.Parts {
			if r.Messages[i].Parts[j].Kind == File {
				file = &r.Messages[i].Parts[j]
			}
		}
	}
	if file == nil {
		t.Fatalf("the input_file was dropped: %+v", r.Messages)
	}
	if file.MediaType != "application/pdf" || file.Data != pdfB64 {
		t.Fatalf("file part %+v, want application/pdf %s", *file, pdfB64)
	}
}

// A content part carries the fields flat, as the OpenAI shape has them
// and the report's case C sends them; a Chat-style client nests them under
// "file". Both ride.
func TestResponsesInputFilePartSurvivesTranslation(t *testing.T) {
	for _, tc := range []struct{ name, part string }{
		{"flat", `{"type":"input_file","filename":"doc.pdf","file_data":"data:application/pdf;base64,` + pdfB64 + `"}`},
		{"nested", `{"type":"input_file","file":{"filename":"doc.pdf","file_data":"data:application/pdf;base64,` + pdfB64 + `"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := parseResponses([]byte(`{"model":"m","input":[{"role":"user","content":[{"type":"input_text","text":"read it"},` + tc.part + `]}]}`))
			if err != nil {
				t.Fatal(err)
			}
			var file *Part
			for i, m := range r.Messages {
				for j := range m.Parts {
					if r.Messages[i].Parts[j].Kind == File {
						file = &r.Messages[i].Parts[j]
					}
				}
			}
			if file == nil {
				t.Fatalf("the input_file was dropped: %+v", r.Messages)
			}
			if file.MediaType != "application/pdf" || file.Data != pdfB64 {
				t.Fatalf("file part %+v, want application/pdf %s", *file, pdfB64)
			}
		})
	}
}

func TestAnthropicDocumentSurvivesTranslation(t *testing.T) {
	r, err := readAnthropic([]byte(`{"model":"m","max_tokens":64,"messages":[{"role":"user","content":[{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"`+pdfB64+`"}}]}]}`), false, false)
	if err != nil {
		t.Fatal(err)
	}
	var file *Part
	for i, m := range r.Messages {
		for j := range m.Parts {
			if r.Messages[i].Parts[j].Kind == File {
				file = &r.Messages[i].Parts[j]
			}
		}
	}
	if file == nil {
		t.Fatalf("the document block was dropped: %+v", r.Messages)
	}
	if file.MediaType != "application/pdf" || file.Data != pdfB64 {
		t.Fatalf("file part %+v, want application/pdf %s", *file, pdfB64)
	}
}

func TestChatFileRidesToResponsesAndAnthropic(t *testing.T) {
	req := []byte(`{"model":"m","messages":[{"role":"user","content":[{"type":"file","file":{"filename":"doc.pdf","file_data":"data:application/pdf;base64,` + pdfB64 + `"}}]}]}`)
	r, err := parseChat(req)
	if err != nil {
		t.Fatal(err)
	}

	// to a Responses upstream: an input_file, not the placeholder text
	out := buildResponses(r, "m", "upstream.test", false)
	if !strings.Contains(string(out), `"input_file"`) || !strings.Contains(string(out), pdfB64) {
		t.Fatalf("responses body lost the file:\n%s", out)
	}
	if strings.Contains(string(out), "[attachment") {
		t.Fatalf("responses body carries the placeholder text:\n%s", out)
	}

	// to an Anthropic upstream: a document block, not the placeholder text
	out = buildAnthropic(r, "m")
	if !strings.Contains(string(out), `"document"`) || !strings.Contains(string(out), pdfB64) {
		t.Fatalf("anthropic body lost the file:\n%s", out)
	}
	if strings.Contains(string(out), "[attachment") {
		t.Fatalf("anthropic body carries the placeholder text:\n%s", out)
	}
}

// Only a PDF goes as a file: Anthropic's base64 document takes a PDF
// alone, so a .docx sent as one would turn the whole turn away with a
// 400 (#934). Any other file is still told of, on both upstreams.
func TestOnlyAPDFGoesAsAFile(t *testing.T) {
	r, err := parseChat([]byte(`{"model":"m","messages":[{"role":"user","content":[{"type":"file","file":{"filename":"notes.csv","file_data":"data:text/csv;base64,YSxiCg=="}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	for name, out := range map[string][]byte{"responses": buildResponses(r, "m", "upstream.test", false), "anthropic": buildAnthropic(r, "m")} {
		if strings.Contains(string(out), `"input_file"`) || strings.Contains(string(out), `"document"`) {
			t.Fatalf("%s body sent a csv as a file:\n%s", name, out)
		}
		if !strings.Contains(string(out), "[attachment text/csv]") {
			t.Fatalf("%s body lost the csv's note:\n%s", name, out)
		}
	}
}

// A Responses input_file with file_data carries a filename, as OpenAI's
// shape has it.
func TestResponsesFileHasAFilename(t *testing.T) {
	r, err := parseChat([]byte(`{"model":"m","messages":[{"role":"user","content":[{"type":"file","file":{"filename":"doc.pdf","file_data":"data:application/pdf;base64,` + pdfB64 + `"}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if out := buildResponses(r, "m", "upstream.test", false); !strings.Contains(string(out), `"filename":"document.pdf"`) {
		t.Fatalf("input_file without a filename:\n%s", out)
	}
}

// A document whose source is text carries its words, not base64: they go
// on as text, never as a PDF's inline data. One given only by URL is told
// of, not fetched by another vendor.
func TestAnthropicDocumentTextAndURLSources(t *testing.T) {
	r, err := readAnthropic([]byte(`{"model":"m","max_tokens":64,"messages":[{"role":"user","content":[{"type":"document","source":{"type":"text","media_type":"text/plain","data":"the plain words"}},{"type":"document","source":{"type":"url","url":"https://example.invalid/a.pdf"}}]}]}`), false, false)
	if err != nil {
		t.Fatal(err)
	}
	out := string(buildResponses(r, "m", "upstream.test", false))
	if !strings.Contains(out, "the plain words") || strings.Contains(out, `"input_file"`) {
		t.Fatalf("text document not carried as text:\n%s", out)
	}
	if !strings.Contains(out, "[attachment : https://example.invalid/a.pdf]") {
		t.Fatalf("url document not told of:\n%s", out)
	}
	if a := string(buildAnthropic(r, "m")); strings.Contains(a, `"url":"https://example.invalid/a.pdf"`) && strings.Contains(a, `"document"`) {
		t.Fatalf("url document sent as a document:\n%s", a)
	}
}
