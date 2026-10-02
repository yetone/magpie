package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

// claudePickerHome is a home whose models.dev lists Anthropic's models as it
// does now, some under an alias and a dated snapshot both, and whose Claude
// Code has settings.
func claudePickerHome(t *testing.T, settings string) (home, path string) {
	t.Helper()
	home, path = claudeSettings(t, settings)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	writeFile(t, catalog.CachePath(), `{"anthropic":{"models":{
		"claude-opus-5-5":{"id":"claude-opus-5-5","name":"Claude Opus 5.5","release_date":"2026-09-22","limit":{"context":1000000}},
		"claude-sonnet-5":{"id":"claude-sonnet-5","name":"Claude Sonnet 5","release_date":"2026-06-29","limit":{"context":1000000}},
		"claude-opus-4-5-20251101":{"id":"claude-opus-4-5-20251101","name":"Claude Opus 4.5","release_date":"2025-11-24","limit":{"context":200000}},
		"claude-opus-4-5":{"id":"claude-opus-4-5","name":"Claude Opus 4.5 (latest)","release_date":"2025-11-24","limit":{"context":200000}},
		"claude-haiku-4-5-20251001":{"id":"claude-haiku-4-5-20251001","name":"Claude Haiku 4.5","release_date":"2025-10-15","limit":{"context":200000}},
		"claude-haiku-4-5":{"id":"claude-haiku-4-5","name":"Claude Haiku 4.5 (latest)","release_date":"2025-10-15","limit":{"context":200000}}
	}}}`)
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	return home, path
}

// claudeSignedIn signs home's Claude Code in to me@example.com, as its
// sign-in and ~/.claude.json say, with an inert claude (and, on a Mac,
// security) first on PATH: magpie names the account from the file and never
// asks the machine's own Claude Code, nor its keychain.
func claudeSignedIn(t *testing.T, home string) {
	t.Helper()
	noKeychain(t)
	bin := t.TempDir()
	for name, body := range map[string]string{"claude": "#!/bin/sh\nexit 1\n", "claude.cmd": "@exit /b 1\r\n"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	writeFile(t, filepath.Join(home, ".claude", ".credentials.json"), claudeOAuth("tok-me"))
	writeFile(t, filepath.Join(home, ".claude.json"), `{"oauthAccount":{"emailAddress":"me@example.com"}}`)
	provider.ForgetAccounts()
	t.Cleanup(provider.ForgetAccounts)
}

func claudeOAuth(tok string) string {
	return fmt.Sprintf(`{"claudeAiOauth":{"accessToken":%q,"refreshToken":"r-%s","expiresAt":%d,"subscriptionType":"max"}}`,
		tok, tok, time.Now().Add(24*time.Hour).UnixMilli())
}

// TestClaudeSnapshotOneRow (#496): models.dev lists Claude Opus, Sonnet and
// Haiku 4.5 under an alias and a dated snapshot both, and Claude Code's
// picker had a row for each. Each is one row now, the alias's — or, while
// Claude Code is set to the snapshot, the snapshot's, so the value set is
// still the one shown — and whichever is picked is written as it is.
func TestClaudeSnapshotOneRow(t *testing.T) {
	home, path := claudePickerHome(t, `{}`)
	a := claude(home)
	// own is the rows of Claude Code's own models; the value it is set to
	// is one of the options, so the picker shows it as set
	own := func() string {
		t.Helper()
		var out []string
		v, offered := a.Field("model").Get(), false
		for _, o := range a.Field("model").Options(a.Values()) {
			offered = offered || o.Value == v
			if o.Direct == "Anthropic" {
				out = append(out, o.Value)
			}
		}
		if v != "" && !offered {
			t.Fatalf("%q isn't offered, so the picker can't show it as set", v)
		}
		return strings.Join(out, " ")
	}
	const all = "claude-opus-5-5 claude-sonnet-5 claude-opus-4-5 claude-haiku-4-5"
	for _, c := range []struct{ file, want string }{
		{`{}`, all},
		{`{"model":"claude-opus-4-5"}`, all},
		{`{"model":"claude-opus-4-5-20251101"}`, "claude-opus-5-5 claude-sonnet-5 claude-opus-4-5-20251101 claude-haiku-4-5"},
		{`{"model":"claude-haiku-4-5-20251001"}`, "claude-opus-5-5 claude-sonnet-5 claude-opus-4-5 claude-haiku-4-5-20251001"},
		// one of Claude Code's aliases first, as before
		{`{"model":"haiku"}`, "haiku " + all},
	} {
		writeFile(t, path, c.file)
		if got := own(); got != c.want {
			t.Errorf("%s: own rows %q, want %q", c.file, got, c.want)
		}
	}
	// a snapshot models.dev doesn't list leaves the alias its row, and is
	// shown as typed, as before
	writeFile(t, path, `{"model":"claude-haiku-4-5-20990101"}`)
	var got []string
	for _, o := range a.Field("model").Options(a.Values()) {
		if o.Direct == "Anthropic" {
			got = append(got, o.Value)
		}
	}
	if strings.Join(got, " ") != all {
		t.Errorf("a snapshot not listed: own rows %q, want %q", got, all)
	}
	// either way, what is picked is what Claude Code is set to
	for _, v := range []string{"claude-opus-4-5-20251101", "claude-opus-4-5"} {
		if err := a.Field("model").Set(v); err != nil {
			t.Fatal(err)
		}
		if got, _ := edit.GetJSON(path, "model"); got != v {
			t.Fatalf("picked %s, settings.json says %q", v, got)
		}
		own()
	}
}

// TestClaudeOwnAccountFolded (#496): signed in to Claude Code, magpie's Claude
// subscription is the same account, and Claude Code's picker listed each of
// its models twice, as Claude Code's own and through magpie (Codex's leaves
// its own subscription out: viaMagpieFor). The ones through magpie are now
// offered folded, picked as before; not when they reach another account than
// Claude Code's own — its sign-in paused or another account on beside it,
// Claude Code signed out, Claude Code's own models going to a relay, a
// distro's Claude Code — nor while Claude Code is set to one of them.
func TestClaudeOwnAccountFolded(t *testing.T) {
	home, path := claudePickerHome(t, `{}`)
	if err := provider.Save(provider.Provider{ID: "v", Name: "V", Chat: "https://example.test/v1", Key: "k", Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	claudeSignedIn(t, home)
	a := claude(home)
	model := a.Field("model")
	// folded is what the picker folds: each value, and whether it is folded
	folded := func(f *Field) map[string]bool {
		t.Helper()
		out := map[string]bool{}
		for _, o := range f.Options(a.Values()) {
			out[o.Value] = o.Folded
			if o.Folded && (!strings.HasPrefix(o.Ref, "claude/") || o.Group != "Claude Code" || o.Note != "me@example.com · via magpie") {
				t.Fatalf("folded: %+v", o)
			}
		}
		return out
	}
	sub := []string{"claude/claude-opus-5-5[1m]", "claude/claude-sonnet-5[1m]", "claude/claude-opus-4-5", "claude/claude-haiku-4-5"}
	want := func(name string, on bool) {
		t.Helper()
		got := folded(model)
		for _, v := range sub {
			if f, ok := got[v]; !ok || f != on {
				t.Fatalf("%s: %s offered %v, folded %v, want folded %v: %v", name, v, ok, f, on, got)
			}
		}
		for _, v := range []string{"claude-opus-5-5", "claude-haiku-4-5", "v/m"} {
			if f, ok := got[v]; !ok || f {
				t.Fatalf("%s: %s offered %v, folded %v: %v", name, v, ok, f, got)
			}
		}
	}

	want("signed in alone", true)
	// still the models magpie serves, a typed one spelt as the picker has it
	if v, err := a.Spell("model", "magpie/claude/claude-sonnet-5"); err != nil || v != "claude/claude-sonnet-5[1m]" {
		t.Fatalf("spelt %q, %v", v, err)
	}

	// picked, Claude Code goes through magpie to it as ever, and it is shown
	// as what Claude Code is set to: nothing is folded while it is
	if err := model.Set("claude/claude-opus-5-5"); err != nil {
		t.Fatal(err)
	}
	env := func(k string) string { v, _ := edit.GetJSON(path, "env."+k); return v }
	if env("ANTHROPIC_BASE_URL") != gateway.URL() || env("ANTHROPIC_MODEL") != "claude/claude-opus-5-5[1m]" || model.Get() != "claude/claude-opus-5-5[1m]" {
		t.Fatalf("picked through magpie:\n%s", readFile(path))
	}
	want("set to one", false)
	for _, tier := range []string{"opus", "subagent"} {
		for v, f := range folded(a.Field(tier)) {
			if f {
				t.Fatalf("%s: %s folded", tier, v)
			}
		}
	}
	if err := model.Set("claude-opus-5-5"); err != nil {
		t.Fatal(err)
	}
	if env("ANTHROPIC_BASE_URL") != "" || model.Get() != "claude-opus-5-5" {
		t.Fatalf("back on its own:\n%s", readFile(path))
	}
	want("back on its own", true)

	// another account on beside it: magpie may answer from that one
	logins := filepath.Join(filepath.Dir(provider.Path()), "logins.json")
	writeFile(t, logins, `[{"agent":"claude","user":"me@example.com","plan":"max","on":true,"auth":`+claudeOAuth("tok-me")+`},
		{"agent":"claude","user":"spare@example.com","plan":"max","on":true,"auth":`+claudeOAuth("tok-spare")+`}]`)
	want("another account on", false)
	// and Claude Code's own paused for it
	if err := provider.SetLoginOn("claude", "me@example.com", false); err != nil {
		t.Fatal(err)
	}
	want("its own paused", false)
	writeFile(t, logins, `[{"agent":"claude","user":"me@example.com","plan":"max","on":true,"auth":`+claudeOAuth("tok-me")+`},
		{"agent":"claude","user":"spare@example.com","plan":"max","auth":`+claudeOAuth("tok-spare")+`}]`)
	want("the other off again", true)

	// Claude Code's own models through a relay of the user's
	writeFile(t, path, `{"env":{"ANTHROPIC_BASE_URL":"https://relay.example.test"}}`)
	for v, f := range folded(model) {
		if f {
			t.Fatalf("own models to a relay: %s folded", v)
		}
	}
	writeFile(t, path, `{}`)

	// a distro's Claude Code, whose sign-in magpie doesn't read
	distro := claudeIn(place{home: t.TempDir(), id: "claude@wsl:Debian"})
	for _, o := range distro.Field("model").Options(nil) {
		if o.Folded {
			t.Fatalf("a distro's Claude Code: %+v", o)
		}
	}

	// Claude Code signed out, magpie serves a saved account in its place
	os.Remove(filepath.Join(home, ".claude", ".credentials.json"))
	provider.ForgetAccounts()
	if p, err := provider.Find("claude"); err != nil || p.Account == nil {
		t.Fatalf("no account served in Claude Code's place: %v", err)
	}
	want("signed out", false)
}
