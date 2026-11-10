<div align="center">

<a href="https://usemagpie.ai"><img src="site/public/img/icon-256.png" width="112" alt="magpie"></a>

# magpie

### Every agent's model. One place.

**Claude Code on Kimi. Codex on DeepSeek. Gemini CLI on GLM. OpenCode on your ChatGPT plan.**<br>
Switch any of them from the menu bar. One local gateway serves them all,<br>
and when a quota runs out, it quietly moves on to your next account.

[![Release](https://img.shields.io/github/v/release/yetone/magpie-releases?label=release&color=111111)](https://github.com/yetone/magpie-releases/releases/latest) [![Stars](https://img.shields.io/github/stars/yetone/magpie?style=flat&color=111111)](https://github.com/yetone/magpie/stargazers) [![Discord](https://img.shields.io/badge/Discord-join-5865F2?logo=discord&logoColor=white)](https://discord.gg/vGSnD3ZKQF) [![License](https://img.shields.io/badge/license-MIT-111111)](LICENSE)<br>
![macOS](https://img.shields.io/badge/macOS-000000?logo=apple&logoColor=white) ![Windows](https://img.shields.io/badge/Windows-0078D4?logo=windows&logoColor=white) ![Linux](https://img.shields.io/badge/Linux-FCC624?logo=linux&logoColor=black) ![Docker](https://img.shields.io/badge/Docker-2496ED?logo=docker&logoColor=white) ![Termux](https://img.shields.io/badge/Termux-000000?logo=android&logoColor=white)

Thanks to [Kimi](https://usemagpie.ai/go/kimi/readme) for supporting magpie and the open source community.

<sub>Sponsors</sub>

<a href="https://usemagpie.ai/go/apinebula/readme"><picture><source media="(prefers-color-scheme: dark)" srcset="site/public/img/sponsor-apinebula-dark.svg"><img src="site/public/img/sponsor-apinebula.svg" width="200" height="50" alt="APINEBULA"></picture></a>&emsp;&emsp;<a href="https://usemagpie.ai/go/zerotoken/readme"><picture><source media="(prefers-color-scheme: dark)" srcset="site/public/img/sponsor-zerotoken-dark.svg"><img src="site/public/img/sponsor-zerotoken.svg" width="200" height="50" alt="零词元"></picture></a>&emsp;&emsp;<a href="https://usemagpie.ai/go/apikeyfan/readme"><picture><source media="(prefers-color-scheme: dark)" srcset="site/public/img/sponsor-apikeyfan-dark.svg"><img src="site/public/img/sponsor-apikeyfan.svg" width="200" height="50" alt="APIKEY.FAN"></picture></a><br>
<a href="https://usemagpie.ai/go/wandapi/readme"><picture><source media="(prefers-color-scheme: dark)" srcset="site/public/img/sponsor-wandapi-dark.svg"><img src="site/public/img/sponsor-wandapi.svg" width="200" height="50" alt="WandAPI"></picture></a>&emsp;&emsp;<a href="https://usemagpie.ai/go/aigocode/readme"><picture><source media="(prefers-color-scheme: dark)" srcset="site/public/img/sponsor-aigocode-readme-dark.png"><img src="site/public/img/sponsor-aigocode-readme.png" width="200" height="50" alt="AIGoCode"></picture></a>

**[Download](https://usemagpie.ai)** &nbsp;·&nbsp; **[Docs](https://usemagpie.ai/docs/start)** &nbsp;·&nbsp; **[Reference](docs/reference.md)** &nbsp;·&nbsp; **[Plugins](https://github.com/magpie-community/plugins)** &nbsp;·&nbsp; **[Discord](https://discord.gg/vGSnD3ZKQF)** &nbsp;·&nbsp; **English** · [简体中文](README.zh-CN.md)

<br>

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="site/public/img/agents-dark.png">
  <img src="site/public/img/agents-light.png" width="900" alt="magpie's Agents page: Claude Code on Kimi K3, Codex on DeepSeek V4 Pro, Gemini CLI on GLM-5.3, each picked from one list">
</picture>

</div>

<br>

## Is magpie for you?

Each agent keeps its model in its own file, in its own format, with its own keys and base URLs. A subscription you pay for works in one agent and nowhere else. magpie puts all of that behind one app and one local gateway:

- **Pick any model for any agent** from one list. magpie edits the one key that matters in the agent's own config.
- **Use one provider everywhere**: an API key, a local model, or the Claude, ChatGPT, Copilot, Gemini or Grok plan you're signed in to.
- **Keep going when a quota runs out**: a provider's next account takes over, and a routing group moves on to another model or provider, so the agent never sees the error. With one account and no group there is nothing to move to, and the error reaches the agent.
- **Keep your secrets off the vendor's servers**: once you turn masking on in **Settings → Privacy** (it's off by default), keys, passwords and emails in a prompt are masked on your computer before the request leaves it, and put back in the reply.

It's for you if you use more than one agent, more than one provider, or more than one account. If you use one agent on its vendor's own plan and that's enough, you don't need it.

Runs on macOS, Windows and Linux (menu bar app, window, TUI, web UI and CLI), in Docker, and on Termux.

## Quick start

**1 · Install.** Download the app from **[usemagpie.ai](https://usemagpie.ai)**, or run:

```sh
curl -fsSL https://usemagpie.ai/install.sh | sh
```

<sub>Mac builds are signed and notarised, and every build updates itself. On Linux the script installs the desktop app, and on a desktop without WebKitGTK 4.1 installs it first with your package manager (sudo asks for your password); with no desktop, or no terminal to ask at, it installs the terminal build and says which package to add. `--gui` or `--tui` chooses. Behind a firewall, use `--proxy` or `--mirror`. Or `go install github.com/yetone/magpie@latest`, or the [Docker image](https://usemagpie.ai/docs/docker). Remove magpie with `curl -fsSL https://usemagpie.ai/install.sh | sh -s -- --uninstall`: it puts your agents back as they were, then takes off what the script installed (with magpie deleted already, it downloads one to put them back); your settings in `~/.config/magpie` stay unless you add `--purge`.</sub>

**2 · Add a provider.** Open magpie and go to **Providers → Add provider**. Pick a preset and paste a key, or sign in with a subscription.

**3 · Pick a model** for each agent on the **Agents** page. Start a new session and it's on the new model.

Or do it all from the terminal:

```sh
magpie provider add deepseek sk-…              # a preset needs only the key
magpie claude deepseek/deepseek-v4-pro          # Claude Code on DeepSeek
magpie codex moonshot/kimi-k2.5                 # Codex on Kimi
magpie group add "Opus anywhere" models=claude/claude-opus-5-5,copilot/claude-opus-5.5 routing=smart
magpie claude group/opus-anywhere               # fails over between subscriptions
magpie save work && magpie use work             # profiles
magpie quota                                    # what's left on every plan
magpie settings set theme dark                  # every Settings switch, from the terminal
magpie tool generate_image "a magpie, watercolour" -o hero.png   # magpie's image, vision and search tools, called directly
magpie tui                                      # the whole thing, in a terminal
```

magpie finds the subscriptions your agents are signed in to by reading their sign-ins (Claude Code's and Codex's files, the Keychain). To keep it from reading them, turn off Settings → Privacy → *Subscription accounts*, or run `magpie accounts discovery off` (`on` turns it back on). `MAGPIE_NO_ACCOUNTS=1` turns it off for one run without changing the saved setting. API-key providers work either way.

## Supported agents

<table>
<tr><td>

Claude Code · Claude Desktop · Claude Science · Codex · Gemini CLI · Antigravity CLI · OpenCode · OpenChamber · MiMo Code · DevEco Code · Pi · oh-my-pi · StepCode · MothX · PenguinHarness · Aside · OmO · Prime Agent · mu · Goose · Cursor CLI · Cursor Private Inference · Zed · VS Code Chat · VS Code Insiders · VSCodium Chat · Zoo Code · JetBrains Air · Air (JetBrains IDEs) · Copilot (JetBrains) · Copilot CLI · Crush · DeepSeek Harness · Reasonix Studio · Command Code · fx · Devin · Hermes Agent · Mister Morph · Kimi Code · jcode · Qwen Code · Muse Code · Empryo · nanobot · miniQ · Paimon · MiniMax Code · Droid · Cline · Qoder · Qoder CN · Grok Build · Grok App · ZCode · WorkBuddy · WorkBuddy AI · CodeBuddy · Pencil · OpenPencil · OpenBot · Craft Agents · Tuanjie Cowork · NewMax · LiveAgent · Raycast · Kiro Crew · OpenMausBot · T3 Code · OpenHanako · AtomCode · Snow CLI · Alma · Cindy · Lody · AionUi · Cherry Studio · Ante · AstrBot · QwenPaw · OpenClaw · Avibe

</td></tr>
</table>

magpie shows only the agents installed on your machine; setup notes for some of them are in the [reference](docs/reference.md#notes-on-some-agents). Anything else that takes a base URL can use the gateway too:

```sh
export OPENAI_BASE_URL=http://127.0.0.1:3425/v1     OPENAI_API_KEY=magpie
export ANTHROPIC_BASE_URL=http://127.0.0.1:3425     ANTHROPIC_API_KEY=magpie
export GOOGLE_GEMINI_BASE_URL=http://127.0.0.1:3425 GEMINI_API_KEY=magpie
```

## How it fits together

The gateway at `127.0.0.1:3425` speaks OpenAI Chat, OpenAI Responses, Anthropic Messages and Gemini, and translates between them, streaming, tool calls and reasoning included.

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="site/public/img/architecture-dark.svg">
  <img src="site/public/img/architecture.svg" alt="How magpie fits together: your agents send requests to the magpie gateway at 127.0.0.1:3425, which routes them through routing groups and plugins to your API keys, subscriptions and local models.">
</picture>


## What it does

### Any model, any agent

Click a value and a searchable list opens: every model of every provider you added, as `provider/model`. The agent's config is rewritten atomically, and its comments, order and formatting stay as they were. **Profiles** save every agent's setup under one name ("Budget", "Focus") and switch them all back in one move. Each agent can also have its own short list of models; a profile keeps that list too.

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

### Privacy masking

Agents put what they read into the prompt: an `.env`, a config file, a log with a token in it. With masking on, magpie swaps those values for placeholders on your computer before a request leaves it, and swaps them back in the reply. Your agent and your files keep the real values; the vendor never sees them.

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="site/public/img/masking-dark.png">
  <img src="site/public/img/masking-light.png" width="900" alt="Insights › Masking: 5,812 values kept from vendors in 7 days, flowing from Claude Code and Codex through magpie to OpenAI, Kimi and DeepSeek, and each request's masked values">
</picture>

| | |
| :-- | :-- |
| **Found two ways** | Rules for what is certain (API keys, private keys, tokens, emails, and card and ID numbers whose checksum adds up), and, if you want it, a model that runs on this computer for the names, addresses and passwords no rule can find (a 1.6 GB download, on the CPU) |
| **Put back everywhere** | A value becomes a placeholder such as `{{API_KEY_k3v9x2mq}}`, and every placeholder in the reply turns back into its value: streamed text and tool-call arguments too, so a command the agent runs has the real key |
| **Nothing breaks** | A value gets the same placeholder every time, so the conversation, the prompt cache and thinking signatures stay valid. A value masked once is masked wherever it shows up later |
| **Never on disk** | Values are held in memory only. The record keeps each value's kind, length and first characters. If masking is on and can't run, the request is held back (503), never sent as written |
| **You can check it** | **Insights › Masking** lists every request it changed, with what the vendor got instead, side by side, and **Try it** in Settings shows what a text would become |
| **Your rules** | Secrets and personal data each on or off, finer kinds (passport, IBAN, MAC, public IP…), your own words and patterns, and whether a value found is masked, only warned about, or held back (**Mask**, **Warn**, **Block**) |

The cost: the model sees `{{API_KEY_…}}`, not the key, so a task about the value itself (why is this token refused?) can't be done on it. Put such values under **Never mask**. Rules can be wrong now and then; the Masking page is where you'd see it. It's off until you turn it on in **Settings → Privacy**. Emails at names kept for examples (`example.com`, `.test`, `.invalid`) are left as written.

### Providers

Pick a preset, paste a key. Model lists come from the vendor, with names and reasoning levels filled in from [models.dev](https://models.dev), so a model released this morning is in your picker on the next refresh.

> **Presets include** Anthropic · OpenAI · Google Gemini · Google Vertex AI · Amazon Bedrock (runtime and Mantle) · DeepSeek · Kimi · Zhipu GLM · MiniMax · StepFun · LongCat · Qwen · Baidu Qianfan · Tencent Cloud · Huawei Cloud MaaS · Volcengine Ark · Mistral · Voyage AI · Groq · Nous Portal · xAI · OpenRouter · Vercel AI Gateway · Together · Fireworks · SiliconFlow · NVIDIA NIM · ModelScope · Ollama · LM Studio … and any OpenAI- or Anthropic-compatible URL.

**Import** brings in the providers from CC Switch, Claude Code, Codex and Alma. **[Add to magpie](https://usemagpie.ai/docs/import)** links let a provider's website hand its config to magpie in one click. Each key and each account can go through a proxy of its own.

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="site/public/img/add-dark.png">
  <img src="site/public/img/add-light.png" width="900" alt="Add provider: subscriptions to sign in to, vendors, relays and local servers">
</picture>

### Subscriptions

An agent you're signed in to is a subscription with models behind it, so magpie offers it as a provider to every other agent. Add several accounts per subscription and magpie fails over between them.

- **Claude**: drives the real local `claude` binary and bridges your agent's tools over MCP
- **Codex / ChatGPT**: your ChatGPT plan's models, in every other agent
- **GitHub Copilot**, **Gemini (Code Assist)**, **Antigravity**, **Grok (SuperGrok)** and more
- **Any OpenCode auth plugin or pi package** from npm

**Daily warm-up** starts each Claude and ChatGPT account's 5-hour window at the times you choose, or the moment the last one resets.

### Routing groups

A **routing group** is several models an agent picks as one, such as `group/daily-coding`. The gateway spreads requests over every member's keys and accounts:

| Mode | What it does |
| :-- | :-- |
| **`smart`** | Of the subscriptions with quota left, use the one that renews soonest, so less is wasted at the reset |
| **`order`** | Use the first model until it can't answer, then the next |
| **`rotate`** | Move to the next member on each turn |
| **`usage`** | Use the least-used member first |
| **`pace`** | Use the account with the most of its week left per hour until it resets |

A conversation **stays with the account that answered it** while the vendor's prompt cache is worth keeping. Groups can contain groups. **[Intent routing](https://usemagpie.ai/docs/intent)** has a small model of your choice read each new turn, so tests go to the strong model and quick questions to the cheap one. The Routing view shows every decision live.

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

### Plugins

```sh
magpie plugin add opencode-gemini-auth   # an OpenCode plugin from npm
magpie plugin add pi-antigravity         # a pi package, the same way
magpie plugin login google-plugin        # its own sign-in flow, in magpie
```

Plugins run on [Bun](https://bun.sh), downloaded the first time one needs it. A plugin signs in, lists its models and makes each request; agents use those models like any other provider's. Browse **[magpie-community/plugins](https://github.com/magpie-community/plugins)**, or [write your own](https://usemagpie.ai/docs/plugins).

A plugin can also be **gateway middleware**: JavaScript that sees what every agent sends and gets back. `onRequest` can rewrite a request or turn it away, `onEvent` sees each streamed event, `onResponse` the whole reply. A hook that throws or runs too long leaves the request as it was.

```js
// alias.middleware.js — magpie plugin add ./alias.middleware.js
export function onRequest(body, ctx) {
  if (body.model === "fast") return { ...body, model: "deepseek/deepseek-chat" };
}
```

<details>
<summary><b>Ready-made middleware</b>, most of it what <a href="https://github.com/QuantumNous/new-api">New API</a> does for its channels, with the same JSON</summary>

<br>

| Package | What it does |
| :-- | :-- |
| `param-override` | New API's `param_override`: set, delete, move or rewrite request fields, under conditions, or turn a request away |
| `model-map` | New API's `model_mapping`: send a model under another name; replies keep the name asked for |
| `system-prompt` | Your system prompt on every request, or some agents' or models' |
| `word-guard` | New API's sensitive-word filter: turn away or mask words in what users send, and in replies |
| `think-tags` | Take `<think>…</think>` out of replies, or put `reasoning_content` into them |

```sh
magpie plugin add @magpie-community/middleware-model-map
magpie plugin options model-map '{"mapping": {"fast": "deepseek/deepseek-chat"}}'
```

Find them under Plugins › Discover › Gateway middleware. See [Gateway middleware](https://usemagpie.ai/docs/plugins#middleware).

</details>

### Usage and cost

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="site/public/img/usage-dark.png">
  <img src="site/public/img/usage-light.png" width="900" alt="The Usage page: balances, tokens, cache hit rate and cost">
</picture>

| | |
| :-- | :-- |
| **Tokens and cost** | Tokens, cache reads and writes, reasoning and calls, with cost at list price in dollars or yuan; set your own price for any model |
| **Quota** | Balances and quota windows for every key, plan and account (`magpie quota`), and a reminder before a window renews with much of it unused |
| **Sessions** | Every agent conversation with its cost and title, reopened in your terminal with one click |
| **Context** | How full each request's context window is, how much came from the cache, and what fills it |
| **Bills** | By account and by upstream key, and OTLP export to your own observability stack |

### Sharing

| | |
| :-- | :-- |
| **Local network** | Turn on *Share on local network* and give each client a named gateway key with its own daily, weekly or monthly token and cost limit |
| **[Remote magpie](https://usemagpie.ai/docs/remote)** | A laptop uses the providers, accounts and groups of the magpie on your desktop, and still wires its own agents |
| **[Docker](https://usemagpie.ai/docs/docker)** | Run `ghcr.io/yetone/magpie` on a server or a NAS and manage it from the web UI |
| **Sync** | Back up to a file, or sync machines over WebDAV (Nutstore, Nextcloud…), S3, or an encrypted GitHub repository backup |

### Also

- **Library**: write instructions, MCP servers, skills and subagents once; magpie writes them into each agent's files in its format, and removes only what it wrote.
- **Web search for any model**: a model that can't search gets magpie's search: a provider that can, or search APIs (Tavily, Brave, Exa, Firecrawl, You.com, DeepSeek, Doubao Search on Volcengine, Yuanbao Search on Tencent Cloud, TinyFish, SearXNG), asked in the order you drag them into, each one able to be switched off without losing its key. An API can hold several keys: searches take turns over them (or use them in order with `pick=order`), a key refused lately is asked last, and Tavily keys show what each has used of its limit, in Settings and in `magpie search`. Any agent can mount the same search as an MCP server, with a `web_search` tool and a `web_fetch` tool that reads a page by its URL.
- **Images and video**: an image tool for any agent over MCP, with the model you choose, and videos with a Grok subscription or a MiniMax API key (MiniMax H3).
- **Agents kept current**: the Agents page shows each CLI's version, updates it the way it was installed, and gives the commands for the ones you don't have.
- **WSL**: on Windows, agents inside WSL distros are wired too, and their sessions counted.
- **Keep awake**, light and dark, and your theme's look on [Omarchy](https://omarchy.org).

## Documentation

| | |
| :-- | :-- |
| **[Get started](https://usemagpie.ai/docs/start)** | The guided tour |
| **[Reference](docs/reference.md)** | Every agent, provider option, gateway endpoint, CLI command and file |
| **[Plugins](https://usemagpie.ai/docs/plugins)** | Use plugins and write your own |
| **[Intent routing](https://usemagpie.ai/docs/intent)** | Route each turn by what it asks for |
| **[Import links](https://usemagpie.ai/docs/import)** | "Add to magpie" buttons for provider websites |
| **[Remote magpie](https://usemagpie.ai/docs/remote)** | Use one magpie's providers and groups from your other computers |
| **[Docker](https://usemagpie.ai/docs/docker)** | Run magpie on a server or a NAS |

## Privacy

Your prompts, replies, keys and accounts go only to the providers you use, and with [privacy masking](#privacy-masking) on, the secrets in them don't go even there. Once a day a released magpie tells us it is in use: a random id, its version and system, and which agents, providers and models it is used with, by magpie's own ids (a provider you added yourself is only `custom`), and for each partner listed first in the add sheet, how many times a day it was shown, opened and added (counts only). No names, URLs, accounts, keys, prompts or usage. Turn part or all of it off in **Settings → Privacy**, or with `DO_NOT_TRACK=1`. A partner's website and key links go through `usemagpie.ai/go/…`, which counts the click, and usemagpie.ai counts fetches of the partner list; the server sees these whatever the settings, and keeps no id or address. Icons named by a plugin, an import link or the library are fetched through `usemagpie.ai/api/icon`, so their hosts see Cloudflare's address, not yours; the server only passes the picture on, and counts and keeps nothing. The [Privacy page](https://usemagpie.ai/docs/privacy) lists everything magpie sends by itself, with an example and every switch; the [reference](docs/reference.md#counting-users) has the rules.

## Community

Questions, ideas, or a model that won't show up? Tell us on **[Discord](https://discord.gg/vGSnD3ZKQF)**. That is where feedback goes.

**This repository doesn't take pull requests.** Only maintainers can open them. If you'd like a fix or a feature, describe it on Discord and we'll build it.

If magpie saved you from editing one more config file, **a ⭐ helps others find it.**

<a href="https://star-history.com/#yetone/magpie&Date">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/svg?repos=yetone/magpie&type=Date&theme=dark">
    <img src="https://api.star-history.com/svg?repos=yetone/magpie&type=Date" width="600" alt="Star history">
  </picture>
</a>

## License

[MIT](LICENSE)

magpie is not affiliated with [Apache Magpie](https://magpie.apache.org) or the [Apache Software Foundation](https://apache.org).
