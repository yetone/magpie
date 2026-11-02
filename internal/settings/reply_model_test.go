package settings

import (
	"encoding/json"
	"os"
	"testing"
)

// A reply's model name is one setting of three (ReplyModel, ZoeCats on
// Discord). A file from before it, with memberModel on, still names the
// member; memberModel is written beside replyModel for such a magpie, and
// the model asked for is "" whichever way it is sent.
func TestReplyModelFoldsMemberModel(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	file := func() map[string]any {
		t.Helper()
		b, err := os.ReadFile(Path())
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	for _, c := range []struct {
		file string
		want string
	}{
		{`{"memberModel":true}`, ReplyMember},
		{`{"memberModel":false}`, ""},
		{`{}`, ""},
		// replyModel, once set, is the user's choice whatever memberModel says
		{`{"replyModel":"vendor","memberModel":true}`, ReplyVendor},
		{`{"replyModel":"asked","memberModel":true}`, ""},
		{`{"replyModel":"member"}`, ReplyMember},
	} {
		if err := os.WriteFile(Path(), []byte(c.file), 0o644); err != nil {
			t.Fatal(err)
		}
		if s := Load(); s.ReplyModel != c.want || s.MemberModel != (c.want == ReplyMember) {
			t.Errorf("%s reads as replyModel %q memberModel %v, want %q", c.file, s.ReplyModel, s.MemberModel, c.want)
		}
		if s := Shared(); s.ReplyModel != c.want {
			t.Errorf("%s is shared as replyModel %q, want %q", c.file, s.ReplyModel, c.want)
		}
	}

	for _, c := range []struct {
		reply  string
		member bool // memberModel as it is sent
		want   string
	}{
		{ReplyVendor, false, ReplyVendor},
		{ReplyVendor, true, ReplyVendor},
		{ReplyMember, false, ReplyMember},
		{ReplyAsked, true, ""},
		{"", true, ReplyMember}, // as a magpie before replyModel sends it
		{"", false, ""},
	} {
		if err := Save(Settings{ReplyModel: c.reply, MemberModel: c.member}); err != nil {
			t.Fatalf("%q: %v", c.reply, err)
		}
		m := file()
		if got, _ := m["replyModel"].(string); got != c.want {
			t.Errorf("%q, memberModel %v: saved replyModel %q, want %q", c.reply, c.member, got, c.want)
		}
		if got, _ := m["memberModel"].(bool); got != (c.want == ReplyMember) {
			t.Errorf("%q, memberModel %v: saved memberModel %v beside replyModel %q", c.reply, c.member, got, c.want)
		}
	}

	if err := Save(Settings{ReplyModel: ReplyVendor}); err != nil {
		t.Fatal(err)
	}
	if err := Save(Settings{ReplyModel: "Vendor's"}); err == nil {
		t.Error("a reply model name of no known kind was saved")
	}
	if s := Load(); s.ReplyModel != ReplyVendor {
		t.Errorf("a refused value changed replyModel to %q", s.ReplyModel)
	}
}
