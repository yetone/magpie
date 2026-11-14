// Run with Node's test runner and Playwright on the module path; see README.md.
// normalcoder_ on X, on Usage › Overview:
//
// Cards: a provider's accounts were told apart by a hairline alone ("账号与
// 账号之间的间隔难以区分"). Each account is now a card of its own inside the
// provider's: a box with its own edge all round (no left-border accent) and
// room between it and the next. A provider's keys, a balance a line, stay
// lines. A right-click inside an account's box is still that account's
// menu, not the whole card's.
//
// Table: a column is what windows are of, not their names. An Antigravity
// account whose pools were read has "Gemini · 5 hours" and "Gemini · 7
// days"; they went in Other beside a Gemini column that accounts without
// pools filled. They go in the Gemini column now, Claude & GPT's in
// Claude's, and a vendor's "Weekly" in the 7 days column, each cell naming
// a window its head doesn't. Both engines, every language, wide and
// narrow; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const at = (h) => new Date(Date.now() + h * 36e5).toISOString();
const w = (name, used, h) => ({ name, used, resetsAt: at(h) });
const model = (name, family, pool, used, h) => ({ name, family, ...(pool ? { pool } : {}), used, resetsAt: at(h) });
const pool = (p, name, used, h) => ({ name, pool: p, used, resetsAt: at(h) });
const AG = { provider: "antigravity", name: "Antigravity", icon: "antigravity-color", plan: "Google AI Pro" };
// as magpie reports them: one account whose quota summary was read (its
// models draw on pools, each pool's own windows beside them), two whose
// summary wasn't (their models' own windows, by family)
const pooled = { ...AG, user: "pooled@example.com", windows: [
  model("Claude Sonnet 4.6", "Claude", "Claude & GPT", 30, 3), model("GPT-OSS 120B (Medium)", "GPT-OSS", "Claude & GPT", 30, 3),
  model("Gemini 3 Flash", "Gemini", "Gemini", 11, 2), model("Gemini 3.1 Pro (High)", "Gemini", "Gemini", 11, 2),
  pool("Gemini", "7 days", 23, 100), pool("Gemini", "5 hours", 11, 2),
  pool("Claude & GPT", "7 days", 64, 90), pool("Claude & GPT", "5 hours", 30, 3),
] };
const bare = (user, g) => ({ ...AG, user, windows: [
  model("Gemini 3 Flash", "Gemini", "", g, 4), model("Gemini 3.1 Pro (High)", "Gemini", "", g, 4),
  model("Claude Sonnet 4.6", "Claude", "", 5, 4), model("GPT-OSS 120B (Medium)", "GPT-OSS", "", 5, 4),
] });
const quotas = [
  pooled, bare("bare1@example.com", 40), bare("bare2@example.com", 0),
  { provider: "kimi", name: "Kimi", icon: "kimi", user: "k1@example.com", windows: [w("Weekly", 42, 70)] },
  { provider: "zai", name: "Z.ai", icon: "zai", user: "z1@example.com", windows: [w("5 hours", 64, 1), w("Weekly", 12, 70)] },
  { provider: "codex", name: "Codex", kind: "subscription", icon: "codex-color", plan: "Plus", user: "c1@example.com", windows: [w("5 hours", 20, 2), w("7 days", 40, 90)] },
  { provider: "codex", name: "Codex", kind: "subscription", icon: "codex-color", plan: "Plus", user: "c2@example.com", windows: [w("5 hours", 90, 2), w("7 days", 10, 90)] },
  // a provider's keys: a balance a line
  { provider: "openrouter", name: "OpenRouter", icon: "openrouter", user: "key-a", windows: [], balance: "$4.00" },
  { provider: "openrouter", name: "OpenRouter", icon: "openrouter", user: "key-b", windows: [], balance: "$9.50" },
];

function serve(lang) {
  const settings = { theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd" };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/usage/quotas") return json(quotas);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const TABLE = { en: "Table", zh: "表格", "zh-TW": "表格", ja: "表", de: "Tabelle" };
const shots = process.env.SHOTS;

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}px: each account a card of its own; a column is what its windows are of`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang));
        await page.goto("http://magpie.test/?view=usage");
        await page.waitForSelector('#subscriptionUsage .subscription-card[data-provider="antigravity"] .subscription-acct');

        // --- cards: an account a box of its own
        const ag = page.locator('#subscriptionUsage .subscription-card[data-provider="antigravity"]');
        const boxes = ag.locator(".subscription-acct");
        assert.equal(await boxes.count(), 3, "a box an account");
        const look = await ag.evaluate((card) => {
          const bs = [...card.querySelectorAll(":scope > .subscription-acct")];
          const cs = (e) => getComputedStyle(e);
          return {
            accounts: bs.map((b) => b.querySelectorAll(".subscription-account").length),
            firsts: bs.map((b) => b.firstElementChild.classList.contains("subscription-account")),
            card: cs(card).backgroundColor,
            boxes: bs.map((b) => {
              const s = cs(b), r = b.getBoundingClientRect();
              return { bg: s.backgroundColor, radius: parseFloat(s.borderTopLeftRadius), top: r.top, bottom: r.bottom, left: r.left, right: r.right,
                widths: [s.borderTopWidth, s.borderRightWidth, s.borderBottomWidth, s.borderLeftWidth], colors: new Set([s.borderTopColor, s.borderRightColor, s.borderBottomColor, s.borderLeftColor]).size };
            }),
            inside: (() => { const r = card.getBoundingClientRect(); return bs.every((b) => { const x = b.getBoundingClientRect(); return x.left >= r.left && x.right <= r.right + 0.5; }); })(),
          };
        });
        assert.deepEqual(look.accounts, [1, 1, 1], "each box holds one account");
        assert.deepEqual(look.firsts, [true, true, true], "each box starts with its account's name");
        assert.ok(look.inside, "the boxes stay inside the provider's card");
        for (const [i, b] of look.boxes.entries()) {
          assert.ok(b.widths.every((x) => parseFloat(x) >= 1), `box ${i} has an edge all round: ${b.widths}`);
          assert.equal(new Set(b.widths).size, 1, "no side is thicker (no left-border accent)");
          assert.equal(b.colors, 1, "one edge colour all round");
          assert.ok(b.radius > 0, "rounded, as a card");
          assert.notEqual(b.bg, look.card, "its ground tells it from the provider's card");
          if (i) assert.ok(b.top - look.boxes[i - 1].bottom >= 4, `room between boxes ${i - 1} and ${i}: ${b.top - look.boxes[i - 1].bottom}px`);
        }
        // the keys stay lines
        const keys = page.locator('#subscriptionUsage .subscription-card[data-provider="openrouter"]');
        assert.equal(await keys.locator(".subscription-acct").count(), 0, "a provider's keys are no boxes");
        assert.equal(await keys.locator(":scope > .subscription-account").count(), 2);
        // a single account's card has no box in it
        assert.equal(await page.locator('#subscriptionUsage .subscription-card[data-provider="kimi"] .subscription-acct').count(), 0);
        if (shots) await page.screenshot({ path: path.join(shots, `cards-${engine}-${lang}-${width}.png`), fullPage: true });

        // a right-click in the second account's box is that account's menu
        const hideOne = await page.evaluate(() => t("Hide this account from Overview"));
        await boxes.nth(1).locator(".quota-windows").click({ button: "right" });
        await page.locator(".row-menu").waitFor();
        assert.equal((await page.locator(".row-menu .rm-item").first().textContent()).trim(), hideOne);
        await page.keyboard.press("Escape");

        // --- the table
        await page.locator("#quotaView .opt", { hasText: TABLE[lang] }).click();
        await page.waitForSelector("#subscriptionUsage .qt tbody tr");
        const table = await page.evaluate(() => {
          const heads = [...document.querySelectorAll(".qt thead th")].map((x) => x.textContent.replace(/[↑↓]/g, "").trim());
          const rows = Object.fromEntries([...document.querySelectorAll(".qt tbody tr")].map((tr) => [
            tr.querySelector(".qt-acct").textContent.trim(),
            [...tr.children].slice(2).map((td) => [...td.querySelectorAll(".qt-win")].map((x) => x.querySelector(".qt-fig").textContent.replace(/\s+/g, " ").trim())),
          ]));
          return { heads, rows };
        });
        const tr = (s) => page.evaluate((x) => t(x), s);
        const [five, week] = [await tr("5 hours"), await tr("7 days")];
        const col = (name) => table.heads.indexOf(name) - 2;
        assert.ok(col("Gemini") >= 0 && col("Claude") >= 0, `${table.heads}`);
        // the pooled account's Gemini windows are in the Gemini column, named
        const pg = table.rows["pooled@example.com"];
        assert.deepEqual(pg[col("Gemini")], [`Gemini · ${five} 11%`, `Gemini · ${week} 23%`], `${JSON.stringify(pg)}`);
        assert.deepEqual(pg[col("Claude")], [`Claude & GPT · ${five} 30%`, `Claude & GPT · ${week} 64%`]);
        // the others' families in the same column, as they were
        assert.deepEqual(table.rows["bare1@example.com"][col("Gemini")], ["40%"]);
        // "Weekly" goes in the 7 days column, named; "7 days" unnamed
        assert.ok(col(week) >= 0 && !table.heads.includes(await tr("Weekly")), `${table.heads}`);
        assert.deepEqual(table.rows["k1@example.com"][col(week)], [`${await tr("Weekly")} 42%`]);
        assert.deepEqual(table.rows["c1@example.com"][col(week)], ["40%"]);
        // nothing of these left over for Other
        const other = await tr("Other");
        if (table.heads.includes(other)) {
          for (const u of ["pooled@example.com", "k1@example.com", "z1@example.com"])
            assert.deepEqual(table.rows[u][col(other)], [], `${u}'s Other: ${table.rows[u][col(other)]}`);
        }
        // sorting by the Gemini column reads the pooled row by its fuller window
        await page.locator(".qt thead th .qt-sort", { hasText: "Gemini" }).click();
        const firstRow = await page.$eval(".qt tbody tr:nth-child(1) .qt-acct", (x) => x.textContent.trim());
        assert.equal(firstRow, "bare1@example.com", "40% before the pooled row's 23%");
        if (shots) await page.screenshot({ path: path.join(shots, `table-${engine}-${lang}-${width}.png`), fullPage: true });

        const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
        assert.ok(over <= 0, `the page scrolls sideways by ${over}px`);
        assert.deepEqual(errors, []);
      });
    }
  }
}
