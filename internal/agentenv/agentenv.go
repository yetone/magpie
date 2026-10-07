// Package agentenv names the environment variables through which an agent
// really installed on this machine reaches magpie: the one list a test
// sandbox clears, so that a test meets only the agents it put there itself.
//
// A sandbox fakes magpie's own folders through HOME, USERPROFILE and the XDG
// variables (appdir decides those), and an agent's folder through the
// variable that agent's own CLI reads before it falls back to its folder in
// the home: CLAUDE_CONFIG_DIR, CODEX_HOME, QODER_CONFIG_DIR and the rest.
// Any one of them left over from the developer's shell points magpie at a
// real agent, whose sessions and settings a test then reads — and, where
// magpie writes what it wires, writes into (#522). Qoder's CLI sets both of
// its variables for every child it starts, and magpie manages Qoder, so
// writing magpie from inside Qoder was one way to meet this.
//
// Each package keeps its own sandbox, because a test in internal/sessions
// cannot import internal/agent, which imports it, to ask which variables
// there are; this is the list they all clear. A name here is one magpie
// reads, and TestVarsAreRead says so, so a variable magpie stopped reading
// does not linger — that is how GEMINI_CLI_HOME and OPENCODE_CONFIG came to
// be cleared by a sandbox while OPENCODE_CONFIG_DIR, the one magpie reads,
// was not. What is deliberately not here: APPDATA and LOCALAPPDATA, Windows'
// folders rather than an agent's, which a sandbox sets to a folder of its
// own instead of clearing; and CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC, a
// behaviour flag whose one test sets it on purpose to read what magpie then
// does.
package agentenv

// Vars are the variables, by the agent they belong to. A sandbox clears all
// of them, and sets back the ones its own fixtures need.
var Vars = []string{
	// Claude Code, Codex and Copilot CLI
	"CLAUDE_CONFIG_DIR", "CODEX_HOME", "COPILOT_HOME",
	// Claude Code's temp folder, the images its sessions were given
	"CLAUDE_CODE_TMPDIR",
	// Zed, or a fork of it that keeps its settings (ZedG)
	"MAGPIE_ZED_BIN", "MAGPIE_ZED_CONFIG_DIR", "MAGPIE_ZED_PROCESS_NAMES",
	// Codex's state database, when it is kept apart from CODEX_HOME
	"CODEX_SQLITE_HOME",
	// Gemini CLI's session/config home
	"GEMINI_CLI_HOME",
	// Cline: its folder, its data, its sessions and its MCP settings file
	"CLINE_DIR", "CLINE_DATA_DIR", "CLINE_SESSION_DATA_DIR", "CLINE_MCP_SETTINGS_PATH",
	// Pi and its forks (OmO, Senpi), whose profile and config are apart
	"PI_CODING_AGENT_DIR", "PI_CODING_AGENT_SESSION_DIR", "PI_CONFIG_DIR", "PI_PROFILE",
	"OMO_CODING_AGENT_DIR", "SENPI_CODING_AGENT_DIR",
	// omp
	"OMP_PROFILE",
	// Qoder's two builds, the global site's and China's
	"QODER_CONFIG_DIR", "QODERCN_CONFIG_DIR",
	// Grok: its home, and where its CLI is installed
	"GROK_HOME", "GROK_BIN_DIR",
	// Kimi Code and its shared folder
	"KIMI_CODE_HOME", "KIMI_SHARE_DIR",
	// Qwen Code's home, ~/.qwen without it
	"QWEN_HOME",
	// Ante
	"ANTE_HOME",
	// MiMo Code, MiniMax Code, OpenHanako, Hermes, dsh, WorkBuddy, CodeBuddy Code
	"MIMOCODE_HOME", "MINIMAX_DATA_DIR", "HANA_HOME", "HERMES_HOME", "DSH_HOME",
	"WORKBUDDY_CONFIG_DIR", "CODEBUDDY_CONFIG_DIR",
	// alma-server's data folder (Alma without a desktop, on Linux)
	"ALMA_DATA_DIR",
	// Reasonix Studio and its native CLI share this config home
	"REASONIX_HOME",
	// Mister Morph's config file
	"MISTER_MORPH_CONFIG",
	// T3 Code's base folder (its settings in userdata/)
	"T3CODE_HOME",
	// AtomCode's config folder
	"ATOMCODE_HOME",
	// Snow CLI's config folder, ~/.snow without it
	"SNOW_CONFIG_DIR",
	// Cursor's CLI: its config folder (its chats) and its data folder
	"CURSOR_CONFIG_DIR", "CURSOR_DATA_DIR",
	// OpenCode and OpenChamber
	"OPENCODE_CONFIG_DIR", "OPENCODE_DB", "OPENCHAMBER_DATA_DIR",
	// Droid's home, Windsurf's API server, ZCode's credential seed: what
	// makes an account or an installation visible that the test didn't make
	"FACTORY_HOME_OVERRIDE", "WINDSURF_API_SERVER_URL", "ZCODE_CREDENTIAL_SECRET",
	// Google Cloud's credentials file and gcloud's folder, which Vertex AI's
	// tokens are minted from (and Gemini CLI's own, on Vertex AI)
	"GOOGLE_APPLICATION_CREDENTIALS", "CLOUDSDK_CONFIG",
}

// NotPaths are the Vars that name no folder or file under the working
// folder: a profile's name, a server's address, a secret, or a name its
// agent puts under a folder of its own (PI_CONFIG_DIR under the home,
// OPENCODE_DB in OpenCode's data folder), which a relative value is meant
// for.
var NotPaths = map[string]bool{
	"PI_PROFILE": true, "OMP_PROFILE": true,
	"WINDSURF_API_SERVER_URL": true, "ZCODE_CREDENTIAL_SECRET": true,
	"PI_CONFIG_DIR": true, "OPENCODE_DB": true,
	"MAGPIE_ZED_BIN": true, "MAGPIE_ZED_PROCESS_NAMES": true,
}
