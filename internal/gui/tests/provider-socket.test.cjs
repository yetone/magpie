// Run with Node's test runner and Playwright on the module path; see README.md.
// #5781 (faithleysath, from matrix0487 on Discord): a provider with a
// Responses URL has a "WebSocket" row under Advanced, beside "HTTP
// version", whose tick sends its Responses turns over the Responses
// WebSocket. Ticked it saves auto; one set on from the CLI opens ticked
// and keeps on; unticked it saves off. One without a Responses URL and a
// signed-in account show none, and their Save sends none. Every
// language, wide and narrow.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const base = {
  icon: "generic", chat: "", responses: "", anthropic: "", catalog: "",
  models: [{ id: "gpt-6", name: "gpt-6", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "", searches: false,
};
const relay = { ...base, id: "relay", name: "Relay Hub", responses: "https://relay.example/v1", takesSocket: true, socket: "" };
const kept = { ...base, id: "kept", name: "Kept Hub", responses: "https://kept.example/v1", takesSocket: true, socket: "on" };
const chat = { ...base, id: "chat", name: "Chat Hub", chat: "https://chat.example/v1", takesSocket: false };
const codex = {
  id: "codex", name: "Codex", icon: "codex-color", chat: "", responses: "", anthropic: "", catalog: "",
  models: [{ id: "gpt-6", name: "GPT-6", on: true }], agents: [], fallback: [], headers: {}, keyList: [],
  account: { agent: "codex", agentName: "Codex", user: "me@example.com", plan: "PLUS", logins: [{ user: "me@example.com", plan: "PLUS", active: true, on: true }] },
  proxy: "", takesSocket: false,
};

function serve(lang, posts) {
  const providers = { providers: [relay, kept, chat, codex], presets: [], excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname.startsWith("/api/provider/") && route.request().method() === "POST") {
      posts.push({ action: url.pathname.slice("/api/provider/".length), body: route.request().postDataJSON() });
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const strings = ["WebSocket", "Send Responses turns over WebSocket"];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1100, 420]) {
      const open = async (t, name) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        let page;
        t.after(async () => {
          if (process.env.ARTIFACT_DIR && page) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-${name}-socket.png`), fullPage: true });
          }
          await browser.close();
        });
        page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const posts = [];
        await page.route("**/*", serve(lang, posts));
        await page.goto("http://magpie.test/?view=providers");
        const w = await page.evaluate(([lang, ks]) => Object.fromEntries(ks.map((k) => [k, (I18N[lang] || {})[k] || k])), [lang, [...strings, "Save"]]);
        await page.locator(".row.provider", { hasText: name }).click();
        await page.locator(".editor .bar").waitFor();
        return { page, errors, posts, w };
      };
      const adv = (page) => page.locator(".editor details.advanced");
      const unfold = async (page) => {
        await adv(page).waitFor({ state: "attached" });
        if (!(await adv(page).evaluate((d) => d.open))) await adv(page).locator(":scope > summary").click();
      };
      const row = (page, w) => page.locator(".editor label.tick", { hasText: w["Send Responses turns over WebSocket"] });
      const box = (page, w) => row(page, w).locator("input");
      const save = async (page, posts, w) => {
        const n = posts.length;
        await page.locator(".editor .bar").getByRole("button", { name: w.Save, exact: true }).click();
        for (let i = 0; i < 50 && posts.length === n; i++) await page.waitForTimeout(50);
        return posts.at(-1);
      };

      test(`${engine} ${lang} ${width}: a Responses provider's turns go over WebSocket from a tick under Advanced`, async (t) => {
        const { page, errors, posts, w } = await open(t, "Relay Hub");
        if (lang !== "en") {
          const missing = await page.evaluate(([lang, ks]) => ks.filter((k) => !(I18N[lang] || {})[k]), [lang, strings]);
          assert.deepEqual(missing, [], `every string has its ${lang}`);
        }
        assert.equal(await adv(page).evaluate((d) => d.open), false, "Advanced stays shut while nothing in it is set");
        await unfold(page);
        assert.equal(await adv(page).locator("label.tick", { hasText: w["Send Responses turns over WebSocket"] }).count(), 1, "the row is under Advanced");
        await box(page, w).scrollIntoViewIfNeeded();
        assert.equal(await row(page, w).isVisible(), true);
        assert.equal(await box(page, w).isChecked(), false, "opens as saved");
        const r = await row(page, w).boundingBox();
        assert.ok(r.width > 60 && r.x >= 0 && r.x + r.width <= width, `the row fits: ${JSON.stringify(r)}`);
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true, "no sideways scroll");
        await box(page, w).check();
        const saved = await save(page, posts, w);
        assert.equal(saved.action, "save");
        assert.equal(saved.body.id, "relay");
        assert.equal(saved.body.socket, "auto");
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}: one set on opens Advanced ticked, keeps on, and is saved off unticked`, async (t) => {
        const { page, errors, posts, w } = await open(t, "Kept Hub");
        await adv(page).waitFor({ state: "attached" });
        assert.equal(await adv(page).evaluate((d) => d.open), true, "Advanced opens when it holds a setting");
        assert.equal(await box(page, w).isChecked(), true);
        let saved = await save(page, posts, w);
        assert.equal(saved.body.socket, "on", "a Save leaves the CLI's on as it was");
        await page.locator(".editor .bar").waitFor({ state: "detached" });
        await page.locator(".row.provider", { hasText: "Kept Hub" }).click();
        await page.locator(".editor .bar").waitFor();
        await adv(page).waitFor({ state: "attached" });
        await box(page, w).scrollIntoViewIfNeeded();
        await box(page, w).uncheck();
        saved = await save(page, posts, w);
        assert.equal(saved.body.id, "kept");
        assert.equal(saved.body.socket, "");
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}: a provider without a Responses URL and a sign-in have no row, nor send one`, async (t) => {
        {
          const { page, errors, posts, w } = await open(t, "Chat Hub");
          await unfold(page);
          assert.equal(await row(page, w).isVisible(), false);
          const saved = await save(page, posts, w);
          assert.equal("socket" in saved.body, false, `sent: ${JSON.stringify(saved.body)}`);
          assert.deepEqual(errors, []);
        }
        const { page, errors, w } = await open(t, "Codex");
        assert.equal(await row(page, w).isVisible(), false);
        assert.deepEqual(errors, []);
      });
    }
  }
}
