// An app magpie is added to by a link of its own (Cindy, Lody, AionUi, Cherry Studio) is a row like the
// others on the Agents page (the owner: its wide button, with no line under
// the name, sat apart from every other): a line under its name says whether
// magpie is added, and its button stands where the others' model picker
// does, as wide and lined up with it.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

// connected when Cindy has magpie, so neither is folded under Show more
const crush = (wired) => ({
  id: "crush", name: "Crush", path: "/test/crush.json", icon: "crush", wired,
  fields: [{ key: "model", label: "model", value: wired ? "magpie/deepseek-flash" : "", options: [{ value: "magpie/deepseek-flash", label: "DeepSeek Flash", ref: "deepseek/deepseek-flash" }] }],
});
const cindy = (added) => ({ id: "cindy", name: "Cindy", path: "", icon: "cindy", import: "cindy://import?x=1", added, fields: [] });
// AionUi's link as magpie makes it (agent.AionUiLink), its status the
// other way round from Cindy's so each run has both
const AIONUI_LINK = "aionui://add-provider?api_key=magpie-aionui&base_url=http%3A%2F%2F127.0.0.1%3A3425%2Fv1&name=magpie&platform=custom";
const aionui = (added) => ({ id: "aionui", name: "AionUi", path: "", icon: "aionui", import: AIONUI_LINK, added, fields: [] });
// Cherry Studio's link as magpie makes it (agent.CherryStudioLink)
const CHERRY_LINK = "cherrystudio://providers/api-keys?v=1&data=eyJpZCI6Im1hZ3BpZSIsImJhc2VVcmwiOiJodHRwOi8vMTI3LjAuMC4xOjM0MjUvdjEiLCJhcGlLZXkiOiJtYWdwaWUtY2hlcnJ5c3R1ZGlvIiwibmFtZSI6Im1hZ3BpZSIsInR5cGUiOiJvcGVuYWkifQ%3D%3D";
const cherry = (added) => ({ id: "cherrystudio", name: "Cherry Studio", path: "", icon: "cherrystudio-color", import: CHERRY_LINK, added, fields: [] });

async function serve(route, lang, added, ctl = {}) {
  const url = new URL(route.request().url());
  const json = (data) => route.fulfill({ json: data });
  if (url.pathname === "/boot.js") {
    return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false,textSize:100};` });
  }
  if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
  if (url.pathname === "/api/state") return json({ agents: [crush(added), cindy(added), aionui(!added), cherry(added), { id: "lody", name: "Lody", path: "", icon: "lody", import: "lody://provider/import?v=1&data=fixture", fields: [] }], profiles: [], settings: { lang, theme: "light", textSize: 100 } });
  if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
  if (url.pathname === "/api/plugins") return json({ plugins: [] });
  // nothing here opens the app's links (agent.NoLinkHandlerError)
  if (url.pathname === "/api/open" && ctl.noHandler) return route.fulfill({ status: 400, json: { error: "nothing on this computer opens Cindy's links (cindy://)", code: "no_link_handler", app: "Cindy", exe: ctl.exe || "", said: LINK_SAID + (ctl.exe ? " ({exe})" : "") } });
  if (url.pathname.startsWith("/api/")) return json({});
  const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
  const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
  await route.fulfill({ body: await fs.readFile(file), contentType });
}

const SAID = {
  en: [/^Not added · Cindy asks to add magpie/, /^Added · magpie is a provider in Cindy/],
  zh: [/^未添加 · /, /^已添加 · /],
  "zh-TW": [/^未新增 · /, /^已新增 · /],
  ja: [/^未追加 · /, /^追加済み · /],
  de: [/^Nicht hinzugefügt · /, /^Hinzugefügt · /],
};
// Lody has no status magpie can read: its line asks to confirm in Lody, in
// the reader's language, and never says added or not
const UNKNOWN = {
  en: "Opens Lody to add magpie as a provider — confirm it there",
  zh: "打开 Lody 添加 magpie 供应商，在其中确认",
  "zh-TW": "開啟 Lody 新增 magpie 供應商，在其中確認",
  ja: "Lody を開いて magpie をプロバイダとして追加します。そちらで確認してください",
  de: "Öffnet Lody, um magpie als Anbieter hinzuzufügen — dort bestätigen",
};

// what a click on Add magpie became, in the reader's language (sungt,
// 10-29: a link nothing opened did nothing, and nothing said so)
const OPENED = {
  en: "Sent to Cindy: confirm adding magpie there",
  zh: "已交给 Cindy：请在其中确认添加 magpie",
  "zh-TW": "已交給 Cindy：請在其中確認新增 magpie",
  ja: "Cindy に渡しました。そちらで magpie の追加を確認してください",
  de: "An Cindy übergeben: dort das Hinzufügen von magpie bestätigen",
};
// the i18n.js key api.go's linkSaid sends
const LINK_SAID = "Nothing on this computer opens {app}'s links, so {app} didn't open: open {app} once or install it again, then click Add magpie again";
const NO_HANDLER = {
  en: "Nothing on this computer opens Cindy's links, so Cindy didn't open: open Cindy once or install it again, then click Add magpie again",
  zh: "这台电脑上没有程序能打开 Cindy 的链接，所以 Cindy 没有打开：先打开一次 Cindy 或重新安装，再点「添加 magpie」",
  "zh-TW": "這台電腦上沒有程式能開啟 Cindy 的連結，所以 Cindy 沒有開啟：先開啟一次 Cindy 或重新安裝，再點「新增 magpie」",
  ja: "このコンピュータには Cindy のリンクを開くアプリがないため、Cindy は開きませんでした。Cindy を一度起動するか入れ直してから、もう一度「magpie を追加」を押してください",
  de: "Auf diesem Computer öffnet nichts die Links von Cindy, daher wurde Cindy nicht geöffnet: Cindy einmal starten oder neu installieren, dann erneut auf „magpie hinzufügen“ klicken",
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(SAID)) {
    for (const added of [false, true]) {
      test(`${engine} ${lang} ${added ? "added" : "not added"}: import rows show known or unknown status and open their app`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await browser.newPage({ viewport: { width: 960, height: 600 } });
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${added}-import-row.png`) });
          }
          await browser.close();
        });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const ctl = {};
        await page.route("**/*", (route) => serve(route, lang, added, ctl));
        await page.goto("http://magpie.test/");
        await page.waitForLoadState("networkidle");

        const more = page.locator('.agent-more[aria-expanded="false"]');
        if (await more.count()) await more.click();
        const lody = page.locator('.row.agent[data-id="lody"]');
        await lody.scrollIntoViewIfNeeded();
        await lody.locator(".ag-st-t").waitFor();
        const hint = await lody.locator(".ag-st-t").innerText();
        assert.equal(hint, UNKNOWN[lang]);
        for (const said of Object.values(SAID).flat()) assert.doesNotMatch(hint, said);
        assert.equal(await lody.locator(".ag-st").evaluate((e) => e.classList.contains("on")), false);
        const lodyOpened = page.waitForRequest((r) => r.url().includes("/api/open"));
        await lody.locator(".ag-start").click();
        assert.equal(JSON.parse((await lodyOpened).postData()).url, "lody://provider/import?v=1&data=fixture");

        const row = page.locator('.row.agent[data-id="cindy"]');
        await row.locator(".ag-st").waitFor();
        assert.match(await row.locator(".ag-st-t").innerText(), SAID[lang][added ? 1 : 0]);
        assert.equal(await row.locator(".ag-st").evaluate((e) => e.classList.contains("on")), added);
        for (const width of [960, 700]) {
          await page.setViewportSize({ width, height: 600 });
          const box = (sel) => page.locator(sel).evaluate((e) => { const r = e.getBoundingClientRect(); return { x: r.x, right: r.right, width: r.width, y: r.y, bottom: r.bottom }; });
          // the rows settle a moment after a resize (under load one was
          // caught mid-way, 194.4px beside 193.4px), so wait for them to line
          // up before calling them apart
          let pick, link;
          for (const until = Date.now() + 3000; ;) {
            pick = await box('.row.agent[data-id="crush"] > .ag-start');
            link = await box('.row.agent[data-id="cindy"] > .ag-start');
            if ((Math.abs(pick.right - link.right) < 1 && Math.abs(pick.width - link.width) < 1) || Date.now() > until) break;
            await page.waitForTimeout(50);
          }
          assert(Math.abs(pick.right - link.right) < 1 && Math.abs(pick.width - link.width) < 1, `Cindy's button lines up with the model picker at ${width}px: ${JSON.stringify({ pick, link })}`);
          const who = await box('.row.agent[data-id="cindy"] > .who');
          assert(link.x >= who.right, `the button stays clear of the name at ${width}px`);
        }
        // AionUi: its own icon, its known status in the reader's language,
        // and its add-provider link as the button opens it
        const aion = page.locator('.row.agent[data-id="aionui"]');
        await aion.locator(".ag-st").waitFor();
        const aionSaid = await aion.locator(".ag-st-t").innerText();
        assert.match(aionSaid, new RegExp(SAID[lang][added ? 0 : 1].source.replace("Cindy", "AionUi")));
        assert.equal(await aion.locator(".ag-st").evaluate((e) => e.classList.contains("on")), !added);
        assert.equal(await aion.locator('.ic[data-icon="aionui"]').count() > 0, true, "AionUi's row has its icon");
        const aionIcon = await page.evaluate(async () => (await fetch("/icons/aionui.svg")).status);
        assert.equal(aionIcon, 200);
        const aionOpened = page.waitForRequest((r) => r.url().includes("/api/open"));
        await aion.locator(".ag-start").click();
        assert.equal(JSON.parse((await aionOpened).postData()).url, AIONUI_LINK);
        // Cherry Studio: the same, with its own icon and link
        const cherryRow = page.locator('.row.agent[data-id="cherrystudio"]');
        await cherryRow.locator(".ag-st").waitFor();
        assert.match(await cherryRow.locator(".ag-st-t").innerText(), new RegExp(SAID[lang][added ? 1 : 0].source.replace("Cindy", "Cherry Studio")));
        assert.equal(await cherryRow.locator(".ag-st").evaluate((e) => e.classList.contains("on")), added);
        assert.equal(await cherryRow.locator('.ic[data-icon="cherrystudio-color"]').count() > 0, true, "Cherry Studio's row has its icon");
        assert.equal(await page.evaluate(async () => (await fetch("/icons/cherrystudio-color.svg")).status), 200);
        const cherryOpened = page.waitForRequest((r) => r.url().includes("/api/open"));
        await cherryRow.locator(".ag-start").click();
        assert.equal(JSON.parse((await cherryOpened).postData()).url, CHERRY_LINK);

        // narrow: no page scroll sideways, and every import row keeps its
        // line and button inside the window
        await page.setViewportSize({ width: 420, height: 700 });
        // the header's segments settle a moment after a resize, so wait for
        // the state rather than reading it at once
        await page.waitForFunction(() => document.documentElement.scrollWidth <= window.innerWidth, null, { timeout: 3000 }).catch(async () => {
          assert.fail(`sideways scroll at 420px: ${await page.evaluate(() => document.documentElement.scrollWidth)}`);
        });
        for (const id of ["lody", "cindy", "aionui", "cherrystudio"]) {
          const r = page.locator(`.row.agent[data-id="${id}"]`);
          for (const sel of [".ag-start", ".ag-st-t"]) {
            const b = await r.locator(sel).evaluate((e) => { const x = e.getBoundingClientRect(); return { left: x.left, right: x.right, width: x.width }; });
            assert(b.width > 0 && b.left >= 0 && b.right <= 420 + 0.5, `${id} ${sel} inside the window at 420px: ${JSON.stringify(b)}`);
          }
        }
        await page.setViewportSize({ width: 960, height: 600 });
        // a click opens the app's link, the row's one control
        const opened = page.waitForRequest((r) => r.url().includes("/api/open"));
        await row.locator(".ag-start").click();
        const req = await opened;
        assert.equal(JSON.parse(req.postData() || "{}").url, "cindy://import?x=1");
        // and says what became of it: opened, or why nothing opened
        const said = async (text, kind) => {
          await page.waitForFunction(([want, k]) => { const s = document.querySelector("#status"); return s && s.textContent === want && s.classList.contains(k); }, [text, kind], { timeout: 3000 }).catch(async () => {
            assert.fail(`status ${JSON.stringify(await page.locator("#status").evaluate((s) => [s.textContent, s.className]))}, want ${kind} ${JSON.stringify(text)}`);
          });
        };
        await said(OPENED[lang], "ok");
        ctl.noHandler = true;
        await row.locator(".ag-start").click();
        await said(NO_HANDLER[lang], "err");
        ctl.exe = "C:\\gone\\Cindy.exe";
        await row.locator(".ag-start").click();
        await said(NO_HANDLER[lang] + " (C:\\gone\\Cindy.exe)", "err");
      });
    }
  }
}
