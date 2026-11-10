// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings › Privacy's "Sent unmasked" (CrazyMouse_TT on X wanted masking
// off for their own model and found no way to): the providers set to Send
// requests unmasked, each a button that opens its editor, where the switch
// shows ticked under Advanced; with none, the row says where the switch
// is. In every language, Chromium and WebKit, wide and narrow.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { reach, watchSettingsReads, settingsDrawn } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const words = {
  en: { name: "Sent unmasked", tick: "Send requests unmasked" },
  zh: { name: "不脱敏的供应商", tick: "请求不脱敏" },
  "zh-TW": { name: "不脫敏的供應商", tick: "請求不脫敏" },
  ja: { name: "マスクしないプロバイダ", tick: "リクエストをマスクしない" },
  de: { name: "Unmaskiert gesendet", tick: "Anfragen unmaskiert senden" },
};
const box = {
  id: "box", name: "My Box", icon: "generic", chat: "http://192.168.1.20:8000/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "qwen3", name: "Qwen3", on: true }], agents: [], fallback: [], headers: {},
  key: { set: false, masked: "" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "", searches: false, unredacted: true,
};

function serve(lang, { unmasked = [{ id: "box", name: "My Box", provider: "box", providerName: "My Box", icon: "generic" }] } = {}) {
  const settings = { lang, theme: "light", redact: true, redactPersonal: false, redactWords: ["acmecorp"], titleModels: [], maskRouteModels: [], unmaskedProviders: unmasked };
  const state = { agents: [], profiles: [], settings };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (url.pathname === "/api/providers") return json({ providers: [box], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

async function open(engine, lang, width, opts) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  const page = await (await browser.newContext({ viewport: { width, height: 1400 }, reducedMotion: "reduce" })).newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, opts));
  await watchSettingsReads(page);
  await page.goto("http://magpie.test/?view=settings&tab=privacy");
  await page.locator("#redactList .row").first().waitFor();
  await settingsDrawn(page);
  return { browser, page, errors };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const [lang, w] of Object.entries(words)) {
    for (const width of [1100, 440]) {
      test(`${engine} ${lang} ${width}px: a provider sent unmasked is listed, and opens its editor with the switch on`, async (t) => {
        const { browser, page, errors } = await open(engine, lang, width);
        t.after(() => browser.close());
        const row = page.locator("#unmaskedRow");
        await reach(page, row);
        const m = await row.evaluate((r) => {
          const list = document.querySelector("#redactList").getBoundingClientRect(), b = r.getBoundingClientRect();
          return {
            name: r.querySelector(".name").textContent, sub: r.querySelector(".sub").textContent,
            buttons: [...r.querySelectorAll("button")].map((x) => x.textContent.trim()),
            inside: b.left >= list.left - 1 && b.right <= list.right + 1,
            wide: document.documentElement.scrollWidth > document.documentElement.clientWidth,
          };
        });
        assert.equal(m.name, w.name);
        assert.deepEqual(m.buttons, ["My Box"]);
        assert(m.inside && !m.wide, "inside the page");
        if (lang !== "en") assert(!m.sub.startsWith("Requests to these"), `its words translated: ${m.sub}`);
        await row.locator("button", { hasText: "My Box" }).click();
        const tick = page.locator(".editor label.tick", { hasText: w.tick }).locator("input");
        await tick.waitFor({ state: "attached" });
        assert.equal(await page.locator(".editor details.advanced").evaluate((d) => d.open), true, "Advanced opens, the switch on");
        assert.equal(await tick.isChecked(), true);
        assert.deepEqual(errors, []);
      });
    }
  }
  test(`${engine}: with none, the row says where the switch is`, async (t) => {
    const { browser, page, errors } = await open(engine, "en", 1100, { unmasked: [] });
    t.after(() => browser.close());
    const row = page.locator("#unmaskedRow");
    await reach(page, row);
    assert.equal(await row.locator("button").count(), 0);
    assert.match(await row.locator(".sub").textContent(), /Send requests unmasked.*Advanced/);
    assert.deepEqual(errors, []);
  });
}
