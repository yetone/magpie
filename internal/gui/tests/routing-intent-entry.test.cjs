// Run with Node's test runner and Playwright on the module path; see README.md.
// A routing group's rule by intent has an entry of its own (CherryL1quor on
// X took Jev for picking the effort only: the intent was one of a rule's
// seven conditions). Beside "Add a rule", "Route by what it asks for" adds a
// rule with its intent box focused and Jev picked as the classifier when
// there is one, the button kept under the pointer as the rule comes in
// above it; it saves as {use, intent} with the classifier. A group whose effort Jev picks offers, under the classifier,
// to add a rule with an intent. At a narrow window both buttons stay inside
// the editor. In English and Chinese.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { id: "a/opus", name: "opus", providerName: "A", icon: "generic", context: 1000000, ready: true },
  { id: "b/flash", name: "flash", providerName: "B", icon: "generic", context: 128000, ready: true },
];
const member = (id, context) => ({ id, ready: true, context });
const groups = () => ({
  models,
  deciders: [{ id: "typesafe/jev-latest", name: "Jev", providerName: "TypeSafe", icon: "generic" }],
  groups: [
    { id: "main", name: "Main", members: ["a/opus", "b/flash"], routing: "order", ready: true, memberInfo: [member("a/opus", 1000000), member("b/flash", 128000)] },
    { id: "hard", name: "Hard", members: ["a/opus", "b/flash"], routing: "order", ready: true, effort: "auto", classifier: "typesafe/jev-latest", memberInfo: [member("a/opus", 1000000), member("b/flash", 128000)] },
  ],
  pools: [],
});

const words = {
  en: { edit: "Edit", add: "Add a rule", intent: "Route by what it asks for", more: "It can pick the model too: add a rule with an intent", told: "Intent told by", save: "Save" },
  zh: { edit: "编辑", add: "添加规则", intent: "按意图分流", more: "它也能挑模型：添加一条带意图的规则", told: "意图判断模型", save: "保存" },
};

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
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
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

// wheel down to a control, as the reader would (the page keeps still for
// any other scroll), then click it with the mouse
async function press(page, loc, height) {
  await page.mouse.move(Math.min(550, (page.viewportSize().width / 2) | 0), 300);
  for (let i = 0; i < 12 && (await loc.boundingBox()).y > height - 160; i++) {
    await page.mouse.wheel(0, 300);
    await page.waitForTimeout(150);
  }
  const b = await loc.boundingBox();
  await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    for (const width of [1100, 440]) {
      test(`${engine} ${lang} ${width}: a rule by intent has its own entry`, async (t) => {
        const height = 1400;
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height } })).newPage();
        page.setDefaultTimeout(5000);
        const errors = [], posts = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, posts));
        await page.goto("http://magpie.test/?view=routing");
        const group = page.locator(".rt-group").first();
        await group.waitFor();

        await press(page, group.locator("button", { hasText: w.edit }), height);
        const ed = page.locator(".rt-gedit");
        const add = ed.locator(".rt-gadds button", { hasText: w.add });
        const byIntent = ed.locator(".rt-gadds button", { hasText: w.intent });
        await byIntent.waitFor();
        assert(await add.isVisible(), "Add a rule is there too");
        // both inside the editor, however narrow
        const eb = await ed.boundingBox();
        for (const b of [await add.boundingBox(), await byIntent.boundingBox()]) assert(b.x >= eb.x - 0.5 && b.x + b.width <= eb.x + eb.width + 0.5, `${JSON.stringify(b)} in ${JSON.stringify(eb)}`);
        // no classifier yet: no rule has an intent
        assert.equal(await ed.locator(".rt-classifier").isVisible(), false);

        // a rule, its intent box focused, Jev picked; nothing scrolled
        await page.mouse.move(Math.min(550, width / 2), 300);
        for (let i = 0; i < 12 && (await byIntent.boundingBox()).y > height - 160; i++) {
          await page.mouse.wheel(0, 300);
          await page.waitForTimeout(150);
        }
        const bb = await byIntent.boundingBox();
        await page.mouse.click(bb.x + bb.width / 2, bb.y + bb.height / 2);
        const rule = ed.locator(".rt-rule").first();
        await rule.waitFor();
        await page.waitForTimeout(300);
        // what was clicked stays under the pointer as the rule comes in
        // above it, and the rule shows in the window
        const after = await byIntent.boundingBox();
        assert(Math.abs(after.y - bb.y) < 1, `the button moved from ${bb.y} to ${after.y}`);
        const rb = await rule.boundingBox();
        assert(rb.y >= 0 && rb.y + rb.height <= height, `the rule in view: ${JSON.stringify(rb)}`);
        const box = rule.locator(".rt-cond.in input");
        assert(await box.evaluate((b) => b === document.activeElement), "the intent box has the focus");
        const cls = ed.locator(".rt-classifier");
        assert(await cls.isVisible(), "the classifier shows for the rule");
        assert.match(await cls.locator("button.rt-cond").first().textContent(), /Jev/);
        assert(await ed.evaluate((e, s) => e.innerText.includes(s), w.told), "labelled as telling the intent");

        await page.keyboard.type("a quick question");
        await press(page, ed.locator("button.primary", { hasText: w.save }), height);
        await page.waitForFunction(() => !document.querySelector(".rt-gedit"));
        const save = posts.find((p) => p.path === "/api/groups/save");
        assert(save, `saved: ${JSON.stringify(posts)}`);
        assert.deepEqual(save.body.rules.map((r) => [r.use, r.intent]), [["b/flash", "a quick question"]]);
        assert.equal(save.body.classifier, "typesafe/jev-latest");

        // Jev picking the effort alone offers to pick the model too
        const hard = page.locator(".rt-group").nth(1);
        await press(page, hard.locator("button", { hasText: w.edit }), height);
        await ed.waitFor();
        const more = ed.locator(".rt-classifier button", { hasText: w.more });
        await more.waitFor();
        const mb = await more.boundingBox(), eb2 = await ed.boundingBox();
        assert(mb.x + mb.width <= eb2.x + eb2.width + 0.5, "the offer wraps inside the editor");
        await press(page, more, height);
        await ed.locator(".rt-rule").first().waitFor();
        assert(await ed.locator(".rt-rule .rt-cond.in input").first().evaluate((b) => b === document.activeElement), "its intent box has the focus");
        assert.equal(await more.count(), 0, "offered no more once a rule has an intent");
        assert.deepEqual(errors, []);
      });
    }
  }
}
