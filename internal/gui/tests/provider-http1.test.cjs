// Run with Node's test runner and Playwright on the module path; see README.md.
// kepner_53726 (Discord): a provider the user added, at an https://
// address (a company gateway whose HTTP/2 connections break mid-reply with
// "tls: bad record MAC"), has an "HTTP version" row under Advanced with
// "HTTP/1.1 only". Advanced opens by itself when it is set, Save posts it,
// and Fetch models before a Save asks with it. One at an http:// address,
// a preset's and a signed-in account have no such row, and a preset's
// Save never sends it. Every language, wide and narrow.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const base = {
  icon: "generic", chat: "", responses: "", anthropic: "", catalog: "",
  models: [{ id: "qwen3-32b", name: "qwen3-32b", on: true }], agents: [], fallback: [], headers: {},
  key: { set: false, masked: "" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "", searches: false,
};
const vllm = { ...base, id: "vllm", name: "LAN vLLM", chat: "https://192.168.1.20:8443/v1", takesInsecureTLS: true, http1: false };
const kept = { ...base, id: "kept", name: "Kept Box", chat: "https://ollama.corp.example/v1", takesInsecureTLS: true, http1: true };
const plain = { ...base, id: "plain", name: "Plain Box", chat: "http://192.168.1.21:8000/v1", takesInsecureTLS: true };
const openai = { ...base, id: "openai", name: "OpenAI", preset: "openai", chat: "https://api.openai.example/v1", key: { set: true, masked: "sk-…one" }, takesInsecureTLS: false };
const codex = {
  id: "codex", name: "Codex", icon: "codex-color", chat: "", responses: "", anthropic: "", catalog: "",
  models: [{ id: "gpt-6", name: "GPT-6", on: true }], agents: [], fallback: [], headers: {}, keyList: [],
  account: { agent: "codex", agentName: "Codex", user: "me@example.com", plan: "PLUS", logins: [{ user: "me@example.com", plan: "PLUS", active: true, on: true }] },
  proxy: "", takesInsecureTLS: false,
};

function serve(lang, posts) {
  const providers = {
    providers: [vllm, kept, plain, openai, codex],
    presets: [{ id: "openai", name: "OpenAI", kind: "vendor", icon: "openai", chat: "https://api.openai.example/v1", added: true }],
    excluded: [], gateway: { running: true, window: true },
  };
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
      const action = url.pathname.slice("/api/provider/".length);
      posts.push({ action, body: route.request().postDataJSON() });
      if (action === "models") return json({ count: 1, provider: vllm });
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const strings = ["HTTP version", "HTTP/1.1 only"];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1100, 420]) {
      const open = async (t, name) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        let page;
        t.after(async () => {
          if (process.env.ARTIFACT_DIR && page) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-${name}-http1.png`), fullPage: true });
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
        const w = await page.evaluate(([lang, ks]) => Object.fromEntries(ks.map((k) => [k, (I18N[lang] || {})[k] || k])), [lang, [...strings, "Save", "Fetch models"]]);
        await page.locator(".row.provider", { hasText: name }).click();
        await page.locator(".editor .bar").waitFor();
        return { page, errors, posts, w };
      };
      const adv = (page) => page.locator(".editor details.advanced");
      const unfold = async (page) => {
        await adv(page).waitFor({ state: "attached" });
        if (!(await adv(page).evaluate((d) => d.open))) await adv(page).locator(":scope > summary").click();
      };
      const row = (page, w) => page.locator(".editor label.tick", { hasText: w["HTTP/1.1 only"] });
      const box = (page, w) => row(page, w).locator("input");
      const save = async (page, posts, w) => {
        const n = posts.length;
        await page.locator(".editor .bar").getByRole("button", { name: w.Save, exact: true }).click();
        for (let i = 0; i < 50 && posts.length === n; i++) await page.waitForTimeout(50);
        return posts.at(-1);
      };

      test(`${engine} ${lang} ${width}: a provider of the user's at https:// is held to HTTP/1.1 under Advanced, and saved so`, async (t) => {
        const { page, errors, posts, w } = await open(t, "LAN vLLM");
        if (lang !== "en") {
          const missing = await page.evaluate(([lang, ks]) => ks.filter((k) => !(I18N[lang] || {})[k]), [lang, strings]);
          assert.deepEqual(missing, [], `every string has its ${lang}`);
        }
        assert.equal(await adv(page).evaluate((d) => d.open), false, "Advanced stays shut while nothing in it is set");
        await unfold(page);
        assert.equal(await adv(page).locator("label.tick", { hasText: w["HTTP/1.1 only"] }).count(), 1, "the row is under Advanced");
        await box(page, w).scrollIntoViewIfNeeded();
        assert.equal(await row(page, w).isVisible(), true);
        assert.equal(await box(page, w).isChecked(), false, "opens as saved");
        const r = await row(page, w).boundingBox();
        assert.ok(r.width > 60 && r.x >= 0 && r.x + r.width <= width, `the row fits: ${JSON.stringify(r)}`);
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true, "no sideways scroll");
        await box(page, w).check();
        await page.locator(".editor .mfoot").getByRole("button", { name: w["Fetch models"], exact: true }).click();
        for (let i = 0; i < 50 && !posts.some((p) => p.action === "models"); i++) await page.waitForTimeout(50);
        const asked = posts.find((p) => p.action === "models");
        assert.ok(asked, "Fetch models asked");
        assert.equal(asked.body.http1, true, "Fetch models asks over HTTP/1.1");
        const saved = await save(page, posts, w);
        assert.equal(saved.action, "save");
        assert.equal(saved.body.id, "vllm");
        assert.equal(saved.body.http1, true);
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}: one saved with HTTP/1.1 opens Advanced ticked, and is saved unticked`, async (t) => {
        const { page, errors, posts, w } = await open(t, "Kept Box");
        await adv(page).waitFor({ state: "attached" });
        assert.equal(await adv(page).evaluate((d) => d.open), true, "Advanced opens when it holds a setting");
        assert.equal(await box(page, w).isChecked(), true);
        await box(page, w).scrollIntoViewIfNeeded();
        await box(page, w).uncheck();
        const saved = await save(page, posts, w);
        assert.equal(saved.body.id, "kept");
        assert.equal(saved.body.http1, false);
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}: http://, a preset's and a sign-in's have no row, and a preset's Save sends none`, async (t) => {
        {
          const { page, errors, w } = await open(t, "Plain Box");
          await unfold(page);
          assert.equal(await row(page, w).isVisible(), false);
          assert.deepEqual(errors, []);
        }
        {
          const { page, errors, posts, w } = await open(t, "OpenAI");
          assert.equal(await row(page, w).count(), 0);
          const saved = await save(page, posts, w);
          assert.equal(saved.action, "save");
          assert.equal("http1" in saved.body, false, `a preset's Save sends none: ${JSON.stringify(saved.body)}`);
          assert.deepEqual(errors, []);
        }
        const { page, errors, w } = await open(t, "Codex");
        assert.equal(await row(page, w).count(), 0);
        assert.deepEqual(errors, []);
      });
    }
  }
}
