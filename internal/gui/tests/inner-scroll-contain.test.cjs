// Run with Node's test runner and Playwright on the module path; see README.md.
// 碳碳双键 on Discord: a list that scrolls inside a page (Usage's requests,
// "No session ID" and its workbuddy rows), wheeled to its top or its foot,
// went on to scroll the whole page, so the first and last rows were hard to
// read without the page jumping; "in many places". A box that scrolls inside
// a page now keeps the wheel at its ends, every such box (one rule in
// app.css), while a box that scrolls only sideways still lets the wheel move
// the page, and a short window's model chips still pass it on (悠悠哥, #833).
// Wheeled past each end, the page's scrollTop stays where it was, in
// Chromium and WebKit, in English and Chinese.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const engines = process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"];
const launch = (engine) => engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" });

const now = new Date();
const day = [now.getFullYear(), now.getMonth() + 1, now.getDate()].map((n) => String(n).padStart(2, "0")).join("-");
const at = (i) => new Date(now.getTime() - (i + 1) * 60e3).toISOString();
// the reporter's rows: WorkBuddy's deepseek, no session id, under a
// session that has one
const who = { id: "workbuddy-key", provider: "workbuddy", name: "WorkBuddy", who: "key-1", kind: "key", model: "deepseek-v4.1-flash", routing: "", used: 0 };
const routes = Array.from({ length: 40 }, (_, i) => ({ id: 100 - i, seq: 100 - i, time: at(i), agent: "workbuddy", model: "deepseek-v4.1-flash", provider: "workbuddy", order: [who],
  tries: [{ id: who.id, model: "deepseek-v4.1-flash", start: at(i), done: true, status: 200, ms: 1200 }], done: true, status: 200, ms: 1200, tokens: 3000, ...(i < 4 ? { agent: "codex", session: "chat-1" } : {}) }));
const agents = Array.from({ length: 14 }, (_, i) => ({ id: "a" + i, name: "Agent " + i, path: "/t/a" + i + ".json", icon: "generic", wired: true,
  fields: [{ key: "model", label: "model", value: "m", options: [{ value: "m", label: "M", ref: "a/m" }] }] }));

function serve(lang) {
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents, profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) await new Promise((ok) => setTimeout(ok, 100));
      return json({ mine: true, now: now.toISOString(), seq: 1, totals: { requests: routes.length, rerouted: 0, errors: 0 }, routes });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [{ day, requests: routes.length }], routes: url.searchParams.get("day") ? routes : [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// the wheel, over the middle of box, n turns of dy, and a moment to settle
async function wheel(page, box, dy, n) {
  const b = await box.boundingBox();
  await page.mouse.move(b.x + b.width / 2, b.y + b.height / 2);
  for (let i = 0; i < n; i++) { await page.mouse.wheel(0, dy); await page.waitForTimeout(60); }
  await page.waitForTimeout(400);
}
const tops = async (view, box) => view.evaluate((v, b) => ({ page: v.scrollTop, pageRange: v.scrollHeight - v.clientHeight, box: b.scrollTop, boxRange: b.scrollHeight - b.clientHeight }), await box.elementHandle());

// box brought to the page's middle by the wheel at the page's own left
// edge, as a reader would (the page puts a scroll by code back where the
// reader left it)
async function bringIn(page, view, box) {
  const vb = await view.boundingBox();
  for (let i = 0; i < 40; i++) {
    const b = await box.boundingBox(), off = b.y + b.height / 2 - (vb.y + vb.height / 2);
    if (Math.abs(off) < 40) break;
    await page.mouse.move(vb.x + 3, vb.y + vb.height / 2);
    await page.mouse.wheel(0, Math.sign(off) * Math.min(100, Math.abs(off)));
    await page.waitForTimeout(80);
  }
  await page.waitForTimeout(300);
}
// past each end of box, the page stays put
async function heldAtEnds(page, view, box, what) {
  await bringIn(page, view, box);
  let s = await tops(view, box);
  assert.ok(s.boxRange > 20, `${what} scrolls: ${JSON.stringify(s)}`);
  assert.ok(s.page > 0 && s.page < s.pageRange, `${what}: the page can move both ways: ${JSON.stringify(s)}`);

  // at its foot, wheeled on down
  await box.evaluate((b) => { b.scrollTop = b.scrollHeight; });
  await page.waitForTimeout(100);
  const before = (await tops(view, box)).page;
  await wheel(page, box, 120, 6);
  s = await tops(view, box);
  assert.ok(s.box >= s.boxRange - 1, `${what} still at its foot: ${JSON.stringify(s)}`);
  assert.equal(s.page, before, `${what}, wheeled past its foot: the page didn't move`);

  // up to its top by the wheel, and on up
  await wheel(page, box, -120, Math.ceil(s.boxRange / 120) + 6);
  s = await tops(view, box);
  assert.equal(s.box, 0, `${what} wheeled to its top: ${JSON.stringify(s)}`);
  assert.equal(s.page, before, `${what}, wheeled past its top: the page didn't move`);
}

for (const engine of engines) {
  test(`${engine}: Requests' list on its page keeps the wheel at its ends`, async (t) => {
    const browser = await launch(engine);
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    for (const lang of ["en", "zh"]) for (const [width, height] of [[900, 560], [1300, 600]]) {
      await t.test(`${lang}, ${width}x${height}`, async () => {
        const ctx = await browser.newContext({ viewport: { width, height }, reducedMotion: "reduce" });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang));
        await page.goto("http://magpie.test/?view=routing");
        const view = page.locator("#view-routing");
        await page.locator("#view-routing .rt-req").nth(39).waitFor();
        // grouped by session, as the reporter's list was
        await page.locator("#view-routing .rt-group-by button").nth(1).click();
        assert.equal(await page.locator("#view-routing .rt-session", { hasText: lang === "zh" ? "未提供会话标识" : "No session ID" }).locator(".nm").textContent(), lang === "zh" ? "未提供会话标识" : "No session ID");
        await page.waitForTimeout(500);
        await heldAtEnds(page, view, page.locator("#view-routing .rt-reqs").first(), "the requests' list");
        await ctx.close();
      });
    }
    assert.deepEqual(errors, []);
  });

  // The class, on the same page with the app's own stylesheets and
  // scripts: a box of each kind that scrolls inside a page, one added
  // later that names no rule of its own, and the boxes that must still let
  // the wheel move the page: a list too short to scroll (Chromium holds the
  // wheel in one that is told to contain, overflowing or not) and the ones
  // that scroll only sideways.
  test(`${engine}: every box that scrolls inside a page keeps the wheel; the others pass it on`, async (t) => {
    const browser = await launch(engine);
    t.after(() => browser.close());
    const lines = (n) => Array.from({ length: n }, (_, i) => `a line of text ${i + 1}`).join("\n");
    const rows = (n, tag = "div") => Array.from({ length: n }, (_, i) => `<${tag} style="height:28px;flex:0 0 100%">row ${i + 1}</${tag}>`).join("");
    const wide = `<div style="width:2400px;height:60px">wide</div>`;
    const held = {
      "rt-reqs": `<div class="rt-reqs" data-box>${rows(40)}</div>`,
      "call-pre": `<div class="call-body"><pre data-box>${lines(60)}</pre></div>`,
      "sess-talk": `<div class="sess-talk" data-box>${rows(60)}</div>`,
      "add-models-list": `<div class="add-models-list" data-box>${rows(30)}</div>`,
      "mnames": `<div class="mnames" data-box>${rows(30)}</div>`,
      "mchips": `<div class="mchips" data-box>${rows(40, "span")}</div>`,
      "lib-picks": `<div class="lib-picks" data-box>${rows(30)}</div>`,
      "rt-steps": `<ol class="rt-steps" data-box>${rows(20, "li")}</ol>`,
      "ag-diff-box": `<div class="ag-diff-box" data-box>${rows(30)}</div>`,
      "a new box": `<div style="max-height:200px;overflow-y:auto" data-box>${rows(30)}</div>`,
    };
    const passed = {
      "a short rt-reqs": `<div class="rt-reqs" data-box>${rows(3)}</div>`,
      "heat-scroll": `<div class="heat-scroll" data-box>${wide}</div>`,
      "led-wrap": `<div class="led-wrap" data-box>${wide}</div>`,
      "snip": `<pre class="snip" data-box>${"x".repeat(600)}</pre>`,
    };
    const ctx = await browser.newContext({ viewport: { width: 900, height: 700 }, reducedMotion: "reduce" });
    const page = await ctx.newPage();
    page.setDefaultTimeout(5000);
    page.setDefaultNavigationTimeout(60000);
    await page.route("**/*", serve("en"));
    await page.goto("http://magpie.test/?view=routing");
    const view = page.locator("#view-routing");
    await page.locator("#view-routing .rt-req").nth(39).waitFor();
    await view.evaluate((v, html) => v.insertAdjacentHTML("beforeend", html),
      Object.entries({ ...held, ...passed }).map(([id, h]) => `<div data-id="${id}" style="flex:none;margin:240px 0">${h}</div>`).join("") + `<div style="flex:none;height:900px"></div>`);
    const box = (id) => page.locator(`[data-id="${id}"] [data-box]`);
    for (const id of Object.keys(held)) await t.test(id, () => heldAtEnds(page, view, box(id), id));
    for (const id of Object.keys(passed)) await t.test(id + " passes the wheel on", async () => {
      await bringIn(page, view, box(id));
      const s = await tops(view, box(id));
      assert.ok(s.page < s.pageRange - 300, `${id}: the page can move down: ${JSON.stringify(s)}`);
      await wheel(page, box(id), 120, 2);
      assert.ok((await tops(view, box(id))).page > s.page, `${id}, wheeled down over: the page moved`);
      // and up past its top
      await bringIn(page, view, box(id));
      const up = await tops(view, box(id));
      await wheel(page, box(id), -120, 2);
      const after = await tops(view, box(id));
      assert.ok(after.page < up.page, `${id}, wheeled up over: the page moved: ${JSON.stringify([up, after])}`);
    });
    // the page itself chains as before
    assert.equal(await view.evaluate((v) => getComputedStyle(v).overscrollBehaviorY), "auto", "the page's own");
    await ctx.close();

    // a short window: the model chips pass the wheel on to the editor's
    // body, as #833 asked (悠悠哥 on Discord: the body could never be
    // wheeled on past them)
    const short = await browser.newPage({ viewport: { width: 900, height: 600 } });
    await short.route("**/*", serve("en"));
    await short.goto("http://magpie.test/?view=routing");
    await short.locator("#view-routing .rt-req").nth(39).waitFor();
    await short.locator("#view-routing").evaluate((v, html) => v.insertAdjacentHTML("afterbegin", html), held.mchips);
    const chips = short.locator("#view-routing .mchips");
    const b = await chips.boundingBox();
    await short.mouse.move(b.x + 20, b.y + 20);
    await short.waitForTimeout(100);
    assert.ok(await chips.evaluate((m) => m.scrollHeight > m.clientHeight), "the chips scroll");
    assert.equal(await chips.evaluate((m) => getComputedStyle(m).overscrollBehaviorY), "auto", "the chips in a short window pass the wheel on");
    await short.close();
  });
}

// The guard: every rule in every stylesheet under assets that makes a box
// scroll vertically (overflow or overflow-y auto or scroll) is built from
// its own selector, filled past its height and pointed at, and has to hold
// the wheel at its ends (overscroll-behavior-y contain). A new scroller
// held by nothing, or a rule that sets a box's overscroll-behavior back to
// auto, is red here, named by its file and selector. The page itself
// (.view) is the one box that chains as before.
const PAGES = new Set([".view", "body.window #view-usage", "body.window #view-routing"]);
// a selector as the elements it names, one per compound, the last the box:
// :where() and :is() take their first choice; :not() and other
// pseudo-classes are left out
function compounds(sel) {
  const out = [];
  let cur = "", depth = 0, comb = null;
  const flush = () => { if (cur) { out.push({ comb: comb || " ", text: cur }); cur = ""; comb = null; } };
  for (const ch of sel.trim()) {
    if (ch === "(" || ch === "[") depth++;
    else if (ch === ")" || ch === "]") depth--;
    if (!depth && /\s/.test(ch)) { flush(); continue; }
    if (!depth && /[>+~]/.test(ch)) { flush(); comb = ch; continue; }
    cur += ch;
  }
  flush();
  return out.map(({ comb, text }) => {
    text = text.replace(/:(?:where|is)\(\s*([^,()]+)[^()]*\)/g, "$1").replace(/:not\([^()]*\)/g, "").replace(/::?[a-z-]+(\([^()]*\))?/g, "");
    return {
      comb,
      tag: /^[a-z][a-z0-9-]*/i.exec(text)?.[0] || "div",
      id: /#([\w-]+)/.exec(text)?.[1],
      cls: [...text.matchAll(/\.([\w-]+)/g)].map((m) => m[1]),
      attrs: [...text.matchAll(/\[([\w-]+)(?:[~|^$*]?=["']?([^"'\]]*)["']?)?\]/g)].map((m) => [m[1], m[2] ?? ""]),
    };
  });
}
// the box a selector names, built at the foot of the page, filled past
// its height and pointed at: what it holds
function tryBox({ parts }) {
  const stage = document.createElement("div");
  stage.style.cssText = "flex:none;position:relative";
  document.querySelector("#view-routing").append(stage);
  const added = [];
  let at = stage, box = null;
  for (const p of parts) {
    if (p.tag === "html") continue;
    if (p.tag === "body") { for (const c of p.cls) if (!document.body.classList.contains(c)) { document.body.classList.add(c); added.push(c); } continue; }
    const e = document.createElement(p.tag);
    if (p.id) e.id = p.id;
    e.classList.add(...p.cls);
    for (const [k, v] of p.attrs) e.setAttribute(k, v);
    if ((p.comb === "+" || p.comb === "~") && at !== stage) at.after(e); else at.append(e);
    at = box = e;
  }
  const done = (r) => { stage.remove(); for (const c of added) document.body.classList.remove(c); return r; };
  if (!box) return done({ skip: "names no element" });
  // its size, so it overflows wherever it sits, and shown
  for (const [k, v] of [["height", "200px"], ["min-height", "0"], ["max-height", "200px"]]) box.style.setProperty(k, v, "important");
  for (let e = box; e !== stage; e = e.parentElement) if (getComputedStyle(e).display === "none") e.style.setProperty("display", "block", "important");
  if (box.tagName === "TEXTAREA") box.value = Array.from({ length: 60 }, (_, i) => "line " + i).join("\n");
  else for (let i = 0; i < 40; i++) {
    const row = document.createElement(/^[UO]L$/.test(box.tagName) ? "li" : "div");
    row.style.cssText = "display:block;flex:0 0 100%;width:100%;height:28px";
    row.textContent = "row " + i;
    box.append(row);
  }
  const overflowY = getComputedStyle(box).overflowY, overflows = box.scrollHeight > box.clientHeight + 1;
  box.dispatchEvent(new PointerEvent("pointerover", { bubbles: true, composed: true }));
  return done({ overflowY, overflows, overscroll: getComputedStyle(box).overscrollBehaviorY });
}

for (const engine of engines) {
  test(`${engine}: every scroller in every stylesheet holds the wheel at its ends`, async () => {
    const browser = await launch(engine);
    try {
      const page = await browser.newPage({ viewport: { width: 1300, height: 800 } });
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      await page.route("**/*", serve("en"));
      await page.goto("http://magpie.test/?view=routing");
      await page.locator("#view-routing .rt-req").nth(39).waitFor();
      // every stylesheet under assets, added where the page doesn't link it
      const sheets = (await fs.readdir(assets, { recursive: true })).map((f) => f.split(path.sep).join("/")).filter((f) => f.endsWith(".css")).sort();
      await page.evaluate((files) => {
        const have = new Set([...document.styleSheets].map((s) => s.href && new URL(s.href).pathname.slice(1)));
        return Promise.all(files.filter((f) => !have.has(f)).map((f) => new Promise((ok) => {
          const l = document.createElement("link");
          l.rel = "stylesheet"; l.href = f; l.onload = l.onerror = ok;
          document.head.append(l);
        })));
      }, sheets);
      // the rules that scroll a box vertically, each selector of a list
      const rules = await page.evaluate(() => {
        const out = [];
        const walk = (list, file, media) => {
          for (const r of list) {
            if (r instanceof CSSStyleRule) {
              if (/auto|scroll|overlay/.test(r.style.overflowY)) for (const sel of r.selectorText.split(/,(?![^(]*\))/)) out.push({ file, sel: sel.trim(), media });
            } else if (r.cssRules) walk(r.cssRules, file, r.media ? r.media.mediaText : r.conditionText || media);
          }
        };
        for (const s of document.styleSheets) if (s.href) walk(s.cssRules, new URL(s.href).pathname.slice(1), "");
        return out;
      });
      const files = new Set(rules.map((r) => r.file));
      for (const f of ["app.css", "plugins/routing/routing.css", "plugins/sessions/sessions.css", "plugins/library/library.css"]) assert.ok(files.has(f), `scrollers read from ${f}: ${[...files]}`);

      const bad = [], held = [];
      for (const r of rules) {
        if (PAGES.has(r.sel.replace(/\s+/g, " "))) continue;
        const name = `${r.file}: ${r.media ? "@media " + r.media + " " : ""}${r.sel}`;
        // a rule under @media is tried at a size it holds for
        if (r.media) {
          let fits = false;
          for (const [width, height] of [[1300, 800], [600, 800], [1300, 560], [360, 640]]) {
            await page.setViewportSize({ width, height });
            if (await page.evaluate((m) => matchMedia(m).matches, r.media)) { fits = true; break; }
          }
          if (!fits) { bad.push(`${name}: no window size here that its @media holds for`); continue; }
        }
        const got = await page.evaluate(tryBox, { parts: compounds(r.sel) });
        await page.setViewportSize({ width: 1300, height: 800 });
        if (got.skip) bad.push(`${name}: ${got.skip}`);
        else if (!/auto|scroll|overlay/.test(got.overflowY)) bad.push(`${name}: built from its selector, but the rule didn't take (overflow-y ${got.overflowY})`);
        else if (!got.overflows) bad.push(`${name}: filled, but doesn't overflow`);
        else if (got.overscroll !== "contain") bad.push(`${name}: overscroll-behavior-y ${got.overscroll}, so the wheel at its ends moves the page`);
        else held.push(name);
      }
      assert.deepEqual(bad, [], `${held.length} scrollers hold the wheel; these don't`);
      assert.ok(held.length >= 30, `scrollers checked: ${held.length}`);
    } finally {
      await browser.close();
    }
  });
}

// Safari 15 (macOS 12's WebKit unless Safari was updated, #220) has no
// overscroll-behavior: compat.js keeps the wheel at a held box's ends
// itself. Here the engine is made to lack it: CSS.supports says no and
// every box's overscroll-behavior is auto.
for (const engine of engines) {
  test(`${engine}: without overscroll-behavior, Requests' list still keeps the wheel at its ends`, async () => {
    const browser = await launch(engine);
    try {
      const ctx = await browser.newContext({ viewport: { width: 900, height: 560 }, reducedMotion: "reduce" });
      await ctx.addInitScript(() => {
        const supports = CSS.supports.bind(CSS);
        CSS.supports = (...a) => !/overscroll/.test(a.join(" ")) && supports(...a);
        document.addEventListener("DOMContentLoaded", () => document.head.append(Object.assign(document.createElement("style"), { textContent: "* { overscroll-behavior: auto !important; }" })));
      });
      const page = await ctx.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      await page.route("**/*", serve("en"));
      await page.goto("http://magpie.test/?view=routing");
      const view = page.locator("#view-routing");
      await page.locator("#view-routing .rt-req").nth(39).waitFor();
      const list = page.locator("#view-routing .rt-reqs").first();
      assert.equal(await list.evaluate((l) => getComputedStyle(l).overscrollBehaviorY), "auto", "the engine is made to lack it");
      await heldAtEnds(page, view, list, "the requests' list");
      // the page's own wheel still moves it, over its gutter
      const vb = await view.boundingBox(), before = await view.evaluate((v) => v.scrollTop);
      await page.mouse.move(vb.x + 3, vb.y + vb.height / 2);
      await page.mouse.wheel(0, 200);
      await page.waitForTimeout(400);
      assert.notEqual(await view.evaluate((v) => v.scrollTop), before, "the page itself still scrolls");
    } finally {
      await browser.close();
    }
  });
}
