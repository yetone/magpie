// Run with Node's test runner and Playwright on the module path; see README.md.
// A subscription's accounts by weight (rubbzha on X: a Max account and a
// Pro one, shared by how much more one allows). The provider editor's
// Routing offers By weight for accounts as it does for keys, and with it
// picked each account in use shows its weight and its share beside its
// name, as a key does; a click edits it in place and posts
// provider/accountweight. Hidden under the other routings. Each line stays
// one line, with nothing past the sheet's edge, at the window's narrowest
// and in a phone's browser, in every language, without the page scrolling.
// MAGPIE_ACC_ASSETS runs it on older assets.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ACC_ASSETS || path.resolve(__dirname, "../assets"));
const claude = () => ({
  id: "claude", name: "Claude Code", icon: "claude-color", chat: "", responses: "", anthropic: "", catalog: "", routing: "",
  models: [{ id: "claude-opus-5-5", name: "Claude Opus 5.5", on: true }], agents: [], fallback: [], headers: {}, keyList: [],
  account: {
    agent: "claude", agentName: "Claude Code", user: "max@example.com", plan: "max",
    logins: [
      { user: "max@example.com", plan: "max", active: true, on: true },
      { user: "pro@example.com", plan: "pro", on: true },
      { user: "off@example.com", plan: "pro", on: false },
    ],
  },
  accountWeights: { "max@example.com": 3 },
});

function serve(lang, web, posts) {
  const p = claude();
  const providers = { providers: [p], presets: [], excluded: [], gateway: { running: true, window: true } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:${web}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/login/usage") return json({
      "max@example.com": { windows: [{ label: "5h", used: 10, limit: 100 }] },
      "pro@example.com": { windows: [{ label: "5h", used: 30, limit: 100 }] },
    });
    if (url.pathname === "/api/provider/accountweight" && route.request().method() === "POST") {
      const body = route.request().postDataJSON();
      posts.push(body);
      p.accountWeights = { ...p.accountWeights, [body.account.toLowerCase()]: body.weight };
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

// the app's own words, as t() gives them
const say = (page, lang, key, vars = {}) => page.evaluate(([lang, key, vars]) => ((lang !== "en" && I18N[lang]?.[key]) || key).replace(/\{(\w+)\}/g, (_, k) => vars[k]), [lang, key, vars]);

const langs = ["en", "zh", "zh-TW", "ja", "de"];
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: By weight shows each account's weight and share, and a click sets it`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const [web, width] of [[false, 1000], [false, 560], [true, 390]]) for (const lang of langs) {
      const at = `${lang} ${web ? "web " : ""}${width}`;
      const context = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const posts = [];
      await page.route("**/*", serve(lang, web, posts));
      await page.goto("http://magpie.test/?view=providers");
      await page.locator(".row.provider", { hasText: "Claude Code" }).first().click();
      await page.locator('.editor .acc[data-account-id="off@example.com"]').waitFor();
      const chip = (id) => page.locator(`.editor .acc[data-account-id="${id}"] .key-weight`);
      const scrolled = () => page.evaluate(() => [window.scrollY, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop > 0).map((e) => `${e.className}:${e.scrollTop}`)].join(" "));

      // Smart: no weights shown
      assert.equal(await chip("max@example.com").isVisible(), false, `${at}: a weight under Smart`);
      // Routing is under Advanced
      { const adv = page.locator(".editor details.advanced"); await adv.waitFor({ state: "attached" }); if (!(await adv.evaluate((d) => d.open))) await adv.locator(":scope > summary").click(); }
      const by = page.locator(".editor").getByRole("button", { name: await say(page, lang, "By weight"), exact: true });
      await by.scrollIntoViewIfNeeded();
      // the weights it shows sit above it, in the accounts, so the editor
      // holds it where it was clicked rather than keep its scroll
      const top = () => by.evaluate((b) => Math.round(b.getBoundingClientRect().top));
      const was = await top();
      await by.click();
      await chip("max@example.com").waitFor();
      assert(Math.abs((await top()) - was) <= 1, `${at}: By weight moved from ${was} to ${await top()}`);
      // the routing's hint names accounts too
      await page.locator(".editor .hint", { hasText: (await say(page, lang, "Requests spread over the keys or accounts by the weight set beside each: one weighing 3 takes three requests for every one weighing 1 takes, evenly over a few requests. One that fails is passed over while it rests, and the others share its requests; a conversation stays with it as Stays says.")).slice(0, 20) }).first().waitFor();
      assert.equal((await chip("max@example.com").textContent()).trim(), await say(page, lang, "Weight {n} · {share}%", { n: 3, share: 75 }), at);
      assert.equal((await chip("pro@example.com").textContent()).trim(), await say(page, lang, "Weight {n} · {share}%", { n: 1, share: 25 }), at);
      assert.equal(await chip("off@example.com").count(), 0, `${at}: an account not in use has no share`);

      // each line one line, nothing past its row
      const fit = await page.evaluate(() => [...document.querySelectorAll(".editor .accts .acc[data-account-id]")].map((r) => {
        const line = r.querySelector(".acc-line"), c = r.querySelector(".key-weight");
        return { who: r.dataset.accountId, h: Math.round(line.getBoundingClientRect().height), over: line.scrollWidth - line.clientWidth, out: c ? Math.round(c.getBoundingClientRect().right - r.getBoundingClientRect().right) : -1 };
      }));
      for (const f of fit) {
        assert.ok(f.h <= (web ? 48 : 24), `${at}: ${f.who}'s line is one line (${f.h}px)`);
        assert.ok(f.over <= 1 && f.out <= 0, `${at}: ${f.who}'s line runs past its row (${f.over}, ${f.out})`);
      }
      if (process.env.ARTIFACT_DIR && lang === "en") {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        await page.locator(".editor .accts").screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${web ? "web-" : ""}${width}-account-weight.png`) });
      }

      // a click edits it in place, Enter saves it; the chip is brought into
      // view first (By weight can be a screen below it on a phone), as the
      // click would, so what is measured is the click's own scrolling
      await chip("pro@example.com").scrollIntoViewIfNeeded();
      const atChip = await scrolled();
      await chip("pro@example.com").click();
      const box = page.locator('.editor .acc[data-account-id="pro@example.com"] .key-weight-in');
      await box.waitFor();
      assert.equal(await box.getAttribute("aria-label"), await say(page, lang, "Account weight"), at);
      assert.equal(await scrolled(), atChip, `${at}: a click scrolled`);
      await box.fill("2");
      await box.press("Enter");
      for (let i = 0; i < 60 && !posts.length; i++) await new Promise((r) => setTimeout(r, 50));
      assert.deepEqual(posts.at(-1), { id: "claude", account: "pro@example.com", weight: 2 }, at);
      await chip("pro@example.com").filter({ hasText: await say(page, lang, "Weight {n} · {share}%", { n: 2, share: 40 }) }).waitFor();
      assert.equal((await chip("max@example.com").textContent()).trim(), await say(page, lang, "Weight {n} · {share}%", { n: 3, share: 60 }), at);
      // said once state is read again, after the chips are drawn: wait for it
      const note = await say(page, lang, "{key} weighs {n}", { key: "pro@example.com", n: 2 });
      await page.waitForFunction((n) => document.querySelector("#status")?.textContent.trim() == n, note).catch(() => {});
      assert.equal((await page.locator("#status").textContent()).trim(), note, at);

      // back to Smart hides them
      await page.locator(".editor").getByRole("button", { name: await say(page, lang, "Smart"), exact: true }).first().click();
      assert.equal(await chip("max@example.com").isVisible(), false, `${at}: a weight under Smart`);
      // open, the editor scrolls and the dialog around it doesn't: the lines
      // its check-in and warm-up rows keep out of sight were held against
      // the dialog and grew it a second scrollbar, every row 8px narrower
      const dialog = await page.evaluate(() => { const d = document.querySelector(".dialog"); return [d.scrollHeight, d.clientHeight]; });
      assert.ok(dialog[0] <= dialog[1] + 1, `${at}: the dialog scrolls beside the editor (${dialog})`);
      assert.deepEqual(errors, [], at);
      await context.close();
    }
  });
}
