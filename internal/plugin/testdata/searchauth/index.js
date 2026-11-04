// A plugin whose web search signs in as its own provider (search entries
// with auth): "seekco" is an auth hook with no models, renewed through
// magpie (auth.refresh: "r-slow…" takes 3 seconds). No network: the
// search answers with what ctx.getAuth() gave it; the query "rotate" saves
// a new token through client.auth.set, as a vendor that rotates it would.
export default async ({ client }) => ({
  auth: {
    provider: "seekco",
    methods: [{ type: "api", label: "API key" }],
    async refresh(auth) {
      // "r-hold": says it began (SEARCHAUTH_HOLD.in) and waits for the test
      // to let it go (SEARCHAUTH_HOLD)
      const hold = process.env.SEARCHAUTH_HOLD
      if (hold && auth.refresh.startsWith("r-hold")) {
        const fs = await import("node:fs")
        fs.writeFileSync(hold + ".in", "")
        while (!fs.existsSync(hold)) await new Promise((ok) => setTimeout(ok, 20))
      }
      await new Promise((ok) => setTimeout(ok, auth.refresh.startsWith("r-slow") ? 3000 : 100))
      return { access: "renewed-" + auth.refresh, refresh: auth.refresh + "+", expires: Date.now() + 3600e3 }
    },
  },
  search: {
    seek: {
      name: "Seek",
      auth: "seekco",
      async execute({ query, key }, ctx) {
        const a = await ctx.getAuth()
        if (query === "rotate") await client.auth.set({ path: { id: "seekco" }, body: { ...a, access: "rotated-" + a.accountId } })
        if (a.access === "spent") throw Object.assign(new Error("Seek: rate limited"), { status: 429, retryAfter: 60 })
        return [{ title: a.accountId + " " + a.access, url: "https://seek.invalid/" + query, text: "key=" + key + " fields=" + Object.keys(a).sort().join(",") }]
      },
    },
    // an id the plugin doesn't sign in to: a key's entry, as before
    stray: { name: "Stray", auth: "elsewhere", execute: async ({ key }, ctx) => [{ title: key + " " + typeof ctx.getAuth, url: "https://x.invalid/" }] },
    keyed: { name: "Keyed", execute: async ({ key }, ctx) => [{ title: key + " " + typeof ctx.getAuth, url: "https://x.invalid/" }] },
  },
})
