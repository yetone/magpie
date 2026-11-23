package gateway

import (
	"regexp"
	"strings"
	"testing"
)

// TestItemIDRefusalAsTheRegexp: the hand-made match finds what the
// regexp it replaced found, at the 1000-character edge too.
func TestItemIDRefusalAsTheRegexp(t *testing.T) {
	was := regexp.MustCompile(`(?s)Invalid 'input\[\d+\]\.id'.{0,1000}?Expected an ID that contains letters, numbers, underscores, or dashes`)
	const head, tail = "Invalid 'input[12].id'", "Expected an ID that contains letters, numbers, underscores, or dashes"
	in := []string{
		`{"error":{"message":"Invalid 'input[1].id': 'rs_6aca2fc7533c979d5f0e4f7a:rs_01a1'.\nExpected an ID that contains letters, numbers, underscores, or dashes, but this value contained additional characters."}}`,
		"", head, tail, tail + head, head + tail, "Invalid 'input[].id'" + tail,
		head + strings.Repeat("x", 2000) + head + "." + tail,
		head + "Expected an ID" + strings.Repeat(" ", 900) + tail,
	}
	for _, unit := range []string{"x", "é", "日", "\xff", "\n"} {
		for _, n := range []int{998, 999, 1000, 1001, 1002} {
			in = append(in, head+strings.Repeat(unit, n)+tail, head+strings.Repeat(unit, n)+tail[:20])
		}
	}
	for _, s := range in {
		if w, g := was.MatchString(s), itemIDRefusal([]byte(s)); w != g {
			t.Errorf("%d bytes: %v, want %v", len(s), g, w)
		}
	}
}

// TestItemIDRefusalLengthTooLong: a refusal of an id longer than the 64
// an id may have is an item id refusal as much as one of a character is —
// word for word as OpenAI's API words it, and a relay in front of it
// passes on (#4525 isnine's 420, #4527 ghjkllasdf-fake's 444, #5812
// JaxWeng's 428: "string too long. Expected a string with maximum length
// 64, but got a string with length 428 instead.", which the character
// tail alone never matches, so the ask again never fired and every turn
// of the session failed the same way). A "string too long" anywhere but
// within the 1000 characters after the head is nobody's item id refusal.
func TestItemIDRefusalLengthTooLong(t *testing.T) {
	const head = "Invalid 'input[286].id'"
	in := []struct {
		s    string
		want bool
	}{
		{`{"error":{"message":"Invalid 'input[286].id': string too long. Expected a string with maximum length 64, but got a string with length 428 instead.","type":"invalid_request_error","param":"input[286].id","code":"string_too_long"}}`, true},
		{head + ` string too long. Expected a string with maximum length 64, but got a string with length 420 instead.`, true},
		// LiteLLM's, the Azure deployment's refusal inside it (et84121, #5812)
		{`{"error":{"message":"litellm.BadRequestError: AzureException BadRequestError - {\n  \"error\": {\n    \"message\": \"Invalid 'input[58].id': string too long. Expected a string with maximum length 64, but got a string with length 484 instead.\",\n    \"type\": \"invalid_request_error\",\n    \"param\": \"input[58].id\",\n    \"code\": \"string_above_max_length\"\n  }\n}\nReceived Model Group=gpt-6.2-sol\nAvailable Model Group Fallbacks=None","type":null,"param":null,"code":"400"}}`, true},
		// a refusal of another field's length is not an item id's
		{`{"error":{"message":"Invalid 'input[3].content': string too long. Expected a string with maximum length 4096."}}`, false},
		// the length tail beyond the 1000 characters after the head
		{head + strings.Repeat("x", 1001) + " Expected a string with maximum length 64, but got a string with length 428 instead.", false},
	}
	for _, c := range in {
		if g := itemIDRefusal([]byte(c.s)); g != c.want {
			t.Errorf("%d bytes: %v, want %v", len(c.s), g, c.want)
		}
	}
}
