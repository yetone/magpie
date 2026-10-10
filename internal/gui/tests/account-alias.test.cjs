// Run with Node's test runner and Playwright on the module path; see README.md.
// A signed-in account has a name of the user's own (#1515, aindijrncom):
// a GLM Coding Plan team seat signed in through the ZCode plugin read as
// "31****36@qq.com" or just "ZCode" on the Usage page, while the key plan
// it stands in for had the name they gave it. The editor's account row
// names the account by that name with its address beside it, or, unnamed,
// by its address with the key plan it is the same seat as; a click on the
// name types a new one in place, posted once to provider/accountname. The
// Usage page's card and the tray panel's name it the same way, and Hide
// accounts hides the name. A click moves nothing; no native select, no
// border stripe; it fits at 360px. In every language.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const SEAT = "某某·GLM高级版", TEAM = "3141592636@qq.com", ME = "me@example.com";
const inHours = (h) => new Date(Date.now() + h * 3600e3 + 60e3).toISOString();
const fresh = () => ({
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
  { provider: "zcode", name: "ZCode", icon: "zcode", plan: "Lite", user: ME, alias: "Personal",
    windows: [{ name: "5 hours", used: 60, resetsAt: inHours(2) }] },
];

function serve(lang, posts, panel = false) {
  const settings = { theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd" };
  const providers = { providers: [fresh()], presets: [], excluded: [], gateway: { running: true, window: true } };
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
    if (url.pathname === "/api/provider/accountname" && route.request().method() === "POST") {
      const body = route.request().postDataJSON();
      posts.push(body);
      const p = providers.providers[0], names = { ...(p.accountNames || {}) };
      if (body.alias.trim()) names[body.account.toLowerCase()] = body.alias.trim();
      else delete names[body.account.toLowerCase()];
      p.accountNames = names;
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const KEYS = ["Name this account", "{user} · click to rename", "The same seat as the key plan {name}, shown once, on this account's card", "Name, e.g. Personal or Team"];
const LANGS = ["en", "zh", "zh-TW", "ja", "de"];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of LANGS) {
    const launch = async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      return browser;
    };
    const tr = (page, k, v) => page.evaluate(([k, v]) => t(k, v), [k, v]);
    const scrolled = (page) => page.evaluate(() => [window.scrollY, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop > 0).map((e) => `${e.className}:${e.scrollTop}`)].join(" "));
    const wide = (page) => page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
    const stripes = (page, sel) => page.evaluate((sel) => [...document.querySelectorAll(sel)].map((e) => getComputedStyle(e).borderLeftWidth).filter((b) => parseFloat(b) > 1), sel);

    test(`${engine} ${lang}: an account is named on its row, renamed in place, and posted once`, async (t) => {
      const browser = await launch(t);
      for (const width of [900, 360]) {
        const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        const errors = [], posts = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, posts));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator(".row.provider", { hasText: "ZCode" }).first().click();
        const row = (id) => page.locator(`.editor .accts .acc[data-account-id="${id}"]`);
        await row(TEAM).locator(".acct-who").waitFor();

        // unnamed: its address, and the key plan it is the same seat as
        const team = row(TEAM).locator(".acct-who");
        assert.equal(await team.locator(".n.rename").textContent(), TEAM);
        assert.equal(await team.locator(".n.rename").getAttribute("title"), await tr(page, "Name this account"));
        assert.equal(await team.locator(".acct-seat").textContent(), "≡ " + SEAT);
        assert.equal(await team.locator(".acct-seat").getAttribute("title"), await tr(page, KEYS[2], { name: SEAT }));
        // named: its name, the address beside it
        const me = row(ME).locator(".acct-who");
        assert.equal(await me.locator(".n.rename").textContent(), "Personal");
        assert.equal(await me.locator(".acct-user").textContent(), ME);
        assert.equal(await me.locator(".n.rename").getAttribute("title"), await tr(page, KEYS[1], { user: ME }));
        assert.equal(await wide(page), 0, `no sideways scroll at ${width}px`);

        // a click types a name in place, the seat as its hint; nothing moves
        const b = team.locator(".n.rename");
        await b.scrollIntoViewIfNeeded();
        await page.waitForTimeout(150);
        const top = await row(TEAM).evaluate((e) => Math.round(e.getBoundingClientRect().top)), sc = await scrolled(page);
        await b.click();
        const typed = row(TEAM).locator("input.rename-in");
        await typed.waitFor();
        assert.equal(await typed.getAttribute("placeholder"), SEAT);
        assert.equal(await row(TEAM).evaluate((e) => Math.round(e.getBoundingClientRect().top)), top, "the row moved");
        assert.equal(await scrolled(page), sc, "a click scrolled");
        await typed.fill("  Team seat ");
        await typed.press("Enter");
        await page.waitForFunction((id) => document.querySelector(`.editor .acc[data-account-id="${id}"] .acct-who .n.rename`)?.textContent === "Team seat", TEAM);
        await page.waitForTimeout(200);
        assert.deepEqual(posts, [{ id: "zcode", account: TEAM, alias: "  Team seat " }], "posted once, Enter and the blur after it");
        assert.equal(await row(TEAM).locator(".acct-user").textContent(), TEAM);

        // Escape leaves it; an emptied name clears it back to the address
        await row(ME).locator(".acct-who .n.rename").click();
        await row(ME).locator("input.rename-in").press("Escape");
        await row(ME).locator(".acct-who").waitFor();
        assert.equal(posts.length, 1, "Escape posts nothing");
        // from the keyboard too, and a drag over the address selects it, renaming nothing
        await row(ME).locator(".acct-who .n.rename").focus();
        await page.keyboard.press("Enter");
        await row(ME).locator("input.rename-in").press("Escape");
        const box = await row(ME).locator(".acct-who .n.rename").boundingBox();
        await page.mouse.move(box.x + 2, box.y + box.height / 2); await page.mouse.down();
        await page.mouse.move(box.x + box.width - 2, box.y + box.height / 2, { steps: 8 }); await page.mouse.up();
        assert.ok(await page.evaluate(() => getSelection().toString().length > 0), "the name is selectable");
        assert.equal(await row(ME).locator("input.rename-in").count(), 0, "a drag renames nothing");
        await page.evaluate(() => getSelection().removeAllRanges());
        await row(ME).locator(".acct-who .n.rename").click();
        await row(ME).locator("input.rename-in").fill("");
        await row(ME).locator("input.rename-in").press("Enter");
        await page.waitForFunction((id) => document.querySelector(`.editor .acc[data-account-id="${id}"] .acct-who .n.rename`)?.textContent === id, ME);
        assert.deepEqual(posts.at(-1), { id: "zcode", account: ME, alias: "" });

        assert.equal(await page.locator("select").count(), 0, "no native select");
        assert.deepEqual(await stripes(page, ".accts .acct-who, .accts .acct-who *, .accts .rename-in"), [], "no border stripes");
        assert.equal(await wide(page), 0, `no sideways scroll at ${width}px`);
        const missing = await page.evaluate(([keys, lang]) => lang === "en" ? [] : keys.filter((k) => !I18N[lang]?.[k]), [KEYS, lang]);
        assert.deepEqual(missing, [], `every string has its ${lang}`);
        assert.deepEqual(errors, []);
        await page.context().close();
      }
    });

    test(`${engine} ${lang}: the Usage page and the tray panel name the account, and Hide accounts hides the name`, async (t) => {
      const browser = await launch(t);
      for (const width of [900, 360]) {
        const context = await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" });
        const errors = [];
        const open = async (url) => {
          const page = await context.newPage();
          page.setDefaultTimeout(5000);
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", serve(lang, [], url.includes("mode=panel")));
          await page.goto(url);
          return page;
        };
        const page = await open("http://magpie.test/?view=usage");
        await page.locator(".subscription-account .quota-alias").first().waitFor();
        const accts = await page.evaluate(() => [...document.querySelectorAll(".subscription-account")].map((a) => ({
          alias: a.querySelector(".quota-alias")?.textContent, aliasTitle: a.querySelector(".quota-alias")?.title,
          of: a.querySelector(".quota-of")?.textContent, seat: a.querySelector(".quota-seat")?.textContent ?? null,
        })));
        const seatLine = await tr(page, KEYS[2], { name: SEAT });
        // the seat: named for the key plan, said once in its tooltip
        assert.deepEqual(accts[0], { alias: SEAT, aliasTitle: SEAT + " · " + TEAM + "\n" + seatLine, of: TEAM, seat: null });
        assert.deepEqual(accts[1], { alias: "Personal", aliasTitle: "Personal · " + ME, of: ME, seat: null });
        assert.equal(await wide(page), 0, `no sideways scroll at ${width}px`);
        assert.deepEqual(await stripes(page, ".subscription-account, .subscription-account *"), [], "no border stripes");

        // the tray panel: the name before the address
        const panel = await open("http://magpie.test/?mode=panel");
        await panel.evaluate(() => setPanelTab("usage"));
        await panel.locator("#panelQuota .pq-user").first().waitFor();
        // one in sight, the other behind Show 1 more account
        await panel.locator("#panelQuota .pq-more:not(.pq-arrange)").click();
        await panel.waitForFunction(() => document.querySelectorAll("#panelQuota .pq-card").length === 2);
        const pq = await panel.evaluate(() => [...document.querySelectorAll("#panelQuota .pq-card")].map((c) => ({ text: c.querySelector(".pq-user").textContent, title: c.title.split(" · ").slice(0, 3).join(" · ") })));
        assert.deepEqual(pq, [{ text: SEAT, title: `ZCode · ${SEAT} · ${TEAM}` }, { text: "Personal", title: `ZCode · Personal · ${ME}` }]);

        // Hide accounts: the name of the user's own is an account's name too
        await page.locator("#usageMask").click();
        await page.waitForFunction(() => [...document.querySelectorAll(".subscription-account .pii")].some((e) => e.dataset.raw === "Personal"));
        const shown = await page.evaluate(() => [...document.querySelectorAll(".subscription-account")].map((a) => a.textContent + " " + [...a.querySelectorAll("[title]")].map((e) => e.title).join(" ")).join(" | "));
        assert.ok(!/Personal|3141592636|me@example/.test(shown), shown);
        await panel.waitForFunction(() => [...document.querySelectorAll("#panelQuota .pq-user .pii")].some((e) => e.dataset.raw === "Personal"));
        assert.deepEqual(errors, []);
        await context.close();
      }
    });
  }
}
