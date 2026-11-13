// Run with Node's test runner and Playwright on the module path; see README.md.
// One ChatGPT account added twice, as Codex and as ChatGPT API, is two
// members of one name. The story's line that a ChatGPT API account's
// allowance isn't known led with the logo of the first member of that
// name, its Codex twin, so the Codex row that read its week and reset
// above read as "not known" below (tmr11235 on X). A line about one
// member now leads with that member's logo. English, Chinese (both),
// Japanese and German; 1100 and 420 wide; no backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const ago = (s) => new Date(now.getTime() - s * 1e3).toISOString();
const ahead = (h) => new Date(Date.now() + h * 3600e3).toISOString();
const acct = (p, name, icon, user, extra = {}) => ({ id: `${p}@${user}`, provider: p, name, icon, who: user, kind: "account", agent: p, model: "gpt-6.1-sol", routing: "", ...extra });

const codexA = acct("codex", "Codex", "codex-color", "ahi@x.test", { plan: "Pro Lite", known: true, used: 30, renews: [ahead(140)] });
const codexB = acct("codex", "Codex", "codex-color", "a@trel.test", { plan: "Pro Max", known: true, used: 50, renews: [ahead(70)] });
const apiA = acct("chatgpt-api", "ChatGPT API", "openai", "ahi@x.test");
const apiB = acct("chatgpt-api", "ChatGPT API", "openai", "a@trel.test");
const members = ["chatgpt-api/gpt-6.1-sol", "codex/gpt-6.1-sol"];
const route = {
  id: 1, seq: 1, time: ago(30), agent: "codex", model: "sol", provider: "codex", done: true, status: 200, ms: 900, tokens: 1200,
  group: { id: "sol", name: "sol", routing: "", members },
  order: [codexA, codexB, apiA, apiB],
  tries: [{ id: codexA.id, model: codexA.model, start: ago(30), done: true, status: 200, ms: 900 }],
};

function serve(lang) {
  return async (request) => {
    const url = new URL(request.request().url());
    const json = (data) => request.fulfill({ json: data });
    if (url.pathname === "/boot.js") return request.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return request.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "codex", name: "Codex", path: "/test/config.toml", fields: [] }], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) { await new Promise((r) => setTimeout(r, 20e3)); return json({}); }
      return json({ mine: true, now: now.toISOString(), seq: 1, totals: { requests: 1, rerouted: 0, errors: 0 }, routes: [route] });
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

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}: a line about a ChatGPT API account leads with its logo, not its Codex twin's`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await browser.newPage({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (error) => errors.push(error.message));
        await page.route("**/*", serve(lang));
        await page.goto("http://magpie.test/?view=routing");
        const say = (key, vars = {}) => page.evaluate(([lang, key, vars]) => {
          const s = (lang !== "en" && I18N[lang]?.[key]) || key;
          return s.replace(/\{(\w+)\}/g, (_, k) => vars[k] ?? `{${k}}`);
        }, [lang, key, vars]);
        const row = page.locator(".rt-req", { hasText: "sol" }).first();
        await row.waitFor();
        await row.dispatchEvent("click");
        await page.locator(".rt-steps li.ok").waitFor();
        const lines = await page.locator(".rt-steps li").evaluateAll((lis) => lis.map((li) => ({
          cls: li.className, text: li.textContent.trim(),
          logos: [...li.querySelectorAll(".rt-named")].map((n) => [n.querySelector(".ic")?.dataset.icon, n.textContent]),
        })));
        for (const user of ["ahi@x.test", "a@trel.test"]) {
          const text = await say("{who}: what it has left isn't known yet, so it goes after those known.", { who: user });
          const l = lines.find((x) => x.cls === "aside" && x.text === text);
          assert(l, JSON.stringify(lines));
          assert.deepEqual(l.logos, [["openai", user]], JSON.stringify(l));
        }
        // the first, a Codex account, keeps Codex's logo
        const why = lines.find((x) => x.cls === "why");
        assert.equal(why?.logos[0]?.[0], "codex-color", JSON.stringify(lines));
        const spills = await page.locator(".rt-steps li").evaluateAll((els) => els.filter((e) => e.getBoundingClientRect().right > document.documentElement.clientWidth + 0.5).map((e) => e.textContent.slice(0, 40)));
        assert.deepEqual(spills, []);
        assert.deepEqual(errors, []);
      });
    }
  }
}
