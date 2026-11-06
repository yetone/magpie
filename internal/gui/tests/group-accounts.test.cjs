// Run with Node's test runner and Playwright on the module path; see README.md.
// A routing group can be held to chosen accounts of a provider (duanckham on
// X: personal and company GPT accounts, one tool, one gateway). The group
// editor shows an Accounts section for each member's provider with more than
// one account. Its button says "Any of its accounts" until accounts are
// ticked in a menu that stays open; one ticked account shows its name, two
// show how many. Save sends the picks as Group.Accounts. A group whose
// providers have one account each shows no section. Nothing on the page
// moves. In every language, Chromium and WebKit, at a wide and a narrow
// window.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { id: "codex/gpt-6.1-sol", name: "gpt-6.1-sol", provider: "codex", providerName: "Codex", icon: "generic" },
  { id: "rl/glm-5", name: "glm-5", provider: "rl", providerName: "Relay", icon: "generic" },
];
const accounts = {
  codex: [
    { id: "codex/me@x.com", name: "me@x.com", provider: "codex", providerName: "Codex", account: true, plan: "Plus" },
    { id: "codex/work@corp.example", name: "work@corp.example", provider: "codex", providerName: "Codex", account: true, plan: "Team" },
  ],
};
const groups = (members, held) => ({
  models, pools: [], accounts: held === false ? {} : accounts,
  groups: [{ id: "work", name: "Work", members, routing: "order", ready: true, accounts: [],
    memberInfo: members.map((id) => ({ id, ready: true })) }],
});

const words = {
  en: { edit: "Edit", save: "Save", accounts: "Accounts", any: "Any of its accounts", two: "2 of its accounts" },
  zh: { edit: "编辑", save: "保存", accounts: "账号", any: "任一账号", two: "2 个账号" },
  "zh-TW": { edit: "編輯", save: "儲存", accounts: "帳號", any: "任一帳號", two: "2 個帳號" },
  ja: { edit: "編集", save: "保存", accounts: "アカウント", any: "どのアカウントでも", two: "2 個のアカウント" },
  de: { edit: "Bearbeiten", save: "Speichern", accounts: "Konten", any: "Jedes seiner Konten", two: "2 seiner Konten" },
};

function serve(lang, posts, members, held) {
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
    if (url.pathname === "/api/groups") return json(groups(members, held));
    if (url.pathname.startsWith("/api/groups/")) {
      posts.push({ path: url.pathname, body: JSON.parse(r.request().postData() || "{}") });
      return json(groups(members, held));
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

async function open(engine, lang, width, members, held, t) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  const page = await (await browser.newContext({ viewport: { width, height: 1600 }, reducedMotion: "reduce" })).newPage();
  t.after(async () => {
    if (process.env.ARTIFACT_DIR) {
      await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
      await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-group-accounts.png`) });
    }
    await browser.close();
  });
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [], posts = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, posts, members, held));
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
      test(`${engine} ${lang} ${width}px: a group is held to chosen accounts`, async (t) => {
        const { page, card, errors, posts } = await open(engine, lang, width, ["codex/gpt-6.1-sol", "rl/glm-5"], true, t);
        await card.locator("button", { hasText: w.edit }).click();
        const ed = page.locator(".rt-gedit");
        const label = ed.locator("> label.rt-gacc");
        await label.waitFor();
        assert.equal((await label.textContent()).trim(), w.accounts);
        // one row, for Codex only: Relay has no accounts listed
        const rows = ed.locator(".rt-gaccts .fbrow");
        assert.equal(await rows.count(), 1);
        assert((await rows.first().textContent()).includes("Codex"));
        const chip = rows.first().locator("button.rt-gacct");
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

        // the menu lists each account, none ticked, and stays open as one is
        await chip.click();
        await option(page, "work@corp.example").waitFor();
        assert.deepEqual(await page.locator("#pop #list li:not(.group) .v").allTextContents(), ["me@x.com", "work@corp.example"]);
        assert.doesNotMatch((await option(page, "work@corp.example").getAttribute("class")) || "", /\bcur\b/);
        await option(page, "work@corp.example").click();
        await page.waitForFunction(() => !document.querySelector("#pop").hidden);
        assert.match((await option(page, "work@corp.example").getAttribute("class")) || "", /\bcur\b/);
        assert.equal(await said(), "work@corp.example");
        assert.match((await chip.getAttribute("class")) || "", /\bon\b/);
        // two ticked say how many; unticking one says its name again
        await option(page, "me@x.com").click();
        assert.equal(await said(), w.two);
        await option(page, "me@x.com").click();
        assert.equal(await said(), "work@corp.example");
        await page.keyboard.press("Escape");
        await page.waitForFunction(() => document.querySelector("#pop").hidden);
        assert.equal(await at(), before, "the picks moved nothing");

        // the row fits a narrow window, the page doesn't scroll sideways
        const over = await page.evaluate(() => {
          const row = document.querySelector(".rt-gaccts .fbrow"), b = row.querySelector(".rt-gacct");
          return { page: document.documentElement.scrollWidth - document.documentElement.clientWidth, row: b.getBoundingClientRect().right - row.getBoundingClientRect().right };
        });
        assert(over.page <= 0, `the page scrolls sideways by ${over.page}px`);
        assert(over.row <= 1, `the account button sticks out of its row by ${over.row}px`);

        await save(page, w);
        const sent = posts.filter((p) => p.path === "/api/groups/save").at(-1).body;
        assert.deepEqual(sent.accounts, ["codex/work@corp.example"]);
        assert.deepEqual(errors, []);
      });

      if (width === 1100) {
        test(`${engine} ${lang}: a group whose providers have one account shows no Accounts`, async (t) => {
          const { page, card, errors, posts } = await open(engine, lang, width, ["rl/glm-5"], false, t);
          await card.locator("button", { hasText: w.edit }).click();
          const ed = page.locator(".rt-gedit");
          await ed.locator("button.primary", { hasText: w.save }).waitFor();
          assert.equal(await ed.locator("> .rt-gacc:visible").count(), 0);
          await save(page, w);
          const sent = posts.filter((p) => p.path === "/api/groups/save").at(-1).body;
          assert.deepEqual(sent.accounts, []);
          assert.deepEqual(errors, []);
        });
      }
    }
  }
}
