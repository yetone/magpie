// Run with Node's test runner and Playwright on the module path; see README.md.
// quantum_lsfc (X): a provider the user added has a "Connect to" box under
// Advanced, one line per host: `host = place, place` (an IP, or
// dns:<name>), as curl --resolve. Advanced opens by itself when it is set,
// Save posts it as a map, Fetch models before a Save asks with it, and
// emptying the box posts an empty map. A preset's and a signed-in account's
// have no such row, and their Save never sends it. Every language, wide
// and narrow.
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
const vllm = { ...base, id: "vllm", name: "LAN vLLM", chat: "https://llm.corp.example/v1", takesResolve: true };
const kept = { ...base, id: "kept", name: "Kept Box", chat: "https://ollama.corp.example/v1", takesResolve: true, resolve: { "ollama.corp.example": ["dns:gw.managed.test", "10.0.0.1"] } };
const plain = vllm;
const openai = { ...base, id: "openai", name: "OpenAI", preset: "openai", chat: "https://api.openai.example/v1", key: { set: true, masked: "sk-…one" }, takesInsecureTLS: false };
const codex = {
  id: "codex", name: "Codex", icon: "codex-color", chat: "", responses: "", anthropic: "", catalog: "",
  models: [{ id: "gpt-6", name: "GPT-6", on: true }], agents: [], fallback: [], headers: {}, keyList: [],
  account: { agent: "codex", agentName: "Codex", user: "me@example.com", plan: "PLUS", logins: [{ user: "me@example.com", plan: "PLUS", active: true, on: true }] },
  proxy: "", takesInsecureTLS: false,
};

function serve(lang, posts) {
  const providers = {
    providers: [vllm, kept, openai, codex],
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

const strings = ["Connect to", "Where a host's connection goes, as curl --resolve: the name in the URL stays for TLS and Host. Tried in order; if none connects, it connects as usual. Not used through a proxy."];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1100, 420]) {
      const open = async (t, name) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        let page;
        t.after(async () => {
          if (process.env.ARTIFACT_DIR && page) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-${name}-resolve.png`), fullPage: true });
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
      const box = (page) => page.locator(".editor textarea.resolve-rules");
      const save = async (page, posts, w) => {
        const n = posts.length;
        await page.locator(".editor .bar").getByRole("button", { name: w.Save, exact: true }).click();
        for (let i = 0; i < 50 && posts.length === n; i++) await page.waitForTimeout(50);
        return posts.at(-1);
      };

      test(`${engine} ${lang} ${width}: a provider of the user's has a Connect to box under Advanced, saved as a map`, async (t) => {
        const { page, errors, posts, w } = await open(t, "LAN vLLM");
        if (lang !== "en") {
          const missing = await page.evaluate(([lang, ks]) => ks.filter((k) => !(I18N[lang] || {})[k]), [lang, strings]);
          assert.deepEqual(missing, [], `every string has its ${lang}`);
        }
        assert.equal(await adv(page).evaluate((d) => d.open), false, "Advanced stays shut while nothing in it is set");
        await unfold(page);
        await box(page).waitFor();
        assert.equal(await adv(page).locator("textarea.resolve-rules").count(), 1, "the row is under Advanced");
        await box(page).scrollIntoViewIfNeeded();
        assert.equal(await box(page).inputValue(), "", "opens as saved");
        const r = await box(page).boundingBox();
        assert.ok(r.width > 60 && r.x >= 0 && r.x + r.width <= width, `the row fits: ${JSON.stringify(r)}`);
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true, "no sideways scroll");
        await box(page).fill("LLM.corp.example = dns:gw.managed.test, 10.0.0.1\napi2.corp.example = 10.0.0.2:8443");
        await page.locator(".editor .mfoot").getByRole("button", { name: w["Fetch models"], exact: true }).click();
        for (let i = 0; i < 50 && !posts.some((p) => p.action === "models"); i++) await page.waitForTimeout(50);
        const asked = posts.find((p) => p.action === "models");
        assert.ok(asked, "Fetch models asked");
        const want = { "LLM.corp.example": ["dns:gw.managed.test", "10.0.0.1"], "api2.corp.example": ["10.0.0.2:8443"] };
        assert.deepEqual(asked.body.resolve, want, "Fetch models asks with it");
        const saved = await save(page, posts, w);
        assert.equal(saved.action, "save");
        assert.equal(saved.body.id, "vllm");
        assert.deepEqual(saved.body.resolve, want);
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}: one saved with addresses opens Advanced with them, keeps them, and clears them`, async (t) => {
        {
          const { page, errors, posts, w } = await open(t, "Kept Box");
          await adv(page).waitFor({ state: "attached" });
          assert.equal(await adv(page).evaluate((d) => d.open), true, "Advanced opens when it holds a setting");
          await box(page).waitFor();
          assert.equal(await box(page).inputValue(), "ollama.corp.example = dns:gw.managed.test, 10.0.0.1");
          const saved = await save(page, posts, w);
          assert.equal(saved.action, "save");
          assert.deepEqual(saved.body.resolve, kept.resolve, "an untouched Save sends what it had");
          assert.deepEqual(errors, []);
        }
        const { page, errors, posts, w } = await open(t, "Kept Box");
        await box(page).waitFor();
        await box(page).scrollIntoViewIfNeeded();
        await box(page).fill("");
        const saved = await save(page, posts, w);
        assert.deepEqual(saved.body.resolve, {}, "emptied, an empty map is sent");
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}: a preset's and a sign-in's have no row, and their Save sends none`, async (t) => {
        {
          const { page, errors, posts, w } = await open(t, "OpenAI");
          await unfold(page);
          assert.equal(await box(page).count(), 0);
          const saved = await save(page, posts, w);
          assert.equal(saved.action, "save");
          assert.equal("resolve" in saved.body, false, `a preset's Save sends none: ${JSON.stringify(saved.body)}`);
          assert.deepEqual(errors, []);
        }
        const { page, errors } = await open(t, "Codex");
        await page.waitForTimeout(200);
        assert.equal(await box(page).count(), 0);
        assert.deepEqual(errors, []);
      });
    }
  }
}
