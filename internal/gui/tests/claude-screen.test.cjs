// Run with Node's test runner and Playwright on the module path; see README.md.
// A Claude subscription's editor shows, under Claude Code runs as, each
// hidden Claude Code's screen while it runs (#8075, FremontDango on
// Discord: a run that seemed stuck said nothing of where until it ended).
// Here:
// - in the interactive mode, Show screen is beside the switch, and
//   /api/claude/screens isn't read before it is pressed;
// - pressed, each run's screen is drawn as the gateway sent it, row by
//   row, under its model, state and pid, scrolled inside its own box and
//   not past the window; a run with none says why in the reader's
//   language; it is read again while open and the screen follows it;
// - Hide screen closes it and the reads stop, and so does closing the
//   editor;
// - none running says so; with every request as claude -p there is no
//   Show screen.
// Chromium and WebKit, 900 and 420 wide, in every language, the API faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const USER = "me@example.invalid";
const claude = { id: "claude", name: "Claude Code", icon: "claude", models: [{ id: "claude-opus-5-5", on: true }], agents: [], key: {},
  account: { agent: "claude", agentName: "Claude Code", agentIcon: "claude", user: USER, logins: [{ user: USER, plan: "max", active: true, on: true }] } };

// a screen as tuidrive's Screen gives it, wider than a phone's window
const drawn = (n) => [
  "╭─── Claude Code ─────────────────────────────────────────────────────────────────────────────────────╮",
  "│ > read the file and fix the test" + " ".repeat(68) + "│",
  "╰─────────────────────────────────────────────────────────────────────────────────────────────────────╯",
  `  ⎿  Reading internal/x.go… (${n}s)`,
];
const RUNS = (n) => ({ object: "claude-runs", live: 2, most: 0, runs: [
  { pid: 4101, model: "claude-opus-5-5", state: "answering", since: new Date().toISOString(), started: new Date().toISOString(), screen: drawn(n) },
  { pid: 4102, model: "claude-sonnet-5", state: "idle", since: new Date().toISOString(), started: new Date().toISOString(), screenWhy: "Claude Code hasn't drawn anything yet" },
] });

// each language's words, as the i18n table has them
const W = {
  en: { show: "Show screen", hide: "Hide screen", answering: "Answering", idle: "Kept for its next turn", none: "Hasn't drawn anything yet", nobody: "No Claude Code is running now." },
  zh: { show: "显示屏幕", hide: "隐藏屏幕", answering: "正在回答", idle: "留待下一轮", none: "还没有画出任何内容", nobody: "现在没有正在运行的 Claude Code。" },
  "zh-TW": { show: "顯示螢幕", hide: "隱藏螢幕", answering: "正在回答", idle: "留待下一輪", none: "還沒有畫出任何內容", nobody: "現在沒有正在執行的 Claude Code。" },
  ja: { show: "画面を表示", hide: "画面を隠す", answering: "回答中", idle: "次のターンまで待機", none: "まだ何も描かれていません", nobody: "実行中の Claude Code はありません。" },
  de: { show: "Bildschirm zeigen", hide: "Bildschirm ausblenden", answering: "Antwortet", idle: "Für die nächste Runde behalten", none: "Hat noch nichts gezeichnet", nobody: "Gerade läuft kein Claude Code." },
};

function serve(lang, { printMode = false, empty = false } = {}, reads) {
  const settings = { lang, theme: "light", redact: true, claudePrintMode: printMode };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json({ providers: [claude], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [] });
    if (url.pathname === "/api/claude/mode") return json(printMode ? { tui: false, why: "off" } : { tui: true });
    if (url.pathname === "/api/claude/screens") {
      reads.n++;
      return json(empty ? { object: "claude-runs", runs: [], live: 0, most: 0 } : RUNS(reads.n));
    }
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    if (!/^\/[\w./-]*$/.test(url.pathname) || url.host !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

async function openEditor(browser, lang, opts = {}, width = 900) {
  const page = await (await browser.newContext({ viewport: { width, height: 760 } })).newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [], reads = { n: 0 };
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, opts, reads));
  await page.goto("http://magpie.test/?view=providers");
  // the first draw waits on the page's first reads: a load's room, not a click's (#4470)
  const row = page.locator('.row.provider[data-id="claude"]');
  await row.waitFor({ timeout: 60000 });
  await row.click();
  await page.mouse.move(0, 0);
  await page.locator("#modal .ebody").waitFor();
  await page.locator("#modal .cc-run .cc-pick").waitFor();
  return { page, errors, reads };
}

// reads reaching at least n, polled
async function readsReach(page, reads, n) {
  for (let i = 0; i < 200 && reads.n < n; i++) await page.waitForTimeout(25);
  assert(reads.n >= n, `read ${reads.n} times, want ${n}`);
}

const launch = (engine) => (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [900, 420]) for (const lang of Object.keys(W)) {
    const w = W[lang];
    test(`${engine} ${width} ${lang}: Show screen draws each run's screen, follows it, and Hide stops reading`, async (t) => {
      const browser = await launch(engine);
      t.after(() => browser.close());
      const { page, errors, reads } = await openEditor(browser, lang, {}, width);
      const btn = page.locator("#modal .cc-run .cc-screen-btn");
      assert.equal((await btn.textContent()).trim(), w.show);
      assert.equal(await btn.getAttribute("aria-expanded"), "false");
      assert.equal(reads.n, 0, "nothing is read before Show screen is pressed");
      await btn.click();
      const box = page.locator("#modal #ccScreens");
      await box.locator(".cc-screen").nth(1).waitFor();
      assert.equal((await btn.textContent()).trim(), w.hide);
      assert.equal(await btn.getAttribute("aria-expanded"), "true");
      const runs = await box.locator(".cc-screen").evaluateAll((bs) => bs.map((b) => ({ h: b.querySelector(".h").textContent, pre: b.querySelector("pre").textContent, none: b.querySelector("pre").classList.contains("none") })));
      assert.equal(runs[0].h, `claude-opus-5-5 · ${w.answering} · pid 4101`);
      assert.equal(runs[0].pre, drawn(1).join("\n"), "the screen as it was sent, row by row");
      assert.equal(runs[0].none, false);
      assert.deepEqual(runs[1], { h: `claude-sonnet-5 · ${w.idle} · pid 4102`, pre: w.none, none: true });
      // the box scrolls the screen; the editor doesn't widen for it
      const fit = await box.locator(".cc-screen pre").first().evaluate((p) => {
        const r = p.getBoundingClientRect(), s = getComputedStyle(p);
        return { right: r.right, inner: innerWidth, wide: p.scrollWidth > p.clientWidth, ws: s.whiteSpace, mono: /mono|menlo|courier|consol/i.test(s.fontFamily) };
      });
      assert(fit.right <= fit.inner + 1, `the screen fits the window at ${width}: ${JSON.stringify(fit)}`);
      assert.equal(fit.ws, "pre");
      assert(fit.mono, "drawn in a monospace font");
      if (width === 420) assert(fit.wide, "a screen wider than the window scrolls inside its box");
      // read again while open, and the screen follows
      await readsReach(page, reads, 3);
      await page.waitForFunction(() => /\((?:[3-9]|\d\d)s\)/.test(document.querySelector("#ccScreens pre")?.textContent || ""));
      await btn.click();
      assert.equal(await box.count(), 0, "Hide screen closes it");
      assert.equal((await btn.textContent()).trim(), w.show);
      const was = reads.n;
      await page.waitForTimeout(1600);
      assert(reads.n <= was + 1, `reads went on after Hide: ${was} then ${reads.n}`);
      assert.deepEqual(errors, []);
    });
  }

  test(`${engine}: closing the editor stops the reads`, async (t) => {
    const browser = await launch(engine);
    t.after(() => browser.close());
    const { page, errors, reads } = await openEditor(browser, "en");
    await page.locator("#modal .cc-screen-btn").click();
    await readsReach(page, reads, 2);
    await page.keyboard.press("Escape");
    await page.locator("#modal").waitFor({ state: "hidden" });
    assert.equal(await page.locator("#ccScreens").count(), 0, "the screens went with the editor");
    const was = reads.n;
    await page.waitForTimeout(1600);
    assert(reads.n <= was + 1, `reads went on after the editor closed: ${was} then ${reads.n}`);
    assert.deepEqual(errors, []);
  });

  for (const lang of Object.keys(W)) test(`${engine} ${lang}: none running says so`, async (t) => {
    const browser = await launch(engine);
    t.after(() => browser.close());
    const { page, errors } = await openEditor(browser, lang, { empty: true });
    await page.locator("#modal .cc-screen-btn").click();
    const none = page.locator("#modal #ccScreens .cc-screen-none");
    await none.waitFor();
    assert.equal((await none.textContent()).trim(), W[lang].nobody);
    assert.deepEqual(errors, []);
  });

  test(`${engine}: every request as claude -p has no Show screen`, async (t) => {
    const browser = await launch(engine);
    t.after(() => browser.close());
    const { page, errors, reads } = await openEditor(browser, "en", { printMode: true });
    assert.equal(await page.locator("#modal .cc-screen-btn").count(), 0);
    assert.equal(reads.n, 0);
    assert.deepEqual(errors, []);
  });
}
