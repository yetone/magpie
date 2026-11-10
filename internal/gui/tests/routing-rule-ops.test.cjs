// Run with Node's test runner and Playwright on the module path; see README.md.
// aipha2025 on X: a routing group's rule compares by a field, a way to
// compare and a value: tokens ≥ > < ≤, reasoning ≥ ≤ is / is not (off,
// on or a level), agent is / is not, a subagent's turn or not, and the
// message contains, doesn't contain, starts with or matches. Each way to
// compare is picked in the app's menu (never a native select), inside the
// rule's own pills; a rule saved before keeps its fields as they were,
// and one set to compare another way is saved as provider.Cond in where.
// The group's card says the conditions. In every language, at a narrow
// width with nothing run past the side.
// SucroseMoE on X: a rule matches the system prompt or the last user
// message as the agent sent them, by words or a regex, so a harness's
// title or compaction call is routed with no model asked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, process.env.MAGPIE_RULE_OPS_ASSETS || "../assets");
const models = [
  { id: "a/glm", name: "glm", providerName: "A", icon: "generic", context: 200000, ready: true },
  { id: "b/other", name: "other", providerName: "B", icon: "generic", context: 200000, ready: true },
];
const RULES = [
  { use: "b/other", tokens: 50000 },
  { use: "b/other", where: [{ field: "effort", op: "not", value: "high" }] },
  { use: "b/other", agents: ["claude"] },
  { use: "b/other", images: true },
];
// every field at once, for the narrow width
const ALL = [{ use: "b/other", where: [
  { field: "tokens", op: "lt", value: "20000" }, { field: "effort", op: "not", value: "off" },
  { field: "agent", op: "not", value: "codex" }, { field: "subagent", op: "is" },
  { field: "message", op: "contains", value: "run the tests" },
  { field: "system", op: "matches", value: "^你是对话标题命名" }, { field: "last", op: "starts", value: "正在上下文压缩" },
] }];
// a title call told by its system prompt, and a rule to add the rest to
const SYS = [
  { use: "b/other", where: [{ field: "system", op: "matches", value: "^你是对话标题命名" }] },
  { use: "b/other", tokens: 1000 },
];
const groups = (rules) => ({
  models,
  groups: [{ id: "main", name: "Main", members: ["a/glm", "b/other"], routing: "order", ready: true,
    memberInfo: [{ id: "a/glm", ready: true, context: 200000 }, { id: "b/other", ready: true, context: 200000 }], rules }],
  pools: [],
});

const words = {
  en: { card: "2. reasoning is not high → B · other", lt: "<", isNot: "≠", agentNot: "agent is none of", message: "Message text", subagent: "Subagent",
    startsWith: "starts with the words", notSub: "not sent by a subagent", sub: "sent by a subagent", contains: "has the words", save: "Save", add: "Add a rule",
    system: "System prompt text", last: "Last user message", matches: "matches the expression", sysCard: "system prompt matches the expression “^你是对话标题命名”" },
  zh: { card: "2. 推理不是 high → B · other", lt: "<", isNot: "≠", agentNot: "Agent 不是", message: "消息内容", subagent: "子代理",
    startsWith: "开头是", notSub: "不是子代理发出", sub: "子代理发出", contains: "包含", save: "保存", add: "添加规则",
    system: "系统提示词内容", last: "最后一条用户消息", matches: "匹配", sysCard: "系统提示词匹配“^你是对话标题命名”" },
  "zh-TW": { isNot: "≠", agentNot: "Agent 不是", contains: "包含", sub: "子代理發出", matches: "符合", startsWith: "開頭是" },
  ja: { isNot: "≠", agentNot: "エージェント ≠", contains: "を含む", sub: "サブエージェントから", matches: "にマッチ", startsWith: "で始まる" },
  de: { isNot: "≠", agentNot: "Agent ist nicht", contains: "enthält", sub: "von einem Subagent", matches: "passt auf", startsWith: "beginnt mit" },
};
const EDIT = { en: "Edit", zh: "编辑", "zh-TW": "編輯", ja: "編集", de: "Bearbeiten" };

function serve(lang, posts, rules) {
  const state = { agents: [{ id: "claude", name: "Claude Code", path: "/test/claude", fields: [] }, { id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
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
    if (url.pathname === "/api/usage") return json({ agents: [] });
    if (url.pathname === "/api/groups") return json(groups(rules));
    if (url.pathname.startsWith("/api/groups/")) {
      posts.push({ path: url.pathname, body: JSON.parse(r.request().postData() || "{}") });
      return json(groups(rules));
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

async function open(engine, lang, width, t, rules = RULES) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  t.after(() => browser.close());
  const page = await (await browser.newContext({ viewport: { width, height: 1400 } })).newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [], posts = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, posts, rules));
  await page.goto("http://magpie.test/?view=groups");
  const group = page.locator(".rt-group").first();
  await group.waitFor();
  return { page, group, errors, posts };
}
// a click as the reader's: wheeled into the middle of the view first (a
// click never moves the page), then the mouse on it once it holds still
async function press(page, x, still) {
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
  const before = still && await scrolled(page);
  const b = await x.boundingBox();
  await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
  if (still) {
    await page.waitForTimeout(300);
    assert.equal(await scrolled(page), before, "a click never moves the page");
  }
}
const overrun = (page) => page.evaluate(() => {
  const ed = document.querySelector(".rt-gedit").getBoundingClientRect();
  return [...document.querySelectorAll(".rt-rule, .rt-rule *")].filter((e) => e.offsetParent && e.getBoundingClientRect().right > ed.right + 1).map((e) => e.className || e.tagName);
});
const pick = async (page, label) => {
  const li = page.locator("#list li", { hasText: label });
  await li.first().waitFor();
  await li.first().click();
};
// the scroll of every scroller, to tell that a click moved nothing
const scrolled = (page) => page.evaluate(() => [...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => `${e.id || e.className}:${e.scrollTop}`).join(";"));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: a rule compares by field, op and value`, async (t) => {
      const { page, group, errors, posts } = await open(engine, lang, 1100, t);
      const title = await group.evaluate((g) => [...g.querySelectorAll("[title]")].map((e) => e.title).join("\n"));
      assert(title.includes(w.card), `the card says the condition: ${title}`);

      await press(page, group.locator("button", { hasText: EDIT[lang] }));
      const rows = page.locator(".rt-gedit .rt-rule");
      await rows.first().waitFor();
      const [r1, r2, r3, r4] = [0, 1, 2, 3].map((i) => rows.nth(i));

      // tokens: ≥ as before, < picked from the app's menu
      await press(page, r1.locator(".rt-cond.tk .rt-op"), true);
      assert.equal(await page.locator("select").count(), 0, "the app menu, not a native select");
      assert.equal(await page.locator("#list li").count(), 4);
      await pick(page, w.lt);
      assert.equal(await r1.locator(".rt-cond.tk .rt-op").textContent(), "<");
      assert.equal(await r1.locator(".rt-cond.tk input").inputValue(), "50000");

      // the message, added from the + menu: starts with
      await r1.locator(".rt-addc").click();
      await page.locator(".proto-menu .pm-item", { hasText: w.message }).click();
      const ms = r1.locator(".rt-cond.ms");
      await ms.locator("input").fill("/review");
      await ms.locator(".rt-op").click();
      assert.equal(await page.locator("#list li").count(), 4);
      await pick(page, w.startsWith);
      assert.equal(await ms.locator(".rt-op").textContent(), w.startsWith);
      assert.match(await ms.getAttribute("class"), /\bon\b/);
      // a subagent's turn or not
      await r1.locator(".rt-addc").click();
      await page.locator(".proto-menu .pm-item", { hasText: w.subagent }).click();
      const sb = r1.locator(".rt-sub");
      assert.equal(await sb.textContent(), w.sub);
      await sb.click();
      await pick(page, w.notSub);
      assert.equal(await sb.textContent(), w.notSub);

      // reasoning saved as a condition reads back as one
      assert.equal(await r2.locator(".rt-cond.ef .rt-op").first().textContent(), w.isNot);
      assert.equal(await r2.locator(".rt-cond.ef .rt-op").nth(1).textContent(), "high");
      // agents: is one of them as before, is none of them a condition
      await r3.locator(".rt-aop").click();
      await pick(page, w.agentNot);
      assert.equal(await r3.locator(".rt-aop").textContent(), w.agentNot);

      if (process.env.SHOT) await page.waitForTimeout(500), await page.locator(".rt-gedit .rt-rules, .rt-gedit").first().screenshot({ path: `${process.env.SHOT}/${engine}-${lang}-wide.png` });
      await press(page, page.locator(".rt-gedit button.primary", { hasText: w.save }));
      await page.waitForFunction(() => !document.querySelector(".rt-gedit"));
      const save = posts.find((p) => p.path === "/api/groups/save");
      assert(save, `saved: ${JSON.stringify(posts)}`);
      const [s1, s2, s3, s4] = save.body.rules;
      assert.equal(s1.tokens, 0);
      assert.deepEqual(s1.where, [{ field: "tokens", op: "lt", value: "50000" }, { field: "message", op: "starts", value: "/review" }, { field: "subagent", op: "not" }]);
      assert.deepEqual(s2.where, [{ field: "effort", op: "not", value: "high" }], "kept as it was");
      assert.deepEqual([s3.agents, s3.where], [[], [{ field: "agent", op: "not", value: "claude" }]]);
      assert.deepEqual([s4.images, s4.where], [true, undefined], "a rule saved before saves as before");
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: a pause takes agent is not, and no subagent or message`, async (t) => {
      const { page, group, errors, posts } = await open(engine, lang, 1100, t, [{ use: "b/other", pause: true, agents: ["claude"] }]);
      await press(page, group.locator("button", { hasText: EDIT[lang] }));
      const row = page.locator(".rt-gedit .rt-rule").first();
      await row.waitFor();
      await row.locator(".rt-aop").click();
      await pick(page, w.agentNot);
      await row.locator(".rt-addc").click();
      assert.equal(await page.locator(".proto-menu .pm-item", { hasText: w.message }).count(), 0);
      assert.equal(await page.locator(".proto-menu .pm-item", { hasText: w.subagent }).count(), 0);
      assert.equal(await page.locator(".proto-menu .pm-item", { hasText: w.system }).count(), 0);
      assert.equal(await page.locator(".proto-menu .pm-item", { hasText: w.last }).count(), 0);
      await page.keyboard.press("Escape");
      await press(page, page.locator(".rt-gedit button.primary", { hasText: w.save }));
      await page.waitForFunction(() => !document.querySelector(".rt-gedit"));
      const save = posts.find((p) => p.path === "/api/groups/save");
      assert.deepEqual([save.body.rules[0].pause, save.body.rules[0].agents, save.body.rules[0].where], [true, [], [{ field: "agent", op: "not", value: "claude" }]]);
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: a rule matches the system prompt and the last user message`, async (t) => {
      const { page, group, errors, posts } = await open(engine, lang, 1100, t, SYS);
      const title = await group.evaluate((g) => [...g.querySelectorAll("[title]")].map((e) => e.title).join("\n"));
      assert(title.includes(w.sysCard), `the card says the condition: ${title}`);

      await press(page, group.locator("button", { hasText: EDIT[lang] }));
      const rows = page.locator(".rt-gedit .rt-rule");
      await rows.first().waitFor();
      const [r1, r2] = [rows.nth(0), rows.nth(1)];
      // saved as a pattern, it reads back as one
      assert.equal(await r1.locator(".rt-cond.sy .rt-op").textContent(), w.matches);
      assert.equal(await r1.locator(".rt-cond.sy input").inputValue(), "^你是对话标题命名");

      // the last user message, from the + menu: starts with
      await press(page, r2.locator(".rt-addc"));
      await page.locator(".proto-menu .pm-item", { hasText: w.last }).click();
      const lu = r2.locator(".rt-cond.lu");
      await lu.locator("input").fill("正在上下文压缩");
      await lu.locator(".rt-op").click();
      assert.equal(await page.locator("select").count(), 0, "the app menu, not a native select");
      assert.equal(await page.locator("#list li").count(), 4);
      await pick(page, w.startsWith);
      assert.equal(await lu.locator(".rt-op").textContent(), w.startsWith);
      // the system prompt, a pattern: one that isn't shows so
      await press(page, r2.locator(".rt-addc"));
      await page.locator(".proto-menu .pm-item", { hasText: w.system }).click();
      const sy = r2.locator(".rt-cond.sy");
      await sy.locator(".rt-op").click();
      await pick(page, w.matches);
      await sy.locator("input").fill("(unclosed");
      assert.match(await sy.getAttribute("class"), /\bbad\b/);
      await sy.locator("input").fill("^You are");
      assert.doesNotMatch(await sy.getAttribute("class"), /\bbad\b/);
      assert.deepEqual(await overrun(page), []);

      await press(page, page.locator(".rt-gedit button.primary", { hasText: w.save }));
      await page.waitForFunction(() => !document.querySelector(".rt-gedit"));
      const save = posts.find((p) => p.path === "/api/groups/save");
      assert(save, `saved: ${JSON.stringify(posts)}`);
      const [s1, s2] = save.body.rules;
      assert.deepEqual(s1.where, [{ field: "system", op: "matches", value: "^你是对话标题命名" }], "kept as it was");
      assert.equal(s2.tokens, 1000);
      assert.deepEqual(s2.where, [{ field: "last", op: "starts", value: "正在上下文压缩" }, { field: "system", op: "matches", value: "^You are" }]);
      assert.deepEqual(errors, []);
    });
  }
  // narrow, in every language: every field reads in its pill, nothing runs past
  for (const lang of Object.keys(words)) {
    test(`${engine} ${lang}: every kind of condition at a narrow width`, async (t) => {
      const { page, group, errors } = await open(engine, lang, 420, t, ALL);
      await press(page, group.locator("button", { hasText: EDIT[lang] }));
      const row = page.locator(".rt-gedit .rt-rule").first();
      await row.waitFor();
      assert.equal(await row.locator(".rt-cond.tk .rt-op").textContent(), "<");
      assert.equal(await row.locator(".rt-cond.ef .rt-op").first().textContent(), words[lang].isNot);
      assert.equal(await row.locator(".rt-aop").textContent(), words[lang].agentNot);
      assert.equal(await row.locator(".rt-sub").textContent(), words[lang].sub);
      assert.equal(await row.locator(".rt-cond.ms .rt-op").textContent(), words[lang].contains);
      assert.equal(await row.locator(".rt-cond.sy .rt-op").textContent(), words[lang].matches);
      assert.equal(await row.locator(".rt-cond.sy input").inputValue(), "^你是对话标题命名");
      assert.equal(await row.locator(".rt-cond.lu .rt-op").textContent(), words[lang].startsWith);
      assert.equal(await row.locator(".rt-more").count(), 0, "each in its own pill");
      if (process.env.SHOT) { await page.waitForTimeout(500); await row.scrollIntoViewIfNeeded(); await page.screenshot({ path: `${process.env.SHOT}/${engine}-${lang}-narrow.png` }); }
      assert.deepEqual(await overrun(page), []);
      assert.deepEqual(errors, []);
    });
  }
}
