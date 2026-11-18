// Run with Node's test runner and Playwright on the module path; see README.md.
// Each key or account can have its own limit on requests at once (#892,
// margbug01), over the provider's Concurrency, counted across every model,
// routing group and agent. Its row has a pill: what runs and waits under
// it while requests are out (the gateway's lanes, read again every two
// seconds and written into the pill in place), else its limit; a row with
// none says so under the account's ⋯ only. A click opens the app's own menu (no native
// select): the provider's, no limit, a number, or Other… typed in place,
// each posted to provider/accountconcurrency. The editor has the queue's
// length and wait under Concurrency, saved with it and refused when not a
// whole number in range. A click moves nothing. In English and Chinese.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const fresh = () => ({
  id: "relay", name: "Relay", icon: "generic", chat: "https://relay.example.com/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "model-a", name: "Model A", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" },
  keyList: [
    { id: "k1", name: "Main", masked: "sk-…one", on: true },
    { id: "k2", name: "Spare", masked: "sk-…two", on: true },
  ],
  balanceToken: { takes: false, set: false }, proxy: "",
  maxConcurrency: 2, accountConcurrency: { k1: 1 }, queueLimit: 3, queueWait: 30,
});
const codex = () => ({
  id: "codex", name: "Codex", icon: "codex-color", chat: "", responses: "", anthropic: "", catalog: "",
  models: [{ id: "gpt-6", name: "GPT-6", on: true }], agents: [], fallback: [], headers: {}, keyList: [],
  account: { agent: "codex", agentName: "Codex", user: "me@example.com", plan: "PLUS", logins: [
    { user: "me@example.com", plan: "PLUS", active: true, on: true },
    { user: "Spare@Example.com", plan: "PLUS", on: true },
  ] },
  proxy: "", maxConcurrency: null, accountConcurrency: { "spare@example.com": 4 },
});

function serve(lang, posts, lanes) {
  const relay = fresh();
  const providers = { providers: [relay, codex()], presets: [], excluded: [], gateway: { running: true, window: true, lanes: lanes.now } };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/lanes") { lanes.reads++; return json(lanes.now); }
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/provider/") && route.request().method() === "POST") {
      const action = url.pathname.slice("/api/provider/".length), body = route.request().postDataJSON();
      posts.push({ action, body });
      if (action === "accountconcurrency") {
        const p = providers.providers.find((x) => x.id === body.id);
        const m = { ...(p.accountConcurrency || {}) };
        if (body.limit == null) delete m[body.account.toLowerCase()];
        else m[body.account.toLowerCase()] = body.limit;
        p.accountConcurrency = m;
      }
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: {
    busy: "1/1 running · 2 queued", running: "3 running", one: "1 at once", two: "2 at once", three: "3 at once", four: "4 at once", five: "5 at once", seven: "7 at once", none: "No limit at once",
    menu: "Requests at once", def: "Provider's", other: "Other…", save: "Save", queue: "Queue size", wait: "Queue wait",
    badQueue: "Queue size: a whole number from 0 to 10000",
  },
  zh: {
    busy: "1/1 运行中 · 2 排队", running: "3 运行中", one: "并发 1", two: "并发 2", three: "并发 3", four: "并发 4", five: "并发 5", seven: "并发 7", none: "并发不限",
    menu: "同时请求数", def: "沿用提供商", other: "其他…", save: "保存", queue: "排队上限", wait: "排队等待",
    badQueue: "排队上限：0 到 10000 的整数",
  },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    const open = async (t, name, lanes) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${name}-lane-limit.png`) });
        }
        await browser.close();
      });
      const page = await (await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const posts = [];
      await page.route("**/*", serve(lang, posts, lanes));
      await page.goto("http://magpie.test/?view=providers");
      await page.locator(".row.provider", { hasText: name }).first().click();
      await page.locator(".editor .accts .acc .alane").first().waitFor({ state: "attached" });
      await page.waitForTimeout(300);
      return { page, errors, posts };
    };
    const row = (page, id) => page.locator(`.editor .accts .acc[data-account-id="${id}"]`);
    const pill = (page, id) => row(page, id).locator(".alane");
    const menu = (page) => page.locator(".proto-menu");
    const item = (page, name) => menu(page).locator(".pm-item", { has: page.locator(".pm-name", { hasText: new RegExp(`^${name.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}$`) }) });
    const scrolled = (page) => page.evaluate(() => [window.scrollY, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop > 0).map((e) => `${e.className}:${e.scrollTop}`)].join(" "));
    // a click that leaves the row where it was and scrolls nothing
    const still = async (page, id, b) => {
      await row(page, id).hover();
      await page.waitForTimeout(150);
      const before = await row(page, id).evaluate((e) => Math.round(e.getBoundingClientRect().top)), sc = await scrolled(page);
      await b.click();
      await page.waitForTimeout(200);
      assert.equal(await row(page, id).evaluate((e) => Math.round(e.getBoundingClientRect().top)), before, `${id}'s row moved`);
      assert.equal(await scrolled(page), sc, "a click scrolled");
    };
    const posted = async (posts, n) => {
      for (let i = 0; i < 60 && posts.length === n; i++) await new Promise((r) => setTimeout(r, 50));
      assert(posts.length > n, "nothing posted");
      return posts.at(-1);
    };
    const opacity = (l) => l.evaluate((e) => getComputedStyle(e).opacity);
    // an account's unset settings wait under its ⋯
    const more = async (page, id) => { await row(page, id).hover(); await row(page, id).locator(".acc-more").click(); };

    test(`${engine} ${lang}: a key's limit on requests at once shows what runs and waits, and is set from the app's menu`, async (t) => {
      const lanes = { now: { "relay#k1": { busy: 1, waiting: 2, limit: 1 } }, reads: 0 };
      const { page, errors, posts } = await open(t, "Relay", lanes);
      // k1 has its own 1, with one out and two waiting; k2 the provider's 2
      assert.equal(await pill(page, "k1").textContent(), w.busy);
      assert.equal(await pill(page, "k2").textContent(), w.two);
      await page.mouse.move(0, 0);
      await page.waitForTimeout(250);
      assert.equal(await opacity(pill(page, "k1")), "1", "a limit is always shown");
      assert.equal(await page.locator("select").count(), 0, "no native select");

      // the lanes are read again, and the pill rewritten in place
      const el = await pill(page, "k1").elementHandle();
      lanes.now = { "relay#k1": { busy: 0, waiting: 0, limit: 1 } };
      const reads = lanes.reads;
      for (let i = 0; i < 80 && (await pill(page, "k1").textContent()) !== w.one; i++) await page.waitForTimeout(50);
      assert(lanes.reads > reads, "the lanes were read again");
      assert.equal(await pill(page, "k1").textContent(), w.one);
      assert.equal(await el.evaluate((e) => e.isConnected), true, "the same pill, not the row drawn again");

      // k2: its own 3 from the menu
      await still(page, "k2", pill(page, "k2"));
      await menu(page).waitFor();
      assert.equal(await menu(page).locator(".pm-head").textContent(), w.menu);
      let n = posts.length;
      await item(page, "3").click();
      let p = await posted(posts, n);
      assert.equal(p.action, "accountconcurrency");
      assert.deepEqual(p.body, { id: "relay", account: "k2", limit: 3 });
      await page.waitForTimeout(250);
      assert.equal(await pill(page, "k2").textContent(), w.three);

      // k1 back to the provider's: limit null
      await still(page, "k1", pill(page, "k1"));
      n = posts.length;
      await item(page, w.def).click();
      p = await posted(posts, n);
      assert.deepEqual(p.body, { id: "relay", account: "k1", limit: null });
      await page.waitForTimeout(250);
      assert.equal(await pill(page, "k1").textContent(), w.two);

      // Other…: typed in place
      await still(page, "k1", pill(page, "k1"));
      await item(page, w.other).click();
      const box = row(page, "k1").locator("input.alane-in");
      await box.waitFor();
      n = posts.length;
      await box.fill("7");
      await box.press("Enter");
      p = await posted(posts, n);
      assert.deepEqual(p.body, { id: "relay", account: "k1", limit: 7 });
      await page.waitForTimeout(250);
      assert.equal(await pill(page, "k1").textContent(), w.seven);

      // a left border thicker or another colour than the right one is a stripe
      const stripes = await page.evaluate(() => [...document.querySelectorAll(".alane, .alane-in")].map((e) => getComputedStyle(e)).filter((s) => parseFloat(s.borderLeftWidth) > parseFloat(s.borderRightWidth) || (parseFloat(s.borderLeftWidth) > 0 && s.borderLeftColor !== s.borderRightColor)).length);
      assert.equal(stripes, 0, "no border stripes");
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: an account's own limit is shown, and one with none only under ⋯`, async (t) => {
      const lanes = { now: {}, reads: 0 };
      const { page, errors, posts } = await open(t, "Codex", lanes);
      assert.equal(await pill(page, "Spare@Example.com").textContent(), w.four);
      assert.equal(await pill(page, "me@example.com").textContent(), w.none);
      await page.mouse.move(0, 0);
      await page.waitForTimeout(250);
      assert.equal(await opacity(pill(page, "Spare@Example.com")), "1");
      assert.equal(await pill(page, "me@example.com").isVisible(), false, "none set: under ⋯ only");
      await more(page, "me@example.com");
      await page.waitForTimeout(250);
      assert.equal(await opacity(pill(page, "me@example.com")), "1");
      assert.equal(await pill(page, "me@example.com").isVisible(), true);
      await still(page, "me@example.com", pill(page, "me@example.com"));
      const n = posts.length;
      await item(page, "2").click();
      const p = await posted(posts, n);
      assert.deepEqual(p.body, { id: "codex", account: "me@example.com", limit: 2 });
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: with no limit, what runs is shown, and none running says no limit again`, async (t) => {
      // chentuoyan on Discord: an account with no limit said only "no
      // limit", never how many of its requests were out
      const lanes = { now: { "codex@me@example.com": { busy: 3, waiting: 0, limit: 0 } }, reads: 0 };
      const { page, errors } = await open(t, "Codex", lanes);
      const me = pill(page, "me@example.com");
      for (let i = 0; i < 80 && (await me.textContent()) !== w.running; i++) await page.waitForTimeout(50);
      assert.equal(await me.textContent(), w.running);
      await page.mouse.move(0, 0);
      await page.waitForTimeout(250);
      assert.equal(await me.isVisible(), true, "running: shown on its line, ⋯ closed");
      assert.equal(await me.evaluate((e) => e.parentElement.classList.contains("acc-line")), true);
      // the rest of the row's pill look, no colour of a full lane
      assert.equal(await me.evaluate((e) => e.classList.contains("busy-lane") || e.classList.contains("queued")), false);
      const el = await me.elementHandle();
      lanes.now = {};
      for (let i = 0; i < 80 && (await me.textContent()) !== w.none; i++) await page.waitForTimeout(50);
      assert.equal(await me.textContent(), w.none);
      assert.equal(await el.evaluate((e) => e.isConnected), true, "the same pill, rewritten in place");
      await page.waitForTimeout(250);
      assert.equal(await me.isVisible(), false, "idle with no limit: under ⋯ again");
      assert.equal(await pill(page, "Spare@Example.com").textContent(), w.four);
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: a key on the provider's limit shows the Concurrency typed before Save`, async (t) => {
      // the pill read the saved limit while the editor said another
      const lanes = { now: {}, reads: 0 };
      const { page, errors, posts } = await open(t, "Relay", lanes);
      const box = page.locator(".editor input.concurrency");
      assert.equal(await pill(page, "k2").textContent(), w.two);
      await box.fill("5");
      assert.equal(await pill(page, "k2").textContent(), w.five, "the provider's limit as typed");
      assert.equal(await pill(page, "k1").textContent(), w.one, "its own limit stays");
      await pill(page, "k2").click();
      await menu(page).waitFor();
      assert.match(await item(page, w.def).textContent(), new RegExp(w.five), "the menu's provider's limit as typed");
      await page.keyboard.press("Escape");
      await box.fill("");
      assert.equal(await pill(page, "k2").textContent(), w.none, "empty is no limit");
      await box.fill("2000");
      assert.equal(await pill(page, "k2").textContent(), w.two, "typed wrong: the saved one");
      assert.equal(posts.length, 0, "nothing saved");
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: the queue's length and wait are saved with the editor, and refused out of range`, async (t) => {
      const lanes = { now: {}, reads: 0 };
      const { page, errors, posts } = await open(t, "Relay", lanes);
      const ql = page.locator(".editor input.queue-limit"), qw = page.locator(".editor input.queue-wait");
      assert.equal(await page.locator(".editor label", { hasText: w.queue }).count(), 1);
      assert.equal(await page.locator(".editor label", { hasText: w.wait }).count(), 1);
      // each label on one line, each field as narrow as Concurrency's
      const one = await page.locator(".editor label").first().evaluate((e) => e.getBoundingClientRect().height);
      for (const l of [w.queue, w.wait]) {
        const h = await page.locator(".editor label", { hasText: l }).evaluate((e) => e.getBoundingClientRect().height);
        assert(h <= one + 1, `${l} takes ${h}px, one line is ${one}px`);
      }
      const width = (l) => l.evaluate((e) => e.getBoundingClientRect().width);
      const cw = await width(page.locator(".editor input.concurrency"));
      assert.equal(await width(ql), cw);
      assert.equal(await width(qw), cw);
      assert.equal(await ql.inputValue(), "3");
      assert.equal(await qw.inputValue(), "30");
      await ql.fill("20000");
      await page.locator(".editor .bar").getByRole("button", { name: w.save, exact: true }).click();
      await page.locator(".editor .editor-error").waitFor();
      assert.equal(await page.locator(".editor .editor-error").textContent(), w.badQueue);
      assert.equal(posts.filter((p) => p.action === "save").length, 0);
      await ql.fill("5");
      await qw.fill("");
      const n = posts.length;
      await page.locator(".editor .bar").getByRole("button", { name: w.save, exact: true }).click();
      const p = await posted(posts, n);
      assert.equal(p.action, "save");
      assert.equal(p.body.queueLimit, 5);
      assert.equal(p.body.queueWait, 0, "empty waits as long as it takes");
      assert.equal(p.body.maxConcurrency, 2, "Concurrency goes with it");

      const missing = await page.evaluate(() => [
        "Queue size", "Queue wait", "No bound", "As long as it takes",
        "How many requests may wait for each key or account; one more is turned away at once. 0 or empty is no bound",
        "Seconds a request waits for a free slot before it is turned away. 0 or empty waits as long as it takes",
        "Queue size: a whole number from 0 to 10000", "Queue wait: a whole number of seconds from 0 to 3600",
        "{n}/{max} running · {q} queued", "{n}/{max} running", "{n} running", "{n} at once", "No limit at once", "its own", "the provider's",
        "At most {n} requests out at once on this one, {whose}; more wait in a queue and go in order. Counted across every model, routing group and agent",
        "No limit on requests at once on this one, {whose}", "{n} running, {q} queued now", "Click to change",
        "{who} takes the provider's limit", "{who}: at most {n} requests at once", "{who}: no limit on requests at once",
        "Requests at once", "Provider's", "Any number at once", "A number of your own, up to 1000",
      ].flatMap((k) => ["zh", "ja", "de"].filter((l) => !I18N[l][k]).map((l) => `${l}: ${k}`)));
      assert.deepEqual(missing, [], "every string has its Chinese, Japanese and German");
      assert.deepEqual(errors, []);
    });
  }
}
