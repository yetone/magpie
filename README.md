<div align="center">

<a href="https://usemagpie.ai"><img src="site/public/img/icon-256.png" width="120" alt="magpie"></a>

# magpie

### Every agent's model. One place.

Claude Code on Kimi, Codex on DeepSeek, Gemini CLI on GLM, OpenCode on your ChatGPT plan.<br>
Switch them from the menu bar. One local gateway serves them all, and it moves to another account when a quota runs out.

[![Release](https://img.shields.io/github/v/release/yetone/magpie-releases?label=release&color=111111)](https://github.com/yetone/magpie-releases/releases/latest) [![Stars](https://img.shields.io/github/stars/yetone/magpie?style=flat&color=111111)](https://github.com/yetone/magpie/stargazers) [![Discord](https://img.shields.io/badge/Discord-join-5865F2?logo=discord&logoColor=white)](https://discord.gg/vGSnD3ZKQF) [![License](https://img.shields.io/badge/license-MIT-111111)](LICENSE)<br>
![macOS](https://img.shields.io/badge/macOS-000000?logo=apple&logoColor=white) ![Windows](https://img.shields.io/badge/Windows-0078D4?logo=windows&logoColor=white) ![Linux](https://img.shields.io/badge/Linux-FCC624?logo=linux&logoColor=black) ![Docker](https://img.shields.io/badge/Docker-2496ED?logo=docker&logoColor=white) ![Termux](https://img.shields.io/badge/Termux-000000?logo=android&logoColor=white)

**[Download](https://usemagpie.ai)** · **[Docs](https://usemagpie.ai/docs/start)** · **[Reference](docs/reference.md)** · **[Discord](https://discord.gg/vGSnD3ZKQF)** · **English** · [简体中文](README.zh-CN.md)

<br>

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="site/public/img/agents-dark.png">
  <img src="site/public/img/agents-light.png" width="900" alt="magpie's Agents page: Claude Code on Kimi K3, Codex on DeepSeek V4 Pro, Gemini CLI on GLM-5.3, each picked from one list">
</picture>

</div>

<br>

## Why magpie

You probably use more than one coding agent. Each one keeps its model in its own file, in its own format, with its own keys and base URLs. Each one also has its own idea of which vendors it supports. A subscription you pay for works in one agent and nowhere else. When it runs out at 3 pm, you start editing config files.

magpie puts all of it in one place:

<table>
<tr>
<td width="33%" valign="top">

**🎛 One screen for every agent**<br>
Over 35 agents in one list. Click a model and pick another. magpie changes only that one key in the agent's own config file. Comments, ordering and formatting stay as they were.

</td>
<td width="33%" valign="top">

**🔌 One gateway for every API**<br>
`127.0.0.1:3425` speaks OpenAI Chat, OpenAI Responses, Anthropic Messages and Gemini. It translates between them, streaming, tool calls and reasoning included.

</td>
<td width="33%" valign="top">

**🔀 Routing that keeps going**<br>
Put several models from several providers in a routing group. When one hits a rate limit or runs out of quota, the next one answers. Your agent never sees the error.

</td>
</tr>
<tr>
<td valign="top">

**🔑 Subscriptions you can share**<br>
Your Claude, ChatGPT, Copilot, Gemini or Grok sign-in becomes a provider that every other agent can use. There is no key to copy.

</td>
<td valign="top">

**🧩 Plugins**<br>
OpenCode auth plugins and pi provider packages from npm run in magpie as they do in their own apps. Any plan a plugin signs in to works in every agent. A plugin can also be gateway middleware that reads and rewrites every request and reply.

</td>
<td valign="top">

**📊 Usage and cost tracking**<br>
See tokens, cache hits, cost at list price, balances and quota windows for every provider and account. You can also set a limit for each key.

</td>
</tr>
</table>

<br>

## Pick any model for any agent

Click a value and a filtered list opens. It holds every model of every provider you added, as `provider/model`. Pick one and the agent's config file is rewritten safely and atomically. Use **Profiles** to save the setup of every agent under one name ("Budget", "Focus") and switch them all at once.

magpie also lives in the **menu bar**. There is a tray panel on macOS, Windows and Linux, a full window, a **TUI** (`magpie tui`), a **web UI** (`magpie web`) and a plain **CLI**.

<table>
<tr>
<td width="50%">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset="site/public/img/panel-dark.png">
  <img src="site/public/img/panel-light.png" alt="The menu bar panel">
</picture>
</td>
<td width="50%">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset="site/public/img/picker-dark.png">
  <img src="site/public/img/picker-light.png" alt="The model picker">
</picture>
</td>
</tr>
</table>

## Add providers with one field

Pick a preset and paste a key. That's it. The model list comes from the vendor itself, with names and reasoning levels filled in from [models.dev](https://models.dev), so a model released this morning shows up on the next refresh. No model list is built into magpie.

**Presets include** Anthropic · OpenAI · Google Gemini · DeepSeek · Kimi · Zhipu GLM · MiniMax · StepFun · Qwen · Baidu Qianfan · Tencent Cloud · Huawei Cloud MaaS · Volcengine Ark · Mistral · Groq · xAI · OpenRouter · Together · Fireworks · SiliconFlow · NVIDIA NIM · ModelScope · Ollama · LM Studio … and any OpenAI-compatible or Anthropic-compatible URL.

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="site/public/img/add-dark.png">
  <img src="site/public/img/add-light.png" width="900" alt="Add provider: subscriptions to sign in to, vendors, relays and local servers">
</picture>

Already set up somewhere else? **Import** reads the providers you set up in CC Switch, Claude Code, Codex and Alma. **[Add to magpie](https://usemagpie.ai/docs/import)** links let a provider's website hand its config to magpie in one click.

## Routing that keeps going

A **routing group** is several models that an agent picks as one: `group/daily-coding`. The gateway spreads requests over every member's keys and accounts:

| Mode | What it does |
| --- | --- |
| `smart` | Of the subscriptions with quota left, use the one whose allowance renews soonest, so less is lost at the reset |
| `order` | Use the first model until it can't answer, then the next |
| `rotate` | Move to the next member on each turn |
| `usage` | Use the least-used member first |
| `pace` | Use each account's week up evenly until its reset |

Conversations **stay with the account that answered them** while the vendor's prompt cache is still worth keeping. **Intent routing** goes further: a small model you choose reads each new turn, so tests can go to the strong model and quick questions to the fast, cheap one. Groups can contain other groups. The Routing tab shows each decision live.

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="site/public/img/routing-dark.png">
  <img width="760" src="site/public/img/routing-light.png" alt="The Routing view: four agents through magpie to seven providers, live">
</picture>

<table>
<tr>
<td width="50%">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset="site/public/img/intent-trace-dark.png">
  <img src="site/public/img/intent-trace-light.png" alt="An intent-routed turn, explained step by step">
</picture>
</td>
<td width="50%">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset="site/public/img/nested-routing-dark.png">
  <img src="site/public/img/nested-routing-light.png" alt="A routing group inside a routing group">
</picture>
</td>
</tr>
</table>

→ [Intent routing, in depth](https://usemagpie.ai/docs/intent)

## Sign in once, use it everywhere

An agent you're signed in to is a subscription with models behind it, so magpie offers it as a provider. Several accounts per subscription are supported, and magpie fails over between them.

- **Claude**: drives the real local `claude` binary and bridges your agent's tools over MCP
- **Codex / ChatGPT**: your ChatGPT plan's models in every other agent
- **GitHub Copilot**, **Gemini (Code Assist)**, **Antigravity**, **Grok (SuperGrok)**, **Devin**, **Cursor** and others
- **Any OpenCode auth plugin or pi package** from npm:

```sh
magpie plugin add opencode-gemini-auth   # an OpenCode plugin from npm
magpie plugin add pi-antigravity         # a pi package, the same way
magpie plugin login google-plugin        # its own sign-in flow, in magpie
```

magpie runs plugins on [Bun](https://bun.sh), which it downloads the first time a plugin needs it. A plugin signs in, lists its models and makes each request. Agents use its models like any other provider's. Browse the community plugins at **[magpie-community/plugins](https://github.com/magpie-community/plugins)**, or [write your own](https://usemagpie.ai/docs/plugins).

A plugin can also be **gateway middleware**: JavaScript run inside magpie's gateway on what every agent sends and gets back, whatever the provider. `onRequest` can rewrite a request or turn it away, `onEvent` sees each streamed event, and `onResponse` sees a whole reply. It runs in-process, so an event costs about a microsecond, and a hook that throws or runs too long leaves the request as it was.

```js
// alias.middleware.js — magpie plugin add ./alias.middleware.js
export function onRequest(body, ctx) {
  if (body.model === "fast") return { ...body, model: "deepseek/deepseek-chat" };
}
```

Ready-made middleware, most of it what [New API](https://github.com/QuantumNous/new-api) does for its channels, with the same JSON, is under Plugins › Discover › Gateway middleware:

| Package | What it does |
|---|---|
| `param-override` | New API's `param_override`: set, delete, move or rewrite request fields, under conditions, or turn a request away |
| `model-map` | New API's `model_mapping`: send a model under another name; replies keep the name asked for |
| `system-prompt` | Your system prompt on every request, or some agents' or models' |
| `word-guard` | New API's sensitive-word filter: turn away or mask words in what users send, and in replies |
| `think-tags` | Take `<think>…</think>` out of replies, or put `reasoning_content` into them |

```sh
magpie plugin add @magpie-community/middleware-model-map
magpie plugin options model-map '{"mapping": {"fast": "deepseek/deepseek-chat"}}'
```

See [Gateway middleware](https://usemagpie.ai/docs/plugins#middleware).

## Usage and cost tracking

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="site/public/img/usage-dark.png">
  <img src="site/public/img/usage-light.png" width="900" alt="The Usage page: balances, tokens, cache hit rate and cost">
</picture>

- Tokens, cache reads and writes, reasoning tokens and calls, with **cost at list price**. You can set your own prices for each model.
- **Balances and quota windows** for every key, plan and subscription account (`magpie quota`). The **reset reminder** warns you before a window renews with much of it unused.
- Usage by **session**: each agent conversation with its cost and title, and a command to resume it.
- Usage by **account** and by **upstream key**, so you can check a vendor's bill line by line.
- **OTLP export** to your own observability stack.

## Share one magpie

- **Share it on your network.** Turn on *Share on local network*, then create a named **gateway key** for each client, each with its own daily, weekly or monthly token and cost limit.
- **Remote magpie.** A laptop can use the providers, accounts and routing groups of the magpie on your desktop, while still wiring its own agents.
- **Docker.** Run `ghcr.io/yetone/magpie` on a server or a NAS, and manage it from the web UI.
- **Sync.** Back up to a file, or keep machines in sync over WebDAV (Nutstore, Nextcloud…) or S3.

## Library: instructions, MCP servers and skills

Write your instructions, MCP servers and skills once. magpie writes them into each agent's own files in that agent's own format. It removes only what it wrote, so everything else in those files stays as it was.

## Supported agents

<table>
<tr><td>

Claude Code · Claude Desktop · Codex · Gemini CLI · OpenCode · OpenChamber · MiMo Code · Pi · Aside · OmO · Goose · Cursor CLI · Zed · VS Code Chat · VS Code Insiders · VSCodium Chat · JetBrains Air · Copilot CLI · Crush · DeepSeek Harness · Reasonix Studio · Command Code · fx · oh-my-pi · Devin · Hermes Agent · Mister Morph · Kimi Code · Muse Code · Empryo · MiniMax Code · Droid · Cline · Qoder · Qoder CN · Grok Build · ZCode · WorkBuddy · CodeBuddy Code · T3 Code · OpenHanako · AtomCode · Alma

</td></tr>
</table>

magpie shows only the agents installed on your machine. Anything else that takes a base URL can use the gateway too:

```sh
export OPENAI_BASE_URL=http://127.0.0.1:3425/v1     OPENAI_API_KEY=magpie
export ANTHROPIC_BASE_URL=http://127.0.0.1:3425     ANTHROPIC_API_KEY=magpie
export GOOGLE_GEMINI_BASE_URL=http://127.0.0.1:3425 GEMINI_API_KEY=magpie
```

### Reasonix Studio

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

### VSCodium Chat

VSCodium's Chat features are disabled by default. To use the Chat model
picker with magpie, set `"chat.disableAIFeatures": false` in VSCodium's
settings and add the `defaultChatAgent` and `trustedExtensionAuthAccess`
entries required by [VSCodium's Copilot guide](https://github.com/VSCodium/vscodium/blob/master/docs/ext-github-copilot.md)
to VSCodium's `product.json`. The guide also explains how to install a
compatible GitHub Copilot Chat extension, since the Open VSX registry does
not normally provide Microsoft's extension. Restart VSCodium, or run
**Developer: Reload Window**, after changing these files. Magpie's models
then appear under the `magpie` custom endpoint group.

### Zed-compatible Agent paths

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

## Quick start

**1. Install.** Download the app from **[usemagpie.ai](https://usemagpie.ai)**, or run:

```sh
curl -fsSL https://usemagpie.ai/install.sh | sh
```

<sub>Mac builds are signed and notarised. Every build updates itself. Behind a firewall, use `--proxy` or `--mirror`. You can also use `go install github.com/yetone/magpie@latest`, or the [Docker image](docs/reference.md#docker).</sub>

**2. Add a provider.** Open magpie, then go to **Providers → Add provider**. Pick a preset, or sign in with a subscription.

**3. Pick a model** for each agent on the **Agents** page. Start a new agent session and it uses the new model.

Or do the same from the terminal:

```sh
magpie provider add deepseek sk-…              # a preset needs only the key
magpie claude deepseek/deepseek-v4-pro          # Claude Code on DeepSeek
magpie codex moonshot/kimi-k2.5                 # Codex on Kimi
magpie group add "Opus anywhere" models=claude/claude-opus-5-5,copilot/claude-opus-5.5 routing=smart
magpie claude group/opus-anywhere               # fails over between subscriptions
magpie save work && magpie use work             # profiles
magpie quota                                    # what's left on every plan
magpie tui                                      # the whole thing, in a terminal
```

## Documentation

- **[Get started](https://usemagpie.ai/docs/start)**: the guided tour
- **[Reference](docs/reference.md)**: every agent, provider option, gateway endpoint, CLI command and file
- **[Plugins](https://usemagpie.ai/docs/plugins)**: use plugins and write your own
- **[Intent routing](https://usemagpie.ai/docs/intent)**: route each turn by what it asks for
- **[Import links](https://usemagpie.ai/docs/import)**: "Add to magpie" buttons for provider websites

## Community

Questions, ideas or a model that won't show up? Join us on **[Discord](https://discord.gg/vGSnD3ZKQF)** or [open an issue](https://github.com/yetone/magpie/issues).

If magpie saves you from editing one more config file, **a ⭐ helps others find it.**

<a href="https://star-history.com/#yetone/magpie&Date">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/svg?repos=yetone/magpie&type=Date&theme=dark">
    <img src="https://api.star-history.com/svg?repos=yetone/magpie&type=Date" width="600" alt="Star history">
  </picture>
</a>

## License

[MIT](LICENSE)
