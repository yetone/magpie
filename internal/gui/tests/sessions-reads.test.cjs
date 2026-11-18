// Run with Node's test runner and Playwright on the module path; see README.md.
// Which agents' session history magpie reads (RustCats on X: a new
// install showed every agent's history before anyone agreed). Until the
// user says (sessions/manage's reads.asked false), the Sessions page shows
// the consent first: the agents found, each with a switch of its own, off;
// Allow selected stays off until one is picked and saves those picked,
// Allow all saves every agent and those found later. Settings › Privacy
// has a row for agents found later and one per agent, each saved alone.
// An agent found after a choice that names its agents (baysonfox on X:
// Antigravity 2.0 installed after allow selected) is asked about once on
// the Sessions page: Allow names it on, Keep off names it off.
// Every language, Chromium and WebKit, a narrow window; the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const at = (min) => new Date(Date.now() - min * 60e3).toISOString();
const found = () => [
  { agent: "claude", name: "Claude Code", icon: "claudecode-color", on: false },
  { agent: "codex", name: "Codex", icon: "codex", on: false },
];

function serve(lang, store, posts) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light", sessionReads: { asked: store.asked, all: store.all } } });
    if (url.pathname === "/api/settings") return json({ lang, theme: "light" });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/sessions/reads") {
      if (req.method() === "POST") {
        const b = req.postDataJSON();
        posts.push(b);
        store.asked = true;
        if (typeof b.all === "boolean") store.all = b.all;
        for (const f of store.found) if (b.agents && f.agent in b.agents) f.on = b.agents[f.agent];
      }
      for (const p of posts) for (const a in p.agents || {}) store.named[a] = true;
      for (const f of store.found) if (!(f.agent in store.named)) f.on = store.asked && store.all;
      // as readsNow: found, not named and not read under a choice that names
      for (const f of store.found) f.new = store.asked && !store.all && !(f.agent in store.named);
      return json({ asked: store.asked, all: store.all, found: store.found });
    }
    if (url.pathname === "/api/sessions/manage") {
      for (const f of store.found) f.new = store.asked && !store.all && !(f.agent in store.named);
      const on = store.found.filter((f) => store.asked && f.on);
      const agents = on.map((f) => ({ agent: f.agent, count: 1, deletable: true, name: f.name, icon: f.icon }));
      const agent = agents[0]?.agent || "";
      const sessions = agent ? [{ agent, id: agent + "-1", cwd: "/work/app", title: "a " + agent + " chat", start: at(10), last: at(5), models: [], resume: "", path: "/x/" + agent, size: 10, messages: 2, files: 1, deletable: true }] : [];
      return json({ agents, agent, sessions, terminal: false, trash: [], trashDir: "", reads: { asked: store.asked, all: store.all, found: store.found } });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const words = {
  en: { nav: "Sessions", title: "Show your agents' sessions?", some: "Allow selected", all: "Allow all", head: "Agents' session history", later: "Agents found later", on: "On", off: "Off", laterTitle: "Show these agents' sessions too?", keepOff: "Keep off", allow: "Show their sessions" },
  zh: { nav: "会话", title: "显示智能体的会话吗？", some: "允许所选", all: "全部允许", head: "智能体的会话历史", later: "之后发现的智能体", on: "开启", off: "关闭", laterTitle: "还要显示这些智能体的会话吗？", keepOff: "保持关闭", allow: "显示会话" },
  "zh-TW": { nav: "會話", title: "顯示智慧體的會話嗎？", some: "允許所選", all: "全部允許", head: "智慧體的會話歷史", later: "之後發現的智慧體", on: "開啟", off: "關閉", laterTitle: "也要顯示這些智慧體的會話嗎？", keepOff: "保持關閉", allow: "顯示會話" },
  ja: { nav: "セッション", title: "エージェントのセッションを表示しますか？", some: "選択したものを許可", all: "すべて許可", head: "エージェントのセッション履歴", later: "あとで見つかったエージェント", on: "オン", off: "オフ", laterTitle: "これらのエージェントのセッションも表示しますか？", keepOff: "オフのままにする", allow: "セッションを表示" },
  de: { nav: "Sitzungen", title: "Sitzungen deiner Agenten anzeigen?", some: "Ausgewählte erlauben", all: "Alle erlauben", head: "Sitzungsverlauf der Agenten", later: "Später gefundene Agenten", on: "An", off: "Aus", laterTitle: "Auch die Sitzungen dieser Agenten anzeigen?", keepOff: "Aus lassen", allow: "Sitzungen anzeigen" },
};

async function open(engine, width, t) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  const context = await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" });
  const page = await context.newPage();
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  page.on("dialog", (d) => { errors.push("a browser dialog: " + d.message()); d.dismiss(); });
  t.after(async () => {
    if (process.env.ARTIFACT_DIR) {
      await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
      await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${t.name.replace(/[^\w-]+/g, "-")}.png`) }).catch(() => {});
    }
    await browser.close();
  });
  page.setDefaultTimeout(6000);
  page.setDefaultNavigationTimeout(60000);
  return { page, errors };
}

// nothing in the card is cut off or wider than the window
async function fits(page, sel) {
  const over = await page.$eval(sel, (card) => {
    const vw = document.documentElement.clientWidth;
    const r = card.getBoundingClientRect();
    const bad = [...card.querySelectorAll("*")].filter((e) => { const b = e.getBoundingClientRect(); return b.width && (b.right > vw + 0.5 || b.left < -0.5); }).map((e) => e.className);
    return { right: r.right, vw, bad, scroll: document.documentElement.scrollWidth > vw };
  });
  assert.ok(over.right <= over.vw + 0.5 && !over.bad.length && !over.scroll, JSON.stringify(over));
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const [lang, width] of [["en", 900], ["en", 390], ["zh", 420], ["zh-TW", 420], ["ja", 420], ["de", 390]]) {
    const w = words[lang];
    test(`${engine} ${lang} ${width}px: the Sessions page asks before reading any agent's sessions`, async (t) => {
      const { page, errors } = await open(engine, width, t);
      const store = { asked: false, all: false, found: found(), named: {} }, posts = [];
      await page.route("**/*", serve(lang, store, posts));
      await page.goto("http://magpie.test/?view=sessions");
      const card = page.locator("#view-sessions .sm-consent");
      await card.waitFor();
      assert.equal(await card.locator(".sm-consent-title").textContent(), w.title);
      assert.equal(await card.getAttribute("role"), "group");
      // no session is listed, and no empty state talks over the card
      assert.equal(await page.locator("#view-sessions .row.sm-sess").count(), 0);
      assert.equal(await page.locator("#view-sessions .empty-state").count(), 0);
      const sw = (name) => card.getByRole("switch", { name, exact: true });
      assert.equal(await sw("Claude Code").getAttribute("aria-checked"), "false");
      assert.equal(await sw("Codex").getAttribute("aria-checked"), "false");
      const some = card.getByRole("button", { name: w.some, exact: true });
      assert.ok(await some.isDisabled(), "Allow selected with nothing picked");
      await fits(page, "#view-sessions .sm-consent");
      // a click on the row's name flips its switch too
      await card.locator(".sm-consent-row", { hasText: "Claude Code" }).locator(".sm-consent-name").click();
      assert.equal(await sw("Claude Code").getAttribute("aria-checked"), "true");
      assert.ok(await some.isEnabled());
      await sw("Codex").click();
      await sw("Codex").click();
      assert.equal(await sw("Codex").getAttribute("aria-checked"), "false");
      assert.equal(posts.length, 0, "nothing is saved before a button is pressed");
      await some.click();
      await card.waitFor({ state: "detached" });
      assert.deepEqual(posts, [{ all: false, agents: { claude: true, codex: false } }]);
      await page.locator('#view-sessions .row.sm-sess[data-id="claude-1"]').waitFor();
      assert.deepEqual(errors, []);
    });
  }
  for (const [lang, width] of [["en", 900], ["en", 390], ["zh", 420], ["zh-TW", 420], ["ja", 420], ["de", 390]]) {
    const w = words[lang];
    for (const allow of [true, false]) {
      test(`${engine} ${lang} ${width}px: an agent found after allow selected is asked about once (${allow ? "allow" : "keep off"})`, async (t) => {
        const { page, errors } = await open(engine, width, t);
        const store = {
          asked: true, all: false, named: { claude: true },
          found: [{ agent: "claude", name: "Claude Code", icon: "claudecode-color", on: true }, { agent: "antigravity", name: "Antigravity", icon: "antigravity-color", on: false }],
        }, posts = [];
        await page.route("**/*", serve(lang, store, posts));
        await page.goto("http://magpie.test/?view=sessions");
        const card = page.locator("#view-sessions .sm-later");
        await card.waitFor();
        assert.equal(await page.locator("#view-sessions .sm-consent:not(.sm-later)").count(), 0, "the first consent is not shown again");
        assert.equal(await card.locator(".sm-consent-title").textContent(), w.laterTitle);
        assert.equal(await card.getAttribute("role"), "group");
        assert.deepEqual(await card.locator(".sm-consent-name").allTextContents(), ["Antigravity"]);
        // the sessions already read are listed under it
        await page.locator('#view-sessions .row.sm-sess[data-id="claude-1"]').waitFor();
        await fits(page, "#view-sessions .sm-later");
        assert.equal(posts.length, 0);
        await card.getByRole("button", { name: allow ? w.allow : w.keepOff, exact: true }).click();
        await card.waitFor({ state: "detached" });
        assert.deepEqual(posts, [{ agents: { antigravity: allow } }]);
        // not asked again
        await page.reload();
        await page.locator('#view-sessions .row.sm-sess[data-id="claude-1"]').waitFor();
        assert.equal(await page.locator("#view-sessions .sm-later").count(), 0);
        assert.deepEqual(errors, []);
      });
    }
  }
  test(`${engine} en 390px: Allow all reads every agent, those found later too`, async (t) => {
    const { page, errors } = await open(engine, 390, t);
    const store = { asked: false, all: false, found: found(), named: {} }, posts = [];
    await page.route("**/*", serve("en", store, posts));
    await page.goto("http://magpie.test/?view=sessions");
    const card = page.locator("#view-sessions .sm-consent");
    await card.getByRole("button", { name: "Allow all", exact: true }).click();
    await card.waitFor({ state: "detached" });
    assert.deepEqual(posts, [{ all: true, agents: { claude: true, codex: true } }]);
    await page.locator("#view-sessions .row.sm-sess").first().waitFor();
    assert.deepEqual(errors, []);
  });
  test(`${engine} en: no agent found, the consent says so and still offers Allow all`, async (t) => {
    const { page, errors } = await open(engine, 900, t);
    const store = { asked: false, all: false, found: [], named: {} }, posts = [];
    await page.route("**/*", serve("en", store, posts));
    await page.goto("http://magpie.test/?view=sessions");
    const card = page.locator("#view-sessions .sm-consent");
    await card.locator(".sm-consent-none").waitFor();
    assert.equal(await card.getByRole("button", { name: "Allow selected" }).count(), 0);
    assert.equal(await card.getByRole("button", { name: "Allow all", exact: true }).count(), 1);
    assert.deepEqual(errors, []);
  });
  for (const [lang, width] of [["en", 900], ["zh", 420], ["zh-TW", 420], ["ja", 420], ["de", 390]]) {
    const w = words[lang];
    test(`${engine} ${lang} ${width}px: Settings › Privacy changes each agent's reading later`, async (t) => {
      const { page, errors } = await open(engine, width, t);
      const store = { asked: true, all: true, found: found().map((f) => ({ ...f, on: true })), named: {} }, posts = [];
      await page.route("**/*", serve(lang, store, posts));
      await page.goto("http://magpie.test/?view=settings&tab=privacy");
      const list = page.locator("#sessionReadsList");
      await page.locator("#setPage-privacy .row-head .label", { hasText: w.head }).waitFor();
      const row = (sel) => list.locator(sel);
      await row('.row[data-agent="codex"]').waitFor();
      const later = list.locator(".row", { has: page.locator(".name", { hasText: w.later }) });
      const val = (r) => r.locator(".opt.on").textContent();
      assert.equal(await val(later), w.on);
      assert.equal(await val(row('.row[data-agent="claude"]')), w.on);
      assert.equal(await val(row('.row[data-agent="codex"]')), w.on);
      await fits(page, "#sessionReadsList");
      // a click scrolls the row into view itself, and finds it again when a
      // save has drawn the list anew
      const click = (r, name) => r.getByRole("button", { name, exact: true }).click();
      await click(row('.row[data-agent="codex"]'), w.off);
      for (let i = 0; i < 100 && !posts.length; i++) await page.waitForTimeout(50);
      assert.deepEqual(posts[0], { agents: { codex: false } });
      await page.waitForFunction((off) => document.querySelector('#sessionReadsList .row[data-agent="codex"] .opt.on')?.textContent === off, w.off);
      assert.equal(await val(row('.row[data-agent="claude"]')), w.on, "only the agent switched changed");
      await click(later, w.off);
      for (let i = 0; i < 100 && posts.length < 2; i++) await page.waitForTimeout(50);
      // the agents listed keep what their rows show
      assert.deepEqual(posts[1], { all: false, agents: { claude: true, codex: false } });
      await page.waitForFunction(([later, off]) => [...document.querySelectorAll("#sessionReadsList .row")].some((r) => r.querySelector(".name")?.textContent === later && r.querySelector(".opt.on")?.textContent === off), [w.later, w.off]);
      assert.equal(await val(row('.row[data-agent="claude"]')), w.on, "a listed agent keeps its row's value");
      assert.deepEqual(errors, []);
    });
  }
}
