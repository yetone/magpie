package gateway

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

// seeing saves a provider whose one enabled model, eye, sees, at price
// a million tokens in and out.
func seeing(t *testing.T, p provider.Provider, price float64) {
	t.Helper()
	p.Name, p.Key, p.Models = p.ID, p.ID+"-key", []string{"eye"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	base := p.Responses + p.Anthropic + p.Chat
	if err := catalog.SaveLive(p.ID, base, []catalog.Model{
		{ID: "eye", Images: true, ImageInput: imageInputBool(true), Price: &catalog.Price{Input: price, Output: price}},
	}); err != nil {
		t.Fatal(err)
	}
}

func heldFile(t *testing.T) autoPicksFile {
	t.Helper()
	b, err := os.ReadFile(autoPicksPath())
	if err != nil {
		t.Fatal(err)
	}
	var f autoPicksFile
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func autoSearcherID() string {
	p, _, ok := autoSearcher()
	if !ok {
		return ""
	}
	return p.ID
}

// A provider added while Image recognition and Web search are left on
// Automatic takes neither over (hxy920830 on X: a Claude account added
// for account switching alone took every agent's images and searches).
// What served them goes on serving them; one added later serves them
// only once it can't, and a model picked in Settings is still the one
// used.
func TestAddedProviderTakesNoAutomaticPick(t *testing.T) {
	fresh(t)
	// OpenAI's API: searches by itself (rank 3), sees at 5
	seeing(t, provider.Provider{ID: "oai", Responses: "https://api.openai.com/v1"}, 5)
	seedAutoPicks()
	if f := heldFile(t); !f.Seeded || f.Vision != "oai" || f.Searcher != "oai" {
		t.Fatalf("held %+v, want oai for both", f)
	}

	// Anthropic's API, added later: ranks before it for both (rank 2,
	// cheaper), as a Claude account ranks first
	seeing(t, provider.Provider{ID: "anth", Anthropic: "https://api.anthropic.com"}, 1)
	if m := AutoVision(); m != "oai/eye" {
		t.Fatalf("AutoVision = %q after a provider was added, want oai/eye still", m)
	}
	if id := autoSearcherID(); id != "oai" {
		t.Fatalf("searcher = %q after a provider was added, want oai still", id)
	}
	if s := AutoSearcher(); !strings.HasPrefix(s, "oai") {
		t.Fatalf("AutoSearcher = %q, Settings' Automatic would name another", s)
	}

	// the user's own pick is the one used
	st := settings.Load()
	st.Vision, st.Searcher = "anth/eye", "anth"
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	if m, _ := seer(); m != "anth/eye" {
		t.Fatalf("seer = %q with anth/eye picked", m)
	}
	if p, _, ok := searcher(); !ok || p.ID != "anth" {
		t.Fatalf("searcher = %q with anth picked", p.ID)
	}
	st.Vision, st.Searcher = "", ""
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}

	// the held provider turned off: the slot is empty, and the next fills it
	p, err := provider.Find("oai")
	if err != nil {
		t.Fatal(err)
	}
	p.Off = true
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	if m := AutoVision(); m != "anth/eye" {
		t.Fatalf("AutoVision = %q with oai off", m)
	}
	if id := autoSearcherID(); id != "anth" {
		t.Fatalf("searcher = %q with oai off", id)
	}
	if f := heldFile(t); f.Vision != "anth" || f.Searcher != "anth" {
		t.Fatalf("held %+v with oai off, want anth", f)
	}
	// and turned on again it doesn't take the slot back from anth
	p.Off = false
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	if m := AutoVision(); m != "anth/eye" {
		t.Fatalf("AutoVision = %q with oai back", m)
	}
}

// Before holds were kept, the provider added later took the slots: the
// first start with them gives each back to what served it before, as the
// ledger's Image description and Web search calls show, while it can
// still serve. One whose calls the ledger shows from the first row is
// kept, and so is the pick now when the one before is gone.
func TestAutoPicksSeededFromTheLedger(t *testing.T) {
	ago := func(h int) time.Time { return time.Now().Add(-time.Duration(h) * time.Hour) }
	call := func(at time.Time, kind, pid, source string) {
		usage.Append(usage.Record{Time: at, Agent: "magpie", Kind: kind, Provider: pid, Model: "eye", Source: source, Status: 200})
	}

	t.Run("taken over", func(t *testing.T) {
		fresh(t)
		seeing(t, provider.Provider{ID: "oai", Responses: "https://api.openai.com/v1"}, 5)
		seeing(t, provider.Provider{ID: "anth", Anthropic: "https://api.anthropic.com"}, 1)
		// anth's own search for its own model, before it was magpie's pick:
		// not a searcher's call
		call(ago(9), "web_search", "anth", usage.SourceSearch)
		call(ago(8), "vision", "oai", "")
		call(ago(7), "web_search", "oai", "")
		call(ago(6), "vision", "oai", "")
		call(ago(5), "web_search", "oai", "")
		// anth added: magpie picked it for both
		call(ago(2), "vision", "anth", "")
		call(ago(1), "web_search", "anth", "")
		seedAutoPicks()
		if f := heldFile(t); f.Vision != "oai" || f.Searcher != "oai" {
			t.Fatalf("held %+v, want oai given back for both", f)
		}
		if m := AutoVision(); m != "oai/eye" {
			t.Fatalf("AutoVision = %q", m)
		}
		if id := autoSearcherID(); id != "oai" {
			t.Fatalf("searcher = %q", id)
		}
	})

	t.Run("never taken over", func(t *testing.T) {
		fresh(t)
		seeing(t, provider.Provider{ID: "oai", Responses: "https://api.openai.com/v1"}, 5)
		seeing(t, provider.Provider{ID: "anth", Anthropic: "https://api.anthropic.com"}, 1)
		// anth served from the first call; oai only while anth was out
		call(ago(8), "vision", "anth", "")
		call(ago(6), "vision", "oai", "")
		call(ago(2), "vision", "anth", "")
		seedAutoPicks()
		if f := heldFile(t); f.Vision != "anth" || f.Searcher != "anth" {
			t.Fatalf("held %+v, want anth kept", f)
		}
	})

	t.Run("the one before gone", func(t *testing.T) {
		fresh(t)
		seeing(t, provider.Provider{ID: "anth", Anthropic: "https://api.anthropic.com"}, 1)
		call(ago(8), "vision", "removed", "")
		call(ago(2), "vision", "anth", "")
		seedAutoPicks()
		if f := heldFile(t); f.Vision != "anth" {
			t.Fatalf("held %+v, want anth: the one before is gone", f)
		}
	})
}

// The reporter's case: a Claude account, which ranks first for Image
// recognition, signed in after a key's model that sees served it.
func TestClaudeAccountAddedTakesNoImages(t *testing.T) {
	claudeMadeFirst(t, false)
	logins := filepath.Join(filepath.Dir(provider.Path()), "logins.json")
	raw, err := os.ReadFile(logins)
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(logins)
	provider.ForgetAccounts()
	t.Cleanup(provider.ForgetAccounts)
	seeing(t, provider.Provider{ID: "keyed", Chat: "http://keyed.invalid/v1"}, 1)
	seedAutoPicks()
	if m := AutoVision(); m != "keyed/eye" {
		t.Fatalf("AutoVision = %q before the Claude account", m)
	}

	if err := os.WriteFile(logins, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	provider.ForgetAccounts()
	if err := provider.Save(provider.Provider{ID: "claude", Models: []string{"claude-haiku-5-5"}}); err != nil {
		t.Fatal(err)
	}
	yes := true
	if err := provider.SetModelImage("claude/claude-haiku-5-5", &yes); err != nil {
		t.Fatal(err)
	}
	if r := seersRankedHeld(nil, ""); len(r) == 0 || !strings.HasPrefix(r[0].model, "claude/") {
		t.Fatalf("unheld, the Claude account isn't first: %v", r)
	}
	if m := AutoVision(); m != "keyed/eye" {
		t.Fatalf("AutoVision = %q after the Claude account was added, want keyed/eye", m)
	}
}
