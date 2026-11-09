// Run with Node's test runner and Playwright on the module path; see README.md.
// A Claude subscription's editor says whether its requests run Claude
// Code's interactive mode (TUI) or claude -p, switches between them right
// there (the owner, 11-01), and hovering it shows what each is and what
// each is good for (the owner, 10-31). Here:
// - on, its last request interactive: TUI pressed, the last request said;
// - hovering or focusing the switch shows both modes, the one chosen
//   marked, each with its advantage, inside the window, and says a turn
//   Anthropic refuses as a third-party app's runs as -p; moving away hides it;
// - turned off: claude -p pressed, every request said to run as it;
//   pressing TUI saves claudePrintMode false with the rest kept, reads the
//   mode again and draws TUI pressed; pressing claude -p turns it back;
// - on where it can't run (Claude Code in WSL whose home wasn't found):
//   TUI pressed, and Claude requests are said to fail, and why;
// - another subscription (Codex), and Only switch, which serves nothing,
//   have no such row, and /api/claude/mode isn't read for them.
// The mode is read from /api/claude/mode as the editor opens, and again
// after a switch.
// Chromium and WebKit, 900 and 420 wide, in every language, the API faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const ago = (min) => new Date(Date.now() - min * 60e3).toISOString();
const USER = "me@example.invalid";
const claude = (switchOnly) => ({ id: "claude", name: "Claude Code", icon: "claude", models: [{ id: "claude-opus-5-5", on: true }], agents: [], key: {}, switchOnly,
  account: { agent: "claude", agentName: "Claude Code", agentIcon: "claude", user: USER, logins: [{ user: USER, plan: "max", active: true, on: true }] } });
const codex = { id: "codex", name: "Codex", icon: "codex", models: [{ id: "gpt-6", on: true }], agents: [], key: {},
  account: { agent: "codex", agentName: "Codex", agentIcon: "codex", user: USER, logins: [{ user: USER, plan: "plus", active: true, on: true }] } };

const RUNS = {
  tui: { tui: true, last: { tui: true, at: ago(3) } },
  off: { tui: false, why: "off", last: { tui: false, why: "off", at: ago(3) } },
  os: { tui: false, why: "os" },
};

// each language's words, as the i18n table has them
const W = {
  en: { row: "Claude Code runs as", tui: "Interactive mode (TUI)", inUse: "In use", lastTui: "interactive mode", allP: "Every request runs as claude -p" },
  zh: { row: "Claude Code 运行方式", tui: "交互模式（TUI）", inUse: "使用中", lastTui: "交互模式", allP: "每个请求都以 claude -p 运行" },
  "zh-TW": { row: "Claude Code 執行方式", tui: "互動模式（TUI）", inUse: "使用中", lastTui: "互動模式", allP: "每個請求都以 claude -p 執行" },
  ja: { row: "Claude Code の実行方式", tui: "対話モード（TUI）", inUse: "使用中", lastTui: "対話モード", allP: "すべてのリクエストを claude -p で実行します" },
  de: { row: "Claude Code läuft als", tui: "Interaktiver Modus (TUI)", inUse: "In Verwendung", lastTui: "interaktiver Modus", allP: "Jede Anfrage läuft als claude -p" },
};

function serve(lang, run, switchOnly, reads) {
  const settings = { lang, theme: "light", redact: true, claudePrintMode: run.why === "off" };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") {
      if (route.request().method() === "POST") {
        reads.saved.push(JSON.parse(route.request().postData()));
        Object.assign(settings, reads.saved.at(-1));
      }
      return json(settings);
    }
    if (url.pathname === "/api/providers") return json({ providers: [claude(switchOnly), codex], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [] });
    // the mode as the setting has it now: turned on, the machine's own
    if (url.pathname === "/api/claude/mode") {
      reads.n++;
      if (settings.claudePrintMode) return json(RUNS.off);
      return json(run.why === "off" ? { tui: true } : run);
    }
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    if (!/^\/[\w./-]*$/.test(url.pathname) || url.host !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

async function openEditor(browser, lang, run, { id = "claude", width = 900, switchOnly = false } = {}) {
  const page = await (await browser.newContext({ viewport: { width, height: 760 } })).newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [], reads = { n: 0, saved: [] };
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, run, switchOnly, reads));
  await page.goto("http://magpie.test/?view=providers");
  // the first draw waits on the page's first reads: a load's room, not a click's (#4470)
  const row = page.locator(`.row.provider[data-id="${id}"]`);
  await row.waitFor({ timeout: 60000 });
  await row.click();
  // the click's pointer stays where the editor draws its switch, and
  // WebKit opens the hover card for a pointer that never moved (#4638):
  // the pointer leaves the page before the editor is read
  await page.mouse.move(0, 0);
  await page.locator("#modal .ebody").waitFor();
  return { page, errors, reads };
}

// the row's label and value, in a slot that lays out as the editor's rows
const runRow = (page) => page.locator("#modal .claude-mode", { has: page.locator(".cc-run") });

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [900, 420]) for (const lang of Object.keys(W)) {
    const w = W[lang];
    test(`${engine} ${width} ${lang}: the interactive mode in use, its last request said, both modes on hover`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const { page, errors, reads } = await openEditor(browser, lang, RUNS.tui, { width });
      const row = runRow(page);
      await row.waitFor();
      const tr = (k, v) => page.evaluate(([k, v]) => t(k, v), [k, v]);
      assert.equal((await row.locator(":scope > label").textContent()).trim(), w.row);
      assert(await row.locator(":scope > label").evaluate((l) => l.compareDocumentPosition(document.querySelector("#modal .accts")) & Node.DOCUMENT_POSITION_FOLLOWING), "the row is above the accounts");
      const pick = row.locator(".cc-pick");
      assert.deepEqual(await pick.locator(".opt").evaluateAll((bs) => bs.map((b) => [b.textContent.trim(), b.getAttribute("aria-pressed"), b.disabled])),
        [[w.tui, "true", false], ["claude -p", "false", false]], "TUI pressed, both can be pressed");
      assert(await pick.evaluate((e) => { const r = e.getBoundingClientRect(); return r.width > 0 && r.right <= innerWidth + 1; }), `the switch fits at ${width}`);
      assert.match(await row.locator(".cc-said").textContent(), new RegExp(w.lastTui.replace(/[()]/g, "\\$&") + "$"));
      assert.equal(await page.locator(".cc-tip").count(), 0);
      await pick.hover();
      const tip = page.locator(".cc-tip");
      await tip.waitFor();
      const modes = await tip.locator(".cc-m").evaluateAll((ms) => ms.map((m) => ({ on: m.classList.contains("on"), name: m.querySelector(".h b").textContent, now: m.querySelector(".now")?.textContent || "", good: m.querySelectorAll("p.good").length, bad: m.querySelectorAll("p.bad").length })));
      assert.deepEqual(modes, [
        { on: true, name: w.tui, now: w.inUse, good: 1, bad: 0 },
        { on: false, name: "claude -p", now: "", good: 1, bad: 1 },
      ]);
      // nothing in the card says a request falls back to -p (the owner, 11-01)
      assert.equal((await tip.locator(".foot").textContent()).trim(), await tr("Switched here. With the interactive mode on, a request it can't take, or one it fails to start for, fails saying why. A turn Anthropic refuses there as a third-party app's runs as -p."));
      assert.equal((await tip.locator(".cc-m").nth(1).locator("p.good").textContent()).trim(), await tr("It starts at once, with no terminal of its own to open first."));
      const box = await tip.boundingBox();
      assert(box.x >= 0 && box.y >= 0 && box.x + box.width <= width && box.y + box.height <= 760, `the card is inside the window: ${JSON.stringify(box)}`);
      if (lang !== "en") {
        // every line of the card is in the reader's language
        const text = await tip.textContent();
        assert(!/magpie opens Claude Code as a person does|Your plan counts|It starts at once|Switched here/.test(text), `English left in the card: ${text}`);
      }
      await page.mouse.move(5, 5);
      await tip.waitFor({ state: "detached" });
      // the keyboard reaches it too
      await pick.locator(".opt").first().focus();
      await tip.waitFor();
      assert.equal(reads.n, 1, "the mode is read once, not on each draw");
      assert.deepEqual(errors, []);
    });

    if (width !== 900) continue;
    test(`${engine} ${lang}: turned off, it says every request is -p, and switches to TUI and back right there`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const { page, errors, reads } = await openEditor(browser, lang, RUNS.off);
      const row = runRow(page);
      await row.waitFor();
      const opt = (i) => row.locator(".cc-pick .opt").nth(i);
      const pressed = () => row.locator(".cc-pick .opt").evaluateAll((bs) => bs.map((b) => b.getAttribute("aria-pressed")));
      assert.deepEqual(await pressed(), ["false", "true"], "claude -p pressed");
      assert.equal((await row.locator(".cc-said").textContent()).trim(), w.allP);
      await opt(1).hover();
      await page.locator(".cc-tip").waitFor();
      assert.deepEqual(await page.locator(".cc-tip .cc-m").evaluateAll((ms) => ms.map((m) => m.classList.contains("on"))), [false, true], "-p is the one in use");
      await opt(0).click();
      for (let i = 0; i < 100 && reads.n < 2; i++) await page.waitForTimeout(50);
      await page.waitForFunction(() => document.querySelector("#modal .cc-pick .opt")?.getAttribute("aria-pressed") === "true");
      assert.equal(reads.saved.length, 1, "saved once");
      assert.equal(reads.saved[0].claudePrintMode, false);
      assert.equal(reads.saved[0].redact, true, "the rest of the settings kept");
      assert(!("claudeTUIOnly" in reads.saved[0]), "the retired setting isn't sent");
      assert.equal(reads.n, 2, "the mode is read again after the switch");
      assert.equal(await row.locator(".cc-said").count(), 0, "nothing to say before a request has run");
      assert.equal(await page.locator("#modal").count(), 1, "the editor stays open");
      await opt(1).click();
      for (let i = 0; i < 100 && reads.n < 3; i++) await page.waitForTimeout(50);
      assert.equal(reads.saved[1]?.claudePrintMode, true, "and back to -p");
      await page.waitForFunction(() => document.querySelectorAll("#modal .cc-pick .opt")[1]?.getAttribute("aria-pressed") === "true");
      assert.equal((await row.locator(".cc-said").textContent()).trim(), w.allP);
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: on where it can't run, Claude requests are said to fail, and why`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const { page, errors } = await openEditor(browser, lang, RUNS.os);
      const row = runRow(page);
      await row.waitFor();
      const tr = (k, v) => page.evaluate(([k, v]) => t(k, v), [k, v]);
      assert.equal(await row.locator(".cc-pick .opt").first().getAttribute("aria-pressed"), "true", "TUI is still the one chosen: never -p");
      const said = (await row.locator(".cc-said.bad").textContent()).trim();
      assert.equal(said, await tr("Claude requests fail: {why}", { why: await tr("this system can't run the interactive mode") }));
      if (lang !== "en") assert(!/Claude requests fail|can't run/.test(said), said);
      assert.deepEqual(errors, []);
    });
  }

  test(`${engine}: another subscription has no such row`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const { page, errors, reads } = await openEditor(browser, "en", RUNS.tui, { id: "codex" });
    await page.locator("#modal .acc").first().waitFor();
    assert.equal(await page.locator("#modal .cc-run, #modal .claude-mode").count(), 0);
    assert.equal(reads.n, 0, "nothing is read for a row not shown");
    assert.deepEqual(errors, []);
  });

  test(`${engine}: Only switch, which serves nothing, has no such row`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const { page, errors, reads } = await openEditor(browser, "en", RUNS.tui, { switchOnly: true });
    await page.locator("#modal .acc").first().waitFor();
    assert.equal(await page.locator("#modal .cc-run, #modal .claude-mode").count(), 0);
    assert.equal(reads.n, 0, "nothing is read for a row not shown");
    assert.deepEqual(errors, []);
  });
}
