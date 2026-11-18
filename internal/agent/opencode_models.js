// magpie: written by magpie; it is rewritten as magpie's models change and removed with magpie's provider
//
// Fills the models of magpie's provider from magpie itself as OpenCode
// starts: OpenCode 1 through its config hook, OpenCode 2 through a model
// transform. A model in opencode.json under magpie's provider is the
// user's: its fields win over magpie's, and one magpie doesn't list stays.
// With magpie not answering, the list magpie had when it wrote this file.
const M = __MAGPIE__
const ID = "magpie"

const plain = (v) => v !== null && typeof v === "object" && !Array.isArray(v)

// over's fields over base's, objects merged alike
const merge = (base, over) => {
  if (!plain(base) || !plain(over)) return over === undefined ? base : over
  const out = { ...base }
  for (const [k, v] of Object.entries(over)) out[k] = merge(base[k], v)
  return out
}

// magpie's models as [{id, entry}] in its order, null when it can't say
const fetchList = async (baseURL, apiKey, format) => {
  if (typeof baseURL !== "string" || !baseURL) return null
  try {
    const res = await fetch(baseURL.replace(/\/+$/, "") + "/models?format=" + format, {
      headers: apiKey ? { authorization: "Bearer " + apiKey } : {},
      signal: AbortSignal.timeout(3000),
    })
    if (!res.ok) return null
    const body = await res.json()
    return Array.isArray(body?.models) ? body.models : null
  } catch {
    return null
  }
}

export default {
  id: "magpie.models",

  // OpenCode 1: the config hook runs before OpenCode reads its providers
  server: async () => ({
    config: async (cfg) => {
      const p = cfg?.provider?.[ID]
      if (!plain(p)) return
      const o = plain(p.options) ? p.options : {}
      const list = (await fetchList(o.baseURL ?? M.baseURL, o.apiKey ?? M.apiKey, "opencode")) ?? M.v1
      const mine = plain(p.models) ? p.models : {}
      const out = {}
      for (const { id, entry } of list) out[id] = id in mine ? merge(entry, mine[id]) : structuredClone(entry)
      for (const [id, m] of Object.entries(mine)) if (!(id in out)) out[id] = m
      p.models = out
    },
  }),

  // OpenCode 2: the config's own models are laid over these afterwards
  setup: async (ctx) => {
    const models = ctx?.model?.transform ? ctx.model : ctx?.catalog
    if (!models?.transform) return
    let s = {}
    try {
      const got = await ctx.provider.get({ providerID: ID })
      const info = got?.data ?? got
      if (plain(info?.settings)) s = info.settings
    } catch {}
    const list = (await fetchList(s.baseURL ?? M.baseURL, s.apiKey ?? M.apiKey, "opencode2")) ?? M.v2
    await models.transform((ed) => {
      if (ed.provider?.get && !ed.provider.get(ID)) return
      for (const { id, entry } of list)
        ed.update(ID, id, (m) => {
          m.name = entry.name ?? m.name
          if (plain(entry.capabilities)) m.capabilities = { ...m.capabilities, ...entry.capabilities }
          if (plain(entry.limit)) m.limit = { ...m.limit, ...entry.limit }
          if (Array.isArray(entry.cost)) m.cost = structuredClone(entry.cost)
          m.variants = Array.isArray(entry.variants) ? structuredClone(entry.variants) : []
        })
    })
  },
}
