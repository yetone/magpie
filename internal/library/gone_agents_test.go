package library

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/appdir"
)

// holdApp puts a macOS app bundle, its Info.plist saying id, in the sandbox
// home's Applications, where a Mac's apps are looked for.
func holdApp(t *testing.T, h, name, id string) {
	t.Helper()
	write(t, filepath.Join(h, "Applications", name, "Contents", "Info.plist"),
		`<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict><key>CFBundleIdentifier</key><string>`+id+`</string></dict></plist>`)
}

// @jiahao_jayden on X: the library's rows listed agents that aren't
// installed. One gone from here (uninstalled: its CLI gone, its folder
// left, #2177) is listed only while it still has what magpie gave it, on
// the rows of what it has, so that can be unticked and taken out of its
// folder; a sync gives it nothing new, and once it has nothing it isn't
// listed at all.
func TestGoneAgentListedToTakeOut(t *testing.T) {
	h := sandbox(t)
	// Hermes's own home: ~/.hermes, or %LOCALAPPDATA%\hermes on Windows
	t.Setenv("LOCALAPPDATA", filepath.Join(h, "AppData", "Local"))
	hh := appdir.HermesHome()
	write(t, filepath.Join(hh, "config.yaml"), "")
	holdCLI(t, h, "hermes")
	src := filepath.Join(h, "src/skills")
	skill(t, filepath.Join(src, "pdf"), "pdf", "Read PDFs")
	skill(t, filepath.Join(src, "csv"), "csv", "Read CSVs")
	ok(t)(InstallSkills(src, []string{"pdf", "csv"}, []string{"claude", "hermes"}))
	pdf, csv := filepath.Join(hh, "skills/pdf"), filepath.Join(hh, "skills/csv")
	if !ours(pdf, "pdf") || !ours(csv, "csv") {
		t.Fatal("hermes wasn't given the skills")
	}
	cfg := filepath.Join(hh, "config.yaml")
	ok(t)(SaveServer("", Server{Name: "fs", Transport: "stdio", Command: "npx", Args: []string{"-y", "@mcp/fs"}, Agents: []string{"claude", "hermes"}}))
	if !strings.Contains(read(t, cfg), "@mcp/fs") {
		t.Fatalf("hermes wasn't given the server:\n%s", read(t, cfg))
	}

	// uninstalled: its CLI gone, its folder left
	os.Remove(filepath.Join(h, "bin/hermes"))
	os.Remove(filepath.Join(h, "bin/hermes.exe"))
	if targetByID("hermes") != nil {
		t.Fatal("hermes is still a target without its CLI")
	}
	gone := func() *AgentView {
		t.Helper()
		v, err := Read(nil)
		if err != nil {
			t.Fatal(err)
		}
		for i, a := range v.Agents {
			if a.ID == "hermes" {
				return &v.Agents[i]
			}
		}
		return nil
	}
	if a := gone(); a == nil || !a.Gone || a.Skills == "" || a.MCP == "" {
		t.Fatalf("hermes, holding a skill and a server, isn't listed as gone: %+v", a)
	}
	// claude is no gone agent
	if v, _ := Read(nil); !slices.ContainsFunc(v.Agents, func(a AgentView) bool { return a.ID == "claude" && !a.Gone }) {
		t.Errorf("claude: %+v", v.Agents)
	}

	// a sync gives it nothing new: a link taken out by hand isn't made again
	os.Remove(csv)
	ok(t)(Sync())
	if _, err := os.Lstat(csv); !os.IsNotExist(err) {
		t.Error("a sync gave the gone hermes a skill again")
	}

	// unticked, the skill and the server come out of its folder
	ok(t)(SkillAgents("pdf", []string{"claude"}))
	if _, err := os.Lstat(pdf); !os.IsNotExist(err) {
		t.Errorf("pdf unticked is still in the gone hermes's skills: %v", err)
	}
	if !ours(filepath.Join(h, ".claude/skills/pdf"), "pdf") {
		t.Error("claude lost pdf")
	}
	ok(t)(SkillAgents("csv", []string{"claude"}))
	ok(t)(ServerAgents("fs", []string{"claude"}))
	if strings.Contains(read(t, cfg), "@mcp/fs") {
		t.Errorf("the server unticked is still in the gone hermes's config:\n%s", read(t, cfg))
	}
	// with nothing left it isn't listed
	if a := gone(); a != nil {
		t.Errorf("hermes, holding nothing, is still listed: %+v", a)
	}
}

// izhidigua on X: after the config was synced to a computer without Claude
// Code, Claude Desktop and Pi, Skills listed the agents installed there,
// and MCP the uninstalled ones as well, ticked on its servers by the other
// computer. A tick that came with the library is nothing magpie wrote
// here: an agent not installed is listed only for what it holds
// (Applied), as for a server or a skill alike.
func TestGoneAgentWithOnlySyncedTicksIsNotListed(t *testing.T) {
	h := sandbox(t)
	write(t, filepath.Join(h, ".claude/settings.json"), "{}")
	write(t, filepath.Join(h, ".pi/agent/settings.json"), "{}")
	for _, n := range []string{"claude", "claude.exe", "pi", "pi.exe"} {
		os.Remove(filepath.Join(h, "bin", n))
	}
	src := filepath.Join(h, "src/skills")
	skill(t, filepath.Join(src, "pdf"), "pdf", "Read PDFs")
	all := []string{"claude", "claude-desktop", "pi", "codex"}
	ok(t)(InstallSkills(src, []string{"pdf"}, all))
	ok(t)(SaveServer("", Server{Name: "fs", Transport: "stdio", Command: "npx", Args: []string{"-y", "@mcp/fs"}, Agents: all}))
	v, err := Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range v.Agents {
		if a.Gone {
			t.Errorf("%s is listed as gone, holding nothing written here", a.ID)
		}
	}
	if !slices.ContainsFunc(v.Agents, func(a AgentView) bool { return a.ID == "codex" && !a.Gone }) {
		t.Errorf("codex isn't listed: %+v", v.Agents)
	}
	// the ticks stay for the day it is installed
	if got := v.Servers[0].Agents; !slices.Contains(got, "pi") || !slices.Contains(got, "claude") {
		t.Errorf("the server's ticks were changed: %v", got)
	}
}
