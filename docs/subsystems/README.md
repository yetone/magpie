# Subsystem design references

These pages describe current responsibilities, runtime paths, state, and contracts. Start with the subsystem relevant to your task, then follow its source links to verify the implementation you change.

| Subsystem | Reference |
| --- | --- |
| Built-in subscriptions moving to plugins | [Provider and plugin ownership](provider-plugins.md) |
| Plugins that run on the gateway's requests and replies | [Gateway middleware](gateway-middleware.md) |
| Claude subscription turns across Claude Code runs | [Claude subscription bridge](claude-subscription-bridge.md) |
| How the gateway picks a provider, key or account, and falls back | [Gateway routing and fallback](gateway-routing.md) |
| Connecting an agent's config to the gateway, drift, disconnect, WSL twins | [Agent wiring](agent-wiring.md) |
| Providers, keys, subscription accounts, refresh and allowance | [Providers and accounts](providers-accounts.md) |
| Instructions, MCP servers, skills and RTK written into agents | [Library](library.md) |
| Reasonix local conversations and retained native usage | [Reasonix sessions](reasonix-sessions.md) |
| Tray, panel, window, `magpie web` and the page's JSON API | [GUI app shell](gui-shell.md) |

A subsystem can span several packages or repositories. Its reference describes the behavior those parts provide together; it does not need to list every function.

## Maintain a reference

Update the owning reference in the same PR when a change alters a documented responsibility, interface, state transition, runtime path, compatibility rule, or failure behavior. An internal refactor that preserves these contracts does not require a prose change.

Keep each fact in one reference and link to it from agent instructions or other pages. Link to source files and named symbols rather than copying implementation details that change frequently. Explain field meanings and constraints that signatures alone do not express.

For a new reference, include the responsibilities and boundaries, source of truth, main runtime paths, state transitions, important constraints, and existing verification commands. Include failure and compatibility behavior where relevant. Document current behavior separately from proposed changes; the proposal belongs in its issue or PR.

## Describe a PR's semantic changes

For each affected subsystem, explain the observable change with its trigger and before/after behavior. Include changes to interfaces, state, compatibility, and failure behavior when relevant. Link the affected reference, implementation, and verification evidence. State when a refactor preserves the documented behavior.

For example, a change to plugin adoption should explain which migration state and account condition trigger it, which implementation handles requests before and after adoption, and which test exercises that transition.

This is an author-provided explanation that a reviewer verifies against the diff and surrounding code. It is not an automatically proven compatibility verdict.

## Review a change

1. Identify the affected subsystem and the actual runtime path, including plugin or built-in ownership.
2. Compare the claimed before/after behavior with the base and head implementation. Trace relevant callers and state transitions beyond the changed lines.
3. Check that changed contracts appear in the owning reference and that source links still resolve.
4. Check that the cited tests or runtime evidence exercise the claimed behavior. Report checks that were not run.

A documentation update alone does not prove the implementation. Existing tests and runtime evidence remain necessary for behavior changes.
