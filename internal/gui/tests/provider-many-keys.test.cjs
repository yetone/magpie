// Run with Node's test runner and Playwright on the module path; see README.md.
// Hundreds of keys on one provider (361 on Discord: 导入了几百个 key，账号列表
// 铺满整屏): the Accounts list shows the first five and All 300 keys; opened,
// a filter, a page of 50 rows and how many more, and removing many at once
// (those turned off, those failing for good, those the filter matches) after
// an in-app confirmation. Moving a shown key sends every key's place. Several keys
// pasted into a provider's API key field are kept and counted. No click
// scrolls, there is no <select>, nothing has a left border. In English and
// Chinese.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const until = new Date(Date.now() + 10 * 60e3).toISOString();
const N = 300;
const idOf = (i) => i.toString(16).padStart(10, "0");
const relay = () => ({
  id: "relay", name: "Relay", icon: "generic", chat: "https://relay.example.com/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "big", name: "", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…0000" }, balanceToken: { takes: false, set: false }, proxy: "",
  keyList: Array.from({ length: N }, (_, i) => ({
    id: idOf(i), name: i === 7 ? "Team" : "", masked: "sk-…" + String(i).padStart(4, "0"),
    on: i % 50 !== 49, // six turned off
    active: i === 0,
    // three whose sign-in the vendor refuses, one only rate limited
    ...(i === 10 || i === 20 || i === 30 ? { rest: { why: "auth", status: 401, until, key: "relay#" + idOf(i) } } : {}),
    ...(i === 40 ? { rest: { why: "rate", status: 429, until, key: "relay#" + idOf(i) } } : {}),
  })),
});
const solo = () => ({
  id: "solo", name: "Solo", icon: "generic", chat: "https://solo.example.com/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "small", name: "", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…solo" }, balanceToken: { takes: false, set: false }, proxy: "",
  keyList: [{ id: "5050505050", name: "", masked: "sk-…solo", on: true, active: true }],
});

function serve(lang, posts) {
  const providers = { providers: [relay(), solo()], presets: [], excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/") && route.request().method() === "POST") {
      const body = route.request().postDataJSON();
      posts.push({ path: url.pathname, body });
      if (url.pathname === "/api/keys/remove-many") {
        const gone = new Set(body.refs);
        const p = providers.providers[0];
        p.keyList = p.keyList.filter((k) => !gone.has(k.id));
        return json({ ...providers, removed: gone.size });
      }
      return json(url.pathname === "/api/provider/arrange" ? providers : {});
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { all: "All 300 keys", fewer: "Show fewer", more: "250 more — type to narrow them down", failing: "Remove 3 failing", off: "Remove 6 turned off", matching: /^Remove the \d+ matching$/, count: "3 keys", of: "1 of 300 keys" },
  zh: { all: "全部 300 个密钥", fewer: "收起", more: "还有 250 个，输入关键词缩小范围", failing: "移除 3 个失效的", off: "移除 6 个已停用的", matching: /^移除筛出的 \d+ 个$/, count: "3 个密钥", of: "300 个密钥中的 1 个" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    const open = async (t, name, provider = "Relay") => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${name}-many-keys.png`) });
        }
        await browser.close();
      });
      const page = await (await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const posts = [];
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=providers");
      await page.locator(".row.provider", { hasText: provider }).first().click();
      await page.locator(".editor").waitFor();
      return { page, errors, posts };
    };
    const rows = (page) => page.locator(".editor .accts .acc[data-account-id]");
    const scrolled = (page) => page.evaluate(() => [window.scrollY, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop > 0).map((e) => `${e.className}:${e.scrollTop}`)].join(" "));
    const plain = (page) => page.evaluate(() => ({
      selects: document.querySelectorAll("select").length,
      leftBorders: [...document.querySelectorAll(".editor .accts, .editor .accts *")]
        .filter((e) => { const s = getComputedStyle(e); return parseFloat(s.borderLeftWidth) > 0 && s.borderLeftStyle !== "none" && s.borderLeftWidth !== s.borderRightWidth; })
        .map((e) => e.className),
    }));
    const posted = async (posts, n) => {
      for (let i = 0; i < 60 && posts.length < n; i++) await new Promise((r) => setTimeout(r, 50));
      return posts.at(-1);
    };

    test(`${engine} ${lang}: 300 keys fold, open on a filter, and page`, async (t) => {
      const { page, errors } = await open(t, "fold");
      await page.locator(".editor .accts .keys-more").waitFor();
      assert.equal(await rows(page).count(), 5, "a folded list draws the first five");
      assert.equal(await rows(page).first().getAttribute("data-account-id"), idOf(0));
      assert.equal((await page.locator(".keys-more .n").textContent()).trim(), w.all);
      assert.deepEqual(await plain(page), { selects: 0, leftBorders: [] });
      const sc = await scrolled(page);
      await page.locator(".keys-more").click();
      const filter = page.locator(".keys-tools .keys-filter");
      await filter.waitFor();
      assert.equal(await filter.getAttribute("aria-label"), await filter.getAttribute("placeholder"));
      assert.equal(await scrolled(page), sc, "opening scrolled");
      assert.ok(await filter.evaluate((i) => document.activeElement === i), "the filter has the focus");
      assert.equal(await rows(page).count(), 50, "opened, a page of 50 rows");
      assert.equal((await page.locator(".accts .keys-rest").textContent()).trim(), w.more);
      assert.deepEqual(await plain(page), { selects: 0, leftBorders: [] });
      // the filter finds a key by its masked end and by its name
      await filter.pressSequentially("0123");
      await page.waitForFunction(() => document.querySelectorAll(".editor .accts .acc[data-account-id]").length === 1);
      assert.equal(await rows(page).first().getAttribute("data-account-id"), idOf(123));
      assert.equal((await page.locator(".keys-count").textContent()).trim(), w.of);
      assert.ok(await page.locator(".keys-filter").evaluate((i) => document.activeElement === i && i.value === "0123"), "typing keeps the focus");
      await page.locator(".keys-filter").fill("team");
      await page.locator(".keys-filter").dispatchEvent("input");
      await page.waitForFunction(() => document.querySelectorAll(".editor .accts .acc[data-account-id]").length === 1);
      assert.equal(await rows(page).first().getAttribute("data-account-id"), idOf(7));
      await page.getByRole("button", { name: w.fewer }).click();
      await page.locator(".keys-more").waitFor();
      assert.equal(await rows(page).count(), 5);
      assert.equal(await scrolled(page), sc, "a click scrolled");
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: bulk key removal waits for confirmation across editor redraws`, async (t) => {
      const { page, errors, posts } = await open(t, "remove");
      await page.locator(".keys-more").click();
      await page.locator(".keys-tools").waitFor();
      assert.equal(await page.getByRole("button", { name: w.off }).count(), 1);
      // nothing typed, nothing to remove by the filter
      assert.equal(await page.locator(".keys-tools .keys-remove").filter({ hasText: w.matching }).count(), 0);
      const sc = await scrolled(page);
      const failing = page.getByRole("button", { name: w.failing });
      const ask = page.locator("dialog.action-confirm[open]");
      await failing.click();
      await ask.waitFor();
      assert.match(await ask.locator("h2").textContent(), /sk-…0010, sk-…0020, sk-…0030/);
      assert.equal(posts.length, 0, "the first click only asks");
      await page.evaluate(() => renderProviders());
      assert.equal(await ask.evaluate((d) => d.inert), false, "redrawing leaves the confirmation interactive");
      await ask.locator("button").first().click();
      assert.equal(posts.length, 0, "Cancel removes nothing");
      assert.match(await page.locator(".keys-count").textContent(), /300/);
      await failing.click();
      await ask.locator("button").last().click();
      const last = await posted(posts, 1);
      assert.equal(last.path, "/api/keys/remove-many");
      assert.deepEqual(last.body, { id: "relay", refs: [idOf(10), idOf(20), idOf(30)] });
      await page.waitForFunction(() => /297/.test(document.querySelector(".keys-count")?.textContent || ""));
      assert.equal(await page.getByRole("button", { name: w.failing }).count(), 0);
      assert.equal(await scrolled(page), sc, "a click scrolled");
      // a typed filter offers removing what it matches
      await page.locator(".keys-filter").fill("sk-…004");
      await page.locator(".keys-filter").dispatchEvent("input");
      const matching = page.locator(".keys-tools .keys-remove").filter({ hasText: w.matching });
      await matching.waitFor();
      await matching.click();
      await ask.waitFor();
      await page.keyboard.press("Escape");
      assert.equal(posts.length, 1, "Escape keeps matching keys");
      await matching.click();
      await ask.locator("button").last().click();
      assert.deepEqual((await posted(posts, 2)).body, { id: "relay", refs: Array.from({ length: 10 }, (_, i) => idOf(40 + i)) });
      await page.waitForFunction(() => /287/.test(document.querySelector(".keys-count")?.textContent || ""));
      await page.locator(".keys-filter").fill("");
      const off = page.locator(".keys-tools .keys-remove").first();
      await off.click();
      await ask.waitFor();
      assert.equal(posts.length, 2, "turned-off keys also wait for confirmation");
      await ask.locator("button").last().click();
      assert.deepEqual((await posted(posts, 3)).body, { id: "relay", refs: [99, 149, 199, 249, 299].map(idOf) });
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: moving a shown key keeps the folded ones in place`, async (t) => {
      const { page, errors, posts } = await open(t, "arrange");
      await page.locator(".keys-more").waitFor();
      const second = rows(page).nth(1);
      await second.focus();
      await second.press("Alt+ArrowUp");
      const last = await posted(posts, 1);
      assert.equal(last.path, "/api/provider/arrange");
      const want = Array.from({ length: N }, (_, i) => idOf(i));
      [want[0], want[1]] = [want[1], want[0]];
      assert.deepEqual(last.body.accountOrder, want, "every key is sent, the folded ones where they were");
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: several keys pasted into the API key field are counted`, async (t) => {
      const { page, errors } = await open(t, "field", "Solo");
      const field = page.locator(".editor .pair input[type=password]").first();
      await field.waitFor();
      await field.evaluate((i) => {
        const dt = new DataTransfer();
        dt.setData("text/plain", "sk-a\nsk-b\n\nsk-c\nsk-b");
        i.dispatchEvent(new ClipboardEvent("paste", { clipboardData: dt, bubbles: true, cancelable: true }));
      });
      assert.equal(await field.inputValue(), "sk-a, sk-b, sk-c");
      assert.equal((await page.locator(".editor .key-count").textContent()).trim(), w.count);
      assert.deepEqual(await plain(page), { selects: 0, leftBorders: [] });
      assert.deepEqual(errors, []);
    });
  }
}
