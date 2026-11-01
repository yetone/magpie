// A plugin whose vendor never lets it through, as one whose sign-in is
// answered 401 and tried again for ever: with $STUCK_LOAD set its plugin
// function never returns, with $STUCK_MODELS set its models hook never
// does, and with $STUCK_USAGE set its usage hook doesn't either — but not
// before saying what its caller handed it, and whether an abort reaches it,
// to the file $STUCK_TRACE names (#4678).
const never = () => new Promise(() => {})

import fs from "node:fs"

// traceUsage says what the host handed the hook: a signal it can listen to,
// and, later, that the abort arrived, one JSON line each.
function traceUsage(options) {
  const signal = options?.signal
  const live = signal && typeof signal.addEventListener === "function"
  fs.appendFileSync(process.env.STUCK_TRACE, JSON.stringify({ signal: live ? (signal.aborted ? "aborted" : "live") : "none" }) + "\n")
  if (live && !signal.aborted) {
    signal.addEventListener("abort", () => fs.appendFileSync(process.env.STUCK_TRACE, JSON.stringify({ aborted: true }) + "\n"), { once: true })
  }
}

export const StuckPlugin = async () => {
  if (process.env.STUCK_LOAD) await never()
  return {
    config: async (cfg) => {
      cfg.provider = cfg.provider ?? {}
      cfg.provider.stuckco = {
        name: "StuckCo",
        npm: "@ai-sdk/openai-compatible",
        api: "https://stuck.invalid/v1",
        models: { "stuck-1": { name: "Stuck One", limit: { context: 1000, output: 100 } } },
      }
    },
    provider: {
      id: "stuckco",
      async models(provider, options) {
        if (process.env.STUCK_MODELS_TRACE) traceUsage(options)
        if (process.env.STUCK_MODELS) await never()
        return provider.models
      },
    },
    auth: {
      provider: "stuckco",
      methods: [{ type: "api", label: "API key" }],
      loader: async () => ({ baseURL: "https://stuck.invalid/v1" }),
      usage: async (_getAuth, _provider, options) => {
        if (process.env.STUCK_USAGE) {
          traceUsage(options)
          // with $STUCK_USAGE_LINGER set, an abort ends the hook that many
          // ms later, as a hook still saving a renewed sign-in would, and it
          // says so first
          const linger = Number(process.env.STUCK_USAGE_LINGER)
          if (linger > 0 && options?.signal) {
            await new Promise((ok) => options.signal.addEventListener("abort", () => setTimeout(() => {
              fs.appendFileSync(process.env.STUCK_TRACE, JSON.stringify({ finished: true }) + "\n")
              ok()
            }, linger), { once: true }))
          } else await never()
        }
        return { windows: [] }
      },
    },
  }
}
