// Run with Node's test runner and Playwright on the module path; see README.md.
// A Codex account spending a reset by itself (StringKe on Discord: resets
// gifted to the account, used once its week is out), a standing say of the
// account's (#719, thedavidweng): every named Codex account's card has a
// row that says both times it uses one — the week running out, a reset
// about to expire — with a switch, off until turned on, that posts
// settings/codex-auto-reset for that account; an account holding no reset
// has it too, so it can be set ahead of one. Holding resets, the row of
// them says whether the one expiring first is auto-used, beside "Use a
// reset" on the Usage page's card and "Use one…" on the menu bar panel's.
// An account in brief says its say beside its name, with its switch (#801:
// the account a week ran out on is the one in brief). A GLM team's resets,
// spent on its own site, a plugin's, and an account with no name get none,
// nor a button to use one. No click moves the page; no left-border accent.
// English and Chinese, Chromium and WebKit; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const later = new Date(Date.now() + 3 * 864e5).toISOString();
const now = new Date().toISOString();
const quotas = [
  { provider: "codex", name: "Codex", icon: "codex-color", user: "Me@example.com", plan: "Plus", lastServedAt: now,
    windows: [{ name: "5 hours", used: 100, resetsAt: later }, { name: "7 days", used: 100, resetsAt: later }],
    resets: { count: 2, until: later } },
  // a second account, holding no reset: its say is set ahead of one
  { provider: "codex", name: "Codex", icon: "codex-color", user: "two@example.com", plan: "Plus",
    windows: [{ name: "5 hours", used: 10, resetsAt: later }, { name: "7 days", used: 20, resetsAt: later }] },
  { provider: "zhipu", name: "GLM Coding", icon: "zhipu-color", user: "team@example.com", plan: "Team",
    windows: [{ name: "5 hours", used: 30 }], resets: { count: 1, byWindow: true, fiveHour: 1 } },
  // a plugin's, its resets told but not spent from magpie: never sent to Codex's
  { provider: "codex-plugin", name: "Codex (plugin)", user: "Me@example.com", plan: "Plus",
    windows: [{ name: "7 days", used: 100, resetsAt: later }], resets: { count: 3, until: later } },
];

function serve(lang, posts) {
  let auto = [];
  const settings = () => ({ theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd", trayUsages: ["codex", "zhipu", "codex-plugin"], trayUsageEvery: 3, codexAutoReset: auto });
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: settings() });
    if (url.pathname === "/api/settings") return json(settings());
    if (url.pathname === "/api/settings/codex-auto-reset") {
      const body = req.postDataJSON();
      posts.push(body);
      const who = body.user.toLowerCase();
      auto = auto.filter((u) => u !== who);
      if (body.on) auto.push(who);
      return json(settings());
    }
    if (url.pathname === "/api/usage/quotas") return json(quotas);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: {
    say: "Auto-use resets: when the week runs out, or before one expires",
    week: /week runs out/, expiry: /before one expires/,
    keptOn: "· auto-used before it expires", keptOff: "· not auto-used",
    brief: "Auto-use", label: "Auto-use resets",
    on: (who) => `${who} uses a reset by itself when its week runs out, or before one expires`, off: (who) => `${who} no longer uses a reset by itself`,
    use: "Use a reset", useOne: "Use one…",
  },
  zh: {
    say: "自动使用重置卡： 周额度用完时 · 重置卡到期前",
    week: /周额度用完时/, expiry: /重置卡到期前/,
    keptOn: "· 到期前自动使用", keptOff: "· 不会自动使用",
    brief: "自动用卡", label: "自动使用重置卡",
    on: (who) => `${who} 会在周额度用完时、或重置卡到期前自动使用重置卡`, off: (who) => `${who} 不再自动使用重置卡`,
    use: "用一张重置卡", useOne: "用一张…",
  },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: a Codex account's resets used by themselves, a standing say set held or not`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const pages = [];
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          for (const [i, p] of pages.entries()) await p.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-codex-auto-reset-${i}.png`) });
        }
        await browser.close();
      });
      const errors = [];
      const open = async (url, viewport, posts) => {
        const page = await (await browser.newContext({ viewport, reducedMotion: "reduce" })).newPage();
        pages.push(page);
        page.setDefaultTimeout(5000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, posts));
        await page.goto(url);
        return page;
      };
      const wait = async (posts, n) => { for (let i = 0; i < 60 && posts.length < n; i++) await new Promise((r) => setTimeout(r, 50)); };
      const text = (loc) => loc.evaluate((e) => e.textContent.replace(/\s+/g, " ").trim());
      // a switch's click: it posts, turns, and moves nothing on the page
      const flip = async (page, sel, posts, who, want) => {
        const b = page.locator(sel);
        const at = await b.evaluate((e) => [e.getBoundingClientRect().top, document.scrollingElement.scrollTop, ...[...document.querySelectorAll(".view")].map((v) => v.scrollTop)]);
        const n = posts.length;
        await b.click();
        await wait(posts, n + 1);
        assert.deepEqual(posts.at(-1), { user: who, on: want });
        await page.locator(sel + `[aria-checked="${want}"]`).waitFor();
        assert.equal(await page.locator(sel).evaluate((e) => e.classList.contains("on")), want);
        assert.equal(await page.locator("#status").textContent(), want ? w.on(who) : w.off(who));
        await page.waitForTimeout(200);
        assert.deepEqual(await page.locator(sel).evaluate((e) => [e.getBoundingClientRect().top, document.scrollingElement.scrollTop, ...[...document.querySelectorAll(".view")].map((v) => v.scrollTop)]), at, "the click moved the page");
      };

      // the Usage page's card: Me in full, two in brief
      const posts = [];
      const page = await open("http://magpie.test/?view=usage", { width: 900, height: 700 }, posts);
      const me = '.quota-autoreset[data-user="Me@example.com"]', two = '.quota-autoreset[data-user="two@example.com"]';
      await page.locator(me).waitFor();
      assert.equal(await page.locator(".quota-autoreset").count(), 1, "only Codex's own named accounts in full have the say; the GLM team's and the plugin's don't");
      assert.equal(await page.locator(".quota-resets").count(), 3, "every account's resets in full are told");
      assert.equal(await page.locator(".quota-resets button").count(), 1, "only Codex's resets are used from here");
      // both times it uses one, in the row's own words, not only its tooltip
      const said = await text(page.locator(me + " .ar-say"));
      assert.equal(said, w.say);
      assert.match(said, w.week);
      assert.match(said, w.expiry);
      assert.equal(await page.locator(me + " .auto-reset").getAttribute("role"), "switch");
      assert.equal(await page.locator(me + " .auto-reset").getAttribute("aria-checked"), "false", "off until turned on");
      assert(await page.locator(me + " .auto-reset").getAttribute("title"));
      // the resets held: the one expiring first not kept until it is on
      const kept = '.quota-resets .resets-kept';
      assert.equal((await page.locator(kept).textContent()).trim(), w.keptOff);
      assert.equal((await page.locator(".quota-resets .text").textContent()).trim(), w.use);
      // the quick switch, with resets held
      await flip(page, me + " .auto-reset", posts, "Me@example.com", true);
      assert.equal((await page.locator(kept).textContent()).trim(), w.keptOn);
      assert.equal(await page.locator(kept).evaluate((e) => e.classList.contains("on")), true);

      // the account in brief says its say beside its name, with its
      // switch: the account a week ran out on is the one in brief (#801)
      const brief = '.subscription-account.brief[data-card="codex|two@example.com"]';
      assert.equal((await page.locator(brief + " .acct-auto").textContent()).trim(), w.brief);
      assert.equal(await page.locator(brief + " .auto-reset").getAttribute("role"), "switch");
      assert.equal(await page.locator(brief + " .auto-reset").getAttribute("aria-label"), w.label);
      await flip(page, brief + " .auto-reset", posts, "two@example.com", true);
      assert.equal(await page.locator(brief + " .acct-auto").evaluate((e) => e.classList.contains("on")), true);
      // in full, holding no reset, it has the say and its switch, and no reset row
      await page.locator(brief + " .quota-acct-fold").click();
      await page.locator(two).waitFor();
      assert.equal(await text(page.locator(two + " .ar-say")), w.say);
      assert.equal(await page.locator(two + " .auto-reset").getAttribute("aria-checked"), "true");
      assert.equal(await page.locator(".quota-resets .text").count(), 1, "nothing to use for the account holding none");
      await flip(page, two + " .auto-reset", posts, "two@example.com", false);
      // back in brief, off
      await page.locator('.subscription-account[data-card="codex|two@example.com"] .quota-acct-fold').click();
      await page.locator(brief + ' .auto-reset[aria-checked="false"]').waitFor();
      assert.equal(await page.locator(brief + " .acct-auto").evaluate((e) => e.classList.contains("on")), false);
      // and Me in brief: the resets it holds, and on
      await page.locator('.subscription-account[data-card="codex|Me@example.com"] .quota-acct-fold').click();
      const meBrief = '.subscription-account.brief[data-card="codex|Me@example.com"] .acct-auto';
      await page.locator(meBrief).waitFor();
      assert.equal((await page.locator(meBrief).textContent()).trim(), "↺ 2 ·" + w.brief);
      assert.equal(await page.locator(meBrief + " .auto-reset").getAttribute("aria-checked"), "true");
      const border = await page.evaluate(() => [...document.querySelectorAll(".quota-resets, .quota-resets *, .quota-autoreset, .quota-autoreset *, .acct-auto")].map((e) => getComputedStyle(e).borderLeftWidth).filter((b) => parseFloat(b) > 1));
      assert.deepEqual(border, [], "no left-border accent");
      assert.equal(await page.locator("select").count(), 0, "no native select");

      // the menu bar panel's card
      const panelPosts = [];
      const panel = await open("http://magpie.test/?mode=panel", { width: 440, height: 600 }, panelPosts);
      const psel = '.pq-autoreset[data-user="Me@example.com"]';
      await panel.locator('#ptabs [data-ptab="usage"]').click();
      await panel.locator(psel).waitFor();
      assert.equal(await text(panel.locator(psel + " .ar-say")), w.say);
      assert.equal(await panel.locator(psel + " .auto-reset").getAttribute("aria-checked"), "false");
      assert.equal(await panel.locator(".pq-resets").count(), 3);
      assert.equal(await panel.locator(".pq-resets button").count(), 1, "only Codex's resets are used from the panel");
      assert.equal((await panel.locator(".pq-resets button").textContent()).trim(), w.useOne);
      assert.equal((await panel.locator(".pq-resets .resets-kept").textContent()).trim(), w.keptOff);
      await flip(panel, psel + " .auto-reset", panelPosts, "Me@example.com", true);
      assert.equal((await panel.locator(".pq-resets .resets-kept").textContent()).trim(), w.keptOn);
      // the switch and its words fit the card: none of it cut off or wider
      const fits = await panel.locator(psel).evaluate((r) => {
        const c = r.closest(".pq-card").getBoundingClientRect(), b = r.getBoundingClientRect(), s = r.querySelector(".auto-reset").getBoundingClientRect();
        return b.right <= c.right + 0.5 && s.right <= c.right + 0.5 && s.width > 0;
      });
      assert(fits, "the say fits the panel's card");
      // the account holding none, shown: its say too
      // its group's Show more, not the tab's Arrange (490d2ea3), which is a .pq-more too
      await panel.locator(".pq-group .pq-more").click();
      const ptwo = '.pq-autoreset[data-user="two@example.com"]';
      await panel.locator(ptwo).waitFor();
      await flip(panel, ptwo + " .auto-reset", panelPosts, "two@example.com", true);

      const missing = await page.evaluate(() => [
        "Auto-use resets", "Auto-use resets:", "when the week runs out, or before one expires", "Auto-use: on", "Auto-use: off",
        "auto-used before it expires", "not auto-used",
        "{who} no longer uses a reset by itself", "{who} uses a reset by itself when its week runs out, or before one expires",
        "Auto-use is on: the reset that runs out first is used about half an hour before it does, if this account's windows have been used, so what they have left can be used until then and it isn't lost; at once if the account is held up until after then.",
        "Auto-use is off: the reset that runs out first is lost unless it's used by hand before then.",
        "On: a reset is used by itself when this account's week is used up and no other account can answer, one a week at most, and the one about to run out is used about half an hour before it does, if the account has been used, or at once when the account is held up until after then. Otherwise the five hours running out never uses one. Click to turn it off.",
        "Off: this account's resets are used only by hand. Turned on, one is used by itself when its week is used up and no other account can answer, one a week at most, and the one about to run out about half an hour before it does, if the account has been used, or at once when the account is held up until after then. Otherwise the five hours running out never uses one.",
      ].filter((k) => !I18N.zh[k]));
      assert.deepEqual(missing, [], "every string has its Chinese");
      // the credit is 重置卡 in Chinese wherever it is counted or spent
      const zhCredit = await page.evaluate(() => ["1 reset", "{n} resets", "Use a reset", "Use one…", "Use a Codex reset?", "{who} no longer uses a reset by itself"].map((k) => I18N.zh[k]));
      for (const s of zhCredit) assert.match(s, /张|重置卡/, s);
      assert.deepEqual(errors, []);
    });
  }
}
