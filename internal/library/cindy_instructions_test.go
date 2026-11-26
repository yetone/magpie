package library

import (
	"os"
	"path/filepath"
	"testing"
)

// cindyHere installs Cindy in the sandbox: its app, which a Mac finds it
// by (macAppRemoved), and its data folder, as Cindy opened once leaves it,
// which it returns.
func cindyHere(t *testing.T, h string) string {
	t.Helper()
	holdApp(t, h, "Cindy.app", "com.xd.cindycn")
	t.Setenv("APPDATA", filepath.Join(h, "AppData", "Roaming"))
	cfg, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	d := filepath.Join(cfg, "Cindy")
	write(t, filepath.Join(d, "cindy-local-v1.db"), "")
	return d
}

// T_t__biu on X (#7854): shared instructions had no Cindy. Cindy's Pi,
// its default runtime, puts Pi's own user-wide file in every chat's system
// prompt (readPiGlobalContext, resolvePiGlobalContextHome: ~/.pi/agent, or
// PI_CODING_AGENT_DIR), so that is the file Cindy is given, with Pi's
// order of names. Its Codex's codex-home/AGENTS.md is Cindy's own copy of
// ~/.codex/AGENTS.md and is never written; nor is ~/.codex/AGENTS.md, the
// codex target's.
func TestCindyInstructions(t *testing.T) {
	h := sandbox(t)
	// Cindy with no Pi installed: the file is Cindy's alone
	if err := os.Remove(filepath.Join(h, "bin", "pi")); err != nil {
		t.Fatal(err)
	}
	dir := cindyHere(t, h)
	pi := filepath.Join(h, ".pi", "agent")
	if tg := targetByID("pi"); tg != nil {
		t.Fatalf("pi is here: %+v", tg)
	}
	tg := targetByID("cindy")
	if tg == nil || tg.Instructions != filepath.Join(pi, "AGENTS.md") || tg.Override != filepath.Join(pi, "AGENTS.override.md") {
		t.Fatalf("cindy's instructions: %+v", tg)
	}
	if id, err := Takes("cindy", "instructions"); id != "cindy" || err != nil {
		t.Errorf("Takes: %q %v", id, err)
	}

	shared := "Use tabs."
	ok(t)(SaveInstructions(InstructionsChange{Shared: &shared, Agents: []string{"cindy"}}))
	want := blockBegin + "\nUse tabs.\n" + blockEnd + "\n"
	if s := read(t, filepath.Join(pi, "AGENTS.md")); s != want {
		t.Fatalf("Pi's AGENTS.md:\n%q", s)
	}
	for _, p := range []string{filepath.Join(dir, "codex-home", "AGENTS.md"), filepath.Join(h, ".codex", "AGENTS.md"), filepath.Join(dir, "pi-agent-home", "AGENTS.md")} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s was written for Cindy: %v", p, err)
		}
	}
	v, err := ReadInstructions()
	if err != nil {
		t.Fatal(err)
	}
	var row *AgentInstructions
	for i := range v.Agents {
		if v.Agents[i].Agent == "cindy" {
			row = &v.Agents[i]
		}
	}
	if row == nil || !row.On || row.Edited || row.Path != filepath.Join(pi, "AGENTS.md") || row.Override != "" {
		t.Fatalf("Cindy's row: %+v", row)
	}

	// an AGENTS.override.md of the user's is what Cindy's Pi reads instead
	write(t, filepath.Join(pi, "AGENTS.override.md"), "# Mine\n")
	v, err = ReadInstructions()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range v.Agents {
		if a.Agent == "cindy" && a.Override != filepath.Join(pi, "AGENTS.override.md") {
			t.Errorf("the override isn't shown: %+v", a)
		}
	}
	if s := read(t, filepath.Join(pi, "AGENTS.override.md")); s != "# Mine\n" {
		t.Errorf("the user's AGENTS.override.md was written: %q", s)
	}

	ok(t)(SaveInstructions(InstructionsChange{Agents: []string{}}))
	if s := read(t, filepath.Join(pi, "AGENTS.md")); s != "" {
		t.Errorf("switched off:\n%q", s)
	}
}

// Cindy reads Pi's file where PI_CODING_AGENT_DIR says, "~" in it being
// home, as Pi does; a CLAUDE.md of the user's there is written into, not
// hidden by a new AGENTS.md.
func TestCindyInstructionsPiDir(t *testing.T) {
	h := sandbox(t)
	cindyHere(t, h)
	t.Setenv("PI_CODING_AGENT_DIR", "~/pi-home")
	d := filepath.Join(h, "pi-home")
	write(t, filepath.Join(d, "CLAUDE.md"), "# Mine\n")
	tg := targetByID("cindy")
	if tg == nil || tg.Instructions != filepath.Join(d, "CLAUDE.md") || tg.Override != filepath.Join(d, "AGENTS.override.md") {
		t.Fatalf("cindy's instructions: %+v", tg)
	}
	shared := "Use tabs."
	ok(t)(SaveInstructions(InstructionsChange{Shared: &shared, Agents: []string{"cindy"}}))
	if s := read(t, filepath.Join(d, "CLAUDE.md")); s != "# Mine\n\n"+blockBegin+"\nUse tabs.\n"+blockEnd+"\n" {
		t.Fatalf("CLAUDE.md:\n%q", s)
	}
	if _, err := os.Stat(filepath.Join(d, "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatalf("an AGENTS.md hides CLAUDE.md: %v", err)
	}
}

// Pi and Cindy read one file: the one switched on keeps magpie's part in
// it, whichever it is, and the other, switched off, neither takes it out
// nor calls it edited (instrOwner, as agy and Antigravity's app).
func TestCindyInstructionsSharedWithPi(t *testing.T) {
	for _, on := range []string{"pi", "cindy"} {
		t.Run(on, func(t *testing.T) {
			h := sandbox(t)
			cindyHere(t, h)
			p := filepath.Join(h, ".pi", "agent", "AGENTS.md")
			for _, id := range []string{"pi", "cindy"} {
				if tg := targetByID(id); tg == nil || tg.Instructions != p {
					t.Fatalf("%s: %+v", id, tg)
				}
			}
			want := blockBegin + "\nUse tabs.\n" + blockEnd + "\n"
			shared := "Use tabs."
			ok(t)(SaveInstructions(InstructionsChange{Shared: &shared, Agents: []string{on}}))
			if s := read(t, p); s != want {
				t.Fatalf("after the switch:\n%q", s)
			}
			ok(t)(Sync())
			if s := read(t, p); s != want {
				t.Fatalf("after a sync:\n%q", s)
			}
			v, err := ReadInstructions()
			if err != nil {
				t.Fatal(err)
			}
			for _, a := range v.Agents {
				if (a.Agent == "pi" || a.Agent == "cindy") && (a.Edited || a.On != (a.Agent == on)) {
					t.Errorf("row %+v", a)
				}
			}
			ok(t)(SaveInstructions(InstructionsChange{Shared: &shared, Agents: []string{"pi", "cindy"}}))
			if s := read(t, p); s != want {
				t.Fatalf("both on:\n%q", s)
			}
			ok(t)(SaveInstructions(InstructionsChange{Agents: []string{}}))
			if s := read(t, p); s != "" {
				t.Errorf("both off:\n%q", s)
			}
		})
	}
}
