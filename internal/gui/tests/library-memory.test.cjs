// Run with Node's test runner and Playwright on the module path; see README.md.
// The Library's Memory tab (internal/memory, /api/memory): the switch that
// gives the agents ticked for instructions the memories' summary, the
// Magpie Memory server offered while it isn't in the library, the memories
// in use pinned first, a search, the ones an agent only guessed waiting
// under To review, and a memory added, edited through the app's menu (never
// a <select>), kept and deleted. Conversations read into memory: switched
// on, a model picked through the app's menu, read now, and a run that
// stopped says why. The nightly tidy: switched on, with the model picked
// on its row while conversations aren't read, tidied now and undone. Every language, wide and narrow, Chromium
// and WebKit; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/mem";
const library = {
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [{ id: "claude", name: "Claude Code", icon: "", instructions: `${HOME}/.claude/CLAUDE.md` }, { id: "codex", name: "Codex", icon: "", instructions: `${HOME}/.codex/AGENTS.md` }],
  instructions: { shared: "", sets: [{ id: "default", name: "", active: true, text: "" }], agents: [] },
  foundServers: [], projects: [], foundSkills: [], skills: [], servers: [],
};
const at = (d) => `2026-11-0${d}T10:00:00Z`;
function memories() {
  return {
    memories: [
      { id: "m1", text: "Use pnpm, never npm, in this repo", kind: "rule", scope: "project:p1", origin: "user", status: "active", created: at(1), updated: at(5) },
      { id: "m2", text: "Answers in Chinese, code comments in English", kind: "preference", scope: "global", origin: "user", status: "active", pinned: true, created: at(1), updated: at(2) },
      { id: "m3", text: "Deploys go out on Fridays", kind: "fact", scope: "global", origin: "inferred", status: "candidate", sources: [{ agent: "codex", at: at(3) }], created: at(3), updated: at(3) },
    ],
    projects: { p1: { key: "p1", name: "magpie", roots: [`${HOME}/magpie`] } },
    digest: true,
    agents: ["claude"],
  };
}

function distill() {
  return {
    on: false, model: "", auto: "dt/small", running: false, gets: 0,
    efforts: ["low", "medium", "high"], effort: "", effortNow: "", days: 0, ranges: [7, 30, 90, 365, -1],
    models: [{ id: "dt/small", name: "Small", providerName: "DT" }, { id: "dt/m1", name: "Model One", providerName: "DT" }],
  };
}

function server(lang, state, sent, dist) {
  return async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/library") return route.fulfill({ json: library });
    if (url.pathname.startsWith("/api/library/")) {
      sent.push({ path: url.pathname, body: route.request().postDataJSON() });
      return route.fulfill({ json: { ...library, result: { changed: [] } } });
    }
    if (url.pathname.startsWith("/api/memory/dream")) {
      const body = route.request().postDataJSON();
      sent.push({ path: url.pathname, body });
      const m9 = state.memories.find((m) => m.id === "m9");
      if (url.pathname === "/api/memory/dream") dist.dream = body.on;
      else if (url.pathname === "/api/memory/dream/run") Object.assign(dist, { dreaming: true, dgets: 0 });
      else if (url.pathname === "/api/memory/dream/undo") { dist.undone = true; if (m9) m9.status = "candidate"; }
      return route.fulfill({ json: dist });
    }
    if (url.pathname.startsWith("/api/memory/distill")) {
      if (route.request().method() === "GET" && dist.dreaming && ++dist.dgets > 1) {
        // the tidy ends: it set aside the one read in, unreviewed
        Object.assign(dist, { dreaming: false, dreamt: new Date().toISOString(), undone: false, changes: [{ action: "aside", ids: ["m9"], was: ["candidate"], text: "Ships on Tuesdays", why: "unreviewed" }] });
        state.memories.find((m) => m.id === "m9").status = "archived";
      }
      if (route.request().method() === "POST") {
        const body = route.request().postDataJSON();
        sent.push({ path: url.pathname, body });
        if (url.pathname === "/api/memory/distill/run") Object.assign(dist, { running: false, error: "429: too many requests", last: new Date().toISOString(), left: 0 });
        else if (url.pathname === "/api/memory/distill/stop") Object.assign(dist, { running: false, last: new Date().toISOString(), added: 0, left: 397, doing: 0, of: 0 });
        else if ("effort" in body) Object.assign(dist, { effort: body.effort, effortNow: body.effort });
        else if ("days" in body) Object.assign(dist, { days: body.days === 7 ? 0 : body.days, running: body.days === -1 || body.days > (dist.days || 7), gets: 0, doing: 3, of: 400 });
        else Object.assign(dist, { on: body.on, model: body.model, running: body.on, gets: 0, error: "", doing: 12, of: 40 });
      } else if (dist.running && dist.of !== 400 && ++dist.gets > 1) {
        // (All's 400 go on until stopped)
        // the run ends: it read three and proposed one
        Object.assign(dist, { running: false, last: new Date().toISOString(), read: 3, added: 1 });
        if (!state.memories.some((m) => m.id === "m9")) state.memories.push({ id: "m9", text: "Ships on Tuesdays", kind: "fact", scope: "global", origin: "inferred", status: "candidate", created: at(7), updated: at(7) });
      }
      return route.fulfill({ json: dist });
    }
    if (url.pathname.startsWith("/api/memory")) {
      if (route.request().method() === "POST") {
        const body = route.request().postDataJSON();
        sent.push({ path: url.pathname, body });
        if (url.pathname === "/api/memory/digest") state.digest = body.on;
        else if (url.pathname === "/api/memory/remove") state.memories = state.memories.filter((m) => !body.ids.includes(m.id));
        else if (body.id) Object.assign(state.memories.find((m) => m.id === body.id), body, { id: body.id });
        else state.memories.push({ id: "m" + (state.memories.length + 1), kind: "preference", scope: "global", origin: "user", status: "active", created: at(6), updated: at(6), ...body });
      }
      return route.fulfill({ json: state });
    }
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const view = "#view-library";
const words = {
  en: { tab: "Memory", review: "To review", rule: "Rule", fact: "Fact", learn: "Learn from conversations", now: "Read now", tidy: "Tidy up every night" },
  zh: { tab: "记忆", review: "待确认", rule: "规则", fact: "事实", learn: "从对话中学习", now: "立即读取", tidy: "每晚整理" },
  "zh-TW": { tab: "記憶", review: "待確認", rule: "規則", fact: "事實", learn: "從對話中學習", now: "立即讀取", tidy: "每晚整理" },
  ja: { tab: "メモリ", review: "確認待ち", rule: "ルール", fact: "事実", learn: "会話から学ぶ", now: "今すぐ読む", tidy: "毎晩整理する" },
  de: { tab: "Memory", review: "Zu prüfen", rule: "Regel", fact: "Fakt", learn: "Aus Unterhaltungen lernen", now: "Jetzt lesen", tidy: "Jede Nacht aufräumen" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the Library's Memory tab lists, searches, adds, edits, keeps and deletes memories", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    for (const lang of Object.keys(words)) for (const width of [980, 420]) {
      await t.test(`${lang} ${width}`, async () => {
        const w = words[lang];
        const ctx = await browser.newContext({ viewport: { width, height: 860 }, reducedMotion: "reduce" });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "memory"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        const sent = [];
        const state = memories();
        const dist = distill();
        await page.route("**/*", server(lang, state, sent, dist));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const rows = page.locator(`${view} .lib-mems .lib-mem`);
        await rows.first().waitFor();

        // the tab is named, after Instructions
        const tabs = await page.locator(`${view} .lib-tabs .opt`).allTextContents();
        assert.equal(tabs[1].trim(), w.tab);
        assert.ok((await page.locator(`${view} .lib-tabs .opt.on`).textContent()).includes(w.tab));
        // the page is in the reader's language and fits the window
        if (lang !== "en") assert.doesNotMatch(await page.locator(`${view} .lib-intro`).textContent(), /What your agents know/);
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1), "the page scrolls sideways");
        const body = await page.locator(`${view} .lib-body`).boundingBox();
        for (const b of await page.locator(`${view} .lib-membar > *`).all()) {
          const r = await b.boundingBox();
          assert.ok(r.x + r.width <= body.x + body.width + 1, "a control of the bar is cut off");
        }

        // conversations aren't read until switched on, and the row is named in the reader's language
        const drow = page.locator(`${view} .lib-memdistill`);
        assert.match(await drow.locator(".name").textContent(), new RegExp(w.learn));
        assert.equal(await drow.locator("button.lib-switch").getAttribute("aria-checked"), "false");
        assert.equal(await drow.locator(".lib-distmodel, .lib-distnow").count(), 0);
        // nor is memory tidied; switched on, it picks its model on its own row while conversations aren't read
        const trow = page.locator(`${view} .lib-memdream`);
        assert.match(await trow.locator(".name").textContent(), new RegExp(w.tidy));
        assert.equal(await trow.locator("button.lib-switch").getAttribute("aria-checked"), "false");
        assert.equal(await trow.locator(".lib-distmodel, .lib-dreamnow, .lib-dreamundo").count(), 0);
        await trow.locator("button.lib-switch").click();
        await trow.locator(".lib-dreamnow").waitFor();
        assert.deepEqual(sent.findLast((x) => x.path === "/api/memory/dream").body, { on: true });
        assert.match(await trow.locator(".lib-distmodel").textContent(), /Small/);
        assert.equal(await drow.locator(".lib-distmodel").count(), 0);
        {
          const card = await page.locator(`${view} .lib-memcard`).boundingBox();
          for (const b of await trow.locator(".lib-acts > *").all()) {
            const r = await b.boundingBox();
            assert.ok(r.x + r.width <= card.x + card.width + 1, "a control of the tidy row is cut off");
          }
        }

        // in use: the pinned one first, then the newer one; the one guessed waits
        assert.equal(await rows.count(), 2);
        assert.match(await rows.nth(0).textContent(), /Answers in Chinese/);
        assert.match(await rows.nth(1).textContent(), /Use pnpm/);
        assert.match(await rows.nth(1).textContent(), /magpie/);
        assert.match(await rows.nth(1).textContent(), new RegExp(w.rule));
        // Magpie Memory isn't in the library: it is offered
        assert.equal(await page.locator(`${view} .lib-memtools`).count(), 1);

        // the search narrows the list
        await page.locator(`${view} input.lib-memq`).fill("pnpm");
        await page.waitForFunction(() => document.querySelectorAll("#view-library .lib-mems .lib-mem").length === 1);
        await page.locator(`${view} input.lib-memq`).fill("");
        await page.waitForFunction(() => document.querySelectorAll("#view-library .lib-mems .lib-mem").length === 2);

        if (width === 980) {
          // edited: the kind through the app's menu
          await rows.nth(1).click();
          const ta = page.locator(`${view} textarea[data-lib="mem:m1"]`);
          await ta.waitFor();
          assert.equal(await page.locator(`${view} select`).count(), 0, "a native select");
          await ta.fill("Use pnpm in this repo");
          await page.locator(`${view} .lib-memdetail .lib-mempick`).first().click();
          await page.locator(".proto-menu .pm-item", { hasText: w.fact }).click();
          await page.locator(`${view} .lib-memdetail .primary`).click();
          await page.waitForFunction(() => !document.querySelector("#view-library .lib-memdetail"));
          let s = sent.findLast((x) => x.path === "/api/memory");
          assert.deepEqual({ id: s.body.id, text: s.body.text, kind: s.body.kind, scope: s.body.scope }, { id: "m1", text: "Use pnpm in this repo", kind: "fact", scope: "project:p1" });

          // added: everywhere by default, the user's own
          await page.locator(`${view} .lib-memadd`).click();
          const nt = page.locator(`${view} textarea[data-lib="mem:new"]`);
          await nt.waitFor();
          await nt.fill("Never push to main");
          await page.locator(`${view} input.lib-memtags`).fill("git, 规则");
          await page.locator(`${view} .lib-memdetail .primary`).click();
          await page.waitForFunction(() => document.querySelectorAll("#view-library .lib-mems .lib-mem").length === 3);
          s = sent.findLast((x) => x.path === "/api/memory");
          assert.equal(s.body.id, undefined);
          assert.deepEqual([s.body.text, s.body.scope, s.body.tags], ["Never push to main", "global", ["git", "规则"]]);

          // the one guessed is kept
          await page.locator(`${view} .lib-memstatus .opt`, { hasText: w.review }).click();
          await page.waitForFunction(() => document.querySelectorAll("#view-library .lib-mems .lib-mem").length === 1);
          const guessed = page.locator(`${view} .lib-mems .lib-mem`).first();
          assert.match(await guessed.textContent(), /Codex/);
          await guessed.click();
          await page.locator(`${view} .lib-memdetail .lib-acts button`).nth(1).click();
          await page.waitForFunction(() => document.querySelectorAll("#view-library .lib-mems .lib-mem").length === 0);
          s = sent.findLast((x) => x.path === "/api/memory");
          assert.deepEqual(s.body, { id: "m3", status: "active" });

          // deleted, on a second click
          await page.locator(`${view} .lib-memstatus .opt`).first().click();
          await page.waitForFunction(() => document.querySelectorAll("#view-library .lib-mems .lib-mem").length === 4);
          await page.locator(`${view} .lib-mems .lib-mem`, { hasText: "Never push" }).click();
          const del = page.locator(`${view} .lib-memdetail .danger`);
          await del.click();
          assert.equal(sent.filter((x) => x.path === "/api/memory/remove").length, 0);
          await page.locator(`${view} .lib-memdetail .danger-fill`).click();
          await page.waitForFunction(() => document.querySelectorAll("#view-library .lib-mems .lib-mem").length === 3);
          assert.deepEqual(sent.findLast((x) => x.path === "/api/memory/remove").body, { ids: ["m4"] });

          // the summary switched off
          await page.locator(`${view} .lib-memdigest button.lib-switch`).click();
          await page.waitForFunction(() => document.querySelector("#view-library .lib-memdigest button.lib-switch")?.getAttribute("aria-checked") === "false");
          assert.deepEqual(sent.findLast((x) => x.path === "/api/memory/digest").body, { on: false });
        }

        // conversations read: switched on, it reads at once with the automatic model
        await drow.locator("button.lib-switch").click();
        await page.waitForFunction(() => document.querySelector("#view-library .lib-memdistill button.lib-switch")?.getAttribute("aria-checked") === "true");
        assert.deepEqual(sent.findLast((x) => x.path === "/api/memory/distill").body, { on: true, model: "" });
        assert.match(await drow.locator(".lib-distmodel").textContent(), /Small/);
        assert.equal(await trow.locator(".lib-distmodel").count(), 0, "a model picked on both rows");
        assert.equal(await drow.locator(".lib-distnow").count(), 0, "Read now while reading");
        // how far it has gone, and a way to stop it (RealZeroP27 on X)
        assert.match(await drow.locator(".sub").textContent(), /12\D+40/);
        assert.equal(await drow.locator(".lib-diststop").count(), 1, "no Stop while reading");
        assert.equal(await drow.locator(".lib-disteffort").count(), 1, "no reasoning level");
        const rangeShown = await drow.locator(".lib-distrange").textContent();
        assert.match(rangeShown, /7/);
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1), "the row scrolls sideways");
        const card = await page.locator(`${view} .lib-memcard`).boundingBox();
        for (const b of await drow.locator(".lib-acts > *").all()) {
          const r = await b.boundingBox();
          assert.ok(r.x + r.width <= card.x + card.width + 1, "a control of the row is cut off");
        }
        // the run ends: Read now is back, and what it proposed waits under To review
        await drow.locator(".lib-distnow").waitFor({ timeout: 8000 });
        assert.match(await drow.locator(".lib-distnow").textContent(), new RegExp(w.now));
        // every control of a row shares one centre line, the switch too
        for (const row of [drow, trow]) {
          const mids = [];
          for (const c of await row.locator(".lib-acts > *").all()) {
            const r = await c.boundingBox();
            mids.push(r.y + r.height / 2);
          }
          // a narrow row wraps: a gap over half a control's height starts a line
          mids.sort((a, b) => a - b);
          assert.ok(mids.length > 1, "a row with one control");
          let first = mids[0];
          for (let i = 1; i < mids.length; i++) {
            if (mids[i] - mids[i - 1] > 12) { first = mids[i]; continue; }
            assert.ok(mids[i] - first <= 1, "controls off one centre line: " + mids.join(", "));
          }
        }
        assert.match(await drow.locator(".sub").textContent(), /1/);
        await page.locator(`${view} .lib-memstatus .opt`, { hasText: w.review }).click();
        await page.locator(`${view} .lib-mems .lib-mem`, { hasText: "Ships on Tuesdays" }).waitFor();

        if (width === 980) {
          // a model picked through the app's menu, not a <select>
          await drow.locator(".lib-distmodel").click();
          await page.locator(".proto-menu .pm-item", { hasText: "Model One" }).click();
          await page.waitForFunction(() => /Model One/.test(document.querySelector("#view-library .lib-distmodel")?.textContent || ""));
          assert.deepEqual(sent.findLast((x) => x.path === "/api/memory/distill").body, { on: true, model: "dt/m1" });
          assert.equal(await page.locator(`${view} select`).count(), 0, "a native select");
          await drow.locator(".lib-distnow").waitFor({ timeout: 8000 });
          // a reasoning level picked: the model's own levels, Default first
          assert.equal(await drow.locator(".lib-disteffort .effort-ic").count(), 1);
          await drow.locator(".lib-disteffort").click();
          assert.equal(await page.locator(".proto-menu .pm-item").count(), 4);
          await page.locator(".proto-menu .pm-item").nth(3).click();
          await page.waitForFunction(() => document.querySelector("#view-library .lib-disteffort")?.textContent.trim() !== "" && !document.querySelector(".proto-menu"));
          assert.deepEqual(sent.findLast((x) => x.path === "/api/memory/distill").body, { on: true, model: "dt/m1", effort: "high" });
          // how far back: 7 days unless picked; All reads at once
          await drow.locator(".lib-distrange").click();
          assert.equal(await page.locator(".proto-menu .pm-item").count(), 5);
          await page.locator(".proto-menu .pm-item").nth(4).click();
          assert.deepEqual(sent.findLast((x) => x.path === "/api/memory/distill").body, { on: true, model: "dt/m1", days: -1 });
          await page.waitForFunction((was) => document.querySelector("#view-library .lib-distrange")?.textContent !== was, rangeShown);
          // stopped: what it left is said, and Read now reads the rest
          assert.match(await drow.locator(".sub").textContent(), /3\D+400/);
          await drow.locator(".lib-diststop").click();
          await drow.locator(".lib-distnow").waitFor({ timeout: 8000 });
          assert.ok(sent.some((x) => x.path === "/api/memory/distill/stop"));
          assert.match(await drow.locator(".sub").textContent(), /397/);
          assert.equal(await drow.locator(".lib-diststop").count(), 0);
          // a run that stopped says why
          await drow.locator(".lib-distnow").click();
          await drow.locator(".sub.err").waitFor();
          assert.match(await drow.locator(".sub.err").textContent(), /429: too many requests/);
          assert.ok(sent.some((x) => x.path === "/api/memory/distill/run"));
        }

        // tidied now: what it set aside leaves To review, and is undone once
        await page.locator(`${view} .lib-memstatus .opt`, { hasText: w.review }).click();
        await page.locator(`${view} .lib-mems .lib-mem`, { hasText: "Ships on Tuesdays" }).waitFor();
        await trow.locator(".lib-dreamnow").click();
        await trow.locator(".lib-dreamundo").waitFor({ timeout: 8000 });
        assert.ok(sent.some((x) => x.path === "/api/memory/dream/run"));
        assert.match(await trow.locator(".sub").textContent(), /1/);
        await page.locator(`${view} .lib-mems .lib-mem`, { hasText: "Ships on Tuesdays" }).waitFor({ state: "detached" });
        await trow.locator(".lib-dreamundo").click();
        await page.locator(`${view} .lib-mems .lib-mem`, { hasText: "Ships on Tuesdays" }).waitFor();
        assert.equal(await trow.locator(".lib-dreamundo").count(), 0);
        assert.ok(sent.some((x) => x.path === "/api/memory/dream/undo"));
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1), "the page scrolls sideways");
        await ctx.close();
      });
    }
    assert.deepEqual(errors, []);
  });
}
