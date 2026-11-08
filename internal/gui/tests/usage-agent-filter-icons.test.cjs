// Run with Node's test runner and Playwright on the module path; see README.md.
// Usage's agent filters show each agent's icon beside its name (the owner:
// "这里应该有Agent的icon"), on Requests and on Sessions: an agent's own
// logo, the generic one for a client magpie doesn't know (AsyncOpenAI,
// Go-http-client), and the All row keeps the icon's room blank, so every
// name starts at one x. Each icon sits on its name's line. English and
// Chinese, Chromium and WebKit; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();
const LED_AGENTS = [{ id: "AsyncOpenAI", name: "AsyncOpenAI", icon: "generic" }, { id: "claude", name: "Claude Code", icon: "claudecode-color" }, { id: "codex", name: "Codex", icon: "codex-color" }, { id: "x", name: "Go-http-client" }];
const rows = LED_AGENTS.map((a, i) => ({ t: new Date(now - (60 + i * 60) * 1e3).toISOString(), agent: a.id, agentName: a.name, icon: a.icon || "generic",
  provider: "relay", providerName: "Relay", model: "m", in: 1000, out: 100, ms: 900, status: 200, cost: 0.001, priced: true }));
const iso = (d) => [d.getFullYear(), d.getMonth() + 1, d.getDate()].map((n) => String(n).padStart(2, "0")).join("-");
const today = iso(new Date());

function serve(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/gateway/trace") return json({ mine: false, seq: 0, routes: [], totals: { requests: 0, rerouted: 0, errors: 0 }, now: new Date().toISOString() });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "pi", name: "Pi", icon: "pi", path: "/x/pi", fields: [] }], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/usage/requests") return json({ period: "all", rows, offset: 0, total: rows.length, calls: rows.length, errors: 0, input: 4000, output: 400, cost: 0.004, agents: LED_AGENTS });
    if (url.pathname === "/api/usage") return json({ calls: 4, input: 4000, output: 400, cost: 0.004, bucket: "day", series: [], agents: [], models: [], path: "~/.config/magpie/usage.jsonl" });
    if (url.pathname === "/api/sessions/progress") return json({ indexing: false });
    // claude from a session's own icon, pi (no session here) from the agents magpie knows
    if (url.pathname === "/api/sessions") return json({ sessions: [{ id: "s1", agent: "claude", name: "Claude Code", icon: "claudecode-color", cwd: "/w", title: "a", updated: new Date(now).toISOString() }], dirs: [] });
    if (url.pathname === "/api/sessions/stats") return json({ from: today, to: today, agents: { claude: "Claude Code", pi: "Pi", odd: "Odd" },
      days: [{ date: today, active: [], usage: ["claude", "pi", "odd"].map((a) => ({ agent: a, cwd: "/w", model: "m", input: 1000, output: 100, cache_read: 0, cache_write: 0, cost: 0.1, priced: true })) }] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], models: [], gateway: { running: false } });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    return body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" });
  };
}

// each row of the open menu: its name, its icon, and where they sit
const menuRows = (page) => page.$$eval(".sess-menu .pm-item", (items) => items.map((b) => {
  const ic = b.querySelector(":scope > .ic"), n = b.querySelector(".pm-name").getBoundingClientRect();
  const r = ic?.getBoundingClientRect();
  return { name: b.querySelector(".pm-name").textContent, icon: ic ? ic.dataset.icon ?? "" : null, drawn: !!ic?.firstElementChild,
    nameX: n.left, nameMid: n.top + n.height / 2, icMid: r ? r.top + r.height / 2 : null };
}));

function check(got, want, all) {
  const at = JSON.stringify(got);
  assert.deepEqual(got.map((r) => [r.name, r.icon]), [[all, ""], ...want], at);
  assert.equal(got[0].drawn, false, "the All row's icon room is blank: " + at);
  assert(got.slice(1).every((r) => r.drawn), "every agent's icon is drawn: " + at);
  assert(got.every((r) => Math.abs(r.nameX - got[0].nameX) < 0.5), "every name starts at one x: " + at);
  assert(got.slice(1).every((r) => Math.abs(r.icMid - r.nameMid) <= 1), "each icon sits on its name's line: " + at);
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: Usage's agent filters show each agent's icon`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const errors = [];
      const open = async (tab) => {
        const page = await (await browser.newContext({ viewport: { width: 1100, height: 700 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(8000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang));
        await page.addInitScript((tab) => { localStorage.setItem("magpie.usageTab", tab); localStorage.setItem("magpie.sessRange", "all"); localStorage.setItem("magpie.usageEvery", "0"); }, tab);
        // as a user comes to it: magpie open, its agents read, then Usage
        await page.goto("http://magpie.test/");
        await page.waitForFunction(() => state.agents.length > 0);
        await page.locator('[data-view="usage"]').first().click();
        return page;
      };
      let page = await open("requests");
      const all = await page.evaluate(() => t("All agents"));

      await page.locator("#ledWrap .led tbody tr").first().waitFor();
      await page.locator("#ledAgent").click();
      await page.locator(".sess-menu .pm-item").first().waitFor();
      check(await menuRows(page), [["AsyncOpenAI", "generic"], ["Claude Code", "claudecode-color"], ["Codex", "codex-color"], ["Go-http-client", "generic"]], all);
      // the search narrows the rows, icons and all
      await page.locator(".sess-menu .sc-q").fill("codex");
      assert.deepEqual((await menuRows(page)).map((r) => [r.name, r.icon]), [["Codex", "codex-color"]]);
      await page.keyboard.press("Escape");

      page = await open("sessions");
      await page.locator("#sessAgent").waitFor({ state: "visible" });
      await page.locator("#sessAgent").click();
      await page.locator(".sess-menu .pm-item").first().waitFor();
      const got = await menuRows(page);
      check(got.slice(0, 1).concat(got.slice(1).sort((a, b) => a.name.localeCompare(b.name))), [["Claude Code", "claudecode-color"], ["Odd", "generic"], ["Pi", "pi"]], all);
      assert.deepEqual(errors, []);
    });
  }
}
