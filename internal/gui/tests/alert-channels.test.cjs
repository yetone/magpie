// Run with Node's test runner and Playwright on the module path; see README.md.
// Telegram and Discord beside Bark (gandli on X): Settings › Usage has a
// Telegram row, where a bot's token and a chat are saved, and a Discord row,
// where a webhook is. The page never has the token or the webhook back:
// after each is saved only dots show (and the chat, which is no secret), and
// neither is in any text of the page. Test sends to that one, Remove takes it
// away. The daily summary shows with any of them set and goes once the last
// is removed. Every language, wide and narrow, both engines; the API is faked
// here, and nothing reaches the network.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn, reach } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const TOKEN = "7123456789:AAHfakeTokenForTestsOnly_abcdefghij";
const HOOK = "https://discord.com/api/webhooks/1311234567890123456/fakeWebhookTokenForTestsOnly-abcdefghijklmnop";

function server(lang, posts) {
  const fixed = {
    theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd", dock: false, dockWindow: false, proxy: "",
    redact: true, redactPersonal: false, redactWords: [], codexWarmup: "", claudeWarmup: "", codexWarmAt: "", claudeWarmAt: "",
    trayUsage: "", trayUsageEvery: 3, vision: "", imageGen: "", version: "0.1.900", dir: "~/.config/magpie",
    gateway: "http://127.0.0.1:3425", proxyNow: "none", proxySource: "none", visionModels: [], imageGenModels: [], lanURLs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false }, turnDoneAlert: 0, featuresOff: [],
  };
  let cur = fixed;
  // as api.go answers: whether each is set, the chat, never a secret; the
  // summary goes with the last channel
  const answer = (next) => {
    if (!next.barkKeySet && !next.telegramSet && !next.discordSet) delete next.barkDaily;
    for (const k of ["barkKeySet", "telegramSet", "discordSet", "telegramChat"]) if (!next[k]) delete next[k];
    cur = next;
    return cur;
  };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false,featuresOff:[]};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: fixed.fx });
    if (url.pathname === "/api/settings/alert-channels") {
      const body = req.postDataJSON();
      posts.push({ path: "channels", ...body });
      const next = { ...cur };
      if ("telegramToken" in body) next.telegramSet = !!body.telegramToken;
      if ("telegramChat" in body) next.telegramChat = body.telegramChat;
      if ("discordWebhook" in body) next.discordSet = !!body.discordWebhook;
      return json(answer(next));
    }
    if (url.pathname === "/api/settings/bark") {
      const body = req.postDataJSON();
      posts.push({ path: "bark", ...body });
      const next = { ...cur };
      if ("daily" in body) next.barkDaily = body.daily;
      return json(answer(next));
    }
    if (url.pathname === "/api/alert-channels/test") { posts.push({ path: "test", ...req.postDataJSON() }); return json({ ok: true }); }
    if (url.pathname === "/api/settings") return json(cur);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { token: "Bot token", chat: "Chat ID", hook: "Webhook URL", daily: "Daily summary", test: "Test", remove: "Remove", on: "On", tg: "Sent to Telegram", dc: "Sent to Discord" },
  zh: { token: "Bot Token", chat: "Chat ID", hook: "Webhook 地址", daily: "每日汇总", test: "测试", remove: "移除", on: "开启", tg: "已发送到 Telegram", dc: "已发送到 Discord" },
  "zh-TW": { token: "Bot Token", chat: "Chat ID", hook: "Webhook 網址", daily: "每日彙總", test: "測試", remove: "移除", on: "開啟", tg: "已傳送到 Telegram", dc: "已傳送到 Discord" },
  ja: { token: "Bot トークン", chat: "チャット ID", hook: "Webhook URL", daily: "毎日のまとめ", test: "テスト", remove: "削除", on: "オン", tg: "Telegram に送信しました", dc: "Discord に送信しました" },
  de: { token: "Bot-Token", chat: "Chat-ID", hook: "Webhook-URL", daily: "Tagesübersicht", test: "Testen", remove: "Entfernen", on: "An", tg: "An Telegram gesendet", dc: "An Discord gesendet" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    for (const width of [900, 380]) {
      test(`${engine} ${lang} ${width}px: Telegram and Discord are set, tried and removed beside Bark`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await (await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" })).newPage();
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-alert-channels.png`) });
          }
          await browser.close();
        });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const posts = [];
        await page.route("**/*", server(lang, posts));
        await watchSettingsReads(page);
        await page.goto("http://magpie.test/?view=settings&tab=usage");
        await settingsDrawn(page);
        const w = words[lang];
        const posted = async (n) => { for (let i = 0; i < 100 && posts.length < n; i++) await page.waitForTimeout(20); assert.equal(posts.length, n, "posts " + JSON.stringify(posts)); return posts[n - 1]; };
        const secretsGone = async () => {
          const all = await page.evaluate(() => document.documentElement.outerHTML + JSON.stringify([...document.querySelectorAll("input")].map((i) => i.value)));
          assert.ok(!all.includes(TOKEN) && !all.includes("AAHfake"), "the bot token is in the page");
          assert.ok(!all.includes("fakeWebhookToken"), "the webhook is in the page");
        };

        const token = page.locator("#telegramBox input[type=password]"), chat = page.locator("#telegramBox input[type=text]");
        const hook = page.locator("#discordBox input[type=password]");
        await token.waitFor();
        assert.equal((await page.locator("#telegramRow .name").textContent()).trim(), "Telegram");
        assert.equal((await page.locator("#discordRow .name").textContent()).trim(), "Discord");
        assert.equal(await page.locator("#telegramRow .sub, #discordRow .sub").count(), 0, "no words under the names");
        assert.equal(await token.getAttribute("placeholder"), w.token);
        assert.equal(await chat.getAttribute("placeholder"), w.chat);
        assert.equal(await hook.getAttribute("placeholder"), w.hook);
        assert.equal(await page.locator("#barkDailyRow").isHidden(), true, "no summary with nothing set");

        // a token with no chat saves nothing
        await reach(page, page.locator("#telegramBox .bark-save"));
        await token.fill(TOKEN);
        await page.locator("#telegramBox .bark-save").click();
        await page.waitForTimeout(150);
        assert.equal(posts.length, 0, "saved with no chat");
        assert.equal(await page.evaluate(() => document.activeElement && document.activeElement.type), "text", "the empty chat is asked for");
        await chat.fill("123456789");
        await chat.press("Enter");
        assert.deepEqual(await posted(1), { path: "channels", telegramToken: TOKEN, telegramChat: "123456789" });

        // the token is written and never shown: the chat, dots, Test and Remove
        await page.locator("#telegramBox .bark-key").waitFor();
        assert.equal(await page.locator("#telegramBox input").count(), 0, "no field left");
        assert.equal((await page.locator("#telegramBox .bark-server").textContent()).trim(), "123456789");
        assert.deepEqual(await page.locator("#telegramBox button").allTextContents(), [w.test, w.remove]);
        await secretsGone();

        // the daily summary is there with Telegram alone
        await page.locator("#barkDailyRow").waitFor({ state: "visible" });
        assert.equal((await page.locator("#barkDailyRow .name").textContent()).trim(), w.daily);
        await page.locator("#barkDailySegs .opt").nth(1).click();
        assert.deepEqual(await posted(2), { path: "bark", daily: "21:00" });
        await page.locator("#barkDailySegs input.at").waitFor();

        await page.locator("#telegramBox .bark-test").click();
        assert.deepEqual(await posted(3), { path: "test", to: "telegram" });
        await page.getByText(w.tg).first().waitFor();

        // Discord: the webhook, then dots, Test and Remove
        await reach(page, hook);
        await hook.fill(HOOK);
        await page.locator("#discordBox .bark-save").click();
        assert.deepEqual(await posted(4), { path: "channels", discordWebhook: HOOK });
        await page.locator("#discordBox .bark-key").waitFor();
        assert.equal(await page.locator("#discordBox input").count(), 0, "no field left");
        assert.equal(await page.locator("#discordBox .bark-server").count(), 0, "the webhook is shown");
        await secretsGone();
        await page.locator("#discordBox .bark-test").click();
        assert.deepEqual(await posted(5), { path: "test", to: "discord" });
        await page.getByText(w.dc).first().waitFor();

        const over = await page.evaluate(() => {
          const rows = [...document.querySelectorAll("#telegramRow, #discordRow, #barkDailyRow")];
          return rows.flatMap((row) => { const r = row.getBoundingClientRect(); return [...row.querySelectorAll("*")].filter((e) => e.getBoundingClientRect().right > Math.max(r.right, innerWidth) + 1).map((e) => e.className || e.id); });
        });
        assert.deepEqual(over, [], "nothing past the edge");

        // Remove one: the other keeps the summary; remove both: it goes
        await reach(page, page.locator("#telegramBox .bark-remove"));
        await page.locator("#telegramBox .bark-remove").click();
        assert.deepEqual(await posted(6), { path: "channels", telegramToken: "", telegramChat: "" });
        await token.waitFor();
        assert.equal(await page.locator("#barkDailyRow").isHidden(), false, "the summary went with Discord still set");
        assert.equal(await page.locator("#barkDailySegs input.at").inputValue(), "21:00");
        await reach(page, page.locator("#discordBox .bark-remove"));
        await page.locator("#discordBox .bark-remove").click();
        assert.deepEqual(await posted(7), { path: "channels", discordWebhook: "" });
        await hook.waitFor();
        assert.equal(await page.locator("#barkDailyRow").isHidden(), true, "a summary with nowhere to go");
        assert.deepEqual(errors, []);
      });
    }
  }
}
