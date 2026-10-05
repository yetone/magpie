// Run with Node's test runner and Playwright on the module path; see README.md.
// PAMI on Discord: a price box took no decimals as typed (a number box
// drops what its locale doesn't take, "0,25" became 25), OpenAI bills
// gpt-6-astra over 272K input at another price, and Anthropic a 1-hour cache
// write at 2× input. The boxes are text ones that take "0.25" and "0,25"; a
// model's 1-hour cache write has a box of its own, empty for 2× input; and a
// quiet Long-context price row, shown where the list has one and behind a
// link where it hasn't, gives what the whole request costs over a size.
// English and Chinese.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const astra = { input: 10, output: 50, cache_read: 1, cache_write: 12.5, tiers: [{ above: 272000, input: 20, output: 75, cache_read: 2, cache_write: 25 }] };
const opus = { input: 5, output: 25, cache_read: 0.5, cache_write: 6.25, cache_write_1h: 10 };
const plain = { input: 1, output: 4, cache_read: 0.1, cache_write: 0 };

function serve(lang, posts) {
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  const models = [
    { id: "gpt-6-astra", name: "GPT-6 Astra", on: true, efforts: [], images: false, list: astra },
    { id: "claude-opus-5-5", name: "Claude Opus 5.5", on: true, efforts: [], images: false, list: opus },
    { id: "plain-1", name: "Plain 1", on: true, efforts: [], images: false, list: plain },
  ];
  const provider = { id: "relay", name: "Relay", icon: "generic", chat: "https://relay.example/v1", responses: "", anthropic: "", models, agents: [], key: { set: true, masked: "sk-…1234" }, ready: true };
  const providers = { providers: [provider], presets: [], excluded: [], gateway: { running: true, window: true } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname.startsWith("/api/provider/")) {
      posts.push({ path: url.pathname, body: route.request().postDataJSON() });
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    await route.fulfill({ body: await fs.readFile(file), contentType: { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)] });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: decimals, a 1-hour cache write and a long-context price`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 900, height: 900 }, reducedMotion: "reduce" })).newPage();
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-model-price-tiers.png`) });
        }
        await browser.close();
      });
      page.setDefaultTimeout(5000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=providers");
      const zh = lang === "zh";
      const L = { names: zh ? "名称与推理档位" : "Names & levels", input: zh ? "输入" : "Input", output: zh ? "输出" : "Output", cacheRead: zh ? "缓存读取" : "Cache read",
        oneHour: zh ? "1 小时缓存写入" : "Cache write 1h", long: zh ? "长上下文价格" : "Long-context price", over: zh ? "超过的输入 token 数" : "Over input tokens",
        save: zh ? "保存" : "Save", badSize: zh ? "长上下文价格要写从多少输入 token 起算，比如 272K" : "A long-context price starts over a number of input tokens, like 272K" };
      await page.locator('.row.provider[data-id="relay"]').click();
      if (!(await page.locator(".mnames:not([hidden])").count())) await page.getByRole("button", { name: L.names, exact: true }).click();
      const row = (id) => page.locator(".mname", { has: page.locator("code", { hasText: id }) });
      const base = (id) => row(id).locator(".mprice > .mpart input");
      const tier = (id) => row(id).locator(".mtier input");
      // Blurring the previous price normalizes its siblings before fill selects their text.
      const typeIn = async (box, v) => { await box.focus(); await box.fill(v); await box.press("Enter"); };
      const y = await page.evaluate(() => scrollY);

      // a list with a long-context price shows it in its quiet row, greyed
      assert(await row("gpt-6-astra").locator(".mtier").isVisible());
      assert(!(await row("gpt-6-astra").locator(".mtier-add").isVisible()));
      assert.deepEqual(await tier("gpt-6-astra").evaluateAll((is) => is.map((i) => [i.value, i.placeholder])), [["", "272K"], ["", "20"], ["", "75"], ["", "2"], ["", "25"]]);
      // the 1-hour box shows the list's, else 2× its input
      assert.deepEqual(await base("claude-opus-5-5").evaluateAll((is) => is.map((i) => i.placeholder)), ["5", "25", "0.5", "6.25", "10"]);
      // only a Claude model has a 1-hour box: gpt-6-astra has no such write
      // to price, so no 2× input one is shown for it (PAMI on Discord)
      assert(await row("claude-opus-5-5").getByRole("textbox", { name: L.oneHour, exact: true }).isVisible());
      assert(!(await row("gpt-6-astra").getByRole("textbox", { name: L.oneHour, exact: true }).isVisible()));
      assert(!(await row("plain-1").getByRole("textbox", { name: L.oneHour, exact: true }).isVisible()));
      // where there is none, the row waits behind a link
      assert(!(await row("plain-1").locator(".mtier").isVisible()));
      assert(await row("plain-1").locator(".mtier-add").isVisible());
      assert.equal(await row("plain-1").locator("input[type=number]").count(), 0, "no number boxes");

      // Closing the browser while typing has not fired change/blur yet.
      // Both new price kinds must protect the raw input, and reverting it is clean.
      const leaveAsked = () => page.evaluate(() => {
        const event = new Event("beforeunload", { cancelable: true });
        window.dispatchEvent(event);
        return event.defaultPrevented;
      });
      assert.equal(await leaveAsked(), false, "unchanged prices ask nothing");
      for (const [box, value] of [[base("claude-opus-5-5").last(), "7,5"], [tier("gpt-6-astra").first(), "350K"], [tier("gpt-6-astra").nth(1), "33"]]) {
        await box.fill(value);
        assert.equal(await leaveAsked(), true, "a focused price edit asks before leaving");
        await box.fill("");
        assert.equal(await leaveAsked(), false, "reverting the raw edit asks nothing");
      }
      assert.deepEqual(posts, [], "typing alone has saved nothing");

      // decimals, with a point and with a comma, each kept as typed
      await typeIn(row("gpt-6-astra").getByRole("textbox", { name: L.cacheRead, exact: true }), "0,25");
      await typeIn(row("gpt-6-astra").getByRole("textbox", { name: L.output, exact: true }), "1.5");
      assert.deepEqual(await base("gpt-6-astra").evaluateAll((is) => is.map((i) => i.value)), ["10", "1.5", "0.25", "12.5", ""]);
      // the 1-hour cache write, its own price
      await typeIn(row("claude-opus-5-5").getByRole("textbox", { name: L.oneHour, exact: true }), "7,5");
      assert.equal(await base("claude-opus-5-5").evaluateAll((is) => is.at(-1).value), "7.5");

      // a long-context price given where the list has none: a size that
      // isn't one is refused, then 200k and its input
      await row("plain-1").locator(".mtier-add").click();
      assert(await row("plain-1").locator(".mtier").isVisible());
      assert.equal(await page.evaluate(() => document.activeElement?.getAttribute("aria-label")), L.over);
      await typeIn(row("plain-1").getByRole("textbox", { name: L.over, exact: true }), "lots");
      await page.locator("#status", { hasText: L.badSize }).waitFor();
      // (Enter first: the size taken fills the price boxes from its own, and
      // in Chromium a fill that blurs the size box lands after that fill)
      await typeIn(row("plain-1").getByRole("textbox", { name: L.over, exact: true }), "200k");
      await typeIn(row("plain-1").getByRole("textbox", { name: L.long + " · " + L.input, exact: true }), "2,5");
      assert.deepEqual(await tier("plain-1").evaluateAll((is) => is.map((i) => i.value)), ["200K", "2.5", "4", "0.1", "0"]);
      assert.equal(await page.evaluate(() => scrollY), y, "nothing scrolled the page");
      assert.equal(await page.locator(".mname select").count(), 0);
      if (process.env.ARTIFACT_DIR) await page.locator(".mnames").screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-model-price-tier-rows.png`) });

      await page.getByRole("button", { name: L.save, exact: true }).click();
      await page.waitForFunction(() => !document.querySelector(".mnames"));
      assert.deepEqual(posts.map((p) => p.path), ["/api/provider/save"]);
      assert.deepEqual(posts[0].body.modelPrefs, {
        // its list's long-context price goes with the price set
        "gpt-6-astra": { price: { input: 10, output: 1.5, cache_read: 0.25, cache_write: 12.5, tiers: astra.tiers } },
        "claude-opus-5-5": { price: { input: 5, output: 25, cache_read: 0.5, cache_write: 6.25, cache_write_1h: 7.5 } },
        "plain-1": { price: { input: 1, output: 4, cache_read: 0.1, cache_write: 0, tiers: [{ above: 200000, input: 2.5, output: 4, cache_read: 0.1, cache_write: 0 }] } },
      });
      const missing = await page.evaluate(() => ["Cache write 1h", "Long-context price", "Long-context price, over", "Over input tokens",
        "A long-context price starts over a number of input tokens, like 272K", "A cache write kept for an hour, as Anthropic bills it; empty: 2× input",
        "What the whole request costs when its input — cached tokens and cache writes included — is over this many tokens, as OpenAI bills gpt-6-astra over 272K; empty: the list's",
        "{m} for 5 minutes, {h} for an hour"].filter((k) => !I18N.zh[k] || !I18N.ja[k] || !I18N.de[k]));
      assert.deepEqual(missing, []);
      // no left-border accents on the new row
      assert.deepEqual(await page.evaluate(() => [...document.querySelectorAll(".mtier, .mtier *")].filter((e) => parseFloat(getComputedStyle(e).borderLeftWidth) > 0 && getComputedStyle(e).borderLeftColor !== getComputedStyle(e).borderTopColor).length), 0);
      assert.deepEqual(errors, []);
    });
  }
}
