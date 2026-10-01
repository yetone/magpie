// magpie's plugin host: runs OpenCode (v1) server plugins under Bun and
// answers magpie over stdin/stdout, one JSON message a line.
//
// magpie → host: {id, method, params}; the host answers {id, result} or
// {id, error}. A fetch streams its reply first: {id, event: "head", status,
// headers}, then {id, event: "chunk", data} (base64) as the body comes, then
// {id, result: null}. {method: "abort", params: {id}} cancels one.
// host → magpie, unasked: {event: "log", level, message} and {event:
// "toast", ...} (what a plugin logs or shows), {event: "auth", provider}
// (a sign-in the host saved or refreshed).
//
// The plugins get what OpenCode hands them (PluginInput): a client whose
// auth.set saves to magpie's plugin-auth.json in OpenCode's auth.json shape,
// tui.showToast and app.log, config.get; Bun's $; the folder magpie keeps
// its files in. What else a plugin asks the client for answers {data:
// undefined}.
//
// A provider may be signed in to more than once: the first account is kept
// under the provider's id, as OpenCode keeps it, the others under
// id#<slot>. Everything a request does runs in its account's scope, so the
// plugin's getAuth, client.auth.set and loader see that account alone.

import { AsyncLocalStorage } from "node:async_hooks"
import fs from "node:fs"
import path from "node:path"
import readline from "node:readline"
import { pathToFileURL } from "node:url"

const rpcWrite = process.stdout.write.bind(process.stdout)
const send = (msg) => rpcWrite(JSON.stringify(msg) + "\n")

// A plugin writing to stdout would break the protocol: everything it
// prints goes to stderr, which magpie logs.
const toErr = (...xs) => process.stderr.write(xs.map((x) => (typeof x === "string" ? x : Bun.inspect(x))).join(" ") + "\n")
console.log = console.info = console.debug = console.warn = toErr
process.stdout.write = (chunk, enc, cb) => process.stderr.write(chunk, enc, cb)

let authPath = ""
let modelsDevPath = ""
let directory = process.cwd()
let userConfig = {}
const hooks = [] // {spec, hooks}
const loaded = [] // {spec, id, error}
const loaders = new Map() // account → options the auth loader returned
const sessions = new Map() // oauth sign-in in progress → its authorize result
const inflight = new Map() // fetch id → AbortController
let config = { provider: {} } // what the plugins' config hooks made of it

// ---- a provider's own proxy ---------------------------------------------------

// A request made for a provider or an account with a proxy of its own
// (#237) goes through it, and so does every fetch the plugin makes for
// it; one for a provider set to "direct" goes through none. Bun reads
// *_PROXY once, and a fetch given no proxy option can't be told to skip
// them, so the host is started with them as MAGPIE_*_PROXY and each
// fetch is given magpie's own here when it has none of its own.
// Loopback never goes through one.
const via = new AsyncLocalStorage() // the proxy: "", "direct" or its URL
const bunFetch = globalThis.fetch

function proxyVar(k) {
  return process.env["MAGPIE_" + k] || process.env["MAGPIE_" + k.toLowerCase()] || ""
}

const globalProxy = {
  https: proxyVar("HTTPS_PROXY") || proxyVar("ALL_PROXY"),
  http: proxyVar("HTTP_PROXY") || proxyVar("ALL_PROXY"),
  no: proxyVar("NO_PROXY")
    .split(",")
    .map((s) => s.trim().toLowerCase().replace(/^\*?\./, ""))
    .filter(Boolean),
}

function hostOf(input) {
  try {
    const u = new URL(typeof input === "string" || input instanceof URL ? input : input.url)
    return { protocol: u.protocol, host: u.hostname.toLowerCase().replace(/^\[|\]$/g, "") }
  } catch {
    return null
  }
}

function loopback(host) {
  return host === "localhost" || host.endsWith(".localhost") || host === "::1" || /^127\./.test(host)
}

// proxyFor is the proxy a fetch of input goes through, "" for none.
function proxyFor(input) {
  const u = hostOf(input)
  if (!u || loopback(u.host)) return ""
  const own = via.getStore()
  if (own === "direct") return ""
  if (own) return own
  if (globalProxy.no.some((n) => n === "*" || u.host === n || u.host.endsWith("." + n))) return ""
  return u.protocol === "https:" ? globalProxy.https : u.protocol === "http:" ? globalProxy.http : ""
}

// reach is what a check's fetches came to: whether one got an answer, and
// the first that got none
const reach = new AsyncLocalStorage()

function sent(input, init) {
  if (init && "proxy" in init) return bunFetch(input, init)
  const p = proxyFor(input)
  return p ? bunFetch(input, { ...init, proxy: p }) : bunFetch(input, init)
}

// listing is what a models hook's fetches came to: whether it asked any,
// and whether the last it asked answered
const listing = new AsyncLocalStorage()

function asked(input, init) {
  const l = listing.getStore()
  if (!l) return sent(input, init)
  l.tried = true
  return sent(input, init).then(
    (res) => ((l.lastOk = res.ok), res),
    (e) => {
      l.lastOk = false
      throw e
    },
  )
}

globalThis.fetch = Object.assign(function fetch(input, init) {
  const r = reach.getStore()
  if (!r) return asked(input, init)
  return asked(input, init).then(
    (res) => ((r.reached = true), res),
    (e) => {
      r.failed ??= e
      throw e
    },
  )
}, bunFetch)

// ---- auth.json ---------------------------------------------------------------

function readAuth() {
  let text
  for (let i = 0; ; i++) {
    try {
      text = fs.readFileSync(authPath, "utf8")
      break
    } catch (e) {
      if (e?.code === "ENOENT") return {}
      // Windows refuses a file being renamed over for a moment: read as
      // none, the next setAuth would write the others' accounts away
      if (i >= 50) throw e
      pause(10)
    }
  }
  try {
    const v = JSON.parse(text)
    return v && typeof v === "object" ? v : {}
  } catch {
    return {}
  }
}

function writeAuth(all) {
  fs.mkdirSync(path.dirname(authPath), { recursive: true })
  const tmp = authPath + ".tmp-" + process.pid
  fs.writeFileSync(tmp, JSON.stringify(all, null, 2) + "\n", { mode: 0o600 })
  // and a file held open by a reader refuses to be renamed over
  for (let i = 0; ; i++) {
    try {
      return fs.renameSync(tmp, authPath)
    } catch (e) {
      if (i >= 50 || !["EPERM", "EACCES", "EBUSY"].includes(e?.code)) throw e
      pause(10)
    }
  }
}

function pause(ms) {
  Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, ms)
}

function setAuth(key, info) {
  const all = readAuth()
  all[key] = info
  writeAuth(all)
  loaders.delete(key)
  send({ event: "auth", provider: providerOf(key), account: key })
}

function removeAuth(key) {
  const all = readAuth()
  delete all[key]
  writeAuth(all)
  loaders.delete(key)
  send({ event: "auth", provider: providerOf(key), account: key })
}

// ---- accounts ----------------------------------------------------------------

// scope is the account a request is for: {provider, key}.
const scope = new AsyncLocalStorage()

const providerOf = (key) => key.split("#")[0]

// accountsOf are the keys provider's accounts are kept under, the one under
// its own id first.
function accountsOf(all, provider) {
  return Object.keys(all)
    .filter((k) => k === provider || k.startsWith(provider + "#"))
    .sort((a, b) => (a === provider ? -1 : b === provider ? 1 : a < b ? -1 : a > b ? 1 : 0))
}

// keyFor is where the plugin's id is kept in this scope: the scope's
// account when the plugin names the scope's provider, else its own id.
function keyFor(id) {
  const s = scope.getStore()
  return s && s.provider === id ? s.key : id
}

// accountKey is the account a request names, the provider's first when it
// names none.
function accountKey(provider, account) {
  if (account && providerOf(account) === provider) return account
  return accountsOf(readAuth(), provider)[0] ?? provider
}

// freshKey is where a new sign-in to provider goes: its own id while that
// is free.
function freshKey(provider) {
  const all = readAuth()
  if (!(provider in all)) return provider
  for (;;) {
    const k = provider + "#" + Math.random().toString(36).slice(2, 8)
    if (!(k in all)) return k
  }
}

function inScope(provider, key, fn) {
  return scope.run({ provider, key }, fn)
}

function whoOf(a) {
  return a?.accountId ?? a?.metadata?.email ?? a?.email ?? ""
}

// hintOf tells an account with no id from another: the end of its key.
function hintOf(a) {
  return a?.type === "api" && typeof a.key === "string" && a.key.length >= 12 ? a.key.slice(-4) : ""
}

function secretOf(a) {
  return a?.type === "oauth" ? a.refresh ?? a.access ?? "" : a?.key ?? ""
}

// settle keeps a sign-in just saved at key once: one to an account already
// signed in (the same account id, else the same secret) replaces that one's
// and goes. It gives where it is kept.
function settle(provider, key) {
  const all = readAuth()
  const now = all[key]
  if (!now) return key
  const who = whoOf(now)
  const secret = secretOf(now)
  for (const k of accountsOf(all, provider)) {
    if (k === key) continue
    const was = all[k]
    if ((who && whoOf(was) === who) || (!who && !whoOf(was) && secret && secretOf(was) === secret)) {
      all[k] = now
      delete all[key]
      writeAuth(all)
      loaders.delete(k)
      loaders.delete(key)
      send({ event: "auth", provider, account: k })
      return k
    }
  }
  return key
}

// ---- the client plugins are given --------------------------------------------

function stub(name) {
  return new Proxy(async () => ({ data: undefined }), {
    get(_, key) {
      if (key === "then") return undefined
      return stub(name + "." + String(key))
    },
    apply() {
      return Promise.resolve({ data: undefined })
    },
  })
}

function makeClient() {
  const known = {
    auth: {
      // OpenCode's SDK: auth.set({path: {id}, body})
      set: async (opts) => {
        const id = opts?.path?.id ?? opts?.id ?? opts?.providerID
        const body = opts?.body ?? opts?.auth
        if (typeof id === "string" && body && typeof body === "object") {
          // OpenCode keeps what the plugin gives it, saving refreshed
          // tokens over the old; nothing is merged. It goes to the
          // account the request is for.
          const key = keyFor(id)
          const prev = readAuth()[key]
          if (JSON.stringify(prev) !== JSON.stringify(body)) setAuth(key, body)
        }
        return { data: true }
      },
      remove: async (opts) => {
        const id = opts?.path?.id ?? opts?.id
        if (typeof id === "string") removeAuth(keyFor(id))
        return { data: true }
      },
    },
    tui: {
      showToast: async (opts) => {
        const b = opts?.body ?? opts ?? {}
        send({ event: "toast", title: b.title ?? "", message: String(b.message ?? ""), variant: b.variant ?? "info" })
        return { data: true }
      },
    },
    app: {
      log: async (opts) => {
        const b = opts?.body ?? opts ?? {}
        send({ event: "log", level: b.level ?? "info", message: `[${b.service ?? "plugin"}] ${b.message ?? ""}` })
        return { data: true }
      },
    },
    config: {
      get: async () => ({ data: config }),
    },
  }
  const wrap = (obj, name) =>
    new Proxy(obj, {
      get(target, key) {
        if (key in target) {
          const v = target[key]
          return v && typeof v === "object" ? wrap(v, name + "." + String(key)) : v
        }
        if (key === "then") return undefined
        return stub(name + "." + String(key))
      },
    })
  return wrap(known, "client")
}

// ---- loading plugins ---------------------------------------------------------

const INDEX_FILES = ["index.ts", "index.tsx", "index.js", "index.mjs", "index.cjs"]

function readJSON(file) {
  try {
    return JSON.parse(fs.readFileSync(file, "utf8"))
  } catch {
    return undefined
  }
}

// entries are the files a plugin's server side may load from, as
// OpenCode looks for them: the package's exports["./server"], its main,
// exports["."], else an index file; a path to a file is that file. A
// package whose ./server is written for OpenCode's next plugin API
// (Plugin.define, opencode-gemini-auth 2) keeps its v1 plugin at main.
function entries(target) {
  const stat = fs.statSync(target, { throwIfNoEntry: false })
  if (!stat) throw new Error(`no such plugin: ${target}`)
  if (!stat.isDirectory()) return [target]
  const out = []
  const add = (v) => typeof v === "string" && v.trim() && out.push(path.resolve(target, v.trim()))
  const pick = (x) => (typeof x === "string" ? x : x?.import ?? x?.default)
  const pkg = readJSON(path.join(target, "package.json"))
  if (pkg) {
    const ex = pkg.exports && typeof pkg.exports === "object" && !Array.isArray(pkg.exports) ? pkg.exports : undefined
    if (ex) add(pick(ex["./server"]))
    add(pkg.main)
    if (ex) add(pick(ex["."]))
    if (typeof pkg.exports === "string") add(pkg.exports)
  }
  for (const f of INDEX_FILES) {
    const p = path.join(target, f)
    if (fs.existsSync(p)) out.push(p)
  }
  if (out.length === 0) throw new Error(`plugin ${target} has no entry (package.json main, exports or index file)`)
  return [...new Set(out)]
}

// servers are the plugin functions a module exports: default {id?,
// server} (v1), else every function it exports, or {server} objects
// (legacy), each once.
function servers(mod) {
  const d = mod.default
  if (d && typeof d === "object" && ("server" in d || "id" in d || "tui" in d)) {
    return typeof d.server === "function" ? [d.server] : []
  }
  const seen = new Set()
  const out = []
  for (const v of Object.values(mod)) {
    if (seen.has(v)) continue
    seen.add(v)
    if (typeof v === "function") out.push(v)
    else if (v && typeof v === "object" && typeof v.server === "function") out.push(v.server)
  }
  return out
}

async function loadPlugins(list) {
  const input = {
    client: makeClient(),
    project: { id: "magpie", worktree: directory, vcs: undefined, time: { created: Date.now() } },
    directory,
    worktree: directory,
    experimental_workspace: { register() {} },
    serverUrl: new URL("http://127.0.0.1:4096"),
    $: Bun.$,
  }
  for (const p of list) {
    try {
      let fns = []
      for (const file of entries(p.target)) {
        fns = servers(await import(pathToFileURL(file).href))
        if (fns.length) break
      }
      if (fns.length === 0) throw new Error("exports no OpenCode plugin function")
      for (const fn of fns) hooks.push({ spec: p.spec, target: p.target, hooks: (await fn(input, p.options)) ?? {} })
      loaded.push({ spec: p.spec })
    } catch (e) {
      loaded.push({ spec: p.spec, error: String(e?.stack ?? e) })
    }
  }
  config = { provider: {}, ...structuredClone(userConfig) }
  for (const h of hooks) {
    if (typeof h.hooks.config !== "function") continue
    try {
      await h.hooks.config(config)
    } catch (e) {
      send({ event: "log", level: "error", message: `${h.spec}: config hook: ${e?.message ?? e}` })
    }
  }
}

// auths are the auth hooks by provider, the last plugin to name one
// winning, as in OpenCode.
function auths() {
  const m = new Map()
  for (const h of hooks) if (h.hooks.auth?.provider) m.set(h.hooks.auth.provider, { spec: h.spec, target: h.target, auth: h.hooks.auth })
  return m
}

function authOf(provider) {
  const a = auths().get(provider)
  if (!a) throw new Error(`no plugin signs in to ${provider}`)
  return a.auth
}

// ---- providers and models ----------------------------------------------------

let modelsDev
function mdev() {
  if (modelsDev === undefined) modelsDev = (modelsDevPath && readJSON(modelsDevPath)) || {}
  return modelsDev
}

function cost(c) {
  return {
    input: c?.input ?? 0,
    output: c?.output ?? 0,
    cache: { read: c?.cache_read ?? c?.cache?.read ?? 0, write: c?.cache_write ?? c?.cache?.write ?? 0 },
  }
}

// fromModelsDev is a models.dev model as OpenCode's provider list has it.
function fromModelsDev(p, m) {
  return {
    id: m.id,
    providerID: p.id,
    name: m.name ?? m.id,
    family: m.family,
    api: { id: m.id, url: m.provider?.api ?? p.api ?? "", npm: m.provider?.npm ?? p.npm ?? "@ai-sdk/openai-compatible" },
    status: m.status ?? "active",
    headers: {},
    options: {},
    cost: cost(m.cost),
    limit: { context: m.limit?.context ?? 0, input: m.limit?.input, output: m.limit?.output ?? 0 },
    capabilities: {
      temperature: m.temperature ?? false,
      reasoning: m.reasoning ?? false,
      attachment: m.attachment ?? false,
      toolcall: m.tool_call ?? true,
      input: {
        text: true,
        image: (m.modalities?.input ?? []).includes("image"),
        audio: (m.modalities?.input ?? []).includes("audio"),
        video: (m.modalities?.input ?? []).includes("video"),
        pdf: (m.modalities?.input ?? []).includes("pdf"),
      },
      output: { text: true, image: false, audio: false, video: false, pdf: false },
      interleaved: m.interleaved ?? false,
    },
    release_date: m.release_date ?? "",
    variants: {},
  }
}

// info is the provider as OpenCode builds it: models.dev's entry, what
// the config (with the plugins' config hooks) says of it, the plugin's
// provider.models hook.
// info is what OpenCode knows of provider id, its models as the plugin
// lists them for account key (the first when none); strict fails where the
// plugin's list does, rather than keeping the list it was given.
async function info(id, key, strict) {
  const md = mdev()[id]
  const cfg = config.provider?.[id]
  const out = {
    id,
    name: cfg?.name ?? md?.name ?? id,
    source: md ? "api" : "config",
    env: cfg?.env ?? md?.env ?? [],
    options: { ...(cfg?.options ?? {}) },
    npm: cfg?.npm ?? md?.npm,
    api: cfg?.api ?? md?.api,
    models: {},
  }
  if (md) for (const m of Object.values(md.models ?? {})) out.models[m.id] = fromModelsDev(md, m)
  for (const [key, m] of Object.entries(cfg?.models ?? {})) {
    const was = out.models[m.id ?? key]
    const npm = m.provider?.npm ?? cfg.npm ?? was?.api.npm ?? md?.npm ?? "@ai-sdk/openai-compatible"
    out.models[key] = {
      ...(was ?? {}),
      id: key,
      providerID: id,
      name: m.name ?? was?.name ?? key,
      api: { id: m.id ?? was?.api.id ?? key, url: m.provider?.api ?? cfg.api ?? was?.api.url ?? md?.api ?? "", npm },
      cost: m.cost ? cost(m.cost) : was?.cost ?? cost(),
      limit: { ...(was?.limit ?? { context: 0, output: 0 }), ...(m.limit ?? {}) },
      options: { ...(was?.options ?? {}), ...(m.options ?? {}) },
      headers: { ...(was?.headers ?? {}), ...(m.headers ?? {}) },
      capabilities: {
        ...(was?.capabilities ?? { temperature: true, toolcall: true, input: { text: true }, output: { text: true } }),
        ...(m.reasoning !== undefined ? { reasoning: m.reasoning } : {}),
        ...(m.attachment !== undefined ? { attachment: m.attachment } : {}),
        ...(m.tool_call !== undefined ? { toolcall: m.tool_call } : {}),
        ...(m.modalities?.input ? { input: Object.fromEntries(["text", "image", "audio", "video", "pdf"].map((k) => [k, m.modalities.input.includes(k)])) } : {}),
      },
      variants: m.variants ?? was?.variants ?? {},
    }
  }
  for (const h of hooks) {
    const ph = h.hooks.provider
    if (ph?.id !== id || typeof ph.models !== "function") continue
    const all = readAuth()
    const k = key ?? accountsOf(all, id)[0] ?? id
    try {
      const given = JSON.parse(JSON.stringify(out))
      const l = { tried: false, lastOk: false }
      const next = await listing.run(l, () => inScope(id, k, () => ph.models(given, { auth: all[k] })))
      // a hook that asked its vendor, got no list and gave back the one it
      // was given fell back: magpie keeps the list it had, as a built-in
      // whose fetch failed keeps the one it fetched last; so does one that
      // says so, handing back a list of its own (Symbol.for("magpie.fellBack")
      // on it: Command Code's Go table, ZCode's models)
      out.fellBack = (next === given.models && l.tried && !l.lastOk) || next?.[Symbol.for("magpie.fellBack")] === true
      out.models = Object.fromEntries(Object.entries(next ?? {}).map(([k, m]) => [k, { ...m, id: k, providerID: id }]))
    } catch (e) {
      // an error the models hook throws may say what it means for the
      // sign-in, as an answer's X-Magpie-Sign-In does: a built-in whose
      // model list the vendor refused marked the account
      if (["expired", "kept", "renewed"].includes(e?.signIn) && all[k]) send({ event: "signIn", provider: id, account: k, said: e.signIn })
      // strict (a move's check) fails on what the account can't do, but
      // a sign-in the vendor refused isn't that: it is marked, as the
      // built-in marked it, and goes along untried, as a lapsed one does
      if (strict && !(e?.signIn === "expired" && all[k])) throw e
      const r = reach.getStore()
      if (r && e?.signIn === "expired") r.refused ??= e?.message ?? String(e)
      // a hook that threw on its vendor's failure has no list to tell:
      // magpie keeps the one it had, as for one that gave its defaults back
      out.fellBack = true
      send({ event: "log", level: "error", message: `${h.spec}: provider.models: ${e?.message ?? e}` })
    }
  }
  for (const [k, m] of Object.entries(cfg?.models ?? {})) if (m?.disabled) delete out.models[k]
  return out
}

// shown is a plugin's string as magpie shows it: trimmed and at most n
// characters, "" for anything else.
const shown = (v, n) => (typeof v === "string" ? v.trim().slice(0, n) : "")

// methods are the auth hook's ways to sign in. An "api" one may say what
// its key looks like (placeholder, magpie's own: OpenCode's dialog says
// "API key"); its label titles the key's field, as OpenCode's does.
function methods(auth) {
  return (auth.methods ?? []).map((m) => ({
    type: m.type,
    label: m.label,
    ...(m.type === "api" && shown(m.placeholder, 200) ? { placeholder: shown(m.placeholder, 200) } : {}),
  }))
}

// iconOf is the picture a plugin gives its provider, magpie's own field
// (OpenCode's providers have none): the auth hook's icon, else
// package.json's magpie.icon. An https URL or a data:image URI; magpie
// checks and keeps it (internal/provider), nothing is fetched here.
function iconOf(a) {
  const ok = (v) => {
    const s = typeof v === "string" ? v.trim() : ""
    // a data URI of a picture over 1 MB, magpie's most, isn't carried
    return s.length <= 3 << 19 && /^(https:\/\/|data:image\/)/i.test(s) ? s : ""
  }
  const own = ok(a.auth.icon)
  if (own || !a.target) return own
  const dir = fs.statSync(a.target, { throwIfNoEntry: false })?.isDirectory() ? a.target : path.dirname(a.target)
  return ok(readJSON(path.join(dir, "package.json"))?.magpie?.icon)
}

async function providers() {
  const stored = readAuth()
  const out = []
  for (const [id, a] of auths()) {
    const keys = accountsOf(stored, id)
    const p = await info(id)
    // each account's own models, as the built-ins read each account's: a
    // plan may serve fewer, or others, than the first account's. One that
    // can't be read is taken to have them all, or the ones it was last
    // told to have, as a built-in account whose fetch failed keeps its own.
    const own = await Promise.all(keys.slice(1).map((k) => info(id, k, true).catch(() => ({ failed: true }))))
    const models = { ...p.models }
    for (const q of own) for (const [k, m] of Object.entries(q?.models ?? {})) models[k] ??= m
    const ids = (q) => Object.values(q.models).filter((m) => m.status !== "deprecated").map((m) => m.id)
    const first = stored[keys[0]]
    out.push({
      id,
      spec: a.spec,
      name: p.name,
      npm: p.npm ?? "",
      api: p.api ?? "",
      methods: methods(a.auth),
      icon: iconOf(a),
      usage: typeof a.auth.usage === "function",
      signedIn: keys.length > 0,
      authType: first?.type ?? "",
      accountId: whoOf(first),
      fellBack: keys.length > 0 && !!p.fellBack,
      accounts: keys.map((k, i) => ({
        key: k,
        type: stored[k]?.type ?? "",
        accountId: whoOf(stored[k]),
        hint: hintOf(stored[k]),
        models: own.length === 0 ? undefined : i === 0 ? ids(p) : own[i - 1]?.models ? ids(own[i - 1]) : undefined,
        fellBack: i === 0 ? !!p.fellBack : !!(own[i - 1]?.fellBack || own[i - 1]?.failed),
      })),
      models: Object.values(models)
        .filter((m) => m.status !== "deprecated")
        .map((m) => ({
          id: m.id,
          name: m.name,
          npm: m.api?.npm ?? p.npm ?? "",
          url: m.api?.url ?? "",
          apiId: m.api?.id ?? m.id,
          context: m.limit?.context ?? 0,
          input: m.limit?.input ?? 0,
          output: m.limit?.output ?? 0,
          reasoning: !!m.capabilities?.reasoning,
          image: !!m.capabilities?.input?.image,
          imageSaid: typeof m.capabilities?.input?.image === "boolean",
          released: m.release_date ?? "",
          cost: m.cost,
          variants: Object.keys(m.variants ?? {}),
          free: m.free === true,
        })),
    })
  }
  return out
}

// ---- signing in --------------------------------------------------------------

function applies(prompt, inputs) {
  if (prompt.when) {
    const v = inputs[prompt.when.key]
    if (v === undefined) return false
    const eq = v === prompt.when.value
    if (prompt.when.op === "eq" ? !eq : eq) return false
  }
  if (typeof prompt.condition === "function" && !prompt.condition(inputs)) return false
  return true
}

// nextPrompt is the method's next question for inputs so far, as
// OpenCode's CLI asks them: in order, those whose when/condition hold.
function nextPrompt(provider, index, inputs) {
  const m = authOf(provider).methods[index]
  if (!m) throw new Error(`no sign-in method ${index} for ${provider}`)
  for (const p of m.prompts ?? []) {
    if (p.key in inputs) continue
    if (!applies(p, inputs)) continue
    return {
      type: p.type,
      key: p.key,
      message: p.message,
      placeholder: p.placeholder ?? "",
      options: p.type === "select" ? p.options.map((o) => ({ label: o.label, value: o.value, hint: o.hint ?? "" })) : undefined,
    }
  }
  return null
}

function validate(provider, index, key, value) {
  const p = (authOf(provider).methods[index]?.prompts ?? []).find((x) => x.key === key)
  if (!p || typeof p.validate !== "function") return null
  return p.validate(value) ?? null
}

// save keeps a successful sign-in as OpenCode's CLI does, at key (or, for
// a sign-in the plugin says is another provider's, a new account of
// that one). It gives the provider and where the account is kept.
function save(provider, key, result, inputs, apiKey) {
  const id = result.provider ?? provider
  if (id !== provider) key = freshKey(id)
  if ("refresh" in result) {
    const { type: _t, provider: _p, refresh, access, expires, ...extra } = result
    setAuth(key, { type: "oauth", refresh, access, expires, ...extra })
  } else if ("key" in result || apiKey) {
    const md = { ...(inputs && Object.keys(inputs).length ? inputs : {}), ...(result.metadata ?? {}) }
    setAuth(key, { type: "api", key: result.key ?? apiKey, ...(Object.keys(md).length ? { metadata: md } : {}) })
  }
  return { provider: id, account: settle(id, key) }
}

// signInKey is where a sign-in goes: the account named, else a new one.
function signInKey(provider, account) {
  return account && account !== "new" && providerOf(account) === provider ? account : freshKey(provider)
}

let nextSession = 1

async function authorize({ provider, method, inputs, account }) {
  const m = authOf(provider).methods[method]
  if (!m) throw new Error(`no sign-in method ${method} for ${provider}`)
  if (m.type !== "oauth") throw new Error("not an oauth method")
  const key = signInKey(provider, account)
  // the inputs go only to a method that asks something, as OpenCode's TUI
  // (/connect) passes them: an object with none is how its CLI (opencode
  // auth login) calls, and a plugin told so asks its questions on the
  // terminal, magpie's stdin here (opencode-antigravity-auth waited on its
  // "Project ID" prompt, the sign-in never starting)
  const a = await inScope(provider, key, () => m.authorize(m.prompts?.length ? inputs ?? {} : undefined))
  const session = String(nextSession++)
  sessions.set(session, { provider, key, a, inputs })
  return { session, url: a.url ?? "", instructions: a.instructions ?? "", method: a.method }
}

// failed is a sign-in's failure, with why, where the plugin tells it
// ({ type: "failed", error: "…" }; OpenCode's own result has no reason).
function failed(r) {
  const why = typeof r?.error === "string" ? r.error.trim() : r?.error instanceof Error ? r.error.message : ""
  return why ? { ok: false, error: why.slice(0, 500) } : { ok: false }
}

async function callback({ session, code }) {
  const s = sessions.get(session)
  if (!s) throw new Error("no such sign-in")
  sessions.delete(session)
  const r = await inScope(s.provider, s.key, () => (s.a.method === "code" ? s.a.callback(code ?? "") : s.a.callback()))
  if (!r || r.type !== "success") return failed(r)
  return { ok: true, ...save(s.provider, s.key, r, undefined) }
}

async function apiKey({ provider, method, inputs, key, account }) {
  const m = authOf(provider).methods[method]
  if (!m || m.type !== "api") throw new Error("not an API key method")
  const at = signInKey(provider, account)
  if (typeof m.authorize !== "function") {
    const md = inputs && Object.keys(inputs).length ? { metadata: inputs } : {}
    setAuth(at, { type: "api", key, ...md })
    return { ok: true, provider, account: settle(provider, at) }
  }
  const r = await inScope(provider, at, () => m.authorize(inputs ?? {}))
  if (!r || r.type !== "success") return failed(r)
  return { ok: true, ...save(provider, at, r, inputs, key) }
}

// ---- requests ----------------------------------------------------------------

// options is what the provider's auth loader returned for the account at
// key, run once per sign-in as OpenCode runs it once per start. Run in
// the account's scope, whatever the loader keeps (its fetch) saves to it.
async function options(provider, key) {
  if (loaders.has(key)) return loaders.get(key)
  const a = auths().get(provider)?.auth
  const stored = readAuth()[key]
  let opts = {}
  if (a?.loader && stored) {
    const p = await info(provider, key)
    opts = (await inScope(provider, key, () => a.loader(async () => readAuth()[key], JSON.parse(JSON.stringify(p))))) ?? {}
  }
  if (!opts.apiKey && stored?.type === "api") opts = { ...opts, apiKey: stored.key }
  loaders.set(key, opts)
  return opts
}

async function load({ provider, account }) {
  const key = accountKey(provider, account)
  const o = await inScope(provider, key, () => options(provider, key))
  return {
    baseURL: typeof o.baseURL === "string" ? o.baseURL : "",
    apiKey: typeof o.apiKey === "string" ? o.apiKey : "",
    headers: o.headers && typeof o.headers === "object" ? o.headers : {},
    fetch: typeof o.fetch === "function",
  }
}

// ---- usage -------------------------------------------------------------------

// usage is how much of its allowance the account at key has used, as the
// plugin's auth.usage says (magpie's own hook, which OpenCode ignores):
//   auth.usage(getAuth, provider) → {
//     plan?, user? (the account as the service names it), until?,
//     renew?: "auto" | "off", balance?, error?,
//     resets?: { count, until?, byWindow?, fiveHour?, weekly? } (the
//       rate-limit resets the account may spend),
//     windows?: [{ name, used (percent, 0–100), resetsAt? (ISO or ms),
//       resetSecs?, display?, span? (seconds the window runs), model? (a
//       word in the ids of the only models it counts), models? / notModels?
//       (the ids it counts, or all but these), aside? (using it up doesn't
//       stop the account) }],
//     signIn?: "expired" | "kept" | "renewed" (what the read means for the
//       account's sign-in, as a model request's X-Magpie-Sign-In says;
//       without it an error saying to sign in again marks the account and
//       a clean read clears it)
//   }
// Run in the account's scope, a token it renews is saved to that account.
async function usage({ provider, account, proxy }) {
  return via.run(proxy ?? "", () => usageOf(provider, account))
}

async function usageOf(provider, account) {
  const a = auths().get(provider)?.auth
  if (typeof a?.usage !== "function") throw new Error(`${provider}'s plugin doesn't tell its usage`)
  const key = accountKey(provider, account)
  if (!readAuth()[key]) throw new Error("not signed in")
  const p = await info(provider, key)
  const u = (await inScope(provider, key, () => a.usage(async () => readAuth()[key], JSON.parse(JSON.stringify(p))))) ?? {}
  const when = (v) => {
    if (v === undefined || v === null || v === "") return ""
    const d = new Date(typeof v === "number" && v < 1e11 ? v * 1000 : v)
    return isNaN(d) ? "" : d.toISOString()
  }
  const text = (v) => (typeof v === "string" ? v : "")
  const num = (v) => (typeof v === "number" && isFinite(v) ? v : 0)
  const ids = (v) => (Array.isArray(v) ? v.filter((x) => typeof x === "string") : [])
  return {
    plan: text(u.plan),
    until: when(u.until),
    renew: u.renew === "auto" || u.renew === "off" ? u.renew : "",
    balance: text(u.balance),
    error: text(u.error),
    user: text(u.user),
    signIn: ["expired", "kept", "renewed"].includes(u.signIn) ? u.signIn : "",
    resets:
      u.resets && typeof u.resets === "object"
        ? { count: num(u.resets.count), until: when(u.resets.until), byWindow: !!u.resets.byWindow, fiveHour: num(u.resets.fiveHour), weekly: num(u.resets.weekly) }
        : null,
    windows: (Array.isArray(u.windows) ? u.windows : []).map((w) => ({
      name: text(w?.name),
      used: Math.max(0, num(w?.used)), // past 100 when overspent
      resetsAt: when(w?.resetsAt),
      resetSecs: Math.max(0, Math.round(num(w?.resetSecs))),
      display: text(w?.display),
      span: Math.max(0, num(w?.span)),
      model: text(w?.model),
      models: ids(w?.models),
      notModels: ids(w?.notModels),
      aside: !!w?.aside,
    })),
  }
}

// sdkHeaders are what the AI SDK package the model is on sends of the key.
function sdkHeaders(npm, key) {
  if (!key) return {}
  if (npm === "@ai-sdk/anthropic" || npm === "@ai-sdk/google-vertex/anthropic") return { "x-api-key": key }
  if (npm === "@ai-sdk/google") return { "x-goog-api-key": key }
  return { authorization: `Bearer ${key}` }
}

// bodyOf is the body as OpenCode hands it to a plugin's fetch: the string
// the AI SDK built, as the plugins reshape only a string body. One that
// isn't UTF-8 text stays bytes.
function bodyOf(b64) {
  if (!b64) return undefined
  const b = Buffer.from(b64, "base64")
  try {
    return new TextDecoder("utf-8", { fatal: true, ignoreBOM: true }).decode(b)
  } catch {
    return b
  }
}

async function doFetch(id, params) {
  const key = accountKey(params.provider, params.account)
  return via.run(params.proxy ?? "", () => inScope(params.provider, key, () => fetchAs(id, key, params)))
}

async function fetchAs(id, key, { provider, model, npm, url, method, headers, body, session }) {
  const ctl = new AbortController()
  inflight.set(id, ctl)
  try {
    const o = await options(provider, key)
    const h = new Headers()
    for (const [k, v] of Object.entries(sdkHeaders(npm, o.apiKey))) h.set(k, v)
    for (const [k, v] of Object.entries(o.headers ?? {})) h.set(k, String(v))
    for (const [k, v] of Object.entries(headers ?? {})) h.set(k, v)
    // chat.headers: what the plugins add to the request, theirs winning
    const p = await info(provider, key)
    const m = p.models[model] ?? { id: model, providerID: provider, api: { id: model, npm } }
    for (const x of hooks) {
      const fn = x.hooks["chat.headers"]
      if (typeof fn !== "function") continue
      const out = { headers: {} }
      try {
        await fn(
          {
            sessionID: session ?? "",
            agent: "build",
            model: m,
            provider: { source: "custom", info: p, options: o },
            message: { id: "", sessionID: session ?? "", role: "user", time: { created: Date.now() }, agent: "build", model: { providerID: provider, modelID: model } },
          },
          out,
        )
      } catch (e) {
        send({ event: "log", level: "error", message: `${x.spec}: chat.headers: ${e?.message ?? e}` })
      }
      for (const [k, v] of Object.entries(out.headers)) h.set(k, String(v))
    }
    const f = typeof o.fetch === "function" ? o.fetch : fetch
    const res = await f(url, {
      method: method ?? "POST",
      headers: h,
      body: bodyOf(body),
      signal: ctl.signal,
    })
    const rh = {}
    res.headers.forEach((v, k) => (rh[k] = v))
    send({ id, event: "head", status: res.status, headers: rh })
    if (res.body) {
      for await (const chunk of res.body) {
        send({ id, event: "chunk", data: Buffer.from(chunk).toString("base64") })
      }
    }
    send({ id, result: null })
  } catch (e) {
    send({ id, error: { message: String(e?.message ?? e) } })
  } finally {
    inflight.delete(id)
  }
}

// ---- the loop ----------------------------------------------------------------

const handlers = {
  async init(p) {
    authPath = p.authPath
    modelsDevPath = p.modelsDevPath ?? ""
    directory = p.directory ?? directory
    userConfig = p.config ?? {}
    await loadPlugins(p.plugins ?? [])
    return { plugins: loaded }
  },
  providers: () => providers(),
  prompt: (p) => ({ prompt: nextPrompt(p.provider, p.method, p.inputs ?? {}) }),
  validate: (p) => ({ error: validate(p.provider, p.method, p.key, p.value) }),
  authorize,
  callback,
  apiKey,
  load,
  usage,
  // check tries one account as a request would: its loader, then its
  // models as the plugin lists them for it, then its usage, which asks the
  // vendor of the account itself. refused is the models hook saying the
  // vendor turned the sign-in away. A models hook may fall back to a list
  // it keeps when the vendor can't be reached, so a check that reached
  // none of the places it asked proves nothing, and fails.
  async check(p) {
    const key = accountKey(p.provider, p.account)
    const r = { reached: false, failed: null, refused: null }
    return via.run(p.proxy ?? "", () => reach.run(r, async () => {
      await load({ provider: p.provider, account: key })
      const pi = await info(p.provider, key, true)
      const u = typeof auths().get(p.provider)?.auth?.usage === "function" ? await usageOf(p.provider, key) : null
      if (r.failed && !r.reached) throw new Error(`couldn't reach ${pi.name}: ${r.failed?.message ?? r.failed}`)
      return { models: Object.keys(pi.models), usage: u, refused: r.refused ?? "" }
    }))
  },
  // import keeps a sign-in made elsewhere (a built-in subscription's, moved
  // onto its plugin) as one more account, or as the account it already is
  // import keeps a sign-in made elsewhere as one of provider's accounts;
  // with a key, as that account again (one taken back)
  import(p) {
    if (p.key && providerOf(p.key) === p.provider) {
      setAuth(p.key, p.auth)
      return { account: p.key }
    }
    const key = freshKey(p.provider)
    setAuth(key, p.auth)
    return { account: settle(p.provider, key) }
  },
  // take gives the accounts named (else every account of the provider) and
  // forgets them in one step: nothing renews a token between the two
  take(p) {
    const all = readAuth()
    const out = {}
    for (const k of p.accounts?.length ? p.accounts : accountsOf(all, p.provider)) {
      if (!(k in all)) continue
      out[k] = all[k]
      removeAuth(k)
    }
    return { auths: out }
  },
  // signOut forgets the account named, else every account of the provider
  signOut(p) {
    const keys = p.account ? [p.account] : accountsOf(readAuth(), p.provider)
    for (const k of keys) removeAuth(k)
    return null
  },
  reload(p) {
    for (const k of p.account ? [p.account] : accountsOf(readAuth(), p.provider)) loaders.delete(k)
    return null
  },
}

// Everything waits for init; a request is answered even when magpie has
// closed stdin behind it, the host leaving once nothing is pending.
let ready
let pending = 0
let closed = false
const done = () => {
  if (--pending === 0 && closed) process.exit(0)
}

const rl = readline.createInterface({ input: process.stdin, terminal: false })
rl.on("line", (line) => {
  if (!line.trim()) return
  let msg
  try {
    msg = JSON.parse(line)
  } catch {
    return
  }
  if (msg.method === "abort") {
    inflight.get(msg.params?.id)?.abort()
    return
  }
  pending++
  if (msg.method === "init") {
    ready = handlers.init(msg.params ?? {})
    ready.then(
      (result) => send({ id: msg.id, result }),
      (e) => send({ id: msg.id, error: { message: String(e?.message ?? e) } }),
    ).finally(done)
    return
  }
  const wait = ready ?? Promise.reject(new Error("the host has not been initialised"))
  if (msg.method === "fetch") {
    wait.then(() => doFetch(msg.id, msg.params ?? {}), (e) => send({ id: msg.id, error: { message: String(e?.message ?? e) } })).finally(done)
    return
  }
  const fn = handlers[msg.method]
  if (!fn) {
    send({ id: msg.id, error: { message: `no method ${msg.method}` } })
    done()
    return
  }
  wait
    .then(() => fn(msg.params ?? {}))
    .then(
      (result) => send({ id: msg.id, result: result ?? null }),
      (e) => send({ id: msg.id, error: { message: String(e?.message ?? e) } }),
    )
    .finally(done)
})
rl.on("close", () => {
  closed = true
  if (pending === 0) process.exit(0)
})
