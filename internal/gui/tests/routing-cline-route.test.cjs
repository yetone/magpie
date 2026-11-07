// Run with Node's test runner and Playwright on the module path; see README.md.
// A ClinePass reply named for a route ClinePass picked itself
// (vmc/deepseek-v4p1-flash-1017-contributor-fallbacks) and no provider
// (zRain on Discord, 10-24): its tag says "ClinePass route …", not
// "served …", and its title and story say it is the route's name, not
// another model, and whether DeepSeek was asked for: asked and not said
// who answered, or nothing asked (with no tick to point to: since 11-06
// Cline refuses the pin, #5442). Another vendor's
// swap still reads as one. The Usage page's Requests say the route too.
// English and Chinese; the API is faked. MAGPIE_ROUTE_ASSETS points at an
// older asset folder to watch it fail.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ROUTE_ASSETS || path.join(__dirname, "../assets"));
const now = new Date();
const day = [now.getFullYear(), now.getMonth() + 1, now.getDate()].map((n) => String(n).padStart(2, "0")).join("-");
const at = (i) => new Date(now.getTime() - (i + 1) * 60e3).toISOString();
const VMC = "vmc/deepseek-v4p1-flash-1017-contributor-fallbacks";
const DS = "cline-pass/deepseek-v4.1-flash-1017";
const key = { id: "clinepass", provider: "clinepass", name: "ClinePass", kind: "provider", model: DS };
const other = { id: "relay", provider: "relay", name: "Relay", kind: "provider", model: "gpt-5.5" };
// newest first: DeepSeek asked for, nothing asked for, another vendor's swap
const cases = [
  { k: key, model: DS, served: VMC, pin: "deepseek" },
  { k: key, model: DS, served: VMC },
  { k: other, model: "gpt-5.5", served: "gpt-4o-mini" },
];
const routes = cases.map((c, i) => ({
  id: 100 - i, seq: 100 - i, time: at(i), agent: "codex", model: c.k.id + "/" + c.model, provider: c.k.id,
  order: [c.k], tries: [{ id: c.k.id, model: c.model, start: at(i), done: true, status: 200, ms: 900, served: c.served, swapped: true, ...(c.pin ? { pin: c.pin } : {}) }],
  done: true, status: 200, ms: 900, tokens: 1200, served: c.served,
}));
const ROWS = [{
  t: at(0), agent: "codex", agentName: "Codex", icon: "codex-color", provider: "clinepass", providerName: "ClinePass",
  req: "clinepass/" + DS, model: DS, served: VMC, swapped: true, in: 300, out: 40, ms: 2000, status: 200, rid: "gen-0", ep: "/backend-api/codex/responses → /v1/chat/completions",
}];

function serve(lang) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/config.toml", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) await new Promise((r) => setTimeout(r, 20e3));
      return json({ mine: true, now: now.toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") {
      const d = url.searchParams.get("day");
      return json({ cut: false, days: [{ day, requests: routes.length }], routes: d ? routes : [] });
    }
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/requests") return json({ period: url.searchParams.get("period"), rows: ROWS, offset: 0, total: 1, calls: 1, errors: 0, input: 300, output: 40, cache_read: 0, cache_write: 0, reasoning: 0, cost: 0, unpriced: 1, agents: [{ id: "codex", name: "Codex", icon: "codex-color" }] });
    if (url.pathname === "/api/usage/requests/content") return json({ found: false, why: "read" });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage") return json({ calls: 1, errors: 0, input: 300, output: 40, cache_read: 0, cache_write: 0, reasoning: 0, unpriced: 1, cost: 0, bucket: "day", series: [], agents: [], models: [], path: "~/.config/magpie/usage.jsonl" });
    if (url.pathname === "/api/sessions") return json({ sessions: [], dirs: [] });
    if (url.pathname === "/api/sessions/stats") return json({ from: "", to: "", days: [], agents: {} });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const want = {
  en: {
    tags: ["ClinePass route " + VMC, "ClinePass route " + VMC, "served gpt-4o-mini"],
    route: /a route it picks itself: the name is the route's, not another model's/,
    asked: /deepseek was asked for, but its reply didn't say which provider answered/,
    none: /No provider was asked for, and its reply didn't say which one answered\./,
    swap: /likely another model/,
    usage: /didn't say which provider answered\.$/,
  },
  zh: {
    tags: ["ClinePass 路由 " + VMC, "ClinePass 路由 " + VMC, "实际 gpt-4o-mini"],
    route: /这是路由的名字，不是换成了别的模型/,
    asked: /请求时指定了 deepseek，但回复没有说是哪家上游作答/,
    none: /请求时没有指定上游，回复也没有说是哪家作答。/,
    swap: /很可能换成了别的模型/,
    usage: /回复没有说是哪家上游作答。$/,
  },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: a ClinePass route reads as its route, with what was asked`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const context = await browser.newContext({ viewport: { width: 1100, height: 760 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang));
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-cline-route.png`), fullPage: true });
        }
        await browser.close();
      });
      await page.goto("http://magpie.test/?view=routing");
      await page.locator(".rt-day").nth(1).waitFor({ state: "visible", timeout: 15000 });
      await page.locator(".rt-day").nth(1).click();
      await page.locator(".rt-req").nth(cases.length - 1).waitFor();
      const w = want[lang];

      const tags = await page.locator(".rt-req").evaluateAll((rows) => rows.map((r) => {
        const s = r.querySelector(".to .swap");
        return s ? [s.textContent, s.title] : ["", ""];
      }));
      assert.deepEqual(tags.map((x) => x[0]), w.tags);
      assert.match(tags[0][1], w.route);
      assert.match(tags[0][1], w.asked);
      assert.match(tags[1][1], w.route);
      assert.match(tags[1][1], w.none);
      // the tick no longer pins DeepSeek (#5442), so nothing points to it
      assert.doesNotMatch(tags[1][1], /tick|勾选/);
      assert.doesNotMatch(tags[0][1], w.swap);
      assert.match(tags[2][1], w.swap);
      const fits = await page.locator(".rt-req .to .swap").evaluateAll((es) => es.map((e) => {
        const row = e.closest(".rt-req").getBoundingClientRect(), b = e.getBoundingClientRect();
        return b.width > 0 && b.left >= row.left && b.right <= row.right + 0.5 && getComputedStyle(e).borderLeftWidth === "0px";
      }));
      assert.deepEqual(fits, [true, true, true], "each tag sits in its row, no left-border accent");

      // the story of each says the same
      for (const [i, re] of [[0, w.asked], [1, w.none], [2, w.swap]]) {
        await page.locator(".rt-req").nth(i).click();
        // the story is drawn again once the pick lands: wait for the line
        const said = await page.waitForFunction((src) => {
          const li = document.querySelector(".rt-steps li.swap");
          return li && new RegExp(src).test(li.textContent) && li.textContent;
        }, re.source).then((h) => h.jsonValue(), (e) => page.locator(".rt-steps li.swap").first().textContent().catch(() => String(e)));
        assert.match(said, re);
      }

      // the Usage page's Requests: the served model's title says the route
      await page.locator('[data-view="usage"]').first().click();
      await page.locator(":is(#usageTab .opt, .nav-s):visible").nth(1).click();
      const served = page.locator(".led tbody tr.led-row .swap").first();
      await served.waitFor();
      const title = await served.getAttribute("title");
      assert.match(title, w.route);
      assert.match(title, w.usage);
      for (const l of ["zh", "zh-TW", "ja", "de"]) {
        const missing = await page.evaluate((l) => ["ClinePass route {served}", "requested {sent} · ClinePass route {served}",
          "ClinePass answered {sent} through {served}, a route it picks itself: the name is the route's, not another model's.",
          "Its reply says {upstream} answered.", "Its reply didn't say which provider answered.",
          "{pin} was asked for, but its reply didn't say which provider answered, so whether ClinePass kept to it is unknown.",
          "No provider was asked for, and its reply didn't say which one answered."].filter((k) => !I18N[l][k]), l);
        assert.deepEqual(missing, [], `every string has its ${l}`);
      }
      assert.deepEqual(errors, []);
    });
  }
}
