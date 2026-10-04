// Package omarchy is what magpie knows of Omarchy (omarchy.org), the
// Hyprland desktop: whether it runs on one, and the look its current theme
// gives every surface, so magpie's windows can take that look there and
// nowhere else.
//
// Omarchy (4.x) keeps the current theme under
// ~/.local/state/omarchy/current/theme: colors.toml, the palette every app's
// theme is made from, and shell.toml, the tokens its own shell draws with
// (controls, popups, menus). The font is omarchy-font-current's, the corners
// Hyprland's decoration:rounding (0 unless changed) and the border its
// general:border_size in the colour of the active window's.
package omarchy

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/proc"
)

// Detect reports whether magpie runs on Omarchy. MAGPIE_OMARCHY=1 says it
// does (with MAGPIE_OMARCHY_THEME the theme's folder), =0 that it doesn't.
func Detect() bool {
	switch os.Getenv("MAGPIE_OMARCHY") {
	case "1":
		return true
	case "0":
		return false
	}
	if runtime.GOOS != "linux" || ThemeDir() == "" {
		return false
	}
	if appdir.Getenv("OMARCHY_PATH") != "" {
		return true
	}
	home, _ := os.UserHomeDir()
	for _, d := range []string{"/usr/share/omarchy", filepath.Join(home, ".local", "share", "omarchy")} {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			return true
		}
	}
	return false
}

// ThemeDir is the current theme's folder, or "" when there is none.
func ThemeDir() string {
	if d := os.Getenv("MAGPIE_OMARCHY_THEME"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	state := appdir.Getenv("XDG_STATE_HOME")
	if state == "" {
		state = filepath.Join(home, ".local", "state")
	}
	for _, d := range []string{
		filepath.Join(state, "omarchy", "current", "theme"),
		filepath.Join(home, ".config", "omarchy", "current", "theme"), // before 4.0
	} {
		if _, err := os.Stat(filepath.Join(d, "colors.toml")); err == nil {
			return d
		}
	}
	return ""
}

// Theme is the current theme as magpie draws with it.
type Theme struct {
	Name  string            `json:"name"`  // tokyo-night
	Mode  string            `json:"mode"`  // dark or light
	Stamp string            `json:"stamp"` // changes when any of it does
	Vars  map[string]string `json:"vars"`  // CSS custom properties
}

// Parse reads a TOML file's key = value lines, a section's keys as
// "section.key": the little of TOML the theme files use (strings, numbers,
// booleans, comments).
func Parse(b []byte) map[string]string {
	out := map[string]string{}
	section := ""
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		if line[0] == '[' {
			section = strings.Trim(line, "[] ")
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if strings.HasPrefix(v, `"`) {
			if end := strings.Index(v[1:], `"`); end >= 0 {
				v = v[1 : end+1]
			}
		} else if i := strings.Index(v, "#"); i >= 0 {
			v = strings.TrimSpace(v[:i])
		}
		if section != "" {
			k = section + "." + k
		}
		out[k] = v
	}
	return out
}

// color is an RGBA colour, each part 0..1.
type color struct{ r, g, b, a float64 }

// parseColor reads #rgb, #rrggbb, #rrggbbaa, Hyprland's rgb(rrggbb) and
// rgba(rrggbbaa), and CSS's rgb(r, g, b) and rgba(r, g, b, a). A gradient
// ("rgba(…) rgba(…) 45deg") is read as its first colour.
func parseColor(s string) (color, bool) {
	s = strings.TrimSpace(strings.ToLower(s))
	if f := strings.Fields(s); len(f) > 1 && !strings.Contains(s, ",") {
		s = f[0]
	}
	hex := func(h string) (color, bool) {
		switch len(h) {
		case 3:
			h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
		case 6:
		case 8:
		default:
			return color{}, false
		}
		n, err := strconv.ParseUint(h, 16, 32)
		if err != nil {
			return color{}, false
		}
		if len(h) == 6 {
			n = n<<8 | 0xff
		}
		return color{float64(n>>24&0xff) / 255, float64(n>>16&0xff) / 255, float64(n>>8&0xff) / 255, float64(n&0xff) / 255}, true
	}
	switch {
	case strings.HasPrefix(s, "#"):
		return hex(s[1:])
	case strings.HasPrefix(s, "rgb"):
		open, end := strings.Index(s, "("), strings.LastIndex(s, ")")
		if open < 0 || end < open {
			return color{}, false
		}
		in := strings.TrimSpace(s[open+1 : end])
		if !strings.Contains(in, ",") {
			return hex(in)
		}
		parts := strings.Split(in, ",")
		if len(parts) < 3 {
			return color{}, false
		}
		var v [4]float64
		v[3] = 1
		for i, p := range parts[:min(len(parts), 4)] {
			f, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
			if err != nil {
				return color{}, false
			}
			if i < 3 {
				f /= 255
			}
			v[i] = f
		}
		return color{v[0], v[1], v[2], v[3]}, true
	}
	return color{}, false
}

func (c color) css() string {
	to := func(f float64) int { return int(max(0, min(255, f*255+0.5))) }
	if c.a >= 0.999 {
		return fmt.Sprintf("#%02x%02x%02x", to(c.r), to(c.g), to(c.b))
	}
	return fmt.Sprintf("rgba(%d, %d, %d, %s)", to(c.r), to(c.g), to(c.b), strconv.FormatFloat(max(0, min(1, c.a)), 'f', 3, 64))
}

// over is c at alpha a laid over the opaque bg, as one opaque colour.
func (c color) over(bg color, a float64) color {
	return color{bg.r + (c.r-bg.r)*a, bg.g + (c.g-bg.g)*a, bg.b + (c.b-bg.b)*a, 1}
}

func (c color) alpha(a float64) color { c.a = a; return c }

// luminance is c's relative luminance, to tell a light theme from a dark one
// that doesn't say.
func (c color) luminance() float64 { return 0.2126*c.r + 0.7152*c.g + 0.0722*c.b }

// Vars turns the theme's palette (colors.toml) and shell tokens
// (shell.toml) into magpie's CSS custom properties, the rest given: the
// font's family, the corners' radius and the border's width.
func Vars(colors, shell map[string]string, font string, rounding, border int) (map[string]string, string) {
	get := func(m map[string]string, fallback color, keys ...string) color {
		for _, k := range keys {
			v := m[k]
			// a shell token may name another: "hyprland.active-border"
			for range 3 {
				if ref, ok := shell[v]; ok {
					v = ref
				}
			}
			if c, ok := parseColor(v); ok {
				return c
			}
		}
		return fallback
	}
	num := func(k string, fallback float64) float64 {
		if f, err := strconv.ParseFloat(shell[k], 64); err == nil {
			return f
		}
		return fallback
	}
	black := color{0, 0, 0, 1}
	bg := get(shell, get(colors, color{0.102, 0.106, 0.149, 1}, "background"), "popups.background")
	bg.a = 1
	fg := get(shell, get(colors, color{0.663, 0.694, 0.839, 1}, "foreground"), "popups.text")
	mode := strings.ToLower(colors["mode"])
	if mode != "light" && mode != "dark" {
		mode = "dark"
		if bg.luminance() > 0.5 {
			mode = "light"
		}
	}
	dim := get(colors, fg.over(bg, 0.55), "dark_foreground")
	if mode == "light" {
		dim = get(colors, fg.over(bg, 0.6), "muted", "dark_foreground")
	}
	accent := get(shell, get(colors, color{0.478, 0.635, 0.969, 1}, "accent", "blue"), "menu.selected-text")
	bright := get(colors, fg, "bright_foreground")
	red := get(colors, color{0.969, 0.463, 0.557, 1}, "red")
	green := get(colors, color{0.62, 0.808, 0.416, 1}, "green")
	yellow := get(colors, color{0.878, 0.686, 0.408, 1}, "yellow")
	orange := get(colors, yellow, "orange")
	cyan := get(colors, color{0.267, 0.616, 0.671, 1}, "cyan")
	blue := get(colors, accent, "blue")
	magenta := get(colors, color{0.678, 0.557, 0.902, 1}, "magenta")
	edge := get(shell, accent, "popups.border", "hyprland.active-border")
	selFg := get(shell, fg, "menu.selected-background")
	selA := num("menu.selected-background-alpha", 0.08)
	fillA := num("controls.normal-fill-alpha", 0.04)
	hoverA := num("controls.hover-cursor-fill-alpha", 0.08)
	ctlBorderA := num("controls.normal-border-alpha", 0.4)
	pressA := num("controls.pressed-fill-alpha", 0.22)
	selectedA := num("controls.selected-fill-alpha", 0.18)
	size := num("font.base-size", 12)
	soft := func(c color) string { return c.over(bg, 0.16).css() }
	shadow := black.alpha(0.35)
	if mode == "light" {
		shadow = black.alpha(0.12)
	}
	invert := "1"
	if mode == "light" {
		invert = "0"
	}
	if font == "" {
		font = "JetBrainsMono Nerd Font"
	}
	family := fmt.Sprintf("%q, \"JetBrainsMono Nerd Font\", \"JetBrains Mono\", ui-monospace, monospace", font)
	v := map[string]string{
		"--bg":          bg.css(),
		"--card":        fg.over(bg, fillA).css(),
		"--card-2":      fg.over(bg, fillA/2).css(),
		"--pill":        fg.over(bg, fillA).css(),
		"--pill-hover":  fg.over(bg, hoverA).css(),
		"--pill-line":   fg.alpha(ctlBorderA).css(),
		"--pill-shadow": "none",
		"--card-edge":   "none",
		"--seg-track":   "transparent",
		"--seg-thumb":   fg.over(bg, selectedA).css(),
		"--seg-edge":    "none",
		"--line":        fg.over(bg, 0.16).css(),
		"--line-2":      fg.over(bg, 0.1).css(),
		"--fg":          fg.css(),
		"--fg-2":        fg.css(),
		"--fg-hi":       bright.css(),
		"--muted":       dim.css(),
		"--faint":       fg.over(bg, 0.38).css(),
		"--accent":      accent.css(),
		"--accent-soft": accent.over(bg, 0.14).css(),
		"--accent-fg":   bg.css(),
		"--sel":         selFg.over(bg, selA).css(),
		"--press":       fg.over(bg, pressA).css(),
		"--green":       green.css(),
		"--green-soft":  soft(green),
		"--red":         red.css(),
		"--red-soft":    soft(red),
		"--amber":       yellow.css(),
		"--amber-soft":  soft(yellow),
		"--drift":       red.css(),
		"--drift-soft":  soft(red),
		"--pop-bg":      bg.css(),
		"--shadow":      "0 8px 24px " + shadow.css(),
		"--skel":        fg.over(bg, 0.06).css(),
		"--skel-hi":     fg.over(bg, 0.12).css(),
		"--font":        family,
		"--code":        family,
		"--logo-invert": invert,
		"--tk-s":        green.css(), "--tk-k": magenta.css(), "--tk-f": blue.css(),
		"--tk-n": orange.css(), "--tk-v": cyan.css(), "--tk-o": magenta.css(), "--tk-c": dim.css(),
		"--om-edge":       edge.css(),
		"--om-border":     strconv.Itoa(max(border, 1)) + "px",
		"--om-radius":     strconv.Itoa(max(rounding, 0)) + "px",
		"--om-size":       strconv.FormatFloat(size, 'f', -1, 64) + "px",
		"--om-hover":      fg.alpha(hoverA).css(),
		"--om-hover-line": fg.alpha(num("controls.hover-cursor-border-alpha", 0.25)).css(),
		"--om-sel":        selFg.alpha(selA).css(),
		"--om-sel-line":   get(shell, fg, "menu.selected-border").alpha(num("menu.selected-border-alpha", 0.25)).css(),
		"--om-cyan":       cyan.css(),
		"--om-magenta":    magenta.css(),
		"--om-orange":     orange.css(),
	}
	return v, mode
}

var (
	mu     sync.Mutex
	cached Theme
	seen   string // the theme's files' stamp cached was read at
	asked  time.Time
	// the font and Hyprland's corners and border, asked of
	// omarchy-font-current and hyprctl only when their own files change: a
	// theme set reloads Hyprland, and hyprctl asked meanwhile can stall for
	// seconds, so a new theme is read from its files alone
	desk     string // the font's and Hyprland's files' stamp they were asked at
	font     string
	rounding int
	border   int
)

// the files whose change changes the look: the theme's, and the font's
// (omarchy-font-set writes fontconfig's) and Hyprland's config
func themeFiles(dir string) []string {
	return []string{
		filepath.Join(dir, "colors.toml"),
		filepath.Join(dir, "shell.toml"),
		filepath.Join(filepath.Dir(dir), "theme.name"),
	}
}

func deskFiles() []string {
	home, _ := os.UserHomeDir()
	cfg := appdir.Getenv("XDG_CONFIG_HOME")
	if cfg == "" {
		cfg = filepath.Join(home, ".config")
	}
	hypr, _ := filepath.Glob(filepath.Join(cfg, "hypr", "*"))
	return append([]string{filepath.Join(cfg, "fontconfig", "fonts.conf")}, hypr...)
}

func stampOf(files []string) string {
	var b strings.Builder
	for _, f := range files {
		if fi, err := os.Stat(f); err == nil {
			fmt.Fprintf(&b, "%s:%d:%d;", filepath.Base(f), fi.ModTime().UnixNano(), fi.Size())
		}
	}
	return b.String()
}

// Current is the theme as it is now: read again only when one of its files
// has changed, and those looked at no more than once a second.
func Current() (Theme, bool) {
	dir := ThemeDir()
	if dir == "" {
		return Theme{}, false
	}
	mu.Lock()
	defer mu.Unlock()
	if !asked.IsZero() && time.Since(asked) < time.Second && cached.Stamp != "" {
		return cached, true
	}
	asked = time.Now()
	stamp, d := stampOf(themeFiles(dir)), stampOf(deskFiles())
	if stamp == seen && d == desk && cached.Stamp != "" {
		return cached, true
	}
	if d != desk || cached.Stamp == "" {
		font = fontName()
		rounding, border = hyprInt("decoration:rounding", 0), hyprInt("general:border_size", 2)
		desk = d
	}
	colors, _ := os.ReadFile(filepath.Join(dir, "colors.toml"))
	shell, _ := os.ReadFile(filepath.Join(dir, "shell.toml"))
	name, _ := os.ReadFile(filepath.Join(filepath.Dir(dir), "theme.name"))
	vars, mode := Vars(Parse(colors), Parse(shell), font, rounding, border)
	seen = stamp
	cached = Theme{
		Name: strings.TrimSpace(string(name)), Mode: mode, Vars: vars,
		Stamp: fmt.Sprintf("%x", fnv(stamp+d+font+strconv.Itoa(rounding)+strconv.Itoa(border))),
	}
	return cached, true
}

func fnv(s string) uint64 {
	h := uint64(14695981039346656037)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return h
}

// fontName is the font Omarchy sets for everything, as omarchy-font-current
// says.
func fontName() string {
	if f := os.Getenv("MAGPIE_OMARCHY_FONT"); f != "" {
		return f
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := proc.CommandContext(ctx, "omarchy-font-current").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// hyprInt is one of Hyprland's integer options ("int: 0"), or fallback when
// Hyprland doesn't answer.
func hyprInt(option string, fallback int) int {
	if os.Getenv("HYPRLAND_INSTANCE_SIGNATURE") == "" {
		return fallback
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := proc.CommandContext(ctx, "hyprctl", "getoption", option).Output()
	if err != nil {
		return fallback
	}
	for _, line := range strings.Split(string(out), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "int:"); ok {
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
				return n
			}
		}
	}
	return fallback
}
