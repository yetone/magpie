// Runs magpie's OpenCode plugin (argv[2]) as OpenCode would, mode argv[3]:
// v1-up, v1-down (OpenCode 1's config hook), v2-up, v2-down (OpenCode 2's
// model transform), with the gateway answering MAGPIE_SERVED or down.
// Prints magpie's provider's models.
import { pathToFileURL } from "node:url"

const [, , plugin, mode] = process.argv
const served = JSON.parse(process.env.MAGPIE_SERVED || "{}")
globalThis.fetch = async (url, opts) => {
  if (mode.endsWith("-down")) throw new Error("connection refused")
  const format = new URL(url).searchParams.get("format")
  if (!opts?.headers?.authorization?.startsWith("Bearer ")) return { ok: false, status: 401 }
  return { ok: true, json: async () => served[format] }
}
const mod = (await import(pathToFileURL(plugin).href)).default

if (mode.startsWith("v1")) {
  const hooks = await mod.server({}, {})
  const cfg = {
    provider: {
      magpie: {
        npm: "@ai-sdk/openai-compatible",
        options: {},
        models: {
          "live/one": { limit: { output: 4321 }, variants: { mine: { temperature: 0.5 } } },
          "local/own": { name: "own" },
        },
      },
    },
  }
  await hooks.config(cfg)
  console.log(JSON.stringify(cfg.provider.magpie.models))
} else {
  const models = {}
  const editor = {
    provider: { get: (id) => (id === "magpie" ? { provider: { id }, models: [] } : undefined) },
    update: (p, m, fn) => {
      if (p !== "magpie") return
      const cur = models[m] ?? {
        id: m, name: m, variants: [], cost: [],
        capabilities: { tools: true, input: ["text", "image"], output: ["text"] },
        limit: { context: 200000, output: 32000 },
      }
      fn(cur)
      models[m] = cur
    },
  }
  await mod.setup({
    model: { transform: async (cb) => { cb(editor); return { dispose() {} } } },
    provider: { get: async () => ({ location: "test", data: { id: "magpie", settings: {} } }) },
  })
  console.log(JSON.stringify(models))
}
