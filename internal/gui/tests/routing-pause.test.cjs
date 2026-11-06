// Run with Node's test runner and Playwright on the module path; see README.md.
// A routing group's rule can pause one of its models at some hours (John on
// Discord: pause a model of the group in its vendor's peak hours). A pause
// keeps the model out of the group then: not first, not a fallback. The
// group's rule reads as "→ pause <model>"; the trace tells the model was
// paused, by which rule and when; in the editor the rule's action is picked
// from the app menu, a pause shows only the conditions it holds by (hours,
// days, agents, folders), isn't saved without one, and is saved as pause:true. In
// every language the page has, at a narrow width with nothing run past
// the side.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { id: "a/glm", name: "glm", providerName: "A", icon: "generic", context: 200000, ready: true },
  { id: "b/other", name: "other", providerName: "B", icon: "generic", context: 200000, ready: true },
];
const WEEKDAYS = ["mon", "tue", "wed", "thu", "fri"];
const groups = () => ({
  models,
  groups: [{ id: "main", name: "Main", members: ["a/glm", "b/other"], routing: "order", ready: true,
    memberInfo: [{ id: "a/glm", ready: true, context: 200000 }, { id: "b/other", ready: true, context: 200000 }],
    rules: [{ use: "a/glm", pause: true, time: { from: "14:00", to: "18:00", days: WEEKDAYS } }] }],
  pools: [],
});
const now = Date.now();
const route = {
  id: 7, seq: 7, time: new Date(now - 2000).toISOString(), agent: "claude", model: "main", provider: "b", done: true, status: 200, ms: 900, tokens: 9000,
  group: { id: "main", name: "Main", members: ["b/other"], paused: [{ member: "a/glm", rule: 1, use: "a/glm", when: ["time 14:00–18:00 Mon–Fri"] }] },
  order: [{ id: "b", provider: "b", name: "B", kind: "key", model: "other", known: true }],
  tries: [{ id: "b", model: "other", start: new Date(now - 2000).toISOString(), done: true, ms: 800, status: 200 }],
};

const words = {
  en: { aside: "a/glm is paused by rule 1 (14:00–18:00 Mon–Fri), so it is left out until that ends.", title: "1. 14:00–18:00 Mon–Fri → pause A · glm", pause: "pause", send: "send to", edit: "Edit", add: "Add a rule", save: "Save", bare: "Rule 2: a pause needs hours, days, agents or folders. Add one with its +, or remove the rule from its ⋯ menu", warn: "left out of the group then: not tried, not a fallback", length: "Conversation length", hours: "Hours and days", toPause: "Pause the model instead" },
  zh: { aside: "a/glm 被规则 1（周一–周五 14:00–18:00）暂停，在此之前不会被调用。", title: "1. 周一–周五 14:00–18:00 → 暂停 A · glm", pause: "暂停", send: "先发给", edit: "编辑", add: "添加规则", save: "保存", bare: "第 2 条规则：暂停需要设时段、星期、Agent 或文件夹。用它的 + 添加，或在它的 ⋯ 菜单里删除这条规则", warn: "这段时间移出分组：不会被调用，也不做备用", length: "对话长度", hours: "时段", toPause: "改为暂停该模型" },
  "zh-TW": { pause: "暫停", send: "先發給", edit: "編輯", add: "新增規則", save: "儲存" },
  ja: { pause: "一時停止", send: "送信先" },
  de: { pause: "pausieren", send: "senden an" },
};

function serve(lang, posts) {
  const state = { agents: [{ id: "claude", name: "Claude Code", path: "/test/claude", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  let first = true;
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      const routes = first ? [route] : [];
      first = false;
      return json({ mine: true, now: new Date().toISOString(), seq: 7, totals: { requests: 1, rerouted: 0, errors: 0 }, routes });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json(groups());
    if (url.pathname.startsWith("/api/groups/")) {
      posts.push({ path: url.pathname, body: JSON.parse(r.request().postData() || "{}") });
      return json(groups());
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const EDIT = { en: "Edit", zh: "编辑", "zh-TW": "編輯", ja: "編集", de: "Bearbeiten" };

async function open(engine, lang, width, t) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  t.after(() => browser.close());
  const page = await (await browser.newContext({ viewport: { width, height: 1400 }, timezoneId: "Asia/Tokyo" })).newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [], posts = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, posts));
  await page.goto("http://magpie.test/?view=routing");
  const group = page.locator(".rt-group").first();
  await group.waitFor({ state: "attached" });
  return { page, group, errors, posts };
}
// a click as the reader's: wheeled into the middle of the view first (a
// click never moves the page, so Playwright's own scroll is put back), then
// the mouse on it once it holds still
async function press(page, x) {
  // a group opening into its editor moves its height and parts for ~700ms
  // (91b0be836): where x will be is read once that has stopped
  await page.waitForFunction(() => !document.getAnimations().some((a) => a.playState === "running" && Number.isFinite(a.effect?.getComputedTiming().endTime)));
  const d = await x.evaluate((e) => { const v = document.querySelector("#view-groups");
    return Math.round(e.getBoundingClientRect().top - v.getBoundingClientRect().top - v.clientHeight / 2); });
  if (Math.abs(d) > 100) {
    // over the view, not the sidebar beside it
    const v = await page.locator("#view-groups").boundingBox();
    await page.mouse.move(v.x + 10, 300);
    await page.mouse.wheel(0, d);
    await page.waitForTimeout(400);
  }
  await page.waitForTimeout(300); // the page settled (WebKit's folds)
  const b = await x.boundingBox();
  await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
}
// nothing in the rules runs past the editor's side
const overrun = (page) => page.evaluate(() => {
  const ed = document.querySelector(".rt-gedit").getBoundingClientRect();
  return [...document.querySelectorAll(".rt-rule, .rt-rule *")].filter((e) => e.offsetParent && e.getBoundingClientRect().right > ed.right + 1).map((e) => e.className || e.tagName);
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: a rule that pauses a model at some hours`, async (t) => {
      const { page, group, errors, posts } = await open(engine, lang, 1100, t);

      // the trace: the model paused, by which rule and when
      await page.waitForFunction((s) => document.documentElement.innerText.includes(s), w.aside);
      await page.evaluate(() => window.show("groups"));
      await group.waitFor();

      // the group's rule, in words
      const title = await group.evaluate((g) => [...g.querySelectorAll("[title]")].map((e) => e.title).join("\n"));
      assert(title.includes(w.title), `the rule reads as a pause: ${title}`);

      // the editor: the pause rule shows its action, and only hours, days, agents
      await press(page, group.locator("button", { hasText: w.edit }));
      const ed = page.locator(".rt-gedit");
      const rows = ed.locator(".rt-rule");
      await rows.first().waitFor();
      const act1 = rows.first().locator(".rt-act");
      assert.equal(await act1.textContent(), w.pause);
      assert.equal(await rows.first().locator(".rt-cond.tk").isVisible(), false, "no tokens on a pause");
      assert.equal(await rows.first().locator(".rt-cond.in").isVisible(), false, "no intent on a pause");
      assert.equal(await rows.first().locator(".rt-cond.tm").getAttribute("class"), "rt-cond tm on");
      assert.equal(await rows.first().locator(".rt-rwarn").textContent(), w.warn);

      // a second rule: a send-to rule (sending goes unsaid) turned into a
      // pause from its ⋯ menu
      const addBtn = ed.locator("button", { hasText: w.add });
      await press(page, addBtn);
      const row2 = rows.nth(1);
      assert.equal(await row2.locator(".rt-act").count(), 0, "sending is the plain case");
      // a condition set before it turns to a pause is cleared, not kept out of sight
      await row2.locator(".rt-addc").click();
      assert.equal(await page.locator("select").count(), 0, "the app menu, not a native select");
      await page.locator(".proto-menu .pm-item", { hasText: w.length }).click();
      await row2.locator(".rt-cond.tk input").fill("50000");
      assert.equal(await row2.locator(".rt-cond.tk").isVisible(), true);
      await press(page, row2.locator(".rt-rmore"));
      await page.locator(".row-menu .rm-item", { hasText: w.toPause }).click();
      const row2b = rows.nth(1);
      assert.equal(await row2b.locator(".rt-act").textContent(), w.pause);
      assert.equal(await row2b.locator(".rt-cond.tk").count(), 0);
      assert.equal(await row2b.locator(".rt-cond.in").count(), 0, "nor the intent it was added with");
      // what a pause can't hold isn't offered for it
      await row2b.locator(".rt-addc").click();
      assert.equal(await page.locator(".proto-menu .pm-item", { hasText: w.length }).count(), 0);
      await page.locator(".proto-menu .pm-item", { hasText: w.hours }).click();
      assert.equal(await row2b.locator(".rt-cond.tm").isVisible(), true);

      // not saved without hours, days or agents
      const saveBtn = ed.locator("button.primary", { hasText: w.save });
      await press(page, saveBtn);
      await page.waitForFunction(() => document.querySelector("#status").textContent).catch(() => {});
      assert.equal(await page.locator("#status").textContent(), w.bare, JSON.stringify(posts));
      assert.equal(posts.filter((p) => p.path === "/api/groups/save").length, 0);

      // its hours typed: saved as pause:true, the tokens it had gone
      const tm2 = row2b.locator(".rt-cond.tm");
      await tm2.locator("input").nth(0).fill("09:00");
      await tm2.locator("input").nth(1).fill("12:00");
      await tm2.locator("input").nth(1).blur();
      await press(page, saveBtn);
      await page.waitForFunction(() => !document.querySelector(".rt-gedit"));
      const save = posts.find((p) => p.path === "/api/groups/save");
      assert(save, `saved: ${JSON.stringify(posts)}`);
      assert.deepEqual(save.body.rules.map((r) => [r.use, !!r.pause, r.tokens || 0, r.time]), [
        ["a/glm", true, 0, { from: "14:00", to: "18:00", days: WEEKDAYS }],
        // hours set here are in the browser's zone, saved with them (c888ef272)
        ["b/other", true, 0, { from: "09:00", to: "12:00", days: [], zone: "Asia/Tokyo" }],
      ]);
      assert.deepEqual(errors, []);
    });
  }
  // narrow, in every language: the action reads in it, nothing runs past
  for (const lang of Object.keys(words)) {
    test(`${engine} ${lang}: a pause rule at a narrow width`, async (t) => {
      const { page, group, errors } = await open(engine, lang, 420, t);
      await page.evaluate(() => window.show("groups"));
      await group.waitFor();
      await press(page, group.locator("button", { hasText: EDIT[lang] }));
      const row = page.locator(".rt-gedit .rt-rule").first();
      await row.waitFor();
      assert.equal(await row.locator(".rt-act").textContent(), words[lang].pause);
      await press(page, row.locator(".rt-act"));
      const li = page.locator("#list li");
      assert.equal(await li.count(), 2);
      assert(await li.filter({ hasText: words[lang].send }).count() >= 1, "send to offered");
      await page.keyboard.press("Escape");
      assert.deepEqual(await overrun(page), []);
      assert.deepEqual(errors, []);
    });
  }
}
