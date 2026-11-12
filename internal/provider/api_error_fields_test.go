package provider

import "testing"

// A 400 whose message is only the status's text ("Bad Request", Codex on a
// Team account, FjTxw0Mz8wxBuye on X) says what it refused in its type,
// code and param, which the user is shown after it; a message with words
// of its own is shown as it is.
func TestAPIErrorBareStatusMessageGivesItsFields(t *testing.T) {
	for _, c := range []struct{ body, want string }{
		{`{"error":{"message":"Bad Request","type":"invalid_request_error","code":"invalid_value","param":"input[3].content"}}`,
			"Bad Request (invalid_request_error, invalid_value, input[3].content)"},
		{`{"error":{"message":"Bad Request","type":"invalid_request_error","code":null,"param":null}}`,
			"Bad Request (invalid_request_error)"},
		{`{"error":{"message":"Bad Request"}}`, "Bad Request"},
		{`{"error":{"message":"Unsupported parameter: foo","type":"invalid_request_error","param":"foo"}}`, "Unsupported parameter: foo"},
	} {
		if got := APIError([]byte(c.body), "400 Bad Request"); got != c.want {
			t.Errorf("%s\n got %q\nwant %q", c.body, got, c.want)
		}
	}
}
