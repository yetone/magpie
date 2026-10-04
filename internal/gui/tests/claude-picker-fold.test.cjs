// Run with Node's test runner and Playwright on the module path; see README.md.
// Claude Code's model picker had every model twice (#496): its own, and
// the same models through magpie on the Claude subscription added there,
// which is the very account Claude Code is signed in to; and models.dev's
// alias and dated id of one model each had a row (claude-opus-4-5,
// claude-opus-4-5-20251101). The rows on its own account now fold into
// one, opened at a click without the page moving; an alias and its dated
// id are one row, the alias's, or the dated one's while that is the value
// set. In English and Chinese. No backend: the API is faked here.
// The fold row ends Claude Code's own rows, though the account comes after
// the providers the user added (where it was, Claude Code was headed twice);
// a click on it leaves the keys to the filter (focus went to the page, and
// Esc and the arrows did nothing); a query shows the rows it finds, so an id
// typed in full is picked with Enter (which only opened the fold); and a
// star on a dated id still shows (its row was gone, Favorites empty). The
// picker is the one beside the connected row's switch.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const own = (value, note, alias) => ({ value, note, icon: "claude-color", group: "Claude Code", direct: "Anthropic", ...(alias ? { alias } : {}) });
const via = (value, label, alias) => ({ value, label, note: "me@example.com · via magpie", icon: "claudecode-color", group: "Claude Code",
  ref: value.replace("[1m]", ""), same: true, ...(alias ? { alias } : {}) });
// in the backend's order: Claude Code's own, the providers the user added,
// then the signed-in accounts (provider.All)
const options = [
  own("claude-opus-5-5", "Claude Opus 5.5"),
  own("claude-opus-4-5", "Claude Opus 4.5 (latest)"),
  own("claude-opus-4-5-20251101", "Claude Opus 4.5", "claude-opus-4-5"),
  own("claude-sonnet-4-5", "Claude Sonnet 4.5 (latest)"),
  own("claude-sonnet-4-5-20250929", "Claude Sonnet 4.5", "claude-sonnet-4-5"),
  { value: "deepseek/deepseek-v4", label: "DeepSeek V4", note: "DeepSeek · via magpie", icon: "deepseek-color", group: "DeepSeek", ref: "deepseek/deepseek-v4" },
  via("claude/claude-opus-5-5[1m]", "Claude Opus 5.5"),
  via("claude/claude-opus-4-5", "Claude Opus 4.5 (latest)"),
  via("claude/claude-opus-4-5-20251101", "Claude Opus 4.5", "claude/claude-opus-4-5"),
];
const state = (value) => ({
  agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color", path: "/test/settings.json", wired: true,
    fields: [{ key: "model", label: "model", value, options }] }],
  profiles: [],
});

function server(lang, value, sets) {
  let cur = state(value);
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { ...cur, settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/set") {
      const body = req.postDataJSON();
      sets.push(body);
      cur = state(body.value);
      return route.fulfill({ json: { ...cur, settings: { lang, theme: "light" } } });
    }
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/agents/cli") return route.fulfill({ json: { agents: {}, pending: false } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const row = '.row.agent[data-id="claude"]';
const words = {
  en: { two: "2 models via magpie", one: "1 model via magpie", whose: "me@example.com · the account Claude Code is signed in to" },
  zh: { two: "2 个模型经 magpie", one: "1 个模型经 magpie", whose: "me@example.com · 即 Claude Code 自己登录的账号" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": Claude Code's picker shows each model once", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    const open = async (lang, value, sets = [], favorites) => {
      const context = await browser.newContext({ viewport: { width: 980, height: 640 } });
      if (favorites) await context.addInitScript((f) => localStorage.setItem("magpie.modelFavorites", JSON.stringify(f)), favorites);
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang, value, sets));
      await page.goto("http://magpie.test/");
      await page.locator(row).waitFor();
      await page.locator(`${row} > .field.ag-start[data-key="model"]`).click();
      await page.locator("#pop:not([hidden]) #list li").first().waitFor();
      await page.waitForTimeout(400); // the picker grows open
      return page;
    };
    // the rows as drawn: name, note, and whether it's the value set
    const rows = (page) => page.locator("#list li:not(.group)").evaluateAll((ls) => ls.map((l) => ({
      v: l.querySelector(".v")?.textContent, n: l.querySelector(".n")?.textContent || "",
      cur: l.classList.contains("cur"), fold: l.classList.contains("fold"),
    })));

    for (const lang of ["en", "zh"]) {
      const w = words[lang];
      await t.test(lang + ": folded, opened at a click", async () => {
        const sets = [];
        const page = await open(lang, "claude-opus-5-5", sets);
        let list = await rows(page);
        const names = list.map((r) => r.v);
        // an alias and its dated id: the alias alone
        assert(names.includes("claude-opus-4-5") && !names.includes("claude-opus-4-5-20251101"), names.join(", "));
        assert(names.includes("claude-sonnet-4-5") && !names.includes("claude-sonnet-4-5-20250929"), names.join(", "));
        // Claude Code's own account through magpie: one folded row
        assert(!names.some((n) => /^Claude Opus/.test(n || "")), names.join(", "));
        const fold = list.filter((r) => r.fold);
        assert.equal(fold.length, 1, names.join(", "));
        assert.equal(fold[0].v, w.two);
        assert.equal(fold[0].n, w.whose);
        assert(names.includes("DeepSeek V4"), "another provider's stay unfolded");
        // after Claude Code's own rows, before the providers the user added:
        // Claude Code is headed once
        assert.deepEqual(await page.locator("#list li.group").allTextContents(), ["Claude Code", "DeepSeek"]);
        assert.equal(await page.locator("#list li.fold + li.group").textContent(), "DeepSeek");
        const foldRow = page.locator("#list li.fold");
        assert.equal(await foldRow.getAttribute("aria-expanded"), "false");

        const view = page.locator("#view-agents");
        const before = { view: await view.evaluate((v) => v.scrollTop), win: await page.evaluate(() => scrollY), box: await foldRow.boundingBox() };
        await foldRow.click();
        assert.equal(await page.locator("#pop:not([hidden])").count(), 1, "the picker stays open");
        assert.equal(await foldRow.getAttribute("aria-expanded"), "true");
        list = await rows(page);
        // opened: the two through magpie, the dated one still one with its alias
        assert.deepEqual(list.filter((r) => /^Claude Opus/.test(r.v)).map((r) => r.v), ["Claude Opus 5.5", "Claude Opus 4.5 (latest)"]);
        assert.equal(await view.evaluate((v) => v.scrollTop), before.view, "the click moved the page");
        assert.equal(await page.evaluate(() => scrollY), before.win, "the click moved the page");
        assert(Math.abs((await foldRow.boundingBox()).y - before.box.y) <= 2, "the row stays under the pointer");

        // closed again, then one picked through magpie after opening it
        await foldRow.click();
        assert(!(await rows(page)).some((r) => /^Claude Opus/.test(r.v)));
        await foldRow.click();
        await page.locator("#list li").filter({ has: page.locator(".v", { hasText: /^Claude Opus 5\.5$/ }) }).click();
        await page.waitForFunction(() => document.querySelector("#pop").hidden);
        assert.deepEqual(sets, [{ agent: "claude", field: "model", value: "claude/claude-opus-5-5[1m]" }]);
        // set: it is its own row, current, and the rest fold again
        await page.locator(`${row} .field[data-key="model"]`).click();
        await page.locator("#pop:not([hidden]) #list li").first().waitFor();
        list = await rows(page);
        // after Default and, connected, the way back
        assert.deepEqual(list[2], { v: "Claude Opus 5.5", n: "me@example.com", cur: true, fold: false }, JSON.stringify(list));
        // "via magpie" is said once, as its tag, beside Claude Code's own marked direct (#726)
        assert.equal(await page.locator("#list li.cur .badge.path.via").textContent(), lang === "en" ? "via magpie" : "经 magpie");
        assert.equal(await page.locator("#list li:not(.cur)", { hasText: "claude-opus-5-5" }).first().locator(".badge.path.direct").count(), 1);
        assert.equal(list.filter((r) => r.fold).map((r) => r.v).join(), w.one);
        await page.context().close();
      });

      await t.test(lang + ": a dated id set before still shows as set", async () => {
        const page = await open(lang, "claude-opus-4-5-20251101");
        assert.equal(await page.locator(`${row} .field[data-key="model"] .v`).textContent(), "claude-opus-4-5-20251101");
        const list = await rows(page);
        const names = list.map((r) => r.v);
        assert.deepEqual(list.filter((r) => r.cur).map((r) => r.v), ["claude-opus-4-5-20251101"]);
        assert(!names.includes("claude-opus-4-5"), "its alias is the same row: " + names.join(", "));
        await page.context().close();
      });

      await t.test(lang + ": a click on the fold leaves the keys to the filter", async () => {
        const page = await open(lang, "claude-opus-5-5");
        await page.locator("#list li.fold").click();
        assert.equal(await page.evaluate(() => document.activeElement?.id), "q");
        await page.keyboard.press("ArrowDown");
        assert.equal(await page.locator("#list li.sel .v").textContent(), "Claude Opus 5.5", "the arrow went on from the fold");
        await page.keyboard.press("Escape");
        assert(await page.evaluate(() => document.querySelector("#pop").hidden), "Esc closes the picker");
        await page.context().close();
      });

      await t.test(lang + ": a query shows the rows it finds, an id typed in full is Enter's", async () => {
        const sets = [];
        const page = await open(lang, "claude-opus-5-5", sets);
        await page.locator("#q").fill("Claude Opus 5.5");
        const list = await rows(page);
        assert(!list.some((r) => r.fold), JSON.stringify(list));
        assert(list.some((r) => r.v === "Claude Opus 5.5" && r.n === "me@example.com"), JSON.stringify(list));
        await page.locator("#q").fill("claude/claude-opus-5-5[1m]");
        await page.keyboard.press("Enter");
        await page.waitForFunction(() => document.querySelector("#pop").hidden);
        assert.deepEqual(sets, [{ agent: "claude", field: "model", value: "claude/claude-opus-5-5[1m]" }]);
        await page.context().close();
      });

      await t.test(lang + ": a star on a dated id still shows", async () => {
        const page = await open(lang, "claude-opus-5-5", [], ["claude-opus-4-5-20251101", "claude/claude-opus-4-5-20251101"]);
        // the dated one's row is the two's
        const names = (await rows(page)).map((r) => r.v);
        assert(names.includes("claude-opus-4-5-20251101") && !names.includes("claude-opus-4-5"), names.join(", "));
        await page.locator('#pickerRail .rail-item[data-group="favorites"]').click();
        await page.waitForFunction(() => document.querySelectorAll("#list li:not(.group)").length === 2);
        assert.deepEqual((await rows(page)).map((r) => r.v), ["claude-opus-4-5-20251101", "Claude Opus 4.5"]);
        await page.context().close();
      });
    }
    assert.deepEqual(errors, []);
  });
}
