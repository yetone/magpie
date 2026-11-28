package provider

import (
	"encoding/json"
	"maps"
	"slices"
	"testing"
)

// TestAccountSettingsKept: what the user sets on a signed-in account's
// provider is written by Save and read back by Find, every setting alike.
// Save and withPicks kept two lists, and a setting on one alone was lost
// (#4738: `magpie provider set codex additional-tools=on` was on neither,
// so the setting seemed taken and nothing changed; an account's extraBody
// and reply headers were read back but never written; its rests were
// written but never read back, so the next Save cleared them).
func TestAccountSettingsKept(t *testing.T) {
	signIn(t)
	for _, id := range []string{"codex", "copilot"} {
		p, err := Find(id)
		if err != nil {
			t.Fatal(err)
		}
		q := *p
		q.AdditionalTools = true
		q.ExtraBody = ExtraBody{"service_tier": json.RawMessage(`"priority"`)}
		q.ReplyHeaders = []string{"X-Trace-Id"}
		q.QuotaRest = 90
		q.RestCap = 20
		q.LimitRestOff = true
		q.Patience = 30
		q.FirstToken = 45
		if err := Save(q); err != nil {
			t.Fatal(err)
		}
		got, err := Find(id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Account == nil {
			t.Fatalf("%s: no longer the signed-in account: %+v", id, got)
		}
		if !got.AdditionalTools {
			t.Errorf("%s: additional-tools not kept", id)
		}
		if !maps.EqualFunc(got.ExtraBody, q.ExtraBody, func(a, b json.RawMessage) bool { return string(a) == string(b) }) {
			t.Errorf("%s: extraBody %v, want %v", id, got.ExtraBody, q.ExtraBody)
		}
		if !slices.Equal(got.ReplyHeaders, q.ReplyHeaders) {
			t.Errorf("%s: reply headers %v, want %v", id, got.ReplyHeaders, q.ReplyHeaders)
		}
		if got.QuotaRest != 90 || got.RestCap != 20 || !got.LimitRestOff || got.Patience != 30 || got.FirstToken != 45 {
			t.Errorf("%s: rests %d %d %v, patience %d, first token %d", id, got.QuotaRest, got.RestCap, got.LimitRestOff, got.Patience, got.FirstToken)
		}
		// set back, each is the default again
		r := *got
		r.AdditionalTools, r.ExtraBody, r.ReplyHeaders = false, nil, nil
		r.QuotaRest, r.RestCap, r.LimitRestOff, r.Patience, r.FirstToken = 0, 0, false, 0, 0
		if err := Save(r); err != nil {
			t.Fatal(err)
		}
		if got, _ := Find(id); got.AdditionalTools || len(got.ExtraBody) > 0 || len(got.ReplyHeaders) > 0 || got.QuotaRest != 0 || got.RestCap != 0 || got.LimitRestOff || got.Patience != 0 || got.FirstToken != 0 {
			t.Errorf("%s: left after clearing: %+v", id, got)
		}
	}
}
