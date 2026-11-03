// Run with Node's test runner and Playwright on the module path; see README.md.
// A provider's Reply headers (#4145, codex-cn: a relay's X-OneAPI-Request-ID
// lost on the way to the agent): the editor shows the saved list, a Save
// sends what is typed as a list, refuses a name magpie never passes on
// (Set-Cookie, Content-*, a name that isn't one) and says why, and an
// emptied box clears it ([]). It fits a narrow window. In English,
// Chinese, Traditional Chinese, Japanese and German, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const relay = {
  id: "relay", name: "Relay", preset: "", icon: "generic", chat: "https://relay.example.com/v1", responses: "", anthropic: "", catalog: "", baseAPI: "chat",
  models: [{ id: "gpt-5.1", name: "gpt-5.1", on: true }], agents: [], fallback: [], headers: {},
  replyHeaders: ["X-Trace-Id"],
  key: { set: true, masked: "sk-…one" }, keyList: [{ id: "k1", name: "", masked: "sk-…one", on: true, active: true }],
  balanceToken: { takes: false, set: false }, proxy: "",
};

function serve(lang, calls) {
  const providers = { providers: [relay], presets: [], excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname.startsWith("/api/provider/")) {
      calls.push([url.pathname.slice(14), route.request().postDataJSON()]);
      if (url.pathname === "/api/provider/save") return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const words = {
  en: { label: "Reply headers", save: "Save", never: "is never passed on", name: "isn't a header name" },
  zh: { label: "透传响应头", save: "保存", never: "永不透传", name: "不是有效的头名称" },
  "zh-TW": { label: "透傳回應標頭", save: "儲存", never: "一律不透傳", name: "不是有效的標頭名稱" },
  ja: { label: "レスポンスヘッダー", save: "保存", never: "決して渡しません", name: "ヘッダー名ではありません" },
  de: { label: "Antwort-Header", save: "Speichern", never: "wird nie weitergegeben", name: "ist kein Header-Name" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    const w = words[lang];
    test(`${engine} ${lang}: Reply headers are shown, refused, saved and cleared`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 900, height: 900 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const calls = [];
      await page.route("**/*", serve(lang, calls));
      await page.goto("http://magpie.test/?view=providers");
      const open = async () => {
        await page.locator(".row.provider", { hasText: "Relay" }).click();
        const ed = page.locator(".editor");
        await ed.locator("input.reply-headers").waitFor();
        return ed;
      };
      const save = async (ed) => {
        await ed.locator(".bar").getByRole("button", { name: w.save, exact: true }).click();
        for (let i = 0; i < 20 && !calls.some((c) => c[0] === "save"); i++) await page.waitForTimeout(50);
      };
      let ed = await open();
      const box = ed.locator("input.reply-headers");
      assert.equal(await page.locator(`label[for="${await box.getAttribute("id")}"]`).textContent(), w.label, "labelled");
      assert.equal(await box.inputValue(), "X-Trace-Id", "the saved list is shown");

      // refused before a Save, with why, the box focused
      for (const [text, want] of [["x-trace-id, set-cookie", w.never], ["Content-Encoding", w.never], ["Access-Control-Allow-Origin", w.never], ["x trace", w.name]]) {
        await box.fill(text);
        await save(ed);
        assert.deepEqual(calls.map((c) => c[0]), [], `${text}: nothing saved`);
        const said = await ed.locator(".editor-error").textContent();
        assert.ok(said.includes(want) && said.includes(w.label), `${text}: ${said}`);
        assert.ok(await box.evaluate((e) => e === document.activeElement), "the box is focused");
      }

      // the narrow window: the box stays inside the editor
      await page.setViewportSize({ width: 420, height: 900 });
      const fits = await page.evaluate(() => {
        const ed = document.querySelector(".editor").getBoundingClientRect();
        const r = document.querySelector("input.reply-headers").getBoundingClientRect();
        return r.left >= ed.left - 0.5 && r.right <= ed.right + 0.5;
      });
      assert.ok(fits, "fits a narrow window");
      assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth), "no sideways scroll");
      await page.setViewportSize({ width: 900, height: 900 });

      await box.fill(" x-oneapi-request-id ,X-Trace-Id,, ");
      await save(ed);
      const saved = calls.find((c) => c[0] === "save");
      assert.ok(saved, "saved");
      assert.deepEqual(saved[1].replyHeaders, ["x-oneapi-request-id", "X-Trace-Id"], "saved as a list");

      // emptied: a Save clears it
      calls.length = 0;
      await page.locator(".editor").waitFor({ state: "hidden" });
      ed = await open();
      await ed.locator("input.reply-headers").fill("");
      await save(ed);
      assert.deepEqual(calls.find((c) => c[0] === "save")?.[1].replyHeaders, [], "an empty box clears it");
      assert.deepEqual(errors, []);
    });
  }
}
