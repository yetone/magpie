// Run with Node's test runner and Playwright on the module path; see README.md.
// skills.sh lists a repository's skills one by one (Ethereal499 on X:
// mattpocock/skills' all apart). Two or more from one repository are one
// card, the repository, where it first comes; a repository with one skill
// listed, and magpie's own, keep their own cards. Opened, the card lists
// the repository's skills, the whole repository once Find skills has read
// it, the list's ticked, and installs the ones picked in one go. A
// repository too big to read whole keeps the list's skills, each installed
// as its card's Add does. Every language, a narrow window, both engines.
// No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/tester";
const mp = (id, installs) => ({ id: `mattpocock/skills/${id}`, name: id, source: "mattpocock/skills", skillId: id, installs, icon: "" });
const items = [
  { id: "magpie-community/plugins/magpie-quota", name: "magpie-quota", source: "magpie-community/plugins", skillId: "magpie-quota", installs: 0, official: true, featured: true, icon: "", description: "Check what is left of every subscription." },
  { id: "magpie-community/plugins/magpie-setup", name: "magpie-setup", source: "magpie-community/plugins", skillId: "magpie-setup", installs: 0, official: true, featured: true, icon: "", description: "Set magpie up." },
  mp("tdd", 3000),
  { id: "acme/skills/pdf", name: "pdf", source: "acme/skills", skillId: "pdf", installs: 1200, icon: "" },
  mp("grill-me", 2000),
  mp("write-a-prd", 1000),
];
// the whole repository, as Find skills reads it: two more than the list
// has, one of them in the library already
const candidates = [
  { name: "tdd", path: "skills/tdd", description: "Test first." },
  { name: "grill-me", path: "skills/grill-me", description: "Asks hard questions." },
  { name: "write-a-prd", path: "skills/write-a-prd", description: "Writes a PRD." },
  { name: "triage", path: "skills/triage", description: "Triage issues." },
  { name: "caveman", path: "skills/caveman", description: "Talks less.", have: true },
];
const words = {
  en: { skills: "3 skills", five: "5 skills", choose: "Choose…", install: "Install {n} skills", one: "Install 1 skill" },
  zh: { skills: "3 个技能", five: "5 个技能", choose: "选择…", install: "安装 {n} 个技能", one: "安装 1 个技能" },
  "zh-TW": { skills: "3 個技能", five: "5 個技能", choose: "選擇…", install: "安裝 {n} 個技能", one: "安裝 1 個技能" },
  ja: { skills: "3 件のスキル", five: "5 件のスキル", choose: "選択…", install: "{n} 個のスキルをインストール", one: "1 個のスキルをインストール" },
  de: { skills: "3 Skills", five: "5 Skills", choose: "Auswählen…", install: "{n} Skills installieren", one: "1 Skill installieren" },
};

function serve(lang, st) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data, status = 200) => route.fulfill({ status, json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    const library = () => ({
      dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
      agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color", skills: `${HOME}/.claude/skills`, mcp: `${HOME}/.claude.json` }],
      instructions: { agents: [], sets: [] }, servers: [], foundServers: [], projects: [], foundSkills: [], skills: [], problems: [],
    });
    if (url.pathname === "/api/library") return json(library());
    if (url.pathname === "/api/library/market/skills") return json({ items: structuredClone(items) });
    if (url.pathname === "/api/library/market/servers") return json({ items: [] });
    if (url.pathname === "/api/library/market/about") {
      const { ids } = route.request().postDataJSON();
      st.about.push(...ids);
      return json(Object.fromEntries(ids.map((id) => [id, `About ${id}`])));
    }
    if (url.pathname === "/api/library/skills/probe") {
      st.probed.push(route.request().postDataJSON().source);
      await st.probeGate;
      if (st.tooBig) return json({ error: "mattpocock/skills is too big to download" }, 400);
      return json({ source: "mattpocock/skills", kind: "github", candidates: structuredClone(candidates) });
    }
    if (url.pathname === "/api/library/skills/install" || url.pathname === "/api/library/market/skill") {
      st.installs.push([url.pathname.replace("/api/library/", ""), route.request().postDataJSON()]);
      return json({ ...library(), result: { added: [] } });
    }
    if (url.pathname === "/api/library/icon") return route.fulfill({ status: 404, body: "" });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    await (body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" }));
  };
}

async function open(engine, lang, t, st) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  t.after(() => browser.close());
  const context = await browser.newContext({ viewport: { width: 420, height: 760 }, reducedMotion: "reduce" });
  const page = await context.newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, st));
  await page.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "skills"); } catch {} });
  await page.goto("http://magpie.test/?view=library");
  return { page, errors };
}

const gate = () => { let go; const p = new Promise((ok) => { go = ok; }); return [p, go]; };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    const w = words[lang];
    test(`${engine} ${lang}: a repository's skills are one card, opened to pick all or a few`, async (t) => {
      const [probeGate, letProbe] = gate();
      const st = { about: [], probed: [], installs: [], probeGate };
      const { page, errors } = await open(engine, lang, t, st);
      const box = page.locator('#view-library .mk[data-market="skills"]');
      const repo = box.locator('.mk-card[data-id="repo:mattpocock/skills"]');
      await repo.waitFor();
      // where its first skill came, after magpie's own two, before acme's
      assert.deepEqual(await box.locator(".mk-card").evaluateAll((cs) => cs.map((c) => c.dataset.id)),
        ["magpie-community/plugins/magpie-quota", "magpie-community/plugins/magpie-setup", "repo:mattpocock/skills", "acme/skills/pdf"]);
      assert.equal((await repo.locator(".mk-title").textContent()).trim(), "mattpocock/skills");
      assert.equal((await repo.locator(".mk-count").textContent()).trim(), w.skills);
      assert.equal((await repo.locator(".mk-desc").textContent()).trim(), "tdd, grill-me, write-a-prd");
      assert.equal((await repo.locator(".mk-installs").textContent()).trim(), "6K");
      assert.equal((await repo.locator(".mk-add").textContent()).trim(), w.choose);
      // only a skill's own card asks for its about
      await page.waitForFunction(() => !document.querySelector(".mk-desc.wait"));
      assert.deepEqual(st.about, ["acme/skills/pdf"]);
      // the card fits the narrow window
      assert(await repo.evaluate((c) => c.getBoundingClientRect().right <= document.documentElement.clientWidth));

      await repo.click();
      const sheet = page.locator("#modal .mk-reposheet");
      await sheet.waitFor();
      const rows = sheet.locator(".lib-pick");
      const go = sheet.locator(".bar .primary");
      // the list's three at once, ticked, while the repository is read
      await sheet.locator(".mk-repowait").waitFor();
      assert.equal(await rows.count(), 3);
      assert.equal(await sheet.locator(".lib-pick input:checked").count(), 3);
      assert.equal((await go.textContent()).trim(), w.install.replace("{n}", 3));
      letProbe();
      await page.waitForFunction(() => document.querySelectorAll("#modal .mk-reposheet .lib-pick").length === 5);
      assert.deepEqual(st.probed, ["mattpocock/skills"]);
      assert.equal(await sheet.locator(".mk-repowait").count(), 0);
      // the card behind now counts what the sheet lists, not skills.sh
      assert.equal((await repo.locator(".mk-count").textContent()).trim(), w.five);
      // the list's still ticked, the rest not, the one had greyed
      assert.deepEqual(await rows.evaluateAll((rs) => rs.map((r) => [r.querySelector(".name").textContent, r.querySelector("input").checked, r.querySelector("input").disabled])),
        [["tdd", true, false], ["grill-me", true, false], ["write-a-prd", true, false], ["triage", false, false], ["caveman", false, true]]);
      // Select all takes every one not had; then a few
      await sheet.locator(".mk-repoall").click();
      assert.equal((await go.textContent()).trim(), w.install.replace("{n}", 4));
      await sheet.locator(".mk-repoall").click();
      assert.equal(await go.isDisabled(), true);
      await rows.nth(1).locator("input").check();
      await rows.nth(3).locator("input").check();
      assert.equal((await go.textContent()).trim(), w.install.replace("{n}", 2));
      // the sheet fits the narrow window
      assert(await go.evaluate((b) => b.getBoundingClientRect().right <= document.documentElement.clientWidth));
      await go.click();
      await sheet.waitFor({ state: "detached" });
      assert.deepEqual(st.installs, [["skills/install", { source: "mattpocock/skills", paths: ["skills/grill-me", "skills/triage"], agents: ["claude"] }]]);
      // drawn again, the card keeps the sheet's count
      assert.equal((await repo.locator(".mk-count").textContent()).trim(), w.five);
      // opened again, the sheet lists the read repository at once, the
      // two just installed greyed, while it is read again
      const [again, letAgain] = gate();
      st.probeGate = again;
      await repo.click();
      await sheet.waitFor();
      assert.equal(await sheet.locator(".mk-repowait").count(), 0);
      assert.deepEqual(await rows.evaluateAll((rs) => rs.map((r) => [r.querySelector(".name").textContent, r.querySelector("input").checked, r.querySelector("input").disabled])),
        [["tdd", true, false], ["grill-me", false, true], ["write-a-prd", true, false], ["triage", false, true], ["caveman", false, true]]);
      letAgain();
      await page.waitForFunction(() => document.querySelector("#modal .mk-reposheet") && !document.querySelector(".mk-repowait"));
      assert.equal(st.probed.length, 2);
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: a repository too big to read whole keeps the list's skills, each installed by itself`, async (t) => {
      const st = { about: [], probed: [], installs: [], probeGate: Promise.resolve(), tooBig: true };
      const { page, errors } = await open(engine, lang, t, st);
      const repo = page.locator('#view-library .mk-card[data-id="repo:mattpocock/skills"]');
      await repo.locator(".mk-add").click();
      const sheet = page.locator("#modal .mk-reposheet");
      await sheet.locator(".mk-repoerr").waitFor();
      assert.match(await sheet.locator(".mk-repoerr").textContent(), /too big to download/);
      const rows = sheet.locator(".lib-pick");
      assert.equal(await rows.count(), 3);
      await rows.nth(1).locator("input").uncheck();
      await sheet.locator(".bar .primary").click();
      await sheet.waitFor({ state: "detached" });
      assert.deepEqual(st.installs, [
        ["market/skill", { source: "mattpocock/skills", id: "tdd", agents: ["claude"] }],
        ["market/skill", { source: "mattpocock/skills", id: "write-a-prd", agents: ["claude"] }],
      ]);
      assert.deepEqual(errors, []);
    });
  }
}
