// Run with Node's test runner and Playwright on the module path; see README.md.
// The story of a request on the Routing page puts the provider's logo
// before the account, key or provider a line names, so who it is about
// reads at a glance (the owner: 这里在前面显示对应的 provider 图标会不会更直观一点).
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const day = [now.getFullYear(), now.getMonth() + 1, now.getDate()].map((n) => String(n).padStart(2, "0")).join("-");
const at = (i) => new Date(now.getTime() - (i + 1) * 60e3).toISOString();
const glm = { id: "zhipu/a1", provider: "zhipu", name: "Zhipu", icon: "zhipu-color", who: "18610345303", kind: "account", agent: "claude", model: "glm-5.3-flash", routing: "order", shared: true, used: 20 };
const gpt = { id: "openai/k1", provider: "openai", name: "OpenAI", icon: "openai", who: "work", kind: "key", model: "glm-5.3-flash", routing: "order", fallback: true, used: 0 };
const routes = [{
  id: 100, seq: 100, time: at(0), agent: "claude", model: "glm-5.3-flash", provider: "zhipu",
  order: [glm, gpt],
  tries: [
    { id: glm.id, model: glm.model, start: at(0), done: true, status: 429, ms: 400, error: "rate limited" },
    { id: gpt.id, model: gpt.model, start: at(0), done: true, status: 200, ms: 22000 },
  ],
  done: true, status: 200, ms: 22400, tokens: 1200,
}];

function serve(lang) {
  const state = { agents: [{ id: "claude", name: "Claude Code", path: "/test/settings.json", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
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
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: the story's lines lead with the provider's logo`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const context = await browser.newContext({ viewport: { width: 1100, height: 760 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang));
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.locator(".rt-steps").screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-log-icons.png`) });
        }
        await browser.close();
      });
      await page.goto("http://magpie.test/?view=routing");
      await page.locator(".rt-days .rt-day").nth(1).click();
      await page.locator(".rt-req").first().click();
      await page.locator(".rt-detail-toggle").click();
      await page.locator(".rt-steps li.ok").waitFor();

      const lines = await page.locator(".rt-steps li").evaluateAll((lis) => lis.map((li) => {
        const n = li.querySelector(".rt-named");
        if (!n) return { cls: li.className, text: li.textContent };
        const ic = n.querySelector(".ic"), r = ic.getBoundingClientRect();
        // the logo, then the name, on the line's first row
        const name = document.createRange();
        name.selectNodeContents(n.lastChild);
        const nr = name.getBoundingClientRect();
        return { cls: li.className, text: li.textContent, logo: ic.dataset.icon, name: n.textContent, size: [r.width, r.height], before: r.right <= nr.left + 0.5, inline: Math.abs((r.top + r.bottom) / 2 - (nr.top + nr.bottom) / 2) <= 3 };
      }));
      const of = (cls) => lines.filter((l) => l.cls === cls);
      const why = of("why")[0], bad = of("bad")[0], ok = of("ok")[0];
      for (const [l, logo, name] of [[why, "zhipu-color", "18610345303"], [bad, "zhipu-color", "18610345303"], [ok, "openai", "work"]]) {
        assert.equal(l?.logo, logo, JSON.stringify(lines));
        assert.equal(l.name, name, JSON.stringify(l));
        assert.deepEqual(l.size, [14, 14], JSON.stringify(l));
        assert(l.before && l.inline, JSON.stringify(l));
      }
      // what the vendor said is word for word, with no logo in it
      assert(!lines.some((l) => /said/.test(l.cls) && l.logo), JSON.stringify(lines));
      assert.deepEqual(errors, []);
    });
  }
}
