// Run with Node's test runner and Playwright on the module path; see README.md.
// A provider's Accounts at once (01huadalang on Discord: three ZCode
// accounts used at once got the address refused with a 405, two didn't):
// its editor has the field beside Concurrency, empty for none set, a
// plugin's own count as the placeholder; Save posts the number typed (0 for
// no limit), null when it is empty, and refuses one that isn't a whole
// number before anything is posted. On Routing a key held back says it is
// in reserve, on its row and in the request's story. In every language.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const unfold = async (page) => {
  const adv = page.locator(".editor details.advanced");
  await adv.waitFor({ state: "attached" });
  if (!(await adv.evaluate((d) => d.open))) await adv.locator(":scope > summary").click();
};
const keys = [1, 2, 3].map((n, i) => ({ id: `key-${n}`, name: `Key ${n}`, masked: `k${n}`, active: i === 0, on: true }));
const relay = {
  id: "relay", name: "Relay", icon: "generic", chat: "https://relay.example.invalid/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "m", name: "M", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "k1" }, keyList: keys, balanceToken: { takes: false, set: false }, proxy: "", maxConcurrency: null, maxAccounts: null,
};
const lemon = {
  id: "lemon", name: "Lemon", icon: "generic", chat: "plugin://lemon/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "m1", name: "M1", on: true }], agents: [], fallback: [], headers: {}, keyList: [],
  account: { agent: "plugin", agentName: "Lemon", user: "lemon@example.invalid", plan: "" },
  proxy: "", maxConcurrency: null, maxAccounts: null, pluginAccounts: 2,
};
// one request as the gateway traces it: Key 1 and Key 2 kept, Key 3 in
// reserve after them
const seat = (k, rank, extra) => ({ id: `relay#${k.id}`, provider: "relay", name: "Relay", who: k.name, kind: "key", model: "m", rank, shared: true, ...extra });
const t0 = Date.now() - 2000;
const order = [seat(keys[0], 0), seat(keys[1], 1), seat(keys[2], 2, { aside: true, reserve: 2 })];
const route = { id: 100, seq: 1, time: new Date(t0).toISOString(), agent: "claude", model: "relay/m", provider: "relay", order,
  tries: [{ id: order[0].id, model: "m", start: new Date(t0 + 10).toISOString(), done: true, status: 200, ms: 300 }], done: true, status: 200, ms: 320, tokens: 100 };

function serve(lang, posts, wait) {
  const providers = { providers: [relay, lemon], presets: [], excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [{ id: "claude", name: "Claude", icon: "generic", path: "/fixture", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") {
      const out = (routes) => json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 1, rerouted: 0, errors: 0 }, routes });
      if (!url.searchParams.get("wait")) return out([route]);
      return new Promise((res) => wait.push(() => res(out([]))));
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname.startsWith("/api/provider/") && r.request().method() === "POST") {
      posts.push({ action: url.pathname.slice("/api/provider/".length), body: r.request().postDataJSON() });
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { label: "Accounts at once", save: "Save", none: "No limit", plugin: "2, as its plugin says",
    hint: "How many of its accounts or keys take requests at once, the first in the list; the next takes over when one runs out or fails. 0 is no limit",
    bad: "Accounts at once: a whole number from 0 to 1000", row: "in reserve · 2 at once",
    why: "Key 3 is in reserve: 2 of its provider's accounts or keys take requests at once, the first in the list, so it's tried only once they have all failed." },
  zh: { label: "同时使用的账号数", save: "保存", none: "不限制", plugin: "2（插件的默认值）",
    hint: "同时接请求的账号或 Key 数量，按列表顺序取前几个；其中一个额度用完或失败时，由下一个接替。0 为不限制",
    bad: "同时使用的账号数：0 到 1000 的整数", row: "备用 · 同时 2 个", why: "是备用的：其提供商同时只用列表中前 2 个账号或 Key" },
  "zh-TW": { label: "同時使用的帳號數", row: "備用 · 同時 2 個", why: "是備用的：其供應商同時只用清單中前 2 個帳號或 Key" },
  ja: { label: "同時に使うアカウント数", row: "予備 · 同時に 2 個", why: "は予備です。このプロバイダーは一覧の先頭から 2 個" },
  de: { label: "Konten gleichzeitig", row: "in Reserve · 2 gleichzeitig", why: "ist in Reserve: Von seinem Anbieter nehmen nur die ersten 2 Konten" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    const w = words[lang];
    const open = async (t, view, width = 900) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const wait = [];
      const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${view}-${width}-at-once.png`) });
        }
        for (const f of wait) f();
        await browser.close();
      });
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const posts = [];
      await page.route("**/*", serve(lang, posts, wait));
      await page.goto(`http://magpie.test/?view=${view}`);
      return { page, errors, posts };
    };
    const box = (page) => page.locator(".editor input.max-accounts");
    const edit = async (page, name) => {
      await page.locator(".row.provider", { hasText: name }).click();
      await unfold(page);
      await box(page).waitFor();
      await page.waitForTimeout(300); // the editor drawn again as what it asked for comes in
    };
    const save = async (page, posts) => {
      const n = posts.length;
      await page.locator(".editor .bar").getByRole("button", { name: w.save, exact: true }).click();
      for (let i = 0; i < 50 && posts.length === n; i++) await page.waitForTimeout(50);
      return posts.at(-1);
    };

    for (const width of [900, 360]) {
      test(`${engine} ${lang} ${width}px: Accounts at once sits beside Concurrency, its label on one line`, async (t) => {
        const { page, errors } = await open(t, "providers", width);
        await edit(page, "Relay");
        const label = page.locator(".editor label", { hasText: w.label });
        assert.equal(await label.count(), 1);
        // right after Concurrency, in the same column of fields
        const next = await page.locator(".editor input.concurrency").evaluate((c) => {
          const all = [...document.querySelectorAll(".editor input")];
          return all[all.indexOf(c) + 1]?.className;
        });
        assert.match(next, /max-accounts/);
        const look = (e) => { const s = getComputedStyle(e); return { bg: s.backgroundColor, border: s.borderTopColor, radius: s.borderTopLeftRadius, height: s.height }; };
        assert.deepEqual(await box(page).evaluate(look), await page.locator(".editor input.concurrency").evaluate(look), "styled as Concurrency is");
        // the text's own height: a name beside a field is padded down to its line
        const text = (e) => { const s = getComputedStyle(e); return e.getBoundingClientRect().height - parseFloat(s.paddingTop) - parseFloat(s.paddingBottom); };
        const h = await label.evaluate(text);
        const one = await page.locator(".editor label").first().evaluate(text);
        assert(h <= one + 1, `the label takes ${h}px, one line is ${one}px`);
        const stripes = await box(page).evaluate((e) => { const s = getComputedStyle(e); return parseFloat(s.borderLeftWidth) > parseFloat(s.borderRightWidth) || (parseFloat(s.borderLeftWidth) > 0 && s.borderLeftColor !== s.borderRightColor); });
        assert.equal(stripes, false, "no border stripe");
        assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "no sideways scroll");
        assert.deepEqual(errors, []);
      });
    }

    test(`${engine} ${lang}: a key held back says it is in reserve on Routing`, async (t) => {
      const { page, errors } = await open(t, "routing", 1100);
      const fold = page.locator(".rt-accts > li.rt-fold:not(.open)");
      await page.locator(".rt-accts > li").first().waitFor();
      if (await fold.count()) await fold.first().click();
      const k3 = page.locator('.rt-accts > li[title^="relay#key-3"]');
      await k3.waitFor();
      await page.waitForFunction((want) => document.querySelector('.rt-accts > li[title^="relay#key-3"]')?.textContent.includes(want), w.row);
      const k2 = await page.locator('.rt-accts > li[title^="relay#key-2"]').textContent();
      assert(!k2.includes(w.row), `a kept key isn't in reserve: ${k2}`);
      await page.locator(".rt-req").first().click();
      await page.waitForTimeout(400);
      const story = await page.locator(".rt-steps li").allTextContents();
      assert(story.some((s) => s.includes(w.why)), JSON.stringify(story));
      assert.deepEqual(errors, []);
    });

    if (!w.save) continue;

    test(`${engine} ${lang}: accounts at once are typed and saved`, async (t) => {
      const { page, errors, posts } = await open(t, "providers");
      await edit(page, "Relay");
      assert.equal(await box(page).inputValue(), "", "none set");
      assert.equal(await box(page).getAttribute("placeholder"), w.none);
      assert.equal(await box(page).evaluate((e) => e.parentElement.querySelector(".hint")?.textContent), w.hint);

      // not a whole number: refused, nothing posted
      await box(page).fill("1.5");
      await page.locator(".editor .bar").getByRole("button", { name: w.save, exact: true }).click();
      await page.locator(".editor .editor-error").waitFor();
      assert.equal(await page.locator(".editor .editor-error").textContent(), w.bad);
      assert.equal(posts.length, 0);

      await box(page).fill("2");
      let saved = await save(page, posts);
      assert.equal(saved.body.id, "relay");
      assert.equal(saved.body.maxAccounts, 2);
      assert.equal(saved.body.maxConcurrency, null, "Concurrency left as it was");

      await edit(page, "Relay");
      await box(page).fill("");
      saved = await save(page, posts);
      assert(Object.hasOwn(saved.body, "maxAccounts"), "an empty field is saved as none, not left out");
      assert.equal(saved.body.maxAccounts, null);
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: a plugin's provider shows its plugin's count, and 0 is no limit`, async (t) => {
      const { page, errors, posts } = await open(t, "providers");
      await edit(page, "Lemon");
      assert.equal(await box(page).inputValue(), "");
      assert.equal(await box(page).getAttribute("placeholder"), w.plugin);
      let saved = await save(page, posts);
      assert.equal(saved.body.maxAccounts, null, "empty follows the plugin");
      await edit(page, "Lemon");
      await box(page).fill("0");
      saved = await save(page, posts);
      assert.equal(saved.body.maxAccounts, 0, "0 is the user's own no limit, saved as such");
      assert.deepEqual(errors, []);
    });
  }
}
