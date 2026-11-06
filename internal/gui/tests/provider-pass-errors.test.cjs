// Run with Node's test runner and Playwright on the module path; see README.md.
// ne9ko9 (Discord): a user who wants a provider's errors as the vendor sent
// them, asked once, says so under Advanced: "Errors", magpie handles by
// default, Pass through picked from the app's menu (never a native
// select). Picked and saved, it posts passErrors: true; one saved so opens
// with Advanced open and Pass through shown, and magpie handles posts
// false. A provider of a key, a preset's too, has it; a signed-in account
// has none and its Save sends none. Every language, wide and narrow.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const base = {
  icon: "generic", chat: "", responses: "", anthropic: "", catalog: "",
  models: [{ id: "glm-5", name: "glm-5", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…agg" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "", searches: false,
};
const agg = { ...base, id: "agg", name: "Local Aggregator", chat: "http://127.0.0.1:8317/v1", passErrors: false, takesPassErrors: true };
const kept = { ...base, id: "kept", name: "Kept Relay", chat: "http://127.0.0.1:8318/v1", passErrors: true, takesPassErrors: true };
const openai = { ...base, id: "openai", name: "OpenAI", preset: "openai", chat: "https://api.openai.example/v1", passErrors: false, takesPassErrors: true };
const codex = {
  id: "codex", name: "Codex", icon: "codex-color", chat: "", responses: "", anthropic: "", catalog: "",
  models: [{ id: "gpt-6", name: "GPT-6", on: true }], agents: [], fallback: [], headers: {}, keyList: [],
  account: { agent: "codex", agentName: "Codex", user: "me@example.com", plan: "PLUS", logins: [{ user: "me@example.com", plan: "PLUS", active: true, on: true }] },
  proxy: "",
};

function serve(lang, posts) {
  const providers = {
    providers: [agg, kept, openai, codex],
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
      posts.push({ action: url.pathname.slice("/api/provider/".length), body: route.request().postDataJSON() });
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const strings = [
  "Errors", "magpie handles", "Pass through",
];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1100, 420]) {
      const open = async (t, name) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        let page;
        t.after(async () => {
          if (process.env.ARTIFACT_DIR && page) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-${name}-limit-rest.png`), fullPage: true });
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
      const pick = (page) => page.locator(".editor button.pass-errors");
      const choose = async (page, w, name) => {
        await pick(page).scrollIntoViewIfNeeded();
        await pick(page).click();
        const item = page.locator(".proto-menu .pm-item", { hasText: w[name] });
        assert.equal(await page.locator(".proto-menu .pm-item").count(), 2);
        const r = await page.locator(".proto-menu").boundingBox();
        assert.ok(r.x >= 0 && r.x + r.width <= width + 1, `the menu fits: ${JSON.stringify(r)}`);
        await item.click();
        await page.locator(".proto-menu").waitFor({ state: "detached" });
      };
      const save = async (page, posts, w) => {
        const n = posts.length;
        await page.locator(".editor .bar").getByRole("button", { name: w.Save, exact: true }).click();
        for (let i = 0; i < 50 && posts.length === n; i++) await page.waitForTimeout(50);
        return posts.at(-1);
      };

      test(`${engine} ${lang} ${width}: a provider of a key has magpie handle its errors by default, and is saved to pass them through`, async (t) => {
        const { page, errors, posts, w } = await open(t, "Local Aggregator");
        if (lang !== "en") {
          const missing = await page.evaluate(([lang, ks]) => ks.filter((k) => !(I18N[lang] || {})[k]), [lang, strings]);
          assert.deepEqual(missing, [], `every string has its ${lang}`);
        }
        assert.equal(await adv(page).evaluate((d) => d.open), false, "Advanced stays folded while it is as by default");
        await unfold(page);
        assert.equal(await pick(page).innerText(), w["magpie handles"]);
        assert.equal(await page.locator(".editor select").count(), 0, "no native select");
        await pick(page).scrollIntoViewIfNeeded();
        const r = await pick(page).boundingBox();
        assert.ok(r.width > 40 && r.x >= 0 && r.x + r.width <= width, `the control fits: ${JSON.stringify(r)}`);
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true, "no sideways scroll");
        await choose(page, w, "Pass through");
        assert.equal(await pick(page).innerText(), w["Pass through"]);
        const saved = await save(page, posts, w);
        assert.equal(saved.action, "save");
        assert.equal(saved.body.id, "agg");
        assert.equal(saved.body.passErrors, true);
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}: one saved to pass them opens so with Advanced open, and magpie handles is saved`, async (t) => {
        const { page, errors, posts, w } = await open(t, "Kept Relay");
        assert.equal(await adv(page).evaluate((d) => d.open), true, "Advanced opens with it set");
        assert.equal(await pick(page).innerText(), w["Pass through"]);
        await choose(page, w, "magpie handles");
        const saved = await save(page, posts, w);
        assert.equal(saved.body.id, "kept");
        assert.equal(saved.body.passErrors, false);
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}: a preset of a key has it, a sign-in's has none and sends none`, async (t) => {
        {
          const { page, errors, posts, w } = await open(t, "OpenAI");
          await unfold(page);
          assert.equal(await pick(page).count(), 1);
          const saved = await save(page, posts, w);
          assert.equal(saved.action, "save");
          assert.equal(saved.body.passErrors, false, `a preset's Save keeps it as it was: ${JSON.stringify(saved.body)}`);
          assert.deepEqual(errors, []);
        }
        const { page, errors, w } = await open(t, "Codex");
        await unfold(page);
        assert.equal(await pick(page).count(), 0);
        assert.deepEqual(errors, []);
      });
    }
  }
}
