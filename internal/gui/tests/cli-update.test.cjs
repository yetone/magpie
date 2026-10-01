// Run with Node's test runner and Playwright on the module path; see README.md.
// The agents' CLIs on the Agents page (#202): each row's version after its
// name, an "Update to x.y.z" pill where a newer one is out and magpie knows
// how the CLI was installed, the version alone where it doesn't; the rows
// keep their height; a click updates in place (busy, then the new version,
// the pill gone) with the view left where it was; a failed update says why
// and gives the pill back; the words in Chinese. No backend: the API is
// faked here, and nothing is ever installed.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = ["model-a", "model-b"].map((m) => ({ value: m, label: m }));
const agent = (id, used = true) => ({
  id, name: id === "codex" ? "Codex" : id, path: "/test/" + id, fields: [
    { key: "model", label: "model", value: used ? "model-a" : "", options: models },
    { key: "effort", label: "effort", value: used ? "high" : "", options: ["low", "medium", "high"].map((v) => ({ value: v, label: v })) },
  ],
});
const state = {
  agents: [...Array.from({ length: 12 }, (_, i) => agent("agent-" + i)), agent("codex"), agent("gemini"), agent("unset-1", false)],
  profiles: [], settings: { lang: "en", theme: "light" },
};
const CLIS = {
  codex: { version: "0.155.1", latest: "0.159.0", via: "self", command: "codex update", update: true },
  gemini: { version: "0.60.0", latest: "0.61.0", via: "npm", command: "npm install -g --prefix /x @google/gemini-cli@latest", update: true },
  "agent-0": { version: "1.2.3" }, // installed some way magpie can't tell
  "agent-1": { version: "2.0.0", latest: "2.0.0", via: "brew", command: "brew upgrade agent-1" },
};

function server(lang, posts) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { ...state, settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/agents/cli" && req.method() === "GET") return route.fulfill({ json: { agents: CLIS, pending: false } });
    if (url.pathname.startsWith("/api/agents/cli/") && req.method() === "POST") {
      const id = url.pathname.split("/").pop();
      posts.push(id);
      await new Promise((r) => setTimeout(r, 700)); // an update takes a while
      if (id === "gemini") return route.fulfill({ status: 500, json: { error: "npm install -g @google/gemini-cli@latest: EACCES: permission denied" } });
      return route.fulfill({ json: { ...CLIS[id], version: CLIS[id].latest, update: false } });
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

const row = (id) => `.row.agent[data-id="${id}"]`;

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the agents' CLI versions and updates", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    const open = async (lang, posts) => {
      const page = await (await browser.newContext({ viewport: { width: 980, height: 520 } })).newPage();
      page.setDefaultTimeout(5000);
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang, posts));
      await page.goto("http://magpie.test/");
      await page.locator(`${row("codex")} .ag-ver`).waitFor();
      return page;
    };

    await t.test("versions, and a pill only where magpie can update", async () => {
      const page = await open("en", []);
      assert.equal(await page.locator(`${row("codex")} .ag-ver`).textContent(), "0.155.1");
      assert.equal(await page.locator(`${row("codex")} .ag-up`).textContent(), "Update to 0.159.0");
      assert.match(await page.locator(`${row("codex")} .ag-up`).getAttribute("title"), /codex update/);
      // installed some way magpie can't tell: the version, and nothing to click
      assert.equal(await page.locator(`${row("agent-0")} .ag-ver`).textContent(), "1.2.3");
      assert.equal(await page.locator(`${row("agent-0")} .ag-up`).count(), 0);
      assert.match(await page.locator(`${row("agent-0")} .ag-ver`).getAttribute("title"), /can't tell how it was installed/);
      // up to date
      assert.equal(await page.locator(`${row("agent-1")} .ag-up`).count(), 0);
      // no CLI known: nothing at all
      assert.equal(await page.locator(`${row("agent-2")} .ag-ver`).count(), 0);
      // on the name's line: a row with a pill is as tall as one without
      const h = await page.evaluate((s) => s.map((q) => document.querySelector(q).getBoundingClientRect().height), [row("codex"), row("agent-2")]);
      assert(Math.abs(h[0] - h[1]) <= 1, `rows differ in height: ${h}`);
      // the name, version and pill sit on one line
      const tops = await page.evaluate((r) => [".name", ".ag-ver", ".ag-up"].map((s) => {
        const b = document.querySelector(r + " " + s).getBoundingClientRect();
        return b.top + b.height / 2;
      }), row("codex"));
      assert(Math.max(...tops) - Math.min(...tops) <= 2, `not on one line: ${tops}`);
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-cli-update.png`) });
        await page.setViewportSize({ width: 980, height: 1000 });
        await page.waitForTimeout(300);
        await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-cli-update-all.png`) });
      }
    });

    await t.test("an update, the view left where it was", async () => {
      const posts = [];
      const page = await open("en", posts);
      const view = page.locator("#view-agents");
      // the reader scrolls down a little, so a scroll would show
      await page.mouse.move(400, 300);
      for (let i = 0; i < 6; i++) { await page.mouse.wheel(0, 40); await page.waitForTimeout(20); }
      await page.waitForTimeout(300);
      const top = await view.evaluate((v) => v.scrollTop);
      assert(top > 0, "the list must be scrolled");
      const was = await page.locator(`${row("codex")} .ag-up`).evaluate((b) => b.getBoundingClientRect().top);
      await page.locator(`${row("codex")} .ag-up`).click();
      await page.waitForTimeout(150);
      assert.equal(await page.locator(`${row("codex")} .ag-up`).textContent(), "Updating…");
      assert.equal(await page.locator(`${row("codex")} .ag-up.busy`).count(), 1);
      // clicked again while it runs: nothing more is asked
      await page.locator(`${row("codex")} .ag-up`).click({ force: true });
      await page.waitForTimeout(900);
      assert.deepEqual(posts, ["codex"]);
      assert.equal(await page.locator(`${row("codex")} .ag-ver`).textContent(), "0.159.0");
      assert.equal(await page.locator(`${row("codex")} .ag-up`).count(), 0);
      assert.match(await page.locator("#status").textContent(), /Codex updated to 0\.159\.0/);
      assert.equal(await view.evaluate((v) => v.scrollTop), top, "the update moved the page");
      const now = await page.locator(`${row("codex")} .ag-ver`).evaluate((b) => b.getBoundingClientRect().top);
      assert(Math.abs(now - was) <= 3, `the row moved ${now - was}px`);

      // one that fails says why, and can be tried again
      await page.locator(`${row("gemini")} .ag-up`).click();
      await page.waitForTimeout(1000);
      assert.match(await page.locator("#status").textContent(), /EACCES/);
      assert.equal(await page.locator(`${row("gemini")} .ag-up`).textContent(), "Update to 0.61.0");
      assert.equal(await page.locator(`${row("gemini")} .ag-up.busy`).count(), 0);
    });

    await t.test("in Chinese", async () => {
      const page = await open("zh", []);
      assert.equal(await page.locator(`${row("codex")} .ag-up`).textContent(), "更新到 0.159.0");
      assert.match(await page.locator(`${row("codex")} .ag-up`).getAttribute("title"), /通过 codex update 更新/);
    });

    await t.test("a tight row cuts the pill and the version before the name", async () => {
      const page = await open("zh", []);
      const look = () => page.evaluate((r) => {
        const q = (s) => document.querySelector(r + " " + s);
        const shown = (e) => !!e && e.getBoundingClientRect().width > 0 && getComputedStyle(e).display !== "none";
        const name = q(".name"), up = q(".ag-up"), words = q(".ag-up > span");
        return { cut: name.scrollWidth > name.clientWidth + 1, up: shown(up), words: shown(words), ver: shown(q(".ag-ver")), label: up?.getAttribute("aria-label") };
      }, row("codex"));
      // wide: everything
      assert.deepEqual(await look(), { cut: false, up: true, words: true, ver: true, label: "更新到 0.159.0" });
      // the window at its first width, or a little less: the arrow alone
      // (the name was "C." here)
      await page.setViewportSize({ width: 620, height: 520 });
      await page.waitForTimeout(100);
      const tight = await look();
      assert.equal(tight.cut, false, "the name was cut before the pill");
      assert.equal(tight.up, true);
      assert.equal(tight.words, false, "the pill kept its words in a tight row");
      assert.equal(tight.label, "更新到 0.159.0");
      // tighter still: the version goes too, the name last (just above
      // 600px, the narrowest the row gets beside its fields)
      await page.setViewportSize({ width: 604, height: 520 });
      await page.waitForTimeout(100);
      const tighter = await look();
      assert.equal(tighter.cut, false, "the name was cut before the version");
      assert.equal(tighter.ver, false);
      // at 600px or less the fields go under the name (#440), so the name's
      // line has room again for everything
      await page.setViewportSize({ width: 540, height: 520 });
      await page.waitForTimeout(100);
      assert.deepEqual(await look(), { cut: false, up: true, words: true, ver: true, label: "更新到 0.159.0" });
    });

    assert.deepEqual(errors, []);
  });
}
