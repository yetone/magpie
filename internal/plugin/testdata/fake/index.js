// A plugin as OpenCode's are written, signing in to a made-up provider
// whose requests go to $FAKE_BASE; $FAKE_ID names it another's id, as a
// built-in's plugin does.
const ID = process.env.FAKE_ID || "fakeco"

// fakeWho is who a team signs in as: team@fake, and a team named name/uid
// is name@fake with that uid, as WorkBuddy's plugin keeps one
function fakeWho(team) {
  const [name, uid] = (team ?? "me").split("/")
  return uid ? { accountId: name + "@fake", uid } : { accountId: name + "@fake" }
}

// h2 POSTs path on an HTTP/2 session to origin, a test server's
// certificate taken, and answers with what it said; one that fails is a
// 502 saying why.
async function h2(origin, path) {
  const http2 = (await import("node:http2")).default
  return new Promise((resolve) => {
    const s = http2.connect(origin, { rejectUnauthorized: false })
    const fail = (e) => (s.destroy(), resolve(new Response("h2: " + (e?.message || e?.code || e), { status: 502 })))
    s.on("error", fail)
    const req = s.request({ ":method": "POST", ":path": path })
    req.on("error", fail)
    let status = 0
    const body = []
    req.on("response", (h) => (status = h[":status"]))
    req.on("data", (c) => body.push(c))
    req.on("end", () => (s.close(), resolve(new Response(Buffer.concat(body), { status }))))
    req.end()
  })
}

// loopbackSignIn is a browser sign-in finished at a port on this machine:
// /callback with this sign-in's state and the code "good" signs in, a
// wrong code fails it, and /next sends the browser on to another page
// that comes back here, as Kiro's AWS sign-in does.
async function loopbackSignIn(inputs) {
  const http = (await import("node:http")).default
  const state = Math.random().toString(36).slice(2)
  let settle
  const done = new Promise((r) => (settle = r))
  const server = http.createServer((req, res) => {
    const u = new URL(req.url ?? "/", "http://127.0.0.1")
    const back = `http://127.0.0.1:${server.address().port}/callback`
    if (u.pathname === "/next") return res.writeHead(302, { Location: "https://aws.invalid/authorize?redirect_uri=" + encodeURIComponent(back) }).end()
    if (u.pathname !== "/callback") return res.writeHead(404).end()
    if (u.searchParams.get("state") !== state) return res.writeHead(200).end("not this sign-in")
    const code = u.searchParams.get("code")
    settle(code === "good" ? { type: "success", refresh: "r-" + (inputs.team ?? "none"), access: "stale", expires: 0, ...fakeWho(inputs.team) } : { type: "failed", error: "FakeCo refused the code" })
    res.writeHead(200).end("done")
  })
  await new Promise((r) => server.listen(0, "127.0.0.1", r))
  const back = `http://127.0.0.1:${server.address().port}/callback`
  return {
    url: "https://fake.invalid/auth?" + new URLSearchParams({ redirect_uri: back, state }),
    instructions: "Sign in in the browser",
    method: "auto",
    callback: () => done.finally(() => setTimeout(() => server.close(), 1000)),
  }
}

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
    // $FAKE_RESPONSES: one model more, on OpenAI's Responses (Grok's)
    // $FAKE_FAST: fake-1 has a fast one, as a Cursor model has its -fast
    if (process.env.FAKE_FAST) cfg.provider[ID].models["fake-1-fast"] = { name: "Fake One Fast", limit: { context: 1000, output: 100 } }
    if (process.env.FAKE_RESPONSES) cfg.provider[ID].models["fake-resp"] = { name: "Fake Responses", provider: { npm: "@ai-sdk/openai" }, limit: { context: 4000, output: 400 } }
    // $FAKE_DEEPSEEK: a DeepSeek model, as Cline's cline-pass/deepseek-v4-pro
    if (process.env.FAKE_DEEPSEEK) cfg.provider[ID].models["deepseek-v4-pro"] = { name: "DeepSeek V4 Pro", limit: { context: 1000, output: 100 } }
    // $FAKE_OFF: a model that stops thinking at none and one that can't,
    // as Factory's plugin has Kimi K3 and GLM-5.3 (#899)
    if (process.env.FAKE_OFF) {
      const levels = (l) => Object.fromEntries(l.map((e) => [e, { reasoningEffort: e }]))
      cfg.provider[ID].models["fake-off"] = { name: "Fake Off", reasoning: true, variants: levels(["none", "low", "high", "max"]), limit: { context: 1000, output: 100 } }
      cfg.provider[ID].models["fake-on"] = { name: "Fake On", reasoning: true, variants: levels(["low", "high", "max"]), limit: { context: 1000, output: 100 } }
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
        // $FAKE_LOOPBACK: the page comes back to a port here, as Devin's
        // and Kiro's plugins sign in
        authorize: async (inputs) => process.env.FAKE_LOOPBACK ? loopbackSignIn(inputs) : ({
          url: "https://fake.invalid/auth?where=" + inputs.where,
          instructions: "Paste the code",
          method: "code",
          callback: async (code) =>
            code === "good"
              ? { type: "success", refresh: "r-" + (inputs.team ?? "none"), access: "stale", expires: 0, ...fakeWho(inputs.team) }
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
        // "r-revoked": the vendor turned the refresh away, said as Zed's says it
        if (a.type === "oauth" && a.refresh === "r-revoked") throw Object.assign(new Error("FakeCo turned the sign-in away"), { signIn: "expired" })
        if (a.type === "oauth" && a.expires < Date.now()) {
          a = { ...a, access: "fresh-" + a.refresh, expires: Date.now() + 3600e3 }
          await client.auth.set({ path: { id: ID }, body: a })
        }
        const h = new Headers(init.headers)
        h.set("authorization", "Bearer " + (a.type === "oauth" ? a.access : a.key))
        h.set("x-models", Object.keys(provider.models).sort().join(","))
        h.set("x-body-type", typeof init.body)
        // $FAKE_H2: the request goes on node:http2 to that origin, as
        // Cursor's plugin runs its chats
        if (process.env.FAKE_H2) return h2(process.env.FAKE_H2, new URL(url).pathname)
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
        // a 401 there: the vendor refused the sign-in
        if (r.status === 401) return { error: "the FakeCo sign-in has expired — sign in again" }
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
          { name: "Week", used: 10, resetsAt: Math.floor(Date.now() / 1000) + 86400, span: 7 * 86400, amount: 120, limit: 1200, unit: "credits" },
          { name: "Extra", used: 250, display: "$2.50", aside: true },
        ],
      }
    },
    // $FAKE_CHECKIN: the plugin presses the daily check-in itself, each
    // press written as a line of the account's key to that file, and the
    // answer said by the key: ck-claim gives 50 on a 3-day streak, ck-done
    // is in already, ck-captcha is asked a captcha, ck-throw can't reach
    // the vendor, ck-odd answers no outcome magpie knows
    ...(process.env.FAKE_CHECKIN
      ? {
          checkin: async (getAuth) => {
            const a = await getAuth()
            const fs = await import("node:fs")
            fs.appendFileSync(process.env.FAKE_CHECKIN, (a.key ?? a.accountId ?? "") + "\n")
            switch (a.key) {
              case "ck-claim":
                return { outcome: "claimed", credit: 50, streak: 3 }
              case "ck-done":
                return { outcome: "done", credit: 50 }
              case "ck-captcha":
                return { outcome: "captcha", message: "slide the puzzle" }
              case "ck-throw":
                throw new Error("FakeCo's check-in is down")
              case "ck-odd":
                return { outcome: "yes" }
            }
            return { outcome: "inactive" }
          },
        }
      : {}),
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
      // fake-claude's credits are a number, discounted; fake-gemini's as
      // WorkBuddy's picker writes them
      if (p.models["fake-claude"]) Object.assign(p.models["fake-claude"], { rate: 0.5, rateWas: 1 })
      if (p.models["fake-gemini"]) p.models["fake-gemini"].rate = "x0.03"
      if (auth?.key === "few") return { "fake-1": p.models["fake-1"] }
      // $FAKE_MODELS: the vendor's list, whose answer names one more model
      if (process.env.FAKE_MODELS && auth) {
        const r = await fetch(process.env.FAKE_MODELS).then((r) => r.text()).catch((e) => {
          // $FAKE_MODELS_THROW: the hook throws, as Cursor's, Grok's and Devin's do
          if (process.env.FAKE_MODELS_THROW === "1") throw e
          return null
        })
        // "own": it hands back a table of its own, saying it fell back, as
        // Command Code's Go and ZCode do
        if (r === null && process.env.FAKE_MODELS_THROW === "own")
          return { "fake-1": { ...p.models["fake-1"] }, [Symbol.for("magpie.fellBack")]: true }
        if (r) p.models["fake-" + r] = { ...p.models["fake-1"], id: "fake-" + r, name: r }
      }
      return p.models
    },
  },
  "chat.headers": async (input, output) => {
    if (input.model.providerID === ID) output.headers["x-plugin-model"] = input.model.id
  },
})
