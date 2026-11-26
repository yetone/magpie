// Run with Node's test runner and Playwright on the module path; see README.md.
// Claude Code wired to magpie while a provider switch magpie can't take out
// sends its requests elsewhere (jw1dev on X: CLAUDE_CODE_USE_BEDROCK=1 kept
// in the env, Bedrock refused magpie's model, magpie said nothing). Check
// says which variable and where, and the row says it in the window's
// language: the line under the name and the Reconnect pill carry the advice,
// with its file, variable and cloud as given. Both notices (a file, the
// shell), English, Chinese, Traditional Chinese, Japanese and German, a wide
// window and a narrow one, Chromium and WebKit. No backend: the API is faked.
//
// What setting the agent again can't change (Claude Code's managed settings,
// a switch outside the file magpie writes) is drift of its own kind,
// "outside", whose pill explains rather than reconnects; and Reconnect that
// leaves drift says what is still off, never that the agent goes through
// magpie again (jimlee0824v2 on X: T3 Code's row stayed red while Reconnect
// said it was connected).
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const FILE = "{file} sets {var}, so Claude Code sends its requests to {cloud}, not to magpie: take it out there to go through magpie.";
const SHELL = "{var} is set in your shell's environment, so Claude Code started there sends its requests to {cloud}, not to magpie: take it out of your shell's profile to go through magpie.";
const MANAGED = "Claude Code's managed settings ({file}) set ANTHROPIC_BASE_URL to {url}, which wins over magpie's: take it out there to go through magpie.";
const T3_FLAG = "The ANTHROPIC_BASE_URL in the launch arguments of magpie's instance in T3 Code ({file}) is {url}, not magpie's gateway at {gateway}.";
const T3_BASE = "{file} sets ANTHROPIC_BASE_URL to {url}, which Claude Code puts over magpie's instance in T3 Code: reconnect to have the instance's launch arguments name magpie, which win over it.";
const T3_OWN = "{file} sets ANTHROPIC_BASE_URL to {url}, which Claude Code puts over magpie's instance in T3 Code, whose own --settings names a file magpie leaves alone: take it out there to go through magpie.";
const cases = [
  { name: "managed settings", en: FILE, kind: "outside", vars: { file: "/Library/Application Support/ClaudeCode/managed-settings.json", var: "CLAUDE_CODE_USE_BEDROCK", cloud: "Amazon Bedrock" } },
  { name: "the shell", en: SHELL, kind: "outside", vars: { var: "CLAUDE_CODE_USE_VERTEX", cloud: "Google Vertex AI" } },
  { name: "settings.json", en: FILE, kind: "unwired", vars: { file: "/Users/me/.claude/settings.json", var: "CLAUDE_CODE_USE_MANTLE", cloud: "Amazon Bedrock (Mantle)" } },
  { name: "a managed base URL", en: MANAGED, kind: "outside", vars: { file: "/etc/claude-code/managed-settings.json", url: "https://corp.example" } },
  { name: "T3's flag elsewhere", en: T3_FLAG, kind: "unwired", vars: { file: "settings.json", url: "http://127.0.0.1:15721", gateway: "http://127.0.0.1:3425" } },
  { name: "T3 under CC Switch", en: T3_BASE, kind: "unwired", vars: { file: "/Users/me/.claude/settings.json", url: "http://127.0.0.1:15721" } },
  { name: "T3's own --settings", en: T3_OWN, kind: "outside", vars: { file: "/Users/me/.claude/settings.json", url: "http://127.0.0.1:15721" } },
];
const fill = (en, vars) => en.replace(/\{(\w+)\}/g, (_, k) => vars[k]);
const claude = (detail, kind = "unwired") => ({
  id: "claude", name: "Claude Code", icon: "claude", path: "/c", wired: true,
  fields: [{ key: "model", label: "model", value: "fake/m1", options: [{ value: "fake/m1", label: "m1", ref: "fake/m1" }] }],
  drift: detail ? { kind, field: "model", now: "fake/m1", want: "fake/m1", detail } : undefined,
});

// after is the detail Reconnect leaves ("" for none)
function serve(lang, detail, kind, after = detail) {
  return async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname === "/api/agents/reapply/claude") return route.fulfill({ json: { agents: [claude(after, kind)], profiles: [], settings: { lang, theme: "light" } } });
    const st = { agents: [claude(detail, kind)], profiles: [], settings: { lang, theme: "light" } };
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: st });
    if (url.pathname === "/api/drift") return route.fulfill({ json: { claude: st.agents[0].drift } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const row = `.row.agent[data-id="claude"]`;

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a Claude provider switch that bypasses magpie is said in the window's language", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      for (const width of [1100, 420]) {
        for (const c of cases) {
          await t.test(`${lang} at ${width}px: ${c.name}`, async () => {
            const page = await (await browser.newContext({ viewport: { width, height: 900 } })).newPage();
            page.setDefaultTimeout(5000);
            page.setDefaultNavigationTimeout(60000);
            page.on("pageerror", (e) => errors.push(e.message));
            const given = fill(c.en, c.vars);
            await page.route("**/*", serve(lang, given, c.kind));
            await page.goto("http://magpie.test/");
            await page.locator(`${row} .ag-fix`).waitFor();
            const want = await page.evaluate(([en, vars]) => t(en, vars), [c.en, c.vars]);
            if (lang !== "en") assert.notEqual(want, given, "untranslated");
            // the variable, the file and the cloud stay as given, in every language
            for (const v of Object.values(c.vars)) assert.ok(want.includes(v), want);

            const title = await page.locator(`${row} .ag-st-t`).getAttribute("title");
            assert.equal(title, want);
            const pill = await page.locator(`${row} .ag-fix`).getAttribute("title");
            assert.equal(pill.split("\n").slice(1).join("\n"), want);
            if (lang !== "en") {
              assert.ok(!title.includes("sends its requests"), title);
              assert.ok(!pill.includes("sends its requests"), pill);
            }
            assert.ok(await page.evaluate(() => document.scrollingElement.scrollWidth <= innerWidth), "scrolls sideways");
            const fix = page.locator(`${row} .ag-fix`);
            const how = await page.evaluate(() => t("How to fix"));
            if (c.kind === "outside") {
              // no Reconnect that can't help: the pill explains, in a dialog
              assert.equal(await fix.getAttribute("aria-label"), how);
              await fix.click();
              await page.locator("#modal .drift-why").waitFor();
              assert.equal(await page.locator("#modal .drift-why").textContent(), want);
            } else {
              assert.notEqual(await fix.getAttribute("aria-label"), how);
              // Reconnect that leaves it says what is still off
              await fix.click();
              await page.waitForFunction(() => document.querySelector("#status")?.classList.contains("err"));
              assert.equal(await page.locator("#status").textContent(), want);
            }
            await page.context().close();
          });
        }
        await t.test(`${lang} at ${width}px: Reconnect that clears it says so`, async () => {
          const page = await (await browser.newContext({ viewport: { width, height: 900 } })).newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", serve(lang, fill(T3_BASE, { file: "/Users/me/.claude/settings.json", url: "http://127.0.0.1:15721" }), "unwired", ""));
          await page.goto("http://magpie.test/");
          await page.locator(`${row} .ag-fix`).click();
          const ok = await page.evaluate(() => t("{agent} goes through magpie again", { agent: "Claude Code" }));
          await page.waitForFunction((ok) => document.querySelector("#status")?.textContent === ok, ok);
          assert.ok(!(await page.locator("#status").getAttribute("class")).includes("err"));
          assert.equal(await page.locator(`${row} .ag-fix`).count(), 0);
          await page.context().close();
        });
      }
    }
    assert.deepEqual(errors, []);
  });
}
