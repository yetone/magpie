// Run with Node's test runner and Playwright on the module path; see README.md.
// An agent's advice after a change (state.notice) reads in the window's
// language (#1508, NovaTrailX): a Chinese window said "ZCode 已接入
// magpie. ZCode reads its providers at start-up — …" in English. Every
// template in NOTICES, its slots filled, a notice of several joined, and
// one from a plugin (no template: as given), in every language, in Chromium
// and WebKit, at a wide and a narrow window. No backend: the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const ZCODE = "ZCode reads its providers at start-up — restart ZCode to see magpie's models in its picker.";
const models = [{ value: "magpie/deepseek/pro", label: "magpie/deepseek/pro", ref: "deepseek/pro" }];
const agent = (id, name) => ({
  id, name, path: "/test/" + id, wired: true,
  fields: [{ key: "model", label: "model", value: "magpie/deepseek/pro", options: models }],
});
const zcode = (on) => ({
  id: "zcode", name: "ZCode", icon: "zcode", path: "/test/zcode", wired: on,
  fields: [{ key: "model", label: "model", value: on ? "magpie/deepseek/pro" : "", options: models }],
});

function server(lang) {
  let on = false;
  const st = () => ({ agents: [agent("claude", "Claude Code"), agent("codex", "Codex"), zcode(on)], profiles: [], settings: { lang, theme: "light" } });
  return async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: st() });
    if (url.pathname === "/api/agents/connect/zcode") {
      on = true;
      return route.fulfill({ json: { ...st(), connected: { how: "magpie" }, notice: ZCODE } });
    }
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": an agent's advice after a change reads in the window's language", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      for (const width of [1100, 420]) {
        await t.test(`${lang} at ${width}px`, async () => {
          const page = await (await browser.newContext({ viewport: { width, height: 900 } })).newPage();
          page.setDefaultTimeout(5000);
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", server(lang));
          await page.goto("http://magpie.test/");
          await page.waitForFunction(() => typeof tNotice === "function" && typeof NOTICES === "object");

          // every template, its slots filled, reads as its translation, alone
          // and joined to the one before it
          const bad = await page.evaluate((zc) => {
            const fill = (en) => {
              const vars = {};
              for (const [, k] of en.matchAll(/\{(\w+)\}/g)) vars[k] = { agent: "Kimi Code", distro: "Ubuntu-24.04", net: "nat", url: "http://172.29.0.1:3425", command: "codex app-server daemon restart", version: "0.30.1", project: "/Users/me/my app", model: "openai/gpt-5", key: "magpie-cursor-local" }[k] ?? k;
              return [en.replace(/\{(\w+)\}/g, (_, k) => vars[k]), t(en, vars)];
            };
            const sep = /^(zh|ja)/.test(locale) ? "" : " ";
            const out = [];
            NOTICES.forEach((en, i) => {
              const [given, want] = fill(en);
              if (tNotice(given) !== want) out.push({ given, got: tNotice(given), want });
              if (locale !== "en" && want === given) out.push({ untranslated: en });
              // one that ends in a command to run (agy's) has no full stop
              // and is said last, so it is the second here
              let [a, wa] = [given, want], [b, wb] = fill(NOTICES[(i + 1) % NOTICES.length]);
              if (en.endsWith("}")) [a, wa, b, wb] = [b, wb, a, wa];
              if (tNotice(a + " " + b) !== wa + sep + wb) out.push({ joined: a + " " + b, got: tNotice(a + " " + b) });
            });
            // a plugin's own advice has no template: it reads as given, and so
            // does a notice joined to it, rather than half in each language
            const own = "Restart Foo to see this.";
            if (tNotice(own) !== own || tNotice(zc + " " + own) !== zc + " " + own) out.push({ own, got: tNotice(zc + " " + own) });
            return out;
          }, ZCODE).catch((e) => [String(e)]);
          assert.deepEqual(bad, [], lang);

          // and as the reporter saw it: connecting ZCode
          await page.locator(".agent-more").click();
          const sw = page.locator(`.row.agent[data-id="zcode"] .ag-conn`);
          await sw.waitFor({ state: "visible" });
          await sw.click();
          // connected: the row is drawn anew, and may sit elsewhere in the
          // list by then, so the switch's state is read, not its visibility
          await page.waitForFunction(() => document.querySelector('.row.agent[data-id="zcode"] .ag-conn')?.getAttribute("aria-checked") === "true");
          await page.waitForFunction(() => document.querySelector("#status")?.textContent.trim());
          const said = (await page.locator("#status").textContent()).trim();
          const want = await page.evaluate((z) => t(z), ZCODE);
          assert.ok(said.endsWith(want), said);
          assert.ok(said.startsWith(await page.evaluate(() => t("{agent} is connected to magpie", { agent: "ZCode" }))), said);
          if (lang === "zh") assert.ok(said.endsWith("ZCode 启动时读取供应商，需重启 ZCode 才能在它的模型选择器中看到 magpie 的模型。"), said);
          if (lang !== "en") assert.ok(!said.includes("reads its providers"), said);
          const fits = await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth);
          assert.ok(fits, "the status runs past the window");
        });
      }
    }
    assert.deepEqual(errors, []);
  });
}
