// Run with Node's test runner and Playwright on the module path; see README.md.
// How far the Routing stage opens is the reader's to pick (ipsikent on X:
// often only which model a route uses matters, not which provider's
// account it went to). Expand, at the end of the stage's top line, offers Automatic (long
// lists folded, short ones open, as before), Models (every provider's
// accounts and keys, two or more, folded into one row naming the model;
// every group in the group open) and Accounts (each account and key in its
// own row). Picking one forgets what the reader had opened or folded on
// the stage, so every row follows it at once; it is kept across a reload,
// and a row clicked after it is kept as before. In Chromium and WebKit, in
// every language, at 1100 and 420 wide; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { reach } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const ago = (s) => new Date(now.getTime() - s * 1e3).toISOString();
// a rest runs from when the test builds it, not from when the file loaded:
// under load the webkit half runs minutes after, past a 3-minute rest
const ahead = (m) => new Date(Date.now() + m * 60e3).toISOString();
const M = "gpt-6";
const rest = (k) => ({ why: "rate", until: ahead(3), key: k, for: 180e9 });
const acct = (p, name, user, extra = {}) => ({ id: `${p}@${user}`, provider: p, name, who: user, kind: "account", model: M, plan: "Pro", ...extra });
// the group: a group in it of eight ChatGPT accounts, then a team of
// three accounts, a provider of seven and one key
const pool = Array.from({ length: 8 }, (_, i) => `p${i + 1}@x.test`);
const big = Array.from({ length: 7 }, (_, i) => `b${i + 1}@x.test`);
const team = ["t1@x.test", "t2@x.test", "t3@x.test"];
const duo = ["d1@x.test", "d2@x.test"];
const order = (resting = ["p2@x.test"]) => [
  ...pool.map((u) => acct("chatgpt", "ChatGPT", u, { via: ["pool"], ...(resting.includes(u) ? { rest: rest(`chatgpt@${u}`) } : {}) })),
  ...team.map((u) => acct("team", "Team", u)),
  ...big.map((u) => acct("big", "Big", u)),
  ...duo.map((u) => acct("duo", "Duo", u)),
  { id: "solo#s1", provider: "solo", name: "Solo", who: "solo-1", kind: "key", model: M },
];
const group = { id: "main", name: "Main", routing: "", members: ["chatgpt/" + M, "team/" + M, "big/" + M, "duo/" + M, "solo/" + M],
  subs: [{ id: "pool", name: "Pool", routing: "order", in: "main" }], via: ["pool", "", "", "", ""] };
const route = (id, tries, extra = {}) => ({ id, seq: id, time: ago(60 - id), agent: "codex", model: "main", provider: "chatgpt", done: tries.every((x) => x.done),
  status: 200, ms: 900, tokens: 1200, group, order: order(), tries, ...extra });
const first = route(1, [{ id: "chatgpt@p1@x.test", model: M, start: ago(59), done: true, status: 200, ms: 900 }]);

function serve(lang, live) {
  return async (request) => {
    const url = new URL(request.request().url());
    const json = (data) => request.fulfill({ json: data });
    if (url.pathname === "/boot.js") return request.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return request.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "codex", name: "Codex", path: "/test/config.toml", fields: [] }], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) {
        if (live.queued.length) return json(live.queued.shift());
        live.pending.push(request);
        return;
      }
      return json({ mine: true, now: now.toISOString(), seq: 1, totals: { requests: 1, rerouted: 0, errors: 0 }, routes: [first] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await request.fulfill({ body: await fs.readFile(file), contentType });
  };
}

async function open(t, engine, lang, width = 1100) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  t.after(() => browser.close());
  const page = await browser.newPage({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  const live = { pending: [], queued: [], seq: 1 };
  await page.route("**/*", serve(lang, live));
  const ready = async () => {
    await page.goto("http://magpie.test/?view=routing");
    await page.locator(".rt-accts li.rt-sub").first().waitFor();
    await page.waitForFunction(() => document.querySelector(".rt-accts li.rt-fold em")?.textContent.trim());
  };
  await ready();
  const send = (...routes) => {
    const d = { mine: true, now: new Date().toISOString(), seq: ++live.seq, totals: { requests: live.seq }, routes };
    const r = live.pending.shift();
    if (r) r.fulfill({ json: d }); else live.queued.push(d);
  };
  const say = (key, vars = {}) => page.evaluate(([lang, key, vars]) => {
    const s = (lang !== "en" && I18N[lang]?.[key]) || key;
    return s.replace(/\{(\w+)\}/g, (_, k) => vars[k] ?? `{${k}}`);
  }, [lang, key, vars]);
  const sub = page.locator(".rt-accts li.rt-sub");
  const fold = (name) => page.locator(".rt-accts li.rt-fold", { has: page.locator(".who", { hasText: new RegExp(`^${name}$`) }) });
  // what the stage lists, top to bottom: a group in the group's heading,
  // a fold (+ open or - folded), or an account's or key's row
  const names = () => page.locator(".rt-accts > li").evaluateAll((lis) => lis.map((li) => li.classList.contains("rt-sub") ? "sub:" + li.querySelector("b").textContent + (li.classList.contains("open") ? "+" : "-")
    : li.classList.contains("rt-fold") ? "fold:" + li.querySelector(".who").textContent + (li.classList.contains("open") ? "+" : "-") : li.querySelector(".who")?.textContent));
  const viewTop = () => page.evaluate(() => document.querySelector("#view-routing").scrollTop);
  return { page, errors, send, say, sub, fold, names, ready, viewTop };
}

const spills = (page, sel) => page.locator(sel).evaluateAll((els) => els.filter((e) => !e.hidden && e.offsetParent && (e.scrollWidth > e.clientWidth + 1 || e.getBoundingClientRect().right > document.documentElement.clientWidth))
  .map((e) => e.className + ": " + e.textContent.trim().slice(0, 60)));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}: Expand opens the stage to models or to accounts, kept across a reload`, async (t) => {
        const { page, errors, say, names, ready } = await open(t, engine, lang, width);
        const fold = (name) => page.locator(".rt-accts li.rt-fold", { has: page.locator(".who", { hasText: new RegExp(`^${name}$`) }) });
        const auto = ["sub:Pool-", "fold:Team+", ...team, "fold:Big-", ...duo, "solo-1"];
        assert.deepEqual(await names(), auto);
        // the reader had opened Big by hand
        await fold("Big").click();
        await page.waitForFunction(() => document.querySelectorAll(".rt-accts > li").length === 1 + 1 + 3 + 1 + 7 + 2 + 1);
        const pick = async (name) => {
          const b = page.locator("#rtDepth");
          assert.equal((await b.textContent()).trim(), await say("Expand"));
          assert.equal(await b.getAttribute("title"), await say("Expand routes to"));
          await b.click();
          const menu = page.locator(".proto-menu.rt-depth-menu");
          await menu.waitFor();
          assert.equal(await b.getAttribute("aria-expanded"), "true");
          assert.equal(await menu.locator(".pm-head").textContent(), await say("Expand routes to"));
          assert.deepEqual(await menu.locator(".pm-name").allTextContents(), [await say("Automatic"), await say("Models"), await say("Accounts")]);
          // inside the window, whatever its width
          const box = await menu.boundingBox();
          assert.ok(box.x >= 0 && box.x + box.width <= width, `menu at ${box.x}+${box.width}`);
          await menu.locator(".pm-item", { has: page.locator(".pm-name", { hasText: new RegExp(`^${await say(name)}$`) }) }).click();
          await menu.waitFor({ state: "detached" });
        };
        const ticked = async () => {
          await page.locator("#rtDepth").click();
          const on = await page.locator(".proto-menu.rt-depth-menu .pm-item.on .pm-name").textContent();
          await page.keyboard.press("Escape");
          await page.locator(".proto-menu.rt-depth-menu").waitFor({ state: "detached" });
          return on;
        };
        const models = ["sub:Pool+", "fold:ChatGPT-", "fold:Team-", "fold:Big-", "fold:Duo-", "solo-1"];
        await pick("Models");
        await page.waitForFunction((n) => document.querySelectorAll(".rt-accts > li").length === n, models.length);
        assert.deepEqual(await names(), models);
        // a fold names its model, as the reader asked
        assert.equal(await fold("Duo").locator(".mdl").textContent(), M);
        assert.equal(await fold("Duo").locator(".n").textContent(), await say("{n} accounts", { n: 2 }));
        await ready();
        assert.deepEqual(await names(), models);
        assert.equal(await ticked(), await say("Models"));
        // a row clicked after the pick is kept as before
        await fold("Team").click();
        await page.waitForFunction((n) => document.querySelectorAll(".rt-accts > li").length === n, models.length + 3);
        await ready();
        assert.deepEqual(await names(), ["sub:Pool+", "fold:ChatGPT-", "fold:Team+", ...team, "fold:Big-", "fold:Duo-", "solo-1"]);

        const accounts = ["sub:Pool+", "fold:ChatGPT+", ...pool, "fold:Team+", ...team, "fold:Big+", ...big, ...duo, "solo-1"];
        await pick("Accounts");
        await page.waitForFunction((n) => document.querySelectorAll(".rt-accts > li").length === n, accounts.length);
        assert.deepEqual(await names(), accounts);
        await ready();
        assert.deepEqual(await names(), accounts);

        // back to Automatic: long lists folded, short ones open, as at first
        await pick("Automatic");
        await page.waitForFunction((n) => document.querySelectorAll(".rt-accts > li").length === n, auto.length);
        assert.deepEqual(await names(), auto);
        await ready();
        assert.deepEqual(await names(), auto);
        assert.equal(await ticked(), await say("Automatic"));
        // Expand ends the stage's top line, beside the request line and
        // inside the card; nothing in the page head or that line spills
        const box = await page.evaluate(() => {
          const b = document.querySelector("#rtDepth").getBoundingClientRect(), w = document.querySelector(".rt-what").getBoundingClientRect(), c = document.querySelector(".rt-top").getBoundingClientRect();
          return { top: Math.round(b.top - w.top), right: Math.round(c.right - b.right), after: b.left >= w.right - 1, h: Math.round(b.height) };
        });
        assert.ok(box.after && box.right >= -8 && box.right <= 2 && Math.abs(box.top) <= 6, JSON.stringify(box));
        assert.deepEqual(await spills(page, "#view-routing .row-head > *"), []);
        assert.deepEqual(await spills(page, ".rt-toprow > *"), []);
        assert.deepEqual(errors, []);
      });
    }
  }
}
