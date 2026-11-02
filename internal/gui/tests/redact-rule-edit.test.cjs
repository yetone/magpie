// Run with Node's test runner and Playwright on the module path; see README.md.
// A masking rule is edited in place (nf1qaz2w on Discord: 自定义脱敏规则现在
// 只有移除，能否增加一个编辑功能): each rule's row has Edit beside Remove.
// Edit opens the rule in its own row, where it stands, with its name, prefix
// or regex and what it matches filled in, the fields on a line under its
// words (its name, where its explanation rests out of sight for the hover
// card), none on top of another. Cancel changes nothing and sends nothing.
// A pattern the gateway refuses is said in the row, what was typed kept and
// the rule as it was; a good one is saved in the same place. No click moves
// the page. In every language, in a wide window and a narrow one.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const words = {
  en: { edit: "Edit", save: "Save", cancel: "Cancel", matches: "Matches ns_[0-9a-f]{12}" },
  zh: { edit: "编辑", save: "保存", cancel: "取消", matches: "匹配 ns_[0-9a-f]{12}" },
  "zh-TW": { edit: "編輯", save: "儲存", cancel: "取消", matches: "匹配 ns_[0-9a-f]{12}" },
  ja: { edit: "編集", save: "保存", cancel: "キャンセル", matches: "ns_[0-9a-f]{12} に一致" },
  de: { edit: "Bearbeiten", save: "Speichern", cancel: "Abbrechen", matches: "Entspricht ns_[0-9a-f]{12}" },
};

function serve(lang, calls) {
  const s = { lang, theme: "light", redact: true, redactRules: [{ kind: "GATEWAY_KEY", prefix: "oc_sk_" }, { kind: "NS", regex: "ns-[0-9a-f]{12}" }, { kind: "LAST", prefix: "zz_tok_" }] };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data, status = 200) => r.fulfill({ status, json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: s });
    if (url.pathname === "/api/settings") return json(s);
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname === "/api/settings/redact-rule" || url.pathname === "/api/settings/redact-rules") {
      const body = r.request().postDataJSON();
      calls.push({ path: url.pathname, body });
      // as the gateway checks one: a pattern that doesn't compile is refused
      if (body.rule?.regex) {
        try { new RegExp(body.rule.regex); } catch {
          return json({ error: "the masking rule NS has a regular expression that doesn't work: error parsing regexp: missing closing ]: `[0-9a-f`" }, 400);
        }
      }
      if (body.rule) s.redactRules = s.redactRules.map((x, i) => (i === body.at ? { kind: body.rule.kind.toUpperCase(), ...body.rule.regex ? { regex: body.rule.regex } : { prefix: body.rule.prefix } } : x));
      return json(s);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    for (const width of [1100, 420]) {
      const w = words[lang];
      test(`${engine} ${lang} ${width}px: a masking rule is edited in its place`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 1400 } })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], calls = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, calls));
        await watchSettingsReads(page);
        await page.goto("http://magpie.test/?view=settings&tab=privacy");
        const head = page.locator("#redactList .row.rule-row").first();
        await head.waitFor();
        await settingsDrawn(page); // the rows found are the ones that stay (#1653)
        // the rules, in order, from the row that adds one
        const listed = () => head.evaluate((h) => {
          const out = [];
          for (let n = h.nextElementSibling; n && out.length < 3; n = n.nextElementSibling) out.push({ name: n.querySelector(".name").textContent, sub: n.querySelector(".sub").textContent, editing: n.classList.contains("rule-editing") });
          return out;
        });
        const rule = (name) => page.locator("#redactList .row").filter({ has: page.locator(".name", { hasText: new RegExp(`^${name}$`) }) });
        const scroll = () => page.evaluate(() => [scrollX, scrollY, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => e.scrollTop)]);
        const click = async (loc) => {
          // the app undoes a script's scroll, so scroll as a reader does
          // until the target clears the foot
          await page.mouse.move(200, 300);
          for (let i = 0; i < 20; i++) {
            const [b, top] = await Promise.all([loc.boundingBox(), page.evaluate(() => document.querySelector("footer.foot")?.getBoundingClientRect().top ?? innerHeight)]);
            if (b.y >= 0 && b.y + b.height <= top - 4) break;
            await page.mouse.wheel(0, b.y < 0 ? -200 : 200);
            await page.waitForTimeout(80);
          }
          const before = await scroll();
          const b = await loc.boundingBox();
          await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
          await page.waitForTimeout(50);
          assert.deepEqual(await scroll(), before, "the click moved nothing");
        };

        const edit = rule("NS").locator("button.rule-edit");
        assert.equal(await edit.textContent(), w.edit);
        await click(edit);
        const ed = page.locator("#redactList .row.rule-editing");
        await ed.waitFor();
        assert.deepEqual((await listed()).map((x) => [x.name, x.editing]), [["GATEWAY_KEY", false], ["NS", true], ["LAST", false]], "opened where it stands");
        assert.equal(await ed.locator("input.rule-kind").inputValue(), "NS");
        assert.equal(await ed.locator("input.rule-match").inputValue(), "ns-[0-9a-f]{12}");
        assert.equal(await ed.locator(".segs .opt.on").count(), 1);
        assert.equal(await ed.locator("button.rule-save").textContent(), w.save);
        assert.equal(await ed.locator("button.rule-cancel").textContent(), w.cancel);
        // its fields on a line under its words, inside the row, none on top of another
        const m = await ed.evaluate((r) => {
          const box = (e) => { const b = e.getBoundingClientRect(); return { l: b.left, r: b.right, t: b.top, b: b.bottom, mid: (b.top + b.bottom) / 2, w: b.width }; };
          const sub = r.querySelector(".sub"), words = getComputedStyle(sub).getPropertyValue("--hint").trim() === "1" ? r.querySelector(".name") : sub;
          return { row: box(r), sub: box(words), tools: [...r.querySelectorAll(".val input, .val .segs, .val button:not(.opt)")].map((e) => ({ cls: e.className, ...box(e) })), pageW: document.documentElement.scrollWidth, winW: innerWidth };
        });
        assert.deepEqual(m.tools.map((e) => e.cls.split(" ")[0]), ["words", "segs", "words", "text", "text"], "name, prefix or regex, match, Save, Cancel");
        for (const e of m.tools) assert(e.w > 0 && e.l >= m.row.l - 1 && e.r <= m.row.r + 1, `in the row: ${JSON.stringify(m)}`);
        // a wide window has them on one line; a narrow one puts the match,
        // Save and Cancel on a line under the name and Prefix or Regex
        const line = (a, b) => Math.abs(a.mid - b.mid) < 2;
        if (width > 760) for (const e of m.tools) assert(line(e, m.tools[0]), `on one line: ${JSON.stringify(m.tools)}`);
        else {
          assert(line(m.tools[0], m.tools[1]) && line(m.tools[2], m.tools[3]) && line(m.tools[2], m.tools[4]) && m.tools[2].t >= m.tools[0].b, `two lines: ${JSON.stringify(m.tools)}`);
          assert(Math.abs(m.tools[2].l - m.tools[0].l) < 1.5 && Math.abs(m.tools[4].r - m.row.r) < 40, `each from the row's left: ${JSON.stringify(m)}`);
        }
        for (const [i, a] of m.tools.entries()) for (const b of m.tools.slice(i + 1)) {
          assert(a.r <= b.l + 0.5 || b.r <= a.l + 0.5 || a.b <= b.t + 0.5 || b.b <= a.t + 0.5, `none on top of another: ${JSON.stringify(m.tools)}`);
        }
        assert(m.tools[2].w >= 120, `the match field has room: ${m.tools[2].w}`);
        assert(m.tools[0].t >= m.sub.b, "the fields are under the words");
        assert(m.pageW <= m.winW, "the page doesn't scroll sideways");

        // Cancel: what was typed goes, nothing is sent, the rule is as it was
        await ed.locator("input.rule-match").fill("changed_[0-9]+");
        await click(ed.locator("button.rule-cancel"));
        await ed.waitFor({ state: "detached" });
        assert.deepEqual(calls, []);
        const back = await listed();
        assert.deepEqual(back.map((x) => [x.name, x.editing]), [["GATEWAY_KEY", false], ["NS", false], ["LAST", false]]);
        assert.match(back[1].sub, /ns-\[0-9a-f\]\{12\}/);
        await click(edit);
        assert.equal(await ed.locator("input.rule-match").inputValue(), "ns-[0-9a-f]{12}", "Cancel left nothing of the edit");

        // a pattern that doesn't compile: said in the row, what was typed kept
        await ed.locator("input.rule-kind").fill("NS_KEY");
        await ed.locator("input.rule-match").fill("ns_[0-9a-f");
        await click(ed.locator("button.rule-save"));
        await ed.locator(".sub.err").waitFor();
        assert.match(await ed.locator(".sub.err").textContent(), /doesn't work/);
        assert.equal(await ed.locator("input.rule-match").inputValue(), "ns_[0-9a-f");
        assert.equal(await ed.locator("input.rule-kind").inputValue(), "NS_KEY");
        assert.equal(calls.length, 1);
        assert.deepEqual(calls[0], { path: "/api/settings/redact-rule", body: { at: 1, was: { kind: "NS", regex: "ns-[0-9a-f]{12}" }, rule: { kind: "NS_KEY", regex: "ns_[0-9a-f" } } });

        // fixed and saved by Enter: the rule changes where it stands
        await ed.locator("input.rule-match").fill("ns_[0-9a-f]{12}");
        await ed.locator("input.rule-match").press("Enter");
        await ed.waitFor({ state: "detached" });
        assert.deepEqual(calls[1].body, { at: 1, was: { kind: "NS", regex: "ns-[0-9a-f]{12}" }, rule: { kind: "NS_KEY", regex: "ns_[0-9a-f]{12}" } });
        assert.deepEqual(calls.map((c) => c.path), ["/api/settings/redact-rule", "/api/settings/redact-rule"], "one rule sent, not the list");
        const after = await listed();
        assert.deepEqual(after.map((x) => x.name), ["GATEWAY_KEY", "NS_KEY", "LAST"]);
        assert.equal(after[1].sub, w.matches);
        assert.equal(await page.locator("#redactList .row.rule-editing").count(), 0);
        assert.deepEqual(errors, []);
      });
    }
  }
}
