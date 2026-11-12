package gui

import (
	"context"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/proc"
)

// FeedbackURL is the post on X where users reply with feedback; Settings ›
// About's Report a problem opens it after copying the facts below.
const FeedbackURL = "https://x.com/magpie_router/status/2119861020978188499"

var osVersionRE = regexp.MustCompile(`\d+(\.\d+)+`)

// osVersion names the system and its version: "macOS 26.2", "Windows
// 10.0.26100", "Ubuntu 24.04.1 LTS". Only what the system calls itself:
// no host, user or path.
func osVersion() string {
	run := func(name string, args ...string) string {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		out, _ := proc.CommandContext(ctx, name, args...).Output()
		return osVersionRE.FindString(string(out))
	}
	switch runtime.GOOS {
	case "darwin":
		return strings.TrimSpace("macOS " + run("/usr/bin/sw_vers", "-productVersion"))
	case "windows":
		v := run("cmd", "/c", "ver") // "Microsoft Windows [Version 10.0.26100.4652]"
		if parts := strings.Split(v, "."); len(parts) > 3 {
			v = strings.Join(parts[:3], ".")
		}
		return strings.TrimSpace("Windows " + v)
	}
	if b, err := os.ReadFile("/etc/os-release"); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if v, ok := strings.CutPrefix(l, "PRETTY_NAME="); ok {
				if v = strings.Trim(strings.TrimSpace(v), `"'`); v != "" {
					return v
				}
			}
		}
	}
	return "Linux"
}

// diagnostics is the block a report starts with. It holds the version, the
// system, the architecture and where magpie is open, and never an account,
// a key, a path or a user name: the user pastes it into a public reply.
func diagnostics(surface string) string {
	if surface != "browser" {
		surface = "app"
	}
	return strings.Join([]string{
		"magpie " + Version,
		"OS: " + osVersion(),
		"Arch: " + runtime.GOARCH,
		"Opened in: " + surface,
	}, "\n")
}

// feedbackRoutes serves the facts to copy and the post to open, from
// Settings › About. The block goes to the clipboard, never into a link.
func feedbackRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/feedback", func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, map[string]string{"text": diagnostics(r.URL.Query().Get("surface")), "url": FeedbackURL})
	})
}
