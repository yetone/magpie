// Run with Node's test runner and Playwright on the module path; see README.md.
// ZCode's Start Plan turning a request away (#425: 405 "request has been
// blocked due to unusual activity", code 3012): the Routing page gives
// what the gateway made of it, and apart from that what it means (it was
// sent as the ZCode app sends it, so the network blocked the address or
// ZCode checks something new, and what to use instead), in the page's
// language.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const day = [now.getFullYear(), now.getMonth() + 1, now.getDate()].map((n) => String(n).padStart(2, "0")).join("-");
const at = (i) => new Date(now.getTime() - (i + 1) * 60e3).toISOString();
const hint = "ZCode's Start Plan still turned this request away, though magpie sends it as the ZCode app does; it can be a network block of this IP, or ZCode checking for something new. Use an account with a GLM Coding Plan, or add another provider to this group";
const vendor = "ZCode: 405 Method Not Allowed";
const key = { id: "zcode", provider: "zcode", name: "ZCode", kind: "provider", model: "GLM-5.3-Flash" };
// newest first: the block, then another 405 with nothing to add
const errs = [vendor + " — " + hint, "ZCode: 405 Method Not Allowed", "", "", "", ""];
const routes = errs.map((error, i) => {
  const status = error ? 405 : 200;
  return {
    id: 100 - i, seq: 100 - i, time: at(i), agent: "claude", model: "zcode/GLM-5.3-Flash", provider: "zcode",
    order: [key], tries: [{ id: key.id, model: key.model, start: at(i), done: true, status, ms: 400, ...(error ? { error } : {}) }],
    done: true, status, ms: 400, ...(error ? { error } : {}),
  };
});

function serve(lang) {
  const state = { agents: [{ id: "claude", name: "Claude Code", path: "/test/settings.json", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
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

const want = {
  en: { said: "It said: " + vendor, hint },
  zh: { said: "原话：" + vendor, hint: "ZCode 体验套餐仍拒绝了这个请求（magpie 已按 ZCode 客户端的方式发送）；可能是网络拦截了这个 IP，或 ZCode 又加了新的校验。请改用有 GLM Coding Plan 的账号，或在这个分组里加上其他供应商" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: ZCode's Start Plan block says what it means`, async (t) => {
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
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-zcode-blocked.png`), fullPage: true });
        }
        await browser.close();
      });
      await page.goto("http://magpie.test/?view=routing");
      await page.locator(".rt-day").nth(1).click();
      await page.locator(".rt-req").nth(errs.length - 1).waitFor();

      const steps = async () => page.locator(".rt-steps li").evaluateAll((ls) => ls.map((l) => [l.className, l.textContent]));
      const row = page.locator(".rt-req").nth(0);
      const was = await row.evaluate((e) => e.getBoundingClientRect().top);
      await row.click();
      await page.waitForTimeout(400);
      assert(Math.abs((await row.evaluate((e) => e.getBoundingClientRect().top)) - was) <= 1, "picking the request moved the page");
      let got = await steps();
      // the vendor's words as they were, without magpie's hint in them
      assert.deepEqual(got.filter(([c]) => c === "aside said").map(([, s]) => s), [want[lang].said]);
      // and the hint on its own line, in the page's language
      assert.deepEqual(got.filter(([, s]) => s === want[lang].hint).map(([c]) => c), ["aside"]);

      // another 405 gets no hint
      await page.locator(".rt-req").nth(1).click();
      await page.waitForTimeout(300);
      got = await steps();
      assert.equal(got.filter(([c]) => c === "aside said").length, 1);
      assert(!got.some(([, s]) => s.includes("Start Plan") || s.includes("体验套餐")), JSON.stringify(got));
      assert.deepEqual(errors, []);
    });
  }
}
