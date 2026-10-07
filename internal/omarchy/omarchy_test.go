package omarchy

import (
	"os"
	"path/filepath"
	"testing"
)

const tokyoColors = `mode = "dark"

accent = "#7aa2f7"
selection = "#292e42"
muted = "#414868"

background = "#1a1b26"
foreground = "#a9b1d6"
dark_foreground = "#565f89"
bright_foreground = "#c0caf5"
red = "#f7768e"
yellow = "#e0af68"
green = "#9ece6a"
`

const tokyoShell = `# comment
[hyprland]
active-border            = "#7aa2f7"
active-border-foreground = "#a9b1d6"

[controls]
normal-fill-alpha   = 0.04 # inline
hover-cursor-fill-alpha   = 0.08

[font]
base-size = 12

[popups]
background       = "#1a1b26"
text             = "#a9b1d6"
border           = "hyprland.active-border"

[menu]
selected-background       = "#a9b1d6"
selected-background-alpha = 0.08
selected-text             = "#7aa2f7"
`

func TestParse(t *testing.T) {
	m := Parse([]byte(tokyoShell))
	for k, want := range map[string]string{
		"hyprland.active-border":     "#7aa2f7",
		"controls.normal-fill-alpha": "0.04",
		"font.base-size":             "12",
		"popups.border":              "hyprland.active-border",
	} {
		if m[k] != want {
			t.Errorf("%s = %q, want %q", k, m[k], want)
		}
	}
}

func TestParseColor(t *testing.T) {
	for in, want := range map[string]string{
		"#7aa2f7":                             "#7aa2f7",
		"#fff":                                "#ffffff",
		"rgb(7aa2f7)":                         "#7aa2f7",
		"rgba(7aa2f780)":                      "rgba(122, 162, 247, 0.502)",
		"rgba(33ccffee) rgba(00ff99ee) 45deg": "rgba(51, 204, 255, 0.933)",
		"rgba(10, 20, 30, 0.5)":               "rgba(10, 20, 30, 0.500)",
	} {
		c, ok := parseColor(in)
		if !ok || c.css() != want {
			t.Errorf("parseColor(%q) = %q %v, want %q", in, c.css(), ok, want)
		}
	}
	if _, ok := parseColor("hyprland.active-border"); ok {
		t.Error("a token's name read as a colour")
	}
}

func TestVars(t *testing.T) {
	v, mode := Vars(Parse([]byte(tokyoColors)), Parse([]byte(tokyoShell)), "JetBrainsMono Nerd Font", 0, 2)
	if mode != "dark" {
		t.Errorf("mode %q", mode)
	}
	for k, want := range map[string]string{
		"--bg":          "#1a1b26",
		"--fg":          "#a9b1d6",
		"--accent":      "#7aa2f7",
		"--muted":       "#565f89",
		"--red":         "#f7768e",
		"--om-edge":     "#7aa2f7", // popups.border names the Hyprland border
		"--om-radius":   "0px",
		"--om-border":   "2px",
		"--om-size":     "12px",
		"--logo-invert": "1",
		"--om-sel":      "rgba(169, 177, 214, 0.080)",
		"--card":        "#20212d", // the foreground at 4% over the background
	} {
		if v[k] != want {
			t.Errorf("%s = %q, want %q", k, v[k], want)
		}
	}
	if want := `"JetBrainsMono Nerd Font", "JetBrainsMono Nerd Font", "JetBrains Mono", ui-monospace, monospace`; v["--font"] != want {
		t.Errorf("--font = %s", v["--font"])
	}

	// a light theme that doesn't say so is told by its background
	v, mode = Vars(map[string]string{"background": "#f5f5f5", "foreground": "#222222"}, nil, "", 8, 1)
	if mode != "light" || v["--logo-invert"] != "0" || v["--om-radius"] != "8px" {
		t.Errorf("light: %s %s %s", mode, v["--logo-invert"], v["--om-radius"])
	}
}

func TestCurrent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "current", "theme")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "colors.toml"), []byte(tokyoColors), 0o644)
	os.WriteFile(filepath.Join(dir, "shell.toml"), []byte(tokyoShell), 0o644)
	os.WriteFile(filepath.Join(filepath.Dir(dir), "theme.name"), []byte("tokyo-night\n"), 0o644)
	t.Setenv("MAGPIE_OMARCHY", "1")
	t.Setenv("MAGPIE_OMARCHY_THEME", dir)
	t.Setenv("MAGPIE_OMARCHY_FONT", "Iosevka")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if !Detect() {
		t.Fatal("not detected")
	}
	// the theme an earlier run read (-count) is kept for the process's
	// life, and would be answered for a second
	mu.Lock()
	cached = Theme{}
	mu.Unlock()
	th, ok := Current()
	if !ok || th.Name != "tokyo-night" || th.Vars["--bg"] != "#1a1b26" || th.Stamp == "" {
		t.Fatalf("%v %+v", ok, th)
	}
	// a new theme is read within a second or so
	os.WriteFile(filepath.Join(dir, "shell.toml"), []byte("[font]\nbase-size = 13\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "colors.toml"), []byte("mode = \"light\"\nbackground = \"#fafafa\"\nforeground = \"#111111\"\n"), 0o644)
	mu.Lock()
	asked = asked.Add(-2e9)
	mu.Unlock()
	th2, _ := Current()
	if th2.Stamp == th.Stamp || th2.Mode != "light" || th2.Vars["--bg"] != "#fafafa" {
		t.Fatalf("not read again: %+v", th2)
	}
	// a theme set asks neither omarchy-font-current nor hyprctl (Hyprland
	// reloads meanwhile and hyprctl stalls): the font stays as it was asked
	if th2.Vars["--font"] != th.Vars["--font"] || th.Vars["--font"] == "" {
		t.Fatalf("font %q then %q", th.Vars["--font"], th2.Vars["--font"])
	}
	t.Setenv("MAGPIE_OMARCHY_FONT", "JetBrains Mono")
	os.WriteFile(filepath.Join(dir, "colors.toml"), []byte(tokyoColors), 0o644)
	mu.Lock()
	asked = asked.Add(-2e9)
	mu.Unlock()
	if th3, _ := Current(); th3.Vars["--bg"] != "#1a1b26" || th3.Vars["--font"] != th.Vars["--font"] {
		t.Fatalf("the theme set asked for the font again: %q", th3.Vars["--font"])
	}
	// omarchy-font-set (fontconfig's file) does
	cfg := os.Getenv("XDG_CONFIG_HOME")
	os.MkdirAll(filepath.Join(cfg, "fontconfig"), 0o755)
	os.WriteFile(filepath.Join(cfg, "fontconfig", "fonts.conf"), []byte("<fontconfig/>"), 0o644)
	mu.Lock()
	asked = asked.Add(-2e9)
	mu.Unlock()
	if th4, _ := Current(); th4.Vars["--font"] == th.Vars["--font"] {
		t.Fatalf("a new font not asked: %q", th4.Vars["--font"])
	}
	t.Setenv("MAGPIE_OMARCHY", "0")
	if Detect() {
		t.Error("MAGPIE_OMARCHY=0 detected")
	}
}
