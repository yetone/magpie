// Run with Node's test runner and Playwright on the module path; see README.md.
// #2633 (dengtongcai): a routing group's rule picks the model and the
// reasoning it is asked for. The group's card says the rule's effort after
// its model; in the editor a rule's effort is set from its ⋯ in the app
// menu (never a native select), shows beside the model once set, is
// changed or taken off from there, is cleared when the rule turns to a
// pause, and is saved as useEffort. In every language, at a narrow width
// with nothing run past the side.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, process.env.MAGPIE_RULE_EFFORT_ASSETS || "../assets");
const models = [
  { id: "a/glm", name: "glm", providerName: "A", icon: "generic", context: 200000, ready: true },
  { id: "b/other", name: "other", providerName: "B", icon: "generic", context: 200000, ready: true },
];
const groups = () => ({
  models,
  groups: [{ id: "main", name: "Main", members: ["a/glm", "b/other"], routing: "order", ready: true,
    memberInfo: [{ id: "a/glm", ready: true, context: 200000 }, { id: "b/other", ready: true, context: 200000 }],
    rules: [{ use: "b/other", tokens: 50000, useEffort: "low" }] }],
  pools: [],
});

const words = {
  en: { title: "1. ≥ 50,000 tokens → B · other · low reasoning", low: "low reasoning", high: "high reasoning", medium: "medium reasoning", asAsked: "As the agent asks", set: "Set its reasoning", change: "Change its reasoning", toPause: "Pause the model instead", save: "Save" },
  zh: { title: "1. ≥ 50,000 tokens → B · other · low 推理强度", low: "low 推理强度", high: "high 推理强度", medium: "medium 推理强度", asAsked: "跟随 Agent", set: "设定推理强度", change: "更改推理强度", toPause: "改为暂停该模型", save: "保存" },
  "zh-TW": { low: "low 推理強度", set: "設定推理強度", change: "變更推理強度" },
  ja: { low: "low の推論", set: "推論を指定", change: "推論を変更" },
  de: { low: "low Reasoning", set: "Reasoning festlegen", change: "Reasoning ändern" },
};
const EDIT = { en: "Edit", zh: "编辑", "zh-TW": "編輯", ja: "編集", de: "Bearbeiten" };

function serve(lang, posts) {
  const state = { agents: [{ id: "claude", name: "Claude Code", path: "/test/claude", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: 0, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
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

async function open(engine, lang, width, t) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  t.after(() => browser.close());
  const page = await (await browser.newContext({ viewport: { width, height: 1400 } })).newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [], posts = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, posts));
  await page.goto("http://magpie.test/?view=groups");
  const group = page.locator(".rt-group").first();
  await group.waitFor();
  return { page, group, errors, posts };
}
// a click as the reader's: wheeled into the middle of the view first (a
// click never moves the page), then the mouse on it once it holds still
async function press(page, x) {
  // a group opening into its editor moves its height and parts for ~700ms
  // (91b0be836): where x will be is read once that has stopped
  await page.waitForFunction(() => !document.getAnimations().some((a) => a.playState === "running" && Number.isFinite(a.effect?.getComputedTiming().endTime)));
  const d = await x.evaluate((e) => { const v = document.querySelector("#view-groups");
    return Math.round(e.getBoundingClientRect().top - v.getBoundingClientRect().top - v.clientHeight / 2); });
  if (Math.abs(d) > 100) {
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
const pick = async (page, label) => {
  const li = page.locator("#list li", { hasText: label });
  await li.first().waitFor();
  await li.first().click();
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: a rule sets the reasoning its model is asked for`, async (t) => {
      const { page, group, errors, posts } = await open(engine, lang, 1100, t);
      // the group's card: the rule's effort after its model
      const title = await group.evaluate((g) => [...g.querySelectorAll("[title]")].map((e) => e.title).join("\n"));
      assert(title.includes(w.title), `the rule says its effort: ${title}`);

      await press(page, group.locator("button", { hasText: EDIT[lang] }));
      const row = page.locator(".rt-gedit .rt-rule").first();
      await row.waitFor();
      const chip = row.locator(".rt-then .rt-useeffort");
      assert.equal(await chip.textContent(), w.low);
      // picked from the app's menu: as asked, or one of the five levels
      await press(page, chip);
      assert.equal(await page.locator("select").count(), 0, "the app menu, not a native select");
      assert.equal(await page.locator("#list li").count(), 6);
      await pick(page, w.high);
      assert.equal(await row.locator(".rt-useeffort").textContent(), w.high);
      // taken off from the rule's ⋯: nothing drawn then
      await press(page, row.locator(".rt-rmore"));
      await page.locator(".row-menu .rm-item", { hasText: w.change }).click();
      await pick(page, w.asAsked);
      assert.equal(await row.locator(".rt-useeffort").count(), 0);
      // and set again from there
      await press(page, row.locator(".rt-rmore"));
      await page.locator(".row-menu .rm-item", { hasText: w.set }).click();
      await pick(page, w.medium);
      assert.equal(await row.locator(".rt-useeffort").textContent(), w.medium);

      const saveBtn = page.locator(".rt-gedit button.primary", { hasText: w.save });
      await press(page, saveBtn);
      await page.waitForFunction(() => !document.querySelector(".rt-gedit"));
      const save = posts.find((p) => p.path === "/api/groups/save");
      assert(save, `saved: ${JSON.stringify(posts)}`);
      assert.deepEqual(save.body.rules.map((r) => [r.use, r.tokens, r.useEffort]), [["b/other", 50000, "medium"]]);
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: a pause asks for no reasoning`, async (t) => {
      const { page, group, errors } = await open(engine, lang, 1100, t);
      await press(page, group.locator("button", { hasText: EDIT[lang] }));
      const row = page.locator(".rt-gedit .rt-rule").first();
      await row.waitFor();
      await press(page, row.locator(".rt-rmore"));
      await page.locator(".row-menu .rm-item", { hasText: w.toPause }).click();
      assert.equal(await row.locator(".rt-useeffort").count(), 0);
      await press(page, row.locator(".rt-rmore"));
      assert.equal(await page.locator(".row-menu .rm-item", { hasText: w.set }).count(), 0, "a pause isn't offered one");
      assert.equal(await page.locator(".row-menu .rm-item", { hasText: w.change }).count(), 0);
      await page.keyboard.press("Escape");
      assert.deepEqual(errors, []);
    });
  }
  // narrow, in every language: the effort reads in it, nothing runs past
  for (const lang of Object.keys(words)) {
    test(`${engine} ${lang}: a rule's effort at a narrow width`, async (t) => {
      const { page, group, errors } = await open(engine, lang, 420, t);
      await press(page, group.locator("button", { hasText: EDIT[lang] }));
      const row = page.locator(".rt-gedit .rt-rule").first();
      await row.waitFor();
      assert.equal(await row.locator(".rt-useeffort").textContent(), words[lang].low);
      await press(page, row.locator(".rt-rmore"));
      assert.equal(await page.locator(".row-menu .rm-item", { hasText: words[lang].change }).count(), 1);
      await page.keyboard.press("Escape");
      assert.deepEqual(await overrun(page), []);
      assert.deepEqual(errors, []);
    });
  }
}
