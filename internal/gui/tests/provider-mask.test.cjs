// Run with Node's test runner and Playwright on the module path; see README.md.
// Hide accounts is one switch for every page (paultian in #feedback: the
// Providers page had a switch of its own, so Providers could be hidden
// while Routing wasn't). Routing's, Usage's and Providers' buttons are the
// same switch: turned on any page, every page is hidden and every button
// says so, live in another window and the tray panel too. What an earlier
// version stored for either page is folded into it: hidden on either then
// is hidden everywhere now. Every language, wide and narrow; no backend.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const MAIL = "dev.one@example.com";

function serve(lang, panel) {
  const base = { icon: "generic", chat: "", responses: "", anthropic: "", catalog: "", agents: [], fallback: [], headers: {}, keyList: [], proxy: "" };
  const providers = { providers: [
    { ...base, id: "codex", name: "Codex", icon: "codex-color", models: [{ id: "gpt-6", name: "GPT-6", on: true }], account: { agent: "codex", agentName: "Codex", user: MAIL, plan: "PLUS", logins: [{ agent: "codex", user: MAIL, plan: "PLUS", active: true, on: true }] } },
  ], presets: [], excluded: [], gateway: { running: true, window: true } };
  const quotas = [{ provider: "codex", name: "Codex", icon: "openai", plan: "Plus", user: MAIL, windows: [{ name: "5 hours", used: 20 }] }];
  const settings = { lang, theme: "light", tray: "panel", quotaLeft: false, currency: "usd" };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${!panel}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/usage/quotas") return json(quotas);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const KEYS = ["Hide accounts", "Accounts hidden", "Mask the accounts on every page — email addresses and account names — for a screenshot to share", "Accounts are masked for a screenshot — click to show them", "Email addresses and account names on every page are blurred, for a screenshot to share"];
const BTNS = { routing: "#rtMask", usage: "#usageMask", providers: "#pvMask" };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1000, 420]) {
      // stored: what an earlier version left, [maskEmails, maskProviders]
      const launch = async (t, stored = [null, null]) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const context = await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" });
        await context.addInitScript((v) => { try { if (!localStorage.getItem("seeded")) { if (v[0] !== null) localStorage.setItem("magpie.maskEmails", v[0]); if (v[1] !== null) localStorage.setItem("magpie.maskProviders", v[1]); localStorage.setItem("seeded", "1"); } } catch {} }, stored);
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
        return { open, errors };
      };
      const provText = (page) => page.evaluate(() => document.querySelector("#providers").innerText);
      const listed = (page) => page.waitForFunction(() => document.querySelector("#providers")?.innerText.includes("Codex"));
      // every page's button says the one state, in its words and its tooltip
      const says = async (page, on, where) => {
        for (const [view, sel] of Object.entries(BTNS)) {
          const b = page.locator(sel);
          if (!(await b.count())) continue;
          assert.equal(await b.getAttribute("aria-pressed"), String(on), `${view}'s button ${where}`);
          assert.equal(await b.locator("[data-t]").textContent(), await page.evaluate((k) => t(k), KEYS[on ? 1 : 0]), `${view}'s words ${where}`);
          assert.equal(await b.getAttribute("title"), await page.evaluate((k) => t(k), KEYS[on ? 3 : 2]), `${view}'s tooltip ${where}`);
        }
      };

      test(`${engine} ${lang} ${width}: Hide accounts is one switch on every page`, async (t) => {
        const { open, errors } = await launch(t);
        const page = await open("http://magpie.test/?view=routing");
        await page.locator("#rtMask").waitFor();
        await page.evaluate((m) => { const d = document.createElement("div"); d.id = "sample"; d.textContent = "Codex as " + m; document.querySelector("#view-routing").append(d); }, MAIL);
        // a second window on the same storage, on Providers, and the panel
        const other = await open("http://magpie.test/?view=providers");
        await listed(other);
        const panel = await open("http://magpie.test/?mode=panel");
        await panel.evaluate(() => setPanelTab("usage"));
        await panel.waitForFunction((m) => document.querySelector("#panelQuota")?.innerText.includes(m), MAIL);

        // turned on Routing: Routing hidden, every button on
        await page.locator("#rtMask").click();
        await page.waitForFunction(() => document.querySelector("#sample .pii"));
        await says(page, true, "after Routing's was turned on");

        // and Providers hidden too, its own button saying so
        await page.evaluate(() => show("providers"));
        await listed(page);
        await page.waitForFunction(() => document.querySelector("#view-providers .pii"));
        assert.ok(!(await provText(page)).includes(MAIL), "Providers shows the account while Routing's switch is on");
        await says(page, true, "on Providers");

        // and Usage
        await page.evaluate(() => show("usage"));
        await page.locator("#usageMask").waitFor();
        await page.waitForFunction((m) => { const u = document.querySelector("#view-usage .subscription-account .user"); return u && u.querySelector(".pii") && !u.textContent.includes(m); }, MAIL);
        await says(page, true, "on Usage");

        // live in the other window and the panel
        await other.waitForFunction(() => document.querySelector("#view-providers .pii"));
        assert.ok(!(await provText(other)).includes(MAIL));
        await says(other, true, "in another window");
        await panel.waitForFunction((m) => !document.querySelector("#panelQuota").innerText.includes(m), MAIL);

        // turned off from Providers, in the other window: every page back
        await other.locator("#pvMask").click();
        await other.waitForFunction(() => !document.querySelector(".pii"));
        assert.ok((await provText(other)).includes(MAIL));
        await says(other, false, "after Providers' was turned off");
        await page.waitForFunction(() => !document.querySelector(".pii"));
        await says(page, false, "in the first window, after the other turned it off");
        await page.evaluate(() => show("routing"));
        assert.equal(await page.locator("#sample").textContent(), "Codex as " + MAIL);
        await panel.waitForFunction((m) => document.querySelector("#panelQuota").innerText.includes(m), MAIL);

        // turned on from Usage: Providers hidden again, after a reload too
        await page.evaluate(() => show("usage"));
        await page.locator("#usageMask").click();
        await page.waitForFunction(() => document.querySelector("#view-usage .pii"));
        await page.reload();
        await page.evaluate(() => show("providers"));
        await listed(page);
        await page.waitForFunction(() => document.querySelector("#view-providers .pii"));
        await says(page, true, "after a reload");
        assert.equal(await page.evaluate(() => localStorage.getItem("magpie.maskEmails")), "1");
        assert.equal(await page.evaluate(() => localStorage.getItem("magpie.maskProviders")), null);

        // placed as Routing's: in the page's head row, at its right end
        const box = await page.locator("#pvMask").boundingBox(), q = await page.locator("#pvQ").boundingBox();
        assert.ok(Math.abs((box.y + box.height / 2) - (q.y + q.height / 2)) < 4, `beside the search box: ${JSON.stringify([box, q])}`);
        assert.ok(box.x >= q.x + q.width - 1, `at the right end: ${JSON.stringify([box, q])}`);
        assert.ok(box.x + box.width <= width, "inside the window");

        assert.equal(await page.locator("select").count(), 0, "no native select");
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth), 0, `no sideways scroll at ${width}px`);
        const missing = await page.evaluate(([keys, lang]) => lang === "en" ? [] : keys.filter((k) => !I18N[lang]?.[k]), [KEYS, lang]);
        assert.deepEqual(missing, [], `every string has its ${lang}`);
        assert.deepEqual(errors, []);
      });

      // what an earlier version stored for Routing (maskEmails) and for
      // Providers (maskProviders): hidden on either is hidden everywhere
      for (const [stored, on] of [[[null, "1"], true], [["0", "1"], true], [["1", "0"], true], [["1", null], true], [["0", "0"], false], [["0", null], false]]) {
        test(`${engine} ${lang} ${width}: stored ${JSON.stringify(stored)} before the update is ${on ? "hidden" : "shown"} on every page`, async (t) => {
          const { open, errors } = await launch(t, stored);
          const page = await open("http://magpie.test/?view=providers");
          await listed(page);
          await page.locator("#pvMask").waitFor();
          if (on) await page.waitForFunction(() => document.querySelector("#view-providers .pii"));
          assert.equal(await page.locator("#view-providers .pii").count() > 0, on);
          assert.equal((await provText(page)).includes(MAIL), !on);
          await says(page, on, "on Providers");
          await page.evaluate(() => show("routing"));
          await says(page, on, "on Routing");
          assert.equal(await page.evaluate(() => localStorage.getItem("magpie.maskEmails")), on ? "1" : "0");
          assert.equal(await page.evaluate(() => localStorage.getItem("magpie.maskProviders")), null);
          await page.evaluate((v) => hideAccounts.follow(v), !on);
          await says(page, on, "after Mask personal data changed: a default never turns the user's choice");
          assert.deepEqual(errors, []);
        });
      }
    }
  }
}
