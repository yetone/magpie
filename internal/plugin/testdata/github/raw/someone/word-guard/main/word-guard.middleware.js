// New API's 敏感词过滤 as magpie gateway middleware: words or patterns you
// list are kept out of what agents send, turned away or masked, and can be
// masked in what comes back.
//
//   {"words": ["project-x"], "patterns": ["\\b\\d{3}-\\d{4}\\b"],
//    "action": "reject", "check": "last", "mask": "***",
//    "replies": false, "message": ""}
//
// Only the user's own text is read: the API's user messages' text, not
// tool results, tool calls or the system prompt. "check": "last" reads the
// last user message that has text (New API reads the prompt, that is, the
// turn being sent), "all" every user message. Words match ignoring case.
// "action": "reject" turns the request away with an error naming the word;
// "mask" swaps each match for "mask". "replies": true masks matches in the
// text that comes back as well; a word split across two streamed events
// isn't seen.

export function onRequest(body, ctx) {
  const re = matcher(ctx.options)
  if (!re) return
  const o = ctx.options
  const all = o.check === "all"
  if (o.action === "mask") {
    const mask = typeof o.mask === "string" ? o.mask : "***"
    let changed = false
    userTexts(body, ctx.protocol, all, (t) => {
      re.lastIndex = 0
      const u = t.replace(re, mask)
      if (u !== t) changed = true
      return u
    })
    return changed ? body : undefined
  }
  let hit = null
  userTexts(body, ctx.protocol, all, (t) => {
    if (hit === null) {
      re.lastIndex = 0
      const m = re.exec(t)
      if (m) hit = m[0]
    }
    return t
  })
  if (hit !== null) ctx.reject(400, o.message || `this request has "${hit}", which this gateway doesn't send`)
}

export function onEvent(ev, ctx) {
  const o = ctx.options
  if (!o || !o.replies || o.action !== "mask") return
  const re = matcher(o)
  if (!re) return
  const mask = typeof o.mask === "string" ? o.mask : "***"
  const sub = (t) => {
    re.lastIndex = 0
    return typeof t === "string" ? t.replace(re, mask) : t
  }
  let changed = false
  const set = (obj, k) => {
    if (obj && typeof obj[k] === "string") {
      const u = sub(obj[k])
      if (u !== obj[k]) {
        obj[k] = u
        changed = true
      }
    }
  }
  if (ev.type === "content_block_delta") set(ev.delta, "text")
  else if (ev.type === "response.output_text.delta") set(ev, "delta")
  else if (ev.type === "response.output_text.done") set(ev, "text")
  else if (Array.isArray(ev.choices)) for (const c of ev.choices) set(c.delta, "content")
  // Gemini streams its text in candidates[].content.parts[].text, the shape
  // onResponse below already masks
  else if (Array.isArray(ev.candidates))
    for (const c of ev.candidates) {
      if (c && c.content && Array.isArray(c.content.parts)) for (const p of c.content.parts) set(p, "text")
    }
  return changed ? ev : undefined
}

export function onResponse(body, ctx) {
  const o = ctx.options
  if (!o || !o.replies || o.action !== "mask" || ctx.status >= 400) return
  const re = matcher(o)
  if (!re) return
  const mask = typeof o.mask === "string" ? o.mask : "***"
  let changed = false
  const fix = (obj, k) => {
    if (obj && typeof obj[k] === "string") {
      re.lastIndex = 0
      const u = obj[k].replace(re, mask)
      if (u !== obj[k]) {
        obj[k] = u
        changed = true
      }
    }
  }
  if (Array.isArray(body.content)) for (const b of body.content) if (b && b.type === "text") fix(b, "text")
  if (Array.isArray(body.choices)) for (const c of body.choices) fix(c.message, "content")
  if (Array.isArray(body.output)) for (const it of body.output) if (it && Array.isArray(it.content)) for (const p of it.content) fix(p, "text")
  if (Array.isArray(body.candidates)) for (const c of body.candidates) if (c.content && Array.isArray(c.content.parts)) for (const p of c.content.parts) fix(p, "text")
  return changed ? body : undefined
}

// matcher is one expression for every word and pattern; null for none.
// A runtime is kept for a request's hooks, so it is built once a request.
let cached = null
function matcher(o) {
  if (!o || typeof o !== "object") return null
  const key = JSON.stringify([o.words, o.patterns])
  if (cached && cached.key === key) return cached.re
  const parts = []
  for (const w of Array.isArray(o.words) ? o.words : []) if (typeof w === "string" && w.trim()) parts.push(w.trim().replace(/[.*+?^${}()|[\]\\]/g, "\\$&"))
  for (const p of Array.isArray(o.patterns) ? o.patterns : []) if (typeof p === "string" && p) parts.push("(?:" + p + ")")
  const re = parts.length ? new RegExp(parts.join("|"), "gi") : null
  cached = { key, re }
  return re
}

// userTexts calls fn on each text of the user's own in body, in its API's
// shape, and puts back what fn gives: the last user message with text, or
// all of them.
function userTexts(body, protocol, all, fn) {
  let msgs = []
  if (protocol === "gemini") {
    msgs = (Array.isArray(body.contents) ? body.contents : []).filter((m) => m && (m.role === "user" || !m.role)).map((m) => ({ m, list: m.parts, key: "text", only: (p) => p && typeof p.text === "string" }))
  } else if (protocol === "responses") {
    if (typeof body.input === "string") {
      body.input = fn(body.input)
      return
    }
    msgs = (Array.isArray(body.input) ? body.input : []).filter((m) => m && m.role === "user").map((m) => ({ m, list: m.content, key: "text", only: (p) => p && (p.type === "input_text" || p.type === "text") }))
  } else {
    msgs = (Array.isArray(body.messages) ? body.messages : []).filter((m) => m && m.role === "user").map((m) => ({ m, list: m.content, key: "text", only: (p) => p && p.type === "text" }))
  }
  const hasText = (x) => typeof x.m.content === "string" || (Array.isArray(x.list) && x.list.some(x.only))
  const chosen = all ? msgs.filter(hasText) : msgs.filter(hasText).slice(-1)
  for (const x of chosen) {
    if (typeof x.m.content === "string") x.m.content = fn(x.m.content)
    else for (const p of x.list) if (x.only(p)) p[x.key] = fn(p[x.key])
  }
}
