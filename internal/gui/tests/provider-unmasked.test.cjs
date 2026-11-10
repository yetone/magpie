// Run with Node's test runner and Playwright on the module path; see README.md.
// "Send requests unmasked" (lc on Discord) is the user's switch for each
// provider they added (AcMilan on Discord): the editor's Redaction row is
// there whatever the address, opens as it was saved, and Save posts it.
// Turned on, a caution under it names an address not on this computer or
// the local network (a relay, a vendor, a name magpie says resolves elsewhere:
// /api/local-hosts); a local one (localhost, a private IP, a name of the
// user's own DNS resolving only to the local network) has none. A
// name is asked about only once the switch is on, so an editor merely
// opened asks nothing. A signed-in account or a plugin's provider has the
// row too (isee1_1 on Discord), its caution always shown while on: its
// vendor is never local. Another magpie has it too, with no caution: it
// masks by its own settings (CrazyMouse_TT on X). In English and Chinese,
// wide and narrow.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
// the editor folds what few need under Advanced, this field among them:
// opened as a reader would, unless something set in it opened it already
const unfold = async (page) => {
  const adv = page.locator(".editor details.advanced");
  await adv.waitFor({ state: "attached" });
  if (!(await adv.evaluate((d) => d.open))) await adv.locator(":scope > summary").click();
};
const base = {
  icon: "generic", chat: "", responses: "", anthropic: "", catalog: "",
  models: [{ id: "qwen3", name: "Qwen3", on: true }], agents: [], fallback: [], headers: {},
  key: { set: false, masked: "" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "", searches: false, unredacted: false,
};
const local = { ...base, id: "local", name: "Local Box", chat: "http://localhost:11434/v1" };
const lan = { ...base, id: "lan", name: "LAN Box", chat: "http://192.168.1.20:8000/v1", unredacted: true };
const relay = { ...base, id: "relay", name: "Relay", chat: "https://relay.example.com/v1" };
const named = { ...base, id: "named", name: "Corp LLM", chat: "http://llm.corp.lan:8000/v1" };
// what magpie says each name resolves to: only the local network, or not
const lanHosts = { "llm.corp.lan": true, "relay.example.com": false, "api.vendor.example": false };
const codex = {
  id: "codex", name: "Codex", icon: "codex-color", chat: "", responses: "", anthropic: "", catalog: "",
  models: [{ id: "gpt-6", name: "GPT-6", on: true }], agents: [], fallback: [], headers: {}, keyList: [],
  account: { agent: "codex", agentName: "Codex", user: "me@example.com", plan: "PLUS", logins: [{ user: "me@example.com", plan: "PLUS", active: true, on: true }] },
  proxy: "",
};
// a community plugin's provider, keyless, and another magpie
const plugin = {
  ...base, id: "translate", name: "Translate Plugin", chat: "plugin://translate", unredacted: true,
  account: { agent: "translate", agentName: "Translate Plugin", user: "translate", plan: "", logins: [{ user: "translate", active: true, on: true }] },
};
const home = { ...base, id: "home", name: "Home magpie", preset: "remote-magpie", anthropic: "http://192.168.1.5:3425" };

function serve(lang, posts, asked) {
  const providers = { providers: [local, lan, relay, named, codex, plugin, home], presets: [], excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/local-hosts") {
      const { hosts } = route.request().postDataJSON();
      asked.push(...hosts);
      return json({ local: Object.fromEntries(hosts.map((h) => [h, lanHosts[h] === true])) });
    }
    if (url.pathname.startsWith("/api/provider/") && route.request().method() === "POST") {
      posts.push({ action: url.pathname.slice("/api/provider/".length), body: route.request().postDataJSON() });
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { tick: "Send requests unmasked", save: "Save", away: (h) => `${h} isn't on this computer or your local network` },
  zh: { tick: "请求不脱敏", save: "保存", away: (h) => `${h} 不在这台电脑或局域网里` },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    for (const width of [900, 440]) {
      const w = words[lang];
      const open = async (t, name) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-${name}-unmasked.png`), fullPage: true });
          }
          await browser.close();
        });
        const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const posts = [];
        const asked = [];
        await page.route("**/*", serve(lang, posts, asked));
        await page.goto("http://magpie.test/?view=providers");
        // another magpie's row is the list's last, under its sticky foot
        // at 440 in Linux WebKit (#4484): it is clicked as an event
        const pick = page.locator(".row.provider", { hasText: name });
        await (name === "Home magpie" ? pick.dispatchEvent("click") : pick.click());
        await unfold(page);
        await page.locator(".editor .bar").waitFor();
        return { page, errors, posts, asked };
      };
      const row = (page) => page.locator(".editor label.tick", { hasText: w.tick });
      const box = (page) => row(page).locator("input");
      const caution = (page) => page.locator(".editor .hint.unmasked-away");
      // the caution once it has settled: a name is asked about a moment
      // after it is typed
      const cautionSays = async (page, text) => {
        let shown = "";
        for (let i = 0; i < 100; i++) {
          shown = (await caution(page).isVisible()) ? await caution(page).textContent() : "";
          if (text ? shown.includes(text) : shown === "") return;
          await page.waitForTimeout(20);
        }
        assert.fail(`the caution says ${JSON.stringify(shown)}, want ${JSON.stringify(text)}`);
      };
      const save = async (page, posts) => {
        const n = posts.length;
        await page.locator(".editor .bar").getByRole("button", { name: w.save, exact: true }).click();
        for (let i = 0; i < 50 && posts.length === n; i++) await page.waitForTimeout(50);
        return posts.at(-1);
      };

      test(`${engine} ${lang} ${width}: a model on localhost is set to go unmasked, and saved so`, async (t) => {
        const { page, errors, posts } = await open(t, "Local Box");
        assert.equal(await row(page).isVisible(), true);
        await cautionSays(page, "");
        assert.equal(await box(page).isChecked(), false, "opens masked");
        const r = await row(page).boundingBox();
        assert.ok(r.width > 60 && r.x + r.width <= width, `the row fits: ${JSON.stringify(r)}`);
        await box(page).scrollIntoViewIfNeeded();
        await box(page).check();
        const saved = await save(page, posts);
        assert.equal(saved.action, "save");
        assert.equal(saved.body.id, "local");
        assert.equal(saved.body.unredacted, true);
        const missing = await page.evaluate(() => [
          "Redaction", "Send requests unmasked",
          "For a model running on this computer or your local network: secrets, personal data and your masked words go to it as written. Leave it off for a local relay or proxy that passes requests on to a vendor.",
          "{host} isn't on this computer or your local network: turned on, requests go to it unmasked.",
        ].filter((k) => !I18N.zh[k]));
        assert.deepEqual(missing, [], "every string has its Chinese");
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}: one on the LAN saved unmasked opens ticked, and is saved masked`, async (t) => {
        const { page, errors, posts } = await open(t, "LAN Box");
        assert.equal(await box(page).isChecked(), true);
        await box(page).scrollIntoViewIfNeeded();
        await box(page).uncheck();
        const saved = await save(page, posts);
        assert.equal(saved.body.id, "lan");
        assert.equal(saved.body.unredacted, false);
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}: a relay has the row with a caution naming it, and turned on is saved so`, async (t) => {
        const { page, errors, posts, asked } = await open(t, "Relay");
        assert.equal(await row(page).isVisible(), true);
        await page.waitForTimeout(400);
        assert.deepEqual(asked, [], "nothing is asked while the switch is off");
        await cautionSays(page, "");
        await box(page).scrollIntoViewIfNeeded();
        await box(page).check();
        await cautionSays(page, w.away("relay.example.com"));
        await caution(page).scrollIntoViewIfNeeded();
        const c = await caution(page).boundingBox();
        assert.ok(c.x >= 0 && c.x + c.width <= width, `the caution fits: ${JSON.stringify(c)}`);
        const saved = await save(page, posts);
        assert.equal(saved.body.id, "relay");
        assert.equal(saved.body.unredacted, true, "the user's word, whatever the address");
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}: a local one moved to a vendor's address keeps the switch, now with a caution`, async (t) => {
        const { page, errors, posts } = await open(t, "Local Box");
        await box(page).scrollIntoViewIfNeeded();
        await box(page).check();
        await page.locator(".editor input[type=url]").first().fill("https://api.vendor.example/v1");
        await cautionSays(page, w.away("api.vendor.example"));
        assert.equal(await box(page).isChecked(), true);
        const saved = await save(page, posts);
        assert.equal(saved.body.unredacted, true);
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}: one at a LAN name magpie finds local has no caution, and is saved unmasked`, async (t) => {
        const { page, errors, posts, asked } = await open(t, "Corp LLM");
        await box(page).scrollIntoViewIfNeeded();
        await box(page).check();
        for (let i = 0; i < 100 && !asked.length; i++) await page.waitForTimeout(20);
        assert.deepEqual(asked, ["llm.corp.lan"], "the name is asked about once");
        await page.waitForTimeout(100);
        await cautionSays(page, "");
        const saved = await save(page, posts);
        assert.equal(saved.body.id, "named");
        assert.equal(saved.body.unredacted, true);
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}: a name typed in loses the caution once magpie finds it local, and one it doesn't gets it`, async (t) => {
        const { page, errors, asked } = await open(t, "Relay");
        await box(page).scrollIntoViewIfNeeded();
        await box(page).check();
        await cautionSays(page, w.away("relay.example.com"));
        await page.locator(".editor input[type=url]").first().fill("http://llm.corp.lan:8000/v1");
        for (let i = 0; i < 100 && !asked.includes("llm.corp.lan"); i++) await page.waitForTimeout(20);
        assert.ok(asked.includes("llm.corp.lan"), "the name typed is asked about");
        await page.waitForTimeout(100);
        await cautionSays(page, "");
        await page.locator(".editor input[type=url]").first().fill("https://api.vendor.example/v1");
        await cautionSays(page, w.away("api.vendor.example"));
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}: a signed-in account has the row, its caution shown once on, and is saved so`, async (t) => {
        const { page, errors, posts, asked } = await open(t, "Codex");
        assert.equal(await row(page).isVisible(), true);
        assert.equal(await box(page).isChecked(), false, "opens masked");
        await cautionSays(page, "");
        await box(page).scrollIntoViewIfNeeded();
        await box(page).check();
        await cautionSays(page, w.away("Codex"));
        await caution(page).scrollIntoViewIfNeeded();
        const c = await caution(page).boundingBox();
        assert.ok(c.x >= 0 && c.x + c.width <= width, `the caution fits: ${JSON.stringify(c)}`);
        const saved = await save(page, posts);
        assert.equal(saved.action, "save");
        assert.equal(saved.body.id, "codex");
        assert.equal(saved.body.unredacted, true);
        assert.deepEqual(asked, [], "a vendor's service is never asked about");
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}: a plugin's provider saved unmasked opens ticked with its caution, and is saved masked`, async (t) => {
        const { page, errors, posts } = await open(t, "Translate Plugin");
        assert.equal(await box(page).isChecked(), true);
        await cautionSays(page, w.away("Translate Plugin"));
        await box(page).scrollIntoViewIfNeeded();
        await box(page).uncheck();
        await cautionSays(page, "");
        const saved = await save(page, posts);
        assert.equal(saved.body.id, "translate");
        assert.equal(saved.body.unredacted, false);
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}: another magpie has the row with no caution, and turned on is saved so`, async (t) => {
        const { page, errors, posts } = await open(t, "Home magpie");
        assert.equal(await box(page).isChecked(), false);
        await box(page).scrollIntoViewIfNeeded();
        await box(page).check();
        await cautionSays(page, "");
        const saved = await save(page, posts);
        assert.equal(saved.body.id, "home");
        assert.equal(saved.body.unredacted, true);
        assert.deepEqual(errors, []);
      });
    }
  }
}
