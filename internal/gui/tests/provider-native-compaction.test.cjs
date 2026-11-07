// Run with Node's test runner and Playwright on the module path; see README.md.
// #1099: a provider whose wire carries Codex's own compaction (a Responses
// API, a ChatGPT account, GitHub Copilot) can be set to be asked it as
// Codex sent it. The editor's Compaction row, "Codex compacts on this
// provider", is there for such a provider alone, opens as it was saved, and
// Save posts it. A Chat Completions relay and a Claude account have no such
// row. In every language, wide and narrow.
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
const relay = { ...base, id: "relay", name: "Responses Relay", responses: "https://relay.example.com/v1", carriesCompaction: true };
const chat = { ...base, id: "chatonly", name: "Chat Relay", chat: "https://chat.example.com/v1" };
const account = (id, name, agent, extra) => ({
  id, name, icon: agent + "-color", chat: "", responses: "", anthropic: "", catalog: "",
  models: [{ id: "m", name: "M", on: true }], agents: [], fallback: [], headers: {}, keyList: [],
  account: { agent, agentName: name, user: "me@example.com", plan: "PLUS", logins: [{ user: "me@example.com", plan: "PLUS", active: true, on: true }] },
  proxy: "", ...extra,
});
const codex = account("codex", "Codex", "codex", { responses: "https://chatgpt.com/backend-api/codex", carriesCompaction: true, nativeCompaction: true });
const claude = account("claude", "Claude Code", "claude", { anthropic: "https://api.anthropic.com" });

function serve(lang, posts) {
  const providers = { providers: [relay, chat, codex, claude], presets: [], excluded: [], gateway: { running: true, window: true } };
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
  "Compaction", "Codex compacts on this provider",
  "Codex's compaction is asked of this provider as Codex sent it, and the summary it seals goes back to Codex unchanged. Off, magpie writes the summary. A conversation compacted this way continues only on this provider and account: another is refused with an error, never sent without the context.",
];
const words = {
  en: { tick: "Codex compacts on this provider", save: "Save" },
  zh: { tick: "Codex 在此服务商上原生压缩", save: "保存" },
  "zh-TW": { tick: "Codex 在此服務商上原生壓縮", save: "儲存" },
  ja: { tick: "Codex のコンパクションをこのプロバイダーで行う", save: "保存" },
  de: { tick: "Codex kompaktiert bei diesem Anbieter", save: "Speichern" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    for (const width of [1100, 420]) {
      const w = words[lang];
      const open = async (t, name) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-${name}-compaction.png`), fullPage: true });
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

      test(`${engine} ${lang} ${width}: a Responses relay is set to have Codex compact on it, and saved so`, async (t) => {
        const { page, errors, posts } = await open(t, "Responses Relay");
        assert.equal(await row(page).isVisible(), true);
        assert.equal(await box(page).isChecked(), false, "opens off");
        await box(page).scrollIntoViewIfNeeded();
        const r = await row(page).boundingBox();
        assert.ok(r.width > 60 && r.x + r.width <= width, `the row fits: ${JSON.stringify(r)}`);
        const scroller = () => page.evaluate(() => document.querySelector(".editor")?.closest("[class*=scroll], main")?.scrollTop ?? window.scrollY);
        const y = await scroller();
        await box(page).check();
        assert.equal(await scroller(), y, "the click doesn't scroll");
        assert.equal(await page.locator(".editor select").count(), 0, "no native select");
        const saved = await save(page, posts);
        assert.equal(saved.action, "save");
        assert.equal(saved.body.id, "relay");
        assert.equal(saved.body.nativeCompaction, true);
        if (lang !== "en") {
          const missing = await page.evaluate(([l, ks]) => ks.filter((k) => !I18N[l][k]), [lang, strings]);
          assert.deepEqual(missing, [], `every string has its ${lang}`);
        }
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}: a ChatGPT account saved compacting natively opens ticked, and is saved off`, async (t) => {
        const { page, errors, posts } = await open(t, "Codex");
        assert.equal(await box(page).isChecked(), true);
        await box(page).scrollIntoViewIfNeeded();
        await box(page).uncheck();
        const saved = await save(page, posts);
        assert.equal(saved.body.id, "codex");
        assert.equal(saved.body.nativeCompaction, false);
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}: a Chat Completions relay and a Claude account have no such row`, async (t) => {
        for (const name of ["Chat Relay", "Claude Code"]) {
          const { page, errors, posts } = await open(t, name);
          assert.equal(await row(page).isVisible(), false, name);
          if (name === "Chat Relay") {
            const saved = await save(page, posts);
            assert.notEqual(saved.body.nativeCompaction, true, "never posted on");
          }
          assert.deepEqual(errors, []);
        }
      });
    }
  }
}
