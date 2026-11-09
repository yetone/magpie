// Run with Node's test runner and Playwright on the module path; see README.md.
// A routing group can be bound to gateway keys (Xerxes754 on X), so other
// keys neither see nor use it (Group.Keys). The group editor shows a
// Gateway keys row once there is a key to bind. Its button says "Any
// gateway key" until keys are ticked in a menu that stays open; one ticked
// shows its name, two show how many. Save sends the picks as Group.Keys. A
// bound key since deleted is listed as deleted and can be unticked. With no
// keys at all and none bound, the row is hidden. Nothing on the page moves.
// In every language, Chromium and WebKit, at a wide and a narrow window.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [{ id: "rl/glm-5", name: "glm-5", provider: "rl", providerName: "Relay", icon: "generic" }];
const gatewayKeys = [
  { id: "k-laptop", name: "Laptop", masked: "mg-…a1b2" },
  { id: "k-team", name: "Team server", masked: "mg-…c3d4" },
];
const groups = (keys, bound) => ({
  models, pools: [], accounts: {}, gatewayKeys: keys ? gatewayKeys : [],
  groups: [{ id: "work", name: "Work", members: ["rl/glm-5"], routing: "order", ready: true, accounts: [], ...(bound ? { keys: bound } : {}),
    memberInfo: [{ id: "rl/glm-5", ready: true }] }],
});

const words = {
  en: { edit: "Edit", save: "Save", label: "Gateway keys", any: "Any gateway key", two: "2 gateway keys", removed: "key deleted" },
  zh: { edit: "编辑", save: "保存", label: "网关密钥", any: "任意网关密钥", two: "2 个网关密钥", removed: "密钥已删除" },
  "zh-TW": { edit: "編輯", save: "儲存", label: "閘道金鑰", any: "任意閘道金鑰", two: "2 個閘道金鑰", removed: "金鑰已刪除" },
  ja: { edit: "編集", save: "保存", label: "ゲートウェイキー", any: "どのゲートウェイキーでも", two: "ゲートウェイキー 2 個", removed: "削除されたキー" },
  de: { edit: "Bearbeiten", save: "Speichern", label: "Zugangsschlüssel", any: "Jeder Zugangsschlüssel", two: "2 Gateway-Schlüssel", removed: "Schlüssel entfernt" },
};

function serve(lang, posts, keys, bound) {
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
    if (url.pathname === "/api/groups") return json(groups(keys, bound));
    if (url.pathname.startsWith("/api/groups/")) {
      posts.push({ path: url.pathname, body: JSON.parse(r.request().postData() || "{}") });
      return json(groups(keys, bound));
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

async function open(engine, lang, width, keys, bound, t) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  const page = await (await browser.newContext({ viewport: { width, height: 1600 }, reducedMotion: "reduce" })).newPage();
  t.after(async () => {
    if (process.env.ARTIFACT_DIR) {
      await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
      await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-group-keys.png`) });
    }
    await browser.close();
  });
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [], posts = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, posts, keys, bound));
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
      test(`${engine} ${lang} ${width}px: a group is bound to chosen gateway keys`, async (t) => {
        const { page, card, errors, posts } = await open(engine, lang, width, true, null, t);
        await card.locator("button", { hasText: w.edit }).click();
        const ed = page.locator(".rt-gedit");
        const label = ed.locator("> label.rt-gkeys");
        await label.waitFor();
        assert.equal((await label.textContent()).trim(), w.label);
        const chip = ed.locator("button.rt-gkey");
        const said = async () => (await chip.textContent()).trim();
        assert.equal(await said(), w.any);
        assert.doesNotMatch((await chip.getAttribute("class")) || "", /\bon\b/);

        // the reader scrolls the button into view with the wheel
        await page.mouse.move(width / 2, 800);
        for (let i = 0; i < 20 && (await chip.boundingBox()).y > 1200; i++) await page.mouse.wheel(0, 200), await page.waitForTimeout(100);
        await page.waitForTimeout(200);
        const box = await chip.boundingBox();
        assert(box.y >= 0 && box.y + box.height <= 1600, `the button is in view before the click: ${box.y}`);
        const at = () => page.evaluate(() => [...document.querySelectorAll("*")].filter((e) => e.scrollTop && !e.closest("#pop")).map((e) => [e.id || e.className, e.scrollTop]).join(";") + "|" + Math.round(document.querySelector(".rt-gedit").getBoundingClientRect().top));
        const before = await at();

        // the app's menu (never a native select) lists each key, none
        // ticked, and stays open as one is
        assert.equal(await ed.locator("select").count(), 0);
        await chip.click();
        await option(page, "Team server").waitFor();
        assert.deepEqual(await page.locator("#pop #list li:not(.group) .v").allTextContents(), ["Laptop", "Team server"]);
        assert.doesNotMatch((await option(page, "Team server").getAttribute("class")) || "", /\bcur\b/);
        await option(page, "Team server").click();
        await page.waitForFunction(() => !document.querySelector("#pop").hidden);
        assert.match((await option(page, "Team server").getAttribute("class")) || "", /\bcur\b/);
        assert.equal(await said(), "Team server");
        assert.match((await chip.getAttribute("class")) || "", /\bon\b/);
        // two ticked say how many; unticking one says its name again
        await option(page, "Laptop").click();
        assert.equal(await said(), w.two);
        await option(page, "Laptop").click();
        assert.equal(await said(), "Team server");
        await page.keyboard.press("Escape");
        await page.waitForFunction(() => document.querySelector("#pop").hidden);
        assert.equal(await at(), before, "the picks moved nothing");

        // the row fits a narrow window, the page doesn't scroll sideways
        const over = await page.evaluate(() => {
          const ed = document.querySelector(".rt-gedit"), b = ed.querySelector(".rt-gkey");
          return { page: document.documentElement.scrollWidth - document.documentElement.clientWidth, row: b.getBoundingClientRect().right - ed.getBoundingClientRect().right };
        });
        assert(over.page <= 0, `the page scrolls sideways by ${over.page}px`);
        assert(over.row <= 1, `the key button sticks out of the editor by ${over.row}px`);

        await save(page, w);
        const sent = posts.filter((p) => p.path === "/api/groups/save").at(-1).body;
        assert.deepEqual(sent.keys, ["k-team"]);
        assert.deepEqual(errors, []);
      });

      if (width === 1100) {
        test(`${engine} ${lang}: a bound key since deleted is listed so it can be unticked`, async (t) => {
          const { page, card, errors, posts } = await open(engine, lang, width, true, ["k-laptop", "k-gone"], t);
          await card.locator("button", { hasText: w.edit }).click();
          const chip = page.locator(".rt-gedit button.rt-gkey");
          await chip.waitFor();
          assert.equal((await chip.textContent()).trim(), w.two);
          await chip.click();
          await option(page, "k-gone").waitFor();
          assert((await option(page, "k-gone").textContent()).includes(w.removed));
          assert.match((await option(page, "k-gone").getAttribute("class")) || "", /\bcur\b/);
          await option(page, "k-gone").click();
          assert.equal((await chip.textContent()).trim(), "Laptop");
          await page.keyboard.press("Escape");
          await page.waitForFunction(() => document.querySelector("#pop").hidden);
          await save(page, w);
          const sent = posts.filter((p) => p.path === "/api/groups/save").at(-1).body;
          assert.deepEqual(sent.keys, ["k-laptop"]);
          assert.deepEqual(errors, []);
        });

        test(`${engine} ${lang}: with no gateway keys the row is hidden`, async (t) => {
          const { page, card, errors, posts } = await open(engine, lang, width, false, null, t);
          await card.locator("button", { hasText: w.edit }).click();
          const ed = page.locator(".rt-gedit");
          await ed.locator("button.primary", { hasText: w.save }).waitFor();
          assert.equal(await ed.locator("> .rt-gkeys:visible").count(), 0);
          await save(page, w);
          const sent = posts.filter((p) => p.path === "/api/groups/save").at(-1).body;
          assert.deepEqual(sent.keys, []);
          assert.deepEqual(errors, []);
        });
      }
    }
  }
}
