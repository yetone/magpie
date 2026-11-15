// Run with Node's test runner and Playwright on the module path; see README.md.
// One account shown while Hide accounts hides the rest (yogurt36 on
// Discord, for a screen share): with Hide accounts on, an account's name
// on the provider editor's row and on its Usage card has a quiet eye, in
// sight on hover or keyboard focus. A click shows that account, by its
// address wherever the window draws it, tooltips too; the others stay
// hidden. The name the user gave one is in sight throughout (#3656). The tray panel's Usage cards have
// the same eye. Each page's shown accounts are its own: one shown in the
// window stays hidden in the panel and the other way round. Closing the
// window (hideMain's closed) or putting the panel away hides them again. With Hide accounts off there is no eye. A click moves nothing;
// no native select, no border stripe; it fits at 360px. In every language.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const SEAT = "某某·GLM高级版", TEAM = "3141592636@qq.com", ME = "me@example.com";
const inHours = (h) => new Date(Date.now() + h * 3600e3 + 60e3).toISOString();
const provider = () => ({
  id: "zcode", name: "ZCode", icon: "zcode", chat: "", responses: "", anthropic: "", catalog: "",
  models: [{ id: "glm-5", name: "", on: true }],
  agents: [], fallback: [], headers: {}, keyList: [], proxy: "",
  account: {
    agent: "zcode", agentName: "ZCode", user: TEAM, plan: "Pro",
    logins: [
      { user: TEAM, plan: "Pro", active: true, on: true },
      { user: ME, plan: "Lite", on: true },
    ],
  },
  accountNames: { [ME]: "Personal" },
  accountSeats: { [TEAM]: SEAT },
});
const quotas = () => [
  { provider: "zcode", name: "ZCode", icon: "zcode", plan: "Pro", user: TEAM, alias: SEAT, seat: SEAT,
    windows: [{ name: "5 hours", used: 20, resetsAt: inHours(3) }] },
  { provider: "zcode", name: "ZCode", icon: "zcode", plan: "Lite", user: ME, alias: "Personal", named: true,
    windows: [{ name: "5 hours", used: 60, resetsAt: inHours(2) }] },
];

function serve(lang, panel = false) {
  const settings = { theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd" };
  const providers = { providers: [provider()], presets: [], excluded: [], gateway: { running: true, window: true } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${!panel}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/usage/quotas") return json(quotas());
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/login/usage") return json({});
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const KEYS = ["Show this account", "Hide this account"];
const LANGS = ["en", "zh", "zh-TW", "ja", "de"];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of LANGS) {
    const launch = async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      return browser;
    };
    const tr = (page, k) => page.evaluate((k) => t(k), k);
    const scrolled = (page) => page.evaluate(() => [window.scrollY, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop > 0).map((e) => `${e.className}:${e.scrollTop}`)].join(" "));
    const wide = (page) => page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
    const stripes = (page, sel) => page.evaluate((sel) => [...document.querySelectorAll(sel)].map((e) => getComputedStyle(e).borderLeftWidth).filter((b) => parseFloat(b) > 1), sel);
    // a re-render can replace the card between the locator's resolve and
    // the read, and a detached element's computed display is '' (#5052):
    // read again, which resolves the locator afresh, until it is attached
    const opacity = async (loc) => {
      for (let i = 0; ; i++) {
        const v = await loc.evaluate((e) => !e.isConnected ? null : getComputedStyle(e).display === "none" ? "none" : getComputedStyle(e).opacity);
        if (v !== null || i === 20) return v ?? "detached";
      }
    };
    // what of an element is in sight: its text and every tooltip in it
    const seen = (loc) => loc.evaluate((e) => [e.textContent, ...[e, ...e.querySelectorAll("[title]")].map((x) => x.title || "")].join(" | "));
    // a sentence and a tooltip elsewhere in the window naming both accounts
    const sample = (page) => page.evaluate(([me, team]) => {
      const d = document.createElement("div");
      d.id = "sample";
      d.textContent = `Personal · ${me} · ${team}`;
      d.title = `${me} · ${team}`;
      document.querySelector("main, body").append(d);
    }, [ME, TEAM]);

    test(`${engine} ${lang}: the provider editor's eye shows one account while the others stay hidden`, async (t) => {
      const browser = await launch(t);
      for (const width of [900, 360]) {
        const context = await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" });
        const errors = [];
        const open = async (url) => {
          const page = await context.newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", serve(lang, url.includes("mode=panel")));
          await page.goto(url);
          return page;
        };
        const page = await open("http://magpie.test/?view=providers");
        await page.locator(".row.provider", { hasText: "ZCode" }).first().click();
        const row = (id) => page.locator(`.editor .accts .acc[data-account-id="${id}"]`);
        await row(TEAM).locator(".acct-who").waitFor();
        // the pointer off the rows: the click that opened the editor left it
        // on ME's name at 360px
        await page.mouse.move(0, 0);
        const eye = (id) => row(id).locator(".acct-eye");

        // Hide accounts off: no eye at all
        assert.equal(await eye(ME).count(), 1);
        assert.equal(await opacity(eye(ME)), "none", "no eye while Hide accounts is off");

        await page.evaluate(() => hideAccounts.set(true, true));
        await sample(page);
        await page.waitForFunction(() => document.querySelectorAll("#sample .pii").length === 2);
        await page.waitForFunction((me) => document.querySelector(`.editor .acc[data-account-id="${me}"] .acct-user .pii`), ME);
        for (const id of [ME, TEAM]) assert.ok(!(await seen(row(id))).includes(id), `${id} is hidden first`);

        // quiet until the name is pointed at; named for what it does
        assert.equal(await opacity(eye(ME)), "0");
        await row(ME).locator(".acct-who").hover();
        assert.equal(await opacity(eye(ME)), "1", "the eye shows on hover");
        assert.equal(await eye(ME).getAttribute("aria-label"), await tr(page, KEYS[0]));
        assert.equal(await eye(ME).getAttribute("aria-pressed"), "false");
        assert.equal(await eye(ME).getAttribute("title"), await tr(page, KEYS[0]));

        // a click shows that one, by its address and its name, everywhere
        // the window has it, its tooltips too; nothing moves
        await eye(ME).scrollIntoViewIfNeeded();
        await page.waitForTimeout(150);
        const top = await row(ME).evaluate((e) => Math.round(e.getBoundingClientRect().top)), sc = await scrolled(page);
        await eye(ME).click();
        await page.waitForFunction((me) => !document.querySelector(`.editor .acc[data-account-id="${me}"] .pii`), ME);
        assert.equal(await row(ME).evaluate((e) => Math.round(e.getBoundingClientRect().top)), top, "the row moved");
        assert.equal(await scrolled(page), sc, "a click scrolled");
        assert.equal(await row(ME).locator(".n.rename").textContent(), "Personal");
        assert.equal(await row(ME).locator(".acct-user").textContent(), ME);
        assert.ok((await row(ME).locator(".n.rename").getAttribute("title")).includes(ME), "its tooltip names it");
        assert.equal(await eye(ME).getAttribute("aria-pressed"), "true");
        assert.equal(await eye(ME).getAttribute("title"), await tr(page, KEYS[1]));
        assert.equal(await opacity(eye(ME)), "1", "a shown account's eye stays in sight");
        await page.waitForFunction(() => document.querySelectorAll("#sample .pii").length === 1);
        assert.equal(await page.locator("#sample .pii").getAttribute("data-raw"), TEAM);
        assert.ok((await page.locator("#sample").textContent()).startsWith(`Personal · ${ME} · `));
        assert.ok(!(await page.locator("#sample").textContent()).includes(TEAM));
        assert.equal(await page.locator("#sample").getAttribute("title"), `${ME} · ${"•".repeat(10)}@••.•••`);
        // the other stays hidden, in its row and its tooltips
        assert.ok(await row(TEAM).locator(".pii").count() > 0);
        assert.ok(!(await seen(row(TEAM))).includes(TEAM), "the other account stays hidden");
        assert.equal(await eye(TEAM).getAttribute("aria-pressed"), "false");

        // from the keyboard: Tab from the name reaches its eye, which shows
        await page.mouse.move(0, 0);
        await row(TEAM).locator(".n.rename").focus();
        // WebKit on macOS tabs to a button only with Option, as Safari does
        await page.keyboard.press(engine === "webkit" && process.platform === "darwin" ? "Alt+Tab" : "Tab");
        assert.ok(await eye(TEAM).evaluate((e) => e === document.activeElement), "Tab reaches the eye");
        assert.equal(await opacity(eye(TEAM)), "1", "the eye shows on focus");
        await page.keyboard.press("Enter");
        await page.waitForFunction((id) => !document.querySelector(`.editor .acc[data-account-id="${id}"] .pii`), TEAM);
        assert.ok((await seen(row(TEAM))).includes(TEAM));
        await page.keyboard.press(" ");
        await page.waitForFunction((id) => document.querySelector(`.editor .acc[data-account-id="${id}"] .pii`), TEAM);
        assert.ok(!(await seen(row(TEAM))).includes(TEAM), "pressed again, hidden again");

        // the tray panel follows the window's setting, not its eye: one
        // shown in the window stays hidden in a screenshot of the panel
        assert.equal(await eye(ME).getAttribute("aria-pressed"), "true", "still shown in the window");
        const panel = await open("http://magpie.test/?mode=panel");
        await panel.evaluate(() => setPanelTab("usage"));
        await panel.locator("#panelQuota .pq-user").first().waitFor();
        await panel.waitForFunction(() => document.querySelector("#panelQuota .pq-user .pii"));
        const pq = await panel.evaluate(() => [...document.querySelectorAll("#panelQuota .pq-card")].map((c) => c.textContent + c.title).join(" "));
        assert.ok(!pq.includes(ME), "the panel still hides it: " + pq);
        await panel.close();

        // the window closed: every account hidden again
        await page.evaluate(() => hideAccounts.closed());
        await page.waitForFunction(() => document.querySelectorAll("#sample .pii").length === 2);
        for (const id of [ME, TEAM]) assert.ok(!(await seen(row(id))).includes(id), `${id} is hidden after the window closed`);
        assert.equal(await eye(ME).getAttribute("aria-pressed"), "false");

        // and Hide accounts off: the eyes go
        await page.evaluate(() => hideAccounts.set(false, true));
        assert.equal(await opacity(eye(ME)), "none");

        assert.equal(await page.locator("select").count(), 0, "no native select");
        assert.deepEqual(await stripes(page, ".accts .acct-who, .accts .acct-who *"), [], "no border stripes");
        assert.equal(await wide(page), 0, `no sideways scroll at ${width}px`);
        const missing = await page.evaluate(([keys, lang]) => lang === "en" ? [] : keys.filter((k) => !I18N[lang]?.[k]), [KEYS, lang]);
        assert.deepEqual(missing, [], `every string has its ${lang}`);
        assert.deepEqual(errors, []);
        await context.close();
      }
    });

    test(`${engine} ${lang}: the tray panel's eye shows one account there, until the panel is put away`, async (t) => {
      const browser = await launch(t);
      for (const width of [360, 440]) {
        const context = await browser.newContext({ viewport: { width, height: 640 }, reducedMotion: "reduce" });
        const errors = [];
        const open = async (url) => {
          const page = await context.newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", serve(lang, url.includes("mode=panel")));
          await page.goto(url);
          return page;
        };
        const panel = await open("http://magpie.test/?mode=panel");
        await panel.evaluate(() => setPanelTab("usage"));
        await panel.locator("#panelQuota .pq-user").first().waitFor();
        // the group's second account is under its "more"
        await panel.locator("#panelQuota .pq-group .pq-more:not(.pq-arrange)").first().click();
        await panel.waitForFunction(() => document.querySelectorAll("#panelQuota .pq-card").length === 2);
        const card = (u) => panel.locator(`#panelQuota .pq-card:has(.pq-user:text-is("${u}")), #panelQuota .pq-card:has(.pq-user .pii[data-raw="${u}"])`).first();
        const eye = (u) => card(u).locator(".acct-eye");
        assert.equal(await eye("Personal").count(), 1, "the panel's card has an eye");
        assert.equal(await opacity(eye("Personal")), "none", "no eye while Hide accounts is off");

        await panel.evaluate(() => hideAccounts.set(true));
        await panel.waitForFunction(() => document.querySelectorAll("#panelQuota .pq-user .pii").length === 1);
        await panel.waitForFunction((me) => [...document.querySelectorAll("#panelQuota .pq-card")].some((c) => c.querySelector(".pq-user").textContent === "Personal" && !c.title.includes(me)), ME);
        await panel.mouse.move(0, 0);
        assert.equal(await opacity(eye("Personal")), "0");
        await card("Personal").hover();
        assert.equal(await opacity(eye("Personal")), "1", "the eye shows on hover over its card");
        assert.equal(await eye("Personal").getAttribute("aria-label"), await tr(panel, KEYS[0]));
        assert.equal(await eye("Personal").getAttribute("title"), await tr(panel, KEYS[0]));

        // a click shows that one, its card's tooltip too; the other stays hidden; nothing moves
        const sc = await scrolled(panel);
        await eye("Personal").click();
        await panel.waitForFunction((me) => [...document.querySelectorAll("#panelQuota .pq-card")].some((c) => c.querySelector(".pq-user").textContent === "Personal" && c.title.includes(me)), ME);
        assert.equal(await panel.locator("#panelQuota .pq-user .pii").count(), 1);
        assert.equal(await scrolled(panel), sc, "a click scrolled");
        assert.equal(await card("Personal").locator(".pq-user").textContent(), "Personal");
        assert.ok((await card("Personal").getAttribute("title")).includes(ME), "its tooltip names it");
        assert.equal(await eye("Personal").getAttribute("aria-pressed"), "true");
        assert.equal(await eye("Personal").getAttribute("title"), await tr(panel, KEYS[1]));
        assert.equal(await opacity(eye("Personal")), "1", "a shown account's eye stays in sight");
        assert.ok(!(await seen(card(SEAT))).includes(TEAM) && !(await seen(card(SEAT))).includes(SEAT), "the other card stays hidden");
        assert.equal(await eye(SEAT).getAttribute("aria-pressed"), "false");
        // redrawn (a refresh, a tab), it stays shown
        await panel.evaluate(() => renderPanelQuota());
        assert.equal(await panel.locator("#panelQuota .pq-user .pii").count(), 1);
        assert.equal(await eye("Personal").getAttribute("aria-pressed"), "true");

        // the window has its own: the account shown in the panel stays hidden there
        const win = await open("http://magpie.test/?view=usage");
        await win.locator(".subscription-account .quota-alias").first().waitFor();
        await win.waitForFunction(() => document.querySelectorAll(".subscription-account .quota-of .pii").length === 2);
        const ws = await win.evaluate(() => [...document.querySelectorAll(".subscription-account")].map((c) => c.textContent).join(" "));
        assert.ok(!ws.includes(ME) && ws.includes("Personal"), "the window still hides it, not its name: " + ws);
        await win.close();

        // put away (the panel's page goes hidden): hidden again
        await panel.evaluate(() => {
          Object.defineProperty(document, "hidden", { configurable: true, get: () => true });
          document.dispatchEvent(new Event("visibilitychange"));
        });
        await panel.waitForFunction((me) => [...document.querySelectorAll("#panelQuota .pq-card")].some((c) => c.querySelector(".pq-user").textContent === "Personal" && !c.title.includes(me)), ME);
        assert.ok(!(await seen(card("Personal"))).includes(ME), "hidden once the panel is put away");
        assert.equal(await eye("Personal").getAttribute("aria-pressed"), "false");

        assert.equal(await panel.locator("select").count(), 0, "no native select");
        assert.deepEqual(await stripes(panel, ".pq-card, .pq-card *"), [], "no border stripes");
        assert.equal(await wide(panel), 0, `no sideways scroll at ${width}px`);
        const fits = await panel.evaluate(() => [...document.querySelectorAll("#panelQuota .pq-card")].every((c) => { const r = c.getBoundingClientRect(), e = c.querySelector(".acct-eye").getBoundingClientRect(); return e.right <= r.right && e.width > 0; }));
        assert.ok(fits, "each eye sits inside its card");
        assert.deepEqual(errors, []);
        await context.close();
      }
    });

    test(`${engine} ${lang}: a Usage card's eye shows its account, on the card and its tooltips`, async (t) => {
      const browser = await launch(t);
      for (const width of [900, 360]) {
        const context = await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" });
        const page = await context.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang));
        await page.goto("http://magpie.test/?view=usage");
        await page.locator(".subscription-account .quota-alias").first().waitFor();
        const card = (id) => page.locator(".subscription-account", { has: page.locator(".quota-of", { hasText: id }) });
        assert.equal(await opacity(card(ME).locator(".acct-eye")), "none");

        await page.locator("#usageMask").click();
        await page.waitForFunction(() => document.querySelectorAll(".subscription-account .quota-of .pii").length === 2);
        const eye = (id) => page.locator(".subscription-account .acct-eye").nth(id === TEAM ? 0 : 1);
        assert.equal(await opacity(eye(ME)), "0");
        await page.locator(".subscription-account").nth(1).hover();
        assert.equal(await opacity(eye(ME)), "1");
        const sc = await scrolled(page);
        await eye(ME).click();
        await page.waitForFunction(() => document.querySelectorAll(".subscription-account .quota-of .pii").length === 1);
        assert.equal(await scrolled(page), sc, "a click scrolled");
        const mine = page.locator(".subscription-account").nth(1), team = page.locator(".subscription-account").nth(0);
        assert.equal(await mine.locator(".quota-alias").textContent(), "Personal");
        assert.equal(await mine.locator(".quota-alias").getAttribute("title"), "Personal · " + ME);
        assert.equal(await mine.locator(".quota-of").textContent(), ME);
        assert.ok(!(await seen(team)).includes(TEAM) && !(await seen(team)).includes(SEAT), "the other card stays hidden");
        assert.equal(await eye(ME).getAttribute("aria-pressed"), "true");

        assert.equal(await page.locator("select").count(), 0, "no native select");
        assert.deepEqual(await stripes(page, ".subscription-account, .subscription-account *"), [], "no border stripes");
        assert.equal(await wide(page), 0, `no sideways scroll at ${width}px`);
        assert.deepEqual(errors, []);
        await context.close();
      }
    });
  }
}
