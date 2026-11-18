// Run with Node's test runner and Playwright on the module path; see README.md.
// A setting on another page is found from Settings' search (canlee886 on X:
// a search that goes to the setting, a routing group's among them). ⌘K
// (Ctrl+K off the Mac) and the header's magnifier bring the search up from
// any page, its box focused. A group's strategy is found by "strategy" in
// English and by "Routing strategies" in the language shown, a row per
// group under the Routing groups page's name; a group by its name; a
// provider's Proxy under Providers; Library's tabs under Library. Open goes
// to that page, opens the group's editor (its Advanced too) or the
// provider's, and lights the field, in sight; Back comes back to the
// search. An Open no click asked for moves nothing. Nothing is posted. In
// the smallest window (560 x 420) and magpie web on a phone (390 wide)
// nothing runs off to the side. Every language, Chromium and WebKit; no
// backend, the API is faked here. ARTIFACT_DIR gets screenshots.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { provPrefsProviders } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { id: "oa/gpt-6.1-sol", name: "gpt-6.1-sol", providerName: "OpenAI", icon: "generic", context: 1000000 },
  { id: "an/claude-opus-5-5", name: "claude-opus-5-5", providerName: "Anthropic", icon: "generic", context: 200000 },
];
const members = models.map((m) => m.id);
const group = (id, name) => ({ id, name, members, routing: "order", ready: true, memberInfo: members.map((m) => ({ id: m, ready: true })) });
const groups = { models, pools: [], groups: [group("sol", "Sol"), group("opus", "Opus pool")] };

function server(lang, posts, web) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings: { lang, theme: "light", textSize: 100 } };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (req.method() !== "GET" && url.pathname !== "/api/update/using" && url.pathname !== "/api/page-away") posts.push(url.pathname);
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${web},textSize:100};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/settings") return json({ theme: "light", lang, textSize: 100, version: "0.1.400", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425", proxyNow: "none", proxySource: "none", lanURLs: [], redactWords: [], fx: { rate: 7.2, at: new Date().toISOString(), stale: false } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/providers") return json(provPrefsProviders(["codex"], { codex: "ChatGPT" }));
    if (url.pathname === "/api/groups") return json(groups);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {});
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/window/fit") return route.fulfill({ status: 204 });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// the rows found on the other pages: each page's name and its rows
const found = (page) => page.evaluate(() => [...document.querySelectorAll("#setElse .find-page")].filter((s) => s.getClientRects().length)
  .map((s) => [s.dataset.part, [...s.querySelectorAll(".find-row")].map((r) => [r.querySelector(".name").textContent, r.querySelector(".sub")?.textContent || ""])]));
const overflow = (page) => page.evaluate(() => {
  const out = [], de = document.documentElement;
  if (de.scrollWidth > de.clientWidth) out.push("page " + de.scrollWidth + ">" + de.clientWidth);
  for (const v of document.querySelectorAll(".view:not([hidden])")) if (v.scrollWidth > v.clientWidth + 0.5) out.push(v.id + " " + v.scrollWidth + ">" + v.clientWidth);
  for (const r of document.querySelectorAll("#setElse .find-row")) {
    const b = r.getBoundingClientRect(), go = r.querySelector("button").getBoundingClientRect();
    if (go.right > b.right + 0.5) out.push("Open past its row: " + r.textContent);
  }
  return out;
});
const inSight = (loc) => loc.evaluate((e) => {
  const r = e.getBoundingClientRect();
  return r.height > 0 && r.top >= 0 && r.bottom <= innerHeight;
});
const viewIs = (page, v) => page.waitForFunction((v) => !document.querySelector("#view-" + v).hidden, v);

const LANGS = ["en", "zh", "zh-TW", "ja", "de"];
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a setting on any page, from Settings' search", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const shots = [];
    t.after(async () => {
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        for (const [name, buf] of shots) await fs.writeFile(path.join(process.env.ARTIFACT_DIR, `${engine}-find-anywhere-${name}.png`), buf);
      }
      await browser.close();
    });
    const open = async (lang, posts, { width = 560, height = 420, web = false, at = "agents" } = {}) => {
      const context = await browser.newContext({ viewport: { width, height }, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      page.errors = [];
      page.on("pageerror", (e) => {
        if (engine === "webkit" && /due to access control checks/.test(e.message)) return; // a goto in WebKit can cut off a fetch (#1350)
        if (/^ResizeObserver loop completed with undelivered notifications/.test(e.message)) return;
        page.errors.push(e.message);
      });
      await page.route("**/*", server(lang, posts, web));
      await page.goto("http://magpie.test/?view=" + at);
      await viewIs(page, at);
      await page.waitForFunction(() => window.routingFind?.()?.groups.length === 2, null, { timeout: 30000 });
      return page;
    };
    const type = async (page, s) => {
      await page.locator("#setQ").fill(s);
      await page.waitForFunction(() => window.setFind && document.querySelector("#setElse"));
      await page.waitForTimeout(60);
    };

    for (const lang of LANGS) {
      await t.test(lang, async () => {
        const posts = [];
        const page = await open(lang, posts);
        const tr = (s, vars) => page.evaluate(([s, vars]) => t(s, vars), [s, vars]);
        const mac = await page.evaluate(() => /^Mac/.test(navigator.platform));

        // ⌘K from the Agents page: Settings' search, focused, Back to Agents
        await page.keyboard.press(mac ? "Meta+k" : "Control+k");
        await viewIs(page, "settings");
        assert.equal(await page.evaluate(() => document.activeElement?.id), "setQ");
        assert(await page.locator("#navBack").isVisible(), "Back goes to the page it came from");

        // a group's strategy, by the English word and by the language's
        const routing = await tr("Routing");
        const strategyRows = [[await tr("Routing groups"), [[routing, "Sol"], [routing, "Opus pool"]]]];
        await type(page, "strategy");
        assert.deepEqual(await found(page), strategyRows);
        const sec = page.locator('#setElse .find-page[data-page="groups"]');
        assert.equal(await sec.evaluate((s) => getComputedStyle(s, "::before").content), JSON.stringify(await tr("Routing groups")));
        assert(await page.locator("#setNone").isHidden(), "found: no 'nothing matches'");
        assert.deepEqual(await overflow(page), []);
        shots.push([lang + "-strategy", await page.screenshot()]);
        await type(page, await tr("Routing strategies"));
        assert.deepEqual(await found(page), strategyRows);

        // an Open no click asked for opens the editor and moves nothing
        await page.locator("#setElse .find-row", { hasText: "Opus pool" }).locator("button").evaluate((b) => b.click());
        await viewIs(page, "groups");
        const ed = page.locator(".rt-gedit");
        await ed.waitFor();
        const lit = ed.locator("label.find-lit");
        await lit.waitFor();
        assert.equal(await lit.textContent(), routing);
        assert.equal(await page.locator("#view-groups").evaluate((v) => v.scrollTop), 0, "no scroll the reader didn't ask for");
        assert.equal(await inSight(lit), false, "the field is below the fold at 420 high");

        // Back: the search as it was; the reader's click on Sol's Routing
        await page.locator("#navBack").click();
        await viewIs(page, "settings");
        assert.equal(await page.locator("#setQ").inputValue(), await tr("Routing strategies"));
        await page.waitForFunction(() => document.querySelectorAll("#setElse .find-row").length === 2);
        await page.locator("#setElse .find-row", { hasText: "Sol" }).locator("button").click();
        await viewIs(page, "groups");
        await page.locator(".rt-gedit label.find-lit").waitFor();
        const name = page.locator(".rt-gedit input").first();
        assert.equal(await name.inputValue(), "Sol");
        const l = page.locator(".rt-gedit label.find-lit");
        assert.equal(await l.textContent(), routing);
        assert(await l.evaluate((x) => x.closest("details").open), "Advanced opened");
        await page.waitForFunction(() => {
          const r = document.querySelector(".rt-gedit label.find-lit").getBoundingClientRect();
          return r.top >= 0 && r.bottom <= innerHeight;
        });
        assert.deepEqual(await overflow(page), []);
        shots.push([lang + "-group-routing", await page.screenshot()]);

        // a header short of room drops the magnifier before the name (⌘K stays)
        assert.equal(await page.locator("#find").isVisible(), false, lang + ": magnifier at 560");
        // a group by its name, from the header's magnifier: its row, not its fields
        await page.setViewportSize({ width: 1100, height: 700 });
        await page.locator("#find").click();
        await viewIs(page, "settings");
        assert.equal(await page.evaluate(() => document.activeElement?.id), "setQ");
        await type(page, "sol");
        assert.deepEqual(await found(page), [[await tr("Routing groups"), [["Sol", ""]]]]);
        await page.setViewportSize({ width: 560, height: 420 });

        // a provider's Proxy, under Providers; Open lights it in the editor
        await type(page, await tr("Proxy"));
        const provs = await tr("Providers");
        assert.deepEqual((await found(page)).find(([p]) => p === provs), [provs, [[await tr("Proxy"), "ChatGPT"]]]);
        await page.locator('#setElse .find-page[data-page="providers"] .find-row button').click();
        await viewIs(page, "providers");
        const pl = page.locator("#modal .editor label.find-lit");
        await pl.waitFor();
        assert.equal((await pl.textContent()).trim(), await tr("Proxy"));

        // Library's tabs under Library, the shortcut over the editor's modal
        await page.keyboard.press(mac ? "Meta+k" : "Control+k");
        await viewIs(page, "settings");
        await page.waitForFunction(() => document.querySelector("#modal").hidden, null, { timeout: 3000 }); // the editor's modal closed
        await type(page, "skills");
        const lib = await tr("Library");
        assert.deepEqual((await found(page)).find(([p]) => p === lib), [lib, [[await tr("Skills"), ""]]]);
        // and nothing found says so
        await type(page, "zzqx");
        assert.deepEqual(await found(page), []);
        assert(await page.locator("#setNone").isVisible());

        assert.deepEqual(posts, []);
        assert.deepEqual(page.errors, []);
        await page.context().close();
      });
    }

    await t.test("magpie web on a phone", async () => {
      const posts = [];
      const page = await open("en", posts, { width: 390, height: 700, web: true, at: "routing" });
      await page.locator("#find").click();
      await viewIs(page, "settings");
      await type(page, "strategy");
      assert.equal((await found(page))[0][1].length, 2);
      // Open beside the place's name, not under it as a phone's settings rows are
      assert.deepEqual(await page.evaluate(() => [...document.querySelectorAll("#setElse .find-row")].filter((r) => {
        const w = r.querySelector(".who").getBoundingClientRect(), b = r.querySelector("button").getBoundingClientRect();
        return b.top >= w.bottom - 1 || b.left < w.right;
      }).map((r) => r.textContent)), []);
      assert.deepEqual(await overflow(page), []);
      shots.push(["web-390", await page.screenshot()]);
      await page.locator("#setElse .find-row", { hasText: "Sol" }).locator("button").click();
      await viewIs(page, "groups");
      await page.locator(".rt-gedit label.find-lit").waitFor();
      assert.deepEqual(await overflow(page), []);
      assert.deepEqual(posts, []);
      assert.deepEqual(page.errors, []);
      await page.context().close();
    });
  });
}
