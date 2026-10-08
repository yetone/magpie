# Gateway middleware

A plugin can be gateway middleware: JavaScript that magpie runs inside its gateway on the requests agents send and the replies they get back, whatever provider serves them. It is separate from the provider plugins in [Provider and plugin ownership](provider-plugins.md), which run on Bun in the plugin host. Middleware runs in the gateway's own process on [moejs](https://github.com/Calcium-Ion/moejs), a JavaScript engine written in Go, because it sits on every request and every streamed event.

## Responsibilities and sources of truth

| Part | Responsibility | Source |
| --- | --- | --- |
| Discovery | Find a plugin's middleware file: `magpie.middleware` in its package.json, or a path to a `*.middleware.js` / `.mjs` file | `plugin.Middleware` in [`internal/plugin/middleware.go`](../../internal/plugin/middleware.go) |
| Bun host | Skip a plugin that is middleware only (no `main`, no `exports`) | `Middleware(...)` check in [`host.go`](../../internal/plugin/host.go) |
| Load and run | Compile, pool runtimes, call hooks with limits, count calls and failures | [`internal/middleware`](../../internal/middleware): `middleware.go`, `run.go` |
| Gateway | Run the chain on agents' requests only | `serveAgent` in [`internal/gateway/middleware.go`](../../internal/gateway/middleware.go), called from `gateway.go` and `codex_backend.go` |
| GUI and CLI | Show each middleware's hooks, calls, µs per call, failures and load error | `middleware.States()`; `pluginEntryJSON.Middleware` in [`internal/gui/plugins.go`](../../internal/gui/plugins.go), `mwLine` in `plugins.js`, `listPlugins` in [`plugins_cli.go`](../../plugins_cli.go) |
| Options | A middleware's `options` in plugins.json, as `ctx.options`; set by name, spec or short name; a package's `magpie.options` is only the editor's example | `SetOptions`, `ShortName` in [`store.go`](../../internal/plugin/store.go); `pluginOptions` in `plugins_cli.go`; `optionsEditor` in `plugins.js` |
| Kind | Whether a plugin is a provider, middleware or both, shown on every card and row, also for one that is off | `isMiddleware`, `middlewareOnly` in `pluginEntryJSON`; `kind: "middleware"` market entries (`market.json`, the community `registry.json`); `kindChip` in `plugins.js` |
| Community middleware | Ready-made packages (`@magpie-community/middleware-<name>`), mostly New API's channel settings | `packages/{param-override,model-map,system-prompt,word-guard,think-tags}` in [magpie-community/plugins](https://github.com/magpie-community/plugins), tested with `scripts/middleware.mjs` |
| User docs | The hooks, `ctx` and an example for plugin authors | [usemagpie.ai/docs/plugins#middleware](https://usemagpie.ai/docs/plugins#middleware) (`site/public/docs/*plugins.html`) |

plugins.json is the source of truth for which middleware is installed and on, in its order; a middleware that is off isn't loaded.

## Runtime path

1. `serveAgent` establishes the [gateway session identity and optional recording](gateway-sessions.md), then calls `middleware.Begin` with the request's protocol, model, stream flag, path and agent. With no middleware it calls `serve` directly.
2. `Run.Request` passes the agent's body, in its own API, to each `onRequest` in turn. An object returned is the body the next middleware and the gateway get; `undefined` keeps the bytes as they were. `ctx.reject(status, message)` stops the chain, and the agent gets that error in its API's shape (`writeError`).
3. `Run.Wrap` wraps the response writer when some middleware has `onEvent` or `onResponse`. An SSE reply is split into events. Each event whose data is a JSON object goes to every `onEvent` that wants its name (`export const events`): an object replaces it, `null` drops it, and `undefined` writes the original bytes. `data: [DONE]` and other non-object data pass by. A whole JSON reply is buffered for `onResponse`, with `ctx.status` set. A reply with a `Content-Encoding` passes as-is.
4. `Run.End` returns the runtimes to their pools.

Each middleware gets one runtime for the whole request, so its hooks share `ctx.state`. Runtimes come from a per-middleware `sync.Pool` and modules are compiled once per load.

Only requests agents send go through `serveAgent`. magpie's own requests (thread titles, the router's classifier, the search and vision stand-ins) call `serve` directly. Middleware sees bodies before redaction, which still applies upstream.

## Constraints and failure behavior

- Limits: 250ms for `onRequest` and `onResponse`, 50ms for each `onEvent`, 1s to load. A hook past its limit is interrupted.
- Fail open: a hook that throws, returns something that is neither an object, `null` nor `undefined`, or is interrupted leaves the request or event as it was. The failure is counted and kept as the last error, and logged at most 60 lines a minute.
- A middleware imports only relative files; packages must be bundled into it.
- Reload: a change to plugins.json or to a middleware file is picked up by the next request at most a second later (`stamp`).
- Stats count the whole call, including parsing JSON into the runtime and writing it back. Calls are counted in the process that runs the gateway, so `magpie plugin` shows hooks and load errors only.

Measured with a real account and real Claude Code: about 1–1.6µs an event, 0.2–0.7ms an `onRequest` that parses and rewrites Claude Code's ~80KB body. `BenchmarkEvent` is about 0.9µs, 11 allocations.

## Verification

```sh
go test -tags nogui ./internal/middleware
go test -tags nogui -run TestMiddlewareThroughGateway ./internal/gateway
go test -tags nogui -run TestPluginListSaysMiddleware .
go test -tags nogui -run "TestSetOptionsShortName|TestBuiltinMarket" ./internal/plugin
go test -tags nogui -bench Event ./internal/middleware
node --test internal/gui/tests/plugin-middleware.test.cjs
# in magpie-community/plugins: each package runs its cases.json through the hooks
bun test packages/think-tags packages/model-map packages/param-override packages/system-prompt packages/word-guard
```
