// A plugin that renews its sign-ins through magpie (auth.refresh) but
// says keepAlive: false: an account nothing uses is left to lapse. Each
// renewal is written to $RENEW_LOG as "stay:" and the refresh token.
import fs from "node:fs"

export const StayPlugin = async () => ({
  config: async (cfg) => {
    cfg.provider = cfg.provider ?? {}
    cfg.provider.stayco = {
      name: "StayCo",
      npm: "@ai-sdk/openai-compatible",
      api: "https://stay.invalid/v1",
      models: { "stay-1": { name: "Stay One", limit: { context: 1000, output: 100 } } },
    }
  },
  auth: {
    provider: "stayco",
    methods: [{ type: "api", label: "API key" }],
    keepAlive: false,
    refreshLead: 60_000,
    async refresh(auth) {
      fs.appendFileSync(process.env.RENEW_LOG, "stay:" + auth.refresh + "\n")
      return { access: "stay-new", refresh: auth.refresh + "+", expires: Date.now() + 3600e3 }
    },
  },
})
