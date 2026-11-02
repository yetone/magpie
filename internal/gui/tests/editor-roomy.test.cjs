// Run with Node's test runner and Playwright on the module path; see README.md.
// A provider's editor takes the room a large window offers (5046312 on
// Discord: in a large, wide window Codex's editor, its accounts with their
// meters, cap pill, switches and option rows, stayed a 600px box in the
// middle, cramped). From a 1071px window it grows with the window, 56% of
// its width, up to 880px, as a Mac sheet takes the room its content needs;
// narrower it is 600px as before, and in magpie web on a phone (390px) it
// fits the window with nothing across it cut off or scrolled sideways. A
// question that takes the editor's place in the same dialog (Sign out's)
// stays 600px. Chromium and WebKit, every language, the API faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { strings } = require("./strings.cjs");

// MAGPIE_ROOMY_ASSETS: an older asset directory, to see this fail on it
const assets = path.resolve(process.env.MAGPIE_ROOMY_ASSETS || path.join(__dirname, "../assets"));
const { I18N } = strings();
const tr = (lang, k) => (lang === "en" ? k : I18N[lang]?.[k] || k);

const accounts = Array.from({ length: 5 }, (_, i) => ({ user: `account-${i + 1}@example.invalid`, active: i === 0, on: true, plan: i % 2 ? "pro" : "plus" }));
const codex = { id: "codex", name: "Codex", icon: "codex", models: Array.from({ length: 12 }, (_, i) => ({ id: `gpt-6-${i + 1}`, on: i % 3 !== 2 })), agents: [], key: {}, routing: "smart",
  account: { agent: "codex", agentName: "Codex", agentIcon: "codex", user: accounts[0].user, logins: accounts } };
const SOLO = "solo@example.invalid";
const solo = { ...codex, account: { ...codex.account, user: SOLO, logins: [{ user: SOLO, plan: "plus", active: true, on: true }] } };
const inHours = (h) => new Date(Date.now() + h * 36e5).toISOString();
const usage = () => Object.fromEntries(accounts.map((a, i) => [a.user, { provider: "codex", windows: [{ name: "5 hours", used: 10 + 15 * i, resetsAt: inHours(3) }, { name: "Weekly", used: 5 * i, resetsAt: inHours(100) }] }]));

function serve(lang, { web = false, one = false } = {}) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:${web}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [one ? solo : codex], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [] });
    if (url.pathname === "/api/login/usage") return json(usage());
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    if (!/^\/[\w./-]*$/.test(url.pathname) || url.host !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

// the window, the width the editor's dialog takes in it, and whether it is
// magpie web (a phone's is the narrowest; the app's window is 560 at least)
const SIZES = [[2560, 1440, 880], [1440, 900, 806.4], [1100, 800, 616], [1024, 768, 600], [800, 600, 600], [560, 420, 512], [390, 844, 374, true]];

const measure = (page) => page.evaluate(() => {
  const r = (e) => { const b = e.getBoundingClientRect(); return { top: b.top, bottom: b.bottom, left: b.left, right: b.right, width: b.width, height: b.height }; };
  const d = document.querySelector("#modal .dialog"), body = d.querySelector(".ebody");
  // every element in the editor that is drawn: none reaches past the
  // dialog's sides (an element that scrolls sideways itself, the model
  // chips' strip, holds what is in it)
  const out = [];
  for (const e of d.querySelectorAll(".editor *")) {
    const b = e.getBoundingClientRect();
    if (!b.width || !b.height || e.closest("[hidden]") || getComputedStyle(e).visibility === "hidden") continue;
    let inScroller = false;
    for (let p = e.parentElement; p && p !== body; p = p.parentElement) if (/auto|scroll/.test(getComputedStyle(p).overflowX)) inScroller = true;
    if (inScroller) continue;
    if (b.left < r(d).left - 1 || b.right > r(d).right + 1) out.push(`${e.tagName.toLowerCase()}.${[...e.classList].join(".")} ${Math.round(b.left)}..${Math.round(b.right)}`);
  }
  return {
    view: { width: innerWidth, height: innerHeight }, dialog: r(d), head: r(d.querySelector(".ehead")), bar: r(d.querySelector(".editor > .bar")),
    across: body.scrollWidth - body.clientWidth, pageAcross: document.scrollingElement.scrollWidth - innerWidth,
    out: out.slice(0, 5),
  };
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const [width, height, want, web] of SIZES) {
      test(`${engine} ${lang} ${width}x${height}${web ? " web" : ""}: Codex's editor is ${want}px wide, nothing across it cut off`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, { web }));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator('.row.provider[data-id="codex"]').click();
        await page.locator("#modal .ebody").waitFor();
        // the allowances drawn in, the dialog settled
        await page.waitForFunction(() => document.querySelectorAll("#modal .acc .aq .skeleton").length === 0 && document.querySelectorAll("#modal .acc .aq").length === 5);
        await page.waitForFunction(() => !document.querySelector("#modal .dialog").getAnimations().length);
        const at = await measure(page);
        t.diagnostic(`dialog ${at.dialog.width}x${at.dialog.height} in ${width}x${height}`);
        if (process.env.ARTIFACT_DIR && lang === "en") await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `editor-roomy-${engine}-${width}x${height}.png`) });
        assert.ok(Math.abs(at.dialog.width - want) < 1, `the editor's dialog is ${want}px wide in a ${width}x${height} window: ${at.dialog.width}`);
        assert.ok(at.dialog.left >= 0 && at.dialog.right <= width && at.dialog.top >= 0 && at.dialog.bottom <= height, `the dialog fits the window: ${JSON.stringify(at.dialog)}`);
        assert.ok(at.head.top >= 0 && at.bar.bottom <= height, `its head and buttons are in sight: ${JSON.stringify([at.head, at.bar])}`);
        assert.equal(at.across, 0, "the editor's body doesn't scroll sideways");
        assert.ok(at.pageAcross <= 0, `the page doesn't scroll sideways: ${at.pageAcross}`);
        assert.deepEqual(at.out, [], "nothing in the editor reaches past its sides");
        assert.deepEqual(errors, []);
      });
    }

    test(`${engine} ${lang} 2560x1440: Sign out's question in the editor's place stays 600px, and the editor grows again after Cancel`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 2560, height: 1440 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, { one: true }));
      await page.goto("http://magpie.test/?view=providers");
      await page.locator('.row.provider[data-id="codex"]').click();
      await page.locator("#modal .acc .aq .skeleton").first().waitFor({ state: "detached" });
      await page.waitForFunction(() => !document.querySelector("#modal .dialog").getAnimations().length);
      assert.equal(await page.evaluate(() => document.querySelector("#modal .dialog").getBoundingClientRect().width), 880, "the editor is 880px");
      await page.locator("#modal .acc .acc-more").click();
      await page.locator("#modal .acc button", { hasText: tr(lang, "Sign out") }).click();
      await page.locator("#modal .forget-ask").waitFor();
      await page.waitForFunction(() => !document.querySelector("#modal .dialog").getAnimations().length);
      assert.equal(await page.evaluate(() => document.querySelector("#modal .dialog").getBoundingClientRect().width), 600, "the question is 600px wide");
      await page.locator("#modal .forget-ask button", { hasText: tr(lang, "Cancel") }).click();
      await page.locator("#modal .acc").first().waitFor();
      await page.waitForFunction(() => !document.querySelector("#modal .dialog").getAnimations().length);
      assert.equal(await page.evaluate(() => document.querySelector("#modal .dialog").getBoundingClientRect().width), 880, "the editor is back at 880px");
      assert.deepEqual(errors, []);
    });
  }
}
