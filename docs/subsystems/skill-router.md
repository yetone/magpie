# Skill router

With the Library's switch "Skill router" on, each agent's skills folder is given one skill of magpie's, `magpie-skills`, in place of a link for each skill ticked for it. Its SKILL.md lists those skills, each with its description and the path of its own SKILL.md, and the agent reads that file when a task fits one. An agent then carries one skill's description in its prompt, however many the Library gives it. It is the skills' counterpart of the [MCP router](mcp-router.md); the skills and their ticks are the [Library](library.md)'s. The switch is off by default; with it on, every skill goes through the router unless it is kept direct.

## Responsibilities and sources of truth

| Part | Responsibility | Source |
| --- | --- | --- |
| Switch | `Library.SkillRouter` (`skillRouter` in library.json), this computer's alone: a sync doesn't carry it. `SetSkillRouter` sets it through `change`, so every agent is written again at once. The page posts `skills/router` `{on}`; the CLI is `magpie library skill router on\|off`. | `SetSkillRouter` in [`skill_router.go`](../../internal/library/skill_router.go); `skills/router` in [`gui/library.go`](../../internal/gui/library.go); [`library_cli.go`](../../internal/cli/library_cli.go) |
| Direct | `Skill.Router == "direct"` (`SkillDirect`) keeps a skill out of the router: each agent given it keeps its own link, to type it as `/name` or keep it in the prompt. It is carried by a sync like the skill's ticks. The page posts `skills/router-direct` `{name, on}`; the CLI is `magpie library skill router <name> routed\|direct`. | `SkillRouterHow`; `Skill.Router` in [`skills.go`](../../internal/library/skills.go), [`carry.go`](../../internal/library/carry.go) |
| What is listed | `routedFor`: each skill ticked for the agent (or for an agent reading the same folder), but one kept direct, one whose SKILL.md sets `disable-model-invocation` (for the user to type, which a listed skill can't be: `routable`), one the agent finds in `~/.agents/skills` anyway, one blocked, and one where the agent has its own entry by that name. Each of these keeps its own link. | `routedFor`, `routable` |
| Who keeps their links | An agent whose folder can't take the router keeps every link, and `AgentView.SkillRouter` says why (`routerWhy`): `copies` (given copies: WSL, the user's choice, Claude Desktop), `liveagent` (it uses only the skills picked in its own list), `shared` (its folder is `~/.agents/skills`, which other agents read too), `taken` (a Library skill is called magpie-skills), `own` (the folder has a magpie-skills of the user's). `on` when it is given the router. | `routerWhy` |
| Entry | `placeRouter` writes `routerDir(t.Skills)` (`<settings dir>/skill-router/<sha256 of the folder's real path, 6 bytes>/`): its SKILL.md and, under `.skills/`, a link to each listed skill's folder. The folder's entry `magpie-skills` is a link to it (`routerOurs` knows magpie's by where it leads). `Applied.Router` records it; with nothing listed, the entry and its folder go. | `placeRouter`, `writeRouter`, `routerOurs` |
| SKILL.md | Front matter `name: magpie-skills` and a description of at most 1024 bytes (`routerDescMax`: Codex refuses a longer one), naming the skills with as much of each description as fits, cut on a whole character (`routerDesc`). The body gives each skill's whole description and the path of its SKILL.md through the agent's own folder (`<skills>/magpie-skills/.skills/<name>/SKILL.md`). `.skills` is hidden, so the agents that look for skills in every folder under theirs (Codex, OpenCode, Pi) don't find a listed skill twice. | `writeRouter`, `routerDesc` |
| Claude Code's Read rules | Claude Code asks before it reads outside its project, its own skills folder too, and checks both the path given and where a link leads (2.1.316). For Claude Code (`claude`, any profile; not a plugin agent) magpie adds `permissions.allow` rules to its settings.json: `Read(//<entry's real path>/**)` and `Read(//<library skills folder's real path>/**)`, or one for each listed skill whose folder is elsewhere (`routerRules`; Windows paths as `/c/...`, `claudePath`). `Applied.RouterRules` records magpie's, so it takes out only those and keeps the user's own (`offList`, as for `permissions.deny`). | `routerRules`, `routerAllow` |
| GUI | Under the skill list on the Library's Skills tab: a row "Skill router" with its switch (`.lib-skillrouter`, `.lib-switch`), and with it on, one line for each reason an agent keeps its own links, naming those agents (`.lib-hub-why[data-why]`). Each skill the router may list has a button (`.lib-routedirect`, `aria-pressed`) that keeps it out of the router or lists it again; a skill kept out shows "Not through the router" (`.lib-direct`). | `skillRouterPart` in [`plugins/library/client.js`](../../internal/gui/assets/plugins/library/client.js) |
| Sessions | A Read of `…/magpie-skills/.skills/<name>/SKILL.md` counts as the use of `<name>` (Claude Code, Alma, Droid: `routedSkill`; Codex counts any SKILL.md it reads). Calling up `magpie-skills` itself is counted as a Skill tool call and no skill (`routerSkill`), so a routed use is counted once. | `routedSkill`, `state.tool`, `ccTool` in [`internal/sessions`](../../internal/sessions/sessions.go) |

## Runtime path

1. The user turns the switch on. `change` saves library.json and runs the sync for every agent.
2. For each agent with a skills folder, `syncSkills` asks `routedFor` which skills the router lists there. It writes a link for every other ticked skill, and `placeRouter` writes the router's folder and entry and Claude Code's Read rules.
3. The agent starts with `magpie-skills` in its skill list. When a task fits it, the agent loads it (Claude Code's Skill tool, or a read of its SKILL.md), reads the listed skill's SKILL.md at the path given, and follows it. A file the skill names is in the same folder.
4. On the Claude subscription route, Claude Code sends a loaded skill's body as a text block after the Skill call's result. The [Claude subscription bridge](claude-subscription-bridge.md) hands it to the run in that result (`withSkillBodies`).

## Constraints and failure behavior

- An agent reads its skills when it starts: a change reaches a running agent on its next start.
- A skill listed by the router is not a `/name` command of the agent's. A skill the user types is kept direct, and one with `disable-model-invocation` is never listed.
- The user's own `magpie-skills`, in the folder or in the Library, is never written over. The agent keeps every link, and the page says why (`own`, `taken`).
- Turning the switch off writes every link again, removes the entry, its folder and magpie's Read rules, and leaves the user's own rules.
- Alma reads Claude Code's and Codex's folders besides its own, so with the router on for them too it finds several `magpie-skills`, each listing another agent's skills (#5722).
- An agent other than Claude Code may ask before reading a listed skill's files, as for any file outside its project. Only Claude Code has rules magpie writes.

## Verification

```sh
mote-run go test -tags nogui ./internal/library -run 'SkillRouter|RouterDesc|RouterRules' -count=1
mote-run go test -tags nogui ./internal/sessions -run 'Routed' -count=1
mote-run go test -tags nogui ./internal/gateway -run 'SkillBod' -count=1
mote-run node --test internal/gui/tests/skill-router.test.cjs
```

`TestSkillRouter` covers the links, the entry, SKILL.md, the Read rules, direct skills, a second sync that writes nothing, and the switch turned off. `TestSkillBodyRealClaudeCode` parses the request a real Claude Code sent after loading `magpie-skills`.
