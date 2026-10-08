// Run with Node's test runner and Playwright on the module path; see README.md.
// A custom provider's Base URL opens on the API it was saved as
// (01huadalang on Discord: 我选 response 保存了然后再打开一会就变成 openai
// 兼容了). A provider with a chat URL beside its Responses one, saved as
// OpenAI Responses, opens on OpenAI Responses, its chat URL under More
// endpoints; one saved before the pick was kept opens on the first API with
// a URL, as it did. OpenAI Responses picked and saved with no URL of its
// own is said in the form, never dropped; with its URL typed, the Save says
// which API the Base URL is. In English and Chinese, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const U = "https://relay.example.com/v1";

function serve(lang, posts) {
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  const models = [{ id: "m", name: "m", on: true }];
  const base = { icon: "generic", anthropic: "", models, agents: [], fallback: [], headers: {}, key: { set: true, masked: "sk-…1234" }, keyList: [], ready: true };
  const providers = { providers: [
    { ...base, id: "picked", name: "Picked", chat: U, responses: U, baseAPI: "responses" },
    { ...base, id: "older", name: "Older", chat: U, responses: U },
    { ...base, id: "plain", name: "Plain", chat: U, responses: "" },
  ], presets: [], excluded: [], gateway: { running: true, window: true } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname.startsWith("/api/provider/")) {
      posts.push({ path: url.pathname, body: route.request().postDataJSON() });
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    await route.fulfill({ body: await fs.readFile(file), contentType: { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)] });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: the Base URL opens on the API it was saved as`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" })).newPage();
      t.after(() => browser.close());
      page.setDefaultTimeout(5000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=providers");
      const zh = lang === "zh";
      const L = {
        openai: zh ? "OpenAI 兼容" : "OpenAI compatible", responses: zh ? "OpenAI Responses" : "OpenAI Responses",
        more: zh ? "更多端点" : "More endpoints", save: zh ? "保存" : "Save",
        empty: zh ? "Base URL：请填写 OpenAI Responses 的地址，或改选你已有地址所属的协议" : "Base URL: type the OpenAI Responses URL, or pick the API the URL you have is for",
      };
      const ed = page.locator(".editor");
      const open = async (id) => {
        if (await ed.count()) { await page.keyboard.press("Escape"); await ed.waitFor({ state: "detached" }); }
        await page.locator(`#providers .row[data-id="${id}"]`).click();
        await ed.locator(".base-url").waitFor();
      };
      const on = () => ed.locator(".segs:has(.opt[data-api]) .opt.on").textContent();

      // saved as OpenAI Responses: opens there, chat's URL under More endpoints
      await open("picked");
      assert.equal(await on(), L.responses);
      assert.equal(await ed.locator(".base-url").inputValue(), U);
      const more = await ed.locator("details.more input[type=url]").evaluateAll((is) => is.map((i) => i.value));
      assert(more.includes(U), "the chat URL is under More endpoints: " + more);

      // saved before the pick was kept: the first API with a URL, as before
      await open("older");
      assert.equal(await on(), L.openai);

      // OpenAI Responses picked with no URL of its own: said, nothing sent
      await open("plain");
      assert.equal(await on(), L.openai);
      await ed.locator('.segs .opt[data-api="responses"]').click();
      assert.equal(await ed.locator(".base-url").inputValue(), "", "a saved chat URL stays chat's (#105)");
      await ed.locator(":scope > .bar").getByRole("button", { name: L.save, exact: true }).click();
      assert.equal(await ed.locator(".editor-error").textContent(), L.empty);
      assert.equal(posts.filter((p) => p.path === "/api/provider/save").length, 0, "nothing saved");

      // its URL typed: the Save says the Base URL is the Responses one
      await ed.locator(".base-url").fill(U);
      await ed.locator(":scope > .bar").getByRole("button", { name: L.save, exact: true }).click();
      for (let i = 0; i < 60 && !posts.some((p) => p.path === "/api/provider/save"); i++) await page.waitForTimeout(50);
      const save = posts.find((p) => p.path === "/api/provider/save");
      assert(save, "saved");
      assert.equal(save.body.baseAPI, "responses");
      assert.equal(save.body.responses, U);
      assert.equal(save.body.chat, U, "chat's URL kept (#105)");
      assert.deepEqual(errors, []);
    });
  }
}
