// Run with Node's test runner and Playwright on the module path; see README.md.
// niuniu_8964 on X: several Claude accounts and Hide accounts on, a model
// chip's right-click Test on account… listed them blurred, and the way to
// show them (each account's own eye in the editor, on hover, or the
// switch) was nowhere near. The menu's head now has an eye at its right
// end, in sight while accounts are hidden: pressed, it shows the menu's
// accounts as each account's own eye does (that provider's, till the
// window closes; the switch stays on), pressed again hides them. With
// Hide accounts off there is no eye. Narrow, as the panel is, in every
// language, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ASSETS || path.resolve(__dirname, "../assets"));
const A = "a@mmuu.mom", B = "b@tirol.url", C = "c@corp.example";
const login = (user, on = true) => ({ user, plan: "Max", on, active: user === A });
const claude = { icon: "claude", chat: "", responses: "", anthropic: "https://api.anthropic.com", catalog: "", agents: [], fallback: [], headers: {},
  key: { set: false, masked: "" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "",
  id: "claude", name: "Claude Code",
  account: { agent: "claude", agentName: "Claude Code", user: A, plan: "Max", logins: [login(A), login(B), login(C, false)] },
  models: [{ id: "claude-opus-5", name: "claude-opus-5", on: true }],
  testPicks: [{ id: A, name: A }, { id: B, name: B }, { id: C, name: C, note: "Off" }] };

function serve(lang, tests) {
  const providers = { providers: [claude], presets: [], excluded: [], gateway: { running: true, window: true } };
  const settings = { lang, theme: "light" };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/provider/test") {
      const body = route.request().postDataJSON();
      tests.push(body);
      return json({ results: body.test.map((model) => ({ ok: true, ms: 123, model, protocol: "anthropic", account: body.account || A })) });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const WIDTH = 420;
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: Test on account…'s menu shows its hidden accounts from its head`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
      t.after(() => browser.close());
      const errors = [], tests = [];
      const open = async (masked) => {
        const context = await browser.newContext({ viewport: { width: WIDTH, height: 900 }, reducedMotion: "reduce" });
        if (masked) await context.addInitScript(() => { try { localStorage.setItem("magpie.maskEmails", "1"); } catch {} });
        const page = await context.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, tests));
        await page.goto("http://magpie.test/?view=providers");
        return page;
      };
      const pickMenu = async (page) => {
        const tr = (k) => page.evaluate((k) => t(k), k);
        const chip = page.locator(".editor .mchips .mchip", { hasText: "claude-opus-5" });
        if (!(await chip.count())) await page.locator(".row.provider", { hasText: "Claude Code" }).click();
        await chip.waitFor();
        await chip.scrollIntoViewIfNeeded();
        await page.waitForTimeout(200);
        await chip.click({ button: "right" });
        const name = (await tr("Test on account…")).replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
        await page.locator(".pop.row-menu").getByRole("menuitem", { name: new RegExp("^" + name) }).click();
        const pick = page.locator(".pop.proto-menu.model-account-menu");
        await pick.waitFor();
        return pick;
      };
      const settle = (page) => page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));

      const page = await open(true);
      const tr = (k) => page.evaluate((k) => t(k), k);
      if (lang !== "en") {
        const missing = await page.evaluate(([lang, keys]) => keys.filter((k) => !I18N[lang]?.[k]), [lang, ["Show these accounts", "Hide these accounts"]]);
        assert.deepEqual(missing, [], `every string has its ${lang}`);
      }
      let pick = await pickMenu(page);
      const names = () => pick.locator(".pm-name").allTextContents();
      await page.waitForFunction(() => document.querySelectorAll(".model-account-menu .pm-name .pii").length === 3);
      for (const n of await names()) assert(![A, B, C].includes(n), `hidden at first: ${n}`);

      // the eye: in the head, at its right end, in sight without a hover
      const head = pick.locator(".pm-head");
      const eye = head.locator(".acct-eye");
      assert.equal(await eye.count(), 1, "the menu's head has an eye");
      assert.ok(await eye.isVisible());
      assert.equal(await eye.evaluate((e) => getComputedStyle(e).opacity), "1", "in sight before a hover");
      assert.equal(await eye.getAttribute("aria-pressed"), "false");
      assert.equal(await eye.getAttribute("title"), await tr("Show these accounts"));
      assert.equal(await head.evaluate((h) => h.firstChild.textContent), await tr("Account to test"));
      const hb = await head.boundingBox(), eb = await eye.boundingBox(), mb = await pick.boundingBox();
      assert(mb.x >= 0 && mb.x + mb.width <= WIDTH, `the menu fits: ${JSON.stringify(mb)}`);
      assert(eb.x + eb.width <= hb.x + hb.width + 1 && eb.x + eb.width >= hb.x + hb.width - 12, `the eye ends the head: ${JSON.stringify({ hb, eb })}`);
      assert(eb.y >= hb.y - 3 && eb.y + eb.height <= hb.y + hb.height + 3, `the eye sits in the head's line: ${JSON.stringify({ hb, eb })}`);
      if (process.env.ARTIFACT_DIR && engine === "chromium" && lang === "zh") await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, "test-account-eye-hidden.png") });

      // pressed: the menu stays open and its accounts show; the switch stays on
      await eye.click();
      await settle(page);
      assert.ok(await pick.isVisible(), "the menu stays open");
      assert.deepEqual(await names(), [A, B, C], "the accounts show");
      assert.equal(await eye.getAttribute("aria-pressed"), "true");
      assert.equal(await eye.getAttribute("title"), await tr("Hide these accounts"));
      assert.equal(await page.evaluate(() => localStorage.getItem("magpie.maskEmails")), "1", "Hide accounts is still on");
      assert.ok(await page.evaluate(() => document.body.classList.contains("masked")));
      if (process.env.ARTIFACT_DIR && engine === "chromium" && lang === "zh") await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, "test-account-eye-shown.png") });

      // and a pick still tests that one
      await pick.locator(".pm-item", { hasText: B }).click();
      for (let i = 0; i < 60 && !tests.length; i++) await page.waitForTimeout(50);
      assert.deepEqual(tests.map((b) => ({ id: b.id, test: b.test, account: b.account })), [{ id: "claude", test: ["claude-opus-5"], account: B }]);
      await page.locator(".editor .mchips .mchip .tdot.ok").waitFor();
      await settle(page);

      // opened again: still shown, as an account's own eye keeps it; pressed
      // again, hidden again
      pick = await pickMenu(page);
      await settle(page);
      assert.deepEqual(await names(), [A, B, C], "still shown in the window");
      assert.equal(await pick.locator(".pm-head .acct-eye").getAttribute("aria-pressed"), "true");
      await pick.locator(".pm-head .acct-eye").click();
      await page.waitForFunction(() => document.querySelectorAll(".model-account-menu .pm-name .pii").length === 3);
      for (const n of await names()) assert(![A, B, C].includes(n), `hidden again: ${n}`);
      assert.equal(await pick.locator(".pm-head .acct-eye").getAttribute("aria-pressed"), "false");
      assert.equal(await page.locator("select").count(), 0, "no native select");

      // Hide accounts off: nothing hidden, no eye
      const plain = await open(false);
      pick = await pickMenu(plain);
      assert.deepEqual(await pick.locator(".pm-name").allTextContents(), [A, B, C]);
      assert.equal(await pick.locator(".pm-head .acct-eye").isVisible(), false, "no eye while nothing is hidden");
      assert.deepEqual(errors, []);
    });
  }
}
