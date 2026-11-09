// A plugin as OpenCode's are written, signing in to a made-up provider
// whose requests go to $FAKE_BASE; $FAKE_ID names it another's id, as a
// built-in's plugin does.
const ID = process.env.FAKE_ID || "fakeco"

// born is when the host loaded the plugin. The accounts' windows renew at
// times set from it, as a vendor's are: read again, an account renews when
// it did, so two accounts read either side of an hour aren't an hour apart
// to Smart routing, which weighs renewals to the hour.
const born = Date.now()

// fakeWho is who a team signs in as: team@fake, and a team named name/uid
// is name@fake with that uid, as WorkBuddy's plugin keeps one; a team
// named =id is named by that id, its uid too, as the CodeArts plugin's
// OfficeAce card names a new sign-in by the Huawei domain id (#4473)
function fakeWho(team) {
  if (team?.startsWith("=")) return { accountId: team.slice(1), uid: team.slice(1) }
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
    // $FAKE_KIRO: a Claude model named as Kiro's plugin names it, on
    // Anthropic's Messages API
    if (process.env.FAKE_KIRO) cfg.provider[ID].models["claude-opus-5.5"] = { name: "Claude Opus 5.5", provider: { npm: "@ai-sdk/anthropic" }, reasoning: true, modalities: { input: ["text", "image"], output: ["text"] }, limit: { context: 200000, output: 32000 } }
    // $FAKE_RESPONSES: one model more, on OpenAI's Responses (Grok's)
    // $FAKE_FAST=1: fake-1 has a fast one, as Cursor's plugin 0.1.x lists
    // a model's -fast (=tier: see the models hook)
    if (process.env.FAKE_FAST === "1") cfg.provider[ID].models["fake-1-fast"] = { name: "Fake One Fast", limit: { context: 1000, output: 100 } }
    if (process.env.FAKE_RESPONSES) cfg.provider[ID].models["fake-resp"] = { name: "Fake Responses", provider: { npm: "@ai-sdk/openai" }, limit: { context: 4000, output: 400 } }
    // $FAKE_GROK: a Grok model on Responses, as Grok's plugin serves it
    if (process.env.FAKE_GROK) cfg.provider[ID].models["grok-4.7"] = { name: "Grok 4.7", provider: { npm: "@ai-sdk/openai" }, limit: { context: 4000, output: 400 } }
    // $FAKE_SENSENOVA: SenseNova's image model as its plugin lists it from
    // token.sensenova.cn/v1/models, on the provider's openai-compatible;
    // and an image model on a vendor's own API (Anthropic's package), which
    // no images API of the base serves
    if (process.env.FAKE_SENSENOVA) {
      cfg.provider[ID].models["sensenova-u1.5-lite"] = { name: "sensenova-u1.5-lite", limit: { context: 131072, output: 32768 } }
      cfg.provider[ID].models["flux-2"] = { name: "Flux 2", provider: { npm: "@ai-sdk/anthropic" }, limit: { context: 1000, output: 100 } }
    }
    // $FAKE_DECLARED_DRAWS: image models whose names say nothing, on the
    // provider's openai-compatible: one that says it gives images alone,
    // and one that answers in text and images, which chats (maonekone)
    if (process.env.FAKE_DECLARED_DRAWS) {
      cfg.provider[ID].models["jimeng-4.0"] = { name: "Jimeng 4.0", modalities: { input: ["text", "image"], output: ["image"] }, limit: { context: 1000, output: 100 } }
      cfg.provider[ID].models["fake-canvas"] = { name: "Fake Canvas", modalities: { input: ["text"], output: ["text", "image"] }, limit: { context: 1000, output: 100 } }
    }
    // $FAKE_LOOMY: iFlytek's Loomy gateway's three image generators as its
    // /v1/models lists them (#4913), on the provider's openai-compatible;
    // all three draw on its images API
    if (process.env.FAKE_LOOMY) {
      cfg.provider[ID].models["Hy-Image-3.5-preview"] = { name: "Hy-Image-3.5-preview", limit: { context: 131072, output: 32768 } }
      cfg.provider[ID].models["doubao-seedream-5-lite"] = { name: "doubao-seedream-5-lite", limit: { context: 131072, output: 32768 } }
      cfg.provider[ID].models["qwen-image-3.0-pro"] = { name: "qwen-image-3.0-pro", limit: { context: 131072, output: 32768 } }
    }
    // $FAKE_GOOGLE: the provider's models are on Gemini's API, as XYenon's
    // Google AI Studio plugin gives them all (@ai-sdk/google), so it has
    // no OpenAI-compatible model
    if (process.env.FAKE_GOOGLE) cfg.provider[ID].npm = "@ai-sdk/google"
    // $FAKE_VIDEO: a video model, as a Gemini plugin lists Veo: it says it
    // gives video, nothing else
    if (process.env.FAKE_VIDEO) cfg.provider[ID].models["veo-3.1-generate-preview"] = { name: "Veo 3.1", modalities: { input: ["text", "image"], output: ["video"] }, limit: { context: 1000, output: 100 } }
    // $FAKE_GEMINI_DRAWS: image models on Gemini's API, as XYenon's Google
    // AI Studio plugin lists them: one named for images, one that only
    // says it gives them, and an Imagen, which draws on :predict instead
    if (process.env.FAKE_GEMINI_DRAWS) {
      const g = { provider: { npm: "@ai-sdk/google" }, limit: { context: 3000, output: 300 } }
      cfg.provider[ID].models["gemini-3-pro-image-preview"] = { ...g, name: "Nano Banana Pro", modalities: { input: ["text", "image"], output: ["text", "image"] } }
      cfg.provider[ID].models["gemini-3-flash-canvas"] = { ...g, name: "Gemini 3 Flash Canvas", modalities: { input: ["text", "image"], output: ["text", "image"] } }
      cfg.provider[ID].models["imagen-4.0-generate-001"] = { ...g, name: "Imagen 4", modalities: { input: ["text"], output: ["image"] } }
    }
    // $FAKE_SEES: a model on the provider's openai-compatible that takes
    // images, as WorkBuddy's plugin serves its models
    if (process.env.FAKE_SEES) cfg.provider[ID].models["fake-sees"] = { name: "Fake Sees", modalities: { input: ["text", "image"], output: ["text"] }, limit: { context: 1000, output: 100 } }
    // $FAKE_DEEPSEEK: a DeepSeek model, as Cline's cline-pass/deepseek-v4-pro
    if (process.env.FAKE_DEEPSEEK) cfg.provider[ID].models["deepseek-v4-pro"] = { name: "DeepSeek V4 Pro", limit: { context: 1000, output: 100 } }
    // $FAKE_GLM: a GLM model, as Cline's cline-pass/glm-5.3
    if (process.env.FAKE_GLM) {
      cfg.provider[ID].models["glm-5.3"] = { name: "GLM 5.3", limit: { context: 1000, output: 100 } }
      // Cline sends glm-5.3-flash through OpenRouter, not Vercel's gateway
      cfg.provider[ID].models["glm-5.3-flash"] = { name: "GLM 5.3 Flash", limit: { context: 1000, output: 100 } }
    }
    // $FAKE_MINIMAX: MiniMax Code's models as its plugin (minimax 0.1.4)
    // gives them, on Anthropic's API: M3.1-Flash and its -Preview always
    // think at an effort, M3 thinks or doesn't (plugins#316)
    if (process.env.FAKE_MINIMAX) {
      const efforts = Object.fromEntries(["low", "medium", "high", "xhigh", "max"].map((e) => [e, { thinking: { type: "adaptive" }, effort: e }]))
      const mm = { provider: { npm: "@ai-sdk/anthropic" }, reasoning: true, limit: { context: 1048576, output: 524288 } }
      cfg.provider[ID].models["MiniMax-M3.1-Flash"] = { ...mm, name: "M3.1-Flash", variants: efforts }
      cfg.provider[ID].models["MiniMax-M3.1-Flash-Preview"] = { ...mm, name: "M3.1-Flash-Preview", variants: efforts }
      cfg.provider[ID].models["MiniMax-M3"] = { ...mm, name: "MiniMax-M3", variants: { none: { thinking: { type: "disabled" } }, high: { thinking: { type: "adaptive" } } } }
    }
    // $FAKE_ZCODE: GLM-5.3 as ZCode's plugin (zcode 0.1.x) gives it, on
    // Anthropic's API with a thinking budget per level
    if (process.env.FAKE_ZCODE) {
      const budget = { low: 4096, high: 16000, max: 31999 }
      const variants = Object.fromEntries(["low", "high", "max"].map((e) => [e, { thinking: { type: "enabled", budgetTokens: budget[e] } }]))
      cfg.provider[ID].models["GLM-5.3"] = { name: "GLM-5.3", provider: { npm: "@ai-sdk/anthropic" }, reasoning: true, variants, limit: { context: 1000000, output: 128000 } }
      // $FAKE_ZCODE_TRIAL: the Start Plan trial models beside the
      // subscription's, as a ZCode account that once had them lists them
      if (process.env.FAKE_ZCODE_TRIAL) {
        cfg.provider[ID].models["GLM-5.3-Flash-Trial"] = { name: "GLM 5.3 Flash Trial", provider: { npm: "@ai-sdk/anthropic" }, reasoning: true, limit: { context: 1000000, output: 128000 } }
        cfg.provider[ID].models["GLM-5.3-Trial"] = { name: "GLM 5.3 Trial", provider: { npm: "@ai-sdk/anthropic" }, reasoning: true, limit: { context: 1000000, output: 128000 } }
      }
    }
    // $FAKE_SEARCHES: the provider's upstream searches by itself (#2027),
    // as a plugin in front of Claude's API says
    if (process.env.FAKE_SEARCHES) cfg.provider[ID].searches = true
    // $FAKE_DECIDE: the provider serves a decision API too (#1514), asked
    // at its base /systemone through the loader's fetch: fake-clef is a
    // decision model it marks, jev-9 one magpie knows by its name.
    // "models" marks them without saying decide.
    if (process.env.FAKE_DECIDE) {
      if (process.env.FAKE_DECIDE !== "models") cfg.provider[ID].decide = true
      cfg.provider[ID].models["fake-clef"] = { name: "Fake Clef", decides: true, limit: { context: 1000, output: 0 } }
      cfg.provider[ID].models["jev-9"] = { name: "Jev 9", limit: { context: 1000, output: 0 } }
    }
    // $FAKE_OFF: a model that stops thinking at none and one that can't,
    // as Factory's plugin has Kimi K3 and GLM-5.3 (#899)
    if (process.env.FAKE_OFF) {
      const levels = (l) => Object.fromEntries(l.map((e) => [e, { reasoningEffort: e }]))
      cfg.provider[ID].models["fake-off"] = { name: "Fake Off", reasoning: true, variants: levels(["none", "low", "high", "max"]), limit: { context: 1000, output: 100 } }
      cfg.provider[ID].models["fake-on"] = { name: "Fake On", reasoning: true, variants: levels(["low", "high", "max"]), limit: { context: 1000, output: 100 } }
      // Trae's levels as Our Free Model's plugin lists them (nianshou5)
      cfg.provider[ID].models["fake-trae"] = { name: "Fake Trae", reasoning: true, variants: levels(["light", "high", "extra_high"]), limit: { context: 1000, output: 100 } }
    }
    // $FAKE_TEMP: a model whose vendor turns temperature and top_p away,
    // said as Factory 0.1.31's config models say it of its OpenAI route
    // (plugins#154); fake-1 beside it says nothing and takes them
    if (process.env.FAKE_TEMP) {
      cfg.provider[ID].models["fake-cold"] = { name: "Fake Cold", temperature: false, limit: { context: 1000, output: 100 } }
    }
    // $FAKE_ZEN: a model as opencode-zen-free 0.1.3's config hook writes
    // it, from models.opencode.ai on 2026-10-11 (#1595, tkhs101): it
    // takes audio, video and pdf too, at five levels (its api left to ours)
    if (process.env.FAKE_ZEN) {
      cfg.provider[ID].models["muse-spark-1.3-contributor-free"] = {"id":"muse-spark-1.3-contributor-free","name":"Muse Spark 1.3 Free","provider":{"npm":"@ai-sdk/openai"},"limit":{"context":1048576,"output":131072},"reasoning":true,"tool_call":true,"attachment":true,"modalities":{"input":["text","image","audio","video","pdf"],"output":["text"]},"cost":{"input":0,"output":0},"variants":{"minimal":{"reasoningEffort":"minimal"},"low":{"reasoningEffort":"low"},"medium":{"reasoningEffort":"medium"},"high":{"reasoningEffort":"high"},"xhigh":{"reasoningEffort":"xhigh"}},"free":true}
    }
  },
  auth: {
    provider: ID,
    single: process.env.FAKE_KEYLESS === "1",
    methods: [
      { type: "api", label: "API key", ...(process.env.FAKE_KEYLESS === "1" ? { keyless: true, authorize: async (inputs) => ({ type: "success", key: inputs.credential || "public-fixture" }) } : {}) },
      {
        type: "oauth",
        label: "Browser",
        prompts: [
          { type: "select", key: "where", message: "Where?", options: [{ label: "Home", value: "home" }, { label: "Work", value: "work" }] },
          { type: "text", key: "team", message: "Team?", when: { key: "where", op: "eq", value: "work" }, validate: (v) => (v ? undefined : "Required") },
        ],
        // $FAKE_LOOPBACK: the page comes back to a port here, as Devin's
        // and Kiro's plugins sign in
        // $FAKE_SIGNIN: the sign-in asks the vendor there, as a device
        // sign-in asks for its code and trades it for the tokens (#1663)
        authorize: async (inputs) => process.env.FAKE_LOOPBACK ? loopbackSignIn(inputs) : (process.env.FAKE_SIGNIN && (await fetch(process.env.FAKE_SIGNIN + "/device").catch(() => {})), {
          url: "https://fake.invalid/auth?where=" + inputs.where,
          instructions: "Paste the code",
          method: "code",
          // team "cli-opened": the page is open already, as Grok Build's
          // `grok login` opens its own on macOS and Windows
          opened: inputs.team === "cli-opened",
          callback: async (code) =>
            (process.env.FAKE_SIGNIN && (await fetch(process.env.FAKE_SIGNIN + "/token").catch(() => {})), code === "good")
              ? { type: "success", refresh: "r-" + (inputs.team ?? "none"), access: "stale", expires: 0, ...fakeWho(inputs.team) }
              : code === "expired"
                ? { type: "failed", error: "the sign-in page expired" }
                : { type: "failed" },
        }),
      },
    ],
    loader: async (getAuth, provider) => {
      // $FAKE_LOAD_WAIT: the loader takes that many ms (a sign-in renewed
      // on a slow line), as a cold host's first request waits for it
      if (process.env.FAKE_LOAD_WAIT) await new Promise((r) => setTimeout(r, Number(process.env.FAKE_LOAD_WAIT)))
      return {
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
        // $FAKE_RUN: the test this host runs for, so its vendor tells its
        // own requests from a stray that reached a port the kernel handed
        // on (testenv.FakeVendor)
        if (process.env.FAKE_RUN) h.set("x-fake-run", process.env.FAKE_RUN)
        // where magpie said the agent works, as the plugin read it; its own
        // headers go on too, as a plugin that sends on what it got sends
        // them, for the host to take off
        for (const k of ["x-magpie-work-dir", "x-magpie-work-git", "x-magpie-fallback", "x-magpie-session", "x-magpie-subagent"]) if (h.has(k)) h.set("x-saw-" + k.slice(9), h.get(k))
        // $FAKE_QUEUE: that model is queued at the vendor, as Qoder's free
        // models are: waited out (here until magpie gives up) unless magpie
        // said it needn't be, then said at once with the queue's 503
        if (process.env.FAKE_QUEUE && typeof init.body === "string" && JSON.parse(init.body).model === process.env.FAKE_QUEUE) {
          if (h.get("x-magpie-fallback") === "1") {
            return new Response(JSON.stringify({ error: { message: `FakeCo queued the request for ${process.env.FAKE_QUEUE}; try again in 30s`, type: "queued" } }), { status: 503, headers: { "content-type": "application/json", "retry-after": "30" } })
          }
          await new Promise((_, no) => {
            const t = setTimeout(() => no(new Error("FakeCo's queue gave up")), 300e3)
            init.signal?.addEventListener("abort", () => (clearTimeout(t), no(init.signal.reason)))
          })
        }
        // $FAKE_H2: the request goes on node:http2 to that origin, as
        // Cursor's plugin runs its chats
        if (process.env.FAKE_H2) return h2(process.env.FAKE_H2, new URL(url).pathname)
        return fetch(url, { ...init, headers: h })
      },
    }
    },
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
      // $FAKE_WINDOWS: a JSON file of each account's windows, read at
      // each reading, as a vendor's usage page says them now (Factory's
      // plugin with Standard usage only, plugins#349)
      // an IAM id kept as the accountId, the phone told here (#4416)
      const iam = a.accountId?.startsWith("iam::") ? "180****2361" : undefined
      if (process.env.FAKE_WINDOWS) {
        const fs = await import("node:fs")
        const ws = JSON.parse(fs.readFileSync(process.env.FAKE_WINDOWS, "utf8"))[a.accountId]
        if (ws) return { plan: "Fake Pro", windows: ws, user: iam }
      }
      const full = a.accountId === "full@fake"
      return {
        plan: "Fake Pro",
        user: full ? "Full@Fake.example" : iam,
        until: "2030-01-02T03:04:05Z",
        renew: "auto",
        resets: full ? { count: 3, byWindow: true, fiveHour: 2, weekly: 1, team: true, each: [{ window: "weekly" }, { until: "2031-01-02T00:00:00Z", window: "fiveHour" }, { until: 1924992000, window: "fiveHour" }] } : undefined,
        windows: [
          { name: "5 hours", used: full ? 100 : 25, resetsAt: born + 3600e3, span: 5 * 3600, models: ["fake-claude"] },
          // 80 of its credits are lost in two days, after it resets (#2265)
          { name: "Week", used: 10, resetsAt: Math.floor(born / 1000) + 86400, span: 7 * 86400, amount: 120, limit: 1200, unit: "credits",
            runsOutAt: Math.floor(born / 1000) + 2 * 86400, runsOut: 80 },
          // a run-out the host can't read is not told
          { name: "Extra", used: 250, display: "$2.50", aside: true, runsOutAt: "not a date", runsOut: "12" },
        ],
      }
    },
    // $FAKE_CHECKIN: the plugin presses the daily check-in itself, each
    // press written as a line of the account's key to that file (a signed-in
    // account's name/uid, so two of one name tell apart), and the
    // answer said by the key: ck-claim gives 50 on a 3-day streak, ck-done
    // is in already, ck-captcha is asked a captcha, ck-throw can't reach
    // the vendor, ck-odd answers no outcome magpie knows, and ck-until
    // answers what the test wrote to <FAKE_CHECKIN>.until ({outcome, until})
    ...(process.env.FAKE_CHECKIN
      ? {
          checkin: async (getAuth) => {
            const a = await getAuth()
            const fs = await import("node:fs")
            fs.appendFileSync(process.env.FAKE_CHECKIN, (a.key ?? (a.uid ? a.accountId + "/" + a.uid : a.accountId) ?? "") + "\n")
            // $FAKE_CHECKIN_URL: the vendor's check-in, asked at
            // <url>/<key>; a press it doesn't answer 200 failed (#2777)
            if (process.env.FAKE_CHECKIN_URL) {
              const r = await fetch(process.env.FAKE_CHECKIN_URL + "/" + a.key)
              if (!r.ok) throw new Error(`FakeCo's check-in answered ${r.status}`)
              return { outcome: "claimed", credit: 1 }
            }
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
                return { outcome: "yes", message: "the daily credits were refreshed" }
              case "ck-until":
                return JSON.parse(fs.readFileSync(process.env.FAKE_CHECKIN + ".until", "utf8"))
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
      // $FAKE_MODELS_LOG: each time the hook is asked is a line there
      if (process.env.FAKE_MODELS_LOG) (await import("node:fs")).appendFileSync(process.env.FAKE_MODELS_LOG, "asked\n")
      if (auth?.refresh === "r-dead" || auth?.key === "dead") throw new Error("the vendor refused the sign-in")
      if (auth?.refresh === "r-models-gone") throw Object.assign(new Error("the vendor refused the sign-in"), { signIn: "expired" })
      // a sign-in past its time, said in words only, as Grok's plugin says it
      if (auth?.refresh === "r-models-expired") throw new Error("FakeCo's sign-in has expired; run `fake login`")
      // the vendor unreachable: the list kept, as Zed's plugin keeps it
      if (auth?.refresh === "r-unreachable") await fetch("http://127.0.0.1:9/models").catch(() => {})
      if (auth?.type === "oauth" && auth.refresh?.startsWith("rot-")) {
        // $FAKE_MODELS_HOLD: say the hook came ($FAKE_MODELS_HOLD.in), and
        // spend the token only once that file is there
        const hold = process.env.FAKE_MODELS_HOLD
        if (hold) {
          const fs = await import("node:fs")
          fs.writeFileSync(hold + ".in", "")
          while (!fs.existsSync(hold)) await new Promise((r) => setTimeout(r, 10))
        }
        await client.auth.set({ path: { id: ID }, body: { ...auth, refresh: auth.refresh + "+" } })
      }
      // fake-1 costs the plan nothing, as a WorkBuddy model of x0.00
      // credits ($FAKE_NO_FREE: a list with no free model, as ZCode's is)
      if (!process.env.FAKE_NO_FREE) p.models["fake-1"].free = true
      // fake-claude's credits are a number, discounted; fake-gemini's as
      // WorkBuddy's picker writes them
      if (p.models["fake-claude"]) Object.assign(p.models["fake-claude"], { rate: 0.5, rateWas: 1 })
      if (p.models["fake-gemini"]) p.models["fake-gemini"].rate = "x0.03"
      if (auth?.key === "few") return { "fake-1": p.models["fake-1"] }
      // $FAKE_ZEN: a model as opencode-zen-free 0.1.3's models hook lists
      // it, its capabilities as it gives them (#1595): audio and video in,
      // thinking with no levels to pick
      if (process.env.FAKE_ZEN) {
        p.models["mimo-v2.6-flash-free"] = { ...p.models["fake-1"], id: "mimo-v2.6-flash-free", name: "MiMo V2.6 Flash Free", api: { ...p.models["fake-1"].api, id: "mimo-v2.6-flash-free" }, free: true, variants: {},
          capabilities: {"temperature":true,"reasoning":true,"attachment":true,"toolcall":true,"input":{"text":true,"image":true,"audio":true,"video":true,"pdf":false},"output":{"text":true,"image":false,"audio":false,"video":false,"pdf":false},"interleaved":{"field":"reasoning_content"}} }
        // a model as Command Code's plugin lists its plan's (#4436): it
        // says what the model takes (text; audio, video, pdf no) and says
        // nothing of images (m.images not a boolean there)
        p.models["cc-plan-1"] = { ...p.models["fake-1"], id: "cc-plan-1", name: "CC Plan 1", api: { ...p.models["fake-1"].api, id: "cc-plan-1" }, free: true,
          capabilities: {"temperature":true,"reasoning":true,"toolcall":true,"input":{"text":true,"audio":false,"video":false,"pdf":false},"output":{"text":true}} }
      }
      // $FAKE_KINDS: models as XYenon's magpie-google-ai-studio lists them
      // (#3279): the embedding as the report gives it (output text false,
      // nothing else said), one not named an embedding, a TTS with its
      // parseModels' outputs (audio alone), a model that talks and speaks,
      // one that talks and draws
      if (process.env.FAKE_KINDS) {
        const as = (id, output) => ({ ...p.models["fake-1"], id, name: id, api: { ...p.models["fake-1"].api, id }, free: false,
          capabilities: { ...p.models["fake-1"].capabilities, input: { text: true }, output } })
        p.models["gemini-embedding-001"] = as("gemini-embedding-001", { text: false })
        p.models["bge-m3"] = as("bge-m3", { text: false })
        p.models["gemini-2.5-flash-preview-tts"] = as("gemini-2.5-flash-preview-tts", { text: false, image: false, audio: true, video: false, pdf: false })
        p.models["gemini-2.5-flash-native-audio"] = as("gemini-2.5-flash-native-audio", { text: true, image: false, audio: true, video: false, pdf: false })
        p.models["gemini-2.5-flash-image"] = as("gemini-2.5-flash-image", { text: true, image: true, audio: false, video: false, pdf: false })
      }
      // $FAKE_SEARCHER: one more model, that searches the web by itself
      // when asked in plain words, marked on the list the hook gives, as
      // Trae CN's search_agent ("Search") is listed beside its chat models
      if (process.env.FAKE_SEARCHER) p.models["fake-search"] = { ...p.models["fake-1"], id: "fake-search", name: "Fake Search", api: { ...p.models["fake-1"].api, id: "fake-search" }, free: false, searches: true }
      // $FAKE_DECIDE: one more decision model, marked on the list the hook
      // gives, as a plugin that reads its vendor's list marks one
      if (process.env.FAKE_DECIDE) p.models["fake-judge"] = { ...p.models["fake-1"], id: "fake-judge", name: "Fake Judge", api: { ...p.models["fake-1"].api, id: "fake-judge" }, free: false, decides: true }
      // $FAKE_FAST=tier: as Cursor's plugin 0.2.x lists a model, once per
      // context size, and from 0.2.2 says fast on the sizes Cursor has a
      // fast variant at (Opus 5.5's 300k, not its 1m); no -fast model
      if (process.env.FAKE_FAST === "tier") {
        const sized = (id, name) => ({ ...p.models["fake-1"], id, name, api: { ...p.models["fake-1"].api, id } })
        p.models["fake-opus@300k"] = { ...sized("fake-opus@300k", "Fake Opus @ 300k"), fast: true }
        p.models["fake-opus@1m"] = sized("fake-opus@1m", "Fake Opus @ 1m")
      }
      // $FAKE_TIERS: fake-1 and fake-resp say the service tiers their vendor
      // serves them at (#2514), one as an object as Codex's catalog writes it
      if (process.env.FAKE_TIERS) {
        p.models["fake-1"] = { ...p.models["fake-1"], serviceTiers: ["priority", { id: "ultrafast" }, "", 7, "priority"] }
        if (p.models["fake-resp"]) p.models["fake-resp"] = { ...p.models["fake-resp"], serviceTiers: ["ultrafast"] }
      }
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
