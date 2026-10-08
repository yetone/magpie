// Run with Node's test runner and Playwright on the module path; see README.md.
// A model on this computer or the local network (lc on Discord): the
// provider editor's Redaction row, "Send requests unmasked", is there while
// every address of the provider is local (localhost, a private IP), opens as
// it was saved, and Save posts it. A relay at a vendor's address has no such
// row, and one whose address is changed to a vendor's is saved masked. A
// signed-in account has none. In English and Chinese, wide and narrow.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const base = {
  icon: "generic", chat: "", responses: "", anthropic: "", catalog: "",
  models: [{ id: "qwen3", name: "Qwen3", on: true }], agents: [], fallback: [], headers: {},
  key: { set: false, masked: "" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "", searches: false, unredacted: false,
};
const local = { ...base, id: "local", name: "Local Box", chat: "http://localhost:11434/v1" };
const lan = { ...base, id: "lan", name: "LAN Box", chat: "http://192.168.1.20:8000/v1", unredacted: true };
const relay = { ...base, id: "relay", name: "Relay", chat: "https://relay.example.com/v1" };
const codex = {
  id: "codex", name: "Codex", icon: "codex-color", chat: "", responses: "", anthropic: "", catalog: "",
  models: [{ id: "gpt-6", name: "GPT-6", on: true }], agents: [], fallback: [], headers: {}, keyList: [],
  account: { agent: "codex", agentName: "Codex", user: "me@example.com", plan: "PLUS", logins: [{ user: "me@example.com", plan: "PLUS", active: true, on: true }] },
  proxy: "",
};

function serve(lang, posts) {
  const providers = { providers: [local, lan, relay, codex], presets: [], excluded: [], gateway: { running: true, window: true } };
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

const words = {
  en: { tick: "Send requests unmasked", save: "Save" },
  zh: { tick: "请求不脱敏", save: "保存" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    for (const width of [900, 440]) {
      const w = words[lang];
      const open = async (t, name) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-${name}-unmasked.png`), fullPage: true });
          }
          await browser.close();
        });
        const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const posts = [];
        await page.route("**/*", serve(lang, posts));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator(".row.provider", { hasText: name }).click();
        await page.locator(".editor .bar").waitFor();
        return { page, errors, posts };
      };
      const row = (page) => page.locator(".editor label.tick", { hasText: w.tick });
      const box = (page) => row(page).locator("input");
      const save = async (page, posts) => {
        const n = posts.length;
        await page.locator(".editor .bar").getByRole("button", { name: w.save, exact: true }).click();
        for (let i = 0; i < 50 && posts.length === n; i++) await page.waitForTimeout(50);
        return posts.at(-1);
      };

      test(`${engine} ${lang} ${width}: a model on localhost is set to go unmasked, and saved so`, async (t) => {
        const { page, errors, posts } = await open(t, "Local Box");
        assert.equal(await row(page).isVisible(), true);
        assert.equal(await box(page).isChecked(), false, "opens masked");
        const r = await row(page).boundingBox();
        assert.ok(r.width > 60 && r.x + r.width <= width, `the row fits: ${JSON.stringify(r)}`);
        await box(page).scrollIntoViewIfNeeded();
        await box(page).check();
        const saved = await save(page, posts);
        assert.equal(saved.action, "save");
        assert.equal(saved.body.id, "local");
        assert.equal(saved.body.unredacted, true);
        const missing = await page.evaluate(() => [
          "Redaction", "Send requests unmasked",
          "For a model running on this computer or your local network: secrets, personal data and your masked words go to it as written. Leave it off for a local relay or proxy that passes requests on to a vendor.",
        ].filter((k) => !I18N.zh[k]));
        assert.deepEqual(missing, [], "every string has its Chinese");
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}: one on the LAN saved unmasked opens ticked, and is saved masked`, async (t) => {
        const { page, errors, posts } = await open(t, "LAN Box");
        assert.equal(await box(page).isChecked(), true);
        await box(page).scrollIntoViewIfNeeded();
        await box(page).uncheck();
        const saved = await save(page, posts);
        assert.equal(saved.body.id, "lan");
        assert.equal(saved.body.unredacted, false);
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}: a relay has no row, and a local one moved to a vendor's address is saved masked`, async (t) => {
        {
          const { page, errors } = await open(t, "Relay");
          assert.equal(await row(page).isVisible(), false);
          assert.deepEqual(errors, []);
        }
        const { page, errors, posts } = await open(t, "Local Box");
        await box(page).scrollIntoViewIfNeeded();
        await box(page).check();
        await page.locator(".editor input[type=url]").first().fill("https://api.vendor.example/v1");
        assert.equal(await row(page).isVisible(), false, "the row goes with the local address");
        const saved = await save(page, posts);
        assert.equal(saved.body.unredacted, false);
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}: a signed-in account has no such row`, async (t) => {
        const { page, errors } = await open(t, "Codex");
        assert.equal(await row(page).count(), 0);
        assert.deepEqual(errors, []);
      });
    }
  }
}
