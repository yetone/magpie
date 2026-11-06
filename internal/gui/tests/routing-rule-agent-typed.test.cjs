// Run with Node's test runner and Playwright on the module path; see README.md.
// A rule's agents may be one magpie doesn't know (raikyou on Discord:
// SillyTavern, one's own app with its own User-Agent). The gateway names
// such a client by its User-Agent's product name (usage.AgentOf), and a
// rule matches it in lower case. In the editor the agent picker offers the
// agents magpie knows, then those usage heard from in the last 30 days that
// it doesn't (not "other", nor magpie's own), and a name typed in the
// filter, read as the gateway reads a User-Agent: "MyApp/2.0 (Linux)" is
// myapp, and a name that is a known agent's is that agent. Saved as
// agents:[…]. In every language, and at a phone's width the menu stays on
// the screen with nothing run past the side.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { id: "a/opus", name: "opus", providerName: "A", icon: "generic", context: 1000000, ready: true },
  { id: "b/kimi", name: "kimi", providerName: "B", icon: "generic", context: 256000, ready: true },
];
const groups = () => ({
  models,
  groups: [{ id: "main", name: "Main", members: ["a/opus", "b/kimi"], routing: "order", ready: true,
    memberInfo: [{ id: "a/opus", ready: true, context: 1000000 }, { id: "b/kimi", ready: true, context: 256000 }], rules: [{ use: "b/kimi", agents: ["codex"] }] }],
  pools: [],
});
// /api/usage?period=30d's agents, as usageapi.Page names them: a known
// one, one by its User-Agent as the gateway kept it, and the two the
// picker leaves out
const usage = { agents: [
  { id: "claude", name: "Claude Code", icon: "claudecode-color", calls: 9 },
  { id: "SillyTavern", name: "SillyTavern", icon: "generic", calls: 4 },
  { id: "other", name: "other", icon: "generic", calls: 2 },
  { id: "magpie", name: "magpie", icon: "generic", calls: 1 },
] };

const words = {
  en: { edit: "Edit", save: "Save", filter: "Filter, or type an agent's name…", heard: "heard from in the last 30 days", typed: "the name its User-Agent begins with" },
  zh: { edit: "编辑", save: "保存", filter: "筛选，或输入 Agent 名称…", heard: "近 30 天有请求", typed: "User-Agent 开头的名称" },
  "zh-TW": { edit: "編輯", save: "儲存", filter: "篩選，或輸入 Agent 名稱…", heard: "近 30 天有請求", typed: "User-Agent 開頭的名稱" },
  ja: { edit: "編集", save: "保存", filter: "絞り込み、またはエージェント名を入力…", heard: "過去 30 日にリクエストあり", typed: "User-Agent の先頭の名前" },
  de: { edit: "Bearbeiten", save: "Speichern", filter: "Filtern oder Namen eines Agenten eingeben…", heard: "in den letzten 30 Tagen gesehen", typed: "der Name, mit dem sein User-Agent beginnt" },
};

function serve(lang, posts, asked) {
  const state = {
    agents: [{ id: "claude", name: "Claude Code", path: "/test/claude", fields: [] }],
    clients: [{ id: "claude", name: "Claude Code", icon: "claudecode-color" }, { id: "codex", name: "Codex", icon: "codex-color" }],
    profiles: [], settings: { lang, theme: "light" },
  };
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
    if (url.pathname === "/api/usage") {
      asked.push(url.search);
      return json(url.searchParams.get("period") === "30d" ? usage : { agents: [] });
    }
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

async function open(t, engine, lang, width) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  t.after(() => browser.close());
  const page = await (await browser.newContext({ viewport: { width, height: 1400 }, reducedMotion: "reduce" })).newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [], posts = [], asked = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, posts, asked));
  await page.goto("http://magpie.test/?view=groups");
  const group = page.locator(".rt-group").first();
  await group.waitFor();
  return { page, group, errors, posts, asked };
}

// the view wheeled down to a button, as the reader would (the page keeps
// still for any other scroll), and the button clicked where it is
async function press(page, b) {
  await page.mouse.move(Math.min(550, page.viewportSize().width / 2), 600);
  await b.waitFor();
  for (let i = 0; i < 10 && (await b.boundingBox()).y > 1400 - 160; i++) {
    await page.mouse.wheel(0, 300);
    await page.waitForTimeout(150);
  }
  const bb = await b.boundingBox();
  await page.mouse.click(bb.x + bb.width / 2, bb.y + bb.height / 2);
}

// the menu's rows, by name
const names = (page) => page.locator("#list li .v").evaluateAll((vs) => vs.map((v) => v.textContent));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    const w = words[lang];
    test(`${engine} ${lang}: a rule for an agent magpie knows only by its User-Agent`, async (t) => {
      const { page, group, errors, posts, asked } = await open(t, engine, lang, 1100);
      await press(page, group.locator("button", { hasText: w.edit }));
      const ed = page.locator(".rt-gedit");
      const ag = ed.locator(".rt-rule").first().locator('.rt-c[data-k="agents"] > button.rt-cond');
      await ag.waitFor();
      assert.equal(await ag.textContent(), "Codex");

      // the known agents, then the one usage heard from, by the gateway's name
      await press(page, ag);
      const q = page.locator("#q");
      await q.waitFor();
      assert.equal(await q.getAttribute("placeholder"), w.filter);
      await page.waitForFunction(() => [...document.querySelectorAll("#list li .v")].some((v) => v.textContent === "sillytavern"));
      const listed = await names(page);
      assert.deepEqual(listed.slice(1), ["Claude Code", "Codex", "sillytavern"], `known, then heard from: ${JSON.stringify(listed)}`);
      const heard = page.locator("#list li", { hasText: "sillytavern" });
      assert((await heard.textContent()).includes(w.heard), `said where it came from: ${await heard.textContent()}`);
      assert.equal(asked.filter((x) => x === "?period=30d").length, 1, `usage asked once, for 30 days: ${asked}`);
      await press(page, heard);
      assert.equal(await ag.textContent(), "Codex, sillytavern");

      // a name typed in, as the gateway reads a User-Agent
      await press(page, ag);
      await q.waitFor();
      await q.fill("MyApp/2.0 (Linux)");
      const typed = page.locator("#list li", { hasText: "myapp" });
      await typed.waitFor();
      assert((await typed.textContent()).includes(w.typed), `said how it is read: ${await typed.textContent()}`);
      await q.press("Enter");
      assert.equal(await ag.textContent(), "Codex, sillytavern, myapp");
      assert.equal(await ag.getAttribute("class"), "rt-cond on");

      // a known agent's name typed is that agent, once
      await press(page, ag);
      await q.waitFor();
      await q.fill("Codex/0.50");
      await page.waitForFunction(() => document.querySelectorAll("#list li .v").length > 0);
      assert.deepEqual(await names(page), ["Codex"]);
      // the ones picked come back ticked, the typed one among them
      await q.fill("");
      await page.waitForFunction(() => [...document.querySelectorAll("#list li .v")].some((v) => v.textContent === "myapp"));
      assert.deepEqual((await names(page)).slice(1), ["Claude Code", "Codex", "sillytavern", "myapp"]);
      await page.keyboard.press("Escape");

      // saved as the gateway names them
      const saveBtn = ed.locator("button.primary", { hasText: w.save });
      await press(page, saveBtn);
      await page.waitForFunction(() => !document.querySelector(".rt-gedit"));
      const save = posts.find((p) => p.path === "/api/groups/save");
      assert(save, `saved: ${JSON.stringify(posts)}`);
      assert.deepEqual(save.body.rules.map((r) => [r.use, r.agents]), [["b/kimi", ["codex", "sillytavern", "myapp"]]]);
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: the agent picker at a phone's width`, async (t) => {
      const { page, group, errors } = await open(t, engine, lang, 390);
      await press(page, group.locator("button", { hasText: w.edit })); // the group is below the fold here
      const ag = page.locator('.rt-gedit .rt-rule .rt-c[data-k="agents"] > button.rt-cond').first();
      await ag.waitFor();
      await press(page, ag);
      const q = page.locator("#q");
      await q.waitFor();
      await q.fill("SillyTavern-Desktop/1.0");
      await page.locator("#list li", { hasText: "sillytavern-desktop" }).waitFor();
      const pop = await page.locator("#pop").boundingBox();
      assert(pop.x >= 0 && pop.x + pop.width <= 390 + 1, `the menu stays on the screen: ${JSON.stringify(pop)}`);
      await q.press("Enter");
      assert.equal(await ag.textContent(), "Codex, sillytavern-desktop");
      const [b, row] = await Promise.all([ag.boundingBox(), page.locator(".rt-gedit .rt-rule").first().boundingBox()]);
      assert(b.x >= row.x - 1 && b.x + b.width <= row.x + row.width + 1, `the agents' condition stays in its rule: ${JSON.stringify([b, row])}`);
      const wide = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
      assert(wide <= 0, `no sideways scroll: ${wide}px`);
      assert.deepEqual(errors, []);
    });
  }
}
