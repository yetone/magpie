package library

// References to environment variables in a server's headers and
// environment (#1250). The library writes one as ${NAME} and keeps it as
// written; each agent is given it in the syntax that agent reads, so a
// token can stay in the environment and out of every agent's config. An
// agent that reads no reference where the server has one isn't given the
// server: written as it is, the agent would send "${NAME}" itself, and
// written with the value, the token would be in its file after all.

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/proc"
)

// envRef is a reference as the library writes one: ${NAME}.
var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// refSyntax is how an agent spells a reference in a value.
type refSyntax int

const (
	// refNone: the agent reads no reference there
	refNone refSyntax = iota
	// refDollar: ${NAME}, as the library writes it
	refDollar
	// refOpenCode: {env:NAME}
	refOpenCode
	// refCursor: ${env:NAME}
	refCursor
	// refCodex: Codex's own keys, which name the variable; a value can only
	// be the variable whole (Authorization's Bearer aside)
	refCodex
	// refGoose: ${NAME} in a header, each name listed in env_keys; a
	// command's variable only through env_keys, under its own name
	refGoose
	// refDsh: a !!js expression reading process.env (dshRef)
	refDsh
)

// envSyntax is how the agent reads a reference in a remote server's
// headers and in a command's environment.
type envSyntax struct{ headers, env refSyntax }

// refsOf is how each agent reads a reference, as its own source or docs
// say. A refNone is an agent magpie found no reference in, so a server
// that has one is left out for it.
func (f *mcpFile) refsOf() envSyntax {
	if f.Literal {
		return envSyntax{}
	}
	switch f.Format {
	case fmtClaude:
		// Claude Code expands ${NAME} (and ${NAME:-default}) in url,
		// headers, command, args and env of every scope's servers, the
		// user's in ~/.claude.json as well as a project's .mcp.json
		// (code.claude.com/docs/en/mcp, "Environment variable expansion";
		// 2.1.293's z4t expands the user and local scopes too, and tried:
		// a user-scope server got the token, an unset one is warned of and
		// sent as written). Toward a remote server it reads Anthropic's,
		// the clouds' and the package registries' own credential variables
		// as empty. Droid 0.231 expands ${NAME} in env and headers of
		// ~/.factory/mcp.json and won't connect with one unset
		// (docs.factory.com/cli/configuration/mcp). CodeBuddy Code, and
		// WorkBuddy, whose servers its CodeBuddy engine runs, expand ${NAME}
		// in command, args, cwd, env, url and headers of every scope's
		// servers and warn of one unset (McpConfigEnvExpandService,
		// @tencent-ai/codebuddy-code 2.162.0).
		return envSyntax{refDollar, refDollar}
	case fmtGrok:
		// Grok Build expands ${NAME} and ${NAME:-default} in url, command,
		// args, env and headers of [mcp_servers.*] as it loads config.toml
		// (~/.grok/docs/user-guide/07-mcp-servers.md, 1.0.46)
		return envSyntax{refDollar, refDollar}
	case fmtCodex:
		// codex-rs/config/src/mcp_types.rs (RawMcpServerConfig):
		// bearer_token_env_var sends Authorization: Bearer <its value>,
		// env_http_headers sends a header whole from a variable, and a
		// command's env_vars passes a variable of Codex's own environment
		// on by its name; http_headers and env are sent as written
		return envSyntax{refCodex, refCodex}
	case fmtOpenCode:
		// OpenCode puts in {env:NAME} anywhere in its config file as it
		// reads it (packages/opencode/src/config/config.ts, load), "" for
		// one unset; MiMo Code reads opencode.json the same way
		return envSyntax{refOpenCode, refOpenCode}
	case fmtCursor:
		// Cursor reads ${env:NAME} in url, headers, command, args and env
		// (cursor.com/docs/context/mcp, "Config interpolation"; cursor-agent
		// 2026.10.01's mcp-agent-exec expands every string of mcp.json the
		// same way). A command gets only HOME, LOGNAME, PATH, SHELL, TERM
		// and USER of Cursor's own environment besides its env.
		return envSyntax{refCursor, refCursor}
	case fmtGemini:
		// Gemini CLI expands $NAME, ${NAME} and ${NAME:-default} in every
		// string of settings.json as it loads the file, and saves the file
		// as written (packages/cli/src/config/settings.ts loadSettings ->
		// resolveEnvVarsInObject; envVarResolver.ts resolveEnvVarsInString;
		// read in the 0.62.0 bundle)
		return envSyntax{refDollar, refDollar}
	case fmtCrush:
		// Crush resolves $NAME and ${NAME} in an MCP server's headers and
		// env as it connects (charmbracelet/crush v0.39.3
		// internal/config/resolve.go ResolveValue, config.go
		// MCPConfig.ResolvedHeaders / ResolvedEnv); an unset one is sent
		// empty
		return envSyntax{refDollar, refDollar}
	case fmtCopilot:
		// Copilot CLI: "Supports $VAR, ${VAR}, and ${VAR:-default}
		// expansion" for env, "Supports variable expansion" for headers
		// (github/docs, copilot cli-command-reference.md); a command
		// inherits only PATH (add-mcp-servers.md)
		return envSyntax{refDollar, refDollar}
	case fmtGoose:
		// Goose 1.52.0 crates/goose/src/agents/extension.rs: resolve puts
		// ${NAME} into headers (and uri) from envs and env_keys only, never
		// straight from its environment; envs are given to a command as
		// written. merge_environments reads each env_keys name from the
		// environment (upper-cased), else from Goose's keyring.
		return envSyntax{refGoose, refGoose}
	case fmtPi:
		// pi-mcp-adapter 5.1 reads ${NAME}, $env:NAME and {env:NAME} in a
		// server's headers and env (utils.ts interpolateEnvVars,
		// docs/servers.md); a file pi-mcp-extension reads too is Literal
		return envSyntax{refDollar, refDollar}
	case fmtPiNative:
		// Pi 0.99+ resolves $NAME and ${NAME} in a server's headers and env
		// as it connects, and leaves a server with an unset one unconnected
		// (pi 1.0.4 dist/core/resolve-config-value.js,
		// dist/extensions/mcp/runtime.js createDefaultTransport). OmO
		// (senpi 2026.10.10) reads ${NAME} and ${NAME:-default} anywhere in
		// its mcp.json (dist/core/extensions/builtin/mcp/config.js
		// interpolateConfig); its command gets only HOME, LOGNAME, PATH,
		// SHELL, TERM and USER besides its env (docs/mcp.md)
		return envSyntax{refDollar, refDollar}
	case fmtOmp:
		// omp 18.6 expands ${NAME} and ${NAME:-default} in mcp.json as it
		// loads it (src/discovery/helpers.ts expandEnvVarsDeep); Qoder CLI
		// 1.1.64 expands $NAME, ${NAME} and ${NAME:-default} in settings.json
		// and again in a server's headers, url and env as it connects
		// (bundle/qodercli.js); AtomCode 5.2.1 expands ${NAME} in a server's
		// url, headers, command, args and env, "" for an unset one
		// (crates/atomcode-capabilities/src/mcp/config.rs expand_env_vars)
		return envSyntax{refDollar, refDollar}
	case fmtHermes:
		// Hermes reads ${NAME} (and ${env:NAME}) in config.yaml's
		// mcp_servers, ~/.hermes/.env loaded first, and won't connect a
		// remote server that still has one unset (tools/mcp_tool_config.py
		// _interpolate_env_vars, _require_rendered_remote)
		return envSyntax{refDollar, refDollar}
	case fmtCommandCode:
		// Command Code 1.73 reads ${NAME} and ${NAME:-default} in a server's
		// headers and env, and refuses one unset (dist/cli.mjs
		// resolveEnvPlaceholders)
		return envSyntax{refDollar, refDollar}
	case fmtDsh:
		// DeepSeek Harness evaluates a YAML !!js value of a plugin's config
		// as it activates the row, with its own process in reach (dsh
		// 0.2.0-rc.2: cordis-plugin-include's entryListSchema reads the
		// tag, quoted or not; cordis-plugin-loader's interpolate evaluates
		// it all through the config). dsh-mcp-client's README gives a
		// command GITHUB_TOKEN: !!js process.env.GITHUB_TOKEN, and a command
		// needs it so: it inherits dsh's environment without the
		// credential-shaped names (dsh-subprocess scrubbedParentEnv). Tried
		// (#1435): the server got the token, and with the variable unset
		// dsh left the server out, saying its config is invalid.
		return envSyntax{refDsh, refDsh}
	case fmtKiro:
		// Kiro expands ${VARIABLE_NAME} in mcp.json as it reads it
		// (kiro.dev/docs/mcp/configuration), the ones its Mcp Approved Env
		// Vars setting approves
		return envSyntax{refDollar, refDollar}
	case fmtLiveAgent:
		// K-brain, LiveAgent's engine, reads ${NAME} (and $NAME,
		// ${NAME:-default}) in a server's env and headers as it starts it,
		// and leaves out one whose variable is unset (internal/config/
		// secret.go ResolveEnvMapContext, ResolveHeaderContext; internal/
		// mcp/manager.go defaultTransport, K-brain 507f560)
		return envSyntax{refDollar, refDollar}
	}
	// None: Claude Desktop gives a command its env as written and only
	// HOME, LOGNAME, PATH, SHELL, TERM and USER of its own (2.7032's app.asar
	// Jv; ${...} is expanded for organisations' plugin servers only).
	// Antigravity parses mcp_config.json and sends what it says
	// (agy 1.2.16: no os.Expand on its MCP path; antigravity.google/docs/mcp
	// names none). Kimi reads mcp.json with json.loads and passes headers
	// and env on (kimi_cli/cli/__init__.py, fastmcp mcp_config.py). Cline's
	// settings are plain strings (@cline/core 0.0.90). ZCode expands only
	// its plugins' servers, not mcp.servers (zcode.cjs createTransport).
	// Zed hands a command its env and a url its headers as written
	// (zed-industries/zed 2c99f547: crates/context_server/src/transport/
	// stdio_transport.rs command.envs, http.rs build_request).
	// Alma
	// 0.4.164 JSON.parses mcp.json and hands a command its env and a url
	// its headers as written (out/main/index.js createStdioTransport,
	// connectRemoteServer). Devin's docs name ${env:NAME} for OAuth
	// fields only (extensibility/mcp/configuration.mdx), so its headers
	// and env aren't known to read one.
	return envSyntax{}
}

// refNames are the variables a value references.
func refNames(v string) []string {
	var out []string
	for _, m := range envRef.FindAllStringSubmatch(v, -1) {
		out = append(out, m[1])
	}
	return out
}

// hasRef says whether any value of m references a variable.
func hasRef(m map[string]string) bool {
	for _, v := range m {
		if envRef.MatchString(v) {
			return true
		}
	}
	return false
}

// HasEnvRefs says whether the server's headers or environment reference
// a variable.
func (s *Server) HasEnvRefs() bool { return hasRef(s.Headers) || hasRef(s.Env) }

// errNoEnvRef is why an agent that reads no reference isn't given a
// server with one.
var errNoEnvRef = errors.New("it reads no environment variable from its MCP config, so a server with ${NAME} in its headers or environment isn't given to it: written there, the token would be in its file as plain text")

// whole is the variable a value is in whole (${NAME}), or "".
func whole(v string) string {
	if m := envRef.FindStringSubmatch(v); m != nil && m[0] == v {
		return m[1]
	}
	return ""
}

// bearerOf is the variable Authorization's value "Bearer ${NAME}" names.
func bearerOf(k, v string) string {
	if !strings.EqualFold(k, "Authorization") {
		return ""
	}
	rest, ok := strings.CutPrefix(v, "Bearer ")
	if !ok {
		return ""
	}
	return whole(rest)
}

// refsProblem says why the agent can't be given the server's references,
// or nil when it can (or the server has none).
func (f *mcpFile) refsProblem(s *Server) error {
	r := f.refsOf()
	headers, env := s.Headers, s.Env
	if !s.Remote() {
		headers = nil
	} else {
		env = nil
	}
	if r.headers == refNone && hasRef(headers) || r.env == refNone && hasRef(env) {
		return errNoEnvRef
	}
	if r.headers == refCodex {
		for _, k := range slices.Sorted(maps.Keys(headers)) {
			v := headers[k]
			if envRef.MatchString(v) && whole(v) == "" && bearerOf(k, v) == "" {
				return fmt.Errorf("Codex reads a header from a variable only whole (%s: ${NAME}), or as Authorization: Bearer ${NAME}; %s isn't one", k, k)
			}
		}
	}
	if r.env == refCodex || r.env == refGoose {
		agent := "Codex"
		if r.env == refGoose {
			agent = "Goose"
		}
		for _, k := range slices.Sorted(maps.Keys(env)) {
			v := env[k]
			if envRef.MatchString(v) && whole(v) != k {
				return fmt.Errorf("%s passes a variable on only under its own name (%s: ${%s}); %s isn't one", agent, k, k, k)
			}
		}
	}
	if r.headers == refGoose || r.env == refGoose {
		// Goose reads an env_keys name upper-cased, so a lower-case one
		// would be some other variable
		for _, n := range gooseKeys(headers, env) {
			if n != strings.ToUpper(n) {
				return fmt.Errorf("Goose reads a variable's name upper-cased; ${%s} would be %s", n, strings.ToUpper(n))
			}
		}
	}
	return nil
}

// gooseKeys are the variables Goose is told to read (env_keys): those the
// headers reference and those a command is given.
func gooseKeys(headers, env map[string]string) []string {
	var out []string
	for _, m := range []map[string]string{headers, env} {
		for _, v := range m {
			for _, n := range refNames(v) {
				if !slices.Contains(out, n) {
					out = append(out, n)
				}
			}
		}
	}
	slices.Sort(out)
	return out
}

// refKeys are the keys of an agent's entry that name the variables the
// server references, magpie's to write only while it references one there.
func (f *mcpFile) refKeys(s *Server) []string {
	switch f.Format {
	case fmtCodex:
		if s.Remote() && hasRef(s.Headers) {
			return []string{"bearer_token_env_var", "env_http_headers"}
		}
		if !s.Remote() && hasRef(s.Env) {
			return []string{"env_vars"}
		}
	case fmtGoose:
		if s.Remote() && hasRef(s.Headers) || !s.Remote() && hasRef(s.Env) {
			return []string{"env_keys"}
		}
	}
	return nil
}

// toAgent is a value with its references in the agent's syntax.
func toAgent(syntax refSyntax, v string) string {
	switch syntax {
	case refOpenCode:
		return envRef.ReplaceAllString(v, "{env:$1}")
	case refCursor:
		return envRef.ReplaceAllString(v, "$${env:$1}")
	}
	return v
}

var (
	openCodeRef = regexp.MustCompile(`\{env:([A-Za-z_][A-Za-z0-9_]*)\}`)
	cursorRef   = regexp.MustCompile(`\$\{env:([A-Za-z_][A-Za-z0-9_]*)\}`)
)

// dshEnvRef is the expression dsh reads a variable whole with.
var dshEnvRef = regexp.MustCompile(`^process\.env\.([A-Za-z_][A-Za-z0-9_]*)$`)

// dshRef is a value with references as the !!js expression dsh evaluates
// to it, or false for one with none. A variable whole is process.env.NAME,
// as dsh-mcp-client's README writes one, so dsh leaves the server out
// while it's unset. In a longer value each variable is
// (process.env.NAME ?? "") and the text around it JSON strings, which
// JavaScript reads the same: what the user wrote is never code, and an
// unset variable is empty rather than "undefined".
func dshRef(v string) (dshJS, bool) {
	if n := whole(v); n != "" {
		return dshJS("process.env." + n), true
	}
	ms := envRef.FindAllStringSubmatchIndex(v, -1)
	if len(ms) == 0 {
		return "", false
	}
	var parts []string
	lit := func(s string) {
		if s != "" {
			b, _ := json.Marshal(s)
			parts = append(parts, string(b))
		}
	}
	at := 0
	for _, m := range ms {
		lit(v[at:m[0]])
		parts = append(parts, `(process.env.`+v[m[2]:m[3]]+` ?? "")`)
		at = m[1]
	}
	lit(v[at:])
	return dshJS(strings.Join(parts, " + ")), true
}

// dshPart is one operand of a longer value dshRef writes: a JSON string,
// or a variable with "" in its place while unset.
var dshPart = regexp.MustCompile(`^(?:"(?:[^"\\]|\\.)*"|\(process\.env\.([A-Za-z_][A-Za-z0-9_]*) \?\? ""\))`)

// dshUnref is the value an expression dshRef writes stands for, its
// references as the library writes them; false for any other expression,
// which is dsh's to work out.
func dshUnref(e dshJS) (string, bool) {
	s := string(e)
	if m := dshEnvRef.FindStringSubmatch(s); m != nil {
		return "${" + m[1] + "}", true
	}
	var b strings.Builder
	refs := 0
	for i := 0; i == 0 || s != ""; i++ {
		if i > 0 {
			rest, ok := strings.CutPrefix(s, " + ")
			if !ok {
				return "", false
			}
			s = rest
		}
		m := dshPart.FindStringSubmatchIndex(s)
		if m == nil {
			return "", false
		}
		if m[2] >= 0 {
			b.WriteString("${" + s[m[2]:m[3]] + "}")
			refs++
		} else {
			var lit string
			if json.Unmarshal([]byte(s[:m[1]]), &lit) != nil {
				return "", false
			}
			b.WriteString(lit)
		}
		s = s[m[1]:]
	}
	if refs == 0 {
		return "", false
	}
	return b.String(), true
}

// dshRefs is m as dsh's entry has it, a value with a reference as its
// expression; nil stays nil.
func dshRefs(m map[string]string) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		if e, ok := dshRef(v); ok {
			out[k] = e
		} else {
			out[k] = v
		}
	}
	return out
}

// dshUnrefs is a dsh entry's headers or env with each expression dshRef
// writes read back as the value it stands for; any other value, and
// anything but a map, as it is.
func dshUnrefs(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	out := make(map[string]any, len(m))
	for k, x := range m {
		if e, ok := x.(dshJS); ok {
			if s, ok := dshUnref(e); ok {
				out[k] = s
				continue
			}
		}
		out[k] = x
	}
	return out
}

// fromAgent is a value the agent has, with its references as the library
// writes them.
func fromAgent(syntax refSyntax, v string) string {
	switch syntax {
	case refOpenCode:
		return openCodeRef.ReplaceAllString(v, "$${$1}")
	case refCursor:
		return cursorRef.ReplaceAllString(v, "$${$1}")
	}
	return v
}

// mapTo is m with every value in the agent's syntax; nil stays nil.
func mapTo(syntax refSyntax, m map[string]string) map[string]string {
	if m == nil || syntax != refOpenCode && syntax != refCursor {
		return m
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = toAgent(syntax, v)
	}
	return out
}

// mapFrom is m with the agent's references as the library writes them.
func mapFrom(syntax refSyntax, m map[string]string) map[string]string {
	if m == nil || syntax != refOpenCode && syntax != refCursor {
		return m
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = fromAgent(syntax, v)
	}
	return out
}

// codexHeaders splits headers into those Codex sends as written, the
// variable Authorization's bearer token is read from, and the headers it
// reads whole from a variable.
func codexHeaders(h map[string]string) (plain map[string]string, bearer string, fromEnv map[string]string) {
	for _, k := range slices.Sorted(maps.Keys(h)) {
		v := h[k]
		switch {
		case !envRef.MatchString(v):
			if plain == nil {
				plain = map[string]string{}
			}
			plain[k] = v
		case bearerOf(k, v) != "" && bearer == "":
			bearer = bearerOf(k, v)
		default:
			if fromEnv == nil {
				fromEnv = map[string]string{}
			}
			fromEnv[k] = whole(v)
		}
	}
	return
}

// codexEnv splits a command's environment into what Codex sets as written
// and the variables it passes on by name.
func codexEnv(e map[string]string) (plain map[string]string, pass []string) {
	for _, k := range slices.Sorted(maps.Keys(e)) {
		if v := e[k]; envRef.MatchString(v) {
			pass = append(pass, k)
		} else {
			if plain == nil {
				plain = map[string]string{}
			}
			plain[k] = v
		}
	}
	return
}

// putRef sets k to v in *into, made if need be, unless k is there already
// (a value written out wins over the variable it's read from).
func putRef(into *map[string]string, k, v string) {
	if *into == nil {
		*into = map[string]string{}
	}
	if _, ok := (*into)[k]; !ok {
		(*into)[k] = v
	}
}

// codexEnvVars are the names a Codex entry's env_vars passes on: each a
// name, or a table with one (codex-rs's McpServerEnvVar); Goose's env_keys
// is a list of names too.
func codexEnvVars(v any) []string {
	a, _ := v.([]any)
	var out []string
	for _, x := range a {
		switch e := x.(type) {
		case string:
			out = append(out, e)
		case map[string]any:
			if n, _ := e["name"].(string); n != "" {
				out = append(out, n)
			}
		}
	}
	return out
}

// loginVars is the reader of what a terminal has for variables magpie's
// own environment lacks (proc.LoginVars); a var for tests.
var loginVars = proc.LoginVars

// expandAll is m with every reference replaced by the variable's value,
// for magpie to reach the server itself, and the variables set nowhere,
// sorted. A variable is magpie's own, else the login shell's (the
// registry's on Windows): a magpie opened from the Finder has none of the
// profile's, and a server whose token was exported in .zshrc showed no
// tools (Cyrus on Discord). The value is only in what is returned: never
// in library.json, an agent's file, magpie's environment or a log.
func expandAll(m map[string]string) (map[string]string, []string) {
	vals := map[string]string{}
	var missing []string
	for _, v := range m {
		for _, n := range refNames(v) {
			if _, have := vals[n]; have || slices.Contains(missing, n) {
				continue
			}
			if x, ok := os.LookupEnv(n); ok {
				vals[n] = x
			} else {
				missing = append(missing, n)
			}
		}
	}
	var unset []string
	if len(missing) > 0 {
		got := loginVars(missing)
		for _, n := range missing {
			if x, ok := got[n]; ok {
				vals[n] = x
			} else {
				unset = append(unset, n)
			}
		}
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = envRef.ReplaceAllStringFunc(v, func(r string) string { return vals[r[2:len(r)-1]] })
	}
	slices.Sort(unset)
	return out, unset
}
