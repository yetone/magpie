# magpie reference

The full manual: every agent magpie wires, every provider option, the gateway, routing, plugins, the CLI, Docker, sync and the files magpie keeps. For the overview, see the [README](../README.md).

Renamed a project folder? See [relocating Claude Code sessions](session-relocation.md).

One place to pick every agent's model: Codex on DeepSeek, Claude Code
on Kimi, Gemini CLI on GLM, from the menu bar. [usemagpie.ai](https://usemagpie.ai)

[![Discord](https://img.shields.io/badge/Discord-join%20the%20community-5865F2?logo=discord&logoColor=white)](https://discord.gg/vGSnD3ZKQF)

`magpie` is a single screen that lists each AI agent on your machine and
the model it is set to. Click a value, pick a model. That is the whole app.

It lives in the menu bar: click the icon and a panel drops down; the same
screen also opens as a normal window (`magpie`, or *Open magpie* in the tray menu),
which opens on the page you last left, as a Mac app comes back as it was,
and there is a terminal version (`magpie tui`) and a plain CLI. The
terminal version serves the gateway while it is open when no other magpie
(the app, `magpie web`, `magpie serve`) does, and says so on its status
line; agents connected to magpie lose it when it quits.

```
  ◉ magpie

  ▸ Claude Code   claude-fable-5-1[1m]                        ~/.claude/settings.json
    Codex         gpt-6-astra   effort medium
    Gemini CLI    gemini-3.1-pro
    OpenCode      anthropic/claude-sonnet-5   small anthropic/claude-haiku-4-5
    MiMo Code     anthropic/claude-sonnet-5
    Pi            openrouter/z-ai/glm-5.2:batch
    Goose         anthropic/claude-sonnet-5
    Cursor CLI    auto
    Copilot CLI   claude-fable-5

  ↑↓ agent  ·  ←→ field  ·  ↵ change  ·  s save profile  ·  p profiles  ·  q quit
```

- **One binary.** About 30 MB with the desktop app (it uses the system
  webview through [Wails](https://wails.io), nothing bundled; a 15 MB
  download on a Mac), and under 30 MB for the terminal-only build. macOS,
  Linux and Windows.
- **Edits config files surgically.** Only the one key you change is touched;
  comments, ordering and indentation in your `settings.json`, `config.toml`,
  `opencode.jsonc` or `config.yaml` survive intact. Writes are atomic.
- **One endpoint for every agent.** magpie runs a local gateway that speaks
  OpenAI chat completions, OpenAI Responses and the Anthropic Messages API,
  and forwards to whichever vendor serves the model. Codex, Claude Code,
  OpenCode and the rest all point at `http://127.0.0.1:3425/v1` and pick
  from one catalog; the translation between APIs happens in magpie, streaming
  and tool calls included.
- **Your subscriptions, shared.** Sign in to Claude Code, Codex (ChatGPT)
  or Copilot and that login shows up as a provider: every other agent can
  use its models through the gateway, with nothing copied and no key to
  paste.
- **Providers with one field.** Pick a preset (Anthropic, OpenAI, Gemini,
  Amazon Bedrock and its Mantle endpoint, DeepSeek, Kimi, GLM, MiniMax, StepFun, Qwen, Baidu Qianfan, Tencent Cloud Token Plan,
  Huawei Cloud MaaS, Volcengine Ark, Mistral, Groq, Nous Portal, xAI, OpenRouter, Together,
  Fireworks, SiliconFlow, NVIDIA NIM, ModelScope, AiHubMix, PipeLLM, 302.AI, Ollama, LM Studio…),
  paste a key, done. Custom vendors need a name and a base URL. magpie never
  reads keys from your shell environment.
- **Real model lists, nothing compiled in.** With a key in hand magpie asks
  the vendor which models it serves and offers exactly those; the
  [models.dev](https://models.dev) catalog fills in names, reasoning efforts
  and the list for vendors that have none (a model the vendor's own entry
  doesn't list is named as the other providers serving it name it, so
  glm-5-turbo reads GLM-5-Turbo under Zhipu as under ZCode; an Azure
  deployment keeps its own name), and refreshes itself in the
  background once it goes stale. Choose which models each provider exposes,
  or expose them all — a model released this morning is in the picker on
  the next refresh.
- **Each agent's own model list.** Under an agent's name on the Agents page,
  "Showing 5 / 32 models" opens its list: click a model to take it out of
  that agent's picker (Codex's `/model` included, its ChatGPT models too) or
  put it back; other agents still use it, and a new model is shown. Its
  "Only models I pick" switch turns the list around: the models ticked
  when it is switched on stay, and a model added later, of any provider,
  stays out of that agent's picker and its config until it is ticked
  (`magpie visible <agent> --only-picked`, back with `--show-new`). A
  model asked for by name still works either way.
- **Profiles.** Snapshot every agent's settings under a name and switch all of
  them back in one move. The profile in use, the one applying would change
  nothing, is ticked, the tray panel's Profiles button shows its name, and
  `magpie profiles` marks it ✓.
- **Real logos, no framework.** Plain HTML over the system webview; brand
  icons from [lobehub/icons](https://github.com/lobehub/lobe-icons).

## Agents

| Agent        | File                              | Fields          |
| ------------ | --------------------------------- | --------------- |
| Claude Code  | `~/.claude/settings.json`         | provider, model, opus/sonnet/haiku/fable, sign-in (through magpie) |
| Claude Desktop | `Claude/` + `Claude-3p/configLibrary/` in `~/Library/Application Support` (`%LOCALAPPDATA%` on Windows, `~/.config` on Linux). A Desktop installed as an MSIX package on Windows keeps them in its package instead (`%LOCALAPPDATA%\Packages\Claude_<publisher>\LocalCache\Local`, or `AnthropicPBC.Claude_<publisher>` for the Store's package, its own data in `LocalCache\Roaming\Claude`). Desktop 2.31226 and later, packaged, reads the real `%LOCALAPPDATA%\Claude-3p` instead, so for a packaged Desktop magpie writes both ([`desktopdir`](../internal/desktopdir/desktopdir.go)) | provider (its third-party gateway mode: Code and Cowork on magpie, no Anthropic sign-in; restart Desktop), opus/sonnet/haiku/fable, subagent model and subagent effort (magpie's settings, applied by the gateway to subagent tool turns; no restart) |
| Codex        | `~/.codex/config.toml`            | provider, model, effort |
| Gemini CLI   | `~/.gemini/settings.json`, `~/.gemini/.env` | auth, model |
| OpenCode     | `~/.config/opencode/opencode.json(c)` (`$OPENCODE_CONFIG_DIR`) | model, small |
| OpenChamber  | `~/.config/openchamber/preferences.json` (`$OPENCHAMBER_DATA_DIR`; magpie's provider in OpenCode's config) | model, small (its own defaults, over OpenCode's) |
| MiMo Code    | `~/.config/mimocode/mimocode.json(c)` | model, small |
| DevEco Code  | `~/.config/deveco/deveco.json(c)` (`$DEVECO_CONFIG_DIR`; its key is `magpie-deveco`, as parts of it send OpenCode's User-Agent) | model, small |
| Pi           | `~/.pi/agent/settings.json`       | model           |
| StepCode     | `~/.stepcode/config.toml` (+ `models.json`; `$STEP_CODING_AGENT_DIR`, `$STEPCODE_CONFIG_DIR`) | model |
| MothX        | `~/.mothx/settings.json` (`$MOTHX_DIR`; `%APPDATA%\mothx` on Windows) | model, thinking (`defaultProvider`/`defaultModel`/`defaultThinkingLevel`; magpie as the provider `magpie` beside the user's own, `openai-chat` with `thinkingFormat: "openai"`) |
| Aside        | `~/.aside/u/0/settings.json` (+ `models.json`; the first account, the only one magpie wires) | model, effort, fast, standard, deep, visual, image |
| OmO (omo-ai) | `~/.omo/agent/settings.json` (+ `models.json`; `$OMO_CODING_AGENT_DIR`, `$SENPI_CODING_AGENT_DIR`) | model |
| Prime Agent  | `~/.prime/agent/settings.json` (+ `models.json`; `$PRIME_AGENT_CODING_AGENT_DIR`) | model, thinking (Pi's fields; its key `magpie-prime-agent` names it, since its requests carry no User-Agent; the global settings alone, `allowedModels` and `subagentDefaultModel` left as they are) |
| Goose        | `~/.config/goose/config.yaml`     | model           |
| Cursor CLI   | `~/.cursor/cli-config.json`       | model           |
| Zed          | `~/.config/zed/settings.json` (`$XDG_CONFIG_HOME` on Linux, `%APPDATA%\Zed` on Windows) | model (a `magpie` OpenAI-compatible provider; its catalog in Zed's picker) |
| VS Code (Chat) | `~/Library/Application Support/Code/User/settings.json` + `chatLanguageModels.json` (`~/.config/Code/User` on Linux, `%APPDATA%\Code\User` on Windows) | model (`chat.defaultModel`; a `magpie` Custom Endpoint group, its catalog in Chat's model picker; VS Code 1.122+, no Copilot sign-in or key needed). Each profile's own pair under `User/profiles/<id>/` (listed in `globalStorage/storage.json`) gets the same, unless the profile uses the default's |
| VS Code Insiders (Chat) | the same files under `Code - Insiders/User` in place of `Code/User` | as VS Code, a row of its own; its models send the token `magpie-vscode-insiders`, since its chat's User-Agent is VS Code's |
| VSCodium (Chat) | the same files under `VSCodium/User` in place of `Code/User` | as VS Code, a row of its own; its models send the token `magpie-vscodium`, so Usage counts them as VSCodium; its Chat features must be turned on (`chat.disableAIFeatures=false`, and `defaultChatAgent` plus `trustedExtensionAuthAccess` for GitHub.copilot-chat in its product.json). With the Marketplace's GitHub Copilot Chat 0.48.1, which has no Custom Endpoint provider, the group is written for its OpenAI Compatible provider (`customoai`, the token sent as `x-api-key`), which lists models only while Copilot Chat is signed in to GitHub with a personal Copilot plan |
| Copilot (JetBrains) | `byok.json` in `~/.config/github-copilot` (`$XDG_CONFIG_HOME/github-copilot` when set, `%USERPROFILE%\AppData\Local\github-copilot` on Windows), the Copilot language server's own file, which Xcode's and Eclipse's Copilot read too | provider (a `magpie` custom endpoint provider beside the user's: every magpie model at the gateway's `/v1/chat/completions`, sent with `magpie-copilot-jetbrains`; a model hidden in Manage Models stays hidden through a sync; off removes magpie's three keys alone). Found by the `github-copilot-intellij` plugin in a JetBrains IDE or Android Studio. Listed after the IDE restarts, and only while Copilot is signed in to GitHub on a plan with Bring Your Own Key (Free, Pro, Pro+; Business and Enterprise when the organisation allows it); the model is picked in Copilot Chat |
| JetBrains Air | `acp.json` in `~/Library/Application Support/JetBrains/Air` (`~/.config/JetBrains/Air` on Linux, `%APPDATA%\JetBrains\Air` on Windows) + `magpie-opencode.json` beside it | model (a `Magpie` ACP agent: OpenCode's `opencode acp` on magpie's provider alone, its models and routing groups in Air's model menu; needs OpenCode installed) |
| Copilot CLI  | `~/.copilot/settings.json`        | model           |
| Crush        | `~/.config/crush/crush.json`      | large, small    |
| DeepSeek Harness (dsh) | `~/.dsh/profiles/*/cordis.patch.yml` (`$DSH_HOME`; a custom provider, Magpie, its key `MAGPIE_GATEWAY_KEY` in `~/.dsh/.env` and dsh's own key store `~/.dsh/.credentials.yaml`, which the desktop app reads; on one of magpie's models its `web-search-deepseek` row also goes to the gateway, which searches with the model or Settings › Web search, unless dsh has a `DEEPSEEK_API_KEY` or a row of your own), or `~/.dsh/config.yaml` before dsh 0.1.5 | model, effort |
| Reasonix Studio (2.x, or a native Go `reasonix` 2.x / 1.39.x CLI) | `~/.reasonix/config.toml` + `.env` (`%APPDATA%/reasonix` on Windows; `$REASONIX_HOME`) | model (Executor), planner (Plan), effort (a dedicated `magpie` provider, shared by Studio and the native CLI) |
| Command Code | `~/.commandcode/settings.json` (+ `providers.json`) | model |
| fx           | `~/.fx/settings.json`             | model (a keyless `magpie` provider) |
| omp (oh-my-pi) | `~/.omp/agent/config.yml` (+ `models.yml`) | model |
| Devin        | `~/.config/devin/config.json` (`%APPDATA%\devin\config.json` on Windows) | model |
| Hermes Agent | `~/.hermes/config.yaml` (`%LOCALAPPDATA%\hermes` on Windows; `$HERMES_HOME`), and each profile's under `profiles/` | model |
| Mister Morph | `~/.morph/config.yaml` (`$MISTER_MORPH_CONFIG`) | model, effort (`llm` on the gateway as `openai_response_compatible`, the Responses API; what it had comes back when you switch away) |
| Kimi Code    | `~/.kimi/config.toml` (`$KIMI_SHARE_DIR`) | model (a `magpie` provider; magpie's models in Kimi's /model) |
| Qwen Code    | `~/.qwen/settings.json` (`$QWEN_HOME`) | model (magpie's models as `modelProviders.openai` entries on a `MAGPIE_QWEN_API_KEY` env var; settings.model; `security.auth.selectedType` openai while wired) |
| Muse Code    | `~/.config/muse/settings.json` (`$XDG_CONFIG_HOME`) | model (endpoint_transport to the gateway, auth none; magpie's models in Muse's list) |
| Empryo       | `~/.empryo/config.json` | defaultModel (a `magpie` provider at the gateway in `providers`) |
| Ante         | `~/.ante/catalog.json` and `settings.json` (`$ANTE_HOME`) | provider, model (a `magpie` provider on OpenAiCompatible, with no auth of magpie's: the gateway lets this machine in with any token and Ante counts a provider with none as authenticated; magpie's models in Ante's model picker, and Ante started on magpie, which is what its settings' `provider` decides) |
| AstrBot      | `data/cmd_config.json` under `$ASTRBOT_ROOT`, else `~/.astrbot` (the desktop app's) | model (a `magpie` entry in `provider_sources` and magpie's models in `provider`, the user's own entries kept; the default chat model in `agent_runner.config.model.provider_id`, or `provider_settings.default_provider_id` on AstrBot before 4.28's agent runner; restart AstrBot after connecting; Docker installs aren't reached) |
| jcode        | `config.toml` in `$JCODE_HOME`, else `~/.jcode` | model (a `[providers.magpie]` profile, `openai-compatible`, with magpie's models in it; `[provider] default_provider`/`default_model` set to it, what was there put back on Disconnect; the user's own keys in the profile, such as `headers` and a model's `reasoning_effort`, kept; restart open jcode sessions after connecting) |
| MiniMax Code (mcode) | `~/.minimax/config.yaml` (`$MINIMAX_DATA_DIR`) | model (a `magpie` custom provider; magpie's models in its /model) |
| Droid (Factory) | `~/.factory/settings.json` (`$FACTORY_HOME_OVERRIDE`) | model (magpie's models as BYOK `customModels`, in Droid's /model) |
| Cline (CLI)  | `~/.cline/data/settings/providers.json` (`$CLINE_DIR`) | model, effort (magpie takes its openai-compatible provider) |
| Qoder (CLI)  | `~/.qoder/settings.json` (`$QODER_CONFIG_DIR`) | model, effort (a `magpie` custom provider; needs a Qoder plan with BYOK) |
| Qoder CN (CLI) | `~/.qoder-cn/settings.json` (`$QODERCN_CONFIG_DIR`) | model, effort (as Qoder; its own accounts, a Qoder CN plan with BYOK) |
| Grok Build   | `~/.grok/config.toml` (`$GROK_HOME`) | model, effort |
| Grok App     | `agent-home/config.toml` in its data folder (`$GROK_APP_HOME`, else `~/Library/Application Support/com.grokapp.grok-app`, `%APPDATA%\grokapp\grok-app\data`, `$XDG_DATA_HOME/grok-app`, or `~/.grok-app`) | model (a `[model.magpie]` custom provider under Accounts, with magpie's models and their effort levels in `app_models`; the route, `[models] default`, stays until the user presses Use on magpie there (one of the App's own routes picked in magpie is written as the route, and magpie's channel goes); a route an older magpie set to `magpie` is put back once; Disconnect drops the channel and puts the route back only if it is still `magpie`, else the one it was on before Connect if none is left; a model or default effort picked in the App kept; instructions, MCP and skills are the Library's Grok App row on agent-home; RTK has no hook for it or for Grok Build) |
| ZCode        | `~/.zcode/v2/config.json`         | provider (magpie's models in ZCode's picker) |
| WorkBuddy    | `~/.workbuddy/models.json` (`$WORKBUDDY_CONFIG_DIR`) | provider (magpie's models in WorkBuddy's picker) |
| WorkBuddy AI | `~/.workbuddy-ai/models.json` (the international build) | provider (magpie's models in WorkBuddy AI's picker) |
| CodeBuddy Code | `~/.codebuddy/models.json` (`$CODEBUDDY_CONFIG_DIR`), `settings.json` | model (magpie's models in its list; the session model) |
| Raycast      | `~/.config/raycast/ai/providers.yaml` in the user's folder, on Mac and Windows | provider (a `magpie` entry in Raycast AI's Custom Providers, magpie's models in its model picker; Raycast Pro; the user's providers, `tools` and comments kept) |
| OpenPencil   | `openpencil/settings.json` in the config folder: `~/Library/Application Support` (macOS), `$XDG_CONFIG_HOME` or `~/.config` (Linux), `%APPDATA%` (Windows) | provider (a `magpie` entry in its `builtin_agents`, magpie's models in its chat's model picker; the user's entries and settings kept; refused while OpenPencil runs, as it writes the file back when it quits) |
| T3 Code      | `~/.t3/userdata/settings.json` (`$T3CODE_HOME/userdata`) | provider (a `magpie` provider instance on Claude Code, magpie's models as its custom models) |
| OpenHanako   | `~/.hanako/provider-catalog.json` + `agents/<id>/config.yaml` (`$HANA_HOME`; its local API while it runs) | model (the primary agent's; magpie's models as a provider) |
| AtomCode     | `~/.atomcode/config.toml` (`$ATOMCODE_HOME`) | model, effort (a `magpie` provider account, one model table per catalog model as its own sign-in writes) |
| Snow CLI     | `~/.snow/profiles/magpie.json` (`$SNOW_CONFIG_DIR`), made the active profile and copied to `config.json` | model (the user's other profiles' models too; Snow App takes the profile in with "Sync Snow CLI API config") |
| Alma         | Alma's local API (`localhost:23001`, while Alma runs; alma-server's data in `$ALMA_DATA_DIR`, `$XDG_DATA_HOME/alma` or `~/.local/share/alma` on Linux) | model (Alma's default; magpie's models as a provider), and Image Generation's model when it is Auto: the one magpie draws with |
| Lody | Its own confirmation dialog (`lody://provider/import`) | adds selected Claude, Codex, Pi, DSH, Kimi Code, Grok and Bub Providers on the local machine; no settings edited by magpie |

Provider-scoped agents (OpenCode, MiMo Code, DevEco Code, Pi, OmO, StepCode, MothX, Aside, Goose, Crush, omp, Hermes Agent) take `provider/model`.
Only agents that are installed or configured are shown.

### Notes on some agents

#### StepCode

StepCode is configured via [`stepcode.go`](../internal/agent/stepcode.go). Magpie manages global `defaultProvider` and `defaultModel` in `~/.stepcode/config.toml` (or the parent of an absolute `STEP_CODING_AGENT_DIR`) and writes its catalog into `~/.stepcode/models.json` using Pi's provider format. Magpie only manages model selection; StepCode controls thinking levels itself. Project-local `.stepcode/config.toml` overlays take precedence over global settings. Restart StepCode or begin a new session after changing models. Detection is strictly directory-based (`~/.stepcode`), so having Smallstep's unrelated `step` certificate CLI on `PATH` is never mistaken for StepCode.

The optional real CLI verification test is run with `MAGPIE_TEST_STEPCODE_BIN=/path/to/step go test ./internal/agent -run '^TestStepCodeRealCLISandbox$' -count=1`. It runs StepCode in an isolated temporary sandbox with telemetry disabled and without live credentials.

#### MothX

MothX (oschina/mothx, formerly vibecoding) is configured via [`mothx.go`](../internal/agent/mothx.go). Magpie writes its provider `magpie` into `providers` in MothX's global `settings.json` (`$MOTHX_DIR`, else `%APPDATA%\mothx` on Windows, else `~/.mothx`) and sets `defaultProvider`, `defaultModel` and `defaultThinkingLevel`. The provider uses `openai-chat` with `thinkingFormat: "openai"`, so a thinking level goes out as `reasoning_effort` for every model; without it MothX guesses Qwen's or DeepSeek's thinking fields from a model id. What the user adds to magpie's block (headers, a `thinkingFormat` of their own) stays. The key `magpie-mothx` names it, and so does its User-Agent (`Vibecoding Client/<version>`). A project's `.mothx/settings.json` overlays the global one and is left alone. MothX reads its settings at start-up, so restart open `mothx` sessions after a change. Disconnect puts `settings.json` back byte for byte when nothing else changed in it; when MothX or the user changed it meanwhile (its `/model` and `/statusline … global` write the whole file), Disconnect takes out only magpie's provider, its pick and a `providers` object it left empty.

#### Reasonix Studio

Reasonix Studio is detected from its desktop installation or a native Go
`reasonix` 2.x or 1.39.x CLI; historical npm wrappers and a shared config alone
do not count. Tested with
[Studio 2.24.0](https://github.com/esengine/DeepSeek-Reasonix/releases/tag/studio-v2.24.0).

Pick the Executor model in the app, or use
`magpie reasonix magpie/deepseek/deepseek-chat` (`magpie reasonix
magpie/group/code` for a routing group). Select Plan independently with
`magpie reasonix planner magpie/deepseek/deepseek-chat`, or use `magpie reasonix
planner off` to disable the separate planner. `magpie reasonix effort high` sets
an advertised Executor reasoning level. `magpie reasonix default` restores the
previous Executor selection; `magpie reasonix planner default` restores Plan.
Magpie's provider and private `.env` key are removed when neither role needs
them. Restart Studio for new sessions; project/session overrides still take
precedence. Other providers and credentials are preserved. A non-managed
provider named `magpie` is a conflict, reported without overwriting it. Studio
and the native CLI share these files, so updating only Studio does not isolate
their settings.

The released-client stream/tool contract test is run with
`MAGPIE_TEST_REASONIX_CLI=/path/to/reasonix go test ./internal/agent -run
'^TestReasonixStudioCLIIntegration$' -count=1`. It uses isolated settings and
a local test upstream, with no vendor credentials.

#### VSCodium Chat

VSCodium's Chat features are disabled by default. To use the Chat model
picker with magpie, set `"chat.disableAIFeatures": false` in VSCodium's
settings and add the `defaultChatAgent` and `trustedExtensionAuthAccess`
entries required by [VSCodium's Copilot guide](https://github.com/VSCodium/vscodium/blob/master/docs/ext-github-copilot.md)
to VSCodium's `product.json`. The guide also explains how to install a
compatible GitHub Copilot Chat extension, since the Open VSX registry does
not normally provide Microsoft's extension. Restart VSCodium, or run
**Developer: Reload Window**, after changing these files. Magpie's models
then appear under the `magpie` custom endpoint group.

#### Zed-compatible Agent paths

Magpie can use a Zed-compatible fork or installation whose executable or
configuration directory is different from the upstream defaults. Set these
variables before starting Magpie:

```sh
export MAGPIE_ZED_BIN=/Applications/ZedG.app/Contents/MacOS/zedg
export MAGPIE_ZED_CONFIG_DIR="$HOME/.config/zed"
export MAGPIE_ZED_PROCESS_NAMES=zedg,ZedG
magpie serve
```

`MAGPIE_ZED_BIN` controls installation detection, `MAGPIE_ZED_CONFIG_DIR`
selects the directory containing `settings.json`, and
`MAGPIE_ZED_PROCESS_NAMES` supplies comma-separated process names used for
restart notices. The existing Zed configuration adapter is reused, so this is
intended for forks that keep Zed's `settings.json` and Agent model schema, such
as [ZedG](https://github.com/x6nux/zed-globalization). These variables affect
the Magpie process in which they are set; put them in the service environment
when running Magpie under systemd or Docker. They configure a Zed-compatible
Agent on the same machine as Magpie; they do not discover or modify an Agent
running on another host. On macOS, if the configured binary does not resolve to
a `.app` bundle, Magpie falls back to the standard Zed application locations
when authorizing the gateway credential.

## Providers and the gateway

Every model an agent can pick is spelled `provider/model` and served by
magpie's gateway, so agents never hold vendor keys or vendor URLs. Add a
provider, and its models appear in every agent's picker:

```sh
magpie presets                          # the vendors magpie knows, grouped: vendors, relays, local
magpie provider add deepseek sk-…       # a preset needs only the key
magpie provider add ollama              # local servers need none
magpie provider add "My Relay" url=https://relay.example.com/v1 key=sk-… models=gpt-5.5,claude-sonnet-5
magpie providers                        # host, key, exposed models, who uses what
magpie provider deepseek                # one provider in detail
magpie provider models deepseek         # re-fetch the vendor's list (add ids to choose which to expose)
magpie provider models deepseek +deepseek-v4 -deepseek-chat   # expose one more, take one out; the rest stay
magpie provider models deepseek a b c   # the whole list of models to expose, replacing it (all: the default)
magpie provider refresh deepseek        # re-fetch it, and drop picks it no longer has (the TUI: m)
magpie provider refresh-every relay 6h  # the gateway fetches its list every 6h (1d, 30m; off; at least 10m)
magpie provider auto-include relay 'gpt-*' 're:claude-.*'  # a newly listed model these match is exposed (none clears)
magpie provider test deepseek           # one tiny request per API, with latency
magpie provider key deepseek sk-…       # replace the key
magpie provider rm deepseek
magpie models                           # the catalog agents see
magpie claude deepseek/deepseek-chat    # use it
```

Custom providers take `url=` (an OpenAI-compatible base), `anthropic=` (an
Anthropic-compatible base), or both, plus `responses=` when the vendor has a
separate Responses endpoint, `catalog=` to borrow a models.dev list, and
`models=` to name the models to expose. Anything a preset does not know can
be overridden the same way.

SiliconFlow defaults to China (`api.siliconflow.cn`). For an international
account, use `magpie provider add siliconflow sk-… region=intl`, or an import
link with `preset=siliconflow&region=intl`. The editor's Region control chooses
China or Global and uses that site's key page. The sites have independent
accounts, so use a key from the selected site. Add another SiliconFlow provider
to keep both accounts; each provider has its own endpoint and model list.
The existing balance lookup uses the old `/user/info` API, which returns
HTTP 410 for Global. This does not prevent model discovery or chat; check
your balance in the selected site's console. See
[Providers and accounts](subsystems/providers-accounts.md#constraints-and-failure-behavior)
for the balance limitation.

A model whose vendor answers that it is deprecated or retired (OpenCode
Zen's 410 "Model exo-free has been deprecated.") leaves that provider's
models in `magpie models`, `/v1/models` and every agent's list, even while
the vendor's own list still names it. It comes back once a request for it
as `provider/model` is answered, or after a week.

`magpie provider set <id> header.<Name>=<value>` sends a header of your own
on every request to a key+URL provider (an empty value removes it; signed-in
accounts ignore them). It replaces a header of the same name magpie would
send, except `anthropic-beta`, which is a list: your betas are added after
the ones the agent asked for that request (Claude Code's, its 1M context's
`context-1m-2025-08-07` for a `[1m]` model, fast mode's), each once. A beta
the provider turns away (`Unexpected value(s) … for the anthropic-beta
header`) is dropped from the retry and from then on, yours as well as the
agent's; any other of yours is always sent. The beta of the agent's own
sign-in (`oauth-2025-04-20`, which Claude Code signed in to claude.ai asks)
never goes, as the sign-in never does; one you set yourself does.

Requests go to a vendor with magpie's own User-Agent (`magpie/<version>`).
A coding plan that serves only the agents it names knows them by theirs:
`magpie provider set <id> user-agent=agent` (the editor's "Send the
agent's own User-Agent") sends a key+URL provider the User-Agent the agent
sent magpie, as it sent it, on every API (chat, Anthropic, Responses,
Gemini, count_tokens, embeddings, images, videos). A request the agent
sent without one, and magpie's own (its classifier's, the image tool's),
still go as magpie. A `header.User-Agent` you set wins over both.
`user-agent=magpie` turns it off again; off is the default. A signed-in
account, a plugin's and a vendor whose own client magpie goes as
(OpenCode, Cline, Kilo, another magpie) keep the User-Agent their sign-in
sends, and don't take it.

`magpie usage` (and `--csv`) takes `today`, `7d`, `30d` or `all`, each from a
local midnight, or a window ending now: `--last 24h` (any Go duration, `90m`)
or `--days 1.5`; the CSV then holds that window's calls only (#2075).

`magpie usage` also lists **upstream provider keys** to help check upstream bills.
Each request records the fingerprint and saved name of the key that actually
served it, including image calls and account/key failover. The CSV adds
`provider_key_id` and `provider_key_name`. JSON uses `providerKeyId` and `providerKeyName`, distinct from gateway caller
keys. System One calls use the same attribution. No raw credential is stored
in usage records.
Rotating the provider's first key does not move old usage to its replacement;
deleted keys keep their historical identity. Older records appear as
**key not recorded**, never inferred from today's configured key.
These are upstream credentials, not keys clients use to call Magpie.

The CSV's `cache_write_tokens` is still every cache write; the two
columns, `cache_write_5m_tokens` and `cache_write_1h_tokens`, split it by how
long Anthropic keeps it (a record from before the split is all 5-minute).
The app shows the split on a request's Cache and the usage tiles' tooltips.

**Requests and failures** count requests, each once by how it ended: a
request failed when the agent got an error. One that a later try answered
(the next member of a group, another account) is not a failure; the Usage
page, the Requests card, its rankings and group rows, and `magpie usage`
count it as answered on a retry (`retried` in the JSON). On the Requests
card each count is a button for its requests in the list below (the
Failed and Retried filters, `failed=1` and `retried=1`). A try a vendor
refused or turned away keeps its own row, for what it billed and why, marked
handed on, and isn't counted as a request. The CSV's last columns are
`tries` (on the final row, when the request took more than one),
`handed_on`, then `tier_asked`, `tier_sent` and `tier_served`: the
service tier the client asked for (Claude's fast speed reads `fast`), the
one that try went out at (a fast pick sends `priority`, or `ultrafast`
where the pick is set to Ultrafast; Ultrafast becomes
`priority` on an account whose plan doesn't offer it), and the one the
vendor's reply said it ran at, empty when the reply doesn't say. The JSON
log keeps them as `tier_asked`, `tier_sent` and `tier_served`, and each
try in `/v1/magpie/route` has `tier` and `tier_served`. The last column,
`upstream_stop`, is why the upstream said its reply ended, in its own
words (`stop`, `tool_calls`, `end_turn`, a Responses reply's
`completed`), empty when it said none. A request's row on the Requests
page shows it as Upstream stop reason.

**Output speed** (tok/s on the Usage and Routing pages, `magpie usage`) is
output tokens over the time after the first token, summed per request: the
tokens of every timed request over the sum of their windows, so concurrent
subagents never add up. A reply that reasoned counts only its answer, from its
first text (`usage.DecodeOf`): hidden reasoning is written before the stream
shows anything, so counting it in a window that starts after it read
gpt-6.1-sol at hundreds of tok/s. A reply that reasoned and then wrote only
tool calls, one not streamed, and a burst (under 100 ms, or over 10,000 tok/s)
tell no speed. So does a reply its vendor held back and let go in a burst at
its end, however long the burst took to come: one whose content came in under
a quarter of its window (`flow_ms`, from a tenth of its bytes to nine tenths,
scaled to the whole), or that reasoned and whose answer would have come over
20 times faster than its reasoning came before its first text (its text came
with the burst). The time a burst takes to come is not the time it took to
write, and that time isn't seen. History is read the same way, as it keeps the
reasoning, the first text and the flow; a record from before the flow was kept
is told by its reasoning's pace alone.

A call read from an agent's session file rather than seen by the gateway has
no first token: Claude Code's file has only when the line that asked for a
reply and the reply's last line were written. Such a row on the Requests tab
shows the least its reply wrote at instead (`≥98 tok/s`): its output tokens
over that whole time, the wait for the first token in it, so the speed after
the first token is never under it. Only a reply of 1,000 tokens or more
written over a second or more shows one; a shorter reply's wait is too much
of its time to tell anything; a count of tokens bounds that share, a
duration doesn't (a 5 s reply can be 4 s of wait). Claude Code's, Claude
Desktop's and Codex's rows show one: Claude's line that asked for a reply is
written before it is sent, and a Codex call in a turn of several is timed
from what the model had last been given before the call's first output
item to its last, never from the output of a tool the call itself ran,
which an older rollout writes before the call's token count. OpenCode's rows
show none.
A call whose session file timed it (a Codex turn of one call) shows its own
speed when the time after its first token is a second or more and it wrote
100 tokens or more in it (`usage.CodexWindowMs`, `usage.CodexBodyTokens`).
That time can be one network push rather than the writing: a reply whose
first token came at 6.8 s and the rest 0.2 s later read 1,900 tok/s. When
that call reasoned, the file has its first token but not its first text, so
no speed is measured; it shows the least its answer wrote at instead: its
output less the reasoning, all written after the first token, over the time
from the first token to its end, held to the same second and 100 tokens. Its
whole output over that time would count reasoning written before the first
token and read high. A Codex turn of one call that tells neither shows the
least it wrote at over the whole turn, the wait for the first token in it
(`≥120 tok/s` for 1,200 tokens in 10 s), by the same rule as any floor over
a whole time: 1,000 tokens or more over a second or more. Under that the
wait is too much of the time to tell anything, so an 11-token answer in
2.75 s shows none, as an 815-token Claude Code reply does. Gateway rows and other agents' keep their own
speed. A floor never mixes into a measured speed: the Speed trend and
`magpie usage` count timed calls only.

A group's speed (a row of the Usage page's ranking, by model, provider or
agent, its Output speed card, and the overview's lists) says how many
replies it is of, by one rule: with replies whose speed was measured, their
speed and how many they are (`62 tok/s · 2 timed replies`, not beside "260
requests" alone); with rows that tell a floor, the least those wrote at and
how many (`≥100 tok/s · 30 replies`); with both, both, the one of more
replies first (`≥50 tok/s · 259 replies; 125 tok/s · 1 timed reply`), as a
few measured replies may not stand for the rest; with neither, nothing.
Counts are words, never a bare number in parentheses. The Output speed card
shows the first as its figure, its count beside it, the other under it, and
the mean wait for the first token on a line of its own with how many timed
replies it is of. The tooltip says what each is of. A ranking by speed
orders and bars a group by the figure it shows first (`usage.Totals`
`decode_n`, `floor_out`, `floor_ms`, `floor_n`; `usage.Row.Floor`).

It lists **accounts** too: each Codex, Claude or other subscription account's
tokens and cost, by the account that actually answered — the one that took
over after a failover, the one `X-Magpie-Account` pinned. The account is named
as the Routing view names it (its email or login, never a token); the CSV
adds `provider_account` (JSON `providerAccount`), `magpie usage --csv
--account <name>` keeps one account's calls, and the app's Usage page has an
Accounts list and an Account filter on Requests. An older record names the
account its call went out as when its host says so (`chatgpt.com as
dee@example.com`); otherwise it appears as **account not recorded**, never
inferred from today's sign-in. OTLP export never carries the account.

One magpie can serve several computers (an office one, a personal one):
share it on the network (Settings → Share on local network), and on each
other computer add it as a **Remote magpie** — in the app's Add sheet, under
its own heading, Another computer's magpie, after On this machine; on the
TUI's Providers page (`a`, then its address and key; `w` changes the address,
`m` fetches its list again), or
`magpie provider add remote-magpie sk-magpie-… url=http://192.168.1.20:3425 id=office`.
Each computer's own magpie still wires its agents, while the providers,
routing groups (`office/group/…`) and usage are the shared one's. A request
goes on in the API the agent spoke — Anthropic Messages, Responses, Chat
Completions, token counting — and a model the shared magpie's provider serves
on another API only is turned into that API once, never on both computers.
Its list is the models the shared magpie's agents are shown, each named with
its provider there (`Claude Sonnet 5 · Relay A · office`) — the ids stay ids
(`office/relay-a/claude-sonnet-5`), only these labels carry the names.
Its image and video models are discovered automatically and served through
its image generation/edit and video creation/status/content APIs. System One
is also configured automatically at its `/v1/systemone`: refresh the remote
provider's model list to discover its decision models, including Jev, Clef
and models with custom names. These appear as decision models rather than
chat models. A bare provider id such as `office` asks the first listed
decision model; it does not pick another model to satisfy a key's limits.
If the key permits only another model, use its full `office/provider/model`
id to avoid a 403. Embeddings, rerank and speech-to-text requests use the remote's corresponding
APIs, and its exposed retrieval models are kept when fetching its list.
These remain in agent model lists as on the remote; being listed does not
mean an embedding or rerank model can hold a conversation.
The remote must serve the requested API and model, and allow the supplied
key to use them. Responses uses HTTP, including SSE, rather than WebSocket.
An older remote without decision-model discovery still serves its existing
APIs; update it and refresh the list to expose System One models here.
Its quotas show too: the Usage page, the menu bar and `magpie quota` list the
shared magpie's cards named with it (`Codex · office`, id `office/codex`), as
that magpie last read them — only the computer holding the sign-ins asks the
vendors, and it answers from what it has kept (`GET /v1/magpie/quotas/cards`).
A card's refresh here has it read that card once more
(`POST /v1/magpie/quotas/refresh`, at most once in 30 seconds a card); until it
has read anything, the remote's one card says so. Their history is shown with
its own. A Codex reset or a check-in is pressed on that computer, not here.

Codex's native image tool first asks the provider that served its conversation
turn for the requested image model. If that provider does not list the model,
or the turn cannot be identified, it uses Settings → Images → Image generation
(including Automatic). Off disables this fallback, not a matching model on the
turn's provider. An explicit `provider/model` keeps its destination; an upstream
image error does not retry through the setting. Magpie Image MCP is not required.

WorkBuddy (China)'s daily check-in (签到) is pressed from the app's Usage card
(*Check in now*), `magpie accounts checkin`, or `c` on the TUI's Usage page,
which checks in every account signed in on this computer at once; its
WorkBuddy lines say how today's went. A shared magpie's accounts are checked
in on that magpie, from its own app, TUI or CLI.

A vendor with two or more accounts lists each under its Daily check-in in
**Settings → Usage**, with its own Off/On (#1902). An account turned off is
left out of the daily check-in and of *Check in now*, the CLI's and the
TUI's too, while the vendor's other accounts are checked in; its Usage card
says its auto check-in is off. The choice is kept in `checkinOff` in
`settings.json`, by the account's entry in the vendor's check-in file
(`workbuddy|<uid>`), and set with `POST /api/settings/checkin-account
{key, on}`.

A plugin whose `auth` hook has `checkin` checks its own accounts in the same
way: once a day while its switch under **Settings → Usage → Plugins** is on,
with the result on each account's Usage card. A check-in the vendor wants a
captcha for is shown as such (check in in the vendor's app); magpie never
solves captchas. See [provider-plugins.md](subsystems/provider-plugins.md#a-plugins-daily-check-in).

A plugin can also declare its own switches for its provider (`auth.options`).
They are listed under **Plugin options** in the provider's editor, an Off/On
each with the plugin's own words, and saved with the provider. The values are
kept in `pluginOptions` in `settings.json`, and the plugin reads them as it
works. Changing one starts nothing by itself. See
[provider-plugins.md](subsystems/provider-plugins.md#a-plugins-own-options).

The gateway issues **gateway keys** for clients, separate from a provider's
upstream API keys. Turn on **Settings → Share on local network**, then open
**Gateway → Gateway keys → Add gateway key**. This block appears only while
sharing is on. Create a named key for each client and copy it from its row.
To move from another gateway without touching its clients, enter the key they
already send in **Your own key (optional)** (8–256 characters, no spaces, not
starting with `sk-magpie-`, unlike any other key); left empty, magpie makes
one. The list shows only its last four characters, and it is counted,
limited and restricted as any other key, from this computer too.
Rename, disable, rotate or remove keys independently; rotation and removal
ask for confirmation. Rotation keeps the name, enabled state and usage
history; other keys are unchanged. While sharing is on, **Gateway → Connect**
offers loopback and shared addresses, plus enabled gateway keys, for all
connection examples. With sharing off, Connect keeps the original **API key**
field and local `magpie` token, without a gateway-key picker. Its arbitrary
local option is **This computer**, distinct from the named **Magpie** key.

For a headless gateway, use the CLI before exposing the port:

```sh
magpie gateway-key add "Remote laptop" # prints the new credential once
magpie gateway-key add Family --key -  # keeps a key clients already send, read from stdin
magpie gateway-key list                # ids, names, enabled state and masked keys
magpie gateway-key rotate <id>         # prints the replacement; identity stays the same
magpie gateway-key remove <id>         # revokes remote access
magpie gateway-key limit <id> week --tokens 2m --cost 5   # its own limit
magpie gateway-key limit <id> list     # each limit, used, left and reset
magpie gateway-key limit <id> 10d --tokens 5m   # every 10 days, from now
magpie gateway-key limit <id> 5h --tokens 200k  # every 5 hours, from now
magpie gateway-key limit <id> add week --cost 20   # one more limit; all must pass
magpie gateway-key limit <id> remove 2 # take off the 2nd limit
magpie gateway-key limit <id> reset    # count its usage from 0 again (reset 2: only the 2nd)
magpie gateway-key limit <id> off      # no limit
magpie gateway-key models <id> openai/gpt-5 anthropic/*   # only these models
magpie gateway-key models <id> all     # every model
magpie gateway-key accounts <id> codex/me@example.com   # only these accounts
magpie gateway-key accounts <id> all    # every account
magpie gateway-key quotas <id> on      # told the quotas of what it may use
```

Each gateway key can have its own **limit**: a token total, an estimated
cost in US$, or both, per day, week or month (calendar windows in local
time: from midnight, from Monday, from the 1st), every N days (#1509;
N from 1 to 3650) or every N hours (#2102; N from 1 to 8760, such as 5 for
a subscription's 5-hour pace). An N-day or N-hour cycle starts when the
limit is set and runs N×24 or N hours, then the next one starts where it
ended, so a restart, a time-zone change or a daylight-saving shift doesn't
move it. Changing only
the caps or **Count cache reads too** keeps the cycle; changing N or the
period, or turning the limit on, starts a new one then. Set it with **Limit** on
the key's row (saved with Save) or `magpie gateway-key limit`; the row shows
what the key has used, what is left and when it resets. A key can have up to
five limits, each in a window of its own (#2227), such as $3 every 5 hours and
$20 a week, as a subscription's two windows: **Add a limit** in the editor or
`magpie gateway-key limit <id> add`. A request passes only while every one has
something left; its reservation is held in each window, and its 429 names the
spent limit that resets last. Two limits can't share a window: a window's
tokens and cost caps go in one limit. Setting a window without `add` changes
the key's limit of that window, or its only one. `GET /v1/magpie/limit` gives
each in `limits`, and in `limit` the one holding the key most, which a magpie
from before #2227 reads; such a magpie also keeps only the first limit
(`limit` in caller-keys.json, the rest under `moreLimits`). Tokens counted are a
call's uncached input, output and cache writes, plus cache reads when **Count
cache reads too** is on. Cost is an estimate at the Usage page's prices; a
call with no known price adds none. A call counts in the window it started
in. Once a key is spent, its requests are refused before any provider is
asked, with a 429 in the API's own error shape that names the key, the
limit and the reset time, plus `Retry-After`; other keys are unaffected. The
counts are read from the usage log, so they survive a restart. A request in
flight holds a reservation (its body's size in tokens plus the key's mean
output per call), so requests sent at once overshoot by about one call; a
streamed reply is settled when it ends with the usage its vendor reported.
**Reset usage** in the key's limit editor (or `magpie gateway-key limit
<id> reset`) counts the key's tokens and cost from 0 together, from now: the
editor's resets the limit it is open on, the CLI's every limit, or the nth
with `reset <n>`. An
N-day or N-hour key starts a new cycle then. A day, week or month key keeps
its window's end (midnight, Monday, the 1st): only the calls before the
reset stop counting, so it doesn't drift off the calendar. The reset is
stored with the key (`since` in caller-keys.json, magpie's own: a value a
client sends is ignored), so it survives a restart; the usage log itself is
untouched. Keys saved before keep their JSON as it was.
A key can read its own status with `GET /v1/magpie/limit`; it is told its
own and no other key's, whatever the request names. A magpie that has this
one as a Remote magpie provider shows each of its keys' limits on its
Usage page this way (#1547). Requests from
this computer that send no gateway key are not limited; a gateway key used
from this computer is.

A gateway key can also be held to some **models** (#882): pick them with the
**All models** badge on the key's row, or run `magpie gateway-key models <id>
openai/gpt-5 anthropic/*` (`all` takes the restriction off). A pattern is
`<provider>/<model>` or `<provider>/*`, matched against the provider that
serves the call, so a bare model name is resolved first, or a routing group,
`group/<id>` (`group/*` for every group). A key that names a group may use
the group with every member in it, though not those members asked for by
name. Such a key sees only its models in `/v1/models`, `/v1/codex/models`
and the Anthropic and Gemini lists, a routing group it doesn't name only when it may use every
member, and is refused any other model with a 403 in the API's error shape
before a provider is asked; a fallback it may not use is skipped. A key with
no models listed may use every model.

A gateway key can also be held to some **accounts** (#905): the accounts
and keys its requests may use, picked in the same menu right after their
provider's models — an account by who is signed in, a key by its
fingerprint — or with
`magpie gateway-key accounts <id> codex/me@example.com openai/<key id>`
(`all` takes the restriction off). The list holds a key to some accounts
**of the providers it names**: a provider it names no account of, the key
uses as it always did. The list keeps an account by its stable id, kept
through renames, shown by who is signed in; one gone later — a key
rotated, an account signed out, an account of an agent that keeps no
logins renamed — is kept as it is, matching nothing, so the key is held
closer, never wider. A routing group the key names is its members'
through the accounts it may use only: naming a group is not naming its
accounts, and magpie's own calls for the key — a web search, a picture
described, a Codex title — are of the models the user picked, but their
spend lands on an account, so the accounts hold them too. When every
account or key behind a model is one the key may not use, the request is
refused with a 403 in the API's own error shape naming the model and the
accounts it may use, before a provider is asked — after a usage cap's 429
and before an account barred for the model's — and `/v1/models` lists the
key only the models an account or key it may use serves, the Routing
view's left-out list marking what the key held out. A key with no
accounts listed may use every account, as keys always did.

While LAN sharing is enabled, or `MAGPIE_ADDR` puts the gateway on the
network (a host other than loopback or `localhost`: `0.0.0.0` in the Docker
image, a server's address), remote requests require an enabled gateway key
sent as Bearer, `x-api-key`, `x-goog-api-key` or `?key=`; one without gets
401. A request is remote when its peer isn't loopback, which includes
Docker's port publishing (the peer is the bridge, not the host's loopback)
and a WSL distro under NAT. Loopback remains permissive: any token works,
including a stale or disabled gateway key. Only a valid, enabled key is
attributed to its named identity. Neither shared nor on the network, a
remote request is refused (403). A `MAGPIE_ADDR` pinned to one interface's
address asks a key even of this computer's agents calling that address;
`0.0.0.0` with agents on `127.0.0.1` doesn't. The Claude subscription's
own tool calls back to magpie are let in there by their run's one-time
token, without a key (#1546).
Sharing listens on every interface, unless `MAGPIE_ADDR` names a host of its
own: `MAGPIE_ADDR=127.0.0.1:3425` behind Tailscale Serve, or one interface's
address, stays where it is while shared, and what reaches it from elsewhere
still needs an enabled gateway key (#1112).

**Codex on another computer** (#1281) can name the shared magpie its model
provider, with a gateway key, and read magpie's models in Codex's own
catalog shape from `GET /v1/codex/models`. `/v1/models` is OpenAI's list,
which Codex can't read, and `/backend-api/codex/models` is the ChatGPT
backend's, for a Codex signed in to ChatGPT on the gateway's own computer;
a gateway key gets a 401 there.

```toml
model_provider = "magpie"
model = "provider/model"

[model_providers.magpie]
name = "magpie"
base_url = "http://192.168.1.20:3425/v1"
model_catalog_url = "http://192.168.1.20:3425/v1/codex/models"
env_key = "MAGPIE_API_KEY"   # the gateway key
wire_api = "responses"
supports_websockets = false
```

The list is the models the shared magpie shows Codex (its Agents page
picks), each as the `model_catalog_json` magpie writes for a Codex on its
own computer describes it, and only the models the key may use. Codex 0.161
reads it by default; 0.160 needs `[features] api_key_model_discovery = true`.
Codex reads at most 1 MiB of a catalog, and every entry carries Codex's
prompt, so past about 125 models the list keeps the first that fit and the
answer's `X-Magpie-Left-Out` header says how many it left out: pick fewer
for Codex, or hold the key to fewer.

A request that reaches loopback through a proxy or tunnel on this computer
(Cloudflare Tunnel's `cloudflared`, ngrok, Tailscale serve or funnel, frp's
HTTP proxy, Caddy, nginx) is someone else's, and is answered as one from
another machine (#1022): it carries a forwarding header (`Forwarded`,
`X-Forwarded-For`, `X-Real-IP`, `Cf-Connecting-IP`, `True-Client-IP`,
`Tailscale-User-Login`), which no agent sends. Without sharing it is refused;
with sharing, or a `MAGPIE_ADDR` on the network, it needs an enabled gateway
key, as from the network. This
covers every route: the model APIs, quotas, MCP sign-ins. To serve magpie
through a Cloudflare Tunnel, turn on **Settings → Share on local network**,
add a gateway key for each client, point the tunnel's service at
`http://127.0.0.1:3425`, and give clients the tunnel's address with that key.
A tunnel that forwards bare TCP (frp's `tcp`, `ssh -R`, socat) adds no header
and can't be told from an agent here: point it at this computer's LAN
address with sharing on, not at loopback. A proxy here that signs its
clients in itself can be trusted with `MAGPIE_TRUST_PROXY=1`, which keeps
what it forwards local, as before.

To let other computers in only from some addresses, list them as IPs and
CIDR ranges in **Settings → Network and sharing → Allowed addresses**, or in
`MAGPIE_ALLOW_IPS` (`203.0.113.7,10.0.0.0/8,2001:db8::/32`), which comes
first. A request from any other address is refused 403, even with an
enabled gateway key, and the refusal names the address it came from. This
computer is always let in. The address is the connection's own, never a
forwarding header, so behind a proxy or tunnel every client has the proxy's
address: limit them at the proxy. A mistyped entry in `MAGPIE_ALLOW_IPS`
lets nothing in rather than everything. When settings.json can't be read, or
doesn't parse and has no good backup, the list read last stays in force; if
none was read since magpie started, only this computer is let in until the
file is repaired. `magpie web`'s page honours the same list.

**HTTPS.** A magpie reached over the internet (a server used as a relay)
can serve HTTPS itself, with no proxy in front:

```sh
magpie serve --tls-cert /etc/letsencrypt/live/relay.example.com/fullchain.pem \
             --tls-key  /etc/letsencrypt/live/relay.example.com/privkey.pem
```

`MAGPIE_TLS_CERT` and `MAGPIE_TLS_KEY` do the same (for Docker), and `magpie
web` and `magpie daemon install` take the same flags; the service keeps the
pair when it is installed again without them, and `--no-tls` takes it away.
The files are PEM: the certificate with its chain after it, and its private
key. One without the other, or files that don't load, refuse to start
rather than serve plain HTTP. The certificate is read again when either
file changes, so a renewal (certbot, acme.sh) is served on the next
connection without a restart; files that don't load then (a renewal half
written) leave the one before in place, and the log says so. Other
computers reach the gateway, and `magpie web`'s page, over `https://` only,
and the network links magpie prints say `https://`; plain HTTP from them is
answered 400. This computer may still call `http://127.0.0.1` on the same
port, as the agents, MCP servers and status checks magpie wires here do,
since no certificate for the server's name covers loopback. A container or
a WSL distro calling through its NAT address is another computer, and uses
`https://`. Gateway keys, sharing and allowed addresses work as over HTTP.

Caddy is the alternative that also gets the certificate for you: run
`relay.example.com { reverse_proxy 127.0.0.1:3425 }` with sharing on and a
gateway key per client. A proxy on this computer adds forwarding headers,
so its requests count as another computer's, as above.

**Usage → Overview → Gateway keys** groups calls by the client's key, never
the provider's credential. **Usage → Requests** offers the same filter;
CSV includes `caller_key_id` and `caller_key_name`. Deleted keys keep their
history. The usual local `magpie` token and older records stay unattributed.
Caller attribution includes chat, images, video creation and System One.
Provider attribution stays independent; CSV puts `provider_key_*` before
`caller_key_*`.
An existing LAN key becomes **Magpie** without changing the credential.
`lanKey` remains in settings for older Magpie versions. Disabling or removing
the default key replaces that mirror with a random non-empty revoked value;
rotation does not re-enable it. Turning sharing off and on again neither
re-enables a disabled default key nor makes a removed one again; only
asking for a new sharing key makes one. The key store records migration completion,
even if `lanKeyId` cannot be saved, so reads do not keep retrying that write.
A migration write failure is logged without preventing gateway startup,
CLI key management, or the Settings and key-list pages from opening.
If settings are read-only, changing the default key fails without changing
it: make `settings.json` writable and retry so older versions cannot keep
accepting its old credential. Independent named keys remain manageable.
Gateway credentials stay in `~/.config/magpie/caller-keys.json` (XDG-aware,
mode `0600`), never in usage records or list responses.

Baidu Qianfan's [Token Plans](https://cloud.baidu.com/doc/qianfan/s/Dmrabu8b6)
are available as `baidu-qianfan`: a personal (个人版) and an enterprise (企业版)
plan and pay as you go, each with its own Chat Completions, Responses and
Anthropic Messages endpoints, and a key that works only on its own plan. Add
it with `magpie provider add baidu-qianfan <api-key>` — the id it carried its
first day, `qianfan-token-plan`, is taken too. The plans serve no model list,
so the preset carries their documented models; pay as you go serves its own
at `/v2/models`.

Alibaba's Token Plan is sold in two places, each with its own `sk-sp-` key
that only its own host takes: on the Qwen AI platform as `qwen-token-plan`
(`token-plan.maas.qianwenaiapi.com`), and on Alibaba Cloud Bailian as
`bailian-token-plan` (`token-plan.cn-beijing.maas.aliyuncs.com`). Both serve
Chat Completions at `/compatible-mode/v1` and Anthropic Messages at
`/apps/anthropic`, and carry the plans' documented text models for when the
host lists none. Bailian's also answers its decision model,
`decision-model-preview`, on System One at `/compatible-mode/v1/systemone`
with the same key, so a routing group can pick it as its classifier. The
same model on the Qwen AI platform's pay as you go is `qwen-decision`
(`maas.qianwenaiapi.com`), and on a Bailian workspace `bailian-decision`.
Neither plan has an API for its credits: the console's subscription page
shows them, so magpie shows no plan usage for these keys.

### Google Vertex AI

Google Vertex AI (`google-vertex`), which Google's documentation now calls
Gemini Enterprise Agent Platform, serves Google's Gemini models from your own
Google Cloud project, which is billed for them. It takes no API key: each
request is signed with a token minted from your Google credentials. First turn
on the Agent Platform API (`aiplatform.googleapis.com`) in the project (billing
has to be on there), then sign in with `gcloud auth application-default login`
or have a service account's key file
([Google's guide](https://docs.cloud.google.com/gemini-enterprise-agent-platform/models/start/gcp-auth)).
[Where magpie looks for the credentials, and the kinds it reads](subsystems/providers-accounts.md#responsibilities-and-sources-of-truth).

```sh
magpie provider add google-vertex project=my-project
magpie provider set google-vertex location=us-central1        # global (the default), us, eu or a region
magpie provider set google-vertex credentials=~/keys/vertex.json
magpie provider set google-vertex impersonate=vertex@my-project.iam.gserviceaccount.com
magpie provider set google-vertex impersonate=                # empty clears it
magpie provider test google-vertex
```

`project=` is the project's id. `location=` is `global` (the default, and what
an empty value goes back to), `us`, `eu` or a region such as `us-central1`.
`credentials=` names a credentials file to sign with in place of gcloud's
sign-in, such as a service account's key; a relative path is made full from
the folder the command runs in. `impersonate=` has requests made as that
service account; the account signed in needs the Service Account Token Creator
role on it. The app's editor asks the same in **Project ID**,
**Location**, **Credentials file** and **Service account**; the TUI's `a` asks
for the project alone, and the CLI sets the rest. A key is refused, and so is
an address (`url=` and the like): the project and location make the only one
its token is sent to. Adding, listing and showing it ask Google nothing; the
credentials are read when a request is signed.

Each location serves models of its own, and Vertex AI has no list to ask which,
so magpie gives each location the models it answered with on 2026-10-06
(and `gemini-4-argon`, at `global` since 2026-10-13):

| Location | Models |
| --- | --- |
| `global` | `gemini-3.8-flash`, `gemini-3.7-flash`, `gemini-3.6-flash`, `gemini-3.5-flash`, `gemini-3.5-flash-lite`, `gemini-4-argon`, `gemini-3.1-pro-preview`, `gemini-3.1-pro-preview-customtools`, `gemini-3.1-flash-lite`, `gemini-3-flash-preview`, `gemini-2.5-pro`, `gemini-2.5-flash`, `gemini-2.5-flash-lite` |
| `us`, `eu` | `gemini-3.8-flash`, `gemini-3.7-flash`, `gemini-3.6-flash`, `gemini-3.5-flash`, `gemini-3.5-flash-lite`, `gemini-3.1-flash-lite` |
| A region | `gemini-2.5-pro`, `gemini-2.5-flash`, `gemini-2.5-flash-lite` |

A region that serves one more, as asia-northeast1 serves `gemini-3.5-flash`,
has it typed in with the rest you want:

```sh
magpie provider models google-vertex gemini-2.5-pro gemini-2.5-flash gemini-2.5-flash-lite gemini-3.5-flash
magpie provider models google-vertex all     # back to the location's list
```

`magpie provider set google-vertex location=…` keeps the models picked, so pick
again after a move.

Google's [Priority PayGo](https://docs.cloud.google.com/gemini-enterprise-agent-platform/models/priority-paygo)
and [Flex PayGo](https://docs.cloud.google.com/gemini-enterprise-agent-platform/models/flex-paygo)
are asked for with request headers. The editor's **Headers** offers both,
`X-Vertex-AI-LLM-Request-Type` and `X-Vertex-AI-LLM-Shared-Request-Type`, to
add; their values are the ones Google's pages give. From the CLI:
`magpie provider set google-vertex header.<Name>=<value>`. [What was seen of
Priority PayGo](subsystems/providers-accounts.md#constraints-and-failure-behavior).

When a request fails:

- `no Google credentials at <file>`: there is no credentials file there. When
  it is the provider's own, correct `credentials=`, or clear it with
  `credentials=` alone; otherwise run `gcloud auth application-default login`,
  or set `credentials=`.
- `GOOGLE_APPLICATION_CREDENTIALS names <file>, which isn't there`, or
  `GOOGLE_APPLICATION_CREDENTIALS is "…", not a full path` (`CLOUDSDK_CONFIG`
  too): the variable names no file magpie can read, and gcloud's own isn't
  read in its place. Give the whole path (a `~/` at its start is the home),
  unset it, or set `credentials=`.
- `the Google credentials in <file>: the sign-in was refused`, with the advice
  to sign in again: the sign-in expired or was revoked. Run the command the
  advice names for that file: `gcloud auth application-default login` (with
  `--impersonate-service-account=` when the file impersonates one) for
  gcloud's Application Default Credentials, `gcloud auth login <account>`
  for a file under gcloud's `legacy_credentials`. A file of your own is
  written anew, or another one given with `credentials=`. The next request
  reads the new file, with no restart.
- A 403 `PERMISSION_DENIED` naming `aiplatform.endpoints.predict`: the account
  that signs, or the service account it goes as, may not use Vertex AI in the
  project. Give it Agent Platform User (`roles/aiplatform.user`) on the project
  ([the role](https://docs.cloud.google.com/gemini-enterprise-agent-platform/machine-learning/general/access-control#aiplatform.user)).
- `impersonating …: the sign-in was refused (403)`: the account signed in
  needs Service Account Token Creator (`roles/iam.serviceAccountTokenCreator`)
  on that service account.

### Plugins

A subscription magpie doesn't sign in to itself can come from an
[OpenCode](https://opencode.ai) provider plugin or a
[pi](https://github.com/earendil-works/pi) package: the npm packages OpenCode
users install to sign in to a plan (their `auth` hook), and the pi packages
that register a provider (`pi.registerProvider`), work in magpie as they do
there. magpie runs them on [Bun](https://bun.sh), downloaded the first
time a plugin needs it, and the plugin signs in, lists the models and makes
each request; magpie serves them to agents like any provider's.

```sh
magpie plugin add opencode-gemini-auth   # an npm package, or a path to a plugin of your own
magpie plugin add pi-antigravity         # a pi package, the same way
magpie plugin                           # the plugins, what each signs in to, and whether you are
magpie plugin login google-plugin       # its sign-in: the method, its questions, the browser or a key
magpie plugin logout google-plugin
magpie plugin off opencode-gemini-auth  # on brings it back; rm removes it; update updates them all
```

A provider id magpie already has (google, openai, anthropic) is
`<id>-plugin`. In the app, Settings → Plugins adds and removes them, and
the providers they sign in to are in Add provider → From plugins.

Sometimes two installed plugins sign in to the same provider id, for
example a third-party plugin and your own copy of it. One of them serves
that id: the one you picked, or else the one added last. Both plugins stay
listed, and each row in Plugins › Installed says which one serves the
provider. The other row has **Use for <provider>**, which switches the
provider to it. `magpie plugin` prints the same thing, and

```sh
magpie plugin use ~/dev/my-acme-auth acme   # this plugin serves acme from now on
```

makes the pick. Each plugin can still be switched off or removed on its
own.

A package is pi's when its `package.json` has a `pi` manifest, the
`pi-package` keyword, or depends on `@earendil-works/pi-coding-agent`.
magpie installs pi beside it and loads it with pi's own loader; the
providers it registers are what magpie uses, and its commands, tools and
renderers are left alone. Its sign-in runs as in pi: an OAuth login's
questions and pages, and the dialogs and pickers it shows (`ctx.ui`, its
terminal components too), are asked in magpie's sign-in; an API key works
as well. pi's own files (its `auth.json` and what a package keeps) are in
`pi/` under magpie's config directory, not your `~/.pi`, unless
`PI_CODING_AGENT_DIR` says otherwise. Requests reach the package as pi
sends them, and its replies keep their thinking and tool calls.

#### For plugin authors

A plugin is an OpenCode plugin or a pi package; nothing magpie-specific is
needed. For an OpenCode plugin magpie reads a few more fields, which
OpenCode ignores:

- **The provider's icon**: `icon` on the `auth` hook, or `"magpie": {
  "icon": "…" }` in the plugin's `package.json` (for every provider it
  signs in to that names none). An `https://` URL of a picture on a public
  host, which magpie fetches once and keeps, or a `data:image/…` URI;
  PNG, JPEG, GIF, WebP, ICO or SVG, at most 1 MB. Anything else is ignored,
  and the icon the plugin market lists for the plugin is shown instead.
  The `package.json` icon is also the card's picture under Discover →
  Unofficial, on GitHub for a repository tagged `magpie-plugin`.
- **An API key's field**: a `type: "api"` method's `label` titles the key's
  field, as OpenCode's dialog does (one that only says "API key" reads
  "<provider> API key"), and its `placeholder` is the hint inside the field
  (and after the question in `magpie plugin login`). The method's `prompts`
  are asked first, as in OpenCode, and reach `authorize(inputs)`. A prompt of
  `type: "password"` (magpie's own; OpenCode has `text` and `select`) is typed unseen:
  a masked field in the app, no echo in `magpie plugin login` (#1982).

```js
export const LemonPlugin = async () => ({
  auth: {
    provider: "lemon",
    icon: "https://lemon.example/icon.png", // or "data:image/svg+xml;base64,…"
    methods: [
      { type: "api", label: "Lemon API key (lemon.example/keys)", placeholder: "sk-lemon-…" },
    ],
  },
})
```

In TypeScript, `icon` and `placeholder` aren't in OpenCode's types: build
the hook as a variable (or cast it), or put the icon in `package.json`.

#### Gateway middleware

A plugin can also be gateway middleware: `onRequest(body, ctx)` sees each
request an agent sends before it is routed, `onEvent(event, ctx)` each
event of a streamed reply and `onResponse(body, ctx)` a whole one, all in
the agent's own API. A hook returns the changed object, or nothing to
leave it; `onEvent` returns `null` to drop an event, and
`ctx.reject(status, message)` turns a request away. Declare it with
`"magpie": { "middleware": "./mw.js" }` in `package.json`, or add a single
`*.middleware.js` file:

```js
// alias.middleware.js — magpie plugin add ./alias.middleware.js
export const events = ["message_start"]
export function onRequest(body, ctx) {
  ctx.state.asked = body.model
  if (body.model === "fast") body.model = "deepseek/deepseek-chat"
  return body
}
export function onEvent(ev, ctx) {
  ev.message.model = ctx.state.asked // the agent sees the name it asked for
  return ev
}
```

Middleware runs inside the gateway in [moejs](https://github.com/Calcium-Ion/moejs),
a JavaScript engine written in Go (about a microsecond a streamed event),
not on Bun: no timers, `fetch` or Node APIs, and imports only of files
beside it. A hook that throws or takes too long (250 ms a request, 50 ms an
event) leaves what it was given as it was. Settings → Plugins shows each
middleware's calls, time and failures. The [plugin guide](https://usemagpie.ai/docs/plugins#middleware)
has the rest.

Middleware runs through the built-in plugin `middleware` (Gateway
middleware in Plugins › Built-in), which stays on while one is on. Turned
off, no middleware runs and agents' requests go as they came. Privacy
masking is the built-in plugin `redact` (Privacy masking), on while
Settings → Privacy asks for any masking; if masking is asked for and it
isn't serving, a request gets a 503 and nothing is sent.

`ctx.options` is the middleware's entry's `options` in `plugins.json`: set
them with its **Options** button in Plugins › Installed, or

```sh
magpie plugin options model-map '{"mapping": {"fast": "deepseek/deepseek-chat"}}'
magpie plugin options model-map off   # clears them
```

The name can be a package's short name (`model-map` for
`@magpie-community/middleware-model-map`). A package's
`"magpie": { "options": {…} }` is the example the editor starts from.

Ready-made middleware, under Plugins › Discover › Gateway middleware
(`@magpie-community/middleware-<name>`), mostly New API's channel settings
with the same JSON: `param-override` (`param_override`), `model-map`
(`model_mapping`), `system-prompt`, `word-guard` (sensitive words) and
`think-tags` (strip `<think>…</think>`, or `thinking_to_content`). A
middleware's entry in the community `registry.json` has
`"kind": "middleware"` and no `providers`.

#### Agent plugins

A plugin can add an agent magpie has no setup of its own for. Its
`package.json` names the module (`"magpie": { "agent": "./agent.js" }`), or
it is a single `*.agent.js` file:

```js
export const agent = {
  id: "aider", name: "Aider", bin: "aider",
  config: "~/.aider.conf.yml",   // json, yaml or env
  model: "model",                // the key of the model
  prefix: "openai/",             // written before magpie's model id
  ua: ["aider", "litellm"],
}
export function connect({ gateway, model, models }) {
  return { "openai-api-base": gateway.v1, "openai-api-key": gateway.key }
}
```

It is then on the Agents page like magpie's own: picking one of magpie's
models writes the model and what `connect` returns, and its own model or
Disconnect puts every key back as it was. It runs in moejs like middleware.
An id magpie's own agents use is refused. In the market an agent's entry has
`"kind": "agent"` (`@magpie-community/agent-<name>`). The full reference is
on the site's Plugins page, under Agent plugins.

### What a model costs

A call is counted at its **effective price**: what you set for that provider
and model if you did, otherwise what the provider's own catalogue lists, and
otherwise what models.dev lists for the model's maker. A provider you added by
its address, with no catalogue named, has for its own the one models.dev
lists at that address (its `api`), when exactly one does and it isn't a
plan's: a provider at `https://developer.amd.com.cn/radeon/api/v1` is priced
from models.dev's `amd`, whatever you called it. Out of the box that
last one is the whole story, and it is the wrong number for any provider that
does not charge list price — a relay reselling at a discount or a multiplier
is counted at whatever the model's maker charges. Say what a provider
actually charges, and the usage ledger and the session totals use that:

```sh
magpie model price relay-a/gpt-5.5                        # what it is counted at, and where that came from
magpie model price relay-a/gpt-5.5 0.12,0.60,0.01,0.15   # input,output,cache read,cache write
magpie model price relay-a/gpt-5.5 --reset                # take your price off this model
magpie model prices                                       # every model you priced
```

The four numbers are USD per million tokens. All four are asked for, because
a price missing one would understate the rest of every call; `0` is a model
served at no cost, which is a price, not the absence of one. Decimals take a
point or, in the app's boxes, a comma (`0,25`).

A call to a model no price is known for is counted at the price of the model
the reply said answered, at the same provider, when that one has a price: a
relay that sells `kimi-k3` as `moonshot-kimi-k3` answers as `kimi-k3`, so its
calls are counted at `relay-a/kimi-k3`'s price. A model with a price of its
own, `0` included, keeps it whatever its reply says.

`magpie model price provider/model` says where a price came from, and for a
model with none, the two commands that give it one.

A call with no price at all is left out of a total's cost, never counted as
free: the Usage page's sum says how many calls it couldn't price beside it
("≈$1.20+", then "3 calls unpriced"; Sessions, which prices by model, says how
many models), and `magpie usage` and the terminal app's Usage page say the
same after their totals. magpie doesn't guess a variant's price from a
similar name (`-auto`, `-max`, `-fast` can each cost differently); give it
one with `magpie model price`, or in the provider's editor under the model.

A **Kimi Code** membership's models are counted at the Kimi API model each
one is, not at the $0 models.dev lists them at for the plan: `k3` and
`k3-256k` at `kimi-k3`, `kimi-for-coding-highspeed` at
`kimi-k2.7-code-highspeed`. `kimi-for-coding` is K2.8 Preview, which the API
doesn't sell, so it has no price until you give it one
(`magpie model price kimi-code/kimi-for-coding …`).

A **fifth number** is a 1-hour cache write's price. Anthropic bills a cache
write kept for 5 minutes at 1.25× input and one kept for an hour at 2× input,
and its usage says which were which (`cache_creation.ephemeral_5m_input_tokens`
/ `ephemeral_1h_input_tokens`, from the API and from Claude Code's
transcripts). The fourth number is the 5-minute price; the fifth, when not
given, is 2× input for a Claude model — Anthropic's rule. Any other model has
no 1-hour price unless you give one: a 1-hour write it reports is counted at
the 5-minute price, and neither `magpie model price` nor the app's boxes show
a 1-hour price for it. A call recorded
before magpie kept the split counts all its writes at the 5-minute price, as
it did.

A **long-context price** is what the whole request costs once its prompt is
over a size, as OpenAI bills gpt-6-astra, gpt-6.1-sol and gpt-6-luna over
272K input (2× input and cache, 1.5× output):

```sh
magpie model price openai/gpt-6-astra 10,50,1,12.5 --tier 272k 20,75,2,25
```

The size counts the prompt the way OpenAI does: uncached input + cache reads
+ cache writes, and the tier applies when that is **over** the size (272,000
is not, 272,001 is). Then every part of the call — input, output, cache — is
at the tier's price, not only the tokens past the size. `--tier` may be given
again for another size. The built-in prices carry a tier only where
models.dev lists one (`cost.tiers` of type `context`), as it does for those
three. Session and day totals sum many calls, so they are counted at the base
price; the ledger and each request are counted at the tier they reached. In
the app, a model's price row has a quiet **Long-context price** row under it,
shown with the list's tier greyed in, or behind a link where there is none.

The order a price is looked for in is: **the price for this model → the price
for `<provider id>/*`, which covers every model of that provider → what the
provider's own catalogue lists → what models.dev lists for the maker →
what models.dev lists for the maker of the model you said it is the same
as.** The last is for a provider's own id that no list prices, as
WorkBuddy's `kimi-k3-1` or a plugin's `qfmodel` (#2344): set **Same as**
on the model in the provider's model editor (`"modelSameAs":
{"workbuddy/kimi-k3-1": "kimi-k3"}` in settings.json) and its calls are
costed at Kimi K3's list price, at the provider's price rate, never at
another provider's tariff. A model with no Same as is never matched to
another by its name. `magpie model price <provider/model>` says when a
price came from it.
`--reset` removes the first, and says so when a `<provider id>/*` price is
still in force; reset that one by name to take it away too.

A price is **one provider's tariff for one model**, not the model's own: the
same model through two providers is two prices, and each keeps its own.
Nothing an agent can see changes. The model list, the agents' own settings
and the pickers that choose a model for a background task — an image, a web
search, a description — all still work from the catalogue; only the cost
reports read the effective price.

Two things worth knowing. The ledger and the session totals re-price when they
are read, so adding or changing a price restates earlier figures: they are
estimates at the effective price, not settled charges. And a price is per
provider and model. Records now identify upstream API keys, but a provider
charging different tariffs per key still cannot be costed exactly from a
single provider-wide price.

### What a model takes

A provider that serves a model models.dev does not list, or lists at the
wrong size, has a window and a reply limit magpie cannot know. Say what they
are:

```sh
magpie model context relay-a/gpt-5.5 262144    # the window a request may hold, or 1m
magpie model output  relay-a/gpt-5.5 131072    # the most a reply may hold, or 128k
magpie model context "relay-a/*" 200000        # every model of that provider
magpie model output  relay-a/gpt-5.5 --reset   # take your limit off this model
```

The `*` is quoted because zsh treats a name it cannot expand as a command
that failed, rather than passing the name on as bash does.

Both are looked for in this order: **this model → this provider's `*` → the
provider's own list → models.dev.** `--reset` removes only the value this
model has of its own.

A provider you keep unlisted, or switch off, takes them like any other: the
numbers are kept, and are what its models take once it is serving again.

A window is a number agents are shown **and a routing input**: at 95% of the
window a request held on a routing-group member moves to one that takes more,
so overstating a window makes that move happen too late. A reply limit is
advertised in `/models` and is what a group advertises the smallest of. The
gateway doesn't lower a limit an agent sends by it. A request that names none
and goes to Anthropic's Messages API, which needs one (Codex's Responses turns,
a Chat client's), is sent it as `max_tokens`, within the window as agents are
told it. Only a model whose limit nobody knows goes at 16384. A reply that
runs into it ends with `stop_reason: max_tokens`, which a Responses client
reads as `incomplete_details.reason: "max_output_tokens"` (#1792).

Saving a window or a reply limit writes that number into the model lists magpie
keeps in the agents' own files — Pi's `contextWindow` and `maxTokens`, OpenCode's
`limit`, and Crush's, droid's, Cline's, Qoder's and Zcode's — which an agent reads
at start-up. A session already running therefore keeps the window it began with,
while the gateway's own `/models` and every request from then on are right at
once.

### The name a vendor knows a model by

A relay often serves a model under an id of its own — a prefix it namespaces
with, a dated name, a `-latest` that is not what models.dev calls it. Say
which name to ask for:

```sh
magpie model wire relay-b/model-2                           # what the vendor is asked for
magpie model wire relay-b/model-2 vendor-c/model-2-preview  # ask for it by this
magpie model wire 'relay-b/*' 'vendor-c/*'                  # every model, * being the model
magpie model wire relay-b/model-2 --reset                   # ask for it by its own name again
magpie model wires                                          # every name your vendors are asked for models by
```

Quote the arguments with a `*` in them: zsh reads a bare `*` as a glob and
answers `no matches found`. In the name, `*` stands for the model itself, so
`'relay-b/*' 'vendor-c/*'` sends `vendor-c/model-3` for `model-3` and
`vendor-c/model-2` for `model-2` — one name for a relay that namespaces its
models, each still asked for by its own. A name with no `*` in it sends every
model of that key under that one name, which is the right answer only for a
relay that does serve them all alike. The model's own key wins over the
provider's, and `--reset` takes away only the one it is given, so resetting
`relay-b/model-2` while `'relay-b/*'` is set leaves the provider's name in
force — the CLI says which of the two is in force after every change, naming
the models a name for the whole provider leaves to their own, and
`--reset` over a name that was never given says so rather than ticking a
removal that took nothing away.

Only the request that goes out carries that name, in the model field of a
chat, Responses or Anthropic Messages request and of an Anthropic token
count; image requests are left with the name magpie knows the model by.
Gemini CLI and Antigravity sign-ins go on Code Assist, and there the model
an effort picks is a variant of the model magpie knows — `gemini-3.7-flash`
at `high` is sent as `gemini-3.7-flash-high` — so a `*` in the name is that
variant: `'antigravity/*' 'vendor-c/*'` asks for `vendor-c/gemini-3.7-flash-high`
and `vendor-c/gemini-3.7-flash-low` each by its own, as it does everywhere
else. A name with no `*` in it, or one given for `antigravity/gemini-3.7-flash`
itself, is that one name at every level.

Everything else keeps the name magpie knows the model by: the catalog agents
pick from, the routing groups' membership, `GET /models`, and what a call is
recorded and priced as. What the vendor's own reply said answered is kept
beside that, in the ledger's `served_model`, which is what it is for, and it
is compared with the name the vendor was asked for — so a model answered
under the relay's own id is not read as a swap. That is against the names
in force when the ledger is read, since a record keeps what the vendor
answered and not the name the request went out under: naming a model after
the call re-judges that call, which is then left reading as a swap. What a
provider *supports* — whether it takes a temperature, which reasoning levels
fit — is still asked about the model magpie knows, so a rename upstream does
not change how magpie behaves towards the model.

An upstream name is **one provider's**, not the model's: another provider
serving the same id is asked for it under its own name, or this one. The
model it is given for has to be one that provider serves — `<provider>/*`
is the way to say one for every model — because a name for a model magpie
would never ask the provider for is not a name of its own: the model is
asked for by the name magpie knows it by, and nothing anywhere would say the
name given for it is not the one in force. A relay that serves a model under
an id of its own *and* lists it does get the name; what is refused is a
model the provider's list does not have at all. The model test uses the name
too, so a relay that only knows its own ids does not report a working model
as broken. An image model is the exception that follows the rule above: it is
tested by the name magpie knows it by, on the images API and on the chat it
falls back to, because that is how a drawing is asked for.

### Routing groups

A routing group is several models, from one provider or many, that an agent
picks as one: `group/<id>`. The gateway routes each request over every
member's keys and accounts together. A model two of your providers serve
under the same name becomes a group on its own; the Routing view in the app
and `magpie group` make any other:

```sh
magpie groups                           # yours, then those magpie found
magpie group add "Opus anywhere" models=claude/claude-opus-5-5,copilot/claude-opus-5.5 routing=order stays=session
magpie group opus-anywhere              # one group, its models in order
magpie group set opus-anywhere models+=openrouter/anthropic/claude-opus-5.5 routing=usage
magpie group set opus-anywhere models-=copilot/claude-opus-5.5
magpie group set opus-anywhere routing=weight weight=claude/claude-opus-5-5=3,copilot/claude-opus-5.5=1
magpie group copy opus-anywhere "Opus test"  # the same models, routing and rules, to change on its own
magpie group rm opus-anywhere           # one magpie found is hidden; magpie group restore <id> brings it back
magpie claude group/opus-anywhere       # use it
```

`routing=` is `smart` (the default: of the subscriptions with quota to
spare, the one whose allowance renews soonest first), `order` (the first
model until it can't answer, then the next), `rotate` (each turn to the next
member), `usage` (least used first) or `pace` (weekly pace: the account with
the most of its week left per hour until it renews first, so less of a week
is lost at its reset) or `weight` (each model takes its share of the
requests by its weight, smoothly interleaved: `weight=claude/claude-opus-5-5=3`
gives it three requests for every one a model left at weight 1 takes; a
weight is 1 to 1000, `weight=` alone clears them all, and a group in the
group counts as one member, `weight=group/fast=2`). A key has no allowance to weigh, so it goes by its
order or by what magpie sent it lately — except a sub2api key its owner gave
a 5-hour, day or 7-day limit: with the provider's Balance URL set to the
relay's `/v1/usage`, its card shows those windows and routing weighs it by
them as it does a subscription, resting it till the window renews once the
relay says `api key 7天限额已用完` — or till its windows are next read short
of full, its limit raised or its usage reset. A key bought as a plan — Kimi Code, GLM Coding Plan
or Z.ai, MiniMax Coding Plan, OpenCode Go, Command Code, Ollama Cloud — is weighed by its
plan's windows the same way. With one key or account, the Routing page still
shows what it has left. `stays=` is how long a conversation
stays with the key or account that answered it: `auto` (the default, while
the vendor's cache of it is worth keeping), `session`, `turn`, `off` or
`strict` (never moved to another key or account, even past its usage cap:
its errors go to the agent).
`models=` replaces the whole list, in order; a bare model id works when only
one provider serves it.

`listed=no` (the group editor's **Model lists: The group alone**) keeps the
models a group names, and those its patterns match, out of `magpie models`,
`/v1/models` and every agent's list, so a client offers the group alone.
They are still served: the group routes over them, and a request naming one
as `provider/model` is answered. A group in it keeps its own setting, and a
group switched off keeps nothing out. `listed=yes` lists them again. To keep
a whole provider for groups, use its **Only through routing groups** switch
instead.

A group can also be of decision models alone (Jev and the like: TypeSafe's,
Bailian's `decision-model-preview`, a plugin's marked models) — never mixed
with models that hold a conversation. It is not offered to agents; it is asked
at `POST /v1/systemone` with `"model":"group/<id>"` (or its id or name), and a
chat group can take it as its classifier. Each of its models' keys and
accounts is asked in the group's routing order: one that fails as a chat
request would fail over (a 429, a 5xx, an unreachable host, an exhausted
key) rests and the next is asked, and the Routing page's Requests list shows
every try. It takes no patterns, rules, classifier, effort or fast mode.

```sh
magpie group add Jevs models=typesafe/jev-latest,bailian-token-plan/decision-model-preview routing=order
curl -s localhost:3425/v1/systemone -H 'Authorization: Bearer magpie' \
  -d '{"model":"group/jevs","state":{"message":"fix the login bug"},"questions":{"intent":{"type":"choice","choices":["bug","feature"]}}}'
```

The Routing page's Requests list defaults to the time-ordered By request view.
Choose By session to group calls by the agent's session ID; the page remembers
your choice across reloads.
Codex title helpers with an explicit parent or fork source join their originating
chat, retaining their title badge and contributing to its cost. Titles without
ancestry and ordinary forked chats stay separate.
Codex chat names come from its local name index and follow renames. Other
agents' sessions (Claude Code and the rest the Sessions page reads) take the
name the Sessions page shows for them, read from their own files on this
computer. Unknown or remote-only names fall back to the ID; the full ID
remains in the heading tooltip.
Expand a session to see each request. Each request and session shows its estimated cost at the effective model
prices, including cache reads and writes. Session totals cover the listed
requests only (the live trace or the selected day's retained history), and a
`+` marks a partial estimate. Calls without a session ID are listed separately;
old history without token tiers, or a model without a known price, shows `—`.

Opening the Providers page automatically checks Alma, CC Switch, Claude Code
and Codex for local provider configurations that can be imported. With no
providers yet, a hint shows how many were found and which apps they came from;
otherwise a small **Import local configurations** link sits beside **Add
provider**. Both open the existing picker with only the offered configurations
selected; nothing is added until you confirm the selection. **Ignore** remembers
the offered configurations on this device across window restarts; new or changed
configurations can be offered again. **Add provider → Import…** still includes ignored configurations.
Discovery runs in the background, at most once a minute when opening or
refreshing the page, and refreshes after an import. Configurations already in
magpie, entries that cannot be imported, providers turned off in their source
app, and ID collisions that cannot join as another key are excluded from the
hint. The latter two remain available through manual import. Counts refer to
configurations in source apps, which may include the same provider
in more than one app. Only app names and opaque configuration fingerprints
reach the automatic hint; ignored fingerprints are kept in local browser storage.

The app's Import from other apps dialog can copy providers from Claude Code's
`settings.json` (`CLAUDE_CONFIG_DIR` when set) and Codex's `config.toml`
(`CODEX_HOME` when set) into magpie. Codex imports custom
`[model_providers.*]` entries with an inline `experimental_bearer_token`,
including fixed headers for custom providers in
`[model_providers.*.http_headers]` and models from
`[profiles.*]` or `model_catalog_json`. Review the entries before importing;
subsequent changes to agent settings are not automatically copied to magpie.
Entries that point back to magpie or only name an `env_key` are skipped.

The same dialog reads OpenCode's own sign-ins, `auth.json` in
`$XDG_DATA_HOME/opencode` (`~/.local/share/opencode` when unset, on Windows
too), where `opencode auth login` keeps them. An API key comes in on the
magpie preset at the same address: OpenCode Go's key on OpenCode Go, Zen's
(`opencode`) on OpenCode Zen, and a vendor's (OpenAI, Anthropic, DeepSeek,
OpenRouter and others) on its own. OpenCode keeps no address beside a key,
so one of a provider magpie has no preset for is listed and skipped. Sign-ins
(Claude Pro/Max, Copilot, an enterprise server's token) are listed and
skipped; add the subscription in magpie instead. OpenCode is read only when
you open the dialog: the Providers page's automatic check leaves it out, as
the file holds every sign-in OpenCode has. magpie never writes to it.

### How magpie picks an account

When a subscription has several accounts on (Claude Code's or Codex's
saved accounts, say), or a routing group has several members, the gateway
orders them for each request in these steps. The Routing page shows the
result for every request: who went first, and why.

1. **Who can answer.** The account the agent is signed in to, unless it
   is paused, and the other accounts that are ticked. An account set not
   to serve the model, or held at its usage cap, is never tried. An account whose plan doesn't list the model (a Free one behind a
   Plus) is tried only when no other lists it. A Claude account that has to
   be signed in again stays in the list, but is passed over when its turn
   comes, without resting.
2. **The routing.** What is left is put in order by the routing the
   subscription or group uses (below).
3. **Rate limited ones last**, only with *Sink* on: an account a 429 rate
   limited while it still had quota goes behind every one not rate limited
   since.
4. **The provider's fallback models** come after its own accounts.
5. **Resting ones last.** An account resting after a failure goes to the
   back, behind every fallback. It is never dropped: when it is the only
   one, it is tried anyway.
6. **The conversation's account first.** Once an account has answered a
   conversation, it is moved to the front for the conversation's next
   requests, as *Stays* says. In auto mode that is within a turn, and
   across turns while the last request read from the vendor's cache and
   wrote to it at least 1,024 tokens between them, and it answered in the
   last 5 minutes. A first turn reads nothing and writes the whole prompt,
    so the second stays to read it; a vendor that keeps a prompt cached
    without reporting it (OpenAI's automatic prefix cache, Gemini) is taken
    to have written what it was sent. This
   never brings back an account that is resting, nor one that has used 98%
   of a window (100% In order). *Stays: Strict* does, and asks no other:
   the conversation stays on its account, resting, spent or past its cap,
   and its errors go to the agent.

So routing decides who goes first only for a conversation nobody has
answered yet. An account that has used 90% goes behind the others for new
conversations, but a conversation already on it stays there until 98%.

**The routings.** Smart, Least used first and Weekly pace go by the share
used of the fullest window that counts the model, the 5-hour window
included. Smart and Weekly pace put an account behind the others once a
window is at 90% or more, and behind those once one is at 98% or more,
each of these groups ordered by the share used.

- **Smart** (the default): of the accounts under 90%, the one whose
  allowance renews soonest goes first, because what it has left is lost at
  the reset. The biggest window decides: the week, then the 5 hours only
  when the weeks renew in the same hour. Reset times are compared to the
  hour, by the clock: 13:55 and 14:05 are different hours, 14:05 and
  14:55 the same. An account
  with no week (Claude Enterprise) goes by its 5 hours. Credits that
  run out before their window renews, or that have no reset at all
  (WorkBuddy's packages, each ending on its own day), count as renewing
  when they run out: of several WorkBuddy accounts, the one whose credits
  left end first goes first. The daily Free Plan credits aren't counted,
  since they end at the same midnight on every account. A Claude account
  magpie knows nothing of yet goes first once, to learn what it has left
  from the answer; any other unknown account goes after those known.
  An account with a month beside its week (Factory) goes by its month:
  when the month holds less than its weeks add up to (Factory's, by
  one user's readings, about two weeks' worth), what a week leaves isn't
  lost at the week's reset, as the
  month still has it to spend later. Its row
  on the Routing page says the fullest window's share with when that
  window renews: "32% used · renews in 3 days" is the week's.
- **In order**: the accounts in their order. The first takes every request
  until it is used up (100%) or fails.
- **In turn**: each turn of a conversation goes to the next account. A new
  conversation goes to the account the fewest other conversations of the
  last 30 minutes are on.
- **Least used first**: the lowest share used first; then the account
  magpie sent the fewest tokens lately (half of them stop counting after an
  hour). An account not known counts as unused.
- **Weekly pace**: the account with the highest pace first. Pace is the
  share of the week left divided by the hours until the week renews (at
  least 1): 60% left with 20 hours to go is 3 per hour. With two weekly
  windows that count the model (Opus's own and the general one), the lower
  pace counts. An account with no week goes by its 5 hours. Accounts whose
  pace is within a tenth of the highest in their band count as alike, and
  among them the one magpie sent the fewest tokens lately goes first. A
  Claude account not known goes first once, as in Smart; another unknown
  account counts as a fresh week.

The routings besides Smart are the built-in plugin `rotation` (Routing
strategies in Plugins › Built-in). It stays on while a provider or group is
routed by one of them. Turned off, the pickers offer Smart alone, and a
provider or group still set to one of them is tried in its listed order.

Your own routing groups are the built-in plugin `groups` (Routing groups in
Plugins › Built-in), which stays on while you have one. The groups magpie
finds for a model two providers serve don't need it. Turned off, the gateway
doesn't list or serve a group of your own, and the Routing page has no New
group button; `magpie groups` still lists them.

**Make first** sets the accounts' order. How much that order counts
depends on the routing:

| Routing | What the order decides |
| --- | --- |
| In order | Everything: the first takes every request it can. |
| In turn | Where the rotation goes next. |
| Smart | Only between accounts whose windows all renew in the same hours, or whose shares used are equal. |
| Least used first | Only between accounts with the same share used and the same tokens sent lately. |
| Weekly pace | Only between accounts in the same pace band that magpie sent the same tokens lately, in practice none since magpie started. |

For Claude Code and Codex, *Make first* also signs the agent in to that
account, because the account the agent is signed in to is first. With
*Keep … signed in to* on, *Make first* changes only the gateway's order,
and the agent stays signed in where it is kept.

**When an account runs out.** A 429 that says the allowance is used up
rests the account until it is back, and the same request goes to the next
candidate at once, as long as none of the reply has been sent. How long it
rests comes from the first of these that is known: the reset time in the
refusal (Claude Code's `usage limit reached|<time>`, ChatGPT's
`resets_at`), the reset of a window magpie last read as used up (98%, 100%
In order), the vendor's `Retry-After` or rate-limit reset header (an hour
at most), else 15 minutes. A key's provider can rest longer of its own:
`magpie provider quota-rest <id> 6h`, or `until 08:00` for an allowance
that comes back at a time of day (`until 00:00+08:00` on the vendor's
clock). Its own rest stands over a `Retry-After` that asked for more than
the hour. It is never longer than 8 days. The account's
windows are read again right away, and once a reading finds the window it
filled started again (for Claude, a new `/usage`; for any subscription,
its reset gone by), it is back at once, not at the time the refusal
named. A five hours started again while its week is still used up doesn't
bring it back. A 429 that is a short rate limit rests
the account for as long as the vendor asks (an hour at most), or a
minute, doubled each time it comes back right after its rest, up to 30
minutes. Out of credit rests half an hour. Any other failure rests a minute, longer each time it fails
again, up to 10 minutes; a subscription that fails with a window used up
rests until that window renews. `magpie provider rest-cap <id> 4h` moves
both of those caps, 30 and 10 minutes, for one provider's keys and
accounts, from 1 minute (every rest a minute) up to 8 days; `default` gives
them back. `magpie provider rest-cap <id> off` is for a relay or an account
pool whose unknown errors come now and then: a key or account of it that
fails without saying why (a 5xx, a dropped connection) doesn't rest at all,
and the next request asks it again. A refused key (401, 403), a model it
doesn't serve, being out of quota or credit, or a rate limit still rests
it as above.

**When the safety filter refuses.** A reply the vendor's safety filter
refuses before any of it is said rests nobody: it is about what was asked,
not the account. By default the same request is asked of the model's other
accounts, then the routing group's other members, until one answers.
Settings › Models › Replies › *Ask again after a refusal* sets how many more
are asked: 1, 2 or 3, or *Never ask again*. When nobody else is asked, an
agent on Anthropic's Messages API (Claude Code) gets the refusal as the
vendor said it, `stop_reason` `refusal` with its category, which Claude Code
answers by switching to its own fallback model, as it does on Anthropic's
API. Codex and Chat clients get a 400 saying the model refused the request,
which they don't ask again for. Any other 400 is never asked again.

**Through the gateway, or on its own.** Everything above is the gateway's,
for requests sent through magpie. Claude Code or Codex used on its own
talks to the vendor with the account it is signed in to, and routing
never sees it. For that, magpie looks at the account the agent is signed
in to a minute after it starts and every 5 minutes after: once it has used
98% of a window (100% In order), or reached its usage cap, magpie signs
the agent in to the next ticked account, in their order, whose allowance
is known and under that share. A saved Claude account magpie knows
nothing of yet (see below) is not moved to. Once the account it moved the
agent off has every window under 90% again, it signs the agent back in to
it. *Keep … signed in to* turns this off: the agent stays on the first
account, or on the account you picked, whatever it has left. It changes nothing in the gateway's routing.

With more than one Codex account on, magpie also sets `openai_base_url`
in Codex's config.toml, so a request the signed-in account has no
allowance for goes on to the others through the gateway. *Only switch
Codex's sign-in* under Providers › Codex (or `magpie accounts switch-only
codex on`) leaves that out: magpie only moves Codex's own sign-in
(`auth.json`) as above, and Codex's requests go straight to OpenAI, never
through magpie (#1907). An `openai_base_url` you wrote yourself stays. The
Codex app and a running `codex app-server` read the sign-in when they
start, so one already open stays on its account until it is opened again;
a new `codex` session takes the new one. Connected to magpie, Codex's
requests go through it as before.

**How often an allowance is read.** Routing uses what was read last and
never waits for a reading, except the first one after magpie starts (3
seconds at most). A reading over a minute old is read again in the
background as a request is routed, and an account that fails for its
quota is read again at once; if a reading was already under way as it
failed, the account is read again as soon as that reading is back. A
reading the Usage page made counts as one, its age from when it was made.
Codex and most other subscriptions read every
account from the vendor this way. A Codex account is also known from each
reply ChatGPT sends magpie for it, which says what the account has used:
an account near its usage cap is held from the next turn on (#1295). A
usage cap is set on the account's row for each of its windows, and a
window can have one of its own, set from its meter: a five-hour window at
50% and a weekly one at 40%, or no cap on one window. The account is held
while any window is past its own. A
usage cap is still a stop on what magpie has read, not a guarantee: a turn
already under way can take an account past it, so a 99% cap doesn't
promise 1% is left. Claude is different: magpie never asks
Anthropic itself. It reads only the account Claude Code is signed in to,
by running Claude Code's `/usage`:

- once after magpie starts;
- when you open or refresh the Usage page (in the app or the TUI), or run
  `magpie quota` or `magpie accounts`, at most every 30 seconds;
- otherwise again after 5 to 15 minutes (drawn at random each time), and
  only if Claude Code was used since.

A saved Claude account that isn't signed in is never read. What it has
left is known only from what Claude Code says as that account answers a
request through the gateway, and its Usage card says so until then. So
the windows routing goes by for such an account can be hours old; a window
whose reset has passed counts as empty again.

### Signed-in agents as providers

An agent you have signed in to is a subscription with models behind it, so
magpie offers it as a provider too. Claude Code (an OAuth login in the macOS
Keychain or `~/.claude/.credentials.json`), Codex (a ChatGPT login in
`~/.codex/auth.json`), Copilot (a GitHub login in
`~/.config/github-copilot/apps.json`), Devin (`devin auth login`, kept in
`~/.local/share/devin/credentials.toml`) and Qoder (signed in from magpie with
its OAuth device flow, kept in magpie's own config; Qoder CN is its own
subscription beside it, for accounts on qoder.cn made with an Alibaba Cloud
account or a phone number, which can't sign in on qoder.com) appear in `magpie providers` and in
the Providers tab as *signed in as …*, with their models spelled
`claude/claude-sonnet-5`, `codex/gpt-5.5`, `copilot/claude-sonnet-4.5` or
`devin/swe-2-max` in every other agent's picker. magpie reads the agent's own credentials each
time, refreshes tokens the way the agent does — writing a rotated token
back where the agent will find it — and stores nothing but your model
picks; sign out of the agent and the provider is gone. The model list is
the vendor's own too: magpie asks Anthropic's, Copilot's or Codex's API with
that same sign-in, so a model added upstream appears on the next refresh.
The ChatGPT backend only streams and rejects a few parameters, so magpie
translates non-streaming requests and drops what it would refuse.
Claude subscriptions are different: Anthropic classifies another agent's
system prompt as third-party traffic even when the OAuth request otherwise
looks like Claude Code. magpie therefore drives the genuine local `claude`
binary for every Claude subscription generation. The caller's tools are
bridged into that live turn over MCP, and tool results resume the same Claude
Code process; Pi, OpenCode and every other agent use this path automatically.
The generated harness stays out of Anthropic's system-prompt classifier while
its instructions remain part of the user context. This requires Claude Code
to be installed and signed in.
A Grok subscription (SuperGrok, signed in with Grok Build) talks straight
to the Responses API the grok CLI uses, a Devin subscription to the API the
devin CLI uses, and a Cursor subscription to the agent API cursor-agent
uses, each with the CLI's sign-in and the caller's tools passed through
(Cursor's model calls them as MCP tools; none of Cursor's own tools run).
Google sign-ins — Gemini CLI's and Antigravity's — talk to Google's Code
Assist API directly: magpie reads Gemini CLI's own login from `~/.gemini` or
signs one in itself, and refreshes the token in memory. Google no longer
serves Gemini CLI's sign-in to individual accounts, only to Gemini Code
Assist Standard and Enterprise, which need a Google Cloud project named
(`magpie accounts project gemini <email> <project-id>`, or
`GOOGLE_CLOUD_PROJECT` in `~/.gemini/.env`). Google may suspend an
Antigravity account it sees used outside Antigravity, so magpie asks before
adding one; use an account you can afford to lose.

Accounts signed in elsewhere can be imported from their files instead of
signed in again: *Import accounts from a file…* under a subscription's
accounts, or `magpie accounts import <codex|claude|antigravity|factory>
<file>... [--yes]`. For ChatGPT that takes Codex's `auth.json`, codexbar's
`~/.codexbar/config.json` (every ChatGPT account it switches between) and
the exports of Cockpit Tools, CLIProxyAPI and Sub2API, as many files as
you like. Each ChatGPT sign-in is refreshed before it is added, which
checks it and makes magpie its only holder: the tool the file came from
(Codex on another computer, codexbar, Cockpit Tools, CLIProxyAPI) is
signed out of that account and has to sign in again. A backup carries no
sign-in, so this is also how accounts move to a new computer: import the
file from the old one there. The files are only read. An account
magpie has already is left as it is, two Team seats of one email stay two
accounts, and an entry with only an access token (a ChatGPT web session)
is refused, since it would stop working within days with nothing to renew
it.

### Connecting anything else

The gateway listens on `127.0.0.1:3425` (`MAGPIE_ADDR` changes it) and starts
with the app; `magpie serve` runs it alone. For reverse-proxied or container
deployments, set `MAGPIE_PUBLIC_URL=https://magpie.example.com` to the base
URL shown in the console and CLI, including connection examples. While
other machines are let in (Share on local network, or a `MAGPIE_ADDR` on
the network), the Gateway page's Connect starts at it until you pick
another address there, Settings › About lists it beside the local URL, and
`magpie web` shows it as the gateway's address. Local agent configs still
use the local gateway address.

Building an app or agent that should use magpie, or get a row on the
Agents page: see [Integrating your app or agent](integrating.md).

External clients behind a reverse proxy need an enabled gateway key
(Gateway → Gateway keys): with Settings → Share on local network on, or a
`MAGPIE_ADDR` on the network, magpie asks for one; otherwise it refuses
them, unless the proxy authenticates its clients itself
(`MAGPIE_TRUST_PROXY=1`, below). A public URL with no port of
its own — a reverse proxy's `https://magpie.example.com` — is the address
`magpie web` prints for its own page too, so the proxy must forward `/v1`
and `/v1beta` to the gateway's port and the rest to the page's. When the
proxy and magpie run on the same machine, a request it forwards over
loopback carries a forwarding header and needs a gateway key as one from
the network does (see [Providers and the gateway](#providers-and-the-gateway)); a proxy that
authenticates its clients itself can be trusted with `MAGPIE_TRUST_PROXY=1`.

It exposes:

| Path                     | API                        |
| ------------------------ | -------------------------- |
| `/v1/chat/completions`   | OpenAI chat completions    |
| `/v1/responses`          | OpenAI Responses (HTTP, streamed as SSE; a WebSocket upgrade is answered 426) |
| `/v1/messages`           | Anthropic Messages         |
| `/v1/messages/count_tokens` | Anthropic token counting |
| `/v1beta/models/{model}:generateContent` | Google Gemini (also `:streamGenerateContent`, `:countTokens`) |
| `/v1/audio/transcriptions`, `/v1/audio/translations` | OpenAI speech to text (multipart, passed through) |
| `/v1/models`, `/v1beta/models` | the catalog            |
| `/v1/codex/models`       | the catalog as Codex's `model_catalog_url` reads it ([Codex on another computer](#providers-and-the-gateway)) |

Each `/v1/models` entry includes `reasoning` and `supported_reasoning_levels`
(`[{"effort":"low"}, ...]`), and the same levels as plain names in
`reasoning_levels` (`["low", ...]`, `[]` when there are none), which
clients of the Catwalk schema (Crush and kin) read (#2576). A routing group is marked `reasoning` when any
member thinks; `supported_reasoning_levels` lists the levels the members
that think have in common. A member known not to think doesn't take them
away (#2127), nor does one whose levels nothing knows (#597). A member magpie
knows does not think is sent no reasoning ask when the group routes to it,
so the effort the group is asked for reaches the members that take it and
doesn't turn a vendor away as a 400; a member nothing speaks for is sent
what the agent asked, as before. `native_endpoints`
(`["/v1/messages"]`) names the APIs a request for the model is passed
straight through on; it is left out of a routing group, and of a model every
request to which is translated anyway.

Requests pass straight through when the vendor speaks the agent's API and
are translated otherwise, streaming, tool calls and reasoning included. The
key is `magpie` (any value works; the gateway only listens on loopback), and
models are named `provider/model`.

- A routing group is asked by its id (`group/<id>`) or its name.
- A bare model id (`glm-5.3`) works too. When several providers serve it,
  it goes to the routing group magpie found for it, which asks them in the
  order of the Providers page and falls over to the next. With found
  groups off, it goes to the first of them in that order alone.
- Speech to text (`/v1/audio/transcriptions`, `/v1/audio/translations`)
  takes OpenAI's multipart form, its `model` a magpie id such as
  `openai/whisper-1`, `groq/whisper-large-v3` or a custom provider's own
  (a vLLM `qwen3-asr`). The form goes to that provider's OpenAI-style base
  as it was sent, the audio byte for byte; only the model is set to the
  provider's own id, and a `prompt` masked as your redaction settings say.
  The answer comes back in the vendor's own format (`json`, `text`, `srt`,
  `vtt`). A provider with no OpenAI-style API is refused; one that has no
  such endpoint answers its own 404. Text to speech is not served.
- Every reply names who answered in `X-Magpie-Provider` (the provider)
  and `X-Magpie-Model` (`provider/model`), and so does the usage log.

Anything with a base-URL setting can use it:

| Tool speaks | Base URL                   | Environment                                   |
| ----------- | -------------------------- | --------------------------------------------- |
| OpenAI      | `http://127.0.0.1:3425/v1` | `OPENAI_BASE_URL`, `OPENAI_API_KEY=magpie`      |
| Anthropic   | `http://127.0.0.1:3425`    | `ANTHROPIC_BASE_URL`, `ANTHROPIC_API_KEY=magpie` |
| Gemini      | `http://127.0.0.1:3425`    | `GOOGLE_GEMINI_BASE_URL`, `GEMINI_API_KEY=magpie` |

An optional `X-Magpie-Account: <account>` header (the account's email
or login, or its id on the Routing page) pins a request to one account of a
subscription with several: only it is tried, and an unknown account, one
whose plan lacks the model, or one resting is an error rather than another
account's reply. The header is not sent on to the vendor.

A status bar can show where a turn went before its first token arrives:
send `X-Magpie-Session: <id>` with the requests (an agent's own session
header, such as Pi's or Claude Code's, works too) and read
`GET /v1/magpie/route?session=<id>`. It answers the session's latest
request as routing has it so far — `asked` (the model the agent named),
`group`, `rule` (the group's rule that matched, and `rule.pick`, the
effort its decision model picked), `model` and `effort` (the member being
tried now, as `provider/model`, and the reasoning it was sent at), and
`tries`, one per member tried, each failed one a fallback with its `fail`
— then `done`, `status` and `served` once the reply is over; `route` is
`null` before the session has one. The route appears once routing has
decided, before the vendor is asked. `after=<seq>&wait=<seconds>` (up to 60)
holds the answer until the route changes past the `seq` of the last one,
so a UI can follow a turn with one request at a time. Only the session
named is told; like `/v1/magpie/quotas`, it answers this machine, and
another only with the key of a gateway shared on the local network. A key
held to a budget, to some models or to some accounts is told the routes of
its own requests alone: another key's session answers `route: null`.

To show the one in use without a session, `GET /v1/magpie/quotas` (and
`magpie quota --json`, the same list) has `lastServedAt` on each
subscription account, plan and key that answered a request through the
gateway in the last 30 days, and `last: true` on the latest. It is kept
in `served.json` beside `providers.json`, so a restart keeps it.

Each subscription window there also has the Usage page's whole-window
estimate (#2333): `holds` (`tokens`, `cost` at API list prices, `priced`)
reckoned from magpie's own calls through the account since the window
began, over the share used. Where it can't be told honestly, `holdsWhy`
says why instead: `low` (under 5% used), `clock` (no reset clock), `log`
(the window began before magpie's log), `none` (nothing routed in it) or
`credits` (a call in it may have been paid from credits). A window with
neither is one no whole is told for: a pool, a family, money, or a card
with a balance or read on another computer.

`GET /v1/magpie/quotas?kept=1` answers the same list from what magpie last
read, asking no vendor and never waiting for a read, for something that asks
often. `magpie statusline` is built on it for Claude Code's status line
(#1997): Claude Code runs it on every message with the session's JSON on
stdin, and it prints one line, the model the session's latest turn went to
(from `/v1/magpie/route`), then each subscription and plan with its windows'
use, `*` on the one that answered last:
`→ claude/claude-opus-5-5 | claude a@example.com* 5h 62% 7d 41% | claude B 5h 8% 7d 30%`.
The accounts that answered through the gateway in the last 30 days are
shown, every one when none has; `magpie statusline claude` (a provider, or
an account's email, login or name) keeps only those. It works however Claude
Code signs in to the gateway, unlike the `anthropic-ratelimit-*` headers,
which Claude Code reads only while signed in to claude.ai. magpie doesn't
write it into Claude Code's settings, where another status line may be;
add it to `~/.claude/settings.json` yourself:
`"statusLine": {"type": "command", "command": "magpie statusline"}`. With
the gateway not running it prints an empty line.

`magpie heap [<file>]` is for a report of magpie holding too much memory: a
macOS `sample` of the app shows Go's frames as `???`. It asks the running
magpie that serves the gateway (the app, the menu bar, `magpie serve` or
`magpie web`) for `GET /v1/magpie/heap`, a Go heap profile taken after a
collection (`runtime/pprof`'s `heap`, gzipped protobuf, which
`go tool pprof` reads), and writes it to `<file>`, else
`magpie-heap-<date>-<time>.pb.gz` in the working folder, never over a file
there. It prints the file and the runtime's numbers the gateway sends in
`X-Magpie-Heap-Stats`: the process, `HeapAlloc` (and what is live after the
collection), `HeapInuse`, `HeapSys`, `StackSys`, `Sys` (all Go took from
the system; a footprint far above it is outside Go, such as the webview),
`NumGC` and the goroutine count. The route answers only a program on this
computer calling it as `localhost` or a loopback address: another machine,
a proxy's or tunnel's client, any request with a browser's marks (an
Origin, listed or not, `null`, `Sec-Fetch-*`, a `Mozilla/` user agent) and
any other `Host` are refused 403, with a gateway key too. With
`MAGPIE_ADDR` on an address that isn't loopback, the call isn't from
loopback, so it is refused as well.

`magpie cpu [<file>] [--seconds N]` is for a report of magpie using too
much CPU (@LeiCheng42 on X, while Codex ran). It asks the same running
magpie for `GET /v1/magpie/cpu?seconds=N`, a Go CPU profile recorded for
30 seconds, or N from 5 to 120 (`runtime/pprof`'s CPU profile, gzipped
protobuf, which `go tool pprof` reads). While it records, use magpie as
you do when it is busy, such as running Codex: the gateway goes on serving
every other call, and the profile is of that. It writes the profile to
`<file>`, else `magpie-cpu-<date>-<time>.pb.gz` in the working folder,
and refuses a file that is there before it records, not after. It prints
the file and the numbers the gateway sends in `X-Magpie-CPU-Stats`: the
process, the CPU time it used over the seconds recorded (user and system,
as the system counts it, with the share of one core) and since it started,
its cores and `GOMAXPROCS`, and the goroutine count. Go records one CPU
profile at a time, so a second `magpie cpu` while one records is refused
409; one whose caller hangs up stops recording. The route answers the same
callers as `/v1/magpie/heap`, and refuses the same ones 403.

A gateway key held to a budget, to some models or to some accounts is told
none of this magpie's accounts (#1981): `/v1/magpie/quotas`,
`/v1/magpie/quotas/history`, `/v1/magpie/quotas/cards`,
`POST /v1/magpie/quotas/refresh` and `/v1/magpie/concurrency` answer it
403 with `"code": "gateway_key_held"`, from this machine or another. It
reads its own limit from `GET /v1/magpie/limit`. Another magpie that uses
such a key shows only that key's own card.

Its owner can let such a key be told the quotas of what it may use
(#2216): the **Quotas** badge on the key's row (on hover; it reads
*Quotas shown* while on), or `magpie gateway-key quotas <id> on` (`off`
again, or no argument to see which). The four quota routes then tell it
the cards of the providers whose every model it may use — a key held to
some of a provider's models is told nothing of that provider, since the
models it may not use spend the same quota — and of those only the
accounts and keys it may use, account names and balances included. A
card it isn't told is refused a refresh (403 `gateway_key_held`), and a
refresh that names none reads again only the cards it is told. A key
told none of the cards is answered `/v1/magpie/quotas/cards` 403
`gateway_key_held` as before, so another magpie shows its own card, not
that nothing was read. `/v1/magpie/concurrency` and `/v1/magpie/usage`
still refuse it.

An app of your own can read the Usage page's numbers too.
`GET /v1/magpie/usage?period=<today|week|month|all>` is the page's summary,
the same JSON as the window's `/api/usage` (`path` is left out for another
machine), with every session in `sessions` where the window's lists the
top 100 by tokens; `sessionCount` is how many there are. `GET /v1/magpie/usage/requests` is its requests a page at a time
(`offset`, `limit` up to 500), with the same filters as the Requests log:
`period`, `day`, `agent`, `provider`, `model`, `account`, `callerKey`,
`purpose`, `failed`, `retried`, `q`, `via`, `computer`. `callerKey=none`
keeps the calls no gateway key made, and `by.callerKey` splits the page's
calls by key, `none` among them (#2521). What was said in a call is not
in either. Like `/v1/magpie/quotas`, they answer this machine, and another
only with a gateway key. A key held to a budget, to some models or to some
accounts is told its own calls alone: `/v1/magpie/usage` refuses it with a
403, and `/v1/magpie/usage/requests` keeps only its rows, filter lists and
share of the split by key.

What is in flight now, for a status bar such as SketchyBar (#2132):
`GET /v1/magpie/concurrency` answers `requests`, every agent request the
gateway is working on, whether or not any limit is set (those waiting for a
slot among them); `waiting`, how many of those wait for a slot under a
provider's or account's limit; and `tools`, Claude Code turns run through a
Claude subscription that wait on their caller's tool results, which are not
requests. Each is `0` when idle. `data` is each limited key's or account's
own count, empty when no limit is set. `active` lists those requests one
by one, oldest first, `[]` when idle: `id`, `agent`, `model` and
`provider` (once routed), `session` when the agent sends one, `started`
and `ms` since, and `phase` only where the gateway knows it: `slot`
waiting in a key's or account's queue, `rpm` waiting for room in the
minute, `upstream` out at the vendor (sent, or its reply streaming). No
prompt, reply or key is in it. The read asks no vendor and isn't counted
itself, so it can be polled every second. It answers as
`/v1/magpie/quotas` does: this machine, another only with a gateway key,
and never a held key.

An app that calls its vendor itself, not through magpie, can report those
calls so the Usage page counts them: `POST /v1/magpie/usage/requests` with
`Content-Type: application/json`. The body is one record or an array of up
to 5000 (32 MiB at most), shaped like a `usage.jsonl` line:

```sh
curl -s http://127.0.0.1:3425/v1/magpie/usage/requests \
  -H 'Content-Type: application/json' \
  -d '[{"t":"2026-10-13T08:00:00Z","agent":"my-app","provider":"anthropic",
        "model":"claude-sonnet-4-6","in":1200,"out":340,"cache_read":9000,
        "cache_write":0,"ms":2400,"ttft_ms":610,"status":200,"rid":"req_01"}]'
```

- **Fields:** `t` (RFC 3339), `agent`, `provider`, `model` and `rid` are
  required. `in`, `out`, `cache_read`, `cache_write`, `cache_write_1h`,
  `reasoning`, `ms`, `ttft_ms`, `first_text_ms`, `flow_ms`, `status`,
  `cost` (USD), `effort`, `session` and `err` may be left out.
- **Speed:** the Requests page tells a reply's speed from `out`, `ms` and
  `ttft_ms` (ms from the request to the reply's first content: text,
  reasoning or a tool call). A reply that reasoned (`reasoning` above 0)
  is timed from its first text, so it tells a speed only with
  `first_text_ms` (ms from the request to its first text), which may not
  be before `ttft_ms` or after `ms`. `flow_ms` is how long the content
  took to come, from where the speed is timed: the time from a tenth of
  it to nine tenths, times 10/8 (at most `ms` × 10/8). A reply whose
  content came in under a quarter of its window was held and let go in a
  burst, and tells no speed. Leave out what you don't know.
- **Pricing:** a record without `cost` is priced at magpie's prices for
  its provider and model.
- **Past times:** `t` is taken as given, however far back, so a history
  can be backfilled; it may not be before 2000-01-01T00:00:00Z or more
  than an hour ahead.
- **Repeats:** a `rid` already reported is skipped, after a restart too,
  so a batch can be sent again.
- **Refusals:** a bad record refuses the whole post with a 400 naming it,
  as `error.index` and `error.param` (`record 3: in: must not be
  negative`), and nothing is written.
- **The reply:** `{"accepted":1,"skipped":0,"records":[{"index":0,"rid":"req_01","status":"accepted"}]}`.
- **Showing:** reported rows carry `source: "reported"`, count in every
  total, and can be listed alone with `via=reported` (`reported` in the
  GET's reply has their totals).
- **Nothing live:** they never count against a gateway key's limit or a
  budget, a subscription window, an alert, a rate limit or routing.
- **Who may post:** the same as the GETs: this machine, or another with a
  gateway key. A web page needs a key even on this machine.

A row's `session` is the conversation it belongs to. A Claude Code
subagent's call keeps its parent conversation there and adds `subagent`,
its agent id (the `agentId` of its lines and its
`<session>/subagents/agent-<id>.jsonl` file), and `parent_agent`: the
subagent that started it (Claude Code's `x-claude-code-parent-agent-id`, or
the `parentAgentId` of the subagent's `.meta.json`); none when the main
conversation did.
Records written before these fields came have neither. Codex's sub-agents are told
apart by `kind` only.

`magpie quota refresh <provider> [<account>]` reads that card again, as its
refresh button on the Usage page does, and prints it alone; the other
vendors aren't asked. Name the account by its login or its alias, or write
`<provider>/<account>`. A second refresh of the same card within 30 seconds
asks nobody and prints what was read, saying so on stderr, so a status line
can run it in a loop. `--json` prints the card as `magpie quota --json` does,
with `readAt` (when it was read) and `error` (why it couldn't be).

`magpie quota wait <provider|account>` blocks until that subscription (any
of its accounts magpie has on) or that one account has allowance again — no
window that stops it used up — then exits 0, so a long task stopped by its
limit can go on unattended:

```sh
until codex exec "…"; do magpie quota wait codex || break; done
```

It reads the vendors itself, whether or not the gateway runs, and again
shortly after the soonest reset it knows (every 1 to 10 minutes; an
allowance it can't read is asked again less often each time), saying on
stderr what it waits for and until when. Name an account by its email or
login, or as `<provider>/<account>` when two subscriptions share it.
`--timeout 6h` exits 1 if it passes first, `--quiet` says nothing; an
unknown name exits 2 and Ctrl+C 130.

A ChatGPT account that holds credits keeps answering once a usage window is
used up: the vendor spends the credits, so a task goes on. That is the
default. `magpie quota credits <account> off` (or *Use credits* on the
account's Usage card) turns it off for that account: once magpie's latest
reading of its windows, refreshed about every minute, shows one used up,
the gateway holds it as used up till the window renews, and requests go to
the other accounts, groups and fallbacks; with none left, the request gets a
429 saying why, unless the account spends its resets by itself and its week
is used up, when a reset is spent first. `magpie quota credits` lists the
accounts set not to spend them, `magpie quota credits <account>` says one's.
It changes the gateway's routing only, never the account Codex is signed in
to. The credits an account holds show beside its windows in `magpie quota`,
`magpie accounts`, the Usage page and the menu bar panel.

An account can have a name of your own, for telling apart seats the vendor
names alike (a team's GLM seats under masked emails): click the account's
name in its provider's editor, or `magpie accounts alias <agent> <email>
<name>` (`--clear` takes it off). The name is magpie's, kept in
providers.json; the vendor's or plugin's sign-in is never written to. It
shows on the account's Usage card, in the menu bar, in `magpie accounts`,
`magpie quota` and `GET /v1/magpie/quotas` (`alias`). A GLM Coding Plan
seat added both as a key and as a signed-in ZCode account is shown once, on
the account's card (two cards would count its allowance twice); that card
is then named for the key's provider until you name it yourself, and says
so (`seat`). Once the two are found to be one seat, a failed read of either
doesn't bring the key's card back: only the account signed out, or windows
that no longer agree, do.

The Usage page's cards and the menu bar panel's *Allowances* tab are in one
order: drag a card's logo on the Usage page (or Alt+arrow keys on it), or
press *Arrange* at the foot of the panel's tab and move the rows there. The
same *Arrange* hides a subscription from the panel's tab (*Hide*, *Show*
puts it back), and the tab's foot says how many are hidden. Hiding is the
panel's only: routing, caps, the Usage page and the menu bar's own cells
still have it, and a menu bar cell for it still opens its card. Both are
kept in magpie's settings (`usageOrder`, `panelUsageHidden`). Which
allowances the menu bar itself shows beside its icon is picked in Settings.
The same list has *Tokens and cost today* and *Tokens and cost, last 7
days*: every agent's requests as the Usage page's Requests tab sums them,
the agents' own session-file calls among them, all their tokens (input,
output and cache, marked Σ) over the cost in your currency (`usage:today`,
`usage:7d` in `trayUsages`); the tooltip splits the tokens into in, out and
cached. Clicked, one opens the Requests tab at that period.

The *Gateway* tab in the app has this as copy buttons and ready-made
snippets (shell, curl, Python, Node) for each API, the list of model ids,
and the recent calls; `MAGPIE_DEBUG=1` logs every call to the terminal.

**Claude Code** gets `ANTHROPIC_BASE_URL`, `ANTHROPIC_AUTH_TOKEN` and the
model variables in the `env` block of `settings.json`; picking a native
model (`opus`, `sonnet`…) removes them and restores whatever was there.
A key in `ANTHROPIC_AUTH_TOKEN` signs Claude Code out of claude.ai while it
runs through magpie: claude.ai's plan limits in `/usage`, its connectors,
voice and `/teleport` are off. Set its sign-in to claude.ai to keep that
login: magpie writes the token empty, Claude Code sends magpie its claude.ai
sign-in, and magpie never passes it on (nor its `oauth-2025-04-20` beta). A
provider that refuses magpie's own key is then reported to Claude Code as
a 502, not as its sign-in refused, so Claude Code doesn't log out. Remote
Control and ultrareview stay off: Claude Code has them only on Anthropic's
own address. The choice is offered where the gateway takes any key, so not
from a WSL distro under NAT while magpie is shared on the network.

**Codex** gets a `[model_providers.magpie]` table, `model_catalog_json`
pointing at `~/.codex/magpie-models.json` (written from the catalog, so the
models show in Codex's own list) and a valid `model`/`effort`; picking a
native model or disconnecting removes `model_provider` and the catalog. The
`[model_providers.magpie]` table stays, so a thread started on magpie can
still be opened. Codex won't load its config at all when `model_provider =
"magpie"` has no table ("Model provider `magpie` not found"). If another
tool leaves that state behind, magpie writes the table back the next time
it syncs. Signed in to ChatGPT (the sign-in field's default), Codex keeps
its own provider and sign-in, and magpie's models join its list through
`openai_base_url`. magpie becomes Codex's provider then only while the
Codex app holds the account: OpenAI no longer allows it and it has no
credits left, except a workspace account still within its overage. The
app sends nothing for a held account, so magpie steps in, and it steps
back once the account has room again. A window at 100% with credits left
doesn't count: Codex stays signed in and keeps sending on those. Your
ChatGPT sign-in is never touched.
Codex reads its model list at start-up, so restart it after a switch.

**OpenCode, Pi, Crush** get a `magpie` provider entry and `magpie/provider/model`.

**Gemini CLI** switches `auth` between API key, Google account and Vertex;
the API key goes to `~/.gemini/.env`. Picking a catalog model points
`GOOGLE_GEMINI_BASE_URL` at the gateway (which speaks the Gemini API), sets
`auth` to API key with the gateway token, and names the model in
`settings.json`; a native model puts the previous auth back.

### Import links

A vendor or relay can hand its users a ready-made provider as a link:

```
magpie://import?preset=deepseek&key=sk-…
magpie://import?name=Acme%20Relay&chat=https://api.acme.example/v1&anthropic=https://api.acme.example&key=sk-…&models=gpt-5.5,claude-sonnet-5
```

Opening one brings up magpie with what the link would add: the name, the
hosts your prompts and key would go to, the models. Nothing is saved until
you press *Add*. `magpie import <link>` does the same in a terminal.

| Parameter   | Meaning                                                            |
| ----------- | ------------------------------------------------------------------ |
| `preset`    | a preset id (`magpie presets`); its endpoints are used. Not `google-vertex`, which is asked at your own Google Cloud project |
| `region`    | with a preset that has regions, which one; SiliconFlow: `cn` (default) or `intl` |
| `name`      | the provider's name; required without a preset                     |
| `id`        | its id; derived from the name when absent                          |
| `key`       | the API key; the user pastes one when absent                       |
| `chat`      | OpenAI Chat Completions base URL (`…/v1`)                          |
| `responses` | OpenAI Responses base URL (`…/v1`)                                 |
| `anthropic` | Anthropic Messages base URL (the root, without `/v1`)              |
| `gemini`    | Google Gemini API base URL (`…/v1beta`), as the app's editor and the TUI take one |
| `models`    | model ids to expose, comma separated                               |
| `catalog`   | models.dev provider id, for model names and reasoning levels       |
| `website`, `keys` | the vendor's site and its API-key page (https)               |
| `icon`      | an https picture of the vendor's own (PNG, JPEG, GIF, WebP, ICO, SVG, at most 1 MB). magpie downloads it once, after you confirm the import, into its icons folder; without one it falls back to the catalog's logo or a plain mark |

Base URLs must be https (plain http only to this machine or the local
network). Web pages and GitHub don't link custom schemes reliably, so link
to `https://usemagpie.ai/import#<same parameters>` instead: it opens
magpie, and offers the download when it is not installed. The parameters
stay in the fragment, which browsers never send to a server. The full guide,
with a link builder: <https://usemagpie.ai/docs/import>.

## Install

Download the app for macOS, Windows or Linux from
[usemagpie.ai](https://usemagpie.ai), or install it from a terminal (on
Linux, the desktop app when WebKitGTK 4.1 is installed, the command
otherwise):

```sh
curl -fsSL https://usemagpie.ai/install.sh | sh
```

Behind a firewall, or without access to GitHub, both the installer and
`magpie update` take `--proxy <url>` (http, https, socks5, socks5h; or
`MAGPIE_PROXY`, and `HTTPS_PROXY` / `ALL_PROXY` as usual) and
`--mirror <prefix>`, a GitHub download mirror of your choosing put before
the release's github.com URL (or `MAGPIE_MIRROR`; `magpie update mirror
<prefix>|off` keeps one for every update, the app's too):

```sh
curl -fsSL https://usemagpie.ai/install.sh | sh -s -- --proxy http://127.0.0.1:7890
magpie update --mirror https://mirror.example/
```

No mirror is used unless you give one, and the SHA-256 every download is
checked against still comes from usemagpie.ai, never from the mirror.

Mac releases are signed and notarised; the Windows and Linux builds are not
signed yet (Windows SmartScreen may ask before the first run). Every build
keeps itself current: the app
downloads a new version in the background and installs it when you restart
(*Restart to Update* in the menu) or quit; `magpie update` does the same from
a terminal. Every release is on
[yetone/magpie-releases](https://github.com/yetone/magpie-releases/releases).

From source:

```sh
go install github.com/yetone/magpie@latest
```

or build locally:

```sh
make build            # ./magpie with the desktop app (needs cgo + the platform webview)
make app              # macOS: magpie.app, a menu bar app with no Dock icon
make cli              # terminal-only build, no cgo, cross-compiles anywhere
make release          # dist/: native app build + cli builds for every platform
make release-windows  # dist/: the Windows app, amd64 and arm64 (cross-compiles)
make release-linux    # dist/: the Linux app for this machine's arch
```

Linux needs `libgtk-3-dev` and `libwebkit2gtk-4.1-dev` for the app build
(the Makefile adds the `gtk3` tag; with plain `go build`, pass `-tags gtk3`);
Windows uses the WebView2 runtime that ships with the OS.

Termux uses the Android terminal build: the installer puts it in
`$PREFIX/bin`, and `make build` selects it automatically. With plain
`go build` or `go install`, pass `-tags nogui`; use `magpie web` or
`magpie tui` for the interface. `magpie autostart on` requires Termux:Boot.

### As a background service

`magpie daemon install` runs `magpie serve` as a service of your own, with
no window and no desktop session: a server, or a machine you reach over
SSH. The system starts it at login (and at boot, see below) and starts it
again when it stops with a failure.

```sh
magpie daemon install    # write the service and start it; again, to take a new PATH or proxy
magpie daemon status     # running or not, its pid, file and logs, whether the gateway answers
magpie daemon restart    # after updating magpie
magpie daemon stop|start
magpie daemon uninstall  # stop it and remove the file
magpie daemon web on     # the service serves the browser page too; off stops it, no word shows it
```

The service serves only the gateway unless asked for the page:
`magpie daemon install --web` (or `magpie daemon web on` once installed)
runs `magpie serve --web`, which serves the gateway and `magpie web`'s
page from one process, so the page shows the service's own gateway,
providers and accounts. It listens on `127.0.0.1:3430`; `--lan` puts it on
`0.0.0.0:3430` and `--addr host:port` anywhere, with the same warning as
`magpie web --lan`: anyone with the link can change your settings. Its key
is made once and kept in the service file (as `MAGPIE_WEB_KEY`, readable by
you alone), so the link stays the same across restarts and reinstalls;
`daemon web off` drops it, and the next `on` makes a new one.
`magpie daemon status` prints the link. `--no-web` installs without it.

`magpie web` beside a running service doesn't take the gateway's port:
when the service serves the page it prints that page's link and exits, and
otherwise its own page uses the service's gateway and says so. A magpie
holding the port but slow to answer (a busy server, or one still starting)
is waited for, not raced: the new one serves the gateway only once the
port is free.

- **Linux:** a systemd user unit, `~/.config/systemd/user/magpie.service`
  (`$XDG_CONFIG_HOME/systemd/user` when set), run with `systemctl --user`,
  no root. Its log: `journalctl --user -u magpie -f`. systemd starts a
  user's units at their first login and stops them at their last, so for
  the gateway to run from boot with no one logged in, run
  `sudo loginctl enable-linger $USER` once; `magpie daemon status` says so
  when it isn't on. From `su` or `sudo -u`, `systemctl --user` can't reach
  your session: log in as the user, or set
  `XDG_RUNTIME_DIR=/run/user/$(id -u)`. On WSL, turn systemd on first
  (`[boot] systemd=true` in `/etc/wsl.conf`). A system without systemd is
  told so; run `magpie serve` under the manager it has.
- **macOS:** a launch agent, `~/Library/LaunchAgents/com.yetone.magpie.serve.plist`,
  loaded with `launchctl bootstrap gui/$UID`. Its log:
  `~/Library/Logs/magpie/serve.log`.
- **Windows:** not a service; `magpie autostart on` opens magpie, which
  serves the gateway, when you log in. Inside WSL, `magpie daemon` works as
  on Linux.

The service gets the environment `magpie daemon install` ran in: `PATH`
(where it finds the agents' CLIs and bun), the proxy variables, `MAGPIE_*`
settings such as `MAGPIE_ADDR` (and the `--tls-cert`/`--tls-key` pair, as
`MAGPIE_TLS_CERT` and `MAGPIE_TLS_KEY`), the XDG folders and the agents' own folder
variables (`CODEX_HOME`, `CLAUDE_CONFIG_DIR`…). To that `PATH` it adds
what a terminal of yours has and the shell may not: nvm's default Node
(not its newest), the other version managers' and npm's folders, and your
login shell's own `PATH`, so plugins and the MCP servers they start find
`node` with nvm loaded lazily or over SSH. When no `node` is found even
then, the install says so. The file is readable by you
alone, since a proxy's address can hold a password. Run the install again
after changing any of them. It runs magpie by the name it has on `PATH`
when that is the same file (Homebrew's link stays right across updates),
and refuses a `go run` build. It is refused while `magpie autostart` is on:
the app opened at login serves the gateway too, and the two would take
turns failing on the port. The app opened by hand while the service runs
uses the service's gateway.

### Docker

`docker build` makes a server image: the terminal-only binary on
distroless (`cc`, for the glibc the plugins' Bun needs) with bash and
busybox for a terminal in the container (`docker exec -it magpie bash`, or a
NAS panel's terminal; `magpie` is on its PATH), run as nonroot,
with everything it keeps in a volume at `/config`: magpie's own files
(`/config/magpie`), the sign-ins kept where their agent keeps them (HOME is
`/config/home`, so `~/.codex`, `~/.claude`… are in it) and the cache with the
Bun plugins run on (`/config/cache`, downloaded once). A volume made by an
older image keeps working: magpie adds these folders to it on start, and only
sign-ins made with that older image, which lived outside the volume, have to
be made again.

```sh
docker build -t magpie .
docker run -d --name magpie -p 127.0.0.1:3425:3425 -p 127.0.0.1:3430:3430 -v magpie-config:/config magpie
```

3425 is the gateway for agents. Inside the container it takes any key; a
request from outside it, the host's own included, must carry an enabled
gateway key as its API key, and one without gets 401. Make one in the
browser UI (Gateway → Gateway keys) or with
`docker exec magpie magpie gateway-key add "Docker client"`. Sharing
(Settings → Share on local network) isn't needed for this; turning it on
also creates a key named **Magpie**. The ports above are published on the
host's loopback; Docker's `-p 3425:3425` puts the gateway on every interface
of the host, for other machines to reach. Upgrading from an earlier image:
an agent outside the container that sends `magpie`, or another key that
isn't an enabled gateway key, now gets 401 ("the API key sent is not an
enabled magpie gateway key"); give it a gateway key.
Inside the container
magpie only sees the container's own address (Docker's 172.17.x), so set
`-e MAGPIE_PUBLIC_URL=http://<the host's or NAS's address>:3425` (the port
published on the host) for the address it shows and prints to be the one
other machines use; behind a reverse proxy, set it to that external base URL
and follow the [authentication requirements above](#connecting-anything-else),
especially when the proxy reaches magpie over loopback.

For the browser UI run the image with `magpie web --addr 0.0.0.0:3430 --no-open`
in place of the default `serve`, and open
`http://localhost:3430/?k=<key from docker logs magpie>` (set `MAGPIE_WEB_KEY`
to keep one key across restarts; a browser then stays signed in for 400
days). A browser keeps cookies per host, not per port, so another service on
the same address can take the sign-in away: by clearing its site data, or by
filling the host's cookie limit (the sign-in is the last Chrome drops). A page
opened without it, such as a bookmark, says why it was turned away and takes
the key in a field, then comes back to the same page. There you add providers, sign in to
subscriptions and import sign-ins from a file. The vendor sends a sign-in's
browser back to `localhost` (ChatGPT to `http://localhost:1455/auth/callback?code=…`),
which is your own machine, not the container, so that page won't load: copy
its whole address from the address bar and paste it into the sign-in's
*Callback URL* field. `docker exec -it magpie /magpie accounts add codex`
does the same in a terminal: open the link it prints, then paste the address
the browser ended on. Keys, sign-ins and plugins live in the volume, so a
restart, or a new container on the same volume, keeps them.

The image has a `HEALTHCHECK`: `magpie healthcheck` exits 0 while the gateway
answers on `MAGPIE_ADDR`, under `serve` and `web` alike, so `docker ps` shows
the container as healthy (Compose: `depends_on: condition: service_healthy`)
with no curl in the image. Bind-mounting a folder at `/config` in place of a
named volume works too; it has to be writable by uid 65532.

#### NAS with agents on other machines

Magpie can run on a headless NAS as a shared gateway even when the agents that
use it (Claude Code, Codex, OpenCode, and so on) run on other computers. The
container does not discover agent installations on the NAS host or on remote
machines: configure those agents to use the NAS address and an enabled gateway
key. See [Connecting anything else](#connecting-anything-else) for the
protocol-specific base URLs. Keep `3425` behind a firewall or VPN and do not
expose it publicly without gateway-key authentication.

Such a page is in **gateway mode**: it shows Providers, Gateway, Routing,
Usage, Plugins and Settings, with no Agents, Sessions or Library tab, and
Settings leaves out what is written into this machine's agents (provider in
model names, Codex subagents, long conversations, Codex thread titles,
Codex thread descriptions, Codex composer predictions) and
the desktop's alerts and tray. `magpie web` is in it by itself when it finds
no agents on its machine, as in the container, and with
`magpie web --gateway`. *Settings › General › Gateway mode* picks Automatic,
On or Off for this machine (it wins over both); Off brings every page back.
The app's own window is never in gateway mode.

For a bind-mounted configuration directory, the directory must be writable by
the non-root container user (uid 65532):

```sh
sudo chown -R 65532:65532 /volume1/docker/magpie/config
```

An example Docker Compose project is:

```yaml
services:
  magpie:
    image: ghcr.io/yetone/magpie:latest
    restart: unless-stopped
    ports:
      # 127.0.0.1: the host alone; every caller outside the container needs a gateway key.
      - "127.0.0.1:3425:3425"
      - "127.0.0.1:3430:3430"
    environment:
      MAGPIE_ADDR: 0.0.0.0:3425
      MAGPIE_PUBLIC_URL: http://<nas-address>:3425
      MAGPIE_WEB_KEY: <random-key>
    volumes:
      - ./config:/config
    command: [web, --addr, 0.0.0.0:3430, --no-open]
```

Open `http://127.0.0.1:3430/?k=<random-key>` through an SSH tunnel to
configure providers, then create a gateway key for each remote agent host.
To let remote agents reach it, change the gateway mapping to `3425:3425` (or
bind it only to the NAS interface or VPN address) and recreate the
container. For Internet access, prefer a
VPN such as Tailscale or WireGuard; a reverse proxy or tunnel forwarding to
Magpie over loopback needs a gateway key from its clients, as the network does.

### Developing

```sh
make dev
```

builds with `-tags dev` and opens the app with the UI served straight from
`internal/gui/assets`: save `app.css`, `app.js` or `index.html` and the window
reloads itself. With `fswatch` installed (`brew install fswatch`), a change to a
Go file rebuilds and relaunches the app too. The dev build uses its own gateway
port (`DEV_ADDR`, default 127.0.0.1:3426), so a magpie you already run keeps
serving your agents. Point it at a scratch home to keep your real agent
configs out of it:

```sh
HOME=/tmp/magpie-home XDG_CONFIG_HOME=/tmp/magpie-home/.config make dev
```

`MAGPIE_THEME=light|dark` forces the palette and `MAGPIE_DEBUG=1` prints what the
gateway translates.

## Use

```sh
magpie                          # open the app: a window plus the menu bar icon
magpie tray                     # menu bar icon only (use this in your login items)
magpie tui                      # the same thing, in the terminal; serves the gateway while open when no other magpie does
magpie web                      # the app's window in a browser (WSL, a server over SSH); --lan, --addr, --no-open, --gateway, --tls-cert/--tls-key
                                # (a new key each run; MAGPIE_WEB_KEY keeps one, for a page run as a service)
                                # (behind a reverse proxy, MAGPIE_WEB_URL=https://<the page there> prints the link through it)
magpie serve                    # the gateway alone, no window; --web[=host:port] serves the page too, from the same process
                                # --tls-cert <file> --tls-key <file>: https:// for other computers (see HTTPS under sharing)
magpie daemon install           # run magpie serve as a service (systemd, launchd); --web, --lan, --addr serve the page too; --tls-cert/--tls-key, --no-tls
magpie ls                       # list every agent and its current settings
magpie claude opus              # set a model (agent names accept prefixes: cc, oc, gem …)
magpie codex gpt-5.6-sol
magpie codex effort high        # other fields
magpie codex effort             # one field, as it is now
magpie codex xhigh              # bare effort levels are recognised too
magpie codex deepseek/deepseek-chat   # any catalog model, through the gateway
magpie claude moonshot/kimi-k2.5
magpie claude haiku deepseek/deepseek-v4-flash   # one tier on its own model
magpie claude haiku ""          # back to the main model
magpie gemini auth api-key
magpie opencode anthropic/claude-sonnet-5
magpie oc small anthropic/claude-haiku-4-5
magpie mimo anthropic/claude-sonnet-5

magpie save work                # snapshot everything as a profile
magpie use work                 # switch back
magpie profiles
magpie rm work

magpie sync                     # refresh the models.dev catalog and every live model list

magpie quota                    # what is left of every subscription, plan and key balance
magpie quota refresh codex      # read Codex's cards again now (once per 30s)
magpie quota wait codex         # block until a Codex account has allowance again
magpie quota credits me@example.com off   # hold a ChatGPT account at its limit instead of spending credits
```

In the app, click any value to open a filtered list; type to search or to
enter something that is not listed; `esc` closes the panel. Profiles are the
chips at the bottom: click to apply, `×` to delete, *+ save current* to add.
The *Providers* tab of the window lists your providers with the agents on
each; click a row to change the key or the exposed models, *Test* it, or
click an agent icon to point that agent at one of its models. *Add
provider* shows the presets as tiles: pick one, paste the key.

In *Usage → Requests* and the tray's *Usage* tab, click a daily bar to see
that day's totals and details. Other days turn gray; click the selected day
again to return to the whole period. Changing the period clears the selection.

On macOS, *Settings → Preferences → Session terminal* chooses which installed
app opens a session from the terminal button in *Usage → Sessions*. The list
contains apps registered to open `.command` files, with the current system
default listed once. With no saved choice, magpie follows that default, or
Terminal when the default is not a terminal. The *Resume* button still copies
the command.

Keys in the terminal version:

| Key        | Action                                |
| ---------- | ------------------------------------- |
| `↑` `↓`    | choose agent                          |
| `←` `→`    | choose field (model, effort, small …) |
| `↵`        | open the picker                       |
| type       | filter; enter accepts custom values   |
| `s`        | save current setup as a profile       |
| `p`        | apply or delete (`ctrl+d`) a profile  |
| `S`        | sync the model catalog                |
| `q`        | quit                                  |

Agents read their config at startup, so a running session keeps its model
until you start a new one.

### Moving to another machine

```sh
magpie backup                   # writes magpie.magpie-backup, asks for a passphrase twice
magpie backup --no-keys ~/b.magpie-backup   # the same with no API keys in it
magpie restore magpie.magpie-backup         # on the other machine
magpie restore --no-agents b.magpie-backup  # providers, settings, profiles; agents left as they are
magpie restore --no-library b.magpie-backup # the library here left as it is
```

A backup holds your providers (with their keys, unless `--no-keys`), the
pictures picked for them, the settings, the profiles, every agent's model and
the library (unless `--no-library`): the instruction sets, the MCP servers and
the skills with their files, templates and other binaries included (a file
over 16 MB is left out, and so are the biggest once the skills' files come to
32 MB; restoring keeps the copy of such a file the other machine already
has). Without keys, a
server's environment variables and headers that look like a key go empty.
Gateway credentials, their names, ids and disabled state travel encrypted
with Settings too. Restoring Settings replaces the gateway-key store with
the backed-up one. `--no-keys` leaves gateway credentials and their legacy
mirror out; restoring it preserves the destination's existing keys instead.
Restoring the library replaces the one there — what it replaces is kept with
the library's backups — and writes it into the agents on that machine.
It is encrypted on your machine (AES-256-GCM, the key derived from the
passphrase with PBKDF2-SHA256); nothing in it can be read without the
passphrase. Restoring replaces providers with the same id and adds the rest;
one that came without a key keeps the key already there. Agent models are set
only for agents installed on that machine. Subscriptions are not in it: sign
in to them on each machine. Their model picks and settings come along, and
show once signed in; the restore, and a sync's status on the other machine,
name the ones still to sign in to. The plugins added from npm or git come
along too and are installed where it is put back (one added from a folder
stays on its machine; without keys, an option whose name looks like a key
is left out); a plugin's sign-in is not carried, a plugin already there is
left as it is, and one removed on one machine stays on the others. Piped
in, the passphrase is the first line of stdin.

### Keeping machines in sync

*Settings → Sync and backup → WebDAV, S3 or GitHub sync* keeps the same backup
on a server and brings every machine up to date with it, every 3 minutes while
the gateway runs. Choose one of these:

- **WebDAV**: a folder on a WebDAV server such as 坚果云, Nextcloud or a
  Synology.
- **S3**: a bucket on AWS S3, Cloudflare R2, Backblaze B2, MinIO, Garage, a
  NAS or any other S3-compatible server.
- **GitHub**: an existing repository, using a fine-grained token
  with Contents read and write permission. The repository must be private:
  sync refuses a public one, whose history would give anyone every sealed version.
  The address can include a folder, such as `github://owner/repo/magpie-sync`.
  Each update is a commit to that branch, so the repository also provides a
  history of the encrypted backup.

The file is sealed on your machine with the passphrase, so the server only
ever stores ciphertext. Each machine writes only over the version it read (a
conditional write), so an update that another machine made in between is
merged rather than lost.

```sh
magpie webdav on https://dav.jianguoyun.com/dav/ user=me@example.com
magpie s3 on s3://my-bucket/magpie endpoint=https://<account>.r2.cloudflarestorage.com access-key-id=…
magpie s3 on s3://backups endpoint=http://nas.local:9000 path-style=yes access-key-id=…
magpie s3                       # where it syncs to and how the last sync went; magpie s3 now, off
magpie github on github://owner/private-backups/settings branch=main
magpie github                   # where it syncs to and how the last sync went; magpie github now, off
```

For S3:

- `endpoint` is empty for AWS.
- `region` defaults to `us-east-1`, or to `auto` on R2.
- `path-style=yes` puts the bucket in the path, which MinIO and most servers
  you run yourself need.
- The secret is asked for and saved like the WebDAV password. It is used only
  with the endpoint and access key it was given for.
- The bucket must already exist.
- The access key needs to read and write `<prefix>/magpie/`. On AWS it also
  needs to list the bucket.
- A server without conditional writes is supported. There magpie checks the
  object's ETag just before each write. Two computers that write in the
  same moment can both pass that check (Aliyun OSS, #1776), and the later
  write is the one kept. The other computer notices at its next sync, a
  few minutes later. It finds a file other than the one it wrote, so it
  merges again from before its own write: what it changed goes back up
  beside the other computer's change. It never takes the other's file as
  having seen its own.
- Shared usage is reconciled only after a complete S3 listing. A missing or
  repeated continuation token, or a listing still truncated after 100 pages,
  reports a sync error and keeps previously downloaded usage days in place.

For GitHub:

- Create the repository before enabling sync. Leaving Branch empty uses its
  default branch. For an empty repository, the first encrypted backup creates
  the initial commit on that default branch. A selected branch other than the
  default must already exist.
- Give a fine-grained token access to this repository with Contents read and
  write permission. It is asked for by the CLI, never accepted in a command
  line, and is saved separately from the library's GitHub token.
- Folder is optional. Files go in `<folder>/magpie/`; every changed backup
  creates a commit. Unchanged setup creates no backup commit.
- Use the same encryption passphrase on every computer. It must differ from
  the GitHub token. The token and sync connection settings stay on each computer.
- The file limit is 64 MiB. Repository history retains encrypted old versions,
  including usage files if sharing is enabled.
- Switching between WebDAV, S3 and GitHub keeps the other services' settings
  and credentials for switching back; one service syncs at a time.

## OTLP export

Settings → Observability can export gateway request metadata over OTLP/HTTP
(JSON). Export is off by default. Set the collector's base URL and optional
headers, then enable **OTLP export**. **Export metrics** is separately off by
default; enable it for a collector that accepts duration and token histograms.
No restart is needed for saved settings. The export is the built-in plugin
`otel` (Observability in Plugins › Built-in), which stays on while settings
or the environment say to export.

For `magpie serve`, environment variables override the saved preferences:

```sh
MAGPIE_OTEL_ENABLED=true MAGPIE_OTEL_ENDPOINT=http://localhost:4318 magpie serve
```

- `MAGPIE_OTEL_ENABLED`: `true` or `false`; an endpoint alone does not enable export.
- `MAGPIE_OTEL_ENDPOINT`: an HTTP(S) base URL; `/v1/traces` and `/v1/metrics` are appended.
- `MAGPIE_OTEL_HEADERS`: comma-separated `name=value` pairs, for example
  `Authorization=Bearer%20token`. Percent-encode spaces and commas in values.
- `MAGPIE_OTEL_METRICS`: `true` or `false`, off by default.
- `MAGPIE_OTEL_SESSIONS`: `true` or `false`, off by default; trace supported local
  agent interactions from newly recorded session events.
- `MAGPIE_OTEL_BODIES`: `true` or `false`, off by default; sends each call's
  request and reply as the trace's Langfuse input and output.
- `MAGPIE_OTEL_BODIES_WHOLE`: `true` or `false`, off by default; with bodies on,
  keeps them entire rather than cut at 256 KB. A long reply is written to a
  temporary file; a body too large for the collector is still refused.

For Langfuse, use `https://<your-langfuse-host>/api/public/otel` as the base
URL and `Authorization=Basic%20<base64(public-key:secret-key)>` as the header.
Leave metrics off. This uses Langfuse's OTLP ingestion endpoint. For Langfuse
v4, add the header `x-langfuse-ingestion-version=4` for real-time ingestion.

Traces include agent, provider, model, token counts (including cache and
reasoning), HTTP status, timing, and route ID. Attempts with the same route ID
share a trace ID. Chat, Responses, Anthropic and Gemini gateway requests
also export a parent request span and child spans for every routing attempt,
including unbilled failures, retries and fallbacks, so Langfuse can render a
waterfall. A valid incoming W3C version-00 `traceparent` connects these spans
to the caller's trace. Attempt spans carry token usage; the parent does not
duplicate it. Attempt timings include any wait for a concurrency slot, while
routing and retry delays remain visible as gaps inside the parent span.
Tool execution inside the caller is outside the gateway's trace.

Model-call spans also send estimated USD input, output and total costs to
Langfuse via `langfuse.observation.cost_details`, using the same effective
prices as Magpie's usage ledger (custom prices first, then catalog prices).
Input cost includes cache reads and writes; reasoning tokens are already
included in output cost. Explicit zero prices are exported too. Unknown
prices are omitted so Langfuse can use its own model pricing. Parent and tool
spans carry no costs, avoiding duplicate counting. Subscription costs at
catalog prices are API-equivalent estimates, not subscription charges.

Enable **Trace agent conversations** to instead export one trace per
user interaction, grouping model calls and tool executions under an agent
root. Conversation IDs group those traces into Langfuse sessions. Gateway traces
remain available until a visible local store covers that exact agent/session.
For Codex and Pi, a readable session header establishes readiness without
waiting for a model/tool span. The first request checks a header bounded to
256 KiB when needed (including Codex base instructions); subsequent polls keep
idle sessions ready while their files remain among the reader's 200 newest main
files. Claude children have a separate 200-file quota and share the same 8 MiB
polling read budget. Header checks never upload history or bodies. Discovery is shared across
request IDs for two seconds. The request that refreshes discovery waits for the
scan; other unresolved concurrent requests retain gateway traces without waiting.
Polling reuses headers by path, size and mtime.
Other adapters still use recent observations (five minutes).
Only loopback requests without a gateway key can be deduplicated. The native
client session ID takes precedence over a Magpie routing override; unknown
sessions retain gateway traces, including WSL mirrored and Docker Desktop
clients whose sessions are not visible locally. Requests without a session ID
fall back to recent observations for that agent.
Claude Code/Cowork readers also follow `<session>/subagents/*.jsonl` and a
workflow's (ultracode) `<session>/subagents/workflows/<run>/agent-*.jsonl`
(not its `journal.jsonl`). Child
interactions share their parent session and use separate trace/span IDs, even
when a user UUID is copied. Child final responses are combined across content
blocks and exported when the transcript is unchanged for two seconds; no
`turn_duration` is required.
Their start times remain inferred; a parent tool link is not guessed. Main-thread calls can therefore be deduplicated. Claude
tool-less small requests (`max_tokens` at most 4096), including title/haiku
helpers, retain gateway traces because they may not enter a transcript. This
conservative rule can retain a short tool-less main request as well. Explicitly
classified auxiliary calls also retain gateway traces for other agents. This
includes Codex calls with an explicit kind (subagent, review, memgen, title or
compact): even if a child has a readable rollout, these can appear twice. Only
its unclassified calls use local-session deduplication; a rollout alone does not
prove every auxiliary request was recorded. Keyed and non-loopback clients
always retain gateway traces. Magpie reads new events from local session stores
every two seconds; it does not upload
completed history when enabled. Restarting or changing the destination starts
an observation window. Span IDs remain stable across repeated records.

Pi and Codex have been tested end to end with real clients. The other adapters
are covered by format fixtures but have **not been tested end to end**.

Supported clients and formats:

| Client | Local store | Timing |
| --- | --- | --- |
| Codex | JSONL rollouts (`token_usage_record`, `item_completed`, `response_item`) | Recorded operations and paired native tools; inferred model/native-tool intervals |
| Pi | Version-3 JSONL; optional `timing-final` | Recorded model times; inferred tool intervals |
| Oh My Pi | Pi-compatible JSONL, including `model_usage` | Pi timing; auxiliary calls have inferred zero duration |
| Claude Code / Cowork | `projects/*/*.jsonl`, `<session>/subagents/*.jsonl` and `<session>/subagents/workflows/*/agent-*.jsonl`, repeated assistant blocks and paired tool results | Inferred model/tool starts; recorded transcript boundaries |
| OpenCode | SQLite V1/V2 (`message`/`part` or `session_message`); legacy JSON storage | Recorded model and tool timestamps |
| Gemini CLI | `~/.gemini/tmp/*/chats/session-*.json[l]`, including patches and rewinds | Inferred intervals from message/tool event boundaries |

The adapters follow upstream schemas:
[OpenCode V1](https://github.com/anomalyco/opencode/blob/dev/packages/schema/src/v1/session.ts),
[OpenCode V2](https://github.com/anomalyco/opencode/blob/dev/packages/schema/src/session-message.ts),
[Gemini CLI](https://github.com/google-gemini/gemini-cli/blob/main/packages/core/src/services/chatRecordingTypes.ts),
[Oh My Pi](https://github.com/can1357/oh-my-pi/blob/main/packages/coding-agent/src/session/session-manager.ts).
Claude transcript records are checked against local Claude Code sessions;
its [hook documentation](https://code.claude.com/docs/en/hooks) describes
transcript locations. Separate subagent transcripts are not joined: the parent
agent's task/delegation tool is included, but its children's internal work is
not yet linked. No hooks or client changes are required.

Polling uses a soft 8 MiB reading budget and up to 200 recent files per store.
JSONL files are read incrementally; incomplete lines wait for the next poll.
OpenCode SQLite is opened read-only, including WAL changes, and unchanged
stores are skipped. SQLite message/part updates are read incrementally in bounded
batches on separate read-only connections; oversized conversations resume on the
next poll. OpenCode retains up to 256 messages and an 8 MiB body window per
session; individual rows above 8 MiB export metadata only. Legacy JSON and
Gemini snapshot reads cap at 32 MiB; Gemini retains the current prompt plus
255 recent messages.
Completed observations are deduplicated with bounded metadata. Claude's final
response and Gemini's final token records may take one extra poll to settle.

Inferred timings carry `magpie.timing.source=inferred`. Tool arguments/results
and user input/output follow **Include request and response bodies**, masking
secrets and respecting the whole-body preference. Session directory/title
metadata, credentials and account names are not exported.

Conversation tracing replaces standalone gateway traces for these local
clients to avoid duplicate token usage. Unsupported clients and remote
forwarded requests retain gateway tracing; the local usage ledger is unchanged.
This mode shows agent activity rather than gateway retries or provider-routing
details. It reads this computer's local sessions, including calls made without
Magpie as the proxy.

Metrics group duration and input/output token histograms by agent, provider,
model, operation and error status. Prompt/reply text is exported only when
**Include request and response bodies** is enabled (`MAGPIE_OTEL_BODIES=true`);
secrets are masked and each body is limited to 256 KiB unless **Include the
whole bodies** is enabled. Conversation IDs are exported only with
conversation tracing; provider account names/keys are never exported.

Whole bodies increase transient memory and allocation costs during read-back,
secret scrubbing and JSON encoding; a 32 MiB request and reply can roughly
double total allocations compared with truncated export. The export limits
queued bodies to 128 MiB, but this does not bound in-flight processing memory.
Recent calls retain only the first 256 KiB of each body.

Export runs in the background with a bounded queue (128 records) and batches
of up to 32 records, flushed every five seconds. A full queue drops telemetry
without delaying gateway requests. Network errors and HTTP 429/502/503/504
are retried up to two times, with retry delays capped at 60 seconds; other
errors and partial rejection are logged
without the collector's response body. Each HTTP attempt times out after
three seconds. Graceful gateway shutdown allows at most three seconds to
drain; `magpie serve` currently exits on SIGTERM without draining, so its last
batch may be lost. This is best-effort export; local usage records remain
available if it fails.
Requests follow magpie's proxy setting, with loopback collectors going direct.
Queued records are discarded if export is disabled or the destination or
credentials change before sending. Redirects are not followed.
Backups without keys omit OTLP headers; restoring one preserves this machine's
headers only when the collector endpoint is unchanged.

## Mirrors

The Plugins page's **Mirrors in China** (国内镜像) switch has what that page
downloads asked of a mirror in China first and of its official address
after: npm's registry (plugin packages, their versions and search) at
`registry.npmmirror.com`, Bun's releases at npmmirror's binary mirror, and
the plugin list at jsDelivr (`cdn.jsdelivr.net/gh/magpie-community/plugins`).
Each is a copy of the very file — Bun's zip is still checked against Bun's
SHA-256s, and a package against the integrity npm gave — and what the
mirror hasn't copied yet is asked of the official address. npm's weekly
download counts stay npmjs's own. `bun add` is pointed at npmmirror too,
unless `BUN_CONFIG_REGISTRY` or `NPM_CONFIG_REGISTRY` names a registry
already. The switch never sends anything through a proxy such as
gh-proxy.

Mirror fallback is off by default. Set `MAGPIE_MIRRORS=on` to use it. When
it is on, the official source is tried first; a mirror is only tried after
a network error, HTTP 429, HTTP 5xx, or GitHub's rate-limit 403. The
defaults are `registry.npmmirror.com` for npm and `gh-proxy.com` for
GitHub. `MAGPIE_NPM_REGISTRY` and `MAGPIE_GITHUB_MIRROR` replace them.

Only two downloads are checked against a checksum that does not come from
the mirror: Magpie's own update assets (their SHA-256 comes from the
unmirrored update feed) and Bun's zip. Bun's default version carries its
SHA-256s in the code, so it can be downloaded through a mirror even when
Bun's official `SHASUMS256.txt` is unreachable; a newer Bun still needs
that official file. Skill tarballs always use the official source, and the plugin-market
registry does unless the Mirrors in China switch is on (jsDelivr, above). npm metadata, plugin README/search results and
version checks are not checksummed; a mirror can report a different
version.

The official source and a mirror share one request budget, so a hanging
official source doesn't make the whole wait twice as long.

Requests that carry credentials, such as an `Authorization`, `Cookie`,
API key, or URL userinfo/query token, never use a mirror; they go to the
official source alone.

```sh
MAGPIE_MIRRORS=on \
MAGPIE_NPM_REGISTRY=https://registry.example.com \
MAGPIE_GITHUB_MIRROR=https://gh.example magpie serve
```

## Files

magpie's folder, `~/.config/magpie`, is kept flat on purpose: backups,
sync, Docker volume mounts and older magpies all find each file by its
name there (#2028). Below, each file says what it is for. A `.lock`
beside a file is held while it is written, across magpies; a `.bak` is
the copy made before a write.

What you set:

- `settings.json` — Settings
- `providers.json` — your providers, keys included (0600); `pluginLeft` in it
  keeps a removed plugin's model picks until it is added again
- `profiles.json` — saved profiles
- `library.json`, `library/` — the library: its instructions, skills, MCP
  servers and hooks, and the agents each goes to
- `plugins.json`, `plugins/` — the plugins added, and their packages
- `plugins.yml` — which built-in plugins are on, and each one's config
- `caller-keys.json` — the gateway keys you made for other callers (0600)

Accounts and sign-ins:

- `logins.json` — the subscription accounts signed in through magpie
  (0600)
- `plugin-auth.json` — the plugins' sign-ins (0600)
- `mcp-signins.json` — sign-ins to MCP servers (0600)
- `cli-identity.json` — who an agent's own CLI last said was signed in
  (`cursor-agent about`), so a start doesn't wait for the CLIs
- `redact.key` — the key behind the placeholders privacy masking puts in
  place of what it hides

What magpie set on your agents:

- `stash.json` — values magpie replaced, restored on switch-back
- `applied.json` — what magpie last set on each agent, and when
- `migrations.json` — which subscriptions moved to their plugin
  ([Built-in subscriptions that a plugin serves](../AGENTS.md))

The gateway and usage:

- `usage.jsonl` — one line per request: the Usage page's numbers
- `routing/` — each day's routing attempts (`<date>.jsonl`), the Routing
  page's history
- `affinity.json` — which key or account answered each conversation, so a
  restart keeps it there while the vendor has it cached
- `served.json` — when each account and key last answered
- `quotas.json` — the last allowance each account reported
- `quota-history.json`, `balance-history.json` — allowance and balance
  readings over time, for the Usage page's charts
- `usage-alerts.json` — the usage alerts already shown, so a restart
  doesn't show them again
- `client-shapes.json` — the headers the gateway last let through for each
  client, for a test from the CLI
- `gateway-sessions-hidden.json` — gateway sessions you deleted

Kept for the window:

- `icons/` — providers' icons, fetched once
- `plugin-icons.json`, `plugin-providers.json`, `plugin-updates.json` —
  plugins' icons, the providers each serves, and what the last look at
  npm found for them (updates waiting, and the ones made)
- `trash/` — deleted sessions, until you empty it or restore them
- `last-version`, `whatsnew-quiet-day` — the version that ran last, for
  What's new, and the day you said not to show it again
- `start-menu-shortcut` — the Start menu shortcut was made (Windows)
- `install-id`, `stats-sent` — the [user count](#counting-users)'s
  random id and the last day it went

In `~/.cache/magpie`:

- `bun/<version>/magpie-bun` — the Bun plugins run on, named
  so that a proxy app's PROCESS-NAME rule can match it (`magpie-bun.exe` on
  Windows)
- `models.json` — models.dev catalog (OpenCode's cache at
  `~/.cache/opencode/models.json` is used when present)
- `models/<provider>.json` — model lists fetched from vendors
- `partner-counts.json` — partner counts not yet sent (at most
  14 days)

`XDG_CONFIG_HOME` and `XDG_CACHE_HOME` are respected.

## Counting users

Once a day, a running magpie (the app, or `magpie serve`) sends one event to
PostHog so we know how many people use it: a random id made up on your
computer (`~/.config/magpie/install-id`, made the first time the event is
sent), magpie's version, and your system and architecture. The day it last
went is in `~/.config/magpie/stats-sent`, so a day is sent once; the
event is tried at start and then every hour.

With it goes what magpie is used with, so we know which agents, providers
and models to look after first, by magpie's own ids only:

- the agents connected to magpie (`claude`, `codex`); an omp profile is
  only `omp`;
- the providers that are on: a preset's id (`deepseek`), a subscription's
  (`codex`, `copilot`), a community plugin's (`plugin:kiro`); a provider you
  added yourself is only `custom`, another plugin only `plugin`;
- the models the connected agents are set to, as the provider's id and the
  vendor's model id (`deepseek/deepseek-v4`); on a key's provider, a model
  id that [models.dev](https://models.dev) doesn't list (a local model, a
  fine-tune) is only `<provider>/other`; one on a provider of your own is
  only `custom`, a routing group only `group`;
- how many of each, and how many routing groups you have.
- for each partner (a sponsor listed first in the add sheet), by its id, how
  many times a day the add sheet showed it, its row was opened, its key
  page was opened and a provider was added from it (at most 20 of each,
  5 adds), kept in `~/.cache/magpie/partner-counts.json` for at most 14
  days and sent the day after as `magpie partner` events.

No names, base URLs, accounts, keys, prompts or usage go. Turn that part off
in Settings → Privacy → Share the agents, providers and models I use; the
day's event then has the id, version and system only. Turn it all off in
Settings → Privacy → Count me as a user, or with `DO_NOT_TRACK=1` or
`MAGPIE_NO_STATS=1`. Builds from source never send it. The code is
[internal/stats](../internal/stats/stats.go). Deleting `install-id` makes a
new one at the next send. The [Privacy page](https://usemagpie.ai/docs/privacy)
shows a whole batch and everything else magpie sends by itself.

Partners are also counted where usemagpie.ai sees them, whatever these
settings say, as any website sees its visits. The partner list
(`/api/partners`) gives each partner's website and key page as a
`https://usemagpie.ai/go/<id>/site|keys[/<region>]` link, which sends a
`magpie partner go` event (the partner, which link, the region, and the
country Cloudflare gives) before sending the browser on; and one in ten
fetches of the list sends a `magpie partners fetch` event with weight 10
and the country, while a partner is listed. Each event has an id of its
own: no install id, address or account goes with it. The code is
[site/worker.js](../site/worker.js).

A plugin's, an import link's and the library's icons sit on hosts their
authors picked. magpie fetches each through
`https://usemagpie.ai/api/icon?url=<https URL>`, so the host sees
Cloudflare's address and never the user's. The worker sends only
`Accept: image/*` and its own User-Agent, passes on a PNG, JPEG, GIF, WebP,
ICO or SVG of at most 1 MB and refuses anything else, and counts and keeps
nothing. The code is [internal/iconproxy](../internal/iconproxy/iconproxy.go)
and `icon` in [site/worker.js](../site/worker.js).

## Community

Questions, setups worth sharing, ideas, bugs: come talk to us and other
magpie users on [Discord](https://discord.gg/vGSnD3ZKQF). Issues and pull
requests are welcome here too.

## License

MIT. See [LICENSE](../LICENSE).
