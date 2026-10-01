// A plugin as OpenCode's are written, signing in to a made-up provider
// whose requests go to $FAKE_BASE; $FAKE_ID names it another's id, as a
// built-in's plugin does.
const ID = process.env.FAKE_ID || "fakeco"

export const FakePlugin = async ({ client }) => ({
  config: async (cfg) => {
    cfg.provider = cfg.provider ?? {}
    cfg.provider[ID] = {
      name: "FakeCo",
      npm: "@ai-sdk/openai-compatible",
      api: "https://fake.invalid/v1",
      models: {
        "fake-1": { name: "Fake One", limit: { context: 1000, output: 100 } },
        "fake-claude": { name: "Fake Claude", provider: { npm: "@ai-sdk/anthropic" }, modalities: { input: ["text", "image"], output: ["text"] }, limit: { context: 2000, output: 200 } },
        "fake-gemini": { name: "Fake Gemini", provider: { npm: "@ai-sdk/google" }, reasoning: true, modalities: { input: ["text"], output: ["text"] }, limit: { context: 3000, output: 300 } },
      },
    }
  },
  auth: {
    provider: ID,
    methods: [
      { type: "api", label: "API key" },
      {
        type: "oauth",
        label: "Browser",
        prompts: [
          { type: "select", key: "where", message: "Where?", options: [{ label: "Home", value: "home" }, { label: "Work", value: "work" }] },
          { type: "text", key: "team", message: "Team?", when: { key: "where", op: "eq", value: "work" }, validate: (v) => (v ? undefined : "Required") },
        ],
        authorize: async (inputs) => ({
          url: "https://fake.invalid/auth?where=" + inputs.where,
          instructions: "Paste the code",
          method: "code",
          callback: async (code) =>
            code === "good"
              ? { type: "success", refresh: "r-" + (inputs.team ?? "none"), access: "stale", expires: 0, accountId: (inputs.team ?? "me") + "@fake" }
              : code === "expired"
                ? { type: "failed", error: "the sign-in page expired" }
                : { type: "failed" },
        }),
      },
    ],
    loader: async (getAuth, provider) => ({
      apiKey: "dummy",
      baseURL: process.env.FAKE_BASE,
      async fetch(url, init) {
        let a = await getAuth()
        if (a.type === "oauth" && a.expires < Date.now()) {
          a = { ...a, access: "fresh-" + a.refresh, expires: Date.now() + 3600e3 }
          await client.auth.set({ path: { id: ID }, body: a })
        }
        const h = new Headers(init.headers)
        h.set("authorization", "Bearer " + (a.type === "oauth" ? a.access : a.key))
        h.set("x-models", Object.keys(provider.models).sort().join(","))
        h.set("x-body-type", typeof init.body)
        return fetch(url, { ...init, headers: h })
      },
    }),
    // magpie's: the account's allowance; "full@fake" has used its five
    // hours, which count only fake-claude
    usage: async (getAuth) => {
      const a = await getAuth()
      // $FAKE_USAGE: the plan is what the vendor's page there says
      if (process.env.FAKE_USAGE) {
        const r = await fetch(process.env.FAKE_USAGE)
        return { plan: await r.text() }
      }
      if (a.type !== "oauth") return { error: "an API key has no plan" }
      if (a.refresh === "r-gone") return { error: `${a.accountId}: the FakeCo sign-in has expired — sign in again` }
      if (a.refresh === "r-offline") throw new Error("fetch failed")
      // the vendor unreachable, said as Zed's plugin says it
      if (a.refresh === "r-unreachable") {
        try {
          await fetch("http://127.0.0.1:9/me")
        } catch (e) {
          return { error: e?.message ?? String(e), signIn: "kept" }
        }
      }
      if (a.refresh === "r-kept") return { error: "sign in again to see usage", signIn: "kept" }
      if (a.refresh === "r-renewed") return { error: "usage is down", signIn: "renewed" }
      const full = a.accountId === "full@fake"
      return {
        plan: "Fake Pro",
        user: full ? "Full@Fake.example" : undefined,
        until: "2030-01-02T03:04:05Z",
        renew: "auto",
        resets: full ? { count: 3, byWindow: true, fiveHour: 2, weekly: 1 } : undefined,
        windows: [
          { name: "5 hours", used: full ? 100 : 25, resetsAt: Date.now() + 3600e3, span: 5 * 3600, models: ["fake-claude"] },
          { name: "Week", used: 10, resetsAt: Math.floor(Date.now() / 1000) + 86400, span: 7 * 86400 },
          { name: "Extra", used: 250, display: "$2.50", aside: true },
        ],
      }
    },
  },
  // the models an account has: refused for a dead one; a "rot-" sign-in
  // spends its refresh token asking, as a rotating one does
  provider: {
    id: ID,
    models: async (p, { auth }) => {
      if (auth?.refresh === "r-dead" || auth?.key === "dead") throw new Error("the vendor refused the sign-in")
      if (auth?.refresh === "r-models-gone") throw Object.assign(new Error("the vendor refused the sign-in"), { signIn: "expired" })
      // a sign-in past its time, said in words only, as Grok's plugin says it
      if (auth?.refresh === "r-models-expired") throw new Error("FakeCo's sign-in has expired; run `fake login`")
      // the vendor unreachable: the list kept, as Zed's plugin keeps it
      if (auth?.refresh === "r-unreachable") await fetch("http://127.0.0.1:9/models").catch(() => {})
      if (auth?.type === "oauth" && auth.refresh?.startsWith("rot-")) {
        await client.auth.set({ path: { id: ID }, body: { ...auth, refresh: auth.refresh + "+" } })
      }
      // fake-1 costs the plan nothing, as a WorkBuddy model of x0.00 credits
      p.models["fake-1"].free = true
      if (auth?.key === "few") return { "fake-1": p.models["fake-1"] }
      // $FAKE_MODELS: the vendor's list, whose answer names one more model
      if (process.env.FAKE_MODELS && auth) {
        const r = await fetch(process.env.FAKE_MODELS).then((r) => r.text()).catch(() => "")
        if (r) p.models["fake-" + r] = { ...p.models["fake-1"], id: "fake-" + r, name: r }
      }
      return p.models
    },
  },
  "chat.headers": async (input, output) => {
    if (input.model.providerID === ID) output.headers["x-plugin-model"] = input.model.id
  },
})
