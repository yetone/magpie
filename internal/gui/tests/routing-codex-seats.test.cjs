// Codex accounts in one group on the Routing page (RayYanOnline on X): seats
// that share a name (a Team seat and a Pro Lite login of one email) were
// folded into one card in the accounts list, so several accounts read as
// one. An account is told apart by its plan as well as its name.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");


function routes(now) {
  const reset = new Date(now.getTime() + 4 * 3600e3).toISOString();
  const kiro = { provider: "codex", name: "Codex", kind: "account", icon: "codex-color", agent: "codex", plan: "team", model: "gpt-6.1-sol", known: true, used: 10, renews: [reset] };
  const named = { ...kiro, id: "codex", who: "jd@example.com" };
  const other = { ...kiro, id: "codex@jd@example.com", who: "jd@example.com", plan: "prolite", used: 20 };
  const key = { ...kiro, id: "codex@jd@example.com#2", who: "jd@example.com", plan: "plus", used: 30 };
  return Array.from({ length: 4 }, (_, i) => {
    const time = new Date(now.getTime() - (4 - i) * 1000).toISOString();
    const id = ["codex", other.id, key.id][i % 3];
    return { id: i + 1, seq: i + 1, time, agent: "claude", model: "kiro/claude-sonnet-4.5", provider: "kiro", order: [named, other, key], done: true,
      status: 200, ms: 20, tries: [{ id, model: "gpt-6.1-sol", start: time, done: true, status: 200, ms: 20 }] };
  }).reverse();
}

function serve(lang, now, names, users = () => ({})) {
  const assets = path.resolve(__dirname, "../assets");
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs={lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window={};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.has("wait")) return;
      return json({ mine: true, now: now.toISOString(), seq: 4, totals: { requests: 4, rerouted: 0, errors: 0 }, routes: routes(now) });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [{
      id: "codex", name: "Codex", icon: "codex-color", models: [{ id: "gpt-6.1-sol", on: true }], keyList: [],
      account: { agent: "codex", user: "jd@example.com · Team", logins: [] },
      accountNames: names(), accountUsers: users(),
    }], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file).catch(() => ""), contentType });
  };
}



for (const engine of process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"]) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: Routing keeps Codex seats of one name apart`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await browser.newPage({ viewport: { width: 1100, height: 1000 } });
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, new Date(), () => ({}), () => ({})));
      await page.goto("http://magpie.test/?view=routing");
      await page.locator(".rt-act").first().waitFor();
      assert.equal(await page.locator(".rt-act", { hasText: "jd@example.com" }).count(), 3, "three seats are three cards: " + (await page.locator(".rt-acts").innerText()));
      const text = (await page.locator(".rt-act", { hasText: "jd@example.com" }).allInnerTexts()).join(" | ").toLowerCase();
      for (const plan of ["team", "plus"]) assert.ok(text.includes(plan), plan + " tells its seat apart: " + text);
      assert.deepEqual(errors, []);
    });
  }
}
