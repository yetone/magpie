// Run with Node's test runner and Playwright on the module path; see README.md.
// Claude Code can't keep max in settings.json, so magpie puts it in Claude
// Code's environment (CLAUDE_CODE_EFFORT_LEVEL), which outranks /effort and
// /model's slider (@daix91 on X). With max set, New sessions' effort in the
// opened row says so in its title; another level picked takes the line away
// at once and in the row drawn again from the state, and max picked puts it back. The
// window, wide and narrow (no sideways scroll), in every language,
// Chromium and WebKit. No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const five = ["low", "medium", "high", "xhigh", "max"].map((value) => ({ value }));
const claude = () => ({
  id: "claude", name: "Claude Code", path: "/test/settings.json", icon: "claudecode-color", wired: true,
  fields: [
    { key: "model", label: "model", value: "claude-opus-5-5", options: [{ value: "claude-opus-5-5", icon: "claude-color", ref: "claude/claude-opus-5-5" }] },
    { key: "effort", label: "effort", value: "max", options: five },
  ],
});

function server(lang, sets) {
  let cur = { agents: [claude()], profiles: [] };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { ...cur, settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/set") {
      const body = req.postDataJSON();
      sets.push(body);
      cur = JSON.parse(JSON.stringify(cur));
      cur.agents.find((a) => a.id === body.agent).fields.find((f) => f.key === body.field).value = body.value;
      return route.fulfill({ json: { ...cur, settings: { lang, theme: "light" } } });
    }
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/agents/cli") return route.fulfill({ json: { agents: {}, pending: false } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// the line as i18n.js has it
const MAX = {
  en: "Max is fixed in Claude Code's environment, so /effort can't change it. Pick another level to get /effort back.",
  zh: "“最高”写在 Claude Code 的环境变量里，/effort 改不了它。选其他强度即可恢复 /effort。",
  "zh-TW": "「最高」寫在 Claude Code 的環境變數裡，/effort 改不了它。選其他強度即可恢復 /effort。",
  ja: "「最大」は Claude Code の環境変数で固定されるため、/effort では変更できません。別のレベルを選ぶと /effort が使えるようになります。",
  de: "Maximal ist in der Umgebung von Claude Code festgelegt, /effort kann es nicht ändern. Wähle eine andere Stufe, um /effort wieder zu nutzen.",
};
const cc = '.row.agent[data-id="claude"]';
// the slider's stop from its top: max is 0, high 2 (Default is its first)
const pickAt = (page, i) => page.locator("#effortRange").evaluate((r, i) => {
  r.value = String(Number(r.max) - i);
  r.dispatchEvent(new Event("input", { bubbles: true }));
  r.dispatchEvent(new Event("change", { bubbles: true }));
}, i);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(MAX)) {
    for (const width of [1100, 560]) {
      test(`${engine} ${lang} ${width}px: Claude Code's max effort says /effort can't change it`, async (t) => {
        const line = MAX[lang];
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await browser.newPage({ viewport: { width, height: 700 } });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], sets = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, sets));
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-claude-effort-max.png`) });
          }
          await browser.close();
        });
        await page.goto("http://magpie.test/");
        await page.locator(cc).waitFor();
        await page.locator(`${cc} .ag-link`).click();
        await page.locator(`${cc} .ag-exp`).waitFor();

        const eff = page.locator(`${cc} .ag-exp .field[data-key="effort"]`);
        const title = () => eff.getAttribute("title");
        // max: the line, once, as the title's last
        assert.ok((await title()).endsWith("\n" + line), await title());
        assert.equal((await title()).split(line).length, 2);
        // no new words on the page: the line is in the title only
        assert.ok(!(await page.locator(cc).innerText()).includes(line));
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "the Agents view scrolls sideways");

        // another level: the line goes at once, and stays gone in the row drawn again
        await eff.click();
        await page.locator("#effortControl:not([hidden])").waitFor();
        await pickAt(page, 2);
        assert.equal(await page.locator("#effortValue").textContent(), { en: "high", zh: "高", "zh-TW": "高", ja: "高", de: "hoch" }[lang] ?? null);
        assert.ok(!(await title()).includes(line), await title());
        assert.ok((await title()).includes("high"), await title());
        await page.waitForFunction(() => document.querySelector("#status")?.textContent);
        assert.deepEqual(sets, [{ agent: "claude", field: "effort", value: "high" }]);
        await page.keyboard.press("Escape");
        await page.reload();
        await page.locator(`${cc} .ag-link`).click();
        await page.locator(`${cc} .ag-exp`).waitFor();
        assert.ok((await title()).includes("high"), await title());
        assert.ok(!(await title()).includes(line), await title());

        // max again: the line is back
        await eff.click();
        await page.locator("#effortControl:not([hidden])").waitFor();
        await pickAt(page, 0);
        assert.ok((await title()).endsWith("\n" + line), await title());
        assert.equal((await title()).split(line).length, 2);
        assert.deepEqual(errors, []);
      });
    }
  }
}
