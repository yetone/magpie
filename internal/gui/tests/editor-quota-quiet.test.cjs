// Run with Node's test runner and Playwright on the module path; see README.md.
// A provider's accounts get their allowances from a read of their own, and
// the open editor is drawn again when it comes in (loginUsageOf). That
// redraw is nothing the reader did, so the dialog doesn't play the soft
// refresh a change of its content gets (.dialog.swap, content-in from
// opacity .5 and 2px down): congee949 saw the whole dialog, its head and
// buttons too, dim and drop and come back as the read came in, an empty
// answer too (#3492). Here every frame from the answer until well after
// the redraw keeps the dialog at opacity 1 and its top where it was, and
// the body keeps its scroll and the focus, for an empty answer and for
// one that fills the meters in. A change the reader makes still plays
// it: Codex's only account's Sign out turns the dialog to its question,
// and Cancel turns it back to the editor. The reporter's fixture (five
// accounts, 16 models, 1960 wide, dark; 760 high, as since an account's
// settings went under its ⋯ that editor fits 1015 whole and its body has
// nothing to scroll); Chromium and WebKit, English and Chinese, the API
// faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const accounts = Array.from({ length: 5 }, (_, i) => ({ user: `account-${i + 1}@example.invalid`, active: i === 0, on: true, plan: "personal_standard" }));
const fakeco = { id: "fakeco", name: "FakeCo", icon: "generic", models: Array.from({ length: 16 }, (_, i) => ({ id: `model-${i + 1}`, on: i % 2 === 0 })), agents: [], key: {}, routing: "smart",
  account: { agent: "fakeco", agentName: "FakeCo", agentIcon: "generic", user: accounts[0].user, logins: accounts } };
const SOLO = "solo@example.invalid";
const codex = { id: "codex", name: "Codex", icon: "codex", models: [{ id: "gpt-6", on: true }], agents: [], key: {},
  account: { agent: "codex", agentName: "Codex", agentIcon: "codex", user: SOLO, logins: [{ user: SOLO, plan: "plus", active: true, on: true }] } };
const plugins = [{ id: "fakeco", pid: "fakeco", name: "FakeCo", icon: "generic", spec: "fakeco-fixture", signedIn: true, models: 16, methods: [{ type: "oauth", label: "Browser sign-in" }] }];

const inHours = (h) => new Date(Date.now() + h * 36e5).toISOString();
// the reporter's empty answer, and one with each account's two windows
const answers = {
  empty: () => ({}),
  meters: () => Object.fromEntries(accounts.map((a, i) => [a.user, { provider: "fakeco", windows: [{ name: "5 hours", used: 10 + 15 * i, resetsAt: inHours(3) }, { name: "Weekly", used: 5 * i, resetsAt: inHours(100) }] }])),
};

function serve(lang, usage) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"dark",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "dark" } });
    if (url.pathname === "/api/providers") return json({ providers: [fakeco, codex], presets: [], excluded: [], gateway: { running: true, window: true }, plugins });
    if (url.pathname === "/api/login/usage") return json(await usage(url.searchParams.get("agent")));
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    if (!/^\/[\w./-]*$/.test(url.pathname) || url.host !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

async function openEditor(browser, lang, id, usage) {
  const page = await (await browser.newContext({ viewport: { width: 1960, height: 760 }, reducedMotion: "no-preference" })).newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, usage));
  await page.goto("http://magpie.test/?view=providers");
  await page.locator(`.row.provider[data-id="${id}"]`).click();
  await page.locator("#modal .ebody").waitFor();
  // the open's grow and fade over: what moves after is the redraw's
  await page.waitForFunction(() => { const m = document.querySelector("#modal"); return !m.getAnimations().length && !m.firstElementChild.getAnimations().length; });
  return { page, errors };
}

// From here every frame's opacity and top of the dialog and its body's
// scroll, until `until` holds and 400ms more, past the swap's 220ms; and
// the opacity read as its class changes, before any frame, so a frame
// late under load still sees a swap begin.
const watch = (page, until) => page.evaluate((until) => {
  // drawn again: the body, or (#5310) only its accounts list
  const d = document.querySelector("#modal .dialog"), was = d.querySelector(".ebody"), list = () => d.querySelector(".accts:not(.keys)"), wasList = list();
  const seen = window.seen = { frames: [], atClass: [], at: 0, done: false };
  new MutationObserver(() => seen.atClass.push({ opacity: +getComputedStyle(d).opacity, anims: d.getAnimations().map((a) => a.animationName || "") }))
    .observe(d, { attributes: true, attributeFilter: ["class"] });
  const step = (now) => {
    const body = d.querySelector(".ebody");
    seen.frames.push({ opacity: +getComputedStyle(d).opacity, top: d.getBoundingClientRect().top, scroll: body?.scrollTop });
    if (!seen.at && (until === "redrawn" ? body && (body !== was || list() !== wasList) : d.querySelector(until))) seen.at = now;
    if (seen.at && now - seen.at > 400) { seen.done = true; return; }
    requestAnimationFrame(step);
  };
  requestAnimationFrame(step);
}, until);
const watched = async (page, what) => {
  await page.waitForFunction(() => window.seen.done).catch(() => assert.fail(`${what} never came, so this tested nothing`));
  return page.evaluate(() => window.seen);
};

// the focused element as a redraw that keeps it finds it again: its row,
// its kind and its words (its place among the dialog's controls moves, as
// the allowances bring each account's own controls in above it)
const focused = (page) => page.evaluate(() => {
  const d = document.querySelector("#modal .dialog"), a = document.activeElement;
  return { row: a.closest(".acc")?.dataset.accountId, kind: d.contains(a) ? a.className : "", text: a.textContent };
});

const W = {
  en: { signOut: "Sign out", cancel: "Cancel", ask: `Sign Codex out of ${SOLO}?` },
  zh: { signOut: "退出登录", cancel: "取消", ask: `让 Codex 退出 ${SOLO}？` },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    for (const answer of ["empty", "meters"]) {
      test(`${engine} ${lang}: the allowances coming in (${answer}) don't dim or move the open editor`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        let release;
        const held = new Promise((r) => { release = r; });
        const { page, errors } = await openEditor(browser, lang, "fakeco", async () => { await held; return answers[answer](); });
        // read down the body a way, a control in it focused, as the reporter scrolled
        const before = await page.evaluate(() => {
          const d = document.querySelector("#modal .dialog"), body = d.querySelector(".ebody");
          body.scrollTop = 120;
          [...d.querySelectorAll(".acc .acc-more")].at(-2).focus({ preventScroll: true });
          return { scroll: body.scrollTop, room: body.scrollHeight - body.clientHeight, waiting: d.querySelectorAll(".aq .skeleton").length };
        });
        assert(before.scroll > 0, `the body scrolls, so its scroll is something to keep: ${JSON.stringify(before)}`);
        assert(before.waiting > 0, `the allowances are still on their way: ${JSON.stringify(before)}`);
        const focus = await focused(page);
        assert(focus.kind === "acc-more" && focus.row, `a control in an account's row has the focus: ${JSON.stringify(focus)}`);
        await watch(page, "redrawn");
        release();
        const seen = await watched(page, "the editor's redraw for the allowances");
        assert.equal(await page.locator("#modal .aq .skeleton").count(), 0, "the allowances are drawn in");
        const low = Math.min(...seen.frames.map((f) => f.opacity)), tops = seen.frames.map((f) => f.top);
        t.diagnostic(`${seen.frames.length} frames, opacity ${low} at the lowest, top ${Math.min(...tops)} to ${Math.max(...tops)}px, scroll ${before.scroll}`);
        assert(low === 1 && seen.atClass.every((c) => c.opacity === 1) && Math.max(...tops) - Math.min(...tops) < 0.5,
          `the dialog dimmed or moved as the allowances came in: its opacity ${seen.atClass.map((c) => [c.opacity, ...c.anims].join(" ")).join(", ")} as its class changed, `
          + `${low} at its lowest over ${seen.frames.length} frames, its top from ${Math.min(...tops)} to ${Math.max(...tops)}px`);
        const scrolls = [...new Set(seen.frames.map((f) => f.scroll))];
        assert.deepEqual(scrolls, [before.scroll], "the body keeps its scroll");
        assert.deepEqual(await focused(page), focus, "the focus stays on its control");
        assert.deepEqual(errors, []);
      });
    }

    test(`${engine} ${lang}: a change the reader makes still plays the dialog's swap`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const w = W[lang];
      const { page, errors } = await openEditor(browser, lang, "codex", answers.empty);
      // its allowance read in and drawn first, so the swaps below are the clicks'
      await page.locator("#modal .acc .aq .aq-none").waitFor();
      await page.waitForFunction(() => !document.querySelector("#modal .dialog").getAnimations().length);
      // Sign out waits under the account's ⋯, which opens in place; it asks
      // first, in the same dialog
      await page.locator("#modal .acc .acc-more").click();
      await watch(page, ".forget-ask");
      await page.locator("#modal .acc button", { hasText: w.signOut }).click();
      let seen = await watched(page, "Sign out's question");
      assert.equal(await page.locator("#modal .forget-ask .ehead b").textContent(), w.ask);
      // read as the class changes: a frame comes when it will, and under
      // load one may come after the swap's 220ms
      const swapped = (s) => s.atClass.some((c) => c.opacity < 0.9 && c.anims.includes("content-in"));
      assert(swapped(seen), `the dialog turned to the question without its swap: ${JSON.stringify(seen.atClass)}`);
      // Cancel goes back to the editor, as a change of content too
      await page.waitForFunction(() => !document.querySelector("#modal .dialog").getAnimations().length);
      await watch(page, ".acc");
      await page.locator("#modal .forget-ask button", { hasText: w.cancel }).click();
      seen = await watched(page, "the editor after Cancel");
      assert(swapped(seen), `the dialog turned back to the editor without its swap: ${JSON.stringify(seen.atClass)}`);
      assert.deepEqual(errors, []);
    });
  }
}
