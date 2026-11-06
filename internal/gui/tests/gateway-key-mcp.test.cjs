// The MCP servers and tools a gateway key may use (wakaka on Discord):
// picked in the app's own menu, opened from the key row's menu and from
// the key's badge once it holds some, a server whole or some of its
// tools, sent as "mcp-key" when the menu closes; "All MCP servers" takes
// the restriction off, "No MCP server" holds it to none.
const assert = require("node:assert/strict");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { fixture } = require("./fixtures/caller-keys.cjs");

const words = {
  en: { item: "MCP servers", label: "MCP servers this key may use", all: "All MCP servers", two: "2 MCP servers", none: "No MCP server", only: /Only these MCP servers and tools: github, magpie-web-search\/web_fetch/ },
  zh: { item: "MCP 服务器", label: "这个密钥可用的 MCP 服务器", all: "全部 MCP 服务器", two: "2 个 MCP 服务器", none: "无 MCP 服务器", only: /仅限这些 MCP 服务器和工具/ },
  "zh-TW": { item: "MCP 伺服器", label: "這個金鑰可用的 MCP 伺服器", all: "全部 MCP 伺服器", two: "2 個 MCP 伺服器", none: "無 MCP 伺服器", only: /僅限這些 MCP 伺服器和工具/ },
  ja: { item: "MCP サーバー", label: "このキーが使える MCP サーバー", all: "すべての MCP サーバー", two: "2 個の MCP サーバー", none: "MCP サーバーなし", only: /この MCP サーバーとツールのみ/ },
  de: { item: "MCP-Server", label: "MCP-Server, die dieser Schlüssel nutzen darf", all: "Alle MCP-Server", two: "2 MCP-Server", none: "Kein MCP-Server", only: /Nur diese MCP-Server und Werkzeuge/ },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [1100, 420]) {
    for (const [lang, w] of Object.entries(words)) {
      test(`${engine} ${width} ${lang}: a gateway key is held to the MCP servers and tools picked for it`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(6000);
        page.setDefaultNavigationTimeout(60000);
        const events = [], errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", fixture(lang, "light", events, { lan: true, keyHolds: { server: { mcp: ["github", "magpie-web-search/web_fetch"] } } }));
        await page.goto("http://magpie.test/?view=gateway");
        await page.locator("#gatewayKeys .acc[data-key]").last().waitFor();
        const row = (id) => page.locator(`#gatewayKeys .acc[data-key="${id}"]`);
        const badge = (id) => row(id).getByRole("button", { name: w.label, exact: true });
        // a held key says how many servers; a free one has no badge, not
        // even on hover, and is picked from its row's menu
        await page.mouse.move(0, 0);
        assert.equal((await badge("server").textContent()).trim(), w.two);
        assert.match(await badge("server").getAttribute("title"), w.only);
        await row("work").hover();
        assert.equal(await row("work").locator(".key-mcp").count(), 0);
        const view = page.locator("#view-gateway");
        const scrolled = async () => [await view.evaluate((v) => v.scrollTop), await page.evaluate(() => window.scrollY)];
        const at = await scrolled();
        await row("work").locator(".gw-key-more").click();
        await page.locator(".row-menu").getByRole("menuitem", { name: w.item, exact: true }).click();
        const menu = page.locator(".proto-menu");
        await menu.waitFor();
        assert.equal(await page.locator("select").count(), 0, "no native select");
        assert.equal((await menu.locator(".pm-head").textContent()).trim(), w.label);
        assert.equal(await menu.locator(".pm-item.on").count(), 1, "All MCP servers is ticked for a free key");
        // a server whole, and one tool of another; sent when the menu closes
        await menu.locator('.pm-item:has(.pm-name:text-is("Magpie Browser"))').click();
        await menu.locator('.pm-item:has(.pm-note:text-is("github/create_issue"))').click();
        assert.equal(events.filter((e) => e.action === "mcp-key").length, 0, "a pick was sent before the menu closed");
        await page.keyboard.press("Escape");
        await menu.waitFor({ state: "detached" });
        assert.deepEqual(events.filter((e) => e.action === "mcp-key").map((e) => e.body), [{ key: "work", mcp: ["magpie-browser", "github/create_issue"] }]);
        await page.waitForFunction(() => document.querySelector('#gatewayKeys .acc[data-key="work"] .key-mcp.set'));
        assert.equal((await badge("work").textContent()).trim(), w.two);
        // "No MCP server" ticked beside the key's servers holds it to none
        await row("server").hover();
        await badge("server").click();
        await menu.waitFor();
        assert.equal(await menu.locator(".pm-item.on").count(), 2);
        await menu.locator(".pm-item", { hasText: w.none }).click();
        await page.keyboard.press("Escape");
        await menu.waitFor({ state: "detached" });
        assert.deepEqual(events.filter((e) => e.action === "mcp-key").at(-1).body, { key: "server", mcp: ["-"] });
        await page.waitForFunction((none) => document.querySelector('#gatewayKeys .acc[data-key="server"] .key-mcp')?.textContent.trim() === none, w.none);
        assert.equal((await badge("server").textContent()).trim(), w.none);
        // All MCP servers takes it off
        await badge("server").click();
        await menu.waitFor();
        await menu.locator(".pm-item", { hasText: w.all }).first().click();
        await menu.waitFor({ state: "detached" });
        assert.deepEqual(events.filter((e) => e.action === "mcp-key").at(-1).body, { key: "server", mcp: [] });
        await page.waitForFunction(() => !document.querySelector('#gatewayKeys .acc[data-key="server"] .key-mcp'));
        assert.deepEqual(await scrolled(), at, "a click moved the page");
        // the row still fits
        const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
        assert.ok(over <= 0, `the page scrolls sideways by ${over}px`);
        const lefts = await page.locator("#gatewayKeys .key-mcp").evaluateAll((els) => els.map((e) => getComputedStyle(e).borderLeftStyle));
        assert(lefts.every((s) => s === "none"), "a coloured left border");
        assert.deepEqual(errors, []);
      });
    }
  }
}
