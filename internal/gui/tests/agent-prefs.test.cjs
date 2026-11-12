// Run with Node's test runner and Playwright on the module path; see README.md.
// An agent's own settings are where the agent is (the owner, 10-20: Codex's
// and Claude Code's rows sat in Settings › General, apart from the agents
// they set). In the window, Codex's opened row holds its nine and Claude
// Code's and Grok Build's their one, Long conversations (Grok Build's since
// uk.jent on Discord: its context_window is where it compacts); Settings has none of them, but its
// search still finds them, as a row saying where they are whose Open shows
// the agent's sheet. In the panel a "Settings" line opens the sheet, and so
// does the agent's menu; shut, the rows are back where they were. Gateway
// mode writes no agent's files, so none of it shows, but Long conversations:
// it sets the windows the gateway serves, which agents elsewhere read, so
// with no Codex or Claude Code here (a gateway, magpie in Docker) it stays
// in Settings › General, on the size set before (Linka on Discord). Five languages, wide
// and narrow, Chromium and WebKit; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, agentRow, openAgentSettings } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const CODEX = ["codexAgentsV1Row", "codexOpenRow", "codexServiceTierRow", "fullContextRow", "codexAsyncRow", "codexTitlesRow", "codexDescriptionsRow", "codexPredictionsRow", "codexAutoReviewRow", "codexMemoriesRow"];

function settingsPayload(over) {
  return {
    theme: "light", lang: "en", tray: "panel", quotaLeft: false, currency: "usd",
    dock: false, dockWindow: false, proxy: "", redact: false, redactPersonal: false, redactWords: [],
    codexWarmup: "", claudeWarmup: "", codexWarmAt: "", claudeWarmAt: "", workbuddyCheckin: false, noStats: false,
    trayUsage: "", trayUsageEvery: 3, vision: "", imageGen: "",
    version: "0.1.400", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425",
    proxyNow: "none", proxySource: "none", login: false,
    visionModels: [], imageGenModels: [], workbuddyCheckins: [], lanURLs: [], titleModels: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
    ...over,
  };
}

function server(lang, { gateway = false, none = false, posts = [] } = {}) {
  let settings = settingsPayload({ lang, compactAt: 200000, ...(gateway ? { web: true, gatewayMode: "", gatewayOn: true } : {}) });
  // Codex with a memories field, so its memories row is there to show
  const codex = agentRow("codex", "Codex");
  codex.fields.push({ key: "memories", label: "memories", value: "", options: [{ value: "relay/m1", label: "m1", ref: "relay/m1" }] });
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${gateway}${gateway ? ",gateway:true" : ""}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: none ? [agentRow("pi", "Pi")] : [codex, agentRow("claude", "Claude Code"), { ...agentRow("grok", "Grok Build"), icon: "xai" }, agentRow("pi", "Pi")], profiles: [], settings: { lang, theme: "light" }, fx: settings.fx });
    if (url.pathname === "/api/settings/full-context") {
      const body = route.request().postDataJSON();
      posts.push(body);
      settings = { ...settings, fullContext: body.on, ...(body.at !== undefined ? { compactAt: body.at } : {}) };
      return json(settings);
    }
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try {
      await route.fulfill({ body: await fs.readFile(file), contentType });
    } catch {
      await route.fulfill({ status: 404, body: "" });
    }
  };
}

const want = {
  en: { titles: "Codex thread titles", sheet: "Codex settings", open: "Open" },
  zh: { titles: "Codex 会话标题", sheet: "Codex 设置", open: "打开" },
  "zh-TW": { titles: "Codex 會話標題", sheet: "Codex 設定", open: "開啟" },
  ja: { titles: "Codex のスレッドタイトル", sheet: "Codex の設定", open: "開く" },
  de: { titles: "Codex-Thread-Titel", sheet: "Einstellungen von Codex", open: "Öffnen" },
};
// the ids of an agent's settings rows shown in `box`, in order
const shownIn = (page, box) => page.locator(box).evaluate((b) => [...b.querySelectorAll(":scope [id$=Row]")].filter((r) => r.checkVisibility()).map((r) => r.id));
const sheet = (page) => page.locator("#modal:not([hidden]) .editor.agent-prefs");
const noSideScroll = (page, what) => page.evaluate(() => [document.documentElement.scrollWidth, innerWidth]).then(([w, iw]) => assert(w <= iw, `${what}: no sideways scroll: ${w} > ${iw}`));

async function newPage(engine, t, width, lang, opts) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  t.after(() => browser.close());
  const page = await browser.newPage({ viewport: { width, height: 760 }, reducedMotion: "reduce" });
  page.setDefaultTimeout(8000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", server(lang, opts));
  await watchSettingsReads(page);
  return { page, errors };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(want)) {
    for (const width of [1100, 440]) {
      test(`${engine} ${lang} ${width}: an agent's own settings are in its opened row, and Settings' search finds them there`, async (t) => {
        const { page, errors } = await newPage(engine, t, width, lang);
        const w = want[lang];
        await page.goto("http://magpie.test/?view=agents");
        await openAgentSettings(page, "codex");
        const codexBox = '.row.agent[data-id="codex"] .ag-prefs';
        assert.deepEqual(await shownIn(page, codexBox), CODEX, `${lang} ${width}: Codex's ten`);
        assert(await page.locator(codexBox).evaluate((b) => b.scrollWidth <= b.clientWidth + 1), `${lang} ${width}: the rows fit the opened row`);
        await noSideScroll(page, `${lang} ${width} Codex`);
        await openAgentSettings(page, "claude");
        assert.deepEqual(await shownIn(page, '.row.agent[data-id="claude"] .ag-prefs'), ["fullContextRow"], `${lang} ${width}: Claude Code's one`);
        // Grok Build's one, Long conversations, whose line names it
        await openAgentSettings(page, "grok");
        const grokBox = '.row.agent[data-id="grok"] .ag-prefs';
        assert.deepEqual(await shownIn(page, grokBox), ["fullContextRow"], `${lang} ${width}: Grok Build's one`);
        assert.match(await page.locator(`${grokBox} #fullContextRow .sub`).textContent(), /Grok Build/, `${lang}: the line names Grok Build`);
        assert(await page.locator(grokBox).evaluate((b) => b.scrollWidth <= b.clientWidth + 1), `${lang} ${width}: Grok Build's row fits`);
        await noSideScroll(page, `${lang} ${width} Grok Build`);

        // none in Settings; its search says where they are
        await page.goto("http://magpie.test/?view=settings");
        await page.locator("#setQ").waitFor();
        // #setQ is in the page before Settings is drawn, and Long
        // conversations with it: wait for the drawing to take it out
        await page.waitForFunction(() => !document.querySelector("#view-settings #fullContextRow")).catch(() => {});
        for (const id of CODEX) assert.equal(await page.locator(`#view-settings #${id}`).count(), 0, `${lang}: ${id} not in Settings`);
        assert.equal(await page.locator("#agentPrefLinks .agent-pref-link").first().isVisible(), false, "no link rows till searching");
        await page.locator("#setQ").fill(w.titles);
        const link = page.locator("#agentPrefLinks .agent-pref-link").filter({ hasText: w.titles });
        await link.waitFor();
        await noSideScroll(page, `${lang} ${width} search`);
        await link.getByRole("button", { name: w.open, exact: true }).click();
        await sheet(page).waitFor();
        assert.equal(await sheet(page).locator(".ehead").textContent(), w.sheet);
        assert.deepEqual(await shownIn(page, "#modal .editor.agent-prefs"), CODEX, `${lang} ${width}: the sheet has Codex's ten`);
        assert(await page.locator("#view-agents").isVisible(), "Open goes to the Agents page");
        await page.keyboard.press("Escape");
        await page.locator("#modal").waitFor({ state: "hidden" });
        // shut, every row is back in the page, none lost with the sheet
        for (const id of CODEX) assert.equal(await page.locator(`#${id}`).count(), 1, `${lang}: ${id} back`);
        assert.deepEqual(errors, []);
      });
    }

    // ysm119 on Discord (v0.1.2519, WebKit): Codex's row opened and shut
    // again, then Settings, threw "null is not an object (evaluating
    // '$(row + ".who").title = t(en)')". Shut, the row was drawn again
    // without its opened part, and Codex's settings rows went with that
    // part, out of the page, where Settings and a language change look
    // for them. Shut, they are back in it; Settings opens on its own
    // button, with no reload, and says what each row does.
    for (const width of [1100, 440]) {
      test(`${engine} ${lang} ${width}: Settings opens after Codex's row was opened and shut`, async (t) => {
        const { page, errors } = await newPage(engine, t, width, lang);
        await page.goto("http://magpie.test/?view=agents");
        await openAgentSettings(page, "codex");
        await page.locator('.row.agent[data-id="codex"] .ag-link').click();
        await page.locator('.row.agent[data-id="codex"] .ag-prefs').waitFor({ state: "detached" });
        await page.locator('.row.agent[data-id="codex"] .ag-link[aria-expanded="false"]').waitFor();
        for (const id of CODEX) assert(await page.evaluate((id) => document.getElementById(id)?.isConnected === true, id), `${lang} ${width}: ${id} in the page once shut`);
        await page.locator("#prefs").click();
        await page.locator("#view-settings").waitFor();
        await page.waitForFunction(() => __settingsReads.asked > 0 && __settingsReads.asked === __settingsReads.drawn);
        assert.deepEqual(errors, [], `${lang} ${width}: no error opening Settings`);
        assert.equal(await page.locator("#status.err").textContent().catch(() => ""), "", `${lang} ${width}: no red error line`);
        const tip = await page.evaluate(() => document.querySelector("#codexTitlesRow .who")?.title);
        assert.equal(tip, await page.evaluate(() => t("Names each thread in a hidden request of its own; same as the session, it goes on the session’s model, as Codex sends it when that is one of Codex’s own")), `${lang}: the titles row says what it does`);
        if (lang !== "en") assert(!/^Names each/.test(tip), `${lang}: in the language`);
        await noSideScroll(page, `${lang} ${width} Settings`);
      });
    }

    test(`${engine} ${lang}: in the panel a Settings line and the agent's menu open its sheet`, async (t) => {
      const { page, errors } = await newPage(engine, t, 360, lang);
      const w = want[lang];
      await page.goto("http://magpie.test/?mode=panel&view=agents");
      // the panel shows a row's controls once it's opened
      await page.locator('.row.agent[data-id="codex"] .ag-sum').click();
      const line = page.locator('.row.agent[data-id="codex"] .agent-prefs-open');
      await line.waitFor();
      assert.equal(await line.getAttribute("aria-label"), w.sheet);
      assert.equal(await page.locator('.row.agent[data-id="pi"] .agent-prefs-open').count(), 1, "Pi has one, for its own context window only");
      await noSideScroll(page, `${lang} panel`);
      await line.click();
      await sheet(page).waitFor();
      assert.deepEqual(await shownIn(page, "#modal .editor.agent-prefs"), CODEX);
      assert(await page.locator("#modal .dialog").evaluate((d) => d.scrollWidth <= d.clientWidth + 1), `${lang}: the sheet fits 360`);
      await page.keyboard.press("Escape");
      await page.locator("#modal").waitFor({ state: "hidden" });

      await page.locator('.row.agent[data-id="claude"] .ag-handle').click();
      await page.getByRole("menuitem", { name: w.sheet.replace("Codex", "Claude Code") }).click();
      await sheet(page).waitFor();
      assert.deepEqual(await shownIn(page, "#modal .editor.agent-prefs"), ["fullContextRow"]);
      await page.keyboard.press("Escape");
      await page.locator("#modal").waitFor({ state: "hidden" });

      await page.locator('.row.agent[data-id="grok"] .ag-handle').click();
      await page.getByRole("menuitem", { name: w.sheet.replace("Codex", "Grok Build") }).click();
      await sheet(page).waitFor();
      assert.deepEqual(await shownIn(page, "#modal .editor.agent-prefs"), ["fullContextRow"], `${lang}: Grok Build's sheet`);
      assert(await page.locator("#modal .dialog").evaluate((d) => d.scrollWidth <= d.clientWidth + 1), `${lang}: Grok Build's sheet fits 360`);
      await page.keyboard.press("Escape");
      await page.locator("#modal").waitFor({ state: "hidden" });
      for (const id of CODEX) assert.equal(await page.locator(`#${id}`).count(), 1, `${lang}: ${id} back`);
      assert.deepEqual(errors, []);
    });
  }

  test(`${engine}: gateway mode shows none of an agent's settings`, async (t) => {
    const { page, errors } = await newPage(engine, t, 1100, "en", { gateway: true });
    // it has no Agents page: asked for, the Providers page shows
    await page.goto("http://magpie.test/?view=agents");
    await page.locator("#view-providers").waitFor();
    assert.equal(await page.locator("#view-agents").isVisible(), false, "no Agents page");
    assert.equal(await page.locator(".ag-prefs, .agent-prefs-open").count(), 0, "no rows, no line");
    await page.goto("http://magpie.test/?view=settings");
    await page.locator("#setQ").fill("Codex");
    assert.equal(await page.locator("#agentPrefLinks .agent-pref-link").count(), 0, "no link rows");
    assert.deepEqual(errors, []);
  });

  // Long conversations with nothing here to hold it: in Settings › General,
  // showing the size set before, and a new size is saved from there
  for (const [what, opts] of [["gateway mode", { gateway: true }], ["no Codex or Claude Code", { none: true }]]) {
    test(`${engine}: ${what} keeps Long conversations in Settings`, async (t) => {
      const posts = [];
      const { page, errors } = await newPage(engine, t, 1100, "en", { ...opts, posts });
      await page.goto("http://magpie.test/?view=settings&tab=general");
      const row = page.locator("#setPage-general #fullContextRow");
      await row.waitFor();
      for (const id of CODEX.filter((id) => id !== "fullContextRow")) assert.equal(await page.locator(`#view-settings #${id}`).count(), 0, `${id} not in Settings`);
      const segs = row.locator("#fullContextSegs");
      assert.match(await segs.locator(".opt.on").textContent(), /200K/i, "the size set before");
      const num = segs.locator("input.compact-num");
      assert.match(await num.inputValue(), /200k/i);
      await num.fill("150k");
      await num.press("Enter");
      await num.blur();
      await page.waitForFunction(() => document.querySelector("#fullContextSegs .opt.on")?.textContent.includes("150"));
      assert.deepEqual(posts.at(-1), { on: false, at: 150000 });
      assert.deepEqual(errors, []);
    });
  }
}
