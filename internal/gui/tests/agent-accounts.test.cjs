// Run with Node's test runner and Playwright on the module path; see README.md.
// The account or key each agent uses (nb5p on Discord: Claude Code on one
// key of a relay, Codex on another, so the relay's dashboard shows each
// one's use). The editor's Advanced of a provider of two or more keys or
// accounts has Accounts by agent: a line for each agent set to one, with
// its account's button, and Add agent. Picking an agent opens its account
// menu; All accounts lets it go. The choices wait for Save and go out
// before the provider's own save; an editor left alone sends none.
// Magpie's own requests (memory, search, routing's classifier) are one
// to add, first in the menu, sent as the agent "magpie". A provider of
// one key has no such row. In every language, wide and
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
const relay = () => ({
  id: "relay", name: "Relay", icon: "generic", chat: "https://relay.example.com/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "model-a", name: "Model A", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" }, balanceToken: { takes: false, set: false }, proxy: "", maxConcurrency: null,
  keyList: [{ id: "k-a", name: "Work", masked: "sk-…aaaa" }, { id: "k-b", name: "Personal", masked: "sk-…bbbb" }],
  agentChoices: [{ id: "k-a", name: "Work" }, { id: "k-b", name: "Personal" }],
  agentAccounts: { claude: "k-a" },
});
const single = {
  id: "single", name: "Single", icon: "generic", chat: "https://single.example.com/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "model-a", name: "Model A", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "", maxConcurrency: null,
};

// a relay of twelve keys: its account menu has a search
const many = () => {
  const keys = Array.from({ length: 12 }, (_, i) => ({ id: `k-${i}`, name: `Key ${i}`, masked: `sk-…${i}` }));
  return { ...relay(), id: "many", name: "Many", keyList: keys, agentChoices: keys.map(({ id, name }) => ({ id, name })), agentAccounts: { claude: "k-3" } };
};

function serve(lang, posts) {
  const providers = { providers: [relay(), single, many()], presets: [], excluded: [], gateway: { running: true, window: true } };
  const agents = [
    { id: "claude", name: "Claude Code", icon: "", path: "/test/settings.json", fields: [] },
    { id: "codex", name: "Codex", icon: "", path: "/test/config.toml", fields: [] },
  ];
  const state = { agents, profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data, status = 200) => route.fulfill({ status, json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/provider-keys") return json({ keys: [] });
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

const STRINGS = ["Accounts by agent", "Add agent", "Magpie's own requests", "All accounts", "Which account this agent uses", "Which agent", "Search keys", "Save"];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [900, 400]) {
      const open = async (t, name, shot) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        let page;
        t.after(async () => {
          if (process.env.ARTIFACT_DIR && page) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-${shot}-agent-accounts.png`) });
          }
          await browser.close();
        });
        page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const posts = [];
        await page.route("**/*", serve(lang, posts));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator(".row.provider", { hasText: name }).click();
        await unfold(page);
        await page.locator(".editor .concurrency").waitFor();
        await page.waitForTimeout(300); // the editor drawn again as what it asked for comes in
        const tr = (k, v) => page.evaluate(([k, v]) => t(k, v), [k, v]);
        return { page, errors, posts, tr };
      };
      const box = (page) => page.locator(".editor .agent-accts");
      const menu = (page) => page.locator(".proto-menu");
      const item = (page, name) => menu(page).locator(".pm-item", { has: page.locator(".pm-name", { hasText: new RegExp(`^${name}$`) }) });
      // clicked as a reader would: scrolled to first, the scroll settled,
      // since a reader's scroll closes a menu
      const press = async (page, loc) => {
        await loc.scrollIntoViewIfNeeded();
        await page.waitForTimeout(200);
        await loc.click();
      };
      const save = async (page, tr) => page.locator(".editor .bar").getByRole("button", { name: await tr("Save"), exact: true }).click();
      const waitFor = async (page, ok) => { for (let i = 0; i < 60 && !ok(); i++) await page.waitForTimeout(50); };
      const inView = async (loc, what) => {
        const r = await loc.evaluate((e) => { const b = e.getBoundingClientRect(); return { l: b.left, r: b.right, w: innerWidth }; });
        assert(r.l >= 0 && r.r <= r.w + 0.5, `${what} runs out of the window: ${JSON.stringify(r)}`);
      };

      test(`${engine} ${lang} ${width}px: each agent is set to its own key, and Save writes it before the provider`, async (t) => {
        const { page, errors, posts, tr } = await open(t, "Relay", "set");
        if (lang !== "en") {
          const lacking = await page.evaluate(([ks, l]) => ks.filter((k) => !I18N[l]?.[k]), [STRINGS, lang]);
          assert.deepEqual(lacking, [], `every string has its ${lang}`);
        }
        await box(page).waitFor();
        assert.equal(await page.locator(".editor label", { hasText: await tr("Accounts by agent") }).count(), 1);
        // the saved one: Claude Code on Work
        const claude = box(page).locator('.agent-acct[data-agent="claude"]');
        assert.equal(await claude.locator(".who").textContent(), "Claude Code");
        assert.equal(await claude.locator("button").textContent(), "Work");
        await inView(claude.locator("button"), "Claude Code's account");
        // Codex added: its account menu opens at once, Personal picked
        const add = box(page).locator(".agent-acct-add");
        assert.equal(await add.textContent(), await tr("Add agent"));
        await press(page, add);
        await menu(page).waitFor();
        assert.equal(await menu(page).locator(".pm-head").textContent(), await tr("Which agent"));
        assert.equal(await item(page, "Claude Code").count(), 0, "an agent set already isn't offered again");
        await item(page, "Codex").click();
        await page.waitForFunction((h) => document.querySelector(".proto-menu .pm-head")?.textContent === h, await tr("Which account this agent uses"));
        await inView(menu(page), "the account menu");
        assert.deepEqual(await menu(page).locator(".pm-name").allTextContents(), [await tr("All accounts"), "Work", "Personal"]);
        await item(page, "Personal").click();
        const codex = box(page).locator('.agent-acct[data-agent="codex"]');
        assert.equal(await codex.locator("button").textContent(), "Personal");
        // every agent is set: magpie's own requests are left to add
        await press(page, box(page).locator(".agent-acct-add"));
        await menu(page).waitFor();
        assert.deepEqual(await menu(page).locator(".pm-name").allTextContents(), [await tr("Magpie's own requests")]);
        await page.keyboard.press("Escape");
        await menu(page).waitFor({ state: "detached" });
        // Claude Code let go: All accounts
        await press(page, claude.locator("button"));
        await menu(page).waitFor();
        assert.equal(await item(page, await tr("All accounts")).getAttribute("aria-checked"), "false");
        assert.equal(await item(page, "Work").getAttribute("aria-checked"), "true");
        await item(page, await tr("All accounts")).click();
        assert.equal(await box(page).locator('.agent-acct[data-agent="claude"]').count(), 0);
        assert.equal(posts.filter((p) => p.action === "agentaccount").length, 0, "nothing written before Save");
        await save(page, tr);
        await waitFor(page, () => posts.some((p) => p.action === "save"));
        const sent = posts.filter((p) => p.action === "agentaccount").map((p) => p.body);
        assert.deepEqual(sent.sort((a, b) => a.agent.localeCompare(b.agent)), [
          { id: "relay", agent: "claude", account: "" },
          { id: "relay", agent: "codex", account: "k-b" },
        ]);
        assert(posts.findIndex((p) => p.action === "save") > posts.findLastIndex((p) => p.action === "agentaccount"), "the choices go out before the provider's save");
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}px: magpie's own requests are set to a key of their own`, async (t) => {
        const { page, errors, posts, tr } = await open(t, "Relay", "own");
        await box(page).waitFor();
        const own = await tr("Magpie's own requests");
        if (lang !== "en") assert.notEqual(own, "Magpie's own requests", `said in ${lang}`);
        await press(page, box(page).locator(".agent-acct-add"));
        await menu(page).waitFor();
        assert.deepEqual(await menu(page).locator(".pm-name").allTextContents(), [own, "Codex"], "magpie's own first, then the agents not set");
        await inView(menu(page), "the agent menu");
        await item(page, own).click();
        await page.waitForFunction((h) => document.querySelector(".proto-menu .pm-head")?.textContent === h, await tr("Which account this agent uses"));
        await item(page, "Personal").click();
        const line = box(page).locator('.agent-acct[data-agent="magpie"]');
        assert.equal(await line.locator(".who").textContent(), own);
        assert.equal(await line.locator("button").textContent(), "Personal");
        await inView(line.locator("button"), "magpie's own requests' account");
        const who = await line.locator(".who").evaluate((e) => ({ l: e.getBoundingClientRect().left, r: e.getBoundingClientRect().right, b: e.parentElement.querySelector("button").getBoundingClientRect().left }));
        assert(who.l >= 0 && who.r <= who.b + 0.5, `the name runs into its account: ${JSON.stringify(who)}`);
        await save(page, tr);
        await waitFor(page, () => posts.some((p) => p.action === "save"));
        assert.deepEqual(posts.filter((p) => p.action === "agentaccount").map((p) => p.body), [{ id: "relay", agent: "magpie", account: "k-b" }]);
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}px: an editor left alone sends no choice, and a provider of one key has no row`, async (t) => {
        const { page, errors, posts, tr } = await open(t, "Relay", "alone");
        await box(page).waitFor();
        await save(page, tr);
        await waitFor(page, () => posts.some((p) => p.action === "save"));
        assert.equal(posts.filter((p) => p.action === "agentaccount").length, 0);
        assert.deepEqual(errors, []);
        const other = await open(t, "Single", "single");
        assert.equal(await box(other.page).count(), 0);
        assert.equal(await other.page.locator(".editor label", { hasText: await other.tr("Accounts by agent") }).count(), 0);
        assert.deepEqual(other.errors, []);
      });

      test(`${engine} ${lang} ${width}px: a provider of many keys has a search in the account menu`, async (t) => {
        const { page, errors, tr } = await open(t, "Many", "many");
        await box(page).waitFor();
        const claude = box(page).locator('.agent-acct[data-agent="claude"]');
        assert.equal(await claude.locator("button").textContent(), "Key 3");
        await press(page, claude.locator("button"));
        await menu(page).waitFor();
        const q = menu(page).locator("input.sc-q");
        assert.equal(await q.getAttribute("placeholder"), await tr("Search keys"));
        await q.fill("Key 1");
        await page.waitForFunction(() => [...document.querySelectorAll(".proto-menu .pm-item")].filter((b) => b.offsetParent).length === 3);
        await inView(menu(page), "the account menu");
        assert.deepEqual(errors, []);
      });
    }
  }
}
