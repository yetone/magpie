// Run with Node's test runner and Playwright on the module path; see README.md.
// yetone, 10-10: a routing group took the whole editor to make. Templates
// make one of the user's own models in a click: with no group of the
// user's yet, the Routing page shows them as cards, each with the models
// it takes, Add and Edit first; one that can't be made says what it needs
// and has no buttons. Add posts groups/template with the kind and its name
// in the reader's language; once the user has a group, the cards give way
// to a Templates menu by New group. Edit first opens the editor with the
// template in it. 10-14: a pick from the menu does the same, filled in and
// brought into view, not a group added out of sight. At 420px, in every
// language, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { id: "a/big", name: "Big", providerName: "A", icon: "generic" },
  { id: "a/mini", name: "Mini", providerName: "A", icon: "generic" },
];
const info = (ids) => ids.map((id) => ({ id, ready: true }));
const HARD = "a hard task: designing, debugging, a change across several files, or planning";
const EASY = "an easy task: a quick question, a small edit, a lookup, or running a command";
const templates = [
  { kind: "smart", group: { id: "smart-split", name: "Hard to strong, easy to cheap", members: ["a/big", "a/mini"], routing: "order", classifier: "a/mini",
    rules: [{ use: "a/mini", compact: true }, { use: "a/big", intent: HARD }, { use: "a/mini", intent: EASY }] } },
  { kind: "thrifty", group: { id: "cheap-first", name: "Cheap first", members: ["a/mini", "a/big"], routing: "order", rules: [{ use: "a/big", effort: "high" }] } },
  { kind: "steady", group: { id: "never-stuck", name: "Never stuck", routing: "order" }, why: "it needs models from two providers: A is the only one", need: "providers", of: "A" },
  { kind: "plan", group: { id: "plan-first", name: "Plan first", routing: "order" }, why: "no provider of yours is billed by a plan", need: "plan" },
  { kind: "fast", group: { id: "fastest-first", name: "Fastest first", members: ["a/big", "a/mini"], routing: "fast" } },
  { kind: "even", group: { id: "spread-evenly", name: "Spread evenly", members: ["a/big"], routing: "rotate" } },
];

const words = {
  en: { smart: "Hard to strong, easy to cheap", thrifty: "Cheap first", steady: "Never stuck", add: "Add", edit: "Edit first", added: "Added Hard to strong, easy to cheap", menu: "Templates", need: "Needs models from two providers: A is the only one", plan: "Plan first", fast: "Fastest first", even: "Spread evenly", planNeed: "Needs a subscription or token plan" },
  zh: { smart: "难题给强模型，简单的给便宜模型", thrifty: "便宜优先", steady: "永不卡住", add: "添加", edit: "先编辑", added: "已添加 难题给强模型，简单的给便宜模型", menu: "模板", need: "需要两家供应商的模型：目前只有 A", plan: "计划优先", fast: "最快优先", even: "均匀分担", planNeed: "需要一个订阅或 Token 套餐" },
  "zh-TW": { smart: "難題給強模型，簡單的給便宜模型", thrifty: "便宜優先", steady: "永不卡住", add: "新增", edit: "先編輯", added: "已新增 難題給強模型，簡單的給便宜模型", menu: "範本", need: "需要兩家供應商的模型：目前只有 A", plan: "方案優先", fast: "最快優先", even: "平均分擔", planNeed: "需要一個訂閱或 Token 方案" },
  ja: { smart: "難しい作業は強いモデル、簡単な作業は安いモデルへ", thrifty: "安いモデル優先", steady: "止まらない", add: "追加", edit: "先に編集", added: "難しい作業は強いモデル、簡単な作業は安いモデルへ を追加しました", menu: "テンプレート", need: "2 つのプロバイダーのモデルが必要です：今は A だけです", plan: "プラン優先", fast: "最速優先", even: "均等に分散", planNeed: "サブスクリプションまたはトークンプランが必要です" },
  de: { smart: "Schweres zum starken, Leichtes zum günstigen Modell", thrifty: "Günstig zuerst", steady: "Nie blockiert", add: "Hinzufügen", edit: "Erst bearbeiten", added: "Schweres zum starken, Leichtes zum günstigen Modell hinzugefügt", menu: "Vorlagen", need: "Braucht Modelle von zwei Anbietern: A ist der einzige", plan: "Tarif zuerst", fast: "Schnellster zuerst", even: "Gleichmäßig verteilen", planNeed: "Braucht ein Abo oder einen Token-Tarif" },
};
const KEYS = ["Hard to strong, easy to cheap", "A hard task goes to the strongest model, an easy one and compaction to a cheap one", "Cheap first",
  "A cheap model first; the strongest for high reasoning, or when the cheap ones fail", "Never stuck",
  "The strongest model of each provider, each behind the other: one down doesn't stop the agent", "Needs models from two providers: {name} is the only one",
  "Needs a model cheap beside {name}", "Needs a subscription or token plan", "Plan first", "Subscriptions and token plans first, pay-as-you-go keys only after them", "Fastest first", "Whichever provider has answered fastest lately goes first", "Spread evenly", "The strongest model, taken in turn over every provider and account that serves it", "Add a provider first", "Added {name}", "Start from a template: made of your own models, added in a click", "Edit first", "Templates"];

function serve(lang, posts, mine = 0) {
  // a group magpie found is no group of the user's: the cards still show
  let list = [{ id: "auto-big", name: "Big", members: ["a/big"], ready: true, auto: true, memberInfo: info(["a/big"]) },
    ...Array.from({ length: mine }, (_, i) => ({ id: `mine-${i}`, name: `Mine ${i}`, members: ["a/big", "a/mini"], routing: "order", ready: true, memberInfo: info(["a/big", "a/mini"]) }))];
  const groups = () => ({ models, pools: [], deciders: [], found: true, groups: list, templates });
  const state = { agents: [{ id: "claude", name: "Claude Code", path: "/test/claude", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {});
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(groups());
    if (url.pathname === "/api/groups/template") {
      const body = JSON.parse(r.request().postData() || "{}");
      posts.push(body);
      const tp = templates.find((x) => x.kind === body.kind).group;
      list = [...list, { ...structuredClone(tp), id: tp.id + (list.some((g) => g.id === tp.id) ? "-2" : ""), name: body.name, ready: true, memberInfo: info(tp.members) }];
      return json(groups());
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    const w = words[lang];
    test(`${engine} ${lang}: a routing group is added from a template in a click`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 420, height: 1400 }, reducedMotion: "reduce" })).newPage();
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-group-template.png`), fullPage: true });
        }
        await browser.close();
      });
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], posts = [];
      // WebKit's notice when a layout outruns its observers, not a fault
      page.on("pageerror", (e) => { if (!/^ResizeObserver loop completed with undelivered notifications/.test(e.message)) errors.push(e.message); });
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=groups");
      const cards = page.locator(".rt-gtpl-card");
      await cards.nth(5).waitFor();
      await page.waitForTimeout(500);
      assert.deepEqual(await cards.evaluateAll((cs) => cs.map((c) => c.dataset.kind)), ["smart", "thrifty", "steady", "plan", "fast", "even"]);
      const smart = page.locator(".rt-gtpl-card[data-kind=smart]"), steady = page.locator(".rt-gtpl-card[data-kind=steady]");
      assert.equal((await smart.locator("b").textContent()).trim(), w.smart);
      assert.equal((await smart.locator(".mem").textContent()).trim(), "Big → Mini");
      // one that can't be made says what it needs, and offers nothing
      assert.equal((await steady.locator(".why").textContent()).trim(), w.need);
      assert.equal(await steady.locator("button").count(), 0);
      // the recipes: named in the reader's language, one needing a plan says so
      assert.deepEqual(await page.locator(".rt-gtpl-card[data-kind=plan] b, .rt-gtpl-card[data-kind=fast] b, .rt-gtpl-card[data-kind=even] b").allTextContents(), [w.plan, w.fast, w.even]);
      assert.equal((await page.locator(".rt-gtpl-card[data-kind=plan] .why").textContent()).trim(), w.planNeed);
      assert.equal((await page.locator(".rt-gtpl-card[data-kind=fast] .mem").textContent()).trim(), "Big → Mini");
      // every card whole on a narrow window, nothing scrolls sideways
      for (const box of await cards.evaluateAll((cs) => cs.map((c) => c.getBoundingClientRect().toJSON()))) {
        assert(box.x >= 0 && box.right <= 420, `card off the window: ${JSON.stringify(box)}`);
      }
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "the page scrolls sideways");
      const at = () => page.evaluate(() => [...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => [e.id || e.className, e.scrollTop]).join(";") + "|" + scrollX);
      const before = await at();

      await smart.getByRole("button", { name: w.add, exact: true }).click();
      await page.waitForFunction((m) => document.querySelector("#status").textContent.includes(m), w.added);
      assert.deepEqual(posts, [{ kind: "smart", name: w.smart }]);
      await page.locator(".rt-group[data-id=smart-split]").waitFor();
      assert.equal(await page.locator(".rt-gedit").count(), 0, "adding opened the editor");
      assert.equal(await at(), before, "a click moved the page");
      // the user has a group now: a menu by New group, not the cards
      assert.equal(await cards.count(), 0);
      const btn = page.locator(".rt-gtplbtn");
      assert.equal((await btn.textContent()).trim(), w.menu);
      await page.mouse.move(1, 1);
      await btn.click();
      const menu = page.locator(".row-menu");
      await menu.waitFor();
      const box = await menu.boundingBox();
      assert(box.x >= 0 && box.x + box.width <= 420, `menu off the window: ${JSON.stringify(box)}`);
      assert.equal(await menu.getByRole("menuitem").nth(2).isDisabled(), true, "steady can't be made");
      await menu.getByRole("menuitem", { name: w.thrifty }).click();
      const ed = page.locator(".rt-gedit");
      await ed.waitFor();
      assert.equal(await ed.locator("input").first().inputValue(), w.thrifty);
      assert.equal(posts.length, 1, "picking a template added it unseen");

      if (lang !== "en") {
        const missing = await page.evaluate(([l, ks]) => ks.filter((k) => !I18N[l][k]), [lang, KEYS]);
        assert.deepEqual(missing, [], `every string has its ${lang}`);
      }
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: a template picked from the menu opens filled in, in view`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 420, height: 900 }, reducedMotion: "reduce" })).newPage();
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-group-template-menu.png`) });
        await browser.close();
      });
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], posts = [];
      // WebKit's notice when a layout outruns its observers, not a fault
      page.on("pageerror", (e) => { if (!/^ResizeObserver loop completed with undelivered notifications/.test(e.message)) errors.push(e.message); });
      // enough groups of the user's that the editor, last in the list,
      // opens below the window
      await page.route("**/*", serve(lang, posts, 12));
      await page.goto("http://magpie.test/?view=groups");
      const btn = page.locator(".rt-gtplbtn");
      await btn.waitFor();
      await page.waitForTimeout(500);
      await page.mouse.move(1, 1);
      await btn.click();
      await page.locator(".row-menu").getByRole("menuitem", { name: w.thrifty }).click();
      const ed = page.locator(".rt-gedit");
      await ed.waitFor();
      assert.equal(await ed.locator("input").first().inputValue(), w.thrifty);
      assert.deepEqual(posts, [], "picking a template added it unseen");
      await page.waitForFunction(() => {
        const r = document.querySelector(".rt-gedit").getBoundingClientRect();
        return r.top >= 0 && r.top < innerHeight - 60;
      }, null, { timeout: 3000 }).catch(() => assert.fail("the editor the pick opened is out of sight"));
      assert.equal(await page.evaluate(() => document.activeElement === document.querySelector(".rt-gedit input")), true, "the template's name isn't focused");
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: Edit first opens a template in the editor`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 420, height: 1400 }, reducedMotion: "reduce" })).newPage();
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-group-template-edit.png`), fullPage: true });
        await browser.close();
      });
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      // WebKit's notice when a layout outruns its observers, not a fault
      page.on("pageerror", (e) => { if (!/^ResizeObserver loop completed with undelivered notifications/.test(e.message)) errors.push(e.message); });
      await page.route("**/*", serve(lang, []));
      await page.goto("http://magpie.test/?view=groups");
      const smart = page.locator(".rt-gtpl-card[data-kind=smart]");
      await smart.waitFor();
      if (process.env.ARTIFACT_DIR) await page.locator(".rt-gtpl").screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-group-template-cards.png`) });
      await smart.getByRole("button", { name: w.edit, exact: true }).click();
      const ed = page.locator(".rt-gedit");
      await ed.waitFor();
      assert.equal(await ed.locator("input").first().inputValue(), w.smart);
      // its models and its intents are in the editor, as in the template
      const text = await ed.evaluate((e) => e.textContent + [...e.querySelectorAll("input, textarea")].map((i) => i.value).join("\n"));
      for (const s of ["Big", "Mini", HARD, EASY]) assert(text.includes(s), `editor lacks ${s}`);
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "the page scrolls sideways");
      assert.deepEqual(errors, []);
    });
  }
}
