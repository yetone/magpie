// Run with Node's test runner and Playwright on the module path; see README.md.
// #1851: a coding plan that serves only the agents it names knows them by
// their User-Agent, and a gateway of the user's own behind magpie wants to
// see the agent's (nb5p on Discord). The provider editor's User-Agent row,
// "Send the agent's own User-Agent", is there for a provider with a key,
// opens ticked unless it was saved as magpie, and Save posts it. A signed-in account and a vendor magpie goes as
// the client of (OpenCode here) have no such row. In every language, wide
// and narrow.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
// the editor folds what few need under Advanced, this field among them:
// opened as a reader would, unless something set in it opened it already
const unfold = async (page) => {
  const adv = page.locator(".editor details.advanced");
  await adv.waitFor({ state: "attached" });
  if (!(await adv.evaluate((d) => d.open))) await adv.locator(":scope > summary").click();
};
const base = {
  icon: "generic", chat: "", responses: "", anthropic: "", catalog: "",
  models: [{ id: "m1", name: "M1", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…1234" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "", searches: false, unredacted: false, passUserAgent: true,
};
const plan = { ...base, id: "plan", name: "Agent Plan", chat: "https://plan.example.com/v1" };
const asMagpie = { ...base, id: "asmagpie", name: "Magpie Plan", chat: "https://asmagpie.example.com/v1", passUserAgent: false };
const zen = { ...base, id: "opencode", name: "OpenCode Zen", chat: "https://opencode.ai/zen/v1", ownClient: true };
const codex = {
  id: "codex", name: "Codex", icon: "codex-color", chat: "", responses: "", anthropic: "", catalog: "",
  models: [{ id: "gpt-6", name: "GPT-6", on: true }], agents: [], fallback: [], headers: {}, keyList: [],
  account: { agent: "codex", agentName: "Codex", user: "me@example.com", plan: "PLUS", logins: [{ user: "me@example.com", plan: "PLUS", active: true, on: true }] },
  proxy: "",
};

function serve(lang, posts) {
  const providers = { providers: [plan, asMagpie, zen, codex], presets: [], excluded: [], gateway: { running: true, window: true } };
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
  "User-Agent", "Send the agent's own User-Agent",
  "Off, every request goes as magpie. A User-Agent set in Headers above always wins.",
];
const words = {
  en: { tick: "Send the agent's own User-Agent", save: "Save" },
  zh: { tick: "发送 Agent 自己的 User-Agent", save: "保存" },
  "zh-TW": { tick: "傳送 Agent 自己的 User-Agent", save: "儲存" },
  ja: { tick: "エージェント自身の User-Agent を送る", save: "保存" },
  de: { tick: "Den eigenen User-Agent des Agenten senden", save: "Speichern" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    for (const width of [900, 440]) {
      const w = words[lang];
      const open = async (t, name) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-${name}-user-agent.png`), fullPage: true });
          }
          await browser.close();
        });
        const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const posts = [];
        await page.route("**/*", serve(lang, posts));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator(".row.provider", { hasText: name }).click();
        await unfold(page);
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

      test(`${engine} ${lang} ${width}: one saved as magpie opens unticked, and is set to send the agent's User-Agent`, async (t) => {
        const { page, errors, posts } = await open(t, "Magpie Plan");
        assert.equal(await row(page).isVisible(), true);
        assert.equal(await box(page).isChecked(), false, "opens as magpie");
        const r = await row(page).boundingBox();
        assert.ok(r.width > 60 && r.x + r.width <= width, `the row fits: ${JSON.stringify(r)}`);
        await box(page).scrollIntoViewIfNeeded();
        const y = await page.evaluate(() => document.querySelector(".editor")?.closest("[class*=scroll], main")?.scrollTop ?? window.scrollY);
        await box(page).check();
        const y2 = await page.evaluate(() => document.querySelector(".editor")?.closest("[class*=scroll], main")?.scrollTop ?? window.scrollY);
        assert.equal(y2, y, "the click doesn't scroll");
        const saved = await save(page, posts);
        assert.equal(saved.action, "save");
        assert.equal(saved.body.id, "asmagpie");
        assert.equal(saved.body.passUserAgent, true);
        if (lang !== "en") {
          const missing = await page.evaluate(([l, ks]) => ks.filter((k) => !I18N[l][k]), [lang, strings]);
          assert.deepEqual(missing, [], `every string has its ${lang}`);
        }
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}: a plan with a key opens ticked by default, and unticked is saved as magpie`, async (t) => {
        const { page, errors, posts } = await open(t, "Agent Plan");
        assert.equal(await box(page).isChecked(), true, "sends the agent's by default");
        await box(page).scrollIntoViewIfNeeded();
        await box(page).uncheck();
        const saved = await save(page, posts);
        assert.equal(saved.body.id, "plan");
        assert.equal(saved.body.passUserAgent, false);
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}: a vendor magpie goes as the client of, and a signed-in account, have no such row`, async (t) => {
        for (const name of ["OpenCode Zen", "Codex"]) {
          const { page, errors } = await open(t, name);
          assert.equal(await row(page).count(), 0, name);
          assert.deepEqual(errors, []);
        }
      });
    }
  }
}
