// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings' Web search section: which provider searches for a model that
// can't (01huadalang on Discord). The row shows magpie's own pick while none
// is named; the picker offers Automatic, Off, each provider that can search by
// its small model, and each of its models, including a relay said to search;
// a pick is saved as searcher ("<provider>" or "<provider>/<model>") and shown;
// a provider's small model is offered once, not again among its models, and
// one saved by name is ticked as it (Player on Discord); no "via magpie" tag,
// as no agent asks for these;
// one named that magpie can't use (turned off) is said in the row, magpie's pick shown instead;
// relays said to search are manual-only, including during fallback; a Kimi Code plan, which
// searches by its web search with no model, is offered by itself and said to search for its
// own models first. Another setting saved
// keeps the pick (prefsKeep). No click moves the page. English and Chinese, Chromium and
// WebKit, with the API faked. Search help covers costs for every provider and
// explains current eligibility without claiming a provider can never search.
// The help is also checked in Japanese and German, whole in the hover card
// at every width (the owner, 11-02: a row's explanation rests out of sight;
// before, it wrapped whole beside the picker, arculongus on Discord: cut
// with an ellipsis on the desktop, it couldn't be read).
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn, reach, pickItem } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const choices = [
  { id: "claude", name: "Claude", icon: "claude", small: "claude-haiku-4-5", models: [
    { id: "claude/claude-haiku-4-5", name: "Claude Haiku 4.5", provider: "claude", providerName: "Claude" },
    { id: "claude/claude-opus-4-5", name: "Claude Opus 4.5", provider: "claude", providerName: "Claude" }] },
  { id: "openai", name: "OpenAI", icon: "openai", small: "gpt-5-mini", smallName: "GPT-5 Mini Named", models: [
    { id: "openai/gpt-5-mini", name: "GPT-5 mini", provider: "openai", providerName: "OpenAI" },
    { id: "openai/gpt-5.5", name: "GPT Five Five", provider: "openai", providerName: "OpenAI" }] },
  { id: "relay", name: "MyRelay", icon: "generic", small: "claude-haiku-4-5", models: [
    { id: "relay/claude-haiku-4-5", name: "Claude Haiku 4.5", provider: "relay", providerName: "MyRelay" }] },
  { id: "kimi", name: "Kimi Code", icon: "kimi", small: "", models: [], service: true },
  { id: "antigravity", name: "Antigravity", icon: "antigravity", small: "gemini-3-flash", own: true, models: [
    { id: "antigravity/gemini-3-flash", name: "Gemini 3 Flash", provider: "antigravity", providerName: "Antigravity" }] },
];
const words = {
  en: { name: "Searches for other models", off: "Off", auto: "Automatic", small: "GPT-5 Mini Named, its small model", unused: "isn't used: it is turned off",
    own: "A Kimi Code plan (Kimi Code) searches for its own models first, with its web search; for other models only when named here", web: "its web search",
    google: "Antigravity search for their own models first, with Gemini's Google Search",
    more: "A1, A2, A3, A4, A5 and 3 more." },
  zh: { name: "代搜供应商", off: "关闭", auto: "自动", small: "GPT-5 Mini Named（它的小模型）", unused: "没有用 OpenAI · GPT-5 Mini Named：它已关闭",
    own: "Kimi Code 套餐（Kimi Code）的模型优先用套餐自带的联网搜索；其他模型仅在此处选中时使用", web: "它自带的联网搜索",
    google: "Antigravity 的模型优先用 Gemini 自带的 Google 搜索",
    more: "A1, A2, A3, A4, A5 以及另外 3 个。" },
};
const helpWords = {
  en: {
    help: "When a model can't search the web directly, the selected provider searches for it and returns the results. Searches may use the service's quota or incur charges; if the provider magpie picks fails, it tries other available sources, and a provider you select here gives way to the ones you add after it, in their order, then, when it can't search at all (out of quota or rate-limited), to other available sources, then to your Search APIs.",
    relays: "These relays must be selected manually and are not used for automatic selection or fallback: MyRelay.",
    left: "These providers can't be selected to search for other models with the current configuration: MiniMax, Kimi For Coding. Their models can still get search results through other available search providers or configured search APIs.",
  },
  zh: {
    help: "当模型无法直接联网搜索时，由所选供应商代为搜索并返回结果。代搜可能消耗所用服务的额度或产生费用；magpie 自动选择的供应商失败时会尝试其他可用来源，在此手动选中的供应商失败时会按顺序换成你在它后面添加的供应商；若它完全搜不了（额度用完或被限流），再尝试其他可用来源，最后改用你配置的搜索 API。",
    relays: "以下中转服务需手动指定，不参与自动选择或自动回退：MyRelay。",
    left: "以下供应商在当前配置下不可选为代搜供应商：MiniMax, Kimi For Coding。它们的模型仍可通过其他可用的代搜供应商或已配置的搜索 API 获取搜索结果。",
  },
  ja: {
    help: "モデルが直接ウェブ検索できない場合、選択したプロバイダが代わりに検索し、結果を返します。検索にはサービスの利用枠を消費したり、料金が発生したりする場合があります。magpie が自動で選んだプロバイダが失敗すると他の利用可能な検索元を試しますが、ここで選んだプロバイダが失敗すると、その後ろに追加したプロバイダをその順番で試し、利用枠切れやレート制限でまったく検索できないときは他の利用可能な検索元も試し、最後に設定済みの検索 API を使います。",
    relays: "次の中継サービスは手動で選択する必要があり、自動選択や自動フォールバックの対象にはなりません：MyRelay。",
    left: "次のプロバイダは現在の設定では他のモデルの検索用に選択できません：MiniMax, Kimi For Coding。これらのモデルも、他の利用可能な検索用プロバイダや設定済みの検索 API を通じて検索結果を取得できます。",
  },
  de: {
    help: "Wenn ein Modell nicht direkt im Web suchen kann, übernimmt der ausgewählte Anbieter die Suche und liefert die Ergebnisse. Suchen können das Kontingent des Dienstes verbrauchen oder Kosten verursachen. Schlägt der von magpie gewählte Anbieter fehl, versucht magpie andere verfügbare Quellen; ein hier ausgewählter Anbieter wird zuerst durch die Anbieter ersetzt, die Sie danach hinzufügen, in ihrer Reihenfolge; kann er gar nicht suchen (Kontingent aufgebraucht oder Ratenlimit), dann durch andere verfügbare Quellen und zuletzt durch Ihre Such-APIs.",
    relays: "Diese Relays müssen manuell ausgewählt werden und werden weder automatisch noch als Fallback gewählt: MyRelay.",
    left: "Diese Anbieter können mit der aktuellen Konfiguration nicht für die Suche anderer Modelle ausgewählt werden: MiniMax, Kimi For Coding. Ihre Modelle können weiterhin über andere verfügbare Suchanbieter oder konfigurierte Such-APIs Suchergebnisse erhalten.",
  },
};

function serve(lang, posted, st) {
  const settings = () => ({ lang, theme: st.theme || "light", searchVendors: [], searchAPIs: st.apis || [], searchFirst: st.first || "", searchChoices: choices,
    searchAuto: "Claude · claude-haiku-4-5", searchProvider: st.searcher === "off" ? "" : st.unused || !st.searcher ? "Claude · claude-haiku-4-5" : st.searcher,
    searchRelays: st.relays || ["MyRelay"], searchLeftOut: st.leftOut || ["MiniMax", "Kimi For Coding"], searcher: st.searcher, searchUnused: st.unused, searchFailed: st.failed });
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data, status = 200) => r.fulfill({ status, json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = ${JSON.stringify({ lang, theme: st.theme || "light", web: true })};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: settings() });
    if (url.pathname === "/api/settings") {
      if (r.request().method() === "POST") {
        const b = JSON.parse(r.request().postData());
        posted.push(b);
        st.searcher = b.searcher || "";
      }
      return json(settings());
    }
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    await (body ? r.fulfill({ body, contentType }) : r.fulfill({ status: 404, body: "" }));
  };
}

// The searcher picked that didn't search last time is said first in the
// row, ahead of the one-line help's ellipsis: no other provider searched in
// its place (Gearclickist on Discord: Grok picked answered without
// searching, and Codex's gpt-6-astra searched instead, unasked).
const failedWords = {
  en: { notSearched: /^GPT Five Five · OpenAI didn't search .+: it answered without searching the web, so its account or plan may not offer web search\. No other provider searched in its place; pick Automatic or another provider, or add a Search API\.$/,
    error: /^GPT Five Five · OpenAI didn't search .+: 400 search quota used up\. No other provider/ },
  zh: { notSearched: /^GPT Five Five · OpenAI .+没能搜索：它没有联网搜索就直接回答了，它的账号或套餐可能不支持联网搜索。没有换用其他供应商代搜；可改选“自动”或其他供应商，或添加搜索 API。$/,
    error: /^GPT Five Five · OpenAI .+没能搜索：400 search quota used up。/ },
  "zh-TW": { notSearched: /^GPT Five Five · OpenAI .+沒能搜尋：它沒有聯網搜尋就直接回答了，它的帳號或方案可能不支援聯網搜尋。沒有換用其他供應商代搜；可改選「自動」或其他供應商，或新增搜尋 API。$/,
    error: /^GPT Five Five · OpenAI .+沒能搜尋：400 search quota used up。/ },
  ja: { notSearched: /^GPT Five Five · OpenAI は.+、検索できませんでした：ウェブ検索をせずに回答しました。アカウントやプランがウェブ検索に対応していない可能性があります。他のプロバイダが代わりに検索することはありません。「自動」か別のプロバイダを選ぶか、検索 API を追加してください。$/,
    error: /^GPT Five Five · OpenAI は.+、検索できませんでした：400 search quota used up。/ },
  de: { notSearched: /^GPT Five Five · OpenAI hat .+ nicht gesucht: es hat ohne Websuche geantwortet, daher bietet sein Konto oder Tarif möglicherweise keine Websuche\. Kein anderer Anbieter hat an seiner Stelle gesucht; wählen Sie Automatisch oder einen anderen Anbieter, oder fügen Sie eine Such-API hinzu\.$/,
    error: /^GPT Five Five · OpenAI hat .+ nicht gesucht: 400 search quota used up\. Kein anderer Anbieter/ },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(failedWords)) {
    test(`${engine} ${lang}: the picked searcher that didn't search is said first`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const posted = [];
      const at = new Date(Date.now() - 5 * 60e3).toISOString();
      const st = { searcher: "openai/gpt-5.5", unused: "", failed: { searcher: "openai/gpt-5.5", notSearched: true, at } };
      await page.route("**/*", serve(lang, posted, st));
      const w = failedWords[lang];
      for (const width of [560, 1000]) {
        for (const [kind, failed] of [["notSearched", { searcher: "openai/gpt-5.5", notSearched: true, at }], ["error", { searcher: "openai/gpt-5.5", why: "400 search quota used up", at }]]) {
          st.failed = failed;
          await page.mouse.move(0, 0);
          await page.setViewportSize({ width, height: 900 });
          await watchSettingsReads(page);
          await page.goto("http://magpie.test/?view=settings&tab=models");
          await settingsDrawn(page);
          const row = page.locator("#searchList .searcher-row");
          await row.waitFor();
          const note = row.locator(".sub .searcher-failed");
          assert.match(await note.innerText(), w[kind], `${width} ${kind}`);
          const where = await row.evaluate((r) => {
            const sub = r.querySelector(".sub"), n = sub.querySelector(".searcher-failed");
            const a = sub.getBoundingClientRect(), b = n.getBoundingClientRect();
            return { first: sub.firstChild === n, starts: b.left >= a.left - 1 && b.left < a.right && b.top < a.bottom };
          });
          assert(where.first && where.starts, `${width} ${kind}: ${JSON.stringify(where)}`);
        }
      }
      // turned off, or the one named not used: no such note
      st.unused = "off";
      await watchSettingsReads(page);
      await page.goto("http://magpie.test/?view=settings&tab=models");
      await settingsDrawn(page);
      await page.locator("#searchList .searcher-unused").waitFor();
      assert.equal(await page.locator("#searchList .searcher-failed").count(), 0);
      assert.deepEqual(posted, [], "reading the note must not change settings");
      assert.deepEqual(errors, []);
    });
  }

  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    const h = helpWords[lang];
    test(`${engine} ${lang}: the provider that searches for other models is picked in Settings`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 1000, height: 900 } })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const posted = [];
      const st = { searcher: "", unused: "" };
      await page.route("**/*", serve(lang, posted, st));
      await watchSettingsReads(page);
      await page.goto("http://magpie.test/?view=settings&tab=models");
      await settingsDrawn(page); // rows found are the ones that stay (#1659)
      const row = page.locator("#searchList .row.searcher-row");
      await row.waitFor();
      await row.scrollIntoViewIfNeeded();
      assert.equal(await row.locator(".name").innerText(), w.name);
      assert.equal(await row.locator("button.searcher-pick").innerText(), `${w.auto} · Claude · claude-haiku-4-5`);
      assert((await row.locator(".sub").innerText()).startsWith(h.help), "costs and fallback apply to every search provider");
      assert.equal(await row.locator(".searcher-relays").innerText(), h.relays);
      assert((await row.locator(".sub").innerText()).includes(w.own), "the Kimi Code plan is said to search for its own models");
      assert((await row.locator(".sub").innerText()).includes(w.google), "a Google sign-in is said to search for its own models (#757)");
      assert.equal(await row.locator(".searcher-left-out").innerText(), h.left);
      // a long list is cut short
      st.leftOut = ["A1", "A2", "A3", "A4", "A5", "A6", "A7", "A8"];
      await page.evaluate(() => fetch("/api/settings").then((r) => r.json()).then((s) => { prefs = s; state.settings = s; renderSettings(); }));
      await page.waitForFunction(() => document.querySelector("#searchList .searcher-left-out")?.innerText.includes("A5"));
      const cut = await row.locator(".searcher-left-out").innerText();
      assert(cut.includes(w.more) && !cut.includes("A6"), cut);
      delete st.leftOut;
      await page.evaluate(() => fetch("/api/settings").then((r) => r.json()).then((s) => { prefs = s; state.settings = s; renderSettings(); }));
      await page.waitForFunction(() => document.querySelector("#searchList .searcher-left-out")?.innerText.includes("MiniMax"));
      // the Search APIs come after it
      assert.equal(await page.locator("#searchList .row").nth(1).evaluate((e) => e.classList.contains("search-add")), true);

      const where = () => page.evaluate(() => [scrollX, scrollY, document.scrollingElement.scrollTop, document.querySelector("main")?.scrollTop ?? 0]);
      const click = (loc) => pickItem(page, loc);
      const before = await where();

      // a model of a provider
      await click(row.locator("button.searcher-pick"));
      await page.locator("#pop").waitFor({ state: "visible" });
      const items = await page.locator("#list li:not(.group)").allInnerTexts();
      assert(items[0].includes(w.auto), "Automatic comes first");
      assert(items.some((x) => x.includes("GPT Five Five")) && items.some((x) => x.includes("Claude Opus 4.5")));
      assert(items.some((x) => x.includes("MyRelay")), "a relay said to search can be named");
      // the small model once: GPT-5 mini is OpenAI's, Claude Haiku 4.5 Claude's and MyRelay's
      assert.equal(items.filter((x) => x.includes("GPT-5 mini") || x.includes("GPT-5 Mini Named")).length, 1, items.join(" | "));
      assert.equal(items.filter((x) => x.includes("MyRelay")).length, 1, items.join(" | "));
      assert.equal(items.filter((x) => x.includes("Claude Haiku 4.5")).length, 0, "Claude's and MyRelay's small model only as their small model");
      assert.equal(items.filter((x) => x.startsWith("claude-haiku-4-5")).length, 2, items.join(" | "));
      assert.equal(await page.locator("#list .badge.path").count(), 0, "no agent asks for these: no via magpie");
      await click(page.locator("#list li:not(.group)", { hasText: "GPT Five Five" }));
      await page.waitForFunction(() => document.querySelector("#searchList button.searcher-pick")?.innerText.includes("GPT Five Five"));
      assert.equal(posted.at(-1).searcher, "openai/gpt-5.5");
      assert.equal(await row.locator("button.searcher-pick").innerText(), "GPT Five Five · OpenAI");

      // a provider, by its small model
      await click(row.locator("button.searcher-pick"));
      await page.locator("#pop").waitFor({ state: "visible" });
      await click(page.locator("#list li:not(.group)", { hasText: w.small }));
      await page.waitForFunction(() => document.querySelector("#searchList button.searcher-pick")?.innerText === "OpenAI · GPT-5 Mini Named");
      assert.equal(posted.at(-1).searcher, "openai");

      // a small model saved by name ("<provider>/<small>", as the model row
      // below it once saved) is ticked as the small model, and shown by name
      st.searcher = "openai/gpt-5-mini";
      await page.evaluate(() => fetch("/api/settings").then((r) => r.json()).then((s) => { prefs = s; state.settings = s; renderSettings(); }));
      await page.waitForFunction(() => document.querySelector("#searchList button.searcher-pick")?.innerText === "GPT-5 mini · OpenAI");
      await click(row.locator("button.searcher-pick"));
      await page.locator("#pop").waitFor({ state: "visible" });
      assert.deepEqual(await page.locator("#list li.cur").allInnerTexts().then((x) => x.map((s) => s.includes(w.small))), [true]);
      await page.keyboard.press("Escape");
      await page.locator("#pop").waitFor({ state: "hidden" });

      // Manual-only is not unavailable: selecting a relay still saves and uses it.
      await click(row.locator("button.searcher-pick"));
      await page.locator("#pop").waitFor({ state: "visible" });
      const relay = page.locator("#list li:not(.group)", { hasText: "MyRelay" }).last();
      await relay.scrollIntoViewIfNeeded();
      await click(relay);
      await page.waitForFunction(() => document.querySelector("#searchList button.searcher-pick")?.innerText === "MyRelay · claude-haiku-4-5");
      assert.equal(posted.at(-1).searcher, "relay");
      assert.equal(await row.locator(".searcher-relays").innerText(), h.relays);

      // a Kimi Code plan, by its web search: no model of it is offered
      await click(row.locator("button.searcher-pick"));
      await page.locator("#pop").waitFor({ state: "visible" });
      const kimi = (await page.locator("#list li:not(.group)").allInnerTexts()).filter((x) => x.includes("Kimi Code"));
      assert.equal(kimi.length, 1, "the plan is offered once, with no model");
      assert(kimi[0].includes(w.web));
      const web = page.locator("#list li:not(.group)", { hasText: w.web });
      await web.scrollIntoViewIfNeeded();
      await click(web);
      await page.waitForFunction((want) => document.querySelector("#searchList button.searcher-pick")?.innerText === want, `Kimi Code · ${w.web}`);
      assert.equal(posted.at(-1).searcher, "kimi");

      // back to a provider, by its small model
      await click(row.locator("button.searcher-pick"));
      await page.locator("#pop").waitFor({ state: "visible" });
      await click(page.locator("#list li:not(.group)", { hasText: w.small }));
      await page.waitForFunction(() => document.querySelector("#searchList button.searcher-pick")?.innerText === "OpenAI · GPT-5 Mini Named");

      // another setting saved keeps the pick
      const n = posted.length;
      await page.evaluate(() => savePrefs({ ...prefsKeep(prefs), noStats: true }));
      await page.waitForTimeout(200);
      assert.equal(posted.length, n + 1);
      assert.equal(posted.at(-1).searcher, "openai", "another setting saved sends the pick as it was");

      // the one named turned off: said, and magpie's pick shown
      st.unused = "off";
      await page.evaluate(() => renderSettings && fetch("/api/settings").then((r) => r.json()).then((s) => { prefs = s; state.settings = s; renderSettings(); }));
      await page.waitForFunction(() => document.querySelector("#searchList .searcher-unused"));
      assert((await row.locator(".searcher-unused").innerText()).includes(w.unused));
      assert.equal(await row.locator("button.searcher-pick").innerText(), `${w.auto} · Claude · claude-haiku-4-5`);
      st.unused = "";

      assert.deepEqual(await where(), before, "the clicks moved nothing");
      st.apis = [{ vendor: "searxng", name: "SearXNG", ready: true, url: "https://search.example.com" }];
      st.first = "api";

      // Off survives another save and a reload, suppresses provider-specific
      // help, and still allows selecting Automatic again.
      await click(row.locator("button.searcher-pick"));
      await page.locator("#pop").waitFor({ state: "visible" });
      await click(page.locator("#list li:not(.group)").filter({ hasText: new RegExp(`^${w.off}`) }));
      await page.waitForFunction((off) => document.querySelector("#searchList button.searcher-pick")?.innerText === off, w.off);
      assert.equal(posted.at(-1).searcher, "off");
      await page.evaluate(() => savePrefs({ ...prefsKeep(prefs), noStats: true }));
      assert.equal(posted.at(-1).searcher, "off");
      await page.reload();
      await row.waitFor();
      await row.scrollIntoViewIfNeeded();
      assert.equal(await row.locator("button.searcher-pick").innerText(), w.off);
      assert.equal(await row.locator(".searcher-own, .searcher-unused, .searcher-relays, .searcher-left-out").count(), 0);
      assert.equal(await page.locator("#searchFirstRow").count(), 0);
      assert.equal(await row.locator(".sub").innerText(), lang === "zh"
        ? "关闭供应商代搜后，仅使用已配置的搜索 API；未配置时，Magpie 不添加搜索工具。供应商自带的联网搜索不受影响。"
        : "When provider search is off, only configured Search APIs are used. Without a Search API, magpie does not add a search tool. Providers' native web search is unchanged.");

      // back to Automatic
      const afterReload = await where();
      await click(row.locator("button.searcher-pick"));
      await page.locator("#pop").waitFor({ state: "visible" });
      await click(page.locator("#list li:not(.group)", { hasText: w.auto }).first());
      await page.waitForFunction(() => !document.querySelector("#searchList .searcher-unused") &&
        document.querySelector("#searchList button.searcher-pick")?.innerText.includes("claude-haiku"));
      assert.equal(posted.at(-1).searcher, "");
      assert.deepEqual(await where(), afterReload, "the clicks moved nothing");
      assert.deepEqual(errors, []);
    });
  }
  for (const lang of Object.keys(helpWords)) {
    test(`${engine} ${lang}: search help preserves ellipsis and hover titles`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ reducedMotion: "reduce" })).newPage();
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const posted = [];
      const st = { searcher: "relay/claude-haiku-4-5", unused: "" };
      const h = helpWords[lang];
      await page.route("**/*", serve(lang, posted, st));
      for (const theme of ["light", "dark"]) {
        st.theme = theme;
        for (const width of [560, 1000]) {
          await page.mouse.move(0, 0);
          await page.setViewportSize({ width, height: 900 });
          await watchSettingsReads(page);
          await page.goto("http://magpie.test/?view=settings&tab=models");
          await settingsDrawn(page); // rows found are the ones that stay (#1659)
          const row = page.locator("#searchList .searcher-row");
          await row.waitFor();
          // no title before the pointer has come over the help: asked here,
          // as reach's wheel, from the view's middle, can pass over it (with
          // Image generation timeout drawn above, it does in Japanese)
          assert.equal(await row.locator(".sub").getAttribute("title"), null);
          // under Image recognition's MCP address (6c1c0a747) the row is
          // past the window's foot: the reader scrolls down to it
          await reach(page, row.locator("button.searcher-pick"));
          // Hover retries if Settings' startup refresh replaces the row.
          await row.locator("button.searcher-pick").hover();
          assert((await row.locator(".sub").innerText()).startsWith(h.help));
          assert.equal(await row.locator(".searcher-relays").innerText(), h.relays);
          assert.equal(await row.locator(".searcher-left-out").innerText(), h.left);
          assert.equal(await row.locator("button.searcher-pick").innerText(), "Claude Haiku 4.5 · MyRelay");
          const sub = row.locator(".sub");
          assert.equal(await sub.getAttribute("class"), "sub", "every setting row's line wraps, with no class of its own to opt in");
          // the help explains the row: it rests out of sight and the
          // pointer on the row shows it whole in the card (the owner,
          // 11-02; settings-hints.test.cjs), the hover above showing it
          if (await sub.evaluate((s) => getComputedStyle(s).getPropertyValue("--hint").trim() === "1")) {
            const card = page.locator(".set-hint.on");
            await card.waitFor();
            assert((await card.innerText()).includes(h.help), "the card has the help");
            assert((await card.innerText()).includes(h.relays), "the card has the relays");
            continue;
          }
          const layout = await row.evaluate((r) => {
            const sub = r.querySelector(".sub");
            const b = r.querySelector("button.searcher-pick").getBoundingClientRect();
            const s = sub.getBoundingClientRect(), bounds = r.getBoundingClientRect();
            return { whiteSpace: getComputedStyle(sub).whiteSpace, textOverflow: getComputedStyle(sub).textOverflow,
              overflow: sub.scrollWidth - sub.clientWidth,
              clipped: sub.scrollHeight - sub.clientHeight,
              fits: b.left >= bounds.left && b.right <= bounds.right + 1 &&
                b.top >= bounds.top && b.bottom <= bounds.bottom + 1 &&
                (s.right <= b.left + 1 || s.bottom <= b.top + 1) };
          });
          assert(layout.clipped <= 1 && layout.fits, JSON.stringify(layout));
          assert.equal(layout.whiteSpace, "normal", "the help wraps");
          assert(layout.overflow <= 1, JSON.stringify(layout));
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await row.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${theme}-${width}-search-help.png`) });
          }
        }
      }
      // Costs and fallback still matter when no manual-only or unavailable providers are listed.
      st.relays = [];
      st.leftOut = [];
      st.searcher = "";
      await watchSettingsReads(page);
      await page.goto("http://magpie.test/?view=settings&tab=models");
      await settingsDrawn(page); // rows found are the ones that stay (#1659)
      const row = page.locator("#searchList .searcher-row");
      await row.waitFor();
      assert((await row.locator(".sub").innerText()).startsWith(h.help));
      assert.equal(await row.locator(".searcher-relays, .searcher-left-out").count(), 0);
      assert.deepEqual(posted, [], "reading help must not change settings");
      assert.deepEqual(errors, []);
    });
  }
}
