package agent

// Codex's status line, magpie's (lyj1115 on X: the current account's quota,
// or the relay's balance, under Codex's chat box, off unless turned on).
// Codex's TUI, from 0.171.0-alpha.12, draws its footer from a status line
// provider: a stdio MCP server whose tool advertises a statusline
// entrypoint, picked in config.toml's [tui] status_line_provider = {
// server, tool } behind [features] tui_extensions_statusline (its bundled
// create-statusline skill). `magpie statusline --codex` is such a server
// (internal/cli/statusline_codex.go). The switch on Codex's row writes
// those three keys, the server as this magpie's binary by its full path;
// off takes out only what is magpie's.
//
// A provider of the user's own, or an [mcp_servers.quota_line] of theirs,
// is never written over: the switch isn't offered while one is there, and
// on is refused; nor is a features flag they turned off. The flag is
// taken out on off only when magpie wrote it (stashed). Only offered
// where a Codex CLI here can draw it: 0.170.0 reads the keys without
// complaint and draws nothing. The desktop app has no such footer, and is
// not reached into (cockpit-tools drives it through its debug port).
// Only on this computer, as Claude Code's switch.

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/filememo"
)

const (
	// the names config.toml has for magpie's server and its tool;
	// cli.CodexStatusServer and cli.CodexStatusTool say the same
	codexStatusServer = "quota_line"
	codexStatusTool   = "statusline"
	// codexStatusFrom is the first Codex that draws a provider's footer
	codexStatusFrom = "0.171.0-alpha.12"
	// codexStatusFeature is the flag status_line_provider is read behind
	codexStatusFeature = "tui_extensions_statusline"
	// codexStatusFlagKey stashes that magpie wrote the flag
	codexStatusFlagKey = "codex.statusline_feature"
)

// codexStatusArgs are what Codex runs magpie's binary with.
var codexStatusArgs = []string{"statusline", "--codex"}

// codexStatusState is what config.toml says about the status line.
type codexStatusState struct {
	readable bool
	// mine: a provider picked that isn't magpie's (not "default")
	mine bool
	// server: [mcp_servers.quota_line] is there; ours: it runs magpie's
	server, serverOurs bool
	// picked: status_line_provider names magpie's server and tool
	picked bool
	// flag is features.tui_extensions_statusline: "true", "false" or ""
	flag string
}

// ours says the status line is magpie's and on.
func (s codexStatusState) ours() bool { return s.picked && s.serverOurs }

// free says magpie may turn it on: nothing of the user's in the way.
func (s codexStatusState) free() bool {
	return s.readable && !s.mine && (!s.server || s.serverOurs) && s.flag != "false"
}

// codexStatusRead is config.toml's status line keys, read again only when
// the file changed. Each look decoded the whole file and kept its text: on
// a real Codex history (~11k [projects] tables, 1.5 MB) that was ~10 MB of
// garbage on every window poll that asked for Codex's values, and the
// file's 1.5 MB held for good, for a few keys (#2278).
func codexStatusRead(path string) codexStatusState {
	s, err := filememo.ReadNow("codex status line", path, func(b []byte) (codexStatusState, error) {
		return codexStatusParse(edit.TrimBOM(b)), nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return codexStatusParse(nil) // no file: nothing of the user's in the way
	}
	if err != nil {
		return codexStatusState{}
	}
	return s
}

// codexStatusParsed, when set (tests), is told of each decode.
var codexStatusParsed func()

func codexStatusParse(raw []byte) codexStatusState {
	if codexStatusParsed != nil {
		codexStatusParsed()
	}
	var doc struct {
		Features   map[string]any `toml:"features"`
		McpServers map[string]any `toml:"mcp_servers"`
		Tui        struct {
			Provider any `toml:"status_line_provider"`
		} `toml:"tui"`
	}
	if err := toml.Unmarshal(raw, &doc); err != nil {
		return codexStatusState{}
	}
	s := codexStatusState{readable: true}
	switch v := doc.Features[codexStatusFeature].(type) {
	case bool:
		s.flag = fmt.Sprint(v)
	case nil:
	default:
		s.flag = "false" // not Codex's spelling: the user's, left alone
	}
	if srv, ok := doc.McpServers[codexStatusServer]; ok {
		s.server = true
		m, _ := srv.(map[string]any)
		cmd, _ := m["command"].(string)
		args, _ := m["args"].([]any)
		var as []string
		for _, a := range args {
			v, _ := a.(string)
			as = append(as, v)
		}
		s.serverOurs = magpieProgram(cmd) && slices.Equal(as, codexStatusArgs)
	}
	switch v := doc.Tui.Provider.(type) {
	case nil:
	case string:
		s.mine = v != "default"
	case map[string]any:
		s.picked = v["server"] == codexStatusServer && v["tool"] == codexStatusTool
		s.mine = !s.picked
	default:
		s.mine = true
	}
	return s
}

// magpieProgram says whether a command path is a magpie binary.
func magpieProgram(cmd string) bool {
	base := strings.ToLower(filepath.Base(strings.ReplaceAll(cmd, `\`, "/")))
	base = strings.TrimSuffix(base, ".exe")
	return base == "magpie" || strings.HasPrefix(base, "magpie-")
}

// codexStatusDraws says whether a Codex CLI here draws a provider's
// footer.
func codexStatusDraws(at place) bool {
	for _, r := range codexReaders(at) {
		if r.Kind == "cli" && r.Version != "" && !Newer(codexStatusFrom, r.Version) {
			return true
		}
	}
	return false
}

// codexStatusField is the statusline switch of Codex's config.toml at
// path; here says the Codex is this computer's (not a distro's).
func codexStatusField(at place, path string, here bool) Field {
	flagKey := at.key(codexStatusFlagKey)
	return Field{
		Key: "statusline", Label: "status line", Quiet: true,
		Get: func() string {
			if codexStatusRead(path).ours() {
				return "on"
			}
			return ""
		},
		Set: func(v string) error {
			s := codexStatusRead(path)
			switch v {
			case "", "off":
				if s.picked {
					if err := edit.DelTOMLKey(path, "tui", "status_line_provider"); err != nil {
						return err
					}
				}
				if s.serverOurs {
					if err := edit.DelTOMLTable(path, "mcp_servers."+codexStatusServer); err != nil {
						return err
					}
				}
				if peek(flagKey)[flagKey] != "" {
					if s.flag == "true" {
						if err := edit.DelTOMLKey(path, "features", codexStatusFeature); err != nil {
							return err
						}
					}
					forget(flagKey)
				}
				return nil
			case "on":
				if s.ours() {
					return nil
				}
				if !here {
					return fmt.Errorf("magpie's status line is for Codex on this computer, not in WSL")
				}
				if !s.readable {
					return fmt.Errorf("%s can't be read; magpie leaves it as it is", at.native(path))
				}
				if !s.free() {
					return fmt.Errorf("%s has a status line provider, an [mcp_servers.%s] or a %s of its own; magpie leaves them as they are", at.native(path), codexStatusServer, codexStatusFeature)
				}
				was, err := edit.Read(path)
				if err != nil {
					return err
				}
				stashed := false
				undo := func(err error) error {
					if stashed {
						forget(flagKey)
					}
					if was == nil {
						edit.DelTOMLKey(path, "tui", "status_line_provider")
						edit.DelTOMLTable(path, "mcp_servers."+codexStatusServer)
						edit.DelTOMLKey(path, "features", codexStatusFeature)
					} else {
						edit.WriteAtomic(path, was)
					}
					return err
				}
				if s.flag == "" {
					stash(map[string]string{flagKey: "1"})
					stashed = true
					if err := edit.SetTOMLKey(path, "features", codexStatusFeature, true); err != nil {
						return undo(err)
					}
				}
				args := make([]any, len(codexStatusArgs))
				for i, a := range codexStatusArgs {
					args[i] = a
				}
				if err := edit.SetTOMLTable(path, "mcp_servers."+codexStatusServer,
					edit.KV{Path: "command", Value: claudeStatusExe()},
					edit.KV{Path: "args", Value: edit.Raw(tomlStrings(codexStatusArgs))},
					// Codex hands a server only a few of its variables: a
					// MAGPIE_ADDR the user runs Codex with is the gateway
					// the footer asks
					edit.KV{Path: "env_vars", Value: edit.Raw(tomlStrings([]string{"MAGPIE_ADDR"}))},
				); err != nil {
					return undo(err)
				}
				if err := edit.SetTOMLKey(path, "tui", "status_line_provider", edit.Inline{
					{Path: "server", Value: codexStatusServer}, {Path: "tool", Value: codexStatusTool},
				}); err != nil {
					return undo(err)
				}
				// a file whose tui or features were spelled so the keys
				// landed beside them (dotted keys up top) reads back other
				if !codexStatusRead(path).ours() {
					return undo(fmt.Errorf("%s didn't read back as magpie wrote it; it is left as it was", at.native(path)))
				}
				return nil
			}
			return fmt.Errorf("status line is on or off, not %q", v)
		},
		Options: func(map[string]string) []Option {
			if !here {
				return nil
			}
			s := codexStatusRead(path)
			if !s.ours() && (!s.free() || !codexStatusDraws(at)) {
				return nil
			}
			return []Option{{Value: "on", Note: "the remaining quota or balance of the account in use, under Codex's prompt"}}
		},
	}
}

// tomlStrings is a TOML array of strings.
func tomlStrings(ss []string) string {
	parts := make([]string, len(ss))
	for i, s := range ss {
		b, _ := toml.Marshal(map[string]string{"s": s})
		_, v, _ := strings.Cut(strings.TrimSpace(string(b)), " = ")
		parts[i] = v
	}
	return "[" + strings.Join(parts, ", ") + "]"
}
