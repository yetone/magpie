// Run with Node's test runner and Playwright on the module path; see README.md.
// Privacy's Mask personal data hides the accounts on screen too (inaction on
// Discord: on another computer with it on, Usage still showed the email
// address, until Hide accounts was found and turned on as well). Until Hide
// accounts is chosen on this computer it follows Mask personal data;
// turning Mask personal data on turns it on; and Privacy has the Hide
// accounts switch itself. A choice made with the switch is kept over it.
// Mask personal data's description says so. Every language; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const quotas = () => [
  { provider: "codex", name: "Codex", icon: "openai", plan: "Plus", user: "dev.one@example.com", windows: [{ name: "5 hours", used: 20 }] },
];

function serve(lang, settings, saved) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") {
      if (req.method() === "POST") { const b = req.postDataJSON(); saved.push(b); Object.assign(settings, b); }
      return json(settings);
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/usage/quotas") return json(quotas());
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

// sub: Mask personal data's description, which says it turns Hide accounts on too
const words = {
  en: { personal: "Mask personal data", hide: "Hide accounts", on: "On", off: "Off",
    sub: "Emails, phone numbers, ID and bank card numbers too. Turning it on turns on Hide accounts as well" },
  zh: { personal: "脱敏个人信息", hide: "账号打码", on: "开启", off: "关闭", sub: "邮箱、手机号、身份证号和银行卡号也一并脱敏。打开时会一并打开「账号打码」" },
  "zh-TW": { personal: "脫敏個人資訊", hide: "帳號打碼", on: "開啟", off: "關閉", sub: "郵箱、手機號、身份證號和銀行卡號也一併脫敏。開啟時會一併開啟「帳號打碼」" },
  ja: { personal: "個人情報をマスク", hide: "アカウントを隠す", on: "オン", off: "オフ",
    sub: "メールアドレス、電話番号、ID 番号、銀行カード番号もマスクします。オンにすると「アカウントを隠す」もオンになります" },
  de: { personal: "Personenbezogene Daten maskieren", hide: "Konten verbergen", on: "An", off: "Aus",
    sub: "Auch E-Mail-Adressen, Telefonnummern, Ausweis- und Bankkartennummern. Eingeschaltet schaltet es auch „Konten verbergen“ ein" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of (process.env.LANGS ? process.env.LANGS.split(",") : Object.keys(words))) {
    const w = words[lang];
    test(`${engine} ${lang}: masking personal data hides the accounts on screen`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const errors = [];
      const open = async (settings, stored, url = "http://magpie.test/?view=usage", saved = []) => {
        const context = await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" });
        await context.addInitScript((v) => { try { if (v === null) localStorage.removeItem("magpie.maskEmails"); else localStorage.setItem("magpie.maskEmails", v); } catch {} }, stored);
        const page = await context.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, settings, saved));
        await page.goto(url);
        return page;
      };
      const base = () => ({ theme: "light", lang, tray: "panel", currency: "usd" });
      const hidden = (page) => page.evaluate(() => {
        const u = document.querySelector(".subscription-account .user");
        return { pii: !!u.querySelector(".pii"), text: u.textContent, pressed: document.querySelector("#usageMask").getAttribute("aria-pressed") };
      });

      // never chosen here, personal data masked: the address is hidden
      let page = await open({ ...base(), redactPersonal: true }, null);
      await page.waitForSelector(".subscription-account .user .pii");
      let h = await hidden(page);
      assert.ok(!h.text.includes("dev.one@example.com"), h.text);
      assert.equal(h.pressed, "true");
      await page.context().close();

      // never chosen, personal data not masked: it shows, as before
      page = await open({ ...base(), redactPersonal: false }, null);
      await page.waitForSelector(".subscription-account .user");
      h = await hidden(page);
      assert.equal(h.text, "dev.one@example.com");
      assert.equal(h.pressed, "false");
      await page.context().close();

      // shown on purpose with the button: that choice is kept
      page = await open({ ...base(), redactPersonal: true }, "0");
      await page.waitForSelector(".subscription-account .user");
      h = await hidden(page);
      assert.equal(h.text, "dev.one@example.com");
      await page.context().close();

      // Privacy: the Hide accounts switch is there, and turning Mask
      // personal data on turns it on
      const settings = { ...base(), redactPersonal: false }, saved = [];
      page = await open(settings, null, "http://magpie.test/?view=settings&tab=privacy", saved);
      const row = (name) => page.locator("#setPage-privacy .list .row", { has: page.locator(".name", { hasText: new RegExp("^" + name + "$") }) });
      await row(w.hide).waitFor();
      // it says it turns Hide accounts on, before it is clicked
      assert.equal((await row(w.personal).locator(".sub").textContent()).trim(), w.sub);
      const top = () => page.evaluate(() => document.querySelector("#view-settings").scrollTop);
      const y0 = await top();
      await row(w.personal).getByRole("button", { name: w.on, exact: true }).click();
      await page.waitForFunction(() => localStorage.getItem("magpie.maskEmails") === "1");
      await page.waitForFunction(() => document.querySelector("#usageMask")?.getAttribute("aria-pressed") === "true");
      assert.ok(saved.some((b) => b.redactPersonal === true), JSON.stringify(saved));
      // the switch here says so, and turns it off again
      await page.waitForFunction((on) => [...document.querySelectorAll("#setPage-privacy .list .row")].some((r) => r.querySelector(".name")?.textContent === on.hide && r.querySelector(".opt.on")?.textContent === on.on), w);
      assert.equal(await top(), y0, "a click never scrolls");
      // since Settings' search box (51042630e) the row sits under the
      // window's foot at this size, and the reader scrolls to it with the
      // wheel. Playwright's own scroll into view is code, which the click
      // guard puts back; WebKit's click came after that and hit the foot
      // (#1632)
      const off = row(w.hide).getByRole("button", { name: w.off, exact: true });
      const under = () => off.evaluate((b) => b.getBoundingClientRect().bottom > document.querySelector("footer.foot").getBoundingClientRect().top - 4);
      await page.mouse.move(450, 300);
      for (let i = 0; i < 30 && (await under()); i++) { await page.mouse.wheel(0, 60); await page.waitForTimeout(30); }
      assert.equal(await under(), false, "the wheel brings the row into sight");
      // a wheel step can still be landing (Chromium scrolls off the main
      // thread): the place the click is measured from is where it settles
      await page.evaluate(() => new Promise((done) => {
        const v = document.querySelector("#view-settings");
        let last = v.scrollTop, still = 0;
        const look = () => { if (v.scrollTop === last) still++; else { last = v.scrollTop; still = 0; } if (still >= 10) done(); else requestAnimationFrame(look); };
        requestAnimationFrame(look);
      }));
      const y1 = await top();
      await off.click();
      await page.waitForFunction(() => localStorage.getItem("magpie.maskEmails") === "0");
      assert.equal(await top(), y1, "a click never scrolls");
      // no coloured left border on the new row
      assert.equal(await row(w.hide).evaluate((r) => getComputedStyle(r).borderLeftWidth), "0px");
      await page.context().close();
      assert.deepEqual(errors, []);
    });
  }
}
