// Run with Node's test runner and Playwright on the module path; see README.md.
// iamleehom on X ("为什么路由中不显示供应商名称了？"): two OpenAI-compatible
// providers' keys, neither named, served gpt-6-astra under Smart, and the
// stage's rows read "sk-4…ab10" and "sk-a…eb0c": the masked keys where the
// provider's name was looked for, the same OpenAI logo on both. A key's row
// is named by its provider, the key after it, muted; the provider's name
// keeps its room at a narrow width, the key's gives it up; a fold of a
// provider's keys names the provider once, and the keys under it by their
// own names; the answered try's card names the provider too. Every
// language, both engines, a wide and a narrow width; the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const ago = (s) => new Date(now.getTime() - s * 1e3).toISOString();
const M = "gpt-6-astra";
const key = (provider, name, k) => ({ id: `${provider}#${k}`, provider, name, icon: "openai", who: k, kind: "key", model: M });
const order = [key("relay-east", "Relay East", "sk-4…ab10"), key("relay-west", "Relay West", "sk-a…eb0c")];
const group = { id: "astra", name: "Astra", routing: "", members: ["relay-east/" + M, "relay-west/" + M] };
const first = { id: 1, seq: 1, time: ago(30), agent: "codex", model: "astra", provider: "relay-east", done: true, status: 200, ms: 900, tokens: 1200, group, order,
  tries: [{ id: order[0].id, model: M, start: ago(30), done: true, status: 200, ms: 900 }] };
// one provider's six keys, folded (FOLD_AT) and opened
const many = Array.from({ length: 6 }, (_, i) => key("pool", "Pool", `sk-${i}…000${i}`));
const pooled = { id: 2, seq: 2, time: ago(10), agent: "codex", model: "pool/" + M, provider: "pool", done: true, status: 200, ms: 700, tokens: 900, order: many,
  tries: [{ id: many[0].id, model: M, start: ago(10), done: true, status: 200, ms: 700 }] };

// live: routes go out on the long poll as the test sends them
function serve(lang, routes, live) {
  return async (request) => {
    const url = new URL(request.request().url());
    const json = (data) => request.fulfill({ json: data });
    if (url.pathname === "/boot.js") return request.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return request.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "codex", name: "Codex", path: "/test/config.toml", fields: [] }], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) {
        if (!live) await new Promise((r) => setTimeout(r, 20e3));
        else if (live.queued.length) return json(live.queued.shift());
        else { live.pending.push(request); return; }
      }
      return json({ mine: true, now: now.toISOString(), seq: routes.length, totals: { requests: routes.length, rerouted: 0, errors: 0 }, routes });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3425" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await request.fulfill({ body: await fs.readFile(file).catch(() => ""), contentType });
  };
}

async function open(browser, lang, width, routes, live) {
  const context = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
  const page = await context.newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, routes, live));
  await page.goto("http://magpie.test/?view=routing");
  return { context, page, errors };
}

const rowsOf = (page) => page.locator(".rt-accts > li:not(.rt-fold):not(.rt-sub)").filter({ has: page.locator("b span.who") });
const read = (rows) => rows.evaluateAll((ls) => ls.map((li) => {
  const b = li.querySelector("b"), prov = b.querySelector("span.prov"), who = b.querySelector("span.who");
  return { prov: prov.textContent, name: who.textContent, provCut: prov.scrollWidth > prov.clientWidth + 1 };
}));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: a key's row is named by its provider`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      for (const width of [1100, 440]) {
        await t.test(`${lang} ${width}px: two providers' unnamed keys`, async () => {
          const { context, page, errors } = await open(browser, lang, width, [first]);
          t.after(() => context.close());
          const rows = rowsOf(page);
          await rows.nth(order.length - 1).waitFor();
          const seen = await read(rows);
          assert.deepEqual(seen.map((r) => [r.prov, r.name]), [["Relay East", "sk-4…ab10"], ["Relay West", "sk-a…eb0c"]]);
          if (width === 1100) for (const r of seen) assert.equal(r.provCut, false, `${r.prov} is whole`);
          assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, "nothing runs off the page");
          assert.deepEqual(errors, []);
        });
        await t.test(`${lang} ${width}px: the answer's card names the provider`, async () => {
          const live = { pending: [], queued: [], seq: 0 };
          const { context, page, errors } = await open(browser, lang, width, [], live);
          t.after(() => context.close());
          const send = (route) => {
            const d = { mine: true, now: new Date().toISOString(), seq: ++live.seq, totals: { requests: live.seq, rerouted: 0, errors: 0 }, routes: [route] };
            const r = live.pending.shift();
            if (r) r.fulfill({ json: d }); else live.queued.push(d);
          };
          await page.locator(".rt-cap").waitFor({ state: "attached" });
          const going = { ...first, time: new Date().toISOString(), done: false, status: 0, ms: 0, tokens: 0,
            tries: [{ ...first.tries[0], start: new Date().toISOString(), done: false, status: 0, ms: 0 }] };
          send(going);
          await rowsOf(page).nth(order.length - 1).waitFor();
          send({ ...going, done: true, status: 200, ms: 900, tokens: 1200, tries: [{ ...going.tries[0], done: true, status: 200, ms: 900 }] });
          const who = page.locator(".rt-cap .rt-done > .who");
          await who.waitFor();
          assert.equal(await who.textContent(), "Relay East · sk-4…ab10");
          assert.deepEqual(errors, []);
        });
        await t.test(`${lang} ${width}px: a fold names the provider once`, async () => {
          const { context, page, errors } = await open(browser, lang, width, [pooled]);
          t.after(() => context.close());
          const fold = page.locator(".rt-accts > li.rt-fold");
          await fold.waitFor();
          assert.equal(await fold.locator("b span.who").textContent(), "Pool");
          if ((await fold.getAttribute("aria-expanded")) !== "true") await fold.click();
          const rows = rowsOf(page);
          await rows.nth(many.length - 1).waitFor();
          const seen = await read(rows);
          assert.deepEqual(seen.map((r) => [r.prov, r.name]), many.map((k) => ["", k.who]));
          assert.deepEqual(errors, []);
        });
      }
    }
  });
}
