// Run with Node's test runner and Playwright on the module path; see README.md.
// Kiro Crew (Vincent on Discord) runs on a harness, an agent connected to
// magpie. Connecting it with none connected, or with its harness set in
// its config.local.json, is refused, and the refusal reads in the
// window's language; connected, the notice that new sessions take the new
// harness does too, and so does the one that magpie connected the harness
// Crew is set to along with it (wuping9525: Claude Code, not connected). In every language, in Chromium and WebKit, at a wide
// and a narrow window. No backend: the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const NO_HARNESS = "Kiro Crew runs on a harness: connect Codex, Claude Code, OpenCode, Pi or Goose to magpie first, then connect Kiro Crew";
const PINNED = "Kiro Crew's config.local.json sets its harness, and that wins over config.json: magpie leaves it alone. Change agent.acp_backend there, or take it out, then connect again";
const NEW_SESSIONS = "Kiro Crew takes the new harness for new sessions; sessions already open keep theirs.";
const JOINED = "Kiro Crew runs on {harness}, so magpie connected {harness} too; turning Kiro Crew off disconnects it again.";
const CONNECTED = NEW_SESSIONS + " " + JOINED.replaceAll("{harness}", "Claude Code");
const NOTE = "its harness, an agent connected to magpie, runs on magpie's models";
const crew = (on) => ({
  id: "kirocrew", name: "Kiro Crew", icon: "generic", path: "/test/kiro/crew/config.json", wired: on,
  fields: [
    { key: "provider", label: "provider", value: on ? "magpie" : "", options: [{ value: "magpie", label: "magpie", icon: "magpie", note: NOTE }] },
    { key: "harness", label: "harness", value: on ? "codex" : "", quiet: true, options: [{ value: "codex", label: "Codex", icon: "codex-color" }] },
  ],
});

function server(lang, answers) {
  let on = false;
  const st = () => ({ agents: [crew(on)], profiles: [], settings: { lang, theme: "light", guideHidden: true } });
  return async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: st() });
    if (url.pathname === "/api/agents/connect/kirocrew") {
      const a = answers.shift();
      if (a) return route.fulfill({ status: 400, json: { error: a } });
      on = true;
      return route.fulfill({ json: { ...st(), connected: { how: "magpie" }, notice: CONNECTED } });
    }
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/agents/cli") return route.fulfill({ json: { agents: {}, pending: false } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], presets: [], excluded: [], gateway: { running: true } } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": Kiro Crew's refusals and notice read in the window's language", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      for (const width of [1100, 420]) {
        await t.test(`${lang} at ${width}px`, async () => {
          const page = await (await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" })).newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", server(lang, [NO_HARNESS, PINNED]));
          await page.goto("http://magpie.test/?view=agents");
          const sw = page.locator('.row.agent[data-id="kirocrew"] .ag-conn');
          await sw.waitFor();
          // each word is said in this language, not English
          const words = await page.evaluate((ws) => ws.map((w) => [w, t(w)]), [NO_HARNESS, PINNED, NEW_SESSIONS, JOINED, NOTE, "harness"]);
          for (const [en, said] of words) if (lang !== "en" && en !== "harness" || lang === "de" && en === "harness") assert.notEqual(said, en, `${lang}: ${en}`);
          for (const refusal of [NO_HARNESS, PINNED]) {
            const want = await page.evaluate((w) => t(w), refusal);
            await page.evaluate(() => (document.querySelector("#status").textContent = ""));
            await sw.click();
            await page.waitForFunction((w) => document.querySelector("#status")?.textContent.includes(w), want);
            assert.equal(await sw.getAttribute("aria-checked"), "false", "the switch goes back off");
          }
          await page.evaluate(() => (document.querySelector("#status").textContent = ""));
          await sw.click();
          await page.waitForFunction(() => document.querySelector('.row.agent[data-id="kirocrew"] .ag-conn')?.getAttribute("aria-checked") === "true", null, { timeout: 30000 });
          await page.waitForFunction(() => document.querySelector("#status")?.textContent.trim(), null, { timeout: 30000 });
          const said = (await page.locator("#status").textContent()).trim();
          const want = await page.evaluate((n) => tNotice(n), CONNECTED);
          assert.ok(said.endsWith(want), said);
          if (lang !== "en") assert.ok(!said.includes("sessions already open") && !said.includes("magpie connected"), said);
          assert.ok(said.includes("Claude Code") && !said.includes("{harness}"), said);
          const fits = await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth);
          assert.ok(fits, "the page runs past the window");
        });
      }
    }
    assert.deepEqual(errors, []);
  });
}
