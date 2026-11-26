// Run with Node's test runner and Playwright on the module path; see README.md.
// A routing group held to some accounts of a provider (Group.Accounts) never
// sends a member on another (the gateway's planLevel: groupHeld), so a model
// row's own account can't be one the group leaves out (JJasonsun on Discord:
// the row's picker listed every account, and a row pinned to one the group
// didn't allow was never used, saying nothing). The row's menu shows such an
// account greyed with why, and a click on it changes nothing; "Also on
// another account…" offers only those the group allows. A row pinned before
// the group left its account out says so: on the group's card, under the
// model's name and on its chip, at once when the group's accounts are
// changed in the editor, and no longer once an allowed account is picked.
// In every language, Chromium and WebKit, at a wide and a narrow window.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ASSETS || path.join(__dirname, "../assets"));
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
const groups = (members, held) => ({
  models, pools: [], accounts,
  groups: [{ id: "work", name: "Work", members, routing: "order", ready: true, accounts: held,
    memberInfo: members.map((id) => ({ id, ready: true })) }],
});

const words = {
  en: { edit: "Edit", save: "Save", any: "Any account", also: "Also on another account…", out: "not allowed in this group" },
  zh: { edit: "编辑", save: "保存", any: "任意账号", also: "再加一个账号…", out: "不在本分组允许的账号里" },
  "zh-TW": { edit: "編輯", save: "儲存", any: "任意帳號", also: "再加一個帳號…", out: "不在本分組允許的帳號裡" },
  ja: { edit: "編集", save: "保存", any: "どのアカウントでも", also: "別のアカウントでも追加…", out: "このグループでは使えません" },
  de: { edit: "Bearbeiten", save: "Speichern", any: "Beliebiges Konto", also: "Auch mit einem anderen Konto…", out: "in dieser Gruppe nicht erlaubt" },
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
      await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-group-member-account-held.png`) });
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

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    for (const width of [1100, 420]) {
      const w = words[lang];
      test(`${engine} ${lang} ${width}px: a model row picks only accounts the group allows`, async (t) => {
        const { page, card, errors } = await open(engine, lang, width, ["codex/gpt-6-astra", "rl/glm-5"], ["codex/" + A], t);
        await card.locator("button", { hasText: w.edit }).click();
        const ed = page.locator(".rt-gedit");
        const chip = () => ed.locator(".rt-macct").first();
        await chip().waitFor();
        await chip().click();
        await option(page, "work@corp.example").waitFor();
        assert.deepEqual(await page.locator("#pop #list li:not(.group) .v").allTextContents(), [w.any, "me@x.com", "work@corp.example", w.also]);
        const out = option(page, "work@corp.example");
        assert.equal(await out.getAttribute("aria-disabled"), "true");
        assert((await out.locator(".n").textContent()).includes(w.out));
        assert.equal(await option(page, "me@x.com").getAttribute("aria-disabled"), null);
        // a click on the one left out picks nothing
        await out.click();
        assert.equal(await page.locator("#pop").isHidden(), false);
        await option(page, w.any).click();
        await page.waitForFunction(() => document.querySelector("#pop").hidden);
        assert.equal((await chip().textContent()).trim(), w.any);
        // the same model on another account: only the one the group allows
        await chip().click();
        await option(page, w.also).click();
        await option(page, "me@x.com").waitFor();
        assert.deepEqual(await page.locator("#pop:not([hidden]) #list li:not(.group) .v").allTextContents(), ["me@x.com"]);
        await page.keyboard.press("Escape");
        await page.waitForFunction(() => document.querySelector("#pop").hidden);
        assert.equal(await ed.locator(".rt-macct.bad").count(), 0);
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}px: a row on an account the group leaves out says so`, async (t) => {
        const { page, card, errors, posts } = await open(engine, lang, width, ["codex/gpt-6-astra#" + B, "rl/glm-5"], ["codex/" + A], t);
        // on the group's card
        assert((await card.textContent()).includes(`work@corp.example (${w.out})`), await card.textContent());
        await card.locator("button", { hasText: w.edit }).click();
        const ed = page.locator(".rt-gedit");
        const chip = () => ed.locator(".rt-macct").first();
        await chip().waitFor();
        assert.equal((await chip().textContent()).trim(), "work@corp.example");
        assert.match((await chip().getAttribute("class")) || "", /\bbad\b/);
        assert((await chip().getAttribute("aria-label")).includes(w.out));
        const row = ed.locator(".fbrow", { has: page.locator(".rt-macct") }).first();
        assert((await row.locator(".n small").first().textContent()).includes(w.out));
        // the row still fits a narrow window
        const over = await page.evaluate(() => {
          const b = document.querySelector(".rt-gedit .rt-macct"), r = b.closest(".fbrow");
          return { page: document.documentElement.scrollWidth - document.documentElement.clientWidth, row: b.getBoundingClientRect().right - r.getBoundingClientRect().right };
        });
        assert(over.page <= 0, `the page scrolls sideways by ${over.page}px`);
        assert(over.row <= 1, `the account chip sticks out of its row by ${over.row}px`);
        // an account the group allows puts it right
        await chip().click();
        await option(page, "me@x.com").click();
        await page.waitForFunction(() => document.querySelector("#pop").hidden);
        assert.equal((await chip().textContent()).trim(), "me@x.com");
        assert.doesNotMatch((await chip().getAttribute("class")) || "", /\bbad\b/);
        assert(!(await row.locator(".n small").first().textContent()).includes(w.out));
        const b = page.locator(".rt-gedit button.primary", { hasText: w.save });
        await b.scrollIntoViewIfNeeded();
        await b.click();
        await page.waitForFunction(() => !document.querySelector(".rt-gedit"));
        const sent = posts.filter((p) => p.path === "/api/groups/save").at(-1).body;
        assert.deepEqual(sent.members, ["codex/gpt-6-astra#" + A, "rl/glm-5"]);
        assert.deepEqual(sent.accounts, ["codex/" + A]);
        assert.deepEqual(errors, []);
      });
    }
  }
}

// The group's accounts changed in the editor: a row pinned to the one taken
// out says so at once, and again allowed, it is as before.
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: narrowing the group's accounts marks a row at once`, async (t) => {
    const { page, card, errors } = await open(engine, "en", 1100, ["codex/gpt-6-astra#" + B], [], t);
    await card.locator("button", { hasText: "Edit" }).click();
    const ed = page.locator(".rt-gedit");
    const chip = () => ed.locator(".rt-macct").first();
    await chip().waitFor();
    assert.doesNotMatch((await chip().getAttribute("class")) || "", /\bbad\b/);
    await ed.locator("button.rt-gacct").first().click();
    await option(page, "me@x.com").click();
    await page.waitForFunction(() => document.querySelector(".rt-gedit .rt-macct.bad"));
    await option(page, "work@corp.example").click();
    await page.waitForFunction(() => !document.querySelector(".rt-gedit .rt-macct.bad"));
    await option(page, "work@corp.example").click();
    await page.waitForFunction(() => document.querySelector(".rt-gedit .rt-macct.bad"));
    await page.keyboard.press("Escape");
    await page.waitForFunction(() => document.querySelector("#pop").hidden);
    assert.match((await chip().getAttribute("class")) || "", /\bbad\b/);
    assert.deepEqual(errors, []);
  });
}
