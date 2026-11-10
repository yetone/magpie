// A plugin that leaves renewing its sign-ins to magpie (auth.refresh),
// its requests going to $FAKE_BASE. Each renewal is written to
// $RENEW_LOG as a line naming the refresh token it spent. The refresh
// token says how the vendor takes it: "r-ok…" renews (slowly, as a
// vendor does, so requests arriving meanwhile find it under way),
// "r-gone…" is turned away for good, "r-down…" can't be reached,
// "r-none…" gives nothing back (as magpie-ima-auth's does when its vendor
// refuses), and "r-slow…" renews after 4 seconds.
import fs from "node:fs"

export const RenewPlugin = async () => ({
  config: async (cfg) => {
    cfg.provider = cfg.provider ?? {}
    cfg.provider.renewco = {
      name: "RenewCo",
      npm: "@ai-sdk/openai-compatible",
      api: "https://renew.invalid/v1",
      models: { "renew-1": { name: "Renew One", limit: { context: 1000, output: 100 } } },
    }
  },
  auth: {
    provider: "renewco",
    methods: [{ type: "api", label: "API key" }],
    async refresh(auth) {
      fs.appendFileSync(process.env.RENEW_LOG, auth.refresh + "\n")
      await new Promise((ok) => setTimeout(ok, auth.refresh.startsWith("r-slow") ? 4000 : 300))
      if (auth.refresh.startsWith("r-gone")) throw Object.assign(new Error("RenewCo turned the refresh token away"), { signIn: "expired" })
      if (auth.refresh.startsWith("r-down")) throw new Error("RenewCo can't be reached")
      if (auth.refresh.startsWith("r-none")) return undefined
      const n = fs.readFileSync(process.env.RENEW_LOG, "utf8").trim().split("\n").length
      // a vendor that spends a refresh token once gives a new one
      return { access: "a-" + n, refresh: auth.refresh + "+", expires: Date.now() + 3600e3 }
    },
    loader: async (getAuth) => ({
      baseURL: process.env.FAKE_BASE,
      async fetch(url, init) {
        const a = await getAuth()
        const h = new Headers(init.headers)
        h.set("authorization", "Bearer " + a.access)
        // the test this host runs for (testenv.FakeVendor)
        if (process.env.FAKE_RUN) h.set("x-fake-run", process.env.FAKE_RUN)
        return fetch(url, { ...init, headers: h })
      },
    }),
  },
})
