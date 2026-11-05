// Run with Node's test runner and Playwright on the module path; see README.md.
// The Routing page's request list by gateway key (acezan on Discord: "路由
// 页看到 api 的使用情况，像每个 agent 那样"): once the user has a key, By key
// sits beside By request and By session, and heads each key's requests
// with the key's name as Gateway keys has it now, its agents beside it, and
// what they came to; the requests sent with no key (from this computer, or
// kept before a route named its key) are a group of their own. A row says
// its key under the pointer. With no key there is no By key, and a list
// left on it reads by request. In every language, at 1100 and 420. No
// backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function fixture() {
  const now = Date.now(), at = (s) => new Date(now - s * 1000).toISOString();
  const seat = { id: "relay", provider: "relay", name: "Relay", who: "team", kind: "key", model: "m1" };
  const req = (id, agent, ago, key, extra = {}) => ({ id, seq: id, time: at(ago), agent, session: "s-" + id,
    ...(key ? { callerKey: key.id, callerKeyName: key.name } : {}),
    model: "relay/m1", provider: "relay", order: [seat], tries: [{ id: seat.id, model: "m1", start: at(ago), done: true, status: 200, ms: 500 }],
    done: true, status: 200, ms: 500, tokens: 1000, ...extra });
  const laptop = { id: "k-laptop", name: "Laptop" }, server = { id: "k-server", name: "Server" };
  return [
    req(1, "claude", 6, laptop),
    req(2, "codex", 5, laptop),
    req(3, "claude", 4, server),
    req(4, "claude", 3, null), // from this computer
    req(5, "codex", 2, laptop),
    req(6, "claude", 1, { id: "k-gone", name: "Old key" }), // a key removed since
  ];
}

function serve(lang, routes, keys) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "claude", name: "Claude Code", fields: [] }, { id: "codex", name: "Codex", fields: [] }], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.has("wait")) return new Promise(() => {});
      return json({ mine: true, now: new Date().toISOString(), seq: 6, totals: { requests: 6, rerouted: 0, errors: 0 }, routes });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/caller-keys") return json({ keys, accountNames: {} });
    if (url.pathname === "/api/gateway/session-titles") return json({ titles: {} });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/window/fit") return route.fulfill({ status: 204 });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    return route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { byKey: "By key", none: "No gateway key", key: "Gateway key" },
  zh: { byKey: "按密钥", none: "无网关密钥", key: "网关密钥" },
  "zh-TW": { byKey: "按金鑰", none: "無閘道金鑰", key: "閘道金鑰" },
  ja: { byKey: "キー別", none: "ゲートウェイキーなし", key: "ゲートウェイキー" },
  de: { byKey: "Nach Schlüssel", none: "Ohne Zugangsschlüssel", key: "Zugangsschlüssel" },
};
// the keys as Gateway keys lists them: Laptop renamed since its requests
const keys = [{ id: "k-laptop", name: "MacBook" }, { id: "k-server", name: "Server" }];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}: routing lists the requests by gateway key`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const w = words[lang], errors = [];
        const open = async (ks, setup) => {
          const page = await browser.newPage({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", serve(lang, fixture(), ks));
          if (setup) await page.addInitScript(setup);
          await page.goto("http://magpie.test/?view=routing");
          await page.locator(".rt-req").nth(5).waitFor();
          return page;
        };
        const page = await open(keys);
        const byKey = page.locator(".rt-group-by .rt-day", { hasText: w.byKey });
        await byKey.waitFor();
        assert.equal(await page.locator(".rt-group-by .rt-day").count(), 3, "By request, By session, By key");
        await byKey.click();
        await page.locator(".rt-session").nth(3).waitFor();
        assert.equal(await byKey.getAttribute("aria-pressed"), "true");
        assert.equal(await page.evaluate(() => localStorage.getItem("magpie.routingGroupBy")), "key");

        const heads = await page.locator(".rt-session").evaluateAll((es) => es.map((e) => ({
          name: e.querySelector(".nm").textContent, title: e.querySelector(".nm").title,
          dir: e.querySelector(".dir")?.hidden ? "" : e.querySelector(".dir")?.textContent || "",
          count: e.querySelector(".session-count")?.textContent || "",
        })));
        // by each group's newest: the removed key (by the name it had),
        // Laptop (by its name now), no key, Server
        assert.deepEqual(heads.map((h) => h.name), ["Old key", "MacBook", w.none, "Server"], JSON.stringify(heads));
        const of = (name) => heads.find((h) => h.name === name);
        assert.equal(of("MacBook").dir, "Codex, Claude Code", "the key's agents beside it");
        assert.match(of("MacBook").count, /3/, "Laptop's three requests");
        assert.match(of("Server").count, /1/);
        assert.match(of("MacBook").title, new RegExp(`^${w.key}: MacBook\\n`));
        assert.doesNotMatch(of(w.none).title, new RegExp(`^${w.key}:`), "no key: said as none");
        // each request under its key, and a row says its key under the pointer
        const order = await page.locator(".rt-reqs > :is(.rt-session, .rt-req)").evaluateAll((es) => es.map((e) => e.classList.contains("rt-session") ? "head" : "row"));
        assert.deepEqual(order, ["head", "row", "head", "row", "row", "row", "head", "row", "head", "row"]);
        const rowTitles = await page.locator(".rt-req").evaluateAll((es) => es.map((e) => e.title));
        assert.equal(rowTitles.filter((x) => x.includes(`\n${w.key}: MacBook`)).length, 3, JSON.stringify(rowTitles));
        assert.equal(rowTitles.filter((x) => x.includes(`${w.key}:`)).length, 5, "the request with no key names none");
        // a key's group folds
        await page.locator(".rt-session", { hasText: "MacBook" }).click();
        await page.waitForFunction(() => document.querySelectorAll(".rt-req").length === 3);

        const fit = await page.evaluate(() => ({ client: document.documentElement.clientWidth, scroll: document.documentElement.scrollWidth }));
        assert(fit.scroll <= fit.client + 1, `the page overflows at ${width}: ${JSON.stringify(fit)}`);
        const reqs = await page.locator(".rt-reqs").evaluate((e) => ({ client: e.clientWidth, scroll: e.scrollWidth }));
        assert(reqs.scroll <= reqs.client + 1, `request list overflows at ${width}: ${JSON.stringify(reqs)}`);
        const clipped = await page.locator(".rt-session").evaluateAll((es) => es.some((e) => [...e.children].some((c) => !c.hidden && c.getBoundingClientRect().right > e.getBoundingClientRect().right + 1)));
        assert.equal(clipped, false, "each heading's parts inside it");
        const bar = await page.locator(".rt-group-by").evaluate((e) => ({ client: e.clientWidth, scroll: e.scrollWidth }));
        assert(bar.scroll <= bar.client + 1, `the group bar overflows at ${width}: ${JSON.stringify(bar)}`);
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-by-key.png`), fullPage: true });
        }

        // no gateway key: no By key, and a list left on it reads by request
        const bare = await open([], () => localStorage.setItem("magpie.routingGroupBy", "key"));
        await bare.waitForFunction(() => [...document.querySelectorAll(".rt-group-by .rt-day")].filter((b) => !b.hidden).length === 2);
        assert.equal(await bare.locator(".rt-group-by .rt-day", { hasText: w.byKey }).isVisible(), false);
        assert.equal(await bare.locator(".rt-session").count(), 0, "by request");
        assert.deepEqual(errors, []);
      });
    }
  }
}
