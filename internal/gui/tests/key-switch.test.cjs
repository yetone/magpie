// Run with Node's test runner and Playwright on the module path; see README.md.
// A key is switched off and on again from its own row, by name (wei_VC on
// X: "I want to disable some keys; now there is only Remove, and to use one
// again I have to add it again"). The tick dot was the only switch and
// wasn't read as one. Each key's row has Switch off, or Switch on when it is
// off, beside Remove: the first key too, while another key is on. Switch
// off is always shown, as Remove is, and so is an off key's Switch
// on. The last key on has none (the provider's own switch is for that). The click posts
// keys/off or keys/on for that row's key, never keys/remove, and doesn't
// scroll the page; the button fits a phone's width. In every language,
// Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.ASSET_DIR || path.resolve(__dirname, "../assets"));
const key = (id, name, on, active) => ({ id, name, masked: "sk-…" + id.slice(0, 3), on, ...(active ? { active: true } : {}) });
const relay = () => ({
  id: "relay", name: "Relay", icon: "generic", chat: "https://relay.example.com/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "big", name: "", on: true }], agents: [], fallback: [], headers: {}, routing: "",
  key: { set: true, masked: "sk-…aaa" }, balanceToken: { takes: false, set: false }, proxy: "",
  keyList: [key("aaaaaaaaaa", "team-a", true, true), key("bbbbbbbbbb", "team-b", true), key("cccccccccc", "", false)],
});
// one key on, one off: the one on is the last, so it has no Switch off
const lone = () => ({
  ...relay(), id: "lone", name: "Lone", chat: "https://lone.example.com/v1",
  keyList: [key("dddddddddd", "only", true, true), key("eeeeeeeeee", "spare", false)],
});

function serve(lang, posted) {
  const providers = { providers: [relay(), lone()], presets: [], excluded: [], gateway: { running: true, window: true } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/keys/")) {
      const body = route.request().postDataJSON();
      posted.push([url.pathname.slice(5), body.id, body.ref]);
      // the switch as the server does it: the first key off gives its place
      // to the next one on, and goes after it, off
      const p = providers.providers.find((x) => x.id === body.id);
      const k = p.keyList.find((x) => x.id === body.ref);
      k.on = url.pathname.endsWith("/on");
      if (!k.on && k.active) {
        delete k.active;
        const next = p.keyList.find((x) => x.on);
        next.active = true;
        p.keyList = [next, ...p.keyList.filter((x) => x !== next)];
      }
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file).catch(() => ""), contentType });
  };
}

const words = {
  en: ["Switch off", "Switch on"],
  zh: ["停用", "启用"],
  "zh-TW": ["停用", "啟用"],
  ja: ["オフにする", "オンにする"],
  de: ["Ausschalten", "Einschalten"],
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    for (const width of [420, 900]) {
      test(`${engine} ${lang} ${width}px: a key switches off and on from its row`, async (t) => {
        const [off, on] = words[lang];
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const posted = [];
        await page.route("**/*", serve(lang, posted));
        await page.goto("http://magpie.test/?view=providers");
        const open = async (name) => {
          await page.locator(".row.provider", { hasText: name }).first().click();
          await page.locator(".editor .accts .acc.add").waitFor();
        };
        const row = (id) => page.locator(`.editor .accts.keys .acc[data-account-id="${id}"]`);
        const sw = (id) => row(id).locator("button.key-switch");
        const scrolled = () => page.evaluate(() => [window.scrollY, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop > 0).map((e) => `${String(e.className).split(" ")[0]}:${e.scrollTop}`)].join(" "));
        const click = async (id, label) => {
          const b = sw(id);
          await row(id).scrollIntoViewIfNeeded();
          // shown off the pointer, Switch off as Switch on (#841)
          await page.mouse.move(0, 0);
          assert.equal(await b.evaluate((e) => getComputedStyle(e).opacity), "1", `${id}'s ${label} is hidden off the pointer`);
          await row(id).hover({ position: { x: 4, y: 4 } });
          assert.equal((await b.textContent()).trim(), label, `${id}'s switch`);
          const box = await b.boundingBox();
          assert.ok(box && box.x >= 0 && box.x + box.width <= width, `${id}'s switch off the row: ${JSON.stringify(box)}`);
          const sc = await scrolled();
          const n = posted.length;
          await b.click();
          // the post is counted, not waited for by time
          while (posted.length === n) await page.waitForTimeout(20);
          assert.equal(await scrolled(), sc, "a click scrolled");
        };

        await open("Relay");
        // every key has a switch: the first and the second on, the third off
        assert.equal((await sw("aaaaaaaaaa").textContent()).trim(), off);
        assert.equal((await sw("bbbbbbbbbb").textContent()).trim(), off);
        assert.equal((await sw("cccccccccc").textContent()).trim(), on);
        // the first key goes off; team-b takes its place and team-a can come back
        await click("aaaaaaaaaa", off);
        await row("aaaaaaaaaa").locator("button.key-switch", { hasText: on }).waitFor();
        assert.ok(await row("aaaaaaaaaa").evaluate((r) => r.classList.contains("off")), "team-a isn't shown off");
        await click("cccccccccc", on);
        await row("cccccccccc").locator("button.key-switch", { hasText: off }).waitFor();
        await click("aaaaaaaaaa", on);
        await row("aaaaaaaaaa").locator("button.key-switch", { hasText: off }).waitFor();
        assert.deepEqual(posted, [
          ["keys/off", "relay", "aaaaaaaaaa"],
          ["keys/on", "relay", "cccccccccc"],
          ["keys/on", "relay", "aaaaaaaaaa"],
        ]);

        // the last key on has no switch; the one off beside it has
        await page.keyboard.press("Escape");
        await open("Lone");
        assert.equal(await sw("dddddddddd").count(), 0, "the last key on has a Switch off");
        assert.equal((await sw("eeeeeeeeee").textContent()).trim(), on);

        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "the page scrolls sideways");
        assert.deepEqual(errors, []);
      });
    }
  }
}
