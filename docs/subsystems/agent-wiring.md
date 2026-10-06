# Agent wiring

magpie connects a coding agent to its gateway by editing the agent's own
config. That covers the endpoint, the key `magpie-<agent>` and the model,
in the agent's own format. When the agent is disconnected, magpie puts back
what the agent had before. Package `internal/agent` describes every
supported agent: where its config lives, which fields matter and which
values to offer. It also records what magpie set, so a later change made by
something else shows up as drift.

## Responsibilities and sources of truth

| Part | Responsibility | Source |
| --- | --- | --- |
| Agent description | `Agent` has an id, name, icon, `Bin` and `Dir` for detection, `Path`, `Fields` and `Native`. It also has optional hooks: `Notice`, `UA`, `ListsModels`, `Sync`, `Unwire`, `Join`/`Joined`, `Routed`, `Follow`, `RenameRefs`, `Check`, `LastUsed`, `Reached`, `Import`/`Added`, `Launch`, `SplitSuffix` and `Spelled`. Each hook's comment says which agents need it. | [`agent.go`](../../internal/agent/agent.go) |
| Fields and options | A `Field` has `Get`, `Set` and `Options`. `Set("")` returns the field to the agent's default and takes magpie's wiring out. `Quiet` fields follow another field until set (`Follows`, such as Claude Code's tiers). An `Option` carries the picker's facts: `Direct`, `Via`, `Same`, `Alias`, `Rate` and `Context`. | [`agent.go`](../../internal/agent/agent.go), [`route.go`](../../internal/agent/route.go) |
| The list of agents | `All` lists every agent magpie knows, then the WSL agents (`wslAgents`). `Clients` lists every agent whose requests the gateway knows by name, which `usage.Agents` reads. One file per agent: `claude.go`, `codex.go`, `opencode` in `agents.go`, and so on. | [`agents.go`](../../internal/agent/agents.go) |
| Connect | `ConnectHow` picks the model an agent starts on and reports how. `How` is one of: `kept`, `again`, `joined`, `magpie`, `same`, `alike`, `default` or `first` (#726). With no models to give, it returns `NoModelsError` (code `no_models`). | [`applied.go`](../../internal/agent/applied.go) |
| What magpie set | `Apply` sets a field and records it in `applied.json`. `Wired` says whether magpie is in the config. `Drift` reports `unwired`, `replaced` or `bypassed`. `Reapply` sets the record again. `Keep` accepts the config as it is now. | [`applied.go`](../../internal/agent/applied.go) |
| What came before | The stash (`stash.json`) remembers what the config said before magpie pointed it at the gateway, so Disconnect restores it rather than guessing. | [`stash.go`](../../internal/agent/stash.go) |
| Disconnect and preview | `Disconnect` runs `Unwire`, then returns each field still on magpie to its default. `DisconnectPreview` runs the real Disconnect on a copy under a temporary home, in a subprocess (`DryRunArg`), and diffs the files. The preview is never a second account of what Disconnect does. | [`applied.go`](../../internal/agent/applied.go), [`preview.go`](../../internal/agent/preview.go) |
| Keeping configs current | `Reseat` moves agents off a model magpie no longer serves (#200). `OnGateway` and `Rewire` write the new gateway address into every connected agent when the port changes. `Source` and `Stale` say what an unconnected agent runs on, and whether running copies still use the old list. | [`reseat.go`](../../internal/agent/reseat.go), [`rewire.go`](../../internal/agent/rewire.go), [`connectinfo.go`](../../internal/agent/connectinfo.go) |
| WSL twins | On Windows each running distro is probed once, without starting a stopped one. The `wslKinds` agents found there become agents of their own, `<id>@wsl:<distro>`, edited through `\\wsl.localhost\<distro>`. The gateway address and key follow the distro's networking: mirrored or NAT (`place`, `keyAt`). `ListsFor` keeps a twin's model picks under the Windows twin's id (#927). | [`wsl.go`](../../internal/agent/wsl.go), [`gatewaykey.go`](../../internal/agent/gatewaykey.go) |
| Environment-only agents | Agents that read the gateway only from the environment get a `Launch` command. Cursor Private Inference instead gets user-level variables (`cursorLocalOn`, `KeepCursorLocalEnv`), and magpie writes none of Cursor's own files. | [`cursorlocal.go`](../../internal/agent/cursorlocal.go) |

## Runtime path

1. **Connect.** The Agents page's switch calls `ConnectHow` (`POST /api/agents/{action}/{id}` in [`internal/gui/api.go`](../../internal/gui/api.go)). It stashes the current values (`<id>.connect.was`). It brings back the models used before a switch-off (`reconnect`), or calls `Join` where the agent keeps its own model (Codex signed in with ChatGPT). Otherwise it picks a model by the order `ConnectHow` documents and sets it through `Apply`.
2. **Pick.** `Pick` sets a field from the model picker. Picking one of magpie's models on an agent that isn't connected connects it first.
3. **Notice.** After a change the page shows `Notice` when the agent reads its config only at start-up and needs a restart.
4. **Drift.** As the Agents page is drawn, `Drift` compares the config with `applied.json`. It runs `Check` first, then each field. When the config is right, it checks whether the agent's latest use (`LastUsed`, `Reached`) reached the gateway.
5. **Disconnect.** `Disconnect` puts back what the stash kept and forgets the record.

## Constraints and failure behavior

- magpie only removes what it wrote. Anything else in an agent's config stays as it was.
- Codex signed in to ChatGPT stays beside its sign-in (`openai_base_url`) unless the user picks magpie API. magpie becomes its provider only while the Codex app holds the active account, which it sends nothing for: `/wham/usage` says it isn't allowed (`allowed` false or `limit_reached` true), and it is at a spend cap or past its overage, credits or not, or has no credits and isn't a workspace still within its overage (`codexHeld`, read by `provider.CodexUsedUp`). Where the app goes by what usage can't show (an experiment's gate, a reserve, a Team plan's overage under the reserve experiment), it counts as held: a wrong move costs ChatGPT extras, a missed one every turn. A saved reading kept through a failed read stops saying held once its used-up window has started again. The app then loses its ChatGPT state (durable threads, remote control), so a window at 100% alone, which credits get past, doesn't move it (`codex.out` marks the move, and Sync undoes it once the account has room).
- Moving a field from one of magpie's models to another (the agent's own picker) is not drift. Moving it off magpie is.
- A value an agent spells as its own provider's model is never taken for one of magpie's, even when magpie has a provider of the same name (`Spelled`, #835).
- Listing never starts a stopped WSL distro. A stopped distro's agent shows what was last seen there (`asleep`, `wsl.json`); setting one of its fields writes its files, which starts the distro.
- Codex's sign-in (`login`) is ChatGPT (`""`, the default), Always ChatGPT (`chatgpt`) or magpie API (`api`). With ChatGPT, magpie's models join Codex's own beside the sign-in (`openai_base_url`), and while the ChatGPT account Codex is signed in to is used up (`codexUsedUp`), magpie is made Codex's provider (`codex.out`): the Codex app sends nothing at all on an account it knows is out, a magpie model's turn included (#540), and as a provider of its own magpie is past that. Sync puts it back beside the sign-in once the account has room. Always ChatGPT never does that: Codex stays in its ChatGPT state, and the Codex app may send nothing till the account has room, while Codex CLI's turns still reach magpie by the base URL, and its routing and other accounts serve them.
- Tests never touch the real machine. `TestMain` uses `testenv`, which points HOME, USERPROFILE, the XDG folders and APPDATA into a temporary folder, clears `agentenv.Vars` and puts failing stand-ins for keychain tools and agent CLIs on PATH. Agent-specific side effects (Aside, the Zed keychain, Codex's usage check) are also stubbed in `TestMain`.

## Verification

```sh
go test -tags nogui ./internal/agent -run 'TestConnect|TestDisconnect|Drift|TestDryRunKnowsTheAskersProviders|TestReseat|TestPort|TestWSL'
go test -tags nogui ./internal/agent
node --test internal/gui/tests/agent-connect.test.cjs internal/gui/tests/agent-disconnect-preview.test.cjs
```
