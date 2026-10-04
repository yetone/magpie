// Run with Node's test runner and Playwright on the module path; see README.md.
// A routing group's rule may be for the agent compacting its conversation
// (MR.Guo on X: a model of its own for context compression, cheaper and
// faster than the one the conversation is on). A group's rule for it reads
// "compacting"; in the editor it is a condition of its own, toggled with
// nothing moved, a rule with only it isn't refused as having none, and it
// is saved as compact:true; a member with less room than another is noted;
// a compaction in the trace is told as one, the models passed over named.
// In English and Chinese.
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
const groups = () => ({
  models,
  groups: [{ id: "main", name: "Main", members: ["a/opus", "b/flash"], routing: "order", ready: true,
    memberInfo: [{ id: "a/opus", ready: true, context: 1000000 }, { id: "b/flash", ready: true, context: 128000 }], rules: [{ use: "b/flash", compact: true }] }],
  pools: [],
});
const now = Date.now();
const route = {
  id: 7, seq: 7, time: new Date(now - 2000).toISOString(), agent: "claude", model: "main", provider: "b", done: true, status: 200, ms: 900, tokens: 90000,
  group: { id: "main", name: "Main", members: ["a/opus", "b/flash"] },
  order: [{ id: "b", provider: "b", name: "B", kind: "key", model: "flash", known: true }, { id: "a", provider: "a", name: "A", kind: "key", model: "opus", known: true }],
  tries: [{ id: "b", model: "flash", start: new Date(now - 2000).toISOString(), done: true, ms: 800, status: 200 }],
  rule: { n: 1, use: "b/flash", when: ["compacting"], turn: 3, tokens: 90000, compact: true, small: ["c/mini"] },
};

const words = {
  en: { cond: "compacting", story: "The agent is compacting the conversation, so rule 1 sends the summary to", small: "Passed over, as the conversation is longer than they take: c/mini.", skip: "longer conversations skip it: it takes 128,000 tokens", edit: "Edit", add: "Add a rule", save: "Save" },
  zh: { cond: "压缩上下文时", story: "Agent 正在压缩上下文，规则 1 把这次总结交给", small: "对话比它们的上下文长，已跳过：c/mini。", skip: "更长的对话会跳过它：它的上下文只有 128,000 tokens", edit: "编辑", add: "添加规则", save: "保存" },
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

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: a rule for compacting`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 1100, height: 1400 } })).newPage();
      page.setDefaultTimeout(5000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=routing");
      const group = page.locator(".rt-group").first();
      await group.waitFor();

      // the compaction in the trace, told as one
      await page.waitForFunction((s) => document.documentElement.innerText.includes(s), w.story);
      const said = await page.evaluate(() => document.documentElement.innerText);
      assert(said.includes(w.small), "the model passed over is named");

      // the group's rule, in words
      const title = await group.evaluate((g) => [...g.querySelectorAll("[title]")].map((e) => e.title).join("\n"));
      assert(title.includes(`1. ${w.cond} →`), `the rule reads as compacting: ${title}`);

      // the editor: the rule's condition on, and the note on its room
      await group.locator("button", { hasText: w.edit }).click();
      const ed = page.locator(".rt-gedit");
      const rows = ed.locator(".rt-rule");
      await rows.first().waitFor();
      const cp = rows.first().locator("button.rt-cond", { hasText: w.cond });
      assert.equal(await cp.getAttribute("class"), "rt-cond on");
      assert.equal(await rows.first().locator(".rt-rwarn").textContent(), w.skip);

      // a second rule, compacting alone: toggled with nothing moved
      // (wheeled down to, as the reader would: the editor runs past the
      // window and the page keeps still for any other scroll)
      const add = ed.locator("button", { hasText: w.add });
      await page.mouse.move(550, 600);
      for (let i = 0; i < 10 && (await add.boundingBox()).y > 1400 - 160; i++) {
        await page.mouse.wheel(0, 300);
        await page.waitForTimeout(150);
      }
      const ab = await add.boundingBox();
      await page.mouse.click(ab.x + ab.width / 2, ab.y + ab.height / 2);
      const second = rows.nth(1).locator("button.rt-cond", { hasText: w.cond });
      await second.waitFor();
      const at = () => page.evaluate(() => [...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => [e.id || e.className, e.scrollTop]).join(";") + "|" + Math.round(document.querySelector(".rt-gedit").getBoundingClientRect().top));
      await second.scrollIntoViewIfNeeded(); // by the test, as the reader would
      await page.waitForTimeout(200);
      const before = await at();
      await second.click();
      assert.equal(await second.getAttribute("class"), "rt-cond on");
      assert.equal(await at(), before, "the click moved nothing");
      await second.click();
      assert.equal(await second.getAttribute("class"), "rt-cond");
      await second.click();

      // saved: compact:true, neither rule refused as having no condition
      const saveBtn = ed.locator("button.primary", { hasText: w.save });
      // (the view wheeled down to it, as the page keeps still for any
      // other scroll, and the mouse where it is: the browsers' scroll
      // into view before a click can leave it under the footer)
      await page.mouse.move(550, 600);
      for (let i = 0; i < 10 && (await saveBtn.boundingBox()).y > 1400 - 160; i++) {
        await page.mouse.wheel(0, 300);
        await page.waitForTimeout(150);
      }
      const sb = await saveBtn.boundingBox();
      await page.mouse.click(sb.x + sb.width / 2, sb.y + sb.height / 2);
      await page.waitForFunction(() => !document.querySelector(".rt-gedit"));
      const save = posts.find((p) => p.path === "/api/groups/save");
      assert(save, `saved: ${JSON.stringify(posts)}`);
      assert.deepEqual(save.body.rules.map((r) => [r.use, r.compact]), [["b/flash", true], ["b/flash", true]]);
      assert.deepEqual(errors, []);
    });
  }
}
