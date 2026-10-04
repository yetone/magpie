// Run with Node's test runner and Playwright on the module path; see README.md.
// #717, #718: a Codex account that spends a reset about to run out by
// itself goes first, in Weekly pace and Smart, when that reset is spent
// before its week renews — and the Routing page's story says the reset
// set its deadline, not its week. An account whose week decides is told
// as before. In English and Chinese.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const ago = (minutes) => new Date(now.getTime() - minutes * 60e3).toISOString();
const ahead = (hours) => new Date(now.getTime() + hours * 36e5).toISOString();
const base = { provider: "codex", name: "Codex", kind: "account", agent: "codex", model: "gpt-6", known: true };
// A: 80% left, its week renewing in five days, a reset spent in an hour
const a = (routing, fields) => ({ ...base, id: "codex#a", who: "a@example.com", routing, used: 20, renews: [ahead(120), ahead(3)], ...fields });
const b = (routing) => ({ ...base, id: "codex#b", who: "b@example.com", routing, used: 50, renews: [ahead(48), ahead(3)], pace: 50 / 48, due: ahead(48) });
const route = (id, order) => ({
  id, seq: id, time: ago(1), agent: "codex", model: "gpt-6", provider: "codex", done: true, status: 200, ms: 900, tokens: 1200,
  order, tries: [{ id: order[0].id, model: "gpt-6", start: ago(1), done: true, status: 200, ms: 900 }],
});
const routes = [
  route(4, [a("pace", { pace: 80, due: ahead(1), dueBy: "reset", restarts: ahead(1) }), b("pace")]),
  route(3, [a("", { restarts: ahead(1) }), b("")]),
  route(2, [b("pace"), a("pace", { pace: 80 / 120, due: ahead(120) })]),
  route(1, [b(""), a("")]),
];

function serve(lang) {
  return async (request) => {
    const url = new URL(request.request().url());
    const json = (data) => request.fulfill({ json: data });
    if (url.pathname === "/boot.js") return request.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return request.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "codex", name: "Codex", path: "/test/config.toml", fields: [] }], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: now.toISOString(), seq: 4, totals: { requests: routes.length }, routes });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await request.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { pace: /until one of its Codex resets about to run out is used by itself/, smart: /one of its Codex resets about to run out is used by itself in/,
    paceWeek: /per hour until reset/, smartWeek: /its allowance renews soonest/ },
  zh: { pace: /距自动使用一次快过期的 Codex 重置的小时数最高/, smart: /会自动使用它一次快过期的 Codex 重置/,
    paceWeek: /除以距重置的小时数最高/, smartWeek: /它的额度最先重置/ },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: a reset spent before the week renews is told as the deadline`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await browser.newPage({ viewport: { width: 1100, height: 1200 }, reducedMotion: "reduce" });
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (error) => errors.push(error.message));
      await page.route("**/*", serve(lang));
      await page.goto("http://magpie.test/?view=routing");
      await page.locator(".rt-req").nth(routes.length - 1).waitFor();
      const w = words[lang];
      const why = () => page.locator(".rt-steps li.why").first().textContent();
      const open = async (n, who, want) => {
        await page.locator(".rt-req").nth(n).click();
        await page.waitForFunction(([who, src]) => {
          const li = document.querySelector(".rt-steps li.why");
          return li && li.textContent.includes(who) && new RegExp(src).test(li.textContent);
        }, [who, want.source]);
      };
      await open(0, "a@example.com", w.pace);
      assert.doesNotMatch(await why(), w.paceWeek);
      await open(1, "a@example.com", w.smart);
      assert.match(await why(), /b@example\.com/);
      // the week decides: as before
      await open(2, "b@example.com", w.paceWeek);
      assert.doesNotMatch(await why(), w.pace);
      await open(3, "b@example.com", w.smartWeek);
      assert.doesNotMatch(await why(), w.smart);
      assert.deepEqual(errors, []);
    });
  }
}
