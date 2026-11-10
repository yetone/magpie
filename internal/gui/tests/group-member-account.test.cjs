// Run with Node's test runner and Playwright on the module path; see README.md.
// A group's model can be sent on one account of its own (EastonXvii on X:
// Astra on account A, then Astra on B, then Sol on C). The model row has an
// account chip, "Any account" until one is picked in the app's menu; picked,
// the row says the account. "Also on another account…" adds the same model on
// another account as a member of its own, after the row, and the save sends
// both as provider/model#<account id> in that order. A model whose provider
// has one account has no chip. In every language, Chromium and WebKit, at a
// wide and a narrow window.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const A = "a1b2c3d4e5f60718", B = "0f1e2d3c4b5a6978";
const models = [
  { id: "codex/gpt-6-astra", name: "gpt-6-astra", provider: "codex", providerName: "Codex", icon: "generic" },
  { id: "rl/glm-5", name: "glm-5", provider: "rl", providerName: "Relay", icon: "generic" },
];
const accounts = {
  codex: [
    { id: "codex/" + A, name: "me@x.com", provider: "codex", providerName: "Codex", account: true, plan: "Plus" },
    { id: "codex/" + B, name: "work@corp.example", provider: "codex", providerName: "Codex", account: true, plan: "Team" },
  ],
};
const groups = (members) => ({
  models, pools: [], accounts,
  groups: [{ id: "work", name: "Work", members, routing: "order", ready: true, accounts: [],
    memberInfo: members.map((id) => ({ id, ready: true })) }],
});

const words = {
  en: { edit: "Edit", save: "Save", any: "Any account", also: "Also on another account…" },
  zh: { edit: "编辑", save: "保存", any: "任意账号", also: "再加一个账号…" },
  "zh-TW": { edit: "編輯", save: "儲存", any: "任意帳號", also: "再加一個帳號…" },
  ja: { edit: "編集", save: "保存", any: "どのアカウントでも", also: "別のアカウントでも追加…" },
  de: { edit: "Bearbeiten", save: "Speichern", any: "Beliebiges Konto", also: "Auch mit einem anderen Konto…" },
};

function serve(lang, posts, members) {
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
    if (url.pathname === "/api/groups") return json(groups(members));
    if (url.pathname.startsWith("/api/groups/")) {
      posts.push({ path: url.pathname, body: JSON.parse(r.request().postData() || "{}") });
      return json(groups(members));
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

async function open(engine, lang, width, members, t) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  const page = await (await browser.newContext({ viewport: { width, height: 1600 }, reducedMotion: "reduce" })).newPage();
  t.after(async () => {
    if (process.env.ARTIFACT_DIR) {
      await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
      await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-group-member-account.png`) });
    }
    await browser.close();
  });
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [], posts = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, posts, members));
  await page.goto("http://magpie.test/?view=groups");
  const card = page.locator(".rt-group", { hasText: "Work" });
  await card.waitFor();
  return { page, card, errors, posts };
}

const option = (page, text) => page.locator("#pop:not([hidden]) #list li:not(.group)", { has: page.locator(".v", { hasText: new RegExp("^" + text.replace(/[.*+?^${}()|[\]\\]/g, "\\$&") + "$") }) });

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    for (const width of [1100, 420]) {
      const w = words[lang];
      test(`${engine} ${lang} ${width}px: a model on accounts of its own, in order`, async (t) => {
        const { page, card, errors, posts } = await open(engine, lang, width, ["codex/gpt-6-astra", "rl/glm-5"], t);
        await card.locator("button", { hasText: w.edit }).click();
        const ed = page.locator(".rt-gedit");
        await ed.locator(".rt-macct").first().waitFor();
        // a chip on the Codex row only: Relay has no accounts listed
        assert.equal(await ed.locator(".rt-macct").count(), 1);
        const chip = () => ed.locator(".rt-macct").first();
        assert.equal((await chip().textContent()).trim(), w.any);
        assert.doesNotMatch((await chip().getAttribute("class")) || "", /\bon\b/);

        // picked from the app's menu, never a native select
        await chip().click();
        await option(page, "work@corp.example").waitFor();
        assert.equal(await page.locator(".rt-gedit select").count(), 0);
        assert.deepEqual(await page.locator("#pop #list li:not(.group) .v").allTextContents(), [w.any, "me@x.com", "work@corp.example", w.also]);
        await option(page, "work@corp.example").click();
        await page.waitForFunction(() => document.querySelector("#pop").hidden);
        assert.equal((await chip().textContent()).trim(), "work@corp.example");
        assert.match((await chip().getAttribute("class")) || "", /\bon\b/);
        const codexRow = ed.locator(".fbrow", { has: page.locator(".rt-macct") }).first();
        assert((await codexRow.locator(".n small").first().textContent()).includes("work@corp.example"));

        // the same model on the other account: a member of its own, next
        await chip().click();
        await option(page, w.also).click();
        await option(page, "me@x.com").waitFor();
        assert.deepEqual(await page.locator("#pop:not([hidden]) #list li:not(.group) .v").allTextContents(), ["me@x.com"]);
        await option(page, "me@x.com").click();
        await page.waitForFunction(() => document.querySelectorAll(".rt-gedit .rt-macct").length === 2);
        assert.deepEqual((await ed.locator(".rt-macct").allTextContents()).map((s) => s.trim()), ["work@corp.example", "me@x.com"]);
        // on every account it has already: nothing more to add
        await chip().click();
        await option(page, "me@x.com").waitFor();
        assert.deepEqual(await page.locator("#pop #list li:not(.group) .v").allTextContents(), [w.any, "me@x.com", "work@corp.example"]);
        await page.keyboard.press("Escape");
        await page.waitForFunction(() => document.querySelector("#pop").hidden);
        assert.equal(await ed.locator(".rt-macct").count(), 2);

        // the row fits a narrow window, the page doesn't scroll sideways
        const over = await page.evaluate(() => {
          const b = document.querySelector(".rt-gedit .rt-macct"), row = b.closest(".fbrow");
          return { page: document.documentElement.scrollWidth - document.documentElement.clientWidth, row: b.getBoundingClientRect().right - row.getBoundingClientRect().right };
        });
        assert(over.page <= 0, `the page scrolls sideways by ${over.page}px`);
        assert(over.row <= 1, `the account chip sticks out of its row by ${over.row}px`);

        const b = page.locator(".rt-gedit button.primary", { hasText: w.save });
        await b.scrollIntoViewIfNeeded();
        await b.click();
        await page.waitForFunction(() => !document.querySelector(".rt-gedit"));
        const sent = posts.filter((p) => p.path === "/api/groups/save").at(-1).body;
        assert.deepEqual(sent.members, ["codex/gpt-6-astra#" + B, "codex/gpt-6-astra#" + A, "rl/glm-5"]);
        assert.deepEqual(errors, []);
      });
    }
  }
}

// A group saved with members on accounts reads back with each row's
// account, and its reasoning changed on one keeps the account.
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: reasoning set on a member keeps its account`, async (t) => {
    const { page, card, errors, posts } = await open(engine, "en", 1100, ["codex/gpt-6-astra#" + A, "codex/gpt-6-astra#" + B], t);
    await card.locator("button", { hasText: "Edit" }).click();
    const ed = page.locator(".rt-gedit");
    await ed.locator(".rt-macct").first().waitFor();
    assert.deepEqual((await ed.locator(".rt-macct").allTextContents()).map((s) => s.trim()), ["me@x.com", "work@corp.example"]);
    await ed.locator(".rt-fixed:not(.rt-macct):not(.rt-fast)").nth(1).click();
    await option(page, "high reasoning").click();
    const b = page.locator(".rt-gedit button.primary", { hasText: "Save" });
    await b.click();
    await page.waitForFunction(() => !document.querySelector(".rt-gedit"));
    const sent = posts.filter((p) => p.path === "/api/groups/save").at(-1).body;
    assert.deepEqual(sent.members, ["codex/gpt-6-astra#" + A, "codex/gpt-6-astra:high#" + B]);
    assert.deepEqual(errors, []);
  });
}
