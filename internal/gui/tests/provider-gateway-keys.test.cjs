// Run with Node's test runner and Playwright on the module path; see README.md.
// A provider's gateway keys (justforlxz on X: with many keys, ticking a
// provider in each key's models is a chore). The editor's Advanced has a
// Gateway keys row: its button says how many keys may use the provider, and
// its menu ticks them. A key that may use every provider is ticked and can
// be unticked (justforlxz on X: such keys were locked, so the list did
// nothing); one held to this provider alone is ticked and stays ticked. The changes wait for
// Save, go out before the provider's own save, and a closed editor drops
// them. A provider no key could use has no row. In every language, wide and
// narrow, in Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const unfold = async (page) => {
  const adv = page.locator(".editor details.advanced");
  await adv.waitFor({ state: "attached" });
  if (!(await adv.evaluate((d) => d.open))) await adv.locator(":scope > summary").click();
};
const relay = {
  id: "relay", name: "Relay", icon: "generic", chat: "https://relay.example.com/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "model-a", name: "Model A", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "", maxConcurrency: null,
};
const codex = {
  id: "codex", name: "Codex", icon: "codex-color", chat: "", responses: "", anthropic: "", catalog: "",
  models: [{ id: "gpt-6", name: "GPT-6", on: true }], agents: [], fallback: [], headers: {}, keyList: [],
  account: { agent: "codex", agentName: "Codex", user: "me@example.com", plan: "PLUS", logins: [{ user: "me@example.com", plan: "PLUS", active: true, on: true }] },
  proxy: "", maxConcurrency: null,
};
const keysOf = () => [
  { id: "k-all", name: "Laptop", reach: "all" },
  { id: "k-team", name: "Team", reach: "whole" },
  { id: "k-ci", name: "CI", reach: "", via: ["re:^gpt-"] },
  { id: "k-solo", name: "Solo", reach: "whole", only: true },
  { id: "k-bot", name: "Bot", reach: "some", off: true },
];

function serve(lang, posts, keys) {
  const providers = { providers: [relay, codex], presets: [], excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data, status = 200) => route.fulfill({ status, json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/provider-keys") {
      if (req.method() === "POST") {
        const body = req.postDataJSON();
        posts.push({ action: "provider-keys", body });
        if (keys.refuse) return json({ error: keys.refuse }, 400);
        keys.post?.(body);
        return json({ keys: keys.of(url.searchParams.get("provider")) });
      }
      return json({ keys: keys.of(url.searchParams.get("provider")) });
    }
    if (url.pathname.startsWith("/api/provider/") && req.method() === "POST") {
      posts.push({ action: url.pathname.slice("/api/provider/".length), body: req.postDataJSON() });
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const STRINGS = [
  "Gateway keys", "Gateway keys that may use this provider", "Every key", "No key", "1 of {m} keys", "{n} of {m} keys",
  "Every provider; unticked, it keeps the others there are now", "Only this provider: change it on the Gateway page",
  "Every model", "Some models", "Maybe some, through {list}", "Search keys", "Off", "Save", "Discard",
];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [900, 400]) {
      const open = async (t, name, keys = { of: () => keysOf() }) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        let page;
        t.after(async () => {
          if (process.env.ARTIFACT_DIR && page) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-${name}-gateway-keys.png`) });
          }
          await browser.close();
        });
        page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const posts = [];
        await page.route("**/*", serve(lang, posts, keys));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator(".row.provider", { hasText: name }).click();
        await unfold(page);
        await page.locator(".editor .concurrency").waitFor();
        await page.waitForTimeout(300); // the editor drawn again as what it asked for comes in
        // t in the page: the language's own words, English where it has none
        const tr = (k, v) => page.evaluate(([k, v]) => t(k, v), [k, v]);
        return { page, errors, posts, tr, keys };
      };
      const button = (page) => page.locator(".editor button.gw-keys");
      const menu = (page) => page.locator(".proto-menu");
      // opened as a reader would: the button scrolled to first, the scroll
      // settled, since a reader's scroll closes the menu
      const openMenu = async (page) => {
        const b = button(page);
        await b.scrollIntoViewIfNeeded();
        await page.waitForTimeout(200);
        await b.click();
        await menu(page).waitFor();
      };
      const item = (page, name) => menu(page).locator(".pm-item", { has: page.locator(".pm-name", { hasText: new RegExp(`^${name}$`) }) });
      const save = async (page, tr) => page.locator(".editor .bar").getByRole("button", { name: await tr("Save"), exact: true }).click();
      const waitPosts = async (page, posts, n) => { for (let i = 0; i < 60 && posts.length < n; i++) await page.waitForTimeout(50); };
      const inView = async (page, loc, what) => {
        const r = await loc.evaluate((e) => { const b = e.getBoundingClientRect(); return { l: b.left, r: b.right, w: innerWidth }; });
        assert(r.l >= 0 && r.r <= r.w + 0.5, `${what} runs out of the window: ${JSON.stringify(r)}`);
      };

      test(`${engine} ${lang} ${width}px: the keys that may use a provider are ticked here, and Save writes the change`, async (t) => {
        const { page, errors, posts, tr } = await open(t, "Relay");
        if (lang !== "en") {
          const lacking = await page.evaluate(([ks, l]) => ks.filter((k) => !I18N[l]?.[k]), [STRINGS, lang]);
          assert.deepEqual(lacking, [], `every string has its ${lang}`);
        }
        assert.equal(await page.locator(".editor label", { hasText: await tr("Gateway keys") }).count(), 1);
        const b = button(page);
        // Laptop (every provider), Team, Solo and Bot may use it; CI only maybe
        assert.equal(await b.textContent(), await tr("{n} of {m} keys", { n: 4, m: 5 }));
        assert.equal(await b.getAttribute("aria-label"), await tr("Gateway keys that may use this provider"));
        await inView(page, b, "the button");
        await openMenu(page);
        await inView(page, menu(page), "the menu");
        assert.equal(await menu(page).locator(".pm-head").textContent(), await tr("Gateway keys that may use this provider"));
        const on = async (n) => (await item(page, n).getAttribute("aria-checked")) === "true";
        assert.deepEqual(await Promise.all(["Laptop", "Team", "CI", "Solo", "Bot"].map(on)), [true, true, false, true, true]);
        assert.equal(await item(page, "Laptop").locator(".pm-note").textContent(), await tr("Every provider; unticked, it keeps the others there are now"));
        assert.equal(await item(page, "Solo").locator(".pm-note").textContent(), await tr("Only this provider: change it on the Gateway page"));
        assert.equal(await item(page, "Team").locator(".pm-note").textContent(), await tr("Every model"));
        assert.equal(await item(page, "Bot").locator(".pm-note").textContent(), (await tr("Off")) + " · " + (await tr("Some models")));
        assert.equal(await item(page, "CI").locator(".pm-note").textContent(), await tr("Maybe some, through {list}", { list: "re:^gpt-" }));
        // a key on every provider is unticked; one on this provider alone stays
        await item(page, "Laptop").click();
        await item(page, "Solo").click();
        assert.equal(await on("Laptop"), false, "a key on every provider is unticked here");
        assert.equal(await on("Solo"), true, "a key on this provider alone can't be unticked here");
        await item(page, "Team").click();
        await item(page, "CI").click();
        await page.keyboard.press("Escape");
        await menu(page).waitFor({ state: "detached" });
        assert.equal(await b.textContent(), await tr("{n} of {m} keys", { n: 3, m: 5 }));
        assert.equal(posts.length, 0, "nothing is written before Save");

        await save(page, tr);
        await waitPosts(page, posts, 2);
        assert.deepEqual(posts.map((p) => p.action), ["provider-keys", "save"], "the keys go out before the provider's own save");
        assert.deepEqual(posts[0].body, { provider: "relay", add: ["k-ci"], remove: ["k-all", "k-team"] });
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}px: an untouched editor leaves at once, a changed one asks Discard and drops the change`, async (t) => {
        const { page, errors, posts, tr } = await open(t, "Codex");
        const modal = page.locator("#modal"), ask = page.getByRole("alertdialog");
        // the row drawn, nothing ticked: Escape leaves with no Discard
        await button(page).waitFor();
        await page.waitForFunction(() => document.querySelector(".editor button.gw-keys")?.textContent !== "…");
        await page.keyboard.press("Escape");
        await Promise.race([modal.waitFor({ state: "hidden" }), ask.waitFor()]);
        assert.equal(await ask.count(), 0, "no Discard asked for an untouched editor");

        await page.locator(".row.provider", { hasText: "Codex" }).click();
        await unfold(page);
        await page.waitForTimeout(300);
        await openMenu(page);
        await item(page, "Team").click();
        await item(page, "Bot").click();
        await page.keyboard.press("Escape");
        await menu(page).waitFor({ state: "detached" });
        assert.equal(await button(page).textContent(), await tr("{n} of {m} keys", { n: 2, m: 5 }));
        // ticked back as it was: nothing to discard either
        await openMenu(page);
        await item(page, "Team").click();
        await item(page, "Bot").click();
        await page.keyboard.press("Escape");
        await menu(page).waitFor({ state: "detached" });
        await page.keyboard.press("Escape");
        await Promise.race([modal.waitFor({ state: "hidden" }), ask.waitFor()]);
        assert.equal(await ask.count(), 0, "no Discard asked once ticked back");

        await page.locator(".row.provider", { hasText: "Codex" }).click();
        await unfold(page);
        await page.waitForTimeout(300);
        await openMenu(page);
        await item(page, "Team").click();
        await page.keyboard.press("Escape");
        await menu(page).waitFor({ state: "detached" });
        await page.keyboard.press("Escape");
        await ask.waitFor();
        await ask.getByRole("button", { name: await tr("Discard"), exact: true }).click();
        await modal.waitFor({ state: "hidden" });
        await page.locator(".row.provider", { hasText: "Codex" }).click();
        await unfold(page);
        await page.waitForTimeout(300);
        assert.equal(await button(page).textContent(), await tr("{n} of {m} keys", { n: 4, m: 5 }), "opened again as saved");
        await save(page, tr);
        await waitPosts(page, posts, 1);
        await page.waitForTimeout(200);
        assert.deepEqual(posts.map((p) => p.action), ["save"], "a save with no key changed writes no key");
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}px: a refused change says why and saves nothing else`, async (t) => {
        const keys = { of: () => keysOf(), refuse: "Team may use only this provider's models" };
        const { page, errors, posts, tr } = await open(t, "Relay", keys);
        await openMenu(page);
        await item(page, "Team").click();
        await page.keyboard.press("Escape");
        await save(page, tr);
        await page.locator(".editor .editor-error").waitFor();
        assert.match(await page.locator(".editor .editor-error").textContent(), /Team may use only/);
        assert.deepEqual(posts.map((p) => p.action), ["provider-keys"], "the provider isn't saved past the refusal");
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}px: with no key, every key and one key the row says so, and with none to give there is no row`, async (t) => {
        const keys = { of: () => [{ id: "k1", name: "One", reach: "whole" }, { id: "k2", name: "Two", reach: "" }] };
        const { page, errors, tr } = await open(t, "Relay", keys);
        assert.equal(await button(page).textContent(), await tr("1 of {m} keys", { m: 2 }));
        await openMenu(page);
        await item(page, "Two").click();
        await page.keyboard.press("Escape");
        assert.equal(await button(page).textContent(), await tr("Every key"));
        await openMenu(page);
        await item(page, "One").click();
        await item(page, "Two").click();
        await page.keyboard.press("Escape");
        assert.equal(await button(page).textContent(), await tr("No key"));
        keys.of = () => [];
        await page.keyboard.press("Escape");
        await page.getByRole("alertdialog").getByRole("button", { name: await tr("Discard"), exact: true }).click();
        await page.locator("#modal").waitFor({ state: "hidden" });
        await page.locator(".row.provider", { hasText: "Codex" }).click();
        await unfold(page);
        await page.waitForTimeout(400);
        assert.equal(await button(page).count(), 0, "no key to give, no row");
        assert.equal(await page.locator(".editor label", { hasText: new RegExp(`^${await tr("Gateway keys")}$`) }).count(), 0);
        assert.deepEqual(errors, []);
      });
      // justforlxz's case: new keys, each on every model, and keys held to
      // other providers. Each click ticks or unticks, by mouse and by
      // Space and Enter, Save writes it, and the editor opened again shows
      // it, from a key provider's editor and a subscription's.
      for (const name of ["Relay", "Codex"]) {
        test(`${engine} ${lang} ${width}px: ${name}'s keys on every model, or on other providers, tick and untick, and Save keeps it`, async (t) => {
          const pid = name.toLowerCase();
          const saved = { "k-1": "all", "k-2": "all", "k-3": "all", "k-4": "", "k-5": "" };
          const names = { "k-1": "Laptop", "k-2": "Phone", "k-3": "Server", "k-4": "Team", "k-5": "Bot" };
          const keys = { of: () => Object.entries(saved).map(([id, reach]) => ({ id, name: names[id], reach })) };
          keys.post = ({ add, remove }) => { for (const id of add) saved[id] = "whole"; for (const id of remove) saved[id] = ""; };
          const { page, errors, posts, tr } = await open(t, name, keys);
          const b = button(page);
          await page.waitForFunction(() => document.querySelector(".editor button.gw-keys")?.textContent !== "…");
          assert.equal(await b.textContent(), await tr("{n} of {m} keys", { n: 3, m: 5 }));
          await openMenu(page);
          const on = async (n) => (await item(page, n).getAttribute("aria-checked")) === "true";
          const all = () => Promise.all(Object.values(names).map(on));
          assert.deepEqual(await all(), [true, true, true, false, false]);
          await item(page, "Laptop").click();
          await item(page, "Team").click();
          assert.deepEqual(await all(), [false, true, true, true, false], "a click ticks or unticks");
          // the keyboard: Space and Enter on a row
          await item(page, "Phone").focus();
          await page.keyboard.press("Space");
          await item(page, "Bot").focus();
          await page.keyboard.press("Enter");
          assert.deepEqual(await all(), [false, false, true, true, true], "Space and Enter tick and untick");
          // closed by a click beside it in the editor, as a reader closes it
          await page.locator(".editor .ehead b").click();
          await menu(page).waitFor({ state: "detached" });
          assert.equal(await b.textContent(), await tr("{n} of {m} keys", { n: 3, m: 5 }));
          await save(page, tr);
          await waitPosts(page, posts, 2);
          assert.deepEqual(posts.map((p) => p.action), ["provider-keys", "save"]);
          assert.deepEqual(posts[0].body, { provider: pid, add: ["k-4", "k-5"], remove: ["k-1", "k-2"] });
          // opened again: the keys as saved
          await page.locator(".editor").waitFor({ state: "detached" });
          await page.locator(".row.provider", { hasText: name }).click();
          await unfold(page);
          await page.waitForFunction(() => document.querySelector(".editor button.gw-keys")?.textContent !== "…");
          assert.equal(await b.textContent(), await tr("{n} of {m} keys", { n: 3, m: 5 }));
          await openMenu(page);
          assert.deepEqual(await all(), [false, false, true, true, true], "opened again as saved");
          assert.deepEqual(errors, []);
        });
      }
    }
  }
}
