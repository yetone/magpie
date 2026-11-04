package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
)

// memoBody is a request body past validMemoMin, unique to the test.
func memoBody(tag string) []byte {
	return []byte(`{"model":"` + tag + `","messages":[{"role":"user","content":"` +
		strings.Repeat("a long prompt ", 6000) + `"}]}`)
}

// countScans counts jsonValid's byte-by-byte checks of bodies equal to b.
func countScans(t *testing.T, b []byte) *atomic.Int32 {
	var n atomic.Int32
	f := func(data []byte) {
		if bytes.Equal(data, b) {
			n.Add(1)
		}
	}
	validScanned.Store(&f)
	t.Cleanup(func() { validScanned.Store(nil) })
	return &n
}

// A long body decoded by each reader of a request is checked for valid
// JSON once, not by every decodeJSON: at 2 MB each check cost ~2.5ms, and
// a request's body went through a dozen of them.
func TestDecodeJSONChecksALongBodyOnce(t *testing.T) {
	body := memoBody(t.Name())
	scans := countScans(t, body)
	for range 6 {
		var v struct {
			Model string `json:"model"`
		}
		if err := decodeJSON(body, &v); err != nil || v.Model != t.Name() {
			t.Fatalf("decode: %v %q", err, v.Model)
		}
	}
	if n := scans.Load(); n != 1 {
		t.Fatalf("a %d-byte body was checked %d times over six decodes, want 1", len(body), n)
	}
}

// A member of a body being served, decoded alone (a Responses request's
// input, by each of the request's readers, two of them at once), is not
// checked byte by byte again: the body was found valid whole. A 516 MB
// Codex turn of generated images was checked three times over before
// routing (#5281). A part of the body that isn't a member's whole value is
// still checked, and refused when it isn't valid.
func TestDecodeJSONHeldMemberNotCheckedAgain(t *testing.T) {
	body := []byte(`{"model":"` + t.Name() + `","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"` +
		strings.Repeat("a long prompt ", 6000) + `"}]}]}`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	holdBody(ctx, body)
	var whole, parts atomic.Int32
	f := func(data []byte) {
		if bytes.Equal(data, body) {
			whole.Add(1)
		} else {
			parts.Add(1)
		}
	}
	validScanned.Store(&f)
	t.Cleanup(func() { validScanned.Store(nil) })
	var q struct {
		Model string          `json:"model"`
		Input json.RawMessage `json:"input"`
	}
	if err := decodeJSON(body, &q); err != nil || q.Model != t.Name() {
		t.Fatalf("decode: %v %q", err, q.Model)
	}
	for range 3 {
		var s string
		if decodeJSON(q.Input, &s) == nil {
			t.Fatal("an array decoded as a string")
		}
		var items []struct {
			Role string `json:"role"`
		}
		if err := decodeJSON(q.Input, &items); err != nil || len(items) != 1 || items[0].Role != "user" {
			t.Fatalf("input: %v %+v", err, items)
		}
		if !jsonValid(q.Input) {
			t.Fatal("input not valid")
		}
	}
	if w, p := whole.Load(), parts.Load(); w != 1 || p != 0 {
		t.Fatalf("the body was checked %d times and its input %d, want 1 and 0", w, p)
	}
	// a part that is no member's whole value: checked, and refused
	if cut := q.Input[:len(q.Input)-1]; jsonValid(cut) {
		t.Fatal("a cut input was found valid")
	}
	if parts.Load() != 1 {
		t.Fatalf("a cut input was checked %d times, want 1", parts.Load())
	}
}

// A long body's top level is split at its members once, not by each decode
// of it: each reader of a request (the model, the stream flag, the
// messages, the tools…) walked every byte of the conversation again to
// find its member. What each is given is what encoding/json gives it.
func TestDecodeJSONSplitsALongBodyOnce(t *testing.T) {
	body := []byte(`{"model":"` + t.Name() + `","stream":true,"max_tokens":9,"messages":[{"role":"user","content":"` +
		strings.Repeat("a long prompt ", 6000) + `"}],"metadata":{"user_id":"u"}}`)
	var n atomic.Int32
	f := func(data []byte) {
		if bytes.Equal(data, body) {
			n.Add(1)
		}
	}
	topSplit.Store(&f)
	t.Cleanup(func() { topSplit.Store(nil) })
	type msgs struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	for range 4 {
		var a struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		var b msgs
		var c *msgs
		var d map[string]json.RawMessage
		var e struct {
			Metadata struct {
				UserID string `json:"user_id"`
			} `json:"metadata"`
			Max int `json:"max_tokens"`
		}
		for _, dst := range []any{&a, &b, &c, &d, &e} {
			if err := decodeJSON(body, dst); err != nil {
				t.Fatal(err)
			}
		}
		wa, wb, we := a, msgs{}, e
		wa.Model, wa.Stream, we.Metadata.UserID, we.Max = "", false, "", 0
		var wd map[string]json.RawMessage
		json.Unmarshal(body, &wa)
		json.Unmarshal(body, &wb)
		json.Unmarshal(body, &wd)
		json.Unmarshal(body, &we)
		if a != wa || len(b.Messages) != 1 || b.Messages[0] != wb.Messages[0] || c == nil || len(c.Messages) != 1 ||
			len(d) != len(wd) || string(d["messages"]) != string(wd["messages"]) || e != we {
			t.Fatalf("decoded otherwise than encoding/json: %+v %+v %d %v", a, e, len(d), c == nil)
		}
	}
	if got := n.Load(); got != 1 {
		t.Fatalf("a %d-byte body's top level was split %d times over 20 decodes, want 1", len(body), got)
	}
}

// What is remembered is a valid body: an invalid one, or a valid one
// changed by a byte at the same length, is checked again and refused.
func TestDecodeJSONMemoKeepsRefusing(t *testing.T) {
	body := memoBody(t.Name())
	var v map[string]any
	if err := decodeJSON(body, &v); err != nil {
		t.Fatal(err)
	}
	bad := bytes.Clone(body)
	bad[len(bad)-1] = '!'
	for range 3 {
		var w map[string]any
		if err := decodeJSON(bad, &w); err == nil {
			t.Fatal("a body with a broken end decoded")
		}
	}
	trunc := body[:len(body)-1]
	var w map[string]any
	if err := decodeJSON(trunc, &w); err == nil {
		t.Fatal("a truncated body decoded")
	}
	if !jsonValid(body) || jsonValid(bad) {
		t.Fatal("jsonValid's answer changed")
	}
}
