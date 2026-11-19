// Run with Node's test runner and Playwright on the module path; see README.md.
// Many keys at once (361 on Discord: 密钥比较多的话，需要批量导入的话就不方便了):
// a key provider's Add another key has Paste several, a box that takes keys
// one a line or separated by commas, counts them, and posts keys/import;
// pasting several into the one key's field opens it with them. The note
// says how many were added and how many it had. A key the gateway rests
// says why and until when on its row. Adding one key, which the gateway
// checks with the provider and can take seconds, shows a spinner and
// Adding… on its button until it is done, takes Enter once, and gives the
// label back when the key is refused; the name field is the narrower one.
// The page doesn't scroll. In English and Chinese.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const until = new Date(Date.now() + 10 * 60e3).toISOString();
const relay = (said) => ({
  id: "relay", name: "Relay", icon: "generic", chat: "https://relay.example.com/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "big", name: "", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" }, balanceToken: { takes: false, set: false }, proxy: "",
  keyList: [
    { id: "aaaaaaaaaa", name: "", masked: "sk-…one", on: true, active: true },
    { id: "bbbbbbbbbb", name: "", masked: "sk-…two", on: true, rest: { why: "rate", status: 429, until, key: "relay#bbbbbbbbbb", ...(said ? { error: said } : {}) } },
  ],
});

function serve(lang, posts, adds = [], said) {
  const p = relay(said);
  const providers = { providers: [p], presets: [], excluded: [], gateway: { running: true, window: true } };
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
    if (url.pathname === "/api/keys/import" && route.request().method() === "POST") {
      const body = route.request().postDataJSON();
      posts.push(body);
      p.keyList = [...p.keyList, { id: "cccccccccc", name: "", masked: "sk-…new", on: true }, { id: "dddddddddd", name: "", masked: "sk-…nw2", on: true }];
      return json({ ...providers, added: 2, had: 1 });
    }
    // keys/add answers when the test lets it: each call waits on the next
    // entry of adds, a promise giving true to add the key or false to refuse
    if (url.pathname === "/api/keys/add" && route.request().method() === "POST") {
      posts.push(route.request().postDataJSON());
      if (!await adds.shift()) return route.fulfill({ status: 400, json: { error: "the provider refused this key" } });
      p.keyList = [...p.keyList, { id: "eeeeeeeeee", name: "", masked: "sk-…add", on: true }];
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { another: "Add another key", several: "Paste several", add: "Add", adding: "Adding…", three: "3 keys", note: "2 keys added, 1 already there", rest: /Resting · 429 rate limited · back at/ },
  zh: { another: "添加另一个密钥", several: "批量粘贴", add: "添加", adding: "添加中…", three: "3 个密钥", note: "已添加 2 个密钥，1 个已存在", rest: /暂停使用 · 429 限流 · .* 恢复/ },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    const open = async (t, name, adds, said) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${name}-key-import.png`) });
        }
        await browser.close();
      });
      const page = await (await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" })).newPage();
      // room for a busy machine: one click took over 5s at load 190
      page.setDefaultTimeout(15000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const posts = [];
      await page.route("**/*", serve(lang, posts, adds, said));
      await page.goto("http://magpie.test/?view=providers");
      await page.locator(".row.provider", { hasText: "Relay" }).first().click();
      await page.locator(".editor .accts .acc.add").waitFor();
      return { page, errors, posts };
    };
    // got waits until n keys/add posts have arrived: a busy machine can take
    // seconds to send the first
    const got = async (posts, n) => {
      for (let i = 0; i < 300 && posts.length < n; i++) await new Promise((r) => setTimeout(r, 50));
      assert.equal(posts.length, n);
    };
    const later = () => { let done; const p = new Promise((r) => { done = r; }); return { p, done }; };
    const scrolled = (page) => page.evaluate(() => [window.scrollY, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop > 0).map((e) => `${e.className}:${e.scrollTop}`)].join(" "));
    const posted = async (posts) => {
      for (let i = 0; i < 60 && !posts.length; i++) await new Promise((r) => setTimeout(r, 50));
      return posts.at(-1);
    };

    test(`${engine} ${lang}: Paste several takes many keys at once`, async (t) => {
      const { page, errors, posts } = await open(t, "box");
      // a resting key says why and until when
      assert.match(await page.locator('.acc[data-account-id="bbbbbbbbbb"] .key-rest').textContent(), w.rest);
      assert.equal(await page.locator('.acc[data-account-id="aaaaaaaaaa"] .key-rest').count(), 0);
      await page.getByRole("button", { name: w.another }).click();
      const sc = await scrolled(page);
      await page.locator(".acc.adding").getByRole("button", { name: w.several }).click();
      const box = page.locator(".acc.adding.bulk textarea");
      await box.waitFor();
      assert.equal(await scrolled(page), sc, "a click scrolled");
      await box.fill("sk-one\nsk-two, sk-three\n\nsk-two");
      assert.equal((await page.locator(".bulk-count").textContent()).trim(), w.three);
      await page.locator(".acc.adding.bulk").getByRole("button", { name: w.add, exact: true }).click();
      const body = await posted(posts);
      assert.equal(body.id, "relay");
      assert.equal(body.key, "sk-one\nsk-two, sk-three\n\nsk-two");
      await page.locator('.acc[data-account-id="dddddddddd"]').waitFor();
      assert.equal(await page.locator(".acc.adding").count(), 0, "the box stays open");
      assert.equal((await page.locator("#status").textContent()).trim(), w.note);
      assert.deepEqual(errors, []);
    });

    // @YsiCing on X: a key rested "400 · out of credit" said nothing of what
    // its vendor answered; the row says it, as an account's does
    test(`${engine} ${lang}: a resting key says what its vendor answered`, async (t) => {
      const said = "Rate limit reached for gpt-x on requests per min (RPM)";
      const { page, errors } = await open(t, "said", [], said);
      assert.match(await page.locator('.acc[data-account-id="bbbbbbbbbb"] .key-rest').textContent(), w.rest);
      assert.equal(await page.locator('.acc[data-account-id="bbbbbbbbbb"] .key-rest-said').textContent(), said);
      assert.equal(await page.locator('.acc[data-account-id="aaaaaaaaaa"] .key-rest-said').count(), 0);
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: several keys pasted into the key field open the box with them`, async (t) => {
      const { page, errors } = await open(t, "paste");
      await page.getByRole("button", { name: w.another }).click();
      const field = page.locator(".acc.adding input[type=password]");
      await field.waitFor();
      await field.evaluate((i) => {
        const dt = new DataTransfer();
        dt.setData("text/plain", "sk-a\nsk-b\nsk-c");
        i.dispatchEvent(new ClipboardEvent("paste", { clipboardData: dt, bubbles: true, cancelable: true }));
      });
      const box = page.locator(".acc.adding.bulk textarea");
      await box.waitFor();
      assert.equal(await box.inputValue(), "sk-a\nsk-b\nsk-c");
      assert.equal((await page.locator(".bulk-count").textContent()).trim(), w.three);
      // one key pasted stays in the field
      await page.locator(".acc.adding.bulk").getByRole("button", { name: lang === "en" ? "Add one key" : "添加单个密钥" }).click();
      const one = page.locator(".acc.adding input[type=password]");
      await one.waitFor();
      await one.evaluate((i) => {
        const dt = new DataTransfer();
        dt.setData("text/plain", "sk-only");
        i.dispatchEvent(new ClipboardEvent("paste", { clipboardData: dt, bubbles: true, cancelable: true }));
      });
      assert.equal(await page.locator(".acc.adding.bulk").count(), 0);
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: adding one key shows it is under way`, async (t) => {
      const refused = later(), taken = later();
      const { page, errors, posts } = await open(t, "busy", [refused.p, taken.p]);
      await page.getByRole("button", { name: w.another }).click();
      const row = page.locator(".acc.adding");
      const name = row.locator(".kf input").first(), key = row.locator("input[type=password]");
      await key.waitFor();
      const [nw, kw] = [await name.evaluate((e) => e.getBoundingClientRect().width), await key.evaluate((e) => e.getBoundingClientRect().width)];
      assert.ok(nw * 1.8 < kw, `the name field (${nw}px) is not the narrower one beside the key (${kw}px)`);
      await key.fill("sk-bad");
      const add = row.locator(".kb button.primary");
      await add.click();
      await got(posts, 1);
      // held: a spinner and Adding…, and a second Enter posts nothing
      assert.equal((await add.textContent()).trim(), w.adding);
      assert.equal(await add.getAttribute("aria-busy"), "true");
      assert.equal(await add.locator("svg").count(), 1);
      await key.press("Enter");
      await page.waitForTimeout(100);
      assert.equal(posts.length, 1, "Enter posted the key again while it was being added");
      // refused: the label comes back and the row stays to be fixed
      refused.done(false);
      await page.waitForFunction(() => document.querySelector(".acc.adding .kb button.primary")?.getAttribute("aria-busy") === null);
      assert.equal((await add.textContent()).trim(), w.add);
      assert.equal(await add.locator("svg").count(), 0);
      assert.equal(await row.count(), 1);
      await key.fill("sk-good");
      await add.click();
      await got(posts, 2);
      assert.equal((await add.textContent()).trim(), w.adding);
      taken.done(true);
      await page.locator('.acc[data-account-id="eeeeeeeeee"]').waitFor();
      assert.equal(await page.locator(".acc.adding").count(), 0);
      assert.deepEqual(posts.map((b) => b.key), ["sk-bad", "sk-good"]);
      assert.deepEqual(errors, []);
    });
  }
}
