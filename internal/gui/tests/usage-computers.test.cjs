// Run with Node's test runner and Playwright on the module path; see README.md.
// Usage shared between computers through sync (#542): with no other
// computer's usage the Usage page and the panel have no Computer filter, as
// before; with one, the Requests have a Computer pick (this computer, each
// other one) that asks for computer=, the panel's Usage tab has one too, a
// row made on another computer says so beside its agent, and its details
// name the computer and say what was said stays there, nothing asked of this
// computer's session files for it. The sync form's Usage tick posts usage:
// true, and leaving it off posts nothing new. No click moves the page.
// English and Chinese; the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const reader = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();
const STUDIO = "a1b2c3d4e5f60718";
const row = (i, model, computer) => ({
  t: new Date(now - (i + 1) * 3600e3).toISOString(), agent: "claude", agentName: "Claude Code", icon: "claudecode-color", provider: "relay", providerName: "Relay",
  req: model, model, served: model, in: 300, out: 40, ms: 2000, status: 200, rid: "req_" + i, ep: "/v1/messages", cost: 0.01, priced: true,
  ...(computer ? { computer, computerName: "studio" } : {}),
});
const ROWS = [row(0, "here"), row(1, "there", STUDIO)];
const share = (id, name, calls) => ({ id, name, icon: "", calls, errors: 0, input: 300 * calls, output: 40 * calls, cache_write: 0, cache_read: 0, cost: 0.01 * calls });

function ledger(q, shared) {
  const c = q.get("computer") || "";
  const rows = ROWS.filter((r) => !c || (c === "this" ? !r.computer : r.computer === c || (c === "others" && r.computer)));
  return {
    period: q.get("period"), rows, offset: 0, total: rows.length, calls: rows.length, errors: 0, input: 300 * rows.length, output: 40 * rows.length,
    cache_read: 0, cache_write: 0, reasoning: 0, cost: 0.01 * rows.length, unpriced: 0, bucket: "hour", series: [],
    agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color" }], providers: [{ id: "relay", name: "Relay", icon: "generic" }],
    by: { provider: [share("relay", "Relay", rows.length)], agent: [], model: [] },
    ...(shared ? { computers: [share("this", "", 1), share(STUDIO, "studio", 1)] } : {}),
  };
}

function server(lang, shared, asked, contents, posts) {
  let sync = { on: false, keys: true, agents: true, library: true };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light", tray: "panel", version: "test", dir: "/tmp/magpie", visionModels: [], imageGenModels: [], fx: { rate: 7.2, stale: false } } });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true, mine: true, groups: [], calls: [] } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/requests") { asked.push(url.searchParams); return json(ledger(url.searchParams, shared)); }
    if (url.pathname === "/api/usage/requests/content") { contents.push(url.search); return json({ found: false, why: "read" }); }
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage") return json({ calls: 2, errors: 0, input: 600, output: 80, cache_read: 0, cache_write: 0, reasoning: 0, unpriced: 0, cost: 0.02, bucket: "day", series: [], agents: [], models: [], path: "~/.config/magpie/usage.jsonl" });
    if (url.pathname === "/api/sessions") return json({ sessions: [], dirs: [] });
    if (url.pathname === "/api/sessions/stats") return json({ from: "", to: "", days: [], agents: {} });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/gateway/trace") return json({ mine: true, now: new Date().toISOString(), seq: 0, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/window/fit" || url.pathname === "/api/window/main") return route.fulfill({ status: 204 });
    if (url.pathname === "/api/davsync") return json(sync);
    if (url.pathname === "/api/davsync/save") {
      const b = req.postDataJSON();
      posts.push(b);
      sync = { on: true, kind: "webdav", url: b.url, passwordSet: true, passphraseSet: true, keys: b.keys, agents: b.agents, library: b.library, usage: !!b.usage };
      return json(sync);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const L = {
  en: { all: "All computers", here: "This computer", on: "Claude Code · on studio", key: "Computer", stays: "What was said stays on studio: sync shares the usage alone.",
    tick: "Usage: this computer's calls, for the Usage page on the others" },
  zh: { all: "全部电脑", here: "本机", on: "Claude Code · 在 studio", key: "电脑", stays: "对话内容留在 studio 上：同步只共享用量。",
    tick: "用量：本机的请求，在其他电脑的用量页里显示" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": usage of the other computers", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) {
      const w = L[lang];
      const open = async (url, viewport, shared, asked = [], contents = [], posts = []) => {
        const ctx = await browser.newContext({ viewport, reducedMotion: "reduce" });
        t.after(() => ctx.close());
        const p = await ctx.newPage();
        p.setDefaultTimeout(5000);
        const errors = [];
        p.on("pageerror", (e) => errors.push(e.message));
        await p.route("**/*", server(lang, shared, asked, contents, posts));
        await p.goto(url);
        return { p, errors };
      };
      const requests = async (p) => {
        await p.locator('[data-view="usage"]').first().click();
        await p.locator("#usageTab .opt").nth(1).click();
        await p.locator(".led tbody tr.led-row").first().waitFor();
      };

      await t.test(`${lang}: no other computer's usage, no Computer filter`, async () => {
        const { p, errors } = await open("http://magpie.test/", { width: 1180, height: 640 }, false);
        await requests(p);
        assert.equal(await p.locator(".led tbody tr.led-row").count(), 2);
        assert(await p.locator("#ledComputer").isHidden(), "no Computer pick");
        const q = await open("http://magpie.test/?mode=panel", { width: 440, height: 340 }, false);
        await q.p.locator('[data-ptab="stats"]').click();
        await q.p.locator("#panelUsage .pu-tot").waitFor();
        assert.equal(await q.p.locator("#panelUsage .sess-pick").count(), 1, "the provider pick alone");
        assert.deepEqual([...errors, ...q.errors], []);
      });

      await t.test(`${lang}: a Computer filter, and another computer's row`, async () => {
        const asked = [], contents = [];
        const { p, errors } = await open("http://magpie.test/", { width: 1180, height: 640 }, true, asked, contents);
        await requests(p);
        const pick = p.locator("#ledComputer");
        await pick.waitFor();
        assert.equal((await pick.textContent()).trim(), w.all);
        const rows = p.locator(".led tbody tr.led-row");
        assert.equal(await rows.count(), 2);
        assert((await rows.nth(1).innerText()).includes(w.on), await rows.nth(1).innerText());
        assert(!(await rows.nth(0).innerText()).includes("studio"), "this computer's row says nothing of it");

        const top = () => p.locator("#view-usage").evaluate((v) => v.scrollTop);
        const press = async (loc) => {
          await reader.inView(p, loc);
          const at = await top();
          await loc.click();
          await p.waitForTimeout(200);
          assert.equal(await top(), at, "a click moved the page");
        };
        // the other computer's details: named, and nothing read here
        await press(rows.nth(1));
        const detail = p.locator(".led-detail").first();
        await detail.waitFor();
        const text = await detail.innerText();
        assert(text.includes(w.key) && text.includes("studio"), text);
        assert(text.includes(w.stays), text);
        await p.waitForTimeout(200);
        assert.deepEqual(contents, [], "no session file asked for another computer's call");
        await press(rows.nth(1));

        // this computer alone
        await press(pick);
        const items = p.locator(".sess-menu .pm-item .pm-name");
        assert.deepEqual(await items.allTextContents(), [w.all, w.here, "studio"]);
        await press(p.locator(".sess-menu .pm-item", { hasText: w.here }));
        for (let i = 0; i < 60 && asked.at(-1)?.get("computer") !== "this"; i++) await p.waitForTimeout(40);
        assert.equal(asked.at(-1).get("computer"), "this");
        await p.locator(".led tbody tr.led-row").nth(1).waitFor({ state: "detached" });
        assert.equal((await pick.textContent()).trim(), w.here);
        assert.deepEqual(errors, []);
      });

      await t.test(`${lang}: the panel's Computer pick`, async () => {
        const asked = [];
        const { p, errors } = await open("http://magpie.test/?mode=panel", { width: 440, height: 400 }, true, asked);
        await p.locator('[data-ptab="stats"]').click();
        await p.locator("#panelUsage .pu-tot").waitFor();
        const pick = p.locator("#panelUsage .sess-pick", { hasText: w.all });
        await pick.waitFor();
        await pick.click();
        await p.locator(".sess-menu .pm-item", { hasText: "studio" }).click();
        for (let i = 0; i < 60 && asked.at(-1)?.get("computer") !== STUDIO; i++) await p.waitForTimeout(40);
        assert.equal(asked.at(-1).get("computer"), STUDIO);
        await p.locator("#panelUsage .sess-pick", { hasText: "studio" }).waitFor();
        assert(await p.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), "the panel doesn't scroll sideways");
        assert.deepEqual(errors, []);
      });

      await t.test(`${lang}: the sync form's Usage tick`, async () => {
        const posts = [];
        const { p, errors } = await open("http://magpie.test/", { width: 900, height: 420 }, false, [], [], posts);
        await p.locator("#prefs").click();
        await p.locator("#setTab-sync").click();
        const first = p.locator("#syncList .row.pref").first();
        await first.waitFor({ state: "visible" });
        const toEnd = async () => {
          await p.mouse.move(450, 300);
          const left = () => p.locator("#view-settings").evaluate((v) => v.scrollHeight - v.clientHeight - v.scrollTop);
          for (let i = 0; i < 120 && (await left()) > 0.5; i++) { await p.mouse.wheel(0, 80); await p.waitForTimeout(15); }
          await p.waitForTimeout(300);
        };
        await toEnd();
        await first.locator(".val button").click();
        const form = p.locator("#syncList .sync-form");
        await form.waitFor({ state: "visible" });
        await form.locator(":scope > div").nth(1).locator("input").fill("https://dav.example.com/dav/");
        const box = (label) => form.locator(":scope > label:visible", { hasText: new RegExp("^" + label + "$") }).locator("xpath=following-sibling::div[1]").locator("input");
        await box(lang === "zh" ? "口令" : "Passphrase").fill("correct horse");
        await toEnd();
        const tick = form.locator("label.tick", { hasText: w.tick });
        assert.equal(await tick.locator("input").isChecked(), false, "off unless picked");
        const top = () => p.locator("#view-settings").evaluate((v) => v.scrollTop);
        const at = await top();
        await tick.click();
        await p.waitForTimeout(200);
        assert.equal(await top(), at, "the tick moved the page");
        await form.locator(".bar .primary").click();
        await form.waitFor({ state: "detached" });
        assert.equal(posts.length, 1);
        assert.equal(posts[0].usage, true);
        assert.deepEqual(errors, []);
      });
    }
  });
}
