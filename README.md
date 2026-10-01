# magpie

One place to pick every agent's model: Codex on DeepSeek, Claude Code
on Kimi, Gemini CLI on GLM, from the menu bar. [usemagpie.ai](https://usemagpie.ai)

[![Discord](https://img.shields.io/badge/Discord-join%20the%20community-5865F2?logo=discord&logoColor=white)](https://discord.gg/vGSnD3ZKQF)

`magpie` is a single screen that lists each AI agent on your machine and
the model it is set to. Click a value, pick a model. That is the whole app.

It lives in the menu bar: click the icon and a panel drops down; the same
screen also opens as a normal window (`magpie`, or *Open magpie* in the tray menu),
and there is a terminal version (`magpie tui`) and a plain CLI.

```
  ◉ magpie

  ▸ Claude Code   claude-fable-5-1[1m]                        ~/.claude/settings.json
    Codex         gpt-6-astra   effort medium
    Gemini CLI    gemini-3.1-pro
    OpenCode      anthropic/claude-sonnet-5   small anthropic/claude-haiku-4-5
    MiMo Code     anthropic/claude-sonnet-5
    Pi            openrouter/z-ai/glm-5.2:batch
    Goose         anthropic/claude-sonnet-5
    Cursor        auto
    Copilot CLI   claude-fable-5

  ↑↓ agent  ·  ←→ field  ·  ↵ change  ·  s save profile  ·  p profiles  ·  q quit
```

- **One small binary.** Under 15 MB with the desktop app (it uses the system
  webview through [Wails](https://wails.io), nothing bundled), 7 MB for the
  terminal-only build. macOS, Linux and Windows.
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
  DeepSeek, Kimi, GLM, MiniMax, StepFun, Qwen, Baidu Qianfan, Tencent Cloud Token Plan,
  Huawei Cloud MaaS, Volcengine Ark, Mistral, Groq, xAI, OpenRouter, Together,
  Fireworks, SiliconFlow, NVIDIA NIM, ModelScope, AiHubMix, 302.AI, Ollama, LM Studio…),
  paste a key, done. Custom vendors need a name and a base URL. magpie never
  reads keys from your shell environment.
- **Real model lists, nothing compiled in.** With a key in hand magpie asks
  the vendor which models it serves and offers exactly those; the
  [models.dev](https://models.dev) catalog fills in names, reasoning efforts
  and the list for vendors that have none, and refreshes itself in the
  background once it goes stale. Choose which models each provider exposes,
  or expose them all — a model released this morning is in the picker on
  the next refresh.
- **Each agent's own model list.** Under an agent's name on the Agents page,
  "Showing 5 / 32 models" opens its list: click a model to take it out of
  that agent's picker (Codex's `/model` included, its ChatGPT models too) or
  put it back; other agents still use it, and a new model is shown.
- **Profiles.** Snapshot every agent's settings under a name and switch all of
  them back in one move.
- **Real logos, no framework.** Plain HTML over the system webview; brand
  icons from [lobehub/icons](https://github.com/lobehub/lobe-icons).

## Agents

| Agent        | File                              | Fields          |
| ------------ | --------------------------------- | --------------- |
| Claude Code  | `~/.claude/settings.json`         | provider, model, opus/sonnet/haiku/fable (through magpie) |
| Claude Desktop | `Claude/` + `Claude-3p/configLibrary/` in `~/Library/Application Support` (`%LOCALAPPDATA%` on Windows, `~/.config` on Linux) | provider (its third-party gateway mode: Code and Cowork on magpie, no Anthropic sign-in; restart Desktop) |
| Codex        | `~/.codex/config.toml`            | provider, model, effort |
| Gemini CLI   | `~/.gemini/settings.json`, `~/.gemini/.env` | auth, model |
| OpenCode     | `~/.config/opencode/opencode.json(c)` (`$OPENCODE_CONFIG_DIR`) | model, small |
| OpenChamber  | `~/.config/openchamber/preferences.json` (`$OPENCHAMBER_DATA_DIR`; magpie's provider in OpenCode's config) | model, small (its own defaults, over OpenCode's) |
| MiMo Code    | `~/.config/mimocode/mimocode.json(c)` | model, small |
| Pi           | `~/.pi/agent/settings.json`       | model           |
| OmO (omo-ai) | `~/.omo/agent/settings.json` (+ `models.json`; `$OMO_CODING_AGENT_DIR`, `$SENPI_CODING_AGENT_DIR`) | model |
| Goose        | `~/.config/goose/config.yaml`     | model           |
| Cursor CLI   | `~/.cursor/cli-config.json`       | model           |
| Copilot CLI  | `~/.copilot/settings.json`        | model           |
| Crush        | `~/.config/crush/crush.json`      | large, small    |
| DeepSeek Harness (dsh) | `~/.dsh/profiles/*/cordis.patch.yml` (`$DSH_HOME`; a custom provider, Magpie), or `~/.dsh/config.yaml` before dsh 0.1.5 | model, effort |
| Command Code | `~/.commandcode/settings.json` (+ `providers.json`) | model |
| fx           | `~/.fx/settings.json`             | model (a keyless `magpie` provider) |
| omp (oh-my-pi) | `~/.omp/agent/config.yml` (+ `models.yml`) | model |
| Devin        | `~/.config/devin/config.json` (`%APPDATA%\devin\config.json` on Windows) | model |
| Hermes Agent | `~/.hermes/config.yaml` (`$HERMES_HOME`) | model |
| Kimi Code    | `~/.kimi/config.toml` (`$KIMI_SHARE_DIR`) | model (a `magpie` provider; magpie's models in Kimi's /model) |
| Muse Code    | `~/.config/muse/settings.json` (`$XDG_CONFIG_HOME`) | model (endpoint_transport to the gateway, auth none; magpie's models in Muse's list) |
| MiniMax Code (mcode) | `~/.minimax/config.yaml` (`$MINIMAX_DATA_DIR`) | model (a `magpie` custom provider; magpie's models in its /model) |
| Droid (Factory) | `~/.factory/settings.json` (`$FACTORY_HOME_OVERRIDE`) | model (magpie's models as BYOK `customModels`, in Droid's /model) |
| Cline (CLI)  | `~/.cline/data/settings/providers.json` (`$CLINE_DIR`) | model, effort (magpie takes its openai-compatible provider) |
| Qoder (CLI)  | `~/.qoder/settings.json` (`$QODER_CONFIG_DIR`) | model, effort (a `magpie` custom provider; needs a Qoder plan with BYOK) |
| Qoder CN (CLI) | `~/.qoder-cn/settings.json` (`$QODERCN_CONFIG_DIR`) | model, effort (as Qoder; its own accounts, a Qoder CN plan with BYOK) |
| Grok Build   | `~/.grok/config.toml` (`$GROK_HOME`) | model, effort |
| ZCode        | `~/.zcode/v2/config.json`         | provider (magpie's models in ZCode's picker) |
| WorkBuddy    | `~/.workbuddy/models.json` (`$WORKBUDDY_CONFIG_DIR`) | provider (magpie's models in WorkBuddy's picker) |
| OpenHanako   | `~/.hanako/provider-catalog.json` + `agents/<id>/config.yaml` (`$HANA_HOME`; its local API while it runs) | model (the primary agent's; magpie's models as a provider) |
| Alma         | Alma's local API (`localhost:23001`, while Alma runs) | model (Alma's default; magpie's models as a provider) |

Provider-scoped agents (OpenCode, MiMo Code, Pi, OmO, Goose, Crush, omp, Hermes Agent) take `provider/model`.
Only agents that are installed or configured are shown.

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

One magpie can serve several computers (an office one, a personal one):
share it on the network (Settings → Share on local network), and on each
other computer add it as a **Remote magpie** — in the app's Add sheet, or
`magpie provider add remote-magpie sk-magpie-… url=http://192.168.1.20:3425 id=office`.
Each computer's own magpie still wires its agents, while the providers,
routing groups (`office/group/…`) and usage are the shared one's. A request
goes on in the API the agent spoke — Anthropic Messages, Responses, Chat
Completions, token counting — and a model the shared magpie's provider serves
on another API only is turned into that API once, never on both computers.
Its list is the models the shared magpie's agents are shown, each named with
its provider there (`Claude Sonnet 5 · Relay A · office`), and its image
models are listed under Settings → Images and draw through it.

The gateway issues **gateway keys** for clients, separate from a provider's
upstream API keys. Turn on **Settings → Share on local network**, then open
**Gateway → Gateway keys → Add gateway key**. This block appears only while
sharing is on. Create a named key for each client and copy it from its row.
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
magpie gateway-key list                # ids, names, enabled state and masked keys
magpie gateway-key rotate <id>         # prints the replacement; identity stays the same
magpie gateway-key remove <id>         # revokes remote access
```

While LAN sharing is enabled, remote requests require an enabled gateway key
sent as Bearer, `x-api-key`, `x-goog-api-key` or `?key=`. Loopback remains
permissive: any token works, including a stale or disabled gateway key.
Only a valid, enabled key is attributed to its named identity.
Without sharing, an explicitly exposed `MAGPIE_ADDR` keeps its original open
access, including old `sk-magpie-…` tokens, without key authentication.

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
rotation does not re-enable it. The key store records migration completion,
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

### Plugins

A subscription magpie doesn't sign in to itself can come from an
[OpenCode](https://opencode.ai) provider plugin: the npm packages OpenCode
users install to sign in to a plan (their `auth` hook) work in magpie as they
do there. magpie runs them on [Bun](https://bun.sh), downloaded the first
time a plugin needs it, and the plugin signs in, lists the models and makes
each request; magpie serves them to agents like any provider's.

```sh
magpie plugin add opencode-gemini-auth   # an npm package, or a path to a plugin of your own
magpie plugin                           # the plugins, what each signs in to, and whether you are
magpie plugin login google-plugin       # its sign-in: the method, its questions, the browser or a key
magpie plugin logout google-plugin
magpie plugin off opencode-gemini-auth  # on brings it back; rm removes it; update updates them all
```

A provider id magpie already has (google, openai, anthropic) is
`<id>-plugin`. In the app, Settings → Plugins adds and removes them, and
the providers they sign in to are in Add provider → From plugins.

#### For plugin authors

A plugin is an OpenCode plugin; magpie reads a few more fields, which
OpenCode ignores:

- **The provider's icon**: `icon` on the `auth` hook, or `"magpie": {
  "icon": "…" }` in the plugin's `package.json` (for every provider it
  signs in to that names none). An `https://` URL of a picture on a public
  host, which magpie fetches once and keeps, or a `data:image/…` URI;
  PNG, JPEG, GIF, WebP, ICO or SVG, at most 1 MB. Anything else is ignored,
  and the icon the plugin market lists for the plugin is shown instead.
- **An API key's field**: a `type: "api"` method's `label` titles the key's
  field, as OpenCode's dialog does (one that only says "API key" reads
  "<provider> API key"), and its `placeholder` is the hint inside the field
  (and after the question in `magpie plugin login`). The method's `prompts`
  are asked first, as in OpenCode, and reach `authorize(inputs)`.

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

### What a model costs

A call is counted at its **effective price**: what you set for that provider
and model if you did, otherwise what the provider's own catalogue lists, and
otherwise what models.dev lists for the model's maker. Out of the box that
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
served at no cost, which is a price, not the absence of one.

The order a price is looked for in is: **the price for this model → the price
for `<provider id>/*`, which covers every model of that provider → what the
provider's own catalogue lists → what models.dev lists for the maker.**
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
advertised in `/models` and is what a group advertises the smallest of; the
gateway does not itself cap a reply by it.

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
magpie group rm opus-anywhere           # one magpie found is hidden; magpie group restore <id> brings it back
magpie claude group/opus-anywhere       # use it
```

`routing=` is `smart` (the default: of the subscriptions with quota to
spare, the one whose allowance renews soonest first), `order` (the first
model until it can't answer, then the next), `rotate` (each turn to the next
member) or `usage` (least used first). `stays=` is how long a conversation
stays with the key or account that answered it: `auto` (the default, while
the vendor's cache of it is worth keeping), `session`, `turn` or `off`.
`models=` replaces the whole list, in order; a bare model id works when only
one provider serves it.

The Routing page's Requests list defaults to the time-ordered By request view.
Choose By session to group calls by the agent's session ID; the page remembers
your choice across reloads.
Codex title helpers with an explicit parent or fork source join their originating
chat, retaining their title badge and contributing to its cost. Titles without
ancestry and ordinary forked chats stay separate.
Codex chat names come from its local name index and follow renames. Unknown or
remote-only names fall back to the ID; the full ID remains in the heading tooltip.
Expand a session to see each request. Each request and session shows its estimated cost at the effective model
prices, including cache reads and writes. Session totals cover the listed
requests only (the live trace or the selected day's retained history), and a
`+` marks a partial estimate. Calls without a session ID are listed separately;
old history without token tiers, or a model without a known price, shows `—`.

The app's Import from other apps dialog can copy providers from Claude Code's
`settings.json` (`CLAUDE_CONFIG_DIR` when set) and Codex's `config.toml`
(`CODEX_HOME` when set) into magpie. Codex imports custom
`[model_providers.*]` entries with an inline `experimental_bearer_token`,
including fixed headers for custom providers in
`[model_providers.*.http_headers]` and models from
`[profiles.*]` or `model_catalog_json`. Review the entries before importing;
subsequent changes to agent settings are not automatically copied to magpie.
Entries that point back to magpie or only name an `env_key` are skipped.

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

### Connecting anything else

The gateway listens on `127.0.0.1:3425` (`MAGPIE_ADDR` changes it) and starts
with the app; `magpie serve` runs it alone. For reverse-proxied or container
deployments, set `MAGPIE_PUBLIC_URL=https://magpie.example.com` to the base
URL shown in the console and CLI, including connection examples. Local
agent configs still use the local gateway address.

A reverse proxy must enforce authentication itself, or you must enable
Settings → Share on local network and use an enabled gateway key
(Gateway → Gateway keys) for external clients. A public URL with no port of
its own — a reverse proxy's `https://magpie.example.com` — is the address
`magpie web` prints for its own page too, so the proxy must forward `/v1`
and `/v1beta` to the gateway's port and the rest to the page's. When the
proxy and magpie run on the same machine, requests forwarded over loopback
are treated as local and need no key, so the proxy must authenticate those
clients itself.

It exposes:

| Path                     | API                        |
| ------------------------ | -------------------------- |
| `/v1/chat/completions`   | OpenAI chat completions    |
| `/v1/responses`          | OpenAI Responses           |
| `/v1/messages`           | Anthropic Messages         |
| `/v1/messages/count_tokens` | Anthropic token counting |
| `/v1beta/models/{model}:generateContent` | Google Gemini (also `:streamGenerateContent`, `:countTokens`) |
| `/v1/models`, `/v1beta/models` | the catalog            |

Each `/v1/models` entry includes `reasoning` and `supported_reasoning_levels`
(`[{"effort":"low"}, ...]`). A routing group lists only the levels every
member supports. `native_endpoints` (`["/v1/messages"]`) names the APIs a
request for the model is passed straight through on; it is left out of a
routing group, and of a model every request to is translated anyway.

Requests pass straight through when the vendor speaks the agent's API and
are translated otherwise, streaming, tool calls and reasoning included. The
key is `magpie` (any value works; the gateway only listens on loopback), and
models are named `provider/model`. Anything with a base-URL setting can use
it:

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
another only with the key of a gateway shared on the local network.

The *Gateway* tab in the app has this as copy buttons and ready-made
snippets (shell, curl, Python, Node) for each API, the list of model ids,
and the recent calls; `MAGPIE_DEBUG=1` logs every call to the terminal.

**Claude Code** gets `ANTHROPIC_BASE_URL`, `ANTHROPIC_AUTH_TOKEN` and the
model variables in the `env` block of `settings.json`; picking a native
model (`opus`, `sonnet`…) removes them and restores whatever was there.

**Codex** gets a `[model_providers.magpie]` table, `model_catalog_json`
pointing at `~/.codex/magpie-models.json` (written from the catalog, so the
models show in Codex's own list) and a valid `model`/`effort`; picking a
native model removes all of that. Your ChatGPT sign-in is never touched.
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
| `preset`    | a preset id (`magpie presets`); its endpoints are used             |
| `region`    | with a preset that has regions, which one                          |
| `name`      | the provider's name; required without a preset                     |
| `id`        | its id; derived from the name when absent                          |
| `key`       | the API key; the user pastes one when absent                       |
| `chat`      | OpenAI Chat Completions base URL (`…/v1`)                          |
| `responses` | OpenAI Responses base URL (`…/v1`)                                 |
| `anthropic` | Anthropic Messages base URL (the root, without `/v1`)              |
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

### Docker

`docker build` makes a server image: the terminal-only binary on
distroless (`cc`, for the glibc the plugins' Bun needs), run as nonroot,
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

3425 is the gateway for agents. Until it is shared (below) it takes any key,
`Bearer magpie` included, from anyone who reaches it, so the ports above are
published on the host's loopback only; Docker's `-p 3425:3425` would put it
on every interface of the host, past its firewall. To reach it from other
machines, turn on Settings → Share on local network in the browser UI (or
put `"lan": true` in `/config/magpie/settings.json`). Turning it on in Settings
creates a named **Magpie** key. With settings edited by hand, run
`magpie gateway-key add "Docker client"` in the container to create a key
without the browser UI. A request from outside the container must carry one of
those keys as its API key. Only then publish the port beyond 127.0.0.1.
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
to keep one key across restarts). There you add providers, sign in to
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
magpie tui                      # the same thing, in the terminal
magpie web                      # the app's window in a browser (WSL, a server over SSH); --lan, --addr, --no-open
                                # (a new key each run; MAGPIE_WEB_KEY keeps one, for a page run as a service)
magpie ls                       # list every agent and its current settings
magpie claude opus              # set a model (agent names accept prefixes: cc, oc, gem …)
magpie codex gpt-5.6-sol
magpie codex effort high        # other fields
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
```

In the app, click any value to open a filtered list; type to search or to
enter something that is not listed; `esc` closes the panel. Profiles are the
chips at the bottom: click to apply, `×` to delete, *+ save current* to add.
The *Providers* tab of the window lists your providers with the agents on
each; click a row to change the key or the exposed models, *Test* it, or
click an agent icon to point that agent at one of its models. *Add
provider* shows the presets as tiles: pick one, paste the key.

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
the skills with their files (a file over 2 MB is left out). Without keys, a
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
in to them on each machine. Piped in, the passphrase is the first line of
stdin.

### Keeping machines in sync

*Settings → Sync and backup → WebDAV or S3 sync* keeps the same backup on a
server and brings every machine up to date with it, every 3 minutes while the
gateway runs. Choose one of these:

- **WebDAV**: a folder on a WebDAV server such as 坚果云, Nextcloud or a
  Synology.
- **S3**: a bucket on AWS S3, Cloudflare R2, Backblaze B2, MinIO, Garage, a
  NAS or any other S3-compatible server.

The file is sealed on your machine with the passphrase, so the server only
ever stores ciphertext. Each machine writes only over the version it read (a
conditional write), so an update that another machine made in between is
merged rather than lost.

```sh
magpie webdav on https://dav.jianguoyun.com/dav/ user=me@example.com
magpie s3 on s3://my-bucket/magpie endpoint=https://<account>.r2.cloudflarestorage.com access-key-id=…
magpie s3 on s3://backups endpoint=http://nas.local:9000 path-style=yes access-key-id=…
magpie s3                       # where it syncs to and how the last sync went; magpie s3 now, off
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
  object's ETag just before each write.

## OTLP export

Settings → Observability can export gateway request metadata over OTLP/HTTP
(JSON). Export is off by default. Set the collector's base URL and optional
headers, then enable **OTLP export**. **Export metrics** is separately off by
default; enable it for a collector that accepts duration and token histograms.
No restart is needed for saved settings.

For `magpie serve`, environment variables override the saved preferences:

```sh
MAGPIE_OTEL_ENABLED=true MAGPIE_OTEL_ENDPOINT=http://localhost:4318 magpie serve
```

- `MAGPIE_OTEL_ENABLED`: `true` or `false`; an endpoint alone does not enable export.
- `MAGPIE_OTEL_ENDPOINT`: an HTTP(S) base URL; `/v1/traces` and `/v1/metrics` are appended.
- `MAGPIE_OTEL_HEADERS`: comma-separated `name=value` pairs, for example
  `Authorization=Bearer%20token`. Percent-encode spaces and commas in values.
- `MAGPIE_OTEL_METRICS`: `true` or `false`, off by default.

For Langfuse, use `https://<your-langfuse-host>/api/public/otel` as the base
URL and `Authorization=Basic%20<base64(public-key:secret-key)>` as the header.
Leave metrics off. This uses Langfuse's OTLP ingestion endpoint.

Traces include agent, provider, model, token counts (including cache and
reasoning), HTTP status, timing, and route ID. Attempts with the same route ID
share a trace ID. Metrics group duration and input/output token histograms by
agent, provider, model, operation and error status. Prompt/reply text, tool
arguments, sessions and provider account names/keys are never exported.

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

## Files

- `~/.config/magpie/profiles.json` — saved profiles
- `~/.config/magpie/providers.json` — your providers, keys included (0600)
- `~/.config/magpie/stash.json` — values magpie replaced, restored on switch-back
- `~/.config/magpie/plugins.json`, `plugins/` — the plugins added, and their packages
- `~/.config/magpie/plugin-auth.json` — the plugins' sign-ins (0600)
- `~/.cache/magpie/bun/` — the Bun plugins run on
- `~/.cache/magpie/models.json` — models.dev catalog (OpenCode's cache at
  `~/.cache/opencode/models.json` is used when present)
- `~/.cache/magpie/models/<provider>.json` — model lists fetched from vendors

`XDG_CONFIG_HOME` and `XDG_CACHE_HOME` are respected.

## Counting users

Once a day, a running magpie (the app, or `magpie serve`) sends one event to
PostHog so we know how many people use it: a random id made up on your
computer (`~/.config/magpie/install-id`), magpie's version, and your system
and architecture. Nothing else goes: no accounts, keys, providers, models,
prompts or usage. Turn it off in Settings → Privacy → Count me as a user, or
with `DO_NOT_TRACK=1` or `MAGPIE_NO_STATS=1`. Builds from source never send
it. The code is [internal/stats](internal/stats/stats.go).

## Community

Questions, setups worth sharing, ideas, bugs: come talk to us and other
magpie users on [Discord](https://discord.gg/vGSnD3ZKQF). Issues and pull
requests are welcome here too.

## License

MIT. See [LICENSE](LICENSE).
