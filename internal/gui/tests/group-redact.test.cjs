// Run with Node's test runner and Playwright on the module path; see README.md.
// A routing group can be masked or not on its own (1dwsdqwd2 on X): the
// group editor has a Masking row whose button says "Follow providers"
// until the app's menu picks "Always masked" or "Sent as written", and
// Save sends the pick as Group.Redact ("follow" clears it). A group that
// has one shows it. In every language, Chromium and WebKit, at a wide and
// a narrow window; nothing sticks out of the editor.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { reach } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const models = [{ id: "rl/glm-5", name: "glm-5", provider: "rl", providerName: "Relay", icon: "generic" }];
const groups = (red) => ({
  models, pools: [], accounts: {}, gatewayKeys: [],
  groups: [{ id: "work", name: "Work", members: ["rl/glm-5"], routing: "order", ready: true, accounts: [], ...(red ? { redact: red } : {}),
    memberInfo: [{ id: "rl/glm-5", ready: true }] }],
});

const words = {
  en: { edit: "Edit", save: "Save", label: "Masking", follow: "Follow providers", on: "Always masked", off: "Sent as written" },
  zh: { edit: "编辑", save: "保存", label: "脱敏", follow: "跟随各供应商", on: "始终脱敏", off: "按原样发送" },
  "zh-TW": { edit: "編輯", save: "儲存", label: "脫敏", follow: "跟隨各供應商", on: "一律脫敏", off: "照原樣送出" },
  ja: { edit: "編集", save: "保存", label: "マスク", follow: "プロバイダに従う", on: "常にマスクする", off: "そのまま送信" },
  de: { edit: "Bearbeiten", save: "Speichern", label: "Maskierung", follow: "Den Anbietern folgen", on: "Immer maskiert", off: "Unverändert gesendet" },
};

function serve(lang, posts, red) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(groups(red));
    if (url.pathname.startsWith("/api/groups/")) {
      posts.push({ path: url.pathname, body: JSON.parse(r.request().postData() || "{}") });
      return json(groups(red));
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

async function open(engine, lang, width, red, t) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  const page = await (await browser.newContext({ viewport: { width, height: 1600 }, reducedMotion: "reduce" })).newPage();
  t.after(async () => {
    if (process.env.ARTIFACT_DIR) {
      await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
      await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-group-redact.png`) });
    }
    await browser.close();
  });
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [], posts = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, posts, red));
  await page.goto("http://magpie.test/?view=groups");
  const card = page.locator(".rt-group", { hasText: "Work" });
  await card.waitFor();
  return { page, card, errors, posts };
}

const option = (page, text) => page.locator("#pop:not([hidden]) #list li:not(.group)", { has: page.locator(".v", { hasText: new RegExp("^" + text.replace(/[.*+?^${}()|[\]\\]/g, "\\$&") + "$") }) });

async function save(page, w) {
  const b = page.locator(".rt-gedit button.primary", { hasText: w.save });
  await b.scrollIntoViewIfNeeded();
  await b.click();
  await page.waitForFunction(() => !document.querySelector(".rt-gedit"));
}


for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    for (const width of [1100, 420]) {
      const w = words[lang];
      test(`${engine} ${lang} ${width}px: a group is masked, or not, on its own`, async (t) => {
        const { page, card, errors, posts } = await open(engine, lang, width, "", t);
        await card.locator("button", { hasText: w.edit }).click();
        const ed = page.locator(".rt-gedit");
        // set by few, so folded under Advanced (the owner, 11-12); this
        // group's routing is set, so it opens on it
        await ed.locator(".rt-gadv").waitFor();
        assert.equal(await ed.locator("> .rt-gredact").count(), 0, "not on the form itself");
        if (!(await ed.locator(".rt-gadv").evaluate((d) => d.open))) await ed.locator(".rt-gadv > summary").click();
        const label = ed.locator(".rt-gadv > .inner > label.rt-gredact");
        await label.waitFor();
        assert.equal((await label.textContent()).trim(), w.label);
        const chip = ed.locator("button.rt-gredact-b");
        const said = async () => (await chip.textContent()).trim();
        assert.equal(await said(), w.follow);
        assert.doesNotMatch((await chip.getAttribute("class")) || "", /\bon\b/);
        assert.equal(await ed.locator("select").count(), 0);

        // wheeled to, as a reader would: the app puts back a scroll that
        // isn't the reader's (Playwright's own), and opening the menu then
        // leaves the page where the reader left it
        await reach(page, chip);
        const top = () => page.evaluate(() => { let e = document.querySelector(".rt-gredact-b"); while ((e = e.parentElement) && !(e.scrollHeight > e.clientHeight + 2 && getComputedStyle(e).overflowY !== "visible")); return e ? e.scrollTop : scrollY; });
        const was = await top();
        await chip.click();
        await option(page, w.off).waitFor();
        assert.equal(await top(), was, "opening Masking's menu scrolled the page");
        assert.deepEqual(await page.locator("#pop #list li:not(.group) .v").allTextContents(), [w.follow, w.on, w.off]);
        await option(page, w.off).click();
        await page.waitForFunction(() => document.querySelector("#pop").hidden);
        assert.equal(await said(), w.off);
        assert.match((await chip.getAttribute("class")) || "", /\bon\b/);

        const over = await page.evaluate(() => {
          const ed = document.querySelector(".rt-gedit"), b = ed.querySelector(".rt-gredact-b");
          return { page: document.documentElement.scrollWidth - document.documentElement.clientWidth, row: b.getBoundingClientRect().right - ed.getBoundingClientRect().right };
        });
        assert(over.page <= 0, `the page scrolls sideways by ${over.page}px`);
        assert(over.row <= 1, `the button sticks out of the editor by ${over.row}px`);

        await save(page, w);
        assert.equal(posts.filter((p) => p.path === "/api/groups/save").at(-1).body.redact, "off");
        assert.deepEqual(errors, []);
      });

      if (width === 1100) {
        test(`${engine} ${lang}: a group that masks always shows it, and goes back to following`, async (t) => {
          const { page, card, errors, posts } = await open(engine, lang, width, "on", t);
          await card.locator("button", { hasText: w.edit }).click();
          const chip = page.locator(".rt-gedit button.rt-gredact-b");
          await chip.waitFor();
          assert.equal((await chip.textContent()).trim(), w.on);
          assert.equal(await page.locator(".rt-gedit .rt-gadv").evaluate((d) => d.open), true, "Advanced opens on a set masking");
          await save(page, w);
          assert.equal(posts.filter((p) => p.path === "/api/groups/save").at(-1).body.redact, "on", "saved as it was");
          await card.locator("button", { hasText: w.edit }).click();
          await chip.waitFor();
          await chip.click();
          await option(page, w.follow).click();
          await page.waitForFunction(() => document.querySelector("#pop").hidden);
          assert.equal((await chip.textContent()).trim(), w.follow);
          await save(page, w);
          assert.equal(posts.filter((p) => p.path === "/api/groups/save").at(-1).body.redact, "follow");
          assert.deepEqual(errors, []);
        });
      }
    }
  }
}
