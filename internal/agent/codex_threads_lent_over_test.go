package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
)

// Lice9735 on X, the Codex app on Windows: a chat from 25 days before
// answered "模型提供商不匹配 — 此聊天使用的是 OpenAI，但你当前的提供商
// 是 magpie。请切换至 OpenAI 以继续", and only a new chat worked. Codex
// had been on a provider table of the user's own (openai-http) before
// magpie became its provider, so bringOver moved that provider's threads
// to magpie and left OpenAI's, which the Codex app lists and refuses to
// go on with on magpie. Now OpenAI's come too, recorded as lendOwn's are
// (codex.lent, to the user's provider): the Disconnect gives them to that
// provider, where Codex resumes them, and Codex on OpenAI again has them
// back on OpenAI.
func TestCodexOpenAIThreadsFollowMagpieFromOwnProvider(t *testing.T) {
	const (
		plain  = "019a0000-0000-7000-8000-0000000000f1"
		packed = "019a0000-0000-7000-8000-0000000000f2"
		theirs = "019a0000-0000-7000-8000-0000000000f3"
	)
	table := "[model_providers.openai-http]\nname = \"OpenAI\"\nbase_url = \"https://api.openai.com/v1\"\nwire_api = \"responses\"\nservice_tier = \"flex\"\n"
	t.Run("moved and back", func(t *testing.T) {
		home, read := codexHome(t, "", "model = \"gpt-5.4\"\nmodel_provider = \"openai-http\"\n\n"+table)
		dir := filepath.Join(home, ".codex")
		cfgPath := filepath.Join(dir, "config.toml")
		plainPath := codexThreadOn(t, dir, plain, "openai")
		packedPath := codexThreadOn(t, dir, packed, "openai")
		packThread(t, packedPath)
		codexThreadOn(t, dir, theirs, "openai-http")
		cx := codex(home)
		if err := cx.Fields[0].Set("fake/m1"); err != nil {
			t.Fatal(err)
		}
		if p, _ := edit.GetTOMLTop(cfgPath, "model_provider"); p != "magpie" {
			t.Fatalf("provider %q, want magpie:\n%s", p, read())
		}
		for id, want := range map[string]string{plain: "magpie", packed: "magpie", theirs: "magpie"} {
			if p := codexRowOn(t, dir, id); p != want {
				t.Errorf("connected: thread %s on %q, want %q", id, p, want)
			}
		}
		if b, _ := os.ReadFile(plainPath); !strings.Contains(string(b), `"model_provider":"magpie"`) {
			t.Fatalf("rollout still on OpenAI, which the Codex app reads for a legacy thread:\n%s", b)
		}
		if p := zstMetaProvider(t, packedPath+".zst"); p != "magpie" {
			t.Fatalf("packed rollout on %q, want magpie", p)
		}
		var l struct {
			To  string   `json:"to"`
			IDs []string `json:"ids"`
		}
		json.Unmarshal([]byte(stashLoad()[here(home).key("codex.lent")]), &l)
		if l.To != "openai-http" || !slices.Contains(l.IDs, plain) || !slices.Contains(l.IDs, packed) || slices.Contains(l.IDs, theirs) {
			t.Fatalf("codex.lent %+v, want OpenAI's two lent to openai-http", l)
		}
		// a sync leaves them so
		if err := cx.Sync(); err != nil {
			t.Fatal(err)
		}
		if p := codexRowOn(t, dir, plain); p != "magpie" {
			t.Fatalf("after a sync: %q", p)
		}
		// the Disconnect puts openai-http back, and every thread on magpie
		// goes there, where Codex resumes it
		if err := cx.Unwire(); err != nil {
			t.Fatal(err)
		}
		if p, _ := edit.GetTOMLTop(cfgPath, "model_provider"); p != "openai-http" {
			t.Fatalf("provider %q after the Disconnect, want openai-http back:\n%s", p, read())
		}
		for id, want := range map[string]string{plain: "openai-http", packed: "openai-http", theirs: "openai-http"} {
			if p := codexRowOn(t, dir, id); p != want {
				t.Errorf("disconnected: thread %s on %q, want %q", id, p, want)
			}
		}
		// on OpenAI again: OpenAI's go back to it, openai-http's stay
		if err := edit.DelTOMLTop(cfgPath, "model_provider"); err != nil {
			t.Fatal(err)
		}
		if err := cx.Sync(); err != nil {
			t.Fatal(err)
		}
		for id, want := range map[string]string{plain: "openai", packed: "openai", theirs: "openai-http"} {
			if p := codexRowOn(t, dir, id); p != want {
				t.Errorf("back on OpenAI: thread %s on %q, want %q", id, p, want)
			}
		}
		if _, ok := stashLoad()[here(home).key("codex.lent")]; ok {
			t.Error("record left after the threads went back")
		}
	})
	// a profile naming OpenAI is the user's way to go on with its threads
	t.Run("a profile names OpenAI", func(t *testing.T) {
		home, _ := codexHome(t, "", "model = \"gpt-5.4\"\nmodel_provider = \"openai-http\"\n\n"+table+"\n[profiles.official]\nmodel_provider = \"openai\"\n")
		dir := filepath.Join(home, ".codex")
		codexThreadOn(t, dir, plain, "openai")
		codexThreadOn(t, dir, theirs, "openai-http")
		cx := codex(home)
		if err := cx.Fields[0].Set("fake/m1"); err != nil {
			t.Fatal(err)
		}
		cx.Sync()
		for id, want := range map[string]string{plain: "openai", theirs: "magpie"} {
			if p := codexRowOn(t, dir, id); p != want {
				t.Errorf("thread %s on %q, want %q", id, p, want)
			}
		}
	})
}

// The threads a move rewrites go packedBudget bytes a sync, and the rest
// waited for the next one, which may be hours away: the Codex app refused
// each one left all that while. A move the budget stopped asks for the
// next sync at once.
func TestCodexThreadsLeftByTheBudgetAskAgain(t *testing.T) {
	const (
		a = "019a0000-0000-7000-8000-0000000000f4"
		b = "019a0000-0000-7000-8000-0000000000f5"
	)
	home, _ := codexHome(t, "", "model = \"gpt-5.4\"\n")
	dir := filepath.Join(home, ".codex")
	for _, id := range []string{a, b} {
		packThread(t, codexThreadOn(t, dir, id, "openai"))
	}
	asked := 0
	oldAgain, oldBudget := codexThreadsAgain, packedBudget
	codexThreadsAgain = func() { asked++ }
	packedBudget = 1
	t.Cleanup(func() { codexThreadsAgain, packedBudget = oldAgain, oldBudget })
	cx := codex(home)
	if err := cx.Fields[0].Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	on := func() (n int) {
		for _, id := range []string{a, b} {
			if codexRowOn(t, dir, id) == "magpie" {
				n++
			}
		}
		return n
	}
	if n := on(); n != 1 {
		t.Fatalf("%d thread(s) moved at the switch, want one of two at a 1-byte budget", n)
	}
	if asked != 1 {
		t.Fatalf("%d sync(s) asked for with a thread left, want 1", asked)
	}
	// the sync asked for moves the other, and asks for none past it
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if n := on(); n != 2 {
		t.Fatalf("%d thread(s) on magpie after the next sync, want both", n)
	}
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if asked != 1 {
		t.Fatalf("%d syncs asked for, want none once nothing is left", asked)
	}
}
