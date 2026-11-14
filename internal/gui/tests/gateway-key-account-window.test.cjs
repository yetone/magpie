// A gateway key's limit that follows a subscription account's own window
// (ljayic on X): the window menu offers "An account's window", a second
// picker lists the accounts' windows as last read, Save sends the account
// and window, and a limit whose window can't be read now says it resets
// with the account's, never a made-up time. In every language, both
// engines, and at a narrow width, with no native select.
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { fixture } = require("./fixtures/caller-keys.cjs");

const src = fs.readFileSync(path.resolve(__dirname, "../assets/i18n.js"), "utf8");
const I18N = new Function(src.slice(0, src.indexOf("\n};") + 3) + "; return I18N;")();

const hour = 36e5;
const accountWindows = () => [
  { account: "codex/me@example.com", window: "5h", provider: "Codex", user: "me@example.com",
    start: new Date(Date.now() - 2 * hour).toISOString(), reset: new Date(Date.now() + 3 * hour).toISOString() },
  { account: "codex/me@example.com", window: "Weekly", provider: "Codex", user: "me@example.com",
    start: new Date(Date.now() - 48 * hour).toISOString(), reset: new Date(Date.now() + 120 * hour).toISOString() },
];
// Laptop's limit follows Claude's 5 hours, which can't be read now: spent,
// and held until that window resets
const limits = () => ({
  laptop: {
    limit: { period: "account", account: "claude/me@example.com", accountWindow: "5h", tokens: 1000 },
    used: { period: "account", account: "claude/me@example.com", accountWindow: "5h", resetUnknown: true,
      start: new Date(Date.now() - 4 * hour).toISOString(), reset: "0001-01-01T00:00:00Z", calls: 9,
      tokens: 1200, cost: 0, tokenLimit: 1000, tokensLeft: 0, spent: true },
  },
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: a key's limit follows an account's window`, async (t) => {
      const tr = (k, vars = {}) => {
        const s = lang === "en" ? k : I18N[lang][k];
        assert.equal(typeof s, "string", `${lang} has ${k}`);
        return s.replace(/\{(\w+)\}/g, (_, v) => String(vars[v]));
      };
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 1000, height: 700 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(6000);
      page.setDefaultNavigationTimeout(60000);
      const events = [], errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", fixture(lang, "light", events, { lan: true, limits: limits(), accountWindows: accountWindows() }));
      await page.goto("http://magpie.test/?view=gateway");
      await page.locator("#gatewayKeys .acc[data-key]").last().waitFor();
      const row = (id) => page.locator(`#gatewayKeys .acc[data-key="${id}"]`);
      const sent = (action) => events.filter((e) => e.action === action);
      const per = tr("An account's window");
      if (lang === "zh") assert.equal(per, "账号的周期");

      // the row: the account's window, and refused until it resets, no time
      const claude5h = tr("{window} of {account}", { window: "5h", account: "claude/me@example.com" });
      assert.match((await row("laptop").locator(".amodels.limit").textContent()).trim(), new RegExp(`· ${claude5h.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}$`));
      const line = row("laptop").locator(".key-limit").first();
      assert((await line.textContent()).includes(tr("refused until the account's window resets")), await line.textContent());
      assert.doesNotMatch(await line.textContent(), /0001|1970/);
      const tail = tr("Counted from {start}; the account's window can't be read now, so when it resets isn't known", { start: "\u0000" }).split("\u0000")[1];
      assert((await line.getAttribute("title")).endsWith(tail), await line.getAttribute("title"));

      // a new limit: the menu offers the account's window before "in total"
      await row("work").getByRole("button", { name: tr("Set a limit for this key"), exact: true }).click();
      const wed = row("work").locator(".key-limit-ed");
      await wed.waitFor();
      await wed.getByRole("textbox", { name: tr("Estimated cost limit, US$"), exact: true }).fill("5");
      const acct = wed.getByRole("button", { name: tr("Account window"), exact: true });
      assert.equal(await acct.isVisible(), false, "the account picker shows for another window");
      await wed.getByRole("button", { name: tr("Limit window"), exact: true }).click();
      const items = page.locator(".proto-menu .pm-item");
      const names = (await items.allTextContents()).map((s) => s.trim());
      assert.deepEqual(names.slice(-2), [per, tr("in total")]);
      await items.nth(names.length - 2).click();
      assert.equal(await wed.getByRole("button", { name: tr("Limit window"), exact: true }).getAttribute("data-value"), "account");
      await acct.waitFor();
      // none picked yet: said, and nothing to save
      const save = wed.getByRole("button", { name: tr("Save"), exact: true });
      assert.equal(await save.isDisabled(), true);
      assert((await wed.textContent()).includes(tr("Pick an account's window")));
      // the accounts' windows as read, with when each resets
      await acct.click();
      const picks = page.locator(".proto-menu .pm-item");
      await picks.first().waitFor();
      assert.equal(await picks.count(), 2);
      const weekly = tr("{window} of {account}", { window: "Weekly", account: "codex/me@example.com" });
      assert.equal((await picks.nth(1).locator(".pm-name").textContent()).trim(), weekly);
      await picks.nth(1).click();
      assert.equal((await acct.textContent()).trim(), weekly);
      assert.equal(await wed.locator("select").count(), 0, "no native select");
      // narrow: the editor fits
      await page.setViewportSize({ width: 500, height: 740 });
      await page.waitForTimeout(100);
      assert.equal(await wed.evaluate((e) => e.scrollWidth > e.clientWidth || e.getBoundingClientRect().right > window.innerWidth), false, "the editor overflows at 500px");
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth), false, "the page overflows at 500px");
      await page.setViewportSize({ width: 1000, height: 700 });
      await save.click();
      await wed.waitFor({ state: "detached" });
      assert.deepEqual(sent("limit-key").map((e) => e.body), [{ key: "work", limit: { period: "account", tokens: 0, cost: 5, cacheReads: false, account: "codex/me@example.com", accountWindow: "Weekly" } }]);
      assert.match(await row("work").locator(".amodels.limit").textContent(), new RegExp(`\\$5\\.00 · ${weekly.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}`));

      // the window kept on Laptop isn't read now: it stays, said so
      await row("laptop").getByRole("button", { name: tr("Change this key's limit"), exact: true }).click();
      const ed = row("laptop").locator(".key-limit-ed");
      await ed.waitFor();
      const lacct = ed.getByRole("button", { name: tr("Account window"), exact: true });
      assert.equal((await lacct.textContent()).trim(), claude5h);
      await lacct.click();
      await page.locator(".proto-menu .pm-item").first().waitFor();
      assert.equal(await page.locator(".proto-menu .pm-item").count(), 3);
      assert.equal((await page.locator(".proto-menu .pm-item").last().locator(".pm-note").textContent()).trim(), tr("Not read now"));
      await page.keyboard.press("Escape");
      // Reset usage says it still resets when the account's window does
      await ed.getByRole("button", { name: tr("Reset usage"), exact: true }).click();
      const modal = page.locator("#modal");
      await modal.getByText(tr("Reset {name}'s usage?", { name: "Laptop" })).waitFor();
      assert((await modal.textContent()).includes(tr("The tokens and cost {name} has used go back to 0 together. Calls so far in this window stop counting; it still resets when the account's window does.", { name: "Laptop" })));
      await modal.getByRole("button", { name: tr("Reset usage"), exact: true }).click();
      await modal.waitFor({ state: "hidden" });
      assert.deepEqual(sent("reset-limit-key").map((e) => e.body), [{ key: "laptop" }]);
      assert.deepEqual(errors, []);
    });
  }
}
