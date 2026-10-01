package edit

import "testing"

func TestEnvInlineComments(t *testing.T) {
	for _, tc := range []struct{ line, want string }{{"GEMINI_API_KEY=abc123 # my key\n", "abc123"}, {"GEMINI_API_KEY=abc123#comment\n", "abc123"}, {"export GEMINI_API_KEY='abc#123' # my key\n", "abc#123"}, {"GEMINI_API_KEY=\"abc#123\" # my key\n", "abc#123"}, {"GEMINI_API_KEY= # none\n", ""}} {
		p := tmpFile(t, ".env", tc.line)
		got, ok := GetEnvFile(p, "GEMINI_API_KEY")
		if !ok || got != tc.want {
			t.Fatalf("line %q: got %q %v, want %q", tc.line, got, ok, tc.want)
		}
	}
}
