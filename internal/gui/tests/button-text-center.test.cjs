// Run with Node's test runner and Playwright on the module path; see README.md.
// Words in a button sit in its middle (the owner, 11-26: 「按钮中的文字并没有
// 在垂直方向上居中，这是为什么？能够彻底避免这种问题再次发生吗？」). The
// Usage speed table's 检查… sat low in its 20px box: .led-spd-check set
// padding 0, but .text's 3px came later in app.css and won, and its content
// box was shorter than the line. Now every control with one line of words,
// on every page this visits, has that line's middle within ALLOWED of its
// box's middle (btn-align.cjs), and the offenders are listed by where they
// are. Both engines and every language run, as each lays out its own words.
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { buttonLines } = require("./btn-align.cjs");
const { server, visitPages } = require("./app-pages.cjs");

const ALLOWED = 0.5; // CSS px

const LANGS = process.env.LANGS ? process.env.LANGS.split(",") : ["en", "zh", "zh-TW", "ja", "de"];
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": every button's words sit in its middle", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of LANGS) {
      await t.test(lang, async (st) => {
        const ctx = await browser.newContext({ viewport: { width: 1180, height: 900 }, reducedMotion: "reduce" });
        st.after(() => ctx.close());
        const page = await ctx.newPage();
        page.setDefaultTimeout(8000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang));
        const bad = [], seen = new Set();
        let count = 0, speedCheck = 0;
        await visitPages(page, async (view) => {
          const lines = await page.evaluate(`(${buttonLines.toString()})(document.body)`);
          for (const l of lines) {
            count++;
            if (l.where.includes("led-spd-check")) speedCheck++;
            const key = l.where + " " + JSON.stringify(l.words);
            if (seen.has(key)) continue;
            seen.add(key);
            if (process.env.BTN_LOG) console.log(engine, lang, view, l.d.toFixed(2), l.over.toFixed(2), l.h.toFixed(1), l.line.toFixed(1), l.where, JSON.stringify(l.words));
            if (Math.abs(l.d) > ALLOWED) bad.push(`${view}: ${l.where} ${JSON.stringify(l.words)}: its line sits ${Math.abs(l.d).toFixed(2)}px ${l.d > 0 ? "low" : "high"} in its ${l.h.toFixed(1)}px box (line ${l.line.toFixed(1)}px${l.over ? `, ${l.over.toFixed(2)}px past its padding` : ""})`);
          }
        });
        assert.ok(speedCheck >= 1, "the speed table's Check… was never measured");
        assert.ok(count >= 40, `only ${count} buttons with words were found`);
        assert.deepEqual(bad, [], `${bad.length} buttons with words off their middle by more than ${ALLOWED}px`);
        assert.deepEqual(errors, []);
      });
    }
  });
}

// and where it starts: a rule giving a text button its own size, padding
// or colour holds only while it comes after .text's, which sets them for
// every one at the same weight. .text sat at line 2482 and won over five
// such rules declared before it, .led-spd-check's among them.
test("no text button's own rule comes before .text's", () => {
  const dir = path.resolve(__dirname, process.env.MAGPIE_ASSETS || "../assets");
  const css = fs.readFileSync(path.join(dir, "app.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, (c) => c.replace(/[^\n]/g, " "));
  const rules = [...css.matchAll(/([^{}@;]+)\{([^{}]*)\}/g)].map((m) => ({ line: css.slice(0, m.index).split("\n").length, sels: m[1].split(",").map((s) => s.trim()), body: m[2] }));
  const props = (body) => new Set(body.split(";").map((d) => d.split(":")[0].trim()).filter(Boolean));
  const base = rules.find((r) => r.sels.length === 1 && r.sels[0] === ".text");
  assert.ok(base, "no .text rule in app.css");
  const set = props(base.body);
  // the classes given beside "text" anywhere in the scripts
  const scripts = [...fs.readdirSync(dir).filter((f) => f.endsWith(".js")).map((f) => path.join(dir, f)),
    ...fs.readdirSync(path.join(dir, "plugins")).flatMap((p) => { try { return fs.readdirSync(path.join(dir, "plugins", p)).filter((f) => f.endsWith(".js")).map((f) => path.join(dir, "plugins", p, f)); } catch { return []; } })];
  const beside = new Set();
  for (const f of scripts) {
    for (const m of fs.readFileSync(f, "utf8").matchAll(/["'`]((?:[a-z0-9-]+ )*text(?: [a-z0-9-]+)*)["'`]/g)) {
      for (const c of m[1].split(" ")) if (c !== "text") beside.add(c);
    }
  }
  assert.ok(beside.has("led-spd-check") && beside.size > 50, `only ${beside.size} classes found beside text`);
  const early = [];
  for (const r of rules) {
    if (r.line >= base.line) continue;
    for (const s of r.sels) {
      const m = /^\.([a-z0-9-]+)$/.exec(s);
      if (!m || !beside.has(m[1])) continue;
      const lost = [...props(r.body)].filter((p) => set.has(p));
      if (lost.length) early.push(`app.css:${r.line} ${s} sets ${lost.join(", ")}, and .text's at ${base.line} wins`);
    }
  }
  assert.deepEqual(early, []);
});
