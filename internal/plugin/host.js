// magpie's plugin host: runs OpenCode (v1) server plugins under Bun and
// answers magpie over stdin/stdout, one JSON message a line.
//
// magpie → host: {id, method, params}; the host answers {id, result} or
// {id, error}. A fetch streams its reply first: {id, event: "head", status,
// headers}, then {id, event: "chunk", data} (base64) as the body comes, then
// {id, result: null}. {method: "abort", params: {id}} cancels one, and
// {method: "credit", params: {id, n}} gives a fetch back the n charged bytes
// its reader took (the payload it read, and a frame's overhead once the frame
// is done), so it may send that much more. A fetch starts with the window
// magpie gave it and sends no more than that ahead of its reader, so a slow
// reader pauses its upstream instead of the host buffering without bound.
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
import http2 from "node:http2"
import net from "node:net"
import path from "node:path"
import readline from "node:readline"
import { Duplex } from "node:stream"
import tls from "node:tls"
import { pathToFileURL } from "node:url"

const rpcWrite = process.stdout.write.bind(process.stdout)

// Everything the child writes goes through one ordered queue: a line goes out
// only when the stream can take it, so a parent that stops reading bounds what
// Bun buffers instead of it growing without bound. A caller may cancel its own
// wait, which drops its line if it has not been written yet, so an aborted
// request leaves no listener or closure behind. A write failure or a closed
// stream fails every line still waiting rather than leaving them pending.
const outq = [] // {line, settle}
let pumping = false
let outFail = null

function waitDrain() {
  return new Promise((resolve, reject) => {
    const done = () => { clear(); resolve() }
    const bad = (e) => { clear(); reject(e ?? new Error("the host's stdout closed")) }
    const clear = () => {
      process.stdout.removeListener("drain", done)
      process.stdout.removeListener("error", bad)
      process.stdout.removeListener("close", bad)
    }
    process.stdout.once("drain", done)
    process.stdout.once("error", bad)
    process.stdout.once("close", bad)
  })
}

function pump() {
  if (pumping) return
  pumping = true
  while (outq.length) {
    const item = outq[0]
    if (outFail) {
      outq.shift()
      item.settle(outFail)
      continue
    }
    // write returns false when the stream is full, but the line was still
    // taken: wait for it to drain before the next one, never write it twice
    const ok = rpcWrite(item.line)
    outq.shift()
    item.settle(null)
    if (!ok) {
      waitDrain().then(
        () => { pumping = false; pump() },
        (e) => { outFail = e; pumping = false; pump() },
      )
      return
    }
  }
  pumping = false
}

// queueLine queues one line, giving a promise that settles with null once it is
// written or with the error that stopped it, and a way to give the wait up.
function queueLine(line) {
  const item = { line, settle: () => {} }
  const promise = new Promise((resolve) => { item.settle = (err) => resolve(err ?? null) })
  outq.push(item)
  pump()
  return {
    promise,
    cancel: () => {
      const i = outq.indexOf(item)
      if (i >= 0) {
        outq.splice(i, 1)
        item.settle(null)
      }
    },
  }
}

// send queues one message, ignoring whether it was written.
const send = (msg) => queueLine(JSON.stringify(msg) + "\n").promise

// sendCancelable is send, with a way to give the wait up.
const sendCancelable = (msg) => queueLine(JSON.stringify(msg) + "\n")

// A host cancellation suppresses the answer; a local abort still owes Go
// its error. The record, rather than the signal, owns pending wire output.
function transitionFetch(record, state) {
  if (record.state === "finished") return
  if (record.state !== "cancelled") record.state = state
  if (state === "streaming") return
  record.gate.stop()
  const body = record.unconsumedBody
  record.unconsumedBody = undefined
  try { Promise.resolve(body?.cancel?.()).catch(() => {}) } catch {}
  if (state === "finishing") return
  record.pendingSend?.cancel()
  record.readyCleanup?.()
  record.readyCleanup = null
  if (state === "cancelled") record.controller.abort()
  else record.state = "finished"
}

function checkFetch(record) {
  if (record.state === "cancelled" || record.state === "finished") throw new DOMException("Aborted", "AbortError")
  record.controller.signal.throwIfAborted()
}

async function sendForFetch(record, msg) {
  if (record.state === "cancelled" || record.state === "finished") return
  const ctl = record.controller
  const pendingSend = sendCancelable(msg)
  record.pendingSend = pendingSend
  const onAbort = () => pendingSend.cancel()
  ctl.signal.addEventListener("abort", onAbort, { once: true })
  try {
    const err = await pendingSend.promise
    if (err) throw err
  } finally {
    record.pendingSend = null
    ctl.signal.removeEventListener("abort", onAbort)
  }
}

// The largest decoded bytes one frame carries: the reader credits in larger
// steps than this, so frames stay small enough that a credit round trip is
// cheap without one frame dwarfing the window.
const MAX_FRAME = 64 << 10

// FRAME_OVERHEAD is what one queued frame costs the window besides its bytes,
// as Go counts it too: the message struct and the decoded slice behind it. It
// keeps a producer of tiny frames from filling a window with structs.
const FRAME_OVERHEAD = 2048

// HEAD_MAX and HEAD_ENTRIES are magpie's own head-envelope policy, not HTTP's:
// a fetch's head carries at most HEAD_MAX header bytes (the UTF-8 bytes of
// every name and value, plus HEAD_ENTRY_COST an entry) and HEAD_ENTRIES
// entries, counted the same by Go before it is queued, so a vendor's unbounded
// header set is not a free 0-charge line.
const HEAD_MAX = 64 << 10
const HEAD_ENTRIES = 256
const HEAD_ENTRY_COST = 4

// headCost is a head's header cost, as Go counts it too: the UTF-8 bytes of
// every name and value, plus HEAD_ENTRY_COST an entry.
function headCost(h) {
  let n = 0
  for (const [k, v] of Object.entries(h)) n += Buffer.byteLength(k) + Buffer.byteLength(v) + HEAD_ENTRY_COST
  const entries = Object.keys(h).length
  return { n, entries }
}

// makeGate is one fetch's credit: the child may have `window` decoded bytes
// outstanding, each frame costing its bytes plus FRAME_OVERHEAD. take returns
// how many bytes of a frame may go now (-1 when the fetch was stopped), spend
// takes the frame's cost, add is the reader giving credit back.
function makeGate(window) {
  let free = window
  let stopped = false
  let wake = null
  const fit = (want) => Math.min(want, free - FRAME_OVERHEAD)
  return {
    take(want) {
      if (stopped) return Promise.resolve(-1)
      const n = fit(want)
      if (n > 0) return Promise.resolve(n)
      return new Promise((resolve) => { wake = (ok) => resolve(ok ? fit(want) : -1) })
    },
    spend(cost) { free -= cost },
    add(n) {
        free += n
      if (wake && free > FRAME_OVERHEAD) { const w = wake; wake = null; w(true) }
    },
    stop() {
      stopped = true
      if (wake) { const w = wake; wake = null; w(false) }
    },
  }
}

// A plugin writing to stdout would break the protocol: everything it
// prints goes to stderr, which magpie logs.
const toErr = (...xs) => process.stderr.write(xs.map((x) => (typeof x === "string" ? x : Bun.inspect(x))).join(" ") + "\n")
console.log = console.info = console.debug = console.warn = toErr
process.stdout.write = (chunk, enc, cb) => process.stderr.write(chunk, enc, cb)

// Nor may a program a plugin starts have them: one given the host's stdin
// reads magpie's messages away (pi-devin-plus runs `devin auth login` with
// stdio "inherit", which took the next requests as its answer and gave up,
// "Login canceled"), and its stdout is the host's answers. It reads
// nothing instead and writes to stderr. node:child_process starts every
// program through Bun.spawn and Bun.spawnSync, so these see all of them.
//
// And it has the proxy the plugin's own fetches take, as a built-in's CLI
// had magpie's (netproxy.Env): the host has its *_PROXY only as
// MAGPIE_*_PROXY (see below), so `grok login`, which the Grok plugin runs,
// went out with none and, where x.ai is reached only through one, printed
// no link to open (𝕏 on Discord). One the plugin set itself is kept.
const own = (v, fd) => v === "inherit" || v === fd || v === (fd ? process.stdout : process.stdin)
const PROXY_VARS = ["HTTPS_PROXY", "HTTP_PROXY", "ALL_PROXY"]
function proxied(env) {
  env = { ...(env ?? process.env) }
  if (Object.keys(env).some((k) => PROXY_VARS.includes(k.toUpperCase()) && env[k])) return env
  const mine = via.getStore()
  if (mine === "direct") return env
  const set = mine ? { HTTPS_PROXY: mine, HTTP_PROXY: mine } : { HTTPS_PROXY: globalProxy.https, HTTP_PROXY: globalProxy.http, NO_PROXY: proxyVar("NO_PROXY") }
  for (const [k, v] of Object.entries(set)) {
    if (!v || Object.keys(env).some((e) => e.toUpperCase() === k)) continue
    env[k] = v
    // Windows' names are one whatever their case
    if (process.platform !== "win32") env[k.toLowerCase()] = v
  }
  return env
}
const guard = (o) => {
  if (o != null && typeof o !== "object") return o
  o = { ...o }
  o.env = proxied(o.env)
  if (Array.isArray(o.stdio)) {
    o.stdio = [...o.stdio]
    if (own(o.stdio[0], 0)) o.stdio[0] = "ignore"
    if (own(o.stdio[1], 1)) o.stdio[1] = 2
  }
  if (own(o.stdin, 0)) o.stdin = "ignore"
  if (own(o.stdout, 1)) o.stdout = 2
  return o
}
for (const name of ["spawn", "spawnSync"]) {
  const run = Bun[name]
  Bun[name] = function (a, b) {
    return Array.isArray(a) ? run.call(this, a, guard(b)) : run.call(this, guard(a))
  }
}

let authPath = ""
let modelsDevPath = ""
let piPath = "" // pi.js, which loads pi's extensions
let directory = process.cwd()
let userConfig = {}
const hooks = [] // {spec, hooks}
const loaded = [] // {spec, id, error}
const loaders = new Map() // account → options the auth loader returned
const sessions = new Map() // oauth sign-in in progress → its authorize result
const inflight = new Map() // fetch id → its lifecycle and owned resources
const renewing = new Map() // account → its sign-in's renewal under way
const unrenewed = new Map() // account → its last renewal that failed: {at, secret, gone}
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
  if (!p) return bunFetch(input, init)
  return bunFetch(input, { ...init, proxy: p }).catch(async (e) => {
    if (e?.name === "AbortError" || init?.signal?.aborted) throw e
    const why = await proxyRefuses(p, input)
    if (why) throw Object.assign(new Error(`proxyconnect tcp: ${why}`), { code: e?.code, cause: e })
    throw e
  })
}

// proxyRefuses is why the proxy p won't carry a request to input, "" when
// it will: Bun says a proxy that is down, or that turns the tunnel away,
// only as a connection that failed, which a vendor's own failure is too.
// The tunnel is asked for again, as Go's transport asks for it, so a
// failure is said in its words ("proxyconnect …") and the gateway takes it
// for the proxy's, asking the next account rather than resting this one.
function proxyRefuses(p, input) {
  let pu, target
  try {
    pu = new URL(p)
    const u = new URL(typeof input === "string" || input instanceof URL ? input : input.url)
    target = `${u.hostname.includes(":") ? `[${u.hostname.replace(/^\[|\]$/g, "")}]` : u.hostname}:${u.port || (u.protocol === "http:" ? 80 : 443)}`
  } catch {
    return Promise.resolve("")
  }
  const host = pu.hostname.replace(/^\[|\]$/g, "")
  const port = Number(pu.port || (pu.protocol === "https:" ? 443 : 80))
  return new Promise((resolve) => {
    let got = ""
    let refused
    const done = (why) => {
      sock.destroy()
      resolve(why)
    }
    const opts = { host, port, servername: net.isIP(host) ? undefined : host }
    const sock = pu.protocol === "https:" ? tls.connect(opts) : net.connect(opts)
    sock.setTimeout(10_000, () => done(`dial tcp ${host}:${port}: i/o timeout`))
    sock.on("error", (e) => done(`dial tcp ${host}:${port}: ${e?.message ?? e}`))
    sock.once(pu.protocol === "https:" ? "secureConnect" : "connect", () => {
      let req = `CONNECT ${target} HTTP/1.1\r\nHost: ${target}\r\n`
      if (pu.username) {
        const cred = `${decodeURIComponent(pu.username)}:${decodeURIComponent(pu.password)}`
        req += `Proxy-Authorization: Basic ${Buffer.from(cred).toString("base64")}\r\n`
      }
      sock.write(req + "\r\n")
    })
    sock.on("data", (d) => {
      got += d.toString("latin1")
      const end = got.indexOf("\r\n\r\n")
      if (end < 0 && got.length < 8192) return
      const status = got.slice(0, got.indexOf("\r\n")).replace(/^HTTP\/\d(\.\d)?\s+/, "")
      if (/^2\d\d/.test(status)) return done("")
      // a bridge to a SOCKS proxy says why after its status
      refused ??= () => {
        const body = end < 0 ? "" : got.slice(end + 4, end + 304).trim()
        return body ? `${status}: ${body}` : status
      }
      setTimeout(() => done(refused()), 50)
    })
    sock.on("end", () => done(refused ? refused() : got ? "" : `${host}:${port} closed the connection`))
  })
}

// listing is what a models hook's fetches came to: whether it asked any,
// and whether the last it asked answered
const listing = new AsyncLocalStorage()

function asked(input, init) {
  const l = listing.getStore()
  if (!l) return sent(input, init)
  l.tried = true
  return sent(input, init).then(
    (res) => ((l.lastOk = res.ok), (l.said = res.ok ? "" : `HTTP ${res.status}`), res),
    (e) => {
      l.lastOk = false
      l.said = e?.message ?? String(e)
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

// An HTTP/2 session a plugin opens itself goes through the proxy its
// fetches take, as a built-in's requests on Go's transport did. Cursor's
// plugin runs agent.v1.AgentService/Run on node:http2, which Bun connects
// directly, *_PROXY or not: where Cursor is reached only through magpie's
// proxy (the system's, or one set in magpie), its sign-in, models and
// usage came through and every chat went out without it (#1035).
const h2connect = http2.connect
http2.connect = function connect(authority, options, listener) {
  if (typeof options === "function") [listener, options] = [options, undefined]
  let u
  try {
    u = typeof authority === "string" ? new URL(authority) : authority instanceof URL ? authority : new URL(`${authority.protocol ?? "https:"}//${authority.host ?? authority.hostname}`)
  } catch {
    return h2connect.call(this, authority, options, listener)
  }
  const p = options?.createConnection ? "" : proxyFor(u.href)
  if (!p) return h2connect.call(this, authority, options, listener)
  const host = u.hostname.replace(/^\[|\]$/g, "")
  const port = Number(u.port || (u.protocol === "http:" ? 80 : 443))
  const raw = tunnel(p, host, port)
  const createConnection = () =>
    u.protocol === "http:" ? raw : tls.connect({ ...options, socket: raw, servername: options?.servername || (net.isIP(host) ? undefined : host), ALPNProtocols: ["h2"] })
  const session = h2connect.call(this, authority, { ...options, createConnection }, listener)
  // the tunnel's failure is the session's: Bun's TLS over a stream leaves
  // the stream's errors unheard (one unheard ends the host), and says a
  // tunnel that never came as "h2 is not supported"
  raw.on("error", (e) => session.destroy(e))
  return session
}

// tunnel is a connection to host:port through the HTTP proxy p (a SOCKS
// one is handed to the host as an HTTP bridge to it, netproxy.ForBun): its
// CONNECT is asked first, and what is written waits for the tunnel. A proxy
// that turns it away fails it in Go's words ("proxyconnect …").
function tunnel(p, host, port) {
  const pu = new URL(p)
  const ph = pu.hostname.replace(/^\[|\]$/g, "")
  const pp = Number(pu.port || (pu.protocol === "https:" ? 443 : 80))
  const target = `${host.includes(":") ? `[${host}]` : host}:${port}`
  let ready = false
  const queued = []
  const sock = pu.protocol === "https:" ? tls.connect({ host: ph, port: pp, servername: net.isIP(ph) ? undefined : ph }) : net.connect({ host: ph, port: pp })
  const conn = new Duplex({
    read() {},
    write(chunk, enc, cb) {
      if (ready) sock.write(chunk, cb)
      else queued.push([chunk, cb])
    },
    final(cb) {
      sock.end()
      cb()
    },
    destroy(err, cb) {
      sock.destroy()
      cb(err)
    },
  })
  sock.once(pu.protocol === "https:" ? "secureConnect" : "connect", () => {
    let req = `CONNECT ${target} HTTP/1.1\r\nHost: ${target}\r\n`
    if (pu.username) {
      const cred = `${decodeURIComponent(pu.username)}:${decodeURIComponent(pu.password)}`
      req += `Proxy-Authorization: Basic ${Buffer.from(cred).toString("base64")}\r\n`
    }
    sock.write(req + "\r\n")
  })
  let got = Buffer.alloc(0)
  const head = (chunk) => {
    got = Buffer.concat([got, chunk])
    const end = got.indexOf("\r\n\r\n")
    if (end < 0) {
      if (got.length > 8192) conn.destroy(new Error(`proxyconnect tcp: ${ph}:${pp} answered CONNECT with no HTTP`))
      return
    }
    sock.off("data", head)
    const status = got.subarray(0, got.indexOf("\r\n")).toString("latin1").replace(/^HTTP\/\d(\.\d)?\s+/, "")
    if (!/^2\d\d/.test(status)) {
      const body = got.subarray(end + 4, end + 304).toString("utf8").trim()
      return conn.destroy(new Error(`proxyconnect tcp: ${body ? `${status}: ${body}` : status}`))
    }
    ready = true
    if (got.length > end + 4) conn.push(got.subarray(end + 4))
    sock.on("data", (d) => conn.push(d))
    for (const [c, cb] of queued.splice(0)) sock.write(c, cb)
  }
  sock.on("data", head)
  sock.on("end", () => conn.push(null))
  sock.on("error", (e) => conn.destroy(ready ? e : new Error(`proxyconnect tcp: dial tcp ${ph}:${pp}: ${e?.message || e?.code || e}`)))
  return conn
}

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

// Two hosts run at once while one is restarted (the old one finishing its
// calls), and magpie writes the file when no host runs: each change to it
// is made under plugin-auth.json.lock, read afresh and written back with
// only its own accounts changed, so a host never writes another's newer
// token away with what it read before. Nothing awaits under the lock; one
// held longer than AUTH_LOCK_STALE was left by a host that died.
const AUTH_LOCK_STALE = 10 * 1000

function lockAuth() {
  const lock = authPath + ".lock"
  fs.mkdirSync(path.dirname(authPath), { recursive: true })
  for (const start = Date.now(); ; ) {
    try {
      fs.closeSync(fs.openSync(lock, "wx", 0o600))
      return () => {
        try {
          fs.unlinkSync(lock)
        } catch {}
      }
    } catch (e) {
      if (e?.code !== "EEXIST" && e?.code !== "EPERM" && e?.code !== "EACCES") throw e
    }
    let old = false
    try {
      old = Date.now() - fs.statSync(lock).mtimeMs > AUTH_LOCK_STALE
    } catch {}
    if (old || Date.now() - start > 2 * AUTH_LOCK_STALE) {
      try {
        fs.unlinkSync(lock)
      } catch {}
    }
    pause(5)
  }
}

// changeAuth runs change on the file as it is now, under the lock, and
// writes it back when change says it changed it.
function changeAuth(change) {
  const unlock = lockAuth()
  try {
    const all = readAuth()
    const out = change(all)
    if (out !== false) writeAuth(all)
    return out
  } finally {
    unlock()
  }
}

function setAuth(key, info) {
  changeAuth((all) => {
    all[key] = info
  })
  loaders.delete(key)
  send({ event: "auth", provider: providerOf(key), account: key })
}

function removeAuth(key) {
  changeAuth((all) => {
    delete all[key]
  })
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

// uidOf is the vendor's id a plugin keeps beside an account's name
// (WorkBuddy's uid): two accounts of one name are told apart by it (#413).
function uidOf(a) {
  return typeof a?.uid === "string" ? a.uid : ""
}

// settle keeps a sign-in just saved at key once: one to an account already
// signed in (the same account id and, where both have one, the same uid,
// else the same secret) replaces that one's and goes. It gives where it is
// kept.
function settle(provider, key) {
  const k = changeAuth((all) => {
    const now = all[key]
    if (!now) return false
    const who = whoOf(now)
    const secret = secretOf(now)
    for (const k of accountsOf(all, provider)) {
      if (k === key) continue
      const was = all[k]
      const other = uidOf(now) && uidOf(was) && uidOf(now) !== uidOf(was)
      if ((who && whoOf(was) === who && !other) || (!who && !whoOf(was) && secret && secretOf(was) === secret)) {
        all[k] = now
        delete all[key]
        return k
      }
    }
    return false
  })
  if (k) {
    loaders.delete(k)
    loaders.delete(key)
    send({ event: "auth", provider, account: k })
    return k
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
          const kept = changeAuth((all) => {
            if (JSON.stringify(all[key]) === JSON.stringify(body)) return false
            all[key] = body
          })
          if (kept !== false) {
            loaders.delete(key)
            send({ event: "auth", provider: providerOf(key), account: key })
          }
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
  // pi's packages and extensions are loaded as pi loads them (pi.js)
  let pi
  if (piPath) {
    try {
      pi = await import(pathToFileURL(piPath).href)
    } catch (e) {
      toErr("pi.js:", e)
    }
  }
  const pis = pi ? list.filter((p) => pi.isPi(p.target)) : []
  if (pis.length) {
    const h = { readAuth, changeAuth, keyFor, send, directory }
    const got = await pi.load(h, pis).catch((e) => pis.map((p) => ({ spec: p.spec, error: String(e?.stack ?? e) })))
    for (const r of got) {
      for (const x of r.hooks ?? []) hooks.push(x)
      loaded.push(r.error ? { spec: r.spec, error: r.error } : { spec: r.spec })
    }
  }
  for (const p of list) {
    if (pis.includes(p)) continue
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
    const k = key ?? accountsOf(readAuth(), id)[0] ?? id
    await fresh(id, k)
    const all = readAuth()
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
      // why, as far as the host saw it: the editor says so rather than
      // show the plugin's short defaults as if they were the account's
      if (out.fellBack) out.listError = l.said || "the plugin couldn't get its vendor's list"
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
      out.listError = e?.message ?? String(e)
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

// concurrencyOf is how many requests the plugin says each of its accounts
// takes at once, magpie's own field: the auth hook's maxConcurrency, else
// package.json's magpie.maxConcurrency. A whole number over 0, else none
// (0); the user's setting on the provider goes over it.
function concurrencyOf(a) {
  const ok = (v) => (Number.isInteger(v) && v > 0 ? Math.min(v, 1000) : 0)
  const own = ok(a.auth.maxConcurrency)
  if (own || !a.target) return own
  const dir = fs.statSync(a.target, { throwIfNoEntry: false })?.isDirectory() ? a.target : path.dirname(a.target)
  return ok(readJSON(path.join(dir, "package.json"))?.magpie?.maxConcurrency)
}

// rateOf reads a model's credit rate as a plugin gives it: a number (0.5)
// or as the vendor's picker writes it ("x0.03", "0.5×"); 0 is none, as is
// one it can't read
function rateOf(v) {
  const n = typeof v === "string" ? Number(v.trim().replace(/^[x×]\s*|\s*[x×]$/gi, "")) : v
  return typeof n === "number" && Number.isFinite(n) && n > 0 ? n : 0
}

// providers lists each provider and its accounts' models, each asked
// through its proxy (proxies[provider][key], "" the provider's own), as a
// built-in fetches each account's list through the account's.
async function providers({ proxies } = {}) {
  const stored = readAuth()
  const out = []
  for (const [id, a] of auths()) {
    const keys = accountsOf(stored, id)
    const through = (k) => proxies?.[id]?.[k ?? keys[0] ?? ""] ?? proxies?.[id]?.[""] ?? ""
    const p = await via.run(through(), () => info(id))
    // each account's own models, as the built-ins read each account's: a
    // plan may serve fewer, or others, than the first account's. One that
    // can't be read is taken to have them all, or the ones it was last
    // told to have, as a built-in account whose fetch failed keeps its own.
    const own = await Promise.all(keys.slice(1).map((k) => via.run(through(k), () => info(id, k, true)).catch((e) => ({ failed: true, listError: e?.message ?? String(e) }))))
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
      checkin: typeof a.auth.checkin === "function",
      maxConcurrency: concurrencyOf(a),
      signedIn: keys.length > 0,
      authType: first?.type ?? "",
      accountId: whoOf(first),
      fellBack: keys.length > 0 && !!p.fellBack,
      listError: keys.length > 0 && p.fellBack ? p.listError ?? "" : "",
      accounts: keys.map((k, i) => ({
        key: k,
        type: stored[k]?.type ?? "",
        accountId: whoOf(stored[k]),
        hint: hintOf(stored[k]),
        models: own.length === 0 ? undefined : i === 0 ? ids(p) : own[i - 1]?.models ? ids(own[i - 1]) : undefined,
        fellBack: i === 0 ? !!p.fellBack : !!(own[i - 1]?.fellBack || own[i - 1]?.failed),
        listError: (i === 0 ? p.fellBack && p.listError : own[i - 1]?.listError) || "",
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
          // what a request costs of the plan's credits, as a multiple,
          // and before a discount running now (Qoder's price_factor)
          rate: rateOf(m.rate),
          rateWas: rateOf(m.rateWas),
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
// OpenCode's CLI asks them: in order, those whose when/condition hold. A
// method may ask its own way (magpie's hook, which pi's sign-ins use, as
// they ask as they go): ask(inputs) → the next question, or null.
async function nextPrompt(provider, index, inputs) {
  const m = authOf(provider).methods[index]
  if (!m) throw new Error(`no sign-in method ${index} for ${provider}`)
  if (typeof m.ask === "function") return (await m.ask(inputs)) ?? null
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

// ---- renewing a sign-in ------------------------------------------------------

// A plugin may leave renewing its accounts' tokens to magpie (magpie's own
// hook, which OpenCode ignores):
//   auth.refresh(auth, provider) → the account's sign-in renewed: the
//     fields to keep over the old ({ access, refresh, expires, … }), or
//     nothing when there is nothing to renew. It throws when it can't; an
//     error with signIn "expired" says the vendor turned the sign-in away
//     for good, and the account is marked.
//   auth.refreshLead: how long (ms) before expires a token is renewed,
//     5 minutes when not said.
// An OAuth sign-in whose expires is that close is renewed before its
// loader, models, usage or a request runs, once at a time per account: the
// requests that find it due wait for the one renewal (a vendor that spends
// a refresh token once turns a second away). A renewal that fails leaves
// the sign-in as it was, for the vendor's answer to tell, and isn't tried
// again for RENEW_RETRY, or, turned away for good, till it is signed in
// again.

const RENEW_LEAD = 5 * 60 * 1000
const RENEW_WAIT = 15 * 1000
const RENEW_RETRY = 30 * 1000

function leadOf(a) {
  const v = a?.refreshLead
  return Number.isFinite(v) && v >= 0 ? Math.min(v, 24 * 3600e3) : RENEW_LEAD
}

// renewAt is when the sign-in at key is due to be renewed, 0 for never: an
// OAuth one with an expiry, of a plugin that renews through magpie.
function renewAt(a, stored) {
  if (typeof a?.refresh !== "function" || stored?.type !== "oauth") return 0
  const exp = Number(stored.expires)
  return Number.isFinite(exp) && exp > 0 ? Math.max(1, exp - leadOf(a)) : 0
}

// fresh renews the sign-in at key when it is due, waiting at most
// RENEW_WAIT for it: a renewal that takes longer goes on, and is kept when
// it ends, but what waits for it goes on with the sign-in as it is.
async function fresh(provider, key) {
  const a = auths().get(provider)?.auth
  const stored = readAuth()[key]
  const at = renewAt(a, stored)
  if (!at || Date.now() < at) return
  const f = unrenewed.get(key)
  if (f && f.secret === secretOf(stored) && (f.gone || Date.now() - f.at < RENEW_RETRY)) return
  let r = renewing.get(key)
  if (!r) {
    pending++ // the host doesn't leave in the middle of it
    r = renew(provider, key, a).finally(() => {
      renewing.delete(key)
      // nor does magpie stop it in the middle of one (host.go's stop)
      send({ event: "renewing", count: renewing.size })
      done()
    })
    renewing.set(key, r)
    send({ event: "renewing", count: renewing.size })
  }
  let t
  await Promise.race([r, new Promise((ok) => (t = setTimeout(ok, RENEW_WAIT)))])
  clearTimeout(t)
}

async function renew(provider, key, a) {
  const was = readAuth()[key]
  const at = renewAt(a, was)
  if (!at || Date.now() < at) return // renewed meanwhile
  let got
  try {
    got = await inScope(provider, key, () => a.refresh(JSON.parse(JSON.stringify(was)), provider))
  } catch (e) {
    unrenewed.set(key, { at: Date.now(), secret: secretOf(was), gone: e?.signIn === "expired" })
    if (e?.signIn === "expired") send({ event: "signIn", provider, account: key, said: "expired" })
    send({ event: "log", level: "error", message: `${auths().get(provider)?.spec ?? provider}: auth.refresh: ${e?.message ?? e}` })
    return
  }
  if (!got || typeof got !== "object") return
  // signed out, signed in again or renewed by another host while it ran:
  // that one stands. Checked under the lock the save is made under, so
  // nothing comes between the two.
  const { type: _t, ...fields } = got
  const kept = changeAuth((all) => {
    const now = all[key]
    if (!now || secretOf(now) !== secretOf(was) || now.access !== was.access) return false
    all[key] = { ...was, ...fields, type: "oauth" }
  })
  if (kept === false) return
  unrenewed.delete(key)
  loaders.delete(key)
  send({ event: "auth", provider, account: key })
  send({ event: "signIn", provider, account: key, said: "renewed" })
}

// ---- requests ----------------------------------------------------------------

// options is what the provider's auth loader returned for the account at
// key, run once per sign-in as OpenCode runs it once per start. Run in
// the account's scope, whatever the loader keeps (its fetch) saves to it.
async function options(provider, key) {
  await fresh(provider, key)
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

async function load({ provider, account, proxy }) {
  const key = accountKey(provider, account)
  const o = await via.run(proxy ?? "", () => inScope(provider, key, () => options(provider, key)))
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
//       resetSecs?, display?, amount? / limit? / unit? (the window's own
//       count, when it counts in amounts: amount of limit used, in unit,
//       "credits"), span? (seconds the window runs), model? (a
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
  await fresh(provider, key)
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
      amount: Math.max(0, num(w?.amount)),
      limit: Math.max(0, num(w?.limit)),
      unit: text(w?.unit),
      span: Math.max(0, num(w?.span)),
      model: text(w?.model),
      models: ids(w?.models),
      notModels: ids(w?.notModels),
      aside: !!w?.aside,
    })),
  }
}

// ---- check-in ----------------------------------------------------------------

// checkin presses the vendor's daily check-in (签到) for the account at key,
// as the plugin's auth.checkin does it (magpie's own hook, which OpenCode
// ignores; Lemon on Discord: 签到 belongs in the plugins):
//   auth.checkin(getAuth, provider) → {
//     outcome: "claimed" (checked in now) | "done" (in already today) |
//       "ineligible" (the account can't take part) | "inactive" (no
//       check-in running) | "captcha" (the vendor asks for a captcha: the
//       user checks in in its own app; never solved) | "failed",
//     credit? (what it gave), streak? (days in a row), message?
//   }
// magpie asks once a Beijing day per account while the user has it on, and
// again later that day after a failure; a hook that throws is a failure.
// Run in the account's scope, a token it renews is saved to that account.
async function checkin({ provider, account, proxy }) {
  return via.run(proxy ?? "", () => checkinOf(provider, account))
}

const checkinOutcomes = ["claimed", "done", "ineligible", "inactive", "captcha", "failed"]

async function checkinOf(provider, account) {
  const a = auths().get(provider)?.auth
  if (typeof a?.checkin !== "function") throw new Error(`${provider}'s plugin doesn't check in`)
  const key = accountKey(provider, account)
  if (!readAuth()[key]) throw new Error("not signed in")
  await fresh(provider, key)
  const p = await info(provider, key)
  const r = (await inScope(provider, key, () => a.checkin(async () => readAuth()[key], JSON.parse(JSON.stringify(p))))) ?? {}
  const num = (v) => (typeof v === "number" && isFinite(v) ? Math.max(0, v) : 0)
  const message = typeof r.message === "string" ? r.message : ""
  if (!checkinOutcomes.includes(r.outcome)) {
    return { outcome: "failed", credit: 0, streak: 0, message: message || `the plugin answered no outcome (${JSON.stringify(r.outcome ?? null)})` }
  }
  return { outcome: r.outcome, credit: num(r.credit), streak: Math.round(num(r.streak)), message }
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

async function doFetch(id, record, params) {
  const key = accountKey(params.provider, params.account)
  return via.run(params.proxy ?? "", () => inScope(params.provider, key, () => fetchAs(id, record, key, params)))
}

async function fetchAs(id, record, key, params) {
  const { controller: ctl, gate } = record
  const { provider, model, npm, url, method, headers, body, session } = params
  try {
    const o = await options(provider, key)
    checkFetch(record)
    const h = new Headers()
    for (const [k, v] of Object.entries(sdkHeaders(npm, o.apiKey))) h.set(k, v)
    for (const [k, v] of Object.entries(o.headers ?? {})) h.set(k, String(v))
    for (const [k, v] of Object.entries(headers ?? {})) h.set(k, v)
    // chat.headers: what the plugins add to the request, theirs winning
    const p = await info(provider, key)
    checkFetch(record)
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
      checkFetch(record)
      for (const [k, v] of Object.entries(out.headers)) h.set(k, String(v))
    }
    const f = typeof o.fetch === "function" ? o.fetch : fetch
    // the fetch is owned since its message was read: an abort that came while
    // this setup was awaiting must not let the upstream start
    checkFetch(record)
    const res = await f(url, {
      method: method ?? "POST",
      headers: h,
      body: bodyOf(body),
      signal: ctl.signal,
    })
    record.unconsumedBody = res.body
    checkFetch(record)
    transitionFetch(record, "streaming")
    const rh = {}
    res.headers.forEach((v, k) => (rh[k] = v))
    const head = headCost(rh)
    if (head.entries > HEAD_ENTRIES || head.n > HEAD_MAX) {
      // the reply's head is past magpie's envelope: give the fetch up rather
      // than write a line the host would refuse, and let the body go
      throw new Error(`the reply's headers are past the host's head envelope (${HEAD_MAX} bytes or ${HEAD_ENTRIES} entries)`)
    }
    await sendForFetch(record, { id, event: "head", status: res.status, headers: rh })
    checkFetch(record)
    if (res.body) {
      record.unconsumedBody = undefined
      // an abort gives up this fetch's own pending write, so its wait ends and
      // the loop leaves rather than parking on a stream that never drains
      for await (const chunk of res.body) {
        checkFetch(record)
        const bytes = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk)
        for (let off = 0; off < bytes.length; ) {
          // one frame at a time, no more than the reader has credited: a
          // reader that stopped leaves the upstream paused here
          const n = await gate.take(Math.min(MAX_FRAME, bytes.length - off))
          checkFetch(record)
          if (n <= 0) break
          const data = bytes.subarray(off, off + n).toString("base64")
          gate.spend(n + FRAME_OVERHEAD)
          await sendForFetch(record, { id, event: "chunk", data })
          checkFetch(record)
          off += n
        }
      }
    }
    checkFetch(record)
    transitionFetch(record, "finishing")
    await sendForFetch(record, { id, result: null })
  } catch (e) {
    transitionFetch(record, "finishing")
    // a fetch hook that threw on its sign-in (a refresh the vendor turned
    // away) marks the account, as a built-in's refused refresh marked it
    if (["expired", "kept", "renewed"].includes(e?.signIn) && key) send({ event: "signIn", provider, account: key, said: e.signIn })
    // a fetch the host already gave up on needs no late error: its slot is
    // gone, and queueing an uncancellable line for it would only be noise
    await sendForFetch(record, { id, error: { message: String(e?.message ?? e) } })
  }
}

// ---- the loop ----------------------------------------------------------------

const handlers = {
  async init(p) {
    authPath = p.authPath
    modelsDevPath = p.modelsDevPath ?? ""
    piPath = p.piPath ?? ""
    directory = p.directory ?? directory
    userConfig = p.config ?? {}
    await loadPlugins(p.plugins ?? [])
    return { plugins: loaded }
  },
  providers: (p) => providers(p ?? {}),
  prompt: async (p) => ({ prompt: await nextPrompt(p.provider, p.method, p.inputs ?? {}) }),
  validate: (p) => ({ error: validate(p.provider, p.method, p.key, p.value) }),
  authorize,
  callback,
  apiKey,
  load,
  usage,
  checkin,
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
    const out = {}
    changeAuth((all) => {
      for (const k of p.accounts?.length ? p.accounts : accountsOf(all, p.provider)) {
        if (!(k in all)) continue
        out[k] = all[k]
        delete all[k]
      }
      if (!Object.keys(out).length) return false
    })
    for (const k of Object.keys(out)) {
      loaders.delete(k)
      send({ event: "auth", provider: providerOf(k), account: k })
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
let readyState
const readyWaiters = new Set()
function setReady(promise) {
  readyState = undefined
  ready = promise
  const settle = (state) => {
    readyState = state
    for (const waiter of readyWaiters) waiter(state)
    readyWaiters.clear()
  }
  ready.then(
    () => settle({}),
    (error) => settle({ error }),
  )
}
function waitForReady(record) {
  const signal = record.controller.signal
  if (!ready) return Promise.reject(new Error("the host has not been initialised"))
  return new Promise((resolve, reject) => {
    const finish = (state) => {
      readyWaiters.delete(finish)
      signal.removeEventListener("abort", abort)
      record.readyCleanup = null
      if ("error" in state) reject(state.error)
      else resolve()
    }
    const abort = () => finish({ error: new DOMException("Aborted", "AbortError") })
    record.readyCleanup = abort
    if (signal.aborted) abort()
    else if (readyState) finish(readyState)
    else {
      readyWaiters.add(finish)
      signal.addEventListener("abort", abort, { once: true })
    }
  })
}
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
    const id = msg.params?.id
    const record = inflight.get(id)
    if (record) transitionFetch(record, "cancelled")
    return
  }
  if (msg.method === "credit") {
    const record = inflight.get(msg.params?.id)
    if (record?.state === "streaming") record.gate.add(msg.params?.n ?? 0)
    return
  }
  pending++
  if (msg.method === "init") {
    setReady(handlers.init(msg.params ?? {}))
    ready.then(
      (result) => send({ id: msg.id, result }),
      (e) => send({ id: msg.id, error: { message: String(e?.message ?? e) } }),
    ).finally(done)
    return
  }
  if (msg.method === "fetch") {
    const id = msg.id
    // the fetch is owned the moment its message is read, before the async
    // setup below: an abort that comes before the upstream is reached still
    // stops it, and one that comes during the await is not missed
    const record = {
      state: "setup", controller: new AbortController(),
      gate: makeGate(msg.params?.window > 0 ? msg.params.window : 512 << 10),
      pendingSend: null, readyCleanup: null, unconsumedBody: undefined,
    }
    inflight.set(id, record)
    // waiting for the host to be ready is the host's own await: an abort must
    // end it too, so a request whose setup never finishes still runs its
    // finally and lets its controller, gate and body go. The shared setup
    // promise itself is not cancelled — only this request's wait on it.
    const start = () => {
      checkFetch(record)
      return doFetch(id, record, msg.params ?? {})
    }
    waitForReady(record).then(start)
      .catch((e) => {
        transitionFetch(record, "finishing")
        return sendForFetch(record, { id, error: { message: String(e?.message ?? e) } })
      })
      .finally(() => {
        transitionFetch(record, "finished")
        inflight.delete(id)
        done()
      })
    return
  }
  const wait = ready ?? Promise.reject(new Error("the host has not been initialised"))
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
