// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings' Web search section: which provider searches for a model that
// can't (01huadalang on Discord). The row shows magpie's own pick while none
// is named; the picker offers Automatic, each provider that can search by
// its small model, and each of its models, including a relay said to search;
// a pick is saved as searcher ("<provider>" or "<provider>/<model>") and shown;
// one named that magpie can't use (turned off) is said in the row, magpie's pick shown instead;
// relays said to search are said not to be picked automatically; a Kimi Code plan, which
// searches by its web search with no model, is offered by itself and said to search for its
// own models first. Another setting saved
// keeps the pick (prefsKeep). No click moves the page. English and Chinese, Chromium and
// WebKit, with the API faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const choices = [
  { id: "claude", name: "Claude", icon: "claude", small: "claude-haiku-4-5", models: [
    { id: "claude/claude-haiku-4-5", name: "Claude Haiku 4.5", provider: "claude", providerName: "Claude" },
    { id: "claude/claude-opus-4-5", name: "Claude Opus 4.5", provider: "claude", providerName: "Claude" }] },
  { id: "openai", name: "OpenAI", icon: "openai", small: "gpt-5-mini", smallName: "GPT-5 Mini Named", models: [
    { id: "openai/gpt-5-mini", name: "GPT-5 mini", provider: "openai", providerName: "OpenAI" },
    { id: "openai/gpt-5.5", name: "GPT Five Five", provider: "openai", providerName: "OpenAI" }] },
  { id: "relay", name: "MyRelay", icon: "generic", small: "claude-haiku-4-5", models: [
    { id: "relay/claude-haiku-4-5", name: "Claude Haiku 4.5", provider: "relay", providerName: "MyRelay" }] },
  { id: "kimi", name: "Kimi Code", icon: "kimi", small: "", models: [], service: true },
];
const words = {
  en: { name: "Searches for other models", auto: "Automatic", small: "GPT-5 Mini Named, its small model", unused: "isn't used: it is turned off", relays: "Relays said to search (MyRelay) are never picked automatically: they would spend the relay's quota on other models' searches; if one refuses magpie's own request, magpie falls back",
    own: "A Kimi Code plan (Kimi Code) searches for its own models first, with its web search; for other models only when named here", web: "its web search" },
  zh: { name: "代搜供应商", auto: "自动", small: "GPT-5 Mini Named（它的小模型）", unused: "没有用 OpenAI · GPT-5 Mini Named：它已关闭", relays: "标为能搜索的中转站（MyRelay）不会被自动选择：它们会为别的模型的搜索花掉中转站的额度；如果它拒绝 magpie 自己发出的请求，magpie 会退回其他选择",
    own: "Kimi Code 套餐（Kimi Code）的模型先用套餐自带的联网搜索；别的模型只有在这里选了它才用", web: "它自带的联网搜索" },
};

function serve(lang, posted, st) {
  const settings = () => ({ lang, theme: "light", searchVendors: [], searchAPIs: [], searchChoices: choices,
    searchAuto: "Claude · claude-haiku-4-5", searchProvider: st.unused || !st.searcher ? "Claude · claude-haiku-4-5" : st.searcher,
    searchRelays: ["MyRelay"], searcher: st.searcher, searchUnused: st.unused });
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data, status = 200) => r.fulfill({ status, json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: settings() });
    if (url.pathname === "/api/settings") {
      if (r.request().method() === "POST") {
        const b = JSON.parse(r.request().postData());
        posted.push(b);
        st.searcher = b.searcher || "";
      }
      return json(settings());
    }
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    await (body ? r.fulfill({ body, contentType }) : r.fulfill({ status: 404, body: "" }));
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: the provider that searches for other models is picked in Settings`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 1000, height: 900 } })).newPage();
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const posted = [];
      const st = { searcher: "", unused: "" };
      await page.route("**/*", serve(lang, posted, st));
      await page.goto("http://magpie.test/?view=settings&tab=models");
      const row = page.locator("#searchList .row.searcher-row");
      await row.waitFor();
      await row.scrollIntoViewIfNeeded();
      assert.equal(await row.locator(".name").innerText(), w.name);
      assert.equal(await row.locator("button.searcher-pick").innerText(), `${w.auto} · Claude · claude-haiku-4-5`);
      assert((await row.locator(".sub").innerText()).includes(w.relays), "the relays said to search are named");
      assert((await row.locator(".sub").innerText()).includes(w.own), "the Kimi Code plan is said to search for its own models");
      // the Search APIs come after it
      assert.equal(await page.locator("#searchList .row").nth(1).evaluate((e) => e.classList.contains("search-add")), true);

      const where = () => page.evaluate(() => [scrollX, scrollY, document.scrollingElement.scrollTop, document.querySelector("main")?.scrollTop ?? 0]);
      const click = async (loc) => {
        const b = await loc.boundingBox();
        await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
      };
      const before = await where();

      // a model of a provider
      await click(row.locator("button.searcher-pick"));
      await page.locator("#pop").waitFor({ state: "visible" });
      const items = await page.locator("#list li:not(.group)").allInnerTexts();
      assert(items[0].includes(w.auto), "Automatic comes first");
      assert(items.some((x) => x.includes("GPT Five Five")) && items.some((x) => x.includes("Claude Opus 4.5")));
      assert(items.some((x) => x.includes("MyRelay")), "a relay said to search can be named");
      await click(page.locator("#list li:not(.group)", { hasText: "GPT Five Five" }));
      await page.waitForFunction(() => document.querySelector("#searchList button.searcher-pick")?.innerText.includes("GPT Five Five"));
      assert.equal(posted.at(-1).searcher, "openai/gpt-5.5");
      assert.equal(await row.locator("button.searcher-pick").innerText(), "GPT Five Five · OpenAI");

      // a provider, by its small model
      await click(row.locator("button.searcher-pick"));
      await page.locator("#pop").waitFor({ state: "visible" });
      await click(page.locator("#list li:not(.group)", { hasText: w.small }));
      await page.waitForFunction(() => document.querySelector("#searchList button.searcher-pick")?.innerText === "OpenAI · GPT-5 Mini Named");
      assert.equal(posted.at(-1).searcher, "openai");

      // a Kimi Code plan, by its web search: no model of it is offered
      await click(row.locator("button.searcher-pick"));
      await page.locator("#pop").waitFor({ state: "visible" });
      const kimi = (await page.locator("#list li:not(.group)").allInnerTexts()).filter((x) => x.includes("Kimi Code"));
      assert.equal(kimi.length, 1, "the plan is offered once, with no model");
      assert(kimi[0].includes(w.web));
      const web = page.locator("#list li:not(.group)", { hasText: w.web });
      await web.scrollIntoViewIfNeeded();
      await click(web);
      await page.waitForFunction((want) => document.querySelector("#searchList button.searcher-pick")?.innerText === want, `Kimi Code · ${w.web}`);
      assert.equal(posted.at(-1).searcher, "kimi");

      // back to a provider, by its small model
      await click(row.locator("button.searcher-pick"));
      await page.locator("#pop").waitFor({ state: "visible" });
      await click(page.locator("#list li:not(.group)", { hasText: w.small }));
      await page.waitForFunction(() => document.querySelector("#searchList button.searcher-pick")?.innerText === "OpenAI · GPT-5 Mini Named");

      // another setting saved keeps the pick
      const n = posted.length;
      await page.evaluate(() => savePrefs({ ...prefsKeep(prefs), noStats: true }));
      await page.waitForTimeout(200);
      assert.equal(posted.length, n + 1);
      assert.equal(posted.at(-1).searcher, "openai", "another setting saved sends the pick as it was");

      // the one named turned off: said, and magpie's pick shown
      st.unused = "off";
      await page.evaluate(() => renderSettings && fetch("/api/settings").then((r) => r.json()).then((s) => { prefs = s; state.settings = s; renderSettings(); }));
      await page.waitForFunction(() => document.querySelector("#searchList .searcher-unused"));
      assert((await row.locator(".searcher-unused").innerText()).includes(w.unused));
      assert.equal(await row.locator("button.searcher-pick").innerText(), `${w.auto} · Claude · claude-haiku-4-5`);
      st.unused = "";

      // back to Automatic
      await click(row.locator("button.searcher-pick"));
      await page.locator("#pop").waitFor({ state: "visible" });
      await click(page.locator("#list li:not(.group)", { hasText: w.auto }).first());
      await page.waitForFunction(() => !document.querySelector("#searchList .searcher-unused") &&
        document.querySelector("#searchList button.searcher-pick")?.innerText.includes("claude-haiku"));
      assert.equal(posted.at(-1).searcher, "");
      assert.deepEqual(await where(), before, "the clicks moved nothing");
      assert.deepEqual(errors, []);
    });
  }
}
