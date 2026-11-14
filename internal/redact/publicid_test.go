package redact

import (
	"encoding/json"
	"strings"
	"testing"
)

// bvScript is the tool call jimlee0824v2 posted on X: a script fetching
// nine Bilibili videos by their BV ids, five of which went to the vendor
// as {{SECRET_…}} (BV1VMnk68E9Q, BV1JvaW6JEMb, BV19gp16kEAc, BV1kLn36WEN6,
// BV1wvJB6aEp5).
const bvScript = `{"command":"python3 -c \"\nimport urllib.request, json\n\nbvs = [\n    'BV1f94c6uEr3', 'BV1bqx76wEUM', 'BV1hWar6SEVP',\n    'BV1bAxg6GEX8', 'BV1VMnk68E9Q',\n    'BV1JvaW6JEMb',\n    'BV19gp16kEAc',\n    'BV1kLn36WEN6', 'BV1wvJB6aEp5'\n]\n\nfor bv in bvs:\n    url = f'https://api.bilibili.com/x/web-interface/view?bvid={bv}'\n    req = urllib.request.Request(url, headers={'User-Agent': 'Mozilla/5.0'})\n\""}`

var bvIDs = []string{
	"BV1f94c6uEr3", "BV1bqx76wEUM", "BV1hWar6SEVP", "BV1bAxg6GEX8", "BV1VMnk68E9Q",
	"BV1JvaW6JEMb", "BV19gp16kEAc", "BV1kLn36WEN6", "BV1wvJB6aEp5",
}

// A public video's id goes as written whatever the local model takes it
// for: a Bilibili BV id or av number by its own shape, a YouTube video's,
// playlist's or channel's id by its link. No rule takes one either. A
// secret of the same length, a near-BV with a letter base58 has none of,
// and a key after a YouTube link's key= are masked still. Each case
// starts with nothing known, so one label's mask isn't the next one's
// known value.
func TestPublicVideoIDGoesAsWritten(t *testing.T) {
	all := map[string]bool{}
	for _, c := range Categories {
		all[c.ID] = true
	}
	type tc struct{ text, value string }
	var keep []tc
	for _, id := range bvIDs {
		keep = append(keep, tc{bvScript, id})
	}
	keep = append(keep,
		tc{"看一下 https://www.bilibili.com/video/BV1VMnk68E9Q/?spm_id_from=333.1007 这个视频", "BV1VMnk68E9Q"},
		tc{"老视频 av170001 还在", "av170001"},
		tc{"https://www.youtube.com/watch?v=dQw4w9WgXcQ&t=42s", "dQw4w9WgXcQ"},
		tc{"see https://youtu.be/dQw4w9WgXcQ?si=x", "dQw4w9WgXcQ"},
		tc{"https://m.youtube.com/shorts/aqz-KE-bpKQ", "aqz-KE-bpKQ"},
		tc{`<iframe src="https://www.youtube-nocookie.com/embed/M7lc1UVf-VE">`, "M7lc1UVf-VE"},
		tc{"https://www.youtube.com/playlist?list=PLFgquLnL59alCl_2TQvOiD5Vgm1hCaGSI", "PLFgquLnL59alCl_2TQvOiD5Vgm1hCaGSI"},
		tc{"https://www.youtube.com/watch?v=dQw4w9WgXcQ&list=PLFgquLnL59alCl_2TQvOiD5Vgm1hCaGSI", "PLFgquLnL59alCl_2TQvOiD5Vgm1hCaGSI"},
		tc{"https://www.youtube.com/channel/UC_x5XG1OV2P6uZZ5FSM9Ttw", "UC_x5XG1OV2P6uZZ5FSM9Ttw"},
	)
	for _, c := range keep {
		Forget()
		if out, n := Mask(c.text, Options{Secrets: true, Personal: true, Kinds: all}); n != 0 {
			t.Errorf("%q by the rules: masked %q", c.value, out)
		}
		for _, label := range []string{"secret", "account_number"} {
			Forget()
			f := &fakeModel{values: map[string]string{c.value: label}}
			if out, _ := Mask(c.text, Options{Secrets: true, Personal: true, Kinds: all, Model: f.find}); !strings.Contains(out, c.value) {
				t.Errorf("%q as %s: masked in %q", c.value, label, out)
			}
		}
	}

	for _, c := range []tc{
		// a secret as long as a BV id
		{"the vault code is Xq7vR2mK9pLs", "Xq7vR2mK9pLs"},
		// BV1 and a 0, an O, an I and an l: no base58, no BV id
		{"ref BV10OIl6aEp5 again", "BV10OIl6aEp5"},
		// as long as a BV id, BV1 and one more letter than a BV id has
		{"ref BV1wvJB6aEp5x again", "BV1wvJB6aEp5x"},
		// an 11-letter run that is no YouTube link's id
		{"the token is dQw4w9WgXcQ", "dQw4w9WgXcQ"},
		{"https://example.com/watch?v=dQw4w9WgXcQ", "dQw4w9WgXcQ"},
		{"https://www.youtube.com/watch?v=dQw4w9WgXcQ&key=Rk7vT2xLk9RbW4nZpE6s", "Rk7vT2xLk9RbW4nZpE6s"},
	} {
		Forget()
		f := &fakeModel{values: map[string]string{c.value: "secret"}}
		if out, _ := Mask(c.text, Options{Secrets: true, Personal: true, Model: f.find}); strings.Contains(out, c.value) {
			t.Errorf("%q: left in %q", c.value, out)
		}
	}
	// a key whose name is before it is the rules', in a BV id's shape too
	Forget()
	if out, _ := Mask("api_key=BV1VMnk68E9Q", Options{Secrets: true}); strings.Contains(out, "BV1VMnk68E9Q") {
		t.Errorf("named key left in %q", out)
	}
}

// The reporter's tool call, as MaskJSON sends it: every BV id goes as
// written with the model taking each for a secret.
func TestBVScriptToolCallKeepsIDs(t *testing.T) {
	Forget()
	values := map[string]string{}
	for _, id := range bvIDs {
		values[id] = "secret"
	}
	f := &fakeModel{values: values}
	args, _ := json.Marshal(bvScript)
	body := `{"messages":[{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"bash","arguments":` + string(args) + `}}]}]}`
	out, _ := MaskJSON([]byte(body), Options{Secrets: true, Personal: true, Model: f.find})
	for _, id := range bvIDs {
		if !strings.Contains(string(out), id) {
			t.Errorf("%s masked: %s", id, out)
		}
	}
}
